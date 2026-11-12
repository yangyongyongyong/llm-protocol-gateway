import {useMemo, useState} from 'react';
import type {LogEntry, Provider} from '../types';
import {formatCompactCount, formatTokenCount, isTrafficLogCanceled, providerUsageLabel} from '../lib';
import {resolveProviderIcon} from '../provider_presets';
import {UsageDonutChart} from './charts';
import {BrandIcon} from './ui';
import {percentile} from './usage_analysis';
import './usage.css';

type SortBy = 'tokens' | 'requests' | 'latency';
type LogTarget = { type: 'model' | 'provider' | 'apiKey'; value: string; label: string };

type PerfEntity = {
  id: string;
  requests: number;
  successes: number;
  canceled: number;
  errors4: number;
  errors5: number;
  input: number;
  output: number;
  cache: number;
  latencies: number[];
  ttfts: number[];
  speeds: number[];
  providers: Map<string, number>;
};

const isSuccess = (row: LogEntry) => row.status >= 200 && row.status < 300;
// Same heuristic as the request tab: captured logs know whether the call
// streamed, legacy logs only expose a first-byte value smaller than latency.
const isStreamSample = (row: LogEntry) => isSuccess(row) && (row.ttftMs || 0) > 0 && row.latencyMs > (row.ttftMs || 0) && (!row.analysis?.captured || row.analysis.stream);
const percent = (part: number, total: number) => total > 0 ? `${(part / total * 100).toFixed(1)}%` : '—';
const duration = (value: number | null) => value == null ? '—' : `${value.toLocaleString()} ms`;
const tone = (rate: number) => rate >= 99 ? 'good' : rate >= 95 ? 'warn' : 'bad';

const CONTEXT_BUCKETS = [
  {label: '≤ 10K', max: 10_000},
  {label: '10–50K', max: 50_000},
  {label: '50–200K', max: 200_000},
  {label: '≥ 200K', max: Number.POSITIVE_INFINITY},
];

function contextBuckets(rows: LogEntry[]) {
  const counts = CONTEXT_BUCKETS.map(() => 0);
  for (const row of rows) {
    const index = CONTEXT_BUCKETS.findIndex(bucket => (row.inputTokens || 0) <= bucket.max);
    counts[index >= 0 ? index : counts.length - 1]++;
  }
  return CONTEXT_BUCKETS.map((bucket, index) => ({label: bucket.label, value: counts[index]}));
}

function aggregate(rows: LogEntry[], keyOf: (row: LogEntry) => string) {
  const entities = new Map<string, PerfEntity>();
  for (const row of rows) {
    const id = keyOf(row) || '未知';
    let entity = entities.get(id);
    if (!entity) {
      entity = {id, requests: 0, successes: 0, canceled: 0, errors4: 0, errors5: 0, input: 0, output: 0, cache: 0, latencies: [], ttfts: [], speeds: [], providers: new Map()};
      entities.set(id, entity);
    }
    entity.requests++;
    entity.input += row.inputTokens || 0;
    entity.output += row.outputTokens || 0;
    entity.cache += row.cacheTokens || 0;
    if (row.providerId) entity.providers.set(row.providerId, (entity.providers.get(row.providerId) || 0) + 1);
    if (isSuccess(row)) {
      entity.successes++;
      if (row.latencyMs > 0) entity.latencies.push(row.latencyMs);
      if (isStreamSample(row)) {
        entity.ttfts.push(row.ttftMs!);
        const tail = row.latencyMs - row.ttftMs!;
        if (row.outputTokens > 0 && tail > 0) entity.speeds.push(Math.round(row.outputTokens * 1000 / tail));
      }
    } else if (isTrafficLogCanceled(row)) {
      // 客户端主动取消不计入失败率：请求已经正常建立，只是调用端先结束了连接。
      entity.canceled++;
    } else if (row.status >= 500) {
      entity.errors5++;
    } else if (row.status >= 400) {
      entity.errors4++;
    }
  }
  return [...entities.values()];
}

function rankEntities(entities: PerfEntity[], sortBy: SortBy) {
  return [...entities].sort((a, b) => {
    if (sortBy === 'requests') return b.requests - a.requests;
    if (sortBy === 'latency') {
      const left = percentile(a.latencies, .5);
      const right = percentile(b.latencies, .5);
      if (left == null || right == null) return left == null ? (right == null ? 0 : 1) : -1;
      return right - left;
    }
    return (b.input + b.output) - (a.input + a.output);
  });
}

function topProvider(entity: PerfEntity) {
  return [...entity.providers.entries()].sort((a, b) => b[1] - a[1])[0]?.[0] || '';
}

function topCategories(rows: LogEntry[], keyOf: (row: LogEntry) => string, limit = 6) {
  const counts = new Map<string, number>();
  for (const row of rows) {
    const key = keyOf(row);
    counts.set(key, (counts.get(key) || 0) + 1);
  }
  const sorted = [...counts.entries()].sort((a, b) => b[1] - a[1]);
  if (sorted.length <= limit) return sorted.map(([label, value]) => ({label, value}));
  const head = sorted.slice(0, limit - 1).map(([label, value]) => ({label, value}));
  const rest = sorted.slice(limit - 1).reduce((sum, [, value]) => sum + value, 0);
  return [...head, {label: '其他', value: rest}];
}

function EntityIcon({provider}: {provider?: Provider}) {
  if (!provider) return null;
  return <span className="usage-entity-avatar"><BrandIcon icon={resolveProviderIcon(provider)} name={provider.name} size={22} /></span>;
}

function ErrorHotspots({models}: {models: PerfEntity[]}) {
  const hotspots = models
    .filter(entity => entity.errors4 + entity.errors5 > 0)
    .sort((a, b) => (b.errors4 + b.errors5) - (a.errors4 + a.errors5))
    .slice(0, 6);
  const max = Math.max(1, ...hotspots.map(entity => entity.errors4 + entity.errors5));
  return <div className="usage-chart-card perf-error-card">
    <div className="usage-section-title-row">
      <div>
        <div className="usage-section-title">错误热点</div>
        <div className="usage-section-subtitle">按模型聚合 4xx / 5xx，定位上游与请求问题</div>
      </div>
    </div>
    {hotspots.length === 0 ? <div className="empty-state compact">当前区间没有失败请求</div> : <div className="perf-error-list">
      {hotspots.map(entity => {
        const errors = entity.errors4 + entity.errors5;
        return <div className="perf-error-row" key={entity.id}>
          <div className="perf-error-head"><span title={entity.id}>{entity.id}</span><b>{errors.toLocaleString()} 次 · {percent(errors, entity.requests)}</b></div>
          <div className="perf-error-track" title={`4xx ${entity.errors4} · 5xx ${entity.errors5}`}>
            <i className="is-4xx" style={{width: `${entity.errors4 / max * 100}%`}} />
            <i className="is-5xx" style={{width: `${entity.errors5 / max * 100}%`}} />
          </div>
          <small>4xx {entity.errors4} · 5xx {entity.errors5}</small>
        </div>;
      })}
    </div>}
  </div>;
}

function AccessSourceCard({rows}: {rows: LogEntry[]}) {  const labels: Record<string, string> = {lan: '局域网', public: '公网', local: '本机'};
  const items = topCategories(rows, row => labels[row.accessSource || ''] || row.accessSource || '未记录', 5);
  const max = Math.max(1, ...items.map(item => item.value));
  return <div className="usage-chart-card perf-source-card">
    <div className="usage-section-title-row">
      <div>
        <div className="usage-section-title">访问来源</div>
        <div className="usage-section-subtitle">请求从哪个入口进入网关</div>
      </div>
    </div>
    <div className="perf-source-list">
      {items.map((item, index) => <div className="perf-source-row" key={item.label}>
        <span><i style={{background: `var(--usage-category-${index + 1})`}} />{item.label}</span>
        <b>{item.value.toLocaleString()}</b>
        <small>{percent(item.value, rows.length)}</small>
        <div className="perf-source-track"><i style={{width: `${item.value / max * 100}%`}} /></div>
      </div>)}
      {!items.length && <div className="empty-state compact">暂无访问来源记录</div>}
    </div>
  </div>;
}

export function PerformanceAnalysisPanel({rows, providers, onNavigateLogs}: {rows: LogEntry[]; providers: Provider[]; onNavigateLogs: (target: LogTarget) => void}) {
  const [sortBy, setSortBy] = useState<SortBy>('tokens');
  const providerByID = useMemo(() => new Map(providers.map(provider => [provider.id, provider])), [providers]);
  const models = useMemo(() => aggregate(rows, row => row.model), [rows]);
  const providerRows = useMemo(() => aggregate(rows, row => row.providerId), [rows]);
  const rankedModels = useMemo(() => rankEntities(models, sortBy).slice(0, 12), [models, sortBy]);
  const rankedProviders = useMemo(() => rankEntities(providerRows, sortBy), [providerRows, sortBy]);
  const reasoning = useMemo(() => topCategories(rows, row => row.reasoningEffort?.trim() || '未设置'), [rows]);
  const inputs = useMemo(() => contextBuckets(rows), [rows]);
  const slowest = useMemo(() => [...rows].filter(row => row.latencyMs > 0).sort((a, b) => b.latencyMs - a.latencyMs).slice(0, 8), [rows]);

  if (rows.length === 0) return <div className="usage-empty">当前区间暂无性能明细。</div>;

  return <div className="perf-stack">
    <div className="perf-toolbar">
      <div className="usage-segments">
        {([{id: 'tokens', label: '按 Token'}, {id: 'requests', label: '按请求'}, {id: 'latency', label: '按耗时'}] as const).map(item =>
          <button key={item.id} type="button" className={sortBy === item.id ? 'active' : ''} onClick={() => setSortBy(item.id)}>{item.label}</button>)}
      </div>
      <span className="usage-note-inline">覆盖 {rows.length.toLocaleString()} 条保留期明细 · {models.length} 个模型 · {providerRows.length} 个 Provider</span>
    </div>

    <section className="usage-block perf-section">
      <div className="usage-block-head">
        <div><h3>模型性能对照</h3><p>请求量、Token、成功率与耗时均来自保留期内明细样本。</p></div>
        <span className="usage-note-inline">Top {rankedModels.length} / {models.length}</span>
      </div>
      <div className="usage-table-wrap">
        <table className="usage-data-table perf-table">
          <thead><tr>{['模型 / 来源', '请求', '成功率', '耗时 P50 / P90', '流式首字', '输出速度', '缓存命中', 'Token / 请求', ''].map((head, index) => <th key={head || index}>{head}</th>)}</tr></thead>
          <tbody>
            {rankedModels.map((entity, index) => {
              const provider = providerByID.get(topProvider(entity));
              const total = entity.input + entity.output;
              const measured = Math.max(0, entity.requests - entity.canceled);
              const rate = measured ? entity.successes / measured * 100 : 0;
              const rateTone = measured ? tone(rate) : 'neutral';
              return <tr key={entity.id}>
                <td><div className="usage-rank-identity">
                  <span className={`usage-rank-number rank-${index < 3 ? index + 1 : 'rest'}`}>{String(index + 1).padStart(2, '0')}</span>
                  <EntityIcon provider={provider} />
                  <div><span className="usage-entity-name" title={entity.id}>{entity.id}</span><small className="usage-entity-subtitle">{provider ? provider.name : '未知来源'}{entity.providers.size > 1 ? ` 等 ${entity.providers.size} 个来源` : ''}</small></div>
                </div></td>
                <td><b className="usage-ranking-value">{entity.requests.toLocaleString()}</b><small className="usage-cell-caption">{percent(entity.requests, rows.length)} 占比</small></td>
                <td><span className={`perf-rate is-${rateTone}`}>{measured ? `${rate.toFixed(1)}%` : '—'}</span><small className="usage-cell-caption">{Math.max(0, measured - entity.successes)} 次失败{entity.canceled ? ` · ${entity.canceled} 次取消` : ''}</small></td>
                <td>{duration(percentile(entity.latencies, .5))}<small className="usage-cell-caption">P90 {duration(percentile(entity.latencies, .9))}</small></td>
                <td>{duration(percentile(entity.ttfts, .5))}<small className="usage-cell-caption">{entity.ttfts.length} 个流式样本</small></td>
                <td>{entity.speeds.length ? `${percentile(entity.speeds, .5)} tok/s` : '—'}<small className="usage-cell-caption">{entity.speeds.length} 个样本</small></td>
                <td>{percent(entity.cache, entity.input)}<small className="usage-cell-caption">{formatTokenCount(entity.cache)} Token</small></td>
                <td>{formatTokenCount(total)}<small className="usage-cell-caption">{formatTokenCount(Math.round(total / entity.requests))} / 次</small></td>
                <td><button className="mini-btn usage-log-link" type="button" onClick={() => onNavigateLogs({type: 'model', value: entity.id, label: entity.id})}>日志 ↗</button></td>
              </tr>;
            })}
            {!rankedModels.length && <tr><td colSpan={9} className="usage-empty">当前区间暂无模型明细</td></tr>}
          </tbody>
        </table>
      </div>
    </section>

    <div className="perf-split-row">
      <section className="usage-block perf-section">
        <div className="usage-block-head"><div><h3>Provider 性能对照</h3><p>按上游连接比较成功率、首字与缓存效率。</p></div></div>
        <div className="usage-table-wrap">
          <table className="usage-data-table perf-table">
            <thead><tr>{['Provider', '请求', '成功率', '耗时 P50 / P90', '首字 P50', '缓存命中', 'Token', ''].map((head, index) => <th key={head || index}>{head}</th>)}</tr></thead>
            <tbody>
              {rankedProviders.map((entity, index) => {
                const provider = providerByID.get(entity.id);
                const measured = Math.max(0, entity.requests - entity.canceled);
                const rate = measured ? entity.successes / measured * 100 : 0;
                const rateTone = measured ? tone(rate) : 'neutral';
                return <tr key={entity.id}>
                  <td><div className="usage-rank-identity">
                    <span className={`usage-rank-number rank-${index < 3 ? index + 1 : 'rest'}`}>{String(index + 1).padStart(2, '0')}</span>
                    <EntityIcon provider={provider} />
                    <div><span className="usage-entity-name" title={entity.id}>{providerUsageLabel(entity.id, providers)}</span><small className="usage-entity-subtitle">{entity.id}</small></div>
                  </div></td>
                  <td><b className="usage-ranking-value">{entity.requests.toLocaleString()}</b><small className="usage-cell-caption">{percent(entity.requests, rows.length)} 占比</small></td>
                  <td><span className={`perf-rate is-${rateTone}`}>{measured ? `${rate.toFixed(1)}%` : '—'}</span><small className="usage-cell-caption">{Math.max(0, measured - entity.successes)} 次失败{entity.canceled ? ` · ${entity.canceled} 次取消` : ''}</small></td>
                  <td>{duration(percentile(entity.latencies, .5))}<small className="usage-cell-caption">P90 {duration(percentile(entity.latencies, .9))}</small></td>
                  <td>{duration(percentile(entity.ttfts, .5))}<small className="usage-cell-caption">{entity.ttfts.length} 样本</small></td>
                  <td>{percent(entity.cache, entity.input)}<small className="usage-cell-caption">{formatTokenCount(entity.cache)} Token</small></td>
                  <td>{formatTokenCount(entity.input + entity.output)}<small className="usage-cell-caption">{formatCompactCount(entity.input + entity.output)}</small></td>
                  <td><button className="mini-btn usage-log-link" type="button" onClick={() => onNavigateLogs({type: 'provider', value: entity.id, label: providerUsageLabel(entity.id, providers)})}>日志 ↗</button></td>
                </tr>;
              })}
              {!rankedProviders.length && <tr><td colSpan={8} className="usage-empty">当前区间暂无 Provider 明细</td></tr>}
            </tbody>
          </table>
        </div>
      </section>
      <ErrorHotspots models={models} />
    </div>

    <div className="perf-dimension-row">
      <UsageDonutChart title="推理强度分布" subtitle="按请求数统计上游实际收到的推理设置" items={reasoning} totalLabel="请求" formatValue={value => value.toLocaleString()} donutSize={150} />
      <UsageDonutChart title="输入规模分布" subtitle="按单次请求输入 Token 分档" items={inputs} totalLabel="请求" formatValue={value => value.toLocaleString()} donutSize={150} />
      <AccessSourceCard rows={rows} />
    </div>

    <section className="usage-block perf-section">
      <div className="usage-block-head"><div><h3>最慢请求</h3><p>按端到端耗时排序，用于定位长尾与上游卡顿。</p></div><span className="usage-note-inline">Top {slowest.length}</span></div>
      <div className="usage-table-wrap">
        <table className="usage-data-table usage-request-table perf-table">
          <thead><tr>{['时间 / 模型', 'Provider', '状态', '耗时 / 首字', '输入 / 输出', '推理'].map(head => <th key={head}>{head}</th>)}</tr></thead>
          <tbody>
            {slowest.map((row, index) => <tr key={row.id || index} className={row.status >= 400 && !isTrafficLogCanceled(row) ? 'is-error' : ''}>
              <td><span className="usage-request-model">{row.model}</span><small className="usage-cell-caption">{new Date(row.time).toLocaleString()}</small></td>
              <td>{providerUsageLabel(row.providerId, providers)}</td>
              <td><span className={`usage-status${row.status >= 400 && !isTrafficLogCanceled(row) ? ' is-error' : isTrafficLogCanceled(row) ? ' is-canceled' : ''}`}>{isTrafficLogCanceled(row) ? '—' : row.status >= 400 ? '×' : '✓'} {row.status}</span></td>
              <td>{duration(row.latencyMs)}<small className="usage-cell-caption">{isStreamSample(row) ? duration(row.ttftMs!) : '—'}</small></td>
              <td>{formatTokenCount(row.inputTokens)} / {formatTokenCount(row.outputTokens)}</td>
              <td>{row.reasoningEffort || '—'}</td>
            </tr>)}
            {!slowest.length && <tr><td colSpan={6} className="usage-empty">当前区间暂无耗时样本</td></tr>}
          </tbody>
        </table>
      </div>
    </section>
  </div>;
}
