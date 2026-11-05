// 应用入口：App 组件 + 启动引导。通用类型/工具/组件已拆分到 types.ts、lib.ts、components/。
import React, {useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState} from 'react';
import {createRoot} from 'react-dom/client';
import './styles.css';
import './rbac.css';
import {ApiKeyEditableField, ApiKeyFixedModelField, ApiKeyModelMappingControl} from './components/apikeys';
import {LogTimingWaterfall, UsageRangeCalendar} from './components/charts';
import {
    ApiKeyBatchBar,
    ApiKeyDetailWorkspace,
    ApiKeyDrawer,
    ApiKeyFleetPane,
    ApiKeyFullTable,
    ApiKeysTopBar,
    type ApiKeyUsageSource,
    type ApiKeyViewMode,
    buildApiKeyUsageMap
} from './components/api_key_console';
import {UsagePage} from './components/usage';
import {MachineStatusPage} from './components/machine_status';
import {runCloudflareAuthPoll} from './pollers';
import {PwaInstallButton, PwaInstallPrompt} from './components/pwa_install';
import {registerPwaServiceWorker, startPwaInstallCapture} from './pwa_install_store';
import {providerRankOrder, rankProviderIDs} from './usage_poll';
import {type MergedModel, mergeModelsAcrossAccounts} from './model_merge';
import {
    activePublicBaseURL,
    activeUIPublicBaseURL,
    aggregateScheduleAttempts,
    antiAutofillProps,
    API_BASE,
    API_KEY_VIEW_STORAGE_KEY,
    API_KEYS_PAGE_SIZE,
    apiKeyReferencesProvider,
    authRejectionMessage,
    BACKEND_FAIL_STREAK_LIMIT,
    BACKEND_POLL_MS_LOCAL,
    BACKEND_POLL_MS_REMOTE,
    browserAccessOrigin,
    buildApiKeyPatchBody,
    buildCurlCommand,
    buildProviderChatCurl,
    buildRouteTestCurl,
    buildSelfRegistrationPrompt,
    can,
    chatgptAccountLabelFriendly,
    chatTestToastText,
    chatTestVerdictFromBody,
    clearUICache,
    clientGatewayRoot,
    compactRequestAdapterJSON,
    composeCustomDomain,
    copyToClipboard,
    defaultProviderChatTestOptions,
    defaultPublicAccess,
    defaultThinkingValueForField,
    deriveUIDomainFromAPI,
    endpointURL,
    explainHttpStatus,
    fallbackState,
    fetchWithTimeout,
    findRouteForBinding,
    fixedOutputLabels,
    formatBytes,
    formatCostMicros,
    formatDateInZone,
    formatJsonDisplay,
    formatLocalISODate,
    formatTrafficLogDetail,
    formatTrafficLogJSON,
    getApiKeyBinding,
    httpStatusLabel,
    isFollowingTodayRange,
    isFreeModel,
    isQoderConnectKind,
    isRemoteOrigin,
    isSubscriptionConnectKind,
    isThinkingModel,
    isTraeConnectKind,
    isTrafficLogError,
    isVisionModel,
    isWorkBuddyConnectKind,
    isZCodeConnectKind,
    loadBootSession,
    localDateToRFC3339,
    localGatewayRoot,
    LOG_OWNER_FILTER_ADMIN,
    logLevelValues,
    LOGS_PAGE_SIZE,
    mapFromTrafficRanks,
    modelAlias,
    navIDFromPath,
    navItems,
    navPathForID,
    navSections,
    navVisibleFor,
    normalizeGatewayState,
    normalizeRequestStats,
    parseSSEStreamContent,
    pickCustomDomainRoot,
    previewRequestAdapterCurl,
    promoteApiKeyProvider,
    protocolFromLabel,
    protocolIcon,
    protocolLabel,
    protocolTone,
    PROVIDER_CONNECT_FILTERS,
    providerAccountLabel,
    providerAuthTypeParam,
    providerConnectKind,
    providerConnectLabel,
    providerDisplayLabel,
    providerOptionLabel,
    publicAccessMetricValue,
    publicAccessStatusLabel,
    publicAccessURL,
    publicStatusTone,
    qoderDefaultBaseURL,
    readConnectResponse,
    readStoredDecisionPanelOpen,
    readStoredNavCollapsed,
    readStoredProviderDensity,
    readStoredSidebarCollapsed,
    readUICache,
    reportIfLooksLikeAutofill,
    REQUEST_ADAPTER_PRESETS,
    resolveProviderChatURL,
    resolveProviderTestModel,
    routeGatewayTestURL,
    selfRegisterPlaceholderBaseURL,
    shortenAccountLabel,
    splitCustomDomain,
    statusTone,
    thinkingDepthSelectOptions,
    thinkingPresetsForProtocol,
    trafficLogKeyLabel,
    trafficLogProviderLabel,
    trafficLogServing,
    trafficRanksEqual,
    trafficRanksFromMap,
    uiCacheScope,
    writeStoredDecisionPanelOpen,
    writeStoredNavCollapsed,
    writeStoredProviderDensity,
    writeStoredSidebarCollapsed,
    writeUICache
} from './lib';
import {
    ChatGPTOAuthUsagePanel,
    ClaudeOAuthUsagePanel,
    CursorOAuthUsagePanel,
    DeepSeekBalancePanel,
    formatClaudeUsageResetAt,
    isPageVisible,
    ProviderCard,
    WorkBuddyUsagePanel,
    ZhipuUsagePanel
} from './components/panels';
import {SubscriptionConnect} from './components/subscription';
import {providerIcon, ProviderLabel} from './components/provider_label';
import {MonitoringPage} from './monitoring/MonitoringPage';
import {groupProvidersByBrand, providerSelectOptions} from './components/provider_select';
import {ChatTestDialog, type ChatTestTab} from './components/chat_test';
import {QuotaKeeperPanel} from './components/quota_keeper';
import {ReplayPanel, ScheduleTracePanel, trafficLogRowKey, TrafficLogTable} from './components/traffic_log';
import {IconSelect} from './components/selects';
import {ProviderCatalogDialog} from './components/provider_catalog';
import {PROVIDER_CONNECT_CHOICES, providerConnectIcon, resolveProviderIcon} from './provider_presets';
import {
    AdminAuthStatus,
    APIKey,
    AppLogEntry,
    ChatTestContext,
    CloudflareZoneOption,
    ConsoleUser,
    GatewayState,
    KeyProfile,
    LegacyRequestStatsSnapshot,
    LogEntry,
    LogPage,
    Model,
    NavItemID,
    Protocol,
    Provider,
    ProviderAuthPreview,
    ProviderAuthType,
    ProviderCacheTestResult,
    ProviderChatTestOptions,
    ProviderConnectKind,
    ProviderPreset,
    ProvidersImportResult,
    ProviderTestResult,
    ProviderThinkingTestResult,
    PublicAccessSettings,
    RequestAdapter,
    RequestStatsSnapshot,
    Route,
    RouteTestResult,
    RTKStatsResponse,
    ThemeMode,
    TrafficRankCache
} from './types';
import {
    applyThemeMode,
    Badge,
    BrandIcon,
    CopyButton,
    Field,
    InfoTip,
    Modal,
    ModelPriceTags,
    NavIcon,
    readStoredTheme,
    resolveTheme,
    SelectField,
    Switch,
    THEME_STORAGE_KEY,
    ThemeSwitch,
    URLRow
} from './components/ui';
import {TerminalAuthGate} from './components/terminal_auth';
import {RolesPage, useRoles} from './components/roles_page';
import {ProfileModal, UsersPage} from './components/users_page';
import {
    CURRENT_APP_VERSION,
    dismissUpgrade,
    fetchLatestAppVersion,
    isNewerVersion,
    reloadToVersion
} from './app_version';

// 「全部 Provider」筛选项的哨兵值：日志筛选的 providerId 为空串，下拉里得有个能选中的项。
const ALL_PROVIDERS_VALUE = '__all_providers__';

// 日志第 1 页走增量追加（新行插到表头），所以单独用更短的节奏，不受面板轮询的 15 秒影响。
const LOGS_LIVE_POLL_MS = 5000;

/** 请求量排名缓存的账号作用域：换用户就换一份，不把上一个用户的排名带过来。 */
function trafficRankScopeOf(auth: AdminAuthStatus | null): string {
  return uiCacheScope(auth);
}

// 旧版「普通用户」：没有权限列表的会话按旧白名单兜底（navVisibleFor 内部处理）。
function isLegacyUserAuth(auth: AdminAuthStatus | null | undefined): boolean {
  return auth?.role === 'user';
}

// 登录落地页 / 无权限兜底页：侧栏顺序里第一个有权限的页面；一个都没有时退回 API 密钥。
function firstVisibleNavID(auth: AdminAuthStatus | null | undefined): NavItemID {
  const legacyUser = isLegacyUserAuth(auth);
  for (const item of navItems) {
    if (navVisibleFor(auth?.permissions, item.id, legacyUser)) return item.id;
  }
  return 'api-keys';
}

// 侧栏账号卡：有头像用头像，没有就退回用户名首字母；副标题显示角色与数据更新时间。
function SidebarUserCard({ auth, busy, dataFetchedAt, onLogout, onEditProfile }: {
  auth: AdminAuthStatus;
  busy: boolean;
  dataFetchedAt: Date | null;
  onLogout: () => void;
  onEditProfile: () => void;
}) {
  const name = auth.displayName || auth.username || '';
  return (
    <div className="user-card">
      {auth.avatarUrl ? (
        <img className="user-avatar user-avatar-img" src={auth.avatarUrl} alt="" referrerPolicy="no-referrer" />
      ) : (
        <div className="user-avatar">{name.slice(0, 1).toUpperCase()}</div>
      )}
      <div className="user-meta">
        <b title={name}>{name}</b>
        <span title={dataFetchedAt ? `页面数据最近一次成功拉取的时间（约每 5 秒自动刷新）` : undefined}>
          {dataFetchedAt ? <i className="live-dot" aria-hidden="true" /> : null}
          {auth.roleName || (auth.role === 'user' ? '普通用户' : '管理员')}
          {dataFetchedAt ? ` · 更新于 ${dataFetchedAt.toLocaleTimeString()}` : ''}
        </span>
      </div>
      <button
        className="icon-btn"
        type="button"
        onClick={onEditProfile}
        title="个人资料（显示名称与头像）"
        aria-label="个人资料"
      >
        <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <circle cx="12" cy="8.5" r="3.5" />
          <path d="M5 20a7 7 0 0 1 14 0" />
        </svg>
      </button>
      {auth.requireAuth ? (
        <button
          className="icon-btn"
          type="button"
          disabled={busy}
          onClick={onLogout}
          title="退出登录"
          aria-label="退出登录"
        >
          <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="M15 4h3.5A1.5 1.5 0 0 1 20 5.5v13a1.5 1.5 0 0 1-1.5 1.5H15" />
            <path d="M10 8l-4 4 4 4M6 12h10" />
          </svg>
        </button>
      ) : null}
    </div>
  );
}

function App() {
  const [activeNav, setActiveNav] = useState<NavItemID>(() => navIDFromPath(window.location.pathname));
  const [themeMode, setThemeMode] = useState<ThemeMode>(() => readStoredTheme());
  const [resolvedTheme, setResolvedTheme] = useState<'light' | 'dark'>(() => resolveTheme(readStoredTheme()));
  const [sidebarCollapsed, setSidebarCollapsed] = useState<boolean>(() => readStoredSidebarCollapsed());
  // 手机档（≤440）导航条里的「⋯」面板：主题切换与账号原来在侧栏页脚，收进顶部单行栏后放这里。
  const [sidebarMoreOpen, setSidebarMoreOpen] = useState(false);
  useEffect(() => {
    if (!sidebarMoreOpen) return;
    const onDown = (event: MouseEvent) => {
      const target = event.target as HTMLElement | null;
      if (target && typeof target.closest === 'function' && target.closest('.sidebar-more')) return;
      setSidebarMoreOpen(false);
    };
    const onKey = (event: KeyboardEvent) => { if (event.key === 'Escape') setSidebarMoreOpen(false); };
    document.addEventListener('mousedown', onDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [sidebarMoreOpen]);
  useEffect(() => { writeStoredSidebarCollapsed(sidebarCollapsed); }, [sidebarCollapsed]);
  // 侧栏分组的展开/收起：只按当前用户持久化（换账号不串），搜索词不落盘。
  const [navQuery, setNavQuery] = useState('');
  const [showScrollTop, setShowScrollTop] = useState(false);
  useEffect(() => {
    const onScroll = () => setShowScrollTop(window.scrollY > 360);
    onScroll();
    window.addEventListener('scroll', onScroll, { passive: true });
    return () => window.removeEventListener('scroll', onScroll);
  }, []);
  const [bootSession] = useState(loadBootSession);
  // 网关自己的时区（/__state 的 serverTimeZone）：「今日」、日界都按它算，和每个密钥的
  // 「今日 N 次」同一天。首次渲染还不知道时退回浏览器时区。
  const [serverTimeZone, setServerTimeZone] = useState(() => (bootSession.state?.serverTimeZone || ''));
  const [state, setState] = useState<GatewayState>(() => bootSession.state || fallbackState);
  // 输入 Provider 列表的顺序：只在进入这一页的首次绘制、或点了「获取模型」时按
  // 近 3 日请求量重排。日志轮询照常更新计数，但顺序冻结——请求量每分钟都可能
  // 交叉，跟着轮询重排会让卡片在光标底下换位，点 ▸ 编辑 落到另一张卡上。
  const [providerRankSnapshot, setProviderRankSnapshot] = useState<{ scope: string; order: string[] | null }>(() => ({
    scope: trafficRankScopeOf(bootSession.auth),
    order: null,
  }));
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const [logsTotal, setLogsTotal] = useState(0);
  const [logsPage, setLogsPage] = useState(1);
  const [logsLoading, setLogsLoading] = useState(false);
  const [logsFetchedOnce, setLogsFetchedOnce] = useState(false);
  // 刚落表的新行：给它们挂进场动画，动画放完就清空，避免后续重渲染再演一遍。
  const [logsFreshKeys, setLogsFreshKeys] = useState<Set<string>>(() => new Set());
  // 「调度决策」面板默认收起（它只是日志表的辅助说明，不值得常驻几十像素）。
  const [decisionPanelOpen, setDecisionPanelOpen] = useState<boolean>(() => readStoredDecisionPanelOpen());
  const [logsStatusFilter, setLogsStatusFilter] = useState<'all' | '2xx' | '4xx' | '5xx'>('all');
  const [logsApiKeyName, setLogsApiKeyName] = useState('');
  const [logsUsageTarget, setLogsUsageTarget] = useState<{ type: 'model' | 'apiKey'; value: string; label: string } | null>(null);
  const [logsProviderFilter, setLogsProviderFilter] = useState('');
  // 日志筛选的 Provider 下拉：默认一项「全部 Provider」，其后的每一项都要能认人
  // （图标 + 账号），否则一串同名 WorkBuddy 根本选不中想要的那个。
  const trafficProviderFilterOptions = useMemo(
    () => [{value: ALL_PROVIDERS_VALUE, label: '全部 Provider'}, ...providerSelectOptions(state.providers || [], {withProtocol: true})],
    [state.providers],
  );
  // 空字符串 = 全部；LOG_OWNER_FILTER_ADMIN = 管理员（无所属用户的旧密钥）；
  // 其余为具体用户 id。仅管理员可见/可用，与后端 ownedKeyIDsForOwnerFilter 对应。
  const [logsOwnerFilter, setLogsOwnerFilter] = useState('');
  const [logsFrom, setLogsFrom] = useState(() => formatLocalISODate(new Date()));
  const [logsTo, setLogsTo] = useState(() => formatLocalISODate(new Date()));
  // 分页快照：离开第 1 页时冻结当时最新日志的 time+id 作为上界，之后翻页只在
  // 该快照内做 offset。新日志不再挤动窗口。回到第 1 页或换筛选时清空。
  // 注意：绝不能把 logsPage 放进「拉日志」的 effect 依赖里再调 refreshLogs——
  // 否则翻页 setLogsPage 会再次触发拉取，再叠上第 1 页 5s 轮询的竞态，就会出现
  // 「点下一页刷十几次 / 页码来回跳」。
  const logsSnapshotBeforeIdRef = useRef(0);
  const logsSnapshotBeforeTimeRef = useRef('');
  const logsPageRef = useRef(1);
  const logsFetchGenRef = useRef(0);
  // 当前表里的行（增量轮询按它去重）与「往下翻时锚住的那一行」。
  const logsItemsRef = useRef<LogEntry[]>([]);
  const logsAnchorRef = useRef<{ key: string; top: number } | null>(null);
  const logsFreshTimerRef = useRef(0);
  const logsTotalRef = useRef(0);
  // 「跟随今天」：默认/点「今天」/点选当日时为 true。跨午夜后侧边栏再进本页会滚到新的今天，
  // 不必整页刷新。用户选了历史单日、多日区间或清除日期后为 false，不再自动改日期。
  const logsFollowTodayRef = useRef(true);
  const usageFollowTodayRef = useRef(true);
  useEffect(() => {
    logsPageRef.current = logsPage;
  }, [logsPage]);
  const [requestLogRetentionDays, setRequestLogRetentionDays] = useState(7);
  // 成功请求是否落正文：默认关（正文是每请求写盘量的主要来源）。
  const [log2xxBodies, setLog2xxBodies] = useState(false);
  // 用量统计攒批阈值:改动需重启网关才生效(worker 启动时读一次)。
  const [usageBatchMaxSize, setUsageBatchMaxSize] = useState(500);
  const [usageBatchMaxWaitSeconds, setUsageBatchMaxWaitSeconds] = useState(60);
  const [usageFrom, setUsageFrom] = useState(() => formatLocalISODate(new Date()));
  const [usageTo, setUsageTo] = useState(() => formatLocalISODate(new Date()));
  // 背景轮询拿到的是首次渲染的闭包，必须经 ref 读取最新区间，
  // 否则用户应用自定义区间后会立刻被「今天」的数据覆盖。
  const usageRangeRef = useRef({ from: usageFrom, to: usageTo });
  useEffect(() => {
    usageRangeRef.current = { from: usageFrom, to: usageTo };
  }, [usageFrom, usageTo]);
  // 最近一次成功拉取数据的本地时间，随自动/手动刷新更新
  const [dataFetchedAt, setDataFetchedAt] = useState<Date | null>(null);
  const authStatusRef = useRef<AdminAuthStatus | null>(null);
  const [trafficLogDetail, setTrafficLogDetail] = useState<LogEntry | null>(null);
  const [trafficLogDetailLoading, setTrafficLogDetailLoading] = useState(false);
  const [trafficLogDetailTab, setTrafficLogDetailTab] = useState<'apm' | 'request' | 'response' | 'metadata' | 'curl'>('apm');
  const [trafficLogRawReqView, setTrafficLogRawReqView] = useState(false);
  const [trafficLogRawRespView, setTrafficLogRawRespView] = useState(false);
  const [trafficLogSSEParsedView, setTrafficLogSSEParsedView] = useState(true);
  const [logsKeyword, setLogsKeyword] = useState('');
  // 会话视图：按 sessionId 过滤请求日志（后端 json_extract 查询）。
  const [logsSessionFilter, setLogsSessionFilter] = useState('');
  // 日志页筛选条的两个弹层/折叠状态：日期日历改到弹层里，密钥/Provider/用户
  // 筛选收进「高级」折叠，主条只占两行，把纵向空间还给日志本身。
  // 每页条数跟随可用高度：筛选区收窄后空出的高度要变成「多看到几条日志」，
  // 而不是一片空白。行高与表头在渲染后量（固定 10 条会和任何布局变化脱节）。
  const [logsPageSize, setLogsPageSize] = useState(LOGS_PAGE_SIZE);
  const pageSizeRef = useRef(LOGS_PAGE_SIZE);
  // 量页容量的函数：表格节点挂载时由 ref 回调触发（见 .log-table 的 ref）。
  const measureLogsPageSizeRef = useRef<(() => void) | null>(null);
  const logsTableRef = useRef<HTMLDivElement | null>(null);
  const [logsDateOpen, setLogsDateOpen] = useState(false);
  const [logsAdvancedOpen, setLogsAdvancedOpen] = useState(false);
  const logsDateRef = useRef<HTMLDivElement | null>(null);
  // 日期按钮上显示当前区间：同一天就只写那一天，区间写两端，未筛写「全部日期」。
  const logsDateLabel = !logsFrom && !logsTo
    ? '全部日期'
    : logsFrom === logsTo || !logsTo
      ? (logsFrom || logsTo)
      : `${logsFrom} ~ ${logsTo}`;
  const displayedLogs = useMemo(() => {
    if (!logsKeyword.trim()) return logs;
    const kw = logsKeyword.trim().toLowerCase();
    return logs.filter((log) =>
      (log.model && log.model.toLowerCase().includes(kw)) ||
      (log.path && log.path.toLowerCase().includes(kw)) ||
      (log.apiKeyName && log.apiKeyName.toLowerCase().includes(kw)) ||
      (log.clientIp && log.clientIp.toLowerCase().includes(kw)) ||
      (log.errorDescription && log.errorDescription.toLowerCase().includes(kw)) ||
      (log.userName && log.userName.toLowerCase().includes(kw)) ||
      (log.accessSource && log.accessSource.toLowerCase().includes(kw)) ||
      (log.providerId && log.providerId.toLowerCase().includes(kw))
    );
  }, [logs, logsKeyword]);
  // 当前页日志里的调度尝试聚合：谁被尝试、谁真服务、谁被跳过/失败。
  const decisionAggregate = useMemo(() => aggregateScheduleAttempts(displayedLogs), [displayedLogs]);
  const [requestStats, setRequestStats] = useState<RequestStatsSnapshot | null>(null);
  const usageRequestVersion = useRef(0);
  const [usageLoading, setUsageLoading] = useState(false);
  const [usageError, setUsageError] = useState('');
  const [rtkStats, setRtkStats] = useState<RTKStatsResponse | null>(null);
  const rtkRequestVersion = useRef(0);
  // RTK 统计有自己独立的区间（7/30/90/全部），不跟随用量页的日历区间。
  const [rtkStatsDays, setRtkStatsDays] = useState(30);
  const [appLogs, setAppLogs] = useState<AppLogEntry[]>([]);
  const [logLevel, setLogLevel] = useState('info');
  const [selectedRouteID, setSelectedRouteID] = useState('');
  const [selectedProviderID, setSelectedProviderID] = useState('');
  const [selectedExportProviderIDs, setSelectedExportProviderIDs] = useState<string[]>([]);
  const providerImportInputRef = useRef<HTMLInputElement | null>(null);
  const [selectedOutputProtocol, setSelectedOutputProtocol] = useState<Protocol>('openai_chat');
  const [publicDraft, setPublicDraft] = useState<PublicAccessSettings>(defaultPublicAccess);
  const [customDomainPrefix, setCustomDomainPrefix] = useState('gateway');
  const [uiDomainPrefix, setUIDomainPrefix] = useState('console');
  const [customDomainRoot, setCustomDomainRoot] = useState('');
  const [cloudflareZones, setCloudflareZones] = useState<CloudflareZoneOption[]>([]);
  const [customTunnelToken, setCustomTunnelToken] = useState('');
  const [cloudflareAuthorized, setCloudflareAuthorized] = useState(false);
  const [cloudflareAuthPending, setCloudflareAuthPending] = useState(false);
  const [showManualToken, setShowManualToken] = useState(false);
  // 公网开关的机器控制令牌（bearer）：只能开/关公网访问，供脚本或 LLM 调用。
  // rawToken 只在生成那一刻有值，刷新页面后只剩掩码预览。
  const [publicControlTokenConfigured, setPublicControlTokenConfigured] = useState(false);
  const [publicControlTokenPreview, setPublicControlTokenPreview] = useState('');
  const [publicControlRawToken, setPublicControlRawToken] = useState('');
  const [publicControlTokenBusy, setPublicControlTokenBusy] = useState(false);
  const cloudflarePollRef = useRef<number | null>(null);
  // 授权轮询的驱动（见 pollers.ts）：停轮询时要连它一起停，否则正在飞的那次检查
  // 回来后还会再装一个定时器。
  let cloudflarePoll: {stop: () => void} | null = null;
  // 5s __state 轮询会不断换新 publicAccess 对象（含 tunnel 状态）。用指纹跳过
  // 「仅运行时变化」的同步，避免正在编辑的子域名前缀被已保存域名盖回去。
  const publicAccessFormSyncRef = useRef('');
  const [tunnelBusy, setTunnelBusy] = useState(false);
  // null = 尚未探测，避免刷新瞬间误闪「后端未连接」
  const [backendConnected, setBackendConnected] = useState<boolean | null>(null);
  // 后端当前版本：与本页构建版本不同即说明发布了新版本，提示升级（见 app_version.ts）。
  const [latestAppVersion, setLatestAppVersion] = useState<string>('');
  const [upgradeDismissed, setUpgradeDismissed] = useState<boolean>(false);
  const upgradeAvailable = isNewerVersion(latestAppVersion, CURRENT_APP_VERSION) && !upgradeDismissed;
  useEffect(() => {
    if (!CURRENT_APP_VERSION) {
      // 开发构建没有版本号：不轮询，也不提示。
      return;
    }
    let cancelled = false;
    const check = () => {
      void (async () => {
        try {
          const latest = await fetchLatestAppVersion();
          if (cancelled || !latest) return;
          setLatestAppVersion(latest);
        } catch {
          // 后端暂时不可达不代表有新版本：保持上次的结论，等下一轮。
        }
      })();
    };
    check();
    const timer = window.setInterval(check, isRemoteOrigin() ? BACKEND_POLL_MS_REMOTE : BACKEND_POLL_MS_LOCAL);
    const onVisible = () => { if (isPageVisible()) check(); };
    document.addEventListener('visibilitychange', onVisible);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
      document.removeEventListener('visibilitychange', onVisible);
    };
  }, []);
  // 连续失败计数。公网走 Cloudflare 隧道时单次 TLS 握手实测约 1.5s，偶发抖动
  // 在所难免；单次失败就翻红会让 UI 频繁误报「重连中」，故需连续失败若干次
  // 才判定断线。ref 而非 state：只用于判定、不驱动渲染。
  const backendFailStreakRef = useRef(0);
  const [backendReconnecting, setBackendReconnecting] = useState(false);
  const [authStatus, setAuthStatus] = useState<AdminAuthStatus | null>(() => bootSession.auth);
  // 权限点：管理员为 ['*']；旧会话没有列表时 navVisibleFor 回退到旧白名单。
  const permissions = authStatus?.permissions;
  const legacyUser = isLegacyUserAuth(authStatus);
  // 侧栏分组折叠状态按用户 id 分开记忆；切换账号时重新读取该用户的偏好。
  const navCollapseUserKey = authStatus?.userId || authStatus?.username || 'anonymous';
  const [navCollapsedGroups, setNavCollapsedGroups] = useState<string[]>(() => readStoredNavCollapsed(navCollapseUserKey));
  useEffect(() => { setNavCollapsedGroups(readStoredNavCollapsed(navCollapseUserKey)); }, [navCollapseUserKey]);
  function toggleNavGroup(sectionID: string) {
    setNavCollapsedGroups((current) => {
      const next = current.includes(sectionID) ? current.filter((id) => id !== sectionID) : [...current, sectionID];
      writeStoredNavCollapsed(navCollapseUserKey, next);
      return next;
    });
  }
  useEffect(() => {
    authStatusRef.current = authStatus;
  }, [authStatus]);
  // 有上次登录缓存时直接视为已检查，避免闪「正在检查登录状态」
  const [authChecked, setAuthChecked] = useState(() => Boolean(bootSession.auth));
  // 首屏 __state 未返回前不渲染空列表，避免「暂无密钥」闪一下
  const [stateHydrated, setStateHydrated] = useState(() => Boolean(bootSession.state));
  const [authPassword, setAuthPassword] = useState('');
  const [authUsername, setAuthUsername] = useState('');
  const [authPasswordConfirm, setAuthPasswordConfirm] = useState('');
  const [authBusy, setAuthBusy] = useState(false);
  const [authTrustDevice, setAuthTrustDevice] = useState(() => {
    try {
      return localStorage.getItem('gateway_trust_device') !== 'false';
    } catch {
      return true;
    }
  });
  const [authError, setAuthError] = useState('');

  // 用户管理 / 角色管理共用：账号列表由 UsersPage 自己拉取，这里只保留 owner 名字映射。
  const [consoleUsers, setConsoleUsers] = useState<ConsoleUser[]>([]);
  // RBAC：角色列表 + 下拉选项 + 权限目录，角色页与用户编辑弹窗共用。
  // 没有 users.manage 的账号既进不了这两处，也不该去拉 /__roles 吃 403。
  const rolesModel = useRoles(Boolean(permissions && (permissions.includes('*') || permissions.includes('users.manage'))));
  const [profileModalOpen, setProfileModalOpen] = useState(false);
  const [adminCurrentPassword, setAdminCurrentPassword] = useState('');
  const [adminNewPassword, setAdminNewPassword] = useState('');
  const [adminNewPasswordConfirm, setAdminNewPasswordConfirm] = useState('');
  const [adminPasswordBusy, setAdminPasswordBusy] = useState(false);
  const [chatTestOpen, setChatTestOpen] = useState(false);
  const [chatTestContext, setChatTestContext] = useState<ChatTestContext | null>(null);
  const [chatTestModel, setChatTestModel] = useState('');
  const [chatTestMessage, setChatTestMessage] = useState('ping from UI');
  const [chatTestResult, setChatTestResult] = useState<RouteTestResult | null>(null);
  // 按「kind:id」隔离进行中的对话测试，避免一个 Provider 测试中把其它卡片按钮一起弄灰。
  const [chatTestingKeys, setChatTestingKeys] = useState<string[]>([]);
  const chatTestContextRef = useRef<ChatTestContext | null>(null);
  const [providerChatOptions, setProviderChatOptions] = useState<ProviderChatTestOptions>(defaultProviderChatTestOptions);
  const [providerAuthPreview, setProviderAuthPreview] = useState<ProviderAuthPreview | null>(null);
  const [cacheTestResult, setCacheTestResult] = useState<ProviderCacheTestResult | null>(null);

  const [thinkingTestResult, setThinkingTestResult] = useState<ProviderThinkingTestResult | null>(null);

  const [providerModelsOpen, setProviderModelsOpen] = useState(false);
  const [providerModelsLoading, setProviderModelsLoading] = useState(false);
  const [providerModelsResult, setProviderModelsResult] = useState<ProviderTestResult | null>(null);
  const [providerModelsName, setProviderModelsName] = useState('');
  const [providerModelsID, setProviderModelsID] = useState('');
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  // 导入单独一个 busy 标记：设置 saving 会把右侧面板顶部四个按钮全禁用，`导入` 按钮被禁后
  // chip 从鼠标下方消失，浏览器随即丢掉按钮的 hover，一次性结果气泡刚出现就跟着消失。
  const [importingProviders, setImportingProviders] = useState(false);
  const [testingProviderID, setTestingProviderID] = useState('');
  const [providerModalOpen, setProviderModalOpen] = useState(false);
  const [editingProviderID, setEditingProviderID] = useState('');
  const [routeModalOpen, setRouteModalOpen] = useState(false);
  const [editingRouteID, setEditingRouteID] = useState('');
  const [apiKeyModalOpen, setApiKeyModalOpen] = useState(false);
  const [selectedApiKeyID, setSelectedApiKeyID] = useState('');
  const [checkedApiKeyIDs, setCheckedApiKeyIDs] = useState<string[]>([]);
  const apiKeyCheckAnchorRef = useRef<number | null>(null);
  const [apiKeyKeyword, setApiKeyKeyword] = useState('');
  // 多维筛选：所属用户（仅管理员可见）、输入 Provider、输出协议；空数组 = 全部。
  const [apiKeyOwnerFilter, setApiKeyOwnerFilter] = useState<string[]>([]);
  const [apiKeyProviderFilter, setApiKeyProviderFilter] = useState<string[]>([]);
  const [apiKeyProtocolFilter, setApiKeyProtocolFilter] = useState<string[]>([]);
  const [apiKeyPage, setApiKeyPage] = useState(1);
  const [apiKeySortBy, setApiKeySortBy] = useState<'name' | 'createdAt' | 'owner'>('owner');
  const [apiKeySortDir, setApiKeySortDir] = useState<'asc' | 'desc'>('asc');
  // 密钥页视图：双栏聚焦（左队列 + 右详情，参照 Nexus 控制台的交互）或全量表格 + 抽屉。
  // 用户的选择记在本地，下次进来保持一致。
  const [apiKeyViewMode, setApiKeyViewMode] = useState<ApiKeyViewMode>(() => {
    try {
      return localStorage.getItem(API_KEY_VIEW_STORAGE_KEY) === 'table' ? 'table' : 'split';
    } catch {
      return 'split';
    }
  });
  const [apiKeyDrawerOpen, setApiKeyDrawerOpen] = useState(false);
  const [copiedApiKeyID, setCopiedApiKeyID] = useState('');
  const copiedApiKeyTimer = useRef<number | null>(null);
  // 今日 / 本月用量：与用量页同源（/__request-stats），只取按密钥聚合的两块。
  const [apiKeyUsageSnapshot, setApiKeyUsageSnapshot] = useState<ApiKeyUsageSource | null>(null);
  const [providerDraft, setProviderDraft] = useState({
    name: '我的 OpenAI 对话 Provider',
    protocol: 'openai_chat' as Protocol,
    baseUrl: 'https://example.com/v1/chat/completions',
    apiKeySource: '',
    defaultModel: '',
    defaultThinkingDepth: '',
    authType: 'api_key' as ProviderAuthType,
    requestAdapterJSON: '',
    teamOrganizationId: '',
    teamProjectId: '',
  });
  // 首包超时是 Provider 的调度旋钮，走专用接口保存：Provider 的 PATCH 是增量的，
  // 数字 0 分不清「没传」和「关掉限制」。输入框非受控，切到别的 Provider 时靠 key 重置。
  const providerFirstPacketRef = useRef<HTMLInputElement>(null);
  const providerConcurrencyRef = useRef<HTMLInputElement>(null);
  const providerQueueWaitRef = useRef<HTMLInputElement>(null);
  const providerRPMRef = useRef<HTMLInputElement>(null);
  const providerLaneGroupRef = useRef<HTMLInputElement>(null);
  const [claudeOAuthState, setClaudeOAuthState] = useState('');
  const [claudeOAuthCode, setClaudeOAuthCode] = useState('');
  const [claudeOAuthBusy, setClaudeOAuthBusy] = useState(false);
  // 自助注册（内网穿透）：原始令牌只在生成那一刻拿到，仅保存在内存里，
  // 弹窗关闭/刷新后不再可见。
  const [selfRegToken, setSelfRegToken] = useState('');
  const [selfRegBusy, setSelfRegBusy] = useState(false);
  const [claudeOAuthError, setClaudeOAuthError] = useState('');
  const [claudeOAuthFlowId, setClaudeOAuthFlowId] = useState('');
  const [claudeOAuthPolling, setClaudeOAuthPolling] = useState(false);
  // 回调进不来时怎么收尾：网关不在本机时，浏览器那一步永远回不到 localhost 的
  // 回调地址，所以「把浏览器停住的那条地址粘回来」是正式路径，不是报错后的备胎。
  const [claudeOAuthCallbackHint, setClaudeOAuthCallbackHint] = useState('');
  const [cursorOAuthBusy, setCursorOAuthBusy] = useState(false);
  const [cursorOAuthError, setCursorOAuthError] = useState('');
  const [cursorOAuthFlowId, setCursorOAuthFlowId] = useState('');
  const [cursorOAuthPolling, setCursorOAuthPolling] = useState(false);
  const [chatgptOAuthCode, setChatgptOAuthCode] = useState('');
  const [chatgptOAuthBusy, setChatgptOAuthBusy] = useState(false);
  const [chatgptOAuthError, setChatgptOAuthError] = useState('');
  const [chatgptOAuthFlowId, setChatgptOAuthFlowId] = useState('');
  const [chatgptOAuthPolling, setChatgptOAuthPolling] = useState(false);
  // 回调端口的事：ChatGPT 只允许回到 localhost:1455，网关在别的机器上时浏览器
  // 回调必然进不来，所以「粘贴浏览器停住的那条地址」是正路而不是报错后的备胎。
  const [chatgptOAuthCallbackHint, setChatgptOAuthCallbackHint] = useState('');
  const [chatgptOAuthCallbackBlocked, setChatgptOAuthCallbackBlocked] = useState('');
  const [qoderPatInput, setQoderPatInput] = useState('');
  const [qoderPatBusy, setQoderPatBusy] = useState(false);
  const [qoderPatError, setQoderPatError] = useState('');
  // WorkBuddy 订阅登录：浏览器外部链接 + 后端轮询，与 Cursor 同一套流程形状。
  const [workbuddyOAuthBusy, setWorkbuddyOAuthBusy] = useState(false);
  const [workbuddyOAuthPolling, setWorkbuddyOAuthPolling] = useState(false);
  const [workbuddyOAuthError, setWorkbuddyOAuthError] = useState('');
  const [workbuddyOAuthUrl, setWorkbuddyOAuthUrl] = useState('');
  // 添加 Provider 的目录 sheet（就地展开在列表下方），选一个预设再进入表单。
  const [providerSheetOpen, setProviderSheetOpen] = useState(false);
  const [routeDraft, setRouteDraft] = useState({
    name: '新建对话路由',
    providerId: '',
    outputProtocol: 'openai_chat' as Protocol,
  });
  const [apiKeyDraft, setApiKeyDraft] = useState({
    name: '新 API 密钥',
    providerId: '',
    outputProtocol: 'openai_chat' as Protocol,
    modelOverride: '',
    modelAliases: {} as Record<string, string>,
    thinkingDepthOverride: '',
    maxOutputTokens: 0,
    streamEnabled: true,
    codexForceFast: false,
  });
  // 模型页的 Provider 筛选分两级：一级是服务商，二级是同一服务商下的账号。
  // 同一个服务商每多登一个账号就多一条同名记录，平铺时一排全是「WorkBuddy 订阅」，
  // 光看名字认不出谁是谁。
  const [modelsBrandFilter, setModelsBrandFilter] = useState('__all__');
  const [modelsProviderFilter, setModelsProviderFilter] = useState('__all__');
  const [modelsSearchQuery, setModelsSearchQuery] = useState('');
  const [modelsCapabilityFilter, setModelsCapabilityFilter] = useState<'all' | 'thinking' | 'vision' | 'context' | 'free'>('all');
  const [modelsViewMode, setModelsViewMode] = useState<'grid' | 'table'>('grid');
  // 模型可能上百个：网格先渲一屏，其余按需追加，避免一次挂上全部卡片。
  const [modelsVisibleCount, setModelsVisibleCount] = useState(60);
  const [providersSearchQuery, setProvidersSearchQuery] = useState('');
  const [providersConnectFilter, setProvidersConnectFilter] = useState<'' | ProviderConnectKind>('');  const [providersStatusFilter, setProvidersStatusFilter] = useState<'all' | 'healthy' | 'unavailable' | 'disabled' | 'oauth'>('all');
  // 卡片密度只影响本机视觉，持久化在 localStorage（与主题/侧边栏一致）。
  const [providerCardDensity, setProviderCardDensity] = useState(readStoredProviderDensity);
  const chooseProviderDensity = (next: 'standard' | 'compact') => {
    setProviderCardDensity(next);
    writeStoredProviderDensity(next);
  };

  const selectedRoute = useMemo(
    () => state.routes.find((route) => route.id === selectedRouteID) || state.routes[0],
    [state.routes, selectedRouteID],
  );
  const selectedProvider = useMemo(
    () => state.providers.find((provider) => provider.id === selectedProviderID) || state.providers.find((provider) => provider.id === selectedRoute?.providerId) || state.providers[0],
    [state.providers, selectedProviderID, selectedRoute],
  );
  const selectedEndpoint = useMemo(
    () => state.endpoints.find((endpoint) => endpoint.protocol === selectedOutputProtocol) || state.endpoints.find((endpoint) => endpoint.id === selectedRoute?.outputEndpointId) || state.endpoints[0],
    [state.endpoints, selectedOutputProtocol, selectedRoute],
  );
  // Keep traffic-based ranking, but seed counts from localStorage so the first
  // paint after refresh matches the last session. Only re-sort when live
  // request counts actually change (avoids the cache→stats jump).
  const trafficRankScope = uiCacheScope(authStatus);
  const [cachedTrafficRanks, setCachedTrafficRanks] = useState<TrafficRankCache>(() => (
    readUICache<TrafficRankCache>(uiCacheScope(bootSession.auth), 'traffic-ranks')
    || { providers: {}, models: {} }
  ));

  const recentActivityCutoffMs = useMemo(() => Date.now() - 3 * 24 * 60 * 60 * 1000, [logs, requestStats]);

  const liveProviderRequestCounts = useMemo(() => {
    const counts = new Map<string, number>();
    for (const log of logs) {
      const ts = new Date(log.time).getTime();
      if (!Number.isFinite(ts) || ts < recentActivityCutoffMs) continue;
      const providerID = log.providerId || '';
      if (!providerID) continue;
      counts.set(providerID, (counts.get(providerID) || 0) + 1);
    }
    for (const item of requestStats?.range?.byProvider || requestStats?.month?.byProvider || []) {
      if (!item.providerId) continue;
      const current = counts.get(item.providerId) || 0;
      if (item.requestCount > current) counts.set(item.providerId, item.requestCount);
    }
    return counts;
  }, [logs, requestStats, recentActivityCutoffMs]);

  const liveModelRequestCounts = useMemo(() => {
    const counts = new Map<string, number>();
    for (const log of logs) {
      const ts = new Date(log.time).getTime();
      if (!Number.isFinite(ts) || ts < recentActivityCutoffMs) continue;
      const modelID = (log.model || '').split('->').pop()?.trim() || log.model || '';
      if (!modelID) continue;
      const key = `${log.providerId || ''}::${modelID}`;
      counts.set(key, (counts.get(key) || 0) + 1);
      counts.set(modelID, (counts.get(modelID) || 0) + 1);
    }
    return counts;
  }, [logs, recentActivityCutoffMs]);

  const liveTrafficReady = liveProviderRequestCounts.size > 0 || liveModelRequestCounts.size > 0
    || Boolean(requestStats?.range?.byProvider?.length || requestStats?.month?.byProvider?.length);

  useEffect(() => {
    if (!liveTrafficReady) return;
    const next: TrafficRankCache = {
      providers: trafficRanksFromMap(liveProviderRequestCounts),
      models: trafficRanksFromMap(liveModelRequestCounts),
    };
    if (
      trafficRanksEqual(next.providers, cachedTrafficRanks.providers)
      && trafficRanksEqual(next.models, cachedTrafficRanks.models)
    ) {
      return;
    }
    setCachedTrafficRanks(next);
    writeUICache(trafficRankScope, 'traffic-ranks', next);
  }, [liveTrafficReady, liveProviderRequestCounts, liveModelRequestCounts, trafficRankScope, cachedTrafficRanks]);

  const recentProviderRequestCounts = useMemo(() => {
    if (liveTrafficReady) return liveProviderRequestCounts;
    return mapFromTrafficRanks(cachedTrafficRanks.providers);
  }, [liveTrafficReady, liveProviderRequestCounts, cachedTrafficRanks.providers]);

  const recentModelRequestCounts = useMemo(() => {
    if (liveTrafficReady) return liveModelRequestCounts;
    return mapFromTrafficRanks(cachedTrafficRanks.models);
  }, [liveTrafficReady, liveModelRequestCounts, cachedTrafficRanks.models]);

  const providerRankScope = trafficRankScopeOf(authStatus);
  const providerRankingReady = liveTrafficReady;
  const cachedProviderRanks = useMemo(
    () => mapFromTrafficRanks(cachedTrafficRanks.providers),
    [cachedTrafficRanks.providers],
  );

  // 换账号（作用域变了）或停在这页之外：清掉这一把顺序，回到「等下重排」。
  useEffect(() => {
    if (providerRankSnapshot.scope === providerRankScope && activeNav === 'input-providers') return;
    setProviderRankSnapshot({ scope: providerRankScope, order: null });
  }, [activeNav, providerRankScope, providerRankSnapshot.scope]);

  // 用户主动点了「获取模型」：按当下最新计数重排这一页的 Provider。
  const [providerRankRefreshTick, setProviderRankRefreshTick] = useState(0);

  // 快照：进入这一页的首次绘制（或刚点了「获取模型」）才按当时的计数排一次；
  // 之后日志轮询只更新数字，顺序不动。
  const rankedOrder = useMemo(() => {
    const decision = providerRankOrder({ previous: providerRankSnapshot.order, ready: providerRankingReady, force: false });
    if (decision !== 'rank') return null;
    const providers = state.providers;
    return rankProviderIDs(providers.map((provider) => provider.id), (id) => recentProviderRequestCounts.get(id) || 0, (id) => providers.find((provider) => provider.id === id)?.name || id);
    // providerRankRefreshTick 只是让显式刷新重新排一次的触发器。
  }, [providerRankSnapshot.order, providerRankingReady, state.providers, recentProviderRequestCounts, providerRankRefreshTick]);
  useEffect(() => {
    if (rankedOrder == null || providerRankSnapshot.order === rankedOrder) return;
    setProviderRankSnapshot((current) => (current.order === rankedOrder ? current : { ...current, order: rankedOrder }));
  }, [rankedOrder, providerRankSnapshot.order]);
  const providerOrder = providerRankSnapshot.order;

  const refreshProviderRanking = () => {
    if (activeNav !== 'input-providers') return;
    setProviderRankSnapshot((current) => ({ ...current, order: null }));
    setProviderRankRefreshTick((tick) => tick + 1);
  };

  const sortedProviders = useMemo(() => {
    if (providerOrder != null) {
      const byID = new Map(state.providers.map((provider) => [provider.id, provider]));
      const ranked = providerOrder.map((id) => byID.get(id)).filter((provider): provider is Provider => provider != null);
      const rankedIDs = new Set(ranked.map((provider) => provider.id));
      // 快照之后新加的 Provider 接在后面，按名字排，同样不因轮询换位。
      const added = state.providers.filter((provider) => !rankedIDs.has(provider.id)).sort((a, b) => a.name.localeCompare(b.name, 'zh'));
      return [...ranked, ...added];
    }
    // 还没排过（统计请求没回来）：沿用上个会话缓存的计数排，首次绘制后不再跳。
    return [...state.providers].sort((a, b) => {
      const ca = cachedProviderRanks.get(a.id) || 0;
      const cb = cachedProviderRanks.get(b.id) || 0;
      if (ca !== cb) return cb - ca;
      return a.name.localeCompare(b.name, 'zh');
    });
  }, [state.providers, providerOrder, cachedProviderRanks]);

  const providersSearch = useMemo(() => {
    const query = providersSearchQuery.trim();
    if (!query) return { matcher: null as null | ((text: string) => boolean), error: '' };
    try {
      const re = new RegExp(query, 'i');
      return { matcher: (text: string) => re.test(text), error: '' };
    } catch (err) {
      const needle = query.toLowerCase();
      return {
        matcher: (text: string) => text.toLowerCase().includes(needle),
        error: err instanceof Error ? err.message : '无效的正则表达式',
      };
    }
  }, [providersSearchQuery]);

  // 筛选胶囊只列"配了的"连接方式，所以当前选中的那种被删光时胶囊会消失：
  // 此时退回"全部"，否则用户会停在一个看不见的筛选项上、列表恒空。
  // 口径与胶囊一致（含已停用的，禁用不等于没配）。
  useEffect(() => {
    if (!providersConnectFilter) return;
    const stillThere = state.providers.some((provider) => providerConnectKind(provider) === providersConnectFilter);
    if (!stillThere) setProvidersConnectFilter('');
  }, [providersConnectFilter, state.providers]);

  const filteredProviders = useMemo(() => {
    // 索引里除了名称/ID/协议，还放账号标签、创建者与它提供的模型：
    // 「按账号找」「按创建人找」「按模型找」是同一张卡上真实的三个入口。
    // consoleUsers 用于把 ownerUserId 解析成显示名（空 = 管理员自建）。
    const ownerNames = new Map<string, string>();
    for (const user of consoleUsers) ownerNames.set(user.id, user.username);
    const modelsByProvider = new Map<string, string>();
    for (const model of state.models) {
      const current = modelsByProvider.get(model.providerId);
      modelsByProvider.set(model.providerId, current ? `${current} ${model.id}` : model.id);
    }
    const ownerLabelOf = (provider: Provider): string => {
      const ownerID = (provider.ownerUserId || '').trim();
      if (!ownerID) return '管理员';
      return ownerNames.get(ownerID) || ownerID;
    };
    return sortedProviders.filter((provider) => {
      if (providersStatusFilter === 'healthy') {
        if (provider.disabled || provider.healthStatus === 'unavailable') return false;
      } else if (providersStatusFilter === 'unavailable') {
        if (provider.disabled || provider.healthStatus !== 'unavailable') return false;
      } else if (providersStatusFilter === 'disabled') {
        if (!provider.disabled) return false;
      } else if (providersStatusFilter === 'oauth') {
        const isOAuth = provider.authType === 'claude_oauth' || provider.authType === 'cursor_oauth' || provider.authType === 'chatgpt_oauth' || isQoderConnectKind(provider.authType);
        if (!isOAuth) return false;
      }

      if (providersConnectFilter && providerConnectKind(provider) !== providersConnectFilter) {
        return false;
      }
      if (!providersSearch.matcher) return true;
      const haystack = [
        provider.name,
        provider.id,
        provider.baseUrl || '',
        providerConnectLabel(providerConnectKind(provider)),
        protocolLabel(provider.protocol),
        providerAccountLabel(provider) || '',
        ownerLabelOf(provider),
        modelsByProvider.get(provider.id) || '',
      ].join(' ');
      return providersSearch.matcher(haystack);
    });
  }, [sortedProviders, providersStatusFilter, providersConnectFilter, providersSearch, consoleUsers, state.models]);

  const modelsMenuProviders = useMemo(() => {
    const providerIDs = new Set(state.models.map((model) => model.providerId));
    // Always show Cursor/Claude OAuth providers that are connected, even before
    // their first successful model sync, so the filter chip is visible.
    return sortedProviders.filter((provider) => {
      if (providerIDs.has(provider.id)) return true;
      if (provider.authType === 'cursor_oauth' && provider.cursorOAuth?.connected) return true;
      if (provider.authType === 'claude_oauth' && provider.claudeOAuth?.connected) return true;
      if (provider.authType === 'chatgpt_oauth' && provider.chatgptOAuth?.connected) return true;
      if (isQoderConnectKind(provider.authType) && provider.qoderPat?.connected) return true;
      return false;
    });
  }, [state.models, sortedProviders]);
  // 一级筛选的胶囊：一个服务商一个，账号数在二级展开。
  const modelsProviderGroups = useMemo(() => groupProvidersByBrand(modelsMenuProviders), [modelsMenuProviders]);
  const modelsActiveGroup = useMemo(
    () => modelsProviderGroups.find((group) => group.key === modelsBrandFilter) ?? null,
    [modelsProviderGroups, modelsBrandFilter],
  );
  // 两级筛选最终收敛成一组 provider id：选中具体账号只留它，选中服务商留全组。
  const modelsFilterProviderIDs = useMemo(() => {
    if (modelsProviderFilter !== '__all__') return new Set([modelsProviderFilter]);
    if (modelsActiveGroup) return new Set(modelsActiveGroup.items.map((provider) => provider.id));
    return null;
  }, [modelsProviderFilter, modelsActiveGroup]);
  // 选中的服务商 / 账号被删掉时回到「全部」，否则用户会停在一个看不见的筛选上、
  // 列表恒空（与 Provider 卡片页的 connect 胶囊同一套兜底）。
  useEffect(() => {
    if (modelsBrandFilter === '__all__') return;
    if (modelsProviderGroups.some((group) => group.key === modelsBrandFilter)) return;
    setModelsBrandFilter('__all__');
    setModelsProviderFilter('__all__');
  }, [modelsBrandFilter, modelsProviderGroups]);
  useEffect(() => {
    if (modelsProviderFilter === '__all__') return;
    if (modelsMenuProviders.some((provider) => provider.id === modelsProviderFilter)) return;
    setModelsProviderFilter('__all__');
  }, [modelsProviderFilter, modelsMenuProviders]);
  // 工具栏那句「当前筛选」要能一句话读出来：只选服务商时说品牌，选了账号说品牌 + 账号。
  const modelsFilterCaption = useMemo(() => {
    if (modelsProviderFilter !== '__all__') {
      const provider = state.providers.find((item) => item.id === modelsProviderFilter);
      if (!provider) return modelsProviderFilter;
      const account = shortenAccountLabel(providerAccountLabel(provider));
      return account && account !== provider.name ? `${provider.name} · ${account}` : provider.name;
    }
    return modelsActiveGroup ? modelsActiveGroup.title : '';
  }, [modelsProviderFilter, modelsActiveGroup, state.providers]);
  const modelsSearch = useMemo(() => {
    const query = modelsSearchQuery.trim();
    if (!query) return { matcher: null as null | ((text: string) => boolean), error: '' };
    try {
      const re = new RegExp(query, 'i');
      return { matcher: (text: string) => re.test(text), error: '' };
    } catch (err) {
      const needle = query.toLowerCase();
      return {
        matcher: (text: string) => text.toLowerCase().includes(needle),
        error: err instanceof Error ? err.message : '无效的正则表达式',
      };
    }
  }, [modelsSearchQuery]);

  // 同一服务商下多个账号列的是几乎完全一样的模型（每家 WorkBuddy 订阅都是同 19 个），
  // 平铺出来就是 285 张一模一样的卡片。选中服务商或单个账号时按模型名合并，每张
  // 卡片带上「哪些账号有这个模型」；只有没选服务商（全部）时才逐账号陈列。
  const modelsMergeAcrossAccounts = Boolean(modelsActiveGroup);
  const modelsByProviderId = useMemo(() => new Map(state.providers.map((provider) => [provider.id, provider])), [state.providers]);

  const filteredModels = useMemo(() => {
    const base = modelsFilterProviderIDs
      ? state.models.filter((model) => modelsFilterProviderIDs.has(model.providerId))
      : state.models;
    const matched = !modelsSearch.matcher
      ? base
      : base.filter((model) => {
          const provider = state.providers.find((item) => item.id === model.providerId);
          const haystacks = [
            model.id,
            model.providerId,
            provider?.name || '',
            provider ? providerOptionLabel(provider) : '',
            protocolLabel(model.protocol),
          ];
          return haystacks.some((text) => Boolean(text) && modelsSearch.matcher!(text));
        });
    const capabilityMatched = modelsCapabilityFilter === 'all'
      ? matched
      : matched.filter((model) => {
          if (modelsCapabilityFilter === 'thinking') {
            return isThinkingModel(model.id);
          }
          if (modelsCapabilityFilter === 'vision') {
            return isVisionModel(model.id);
          }
          if (modelsCapabilityFilter === 'context') {
            return (model.contextLength || 0) >= 128000;
          }
          if (modelsCapabilityFilter === 'free') {
            return isFreeModel(model);
          }
          return true;
        });
    const sorted = [...capabilityMatched].sort((a, b) => {
      const ca = recentModelRequestCounts.get(`${a.providerId}::${a.id}`) || recentModelRequestCounts.get(a.id) || 0;
      const cb = recentModelRequestCounts.get(`${b.providerId}::${b.id}`) || recentModelRequestCounts.get(b.id) || 0;
      if (ca !== cb) return cb - ca;
      return a.id.localeCompare(b.id);
    });
    if (!modelsMergeAcrossAccounts) return sorted;
    return mergeModelsAcrossAccounts(sorted, modelsByProviderId) as Model[];
  }, [state.models, state.providers, modelsFilterProviderIDs, modelsCapabilityFilter, modelsSearch, recentModelRequestCounts, modelsMergeAcrossAccounts, modelsByProviderId]);

  // 筛选条件一变就回到第一屏，否则用户改完筛选还停在“加载了 480 个”的位置。
  useEffect(() => { setModelsVisibleCount(60); }, [modelsBrandFilter, modelsProviderFilter, modelsCapabilityFilter, modelsSearchQuery]);
  const visibleModels = useMemo(() => filteredModels.slice(0, modelsVisibleCount), [filteredModels, modelsVisibleCount]);

  const modelsCapabilityCounts = useMemo(() => {
    const base = modelsFilterProviderIDs
      ? state.models.filter((model) => modelsFilterProviderIDs.has(model.providerId))
      : state.models;
    const matched = !modelsSearch.matcher
      ? base
      : base.filter((model) => {
          const provider = state.providers.find((item) => item.id === model.providerId);
          const haystacks = [
            model.id,
            model.providerId,
            provider?.name || '',
            provider ? providerOptionLabel(provider) : '',
            protocolLabel(model.protocol),
          ];
          return haystacks.some((text) => Boolean(text) && modelsSearch.matcher!(text));
        });
    let thinking = 0;
    let vision = 0;
    let context = 0;
    let free = 0;
    for (const m of matched) {
      if (isThinkingModel(m.id)) thinking++;
      if (isVisionModel(m.id)) vision++;
      if ((m.contextLength || 0) >= 128000) context++;
      if (isFreeModel(m)) free++;
    }
    return { all: matched.length, thinking, vision, context, free };
  }, [state.models, state.providers, modelsFilterProviderIDs, modelsSearch]);

  const modelsMenuSummary = useMemo(() => {
    const counts = new Map<string, number>();
    for (const model of state.models) {
      counts.set(model.providerId, (counts.get(model.providerId) || 0) + 1);
    }
    return counts;
  }, [state.models]);

  // 解析密钥所属用户显示名：空 = 管理员（旧密钥/管理员自建）。
  const apiKeyOwnerName = React.useCallback((key: APIKey) => {
    const ownerID = (key.ownerUserId || '').trim();
    if (!ownerID) return '管理员';
    return consoleUsers.find((user) => user.id === ownerID)?.username || ownerID;
  }, [consoleUsers]);

  // 解析 Provider 创建者显示名：空 = 管理员（旧 Provider/管理员自建）。
  const providerOwnerName = React.useCallback((provider: Provider) => {
    const ownerID = (provider.ownerUserId || '').trim();
    if (!ownerID) return '管理员';
    return consoleUsers.find((user) => user.id === ownerID)?.username || ownerID;
  }, [consoleUsers]);

  const filteredApiKeys = useMemo(() => {
    let keys = state.apiKeys || [];
    const keyword = apiKeyKeyword.trim().toLowerCase();
    if (keyword) {
      // 名称筛选框只匹配名称与密钥本身；Provider / 协议 / 用户走各自的多选筛选。
      keys = keys.filter((key) => `${key.name} ${key.key}`.toLowerCase().includes(keyword));
    }
    if (apiKeyOwnerFilter.length > 0) {
      keys = keys.filter((key) => apiKeyOwnerFilter.includes((key.ownerUserId || '').trim() || LOG_OWNER_FILTER_ADMIN));
    }
    if (apiKeyProviderFilter.length > 0) {
      keys = keys.filter((key) => {
        const route = state.routes.find((item) => item.id === key.routeId);
        return route ? apiKeyProviderFilter.includes(route.providerId) : false;
      });
    }
    if (apiKeyProtocolFilter.length > 0) {
      keys = keys.filter((key) => {
        const route = state.routes.find((item) => item.id === key.routeId);
        return route ? apiKeyProtocolFilter.includes(route.outputProtocol) : false;
      });
    }
    const sorted = [...keys];
    const dir = apiKeySortDir === 'asc' ? 1 : -1;
    sorted.sort((a, b) => {
      if (apiKeySortBy === 'name') {
        const byName = a.name.localeCompare(b.name, 'zh-CN');
        if (byName !== 0) return byName * dir;
      } else if (apiKeySortBy === 'owner') {
        const byOwner = apiKeyOwnerName(a).localeCompare(apiKeyOwnerName(b), 'zh-CN');
        if (byOwner !== 0) return byOwner * dir;
        const byName = a.name.localeCompare(b.name, 'zh-CN');
        if (byName !== 0) return byName * dir;
      } else {
        const ta = Date.parse(a.createdAt || '') || 0;
        const tb = Date.parse(b.createdAt || '') || 0;
        if (ta !== tb) return (ta - tb) * dir;
        const byName = a.name.localeCompare(b.name, 'zh-CN');
        if (byName !== 0) return byName * dir;
      }
      return a.id.localeCompare(b.id) * dir;
    });
    return sorted;
  }, [state.apiKeys, state.routes, apiKeyKeyword, apiKeyOwnerFilter, apiKeyProviderFilter, apiKeyProtocolFilter, apiKeySortBy, apiKeySortDir, apiKeyOwnerName]);

  // 分页展示：每页最多 API_KEYS_PAGE_SIZE 个；页码越界时自动收敛到最后一页。
  const apiKeyTotalPages = Math.max(1, Math.ceil(filteredApiKeys.length / API_KEYS_PAGE_SIZE));
  const currentApiKeyPage = Math.min(apiKeyPage, apiKeyTotalPages);
  const apiKeyPageStart = (currentApiKeyPage - 1) * API_KEYS_PAGE_SIZE;
  const pagedApiKeys = filteredApiKeys.slice(apiKeyPageStart, apiKeyPageStart + API_KEYS_PAGE_SIZE);

  // 过滤/排序变化时回到第 1 页，避免停留在不存在的页。
  useEffect(() => {
    setApiKeyPage(1);
  }, [apiKeyKeyword, apiKeyOwnerFilter, apiKeyProviderFilter, apiKeyProtocolFilter, apiKeySortBy, apiKeySortDir]);

  const selectedApiKey = useMemo(() => {
    const keys = state.apiKeys || [];
    if (selectedApiKeyID && keys.some((item) => item.id === selectedApiKeyID)) {
      return keys.find((item) => item.id === selectedApiKeyID);
    }
    return filteredApiKeys[0];
  }, [state.apiKeys, selectedApiKeyID, filteredApiKeys]);

  useEffect(() => {
    try {
      localStorage.setItem(API_KEY_VIEW_STORAGE_KEY, apiKeyViewMode);
    } catch {
      // 隐私模式下写不进去，忽略：只是不记忆而已
    }
  }, [apiKeyViewMode]);

  // 进入密钥页 / 切回前台时取一次今日、本月用量。统计接口本身有服务端聚合缓存，
  // 不需要跟着 5 秒轮询刷——密钥列表上的「今日 N 次」晚一分钟更新没人会注意。
  useEffect(() => {
    if (activeNav !== 'api-keys') return undefined;
    let cancelled = false;
    const load = () => {
      void fetch(`${API_BASE}/__request-stats`, {credentials: 'same-origin'})
        .then((response) => (response.ok ? response.json() : Promise.reject(new Error(`HTTP ${response.status}`))))
        .then((data: ApiKeyUsageSource) => { if (!cancelled) setApiKeyUsageSnapshot(data); })
        .catch(() => { /* 用量读不到不影响配置，不加噪声 */ });
    };
    load();
    const timer = window.setInterval(load, 60_000);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [activeNav]);

  const apiKeyUsageMap = useMemo(() => buildApiKeyUsageMap(apiKeyUsageSnapshot), [apiKeyUsageSnapshot]);

  // 「哪个节点在服务」由后端的 activeProviderId 决定，而它会在失败转移、回切探测、
  // 别的标签页里发请求时变化——只刷新用量的话，控制台上的绿点会一直停在旧节点，
  // 直到手动点一次刷新。所以停在密钥页时静默轮询 /__state（不弹 toast、不闪 loading）。
  useEffect(() => {
    if (activeNav !== 'api-keys') return undefined;
    let cancelled = false;
    const tick = () => {
      if (!isPageVisible()) return;
      void fetchWithTimeout(`${API_BASE}/__state`, {credentials: 'same-origin'})
        .then((response) => (response.ok ? response.json() : Promise.reject(new Error(`HTTP ${response.status}`))))
        .then((data: GatewayState) => {
          if (cancelled) return;
          setState((current) => normalizeGatewayState(data, current));
          markBackendReachable();
          setDataFetchedAt(new Date());
        })
        .catch(() => { /* 轮询失败保持现状，下一轮再试 */ });
    };
    const timer = window.setInterval(tick, isRemoteOrigin() ? BACKEND_POLL_MS_REMOTE : BACKEND_POLL_MS_LOCAL);
    const onVisible = () => { if (isPageVisible()) tick(); };
    document.addEventListener('visibilitychange', onVisible);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
      document.removeEventListener('visibilitychange', onVisible);
    };
  }, [activeNav]);

  const resetApiKeyFilters = useCallback(() => {
    setApiKeyKeyword('');
    setApiKeyOwnerFilter([]);
    setApiKeyProviderFilter([]);
    setApiKeyProtocolFilter([]);
  }, []);

  // 具备某项权限：本地直连（无鉴权）与管理员都按放行处理。
  const canDo = (key: string) => !authStatus?.requireAuth || can(permissions, key);

  const apiKeyFiltersProps = useMemo(() => ({
    keyword: apiKeyKeyword,
    setKeyword: setApiKeyKeyword,
    ownerOptions: [
      // 哨兵值只代表「无归属」的老 Key；被提权为管理员的用户仍用自己的 id 持有 Key。
      {id: LOG_OWNER_FILTER_ADMIN, label: '内置 admin（无归属）'},
      ...consoleUsers.map((user) => ({id: user.id, label: user.username})),
    ],
    owner: apiKeyOwnerFilter,
    setOwner: setApiKeyOwnerFilter,
    // 只有能看全员数据（usage.all / 全资源范围）时才显示「所属用户」列
    showOwner: authStatus?.scope === 'all' || canDo('usage.all'),
    provider: apiKeyProviderFilter,
    setProvider: setApiKeyProviderFilter,
    protocol: apiKeyProtocolFilter,
    setProtocol: setApiKeyProtocolFilter,
    providerLabels: Object.fromEntries(state.providers.map((provider) => [provider.id, providerOptionLabel(provider)])),
    sort: apiKeySortBy,
    setSort: (value: string) => setApiKeySortBy(value as 'name' | 'createdAt' | 'owner'),
    reset: resetApiKeyFilters,
  }), [apiKeyKeyword, apiKeyOwnerFilter, apiKeyProviderFilter, apiKeyProtocolFilter, state.providers, consoleUsers, authStatus?.role, authStatus?.scope, permissions, resetApiKeyFilters, apiKeySortBy]);

  useEffect(() => {
    const resolved = applyThemeMode(themeMode);
    setResolvedTheme(resolved);
    try {
      localStorage.setItem(THEME_STORAGE_KEY, themeMode);
    } catch {
      // ignore
    }
  }, [themeMode]);

  useEffect(() => {
    if (themeMode !== 'system' || typeof window.matchMedia !== 'function') return;
    const media = window.matchMedia('(prefers-color-scheme: dark)');
    const onChange = () => {
      setResolvedTheme(applyThemeMode('system'));
    };
    media.addEventListener('change', onChange);
    return () => media.removeEventListener('change', onChange);
  }, [themeMode]);

  useEffect(() => {
    const onThemeMessage = (event: MessageEvent) => {
      const data = event.data;
      if (!data || typeof data !== 'object') return;
      const mode = (data as { theme?: string }).theme;
      if (mode === 'light' || mode === 'dark' || mode === 'system') {
        setThemeMode(mode);
      }
    };
    window.addEventListener('message', onThemeMessage);
    (window as unknown as { __setGatewayTheme?: (mode: ThemeMode) => void }).__setGatewayTheme = (mode) => {
      setThemeMode(mode);
    };
    return () => {
      window.removeEventListener('message', onThemeMessage);
      delete (window as unknown as { __setGatewayTheme?: (mode: ThemeMode) => void }).__setGatewayTheme;
    };
  }, []);

  useEffect(() => {
    void (async () => {
      // 首屏并行探测健康与鉴权
      const [connected, auth] = await Promise.all([refreshBackendHealth(), refreshAuthStatus()]);
      setAuthChecked(true);
      if (connected && auth && (!auth.requireAuth || auth.authenticated)) {
        // 有缓存则立刻打开页面，避免「正在加载配置」白屏；后台再静默拉最新
        const scope = uiCacheScope(auth);
        const cachedState = readUICache<GatewayState>(scope, 'state');
        if (cachedState) {
          setState((current) => normalizeGatewayState(cachedState, current));
          setStateHydrated(true);
          const range = usageRangeRef.current;
          const cachedStats = readUICache<RequestStatsSnapshot>(scope, `stats:${range.from}:${range.to}`);
          if (cachedStats) {
            setRequestStats(normalizeRequestStats(cachedStats));
            setDataFetchedAt(new Date());
          }
        }
        await bootstrapAuthenticatedSession(auth);
        setStateHydrated(true);
        return;
      }
      // 需要登录时等登录成功后再 hydrate；其余情况（免登录但未连上）直接放行
      if (!(auth && auth.requireAuth && !auth.authenticated)) {
        setStateHydrated(true);
      }
    })();
    // Tunnel restore is async after gateway start; keep UI in sync with live
    // __state (App WebView and browser otherwise diverge after first paint).
    const pollOnce = () => {
      void (async () => {
        if (!isPageVisible()) return;
        const [connected, auth] = await Promise.all([refreshBackendHealth(), refreshAuthStatus()]);
        if (!connected) {
          // 只有累计失败到阈值（确认真断了）才走重连流程；单次抖动直接跳过本轮，
          // 否则每次抖动都会额外打一串补偿请求，反而加重公网链路负担。
          if (backendFailStreakRef.current >= BACKEND_FAIL_STREAK_LIMIT) {
            await reconnectBackend(false);
          }
          return;
        }
        if (auth && (!auth.requireAuth || auth.authenticated)) {
          void refreshState(false);
          void refreshRequestStats(undefined, undefined, { silent: true });
        }
      })();
    };
    // 公网经隧道访问时单次往返约 1.5s，5s 轮询会让多路请求互相叠加；放慢到 15s。
    const timer = window.setInterval(pollOnce, isRemoteOrigin() ? BACKEND_POLL_MS_REMOTE : BACKEND_POLL_MS_LOCAL);
    // 重新回到前台（Chrome 变为最前台 / 标签页重新可见）时立即补一次刷新，
    // 让底部“数据更新于”不必等到下一个 5s 周期才追上。
    const onFocusResume = () => { if (isPageVisible()) pollOnce(); };
    window.addEventListener('focus', onFocusResume);
    document.addEventListener('visibilitychange', onFocusResume);
    return () => {
      window.clearInterval(timer);
      window.removeEventListener('focus', onFocusResume);
      document.removeEventListener('visibilitychange', onFocusResume);
    };
  }, []);

  useEffect(() => {
    const nextPublicAccess = { ...defaultPublicAccess, ...state.publicAccess };
    // 只在表单相关的持久化字段变化时同步；tunnel.status / statusMessage 变化不碰输入框。
    const formSyncKey = [
      nextPublicAccess.mode || '',
      nextPublicAccess.enabled ? '1' : '0',
      nextPublicAccess.customDomain || '',
      nextPublicAccess.uiDomain || '',
      nextPublicAccess.exposeApi === false ? '0' : '1',
      nextPublicAccess.exposeUi === false ? '0' : '1',
      nextPublicAccess.tunnelName || '',
      nextPublicAccess.credentialsFile || '',
      nextPublicAccess.tunnelConfigFile || '',
    ].join('\0');
    if (formSyncKey === publicAccessFormSyncRef.current) {
      return;
    }
    publicAccessFormSyncRef.current = formSyncKey;

    setPublicDraft(nextPublicAccess);
    // Only sync subdomain prefixes from persisted hostnames. When domains are
    // empty (pre-bind), keep the user's in-progress edits.
    const { prefix, root } = splitCustomDomain(nextPublicAccess.customDomain);
    if (nextPublicAccess.customDomain) {
      setCustomDomainPrefix(prefix || 'gateway');
    }
    if (nextPublicAccess.uiDomain) {
      const uiSplit = splitCustomDomain(nextPublicAccess.uiDomain);
      setUIDomainPrefix(uiSplit.prefix || 'console');
    } else if (nextPublicAccess.customDomain) {
      const uiSplit = splitCustomDomain(deriveUIDomainFromAPI(nextPublicAccess.customDomain));
      if (uiSplit.prefix) setUIDomainPrefix(uiSplit.prefix);
    }
    setCustomDomainRoot((current) => {
      if (root) return root;
      if (current) return current;
      return '';
    });
  }, [state.publicAccess]);

  // 日期弹层：点外面或按 Esc 收起。只在打开时挂监听，避免常驻一个 document 级
  // 捕获阶段回调。
  useEffect(() => {
    if (!logsDateOpen) return;
    const onPointer = (event: PointerEvent) => {
      if (!logsDateRef.current?.contains(event.target as Node)) setLogsDateOpen(false);
    };
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setLogsDateOpen(false);
    };
    document.addEventListener('pointerdown', onPointer);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('pointerdown', onPointer);
      document.removeEventListener('keydown', onKey);
    };
  }, [logsDateOpen]);

  useEffect(() => () => {
    if (cloudflarePollRef.current != null) {
      window.clearInterval(cloudflarePollRef.current);
    }
    cloudflarePoll?.stop();
  }, []);

  useEffect(() => {
    const onPopState = () => {
      setActiveNav(navIDFromPath(window.location.pathname));
      document.querySelector('.main')?.scrollTo({ top: 0, behavior: 'auto' });
      window.scrollTo({ top: 0, behavior: 'auto' });
    };
    window.addEventListener('popstate', onPopState);
    return () => window.removeEventListener('popstate', onPopState);
  }, []);

  useEffect(() => {
    if (activeNav === 'public-access' || activeNav === 'settings') {
      void refreshCloudflareAuthStatus();
      void refreshState(false);
    }
    if (activeNav === 'public-access') {
      void refreshPublicControlTokenStatus();
    }
  }, [activeNav]);

  useEffect(() => {
    if (activeNav !== 'usage-stats') return;
    // 「今日」按网关的时区算：每个密钥的「今日 N 次」用的是服务端的今天。
    const today = formatDateInZone(new Date(), serverTimeZone);
    if (usageFollowTodayRef.current) {
      if (usageRangeRef.current.from !== today || usageRangeRef.current.to !== today) {
        setUsageFrom(today);
        setUsageTo(today);
        usageRangeRef.current = { from: today, to: today };
      }
      void refreshRequestStats(today, today, { silent: Boolean(requestStats) });
    } else {
      void refreshRequestStats(usageRangeRef.current.from, usageRangeRef.current.to, { silent: Boolean(requestStats) });
    }
  }, [activeNav]);

  // RTK 节省统计：进入用量页或切换 7/30/90/全部 时拉取。接口仅管理员可用，
  // 普通用户不会渲染这张卡，403 直接忽略。
  useEffect(() => {
    if (activeNav !== 'usage-stats') return;
    void refreshRTKStats();
  }, [activeNav, rtkStatsDays]);

  // 日志每页条数按可用高度算：拿不到表格（还没渲染）就沿用上次的值。
  // 只在数值真的变了才 setState，避免 ResizeObserver 与重渲染互相触发。
  useEffect(() => {
    if (activeNav !== 'traffic-tokens') return;
    // 窄屏（<901px）不锁卡片高度，表体高度由内容决定——量它会越量越大，直接跳过。
    if (!window.matchMedia('(min-width: 901px)').matches) return;
    let observer: ResizeObserver | null = null;
    let observed: Element[] = [];
    let retry = 0;
    let tries = 0;
    const measure = () => {
      const host = logsTableRef.current;
      if (!host) return;
      observer ??= new ResizeObserver(() => measure());
      // 表体高度变化（调度决策块出现、窗口缩放）要重新算：观察表格与滚动容器本身。
      const targets = [host, host.querySelector('.tlog-scroll')].filter((el): el is Element => Boolean(el));
      for (const el of observed) if (!targets.includes(el)) observer.unobserve(el);
      for (const el of targets) if (!observed.includes(el)) observer.observe(el);
      observed = targets;

      const scroller = host.querySelector('.tlog-scroll');
      const head = host.querySelector('.tlog-head');
      const firstRow = host.querySelector('.tlog-row');
      if (!scroller || !firstRow) {
        // 首屏还在加载态、表体没铺开：稍后再量。空列表永远量不到行，重试要封顶。
        if (++tries > 20) return;
        window.clearTimeout(retry);
        retry = window.setTimeout(measure, 400);
        return;
      }
      // 只量已经落定的行：新行进场时 max-height 从 0 撑到 44px，动画途中量第一行
      // 会读到几像素，页容量被算成 55 → setState → 整表重取，新行连同动画一起被
      // 替换掉，下一次轮询再循环一遍。表里全是动画中的行就这轮不量。
      const rowSample = host.querySelector('.tlog-row:not(.is-new)');
      if (!rowSample) return;
      const rowHeight = rowSample.getBoundingClientRect().height;
      const headHeight = head?.getBoundingClientRect().height || 0;
      // clientHeight 已经扣掉横向滚动条；表头也在这个滚动容器里，要减掉。
      const avail = scroller.clientHeight - headHeight;
      if (rowHeight <= 0 || avail <= 0) return;
      // 下限只保证表里有两三行，屏幕矮时宁可少几行也不能把行裁在卡片底部。
      const next = Math.min(200, Math.max(5, Math.floor(avail / rowHeight)));
      if (next === pageSizeRef.current) return;
      pageSizeRef.current = next;
      setLogsPageSize(next);
    };
    measureLogsPageSizeRef.current = measure;
    measure();
    return () => {
      window.clearTimeout(retry);
      observer?.disconnect();
      measureLogsPageSizeRef.current = null;
    };
    // 表格挂载由 ref 回调再量一次：直接打开 /traffic-tokens 时整页要先过启动闸，
    // 挂载发生在之后的某次提交里，只靠这里执行一次会错过。
  }, [activeNav, logsAdvancedOpen, logsPageSize, logsFetchedOnce]);

  useEffect(() => {
    if (activeNav !== 'traffic-tokens') return;
    // 跟随今天：跨午夜后从侧边栏再进本页，把日期滚到真实今天再拉日志。
    const today = formatLocalISODate(new Date());
    if (logsFollowTodayRef.current && (logsFrom !== today || logsTo !== today)) {
      setLogsFrom(today);
      setLogsTo(today);
      // setState 异步，下面的缓存 key / refresh 必须用 today，不能读旧的 logsFrom。
    }
    const effectiveFrom = logsFollowTodayRef.current ? today : logsFrom;
    const effectiveTo = logsFollowTodayRef.current ? today : logsTo;
    // 筛选条件 / 进入页面：回到第 1 页并拉一次。故意不依赖 logsPage——翻页只由
    // 按钮调用 refreshLogs(n)，避免 setLogsPage → effect → 再拉 的循环。
    logsSnapshotBeforeIdRef.current = 0;
    logsSnapshotBeforeTimeRef.current = '';
    const scope = uiCacheScope(authStatusRef.current);
    const kind = `logs:p1:s${logsStatusFilter}:f${effectiveFrom}:t${effectiveTo}:k${logsApiKeyName.trim()}:pv${logsProviderFilter}:u${logsOwnerFilter}:target${logsUsageTarget?.type || ''}:${logsUsageTarget?.value || ''}`;
    const cached = readUICache<{ items: LogEntry[]; total: number; page: number; fetchedAt?: string }>(scope, kind);
    if (cached?.items?.length) {
      setLogs(cached.items);
      setLogsTotal(cached.total || cached.items.length);
      setLogsFetchedOnce(true);
      if (cached.fetchedAt) {
        const at = new Date(cached.fetchedAt);
        if (!Number.isNaN(at.getTime())) setDataFetchedAt(at);
      }
    }
    if (logsPageRef.current !== 1) setLogsPage(1);
    void refreshLogs(1, effectiveFrom, effectiveTo, { silent: Boolean(cached?.items?.length) });
  }, [activeNav, logsStatusFilter, logsFrom, logsTo, logsApiKeyName, logsProviderFilter, logsOwnerFilter, logsUsageTarget, logsPageSize]);

  // 仅第 1 页自动轮询；翻到第 2 页及以后立刻停表，绝不自动刷新。
  // 第 1 页走增量追加（见 pollNewLogs）：新行插到表头，已有的行原地不动。
  //
  // 这里刻意不做「页面是否可见/是否有焦点」的判断：document.hasFocus() 在副屏、
  // 内嵌 Electron 浏览器里恒为 false，visibilityState 也可能在窗口被判定遮挡时
  // 变 hidden（线上实测：页面明明开着，45 秒里一个 /__logs 都没发），实时日志
  // 于是整个停摆，看着和没做实时一样。这条请求便宜（本地 SQLite 索引查询），
  // 后台标签页浏览器本来就会把定时器节流到 1 次/分钟，不值得为省这点算力
  // 再赌一个不可靠的环境信号。
  useEffect(() => {
    if (activeNav !== 'traffic-tokens') return;
    if (logsPage !== 1) return;
    const tick = () => {
      if (logsPageRef.current !== 1) return;
      void pollNewLogs();
    };
    const timer = window.setInterval(tick, LOGS_LIVE_POLL_MS);
    // 切回本页立刻补一次，不用等下一个 5 秒周期，也补上后台期间空过的窗口。
    window.addEventListener('focus', tick);
    document.addEventListener('visibilitychange', tick);
    return () => {
      window.clearInterval(timer);
      window.removeEventListener('focus', tick);
      document.removeEventListener('visibilitychange', tick);
    };
  }, [activeNav, logsPage, logsStatusFilter, logsFrom, logsTo, logsApiKeyName, logsProviderFilter, logsOwnerFilter, logsUsageTarget]);

  // 表里的行同步进 ref：增量轮询按它去重，不用等 React 状态落地。
  useEffect(() => {
    logsItemsRef.current = logs;
  }, [logs]);

  // 新行插到表头后补回滚动量：用户往下翻着看时，视线钉在原来那一行上。
  useLayoutEffect(() => {
    const anchor = logsAnchorRef.current;
    logsAnchorRef.current = null;
    if (!anchor) return;
    const scroller = logsTableRef.current?.querySelector<HTMLElement>('.tlog-scroll');
    if (!scroller) return;
    const row = [...scroller.querySelectorAll<HTMLElement>('.tlog-row')]
      .find((el) => el.dataset.logKey === anchor.key);
    if (!row) return;
    const delta = row.getBoundingClientRect().top - anchor.top;
    if (Math.abs(delta) >= 1) scroller.scrollTop += delta;
  }, [logs]);

  // 筛选项（后端也只对 admin 生效），不需要拉取。
  useEffect(() => {
    if (activeNav !== 'traffic-tokens') return;
    if (authStatus?.role === 'user') return;
    void refreshConsoleUsers();
  }, [activeNav, authStatus?.role]);


  useEffect(() => {
    if (activeNav !== 'users') return;
    void refreshConsoleUsers();
  }, [activeNav]);

  // 管理员打开密钥页时加载用户列表，用于「所属用户」下拉
  useEffect(() => {
    if (activeNav !== 'api-keys') return;
    if (authStatus?.role === 'user') return;
    void refreshConsoleUsers();
  }, [activeNav, authStatus?.role]);

  // 管理员打开输入 Provider 页时加载用户列表，用于 Provider 卡片上的
  // 「N 个用户」权限徽标与用户权限弹窗；普通用户无权限也无需拉取。
  useEffect(() => {
    if (activeNav !== 'input-providers') return;
    if (authStatus?.role === 'user') return;
    void refreshConsoleUsers();
  }, [activeNav, authStatus?.role]);

  // 访问无权限的页面时跳回第一个可见页（不再硬编码 API 密钥页）
  useEffect(() => {
    if (!authStatus?.authenticated) return;
    if (navVisibleFor(authStatus.permissions, activeNav, isLegacyUserAuth(authStatus))) return;
    const fallbackNav = firstVisibleNavID(authStatus);
    window.history.replaceState({}, '', navPathForID(fallbackNav));
    setActiveNav(fallbackNav);
  }, [authStatus, activeNav]);


  function goToPage(sectionID: NavItemID) {
    const path = navPathForID(sectionID);
    if (window.location.pathname !== path) {
      window.history.pushState({ nav: sectionID }, '', path);
    }
    setActiveNav(sectionID);
    // Scroll the main content back to top when switching pages.
    document.querySelector('.main')?.scrollTo({ top: 0, behavior: 'auto' });
    window.scrollTo({ top: 0, behavior: 'auto' });
  }

  async function refreshAuthStatus(): Promise<AdminAuthStatus | null> {
    try {
      const response = await fetchWithTimeout(`${API_BASE}/__auth/status`, { credentials: 'same-origin' });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const data = await response.json() as AdminAuthStatus;
      setAuthStatus(data);
      if (data.authenticated || data.localBypass || !data.requireAuth) {
        writeUICache('session', 'auth', data);
      } else {
        clearUICache('session', 'auth');
      }
      return data;
    } catch {
      setAuthStatus(null);
      return null;
    }
  }

  async function bootstrapAuthenticatedSession(auth?: AdminAuthStatus | null) {
    const role = auth?.role ?? authStatus?.role;
    if (role === 'user') {
      // 普通用户仅需 state（自己的 Key）与用量数据，其余接口无权限
      await Promise.all([
        refreshState(false),
        refreshRequestStats(),
      ]);
      return;
    }
    await Promise.all([
      refreshState(false),
      refreshRequestStats(),
      refreshAppLogs(),
      refreshCloudflareAuthStatus(),
    ]);
  }

  async function submitAdminAuth(mode: 'setup' | 'login') {
    setAuthBusy(true);
    setAuthError('');
    try {
      if (authPassword.length < 8) {
        setAuthError('密码至少 8 位');
        return;
      }
      if (mode === 'setup' && authPassword !== authPasswordConfirm) {
        setAuthError('两次输入的密码不一致');
        return;
      }
      const response = await fetch(`${API_BASE}/__auth/${mode}`, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(mode === 'setup'
          ? { password: authPassword }
          : { username: authUsername.trim(), password: authPassword, trustDevice: authTrustDevice }),
      });
      if (!response.ok) {
        throw new Error(await authRejectionMessage(response, mode === 'setup' ? '设置密码失败' : '登录失败'));
      }
      const data = await response.json() as AdminAuthStatus;
      setAuthStatus(data);
      writeUICache('session', 'auth', data);
      setAuthPassword('');
      setAuthPasswordConfirm('');
      const landing = firstVisibleNavID(data);
      const currentNav = navIDFromPath(window.location.pathname);
      if (window.location.pathname === '/login') {
        window.history.replaceState({}, '', navPathForID(landing));
        setActiveNav(landing);
      } else if (!navVisibleFor(data.permissions, currentNav, isLegacyUserAuth(data))) {
        window.history.replaceState({}, '', navPathForID(landing));
        setActiveNav(landing);
      }
      await bootstrapAuthenticatedSession(data);
      setStateHydrated(true);
      showToast(mode === 'setup' ? '管理员密码已设置' : '登录成功');
    } catch (error) {
      setAuthError(String(error));
    } finally {
      setAuthBusy(false);
    }
  }

  async function logoutAdmin() {
    if (!window.confirm('确定退出登录？')) return;
    setAuthBusy(true);
    try {
      await fetch(`${API_BASE}/__auth/logout`, { method: 'POST', credentials: 'same-origin' });
      clearUICache('session', 'auth');
      setAuthStatus((current) => current ? { ...current, authenticated: false } : {
        configured: true,
        authenticated: false,
        requireAuth: true,
        localBypass: false,
      });
      setAuthPassword('');
      setAuthPasswordConfirm('');
      window.history.replaceState({}, '', '/login');
      showToast('已退出登录');
    } finally {
      setAuthBusy(false);
    }
  }

  async function updateAdminPassword() {
    setAdminPasswordBusy(true);
    try {
      if (adminNewPassword.length < 8) {
        showToast('新密码至少 8 位');
        return;
      }
      if (adminNewPassword !== adminNewPasswordConfirm) {
        showToast('两次输入的新密码不一致');
        return;
      }
      const response = await fetch(`${API_BASE}/__auth/password`, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          currentPassword: adminCurrentPassword,
          newPassword: adminNewPassword,
        }),
      });
      if (!response.ok) throw new Error(await authRejectionMessage(response, '更新管理密码失败'));
      const data = await response.json() as AdminAuthStatus;
      setAuthStatus(data);
      setAdminCurrentPassword('');
      setAdminNewPassword('');
      setAdminNewPasswordConfirm('');
      showToast(data.configured ? '管理密码已更新' : '管理密码已设置');
    } catch (error) {
      showToast(`更新管理密码失败：${String(error)}`);
    } finally {
      setAdminPasswordBusy(false);
    }
  }

  async function refreshConsoleUsers() {
    try {
      const response = await fetch(`${API_BASE}/__users`, { credentials: 'same-origin' });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      setConsoleUsers(await response.json() as ConsoleUser[]);
    } catch {
      // 无 users.view 权限或后端异常时静默：owner 名字退回 id 显示
    }
  }

  /** 探测成功：立即恢复「已连接」并清零失败计数。 */
  function markBackendReachable() {
    backendFailStreakRef.current = 0;
    setBackendConnected(true);
  }

  /** 探测失败：累计连续失败，达到阈值才翻红，避免单次抖动误报。 */
  function markBackendUnreachable() {
    backendFailStreakRef.current += 1;
    if (backendFailStreakRef.current >= BACKEND_FAIL_STREAK_LIMIT) {
      setBackendConnected(false);
    }
  }

  async function refreshBackendHealth() {
    try {
      const response = await fetchWithTimeout(`${API_BASE}/__health`);
      const body = await response.text();
      const connected = response.ok && body.includes('"status":"ok"');
      if (connected) {
        markBackendReachable();
      } else {
        markBackendUnreachable();
      }
      return connected;
    } catch {
      markBackendUnreachable();
      return false;
    }
  }

  async function reconnectBackend(showFeedback = true) {
    if (backendReconnecting) return false;
    setBackendReconnecting(true);
    try {
      const connected = await refreshBackendHealth();
      if (connected) {
        await Promise.all([
          refreshState(false),
          refreshRequestStats(),
          ...(activeNav === 'traffic-tokens' && logsPageRef.current === 1 ? [refreshLogs(1, undefined, undefined, { silent: true })] : []),
        ]);
        if (showFeedback) showToast('后端已重新连接');
        return true;
      }
      if (showFeedback) showToast('后端仍未连接，请确认已运行 cd web && npm run dev');
      return false;
    } finally {
      setBackendReconnecting(false);
    }
  }

  async function refreshState(toast = true) {
    setLoading(true);
    try {
      const response = await fetchWithTimeout(`${API_BASE}/__state`, { credentials: 'same-origin' });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const data = await response.json() as GatewayState;
      const normalized = normalizeGatewayState(data);
      setState((current) => normalizeGatewayState(data, current));
      setServerTimeZone(data.serverTimeZone || '');
      setSelectedRouteID((current) => current || data.routes?.[0]?.id || '');
      setSelectedProviderID((current) => current || data.providers?.[0]?.id || '');
      setSelectedOutputProtocol((current) => data.routes?.[0]?.outputProtocol || current);
      if (data.requestLogRetentionDays && data.requestLogRetentionDays > 0) {
        setRequestLogRetentionDays(data.requestLogRetentionDays);
      }
      setLog2xxBodies(data.log2xxBodies === true);
      if (data.usageBatchMaxSize && data.usageBatchMaxSize > 0) setUsageBatchMaxSize(data.usageBatchMaxSize);
      if (data.usageBatchMaxWaitSeconds && data.usageBatchMaxWaitSeconds > 0) setUsageBatchMaxWaitSeconds(data.usageBatchMaxWaitSeconds);
      if (data.usageBatchMaxSize && data.usageBatchMaxWaitSeconds) {
        confirmedUsageBatch.current = { size: data.usageBatchMaxSize, wait: data.usageBatchMaxWaitSeconds };
      }
      markBackendReachable();
      setDataFetchedAt(new Date());
      writeUICache(uiCacheScope(authStatusRef.current), 'state', normalized);
      if (toast) showToast('已刷新后端状态和模型列表');
      return true;
    } catch (error) {
      markBackendUnreachable();
      if (toast) showToast(`后端未连接：${String(error)}。请在项目根目录运行 cd web && npm run dev，或单独执行 npm run gateway。`);
      return false;
    } finally {
      setLoading(false);
    }
  }

  function buildLogsQueryParams(page: number, includeBodies = false, fromOverride?: string, toOverride?: string, apiKeyNameOverride?: string) {
    const params = new URLSearchParams();
    params.set('page', String(page));
    params.set('pageSize', String(pageSizeRef.current));
    params.set('status', logsStatusFilter);
    if (includeBodies) params.set('includeBodies', '1');
    // 日历选完立即刷新时 state 尚未生效，需显式传入新区间
    const from = fromOverride !== undefined ? fromOverride : logsFrom;
    const to = toOverride !== undefined ? toOverride : logsTo;
    // 日期按网关时区补齐偏移：纯日期后端按 time.Local 解析，带浏览器偏移会把日界挪走。
    if (from) params.set('from', localDateToRFC3339(from, false, serverTimeZone));
    if (to) params.set('to', localDateToRFC3339(to, true, serverTimeZone));
    const keyName = apiKeyNameOverride !== undefined ? apiKeyNameOverride.trim() : logsApiKeyName.trim();
    if (keyName) params.set('apiKeyName', keyName);
    if (logsUsageTarget) params.set(logsUsageTarget.type === 'model' ? 'model' : 'apiKeyId', logsUsageTarget.value);
    if (logsProviderFilter) params.set('providerId', logsProviderFilter);
    if (logsSessionFilter) params.set('sessionId', logsSessionFilter);
    if (logsOwnerFilter) params.set('ownerUserId', logsOwnerFilter);
    // 第 1 页保持实时；第 2 页及以后带上离开第 1 页时冻结的 newest time+id。
    if (page > 1) {
      if (logsSnapshotBeforeTimeRef.current) params.set('beforeTime', logsSnapshotBeforeTimeRef.current);
      if (logsSnapshotBeforeIdRef.current > 0) params.set('beforeId', String(logsSnapshotBeforeIdRef.current));
    }
    return params;
  }

  function trafficLogMatchKey(log: LogEntry) {
    return `${log.time}|${log.path}|${log.status}|${log.model}|${log.latencyMs}`;
  }

  async function refreshLogs(page = logsPageRef.current, fromOverride?: string, toOverride?: string, opts?: { silent?: boolean; apiKeyName?: string }) {
    const targetPage = page < 1 ? 1 : page;
    // 已离开第 1 页时，静默轮询连 gen 都不要递增，否则会把正在进行的翻页请求判成过期而丢弃。
    if (opts?.silent && targetPage === 1 && logsPageRef.current !== 1) return;
    const fetchGen = ++logsFetchGenRef.current;
    // 维护分页快照：回到第 1 页清空；首次离开第 1 页时用当前列表最新一条冻结 time+id。
    if (targetPage <= 1) {
      logsSnapshotBeforeIdRef.current = 0;
      logsSnapshotBeforeTimeRef.current = '';
    } else if (!logsSnapshotBeforeTimeRef.current && logsSnapshotBeforeIdRef.current === 0) {
      const newest = logs[0];
      if (newest?.time) logsSnapshotBeforeTimeRef.current = newest.time;
      if (typeof newest?.id === 'number' && newest.id > 0) logsSnapshotBeforeIdRef.current = newest.id;
    }
    if (!opts?.silent) setLogsLoading(true);
    try {
      const response = await fetch(`${API_BASE}/__logs?${buildLogsQueryParams(targetPage, false, fromOverride, toOverride, opts?.apiKeyName).toString()}`, { credentials: 'same-origin' });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      // 过期响应直接丢弃：翻页与第 1 页轮询并发时，旧的 page=1 结果不能把页码打回去。
      if (fetchGen !== logsFetchGenRef.current) return;
      if (opts?.silent && logsPageRef.current !== targetPage) return;
      const data = await response.json() as LogPage | LogEntry[];
      if (fetchGen !== logsFetchGenRef.current) return;
      let items: LogEntry[] = [];
      let total = 0;
      let nextPage = targetPage;
      if (Array.isArray(data)) {
        items = data;
        total = data.length;
        nextPage = 1;
      } else {
        items = data.items || [];
        total = data.total || 0;
        nextPage = data.page || targetPage;
      }
      setLogs(items);
      setLogsTotal(total);
      setLogsPage((current) => (current === nextPage ? current : nextPage));
      logsPageRef.current = nextPage;
      logsTotalRef.current = total;
      setLogsFetchedOnce(true);
      const fetchedAt = new Date();
      setDataFetchedAt(fetchedAt);
      const from = fromOverride !== undefined ? fromOverride : logsFrom;
      const to = toOverride !== undefined ? toOverride : logsTo;
      const keyForCache = (opts?.apiKeyName !== undefined ? opts.apiKeyName : logsApiKeyName).trim();
      writeUICache(uiCacheScope(authStatusRef.current), `logs:p${nextPage}:s${logsStatusFilter}:f${from}:t${to}:k${keyForCache}:pv${logsProviderFilter}:u${logsOwnerFilter}:target${logsUsageTarget?.type || ''}:${logsUsageTarget?.value || ''}`, {
        items,
        total,
        page: nextPage,
        fetchedAt: fetchedAt.toISOString(),
      });
    } catch {
      // Keep UI usable when backend is down.
      if (fetchGen === logsFetchGenRef.current) setLogsFetchedOnce(true);
    } finally {
      // 被判过期的请求也要收掉 loading：并发请求互相作废时若都跳过这里，
      // 页面就永远停在「加载流量日志中」。
      if (!opts?.silent) setLogsLoading(false);
    }
  }

  /**
   * 第 1 页增量轮询：拉最新一屏，只把排在「当前表头那一行」之前的行插到表头。
   *
   * 不整段替换 = 已有行不重建、不闪；用户往下翻时按锚点行补回滚动量，
   * 新行从上方进来而视线不动。整表替换曾把每一行都往下顶一格，
   * layout-shift 里能量到整表位移，读日志的人看到的就是「频繁闪动」。
   */
  async function pollNewLogs(fromOverride?: string, toOverride?: string) {
    if (logsPageRef.current !== 1) return;
    const gen = logsFetchGenRef.current;
    const params = buildLogsQueryParams(1, false, fromOverride, toOverride);
    // 多要一些：页面矮时一屏只有 5~10 行，一次来了几条也能全部接住。
    params.set('pageSize', String(Math.max(pageSizeRef.current, 25)));
    try {
      const response = await fetch(`${API_BASE}/__logs?${params.toString()}`, { credentials: 'same-origin' });
      if (!response.ok) return;
      if (gen !== logsFetchGenRef.current || logsPageRef.current !== 1) return;
      const data = await response.json() as LogPage | LogEntry[];
      const items = Array.isArray(data) ? data : (data.items || []);
      const total = Array.isArray(data) ? data.length : (data.total || 0);
      const current = logsItemsRef.current;
      // 首屏还没落表（或表还是空的）：直接整页拉一次，省掉合并。
      if (current.length === 0) {
        if (items.length > 0) void refreshLogs(1, fromOverride, toOverride, { silent: true });
        return;
      }
      // 新行 = 排在「当前表头那一行」之前的那些。不能用「不在表里」判断：
      // 多要的窗口（25 行 > 一屏）会带出当前页之外的老行，那些不能插到表头。
      const topKey = trafficLogRowKey(current[0]);
      const cut = items.findIndex((item) => trafficLogRowKey(item) === topKey);
      const fresh = cut === 0 ? [] : (cut > 0 ? items.slice(0, cut) : items);
      if (fresh.length === 0) {
        if (total !== logsTotalRef.current) {
          logsTotalRef.current = total;
          setLogsTotal(total);
        }
        return;
      }
      // 用户已经往下翻了：记住第一行可见行，插入后把滚动量补回去。
      const scroller = logsTableRef.current?.querySelector<HTMLElement>('.tlog-scroll');
      const scrolledAway = Boolean(scroller && scroller.scrollTop > 4);
      if (scroller && scrolledAway) {
        const edge = scroller.getBoundingClientRect().top;
        const row = [...scroller.querySelectorAll<HTMLElement>('.tlog-row')]
          .find((el) => el.getBoundingClientRect().bottom > edge + 1);
        if (row?.dataset.logKey) {
          logsAnchorRef.current = { key: row.dataset.logKey, top: row.getBoundingClientRect().top };
        }
      }
      logsTotalRef.current = total;
      setLogsTotal(total);
      // 新行挂上进场动画，跑完再清空，之后的重渲染不会再演一遍。
      // 时长要对齐 CSS 里最长那条（错峰延迟 220ms + 强调色 1.6s），提前摘类会把尾巴切掉。
      // 往下翻着看时不开动画：行高从 0 撑开会先让锚点行抖一下再归位。
      if (!scrolledAway) {
        setLogsFreshKeys(new Set(fresh.map(trafficLogRowKey)));
        window.clearTimeout(logsFreshTimerRef.current);
        logsFreshTimerRef.current = window.setTimeout(() => {
          setLogsFreshKeys((current) => (current.size === 0 ? current : new Set()));
        }, 2000);
      }
      // 函数式更新兜住并发：两次轮询同时在途时，后到的那次不会把同一行插两遍。
      setLogs((prev) => {
        const seen = new Set(prev.map(trafficLogRowKey));
        const extra = fresh.filter((item) => !seen.has(trafficLogRowKey(item)));
        if (extra.length === 0) {
          logsAnchorRef.current = null;
          return prev;
        }
        return [...extra, ...prev].slice(0, pageSizeRef.current);
      });
      setDataFetchedAt(new Date());
    } catch {
      /* 轮询失败保持现状，不打扰用户 */
    }
  }

  async function openTrafficLogDetail(log: LogEntry) {
    setTrafficLogDetail(log);
    setTrafficLogDetailTab(log.requestBody || log.responseBody ? 'request' : 'apm');
    setTrafficLogRawReqView(false);
    setTrafficLogRawRespView(false);
    setTrafficLogSSEParsedView(true);
    setTrafficLogDetailLoading(true);
    try {
      if (log.id) {
        const response = await fetch(`${API_BASE}/__logs?id=${log.id}&includeBodies=1`, { credentials: 'same-origin' });
        if (response.ok) {
          const full = await response.json() as LogEntry;
          if (full && full.id === log.id) {
            setTrafficLogDetail(full);
            return;
          }
        }
      }
      const response = await fetch(`${API_BASE}/__logs?${buildLogsQueryParams(logsPage, true).toString()}`, { credentials: 'same-origin' });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const data = await response.json() as LogPage | LogEntry[];
      const items = Array.isArray(data) ? data : data.items || [];
      const matchKey = trafficLogMatchKey(log);
      const full = items.find((item) => (log.id ? item.id === log.id : trafficLogMatchKey(item) === matchKey));
      if (full) setTrafficLogDetail(full);
    } catch {
      // Summary-only detail is still useful when body fetch fails.
    } finally {
      setTrafficLogDetailLoading(false);
    }
  }

  async function updateRequestLogRetention(days: number) {
    setSaving(true);
    try {
      const response = await fetch(`${API_BASE}/__settings/request-log-retention`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ days }),
      });
      if (!response.ok) throw new Error(await response.text());
      const data = await response.json() as { requestLogRetentionDays: number };
      setRequestLogRetentionDays(data.requestLogRetentionDays);
      showToast(`请求日志保留天数已设为 ${data.requestLogRetentionDays} 天`);
    } catch (error) {
      showToast(`更新保留天数失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  async function updateLog2xxBodies(enabled: boolean): Promise<boolean> {
    setSaving(true);
    try {
      const response = await fetch(`${API_BASE}/__settings/log-2xx-bodies`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ enabled }),
      });
      if (!response.ok) throw new Error(await response.text());
      const data = await response.json() as { log2xxBodies: boolean };
      setLog2xxBodies(data.log2xxBodies);
      showToast(data.log2xxBodies ? '已开启记录成功请求正文（写盘量会显著上升）' : '已关闭记录成功请求正文');
      return true;
    } catch (error) {
      showToast(`更新失败：${String(error)}`);
      return false;
    } finally {
      setSaving(false);
    }
  }

  // confirmedUsageBatch 记住服务端确认过的攒批设置：blur 没有改动时不再发请求。
  const confirmedUsageBatch = useRef<{ size: number; wait: number }>({ size: 0, wait: 0 });
  async function updateUsageBatch(maxSize: number, maxWaitSeconds: number): Promise<boolean> {
    // 值没变就别 PATCH：只切一下标签页、把光标移进移出都会触发 blur。
    if (maxSize === confirmedUsageBatch.current.size && maxWaitSeconds === confirmedUsageBatch.current.wait) return true;
    setSaving(true);
    try {
      const response = await fetch(`${API_BASE}/__settings/usage-batch`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ maxSize, maxWaitSeconds }),
      });
      if (!response.ok) throw new Error(await response.text());
      const data = await response.json() as { usageBatchMaxSize: number; usageBatchMaxWaitSeconds: number };
      setUsageBatchMaxSize(data.usageBatchMaxSize);
      setUsageBatchMaxWaitSeconds(data.usageBatchMaxWaitSeconds);
      confirmedUsageBatch.current = { size: data.usageBatchMaxSize, wait: data.usageBatchMaxWaitSeconds };
      showToast(`用量统计攒批已设为 ${data.usageBatchMaxSize} 条 / ${data.usageBatchMaxWaitSeconds} 秒（重启网关后生效）`);
      return true;
    } catch (error) {
      showToast(`更新失败：${String(error)}`);
      return false;
    } finally {
      setSaving(false);
    }
  }

  async function refreshRequestStats(from = usageRangeRef.current.from, to = usageRangeRef.current.to, opts?: { silent?: boolean }) {
    const version = ++usageRequestVersion.current;
    const scope = uiCacheScope(authStatusRef.current);
    if (!opts?.silent) {
      setUsageLoading(true);
      setUsageError('');
    }
    try {
      // 容错：开始时间晚于结束时间时自动交换，避免后端返回空区间
      if (from && to && from > to) [from, to] = [to, from];
      const params = new URLSearchParams();
      // 日期按网关时区补齐偏移：纯日期后端按 time.Local 解析，带浏览器偏移会把日界挪走。
      if (from) params.set('from', localDateToRFC3339(from, false, serverTimeZone));
      if (to) params.set('to', localDateToRFC3339(to, true, serverTimeZone));
      const response = await fetch(`${API_BASE}/__request-stats?${params.toString()}`);
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const snapshot = normalizeRequestStats(await response.json() as RequestStatsSnapshot | LegacyRequestStatsSnapshot);
      if (version !== usageRequestVersion.current || scope !== uiCacheScope(authStatusRef.current)) return;
      setRequestStats(snapshot);
      setDataFetchedAt(new Date());
      if (snapshot) {
        writeUICache(scope, `stats:${from}:${to}`, snapshot);
      }
    } catch {
      if (!opts?.silent && version === usageRequestVersion.current && scope === uiCacheScope(authStatusRef.current)) setUsageError('用量刷新失败，请稍后重试。');
    } finally {
      if (!opts?.silent && version === usageRequestVersion.current) setUsageLoading(false);
    }
  }

  /** 拉取网关侧 RTK 压缩节省统计；days=0 表示全部历史。 */
  async function refreshRTKStats(days = rtkStatsDays) {
    const version = ++rtkRequestVersion.current;
    try {
      const response = await fetch(`${API_BASE}/__rtk/stats?days=${days}`, { credentials: 'same-origin' });
      if (!response.ok) return;
      const data = await response.json() as RTKStatsResponse;
      if (version === rtkRequestVersion.current) setRtkStats(data);
    } catch {
      // Keep UI usable when backend is down.
    }
  }

  async function refreshAppLogs() {
    try {
      const response = await fetch(`${API_BASE}/__app/logs`);
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const data = await response.json() as { level: string; logs: AppLogEntry[] };
      setLogLevel(data.level || 'info');
      setAppLogs(data.logs || []);
    } catch {
      // Keep UI usable when backend is down.
    }
  }

  async function updateLogLevel(level: string) {
    setSaving(true);
    try {
      const response = await fetch(`${API_BASE}/__app/log-level`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ level }),
      });
      if (!response.ok) throw new Error(await response.text());
      const data = await response.json() as { level: string };
      setLogLevel(data.level);
      await refreshAppLogs();
      showToast(`日志级别已切换为：${data.level}`);
    } catch (error) {
      showToast(`切换日志级别失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  async function refreshCloudflareZones(preferredRoot = customDomainRoot) {
    try {
      const response = await fetch(`${API_BASE}/__public/cloudflare/zones`);
      if (!response.ok) {
        setCloudflareZones([]);
        return [] as CloudflareZoneOption[];
      }
      const data = await response.json() as { authorized?: boolean; zones?: CloudflareZoneOption[] };
      const zones = Array.isArray(data.zones) ? data.zones.filter((zone) => zone?.name) : [];
      setCloudflareZones(zones);
      setCloudflareAuthorized(Boolean(data.authorized) || zones.length > 0);
      const nextRoot = pickCustomDomainRoot(preferredRoot, zones);
      if (nextRoot) setCustomDomainRoot(nextRoot);
      return zones;
    } catch {
      setCloudflareZones([]);
      return [] as CloudflareZoneOption[];
    }
  }

  async function refreshCloudflareAuthStatus() {
    try {
      const response = await fetch(`${API_BASE}/__public/cloudflare/login/status`);
      if (!response.ok) return false;
      const data = await response.json() as { authorized?: boolean };
      const authorized = Boolean(data.authorized);
      setCloudflareAuthorized(authorized);
      if (authorized) {
        await refreshCloudflareZones();
      } else {
        setCloudflareZones([]);
      }
      return authorized;
    } catch {
      return false;
    }
  }

  function stopCloudflareLoginPoll() {
    // 轮询的字面定时器归 runCloudflareAuthPoll 管，停它要连「正在飞的那一次检查
    // 回来后别再装定时器」一起停掉，所以先停驱动再清兜底的 id。
    cloudflarePoll?.stop();
    cloudflarePoll = null;
    if (cloudflarePollRef.current != null) {
      window.clearInterval(cloudflarePollRef.current);
      cloudflarePollRef.current = null;
    }
  }

  async function waitForCloudflareAuthorization(timeoutMs = 10 * 60 * 1000) {
    return new Promise<boolean>((resolve) => {
      stopCloudflareLoginPoll();
      // 先查一次，仍在等待才装定时器（runCloudflareAuthPoll）：已授权的控制台
      // 一次就结束，不会留下每 2 秒打一次 status + zones 的那只定时器。
      const poll = runCloudflareAuthPoll({
        timeoutMs,
        now: () => Date.now(),
        checkStatus: refreshCloudflareAuthStatus,
        installTimer: (tick) => window.setInterval(tick, 2000),
        clearTimer: (id) => window.clearInterval(id),
        onSettled: (authorized) => {
          cloudflarePollRef.current = null;
          setCloudflareAuthPending(false);
          resolve(authorized);
        },
      });
      cloudflarePoll = poll;
    });
  }

  async function connectCloudflareAndBind(options?: { exposeApi?: boolean; exposeUi?: boolean }) {
    const exposeApi = options?.exposeApi ?? (publicDraft.exposeApi !== false);
    const exposeUi = options?.exposeUi ?? (publicDraft.exposeUi !== false);
    if (!exposeApi && !exposeUi) {
      showToast('请至少开启「模型 API 公网」或「管理页公网」之一');
      return;
    }
    setTunnelBusy(true);
    setCloudflareAuthPending(true);
    try {
      let authorized = await refreshCloudflareAuthStatus();
      if (!authorized) {
        const response = await fetch(`${API_BASE}/__public/cloudflare/login/start`, { method: 'POST' });
        if (!response.ok) throw new Error(await response.text());
        const data = await response.json() as { loginUrl?: string };
        const loginUrl = data.loginUrl || 'https://dash.cloudflare.com/argotunnel';
        window.open(loginUrl, '_blank', 'noopener,noreferrer');
        showToast('已打开 Cloudflare 授权页，请在浏览器中登录并授权域名');
        authorized = await waitForCloudflareAuthorization();
        if (!authorized) {
          showToast('等待授权超时，请重试');
          return;
        }
        showToast('Cloudflare 授权成功，正在获取根域名…');
      }
      const zones = await refreshCloudflareZones(customDomainRoot);
      const root = pickCustomDomainRoot(customDomainRoot, zones);
      if (!root) {
        showToast('未获取到已授权的根域名，请重新授权 Cloudflare');
        return;
      }
      setCustomDomainRoot(root);
      const customDomain = exposeApi ? composeCustomDomain(customDomainPrefix, root) : '';
      const uiDomain = exposeUi
        ? (composeCustomDomain(uiDomainPrefix, root) || (customDomain ? deriveUIDomainFromAPI(customDomain) : ''))
        : '';
      if (exposeApi && !customDomain) {
        showToast('请填写 API 子域名前缀');
        return;
      }
      if (exposeUi && !uiDomain) {
        showToast('请填写管理页子域名前缀');
        return;
      }
      if (customDomain && uiDomain && customDomain.toLowerCase() === uiDomain.toLowerCase()) {
        showToast('API 域名与管理页域名不能相同');
        return;
      }
      showToast('正在绑定域名…');
      const bindResponse = await fetch(`${API_BASE}/__public/cloudflare/bind`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ customDomain, uiDomain, exposeApi, exposeUi }),
      });
      if (!bindResponse.ok) throw new Error(await bindResponse.text());
      const bindResult = await bindResponse.json() as { publicAccess?: PublicAccessSettings; error?: string };
      if (bindResult.publicAccess) {
        setState((current) => ({ ...current, publicAccess: bindResult.publicAccess! }));
        setPublicDraft((current) => ({ ...current, ...bindResult.publicAccess }));
      }
      await refreshState(false);
      await refreshAppLogs();
      if (bindResult.error) {
        showToast(`域名绑定失败：${bindResult.error}`);
        return;
      }
      const next = bindResult.publicAccess;
      const apiURL = next?.exposeApi ? (next.tunnel?.publicUrl || next.publicBaseUrl || '') : '';
      const uiURL = next?.exposeUi ? (next.tunnel?.uiPublicUrl || next.uiPublicBaseUrl || '') : '';
      if (next?.tunnel?.status === 'running' && (apiURL || uiURL)) {
        const parts = [
          apiURL ? `API ${apiURL}` : '',
          uiURL ? `管理页 ${uiURL}` : '',
        ].filter(Boolean);
        showToast(`域名隧道已开启：${parts.join(' / ')}`);
      } else if (next?.tunnel?.status === 'error') {
        showToast(`域名隧道启动失败：${next.tunnel.message}`);
      } else {
        showToast('Cloudflare 域名已绑定');
      }
    } catch (error) {
      showToast(`连接 Cloudflare 失败：${String(error)}`);
    } finally {
      setTunnelBusy(false);
      setCloudflareAuthPending(false);
      stopCloudflareLoginPoll();
    }
  }

  async function startQuickTunnel() {
    setTunnelBusy(true);
    try {
      const payload: PublicAccessSettings = {
        enabled: true,
        provider: 'cloudflare',
        mode: 'random_tunnel',
        expose: 'all',
        status: publicDraft.status,
        statusMessage: publicDraft.statusMessage,
      };
      const response = await fetch(`${API_BASE}/__public/start`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      });
      if (!response.ok) throw new Error(await response.text());
      const updated = await response.json() as PublicAccessSettings;
      setState((current) => ({ ...current, publicAccess: updated }));
      setPublicDraft((current) => ({ ...current, ...updated }));
      await refreshState(false);
      await refreshAppLogs();
      const nextTunnel = updated.tunnel;
      if (nextTunnel?.status === 'running' && nextTunnel.publicUrl) {
        showToast(`快速隧道已开启：${nextTunnel.publicUrl}`);
      } else if (nextTunnel?.status === 'error') {
        showToast(`开启快速隧道失败：${nextTunnel.message}`);
      } else {
        showToast(`隧道状态：${nextTunnel?.status || 'unknown'}`);
      }
    } catch (error) {
      showToast(`开启快速隧道失败：${String(error)}`);
    } finally {
      setTunnelBusy(false);
    }
  }

  async function startCustomDomainTunnel() {
    const root = customDomainRoot || pickCustomDomainRoot('', cloudflareZones);
    const exposeApi = publicDraft.exposeApi !== false;
    const exposeUi = publicDraft.exposeUi !== false;
    const customDomain = exposeApi ? composeCustomDomain(customDomainPrefix, root) : '';
    const uiDomain = exposeUi
      ? (composeCustomDomain(uiDomainPrefix, root) || (customDomain ? deriveUIDomainFromAPI(customDomain) : ''))
      : '';
    if (!exposeApi && !exposeUi) {
      showToast('请至少开启「模型 API 公网」或「管理页公网」之一');
      return;
    }
    if (exposeApi && !customDomain) {
      showToast('请先完成 Cloudflare 授权以获取根域名，或使用已保存的域名');
      return;
    }
    if (exposeUi && !uiDomain) {
      showToast('请填写管理页子域名前缀');
      return;
    }
    if (customDomain && uiDomain && customDomain.toLowerCase() === uiDomain.toLowerCase()) {
      showToast('API 域名与管理页域名必须不同');
      return;
    }
    const token = customTunnelToken.trim();
    if (!token && !publicDraft.tunnelToken) {
      showToast('请填写 Cloudflare Tunnel Token');
      return;
    }
    setTunnelBusy(true);
    try {
      const payload: PublicAccessSettings = {
        enabled: true,
        provider: 'cloudflare',
        mode: 'custom_domain',
        exposeApi,
        exposeUi,
        customDomain: customDomain || publicDraft.customDomain,
        uiDomain: uiDomain || publicDraft.uiDomain,
        tunnelToken: token || publicDraft.tunnelToken,
        expose: 'all',
        status: publicDraft.status,
        statusMessage: publicDraft.statusMessage,
      };
      const response = await fetch(`${API_BASE}/__public/start`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      });
      if (!response.ok) throw new Error(await response.text());
      const updated = await response.json() as PublicAccessSettings;
      setState((current) => ({ ...current, publicAccess: updated }));
      setPublicDraft((current) => ({ ...current, ...updated }));
      if (token) setCustomTunnelToken('');
      await refreshState(false);
      await refreshAppLogs();
      const nextTunnel = updated.tunnel;
      if (nextTunnel?.status === 'running' && nextTunnel.publicUrl) {
        showToast(`域名隧道已开启：${nextTunnel.publicUrl}`);
      } else if (nextTunnel?.status === 'error') {
        showToast(`域名隧道启动失败：${nextTunnel.message}`);
      } else {
        showToast(`隧道状态：${nextTunnel?.status || 'unknown'}`);
      }
    } catch (error) {
      showToast(`域名隧道启动失败：${String(error)}`);
    } finally {
      setTunnelBusy(false);
    }
  }

  async function stopPublicAccess() {
    setTunnelBusy(true);
    try {
      const response = await fetch(`${API_BASE}/__public/stop`, { method: 'POST' });
      if (!response.ok) throw new Error(await response.text());
      const updated = await response.json() as PublicAccessSettings;
      setState((current) => ({ ...current, publicAccess: updated }));
      setPublicDraft((current) => ({ ...current, ...updated }));
      await refreshState(false);
      await refreshAppLogs();
      showToast('公网隧道已停止');
    } catch (error) {
      showToast(`停止公网隧道失败：${String(error)}`);
    } finally {
      setTunnelBusy(false);
    }
  }

  async function refreshPublicControlTokenStatus() {
    try {
      const response = await fetch(`${API_BASE}/__public/control-token`, { credentials: 'same-origin' });
      if (!response.ok) return;
      const data = await response.json() as { configured?: boolean; preview?: string };
      setPublicControlTokenConfigured(!!data.configured);
      setPublicControlTokenPreview(data.preview || '');
    } catch {
      // 忽略瞬时错误：该面板不是关键路径，下次进页面会重试。
    }
  }

  // 生成/重置公网开关控制令牌；原始令牌只在这次响应里出现一次。
  async function generatePublicControlToken() {
    setPublicControlTokenBusy(true);
    try {
      const response = await fetch(`${API_BASE}/__public/control-token`, {
        method: 'POST',
        credentials: 'same-origin',
      });
      if (!response.ok) throw new Error(await response.text());
      const data = await response.json() as { token: string; preview?: string };
      setPublicControlRawToken(data.token);
      setPublicControlTokenConfigured(true);
      setPublicControlTokenPreview(data.preview || '');
      await refreshAppLogs();
      showToast('已生成公网控制令牌，请立即复制（刷新页面后不再显示）');
    } catch (error) {
      showToast(`生成公网控制令牌失败：${String(error)}`);
    } finally {
      setPublicControlTokenBusy(false);
    }
  }

  async function revokePublicControlToken() {
    if (!window.confirm('确定撤销公网控制令牌？已发出的令牌会立即失效，脚本 / LLM 将无法再远程开关公网访问。')) return;
    setPublicControlTokenBusy(true);
    try {
      const response = await fetch(`${API_BASE}/__public/control-token/revoke`, {
        method: 'POST',
        credentials: 'same-origin',
      });
      if (!response.ok) throw new Error(await response.text());
      setPublicControlRawToken('');
      setPublicControlTokenConfigured(false);
      setPublicControlTokenPreview('');
      await refreshAppLogs();
      showToast('已撤销公网控制令牌');
    } catch (error) {
      showToast(`撤销失败：${String(error)}`);
    } finally {
      setPublicControlTokenBusy(false);
    }
  }

  async function parseRouteTestResponse(response: Response): Promise<RouteTestResult> {
    const contentType = response.headers.get('content-type') || '';
    const text = await response.text();
    // 协议缺失也不再直接当 JSON 解析：有些反代会把它去掉，体仍是 JSON。
    if (contentType.includes('json') || /^\s*[[{]/.test(text)) {
      const verdict = chatTestVerdictFromBody(text, response.status);
      // 失败应答（权限不足、路由不存在……）是 {"error":{...}} 信封，没有 success /
      // status；补成完整的失败结果，弹窗与提示才说得清原因。
      return verdict.success
        ? { ...(JSON.parse(text) as RouteTestResult) }
        : { success: false, status: response.status, error: verdict.error };
    }
    return {
      success: false,
      status: response.status,
      error: `HTTP ${response.status} · ${contentType || '非 JSON 响应'}：${text.slice(0, 2000)}`,
    };
  }

  // 三项测试各自独立运行：主对话几秒就出结果，Cache 要跑两轮会话（上游慢时
  // 半分钟以上），Thinking 又是一次请求。以前点一次全部串起来跑，弹窗看起来像
  // 卡死了，还要靠叠三个弹窗收结果。
  async function runChatTest(test: ChatTestTab = 'main') {
    if (!chatTestContext) {
      showToast('请先选择测试对象');
      return;
    }
    const target = chatTestContext;
    const runKey = `${target.kind}:${target.id}:${test}`;
    let alreadyRunning = false;
    setChatTestingKeys((keys) => {
      if (keys.includes(runKey)) {
        alreadyRunning = true;
        return keys;
      }
      return [...keys, runKey];
    });
    if (alreadyRunning) {
      showToast('该项正在测试中');
      return;
    }
    const label = test === 'main' ? '对话测试' : test === 'cache' ? 'Cache 测试' : 'Thinking 测试';
    try {
      if (target.kind === 'route') {
        const response = await fetch(`${API_BASE}/__routes/${encodeURIComponent(target.id)}/test`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ model: chatTestModel.trim(), message: chatTestMessage.trim() }),
        });
        const result = await parseRouteTestResponse(response);
        setChatTestResult(result);
        showToast(chatTestToastText(result));
      } else {
        const providerPath = `${API_BASE}/__providers/${encodeURIComponent(target.id)}`;
        const path = test === 'main' ? 'chat-test' : test === 'cache' ? 'cache-test' : 'thinking-test';
        const body: Record<string, unknown> = {
          model: chatTestModel.trim(),
          systemPrompt: providerChatOptions.systemPrompt,
          userPrompt: providerChatOptions.userPrompt,
        };
        if (test === 'thinking') {
          body.thinkingField = providerChatOptions.thinkingField;
          body.thinkingValue = providerChatOptions.thinkingValue;
        }
        const response = await fetch(`${providerPath}/${path}`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
        });
        const result = await parseRouteTestResponse(response);
        if (test === 'main') setChatTestResult(result);
        else if (test === 'cache') setCacheTestResult(result as ProviderCacheTestResult);
        else setThinkingTestResult(result as ProviderThinkingTestResult);
        // Provider 自己的对话测试失败时同样可能只有一句话（而不是结果体）：统一取原因。
        const verdict = result.success ? result : chatTestVerdictFromBody(JSON.stringify(result), result.status || response.status);
        showToast(verdict.success ? `${label}成功：HTTP ${result.status}` : `${label}未通过：${chatTestToastText(verdict).replace(/^对话测试未通过：/, '')}`);
      }
      if (activeNav === 'traffic-tokens' && logsFetchedOnce && logsPageRef.current === 1) {
        await refreshLogs(1, undefined, undefined, { silent: true });
      }
      await Promise.all([refreshRequestStats(), refreshAppLogs()]);
    } catch (error) {
      if (test === 'main') setChatTestResult({ success: false, error: String(error) });
      showToast(`${label}失败：${String(error)}`);
    } finally {
      setChatTestingKeys((keys) => keys.filter((key) => key !== runKey));
    }
  }

  // 换模型或换 Provider 时清掉旧结果：留着上一次的答案最容易被误读成"这个模型也能回"。
  function clearChatTestResults() {
    setChatTestResult(null);
    setCacheTestResult(null);
    setThinkingTestResult(null);
  }

  function openChatTestForRoute(route: Route) {
    const provider = state.providers.find((item) => item.id === route.providerId);
    const models = provider ? state.models.filter((model) => model.providerId === provider.id) : [];
    const defaultModel = models[0]?.id || provider?.defaultModel || '';
    const boundKey = (state.apiKeys || []).find((key) => key.enabled && key.routeId === route.id);
    setChatTestContext({
      kind: 'route',
      id: route.id,
      title: `路由对话测试 · ${route.name}`,
      description: '测试网关转发后的输出接口（客户端实际调用的 URL）。',
      curlLabel: '网关 curl 预览',
      endpointLabel: 'gateway',
      hintLine: boundKey ? undefined : '客户端实际使用时需携带绑定该路由的 API 密钥；应用内测试会自动指定路由。',
    });
    setChatTestModel(defaultModel);
    setChatTestMessage('ping from UI');
    clearChatTestResults();
    setChatTestOpen(true);
  }

  function openChatTestForProvider(provider: Provider) {
    const models = state.models.filter((model) => model.providerId === provider.id);
    // 默认模型优先：没有默认时才退到列表第一个。两者都没有就留空，让弹窗提示
    // 先拉模型——带着空模型名发出去只会拿到一个没意义的 400。
    const defaultModel = provider.defaultModel || models[0]?.id || '';
    const thinkingPresets = thinkingPresetsForProtocol(provider.protocol);
    setChatTestContext({
      kind: 'provider',
      id: provider.id,
      title: `Provider 对话测试 · ${provider.name}`,
      description: '直连上游 Provider 的对话接口，验证 Provider 本身是否可用；Cache 与 Thinking 在下方分段里各跑各的。',
      curlLabel: '上游 curl 预览',
      endpointLabel: 'upstream',
    });
    setChatTestModel(defaultModel);
    setProviderChatOptions({
      ...defaultProviderChatTestOptions,
      thinkingField: thinkingPresets.defaultField,
      thinkingValue: defaultThinkingValueForField(provider.protocol, thinkingPresets.defaultField),
    });
    setProviderAuthPreview(null);
    setChatTestResult(null);
    setCacheTestResult(null);
    setThinkingTestResult(null);
    setChatTestOpen(true);
    if (provider.authType === 'claude_oauth' || provider.authType === 'cursor_oauth' || provider.authType === 'chatgpt_oauth' || isQoderConnectKind(provider.authType)) return;
    void fetch(`${API_BASE}/__providers/${encodeURIComponent(provider.id)}/auth-preview`)
      .then(async (response) => {
        if (!response.ok) return;
        setProviderAuthPreview(await response.json() as ProviderAuthPreview);
      })
      .catch(() => undefined);
  }

  async function refreshChatTestModels() {
    if (!chatTestContext) return;
    const providerID = chatTestContext.kind === 'provider'
      ? chatTestContext.id
      : state.routes.find((route) => route.id === chatTestContext.id)?.providerId;
    if (!providerID) {
      showToast('未找到关联 Provider');
      return;
    }
    const provider = state.providers.find((item) => item.id === providerID);
    if (!provider) {
      showToast('未找到关联 Provider');
      return;
    }
    await fetchProviderModels(provider.id, provider.name);
  }

  async function fetchProviderModels(providerID: string, providerName: string, openModal = false) {
    // 普通用户对自己拥有（或管理员授权可见）的 Provider 同样允许获取模型；
    // 后端 requireProviderOwnerForUser 会做所有权校验。
    if (openModal) {
      setProviderModelsOpen(true);
      setProviderModelsLoading(true);
      setProviderModelsID(providerID);
      setProviderModelsName(providerName);
      setProviderModelsResult(null);
    }
    setTestingProviderID(providerID);
    try {
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(providerID)}/test`, { method: 'POST' });
      if (!response.ok) throw new Error(await response.text());
      const result = await response.json() as ProviderTestResult;
      if (openModal) {
        setProviderModelsResult(result);
      } else {
        showToast(result.success ? `${providerName} 已获取 ${result.models.length} 个模型` : `获取模型失败：${result.error || result.status || 'unknown'}`);
      }
      await refreshState(false);
      await refreshAppLogs();
      // 用户主动点的动作：按最新请求量重排一次这一页的 Provider。
      refreshProviderRanking();
    } catch (error) {
      if (openModal) {
        setProviderModelsResult({ success: false, providerId: providerID, models: [], error: String(error) });
      } else {
        showToast(`获取模型失败：${String(error)}`);
      }
    } finally {
      setTestingProviderID('');
      if (openModal) setProviderModelsLoading(false);
    }
  }

  async function runProviderConformance(provider: Provider) {
    if (provider.authType === 'claude_oauth' || provider.authType === 'cursor_oauth' || provider.authType === 'chatgpt_oauth' || isQoderConnectKind(provider.authType)) {
      showToast('协议诊断仅支持 Bearer（api_key）自建上游');
      return;
    }
    setTestingProviderID(provider.id);
    try {
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(provider.id)}/conformance`, { method: 'POST' });
      if (!response.ok) throw new Error(await response.text());
      const report = await response.json() as {
        success?: boolean;
        passedRequired?: boolean;
        passedAll?: boolean;
        summary?: string;
        requiredPassed?: number;
        requiredTotal?: number;
        recommendedPassed?: number;
        recommendedTotal?: number;
      };
      const req = `${report.requiredPassed ?? 0}/${report.requiredTotal ?? 0}`;
      const rec = `${report.recommendedPassed ?? 0}/${report.recommendedTotal ?? 0}`;
      showToast(report.success
        ? `协议诊断通过（必过 ${req} · 建议 ${rec}）${report.passedAll ? ' · 含建议项' : ''}`
        : `协议诊断未通过：${report.summary || `必过 ${req}`}`);
    } catch (error) {
      showToast(`协议诊断失败：${String(error)}`);
    } finally {
      setTestingProviderID('');
    }
  }

  function parseClaudeOAuthInput(raw: string): { code: string; state: string } {
    const trimmed = raw.trim();
    if (!trimmed) return { code: '', state: '' };
    try {
      const url = new URL(trimmed);
      const code = url.searchParams.get('code')?.trim() || '';
      const state = url.searchParams.get('state')?.trim() || '';
      if (code) return { code, state };
    } catch {
      // not a full URL; fall through to code#state parsing
    }
    const hashIndex = trimmed.indexOf('#');
    if (hashIndex >= 0) {
      return { code: trimmed.slice(0, hashIndex).trim(), state: trimmed.slice(hashIndex + 1).trim() };
    }
    return { code: trimmed, state: '' };
  }

  function resetCursorOAuthFlowState() {
    setCursorOAuthBusy(false);
    setCursorOAuthError('');
    setCursorOAuthFlowId('');
    setCursorOAuthPolling(false);
  }

  function resetChatGPTOAuthFlowState() {
    setChatgptOAuthCode('');
    setChatgptOAuthBusy(false);
    setChatgptOAuthError('');
    setChatgptOAuthFlowId('');
    setChatgptOAuthPolling(false);
    setChatgptOAuthCallbackHint('');
    setChatgptOAuthCallbackBlocked('');
  }

  function resetQoderPATFlowState() {
    setQoderPatInput('');
    setQoderPatBusy(false);
    setQoderPatError('');
  }

  function resetWorkBuddyOAuthFlowState() {
    setWorkbuddyOAuthBusy(false);
    setWorkbuddyOAuthPolling(false);
    setWorkbuddyOAuthError('');
    setWorkbuddyOAuthUrl('');
  }

  function resetClaudeOAuthFlowState() {
    setClaudeOAuthState('');
    setClaudeOAuthCode('');
    setClaudeOAuthBusy(false);
    setClaudeOAuthError('');
    setClaudeOAuthFlowId('');
    setClaudeOAuthPolling(false);
    setClaudeOAuthCallbackHint('');
  }

  // 原始令牌只在生成瞬间返回一次；换 Provider / 关闭弹窗都要清掉内存里的值，
  // 避免残留在别的 Provider 编辑视图里被误用。
  function resetSelfRegistrationState() {
    setSelfRegToken('');
    setSelfRegBusy(false);
  }

  // 权限门：按钮已经按权限隐藏，这里再挡一层，防键盘/残留入口绕过界面。
  function denyAction(key: string): boolean {
    if (canDo(key)) return false;
    showToast('当前角色没有该操作权限');
    return true;
  }

  function openProviderModal() {
    if (denyAction('providers.create')) return;
    setEditingProviderID('');
    setProviderDraft({
      name: '我的 OpenAI 对话 Provider',
      protocol: 'openai_chat',
      baseUrl: 'https://example.com/v1/chat/completions',
      apiKeySource: '',
      defaultModel: '',
      defaultThinkingDepth: '',
      authType: 'api_key',
      requestAdapterJSON: '',
      teamOrganizationId: '',
      teamProjectId: '',
    });
    resetClaudeOAuthFlowState();
    resetCursorOAuthFlowState();
    resetChatGPTOAuthFlowState();
    resetQoderPATFlowState();
    resetWorkBuddyOAuthFlowState();
    resetSelfRegistrationState();
    setProviderModalOpen(true);
  }

  // 目录里选中一个预设：把它的连接方式 / 协议 / 接入地址灌进草稿，再进表单。
  // 订阅类的地址与协议由后端按连接方式固定，草稿不留占位地址。
  function openProviderDraftForPreset(preset: ProviderPreset) {
    setProviderSheetOpen(false);
    setEditingProviderID('');
    setProviderDraft({
      name: preset.name,
      protocol: (preset.protocol || 'openai_chat') as Protocol,
      baseUrl: preset.authType === 'api_key' ? (preset.baseUrl || 'https://example.com/v1/chat/completions') : (preset.baseUrl || ''),
      apiKeySource: '',
      defaultModel: '',
      defaultThinkingDepth: '',
      authType: preset.authType,
      requestAdapterJSON: '',
      teamOrganizationId: '',
      teamProjectId: '',
    });
    resetClaudeOAuthFlowState();
    resetCursorOAuthFlowState();
    resetChatGPTOAuthFlowState();
    resetQoderPATFlowState();
    resetWorkBuddyOAuthFlowState();
    resetSelfRegistrationState();
    setProviderModalOpen(true);
  }

  function resolveProviderAuthType(provider: Provider): ProviderConnectKind {
    return providerConnectKind(provider);
  }

  function openEditProviderModal(provider: Provider) {
    setEditingProviderID(provider.id);
    setProviderDraft({
      name: provider.name,
      protocol: provider.protocol,
      baseUrl: provider.baseUrl,
      apiKeySource: provider.apiKeySource,
      defaultModel: provider.defaultModel || '',
      defaultThinkingDepth: provider.defaultThinkingDepth || '',
      authType: resolveProviderAuthType(provider),
      requestAdapterJSON: provider.requestAdapter ? JSON.stringify({
        urlTemplate: provider.requestAdapter.urlTemplate || '',
        headers: provider.requestAdapter.headers || {},
        bodyTemplate: provider.requestAdapter.bodyTemplate || '',
        modelMapping: provider.requestAdapter.modelMapping || {},
      }, null, 2) : '',
      teamOrganizationId: provider.teamOrganizationId || '',
      teamProjectId: provider.teamProjectId || '',
    });
    resetClaudeOAuthFlowState();
    resetCursorOAuthFlowState();
    resetChatGPTOAuthFlowState();
    resetQoderPATFlowState();
    resetWorkBuddyOAuthFlowState();
    resetSelfRegistrationState();
    setProviderModalOpen(true);
  }

  function openCloneProviderModal(provider: Provider) {
    if (denyAction('providers.create')) return;
    // Clone opens the create flow (no editingProviderID) pre-filled from an
    // existing provider so the user can tweak and save it as a new provider.
    setEditingProviderID('');
    setProviderDraft({
      name: `${provider.name} Copy`,
      protocol: provider.protocol,
      baseUrl: provider.baseUrl,
      apiKeySource: provider.apiKeySource,
      defaultModel: provider.defaultModel || '',
      defaultThinkingDepth: provider.defaultThinkingDepth || '',
      authType: resolveProviderAuthType(provider),
      requestAdapterJSON: provider.requestAdapter ? JSON.stringify({
        urlTemplate: provider.requestAdapter.urlTemplate || '',
        headers: provider.requestAdapter.headers || {},
        bodyTemplate: provider.requestAdapter.bodyTemplate || '',
        modelMapping: provider.requestAdapter.modelMapping || {},
      }, null, 2) : '',
      teamOrganizationId: provider.teamOrganizationId || '',
      teamProjectId: provider.teamProjectId || '',
    });
    resetClaudeOAuthFlowState();
    resetCursorOAuthFlowState();
    resetChatGPTOAuthFlowState();
    resetQoderPATFlowState();
    resetWorkBuddyOAuthFlowState();
    resetSelfRegistrationState();
    setProviderModalOpen(true);
  }

  function openRouteModal() {
    setEditingRouteID('');
    setRouteDraft({
      name: '新建对话路由',
      providerId: selectedProvider?.id || state.providers[0]?.id || '',
      outputProtocol: 'openai_chat',
    });
    setRouteModalOpen(true);
  }

  function openEditRouteModal(route: Route) {
    setEditingRouteID(route.id);
    setRouteDraft({
      name: route.name,
      providerId: route.providerId,
      outputProtocol: route.outputProtocol,
    });
    setRouteModalOpen(true);
  }

  function openCloneRouteModal(route: Route) {
    setEditingRouteID('');
    setRouteDraft({
      name: `${route.name} Copy`,
      providerId: route.providerId,
      outputProtocol: route.outputProtocol,
    });
    setRouteModalOpen(true);
  }

  function openApiKeyModal() {
    if (denyAction('keys.create')) return;
    setApiKeyDraft({
      name: '新 API 密钥',
      providerId: selectedProvider?.id || state.providers[0]?.id || '',
      outputProtocol: selectedOutputProtocol || 'openai_chat',
      modelOverride: '',
      modelAliases: {},
      thinkingDepthOverride: '',
      maxOutputTokens: 0,
      streamEnabled: true,
      codexForceFast: false,
    });
    setApiKeyModalOpen(true);
  }

  function openCloneApiKeyModal(key: APIKey) {
    if (denyAction('keys.create')) return;
    const { binding } = getApiKeyBinding(key, state.routes, state.providers);
    setApiKeyDraft({
      name: `${key.name} Copy`,
      providerId: binding.providerId || state.providers[0]?.id || '',
      outputProtocol: binding.outputProtocol,
      modelOverride: key.modelOverride || '',
      modelAliases: { ...(key.modelAliases || {}) },
      thinkingDepthOverride: key.thinkingDepthOverride || '',
      maxOutputTokens: key.maxOutputTokens && key.maxOutputTokens > 0 ? key.maxOutputTokens : 0,
      streamEnabled: key.streamEnabled !== false,
      codexForceFast: key.codexForceFast === true,
    });
    setApiKeyModalOpen(true);
  }

  // template 是「换绑时新路由该长什么样」的来源：把一台新的 Provider 接进这条
  // 密钥时，路由的其余属性（输出端点、模式）沿用原来的接线，只换 Provider。
  // 不传就从默认值新建（首次为密钥建路由）。
  async function ensureRouteForBinding(providerId: string, outputProtocol: Protocol, template?: Route) {
    const existing = findRouteForBinding(state.routes, providerId, outputProtocol);
    if (existing) return existing;
    const provider = state.providers.find((item) => item.id === providerId);
    const response = await fetch(`${API_BASE}/__routes`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: `${provider?.name || providerId} · ${protocolLabel(outputProtocol)}`,
        providerId,
        outputProtocol,
        outputEndpointId: template?.outputEndpointId || '',
        mode: template?.mode || 'auto',
        enabled: true,
      }),
    });
    if (!response.ok) throw new Error(await response.text());
    const created = await response.json() as Route;
    await refreshState(false);
    return created;
  }

  async function createProvider() {
    setSaving(true);
    try {
      const isClaudeOAuth = providerDraft.protocol === 'claude' && providerDraft.authType === 'claude_oauth';
      const isCursorOAuth = providerDraft.protocol === 'openai_chat' && providerDraft.authType === 'cursor_oauth';
      const isChatGPTOAuth = providerDraft.protocol === 'openai_responses' && providerDraft.authType === 'chatgpt_oauth';
      const isQoder = providerDraft.protocol === 'openai_chat' && isQoderConnectKind(providerDraft.authType);
      const isWorkBuddy = isWorkBuddyConnectKind(providerDraft.authType);
      const isSelfRegister = providerDraft.authType === 'self_register';
      let requestAdapter: RequestAdapter | null = null;
      const adapterRaw = providerDraft.requestAdapterJSON.trim();
      if (adapterRaw) {
        try {
          requestAdapter = JSON.parse(adapterRaw) as RequestAdapter;
        } catch {
          throw new Error('自定义适配 JSON 无效');
        }
      }
      const payload = {
        name: providerDraft.name,
        protocol: providerDraft.protocol,
        baseUrl: providerDraft.baseUrl,
        apiKeySource: providerDraft.apiKeySource,
        defaultModel: providerDraft.defaultModel,
        defaultThinkingDepth: providerDraft.defaultThinkingDepth,
        authHeader: providerDraft.protocol === 'claude' ? 'x-api-key' : 'Authorization',
        // Qoder 两个站点是两种连接方式，直接把草稿里选定的那种交回去，
        // 不能让「选了 Qoder CN、存成 qoder_pat」把账号签到错的站点上。
        authType: isClaudeOAuth ? 'claude_oauth' : isCursorOAuth ? 'cursor_oauth' : isChatGPTOAuth ? 'chatgpt_oauth' : isQoder ? providerDraft.authType
          : providerAuthTypeParam(providerDraft.authType),
        requestAdapter,
        // 智谱团队版：两者都填才发送并触发团队端点；否则清空（走个人版）。
        teamOrganizationId: providerDraft.teamOrganizationId.trim(),
        teamProjectId: providerDraft.teamProjectId.trim(),
        codingPlanProvider: providerDraft.teamOrganizationId.trim() && providerDraft.teamProjectId.trim() ? 'zhipu_team' : '',
      };
      const response = await fetch(editingProviderID ? `${API_BASE}/__providers/${encodeURIComponent(editingProviderID)}` : `${API_BASE}/__providers`, {
        method: editingProviderID ? 'PATCH' : 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      });
      if (!response.ok) throw new Error(await response.text());
      const saved = await response.json() as Provider;
      const wasCreate = !editingProviderID;
      resetClaudeOAuthFlowState();
      resetCursorOAuthFlowState();
      resetChatGPTOAuthFlowState();
      resetQoderPATFlowState();
      // OAuth 三种连接方式 / 自助注册模式：创建后不关弹窗，直接停留在编辑态，
      // 用户立刻就能点连接按钮 / 拿到注册令牌，不用再多点一次「编辑」重新进来。
      if (wasCreate && (isSelfRegister || isClaudeOAuth || isCursorOAuth || isChatGPTOAuth || isQoder || isWorkBuddy || isSubscriptionConnectKind(providerDraft.authType))) {
        // 不关弹窗、留在编辑态，并且直接把"创建"和"发起授权/生成令牌"这两步
        // 接在同一次点击触发的调用链里完成，用户不用再多点一次。
        setEditingProviderID(saved.id);
        await refreshState(false);
        if (isSelfRegister) {
          await generateProviderSelfRegToken(saved.id);
        } else if (isClaudeOAuth) {
          showToast(`已添加输入 Provider：${saved.name}，正在跳转 Claude 授权…`);
          await startClaudeOAuthConnect(saved.id);
        } else if (isWorkBuddy) {
          showToast(`已添加输入 Provider：${saved.name}，正在跳转 WorkBuddy 登录…`);
          await startWorkBuddyOAuthConnect(saved.id);
        } else if (isCursorOAuth) {
          showToast(`已添加输入 Provider：${saved.name}，正在跳转 Cursor 授权…`);
          await startCursorOAuthConnect(saved.id);
        } else if (isQoder) {
          // 浏览器授权由下面的连接面板发起：这里只把弹窗留在编辑态，
          // 不再替用户多开一个登录页。
          showToast(`已添加输入 Provider：${saved.name}，点「登录」完成 Qoder 授权`);
        } else if (isChatGPTOAuth) {
          showToast(`已添加输入 Provider：${saved.name}，正在跳转 ChatGPT 授权…`);
          await startChatGPTOAuthConnect(saved.id);
        }
      } else {
        setProviderModalOpen(false);
        setEditingProviderID('');
        showToast(wasCreate ? `已添加输入 Provider：${saved.name}` : `已更新输入 Provider：${saved.name}`);
        await refreshState(false);
        await refreshAppLogs();
        setSelectedProviderID(saved.id);
      }
    } catch (error) {
      showToast(`${editingProviderID ? '更新' : '添加'} Provider 失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  async function pollClaudeOAuthStatus(flowId: string, providerId: string = editingProviderID) {
    const deadline = Date.now() + 15 * 60 * 1000;
    setClaudeOAuthPolling(true);
    try {
      while (Date.now() < deadline) {
        const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(providerId)}/claude-oauth/status?flowId=${encodeURIComponent(flowId)}`);
        if (!response.ok) {
          const text = await response.text();
          throw new Error(text || 'oauth status check failed');
        }
        const data = await response.json() as { status: string; message?: string };
        if (data.status === 'success') {
          resetClaudeOAuthFlowState();
          showToast('Claude 账号连接成功');
          await refreshState(false);
          return;
        }
        if (data.status === 'error') {
          throw new Error(data.message || 'Authorization failed');
        }
        await new Promise((resolve) => window.setTimeout(resolve, 1500));
      }
      throw new Error('授权超时，请重试');
    } finally {
      setClaudeOAuthPolling(false);
    }
  }

  // Anthropic's loopback callback only works when the browser and the gateway
  // share a machine. A console opened over a LAN or public address is finished
  // by pasting the code back instead — otherwise the redirect lands on whatever
  // gateway happens to answer localhost on the user's own computer.
  function claudeOAuthCanUseLocalhostCallback() {
    const host = window.location.hostname;
    return host === 'localhost' || host === '127.0.0.1' || host === '::1' || host === '[::1]';
  }

  async function startClaudeOAuthConnect(providerId: string = editingProviderID) {
    if (!providerId) return;
    setClaudeOAuthBusy(true);
    setClaudeOAuthError('');
    const mode = claudeOAuthCanUseLocalhostCallback() ? 'localhost' : 'manual';
    try {
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(providerId)}/claude-oauth/start`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ mode }),
      });
      if (!response.ok) throw new Error(await response.text());
      const data = await response.json() as { authUrl: string; state: string; flowId?: string; mode?: string; instructions?: string };
      setClaudeOAuthState(data.state);
      if (data.mode === 'manual') {
        setClaudeOAuthFlowId('');
        setClaudeOAuthCallbackHint('授权完成后，浏览器会停在 Anthropic 的回调页；把页面给出的 code（或 code#state）粘贴到下面即可。');
        window.open(data.authUrl, '_blank', 'noopener,noreferrer');
        if (await copyToClipboard(data.authUrl)) {
          showToast('请在授权完成后，把回调页上的 code 粘贴到下方');
        } else {
          showToast('已打开 Claude 授权页面');
        }
        return;
      }
      setClaudeOAuthCallbackHint(data.instructions || '');
      if (data.flowId) setClaudeOAuthFlowId(data.flowId);
      // window.open 传 noopener/noreferrer 时规范规定返回值恒为 null（无论是否
      // 真的打开成功），没法用它判断"是否被拦截"；这里不再据此误报，统一给中性
      // 提示，并始终把链接复制到剪贴板兜底（下方"手动粘贴 code"入口始终可用）。
      window.open(data.authUrl, '_blank', 'noopener,noreferrer');
      if (await copyToClipboard(data.authUrl)) {
        showToast('已尝试打开 Claude 授权页面；如未自动跳转，可粘贴剪贴板中的链接手动打开');
      } else {
        showToast('已尝试打开 Claude 授权页面');
      }
      if (data.flowId) {
        setClaudeOAuthBusy(false);
        await pollClaudeOAuthStatus(data.flowId, providerId);
      }
    } catch (error) {
      setClaudeOAuthError(String(error));
    } finally {
      setClaudeOAuthBusy(false);
    }
  }

  async function startClaudeOAuthManualConnect() {
    if (!editingProviderID) return;
    setClaudeOAuthBusy(true);
    setClaudeOAuthError('');
    try {
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(editingProviderID)}/claude-oauth/start`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ mode: 'manual' }),
      });
      if (!response.ok) throw new Error(await response.text());
      const data = await response.json() as { authUrl: string; state: string };
      setClaudeOAuthState(data.state);
      setClaudeOAuthFlowId('');
      const opened = window.open(data.authUrl, '_blank', 'noopener,noreferrer');
      if (!opened) {
        setClaudeOAuthError('浏览器拦截了弹窗，请复制授权链接手动打开。');
      }
      if (await copyToClipboard(data.authUrl)) {
        if (opened) showToast('已打开授权页面，请复制返回的 code 粘贴到下方');
      } else if (opened) {
        showToast('已打开 Claude 授权页面');
      }
    } catch (error) {
      setClaudeOAuthError(String(error));
    } finally {
      setClaudeOAuthBusy(false);
    }
  }

  async function completeClaudeOAuthConnect() {
    if (!editingProviderID) return;
    setClaudeOAuthBusy(true);
    setClaudeOAuthError('');
    try {
      const parsed = parseClaudeOAuthInput(claudeOAuthCode);
      const code = parsed.code;
      const state = parsed.state || claudeOAuthState;
      if (!code) throw new Error('请粘贴授权 code 或完整回调 URL');
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(editingProviderID)}/claude-oauth/complete`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ code, state }),
      });
      await readConnectResponse(response, 'Claude 账号连接失败');
      resetClaudeOAuthFlowState();
      showToast('Claude 账号连接成功');
      await refreshState(false);
    } catch (error) {
      setClaudeOAuthError(String(error));
    } finally {
      setClaudeOAuthBusy(false);
    }
  }

  async function disconnectClaudeOAuth() {
    if (!editingProviderID) return;
    setClaudeOAuthBusy(true);
    setClaudeOAuthError('');
    try {
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(editingProviderID)}/claude-oauth/disconnect`, { method: 'POST' });
      if (!response.ok) throw new Error(await response.text());
      resetClaudeOAuthFlowState();
      showToast('已断开 Claude 账号连接');
      await refreshState(false);
    } catch (error) {
      setClaudeOAuthError(String(error));
    } finally {
      setClaudeOAuthBusy(false);
    }
  }

  async function pollCursorOAuthStatus(flowId: string, providerId: string = editingProviderID) {
    const deadline = Date.now() + 15 * 60 * 1000;
    setCursorOAuthPolling(true);
    try {
      while (Date.now() < deadline) {
        const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(providerId)}/cursor-oauth/status?flowId=${encodeURIComponent(flowId)}`);
        if (!response.ok) {
          const text = await response.text();
          throw new Error(text || 'oauth status check failed');
        }
        const data = await response.json() as { status: string; message?: string };
        if (data.status === 'connected') {
          resetCursorOAuthFlowState();
          // OAuth finish syncs models server-side before status=connected;
          // force one more models refresh so the models menu updates immediately.
          await fetchProviderModels(providerId, 'cursor pro', false);
          await refreshState(false);
          showToast('Cursor 账号已连接，模型列表已同步');
          return;
        }
        if (data.status === 'error') {
          throw new Error(data.message || 'Authorization failed');
        }
        await new Promise((resolve) => window.setTimeout(resolve, 1500));
      }
      throw new Error('授权超时，请重试');
    } finally {
      setCursorOAuthPolling(false);
    }
  }

  async function startCursorOAuthConnect(providerId: string = editingProviderID) {
    if (!providerId) return;
    setCursorOAuthBusy(true);
    setCursorOAuthError('');
    try {
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(providerId)}/cursor-oauth/start`, { method: 'POST' });
      if (!response.ok) throw new Error(await response.text());
      const data = await response.json() as { authUrl: string; flowId?: string };
      if (data.flowId) setCursorOAuthFlowId(data.flowId);
      // 同上：noopener 场景下返回值恒为 null，不再据此误报"被拦截"。
      window.open(data.authUrl, '_blank', 'noopener,noreferrer');
      if (await copyToClipboard(data.authUrl)) {
        showToast('已尝试打开 Cursor 授权页面；如未自动跳转，可粘贴剪贴板中的链接手动打开');
      } else {
        showToast('已尝试打开 Cursor 授权页面');
      }
      if (data.flowId) {
        setCursorOAuthBusy(false);
        await pollCursorOAuthStatus(data.flowId, providerId);
      }
    } catch (error) {
      setCursorOAuthError(String(error));
    } finally {
      setCursorOAuthBusy(false);
    }
  }

  async function disconnectCursorOAuth() {
    if (!editingProviderID) return;
    setCursorOAuthBusy(true);
    setCursorOAuthError('');
    try {
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(editingProviderID)}/cursor-oauth/disconnect`, { method: 'POST' });
      if (!response.ok) throw new Error(await response.text());
      resetCursorOAuthFlowState();
      showToast('已断开 Cursor 账号连接');
      await refreshState(false);
    } catch (error) {
      setCursorOAuthError(String(error));
    } finally {
      setCursorOAuthBusy(false);
    }
  }

  async function pollChatGPTOAuthStatus(flowId: string, providerId: string = editingProviderID) {
    const deadline = Date.now() + 15 * 60 * 1000;
    setChatgptOAuthPolling(true);
    try {
      while (Date.now() < deadline) {
        const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(providerId)}/chatgpt-oauth/status?flowId=${encodeURIComponent(flowId)}`);
        if (!response.ok) {
          const text = await response.text();
          throw new Error(text || 'oauth status check failed');
        }
        const data = await response.json() as { status: string; message?: string };
        if (data.status === 'connected') {
          resetChatGPTOAuthFlowState();
          await fetchProviderModels(providerId, 'chatgpt', false);
          await refreshState(false);
          showToast('ChatGPT 账号已连接，模型列表已同步');
          return;
        }
        if (data.status === 'error') {
          throw new Error(data.message || 'Authorization failed');
        }
        await new Promise((resolve) => window.setTimeout(resolve, 1500));
      }
      throw new Error('授权超时，请重试');
    } finally {
      setChatgptOAuthPolling(false);
    }
  }

  async function startChatGPTOAuthConnect(providerId: string = editingProviderID) {
    if (!providerId) return;
    setChatgptOAuthBusy(true);
    setChatgptOAuthError('');
    try {
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(providerId)}/chatgpt-oauth/start`, { method: 'POST' });
      if (!response.ok) throw new Error(await response.text());
      const data = await response.json() as { authUrl: string; flowId?: string; instructions?: string; callbackBlocked?: string };
      if (data.flowId) setChatgptOAuthFlowId(data.flowId);
      setChatgptOAuthCallbackHint(data.instructions || '');
      setChatgptOAuthCallbackBlocked(data.callbackBlocked || '');
      // 同上：noopener 场景下返回值恒为 null，不再据此误报"被拦截"。
      window.open(data.authUrl, '_blank', 'noopener,noreferrer');
      if (await copyToClipboard(data.authUrl)) {
        showToast('已尝试打开 ChatGPT 授权页面；如未自动跳转，可粘贴剪贴板中的链接手动打开');
      } else {
        showToast('已尝试打开 ChatGPT 授权页面');
      }
      if (data.flowId) {
        setChatgptOAuthBusy(false);
        await pollChatGPTOAuthStatus(data.flowId, providerId);
      }
    } catch (error) {
      setChatgptOAuthError(String(error));
    } finally {
      setChatgptOAuthBusy(false);
    }
  }

  async function completeChatGPTOAuthConnect() {
    if (!editingProviderID) return;
    setChatgptOAuthBusy(true);
    setChatgptOAuthError('');
    try {
      if (!chatgptOAuthCode.trim()) throw new Error('请粘贴授权 code 或完整回调 URL');
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(editingProviderID)}/chatgpt-oauth/complete`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ code: chatgptOAuthCode }),
      });
      await readConnectResponse(response, 'ChatGPT 账号连接失败');
      resetChatGPTOAuthFlowState();
      showToast('ChatGPT 账号连接成功');
      await refreshState(false);
    } catch (error) {
      setChatgptOAuthError(String(error));
    } finally {
      setChatgptOAuthBusy(false);
    }
  }

  async function pollWorkBuddyOAuthStatus(flowId: string, providerId: string = editingProviderID) {
    const deadline = Date.now() + 15 * 60 * 1000;
    setWorkbuddyOAuthPolling(true);
    try {
      while (Date.now() < deadline) {
        const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(providerId)}/workbuddy-oauth/status?flowId=${encodeURIComponent(flowId)}`);
        if (!response.ok) throw new Error(await response.text() || 'oauth status check failed');
        const data = await response.json() as { status: string; message?: string };
        if (data.status === 'connected') {
          resetWorkBuddyOAuthFlowState();
          await fetchProviderModels(providerId, 'WorkBuddy', false);
          await refreshState(false);
          showToast('WorkBuddy 账号已连接，模型列表已同步');
          return;
        }
        if (data.status === 'failed' || data.status === 'error') {
          throw new Error(data.message || 'WorkBuddy 授权失败');
        }
        await new Promise((resolve) => window.setTimeout(resolve, 1500));
      }
      throw new Error('授权超时，请重试');
    } finally {
      setWorkbuddyOAuthPolling(false);
    }
  }

  async function startWorkBuddyOAuthConnect(providerId: string = editingProviderID) {
    if (!providerId) return;
    setWorkbuddyOAuthBusy(true);
    setWorkbuddyOAuthError('');
    try {
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(providerId)}/workbuddy-oauth/start`, { method: 'POST' });
      if (!response.ok) throw new Error(await response.text());
      const data = await response.json() as { authUrl: string; flowId?: string };
      // 登录页地址留在面板上：弹窗被浏览器拦截时用户可以复制后自己打开。
      setWorkbuddyOAuthUrl(data.authUrl);
      window.open(data.authUrl, '_blank', 'noopener,noreferrer');
      if (data.flowId) {
        setWorkbuddyOAuthBusy(false);
        await pollWorkBuddyOAuthStatus(data.flowId, providerId);
      }
    } catch (error) {
      setWorkbuddyOAuthError(String(error));
    } finally {
      setWorkbuddyOAuthBusy(false);
    }
  }

  async function disconnectWorkBuddyOAuth() {
    if (!editingProviderID) return;
    setWorkbuddyOAuthBusy(true);
    setWorkbuddyOAuthError('');
    try {
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(editingProviderID)}/workbuddy-oauth/disconnect`, { method: 'POST' });
      if (!response.ok) throw new Error(await response.text());
      resetWorkBuddyOAuthFlowState();
      showToast('已断开 WorkBuddy 账号连接');
      await refreshState(false);
    } catch (error) {
      setWorkbuddyOAuthError(String(error));
    } finally {
      setWorkbuddyOAuthBusy(false);
    }
  }

  async function connectQoderPAT() {
    if (!editingProviderID) return;
    setQoderPatBusy(true);
    setQoderPatError('');
    try {
      if (!qoderPatInput.trim()) throw new Error('请粘贴 Qoder 个人访问令牌（pt- 开头）');
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(editingProviderID)}/qoder-pat/complete`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ token: qoderPatInput.trim() }),
      });
      await readConnectResponse(response, 'Qoder 账号连接失败');
      resetQoderPATFlowState();
      showToast('Qoder 账号连接成功');
      await refreshState(false);
    } catch (error) {
      setQoderPatError(String(error));
    } finally {
      setQoderPatBusy(false);
    }
  }

  async function disconnectChatGPTOAuth() {
    if (!editingProviderID) return;
    setChatgptOAuthBusy(true);
    setChatgptOAuthError('');
    try {
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(editingProviderID)}/chatgpt-oauth/disconnect`, { method: 'POST' });
      if (!response.ok) throw new Error(await response.text());
      resetChatGPTOAuthFlowState();
      showToast('已断开 ChatGPT 账号连接');
      await refreshState(false);
    } catch (error) {
      setChatgptOAuthError(String(error));
    } finally {
      setChatgptOAuthBusy(false);
    }
  }

  async function refreshApiKeyDraftModels() {
    if (!apiKeyDraft.providerId) {
      showToast('请先选择输入 Provider');
      return;
    }
    const provider = state.providers.find((item) => item.id === apiKeyDraft.providerId);
    if (!provider) {
      showToast('未找到 Provider');
      return;
    }
    await fetchProviderModels(provider.id, provider.name);
  }

  // 管理员启用/禁用 Provider；禁用后普通用户不可见、不可绑定、请求被拒绝。
  async function toggleProviderEnabled(provider: Provider) {
    const enabling = !!provider.disabled;
    setSaving(true);
    try {
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(provider.id)}/enabled`, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ enabled: enabling }),
      });
      if (!response.ok) throw new Error(await response.text());
      showToast(enabling ? `已启用 Provider：${provider.name}` : `已禁用 Provider：${provider.name}（普通用户将不可用）`);
      await refreshState(false);
    } catch (error) {
      showToast(`切换 Provider 状态失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  // 管理员单独切换 Provider 的 RTK 工具输出压缩。走专用接口而不是 Provider
  // 全量保存：后者是增量 PATCH，bool 的零值分不清「没传」和「关掉」。
  async function toggleProviderRTK(provider: Provider) {
    const enabling = !provider.rtkCompress;
    setSaving(true);
    try {
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(provider.id)}/rtk`, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ enabled: enabling }),
      });
      if (!response.ok) throw new Error(await response.text());
      showToast(enabling ? `已开启 RTK 工具输出压缩：${provider.name}` : `已关闭 RTK 工具输出压缩：${provider.name}`);
      await refreshState(false);
      if (enabling) void refreshRTKStats();
    } catch (error) {
      showToast(`切换 RTK 压缩失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  // 保存该 Provider 的首包超时（秒）。留空或 0 = 不限制：网关只约束「等不到响应头」
  // 的那一段，已经开播的长回答/长思考不会被切断。
  async function saveProviderFirstPacketTimeout(providerID: string) {
    const raw = providerFirstPacketRef.current?.value.trim() ?? '';
    const seconds = raw === '' ? 0 : Number(raw);
    if (!Number.isFinite(seconds) || seconds < 0 || seconds > 600) {
      showToast('首包超时请填 0–600 秒（0 表示不限制）');
      return;
    }
    setSaving(true);
    try {
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(providerID)}/first-packet-timeout`, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ seconds }),
      });
      if (!response.ok) throw new Error(await response.text());
      showToast(seconds > 0 ? `已设置首包超时 ${seconds}s` : '已取消首包超时限制');
      await refreshState(false);
    } catch (error) {
      showToast(`保存首包超时失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  // 保存该 Provider 的并发上限、排队等待与每分钟请求预算。上限 0 = 不限并发；
  // 每分钟请求数 0 = 不限速，单独设置时只限速不限并发。排队秒数在任一限制存在时
  // 才有意义：超出的请求先排队、等不到就回 429。
  async function saveProviderConcurrency(providerID: string) {
    const rawMax = providerConcurrencyRef.current?.value.trim() ?? '';
    const rawWait = providerQueueWaitRef.current?.value.trim() ?? '';
    const rawRPM = providerRPMRef.current?.value.trim() ?? '';
    // 留空 = 这行自己一份额度；填写后，同名账号的各行共用一份额度
    // （同一个订阅的多把密钥、同一个账号的不同 BaseURL）。
    const laneGroup = providerLaneGroupRef.current?.value.trim() ?? '';
    if (laneGroup.length > 120) {
      showToast('账号名请控制在 120 个字符以内');
      return;
    }
    const max = rawMax === '' ? 0 : Number(rawMax);
    const queueWaitSeconds = rawWait === '' ? 0 : Number(rawWait);
    const requestsPerMinute = rawRPM === '' ? 0 : Number(rawRPM);
    if (!Number.isFinite(max) || max < 0 || max > 1000) {
      showToast('并发上限请填 0–1000（0 表示不限制）');
      return;
    }
    if (!Number.isFinite(requestsPerMinute) || requestsPerMinute < 0 || requestsPerMinute > 100000) {
      showToast('每分钟请求数请填 0–100000（0 表示不限制）');
      return;
    }
    if (!Number.isFinite(queueWaitSeconds) || queueWaitSeconds < 0 || queueWaitSeconds > 60) {
      showToast('排队等待请填 0–60 秒（0 表示直接拒绝）');
      return;
    }
    setSaving(true);
    try {
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(providerID)}/concurrency`, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ maxConcurrentRequests: max, queueWaitSeconds, requestsPerMinute, laneGroup }),
      });
      if (!response.ok) throw new Error(await response.text());
      const parts: string[] = [];
      if (max > 0) parts.push(`并发 ${max}`);
      if (requestsPerMinute > 0) parts.push(`${requestsPerMinute} 次/分钟`);
      const account = laneGroup ? `，与账号「${laneGroup}」共用` : '';
      if (parts.length === 0) {
        showToast(account ? `已取消并发与限速${account}` : '已取消并发与限速');
      } else {
        showToast(`已设置${parts.join('、')}${queueWaitSeconds > 0 ? `，排队 ${queueWaitSeconds}s` : '，超出直接拒绝'}${account}`);
      }
      await refreshState(false);
    } catch (error) {
      showToast(`保存并发上限失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  // 生成/重置该 Provider 的自助注册令牌；原始令牌只在这次响应里出现一次，
  // 之后只能看到掩码预览（tokenPreview）。
  async function generateProviderSelfRegToken(providerID: string) {
    setSelfRegBusy(true);
    try {
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(providerID)}/self-register-token`, {
        method: 'POST',
        credentials: 'same-origin',
      });
      if (!response.ok) throw new Error(await response.text());
      const data = await response.json() as { token: string };
      setSelfRegToken(data.token);
      await refreshState(false);
      showToast('已生成注册令牌，请立即复制（离开或刷新页面后不再显示）');
    } catch (error) {
      showToast(`生成令牌失败：${String(error)}`);
    } finally {
      setSelfRegBusy(false);
    }
  }

  async function revokeProviderSelfRegistration(providerID: string) {
    if (!window.confirm('确定撤销自助注册？之前签发的令牌会立即失效，对方脚本将无法再更新地址。')) return;
    setSelfRegBusy(true);
    try {
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(providerID)}/self-register-token/revoke`, {
        method: 'POST',
        credentials: 'same-origin',
      });
      if (!response.ok) throw new Error(await response.text());
      setSelfRegToken('');
      await refreshState(false);
      showToast('已撤销自助注册');
    } catch (error) {
      showToast(`撤销失败：${String(error)}`);
    } finally {
      setSelfRegBusy(false);
    }
  }

  async function deleteProvider(providerID: string, providerName: string) {
    if (denyAction('providers.delete')) return;
    const usedBy = state.routes.filter((route) => route.providerId === providerID);
    if (!window.confirm(`确定删除输入 Provider：${providerName}？\n\n绑定到该 Provider 的 API 密钥引用（备选 / 故障转移 / 模型覆盖）将自动重置为空${usedBy.length > 0 ? `；使用它的 ${usedBy.length} 个路由（${usedBy.map((route) => route.name).join('、')}）将一并删除，绑定这些路由的密钥 RouteID 重置为空` : ''}。`)) return;
    setSaving(true);
    try {
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(providerID)}`, { method: 'DELETE' });
      if (!response.ok) throw new Error(await response.text());
      showToast(`已删除输入 Provider：${providerName}`);
      // 走 refreshState：它会检查响应状态并做归一化。直接拿 /__state 的返回体
      // setState 的话，会话过期时那份 401 error 体会被当成 state，整个页面按空
      // 数据渲染（Provider / 密钥全没了的样子），而提示却是「已删除」。
      await refreshState(false);
      setSelectedProviderID(state.providers[0]?.id || '');
      setSelectedExportProviderIDs((current) => current.filter((id) => id !== providerID));
      await refreshAppLogs();
    } catch (error) {
      showToast(`删除 Provider 失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  async function deleteSelectedProviders() {
    if (denyAction('providers.delete')) return;
    const ids = selectedExportProviderIDs.filter((id) => (state.providers || []).some((provider) => provider.id === id));
    if (ids.length === 0) return;
    const names = ids.map((id) => (state.providers || []).find((provider) => provider.id === id)?.name || id);
    if (!window.confirm(`确定删除选中的 ${ids.length} 个输入 Provider？\n\n${names.join('、')}\n\n绑定到这些 Provider 的 API 密钥引用（备选 / 故障转移 / 模型覆盖）将自动重置为空；使用这些 Provider 的路由将一并删除，绑定这些路由的密钥 RouteID 重置为空。`)) return;
    setSaving(true);
    try {
      const results = await Promise.allSettled(ids.map(async (id) => {
        const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(id)}`, { method: 'DELETE' });
        if (!response.ok) throw new Error(await response.text() || `HTTP ${response.status}`);
        return id;
      }));
      const deleted = results.flatMap((result) => result.status === 'fulfilled' ? [result.value] : []);
      const failed = results.length - deleted.length;
      setSelectedExportProviderIDs((current) => current.filter((id) => !deleted.includes(id)));
      if (selectedProviderID && deleted.includes(selectedProviderID)) {
        setSelectedProviderID('');
      }
      await refreshState(false);
      await refreshAppLogs();
      if (failed > 0) {
        showToast(`已删除 ${deleted.length} 个 Provider，${failed} 个失败`);
      } else {
        showToast(`已删除 ${deleted.length} 个输入 Provider`);
      }
    } catch (error) {
      showToast(`批量删除失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  async function batchTestSelectedProviders() {
    const ids = selectedExportProviderIDs.filter((id) => (state.providers || []).some((provider) => provider.id === id));
    if (ids.length === 0) return;
    setSaving(true);
    showToast(`正在对选中的 ${ids.length} 个 Provider 逐个探测连通性与模型…`);
    try {
      let okCount = 0;
      let failCount = 0;
      for (const id of ids) {
        try {
          const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(id)}/test`, { method: 'POST' });
          if (response.ok) {
            const data = await response.json() as ProviderTestResult;
            if (data.success) okCount++;
            else failCount++;
          } else {
            failCount++;
          }
        } catch {
          failCount++;
        }
      }
      await refreshState(false);
      await refreshAppLogs();
      showToast(`批量探测完成：${okCount} 个成功，${failCount} 个异常`);
    } catch (error) {
      showToast(`批量探测失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  async function batchToggleSelectedProviders(enabled: boolean) {
    const ids = selectedExportProviderIDs.filter((id) => (state.providers || []).some((provider) => provider.id === id));
    if (ids.length === 0) return;
    setSaving(true);
    try {
      const results = await Promise.allSettled(ids.map(async (id) => {
        const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(id)}/enabled`, {
          method: 'POST',
          credentials: 'same-origin',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ enabled }),
        });
        if (!response.ok) throw new Error(await response.text());
        return id;
      }));
      const succeeded = results.filter((r) => r.status === 'fulfilled').length;
      const failed = results.length - succeeded;
      await refreshState(false);
      showToast(`已批量${enabled ? '启用' : '禁用'} ${succeeded} 个 Provider${failed > 0 ? `，${failed} 个失败` : ''}`);
    } catch (error) {
      showToast(`批量修改状态失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  function toggleExportProviderSelection(providerID: string) {
    setSelectedExportProviderIDs((current) => (
      current.includes(providerID)
        ? current.filter((id) => id !== providerID)
        : [...current, providerID]
    ));
  }

  function selectAllExportProviders() {
    setSelectedExportProviderIDs(sortedProviders.map((provider) => provider.id));
  }

  function clearExportProviderSelection() {
    setSelectedExportProviderIDs([]);
  }


  function providersExportFilename() {
    const stamp = new Date().toISOString().slice(0, 10).replace(/-/g, '');
    return `providers-export-${stamp}.json`;
  }

  function downloadJSONFile(filename: string, payload: unknown) {
    const blob = new Blob([`${JSON.stringify(payload, null, 2)}\n`], { type: 'application/json;charset=utf-8' });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement('a');
    anchor.href = url;
    anchor.download = filename;
    document.body.appendChild(anchor);
    anchor.click();
    anchor.remove();
    URL.revokeObjectURL(url);
  }

  async function exportProviders(ids?: string[]) {
    const selectedIDs = ids ?? selectedExportProviderIDs;
    if (ids !== undefined && selectedIDs.length === 0) {
      showToast('请先勾选要导出的 Provider');
      return;
    }
    setSaving(true);
    try {
      const response = await fetch(`${API_BASE}/__providers/export`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(ids === undefined ? {} : { ids: selectedIDs }),
      });
      if (!response.ok) throw new Error(await response.text());
      const bundle = await response.json() as { providers?: Provider[]; errors?: string[] };
      const count = bundle.providers?.length ?? 0;
      if (count === 0) {
        showToast('没有可导出的 Provider');
        return;
      }
      downloadJSONFile(providersExportFilename(), bundle);
      const errorHint = bundle.errors?.length ? `，${bundle.errors.length} 个 ID 未找到` : '';
      showToast(`已导出 ${count} 个 Provider${errorHint}`);
    } catch (error) {
      showToast(`导出失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  async function importProvidersFromFile(file: File) {
    setImportingProviders(true);
    try {
      const text = await file.text();
      let bundle: unknown;
      try {
        bundle = JSON.parse(text);
      } catch {
        throw new Error('JSON 解析失败');
      }
      const response = await fetch(`${API_BASE}/__providers/import`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(bundle),
      });
      const result = await response.json() as ProvidersImportResult & { error?: { message?: string } };
      if (!response.ok && !result.created && !result.updated) {
        throw new Error(result.error?.message || (result.errors?.join('; ')) || await Promise.resolve(JSON.stringify(result)));
      }
      const created = result.created?.length ?? 0;
      const updated = result.updated?.length ?? 0;
      const skipped = result.skipped?.length ?? 0;
      const errors = result.errors?.length ?? 0;
      await refreshState(false);
      setSelectedExportProviderIDs([]);
      showToast(`导入完成：新建 ${created}，更新 ${updated}${skipped ? `，跳过 ${skipped}` : ''}${errors ? `，错误 ${errors}` : ''}`);
      await refreshAppLogs();
    } catch (error) {
      showToast(`导入失败：${String(error)}`);
    } finally {
      setSaving(false);
      if (providerImportInputRef.current) providerImportInputRef.current.value = '';
      setImportingProviders(false);
    }
  }

  async function createRoute() {
    const providerId = routeDraft.providerId || selectedProvider?.id || state.providers[0]?.id || '';
    const endpoint = state.endpoints.find((item) => item.protocol === routeDraft.outputProtocol);
    const wasEditing = editingRouteID;
    setSaving(true);
    try {
      const response = await fetch(wasEditing ? `${API_BASE}/__routes/${encodeURIComponent(wasEditing)}` : `${API_BASE}/__routes`, {
        method: wasEditing ? 'PATCH' : 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          name: routeDraft.name,
          providerId,
          outputProtocol: routeDraft.outputProtocol,
          outputEndpointId: endpoint?.id,
          mode: 'auto',
          enabled: true,
        }),
      });
      if (!response.ok) throw new Error(await response.text());
      const saved = await response.json() as Route;
      setRouteModalOpen(false);
      setEditingRouteID('');
      showToast(wasEditing ? `已更新路由：${saved.name}` : `已添加路由：${saved.name}`);
      await refreshState(false);
      await refreshAppLogs();
      setSelectedRouteID(saved.id);
      setSelectedProviderID(saved.providerId);
      setSelectedOutputProtocol(saved.outputProtocol);
    } catch (error) {
      showToast(`${wasEditing ? '更新' : '添加'}路由失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  async function createApiKey() {
    setSaving(true);
    try {
      const route = await ensureRouteForBinding(apiKeyDraft.providerId, apiKeyDraft.outputProtocol);
      const response = await fetch(`${API_BASE}/__apikeys`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          name: apiKeyDraft.name,
          routeId: route.id,
          modelOverride: apiKeyDraft.modelOverride,
          modelAliases: apiKeyDraft.modelAliases,
          thinkingDepthOverride: apiKeyDraft.thinkingDepthOverride,
          maxOutputTokens: apiKeyDraft.maxOutputTokens > 0 ? apiKeyDraft.maxOutputTokens : 0,
          streamEnabled: apiKeyDraft.streamEnabled,
          codexForceFast: apiKeyDraft.codexForceFast === true,
          enabled: true,
        }),
      });
      if (!response.ok) throw new Error(await response.text());
      const created = await response.json() as APIKey;
      setApiKeyModalOpen(false);
      setSelectedApiKeyID(created.id);
      setApiKeyKeyword('');
      showToast(`已创建 API 密钥：${created.name}`);
      await refreshState(false);
      await refreshAppLogs();
      await copy(created.key);
    } catch (error) {
      showToast(`创建 API 密钥失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  async function updateApiKeyBinding(key: APIKey, providerId: string, outputProtocol: Protocol) {
    const currentRoute = state.routes.find((item) => item.id === key.routeId);
    if (currentRoute?.providerId === providerId && currentRoute.outputProtocol === outputProtocol) return;
    const providerChanged = currentRoute?.providerId !== providerId;
    setSaving(true);
    try {
      const route = await ensureRouteForBinding(providerId, outputProtocol);
      const response = await fetch(`${API_BASE}/__apikeys/${encodeURIComponent(key.id)}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(buildApiKeyPatchBody(key, {
          routeId: route.id,
          // 固定模型只跟输入 Provider 相关；仅换输出协议时保留
          ...(providerChanged ? { modelOverride: '' } : {}),
        })),
      });
      if (!response.ok) throw new Error(await response.text());
      showToast(`已更新 API 密钥绑定：${key.name}`);
      await refreshState(false);
      await refreshAppLogs();
    } catch (error) {
      showToast(`更新 API 密钥失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  // promoteApiKeyFallback 是「设为首选」：一次 PATCH 同时交绑定与备选列表。
  //
  // 以前这个按钮只改 routeId，后端随后把新首选从备选里剔掉（normalizeFallback
  // ProviderIDs），旧首选又不补回来 —— 每点一次就少一个节点。现在按「交换」
  // 提交：旧首选回到被提升者原来的位次，其余备选顺序不变。
  //
  // 输出协议保持首选接线原样：客户端手里的协议不因为换了一台上游就变，母语协议
  // 不同时由网关转换。固定模型跟着 Provider 走，由 promoteApiKeyProvider 搬。
  async function promoteApiKeyFallback(key: APIKey, promoteProviderId: string) {
    const currentRoute = state.routes.find((item) => item.id === key.routeId);
    const currentPrimaryId = currentRoute?.providerId || '';
    if (promoteProviderId === currentPrimaryId) return;
    const swap = promoteApiKeyProvider(
      promoteProviderId,
      {
        providerIds: key.fallbackProviderIds || [],
        modelOverrides: key.fallbackModelOverrides || {},
      },
      currentPrimaryId,
    );
    setSaving(true);
    try {
      const route = await ensureRouteForBinding(promoteProviderId, currentRoute?.outputProtocol || 'openai_chat', currentRoute);
      const response = await fetch(`${API_BASE}/__apikeys/${encodeURIComponent(key.id)}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(buildApiKeyPatchBody(key, {
          routeId: route.id,
          // 首选换了 Provider：固定模型只跟输入 Provider 相关，旧的值留在旧首选
          // 现在所在的备选条目里，首选侧清空。
          modelOverride: '',
          fallbackProviderIds: swap.fallbackProviderIds,
          fallbackModelOverrides: swap.fallbackModelOverrides,
        })),
      });
      if (!response.ok) throw new Error(await response.text());
      showToast(`已设为首选：${key.name}`);
      await refreshState(false);
      await refreshAppLogs();
    } catch (error) {
      showToast(`设为首选失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  async function refreshApiKeyModelsForProvider(providerID: string, providerName: string) {
    const provider = state.providers.find((item) => item.id === providerID);
    if (!provider) {
      showToast('未找到关联 Provider');
      return;
    }
    await fetchProviderModels(provider.id, providerName || provider.name);
  }

  async function updateApiKeyFallbacks(key: APIKey, fallbackProviderIds: string[], fallbackModelOverrides: Record<string, string>) {
    setSaving(true);
    try {
      const response = await fetch(`${API_BASE}/__apikeys/${encodeURIComponent(key.id)}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(buildApiKeyPatchBody(key, { fallbackProviderIds, fallbackModelOverrides })),
      });
      if (!response.ok) throw new Error(await response.text());
      showToast(`已更新备选 Provider：${key.name}`);
      await refreshState(false);
      await refreshAppLogs();
    } catch (error) {
      showToast(`更新备选 Provider 失败：${String(error)}`);
      throw error;
    } finally {
      setSaving(false);
    }
  }

  async function updateApiKeyField(key: APIKey, field: ApiKeyEditableField, value: string | boolean | number) {
    // 额度字段单列一个权限点，其余字段走编辑权限
    if (denyAction(field.startsWith('quota') ? 'keys.quota' : 'keys.edit')) return;
    setSaving(true);
    try {
      const patch: Partial<APIKey> = {};
      if (field === 'name') patch.name = String(value).trim();
      if (field === 'routeId') patch.routeId = String(value);
      if (field === 'modelOverride') patch.modelOverride = String(value);
      if (field === 'thinkingDepthOverride') patch.thinkingDepthOverride = String(value);
      if (field === 'maxOutputTokens') patch.maxOutputTokens = typeof value === 'number' ? value : Number.parseInt(String(value), 10) || 0;
      if (field === 'streamEnabled') patch.streamEnabled = Boolean(value);
      if (field === 'codexKeepOfficialLogin') patch.codexKeepOfficialLogin = Boolean(value);
      if (field === 'codexForceFast') patch.codexForceFast = Boolean(value);
      if (field === 'scheduleMode') patch.scheduleMode = String(value);
      if (field === 'enabled') patch.enabled = Boolean(value);
      if (field === 'quotaWindow') patch.quotaWindow = String(value);
      if (field === 'quotaTokens') patch.quotaTokens = Math.max(0, Math.round(Number(value) || 0));
      if (field === 'quotaCostMicros') patch.quotaCostMicros = Math.max(0, Math.round(Number(value) || 0));
      const response = await fetch(`${API_BASE}/__apikeys/${encodeURIComponent(key.id)}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(buildApiKeyPatchBody(key, patch)),
      });
      if (!response.ok) throw new Error(await response.text());
      const updatedName = field === 'name' ? String(value).trim() : key.name;
      showToast(`已更新 API 密钥：${updatedName}`);
      await refreshState(false);
      await refreshAppLogs();
    } catch (error) {
      showToast(`更新 API 密钥失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  async function updateApiKeyModelAliases(key: APIKey, modelAliases: Record<string, string>) {
    setSaving(true);
    try {
      const response = await fetch(`${API_BASE}/__apikeys/${encodeURIComponent(key.id)}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(buildApiKeyPatchBody(key, { modelAliases })),
      });
      if (!response.ok) throw new Error(await response.text());
      const updated = await response.json() as APIKey;
      setState((current) => ({
        ...current,
        apiKeys: (current.apiKeys || []).map((item) => item.id === updated.id ? { ...item, ...updated, modelAliases: updated.modelAliases || {} } : item),
      }));
      showToast(`已更新模型别名：${key.name}`);
      await refreshAppLogs();
    } catch (error) {
      showToast(`更新模型别名失败：${String(error)}`);
      throw error;
    } finally {
      setSaving(false);
    }
  }

  async function updateApiKeyOwner(key: APIKey, ownerUserId: string) {
    setSaving(true);
    try {
      const response = await fetch(`${API_BASE}/__apikeys/${encodeURIComponent(key.id)}`, {
        method: 'PATCH',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ ...buildApiKeyPatchBody(key), ownerUserId }),
      });
      if (!response.ok) throw new Error(await response.text());
      const updated = await response.json() as APIKey;
      setState((current) => ({
        ...current,
        apiKeys: (current.apiKeys || []).map((item) => item.id === updated.id ? { ...item, ...updated } : item),
      }));
      const ownerName = ownerUserId ? (consoleUsers.find((user) => user.id === ownerUserId)?.username || ownerUserId) : '管理员';
      showToast(`已将「${key.name}」分配给 ${ownerName}`);
    } catch (error) {
      showToast(`分配用户失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  // 一键切换 Key 的当前生效转发方案（profile）。客户端 token 不变。
  async function switchApiKeyProfile(key: APIKey, profileId: string) {
    setSaving(true);
    try {
      const response = await fetch(`${API_BASE}/__apikeys/${encodeURIComponent(key.id)}/active-profile`, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ profileId }),
      });
      if (!response.ok) throw new Error(await response.text());
      const updated = await response.json() as APIKey;
      setState((current) => ({
        ...current,
        apiKeys: (current.apiKeys || []).map((item) => item.id === updated.id ? { ...item, ...updated } : item),
      }));
      const name = (updated.profiles || []).find((p) => p.id === profileId)?.name || profileId;
      showToast(`已切换到方案：${name}`);
      await refreshAppLogs();
    } catch (error) {
      showToast(`切换方案失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  // 新建转发方案：完整克隆当前 Key 顶层转发配置，可选立即启用。
  async function createApiKeyProfile(key: APIKey, profile: Partial<KeyProfile>, activate: boolean) {
    setSaving(true);
    try {
      const response = await fetch(`${API_BASE}/__apikeys/${encodeURIComponent(key.id)}/profiles`, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ ...profile, activate }),
      });
      if (!response.ok) throw new Error(await response.text());
      const updated = await response.json() as APIKey;
      setState((current) => ({
        ...current,
        apiKeys: (current.apiKeys || []).map((item) => item.id === updated.id ? { ...item, ...updated } : item),
      }));
      showToast(`已新增方案：${profile.name || ''}`);
      await refreshAppLogs();
    } catch (error) {
      showToast(`新增方案失败：${String(error)}`);
      throw error;
    } finally {
      setSaving(false);
    }
  }

  async function updateApiKeyProfile(key: APIKey, profileId: string, profile: Partial<KeyProfile>) {
    setSaving(true);
    try {
      const response = await fetch(`${API_BASE}/__apikeys/${encodeURIComponent(key.id)}/profiles/${encodeURIComponent(profileId)}`, {
        method: 'PATCH',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(profile),
      });
      if (!response.ok) throw new Error(await response.text());
      const updated = await response.json() as APIKey;
      setState((current) => ({
        ...current,
        apiKeys: (current.apiKeys || []).map((item) => item.id === updated.id ? { ...item, ...updated } : item),
      }));
      showToast(`已更新方案：${profile.name || ''}`);
      await refreshAppLogs();
    } catch (error) {
      showToast(`更新方案失败：${String(error)}`);
      throw error;
    } finally {
      setSaving(false);
    }
  }

  async function deleteApiKeyProfile(key: APIKey, profileId: string) {
    setSaving(true);
    try {
      const response = await fetch(`${API_BASE}/__apikeys/${encodeURIComponent(key.id)}/profiles/${encodeURIComponent(profileId)}`, {
        method: 'DELETE',
        credentials: 'same-origin',
      });
      if (!response.ok) throw new Error(await response.text());
      const updated = await response.json() as APIKey;
      setState((current) => ({
        ...current,
        apiKeys: (current.apiKeys || []).map((item) => item.id === updated.id ? { ...item, ...updated } : item),
      }));
      showToast('已删除方案');
      await refreshAppLogs();
    } catch (error) {
      showToast(`删除方案失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  function clearApiKeyChecks() {
    setCheckedApiKeyIDs([]);
    apiKeyCheckAnchorRef.current = null;
  }

  function selectAllFilteredApiKeys() {
    setCheckedApiKeyIDs(filteredApiKeys.map((key) => key.id));
    apiKeyCheckAnchorRef.current = filteredApiKeys.length > 0 ? 0 : null;
  }

  function toggleApiKeyCheck(keyID: string, index: number, shiftKey: boolean) {
    if (shiftKey && apiKeyCheckAnchorRef.current != null) {
      const from = Math.min(apiKeyCheckAnchorRef.current, index);
      const to = Math.max(apiKeyCheckAnchorRef.current, index);
      const rangeIDs = filteredApiKeys.slice(from, to + 1).map((key) => key.id);
      setCheckedApiKeyIDs((current) => Array.from(new Set([...current, ...rangeIDs])));
      return;
    }
    setCheckedApiKeyIDs((current) => (
      current.includes(keyID) ? current.filter((id) => id !== keyID) : [...current, keyID]
    ));
    apiKeyCheckAnchorRef.current = index;
  }

  async function deleteApiKey(key: APIKey) {
    if (denyAction('keys.delete')) return;
    if (!window.confirm(`确定删除 API 密钥：${key.name}？`)) return;
    setSaving(true);
    try {
      const response = await fetch(`${API_BASE}/__apikeys/${encodeURIComponent(key.id)}`, { method: 'DELETE' });
      if (!response.ok) throw new Error(await response.text());
      showToast(`已删除 API 密钥：${key.name}`);
      setCheckedApiKeyIDs((current) => current.filter((id) => id !== key.id));
      if (selectedApiKeyID === key.id) {
        setSelectedApiKeyID('');
      }
      await refreshState(false);
      await refreshAppLogs();
    } catch (error) {
      showToast(`删除 API 密钥失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  // 列表行、表头、操作按钮上的复制：不弹按钮态，靠行内的 ✓ 与全局 toast 反馈。
  function copyApiKeyValue(key: APIKey) {
    void copy(key.key);
    setCopiedApiKeyID(key.id);
    if (copiedApiKeyTimer.current !== null) window.clearTimeout(copiedApiKeyTimer.current);
    copiedApiKeyTimer.current = window.setTimeout(() => setCopiedApiKeyID(''), 1600);
  }

  // 一键启用 / 停用：详情标题上的状态胶囊和表格里的状态列共用。
  function toggleApiKeyEnabled(key: APIKey) {
    if (saving) return;
    void updateApiKeyField(key, 'enabled', !key.enabled);
  }

  async function setCheckedApiKeysEnabled(enabled: boolean) {
    const ids = checkedApiKeyIDs.filter((id) => (state.apiKeys || []).some((key) => key.id === id));
    if (ids.length === 0) return;
    setSaving(true);
    try {
      const results = await Promise.allSettled(ids.map(async (id) => {
        const key = (state.apiKeys || []).find((item) => item.id === id);
        if (!key) throw new Error('密钥已不存在');
        const response = await fetch(`${API_BASE}/__apikeys/${encodeURIComponent(id)}`, {
          method: 'PATCH',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify(buildApiKeyPatchBody(key, {enabled})),
        });
        if (!response.ok) throw new Error(await response.text() || `HTTP ${response.status}`);
      }));
      const failed = results.filter((result) => result.status === 'rejected').length;
      await refreshState(false);
      await refreshAppLogs();
      showToast(failed > 0
        ? `已${enabled ? '启用' : '停用'} ${ids.length - failed} 个密钥，${failed} 个失败`
        : `已${enabled ? '启用' : '停用'} ${ids.length} 个密钥`);
      if (failed === 0) clearApiKeyChecks();
    } catch (error) {
      showToast(`批量操作失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  // 在线调试沙箱：按住这把 Key 的输出协议，用它自己的 token 打一次网关，
  // 因此答案是「这条链现在通不通」，而不是模拟。
  async function runApiKeySandboxTest(key: APIKey, prompt: string): Promise<RouteTestResult | null> {
    const route = state.routes.find((item) => item.id === key.routeId);
    if (!route) {
      showToast('该密钥没有绑定路由，无法测试');
      return null;
    }
    const endpoint = state.endpoints.find((item) => item.protocol === route.outputProtocol);
    if (!endpoint) {
      showToast('找不到对应输出协议的接入端点，无法测试');
      return null;
    }
    const base = `${clientGatewayRoot(state.endpoints, '')}${endpoint.basePath}`;
    const model = key.modelOverride?.trim() || state.providers.find((item) => item.id === route.providerId)?.defaultModel || 'auto';
    const isClaude = route.outputProtocol === 'claude';
    const url = isClaude ? `${base}/v1/messages`
      : route.outputProtocol === 'openai_responses' ? `${base}/responses`
      : `${base}/chat/completions`;
    const body = isClaude
      ? {model, max_tokens: 512, messages: [{role: 'user', content: prompt}]}
      : {model, stream: false, messages: [{role: 'user', content: prompt}]};
    const headers: Record<string, string> = {'Content-Type': 'application/json'};
    if (isClaude) {
      headers['x-api-key'] = key.key;
      headers['anthropic-version'] = '2023-06-01';
    } else {
      headers['Authorization'] = `Bearer ${key.key}`;
    }
    const started = performance.now();
    try {
      const response = await fetch(url, {method: 'POST', headers, body: JSON.stringify(body)});
      const latencyMs = Math.round(performance.now() - started);
      const text = await response.text();
      let modelUsed = '';
      let preview = text;
      try {
        const data = JSON.parse(text) as Record<string, any>;
        modelUsed = String(data.model || '');
        const content = route.outputProtocol === 'openai_responses'
          ? (data.output_text || data.output?.[0]?.content?.[0]?.text || '')
          : Array.isArray(data.content)
            ? data.content.map((part: {text?: string}) => part.text || '').join('')
            : data.choices?.[0]?.message?.content || data.error?.message || '';
        preview = content || text;
      } catch {
        // 非 JSON：原文直接给用户看，不做二次包装
      }
      return {
        success: response.ok,
        status: response.status,
        latencyMs,
        model: modelUsed || model,
        protocolFlow: `${protocolLabel(route.outputProtocol)}`,
        preview: preview.slice(0, 4000),
        error: response.ok ? undefined : preview.slice(0, 500),
      };
    } catch (error) {
      return {success: false, latencyMs: Math.round(performance.now() - started), error: String(error)};
    }
  }

  // 详情工作区要的补丁回调：与 apiKeys 面板同源，一次性打包，避免 JSX 里再传十几项。
  const apiKeyPanelProps = {
    onUpdateField: updateApiKeyField,
    onUpdateBinding: updateApiKeyBinding,
    onPromoteFallback: promoteApiKeyFallback,
    onUpdateFallbacks: updateApiKeyFallbacks,
    onUpdateModelAliases: updateApiKeyModelAliases,
    onUpdateOwner: updateApiKeyOwner,
    onDelete: deleteApiKey,
    onClone: openCloneApiKeyModal,
    onRefreshModels: refreshApiKeyModelsForProvider,
    onSwitchProfile: switchApiKeyProfile,
    onCreateProfile: createApiKeyProfile,
    onUpdateProfile: updateApiKeyProfile,
    onDeleteProfile: deleteApiKeyProfile,
  };

  async function deleteCheckedApiKeys() {
    if (denyAction('keys.delete')) return;
    const ids = checkedApiKeyIDs.filter((id) => (state.apiKeys || []).some((key) => key.id === id));
    if (ids.length === 0) return;
    if (!window.confirm(`确定删除选中的 ${ids.length} 个 API 密钥？此操作不可恢复。`)) return;
    setSaving(true);
    try {
      const results = await Promise.allSettled(ids.map(async (id) => {
        const response = await fetch(`${API_BASE}/__apikeys/${encodeURIComponent(id)}`, { method: 'DELETE' });
        if (!response.ok) throw new Error(await response.text() || `HTTP ${response.status}`);
        return id;
      }));
      const deleted = results.flatMap((result) => result.status === 'fulfilled' ? [result.value] : []);
      const failed = results.length - deleted.length;
      if (selectedApiKeyID && deleted.includes(selectedApiKeyID)) {
        setSelectedApiKeyID('');
      }
      setCheckedApiKeyIDs((current) => current.filter((id) => !deleted.includes(id)));
      apiKeyCheckAnchorRef.current = null;
      await refreshState(false);
      await refreshAppLogs();
      if (failed > 0) {
        showToast(`已删除 ${deleted.length} 个密钥，${failed} 个失败`);
      } else {
        showToast(`已删除 ${deleted.length} 个 API 密钥`);
      }
    } catch (error) {
      showToast(`批量删除失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  async function deleteRoute(routeID: string, routeName: string) {
    if (!window.confirm(`确定删除路由：${routeName}？`)) return;
    setSaving(true);
    try {
      const response = await fetch(`${API_BASE}/__routes/${encodeURIComponent(routeID)}`, { method: 'DELETE' });
      if (!response.ok) throw new Error(await response.text());
      showToast(`已删除路由：${routeName}`);
      // 同 deleteProvider：refreshState 先校验状态再 setState，会话过期时不会把
      // 401 的 error 体当成一份空 state 渲染出来。
      await refreshState(false);
      const nextRoute = state.routes[0];
      setSelectedRouteID(nextRoute?.id || '');
      setSelectedProviderID(nextRoute?.providerId || state.providers[0]?.id || '');
      setSelectedOutputProtocol(nextRoute?.outputProtocol || 'openai_chat');
      await refreshAppLogs();
    } catch (error) {
      showToast(`删除路由失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  async function copy(value: string, customMessage?: string) {
    const ok = await copyToClipboard(value);
    if (ok) {
      showToast(customMessage || `已复制：${value}`);
    } else {
      showToast('复制失败，请手动选中文本复制');
    }
    return ok;
  }

  function showToast(text: string) {
    const toast = document.getElementById('toast');
    if (!toast) return;
    toast.textContent = text;
    toast.classList.add('show');
    window.setTimeout(() => toast.classList.remove('show'), 2200);
  }

  async function updateWebExposed(enabled: boolean) {
    const tunnelIsRunning = (state.publicAccess?.tunnel?.status === 'running')
      || (state.publicAccess?.tunnel?.status === 'starting');
    setSaving(true);
    try {
      if (!enabled && tunnelIsRunning) {
        showToast('关闭 Web 访问将同时停止公网隧道…');
      }
      const response = await fetch(`${API_BASE}/__settings/web-exposed`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ enabled }),
      });
      if (!response.ok) throw new Error(await response.text());
      const data = await response.json() as { webExposed: boolean };
      setState((current) => ({ ...current, webExposed: data.webExposed }));
      if (data.webExposed) {
        showToast('已开启 Web 访问（局域网 / 穿透）。管理页无登录，勿对不可信网络长期暴露。');
      } else {
        showToast('已关闭 Web 访问：仅本机 127.0.0.1 可访问');
      }
      await refreshState(false);
    } catch (error) {
      showToast(`更新 Web 访问失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  const outputProtocol = selectedEndpoint?.protocol || selectedRoute?.outputProtocol || selectedOutputProtocol;
  const routeAction = selectedProvider ? (outputProtocol === selectedProvider.protocol ? 'pass_through' : 'convert') : 'not_configured';
  const localURL = endpointURL(selectedEndpoint);
  const publicAccess = state.publicAccess || defaultPublicAccess;
  const tunnel = publicAccess.tunnel;
  const tunnelRunning = tunnel?.status === 'running';
  const livePublicURL = activePublicBaseURL(publicAccess, tunnelRunning);
  const liveUIPublicURL = activeUIPublicBaseURL(publicAccess, tunnelRunning);
  // 控制台挂在域名后面时，浏览器手里的 origin 就是客户端真正能用的公网入口，
  // 比网关自报的网卡地址可信。隧道地址仍优先（可与管理台域名分开）。
  const browserOrigin = browserAccessOrigin();
  const clientPublicRoot = livePublicURL || browserOrigin;
  const activeTunnelMode = tunnelRunning ? tunnel?.mode : '';
  const quickTunnelActive = tunnelRunning && activeTunnelMode === 'quick';
  const customTunnelActive = tunnelRunning && activeTunnelMode === 'custom';
  const composedCustomDomain = composeCustomDomain(customDomainPrefix, customDomainRoot);
  const composedUIDomain = composeCustomDomain(uiDomainPrefix, customDomainRoot) || deriveUIDomainFromAPI(composedCustomDomain);
  const publicMetricValue = publicAccessMetricValue(publicAccess.enabled, publicAccess.mode);
  const portHint = state.endpoints[0]?.listenPort || 18093;
  const webExposed = state.webExposed === true;
  const advertiseHost = state.endpoints[0]?.listenHost || '';
  const lanHostHint = advertiseHost && advertiseHost !== '0.0.0.0' && advertiseHost !== '127.0.0.1' && advertiseHost !== 'localhost'
    ? advertiseHost
    : '';
  const lanAccessURL = webExposed
    ? `http://${lanHostHint || '<局域网IP>'}:${portHint}`
    : '已关闭（仅本机可访问）';
  // 接入根地址的「本机」口径：绑回环时自报网卡 IP 已不可达，只能给 127.0.0.1。
  const localClientRoot = webExposed ? localGatewayRoot(state.endpoints) : `http://127.0.0.1:${portHint}`;
  // 远程开关示例里的 base 地址：该接口拒绝公网调用，所以给的是局域网地址
  // （没探测到局域网 IP 时退回本机地址），不要给公网域名。
  const publicControlBaseURL = webExposed && lanHostHint
    ? `http://${lanHostHint}:${portHint}`
    : `http://127.0.0.1:${portHint}`;
  const publicControlCurlExamples = [
    '# 开启「购买域名」公网访问',
    `curl -X POST ${publicControlBaseURL}/__public/control \\`,
    '  -H "Authorization: Bearer <令牌>" \\',
    '  -H "Content-Type: application/json" \\',
    `  -d '{"mode":"custom_domain","enabled":true}'`,
    '',
    '# 关闭「购买域名」公网访问',
    `curl -X POST ${publicControlBaseURL}/__public/control \\`,
    '  -H "Authorization: Bearer <令牌>" \\',
    '  -H "Content-Type: application/json" \\',
    `  -d '{"mode":"custom_domain","enabled":false}'`,
    '',
    '# 开启 / 关闭「免费随机域名」公网访问（把 enabled 换成 false 即为关闭）',
    `curl -X POST ${publicControlBaseURL}/__public/control \\`,
    '  -H "Authorization: Bearer <令牌>" \\',
    '  -H "Content-Type: application/json" \\',
    `  -d '{"mode":"random_tunnel","enabled":true}'`,
  ].join('\n');
  const activeProviderRouteCount = selectedProvider ? (state.apiKeys || []).filter((key) => (
    apiKeyReferencesProvider(key, state.routes, selectedProvider.id)
  )).length : 0;
  const apiKeyDraftProvider = state.providers.find((item) => item.id === apiKeyDraft.providerId);
  const apiKeyDraftModels = apiKeyDraftProvider ? state.models.filter((model) => model.providerId === apiKeyDraftProvider.id) : [];
  const refreshingApiKeyModels = apiKeyDraftProvider ? testingProviderID === apiKeyDraftProvider.id : false;
  const chatTestRoute = chatTestContext?.kind === 'route' ? state.routes.find((item) => item.id === chatTestContext.id) : undefined;
  const chatTestProvider = chatTestContext?.kind === 'provider'
    ? state.providers.find((item) => item.id === chatTestContext.id)
    : chatTestRoute ? state.providers.find((item) => item.id === chatTestRoute.providerId) : undefined;
  chatTestContextRef.current = chatTestContext;
  const chatTestRunKey = chatTestContext ? `${chatTestContext.kind}:${chatTestContext.id}` : '';
  const chatTestRunning = {
    main: Boolean(chatTestRunKey) && chatTestingKeys.includes(`${chatTestRunKey}:main`),
    cache: Boolean(chatTestRunKey) && chatTestingKeys.includes(`${chatTestRunKey}:cache`),
    thinking: Boolean(chatTestRunKey) && chatTestingKeys.includes(`${chatTestRunKey}:thinking`),
  };
  const chatTestModels = chatTestProvider ? state.models.filter((model) => model.providerId === chatTestProvider.id) : [];
  const refreshingChatTestModels = chatTestProvider ? testingProviderID === chatTestProvider.id : false;
  const chatTestBoundApiKey = chatTestContext?.kind === 'route'
    ? (state.apiKeys || []).find((key) => key.enabled && key.routeId === chatTestContext.id)
    : undefined;
  const chatTestResolvedModel = chatTestProvider
    ? resolveProviderTestModel(chatTestModel.trim() || chatTestProvider.defaultModel || 'request-model-not-set')
    : chatTestModel;
  const chatTestEndpointURL = chatTestContext?.kind === 'route' && chatTestRoute
    ? routeGatewayTestURL(chatTestRoute, state.endpoints)
    : chatTestProvider
      ? resolveProviderChatURL(chatTestProvider, chatTestResolvedModel)
      : API_BASE;
  const chatTestCurl = chatTestContext?.kind === 'route'
    ? buildRouteTestCurl(chatTestEndpointURL, chatTestModel, chatTestMessage, chatTestBoundApiKey?.key)
    : chatTestProvider
      ? buildProviderChatCurl(chatTestProvider, chatTestResolvedModel, providerChatOptions, providerAuthPreview)
      : '';
  // 鉴权解析不出来时提前说清楚，别等 401 再猜。
  const chatTestAuthWarning = chatTestContext?.kind === 'provider' && chatTestProvider
    && !['claude_oauth', 'cursor_oauth', 'chatgpt_oauth', 'qoder_pat', 'qoder_cn', 'workbuddy', 'workbuddy_ai', 'zcode', 'zcode_bigmodel', 'trae_cn', 'trae_global'].includes(chatTestProvider.authType || '')
    && !providerAuthPreview?.value
    ? '未解析到 Provider 鉴权值；若使用 env: 变量，请确认网关进程环境变量已设置。'
    : undefined;
  const providerThinkingPresets = chatTestProvider ? thinkingPresetsForProtocol(chatTestProvider.protocol) : null;
  const isExplicitLoginPage = typeof window !== 'undefined' && window.location.pathname === '/login';
  const needsAuthGate = Boolean((authChecked && authStatus?.requireAuth && !authStatus.authenticated) || isExplicitLoginPage);
  const authSetupMode = Boolean(needsAuthGate && authStatus && !authStatus.configured);
  const isNormalUser = Boolean(authStatus?.authenticated && authStatus.role === 'user');
  // 侧栏模型：按权限点过滤后的分层分组（隐藏项不渲染，空分组整组不渲染），
  // 再按搜索词同时匹配菜单名与分组名。搜索时命中的分组强制展开，不写回折叠状态。
  const navSearch = navQuery.trim().toLowerCase();
  const navModel = navSections
    .map((section) => {
      const items = section.children
        .map((child) => navItems.find((item) => item.id === child.id))
        .filter((item): item is typeof navItems[number] => Boolean(item))
        .filter((item) => navVisibleFor(permissions, item.id, legacyUser));
      const groupHit = navSearch.length > 0 && section.label.toLowerCase().includes(navSearch);
      const hits = items.filter((item) => groupHit || item.label.toLowerCase().includes(navSearch));
      return { id: section.id, label: section.label, items, hits };
    })
    .filter((group) => group.items.length > 0 && (navSearch.length === 0 || group.hits.length > 0));
  const jumpToFirstNavResult = () => {
    const first = navModel.flatMap((group) => group.hits)[0];
    if (!first) return;
    goToPage(first.id);
    setNavQuery('');
  };

  if (!authChecked) {
    return null;
  }

  if (needsAuthGate) {
    return (
      <>
        <TerminalAuthGate
          authSetupMode={authSetupMode}
          authUsername={authUsername}
          setAuthUsername={setAuthUsername}
          authPassword={authPassword}
          setAuthPassword={setAuthPassword}
          authPasswordConfirm={authPasswordConfirm}
          setAuthPasswordConfirm={setAuthPasswordConfirm}
          authBusy={authBusy}
          authError={authError}
          setAuthError={setAuthError}
          trustDevice={authTrustDevice}
          setTrustDevice={setAuthTrustDevice}
          onSubmit={submitAdminAuth}
        />
        {/* 安装提示不依赖登录：先在登录页装到桌面，比登进来再找入口更自然。
            登录页是固定的深色终端壳，卡片跟它同色系，别让白卡浮在深底上。 */}
        <PwaInstallPrompt dark/>
      </>
    );
  }

  if (!stateHydrated) {
    return null;
  }

  return (
    <>
      {upgradeAvailable && (
        <div className="upgrade-toast" role="status" aria-live="polite">
          <div className="upgrade-toast-head">
            <span className="upgrade-toast-icon" aria-hidden="true">
              <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                <path d="M12 3v12" />
                <path d="M7 10l5 5 5-5" />
                <path d="M4 19h16" />
              </svg>
            </span>
            <span className="upgrade-toast-title">发现新版本</span>
            <button
              type="button"
              className="upgrade-toast-close"
              aria-label="暂时不升级"
              title="暂时不升级"
              onClick={() => { dismissUpgrade(latestAppVersion); setUpgradeDismissed(true); }}
            >
              {"\u00d7"}
            </button>
          </div>
          <div className="upgrade-toast-versions">
            <span className="upgrade-toast-version">{CURRENT_APP_VERSION}</span>
            <span className="upgrade-toast-arrow" aria-hidden="true">{"\u2192"}</span>
            <span className="upgrade-toast-version is-latest">{latestAppVersion}</span>
            <span className="upgrade-toast-hint">重新载入即可使用</span>
          </div>
          <div className="upgrade-toast-actions">
            <button type="button" className="upgrade-toast-primary" onClick={() => reloadToVersion(latestAppVersion)}>
              立即升级
            </button>
            <button
              type="button"
              className="upgrade-toast-secondary"
              onClick={() => { dismissUpgrade(latestAppVersion); setUpgradeDismissed(true); }}
            >
              稍后
            </button>
          </div>
        </div>
      )}
      <PwaInstallPrompt/>
      <div className={`shell${sidebarCollapsed ? ' sidebar-collapsed' : ''}`}>
        <aside className={`sidebar${sidebarCollapsed ? ' collapsed' : ''}`}>
          <div className="brand">
            <div className="brand-logo"><img src="/logo.svg" alt="" aria-hidden="true" /></div>
            <div className="brand-text">
              <div className="brand-title">协议网关</div>
              <div className="brand-subtitle">LLM Protocol Gateway</div>
            </div>
            <div className="brand-actions">
              <a
                className="brand-github"
                href="https://github.com/yangyongyongyong/llm-protocol-gateway"
                target="_blank"
                rel="noreferrer"
                title="GitHub · yangyongyongyong/llm-protocol-gateway"
                aria-label="打开 GitHub 仓库"
              >
                <svg viewBox="0 0 16 16" width="15" height="15" fill="currentColor" aria-hidden="true">
                  <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27s1.36.09 2 .27c1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8z" />
                </svg>
              </a>
              <PwaInstallButton/>
              <button
                type="button"
                className="sidebar-toggle"
                onClick={() => setSidebarCollapsed((v) => !v)}
                title={sidebarCollapsed ? '展开侧边栏' : '收起侧边栏'}
                aria-label={sidebarCollapsed ? '展开侧边栏' : '收起侧边栏'}
                aria-expanded={!sidebarCollapsed}
              >
                <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                  <path d="M15 6l-6 6 6 6" />
                </svg>
              </button>
            </div>
          </div>

          {(backendConnected === false || backendReconnecting) && (
            <button
              type="button"
              className={`status-pill status-pill-btn ${backendConnected === false ? 'off' : ''} ${backendReconnecting ? 'reconnecting' : ''}`}
              onClick={() => { if (backendConnected === false && !backendReconnecting) void reconnectBackend(true); }}
              title="点击尝试重新连接后端"
            >
              <span className="dot" />
              {backendReconnecting ? '重连中…' : '后端未连接'}
            </button>
          )}

          {/* 菜单搜索：回车直达第一个结果，Esc 清空并还原折叠状态；图标栏里只留一个放大镜。 */}
          <div className={`nav-search${navQuery ? ' has-query' : ''}`}>
            <span className="nav-search-icon" aria-hidden="true">
              <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
                <circle cx="11" cy="11" r="6.5" />
                <path d="m16 16 4 4" />
              </svg>
            </span>
            <input
              type="search"
              value={navQuery}
              placeholder="搜索菜单"
              aria-label="搜索菜单"
              title="搜索菜单"
              onChange={(event) => setNavQuery(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === 'Enter') {
                  event.preventDefault();
                  jumpToFirstNavResult();
                } else if (event.key === 'Escape') {
                  event.preventDefault();
                  setNavQuery('');
                }
              }}
            />
            {navQuery ? (
              <button
                type="button"
                className="nav-search-clear"
                aria-label="清空搜索"
                title="清空搜索（Esc）"
                onClick={() => setNavQuery('')}
              >
                {"\u00d7"}
              </button>
            ) : null}
          </div>

          <nav className="nav" aria-label="主导航">
            {navModel.length === 0 ? (
              <div className="nav-empty" role="status">没有匹配的菜单</div>
            ) : navModel.map((group) => {
              // 当前页所在的分组始终展开；搜索时只显示命中的菜单项。
              const searching = navSearch.length > 0;
              const expanded = searching || group.items.some((item) => item.id === activeNav) || !navCollapsedGroups.includes(group.id);
              const items = searching ? group.hits : group.items;
              return (
                <div className={`nav-group${expanded ? '' : ' collapsed'}`} key={group.id}>
                  <button
                    type="button"
                    className="nav-group-label"
                    aria-expanded={expanded}
                    title={`${group.label}（${expanded ? '收起' : '展开'}）`}
                    onClick={() => { if (!searching) toggleNavGroup(group.id); }}
                  >
                    <span className="nav-group-text">{group.label}</span>
                    <svg className="nav-group-chevron" viewBox="0 0 24 24" width="12" height="12" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                      <path d="m6 9 6 6 6-6" />
                    </svg>
                  </button>
                  <div className="nav-group-rule" aria-hidden="true" title={group.label} />
                  <div className="nav-group-items">
                    {items.map((item) => (
                      <a
                        className={`nav-item ${activeNav === item.id ? 'active' : ''}`}
                        href={navPathForID(item.id)}
                        key={item.id}
                        title={item.label}
                        onClick={(event) => {
                          event.preventDefault();
                          goToPage(item.id);
                        }}
                      >
                        <span className="nav-icon"><NavIcon id={item.id} /></span>
                        <span className="nav-label">{item.label}</span>
                        {item.id === 'public-access' ? (
                          <span
                            className={`nav-status-dot ${tunnelRunning ? 'on' : 'off'}`}
                            title={tunnelRunning ? '公网隧道已开启' : '公网隧道已关闭'}
                          />
                        ) : null}
                      </a>
                    ))}
                  </div>
                </div>
              );
            })}
          </nav>

          <div className="sidebar-footer">
            <ThemeSwitch value={themeMode} onChange={setThemeMode} size="compact" />
            {authStatus?.authenticated && authStatus.username ? (
              <SidebarUserCard auth={authStatus} busy={authBusy} dataFetchedAt={dataFetchedAt} onLogout={() => void logoutAdmin()} onEditProfile={() => setProfileModalOpen(true)} />
            ) : authStatus?.requireAuth ? (
              <button className="btn" type="button" disabled={authBusy} onClick={() => void logoutAdmin()}>
                退出登录
              </button>
            ) : null}
          </div>

          {/* 手机档（≤440）「⋯」：侧栏页脚收进顶部单行栏后，主题切换与退出放这里。 */}
          <div className="sidebar-more">
            <button
              type="button"
              className={`icon-btn sidebar-more-btn${sidebarMoreOpen ? ' active' : ''}`}
              onClick={() => setSidebarMoreOpen((v) => !v)}
              aria-expanded={sidebarMoreOpen}
              aria-label="更多（主题与账号）"
              title="更多（主题与账号）"
            >
              <svg viewBox="0 0 24 24" width="16" height="16" fill="currentColor" aria-hidden="true">
                <circle cx="5" cy="12" r="1.6" />
                <circle cx="12" cy="12" r="1.6" />
                <circle cx="19" cy="12" r="1.6" />
              </svg>
            </button>
            {sidebarMoreOpen && (
              <div className="sidebar-more-panel">
                <ThemeSwitch value={themeMode} onChange={setThemeMode} size="compact" />
                {authStatus?.authenticated && authStatus.username ? (
                  <SidebarUserCard auth={authStatus} busy={authBusy} dataFetchedAt={dataFetchedAt} onLogout={() => void logoutAdmin()} onEditProfile={() => setProfileModalOpen(true)} />
                ) : authStatus?.requireAuth ? (
                  <button className="btn" type="button" disabled={authBusy} onClick={() => void logoutAdmin()}>
                    退出登录
                  </button>
                ) : null}
              </div>
            )}
          </div>
        </aside>

        <main className="main">
          {activeNav === 'api-keys' && (
          // 两种视图都吃满可视高度（见 akc.css）：模式 class 决定由哪块内部滚动。
          <section className={`section-full akc-page${apiKeyViewMode === 'split' ? ' akc-page-split' : ' akc-page-full'}`}>
            <ApiKeysTopBar
              totalKeys={(state.apiKeys || []).length}
              enabledKeys={(state.apiKeys || []).filter((key) => key.enabled).length}
              viewMode={apiKeyViewMode}
              onViewModeChange={setApiKeyViewMode}
              onCreate={openApiKeyModal}
              createDisabled={saving || !canDo('keys.create') || state.providers.length === 0}
            />
            {state.providers.length === 0 ? (
              <div className="akc-banner">请先创建至少一个输入 Provider，再添加 API 密钥。</div>
            ) : null}

            {apiKeyViewMode === 'split' ? (
              <div className="akc-split">
                <ApiKeyFleetPane
                  keys={filteredApiKeys}
                  totalKeys={(state.apiKeys || []).length}
                  selectedKeyId={selectedApiKey?.id || ''}
                  checkedIds={checkedApiKeyIDs}
                  usage={apiKeyUsageMap}
                  routes={state.routes}
                  providers={state.providers}
                  ownerLabel={apiKeyOwnerName}
                  filters={apiKeyFiltersProps}
                  saving={saving}
                  onSelect={(key) => setSelectedApiKeyID(key.id)}
                  onToggleCheck={toggleApiKeyCheck}
                  onSelectAll={selectAllFilteredApiKeys}
                  onClearSelection={clearApiKeyChecks}
                  onBatchDelete={() => { if (!canDo('keys.delete')) return; void deleteCheckedApiKeys(); }}
                  onCopyKey={copyApiKeyValue}
                  copiedKeyId={copiedApiKeyID}
                  onToggleEnabled={toggleApiKeyEnabled}
                  createDisabled={saving || !canDo('keys.create') || state.providers.length === 0}
                  onCreate={openApiKeyModal}
                />
                {selectedApiKey ? (
                  <ApiKeyDetailWorkspace
                    keyItem={selectedApiKey}
                    providers={state.providers}
                    models={state.models}
                    routes={state.routes}
                    endpoints={state.endpoints}
                    owners={authStatus?.scope === 'all' || canDo('usage.all') ? consoleUsers : undefined}
                    saving={saving}
                    canWriteQuota={canDo('keys.quota')}
                    canDelete={canDo('keys.delete')}
                    testingProviderID={testingProviderID}
                    tunnelRunning={tunnelRunning}
                    livePublicURL={clientPublicRoot}
                    fixedOutputLabels={fixedOutputLabels}
                    panel={apiKeyPanelProps}
                    onToggleEnabled={toggleApiKeyEnabled}
                    onToast={showToast}
                    publicBase={clientPublicRoot}
                    onTest={runApiKeySandboxTest}
                  />
                ) : (
                  <div className="akc-detail card akc-detail-placeholder">
                    <svg viewBox="0 0 24 24" width="26" height="26" fill="none" stroke="currentColor" strokeWidth="1.6" aria-hidden="true"><circle cx="8" cy="12" r="4.2" /><path d="M12.2 12H21M17.5 12v3M20.8 12v2.2" /></svg>
                    <p>{filteredApiKeys.length === 0 ? '没有符合筛选条件的密钥' : '请从左侧选择一把 API 密钥'}</p>
                  </div>
                )}
              </div>
            ) : (
              <ApiKeyFullTable
                keys={pagedApiKeys}
                totalKeys={(state.apiKeys || []).length}
                checkedIds={checkedApiKeyIDs}
                selectedKeyId={selectedApiKey?.id || ''}
                usage={apiKeyUsageMap}
                routes={state.routes}
                providers={state.providers}
                ownerLabel={apiKeyOwnerName}
                saving={saving}
                onToggleCheck={toggleApiKeyCheck}
                onSelectAll={selectAllFilteredApiKeys}
                onClearSelection={clearApiKeyChecks}
                onSelect={(key) => setSelectedApiKeyID(key.id)}
                onOpenDrawer={(key) => {
                  setSelectedApiKeyID(key.id);
                  setApiKeyDrawerOpen(true);
                }}
                onCopyKey={copyApiKeyValue}
                copiedKeyId={copiedApiKeyID}
                onDelete={(key) => void deleteApiKey(key)}
                onToggleEnabled={toggleApiKeyEnabled}
                onBatchDelete={() => { if (!canDo('keys.delete')) return; void deleteCheckedApiKeys(); }}
                pagination={apiKeyTotalPages > 1 ? {
                  page: currentApiKeyPage,
                  total: apiKeyTotalPages,
                  pageSize: API_KEYS_PAGE_SIZE,
                  onPrev: () => setApiKeyPage(currentApiKeyPage - 1),
                  onNext: () => setApiKeyPage(currentApiKeyPage + 1),
                } : undefined}
              />
            )}

            <ApiKeyDrawer open={apiKeyDrawerOpen} title={selectedApiKey?.name || ''} onClose={() => setApiKeyDrawerOpen(false)}>
              {selectedApiKey ? (
                <ApiKeyDetailWorkspace
                  keyItem={selectedApiKey}
                  providers={state.providers}
                  models={state.models}
                  routes={state.routes}
                  endpoints={state.endpoints}
                  owners={authStatus?.scope === 'all' || canDo('usage.all') ? consoleUsers : undefined}
                  saving={saving}
                  canWriteQuota={canDo('keys.quota')}
                  canDelete={canDo('keys.delete')}
                  testingProviderID={testingProviderID}
                  tunnelRunning={tunnelRunning}
                  livePublicURL={clientPublicRoot}
                  fixedOutputLabels={fixedOutputLabels}
                  panel={apiKeyPanelProps}
                  onToggleEnabled={toggleApiKeyEnabled}
                  onToast={showToast}
                  publicBase={clientPublicRoot}
                  onTest={runApiKeySandboxTest}
                />
              ) : null}
            </ApiKeyDrawer>

            <ApiKeyBatchBar
              count={checkedApiKeyIDs.length}
              saving={saving}
              onClear={clearApiKeyChecks}
              onEnable={() => void setCheckedApiKeysEnabled(true)}
              onDisable={() => void setCheckedApiKeysEnabled(false)}
              onDelete={() => { if (!canDo('keys.delete')) return; void deleteCheckedApiKeys(); }}
            />
          </section>
          )}

          {activeNav === 'input-providers' && (
          <section className="section-full providers-page">
            <div className="card panel">
              <div className="panel-header">
                <div>
                  <h2 className="panel-title">输入 Provider</h2>
                  <p className="panel-desc">
                    {isNormalUser
                      ? '展示管理员授权给你的 Provider（只读）及自己创建的 Provider（可编辑 / 克隆 / 删除 / 测试）。删除时绑定的 API 密钥引用会自动重置为空。'
                      : '自定义上游 Provider，按近 3 日请求量排序；勾选后可批量导出 / 导入配置（含 OAuth 元数据）。删除时绑定的 API 密钥引用会自动重置为空。'}
                  </p>
                </div>
                {isNormalUser && canDo('providers.create') ? (
                  <div className="panel-header-actions">
                    <button className="btn primary" disabled={saving} onClick={() => setProviderSheetOpen(true)}>添加输入 Provider</button>
                  </div>
                ) : null}
                {!isNormalUser ? (
                  <div className="panel-header-actions">
                    <button className="btn" disabled={saving || selectedExportProviderIDs.length === 0} onClick={() => void exportProviders(selectedExportProviderIDs)}>导出选中{selectedExportProviderIDs.length > 0 ? ` (${selectedExportProviderIDs.length})` : ''}</button>
                    <button className="btn" disabled={saving || sortedProviders.length === 0} onClick={() => void exportProviders()}>导出全部</button>
                    <button className="btn" disabled={saving || importingProviders} onClick={() => providerImportInputRef.current?.click()}>导入</button>
                    {canDo('providers.create') ? <button className="btn primary" disabled={saving} onClick={() => setProviderSheetOpen(true)}>添加输入 Provider</button> : null}
                    <input
                      ref={providerImportInputRef}
                      type="file"
                      accept="application/json,.json"
                      hidden
                      onChange={(event) => {
                        const file = event.target.files?.[0];
                        if (file) void importProvidersFromFile(file);
                      }}
                    />
                  </div>
                ) : null}
              </div>
              <div className="providers-filter-toolbar">
                <div className="models-search-row">
                  {/* Chrome 会无视 autoComplete="off"（尤其同页存在密码框时），把这类
                      文本框当成登录表单的用户名字段，自动灌入保存的账号——实测按 ESC
                      关弹窗后此框被填成 "admin"。给一个语义明确、与登录无关的值
                      （one-time-code）Chrome 才会真正放弃填充；再叠加 data-1p/lpignore
                      关掉 1Password / LastPass 的注入。 */}
                  <input
                    className="models-search-input"
                    type="search"
                    autoComplete="one-time-code"
                    data-1p-ignore
                    data-lpignore="true"
                    name="providerFilterKeyword"
                    value={providersSearchQuery}
                    placeholder="按名称 / ID / 地址检索，支持正则，如 tuya|claude"
                    {...antiAutofillProps()}
                    onChange={(event) => {
                      const next = event.target.value;
                      reportIfLooksLikeAutofill('provider-filter-filled', providersSearchQuery, next, event);
                      setProvidersSearchQuery(next);
                    }}
                    aria-label="Provider 名称检索"
                  />
                  {providersSearchQuery.trim() ? (
                    <button className="mini-btn" type="button" onClick={() => setProvidersSearchQuery('')}>清除</button>
                  ) : null}
                  <div className="view-mode-toggle provider-density-toggle" role="group" aria-label="Provider 卡片密度">
                    <button
                      type="button"
                      className={`view-mode-btn ${providerCardDensity === 'standard' ? 'active' : ''}`}
                      title="标准卡片：信息更宽松"
                      onClick={() => chooseProviderDensity('standard')}
                    >
                      标准
                    </button>
                    <button
                      type="button"
                      className={`view-mode-btn ${providerCardDensity === 'compact' ? 'active' : ''}`}
                      title="紧凑列表：一行一个 Provider，只看额度、模型数与 Key 数"
                      onClick={() => chooseProviderDensity('compact')}
                    >
                      紧凑
                    </button>
                  </div>
                </div>
                {providersSearch.error ? (
                  <div className="hint-line error">正则无效，已回退为普通包含匹配：{providersSearch.error}</div>
                ) : null}
                <div className="models-filter-group">
                  {sortedProviders.some((p) => !p.disabled && p.healthStatus === 'unavailable') ? (
                    <button
                      type="button"
                      className={`models-filter-chip ${providersStatusFilter === 'unavailable' ? 'active' : ''}`}
                      onClick={() => setProvidersStatusFilter(providersStatusFilter === 'unavailable' ? 'all' : 'unavailable')}
                      title="仅看异常待试的 Provider"
                    >
                      <span>异常待试</span>
                      <span className="models-filter-chip-count">({sortedProviders.filter((p) => !p.disabled && p.healthStatus === 'unavailable').length})</span>
                    </button>
                  ) : null}
                  {sortedProviders.some((p) => !!p.disabled) ? (
                    <button
                      type="button"
                      className={`models-filter-chip ${providersStatusFilter === 'disabled' ? 'active' : ''}`}
                      onClick={() => setProvidersStatusFilter(providersStatusFilter === 'disabled' ? 'all' : 'disabled')}
                      title="仅看已停用的 Provider"
                    >
                      <span>已停用</span>
                      <span className="models-filter-chip-count">({sortedProviders.filter((p) => !!p.disabled).length})</span>
                    </button>
                  ) : null}
                  {providersStatusFilter !== 'all' ? (
                    <button
                      type="button"
                      className="models-filter-chip"
                      onClick={() => setProvidersStatusFilter('all')}
                      title="清除状态筛选"
                    >
                      <span>清除状态</span>
                    </button>
                  ) : null}
                  {(() => {
                    // 只列真的配了的连接方式：0 个的胶囊筛出来是空列表，留着只是噪音。
                    const counts = new Map<string, number>();
                    for (const provider of sortedProviders) {
                      const kind = providerConnectKind(provider);
                      counts.set(kind, (counts.get(kind) || 0) + 1);
                    }
                    const visible = PROVIDER_CONNECT_FILTERS.filter((item) => item.id === '' || (counts.get(item.id) || 0) > 0);
                    return visible.map((item) => {
                      const icon = providerConnectIcon(item.id);
                      const count = item.id === '' ? sortedProviders.length : (counts.get(item.id) || 0);
                      return (
                        <button
                          key={item.id || 'all'}
                          type="button"
                          className={`models-filter-chip${icon ? ' with-icon' : ''} ${providersConnectFilter === item.id ? 'active' : ''}`}
                          onClick={() => setProvidersConnectFilter(item.id)}
                        >
                          {icon ? <BrandIcon icon={icon} name={item.label} size={14} /> : null}
                          <span>{item.label}</span>
                          <span className="models-filter-chip-count">({count})</span>
                        </button>
                      );
                    });
                  })()}
                </div>
                {isNormalUser || !canDo('providers.create') ? (
                  <div className="models-toolbar-meta">
                    显示 {filteredProviders.length} / {sortedProviders.length}
                    {providersStatusFilter !== 'all' ? ` · 状态「${providersStatusFilter === 'healthy' ? '运行健康' : providersStatusFilter === 'unavailable' ? '异常待试' : providersStatusFilter === 'disabled' ? '已停用' : 'OAuth'}」` : ''}
                    {providersSearchQuery.trim() ? ` · 检索「${providersSearchQuery.trim()}」` : ''}
                    {providersConnectFilter ? ` · ${providerConnectLabel(providersConnectFilter)}` : ''}
                  </div>
                ) : null}
              </div>
              {!isNormalUser && canDo('providers.create') && filteredProviders.length > 0 ? (
                <div className="providers-toolbar">
                  <label className="checkbox-field">
                    <input
                      type="checkbox"
                      checked={filteredProviders.length > 0 && filteredProviders.every((provider) => selectedExportProviderIDs.includes(provider.id))}
                      onChange={(event) => {
                        if (event.target.checked) {
                          setSelectedExportProviderIDs((current) => {
                            const next = new Set(current);
                            for (const provider of filteredProviders) next.add(provider.id);
                            return [...next];
                          });
                        } else {
                          const remove = new Set(filteredProviders.map((provider) => provider.id));
                          setSelectedExportProviderIDs((current) => current.filter((id) => !remove.has(id)));
                        }
                      }}
                    />
                    <span>全选</span>
                  </label>
                  <span className="providers-toolbar-meta">
                    {(selectedExportProviderIDs.length > 0 ? `已选 ${selectedExportProviderIDs.length} / ` : '共 ')}{sortedProviders.length} · 显示 {filteredProviders.length} / {sortedProviders.length}
                    {providersStatusFilter !== 'all' ? ` · 状态「${providersStatusFilter === 'healthy' ? '运行健康' : providersStatusFilter === 'unavailable' ? '异常待试' : providersStatusFilter === 'disabled' ? '已停用' : 'OAuth'}」` : ''}
                    {providersSearchQuery.trim() ? ` · 检索「${providersSearchQuery.trim()}」` : ''}
                    {providersConnectFilter ? ` · ${providerConnectLabel(providersConnectFilter)}` : ''}
                  </span>
                  {selectedExportProviderIDs.length > 0 ? (
                    <span className="providers-toolbar-actions">
                      <button className="mini-btn" type="button" disabled={saving} onClick={() => void batchTestSelectedProviders()}>批量探测</button>
                      <button className="mini-btn" type="button" disabled={saving} onClick={() => void batchToggleSelectedProviders(true)}>批量启用</button>
                      <button className="mini-btn" type="button" disabled={saving} onClick={() => void batchToggleSelectedProviders(false)}>批量停用</button>
                      <button className="mini-btn danger" type="button" disabled={saving} onClick={() => void deleteSelectedProviders()}>批量删除</button>
                      <button className="mini-btn" type="button" onClick={clearExportProviderSelection}>清除选择</button>
                    </span>
                  ) : null}
                </div>
              ) : null}
              {sortedProviders.length === 0 ? (
                <div className="empty-state">
                  {isNormalUser ? '暂无可用 Provider。可点击「添加输入 Provider」创建自己的 Provider，或联系管理员为你分配。' : '暂无 Provider。点击「添加输入 Provider」创建。'}
                </div>
              ) : filteredProviders.length === 0 ? (
                <div className="empty-state">没有匹配的 Provider，请调整检索词或连接方式过滤。</div>
              ) : (
                <div className={providerCardDensity === 'compact' ? 'provider-list' : 'provider-card-grid'}>
                  {providerCardDensity === 'compact' ? (
                    <div className="provider-list-head" aria-hidden="true">
                      <span />
                      <span />
                      <span>Provider</span>
                      <span>额度</span>
                      <span>模型 / Keys</span>
                      <span className="provider-list-head-actions">操作</span>
                    </div>
                  ) : null}
                  {filteredProviders.map((provider) => {
                    const usedCount = (state.apiKeys || []).filter((key) => (
                      apiKeyReferencesProvider(key, state.routes, provider.id)
                    )).length;
                    return (
                      <ProviderCard
                        key={provider.id}
                        layout={providerCardDensity === 'compact' ? 'list' : 'card'}
                        active={selectedProvider?.id === provider.id}
                        selected={selectedExportProviderIDs.includes(provider.id)}
                        name={provider.name}
                        providerId={provider.id}
                        protocol={protocolLabel(provider.protocol)}
                        tone={protocolTone(provider.protocol)}
                        url={provider.authType === 'claude_oauth' ? 'api.anthropic.com' : provider.authType === 'cursor_oauth' ? '本地 gRPC Bridge' : provider.authType === 'chatgpt_oauth' ? 'chatgpt.com/codex' : provider.baseUrl}
                        defaultModel={provider.defaultModel}
                        modelCount={(provider.models || []).length}
                        usedCount={usedCount}
                        healthStatus={provider.healthStatus || 'unchecked'}
                        nextRetryAt={provider.nextRetryAt}
                        testing={testingProviderID === provider.id}
                        chatTesting={chatTestingKeys.includes(`provider:${provider.id}`)}
                        readOnly={(isNormalUser && provider.ownerUserId !== authStatus?.userId) || !canDo('providers.edit')}
                        selectable={!isNormalUser && canDo('providers.create')}
                        providerDisabled={!!provider.disabled}
                        onToggleEnabled={!isNormalUser && canDo('providers.toggle') ? () => void toggleProviderEnabled(provider) : undefined}
                        onToggleRTK={!isNormalUser && canDo('providers.toggle') ? () => void toggleProviderRTK(provider) : undefined}
                        subtitle={providerAccountLabel(provider) || undefined}
                        icon={resolveProviderIcon(provider)}
                        isClaudeOAuth={provider.authType === 'claude_oauth'}
                        claudeOAuthConnected={provider.claudeOAuth?.connected}
                        isCursorOAuth={provider.authType === 'cursor_oauth'}
                        cursorOAuthConnected={provider.cursorOAuth?.connected}
                        isChatGPTOAuth={provider.authType === 'chatgpt_oauth'}
                        chatgptOAuthConnected={provider.chatgptOAuth?.connected}
                        isQoderPAT={isQoderConnectKind(provider.authType)}
                        qoderPATConnected={provider.qoderPat?.connected}
                        isWorkBuddy={isWorkBuddyConnectKind(provider.authType || 'api_key')}
                        workBuddyConnected={provider.workbuddyOAuth?.connected}
                        provider={provider}
                        cursorBridge={provider.authType === 'cursor_oauth' ? state.cursorBridge : undefined}
                        ownerName={authStatus?.scope === 'all' || canDo('usage.all') ? providerOwnerName(provider) : undefined}
                        highlightQuery={providersSearchQuery}
                        onToggleSelect={() => toggleExportProviderSelection(provider.id)}
                        onClick={() => {
                          setSelectedProviderID(provider.id);
                          showToast(`已选择输入 Provider：${provider.name}`);
                        }}
                        onTest={canDo('providers.test') ? () => void fetchProviderModels(provider.id, provider.name, true) : undefined}
                        onChatTest={canDo('providers.test') ? () => openChatTestForProvider(provider) : undefined}
                        onConformance={canDo('providers.test') ? () => void runProviderConformance(provider) : undefined}
                        onEdit={canDo('providers.edit') ? () => openEditProviderModal(provider) : undefined}
                        onClone={canDo('providers.create') ? () => openCloneProviderModal(provider) : undefined}
                        onDelete={canDo('providers.delete') ? () => void deleteProvider(provider.id, provider.name) : undefined}
                      />
                    );
                  })}
                </div>
              )}
              {selectedProvider && !isNormalUser ? <div className="hint-text">{`当前 Provider 被 ${activeProviderRouteCount} 个 API 密钥引用（含备选）。删除时这些密钥的 Provider 绑定会自动重置为空。`}</div> : null}
              <ProviderCatalogDialog
                open={providerSheetOpen}
                providers={state.providers}
                hasProviders={sortedProviders.length > 0}
                onClose={() => setProviderSheetOpen(false)}
                onPick={openProviderDraftForPreset}
              />
            </div>
          </section>
          )}

          {activeNav === 'output-providers' && navVisibleFor(permissions, 'output-providers', legacyUser) && (
          <section className="section-full">
            <div className="card panel">
              <div className="panel-header">
                <div>
                  <h2 className="panel-title">接入地址 · 开发者中心</h2>
                  <p className="panel-desc">网关对外提供三种标准大模型协议端点。将端点地址填入你的客户端工具或 SDK 中，即可无缝转发并享受协议转换与智能路由。</p>
                </div>
                <Badge tone={publicStatusTone(publicAccess.status)}>{publicAccessStatusLabel(publicAccess.status)}</Badge>
              </div>

              <div className="output-hero-card">
                <div className="output-hero-left">
                  <div className="output-hero-title">
                    <span>网关连通状态</span>
                    <Badge tone={clientPublicRoot ? 'green' : 'slate'}>
                      {tunnelRunning ? '公网穿透运行中' : browserOrigin ? '域名反代访问' : '仅局域网监听'}
                    </Badge>
                  </div>
                  <div className="output-hero-desc">
                    {tunnelRunning
                      ? `当前已通过 Cloudflare 隧道穿透公网，外网客户端可通过 HTTPS 访问模型 API；Web 管理页具备独立安全鉴权。`
                      : browserOrigin
                        ? `当前通过 ${browserOrigin} 提供公网访问，外网客户端直接使用下面的公网地址即可；Web 管理页要求登录。`
                        : `网关当前仅在本地或局域网提供服务。如需外网访问，请在「公网访问」页一键开启免费隧道或绑定自有域名。`}
                  </div>
                </div>
                <div className="output-hero-actions">
                  <button className="btn" type="button" onClick={() => { copy(localClientRoot); showToast(`已复制${webExposed ? '局域网' : '本机'}根地址：${localClientRoot}`); }}>
                    复制{webExposed ? '局域网' : '本机'}根地址
                  </button>
                  {clientPublicRoot ? (
                    <button className="btn primary" type="button" onClick={() => { copy(clientPublicRoot); showToast(`已复制公网根地址：${clientPublicRoot}`); }}>
                      复制公网根地址
                    </button>
                  ) : null}
                </div>
              </div>

              <div className="output-showcase-grid">
                {state.endpoints.map((endpoint) => {
                  const url = endpointURL(endpoint);
                  // 公网地址：隧道给的是每端点完整 URL（含 basePath）；只有域名时按当前
                  // 浏览器 origin 拼，和「复制公网根地址」保持同一口径。
                  const tunnelEndpointURL = tunnelRunning && endpoint.publicUrl ? publicAccessURL(endpoint, tunnelRunning) : '';
                  const publicEndpointURL = tunnelEndpointURL || (clientPublicRoot ? `${clientPublicRoot}${endpoint.basePath}` : '');
                  const isClaude = endpoint.protocol === 'claude';
                  const isResponses = endpoint.protocol === 'openai_responses';
                  const title = isClaude ? 'Anthropic Claude Messages' : isResponses ? 'OpenAI Responses (Codex)' : 'OpenAI Chat Completions';
                  const desc = isClaude
                    ? '兼容 Claude Code CLI、Cline、Roo Code 及 Anthropic 官方 SDK。支持自动转换为 OpenAI 上游模型或透传。'
                    : isResponses
                      ? '兼容 OpenAI Responses 格式与 Codex 命令行工具，支持流式交互与官方登录态保留。'
                      : '兼容标准 OpenAI SDK、LangChain、LobeChat、Cherry Studio、NextChat、Cursor 等绝大多数 AI 工具生态。';

                  // 绑到回环时「局域网」这行其实是假的：自报网卡 IP 那个端口已经不对外，
                  // 只有本机进程能用 127.0.0.1。
                  const localEndpointURL = webExposed ? url : `http://127.0.0.1:${endpoint.listenPort}${endpoint.basePath}`;
                  const localEndpointTag = webExposed ? '局域网' : '本机';
                  // 示例优先公网地址；没有公网地址时给当前控制台真正能连通的那个。
                  const activeTestUrl = publicEndpointURL || localEndpointURL;
                  const sampleCurl = isClaude
                    ? `curl -X POST "${activeTestUrl}" \\\n  -H "Content-Type: application/json" \\\n  -H "x-api-key: sk-gw-YOUR_KEY" \\\n  -H "anthropic-version: 2023-06-01" \\\n  -d '{"model": "claude-3-7-sonnet-20250219", "max_tokens": 100, "messages": [{"role": "user", "content": "ping"}]}'`
                    : isResponses
                      ? `curl -X POST "${activeTestUrl}" \\\n  -H "Content-Type: application/json" \\\n  -H "Authorization: Bearer sk-gw-YOUR_KEY" \\\n  -d '{"model": "gpt-4o", "input": "ping"}'`
                      : `curl -X POST "${activeTestUrl}" \\\n  -H "Content-Type: application/json" \\\n  -H "Authorization: Bearer sk-gw-YOUR_KEY" \\\n  -d '{"model": "gpt-4o", "messages": [{"role": "user", "content": "ping"}]}'`;

                  const features = isClaude
                    ? ['流式输出 (SSE)', '深度思考 (Extended Thinking)', '提示词缓存 (Prompt Caching)', '协议互转']
                    : isResponses
                      ? ['流式输出 (SSE)', 'Responses 协议', 'Codex 官方兼容', '多方案热切换']
                      : ['流式输出 (SSE)', '多轮对话', 'Function Calling', '智能模型重定向', '高可用备选'];

                  return (
                    <div className="output-card" key={endpoint.id}>
                      <div className="output-card-head">
                        <div className="output-card-title-group">
                          <div className="output-card-title">
                            <span className="output-method-pill">POST</span>
                            <span>{title}</span>
                          </div>
                          <span className="output-card-path">{endpoint.basePath}</span>
                        </div>
                        <Badge tone="slate">{protocolLabel(endpoint.protocol)}</Badge>
                      </div>

                      <div className="output-card-desc">{desc}</div>

                      <div className="output-url-group">
                        <div className="output-url-item">
                          <span className="output-url-tag">{localEndpointTag}</span>
                          <span className="output-url-code" title={localEndpointURL}>{localEndpointURL}</span>
                          <button className="mini-btn" type="button" onClick={() => { copy(localEndpointURL); showToast(`已复制${localEndpointTag}地址：${localEndpointURL}`); }}>复制</button>
                        </div>
                        <div className="output-url-item">
                          <span className="output-url-tag">公网</span>
                          <span className="output-url-code" title={publicEndpointURL || url}>
                            {publicEndpointURL || '未配置公网地址'}
                          </span>
                          {publicEndpointURL ? (
                            <button className="mini-btn" type="button" onClick={() => { copy(publicEndpointURL); showToast(`已复制公网地址：${publicEndpointURL}`); }}>复制</button>
                          ) : (
                            <button className="mini-btn" type="button" onClick={() => goToPage('public-access')}>去开启</button>
                          )}
                        </div>
                      </div>

                      <div className="output-curl-box">
                        <div className="output-curl-head">
                          <span>快速测试命令 (Terminal)</span>
                          <button
                            className="mini-btn"
                            type="button"
                            onClick={() => { copy(sampleCurl); showToast('已复制测试 curl 命令'); }}
                          >
                            复制代码
                          </button>
                        </div>
                        <pre className="output-curl-code">{sampleCurl}</pre>
                      </div>

                      <div className="output-features-list">
                        {features.map((feat) => (
                          <span className="output-feature-pill" key={feat}>{feat}</span>
                        ))}
                      </div>
                    </div>
                  );
                })}
              </div>
            </div>
          </section>
          )}

          {activeNav === 'usage-stats' && (
            <UsagePage
              stats={requestStats}
              providers={state.providers || []}
              models={state.models || []}
              isNormalUser={isNormalUser}
              serverTimeZone={serverTimeZone}
              from={usageFrom}
              to={usageTo}
              loading={usageLoading}
              error={usageError}
              fetchedAt={dataFetchedAt}
              onRangeChange={(from, to) => {
                usageFollowTodayRef.current = isFollowingTodayRange(from, to);
                usageRangeRef.current = { from, to };
                setUsageFrom(from);
                setUsageTo(to);
                void refreshRequestStats(from, to);
              }}
              onRefresh={() => { void refreshRequestStats(); if (!isNormalUser) void refreshRTKStats(); }}
              onNavigateLogs={({ type, value, label }) => {
                setLogsFrom(usageFrom);
                setLogsTo(usageTo);
                setLogsKeyword('');
                setLogsUsageTarget(type === 'provider' ? null : { type, value, label });
                setLogsProviderFilter(type === 'provider' ? value : '');
                setLogsApiKeyName('');
                setLogsStatusFilter('all');
                setLogsPage(1);
                setLogsOwnerFilter('');
                goToPage('traffic-tokens');
              }}
              onOpenLogs={(filters) => {
                setLogsFrom(usageFrom);
                setLogsTo(usageTo);
                setLogsKeyword(filters.keyword || '');
                setLogsUsageTarget(filters.model
                  ? { type: 'model', value: filters.model, label: filters.model }
                  : filters.apiKeyId
                    ? { type: 'apiKey', value: filters.apiKeyId, label: filters.apiKeyName || filters.apiKeyId }
                    : null);
                setLogsProviderFilter(filters.provider || '');
                setLogsApiKeyName('');
                setLogsStatusFilter((filters.status as 'all' | '2xx' | '4xx' | '5xx') || 'all');
                setLogsPage(1);
                setLogsOwnerFilter('');
                goToPage('traffic-tokens');
              }}
              rtkStats={rtkStats}
              rtkDays={rtkStatsDays}
              onRTKDaysChange={setRtkStatsDays}
            />
          )}

          {activeNav === 'public-access' && navVisibleFor(permissions, 'public-access', legacyUser) && (
          <section className="section-grid public-access-grid">
            <div className="card panel" style={{ gridColumn: '1 / -1' }}>
              <div className="panel-header">
                <div>
                  <h2 className="panel-title">局域网访问</h2>
                  <p className="panel-desc">总开关：关闭后仅本机 127.0.0.1 可访问；开启后局域网与 Cloudflare 穿透才可用。本机 App 始终可用。</p>
                </div>
                <Badge tone={webExposed ? 'green' : 'slate'}>{webExposed ? '已开启' : '仅本机'}</Badge>
              </div>
              <div className="public-simple-card">
                <label className="toggle-row" style={{ display: 'flex', alignItems: 'center', gap: 12, cursor: 'pointer' }}>
                  <input
                    type="checkbox"
                    checked={webExposed}
                    disabled={saving}
                    onChange={(e) => void updateWebExposed(e.target.checked)}
                  />
                  <span>开启局域网 / 穿透访问</span>
                </label>
                <div className="hint-text">{webExposed
                    ? '管理页公网需管理员密码；模型 API 使用 API Key。可在下方分别开启管理页或 API 公网。'
                    : '关闭后局域网与穿透均不可用；若公网隧道在跑会自动停止。'}</div>
                <URLRow label="局域网地址" value={lanAccessURL} onCopy={webExposed && lanHostHint ? () => copy(lanAccessURL) : undefined} />
              </div>
            </div>

            <div className="card panel">
              <div className="panel-header">
                <div>
                  <h2 className="panel-title">管理页公网</h2>
                  <p className="panel-desc">独立开关。开启后用管理页域名从外网打开控制台；可与模型 API 公网分开启用。</p>
                </div>
                <Badge tone={publicDraft.exposeUi !== false && customTunnelActive && !!liveUIPublicURL ? 'green' : publicDraft.exposeUi !== false ? 'blue' : 'slate'}>
                  {publicDraft.exposeUi === false ? '未启用' : (liveUIPublicURL && customTunnelActive ? '运行中' : '待绑定')}
                </Badge>
              </div>
              <div className="public-simple-card">
                <label className="toggle-row" style={{ display: 'flex', alignItems: 'center', gap: 12, cursor: 'pointer' }}>
                  <input
                    type="checkbox"
                    checked={publicDraft.exposeUi !== false}
                    onChange={(e) => setPublicDraft((current) => ({ ...current, exposeUi: e.target.checked }))}
                  />
                  <span>启用管理页公网域名</span>
                </label>
                <div className="form-grid compact">
                  <Field label="管理页子域名前缀" value={uiDomainPrefix} onChange={setUIDomainPrefix} placeholder="console" />
                  <div className="field">
                    <label>根域名</label>
                    {cloudflareZones.length > 1 ? (
                      <select value={customDomainRoot} onChange={(event) => setCustomDomainRoot(event.target.value)}>
                        {cloudflareZones.map((zone) => (
                          <option key={zone.id || zone.name} value={zone.name}>{zone.name}</option>
                        ))}
                      </select>
                    ) : (
                      <div className="field-readonly">
                        {customDomainRoot || (cloudflareAuthorized ? '未获取到可用域名' : '授权后自动获取')}
                      </div>
                    )}
                  </div>
                </div>
                <div className="hint-text">{`管理页面：${composedUIDomain ? `https://${composedUIDomain}` : '—'}`}</div>
                {customTunnelActive && liveUIPublicURL ? (
                  <URLRow label="管理页公网" value={liveUIPublicURL} onCopy={() => copy(liveUIPublicURL)} />
                ) : null}
              </div>
            </div>

            <div className="card panel">
              <div className="panel-header">
                <div>
                  <h2 className="panel-title">模型 API 公网</h2>
                  <p className="panel-desc">独立开关。开启后客户端用 API 域名调用模型；与管理页公网互不影响。</p>
                </div>
                <Badge tone={publicDraft.exposeApi !== false && customTunnelActive && !!livePublicURL ? 'green' : publicDraft.exposeApi !== false ? 'blue' : 'slate'}>
                  {publicDraft.exposeApi === false ? '未启用' : (livePublicURL && customTunnelActive ? '运行中' : '待绑定')}
                </Badge>
              </div>
              <div className="public-simple-card">
                <label className="toggle-row" style={{ display: 'flex', alignItems: 'center', gap: 12, cursor: 'pointer' }}>
                  <input
                    type="checkbox"
                    checked={publicDraft.exposeApi !== false}
                    onChange={(e) => setPublicDraft((current) => ({ ...current, exposeApi: e.target.checked }))}
                  />
                  <span>启用模型 API 公网域名</span>
                </label>
                <div className="form-grid compact">
                  <Field label="API 子域名前缀" value={customDomainPrefix} onChange={setCustomDomainPrefix} placeholder="gateway" />
                  <div className="field">
                    <label>根域名</label>
                    <div className="field-readonly">
                      {customDomainRoot || (cloudflareAuthorized ? '未获取到可用域名' : '授权后自动获取')}
                    </div>
                  </div>
                </div>
                <div className="hint-text">{`模型 API：${composedCustomDomain ? `https://${composedCustomDomain}` : '—'}`}</div>
                {customTunnelActive && livePublicURL && publicDraft.exposeApi !== false ? (
                  <URLRow label="模型 API 公网" value={livePublicURL} onCopy={() => copy(livePublicURL)} />
                ) : null}
              </div>
            </div>

            <div className="card panel" style={{ gridColumn: '1 / -1' }}>
              <div className="panel-header">
                <div>
                  <h2 className="panel-title">Cloudflare 绑定</h2>
                  <p className="panel-desc">按上方勾选的管理页 / API 公网分别创建 DNS 与隧道入口。两者可只开其一。</p>
                </div>
                <Badge tone={customTunnelActive ? 'green' : cloudflareAuthorized ? 'blue' : 'slate'}>
                  {customTunnelActive ? '隧道运行中' : cloudflareAuthorized ? '已连接 Cloudflare' : '未授权'}
                </Badge>
              </div>
              <div className="public-simple-card">
                {composedCustomDomain && composedUIDomain && publicDraft.exposeApi !== false && publicDraft.exposeUi !== false
                  && composedCustomDomain.toLowerCase() === composedUIDomain.toLowerCase() ? (
                  <div className="hint-line error">API 与管理页不能使用同一子域名</div>
                ) : null}
                {cloudflareAuthPending ? (
                  <div className="hint-text">正在等待 Cloudflare 授权，请在浏览器中完成登录并选择要绑定的域名…</div>
                ) : (
                  <div className="hint-text">根域名来自 Cloudflare 授权。管理页域名只服务控制台，API 域名只服务模型协议。</div>
                )}
                <div className="public-simple-actions">
                  {customTunnelActive ? (
                    <button className="btn danger" disabled={tunnelBusy} onClick={() => void stopPublicAccess()}>{tunnelBusy ? '处理中…' : '关闭域名隧道'}</button>
                  ) : null}
                  <button
                    className="btn primary"
                    disabled={
                      tunnelBusy
                      || quickTunnelActive
                      || cloudflareAuthPending
                      || !webExposed
                      || (publicDraft.exposeApi === false && publicDraft.exposeUi === false)
                      || !!(publicDraft.exposeApi !== false && publicDraft.exposeUi !== false && composedCustomDomain && composedUIDomain && composedCustomDomain.toLowerCase() === composedUIDomain.toLowerCase())
                    }
                    onClick={() => void connectCloudflareAndBind({
                      exposeApi: publicDraft.exposeApi !== false,
                      exposeUi: publicDraft.exposeUi !== false,
                    })}
                  >
                    {cloudflareAuthPending ? '等待授权…' : tunnelBusy ? '绑定中…' : (customTunnelActive ? '重新绑定并应用' : '连接 Cloudflare 并绑定')}
                  </button>
                  {!webExposed ? <div className="hint-text">请先开启局域网 / 穿透访问。</div> : null}
                  {publicDraft.exposeApi === false && publicDraft.exposeUi === false ? (
                    <div className="hint-text">请先开启「管理页公网」或「模型 API 公网」至少一个开关。</div>
                  ) : null}
                  {quickTunnelActive ? <div className="hint-text">当前正在使用快速隧道，需先关闭后再启域名隧道。</div> : null}
                </div>
                {tunnel?.status === 'error' && publicAccess.mode === 'custom_domain' ? <div className="hint-line error">{tunnel.message}</div> : null}
                <details className="public-advanced" open={showManualToken} onToggle={(event) => setShowManualToken((event.currentTarget as HTMLDetailsElement).open)}>
                  <summary>高级：手动 Token / 快速隧道</summary>
                  <div className="public-advanced-body">
                    <Field
                      label="隧道 Token"
                      value={customTunnelToken}
                      onChange={setCustomTunnelToken}
                      placeholder={publicDraft.tunnelToken ? '已保存 token，留空则复用' : '从 Cloudflare Zero Trust 复制 token'}
                    />
                    <div className="hint-text">若你已在 Zero Trust 手动创建隧道，可粘贴 token 后直接启动，无需浏览器授权。</div>
                    <div className="public-simple-actions">
                      <button
                        className="btn"
                        disabled={tunnelBusy || quickTunnelActive || customTunnelActive || !webExposed}
                        onClick={() => void startCustomDomainTunnel()}
                      >
                        {tunnelBusy ? '启动中…' : '使用 Token 启动'}
                      </button>
                      {quickTunnelActive ? (
                        <button className="btn danger" disabled={tunnelBusy} onClick={() => void stopPublicAccess()}>{tunnelBusy ? '处理中…' : '关闭快速隧道'}</button>
                      ) : (
                        <button className="btn" disabled={tunnelBusy || customTunnelActive || !webExposed} onClick={() => void startQuickTunnel()}>
                          {tunnelBusy && !quickTunnelActive ? '开启中…' : '开启快速隧道（临时）'}
                        </button>
                      )}
                    </div>
                    {quickTunnelActive && livePublicURL ? (
                      <URLRow label="快速隧道地址" value={livePublicURL} onCopy={() => copy(livePublicURL)} />
                    ) : (
                      <div className="hint-text">快速隧道为临时单地址，管理页与 API 共用，适合测试。</div>
                    )}
                    {tunnel?.status === 'error' && publicAccess.mode === 'random_tunnel' ? <div className="hint-line error">{tunnel.message}</div> : null}
                  </div>
                </details>
              </div>
            </div>

            <div className="card panel" style={{ gridColumn: '1 / -1' }}>
              <div className="panel-header">
                <div>
                  <h2 className="panel-title">远程开关令牌（脚本 / LLM）</h2>
                  <p className="panel-desc">
                    生成一个专用令牌后,脚本或 LLM 可以直接调接口开关公网访问,无需登录控制台。
                    令牌只能做这一件事,碰不到任何其它管理接口;且该接口<strong>只接受本机 / 局域网调用</strong>,
                    从公网域名调用会被拒绝。
                  </p>
                </div>
                <Badge tone={publicControlTokenConfigured ? 'green' : 'slate'}>
                  {publicControlTokenConfigured ? '已启用' : '未启用'}
                </Badge>
              </div>
              <div className="public-simple-card">
                <div className="hint-text">{publicControlTokenConfigured
                    ? `已启用 · 令牌尾号 ****${publicControlTokenPreview || '----'}`
                    : '尚未生成令牌，生成后才能远程开关。'}</div>
                {publicControlRawToken ? (
                  <div className="field field-full">
                    <label>新令牌（只显示这一次，请立即复制保存）</label>
                    <div className="field-inline">
                      <input readOnly value={publicControlRawToken} onFocus={(event) => event.currentTarget.select()} />
                      <CopyButton value={publicControlRawToken} label="复制令牌" />
                    </div>
                  </div>
                ) : null}
                <div className="hint-text">操作：购买域名（custom_domain）与免费随机域名（random_tunnel）各自开关，底层只有一条隧道。安全限制：接口只在本机 / 局域网可用，公网调用一律 403；关掉公网后需回本机重新打开。</div>
                <div className="field field-full">
                  <label>调用示例（把 {'<令牌>'} 换成上面复制到的值）</label>
                  <pre className="curl-preview">{publicControlCurlExamples}</pre>
                  <div className="field-inline">
                    <CopyButton
                      value={publicControlCurlExamples}
                      label="复制调用示例"
                      toastContent="已复制调用示例，可直接贴给 LLM 使用"
                    />
                  </div>
                </div>
                <div className="public-simple-actions">
                  <button
                    className={publicControlTokenConfigured ? 'btn' : 'btn primary'}
                    type="button"
                    disabled={publicControlTokenBusy}
                    onClick={() => void generatePublicControlToken()}
                  >
                    {publicControlTokenBusy ? '处理中…' : publicControlTokenConfigured ? '重置令牌' : '生成令牌'}
                  </button>
                  {publicControlTokenConfigured ? (
                    <button
                      className="btn danger"
                      type="button"
                      disabled={publicControlTokenBusy}
                      onClick={() => void revokePublicControlToken()}
                    >
                      {publicControlTokenBusy ? '处理中…' : '撤销令牌'}
                    </button>
                  ) : null}
                </div>
              </div>
            </div>
          </section>
          )}

          {activeNav === 'models-menu' && (
          <section className="models-menu-section">
            <div className="card panel models-menu-panel">
              <div className="panel-header">
                <div>
                  <h2 className="panel-title">模型列表</h2>
                  <p className="panel-desc">
                    {isNormalUser
                      ? '仅展示管理员授权给你的输入 Provider 下的模型（只读）。可按 Provider 过滤或按名称检索，不可同步/获取模型。'
                      : '查看全部模型，或按输入 Provider 过滤；支持按名称关键字 / 正则检索。在 Provider 卡片点击「获取模型」可同步最新列表。'}
                  </p>
                </div>
                <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
                  <div className="view-mode-toggle">
                    <button
                      type="button"
                      className={`view-mode-btn ${modelsViewMode === 'grid' ? 'active' : ''}`}
                      onClick={() => setModelsViewMode('grid')}
                    >
                      卡片网格
                    </button>
                    <button
                      type="button"
                      className={`view-mode-btn ${modelsViewMode === 'table' ? 'active' : ''}`}
                      onClick={() => setModelsViewMode('table')}
                    >
                      紧凑表格
                    </button>
                  </div>
                  <button className="btn" onClick={() => void refreshState()}>刷新列表</button>
                </div>
              </div>
              <div className="models-toolbar">
                <div className="models-search-row">
                  <input
                    className="models-search-input"
                    type="search"
                    autoComplete="one-time-code"
                    data-1p-ignore
                    data-lpignore="true"
                    name="modelFilterKeyword"
                    value={modelsSearchQuery}
                    placeholder="按名称检索，支持正则，如 gpt-5\\.6|sonnet"
                    {...antiAutofillProps()}
                    onChange={(event) => {
                      reportIfLooksLikeAutofill('model-filter-filled', modelsSearchQuery, event.target.value, event);
                      setModelsSearchQuery(event.target.value);
                    }}
                    aria-label="模型名称检索"
                  />
                  {modelsSearchQuery.trim() ? (
                    <button className="mini-btn" type="button" onClick={() => setModelsSearchQuery('')}>清除</button>
                  ) : null}
                </div>
                {modelsSearch.error ? (
                  <div className="hint-line error">正则无效，已回退为普通包含匹配：{modelsSearch.error}</div>
                ) : null}
                {/* 一级：服务商。同一个服务商登了几个账号就只有一个胶囊，
                    账号数写在旁边，点开后在下面二级行里挑具体账号。 */}
                <div className="models-filter-group">
                  <button
                    className={`models-filter-chip ${modelsBrandFilter === '__all__' ? 'active' : ''}`}
                    onClick={() => {
                      setModelsBrandFilter('__all__');
                      setModelsProviderFilter('__all__');
                    }}
                  >
                    全部 ({state.models.length})
                  </button>
                  {modelsProviderGroups.map((group) => {
                    const active = modelsBrandFilter === group.key;
                    const total = group.items.reduce((sum, provider) => sum + (modelsMenuSummary.get(provider.id) || 0), 0);
                    return (
                      <button
                        key={group.key}
                        className={`models-filter-chip with-icon ${active ? 'active' : ''}`}
                        title={group.items.length > 1 ? `${group.title} · ${group.items.length} 个账号` : group.title}
                        onClick={() => {
                          setModelsBrandFilter(active ? '__all__' : group.key);
                          setModelsProviderFilter('__all__');
                        }}
                      >
                        <BrandIcon icon={group.icon} name={group.title} size={14} />
                        <span className="models-filter-brand">{group.title}</span>
                        {group.items.length > 1 ? <span className="models-filter-chip-tag">{group.items.length} 个账号</span> : null}
                        <span className="models-filter-count">{total}</span>
                      </button>
                    );
                  })}
                </div>
                {/* 二级：当前服务商的账号。只有多账号的服务商才展开这一行。 */}
                {modelsActiveGroup && modelsActiveGroup.items.length > 1 ? (
                  <div className="models-filter-group models-filter-subgroup">
                    <span className="filter-group-label" title={`${modelsActiveGroup.title} 的账号`}>账号：</span>
                    <button
                      className={`models-filter-chip ${modelsProviderFilter === '__all__' ? 'active' : ''}`}
                      onClick={() => setModelsProviderFilter('__all__')}
                    >
                      全部账号
                      {' ('}
                      {modelsActiveGroup.items.reduce((sum, provider) => sum + (modelsMenuSummary.get(provider.id) || 0), 0)}
                      {')'}
                    </button>
                    {modelsActiveGroup.items.map((provider) => {
                      const account = shortenAccountLabel(providerAccountLabel(provider)) || provider.name;
                      return (
                        <button
                          key={provider.id}
                          className={`models-filter-chip ${modelsProviderFilter === provider.id ? 'active' : ''}`}
                          title={account !== provider.name ? `${provider.name} · ${account}` : provider.name}
                          onClick={() => setModelsProviderFilter(modelsProviderFilter === provider.id ? '__all__' : provider.id)}
                        >
                          <span className="models-filter-account">{account}</span>
                          <span className="models-filter-count">{modelsMenuSummary.get(provider.id) || 0}</span>
                        </button>
                      );
                    })}
                  </div>
                ) : null}
                <div className="models-filter-group capability-filter-group">
                  <span className="filter-group-label">能力特性：</span>
                  <button
                    type="button"
                    className={`models-filter-chip ${modelsCapabilityFilter === 'all' ? 'active' : ''}`}
                    onClick={() => setModelsCapabilityFilter('all')}
                  >
                    全部 ({modelsCapabilityCounts.all})
                  </button>
                  <button
                    type="button"
                    className={`models-filter-chip ${modelsCapabilityFilter === 'thinking' ? 'active' : ''}`}
                    onClick={() => setModelsCapabilityFilter(modelsCapabilityFilter === 'thinking' ? 'all' : 'thinking')}
                  >
                    思考 / 推理 ({modelsCapabilityCounts.thinking})
                  </button>
                  <button
                    type="button"
                    className={`models-filter-chip ${modelsCapabilityFilter === 'vision' ? 'active' : ''}`}
                    onClick={() => setModelsCapabilityFilter(modelsCapabilityFilter === 'vision' ? 'all' : 'vision')}
                  >
                    视觉 / 多模态 ({modelsCapabilityCounts.vision})
                  </button>
                  <button
                    type="button"
                    className={`models-filter-chip ${modelsCapabilityFilter === 'context' ? 'active' : ''}`}
                    onClick={() => setModelsCapabilityFilter(modelsCapabilityFilter === 'context' ? 'all' : 'context')}
                  >
                    长上下文 (128K+) ({modelsCapabilityCounts.context})
                  </button>
                  {/* 只在"部分是免费"时出现：全免费或全不费时这个筛选没有信息量。 */}
                  {modelsCapabilityCounts.free > 0 && modelsCapabilityCounts.free < modelsCapabilityCounts.all ? (
                    <button
                      type="button"
                      className={`models-filter-chip ${modelsCapabilityFilter === 'free' ? 'active' : ''}`}
                      onClick={() => setModelsCapabilityFilter(modelsCapabilityFilter === 'free' ? 'all' : 'free')}
                    >
                      只选免费 ({modelsCapabilityCounts.free})
                    </button>
                  ) : null}
                </div>
                <div className="models-toolbar-meta">
                  当前显示 {filteredModels.length} 个模型
                  {modelsMergeAcrossAccounts ? '（同名模型已跨账号合并）' : ''}
                  {modelsCapabilityFilter !== 'all' ? ` · 特性「${modelsCapabilityFilter === 'thinking' ? '思考/推理' : modelsCapabilityFilter === 'vision' ? '视觉多模态' : '128K+ 上下文'}」` : ''}
                  {modelsFilterCaption ? ` · ${modelsFilterCaption}` : ''}
                  {modelsSearchQuery.trim() ? ` · 检索「${modelsSearchQuery.trim()}」` : ''}
                </div>
              </div>
              {state.models.length === 0 ? (
                <div className="empty-state">
                  {isNormalUser
                    ? '暂无可用模型。若已授权 Provider，请等待管理员同步模型列表后再刷新。'
                    : '暂无模型。点击输入 Provider 卡片上的「获取模型」后，会根据 Provider 接口自动拉取模型列表。'}
                </div>
              ) : filteredModels.length === 0 ? (
                <div className="empty-state">
                  {modelsSearchQuery.trim() || modelsCapabilityFilter !== 'all'
                    ? '没有匹配当前检索条件的模型，请调整关键字、正则或能力特性筛选。'
                    : '该 Provider 暂无模型记录。'}
                  {!isNormalUser && !modelsSearchQuery.trim() && modelsCapabilityFilter === 'all' && (() => {
                    const provider = state.providers.find((item) => item.id === modelsProviderFilter)
                      ?? (modelsActiveGroup?.items.length === 1 ? modelsActiveGroup.items[0] : undefined);
                    if (!provider) return null;
                    const canSync = provider.authType === 'cursor_oauth'
                      ? !!provider.cursorOAuth?.connected
                      : provider.authType === 'claude_oauth'
                        ? !!provider.claudeOAuth?.connected
                        : provider.authType === 'chatgpt_oauth'
                          ? !!provider.chatgptOAuth?.connected
                          : isQoderConnectKind(provider.authType)
                          ? !!provider.qoderPat?.connected
                        : true;
                    if (!canSync) return <div className="hint-text">请先完成 OAuth 连接后再同步模型。</div>;
                    return (
                      <div style={{ marginTop: 12 }}>
                        <button className="btn primary" type="button" disabled={testingProviderID === provider.id} onClick={() => void fetchProviderModels(provider.id, provider.name, false)}>
                          {testingProviderID === provider.id ? '同步中…' : '同步该 Provider 模型'}
                        </button>
                      </div>
                    );
                  })()}
                </div>
              ) : modelsViewMode === 'table' ? (
                <div className="models-table-wrap">
                  <table className="models-table">
                    <thead>
                      <tr>
                        <th>模型 ID</th>
                        <th>所属 Provider</th>
                        <th>能力特性</th>
                        <th>协议类型</th>
                        <th>上下文长度</th>
                        <th>状态</th>
                        <th style={{ textAlign: 'right' }}>操作</th>
                      </tr>
                    </thead>
                    <tbody>
                      {visibleModels.map((model) => {
                        const provider = state.providers.find((item) => item.id === model.providerId);
                        const isThinking = isThinkingModel(model.id);
                        const isVision = isVisionModel(model.id);
                        const isLongContext = (model.contextLength || 0) >= 128000;
                        return (
                          <tr key={`${model.providerId}-${model.id}`}>
                            <td className="model-id-cell">
                              <div
                                className="model-id-wrapper"
                                title={`点击复制模型 ID：${model.id}`}
                                onClick={() => void copy(model.id, `已复制模型 ID：${model.id}`)}
                              >
                                <span className="model-id-text">{model.id}</span>
                                <span className="model-id-copy-hint" aria-hidden="true">📋</span>
                              </div>
                              {modelAlias(model) ? <span className="model-id-alias">{modelAlias(model)}</span> : null}
                            </td>
                            <td>
                              {(model as MergedModel).providers && ((model as MergedModel).providers!.length) > 1
                                ? <span className="model-provider-count" title={(model as MergedModel).providers!.map((item) => item.name).join(' / ')}>{(model as MergedModel).providers!.length} 个账号可用</span>
                                : provider ? <ProviderLabel provider={provider} size={15} /> : <span className="provider-label missing">{model.providerId}</span>}
                            </td>
                            <td>
                              <div className="model-tags-row">
                                {isThinking ? <span className="model-tag reasoning">思考/推理</span> : null}
                                {isVision ? <span className="model-tag vision">多模态</span> : null}
                                {isLongContext ? <span className="model-tag context">128K+</span> : null}
                                <ModelPriceTags model={model} />
                                {!isThinking && !isVision && !isLongContext ? <span style={{ color: 'var(--muted)', fontSize: 12 }}>-</span> : null}
                              </div>
                            </td>
                            <td><Badge tone="slate">{protocolLabel(model.protocol)}</Badge></td>
                            <td className="model-ctx-cell">{model.contextLength.toLocaleString()}</td>
                            <td><Badge tone="slate">{model.inMenu ? '菜单' : '隐藏'}</Badge></td>
                            <td style={{ textAlign: 'right' }}>
                              <CopyButton
                                value={model.id}
                                label="复制"
                                className="copy-model-btn"
                                toastContent={`已复制模型 ID：${model.id}`}
                              />
                            </td>
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>
                </div>
              ) : (
                <>
                  <div className="model-grid">
                    {visibleModels.map((model) => {
                      const provider = state.providers.find((item) => item.id === model.providerId);
                      const isThinking = isThinkingModel(model.id);
                      const isVision = isVisionModel(model.id);
                      const isLongContext = (model.contextLength || 0) >= 128000;
                      return (
                        <div className="model-card compact" key={`${model.providerId}-${model.id}`}>
                          {(() => {
                            const sources = (model as MergedModel).providers;
                            // 18 个账号各画一枚图标会挤满卡片，只写数量，悬浮看完整名单。
                            if (!sources || sources.length < 2) return null;
                            return (
                              <div className="model-sources" title={sources.map((item) => providerOptionLabel(item)).join(' / ')}>
                                <span className="model-provider-count">{sources.length} 个账号可用</span>
                              </div>
                            );
                          })()}
                          <div className="model-card-head">
                            <div className="model-card-title-row">
                              <div className="model-id-main">
                                <span
                                  className="provider-name model-id-clickable"
                                  title={`点击复制模型 ID：${model.id}`}
                                  onClick={() => void copy(model.id, `已复制模型 ID：${model.id}`)}
                                >
                                  <span className="model-id-text">{model.id}</span>
                                  <span className="model-id-copy-hint" aria-hidden="true">📋</span>
                                </span>
                                {modelAlias(model) ? <span className="model-id-alias">{modelAlias(model)}</span> : null}
                              </div>
                              <Badge tone="slate">{model.inMenu ? '菜单' : '隐藏'}</Badge>
                            </div>
                            <CopyButton
                              value={model.id}
                              label="复制"
                              className="copy-model-btn"
                              toastContent={`已复制模型 ID：${model.id}`}
                            />
                          </div>
                          <div className="provider-meta" title={provider?.name || model.providerId}>
                            {(model as MergedModel).providers && ((model as MergedModel).providers!.length) > 1
                              ? null
                              : provider ? <ProviderLabel provider={provider} size={14} /> : model.providerId}
                          </div>
                          <div className="model-tags-row">
                            {isThinking ? <span className="model-tag reasoning">思考/推理</span> : null}
                            {isVision ? <span className="model-tag vision">视觉多模态</span> : null}
                            {isLongContext ? <span className="model-tag context">128K+ 上下文</span> : null}
                            <ModelPriceTags model={model} />
                          </div>
                          <div className="model-card-stats">
                            <span>上下文 {model.contextLength.toLocaleString()}</span>
                            <span>{protocolLabel(model.protocol)}</span>
                          </div>
                        </div>
                      );
                    })}
                  </div>
                  {filteredModels.length > visibleModels.length ? (
                    <div className="models-more">
                      <button className="btn" type="button" onClick={() => setModelsVisibleCount((n) => n + 60)}>加载更多</button>
                      <span className="models-more-count">已显示 {visibleModels.length} / {filteredModels.length} 个</span>
                    </div>
                  ) : null}
                </>
              )}
            </div>
          </section>
          )}

          {activeNav === 'traffic-tokens' && (
          // logs-page：与密钥全量表格同一套「吃满可视高度」口径（见 akc.css），
          // 日志列表在卡内滚动，卡片底边与侧栏底边齐平。
          <section className="section-full logs-page">
            <div className="card panel traffic-panel">
              <div className="panel-header">
                <div>
                  <h2 className="panel-title">API 日志</h2>
                  <p className="panel-desc">
                    全链路请求追踪与流量审计：支持按时间段、状态码、密钥及 Provider 筛选；展示首字延迟（TTFT）与各阶段耗时。
                  </p>
                </div>
              </div>

              {/* 筛选条：日志页的主人是日志本身，所以这里只占两行——一行「搜索 + 日期
                  + 高级」，一行「状态芯片」。日期用弹层里的日历，不再常驻一块月历把
                  半个屏幕吃掉；「记录 2xx 正文 / 命中数 / 更新于」这类低频信息收进
                  高级面板，主条只留「看完这段日志要做什么」。 */}
              <div className="traffic-toolbar">
                <div className="traffic-toolbar-row">
                  <div className="traffic-search-row">
                    <svg className="traffic-search-icon" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                      <circle cx="11" cy="11" r="8" />
                      <line x1="21" y1="21" x2="16.65" y2="16.65" />
                    </svg>
                    <input
                      type="search"
                      // 占位写短：搜索框不再独占整行，中等宽度下长句会被截成半截；
                      // 能搜哪些字段放进 title，需要时悬停即可见。
                      placeholder="搜索模型、路径、IP、报错…"
                      title="实时过滤：模型、请求路径、客户端 IP、报错摘要、密钥名称、账号"
                      value={logsKeyword}
                      onChange={(e) => setLogsKeyword(e.target.value)}
                      onKeyDown={(e) => { if (e.key === 'Enter') { setLogsPage(1); void refreshLogs(1); } }}
                    />
                    {logsKeyword ? (
                      <button type="button" className="traffic-search-clear" onClick={() => setLogsKeyword('')} title="清空搜索词">×</button>
                    ) : null}
                  </div>
                  <div className="traffic-date-field" ref={logsDateRef}>
                    <button
                      type="button"
                      className={`traffic-date-btn${logsDateOpen ? ' open' : ''}`}
                      onClick={() => setLogsDateOpen((open) => !open)}
                      aria-expanded={logsDateOpen}
                      title="选择日期区间"
                    >
                      <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                        <rect x="3.5" y="5" width="17" height="15.5" rx="2.4" />
                        <path d="M3.5 10h17M8 3v4M16 3v4" />
                      </svg>
                      <span>{logsDateLabel}</span>
                      <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="m6 10 6 6 6-6" /></svg>
                    </button>
                    {logsDateOpen ? (
                      <div className="traffic-date-pop">
                        <UsageRangeCalendar
                          from={logsFrom}
                          to={logsTo}
                          onSelect={(nextFrom, nextTo) => {
                            logsFollowTodayRef.current = isFollowingTodayRange(nextFrom, nextTo);
                            setLogsFrom(nextFrom);
                            setLogsTo(nextTo);
                            setLogsPage(1);
                            void refreshLogs(1, nextFrom, nextTo);
                          }}
                          onClear={() => {
                            logsFollowTodayRef.current = false;
                            setLogsFrom('');
                            setLogsTo('');
                            setLogsPage(1);
                            void refreshLogs(1, '', '');
                          }}
                        />
                      </div>
                    ) : null}
                  </div>
                  {/* 密钥 / Provider / 用户直接从高级面板提到主行：搜索框让出宽度后，
                      这三个是最常用的筛选，不该再藏在一次点击后面。 */}
                  <label className="traffic-inline-field">
                    <select
                      value={logsApiKeyName}
                      aria-label="按 API 密钥筛选"
                      title="按 API 密钥筛选"
                      onChange={(e) => { setLogsPage(1); setLogsApiKeyName(e.target.value); }}
                    >
                      <option value="">全部密钥</option>
                      {(state.apiKeys || []).map((key) => (
                        <option key={key.id} value={key.name}>{key.name}</option>
                      ))}
                    </select>
                  </label>
                  <div className="traffic-inline-field traffic-select">
                    <IconSelect
                      value={logsProviderFilter || ALL_PROVIDERS_VALUE}
                      placeholder="全部 Provider"
                      searchable={false}
                      renderPortal={false}
                      options={trafficProviderFilterOptions}
                      onChange={(next) => { setLogsPage(1); setLogsProviderFilter(next === ALL_PROVIDERS_VALUE ? '' : next); }}
                    />
                  </div>
                  {!isNormalUser ? (
                    <label className="traffic-inline-field">
                      <select
                        value={logsOwnerFilter}
                        aria-label="按所属用户筛选"
                        title="按所属用户筛选"
                        onChange={(e) => { setLogsPage(1); setLogsOwnerFilter(e.target.value); }}
                      >
                        <option value="">全部用户</option>
                        <option value={LOG_OWNER_FILTER_ADMIN}>内置 admin</option>
                        {consoleUsers.map((user) => (
                          <option key={user.id} value={user.id}>{user.username}</option>
                        ))}
                      </select>
                    </label>
                  ) : null}
                  <button
                    type="button"
                    className={`traffic-adv-btn${logsAdvancedOpen ? ' active' : ''}`}
                    onClick={() => setLogsAdvancedOpen((open) => !open)}
                    aria-expanded={logsAdvancedOpen}
                    title="更多：记录 2xx 正文、留存天数、重置筛选"
                  >
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M4 5.5h16l-6.2 7.4V20l-3.6-2v-5.1L4 5.5Z" /></svg>
                  </button>
                </div>

                <div className="traffic-status-row">
                  {([
                    { id: 'all', label: '全部状态', tone: '' },
                    { id: '2xx', label: '2xx 正常', tone: 'green' },
                    { id: '4xx', label: '4xx 限流/异常', tone: 'amber' },
                    { id: '5xx', label: '5xx 熔断/故障', tone: 'red' },
                  ] as const).map((chip) => (
                    <button
                      key={chip.id}
                      type="button"
                      className={`traffic-status-chip ${chip.tone} ${logsStatusFilter === chip.id ? 'active' : ''}`}
                      onClick={() => { setLogsPage(1); setLogsStatusFilter(chip.id); }}
                    >
                      {chip.label}
                    </button>
                  ))}
                  <span className="traffic-status-count" title="当前筛选条件命中的日志总数">
                    共 <b>{logsTotal}</b> 条
                  </span>
                  {/* 生效中的筛选条件在这里显形：进了高级面板、或者日期不是今天，
                      主条上仍看得见「现在按什么在看」。点一下就撤掉那一条。 */}
                  <span className="traffic-active-filters">
                    {logsProviderFilter ? (
                      <button type="button" className="traffic-filter-tag" onClick={() => { setLogsPage(1); setLogsProviderFilter(''); }}>
                        Provider：{providerDisplayLabel((state.providers || []).find((item) => item.id === logsProviderFilter), logsProviderFilter)} ×
                      </button>
                    ) : null}
                    {logsApiKeyName ? (
                      <button type="button" className="traffic-filter-tag" onClick={() => { setLogsPage(1); setLogsApiKeyName(''); }}>
                        密钥：{logsApiKeyName} ×
                      </button>
                    ) : null}
                    {logsOwnerFilter ? (
                      <button type="button" className="traffic-filter-tag" onClick={() => { setLogsPage(1); setLogsOwnerFilter(''); }}>
                        用户：{logsOwnerFilter === LOG_OWNER_FILTER_ADMIN ? '内置 admin' : (consoleUsers.find((u) => u.id === logsOwnerFilter)?.username || logsOwnerFilter)} ×
                      </button>
                    ) : null}
                    {logsUsageTarget ? (
                      <button type="button" className="traffic-filter-tag" onClick={() => { setLogsUsageTarget(null); setLogsPage(1); }}>
                        {logsUsageTarget.type === 'model' ? '模型' : '密钥'}：{logsUsageTarget.label} ×
                      </button>
                    ) : null}
                    {logsKeyword ? (
                      <button type="button" className="traffic-filter-tag" onClick={() => setLogsKeyword('')}>
                        关键词：{logsKeyword} ×
                      </button>
                    ) : null}
                  </span>
                  <span className="traffic-status-meta">
                    {dataFetchedAt ? <>更新于 {dataFetchedAt.toLocaleTimeString()} · </> : null}
                    第 {logsPage} / {Math.max(1, Math.ceil(logsTotal / logsPageSize))} 页
                  </span>
                </div>

                {logsAdvancedOpen ? (
                  <div className="traffic-advanced-row">
                    {!isNormalUser ? (
                      <label className="traffic-quick-switch" title="开启后，2xx 正常请求也将完整落盘请求体与响应体（便于全量审计，但会增加 SQLite 写入量）；关闭时仅 4xx/5xx 错误请求记录正文以保障极限吞吐。">
                        <input type="checkbox" checked={log2xxBodies} disabled={saving} onChange={(e) => void updateLog2xxBodies(e.target.checked)} />
                        <span>记录 2xx 成功正文</span>
                      </label>
                    ) : null}
                    {!isNormalUser ? <span className="traffic-advanced-note">历史留存：{requestLogRetentionDays} 天</span> : null}
                    {logsFrom || logsTo ? (
                      <button
                        className="mini-btn"
                        type="button"
                        onClick={() => {
                          logsFollowTodayRef.current = true;
                          setLogsFrom('');
                          setLogsTo('');
                          setLogsPage(1);
                          void refreshLogs(1, '', '');
                        }}
                      >
                        全部历史日期
                      </button>
                    ) : null}
                    <button
                      className="btn secondary"
                      type="button"
                      title="重置所有筛选项"
                      onClick={() => {
                        setLogsUsageTarget(null);
                        setLogsApiKeyName('');
                        setLogsProviderFilter('');
                        setLogsOwnerFilter('');
                        setLogsStatusFilter('all');
                        setLogsKeyword('');
                        setLogsPage(1);
                        void refreshLogs(1, logsFrom, logsTo, { apiKeyName: '' });
                      }}
                    >
                      重置筛选
                    </button>
                  </div>
                ) : null}
              </div>

              {decisionAggregate.length > 0 ? (
                <details
                  className="decision-aggregate"
                  open={decisionPanelOpen}
                  onToggle={(event) => {
                    const open = event.currentTarget.open;
                    setDecisionPanelOpen(open);
                    writeStoredDecisionPanelOpen(open);
                  }}
                >
                  <summary>
                    调度决策（当前页）
                    <span className="decision-aggregate-count">{decisionAggregate.length} 个账号</span>
                  </summary>
                  <div className="decision-aggregate-body">
                    <div className="decision-aggregate-sub">
                      统计了 {displayedLogs.filter((log) => (log.analysis?.attempts?.length ?? 0) > 0).length} 条带调度记录的请求；
                      「失败」含限流/额度 429 与被丢弃后换号，不代表账号故障
                    </div>
                    <div className={`decision-aggregate-table${decisionAggregate.length > 1 ? ' multi' : ''}`}>
                      <div className="decision-aggregate-row head" aria-hidden="true">
                        <span>账号</span>
                        <span title="被尝试的次数">尝试</span>
                        <span title="真正服务了请求的次数">服务</span>
                        <span title="发出后失败或回答被丢弃的次数">失败</span>
                        <span title="根本没发出去（未授权 / 未锁定模型 / 已禁用）">未发</span>
                        <span title="服务成功时的平均耗时">均耗时</span>
                      </div>
                      {decisionAggregate.map((row) => {
                        const label = providerDisplayLabel((state.providers || []).find((item) => item.id === row.providerId), row.providerId);
                        return (
                          <div className="decision-aggregate-row" key={row.providerId}>
                            <span className="decision-aggregate-provider" title={label}>{label}</span>
                            <span className="num">{row.attempts}</span>
                            <span className={`num${row.served ? ' ok' : ''}`}>{row.served}</span>
                            <span className={`num${row.failed ? ' bad' : ''}`}>{row.failed}</span>
                            <span className={`num${row.skipped ? ' muted' : ''}`}>{row.skipped}</span>
                            {row.avgServedMs != null
                              ? <span className="num">{row.avgServedMs}ms</span>
                              : <span className="num muted">—</span>}
                          </div>
                        );
                      })}
                    </div>
                  </div>
                </details>
              ) : null}

              {logsSessionFilter ? (
                // 会话筛选一旦生效就常驻可见，否则用户在有结果时找不到退出的入口。
                <div className="log-session-bar">
                  <span>当前只看会话 <b className="mono">{logsSessionFilter.slice(0, 8)}</b> 的请求</span>
                  <button className="mini-btn" type="button" onClick={() => { setLogsSessionFilter(''); void refreshLogs(1); }}>
                    退出会话筛选
                  </button>
                </div>
              ) : null}

              {/* 日志表格 */}
              <div
                className="log-table"
                ref={(node) => {
                  // 内联 ref 每次渲染都会先置 null 再挂回来；null 不算卸载，只在真挂载
                  // 到新节点时量一次。否则每 5 秒轮询插入新行都会触发一次测量
                  // （行还在进场动画里，量出来只有几像素），页容量一改就整表重取。
                  if (!node || logsTableRef.current === node) return;
                  logsTableRef.current = node;
                  // 挂载即量一次：直接打开本页时表格挂载晚于上面 effect 的首次执行。
                  measureLogsPageSizeRef.current?.();
                }}
              >
                {logsLoading || !logsFetchedOnce ? (
                  <div className="empty-state">加载流量日志中…</div>
                ) : logs.length === 0 ? (
                  <div className="empty-state">暂无流量日志。运行路由测试或真实转发请求后会记录。</div>
                ) : displayedLogs.length === 0 ? (
                  <div className="empty-state">
                    未找到包含「{logsKeyword}」的日志条目。
                    <button className="mini-btn" type="button" onClick={() => setLogsKeyword('')} style={{ marginLeft: 8 }}>
                      清空搜索词
                    </button>
                  </div>
                ) : (
                  <>
                    <TrafficLogTable
                      logs={displayedLogs}
                      freshKeys={logsFreshKeys}
                      providers={state.providers || []}
                      routes={state.routes || []}
                      apiKeys={state.apiKeys || []}
                      onOpen={(log) => void openTrafficLogDetail(log)}
                    />
                    <div className="log-table-foot" style={{ display: 'flex', gap: 12, alignItems: 'center', justifyContent: 'space-between' }}>
                      <span>
                        共 {logsTotal} 条 · 第 {logsPage} 页 · 每页 {logsPageSize} 条
                        {logsKeyword ? ` · 当前关键词过滤匹配 ${displayedLogs.length} 条` : ''}
                        {logsSessionFilter ? ` · 会话 ${logsSessionFilter.slice(0, 8)}` : ''}
                        {logsPage > 1 ? ' · 当前页不自动刷新（避免新日志把翻页顶得乱跳），需要看最新请回到第 1 页' : ''}
                      </span>
                      <span style={{ display: 'flex', gap: 8 }}>
                        <button className="mini-btn" type="button" disabled={logsPage <= 1} onClick={() => void refreshLogs(logsPage - 1)}>上一页</button>
                        <button className="mini-btn" type="button" disabled={logsPage * logsPageSize >= logsTotal} onClick={() => void refreshLogs(logsPage + 1)}>下一页</button>
                      </span>
                    </div>
                  </>
                )}
              </div>
            </div>
          </section>
          )}

          {activeNav === 'monitoring' && navVisibleFor(permissions, 'monitoring', legacyUser) && (
          <section className="section-full">
            <MonitoringPage
              iconOf={(providerID) => {
                const provider = state.providers.find((item) => item.id === providerID);
                return provider ? providerIcon(provider) : undefined;
              }}
              onOpenAccountLogs={(providerID) => {
                setLogsProviderFilter(providerID);
                // 走 goToPage：它会写地址栏。直接 setActiveNav 的话刷新一下就回到
                // 监控中心，刚选的账号筛选也没了，链接也发不出去。
                goToPage('traffic-tokens');
              }}
            />
          </section>
          )}

          {activeNav === 'quota-keeper' && (
            <QuotaKeeperPanel providers={state.providers} showOwner={!isNormalUser || authStatus?.scope === 'all'} canManage={canDo('quota.manage')} />
          )}

          {/* 只读角色（users.view）也能看列表；拉取账号列表的初始请求只在 users.manage 时发出 */}
          {activeNav === 'users' && navVisibleFor(permissions, 'users', legacyUser) && (
            <UsersPage
              canView={canDo('users.view') || canDo('users.manage')}
              canManage={canDo('users.manage')}
              roleOptions={rolesModel.options}
              refreshRoleOptions={rolesModel.refresh}
              onToast={showToast}
              onChanged={() => { void refreshConsoleUsers(); void refreshState(false); }}
            />
          )}

          {activeNav === 'roles' && navVisibleFor(permissions, 'roles', legacyUser) && (
            <RolesPage model={rolesModel} canManage={canDo('roles.manage')} onToast={showToast} />
          )}

          {activeNav === 'machine' && navVisibleFor(permissions, 'machine', legacyUser) && (
            <MachineStatusPage onToast={showToast} />
          )}

          {activeNav === 'settings' && navVisibleFor(permissions, 'settings', legacyUser) && (
          <section className="section-grid settings-grid">
            <div className="settings-stack">
            <div className="card panel">
                <div className="panel-header">
                  <div>
                    <h2 className="panel-title">公网访问入口</h2>
                    <p className="panel-desc">局域网、管理页公网、模型 API 公网已合并到「公网访问」页，可分别开关。</p>
                  </div>
                  <button className="btn primary" type="button" onClick={() => goToPage('public-access')}>打开公网访问</button>
                </div>
                <div className="hint-text">
                    当前：局域网 {webExposed ? '开' : '关'}
                    {' · '}管理页公网 {publicDraft.exposeUi === false ? '关' : (liveUIPublicURL ? '开' : '待绑定')}
                    {' · '}API 公网 {publicDraft.exposeApi === false ? '关' : (livePublicURL && customTunnelActive ? '开' : '待绑定')}
                </div>
              </div>
              <div className="card panel">
                <div className="panel-header"><div><h2 className="panel-title">应用日志</h2><p className="panel-desc">应用级日志，支持在 UI 内切换级别：debug / info / warn / error。</p></div><button className="btn" onClick={() => void refreshAppLogs()}>刷新日志</button></div>
                <div className="detail-section">
                  <div className="detail-section-title">日志与保留</div>
                  <div className="form-grid compact single-line">
                    <SelectField label="日志级别" values={logLevelValues} value={logLevel} onChange={(value) => void updateLogLevel(value)} />
                    <label className="field">
                      <span>请求日志保留天数</span>
                      <input
                        type="number"
                        min={1}
                        max={365}
                        value={requestLogRetentionDays}
                        onChange={(e) => setRequestLogRetentionDays(Number(e.target.value) || 7)}
                        onBlur={() => void updateRequestLogRetention(requestLogRetentionDays)}
                      />
                    </label>
                  </div>
                  <div className="hint-text">仅影响请求明细（流量日志正文等）。用量统计的每日 Token / 请求次数汇总会永久保留。</div>
                  <label className="toggle-row" style={{ display: 'flex', alignItems: 'center', gap: 12, cursor: 'pointer', marginTop: 8 }}>
                    <input
                      type="checkbox"
                      checked={log2xxBodies}
                      disabled={saving}
                      onChange={(event) => void updateLog2xxBodies(event.target.checked)}
                    />
                    <span>记录成功请求正文</span>
                  </label>
                  <div className="hint-text">默认关闭：成功请求只记元数据、不落正文（省磁盘）；失败请求始终保留正文。</div>
                </div>
                <div className="detail-section">
                  <div className="detail-section-title">用量统计攒批</div>
                  <div className="form-grid compact single-line">
                    <label className="field">
                      <span>用量统计攒批条数</span>
                      <input
                        type="number"
                        min={1}
                        max={10000}
                        value={usageBatchMaxSize}
                        onChange={(e) => setUsageBatchMaxSize(Number(e.target.value) || 500)}
                        onBlur={() => void updateUsageBatch(usageBatchMaxSize, usageBatchMaxWaitSeconds)}
                      />
                    </label>
                    <label className="field">
                      <span>用量统计攒批秒数</span>
                      <input
                        type="number"
                        min={1}
                        max={3600}
                        value={usageBatchMaxWaitSeconds}
                        onChange={(e) => setUsageBatchMaxWaitSeconds(Number(e.target.value) || 60)}
                        onBlur={() => void updateUsageBatch(usageBatchMaxSize, usageBatchMaxWaitSeconds)}
                      />
                    </label>
                  </div>
                  <div className="hint-text">攒够条数或到时间就落库一次（默认 500 条 / 60 秒），值越大写盘越少。修改后需重启网关生效；正常关闭会先落盘，不丢数据。</div>
                </div>
                <div className="detail-section">
                  <div className="detail-section-title">最近日志</div>
                  <div className="app-log-list">
                    {appLogs.length === 0 ? <div className="empty-state">暂无应用日志。</div> : appLogs.map((log, index) => (
                      <div className="app-log-row" key={`${log.time}-${index}`}>
                        <span className="log-time">{new Date(log.time).toLocaleTimeString()}</span>
                        <Badge tone={log.level === 'error' ? 'red' : log.level === 'warn' ? 'amber' : log.level === 'debug' ? 'cyan' : 'blue'}>{log.level}</Badge>
                        <span className="app-log-message">{log.message}</span>
                        <span className="app-log-context">{log.context || '-'}</span>
                      </div>
                    ))}
                  </div>
                </div>
              </div>
            </div>
            <div className="settings-stack">
            <div className="card panel">
              <div className="panel-header">
                <div>
                  <h2 className="panel-title">管理密码</h2>
                  <p className="panel-desc">保护公网与局域网管理页。本机 App 可免登录；忘记密码时可在本机重置。</p>
                </div>
                <Badge tone={authStatus?.configured ? 'green' : 'amber'}>{authStatus?.configured ? '已设置' : '未设置'}</Badge>
              </div>
              <div className="public-simple-card">
                <div className="hint-text">{authStatus?.localBypass
                    ? '当前为本机访问，可直接设置或重置管理密码。'
                    : '当前为远程访问，修改密码需验证当前密码。'}</div>
                {authStatus?.configured && !authStatus.localBypass ? (
                  <label className="field">
                    <span>当前密码</span>
                    <input
                      type="password"
                      autoComplete="current-password"
                      value={adminCurrentPassword}
                      onChange={(event) => setAdminCurrentPassword(event.target.value)}
                    />
                  </label>
                ) : null}
                {authStatus?.configured && authStatus.localBypass ? (
                  <label className="field">
                    <span>当前密码（可选，本机可留空重置）</span>
                    <input
                      type="password"
                      autoComplete="current-password"
                      value={adminCurrentPassword}
                      onChange={(event) => setAdminCurrentPassword(event.target.value)}
                    />
                  </label>
                ) : null}
                <label className="field">
                  <span>{authStatus?.configured ? '新密码' : '设置密码'}</span>
                  <input
                    type="password"
                    autoComplete="new-password"
                    value={adminNewPassword}
                    onChange={(event) => setAdminNewPassword(event.target.value)}
                    placeholder="至少 8 位"
                  />
                </label>
                <label className="field">
                  <span>确认新密码</span>
                  <input
                    type="password"
                    autoComplete="new-password"
                    value={adminNewPasswordConfirm}
                    onChange={(event) => setAdminNewPasswordConfirm(event.target.value)}
                  />
                </label>
                <div className="public-simple-actions">
                  <button className="btn primary" disabled={adminPasswordBusy} onClick={() => void updateAdminPassword()}>
                    {adminPasswordBusy ? '保存中…' : authStatus?.configured ? '更新管理密码' : '设置管理密码'}
                  </button>
                  {authStatus?.requireAuth ? (
                    <button className="btn" type="button" disabled={authBusy} onClick={() => void logoutAdmin()}>退出登录</button>
                  ) : null}
                </div>
              </div>
            </div>
            <div className="card panel">
              <div className="panel-header">
                <div>
                  <h2 className="panel-title">数据目录</h2>
                  <p className="panel-desc">配置、SQLite、隧道与 Cursor token 等用户数据位置，便于备份与排查。</p>
                </div>
                {state.dataPaths?.dataDir ? <CopyButton value={state.dataPaths.dataDir} label="复制根目录" /> : null}
              </div>
              <div className="hint-text">{state.dataPaths?.note || '更新或重装 App 不会删除此目录中的数据；数据独立于 .app 包。'}</div>
              {state.dataPaths ? (
                <>
                  {state.dataPaths.dataDir ? <URLRow label="数据根目录" value={state.dataPaths.dataDir} onCopy={() => void copy(state.dataPaths!.dataDir)} /> : null}
                  {state.dataPaths.configFile ? <URLRow label="配置文件" value={state.dataPaths.configFile} onCopy={() => void copy(state.dataPaths!.configFile)} /> : null}
                  {state.dataPaths.sqliteDb ? <URLRow label="SQLite 数据库" value={state.dataPaths.sqliteDb} onCopy={() => void copy(state.dataPaths!.sqliteDb)} /> : null}
                  {state.dataPaths.cloudflareConfigDir ? <URLRow label="Cloudflare 配置" value={state.dataPaths.cloudflareConfigDir} onCopy={() => void copy(state.dataPaths!.cloudflareConfigDir!)} /> : null}
                  {state.dataPaths.cloudflaredHome ? <URLRow label="cloudflared 证书" value={state.dataPaths.cloudflaredHome} onCopy={() => void copy(state.dataPaths!.cloudflaredHome!)} /> : null}
                  {state.dataPaths.cursorTokenDir ? <URLRow label="Cursor Token 目录" value={state.dataPaths.cursorTokenDir} onCopy={() => void copy(state.dataPaths!.cursorTokenDir!)} /> : null}
                  {state.dataPaths.cursorTokenFile ? <URLRow label="Cursor Token 文件" value={state.dataPaths.cursorTokenFile} onCopy={() => void copy(state.dataPaths!.cursorTokenFile!)} /> : null}
                </>
              ) : (
                <div className="empty-state compact">路径信息暂不可用，请刷新状态。</div>
              )}
            </div>
            </div>
          </section>
          )}
        </main>
      </div>

      {showScrollTop ? (
        <button
          type="button"
          className="back-to-top"
          title="回到顶部"
          aria-label="回到顶部"
          onClick={() => {
            const reduce = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches;
            window.scrollTo({ top: 0, behavior: reduce ? 'auto' : 'smooth' });
          }}
        >
          <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="M12 19V6" />
            <path d="M6 12l6-6 6 6" />
          </svg>
        </button>
      ) : null}

      {trafficLogDetail && (() => {
        const statusInfo = explainHttpStatus(trafficLogDetail.status);
        const hasReqBody = Boolean(trafficLogDetail.requestBody?.trim());
        const hasRespBody = Boolean(trafficLogDetail.responseBody?.trim());
        const isRespSSE = hasRespBody && (trafficLogDetail.responseBody!.trim().startsWith('data:') || trafficLogDetail.responseBody!.includes('\ndata:'));
        const sseParsedText = isRespSSE ? parseSSEStreamContent(trafficLogDetail.responseBody!) : '';
        const curlCmd = buildCurlCommand(trafficLogDetail);
        const jsonMetadata = formatTrafficLogJSON(trafficLogDetail, state.providers || []);
        // 承接方：providerId 记的是真正服务的那条，和路由首选不一致就是走了备选。
        const servedProvider = (state.providers || []).find((item) => item.id === trafficLogDetail.providerId);
        const servingInfo = trafficLogServing(trafficLogDetail, state.providers || [], state.routes || [], state.apiKeys || []);
        const servedLabel = providerDisplayLabel(servedProvider, trafficLogProviderLabel(trafficLogDetail, state.providers || []));
        const servingLabel = servingInfo
          ? `${servingInfo.rank ? `备选 ${servingInfo.rank} ` : '其他 Provider '}承接（首选 ${providerDisplayLabel(servingInfo.primary, '未绑定')} 未服务）`
          : '首选直连';

        return (
        <Modal
          title={isTrafficLogError(trafficLogDetail) ? '请求异常排错诊断' : '请求链路审计详情'}
          description={isTrafficLogError(trafficLogDetail)
            ? '查看完整的错误分类原因、出入报文、全链路 APM 耗时与排错诊断。'
            : '查看全链路 APM 耗时指标、完整出入报文审计与元数据。'}
          onClose={() => setTrafficLogDetail(null)}
        >
          <div className={`test-result-card ${isTrafficLogError(trafficLogDetail) ? 'fail' : 'pass'}`}>
            <div className="test-result-head">
              <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
                <Badge tone={statusTone(trafficLogDetail.status)}>HTTP {trafficLogDetail.status}</Badge>
                <Badge tone={statusInfo.tone}>{statusInfo.category}</Badge>
                {trafficLogDetail.id ? <span className="mono" style={{ fontSize: 11, color: 'var(--muted)' }}>#{trafficLogDetail.id}</span> : null}
              </div>
              <span style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
                <span>{trafficLogKeyLabel(trafficLogDetail)} · {servedLabel} · 路由: {trafficLogDetail.routeId} · 模型: {trafficLogDetail.model}</span>
                {servingInfo ? <Badge tone="amber">{servingLabel}</Badge> : null}
                {trafficLogDetail.analysis?.sessionId ? (
                  // 一次对话的多轮请求共享同一个 sessionId：点一下就把日志列表
                  // 收窄到这一会话（后端按 json_extract 过滤）。
                  <button
                    type="button"
                    className="mini-btn"
                    title={`会话 ${trafficLogDetail.analysis.sessionId}（点击只看这一会话的请求）`}
                    onClick={() => {
                      const session = trafficLogDetail.analysis!.sessionId!;
                      setLogsSessionFilter(session);
                      setTrafficLogDetail(null);
                      showToast(`已筛选会话 ${session.slice(0, 8)}`);
                      void refreshLogs(1);
                    }}
                  >
                    会话 <span className="mono">{trafficLogDetail.analysis.sessionId.slice(0, 8)}</span>
                  </button>
                ) : null}
              </span>
            </div>

            {/* 状态解读横幅：异常/499 时完整展开（含错误摘要与排错建议）；
                正常请求压成一行次要说明，别让大绿框占掉弹窗的第一屏。 */}
            {isTrafficLogError(trafficLogDetail) || trafficLogDetail.status === 499 ? (
              <div className={`log-status-banner ${isTrafficLogError(trafficLogDetail) ? 'error' : 'warning'}`}>
                <div className="log-status-banner-title">
                  <strong>{statusInfo.title}</strong>
                  <span className="log-status-banner-desc">{statusInfo.description}</span>
                </div>
                {trafficLogDetail.errorDescription ? (
                  <div className="log-status-banner-error">
                    <strong>错误摘要：</strong>
                    <code>{trafficLogDetail.errorDescription}</code>
                  </div>
                ) : null}
                <div className="log-status-banner-hint">
                  <span style={{ opacity: 0.85 }}>排错建议：</span>{statusInfo.hint}
                </div>
              </div>
            ) : (
              <div className="log-status-line">
                <strong>{statusInfo.title}</strong>
                <span>{statusInfo.description}</span>
              </div>
            )}

            <ScheduleTracePanel log={trafficLogDetail} providers={state.providers || []} />

            {/* KPI 指标卡片 */}
            <div className="log-audit-metrics">
              <div className="log-audit-chip">
                <span>总耗时</span>
                <b>{trafficLogDetail.latencyMs} ms</b>
              </div>
              <div className="log-audit-chip">
                <span>首字 TTFT</span>
                <b>{trafficLogDetail.ttftMs != null ? `${trafficLogDetail.ttftMs} ms` : '-'}</b>
              </div>
              <div className="log-audit-chip">
                <span>上游 TTFB</span>
                <b>{trafficLogDetail.upstreamTtfbMs != null ? `${trafficLogDetail.upstreamTtfbMs} ms` : '-'}</b>
              </div>
              <div className="log-audit-chip">
                <span>网关开销</span>
                <b>{trafficLogDetail.gatewayOverheadMs != null ? `${trafficLogDetail.gatewayOverheadMs} ms` : '-'}</b>
              </div>
              <div className="log-audit-chip">
                <span>输入 Token</span>
                <b>{trafficLogDetail.inputTokens}</b>
              </div>
              <div className="log-audit-chip">
                <span>输出 Token</span>
                <b>{trafficLogDetail.outputTokens}</b>
              </div>
              <div className="log-audit-chip">
                <span>缓存读取</span>
                <b>{trafficLogDetail.cacheTokens ?? 0}{trafficLogDetail.cacheWriteTokens ? ` · 写 ${trafficLogDetail.cacheWriteTokens}` : ''}</b>
              </div>
              <div className="log-audit-chip">
                <span>成本</span>
                <b title={trafficLogDetail.priced ? '按请求结束时价格表算出，之后改价不回算' : '这条请求没有价格行（或早于成本功能）：是「未计价」，不是免费'}>
                  {trafficLogDetail.priced ? formatCostMicros(trafficLogDetail.costMicros) : '未计价'}
                </b>
              </div>
              <div className="log-audit-chip">
                <span>客户端来源</span>
                <b title={`${trafficLogDetail.clientIp || '-'}${trafficLogDetail.accessSource ? ` (${trafficLogDetail.accessSource})` : ''}`}>
                  {trafficLogDetail.clientIp || trafficLogDetail.accessSource || '-'}
                </b>
              </div>
            </div>

            {/* Modal Tabs */}
            <div className="log-modal-tabs">
              <button
                type="button"
                className={`log-modal-tab ${trafficLogDetailTab === 'apm' ? 'active' : ''}`}
                onClick={() => setTrafficLogDetailTab('apm')}
              >
                耗时分解 (APM)
              </button>
              <button
                type="button"
                className={`log-modal-tab ${trafficLogDetailTab === 'request' ? 'active' : ''}`}
                onClick={() => setTrafficLogDetailTab('request')}
              >
                请求报文 {hasReqBody ? `(${formatBytes(trafficLogDetail.requestBody!.length)})` : '(未落盘)'}
              </button>
              <button
                type="button"
                className={`log-modal-tab ${trafficLogDetailTab === 'response' ? 'active' : ''}`}
                onClick={() => setTrafficLogDetailTab('response')}
              >
                响应报文 {hasRespBody ? `(${formatBytes(trafficLogDetail.responseBody!.length)}${isRespSSE ? ' SSE' : ''})` : '(未落盘)'}
              </button>
              <button
                type="button"
                className={`log-modal-tab ${trafficLogDetailTab === 'metadata' ? 'active' : ''}`}
                onClick={() => setTrafficLogDetailTab('metadata')}
              >
                排错元数据
              </button>
              <button
                type="button"
                className={`log-modal-tab ${trafficLogDetailTab === 'curl' ? 'active' : ''}`}
                onClick={() => setTrafficLogDetailTab('curl')}
              >
                复现命令 (cURL)
              </button>
            </div>

            {trafficLogDetailLoading ? <div className="hint-text">⏳ 正在加载完整请求与响应报文…</div> : null}

            {/* Tab: APM */}
            {trafficLogDetailTab === 'apm' && (
              <div>
                <LogTimingWaterfall log={trafficLogDetail} />
                <div className="machine-facts" style={{ marginTop: 12 }}>
                  <div className="machine-fact"><span>请求协议流向</span><b>{trafficLogDetail.protocolFlow}</b></div>
                  <div className="machine-fact"><span>访问端点路径</span><b title={trafficLogDetail.path}>{trafficLogDetail.path}</b></div>
                  <div className="machine-fact"><span>命中路由 ID</span><b>{trafficLogDetail.routeId}</b></div>
                  <div className="machine-fact"><span>调用客户端 IP</span><b>{trafficLogDetail.clientIp || '—'}</b></div>
                  <div className="machine-fact"><span>上游 Provider</span><b title={trafficLogDetail.providerId || ''}>{servedLabel}</b></div>
                  <div className="machine-fact"><span>承接方式</span><b>{servingLabel}</b></div>
                  <div className="machine-fact"><span>Timing 标记</span><b>{trafficLogDetail.timingFlags || '—'}</b></div>
                </div>
              </div>
            )}

            {/* Tab: Request Body */}
            {trafficLogDetailTab === 'request' && (
              <div className="log-body-card">
                {hasReqBody ? (
                  <>
                    <div className="field-label-row">
                      <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                        <label>请求正文 (Request Body)</label>
                        <span className="badge slate" style={{ fontSize: 11 }}>{formatBytes(trafficLogDetail.requestBody!.length)}</span>
                      </div>
                      <div className="field-label-spacer">
                        <div className="view-toggle-capsule">
                          <button
                            type="button"
                            className={`capsule-btn ${!trafficLogRawReqView ? 'active' : ''}`}
                            onClick={() => setTrafficLogRawReqView(false)}
                          >
                            格式化 JSON
                          </button>
                          <button
                            type="button"
                            className={`capsule-btn ${trafficLogRawReqView ? 'active' : ''}`}
                            onClick={() => setTrafficLogRawReqView(true)}
                          >
                            原始文本
                          </button>
                        </div>
                        <CopyButton value={trafficLogDetail.requestBody!} label="复制请求体" />
                      </div>
                    </div>
                    <pre className="json-preview">{trafficLogRawReqView ? trafficLogDetail.requestBody! : formatJsonDisplay(trafficLogDetail.requestBody!)}</pre>
                  </>
                ) : trafficLogDetail.status < 400 && !log2xxBodies ? (
                  <div className="log-body-empty-notice">
                    <div className="log-body-notice-icon">
                      <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><circle cx="12" cy="12" r="10"/><line x1="12" y1="16" x2="12" y2="12"/><line x1="12" y1="8" x2="12.01" y2="8"/></svg>
                    </div>
                    <div>
                      <strong style={{ fontSize: 14 }}>当前未开启「2xx 成功请求正文落盘」</strong>
                      <p style={{ margin: '6px 0 0', color: 'var(--muted)', maxWidth: 460, fontSize: 13, lineHeight: 1.5 }}>
                        网关默认策略仅对 4xx/5xx 错误请求记录完整报文，以实现万级并发下的最高吞吐并避免日志数据库体积膨胀。开启后，正常请求也将完整保存出入报文（上限已扩容至 128 KiB）。
                      </p>
                    </div>
                    {!isNormalUser && (
                      <button
                        className="btn primary"
                        type="button"
                        disabled={saving}
                        onClick={async () => {
                          const enabledNow = await updateLog2xxBodies(true);
                          if (enabledNow) showToast('已开启记录 2xx 成功请求正文！后续的新请求将完整保存请求体与响应体。');
                        }}
                      >
                        立即开启「记录 2xx 成功正文」
                      </button>
                    )}
                  </div>
                ) : trafficLogDetail.status < 400 && log2xxBodies ? (
                  <div className="log-body-empty-notice info">
                    <div className="log-body-notice-icon">
                      <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><circle cx="12" cy="12" r="10"/><line x1="12" y1="16" x2="12" y2="12"/><line x1="12" y1="8" x2="12.01" y2="8"/></svg>
                    </div>
                    <div>
                      <strong style={{ fontSize: 14 }}>此历史请求发生时未落盘请求正文</strong>
                      <p style={{ margin: '6px 0 0', color: 'var(--muted)', maxWidth: 460, fontSize: 13, lineHeight: 1.5 }}>
                        该请求是在网关开启「2xx 正文落盘」前产生，或者客户端发起了无正文请求（如 GET / 或握手请求）。当前网关已开启 2xx 正文落盘，后续新发起的请求将完整保留报文。
                      </p>
                    </div>
                  </div>
                ) : (
                  <div className="empty-state compact">该请求未携带请求正文（或在网络握手建立前已中断）。</div>
                )}
              </div>
            )}

            {/* Tab: Response Body */}
            {trafficLogDetailTab === 'response' && (
              <div className="log-body-card">
                {hasRespBody ? (
                  <>
                    <div className="field-label-row">
                      <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                        <label>响应正文 (Response Body)</label>
                        <span className="badge slate" style={{ fontSize: 11 }}>
                          {formatBytes(trafficLogDetail.responseBody!.length)}
                          {isRespSSE ? ' · Server-Sent Events 流' : ''}
                        </span>
                      </div>
                      <div className="field-label-spacer">
                        {isRespSSE && sseParsedText ? (
                          <div className="view-toggle-capsule">
                            <button
                              type="button"
                              className={`capsule-btn ${trafficLogSSEParsedView ? 'active' : ''}`}
                              onClick={() => setTrafficLogSSEParsedView(true)}
                            >
                              解包回复文字
                            </button>
                            <button
                              type="button"
                              className={`capsule-btn ${!trafficLogSSEParsedView ? 'active' : ''}`}
                              onClick={() => setTrafficLogSSEParsedView(false)}
                            >
                              原始 SSE 流
                            </button>
                          </div>
                        ) : (
                          <div className="view-toggle-capsule">
                            <button
                              type="button"
                              className={`capsule-btn ${!trafficLogRawRespView ? 'active' : ''}`}
                              onClick={() => setTrafficLogRawRespView(false)}
                            >
                              格式化 JSON
                            </button>
                            <button
                              type="button"
                              className={`capsule-btn ${trafficLogRawRespView ? 'active' : ''}`}
                              onClick={() => setTrafficLogRawRespView(true)}
                            >
                              原始文本
                            </button>
                          </div>
                        )}
                        <CopyButton
                          value={isRespSSE && trafficLogSSEParsedView && sseParsedText ? sseParsedText : trafficLogDetail.responseBody!}
                          label={isRespSSE && trafficLogSSEParsedView && sseParsedText ? '复制解包回复' : '复制响应体'}
                        />
                      </div>
                    </div>
                    {isRespSSE && trafficLogSSEParsedView && sseParsedText ? (
                      <div className="sse-parsed-preview">
                        <div className="sse-parsed-badge">模型生成文本解包预览</div>
                        <pre className="sse-parsed-text">{sseParsedText}</pre>
                      </div>
                    ) : (
                      <pre className="json-preview">
                        {trafficLogRawRespView || isRespSSE ? trafficLogDetail.responseBody! : formatJsonDisplay(trafficLogDetail.responseBody!)}
                      </pre>
                    )}
                  </>
                ) : trafficLogDetail.status === 499 ? (
                  <div className="log-body-empty-notice warning">
                    <div className="log-body-notice-icon">
                      <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><circle cx="12" cy="12" r="10"/><line x1="15" y1="9" x2="9" y2="15"/><line x1="9" y1="9" x2="15" y2="15"/></svg>
                    </div>
                    <div>
                      <strong style={{ fontSize: 14 }}>客户端主动切断连接 (HTTP 499)</strong>
                      <p style={{ margin: '6px 0 0', color: 'var(--muted)', maxWidth: 460, fontSize: 13, lineHeight: 1.5 }}>
                        客户端（如 Cursor、网页或 IDE）提前断开了 HTTP/SSE 连接，请求在传输中被主动取消 (Context Canceled)。上游生成被中途掐断，因此未生成完整响应包体。
                      </p>
                    </div>
                  </div>
                ) : trafficLogDetail.status < 400 && !log2xxBodies ? (
                  <div className="log-body-empty-notice">
                    <div className="log-body-notice-icon">
                      <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><circle cx="12" cy="12" r="10"/><line x1="12" y1="16" x2="12" y2="12"/><line x1="12" y1="8" x2="12.01" y2="8"/></svg>
                    </div>
                    <div>
                      <strong style={{ fontSize: 14 }}>当前未开启「2xx 成功响应正文落盘」</strong>
                      <p style={{ margin: '6px 0 0', color: 'var(--muted)', maxWidth: 460, fontSize: 13, lineHeight: 1.5 }}>
                        网关默认策略仅对 4xx/5xx 错误请求记录完整报文。开启后，正常请求也将完整记录响应正文与流式 SSE 报文（上限 128 KiB）。
                      </p>
                    </div>
                    {!isNormalUser && (
                      <button
                        className="btn primary"
                        type="button"
                        disabled={saving}
                        onClick={async () => {
                          const enabledNow = await updateLog2xxBodies(true);
                          if (enabledNow) showToast('已开启记录 2xx 成功请求正文！后续的新请求将完整保存请求体与响应体。');
                        }}
                      >
                        立即开启「记录 2xx 成功正文」
                      </button>
                    )}
                  </div>
                ) : trafficLogDetail.status < 400 && log2xxBodies ? (
                  <div className="log-body-empty-notice info">
                    <div className="log-body-notice-icon">
                      <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><circle cx="12" cy="12" r="10"/><line x1="12" y1="16" x2="12" y2="12"/><line x1="12" y1="8" x2="12.01" y2="8"/></svg>
                    </div>
                    <div>
                      <strong style={{ fontSize: 14 }}>此历史请求发生时未落盘响应正文</strong>
                      <p style={{ margin: '6px 0 0', color: 'var(--muted)', maxWidth: 460, fontSize: 13, lineHeight: 1.5 }}>
                        该请求是在开启「2xx 正文落盘」前产生，或上游返回空响应。当前已开启 2xx 正文落盘，后续请求将完整保留响应内容。
                      </p>
                    </div>
                  </div>
                ) : (
                  <div className="empty-state compact">该请求未记录响应正文（上游未返回有效响应正文或网络异常中断）。</div>
                )}
              </div>
            )}

            {/* Tab: Troubleshooting Metadata */}
            {trafficLogDetailTab === 'metadata' && (
              <div className="log-body-card">
                <div className="field-label-row">
                  <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                    <label>完整链路排错元数据</label>
                    <span className="badge slate" style={{ fontSize: 11 }}>Structured Context</span>
                  </div>
                  <div className="field-label-spacer">
                    <CopyButton value={jsonMetadata} label="复制全部元数据 (JSON)" />
                    <CopyButton value={formatTrafficLogDetail(trafficLogDetail, state.providers || [])} label="复制文本简报" />
                  </div>
                </div>

                <div className="log-troubleshoot-grid">
                  {/* 分组 1: 网络与传输 */}
                  <div className="log-troubleshoot-card">
                    <h4>网络与传输链路</h4>
                    <div className="troubleshoot-row"><span>请求路径 (Path)</span><code>{trafficLogDetail.path}</code></div>
                    <div className="troubleshoot-row"><span>协议流向 (Flow)</span><b>{trafficLogDetail.protocolFlow}</b></div>
                    <div className="troubleshoot-row"><span>客户端 IP (Client IP)</span><code>{trafficLogDetail.clientIp || '—'}</code></div>
                    <div className="troubleshoot-row"><span>代理域名 (Host)</span><span>{trafficLogDetail.clientHost || '—'}</span></div>
                    <div className="troubleshoot-row"><span>网络类型 (Source)</span><Badge tone="slate">{trafficLogDetail.accessSource || 'local'}</Badge></div>
                  </div>

                  {/* 分组 2: 鉴权与身份 */}
                  <div className="log-troubleshoot-card">
                    <h4>身份与鉴权凭据</h4>
                    <div className="troubleshoot-row"><span>API 密钥名称</span><b>{trafficLogKeyLabel(trafficLogDetail)}</b></div>
                    <div className="troubleshoot-row"><span>API 密钥 ID</span><code>{trafficLogDetail.apiKeyId || '—'}</code></div>
                    <div className="troubleshoot-row"><span>所属用户 (User)</span><span>{trafficLogDetail.userName || '—'}</span></div>
                    <div className="troubleshoot-row"><span>全局日志 ID</span><code>#{trafficLogDetail.id || '—'}</code></div>
                    <div className="troubleshoot-row"><span>精准时间戳</span><span>{new Date(trafficLogDetail.time).toISOString()}</span></div>
                  </div>

                  {/* 分组 3: 路由与上游决策 */}
                  <div className="log-troubleshoot-card">
                    <h4>路由与上游 Provider</h4>
                    <div className="troubleshoot-row"><span>命中路由 ID</span><code>{trafficLogDetail.routeId}</code></div>
                    <div className="troubleshoot-row"><span>上游 Provider</span><b>{servedLabel}</b></div>
                    <div className="troubleshoot-row"><span>承接方式</span><b>{servingLabel}</b></div>
                    <div className="troubleshoot-row"><span>Provider ID</span><code>{trafficLogDetail.providerId || '—'}</code></div>
                    <div className="troubleshoot-row"><span>决策动作 (Action)</span><Badge tone="blue">{trafficLogDetail.action}</Badge></div>
                    <div className="troubleshoot-row"><span>请求模型 (Model)</span><b>{trafficLogDetail.model}</b></div>
                  </div>

                  {/* 分组 4: 细分 APM 耗时与 Token */}
                  <div className="log-troubleshoot-card">
                    <h4>APM 耗时与 Token 统计</h4>
                    <div className="troubleshoot-row"><span>端到端总延时</span><b>{trafficLogDetail.latencyMs} ms</b></div>
                    <div className="troubleshoot-row"><span>首字 TTFT 耗时</span><b>{trafficLogDetail.ttftMs ?? '-'} ms</b></div>
                    <div className="troubleshoot-row"><span>上游 TTFB 耗时</span><b>{trafficLogDetail.upstreamTtfbMs ?? '-'} ms</b></div>
                    <div className="troubleshoot-row"><span>网关开销 (Overhead)</span><b>{trafficLogDetail.gatewayOverheadMs ?? '-'} ms</b></div>
                    <div className="troubleshoot-row"><span>Token (输入/输出/缓存)</span><span>{trafficLogDetail.inputTokens} / {trafficLogDetail.outputTokens} / {trafficLogDetail.cacheTokens ?? 0}</span></div>
                    <div className="troubleshoot-row"><span>Timing 标记</span><code>{trafficLogDetail.timingFlags || 'none'}</code></div>
                  </div>
                </div>

                <details style={{ marginTop: 12 }}>
                  <summary style={{ cursor: 'pointer', fontSize: 12, color: 'var(--muted)', fontWeight: 600 }}>查看原始诊断 JSON (Raw JSON)</summary>
                  <pre className="json-preview" style={{ marginTop: 8 }}>{jsonMetadata}</pre>
                </details>
              </div>
            )}

            {/* Tab: Reproduce with cURL */}
            {trafficLogDetailTab === 'curl' && (
              <div className="log-body-card">
                <div className="field-label-row">
                  <div>
                    <label>终端一键复现 (cURL Command)</label>
                    <p style={{ margin: '4px 0 0', fontSize: 12, color: 'var(--muted)' }}>
                      在终端中粘贴执行此命令即可模拟调用端发起相同参数与 Payload 的请求。请将 YOUR_API_KEY 替换为真实密钥。
                    </p>
                  </div>
                  <CopyButton value={curlCmd} label="复制 cURL" />
                </div>
                <pre className="json-preview">{curlCmd}</pre>
              </div>
            )}

            {/* 重放不单独占一页：排错时看完报文下一步就是「再发一次」。 */}
            <div className="log-body-card">
              <ReplayPanel key={trafficLogDetail.id ?? 'noid'} log={trafficLogDetail} apiKeys={state.apiKeys || []} />
            </div>
          </div>
          <div className="actions modal-actions">
            <button className="btn" onClick={() => setTrafficLogDetail(null)}>关闭</button>
          </div>
        </Modal>
        );
      })()}


      {providerModalOpen && (
        <Modal title={editingProviderID ? '编辑输入 Provider' : '创建输入 Provider'} description={editingProviderID ? '修改后保存即生效。' : '选好连接方式与接入地址即可；订阅类保存后在下方登录一次。'} onClose={() => { setProviderModalOpen(false); setEditingProviderID(''); resetClaudeOAuthFlowState(); resetCursorOAuthFlowState(); resetChatGPTOAuthFlowState(); resetQoderPATFlowState(); resetWorkBuddyOAuthFlowState(); resetSelfRegistrationState(); }}>
          <div className="form-grid modal-form provider-form">
            <Field fullWidth label="Provider 名称" value={providerDraft.name} onChange={(value) => setProviderDraft((current) => ({ ...current, name: value }))} />
            {(() => {
              // 连接方式与它的展示文案都取自 lib 里的那一张表：目录磁贴、筛选条与
              // 这个下拉框共用同一份标签，新增一种订阅不用再改这里。
              const connectChoices = PROVIDER_CONNECT_CHOICES;
              const connectLocked = providerDraft.authType !== 'api_key' && providerDraft.authType !== 'self_register';
              const connectValue = providerDraft.authType;
              const protocolHint = '该平台必须原生兼容这里选中的上游协议（本网关只实现了这三种）：OpenAI Chat 最常见（DeepSeek、智谱、多数中转站），Responses 用于 Codex 一类上游，Claude 用于 Anthropic 及兼容端点。自定义或私有协议请改用「内网穿透自助注册」。';
              // 各连接方式的说明不再占表单行：按当前选择折成一句，挂到「连接方式」标签旁。
              const connectHints: Record<ProviderConnectKind, string> = {
                api_key: "本网关只实现了 OpenAI Chat、OpenAI Responses、Claude 三种上游协议。密钥所属平台不限，但该平台必须原生兼容上面选中的协议（例如 DeepSeek、智谱、各类中转站多为 OpenAI Chat 兼容）。若上游是自定义或私有协议，请改用「内网穿透自助注册」，由你的脚本自行适配后再接入。",
                cursor_oauth: "Cursor OAuth 上游固定为 OpenAI Chat（本地 bridge `/v1/chat/completions`）；客户端若要 Responses/Claude，请在路由输出协议里转换。",
                claude_oauth: "Claude OAuth 上游固定为 Claude 协议。",
                chatgpt_oauth: "ChatGPT OAuth 上游固定为 OpenAI Responses（chatgpt.com/backend-api/codex/responses）。",
                qoder_pat: "Qoder 的模型跑在它客户端自己的接口上：网关会把请求改写成 agent_chat_generation 并加上账号签名，对客户端仍表现为 OpenAI Chat（要 Responses/Claude 请在路由输出协议里转换）。保存后在下方登录；没有浏览器的部署（headless 服务器）可改用个人访问令牌。",
                qoder_cn: "Qoder CN 与 Qoder 同一套协议，跑在它客户端自己的接口上：网关会把请求改写成 agent_chat_generation 并加上账号签名，对客户端仍表现为 OpenAI Chat。保存后在下方登录；没有浏览器的部署（headless 服务器）可改用个人访问令牌。",
                workbuddy: "WorkBuddy 订阅（腾讯 CodeBuddy 套餐）上游原生兼容 OpenAI Chat，协议固定为 OpenAI Chat；客户端若要 Responses/Claude，请在路由输出协议里转换。保存后会自动跳转 WorkBuddy 登录页，在浏览器完成后网关自动轮询取回令牌；令牌到期自动续期，不会回传到浏览器。",
                workbuddy_ai: "WorkBuddy AI 是同一套 API 的国际版，上游原生兼容 OpenAI Chat，协议固定为 OpenAI Chat。保存后会自动跳转 WorkBuddy 登录页，网关自动轮询取回令牌并自动续期，令牌不会回传到浏览器。",
                zcode: "ZCode 不是一种模型接口，而是登录铸造密钥：用浏览器登录 zcode.z.ai 后，网关会取回（或新建）账号默认项目里的 GLM 编程套餐密钥，之后按 Claude 协议转发到该套餐的 Anthropic 端点。密钥与业务令牌都只留在网关，不会回传到浏览器。账号没有个人套餐时会自动找团队席位。",
                zcode_bigmodel: "同一套登录流程，取的是智谱 BigModel 站点的 GLM 编程套餐密钥，按 Claude 协议转发到该站点的 Anthropic 端点。密钥与业务令牌只留在网关，不会回传到浏览器。",
                trae_cn: "Trae 走 IDE 自己的协议：登录、令牌续期、模型列表与额度都由网关代为完成，客户端看到的仍是 OpenAI Chat。Trae 的授权页只接受 127.0.0.1 的回调，所以请在运行网关的这台机器上点登录；网关会临时监听一个本地端口，拿到令牌后即关闭。",
                trae_global: "与 Trae CN 同一套协议的国际部署（节点随账号走）：登录、令牌续期、模型列表与额度都由网关代为完成，客户端看到的仍是 OpenAI Chat。授权页只接受 127.0.0.1 的回调，请在运行网关的这台机器上点登录。",
                opencode_zen_free: "OpenCode Zen 的免费模型（-free 结尾、big-pickle、grok-code）无需任何密钥：网关会按 OpenCode 自己的客户端形状发出，并改走它的推理入口。免费档只接受流式请求，客户端要求一次性返回时网关会在上游把流收回来拼成一条回复。付费模型在下方填入自己的 OpenCode 密钥即可，两种模型可共用这一个 Provider。",
                self_register: "适合“用户自己内网穿透暴露服务”的场景：无需现在填写 Base URL / API Key Source / 协议，保存后会自动生成一个专属令牌 + 一段配置 Prompt，交给对方脚本使用——协议不限（chat/response/claude 任选，脚本已有哪种代码就用哪种），脚本调用接口时把实际用的 baseUrl / 协议一并声明即可。",
              };
              const connectHint = connectHints[providerDraft.authType] ?? '';
              // 自助注册类 Provider：协议只能由脚本通过 self-register 接口的
              // protocol 字段声明，控制台不提供选择/修改入口。
              // resolveProviderAuthType 已根据 selfRegistration 回填 self_register。
              const editingProviderRecord = editingProviderID ? state.providers.find((item) => item.id === editingProviderID) : undefined;
              const protocolManagedByAPI = providerDraft.authType === 'self_register' || !!editingProviderRecord?.selfRegistration;
              return (
                <>
                  {/* 先选连接方式，协议由连接方式决定：OAuth 三选一时协议自动锁定，
                      自助注册协议交给脚本自己声明，只有 API Key 能自由选协议。 */}
                  <IconSelect
                    fullWidth
                    label="连接方式"
                    hint={connectHint}
                    options={connectChoices}
                    value={connectValue}
                    onChange={(value) => setProviderDraft((current) => {
                      const picked = value as ProviderConnectKind;
                      const authType = picked ?? 'api_key';
                      const protocol = authType === 'claude_oauth' ? 'claude'
                        : authType === 'cursor_oauth' ? 'openai_chat'
                        : authType === 'chatgpt_oauth' ? 'openai_responses'
                        : isQoderConnectKind(authType) ? 'openai_chat'
                        : authType === 'workbuddy' || authType === 'workbuddy_ai' ? 'openai_chat'
                        : isZCodeConnectKind(authType) ? 'claude'
                        : isTraeConnectKind(authType) || authType === 'opencode_zen_free' ? 'openai_chat'
                        // 自助注册：协议不在控制台选，这里只是内部占位默认值
                        // （真实协议以脚本注册时声明的为准），固定用 openai_chat。
                        : authType === 'self_register' ? 'openai_chat'
                        : current.protocol;
                      return {
                        ...current,
                        authType,
                        protocol,
                        baseUrl: authType === 'self_register' ? selfRegisterPlaceholderBaseURL(protocol)
                          : isQoderConnectKind(authType) ? qoderDefaultBaseURL(authType)
                          : current.baseUrl,
                      };
                    })}
                  />
                  {protocolManagedByAPI ? (
                    <div className="field field-full">
                      <div className="field-label-row">
                        <label>协议</label>
                        <InfoTip text="由脚本通过自助注册接口的 protocol 字段声明，控制台不提供协议选择/修改；要换协议请让脚本下次注册时改传新的 protocol。" />
                      </div>
                      <div className="static-field">当前：{protocolLabel(editingProviderRecord?.protocol ?? providerDraft.protocol)}</div>
                    </div>
                  ) : (
                    <IconSelect
                      fullWidth
                      label="协议"
                      hint={protocolHint}
                      disabled={connectLocked}
                      value={protocolLabel(providerDraft.protocol)}
                      options={(connectLocked ? [protocolLabel(providerDraft.protocol)] : fixedOutputLabels).map((label) => ({value: label, label, icon: protocolIcon(label)}))}
                      onChange={(value) => setProviderDraft((current) => ({ ...current, protocol: protocolFromLabel(value) }))}
                    />
                  )}
                </>
              );
            })()}
            {isQoderConnectKind(providerDraft.authType) && (
              <Field fullWidth label="Base URL（默认该站点的推理主机，主机变更时可改）" value={providerDraft.baseUrl} onChange={(value) => setProviderDraft((current) => ({ ...current, baseUrl: value }))} />
            )}
            {isWorkBuddyConnectKind(providerDraft.authType) && (
              <Field fullWidth label="Base URL（默认 WorkBuddy 端点，端点变更时可改）" value={providerDraft.baseUrl} onChange={(value) => setProviderDraft((current) => ({ ...current, baseUrl: value }))} />
            )}
            {isZCodeConnectKind(providerDraft.authType) && (
              <Field fullWidth label="Base URL（默认套餐的 Anthropic 端点，端点变更时可改）" value={providerDraft.baseUrl} onChange={(value) => setProviderDraft((current) => ({ ...current, baseUrl: value }))} />
            )}
            {isTraeConnectKind(providerDraft.authType) && (
              <Field fullWidth label="Base URL（默认该站点的模型主机，可在登录后再改）" value={providerDraft.baseUrl} onChange={(value) => setProviderDraft((current) => ({ ...current, baseUrl: value }))} />
            )}
            {providerDraft.authType === 'api_key' && (
              <>
                <Field fullWidth label="Base URL" value={providerDraft.baseUrl} onChange={(value) => setProviderDraft((current) => ({ ...current, baseUrl: value }))} />
                <Field fullWidth label="API Key Source（可选）" hint="留空时透传客户端 Authorization；也可直接填 sk-xxx，或填 env:VAR_NAME / literal:sk-xxx。" value={providerDraft.apiKeySource} onChange={(value) => setProviderDraft((current) => ({ ...current, apiKeySource: value }))} />
                {/(?:bigmodel\.cn|z\.ai)/i.test(providerDraft.baseUrl) && (
                  <>
                    <Field label="智谱组织 ID（团队版填，个人版留空）" value={providerDraft.teamOrganizationId} onChange={(value) => setProviderDraft((current) => ({ ...current, teamOrganizationId: value }))} />
                    <Field label="智谱项目 ID（团队版填，个人版留空）" value={providerDraft.teamProjectId} onChange={(value) => setProviderDraft((current) => ({ ...current, teamProjectId: value }))} />
                  </>
                )}
              </>
            )}
            {/* 兜底模型 / 默认思考深度不在这里配置：日常按需求在「API 密钥」里
                指定即可。字段本身保留（后端仍用于模型别名兜底、{model} URL 模板
                填充与控制台各项测试），编辑时原值原样带走，不会被这个表单清空。 */}
          </div>
          <div className="claude-oauth-panel">
          {editingProviderID && providerDraft.authType === 'api_key' && /(?:bigmodel\.cn|z\.ai)/i.test(providerDraft.baseUrl) && (
            <div className="provider-account-block">
              <div className="hint-text">{`智谱编程套餐额度${providerDraft.teamOrganizationId.trim() && providerDraft.teamProjectId.trim() ? '（团队版 · 需先保存组织/项目 ID）' : '（个人版）'}`}</div>
              <ZhipuUsagePanel providerId={editingProviderID} accountKey={`provider:${editingProviderID}`} />
            </div>
          )}
          {editingProviderID && providerDraft.authType === 'api_key' && /deepseek\.com/i.test(providerDraft.baseUrl) && (
            <div className="claude-oauth-panel">
              <div className="hint-text">DeepSeek 账户余额（来自官方 /user/balance 接口）</div>
              <DeepSeekBalancePanel providerId={editingProviderID} accountKey={`provider:${editingProviderID}`} />
            </div>
          )}
          {providerDraft.protocol === 'claude' && providerDraft.authType === 'claude_oauth' && (
            <div className="claude-oauth-panel">
              {!editingProviderID ? (
                <div className="hint-text">点击下方按钮保存后会自动跳转 Claude 授权页面。</div>
              ) : (() => {
                const editingProvider = state.providers.find((item) => item.id === editingProviderID);
                const connected = editingProvider?.claudeOAuth?.connected;
                if (connected) {
                  return (
                    <>
                      <div className="hint-text">{`已连接${editingProvider?.claudeOAuth?.accountLabel ? ` · ${editingProvider.claudeOAuth.accountLabel}` : ''}${editingProvider?.claudeOAuth?.expiresAt ? ` · 过期时间：${formatClaudeUsageResetAt(editingProvider.claudeOAuth.expiresAt)}` : ''}`}</div>
                      <ClaudeOAuthUsagePanel providerId={editingProviderID} accountKey={`provider:${editingProviderID}`} connected />
                      <button className="btn danger" disabled={claudeOAuthBusy} onClick={() => void disconnectClaudeOAuth()}>{claudeOAuthBusy ? '处理中…' : '断开连接'}</button>
                    </>
                  );
                }
                return (
                  <>
                    <div className="hint-text">点「连接」后跳转 Claude 授权；网关就在你这台机器上时，浏览器回调会自动完成连接。回调进不来的（网关在服务器或容器里）用下面的地址栏粘贴收尾，两条路等价。</div>
                    <button className="btn primary" disabled={claudeOAuthBusy || claudeOAuthPolling} onClick={() => void startClaudeOAuthConnect()}>{claudeOAuthBusy || claudeOAuthPolling ? '等待授权…' : '连接 Claude 账号'}</button>
                    <div className="hint-text">{claudeOAuthCallbackHint || '授权完成后浏览器会停在回调地址（形如 http://localhost:18093/callback?code=…&state=…）：那个页面打不开也没关系，把地址栏里整条地址复制粘贴到下面即可。'}</div>
                    <div className="field-inline" style={{ marginTop: 8 }}>
                      <input placeholder="粘贴浏览器停住的整条地址（或 code#state）" value={claudeOAuthCode} onChange={(event) => setClaudeOAuthCode(event.target.value)} />
                      <button className="mini-btn" disabled={claudeOAuthBusy || !claudeOAuthCode.trim()} onClick={() => void completeClaudeOAuthConnect()}>{claudeOAuthBusy ? '连接中…' : '完成连接'}</button>
                    </div>
                    <details>
                      <summary>走 Anthropic 的手动授权页（另一条路）</summary>
                      <div className="field-inline" style={{ marginTop: 8 }}>
                        <button className="mini-btn" disabled={claudeOAuthBusy || claudeOAuthPolling} onClick={() => void startClaudeOAuthManualConnect()}>打开手动授权页</button>
                      </div>
                    </details>
                    {claudeOAuthError && <div className="hint-line error">{claudeOAuthError}</div>}
                  </>
                );
              })()}
            </div>
          )}
          {providerDraft.protocol === 'openai_chat' && providerDraft.authType === 'cursor_oauth' && (
            <div className="claude-oauth-panel">
              {!editingProviderID ? (
                <div className="hint-text">点击下方按钮保存后会自动跳转 Cursor 授权页面。需要本机安装 bun。</div>
              ) : (() => {
                const editingProvider = state.providers.find((item) => item.id === editingProviderID);
                const connected = editingProvider?.cursorOAuth?.connected;
                if (connected) {
                  return (
                    <>
                      <div className="hint-text">{`已连接${editingProvider?.cursorOAuth?.accountLabel ? ` · ${editingProvider.cursorOAuth.accountLabel}` : ''}${editingProvider?.cursorOAuth?.expiresAt ? ` · 过期时间：${formatClaudeUsageResetAt(editingProvider.cursorOAuth.expiresAt)}` : ''} · 模型 ${editingProvider?.models?.length ?? 0} 个`}</div>
                      <CursorOAuthUsagePanel providerId={editingProviderID} accountKey={`provider:${editingProviderID}`} connected />
                      <div className="actions" style={{ gap: 8 }}>
                        <button className="btn" disabled={testingProviderID === editingProviderID} onClick={() => void fetchProviderModels(editingProviderID, editingProvider?.name || '', true)}>{testingProviderID === editingProviderID ? '同步中…' : '同步模型'}</button>
                        <button className="btn danger" disabled={cursorOAuthBusy} onClick={() => void disconnectCursorOAuth()}>{cursorOAuthBusy ? '处理中…' : '断开连接'}</button>
                      </div>
                    </>
                  );
                }
                return (
                  <>
                    <div className="hint-text">点击连接后跳转 Cursor 授权页，在浏览器完成登录后网关会自动轮询并完成连接。</div>
                    <button className="btn primary" disabled={cursorOAuthBusy || cursorOAuthPolling} onClick={() => void startCursorOAuthConnect()}>{cursorOAuthBusy || cursorOAuthPolling ? '等待授权…' : '连接 Cursor 账号'}</button>
                    {cursorOAuthError && <div className="hint-line error">{cursorOAuthError}</div>}
                  </>
                );
              })()}
            </div>
          )}
          {providerDraft.protocol === 'openai_responses' && providerDraft.authType === 'chatgpt_oauth' && (
            <div className="claude-oauth-panel">
              {!editingProviderID ? (
                <div className="hint-text">点击下方按钮保存后会自动跳转 ChatGPT 授权页面。</div>
              ) : (() => {
                const editingProvider = state.providers.find((item) => item.id === editingProviderID);
                const connected = editingProvider?.chatgptOAuth?.connected;
                if (connected) {
                  return (
                    <>
                      <div className="hint-text">{`已连接${editingProvider?.chatgptOAuth?.accountLabel ? ` · ${chatgptAccountLabelFriendly(editingProvider.chatgptOAuth.accountLabel)}` : ''}${editingProvider?.chatgptOAuth?.expiresAt ? ` · 过期时间：${formatClaudeUsageResetAt(editingProvider.chatgptOAuth.expiresAt)}` : ''} · 模型 ${editingProvider?.models?.length ?? 0} 个`}</div>
                      <ChatGPTOAuthUsagePanel providerId={editingProviderID} accountKey={`provider:${editingProviderID}`} connected />
                      <div className="actions" style={{ gap: 8 }}>
                        <button className="btn" disabled={testingProviderID === editingProviderID} onClick={() => void fetchProviderModels(editingProviderID, editingProvider?.name || '', true)}>{testingProviderID === editingProviderID ? '同步中…' : '同步模型'}</button>
                        <button className="btn danger" disabled={chatgptOAuthBusy} onClick={() => void disconnectChatGPTOAuth()}>{chatgptOAuthBusy ? '处理中…' : '断开连接'}</button>
                      </div>
                    </>
                  );
                }
                return (
                  <>
                    <div className="hint-text">点「连接」后跳转 ChatGPT 授权；网关在本机时浏览器回调会自动完成，回调进不来的（网关在服务器或容器里）用下面的地址栏粘贴收尾，两条路等价。</div>
                    {chatgptOAuthCallbackBlocked === 'reserved' ? <div className="hint-line error">Windows 把 1455 端口整段保留了，这台机器监听不到浏览器回调 —— 授权完成后请把浏览器地址栏里的整条地址粘贴到下面。</div> : null}
                    {chatgptOAuthCallbackBlocked === 'busy' ? <div className="hint-line error">1455 端口被其他程序占着（常见是没退干净的 codex login）—— 授权完成后请把浏览器地址栏里的整条地址粘贴到下面。</div> : null}
                    <button className="btn primary" disabled={chatgptOAuthBusy || chatgptOAuthPolling} onClick={() => void startChatGPTOAuthConnect()}>{chatgptOAuthBusy || chatgptOAuthPolling ? '等待授权…' : '连接 ChatGPT 账号'}</button>
                    <div className="hint-text">{chatgptOAuthCallbackHint || '授权完成后浏览器会停在 http://localhost:1455/auth/callback?code=… ：那个页面打不开也没关系，把地址栏里整条地址复制粘贴到下面即可。'}</div>
                    <div className="field-inline" style={{ marginTop: 8 }}>
                      <input placeholder="粘贴浏览器停住的整条地址（或 code）" value={chatgptOAuthCode} onChange={(event) => setChatgptOAuthCode(event.target.value)} />
                      <button className="mini-btn" disabled={chatgptOAuthBusy || !chatgptOAuthCode.trim()} onClick={() => void completeChatGPTOAuthConnect()}>{chatgptOAuthBusy ? '连接中…' : '完成连接'}</button>
                    </div>
                    {chatgptOAuthError && <div className="hint-line error">{chatgptOAuthError}</div>}
                  </>
                );
              })()}
            </div>
          )}
          {providerDraft.protocol === 'openai_chat' && isWorkBuddyConnectKind(providerDraft.authType) && (
            <div className="claude-oauth-panel">
              {!editingProviderID ? (
                <div className="hint-text">保存后会自动跳转 WorkBuddy 登录页；如果浏览器拦截了新窗口，可以复制面板里的链接手动打开。</div>
              ) : (() => {
                const editingProvider = state.providers.find((item) => item.id === editingProviderID);
                const connected = editingProvider?.workbuddyOAuth?.connected;
                if (connected) {
                  return (
                    <>
                      <div className="hint-text">{`已连接${editingProvider?.workbuddyOAuth?.accountLabel ? ` · ${editingProvider.workbuddyOAuth.accountLabel}` : ''}${editingProvider?.workbuddyOAuth?.expiresAt ? ` · 令牌到期：${formatClaudeUsageResetAt(editingProvider.workbuddyOAuth.expiresAt)}（到期自动续）` : ''} · 模型 ${editingProvider?.models?.length ?? 0} 个`}</div>
                      <WorkBuddyUsagePanel providerId={editingProviderID} accountKey={`provider:${editingProviderID}`} connected />
                      <div className="actions" style={{ gap: 8 }}>
                        <button className="btn" disabled={testingProviderID === editingProviderID} onClick={() => void fetchProviderModels(editingProviderID, editingProvider?.name || '', true)}>{testingProviderID === editingProviderID ? '同步中…' : '同步模型'}</button>
                        <button className="btn" onClick={() => void startWorkBuddyOAuthConnect()}>{workbuddyOAuthBusy || workbuddyOAuthPolling ? '等待授权…' : '换账号登录'}</button>
                        <button className="btn danger" disabled={workbuddyOAuthBusy} onClick={() => void disconnectWorkBuddyOAuth()}>{workbuddyOAuthBusy ? '处理中…' : '断开连接'}</button>
                      </div>
                      {workbuddyOAuthUrl && (
                        <div className="field-inline" style={{ marginTop: 8 }}>
                          <input type="text" readOnly value={workbuddyOAuthUrl} onFocus={(event) => event.target.select()} />
                          <CopyButton value={workbuddyOAuthUrl} label="复制登录链接" toastContent="登录链接已复制" className="mini-btn" />
                        </div>
                      )}
                      {workbuddyOAuthError && <div className="hint-line error">{workbuddyOAuthError}</div>}
                    </>
                  );
                }
                return (
                  <>
                    <div className="hint-text">点击登录会跳转 WorkBuddy 登录页，在浏览器完成登录后网关会自动轮询取回令牌并连接（约 5 分钟内有效）。</div>
                    <button className="btn primary" disabled={workbuddyOAuthBusy || workbuddyOAuthPolling} onClick={() => void startWorkBuddyOAuthConnect()}>{workbuddyOAuthBusy || workbuddyOAuthPolling ? '等待授权…' : '登录 WorkBuddy 账号'}</button>
                    {workbuddyOAuthUrl && (
                      <div className="field-inline" style={{ marginTop: 8 }}>
                        <input type="text" readOnly value={workbuddyOAuthUrl} onFocus={(event) => event.target.select()} />
                        <button className="mini-btn" onClick={() => window.open(workbuddyOAuthUrl, '_blank', 'noopener,noreferrer')}>重新打开</button>
                        <CopyButton value={workbuddyOAuthUrl} label="复制链接" toastContent="登录链接已复制" className="mini-btn" />
                      </div>
                    )}
                    {workbuddyOAuthError && <div className="hint-line error">{workbuddyOAuthError}</div>}
                  </>
                );
              })()}
            </div>
          )}
          {editingProviderID && (isZCodeConnectKind(providerDraft.authType) || isTraeConnectKind(providerDraft.authType)) && (() => {
            const record = state.providers.find((item) => item.id === editingProviderID);
            const zcode = isZCodeConnectKind(providerDraft.authType) ? record?.zcodeOAuth : undefined;
            const trae = isTraeConnectKind(providerDraft.authType) ? record?.traeOAuth : undefined;
            return (
              <SubscriptionConnect
                providerId={editingProviderID}
                kind={providerDraft.authType}
                connected={zcode?.connected ?? trae?.connected}
                accountLabel={zcode?.accountLabel ?? trae?.accountLabel}
                plan={zcode?.plan}
                expiresAt={zcode?.expiresAt ?? trae?.expiresAt}
                hint={isTraeConnectKind(providerDraft.authType) ? '授权页只接受 127.0.0.1 回调：请在运行网关的这台机器上完成登录。' : undefined}
                onConnected={async () => {
                  await refreshState(false);
                  await fetchProviderModels(editingProviderID, record?.name || '', true);
                  showToast('订阅账号已连接，模型列表已同步');
                }}
                onPaused={() => void refreshState(false)}
              />
            );
          })()}

          {providerDraft.protocol === 'openai_chat' && isQoderConnectKind(providerDraft.authType) && (
            <div className="claude-oauth-panel">
              {!editingProviderID ? (
                <div className="hint-text">保存后会留在编辑态：点「登录」在浏览器里授权即可；没有浏览器的部署可展开下面的「用个人访问令牌连接」。</div>
              ) : (() => {
                const editingProvider = state.providers.find((item) => item.id === editingProviderID);
                const qoder = editingProvider?.qoderPat;
                return (
                  <>
                    <SubscriptionConnect
                      providerId={editingProviderID}
                      kind={providerDraft.authType}
                      connected={qoder?.connected}
                      accountLabel={qoder?.accountLabel}
                      plan={qoder?.signInMode === 'pat' ? '个人访问令牌' : qoder?.deviceChat ? '设备令牌直连' : undefined}
                      expiresAt={qoder?.expiresAt}
                      hint={providerDraft.authType === 'qoder_cn' ? 'Qoder CN 认 qoder.cn 的账号（阿里云 / 手机号），与 qoder.com 账号不通用。' : undefined}
                      onConnected={async () => {
                        await refreshState(false);
                        await fetchProviderModels(editingProviderID, editingProvider?.name || '', true);
                        showToast('Qoder 已登录，模型列表取自该账号自己的清单');
                      }}
                      onPaused={() => void refreshState(false)}
                    />
                    <div className="sub-models-row">
                      <span>模型 <b>{editingProvider?.models?.length ?? 0}</b> 个 · 取自该账号可用的清单</span>
                      <button className="mini-btn" disabled={testingProviderID === editingProviderID} onClick={() => void fetchProviderModels(editingProviderID, editingProvider?.name || '', true)}>{testingProviderID === editingProviderID ? '同步中…' : '重新同步'}</button>
                    </div>
                    {/* 备用门：Qoder 的个人访问令牌长期有效，headless 部署打不开
                        浏览器时用它换同一个作业令牌；两个门连的是同一个账号结构。 */}
                    {/* 令牌面板用紧凑样式（.sub-token-details）：默认 .usage-details 是给
                        用量明细页的大块留的 20px 外边距 + 12/18px 内边距，放在这里会撑出
                        一圈空白，输入框也被拉开。 */}
                    <details className="sub-token-details">
                      <summary>用个人访问令牌连接（没有浏览器时）</summary>
                      <div className="sub-token-body">
                        <p>
                          {qoder?.signInMode === 'pat' && qoder?.hasStoredToken
                            ? '当前就是令牌连接；换账号可粘贴另一个 pt- 开头的令牌覆盖。'
                            : `在 ${providerDraft.authType === 'qoder_cn' ? 'qoder.cn' : 'qoder.com'}/account/integrations 生成个人访问令牌（pt- 开头）后粘贴到这里；令牌只用来兑换短期作业令牌，不会回传到浏览器。`}
                        </p>
                        <div className="sub-token-input">
                          <input type="text" placeholder="粘贴 pt- 开头的个人访问令牌" value={qoderPatInput} onChange={(event) => setQoderPatInput(event.target.value)} />
                          <button className="mini-btn" disabled={qoderPatBusy || !qoderPatInput.trim()} onClick={() => void connectQoderPAT()}>{qoderPatBusy ? '连接中…' : '完成连接'}</button>
                        </div>
                        {qoderPatError && <div className="hint-line error">{qoderPatError}</div>}
                      </div>
                    </details>
                  </>
                );
              })()}
            </div>
          )}
          {editingProviderID && (() => {
            const editingProvider = state.providers.find((item) => item.id === editingProviderID);
            if (!editingProvider) return null;
            // 只对「内网穿透自助注册」类 Provider 显示自助注册面板：要么本次会话显式把
            // 连接方式切成了 self_register，要么该 Provider 后端已持久化 selfRegistration
            // 状态。纯 API Key Provider 不再显示「生成注册令牌」——连接方式是互斥的，
            // 生成注册令牌属于「内网穿透自助注册」这一种连接方式，不应出现在 API Key 下。
            const isSelfRegisterProvider = providerDraft.authType === 'self_register' || !!editingProvider.selfRegistration;
            if (!isSelfRegisterProvider) return null;
            const selfReg = editingProvider.selfRegistration;
            const promptText = buildSelfRegistrationPrompt(editingProvider, selfRegToken, window.location.origin);
            return (
              <div className="self-register-panel">
                <div className="hint-text">{'适合\u201c用户自己内网穿透暴露服务\u201d的场景：生成专属令牌后，对方脚本可直接调用接口更新 baseUrl / apiKeySource，协议（chat/responses/claude 三选一）也由脚本在这次调用里声明——这是唯一的协议来源，控制台不提供协议选择/修改入口；还能顺带声明自定义 authHeader，无需登录控制台账号。同一个令牌应调用协议 conformance（/self-check/conformance）直到 success=true，按 cases[].hint 修本地实现。令牌只能操作这一个 Provider，改不了别的。是否异常仍按真实请求失败判定，这里不做心跳超时告警。'}</div>
                {selfReg ? (
                  <div className="hint-text">{`已启用 · 令牌尾号 ****${selfReg.tokenPreview || '----'}${selfReg.createdAt ? ` · 生成于 ${selfReg.createdAt}` : ''}${selfReg.lastSeenAt ? ` · 上次自助注册 ${new Date(selfReg.lastSeenAt).toLocaleString()}` : ' · 尚未收到过注册请求'}`}</div>
                ) : (
                  <div className="hint-text">尚未启用自助注册。</div>
                )}
                {selfRegToken ? (
                  <div className="field field-full">
                    <label>新令牌（只显示这一次，请立即复制保存）</label>
                    <div className="field-inline">
                      <input readOnly value={selfRegToken} onFocus={(event) => event.currentTarget.select()} />
                      <CopyButton value={selfRegToken} label="复制令牌" />
                    </div>
                  </div>
                ) : null}
                <div className="actions" style={{ gap: 8, flexWrap: 'wrap' }}>
                  <button
                    className={selfReg ? 'btn' : 'btn primary'}
                    disabled={selfRegBusy}
                    onClick={() => void generateProviderSelfRegToken(editingProviderID)}
                  >
                    {selfRegBusy ? '处理中…' : selfReg ? '重置令牌' : '生成注册令牌'}
                  </button>
                  <CopyButton value={promptText} label="复制配置 Prompt" toastContent="已复制配置 Prompt，可粘贴给本地编码大模型使用" />
                  {selfReg ? (
                    <button className="btn danger" disabled={selfRegBusy} onClick={() => void revokeProviderSelfRegistration(editingProviderID)}>撤销自助注册</button>
                  ) : null}
                </div>
              </div>
            );
          })()}
                    <details className="adapter-editor" open={!!providerDraft.requestAdapterJSON.trim() || providerDraft.baseUrl.includes('{model}') || providerDraft.baseUrl.includes('deployments/')}>
            <summary>自定义适配（非标准上游）</summary>
            <div className="hint-text">{'用于 tuyadev / Azure deployment 等非标准上游。字段：urlTemplate、headers、bodyTemplate、modelMapping。占位符：{model}、{baseUrl}、{body}。运行时会把请求体里的 model 改写成映射后的上游模型名。'}</div>
            <div className="models-filter-group" style={{ margin: '10px 0' }}>
              {REQUEST_ADAPTER_PRESETS.map((preset) => (
                <button
                  key={preset.id}
                  type="button"
                  className="models-filter-chip"
                  title={preset.hint}
                  onClick={() => setProviderDraft((current) => ({ ...current, requestAdapterJSON: preset.json }))}
                >
                  填入：{preset.label}
                </button>
              ))}
              <button
                type="button"
                className="models-filter-chip"
                onClick={() => setProviderDraft((current) => ({ ...current, requestAdapterJSON: compactRequestAdapterJSON(current.requestAdapterJSON) }))}
              >
                格式化 JSON
              </button>
              {providerDraft.requestAdapterJSON.trim() ? (
                <button
                  type="button"
                  className="models-filter-chip"
                  onClick={() => setProviderDraft((current) => ({ ...current, requestAdapterJSON: '' }))}
                >
                  清空适配
                </button>
              ) : null}
            </div>
            <textarea
              className="json-preview"
              rows={12}
              placeholder={REQUEST_ADAPTER_PRESETS[0].json}
              value={providerDraft.requestAdapterJSON}
              onChange={(event) => setProviderDraft((current) => ({ ...current, requestAdapterJSON: event.target.value }))}
            />
            {(() => {
              const liveCurl = previewRequestAdapterCurl(providerDraft.baseUrl, providerDraft.defaultModel, providerDraft.requestAdapterJSON);
              const savedCurl = editingProviderID
                ? state.providers.find((item) => item.id === editingProviderID)?.requestAdapter?.curlExample
                : '';
              const curl = liveCurl || savedCurl || '';
              if (!curl) return null;
              return (
                <div className="field" style={{ marginTop: 10 }}>
                  <div className="field-label-row">
                    <label>curl 样例（按当前编辑内容实时生成）</label>
                    <CopyButton value={curl} label="复制" />
                  </div>
                  <pre className="json-preview">{curl}</pre>
                </div>
              );
            })()}
          </details>
          {editingProviderID && canDo('providers.edit') && (() => {
            const editingProvider = state.providers.find((item) => item.id === editingProviderID);
            if (!editingProvider) return null;
            return (
              <>
              {/* 三块旋钮原来各自一卡一卡地竖排：每张卡只装一行控件，却各自带
                  12px 外距 + 24px 内距，纵向被拉成近 300px。现在收进同一张卡的两行
                  网格里——第一行 RTK 开关（独占一行），第二行首包超时与并发上限并排，
                  每行左侧是标签、右侧是控件，整体高度减半。 */}
              <div className="provider-knobs">
                <label className="provider-knob-toggle">
                  <input
                    type="checkbox"
                    checked={!!editingProvider.rtkCompress}
                    disabled={saving}
                    onChange={() => void toggleProviderRTK(editingProvider)}
                  />
                  <span>RTK 工具输出压缩</span>
                  <InfoTip text="开启后网关在转发前用 rtk pipe 过滤 tool_result 里的命令输出（git status/diff、go test、grep 等），节省量会累计到用量页的「RTK 节省」卡片。需要网关主机已安装 rtk CLI（或设置 GATEWAY_RTK_PATH）；压缩失败、超时或输出没有更小时自动透传原文。" />
                </label>

                {/* 「上限 / 排队 / 每分钟」是一组并发限制，各占一格；「账号」和保存
                    按钮同排收尾。两列而不是四个一排：弹窗内容宽 630px，四个字段加
                    按钮要 574px，挤在一行必然把标签压成竖排字、按钮掉到下一行。 */}
                <div className="provider-knob-grid">
                  <label className="provider-knob-field">
                    <span>并发上限</span>
                    <span className="knob-input">
                      <input
                        ref={providerConcurrencyRef}
                        key={`concurrency-${editingProviderID}`}
                        type="number"
                        min={0}
                        max={1000}
                        step={1}
                        defaultValue={editingProvider.maxConcurrentRequests || ''}
                        placeholder="不限制"
                        disabled={saving}
                      />
                      <span className="knob-input-unit">个</span>
                    </span>
                  </label>
                  <label className="provider-knob-field">
                    <span>排队等待</span>
                    <span className="knob-input">
                      <input
                        ref={providerQueueWaitRef}
                        key={`queue-wait-${editingProviderID}`}
                        type="number"
                        min={0}
                        max={60}
                        step={1}
                        defaultValue={editingProvider.queueWaitSeconds || ''}
                        placeholder="不排队"
                        disabled={saving}
                      />
                      <span className="knob-input-unit">秒</span>
                    </span>
                  </label>
                  <label className="provider-knob-field">
                    <span>每分钟请求</span>
                    <span className="knob-input">
                      <input
                        ref={providerRPMRef}
                        key={`rpm-${editingProviderID}`}
                        type="number"
                        min={0}
                        max={100000}
                        step={1}
                        defaultValue={editingProvider.requestsPerMinute || ''}
                        placeholder="不限速"
                        disabled={saving}
                      />
                      <span className="knob-input-unit">次</span>
                    </span>
                  </label>
                  <div className="provider-knob-field">
                    <span>首包超时</span>
                    <span className="provider-knob-unit-row">
                      <span className="knob-input">
                        <input
                          ref={providerFirstPacketRef}
                          key={`first-packet-${editingProviderID}`}
                          type="number"
                          min={0}
                          max={600}
                          step={1}
                          defaultValue={editingProvider.firstPacketTimeoutSec || ''}
                          placeholder="不限制"
                          disabled={saving}
                          aria-label="首包超时（秒）"
                        />
                        <span className="knob-input-unit">秒</span>
                      </span>
                      <button className="btn btn-sm" disabled={saving} onClick={() => void saveProviderFirstPacketTimeout(editingProvider.id)}>保存</button>
                    </span>
                    <InfoTip text="网关等这台上游给出「响应头」的最长时间，超时即放弃它、继续这条密钥链路上的下一个 Provider；留空或 0 = 不限制。它只约束开流之前的等待，上游已经开播的思考或长回答不会被切断。适合挂在容易假死的上游上。" />
                  </div>
                  <label className="provider-knob-field">
                    <span>账号分组</span>
                    <span className="knob-input knob-input-wide">
                      <input
                        ref={providerLaneGroupRef}
                        key={`lane-group-${editingProviderID}`}
                        type="text"
                        maxLength={120}
                        defaultValue={editingProvider.laneGroup || ''}
                        placeholder="各自独立"
                        disabled={saving}
                      />
                    </span>
                  </label>
                  <button className="btn btn-sm provider-knob-save" disabled={saving} onClick={() => void saveProviderConcurrency(editingProvider.id)}>保存并发设置</button>
                </div>
                <div className="provider-knob-note">
                  <InfoTip text="并发上限：这台上游同时只允许这么多个请求在飞；每分钟：任意滚动一分钟内最多发这么多次请求（按准入计数）。超出的由网关先拒绝（429 + Retry-After），而不是让厂商回 429 把账号判成异常；留空或 0 = 不限制，两项可只设其一。排队秒数在任一限制生效时才有意义：0 直接拒绝，>0 先在队列里等一个空闲位。如果这条密钥链路上还有别的候选，被限住的请求会先自动换到别的账号，换不出去才回 429。账号分组：留空 = 这一行自己一份额度；填同一个名字的几行（同一个订阅的多把密钥、同一账号的不同 BaseURL）共用这一份额度，避免每行各拿一份把账号打出 429。" />
                  <span>并发与排队留空 = 不限制；账号分组填同一个名字的几行共用一份额度</span>
                </div>
              </div>

              </>
            );
          })()}
          </div>
          <div className="actions modal-actions"><button className="btn" onClick={() => { setProviderModalOpen(false); setEditingProviderID(''); resetClaudeOAuthFlowState(); resetCursorOAuthFlowState(); resetQoderPATFlowState(); resetWorkBuddyOAuthFlowState(); resetSelfRegistrationState(); }}>取消</button><button className="btn primary" disabled={saving} onClick={() => void createProvider()}>{saving ? '保存中…' : editingProviderID ? '保存修改' : providerDraft.authType === 'claude_oauth' ? '创建并连接 Claude 账号' : providerDraft.authType === 'cursor_oauth' ? '创建并连接 Cursor 账号' : providerDraft.authType === 'chatgpt_oauth' ? '创建并连接 ChatGPT 账号' : isQoderConnectKind(providerDraft.authType) ? '创建并登录 Qoder 账号' : providerDraft.authType === 'workbuddy' || providerDraft.authType === 'workbuddy_ai' ? '创建并登录 WorkBuddy 账号' : isSubscriptionConnectKind(providerDraft.authType) ? '创建并登录订阅账号' : providerDraft.authType === 'self_register' ? '创建并生成注册令牌' : '创建 Provider'}</button></div>
        </Modal>
      )}

      {chatTestOpen && chatTestContext && (
        <ChatTestDialog
          title={chatTestContext.title}
          description={chatTestContext.description}
          kind={chatTestContext.kind}
          protocol={chatTestProvider?.protocol || 'openai_chat'}
          supportsExtraTests={Boolean(chatTestProvider) && chatTestProvider?.authType !== 'chatgpt_oauth' && chatTestProvider?.protocol !== 'openai_responses'}
          models={chatTestModels}
          model={chatTestModel}
          onModelChange={(value) => { setChatTestModel(value); clearChatTestResults(); }}
          refreshingModels={refreshingChatTestModels}
          onRefreshModels={() => void refreshChatTestModels()}
          options={providerChatOptions}
          onOptionsChange={(patch) => setProviderChatOptions((current) => ({ ...current, ...patch }))}
          thinkingFields={providerThinkingPresets?.fields || []}
          message={chatTestMessage}
          onMessageChange={setChatTestMessage}
          curl={chatTestCurl}
          curlLabel={chatTestContext.curlLabel}
          endpointURL={chatTestEndpointURL}
          hintLine={chatTestContext.hintLine}
          authWarning={chatTestAuthWarning}
          running={chatTestRunning}
          onRun={(test) => void runChatTest(test)}
          onClose={() => setChatTestOpen(false)}
          results={{ main: chatTestResult, cache: cacheTestResult, thinking: thinkingTestResult }}
        />
      )}

      {providerModelsOpen && (
        <Modal size="wide" title={`获取模型 · ${providerModelsName}`} description="从 Provider 上游 /models 接口拉取可用模型列表，并更新健康状态。" onClose={() => setProviderModelsOpen(false)}>
          {providerModelsLoading ? (
            <div className="empty-state">正在从上游获取模型列表…</div>
          ) : !providerModelsResult ? (
            <div className="empty-state">等待响应…</div>
          ) : (
            <>
              <div className={`test-result-card ${providerModelsResult.success ? 'ok' : 'fail'}`}>
                <div className="test-result-head">
                  <Badge tone={providerModelsResult.success ? 'green' : statusTone(providerModelsResult.status)}>{providerModelsResult.success ? '成功' : '失败'}</Badge>
                  <span>{httpStatusLabel(providerModelsResult.status)} · {providerModelsResult.latencyMs ?? '-'}ms · {providerModelsResult.models.length} 个模型</span>
                  {(() => {
                    const provider = state.providers.find((item) => item.id === providerModelsID);
                    return provider ? <span className="test-result-provider"><ProviderLabel provider={provider} size={14} /></span> : null;
                  })()}
                </div>
                {providerModelsResult.error ? <div className="test-result-body">{providerModelsResult.error}</div> : null}
                {providerModelsResult.preview ? <div className="test-result-body">{providerModelsResult.preview}</div> : null}
                <div className="test-result-meta">modelsUrl={providerModelsResult.modelsUrl || '-'}</div>
              </div>
              {providerModelsResult.models.length > 0 ? (() => {
                const fetchedModels = providerModelsResult.models;
                const freeCount = fetchedModels.filter(isFreeModel).length;
                const ratedCount = fetchedModels.filter((model) => !isFreeModel(model) && (model.rate || 0) > 0).length;
                const longestContext = Math.max(0, ...fetchedModels.map((model) => model.contextLength || 0));
                return <>
                  <div className="fetched-model-summary">
                    <span><b>{fetchedModels.length}</b> 个模型</span>
                    {freeCount > 0 ? <span className="is-free"><b>{freeCount}</b> 免费</span> : null}
                    {ratedCount > 0 ? <span className="is-rated"><b>{ratedCount}</b> 按倍率计费</span> : null}
                    <span>最长上下文 <b>{longestContext.toLocaleString()}</b></span>
                    <span className="fetched-model-summary-hint">点击模型 ID 即可复制</span>
                  </div>
                  <div className="models-table-wrap fetched-models-wrap">
                    <table className="models-table fetched-models-table">
                      <thead>
                        <tr><th>模型</th><th>价格</th><th>上下文</th><th>最大输出</th><th style={{ textAlign: 'right' }}>操作</th></tr>
                      </thead>
                      <tbody>
                        {fetchedModels.map((model) => {
                          const hasPrice = isFreeModel(model) || (model.rate || 0) > 0;
                          return (
                            <tr key={model.id}>
                              <td className="model-id-cell">
                                <div
                                  className="model-id-wrapper"
                                  title={`点击复制模型 ID：${model.id}`}
                                  onClick={() => void copy(model.id, `已复制模型 ID：${model.id}`)}
                                >
                                  <span className="model-id-text">{model.id}</span>
                                  <span className="model-id-copy-hint" aria-hidden="true">📋</span>
                                </div>
                                {modelAlias(model) ? <span className="model-id-alias">{modelAlias(model)}</span> : null}
                                {model.contextLength || model.maxOutputTokens ? (
                                  <span className="fetched-model-inline-meta">
                                    {model.contextLength ? <span>上下文 {model.contextLength.toLocaleString()}</span> : null}
                                    {model.maxOutputTokens ? <span>输出 {model.maxOutputTokens.toLocaleString()}</span> : null}
                                  </span>
                                ) : null}
                              </td>
                              <td><div className="model-tags-row">{hasPrice ? <ModelPriceTags model={model} /> : <span className="fetched-model-empty">—</span>}</div></td>
                              <td className="model-ctx-cell">{model.contextLength ? model.contextLength.toLocaleString() : '—'}</td>
                              <td className="model-ctx-cell">{model.maxOutputTokens ? model.maxOutputTokens.toLocaleString() : '—'}</td>
                              <td style={{ textAlign: 'right' }}>
                                <CopyButton value={model.id} label="复制" toastContent={`已复制模型 ID：${model.id}`} />
                              </td>
                            </tr>
                          );
                        })}
                      </tbody>
                    </table>
                  </div>
                </>;
              })() : (
                <div className="empty-state">未获取到模型。可检查 Provider Base URL、鉴权或上游 /models 接口。</div>
              )}
            </>
          )}
          <div className="actions modal-actions">
            <button className="btn" onClick={() => setProviderModelsOpen(false)}>关闭</button>
            <button className="btn primary" disabled={providerModelsLoading || !providerModelsID} onClick={() => void fetchProviderModels(providerModelsID, providerModelsName, true)}>{providerModelsLoading ? '获取中…' : '重新获取'}</button>
          </div>
        </Modal>
      )}

      {routeModalOpen && (
        <Modal title={editingRouteID ? '编辑路由' : '创建路由'} description={editingRouteID ? '可临时切换输入 Provider 或输出协议，保存后立即生效。' : '路由只描述协议转发：自定义输入 Provider → 固定输出协议。模型与思考深度覆盖请在「API 密钥」页按密钥配置。'} onClose={() => { setRouteModalOpen(false); setEditingRouteID(''); }}>
          <div className="form-grid modal-form">
            <Field label="路由名称" value={routeDraft.name} onChange={(value) => setRouteDraft((current) => ({ ...current, name: value }))} />
            <IconSelect
              label="输入 Provider"
              value={routeDraft.providerId || state.providers[0]?.id || ''}
              options={providerSelectOptions(state.providers)}
              onChange={(value) => setRouteDraft((current) => ({ ...current, providerId: value }))}
            />
            <SelectField label="输出协议" values={fixedOutputLabels} value={protocolLabel(routeDraft.outputProtocol)} onChange={(value) => setRouteDraft((current) => ({ ...current, outputProtocol: protocolFromLabel(value) }))} />
          </div>
          <div className="actions modal-actions"><button className="btn" onClick={() => { setRouteModalOpen(false); setEditingRouteID(''); }}>取消</button><button className="btn primary" disabled={saving || state.providers.length === 0} onClick={() => void createRoute()}>{saving ? '保存中...' : editingRouteID ? '保存修改' : '创建路由'}</button></div>
        </Modal>
      )}

      {apiKeyModalOpen && (
        <Modal title="新建 API 密钥" description="选择输入 Provider 与输出协议；网关会自动创建转发规则并完成透传或协议转换。" onClose={() => setApiKeyModalOpen(false)}>
          <div className="form-stack api-key-form">
            <div className="form-row">
              <Field label="名称" value={apiKeyDraft.name} onChange={(value) => setApiKeyDraft((current) => ({ ...current, name: value }))} />
              <IconSelect
                label="输入 Provider"
                hint="这条 Key 默认转发到的上游；请求落到它时，网关按「输出协议」决定透传还是转换。"
                value={apiKeyDraft.providerId}
                options={providerSelectOptions(state.providers)}
                onChange={(value) => setApiKeyDraft((current) => ({ ...current, providerId: value, modelOverride: '' }))}
              />
              <IconSelect
                fullWidth
                label="输出协议"
                hint="客户端看到的协议。与输入 Provider 的原生协议不同时由网关双向转换（流式与非流式都覆盖）。"
                value={protocolLabel(apiKeyDraft.outputProtocol)}
                options={fixedOutputLabels.map((label) => ({ value: label, label, icon: protocolIcon(label) }))}
                onChange={(value) => setApiKeyDraft((current) => ({ ...current, outputProtocol: protocolFromLabel(value) }))}
              />
              <ApiKeyFixedModelField
                value={apiKeyDraft.modelOverride}
                models={apiKeyDraftModels}
                disabled={!apiKeyDraftProvider}
                refreshing={refreshingApiKeyModels}
                onChange={(value) => setApiKeyDraft((current) => ({ ...current, modelOverride: value }))}
                onRefresh={() => void refreshApiKeyDraftModels()}
              />
            </div>
            <div className="form-row">
              <div className="field">
                <div className="field-label-row">
                  <label>思考深度</label>
                  <InfoTip text="不覆盖时按客户端请求里的 reasoning 参数走；选定后统一覆盖思考档位。可选，通常保持「不覆盖」。" />
                </div>
                <select value={apiKeyDraft.thinkingDepthOverride} onChange={(event) => setApiKeyDraft((current) => ({ ...current, thinkingDepthOverride: event.target.value }))}>
                  {thinkingDepthSelectOptions({ value: '', label: '（不覆盖）' })}
                </select>
              </div>
              <div className="field">
                <div className="field-label-row">
                  <label>最大输出 Token</label>
                  <InfoTip text="留空表示按模型自动解析；新模型若下拉显示偏小，可在此覆盖（上限 200000）。" />
                </div>
                <input
                  type="number"
                  min={0}
                  max={200000}
                  placeholder="0 = 自动（按模型）"
                  value={apiKeyDraft.maxOutputTokens > 0 ? apiKeyDraft.maxOutputTokens : ''}
                  onChange={(event) => {
                    const raw = event.target.value.trim();
                    const n = raw === '' ? 0 : Number.parseInt(raw, 10);
                    setApiKeyDraft((current) => ({ ...current, maxOutputTokens: Number.isFinite(n) && n > 0 ? Math.min(n, 200000) : 0 }));
                  }}
                />
              </div>
            </div>
            <div className="api-key-switch-row">
              <div className="field">
                <div className="field-label-row">
                  <label>流式响应（SSE）</label>
                  <InfoTip text="允许流式响应（关闭后该 Key 的 stream:true 请求将被拒绝）" />
                </div>
                <Switch checked={apiKeyDraft.streamEnabled} label="允许 stream:true" onChange={(value) => setApiKeyDraft((current) => ({ ...current, streamEnabled: value }))} />
              </div>
              <div className="field">
                <div className="field-label-row">
                  <label>Codex Fast 档</label>
                  <InfoTip text="开启后，该 Key 的请求只要落到 ChatGPT OAuth（Codex）Provider，就强制以 fast 档转发（service_tier=priority，速度约 1.5~2.5x，额度消耗 2~2.5x），与客户端传参无关；其他类型 Provider 不受影响。默认关闭。" />
                </div>
                <Switch checked={apiKeyDraft.codexForceFast === true} label="强制 fast 档" onChange={(value) => setApiKeyDraft((current) => ({ ...current, codexForceFast: value }))} />
              </div>
            </div>
            <ApiKeyModelMappingControl
              aliases={apiKeyDraft.modelAliases}
              models={apiKeyDraftModels}
              providerName={apiKeyDraftProvider?.name || 'Provider'}
              disabled={!apiKeyDraftProvider}
              refreshing={refreshingApiKeyModels}
              onRefresh={() => void refreshApiKeyDraftModels()}
              onSave={(modelAliases) => setApiKeyDraft((current) => ({ ...current, modelAliases }))}
            />
          </div>
          <div className="actions modal-actions"><button className="btn" onClick={() => setApiKeyModalOpen(false)}>取消</button><button className="btn primary" disabled={saving || state.providers.length === 0 || !apiKeyDraft.providerId} onClick={() => void createApiKey()}>{saving ? '保存中…' : '创建 API 密钥'}</button></div>
        </Modal>
      )}


      {profileModalOpen && authStatus?.authenticated ? (
        <ProfileModal
          currentName={authStatus.username || ''}
          currentDisplayName={authStatus.displayName}
          currentAvatar={authStatus.avatarUrl}
          onClose={() => setProfileModalOpen(false)}
          onSaved={async () => {
            setProfileModalOpen(false);
            await refreshAuthStatus();
          }}
          onToast={showToast}
        />
      ) : null}

      <div className="toast" id="toast">已复制到剪贴板</div>
    </>
  );
}

createRoot(document.getElementById('root')!).render(<App />);
// beforeinstallprompt 只在满足条件时发一次，必须赶在挂载前监听。
startPwaInstallCapture();
registerPwaServiceWorker();
