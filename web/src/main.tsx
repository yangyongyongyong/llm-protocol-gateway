import React, { useEffect, useMemo, useRef, useState } from 'react';
import { createRoot } from 'react-dom/client';
import './styles.css';

type BadgeTone = 'green' | 'blue' | 'cyan' | 'amber' | 'red' | 'purple' | 'slate';
type Protocol = 'openai_chat' | 'openai_responses' | 'claude';
type ThemeMode = 'light' | 'dark' | 'system';

const THEME_STORAGE_KEY = 'llm-gateway-theme';

function systemPrefersDark() {
  return typeof window !== 'undefined'
    && typeof window.matchMedia === 'function'
    && window.matchMedia('(prefers-color-scheme: dark)').matches;
}

function resolveTheme(mode: ThemeMode): 'light' | 'dark' {
  if (mode === 'system') return systemPrefersDark() ? 'dark' : 'light';
  return mode;
}

function readStoredTheme(): ThemeMode {
  try {
    const raw = localStorage.getItem(THEME_STORAGE_KEY);
    if (raw === 'light' || raw === 'dark' || raw === 'system') return raw;
  } catch {
    // ignore
  }
  return 'system';
}

function applyThemeMode(mode: ThemeMode) {
  const resolved = resolveTheme(mode);
  document.documentElement.dataset.theme = resolved;
  document.documentElement.style.colorScheme = resolved;
  return resolved;
}

type PublicAccessMode = 'random_tunnel' | 'custom_domain';

type AdminAuthStatus = {
  configured: boolean;
  authenticated: boolean;
  requireAuth: boolean;
  localBypass: boolean;
  role?: string;
  username?: string;
  userId?: string;
};

type ConsoleUser = {
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

type TunnelRuntime = {
  status: 'stopped' | 'starting' | 'running' | 'error';
  mode: string;
  publicUrl: string;
  uiPublicUrl?: string;
  message: string;
  startedAt?: string;
  pid?: number;
};

type CursorBridgeRuntime = {
  status: 'stopped' | 'starting' | 'healthy' | 'unhealthy' | 'restarting' | string;
  port?: number;
  pid?: number;
  message?: string;
  startedAt?: string;
  checkedAt?: string;
};

type PublicAccessSettings = {
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

type HostMetrics = {
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

type HostDiskTemp = {
  device: string;
  model?: string;
  tempC: number;
  internal?: boolean;
  source?: string;
};

type HostFanSpeed = {
  id: number;
  name?: string;
  rpm: number;
  minRpm?: number;
  maxRpm?: number;
  percent: number;
  source?: string;
};

type ClaudeOAuthInfo = {
  connected: boolean;
  expiresAt?: string;
  accountLabel?: string;
  scope?: string;
};

type CursorOAuthInfo = {
  connected?: boolean;
  expiresAt?: string;
  accountLabel?: string;
};

type ChatGPTOAuthInfo = {
  connected?: boolean;
  expiresAt?: string;
  accountLabel?: string;
};

type QoderPATInfo = {
  connected?: boolean;
  expiresAt?: string;
  accountLabel?: string;
};

type ClaudeOAuthUsageBucket = {
  utilization: number;
  resets_at?: string;
};

type ClaudeOAuthUsageReport = {
  available: boolean;
  error?: string;
  fetchedAt?: string;
  five_hour?: ClaudeOAuthUsageBucket;
  seven_day?: ClaudeOAuthUsageBucket;
  seven_day_opus?: ClaudeOAuthUsageBucket;
  seven_day_sonnet?: ClaudeOAuthUsageBucket;
  extra_usage?: Record<string, unknown>;
};

type ZhipuUsageBucket = {
  utilization: number;
  resets_at?: string;
};

type ZhipuUsageReport = {
  available: boolean;
  unsupported?: boolean;
  error?: string;
  fetchedAt?: string;
  level?: string;
  five_hour?: ZhipuUsageBucket;
  weekly?: ZhipuUsageBucket;
};

/** DeepSeek 余额：金额为十进制字符串，保持原样不转 number 以免丢精度。 */
type DeepSeekBalanceInfo = {
  currency: string;
  total_balance: string;
  granted_balance?: string;
  topped_up_balance?: string;
};

type DeepSeekBalanceReport = {
  available: boolean;
  unsupported?: boolean;
  error?: string;
  fetchedAt?: string;
  /** 上游 is_available：false 表示账户已无法继续调用（余额耗尽）。 */
  isAvailable: boolean;
  balance_infos?: DeepSeekBalanceInfo[];
};

type CursorOAuthUsageBucket = {
  label: string;
  utilization: number;
  detail?: string;
  resetsAt?: string;
};

type CursorOAuthUsageReport = {
  available: boolean;
  error?: string;
  fetchedAt?: string;
  planName?: string;
  message?: string;
  buckets?: CursorOAuthUsageBucket[];
};

type ChatGPTOAuthUsageBucket = {
  label: string;
  utilization: number;
  detail?: string;
  resetsAt?: string;
};

type ChatGPTOAuthUsageReport = {
  available: boolean;
  error?: string;
  fetchedAt?: string;
  planName?: string;
  message?: string;
  buckets?: ChatGPTOAuthUsageBucket[];
};

type RequestAdapter = {
  urlTemplate?: string;
  headers?: Record<string, string>;
  bodyTemplate?: string;
  modelMapping?: Record<string, string>;
  curlExample?: string;
};

const REQUEST_ADAPTER_PRESETS: Array<{ id: string; label: string; hint: string; json: string }> = [
  {
    id: 'tuya-azure-deployment',
    label: '涂鸦 / Azure Deployment',
    hint: 'BaseURL 含 {model} 部署路径；Claude 客户端模型名映射到上游部署名。',
    json: `{
  "modelMapping": {
    "claude-sonnet-5": "gpt-5.5",
    "claude-opus-4-8": "gpt-5.5",
    "sonnet": "gpt-5.5",
    "opus": "gpt-5.5",
    "haiku": "gpt-5.5"
  }
}`,
  },
  {
    id: 'url-template',
    label: '自定义 URL 模板',
    hint: '用 urlTemplate 覆盖最终上游地址，占位符 {baseUrl}/{model}。',
    json: `{
  "urlTemplate": "{baseUrl}/deployments/{model}/chat/completions?api-version=2024-02-01",
  "modelMapping": {
    "claude-sonnet-5": "gpt-5.5"
  },
  "headers": {}
}`,
  },
  {
    id: 'body-wrap',
    label: 'Body 包装模板',
    hint: '用 {body} 嵌入网关转换后的 JSON，再包一层自定义字段。',
    json: `{
  "bodyTemplate": "{\\"scene\\":\\"gateway\\",\\"payload\\":{body}}",
  "modelMapping": {}
}`,
  },
];

function compactRequestAdapterJSON(raw: string): string {
  const trimmed = raw.trim();
  if (!trimmed) return '';
  try {
    const parsed = JSON.parse(trimmed) as RequestAdapter;
    const compact: RequestAdapter = {};
    if (parsed.urlTemplate?.trim()) compact.urlTemplate = parsed.urlTemplate.trim();
    if (parsed.bodyTemplate?.trim()) compact.bodyTemplate = parsed.bodyTemplate.trim();
    if (parsed.headers && Object.keys(parsed.headers).length > 0) compact.headers = parsed.headers;
    if (parsed.modelMapping && Object.keys(parsed.modelMapping).length > 0) compact.modelMapping = parsed.modelMapping;
    return Object.keys(compact).length ? JSON.stringify(compact, null, 2) : '';
  } catch {
    return trimmed;
  }
}

function previewRequestAdapterCurl(baseUrl: string, defaultModel: string, adapterJSON: string): string {
  const trimmed = adapterJSON.trim();
  if (!trimmed) return '';
  let adapter: RequestAdapter;
  try {
    adapter = JSON.parse(trimmed) as RequestAdapter;
  } catch {
    return 'JSON 无效，保存前请修正语法。';
  }
  const clientModel = Object.keys(adapter.modelMapping || {})[0] || defaultModel || 'gpt-5.5';
  const mappedModel = adapter.modelMapping?.[clientModel] || clientModel;
  let url = (adapter.urlTemplate || '').trim();
  if (!url) {
    url = baseUrl.includes('{model}') ? baseUrl : `${baseUrl.replace(/\/$/, '')}/chat/completions`;
  }
  url = url
    .replaceAll('{model}', mappedModel)
    .replaceAll('{baseUrl}', baseUrl.replace(/\/$/, ''))
    .replaceAll('{baseURL}', baseUrl.replace(/\/$/, ''));
  const sampleBody = JSON.stringify({
    model: mappedModel,
    messages: [{ role: 'user', content: 'hi' }],
    max_completion_tokens: 64,
  });
  let body = sampleBody;
  if (adapter.bodyTemplate?.trim()) {
    body = adapter.bodyTemplate
      .replaceAll('{model}', mappedModel)
      .replaceAll('{baseUrl}', baseUrl.replace(/\/$/, ''))
      .replaceAll('{baseURL}', baseUrl.replace(/\/$/, ''))
      .replaceAll('{body}', sampleBody);
  }
  const headerLines = Object.entries(adapter.headers || {}).map(([key, value]) => {
    const rendered = String(value)
      .replaceAll('{model}', mappedModel)
      .replaceAll('{baseUrl}', baseUrl.replace(/\/$/, ''))
      .replaceAll('{baseURL}', baseUrl.replace(/\/$/, ''));
    return ` \\\n  -H '${key}: ${rendered.replace(/'/g, `'\\''`)}'`;
  }).join('');
  return `curl -sS -X POST '${url.replace(/'/g, `'\\''`)}' \\\n  -H 'Content-Type: application/json'${headerLines} \\\n  -d '${body.replace(/'/g, `'\\''`)}'`;
}

type Provider = {
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

type ProvidersImportResult = {
  created: string[];
  updated: string[];
  skipped: string[];
  errors: string[];
};

type SelfcheckToolInfo = {
  id: string;
  label: string;
  path: string;
  found: boolean;
  client: string;
  protocol: string;
};

type SelfcheckCaseResult = {
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

type SelfcheckJobStatus = {
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

type OutputEndpoint = {
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

type Route = {
  id: string;
  name: string;
  outputEndpointId: string;
  providerId: string;
  outputProtocol: Protocol;
  mode: 'auto' | 'pass_through' | 'convert';
  enabled: boolean;
};

type APIKey = {
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

type KeyProfile = {
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

type Model = {
  id: string;
  providerId: string;
  protocol: Protocol;
  contextLength: number;
  maxOutputTokens?: number;
  inMenu: boolean;
};

type DataPaths = {
  dataDir: string;
  configFile: string;
  sqliteDb: string;
  cloudflareConfigDir?: string;
  cloudflaredHome?: string;
  cursorTokenDir?: string;
  cursorTokenFile?: string;
  note?: string;
};

type GatewayState = {
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
  /** When true, HTTP binds 0.0.0.0 (LAN / tunnel). When false, loopback only. */
  webExposed?: boolean;
  dataPaths?: DataPaths;
  cursorBridge?: CursorBridgeRuntime;
};

type LogEntry = {
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

type LogPage = {
  items: LogEntry[];
  total: number;
  page: number;
};

type AlertRecord = {
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

type AlertCounts = {
  all: number;
  unread: number;
  read: number;
  ignored: number;
};

type AlertPage = {
  items: AlertRecord[];
  total: number;
  page: number;
  pageSize: number;
  counts: AlertCounts;
};

/** 后端已脱敏：只有 botTokenConfigured + 末 4 位,永远没有 botToken 本体。 */
type AlertSettingsView = {
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

type APIKeyDayStats = {
  apiKeyId: string;
  apiKeyName: string;
  requestCount: number;
  inputTokens: number;
  outputTokens: number;
  cacheTokens: number;
};

type ProviderDayStats = {
  providerId: string;
  requestCount: number;
  inputTokens: number;
  outputTokens: number;
  cacheTokens: number;
};

type ModelDayStats = {
  model: string;
  requestCount: number;
  inputTokens: number;
  outputTokens: number;
  cacheTokens: number;
};

type ProtocolDayStats = {
  protocol: string;
  requestCount: number;
  inputTokens: number;
  outputTokens: number;
  cacheTokens: number;
};

type UserDayStats = {
  userId: string;
  userName: string;
  requestCount: number;
  inputTokens: number;
  outputTokens: number;
  cacheTokens: number;
};

type DailyRequestPoint = {
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

type StatusBucketStats = {
  class: string;
  requestCount: number;
};

type RequestStatsSnapshot = {
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

type AppLogEntry = {
  time: string;
  level: 'debug' | 'info' | 'warn' | 'error';
  message: string;
  context?: string;
};

type RouteTestResult = {
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

type RouteTestDiagnostics = {
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

type ChatTestContext = {
  kind: 'route' | 'provider';
  id: string;
  title: string;
  description: string;
  curlLabel: string;
  endpointLabel: string;
  hintLine?: string;
};

type ProviderChatTestOptions = {
  systemPrompt: string;
  userPrompt: string;
  thinkingField: string;
  thinkingValue: string;
};

type ProviderCacheTestResult = {
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

type ProviderThinkingTestResult = {
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

type ProviderAuthPreview = {
  header: string;
  value: string;
};

const PROVIDER_CACHE_ROUND2_USER = '继续';

/** Codex 本地 model catalog 支持的档位：Codex 不认 max，会被折成 xhigh。 */
const THINKING_DEPTH_OPTIONS = ['low', 'medium', 'high', 'xhigh'] as const;

/**
 * 控制台可选的完整推理强度阶梯。网关侧 normalizeReasoningEffort 一直支持 max，
 * Qoder（docs.qoder.com/cli/model 的 /effort：low/medium/high/xhigh/max）与
 * Anthropic Opus 4.6+ 也都认 max，所以下拉要给到 max。
 * 不认 max 的上游会在各自的 map 函数里自行降级到 high。
 */
const SELECTABLE_THINKING_DEPTHS = ['low', 'medium', 'high', 'xhigh', 'max'] as const;

const CODEX_MODEL_CATALOG_REL = '.codex/lpg-model-catalog.json';
const CODEX_MODEL_CATALOG_DISPLAY = `~/${CODEX_MODEL_CATALOG_REL}`;

const defaultProviderChatTestOptions: ProviderChatTestOptions = {
  systemPrompt: 'x=10',
  userPrompt: 'x+5等于几',
  thinkingField: 'reasoning_effort',
  thinkingValue: 'medium',
};

function thinkingDepthSelectOptions(includeEmpty: { value: string; label: string }) {
  return (
    <>
      <option value={includeEmpty.value}>{includeEmpty.label}</option>
      {SELECTABLE_THINKING_DEPTHS.map((depth) => (
        <option key={depth} value={depth}>{depth}</option>
      ))}
    </>
  );
}

function thinkingPresetsForProtocol(protocol: Protocol) {
  if (protocol === 'claude') {
    return {
      defaultField: 'thinking.type',
      fields: [
        { key: 'thinking.type', label: 'thinking.type', presets: ['enabled', 'disabled'] },
        { key: 'thinking.budget_tokens', label: 'thinking.budget_tokens', presets: ['1024', '4096', '10000'], custom: true },
        { key: 'thinking', label: 'thinking (JSON)', presets: ['{"type":"enabled","budget_tokens":4096}'], custom: true },
      ],
    };
  }
  return {
    defaultField: 'reasoning_effort',
    fields: [
      { key: 'reasoning_effort', label: 'reasoning_effort', presets: [...SELECTABLE_THINKING_DEPTHS], custom: true },
      { key: 'thinking.type', label: 'thinking.type', presets: ['enabled', 'disabled'], custom: true },
    ],
  };
}

function defaultThinkingValueForField(protocol: Protocol, field: string) {
  const presets = thinkingPresetsForProtocol(protocol);
  const match = presets.fields.find((item) => item.key === field) || presets.fields[0];
  if (field === 'thinking.type') return 'enabled';
  if (field === 'reasoning_effort') return 'medium';
  return match?.presets[0] || '';
}

type ProviderTestResult = {
  success: boolean;
  providerId: string;
  modelsUrl?: string;
  status?: number;
  latencyMs?: number;
  models: Model[];
  error?: string;
  preview?: string;
};

const API_BASE = '';

// Qoder 直连端点。后端 normalizeProvider 只在 baseUrl 留空时兜这个默认值
// （保留手动覆盖入口），所以切到 Qoder 时前端要主动把占位 URL 换掉，
// 否则会残留创建表单的 example.com 预填值。
const QODER_DEFAULT_BASE_URL = 'https://api2-v2.qoder.sh/model/v1';
const navItems = [
  { id: 'input-providers', label: '输入 Provider' },
  { id: 'models-menu', label: '模型列表' },
  { id: 'api-keys', label: 'API 密钥' },
  { id: 'output-providers', label: '接入地址' },
  { id: 'usage-stats', label: '用量统计' },
  { id: 'public-access', label: '公网访问' },
  { id: 'traffic-tokens', label: 'API 日志' },
  { id: 'alerts', label: '告警' },
  { id: 'users', label: '用户管理' },
  { id: 'self-check', label: '自检' },
  { id: 'machine', label: '机器状态' },
  { id: 'settings', label: '设置' },
] as const;
const navIcons = ['◉', '☰', '🔑', '⌘', '▣', '↗', '≡', '🔔', '👥', '✓', '🖥', '⚙'];
type NavItemID = typeof navItems[number]['id'];

// 核心配置项：新用户只需依次配好这两个即可使用，侧边栏置顶并加底色区分。
const coreNavIDs: NavItemID[] = ['input-providers', 'api-keys'];

// 普通用户仅可访问的页面（其余仅管理员可见）
const userAllowedNavIDs: NavItemID[] = ['input-providers', 'models-menu', 'api-keys', 'traffic-tokens', 'usage-stats'];

function navPathForID(id: NavItemID) {
  return id === 'input-providers' ? '/' : `/${id}`;
}

function navIDFromPath(pathname: string): NavItemID {
  const normalized = pathname.replace(/\/+$/, '') || '/';
  if (normalized === '/login') return 'settings';
  if (normalized === '/' || normalized === '/overview' || normalized === '/input-providers') return 'input-providers';
  const match = navItems.find((item) => normalized === `/${item.id}`);
  return match?.id ?? 'input-providers';
}
const fixedOutputLabels = ['OpenAI Chat', 'OpenAI Responses', 'Claude'];
const logLevelValues = ['debug', 'info', 'warn', 'error'];
const defaultPublicAccess: PublicAccessSettings = {
  enabled: false,
  provider: 'cloudflare',
  mode: 'random_tunnel',
  exposeApi: true,
  exposeUi: true,
  expose: 'all',
  status: 'disabled',
  statusMessage: '公网访问未开启。可一键开启 Cloudflare 快速隧道，或绑定已购买的 Cloudflare 域名。',
};

const fallbackState: GatewayState = {
  providers: [],
  endpoints: [],
  routes: [],
  models: [],
  apiKeys: [],
  metrics: { requests: 0, successRate: 0, inputTokens: 0, outputTokens: 0, averageLatencyMs: 0 },
  publicAccess: defaultPublicAccess,
  webExposed: false,
};

/** Coerce null/missing array fields so role=user redacted state cannot crash the UI. */
function normalizeGatewayState(data: Partial<GatewayState> | null | undefined, current?: GatewayState): GatewayState {
  const base = current ?? fallbackState;
  return {
    ...base,
    ...data,
    providers: data?.providers ?? [],
    endpoints: data?.endpoints ?? [],
    routes: data?.routes ?? [],
    models: data?.models ?? [],
    apiKeys: data?.apiKeys ?? [],
    metrics: data?.metrics ?? base.metrics,
    publicAccess: {
      ...defaultPublicAccess,
      ...data?.publicAccess,
      tunnel: data?.publicAccess?.tunnel ?? current?.publicAccess?.tunnel,
    },
  };
}

const UI_CACHE_PREFIX = 'llm-gateway-ui-cache:v1:';
const UI_CACHE_TTL_MS = 24 * 60 * 60 * 1000;
const LOGS_PAGE_SIZE = 10;
// API 密钥列表每页最多显示 30 个，密钥多时翻页浏览。
const API_KEYS_PAGE_SIZE = 30;
// 用户管理表格列宽（表头与数据行共用，保证对齐）。
const USERS_TABLE_GRID = 'minmax(0,0.7fr) 48px minmax(0,1.2fr) minmax(0,0.8fr) minmax(0,0.9fr) minmax(0,0.9fr) 236px';
// 与后端 internal/gateway/user_isolation.go 里的 logOwnerFilterAdmin 保持一致。
const LOG_OWNER_FILTER_ADMIN = '_admin';

function uiCacheScope(auth?: Pick<AdminAuthStatus, 'userId' | 'role' | 'username'> | null) {
  return auth?.userId || auth?.username || auth?.role || 'anon';
}

function readUICache<T>(scope: string, kind: string): T | null {
  try {
    const raw = localStorage.getItem(`${UI_CACHE_PREFIX}${scope}:${kind}`);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as { at?: number; data?: T };
    if (!parsed || typeof parsed.at !== 'number' || parsed.data == null) return null;
    if (Date.now() - parsed.at > UI_CACHE_TTL_MS) return null;
    return parsed.data;
  } catch {
    return null;
  }
}

function writeUICache(scope: string, kind: string, data: unknown) {
  try {
    localStorage.setItem(`${UI_CACHE_PREFIX}${scope}:${kind}`, JSON.stringify({ at: Date.now(), data }));
  } catch {
    // quota / private mode — ignore
  }
}

function clearUICache(scope: string, kind: string) {
  try {
    localStorage.removeItem(`${UI_CACHE_PREFIX}${scope}:${kind}`);
  } catch {
    // ignore
  }
}

/** 自检页偏好：无 TTL，按登录身份永久保存在本机。 */
const SELFCHECK_PREFS_PREFIX = 'llm-gateway-selfcheck-prefs:v1:';

type SelfcheckPrefs = {
  providerIds?: string[];
  models?: Record<string, string>;
  timeoutSec?: number;
  prompt?: string;
};

function readSelfcheckPrefs(scope: string): SelfcheckPrefs | null {
  try {
    const raw = localStorage.getItem(`${SELFCHECK_PREFS_PREFIX}${scope}`);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as SelfcheckPrefs;
    if (!parsed || typeof parsed !== 'object') return null;
    return parsed;
  } catch {
    return null;
  }
}

function writeSelfcheckPrefs(scope: string, prefs: SelfcheckPrefs) {
  try {
    localStorage.setItem(`${SELFCHECK_PREFS_PREFIX}${scope}`, JSON.stringify(prefs));
  } catch {
    // quota / private mode — ignore
  }
}

function defaultSelfcheckModelForProvider(provider: Provider, models: Model[]): string {
  const listed = models.filter((model) => model.providerId === provider.id);
  return listed[0]?.id?.trim()
    || provider.defaultModel?.trim()
    || provider.models?.find((model) => model.id?.trim())?.id?.trim()
    || '';
}

function modelsForSelfcheckProvider(provider: Provider, allModels: Model[]): Model[] {
  const listed = allModels.filter((model) => model.providerId === provider.id);
  if (listed.length > 0) return listed;
  const fallbackID = provider.defaultModel?.trim();
  if (!fallbackID) return [];
  return [{
    id: fallbackID,
    providerId: provider.id,
    protocol: provider.protocol,
    contextLength: 0,
    inMenu: true,
  }];
}

type TrafficRankCache = {
  providers: Record<string, number>;
  models: Record<string, number>;
};

function trafficRanksFromMap(map: Map<string, number>): Record<string, number> {
  const out: Record<string, number> = {};
  for (const [key, value] of map) {
    if (value > 0) out[key] = value;
  }
  return out;
}

function trafficRanksEqual(a: Record<string, number>, b: Record<string, number>) {
  const aKeys = Object.keys(a);
  const bKeys = Object.keys(b);
  if (aKeys.length !== bKeys.length) return false;
  for (const key of aKeys) {
    if ((a[key] || 0) !== (b[key] || 0)) return false;
  }
  return true;
}

function mapFromTrafficRanks(ranks: Record<string, number> | undefined) {
  const map = new Map<string, number>();
  if (!ranks) return map;
  for (const [key, value] of Object.entries(ranks)) {
    if (value > 0) map.set(key, value);
  }
  return map;
}

/** 上次已登录会话：用于刷新页面时跳过「正在检查登录」闪屏。 */
function loadBootSession(): { auth: AdminAuthStatus | null; state: GatewayState | null } {
  const auth = readUICache<AdminAuthStatus>('session', 'auth');
  if (!auth) return { auth: null, state: null };
  const ok = Boolean(auth.authenticated || auth.localBypass || !auth.requireAuth);
  if (!ok) return { auth: null, state: null };
  const cachedState = readUICache<GatewayState>(uiCacheScope(auth), 'state');
  return {
    auth,
    state: cachedState ? normalizeGatewayState(cachedState) : null,
  };
}

function maskApiKey(key: string) {
  if (key.length <= 12) return key;
  return `${key.slice(0, 8)}…${key.slice(-4)}`;
}

function formatJsonDisplay(raw?: string) {
  const text = raw?.trim();
  if (!text) return '';
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return text;
  }
}

function trafficLogKeyLabel(log: LogEntry) {
  return log.apiKeyName?.trim() || '未绑定 Key';
}

/**
 * 悬浮提示：先给完整的当前密钥名（列宽不够时会被省略号截断），再补一行 ID。
 * apiKeyId 是创建时按当时名字生成的固定 slug，改名后就和名字不一致了，
 * 必须标上「ID:」前缀，否则看起来像是另一把不存在的密钥。
 */
function trafficLogKeyTitle(log: LogEntry) {
  const name = trafficLogKeyLabel(log);
  const id = log.apiKeyId?.trim();
  return id && id !== name ? `${name}\nID: ${id}` : name;
}

/**
 * 「来源」悬浮提示：IP 列已从表格移除（密钥名更需要宽度），把客户端 IP 和
 * Host 收进来源列的提示里，详情弹窗中也仍然可见，信息不丢。
 */
function trafficLogSourceTitle(log: LogEntry) {
  const parts = [
    log.clientIp?.trim() ? `IP: ${log.clientIp.trim()}` : '',
    log.clientHost?.trim() ? `Host: ${log.clientHost.trim()}` : '',
  ].filter(Boolean);
  return parts.length > 0 ? parts.join('\n') : undefined;
}

function trafficLogProviderLabel(log: LogEntry, providers: Provider[]) {
  return providerUsageLabel(log.providerId || '_unknown', providers);
}

function isTrafficLogError(log: LogEntry) {
  return log.status >= 400 || Boolean(log.errorDescription?.trim()) || Boolean(log.responseBody?.trim());
}

function formatTrafficLogDetail(log: LogEntry, providers: Provider[] = []) {
  const lines = [
    '=== Traffic Request Log Detail ===',
    `time: ${new Date(log.time).toLocaleString()}`,
    `status: HTTP ${log.status}`,
    `apiKey: ${trafficLogKeyLabel(log)}${log.apiKeyId ? ` (ID: ${log.apiKeyId})` : ''}`,
    `user: ${log.userName || '-'}`,
    `route: ${log.routeId}`,
    `provider: ${trafficLogProviderLabel(log, providers)}${log.providerId ? ` (${log.providerId})` : ''}`,
    `model: ${log.model}`,
    `action: ${log.action}`,
    `protocolFlow: ${log.protocolFlow}`,
    `path: ${log.path}`,
    `accessSource: ${log.accessSource || '-'}`,
    `clientIp: ${log.clientIp || '-'}${log.clientHost ? ` (host: ${log.clientHost})` : ''}`,
    `latency: ${log.latencyMs}ms`,
    `ttft: ${log.ttftMs != null ? `${log.ttftMs}ms` : '-'}`,
    `timing: prep=${log.prepMs ?? 0}ms preUpstream=${log.preUpstreamMs ?? 0}ms upstreamTtfb=${log.upstreamTtfbMs ?? 0}ms overhead=${log.gatewayOverheadMs ?? 0}ms convertOut=${log.convertOutMs ?? 0}ms post=${log.postMs ?? 0}ms`,
    `timingFlags: ${log.timingFlags || '-'}`,
    `tokens: in=${log.inputTokens} out=${log.outputTokens}${log.cacheTokens ? ` cache=${log.cacheTokens}` : ''}`,
  ];
  if (log.errorDescription) lines.push(`error: ${log.errorDescription}`);
  if (log.requestBody) lines.push('', '--- Request Body ---', formatJsonDisplay(log.requestBody));
  if (log.responseBody) lines.push('', '--- Response Body ---', formatJsonDisplay(log.responseBody));
  return lines.join('\n');
}

function formatSelfcheckCaseDetail(row: SelfcheckCaseResult) {
  const lines = [
    '=== 自检用例详情 ===',
    `Provider: ${row.providerName || row.providerId}`,
    `客户端: ${row.client}`,
    `类型: ${row.kind || 'chat'}`,
    `协议: ${row.protocol}`,
    `模型: ${row.model || '-'}`,
    `密钥: ${row.apiKeyName || '-'}`,
    `路由: ${row.routeId || '-'}`,
    `请求成功: ${row.success ? '是' : '否'}`,
    `内容校验: ${row.contentOK ? 'OK' : '失败'}`,
    `耗时: ${row.latencyMs} ms`,
    `开始: ${row.startedAt || '-'}`,
    `结束: ${row.finishedAt || '-'}`,
  ];
  if (row.error) lines.push('', '--- 错误详情 ---', row.error);
  if (row.outputPreview) lines.push('', '--- 输出预览 ---', row.outputPreview);
  return lines.join('\n');
}

function formatRouteTestDiagnostics(result: RouteTestResult) {
  const diagnostics = result.diagnostics;
  const lines: string[] = ['=== Route Test Diagnostics ==='];
  if (diagnostics) {
    lines.push(
      `route: ${diagnostics.routeId || result.routeId || '-'} (${diagnostics.routeName || '-'})`,
      `provider: ${diagnostics.providerId || result.providerId || '-'} (${diagnostics.providerProtocol || '-'})`,
      `output: ${diagnostics.outputProtocol || '-'}`,
      `flow: ${diagnostics.protocolFlow || result.protocolFlow || '-'} · action=${diagnostics.action || result.action || '-'} · mode=${diagnostics.mode || '-'}`,
      `model: ${diagnostics.model || result.model || '-'}`,
      `status: HTTP ${diagnostics.status ?? result.status ?? '-'}`,
      `gateway: ${diagnostics.gatewayUrl || result.gatewayUrl || '-'}`,
      `upstream: ${diagnostics.upstreamUrl || result.upstreamUrl || '-'}`,
      `providerBaseUrl: ${diagnostics.providerBaseUrl || '-'}`,
    );
    if (diagnostics.transportError) lines.push(`transportError: ${diagnostics.transportError}`);
    if (diagnostics.errorMessage || result.error) lines.push(`errorMessage: ${diagnostics.errorMessage || result.error}`);
    if (diagnostics.responseHeaders && Object.keys(diagnostics.responseHeaders).length > 0) {
      lines.push('', '--- Response Headers ---', formatJsonDisplay(JSON.stringify(diagnostics.responseHeaders)));
    }
    if (diagnostics.requestBody || result.requestBody) {
      lines.push('', '--- Request Body ---', formatJsonDisplay(diagnostics.requestBody || result.requestBody));
    }
    const responseBody = diagnostics.responseBody || result.responseBody || result.preview;
    if (responseBody) {
      lines.push('', '--- Response Body ---', formatJsonDisplay(responseBody));
    }
    if (diagnostics.reproduceCurl) {
      lines.push('', '--- Reproduce curl ---', diagnostics.reproduceCurl);
    }
    return lines.join('\n');
  }
  if (result.error) lines.push(`error: ${result.error}`);
  if (result.requestBody) lines.push('', '--- Request Body ---', formatJsonDisplay(result.requestBody));
  const responseBody = result.responseBody || result.preview;
  if (responseBody) lines.push('', '--- Response Body ---', formatJsonDisplay(responseBody));
  if (result.gatewayUrl) lines.push(`gateway: ${result.gatewayUrl}`);
  if (result.upstreamUrl) lines.push(`upstream: ${result.upstreamUrl}`);
  return lines.join('\n');
}

function formatChatTestResponse(result: RouteTestResult) {
  if (!result.success) return formatRouteTestDiagnostics(result);
  if (result.error) return result.error;
  const raw = result.responseBody || result.preview || '';
  if (!raw) return '无响应预览';
  return formatJsonDisplay(raw);
}

function formatProviderCacheTestDetail(result: ProviderCacheTestResult) {
  if (result.skipped) {
    return result.summary || '该 Provider 跳过 Cache 测试';
  }
  const lines = [
    result.summary || 'Cache 测试完成',
    result.cacheHitTokens != null ? `cacheHitTokens: ${result.cacheHitTokens}` : '',
    result.usageRound1 ? `Round 1 usage:\n${formatJsonDisplay(JSON.stringify(result.usageRound1))}` : '',
    result.usageRound2 ? `Round 2 usage:\n${formatJsonDisplay(JSON.stringify(result.usageRound2))}` : '',
  ].filter(Boolean);
  return lines.join('\n\n');
}

function formatProviderThinkingTestDetail(result: ProviderThinkingTestResult) {
  if (result.skipped) {
    return result.summary || '该 Provider 跳过 Thinking 测试';
  }
  const lines = [
    `field=${result.thinkingField || '-'} · value=${result.thinkingValue || '-'}`,
    result.targetUrl ? `upstream: ${result.targetUrl}` : '',
    result.error ? `error: ${result.error}` : '',
    result.requestBody ? `--- Request Body ---\n${formatJsonDisplay(result.requestBody)}` : '',
    result.responseBody ? `--- Response Body ---\n${formatJsonDisplay(result.responseBody)}` : '',
  ].filter(Boolean);
  return lines.join('\n\n');
}

function protocolLabel(protocol: Protocol) {
  switch (protocol) {
    case 'openai_chat': return 'OpenAI Chat';
    case 'openai_responses': return 'OpenAI Responses';
    case 'claude': return 'Claude';
  }
}

function protocolFromLabel(label: string): Protocol {
  const normalized = label.trim().toLowerCase();
  if (normalized === 'openai responses' || label === 'OpenAI 响应') return 'openai_responses';
  if (normalized === 'claude' || label === 'Claude 消息') return 'claude';
  if (normalized === 'openai chat' || label === 'OpenAI 对话') return 'openai_chat';
  return 'openai_chat';
}

function actionLabel(action: string) {
  if (action === 'not_configured') return '未配置';
  if (action === 'pass_through' || action.includes('pass')) return '透传';
  if (action === 'convert' || action.includes('convert')) return '转换';
  return action;
}

function routeActionLabel(action: string) {
  return action === 'pass_through' ? '透传' : '转换';
}

function testResultBadge(success?: boolean) {
  return success ? '成功' : '失败 / 跳过';
}

// 卡片上只展示密钥掩码；完整值仅在编辑弹窗可见（编辑权限已限创建人/管理员）。
function maskApiKeySource(source?: string): string {
  const value = (source || '').trim();
  if (!value) return '透传客户端 Authorization';
  if (value.startsWith('env:')) return value;
  const raw = value.startsWith('literal:') ? value.slice('literal:'.length) : value;
  if (raw.length <= 8) return '密钥 ••••';
  return `${raw.slice(0, 4)}••••${raw.slice(-4)}`;
}

// selfRegistrationEndpointSpec 描述每种协议下，用户自建服务需要实现的接口
// 形状：路径、上游协议名称，以及一句话的关键格式提示（供生成 Prompt 使用）。
// 「连接方式」下拉框里代表自助注册模式的选项文案；三种协议共用同一个选项
// （不像 OAuth 选项那样绑定单一协议）。
const SELF_REGISTER_CONNECT_LABEL = '内网穿透自助注册（Bearer 令牌）';

// selfRegisterPlaceholderBaseURL 是创建时的占位 baseUrl：真实地址由用户自己的
// 脚本在生成令牌后通过 self-register 接口写入，这里只是先满足"创建 Provider
// 必须填 baseUrl"的校验，明显带有 pending 字样，不会被当成真实可用的上游。
function selfRegisterPlaceholderBaseURL(protocol: Protocol): string {
  return `https://pending-self-registration.example${selfRegistrationEndpointSpec(protocol).path}`;
}

function selfRegistrationEndpointSpec(protocol: Protocol): { path: string; label: string; hint: string } {
  switch (protocol) {
    case 'openai_chat':
      return {
        path: '/v1/chat/completions',
        label: 'OpenAI Chat Completions',
        hint: '请求体含 model/messages（role+content 数组）；stream=true 时按 SSE 逐行返回 `data: {...}\n\n`，每个 chunk 是 delta 增量，以 `data: [DONE]` 结束；非流式返回 choices[0].message。',
      };
    case 'openai_responses':
      return {
        path: '/v1/responses',
        label: 'OpenAI Responses',
        hint: '请求体含 model/input（items 数组，每项有 role+content 或 type=function_call 等）；stream=true 时按 SSE 发送 response.created / response.output_text.delta / response.completed 等事件；非流式返回 { output: [...], status: "completed" }。',
      };
    case 'claude':
      return {
        path: '/v1/messages',
        label: 'Anthropic Messages',
        hint: '请求体含 model/system/messages（content 为 block 数组，如 {type:"text",text:"..."}）、max_tokens 必填；stream=true 时按 SSE 发送 message_start/content_block_delta/message_stop 等事件；非流式返回 { content: [...], stop_reason: "end_turn" }。',
      };
  }
}

// buildSelfRegistrationPrompt 生成一段可直接丢给本地编码大模型（Claude Code /
// Codex / Cursor 等）的说明文本：描述要实现的协议接口形状、鉴权方式、如何用
// 内网穿透暴露成公网地址，以及每次地址变化后如何调用我们的自助注册接口。
// rawToken 只在生成令牌那一刻才有值；没有则用占位符提示先生成。
function buildSelfRegistrationPrompt(provider: Provider, rawToken: string, originBase: string): string {
  const registerUrl = `${originBase}/__providers/${provider.id}/self-register`;
  const tokenLine = rawToken || '<在控制台点击"生成注册令牌"后粘贴到这里>';
  // 三种协议逐条列出，供本地已有代码对号入座，不预设/不强制其中任何一种。
  const protocolMenu = (['openai_chat', 'openai_responses', 'claude'] as Protocol[])
    .map((protocol) => {
      const spec = selfRegistrationEndpointSpec(protocol);
      const header = protocol === 'claude' ? 'x-api-key: <SHARED_SECRET>' : 'Authorization: Bearer <SHARED_SECRET>';
      return `  \u00b7 protocol="${protocol}"：POST ${spec.path}（${spec.label} 协议格式），鉴权头默认 ${header}\n    ${spec.hint}`;
    })
    .join('\n');
  return `帮我实现一个本地服务 + 内网穿透 + 自动注册的完整方案，要求如下：

【1. 本地服务 —— 三选一，选你已有代码 / 更顺手的那种，不强制】
网关支持下面三种协议格式，你的本地服务只需要实现其中任意一种（哪怕你手头已经有
现成的某种格式的代码，直接拿来用即可，不用为了迁就我而改写）：
${protocolMenu}

- 无论选哪种，鉴权都要求请求带上面对应的头（SHARED_SECRET 由我自己设定，作为
  "上游密钥"，和下面第 3 步注册接口用的令牌是两回事，不要混淆；如果你想用非标准
  头名，第 3 步注册时可以用 authHeader 字段显式覆盖，不必强行匹配上面的默认值）
- 内部对接我自己现有的模型/服务即可，只要出参符合你选定的那个协议格式

【2. 内网穿透】
- 用 cloudflared 快速隧道（免费、无需域名、无需登录）：
  cloudflared tunnel --url http://localhost:8787
- 该命令会在标准输出打印一个形如 https://xxxx.trycloudflare.com 的公网地址；
  注意：这个地址每次重启隧道都会变，脚本要能捕获它

【3. 自动注册到网关平台 —— 必须声明你选的协议】
- 每次拿到新的公网地址后（包括隧道意外重启），立刻调用（**protocol 字段必填，
  取值必须跟你在第 1 步实际实现的协议一致**：openai_chat / openai_responses / claude
  三选一；网关这边不会预先假定协议，完全以这次调用声明的为准）：
  curl -X PATCH "${registerUrl}" \\
    -H "Authorization: Bearer ${tokenLine}" \\
    -H "Content-Type: application/json" \\
    -d '{"baseUrl": "https://xxxx.trycloudflare.com/<你第1步实现的路径，比如 /v1/chat/completions>", "apiKeySource": "literal:<第1步里设定的 SHARED_SECRET>", "protocol": "<openai_chat|openai_responses|claude 三选一>"}'
  （把 xxxx.trycloudflare.com 换成实际隧道打印出来的地址；这里 Bearer 后面跟的是
  第 3 步专用的"注册令牌"，跟第 1 步本地服务自己的 SHARED_SECRET 是两个不同的东西）
- 这个注册接口只鉴权 Bearer 令牌本身，和登录账号无关；只允许改这一个 Provider 的
  baseUrl / apiKeySource / protocol / authHeader 这几个字段，改不了别的东西；网关控制台
  这边也不提供协议选择/修改的入口——协议只能通过这个接口声明，这是唯一的协议来源
- 不传 authHeader 时，网关按第 1 步表格里的惯例自动推导鉴权头（claude -> x-api-key，
  其余 -> Authorization）；本地服务用了非标准头名时可以显式传 authHeader 覆盖
- baseUrl 不能是内网地址（127.0.0.1 / 192.168.x.x / 10.x.x.x 等会被拒绝），必须是
  隧道给的公网地址
- 以后如果想换成另一种协议实现，不用回控制台改，下次注册时把 protocol 换成新值即可

【4. 协议 conformance（注册成功后立刻跑，直到必过项全绿）】
网关提供协议套件诊断（用第 3 步同一个注册令牌鉴权）：

    curl -X POST "${originBase}/__providers/${provider.id}/self-check/conformance" \\
      -H "Authorization: Bearer ${tokenLine}"

按你声明的 protocol 自动跑：models 鉴权、非流式形状、流式 SSE、usage 字段、
cache 命中（后两项为建议项）。返回 JSON：
  - success / passedRequired = true 表示必过项全部通过（可以认为协议合格）
  - passedAll = true 表示建议项也全过
  - cases[] 每条含 id/severity/passed/detail/hint

脚本应：注册成功后调用 conformance；若 success=false，根据 cases[].hint 修本地服务，
再重新调用，直到 success=true。不要无限重试烧 token——改完再测。
建议项（usage_fields / cache_hit）失败不阻断接入，但控制台缓存命中可能一直为 0。

（可选兼容）仍可用旧的 health×3 与 chat 自检，但新脚本优先只用 conformance。

【5. 整合成一个常驻脚本】
- 启动本地服务 -> 启动 cloudflared 隧道 -> 解析出公网 URL -> 调用注册接口（带上 protocol）
  -> 注册成功后跑 conformance 直到 success=true -> 持续监控隧道进程，一旦断线/重启就重新走一遍
  解析+注册+conformance
- 可以用 Python/Bash 都行，帮我把这五步串成一个可以后台常驻运行的脚本`;
}

function healthStatusLabel(status: string) {
  switch (status) {
    case 'healthy': return '正常';
    case 'failed': return '失败';
    case 'degraded': return '降级';
    case 'standby': return '待机';
    case 'unavailable': return '异常';
    case 'unchecked': return '未检测';
    default: return status || '未检测';
  }
}

// retrySecondsLabel renders a live "N秒后重试" / "N分N秒后重试" countdown from
// an RFC3339 nextRetryAt timestamp, matching the backend's periodic
// background recovery probe (see gateway.StartProviderFailoverRecovery).
function retrySecondsLabel(nextRetryAt: string | undefined, nowMs: number): string | null {
  if (!nextRetryAt) return null;
  const target = new Date(nextRetryAt).getTime();
  if (Number.isNaN(target)) return null;
  const remainingMs = target - nowMs;
  if (remainingMs <= 0) return '即将重试';
  const totalSeconds = Math.ceil(remainingMs / 1000);
  if (totalSeconds < 60) return `${totalSeconds}秒后重试`;
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  return seconds > 0 ? `${minutes}分${seconds}秒后重试` : `${minutes}分钟后重试`;
}

// useNowTick re-renders every second so retry countdowns stay live between
// the 5s /__state polls, without needing per-provider timers.
function useNowTick(enabled: boolean, intervalMs = 1000): number {
  const [now, setNow] = React.useState(() => Date.now());
  React.useEffect(() => {
    if (!enabled) return undefined;
    const timer = window.setInterval(() => setNow(Date.now()), intervalMs);
    return () => window.clearInterval(timer);
  }, [enabled, intervalMs]);
  return now;
}

function cursorBridgeStatusLabel(status?: string) {
  switch (status) {
    case 'healthy': return 'bridge 正常';
    case 'starting': return 'bridge 启动中';
    case 'restarting': return 'bridge 重启中';
    case 'unhealthy': return 'bridge 异常';
    case 'stopped': return 'bridge 未启动';
    default: return status ? `bridge ${status}` : 'bridge 未启动';
  }
}

function cursorBridgeTone(status?: string): BadgeTone {
  if (status === 'healthy') return 'green';
  if (status === 'starting' || status === 'restarting') return 'amber';
  if (status === 'unhealthy') return 'red';
  return 'slate';
}

function tunnelStatusLabel(status?: string) {
  switch (status) {
    case 'running': return '运行中';
    case 'starting': return '启动中';
    case 'stopped': return '已停止';
    case 'error': return '异常';
    default: return status || '已停止';
  }
}

function publicAccessMetricValue(enabled: boolean, mode: string) {
  if (!enabled) return '仅局域网';
  if (mode === 'custom_domain') return '自有域名';
  return '随机隧道';
}

function publicAccessStatusLabel(status: string) {
  switch (status) {
    case 'runtime_url_recorded': return '已记录公网地址';
    case 'configured_pending_tunnel': return '已配置，待启动隧道';
    case 'waiting_for_tunnel': return '等待隧道';
    case 'unsupported': return '不支持';
    case 'error': return '连接失败';
    default: return status;
  }
}

function httpStatusLabel(status?: number) {
  return status ? `HTTP ${status}` : '无 HTTP 状态';
}

function flowBadgeTone(item: string): BadgeTone {
  if (item === 'Claude' || item === 'Claude 消息') return 'purple';
  if (item.includes('Responses') || item.includes('响应')) return 'amber';
  if (item === '转换' || item === 'Convert') return 'cyan';
  return 'blue';
}

function providerOptionLabel(provider: Provider) {
  return `${provider.name} (${protocolLabel(provider.protocol)})`;
}

type ProviderConnectKind = 'api_key' | 'self_register' | 'claude_oauth' | 'cursor_oauth' | 'chatgpt_oauth' | 'qoder_pat';

function providerConnectKind(provider: Provider): ProviderConnectKind {
  if (provider.authType === 'claude_oauth') return 'claude_oauth';
  if (provider.authType === 'cursor_oauth') return 'cursor_oauth';
  if (provider.authType === 'chatgpt_oauth') return 'chatgpt_oauth';
  if (provider.authType === 'qoder_pat') return 'qoder_pat';
  if (provider.selfRegistration) return 'self_register';
  return 'api_key';
}

function providerConnectLabel(kind: ProviderConnectKind): string {
  switch (kind) {
    case 'claude_oauth': return '登录 Claude 账号 (OAuth)';
    case 'cursor_oauth': return '登录 Cursor 账号 (OAuth)';
    case 'chatgpt_oauth': return '登录 ChatGPT 账号 (OAuth)';
    case 'qoder_pat': return '连接 Qoder 账号 (PAT)';
    case 'self_register': return '内网穿透自助注册（Bearer 令牌）';
    default: return 'API Key';
  }
}

const PROVIDER_CONNECT_FILTERS: Array<{ id: '' | ProviderConnectKind; label: string }> = [
  { id: '', label: '全部连接方式' },
  { id: 'api_key', label: 'API Key' },
  { id: 'self_register', label: '内网穿透自助注册' },
  { id: 'claude_oauth', label: 'Claude OAuth' },
  { id: 'cursor_oauth', label: 'Cursor OAuth' },
  { id: 'chatgpt_oauth', label: 'ChatGPT OAuth' },
  { id: 'qoder_pat', label: 'Qoder PAT' },
];

function buildApiKeyPatchBody(key: APIKey, patch: Partial<APIKey> = {}) {
  return {
    name: (patch.name ?? key.name).trim(),
    routeId: patch.routeId ?? key.routeId,
    modelOverride: patch.modelOverride ?? key.modelOverride ?? '',
    modelAliases: patch.modelAliases ?? key.modelAliases ?? {},
    thinkingDepthOverride: patch.thinkingDepthOverride ?? key.thinkingDepthOverride ?? '',
    maxOutputTokens: patch.maxOutputTokens ?? key.maxOutputTokens ?? 0,
    streamEnabled: patch.streamEnabled ?? key.streamEnabled ?? true,
    codexKeepOfficialLogin: patch.codexKeepOfficialLogin ?? key.codexKeepOfficialLogin ?? false,
    enabled: patch.enabled ?? key.enabled,
    fallbackProviderIds: patch.fallbackProviderIds ?? key.fallbackProviderIds ?? [],
    fallbackModelOverrides: patch.fallbackModelOverrides ?? key.fallbackModelOverrides ?? {},
  };
}

function getApiKeyBinding(key: APIKey, routes: Route[], providers: Provider[]) {
  const route = routes.find((item) => item.id === key.routeId);
  const binding = apiKeyBindingFromRoute(route);
  const routeProvider = binding.providerId ? providers.find((item) => item.id === binding.providerId) : undefined;
  const bindingAction = route && routeProvider
    ? (route.outputProtocol === routeProvider.protocol ? '透传' : '转换')
    : '-';
  return { route, binding, routeProvider, bindingAction };
}

function findRouteForBinding(routes: Route[], providerId: string, outputProtocol: Protocol) {
  return routes.find((route) => route.providerId === providerId && route.outputProtocol === outputProtocol);
}

function apiKeyBindingFromRoute(route: Route | undefined) {
  return {
    providerId: route?.providerId || '',
    outputProtocol: (route?.outputProtocol || 'openai_chat') as Protocol,
  };
}

/** 首选绑定或备选列表中引用了该 Provider 的 API Key 都算引用。 */
function apiKeyReferencesProvider(key: APIKey, routes: Route[], providerId: string) {
  if (!providerId) return false;
  const route = routes.find((item) => item.id === key.routeId);
  if (route?.providerId === providerId) return true;
  if ((key.fallbackProviderIds || []).includes(providerId)) return true;
  if (key.activeProviderId === providerId) return true;
  return false;
}

function formatTokenSummary(stats: Pick<APIKeyDayStats, 'inputTokens' | 'outputTokens' | 'cacheTokens'>) {
  const { totalInput, cacheHits } = normalizePromptTokenStats(stats.inputTokens, stats.cacheTokens || 0);
  return `in ${formatTokenCount(totalInput)} · out ${formatTokenCount(stats.outputTokens)} · cache ${formatTokenCount(cacheHits)}`;
}

type LegacyRequestStatsSnapshot = {
  date: string;
  total: APIKeyDayStats;
  lastRequest?: LogEntry;
  byApiKey: APIKeyDayStats[];
};

function normalizeRequestStats(raw: RequestStatsSnapshot | LegacyRequestStatsSnapshot | null | undefined): RequestStatsSnapshot | null {
  if (!raw) return null;
  if ('today' in raw && raw.today) return raw as RequestStatsSnapshot;
  if ('date' in raw && raw.total) {
    const legacy = raw as LegacyRequestStatsSnapshot;
    return {
      today: {
        date: legacy.date,
        total: legacy.total,
        lastRequest: legacy.lastRequest,
        byApiKey: legacy.byApiKey || [],
        byProvider: [],
        byModel: [],
      },
      month: {
        period: legacy.date.slice(0, 7),
        total: legacy.total,
        byApiKey: legacy.byApiKey || [],
        byProvider: [],
        byModel: [],
      },
    };
  }
  return null;
}

function usageStatsForKey(snapshot: RequestStatsSnapshot | null, apiKeyId: string) {
  const today = snapshot?.today?.byApiKey.find((item) => item.apiKeyId === apiKeyId);
  const month = snapshot?.month?.byApiKey.find((item) => item.apiKeyId === apiKeyId);
  return { today, month };
}

function usageStatsForProvider(snapshot: RequestStatsSnapshot | null, providerId: string) {
  const today = snapshot?.today?.byProvider?.find((item) => item.providerId === providerId);
  const month = snapshot?.month?.byProvider?.find((item) => item.providerId === providerId);
  return { today, month };
}

function providerUsageLabel(providerId: string, providers: Provider[]) {
  if (providerId === '_unknown') return '未知 Provider';
  const provider = providers.find((item) => item.id === providerId);
  return provider ? provider.name : providerId;
}

function buildRouteTestPayload(model: string, message: string) {
  const resolvedModel = model.trim() || 'request-model-not-set';
  const resolvedMessage = message.trim() || 'ping from Protocol Gateway route test';
  return {
    model: resolvedModel,
    stream: false,
    messages: [{ role: 'user', content: resolvedMessage }],
  };
}

function routeGatewayTestURL(route: Route, endpoints: OutputEndpoint[]) {
  const endpoint = endpoints.find((item) => item.protocol === route.outputProtocol) || endpoints.find((item) => item.protocol === 'openai_chat');
  if (!endpoint) return `${API_BASE}/v1/chat/completions`;
  const localRoot = `http://${endpoint.listenHost}:${endpoint.listenPort}`;
  return routeGatewayURL(route, endpoints, localRoot);
}

function routeGatewayURL(route: Route, endpoints: OutputEndpoint[], base: string) {
  const endpoint = endpoints.find((item) => item.protocol === route.outputProtocol);
  if (!endpoint || !base) return '';
  const root = `${base.replace(/\/$/, '')}${endpoint.basePath}`;
  if (route.outputProtocol === 'openai_chat') return `${root}/chat/completions`;
  if (route.outputProtocol === 'openai_responses') return `${root}/responses`;
  // Claude BasePath is /anthropic; clients append /v1/messages themselves.
  if (route.outputProtocol === 'claude') return `${root}/v1/messages`;
  return root;
}

function apiKeyClientBaseURL(route: Route, endpoints: OutputEndpoint[], publicBase: string) {
  const base = publicBase || localGatewayRoot(endpoints);
  const endpoint = endpoints.find((item) => item.protocol === route.outputProtocol);
  if (!endpoint) return base.replace(/\/$/, '');
  return `${base.replace(/\/$/, '')}${endpoint.basePath}`;
}

function apiKeyClientAuthHint(route: Route) {
  if (route.outputProtocol === 'claude') return 'Claude Code：Base URL 填到 /anthropic（不要带 /v1）+ x-api-key';
  if (route.outputProtocol === 'openai_responses') return 'OpenAI Responses 客户端：Base URL + Bearer Key';
  return 'OpenAI 客户端：Base URL（如 /v1）+ Bearer Key';
}

function sanitizeClientConfigID(name: string) {
  const id = name.trim().toLowerCase().replace(/[^a-z0-9_-]+/g, '-').replace(/^-+|-+$/g, '');
  return id || 'gateway';
}

function apiKeyGatewayRoot(endpoints: OutputEndpoint[], publicBase: string) {
  return (publicBase || localGatewayRoot(endpoints)).replace(/\/$/, '');
}

function buildApiKeyOpenCodeConfig(key: APIKey, route: Route | undefined, endpoints: OutputEndpoint[], publicBase: string, provider?: Provider) {
  const providerID = sanitizeClientConfigID(key.name);
  const model = resolveApiKeyModel(key, provider);
  const root = apiKeyGatewayRoot(endpoints, publicBase);
  const protocol = route?.outputProtocol;
  // OpenCode 通过不同 AI SDK 包对接三种输出协议：
  // - Claude Messages → @ai-sdk/anthropic（baseURL 需含 /v1，SDK 再拼 /messages）
  // - OpenAI Responses → @ai-sdk/openai
  // - OpenAI Chat → @ai-sdk/openai-compatible
  let npm = '@ai-sdk/openai-compatible';
  let baseURL = `${root}/v1`;
  if (protocol === 'claude') {
    npm = '@ai-sdk/anthropic';
    baseURL = `${root}/anthropic/v1`;
  } else if (protocol === 'openai_responses') {
    npm = '@ai-sdk/openai';
    baseURL = `${root}/openai/v1`;
  }
  const aliases = key.modelAliases || {};
  const models: Record<string, { name: string; limit: { context: number; output: number } }> = {
    [model]: { name: model, limit: openCodeModelLimit(model, provider, aliases) },
  };
  for (const alias of Object.keys(aliases)) {
    const trimmed = alias.trim();
    if (!trimmed) continue;
    models[trimmed] = { name: trimmed, limit: openCodeModelLimit(trimmed, provider, aliases) };
  }
  const config = {
    $schema: 'https://opencode.ai/config.json',
    model: `${providerID}/${model}`,
    provider: {
      [providerID]: {
        npm,
        name: key.name || providerID,
        options: {
          baseURL,
          apiKey: key.key,
        },
        models,
      },
    },
  };
  return `${JSON.stringify(config, null, 2)}\n`;
}

function openCodeModelLimit(modelID: string, provider?: Provider, aliases?: Record<string, string>) {
  const target = ((aliases?.[modelID] || aliases?.[modelID.trim()] || modelID) || '').trim() || modelID;
  const listed = provider?.models?.find((item) => item.id === target || item.id === modelID);
  // 目录未显式给出上下文长度时，默认按 1M 处理（与 Claude [1m] 保持一致，
  // 避免 Codex/OpenCode 回落到 128k 触发过早压缩）。目录若有真实值则以真实值为准。
  const context = listed?.contextLength && listed.contextLength > 0 ? listed.contextLength : 1_000_000;
  const output = context >= 1_000_000
    ? 128000
    : context >= 200000
      ? (/-haiku|haiku/i.test(target) ? 64000 : 128000)
      : 65536;
  return { context, output };
}

function resolveCodexReasoningEffort(key: APIKey) {
  const raw = (key.thinkingDepthOverride || '').trim().toLowerCase();
  if (!raw) return 'medium';
  if (raw === 'max') return 'xhigh';
  if ((THINKING_DEPTH_OPTIONS as readonly string[]).includes(raw)) return raw;
  return 'medium';
}

/** Codex 本地 model catalog：消掉 “Model metadata for `xxx` not found” */
function buildApiKeyCodexModelCatalogJSON(key: APIKey, provider?: Provider) {
  const primary = resolveApiKeyModel(key, provider);
  const aliases = key.modelAliases || {};
  const slugs = new Set<string>();
  if (primary && primary !== 'your-model') slugs.add(primary);
  for (const alias of Object.keys(aliases)) {
    const trimmed = alias.trim();
    if (trimmed) slugs.add(trimmed);
  }
  for (const target of Object.values(aliases)) {
    const trimmed = (target || '').trim();
    if (trimmed) slugs.add(trimmed);
  }
  if (slugs.size === 0) slugs.add(primary || 'your-model');

  const reasoningLevels = THINKING_DEPTH_OPTIONS.map((effort) => ({
    effort,
    description:
      effort === 'low' ? 'Fast responses with lighter reasoning'
        : effort === 'medium' ? 'Balances speed and reasoning depth for everyday tasks'
          : effort === 'high' ? 'Greater reasoning depth for complex problems'
            : 'Extra high reasoning depth for complex problems',
  }));
  const defaultEffort = resolveCodexReasoningEffort(key);

  const models = [...slugs].map((slug, index) => {
    const limit = openCodeModelLimit(slug, provider, aliases);
    return {
      slug,
      display_name: slug,
      description: `${slug} (via LLM Protocol Gateway)`,
      default_reasoning_level: defaultEffort,
      supported_reasoning_levels: reasoningLevels,
      context_window: limit.context,
      max_context_window: limit.context,
      shell_type: 'shell_command',
      visibility: 'list',
      supported_in_api: true,
      priority: index,
      availability_nux: null,
      upgrade: null,
      base_instructions: 'You are Codex, a coding agent.',
      supports_reasoning_summaries: true,
      support_verbosity: false,
      default_verbosity: null,
      apply_patch_tool_type: null,
      truncation_policy: { mode: 'tokens', limit: 10000 },
      supports_parallel_tool_calls: true,
      experimental_supported_tools: [],
      input_modalities: ['text', 'image'],
    };
  });
  return `${JSON.stringify({ models }, null, 2)}\n`;
}

function buildApiKeyCodexConfig(key: APIKey, route: Route | undefined, endpoints: OutputEndpoint[], publicBase: string, provider?: Provider, keepOfficialLogin?: boolean) {
  const providerID = sanitizeClientConfigID(key.name);
  const model = resolveApiKeyModel(key, provider);
  const effort = resolveCodexReasoningEffort(key);
  const baseURL = `${apiKeyGatewayRoot(endpoints, publicBase)}/openai/v1`;
  const warning = !route || route.outputProtocol !== 'openai_responses'
    ? '# 注意：当前密钥输出协议不是 OpenAI Responses，请先改为「OpenAI Responses」\n'
    : '';
  // 保持账号登录：让该 provider 表在 Codex 眼里“长得像官方 openai” provider
  // （name / supports_websockets 对齐官方形状），使 Codex 官方特性门控（插件市场、
  // 移动端远程控制等）继续命中；不改 base_url / experimental_bearer_token，
  // 实际模型流量仍打到本网关。全程不写 ~/.codex/auth.json（本工具从未写过该文件）。
  const providerName = keepOfficialLogin ? 'OpenAI' : (key.name || providerID);
  const keepOfficialComment = keepOfficialLogin
    ? '# 保持账号登录已开启：name/supports_websockets 已对齐官方 provider 形状\n'
    : '';
  return `# ~/.codex/config.toml （用户级；项目内 .codex/config.toml 不会生效 provider）
# Codex 使用 Responses：base_url 指向网关 /openai/v1，wire_api = "responses"
# model_catalog_json 提供覆盖模型/别名的本地元数据，避免 “Model metadata not found”
# 提醒：第三方/自定义 provider 下 Codex 的 remote compaction（长会话自动压缩）
# 常报 "expected exactly one compaction output item"（社区已知通用缺陷，非本
# 工具问题），建议额外执行一次： codex features disable remote_compaction_v2
# （下方的“复制修改脚本”会自动帮你执行这一步；这里是纯文本，需要手动跑）
${warning}${keepOfficialComment}model_provider = "${providerID}"
model = "${model}"
model_reasoning_effort = "${effort}"
model_catalog_json = "${CODEX_MODEL_CATALOG_DISPLAY}"

[model_providers.${providerID}]
name = "${providerName}"
base_url = "${baseURL}"
wire_api = "responses"
requires_openai_auth = true
${keepOfficialLogin ? 'supports_websockets = true\n' : ''}experimental_bearer_token = "${key.key}"
`;
}

/** 给 Claude 模型名补上 [1m] 后缀，让客户端请求 100 万上下文窗口（避免默认 200k 频繁压缩）。
 *  已带后缀 / 空值不重复加；后缀触发 anthropic-beta: context-1m-2025-08-07 头。 */
function withClaude1MContext(model: string) {
  const trimmed = model.trim();
  if (!trimmed || trimmed === 'your-model') return trimmed;
  if (/\[[^\]]*\]\s*$/.test(trimmed)) return trimmed;
  return `${trimmed}[1m]`;
}

function buildApiKeyClaudeConfig(key: APIKey, route: Route | undefined, endpoints: OutputEndpoint[], publicBase: string, provider?: Provider) {
  const model = withClaude1MContext(resolveApiKeyModel(key, provider));
  const baseURL = `${apiKeyGatewayRoot(endpoints, publicBase)}/anthropic`;
  const config = {
    env: {
      ANTHROPIC_BASE_URL: baseURL,
      ANTHROPIC_AUTH_TOKEN: key.key,
      ANTHROPIC_API_KEY: key.key,
      ANTHROPIC_MODEL: model,
    },
    model,
  };
  return `${JSON.stringify(config, null, 2)}\n`;
}

function clientConfigFilePath(client: 'opencode' | 'codex' | 'claude') {
  if (client === 'opencode') return '~/.config/opencode/opencode.json';
  if (client === 'codex') return '~/.codex/config.toml';
  return '~/.claude/settings.json';
}

function clientConfigHomeRelativePath(client: 'opencode' | 'codex' | 'claude') {
  if (client === 'opencode') return '.config/opencode/opencode.json';
  if (client === 'codex') return '.codex/config.toml';
  return '.claude/settings.json';
}

function clientConfigTitle(client: 'opencode' | 'codex' | 'claude') {
  if (client === 'opencode') return 'OpenCode 配置';
  if (client === 'codex') return 'Codex 配置';
  return 'Claude Code 配置';
}

// Codex config.toml 里，本工具管理的 provider 段的分界线（一对，多个 # 号）。
// 只有落在这两行之间的内容会被本工具增量替换；文件里其余任何区块
// （[features]、[memories]、sandbox_mode、approval_policy、personality 等用户
// 自定义配置）永远原样保留、不会被这份脚本触碰或挪动位置。
const CODEX_PROVIDER_BLOCK_BEGIN = '################ LPG-CODEX-PROVIDER-BEGIN ################';
const CODEX_PROVIDER_BLOCK_END = '################ LPG-CODEX-PROVIDER-END ################';

// 复制脚本对应的动作名：三种客户端现在都走增量合并（"修改脚本"）——只更新本工具
// 管理的键/段，保留用户在同一配置文件里的其它设置（OpenCode 的其它 provider/主题/
// keybinds/mcp，Claude 的 permissions/hooks/model/statusLine 等）。
function clientConfigScriptNoun(_client: 'opencode' | 'codex' | 'claude') {
  return '修改脚本';
}

/** UTF-8 → base64，供粘贴型 Python 脚本安全内嵌配置正文（避免引号/heredoc 坑）。 */
function utf8ToBase64(text: string) {
  const bytes = new TextEncoder().encode(text);
  let binary = '';
  for (let i = 0; i < bytes.length; i += 1) binary += String.fromCharCode(bytes[i]);
  return btoa(binary);
}

/** macOS 终端可直接粘贴执行：外层仅一行 python3 heredoc，逻辑全在 Python。 */
function wrapMacPythonScript(pyBody: string, headerComment: string) {
  const marker = `LPG_PY_${Date.now().toString(36)}`;
  return [
    `# ${headerComment}`,
    '# macOS：粘贴到终端执行（依赖系统自带 python3；不依赖 bash/awk）',
    `python3 <<'${marker}'`,
    pyBody.replace(/\n$/, ''),
    marker,
    '',
  ].join('\n');
}

const PY_HELPERS = `
import base64, datetime, pathlib, shutil, subprocess, sys

def _b64(s: str) -> str:
    return base64.b64decode(s.encode("ascii")).decode("utf-8")

def _backup(path: pathlib.Path) -> None:
    if not path.exists():
        return
    bak = path.with_name(path.name + ".bak." + datetime.datetime.now().strftime("%Y%m%d%H%M%S"))
    shutil.copy2(path, bak)
    print(f"已备份: {bak}")

def _write_text(path: pathlib.Path, text: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    if not text.endswith("\\n"):
        text += "\\n"
    path.write_text(text, encoding="utf-8")
`.trim();

/**
 * 生成 Codex 专用的“修改脚本”：增量合并进 ~/.codex/config.toml。
 * - 没有配置文件：直接新建，只包含我们这段。
 * - 已有配置文件：先摘掉此前由本工具写入的分界线区块（如果存在），再把新的一段
 *   插到文件最顶部，最后拼回文件原本剩余的全部内容——[features]/[memories]、
 *   sandbox_mode/approval_policy/personality 等用户自己的配置一个字符都不会被
 *   改动，也不会被换位置。
 * model catalog 附加文件仍是本工具独占的一个文件，按原方式整份覆盖，不受影响。
 */
function buildApiKeyCodexConfigPatchScript(
  configText: string,
  extraFiles: Array<{ rel: string; display: string; content: string }> = [],
) {
  const blockB64 = utf8ToBase64(configText.endsWith('\n') ? configText : `${configText}\n`);
  const extrasPy = extraFiles.map((file) => {
    const body = file.content.endsWith('\n') ? file.content : `${file.content}\n`;
    return `    (${JSON.stringify(file.rel)}, ${JSON.stringify(file.display)}, ${JSON.stringify(utf8ToBase64(body))}),`;
  }).join('\n');
  const py = [
    PY_HELPERS,
    `BEGIN = ${JSON.stringify(CODEX_PROVIDER_BLOCK_BEGIN)}`,
    `END = ${JSON.stringify(CODEX_PROVIDER_BLOCK_END)}`,
    'HOME = pathlib.Path.home()',
    'FILE = HOME / ".codex" / "config.toml"',
    `NEW_BLOCK = _b64(${JSON.stringify(blockB64)})`,
    'EXTRAS = [',
    extrasPy,
    ']',
    '',
    'def strip_provider_block(text: str) -> str:',
    '    out = []',
    '    skip = 0  # 0=normal 1=inside block 2=just after END',
    '    for line in text.splitlines(keepends=True):',
    '        raw = line.rstrip("\\r\\n")',
    '        if skip == 0:',
    '            if raw == BEGIN:',
    '                skip = 1',
    '                continue',
    '            out.append(line)',
    '            continue',
    '        if skip == 1:',
    '            if raw == END:',
    '                skip = 2',
    '            continue',
    '        if raw == "":',
    '            skip = 0',
    '            continue',
    '        skip = 0',
    '        out.append(line)',
    '    return "".join(out)',
    '',
    'FILE.parent.mkdir(parents=True, exist_ok=True)',
    'rest = ""',
    'if FILE.exists():',
    '    _backup(FILE)',
    '    rest = strip_provider_block(FILE.read_text(encoding="utf-8"))',
    'block = NEW_BLOCK if NEW_BLOCK.endswith("\\n") else NEW_BLOCK + "\\n"',
    'merged = f"{BEGIN}\\n{block}{END}\\n\\n{rest}"',
    '_write_text(FILE, merged)',
    'print("已合并写入（仅替换本工具管理的 provider 段，其余配置保持不变）: ~/.codex/config.toml")',
    '',
    'for rel, display, payload in EXTRAS:',
    '    path = HOME / rel',
    '    _backup(path)',
    '    _write_text(path, _b64(payload))',
    '    print(f"已写入: {display}")',
    '',
    '# 第三方 provider 下 remote compaction 常报错；用官方 CLI 关掉（不手改 [features]）',
    'codex = shutil.which("codex")',
    'if codex:',
    '    try:',
    '        subprocess.run([codex, "features", "disable", "remote_compaction_v2"], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)',
    '        print("已关闭 Codex remote compaction（第三方 provider 下长会话自动压缩常报 compaction 相关错误，改走本地压缩）")',
    '    except Exception:',
    '        print("提示：codex features disable remote_compaction_v2 执行失败，可手动运行一次")',
    'else:',
    '    print("提示：未找到 codex 命令，建议手动执行一次: codex features disable remote_compaction_v2 （第三方 provider 下长会话自动压缩可能报错）")',
    'print("完成。如客户端已在运行，请重启后再试。")',
  ].join('\n');
  return wrapMacPythonScript(py, '修改脚本（增量合并进 config.toml；只替换本工具管理的一段，不动你其他配置）');
}

/**
 * 生成“还原为官方 provider”脚本：只删除本工具此前写入的那一段分界线区块，其余
 * 任何配置都不会被触碰；没有该区块时是无害的空操作（不会报错、不会误删）。
 */
function buildApiKeyCodexRestoreOfficialScript() {
  const py = [
    PY_HELPERS,
    `BEGIN = ${JSON.stringify(CODEX_PROVIDER_BLOCK_BEGIN)}`,
    `END = ${JSON.stringify(CODEX_PROVIDER_BLOCK_END)}`,
    'FILE = pathlib.Path.home() / ".codex" / "config.toml"',
    '',
    'def strip_provider_block(text: str) -> str:',
    '    out = []',
    '    skip = 0',
    '    for line in text.splitlines(keepends=True):',
    '        raw = line.rstrip("\\r\\n")',
    '        if skip == 0:',
    '            if raw == BEGIN:',
    '                skip = 1',
    '                continue',
    '            out.append(line)',
    '            continue',
    '        if skip == 1:',
    '            if raw == END:',
    '                skip = 2',
    '            continue',
    '        if raw == "":',
    '            skip = 0',
    '            continue',
    '        skip = 0',
    '        out.append(line)',
    '    return "".join(out)',
    '',
    'if not FILE.exists():',
    '    print(f"未发现 {FILE}，无需还原")',
    '    sys.exit(0)',
    '_backup(FILE)',
    '_write_text(FILE, strip_provider_block(FILE.read_text(encoding="utf-8")))',
    'print(f"已移除本工具管理的 provider 配置，Codex 将回退到官方 provider（其他设置保持不变）: {FILE}")',
    'codex = shutil.which("codex")',
    'if codex:',
    '    try:',
    '        subprocess.run([codex, "features", "enable", "remote_compaction_v2"], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)',
    '        print("已恢复 Codex remote compaction（官方 provider 下通常没有兼容性问题）")',
    '    except Exception:',
    '        print("提示：codex features enable remote_compaction_v2 执行失败，可忽略或手动运行")',
    'else:',
    '    print("提示：未找到 codex 命令，如需恢复 remote compaction 可手动执行: codex features enable remote_compaction_v2")',
  ].join('\n');
  return wrapMacPythonScript(py, '还原为官方 provider（只移除本工具管理的那一段，不动你其他配置）');
}

/** 生成可粘贴到终端执行的修改脚本（Python）：Codex 走 TOML 分界线增量替换；
 *  OpenCode / Claude 走 JSON 键级深合并。 */
function buildApiKeyClientConfigInstallScript(
  client: 'opencode' | 'codex' | 'claude',
  configText: string,
  extraFiles: Array<{ rel: string; display: string; content: string }> = [],
) {
  if (client === 'codex') {
    return buildApiKeyCodexConfigPatchScript(configText, extraFiles);
  }
  // OpenCode / Claude：JSON 键级深合并，只更新本工具管理的键。
  const rel = clientConfigHomeRelativePath(client);
  const display = clientConfigFilePath(client);
  const jsonB64 = utf8ToBase64(configText.endsWith('\n') ? configText : `${configText}\n`);
  const extrasPy = extraFiles.map((file) => {
    const body = file.content.endsWith('\n') ? file.content : `${file.content}\n`;
    return `    (${JSON.stringify(file.rel)}, ${JSON.stringify(file.display)}, ${JSON.stringify(utf8ToBase64(body))}),`;
  }).join('\n');
  const py = [
    PY_HELPERS,
    'import json',
    'HOME = pathlib.Path.home()',
    `FILE = HOME / ${JSON.stringify(rel)}`,
    `NEW = json.loads(_b64(${JSON.stringify(jsonB64)}))`,
    'EXTRAS = [',
    extrasPy,
    ']',
    '',
    'def merge(dst, src):',
    '    for k, v in src.items():',
    '        if isinstance(v, dict) and isinstance(dst.get(k), dict):',
    '            merge(dst[k], v)',
    '        else:',
    '            dst[k] = v',
    '    return dst',
    '',
    'FILE.parent.mkdir(parents=True, exist_ok=True)',
    'data = {}',
    'if FILE.exists():',
    '    _backup(FILE)',
    '    try:',
    '        loaded = json.loads(FILE.read_text(encoding="utf-8") or "{}")',
    '        if isinstance(loaded, dict):',
    '            data = loaded',
    '    except Exception:',
    '        data = {}',
    'merge(data, NEW if isinstance(NEW, dict) else {})',
    'FILE.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\\n", encoding="utf-8")',
    `print(${JSON.stringify(`已合并写入（仅更新本工具管理的键，保留你其它配置）: ${display}`)})`,
    'for rel, display, payload in EXTRAS:',
    '    path = HOME / rel',
    '    _backup(path)',
    '    _write_text(path, _b64(payload))',
    '    print(f"已写入: {display}")',
    'print("完成。如客户端已在运行，请重启后再试。")',
  ].join('\n');
  return wrapMacPythonScript(py, '修改脚本（增量合并进 JSON 配置；只更新本工具管理的键，保留你其它配置）');
}

function buildApiKeyClientConfigExtras(
  client: 'opencode' | 'codex' | 'claude',
  key: APIKey,
  provider?: Provider,
): Array<{ rel: string; display: string; content: string }> {
  if (client !== 'codex') return [];
  return [{
    rel: CODEX_MODEL_CATALOG_REL,
    display: CODEX_MODEL_CATALOG_DISPLAY,
    content: buildApiKeyCodexModelCatalogJSON(key, provider),
  }];
}

function buildApiKeyClientConfig(
  client: 'opencode' | 'codex' | 'claude',
  key: APIKey,
  route: Route | undefined,
  endpoints: OutputEndpoint[],
  publicBase: string,
  provider?: Provider,
  keepOfficialLogin?: boolean,
) {
  if (client === 'opencode') return buildApiKeyOpenCodeConfig(key, route, endpoints, publicBase, provider);
  if (client === 'codex') return buildApiKeyCodexConfig(key, route, endpoints, publicBase, provider, keepOfficialLogin);
  return buildApiKeyClaudeConfig(key, route, endpoints, publicBase, provider);
}

function clientConfigProtocolHint(client: 'opencode' | 'codex' | 'claude', route?: Route) {
  if (!route) return '请先绑定输出协议';
  if (client === 'opencode' && route.outputProtocol !== 'openai_chat' && route.outputProtocol !== 'openai_responses' && route.outputProtocol !== 'claude') {
    return 'OpenCode 需要密钥输出协议为 OpenAI Chat / Responses / Claude';
  }
  if (client === 'codex' && route.outputProtocol !== 'openai_responses') {
    return 'Codex 需要密钥输出协议为 OpenAI Responses';
  }
  if (client === 'claude' && route.outputProtocol !== 'claude') {
    return 'Claude Code 需要密钥输出协议为 Claude（Messages）；不支持 OpenAI Responses';
  }
  return '';
}

function clientConfigCompatible(client: 'opencode' | 'codex' | 'claude', protocol?: Protocol) {
  if (!protocol) return false;
  if (client === 'opencode') return protocol === 'openai_chat' || protocol === 'openai_responses' || protocol === 'claude';
  if (client === 'codex') return protocol === 'openai_responses';
  return protocol === 'claude';
}

function clientConfigsForProtocol(protocol?: Protocol): Array<'opencode' | 'codex' | 'claude'> {
  return (['opencode', 'codex', 'claude'] as const).filter((client) => clientConfigCompatible(client, protocol));
}

function resolveApiKeyModel(key: APIKey, provider?: Provider) {
  return key.modelOverride?.trim() || provider?.defaultModel?.trim() || 'your-model';
}

function buildApiKeyCallPayload(route: Route, model: string) {
  if (route.outputProtocol === 'claude') {
    return { model, max_tokens: 1024, stream: false, messages: [{ role: 'user', content: '你好' }] };
  }
  return { model, stream: false, messages: [{ role: 'user', content: '你好' }] };
}

function localGatewayRoot(endpoints: OutputEndpoint[]) {
  const endpoint = endpoints[0];
  return endpoint ? `http://${endpoint.listenHost}:${endpoint.listenPort}` : 'http://127.0.0.1:18093';
}

function activePublicBaseURL(publicAccess: PublicAccessSettings, tunnelRunning: boolean) {
  if (!tunnelRunning) return '';
  if (publicAccess.mode === 'custom_domain' && publicAccess.exposeApi === false) return '';
  const tunnel = publicAccess.tunnel;
  return tunnel?.publicUrl || publicAccess.publicBaseUrl || '';
}

function activeUIPublicBaseURL(publicAccess: PublicAccessSettings, tunnelRunning: boolean) {
  if (!tunnelRunning) return '';
  if (publicAccess.mode === 'custom_domain' && publicAccess.exposeUi === false) return '';
  const tunnel = publicAccess.tunnel;
  return tunnel?.uiPublicUrl || publicAccess.uiPublicBaseUrl || (publicAccess.uiDomain ? `https://${publicAccess.uiDomain}` : '');
}

function deriveUIDomainFromAPI(apiDomain: string) {
  const normalized = normalizeDomainInput(apiDomain || '');
  if (!normalized) return '';
  const parts = normalized.split('.');
  if (parts.length < 2) return '';
  const root = parts.slice(1).join('.');
  const prefix = parts[0].toLowerCase();
  for (const candidate of ['console', 'admin', 'panel']) {
    if (candidate !== prefix) return `${candidate}.${root}`;
  }
  return `console.${root}`;
}

function buildApiKeyPublicCurl(key: APIKey, route: Route, endpoints: OutputEndpoint[], publicBase: string, provider?: Provider) {
  const model = resolveApiKeyModel(key, provider);
  const payload = buildApiKeyCallPayload(route, model);
  const url = publicBase
    ? routeGatewayURL(route, endpoints, publicBase)
    : routeGatewayURL(route, endpoints, localGatewayRoot(endpoints));
  const auth = route.outputProtocol === 'claude'
    ? { header: 'x-api-key', value: key.key }
    : { header: 'Authorization', value: `Bearer ${key.key}` };
  return buildChatTestCurl(url, payload, auth);
}

function resolveProviderTestModel(model: string) {
  return model.trim() || 'request-model-not-set';
}

function resolveProviderChatURL(provider: Provider, model: string) {
  const effectiveModel = resolveProviderTestModel(model.trim() || provider.defaultModel || 'request-model-not-set');
  let url = provider.baseUrl.replace('{model}', effectiveModel).trim();
  if (!/^https?:\/\//i.test(url)) {
    url = `https://${url}`;
  }
  if (provider.protocol === 'openai_chat' && !url.toLowerCase().includes('/chat/completions') && !provider.baseUrl.includes('{model}')) {
    url = `${url.replace(/\/$/, '')}/chat/completions`;
  }
  return url;
}

function buildProviderChatMessages(options: ProviderChatTestOptions) {
  const messages: Array<{ role: string; content: string }> = [];
  const systemPrompt = options.systemPrompt.trim();
  const userPrompt = options.userPrompt.trim() || 'x+5等于几';
  if (systemPrompt) messages.push({ role: 'system', content: systemPrompt });
  messages.push({ role: 'user', content: userPrompt });
  return messages;
}

function buildProviderCacheRound2Messages(options: ProviderChatTestOptions) {
  const messages = buildProviderChatMessages(options);
  messages.push({ role: 'assistant', content: '（第一轮 assistant 回复）' });
  messages.push({ role: 'user', content: PROVIDER_CACHE_ROUND2_USER });
  return messages;
}

function buildProviderChatPayload(model: string, messages: Array<{ role: string; content: string }>, thinking?: Pick<ProviderChatTestOptions, 'thinkingField' | 'thinkingValue'>) {
  const payload: Record<string, unknown> = {
    model: resolveProviderTestModel(model.trim() || 'request-model-not-set'),
    stream: false,
    messages,
  };
  const field = thinking?.thinkingField?.trim();
  const value = thinking?.thinkingValue?.trim();
  if (field && value) {
    if (field === 'thinking.type') {
      payload.thinking = { type: value };
    } else if (field === 'thinking.budget_tokens') {
      payload.thinking = { type: 'enabled', budget_tokens: Number.isNaN(Number(value)) ? value : Number(value) };
    } else if (field === 'thinking') {
      try {
        payload.thinking = JSON.parse(value);
      } catch {
        payload.thinking = { type: value };
      }
    } else {
      payload[field] = value;
      if (field === 'reasoning_effort') {
        payload.thinking = { type: 'enabled' };
      }
    }
  }
  return payload;
}

function buildChatTestCurl(url: string, payload: Record<string, unknown>, auth?: ProviderAuthPreview | null) {
  const body = JSON.stringify(payload, null, 2);
  const authLine = auth?.value ? `  -H '${auth.header}: ${auth.value}' \\\n` : '';
  return `curl -s '${url}' \\\n  -H 'Content-Type: application/json' \\\n${authLine}  -d '${body.replace(/'/g, `'\\''`)}'`;
}

function buildRouteTestCurl(gatewayURL: string, model: string, message: string, apiKey?: string) {
  const payload = buildRouteTestPayload(model, message);
  const auth = apiKey ? { header: 'Authorization', value: `Bearer ${apiKey}` } : null;
  return buildChatTestCurl(gatewayURL, payload, auth);
}

function buildClaudeOAuthChatCurl(provider: Provider, model: string, options: ProviderChatTestOptions) {
  const resolvedModel = model.trim() || provider.defaultModel || 'request-model-not-set';
  const payload = {
    model: resolvedModel,
    max_tokens: 4096,
    system: options.systemPrompt.trim() || undefined,
    messages: [{ role: 'user', content: options.userPrompt.trim() || 'x+5等于几' }],
    stream: false,
  };
  const body = JSON.stringify(payload, null, 2);
  return `curl -s 'https://api.anthropic.com/v1/messages' \\\n  -H 'Content-Type: application/json' \\\n  -H 'Authorization: Bearer <oauth-access-token (server-managed)>' \\\n  -H 'anthropic-beta: oauth-2025-04-20' \\\n  -d '${body.replace(/'/g, `'\\''`)}'`;
}

function buildProviderChatCurl(provider: Provider, model: string, options: ProviderChatTestOptions, auth?: ProviderAuthPreview | null) {
  if (provider.authType === 'claude_oauth') {
    return buildClaudeOAuthChatCurl(provider, model, options);
  }
  if (provider.authType === 'cursor_oauth') {
    const resolvedModel = model.trim() || provider.defaultModel || 'request-model-not-set';
    const payload = buildProviderChatPayload(resolvedModel, buildProviderChatMessages(options));
    const body = JSON.stringify(payload, null, 2);
    return `curl -s 'http://127.0.0.1:<cursor-bridge-port>/v1/chat/completions' \\\n  -H 'Content-Type: application/json' \\\n  -d '${body.replace(/'/g, `'\\''`)}'`;
  }
  if (provider.authType === 'qoder_pat') {
    const resolvedModel = model.trim() || provider.defaultModel || 'claude-4.5-sonnet';
    const payload = buildProviderChatPayload(resolvedModel, buildProviderChatMessages(options));
    const body = JSON.stringify(payload, null, 2);
    return `curl -s '${(provider.baseUrl || QODER_DEFAULT_BASE_URL).replace(/\/$/, '')}/chat/completions' \\\n  -H 'Content-Type: application/json' \\\n  -H 'Authorization: Bearer <qoder-job-token (server-managed)>' \\\n  -d '${body.replace(/'/g, `'\\''`)}'`;
  }
  if (provider.authType === 'chatgpt_oauth') {
    const resolvedModel = model.trim() || provider.defaultModel || 'gpt-5.2';
    const payload = buildProviderChatPayload(resolvedModel, buildProviderChatMessages(options));
    const body = JSON.stringify(payload, null, 2);
    return `curl -s 'https://chatgpt.com/backend-api/codex/responses' \\\n  -H 'Content-Type: application/json' \\\n  -H 'Authorization: Bearer <oauth-access-token (server-managed)>' \\\n  -H 'ChatGPT-Account-ID: <account-id>' \\\n  -d '${body.replace(/'/g, `'\\''`)}'`;
  }
  const upstreamURL = resolveProviderChatURL(provider, model);
  const round1 = buildChatTestCurl(upstreamURL, buildProviderChatPayload(model, buildProviderChatMessages(options)), auth);
  const round2 = buildChatTestCurl(upstreamURL, buildProviderChatPayload(model, buildProviderCacheRound2Messages(options)), auth);
  const thinking = buildChatTestCurl(upstreamURL, buildProviderChatPayload(model, buildProviderChatMessages(options), options), auth);
  return `# 主对话测试\n${buildChatTestCurl(upstreamURL, buildProviderChatPayload(model, buildProviderChatMessages(options)), auth)}\n\n# Cache 测试 Round 1\n${round1}\n\n# Cache 测试 Round 2\n${round2}\n\n# Thinking 测试\n${thinking}`;
}

function protocolTone(protocol: Protocol): BadgeTone {
  switch (protocol) {
    case 'openai_chat': return 'blue';
    case 'openai_responses': return 'amber';
    case 'claude': return 'purple';
  }
}

function actionTone(action: string): BadgeTone {
  if (action.includes('pass')) return 'green';
  if (action.includes('convert')) return 'amber';
  if (action.includes('error') || action.includes('fail')) return 'red';
  if (action.includes('test')) return 'cyan';
  return 'slate';
}

function statusTone(status?: number): BadgeTone {
  if (!status) return 'slate';
  if (status >= 200 && status < 300) return 'green';
  if (status === 501) return 'amber';
  if (status >= 400) return 'red';
  return 'slate';
}

function healthTone(status: string): BadgeTone {
  if (status === 'healthy') return 'green';
  if (status === 'failed') return 'red';
  if (status === 'unavailable') return 'red';
  if (status === 'degraded') return 'amber';
  if (status === 'standby') return 'blue';
  return 'slate';
}

function ThemeIcon({ kind }: { kind: ThemeMode }) {
  if (kind === 'light') {
    return (
      <svg className="theme-icon" viewBox="0 0 24 24" aria-hidden="true">
        <circle cx="12" cy="12" r="4.2" fill="currentColor" />
        <g stroke="currentColor" strokeWidth="1.8" strokeLinecap="round">
          <path d="M12 2.5v2.2M12 19.3v2.2M2.5 12h2.2M19.3 12h2.2M5.1 5.1l1.6 1.6M17.3 17.3l1.6 1.6M5.1 18.9l1.6-1.6M17.3 6.7l1.6-1.6" />
        </g>
      </svg>
    );
  }
  if (kind === 'dark') {
    return (
      <svg className="theme-icon" viewBox="0 0 24 24" aria-hidden="true">
        <path
          fill="currentColor"
          d="M15.2 3.1a8.8 8.8 0 1 0 5.7 15.5A8.2 8.2 0 0 1 15.2 3.1Z"
        />
      </svg>
    );
  }
  return (
    <svg className="theme-icon" viewBox="0 0 24 24" aria-hidden="true">
      <rect x="3.5" y="4.5" width="17" height="12" rx="2.2" fill="none" stroke="currentColor" strokeWidth="1.8" />
      <path d="M8 20.2h8M12 16.5v3.7" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" />
    </svg>
  );
}

function ThemeSwitch({
  value,
  onChange,
  size = 'default',
}: {
  value: ThemeMode;
  onChange: (mode: ThemeMode) => void;
  size?: 'default' | 'compact';
}) {
  const options: Array<{ id: ThemeMode; label: string }> = [
    { id: 'light', label: '白天' },
    { id: 'dark', label: '夜晚' },
    { id: 'system', label: '跟随系统' },
  ];
  return (
    <div className={`theme-switch ${size === 'compact' ? 'compact' : ''}`} role="group" aria-label="主题模式">
      {options.map((option) => (
        <button
          key={option.id}
          type="button"
          className={`theme-switch-btn ${value === option.id ? 'active' : ''}`}
          onClick={() => onChange(option.id)}
          title={option.label}
          aria-label={option.label}
          aria-pressed={value === option.id}
        >
          <ThemeIcon kind={option.id} />
        </button>
      ))}
    </div>
  );
}

function Badge({ tone, children }: { tone: BadgeTone; children: React.ReactNode }) {
  return <span className={`badge ${tone}`}>{children}</span>;
}

function UsageLineChart({ title, points }: { title: string; points: DailyRequestPoint[] }) {
  const width = 360;
  const height = 168;
  const padLeft = 36;
  const padRight = 12;
  const padTop = 16;
  const padBottom = 28;
  const plotWidth = width - padLeft - padRight;
  const plotHeight = height - padTop - padBottom;
  const max = Math.max(1, ...points.map((p) => p.requestCount));
  const total = points.reduce((sum, point) => sum + point.requestCount, 0);
  const barGap = points.length <= 1 ? 0 : Math.min(10, plotWidth / (points.length * 4));
  const barWidth = points.length <= 0 ? 0 : Math.max(8, (plotWidth - barGap * Math.max(0, points.length - 1)) / points.length);

  return (
    <div className="usage-chart-card">
      <div className="usage-section-title">{title}</div>
      {points.length === 0 ? <div className="empty-state compact">暂无数据</div> : (
        <svg viewBox={`0 0 ${width} ${height}`} className="usage-chart-svg" role="img" aria-label={title}>
          <line x1={padLeft} y1={padTop} x2={padLeft} y2={height - padBottom} stroke="rgba(15,23,42,0.12)" strokeWidth="1" />
          <line x1={padLeft} y1={height - padBottom} x2={width - padRight} y2={height - padBottom} stroke="rgba(15,23,42,0.12)" strokeWidth="1" />
          {[0, 0.5, 1].map((ratio) => {
            const y = height - padBottom - ratio * plotHeight;
            const label = Math.round(max * ratio);
            return (
              <g key={`grid-${ratio}`}>
                <line x1={padLeft} y1={y} x2={width - padRight} y2={y} stroke="rgba(15,23,42,0.06)" strokeWidth="1" />
                <text x={padLeft - 6} y={y + 3} textAnchor="end" fontSize="9" fill="rgba(15,23,42,0.45)">{label}</text>
              </g>
            );
          })}
          {points.map((point, index) => {
            const x = padLeft + index * (barWidth + barGap);
            const barHeight = (point.requestCount / max) * plotHeight;
            const y = height - padBottom - barHeight;
            const dateLabel = (point.date || '').slice(5) || point.date;
            return (
              <g key={point.date}>
                <rect
                  x={x}
                  y={point.requestCount > 0 ? y : height - padBottom - 2}
                  width={barWidth}
                  height={point.requestCount > 0 ? Math.max(2, barHeight) : 2}
                  rx="3"
                  fill={point.requestCount > 0 ? 'var(--accent, #2563eb)' : 'rgba(15,23,42,0.12)'}
                >
                  <title>{`${point.date}: ${point.requestCount}`}</title>
                </rect>
                {point.requestCount > 0 ? (
                  <text x={x + barWidth / 2} y={y - 4} textAnchor="middle" fontSize="9" fill="rgba(15,23,42,0.7)">{point.requestCount}</text>
                ) : null}
                <text x={x + barWidth / 2} y={height - padBottom + 14} textAnchor="middle" fontSize="9" fill="rgba(15,23,42,0.5)">{dateLabel}</text>
              </g>
            );
          })}
        </svg>
      )}
      <div className="hint-line">
        {points.length
          ? `${points[0]?.date || ''} ~ ${points[points.length - 1]?.date || ''} · 合计 ${total}`
          : ''}
      </div>
    </div>
  );
}

function UsageBarChart({ title, items, formatValue }: { title: string; items: Array<{ label: string; value: number }>; formatValue?: (value: number) => string }) {
  const max = Math.max(1, ...items.map((item) => item.value));
  return (
    <div className="usage-chart-card">
      <div className="usage-section-title">{title}</div>
      {items.length === 0 ? <div className="empty-state compact">暂无数据</div> : (
        <div className="usage-bar-list">
          {items.map((item) => (
            <div className="usage-bar-row" key={item.label}>
              <span className="usage-bar-label" title={item.label}>{item.label}</span>
              <div className="usage-bar-track"><div className="usage-bar-fill" style={{ width: `${(item.value / max) * 100}%` }} /></div>
              <span className="usage-bar-value">{formatValue ? formatValue(item.value) : item.value}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function normalizePromptTokenStats(input: number, cache: number) {
  // InputTokens from API is prompt total (inclusive). Legacy rows may store
  // exclusive non-cached counts; when cache > input, treat as Claude semantics.
  const cacheHits = Math.max(0, cache || 0);
  let totalInput = Math.max(0, input || 0);
  if (cacheHits > 0 && totalInput < cacheHits) {
    totalInput += cacheHits;
  }
  const nonCachedInput = Math.max(0, totalInput - cacheHits);
  const hitRatePct = totalInput > 0 ? Math.min(100, (cacheHits / totalInput) * 100) : 0;
  return { totalInput, cacheHits, nonCachedInput, hitRatePct };
}

function UsageCacheHitRate({ title, input, cache }: { title: string; input: number; cache: number }) {
  const { totalInput, cacheHits, hitRatePct } = normalizePromptTokenStats(input, cache);
  const hitRateLabel = totalInput > 0 ? `${hitRatePct.toFixed(1)}%` : '—';

  return (
    <div className="usage-chart-card">
      <div className="usage-section-title">{title}</div>
      {totalInput === 0 ? (
        <div className="empty-state compact">暂无数据</div>
      ) : (
        <>
          <div className="usage-cache-hit-row">
            <div className="usage-stack-bar usage-cache-hit-bar" title={`cache hit rate ${hitRateLabel}`}>
              <div style={{ width: `${hitRatePct}%`, background: '#d97706' }} title={`cache ${cacheHits}`} />
            </div>
            <span className="usage-cache-hit-pct">{hitRateLabel}</span>
          </div>
          <div className="hint-line">in {formatTokenCount(totalInput)} · cache {formatTokenCount(cacheHits)}</div>
        </>
      )}
    </div>
  );
}

function UsageStatusChart({ title, items }: { title: string; items: StatusBucketStats[] }) {
  const total = Math.max(1, items.reduce((sum, item) => sum + item.requestCount, 0));
  const colors: Record<string, string> = { '2xx': '#059669', '4xx': '#d97706', '5xx': '#dc2626', other: '#64748b' };
  return (
    <div className="usage-chart-card">
      <div className="usage-section-title">{title}</div>
      <div className="usage-stack-bar">
        {items.map((item) => (
          <div key={item.class} style={{ width: `${(item.requestCount / total) * 100}%`, background: colors[item.class] || '#64748b' }} title={`${item.class}: ${item.requestCount}`} />
        ))}
      </div>
      <div className="hint-line">{items.map((item) => `${item.class} ${item.requestCount}`).join(' · ') || '暂无数据'}</div>
    </div>
  );
}

function Metric({ label, value, note }: { label: string; value: string; note: string }) {
  return (
    <div className="card metric">
      <div>
        <div className="metric-label">{label}</div>
        <div className="metric-value">{value}</div>
      </div>
      <div className="metric-note">{note}</div>
    </div>
  );
}

function thermalPressureLabel(level?: string) {
  switch ((level || '').toLowerCase()) {
    case 'nominal': return '正常';
    case 'fair': return '偏高';
    case 'serious': return '较高';
    case 'critical': return '过高';
    default: return level || '—';
  }
}

const TEMP_SOURCE_LABELS: Record<string, string> = {
  'iokit/cpu-cluster': 'CPU 核心簇传感器',
  'iokit/cpu': 'CPU 传感器',
  'iokit/soc-die': 'SoC 芯片传感器',
  'iokit/soc': 'SoC 封装传感器',
  'powermetrics/smc': 'powermetrics SMC',
};

function tempSourceLabel(source?: string) {
  if (!source) return '已采样';
  return `来源 ${TEMP_SOURCE_LABELS[source] || source}`;
}

function formatBytes(bytes?: number, digits = 1): string {
  if (bytes == null || !Number.isFinite(bytes) || bytes < 0) return '—';
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  let value = bytes;
  let i = 0;
  while (value >= 1024 && i < units.length - 1) {
    value /= 1024;
    i += 1;
  }
  return `${value.toFixed(i === 0 ? 0 : digits)} ${units[i]}`;
}

function formatRate(bytesPerSecond?: number): string {
  if (bytesPerSecond == null || !Number.isFinite(bytesPerSecond) || bytesPerSecond < 0) return '—';
  return `${formatBytes(bytesPerSecond)}/s`;
}

function formatDuration(seconds?: number): string {
  if (seconds == null || !Number.isFinite(seconds) || seconds <= 0) return '—';
  const total = Math.floor(seconds);
  const d = Math.floor(total / 86400);
  const h = Math.floor((total % 86400) / 3600);
  const m = Math.floor((total % 3600) / 60);
  if (d > 0) return `${d} 天 ${h} 小时`;
  if (h > 0) return `${h} 小时 ${m} 分`;
  if (m > 0) return `${m} 分 ${total % 60} 秒`;
  return `${total} 秒`;
}

/** 0–100 的占用条，>=90% 判危险、>=75% 判警告。 */
function UsageBar({ percent }: { percent?: number }) {
  const value = Math.max(0, Math.min(100, percent ?? 0));
  const tone = value >= 90 ? 'danger' : value >= 75 ? 'warn' : 'ok';
  return (
    <div className={`usage-bar ${tone}`} role="presentation">
      <span style={{ width: `${value}%` }} />
    </div>
  );
}

function MachineMetric({ label, value, note, percent }: { label: string; value: string; note: string; percent?: number }) {
  return (
    <div className="card metric machine-metric">
      <div>
        <div className="metric-label">{label}</div>
        <div className="metric-value">{value}</div>
        {percent != null ? <UsageBar percent={percent} /> : null}
      </div>
      <div className="metric-note">{note}</div>
    </div>
  );
}

function hostTempMetric(hostMetrics: HostMetrics | null): { value: string; note: string } {
  if (!hostMetrics) {
    return { value: '—', note: '主页可见时每 10 秒刷新' };
  }
  if (hostMetrics.tempAvailable && hostMetrics.tempC != null) {
    return {
      value: `${hostMetrics.tempC.toFixed(1)}°C`,
      note: tempSourceLabel(hostMetrics.tempSource),
    };
  }
  if (hostMetrics.thermalPressureAvailable && hostMetrics.thermalPressure) {
    return {
      value: thermalPressureLabel(hostMetrics.thermalPressure),
      note: `热力等级 ${hostMetrics.thermalPressure}（本机无 ℃ 接口）`,
    };
  }
  return { value: '—', note: '本机未暴露温度传感器' };
}

function diskTempLabel(disk: HostDiskTemp, index: number, total: number): string {
  if (disk.internal === true) return '内置硬盘温度';
  if (disk.internal === false) return '外置硬盘温度';
  if (total > 1) return `硬盘温度 ${index + 1}`;
  return '硬盘温度';
}

function diskTempNote(disk: HostDiskTemp): string {
  const parts: string[] = [];
  if (disk.model) parts.push(disk.model);
  if (disk.device) parts.push(disk.device);
  return parts.join(' · ') || (disk.source ? `来源 ${disk.source}` : 'SMART');
}

function fanSpeedLabel(fan: HostFanSpeed, index: number, total: number): string {
  if (fan.name) return fan.name;
  if (total > 1) return `风扇 ${index + 1}`;
  return '风扇转速';
}

function fanSpeedNote(fan: HostFanSpeed): string {
  const parts: string[] = [];
  if (fan.minRpm != null && fan.maxRpm != null && fan.maxRpm > 0) {
    parts.push(`${Math.round(fan.minRpm)}–${Math.round(fan.maxRpm)} RPM`);
  }
  if (Number.isFinite(fan.percent)) {
    parts.push(`${fan.percent.toFixed(0)}%`);
  }
  return parts.join(' · ') || (fan.source ? `来源 ${fan.source}` : 'SMC');
}

function formatLocalISODate(date: Date) {
  const y = date.getFullYear();
  const m = String(date.getMonth() + 1).padStart(2, '0');
  const d = String(date.getDate()).padStart(2, '0');
  return `${y}-${m}-${d}`;
}

/** 单日且等于本地今天 → 视为「跟随今天」，跨日后侧边栏再进页会自动滚到新的今天。 */
function isFollowingTodayRange(from: string, to: string, today = formatLocalISODate(new Date())) {
  return Boolean(from) && from === to && from === today;
}

/** 用量统计日历：单击选单日，Shift+单击选区间，选完即回调。可选支持清除（不限日期）。 */
function UsageRangeCalendar({ from, to, onSelect, onClear }: {
  from: string;
  to: string;
  onSelect: (from: string, to: string) => void;
  onClear?: () => void;
}) {
  const todayStr = formatLocalISODate(new Date());
  const [viewYM, setViewYM] = React.useState(() => (to || from || todayStr).slice(0, 7));
  const [year, month] = viewYM.split('-').map(Number);
  const startWeekday = (new Date(year, month - 1, 1).getDay() + 6) % 7; // 周一开头
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
              className={
                `usage-calendar-day${inRange ? ' in-range' : ''}${isEdge ? ' edge' : ''}${day === todayStr ? ' today' : ''}`
              }
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

/** 英文紧凑单位：k / M / B，保留一位小数。 */
function formatEnCompact(value: number) {
  const trim = (n: number) => {
    const s = n.toFixed(1);
    return s.endsWith('.0') ? s.slice(0, -2) : s;
  };
  if (value >= 1e9) return `${trim(value / 1e9)}B`;
  if (value >= 1e6) return `${trim(value / 1e6)}M`;
  if (value >= 1e3) return `${trim(value / 1e3)}k`;
  return String(value);
}

/** 按点数稀疏选取 X 轴刻度，避免 30 天全标挤在一起。 */
function pickUsageXTickIndexes(count: number) {
  if (count <= 0) return [] as number[];
  if (count <= 10) return Array.from({ length: count }, (_, i) => i);
  const step = Math.max(1, Math.ceil(count / 8));
  const indexes = new Set<number>([0, count - 1]);
  for (let i = 0; i < count; i += step) indexes.add(i);
  return [...indexes].sort((a, b) => a - b);
}

/** 每日 Token（柱 / 左轴）+ 请求次数（折线 / 右轴）组合图。 */
function UsageMonthlyTokenBars({ title = '最近 7 天 · 每日用量', points, onPickDay }: {
  title?: string;
  points: DailyRequestPoint[];
  onPickDay?: (date: string) => void;
}) {
  const tokenValues = points.map((p) => (p.inputTokens || 0) + (p.outputTokens || 0));
  const requestValues = points.map((p) => p.requestCount || 0);
  const tokenMax = Math.max(...tokenValues, 1);
  const requestMax = Math.max(...requestValues, 1);
  // 折线坐标：每根柱子的中心点，比例坐标（0~100）
  const linePoints = points.map((p, i) => {
    const x = points.length === 1 ? 50 : ((i + 0.5) / points.length) * 100;
    const y = 100 - (requestValues[i] / requestMax) * 96 - 2;
    return `${x.toFixed(2)},${y.toFixed(2)}`;
  }).join(' ');
  // 数值标注：非 0 的每天都标
  const barX = (i: number) => (points.length === 1 ? 50 : ((i + 0.5) / points.length) * 100);
  const xTickIndexes = pickUsageXTickIndexes(points.length);
  return (
    <div className="usage-month-bars" role="img" aria-label={title}>
      <div className="usage-month-bars-head">
        <div className="usage-month-bars-title">{title}</div>
        <div className="usage-month-bars-legend">
          <span className="usage-month-legend-item"><i className="legend-bar" />Token（左轴）</span>
          <span className="usage-month-legend-item"><i className="legend-line" />请求次数（右轴）</span>
        </div>
      </div>
      {points.length === 0 ? (
        <div className="usage-month-bars-empty">暂无数据</div>
      ) : (
        <>
          <div className="usage-month-bars-plot">
            <div className="usage-month-bars-yaxis" aria-label="Token">
              <span>{formatTokenCount(tokenMax)}</span>
              <span>{formatTokenCount(Math.round(tokenMax / 2))}</span>
              <span>0</span>
            </div>
            <div className="usage-month-bars-track">
              {points.map((p, i) => {
                const value = tokenValues[i];
                const h = value > 0 ? Math.max(4, Math.round((value / tokenMax) * 100)) : 2;
                return (
                  <button
                    key={p.date}
                    type="button"
                    className={`usage-month-bar${value === 0 ? ' zero' : ''}`}
                    title={`${p.date} · ${formatTokenCount(value)} tokens · ${p.requestCount} 次请求`}
                    onClick={() => onPickDay?.(p.date)}
                  >
                    <span style={{ height: `${h}%` }} />
                  </button>
                );
              })}
              <svg
                className="usage-month-line"
                viewBox="0 0 100 100"
                preserveAspectRatio="none"
                aria-hidden="true"
              >
                <polyline
                  points={linePoints}
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="1.6"
                  strokeLinejoin="round"
                  strokeLinecap="round"
                  vectorEffect="non-scaling-stroke"
                />
              </svg>
              {/* 柱内底部 Token 标注（k / M / B），与折线上方的请求数标注错开 */}
              {points.map((p, i) => {
                if (tokenValues[i] <= 0) return null;
                return (
                  <span
                    key={`tl-${p.date}`}
                    className="usage-month-label token"
                    style={{ left: `${barX(i)}%`, bottom: '3%' }}
                  >
                    {formatEnCompact(tokenValues[i])}
                  </span>
                );
              })}
              {/* 折线请求次数标注，非 0 每天都标 */}
              {points.map((p, i) => {
                if (requestValues[i] <= 0) return null;
                const y = (requestValues[i] / requestMax) * 96 + 2;
                return (
                  <span
                    key={`rl-${p.date}`}
                    className="usage-month-label request"
                    style={{ left: `${barX(i)}%`, bottom: `${Math.min(y + 3, 95)}%` }}
                  >
                    {formatEnCompact(requestValues[i])}
                  </span>
                );
              })}
            </div>
            <div className="usage-month-bars-yaxis right" aria-label="请求次数">
              <span>{requestMax}</span>
              <span>{Math.round(requestMax / 2)}</span>
              <span>0</span>
            </div>
          </div>
          <div className="usage-month-bars-xaxis">
            {points.map((p, i) => (
              <span key={p.date} className="usage-month-bars-xtick">
                {xTickIndexes.includes(i) ? p.date.slice(5) : ''}
              </span>
            ))}
          </div>
        </>
      )}
    </div>
  );
}

/** 按日转发流量（接收/发送）双折线图。数据来自 usage_daily 汇总，与请求量同源、
 *  一天一条、永久保留。用 SVG 折线而非柱状：天数多时柱子会挤在一起互相重叠，
 *  折线在任意天数下都不会。 */
function UsageDailyTrafficLines({ title = '按日流量', points, onPickDay }: {
  title?: string;
  points: DailyRequestPoint[];
  onPickDay?: (date: string) => void;
}) {
  const width = 960;
  const height = 260;
  const padLeft = 74;
  const padRight = 74;
  const padTop = 26;
  const padBottom = 46;
  const plotWidth = width - padLeft - padRight;
  const plotHeight = height - padTop - padBottom;

  const rxValues = points.map((p) => p.rxBytes || 0);
  const txValues = points.map((p) => p.txBytes || 0);
  const xTickIndexes = pickUsageXTickIndexes(points.length);

  // 双纵轴：接收通常比发送大一两个数量级（实测 91 MB vs 1.3 MB），共用一根轴会
  // 把发送线压在 0 刻度上、和它自己的标注值对不上。故左轴管接收、右轴管发送，
  // 各按自身量级缩放；轴刻度用同色标注，明确哪条线读哪根轴。
  const rxMax = Math.max(1, ...rxValues);
  const txMax = Math.max(1, ...txValues);

  // 单点时居中，避免除零。
  const xAt = (i: number) => (points.length <= 1 ? padLeft + plotWidth / 2 : padLeft + (i / (points.length - 1)) * plotWidth);
  const yOf = (v: number, scaleMax: number) => height - padBottom - (v / scaleMax) * plotHeight;
  const yRx = (v: number) => yOf(v, rxMax);
  const yTx = (v: number) => yOf(v, txMax);
  const polyline = (values: number[], scaleMax: number) =>
    values.map((v, i) => `${xAt(i).toFixed(1)},${yOf(v, scaleMax).toFixed(1)}`).join(' ');

  const hasTraffic = rxValues.some((v) => v > 0) || txValues.some((v) => v > 0);
  // 点太密时标注会糊成一片，只在刻度日标注；天数少时每天都标。
  const labelIndexes = points.length <= 14
    ? points.map((_, i) => i)
    : xTickIndexes;

  return (
    <div className="usage-chart-card usage-traffic-card">
      <div className="usage-month-bars-head">
        <div className="usage-month-bars-title">{title}</div>
        {/* 图例只做颜色与读轴说明。刻意不放合计数值：区间可能是几十天，
            一个区间总量对逐日曲线没有解释力，具体数值看点标注与悬浮提示。 */}
        <div className="usage-month-bars-legend">
          <span className="usage-month-legend-item"><i className="legend-line traffic-rx" />接收（左轴）</span>
          <span className="usage-month-legend-item"><i className="legend-line traffic-tx" />发送（右轴）</span>
        </div>
      </div>
      {points.length === 0 ? (
        <div className="empty-state compact">暂无数据</div>
      ) : !hasTraffic ? (
        <div className="empty-state compact">暂无流量数据（本功能上线后的请求才会计入）</div>
      ) : (
        <svg viewBox={`0 0 ${width} ${height}`} className="usage-chart-svg" role="img" aria-label={title}>
          {[0, 0.25, 0.5, 0.75, 1].map((ratio) => {
            const y = height - padBottom - ratio * plotHeight;
            return (
              <g key={`grid-${ratio}`}>
                <line className="tl-grid" x1={padLeft} y1={y} x2={width - padRight} y2={y} stroke="currentColor" strokeWidth="1" />
                {/* 左轴刻度＝接收量级，右轴刻度＝发送量级，各自用线色标注 */}
                <text className="tl-val rx" x={padLeft - 8} y={y + 3} textAnchor="end" fontSize="10" fill="currentColor">
                  {formatBytes(rxMax * ratio, 1)}
                </text>
                <text className="tl-val tx" x={width - padRight + 8} y={y + 3} textAnchor="start" fontSize="10" fill="currentColor">
                  {formatBytes(txMax * ratio, 1)}
                </text>
              </g>
            );
          })}
          {/* 坐标轴：左右各一根纵轴 + 一根横轴 */}
          <line className="tl-axis" x1={padLeft} y1={padTop} x2={padLeft} y2={height - padBottom} stroke="currentColor" strokeWidth="1" />
          <line className="tl-axis" x1={width - padRight} y1={padTop} x2={width - padRight} y2={height - padBottom} stroke="currentColor" strokeWidth="1" />
          <line className="tl-axis" x1={padLeft} y1={height - padBottom} x2={width - padRight} y2={height - padBottom} stroke="currentColor" strokeWidth="1" />
          {/* 轴标题 */}
          <text className="tl-val rx" x={padLeft - 8} y={padTop - 10} textAnchor="end" fontSize="10" fontWeight="700" fill="currentColor">接收</text>
          <text className="tl-val tx" x={width - padRight + 8} y={padTop - 10} textAnchor="start" fontSize="10" fontWeight="700" fill="currentColor">发送</text>
          <text className="tl-axis-title" x={width / 2} y={height - 6} textAnchor="middle" fontSize="10" fontWeight="700" fill="currentColor">日期</text>

          <polyline points={polyline(rxValues, rxMax)} fill="none" className="tl-line rx" stroke="currentColor" strokeWidth="2" strokeLinejoin="round" strokeLinecap="round" />
          <polyline points={polyline(txValues, txMax)} fill="none" className="tl-line tx" stroke="currentColor" strokeWidth="2" strokeLinejoin="round" strokeLinecap="round" />

          {points.map((p, i) => (
            <g key={`pt-${p.date}`}>
              {/* 透明热区：整列可点/可悬浮，不必精准命中细小的点 */}
              <rect
                x={xAt(i) - Math.max(6, plotWidth / Math.max(points.length, 1) / 2)}
                y={padTop}
                width={Math.max(12, plotWidth / Math.max(points.length, 1))}
                height={plotHeight}
                fill="transparent"
                style={{ cursor: onPickDay ? 'pointer' : 'default' }}
                onClick={() => onPickDay?.(p.date)}
              >
                <title>{`${p.date} · 接收 ${formatBytes(rxValues[i])} · 发送 ${formatBytes(txValues[i])}`}</title>
              </rect>
              {rxValues[i] > 0 ? <circle cx={xAt(i)} cy={yRx(rxValues[i])} r="3" className="tl-dot rx" fill="currentColor" /> : null}
              {txValues[i] > 0 ? <circle cx={xAt(i)} cy={yTx(txValues[i])} r="3" className="tl-dot tx" fill="currentColor" /> : null}
            </g>
          ))}

          {/* 散点数值标注。两条线各读自己的轴，位置可能仍然撞上：按屏幕 y 判断
              谁在上方，上者标上、下者标下；再钳进绘图区避免顶到轴标题。 */}
          {points.map((p, i) => {
            if (!labelIndexes.includes(i)) return null;
            const rx = rxValues[i];
            const tx = txValues[i];
            const rxScreenY = yRx(rx);
            const txScreenY = yTx(tx);
            const clamp = (y: number) => Math.min(Math.max(y, padTop + 8), height - padBottom - 3);
            const collide = Math.abs(rxScreenY - txScreenY) < 14;
            const rxAbove = !collide || rxScreenY <= txScreenY;
            return (
              <g key={`lbl-${p.date}`} pointerEvents="none">
                {rx > 0 ? (
                  <text x={xAt(i)} y={clamp(rxScreenY + (rxAbove ? -9 : 15))} textAnchor="middle" fontSize="9.5" fontWeight="700" className="tl-val rx" fill="currentColor">
                    {formatBytes(rx, 1)}
                  </text>
                ) : null}
                {tx > 0 ? (
                  <text x={xAt(i)} y={clamp(txScreenY + (rxAbove ? 15 : -9))} textAnchor="middle" fontSize="9.5" fontWeight="700" className="tl-val tx" fill="currentColor">
                    {formatBytes(tx, 1)}
                  </text>
                ) : null}
              </g>
            );
          })}

          {points.map((p, i) => (
            xTickIndexes.includes(i) ? (
              <g key={`x-${p.date}`}>
                <line className="tl-axis" x1={xAt(i)} y1={height - padBottom} x2={xAt(i)} y2={height - padBottom + 4} stroke="currentColor" strokeWidth="1" />
                <text
                  x={xAt(i)}
                  y={height - padBottom + 17}
                  textAnchor="middle"
                  fontSize="10"
                  fill="currentColor"
                  className="tl-tick"
                >
                  {p.date.slice(5)}
                </text>
              </g>
            ) : null
          ))}
        </svg>
      )}
    </div>
  );
}

function endpointURL(endpoint?: OutputEndpoint) {
  if (!endpoint) return API_BASE;
  return `http://${endpoint.listenHost}:${endpoint.listenPort}${endpoint.basePath}`;
}

function normalizeDomainInput(value: string) {
  return value.trim().replace(/^https?:\/\//, '').replace(/\/+$/, '');
}

function publicStatusTone(status: string): BadgeTone {
  if (status === 'runtime_url_recorded') return 'green';
  if (status === 'configured_pending_tunnel') return 'blue';
  if (status === 'waiting_for_tunnel') return 'amber';
  if (status === 'unsupported' || status === 'error') return 'red';
  return 'slate';
}

function publicAccessURL(endpoint: OutputEndpoint | undefined, tunnelRunning: boolean) {
  if (!tunnelRunning || !endpoint?.publicUrl) return '隧道未启动，请先在「公网访问」页启动隧道';
  return endpoint.publicUrl;
}

function tunnelStatusTone(status?: string): BadgeTone {
  if (status === 'running') return 'green';
  if (status === 'starting') return 'amber';
  if (status === 'error') return 'red';
  return 'slate';
}

type CloudflareZoneOption = { id: string; name: string };

function splitCustomDomain(full?: string) {
  const normalized = normalizeDomainInput(full || '');
  if (!normalized) return { prefix: 'gateway', root: '' };
  const parts = normalized.split('.');
  if (parts.length <= 2) return { prefix: '', root: normalized };
  return { prefix: parts[0], root: parts.slice(1).join('.') };
}

function pickCustomDomainRoot(preferred: string, zones: CloudflareZoneOption[]) {
  const names = zones.map((zone) => zone.name).filter(Boolean);
  if (preferred && names.includes(preferred)) return preferred;
  if (names.length === 1) return names[0];
  if (preferred && names.length === 0) return preferred;
  return names[0] || preferred || '';
}

function composeCustomDomain(prefix: string, root: string) {
  const cleanPrefix = prefix.trim().replace(/\.$/, '').replace(/\s+/g, '');
  const cleanRoot = normalizeDomainInput(root);
  if (!cleanRoot) return '';
  return cleanPrefix ? `${cleanPrefix}.${cleanRoot}` : cleanRoot;
}

function formatTokenCount(value: number) {
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`;
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)}K`;
  return String(value);
}

function formatModelOutputBudget(n: number | undefined) {
  if (!n || n <= 0) return '';
  if (n >= 1_000_000) {
    const m = n / 1_000_000;
    return Number.isInteger(m) ? `${m}M` : `${m.toFixed(1)}M`;
  }
  if (n >= 1_000) {
    const k = n / 1_000;
    return Number.isInteger(k) ? `${k}k` : `${k.toFixed(1)}k`;
  }
  return String(n);
}

function modelSelectOptionLabel(model: Model) {
  const out = formatModelOutputBudget(model.maxOutputTokens);
  return out ? `${model.id} · out ${out}` : model.id;
}

function formatCompactCount(value: number) {
  const formatScaled = (scaled: number, unit: string) => {
    const decimals = scaled >= 100 ? 0 : scaled >= 10 ? 1 : 2;
    const formatted = scaled.toFixed(decimals).replace(/\.0+$/, '').replace(/(\.\d)0$/, '$1');
    return `${formatted}${unit}`;
  };
  if (value >= 100_000_000) return formatScaled(value / 100_000_000, '亿');
  if (value >= 1_000_000) return formatScaled(value / 1_000_000, '百万');
  if (value >= 10_000) return formatScaled(value / 10_000, '万');
  return value.toLocaleString('zh-CN');
}

function formatTokenSummaryCompact(stats: Pick<APIKeyDayStats, 'inputTokens' | 'outputTokens' | 'cacheTokens'>) {
  const { totalInput, cacheHits } = normalizePromptTokenStats(stats.inputTokens, stats.cacheTokens || 0);
  return `in ${formatCompactCount(totalInput)} · out ${formatCompactCount(stats.outputTokens)} · cache ${formatCompactCount(cacheHits)}`;
}

function App() {
  const [activeNav, setActiveNav] = useState<NavItemID>(() => navIDFromPath(window.location.pathname));
  const [themeMode, setThemeMode] = useState<ThemeMode>(() => readStoredTheme());
  const [resolvedTheme, setResolvedTheme] = useState<'light' | 'dark'>(() => resolveTheme(readStoredTheme()));
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
  const cloudflarePollRef = useRef<number | null>(null);
  // 5s __state 轮询会不断换新 publicAccess 对象（含 tunnel 状态）。用指纹跳过
  // 「仅运行时变化」的同步，避免正在编辑的子域名前缀被已保存域名盖回去。
  const publicAccessFormSyncRef = useRef('');
  const [tunnelBusy, setTunnelBusy] = useState(false);
  const [hostMetrics, setHostMetrics] = useState<HostMetrics | null>(null);
  // null = 尚未探测，避免刷新瞬间误闪「后端未连接」
  const [backendConnected, setBackendConnected] = useState<boolean | null>(null);
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
          await reconnectBackend(false);
          return;
        }
        if (auth && (!auth.requireAuth || auth.authenticated)) {
          void refreshState(false);
          void refreshRequestStats();
        }
      })();
    };
    const timer = window.setInterval(pollOnce, 5000);
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
          const response = await fetch(`${API_BASE}/__host-metrics`, { credentials: 'include' });
          if (!response.ok || cancelled) return;
          const data = await response.json() as HostMetrics;
          if (!cancelled) setHostMetrics(data);
        } catch {
          // ignore transient errors; next tick retries
        }
      })();
    };
    tick();
    timer = window.setInterval(tick, 5000);
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
    }, 5000);
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
      const response = await fetch(`${API_BASE}/__auth/status`, { credentials: 'same-origin' });
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

  async function resetConsoleUserPassword(user: ConsoleUser) {
    const password = window.prompt(`为用户「${user.username}」设置新密码（至少 8 位）：`);
    if (password == null) return;
    if (password.trim().length < 8) {
      showToast('密码至少 8 位');
      return;
    }
    try {
      const response = await fetch(`${API_BASE}/__users/${encodeURIComponent(user.id)}/reset-password`, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password: password.trim() }),
      });
      if (!response.ok) throw new Error(await response.text());
      showToast('密码已重置');
    } catch (error) {
      showToast(`重置失败：${String(error)}`);
    }
  }

  async function refreshBackendHealth() {
    try {
      const response = await fetch(`${API_BASE}/__health`);
      const body = await response.text();
      const connected = response.ok && body.includes('"status":"ok"');
      setBackendConnected(connected);
      return connected;
    } catch {
      setBackendConnected(false);
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
      const response = await fetch(`${API_BASE}/__state`, { credentials: 'same-origin' });
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
      setBackendConnected(true);
      setDataFetchedAt(new Date());
      writeUICache(uiCacheScope(authStatusRef.current), 'state', normalized);
      if (toast) showToast('已刷新后端状态和模型列表');
      return true;
    } catch (error) {
      setBackendConnected(false);
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
        const result = await response.json() as RouteTestResult;
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
        const result = await mainResponse.json() as RouteTestResult;
        if (stillCurrent()) setChatTestResult(result);
        if (supportsExtraTests) {
          const [cacheResponse, thinkingResponse] = await Promise.all([
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
          const cacheResult = await cacheResponse.json() as ProviderCacheTestResult;
          const thinkingResult = await thinkingResponse.json() as ProviderThinkingTestResult;
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
    // 普通用户只读 Provider 页：禁止调用获取模型接口。
    if (authStatus?.role === 'user') {
      showToast('普通用户无权获取模型');
      return;
    }
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
      const data = await response.json();
      if (!response.ok) throw new Error(data?.error?.message || data?.error || 'connect failed');
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
      const data = await response.json();
      if (!response.ok) throw new Error(data?.error?.message || data?.error || 'connect failed');
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
      const data = await response.json();
      if (!response.ok) throw new Error(data?.error?.message || data?.error || 'connect failed');
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
      if (!response.ok) throw new Error(await response.text());
      resetQoderPATFlowState();
      showToast('已断开 Qoder 账号连接');
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
    const usedByKeys = (state.apiKeys || []).filter((key) => apiKeyReferencesProvider(key, state.routes, providerID));
    if (usedByKeys.length > 0) {
      showToast(`无法删除：${providerName} 正被 ${usedByKeys.length} 个 API 密钥引用（含备选）`);
      return;
    }
    const usedBy = state.routes.filter((route) => route.providerId === providerID);
    if (usedBy.length > 0) {
      showToast(`无法删除：${providerName} 正被 ${usedBy.map((route) => route.name).join(', ')} 使用`);
      return;
    }
    if (!window.confirm(`确定删除输入 Provider：${providerName}？`)) return;
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
      <div className="shell">
        <aside className="sidebar">
          <div className="brand">
            <div className="brand-logo">PG</div>
            <div>
              <div className="brand-title">协议网关</div>
            </div>
            <a
              className="brand-github"
              href="https://github.com/yangyongyongyong/llm-protocol-gateway"
              target="_blank"
              rel="noreferrer"
              title="GitHub · yangyongyongyong/llm-protocol-gateway"
              aria-label="打开 GitHub 仓库"
            >
              <svg viewBox="0 0 16 16" width="20" height="20" fill="currentColor" aria-hidden="true">
                <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27s1.36.09 2 .27c1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8z" />
              </svg>
            </a>
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

          <ThemeSwitch value={themeMode} onChange={setThemeMode} size="compact" />

          <nav className="nav">
            {visibleNavItems.map((item) => {
              const index = navItems.findIndex((navItem) => navItem.id === item.id);
              return (
              <a
                className={`nav-item ${coreNavIDs.includes(item.id) ? 'core' : ''} ${activeNav === item.id ? 'active' : ''}`}
                href={navPathForID(item.id)}
                key={item.id}
                onClick={(event) => {
                  event.preventDefault();
                  goToPage(item.id);
                }}
              >
                <span className="nav-icon">{navIcons[index]}</span>
                <span className="nav-label">{item.label}</span>
                {item.id === 'public-access' ? (
                  <span
                    className={`nav-status-dot ${tunnelRunning ? 'on' : 'off'}`}
                    title={tunnelRunning ? '公网隧道已开启' : '公网隧道已关闭'}
                  />
                ) : null}
              </a>
              );
            })}
          </nav>
          {authStatus?.authenticated && authStatus.username ? (
            <div className="hint-line" style={{ padding: '0 12px' }}>
              {authStatus.role === 'user' ? '用户' : '管理员'}：{authStatus.username}
            </div>
          ) : null}
          {dataFetchedAt ? (
            <div className="hint-line" style={{ padding: '0 12px' }} title="页面数据最近一次成功拉取的时间（约每 5 秒自动刷新）">
              数据更新于 {dataFetchedAt.toLocaleTimeString()}
            </div>
          ) : null}
          {authStatus?.requireAuth ? (
            <button className="btn" type="button" disabled={authBusy} onClick={() => void logoutAdmin()}>
              退出登录
            </button>
          ) : null}
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
                      placeholder="名称 / 密钥"
                      value={apiKeyKeyword}
                      onChange={(event) => setApiKeyKeyword(event.target.value)}
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
                      ? '展示管理员授权给你的 Provider（只读）以及你自己创建的 Provider（可编辑/克隆/删除/对话测试/获取模型）。删除前会检查是否被 API 密钥引用。'
                      : '用户自定义添加的上游 Provider。删除前会检查是否被 API 密钥引用。列表按近 3 日请求量排序。支持勾选后导出/导入配置（含 apiKeySource 与已持久化的 OAuth 元数据）。'}
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
                  <input
                    className="models-search-input"
                    type="search"
                    value={providersSearchQuery}
                    placeholder="按名称 / ID / 地址检索，支持正则，如 tuya|claude"
                    onChange={(event) => setProvidersSearchQuery(event.target.value)}
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
                <div className="models-toolbar-meta">
                  显示 {filteredProviders.length} / {sortedProviders.length}
                  {providersSearchQuery.trim() ? ` · 检索「${providersSearchQuery.trim()}」` : ''}
                  {providersConnectFilter ? ` · ${providerConnectLabel(providersConnectFilter)}` : ''}
                </div>
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
                    <span>全选当前结果</span>
                  </label>
                  <span className="providers-toolbar-meta">已选 {selectedExportProviderIDs.length} / {sortedProviders.length}</span>
                  {selectedExportProviderIDs.length > 0 ? (
                    <button className="mini-btn" type="button" onClick={clearExportProviderSelection}>清除选择</button>
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
                        url={provider.authType === 'claude_oauth' ? 'Claude OAuth (api.anthropic.com)' : provider.authType === 'cursor_oauth' ? 'Cursor OAuth (本地 gRPC bridge)' : provider.authType === 'chatgpt_oauth' ? 'ChatGPT OAuth (chatgpt.com/codex)' : provider.authType === 'qoder_pat' ? `Qoder PAT (${provider.baseUrl})` : `${provider.baseUrl} · ${maskApiKeySource(provider.apiKeySource)}`}
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
                          || (provider.authType === 'chatgpt_oauth' && provider.chatgptOAuth?.accountLabel)
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
              {selectedProvider && !isNormalUser ? <div className="hint-line">当前 Provider 被 {activeProviderRouteCount} 个 API 密钥引用（含备选）。引用数不为 0 时不能删除。</div> : null}
            </div>
          </section>
          )}

          {activeNav === 'output-providers' && !isNormalUser && (
          <section className="section-grid">
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
                    value={modelsSearchQuery}
                    placeholder="按名称检索，支持正则，如 gpt-5\\.6|sonnet"
                    onChange={(event) => setModelsSearchQuery(event.target.value)}
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

                  <label className="toggle-row" style={{ display: 'flex', alignItems: 'center', gap: 12, cursor: 'pointer', margin: '16px 0 12px' }}>
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

                  <label className="toggle-row" style={{ display: 'flex', alignItems: 'center', gap: 12, cursor: 'pointer', margin: '16px 0 12px' }}>
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
                    const providerNames = (user.allowedProviderIds || [])
                      .map((id) => state.providers.find((provider) => provider.id === id)?.name || id);
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
                            <button className="mini-btn" type="button" onClick={() => void resetConsoleUserPassword(user)}>重置密码</button>
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

              <div className="hint-line" style={{ marginBottom: 12 }}>
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
                <div className="machine-fact"><span>交换分区</span><b>{hostMetrics?.swapTotal ? `${formatBytes(hostMetrics.swapUsed)} / ${formatBytes(hostMetrics.swapTotal)}` : '未启用'}</b></div>
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
                : 'API Key';
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
                    values={['API Key', '登录 Claude 账号 (OAuth)', '登录 Cursor 账号 (OAuth)', '登录 ChatGPT 账号 (OAuth)', '连接 Qoder 账号 (PAT)', SELF_REGISTER_CONNECT_LABEL]}
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
            <Field label="兜底模型（可选）" value={providerDraft.defaultModel} onChange={(value) => setProviderDraft((current) => ({ ...current, defaultModel: value }))} />
            <div className="field"><label>默认思考深度（可选）</label><select value={providerDraft.defaultThinkingDepth} onChange={(event) => setProviderDraft((current) => ({ ...current, defaultThinkingDepth: event.target.value }))}>
              {thinkingDepthSelectOptions({ value: '', label: '（不指定）' })}
            </select></div>
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
                      <div className="hint-line">已连接{editingProvider?.claudeOAuth?.accountLabel ? ` · ${editingProvider.claudeOAuth.accountLabel}` : ''}{editingProvider?.claudeOAuth?.expiresAt ? ` · 过期时间：${editingProvider.claudeOAuth.expiresAt}` : ''}</div>
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
                      <div className="hint-line">已连接{editingProvider?.cursorOAuth?.accountLabel ? ` · ${editingProvider.cursorOAuth.accountLabel}` : ''}{editingProvider?.cursorOAuth?.expiresAt ? ` · 过期时间：${editingProvider.cursorOAuth.expiresAt}` : ''} · 模型 {editingProvider?.models?.length ?? 0} 个</div>
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
                      <div className="hint-line">已连接{editingProvider?.chatgptOAuth?.accountLabel ? ` · ${editingProvider.chatgptOAuth.accountLabel}` : ''}{editingProvider?.chatgptOAuth?.expiresAt ? ` · 过期时间：${editingProvider.chatgptOAuth.expiresAt}` : ''} · 模型 {editingProvider?.models?.length ?? 0} 个</div>
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
                return (
                  <>
                    <div className="hint-line">在 qoder.com/account/integrations 生成个人访问令牌（pt- 开头）后粘贴到这里。令牌只用来兑换短期作业令牌，不会回传到浏览器。</div>
                    <div className="field-inline" style={{ marginTop: 8 }}>
                      <input type="password" placeholder="粘贴 pt- 开头的个人访问令牌" value={qoderPatInput} onChange={(event) => setQoderPatInput(event.target.value)} />
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

function RouteCard({ active, name, tone, status, meta, flow, onClick, onTest, onEdit, onClone, onDelete }: { active?: boolean; name: string; tone: BadgeTone; status: string; meta: string; flow: string[]; onClick: () => void; onTest: () => void; onEdit: () => void; onClone: () => void; onDelete: () => void }) {
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

function formatClaudeUsageResetAt(resetsAt?: string) {
  if (!resetsAt) return '—';
  const date = new Date(resetsAt);
  if (Number.isNaN(date.getTime())) return resetsAt;
  return date.toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' });
}

function claudeUsageFillTone(utilization: number): string {
  if (utilization >= 90) return 'danger';
  if (utilization >= 70) return 'warn';
  return 'ok';
}

// OAuth usage panels: poll only while the browser tab is visible, and keep the
// interval conservative so backgrounded / multi-provider UIs don't hammer
// Anthropic / Cursor quota APIs.
const OAUTH_USAGE_POLL_MS = 3 * 60_000;
const OAUTH_USAGE_STORAGE_PREFIX = 'oauth-usage:';

function readOAuthUsageCache<T>(path: string): T | null {
  try {
    const raw = sessionStorage.getItem(`${OAUTH_USAGE_STORAGE_PREFIX}${path}`);
    if (!raw) return null;
    return JSON.parse(raw) as T;
  } catch {
    return null;
  }
}

function writeOAuthUsageCache(path: string, data: unknown) {
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
function isDocumentActive(): boolean {
  if (typeof document === 'undefined') return true;
  return document.visibilityState !== 'hidden' && document.hasFocus();
}

function usePageVisible() {
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

function useOAuthUsageReport<T extends { available?: boolean; error?: string }>(
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

function ClaudeOAuthUsagePanel({ providerId, connected, compact }: { providerId: string; connected?: boolean; compact?: boolean }) {
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
    <div className={`claude-usage-panel${compact ? ' compact' : ''}`} onClick={(event) => event.stopPropagation()}>
      <div className="claude-usage-title">
        <span>Claude 订阅额度</span>
        <span className="claude-usage-actions">
          {loading ? <span className="claude-usage-status">刷新中…</span> : report?.fetchedAt ? <span className="claude-usage-status">更新于 {formatClaudeUsageResetAt(report.fetchedAt)}</span> : null}
          <button
            type="button"
            className="btn btn-tiny"
            disabled={loading}
            onClick={(event) => {
              event.stopPropagation();
              void refresh();
            }}
          >
            刷新
          </button>
        </span>
      </div>
      {!report ? (
        <div className="claude-usage-empty">{loading ? '正在拉取额度…' : '暂无额度数据'}</div>
      ) : !report.available ? (
        <div className="claude-usage-empty error">{report.error || '额度不可用'}</div>
      ) : buckets.length === 0 ? (
        <div className="claude-usage-empty">未返回额度桶数据</div>
      ) : (
        <div className="claude-usage-grid">
          {buckets.map(({ key, label, bucket }) => {
            const percent = Math.min(100, Math.max(0, bucket?.utilization ?? 0));
            return (
              <div className="claude-usage-row" key={key}>
                <div className="claude-usage-head">
                  <span>{label}</span>
                  <span>{percent.toFixed(0)}%</span>
                </div>
                <div className="claude-usage-track">
                  <div className={`claude-usage-fill ${claudeUsageFillTone(percent)}`} style={{ width: `${percent}%` }} />
                </div>
                <div className="claude-usage-reset">重置：{formatClaudeUsageResetAt(bucket?.resets_at)}</div>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}

function ZhipuUsagePanel({ providerId, compact }: { providerId: string; compact?: boolean }) {
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
    <div className={`claude-usage-panel${compact ? ' compact' : ''}`} onClick={(event) => event.stopPropagation()}>
      <div className="claude-usage-title">
        <span>智谱编程套餐额度{report?.level ? ` · ${report.level}` : ''}</span>
        <span className="claude-usage-actions">
          {loading ? <span className="claude-usage-status">刷新中…</span> : report?.fetchedAt ? <span className="claude-usage-status">更新于 {formatClaudeUsageResetAt(report.fetchedAt)}</span> : null}
          <button
            type="button"
            className="btn btn-tiny"
            disabled={loading}
            onClick={(event) => {
              event.stopPropagation();
              void refresh();
            }}
          >
            刷新
          </button>
        </span>
      </div>
      {!report ? (
        <div className="claude-usage-empty">{loading ? '正在拉取额度…' : '暂无额度数据'}</div>
      ) : !report.available ? (
        <div className="claude-usage-empty">{report.error || '额度不可用'}</div>
      ) : buckets.length === 0 ? (
        <div className="claude-usage-empty">未返回额度桶数据（老套餐可能只有 5 小时窗口）</div>
      ) : (
        <div className="claude-usage-grid">
          {buckets.map(({ key, label, bucket }) => {
            const percent = Math.min(100, Math.max(0, bucket?.utilization ?? 0));
            return (
              <div className="claude-usage-row" key={key}>
                <div className="claude-usage-head">
                  <span>{label}</span>
                  <span>{percent.toFixed(0)}%</span>
                </div>
                <div className="claude-usage-track">
                  <div className={`claude-usage-fill ${claudeUsageFillTone(percent)}`} style={{ width: `${percent}%` }} />
                </div>
                <div className="claude-usage-reset">重置：{formatClaudeUsageResetAt(bucket?.resets_at)}</div>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}

/** DeepSeek 账户余额面板。与 Zhipu 同为 api_key provider，复用同一套额度面板样式；
 *  但展示的是金额而非百分比进度条，故不渲染 usage-track。 */
function DeepSeekBalancePanel({ providerId, compact }: { providerId: string; compact?: boolean }) {
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
    <div className={`claude-usage-panel${compact ? ' compact' : ''}`} onClick={(event) => event.stopPropagation()}>
      <div className="claude-usage-title">
        <span>DeepSeek 账户余额</span>
        <span className="claude-usage-actions">
          {loading ? <span className="claude-usage-status">刷新中…</span> : report?.fetchedAt ? <span className="claude-usage-status">更新于 {formatClaudeUsageResetAt(report.fetchedAt)}</span> : null}
          <button
            type="button"
            className="btn btn-tiny"
            disabled={loading}
            onClick={(event) => {
              event.stopPropagation();
              void refresh();
            }}
          >
            刷新
          </button>
        </span>
      </div>
      {!report ? (
        <div className="claude-usage-empty">{loading ? '正在拉取余额…' : '暂无余额数据'}</div>
      ) : !report.available ? (
        <div className="claude-usage-empty error">{report.error || '余额不可用'}</div>
      ) : infos.length === 0 ? (
        <div className="claude-usage-empty">未返回余额数据</div>
      ) : (
        <div className="claude-usage-grid">
          {infos.map((info) => (
            <div className="claude-usage-row" key={info.currency}>
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
    </div>
  );
}

/** 金额按原始字符串展示（不转 number，避免精度问题），仅补货币符号。 */
function formatDeepSeekAmount(currency: string, amount?: string) {
  const value = (amount || '').trim();
  if (!value) return '—';
  const symbol = currency === 'CNY' ? '¥' : currency === 'USD' ? '$' : '';
  return `${symbol}${value}`;
}

function CursorOAuthUsagePanel({ providerId, connected, compact }: { providerId: string; connected?: boolean; compact?: boolean }) {
  const { report, loading, refresh } = useOAuthUsageReport<CursorOAuthUsageReport>(
    Boolean(connected),
    `/__providers/${encodeURIComponent(providerId)}/cursor-oauth/usage`,
  );

  if (!connected) return null;

  const buckets = report?.buckets || [];

  return (
    <div className={`claude-usage-panel cursor-usage-panel${compact ? ' compact' : ''}`} onClick={(event) => event.stopPropagation()}>
      <div className="claude-usage-title">
        <span>Cursor 订阅额度{report?.planName ? ` · ${report.planName}` : ''}</span>
        <span className="claude-usage-actions">
          {loading ? <span className="claude-usage-status">刷新中…</span> : report?.fetchedAt ? <span className="claude-usage-status">更新于 {formatClaudeUsageResetAt(report.fetchedAt)}</span> : null}
          <button
            type="button"
            className="btn btn-tiny"
            disabled={loading}
            onClick={(event) => {
              event.stopPropagation();
              void refresh();
            }}
          >
            刷新
          </button>
        </span>
      </div>
      {!report ? (
        <div className="claude-usage-empty">{loading ? '正在拉取额度…' : '暂无额度数据'}</div>
      ) : !report.available ? (
        <div className="claude-usage-empty error">{report.error || '额度不可用'}</div>
      ) : buckets.length === 0 ? (
        <div className="claude-usage-empty">未返回额度桶数据</div>
      ) : (
        <div className="claude-usage-grid">
          {buckets.map((bucket, index) => {
            const percent = Math.min(100, Math.max(0, bucket.utilization ?? 0));
            return (
              <div className="claude-usage-row" key={`${bucket.label}-${index}`}>
                <div className="claude-usage-head">
                  <span>{bucket.label}</span>
                  <span>{percent.toFixed(0)}%</span>
                </div>
                <div className="claude-usage-track">
                  <div className={`claude-usage-fill ${claudeUsageFillTone(percent)}`} style={{ width: `${percent}%` }} />
                </div>
                {bucket.detail ? <div className="claude-usage-reset">{bucket.detail}</div> : null}
                <div className="claude-usage-reset">重置：{formatClaudeUsageResetAt(bucket.resetsAt)}</div>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}

function ChatGPTOAuthUsagePanel({ providerId, connected, compact }: { providerId: string; connected?: boolean; compact?: boolean }) {
  const { report, loading, refresh } = useOAuthUsageReport<ChatGPTOAuthUsageReport>(
    Boolean(connected),
    `/__providers/${encodeURIComponent(providerId)}/chatgpt-oauth/usage`,
  );

  if (!connected) return null;

  const buckets = report?.buckets || [];

  return (
    <div className={`claude-usage-panel chatgpt-usage-panel${compact ? ' compact' : ''}`} onClick={(event) => event.stopPropagation()}>
      <div className="claude-usage-title">
        <span>ChatGPT Codex 额度{report?.planName ? ` · ${report.planName}` : ''}</span>
        <span className="claude-usage-actions">
          {loading ? <span className="claude-usage-status">刷新中…</span> : report?.fetchedAt ? <span className="claude-usage-status">更新于 {formatClaudeUsageResetAt(report.fetchedAt)}</span> : null}
          <button
            type="button"
            className="btn btn-tiny"
            disabled={loading}
            onClick={(event) => {
              event.stopPropagation();
              void refresh();
            }}
          >
            刷新
          </button>
        </span>
      </div>
      {!report ? (
        <div className="claude-usage-empty">{loading ? '正在拉取额度…' : '暂无额度数据'}</div>
      ) : !report.available ? (
        <div className="claude-usage-empty error">{report.error || '额度不可用'}</div>
      ) : buckets.length === 0 ? (
        <div className="claude-usage-empty">{report.message || '未返回额度桶数据'}</div>
      ) : (
        <div className="claude-usage-grid">
          {report.message ? <div className="claude-usage-reset">{report.message}</div> : null}
          {buckets.map((bucket, index) => {
            const percent = Math.min(100, Math.max(0, bucket.utilization ?? 0));
            return (
              <div className="claude-usage-row" key={`${bucket.label}-${index}`}>
                <div className="claude-usage-head">
                  <span>{bucket.label}</span>
                  <span>{percent.toFixed(0)}%</span>
                </div>
                <div className="claude-usage-track">
                  <div className={`claude-usage-fill ${claudeUsageFillTone(percent)}`} style={{ width: `${percent}%` }} />
                </div>
                {bucket.detail ? <div className="claude-usage-reset">{bucket.detail}</div> : null}
                {bucket.resetsAt ? <div className="claude-usage-reset">重置：{formatClaudeUsageResetAt(bucket.resetsAt)}</div> : null}
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}

function ProviderCard({ active, selected, name, providerId, protocol, tone, url, usedCount, healthStatus, nextRetryAt, testing, chatTesting, readOnly, selectable, providerDisabled, onToggleEnabled, subtitle, isClaudeOAuth, claudeOAuthConnected, isCursorOAuth, cursorOAuthConnected, isChatGPTOAuth, chatgptOAuthConnected, isQoderPAT, qoderPATConnected, cursorBridge, authorizedUserCount, onShowUsers, onToggleSelect, onClick, onTest, onChatTest, onConformance, onEdit, onClone, onDelete }: { active?: boolean; selected?: boolean; name: string; providerId: string; protocol: string; tone: BadgeTone; url: string; usedCount: number; healthStatus: string; nextRetryAt?: string; testing: boolean; chatTesting?: boolean; readOnly?: boolean; selectable?: boolean; providerDisabled?: boolean; onToggleEnabled?: () => void; subtitle?: string; isClaudeOAuth?: boolean; claudeOAuthConnected?: boolean; isCursorOAuth?: boolean; cursorOAuthConnected?: boolean; isChatGPTOAuth?: boolean; chatgptOAuthConnected?: boolean; isQoderPAT?: boolean; qoderPATConnected?: boolean; cursorBridge?: CursorBridgeRuntime; authorizedUserCount?: number; onShowUsers?: () => void; onToggleSelect: () => void; onClick: () => void; onTest: () => void; onChatTest: () => void; onConformance?: () => void; onEdit: () => void; onClone: () => void; onDelete: () => void }) {
  const oauthConnected = isClaudeOAuth ? claudeOAuthConnected : isCursorOAuth ? cursorOAuthConnected : isChatGPTOAuth ? chatgptOAuthConnected : isQoderPAT ? qoderPATConnected : false;
  const showOAuthBadge = isClaudeOAuth || isCursorOAuth || isChatGPTOAuth || isQoderPAT;
  const isUnavailable = healthStatus === 'unavailable';
  const now = useNowTick(isUnavailable && !!nextRetryAt);
  const retryLabel = isUnavailable ? retrySecondsLabel(nextRetryAt, now) : null;
  const bridgeHint = cursorBridge?.port
    ? ` · :${cursorBridge.port}${cursorBridge.message ? ` · ${cursorBridge.message}` : ''}`
    : (cursorBridge?.message ? ` · ${cursorBridge.message}` : '');
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
          <div className="provider-subtitle">{subtitle || `${providerId} · ${protocol}`}</div>
        </div>
        <div className="provider-badges">
          {providerDisabled ? <Badge tone="red">已禁用</Badge> : null}
          <Badge tone={tone}>{protocol}</Badge>
          {showOAuthBadge ? (
            <Badge tone={oauthConnected ? 'green' : 'amber'}>{oauthConnected ? 'OAuth 已连接' : 'OAuth 未连接'}</Badge>
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
          <Badge tone={usedCount > 0 ? 'amber' : 'slate'}>{usedCount} 个 API Key</Badge>
          {onShowUsers && authorizedUserCount != null ? (
            <span
              className="provider-users-badge"
              role="button"
              tabIndex={0}
              title="查看/管理有权限使用该 Provider 的用户（仅管理员）"
              onClick={(event) => { event.stopPropagation(); onShowUsers(); }}
              onKeyDown={(event) => { if (event.key === 'Enter') { event.stopPropagation(); onShowUsers(); } }}
            >
              <Badge tone={authorizedUserCount > 0 ? 'blue' : 'slate'}>{authorizedUserCount} 个用户</Badge>
            </span>
          ) : null}
        </div>
      </div>
      <div className="provider-meta">{url}{isCursorOAuth && cursorBridge?.port ? ` · 127.0.0.1:${cursorBridge.port}` : ''}</div>
      {isClaudeOAuth && claudeOAuthConnected && !providerDisabled ? <ClaudeOAuthUsagePanel providerId={providerId} connected={claudeOAuthConnected} compact /> : null}
      {isCursorOAuth && cursorOAuthConnected && !providerDisabled ? <CursorOAuthUsagePanel providerId={providerId} connected={cursorOAuthConnected} compact /> : null}
      {isChatGPTOAuth && chatgptOAuthConnected && !providerDisabled ? <ChatGPTOAuthUsagePanel providerId={providerId} connected={chatgptOAuthConnected} compact /> : null}
      {!providerDisabled && !isClaudeOAuth && !isCursorOAuth && !isChatGPTOAuth && !isQoderPAT && /(?:bigmodel\.cn|z\.ai)/i.test(url) ? <ZhipuUsagePanel providerId={providerId} compact /> : null}
      {!providerDisabled && !isClaudeOAuth && !isCursorOAuth && !isChatGPTOAuth && !isQoderPAT && /deepseek\.com/i.test(url) ? <DeepSeekBalancePanel providerId={providerId} compact /> : null}
      {!readOnly ? (
        <div className="provider-actions">
          <button className="icon-btn" disabled={testing} onClick={(event) => { event.stopPropagation(); onTest(); }} title="从 Provider 接口获取可用模型">{testing ? '获取中' : '获取模型'}</button>
          <button className="icon-btn" disabled={!!chatTesting} onClick={(event) => { event.stopPropagation(); onChatTest(); }} title="直连上游对话接口测试">{chatTesting ? '测试中' : '对话测试'}</button>
          <button className="icon-btn" onClick={(event) => { event.stopPropagation(); onEdit(); }} title="编辑 Provider">编辑</button>
          <button className="icon-btn" onClick={(event) => { event.stopPropagation(); onClone(); }} title="克隆为新 Provider">克隆</button>
          <button className="icon-btn danger" disabled={usedCount > 0} onClick={(event) => { event.stopPropagation(); onDelete(); }} title={usedCount > 0 ? '该 Provider 正被 API Key 引用' : '删除 Provider'}>删除</button>
          {onToggleEnabled ? (
            <button
              className={`icon-btn${providerDisabled ? '' : ' danger'}`}
              onClick={(event) => { event.stopPropagation(); onToggleEnabled(); }}
              title={providerDisabled ? '启用后普通用户恢复可用' : '禁用后普通用户不可见、不可绑定、请求被拒绝（管理员不受影响）'}
            >
              {providerDisabled ? '启用' : '禁用'}
            </button>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

function Field({ label, value, onChange, placeholder, fullWidth }: { label: string; value: string; onChange: (value: string) => void; placeholder?: string; fullWidth?: boolean }) {
  return <div className={`field${fullWidth ? ' field-full' : ''}`}><label>{label}</label><input value={value} placeholder={placeholder} onChange={(event) => onChange(event.target.value)} /></div>;
}

function ApiKeyDetailPanel({
  keyItem,
  providers,
  routes,
  models,
  endpoints,
  saving,
  testingProviderID,
  tunnelRunning,
  livePublicURL,
  fixedOutputLabels,
  onUpdateField,
  onUpdateBinding,
  onUpdateModelAliases,
  onUpdateFallbacks,
  onDelete,
  onClone,
  onRefreshModels,
  onToast,
  owners,
  onUpdateOwner,
  onSwitchProfile,
  onCreateProfile,
  onUpdateProfile,
  onDeleteProfile,
}: {
  keyItem: APIKey;
  providers: Provider[];
  routes: Route[];
  models: Model[];
  endpoints: OutputEndpoint[];
  saving: boolean;
  testingProviderID: string;
  tunnelRunning: boolean;
  livePublicURL: string;
  fixedOutputLabels: string[];
  onUpdateField: (key: APIKey, field: 'name' | 'routeId' | 'modelOverride' | 'thinkingDepthOverride' | 'maxOutputTokens' | 'streamEnabled' | 'codexKeepOfficialLogin' | 'enabled', value: string | boolean | number) => Promise<void>;
  onUpdateBinding: (key: APIKey, providerId: string, outputProtocol: Protocol) => Promise<void>;
  onUpdateModelAliases: (key: APIKey, modelAliases: Record<string, string>) => Promise<void>;
  onUpdateFallbacks: (key: APIKey, fallbackProviderIds: string[], fallbackModelOverrides: Record<string, string>) => Promise<void>;
  onDelete: (key: APIKey) => Promise<void>;
  onClone: (key: APIKey) => void;
  onRefreshModels: (providerId: string, providerName: string) => Promise<void>;
  onToast?: (message: string) => void;
  // 用户归属（仅管理员可见/可改）；owners 为空时不渲染
  owners?: ConsoleUser[];
  onUpdateOwner?: (key: APIKey, ownerUserId: string) => Promise<void>;
  // 转发方案（多套配置 + 一键切换）
  onSwitchProfile: (key: APIKey, profileId: string) => Promise<void>;
  onCreateProfile: (key: APIKey, profile: Partial<KeyProfile>, activate: boolean) => Promise<void>;
  onUpdateProfile: (key: APIKey, profileId: string, profile: Partial<KeyProfile>) => Promise<void>;
  onDeleteProfile: (key: APIKey, profileId: string) => Promise<void>;
}) {
  const { route, binding, routeProvider, bindingAction } = getApiKeyBinding(keyItem, routes, providers);
  const modelOptions = routeProvider ? models.filter((model) => model.providerId === routeProvider.id) : [];
  const publicAvailable = Boolean(tunnelRunning && livePublicURL);
  const defaultPublicBase = publicAvailable ? livePublicURL : '';
  const apiKeyClientURL = route ? apiKeyClientBaseURL(route, endpoints, defaultPublicBase) : '';
  const [clientConfigModal, setClientConfigModal] = React.useState<'opencode' | 'codex' | 'claude' | null>(null);
  const [fallbackModalOpen, setFallbackModalOpen] = React.useState(false);
  const profiles = keyItem.profiles || [];
  const activeProfileId = keyItem.activeProfileId || '';
  const activeProfile = profiles.find((item) => item.id === activeProfileId);
  const fallbackIds = keyItem.fallbackProviderIds || [];
  const fallbackModelOverrides = keyItem.fallbackModelOverrides || {};
  const activeProviderId = keyItem.activeProviderId || binding.providerId;
  const activeProvider = providers.find((item) => item.id === activeProviderId);
  const usingFallback = Boolean(keyItem.activeProviderId && keyItem.activeProviderId !== binding.providerId);
  const activeFallbackModel = usingFallback ? (fallbackModelOverrides[keyItem.activeProviderId || ''] || '') : '';

  function openClientConfigModal(client: 'opencode' | 'codex' | 'claude') {
    setClientConfigModal(client);
  }

  // 新建方案 = 完整克隆当前 Key 顶层转发配置（备选 / 模型映射等一并带上）。
  function snapshotCurrentProfile(name: string): Partial<KeyProfile> {
    return {
      name,
      routeId: keyItem.routeId,
      modelOverride: keyItem.modelOverride,
      modelAliases: { ...(keyItem.modelAliases || {}) },
      thinkingDepthOverride: keyItem.thinkingDepthOverride,
      maxOutputTokens: keyItem.maxOutputTokens && keyItem.maxOutputTokens > 0 ? keyItem.maxOutputTokens : 0,
      fallbackProviderIds: [...(keyItem.fallbackProviderIds || [])],
      fallbackModelOverrides: { ...(keyItem.fallbackModelOverrides || {}) },
      streamEnabled: keyItem.streamEnabled !== false,
    };
  }

  async function handleCreateProfile() {
    const name = window.prompt('新方案名称（将完整复制当前转发配置）', '');
    if (name == null) return;
    const trimmed = name.trim();
    if (!trimmed) {
      onToast?.('请填写方案名称');
      return;
    }
    await onCreateProfile(keyItem, snapshotCurrentProfile(trimmed), true);
  }

  async function handleRenameProfile() {
    if (!activeProfile) return;
    const name = window.prompt('重命名当前方案', activeProfile.name);
    if (name == null) return;
    const trimmed = name.trim();
    if (!trimmed || trimmed === activeProfile.name) return;
    await onUpdateProfile(keyItem, activeProfile.id, { ...activeProfile, name: trimmed });
  }

  async function handleDeleteProfile() {
    if (!activeProfile) return;
    if (profiles.length <= 1) {
      onToast?.('至少保留一套方案，或先新建再删除');
      return;
    }
    if (!window.confirm(`删除方案「${activeProfile.name}」？删除后将切换到其余方案之一。`)) return;
    await onDeleteProfile(keyItem, activeProfile.id);
  }

  return (
    <div className="api-keys-detail card">
      <div className="route-top api-keys-detail-head">
        <div className="route-name">{keyItem.name}</div>
        <div className="route-actions">
          <Badge tone={keyItem.enabled ? 'green' : 'slate'}>{keyItem.enabled ? '启用' : '禁用'}</Badge>
          <Badge tone={bindingAction === '透传' ? 'green' : 'cyan'}>{bindingAction}</Badge>
          {usingFallback ? <Badge tone="amber">已切备选</Badge> : null}
          {route ? <CopyButton value={apiKeyClientURL} label="复制 URL" toastContent={`已复制 URL：${apiKeyClientURL}`} /> : null}
          <CopyButton value={keyItem.key} label="复制 Key" toastContent={`已复制 Key：${keyItem.key}`} />
          <button className="icon-btn" onClick={() => onClone(keyItem)} title="克隆为新 API 密钥">克隆</button>
          <button className="icon-btn danger" onClick={() => void onDelete(keyItem)} title="删除">删除</button>
        </div>
      </div>

      <div className="api-key-profile-selector">
        <div className="field-label-row">
          <label>转发方案</label>
          <div className="api-key-profile-toolbar">
            <button className="btn" type="button" disabled={saving} onClick={() => void handleCreateProfile()}>
              新建方案
            </button>
            {activeProfile ? (
              <button className="btn" type="button" disabled={saving} onClick={() => void handleRenameProfile()}>
                重命名
              </button>
            ) : null}
            {activeProfile && profiles.length > 1 ? (
              <button className="btn danger" type="button" disabled={saving} onClick={() => void handleDeleteProfile()}>
                删除当前
              </button>
            ) : null}
          </div>
        </div>
        <div className="hint-line">
          同一 Key（token 不变）可切换多套完整转发配置。下方整页表单即当前生效方案（Provider / 备选 / 模型映射等）；切换方案后新请求立即走新配置。
        </div>
        <div className="api-key-profile-tabs" role="tablist" aria-label="转发方案">
          {profiles.length === 0 ? (
            <button className="api-key-profile-tab active" type="button" role="tab" aria-selected="true" disabled>
              当前配置
            </button>
          ) : (
            profiles.map((profile) => {
              const isActive = profile.id === activeProfileId;
              return (
                <button
                  key={profile.id}
                  className={`api-key-profile-tab${isActive ? ' active' : ''}`}
                  type="button"
                  role="tab"
                  aria-selected={isActive}
                  disabled={saving || isActive}
                  onClick={() => void onSwitchProfile(keyItem, profile.id)}
                  title={isActive ? '当前生效方案' : `切换到「${profile.name}」`}
                >
                  {profile.name}
                  {isActive ? <span className="api-key-profile-tab-mark">生效</span> : null}
                </button>
              );
            })
          )}
        </div>
      </div>

      <div className="form-grid compact">
        <ApiKeyNameField
          name={keyItem.name}
          disabled={saving}
          onSave={(name) => onUpdateField(keyItem, 'name', name)}
        />
        <div className="field">
          <label>输入 Provider（首选）</label>
          <select
            value={binding.providerId}
            disabled={saving}
            onChange={(event) => {
              void onUpdateBinding(keyItem, event.target.value, binding.outputProtocol);
            }}
          >
            {providers.map((item) => <option key={item.id} value={item.id}>{providerOptionLabel(item)}</option>)}
          </select>
        </div>
        <div className="field">
          <label>输入协议</label>
          <div className="field-readonly">
            {routeProvider ? protocolLabel(routeProvider.protocol) : '未绑定'}
          </div>
        </div>
        {owners && onUpdateOwner ? (
          <div className="field">
            <label>所属用户</label>
            <select
              value={keyItem.ownerUserId || ''}
              disabled={saving}
              onChange={(event) => void onUpdateOwner(keyItem, event.target.value)}
            >
              <option value="">管理员</option>
              {owners.map((user) => <option key={user.id} value={user.id}>{user.username}</option>)}
            </select>
          </div>
        ) : null}
        <div className="field">
          <label>输出协议</label>
          <select
            value={protocolLabel(binding.outputProtocol)}
            disabled={saving || !binding.providerId}
            onChange={(event) => {
              void onUpdateBinding(keyItem, binding.providerId, protocolFromLabel(event.target.value));
            }}
          >
            {fixedOutputLabels.map((label) => <option key={label} value={label}>{label}</option>)}
          </select>
        </div>
        <div className="field field-full">
          <label>备选 Provider</label>
          <div className="api-key-fallback-summary">
            <div className="hint-line">
              当前在用：{activeProvider ? providerOptionLabel(activeProvider) : '未绑定'}
              {usingFallback ? `（已从首选故障转移${activeFallbackModel ? ` · 模型 ${activeFallbackModel}` : ''}）` : ''}
            </div>
            <div className="hint-line">
              {fallbackIds.length === 0
                ? '未配置备选。首选额度耗尽后将无法自动切换。'
                : `备选顺序：${fallbackIds.map((id, index) => {
                  const provider = providers.find((item) => item.id === id);
                  const model = fallbackModelOverrides[id] || '未选模型';
                  return `${index + 1}. ${provider ? providerOptionLabel(provider) : id} → ${model}`;
                }).join(' ； ')}`}
            </div>
            <button className="btn" type="button" disabled={saving || !binding.providerId || providers.length < 2} onClick={() => setFallbackModalOpen(true)}>
              配置备选
            </button>
          </div>
        </div>
        <ApiKeyFixedModelField
          value={keyItem.modelOverride || ''}
          models={modelOptions}
          disabled={saving || !routeProvider}
          refreshing={routeProvider ? testingProviderID === routeProvider.id : false}
          onChange={(value) => void onUpdateField(keyItem, 'modelOverride', value)}
          onRefresh={() => routeProvider ? void onRefreshModels(routeProvider.id, routeProvider.name) : undefined}
        />
        <div className="field">
          <label>思考深度</label>
          <select value={keyItem.thinkingDepthOverride || ''} disabled={saving} onChange={(event) => void onUpdateField(keyItem, 'thinkingDepthOverride', event.target.value)}>
            {thinkingDepthSelectOptions({ value: '', label: '（不覆盖）' })}
          </select>
        </div>
        <ApiKeyMaxOutputTokensField
          value={keyItem.maxOutputTokens && keyItem.maxOutputTokens > 0 ? keyItem.maxOutputTokens : 0}
          disabled={saving}
          onSave={(n) => onUpdateField(keyItem, 'maxOutputTokens', n)}
        />
        <div className="field">
          <label>流式响应（SSE）</label>
          <label className="hint-line" style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <input
              type="checkbox"
              checked={keyItem.streamEnabled !== false}
              disabled={saving}
              onChange={(event) => void onUpdateField(keyItem, 'streamEnabled', event.target.checked)}
            />
            允许流式响应（关闭后该 Key 的 stream:true 请求将被拒绝）
          </label>
        </div>
        <ApiKeyModelMappingControl
          aliases={keyItem.modelAliases || {}}
          models={modelOptions}
          providerName={routeProvider?.name || 'Provider'}
          disabled={saving || !routeProvider}
          saving={saving}
          refreshing={routeProvider ? testingProviderID === routeProvider.id : false}
          onRefresh={() => routeProvider ? void onRefreshModels(routeProvider.id, routeProvider.name) : undefined}
          onSave={(modelAliases) => onUpdateModelAliases(keyItem, modelAliases)}
        />
      </div>

      <div className="api-key-client-configs">
        <div className="field-label-row">
          <label>一键复制客户端配置</label>
        </div>
        <div className="hint-line">
          仅显示与当前输出协议匹配的客户端。点击后复制到剪贴板，并弹窗预览配置路径与内容；可在弹窗内切换内网 / 公网域名。
        </div>
        <div className="api-key-client-config-actions">
          {clientConfigsForProtocol(route?.outputProtocol).map((client) => (
            <button
              key={client}
              className="btn client-config-btn"
              type="button"
              disabled={!route}
              onClick={() => openClientConfigModal(client)}
            >
              {client === 'opencode' ? '复制 OpenCode 配置' : client === 'codex' ? '复制 Codex 配置' : '复制 Claude 配置'}
            </button>
          ))}
        </div>
        {route ? (
          <div className="hint-line">
            当前输出协议：{protocolLabel(route.outputProtocol)}
            {route.outputProtocol === 'openai_responses'
              ? ' · 适配 Codex / OpenCode。'
              : route.outputProtocol === 'claude'
                ? ' · 适配 Claude Code / OpenCode（Messages）；不走 OpenAI Responses。'
                : ' · 适配 OpenCode（Chat Completions）。'}
          </div>
        ) : null}
      </div>

      {route ? (
        <div className="api-key-call-example">
          <div className="field-label-row">
            <label>{tunnelRunning ? '公网调用示例' : '局域网调用示例'}</label>
            <CopyButton value={buildApiKeyPublicCurl(keyItem, route, endpoints, livePublicURL, routeProvider)} label="复制 curl" />
          </div>
          <div className="hint-line">
            协议：{protocolLabel(route.outputProtocol)} · 客户端 Base URL：{apiKeyClientURL}
            {route.outputProtocol === 'openai_chat' ? ' · 完整路径：/v1/chat/completions' : route.outputProtocol === 'claude' ? ' · 完整路径：/anthropic/v1/messages（Base URL 不要带 /v1）' : ' · 完整路径：/openai/v1/responses'}
            {keyItem.modelOverride ? ` · 固定模型：${keyItem.modelOverride}` : routeProvider?.defaultModel ? ` · 默认模型：${routeProvider.defaultModel}` : ''}
          </div>
          <pre className="curl-preview">{buildApiKeyPublicCurl(keyItem, route, endpoints, livePublicURL, routeProvider)}</pre>
        </div>
      ) : null}

      {clientConfigModal && route ? (
        <ApiKeyClientConfigModal
          client={clientConfigModal}
          keyItem={keyItem}
          route={route}
          provider={routeProvider}
          endpoints={endpoints}
          lanRoot={localGatewayRoot(endpoints)}
          publicBase={livePublicURL}
          publicAvailable={publicAvailable}
          onClose={() => setClientConfigModal(null)}
          onToast={onToast}
          onUpdateField={onUpdateField}
        />
      ) : null}

      {fallbackModalOpen ? (
        <ApiKeyFallbackProvidersModal
          preferredProviderId={binding.providerId}
          providers={providers}
          models={models}
          selectedIds={fallbackIds}
          modelOverrides={fallbackModelOverrides}
          saving={saving}
          testingProviderID={testingProviderID}
          onRefreshModels={onRefreshModels}
          onClose={() => setFallbackModalOpen(false)}
          onSave={async (ids, overrides) => {
            await onUpdateFallbacks(keyItem, ids, overrides);
            setFallbackModalOpen(false);
          }}
        />
      ) : null}
    </div>
  );
}

function ApiKeyFallbackProvidersModal({
  preferredProviderId,
  providers,
  models,
  selectedIds,
  modelOverrides,
  saving,
  testingProviderID,
  onRefreshModels,
  onClose,
  onSave,
}: {
  preferredProviderId: string;
  providers: Provider[];
  models: Model[];
  selectedIds: string[];
  modelOverrides: Record<string, string>;
  saving: boolean;
  testingProviderID: string;
  onRefreshModels: (providerId: string, providerName: string) => Promise<void>;
  onClose: () => void;
  onSave: (ids: string[], overrides: Record<string, string>) => Promise<void>;
}) {
  const candidates = providers.filter((item) => item.id !== preferredProviderId);
  const [orderedIds, setOrderedIds] = React.useState<string[]>(() => (
    selectedIds.filter((id) => id !== preferredProviderId && providers.some((item) => item.id === id))
  ));
  const [overrides, setOverrides] = React.useState<Record<string, string>>(() => {
    const next: Record<string, string> = {};
    for (const id of selectedIds) {
      const model = (modelOverrides[id] || '').trim();
      if (model) next[id] = model;
    }
    return next;
  });
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState('');
  const selectedProviders = orderedIds
    .map((id) => candidates.find((item) => item.id === id))
    .filter((item): item is Provider => Boolean(item));
  const unselectedProviders = candidates.filter((item) => !orderedIds.includes(item.id));
  const missingModelIds = orderedIds.filter((id) => !(overrides[id] || '').trim());

  function toggleProvider(id: string) {
    setError('');
    setOrderedIds((current) => {
      if (current.includes(id)) {
        setOverrides((prev) => {
          const next = { ...prev };
          delete next[id];
          return next;
        });
        return current.filter((item) => item !== id);
      }
      return [...current, id];
    });
  }

  function moveProvider(id: string, delta: number) {
    setOrderedIds((current) => {
      const index = current.indexOf(id);
      if (index < 0) return current;
      const next = index + delta;
      if (next < 0 || next >= current.length) return current;
      const copy = [...current];
      const [item] = copy.splice(index, 1);
      copy.splice(next, 0, item);
      return copy;
    });
  }

  function setProviderModel(id: string, model: string) {
    setError('');
    setOverrides((current) => ({ ...current, [id]: model }));
  }

  return (
    <Modal
      title="配置备选 Provider"
      description="按优先级排序（#1 在最上）。每个备选必须选择该 Provider 的固定模型替换。"
      onClose={onClose}
      size="wide"
    >
      <div className="api-key-fallback-modal">
        <div className="hint-line">首选 Provider 不在此列表中。已选按优先级从上到下排列；保存前每个备选都要选好固定模型。</div>
        {candidates.length === 0 ? (
          <div className="empty-state compact">没有可配置的备选 Provider，请先添加更多输入 Provider。</div>
        ) : (
          <div className="api-key-fallback-list">
            {selectedProviders.map((provider, orderIndex) => {
              const providerModels = models.filter((model) => model.providerId === provider.id);
              return (
                <div className="api-key-fallback-item selected" key={provider.id}>
                  <div className="api-key-fallback-item-main">
                    <label className="checkbox-field">
                      <input
                        type="checkbox"
                        checked
                        disabled={busy || saving}
                        onChange={() => toggleProvider(provider.id)}
                      />
                      <span>{providerOptionLabel(provider)}</span>
                    </label>
                    <div className="api-key-fallback-order">
                      <span className="api-key-fallback-rank">#{orderIndex + 1}</span>
                      <button className="mini-btn" type="button" disabled={busy || saving || orderIndex <= 0} onClick={() => moveProvider(provider.id, -1)}>上移</button>
                      <button className="mini-btn" type="button" disabled={busy || saving || orderIndex >= selectedProviders.length - 1} onClick={() => moveProvider(provider.id, 1)}>下移</button>
                    </div>
                  </div>
                  <div className="api-key-fallback-model">
                    <label>固定模型替换（必选）</label>
                    <div className="field-inline">
                      <SearchableModelSelect
                        value={overrides[provider.id] || ''}
                        models={providerModels}
                        disabled={busy || saving}
                        emptyLabel="请选择该 Provider 的固定模型"
                        onChange={(value) => setProviderModel(provider.id, value)}
                      />
                      <button
                        className="mini-btn"
                        type="button"
                        disabled={busy || saving || testingProviderID === provider.id}
                        onClick={() => void onRefreshModels(provider.id, provider.name)}
                        title="刷新该 Provider 模型列表"
                      >
                        {testingProviderID === provider.id ? '刷新中…' : '刷新模型'}
                      </button>
                    </div>
                    {!(overrides[provider.id] || '').trim() ? (
                      <div className="hint-line error">必须为该备选选择固定模型</div>
                    ) : (
                      <div className="hint-line">切换到此备选时，将强制使用该模型</div>
                    )}
                  </div>
                </div>
              );
            })}
            {unselectedProviders.map((provider) => (
              <div className="api-key-fallback-item" key={provider.id}>
                <label className="checkbox-field">
                  <input
                    type="checkbox"
                    checked={false}
                    disabled={busy || saving}
                    onChange={() => toggleProvider(provider.id)}
                  />
                  <span>{providerOptionLabel(provider)}</span>
                </label>
              </div>
            ))}
          </div>
        )}
        <div className="hint-line">
          当前顺序：{orderedIds.length === 0 ? '（无）' : orderedIds.map((id, index) => {
            const provider = providers.find((item) => item.id === id);
            const model = (overrides[id] || '').trim() || '未选模型';
            return `${index + 1}. ${provider ? providerOptionLabel(provider) : id} → ${model}`;
          }).join(' ； ')}
        </div>
        {error ? <div className="hint-line error">{error}</div> : null}
      </div>
      <div className="actions modal-actions">
        <button className="btn" type="button" disabled={busy || saving} onClick={onClose}>取消</button>
        <button
          className="btn primary"
          type="button"
          disabled={busy || saving || missingModelIds.length > 0}
          onClick={() => {
            if (missingModelIds.length > 0) {
              setError('每个已选备选 Provider 都必须选择固定模型替换');
              return;
            }
            const cleaned: Record<string, string> = {};
            for (const id of orderedIds) {
              cleaned[id] = (overrides[id] || '').trim();
            }
            setBusy(true);
            void onSave(orderedIds, cleaned).finally(() => setBusy(false));
          }}
        >
          {busy || saving ? '保存中…' : '保存备选'}
        </button>
      </div>
    </Modal>
  );
}

function ApiKeyClientConfigModal({
  client,
  keyItem,
  route,
  provider,
  endpoints,
  lanRoot,
  publicBase,
  publicAvailable,
  onClose,
  onToast,
  onUpdateField,
}: {
  client: 'opencode' | 'codex' | 'claude';
  keyItem: APIKey;
  route: Route;
  provider?: Provider;
  endpoints: OutputEndpoint[];
  lanRoot: string;
  publicBase: string;
  publicAvailable: boolean;
  onClose: () => void;
  onToast?: (message: string) => void;
  onUpdateField?: (key: APIKey, field: 'name' | 'routeId' | 'modelOverride' | 'thinkingDepthOverride' | 'maxOutputTokens' | 'streamEnabled' | 'codexKeepOfficialLogin' | 'enabled', value: string | boolean | number) => Promise<void>;
}) {
  const [networkMode, setNetworkMode] = React.useState<'lan' | 'public'>(publicAvailable ? 'public' : 'lan');
  // 绑定到具体 key（而非只在本次弹窗会话内），跨次打开保留上次选择。
  const [keepOfficialLogin, setKeepOfficialLogin] = React.useState(() => keyItem.codexKeepOfficialLogin ?? false);
  const effectivePublicBase = networkMode === 'public' && publicAvailable ? publicBase : '';
  const configText = React.useMemo(
    () => buildApiKeyClientConfig(client, keyItem, route, endpoints, effectivePublicBase, provider, keepOfficialLogin),
    [client, keyItem, route, endpoints, effectivePublicBase, provider, keepOfficialLogin],
  );
  const configExtras = React.useMemo(
    () => buildApiKeyClientConfigExtras(client, keyItem, provider),
    [client, keyItem, provider],
  );
  const installScript = React.useMemo(
    () => buildApiKeyClientConfigInstallScript(client, configText, configExtras),
    [client, configText, configExtras],
  );
  // 静态脚本（不依赖当前 key/provider），只用来把本工具此前写入的那一段摘掉。
  const codexRestoreScript = React.useMemo(() => buildApiKeyCodexRestoreOfficialScript(), []);
  const filePath = clientConfigFilePath(client);
  const gatewayRoot = apiKeyGatewayRoot(endpoints, effectivePublicBase);
  const protocolHint = clientConfigProtocolHint(client, route);

  const copyConfig = React.useCallback((text: string, message: string) => {
    void navigator.clipboard.writeText(text).then(() => {
      onToast?.(message);
    });
  }, [onToast]);

  React.useEffect(() => {
    const modeLabel = networkMode === 'public' ? '公网域名' : '内网';
    copyConfig(installScript, `已复制${clientConfigScriptNoun(client)}（${clientConfigTitle(client)} · ${modeLabel}），粘贴到终端执行即可`);
    // 仅打开弹窗时自动复制一次；切换网络时由按钮自行复制
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  React.useEffect(() => {
    if (!publicAvailable && networkMode === 'public') {
      setNetworkMode('lan');
    }
  }, [publicAvailable, networkMode]);

  return (
    <Modal
      title={clientConfigTitle(client)}
      description={
        client === 'codex'
          ? '已复制 Python 修改脚本到剪贴板。粘贴到终端执行即可增量合并进 config.toml（只替换本工具管理的一段，不动你其他配置；会先备份；依赖 macOS 自带 python3）。'
          : '已复制 Python 修改脚本到剪贴板。粘贴到终端执行即可增量合并进 JSON 配置（只更新本工具管理的键，保留你其它配置；会先备份；依赖 macOS 自带 python3）。'
      }
      onClose={onClose}
      size="wide"
    >
      <div className="api-key-client-config-modal">
        <div className="field">
          <label>目标配置文件</label>
          <div className="field-inline">
            <div className="field-readonly code">{filePath}</div>
            <CopyButton value={filePath} label="复制路径" />
          </div>
          {configExtras.length > 0 ? (
            <div className="hint-line">
              同时写入：{configExtras.map((item) => item.display).join('、')}（Codex 模型元数据，消除 Model metadata not found）
            </div>
          ) : null}
        </div>

        <div className="field">
          <label>网关地址</label>
          <div className="api-key-network-toggle" role="group" aria-label="网关地址类型">
            <button
              className={`mini-btn${networkMode === 'lan' ? ' active' : ''}`}
              type="button"
              onClick={() => {
                setNetworkMode('lan');
                const nextConfig = buildApiKeyClientConfig(client, keyItem, route, endpoints, '', provider, keepOfficialLogin);
                const nextScript = buildApiKeyClientConfigInstallScript(client, nextConfig, buildApiKeyClientConfigExtras(client, keyItem, provider));
                copyConfig(nextScript, `已复制${clientConfigScriptNoun(client)}（${clientConfigTitle(client)} · 内网）`);
              }}
            >
              内网
            </button>
            <button
              className={`mini-btn${networkMode === 'public' ? ' active' : ''}`}
              type="button"
              disabled={!publicAvailable}
              title={publicAvailable ? publicBase : '未开启公网域名 / 隧道'}
              onClick={() => {
                if (!publicAvailable) return;
                setNetworkMode('public');
                const nextConfig = buildApiKeyClientConfig(client, keyItem, route, endpoints, publicBase, provider, keepOfficialLogin);
                const nextScript = buildApiKeyClientConfigInstallScript(client, nextConfig, buildApiKeyClientConfigExtras(client, keyItem, provider));
                copyConfig(nextScript, `已复制${clientConfigScriptNoun(client)}（${clientConfigTitle(client)} · 公网域名）`);
              }}
            >
              公网域名
            </button>
          </div>
          <div className="hint-line">
            当前根地址：{gatewayRoot}
            {!publicAvailable ? ' · 公网域名不可用（请先在「公网访问」开启隧道/域名）' : ''}
          </div>
        </div>

        {client === 'codex' ? (
          <div className="field">
            <label>保持账号登录</label>
            <label className="hint-line" style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              <input
                type="checkbox"
                checked={keepOfficialLogin}
                onChange={(event) => {
                  const next = event.target.checked;
                  setKeepOfficialLogin(next);
                  const nextConfig = buildApiKeyClientConfig(client, keyItem, route, endpoints, effectivePublicBase, provider, next);
                  const nextScript = buildApiKeyClientConfigInstallScript(client, nextConfig, buildApiKeyClientConfigExtras(client, keyItem, provider));
                  copyConfig(nextScript, `已复制${clientConfigScriptNoun(client)}（${clientConfigTitle(client)} · ${next ? '保持账号登录' : '不保留'}）`);
                  // 持久化到该 key，下次打开弹窗（甚至换设备/刷新页面）自动恢复这次的选择。
                  void onUpdateField?.(keyItem, 'codexKeepOfficialLogin', next);
                }}
              />
              开启后 provider 表会对齐 Codex 官方 provider 形状（name = "OpenAI"、supports_websockets = true），
              尽量保留 Codex 官方插件市场 / 移动端远程控制；不写入也不影响 ~/.codex/auth.json，实际模型流量仍走本网关。
              默认关闭；若不需要这些官方能力可保持关闭。
            </label>
          </div>
        ) : null}

        {protocolHint ? <div className="hint-line error">{protocolHint}</div> : null}

        <div className="field">
          <label>配置修改脚本（Python）</label>
          <div className="hint-line">
            {client === 'codex' ? (
              <>
                终端粘贴执行后会增量合并进 {filePath}：只替换本工具用一对多个 # 号分界线包起来的那一段
                （provider 相关配置），文件里其他任何区块（比如 [features]、[memories]、
                sandbox_mode、approval_policy、personality 等你自己的配置）原样保留、不会被改动或
                挪动位置——对 Codex App 做最小改动。
                {configExtras.length > 0 ? ` 同时整份覆盖 ${configExtras.map((item) => item.display).join('、')}（本工具独占的模型元数据文件，不影响其他配置）。` : ''}
                {' '}执行前会先备份为同目录 <code>.bak.时间戳</code>。脚本为 Python（macOS 自带 python3），不再依赖 bash/awk。
              </>
            ) : (
              <>
                终端粘贴执行后会增量合并进 {filePath}
                {configExtras.length > 0 ? `，并写入 ${configExtras.map((item) => item.display).join('、')}` : ''}
                ：只更新本工具管理的键，保留你其它配置；若文件已存在，会先备份为同目录 <code>.bak.时间戳</code>。
                脚本为 Python（macOS 自带 python3）。
              </>
            )}
          </div>
          <pre className="curl-preview api-key-client-config-preview">{installScript}</pre>
        </div>
      </div>
      <div className="actions modal-actions">
        <button className="btn" type="button" onClick={onClose}>关闭</button>
        <button
          className="btn"
          type="button"
          onClick={() => copyConfig(configText, `已复制纯配置内容（${clientConfigTitle(client)}）`)}
        >
          仅复制配置内容
        </button>
        {client === 'codex' ? (
          <button
            className="btn"
            type="button"
            title="只移除本工具此前写入的那一段，其余配置不受影响；没有该区块时是无害的空操作"
            onClick={() => copyConfig(codexRestoreScript, '已复制"还原为官方 provider"脚本，粘贴到终端执行即可')}
          >
            还原为官方 provider
          </button>
        ) : null}
        <button
          className="btn primary"
          type="button"
          onClick={() => copyConfig(installScript, `已复制${clientConfigScriptNoun(client)}（${clientConfigTitle(client)}）`)}
        >
          复制{clientConfigScriptNoun(client)}
        </button>
      </div>
    </Modal>
  );
}

function countModelAliases(aliases: Record<string, string>) {
  return Object.keys(aliases || {}).length;
}

function aliasForModel(aliases: Record<string, string>, modelId: string) {
  for (const [alias, target] of Object.entries(aliases || {})) {
    if (target === modelId) {
      return alias;
    }
  }
  return '';
}

function buildModelAliases(models: Model[], aliasByModelId: Record<string, string>) {
  const aliases: Record<string, string> = {};
  const seen = new Set<string>();
  for (const model of models) {
    const alias = (aliasByModelId[model.id] || '').trim();
    if (!alias || alias === model.id) {
      continue;
    }
    if (seen.has(alias)) {
      continue;
    }
    aliases[alias] = model.id;
    seen.add(alias);
  }
  return aliases;
}

function applyModelAliasPrefix(modelId: string, prefix: string) {
  const trimmed = prefix.trim();
  if (!trimmed) {
    return '';
  }
  const alias = `${trimmed}${modelId}`;
  return alias === modelId ? '' : alias;
}

function inferCommonModelAliasPrefix(models: Model[], aliases: Record<string, string>) {
  let prefix: string | null = null;
  let matched = 0;
  for (const model of models) {
    const alias = aliasForModel(aliases, model.id);
    if (!alias || alias === model.id || !alias.endsWith(model.id)) {
      continue;
    }
    const candidate = alias.slice(0, alias.length - model.id.length);
    if (prefix === null) {
      prefix = candidate;
    } else if (prefix !== candidate) {
      return '';
    }
    matched += 1;
  }
  return matched > 0 ? (prefix || '') : '';
}

function ApiKeyModelMappingModal({
  open,
  providerName,
  models,
  aliases,
  saving,
  refreshing,
  onClose,
  onSave,
  onRefresh,
}: {
  open: boolean;
  providerName: string;
  models: Model[];
  aliases: Record<string, string>;
  saving?: boolean;
  refreshing?: boolean;
  onClose: () => void;
  onSave: (aliases: Record<string, string>) => void | Promise<void>;
  onRefresh?: () => void;
}) {
  const [draft, setDraft] = React.useState<Record<string, string>>({});
  const [prefix, setPrefix] = React.useState('');

  React.useEffect(() => {
    if (!open) {
      return;
    }
    const next: Record<string, string> = {};
    for (const model of models) {
      next[model.id] = aliasForModel(aliases, model.id);
    }
    setDraft(next);
    setPrefix(inferCommonModelAliasPrefix(models, aliases));
  }, [open]);

  React.useEffect(() => {
    if (!open) {
      return;
    }
    setDraft((current) => {
      const next = { ...current };
      let changed = false;
      for (const model of models) {
        if (!(model.id in next)) {
          next[model.id] = aliasForModel(aliases, model.id);
          changed = true;
        }
      }
      return changed ? next : current;
    });
  }, [open, models]);

  function applyPrefixToAll() {
    const trimmed = prefix.trim();
    if (!trimmed) {
      return;
    }
    const next: Record<string, string> = {};
    for (const model of models) {
      next[model.id] = applyModelAliasPrefix(model.id, trimmed);
    }
    setDraft(next);
  }

  function clearAllAliases() {
    const next: Record<string, string> = {};
    for (const model of models) {
      next[model.id] = '';
    }
    setDraft(next);
  }

  const prefixPreview = models[0] ? applyModelAliasPrefix(models[0].id, prefix) : '';

  if (!open) {
    return null;
  }

  return (
    <Modal
      size="wide"
      title="模型映射"
      description={`为 ${providerName} 的模型配置客户端别名，避免 Cursor 等客户端的自定义模型名与内置名称冲突。可统一加前缀批量生成，也可单独调整某个模型。`}
      onClose={onClose}
    >
      <div className="modal-toolbar">
        <div className="model-mapping-toolbar-meta">共 {models.length} 个模型 · 已配置 {countModelAliases(aliases)} 个别名</div>
        {onRefresh ? (
          <button className="mini-btn" type="button" disabled={refreshing} onClick={onRefresh}>
            {refreshing ? '刷新中…' : '刷新模型列表'}
          </button>
        ) : null}
      </div>
      {models.length > 0 ? (
        <div className="model-mapping-prefix-bar">
          <div className="field model-mapping-prefix-field">
            <label>统一前缀</label>
            <input
              value={prefix}
              placeholder="例如 gw- 或 custom-"
              onChange={(event) => setPrefix(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === 'Enter') {
                  event.preventDefault();
                  applyPrefixToAll();
                }
              }}
            />
          </div>
          <button className="btn model-mapping-btn" type="button" disabled={!prefix.trim()} onClick={applyPrefixToAll}>应用到全部</button>
          <button className="btn" type="button" onClick={clearAllAliases}>清空全部</button>
          {prefixPreview ? (
            <div className="model-mapping-prefix-preview">示例：{models[0].id} → {prefixPreview}</div>
          ) : null}
        </div>
      ) : null}
      {models.length === 0 ? (
        <div className="empty-state">暂无模型。请先刷新 Provider 模型列表，再配置别名。</div>
      ) : (
        <div className="model-mapping-list">
          <div className="model-mapping-head">
            <span>真实模型</span>
            <span>客户端别名</span>
          </div>
          {models.map((model) => (
            <div className="model-mapping-row" key={model.id}>
              <div className="model-mapping-model">
                <div className="model-mapping-model-id">{model.id}</div>
                {model.contextLength ? <div className="model-mapping-model-meta">{model.contextLength.toLocaleString()} tokens</div> : null}
              </div>
              <input
                value={draft[model.id] || ''}
                placeholder="留空表示不映射"
                onChange={(event) => setDraft((current) => ({ ...current, [model.id]: event.target.value }))}
              />
            </div>
          ))}
        </div>
      )}
      <div className="actions modal-actions">
        <button className="btn" type="button" onClick={onClose}>取消</button>
        <button
          className="btn primary"
          type="button"
          disabled={saving || models.length === 0}
          onClick={() => void Promise.resolve(onSave(buildModelAliases(models, draft))).then(() => onClose())}
        >
          {saving ? '保存中…' : '保存映射'}
        </button>
      </div>
    </Modal>
  );
}

function ApiKeyModelMappingControl({
  aliases,
  models,
  providerName,
  disabled,
  saving,
  refreshing,
  onSave,
  onRefresh,
}: {
  aliases: Record<string, string>;
  models: Model[];
  providerName: string;
  disabled?: boolean;
  saving?: boolean;
  refreshing?: boolean;
  onSave: (aliases: Record<string, string>) => void | Promise<void>;
  onRefresh?: () => void;
}) {
  const [open, setOpen] = React.useState(false);
  const count = countModelAliases(aliases);

  return (
    <>
      <div className="field">
        <label>模型映射</label>
        <div className="model-mapping-trigger">
          <button className="btn model-mapping-btn" type="button" disabled={disabled} onClick={() => setOpen(true)}>模型映射</button>
          <span className="model-mapping-summary">{count > 0 ? `已配置 ${count} 个别名` : '未配置别名'}</span>
        </div>
      </div>
      <ApiKeyModelMappingModal
        open={open}
        providerName={providerName}
        models={models}
        aliases={aliases}
        saving={saving}
        refreshing={refreshing}
        onClose={() => setOpen(false)}
        onRefresh={onRefresh}
        onSave={onSave}
      />
    </>
  );
}

type SearchableModelOption = { id: string; label: string };

function filterModelOptions(models: Model[], queryRaw: string): SearchableModelOption[] {
  const needle = queryRaw.trim().toLowerCase();
  const options = models.map((model) => ({ id: model.id, label: modelSelectOptionLabel(model) }));
  if (!needle) return options;
  return options.filter((option) => option.id.toLowerCase().includes(needle) || option.label.toLowerCase().includes(needle));
}

type MultiFilterOption = { id: string; label: string };

// MultiSelectFilter：下拉多选 + 文本快捷过滤合一的筛选框（复用
// searchable-select 样式，参考 SearchableModelSelect 的交互）。收起时输入框
// 展示已选摘要，展开时变成关键字过滤；点击选项切换勾选且菜单保持展开。
function MultiSelectFilter({ label, options, selected, onChange, allLabel, fieldClassName }: {
  label?: string;
  options: MultiFilterOption[];
  selected: string[];
  onChange: (next: string[]) => void;
  allLabel?: string;
  // 外层 field 样式：筛选栏默认窄宽度，弹窗内可传 '' 占满整行。
  fieldClassName?: string;
}) {
  const rootRef = React.useRef<HTMLDivElement | null>(null);
  const inputRef = React.useRef<HTMLInputElement | null>(null);
  const listRef = React.useRef<HTMLDivElement | null>(null);
  const [open, setOpen] = React.useState(false);
  const [query, setQuery] = React.useState('');
  const [highlight, setHighlight] = React.useState(0);

  const filtered = React.useMemo(() => {
    const keyword = query.trim().toLowerCase();
    if (!keyword) return options;
    return options.filter((option) => option.label.toLowerCase().includes(keyword) || option.id.toLowerCase().includes(keyword));
  }, [options, query]);

  const displayLabel = React.useMemo(() => {
    if (selected.length === 0) return allLabel || '全部';
    const labels = selected.map((id) => options.find((option) => option.id === id)?.label || id);
    const joined = labels.join('、');
    return joined.length > 24 ? `已选 ${selected.length} 项` : joined;
  }, [selected, options, allLabel]);

  const close = React.useCallback(() => {
    setOpen(false);
    setQuery('');
    setHighlight(0);
  }, []);

  const toggleOption = React.useCallback((id: string) => {
    onChange(selected.includes(id) ? selected.filter((item) => item !== id) : [...selected, id]);
  }, [onChange, selected]);

  React.useEffect(() => {
    if (!open) return;
    // 用捕获阶段监听：Modal 的 modal-card 会在冒泡阶段对 mousedown 调用
    // stopPropagation（防止点击弹窗内部误触发遮罩层的关闭逻辑），这会导致
    // 挂在 document 冒泡阶段的“点击外部关闭”监听永远收不到事件——弹窗内的
    // 下拉框因此选完选项后无法通过点击其他地方收起。捕获阶段先于冒泡阶段
    // 触发，不受该 stopPropagation 影响。
    const onPointerDown = (event: MouseEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) close();
    };
    document.addEventListener('mousedown', onPointerDown, true);
    return () => document.removeEventListener('mousedown', onPointerDown, true);
  }, [close, open]);

  React.useEffect(() => {
    setHighlight(0);
  }, [query, open]);

  React.useEffect(() => {
    if (!open || !listRef.current) return;
    const active = listRef.current.querySelector<HTMLElement>('[data-active="true"]');
    active?.scrollIntoView({ block: 'nearest' });
  }, [highlight, open, filtered]);

  const onKeyDown = (event: React.KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'ArrowDown') {
      event.preventDefault();
      if (!open) { setOpen(true); return; }
      setHighlight((prev) => (filtered.length === 0 ? 0 : (prev + 1) % filtered.length));
      return;
    }
    if (event.key === 'ArrowUp') {
      event.preventDefault();
      if (!open) { setOpen(true); return; }
      setHighlight((prev) => (filtered.length === 0 ? 0 : (prev - 1 + filtered.length) % filtered.length));
      return;
    }
    if (event.key === 'Enter') {
      event.preventDefault();
      if (!open) { setOpen(true); return; }
      const option = filtered[highlight];
      if (option) toggleOption(option.id);
      return;
    }
    if (event.key === 'Escape') {
      // 下拉框展开时：ESC 只收起下拉框，并阻止事件冒泡到 Modal 的全局 ESC 监听
      // （否则整个弹窗会被一起关掉）。未展开时不拦截，让 ESC 正常关闭弹窗。
      if (open) {
        event.preventDefault();
        event.stopPropagation();
        close();
      }
      return;
    }
  };

  return (
    <div className={`field ${fieldClassName ?? 'api-keys-filter-field'}`.trim()}>
      {label ? <label>{label}</label> : null}
      <div className={`searchable-select${open ? ' open' : ''}`} ref={rootRef}>
        <input
          ref={inputRef}
          className="searchable-select-input"
          type="text"
          role="combobox"
          aria-expanded={open}
          aria-autocomplete="list"
          value={open ? query : displayLabel}
          placeholder={open ? '输入关键字过滤…' : (allLabel || '全部')}
          onFocus={() => { setOpen(true); setQuery(''); }}
          onClick={() => { setOpen(true); }}
          onChange={(event) => { setOpen(true); setQuery(event.target.value); }}
          onKeyDown={onKeyDown}
        />
        {open ? (
          <div className="searchable-select-menu" role="listbox" aria-multiselectable="true" ref={listRef}>
            {selected.length > 0 ? (
              <button
                type="button"
                className="searchable-select-option multi-clear"
                onMouseDown={(event) => { event.preventDefault(); onChange([]); }}
              >
                清除筛选（显示全部）
              </button>
            ) : null}
            {filtered.length === 0 ? (
              <div className="searchable-select-empty">无匹配项</div>
            ) : filtered.map((option, index) => {
              const checked = selected.includes(option.id);
              return (
                <button
                  key={option.id}
                  type="button"
                  role="option"
                  aria-selected={checked}
                  data-active={index === highlight ? 'true' : 'false'}
                  className={`searchable-select-option${checked ? ' selected' : ''}${index === highlight ? ' active' : ''}`}
                  onMouseEnter={() => setHighlight(index)}
                  onMouseDown={(event) => {
                    event.preventDefault();
                    toggleOption(option.id);
                  }}
                >
                  <span className={`multi-check${checked ? ' on' : ''}`}>{checked ? '✓' : ''}</span>
                  {option.label}
                </button>
              );
            })}
          </div>
        ) : null}
      </div>
    </div>
  );
}

function SearchableModelSelect({
  value,
  models,
  disabled,
  emptyLabel,
  onChange,
}: {
  value: string;
  models: Model[];
  disabled?: boolean;
  emptyLabel: string;
  onChange: (value: string) => void;
}) {
  const rootRef = React.useRef<HTMLDivElement | null>(null);
  const inputRef = React.useRef<HTMLInputElement | null>(null);
  const listRef = React.useRef<HTMLDivElement | null>(null);
  const [open, setOpen] = React.useState(false);
  const [query, setQuery] = React.useState('');
  const [highlight, setHighlight] = React.useState(0);

  const filtered = React.useMemo(() => filterModelOptions(models, query), [models, query]);
  const options = React.useMemo<SearchableModelOption[]>(() => {
    const empty: SearchableModelOption = { id: '', label: emptyLabel };
    if (!query.trim()) return [empty, ...filtered];
    const matchedEmpty = emptyLabel.toLowerCase().includes(query.trim().toLowerCase());
    return matchedEmpty ? [empty, ...filtered] : filtered;
  }, [emptyLabel, filtered, query]);

  const selectedModel = models.find((model) => model.id === value);
  const displayLabel = value
    ? (selectedModel ? modelSelectOptionLabel(selectedModel) : value)
    : emptyLabel;

  const close = React.useCallback(() => {
    setOpen(false);
    setQuery('');
    setHighlight(0);
  }, []);

  const selectOption = React.useCallback((next: string) => {
    onChange(next);
    close();
  }, [close, onChange]);

  React.useEffect(() => {
    if (!open) return;
    // 用捕获阶段监听：Modal 的 modal-card 会在冒泡阶段对 mousedown 调用
    // stopPropagation（防止点击弹窗内部误触发遮罩层的关闭逻辑），这会导致
    // 挂在 document 冒泡阶段的“点击外部关闭”监听永远收不到事件——弹窗内的
    // 下拉框因此选完选项后无法通过点击其他地方收起。捕获阶段先于冒泡阶段
    // 触发，不受该 stopPropagation 影响。
    const onPointerDown = (event: MouseEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) close();
    };
    document.addEventListener('mousedown', onPointerDown, true);
    return () => document.removeEventListener('mousedown', onPointerDown, true);
  }, [close, open]);

  React.useEffect(() => {
    if (!open) return;
    inputRef.current?.focus();
    inputRef.current?.select();
  }, [open]);

  React.useEffect(() => {
    setHighlight(0);
  }, [query, open]);

  React.useEffect(() => {
    if (!open || !listRef.current) return;
    const active = listRef.current.querySelector<HTMLElement>('[data-active="true"]');
    active?.scrollIntoView({ block: 'nearest' });
  }, [highlight, open, options]);

  const onKeyDown = (event: React.KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'ArrowDown') {
      event.preventDefault();
      if (!open) {
        setOpen(true);
        return;
      }
      setHighlight((prev) => (options.length === 0 ? 0 : (prev + 1) % options.length));
      return;
    }
    if (event.key === 'ArrowUp') {
      event.preventDefault();
      if (!open) {
        setOpen(true);
        return;
      }
      setHighlight((prev) => (options.length === 0 ? 0 : (prev - 1 + options.length) % options.length));
      return;
    }
    if (event.key === 'Enter') {
      event.preventDefault();
      if (!open) {
        setOpen(true);
        return;
      }
      const option = options[highlight];
      if (option) selectOption(option.id);
      return;
    }
    if (event.key === 'Escape') {
      // 下拉框展开时：ESC 只收起下拉框，并阻止事件冒泡到 Modal 的全局 ESC 监听
      // （否则整个弹窗会被一起关掉）。未展开时不拦截，让 ESC 正常关闭弹窗。
      if (open) {
        event.preventDefault();
        event.stopPropagation();
        close();
      }
      return;
    }
  };

  return (
    <div className={`searchable-select${open ? ' open' : ''}`} ref={rootRef}>
      <input
        ref={inputRef}
        className="searchable-select-input"
        type="text"
        role="combobox"
        aria-expanded={open}
        aria-autocomplete="list"
        aria-controls="searchable-model-list"
        disabled={disabled}
        value={open ? query : displayLabel}
        placeholder={open ? '输入关键字筛选模型…' : emptyLabel}
        onFocus={() => {
          if (disabled) return;
          setOpen(true);
          setQuery('');
        }}
        onClick={() => {
          if (disabled) return;
          setOpen(true);
          setQuery('');
        }}
        onChange={(event) => {
          setOpen(true);
          setQuery(event.target.value);
        }}
        onKeyDown={onKeyDown}
      />
      {open ? (
        <div className="searchable-select-menu" id="searchable-model-list" role="listbox" ref={listRef}>
          {options.length === 0 ? (
            <div className="searchable-select-empty">无匹配模型</div>
          ) : options.map((option, index) => (
            <button
              key={option.id || '__empty__'}
              type="button"
              role="option"
              aria-selected={option.id === value}
              data-active={index === highlight ? 'true' : 'false'}
              className={`searchable-select-option${option.id === value ? ' selected' : ''}${index === highlight ? ' active' : ''}`}
              onMouseEnter={() => setHighlight(index)}
              onMouseDown={(event) => {
                event.preventDefault();
                selectOption(option.id);
              }}
            >
              {option.label}
            </button>
          ))}
        </div>
      ) : null}
    </div>
  );
}

function ApiKeyFixedModelField({
  value,
  models,
  disabled,
  refreshing,
  onChange,
  onRefresh,
}: {
  value: string;
  models: Model[];
  disabled?: boolean;
  refreshing?: boolean;
  onChange: (value: string) => void;
  onRefresh: () => void;
}) {
  return (
    <div className="field">
      <label>固定模型替换</label>
      <div className="field-inline">
        <SearchableModelSelect
          value={value}
          models={models}
          disabled={disabled}
          emptyLabel="（不替换，使用请求体 model）"
          onChange={onChange}
        />
        <button className="mini-btn" type="button" disabled={disabled || refreshing} onClick={onRefresh} title="从绑定路由的 Provider 重新获取模型列表">
          {refreshing ? '刷新中…' : '刷新模型'}
        </button>
      </div>
      <div className="hint-line">
        设置后将忽略请求体中的 model，统一替换为所选模型。
        {models.length > 0 ? ` · 共 ${models.length} 个，点开后输入关键字筛选；选项含网关解析的 max output` : ''}
      </div>
    </div>
  );
}

function ApiKeyMaxOutputTokensField({
  value,
  disabled,
  onSave,
}: {
  value: number;
  disabled?: boolean;
  onSave: (value: number) => Promise<void> | void;
}) {
  const [draft, setDraft] = React.useState(value > 0 ? String(value) : '');
  React.useEffect(() => {
    setDraft(value > 0 ? String(value) : '');
  }, [value]);

  function commit() {
    const raw = draft.trim();
    const n = raw === '' ? 0 : Number.parseInt(raw, 10);
    const next = Number.isFinite(n) && n > 0 ? Math.min(n, 200000) : 0;
    setDraft(next > 0 ? String(next) : '');
    if (next === (value > 0 ? value : 0)) return;
    void onSave(next);
  }

  return (
    <div className="field">
      <label>最大输出 Token</label>
      <input
        type="number"
        min={0}
        max={200000}
        placeholder="0 = 自动（按模型）"
        disabled={disabled}
        value={draft}
        onChange={(event) => setDraft(event.target.value)}
        onBlur={() => commit()}
        onKeyDown={(event) => {
          if (event.key === 'Enter') {
            event.preventDefault();
            (event.target as HTMLInputElement).blur();
          }
        }}
      />
      <div className="hint-line">留空或 0 按模型自动解析；填写后覆盖上游 max_tokens（上限 200000）。</div>
    </div>
  );
}

function ApiKeyNameField({ name, disabled, onSave }: { name: string; disabled?: boolean; onSave: (name: string) => Promise<void> | void }) {
  const [draft, setDraft] = React.useState(name);
  React.useEffect(() => {
    setDraft(name);
  }, [name]);
  return (
    <div className="field">
      <label>名称</label>
      <input
        value={draft}
        disabled={disabled}
        placeholder="例如 key1"
        onChange={(event) => setDraft(event.target.value)}
        onBlur={() => {
          const trimmed = draft.trim();
          if (!trimmed || trimmed === name) {
            setDraft(name);
            return;
          }
          void onSave(trimmed);
        }}
        onKeyDown={(event) => {
          if (event.key === 'Enter') {
            event.currentTarget.blur();
          }
        }}
      />
    </div>
  );
}

function CopyButton({ value, label = '复制', toastContent }: { value: string; label?: string; toastContent?: string }) {
  const [copied, setCopied] = React.useState(false);
  return (
    <button
      className={`mini-btn${copied ? ' copied' : ''}`}
      type="button"
      onClick={() => {
        void navigator.clipboard.writeText(value).then(() => {
          setCopied(true);
          window.setTimeout(() => setCopied(false), 2000);
          // 仅当调用方显式传入 toastContent 时，才在底部全局 toast 里展示复制的具体内容
          // （持续 3s）；不传时保持原有行为（只有按钮自身文案变化），避免大段文本
          // （比如“复制全部”/“复制 curl”）把底部 toast 撑爆。
          if (toastContent !== undefined) {
            const toast = document.getElementById('toast');
            if (toast) {
              toast.textContent = toastContent;
              toast.classList.add('show');
              window.setTimeout(() => toast.classList.remove('show'), 3000);
            }
          }
        });
      }}
    >
      {copied ? '复制成功' : label}
    </button>
  );
}

function CheckboxField({ label, checked, onChange }: { label: string; checked: boolean; onChange: (value: boolean) => void }) {
  return <label className="field checkbox-field"><input type="checkbox" checked={checked} onChange={(event) => onChange(event.target.checked)} /><span>{label}</span></label>;
}

function SelectField({ label, values, value, onChange, disabled, fullWidth }: { label: string; values: string[]; value?: string; onChange: (value: string) => void; disabled?: boolean; fullWidth?: boolean }) {
  return <div className={`field${fullWidth ? ' field-full' : ''}`}><label>{label}</label><select value={value || values[0] || ''} disabled={disabled} onChange={(event) => onChange(event.target.value)}>{values.map((item) => <option key={item}>{item}</option>)}</select></div>;
}

function URLRow({ label, value, onCopy }: { label: string; value: string; onCopy?: () => void }) {
  return <div className="url-row"><div className="url-label">{label}</div><div className="code">{value}</div><button className="mini-btn" disabled={!onCopy} onClick={onCopy}>{onCopy ? '复制' : '不可用'}</button></div>;
}

type ModalLayer = {
  id: number;
  onClose: () => void;
};

const modalLayers: ModalLayer[] = [];
let nextModalLayerID = 0;
let modalEscapeListenerReady = false;

function ensureModalEscapeListener() {
  if (modalEscapeListenerReady) return;
  modalEscapeListenerReady = true;
  window.addEventListener('keydown', (event) => {
    if (event.key !== 'Escape' || modalLayers.length === 0) return;
    const top = modalLayers[modalLayers.length - 1];
    event.preventDefault();
    event.stopPropagation();
    top.onClose();
  });
}

function Modal({ title, description, children, onClose, size = 'default', blocking = true }: { title: string; description: string; children: React.ReactNode; onClose: () => void; size?: 'default' | 'wide'; blocking?: boolean }) {
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;
  const layerRef = useRef<{ id: number; zIndex: number } | null>(null);

  if (!layerRef.current) {
    ensureModalEscapeListener();
    const id = ++nextModalLayerID;
    layerRef.current = { id, zIndex: 20 + modalLayers.length + 1 };
  }

  useEffect(() => {
    const layer = layerRef.current!;
    const entry: ModalLayer = {
      id: layer.id,
      onClose: () => onCloseRef.current(),
    };
    modalLayers.push(entry);
    return () => {
      const index = modalLayers.findIndex((item) => item.id === layer.id);
      if (index >= 0) modalLayers.splice(index, 1);
    };
  }, []);

  return (
    <div
      className={`modal-backdrop${blocking ? '' : ' non-blocking'}`}
      style={{ zIndex: layerRef.current.zIndex }}
      onMouseDown={blocking ? () => onCloseRef.current() : undefined}
    >
      <div className={`modal-card${size === 'wide' ? ' wide' : ''}`} onMouseDown={(event) => event.stopPropagation()}>
        <div className="modal-header">
          <div>
            <h2>{title}</h2>
            <p>{description}</p>
          </div>
          <button className="icon-btn" onClick={() => onCloseRef.current()}>关闭</button>
        </div>
        {children}
      </div>
    </div>
  );
}

createRoot(document.getElementById('root')!).render(<App />);
