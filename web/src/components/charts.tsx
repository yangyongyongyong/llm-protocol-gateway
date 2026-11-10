// @generated-from main.tsx — 由重构脚本拆分生成，请直接维护本文件。
import React from 'react';
import {
    copyToClipboard,
    formatBytes,
    formatCostMicros,
    formatEnCompact,
    formatLocalISODate,
    formatTokenCount,
    normalizePromptTokenStats,
    pickUsageXTickIndexes,
    providerUsageLabel,
    shortGatewayId,
} from '../lib';
import {
    DailyRequestPoint,
    HourlyBucketStats,
    LogEntry,
    Model,
    Provider,
    RequestStatsSnapshot,
    RTKStatsDailyPoint,
    StatusBucketStats
} from '../types';
import {resolveProviderIcon} from '../provider_presets';
import {BrandIcon} from './ui';

/** 通用经典调色板（支持暗黑与浅色模式） */
export const CHART_PALETTE = [
  '#6366f1', // Indigo
  '#06b6d4', // Cyan
  '#10b981', // Emerald
  '#f59e0b', // Amber
  '#ec4899', // Pink
  '#8b5cf6', // Violet
  '#3b82f6', // Blue
  '#14b8a6', // Teal
  '#f97316', // Orange
  '#64748b', // Slate
];

/** 状态码语义配色 */
export const STATUS_COLORS: Record<string, string> = {
  '2xx': '#10b981', // 成功 绿
  '4xx': '#f59e0b', // 客户端 橙
  '5xx': '#ef4444', // 服务端 红
  other: '#64748b', // 其他 灰
};

/** 计算平滑三次贝塞尔曲线 SVG 路径 */
export function createSmoothPath(points: Array<{ x: number; y: number }>): string {
  if (points.length === 0) return '';
  if (points.length === 1) return `M ${points[0].x.toFixed(1)} ${points[0].y.toFixed(1)}`;
  if (points.length === 2) {
    return `M ${points[0].x.toFixed(1)} ${points[0].y.toFixed(1)} L ${points[1].x.toFixed(1)} ${points[1].y.toFixed(1)}`;
  }

  let d = `M ${points[0].x.toFixed(1)} ${points[0].y.toFixed(1)}`;
  for (let i = 0; i < points.length - 1; i++) {
    const p0 = points[Math.max(0, i - 1)];
    const p1 = points[i];
    const p2 = points[i + 1];
    const p3 = points[Math.min(points.length - 1, i + 2)];

    const cp1x = p1.x + (p2.x - p0.x) / 6;
    const cp1y = p1.y + (p2.y - p0.y) / 6;
    const cp2x = p2.x - (p3.x - p1.x) / 6;
    const cp2y = p2.y - (p3.y - p1.y) / 6;

    d += ` C ${cp1x.toFixed(1)} ${cp1y.toFixed(1)}, ${cp2x.toFixed(1)} ${cp2y.toFixed(1)}, ${p2.x.toFixed(1)} ${p2.y.toFixed(1)}`;
  }
  return d;
}

/** 生成平滑面积图闭合路径 */
export function createAreaPath(points: Array<{ x: number; y: number }>, baselineY: number): string {
  if (points.length === 0) return '';
  const linePath = createSmoothPath(points);
  const first = points[0];
  const last = points[points.length - 1];
  return `${linePath} L ${last.x.toFixed(1)} ${baselineY.toFixed(1)} L ${first.x.toFixed(1)} ${baselineY.toFixed(1)} Z`;
}

/** 辅助生成扇形环段路径 (Donut Slice) */
function describeDonutSlice(
  cx: number,
  cy: number,
  rInner: number,
  rOuter: number,
  startAngle: number,
  endAngle: number,
): string {
  const isFullCircle = Math.abs(endAngle - startAngle) >= Math.PI * 1.999;
  if (isFullCircle) {
    return `M ${cx} ${cy - rOuter} A ${rOuter} ${rOuter} 0 1 0 ${cx} ${cy + rOuter} A ${rOuter} ${rOuter} 0 1 0 ${cx} ${cy - rOuter} M ${cx} ${cy - rInner} A ${rInner} ${rInner} 0 1 1 ${cx} ${cy + rInner} A ${rInner} ${rInner} 0 1 1 ${cx} ${cy - rInner} Z`;
  }

  const x1Outer = cx + rOuter * Math.cos(startAngle);
  const y1Outer = cy + rOuter * Math.sin(startAngle);
  const x2Outer = cx + rOuter * Math.cos(endAngle);
  const y2Outer = cy + rOuter * Math.sin(endAngle);

  const x1Inner = cx + rInner * Math.cos(endAngle);
  const y1Inner = cy + rInner * Math.sin(endAngle);
  const x2Inner = cx + rInner * Math.cos(startAngle);
  const y2Inner = cy + rInner * Math.sin(startAngle);

  const largeArc = endAngle - startAngle > Math.PI ? 1 : 0;

  return [
    `M ${x1Outer.toFixed(2)} ${y1Outer.toFixed(2)}`,
    `A ${rOuter} ${rOuter} 0 ${largeArc} 1 ${x2Outer.toFixed(2)} ${y2Outer.toFixed(2)}`,
    `L ${x1Inner.toFixed(2)} ${y1Inner.toFixed(2)}`,
    `A ${rInner} ${rInner} 0 ${largeArc} 0 ${x2Inner.toFixed(2)} ${y2Inner.toFixed(2)}`,
    'Z',
  ].join(' ');
}

/** 纯前端无依赖 CSV 报表导出 */
export function downloadCSV(filename: string, content: string) {
  const blob = new Blob(['\uFEFF' + content], { type: 'text/csv;charset=utf-8;' });
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = url;
  link.download = filename;
  document.body.appendChild(link);
  link.click();
  document.body.removeChild(link);
  URL.revokeObjectURL(url);
}

/** =========================================================================
 * 1. 现代化指标卡片组件 (Metric)
 * 支持 Sparkline 迷你走势波形、右上角语义状态徽章与环比辅助信息
 * ========================================================================= */
export function Metric({
  label,
  value,
  note,
  badge,
  badgeTone = 'blue',
  sparkline,
  sparklineColor = 'var(--accent)',
}: {
  label: string;
  value: string;
  note: string;
  badge?: string;
  badgeTone?: 'green' | 'amber' | 'blue' | 'purple' | 'red';
  sparkline?: number[];
  sparklineColor?: string;
}) {
  const sparkPoints = React.useMemo(() => {
    if (!sparkline || sparkline.length <= 1) return null;
    const min = Math.min(...sparkline);
    const max = Math.max(...sparkline, min + 1);
    const w = 68;
    const h = 24;
    return sparkline.map((v, i) => ({
      x: (i / (sparkline.length - 1)) * w,
      y: h - ((v - min) / (max - min)) * (h - 4) - 2,
    }));
  }, [sparkline]);

  const sparkPath = sparkPoints ? createSmoothPath(sparkPoints) : '';

  return (
    <div className="card metric modern-metric-card metric-card-fixed">
      <div className="metric-header-row">
        <span className="metric-label">{label}</span>
        {badge ? <span className={`metric-badge badge-${badgeTone}`}>{badge}</span> : null}
      </div>

      <div className="metric-main-row">
        <div className="metric-value font-mono">{value}</div>
        {sparkPoints ? (
          <div className="metric-sparkline-wrap" title="近期波动趋势">
            <svg viewBox="0 0 68 24" className="metric-sparkline-svg">
              <path
                d={sparkPath}
                fill="none"
                stroke={sparklineColor}
                strokeWidth="1.8"
                strokeLinecap="round"
                strokeLinejoin="round"
              />
            </svg>
          </div>
        ) : null}
      </div>

      <div className="metric-note">{note}</div>
    </div>
  );
}

/** =========================================================================
 * 2. 环形饼图组件 (UsageDonutChart)
 * 适用于协议分布、状态码构成、Provider份额等离散分类占比统计
 * ========================================================================= */
export function UsageDonutChart({
  title,
  subtitle,
  items,
  formatValue,
  totalLabel = '总计',
  totalOverride,
  onSelect,
  selectedKey,
  donutSize = 200,
}: {
  title: string;
  subtitle?: string;
  items: Array<{ label: string; value: number; color?: string; hint?: string; sub?: string }>;
  formatValue?: (val: number) => string;
  totalLabel?: string;
  totalOverride?: number;
  onSelect?: (label: string) => void;
  selectedKey?: string;
  donutSize?: number;
}) {
  const [hoveredIdx, setHoveredIdx] = React.useState<number | null>(null);

  const filtered = items.filter((it) => it.value > 0);
  const calculatedTotal = filtered.reduce((s, it) => s + it.value, 0);
  const total = totalOverride ?? calculatedTotal;

  const cx = 110;
  const cy = 110;
  const rOuterBase = 84;
  const rInnerBase = 56;

  let currentAngle = -Math.PI / 2;
  const slices = filtered.map((item, idx) => {
    const fraction = total > 0 ? item.value / total : 0;
    const sliceAngle = fraction * 2 * Math.PI;
    const start = currentAngle;
    const end = currentAngle + sliceAngle;
    currentAngle = end;

    const isHovered = hoveredIdx === idx;
    const isSelected = selectedKey === item.label;
    const color = item.color || CHART_PALETTE[idx % CHART_PALETTE.length];

    const rOuter = isHovered || isSelected ? rOuterBase + 5 : rOuterBase;
    const rInner = isHovered || isSelected ? rInnerBase - 2 : rInnerBase;

    const gap = filtered.length > 1 ? 0.02 : 0;
    const adjustedStart = start + gap / 2;
    const adjustedEnd = Math.max(adjustedStart, end - gap / 2);

    const path = describeDonutSlice(cx, cy, rInner, rOuter, adjustedStart, adjustedEnd);
    const pct = fraction * 100;

    return {
      item,
      idx,
      fraction,
      pctStr: pct > 0 && pct < 0.1 ? '<0.1%' : `${pct.toFixed(1)}%`,
      color,
      path,
      isHovered,
      isSelected,
    };
  });

  const activeSlice = hoveredIdx != null && slices[hoveredIdx] ? slices[hoveredIdx] : null;

  return (
    <div className="usage-chart-card usage-donut-card">
      <div className="usage-section-title-row">
        <div>
          <div className="usage-section-title">{title}</div>
          {subtitle ? <div className="usage-section-subtitle">{subtitle}</div> : null}
        </div>
      </div>

      {filtered.length === 0 ? (
        <div className="empty-state compact">暂无占比数据</div>
      ) : (
        <div className="donut-content-layout">
          <div className="donut-svg-wrap" style={{ width: donutSize, height: donutSize }}>
            <svg viewBox="0 0 220 220" className="donut-svg" role="img" aria-label={title}>
              {slices.map((sl) => (
                <path
                  key={sl.idx}
                  d={sl.path}
                  fill={sl.color}
                  opacity={activeSlice && !sl.isHovered && !sl.isSelected ? 0.45 : 1}
                  className={`donut-slice-path${sl.isSelected ? ' selected' : ''}`}
                  style={{
                    cursor: onSelect ? 'pointer' : 'default',
                    transition: 'all 0.18s cubic-bezier(0.16, 1, 0.3, 1)',
                  }}
                  onMouseEnter={() => setHoveredIdx(sl.idx)}
                  onMouseLeave={() => setHoveredIdx(null)}
                  onClick={() => onSelect?.(sl.item.label)}
                >
                  <title>{`${sl.item.sub ? `${sl.item.label} · ${sl.item.sub}` : sl.item.label}: ${formatValue ? formatValue(sl.item.value) : sl.item.value} (${sl.pctStr})`}</title>
                </path>
              ))}

              {/* 圆心文本展示 */}
              <g className="donut-center-group" pointerEvents="none">
                {activeSlice ? (
                  <>
                    <text x={cx} y={cy - 12} textAnchor="middle" className="donut-center-sub" fontSize="11" fill="var(--muted)">
                      {activeSlice.item.label}
                    </text>
                    <text x={cx} y={cy + 8} textAnchor="middle" className="donut-center-val font-mono" fontSize="17" fontWeight="800" fill="var(--text)">
                      {formatValue ? formatValue(activeSlice.item.value) : activeSlice.item.value}
                    </text>
                    <text x={cx} y={cy + 24} textAnchor="middle" className="donut-center-pct font-mono" fontSize="12" fontWeight="700" fill={activeSlice.color}>
                      {activeSlice.pctStr}
                    </text>
                  </>
                ) : (
                  <>
                    <text x={cx} y={cy - 10} textAnchor="middle" className="donut-center-sub" fontSize="11" fill="var(--muted)">
                      {totalLabel}
                    </text>
                    <text x={cx} y={cy + 10} textAnchor="middle" className="donut-center-val font-mono" fontSize="18" fontWeight="800" fill="var(--text)">
                      {formatValue ? formatValue(total) : total}
                    </text>
                    <text x={cx} y={cy + 25} textAnchor="middle" className="donut-center-count" fontSize="11" fill="var(--subtle)">
                      共 {filtered.length} 个分类
                    </text>
                  </>
                )}
              </g>
            </svg>
          </div>

          {/* 右侧详细图例列表 */}
          <div className="donut-legend-list">
            {slices.map((sl) => (
              <div
                key={sl.idx}
                className={`donut-legend-row${sl.isSelected ? ' selected' : ''}${sl.isHovered ? ' hovered' : ''}`}
                style={{ cursor: onSelect ? 'pointer' : 'default' }}
                onMouseEnter={() => setHoveredIdx(sl.idx)}
                onMouseLeave={() => setHoveredIdx(null)}
                onClick={() => onSelect?.(sl.item.label)}
              >
                <span className="donut-legend-dot" style={{ backgroundColor: sl.color }} />
                <span
                  className={`donut-legend-label${sl.item.sub ? ' stacked' : ''}`}
                  title={sl.item.sub ? `${sl.item.label} · ${sl.item.sub}` : sl.item.label}
                >
                  <span className="donut-legend-name">{sl.item.label}</span>
                  {sl.item.sub ? <span className="donut-legend-sub">{sl.item.sub}</span> : null}
                </span>
                <span className="donut-legend-pct-badge" style={{ color: sl.color }}>
                  {sl.pctStr}
                </span>
                <span className="donut-legend-value">
                  {formatValue ? formatValue(sl.item.value) : sl.item.value}
                </span>
              </div>
            ))}
            {filtered.length === 1 && (
              <div className="donut-single-note">
                ✨ 单一分类承载本区间 100% 全部算力与请求
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  );
}

/** =========================================================================
 * 3. 增强版柱线复合时序走势图 (UsageComboTrendChart)
 * 支持【业务规模复合 (请求+Token)】、【APM 服务性能 (延迟+TTFT)】、【仅 Token】、【仅请求数】
 * ========================================================================= */
export function UsageComboTrendChart({
  title = '业务用量与 APM 时序走势',
  points,
  onPickDay,
  selectedDay,
}: {
  title?: string;
  points: DailyRequestPoint[];
  onPickDay?: (date: string) => void;
  selectedDay?: string;
}) {
  const [viewMode, setViewMode] = React.useState<'combo' | 'tokens' | 'requests' | 'latency'>('combo');
  const [hoverIndex, setHoverIndex] = React.useState<number | null>(null);

  const width = 960;
  const height = 270;
  const padLeft = 66;
  const padRight = 66;
  const padTop = 32;
  const padBottom = 44;
  const plotWidth = width - padLeft - padRight;
  const plotHeight = height - padTop - padBottom;

  const tokenValues = points.map((p) => (p.inputTokens || 0) + (p.outputTokens || 0));
  const requestValues = points.map((p) => p.requestCount || 0);
  const latencyValues = points.map((p) => Math.round(p.avgLatencyMs || 0));
  const ttftValues = points.map((p) => Math.round(p.avgTtftMs || 0));

  const tokenMax = Math.max(1, ...tokenValues);
  const requestMax = Math.max(1, ...requestValues);
  const latencyMax = Math.max(10, ...latencyValues);
  const ttftMax = Math.max(10, ...ttftValues);

  const totalTokens = tokenValues.reduce((s, v) => s + v, 0);
  const totalRequests = requestValues.reduce((s, v) => s + v, 0);
  const validLatencies = latencyValues.filter((v) => v > 0);
  const avgOverallLatency = validLatencies.length ? Math.round(validLatencies.reduce((s, v) => s + v, 0) / validLatencies.length) : 0;

  const xTickIndexes = pickUsageXTickIndexes(points.length);

  // 坐标映射
  const xAt = (i: number) => {
    if (points.length <= 1) return padLeft + plotWidth / 2;
    return padLeft + (i / (points.length - 1)) * plotWidth;
  };

  const yToken = (v: number) => height - padBottom - (v / tokenMax) * plotHeight;
  const yRequest = (v: number) => height - padBottom - (v / requestMax) * plotHeight;
  const yLatency = (v: number) => height - padBottom - (v / latencyMax) * plotHeight;
  const yTtft = (v: number) => height - padBottom - (v / ttftMax) * plotHeight;

  // 柱体宽与位置
  const barSlotWidth = points.length <= 1 ? 40 : plotWidth / points.length;
  const barWidth = Math.max(6, Math.min(26, barSlotWidth * 0.58));

  // 路径生成
  const tokenPoints = points.map((_, i) => ({ x: xAt(i), y: yToken(tokenValues[i]) }));
  const tokenLinePath = createSmoothPath(tokenPoints);
  const tokenAreaPath = createAreaPath(tokenPoints, height - padBottom);

  const latencyPoints = points.map((_, i) => ({ x: xAt(i), y: yLatency(latencyValues[i]) }));
  const latencyLinePath = createSmoothPath(latencyPoints);
  const latencyAreaPath = createAreaPath(latencyPoints, height - padBottom);

  const ttftPoints = points.map((_, i) => ({ x: xAt(i), y: yTtft(ttftValues[i]) }));
  const ttftLinePath = createSmoothPath(ttftPoints);

  const hasData = points.length > 0 && (totalTokens > 0 || totalRequests > 0);

  function handleMouseMove(e: React.MouseEvent<SVGSVGElement>) {
    const rect = e.currentTarget.getBoundingClientRect();
    const mouseX = ((e.clientX - rect.left) / rect.width) * width;
    if (points.length === 0) return;
    const clampedX = Math.max(padLeft, Math.min(width - padRight, mouseX));
    const ratio = (clampedX - padLeft) / Math.max(1, plotWidth);
    const idx = Math.min(points.length - 1, Math.max(0, Math.round(ratio * (points.length - 1))));
    setHoverIndex(idx);
  }

  const activePoint = hoverIndex != null && points[hoverIndex] ? points[hoverIndex] : null;

  return (
    <div className="usage-chart-card usage-combo-card">
      <div className="usage-combo-header">
        <div>
          <div className="usage-combo-title">{title}</div>
          <div className="usage-combo-subtitle">
            {viewMode === 'latency'
              ? `平均耗时走势：区间往返耗时均值 ${avgOverallLatency} ms`
              : `区间统计：${formatEnCompact(totalRequests)} 次请求 · ${formatTokenCount(totalTokens)} tokens`}
          </div>
        </div>

        {/* 模式切换与图例 */}
        <div className="usage-combo-controls">
          <div className="usage-segmented-toggle">
            <button
              type="button"
              className={viewMode === 'combo' ? 'active' : ''}
              onClick={() => setViewMode('combo')}
            >
              业务走势
            </button>
            <button
              type="button"
              className={viewMode === 'tokens' ? 'active' : ''}
              onClick={() => setViewMode('tokens')}
            >
              Token 算力
            </button>
            <button
              type="button"
              className={viewMode === 'requests' ? 'active' : ''}
              onClick={() => setViewMode('requests')}
            >
              请求频次
            </button>
            <button
              type="button"
              className={viewMode === 'latency' ? 'active' : ''}
              onClick={() => setViewMode('latency')}
              title="查看往返延迟 Latency 与首字响应 TTFT"
            >
              ⚡ APM 延迟
            </button>
          </div>

          <div className="usage-combo-legends">
            {viewMode === 'latency' ? (
              <>
                <span className="combo-legend-item">
                  <i className="combo-line-icon" style={{ background: '#a855f7' }} /> 往返延迟 (左轴)
                </span>
                <span className="combo-legend-item">
                  <i className="combo-line-icon" style={{ background: '#f59e0b' }} /> 首字 TTFT (右轴)
                </span>
              </>
            ) : (
              <>
                {viewMode !== 'tokens' && (
                  <span className="combo-legend-item">
                    <i className="combo-bar-icon" /> 请求数（左轴）
                  </span>
                )}
                {viewMode !== 'requests' && (
                  <span className="combo-legend-item">
                    <i className="combo-line-icon" /> Token 消耗（右轴）
                  </span>
                )}
              </>
            )}
          </div>
        </div>
      </div>

      {!hasData ? (
        <div className="empty-state compact">当前所选区间内暂无请求及 Token 数据</div>
      ) : (
        <div className="usage-combo-plot-wrap">
          <svg
            viewBox={`0 0 ${width} ${height}`}
            className="usage-combo-svg"
            role="img"
            aria-label={title}
            onMouseMove={handleMouseMove}
            onMouseLeave={() => setHoverIndex(null)}
          >
            <defs>
              <linearGradient id="token-area-gradient" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stopColor="#6366f1" stopOpacity="0.32" />
                <stop offset="90%" stopColor="#6366f1" stopOpacity="0.02" />
              </linearGradient>
              <linearGradient id="latency-area-gradient" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stopColor="#a855f7" stopOpacity="0.25" />
                <stop offset="90%" stopColor="#a855f7" stopOpacity="0.02" />
              </linearGradient>
              <linearGradient id="request-bar-gradient" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stopColor="#38bdf8" stopOpacity="0.9" />
                <stop offset="100%" stopColor="#0284c7" stopOpacity="0.45" />
              </linearGradient>
            </defs>

            {/* 背景横向参考刻度线 */}
            {[0, 0.33, 0.66, 1].map((ratio) => {
              const y = height - padBottom - ratio * plotHeight;
              return (
                <g key={`grid-${ratio}`}>
                  <line
                    x1={padLeft}
                    y1={y}
                    x2={width - padRight}
                    y2={y}
                    stroke="var(--border)"
                    strokeDasharray="2 3"
                    strokeWidth="1"
                    opacity="0.65"
                  />
                  {viewMode === 'latency' ? (
                    <>
                      <text
                        x={padLeft - 8}
                        y={y + 3.5}
                        textAnchor="end"
                        fontSize="10"
                        fill="#a855f7"
                        fontFamily="var(--font-mono, monospace)"
                      >
                        {Math.round(latencyMax * ratio)} ms
                      </text>
                      <text
                        x={width - padRight + 8}
                        y={y + 3.5}
                        textAnchor="start"
                        fontSize="10"
                        fill="#f59e0b"
                        fontFamily="var(--font-mono, monospace)"
                      >
                        {Math.round(ttftMax * ratio)} ms
                      </text>
                    </>
                  ) : (
                    <>
                      {viewMode !== 'tokens' && (
                        <text
                          x={padLeft - 8}
                          y={y + 3.5}
                          textAnchor="end"
                          fontSize="10"
                          fill="var(--subtle)"
                          fontFamily="var(--font-mono, monospace)"
                        >
                          {Math.round(requestMax * ratio)}
                        </text>
                      )}
                      {viewMode !== 'requests' && (
                        <text
                          x={width - padRight + 8}
                          y={y + 3.5}
                          textAnchor="start"
                          fontSize="10"
                          fill="#818cf8"
                          fontFamily="var(--font-mono, monospace)"
                        >
                          {formatEnCompact(tokenMax * ratio)}
                        </text>
                      )}
                    </>
                  )}
                </g>
              );
            })}

            {/* 底线轴 */}
            <line
              x1={padLeft}
              y1={height - padBottom}
              x2={width - padRight}
              y2={height - padBottom}
              stroke="var(--border)"
              strokeWidth="1"
            />

            {/* 业务走势：柱状图（请求数） */}
            {viewMode !== 'tokens' &&
              viewMode !== 'latency' &&
              points.map((p, i) => {
                const req = requestValues[i];
                const barH = req > 0 ? Math.max(3, (req / requestMax) * plotHeight) : 2;
                const barX = xAt(i) - barWidth / 2;
                const barY = height - padBottom - barH;
                const isSelected = selectedDay === p.date;
                const isHovered = hoverIndex === i;

                return (
                  <rect
                    key={`bar-${p.date}`}
                    x={barX}
                    y={barY}
                    width={barWidth}
                    height={barH}
                    rx="3"
                    fill={isSelected ? '#f59e0b' : 'url(#request-bar-gradient)'}
                    opacity={isHovered ? 1 : 0.82}
                    filter={isHovered ? 'drop-shadow(0 0 6px rgba(56, 189, 248, 0.6))' : undefined}
                    style={{
                      cursor: onPickDay ? 'pointer' : 'default',
                      transition: 'all 0.15s ease',
                    }}
                    onClick={() => onPickDay?.(p.date)}
                  />
                );
              })}

            {/* 业务走势：Token 面积图与折线 */}
            {viewMode !== 'requests' && viewMode !== 'latency' && totalTokens > 0 && (
              <>
                <path d={tokenAreaPath} fill="url(#token-area-gradient)" pointerEvents="none" />
                <path
                  d={tokenLinePath}
                  fill="none"
                  stroke="#6366f1"
                  strokeWidth="2.4"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  pointerEvents="none"
                />
                {points.map((p, i) => {
                  const tv = tokenValues[i];
                  if (tv <= 0) return null;
                  const isHovered = hoverIndex === i;
                  return (
                    <circle
                      key={`pt-${p.date}`}
                      cx={xAt(i)}
                      cy={yToken(tv)}
                      r={isHovered ? 5.5 : 3}
                      fill="#6366f1"
                      stroke="var(--surface)"
                      strokeWidth="2"
                      pointerEvents="none"
                    />
                  );
                })}
              </>
            )}

            {/* APM 性能视图：延迟与 TTFT */}
            {viewMode === 'latency' && (
              <>
                <path d={latencyAreaPath} fill="url(#latency-area-gradient)" pointerEvents="none" />
                <path
                  d={latencyLinePath}
                  fill="none"
                  stroke="#a855f7"
                  strokeWidth="2.4"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  pointerEvents="none"
                />
                <path
                  d={ttftLinePath}
                  fill="none"
                  stroke="#f59e0b"
                  strokeWidth="2"
                  strokeDasharray="4 3"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  pointerEvents="none"
                />
                {points.map((p, i) => {
                  const lat = latencyValues[i];
                  const ttft = ttftValues[i];
                  const isHovered = hoverIndex === i;
                  return (
                    <g key={`latpt-${p.date}`}>
                      {lat > 0 && (
                        <circle
                          cx={xAt(i)}
                          cy={yLatency(lat)}
                          r={isHovered ? 5.5 : 3}
                          fill="#a855f7"
                          stroke="var(--surface)"
                          strokeWidth="2"
                          pointerEvents="none"
                        />
                      )}
                      {ttft > 0 && (
                        <circle
                          cx={xAt(i)}
                          cy={yTtft(ttft)}
                          r={isHovered ? 5 : 2.6}
                          fill="#f59e0b"
                          stroke="var(--surface)"
                          strokeWidth="1.8"
                          pointerEvents="none"
                        />
                      )}
                    </g>
                  );
                })}
              </>
            )}

            {/* X 轴日期刻度 */}
            {points.map((p, i) => {
              if (!xTickIndexes.includes(i)) return null;
              return (
                <text
                  key={`xtick-${p.date}`}
                  x={xAt(i)}
                  y={height - padBottom + 16}
                  textAnchor="middle"
                  fontSize="10"
                  fill="var(--subtle)"
                >
                  {p.date.slice(5)}
                </text>
              );
            })}

            {/* 鼠标悬停 Crosshair 辅助参考线 */}
            {hoverIndex != null && (
              <g pointerEvents="none">
                <line
                  x1={xAt(hoverIndex)}
                  y1={padTop}
                  x2={xAt(hoverIndex)}
                  y2={height - padBottom}
                  stroke="var(--accent)"
                  strokeDasharray="3 3"
                  strokeWidth="1.2"
                />
              </g>
            )}
          </svg>

          {/* 交互 Tooltip 浮窗 */}
          {activePoint && hoverIndex != null && (
            <div
              className="combo-tooltip"
              style={{
                left: `${(xAt(hoverIndex) / width) * 100}%`,
              }}
            >
              <div className="combo-tooltip-head">
                <b>{activePoint.date}</b>
                {onPickDay ? <small>点击下钻此单日</small> : null}
              </div>
              <div className="combo-tooltip-body">
                <div className="tooltip-stat-row">
                  <span className="tooltip-label">请求次数：</span>
                  <span className="tooltip-val font-mono">{activePoint.requestCount} 次</span>
                </div>
                <div className="tooltip-stat-row">
                  <span className="tooltip-label">总消耗 Token：</span>
                  <span className="tooltip-val font-mono">
                    {formatTokenCount((activePoint.inputTokens || 0) + (activePoint.outputTokens || 0))}
                  </span>
                </div>
                {(activePoint.avgLatencyMs || activePoint.avgTtftMs) ? (
                  <div className="tooltip-stat-row" style={{ color: '#a855f7' }}>
                    <span className="tooltip-label" style={{ color: '#a855f7' }}>往返延迟 / TTFT：</span>
                    <span className="tooltip-val font-mono">
                      {Math.round(activePoint.avgLatencyMs || 0)}ms / {Math.round(activePoint.avgTtftMs || 0)}ms
                    </span>
                  </div>
                ) : null}
                <div className="tooltip-sub-row">
                  <span>入: {formatTokenCount(activePoint.inputTokens || 0)}</span>
                  <span>出: {formatTokenCount(activePoint.outputTokens || 0)}</span>
                  {(activePoint.cacheTokens || 0) > 0 ? <span>缓: {formatTokenCount(activePoint.cacheTokens)}</span> : null}
                  {activePoint.costMicros
                    ? <span>成本: {formatCostMicros(activePoint.costMicros)}{activePoint.pricedCount && activePoint.pricedCount < activePoint.requestCount ? `（${activePoint.pricedCount}/${activePoint.requestCount} 次已计价）` : ''}</span>
                    : activePoint.requestCount > 0 ? <span title="这一天的请求都没有价格行，所以没有成本，不是免费">成本: 未计价</span> : null}
                </div>
                {(activePoint.rxBytes || activePoint.txBytes) ? (
                  <div className="tooltip-traffic-row">
                    <span>入流: {formatBytes(activePoint.rxBytes)}</span>
                    <span>出流: {formatBytes(activePoint.txBytes)}</span>
                  </div>
                ) : null}
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

/** =========================================================================
 * 4. 新增：24 小时全天候调用分布图 (UsageHourlyDistribution)
 * 统计 00:00 ~ 23:00 峰值活跃时段，帮助掌握潮汐流量特征
 * ========================================================================= */
export function UsageHourlyDistribution({
  title = '24 小时全天候调用活跃度分布',
  logs = [],
  items,
  rangeRequests,
}: {
  title?: string;
  logs?: LogEntry[];
  items?: HourlyBucketStats[];
  rangeRequests?: number;
}) {
  const [hoveredHour, setHoveredHour] = React.useState<number | null>(null);

  // 聚合 24 个时段：优先从服务端按小时预聚合的 items 取，降级兼容 logs
  const hourlyCounts = React.useMemo(() => {
    const counts = Array.from({ length: 24 }, () => ({ requests: 0, tokens: 0 }));
    if (items && items.length > 0) {
      for (const item of items) {
        const h = item.hour;
        if (h >= 0 && h < 24) {
          counts[h].requests += item.requestCount || 0;
          counts[h].tokens += (item.inputTokens || 0) + (item.outputTokens || 0);
        }
      }
      return counts;
    }
    for (const log of logs) {
      if (!log.time) continue;
      const date = new Date(log.time);
      const h = date.getHours();
      if (h >= 0 && h < 24) {
        counts[h].requests += 1;
        counts[h].tokens += (log.inputTokens || 0) + (log.outputTokens || 0);
      }
    }
    return counts;
  }, [items, logs]);

  const maxRequests = Math.max(1, ...hourlyCounts.map((h) => h.requests));
  const peakHourIndex = hourlyCounts.reduce((maxI, h, i, arr) => (h.requests > arr[maxI].requests ? i : maxI), 0);
  const totalInLogs = hourlyCounts.reduce((s, h) => s + h.requests, 0);
  const totalTokens = hourlyCounts.reduce((s, h) => s + h.tokens, 0);
  const partialDetail = rangeRequests != null && rangeRequests > 0 && rangeRequests !== totalInLogs;

  return (
    <div className="usage-chart-card usage-hourly-card">
      <div className="usage-section-title-row">
        <div>
          <div className="usage-section-title">{title}</div>
          <div className="usage-section-subtitle">
            {totalInLogs > 0
              ? `调用高峰时段：${String(peakHourIndex).padStart(2, '0')}:00 ~ ${String(peakHourIndex + 1).padStart(2, '0')}:00（${hourlyCounts[peakHourIndex].requests} 次，占明细 ${((hourlyCounts[peakHourIndex].requests / totalInLogs) * 100).toFixed(0)}%）`
              : '各时段请求密度分布'}
          </div>
        </div>
      </div>

      <div className="hourly-bars-track">
        {hourlyCounts.map((h, i) => {
          const heightPct = (h.requests / maxRequests) * 100;
          const isPeak = i === peakHourIndex && h.requests > 0;
          const isHovered = hoveredHour === i;
          const isZero = h.requests === 0;
          return (
            <div
              key={i}
              className={`hourly-bar-col${isPeak ? ' peak' : ''}${isHovered ? ' hovered' : ''}`}
              onMouseEnter={() => setHoveredHour(i)}
              onMouseLeave={() => setHoveredHour(null)}
              title={`${String(i).padStart(2, '0')}:00 ~ ${String(i + 1).padStart(2, '0')}:00: ${h.requests} 次请求 · ${formatTokenCount(h.tokens)} tokens`}
            >
              <div className="hourly-bar-fill-wrap">
                <div
                  className="hourly-bar-fill"
                  style={{
                    height: isZero ? '3px' : `${Math.max(8, heightPct)}%`,
                    background: isPeak ? 'var(--amber)' : isZero ? 'var(--bg-active)' : isHovered ? 'var(--accent)' : 'var(--accent-soft)',
                    borderColor: isPeak ? 'var(--amber)' : isZero ? 'transparent' : 'var(--accent)',
                    opacity: isZero && !isHovered ? 0.45 : 1,
                  }}
                />
              </div>
              <span className="hourly-bar-tick">{i % 3 === 0 ? `${i}h` : ''}</span>
            </div>
          );
        })}
      </div>

      <div className="hourly-hover-info">
        {hoveredHour != null ? (
          <>
            <b>{String(hoveredHour).padStart(2, '0')}:00 ~ ${String(hoveredHour + 1).padStart(2, '0')}:00</b>
            <span>{hourlyCounts[hoveredHour].requests} 次请求</span>
            <span>{formatTokenCount(hourlyCounts[hoveredHour].tokens)} tokens</span>
          </>
        ) : (
          <>
            <span>明细累计：<b>{totalInLogs.toLocaleString()}</b> 次请求</span>
            <span><b>{formatTokenCount(totalTokens)}</b> tokens</span>
            <span style={{ marginLeft: 'auto', color: 'var(--subtle)', fontSize: '11px' }}>
              {partialDetail ? `区间总量 ${rangeRequests!.toLocaleString()} 次，小时分布取自保留期内明细` : '悬停查看各时段峰值'}
            </span>
          </>
        )}
      </div>
    </div>
  );
}

/** =========================================================================
 * 5. 按日流量平滑双色面积走势图 (UsageDailyTrafficLines)
 * ========================================================================= */
export function UsageDailyTrafficLines({
  title = '按日业务流量趋势（上行接收 vs 下行发送）',
  points,
  onPickDay,
  selectedDay,
}: {
  title?: string;
  points: DailyRequestPoint[];
  onPickDay?: (date: string) => void;
  selectedDay?: string;
}) {
  const [hoverIndex, setHoverIndex] = React.useState<number | null>(null);

  const width = 960;
  const height = 260;
  const padLeft = 72;
  const padRight = 72;
  const padTop = 30;
  const padBottom = 42;
  const plotWidth = width - padLeft - padRight;
  const plotHeight = height - padTop - padBottom;

  const rxValues = points.map((p) => p.rxBytes || 0);
  const txValues = points.map((p) => p.txBytes || 0);
  const inputValues = points.map((p) => p.inputTokens || 0);
  const outputValues = points.map((p) => p.outputTokens || 0);
  const hasTraffic = rxValues.some((v) => v > 0) || txValues.some((v) => v > 0);
  // Byte counters only exist for requests recorded after the traffic metric
  // shipped. Fall back to token flow so historical ranges stay meaningful.
  const tokenMode = !hasTraffic && (inputValues.some((v) => v > 0) || outputValues.some((v) => v > 0));
  const primaryValues = tokenMode ? inputValues : rxValues;
  const secondaryValues = tokenMode ? outputValues : txValues;
  const primaryMax = Math.max(1, ...primaryValues);
  const secondaryMax = Math.max(1, ...secondaryValues);

  const totalPrimary = primaryValues.reduce((s, v) => s + v, 0);
  const totalSecondary = secondaryValues.reduce((s, v) => s + v, 0);

  const formatSeries = tokenMode ? formatTokenCount : formatBytes;
  const seriesLabel = tokenMode ? '输入 Token' : '接收流量 Rx';
  const secondaryLabel = tokenMode ? '输出 Token' : '发送流量 Tx';

  const xTickIndexes = pickUsageXTickIndexes(points.length);

  const xAt = (i: number) => {
    if (points.length <= 1) return padLeft + plotWidth / 2;
    return padLeft + (i / (points.length - 1)) * plotWidth;
  };

  const yPrimary = (v: number) => height - padBottom - (v / primaryMax) * plotHeight;
  const ySecondary = (v: number) => height - padBottom - (v / secondaryMax) * plotHeight;

  const primaryPoints = points.map((_, i) => ({ x: xAt(i), y: yPrimary(primaryValues[i]) }));
  const primaryLine = createSmoothPath(primaryPoints);
  const primaryArea = createAreaPath(primaryPoints, height - padBottom);

  const secondaryPoints = points.map((_, i) => ({ x: xAt(i), y: ySecondary(secondaryValues[i]) }));
  const secondaryLine = createSmoothPath(secondaryPoints);
  const secondaryArea = createAreaPath(secondaryPoints, height - padBottom);

  function handleMouseMove(e: React.MouseEvent<SVGSVGElement>) {
    const rect = e.currentTarget.getBoundingClientRect();
    const mouseX = ((e.clientX - rect.left) / rect.width) * width;
    if (points.length === 0) return;
    const clampedX = Math.max(padLeft, Math.min(width - padRight, mouseX));
    const ratio = (clampedX - padLeft) / Math.max(1, plotWidth);
    const idx = Math.min(points.length - 1, Math.max(0, Math.round(ratio * (points.length - 1))));
    setHoverIndex(idx);
  }

  const activePoint = hoverIndex != null && points[hoverIndex] ? points[hoverIndex] : null;

  return (
    <div className="usage-chart-card usage-traffic-card">
      <div className="usage-combo-header">
        <div>
          <div className="usage-combo-title">{tokenMode ? '按日 Token 流向趋势（输入 vs 输出）' : title}</div>
          <div className="usage-combo-subtitle">
            {tokenMode
              ? `区间 Token：输入 ${formatTokenCount(totalPrimary)} · 输出 ${formatTokenCount(totalSecondary)} · 合计 ${formatTokenCount(totalPrimary + totalSecondary)}（历史区间未记录字节吞吐）`
              : `区间网络吞吐：接收 ${formatBytes(totalPrimary)} · 发送 ${formatBytes(totalSecondary)} · 合计 ${formatBytes(totalPrimary + totalSecondary)}`}
          </div>
        </div>

        <div className="usage-month-bars-legend">
          <span className="usage-month-legend-item">
            <i className="legend-line traffic-rx" /> {seriesLabel}（左轴）
          </span>
          <span className="usage-month-legend-item">
            <i className="legend-line traffic-tx" /> {secondaryLabel}（右轴）
          </span>
        </div>
      </div>

      {points.length === 0 ? (
        <div className="empty-state compact">暂无流量数据</div>
      ) : !hasTraffic && !tokenMode ? (
        <div className="empty-state compact">暂无业务数据（网关转发请求后将自动记录 Token 与字节吞吐）</div>
      ) : (
        <div className="usage-combo-plot-wrap">
          <svg
            viewBox={`0 0 ${width} ${height}`}
            className="usage-combo-svg"
            role="img"
            aria-label={title}
            onMouseMove={handleMouseMove}
            onMouseLeave={() => setHoverIndex(null)}
          >
            <defs>
              <linearGradient id="rx-area-gradient" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stopColor="#0ea5e9" stopOpacity="0.28" />
                <stop offset="90%" stopColor="#0ea5e9" stopOpacity="0.02" />
              </linearGradient>
              <linearGradient id="tx-area-gradient" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stopColor="#10b981" stopOpacity="0.28" />
                <stop offset="90%" stopColor="#10b981" stopOpacity="0.02" />
              </linearGradient>
            </defs>

            {[0, 0.33, 0.66, 1].map((ratio) => {
              const y = height - padBottom - ratio * plotHeight;
              return (
                <g key={`grid-${ratio}`}>
                  <line
                    x1={padLeft}
                    y1={y}
                    x2={width - padRight}
                    y2={y}
                    stroke="var(--border)"
                    strokeDasharray="2 3"
                    strokeWidth="1"
                    opacity="0.6"
                  />
                  <text
                    x={padLeft - 8}
                    y={y + 3.5}
                    textAnchor="end"
                    fontSize="10"
                    fill="#0ea5e9"
                    fontWeight="600"
                    fontFamily="var(--font-mono, monospace)"
                  >
                    {formatSeries(primaryMax * ratio)}
                  </text>
                  <text
                    x={width - padRight + 8}
                    y={y + 3.5}
                    textAnchor="start"
                    fontSize="10"
                    fill="#10b981"
                    fontWeight="600"
                    fontFamily="var(--font-mono, monospace)"
                  >
                    {formatSeries(secondaryMax * ratio)}
                  </text>
                </g>
              );
            })}

            <line
              x1={padLeft}
              y1={height - padBottom}
              x2={width - padRight}
              y2={height - padBottom}
              stroke="var(--border)"
              strokeWidth="1"
            />

            {totalPrimary > 0 && (
              <>
                <path d={primaryArea} fill="url(#rx-area-gradient)" pointerEvents="none" />
                <path
                  d={primaryLine}
                  fill="none"
                  stroke="#0ea5e9"
                  strokeWidth="2.2"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  pointerEvents="none"
                />
              </>
            )}

            {totalSecondary > 0 && (
              <>
                <path d={secondaryArea} fill="url(#tx-area-gradient)" pointerEvents="none" />
                <path
                  d={secondaryLine}
                  fill="none"
                  stroke="#10b981"
                  strokeWidth="2.2"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  pointerEvents="none"
                />
              </>
            )}

            {points.map((p, i) => {
              const primary = primaryValues[i];
              const secondary = secondaryValues[i];
              const isHovered = hoverIndex === i;
              const isSelected = selectedDay === p.date;
              return (
                <g key={`dots-${p.date}`}>
                  {primary > 0 && (
                    <circle
                      cx={xAt(i)}
                      cy={yPrimary(primary)}
                      r={isHovered || isSelected ? 5 : 2.8}
                      fill="#0ea5e9"
                      stroke="var(--surface)"
                      strokeWidth="1.8"
                    />
                  )}
                  {secondary > 0 && (
                    <circle
                      cx={xAt(i)}
                      cy={ySecondary(secondary)}
                      r={isHovered || isSelected ? 5 : 2.8}
                      fill="#10b981"
                      stroke="var(--surface)"
                      strokeWidth="1.8"
                    />
                  )}
                </g>
              );
            })}

            {points.map((p, i) => {
              if (!xTickIndexes.includes(i)) return null;
              return (
                <text
                  key={`xtick-${p.date}`}
                  x={xAt(i)}
                  y={height - padBottom + 16}
                  textAnchor="middle"
                  fontSize="10"
                  fill="var(--subtle)"
                >
                  {p.date.slice(5)}
                </text>
              );
            })}

            {hoverIndex != null && (
              <g pointerEvents="none">
                <line
                  x1={xAt(hoverIndex)}
                  y1={padTop}
                  x2={xAt(hoverIndex)}
                  y2={height - padBottom}
                  stroke="var(--accent)"
                  strokeDasharray="3 3"
                  strokeWidth="1.2"
                />
              </g>
            )}
          </svg>

          {activePoint && hoverIndex != null && (
            <div
              className="combo-tooltip"
              style={{
                left: `${(xAt(hoverIndex) / width) * 100}%`,
              }}
            >
              <div className="combo-tooltip-head">
                <b>{activePoint.date}</b>
                {onPickDay ? <small>点击下钻此单日</small> : null}
              </div>
              <div className="combo-tooltip-body">
                <div className="tooltip-stat-row">
                  <span className="tooltip-label text-rx">{tokenMode ? '输入 Token：' : '接收 (Rx)：'}</span>
                  <span className="tooltip-val font-mono">{tokenMode ? formatTokenCount(activePoint.inputTokens || 0) : formatBytes(activePoint.rxBytes)}</span>
                </div>
                <div className="tooltip-stat-row">
                  <span className="tooltip-label text-tx">{tokenMode ? '输出 Token：' : '发送 (Tx)：'}</span>
                  <span className="tooltip-val font-mono">{tokenMode ? formatTokenCount(activePoint.outputTokens || 0) : formatBytes(activePoint.txBytes)}</span>
                </div>
                <div className="tooltip-stat-row">
                  <span className="tooltip-label">{tokenMode ? '合计 Token：' : '合计吞吐：'}</span>
                  <span className="tooltip-val font-mono font-bold">
                    {tokenMode
                      ? formatTokenCount((activePoint.inputTokens || 0) + (activePoint.outputTokens || 0))
                      : formatBytes((activePoint.rxBytes || 0) + (activePoint.txBytes || 0))}
                  </span>
                </div>
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

/** =========================================================================
 * 6. 深度模型排行榜组件 (UsageModelLeaderboard)
 * ========================================================================= */
export function UsageModelLeaderboard({
  title = '模型深度用量排行',
  items,
  metric = 'tokens',
  models = [],
  providers = [],
  onSelectModel,
  selectedModel,
}: {
  title?: string;
  items: Array<{
    model: string;
    requestCount: number;
    inputTokens: number;
    outputTokens: number;
    cacheTokens: number;
    /** 已计价成本（微美元）。旧数据与未计价缺省，成本口径下按 0 参与排序。 */
    costMicros?: number;
    /** 有价格行的请求数，用来说明这一行为什么只有部分成本。 */
    pricedCount?: number;
  }>;
  metric?: 'tokens' | 'requests' | 'cost';
  models?: Model[];
  providers?: Provider[];
  onSelectModel?: (model: string) => void;
  selectedModel?: string;
}) {
  const providerByID = new Map(providers.map((provider) => [provider.id, provider]));
  const modelByID = new Map(models.map((model) => [model.id, model]));
  const visible = items.filter((m) => m.model && m.model !== '_unknown');
  const valueOf = (item: { requestCount: number; inputTokens: number; outputTokens: number; costMicros?: number }) =>
    metric === 'tokens' ? item.inputTokens + item.outputTokens : metric === 'cost' ? item.costMicros || 0 : item.requestCount;
  const metricName = metric === 'tokens' ? 'Token' : metric === 'cost' ? '已计价成本' : '请求次数';
  const sorted = [...visible].sort((a, b) => valueOf(b) - valueOf(a)).slice(0, 10);
  const maxValue = Math.max(1, ...sorted.map(valueOf));
  const totalValue = visible.reduce((sum, m) => sum + valueOf(m), 0);
  const covered = totalValue ? sorted.reduce((sum, m) => sum + valueOf(m), 0) / totalValue * 100 : 0;

  return (
    <div className="usage-chart-card usage-leaderboard-card">
      <div className="usage-section-title-row">
        <div>
          <div className="usage-section-title">{title}</div>
          <div className="usage-section-subtitle">
            Top 10 · 占全部 {metricName} 的 {covered.toFixed(1)}%
          </div>
        </div>
        <div className="usage-leaderboard-legend">
          {metric === 'tokens' ? (
            <>
              <span className="prompt-legend"><i />输入 Prompt</span>
              <span className="completion-legend"><i />输出 Completion</span>
            </>
          ) : metric === 'cost' ? (
            <span className="requests-legend"><i />已计价成本 · 未计价记 0</span>
          ) : (
            <span className="requests-legend"><i />请求次数</span>
          )}
        </div>
      </div>

      {sorted.length === 0 ? (
        <div className="empty-state compact">暂无模型使用记录</div>
      ) : (
        <div className="usage-leaderboard-list">
          {sorted.map((item, index) => {
            const value = valueOf(item);
            const pctOfMax = (value / maxValue) * 100;
            const pctOfTotal = totalValue > 0 ? (value / totalValue) * 100 : 0;
            const sum = item.inputTokens + item.outputTokens;
            const inputPct = sum > 0 ? (item.inputTokens / sum) * 100 : 50;
            const outputPct = sum > 0 ? (item.outputTokens / sum) * 100 : 50;
            const isSelected = selectedModel === item.model;
            const model = modelByID.get(item.model);
            const provider = model ? providerByID.get(model.providerId) : undefined;
            const alias = model?.name && model.name !== model.id ? model.name : '';

            return (
              <div
                key={item.model}
                className={`usage-leaderboard-row${isSelected ? ' selected' : ''}`}
                style={{ cursor: onSelectModel ? 'pointer' : 'default' }}
                onClick={() => onSelectModel?.(item.model)}
                title={onSelectModel ? `点击下钻分析模型: ${item.model}` : item.model}
              >
                <div className={`leaderboard-rank-badge rank-${index + 1}`}>
                  #{index + 1}
                </div>
                <div className="leaderboard-model-info">
                  <div className="leaderboard-model-name-row">
                    <span className="leaderboard-model-name-block">
                      {provider ? (
                        <span className="leaderboard-provider-icon" title={provider.name}>
                          <BrandIcon icon={resolveProviderIcon(provider)} name={provider.name} size={15} />
                        </span>
                      ) : null}
                      <span className="leaderboard-model-name">{item.model}</span>
                      {alias ? <span className="leaderboard-model-alias" title={alias}>{alias}</span> : null}
                      {isSelected ? <span className="drill-active-tag">已下钻</span> : null}
                    </span>
                    <div className="leaderboard-row-actions">
                      <button
                        type="button"
                        className="mini-btn leaderboard-copy-btn"
                        title={`复制模型 ID：${item.model}`}
                        onClick={(e) => {
                          e.stopPropagation();
                          void copyToClipboard(item.model).then((ok) => {
                            if (ok) {
                              const toast = document.getElementById('toast');
                              if (toast) {
                                toast.textContent = `已复制模型 ID：${item.model}`;
                                toast.classList.add('show');
                                window.setTimeout(() => toast.classList.remove('show'), 2000);
                              }
                            }
                          });
                        }}
                      >
                        复制
                      </button>
                      <span className="leaderboard-share-badge font-mono" title={`占区间${metricName}的 ${pctOfTotal.toFixed(1)}%`}>
                        {pctOfTotal.toFixed(1)}%
                      </span>
                    </div>
                  </div>
                  <div className={`leaderboard-split-track${metric !== 'tokens' ? ' is-requests' : ''}`} style={{ width: `${Math.max(12, pctOfMax)}%` }}>
                    {metric === 'tokens' ? (
                      <>
                        <div
                          className="split-fill input"
                          style={{ width: `${inputPct}%` }}
                          title={`输入: ${formatTokenCount(item.inputTokens)}`}
                        />
                        <div
                          className="split-fill output"
                          style={{ width: `${outputPct}%` }}
                          title={`输出: ${formatTokenCount(item.outputTokens)}`}
                        />
                      </>
                    ) : (
                      <div
                        className="split-fill requests"
                        style={{ width: '100%' }}
                        title={metric === 'cost' ? `${formatCostMicros(item.costMicros)} · ${item.requestCount} 次请求` : `${item.requestCount} 次请求`}
                      />
                    )}
                  </div>
                </div>
                <div className="leaderboard-stats-col">
                  {metric === 'tokens' ? (
                    <>
                      <span className="leaderboard-token-total font-mono">{formatTokenCount(sum)} tokens</span>
                      <span className="leaderboard-token-meta font-mono">
                        入 {formatTokenCount(item.inputTokens)} · 出 {formatTokenCount(item.outputTokens)}
                        {item.cacheTokens > 0 ? ` · 缓 ${formatTokenCount(item.cacheTokens)}` : ''} · {item.requestCount} 次
                      </span>
                    </>
                  ) : metric === 'cost' ? (
                    <>
                      <span className="leaderboard-token-total font-mono">{formatCostMicros(item.costMicros)}</span>
                      <span className="leaderboard-token-meta font-mono">
                        {item.requestCount.toLocaleString()} 次 · {item.pricedCount
                          ? `${item.pricedCount.toLocaleString()} 次已计价`
                          : '全部未计价'}
                      </span>
                    </>
                  ) : (
                    <>
                      <span className="leaderboard-token-total font-mono">{item.requestCount.toLocaleString()} 次</span>
                      <span className="leaderboard-token-meta font-mono">
                        {formatTokenCount(sum)} Token · 均 {formatTokenCount(Math.round(sum / Math.max(1, item.requestCount)))} / 次
                      </span>
                    </>
                  )}
                </div>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}

/** =========================================================================
 * 7. 增强版排名柱状图组件 (UsageBarChart)
 * ========================================================================= */
export function UsageBarChart({
  title,
  items,
  formatValue,
  onSelect,
  selectedKey,
}: {
  title: string;
  items: Array<{ label: string; value: number }>;
  formatValue?: (value: number) => string;
  onSelect?: (label: string) => void;
  selectedKey?: string;
}) {
  const max = Math.max(1, ...items.map((item) => item.value));
  return (
    <div className="usage-chart-card">
      <div className="usage-section-title-row">
        <div className="usage-section-title">{title}</div>
      </div>
      {items.length === 0 ? (
        <div className="empty-state compact">暂无数据</div>
      ) : (
        <div className="usage-bar-list">
          {items.map((item, index) => {
            const isSelected = selectedKey === item.label;
            return (
              <div
                className={`usage-bar-row${isSelected ? ' selected' : ''}`}
                key={item.label}
                style={{ cursor: onSelect ? 'pointer' : 'default' }}
                onClick={() => onSelect?.(item.label)}
                title={onSelect ? `点击下钻过滤: ${item.label}` : item.label}
              >
                <span className="usage-bar-rank">#{index + 1}</span>
                <span className="usage-bar-label" title={item.label}>
                  {item.label}
                  {isSelected ? <span className="drill-active-tag">已选</span> : null}
                </span>
                <div className="usage-bar-track">
                  <div
                    className="usage-bar-fill"
                    style={{ width: `${(item.value / max) * 100}%` }}
                  />
                </div>
                <span className="usage-bar-value font-mono">
                  {formatValue ? formatValue(item.value) : item.value}
                </span>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}

/** =========================================================================
 * 8. 状态码与成功率组件 (UsageStatusChart)
 * ========================================================================= */
export function UsageStatusChart({
  title = '状态码与健康度分布',
  items,
}: {
  title?: string;
  items: StatusBucketStats[];
}) {
  const total = Math.max(0, items.reduce((sum, item) => sum + item.requestCount, 0));
  const success2xx = items.find((i) => i.class === '2xx')?.requestCount || 0;
  const successRate = total > 0 ? `${((success2xx / total) * 100).toFixed(1)}%` : '—';

  const chartItems = items.map((it) => ({
    label: it.class,
    value: it.requestCount,
    color: STATUS_COLORS[it.class] || '#64748b',
  }));

  return (
    <UsageDonutChart
      title={title}
      subtitle={`整体请求成功率：${successRate}%`}
      items={chartItems}
      totalLabel="总请求"
      totalOverride={total}
      formatValue={(v) => `${v} 次`}
    />
  );
}

/** =========================================================================
 * 9. Prompt 缓存省流与加速深度分析卡片 (UsageCacheHitRate)
 * ========================================================================= */
export function UsageCacheHitRate({
  title = 'Prompt 缓存加速与省流分析',
  input,
  cache,
}: {
  title?: string;
  input: number;
  cache: number;
}) {
  const { totalInput, cacheHits, nonCachedInput, hitRatePct } = normalizePromptTokenStats(input, cache);
  const hasCache = cacheHits > 0;
  const hitRateLabel = totalInput > 0 ? `${hitRatePct.toFixed(1)}%` : '0.0%';

  return (
    <div className="usage-chart-card usage-cache-analytics-card">
      <div className="usage-section-title-row">
        <div>
          <div className="usage-section-title">{title}</div>
          <div className="usage-section-subtitle">
            {hasCache
              ? `Prompt 缓存节省 ${formatTokenCount(cacheHits)}（命中率 ${hitRateLabel}）`
              : totalInput > 0
                ? '当前区间上游请求未产生 Prompt Cache 命中'
                : '暂无 Prompt 缓存数据'}
          </div>
        </div>
      </div>

      <div className="cache-analytics-grid">
        <div className="cache-stat-tile">
          <span className="cache-tile-label">Prompt 总量</span>
          <span className="cache-tile-val font-mono">{formatTokenCount(totalInput)}</span>
          <span className="cache-tile-hint">基准提示词总规模</span>
        </div>
        <div className="cache-stat-tile hit">
          <span className="cache-tile-label">缓存命中省流</span>
          <span className="cache-tile-val font-mono" style={{ color: hasCache ? 'var(--emerald, #10b981)' : 'var(--muted)' }}>
            {hasCache ? formatTokenCount(cacheHits) : '0'}
          </span>
          <span className="cache-tile-hint">{hasCache ? `省流占比 ${hitRateLabel}` : '未触发缓存加速'}</span>
        </div>
        <div className="cache-stat-tile">
          <span className="cache-tile-label">实时推理输入</span>
          <span className="cache-tile-val font-mono">{formatTokenCount(nonCachedInput)}</span>
          <span className="cache-tile-hint">实付 Prompt 算力</span>
        </div>
      </div>

      <div className="cache-bar-wrapper">
        <div className="cache-split-bar-track">
          <div
            className="cache-split-hit"
            style={{ width: `${hasCache ? Math.max(3, Math.min(100, hitRatePct)) : 0}%` }}
            title={`缓存命中节省: ${formatTokenCount(cacheHits)} (${hitRateLabel})`}
          />
          <div
            className="cache-split-miss"
            style={{ width: `${hasCache ? Math.min(100, Math.max(0, 100 - hitRatePct)) : 100}%` }}
            title={`实时推理输入: ${formatTokenCount(nonCachedInput)}`}
          />
        </div>
        <div className="cache-bar-legend">
          <span className="legend-chip">
            <span className="cache-dot hit" />
            缓存命中省流 ({hitRateLabel})
          </span>
          <span className="legend-chip">
            <span className="cache-dot miss" />
            常规推理输入 ({totalInput > 0 ? (100 - hitRatePct).toFixed(1) : '100'}%)
          </span>
        </div>
      </div>
    </div>
  );
}

/** 兼容旧代码的别名组件 */
export function UsageLineChart({ title, points }: { title: string; points: DailyRequestPoint[] }) {
  return <UsageComboTrendChart title={title} points={points} />;
}

export function UsageMonthlyTokenBars({
  title = '每日用量走势',
  points,
  onPickDay,
}: {
  title?: string;
  points: DailyRequestPoint[];
  onPickDay?: (date: string) => void;
}) {
  return <UsageComboTrendChart title={title} points={points} onPickDay={onPickDay} />;
}

export function UsageSegmentedShare({
  title,
  items,
  formatValue,
  onSelect,
}: {
  title: string;
  items: Array<{ label: string; value: number }>;
  formatValue?: (val: number) => string;
  onSelect?: (label: string) => void;
}) {
  return (
    <UsageDonutChart
      title={title}
      items={items}
      formatValue={formatValue}
      onSelect={onSelect}
    />
  );
}

export function UsageRangeCalendar({
  from,
  to,
  onSelect,
  onClear,
}: {
  from: string;
  to: string;
  onSelect: (from: string, to: string) => void;
  onClear?: () => void;
}) {
  const todayStr = formatLocalISODate(new Date());
  const [viewYM, setViewYM] = React.useState(() => (to || from || todayStr).slice(0, 7));
  const [year, month] = viewYM.split('-').map(Number);
  const startWeekday = (new Date(year, month - 1, 1).getDay() + 6) % 7;
  const daysInMonth = new Date(year, month, 0).getDate();

  const cells: (string | null)[] = [];
  for (let i = 0; i < startWeekday; i += 1) cells.push(null);
  for (let d = 1; d <= daysInMonth; d += 1) {
    cells.push(`${year}-${String(month).padStart(2, '0')}-${String(d).padStart(2, '0')}`);
  }

  function shiftMonth(delta: number) {
    const next = new Date(year, month - 1 + delta, 1);
    setViewYM(`${next.getFullYear()}-${String(next.getMonth() + 1).padStart(2, '0')}`);
  }

  function pickPreset(days: number) {
    const end = new Date();
    const start = new Date();
    start.setDate(end.getDate() - (days - 1));
    onSelect(formatLocalISODate(start), formatLocalISODate(end));
    setViewYM(formatLocalISODate(end).slice(0, 7));
  }

  function handleDayClick(day: string, event: React.MouseEvent) {
    if (event.shiftKey && from) {
      const anchor = from;
      const [a, b] = day < anchor ? [day, anchor] : [anchor, day];
      onSelect(a, b);
      return;
    }
    onSelect(day, day);
  }

  return (
    <div className="usage-calendar" role="group" aria-label="选择统计日期区间">
      <div className="usage-calendar-head">
        <button className="mini-btn" type="button" onClick={() => shiftMonth(-1)} aria-label="上个月">‹</button>
        <span className="usage-calendar-title">{year} 年 {month} 月</span>
        <button className="mini-btn" type="button" onClick={() => shiftMonth(1)} aria-label="下个月">›</button>
      </div>
      <div className="usage-calendar-grid">
        {['一', '二', '三', '四', '五', '六', '日'].map((w) => (
          <span key={w} className="usage-calendar-weekday">{w}</span>
        ))}
        {cells.map((day, index) => {
          if (!day) return <span key={`empty-${index}`} />;
          const inRange = from && to && day >= from && day <= to;
          const isEdge = day === from || day === to;
          const isFuture = day > todayStr;
          return (
            <button
              key={day}
              type="button"
              className={`usage-calendar-day${inRange ? ' in-range' : ''}${isEdge ? ' edge' : ''}${day === todayStr ? ' today' : ''}`}
              disabled={isFuture}
              onClick={(event) => handleDayClick(day, event)}
              title={day}
            >
              {Number(day.slice(8))}
            </button>
          );
        })}
      </div>
      <div className="usage-calendar-foot">
        <button className="mini-btn" type="button" onClick={() => pickPreset(1)}>今天</button>
        <button className="mini-btn" type="button" onClick={() => pickPreset(7)}>近 7 天</button>
        <button className="mini-btn" type="button" onClick={() => pickPreset(30)}>近 30 天</button>
        {onClear ? <button className="mini-btn" type="button" onClick={onClear} disabled={!from && !to}>清除</button> : null}
        <span className="usage-calendar-hint">单击选单日 · Shift+单击选区间</span>
      </div>
    </div>
  );
}

export function LogTimingWaterfall({ log }: { log: LogEntry }) {
  const total = Math.max(1, log.latencyMs);
  const stages = [
    { label: '网关准备', ms: log.prepMs ?? 0, color: '#38bdf8' },
    { label: '上游等待 TTFB', ms: log.upstreamTtfbMs ?? 0, color: '#f59e0b' },
    { label: '首字响应 TTFT', ms: log.ttftMs != null ? Math.max(0, log.ttftMs - (log.upstreamTtfbMs ?? 0)) : 0, color: '#8b5cf6' },
    { label: '网关格式转换', ms: log.convertOutMs ?? 0, color: '#06b6d4' },
    { label: '网关处理开销', ms: log.gatewayOverheadMs ?? 0, color: '#10b981' },
    { label: '后处理耗时', ms: log.postMs ?? 0, color: '#64748b' },
  ].filter((s) => s.ms > 0);

  return (
    <div className="log-waterfall-card">
      <div className="log-waterfall-title-row">
        <span>全链路 APM 耗时分解</span>
        <b>总耗时: {log.latencyMs} ms</b>
      </div>
      <div className="log-waterfall-stack-bar">
        {stages.map((stage) => {
          const pct = Math.min(100, Math.max(1, (stage.ms / total) * 100));
          return (
            <div
              key={stage.label}
              className="log-waterfall-slice"
              style={{ width: `${pct}%`, backgroundColor: stage.color }}
              title={`${stage.label}: ${stage.ms}ms (${((stage.ms / total) * 100).toFixed(1)}%)`}
            />
          );
        })}
      </div>
      <div className="log-waterfall-legend">
        {stages.map((stage) => (
          <div key={stage.label} className="log-waterfall-item">
            <span className="log-waterfall-dot" style={{ backgroundColor: stage.color }} />
            <span className="log-waterfall-name">{stage.label}</span>
            <b className="log-waterfall-ms">{stage.ms} ms</b>
            <small>({((stage.ms / total) * 100).toFixed(0)}%)</small>
          </div>
        ))}
      </div>
      {/* 父子链路上这两段的含义会变，但网关没有单独测「跨网关那一段」的字段，
          所以这里不画一个假的分段，只把口径说清楚——否则「上游等待 TTFB」
          在下游看起来是个巨大的数字，会被误判成上游账号本身慢。 */}
      {log.egress ? (
        <p className="log-waterfall-note">
          这条请求经上游网关 {shortGatewayId(log.egress)} 转发：「上游等待 TTFB」是
          <b>整段父网关的耗时</b>（父网关自己的准备 + 它自己等上游 + 跨机网络），
          不是某台上游账号单独的耗时。要拆开，请到那台网关的 API 日志里按同一时刻找这条请求。
        </p>
      ) : null}
      {log.ingress ? (
        <p className="log-waterfall-note">
          这条请求由下游网关 {shortGatewayId(log.ingress)} 送进来：这里的总耗时正是
          <b>它等本机的全部时间</b>，也就是它那条日志里的「上游等待 TTFB」。
        </p>
      ) : null}
    </div>
  );
}

/** =========================================================================
 * 10. 全新多维实体下钻透视中心 (UsagePivotExplorer)
 * 彻底颠覆死板长表格，采用分面切片器 + 实体透视卡片栅格 + 紧凑数据表格
 * ========================================================================= */
export function UsagePivotExplorer({
  stats,
  monthStats,
  providers,
  isNormalUser,
  initialDim = 'models',
  onDrill,
  onNavigateLogs,
}: {
  stats: RequestStatsSnapshot | null;
  monthStats?: RequestStatsSnapshot['month'] | null;
  providers: Provider[];
  isNormalUser: boolean;
  initialDim?: 'models' | 'keys' | 'providers' | 'protocols' | 'users';
  onDrill: (type: 'model' | 'provider' | 'apiKey', value: string, label: string) => void;
  onNavigateLogs: (type: 'model' | 'provider' | 'apiKey', value: string, label: string) => void;
}) {
  const [activeDim, setActiveDim] = React.useState<'models' | 'keys' | 'providers' | 'protocols' | 'users'>(initialDim);
  React.useEffect(() => {
    if (initialDim) setActiveDim(initialDim);
  }, [initialDim]);
  const [viewMode, setViewMode] = React.useState<'cards' | 'table'>('cards');

  // 模型维度数据
  const modelItems = React.useMemo(() => {
    const raw = (stats?.range?.byModel || stats?.today?.byModel || []).filter(
      (m) => m.model && m.model !== '_unknown',
    );
    const sorted = [...raw].sort(
      (a, b) => b.inputTokens + b.outputTokens - (a.inputTokens + a.outputTokens),
    );
    const totalTokens = sorted.reduce((s, m) => s + m.inputTokens + m.outputTokens, 0);
    return sorted.map((m, idx) => {
      const sum = m.inputTokens + m.outputTokens;
      const pct = totalTokens > 0 ? (sum / totalTokens) * 100 : 0;
      return {
        id: m.model,
        name: m.model,
        rank: idx + 1,
        tokens: sum,
        inputTokens: m.inputTokens,
        outputTokens: m.outputTokens,
        requests: m.requestCount,
        pct,
        type: 'model' as const,
      };
    });
  }, [stats]);

  // Key 维度数据
  const keyItems = React.useMemo(() => {
    const raw = stats?.range?.byApiKey || stats?.today?.byApiKey || [];
    const sorted = [...raw].sort(
      (a, b) => b.inputTokens + b.outputTokens - (a.inputTokens + a.outputTokens),
    );
    const totalTokens = sorted.reduce((s, k) => s + k.inputTokens + k.outputTokens, 0);
    return sorted.map((k, idx) => {
      const sum = (k.inputTokens || 0) + (k.outputTokens || 0);
      const pct = totalTokens > 0 ? (sum / totalTokens) * 100 : 0;
      return {
        id: k.apiKeyId || k.apiKeyName || `key-${idx}`,
        name: k.apiKeyName || k.apiKeyId || '未命名密钥',
        rank: idx + 1,
        tokens: sum,
        inputTokens: k.inputTokens || 0,
        outputTokens: k.outputTokens || 0,
        requests: k.requestCount,
        pct,
        type: 'apiKey' as const,
      };
    });
  }, [stats]);

  // Provider 维度数据
  const providerItems = React.useMemo(() => {
    const raw = stats?.range?.byProvider || stats?.today?.byProvider || [];
    const sorted = [...raw].sort(
      (a, b) => b.inputTokens + b.outputTokens - (a.inputTokens + a.outputTokens),
    );
    const totalTokens = sorted.reduce((s, p) => s + p.inputTokens + p.outputTokens, 0);
    return sorted.map((p, idx) => {
      const sum = (p.inputTokens || 0) + (p.outputTokens || 0);
      const pct = totalTokens > 0 ? (sum / totalTokens) * 100 : 0;
      const friendlyName = providerUsageLabel(p.providerId, providers);
      return {
        id: p.providerId,
        name: friendlyName,
        rank: idx + 1,
        tokens: sum,
        inputTokens: p.inputTokens || 0,
        outputTokens: p.outputTokens || 0,
        requests: p.requestCount,
        pct,
        type: 'provider' as const,
      };
    });
  }, [stats, providers]);

  // 协议维度数据
  const protocolItems = React.useMemo(() => {
    const raw = stats?.range?.byProtocol || stats?.today?.byProtocol || [];
    const sorted = [...raw].sort(
      (a, b) => b.inputTokens + b.outputTokens - (a.inputTokens + a.outputTokens),
    );
    const totalTokens = sorted.reduce((s, p) => s + p.inputTokens + p.outputTokens, 0);
    return sorted.map((p, idx) => {
      const sum = (p.inputTokens || 0) + (p.outputTokens || 0);
      const pct = totalTokens > 0 ? (sum / totalTokens) * 100 : 0;
      return {
        id: p.protocol,
        name: p.protocol,
        rank: idx + 1,
        tokens: sum,
        inputTokens: p.inputTokens || 0,
        outputTokens: p.outputTokens || 0,
        requests: p.requestCount,
        pct,
        type: null,
      };
    });
  }, [stats]);

  // 用户维度数据
  const userItems = React.useMemo(() => {
    const raw = stats?.range?.byUser || stats?.today?.byUser || [];
    const sorted = [...raw].sort(
      (a, b) => b.inputTokens + b.outputTokens - (a.inputTokens + a.outputTokens),
    );
    const totalTokens = sorted.reduce((s, u) => s + u.inputTokens + u.outputTokens, 0);
    return sorted.map((u, idx) => {
      const sum = (u.inputTokens || 0) + (u.outputTokens || 0);
      const pct = totalTokens > 0 ? (sum / totalTokens) * 100 : 0;
      return {
        id: u.userId || u.userName || `user-${idx}`,
        name: u.userName || u.userId || '未绑定用户',
        rank: idx + 1,
        tokens: sum,
        inputTokens: u.inputTokens || 0,
        outputTokens: u.outputTokens || 0,
        requests: u.requestCount,
        pct,
        type: null,
      };
    });
  }, [stats]);

  const currentList =
    activeDim === 'models'
      ? modelItems
      : activeDim === 'keys'
      ? keyItems
      : activeDim === 'providers'
      ? providerItems
      : activeDim === 'protocols'
      ? protocolItems
      : userItems;

  const dimTabs = [
    { id: 'models', label: '🧠 模型排行透视', count: modelItems.length },
    { id: 'keys', label: '🔑 API 密钥透视', count: keyItems.length },
    { id: 'providers', label: '⚡ Provider 路由透视', count: providerItems.length },
    { id: 'protocols', label: '🌐 输出协议透视', count: protocolItems.length },
    ...(!isNormalUser ? [{ id: 'users', label: '👤 用户透视', count: userItems.length }] : []),
  ];

  // 跨期对比：获取当前实体在本月的调用量
  const getMonthData = React.useCallback(
    (item: { id: string; name: string }) => {
      if (!monthStats) return null;
      if (activeDim === 'models') {
        const found = monthStats.byModel?.find((m) => m.model === item.id);
        if (!found) return null;
        return {
          requests: found.requestCount,
          tokens: (found.inputTokens || 0) + (found.outputTokens || 0),
        };
      }
      if (activeDim === 'keys') {
        const found = monthStats.byApiKey?.find((k) => k.apiKeyId === item.id || k.apiKeyName === item.name);
        if (!found) return null;
        return {
          requests: found.requestCount,
          tokens: (found.inputTokens || 0) + (found.outputTokens || 0),
        };
      }
      if (activeDim === 'providers') {
        const found = monthStats.byProvider?.find((p) => p.providerId === item.id);
        if (!found) return null;
        return {
          requests: found.requestCount,
          tokens: (found.inputTokens || 0) + (found.outputTokens || 0),
        };
      }
      if (activeDim === 'protocols') {
        const found = monthStats.byProtocol?.find((p) => p.protocol === item.id);
        if (!found) return null;
        return {
          requests: found.requestCount,
          tokens: (found.inputTokens || 0) + (found.outputTokens || 0),
        };
      }
      if (activeDim === 'users') {
        const found = monthStats.byUser?.find((u) => u.userId === item.id || u.userName === item.name);
        if (!found) return null;
        return {
          requests: found.requestCount,
          tokens: (found.inputTokens || 0) + (found.outputTokens || 0),
        };
      }
      return null;
    },
    [monthStats, activeDim],
  );

  return (
    <div className="usage-chart-card pivot-explorer-card">
      <div className="pivot-explorer-header">
        <div>
          <div className="usage-section-title">多维实体下钻透视中心</div>
          <div className="usage-section-subtitle">
            打破死板长列表：以分面卡片矩阵深入剖析核心实体，支持一键锁定下钻与穿透日志
          </div>
        </div>

        {/* 视图模式切换 */}
        <div className="pivot-view-controls">
          <div className="usage-segmented-toggle">
            <button
              type="button"
              className={viewMode === 'cards' ? 'active' : ''}
              onClick={() => setViewMode('cards')}
            >
              ⊞ 实体卡片矩阵
            </button>
            <button
              type="button"
              className={viewMode === 'table' ? 'active' : ''}
              onClick={() => setViewMode('table')}
            >
              ☰ 紧凑数据表
            </button>
          </div>
        </div>
      </div>

      {/* 维度切片导航条 */}
      <div className="pivot-dimension-nav">
        {dimTabs.map((t) => (
          <button
            key={t.id}
            type="button"
            className={`pivot-dim-btn${activeDim === t.id ? ' active' : ''}`}
            onClick={() => setActiveDim(t.id as typeof activeDim)}
          >
            {t.label}
            <span className="pivot-dim-count font-mono">{t.count}</span>
          </button>
        ))}
      </div>

      {/* 内容区域：实体卡片矩阵 vs 紧凑表格 */}
      {currentList.length === 0 ? (
        <div className="empty-state compact">当前维度在选定区间内暂无调用数据</div>
      ) : viewMode === 'cards' ? (
        <div className="pivot-grid">
          {currentList.map((item) => {
            const inputPct = item.tokens > 0 ? (item.inputTokens / item.tokens) * 100 : 50;
            const outputPct = item.tokens > 0 ? (item.outputTokens / item.tokens) * 100 : 50;
            const rankClass = item.rank <= 3 ? `top-rank-${item.rank}` : '';
            const monthInfo = getMonthData(item);

            return (
              <div key={item.id} className={`pivot-entity-card ${rankClass}`}>
                <div className="pivot-entity-header">
                  <div className="pivot-entity-title-row">
                    <span className={`pivot-rank-tag rank-${item.rank}`}>#{item.rank}</span>
                    <span className="pivot-entity-title" title={item.name}>
                      {item.name}
                    </span>
                  </div>
                  <span className="pivot-share-badge font-mono">{item.pct.toFixed(1)}%</span>
                </div>

                <div className="pivot-entity-stats-grid">
                  <div className="pivot-stat-cell">
                    <span className="pivot-stat-lbl">区间 Token</span>
                    <span className="pivot-stat-num font-mono">{formatTokenCount(item.tokens)}</span>
                  </div>
                  <div className="pivot-stat-cell">
                    <span className="pivot-stat-lbl">区间请求</span>
                    <span className="pivot-stat-num font-mono">{formatEnCompact(item.requests)} 次</span>
                  </div>
                </div>

                {/* Prompt vs Completion 双色微进度条 */}
                <div className="pivot-split-bar-wrap">
                  <div className="pivot-split-bar">
                    <div
                      className="split-in"
                      style={{ width: `${inputPct}%` }}
                      title={`输入 Prompt: ${formatTokenCount(item.inputTokens)}`}
                    />
                    <div
                      className="split-out"
                      style={{ width: `${outputPct}%` }}
                      title={`输出 Completion: ${formatTokenCount(item.outputTokens)}`}
                    />
                  </div>
                  <div className="pivot-split-labels">
                    <small>入: {formatTokenCount(item.inputTokens)}</small>
                    <small>出: {formatTokenCount(item.outputTokens)}</small>
                  </div>
                </div>

                {/* 本月对比 pill */}
                {monthInfo ? (
                  <div className="pivot-month-pill font-mono">
                    <span>本月: <b>{formatTokenCount(monthInfo.tokens)}</b> tokens · {formatEnCompact(monthInfo.requests)} 次</span>
                  </div>
                ) : null}

                {/* 下钻操作动作栏 */}
                <div className="pivot-entity-actions">
                  {item.type ? (
                    <button
                      type="button"
                      className="pivot-action-btn drill-btn"
                      onClick={() => onDrill(item.type!, item.id, item.name)}
                      title={`锁定此实体，下钻过滤全站用量看板`}
                    >
                      🔍 锁定下钻
                    </button>
                  ) : null}
                  {item.type ? (
                    <button
                      type="button"
                      className="pivot-action-btn log-btn"
                      onClick={() => onNavigateLogs(item.type!, item.id, item.name)}
                      title={`前往 API 日志穿透追踪该实体的调用明细`}
                    >
                      📋 穿透日志
                    </button>
                  ) : null}
                </div>
              </div>
            );
          })}
        </div>
      ) : (
        /* 紧凑数据表模式 */
        <div className="pivot-table-wrap">
          <table className="pivot-compact-table">
            <thead>
              <tr>
                <th style={{ width: 60 }}>排名</th>
                <th>实体名称</th>
                <th style={{ textAlign: 'right' }}>区间请求</th>
                <th style={{ textAlign: 'right' }}>区间 Token</th>
                <th style={{ textAlign: 'right' }}>输入 / 输出细分</th>
                <th style={{ textAlign: 'right' }}>本月请求</th>
                <th style={{ textAlign: 'right' }}>本月 Token</th>
                <th style={{ textAlign: 'right' }}>区间占比</th>
                <th style={{ textAlign: 'center', width: 130 }}>操作</th>
              </tr>
            </thead>
            <tbody>
              {currentList.map((item) => {
                const monthInfo = getMonthData(item);
                return (
                  <tr key={item.id}>
                    <td>
                      <span className={`pivot-rank-tag rank-${item.rank}`}>#{item.rank}</span>
                    </td>
                    <td>
                      <b className="pivot-table-name" title={item.name}>
                        {item.name}
                      </b>
                    </td>
                    <td style={{ textAlign: 'right' }} className="font-mono">
                      {formatEnCompact(item.requests)}
                    </td>
                    <td style={{ textAlign: 'right' }} className="font-mono font-bold">
                      {formatTokenCount(item.tokens)}
                    </td>
                    <td style={{ textAlign: 'right' }} className="font-mono text-muted">
                      {formatTokenCount(item.inputTokens)} / {formatTokenCount(item.outputTokens)}
                    </td>
                    <td style={{ textAlign: 'right' }} className="font-mono text-muted">
                      {monthInfo ? formatEnCompact(monthInfo.requests) : '—'}
                    </td>
                    <td style={{ textAlign: 'right' }} className="font-mono text-muted">
                      {monthInfo ? formatTokenCount(monthInfo.tokens) : '—'}
                    </td>
                    <td style={{ textAlign: 'right' }} className="font-mono text-accent">
                      {item.pct.toFixed(1)}%
                    </td>
                    <td style={{ textAlign: 'center' }}>
                      <div style={{ display: 'inline-flex', gap: 6 }}>
                        {item.type ? (
                          <button
                            type="button"
                            className="mini-action-pill"
                            onClick={() => onDrill(item.type!, item.id, item.name)}
                          >
                            下钻
                          </button>
                        ) : null}
                        {item.type ? (
                          <button
                            type="button"
                            className="mini-action-pill"
                            onClick={() => onNavigateLogs(item.type!, item.id, item.name)}
                          >
                            日志
                          </button>
                        ) : null}
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

/** =========================================================================
 * 12. RTK 每日压缩节省堆叠柱 (RTKSavingsChart)
 * ========================================================================= */
export function RTKSavingsChart({ points }: { points: RTKStatsDailyPoint[] }) {
  const maxTotal = Math.max(
    1,
    ...points.map((point) => Math.max(0, point.savedTokens) + Math.max(0, point.compressedTokens)),
  );
  const hasData = points.some((point) => point.originalTokens > 0);
  if (!hasData) {
    return (
      <div className="empty-state compact">
        暂无压缩记录：在 Provider 上开启 RTK 后，网关转发命令输出时会自动累计节省量。
      </div>
    );
  }
  const tickIndexes = pickUsageXTickIndexes(points.length);
  return (
    <div className="rtk-daily-chart">
      <div className="rtk-daily-bars">
        {points.map((point) => {
          const saved = Math.max(0, point.savedTokens);
          const forwarded = Math.max(0, point.compressedTokens);
          const total = saved + forwarded;
          const savedPercent = total > 0 ? Math.round((saved / total) * 100) : 0;
          return (
            <div
              className="rtk-daily-column"
              key={point.date}
              title={`${point.date}\n压缩 ${point.commands} 条命令\n节省 ${formatTokenCount(saved)} / 原始 ${formatTokenCount(total)} token（${savedPercent}%）\n仍发给模型 ${formatTokenCount(forwarded)}`}
            >
              <div className="rtk-daily-stack">
                <span className="rtk-daily-saved" style={{ height: `${(saved / maxTotal) * 100}%` }} />
                <span className="rtk-daily-forwarded" style={{ height: `${(forwarded / maxTotal) * 100}%` }} />
              </div>
            </div>
          );
        })}
      </div>
      <div className="rtk-daily-axis">
        {tickIndexes.map((index) => (
          <span
            className="rtk-daily-axis-label"
            key={points[index].date}
            style={{
              left: `${points.length <= 1 ? 50 : (index / (points.length - 1)) * 100}%`,
              // 首尾标签贴边对齐，避免 translateX(-50%) 被图表边缘裁掉一半。
              transform: points.length <= 1 ? 'translateX(-50%)' : index === 0 ? 'translateX(0)' : index === points.length - 1 ? 'translateX(-100%)' : undefined,
            }}
          >
            {points[index].date.slice(5)}
          </span>
        ))}
      </div>
    </div>
  );
}
