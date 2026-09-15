// 应用入口：App 组件 + 启动引导。通用类型/工具/组件已拆分到 types.ts、lib.ts、components/。
import React, { useEffect, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { createRoot } from 'react-dom/client';
import './styles.css';
import { ApiKeyDetailPanel, ApiKeyFixedModelField, ApiKeyModelMappingControl } from './components/apikeys';
import { MachineMetric, Metric, UsageBarChart, UsageCacheHitRate, UsageDailyTrafficLines, UsageLineChart, UsageMonthlyTokenBars, UsageRangeCalendar, UsageStatusChart } from './components/charts';
import { API_BASE, API_KEYS_PAGE_SIZE, API_KEY_CONNECT_LABEL, BACKEND_FAIL_STREAK_LIMIT, BACKEND_POLL_MS_LOCAL, BACKEND_POLL_MS_REMOTE, LOGS_PAGE_SIZE, LOG_OWNER_FILTER_ADMIN, PROVIDER_CONNECT_FILTERS, QODER_DEFAULT_BASE_URL, REQUEST_ADAPTER_PRESETS, SELF_REGISTER_CONNECT_LABEL, USERS_TABLE_GRID, actionLabel, activePublicBaseURL, activeUIPublicBaseURL, antiAutofillProps, apiKeyReferencesProvider, buildApiKeyPatchBody, buildProviderChatCurl, buildRouteTestCurl, buildSelfRegistrationPrompt, chatgptAccountLabelFriendly, clearUICache, compactRequestAdapterJSON, composeCustomDomain, coreNavIDs, defaultProviderChatTestOptions, defaultPublicAccess, defaultSelfcheckModelForProvider, defaultThinkingValueForField, deriveUIDomainFromAPI, diskTempLabel, diskTempNote, endpointURL, fallbackState, fanSpeedLabel, fanSpeedNote, fetchWithTimeout, findRouteForBinding, fixedOutputLabels, formatBytes, formatChatTestResponse, formatCompactCount, formatDuration, formatLocalISODate, formatProviderCacheTestDetail, formatProviderThinkingTestDetail, formatRate, formatSelfcheckCaseDetail, formatTokenCount, formatTokenSummary, formatTokenSummaryCompact, formatTrafficLogDetail, getApiKeyBinding, hostTempMetric, httpStatusLabel, isFollowingTodayRange, isRemoteOrigin, isTrafficLogError, loadBootSession, localGatewayRoot, logLevelValues, mapFromTrafficRanks, maskApiKeySource, modelsForSelfcheckProvider, navGroups, navIDFromPath, navItems, navPathForID, normalizeGatewayState, normalizeRequestStats, pickCustomDomainRoot, previewRequestAdapterCurl, protocolFromLabel, protocolLabel, protocolTone, providerConnectKind, providerConnectLabel, providerOptionLabel, providerUsageLabel, publicAccessMetricValue, publicAccessStatusLabel, publicAccessURL, publicStatusTone, readConnectResponse, readSelfcheckPrefs, readStoredSidebarCollapsed, readUICache, reportIfLooksLikeAutofill, resolveProviderChatURL, resolveProviderTestModel, routeGatewayTestURL, selfRegisterPlaceholderBaseURL, splitCustomDomain, statusTone, testResultBadge, thermalPressureLabel, thinkingDepthSelectOptions, thinkingPresetsForProtocol, trafficLogKeyLabel, trafficLogKeyTitle, trafficLogProviderLabel, trafficLogSourceTitle, trafficRanksEqual, trafficRanksFromMap, uiCacheScope, userAllowedNavIDs, writeSelfcheckPrefs, writeStoredSidebarCollapsed, writeUICache } from './lib';
import { ChatGPTOAuthUsagePanel, ClaudeOAuthUsagePanel, CursorOAuthUsagePanel, DeepSeekBalancePanel, ProviderCard, ZhipuUsagePanel, formatClaudeUsageResetAt, isDocumentActive } from './components/panels';
import { MultiSelectFilter, SearchableModelSelect } from './components/selects';
import { APIKey, AdminAuthStatus, AlertPage, AlertRecord, AlertSettingsView, AppLogEntry, ChatTestContext, CloudflareZoneOption, ConsoleUser, DailyRequestPoint, GatewayState, HostMetrics, KeyProfile, LegacyRequestStatsSnapshot, LogEntry, LogPage, NavItemID, Protocol, Provider, ProviderAuthPreview, ProviderCacheTestResult, ProviderChatTestOptions, ProviderConnectKind, ProviderTestResult, ProviderThinkingTestResult, ProvidersImportResult, PublicAccessSettings, RequestAdapter, RequestStatsSnapshot, Route, RouteTestResult, SelfcheckCaseResult, SelfcheckJobStatus, SelfcheckToolInfo, ThemeMode, TrafficRankCache } from './types';
import { Badge, CopyButton, Field, Modal, NavIcon, SelectField, THEME_STORAGE_KEY, ThemeSwitch, URLRow, applyThemeMode, readStoredTheme, resolveTheme } from './components/ui';
function App() {
  const [activeNav, setActiveNav] = useState<NavItemID>(() => navIDFromPath(window.location.pathname));
  const [themeMode, setThemeMode] = useState<ThemeMode>(() => readStoredTheme());
  const [resolvedTheme, setResolvedTheme] = useState<'light' | 'dark'>(() => resolveTheme(readStoredTheme()));
  const [sidebarCollapsed, setSidebarCollapsed] = useState<boolean>(() => readStoredSidebarCollapsed());
  useEffect(() => { writeStoredSidebarCollapsed(sidebarCollapsed); }, [sidebarCollapsed]);
  const [bootSession] = useState(loadBootSession);
  const [state, setState] = useState<GatewayState>(() => bootSession.state || fallbackState);
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const [logsTotal, setLogsTotal] = useState(0);
  const [logsPage, setLogsPage] = useState(1);
  const [logsLoading, setLogsLoading] = useState(false);
  const [logsFetchedOnce, setLogsFetchedOnce] = useState(false);
  const [logsStatusFilter, setLogsStatusFilter] = useState<'all' | '2xx' | '4xx' | '5xx'>('all');
  const [logsApiKeyName, setLogsApiKeyName] = useState('');
  const [logsProviderFilter, setLogsProviderFilter] = useState('');
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
  const [selfcheckCaseDetail, setSelfcheckCaseDetail] = useState<SelfcheckCaseResult | null>(null);
  const [requestStats, setRequestStats] = useState<RequestStatsSnapshot | null>(null);
  const [monthlyDaily, setMonthlyDaily] = useState<DailyRequestPoint[]>([]);
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
  // 5s __state 轮询会不断换新 publicAccess 对象（含 tunnel 状态）。用指纹跳过
  // 「仅运行时变化」的同步，避免正在编辑的子域名前缀被已保存域名盖回去。
  const publicAccessFormSyncRef = useRef('');
  const [tunnelBusy, setTunnelBusy] = useState(false);
  const [hostMetrics, setHostMetrics] = useState<HostMetrics | null>(null);
  // null = 尚未探测，避免刷新瞬间误闪「后端未连接」
  const [backendConnected, setBackendConnected] = useState<boolean | null>(null);
  // 连续失败计数。公网走 Cloudflare 隧道时单次 TLS 握手实测约 1.5s，偶发抖动
  // 在所难免；单次失败就翻红会让 UI 频繁误报「重连中」，故需连续失败若干次
  // 才判定断线。ref 而非 state：只用于判定、不驱动渲染。
  const backendFailStreakRef = useRef(0);
  const [backendReconnecting, setBackendReconnecting] = useState(false);
  const [authStatus, setAuthStatus] = useState<AdminAuthStatus | null>(() => bootSession.auth);
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
  const [authError, setAuthError] = useState('');
  // 告警页（仅 admin）
  const [alertPage, setAlertPage] = useState<AlertPage | null>(null);
  const [alertsLoading, setAlertsLoading] = useState(false);
  const [alertStatusFilter, setAlertStatusFilter] = useState<'all' | 'unread' | 'read' | 'ignored'>('all');
  const [alertsPageNum, setAlertsPageNum] = useState(1);
  const [alertSettings, setAlertSettings] = useState<AlertSettingsView | null>(null);
  // 密码框留空 = 不修改已配置的 bot token(后端约定)。
  const [telegramTokenInput, setTelegramTokenInput] = useState('');

  // 用户管理页（仅 admin）
  const [consoleUsers, setConsoleUsers] = useState<ConsoleUser[]>([]);
  const [usersLoading, setUsersLoading] = useState(false);
  const [userModalOpen, setUserModalOpen] = useState(false);
  const [editingUserID, setEditingUserID] = useState<string | null>(null);
  const [userFormName, setUserFormName] = useState('');
  const [userFormPassword, setUserFormPassword] = useState('');
  const [userFormProviders, setUserFormProviders] = useState<string[]>([]);
  const [userFormBusy, setUserFormBusy] = useState(false);
  // 用户管理表格排序：null = 默认创建顺序；userActive/keyActive 可点击列名切换。
  const [usersSortBy, setUsersSortBy] = useState<'userActive' | 'keyActive' | null>(null);
  const [usersSortDir, setUsersSortDir] = useState<'asc' | 'desc'>('desc');
  // Provider 用户权限弹窗（仅管理员）：查看/新增/移除哪些普通用户可用某个输入 Provider。
  const [providerUsersModalID, setProviderUsersModalID] = useState<string | null>(null);
  const [providerUsersBusyID, setProviderUsersBusyID] = useState('');
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
  const [cacheTestOpen, setCacheTestOpen] = useState(false);
  const [thinkingTestResult, setThinkingTestResult] = useState<ProviderThinkingTestResult | null>(null);
  const [thinkingTestOpen, setThinkingTestOpen] = useState(false);
  const [providerModelsOpen, setProviderModelsOpen] = useState(false);
  const [providerModelsLoading, setProviderModelsLoading] = useState(false);
  const [providerModelsResult, setProviderModelsResult] = useState<ProviderTestResult | null>(null);
  const [providerModelsName, setProviderModelsName] = useState('');
  const [providerModelsID, setProviderModelsID] = useState('');
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
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
  const [selfcheckProviderIDs, setSelfcheckProviderIDs] = useState<string[]>(() => {
    const prefs = readSelfcheckPrefs(uiCacheScope(bootSession.auth));
    return Array.isArray(prefs?.providerIds) ? prefs!.providerIds!.filter(Boolean) : [];
  });
  const [selfcheckModels, setSelfcheckModels] = useState<Record<string, string>>(() => {
    const prefs = readSelfcheckPrefs(uiCacheScope(bootSession.auth));
    return prefs?.models && typeof prefs.models === 'object' ? { ...prefs.models } : {};
  });
  const [selfcheckTimeoutSec, setSelfcheckTimeoutSec] = useState(() => {
    const prefs = readSelfcheckPrefs(uiCacheScope(bootSession.auth));
    const value = Number(prefs?.timeoutSec);
    return Number.isFinite(value) && value >= 5 && value <= 600 ? value : 90;
  });
  const [selfcheckPrompt, setSelfcheckPrompt] = useState(() => {
    const prefs = readSelfcheckPrefs(uiCacheScope(bootSession.auth));
    const prompt = prefs?.prompt?.trim();
    return prompt || '1+1等于几';
  });
  const [selfcheckTools, setSelfcheckTools] = useState<SelfcheckToolInfo[]>([]);
  const [selfcheckLanRoot, setSelfcheckLanRoot] = useState('');
  const [selfcheckRunning, setSelfcheckRunning] = useState(false);
  const [selfcheckJob, setSelfcheckJob] = useState<SelfcheckJobStatus | null>(null);
  const [selfcheckRetrying, setSelfcheckRetrying] = useState<string[]>([]);
  const selfcheckPollRef = useRef<number | null>(null);
  const [providerDraft, setProviderDraft] = useState({
    name: '我的 OpenAI 对话 Provider',
    protocol: 'openai_chat' as Protocol,
    baseUrl: 'https://example.com/v1/chat/completions',
    apiKeySource: '',
    defaultModel: '',
    defaultThinkingDepth: '',
    authType: 'api_key' as 'api_key' | 'claude_oauth' | 'cursor_oauth' | 'chatgpt_oauth' | 'qoder_pat' | 'self_register',
    requestAdapterJSON: '',
    teamOrganizationId: '',
    teamProjectId: '',
  });
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
  const [cursorOAuthBusy, setCursorOAuthBusy] = useState(false);
  const [cursorOAuthError, setCursorOAuthError] = useState('');
  const [cursorOAuthFlowId, setCursorOAuthFlowId] = useState('');
  const [cursorOAuthPolling, setCursorOAuthPolling] = useState(false);
  const [chatgptOAuthCode, setChatgptOAuthCode] = useState('');
  const [chatgptOAuthBusy, setChatgptOAuthBusy] = useState(false);
  const [chatgptOAuthError, setChatgptOAuthError] = useState('');
  const [chatgptOAuthFlowId, setChatgptOAuthFlowId] = useState('');
  const [chatgptOAuthPolling, setChatgptOAuthPolling] = useState(false);
  const [qoderPatInput, setQoderPatInput] = useState('');
  const [qoderPatBusy, setQoderPatBusy] = useState(false);
  const [qoderPatError, setQoderPatError] = useState('');
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
  });
  const [modelsProviderFilter, setModelsProviderFilter] = useState('__all__');
  const [modelsSearchQuery, setModelsSearchQuery] = useState('');
  const [providersSearchQuery, setProvidersSearchQuery] = useState('');
  const [providersConnectFilter, setProvidersConnectFilter] = useState<'' | ProviderConnectKind>('');

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

  const sortedProviders = useMemo(() => {
    return [...state.providers].sort((a, b) => {
      const ca = recentProviderRequestCounts.get(a.id) || 0;
      const cb = recentProviderRequestCounts.get(b.id) || 0;
      if (ca !== cb) return cb - ca;
      return a.name.localeCompare(b.name, 'zh');
    });
  }, [state.providers, recentProviderRequestCounts]);

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

  const filteredProviders = useMemo(() => {
    return sortedProviders.filter((provider) => {
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
      ].join(' ');
      return providersSearch.matcher(haystack);
    });
  }, [sortedProviders, providersConnectFilter, providersSearch]);

  const modelsMenuProviders = useMemo(() => {
    const providerIDs = new Set(state.models.map((model) => model.providerId));
    // Always show Cursor/Claude OAuth providers that are connected, even before
    // their first successful model sync, so the filter chip is visible.
    return sortedProviders.filter((provider) => {
      if (providerIDs.has(provider.id)) return true;
      if (provider.authType === 'cursor_oauth' && provider.cursorOAuth?.connected) return true;
      if (provider.authType === 'claude_oauth' && provider.claudeOAuth?.connected) return true;
      if (provider.authType === 'chatgpt_oauth' && provider.chatgptOAuth?.connected) return true;
      if (provider.authType === 'qoder_pat' && provider.qoderPat?.connected) return true;
      return false;
    });
  }, [state.models, sortedProviders]);
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

  const filteredModels = useMemo(() => {
    const base = modelsProviderFilter === '__all__'
      ? state.models
      : state.models.filter((model) => model.providerId === modelsProviderFilter);
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
    return [...matched].sort((a, b) => {
      const ca = recentModelRequestCounts.get(`${a.providerId}::${a.id}`) || recentModelRequestCounts.get(a.id) || 0;
      const cb = recentModelRequestCounts.get(`${b.providerId}::${b.id}`) || recentModelRequestCounts.get(b.id) || 0;
      if (ca !== cb) return cb - ca;
      return a.id.localeCompare(b.id);
    });
  }, [state.models, state.providers, modelsProviderFilter, modelsSearch, recentModelRequestCounts]);
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

  function toggleApiKeySort(field: 'name' | 'createdAt' | 'owner') {
    if (apiKeySortBy === field) {
      setApiKeySortDir((current) => (current === 'asc' ? 'desc' : 'asc'));
      return;
    }
    setApiKeySortBy(field);
    setApiKeySortDir(field === 'createdAt' ? 'desc' : 'asc');
  }

  const selectedApiKey = useMemo(() => {
    const keys = state.apiKeys || [];
    if (selectedApiKeyID && keys.some((item) => item.id === selectedApiKeyID)) {
      return keys.find((item) => item.id === selectedApiKeyID);
    }
    return filteredApiKeys[0];
  }, [state.apiKeys, selectedApiKeyID, filteredApiKeys]);

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
        if (!isDocumentActive()) return;
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
          void refreshRequestStats();
        }
      })();
    };
    // 公网经隧道访问时单次往返约 1.5s，5s 轮询会让多路请求互相叠加；放慢到 15s。
    const timer = window.setInterval(pollOnce, isRemoteOrigin() ? BACKEND_POLL_MS_REMOTE : BACKEND_POLL_MS_LOCAL);
    // 重新回到前台（Chrome 变为最前台 / 标签页重新可见）时立即补一次刷新，
    // 让底部“数据更新于”不必等到下一个 5s 周期才追上。
    const onFocusResume = () => { if (isDocumentActive()) pollOnce(); };
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

  useEffect(() => () => {
    if (cloudflarePollRef.current != null) {
      window.clearInterval(cloudflarePollRef.current);
    }
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

  // 机器状态：仅该页可见时每 5s 拉一次；离开/后台则停，服务端无请求会自停采样。
  useEffect(() => {
    if (activeNav !== 'machine') {
      setHostMetrics(null);
      return;
    }
    let cancelled = false;
    let timer: number | null = null;
    const tick = () => {
      if (cancelled) return;
      if (!isDocumentActive()) return;
      void (async () => {
        try {
          const response = await fetchWithTimeout(`${API_BASE}/__host-metrics`, { credentials: 'include' });
          if (!response.ok || cancelled) return;
          const data = await response.json() as HostMetrics;
          if (!cancelled) setHostMetrics(data);
        } catch {
          // ignore transient errors; next tick retries
        }
      })();
    };
    tick();
    timer = window.setInterval(tick, isRemoteOrigin() ? BACKEND_POLL_MS_REMOTE : BACKEND_POLL_MS_LOCAL);
    const onResume = () => { if (isDocumentActive()) tick(); };
    window.addEventListener('focus', onResume);
    document.addEventListener('visibilitychange', onResume);
    return () => {
      cancelled = true;
      if (timer != null) window.clearInterval(timer);
      window.removeEventListener('focus', onResume);
      document.removeEventListener('visibilitychange', onResume);
    };
  }, [activeNav]);

  useEffect(() => {
    if (activeNav !== 'usage-stats') return;
    const today = formatLocalISODate(new Date());
    if (usageFollowTodayRef.current) {
      if (usageRangeRef.current.from !== today || usageRangeRef.current.to !== today) {
        setUsageFrom(today);
        setUsageTo(today);
        usageRangeRef.current = { from: today, to: today };
      }
      void refreshRequestStats(today, today);
    } else {
      void refreshRequestStats(usageRangeRef.current.from, usageRangeRef.current.to);
    }
    void refreshMonthlyDaily();
  }, [activeNav]);

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
    const kind = `logs:p1:s${logsStatusFilter}:f${effectiveFrom}:t${effectiveTo}:k${logsApiKeyName.trim()}:pv${logsProviderFilter}:u${logsOwnerFilter}`;
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
  }, [activeNav, logsStatusFilter, logsFrom, logsTo, logsApiKeyName, logsProviderFilter, logsOwnerFilter]);

  // 仅第 1 页自动轮询；翻到第 2 页及以后立刻停表，绝不自动刷新。
  useEffect(() => {
    if (activeNav !== 'traffic-tokens') return;
    if (logsPage !== 1) return;
    const timer = window.setInterval(() => {
      if (!isDocumentActive()) return;
      if (logsPageRef.current !== 1) return;
      void refreshLogs(1, undefined, undefined, { silent: true });
    }, isRemoteOrigin() ? BACKEND_POLL_MS_REMOTE : BACKEND_POLL_MS_LOCAL);
    return () => window.clearInterval(timer);
  }, [activeNav, logsPage, logsStatusFilter, logsFrom, logsTo, logsApiKeyName, logsProviderFilter, logsOwnerFilter]);

  // 进入告警页：拉一次列表 + 配置。筛选切换时回到第 1 页重新拉。
  useEffect(() => {
    if (activeNav !== 'alerts') return;
    void refreshAlerts(1, alertStatusFilter);
    void refreshAlertSettings();
  }, [activeNav, alertStatusFilter]);

  // 管理员打开 API 日志页时加载用户列表，用于「用户」筛选下拉；普通用户看不到这个
  // 筛选项（后端也只对 admin 生效），不需要拉取。
  useEffect(() => {
    if (activeNav !== 'traffic-tokens') return;
    if (authStatus?.role === 'user') return;
    void refreshConsoleUsers();
  }, [activeNav, authStatus?.role]);

  // 输入 Provider 页要给每个 Provider 标注创建者，同样需要用户列表解析用户名。
  useEffect(() => {
    if (activeNav !== 'input-providers') return;
    if (authStatus?.role === 'user') return;
    void refreshConsoleUsers();
  }, [activeNav, authStatus?.role]);

  useEffect(() => {
    if (activeNav !== 'self-check') return;
    void refreshSelfcheckTools();
  }, [activeNav]);

  useEffect(() => {
    // 自检勾选 Provider / 模型 / 超时 / Prompt 永久写入本机，刷新后仍保留。
    writeSelfcheckPrefs(uiCacheScope(authStatus), {
      providerIds: selfcheckProviderIDs,
      models: selfcheckModels,
      timeoutSec: selfcheckTimeoutSec,
      prompt: selfcheckPrompt,
    });
  }, [authStatus, selfcheckProviderIDs, selfcheckModels, selfcheckTimeoutSec, selfcheckPrompt]);

  useEffect(() => {
    // Provider 被删除时，同步清掉自检勾选与模型记忆里的脏 ID。
    const known = new Set(sortedProviders.map((provider) => provider.id));
    setSelfcheckProviderIDs((current) => {
      const next = current.filter((id) => known.has(id));
      return next.length === current.length ? current : next;
    });
    setSelfcheckModels((current) => {
      let changed = false;
      const next: Record<string, string> = {};
      for (const [providerID, model] of Object.entries(current)) {
        if (!known.has(providerID)) {
          changed = true;
          continue;
        }
        next[providerID] = model;
      }
      return changed ? next : current;
    });
  }, [sortedProviders]);

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

  // 普通用户访问未授权页面时强制跳回 API 密钥页
  useEffect(() => {
    if (!authStatus?.authenticated || authStatus.role !== 'user') return;
    if (userAllowedNavIDs.includes(activeNav)) return;
    window.history.replaceState({}, '', navPathForID('api-keys'));
    setActiveNav('api-keys');
  }, [authStatus, activeNav]);

  useEffect(() => () => {
    if (selfcheckPollRef.current != null) {
      window.clearInterval(selfcheckPollRef.current);
      selfcheckPollRef.current = null;
    }
  }, []);

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
          : { username: authUsername.trim(), password: authPassword }),
      });
      if (!response.ok) {
        const text = await response.text();
        throw new Error(text || `HTTP ${response.status}`);
      }
      const data = await response.json() as AdminAuthStatus;
      setAuthStatus(data);
      writeUICache('session', 'auth', data);
      setAuthPassword('');
      setAuthPasswordConfirm('');
      const isNormalUserLogin = data.role === 'user';
      if (window.location.pathname === '/login') {
        const landing = isNormalUserLogin ? 'api-keys' : 'input-providers';
        window.history.replaceState({}, '', navPathForID(landing));
        setActiveNav(landing);
      } else if (isNormalUserLogin && !userAllowedNavIDs.includes(navIDFromPath(window.location.pathname))) {
        window.history.replaceState({}, '', navPathForID('api-keys'));
        setActiveNav('api-keys');
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
      if (!response.ok) throw new Error(await response.text());
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

  async function refreshAlerts(page = alertsPageNum, status = alertStatusFilter) {
    setAlertsLoading(true);
    try {
      const params = new URLSearchParams({ page: String(page) });
      if (status !== 'all') params.set('status', status);
      const response = await fetch(`${API_BASE}/__alerts?${params.toString()}`, { credentials: 'same-origin' });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      setAlertPage(await response.json() as AlertPage);
      setAlertsPageNum(page);
    } catch {
      // 非管理员或后端异常时静默
    } finally {
      setAlertsLoading(false);
    }
  }

  async function refreshAlertSettings() {
    try {
      const response = await fetch(`${API_BASE}/__alerts/settings`, { credentials: 'same-origin' });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      setAlertSettings(await response.json() as AlertSettingsView);
    } catch {
      // 非管理员或后端异常时静默
    }
  }

  /** 保存告警配置。token 传空串表示保持后端已存的值不变。 */
  async function saveAlertSettings(patch: Partial<AlertSettingsView> & { botToken?: string }) {
    if (!alertSettings) return;
    setSaving(true);
    try {
      const merged = { ...alertSettings, ...patch };
      const response = await fetch(`${API_BASE}/__alerts/settings`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'same-origin',
        body: JSON.stringify({
          multiIpEnabled: merged.multiIpEnabled,
          multiIpWindowMinutes: merged.multiIpWindowMinutes,
          multiIpThreshold: merged.multiIpThreshold,
          concurrentIpEnabled: merged.concurrentIpEnabled,
          concurrentIpWindowMinutes: merged.concurrentIpWindowMinutes,
          concurrentIpThreshold: merged.concurrentIpThreshold,
          cooldownMinutes: merged.cooldownMinutes,
          telegram: {
            enabled: merged.telegram.enabled,
            chatId: merged.telegram.chatId,
            botToken: patch.botToken ?? '',
          },
        }),
      });
      if (!response.ok) throw new Error(await response.text());
      setAlertSettings(await response.json() as AlertSettingsView);
      setTelegramTokenInput('');
      showToast('告警配置已保存');
    } catch (error) {
      showToast(`保存告警配置失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  async function updateAlertStatus(id: number, status: 'unread' | 'read' | 'ignored') {
    try {
      const response = await fetch(`${API_BASE}/__alerts/${id}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'same-origin',
        body: JSON.stringify({ status }),
      });
      if (!response.ok) throw new Error(await response.text());
      await refreshAlerts();
    } catch (error) {
      showToast(`更新告警状态失败：${String(error)}`);
    }
  }

  async function retryAlertPush(id: number) {
    try {
      const response = await fetch(`${API_BASE}/__alerts/${id}/push`, {
        method: 'POST',
        credentials: 'same-origin',
      });
      if (!response.ok) throw new Error(await response.text());
      const updated = await response.json() as AlertRecord;
      showToast(updated.pushStatus === 'sent' ? '已重新推送到 Telegram' : `重推未成功：${updated.pushError || '未知原因'}`);
      await refreshAlerts();
    } catch (error) {
      showToast(`重推失败：${String(error)}`);
    }
  }

  async function sendTelegramTest() {
    setSaving(true);
    try {
      const response = await fetch(`${API_BASE}/__alerts/telegram/test`, {
        method: 'POST',
        credentials: 'same-origin',
      });
      if (!response.ok) throw new Error(await response.text());
      showToast('测试消息已发送，请查看 Telegram');
    } catch (error) {
      showToast(`测试消息发送失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  async function refreshConsoleUsers() {
    setUsersLoading(true);
    try {
      const response = await fetch(`${API_BASE}/__users`, { credentials: 'same-origin' });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      setConsoleUsers(await response.json() as ConsoleUser[]);
    } catch {
      // 非管理员或后端异常时静默
    } finally {
      setUsersLoading(false);
    }
  }

  function toggleUsersSort(field: 'userActive' | 'keyActive') {
    if (usersSortBy === field) {
      setUsersSortDir((current) => (current === 'asc' ? 'desc' : 'asc'));
      return;
    }
    setUsersSortBy(field);
    setUsersSortDir('desc'); // 默认最近的在前
  }

  // 授予/移除某个普通用户对指定输入 Provider 的使用权限（管理员专用，
  // 复用 PATCH /__users/{id} 的 allowedProviderIds 字段）。
  async function updateUserProviderPermission(user: ConsoleUser, providerId: string, grant: boolean) {
    setProviderUsersBusyID(user.id);
    try {
      const current = user.allowedProviderIds || [];
      const next = grant
        ? Array.from(new Set([...current, providerId]))
        : current.filter((id) => id !== providerId);
      const response = await fetch(`${API_BASE}/__users/${encodeURIComponent(user.id)}`, {
        method: 'PATCH',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ allowedProviderIds: next }),
      });
      if (!response.ok) throw new Error(await response.text());
      await refreshConsoleUsers();
      showToast(grant ? `已为 ${user.username} 开通权限` : `已移除 ${user.username} 的权限`);
    } catch (error) {
      showToast(`更新用户权限失败：${String(error)}`);
    } finally {
      setProviderUsersBusyID('');
    }
  }

  function openUserModal(user?: ConsoleUser) {
    setEditingUserID(user?.id ?? null);
    setUserFormName(user?.username ?? '');
    setUserFormPassword('');
    setUserFormProviders(user?.allowedProviderIds ?? []);
    setUserModalOpen(true);
  }

  async function submitUserForm() {
    setUserFormBusy(true);
    try {
      const username = userFormName.trim();
      if (!username) {
        showToast('用户名不能为空');
        return;
      }
      if (!editingUserID && userFormPassword.trim().length < 8) {
        showToast('初始密码至少 8 位');
        return;
      }
      const url = editingUserID ? `${API_BASE}/__users/${encodeURIComponent(editingUserID)}` : `${API_BASE}/__users`;
      const response = await fetch(url, {
        method: editingUserID ? 'PATCH' : 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(editingUserID
          ? { username, allowedProviderIds: userFormProviders }
          : { username, password: userFormPassword, allowedProviderIds: userFormProviders }),
      });
      if (!response.ok) throw new Error(await response.text());
      setUserModalOpen(false);
      await refreshConsoleUsers();
      showToast(editingUserID ? '用户已更新' : '用户已创建');
    } catch (error) {
      showToast(`保存用户失败：${String(error)}`);
    } finally {
      setUserFormBusy(false);
    }
  }

  async function toggleUserEnabled(user: ConsoleUser) {
    try {
      const response = await fetch(`${API_BASE}/__users/${encodeURIComponent(user.id)}`, {
        method: 'PATCH',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ enabled: !user.enabled }),
      });
      if (!response.ok) throw new Error(await response.text());
      await refreshConsoleUsers();
      showToast(user.enabled ? '用户已禁用' : '用户已启用');
    } catch (error) {
      showToast(`操作失败：${String(error)}`);
    }
  }

  async function deleteConsoleUser(user: ConsoleUser) {
    if (!window.confirm(`确定删除用户「${user.username}」？其名下 Key 将归还管理员。`)) return;
    try {
      const response = await fetch(`${API_BASE}/__users/${encodeURIComponent(user.id)}`, {
        method: 'DELETE',
        credentials: 'same-origin',
      });
      if (!response.ok) throw new Error(await response.text());
      await Promise.all([refreshConsoleUsers(), refreshState(false)]);
      showToast('用户已删除');
    } catch (error) {
      showToast(`删除失败：${String(error)}`);
    }
  }

  // 重置用户密码走应用内弹窗：window.prompt 在部分嵌入式浏览器会被静默拦截，按钮形同虚设
  const [passwordResetUser, setPasswordResetUser] = useState<ConsoleUser | null>(null);
  const [passwordResetValue, setPasswordResetValue] = useState('');
  const [passwordResetBusy, setPasswordResetBusy] = useState(false);

  async function confirmPasswordReset() {
    if (!passwordResetUser) return;
    if (passwordResetValue.trim().length < 8) {
      showToast('密码至少 8 位');
      return;
    }
    setPasswordResetBusy(true);
    try {
      const response = await fetch(`${API_BASE}/__users/${encodeURIComponent(passwordResetUser.id)}/reset-password`, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password: passwordResetValue.trim() }),
      });
      if (!response.ok) throw new Error(await response.text());
      showToast('密码已重置');
      setPasswordResetUser(null);
      setPasswordResetValue('');
    } catch (error) {
      showToast(`重置失败：${String(error)}`);
    } finally {
      setPasswordResetBusy(false);
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
      setSelectedRouteID((current) => current || data.routes?.[0]?.id || '');
      setSelectedProviderID((current) => current || data.providers?.[0]?.id || '');
      setSelectedOutputProtocol((current) => data.routes?.[0]?.outputProtocol || current);
      if (data.requestLogRetentionDays && data.requestLogRetentionDays > 0) {
        setRequestLogRetentionDays(data.requestLogRetentionDays);
      }
      setLog2xxBodies(data.log2xxBodies === true);
      if (data.usageBatchMaxSize && data.usageBatchMaxSize > 0) setUsageBatchMaxSize(data.usageBatchMaxSize);
      if (data.usageBatchMaxWaitSeconds && data.usageBatchMaxWaitSeconds > 0) setUsageBatchMaxWaitSeconds(data.usageBatchMaxWaitSeconds);
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
    params.set('pageSize', String(LOGS_PAGE_SIZE));
    params.set('status', logsStatusFilter);
    if (includeBodies) params.set('includeBodies', '1');
    // 日历选完立即刷新时 state 尚未生效，需显式传入新区间
    const from = fromOverride !== undefined ? fromOverride : logsFrom;
    const to = toOverride !== undefined ? toOverride : logsTo;
    if (from) params.set('from', from);
    if (to) params.set('to', to);
    const keyName = apiKeyNameOverride !== undefined ? apiKeyNameOverride.trim() : logsApiKeyName.trim();
    if (keyName) params.set('apiKeyName', keyName);
    if (logsProviderFilter) params.set('providerId', logsProviderFilter);
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
      setLogsFetchedOnce(true);
      const fetchedAt = new Date();
      setDataFetchedAt(fetchedAt);
      const from = fromOverride !== undefined ? fromOverride : logsFrom;
      const to = toOverride !== undefined ? toOverride : logsTo;
      const keyForCache = (opts?.apiKeyName !== undefined ? opts.apiKeyName : logsApiKeyName).trim();
      writeUICache(uiCacheScope(authStatusRef.current), `logs:p${nextPage}:s${logsStatusFilter}:f${from}:t${to}:k${keyForCache}:pv${logsProviderFilter}:u${logsOwnerFilter}`, {
        items,
        total,
        page: nextPage,
        fetchedAt: fetchedAt.toISOString(),
      });
    } catch {
      // Keep UI usable when backend is down.
      if (fetchGen === logsFetchGenRef.current) setLogsFetchedOnce(true);
    } finally {
      if (!opts?.silent && fetchGen === logsFetchGenRef.current) setLogsLoading(false);
    }
  }

  async function openTrafficLogDetail(log: LogEntry) {
    setTrafficLogDetail(log);
    setTrafficLogDetailLoading(true);
    try {
      const response = await fetch(`${API_BASE}/__logs?${buildLogsQueryParams(logsPage, true).toString()}`, { credentials: 'same-origin' });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const data = await response.json() as LogPage | LogEntry[];
      const items = Array.isArray(data) ? data : data.items || [];
      const matchKey = trafficLogMatchKey(log);
      const full = items.find((item) => trafficLogMatchKey(item) === matchKey);
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

  async function updateLog2xxBodies(enabled: boolean) {
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
    } catch (error) {
      showToast(`更新失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  async function updateUsageBatch(maxSize: number, maxWaitSeconds: number) {
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
      showToast(`用量统计攒批已设为 ${data.usageBatchMaxSize} 条 / ${data.usageBatchMaxWaitSeconds} 秒（重启网关后生效）`);
    } catch (error) {
      showToast(`更新失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  function accessSourceLabel(source?: string) {
    switch (source) {
      case 'lan': return '局域网';
      case 'public': return '公网';
      case 'local': return '本机';
      default: return source || '—';
    }
  }

  async function refreshRequestStats(from = usageRangeRef.current.from, to = usageRangeRef.current.to) {
    try {
      // 容错：开始时间晚于结束时间时自动交换，避免后端返回空区间
      if (from && to && from > to) [from, to] = [to, from];
      const params = new URLSearchParams();
      if (from) params.set('from', from);
      if (to) params.set('to', to);
      const response = await fetch(`${API_BASE}/__request-stats?${params.toString()}`);
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const snapshot = normalizeRequestStats(await response.json() as RequestStatsSnapshot | LegacyRequestStatsSnapshot);
      setRequestStats(snapshot);
      setDataFetchedAt(new Date());
      if (snapshot) {
        writeUICache(uiCacheScope(authStatusRef.current), `stats:${from}:${to}`, snapshot);
      }
    } catch {
      // Keep UI usable when backend is down.
    }
  }

  /** 独立拉取最近 7 天的按日数据，供日历旁的组合图使用（不受所选区间影响）。 */
  async function refreshMonthlyDaily() {
    try {
      const end = new Date();
      const start = new Date();
      start.setDate(end.getDate() - 6);
      const params = new URLSearchParams();
      params.set('from', formatLocalISODate(start));
      params.set('to', formatLocalISODate(end));
      const response = await fetch(`${API_BASE}/__request-stats?${params.toString()}`);
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const data = normalizeRequestStats(await response.json() as RequestStatsSnapshot | LegacyRequestStatsSnapshot);
      setMonthlyDaily(data?.daily || []);
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
    if (cloudflarePollRef.current != null) {
      window.clearInterval(cloudflarePollRef.current);
      cloudflarePollRef.current = null;
    }
  }

  async function waitForCloudflareAuthorization(timeoutMs = 10 * 60 * 1000) {
    const started = Date.now();
    return new Promise<boolean>((resolve) => {
      stopCloudflareLoginPoll();
      const check = async () => {
        const authorized = await refreshCloudflareAuthStatus();
        if (authorized) {
          stopCloudflareLoginPoll();
          setCloudflareAuthPending(false);
          resolve(true);
          return;
        }
        if (Date.now() - started > timeoutMs) {
          stopCloudflareLoginPoll();
          setCloudflareAuthPending(false);
          resolve(false);
        }
      };
      void check();
      cloudflarePollRef.current = window.setInterval(() => {
        void check();
      }, 2000);
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
    if (contentType.includes('json')) {
      return await response.json() as RouteTestResult;
    }
    const text = (await response.text()).slice(0, 2000);
    return {
      success: false,
      status: response.status,
      error: `HTTP ${response.status} · ${contentType || '非 JSON 响应'}：${text}`,
    };
  }

  async function runChatTest() {
    if (!chatTestContext) {
      showToast('请先选择测试对象');
      return;
    }
    const target = chatTestContext;
    const targetKey = `${target.kind}:${target.id}`;
    let alreadyRunning = false;
    setChatTestingKeys((keys) => {
      if (keys.includes(targetKey)) {
        alreadyRunning = true;
        return keys;
      }
      return [...keys, targetKey];
    });
    if (alreadyRunning) {
      showToast('该对象正在测试中');
      return;
    }
    const stillCurrent = () => {
      const current = chatTestContextRef.current;
      return !!current && current.kind === target.kind && current.id === target.id;
    };
    if (stillCurrent()) {
      setChatTestResult(null);
      setCacheTestResult(null);
      setThinkingTestResult(null);
      setCacheTestOpen(false);
      setThinkingTestOpen(false);
    }
    try {
      if (target.kind === 'route') {
        const response = await fetch(`${API_BASE}/__routes/${encodeURIComponent(target.id)}/test`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ model: chatTestModel.trim(), message: chatTestMessage.trim() }),
        });
        const result = await parseRouteTestResponse(response);
        if (stillCurrent()) setChatTestResult(result);
        showToast(result.success ? `对话测试成功：HTTP ${result.status}` : `对话测试未通过：${result.status || result.error || 'unknown'}`);
      } else {
        const providerPath = `${API_BASE}/__providers/${encodeURIComponent(target.id)}`;
        const provider = state.providers.find((item) => item.id === target.id);
        // ChatGPT OAuth / pure Responses providers only support the main chat-test path.
        const supportsExtraTests = provider?.authType !== 'chatgpt_oauth'
          && provider?.protocol !== 'openai_responses';
        const baseBody = {
          model: chatTestModel.trim(),
          systemPrompt: providerChatOptions.systemPrompt,
          userPrompt: providerChatOptions.userPrompt,
        };
        const thinkingBody = {
          ...baseBody,
          thinkingField: providerChatOptions.thinkingField,
          thinkingValue: providerChatOptions.thinkingValue,
        };
        const mainResponse = await fetch(`${providerPath}/chat-test`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(baseBody),
        });
        const result = await parseRouteTestResponse(mainResponse);
        if (stillCurrent()) setChatTestResult(result);
        if (supportsExtraTests) {
          let cacheResponse: Response | null = null;
          let thinkingResponse: Response | null = null;
          try {
            [cacheResponse, thinkingResponse] = await Promise.all([
              fetch(`${providerPath}/cache-test`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(baseBody),
              }),
              fetch(`${providerPath}/thinking-test`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(thinkingBody),
              }),
            ]);
          } catch { /* extra tests are best-effort; the main result is authoritative */ }
          let cacheResult: ProviderCacheTestResult | null = null;
          let thinkingResult: ProviderThinkingTestResult | null = null;
          if (cacheResponse) {
            try {
              cacheResult = await parseRouteTestResponse(cacheResponse) as ProviderCacheTestResult;
            } catch { /* same best-effort rule */ }
          }
          if (thinkingResponse) {
            try {
              thinkingResult = await parseRouteTestResponse(thinkingResponse) as ProviderThinkingTestResult;
            } catch { /* same best-effort rule */ }
          }
          if (stillCurrent()) {
            setCacheTestResult(cacheResult);
            setThinkingTestResult(thinkingResult);
            setCacheTestOpen(true);
            setThinkingTestOpen(true);
          }
        } else if (stillCurrent()) {
          setCacheTestResult(null);
          setThinkingTestResult(null);
          setCacheTestOpen(false);
          setThinkingTestOpen(false);
        }
        showToast(result.success ? `对话测试成功：HTTP ${result.status}` : `对话测试未通过：${result.status || result.error || 'unknown'}`);
      }
      if (activeNav === 'traffic-tokens' && logsFetchedOnce && logsPageRef.current === 1) {
        await refreshLogs(1, undefined, undefined, { silent: true });
      }
      await Promise.all([refreshRequestStats(), refreshAppLogs()]);
    } catch (error) {
      if (stillCurrent()) setChatTestResult({ success: false, error: String(error) });
      showToast(`对话测试失败：${String(error)}`);
    } finally {
      setChatTestingKeys((keys) => keys.filter((key) => key !== targetKey));
    }
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
    setChatTestResult(null);
    setChatTestOpen(true);
  }

  function openChatTestForProvider(provider: Provider) {
    const models = state.models.filter((model) => model.providerId === provider.id);
    const defaultModel = models[0]?.id || provider.defaultModel || '';
    const thinkingPresets = thinkingPresetsForProtocol(provider.protocol);
    setChatTestContext({
      kind: 'provider',
      id: provider.id,
      title: `Provider 对话测试 · ${provider.name}`,
      description: provider.authType === 'chatgpt_oauth' || provider.protocol === 'openai_responses'
        ? '直连上游 Provider 的对话接口，验证 Provider 本身是否可用。'
        : '直连上游 Provider 的对话接口，验证 Provider 本身是否可用。运行测试时将自动执行 Cache 与 Thinking 后台测试。',
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
    setCacheTestOpen(false);
    setThinkingTestOpen(false);
    setChatTestOpen(true);
    if (provider.authType === 'claude_oauth' || provider.authType === 'cursor_oauth' || provider.authType === 'chatgpt_oauth' || provider.authType === 'qoder_pat') return;
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
    if (provider.authType === 'claude_oauth' || provider.authType === 'cursor_oauth' || provider.authType === 'chatgpt_oauth' || provider.authType === 'qoder_pat') {
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
  }

  function resetQoderPATFlowState() {
    setQoderPatInput('');
    setQoderPatBusy(false);
    setQoderPatError('');
  }

  function resetClaudeOAuthFlowState() {
    setClaudeOAuthState('');
    setClaudeOAuthCode('');
    setClaudeOAuthBusy(false);
    setClaudeOAuthError('');
    setClaudeOAuthFlowId('');
    setClaudeOAuthPolling(false);
  }

  // 原始令牌只在生成瞬间返回一次；换 Provider / 关闭弹窗都要清掉内存里的值，
  // 避免残留在别的 Provider 编辑视图里被误用。
  function resetSelfRegistrationState() {
    setSelfRegToken('');
    setSelfRegBusy(false);
  }

  function openProviderModal() {
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
    resetSelfRegistrationState();
    setProviderModalOpen(true);
  }

  function resolveProviderAuthType(provider: Provider): 'api_key' | 'claude_oauth' | 'cursor_oauth' | 'chatgpt_oauth' | 'qoder_pat' | 'self_register' {
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
    resetSelfRegistrationState();
    setProviderModalOpen(true);
  }

  function openCloneProviderModal(provider: Provider) {
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
    setApiKeyDraft({
      name: '新 API 密钥',
      providerId: selectedProvider?.id || state.providers[0]?.id || '',
      outputProtocol: selectedOutputProtocol || 'openai_chat',
      modelOverride: '',
      modelAliases: {},
      thinkingDepthOverride: '',
      maxOutputTokens: 0,
      streamEnabled: true,
    });
    setApiKeyModalOpen(true);
  }

  function openCloneApiKeyModal(key: APIKey) {
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
    });
    setApiKeyModalOpen(true);
  }

  async function ensureRouteForBinding(providerId: string, outputProtocol: Protocol) {
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
        mode: 'auto',
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
      const isQoderPAT = providerDraft.protocol === 'openai_chat' && providerDraft.authType === 'qoder_pat';
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
        authType: isClaudeOAuth ? 'claude_oauth' : isCursorOAuth ? 'cursor_oauth' : isChatGPTOAuth ? 'chatgpt_oauth' : isQoderPAT ? 'qoder_pat' : 'api_key',
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
      if (wasCreate && (isSelfRegister || isClaudeOAuth || isCursorOAuth || isChatGPTOAuth || isQoderPAT)) {
        // 不关弹窗、留在编辑态，并且直接把"创建"和"发起授权/生成令牌"这两步
        // 接在同一次点击触发的调用链里完成，用户不用再多点一次。
        setEditingProviderID(saved.id);
        await refreshState(false);
        if (isSelfRegister) {
          await generateProviderSelfRegToken(saved.id);
        } else if (isClaudeOAuth) {
          showToast(`已添加输入 Provider：${saved.name}，正在跳转 Claude 授权…`);
          await startClaudeOAuthConnect(saved.id);
        } else if (isCursorOAuth) {
          showToast(`已添加输入 Provider：${saved.name}，正在跳转 Cursor 授权…`);
          await startCursorOAuthConnect(saved.id);
        } else if (isQoderPAT) {
          // No browser round-trip: the user pastes a PAT into the panel below.
          showToast(`已添加输入 Provider：${saved.name}，请粘贴 Qoder 个人访问令牌`);
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

  async function startClaudeOAuthConnect(providerId: string = editingProviderID) {
    if (!providerId) return;
    setClaudeOAuthBusy(true);
    setClaudeOAuthError('');
    try {
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(providerId)}/claude-oauth/start`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ mode: 'localhost' }),
      });
      if (!response.ok) throw new Error(await response.text());
      const data = await response.json() as { authUrl: string; state: string; flowId?: string; mode?: string };
      setClaudeOAuthState(data.state);
      if (data.flowId) setClaudeOAuthFlowId(data.flowId);
      // window.open 传 noopener/noreferrer 时规范规定返回值恒为 null（无论是否
      // 真的打开成功），没法用它判断"是否被拦截"；这里不再据此误报，统一给中性
      // 提示，并始终把链接复制到剪贴板兜底（下方"手动粘贴 code"入口始终可用）。
      window.open(data.authUrl, '_blank', 'noopener,noreferrer');
      try {
        await navigator.clipboard.writeText(data.authUrl);
        showToast('已尝试打开 Claude 授权页面；如未自动跳转，可粘贴剪贴板中的链接手动打开');
      } catch {
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
      try {
        await navigator.clipboard.writeText(data.authUrl);
        if (opened) showToast('已打开授权页面，请复制返回的 code 粘贴到下方');
      } catch {
        if (opened) showToast('已打开 Claude 授权页面');
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
      try {
        await navigator.clipboard.writeText(data.authUrl);
        showToast('已尝试打开 Cursor 授权页面；如未自动跳转，可粘贴剪贴板中的链接手动打开');
      } catch {
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
      const data = await response.json() as { authUrl: string; flowId?: string };
      if (data.flowId) setChatgptOAuthFlowId(data.flowId);
      // 同上：noopener 场景下返回值恒为 null，不再据此误报"被拦截"。
      window.open(data.authUrl, '_blank', 'noopener,noreferrer');
      try {
        await navigator.clipboard.writeText(data.authUrl);
        showToast('已尝试打开 ChatGPT 授权页面；如未自动跳转，可粘贴剪贴板中的链接手动打开');
      } catch {
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

  async function disconnectQoderPAT() {
    if (!editingProviderID) return;
    setQoderPatBusy(true);
    setQoderPatError('');
    try {
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(editingProviderID)}/qoder-pat/disconnect`, { method: 'POST' });
      await readConnectResponse(response, 'Qoder 断开连接失败');
      showToast('已断开 Qoder 账号连接（令牌已保留，随时可一键重新连接）');
      await refreshState(false);
    } catch (error) {
      setQoderPatError(String(error));
    } finally {
      setQoderPatBusy(false);
    }
  }

  /** 用已保存的个人访问令牌重新连接，无需再粘贴。 */
  async function reconnectQoderPAT() {
    if (!editingProviderID) return;
    setQoderPatBusy(true);
    setQoderPatError('');
    try {
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(editingProviderID)}/qoder-pat/reconnect`, { method: 'POST' });
      await readConnectResponse(response, 'Qoder 重新连接失败');
      resetQoderPATFlowState();
      showToast('已使用已保存的令牌重新连接');
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
    const usedBy = state.routes.filter((route) => route.providerId === providerID);
    if (!window.confirm(`确定删除输入 Provider：${providerName}？\n\n绑定到该 Provider 的 API 密钥引用（备选 / 故障转移 / 模型覆盖）将自动重置为空${usedBy.length > 0 ? `；使用它的 ${usedBy.length} 个路由（${usedBy.map((route) => route.name).join('、')}）将一并删除，绑定这些路由的密钥 RouteID 重置为空` : ''}。`)) return;
    setSaving(true);
    try {
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(providerID)}`, { method: 'DELETE' });
      if (!response.ok) throw new Error(await response.text());
      showToast(`已删除输入 Provider：${providerName}`);
      const stateResponse = await fetch(`${API_BASE}/__state`);
      const data = await stateResponse.json() as GatewayState;
      setState(data);
      setSelectedProviderID(data.providers[0]?.id || '');
      setSelectedExportProviderIDs((current) => current.filter((id) => id !== providerID));
      await refreshAppLogs();
    } catch (error) {
      showToast(`删除 Provider 失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  async function deleteSelectedProviders() {
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

  function toggleSelfcheckProvider(providerID: string) {
    const selecting = !selfcheckProviderIDs.includes(providerID);
    setSelfcheckProviderIDs((current) => (
      selecting
        ? [...current, providerID]
        : current.filter((id) => id !== providerID)
    ));
    if (selecting) {
      const provider = sortedProviders.find((item) => item.id === providerID);
      if (provider) {
        setSelfcheckModels((current) => {
          if (current[providerID]?.trim()) return current;
          const fallback = defaultSelfcheckModelForProvider(provider, state.models);
          if (!fallback) return current;
          return { ...current, [providerID]: fallback };
        });
      }
    }
  }

  function selectAllSelfcheckProviders() {
    setSelfcheckProviderIDs(sortedProviders.map((provider) => provider.id));
    setSelfcheckModels((current) => {
      const next = { ...current };
      let changed = false;
      for (const provider of sortedProviders) {
        if (next[provider.id]?.trim()) continue;
        const fallback = defaultSelfcheckModelForProvider(provider, state.models);
        if (!fallback) continue;
        next[provider.id] = fallback;
        changed = true;
      }
      return changed ? next : current;
    });
  }

  function clearSelfcheckProviders() {
    setSelfcheckProviderIDs([]);
  }

  function selfcheckModelForProvider(provider: Provider) {
    const saved = selfcheckModels[provider.id]?.trim();
    if (saved) return saved;
    return defaultSelfcheckModelForProvider(provider, state.models);
  }

  function setSelfcheckModelForProvider(providerID: string, model: string) {
    setSelfcheckModels((current) => {
      const next = { ...current };
      const trimmed = model.trim();
      if (!trimmed) delete next[providerID];
      else next[providerID] = trimmed;
      return next;
    });
  }

  async function refreshSelfcheckTools() {
    try {
      const response = await fetch(`${API_BASE}/__selfcheck/tools`, { credentials: 'same-origin' });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const data = await response.json() as { tools?: SelfcheckToolInfo[]; lanRoot?: string };
      setSelfcheckTools(data.tools || []);
      setSelfcheckLanRoot(data.lanRoot || localGatewayRoot(state.endpoints));
    } catch (error) {
      showToast(`加载自检工具状态失败：${String(error)}`);
    }
  }

  function stopSelfcheckPolling() {
    if (selfcheckPollRef.current != null) {
      window.clearInterval(selfcheckPollRef.current);
      selfcheckPollRef.current = null;
    }
  }

  async function pollSelfcheckJob(jobId: string) {
    try {
      const response = await fetch(`${API_BASE}/__selfcheck/${encodeURIComponent(jobId)}`, { credentials: 'same-origin' });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const data = await response.json() as SelfcheckJobStatus;
      setSelfcheckJob(data);
      if (data.status === 'done' || data.status === 'error') {
        stopSelfcheckPolling();
        setSelfcheckRunning(false);
        const okCount = (data.results || []).filter((item) => item.success && item.contentOK).length;
        const total = data.total || (data.results || []).length;
        if (data.status === 'error') {
          showToast(`自检失败：${data.error || '未知错误'}`);
        } else {
          showToast(`自检完成：${okCount}/${total} 通过`);
        }
        void refreshState(false);
      }
    } catch (error) {
      stopSelfcheckPolling();
      setSelfcheckRunning(false);
      showToast(`轮询自检结果失败：${String(error)}`);
    }
  }

  async function startSelfcheck() {
    if (selfcheckRunning) return;
    if (selfcheckProviderIDs.length === 0) {
      showToast('请先勾选至少一个 Provider');
      return;
    }
    const timeoutMs = Math.max(5, Math.min(600, selfcheckTimeoutSec || 90)) * 1000;
    const models: Record<string, string> = {};
    for (const providerID of selfcheckProviderIDs) {
      const provider = sortedProviders.find((item) => item.id === providerID);
      const model = (provider ? selfcheckModelForProvider(provider) : selfcheckModels[providerID] || '').trim();
      if (model) models[providerID] = model;
    }
    setSelfcheckRunning(true);
    setSelfcheckJob(null);
    stopSelfcheckPolling();
    try {
      const response = await fetch(`${API_BASE}/__selfcheck`, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          providerIds: selfcheckProviderIDs,
          timeoutMs,
          prompt: selfcheckPrompt.trim() || '1+1等于几',
          models,
        }),
      });
      if (!response.ok) throw new Error(await response.text());
      const data = await response.json() as { jobId: string };
      showToast('自检已开始，正在并行探测…');
      await pollSelfcheckJob(data.jobId);
      selfcheckPollRef.current = window.setInterval(() => {
        void pollSelfcheckJob(data.jobId);
      }, 1000);
    } catch (error) {
      setSelfcheckRunning(false);
      showToast(`启动自检失败：${String(error)}`);
    }
  }

  async function retrySelfcheckCase(caseId: string) {
    if (!selfcheckJob?.jobId || !caseId) return;
    if (selfcheckRetrying.includes(caseId)) return;
    setSelfcheckRetrying((prev) => [...prev, caseId]);
    const jobId = selfcheckJob.jobId;
    try {
      const response = await fetch(
        `${API_BASE}/__selfcheck/${encodeURIComponent(jobId)}/retry/${encodeURIComponent(caseId)}`,
        { method: 'POST', credentials: 'same-origin' },
      );
      if (!response.ok) throw new Error(await response.text());
      showToast('已开始重试该用例…');
      // The retry re-opens the job; resume polling until it settles again.
      setSelfcheckRunning(true);
      await pollSelfcheckJob(jobId);
      if (selfcheckPollRef.current == null) {
        selfcheckPollRef.current = window.setInterval(() => {
          void pollSelfcheckJob(jobId);
        }, 1000);
      }
    } catch (error) {
      showToast(`重试失败：${String(error)}`);
    } finally {
      setSelfcheckRetrying((prev) => prev.filter((id) => id !== caseId));
    }
  }

  function selfcheckClientLabel(client: string) {
    if (client === 'opencode') return 'OpenCode';
    if (client === 'codex') return 'Codex';
    if (client === 'claude') return 'Claude';
    return client;
  }

  function selfcheckKindLabel(kind?: string) {
    if (kind === 'tool') return '工具调用';
    if (kind === 'chat') return '对话';
    return kind || '对话';
  }

  function formatSelfcheckTime(iso?: string) {
    if (!iso) return '—';
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return iso;
    return d.toLocaleString('zh-CN', { hour12: false });
  }

  function openSelfcheckCaseLogs(row: SelfcheckCaseResult) {
    setSelfcheckCaseDetail(row);
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
    setSaving(true);
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

  async function updateApiKeyField(key: APIKey, field: 'name' | 'routeId' | 'modelOverride' | 'thinkingDepthOverride' | 'maxOutputTokens' | 'streamEnabled' | 'codexKeepOfficialLogin' | 'enabled', value: string | boolean | number) {
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
      if (field === 'enabled') patch.enabled = Boolean(value);
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

  async function deleteCheckedApiKeys() {
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
      const stateResponse = await fetch(`${API_BASE}/__state`);
      const data = await stateResponse.json() as GatewayState;
      setState(data);
      const nextRoute = data.routes[0];
      setSelectedRouteID(nextRoute?.id || '');
      setSelectedProviderID(nextRoute?.providerId || data.providers[0]?.id || '');
      setSelectedOutputProtocol(nextRoute?.outputProtocol || 'openai_chat');
      await refreshAppLogs();
    } catch (error) {
      showToast(`删除路由失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  async function copy(value: string) {
    await navigator.clipboard.writeText(value);
    showToast(`已复制：${value}`);
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
  const hostTemp = hostTempMetric(hostMetrics);
  const tunnelRunning = tunnel?.status === 'running';
  const livePublicURL = activePublicBaseURL(publicAccess, tunnelRunning);
  const liveUIPublicURL = activeUIPublicBaseURL(publicAccess, tunnelRunning);
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
  const chatTestLoading = chatTestContext ? chatTestingKeys.includes(`${chatTestContext.kind}:${chatTestContext.id}`) : false;
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
      ? resolveProviderChatURL(chatTestProvider, chatTestModel)
      : API_BASE;
  const chatTestCurl = chatTestContext?.kind === 'route'
    ? buildRouteTestCurl(chatTestEndpointURL, chatTestModel, chatTestMessage, chatTestBoundApiKey?.key)
    : chatTestProvider
      ? buildProviderChatCurl(chatTestProvider, chatTestModel, providerChatOptions, providerAuthPreview)
      : '';
  const chatTestResultMeta = chatTestContext?.kind === 'route'
    ? `${chatTestResult?.protocolFlow || '-'} · model=${chatTestResult?.model || '-'} · gateway=${chatTestResult?.gatewayUrl || chatTestEndpointURL}`
    : `model=${chatTestResult?.model || chatTestResolvedModel} · upstream=${chatTestResult?.targetUrl || chatTestEndpointURL}`;
  const chatTestResponseText = chatTestResult ? formatChatTestResponse(chatTestResult) : '';
  const providerThinkingPresets = chatTestProvider ? thinkingPresetsForProtocol(chatTestProvider.protocol) : null;
  const providerThinkingFieldPresets = providerThinkingPresets?.fields.find((item) => item.key === providerChatOptions.thinkingField) || providerThinkingPresets?.fields[0];
  const usageToday = requestStats?.today;
  const usageMonth = requestStats?.month;
  const needsAuthGate = Boolean(authChecked && authStatus?.requireAuth && !authStatus.authenticated);
  const authSetupMode = Boolean(needsAuthGate && authStatus && !authStatus.configured);
  // 普通用户角色：仅显示 API 密钥 / 流量 / 用量三个页面
  const isNormalUser = Boolean(authStatus?.authenticated && authStatus.role === 'user');
  const roleNavItems = isNormalUser ? navItems.filter((item) => userAllowedNavIDs.includes(item.id)) : navItems;
  // 「输入 Provider」「API 密钥」置顶，其余保持原顺序。
  const visibleNavItems = [
    ...roleNavItems.filter((item) => coreNavIDs.includes(item.id)),
    ...roleNavItems.filter((item) => !coreNavIDs.includes(item.id)),
  ];

  if (!authChecked) {
    return null;
  }

  if (needsAuthGate) {
    return (
      <div className="auth-shell">
        <div className="auth-card">
          <div className="brand">
            <div className="brand-logo">PG</div>
            <div>
              <div className="brand-title">协议网关</div>
              <div className="brand-subtitle">{authSetupMode ? '首次远程访问，请设置管理员密码' : '请登录管理控制台'}</div>
            </div>
          </div>
          <div className="hint-line">
            {authSetupMode
              ? '公网与局域网访问需要管理员密码；本机 App（127.0.0.1）可免登录。'
              : '此域名仅用于管理控制台，模型 API 请使用独立的 API 域名与 API Key。'}
          </div>
          {!authSetupMode ? (
            <label className="field">
              <span>用户名</span>
              <input
                type="text"
                autoComplete="username"
                value={authUsername}
                onChange={(event) => setAuthUsername(event.target.value)}
                placeholder="管理员留空或填 admin"
              />
            </label>
          ) : null}
          <label className="field">
            <span>{authSetupMode ? '管理员密码' : '密码'}</span>
            <input
              type="password"
              autoComplete={authSetupMode ? 'new-password' : 'current-password'}
              value={authPassword}
              onChange={(event) => setAuthPassword(event.target.value)}
              placeholder="至少 8 位"
            />
          </label>
          {authSetupMode ? (
            <label className="field">
              <span>确认密码</span>
              <input
                type="password"
                autoComplete="new-password"
                value={authPasswordConfirm}
                onChange={(event) => setAuthPasswordConfirm(event.target.value)}
                placeholder="再输入一次"
              />
            </label>
          ) : null}
          {authError ? <div className="hint-line error">{authError}</div> : null}
          <div className="public-simple-actions">
            <button
              className="btn primary"
              disabled={authBusy}
              onClick={() => void submitAdminAuth(authSetupMode ? 'setup' : 'login')}
            >
              {authBusy ? '处理中…' : authSetupMode ? '设置密码并进入' : '登录'}
            </button>
          </div>
        </div>
      </div>
    );
  }

  if (!stateHydrated) {
    return null;
  }

  return (
    <>
      <div className={`shell${sidebarCollapsed ? ' sidebar-collapsed' : ''}`}>
        <aside className={`sidebar${sidebarCollapsed ? ' collapsed' : ''}`}>
          <div className="brand">
            <div className="brand-logo">PG</div>
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

          <nav className="nav">
            {navGroups.map((group) => {
              const items = group.ids
                .map((id) => visibleNavItems.find((item) => item.id === id))
                .filter((item): item is typeof visibleNavItems[number] => Boolean(item));
              if (items.length === 0) return null;
              return (
                <div className="nav-group" key={group.label}>
                  <div className="nav-group-label">{group.label}</div>
                  {items.map((item) => (
                    <a
                      className={`nav-item ${activeNav === item.id ? 'active' : ''}`}
                      href={navPathForID(item.id)}
                      key={item.id}
                      title={sidebarCollapsed ? item.label : undefined}
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
              );
            })}
          </nav>

          <div className="sidebar-footer">
            <ThemeSwitch value={themeMode} onChange={setThemeMode} size="compact" />
            {authStatus?.authenticated && authStatus.username ? (
              <div className="user-card">
                <div className="user-avatar">{authStatus.username.slice(0, 1).toUpperCase()}</div>
                <div className="user-meta">
                  <b>{authStatus.username}</b>
                  <span title={dataFetchedAt ? `页面数据最近一次成功拉取的时间（约每 5 秒自动刷新）` : undefined}>
                    {dataFetchedAt ? <i className="live-dot" aria-hidden="true" /> : null}
                    {authStatus.role === 'user' ? '普通用户' : '管理员'}
                    {dataFetchedAt ? ` · 更新于 ${dataFetchedAt.toLocaleTimeString()}` : ''}
                  </span>
                </div>
                {authStatus?.requireAuth ? (
                  <button
                    className="icon-btn"
                    type="button"
                    disabled={authBusy}
                    onClick={() => void logoutAdmin()}
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
            ) : authStatus?.requireAuth ? (
              <button className="btn" type="button" disabled={authBusy} onClick={() => void logoutAdmin()}>
                退出登录
              </button>
            ) : null}
          </div>
        </aside>

        <main className="main">
          {activeNav === 'api-keys' && (
          <section className="section-full">
            <div className="card panel api-keys-panel">
              <div className="panel-header">
                <div>
                  <h2 className="panel-title">API 密钥</h2>
                  <p className="panel-desc">客户端使用 Bearer 或 x-api-key 携带密钥。选择输入 Provider 与输出协议，网关自动完成透传或转换。</p>
                </div>
                <button className="btn primary" disabled={saving || state.providers.length === 0} onClick={openApiKeyModal}>新建 API 密钥</button>
              </div>
              {state.providers.length === 0 ? <div className="empty-state">请先创建至少一个输入 Provider，再添加 API 密钥。</div> : null}
              {(state.apiKeys || []).length > 0 ? (
                <div className="api-keys-toolbar">
                  <div className="field api-keys-filter-field">
                    <label>名称</label>
                    <input
                      type="search"
                      className="api-keys-filter-search"
                      autoComplete="one-time-code"
                      data-1p-ignore
                      data-lpignore="true"
                      name="apiKeyFilterKeyword"
                      placeholder="名称 / 密钥"
                      value={apiKeyKeyword}
                      {...antiAutofillProps()}
                      onChange={(event) => {
                        reportIfLooksLikeAutofill('apikey-filter-filled', apiKeyKeyword, event.target.value, event);
                        setApiKeyKeyword(event.target.value);
                      }}
                    />
                  </div>
                  {!isNormalUser ? (
                    <MultiSelectFilter
                      label="所属用户"
                      options={[
                        { id: LOG_OWNER_FILTER_ADMIN, label: '管理员' },
                        ...consoleUsers.filter((user) => user.role !== 'admin').map((user) => ({ id: user.id, label: user.username })),
                      ]}
                      selected={apiKeyOwnerFilter}
                      onChange={setApiKeyOwnerFilter}
                      allLabel="全部用户"
                    />
                  ) : null}
                  <MultiSelectFilter
                    label="输入 Provider"
                    options={state.providers.map((provider) => ({ id: provider.id, label: providerOptionLabel(provider) }))}
                    selected={apiKeyProviderFilter}
                    onChange={setApiKeyProviderFilter}
                    allLabel="全部 Provider"
                  />
                  <MultiSelectFilter
                    label="输出协议"
                    options={(['openai_chat', 'openai_responses', 'claude'] as Protocol[]).map((protocol) => ({ id: protocol, label: protocolLabel(protocol) }))}
                    selected={apiKeyProtocolFilter}
                    onChange={setApiKeyProtocolFilter}
                    allLabel="全部协议"
                  />
                  <div className="api-keys-toolbar-meta">
                    显示 {filteredApiKeys.length} / {(state.apiKeys || []).length} 个密钥
                    {apiKeyTotalPages > 1 ? ` · 第 ${currentApiKeyPage} / ${apiKeyTotalPages} 页` : ''}
                    {checkedApiKeyIDs.length > 0 ? ` · 已选 ${checkedApiKeyIDs.length}` : ''}
                  </div>
                  {checkedApiKeyIDs.length > 0 ? (
                    <div className="api-keys-bulk-actions">
                      <button className="btn danger" type="button" disabled={saving} onClick={() => void deleteCheckedApiKeys()}>
                        删除选中（{checkedApiKeyIDs.length}）
                      </button>
                      <button className="mini-btn" type="button" disabled={saving} onClick={clearApiKeyChecks}>清除选择</button>
                    </div>
                  ) : null}
                </div>
              ) : null}
              {(state.apiKeys || []).length === 0 ? (
                <div className="empty-state">暂无 API 密钥。点击「新建 API 密钥」生成 sk-gw-… 密钥。</div>
              ) : (
                <div className="api-keys-layout">
                  <div className="api-keys-table-wrap">
                    <div className="api-keys-table">
                      <div className={`api-keys-table-head${!isNormalUser ? ' with-owner' : ''}`}>
                        <label className="api-keys-check" title="全选当前列表" onClick={(event) => event.stopPropagation()}>
                          <input
                            type="checkbox"
                            checked={filteredApiKeys.length > 0 && filteredApiKeys.every((key) => checkedApiKeyIDs.includes(key.id))}
                            disabled={filteredApiKeys.length === 0 || saving}
                            onChange={(event) => {
                              if (event.target.checked) selectAllFilteredApiKeys();
                              else clearApiKeyChecks();
                            }}
                            aria-label="全选当前列表"
                          />
                        </label>
                        <button
                          type="button"
                          className={`api-keys-sort-btn${apiKeySortBy === 'name' ? ' active' : ''}`}
                          onClick={() => toggleApiKeySort('name')}
                          title="按名称排序"
                        >
                          名称{apiKeySortBy === 'name' ? (apiKeySortDir === 'asc' ? ' ↑' : ' ↓') : ''}
                        </button>
                        {!isNormalUser ? (
                          <button
                            type="button"
                            className={`api-keys-sort-btn${apiKeySortBy === 'owner' ? ' active' : ''}`}
                            onClick={() => toggleApiKeySort('owner')}
                            title="按所属用户排序"
                          >
                            用户{apiKeySortBy === 'owner' ? (apiKeySortDir === 'asc' ? ' ↑' : ' ↓') : ''}
                          </button>
                        ) : null}
                      </div>
                      {filteredApiKeys.length === 0 ? (
                        <div className="empty-state compact">当前筛选条件下没有匹配的密钥。</div>
                      ) : pagedApiKeys.map((key, pageIndex) => {
                        // index 基于 filteredApiKeys 全局序号，保证 Shift 连选跨页也正确。
                        const index = apiKeyPageStart + pageIndex;
                        const checked = checkedApiKeyIDs.includes(key.id);
                        return (
                          <button
                            type="button"
                            key={key.id}
                            className={`api-keys-row${!isNormalUser ? ' with-owner' : ''}${selectedApiKey?.id === key.id ? ' active' : ''}${checked ? ' checked' : ''}`}
                            onClick={(event) => {
                              if (event.shiftKey) {
                                event.preventDefault();
                                toggleApiKeyCheck(key.id, index, true);
                                return;
                              }
                              setSelectedApiKeyID(key.id);
                            }}
                          >
                            <label
                              className="api-keys-check"
                              onClick={(event) => event.stopPropagation()}
                              onKeyDown={(event) => event.stopPropagation()}
                            >
                              <input
                                type="checkbox"
                                checked={checked}
                                disabled={saving}
                                onChange={(event) => {
                                  const native = event.nativeEvent as MouseEvent;
                                  toggleApiKeyCheck(key.id, index, !!native.shiftKey);
                                }}
                                aria-label={`选择 ${key.name}`}
                              />
                            </label>
                            <span className="api-keys-cell name" title={key.name}>{key.name}</span>
                            {!isNormalUser ? (
                              <span className="api-keys-cell owner" title={apiKeyOwnerName(key)}>{apiKeyOwnerName(key)}</span>
                            ) : null}
                          </button>
                        );
                      })}
                    </div>
                    {apiKeyTotalPages > 1 ? (
                      <div className="hint-line" style={{ display: 'flex', gap: 12, alignItems: 'center', justifyContent: 'space-between' }}>
                        <span>第 {currentApiKeyPage} / {apiKeyTotalPages} 页 · 每页 {API_KEYS_PAGE_SIZE} 个</span>
                        <span style={{ display: 'flex', gap: 8 }}>
                          <button className="mini-btn" type="button" disabled={currentApiKeyPage <= 1} onClick={() => setApiKeyPage(currentApiKeyPage - 1)}>上一页</button>
                          <button className="mini-btn" type="button" disabled={currentApiKeyPage >= apiKeyTotalPages} onClick={() => setApiKeyPage(currentApiKeyPage + 1)}>下一页</button>
                        </span>
                      </div>
                    ) : null}
                    <div className="api-keys-select-hint">勾选后可批量删除；Shift+点击可连续多选</div>
                  </div>
                  {selectedApiKey ? (
                    <ApiKeyDetailPanel
                      keyItem={selectedApiKey}
                      providers={state.providers}
                      routes={state.routes}
                      models={state.models}
                      endpoints={state.endpoints}
                      saving={saving}
                      testingProviderID={testingProviderID}
                      tunnelRunning={tunnelRunning}
                      livePublicURL={livePublicURL}
                      fixedOutputLabels={fixedOutputLabels}
                      onUpdateField={updateApiKeyField}
                      onUpdateBinding={updateApiKeyBinding}
                      onUpdateModelAliases={updateApiKeyModelAliases}
                      onUpdateFallbacks={updateApiKeyFallbacks}
                      onDelete={deleteApiKey}
                      onClone={openCloneApiKeyModal}
                      onRefreshModels={refreshApiKeyModelsForProvider}
                      onToast={showToast}
                      owners={!isNormalUser ? consoleUsers : undefined}
                      onUpdateOwner={!isNormalUser ? updateApiKeyOwner : undefined}
                      onSwitchProfile={switchApiKeyProfile}
                      onCreateProfile={createApiKeyProfile}
                      onUpdateProfile={updateApiKeyProfile}
                      onDeleteProfile={deleteApiKeyProfile}
                    />
                  ) : (
                    <div className="api-keys-detail empty-state">请从左侧列表选择一个 API 密钥。</div>
                  )}
                </div>
              )}
            </div>
          </section>
          )}

          {activeNav === 'input-providers' && (
          <section className="section-full">
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
                {isNormalUser ? (
                  <div className="panel-header-actions">
                    <button className="btn primary" disabled={saving} onClick={openProviderModal}>添加输入 Provider</button>
                  </div>
                ) : null}
                {!isNormalUser ? (
                  <div className="panel-header-actions">
                    <button className="btn" disabled={saving || selectedExportProviderIDs.length === 0} onClick={() => void exportProviders(selectedExportProviderIDs)}>导出选中{selectedExportProviderIDs.length > 0 ? ` (${selectedExportProviderIDs.length})` : ''}</button>
                    <button className="btn" disabled={saving || sortedProviders.length === 0} onClick={() => void exportProviders()}>导出全部</button>
                    <button className="btn" disabled={saving} onClick={() => providerImportInputRef.current?.click()}>导入</button>
                    <button className="btn primary" disabled={saving} onClick={openProviderModal}>添加输入 Provider</button>
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
                </div>
                {providersSearch.error ? (
                  <div className="hint-line error">正则无效，已回退为普通包含匹配：{providersSearch.error}</div>
                ) : null}
                <div className="models-filter-group">
                  {PROVIDER_CONNECT_FILTERS.map((item) => (
                    <button
                      key={item.id || 'all'}
                      type="button"
                      className={`models-filter-chip ${providersConnectFilter === item.id ? 'active' : ''}`}
                      onClick={() => setProvidersConnectFilter(item.id)}
                    >
                      {item.label}
                      {item.id === ''
                        ? ` (${sortedProviders.length})`
                        : ` (${sortedProviders.filter((provider) => providerConnectKind(provider) === item.id).length})`}
                    </button>
                  ))}
                </div>
                {isNormalUser ? (
                  <div className="models-toolbar-meta">
                    显示 {filteredProviders.length} / {sortedProviders.length}
                    {providersSearchQuery.trim() ? ` · 检索「${providersSearchQuery.trim()}」` : ''}
                    {providersConnectFilter ? ` · ${providerConnectLabel(providersConnectFilter)}` : ''}
                  </div>
                ) : null}
              </div>
              {!isNormalUser && filteredProviders.length > 0 ? (
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
                    已选 {selectedExportProviderIDs.length} / {sortedProviders.length} · 显示 {filteredProviders.length} / {sortedProviders.length}
                    {providersSearchQuery.trim() ? ` · 检索「${providersSearchQuery.trim()}」` : ''}
                    {providersConnectFilter ? ` · ${providerConnectLabel(providersConnectFilter)}` : ''}
                  </span>
                  {selectedExportProviderIDs.length > 0 ? (
                    <span className="providers-toolbar-actions">
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
                <div className="provider-card-grid">
                  {filteredProviders.map((provider) => {
                    const usedCount = (state.apiKeys || []).filter((key) => (
                      apiKeyReferencesProvider(key, state.routes, provider.id)
                    )).length;
                    return (
                      <ProviderCard
                        key={provider.id}
                        active={selectedProvider?.id === provider.id}
                        selected={selectedExportProviderIDs.includes(provider.id)}
                        name={provider.name}
                        providerId={provider.id}
                        protocol={protocolLabel(provider.protocol)}
                        tone={protocolTone(provider.protocol)}
                        url={provider.authType === 'claude_oauth' ? 'api.anthropic.com' : provider.authType === 'cursor_oauth' ? '本地 gRPC Bridge' : provider.authType === 'chatgpt_oauth' ? 'chatgpt.com/codex' : provider.baseUrl}
                        keyMask={provider.authType === 'api_key' || !provider.authType ? maskApiKeySource(provider.apiKeySource) : undefined}
                        defaultModel={provider.defaultModel}
                        modelCount={(provider.models || []).length}
                        usedCount={usedCount}
                        healthStatus={provider.healthStatus || 'unchecked'}
                        nextRetryAt={provider.nextRetryAt}
                        testing={testingProviderID === provider.id}
                        chatTesting={chatTestingKeys.includes(`provider:${provider.id}`)}
                        readOnly={isNormalUser && provider.ownerUserId !== authStatus?.userId}
                        selectable={!isNormalUser}
                        providerDisabled={!!provider.disabled}
                        onToggleEnabled={!isNormalUser ? () => void toggleProviderEnabled(provider) : undefined}
                        subtitle={(provider.authType === 'claude_oauth' && provider.claudeOAuth?.accountLabel)
                          || (provider.authType === 'chatgpt_oauth' && chatgptAccountLabelFriendly(provider.chatgptOAuth?.accountLabel))
                          || (provider.authType === 'qoder_pat' && provider.qoderPat?.accountLabel)
                          || undefined}
                        isClaudeOAuth={provider.authType === 'claude_oauth'}
                        claudeOAuthConnected={provider.claudeOAuth?.connected}
                        isCursorOAuth={provider.authType === 'cursor_oauth'}
                        cursorOAuthConnected={provider.cursorOAuth?.connected}
                        isChatGPTOAuth={provider.authType === 'chatgpt_oauth'}
                        chatgptOAuthConnected={provider.chatgptOAuth?.connected}
                        isQoderPAT={provider.authType === 'qoder_pat'}
                        qoderPATConnected={provider.qoderPat?.connected}
                        cursorBridge={provider.authType === 'cursor_oauth' ? state.cursorBridge : undefined}
                        ownerName={!isNormalUser ? providerOwnerName(provider) : undefined}
                        authorizedUserCount={!isNormalUser
                          ? consoleUsers.filter((user) => user.role !== 'admin' && (user.allowedProviderIds || []).includes(provider.id)).length
                          : undefined}
                        onShowUsers={!isNormalUser
                          ? () => { setProviderUsersModalID(provider.id); void refreshConsoleUsers(); }
                          : undefined}
                        onToggleSelect={() => toggleExportProviderSelection(provider.id)}
                        onClick={() => {
                          setSelectedProviderID(provider.id);
                          showToast(`已选择输入 Provider：${provider.name}`);
                        }}
                        onTest={() => void fetchProviderModels(provider.id, provider.name, true)}
                        onChatTest={() => openChatTestForProvider(provider)}
                        onConformance={() => void runProviderConformance(provider)}
                        onEdit={() => openEditProviderModal(provider)}
                        onClone={() => openCloneProviderModal(provider)}
                        onDelete={() => void deleteProvider(provider.id, provider.name)}
                      />
                    );
                  })}
                </div>
              )}
              {selectedProvider && !isNormalUser ? <div className="hint-line">当前 Provider 被 {activeProviderRouteCount} 个 API 密钥引用（含备选）。删除时这些密钥的 Provider 绑定会自动重置为空。</div> : null}
            </div>
          </section>
          )}

          {activeNav === 'output-providers' && !isNormalUser && (
          <section className="section-full">
            <div className="card panel">
              <div className="panel-header">
                <div>
                  <h2 className="panel-title">接入地址</h2>
                  <p className="panel-desc">网关对外的三种协议接入地址（复制给客户端使用）；公网访问在「公网访问」页统一配置。流式开关已移至 API Key（与 Key 绑定）。</p>
                </div>
                <Badge tone={publicStatusTone(publicAccess.status)}>{publicAccessStatusLabel(publicAccess.status)}</Badge>
              </div>
              {state.endpoints.map((endpoint) => {
                const url = endpointURL(endpoint);
                const publicEndpointURL = publicAccessURL(endpoint, tunnelRunning);
                return (
                  <div className="endpoint-pair" key={endpoint.id}>
                    <URLRow label={`${protocolLabel(endpoint.protocol)} 局域网`} value={url} onCopy={() => copy(url)} />
                    <URLRow label={`${protocolLabel(endpoint.protocol)} 公网`} value={publicEndpointURL} onCopy={tunnelRunning && endpoint.publicUrl ? () => copy(publicEndpointURL) : undefined} />
                  </div>
                );
              })}
            </div>
          </section>
          )}

          {activeNav === 'usage-stats' && (
          <section className="section-full">
            <div className="card panel">
              <div className="panel-header">
                <div>
                  <h2 className="panel-title">用量统计</h2>
                  <p className="panel-desc">
                    按日期区间汇总请求与 Token；单击日历选单日，Shift+单击选区间。
                    {dataFetchedAt ? ` · 数据更新于 ${dataFetchedAt.toLocaleTimeString()}` : ''}
                  </p>
                </div>
              </div>

              <div className="usage-range-bar" style={{ marginBottom: 12 }}>
                <div className="usage-range-left">
                  <UsageRangeCalendar
                    from={usageFrom}
                    to={usageTo}
                    onSelect={(nextFrom, nextTo) => {
                      usageFollowTodayRef.current = isFollowingTodayRange(nextFrom, nextTo);
                      setUsageFrom(nextFrom);
                      setUsageTo(nextTo);
                      void refreshRequestStats(nextFrom, nextTo);
                    }}
                  />
                  <div className="usage-range-summary">
                    当前区间：{usageFrom === usageTo ? usageFrom : `${usageFrom} ~ ${usageTo}`}
                  </div>
                </div>
                <UsageMonthlyTokenBars
                  points={monthlyDaily}
                  onPickDay={(date) => {
                    usageFollowTodayRef.current = isFollowingTodayRange(date, date);
                    setUsageFrom(date);
                    setUsageTo(date);
                    void refreshRequestStats(date, date);
                  }}
                />
              </div>

              <div className="grid-4">
                <Metric label="区间总请求" value={String(requestStats?.range?.total.requestCount ?? usageToday?.total.requestCount ?? 0)} note={requestStats?.range?.period || usageToday?.date || '—'} />
                <Metric label="区间总 Token" value={formatTokenCount(((requestStats?.range?.total.inputTokens ?? usageToday?.total.inputTokens ?? 0) + (requestStats?.range?.total.outputTokens ?? usageToday?.total.outputTokens ?? 0)))} note={requestStats?.range?.total ? formatTokenSummary(requestStats.range.total) : (usageToday?.total ? formatTokenSummary(usageToday.total) : '暂无数据')} />
                <Metric label="本月总请求" value={String(usageMonth?.total.requestCount ?? 0)} note={usageMonth?.period || '—'} />
                <Metric label="本月总 Token" value={formatTokenCount((usageMonth?.total.inputTokens ?? 0) + (usageMonth?.total.outputTokens ?? 0))} note={usageMonth?.total ? formatTokenSummary(usageMonth.total) : '暂无数据'} />
              </div>

              {usageFrom !== usageTo ? (
                <div className="usage-charts-full">
                  <UsageMonthlyTokenBars
                    title={`按日请求量 · ${usageFrom} ~ ${usageTo}`}
                    points={requestStats?.daily || []}
                    onPickDay={(date) => {
                      usageFollowTodayRef.current = isFollowingTodayRange(date, date);
                      setUsageFrom(date);
                      setUsageTo(date);
                      void refreshRequestStats(date, date);
                    }}
                  />
                </div>
              ) : null}

              <div className="usage-charts">
                {usageFrom === usageTo ? (
                  <UsageLineChart title="按日请求量" points={requestStats?.daily || []} />
                ) : null}
                <UsageBarChart
                  title="按 API Key 请求"
                  items={(requestStats?.range?.byApiKey || usageToday?.byApiKey || []).slice(0, 8).map((item) => ({
                    label: item.apiKeyName || item.apiKeyId || '未命名',
                    value: item.requestCount,
                  }))}
                />
                <UsageBarChart
                  title="按 Provider 请求"
                  items={(requestStats?.range?.byProvider || usageToday?.byProvider || []).slice(0, 8).map((item) => ({
                    label: providerUsageLabel(item.providerId, state.providers || []),
                    value: item.requestCount,
                  }))}
                />
                <UsageBarChart
                  title="按输出协议请求"
                  items={(requestStats?.range?.byProtocol || usageToday?.byProtocol || []).slice(0, 8).map((item) => ({
                    label: item.protocol,
                    value: item.requestCount,
                  }))}
                />
                {!isNormalUser && (
                  <UsageBarChart
                    title="按用户请求"
                    items={(requestStats?.range?.byUser || usageToday?.byUser || []).slice(0, 8).map((item) => ({
                      label: item.userName || item.userId || '未绑定用户',
                      value: item.requestCount,
                    }))}
                  />
                )}
                <UsageBarChart
                  title="模型使用量排名（Token）"
                  formatValue={formatCompactCount}
                  items={(requestStats?.range?.byModel || usageToday?.byModel || [])
                    .filter((item) => item.model && item.model !== '_unknown')
                    .slice(0, 8)
                    .map((item) => ({
                      label: item.model,
                      value: (item.inputTokens || 0) + (item.outputTokens || 0),
                    }))}
                />
                <UsageCacheHitRate
                  title="缓存命中率（区间）"
                  input={requestStats?.range?.total.inputTokens ?? usageToday?.total.inputTokens ?? 0}
                  cache={requestStats?.range?.total.cacheTokens ?? usageToday?.total.cacheTokens ?? 0}
                />
                <UsageStatusChart title="状态码分布" items={requestStats?.status || []} />
              </div>

              <div className="usage-charts-full">
                <UsageDailyTrafficLines
                  title={usageFrom === usageTo ? '按日流量（网关转发）' : `按日流量（网关转发） · ${usageFrom} ~ ${usageTo}`}
                  points={requestStats?.daily || []}
                  onPickDay={(date) => {
                    usageFollowTodayRef.current = isFollowingTodayRange(date, date);
                    setUsageFrom(date);
                    setUsageTo(date);
                    void refreshRequestStats(date, date);
                  }}
                />
                <p className="hint-line">
                  统计网关实际转发的请求体 / 响应体字节数（业务流量），一天一条、永久保留。
                  与「机器状态」页的整机网卡累计值不同：后者含本机所有流量且重启归零。
                </p>
              </div>

              {usageToday?.lastRequest ? (
                <div className="usage-last-card">
                  <div className="usage-last-title">最近一次请求 · {new Date(usageToday.lastRequest.time).toLocaleString()}</div>
                  <div className="hint-line">
                    {usageToday.lastRequest.apiKeyName || '未绑定 Key'} · {usageToday.lastRequest.model} · HTTP {usageToday.lastRequest.status} · {formatTokenSummary({
                      inputTokens: usageToday.lastRequest.inputTokens,
                      outputTokens: usageToday.lastRequest.outputTokens,
                      cacheTokens: usageToday.lastRequest.cacheTokens || 0,
                    })}
                  </div>
                </div>
              ) : null}

              <details className="usage-details">
                <summary>明细数据（按维度下钻）</summary>
                <div className="usage-details-body">
              <div className="usage-table-wrap">
                <div className="usage-section-title">按 API 密钥（区间）</div>
                <div className="usage-table">
                  <div className="usage-header">
                    <span>API 密钥</span>
                    <span>区间请求</span>
                    <span>区间 Token</span>
                    <span>本月请求</span>
                    <span>本月 Token</span>
                  </div>
                  {(() => {
                    const rangeKeys = requestStats?.range?.byApiKey || usageToday?.byApiKey || [];
                    if (rangeKeys.length === 0) {
                      return <div className="empty-state">暂无 API 密钥或请求记录。</div>;
                    }
                    return rangeKeys.map((row) => {
                      const month = usageMonth?.byApiKey.find((item) => item.apiKeyId === row.apiKeyId);
                      return (
                        <div className="usage-row" key={row.apiKeyId || row.apiKeyName}>
                          <span className="usage-key-name">{row.apiKeyName || row.apiKeyId}</span>
                          <span>{row.requestCount}</span>
                          <span>{formatTokenSummary(row)}</span>
                          <span>{month?.requestCount ?? 0}</span>
                          <span>{month ? formatTokenSummary(month) : '—'}</span>
                        </div>
                      );
                    });
                  })()}
                </div>
              </div>

              <div className="usage-table-wrap">
                <div className="usage-section-title">按 Provider（区间）</div>
                <div className="usage-table">
                  <div className="usage-header">
                    <span>Provider</span>
                    <span>区间请求</span>
                    <span>区间 Token</span>
                    <span>本月请求</span>
                    <span>本月 Token</span>
                  </div>
                  {(() => {
                    const rows = [...(requestStats?.range?.byProvider || usageToday?.byProvider || [])];
                    if (rows.length === 0) {
                      return <div className="empty-state">暂无 Provider 或请求记录。</div>;
                    }
                    return rows.map((row) => {
                      const month = usageMonth?.byProvider?.find((item) => item.providerId === row.providerId);
                      return (
                        <div className="usage-row" key={row.providerId}>
                          <span className="usage-key-name">{providerUsageLabel(row.providerId, state.providers || [])}</span>
                          <span>{row.requestCount}</span>
                          <span>{formatTokenSummary(row)}</span>
                          <span>{month?.requestCount ?? 0}</span>
                          <span>{month ? formatTokenSummary(month) : '—'}</span>
                        </div>
                      );
                    });
                  })()}
                </div>
              </div>

              <div className="usage-table-wrap">
                <div className="usage-section-title">按输出协议（区间）</div>
                <div className="usage-table">
                  <div className="usage-header">
                    <span>输出协议</span>
                    <span>区间请求</span>
                    <span>区间 Token</span>
                    <span>本月请求</span>
                    <span>本月 Token</span>
                  </div>
                  {(() => {
                    const rows = [...(requestStats?.range?.byProtocol || usageToday?.byProtocol || [])];
                    if (rows.length === 0) {
                      return <div className="empty-state">暂无输出协议请求记录。</div>;
                    }
                    return rows.map((row) => {
                      const month = usageMonth?.byProtocol?.find((item) => item.protocol === row.protocol);
                      return (
                        <div className="usage-row" key={row.protocol}>
                          <span className="usage-key-name">{row.protocol}</span>
                          <span>{row.requestCount}</span>
                          <span>{formatTokenSummary(row)}</span>
                          <span>{month?.requestCount ?? 0}</span>
                          <span>{month ? formatTokenSummary(month) : '—'}</span>
                        </div>
                      );
                    });
                  })()}
                </div>
              </div>

              {!isNormalUser && (
                <div className="usage-table-wrap">
                  <div className="usage-section-title">按用户（区间）</div>
                  <div className="usage-table">
                    <div className="usage-header">
                      <span>用户</span>
                      <span>区间请求</span>
                      <span>区间 Token</span>
                      <span>本月请求</span>
                      <span>本月 Token</span>
                    </div>
                    {(() => {
                      const rows = requestStats?.range?.byUser || usageToday?.byUser || [];
                      if (rows.length === 0) {
                        return <div className="empty-state">暂无用户请求记录。</div>;
                      }
                      return rows.map((row) => {
                        const month = usageMonth?.byUser?.find((item) => item.userId === row.userId);
                        return (
                          <div className="usage-row" key={row.userId || row.userName}>
                            <span className="usage-key-name">{row.userName || row.userId || '未绑定用户'}</span>
                            <span>{row.requestCount}</span>
                            <span>{formatTokenSummary(row)}</span>
                            <span>{month?.requestCount ?? 0}</span>
                            <span>{month ? formatTokenSummary(month) : '—'}</span>
                          </div>
                        );
                      });
                    })()}
                  </div>
                </div>
              )}

              <div className="usage-table-wrap">
                <div className="usage-section-title">模型使用量排名（区间，按 Token 总量）</div>
                <div className="usage-table">
                  <div className="usage-header">
                    <span>排名 / 模型</span>
                    <span>区间请求</span>
                    <span>区间 Token</span>
                    <span>本月请求</span>
                    <span>本月 Token</span>
                  </div>
                  {(() => {
                    const rows = [...(requestStats?.range?.byModel || usageToday?.byModel || [])]
                      .filter((item) => item.model && item.model !== '_unknown');
                    if (rows.length === 0) {
                      return <div className="empty-state">暂无模型请求记录。</div>;
                    }
                    return rows.map((row, index) => {
                      const month = usageMonth?.byModel?.find((item) => item.model === row.model);
                      return (
                        <div className="usage-row" key={row.model}>
                          <span className="usage-key-name">#{index + 1} {row.model}</span>
                          <span>{formatCompactCount(row.requestCount)}</span>
                          <span>{formatTokenSummaryCompact(row)}</span>
                          <span>{formatCompactCount(month?.requestCount ?? 0)}</span>
                          <span>{month ? formatTokenSummaryCompact(month) : '—'}</span>
                        </div>
                      );
                    });
                  })()}
                </div>
              </div>
                </div>
              </details>
            </div>
          </section>
          )}

          {activeNav === 'public-access' && !isNormalUser && (
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
                <div className="hint-line">
                  {webExposed
                    ? '管理页公网需管理员密码；模型 API 使用 API Key。可在下方分别开启管理页或 API 公网。'
                    : '关闭后局域网与穿透均不可用；若公网隧道在跑会自动停止。'}
                </div>
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
                <div className="hint-line">管理页面：{composedUIDomain ? `https://${composedUIDomain}` : '—'}</div>
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
                <div className="hint-line">模型 API：{composedCustomDomain ? `https://${composedCustomDomain}` : '—'}</div>
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
                  <div className="hint-line">正在等待 Cloudflare 授权，请在浏览器中完成登录并选择要绑定的域名…</div>
                ) : (
                  <div className="hint-line">根域名来自 Cloudflare 授权。管理页域名只服务控制台，API 域名只服务模型协议。</div>
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
                  {!webExposed ? <span className="hint-line">请先开启局域网 / 穿透访问。</span> : null}
                  {publicDraft.exposeApi === false && publicDraft.exposeUi === false ? (
                    <span className="hint-line">请先开启「管理页公网」或「模型 API 公网」至少一个开关。</span>
                  ) : null}
                  {quickTunnelActive ? <span className="hint-line">当前正在使用快速隧道，需先关闭后再启域名隧道。</span> : null}
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
                    <div className="hint-line">若你已在 Zero Trust 手动创建隧道，可粘贴 token 后直接启动，无需浏览器授权。</div>
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
                      <div className="hint-line">快速隧道为临时单地址，管理页与 API 共用，适合测试。</div>
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
                <div className="hint-line">
                  {publicControlTokenConfigured
                    ? `已启用 · 令牌尾号 ****${publicControlTokenPreview || '----'}`
                    : '尚未生成令牌，生成后才能远程开关。'}
                </div>
                {publicControlRawToken ? (
                  <div className="field field-full">
                    <label>新令牌（只显示这一次，请立即复制保存）</label>
                    <div className="field-inline">
                      <input readOnly value={publicControlRawToken} onFocus={(event) => event.currentTarget.select()} />
                      <CopyButton value={publicControlRawToken} label="复制令牌" />
                    </div>
                  </div>
                ) : null}
                <div className="hint-line">
                  四种操作分别是：购买域名（custom_domain）开 / 关，免费随机域名（random_tunnel）开 / 关。
                  底层只有一条隧道，开启某一种即切换到该模式；关闭未在运行的那一种是无副作用的空操作。
                </div>
                <div className="hint-line">
                  安全限制：该接口只在本机 / 局域网可用，公网域名调用一律 403 —— 令牌泄露也不会让外网有人替你开关公网。
                  代价是关掉公网后要重新打开，必须回到本机或局域网操作。
                </div>
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
                <button className="btn" onClick={() => void refreshState()}>刷新列表</button>
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
                <div className="models-filter-group">
                  <button
                    className={`models-filter-chip ${modelsProviderFilter === '__all__' ? 'active' : ''}`}
                    onClick={() => setModelsProviderFilter('__all__')}
                  >
                    全部 ({state.models.length})
                  </button>
                  {modelsMenuProviders.map((provider) => (
                    <button
                      key={provider.id}
                      className={`models-filter-chip ${modelsProviderFilter === provider.id ? 'active' : ''}`}
                      onClick={() => setModelsProviderFilter(provider.id)}
                    >
                      {provider.name} ({modelsMenuSummary.get(provider.id) || 0})
                    </button>
                  ))}
                </div>
                <div className="models-toolbar-meta">
                  当前显示 {filteredModels.length} 个模型
                  {modelsProviderFilter !== '__all__' ? ` · ${state.providers.find((item) => item.id === modelsProviderFilter)?.name || modelsProviderFilter}` : ''}
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
                  {modelsSearchQuery.trim()
                    ? '没有匹配当前检索条件的模型，请调整关键字或正则。'
                    : '该 Provider 暂无模型记录。'}
                  {!isNormalUser && !modelsSearchQuery.trim() && (() => {
                    const provider = state.providers.find((item) => item.id === modelsProviderFilter);
                    if (!provider) return null;
                    const canSync = provider.authType === 'cursor_oauth'
                      ? !!provider.cursorOAuth?.connected
                      : provider.authType === 'claude_oauth'
                        ? !!provider.claudeOAuth?.connected
                        : provider.authType === 'chatgpt_oauth'
                          ? !!provider.chatgptOAuth?.connected
                          : provider.authType === 'qoder_pat'
                          ? !!provider.qoderPat?.connected
                        : true;
                    if (!canSync) return <div className="hint-line">请先完成 OAuth 连接后再同步模型。</div>;
                    return (
                      <div style={{ marginTop: 12 }}>
                        <button className="btn primary" type="button" disabled={testingProviderID === provider.id} onClick={() => void fetchProviderModels(provider.id, provider.name, false)}>
                          {testingProviderID === provider.id ? '同步中…' : '同步该 Provider 模型'}
                        </button>
                      </div>
                    );
                  })()}
                </div>
              ) : (
                <div className="model-grid">
                  {filteredModels.map((model) => {
                    const provider = state.providers.find((item) => item.id === model.providerId);
                    return (
                      <div className="model-card compact" key={`${model.providerId}-${model.id}`}>
                        <div className="provider-top">
                          <div className="provider-name" title={model.id}>{model.id}</div>
                          <Badge tone={model.inMenu ? 'green' : 'slate'}>{model.inMenu ? '菜单' : '隐藏'}</Badge>
                        </div>
                        <div className="provider-meta" title={provider?.name || model.providerId}>
                          {provider ? providerOptionLabel(provider) : model.providerId}
                        </div>
                        <div className="model-card-stats">
                          <span>上下文 {model.contextLength.toLocaleString()}</span>
                          <span>{protocolLabel(model.protocol)}</span>
                        </div>
                      </div>
                    );
                  })}
                </div>
              )}
            </div>
          </section>
          )}

          {activeNav === 'traffic-tokens' && (
          <section className="section-full">
            <div className="card panel traffic-panel">
              <div className="panel-header">
                <div>
                  <h2 className="panel-title">API 日志</h2>
                  <p className="panel-desc">
                    支持按时间段、状态与密钥名称筛选；展示访问来源与首 Token 延迟（TTFT）。默认保留 {requestLogRetentionDays} 天。
                    {dataFetchedAt ? ` · 数据更新于 ${dataFetchedAt.toLocaleTimeString()}` : ''}
                  </p>
                </div>
              </div>
              <div className="usage-range-bar" style={{ marginBottom: 12 }}>
                <div className="usage-range-left">
                  <UsageRangeCalendar
                    from={logsFrom}
                    to={logsTo}
                    onSelect={(nextFrom, nextTo) => {
                      logsFollowTodayRef.current = isFollowingTodayRange(nextFrom, nextTo);
                      setLogsFrom(nextFrom);
                      setLogsTo(nextTo);
                      void refreshLogs(1, nextFrom, nextTo);
                    }}
                    onClear={() => {
                      logsFollowTodayRef.current = false;
                      setLogsFrom('');
                      setLogsTo('');
                      void refreshLogs(1, '', '');
                    }}
                  />
                  <div className="usage-range-summary">
                    {logsFrom || logsTo
                      ? `当前区间：${logsFrom === logsTo ? logsFrom : `${logsFrom} ~ ${logsTo}`}`
                      : '未选日期：显示全部日志（倒序）'}
                  </div>
                </div>
                <div className="traffic-filter-fields">
                  <label className="field">
                    <span>状态</span>
                    <select value={logsStatusFilter} onChange={(e) => { setLogsPage(1); setLogsStatusFilter(e.target.value as typeof logsStatusFilter); }}>
                      <option value="all">全部</option>
                      <option value="2xx">2xx</option>
                      <option value="4xx">4xx</option>
                      <option value="5xx">5xx</option>
                    </select>
                  </label>
                  <label className="field">
                    <span>密钥名称</span>
                    <select
                      value={logsApiKeyName}
                      onChange={(e) => { setLogsPage(1); setLogsApiKeyName(e.target.value); }}
                    >
                      <option value="">全部</option>
                      {(state.apiKeys || []).map((key) => (
                        <option key={key.id} value={key.name}>{key.name}</option>
                      ))}
                    </select>
                  </label>
                  <label className="field">
                    <span>输入 Provider</span>
                    <select
                      value={logsProviderFilter}
                      onChange={(e) => { setLogsPage(1); setLogsProviderFilter(e.target.value); }}
                    >
                      <option value="">全部</option>
                      {(state.providers || []).map((provider) => (
                        <option key={provider.id} value={provider.id}>{providerOptionLabel(provider)}</option>
                      ))}
                    </select>
                  </label>
                  {!isNormalUser ? (
                    <label className="field">
                      <span>用户</span>
                      <select
                        value={logsOwnerFilter}
                        onChange={(e) => { setLogsPage(1); setLogsOwnerFilter(e.target.value); }}
                      >
                        <option value="">全部</option>
                        <option value={LOG_OWNER_FILTER_ADMIN}>管理员</option>
                        {consoleUsers.map((user) => (
                          <option key={user.id} value={user.id}>{user.username}</option>
                        ))}
                      </select>
                    </label>
                  ) : null}
                  <div className="traffic-filter-apply">
                    <button
                      className="mini-btn"
                      type="button"
                      title="回到第 1 页并按当前筛选条件刷新（各筛选项修改后已即时生效，此按钮主要用于翻页后重新回到第 1 页）"
                      onClick={() => { setLogsPage(1); void refreshLogs(1); }}
                    >
                      应用筛选
                    </button>
                  </div>
                </div>
              </div>
              <div className="log-table">
                {logsLoading || !logsFetchedOnce ? (
                  <div className="empty-state">加载流量日志中…</div>
                ) : logs.length === 0 ? (
                  <div className="empty-state">暂无流量日志。运行路由测试或真实转发请求后会记录。</div>
                ) : (
                  <>
                    <div className="log-header traffic-log-header">
                      <span>时间</span>
                      <span>状态</span>
                      <span>来源</span>
                      <span>密钥</span>
                      <span>用户</span>
                      <span>输入 Provider</span>
                      <span>模型</span>
                      <span>Token</span>
                      <span>TTFT</span>
                      <span>耗时</span>
                      <span />
                    </div>
                    {logs.map((log, index) => (
                      <div className={`log-row${isTrafficLogError(log) ? ' error' : ''}`} key={trafficLogMatchKey(log)}>
                        <div className="log-row-main traffic-log-row">
                          <span className="log-time">{new Date(log.time).toLocaleString()}</span>
                          <Badge tone={statusTone(log.status)}>{log.status}</Badge>
                          <span className="log-source" title={trafficLogSourceTitle(log)}>{accessSourceLabel(log.accessSource)}</span>
                          <span className="log-key" title={trafficLogKeyTitle(log)}>{trafficLogKeyLabel(log)}</span>
                          <span className="log-user" title={log.userName || undefined}>{log.userName || '—'}</span>
                          <span className="log-provider" title={log.providerId || undefined}>{trafficLogProviderLabel(log, state.providers || [])}</span>
                          <span className="log-model">{log.model}</span>
                          <span className="log-token" title="入=总 input（含缓存命中）；缓存=cache hit">入 {log.inputTokens} · 出 {log.outputTokens} · 缓存 {log.cacheTokens || 0}</span>
                          <span className="log-latency" title={log.upstreamTtfbMs || log.gatewayOverheadMs ? `upstreamTtfb=${log.upstreamTtfbMs ?? 0}ms overhead=${log.gatewayOverheadMs ?? 0}ms prep=${log.prepMs ?? 0}ms flags=${log.timingFlags || '-'}` : undefined}>{log.ttftMs != null ? `${log.ttftMs}ms` : '—'}</span>
                          <span className="log-latency">{log.latencyMs}ms</span>
                          {isTrafficLogError(log) ? (
                            <button className="mini-btn" type="button" onClick={() => void openTrafficLogDetail(log)}>详情</button>
                          ) : <span className="log-detail-placeholder" />}
                        </div>
                        <div className="log-row-sub" title={`${log.protocolFlow} · ${log.path}`}>
                          {trafficLogProviderLabel(log, state.providers || [])} · {actionLabel(log.action)} · {log.protocolFlow} · {log.path}{log.errorDescription ? ` · ${log.errorDescription}` : ''}
                        </div>
                      </div>
                    ))}
                    <div className="hint-line" style={{ display: 'flex', gap: 12, alignItems: 'center', justifyContent: 'space-between' }}>
                      <span>
                        共 {logsTotal} 条 · 第 {logsPage} 页 · 每页 {LOGS_PAGE_SIZE} 条
                        {logsPage > 1 ? ' · 当前页不自动刷新（避免新日志把翻页顶得乱跳），需要看最新请回到第 1 页' : ''}
                      </span>
                      <span style={{ display: 'flex', gap: 8 }}>
                        <button className="mini-btn" type="button" disabled={logsPage <= 1} onClick={() => void refreshLogs(logsPage - 1)}>上一页</button>
                        <button className="mini-btn" type="button" disabled={logsPage * LOGS_PAGE_SIZE >= logsTotal} onClick={() => void refreshLogs(logsPage + 1)}>下一页</button>
                      </span>
                    </div>
                  </>
                )}
              </div>
            </div>
          </section>
          )}

          {activeNav === 'alerts' && !isNormalUser && (
          <section className="section-full">
            <div className="card panel">
              <div className="panel-header">
                <div>
                  <h2 className="panel-title">告警</h2>
                  <p className="panel-desc">
                    检测 API 密钥是否被多方同时使用（疑似泄露）：窗口内独立 IP 数超阈值，或同一时刻多个 IP 的请求真实重叠。
                    仅告警，不会禁用密钥或限流，不影响下游正常使用。
                    检测基于请求日志（默认保留 {requestLogRetentionDays} 天），每 2 分钟扫描一次。
                  </p>
                </div>
              </div>

              <div className="models-filter-group" style={{ marginBottom: 12 }}>
                {([
                  { key: 'all', label: '全部', count: alertPage?.counts.all ?? 0 },
                  { key: 'unread', label: '未读', count: alertPage?.counts.unread ?? 0 },
                  { key: 'read', label: '已读', count: alertPage?.counts.read ?? 0 },
                  { key: 'ignored', label: '已忽略', count: alertPage?.counts.ignored ?? 0 },
                ] as const).map((chip) => (
                  <button
                    key={chip.key}
                    type="button"
                    className={`models-filter-chip${alertStatusFilter === chip.key ? ' active' : ''}`}
                    onClick={() => setAlertStatusFilter(chip.key)}
                  >
                    {chip.label} {chip.count}
                  </button>
                ))}
              </div>

              <div className="log-table">
                {alertsLoading && !alertPage ? (
                  <div className="empty-state">加载告警中…</div>
                ) : !alertPage || alertPage.items.length === 0 ? (
                  <div className="empty-state">
                    暂无告警。触发条件：同一密钥在 {alertSettings?.multiIpWindowMinutes ?? 10} 分钟内被
                    {alertSettings?.multiIpThreshold ?? 5} 个及以上不同 IP 使用，或同一时刻有
                    {alertSettings?.concurrentIpThreshold ?? 4} 个及以上不同 IP 的请求同时在飞行中。
                  </div>
                ) : (
                  <>
                    <div className="log-header alert-log-header">
                      <span>时间</span>
                      <span>规则</span>
                      <span>密钥</span>
                      <span>IP</span>
                      <span>状态</span>
                      <span>操作</span>
                    </div>
                    {alertPage.items.map((alert) => (
                      <div className={`log-row${alert.status === 'unread' ? ' error' : ''}`} key={alert.id}>
                        <div className="log-row-main alert-log-row">
                          <span>{new Date(alert.time).toLocaleString()}</span>
                          <span>
                            <Badge tone={alert.rule === 'apikey_concurrent_ip' ? 'red' : 'amber'}>
                              {alert.rule === 'apikey_concurrent_ip' ? '并发重叠' : '多 IP'}
                            </Badge>
                          </span>
                          <span>{alert.apiKeyName || alert.apiKeyId}</span>
                          <span>
                            {alert.rule === 'apikey_concurrent_ip'
                              ? `同时 ${alert.ipCount} 个`
                              : `${alert.ipCount} 个 / ${alert.windowMinutes} 分钟`}
                          </span>
                          <span>
                            <Badge tone={alert.status === 'unread' ? 'red' : alert.status === 'ignored' ? 'slate' : 'green'}>
                              {alert.status === 'unread' ? '未读' : alert.status === 'ignored' ? '已忽略' : '已读'}
                            </Badge>
                            {alert.pushStatus === 'sent' ? <Badge tone="green">已推送</Badge> : null}
                            {alert.pushStatus === 'failed' ? <Badge tone="red">推送失败</Badge> : null}
                            {alert.pushStatus === 'disabled' ? <Badge tone="slate">未配置推送</Badge> : null}
                          </span>
                          <span style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                            {alert.status !== 'read' ? (
                              <button className="mini-btn" type="button" onClick={() => void updateAlertStatus(alert.id, 'read')}>标记已读</button>
                            ) : null}
                            {alert.status !== 'ignored' ? (
                              <button className="mini-btn" type="button" onClick={() => void updateAlertStatus(alert.id, 'ignored')}>忽略</button>
                            ) : null}
                            {alert.pushStatus === 'failed' ? (
                              <button className="mini-btn" type="button" onClick={() => void retryAlertPush(alert.id)}>重推</button>
                            ) : null}
                          </span>
                        </div>
                        <div className="log-row-sub" style={{ display: 'flex', gap: 14, flexWrap: 'wrap', whiteSpace: 'normal' }}>
                          <span>请求数 {alert.requestCount}</span>
                          {alert.concurrentAt ? <span>并发时刻 {new Date(alert.concurrentAt).toLocaleString()}</span> : null}
                          <span>IP：{alert.ips.join(', ')}</span>
                          {alert.pushError ? <span>推送失败原因：{alert.pushError}</span> : null}
                        </div>
                      </div>
                    ))}
                    <div className="hint-line" style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                      <span>共 {alertPage.total} 条 · 第 {alertPage.page} 页 · 每页 {alertPage.pageSize} 条</span>
                      <span style={{ display: 'flex', gap: 8 }}>
                        <button className="mini-btn" type="button" disabled={alertPage.page <= 1} onClick={() => void refreshAlerts(alertPage.page - 1)}>上一页</button>
                        <button className="mini-btn" type="button" disabled={alertPage.page * alertPage.pageSize >= alertPage.total} onClick={() => void refreshAlerts(alertPage.page + 1)}>下一页</button>
                      </span>
                    </div>
                  </>
                )}
              </div>
            </div>

            <div className="card panel">
              <div className="panel-header">
                <div>
                  <h2 className="panel-title">监控覆盖</h2>
                  <p className="panel-desc">当前已实现与规划中的检测规则。规划中的规则尚未生效，仅用于标记后续迭代方向。</p>
                </div>
              </div>
              <div className="model-grid">
                {[
                  {
                    name: '单密钥多 IP',
                    enabled: alertSettings?.multiIpEnabled ?? false,
                    planned: false,
                    desc: `窗口内同一密钥的独立 IP 数达到阈值即告警。当前：${alertSettings?.multiIpWindowMinutes ?? 10} 分钟 / ${alertSettings?.multiIpThreshold ?? 5} 个 IP。`,
                  },
                  {
                    name: '并发时间重叠',
                    enabled: alertSettings?.concurrentIpEnabled ?? false,
                    planned: false,
                    desc: `同一时刻有多个不同 IP 的请求同时在飞行中即告警（当前阈值 ${alertSettings?.concurrentIpThreshold ?? 4} 个）。请求区间真实重叠，本人切换网络只会产生先后请求，故几乎无误报。`,
                  },
                  {
                    name: '地理 / ASN 跳变',
                    enabled: false,
                    planned: true,
                    desc: '相邻请求的 IP 归属地或运营商在物理上不可能的时间内发生跳变。需接入 IP 归属库。',
                  },
                  {
                    name: '用量突增',
                    enabled: false,
                    planned: true,
                    desc: 'token / 请求数相对历史基线暴涨。能覆盖泄露者走同一代理池出口、IP 数不超阈值的情况。',
                  },
                  {
                    name: '首见新 IP 提醒',
                    enabled: false,
                    planned: true,
                    desc: '学习历史 IP 基线，出现从未见过的 IP 时单独提醒一次。灵敏度更高但初期噪音较多。',
                  },
                ].map((rule) => (
                  <div className="model-card compact" key={rule.name}>
                    <div className="provider-top">
                      <div className="provider-name">{rule.name}</div>
                      <Badge tone={rule.planned ? 'slate' : rule.enabled ? 'green' : 'amber'}>
                        {rule.planned ? '规划中' : rule.enabled ? '已启用' : '已关闭'}
                      </Badge>
                    </div>
                    <div className="provider-meta">{rule.desc}</div>
                  </div>
                ))}
              </div>
            </div>

            <div className="card panel">
              <div className="panel-header">
                <div>
                  <h2 className="panel-title">告警设置</h2>
                  <p className="panel-desc">调整检测阈值与 Telegram 推送。Bot Token 保存后不会再下发到浏览器，只显示末 4 位。</p>
                </div>
              </div>
              {!alertSettings ? (
                <div className="empty-state">加载配置中…</div>
              ) : (
                <>
                  <div className="detail-section">
                    <div className="detail-section-title">单密钥多 IP 检测</div>
                    <label className="toggle-row" style={{ display: 'flex', alignItems: 'center', gap: 12, cursor: 'pointer', marginBottom: 12 }}>
                      <input
                        type="checkbox"
                        checked={alertSettings.multiIpEnabled}
                        disabled={saving}
                        onChange={(event) => void saveAlertSettings({ multiIpEnabled: event.target.checked })}
                      />
                      <span>启用「单密钥多 IP」检测</span>
                    </label>
                    <div className="form-grid compact">
                      <label className="field">
                        <span>检测窗口（分钟）</span>
                        <input
                          type="number"
                          min={1}
                          max={1440}
                          value={alertSettings.multiIpWindowMinutes}
                          onChange={(event) => setAlertSettings({ ...alertSettings, multiIpWindowMinutes: Number(event.target.value) || 10 })}
                          onBlur={() => void saveAlertSettings({})}
                        />
                      </label>
                      <label className="field">
                        <span>独立 IP 阈值（≥2）</span>
                        <input
                          type="number"
                          min={2}
                          value={alertSettings.multiIpThreshold}
                          onChange={(event) => setAlertSettings({ ...alertSettings, multiIpThreshold: Number(event.target.value) || 5 })}
                          onBlur={() => void saveAlertSettings({})}
                        />
                      </label>
                    </div>
                  </div>

                  <div className="detail-section">
                    <div className="detail-section-title">并发时间重叠检测</div>
                    <label className="toggle-row" style={{ display: 'flex', alignItems: 'center', gap: 12, cursor: 'pointer', marginBottom: 12 }}>
                      <input
                        type="checkbox"
                        checked={alertSettings.concurrentIpEnabled}
                        disabled={saving}
                        onChange={(event) => void saveAlertSettings({ concurrentIpEnabled: event.target.checked })}
                      />
                      <span>启用「并发时间重叠」检测</span>
                    </label>
                    <div className="form-grid compact">
                      <label className="field">
                        <span>回溯窗口（分钟）</span>
                        <input
                          type="number"
                          min={1}
                          max={1440}
                          value={alertSettings.concurrentIpWindowMinutes}
                          onChange={(event) => setAlertSettings({ ...alertSettings, concurrentIpWindowMinutes: Number(event.target.value) || 10 })}
                          onBlur={() => void saveAlertSettings({})}
                        />
                      </label>
                      <label className="field">
                        <span>同时并发 IP 阈值（≥2）</span>
                        <input
                          type="number"
                          min={2}
                          value={alertSettings.concurrentIpThreshold}
                          onChange={(event) => setAlertSettings({ ...alertSettings, concurrentIpThreshold: Number(event.target.value) || 4 })}
                          onBlur={() => void saveAlertSettings({})}
                        />
                      </label>
                    </div>
                    <p className="hint-line">
                      判定依据是请求的真实时间区间 [开始, 开始+耗时) 是否重叠，只统计回溯窗口内的记录。
                      同一 IP 的并发（客户端并行工具调用）不计入。
                    </p>
                  </div>

                  <div className="detail-section">
                    <div className="detail-section-title">冷却期</div>
                    <div className="form-grid compact">
                      <label className="field">
                        <span>冷却期（分钟，两条规则共用）</span>
                        <input
                          type="number"
                          min={1}
                          value={alertSettings.cooldownMinutes}
                          onChange={(event) => setAlertSettings({ ...alertSettings, cooldownMinutes: Number(event.target.value) || 60 })}
                          onBlur={() => void saveAlertSettings({})}
                        />
                      </label>
                    </div>
                    <p className="hint-line">
                      冷却期按「规则 + 密钥」独立计算：同一密钥在冷却期内每条规则各只告警一次，
                      避免同一次泄露反复推送；但若出现全新 IP，会立即再告一次。
                    </p>
                  </div>

                  <div className="detail-section">
                    <div className="detail-section-title">Telegram 推送</div>
                    <label className="toggle-row" style={{ display: 'flex', alignItems: 'center', gap: 12, cursor: 'pointer', marginBottom: 12 }}>
                      <input
                        type="checkbox"
                        checked={alertSettings.telegram.enabled}
                        disabled={saving}
                        onChange={(event) => void saveAlertSettings({ telegram: { ...alertSettings.telegram, enabled: event.target.checked } })}
                      />
                      <span>启用 Telegram 推送</span>
                    </label>
                    {alertSettings.telegram.botTokenConfigured ? (
                      <p className="hint-line">已配置 Bot Token · 令牌尾号 ****{alertSettings.telegram.botTokenPreview || '----'}</p>
                    ) : (
                      <p className="hint-line">尚未配置 Bot Token。在 Telegram 里找 @BotFather 创建机器人获取 token，再向机器人发一条消息以取得 chat id。</p>
                    )}
                    <div className="form-grid compact">
                      <label className="field">
                        <span>Bot Token{alertSettings.telegram.botTokenConfigured ? '（留空表示不修改）' : ''}</span>
                        <input
                          type="password"
                          autoComplete="off"
                          name="telegramBotToken"
                          placeholder="粘贴 BotFather 给出的 token"
                          value={telegramTokenInput}
                          onChange={(event) => setTelegramTokenInput(event.target.value)}
                        />
                      </label>
                      <label className="field">
                        <span>Chat ID</span>
                        <input
                          type="text"
                          placeholder="例如 123456789 或 -1001234567890"
                          value={alertSettings.telegram.chatId}
                          onChange={(event) => setAlertSettings({ ...alertSettings, telegram: { ...alertSettings.telegram, chatId: event.target.value } })}
                        />
                      </label>
                    </div>
                    <div className="public-simple-actions" style={{ display: 'flex', gap: 8, marginTop: 12 }}>
                      <button
                        className="btn primary"
                        type="button"
                        disabled={saving}
                        onClick={() => void saveAlertSettings({ botToken: telegramTokenInput.trim() })}
                      >
                        保存 Telegram 配置
                      </button>
                      <button
                        className="mini-btn"
                        type="button"
                        disabled={saving || !alertSettings.telegram.botTokenConfigured || !alertSettings.telegram.chatId.trim()}
                        onClick={() => void sendTelegramTest()}
                      >
                        发送测试消息
                      </button>
                    </div>
                  </div>
                </>
              )}
            </div>
          </section>
          )}

          {activeNav === 'users' && !isNormalUser && (
          <section className="section-full">
            <div className="card panel">
              <div className="panel-header">
                <div>
                  <h2 className="panel-title">用户管理</h2>
                  <p className="panel-desc">创建普通用户账号并分配可用的输入 Provider。普通用户仅能管理自己的 API 密钥、查看自己 Key 的流量日志与用量统计。</p>
                </div>
                <button className="btn primary" onClick={() => openUserModal()}>新建用户</button>
              </div>
              {usersLoading && consoleUsers.length === 0 ? <div className="empty-state">加载中…</div> : null}
              {!usersLoading && consoleUsers.length === 0 ? <div className="empty-state">暂无用户。点击「新建用户」创建第一个普通用户账号。</div> : null}
              {consoleUsers.length > 0 ? (
                <div className="log-table">
                  <div className="users-table-head" style={{ gridTemplateColumns: USERS_TABLE_GRID }}>
                    <span>用户名</span>
                    <span>状态</span>
                    <span>Provider</span>
                    <span>Key</span>
                    <button
                      type="button"
                      className={`api-keys-sort-btn${usersSortBy === 'userActive' ? ' active' : ''}`}
                      onClick={() => toggleUsersSort('userActive')}
                      title="用户浏览器最近一次访问控制台的时间（点击排序）"
                    >
                      user 活跃{usersSortBy === 'userActive' ? (usersSortDir === 'asc' ? ' ↑' : ' ↓') : ''}
                    </button>
                    <button
                      type="button"
                      className={`api-keys-sort-btn${usersSortBy === 'keyActive' ? ' active' : ''}`}
                      onClick={() => toggleUsersSort('keyActive')}
                      title="该用户的 API Key 最近一次被调用的时间（点击排序）"
                    >
                      key 活跃{usersSortBy === 'keyActive' ? (usersSortDir === 'asc' ? ' ↑' : ' ↓') : ''}
                    </button>
                    <span />
                  </div>
                  {consoleUsers.map((user) => {
                    const ownedKeys = (state.apiKeys || []).filter((key) => key.ownerUserId === user.id);
                    // 最近活跃：优先后端内存精确值，老账号回退到登录时间。
                    const lastActive = user.lastActiveAt || user.lastLoginAt;
                    // Key 最近使用：取该用户所有 Key 里最新的 lastUsedAt（内存精确值）。
                    const lastUsedMs = ownedKeys.reduce((acc, key) => {
                      const ts = Date.parse(key.lastUsedAt || '');
                      return Number.isNaN(ts) ? acc : Math.max(acc, ts);
                    }, 0);
                    return { user, ownedKeys, lastActive, lastUsedMs };
                  }).sort((a, b) => {
                    if (!usersSortBy) return 0; // 默认保持创建顺序
                    const dir = usersSortDir === 'asc' ? 1 : -1;
                    const va = usersSortBy === 'userActive' ? (Date.parse(a.lastActive || '') || 0) : a.lastUsedMs;
                    const vb = usersSortBy === 'userActive' ? (Date.parse(b.lastActive || '') || 0) : b.lastUsedMs;
                    if (va !== vb) return (va - vb) * dir;
                    return a.user.username.localeCompare(b.user.username, 'zh-CN');
                  }).map(({ user, ownedKeys, lastActive, lastUsedMs }) => {
                    // 「Provider」列此前只读 allowedProviderIds（管理员手动授权的白名单），
                    // 漏掉了用户自己创建的 Provider——后端权限判定
                    // （allowedProviderIDsForUser，见 user_isolation.go）本身是"白名单 ∪ 自建"，
                    // 用户能正常用自建 Provider，只是这一列没显示出来，看起来像"没权限"。
                    const allowedIds = user.allowedProviderIds || [];
                    const ownedProviderIds = state.providers
                      .filter((provider) => provider.ownerUserId === user.id)
                      .map((provider) => provider.id);
                    const providerIds = Array.from(new Set([...allowedIds, ...ownedProviderIds]));
                    const providerNames = providerIds.map((id) => {
                      const name = state.providers.find((provider) => provider.id === id)?.name || id;
                      return ownedProviderIds.includes(id) && !allowedIds.includes(id) ? `${name}（自建）` : name;
                    });
                    return (
                      <div className="log-row" key={user.id}>
                        <div className="log-row-main" style={{ gridTemplateColumns: USERS_TABLE_GRID }}>
                          <span style={{ fontWeight: 700 }}>{user.username}</span>
                          <span className={user.enabled ? 'ok' : 'err'}>{user.enabled ? '启用' : '禁用'}</span>
                          <span className="muted-text" title={providerNames.join('、')}>
                            {providerNames.length > 0 ? providerNames.join('、') : '未分配'}
                          </span>
                          <span className="muted-text" title={ownedKeys.map((key) => key.name).join('、')}>
                            {ownedKeys.length > 0 ? ownedKeys.map((key) => key.name).join('、') : '暂无'}
                          </span>
                          <span className="muted-text" title="用户浏览器最近一次访问控制台的时间">{lastActive ? new Date(lastActive).toLocaleString() : '从未活跃'}</span>
                          <span className="muted-text" title="该用户的 API Key 最近一次被调用的时间">{lastUsedMs > 0 ? new Date(lastUsedMs).toLocaleString() : '未使用'}</span>
                          <span style={{ display: 'flex', gap: 6, justifyContent: 'flex-end' }}>
                            <button className="mini-btn" type="button" onClick={() => openUserModal(user)}>编辑</button>
                            <button className="mini-btn" type="button" onClick={() => { setPasswordResetValue(''); setPasswordResetUser(user); }}>重置密码</button>
                            <button className="mini-btn" type="button" onClick={() => void toggleUserEnabled(user)}>{user.enabled ? '禁用' : '启用'}</button>
                            <button className="mini-btn danger" type="button" onClick={() => void deleteConsoleUser(user)}>删除</button>
                          </span>
                        </div>
                      </div>
                    );
                  })}
                </div>
              ) : null}
            </div>
          </section>
          )}

          {activeNav === 'self-check' && !isNormalUser && (
          <section className="section-full">
            <div className="card panel">
              <div className="panel-header">
                <div>
                  <h2 className="panel-title">自检</h2>
                  <p className="panel-desc">
                    对勾选的输入 Provider，并行用 OpenCode（Chat）、Codex（Responses）、Claude CLI 走局域网网关探测，并校验回答内容是否像「1+1=2」。每个 Provider 可单独选择模型，选择会永久记在本机。
                  </p>
                </div>
                <button
                  className="btn primary"
                  type="button"
                  disabled={selfcheckRunning || selfcheckProviderIDs.length === 0 || sortedProviders.length === 0}
                  onClick={() => void startSelfcheck()}
                >
                  {selfcheckRunning
                    ? `自检中… ${selfcheckJob?.completed ?? 0}/${selfcheckJob?.total ?? selfcheckProviderIDs.length * 6}`
                    : '开始自检'}
                </button>
              </div>

              <div className="detail-section">
                <div className="detail-section-title">探测参数</div>
                <div className="form-grid compact" style={{ marginBottom: 14 }}>
                  <label className="field">
                    <span>超时（秒 / 每用例）</span>
                    <input
                      type="number"
                      min={5}
                      max={600}
                      value={selfcheckTimeoutSec}
                      disabled={selfcheckRunning}
                      onChange={(event) => setSelfcheckTimeoutSec(Number(event.target.value) || 90)}
                    />
                  </label>
                  <label className="field">
                    <span>探测 Prompt</span>
                    <input
                      type="text"
                      value={selfcheckPrompt}
                      disabled={selfcheckRunning}
                      onChange={(event) => setSelfcheckPrompt(event.target.value)}
                    />
                  </label>
                  <label className="field">
                    <span>局域网根地址</span>
                    <input type="text" readOnly value={selfcheckLanRoot || localGatewayRoot(state.endpoints)} />
                  </label>
                </div>

                <div className="hint-line">
                  CLI 可用性：
                  {selfcheckTools.length === 0
                    ? '加载中…'
                    : selfcheckTools.map((tool) => (
                      <span key={tool.id} style={{ marginLeft: 10 }}>
                        {tool.label}{' '}
                        <Badge tone={tool.found ? 'green' : 'red'}>{tool.found ? '可用' : '缺失'}</Badge>
                      </span>
                    ))}
                </div>
              </div>

              <div className="detail-section">
                <div className="detail-section-title">选择 Provider</div>
                {sortedProviders.length > 0 ? (
                  <div className="providers-toolbar">
                    <label className="checkbox-field">
                      <input
                        type="checkbox"
                        checked={selfcheckProviderIDs.length > 0 && selfcheckProviderIDs.length === sortedProviders.length}
                        disabled={selfcheckRunning}
                        onChange={(event) => {
                          if (event.target.checked) selectAllSelfcheckProviders();
                          else clearSelfcheckProviders();
                        }}
                      />
                      <span>全选 Provider</span>
                    </label>
                    <span className="providers-toolbar-meta">已选 {selfcheckProviderIDs.length} / {sortedProviders.length}</span>
                    {selfcheckProviderIDs.length > 0 ? (
                      <button className="mini-btn" type="button" disabled={selfcheckRunning} onClick={clearSelfcheckProviders}>清除选择</button>
                    ) : null}
                  </div>
                ) : null}

                {sortedProviders.length === 0 ? (
                  <div className="empty-state">暂无输入 Provider，请先在「输入 Provider」页添加。</div>
                ) : (
                  <div className="selfcheck-provider-list">
                  {sortedProviders.map((provider) => {
                    const selected = selfcheckProviderIDs.includes(provider.id);
                    const modelOptions = modelsForSelfcheckProvider(provider, state.models);
                    return (
                      <div className="selfcheck-provider-item" key={provider.id}>
                        <label className="checkbox-field selfcheck-provider-check">
                          <input
                            type="checkbox"
                            checked={selected}
                            disabled={selfcheckRunning}
                            onChange={() => toggleSelfcheckProvider(provider.id)}
                          />
                          <span>{providerOptionLabel(provider)}</span>
                          <Badge tone={protocolTone(provider.protocol)}>{protocolLabel(provider.protocol)}</Badge>
                        </label>
                        <div
                          className="selfcheck-provider-model"
                          onClick={(event) => event.stopPropagation()}
                          onMouseDown={(event) => event.stopPropagation()}
                        >
                          <SearchableModelSelect
                            value={selfcheckModelForProvider(provider)}
                            models={modelOptions}
                            disabled={selfcheckRunning || !selected}
                            emptyLabel={modelOptions.length ? '选择模型' : '无可用模型'}
                            onChange={(value) => setSelfcheckModelForProvider(provider.id, value)}
                          />
                        </div>
                      </div>
                    );
                  })}
                </div>
              )}
              </div>

              {selfcheckJob ? (
                <div className="usage-table-wrap">
                  <div className="usage-section-title">
                    结果
                    {selfcheckJob.status === 'running' ? ` · 进行中 ${selfcheckJob.completed}/${selfcheckJob.total}` : null}
                    {selfcheckJob.status === 'done' ? ' · 已完成' : null}
                    {selfcheckJob.status === 'error' ? ` · 失败：${selfcheckJob.error || ''}` : null}
                  </div>
                  <div className="usage-table selfcheck-table">
                    <div className="selfcheck-header">
                      <span>时间</span>
                      <span>Provider</span>
                      <span>客户端</span>
                      <span>类型</span>
                      <span>协议</span>
                      <span>模型</span>
                      <span>成功</span>
                      <span>内容</span>
                      <span>耗时</span>
                      <span>预览 / 错误</span>
                      <span>操作</span>
                    </div>
                    {(selfcheckJob.results || []).length === 0 ? (
                      <div className="empty-state">等待用例完成…</div>
                    ) : (
                      [...selfcheckJob.results]
                        .sort((a, b) => `${a.providerName}-${a.client}-${a.kind}`.localeCompare(`${b.providerName}-${b.client}-${b.kind}`, 'zh'))
                        .map((row, index) => {
                          const caseId = row.caseId || `${row.providerId}|${row.client}|${row.kind || 'chat'}`;
                          const retrying = selfcheckRetrying.includes(caseId);
                          const passed = row.success && row.contentOK;
                          const timeTitle = [
                            row.startedAt ? `开始 ${row.startedAt}` : '',
                            row.finishedAt ? `结束 ${row.finishedAt}` : '',
                            row.apiKeyName ? `密钥 ${row.apiKeyName}` : '',
                          ].filter(Boolean).join('\n');
                          return (
                          <div className="selfcheck-row" key={`${caseId}-${index}`}>
                            <span className="selfcheck-time" title={timeTitle || undefined}>
                              {formatSelfcheckTime(row.startedAt)}
                            </span>
                            <span className="usage-key-name">{row.providerName || row.providerId}</span>
                            <span>{selfcheckClientLabel(row.client)}</span>
                            <span>{selfcheckKindLabel(row.kind)}</span>
                            <span>{protocolLabel(row.protocol as Protocol)}</span>
                            <span className="selfcheck-model" title={row.model || ''}>{row.model || '—'}</span>
                            <span><Badge tone={row.success ? 'green' : 'red'}>{row.success ? '是' : '否'}</Badge></span>
                            <span><Badge tone={row.contentOK ? 'green' : 'amber'}>{row.contentOK ? 'OK' : '失败'}</Badge></span>
                            <span>{row.latencyMs} ms</span>
                            <span className="selfcheck-preview" title={row.error || row.outputPreview || ''}>
                              {row.error || row.outputPreview || '—'}
                            </span>
                            <span className="selfcheck-actions">
                              {passed ? null : (
                                <button
                                  className="mini-btn"
                                  type="button"
                                  onClick={() => openSelfcheckCaseLogs(row)}
                                >
                                  日志
                                </button>
                              )}
                              {passed ? null : (
                                <button
                                  className="mini-btn"
                                  type="button"
                                  disabled={retrying}
                                  onClick={() => { void retrySelfcheckCase(caseId); }}
                                >
                                  {retrying ? '重试中…' : '重试'}
                                </button>
                              )}
                            </span>
                          </div>
                          );
                        })
                    )}
                  </div>
                </div>
              ) : (
                <div className="empty-state" style={{ marginTop: 16 }}>勾选 Provider 后点击「开始自检」。每个 Provider 会并行跑 6 个用例（3 个客户端 × 对话/工具调用）。</div>
              )}
            </div>
          </section>
          )}

          {activeNav === 'machine' && !isNormalUser && (
          <section className="section-full">
            <div className="panel-header" style={{ marginBottom: 0 }}>
              <div>
                <h2 className="panel-title">机器状态</h2>
                <p className="panel-desc">
                  网关所在主机的实时运行指标（仅管理员可见）。页面打开时每 5 秒拉取一次，离开本页后服务端自动停止采样。
                </p>
              </div>
              <div className="panel-header-actions">
                <span className="hint-line">
                  {hostMetrics?.collectedAt
                    ? `采样于 ${new Date(hostMetrics.collectedAt).toLocaleTimeString()}`
                    : '正在读取…'}
                </span>
              </div>
            </div>

            <div className="machine-grid">
              <MachineMetric
                label="CPU 负载"
                value={hostMetrics ? `${hostMetrics.cpuPercent.toFixed(0)}%` : '—'}
                percent={hostMetrics?.cpuPercent}
                note={hostMetrics
                  ? `load ${hostMetrics.load1.toFixed(2)} / ${hostMetrics.load5.toFixed(2)} / ${hostMetrics.load15.toFixed(2)} · ${hostMetrics.cpuCount} 核`
                  : '读取中…'}
              />
              <MachineMetric label="CPU 温度" value={hostTemp.value} note={hostTemp.note} />
              {hostMetrics?.fans && hostMetrics.fans.length > 0 ? (
                hostMetrics.fans.map((fan, index, list) => (
                  <MachineMetric
                    key={`fan-${fan.id}-${index}`}
                    label={fanSpeedLabel(fan, index, list.length)}
                    value={`${Math.round(fan.rpm).toLocaleString()} RPM`}
                    percent={fan.percent}
                    note={fanSpeedNote(fan)}
                  />
                ))
              ) : hostMetrics ? (
                <MachineMetric label="风扇转速" value="—" note="未检测到风扇（或本机无风扇）" />
              ) : null}
              <MachineMetric
                label="内存占用"
                value={hostMetrics?.memAvailable ? `${(hostMetrics.memPercent ?? 0).toFixed(0)}%` : '—'}
                percent={hostMetrics?.memAvailable ? hostMetrics.memPercent : undefined}
                note={hostMetrics?.memAvailable
                  ? `${formatBytes(hostMetrics.memUsed)} / ${formatBytes(hostMetrics.memTotal)}${hostMetrics.swapUsed ? ` · 交换 ${formatBytes(hostMetrics.swapUsed)}` : ''}`
                  : '本机未提供内存指标'}
              />
              <MachineMetric
                label="磁盘占用"
                value={hostMetrics?.diskAvailable ? `${(hostMetrics.diskPercent ?? 0).toFixed(0)}%` : '—'}
                percent={hostMetrics?.diskAvailable ? hostMetrics.diskPercent : undefined}
                note={hostMetrics?.diskAvailable
                  ? `${formatBytes(hostMetrics.diskUsed)} / ${formatBytes(hostMetrics.diskTotal)} · 根分区`
                  : '本机未提供磁盘指标'}
              />
              {hostMetrics?.diskTemps && hostMetrics.diskTemps.length > 0 ? (
                hostMetrics.diskTemps.map((disk, index, list) => (
                  <MachineMetric
                    key={disk.device || `disk-temp-${index}`}
                    label={diskTempLabel(disk, index, list.length)}
                    value={`${disk.tempC.toFixed(0)}°C`}
                    note={diskTempNote(disk)}
                  />
                ))
              ) : hostMetrics ? (
                <MachineMetric label="硬盘温度" value="—" note="未检测到（需安装 smartctl）" />
              ) : null}
              <MachineMetric
                label="网络下行"
                value={hostMetrics?.netRateReady ? formatRate(hostMetrics.netRxRate) : '—'}
                note={hostMetrics?.netAvailable
                  ? `累计接收 ${formatBytes(hostMetrics.netRxBytes)} · ${hostMetrics.netInterfaces ?? 0} 个物理网卡`
                  : '本机未提供网卡计数'}
              />
              <MachineMetric
                label="网络上行"
                value={hostMetrics?.netRateReady ? formatRate(hostMetrics.netTxRate) : '—'}
                note={hostMetrics?.netAvailable
                  ? `累计发送 ${formatBytes(hostMetrics.netTxBytes)}${hostMetrics.netRateReady ? '' : ' · 速率需两次采样'}`
                  : '本机未提供网卡计数'}
              />
            </div>

            <div className="card panel">
              <div className="panel-header">
                <div>
                  <h2 className="panel-title">主机与进程</h2>
                  <p className="panel-desc">用于排查「网关卡顿 / 掉线」时快速判断是机器压力还是进程本身的问题。</p>
                </div>
              </div>
              <div className="machine-facts">
                <div className="machine-fact"><span>主机名</span><b>{hostMetrics?.hostname || '—'}</b></div>
                <div className="machine-fact"><span>平台</span><b>{hostMetrics?.platform || '—'}</b></div>
                <div className="machine-fact"><span>CPU 核心</span><b>{hostMetrics ? `${hostMetrics.cpuCount} 核` : '—'}</b></div>
                <div className="machine-fact"><span>开机时长</span><b>{formatDuration(hostMetrics?.uptimeSeconds)}</b></div>
                <div className="machine-fact"><span>网关运行时长</span><b>{formatDuration(hostMetrics?.processUptimeSeconds)}</b></div>
                <div className="machine-fact"><span>热力等级</span><b>{hostMetrics?.thermalPressureAvailable ? thermalPressureLabel(hostMetrics.thermalPressure) : '—'}</b></div>
                <div className="machine-fact"><span>Goroutine</span><b>{hostMetrics?.goroutines ?? '—'}</b></div>
                <div className="machine-fact"><span>网关堆内存</span><b>{hostMetrics?.processHeapMiB != null ? `${hostMetrics.processHeapMiB.toFixed(1)} MiB` : '—'}</b></div>
                <div className="machine-fact"><span>交换分区</span><b>{hostMetrics?.swapAvailable ? `${formatBytes(hostMetrics.swapUsed ?? 0)} / ${formatBytes(hostMetrics.swapTotal ?? 0)}` : '未启用'}</b></div>
              </div>
            </div>
          </section>
          )}

          {activeNav === 'settings' && !isNormalUser && (
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
                <div className="hint-line">
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
                  <div className="hint-line">仅影响请求明细（流量日志正文等）。用量统计的每日 Token / 请求次数汇总会永久保留。</div>
                  <label className="toggle-row" style={{ display: 'flex', alignItems: 'center', gap: 12, cursor: 'pointer', marginTop: 8 }}>
                    <input
                      type="checkbox"
                      checked={log2xxBodies}
                      disabled={saving}
                      onChange={(event) => void updateLog2xxBodies(event.target.checked)}
                    />
                    <span>记录成功请求正文</span>
                  </label>
                  <div className="hint-line">默认关闭：成功请求只记元数据、不落正文（省磁盘）；失败请求始终保留正文。</div>
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
                  <div className="hint-line">攒够条数或到时间就落库一次（默认 500 条 / 60 秒），值越大写盘越少。修改后需重启网关生效；正常关闭会先落盘，不丢数据。</div>
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
                <div className="hint-line">
                  {authStatus?.localBypass
                    ? '当前为本机访问，可直接设置或重置管理密码。'
                    : '当前为远程访问，修改密码需验证当前密码。'}
                </div>
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
              <div className="hint-line">{state.dataPaths?.note || '更新或重装 App 不会删除此目录中的数据；数据独立于 .app 包。'}</div>
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

      {trafficLogDetail && (
        <Modal title="流量错误详情" description="包含请求体、响应体与错误摘要，便于复制后排查。" onClose={() => setTrafficLogDetail(null)}>
          <div className={`test-result-card fail`}>
            <div className="test-result-head">
              <Badge tone={statusTone(trafficLogDetail.status)}>HTTP {trafficLogDetail.status}</Badge>
              <span>{trafficLogKeyLabel(trafficLogDetail)} · {trafficLogProviderLabel(trafficLogDetail, state.providers || [])} · {trafficLogDetail.routeId} · {trafficLogDetail.model} · {trafficLogDetail.latencyMs}ms</span>
            </div>
            {trafficLogDetail.errorDescription ? <div className="hint-line error">{trafficLogDetail.errorDescription}</div> : null}
            {trafficLogDetailLoading ? <div className="hint-line">加载请求/响应体…</div> : null}
            <div className="field-label-row">
              <label>完整诊断信息</label>
              <CopyButton value={formatTrafficLogDetail(trafficLogDetail, state.providers || [])} label="复制全部" />
            </div>
            <pre className="json-preview">{formatTrafficLogDetail(trafficLogDetail, state.providers || [])}</pre>
          </div>
          <div className="actions modal-actions">
            <button className="btn" onClick={() => setTrafficLogDetail(null)}>关闭</button>
          </div>
        </Modal>
      )}

      {passwordResetUser && (
        <Modal
          title="重置用户密码"
          description={`为用户「${passwordResetUser.username}」设置新密码，至少 8 位。该用户当前会话不受影响，新密码立即生效。`}
          onClose={() => { setPasswordResetUser(null); setPasswordResetValue(''); }}
        >
          <form
            onSubmit={(event) => {
              event.preventDefault();
              void confirmPasswordReset();
            }}
          >
            <div className="form-grid modal-form">
              <div className="field field-full">
                <label>新密码（至少 8 位）</label>
                <input
                  type="text"
                  autoComplete="new-password"
                  value={passwordResetValue}
                  disabled={passwordResetBusy}
                  placeholder="至少 8 位"
                  onChange={(event) => setPasswordResetValue(event.target.value)}
                />
              </div>
            </div>
            <div className="actions modal-actions">
              <button type="button" className="btn" disabled={passwordResetBusy} onClick={() => { setPasswordResetUser(null); setPasswordResetValue(''); }}>取消</button>
              <button type="submit" className="btn primary" disabled={passwordResetBusy || passwordResetValue.trim().length < 8}>{passwordResetBusy ? '重置中…' : '重置密码'}</button>
            </div>
          </form>
        </Modal>
      )}

      {selfcheckCaseDetail && (
        <Modal
          title="自检用例错误详情"
          description="展示该失败用例的完整错误信息与输出预览，便于排查。"
          onClose={() => setSelfcheckCaseDetail(null)}
        >
          <div className="test-result-card fail">
            <div className="test-result-head">
              <Badge tone="red">{selfcheckClientLabel(selfcheckCaseDetail.client)} · {selfcheckKindLabel(selfcheckCaseDetail.kind)}</Badge>
              <span>{selfcheckCaseDetail.providerName || selfcheckCaseDetail.providerId} · {protocolLabel(selfcheckCaseDetail.protocol as Protocol)} · {selfcheckCaseDetail.model || '-'} · {selfcheckCaseDetail.latencyMs}ms</span>
            </div>
            {selfcheckCaseDetail.error ? <div className="hint-line error">{selfcheckCaseDetail.error}</div> : null}
            <div className="field-label-row">
              <label>完整诊断信息</label>
              <CopyButton value={formatSelfcheckCaseDetail(selfcheckCaseDetail)} label="复制全部" />
            </div>
            <pre className="json-preview">{formatSelfcheckCaseDetail(selfcheckCaseDetail)}</pre>
          </div>
          <div className="actions modal-actions">
            <button className="btn" onClick={() => setSelfcheckCaseDetail(null)}>关闭</button>
          </div>
        </Modal>
      )}

      {cacheTestOpen && cacheTestResult && (
        <Modal title="Cache 测试结果" description="两轮会话：第二轮包含第一轮上下文，检查 usage 中的 cache 命中字段。" blocking={false} onClose={() => setCacheTestOpen(false)}>
          <div className={`test-result-card ${cacheTestResult.success ? 'ok' : 'fail'}`}>
            <div className="test-result-head">
              <Badge tone={cacheTestResult.success ? 'green' : statusTone(cacheTestResult.status)}>{testResultBadge(cacheTestResult.success)}</Badge>
              <span>{httpStatusLabel(cacheTestResult.status)} · {cacheTestResult.latencyMs ?? '-'}ms</span>
            </div>
            <div className="hint-line">{cacheTestResult.summary || 'Cache 测试完成'}</div>
            {cacheTestResult.cacheHitTokens != null ? (
              <div className="hint-line">prompt_cache_hit_tokens / cache_read_input_tokens: <strong>{cacheTestResult.cacheHitTokens}</strong></div>
            ) : null}
            {cacheTestResult.error ? <div className="hint-line error">{cacheTestResult.error}</div> : null}
            <div className="field-label-row">
              <label>Usage 详情</label>
              <CopyButton value={formatProviderCacheTestDetail(cacheTestResult)} label="复制全部" />
            </div>
            <pre className="json-preview">{formatProviderCacheTestDetail(cacheTestResult)}</pre>
          </div>
          <div className="actions modal-actions">
            <button className="btn" onClick={() => setCacheTestOpen(false)}>关闭</button>
          </div>
        </Modal>
      )}

      {thinkingTestOpen && thinkingTestResult && (
        <Modal title="Thinking 测试结果" description="按协议注入 thinking 相关字段，并展示上游响应。" blocking={false} onClose={() => setThinkingTestOpen(false)}>
          <div className={`test-result-card ${thinkingTestResult.success ? 'ok' : 'fail'}`}>
            <div className="test-result-head">
              <Badge tone={thinkingTestResult.success ? 'green' : statusTone(thinkingTestResult.status)}>{testResultBadge(thinkingTestResult.success)}</Badge>
              <span>{httpStatusLabel(thinkingTestResult.status)} · {thinkingTestResult.latencyMs ?? '-'}ms</span>
            </div>
            {thinkingTestResult.skipped
              ? <div className="hint-line">{thinkingTestResult.summary || '该 Provider 跳过 Thinking 测试'}</div>
              : <div className="hint-line">字段：{thinkingTestResult.thinkingField || '-'} · 值：{thinkingTestResult.thinkingValue || '-'}</div>}
            {thinkingTestResult.thinkingOptions?.fields?.length ? (
              <div className="hint-line">
                可用字段：
                {thinkingTestResult.thinkingOptions.fields.map((field) => `${field.label} [${field.presets.join(', ')}]`).join(' · ')}
              </div>
            ) : null}
            {thinkingTestResult.error ? <div className="hint-line error">{thinkingTestResult.error}</div> : null}
            <div className="field-label-row">
              <label>请求与响应</label>
              <CopyButton value={formatProviderThinkingTestDetail(thinkingTestResult)} label="复制全部" />
            </div>
            <pre className="json-preview">{formatProviderThinkingTestDetail(thinkingTestResult)}</pre>
          </div>
          <div className="actions modal-actions">
            <button className="btn" onClick={() => setThinkingTestOpen(false)}>关闭</button>
          </div>
        </Modal>
      )}

      {providerModalOpen && (
        <Modal title={editingProviderID ? '编辑输入 Provider' : '创建输入 Provider'} description="API Key Source 可留空：留空时透传客户端 Authorization；也可直接填 sk-xxx，或填 env:VAR_NAME / literal:sk-xxx。Fallback Model 只在模型接口不可用时兜底。" onClose={() => { setProviderModalOpen(false); setEditingProviderID(''); resetClaudeOAuthFlowState(); resetCursorOAuthFlowState(); resetChatGPTOAuthFlowState(); resetQoderPATFlowState(); resetSelfRegistrationState(); }}>
          <div className="form-grid modal-form">
            <Field fullWidth label="Provider 名称" value={providerDraft.name} onChange={(value) => setProviderDraft((current) => ({ ...current, name: value }))} />
            {(() => {
              const connectLocked = providerDraft.authType === 'claude_oauth' || providerDraft.authType === 'cursor_oauth' || providerDraft.authType === 'chatgpt_oauth' || providerDraft.authType === 'qoder_pat';
              const connectValue = providerDraft.authType === 'claude_oauth' ? '登录 Claude 账号 (OAuth)'
                : providerDraft.authType === 'cursor_oauth' ? '登录 Cursor 账号 (OAuth)'
                : providerDraft.authType === 'chatgpt_oauth' ? '登录 ChatGPT 账号 (OAuth)'
                : providerDraft.authType === 'qoder_pat' ? '连接 Qoder 账号 (PAT)'
                : providerDraft.authType === 'self_register' ? SELF_REGISTER_CONNECT_LABEL
                : API_KEY_CONNECT_LABEL;
              // 自助注册类 Provider：协议只能由脚本通过 self-register 接口的
              // protocol 字段声明，控制台不提供选择/修改入口。
              // resolveProviderAuthType 已根据 selfRegistration 回填 self_register。
              const editingProviderRecord = editingProviderID ? state.providers.find((item) => item.id === editingProviderID) : undefined;
              const protocolManagedByAPI = providerDraft.authType === 'self_register' || !!editingProviderRecord?.selfRegistration;
              return (
                <>
                  {/* 先选连接方式，协议由连接方式决定：OAuth 三选一时协议自动锁定，
                      自助注册协议交给脚本自己声明，只有 API Key 能自由选协议。 */}
                  <SelectField
                    fullWidth
                    label="连接方式"
                    values={[API_KEY_CONNECT_LABEL, '登录 Claude 账号 (OAuth)', '登录 Cursor 账号 (OAuth)', '登录 ChatGPT 账号 (OAuth)', '连接 Qoder 账号 (PAT)', SELF_REGISTER_CONNECT_LABEL]}
                    value={connectValue}
                    onChange={(value) => setProviderDraft((current) => {
                      const authType = value === '登录 Claude 账号 (OAuth)' ? 'claude_oauth'
                        : value === '登录 Cursor 账号 (OAuth)' ? 'cursor_oauth'
                        : value === '登录 ChatGPT 账号 (OAuth)' ? 'chatgpt_oauth'
                        : value === '连接 Qoder 账号 (PAT)' ? 'qoder_pat'
                        : value === SELF_REGISTER_CONNECT_LABEL ? 'self_register'
                        : 'api_key';
                      const protocol = authType === 'claude_oauth' ? 'claude'
                        : authType === 'cursor_oauth' ? 'openai_chat'
                        : authType === 'chatgpt_oauth' ? 'openai_responses'
                        : authType === 'qoder_pat' ? 'openai_chat'
                        // 自助注册：协议不在控制台选，这里只是内部占位默认值
                        // （真实协议以脚本注册时声明的为准），固定用 openai_chat。
                        : authType === 'self_register' ? 'openai_chat'
                        : current.protocol;
                      return {
                        ...current,
                        authType,
                        protocol,
                        baseUrl: authType === 'self_register' ? selfRegisterPlaceholderBaseURL(protocol)
                          : authType === 'qoder_pat' ? QODER_DEFAULT_BASE_URL
                          : current.baseUrl,
                      };
                    })}
                  />
                  {protocolManagedByAPI ? (
                    <div className="field field-full">
                      <label>协议</label>
                      <div className="hint-line" style={{ margin: 0 }}>
                        当前：{protocolLabel(editingProviderRecord?.protocol ?? providerDraft.protocol)}
                        （由脚本通过自助注册接口的 protocol 字段声明，控制台不提供协议选择/修改，
                        避免和脚本实际实现的协议对不上；要换协议请让脚本下次注册时改传新的 protocol）
                      </div>
                    </div>
                  ) : (
                    <SelectField
                      fullWidth
                      label="协议"
                      disabled={connectLocked}
                      values={connectLocked ? [protocolLabel(providerDraft.protocol)] : fixedOutputLabels}
                      value={protocolLabel(providerDraft.protocol)}
                      onChange={(value) => setProviderDraft((current) => ({ ...current, protocol: protocolFromLabel(value) }))}
                    />
                  )}
                </>
              );
            })()}
            {providerDraft.authType === 'api_key' && (
              <div className="hint-line">
                本网关只实现了 OpenAI Chat、OpenAI Responses、Claude 三种上游协议。
                密钥所属平台不限，但该平台必须原生兼容上面选中的协议（例如 DeepSeek、
                智谱、各类中转站多为 OpenAI Chat 兼容）。若上游是自定义或私有协议，
                请改用「内网穿透自助注册」，由你的脚本自行适配后再接入。
              </div>
            )}
            {providerDraft.authType === 'cursor_oauth' && (
              <div className="hint-line">Cursor OAuth 上游固定为 OpenAI Chat（本地 bridge `/v1/chat/completions`）；客户端若要 Responses/Claude，请在路由输出协议里转换。</div>
            )}
            {providerDraft.authType === 'claude_oauth' && (
              <div className="hint-line">Claude OAuth 上游固定为 Claude 协议。</div>
            )}
            {providerDraft.authType === 'chatgpt_oauth' && (
              <div className="hint-line">ChatGPT OAuth 上游固定为 OpenAI Responses（chatgpt.com/backend-api/codex/responses）。</div>
            )}
            {providerDraft.authType === 'qoder_pat' && (
              <>
                <div className="hint-line">Qoder 直连上游原生兼容 OpenAI Chat，协议固定为 OpenAI Chat；客户端若要 Responses/Claude，请在路由输出协议里转换。请先到 qoder.com/account/integrations 生成个人访问令牌（pt- 开头），保存后在下方粘贴。</div>
                <Field fullWidth label="Base URL（默认 Qoder 直连端点，端点变更时可改）" value={providerDraft.baseUrl} onChange={(value) => setProviderDraft((current) => ({ ...current, baseUrl: value }))} />
              </>
            )}
            {providerDraft.authType === 'self_register' && (
              <div className="hint-line">
                适合"用户自己内网穿透暴露服务"的场景：无需现在填写 Base URL / API Key Source / 协议，
                保存后会自动生成一个专属令牌 + 一段配置 Prompt，交给对方脚本使用——协议不限（chat/response/
                claude 任选，脚本已有哪种代码就用哪种），脚本调用接口时把实际用的 baseUrl / 协议一并声明即可。
              </div>
            )}
            {providerDraft.authType === 'api_key' && (
              <>
                <Field fullWidth label="Base URL" value={providerDraft.baseUrl} onChange={(value) => setProviderDraft((current) => ({ ...current, baseUrl: value }))} />
                <Field fullWidth label="API Key Source（可选）" value={providerDraft.apiKeySource} onChange={(value) => setProviderDraft((current) => ({ ...current, apiKeySource: value }))} />
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
          {editingProviderID && providerDraft.authType === 'api_key' && /(?:bigmodel\.cn|z\.ai)/i.test(providerDraft.baseUrl) && (
            <div className="claude-oauth-panel">
              <div className="hint-line">智谱编程套餐额度{providerDraft.teamOrganizationId.trim() && providerDraft.teamProjectId.trim() ? '（团队版 · 需先保存组织/项目 ID）' : '（个人版）'}</div>
              <ZhipuUsagePanel providerId={editingProviderID} />
            </div>
          )}
          {editingProviderID && providerDraft.authType === 'api_key' && /deepseek\.com/i.test(providerDraft.baseUrl) && (
            <div className="claude-oauth-panel">
              <div className="hint-line">DeepSeek 账户余额（来自官方 /user/balance 接口）</div>
              <DeepSeekBalancePanel providerId={editingProviderID} />
            </div>
          )}
          {providerDraft.protocol === 'claude' && providerDraft.authType === 'claude_oauth' && (
            <div className="claude-oauth-panel">
              {!editingProviderID ? (
                <div className="hint-line">点击下方按钮保存后会自动跳转 Claude 授权页面。</div>
              ) : (() => {
                const editingProvider = state.providers.find((item) => item.id === editingProviderID);
                const connected = editingProvider?.claudeOAuth?.connected;
                if (connected) {
                  return (
                    <>
                      <div className="hint-line">已连接{editingProvider?.claudeOAuth?.accountLabel ? ` · ${editingProvider.claudeOAuth.accountLabel}` : ''}{editingProvider?.claudeOAuth?.expiresAt ? ` · 过期时间：${formatClaudeUsageResetAt(editingProvider.claudeOAuth.expiresAt)}` : ''}</div>
                      <ClaudeOAuthUsagePanel providerId={editingProviderID} connected />
                      <button className="btn danger" disabled={claudeOAuthBusy} onClick={() => void disconnectClaudeOAuth()}>{claudeOAuthBusy ? '处理中…' : '断开连接'}</button>
                    </>
                  );
                }
                return (
                  <>
                    <div className="hint-line">点击连接后跳转 Claude 授权，完成后会自动回到 Gateway（localhost:18093/callback）并完成连接。</div>
                    <button className="btn primary" disabled={claudeOAuthBusy || claudeOAuthPolling} onClick={() => void startClaudeOAuthConnect()}>{claudeOAuthBusy || claudeOAuthPolling ? '等待授权…' : '连接 Claude 账号'}</button>
                    <details className="hint-line">
                      <summary>手动粘贴 code（备用）</summary>
                      <div className="field-inline" style={{ marginTop: 8 }}>
                        <button className="mini-btn" disabled={claudeOAuthBusy || claudeOAuthPolling} onClick={() => void startClaudeOAuthManualConnect()}>打开手动授权页</button>
                        <input placeholder="粘贴 code 或完整回调 URL" value={claudeOAuthCode} onChange={(event) => setClaudeOAuthCode(event.target.value)} />
                        <button className="mini-btn" disabled={claudeOAuthBusy || !claudeOAuthCode.trim()} onClick={() => void completeClaudeOAuthConnect()}>{claudeOAuthBusy ? '连接中…' : '完成连接'}</button>
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
                <div className="hint-line">点击下方按钮保存后会自动跳转 Cursor 授权页面。需要本机安装 bun。</div>
              ) : (() => {
                const editingProvider = state.providers.find((item) => item.id === editingProviderID);
                const connected = editingProvider?.cursorOAuth?.connected;
                if (connected) {
                  return (
                    <>
                      <div className="hint-line">已连接{editingProvider?.cursorOAuth?.accountLabel ? ` · ${editingProvider.cursorOAuth.accountLabel}` : ''}{editingProvider?.cursorOAuth?.expiresAt ? ` · 过期时间：${formatClaudeUsageResetAt(editingProvider.cursorOAuth.expiresAt)}` : ''} · 模型 {editingProvider?.models?.length ?? 0} 个</div>
                      <CursorOAuthUsagePanel providerId={editingProviderID} connected />
                      <div className="actions" style={{ gap: 8 }}>
                        <button className="btn" disabled={testingProviderID === editingProviderID} onClick={() => void fetchProviderModels(editingProviderID, editingProvider?.name || '', true)}>{testingProviderID === editingProviderID ? '同步中…' : '同步模型'}</button>
                        <button className="btn danger" disabled={cursorOAuthBusy} onClick={() => void disconnectCursorOAuth()}>{cursorOAuthBusy ? '处理中…' : '断开连接'}</button>
                      </div>
                    </>
                  );
                }
                return (
                  <>
                    <div className="hint-line">点击连接后跳转 Cursor 授权页，在浏览器完成登录后网关会自动轮询并完成连接。</div>
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
                <div className="hint-line">点击下方按钮保存后会自动跳转 ChatGPT 授权页面。</div>
              ) : (() => {
                const editingProvider = state.providers.find((item) => item.id === editingProviderID);
                const connected = editingProvider?.chatgptOAuth?.connected;
                if (connected) {
                  return (
                    <>
                      <div className="hint-line">已连接{editingProvider?.chatgptOAuth?.accountLabel ? ` · ${chatgptAccountLabelFriendly(editingProvider.chatgptOAuth.accountLabel)}` : ''}{editingProvider?.chatgptOAuth?.expiresAt ? ` · 过期时间：${formatClaudeUsageResetAt(editingProvider.chatgptOAuth.expiresAt)}` : ''} · 模型 {editingProvider?.models?.length ?? 0} 个</div>
                      <ChatGPTOAuthUsagePanel providerId={editingProviderID} connected />
                      <div className="actions" style={{ gap: 8 }}>
                        <button className="btn" disabled={testingProviderID === editingProviderID} onClick={() => void fetchProviderModels(editingProviderID, editingProvider?.name || '', true)}>{testingProviderID === editingProviderID ? '同步中…' : '同步模型'}</button>
                        <button className="btn danger" disabled={chatgptOAuthBusy} onClick={() => void disconnectChatGPTOAuth()}>{chatgptOAuthBusy ? '处理中…' : '断开连接'}</button>
                      </div>
                    </>
                  );
                }
                return (
                  <>
                    <div className="hint-line">点击连接后跳转 ChatGPT 授权，完成后会回调 localhost:1455/auth/callback 并自动连接（若 1455 端口被占用，请用手动粘贴 code）。</div>
                    <button className="btn primary" disabled={chatgptOAuthBusy || chatgptOAuthPolling} onClick={() => void startChatGPTOAuthConnect()}>{chatgptOAuthBusy || chatgptOAuthPolling ? '等待授权…' : '连接 ChatGPT 账号'}</button>
                    <details className="hint-line">
                      <summary>手动粘贴 code（备用）</summary>
                      <div className="field-inline" style={{ marginTop: 8 }}>
                        <input placeholder="粘贴 code 或完整回调 URL" value={chatgptOAuthCode} onChange={(event) => setChatgptOAuthCode(event.target.value)} />
                        <button className="mini-btn" disabled={chatgptOAuthBusy || !chatgptOAuthCode.trim()} onClick={() => void completeChatGPTOAuthConnect()}>{chatgptOAuthBusy ? '连接中…' : '完成连接'}</button>
                      </div>
                    </details>
                    {chatgptOAuthError && <div className="hint-line error">{chatgptOAuthError}</div>}
                  </>
                );
              })()}
            </div>
          )}
          {providerDraft.protocol === 'openai_chat' && providerDraft.authType === 'qoder_pat' && (
            <div className="claude-oauth-panel">
              {!editingProviderID ? (
                <div className="hint-line">保存后会留在编辑态，在这里粘贴 Qoder 个人访问令牌完成连接。</div>
              ) : (() => {
                const editingProvider = state.providers.find((item) => item.id === editingProviderID);
                const connected = editingProvider?.qoderPat?.connected;
                const hasStoredToken = editingProvider?.qoderPat?.hasStoredToken;
                if (connected) {
                  return (
                    <>
                      <div className="hint-line">已连接{editingProvider?.qoderPat?.accountLabel ? ` · ${editingProvider.qoderPat.accountLabel}` : ''}{editingProvider?.qoderPat?.expiresAt ? ` · 令牌到期：${editingProvider.qoderPat.expiresAt}（到期自动续）` : ''} · 模型 {editingProvider?.models?.length ?? 0} 个</div>
                      <div className="actions" style={{ gap: 8 }}>
                        <button className="btn" disabled={testingProviderID === editingProviderID} onClick={() => void fetchProviderModels(editingProviderID, editingProvider?.name || '', true)}>{testingProviderID === editingProviderID ? '同步中…' : '同步模型'}</button>
                        <button className="btn danger" disabled={qoderPatBusy} onClick={() => void disconnectQoderPAT()}>{qoderPatBusy ? '处理中…' : '断开连接'}</button>
                      </div>
                      {qoderPatError && <div className="hint-line error">{qoderPatError}</div>}
                    </>
                  );
                }
                // 已断开，但令牌仍保存在后端：一键重新连接，不必再跑一趟
                // qoder.com 重新复制令牌（disconnect 不再清空凭据）。
                if (hasStoredToken) {
                  return (
                    <>
                      <div className="hint-line">已断开{editingProvider?.qoderPat?.accountLabel ? ` · ${editingProvider.qoderPat.accountLabel}` : ''}（个人访问令牌仍保留，未在转发中使用）</div>
                      <div className="actions" style={{ gap: 8 }}>
                        <button className="btn primary" disabled={qoderPatBusy} onClick={() => void reconnectQoderPAT()}>{qoderPatBusy ? '连接中…' : '重新连接（使用已保存的令牌）'}</button>
                      </div>
                      {qoderPatError && <div className="hint-line error">{qoderPatError}</div>}
                      <div className="hint-line" style={{ marginTop: 8 }}>要换成另一个账号？粘贴新的个人访问令牌即可覆盖：</div>
                      <div className="field-inline" style={{ marginTop: 4 }}>
                        <input type="text" placeholder="粘贴 pt- 开头的个人访问令牌" value={qoderPatInput} onChange={(event) => setQoderPatInput(event.target.value)} />
                        <button className="mini-btn" disabled={qoderPatBusy || !qoderPatInput.trim()} onClick={() => void connectQoderPAT()}>{qoderPatBusy ? '连接中…' : '完成连接'}</button>
                      </div>
                    </>
                  );
                }
                return (
                  <>
                    <div className="hint-line">在 qoder.com/account/integrations 生成个人访问令牌（pt- 开头）后粘贴到这里。令牌只用来兑换短期作业令牌，不会回传到浏览器。</div>
                    <div className="field-inline" style={{ marginTop: 8 }}>
                      <input type="text" placeholder="粘贴 pt- 开头的个人访问令牌" value={qoderPatInput} onChange={(event) => setQoderPatInput(event.target.value)} />
                      <button className="mini-btn" disabled={qoderPatBusy || !qoderPatInput.trim()} onClick={() => void connectQoderPAT()}>{qoderPatBusy ? '连接中…' : '完成连接'}</button>
                    </div>
                    {qoderPatError && <div className="hint-line error">{qoderPatError}</div>}
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
                <div className="hint-line">
                  适合"用户自己内网穿透暴露服务"的场景：生成专属令牌后，对方脚本可直接调用接口更新
                  baseUrl / apiKeySource，协议（chat/responses/claude 三选一）也由脚本在这次调用里
                  声明——这是唯一的协议来源，控制台不提供协议选择/修改入口；还能顺带声明自定义
                  authHeader，无需登录控制台账号。同一个令牌应调用协议 conformance
                  （/self-check/conformance）直到 success=true，按 cases[].hint 修本地实现。
                  令牌只能操作这一个 Provider，改不了别的。是否异常仍按真实请求失败判定，这里不做
                  心跳超时告警。
                </div>
                {selfReg ? (
                  <div className="hint-line">
                    已启用 · 令牌尾号 ****{selfReg.tokenPreview || '----'}
                    {selfReg.createdAt ? ` · 生成于 ${selfReg.createdAt}` : ''}
                    {selfReg.lastSeenAt ? ` · 上次自助注册 ${new Date(selfReg.lastSeenAt).toLocaleString()}` : ' · 尚未收到过注册请求'}
                  </div>
                ) : (
                  <div className="hint-line">尚未启用自助注册。</div>
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
            <div className="hint-line">
              用于 tuyadev / Azure deployment 等非标准上游。字段：urlTemplate、headers、bodyTemplate、modelMapping。
              占位符：{'{model}'}、{'{baseUrl}'}、{'{body}'}。运行时会把请求体里的 model 改写成映射后的上游模型名。
            </div>
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
          <div className="actions modal-actions"><button className="btn" onClick={() => { setProviderModalOpen(false); setEditingProviderID(''); resetClaudeOAuthFlowState(); resetCursorOAuthFlowState(); resetQoderPATFlowState(); resetSelfRegistrationState(); }}>取消</button><button className="btn primary" disabled={saving} onClick={() => void createProvider()}>{saving ? '保存中…' : editingProviderID ? '保存修改' : providerDraft.authType === 'claude_oauth' ? '创建并连接 Claude 账号' : providerDraft.authType === 'cursor_oauth' ? '创建并连接 Cursor 账号' : providerDraft.authType === 'chatgpt_oauth' ? '创建并连接 ChatGPT 账号' : providerDraft.authType === 'qoder_pat' ? '创建并粘贴 Qoder 令牌' : providerDraft.authType === 'self_register' ? '创建并生成注册令牌' : '创建 Provider'}</button></div>
        </Modal>
      )}

      {chatTestOpen && chatTestContext && (
        <Modal title={chatTestContext.title} description={chatTestContext.description} blocking={false} onClose={() => setChatTestOpen(false)}>
          <div className="modal-toolbar">
            <button className="btn primary" disabled={chatTestLoading || !backendConnected} onClick={() => void runChatTest()}>{chatTestLoading ? '测试中…' : '运行测试'}</button>
          </div>
          <div className="form-grid modal-form">
            <div className="field">
              <label>测试模型</label>
              <div className="field-inline">
                <select value={chatTestModel} onChange={(event) => setChatTestModel(event.target.value)}>
                  <option value="">（使用 Provider 兜底模型）</option>
                  {chatTestModels.map((model) => <option key={model.id} value={model.id}>{model.id}</option>)}
                </select>
                <button className="mini-btn" type="button" disabled={refreshingChatTestModels || !chatTestProvider} onClick={() => void refreshChatTestModels()}>
                  {refreshingChatTestModels ? '刷新中…' : '刷新模型'}
                </button>
              </div>
            </div>
            {chatTestContext.kind === 'provider' ? (
              <>
                <Field label="系统提示词" value={providerChatOptions.systemPrompt} onChange={(value) => setProviderChatOptions((current) => ({ ...current, systemPrompt: value }))} />
                <Field label="用户提示词" value={providerChatOptions.userPrompt} onChange={(value) => setProviderChatOptions((current) => ({ ...current, userPrompt: value }))} />
                <div className="hint-line">
                  {chatTestProvider?.authType === 'chatgpt_oauth' || chatTestProvider?.protocol === 'openai_responses'
                    ? '点击「运行测试」将执行主对话测试（ChatGPT OAuth / Responses 不跑 Cache / Thinking）。'
                    : '点击「运行测试」将同时执行主对话、Cache（两轮会话）与 Thinking 测试；后两者结果在弹窗中展示。'}
                </div>
                {chatTestProvider && providerThinkingPresets && chatTestProvider.authType !== 'chatgpt_oauth' && chatTestProvider.protocol !== 'openai_responses' ? (
                  <>
                    <div className="field">
                      <label>Thinking 字段（{protocolLabel(chatTestProvider.protocol)}）</label>
                      <select
                        value={providerChatOptions.thinkingField}
                        onChange={(event) => {
                          const nextField = event.target.value;
                          setProviderChatOptions((current) => ({
                            ...current,
                            thinkingField: nextField,
                            thinkingValue: defaultThinkingValueForField(chatTestProvider.protocol, nextField),
                          }));
                        }}
                      >
                        {providerThinkingPresets.fields.map((field) => (
                          <option key={field.key} value={field.key}>{field.label}</option>
                        ))}
                      </select>
                    </div>
                    <div className="field">
                      <label>Thinking 值</label>
                      <div className="field-inline">
                        <select
                          value={providerThinkingFieldPresets?.presets.includes(providerChatOptions.thinkingValue) ? providerChatOptions.thinkingValue : ''}
                          onChange={(event) => {
                            if (event.target.value) {
                              setProviderChatOptions((current) => ({ ...current, thinkingValue: event.target.value }));
                            }
                          }}
                        >
                          <option value="">（自定义）</option>
                          {(providerThinkingFieldPresets?.presets || []).map((preset) => (
                            <option key={preset} value={preset}>{preset}</option>
                          ))}
                        </select>
                        <input
                          placeholder="输入枚举值或自定义内容"
                          value={providerChatOptions.thinkingValue}
                          onChange={(event) => setProviderChatOptions((current) => ({ ...current, thinkingValue: event.target.value }))}
                        />
                      </div>
                      <div className="hint-line">可用枚举：{(providerThinkingFieldPresets?.presets || []).join(' · ') || '无'}</div>
                    </div>
                  </>
                ) : null}
              </>
            ) : (
              <Field label="测试消息" value={chatTestMessage} onChange={setChatTestMessage} />
            )}
            <div className="field">
              <div className="field-label-row">
                <label>{chatTestContext.curlLabel}</label>
                <CopyButton value={chatTestCurl} label="复制 curl" />
              </div>
              <div className="hint-line">目标 URL：{chatTestEndpointURL}</div>
              {chatTestContext.kind === 'provider' && chatTestProvider?.authType !== 'claude_oauth' && chatTestProvider?.authType !== 'cursor_oauth' && chatTestProvider?.authType !== 'chatgpt_oauth' && chatTestProvider?.authType !== 'qoder_pat' && !providerAuthPreview?.value ? (
                <div className="hint-line">未解析到 Provider 鉴权值；若使用 env: 变量，请确认网关进程环境变量已设置。</div>
              ) : null}
              <pre className="curl-preview">{chatTestCurl}</pre>
              {chatTestContext.hintLine ? <div className="hint-line">{chatTestContext.hintLine}</div> : null}
            </div>
          </div>
          <div className={`test-result-card ${chatTestResult?.success ? 'ok' : chatTestResult ? 'fail' : ''}`}>
            {!chatTestResult ? (
              <div className="empty-state">配置参数后点击顶部「运行测试」。</div>
            ) : (
              <>
                <div className="test-result-head"><Badge tone={chatTestResult.success ? 'green' : statusTone(chatTestResult.status)}>{testResultBadge(chatTestResult.success)}</Badge><span>{httpStatusLabel(chatTestResult.status)} · {chatTestResult.latencyMs ?? '-'}ms</span></div>
                <div className="field-label-row">
                  <label>{chatTestResult.success ? '响应 JSON' : '诊断信息'}</label>
                  {chatTestResponseText ? <CopyButton value={chatTestResponseText} label={chatTestResult.success ? '复制 JSON' : '复制诊断信息'} /> : null}
                </div>
                <pre className={`json-preview${chatTestResult.success ? ' ok' : ''}`}>{chatTestResponseText}</pre>
                <div className="test-result-meta">{chatTestResultMeta}</div>
              </>
            )}
          </div>
        </Modal>
      )}

      {providerModelsOpen && (
        <Modal title={`获取模型 · ${providerModelsName}`} description="从 Provider 上游 /models 接口拉取可用模型列表，并更新健康状态。" onClose={() => setProviderModelsOpen(false)}>
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
                </div>
                {providerModelsResult.error ? <div className="test-result-body">{providerModelsResult.error}</div> : null}
                {providerModelsResult.preview ? <div className="test-result-body">{providerModelsResult.preview}</div> : null}
                <div className="test-result-meta">modelsUrl={providerModelsResult.modelsUrl || '-'}</div>
              </div>
              {providerModelsResult.models.length > 0 ? (
                <div className="model-list" style={{ marginTop: 14 }}>
                  {providerModelsResult.models.map((model) => (
                    <div className="route-card" key={model.id}>
                      <div className="route-top">
                        <div className="route-name">{model.id}</div>
                        <Badge tone="slate">{model.contextLength ? `${model.contextLength} 上下文` : '模型'}</Badge>
                      </div>
                    </div>
                  ))}
                </div>
              ) : (
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
            <div className="field">
              <label>输入 Provider</label>
              <select value={routeDraft.providerId || state.providers[0]?.id || ''} onChange={(event) => setRouteDraft((current) => ({ ...current, providerId: event.target.value }))}>
                {state.providers.map((provider) => (
                  <option key={provider.id} value={provider.id}>{providerOptionLabel(provider)}</option>
                ))}
              </select>
            </div>
            <SelectField label="输出协议" values={fixedOutputLabels} value={protocolLabel(routeDraft.outputProtocol)} onChange={(value) => setRouteDraft((current) => ({ ...current, outputProtocol: protocolFromLabel(value) }))} />
          </div>
          <div className="actions modal-actions"><button className="btn" onClick={() => { setRouteModalOpen(false); setEditingRouteID(''); }}>取消</button><button className="btn primary" disabled={saving || state.providers.length === 0} onClick={() => void createRoute()}>{saving ? '保存中...' : editingRouteID ? '保存修改' : '创建路由'}</button></div>
        </Modal>
      )}

      {apiKeyModalOpen && (
        <Modal title="新建 API 密钥" description="选择输入 Provider 与输出协议；网关会自动创建转发规则并完成透传或协议转换。" onClose={() => setApiKeyModalOpen(false)}>
          <div className="form-grid modal-form">
            <Field label="名称" value={apiKeyDraft.name} onChange={(value) => setApiKeyDraft((current) => ({ ...current, name: value }))} />
            <div className="field">
              <label>输入 Provider</label>
              <select
                value={apiKeyDraft.providerId}
                onChange={(event) => setApiKeyDraft((current) => ({ ...current, providerId: event.target.value, modelOverride: '' }))}
              >
                {state.providers.map((provider) => <option key={provider.id} value={provider.id}>{providerOptionLabel(provider)}</option>)}
              </select>
            </div>
            <SelectField
              label="输出协议"
              values={fixedOutputLabels}
              value={protocolLabel(apiKeyDraft.outputProtocol)}
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
            <div className="field field-full">
              <label>思考深度（可选）</label>
              <select value={apiKeyDraft.thinkingDepthOverride} onChange={(event) => setApiKeyDraft((current) => ({ ...current, thinkingDepthOverride: event.target.value }))}>
                {thinkingDepthSelectOptions({ value: '', label: '（不覆盖）' })}
              </select>
            </div>
            <div className="field field-full">
              <label>最大输出 Token（可选）</label>
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
              <div className="hint-line">留空表示按模型自动解析；新模型若下拉显示偏小，可在此覆盖（上限 200000）。</div>
            </div>
            <div className="field field-full">
              <label>流式响应（SSE）</label>
              <label className="hint-line" style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                <input
                  type="checkbox"
                  checked={apiKeyDraft.streamEnabled}
                  onChange={(event) => setApiKeyDraft((current) => ({ ...current, streamEnabled: event.target.checked }))}
                />
                允许流式响应（关闭后该 Key 的 stream:true 请求将被拒绝）
              </label>
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

      {providerUsersModalID && !isNormalUser && (() => {
        const modalProvider = state.providers.find((item) => item.id === providerUsersModalID);
        if (!modalProvider) return null;
        const normalUsers = consoleUsers.filter((user) => user.role !== 'admin');
        const authorizedUsers = normalUsers.filter((user) => (user.allowedProviderIds || []).includes(modalProvider.id));
        const candidateUsers = normalUsers.filter((user) => !(user.allowedProviderIds || []).includes(modalProvider.id));
        return (
          <Modal
            title={`用户权限 · ${modalProvider.name}`}
            description="管理哪些普通用户可以使用该输入 Provider。管理员账号不受此限制，始终可用全部 Provider。"
            onClose={() => setProviderUsersModalID(null)}
          >
            <div className="field field-full">
              <label>已授权用户（{authorizedUsers.length}）</label>
              {authorizedUsers.length === 0 ? <div className="hint-line">暂无用户被授权使用该 Provider。</div> : null}
              <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                {authorizedUsers.map((user) => (
                  <div key={user.id} className="hint-line" style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8 }}>
                    <span>
                      <span style={{ fontWeight: 700 }}>{user.username}</span>
                      <span className={user.enabled ? 'ok' : 'err'} style={{ marginLeft: 8 }}>{user.enabled ? '启用' : '禁用'}</span>
                    </span>
                    <button
                      className="mini-btn danger"
                      type="button"
                      disabled={providerUsersBusyID === user.id}
                      onClick={() => void updateUserProviderPermission(user, modalProvider.id, false)}
                    >
                      {providerUsersBusyID === user.id ? '处理中…' : '移除权限'}
                    </button>
                  </div>
                ))}
              </div>
            </div>
            <div className="field field-full" style={{ marginTop: 12 }}>
              <label>新增授权</label>
              {normalUsers.length === 0 ? <div className="hint-line">暂无普通用户账号，可先到「用户管理」页新建。</div> : null}
              {normalUsers.length > 0 && candidateUsers.length === 0 ? <div className="hint-line">所有普通用户均已授权。</div> : null}
              <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                {candidateUsers.map((user) => (
                  <div key={user.id} className="hint-line" style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8 }}>
                    <span>
                      <span style={{ fontWeight: 700 }}>{user.username}</span>
                      <span className={user.enabled ? 'ok' : 'err'} style={{ marginLeft: 8 }}>{user.enabled ? '启用' : '禁用'}</span>
                    </span>
                    <button
                      className="mini-btn"
                      type="button"
                      disabled={providerUsersBusyID === user.id}
                      onClick={() => void updateUserProviderPermission(user, modalProvider.id, true)}
                    >
                      {providerUsersBusyID === user.id ? '处理中…' : '新增权限'}
                    </button>
                  </div>
                ))}
              </div>
            </div>
            <div className="actions modal-actions">
              <button className="btn" onClick={() => setProviderUsersModalID(null)}>关闭</button>
            </div>
          </Modal>
        );
      })()}

      {userModalOpen && (
        <Modal
          title={editingUserID ? '编辑用户' : '新建用户'}
          description={editingUserID ? '修改用户名或调整可用的输入 Provider。' : '创建普通用户账号：设置用户名与初始密码，并勾选允许使用的输入 Provider。'}
          onClose={() => setUserModalOpen(false)}
        >
          <div className="form-grid modal-form">
            <Field label="用户名" value={userFormName} onChange={setUserFormName} />
            {!editingUserID ? (
              <div className="field">
                <label>初始密码（至少 8 位）</label>
                <input
                  type="password"
                  autoComplete="new-password"
                  value={userFormPassword}
                  onChange={(event) => setUserFormPassword(event.target.value)}
                  placeholder="用户登录后可自行修改"
                />
              </div>
            ) : null}
            <div className="field field-full">
              <label>可用输入 Provider（未选中的不可用）</label>
              {state.providers.length === 0 ? <div className="hint-line">暂无输入 Provider。</div> : (
                <>
                  <MultiSelectFilter
                    options={state.providers.map((provider) => ({ id: provider.id, label: providerOptionLabel(provider) }))}
                    selected={userFormProviders}
                    onChange={setUserFormProviders}
                    allLabel="点击选择可用 Provider…"
                    fieldClassName="modal-multi-select"
                  />
                  {userFormProviders.length > 0 ? (
                    <div className="provider-chips">
                      {userFormProviders.map((id) => {
                        const provider = state.providers.find((item) => item.id === id);
                        return (
                          <span key={id} className="provider-chip">
                            {provider ? providerOptionLabel(provider) : id}
                            <button
                              type="button"
                              className="provider-chip-remove"
                              aria-label={`移除 ${provider?.name || id}`}
                              onClick={() => setUserFormProviders((current) => current.filter((item) => item !== id))}
                            >
                              ×
                            </button>
                          </span>
                        );
                      })}
                    </div>
                  ) : (
                    <div className="hint-line">尚未选择任何 Provider，该用户将没有可用的输入 Provider。</div>
                  )}
                </>
              )}
            </div>
          </div>
          <div className="actions modal-actions">
            <button className="btn" onClick={() => setUserModalOpen(false)}>取消</button>
            <button className="btn primary" disabled={userFormBusy} onClick={() => void submitUserForm()}>
              {userFormBusy ? '保存中…' : editingUserID ? '保存修改' : '创建用户'}
            </button>
          </div>
        </Modal>
      )}

      <div className="toast" id="toast">已复制到剪贴板</div>
    </>
  );
}

createRoot(document.getElementById('root')!).render(<App />);
