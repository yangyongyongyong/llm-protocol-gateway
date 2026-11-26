import {Fragment, useEffect, useMemo, useState} from 'react';
import type {Provider, ProviderModelDayStats, RequestStatsSnapshot} from '../types';
import {
  buildCostReportGroups,
  type CostSortKey,
  formatCostCell,
  formatCostCoverage,
  formatCostMicros,
  formatTokenCount,
  providerUsageLabel,
} from '../lib';
import {Metric} from './charts';
import {ProviderRefLabel} from './provider_label';

type ViewMode = 'grouped' | 'flat';

/** 渲染三态计价覆盖率角标：未计价 / 部分已计价 / 全部已计价。 */
function CostCoverageBadge({pricedCount, requestCount}: { pricedCount?: number; requestCount: number }) {
  const priced = pricedCount || 0;
  if (!requestCount || priced <= 0) {
    return <span className="cost-badge unpriced" title="当前区间没有已计价请求，未配置单价前费用留空">未计价</span>;
  }
  if (priced < requestCount) {
    return (
      <span className="cost-badge partial" title={formatCostCoverage(priced, requestCount)}>
        已计价 {priced.toLocaleString()}/{requestCount.toLocaleString()}
      </span>
    );
  }
  return <span className="cost-badge full" title="区间内全部请求均已计价">全部已计价</span>;
}

/** 渲染单行成本金额：未计价时留空（显示 —），已计价即便为 $0 也明确显示 $0.0000。 */
function CostAmountCell({costMicros, pricedCount}: { costMicros?: number; pricedCount?: number }) {
  const text = formatCostCell(costMicros, pricedCount);
  if (!text) {
    return <span className="cost-amount-empty" title="未配置价格或尚无已计价请求">—</span>;
  }
  return <span className="cost-amount-value font-mono">{text}</span>;
}

/** 渲染已计价请求的均价（单次请求成本）。 */
function CostPerRequestCell({costMicros, pricedCount}: { costMicros?: number; pricedCount?: number }) {
  const priced = pricedCount || 0;
  if (priced <= 0) return <span className="cost-amount-empty">—</span>;
  return <span className="font-mono">{formatCostMicros(Math.round((costMicros || 0) / priced))}</span>;
}

/**
 * 成本 BI 报表组件：直接消费后端预聚合的 stats.range.byProviderModel，
 * 展示 Provider -> Model 分层或平铺明细的区间累计费用、Token 消耗与计价覆盖率。
 */
export function CostReportPanel({
  stats,
  providers,
  isNormalUser,
  onEditPrice,
}: {
  stats: RequestStatsSnapshot;
  providers: Provider[];
  isNormalUser: boolean;
  onEditPrice?: (target: { providerId: string; modelId: string }) => void;
}) {
  const [viewMode, setViewMode] = useState<ViewMode>('grouped');
  const [sortBy, setSortBy] = useState<CostSortKey>('cost');
  const [onlyPriced, setOnlyPriced] = useState(false);
  const [query, setQuery] = useState('');
  const [collapsedProviders, setCollapsedProviders] = useState<Record<string, boolean>>({});

  const rows: ProviderModelDayStats[] = stats.range?.byProviderModel || [];
  const totalRequests = stats.range?.total.requestCount || 0;
  const totalPriced = stats.range?.total.pricedCount || 0;
  const totalCost = stats.range?.total.costMicros || 0;

  const {groups, flatRows} = useMemo(
    () => buildCostReportGroups(rows, providers, {query, onlyPriced, sortBy}),
    [rows, providers, query, onlyPriced, sortBy],
  );

  // 统计有已计价数据的 Provider 与模型数量，给顶部 KPI 使用。
  const pricedSummary = useMemo(() => {
    const providerSet = new Set<string>();
    const modelSet = new Set<string>();
    for (const r of rows) {
      if ((r.pricedCount || 0) > 0) {
        providerSet.add(r.providerId);
        modelSet.add(r.model);
      }
    }
    return {
      pricedProviders: providerSet.size,
      totalProviders: new Set(rows.map(r => r.providerId)).size,
      pricedModels: modelSet.size,
      totalModels: new Set(rows.map(r => r.model)).size,
    };
  }, [rows]);

  // 搜索过滤时自动展开所有匹配分组，避免用户看到搜索结果却被折叠看不到。
  useEffect(() => {
    if (query.trim()) {
      setCollapsedProviders({});
    }
  }, [query]);

  /** 切换单个 Provider 分组的展开/收起状态。 */
  function toggleGroup(providerId: string) {
    setCollapsedProviders(prev => ({...prev, [providerId]: !prev[providerId]}));
  }

  /** 一键全部展开或全部收起 Provider 分组。 */
  function setAllCollapsed(collapsed: boolean) {
    if (!collapsed) {
      setCollapsedProviders({});
      return;
    }
    const next: Record<string, boolean> = {};
    for (const g of groups) {
      next[g.providerId] = true;
    }
    setCollapsedProviders(next);
  }

  const coverageBadge = !totalRequests
    ? undefined
    : totalPriced === 0
      ? '未计价'
      : totalPriced < totalRequests
        ? '部分未计价'
        : '全部已计价';
  const coverageTone = coverageBadge === '全部已计价' ? 'green' : coverageBadge === '部分未计价' ? 'amber' : 'red';
  const hasAnyExpanded = groups.some(g => !collapsedProviders[g.providerId]);

  return (
    <div className="cost-report">
      <div className="cost-kpi-grid">
        <Metric
          label="区间累计已计价成本"
          value={totalPriced > 0 ? formatCostMicros(totalCost) : '—'}
          note={totalRequests ? formatCostCoverage(totalPriced, totalRequests) : '当前区间暂无请求'}
          badge={coverageBadge}
          badgeTone={coverageTone}
        />
        <Metric
          label="已计价请求均价"
          value={totalPriced > 0 ? formatCostMicros(Math.round(totalCost / totalPriced)) : '—'}
          note={totalPriced > 0 ? `基于 ${totalPriced.toLocaleString()} 次已计价请求` : '配置价格后展示单次均值'}
        />
        <Metric
          label="已计价 Provider"
          value={`${pricedSummary.pricedProviders} / ${pricedSummary.totalProviders}`}
          note="存在至少 1 条已计价请求的 Provider 数"
        />
        <Metric
          label="已计价模型维度"
          value={`${pricedSummary.pricedModels} / ${pricedSummary.totalModels}`}
          note="预聚合自 usage_daily_buckets，零日志扫库"
        />
      </div>

      <div className="cost-toolbar">
        <div className="usage-segments" role="group" aria-label="成本报表视图模式">
          <button type="button" className={viewMode === 'grouped' ? 'active' : ''} onClick={() => setViewMode('grouped')}>
            按 Provider 分组
          </button>
          <button type="button" className={viewMode === 'flat' ? 'active' : ''} onClick={() => setViewMode('flat')}>
            平铺明细表
          </button>
        </div>

        {viewMode === 'grouped' && groups.length > 0 ? (
          <button type="button" className="mini-btn" onClick={() => setAllCollapsed(hasAnyExpanded)}>
            {hasAnyExpanded ? '全部收起' : '全部展开'}
          </button>
        ) : null}

        <input
          type="search"
          className="cost-search"
          placeholder="搜索 Provider 或模型名称…"
          value={query}
          onChange={event => setQuery(event.target.value)}
        />

        <label className="cost-filter-check">
          <input type="checkbox" checked={onlyPriced} onChange={event => setOnlyPriced(event.target.checked)} />
          <span>仅看已计价</span>
        </label>

        <div className="usage-segments" role="group" aria-label="成本报表排序方式">
          <button type="button" className={sortBy === 'cost' ? 'active' : ''} onClick={() => setSortBy('cost')}>按费用</button>
          <button type="button" className={sortBy === 'tokens' ? 'active' : ''} onClick={() => setSortBy('tokens')}>按 Token</button>
          <button type="button" className={sortBy === 'requests' ? 'active' : ''} onClick={() => setSortBy('requests')}>按请求</button>
        </div>
      </div>

      {rows.length === 0 ? (
        <div className="usage-empty">当前区间暂无 Provider × 模型维度用量数据。</div>
      ) : flatRows.length === 0 ? (
        <div className="usage-empty">没有符合当前筛选条件的成本记录（可取消「仅看已计价」或清空搜索）。</div>
      ) : (
        <div className="usage-table-wrap">
          <table className="usage-data-table cost-table">
            <thead>
              <tr>
                <th>{viewMode === 'grouped' ? 'Provider / 模型' : 'Provider'}</th>
                {viewMode === 'flat' ? <th>模型</th> : null}
                <th className="num">请求数</th>
                <th className="num">输入（含缓存）</th>
                <th className="num">输出</th>
                <th className="num">缓存读 / 写</th>
                <th className="num">区间累计费用</th>
                <th className="num">已计价均价</th>
                <th>计价覆盖</th>
                {!isNormalUser && onEditPrice ? <th style={{textAlign: 'right'}}>操作</th> : null}
              </tr>
            </thead>
            <tbody>
              {viewMode === 'grouped'
                ? groups.map(group => {
                  const collapsed = Boolean(collapsedProviders[group.providerId]);
                  return (
                    <Fragment key={group.providerId}>
                      <tr className="cost-group-row" onClick={() => toggleGroup(group.providerId)}>
                        <td>
                          <div className="cost-group-cell">
                            <span className={`cost-caret${collapsed ? '' : ' is-open'}`} aria-hidden="true">▸</span>
                            <ProviderRefLabel providerId={group.providerId} providers={providers} size={18} />
                            <span className="cost-model-count">{group.models.length} 个模型</span>
                          </div>
                        </td>
                        <td className="num font-mono">{group.requestCount.toLocaleString()}</td>
                        <td className="num font-mono">{formatTokenCount(group.inputTokens)}</td>
                        <td className="num font-mono">{formatTokenCount(group.outputTokens)}</td>
                        <td className="num font-mono">
                          {formatTokenCount(group.cacheTokens)} / {formatTokenCount(group.cacheWriteTokens)}
                        </td>
                        <td className="num">
                          <CostAmountCell costMicros={group.costMicros} pricedCount={group.pricedCount} />
                        </td>
                        <td className="num">
                          <CostPerRequestCell costMicros={group.costMicros} pricedCount={group.pricedCount} />
                        </td>
                        <td>
                          <CostCoverageBadge pricedCount={group.pricedCount} requestCount={group.requestCount} />
                        </td>
                        {!isNormalUser && onEditPrice ? <td /> : null}
                      </tr>
                      {!collapsed && group.models.map(item => (
                        <tr className="cost-child-row" key={`${item.providerId}::${item.model}`}>
                          <td>
                            <div className="cost-child-model">
                              <span className="cost-tree-branch" aria-hidden="true">└</span>
                              <span className="font-mono" title={item.model}>{item.model}</span>
                            </div>
                          </td>
                          <td className="num font-mono">{item.requestCount.toLocaleString()}</td>
                          <td className="num font-mono">{formatTokenCount(item.inputTokens)}</td>
                          <td className="num font-mono">{formatTokenCount(item.outputTokens)}</td>
                          <td className="num font-mono">
                            {formatTokenCount(item.cacheTokens)} / {formatTokenCount(item.cacheWriteTokens || 0)}
                          </td>
                          <td className="num">
                            <CostAmountCell costMicros={item.costMicros} pricedCount={item.pricedCount} />
                          </td>
                          <td className="num">
                            <CostPerRequestCell costMicros={item.costMicros} pricedCount={item.pricedCount} />
                          </td>
                          <td>
                            <CostCoverageBadge pricedCount={item.pricedCount} requestCount={item.requestCount} />
                          </td>
                          {!isNormalUser && onEditPrice ? (
                            <td style={{textAlign: 'right'}}>
                              <button
                                type="button"
                                className="mini-btn"
                                onClick={() => onEditPrice({providerId: item.providerId, modelId: item.model})}
                              >
                                {item.pricedCount ? '调价' : '设价'}
                              </button>
                            </td>
                          ) : null}
                        </tr>
                      ))}
                    </Fragment>
                  );
                })
                : flatRows.map(item => (
                  <tr key={`${item.providerId}::${item.model}`}>
                    <td title={providerUsageLabel(item.providerId, providers)}>
                      <ProviderRefLabel providerId={item.providerId} providers={providers} size={18} />
                    </td>
                    <td><span className="font-mono" title={item.model}>{item.model}</span></td>
                    <td className="num font-mono">{item.requestCount.toLocaleString()}</td>
                    <td className="num font-mono">{formatTokenCount(item.inputTokens)}</td>
                    <td className="num font-mono">{formatTokenCount(item.outputTokens)}</td>
                    <td className="num font-mono">
                      {formatTokenCount(item.cacheTokens)} / {formatTokenCount(item.cacheWriteTokens || 0)}
                    </td>
                    <td className="num">
                      <CostAmountCell costMicros={item.costMicros} pricedCount={item.pricedCount} />
                    </td>
                    <td className="num">
                      <CostPerRequestCell costMicros={item.costMicros} pricedCount={item.pricedCount} />
                    </td>
                    <td>
                      <CostCoverageBadge pricedCount={item.pricedCount} requestCount={item.requestCount} />
                    </td>
                    {!isNormalUser && onEditPrice ? (
                      <td style={{textAlign: 'right'}}>
                        <button
                          type="button"
                          className="mini-btn"
                          onClick={() => onEditPrice({providerId: item.providerId, modelId: item.model})}
                        >
                          {item.pricedCount ? '调价' : '设价'}
                        </button>
                      </td>
                    ) : null}
                  </tr>
                ))}
            </tbody>
          </table>
        </div>
      )}

      <p className="usage-note">
        本报表直接读取预聚合的 Provider × 模型日桶（永久保留，无需扫描明细日志）。未配置价格的组合费用显示为「—」并标注「未计价」，不会误算成 $0；
        配置单价后仅对后续新请求生效，如需把保留日志中尚未计价的请求补充入账，请在「价格表 → 补算区间未计价」一键触发。
      </p>
    </div>
  );
}
