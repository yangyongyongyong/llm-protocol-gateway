// 机器状态页：网关所在主机的实时观测。
//
// 卡片上的数字与趋势图的最后一个点来自同一份快照（见 host_metrics.ts），所以
// 两处永远一致。数据走 SSE 推送：连上先补一整窗历史，之后每个采样推一帧。
import React, {useCallback, useEffect, useState} from 'react';
import {formatBytes, formatLoad, formatMiBps, formatRate, hostMetricsPollMs, thermalPressureLabel,} from '../lib';
import type {HostDisk, HostDiskIO, HostFanSpeed, HostMetrics} from '../types';
import {
    appendPoint,
    fetchHostSnapshot,
    HOST_WINDOW_MINUTES,
    type HostDiagnosis,
    hostDiagnosticsURL,
    type HostHistory,
    type HostPoint,
    type HostStreamStatus,
    isGap,
    loadHistory,
    openHostStream,
    saveHistory,
} from '../host_metrics';

const TREND_WINDOW_MS = HOST_WINDOW_MINUTES * 60_000;

// ---------------------------------------------------------------- 格式化

function percentText(value: number | undefined, digits = 0): string {
    if (value == null || !Number.isFinite(value)) return '—';
    return `${value.toFixed(digits)}%`;
}

function clockText(ms: number): string {
    if (!ms) return '—';
    return new Date(ms).toLocaleTimeString([], {hour12: false});
}

function formatDuration(seconds?: number): string {
    if (seconds == null || !Number.isFinite(seconds) || seconds <= 0) return '—';
    const total = Math.floor(seconds);
    const days = Math.floor(total / 86400);
    const hours = Math.floor((total % 86400) / 3600);
    const minutes = Math.floor((total % 3600) / 60);
    if (days > 0) return `${days} 天 ${hours} 小时`;
    if (hours > 0) return `${hours} 小时 ${minutes} 分`;
    if (minutes > 0) return `${minutes} 分 ${total % 60} 秒`;
    return `${total} 秒`;
}

function mibText(value: number | undefined, digits = 1): string {
    if (value == null || !Number.isFinite(value)) return '—';
    if (value >= 1024) return `${(value / 1024).toFixed(2)} GiB`;
    return `${value.toFixed(digits)} MiB`;
}

type Tone = 'green' | 'blue' | 'amber' | 'red' | 'slate';

function levelTone(percent: number | undefined, warn = 75, danger = 90): Tone {
    if (percent == null || !Number.isFinite(percent)) return 'slate';
    if (percent >= danger) return 'red';
    if (percent >= warn) return 'amber';
    return 'green';
}

function severityTone(severity: string | undefined): Tone {
    switch (severity) {
        case 'critical':
            return 'red';
        case 'warn':
            return 'amber';
        case 'info':
            return 'blue';
        case 'ok':
            return 'green';
        default:
            return 'slate';
    }
}

function severityLabel(severity: string | undefined): string {
    switch (severity) {
        case 'critical':
            return '严重';
        case 'warn':
            return '注意';
        case 'info':
            return '提示';
        case 'ok':
            return '正常';
        default:
            return '未知';
    }
}

// ---------------------------------------------------------------- 采样循环

/**
 * 订阅循环是模块级单例：开发模式下 StrictMode 会把 effect 跑两遍，写进组件里
 * 很容易叠出两条并行连接；这里第一个订阅者负责建流，最后一个退订才关掉。
 *
 * 数据来自 /__host-metrics/stream：连上先拿一整窗历史，之后每个采样推一帧。
 * 网关自己一直在采样（无人看时降频），所以曲线不依赖这个页面开没开过。
 */
const stream = {
    running: false,
    stop: null as (() => void) | null,
    history: null as HostHistory | null,
    // 徽标只反映「连接通道」，不能由数据帧反推：降级轮询时数据照样在来，
    // 但推送确实已经不可用了，说成「实时推送中」就是假话。
    status: 'connecting' as HostStreamStatus,
    listeners: new Set<(history: HostHistory, status: HostStreamStatus) => void>(),
};

function notify(history: HostHistory, status?: HostStreamStatus) {
    if (status) stream.status = status;
    stream.listeners.forEach((listener) => listener(history, stream.status));
}

function pushHistory(points: HostPoint[], latest: HostMetrics | null) {
    const now = Date.now();
    const previous = stream.history ?? {points: [], last: null, lastAt: 0};
    let merged = previous.points;
    for (const point of points) merged = appendPoint(merged, point, now);
    const lastAt = points.length > 0 ? points[points.length - 1].t : previous.lastAt;
    stream.history = {points: merged, last: latest ?? previous.last, lastAt};
    saveHistory(stream.history);
    return stream.history;
}

/**
 * 快照是服务端整窗历史的权威副本，直接替换本地点，不做合并。
 *
 * 合并会把本机缓存里更早那一版的采样拼进来——旧页面只在打开时轮询，
 * 缓存里正好是一簇簇孤点；换上新版本后这些点还挂在图上，看起来仍是断的。
 * 服务端重启丢掉的那一分钟宁可留断口，也不要拿本地残留去补。
 */
function replaceHistory(points: HostPoint[], latest: HostMetrics | null) {
    const lastAt = points.length > 0 ? points[points.length - 1].t : 0;
    stream.history = {points, last: latest, lastAt};
    saveHistory(stream.history);
    return stream.history;
}

function startStream() {
    if (stream.running) return;
    stream.running = true;
    stream.history = loadHistory();
    stream.stop = openHostStream({
        onSnapshot: ({points, latest}) =>
            notify(points.length > 0 ? replaceHistory(points, latest) : pushHistory([], latest)),
        onSample: (point, latest) => notify(pushHistory([point], latest)),
        onStatus: (status) => notify(stream.history ?? loadHistory(), status),
    });
}

function stopStream() {
    if (!stream.running) return;
    stream.running = false;
    stream.stop?.();
    stream.stop = null;
}

function useHostHistory(enabled: boolean): {history: HostHistory; status: HostStreamStatus} {
    const [history, setHistory] = useState<HostHistory>(() => loadHistory());
    const [status, setStatus] = useState<HostStreamStatus>('connecting');
    useEffect(() => {
        if (!enabled) {
            setHistory({points: [], last: null, lastAt: 0});
            setStatus('forbidden');
            return undefined;
        }
        const listener = (next: HostHistory, nextStatus: HostStreamStatus) => {
            setHistory(next);
            setStatus(nextStatus);
        };
        setStatus(stream.status);
        stream.listeners.add(listener);
        startStream();
        return () => {
            stream.listeners.delete(listener);
            if (stream.listeners.size === 0) stopStream();
        };
    }, [enabled]);
    return {history, status};
}

// ---------------------------------------------------------------- 趋势图

type Series = {key: 'cpu' | 'mem' | 'diskRead' | 'diskWrite' | 'netRx' | 'netTx' | 'threads' | 'fds'; label: string; color: string};

type ChartCardModel = {
    title: string;
    hint: string;
    series: Series[];
    kind: 'percent' | 'mib' | 'bytes' | 'count';
    axis: 'left' | 'right';
};

const TREND_CARDS: ChartCardModel[] = [
    {title: 'CPU 使用率', hint: '整机平均；断开处表示网关在该时段没有采样（重启或挂起）', series: [{key: 'cpu', label: 'CPU', color: 'var(--accent)'}], kind: 'percent', axis: 'left'},
    {title: '内存', hint: '已用内存（不含可回收页缓存）', series: [{key: 'mem', label: '已用', color: 'var(--purple)'}], kind: 'mib', axis: 'left'},
    {
        title: '磁盘吞吐',
        hint: '根分区所在物理盘，读写合计',
        series: [
            {key: 'diskRead', label: '读', color: 'var(--blue)'},
            {key: 'diskWrite', label: '写', color: 'var(--amber)'},
        ],
        kind: 'bytes',
        axis: 'left',
    },
    {
        title: '网络吞吐',
        hint: '物理网卡收发速率',
        series: [
            {key: 'netRx', label: '下行', color: 'var(--cyan)'},
            {key: 'netTx', label: '上行', color: 'var(--green)'},
        ],
        kind: 'bytes',
        axis: 'left',
    },
    {
        title: '网关线程与文件句柄',
        hint: 'goroutine 是 Go 调度单位，句柄超过上限会直接拒绝新连接',
        series: [
            {key: 'threads', label: 'goroutine', color: 'var(--accent)'},
            {key: 'fds', label: '句柄', color: 'var(--amber)'},
        ],
        kind: 'count',
        axis: 'left',
    },
];

function pointValue(point: HostPoint, key: Series['key']): number {
    switch (key) {
        case 'cpu':
            return point.cpu;
        case 'mem':
            return point.memUsed / (1024 * 1024);
        case 'diskRead':
            return point.diskRead;
        case 'diskWrite':
            return point.diskWrite;
        case 'netRx':
            return point.netRx;
        case 'netTx':
            return point.netTx;
        case 'threads':
            return point.goroutines;
        case 'fds':
            return point.fds;
        default:
            return 0;
    }
}

function formatAxisValue(value: number, kind: ChartCardModel['kind']): string {
    switch (kind) {
        case 'percent':
            return `${Math.round(value)}%`;
        case 'mib':
            return value >= 1024 ? `${(value / 1024).toFixed(1)}G` : `${Math.round(value)}M`;
        case 'bytes':
            return formatRate(value).replace('/s', '');
        default:
            return Math.round(value).toLocaleString();
    }
}

function formatTooltipValue(value: number, kind: ChartCardModel['kind']): string {
    switch (kind) {
        case 'percent':
            return `${value.toFixed(1)}%`;
        case 'mib':
            return mibText(value);
        case 'bytes':
            return formatRate(value);
        default:
            return Math.round(value).toLocaleString();
    }
}

// SVG 用固定设计高度 + 实测宽度，纵轴字体与网格线才不会被横向拉长。
const CHART_HEIGHT = 168;
const CHART_MIN_VIEW_WIDTH = 520;
const CHART_PAD = {top: 10, right: 12, bottom: 22, left: 44};

const EMPTY_POINT: HostPoint = {
    t: 0, cpu: 0, load: 0, memUsed: 0, memTotal: 0, memPercent: 0, processCpu: 0,
    diskRead: 0, diskWrite: 0, netRx: 0, netTx: 0, goroutines: 0, rss: 0, fds: 0,
};

function TrendChart({card, points, pollMs, now, windowMs}: {
    card: ChartCardModel;
    points: HostPoint[];
    pollMs: number;
    now: number;
    windowMs: number;
}) {
    // null = 没在图上；数字 = 指针位置换算成 viewBox 坐标。zustand 风格的「同一位置
    // 不重渲染」在这里不适用：光标不动时采样会继续推进，悬浮读数必须跟着刷新。
    const [hoverX, setHoverX] = useState<number | null>(null);
    const [box, setBox] = useState({width: 0, height: 0});
    const viewWidth = box.height > 0
        ? Math.max(CHART_MIN_VIEW_WIDTH, Math.round((CHART_HEIGHT * box.width) / box.height))
        : CHART_MIN_VIEW_WIDTH;
    // 时间窗以「最后一个采样点」为右边界：刚打开页面时曲线贴着右侧长出来，
    // 而不是先空着 60 分钟等数据；历史超过 60 分钟后左侧自然被裁掉。
    const lastT = points.length > 0 ? points[points.length - 1].t : 0;
    const start = lastT > 0 ? lastT - windowMs : now - windowMs;
    const end = start + windowMs;
    const visible = points.filter((point) => point.t >= start - pollMs && point.t <= end + pollMs);
    const innerW = viewWidth - CHART_PAD.left - CHART_PAD.right;
    const innerH = CHART_HEIGHT - CHART_PAD.top - CHART_PAD.bottom;

    let maxValue = 0;
    for (const point of visible) {
        for (const series of card.series) {
            maxValue = Math.max(maxValue, pointValue(point, series.key));
        }
    }
    if (card.kind === 'percent') {
        maxValue = Math.max(100, maxValue);
    } else if (maxValue <= 0) {
        maxValue = 1;
    } else {
        // 取一个好看的步长，避免刻度出现 37.3M 这类数字。
        const rough = maxValue / 4;
        const magnitude = Math.pow(10, Math.floor(Math.log10(rough)));
        const step = [1, 2, 2.5, 5, 10].find((candidate) => candidate * magnitude >= rough) ?? 10;
        maxValue = step * magnitude * 4;
    }

    const xOf = (t: number) => CHART_PAD.left + ((t - start) / windowMs) * innerW;
    const yOf = (value: number) => CHART_PAD.top + innerH - (Math.max(0, value) / maxValue) * innerH;

    // 每条线自己拆段：相邻两点间隔超过容差就是断档，段尾不连线。
    const lineSegments = card.series.map((series) => {
        const segments: Array<Array<{x: number; y: number}>> = [];
        let current: Array<{x: number; y: number}> = [];
        let prev: HostPoint | undefined;
        for (const point of visible) {
            if (isGap(prev, point, pollMs)) {
                if (current.length > 0) segments.push(current);
                current = [];
            }
            current.push({x: xOf(point.t), y: yOf(pointValue(point, series.key))});
            prev = point;
        }
        if (current.length > 0) segments.push(current);
        // 只有一个点时只画圆点，否则单次采样在图上完全看不见。
        const lastPoint = current.length === 1 ? current[0] : null;
        return {color: series.color, segments, lastPoint};
    });

    const tickCount = 4;
    const yTicks = Array.from({length: tickCount + 1}, (_, index) => (maxValue / tickCount) * index);
    const xTicks = Array.from({length: 5}, (_, index) => start + (windowMs / 4) * index);

    let hoverPoint: HostPoint | null = null;
    if (hoverX != null && visible.length > 0) {
        const t = start + ((hoverX - CHART_PAD.left) / innerW) * windowMs;
        let bestDistance = Infinity;
        for (const point of visible) {
            const distance = Math.abs(point.t - t);
            if (distance < bestDistance) {
                bestDistance = distance;
                hoverPoint = point;
            }
        }
        // 鼠标落在断档里时不硬凑最近的点，免得读到几分钟前的数据。
        if (bestDistance > pollMs * 3) hoverPoint = null;
    }

    const latest = visible.length > 0 ? visible[visible.length - 1] : null;

    return (
        <div className="machine-chart">
            <div className="machine-chart-head">
                <div>
                    <div className="machine-chart-title">{card.title}</div>
                    <div className="machine-chart-hint">{card.hint}</div>
                </div>
                <div className="machine-chart-legend">
                    {card.series.map((series) => (
                        <span key={series.key} className="machine-legend-item">
                            <i style={{background: series.color}} />
                            {series.label}
                            {/* 悬浮时读鼠标那一帧，否则读最新一帧：避免一边指着 10:31 的点、
                                图例却显示 10:35 的数。 */}
                            <b>{formatTooltipValue(pointValue(hoverPoint ?? latest ?? EMPTY_POINT, series.key), card.kind)}</b>
                        </span>
                    ))}
                </div>
            </div>
            <div className="machine-chart-body">
                <svg
                    ref={(node) => {
                        if (!node) return;
                        const rect = node.getBoundingClientRect();
                        if (Math.abs(rect.width - box.width) > 2 || Math.abs(rect.height - box.height) > 2) {
                            setBox({width: rect.width, height: rect.height});
                        }
                    }}
                    viewBox={`0 0 ${viewWidth} ${CHART_HEIGHT}`}
                    role="img"
                    aria-label={`${card.title}近 ${HOST_WINDOW_MINUTES} 分钟趋势`}
                    onMouseLeave={() => setHoverX(null)}
                    onMouseMove={(event) => {
                        const rect = event.currentTarget.getBoundingClientRect();
                        const ratio = (event.clientX - rect.left) / rect.width;
                        const x = ratio * viewWidth;
                        setHoverX(Math.min(CHART_PAD.left + innerW, Math.max(CHART_PAD.left, x)));
                    }}
                >
                    {yTicks.map((value) => (
                        <g key={`y-${value}`}>
                            <line
                                x1={CHART_PAD.left}
                                x2={CHART_PAD.left + innerW}
                                y1={yOf(value)}
                                y2={yOf(value)}
                                className="machine-grid-line"
                            />
                            <text x={CHART_PAD.left - 6} y={yOf(value) + 3} textAnchor="end" className="machine-axis-label">
                                {formatAxisValue(value, card.kind)}
                            </text>
                        </g>
                    ))}
                    {xTicks.map((t, index) => (
                        <text
                            key={`x-${t}`}
                            x={xOf(t)}
                            y={CHART_HEIGHT - 6}
                            textAnchor={index === 0 ? 'start' : index === xTicks.length - 1 ? 'end' : 'middle'}
                            className="machine-axis-label"
                        >
                            {clockText(t)}
                        </text>
                    ))}
                    {lineSegments.map((line) =>
                        line.segments.map((segment, index) =>
                            segment.length === 1 ? (
                                <circle key={`dot-${line.color}-${index}`} cx={segment[0].x} cy={segment[0].y} r={1.6} fill={line.color} />
                            ) : (
                                <polyline
                                    key={`seg-${line.color}-${index}`}
                                    points={segment.map((item) => `${item.x.toFixed(1)},${item.y.toFixed(1)}`).join(' ')}
                                    fill="none"
                                    stroke={line.color}
                                    strokeWidth={1.8}
                                    strokeLinejoin="round"
                                    strokeLinecap="round"
                                />
                            ),
                        ),
                    )}
                    {hoverPoint ? (
                        <g>
                            <line
                                x1={xOf(hoverPoint.t)}
                                x2={xOf(hoverPoint.t)}
                                y1={CHART_PAD.top}
                                y2={CHART_PAD.top + innerH}
                                className="machine-hover-line"
                            />
                            {card.series.map((series) => (
                                <circle
                                    key={`hover-${series.key}`}
                                    cx={xOf(hoverPoint!.t)}
                                    cy={yOf(pointValue(hoverPoint!, series.key))}
                                    r={3}
                                    fill={series.color}
                                />
                            ))}
                        </g>
                    ) : null}
                    {!latest ? (
                        <text x={CHART_PAD.left + innerW / 2} y={CHART_PAD.top + innerH / 2} textAnchor="middle" className="machine-empty-label">
                            还没有采样数据
                        </text>
                    ) : null}
                </svg>
                {hoverPoint ? (
                    <div
                        className="machine-tooltip"
                        style={{
                            left: `${(xOf(hoverPoint.t) / viewWidth) * 100}%`,
                            transform: (xOf(hoverPoint.t) / viewWidth) * 100 > 60 ? 'translateX(-100%)' : 'translateX(12px)',
                        }}
                    >
                        <div className="machine-tooltip-time">{clockText(hoverPoint.t)}</div>
                        {card.series.map((series) => (
                            <div key={series.key} className="machine-tooltip-row">
                                <i style={{background: series.color}} />
                                <span>{series.label}</span>
                                <b>{formatTooltipValue(pointValue(hoverPoint!, series.key), card.kind)}</b>
                            </div>
                        ))}
                    </div>
                ) : null}
            </div>
        </div>
    );
}
// ---------------------------------------------------------------- 页面部件

function KpiCard({label, value, unit, note, percent, tone}: {
    label: string;
    value: string;
    unit?: string;
    note?: React.ReactNode;
    percent?: number;
    tone?: Tone;
}) {
    // 100% = 全部逻辑核都跑满；超过 100% 的值（如进程占满多核）夹到满格。
    const barPercent = percent == null ? null : Math.max(0, Math.min(100, percent));
    return (
        <div className="machine-kpi">
            <div className="machine-kpi-label">{label}</div>
            <div className="machine-kpi-value">
                {value}
                {unit ? <span>{unit}</span> : null}
            </div>
            {barPercent != null ? (
                <div className={`machine-kpi-bar ${tone ?? levelTone(barPercent)}`}>
                    <span style={{width: `${barPercent}%`}} />
                </div>
            ) : null}
            {note ? <div className="machine-kpi-note">{note}</div> : null}
        </div>
    );
}

function CoreStrip({metrics}: {metrics: HostMetrics | null}) {
    const cores = metrics?.cpuCores ?? [];
    if (!cores.length) return null;
    const physical = metrics?.cpuPhysicalCores ?? 0;
    const layout = physical > 0 && physical * 2 === cores.length
        ? `${physical} 物理核 / ${cores.length} 线程`
        : `${cores.length} 个逻辑核`;
    return (
        <div className="machine-cores">
            <div className="machine-cores-head">
                <span className="machine-cores-title">逻辑核占用</span>
                <span className="machine-cores-detail">
                    {metrics?.cpuModel || '型号未知'}
                    {metrics?.cpuMhz ? ` · ${Math.round(metrics.cpuMhz)} MHz` : ''}
                    {` · ${layout}`}
                </span>
                {metrics?.processCpuPercent ? (
                    <span className="machine-cores-gateway">网关占 {percentText(metrics.processCpuPercent, 1)}</span>
                ) : null}
            </div>
            <div className="machine-core-list">
                {cores.map((core) => (
                    <div key={core.id} className="machine-core" title={`CPU ${core.id} · ${percentText(core.percent, 0)}`}>
                        <span className="machine-core-label">#{core.id}</span>
                        <div className="machine-core-track">
                            <span className={levelTone(core.percent)} style={{width: `${Math.max(0, Math.min(100, core.percent))}%`}} />
                        </div>
                        <span className="machine-core-value">{core.percent.toFixed(0)}%</span>
                    </div>
                ))}
            </div>
        </div>
    );
}

function UsageRow({name, detail, percent, note}: {name: string; detail?: string; percent: number; note?: string}) {
    return (
        <div className="machine-usage-row">
            <div className="machine-usage-head">
                <span className="machine-usage-name">{name}</span>
                {detail ? <span className="machine-usage-detail">{detail}</span> : null}
                <span className="machine-usage-value">{percentText(percent, 1)}</span>
            </div>
            <div className={`machine-kpi-bar ${levelTone(percent, 80, 92)}`}>
                <span style={{width: `${Math.max(0, Math.min(100, percent))}%`}} />
            </div>
            {note ? <div className="machine-usage-note">{note}</div> : null}
        </div>
    );
}

function DiskIOLine({io}: {io: HostDiskIO}) {
    return (
        <div className="machine-io-line">
            <span>读 <b>{formatMiBps(io.readRate)}</b></span>
            <span>写 <b>{formatMiBps(io.writeRate)}</b></span>
            <span>忙碌 <b>{io.util.toFixed(0)}%</b></span>
            <span>await <b>{io.awaitMs.toFixed(2)} ms</b></span>
        </div>
    );
}

function VolumesCard({metrics}: {metrics: HostMetrics | null}) {
    const volumes: HostDisk[] = metrics?.volumes ?? [];
    return (
        <div className="machine-side-card">
            <div className="machine-side-head">
                <span className="machine-side-title">磁盘与挂载卷</span>
                {metrics?.diskIO ? <span className="machine-side-badge">{metrics.diskIO.device}</span> : null}
            </div>
            {volumes.length === 0 ? (
                <div className="machine-side-detail">本机未列出可用挂载卷</div>
            ) : (
                volumes.slice(0, 6).map((volume) => (
                    <UsageRow
                        key={volume.mount}
                        name={volume.mount}
                        detail={`${formatBytes(volume.used)} / ${formatBytes(volume.total)}`}
                        percent={volume.percent}
                    />
                ))
            )}
            {metrics?.diskIO ? <DiskIOLine io={metrics.diskIO} /> : null}
        </div>
    );
}

function EnvCard({metrics, activity}: {metrics: HostMetrics | null; activity: HostDiagnosis['gateway'] | null}) {
    const tempText = metrics?.tempAvailable && metrics?.tempC != null
        ? `${metrics.tempC.toFixed(1)}°C`
        : metrics?.thermalPressureAvailable
            ? thermalPressureLabel(metrics.thermalPressure)
            : '—';
    const fans: HostFanSpeed[] = metrics?.fans ?? [];
    const facts: Array<[string, React.ReactNode]> = [
        ['操作系统', metrics?.osName || metrics?.platform || '—'],
        ['内核', metrics?.kernel || '—'],
        ['主机名', metrics?.hostname || '—'],
        ['运行架构', metrics?.platform || '—'],
        ['开机时长', formatDuration(metrics?.uptimeSeconds)],
        ['CPU 温度', tempText],
        ['热力等级', metrics?.thermalPressureAvailable ? thermalPressureLabel(metrics.thermalPressure) : '—'],
        ['单次采集', metrics?.sampleCostMs != null ? `${metrics.sampleCostMs.toFixed(0)} ms` : '—'],
    ];
    return (
        <div className="machine-side-card">
            <div className="machine-side-title">主机环境</div>
            <div className="machine-env-grid">
                {facts.map(([label, value]) => (
                    <div key={label} className="machine-env-item">
                        <span>{label}</span>
                        <b>{value}</b>
                    </div>
                ))}
            </div>
            {fans.length > 0 ? (
                <div className="machine-io-line">
                    {fans.map((fan, index) => (
                        <span key={`${fan.id}-${index}`}>
                            {fan.name || `风扇 ${index + 1}`} <b>{Math.round(fan.rpm).toLocaleString()} RPM</b>
                        </span>
                    ))}
                </div>
            ) : null}
            {activity ? (
                <div className="machine-io-line">
                    <span>在途请求 <b>{activity.inFlight}</b></span>
                    <span>请求速率 <b>{activity.requestRate.toFixed(2)}/s</b></span>
                </div>
            ) : null}
        </div>
    );
}

function ProcessCard({metrics}: {metrics: HostMetrics | null}) {
    const started = metrics?.processStartedAt ? Date.parse(metrics.processStartedAt) : 0;
    const rows: Array<[string, React.ReactNode]> = [
        ['进程启动', started
            ? `${new Date(started).toLocaleString()} · 已运行 ${formatDuration(metrics?.processUptimeSeconds)}`
            : formatDuration(metrics?.processUptimeSeconds)],
        ['常驻内存', `${mibText(metrics?.processRssMiB)}${metrics?.processRssFromRuntime ? '（Go runtime 统计）' : ''}`],
        ['Go 堆', `${mibText(metrics?.processHeapMiB)}${metrics?.processPeakMiB ? ` · 峰值 ${mibText(metrics.processPeakMiB)}` : ''}`],
        ['线程', metrics?.processThreads ? `${metrics.processThreads} 线程 · ${metrics.osThreads ?? metrics.processThreads} 个 OS 线程` : '—'],
        ['文件句柄', metrics?.processFds != null ? `${metrics.processFds.toLocaleString()} 个` : '—'],
        ['goroutine', metrics?.goroutines != null ? metrics.goroutines.toLocaleString() : '—'],
        ['CPU 占用', percentText(metrics?.processCpuPercent, 1)],
        ['数据库文件', metrics?.dbFileBytes
            ? `${formatBytes(metrics.dbFileBytes)}${metrics.dbFileUpdated ? ` · 更新于 ${clockText(Date.parse(metrics.dbFileUpdated))}` : ''}`
            : '—'],
    ];
    if (metrics?.cgroupMemLimit) {
        rows.push(['容器内存', `${formatBytes(metrics.cgroupMemUsed)} / ${formatBytes(metrics.cgroupMemLimit)}`]);
    }
    if (metrics?.processRestarts) {
        rows.push(['服务重启', `${metrics.processRestarts} 次`]);
    }
    return (
        <div className="machine-side-card">
            <div className="machine-side-title">网关进程</div>
            <div className="machine-env-grid machine-env-grid-wide">
                {rows.map(([label, value]) => (
                    <div key={label} className="machine-env-item">
                        <span>{label}</span>
                        <b>{value}</b>
                    </div>
                ))}
            </div>
        </div>
    );
}

function DiagnosisCard({diagnosis, loading, onRefresh}: {diagnosis: HostDiagnosis | null; loading: boolean; onRefresh: () => void}) {
    if (!diagnosis) {
        return (
            <div className="machine-diagnosis empty">
                <div className="machine-diagnosis-head">
                    <div>
                        <div className="machine-diagnosis-title">诊断</div>
                        <div className="machine-diagnosis-verdict">
                            {loading ? '正在做两帧对照采样…' : '点「诊断」对当前状态做一次体检，给出结论与处置建议'}
                        </div>
                    </div>
                </div>
            </div>
        );
    }
    return (
        <div className={`machine-diagnosis ${diagnosis.severity}`}>
            <div className="machine-diagnosis-head">
                <div>
                    <div className="machine-diagnosis-title">
                        诊断
                        <span className={`badge ${severityTone(diagnosis.severity)}`}>{severityLabel(diagnosis.severity)}</span>
                    </div>
                    <div className="machine-diagnosis-verdict">{diagnosis.verdict}</div>
                </div>
                <div className="panel-header-actions">
                    <button className="mini-btn" type="button" onClick={onRefresh} disabled={loading}>
                        {loading ? '重新采样中…' : '重新诊断'}
                    </button>
                </div>
            </div>
            <div className="machine-diagnosis-meta">
                两帧间隔 {diagnosis.windowMs} ms · {clockText(Date.parse(diagnosis.evaluatedAt))} · 窗口内 {diagnosis.gateway.requestsDelta} 个请求
                （{diagnosis.gateway.requestRate.toFixed(2)}/s） · 当前在途 {diagnosis.gateway.inFlight}
            </div>
            <ul className="machine-findings">
                {diagnosis.findings.map((finding) => (
                    <li key={finding.id} className={finding.severity}>
                        <span className={`badge ${severityTone(finding.severity)}`}>{severityLabel(finding.severity)}</span>
                        <div>
                            <b>{finding.title}</b>
                            <p>{finding.detail}</p>
                            {finding.action ? <p className="machine-finding-action">建议：{finding.action}</p> : null}
                        </div>
                    </li>
                ))}
            </ul>
        </div>
    );
}

// ---------------------------------------------------------------- 页面

export function MachineStatusPage({onToast, enabled = true, onOpenSettings}: {onToast: (message: string) => void; enabled?: boolean; onOpenSettings?: () => void}) {
    const {history, status} = useHostHistory(enabled);
    // 满速采样间隔由服务端下发；降级轮询时也按同一个节奏画断档判定。
    const pollMs = hostMetricsPollMs();
    const metrics = history.last;
    const [diagnosis, setDiagnosis] = useState<HostDiagnosis | null>(null);
    const [diagnosing, setDiagnosing] = useState(false);
    const [downloading, setDownloading] = useState(false);
    // 图表的时间轴要动起来：没有新采样时横轴也得往前走，断档才看得出来。
    const [now, setNow] = useState(() => Date.now());
    useEffect(() => {
        const timer = window.setInterval(() => setNow(Date.now()), 10_000);
        return () => window.clearInterval(timer);
    }, []);
    useEffect(() => {
        if (history.lastAt) setNow(history.lastAt);
    }, [history.lastAt]);

    const runDiagnosis = useCallback(async () => {
        setDiagnosing(true);
        try {
            const snapshot = await fetchHostSnapshot(true);
            if (snapshot.diagnosis) {
                setDiagnosis(snapshot.diagnosis);
            } else {
                onToast('诊断结果为空');
            }
        } catch (error) {
            onToast(error instanceof Error ? error.message : '诊断失败');
        } finally {
            setDiagnosing(false);
        }
    }, [onToast]);

    const downloadBundle = useCallback(() => {
        setDownloading(true);
        // 用 <a download> 而不是 fetch + Blob：诊断包已经是最新的两帧，服务端
        // 直接给附件，浏览器自己落盘，大包也不会卡住主线程。
        const link = document.createElement('a');
        link.href = hostDiagnosticsURL();
        link.download = '';
        link.rel = 'noopener';
        document.body.appendChild(link);
        link.click();
        link.remove();
        window.setTimeout(() => setDownloading(false), 1200);
    }, []);

    if (!enabled) {
        return (
            <section className="section-full machine-page">
                <div className="machine-top">
                    <div className="machine-top-main">
                        <h2 className="panel-title">机器状态</h2>
                        <p className="machine-top-note">机器状态动态采集已关闭，后台不会继续采样主机指标。</p>
                    </div>
                    <div className="machine-top-side">
                        <span className="badge slate">已关闭</span>
                        {onOpenSettings ? <button className="btn primary" type="button" onClick={onOpenSettings}>去设置开启</button> : null}
                    </div>
                </div>
                <div className="card panel">
                    <div className="empty-state">在「设置 → 机器状态动态采集」中开启后，这里会恢复实时指标、趋势图和诊断。</div>
                </div>
            </section>
        );
    }

    const lastAt = history.lastAt || (metrics?.collectedAt ? Date.parse(metrics.collectedAt) : 0);
    // 网关在没人看时是 15 秒一档，30 秒没新点才算真的断（重启 / 挂起）。
    const stale = lastAt > 0 && now - lastAt > 30_000;

    const statusBadge = status === 'forbidden'
        ? {tone: 'red' as Tone, text: '无权限'}
        : status === 'connecting'
            ? {tone: 'slate' as Tone, text: '连接中…'}
            : status === 'reconnecting'
                ? {tone: 'amber' as Tone, text: '连接断开，重连中'}
                : status === 'polling'
                    ? {tone: 'blue' as Tone, text: '轮询模式（推送不可用）'}
                    : stale
                        ? {tone: 'amber' as Tone, text: '等待数据'}
                        : {tone: 'green' as Tone, text: '实时推送中'};

    const memTotal = metrics?.memTotal != null ? metrics.memTotal / (1024 * 1024) : undefined;
    const processPercent = metrics?.processRssMiB != null && metrics?.memTotal
        ? (metrics.processRssMiB / (metrics.memTotal / (1024 * 1024))) * 100
        : undefined;

    const memUsed = memTotal != null && metrics?.memUsed != null ? metrics.memUsed / (1024 * 1024) : undefined;
    const memNote = metrics?.memAvailable
        ? [
            `${mibText(memUsed)} / ${mibText(memTotal)}`,
            metrics.memCached ? `缓存 ${formatBytes(metrics.memCached)}` : null,
            metrics.swapAvailable ? `交换 ${formatBytes(metrics.swapUsed)} / ${formatBytes(metrics.swapTotal)}` : null,
        ].filter(Boolean).join(' · ')
        : '本机未暴露内存信息';

    const gatewayThreads = metrics?.processThreads ?? metrics?.osThreads;
    const handleLimitNote = metrics?.processFds != null ? 'open() 失败时先看这个数' : undefined;

    return (
        <section className="section-full machine-page">
            <div className="machine-top">
                <div className="machine-top-main">
                    <h2 className="panel-title">机器状态</h2>
                    <p className="machine-top-note">
                        {metrics?.hostname || '网关主机'}
                        {metrics?.osName ? ` · ${metrics.osName}` : metrics?.platform ? ` · ${metrics.platform}` : ''}
                        {metrics?.uptimeSeconds ? ` · 已开机 ${formatDuration(metrics.uptimeSeconds)}` : ''}
                        {` · 网关持续采样（${pollMs / 1000} 秒一档，无人查看时降频），离开页面也不中断`}
                    </p>
                </div>
                <div className="machine-top-side">
                    <span className={`badge ${statusBadge.tone}`}>{statusBadge.text}</span>
                    <span className="machine-top-time">更新于 {lastAt ? clockText(lastAt) : '—'}</span>
                    <button className="btn ghost" type="button" onClick={() => void runDiagnosis()} disabled={diagnosing}>
                        {diagnosing ? '诊断中…' : '诊断'}
                    </button>
                    <button className="btn primary" type="button" onClick={downloadBundle} disabled={downloading}>
                        诊断包
                    </button>
                </div>
            </div>

            <div className="machine-kpis">
                <KpiCard
                    label="CPU 使用率"
                    value={percentText(metrics?.cpuPercent, 1).replace('%', '')}
                    unit="%"
                    percent={metrics?.cpuPercent}
                    note={metrics ? `${metrics.cpuCount} 个逻辑核${metrics.cpuPhysicalCores ? ` · ${metrics.cpuPhysicalCores} 物理核` : ''}` : '读取中…'}
                />
                <KpiCard
                    label="负载（1/5/15 分钟）"
                    value={formatLoad(metrics?.load1)}
                    note={metrics ? `${formatLoad(metrics.load5)} / ${formatLoad(metrics.load15)}${metrics.cpuCount ? ` · 每核 ${formatLoad(metrics.load1 / metrics.cpuCount)}` : ''}` : '读取中…'}
                />
                <KpiCard
                    label="主机内存"
                    value={percentText(metrics?.memPercent, 0).replace('%', '')}
                    unit="%"
                    percent={metrics?.memPercent}
                    note={memNote}
                />
                <KpiCard
                    label="网关常驻内存"
                    value={mibText(metrics?.processRssMiB).replace(' MiB', '').replace(' GiB', '')}
                    unit={metrics?.processRssMiB != null && metrics.processRssMiB >= 1024 ? 'GiB' : 'MiB'}
                    percent={processPercent}
                    tone="blue"
                    note={metrics?.processHeapMiB != null ? `Go 堆 ${mibText(metrics.processHeapMiB)} · ${metrics.processRssFromRuntime ? '来自 runtime' : '常驻'}` : '读取中…'}
                />
                <KpiCard
                    label="线程"
                    value={gatewayThreads != null ? gatewayThreads.toLocaleString() : '—'}
                    note={metrics?.goroutines != null ? `goroutine ${metrics.goroutines.toLocaleString()}` : undefined}
                />
                <KpiCard
                    label="文件句柄"
                    value={metrics?.processFds != null ? metrics.processFds.toLocaleString() : '—'}
                    note={handleLimitNote}
                />
            </div>

            <CoreStrip metrics={metrics} />

            <DiagnosisCard diagnosis={diagnosis} loading={diagnosing} onRefresh={() => void runDiagnosis()} />

            <div className="machine-side-grid">
                <VolumesCard metrics={metrics} />
                <EnvCard metrics={metrics} activity={diagnosis?.gateway ?? null} />
                <ProcessCard metrics={metrics} />
            </div>

            <div className="machine-trend">
                <div className="machine-trend-head">
                    <div>
                        <h3 className="panel-title">近 {HOST_WINDOW_MINUTES} 分钟趋势</h3>
                        <p className="machine-top-note">曲线末端即上方卡片的数值；网关一直在采样，只有重启或挂起会留下断口。</p>
                    </div>
                    <span className="machine-top-time">
                        {history.points.length > 0
                            ? `${history.points.length} 个采样点 · 最早 ${clockText(history.points[0].t)}`
                            : '等待第一个采样点'}
                    </span>
                </div>
                <div className="machine-chart-grid">
                    {TREND_CARDS.map((card) => (
                        <TrendChart key={card.title} card={card} points={history.points} pollMs={pollMs} now={now} windowMs={TREND_WINDOW_MS} />
                    ))}
                </div>
            </div>
        </section>
    );
}
