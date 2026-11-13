import {type ReactNode, useState} from 'react';
import type {Model, Provider, RequestStatsSnapshot, RTKStatsResponse} from '../types';
import {
    formatBytes,
    formatCompactCount,
    formatCostCoverage,
    formatCostMicros,
    formatDateInZone,
    formatLocalISODate,
    formatTokenCount,
    normalizePromptTokenStats,
    providerUsageParts,
} from '../lib';
import {
    downloadCSV,
    Metric,
    RTKSavingsChart,
    UsageComboTrendChart,
    UsageDailyTrafficLines,
    UsageDonutChart,
    UsageHourlyDistribution,
    UsageModelLeaderboard,
    UsageStatusChart,
} from './charts';
import './usage.css';
import {ContextAnalysisPanel, type LogFilters, RequestAnalysisPanel, useUsageAnalysis} from './usage_analysis';
import {PerformanceAnalysisPanel} from './usage_performance';
import {PriceTablePanel} from './pricing';
import {ProviderRefLabel} from './provider_label';

/** 概览份额图按哪个口径切：Token、请求次数，或已计价成本。 */
type Metric = 'tokens' | 'requests' | 'cost';
type LogTarget = { type: 'model' | 'provider' | 'apiKey'; value: string; label: string };

function csvCell(value: string | number) {
  const text = String(value);
  return `"${(/^[=+@\-\t\r]/.test(text) ? "'" : '') + text.replace(/"/g, '""')}"`;
}

/** 导出用美元：CSV 里放换算后的金额，不再放微美元，Excel 直接可读。 */
function usdOf(costMicros?: number) {
  return costMicros ? (costMicros / 1_000_000).toFixed(6) : '';
}

/** 份额口径的中文名，图标题与副标题共用，避免切了口径标题还写 Token。 */
function metricLabel(metric: Metric) {
  return metric === 'requests' ? '请求' : metric === 'cost' ? '已计价成本' : 'Token';
}

type ShareRow = { requestCount: number; inputTokens?: number; outputTokens?: number; costMicros?: number };

/**
 * 一行用量在当前口径下的数值。未计价行的 costMicros 缺省，按 0 参与排序：
 * 成本口径下没有价格的对象本来就贡献不了金额，覆盖率另有 KPI 说明。
 */
function metricValue(row: ShareRow, metric: Metric) {
  if (metric === 'requests') return row.requestCount || 0;
  if (metric === 'cost') return row.costMicros || 0;
  return (row.inputTokens || 0) + (row.outputTokens || 0);
}

/** 成本用美元格式，Token 用万亿缩写，请求数保持原始数字（沿用 undefined 走默认格式化）。 */
function metricFormat(metric: Metric): ((value: number) => string) | undefined {
  if (metric === 'cost') {
    // 没有金额的行显示 —，不写 $0.0000：未计价不等于免费。
    return (value: number) => (value > 0 ? formatCostMicros(value) : '—');
  }
  if (metric === 'tokens') return formatCompactCount;
  return undefined;
}

function exportUsage(stats: RequestStatsSnapshot, from: string, to: string) {
  const period = stats.range;
  const lines: (string | number)[][] = [
    ['统计区间', from, to],
    ['日期', '请求数', '输入 Token（含缓存）', '输出 Token', '缓存读取 Token', '已计价成本 USD', '已计价请求数', '接收字节', '发送字节', '平均耗时 ms', '平均首字 ms'],
    ...(stats.daily || []).map(d => [d.date, d.requestCount, d.inputTokens, d.outputTokens, d.cacheTokens, usdOf(d.costMicros), d.pricedCount || 0, d.rxBytes || 0, d.txBytes || 0, d.avgLatencyMs || 0, d.avgTtftMs || 0]),
  ];
  for (const [name, rows] of [
    ['模型', (period?.byModel || []).map(r => ({ ...r, name: r.model }))],
    ['Provider', (period?.byProvider || []).map(r => ({ ...r, name: r.providerId }))],
    ['下游网关', (period?.byDownstreamGateway || []).map(r => ({ ...r, name: r.gatewayName || r.gatewayId }))],
    ['API 密钥', (period?.byApiKey || []).map(r => ({ ...r, name: r.apiKeyName || r.apiKeyId }))],
    ['输出协议', (period?.byProtocol || []).map(r => ({ ...r, name: r.protocol }))],
    ['用户', (period?.byUser || []).map(r => ({ ...r, name: r.userName || r.userId }))],
  ] as const) {
    lines.push([], [name, '请求数', '输入 Token（含缓存）', '输出 Token', '缓存读取 Token', '已计价成本 USD', '已计价请求数']);
    for (const r of rows) lines.push([r.name, r.requestCount, r.inputTokens, r.outputTokens, r.cacheTokens, usdOf(r.costMicros), r.pricedCount || 0]);
  }
  downloadCSV(`gateway-usage-${from}-to-${to}.csv`, lines.map(row => row.map(csvCell).join(',')).join('\r\n'));
}

function RankBars({ title, subtitle, items, formatValue, toggle }: { title: string; subtitle: string; items: Array<{ label: string; value: number; sub?: string }>; formatValue?: (value: number) => string; toggle?: ReactNode }) {
  const max = Math.max(1, ...items.map(item => item.value));
  return <div className="usage-chart-card usage-rank-bars-card">
    <div className="usage-section-title-row">
      <div><div className="usage-section-title">{title}</div><div className="usage-section-subtitle">{subtitle}</div></div>
      {toggle}
    </div>
    {items.length === 0 ? <div className="empty-state compact">当前区间暂无数据</div> : <div className="usage-bar-list">
      {items.map((item, index) => <div className="usage-bar-row" key={`${item.label}-${index}`}>
        <span className="usage-bar-rank">#{index + 1}</span>
        <span className={`usage-bar-label${item.sub ? ' stacked' : ''}`} title={item.sub ? `${item.label} · ${item.sub}` : item.label}>
          <span className="usage-bar-name">{item.label}</span>
          {item.sub ? <span className="usage-bar-sub">{item.sub}</span> : null}
        </span>
        <div className="usage-bar-track"><div className="usage-bar-fill" style={{ width: `${(item.value / max) * 100}%` }} /></div>
        <span className="usage-bar-value font-mono">{formatValue ? formatValue(item.value) : item.value.toLocaleString()}</span>
      </div>)}
    </div>}
  </div>;
}

function PulseStrip({ stats, todayMismatch }: { stats: RequestStatsSnapshot; todayMismatch: boolean }) {
  const today = stats.today;
  const month = stats.month;
  const last = today.lastRequest;
  const rx = (stats.daily || []).reduce((n, d) => n + (d.rxBytes || 0), 0);
  const tx = (stats.daily || []).reduce((n, d) => n + (d.txBytes || 0), 0);
  return <div className="usage-pulse-strip">
    <div className="usage-pulse-item">
      <span>今日{todayMismatch ? ` · 网关日期 ${today.date}` : ` · ${today.date}`}</span>
      <b>{today.total.requestCount.toLocaleString()} 次</b>
      <small>{formatTokenCount(today.total.inputTokens + today.total.outputTokens)} Token</small>
    </div>
    <div className="usage-pulse-item">
      <span>本月 · {month.period}</span>
      <b>{month.total.requestCount.toLocaleString()} 次</b>
      <small>{formatTokenCount(month.total.inputTokens + month.total.outputTokens)} Token</small>
    </div>
    <div className="usage-pulse-item">
      <span>区间流量</span>
      {rx + tx > 0 ? <><b>接收 {formatBytes(rx)}</b><small>发送 {formatBytes(tx)}</small></> : <><b>—</b><small>新请求开始记录收发字节</small></>}
    </div>
    <div className="usage-pulse-item">
      <span>最近一次请求</span>
      {last ? <><b className="usage-pulse-ellipsis" title={last.model}>{last.model}</b><small>{new Date(last.time).toLocaleTimeString()} · HTTP {last.status}{last.apiKeyName ? ` · ${last.apiKeyName}` : ''}</small></> : <><b>—</b><small>今日暂无请求</small></>}
    </div>
  </div>;
}

function Overview({ stats, providers, models, isNormalUser, metric, onMetricChange, serverTimeZone }: {
  stats: RequestStatsSnapshot; providers: Provider[]; models: Model[]; isNormalUser: boolean; metric: Metric; onMetricChange: (metric: Metric) => void; serverTimeZone: string;
}) {
  const [rankTab, setRankTab] = useState<'keys' | 'users'>('keys');
  const range = stats.range;
  const total = range?.total;
  const daily = stats.daily || [];
  const hasTrafficBytes = daily.some(d => (d.rxBytes || 0) + (d.txBytes || 0) > 0);
  const requests = total?.requestCount || 0;
  const input = total?.inputTokens || 0, output = total?.outputTokens || 0;
  const cache = normalizePromptTokenStats(input, total?.cacheTokens || 0);
  const statusTotal = (stats.status || []).reduce((n, s) => n + s.requestCount, 0);
  const successful = stats.status?.find(s => s.class === '2xx')?.requestCount || 0;
  const successRate = statusTotal > 0 ? successful / statusTotal * 100 : null;
  const measured = daily.filter(d => (d.avgLatencyMs || 0) > 0);
  const latencyCount = measured.reduce((n, d) => n + d.requestCount, 0);
  const avgLatency = latencyCount ? Math.round(measured.reduce((n, d) => n + (d.avgLatencyMs || 0) * d.requestCount, 0) / latencyCount) : null;
  const ttftCount = daily.filter(d => (d.avgTtftMs || 0) > 0).reduce((n, d) => n + d.requestCount, 0);
  const avgTtft = ttftCount ? Math.round(daily.reduce((n, d) => n + (d.avgTtftMs || 0) * ((d.avgTtftMs || 0) > 0 ? d.requestCount : 0), 0) / ttftCount) : null;
  // 服务器的「今天」：服务器的日界是网关所在时区的 00:00，不是浏览器的。
  const todayMismatch = stats.today.date !== formatDateInZone(new Date(), serverTimeZone);
  const rangeDays = daily.length || 1;
  const activeDays = daily.filter(d => d.requestCount > 0).length;
  const sparkRequests = daily.map(d => d.requestCount || 0);
  const sparkTokens = daily.map(d => (d.inputTokens || 0) + (d.outputTokens || 0));
  const sparkCache = daily.map(d => d.cacheTokens || 0);
  const sparkLatency = daily.map(d => Math.round(d.avgLatencyMs || 0));
  const sparkTtft = daily.map(d => Math.round(d.avgTtftMs || 0));
  const sparkCost = daily.map(d => d.costMicros || 0);
  const costMicros = total?.costMicros || 0;
  const pricedCount = total?.pricedCount || 0;
  // 环形图与排行都按「谁消耗的」这一层看：同一个服务商的多个登录账号是各自独立的
  // Provider 记录，只读名字完全分不清，所以窄格子一律拆成 名称 / 账号 两行。
  const providerItems = (range?.byProvider || []).map(p => ({
    ...providerUsageParts(p.providerId, providers),
    value: metricValue(p, metric),
  }));
  const accountItems = [...(range?.byProvider || [])]
    .filter(p => metricValue(p, metric) > 0)
    .sort((a, b) => metricValue(b, metric) - metricValue(a, metric))
    .slice(0, 8)
    .map(p => ({ ...providerUsageParts(p.providerId, providers), value: metricValue(p, metric) }));
  const downstreamItems = [...(range?.byDownstreamGateway || [])]
    .filter(row => metricValue(row, metric) > 0)
    .sort((a, b) => metricValue(b, metric) - metricValue(a, metric))
    .slice(0, 8)
    .map(row => ({
      label: row.gatewayName || row.gatewayId,
      value: metricValue(row, metric),
      sub: `${row.requestCount.toLocaleString()} 次 · ${formatCompactCount((row.inputTokens || 0) + (row.outputTokens || 0))} Token`,
    }));
  const protocolItems = (range?.byProtocol || []).map(p => ({ label: p.protocol, value: metricValue(p, metric) }));
  const activeModels = (range?.byModel || []).filter(m => m.requestCount > 0).length;
  const activeProviders = (range?.byProvider || []).filter(p => p.requestCount > 0).length;
  const topModel = [...(range?.byModel || [])].sort((a, b) => b.requestCount - a.requestCount)[0];
  const topModelShare = topModel && requests ? topModel.requestCount / requests * 100 : null;
  const keyItems = (range?.byApiKey || []).slice(0, 8).map(k => ({ label: k.apiKeyName || k.apiKeyId || '未命名密钥', value: metricValue(k, metric) }));
  const userItems = (range?.byUser || []).slice(0, 8).map(u => ({ label: u.userName || u.userId || '未绑定用户', value: metricValue(u, metric) }));
  const successBadge = successRate == null ? undefined : successRate >= 99 ? '健康' : successRate >= 95 ? '注意' : '异常';
  const successTone = successRate == null ? 'blue' : successRate >= 99 ? 'green' : successRate >= 95 ? 'amber' : 'red';
  // 成本只累加了有价格行的请求，所以金额必须配着覆盖率读：把「大半没设价」显示成
  // 「很便宜」是这个账本唯一不可原谅的错。
  const costBadge = !requests ? undefined : pricedCount === 0 ? '未计价' : pricedCount < requests ? '部分未计价' : '全部已计价';
  const costTone = costBadge === '全部已计价' ? 'green' : costBadge === '部分未计价' ? 'amber' : 'red';
  const coverageSummary = formatCostCoverage(pricedCount, requests);
  return <>
    <div className="usage-metric-grid">
      <Metric label="区间请求" value={requests.toLocaleString()} note={`覆盖 ${rangeDays} 天 · 活跃 ${activeDays} 天 · 日均 ${Math.round(requests / rangeDays).toLocaleString()} 次`} sparkline={sparkRequests} sparklineColor="#38bdf8" />
      <Metric label="区间总 Token" value={formatTokenCount(input + output)} note={`输入 ${formatTokenCount(input)} · 输出 ${formatTokenCount(output)}`} sparkline={sparkTokens} sparklineColor="#6366f1" />
      <Metric label="Prompt 缓存命中" value={input > 0 ? `${cache.hitRatePct.toFixed(1)}%` : '—'} note={cache.cacheHits > 0 ? `缓存读取 ${formatTokenCount(cache.cacheHits)} · 占输入 ${cache.hitRatePct.toFixed(0)}%` : '当前区间未触发 Prompt Cache'} sparkline={sparkCache} sparklineColor="#10b981" />
      <Metric label="平均往返耗时" value={avgLatency == null ? '—' : `${avgLatency.toLocaleString()} ms`} note={avgLatency == null ? '当前区间暂无耗时样本' : '按请求加权 · 含上游与网关耗时'} sparkline={sparkLatency} sparklineColor="#f59e0b" />
      <Metric label="平均首字耗时" value={avgTtft == null ? '—' : `${avgTtft.toLocaleString()} ms`} note={avgTtft == null ? '当前区间暂无首字样本' : '按日加权 · 非流式请求按总耗时计入'} sparkline={sparkTtft} sparklineColor="#a855f7" />
      <Metric label="请求成功率" value={successRate == null ? '—' : `${successRate.toFixed(1)}%`} note={statusTotal ? `2xx ${successful.toLocaleString()} / ${statusTotal.toLocaleString()} 次` : '当前区间暂无状态记录'} badge={successBadge} badgeTone={successTone} />
      <Metric label="区间已计价成本" value={formatCostMicros(costMicros || undefined)} note={requests ? `${coverageSummary} · 已计价的每次 ${pricedCount ? formatCostMicros(Math.round(costMicros / pricedCount)) : '—'}` : '当前区间暂无请求'} sparkline={sparkCost} sparklineColor="#f43f5e" badge={costBadge} badgeTone={costTone} />
    </div>
    <PulseStrip stats={stats} todayMismatch={todayMismatch} />
    <div className="usage-charts-full">
      <UsageComboTrendChart title={`用量与性能走势 · ${range?.period || ''}`} points={daily} />
    </div>
    <div className="overview-ops-row">
      <UsageHourlyDistribution title="24 小时活跃分布" items={stats.hourly} rangeRequests={requests} />
      <UsageStatusChart title="状态码与健康度分布" items={stats.status || []} />
    </div>
    {/* 口径切换放在它真正生效的那一组图表上方：之前挂在顶部工具栏里，
        受影响的份额环图与排行都在两屏之外，点了看不到任何变化，像是没反应。 */}
    <div className="usage-share-bar">
      <div className="usage-share-bar-text">
        <span className="usage-share-bar-title">份额与排行口径</span>
        <small>下方份额环图、模型排行与密钥 / 账号 / 用户排行都按「{metricLabel(metric)}」统计{metric === 'cost' ? '，未计价的对象记为 0' : ''}；上方 KPI 卡片始终是绝对值。</small>
      </div>
      <div className="usage-segments usage-metric-switch" role="group" aria-label="份额统计口径">
        <button type="button" aria-pressed={metric === 'tokens'} className={metric === 'tokens' ? 'active' : ''} onClick={() => onMetricChange('tokens')}>Token 份额</button>
        <button type="button" aria-pressed={metric === 'requests'} className={metric === 'requests' ? 'active' : ''} onClick={() => onMetricChange('requests')}>请求份额</button>
        <button type="button" aria-pressed={metric === 'cost'} className={metric === 'cost' ? 'active' : ''} onClick={() => onMetricChange('cost')} title="按网关价格表算出的成本排序；未计价的对象记为 0">成本份额</button>
      </div>
    </div>
    <div className="overview-distribution-row">
      <UsageDonutChart title={`Provider 份额（${metricLabel(metric)}）`} subtitle="各上游服务商承载占比" items={providerItems} formatValue={metricFormat(metric)} />
      <UsageDonutChart title={`输出协议分布（${metricLabel(metric)}）`} subtitle="网关输出协议转换份额" items={protocolItems} formatValue={metricFormat(metric)} />
      <div className="usage-chart-card usage-profile-card">
        <div className="usage-section-title-row"><div><div className="usage-section-title">请求画像</div><div className="usage-section-subtitle">区间内每次调用的平均消耗与集中度</div></div></div>
        <dl className="usage-facts usage-profile-facts">
          <div><dt>每次请求 Token</dt><dd>{requests ? formatTokenCount(Math.round((input + output) / requests)) : '—'}</dd></div>
          <div><dt>每次输入 / 输出</dt><dd>{requests ? `${formatTokenCount(Math.round(input / requests))} / ${formatTokenCount(Math.round(output / requests))}` : '—'}</dd></div>
          <div><dt>输出 / 输入</dt><dd>{input ? `${(output / input * 100).toFixed(1)}%` : '—'}</dd></div>
          <div><dt>已计价请求的均价</dt><dd title={coverageSummary}>{pricedCount ? formatCostMicros(Math.round(costMicros / pricedCount)) : '—'}</dd></div>
          <div><dt>Top 模型请求占比</dt><dd title={topModel?.model}>{topModelShare == null ? '—' : `${topModelShare.toFixed(1)}%`}</dd></div>
          <div><dt>活跃模型</dt><dd>{activeModels}</dd></div>
          <div><dt>活跃 Provider</dt><dd>{activeProviders}</dd></div>
        </dl>
      </div>
    </div>
    <div className="overview-rankings-row">
      <UsageModelLeaderboard title="核心模型用量排行" items={range?.byModel || []} metric={metric} models={models} providers={providers} />
      <div className="usage-rank-stack">
        <RankBars
          title={rankTab === 'keys' ? 'API 密钥消耗排行' : '用户消耗排行'}
          subtitle={`按${metricLabel(metric)} Top 8`}
          items={rankTab === 'keys' ? keyItems : userItems}
          formatValue={metricFormat(metric)}
          toggle={<div className="usage-segments usage-rank-toggle">
            <button type="button" className={rankTab === 'keys' ? 'active' : ''} onClick={() => setRankTab('keys')}>密钥</button>
            {!isNormalUser && <button type="button" className={rankTab === 'users' ? 'active' : ''} onClick={() => setRankTab('users')}>用户</button>}
          </div>}
        />
        <RankBars
          title="账号消耗排行"
          subtitle={`按${metricLabel(metric)} Top 8 · 同一服务商的多个账号分开计`}
          items={accountItems}
          formatValue={metricFormat(metric)}
        />
        {downstreamItems.length > 0 && <RankBars
          title="下游网关消耗排行"
          subtitle={`按${metricLabel(metric)} Top 8 · 仅统计它送进本机的请求`}
          items={downstreamItems}
          formatValue={metricFormat(metric)}
        />}
      </div>
    </div>
    {hasTrafficBytes && <div className="usage-charts-full">
      <UsageDailyTrafficLines title="按日业务流量（上行接收 vs 下行发送）" points={daily} />
    </div>}
  </>;
}

/** RTK 面板的时间窗：与概览区间解耦，默认 30 天，含「今天」。 */
const RTK_WINDOWS = [{days: 1, label: '今天'}, {days: 7, label: '7 天'}, {days: 30, label: '30 天'}, {days: 90, label: '90 天'}, {days: 0, label: '全部'}];

function CompressionUsage({ stats, days, onDaysChange, providers }: { stats: RTKStatsResponse | null; days: number; onDaysChange: (days: number) => void; providers: Provider[] }) {
  const accounts = stats?.byProvider || [];
  const savingAccounts = accounts.filter(row => row.commands > 0);
  const original = stats?.originalTokens || 0;
  const saved = stats?.savedTokens || 0;
  const windowLabel = RTK_WINDOWS.find(item => item.days === days)?.label || `${days} 天`;
  return <section className="usage-block usage-rtk-block">
    <div className="usage-block-head">
      <div>
        <h3>RTK 工具输出压缩</h3>
        <p>统计网关转发前压缩过的命令输出，时间窗与概览区间相互独立。当前窗口：{windowLabel}{stats?.from ? ` · 自 ${stats.from}` : ''}。</p>
      </div>
      <div className="usage-segments" role="group" aria-label="RTK 时间窗">{RTK_WINDOWS.map(item => <button key={item.days} type="button" aria-pressed={days === item.days} className={days === item.days ? 'active' : ''} onClick={() => onDaysChange(item.days)}>{item.label}</button>)}</div>
    </div>
    <div className="usage-rtk-summary">
      <div className="usage-rtk-hero">
        <span>已节省 Token · {windowLabel}</span>
        <b>{formatTokenCount(saved)}</b>
        <small>平均压缩率 {(stats?.savedPercent || 0).toFixed(1)}% · 覆盖 {savingAccounts.length} 个账号</small>
      </div>
      <dl className="usage-facts">
        <div><dt>原始输出</dt><dd>{formatTokenCount(original)}</dd></div>
        <div><dt>压缩后</dt><dd>{formatTokenCount(stats?.compressedTokens || 0)}</dd></div>
        <div><dt>压缩命令</dt><dd>{(stats?.commands || 0).toLocaleString()}</dd></div>
        <div><dt>平均每条节省</dt><dd>{stats?.commands ? formatTokenCount(Math.round(saved / stats.commands)) : '—'}</dd></div>
        <div><dt>字节节省</dt><dd>{formatBytes(Math.max(0, (stats?.originalBytes || 0) - (stats?.compressedBytes || 0)))}</dd></div>
      </dl>
    </div>
    {stats && !stats.available && <p className="usage-note">当前主机未检测到 RTK，已有历史记录仍会保留。</p>}
    {!stats && <p className="usage-empty">暂无压缩统计</p>}
    {/* 账号维度：同一服务商可以登录多个账号，只按服务商名字聚合永远分不清是谁省下来的。 */}
    <section className="usage-rtk-accounts">
      <div className="usage-block-head">
        <div><h3>账号维度节省</h3><p>每个上游账号独立统计，同一服务商的多个账号分开计。</p></div>
        <span className="usage-note-inline">{savingAccounts.length} 个账号有压缩记录</span>
      </div>
      {accounts.length === 0 ? <div className="usage-empty compact">当前窗口还没有压缩记录</div> : <div className="usage-table-wrap">
        <table className="usage-data-table usage-rtk-table">
          <thead><tr>{['账号', '压缩命令', '原始 Token', '压缩后', '节省', '压缩率'].map(head => <th key={head}>{head}</th>)}</tr></thead>
          <tbody>{accounts.map(row => <tr key={row.providerId}>
            <td><ProviderRefLabel providerId={row.providerId} providers={providers} size={18} /></td>
            <td>{row.commands.toLocaleString()}</td>
            <td>{formatTokenCount(row.originalTokens)}</td>
            <td>{formatTokenCount(row.compressedTokens)}</td>
            <td><b className="usage-rtk-saved">{formatTokenCount(row.savedTokens)}</b></td>
            <td><div className="usage-rtk-rate"><span className="usage-rtk-rate-track"><i style={{width: `${Math.min(100, Math.max(row.commands ? 1.5 : 0, row.savedPercent))}%`}} /></span><small>{row.savedPercent.toFixed(1)}%</small></div></td>
          </tr>)}</tbody>
        </table>
      </div>}
    </section>
    {/* 「今天」只有一个数据点：单根柱子会被 flex 拉满整行，既难看又没有可比的趋势，
        这里直接说明情况，趋势交给 7 天以上的窗口。 */}
    {!!stats?.daily.length && (stats.daily.length > 1 ? <section className="usage-rtk-chart">
      <div className="usage-block-head"><div><h3>按日节省</h3><p>每天压缩掉的 Token 与压缩后实际发送量。</p></div></div>
      <div className="usage-chart-legend"><span><i style={{background: 'var(--usage-category-1)'}} />压缩节省</span><span><i style={{background: 'var(--usage-category-2)'}} />压缩后发送</span></div>
      <RTKSavingsChart points={stats.daily} />
    </section> : <p className="usage-note">「今天」只有单日数据，没有可对比的走势；切到 7 天以上可以看按日节省曲线。</p>)}
  </section>;
}

type Tab = 'overview' | 'requests' | 'context' | 'performance' | 'rtk';

export function UsagePage({ stats, providers, models, isNormalUser, from, to, serverTimeZone, loading, error, fetchedAt, onRangeChange, onRefresh, onNavigateLogs, onOpenLogs, rtkStats, rtkDays, onRTKDaysChange }: {
  stats: RequestStatsSnapshot | null; providers: Provider[]; models: Model[]; isNormalUser: boolean; from: string; to: string; serverTimeZone: string; loading: boolean; error: string; fetchedAt: Date | null;
  onRangeChange: (from: string, to: string) => void; onRefresh: () => void; onNavigateLogs: (target: LogTarget) => void; onOpenLogs: (filters: LogFilters) => void;
  rtkStats: RTKStatsResponse | null; rtkDays: number; onRTKDaysChange: (days: number) => void;
}) {
  const [tab, setTab] = useState<Tab>('overview');
  const [metric, setMetric] = useState<Metric>('tokens');
  const [priceOpen, setPriceOpen] = useState(false);
  const today = formatDateInZone(new Date(), serverTimeZone);
  const analysisTab = tab === 'requests' || tab === 'context' || tab === 'performance';
  const analysis = useUsageAnalysis(from, to, analysisTab);
  const matchesRange = !stats?.from || (formatLocalISODate(new Date(stats.from)) === from && formatLocalISODate(new Date(new Date(stats.to || stats.from).getTime() - 1)) === to);
  const ready = !!stats && matchesRange;
  const tabs: { id: Tab; name: string; note: string }[] = [
    {id: 'overview', name: '概览', note: '总量、趋势与来源'},
    {id: 'requests', name: '请求', note: '调用明细与耗时'},
    {id: 'context', name: '上下文', note: '输入与缓存结构'},
    {id: 'performance', name: '性能', note: '模型与上游效率'},
    ...(!isNormalUser ? [{id: 'rtk' as Tab, name: 'RTK', note: '工具输出压缩'}] : []),
  ];
  function preset(days: number) {
    const start = new Date();
    start.setDate(start.getDate() - days + 1);
    onRangeChange(formatLocalISODate(start), today);
  }
  function refreshAll() {
    onRefresh();
    if (analysisTab) analysis.reload();
  }
  const exportButton = ready && tab === 'overview' ? <button className="mini-btn" type="button" onClick={() => exportUsage(stats!, from, to)}>导出 CSV 报表</button> : null;
  // 价格表只有管理员读得动（/__prices 走 requireAdmin），普通用户不显示入口。
  const priceButton = !isNormalUser ? <button className="mini-btn" type="button" onClick={() => setPriceOpen(true)}>价格表</button> : null;
  const priceCoverage = (stats?.range?.byModel || []).map(row => ({
    model: row.model,
    requestCount: row.requestCount || 0,
    pricedCount: row.pricedCount || 0,
  }));
  const syncing = loading || analysis.refreshing;
  return <section className="section-full usage-page"><div className="card panel">
    <div className="panel-header usage-hero-header">
      <div>
        <h2 className="panel-title">用量统计</h2>
        <p className="panel-desc">按区间汇总请求、Token、缓存与性能；概览汇总永久保留，请求、上下文与性能明细受日志保留天数限制。</p>
      </div>
      <div className="usage-header-actions">
        <span className={`usage-sync-state${syncing ? ' is-syncing' : ''}`}>{syncing ? <><i className="live-dot" aria-hidden="true" />更新中…</> : fetchedAt ? `更新于 ${fetchedAt.toLocaleTimeString()}` : ''}</span>
        {exportButton}
        {priceButton}
        <button className="mini-btn" type="button" disabled={loading} onClick={refreshAll}>刷新</button>
      </div>
    </div>
    <div className="usage-page-tabs" role="tablist" aria-label="用量模块">{tabs.map(t => <button key={t.id} type="button" role="tab" aria-selected={tab === t.id} className={tab === t.id ? 'active' : ''} onClick={() => setTab(t.id)}><span>{t.name}</span><small>{t.note}</small></button>)}</div>
    {tab !== 'rtk' && <div className="usage-toolbar">
      <div className="usage-segments">{[{ days: 1, label: '今天' }, { days: 7, label: '7 天' }, { days: 30, label: '30 天' }, { days: 90, label: '90 天' }].map(item => {
        const start = new Date(); start.setDate(start.getDate() - item.days + 1);
        const active = from === formatLocalISODate(start) && to === today;
        return <button key={item.days} type="button" className={active ? 'active' : ''} onClick={() => preset(item.days)}>{item.label}</button>;
      })}</div>
      <div className="usage-date-inputs"><input type="date" aria-label="用量开始日期" value={from} max={to} onChange={e => { if (e.target.value) onRangeChange(e.target.value, to); }} /><span>至</span><input type="date" aria-label="用量结束日期" value={to} min={from} max={today} onChange={e => { if (e.target.value) onRangeChange(from, e.target.value); }} /></div>
      <div className="usage-toolbar-spacer" />
    </div>}
    {error && <p className="usage-error" role="alert">{error}</p>}
    {!ready ? <div className="usage-empty">{loading ? '正在读取所选区间…' : '所选区间暂无可用统计，请刷新重试。'}</div> : <div aria-busy={syncing} className="usage-content">
      {tab === 'overview' && <Overview stats={stats!} providers={providers} models={models} isNormalUser={isNormalUser} metric={metric} onMetricChange={setMetric} serverTimeZone={serverTimeZone} />}
      {tab === 'rtk' && <div className="usage-rtk-tab"><CompressionUsage stats={rtkStats} days={rtkDays} onDaysChange={onRTKDaysChange} providers={providers} /></div>}
      {analysisTab && <>
        <div className="usage-scope-note">
          <b>明细来自保留日志：当前保留 {analysis.data?.retentionDays || '—'} 天。</b>
          <span>{analysis.data ? `所选区间可分析 ${analysis.data.total.toLocaleString()} 次调用。` : '正在读取分析明细…'} 概览的每日汇总永久保留，不受保留期影响。</span>
          {analysis.fetchedAt && <small>分析明细更新于 {analysis.fetchedAt.toLocaleTimeString()}</small>}
        </div>
        {analysis.error && <p className="usage-error" role="alert">分析明细加载失败：{analysis.error}</p>}
        {(analysis.loading || analysis.stale) ? <div className="usage-empty">正在读取分析明细…</div> : tab === 'requests' ? <RequestAnalysisPanel rows={analysis.data?.items || []} providers={providers} onOpenLogs={onOpenLogs} /> : tab === 'context' ? <ContextAnalysisPanel rows={analysis.data?.items || []} providers={providers} /> : <PerformanceAnalysisPanel rows={analysis.data?.items || []} providers={providers} onNavigateLogs={onNavigateLogs} />}
      </>}
      <p className="usage-note">统计网关记录的请求。账户订阅额度在 Provider 页面查看；请求、上下文与性能明细受日志保留天数限制，升级前已过期日志的分类与状态可能不完整。RTK 压缩统计按自己的时间窗独立计算。成本按「价格表」里的美元单价逐请求算出，只增不改：改价之后的请求用新价，历史仍按当时价格记账。</p>
    </div>}
    {priceOpen ? <PriceTablePanel providers={providers} models={models} modelCoverage={priceCoverage} onClose={() => setPriceOpen(false)} /> : null}
  </div></section>;
}
