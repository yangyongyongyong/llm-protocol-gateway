// @generated-from main.tsx — 由重构脚本拆分生成，请直接维护本文件。
import React from 'react';
import { API_BASE, OAUTH_USAGE_POLL_MS, OAUTH_USAGE_STORAGE_PREFIX, cursorBridgeStatusLabel, cursorBridgeTone, flowBadgeTone, healthStatusLabel, healthTone, retrySecondsLabel, useNowTick } from '../lib';
import { BadgeTone, ChatGPTOAuthUsageReport, ChatGPTResetCredit, ClaudeOAuthUsageBucket, ClaudeOAuthUsageReport, CursorBridgeRuntime, CursorOAuthUsageReport, DeepSeekBalanceReport, ZhipuUsageBucket, ZhipuUsageReport } from '../types';

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
import { Badge, MoreMenu } from './ui';
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
// 所有后台自动轮询都用它兜底：非前台时一律跳过刷新，避免白白消耗后台算力。
export function isDocumentActive(): boolean {
  if (typeof document === 'undefined') return true;
  return document.visibilityState !== 'hidden' && document.hasFocus();
}

export function usePageVisible() {
  const [visible, setVisible] = React.useState(() => isDocumentActive());
  React.useEffect(() => {
    const update = () => setVisible(isDocumentActive());
    document.addEventListener('visibilitychange', update);
    // 监听窗口 focus/blur：Chrome 非最前台时暂停，重新回到前台时恢复。
    window.addEventListener('focus', update);
    window.addEventListener('blur', update);
    return () => {
      document.removeEventListener('visibilitychange', update);
      window.removeEventListener('focus', update);
      window.removeEventListener('blur', update);
    };
  }, []);
  return visible;
}

export function useOAuthUsageReport<T extends { available?: boolean; error?: string }>(
  enabled: boolean,
  path: string,
) {
  const [report, setReport] = React.useState<T | null>(() => (enabled ? readOAuthUsageCache<T>(path) : null));
  const [loading, setLoading] = React.useState(false);
  const pageVisible = usePageVisible();
  const pathRef = React.useRef(path);
  const reportRef = React.useRef(report);
  pathRef.current = path;
  reportRef.current = report;

  const load = React.useCallback(async (opts?: { force?: boolean; skipIfHidden?: boolean; silent?: boolean }) => {
    const force = Boolean(opts?.force);
    const skipIfHidden = opts?.skipIfHidden !== false;
    const silent = Boolean(opts?.silent);
    if (!force && skipIfHidden && !isDocumentActive()) {
      return;
    }
    if (!silent && (force || reportRef.current == null)) {
      setLoading(true);
    }
    try {
      const url = force ? `${API_BASE}${pathRef.current}?refresh=1` : `${API_BASE}${pathRef.current}`;
      const response = await fetch(url);
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}`);
      }
      const data = await response.json() as T;
      setReport(data);
      writeOAuthUsageCache(pathRef.current, data);
    } catch (error) {
      if (reportRef.current == null) {
        setReport({ available: false, error: error instanceof Error ? error.message : '无法获取额度' } as T);
      }
    } finally {
      if (!silent || force) {
        setLoading(false);
      }
    }
  }, []);

  const hadCachedReportRef = React.useRef(report != null);

  React.useEffect(() => {
    if (!enabled) {
      setReport(null);
      return undefined;
    }
    let cancelled = false;
    const safeLoad = async (opts: { force?: boolean; skipIfHidden?: boolean; silent?: boolean }) => {
      if (cancelled) return;
      await load(opts);
    };

    if (pageVisible) {
      void safeLoad({ force: false, skipIfHidden: false, silent: hadCachedReportRef.current });
    }
    const timer = window.setInterval(() => {
      if (!isDocumentActive()) return;
      void safeLoad({ force: false, skipIfHidden: true, silent: true });
    }, OAUTH_USAGE_POLL_MS);

    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [enabled, path, pageVisible, load]);

  return {
    report,
    loading,
    refresh: () => load({ force: true, skipIfHidden: false, silent: false }),
  };
}

/** 各家额度面板的公共外壳：标题行（plan 去重 + 状态 + 刷新）+ 告警消息行 + 内容区。
 *  subtitle 传入卡片副标题：planName 已在其中展示过时不再重复（如 “xxx@gmail.com · plus”）。 */
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
  const planSuffix = planName && !subtitle?.includes(planName) ? ` · ${planName}` : '';
  // “可用”是正面信号，收进标题行不占竖向空间；其余（已用尽/降级/异常）单独一行展开，可完整阅读
  const warnMessage = stateMessage && stateMessage !== '可用' ? stateMessage : '';
  return (
    <div
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
export function UsageBucketRow({ label, percent, note, compact }: { label: string; percent: number; note?: string; compact?: boolean }) {
  const tone = claudeUsageFillTone(percent);
  const noteText = note || '';
  const displayNote = compact ? noteText.replace(/^重置\s*/, '') : noteText;
  return (
    <div
      className={`claude-usage-row ${tone}`}
      title={noteText ? `${label} ${percent.toFixed(0)}% · ${noteText}` : `${label} ${percent.toFixed(0)}%`}
    >
      <div className="claude-usage-head">
        <span>{label}</span>
        <span>{percent.toFixed(0)}%</span>
      </div>
      <div className="claude-usage-track">
        <div className={`claude-usage-fill ${tone}`} style={{ width: `${percent}%` }} />
      </div>
      <div className="claude-usage-reset">{displayNote}</div>
    </div>
  );
}

export function ClaudeOAuthUsagePanel({ providerId, connected, compact, subtitle }: { providerId: string; connected?: boolean; compact?: boolean; subtitle?: string }) {
  const { report, loading, refresh } = useOAuthUsageReport<ClaudeOAuthUsageReport>(
    Boolean(connected),
    `/__providers/${encodeURIComponent(providerId)}/claude-oauth/usage`,
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
              note={bucket?.resets_at ? `重置 ${formatClaudeUsageResetAt(bucket.resets_at)}` : ''}
              compact={compact}
            />
          ))}
        </div>
      )}
    </UsagePanelShell>
  );
}

export function ZhipuUsagePanel({ providerId, compact, subtitle }: { providerId: string; compact?: boolean; subtitle?: string }) {
  const { report, loading, refresh } = useOAuthUsageReport<ZhipuUsageReport>(
    true,
    `/__providers/${encodeURIComponent(providerId)}/zhipu/usage`,
  );

  // 卡片上：非编程套餐 / 拉取失败都不渲染，避免把「额度 UI」误当成 Provider/转发故障。
  if (compact) {
    if (!report?.available) return null;
  } else if (report?.unsupported) {
    return (
      <div className="claude-usage-panel" onClick={(event) => event.stopPropagation()}>
        <div className="hint-line">该密钥非智谱编程套餐，无额度面板（按量转发不受影响）。</div>
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
              note={bucket?.resets_at ? `重置 ${formatClaudeUsageResetAt(bucket.resets_at)}` : ''}
              compact={compact}
            />
          ))}
        </div>
      )}
    </UsagePanelShell>
  );
}

/** DeepSeek 账户余额面板。与 Zhipu 同为 api_key provider，复用同一套额度面板样式；
 *  但展示的是金额而非百分比进度条，故不渲染 usage-track。 */
export function DeepSeekBalancePanel({ providerId, compact, subtitle }: { providerId: string; compact?: boolean; subtitle?: string }) {
  const { report, loading, refresh } = useOAuthUsageReport<DeepSeekBalanceReport>(
    true,
    `/__providers/${encodeURIComponent(providerId)}/deepseek/usage`,
  );

  // 卡片上的紧凑版：拉取失败/不支持时不占位。
  if (compact) {
    if (!report?.available) return null;
  } else if (report?.unsupported) {
    return (
      <div className="claude-usage-panel" onClick={(event) => event.stopPropagation()}>
        <div className="hint-line">{report.error || '该地址不支持余额查询（可能是第三方中转），转发不受影响。'}</div>
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

export function CursorOAuthUsagePanel({ providerId, connected, compact, subtitle }: { providerId: string; connected?: boolean; compact?: boolean; subtitle?: string }) {
  const { report, loading, refresh } = useOAuthUsageReport<CursorOAuthUsageReport>(
    Boolean(connected),
    `/__providers/${encodeURIComponent(providerId)}/cursor-oauth/usage`,
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
              note={bucket.resetsAt ? `重置 ${formatClaudeUsageResetAt(bucket.resetsAt)}` : (bucket.detail || '')}
              compact={compact}
            />
          ))}
        </div>
      )}
    </UsagePanelShell>
  );
}

export function ChatGPTOAuthUsagePanel({ providerId, connected, compact, subtitle }: { providerId: string; connected?: boolean; compact?: boolean; subtitle?: string }) {
  const { report, loading, refresh } = useOAuthUsageReport<ChatGPTOAuthUsageReport>(
    Boolean(connected),
    `/__providers/${encodeURIComponent(providerId)}/chatgpt-oauth/usage`,
  );

  if (!connected) return null;

  const buckets = report?.buckets || [];

  return (
    <UsagePanelShell panelClass="chatgpt-usage-panel" compact={compact} subtitle={subtitle} title="ChatGPT Codex 额度" planName={report?.planName} stateMessage={report?.available ? report.message : undefined} loading={loading} fetchedAt={report?.fetchedAt} onRefresh={refresh}>
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
              note={bucket.resetsAt ? `重置 ${formatClaudeUsageResetAt(bucket.resetsAt)}` : (bucket.detail || '')}
              compact={compact}
            />
          ))}
        </div>
      )}
      {report?.available && report.resetCredits ? (
        <div className="chatgpt-reset-credits" title="官方发放的限流重置卡，用一张可重置 5 小时 / 7 天额度窗口">
          <div className="chatgpt-reset-credits-head">
            <span>重置卡</span>
            <span>
              可用 {report.resetCredits.availableCount} 张
              {report.resetCredits.credits && report.resetCredits.credits.length > 0 ? ` · 最早到期 ${formatClaudeUsageResetAt(resetCreditEarliestExpiry(report.resetCredits.credits))}` : ''}
            </span>
          </div>
          {(report.resetCredits.credits || []).length > 0 ? (
            <div className="chatgpt-reset-credits-list">
              {report.resetCredits.credits!.map((credit, index) => (
                <div className="chatgpt-reset-credit-item" key={credit.id || index} title={credit.title || ''}>
                  <span className="chatgpt-reset-credit-name">
                    重置卡 #{index + 1}{credit.title ? ` · ${credit.title}` : ''}
                  </span>
                  <span className="chatgpt-reset-credit-expiry">
                    {credit.expiresAt ? `有效期至 ${formatClaudeUsageResetAt(credit.expiresAt)}` : '有效期未知'}
                  </span>
                </div>
              ))}
            </div>
          ) : (
            <div className="chatgpt-reset-credit-item">
              <span className="chatgpt-reset-credit-name">暂无可查明细的可用重置卡</span>
            </div>
          )}
        </div>
      ) : null}
    </UsagePanelShell>
  );
}

export function ProviderCard({ active, selected, name, providerId, protocol, tone, url, keyMask, defaultModel, modelCount, usedCount, healthStatus, nextRetryAt, testing, chatTesting, readOnly, selectable, providerDisabled, onToggleEnabled, subtitle, isClaudeOAuth, claudeOAuthConnected, isCursorOAuth, cursorOAuthConnected, isChatGPTOAuth, chatgptOAuthConnected, isQoderPAT, qoderPATConnected, cursorBridge, ownerName, authorizedUserCount, onShowUsers, onToggleSelect, onClick, onTest, onChatTest, onConformance, onEdit, onClone, onDelete }: { active?: boolean; selected?: boolean; name: string; providerId: string; protocol: string; tone: BadgeTone; url: string; keyMask?: string; defaultModel?: string; modelCount?: number; usedCount: number; healthStatus: string; nextRetryAt?: string; testing: boolean; chatTesting?: boolean; readOnly?: boolean; selectable?: boolean; providerDisabled?: boolean; onToggleEnabled?: () => void; subtitle?: string; isClaudeOAuth?: boolean; claudeOAuthConnected?: boolean; isCursorOAuth?: boolean; cursorOAuthConnected?: boolean; isChatGPTOAuth?: boolean; chatgptOAuthConnected?: boolean; isQoderPAT?: boolean; qoderPATConnected?: boolean; cursorBridge?: CursorBridgeRuntime; ownerName?: string; authorizedUserCount?: number; onShowUsers?: () => void; onToggleSelect: () => void; onClick: () => void; onTest: () => void; onChatTest: () => void; onConformance?: () => void; onEdit: () => void; onClone: () => void; onDelete: () => void }) {
  const oauthConnected = isClaudeOAuth ? claudeOAuthConnected : isCursorOAuth ? cursorOAuthConnected : isChatGPTOAuth ? chatgptOAuthConnected : isQoderPAT ? qoderPATConnected : false;
  const showOAuthBadge = isClaudeOAuth || isCursorOAuth || isChatGPTOAuth || isQoderPAT;
  // Qoder 用的是 PAT 而非 OAuth，徽章文案跟着认证方式走，避免误导
  const oauthBadgeLabel = isQoderPAT ? 'PAT' : 'OAuth';
  const isUnavailable = healthStatus === 'unavailable';
  const now = useNowTick(isUnavailable && !!nextRetryAt);
  const retryLabel = isUnavailable ? retrySecondsLabel(nextRetryAt, now) : null;
  const bridgeHint = cursorBridge?.port
    ? ` · :${cursorBridge.port}${cursorBridge.message ? ` · ${cursorBridge.message}` : ''}`
    : (cursorBridge?.message ? ` · ${cursorBridge.message}` : '');
  const subtitleText = subtitle || providerId;
  return (
    <div className={`provider-card clickable ${active ? 'active' : ''} ${selected ? 'selected' : ''} ${providerDisabled ? 'provider-disabled' : ''}`} onClick={onClick} role="button" tabIndex={0} onKeyDown={(event) => { if (event.key === 'Enter') onClick(); }}>
      <div className="provider-head">
        <div className="provider-title-block">
          {selectable ? (
            <label className="provider-select checkbox-field" onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()}>
              <input type="checkbox" checked={!!selected} onChange={onToggleSelect} aria-label={`选择 ${name}`} />
              <span className="provider-name">{name}</span>
            </label>
          ) : (
            <div className="provider-name">{name}</div>
          )}
          {/* 副标题只放账号/ID：协议已由徽章表达，不再重复拼接 */}
          <div className="provider-subtitle" title={subtitleText}>{subtitleText}</div>
        </div>
        <div className="provider-badges">
          {providerDisabled ? <Badge tone="red">已禁用</Badge> : null}
          <Badge tone={tone}>{protocol}</Badge>
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
      </div>
      <div className="provider-meta">
        {keyMask ? (
          <>
            <div className="provider-meta-row">
              <span className="provider-meta-label">接入地址</span>
              <span className="provider-meta-value" title={url}>{url}</span>
            </div>
            <div className="provider-meta-row">
              <span className="provider-meta-label">密钥</span>
              <span className="provider-meta-value" title={keyMask}>{keyMask}</span>
            </div>
            <div className="provider-meta-row">
              <span className="provider-meta-label">默认模型</span>
              <span className="provider-meta-value" title={defaultModel || undefined}>{defaultModel || '未设置'}</span>
            </div>
            <div className="provider-meta-row">
              <span className="provider-meta-label">可用模型</span>
              <span className="provider-meta-value" title={modelCount != null && modelCount > 0 ? undefined : '点击下方「获取模型」拉取可用模型列表'}>
                {modelCount != null && modelCount > 0 ? `${modelCount} 个` : '未获取 · 可点下方「获取模型」拉取'}
              </span>
            </div>
          </>
        ) : (
          <span className="provider-meta-line" title={url}>{url}{isCursorOAuth && cursorBridge?.port ? ` · 127.0.0.1:${cursorBridge.port}` : ''}</span>
        )}
      </div>
      {isClaudeOAuth && claudeOAuthConnected && !providerDisabled ? <ClaudeOAuthUsagePanel providerId={providerId} connected={claudeOAuthConnected} compact subtitle={subtitle} /> : null}
      {isCursorOAuth && cursorOAuthConnected && !providerDisabled ? <CursorOAuthUsagePanel providerId={providerId} connected={cursorOAuthConnected} compact subtitle={subtitle} /> : null}
      {isChatGPTOAuth && chatgptOAuthConnected && !providerDisabled ? <ChatGPTOAuthUsagePanel providerId={providerId} connected={chatgptOAuthConnected} compact subtitle={subtitle} /> : null}
      {!providerDisabled && !isClaudeOAuth && !isCursorOAuth && !isChatGPTOAuth && !isQoderPAT && /(?:bigmodel\.cn|z\.ai)/i.test(url) ? <ZhipuUsagePanel providerId={providerId} compact subtitle={subtitle} /> : null}
      {!providerDisabled && !isClaudeOAuth && !isCursorOAuth && !isChatGPTOAuth && !isQoderPAT && /deepseek\.com/i.test(url) ? <DeepSeekBalancePanel providerId={providerId} compact subtitle={subtitle} /> : null}
      <div className="provider-foot" onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()}>
        <div className="provider-foot-meta">
          <span><b>{usedCount}</b> 个 API Key</span>
          {ownerName ? <span>创建者 <b>{ownerName}</b></span> : null}
          {onShowUsers && authorizedUserCount != null ? (
            <button
              type="button"
              className="provider-foot-link"
              title="查看/管理有权限使用该 Provider 的用户（仅管理员）"
              onClick={() => onShowUsers()}
            >
              <b>{authorizedUserCount}</b> 个用户
            </button>
          ) : null}
        </div>
        {!readOnly ? (
          <div className="provider-actions">
            <button className="icon-btn" disabled={testing} onClick={onTest} title="从 Provider 接口获取可用模型">{testing ? '获取中' : '获取模型'}</button>
            <button className="icon-btn" disabled={!!chatTesting} onClick={onChatTest} title="直连上游对话接口测试">{chatTesting ? '测试中' : '对话测试'}</button>
            <button className="icon-btn" onClick={onEdit} title="编辑 Provider">编辑</button>
            <MoreMenu
              label="更多操作"
              items={[
                { label: '克隆', onClick: onClone },
                ...(onToggleEnabled ? [{ label: providerDisabled ? '启用' : '禁用', onClick: onToggleEnabled }] : []),
                { label: '删除', danger: true, onClick: onDelete },
              ]}
            />
          </div>
        ) : (
          // 只读（管理员授权给普通用户的 Provider）也允许获取模型与对话测试；
          // 编辑/删除等管理操作保持隐藏。后端 requireProviderOwnerForUser 会对
          // 授权用户放行 test/chat-test（authorized users 视同可测试）。
          <div className="provider-actions">
            <button className="icon-btn" disabled={testing} onClick={onTest} title="从 Provider 接口获取可用模型">{testing ? '获取中' : '获取模型'}</button>
            <button className="icon-btn" disabled={!!chatTesting} onClick={onChatTest} title="直连上游对话接口测试">{chatTesting ? '测试中' : '对话测试'}</button>
          </div>
        )}
      </div>
    </div>
  );
}
