import {useCallback, useEffect, useMemo, useRef, useState} from 'react';
import type {LogEntry, Provider, StatusBucketStats, UsageAnalysisResponse} from '../types';
import {API_BASE, formatBytes, formatCostMicros, formatTokenCount, isTrafficLogCanceled, localDateToRFC3339, providerUsageLabel} from '../lib';
import {downloadCSV, UsageDonutChart, UsageStatusChart} from './charts';
import {ProviderRefLabel} from './provider_label';
import {IconSelect} from './selects';
import type {IconSelectOption} from './selects';
import {providerSelectOptions} from './provider_select';

type AnalysisState = {
  key: string;
  data: UsageAnalysisResponse | null;
  error: string;
  loading: boolean;
  refreshing: boolean;
  fetchedAt: Date | null;
};

/**
 * 请求明细分析：仅按区间重新拉取。轮询/父级刷新不会重置它，
 * 同一区间的静默刷新保留旧数据，避免页面每 5 秒清空再重绘。
 *
 * 分析明细一次就是整段区间的每一行（上万行、压缩后仍有数百 KB），所以同一区间
 * 在复用窗口内重复进入「请求 / 上下文 / 性能」时直接用内存里的结果，只有手动
 * 刷新或窗口过期才重新下载。
 */
const ANALYSIS_REUSE_MS = 60_000;

export function useUsageAnalysis(from: string, to: string, enabled: boolean) {
  const key = `${from}|${to}`;
  const [state, setState] = useState<AnalysisState>({key: '', data: null, error: '', loading: false, refreshing: false, fetchedAt: null});
  const abortRef = useRef<AbortController | null>(null);
  const versionRef = useRef(0);
  const stateRef = useRef(state);
  stateRef.current = state;

  const load = useCallback((silent: boolean) => {
    if (!enabled) return;
    const current = stateRef.current;
    if (!silent && current.key === key && current.data && current.fetchedAt && Date.now() - current.fetchedAt.getTime() < ANALYSIS_REUSE_MS) return;
    const version = ++versionRef.current;
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    setState(prev => prev.key === key
      ? {...prev, error: '', loading: !silent && !prev.data, refreshing: silent}
      : {key, data: null, error: '', loading: true, refreshing: false, fetchedAt: null});
    const params = new URLSearchParams({from: localDateToRFC3339(from), to: localDateToRFC3339(to, true)});
    void fetch(`${API_BASE}/__usage-analysis?${params}`, {credentials: 'same-origin', signal: controller.signal})
      .then(async response => {
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        return await response.json() as UsageAnalysisResponse;
      })
      .then(data => {
        if (version !== versionRef.current) return;
        setState({key, data, error: '', loading: false, refreshing: false, fetchedAt: new Date()});
      })
      .catch(error => {
        if (version !== versionRef.current || controller.signal.aborted) return;
        setState(prev => ({...prev, key, error: String(error), loading: false, refreshing: false}));
      });
  }, [enabled, from, to, key]);

  useEffect(() => {
    if (!enabled) {
      abortRef.current?.abort();
      return;
    }
    load(false);
    return () => abortRef.current?.abort();
  }, [enabled, load]);

  return {...state, stale: state.key !== key, reload: () => load(true)};
}

export function percentile(values: number[], p: number) {
  if (!values.length) return null;
  const sorted = [...values].sort((a, b) => a - b);
  return sorted[Math.max(0, Math.ceil(p * sorted.length) - 1)];
}
const sum = (rows: LogEntry[], field: 'inputTokens' | 'outputTokens' | 'cacheTokens' | 'cacheWriteTokens') => rows.reduce((n, r) => n + (r[field] || 0), 0);
const time = (n: number | null) => n == null ? '—' : `${n.toLocaleString()} ms`;
const tokens = (r: LogEntry) => r.inputTokens + r.outputTokens;
function Tile({label, value, note}: {label: string; value: string; note: string}) {
  return <div className="usage-stat"><span>{label}</span><strong>{value}</strong><small>{note}</small></div>;
}
function Pager({page, count, onChange}: {page: number; count: number; onChange: (page: number) => void}) {
  return <div className="usage-table-foot"><span>{count.toLocaleString()} 项</span><div><button className="mini-btn" disabled={!page} onClick={() => onChange(page - 1)}>上一页</button><span>{count ? page + 1 : 0} / {Math.ceil(count / 20)}</span><button className="mini-btn" disabled={(page + 1) * 20 >= count} onClick={() => onChange(page + 1)}>下一页</button></div></div>;
}
function csv(value: string | number) { const text = String(value); return `"${(/^[=+@\-\t\r]/.test(text) ? "'" : '') + text.replace(/"/g, '""')}"`; }

/**
 * 单条请求的成本格。未计价画「—」，下面跟一行「未计价」小字：
 * 空白会被读成「免费」，而这个功能上线前的日志本来就没有价格行。
 */
function CostCell({log}: {log: LogEntry}) {
  return <td className="usage-cost-cell" title={log.priced ? '按请求结束时价格表算出，之后改价不回算' : '这条请求没有价格行（或早于成本功能）：是「未计价」，不是免费'}>
    {log.priced ? formatCostMicros(log.costMicros) : '—'}
    {log.priced ? null : <small className="usage-cell-caption">未计价</small>}
  </td>;
}

function statusBuckets(rows: LogEntry[]): StatusBucketStats[] {
  const counts = new Map<string, number>();
  for (const row of rows) {
    // 499 是客户端主动取消，单独成桶；归进 4xx 会让状态环形图和成功率
    // 把正常 Stop/切会话显示成异常。
    const bucket = isTrafficLogCanceled(row) ? '499' : row.status ? `${Math.floor(row.status / 100)}xx` : '未知';
    counts.set(bucket, (counts.get(bucket) || 0) + 1);
  }
  return [...counts].sort(([a], [b]) => a.localeCompare(b)).map(([label, requestCount]) => ({class: label, requestCount}));
}

const LATENCY_BUCKETS = [
  {label: '≤ 1s', max: 1000},
  {label: '1–3s', max: 3000},
  {label: '3–10s', max: 10000},
  {label: '10–30s', max: 30000},
  {label: '≥ 30s', max: Infinity},
];

function LatencyDistribution({rows}: {rows: LogEntry[]}) {
  const latencies = rows.map(r => r.latencyMs || 0).filter(n => n > 0);
  const total = latencies.length;
  const buckets = LATENCY_BUCKETS.map((bucket, index) => {
    const lower = index === 0 ? 0 : LATENCY_BUCKETS[index - 1].max;
    const count = latencies.filter(n => n > lower && n <= bucket.max).length;
    return {...bucket, count, pct: total ? count / total * 100 : 0};
  });
  const median = percentile(latencies, .5);
  const p90 = percentile(latencies, .9);
  const fast = latencies.filter(n => n <= 10000).length;
  const slow = latencies.filter(n => n > 30000).length;
  return <div className="usage-chart-card usage-latency-card">
    <div className="usage-section-title-row">
      <div>
        <div className="usage-section-title">耗时分布</div>
        <div className="usage-section-subtitle">中位数 {time(median)} · 共 {total.toLocaleString()} 个有效样本</div>
      </div>
    </div>
    <div className="usage-latency-bars">
      {buckets.map((bucket, index) => <div className="usage-latency-row" key={bucket.label}>
        <span className="usage-latency-label">{bucket.label}</span>
        <span className="usage-latency-track"><i className={`tone-${index}`} style={{width: `${Math.max(bucket.count ? 1.5 : 0, bucket.pct)}%`}} /></span>
        <b>{bucket.count.toLocaleString()}</b>
        <small>{bucket.pct.toFixed(0)}%</small>
      </div>)}
    </div>
    {!total && <p className="usage-empty compact">当前筛选下暂无有效耗时样本</p>}
    {!!total && <div className="usage-latency-foot">
      <div><span>P90 耗时</span><b>{time(p90)}</b></div>
      <div><span>≤ 10s 完成</span><b>{(fast / total * 100).toFixed(0)}%</b></div>
      <div><span>≥ 30s 长尾</span><b>{(slow / total * 100).toFixed(0)}%</b></div>
    </div>}
  </div>;
}

export type LogFilters = {
  model?: string;
  provider?: string;
  apiKeyId?: string;
  apiKeyName?: string;
  status?: string;
  keyword?: string;
};

export function RequestAnalysisPanel({rows, providers, onOpenLogs}: {rows: LogEntry[]; providers: Provider[]; onOpenLogs: (filters: LogFilters) => void}) {
  const [search, setSearch] = useState('');
  const [model, setModel] = useState('');
  const [provider, setProvider] = useState('');
  const [key, setKey] = useState('');
  const [status, setStatus] = useState('');
  const [reasoning, setReasoning] = useState('');
  const models = useMemo(() => [...new Set(rows.map(r => r.model))].filter(Boolean).sort(), [rows]);
  const providersInRows = useMemo(() => [...new Set(rows.map(r => r.providerId))].filter(Boolean).sort(), [rows]);
  // Provider 筛选与日志页同一个二级下拉：左边品牌、右边账号；只列当前区间真有请求的账号。
  const providerFilterOptions = useMemo<IconSelectOption[]>(() => {
    const present = new Set(providersInRows);
    const known = providers.filter((item) => present.has(item.id));
    const options: IconSelectOption[] = [{value: '', label: '全部 Provider'}];
    options.push(...providerSelectOptions(known, {withProtocol: false}));
    // 已删除的 Provider 仍可能留在历史日志里：补一条无图标的素项，别让它们选不到。
    const knownIds = new Set(known.map((item) => item.id));
    for (const id of providersInRows) {
      if (!knownIds.has(id)) options.push({value: id, label: providerUsageLabel(id, providers)});
    }
    return options;
  }, [providers, providersInRows]);
  const keys = useMemo(() => [...new Map(rows.filter(r => r.apiKeyId).map(r => [r.apiKeyId!, r.apiKeyName || r.apiKeyId!])).entries()], [rows]);
  const reasoningOptions = useMemo(() => [...new Set(rows.map(r => r.reasoningEffort).filter((v): v is string => !!v))].sort(), [rows]);
  const filtered = useMemo(() => rows.filter(r => (!model || r.model === model) && (!provider || r.providerId === provider) && (!key || r.apiKeyId === key) && (!reasoning || r.reasoningEffort === reasoning) && (!status || (status === '499' ? isTrafficLogCanceled(r) : Math.floor(r.status / 100).toString() === status)) && `${r.model} ${r.apiKeyName} ${r.protocolFlow}`.toLowerCase().includes(search.toLowerCase())), [rows, model, provider, key, reasoning, status, search]);
  const latencies = filtered.map(r => r.latencyMs).filter(n => n > 0);
  // Captured logs know whether the call streamed. Legacy logs predate analysis
  // capture and store ttft == latency for non-stream calls, so a strictly
  // smaller ttft still identifies a real streaming sample.
  const streamSample = (r: LogEntry) => r.status >= 200 && r.status < 300 && (r.ttftMs || 0) > 0 && r.latencyMs > (r.ttftMs || 0) && (!r.analysis?.captured || r.analysis.stream);
  const streamed = filtered.filter(streamSample);
  const ttft = streamed.map(r => r.ttftMs!);
  const speeds = streamed.filter(r => r.outputTokens > 0).map(r => Math.round(r.outputTokens * 1000 / (r.latencyMs - r.ttftMs!)));
  const errors = filtered.filter(r => r.status >= 400 && !isTrafficLogCanceled(r)).length;
  const success = filtered.filter(r => r.status >= 200 && r.status < 300).length;
  const selects = (label: string, value: string, update: (v: string) => void, options: [string, string][]) => <select aria-label={label} value={value} onChange={e => update(e.target.value)}><option value="">全部{label}</option>{options.map(([id, name]) => <option key={id} value={id}>{name}</option>)}</select>;
  const exportRows = () => downloadCSV('gateway-requests.csv', [['时间', '模型', 'Provider', '密钥名称', '状态', '输入', '输出', '缓存读取', '缓存写入', '成本USD', '已计价', '耗时ms', '首字ms', '推理设置'], ...filtered.map(r => [r.time, r.model, providerUsageLabel(r.providerId, providers), r.apiKeyName || '', r.status, r.inputTokens, r.outputTokens, r.cacheTokens || 0, r.cacheWriteTokens || 0, r.priced ? (r.costMicros || 0) / 1_000_000 : '', r.priced ? 'y' : '', r.latencyMs, streamed.includes(r) ? r.ttftMs! : '', r.reasoningEffort || ''])].map(r => r.map(csv).join(',')).join('\r\n'));
  const openLogs = () => onOpenLogs({
    model: model || undefined,
    provider: provider || undefined,
    apiKeyId: key || undefined,
    apiKeyName: keys.find(([id]) => id === key)?.[1],
    status: status === '499' ? '499' : status ? `${status}xx` : undefined,
    keyword: search.trim() || undefined,
  });
  return <>
    <div className="usage-stats-strip usage-stats-strip--five">
      <Tile label="筛选请求" value={filtered.length.toLocaleString()} note={`错误 ${errors.toLocaleString()} 次 · 2xx ${success.toLocaleString()} 次`} />
      <Tile label="请求成功率" value={filtered.length ? `${(success / filtered.length * 100).toFixed(1)}%` : '—'} note={`${success.toLocaleString()} / ${filtered.length.toLocaleString()} 次`} />
      <Tile label="耗时中位数" value={time(percentile(latencies, .5))} note={`P90 ${time(percentile(latencies, .9))} · ${latencies.length} 样本`} />
      <Tile label="流式首字中位数" value={time(percentile(ttft, .5))} note={`P90 ${time(percentile(ttft, .9))} · ${ttft.length} 样本`} />
      <Tile label="流式输出中位速度" value={speeds.length ? `${percentile(speeds, .5)} tok/s` : '—'} note="输出 Token ÷ 首字后耗时，近似值" />
    </div>
    <div className="usage-filter-row">
      <input className="usage-search" aria-label="搜索请求" placeholder="模型、密钥、协议流" value={search} onChange={e => setSearch(e.target.value)} />
      {selects('模型', model, setModel, models.map(v => [v, v]))}
      <div className="usage-provider-select">
        <IconSelect
          value={provider}
          options={providerFilterOptions}
          onChange={setProvider}
          searchable
          renderPortal={false}
        />
      </div>
      {selects('密钥', key, setKey, keys)}
      {selects('状态', status, setStatus, [['2', '2xx 成功'], ['499', '499 客户端取消'], ['4', '4xx 请求异常'], ['5', '5xx 上游错误']])}
      {selects('推理设置', reasoning, setReasoning, reasoningOptions.map(v => [v, v]))}
      <button className="mini-btn usage-export-btn" onClick={exportRows}>导出筛选结果</button>
    </div>
    <div className="usage-log-handoff">
      <div>
        <b>逐条请求明细在「API 日志」</b>
        <span>这里只保留聚合分析。需要看单次请求的完整链路时，把当前筛选条件带过去。</span>
      </div>
      <button className="btn primary" type="button" onClick={openLogs}>在 API 日志查看明细 ↗</button>
    </div>
    <div className="usage-request-charts">
      <LatencyDistribution rows={filtered} />
      <UsageStatusChart title="筛选结果状态构成" items={statusBuckets(filtered)} />
    </div>
    <section className="usage-block">
      <div className="usage-block-head"><div><h3>处理阶段耗时</h3><p>按该阶段具有正值的请求取均值；各阶段可能重叠，不相加。</p></div></div>
      <div className="usage-stage-grid">{([['prepMs', '请求准备'], ['preUpstreamMs', '上游前处理'], ['upstreamTtfbMs', '上游首包'], ['gatewayOverheadMs', '网关开销'], ['convertOutMs', '响应转换'], ['postMs', '收尾']] as const).map(([field, label]) => { const values = filtered.map(r => r[field] || 0).filter(n => n > 0); return <div key={field}><span>{label}</span><b>{time(values.length ? Math.round(values.reduce((a, b) => a + b, 0) / values.length) : null)}</b><small>{values.length} 个样本</small></div>; })}</div>
    </section>
  </>;
}

export function ContextAnalysisPanel({rows, providers}: {rows: LogEntry[]; providers: Provider[]}) {
  const [page, setPage] = useState(0);
  const input = sum(rows, 'inputTokens'), read = sum(rows, 'cacheTokens'), write = sum(rows, 'cacheWriteTokens');
  const captured = rows.filter(r => r.analysis?.captured);
  const coverage = rows.length ? captured.length / rows.length * 100 : 0;
  const composition = [
    {label: '未缓存输入', value: Math.max(0, input - read - write), color: '#6366f1'},
    {label: '缓存读取', value: read, color: '#10b981'},
    {label: '缓存写入', value: write, color: '#f59e0b'},
  ];
  const parts = [['系统指令', captured.reduce((n, r) => n + r.analysis!.systemBytes, 0)], ['消息内容', captured.reduce((n, r) => n + r.analysis!.messageBytes, 0)], ['工具定义', captured.reduce((n, r) => n + r.analysis!.toolBytes, 0)]] as const;
  const bytes = parts.reduce((n, [, value]) => n + value, 0);
  const heavy = useMemo(() => [...rows].sort((a, b) => b.inputTokens - a.inputTokens).slice(0, 20), [rows]);
  const currentPage = Math.min(page, Math.max(0, Math.ceil(heavy.length / 10) - 1));
  return <>
    <div className="usage-stats-strip usage-stats-strip--five">
      <Tile label="单次输入中位数" value={rows.length ? formatTokenCount(percentile(rows.map(r => r.inputTokens), .5)!) : '—'} note={`P90 ${rows.length ? formatTokenCount(percentile(rows.map(r => r.inputTokens), .9)!) : '—'} Token`} />
      <Tile label="缓存读取占比" value={input ? `${(read / input * 100).toFixed(1)}%` : '—'} note={`${formatTokenCount(read)} Token`} />
      <Tile label="缓存写入" value={formatTokenCount(write)} note="已包含在输入内，不重复相加" />
      <Tile label="输入中位数 / 请求" value={rows.length ? formatTokenCount(Math.round(input / rows.length)) : '—'} note={`${rows.length.toLocaleString()} 次调用`} />
      <Tile label="结构采集覆盖" value={rows.length ? `${coverage.toFixed(1)}%` : '—'} note={`${captured.length} / ${rows.length} 次请求`} />
    </div>
    <div className="usage-context-grid">
      <UsageDonutChart
        title="输入 Token 组成"
        subtitle="上游报告的缓存读写与未缓存余量"
        items={composition}
        totalLabel="输入总量"
        formatValue={formatTokenCount}
      />
      <div className="usage-chart-card">
        <div className="usage-section-title-row"><div><div className="usage-section-title">上下文规模</div><div className="usage-section-subtitle">按单次请求输入 Token 观察上下文膨胀</div></div></div>
        <dl className="usage-facts usage-context-facts">
          <div><dt>输入总量</dt><dd>{formatTokenCount(input)}</dd></div>
          <div><dt>单次输入中位数</dt><dd>{rows.length ? formatTokenCount(percentile(rows.map(r => r.inputTokens), .5)!) : '—'}</dd></div>
          <div><dt>单次输入 P90</dt><dd>{rows.length ? formatTokenCount(percentile(rows.map(r => r.inputTokens), .9)!) : '—'}</dd></div>
          <div><dt>单次输入峰值</dt><dd>{rows.length ? formatTokenCount(Math.max(...rows.map(r => r.inputTokens))) : '—'}</dd></div>
          <div><dt>缓存命中率</dt><dd>{input ? `${(read / input * 100).toFixed(1)}%` : '—'}</dd></div>
        </dl>
      </div>
    </div>
    {captured.length > 0 ? <div className="usage-insight-grid">
      <section className="usage-block">
        <div className="usage-block-head"><div><h3>请求结构组成</h3><p>JSON 序列化字节，含结构开销；用于定位大块内容，不等于 Token。</p></div></div>
        <dl className="usage-facts">{parts.map(([label, value]) => <div key={label}><dt>{label}</dt><dd>{formatBytes(value)} <small>{bytes ? (value / bytes * 100).toFixed(1) : 0}%</small></dd></div>)}<div><dt>工具定义 / 工具结果</dt><dd>{captured.reduce((n, r) => n + r.analysis!.tools, 0)} / {captured.reduce((n, r) => n + r.analysis!.toolResults, 0)}</dd></div></dl>
      </section>
      <section className="usage-block">
        <div className="usage-block-head"><div><h3>结构样本画像</h3><p>仅统计已成功解析请求体的调用。</p></div></div>
        <dl className="usage-facts">
          <div><dt>消息数中位数 / P90</dt><dd>{percentile(captured.map(r => r.analysis!.messages), .5) ?? 0} / {percentile(captured.map(r => r.analysis!.messages), .9) ?? 0}</dd></div>
          <div><dt>工具定义中位数 / P90</dt><dd>{percentile(captured.map(r => r.analysis!.tools), .5) ?? 0} / {percentile(captured.map(r => r.analysis!.tools), .9) ?? 0}</dd></div>
          <div><dt>工具结果中位数 / P90</dt><dd>{percentile(captured.map(r => r.analysis!.toolResults), .5) ?? 0} / {percentile(captured.map(r => r.analysis!.toolResults), .9) ?? 0}</dd></div>
          <div><dt>流式请求占比</dt><dd>{captured.length ? `${(captured.filter(r => r.analysis!.stream).length / captured.length * 100).toFixed(0)}%` : '—'}</dd></div>
        </dl>
      </section>
    </div> : <div className="usage-callout">
      <b>结构采集刚刚启用</b>
      <p>请求体结构（系统指令、消息、工具定义字节）只对部署后新产生的请求采集；历史日志没有留存请求体，因此当前区间显示为未采集。新的调用会逐步填充这里，无需额外操作。</p>
    </div>}
    <section className="usage-block">
      <div className="usage-block-head"><div><h3>大上下文请求</h3><p>按输入 Token 排序，识别重复历史与工具定义的开销。窗口上限未知时不展示占用率。</p></div><span className="usage-note-inline">Top {heavy.length}</span></div>
      <div className="usage-table-wrap"><table className="usage-data-table"><thead><tr>{['模型 / 时间', 'Provider', '输入 Token', '缓存读 / 写', '成本', ...(captured.length > 0 ? ['消息', '工具定义', '工具结果'] : [])].map(h => <th key={h}>{h}</th>)}</tr></thead><tbody>{heavy.slice(currentPage * 10, currentPage * 10 + 10).map((r, i) => <tr key={r.id || i}><td>{r.model}<small className="usage-cell-caption">{new Date(r.time).toLocaleString()}</small></td><td><ProviderRefLabel providerId={r.providerId} providers={providers} size={14} /></td><td>{formatTokenCount(r.inputTokens)}</td><td>{formatTokenCount(r.cacheTokens || 0)} / {formatTokenCount(r.cacheWriteTokens || 0)}</td><CostCell log={r} />{captured.length > 0 && <><td>{r.analysis?.captured ? r.analysis.messages : '—'}</td><td>{r.analysis?.captured ? `${r.analysis.tools} · ${formatBytes(r.analysis.toolBytes)}` : '—'}</td><td>{r.analysis?.captured ? r.analysis.toolResults : '—'}</td></>}</tr>)}{!heavy.length && <tr><td colSpan={captured.length > 0 ? 8 : 5} className="usage-empty">当前区间暂无上下文记录</td></tr>}</tbody></table></div>
      {heavy.length > 10 && <Pager page={currentPage} count={heavy.length} onChange={setPage} />}
    </section>
  </>;
}
