// @generated-from main.tsx — 由重构脚本拆分生成，请直接维护本文件。
import React from 'react';
import {
    API_BASE,
    chatgptPlanLabel,
    checkinReport,
    creditsExpiryHint,
    cursorBridgeStatusLabel,
    cursorBridgeTone,
    flowBadgeTone,
    healthStatusLabel,
    healthTone,
    highlightSegments,
    isQoderConnectKind,
    isTraeConnectKind,
    isZCodeConnectKind,
    OAUTH_USAGE_POLL_MS,
    OAUTH_USAGE_STORAGE_PREFIX,
    providerConnectKind,
    resetHint,
    retrySecondsLabel,
    useNowTick
} from '../lib';
import {shouldPollUsage, usagePollDelay, usageRequestKey, UsagePollDecision} from '../usage_poll';
import {
    BadgeTone,
    ChatGPTOAuthUsageReport,
    ChatGPTResetConsumeResult,
    ChatGPTResetCredit,
    ClaudeOAuthUsageBucket,
    ClaudeOAuthUsageReport,
    CursorBridgeRuntime,
    CursorOAuthUsageReport,
    DeepSeekBalanceReport,
    Provider,
    SubscriptionCheckin,
    TimeHint,
    WorkBuddyUsage,
    ZhipuUsageBucket,
    ZhipuUsageReport
} from '../types';
import {SubscriptionUsagePanel} from './subscription';
import {Badge, BrandIcon, MoreMenu} from './ui';
import {WorkBuddyTaskPanel} from './workbuddy_tasks';

/** 最早到期的重置卡时间；无效时间为空串。 */
function resetCreditEarliestExpiry(credits: ChatGPTResetCredit[]): string {
  let earliest = '';
  for (const credit of credits) {
    const value = (credit.expiresAt || '').trim();
    if (!value) continue;
    if (!earliest || value < earliest) earliest = value;
  }
  return earliest;
}

function showPanelToast(text: string, duration = 3000) {
  const toast = document.getElementById('toast');
  if (!toast) return;
  toast.textContent = text;
  toast.classList.add('show');
  window.setTimeout(() => toast.classList.remove('show'), duration);
}

export function RouteCard({ active, name, tone, status, meta, flow, onClick, onTest, onEdit, onClone, onDelete }: { active?: boolean; name: string; tone: BadgeTone; status: string; meta: string; flow: string[]; onClick: () => void; onTest: () => void; onEdit: () => void; onClone: () => void; onDelete: () => void }) {
  return (
    <div className={`route-card clickable ${active ? 'active' : ''}`} onClick={onClick} role="button" tabIndex={0} onKeyDown={(event) => { if (event.key === 'Enter') onClick(); }}>
      <div className="route-top">
        <div className="route-name">{name}</div>
        <div className="route-actions">
          <Badge tone={tone}>{status}</Badge>
          <button className="icon-btn" onClick={(event) => { event.stopPropagation(); onEdit(); }} title="编辑路由">编辑</button>
          <button className="icon-btn" onClick={(event) => { event.stopPropagation(); onClone(); }} title="克隆为新路由">克隆</button>
          <button className="icon-btn" onClick={(event) => { event.stopPropagation(); onTest(); }} title="路由对话测试">对话测试</button>
          <button className="icon-btn danger" onClick={(event) => { event.stopPropagation(); onDelete(); }} title="删除路由">删除</button>
        </div>
      </div>
      <div className="route-meta">{meta}</div>
      <div className="protocol-pair">
        {flow.map((item, index) => (
          <React.Fragment key={`${item}-${index}`}>
            <Badge tone={flowBadgeTone(item)}>{item}</Badge>
            {index < flow.length - 1 && <span className="protocol-arrow">→</span>}
          </React.Fragment>
        ))}
      </div>
    </div>
  );
}

export function formatClaudeUsageResetAt(resetsAt?: string) {
  if (!resetsAt) return '—';
  const date = new Date(resetsAt);
  if (Number.isNaN(date.getTime())) return resetsAt;
  return date.toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' });
}

export function claudeUsageFillTone(utilization: number): string {
  if (utilization >= 90) return 'danger';
  if (utilization >= 70) return 'warn';
  return 'ok';
}

export function readOAuthUsageCache<T>(path: string): T | null {
  try {
    const raw = sessionStorage.getItem(`${OAUTH_USAGE_STORAGE_PREFIX}${path}`);
    if (!raw) return null;
    return JSON.parse(raw) as T;
  } catch {
    return null;
  }
}

export function writeOAuthUsageCache(path: string, data: unknown) {
  try {
    sessionStorage.setItem(`${OAUTH_USAGE_STORAGE_PREFIX}${path}`, JSON.stringify(data));
  } catch {
    // ignore quota / private mode
  }
}

// isDocumentActive 判定「本页面所在 app 当前是否为用户前台」。
// 注意：Chrome 切到别的 macOS app（失去系统焦点）时并不会触发 visibilitychange，
// document.visibilityState 仍是 'visible'；只有 document.hasFocus() 会变成 false。
// 只用于「真正需要省厂商额度」的轮询（订阅额度/用量），每次都会打厂商 API。
export function isDocumentActive(): boolean {
  if (typeof document === 'undefined') return true;
  return document.visibilityState !== 'hidden' && document.hasFocus();
}

// isPageVisible 只判「标签页是否可见」，不要求 OS 焦点。
// 控制台自己的轮询（实时日志、/__state、健康检查）用它：副屏不聚焦、
// 内嵌浏览器（Electron）里 document.hasFocus() 可能恒为 false，
// 用 isDocumentActive 会让实时日志整页停更（2026-10-29 线上实证）。
export function isPageVisible(): boolean {
  if (typeof document === 'undefined') return true;
  return document.visibilityState !== 'hidden';
}

// ── 额度轮询：全控制台一只定时器 ───────────────────────────────────────────
//
// 每个 Provider 卡片最多挂一份额度面板（Claude / Cursor / ChatGPT / WorkBuddy /
// 订阅类 / 智谱 / DeepSeek），每份面板都打一个会碰到厂商 API 的 /usage 端点。
// 早先每份面板各装一只 3 分钟定时器、各自 fetch，pageVisible 又在 effect 的依赖
// 里，任何一次 focus/blur 都会把当页全部面板的 effect 重跑、立刻各发一遍：十二个
// 账号的页面一次回到前台就是十二个厂商请求，编辑 Provider 的弹窗开着也照打不误。
//
// 现在按账号记录「最近一次结案时刻 + 在飞状态」，一只定时器跳到点时自己挑谁该
// 发；弹窗压着时整轮安静；定时器不随 focus 重建，可见性只在跳的那一刻判定。
// 判定逻辑在 usage_poll.ts，由 web/src/lib.test.mjs 直接测。
type AccountUsageRequest = {
    /** 额度端点（不含查询串）。 */
    path: string;
    /** 同一个账号下另取的数据（签到状态等）：跟着额度请求一起送，不单独开轮询。 */
    query?: Record<string, string | undefined>;
};
type UsageEntry = {
    request: AccountUsageRequest;
    report: unknown;
    loading: boolean;
    /** 已经取到的那份数据对应哪个请求键：换了附加查询就是没取过。 */
    fetchedKey: string | null;
    listeners: Set<() => void>;
    pending: boolean;
    lastFinishedAt: number | null;
    controller: AbortController | null;
};
const usageEntries = new Map<string, UsageEntry>();
let usageTimer: number | null = null;

function usageEntry(accountKey: string, request?: AccountUsageRequest): UsageEntry {
    let entry = usageEntries.get(accountKey);
    if (!entry) {
        entry = { request: request ?? { path: '' }, report: null, loading: false, fetchedKey: null, listeners: new Set(), pending: false, lastFinishedAt: null, controller: null };
        usageEntries.set(accountKey, entry);
    } else if (request) {
        entry.request = request;
    }
    return entry;
}

function notifyUsageEntry(entry: UsageEntry) {
    for (const listener of [...entry.listeners]) listener();
}

// 弹窗（编辑 Provider、对话测试……）压在上面时背后的额度面板没人看：整轮暂停。
// 面板自己就渲染在弹窗里时不受影响，由 usageEntryInModal 单独认。
function usageGateOpen(): boolean {
    return typeof document !== 'undefined' && document.querySelector('.modal-backdrop') !== null;
}

function usagePollSchedule() {
    if (typeof window === 'undefined') return;
    if (usageTimer != null) window.clearTimeout(usageTimer);
    usageTimer = null;
    let delay = Infinity;
    const now = Date.now();
    for (const entry of usageEntries.values()) {
        if (entry.listeners.size === 0) continue;
        delay = Math.min(delay, usagePollDelay(now, entry.lastFinishedAt, OAUTH_USAGE_POLL_MS));
    }
    if (!Number.isFinite(delay)) return;
    usageTimer = window.setTimeout(() => {
        usageTimer = null;
        usagePollTick();
    }, delay);
}

function usagePollTick() {
    sendUsageRequests(false);
    usagePollSchedule();
}

/** 发掉所有到期的额度请求；refresh=true 时订阅着的全部强制刷新。 */
function sendUsageRequests(refresh: boolean) {
    const gateOpen = usageGateOpen();
    for (const [accountKey, entry] of usageEntries) {
        if (entry.listeners.size === 0) continue;
        if (entry.pending) continue;
        const decision: UsagePollDecision = refresh
            ? 'fetch'
            : shouldPollUsage({
                fetched: entry.report != null && entry.fetchedKey === usageRequestKey(entry.request.path, entry.request.query),
                lastFinishedAt: entry.lastFinishedAt,
                now: Date.now(),
                active: isDocumentActive(),
                modalOpen: gateOpen && !usageEntryInModal(accountKey),
                pending: entry.pending,
                intervalMs: OAUTH_USAGE_POLL_MS,
            });
        if (decision !== 'fetch') continue;
        void fetchUsage(entry, refresh);
    }
}

// 面板渲染在弹窗里时照常轮询：它是用户正在看的那一份。
function usageEntryInModal(accountKey: string): boolean {
    if (typeof document === 'undefined') return false;
    const node = document.querySelector(`[data-usage-account="${CSS.escape(accountKey)}"]`);
    return node != null && node.closest('.modal-backdrop') != null;
}

async function fetchUsage(entry: UsageEntry, force: boolean): Promise<void> {
    if (entry.pending) return;
    entry.pending = true;
    const controller = new AbortController();
    entry.controller = controller;
    if (force || entry.report == null) {
        entry.loading = true;
        notifyUsageEntry(entry);
    }
    const fetchedKey = usageRequestKey(entry.request.path, entry.request.query);
    try {
        const separator = fetchedKey.includes('?') ? '&' : '?';
        const url = `${API_BASE}${fetchedKey}${force ? `${separator}refresh=1` : ''}`;
        const response = await fetch(url, { signal: controller.signal });
        if (!response.ok) {
            throw new Error(`HTTP ${response.status}`);
        }
        const data = await response.json();
        entry.report = data;
        entry.fetchedKey = fetchedKey;
        writeOAuthUsageCache(fetchedKey, data);
    } catch (error) {
        if (!(error instanceof DOMException && error.name === 'AbortError') && entry.report == null) {
            entry.report = { available: false, error: error instanceof Error ? error.message : '无法获取额度' };
        }
    } finally {
        entry.pending = false;
        entry.controller = null;
        entry.loading = false;
        entry.lastFinishedAt = Date.now();
        notifyUsageEntry(entry);
        if (entry.listeners.size > 0) usagePollSchedule();
    }
}

/** 这个账号上最后一份面板卸载：停掉它的在飞请求。取到的数据写进了缓存，下次重挂先垫上。 */
function releaseUsageAccount(accountKey: string) {
    const entry = usageEntries.get(accountKey);
    if (!entry) return;
    entry.controller?.abort();
    entry.listeners.clear();
    usageEntries.delete(accountKey);
    usagePollSchedule();
}

/**
 * 卡片与弹窗里挂的同一份面板用同一个账号键：两份面板共用一份数据与一条在飞请求
 * （listeners 是集合，不会重复发）。
 */
function accountUsageKey(code: string, accountKey: string | undefined, providerId: string): string {
  return `${code}:${accountKey ?? providerId}`;
}

/**
 * Provider 卡片上额度面板的账号键。一个账号键只对应一次在飞请求与一份数据；
 * 编辑弹窗里的同一份面板传同一个键，两者共享。
 */
function usageAccountKeyFor(providerId: string): string {
  return `provider:${providerId}`;
}

/** 面板的账号请求：额度端点。没有另取的数据，查询串为空。 */
function usageAccountRequest(path: string): AccountUsageRequest {
  return { path };
}

/**
 * 一个 Provider 账号的额度面板订阅。
 */
export function useAccountUsageReport<T extends { available?: boolean; error?: string }>(
  enabled: boolean,
  accountKey: string,
  request: AccountUsageRequest,
) {
  const requestKey = usageRequestKey(request.path, request.query);
  const [data, setData] = React.useState<EntryData<T>>(() => {
    if (!enabled) return EMPTY_USAGE_DATA as EntryData<T>;
    const entry = usageEntries.get(accountKey);
    if (entry && entry.report != null) return { report: entry.report as T, loading: entry.loading };
    return { report: readOAuthUsageCache<T>(requestKey), loading: false };
  });
  const dataRef = React.useRef(data);
  dataRef.current = data;

  React.useEffect(() => {
    if (!enabled) {
      setData(EMPTY_USAGE_DATA as EntryData<T>);
      return undefined;
    }
    const entry = usageEntry(accountKey, request);
    const publish = () => {
      const next = entry.report as T | null;
      if (next === dataRef.current.report && entry.loading === dataRef.current.loading) return;
      const value = { report: next, loading: entry.loading };
      dataRef.current = value;
      setData(value);
    };
    entry.listeners.add(publish);
    publish();
    usagePollSchedule();
    return () => {
      entry.listeners.delete(publish);
      if (entry.listeners.size === 0) releaseUsageAccount(accountKey);
      else usagePollSchedule();
    };
  }, [enabled, accountKey, requestKey]);

  // 订阅着的时候把请求描述带进 entry：查询串变了（签到状态）下一跳直接用新的。
  if (enabled && usageEntries.get(accountKey)?.listeners.size) usageEntry(accountKey, request);

  const refresh = () => {
    if (!enabled) return;
    void fetchUsage(usageEntry(accountKey, request), true);
  };

  return { report: data.report, loading: data.loading, refresh };
}

type EntryData<T> = { report: T | null; loading: boolean };
const EMPTY_USAGE_DATA: EntryData<never> = { report: null, loading: false };

/** 各家额度面板的公共外壳：标题行（plan 档位 + 状态 + 刷新）+ 告警消息行 + 内容区。
 *  planName 统一走友好名（如 self_serve_business_prolite → Business Pro Lite）；
 *  展开面板时卡片副标题仍可见，但档位信息足够短，重复展示不影响阅读。 */
export function UsagePanelShell({ panelClass, compact, subtitle, title, planName, stateMessage, loading, fetchedAt, onRefresh, children }: {
  panelClass?: string;
  compact?: boolean;
  subtitle?: string;
  title: string;
  planName?: string;
  stateMessage?: string;
  loading: boolean;
  fetchedAt?: string;
  onRefresh: () => void;
  children: React.ReactNode;
}) {
  const planSuffix = planName ? ` · ${planName}` : '';
  // “可用”是正面信号，收进标题行不占竖向空间；其余（已用尽/降级/异常）单独一行展开，可完整阅读
  const warnMessage = stateMessage && stateMessage !== '可用' ? stateMessage : '';
  return (
    <div
      data-usage-account={panelClass ? panelClass.split(' ')[0] : ''}
      className={`claude-usage-panel${panelClass ? ` ${panelClass}` : ''}${compact ? ' compact' : ''}`}
      onClick={(event) => event.stopPropagation()}
    >
      <div className="claude-usage-title">
        <span>{title}{planSuffix}</span>
        <span className="claude-usage-actions">
          {stateMessage === '可用' ? <span className="claude-usage-state ok"><i className="live-dot" aria-hidden="true" />可用</span> : null}
          {loading ? <span className="claude-usage-status">刷新中…</span> : fetchedAt ? <span className="claude-usage-status">更新于 {formatClaudeUsageResetAt(fetchedAt)}</span> : null}
          <button
            type="button"
            className="btn btn-tiny"
            disabled={loading}
            onClick={(event) => {
              event.stopPropagation();
              onRefresh();
            }}
          >
            刷新
          </button>
        </span>
      </div>
      {warnMessage ? <div className="claude-usage-state warn">{warnMessage}</div> : null}
      {children}
    </div>
  );
}

/** 单个额度桶：标签 + 百分比（随用量变色）+ 进度条 + 重置时间。
 *  tone class 直接挂在行上（替代旧 :has() 选择器，兼容旧浏览器）。
 *  compact 下格子窄，重置时间省去“重置 ”前缀只留时间戳（完整文案保留在 title）。 */
/**
 * TimeLine 渲染一行时间：绝对时刻和「还有多久」拆成两段，窄卡片先截前者——
 * 「2 小时后」比「10月29日 23:45」更能决定要不要现在用这个账号。
 */
export function TimeLine({ hint, className = 'claude-usage-reset', compact }: { hint: TimeHint; className?: string; compact?: boolean }) {
  if (hint.passed || !hint.when || !hint.relative) {
    return <div className={`${className}${hint.passed ? ' passed' : ''}`} title={hint.title}>{hint.text}</div>;
  }
  return (
    <div className={`${className} usage-time`} title={hint.title}>
      {/* 紧凑卡片里进度条下面那一行就是重置时间，前缀词让位给时刻本身 */}
      {!compact && hint.label ? <span className="time-label">{hint.label}</span> : null}
      <span className="time-when">{hint.when}</span>
      <span className="time-relative">· {hint.relative}</span>
    </div>
  );
}

export function UsageBucketRow({ label, percent, note, reset, compact }: {
  label: string;
  percent: number;
  note?: string;
  /** 窗口什么时候重新开始（厂商没给就不渲染这一行，不编一个时间）。 */
  reset?: TimeHint | null;
  compact?: boolean;
}) {
  const tone = claudeUsageFillTone(percent);
  const noteText = note || '';
  return (
    <div
      className={`claude-usage-row ${tone}`}
      title={[`${label} ${percent.toFixed(0)}%`, noteText, reset?.title].filter(Boolean).join(' · ')}
    >
      <div className="claude-usage-head">
        <span>{label}</span>
        <span>{percent.toFixed(0)}%</span>
      </div>
      <div className="claude-usage-track">
        <div className={`claude-usage-fill ${tone}`} style={{ width: `${percent}%` }} />
      </div>
      {noteText ? <div className="claude-usage-reset">{noteText}</div> : null}
      {reset ? <TimeLine hint={reset} compact={compact} /> : null}
    </div>
  );
}

export function ClaudeOAuthUsagePanel({ providerId, accountKey, connected, compact, subtitle }: { accountKey?: string; providerId: string; connected?: boolean; compact?: boolean; subtitle?: string }) {
  const request = React.useMemo(() => usageAccountRequest(`/__providers/${encodeURIComponent(providerId)}/claude-oauth/usage`), [providerId, accountKey]);
  const { report, loading, refresh } = useAccountUsageReport<ClaudeOAuthUsageReport>(
    Boolean(connected),
    accountUsageKey('claude-oauth', accountKey, providerId),
    request,
  );

  if (!connected) return null;

  const buckets: Array<{ key: string; label: string; bucket?: ClaudeOAuthUsageBucket }> = [
    { key: 'five_hour', label: '5 小时额度', bucket: report?.five_hour },
    { key: 'seven_day', label: '7 天额度', bucket: report?.seven_day },
    { key: 'seven_day_sonnet', label: '7 天 Sonnet', bucket: report?.seven_day_sonnet },
    { key: 'seven_day_opus', label: '7 天 Opus', bucket: report?.seven_day_opus },
  ].filter((item) => item.bucket);

  return (
    <UsagePanelShell compact={compact} subtitle={subtitle} title="Claude 订阅额度" loading={loading} fetchedAt={report?.fetchedAt} onRefresh={refresh}>
      {!report ? (
        <div className="claude-usage-empty">{loading ? '正在拉取额度…' : '暂无额度数据'}</div>
      ) : !report.available ? (
        <div className="claude-usage-empty error">{report.error || '额度不可用'}</div>
      ) : buckets.length === 0 ? (
        <div className="claude-usage-empty">未返回额度桶数据</div>
      ) : (
        <div className="claude-usage-grid">
          {buckets.map(({ key, label, bucket }) => (
            <UsageBucketRow
              key={key}
              label={label}
              percent={Math.min(100, Math.max(0, bucket?.utilization ?? 0))}
              reset={resetHint(bucket?.resets_at)}
              compact={compact}
            />
          ))}
        </div>
      )}
    </UsagePanelShell>
  );
}

export function ZhipuUsagePanel({ providerId, accountKey, compact, subtitle }: { accountKey?: string; providerId: string; compact?: boolean; subtitle?: string }) {
  const request = React.useMemo(() => usageAccountRequest(`/__providers/${encodeURIComponent(providerId)}/zhipu/usage`), [providerId, accountKey]);
  const { report, loading, refresh } = useAccountUsageReport<ZhipuUsageReport>(
    true,
    accountUsageKey('zhipu', accountKey, providerId),
    request,
  );

  // 卡片上：非编程套餐 / 拉取失败都不渲染，避免把「额度 UI」误当成 Provider/转发故障。
  if (compact) {
    if (!report?.available) return null;
  } else if (report?.unsupported) {
    return (
      <div className="claude-usage-panel" onClick={(event) => event.stopPropagation()}>
        <div className="hint-text">该密钥非智谱编程套餐，无额度面板（按量转发不受影响）。</div>
      </div>
    );
  }

  const buckets: Array<{ key: string; label: string; bucket?: ZhipuUsageBucket }> = [
    { key: 'five_hour', label: '5 小时额度', bucket: report?.five_hour },
    { key: 'weekly', label: '每周额度', bucket: report?.weekly },
  ].filter((item) => item.bucket);

  return (
    <UsagePanelShell panelClass="zhipu-usage-panel" compact={compact} subtitle={subtitle} title="智谱编程套餐额度" planName={report?.level} loading={loading} fetchedAt={report?.fetchedAt} onRefresh={refresh}>
      {!report ? (
        <div className="claude-usage-empty">{loading ? '正在拉取额度…' : '暂无额度数据'}</div>
      ) : !report.available ? (
        <div className="claude-usage-empty">{report.error || '额度不可用'}</div>
      ) : buckets.length === 0 ? (
        <div className="claude-usage-empty">未返回额度桶数据（老套餐可能只有 5 小时窗口）</div>
      ) : (
        <div className="claude-usage-grid">
          {buckets.map(({ key, label, bucket }) => (
            <UsageBucketRow
              key={key}
              label={label}
              percent={Math.min(100, Math.max(0, bucket?.utilization ?? 0))}
              reset={resetHint(bucket?.resets_at)}
              compact={compact}
            />
          ))}
        </div>
      )}
      {report?.available && report.resets ? (
        <div className="zhipu-reset-credits" title="重置卡在智谱 / Z.ai 官方页面使用，网关只读展示">
          <span>重置卡</span>
          <span>
            可用 {report.resets.count} 张
            {report.resets.fiveHour ? ` · 5 小时 ${report.resets.fiveHour} 张` : ''}
            {report.resets.weekly ? ` · 每周 ${report.resets.weekly} 张` : ''}
            {report.resets.earliest ? ` · 最早到期 ${formatClaudeUsageResetAt(report.resets.earliest)}` : ''}
          </span>
          <span className="zhipu-reset-credits-hint">到 bigmodel.cn / z.ai 使用</span>
        </div>
      ) : null}
    </UsagePanelShell>
  );
}

/** DeepSeek 账户余额面板。与 Zhipu 同为 api_key provider，复用同一套额度面板样式；
 *  但展示的是金额而非百分比进度条，故不渲染 usage-track。 */
export function DeepSeekBalancePanel({ providerId, accountKey, compact, subtitle }: { accountKey?: string; providerId: string; compact?: boolean; subtitle?: string }) {
  const request = React.useMemo(() => usageAccountRequest(`/__providers/${encodeURIComponent(providerId)}/deepseek/usage`), [providerId, accountKey]);
  const { report, loading, refresh } = useAccountUsageReport<DeepSeekBalanceReport>(
    true,
    accountUsageKey('deepseek', accountKey, providerId),
    request,
  );

  // 卡片上的紧凑版：拉取失败/不支持时不占位。
  if (compact) {
    if (!report?.available) return null;
  } else if (report?.unsupported) {
    return (
      <div className="claude-usage-panel" onClick={(event) => event.stopPropagation()}>
        <div className="hint-text">{`${report.error || '该地址不支持余额查询（可能是第三方中转），转发不受影响。'}`}</div>
      </div>
    );
  }

  const infos = report?.balance_infos || [];

  return (
    <UsagePanelShell compact={compact} subtitle={subtitle} title="DeepSeek 账户余额" loading={loading} fetchedAt={report?.fetchedAt} onRefresh={refresh}>
      {!report ? (
        <div className="claude-usage-empty">{loading ? '正在拉取余额…' : '暂无余额数据'}</div>
      ) : !report.available ? (
        <div className="claude-usage-empty error">{report.error || '余额不可用'}</div>
      ) : infos.length === 0 ? (
        <div className="claude-usage-empty">未返回余额数据</div>
      ) : (
        <div className="claude-usage-grid">
          {infos.map((info) => (
            <div
              className="claude-usage-row"
              key={info.currency}
              title={`可用 ${formatDeepSeekAmount(info.currency, info.total_balance)} · 充值 ${formatDeepSeekAmount(info.currency, info.topped_up_balance)} · 赠送 ${formatDeepSeekAmount(info.currency, info.granted_balance)}`}
            >
              <div className="claude-usage-head">
                <span>可用余额（{info.currency}）</span>
                <span>{formatDeepSeekAmount(info.currency, info.total_balance)}</span>
              </div>
              <div className="claude-usage-reset">
                充值 {formatDeepSeekAmount(info.currency, info.topped_up_balance)}
                {' · '}赠送 {formatDeepSeekAmount(info.currency, info.granted_balance)}
              </div>
            </div>
          ))}
          {!report.isAvailable ? (
            <div className="claude-usage-empty error">账户余额已不足，上游将拒绝新请求。</div>
          ) : null}
        </div>
      )}
    </UsagePanelShell>
  );
}

/** 金额按原始字符串展示（不转 number，避免精度问题），仅补货币符号。 */
export function formatDeepSeekAmount(currency: string, amount?: string) {
  const value = (amount || '').trim();
  if (!value) return '—';
  const symbol = currency === 'CNY' ? '¥' : currency === 'USD' ? '$' : '';
  return `${symbol}${value}`;
}

export function CursorOAuthUsagePanel({ providerId, accountKey, connected, compact, subtitle }: { accountKey?: string; providerId: string; connected?: boolean; compact?: boolean; subtitle?: string }) {
  const request = React.useMemo(() => usageAccountRequest(`/__providers/${encodeURIComponent(providerId)}/cursor-oauth/usage`), [providerId, accountKey]);
  const { report, loading, refresh } = useAccountUsageReport<CursorOAuthUsageReport>(
    Boolean(connected),
    accountUsageKey('cursor-oauth', accountKey, providerId),
    request,
  );

  if (!connected) return null;

  const buckets = report?.buckets || [];

  return (
    <UsagePanelShell panelClass="cursor-usage-panel" compact={compact} subtitle={subtitle} title="Cursor 订阅额度" planName={report?.planName} stateMessage={report?.available ? report.message : undefined} loading={loading} fetchedAt={report?.fetchedAt} onRefresh={refresh}>
      {!report ? (
        <div className="claude-usage-empty">{loading ? '正在拉取额度…' : '暂无额度数据'}</div>
      ) : !report.available ? (
        <div className="claude-usage-empty error">{report.error || '额度不可用'}</div>
      ) : buckets.length === 0 ? (
        <div className="claude-usage-empty">{report.message || '未返回额度桶数据'}</div>
      ) : (
        <div className="claude-usage-grid">
          {buckets.map((bucket, index) => (
            <UsageBucketRow
              key={`${bucket.label}-${index}`}
              label={bucket.label}
              percent={Math.min(100, Math.max(0, bucket.utilization ?? 0))}
              note={bucket.detail || ''}
              reset={resetHint(bucket.resetsAt)}
              compact={compact}
            />
          ))}
        </div>
      )}
    </UsagePanelShell>
  );
}

export function ChatGPTOAuthUsagePanel({ providerId, accountKey, connected, compact, subtitle }: { accountKey?: string; providerId: string; connected?: boolean; compact?: boolean; subtitle?: string }) {
  const request = React.useMemo(() => usageAccountRequest(`/__providers/${encodeURIComponent(providerId)}/chatgpt-oauth/usage`), [providerId, accountKey]);
  const { report, loading, refresh } = useAccountUsageReport<ChatGPTOAuthUsageReport>(
    Boolean(connected),
    accountUsageKey('chatgpt-oauth', accountKey, providerId),
    request,
  );
  const [consuming, setConsuming] = React.useState(false);
  const [consumeError, setConsumeError] = React.useState('');

  if (!connected) return null;

  const buckets = report?.buckets || [];

  const useResetCredit = async () => {
    if (consuming) return;
    const credits = report?.resetCredits?.credits || [];
    const earliest = resetCreditEarliestExpiry(credits);
    const prompt = [
      '确定使用 1 张 ChatGPT 重置卡？',
      '',
      `本次会使用最早到期的一张${earliest ? `（${formatClaudeUsageResetAt(earliest)}）` : ''}。`,
      `账号：${subtitle || providerId}`,
      '重置后当前额度窗口会立即重新开始，操作不可撤销。',
    ].join('\n');
    if (!window.confirm(prompt)) return;
    setConsuming(true);
    setConsumeError('');
    try {
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(providerId)}/chatgpt-oauth/reset-credits/consume`, {
        method: 'POST',
        credentials: 'same-origin',
      });
      const data = await response.json().catch(() => null);
      if (!response.ok) {
        const message = data && typeof data === 'object' && 'error' in data
          ? String((data as { error?: { message?: string } }).error?.message || '')
          : '';
        throw new Error(message || `HTTP ${response.status}`);
      }
      const result = data as ChatGPTResetConsumeResult;
      showPanelToast(
        result.code === 'reset'
          ? `重置成功：${result.windowsReset || 1} 个额度窗口已重新开始`
          : result.code === 'nothing_to_reset'
            ? '当前没有已使用的额度窗口，重置卡未消耗'
            : result.code === 'no_credit'
              ? '账户没有可用的重置卡'
              : result.code === 'already_redeemed' || result.code === 'already_used'
                ? '这张重置卡已被使用'
                : `重置结果：${result.code || '未知'}`,
      );
      await refresh();
    } catch (error) {
      setConsumeError(error instanceof Error ? error.message : String(error));
    } finally {
      setConsuming(false);
    }
  };

  const consumeButton = (
    <button
      type="button"
      className="chatgpt-reset-consume-btn"
      disabled={consuming}
      title="使用最早到期的一张重置卡，立即重新开始当前额度窗口"
      onClick={(event) => {
        event.preventDefault();
        event.stopPropagation();
        void useResetCredit();
      }}
    >
      {consuming ? '重置中…' : '使用重置卡'}
    </button>
  );

  return (
    <UsagePanelShell panelClass="chatgpt-usage-panel" compact={compact} subtitle={subtitle} title="ChatGPT Codex 额度" planName={chatgptPlanLabel(report?.planName)} stateMessage={report?.available ? report.message : undefined} loading={loading} fetchedAt={report?.fetchedAt} onRefresh={refresh}>
      {!report ? (
        <div className="claude-usage-empty">{loading ? '正在拉取额度…' : '暂无额度数据'}</div>
      ) : !report.available ? (
        <div className="claude-usage-empty error">{report.error || '额度不可用'}</div>
      ) : buckets.length === 0 ? (
        <div className="claude-usage-empty">{report.message || '未返回额度桶数据'}</div>
      ) : (
        <div className="claude-usage-grid">
          {buckets.map((bucket, index) => (
            <UsageBucketRow
              key={`${bucket.label}-${index}`}
              label={bucket.label}
              percent={Math.min(100, Math.max(0, bucket.utilization ?? 0))}
              note={bucket.detail || ''}
              reset={resetHint(bucket.resetsAt)}
              compact={compact}
            />
          ))}
        </div>
      )}
      {report?.available && report.resetCredits && (report.resetCredits.availableCount > 0 || (report.resetCredits.credits?.length || 0) > 0) ? (() => {
        const credits = report.resetCredits.credits || [];
        const summary = (
          <>
            <span className="chatgpt-reset-credits-title">
              <span className="chatgpt-reset-credits-label">重置卡</span>
              <span className="chatgpt-reset-credits-count">{report.resetCredits.availableCount}</span>
              <span className="chatgpt-reset-credits-unit">张可用</span>
              {credits.length > 0 ? (
                <span className="chatgpt-reset-credits-expiry">
                  最早到期 {formatClaudeUsageResetAt(resetCreditEarliestExpiry(credits))}
                </span>
              ) : null}
            </span>
            <span className="chatgpt-reset-credits-actions">{consumeButton}</span>
          </>
        );
        const list = credits.length > 0 ? (
          <div className="chatgpt-reset-credits-list">
            {credits.map((credit, index) => (
              <div className="chatgpt-reset-credit-item" key={credit.id || index} title={credit.title || ''}>
                <span className="chatgpt-reset-credit-index">{index + 1}</span>
                <span className="chatgpt-reset-credit-name">{credit.title || '重置卡'}</span>
                <span className="chatgpt-reset-credit-expiry">
                  {credit.expiresAt ? formatClaudeUsageResetAt(credit.expiresAt) : '有效期未知'}
                </span>
              </div>
            ))}
          </div>
        ) : (
          <div className="chatgpt-reset-credit-item">
            <span className="chatgpt-reset-credit-name">暂无可查明细的可用重置卡</span>
          </div>
        );
        // 明细折进摘要行：一张卡 3 条就把整张 Provider 卡撑高 100 多 px，同排卡被
        // 跟着拉伸。没有明细可看时不渲染折叠控件，避免出现一个点开是空的箭头。
        const errorRow = consumeError ? <div className="chatgpt-reset-consume-error">{consumeError}</div> : null;
        return credits.length > 0 ? (
          <details
            className="chatgpt-reset-credits expandable"
            title="官方发放的限流重置卡，用一张可重置 5 小时 / 7 天额度窗口"
            onKeyDown={(event) => event.stopPropagation()}
          >
            <summary className="chatgpt-reset-credits-head">{summary}</summary>
            {list}
            {errorRow}
          </details>
        ) : (
          <div className="chatgpt-reset-credits" title="官方发放的限流重置卡，用一张可重置 5 小时 / 7 天额度窗口">
            <div className="chatgpt-reset-credits-head">{summary}</div>
            {list}
            {errorRow}
          </div>
        );
      })() : null}
    </UsagePanelShell>
  );
}

/**
 * MatchText 按搜索词把一段文字里的命中部分标出来。
 *
 * 卡片上的名称、账号、创建者都是筛选会命中的字段，命中却看不出来等于没命中：
 * 用户搜「4947」时得能一眼看到是哪个账号匹配上的。
 */
function MatchText({ text, query }: { text: string; query?: string }) {
  if (!query || !query.trim() || !text) return <>{text}</>;
  const segments = highlightSegments(text, query);
  if (segments.length === 0) return <>{text}</>;
  return (
    <>
      {segments.map((segment, index) => (
        segment.hit
          ? <mark key={index} className="search-hit">{segment.text}</mark>
          : <React.Fragment key={index}>{segment.text}</React.Fragment>
      ))}
    </>
  );
}

export function ProviderCard({ active, selected, name, providerId, protocol, tone, url, defaultModel, modelCount, usedCount, healthStatus, nextRetryAt, testing, chatTesting, readOnly, selectable, providerDisabled, onToggleEnabled, onToggleRTK, subtitle, icon, provider, isClaudeOAuth, claudeOAuthConnected, isCursorOAuth, cursorOAuthConnected, isChatGPTOAuth, chatgptOAuthConnected, isQoderPAT, qoderPATConnected, isWorkBuddy, workBuddyConnected, cursorBridge, ownerName, highlightQuery, onToggleSelect, onClick, onTest, onChatTest, onConformance, onEdit, onClone, onDelete, layout = 'card' }: { active?: boolean; selected?: boolean; name: string; providerId: string; protocol: string; tone: BadgeTone; url: string; defaultModel?: string; modelCount?: number; usedCount: number; healthStatus: string; nextRetryAt?: string; testing: boolean; chatTesting?: boolean; readOnly?: boolean; selectable?: boolean; providerDisabled?: boolean; onToggleEnabled?: () => void; onToggleRTK?: () => void; subtitle?: string; icon?: string; provider?: Provider; isClaudeOAuth?: boolean; claudeOAuthConnected?: boolean; isCursorOAuth?: boolean; cursorOAuthConnected?: boolean; isChatGPTOAuth?: boolean; chatgptOAuthConnected?: boolean; isQoderPAT?: boolean; qoderPATConnected?: boolean; isWorkBuddy?: boolean; workBuddyConnected?: boolean; cursorBridge?: CursorBridgeRuntime; ownerName?: string; highlightQuery?: string; onToggleSelect: () => void; onClick: () => void; onTest?: () => void; onChatTest?: () => void; onConformance?: () => void; onEdit?: () => void; onClone?: () => void; onDelete?: () => void; layout?: 'card' | 'list' }) {
  // 订阅类（ZCode / Trae）由后端返回的凭证对象决定卡片显示什么：整张 Provider 传进来
  // 比再加六个布尔参数更省事，也不会漏掉以后新增的连接方式。
  const connectKind = provider ? providerConnectKind(provider) : 'api_key';
  const zcodeConnected = !!provider?.zcodeOAuth?.connected;
  const traeConnected = !!provider?.traeOAuth?.connected;
  const isSubscriptionOAuth = isZCodeConnectKind(connectKind) || isTraeConnectKind(connectKind);
  const subscriptionConnected = zcodeConnected || traeConnected;
  const oauthConnected = isClaudeOAuth ? claudeOAuthConnected : isCursorOAuth ? cursorOAuthConnected : isChatGPTOAuth ? chatgptOAuthConnected : isQoderPAT ? qoderPATConnected : isWorkBuddy ? workBuddyConnected : subscriptionConnected;
  const showOAuthBadge = isClaudeOAuth || isCursorOAuth || isChatGPTOAuth || isQoderPAT || isWorkBuddy || isSubscriptionOAuth;
  // Qoder 有两个门：浏览器授权（OAuth 设备码）与粘贴个人访问令牌。徽章跟着
  // 实际走过的那个门走，标成 OAuth 会让用令牌连的人找不到自己的 PT 在哪。
  const oauthBadgeLabel = isQoderPAT ? (provider?.qoderPat?.signInMode === 'pat' ? 'PAT' : 'OAuth') : 'OAuth';
  const isUnavailable = healthStatus === 'unavailable';
  const now = useNowTick(isUnavailable && !!nextRetryAt);
  const retryLabel = isUnavailable ? retrySecondsLabel(nextRetryAt, now) : null;
  const bridgeHint = cursorBridge?.port
    ? ` · :${cursorBridge.port}${cursorBridge.message ? ` · ${cursorBridge.message}` : ''}`
    : (cursorBridge?.message ? ` · ${cursorBridge.message}` : '');
  const subtitleText = subtitle || providerId;
  // 卡片与列表共用同一份额度面板和操作按钮，列表只是换一种排版承载它们。
  const usagePanels = (
    <>
      {/* 额度是运营上最该一眼看到的数字，放在徽章之后、事实区之前当视觉锚点。 */}
      {isClaudeOAuth && claudeOAuthConnected && !providerDisabled ? <ClaudeOAuthUsagePanel providerId={providerId} accountKey={usageAccountKeyFor(providerId)} connected={claudeOAuthConnected} compact subtitle={subtitle} /> : null}
      {isCursorOAuth && cursorOAuthConnected && !providerDisabled ? <CursorOAuthUsagePanel providerId={providerId} accountKey={usageAccountKeyFor(providerId)} connected={cursorOAuthConnected} compact subtitle={subtitle} /> : null}
      {isChatGPTOAuth && chatgptOAuthConnected && !providerDisabled ? <ChatGPTOAuthUsagePanel providerId={providerId} accountKey={usageAccountKeyFor(providerId)} connected={chatgptOAuthConnected} compact subtitle={subtitle} /> : null}
      {isWorkBuddy && workBuddyConnected && !providerDisabled ? <WorkBuddyUsagePanel providerId={providerId} accountKey={usageAccountKeyFor(providerId)} connected={workBuddyConnected} compact subtitle={subtitle} /> : null}
      {isZCodeConnectKind(connectKind) && zcodeConnected && !providerDisabled ? <SubscriptionUsagePanel providerId={providerId} accountKey={usageAccountKeyFor(providerId)} kind={connectKind} connected compact subtitle={subtitle} title="GLM 编程套餐额度" /> : null}
      {isTraeConnectKind(connectKind) && traeConnected && !providerDisabled ? <SubscriptionUsagePanel providerId={providerId} accountKey={usageAccountKeyFor(providerId)} kind={connectKind} connected compact subtitle={subtitle} title="Trae 套餐额度" /> : null}
      {isQoderConnectKind(connectKind) && qoderPATConnected && !providerDisabled ? <SubscriptionUsagePanel providerId={providerId} accountKey={usageAccountKeyFor(providerId)} kind={connectKind} connected compact subtitle={subtitle} title="Qoder 积分" /> : null}
      {!providerDisabled && !isClaudeOAuth && !isCursorOAuth && !isChatGPTOAuth && !isQoderPAT && !isWorkBuddy && !isSubscriptionOAuth && /(?:bigmodel\.cn|z\.ai)/i.test(url) ? <ZhipuUsagePanel providerId={providerId} accountKey={usageAccountKeyFor(providerId)} compact subtitle={subtitle} /> : null}
      {!providerDisabled && !isClaudeOAuth && !isCursorOAuth && !isChatGPTOAuth && !isQoderPAT && !isWorkBuddy && /deepseek\.com/i.test(url) ? <DeepSeekBalancePanel providerId={providerId} accountKey={usageAccountKeyFor(providerId)} compact subtitle={subtitle} /> : null}
    </>
  );
  // 回调缺省＝没有该权限：对应按钮/菜单项不渲染（由父组件按权限点传入）
  const providerActions = (
    <div className="provider-actions">
      {onTest ? <button className="icon-btn" disabled={testing} onClick={onTest} title="从 Provider 接口获取可用模型">{testing ? '获取中' : '获取模型'}</button> : null}
      {onChatTest ? <button className="icon-btn" disabled={!!chatTesting} onClick={onChatTest} title="直连上游对话接口测试">{chatTesting ? '测试中' : '对话测试'}</button> : null}
      {!readOnly && (onEdit || onClone || onDelete || onToggleEnabled || onToggleRTK) ? (
        <>
          {onEdit ? <button className="icon-btn" onClick={onEdit} title="编辑 Provider">编辑</button> : null}
          <MoreMenu
            label="更多操作"
            items={[
              ...(onClone ? [{ label: '克隆', onClick: onClone }] : []),
              ...(onToggleEnabled ? [{ label: providerDisabled ? '启用' : '禁用', onClick: onToggleEnabled }] : []),
              ...(onToggleRTK ? [{ label: provider?.rtkCompress ? '关闭 RTK 压缩' : '开启 RTK 压缩', onClick: onToggleRTK }] : []),
              ...(onDelete ? [{ label: '删除', danger: true, onClick: onDelete }] : []),
            ]}
          />
        </>
      ) : null}
    </div>
  );
  if (layout === 'list') {
    return (
      <div className={`provider-list-row${active ? ' active' : ''}${selected ? ' selected' : ''}${providerDisabled ? ' provider-disabled' : ''}`} onClick={onClick} role="button" tabIndex={0} onKeyDown={(event) => { if (event.key === 'Enter') onClick(); }}>
        {selectable ? (
          <label className="provider-list-check" onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()}>
            <input type="checkbox" checked={!!selected} onChange={onToggleSelect} aria-label={`选择 ${name}`} />
          </label>
        ) : <span className="provider-list-check" />}
        <span className="provider-list-icon"><BrandIcon icon={icon} name={name} size={18} /></span>
        <div className="provider-list-id">
          <div className="provider-list-title">
            <span className="provider-list-name" title={name}><MatchText text={name} query={highlightQuery} /></span>
            <span className="provider-list-sub" title={subtitleText}><MatchText text={subtitleText} query={highlightQuery} /></span>
          </div>
          <div className="provider-list-badges">
            {providerDisabled ? <Badge tone="red">已禁用</Badge> : null}
            {provider?.rtkCompress ? (
              <span title="RTK 工具输出压缩已开启：转发前过滤 tool_result 里的命令输出">
                <Badge tone="green">RTK</Badge>
              </span>
            ) : null}
            <Badge tone="slate">{protocol}</Badge>
            {showOAuthBadge ? (
              <Badge tone={oauthConnected ? 'green' : 'amber'}>{oauthBadgeLabel}{oauthConnected ? ' 已连接' : ' 未连接'}</Badge>
            ) : (
              <Badge tone={healthTone(healthStatus)}>{healthStatusLabel(healthStatus)}</Badge>
            )}
          </div>
        </div>
        <div className="provider-list-quota" onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()}>
          {usagePanels}
        </div>
        <div className="provider-list-metrics">
          <span title={`已获取 ${modelCount ?? 0} 个可用模型`}><b>{modelCount ?? 0}</b> 模型</span>
          <span title={`${usedCount} 个 API Key 引用`}><b>{usedCount}</b> Keys</span>
        </div>
        <div className="provider-list-actions" onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()}>
          {providerActions}
        </div>
      </div>
    );
  }
  return (
    <div className={`provider-card clickable ${active ? 'active' : ''} ${selected ? 'selected' : ''} ${providerDisabled ? 'provider-disabled' : ''}`} onClick={onClick} role="button" tabIndex={0} onKeyDown={(event) => { if (event.key === 'Enter') onClick(); }}>
      <div className="provider-head">
        {/* 品牌图标：一屏十几张卡时，认图比认字快；匹配不到厂商就退回名称首字。 */}
        <span className="provider-icon">
          <BrandIcon icon={icon} name={name} size={20} />
        </span>
        <div className="provider-title-block">
          <div className="provider-name"><MatchText text={name} query={highlightQuery} /></div>
          {/* 副标题只放账号/ID：协议已由徽章表达，不再重复拼接 */}
          <div className="provider-subtitle" title={subtitleText}><MatchText text={subtitleText} query={highlightQuery} /></div>
        </div>
        {selectable ? (
          <label className="provider-check" onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()}>
            <input type="checkbox" checked={!!selected} onChange={onToggleSelect} aria-label={`选择 ${name}`} />
          </label>
        ) : null}
      </div>
      <div className="provider-badges">
        {providerDisabled ? <Badge tone="red">已禁用</Badge> : null}
        {provider?.rtkCompress ? (
          <span title="RTK 工具输出压缩已开启：转发前过滤 tool_result 里的命令输出">
            <Badge tone="green">RTK</Badge>
          </span>
        ) : null}
        <Badge tone="slate">{protocol}</Badge>
        {showOAuthBadge ? (
          <Badge tone={oauthConnected ? 'green' : 'amber'}>{oauthBadgeLabel}{oauthConnected ? ' 已连接' : ' 未连接'}</Badge>
        ) : (
          <Badge tone={healthTone(healthStatus)}>{healthStatusLabel(healthStatus)}</Badge>
        )}
        {retryLabel ? <span className="provider-retry-hint" title="后台会周期性自动重试探测该 Provider">{retryLabel}</span> : null}
        {isCursorOAuth ? (
          <span title={cursorBridge?.checkedAt ? `上次探活：${cursorBridge.checkedAt}${bridgeHint}` : (bridgeHint || undefined)}>
            <Badge tone={cursorBridgeTone(cursorBridge?.status)}>
              {cursorBridgeStatusLabel(cursorBridge?.status)}
            </Badge>
          </span>
        ) : null}
      </div>
      {usagePanels}
      {/* 只留已配置的关键字段：默认模型与模型数量。地址/密钥是编辑期的配置项，
          放在卡片上既占行又没人看；未设置的字段整行省略，不再满屏「未设置」。 */}
      {defaultModel || (modelCount != null && modelCount > 0) ? (
        <div className="provider-card-meta">
          {defaultModel ? (
            <span className="provider-card-meta-item" title={`默认模型：${defaultModel}`}>
              <span className="provider-card-meta-label">默认模型</span>
              <span className="provider-card-meta-value">{defaultModel}</span>
            </span>
          ) : null}
          {modelCount != null && modelCount > 0 ? (
            <span className="provider-card-meta-item" title={`已获取 ${modelCount} 个可用模型`}>
              <span className="provider-card-meta-label">模型</span>
              <span className="provider-card-meta-value">{modelCount} 个</span>
            </span>
          ) : null}
        </div>
      ) : null}
      <div className="provider-foot" onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()}>
        <div className="provider-foot-meta">
          <span><b>{usedCount}</b> 个 API Key</span>
          {ownerName ? <span>创建者 <b><MatchText text={ownerName} query={highlightQuery} /></b></span> : null}
        </div>
        {/* 只读（管理员授权给普通用户的 Provider）也允许获取模型与对话测试；
            编辑/删除等管理操作保持隐藏。后端 requireProviderOwnerForUser 会对
            授权用户放行 test/chat-test（authorized users 视同可测试）。 */}
        {providerActions}
      </div>
    </div>
  );
}

/**
 * WorkBuddy 订阅额度：套餐 credits 的已用/总量。个人版读资源汇总，
 * 团队版没有套餐包，只能从企业计量接口拿到，后端已合并成同一组窗口。
 */
export function WorkBuddyUsagePanel({ providerId, accountKey, connected, compact, subtitle }: { accountKey?: string; providerId: string; connected?: boolean; compact?: boolean; subtitle?: string }) {
  const request = React.useMemo(() => usageAccountRequest(`/__providers/${encodeURIComponent(providerId)}/workbuddy-oauth/usage`), [providerId, accountKey]);
  const { report, loading, refresh } = useAccountUsageReport<WorkBuddyUsage>(
    Boolean(connected),
    accountUsageKey('workbuddy-oauth', accountKey, providerId),
    request,
  );
  const [checkinOverride, setCheckinOverride] = React.useState<SubscriptionCheckin | null>(null);
  React.useEffect(() => { setCheckinOverride(null); }, [providerId]);
  const checkin = checkinOverride || report?.checkin;
  // 哪批积分先过期：厂商报了日期才写这一行
  const expiry = creditsExpiryHint(report?.windows);

  // 签到是账号接口上的另一件事，额度读不出来时它仍要留在原地。
  const hasWindows = Boolean(report) && !report?.error && (report?.windows.length ?? 0) > 0;
  if (compact && !hasWindows && !checkin?.supported) return null;

  return (
    <UsagePanelShell
      panelClass="workbuddy-usage-panel"
      compact={compact}
      subtitle={subtitle}
      title="WorkBuddy 套餐额度"
      planName={report?.plan}
      stateMessage={report && !report.error && report.windows.length > 0 ? '可用' : undefined}
      loading={loading}
      fetchedAt={report?.fetchedAt}
      onRefresh={refresh}
    >
      {!report ? (
        <div className="claude-usage-empty">{loading ? '正在拉取额度…' : '暂无额度数据'}</div>
      ) : report.error ? (
        <div className="claude-usage-empty">{report.error}</div>
      ) : report.windows.length === 0 ? (
        <div className="claude-usage-empty">该账号没有返回额度窗口（未订阅或接口未开放）</div>
      ) : (
        <div className="claude-usage-grid">
          {report.windows.map((window) => (
            <UsageBucketRow
              key={window.name}
              label={window.name}
              percent={Math.min(100, Math.max(0, window.used))}
              note={`${window.amount.toFixed(0)} / ${window.limit.toFixed(0)} ${window.unit}`}
              reset={resetHint(window.resetsAt)}
              compact={compact}
            />
          ))}
        </div>
      )}
      {/* 哪批积分先过期：厂商报了日期才写这一行 */}
      {expiry ? <TimeLine hint={expiry} className="subscription-expiry" /> : null}
      {checkin?.supported ? <CheckinRow providerId={providerId} view={checkin} onChange={setCheckinOverride} /> : null}
      {/* 任务中心：一键完成、活跃上报、保活，以及两个每日开关。 */}
      <WorkBuddyTaskPanel providerId={providerId} connected={connected} compact={compact} />
    </UsagePanelShell>
  );
}

/**
 * 签到行：今天签到了没有、给了多少、连续几天，加两个按钮。
 *
 * 只有厂商真有每日签到活动时后端才给 supported——不是每个订阅都有这个按钮，
 * 一个永远点不亮的开关比没有开关更糟。「立即签到」只在今天还没到手时出现。
 * 连续天数照厂商给的显示：Qoder 的活动接口不报，它那一行就没有这一段。
 */
export function CheckinRow({ providerId, view, onChange }: {
  providerId: string;
  view: SubscriptionCheckin;
  onChange: (next: SubscriptionCheckin) => void;
}) {
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState('');
  const report = checkinReport(view);

  async function post(path: string, body?: unknown): Promise<SubscriptionCheckin | null> {
    setBusy(true);
    setError('');
    try {
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(providerId)}/${path}`, {
        method: 'POST',
        headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
        body: body === undefined ? undefined : JSON.stringify(body),
      });
      const data = await response.json().catch(() => ({}));
      if (!response.ok) {
        throw new Error((data as { error?: { message?: string } }).error?.message || `HTTP ${response.status}`);
      }
      return data as SubscriptionCheckin;
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
      return null;
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="checkin-row" data-state={report.tone}>
      <div className="checkin-line">
        <span className="checkin-say" title={report.text}>
          <i className={`checkin-dot ${report.tone}`} />
          <span>{report.text}</span>
        </span>
        <button
          type="button"
          className={`checkin-btn auto${view.enabled ? ' on' : ''}`}
          aria-pressed={view.enabled}
          disabled={busy}
          title={view.enabled
            ? '已开：网关每天按厂商的算法替这个账号点一次签到。点击关闭。'
            : '让网关每天替这个账号点一次签到（厂商怎么点，它就怎么点）'}
          onClick={() => {
            void post('checkin/auto', { enabled: !view.enabled }).then((next) => {
              if (next) onChange(next);
            });
          }}
        >
          自动签到
        </button>
        {report.dueToday ? (
          <button
            type="button"
            className="checkin-btn now"
            disabled={busy}
            title="现在就替这个账号点一次签到"
            onClick={() => {
              void post('checkin').then((next) => {
                if (next) onChange(next);
              });
            }}
          >
            {busy ? '签到中…' : '立即签到'}
          </button>
        ) : null}
      </div>
      {error ? <div className="checkin-error">{error}</div> : null}
    </div>
  );
}
