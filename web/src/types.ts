// @generated-from main.tsx — 由重构脚本拆分生成，请直接维护本文件。
import React from 'react';
import { navItems } from './lib';
export type BadgeTone = 'green' | 'blue' | 'cyan' | 'amber' | 'red' | 'purple' | 'slate';

export type Protocol = 'openai_chat' | 'openai_responses' | 'claude';

export type ThemeMode = 'light' | 'dark' | 'system';

export type PublicAccessMode = 'random_tunnel' | 'custom_domain';

export type AdminAuthStatus = {
  configured: boolean;
  authenticated: boolean;
  requireAuth: boolean;
  localBypass: boolean;
  role?: string;
  username?: string;
  userId?: string;
};

export type ConsoleUser = {
  id: string;
  username: string;
  role: string;
  allowedProviderIds?: string[];
  enabled: boolean;
  createdAt: string;
  lastLoginAt?: string;
  // 最近活跃：用户浏览器最近一次控制台接口请求时间（内存精确值，
  // 后端最多 5 分钟落库一次）。
  lastActiveAt?: string;
};

export type TunnelRuntime = {
  status: 'stopped' | 'starting' | 'running' | 'error';
  mode: string;
  publicUrl: string;
  uiPublicUrl?: string;
  message: string;
  startedAt?: string;
  pid?: number;
};

export type CursorBridgeRuntime = {
  status: 'stopped' | 'starting' | 'healthy' | 'unhealthy' | 'restarting' | string;
  port?: number;
  pid?: number;
  message?: string;
  startedAt?: string;
  checkedAt?: string;
};

export type PublicAccessSettings = {
  enabled: boolean;
  provider: string;
  mode: PublicAccessMode;
  exposeApi?: boolean;
  exposeUi?: boolean;
  customDomain?: string;
  uiDomain?: string;
  expose: string;
  runtimeUrl?: string;
  tunnelName?: string;
  tunnelToken?: string;
  credentialsFile?: string;
  tunnelConfigFile?: string;
  publicBaseUrl?: string;
  uiPublicBaseUrl?: string;
  status: string;
  statusMessage: string;
  tunnel?: TunnelRuntime;
};

export type HostMetrics = {
  load1: number;
  load5: number;
  load15: number;
  cpuPercent: number;
  cpuCount: number;
  tempC?: number;
  tempAvailable: boolean;
  tempSource?: string;
  thermalPressure?: string;
  thermalPressureAvailable?: boolean;
  thermalPressureSource?: string;
  memTotal?: number;
  memUsed?: number;
  memPercent?: number;
  memAvailable?: boolean;
  swapTotal?: number;
  swapUsed?: number;
  swapAvailable?: boolean;
  diskTotal?: number;
  diskUsed?: number;
  diskPercent?: number;
  diskAvailable?: boolean;
  diskTemps?: HostDiskTemp[];
  fans?: HostFanSpeed[];
  netRxBytes?: number;
  netTxBytes?: number;
  netRxRate?: number;
  netTxRate?: number;
  netRateReady?: boolean;
  netAvailable?: boolean;
  netInterfaces?: number;
  hostname?: string;
  platform?: string;
  uptimeSeconds?: number;
  processUptimeSeconds?: number;
  goroutines?: number;
  processHeapMiB?: number;
  collectedAt?: string;
};

export type HostDiskTemp = {
  device: string;
  model?: string;
  tempC: number;
  internal?: boolean;
  source?: string;
};

export type HostFanSpeed = {
  id: number;
  name?: string;
  rpm: number;
  minRpm?: number;
  maxRpm?: number;
  percent: number;
  source?: string;
};

export type ClaudeOAuthInfo = {
  connected: boolean;
  expiresAt?: string;
  accountLabel?: string;
  scope?: string;
};

export type CursorOAuthInfo = {
  connected?: boolean;
  expiresAt?: string;
  accountLabel?: string;
};

export type ChatGPTOAuthInfo = {
  connected?: boolean;
  expiresAt?: string;
  accountLabel?: string;
};

export type QoderPATInfo = {
  connected?: boolean;
  /** 用户点了「断开连接」：令牌仍保留在后端，只是暂停转发。见 disconnected 分支 UI。 */
  disconnected?: boolean;
  /** 后端仍存有个人访问令牌（即使当前 disconnected），可一键重新连接、无需再粘贴。 */
  hasStoredToken?: boolean;
  expiresAt?: string;
  accountLabel?: string;
};

export type ClaudeOAuthUsageBucket = {
  utilization: number;
  resets_at?: string;
};

export type ClaudeOAuthUsageReport = {
  available: boolean;
  error?: string;
  fetchedAt?: string;
  five_hour?: ClaudeOAuthUsageBucket;
  seven_day?: ClaudeOAuthUsageBucket;
  seven_day_opus?: ClaudeOAuthUsageBucket;
  seven_day_sonnet?: ClaudeOAuthUsageBucket;
  extra_usage?: Record<string, unknown>;
};

export type ZhipuUsageBucket = {
  utilization: number;
  resets_at?: string;
};

export type ZhipuUsageReport = {
  available: boolean;
  unsupported?: boolean;
  error?: string;
  fetchedAt?: string;
  level?: string;
  five_hour?: ZhipuUsageBucket;
  weekly?: ZhipuUsageBucket;
};

/** DeepSeek 余额：金额为十进制字符串，保持原样不转 number 以免丢精度。 */
export type DeepSeekBalanceInfo = {
  currency: string;
  total_balance: string;
  granted_balance?: string;
  topped_up_balance?: string;
};

export type DeepSeekBalanceReport = {
  available: boolean;
  unsupported?: boolean;
  error?: string;
  fetchedAt?: string;
  /** 上游 is_available：false 表示账户已无法继续调用（余额耗尽）。 */
  isAvailable: boolean;
  balance_infos?: DeepSeekBalanceInfo[];
};

export type CursorOAuthUsageBucket = {
  label: string;
  utilization: number;
  detail?: string;
  resetsAt?: string;
};

export type CursorOAuthUsageReport = {
  available: boolean;
  error?: string;
  fetchedAt?: string;
  planName?: string;
  message?: string;
  buckets?: CursorOAuthUsageBucket[];
};

export type ChatGPTOAuthUsageBucket = {
  label: string;
  utilization: number;
  detail?: string;
  resetsAt?: string;
};

export type ChatGPTResetCredit = {
  id: string;
  title?: string;
  status?: string;
  grantedAt?: string;
  expiresAt?: string;
};

export type ChatGPTResetCredits = {
  availableCount: number;
  applicable?: number;
  credits?: ChatGPTResetCredit[];
};

export type ChatGPTOAuthUsageReport = {
  available: boolean;
  error?: string;
  fetchedAt?: string;
  planName?: string;
  message?: string;
  buckets?: ChatGPTOAuthUsageBucket[];
  resetCredits?: ChatGPTResetCredits;
};

export type RequestAdapter = {
  urlTemplate?: string;
  headers?: Record<string, string>;
  bodyTemplate?: string;
  modelMapping?: Record<string, string>;
  curlExample?: string;
};

export type Provider = {
  id: string;
  name: string;
  protocol: Protocol;
  baseUrl: string;
  apiKeySource: string;
  // 实际发送凭证时使用的请求头名（Claude 默认 x-api-key，其余默认
  // Authorization）；自助注册脚本可以在协议不匹配时一并声明修正。
  authHeader?: string;
  defaultModel: string;
  defaultThinkingDepth?: string;
  models?: Model[];
  healthStatus: string;
  // nextRetryAt (RFC3339): set by the backend only while healthStatus ===
  // 'unavailable' — a live upstream request just failed against this
  // provider, and the background recovery loop will re-probe it at this time.
  nextRetryAt?: string;
  // 创建该 Provider 的控制台用户；空 = 管理员创建。普通用户对自己创建的
  // Provider 拥有编辑/克隆/删除/对话测试/获取模型权限。
  ownerUserId?: string;
  // 管理员禁用开关：禁用后普通用户不可见、不可绑定、请求会被拒绝；
  // 新建 Provider 默认启用（字段缺省 = 启用）。
  disabled?: boolean;
  // 自助注册（内网穿透场景）：owner/admin 生成一个 Provider 专属令牌后，
  // 用户自己的脚本可用该令牌调用 PATCH /__providers/{id}/self-register
  // 更新 baseUrl/apiKeySource，无需登录控制台。原始令牌只在生成那一刻返回，
  // 这里只有非敏感的展示信息。
  selfRegistration?: {
    tokenPreview?: string;
    createdAt?: string;
    lastSeenAt?: string;
  };
  authType?: 'api_key' | 'claude_oauth' | 'cursor_oauth' | 'chatgpt_oauth' | 'qoder_pat';
  // 智谱（bigmodel / z.ai）编程套餐额度查询配置：团队版需要组织 + 项目 ID，
  // 两者都填时走团队版端点（?type=2 + bigmodel-organization/-project 请求头），
  // 否则走个人版端点。均为非敏感账号标识。
  codingPlanProvider?: string;
  teamOrganizationId?: string;
  teamProjectId?: string;
  claudeOAuth?: ClaudeOAuthInfo;
  cursorOAuth?: CursorOAuthInfo;
  chatgptOAuth?: ChatGPTOAuthInfo;
  qoderPat?: QoderPATInfo;
  requestAdapter?: RequestAdapter;
};

export type ProvidersImportResult = {
  created: string[];
  updated: string[];
  skipped: string[];
  errors: string[];
};

export type SelfcheckToolInfo = {
  id: string;
  label: string;
  path: string;
  found: boolean;
  client: string;
  protocol: string;
};

export type SelfcheckCaseResult = {
  caseId?: string;
  providerId: string;
  providerName: string;
  client: string;
  kind?: string;
  protocol: string;
  model?: string;
  success: boolean;
  contentOK: boolean;
  latencyMs: number;
  outputPreview?: string;
  error?: string;
  routeId?: string;
  apiKeyName?: string;
  startedAt?: string;
  finishedAt?: string;
};

export type SelfcheckJobStatus = {
  jobId: string;
  status: 'running' | 'done' | 'error';
  prompt?: string;
  timeoutMs?: number;
  lanRoot?: string;
  startedAt?: string;
  finishedAt?: string;
  error?: string;
  results: SelfcheckCaseResult[];
  total: number;
  completed: number;
};

export type OutputEndpoint = {
  id: string;
  name: string;
  protocol: Protocol;
  basePath: string;
  listenHost: string;
  listenPort: number;
  publicAccessEnabled: boolean;
  publicUrl?: string;
  streamEnabled?: boolean;
};

export type Route = {
  id: string;
  name: string;
  outputEndpointId: string;
  providerId: string;
  outputProtocol: Protocol;
  mode: 'auto' | 'pass_through' | 'convert';
  enabled: boolean;
};

export type APIKey = {
  id: string;
  name: string;
  key: string;
  routeId: string;
  modelOverride?: string;
  modelAliases?: Record<string, string>;
  thinkingDepthOverride?: string;
  maxOutputTokens?: number;
  streamEnabled?: boolean;
  // Codex「复制配置」弹窗内“保持账号登录”开关，绑定到具体 key，跨次打开弹窗保留。
  codexKeepOfficialLogin?: boolean;
  /** 强制该 Key 命中 ChatGPT OAuth(Codex) Provider 时开启 fast 档(service_tier=priority,消耗 2~2.5x)。默认关闭。 */
  codexForceFast?: boolean;
  fallbackProviderIds?: string[];
  fallbackModelOverrides?: Record<string, string>;
  activeProviderId?: string;
  ownerUserId?: string;
  profiles?: KeyProfile[];
  activeProfileId?: string;
  enabled: boolean;
  createdAt: string;
  lastUsedAt?: string;
};

export type KeyProfile = {
  id: string;
  name: string;
  routeId: string;
  modelOverride?: string;
  modelAliases?: Record<string, string>;
  thinkingDepthOverride?: string;
  maxOutputTokens?: number;
  fallbackProviderIds?: string[];
  fallbackModelOverrides?: Record<string, string>;
  streamEnabled?: boolean;
};

export type Model = {
  id: string;
  providerId: string;
  protocol: Protocol;
  contextLength: number;
  maxOutputTokens?: number;
  inMenu: boolean;
};

export type DataPaths = {
  dataDir: string;
  configFile: string;
  sqliteDb: string;
  cloudflareConfigDir?: string;
  cloudflaredHome?: string;
  cursorTokenDir?: string;
  cursorTokenFile?: string;
  note?: string;
};

export type GatewayState = {
  providers: Provider[];
  endpoints: OutputEndpoint[];
  routes: Route[];
  models: Model[];
  apiKeys: APIKey[];
  metrics: {
    requests: number;
    successRate: number;
    inputTokens: number;
    outputTokens: number;
    averageLatencyMs: number;
  };
  publicAccess: PublicAccessSettings;
  requestLogRetentionDays?: number;
  /** When true, successful (2xx) requests also persist their bodies. */
  log2xxBodies?: boolean;
  /** Async usage-stat batching thresholds (0 = backend defaults). */
  usageBatchMaxSize?: number;
  usageBatchMaxWaitSeconds?: number;
  /** When true, HTTP binds 0.0.0.0 (LAN / tunnel). When false, loopback only. */
  webExposed?: boolean;
  dataPaths?: DataPaths;
  cursorBridge?: CursorBridgeRuntime;
};

export type LogEntry = {
  id?: number;
  time: string;
  apiKeyId?: string;
  apiKeyName?: string;
  userName?: string;
  routeId: string;
  providerId: string;
  model: string;
  action: string;
  protocolFlow: string;
  path: string;
  status: number;
  inputTokens: number;
  outputTokens: number;
  cacheTokens?: number;
  latencyMs: number;
  ttftMs?: number;
  prepMs?: number;
  preUpstreamMs?: number;
  upstreamTtfbMs?: number;
  gatewayOverheadMs?: number;
  convertOutMs?: number;
  postMs?: number;
  timingFlags?: string;
  clientHost?: string;
  clientIp?: string;
  accessSource?: 'lan' | 'public' | 'local' | string;
  errorDescription?: string;
  requestBody?: string;
  responseBody?: string;
};

export type LogPage = {
  items: LogEntry[];
  total: number;
  page: number;
};

export type AlertRecord = {
  id: number;
  time: string;
  rule: string;
  severity: string;
  apiKeyId: string;
  apiKeyName: string;
  ips: string[];
  ipCount: number;
  windowMinutes: number;
  requestCount: number;
  /** 仅并发重叠规则有值：观测到峰值并发的那一刻。 */
  concurrentAt?: string;
  status: 'unread' | 'read' | 'ignored';
  pushStatus?: string;
  pushError?: string;
};

export type AlertCounts = {
  all: number;
  unread: number;
  read: number;
  ignored: number;
};

export type AlertPage = {
  items: AlertRecord[];
  total: number;
  page: number;
  pageSize: number;
  counts: AlertCounts;
};

/** 后端已脱敏：只有 botTokenConfigured + 末 4 位,永远没有 botToken 本体。 */
export type AlertSettingsView = {
  multiIpEnabled: boolean;
  multiIpWindowMinutes: number;
  multiIpThreshold: number;
  concurrentIpEnabled: boolean;
  concurrentIpWindowMinutes: number;
  concurrentIpThreshold: number;
  cooldownMinutes: number;
  telegram: {
    enabled: boolean;
    chatId: string;
    botTokenConfigured: boolean;
    botTokenPreview?: string;
  };
};

export type APIKeyDayStats = {
  apiKeyId: string;
  apiKeyName: string;
  requestCount: number;
  inputTokens: number;
  outputTokens: number;
  cacheTokens: number;
};

export type ProviderDayStats = {
  providerId: string;
  requestCount: number;
  inputTokens: number;
  outputTokens: number;
  cacheTokens: number;
};

export type ModelDayStats = {
  model: string;
  requestCount: number;
  inputTokens: number;
  outputTokens: number;
  cacheTokens: number;
};

export type ProtocolDayStats = {
  protocol: string;
  requestCount: number;
  inputTokens: number;
  outputTokens: number;
  cacheTokens: number;
};

export type UserDayStats = {
  userId: string;
  userName: string;
  requestCount: number;
  inputTokens: number;
  outputTokens: number;
  cacheTokens: number;
};

export type DailyRequestPoint = {
  date: string;
  requestCount: number;
  inputTokens: number;
  outputTokens: number;
  cacheTokens: number;
  /** 网关当日转发的请求体字节数（下游发给我们的）。永久保留。 */
  rxBytes?: number;
  /** 网关当日返回给下游的字节数。永久保留。 */
  txBytes?: number;
  avgLatencyMs?: number;
  avgTtftMs?: number;
};

export type StatusBucketStats = {
  class: string;
  requestCount: number;
};

export type RequestStatsSnapshot = {
  today: {
    date: string;
    total: APIKeyDayStats;
    lastRequest?: LogEntry;
    byApiKey: APIKeyDayStats[];
    byProvider?: ProviderDayStats[];
    byModel?: ModelDayStats[];
    byUser?: UserDayStats[];
    byProtocol?: ProtocolDayStats[];
  };
  month: {
    period: string;
    total: APIKeyDayStats;
    byApiKey: APIKeyDayStats[];
    byProvider?: ProviderDayStats[];
    byModel?: ModelDayStats[];
    byUser?: UserDayStats[];
    byProtocol?: ProtocolDayStats[];
  };
  range?: {
    period: string;
    total: APIKeyDayStats;
    byApiKey: APIKeyDayStats[];
    byProvider?: ProviderDayStats[];
    byModel?: ModelDayStats[];
    byUser?: UserDayStats[];
    byProtocol?: ProtocolDayStats[];
  };
  from?: string;
  to?: string;
  daily?: DailyRequestPoint[];
  status?: StatusBucketStats[];
};

export type AppLogEntry = {
  time: string;
  level: 'debug' | 'info' | 'warn' | 'error';
  message: string;
  context?: string;
};

export type RouteTestResult = {
  success: boolean;
  routeId?: string;
  providerId?: string;
  model?: string;
  action?: string;
  protocolFlow?: string;
  status?: number;
  latencyMs?: number;
  preview?: string;
  error?: string;
  gatewayUrl?: string;
  upstreamUrl?: string;
  targetUrl?: string;
  requestBody?: string;
  responseBody?: string;
  diagnostics?: RouteTestDiagnostics;
};

export type RouteTestDiagnostics = {
  routeId?: string;
  routeName?: string;
  providerId?: string;
  providerProtocol?: string;
  outputProtocol?: string;
  providerBaseUrl?: string;
  upstreamUrl?: string;
  gatewayUrl?: string;
  action?: string;
  protocolFlow?: string;
  mode?: string;
  model?: string;
  status?: number;
  requestBody?: string;
  responseBody?: string;
  responseHeaders?: Record<string, string>;
  errorMessage?: string;
  transportError?: string;
  reproduceCurl?: string;
};

export type ChatTestContext = {
  kind: 'route' | 'provider';
  id: string;
  title: string;
  description: string;
  curlLabel: string;
  endpointLabel: string;
  hintLine?: string;
};

export type ProviderChatTestOptions = {
  systemPrompt: string;
  userPrompt: string;
  thinkingField: string;
  thinkingValue: string;
};

export type ProviderCacheTestResult = {
  success: boolean;
  skipped?: boolean;
  providerId?: string;
  model?: string;
  status?: number;
  latencyMs?: number;
  summary?: string;
  cacheSupported?: boolean;
  cacheHitTokens?: number;
  usageRound1?: Record<string, unknown>;
  usageRound2?: Record<string, unknown>;
  round1?: Record<string, unknown>;
  round2?: Record<string, unknown>;
  error?: string;
};

export type ProviderThinkingTestResult = {
  success: boolean;
  skipped?: boolean;
  summary?: string;
  providerId?: string;
  model?: string;
  status?: number;
  latencyMs?: number;
  thinkingField?: string;
  thinkingValue?: string;
  thinkingOptions?: {
    protocol?: string;
    defaultField?: string;
    fields?: Array<{
      key: string;
      label: string;
      presets: string[];
      custom?: boolean;
    }>;
  };
  requestBody?: string;
  responseBody?: string;
  targetUrl?: string;
  error?: string;
};

export type ProviderAuthPreview = {
  header: string;
  value: string;
};

export type ProviderTestResult = {
  success: boolean;
  providerId: string;
  modelsUrl?: string;
  status?: number;
  latencyMs?: number;
  models: Model[];
  error?: string;
  preview?: string;
};

export type NavItemID = typeof navItems[number]['id'];

export type SelfcheckPrefs = {
  providerIds?: string[];
  models?: Record<string, string>;
  timeoutSec?: number;
  prompt?: string;
};

export type TrafficRankCache = {
  providers: Record<string, number>;
  models: Record<string, number>;
};

export type ProviderConnectKind = 'api_key' | 'self_register' | 'claude_oauth' | 'cursor_oauth' | 'chatgpt_oauth' | 'qoder_pat';

export type LegacyRequestStatsSnapshot = {
  date: string;
  total: APIKeyDayStats;
  lastRequest?: LogEntry;
  byApiKey: APIKeyDayStats[];
};

export type CloudflareZoneOption = { id: string; name: string };

export type SearchableModelOption = { id: string; label: string };

export type MultiFilterOption = { id: string; label: string };

export type ModalLayer = {
  id: number;
  onClose: () => void;
};
