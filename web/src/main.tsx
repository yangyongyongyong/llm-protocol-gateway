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
  tunnelConfigFile?: string;
  publicBaseUrl?: string;
  uiPublicBaseUrl?: string;
  status: string;
  statusMessage: string;
  tunnel?: TunnelRuntime;
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
  defaultModel: string;
  defaultThinkingDepth?: string;
  models?: Model[];
  healthStatus: string;
  authType?: 'api_key' | 'claude_oauth' | 'cursor_oauth';
  claudeOAuth?: ClaudeOAuthInfo;
  cursorOAuth?: CursorOAuthInfo;
  requestAdapter?: RequestAdapter;
};

type ProvidersImportResult = {
  created: string[];
  updated: string[];
  skipped: string[];
  errors: string[];
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
  streamEnabled?: boolean;
  enabled: boolean;
  createdAt: string;
  lastUsedAt?: string;
};

type Model = {
  id: string;
  providerId: string;
  protocol: Protocol;
  contextLength: number;
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
};

type LogEntry = {
  time: string;
  apiKeyId?: string;
  apiKeyName?: string;
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
  clientHost?: string;
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

type DailyRequestPoint = {
  date: string;
  requestCount: number;
  inputTokens: number;
  outputTokens: number;
  cacheTokens: number;
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
  };
  month: {
    period: string;
    total: APIKeyDayStats;
    byApiKey: APIKeyDayStats[];
    byProvider?: ProviderDayStats[];
  };
  range?: {
    period: string;
    total: APIKeyDayStats;
    byApiKey: APIKeyDayStats[];
    byProvider?: ProviderDayStats[];
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

const defaultProviderChatTestOptions: ProviderChatTestOptions = {
  systemPrompt: '你数学老师,下面问你一些问题',
  userPrompt: '1+1等于几',
  thinkingField: 'reasoning_effort',
  thinkingValue: 'medium',
};

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
      { key: 'reasoning_effort', label: 'reasoning_effort', presets: ['low', 'medium', 'high'], custom: true },
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
const navItems = [
  { id: 'input-providers', label: '输入 Provider' },
  { id: 'models-menu', label: '模型列表' },
  { id: 'api-keys', label: 'API 密钥' },
  { id: 'output-providers', label: '输出 Provider' },
  { id: 'usage-stats', label: '用量统计' },
  { id: 'public-access', label: '公网访问' },
  { id: 'traffic-tokens', label: '流量与 Token' },
  { id: 'settings', label: '设置' },
] as const;
const navIcons = ['◉', '☰', '🔑', '⌘', '▣', '↗', '≡', '⚙'];
type NavItemID = typeof navItems[number]['id'];

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

function isTrafficLogError(log: LogEntry) {
  return log.status >= 400 || Boolean(log.errorDescription?.trim()) || Boolean(log.responseBody?.trim());
}

function formatTrafficLogDetail(log: LogEntry) {
  const lines = [
    '=== Traffic Request Log Detail ===',
    `time: ${new Date(log.time).toLocaleString()}`,
    `status: HTTP ${log.status}`,
    `apiKey: ${trafficLogKeyLabel(log)}${log.apiKeyId ? ` (${log.apiKeyId})` : ''}`,
    `route: ${log.routeId}`,
    `provider: ${log.providerId}`,
    `model: ${log.model}`,
    `action: ${log.action}`,
    `protocolFlow: ${log.protocolFlow}`,
    `path: ${log.path}`,
    `latency: ${log.latencyMs}ms`,
    `tokens: in=${log.inputTokens} out=${log.outputTokens}${log.cacheTokens ? ` cache=${log.cacheTokens}` : ''}`,
  ];
  if (log.errorDescription) lines.push(`error: ${log.errorDescription}`);
  if (log.requestBody) lines.push('', '--- Request Body ---', formatJsonDisplay(log.requestBody));
  if (log.responseBody) lines.push('', '--- Response Body ---', formatJsonDisplay(log.responseBody));
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
  const lines = [
    result.summary || 'Cache 测试完成',
    result.cacheHitTokens != null ? `cacheHitTokens: ${result.cacheHitTokens}` : '',
    result.usageRound1 ? `Round 1 usage:\n${formatJsonDisplay(JSON.stringify(result.usageRound1))}` : '',
    result.usageRound2 ? `Round 2 usage:\n${formatJsonDisplay(JSON.stringify(result.usageRound2))}` : '',
  ].filter(Boolean);
  return lines.join('\n\n');
}

function formatProviderThinkingTestDetail(result: ProviderThinkingTestResult) {
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

function healthStatusLabel(status: string) {
  switch (status) {
    case 'healthy': return '正常';
    case 'failed': return '失败';
    case 'degraded': return '降级';
    case 'standby': return '待机';
    case 'unchecked': return '未检测';
    default: return status || '未检测';
  }
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

function buildApiKeyPatchBody(key: APIKey, patch: Partial<APIKey> = {}) {
  return {
    name: (patch.name ?? key.name).trim(),
    routeId: patch.routeId ?? key.routeId,
    modelOverride: patch.modelOverride ?? key.modelOverride ?? '',
    modelAliases: patch.modelAliases ?? key.modelAliases ?? {},
    thinkingDepthOverride: patch.thinkingDepthOverride ?? key.thinkingDepthOverride ?? '',
    streamEnabled: patch.streamEnabled ?? key.streamEnabled ?? true,
    enabled: patch.enabled ?? key.enabled,
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

function formatTokenSummary(stats: Pick<APIKeyDayStats, 'inputTokens' | 'outputTokens' | 'cacheTokens'>) {
  return `入(含缓存) ${formatTokenCount(stats.inputTokens)} · 出 ${formatTokenCount(stats.outputTokens)} · 缓存命中 ${formatTokenCount(stats.cacheTokens || 0)}`;
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
      },
      month: {
        period: legacy.date.slice(0, 7),
        total: legacy.total,
        byApiKey: legacy.byApiKey || [],
        byProvider: [],
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
  const userPrompt = options.userPrompt.trim() || '1+1等于几';
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
    messages: [{ role: 'user', content: options.userPrompt.trim() || '1+1等于几' }],
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

function UsageBarChart({ title, items }: { title: string; items: Array<{ label: string; value: number }> }) {
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
              <span className="usage-bar-value">{item.value}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function UsageStackedTokens({ title, input, output, cache }: { title: string; input: number; output: number; cache: number }) {
  const total = Math.max(1, input + output + cache);
  return (
    <div className="usage-chart-card">
      <div className="usage-section-title">{title}</div>
      <div className="usage-stack-bar">
        <div style={{ width: `${(input / total) * 100}%`, background: '#2563eb' }} title={`输入 ${input}`} />
        <div style={{ width: `${(output / total) * 100}%`, background: '#059669' }} title={`输出 ${output}`} />
        <div style={{ width: `${(cache / total) * 100}%`, background: '#d97706' }} title={`缓存 ${cache}`} />
      </div>
      <div className="hint-line">入 {formatTokenCount(input)} · 出 {formatTokenCount(output)} · 缓存 {formatTokenCount(cache)}</div>
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

function App() {
  const [activeNav, setActiveNav] = useState<NavItemID>(() => navIDFromPath(window.location.pathname));
  const [themeMode, setThemeMode] = useState<ThemeMode>(() => readStoredTheme());
  const [resolvedTheme, setResolvedTheme] = useState<'light' | 'dark'>(() => resolveTheme(readStoredTheme()));
  const [state, setState] = useState<GatewayState>(fallbackState);
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const [logsTotal, setLogsTotal] = useState(0);
  const [logsPage, setLogsPage] = useState(1);
  const [logsStatusFilter, setLogsStatusFilter] = useState<'all' | '2xx' | '4xx' | '5xx'>('all');
  const [logsApiKeyName, setLogsApiKeyName] = useState('');
  const [logsFrom, setLogsFrom] = useState('');
  const [logsTo, setLogsTo] = useState('');
  const [requestLogRetentionDays, setRequestLogRetentionDays] = useState(7);
  const [usageFrom, setUsageFrom] = useState(() => new Date().toISOString().slice(0, 10));
  const [usageTo, setUsageTo] = useState(() => new Date().toISOString().slice(0, 10));
  const [trafficLogDetail, setTrafficLogDetail] = useState<LogEntry | null>(null);
  const [requestStats, setRequestStats] = useState<RequestStatsSnapshot | null>(null);
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
  const [tunnelBusy, setTunnelBusy] = useState(false);
  const [backendConnected, setBackendConnected] = useState(false);
  const [backendReconnecting, setBackendReconnecting] = useState(false);
  const [authStatus, setAuthStatus] = useState<AdminAuthStatus | null>(null);
  const [authChecked, setAuthChecked] = useState(false);
  const [authPassword, setAuthPassword] = useState('');
  const [authPasswordConfirm, setAuthPasswordConfirm] = useState('');
  const [authBusy, setAuthBusy] = useState(false);
  const [authError, setAuthError] = useState('');
  const [adminCurrentPassword, setAdminCurrentPassword] = useState('');
  const [adminNewPassword, setAdminNewPassword] = useState('');
  const [adminNewPasswordConfirm, setAdminNewPasswordConfirm] = useState('');
  const [adminPasswordBusy, setAdminPasswordBusy] = useState(false);
  const [chatTestOpen, setChatTestOpen] = useState(false);
  const [chatTestContext, setChatTestContext] = useState<ChatTestContext | null>(null);
  const [chatTestModel, setChatTestModel] = useState('');
  const [chatTestMessage, setChatTestMessage] = useState('ping from UI');
  const [chatTestResult, setChatTestResult] = useState<RouteTestResult | null>(null);
  const [chatTestLoading, setChatTestLoading] = useState(false);
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
  const [apiKeyFilterID, setApiKeyFilterID] = useState('__all__');
  const [apiKeyProviderFilter, setApiKeyProviderFilter] = useState('__all__');
  const [apiKeyOutputProtocolFilter, setApiKeyOutputProtocolFilter] = useState('__all__');
  const [providerDraft, setProviderDraft] = useState({
    name: '我的 OpenAI 对话 Provider',
    protocol: 'openai_chat' as Protocol,
    baseUrl: 'https://example.com/v1/chat/completions',
    apiKeySource: '',
    defaultModel: '',
    defaultThinkingDepth: '',
    authType: 'api_key' as 'api_key' | 'claude_oauth' | 'cursor_oauth',
    requestAdapterJSON: '',
  });
  const [claudeOAuthState, setClaudeOAuthState] = useState('');
  const [claudeOAuthCode, setClaudeOAuthCode] = useState('');
  const [claudeOAuthBusy, setClaudeOAuthBusy] = useState(false);
  const [claudeOAuthError, setClaudeOAuthError] = useState('');
  const [claudeOAuthFlowId, setClaudeOAuthFlowId] = useState('');
  const [claudeOAuthPolling, setClaudeOAuthPolling] = useState(false);
  const [cursorOAuthBusy, setCursorOAuthBusy] = useState(false);
  const [cursorOAuthError, setCursorOAuthError] = useState('');
  const [cursorOAuthFlowId, setCursorOAuthFlowId] = useState('');
  const [cursorOAuthPolling, setCursorOAuthPolling] = useState(false);
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
    streamEnabled: true,
  });
  const [modelsProviderFilter, setModelsProviderFilter] = useState('__all__');

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
  const recentActivityCutoffMs = useMemo(() => Date.now() - 3 * 24 * 60 * 60 * 1000, [logs, requestStats]);

  const recentProviderRequestCounts = useMemo(() => {
    const counts = new Map<string, number>();
    for (const log of logs) {
      const ts = new Date(log.time).getTime();
      if (!Number.isFinite(ts) || ts < recentActivityCutoffMs) continue;
      const providerID = log.providerId || '';
      if (!providerID) continue;
      counts.set(providerID, (counts.get(providerID) || 0) + 1);
    }
    // Also fold month/range stats when logs page is sparse.
    for (const item of requestStats?.range?.byProvider || requestStats?.month?.byProvider || []) {
      if (!item.providerId) continue;
      const current = counts.get(item.providerId) || 0;
      if (item.requestCount > current) counts.set(item.providerId, item.requestCount);
    }
    return counts;
  }, [logs, requestStats, recentActivityCutoffMs]);

  // Per-model request counts over the last 3 days (for models-menu ranking).
  const recentModelRequestCounts = useMemo(() => {
    const counts = new Map<string, number>();
    for (const log of logs) {
      const ts = new Date(log.time).getTime();
      if (!Number.isFinite(ts) || ts < recentActivityCutoffMs) continue;
      const modelID = (log.model || '').split('->').pop()?.trim() || log.model || '';
      if (!modelID) continue;
      // Key by provider+model so the same model id under different providers ranks separately.
      const key = `${log.providerId || ''}::${modelID}`;
      counts.set(key, (counts.get(key) || 0) + 1);
      counts.set(modelID, (counts.get(modelID) || 0) + 1);
    }
    return counts;
  }, [logs, recentActivityCutoffMs]);

  const sortedProviders = useMemo(() => {
    return [...state.providers].sort((a, b) => {
      const ca = recentProviderRequestCounts.get(a.id) || 0;
      const cb = recentProviderRequestCounts.get(b.id) || 0;
      if (ca !== cb) return cb - ca;
      return a.name.localeCompare(b.name, 'zh');
    });
  }, [state.providers, recentProviderRequestCounts]);

  const modelsMenuProviders = useMemo(() => {
    const providerIDs = new Set(state.models.map((model) => model.providerId));
    // Always show Cursor/Claude OAuth providers that are connected, even before
    // their first successful model sync, so the filter chip is visible.
    return sortedProviders.filter((provider) => {
      if (providerIDs.has(provider.id)) return true;
      if (provider.authType === 'cursor_oauth' && provider.cursorOAuth?.connected) return true;
      if (provider.authType === 'claude_oauth' && provider.claudeOAuth?.connected) return true;
      return false;
    });
  }, [state.models, sortedProviders]);
  const filteredModels = useMemo(() => {
    const base = modelsProviderFilter === '__all__'
      ? state.models
      : state.models.filter((model) => model.providerId === modelsProviderFilter);
    return [...base].sort((a, b) => {
      const ca = recentModelRequestCounts.get(`${a.providerId}::${a.id}`) || recentModelRequestCounts.get(a.id) || 0;
      const cb = recentModelRequestCounts.get(`${b.providerId}::${b.id}`) || recentModelRequestCounts.get(b.id) || 0;
      if (ca !== cb) return cb - ca;
      return a.id.localeCompare(b.id);
    });
  }, [state.models, modelsProviderFilter, recentModelRequestCounts]);
  const modelsMenuSummary = useMemo(() => {
    const counts = new Map<string, number>();
    for (const model of state.models) {
      counts.set(model.providerId, (counts.get(model.providerId) || 0) + 1);
    }
    return counts;
  }, [state.models]);

  const filteredApiKeys = useMemo(() => {
    let keys = state.apiKeys || [];
    if (apiKeyProviderFilter !== '__all__') {
      keys = keys.filter((key) => {
        const route = state.routes.find((item) => item.id === key.routeId);
        return route?.providerId === apiKeyProviderFilter;
      });
    }
    if (apiKeyOutputProtocolFilter !== '__all__') {
      keys = keys.filter((key) => {
        const route = state.routes.find((item) => item.id === key.routeId);
        return route?.outputProtocol === apiKeyOutputProtocolFilter;
      });
    }
    if (apiKeyFilterID !== '__all__') {
      keys = keys.filter((key) => key.id === apiKeyFilterID);
    }
    return keys;
  }, [state.apiKeys, state.routes, apiKeyFilterID, apiKeyProviderFilter, apiKeyOutputProtocolFilter]);

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
      const auth = await refreshAuthStatus();
      setAuthChecked(true);
      if (auth && (!auth.requireAuth || auth.authenticated)) {
        await bootstrapAuthenticatedSession();
      }
    })();
    // Tunnel restore is async after gateway start; keep UI in sync with live
    // __state (App WebView and browser otherwise diverge after first paint).
    const timer = window.setInterval(() => {
      void (async () => {
        const connected = await refreshBackendHealth();
        if (!connected) {
          await reconnectBackend(false);
          return;
        }
        const auth = await refreshAuthStatus();
        if (auth && (!auth.requireAuth || auth.authenticated)) {
          void refreshState(false);
          void refreshRequestStats();
        }
      })();
    }, 3000);
    return () => window.clearInterval(timer);
  }, []);

  useEffect(() => {
    const nextPublicAccess = { ...defaultPublicAccess, ...state.publicAccess };
    setPublicDraft(nextPublicAccess);
    // Only sync subdomain prefixes from persisted hostnames. When domains are
    // empty (pre-bind), keep the user's in-progress edits — the 3s health poll
    // would otherwise reset them to gateway/console on every refresh.
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
      // Keep previously saved lucadesign.uk only when already present in settings.
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
      return data;
    } catch {
      setAuthStatus(null);
      return null;
    }
  }

  async function bootstrapAuthenticatedSession() {
    await refreshState(false);
    await refreshLogs();
    await refreshRequestStats();
    await refreshAppLogs();
    await refreshCloudflareAuthStatus();
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
        body: JSON.stringify({ password: authPassword }),
      });
      if (!response.ok) {
        const text = await response.text();
        throw new Error(text || `HTTP ${response.status}`);
      }
      const data = await response.json() as AdminAuthStatus;
      setAuthStatus(data);
      setAuthPassword('');
      setAuthPasswordConfirm('');
      if (window.location.pathname === '/login') {
        window.history.replaceState({}, '', '/');
        setActiveNav('input-providers');
      }
      await bootstrapAuthenticatedSession();
      showToast(mode === 'setup' ? '管理员密码已设置' : '登录成功');
    } catch (error) {
      setAuthError(String(error));
    } finally {
      setAuthBusy(false);
    }
  }

  async function logoutAdmin() {
    setAuthBusy(true);
    try {
      await fetch(`${API_BASE}/__auth/logout`, { method: 'POST', credentials: 'same-origin' });
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
        await refreshState(false);
        await refreshLogs();
        await refreshRequestStats();
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
      setState((current) => ({
        ...data,
        apiKeys: data.apiKeys || [],
        publicAccess: {
          ...defaultPublicAccess,
          ...data.publicAccess,
          // Keep live tunnel snapshot if a concurrent refresh races with start/stop.
          tunnel: data.publicAccess?.tunnel ?? current.publicAccess.tunnel,
        },
      }));
      setSelectedRouteID((current) => current || data.routes[0]?.id || '');
      setSelectedProviderID((current) => current || data.providers[0]?.id || '');
      setSelectedOutputProtocol((current) => data.routes[0]?.outputProtocol || current);
      if (data.requestLogRetentionDays && data.requestLogRetentionDays > 0) {
        setRequestLogRetentionDays(data.requestLogRetentionDays);
      }
      setBackendConnected(true);
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

  async function refreshLogs(page = logsPage) {
    try {
      const params = new URLSearchParams();
      params.set('page', String(page));
      params.set('pageSize', '100');
      params.set('status', logsStatusFilter);
      if (logsFrom) params.set('from', logsFrom);
      if (logsTo) params.set('to', logsTo);
      if (logsApiKeyName.trim()) params.set('apiKeyName', logsApiKeyName.trim());
      const response = await fetch(`${API_BASE}/__logs?${params.toString()}`);
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const data = await response.json() as LogPage | LogEntry[];
      if (Array.isArray(data)) {
        setLogs(data);
        setLogsTotal(data.length);
        setLogsPage(1);
      } else {
        setLogs(data.items || []);
        setLogsTotal(data.total || 0);
        setLogsPage(data.page || page);
      }
      await refreshRequestStats();
    } catch {
      // Keep UI usable when backend is down.
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

  async function refreshRequestStats(from = usageFrom, to = usageTo) {
    try {
      const params = new URLSearchParams();
      if (from) params.set('from', from);
      if (to) params.set('to', to);
      const response = await fetch(`${API_BASE}/__request-stats?${params.toString()}`);
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      setRequestStats(normalizeRequestStats(await response.json() as RequestStatsSnapshot | LegacyRequestStatsSnapshot));
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
    setChatTestLoading(true);
    setChatTestResult(null);
    setCacheTestResult(null);
    setThinkingTestResult(null);
    setCacheTestOpen(false);
    setThinkingTestOpen(false);
    try {
      if (chatTestContext.kind === 'route') {
        const response = await fetch(`${API_BASE}/__routes/${encodeURIComponent(chatTestContext.id)}/test`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ model: chatTestModel.trim(), message: chatTestMessage.trim() }),
        });
        const result = await response.json() as RouteTestResult;
        setChatTestResult(result);
        showToast(result.success ? `对话测试成功：HTTP ${result.status}` : `对话测试未通过：${result.status || result.error || 'unknown'}`);
      } else {
        const providerPath = `${API_BASE}/__providers/${encodeURIComponent(chatTestContext.id)}`;
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
        const [mainResponse, cacheResponse, thinkingResponse] = await Promise.all([
          fetch(`${providerPath}/chat-test`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(baseBody),
          }),
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
        const result = await mainResponse.json() as RouteTestResult;
        const cacheResult = await cacheResponse.json() as ProviderCacheTestResult;
        const thinkingResult = await thinkingResponse.json() as ProviderThinkingTestResult;
        setChatTestResult(result);
        setCacheTestResult(cacheResult);
        setThinkingTestResult(thinkingResult);
        setCacheTestOpen(true);
        setThinkingTestOpen(true);
        showToast(result.success ? `对话测试成功：HTTP ${result.status}` : `对话测试未通过：${result.status || result.error || 'unknown'}`);
      }
      await refreshLogs();
      await refreshAppLogs();
    } catch (error) {
      setChatTestResult({ success: false, error: String(error) });
      showToast(`对话测试失败：${String(error)}`);
    } finally {
      setChatTestLoading(false);
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
      title: `输出 Provider 对话测试 · ${route.name}`,
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
      description: '直连上游 Provider 的对话接口，验证 Provider 本身是否可用。运行测试时将自动执行 Cache 与 Thinking 后台测试。',
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
    if (provider.authType === 'claude_oauth' || provider.authType === 'cursor_oauth') return;
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

  function resetClaudeOAuthFlowState() {
    setClaudeOAuthState('');
    setClaudeOAuthCode('');
    setClaudeOAuthBusy(false);
    setClaudeOAuthError('');
    setClaudeOAuthFlowId('');
    setClaudeOAuthPolling(false);
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
    });
    resetClaudeOAuthFlowState();
    resetCursorOAuthFlowState();
    setProviderModalOpen(true);
  }

  function resolveProviderAuthType(provider: Provider): 'api_key' | 'claude_oauth' | 'cursor_oauth' {
    if (provider.authType === 'claude_oauth') return 'claude_oauth';
    if (provider.authType === 'cursor_oauth') return 'cursor_oauth';
    return 'api_key';
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
    });
    resetClaudeOAuthFlowState();
    resetCursorOAuthFlowState();
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
    });
    resetClaudeOAuthFlowState();
    resetCursorOAuthFlowState();
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
        authType: isClaudeOAuth ? 'claude_oauth' : isCursorOAuth ? 'cursor_oauth' : 'api_key',
        requestAdapter,
      };
      const response = await fetch(editingProviderID ? `${API_BASE}/__providers/${encodeURIComponent(editingProviderID)}` : `${API_BASE}/__providers`, {
        method: editingProviderID ? 'PATCH' : 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      });
      if (!response.ok) throw new Error(await response.text());
      const saved = await response.json() as Provider;
      const wasCreate = !editingProviderID;
      setProviderModalOpen(false);
      setEditingProviderID('');
      resetClaudeOAuthFlowState();
      resetCursorOAuthFlowState();
      if (wasCreate && isClaudeOAuth) {
        showToast(`已添加输入 Provider：${saved.name}。在列表中点击编辑可连接 Claude 账号。`);
      } else if (wasCreate && isCursorOAuth) {
        showToast(`已添加输入 Provider：${saved.name}。在列表中点击编辑可连接 Cursor 账号。`);
      } else {
        showToast(wasCreate ? `已添加输入 Provider：${saved.name}` : `已更新输入 Provider：${saved.name}`);
      }
      await refreshState(false);
      await refreshAppLogs();
      setSelectedProviderID(saved.id);
    } catch (error) {
      showToast(`${editingProviderID ? '更新' : '添加'} Provider 失败：${String(error)}`);
    } finally {
      setSaving(false);
    }
  }

  async function pollClaudeOAuthStatus(flowId: string) {
    const deadline = Date.now() + 15 * 60 * 1000;
    setClaudeOAuthPolling(true);
    try {
      while (Date.now() < deadline) {
        const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(editingProviderID!)}/claude-oauth/status?flowId=${encodeURIComponent(flowId)}`);
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

  async function startClaudeOAuthConnect() {
    if (!editingProviderID) return;
    setClaudeOAuthBusy(true);
    setClaudeOAuthError('');
    try {
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(editingProviderID)}/claude-oauth/start`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ mode: 'localhost' }),
      });
      if (!response.ok) throw new Error(await response.text());
      const data = await response.json() as { authUrl: string; state: string; flowId?: string; mode?: string };
      setClaudeOAuthState(data.state);
      if (data.flowId) setClaudeOAuthFlowId(data.flowId);
      const opened = window.open(data.authUrl, '_blank', 'noopener,noreferrer');
      if (!opened) {
        setClaudeOAuthError('浏览器拦截了弹窗，请复制下方授权链接手动打开。');
      }
      try {
        await navigator.clipboard.writeText(data.authUrl);
        if (opened) showToast('已打开授权页面，授权完成后将自动连接');
      } catch {
        if (opened) showToast('已打开 Claude 授权页面');
      }
      if (data.flowId) {
        setClaudeOAuthBusy(false);
        await pollClaudeOAuthStatus(data.flowId);
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

  async function pollCursorOAuthStatus(flowId: string) {
    const deadline = Date.now() + 15 * 60 * 1000;
    setCursorOAuthPolling(true);
    try {
      while (Date.now() < deadline) {
        const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(editingProviderID!)}/cursor-oauth/status?flowId=${encodeURIComponent(flowId)}`);
        if (!response.ok) {
          const text = await response.text();
          throw new Error(text || 'oauth status check failed');
        }
        const data = await response.json() as { status: string; message?: string };
        if (data.status === 'connected') {
          const providerID = editingProviderID!;
          resetCursorOAuthFlowState();
          // OAuth finish syncs models server-side before status=connected;
          // force one more models refresh so the models menu updates immediately.
          await fetchProviderModels(providerID, 'cursor pro', false);
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

  async function startCursorOAuthConnect() {
    if (!editingProviderID) return;
    setCursorOAuthBusy(true);
    setCursorOAuthError('');
    try {
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(editingProviderID)}/cursor-oauth/start`, { method: 'POST' });
      if (!response.ok) throw new Error(await response.text());
      const data = await response.json() as { authUrl: string; flowId?: string };
      if (data.flowId) setCursorOAuthFlowId(data.flowId);
      const opened = window.open(data.authUrl, '_blank', 'noopener,noreferrer');
      if (!opened) {
        setCursorOAuthError('浏览器拦截了弹窗，请复制授权链接手动打开。');
      }
      try {
        await navigator.clipboard.writeText(data.authUrl);
        if (opened) showToast('已打开 Cursor 授权页面，完成后将自动连接');
      } catch {
        if (opened) showToast('已打开 Cursor 授权页面');
      }
      if (data.flowId) {
        setCursorOAuthBusy(false);
        await pollCursorOAuthStatus(data.flowId);
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

  async function deleteProvider(providerID: string, providerName: string) {
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
          streamEnabled: apiKeyDraft.streamEnabled,
          enabled: true,
        }),
      });
      if (!response.ok) throw new Error(await response.text());
      const created = await response.json() as APIKey;
      setApiKeyModalOpen(false);
      setSelectedApiKeyID(created.id);
      setApiKeyFilterID(created.id);
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
    setSaving(true);
    try {
      const route = await ensureRouteForBinding(providerId, outputProtocol);
      const response = await fetch(`${API_BASE}/__apikeys/${encodeURIComponent(key.id)}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(buildApiKeyPatchBody(key, {
          routeId: route.id,
          modelOverride: '',
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

  async function updateApiKeyField(key: APIKey, field: 'name' | 'routeId' | 'modelOverride' | 'thinkingDepthOverride' | 'streamEnabled' | 'enabled', value: string | boolean) {
    setSaving(true);
    try {
      const patch: Partial<APIKey> = {};
      if (field === 'name') patch.name = String(value).trim();
      if (field === 'routeId') patch.routeId = String(value);
      if (field === 'modelOverride') patch.modelOverride = String(value);
      if (field === 'thinkingDepthOverride') patch.thinkingDepthOverride = String(value);
      if (field === 'streamEnabled') patch.streamEnabled = Boolean(value);
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

  async function deleteApiKey(key: APIKey) {
    if (!window.confirm(`确定删除 API 密钥：${key.name}？`)) return;
    setSaving(true);
    try {
      const response = await fetch(`${API_BASE}/__apikeys/${encodeURIComponent(key.id)}`, { method: 'DELETE' });
      if (!response.ok) throw new Error(await response.text());
      showToast(`已删除 API 密钥：${key.name}`);
      if (selectedApiKeyID === key.id) {
        setSelectedApiKeyID('');
        setApiKeyFilterID('__all__');
      }
      await refreshState(false);
      await refreshAppLogs();
    } catch (error) {
      showToast(`删除 API 密钥失败：${String(error)}`);
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
  const activeProviderRouteCount = selectedProvider ? (state.apiKeys || []).filter((key) => {
    const route = state.routes.find((item) => item.id === key.routeId);
    return route?.providerId === selectedProvider.id;
  }).length : 0;
  const apiKeyDraftProvider = state.providers.find((item) => item.id === apiKeyDraft.providerId);
  const apiKeyDraftModels = apiKeyDraftProvider ? state.models.filter((model) => model.providerId === apiKeyDraftProvider.id) : [];
  const refreshingApiKeyModels = apiKeyDraftProvider ? testingProviderID === apiKeyDraftProvider.id : false;
  const chatTestRoute = chatTestContext?.kind === 'route' ? state.routes.find((item) => item.id === chatTestContext.id) : undefined;
  const chatTestProvider = chatTestContext?.kind === 'provider'
    ? state.providers.find((item) => item.id === chatTestContext.id)
    : chatTestRoute ? state.providers.find((item) => item.id === chatTestRoute.providerId) : undefined;
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

  if (!authChecked) {
    return (
      <div className="auth-shell">
        <div className="auth-card">
          <div className="brand-title">协议网关</div>
          <div className="hint-line">正在检查登录状态…</div>
        </div>
      </div>
    );
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

  return (
    <>
      <div className="shell">
        <aside className="sidebar">
          <div className="brand">
            <div className="brand-logo">PG</div>
            <div>
              <div className="brand-title">协议网关</div>
              <div className="brand-subtitle">协议入 · 协议出</div>
            </div>
          </div>

          {(!backendConnected || backendReconnecting) && (
            <button
              type="button"
              className={`status-pill status-pill-btn ${backendConnected ? '' : 'off'} ${backendReconnecting ? 'reconnecting' : ''}`}
              onClick={() => { if (!backendConnected && !backendReconnecting) void reconnectBackend(true); }}
              title="点击尝试重新连接后端"
            >
              <span className="dot" />
              {backendReconnecting ? '重连中…' : '后端未连接'}
            </button>
          )}

          <ThemeSwitch value={themeMode} onChange={setThemeMode} size="compact" />

          <nav className="nav">
            {navItems.map((item, index) => (
              <a
                className={`nav-item ${activeNav === item.id ? 'active' : ''}`}
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
            ))}
          </nav>
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
                    <label>筛选密钥</label>
                    <select
                      value={apiKeyFilterID}
                      onChange={(event) => {
                        const value = event.target.value;
                        setApiKeyFilterID(value);
                        if (value !== '__all__') {
                          setSelectedApiKeyID(value);
                        }
                      }}
                    >
                      <option value="__all__">全部密钥（{(state.apiKeys || []).length}）</option>
                      {(state.apiKeys || []).map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
                    </select>
                  </div>
                  <div className="field api-keys-filter-field">
                    <label>筛选 Provider</label>
                    <select
                      value={apiKeyProviderFilter}
                      onChange={(event) => {
                        setApiKeyProviderFilter(event.target.value);
                        setApiKeyFilterID('__all__');
                      }}
                    >
                      <option value="__all__">全部 Provider</option>
                      {state.providers.map((item) => <option key={item.id} value={item.id}>{providerOptionLabel(item)}</option>)}
                    </select>
                  </div>
                  <div className="field api-keys-filter-field">
                    <label>筛选输出协议</label>
                    <select
                      value={apiKeyOutputProtocolFilter}
                      onChange={(event) => {
                        setApiKeyOutputProtocolFilter(event.target.value);
                        setApiKeyFilterID('__all__');
                      }}
                    >
                      <option value="__all__">全部输出协议</option>
                      {fixedOutputLabels.map((label) => <option key={label} value={protocolFromLabel(label)}>{label}</option>)}
                    </select>
                  </div>
                  <div className="api-keys-toolbar-meta">
                    显示 {filteredApiKeys.length} / {(state.apiKeys || []).length} 个密钥
                  </div>
                </div>
              ) : null}
              {(state.apiKeys || []).length === 0 ? (
                <div className="empty-state">暂无 API 密钥。点击「新建 API 密钥」生成 sk-gw-… 密钥。</div>
              ) : (
                <div className="api-keys-layout">
                  <div className="api-keys-table-wrap">
                    <div className="api-keys-table">
                      <div className="api-keys-table-head">
                        <span>名称</span>
                      </div>
                      {filteredApiKeys.length === 0 ? (
                        <div className="empty-state compact">当前筛选条件下没有匹配的密钥。</div>
                      ) : filteredApiKeys.map((key) => {
                        return (
                          <button
                            type="button"
                            key={key.id}
                            className={`api-keys-row${selectedApiKey?.id === key.id ? ' active' : ''}`}
                            onClick={() => setSelectedApiKeyID(key.id)}
                          >
                            <span className="api-keys-cell name">{key.name}</span>
                          </button>
                        );
                      })}
                    </div>
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
                      onDelete={deleteApiKey}
                      onClone={openCloneApiKeyModal}
                      onRefreshModels={refreshApiKeyModelsForProvider}
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
                  <p className="panel-desc">用户自定义添加的上游 Provider。删除前会检查是否被 API 密钥引用。列表按近 3 日请求量排序。支持勾选后导出/导入配置（含 apiKeySource 与已持久化的 OAuth 元数据）。</p>
                </div>
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
              </div>
              {sortedProviders.length > 0 ? (
                <div className="providers-toolbar">
                  <label className="checkbox-field">
                    <input
                      type="checkbox"
                      checked={selectedExportProviderIDs.length > 0 && selectedExportProviderIDs.length === sortedProviders.length}
                      onChange={(event) => {
                        if (event.target.checked) selectAllExportProviders();
                        else clearExportProviderSelection();
                      }}
                    />
                    <span>全选</span>
                  </label>
                  <span className="providers-toolbar-meta">已选 {selectedExportProviderIDs.length} / {sortedProviders.length}</span>
                  {selectedExportProviderIDs.length > 0 ? (
                    <button className="mini-btn" type="button" onClick={clearExportProviderSelection}>清除选择</button>
                  ) : null}
                </div>
              ) : null}
              {sortedProviders.length === 0 ? <div className="empty-state">暂无 Provider。点击「添加输入 Provider」创建。</div> : (
                <div className="provider-card-grid">
                  {sortedProviders.map((provider) => {
                    const usedCount = (state.apiKeys || []).filter((key) => {
                      const route = state.routes.find((item) => item.id === key.routeId);
                      return route?.providerId === provider.id;
                    }).length;
                    return (
                      <ProviderCard
                        key={provider.id}
                        active={selectedProvider?.id === provider.id}
                        selected={selectedExportProviderIDs.includes(provider.id)}
                        name={provider.name}
                        providerId={provider.id}
                        protocol={protocolLabel(provider.protocol)}
                        tone={protocolTone(provider.protocol)}
                        url={provider.authType === 'claude_oauth' ? 'Claude OAuth (api.anthropic.com)' : provider.authType === 'cursor_oauth' ? 'Cursor OAuth (本地 gRPC bridge)' : `${provider.baseUrl} · ${provider.apiKeySource || '透传客户端 Authorization'}`}
                        usedCount={usedCount}
                        healthStatus={provider.healthStatus || 'unchecked'}
                        testing={testingProviderID === provider.id}
                        isClaudeOAuth={provider.authType === 'claude_oauth'}
                        claudeOAuthConnected={provider.claudeOAuth?.connected}
                        isCursorOAuth={provider.authType === 'cursor_oauth'}
                        cursorOAuthConnected={provider.cursorOAuth?.connected}
                        onToggleSelect={() => toggleExportProviderSelection(provider.id)}
                        onClick={() => {
                          setSelectedProviderID(provider.id);
                          showToast(`已选择输入 Provider：${provider.name}`);
                        }}
                        onTest={() => void fetchProviderModels(provider.id, provider.name, true)}
                        onChatTest={() => openChatTestForProvider(provider)}
                        onEdit={() => openEditProviderModal(provider)}
                        onClone={() => openCloneProviderModal(provider)}
                        onDelete={() => void deleteProvider(provider.id, provider.name)}
                      />
                    );
                  })}
                </div>
              )}
              {selectedProvider && <div className="hint-line">当前 Provider 被 {activeProviderRouteCount} 个 API 密钥引用。引用数不为 0 时不能删除。</div>}
            </div>
          </section>
          )}

          {activeNav === 'output-providers' && (
          <section className="section-grid">
            <div className="card panel">
              <div className="panel-header">
                <div>
                  <h2 className="panel-title">固定输出 Provider</h2>
                  <p className="panel-desc">应用固定提供三种输出协议入口；公网访问在「公网访问」页统一配置。流式开关已移至 API Key（与 Key 绑定）。</p>
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
                  <p className="panel-desc">按日期区间汇总请求与 Token；默认今天。图表为纯 SVG，不依赖额外库。</p>
                </div>
                <button className="btn" onClick={() => void refreshRequestStats(usageFrom, usageTo)}>刷新统计</button>
              </div>

              <div className="form-grid compact" style={{ marginBottom: 12 }}>
                <label className="field">
                  <span>开始日期</span>
                  <input type="date" value={usageFrom} onChange={(e) => setUsageFrom(e.target.value)} />
                </label>
                <label className="field">
                  <span>结束日期</span>
                  <input type="date" value={usageTo} onChange={(e) => setUsageTo(e.target.value)} />
                </label>
                <div className="field" style={{ display: 'flex', alignItems: 'flex-end' }}>
                  <button className="btn" type="button" onClick={() => void refreshRequestStats(usageFrom, usageTo)}>应用区间</button>
                </div>
              </div>

              <div className="grid-4">
                <Metric label="区间总请求" value={String(requestStats?.range?.total.requestCount ?? usageToday?.total.requestCount ?? 0)} note={requestStats?.range?.period || usageToday?.date || '—'} />
                <Metric label="区间总 Token" value={formatTokenCount(((requestStats?.range?.total.inputTokens ?? usageToday?.total.inputTokens ?? 0) + (requestStats?.range?.total.outputTokens ?? usageToday?.total.outputTokens ?? 0)))} note={requestStats?.range?.total ? formatTokenSummary(requestStats.range.total) : (usageToday?.total ? formatTokenSummary(usageToday.total) : '暂无数据')} />
                <Metric label="本月总请求" value={String(usageMonth?.total.requestCount ?? 0)} note={usageMonth?.period || '—'} />
                <Metric label="本月总 Token" value={formatTokenCount((usageMonth?.total.inputTokens ?? 0) + (usageMonth?.total.outputTokens ?? 0))} note={usageMonth?.total ? formatTokenSummary(usageMonth.total) : '暂无数据'} />
              </div>

              <div className="usage-charts">
                <UsageLineChart title="按日请求量" points={requestStats?.daily || []} />
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
                <UsageStackedTokens
                  title="Token 构成（区间）"
                  input={requestStats?.range?.total.inputTokens ?? usageToday?.total.inputTokens ?? 0}
                  output={requestStats?.range?.total.outputTokens ?? usageToday?.total.outputTokens ?? 0}
                  cache={requestStats?.range?.total.cacheTokens ?? usageToday?.total.cacheTokens ?? 0}
                />
                <UsageStatusChart title="状态码分布" items={requestStats?.status || []} />
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
            </div>
          </section>
          )}

          {activeNav === 'public-access' && (
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
                  <p className="panel-desc">查看全部模型，或按输入 Provider 过滤。在 Provider 卡片点击「获取模型」可同步最新列表。</p>
                </div>
                <button className="btn" onClick={() => void refreshState()}>刷新列表</button>
              </div>
              <div className="models-toolbar">
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
                </div>
              </div>
              {state.models.length === 0 ? (
                <div className="empty-state">暂无模型。点击输入 Provider 卡片上的「获取模型」后，会根据 Provider 接口自动拉取模型列表。</div>
              ) : filteredModels.length === 0 ? (
                <div className="empty-state">
                  该 Provider 暂无模型记录。
                  {(() => {
                    const provider = state.providers.find((item) => item.id === modelsProviderFilter);
                    if (!provider) return null;
                    const canSync = provider.authType === 'cursor_oauth'
                      ? !!provider.cursorOAuth?.connected
                      : provider.authType === 'claude_oauth'
                        ? !!provider.claudeOAuth?.connected
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
              <div className="panel-header"><div><h2 className="panel-title">流量请求日志</h2><p className="panel-desc">支持按时间段、状态与密钥名称筛选；展示访问来源与首 Token 延迟（TTFT）。默认保留 {requestLogRetentionDays} 天。</p></div><button className="btn" onClick={() => void refreshLogs(1)}>刷新日志</button></div>
              <div className="form-grid compact" style={{ marginBottom: 12 }}>
                <label className="field">
                  <span>开始日期</span>
                  <input type="date" value={logsFrom} onChange={(e) => setLogsFrom(e.target.value)} />
                </label>
                <label className="field">
                  <span>结束日期</span>
                  <input type="date" value={logsTo} onChange={(e) => setLogsTo(e.target.value)} />
                </label>
                <label className="field">
                  <span>状态</span>
                  <select value={logsStatusFilter} onChange={(e) => setLogsStatusFilter(e.target.value as typeof logsStatusFilter)}>
                    <option value="all">全部</option>
                    <option value="2xx">2xx</option>
                    <option value="4xx">4xx</option>
                    <option value="5xx">5xx</option>
                  </select>
                </label>
                <label className="field">
                  <span>密钥名称</span>
                  <input
                    list="traffic-api-key-names"
                    type="text"
                    value={logsApiKeyName}
                    placeholder="全部，或输入/选择名称"
                    onChange={(e) => setLogsApiKeyName(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter') {
                        e.preventDefault();
                        setLogsPage(1);
                        void refreshLogs(1);
                      }
                    }}
                  />
                  <datalist id="traffic-api-key-names">
                    {(state.apiKeys || []).map((key) => (
                      <option key={key.id} value={key.name} />
                    ))}
                  </datalist>
                </label>
                <div className="field" style={{ display: 'flex', alignItems: 'flex-end', gap: 8 }}>
                  <button className="btn" type="button" onClick={() => { setLogsPage(1); void refreshLogs(1); }}>应用筛选</button>
                  {logsApiKeyName ? (
                    <button className="btn" type="button" onClick={() => { setLogsApiKeyName(''); setLogsPage(1); void refreshLogs(1); }}>清除密钥</button>
                  ) : null}
                </div>
              </div>
              <div className="log-table">
                {logs.length === 0 ? <div className="empty-state">暂无流量日志。运行路由测试或真实转发请求后会记录。</div> : (
                  <>
                    <div className="log-header traffic-log-header">
                      <span>时间</span>
                      <span>状态</span>
                      <span>来源</span>
                      <span>密钥</span>
                      <span>模型</span>
                      <span>Token</span>
                      <span>TTFT</span>
                      <span>耗时</span>
                      <span />
                    </div>
                    {logs.map((log, index) => (
                      <div className={`log-row${isTrafficLogError(log) ? ' error' : ''}`} key={`${log.time}-${index}`}>
                        <div className="log-row-main traffic-log-row">
                          <span className="log-time">{new Date(log.time).toLocaleString()}</span>
                          <Badge tone={statusTone(log.status)}>{log.status}</Badge>
                          <span className="log-source" title={log.clientHost || undefined}>{accessSourceLabel(log.accessSource)}</span>
                          <span className="log-key" title={log.apiKeyId || undefined}>{trafficLogKeyLabel(log)}</span>
                          <span className="log-model">{log.model}</span>
                          <span className="log-token" title="入=总 input（含缓存命中）；缓存=cache hit">入 {log.inputTokens} · 出 {log.outputTokens} · 缓存 {log.cacheTokens || 0}</span>
                          <span className="log-latency">{log.ttftMs != null ? `${log.ttftMs}ms` : '—'}</span>
                          <span className="log-latency">{log.latencyMs}ms</span>
                          {isTrafficLogError(log) ? (
                            <button className="mini-btn" type="button" onClick={() => setTrafficLogDetail(log)}>详情</button>
                          ) : <span className="log-detail-placeholder" />}
                        </div>
                        <div className="log-row-sub" title={`${log.protocolFlow} · ${log.path}`}>
                          {actionLabel(log.action)} · {log.protocolFlow} · {log.path}{log.errorDescription ? ` · ${log.errorDescription}` : ''}
                        </div>
                      </div>
                    ))}
                    <div className="hint-line" style={{ display: 'flex', gap: 12, alignItems: 'center', justifyContent: 'space-between' }}>
                      <span>共 {logsTotal} 条 · 第 {logsPage} 页</span>
                      <span style={{ display: 'flex', gap: 8 }}>
                        <button className="mini-btn" type="button" disabled={logsPage <= 1} onClick={() => void refreshLogs(logsPage - 1)}>上一页</button>
                        <button className="mini-btn" type="button" disabled={logsPage * 100 >= logsTotal} onClick={() => void refreshLogs(logsPage + 1)}>下一页</button>
                      </span>
                    </div>
                  </>
                )}
              </div>
            </div>
          </section>
          )}

          {activeNav === 'settings' && (
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
              <span>{trafficLogKeyLabel(trafficLogDetail)} · {trafficLogDetail.routeId} · {trafficLogDetail.model} · {trafficLogDetail.latencyMs}ms</span>
            </div>
            {trafficLogDetail.errorDescription ? <div className="hint-line error">{trafficLogDetail.errorDescription}</div> : null}
            <div className="field-label-row">
              <label>完整诊断信息</label>
              <CopyButton value={formatTrafficLogDetail(trafficLogDetail)} label="复制全部" />
            </div>
            <pre className="json-preview">{formatTrafficLogDetail(trafficLogDetail)}</pre>
          </div>
          <div className="actions modal-actions">
            <button className="btn" onClick={() => setTrafficLogDetail(null)}>关闭</button>
          </div>
        </Modal>
      )}

      {cacheTestOpen && cacheTestResult && (
        <Modal title="Cache 测试结果" description="两轮会话：第二轮包含第一轮上下文，检查 usage 中的 cache 命中字段。" onClose={() => setCacheTestOpen(false)}>
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
        <Modal title="Thinking 测试结果" description="按协议注入 thinking 相关字段，并展示上游响应。" onClose={() => setThinkingTestOpen(false)}>
          <div className={`test-result-card ${thinkingTestResult.success ? 'ok' : 'fail'}`}>
            <div className="test-result-head">
              <Badge tone={thinkingTestResult.success ? 'green' : statusTone(thinkingTestResult.status)}>{testResultBadge(thinkingTestResult.success)}</Badge>
              <span>{httpStatusLabel(thinkingTestResult.status)} · {thinkingTestResult.latencyMs ?? '-'}ms</span>
            </div>
            <div className="hint-line">字段：{thinkingTestResult.thinkingField || '-'} · 值：{thinkingTestResult.thinkingValue || '-'}</div>
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
        <Modal title={editingProviderID ? '编辑输入 Provider' : '创建输入 Provider'} description="API Key Source 可留空：留空时透传客户端 Authorization；也可直接填 sk-xxx，或填 env:VAR_NAME / literal:sk-xxx。Fallback Model 只在模型接口不可用时兜底。" onClose={() => { setProviderModalOpen(false); setEditingProviderID(''); resetClaudeOAuthFlowState(); resetCursorOAuthFlowState(); }}>
          <div className="form-grid modal-form">
            <Field label="Provider 名称" value={providerDraft.name} onChange={(value) => setProviderDraft((current) => ({ ...current, name: value }))} />
            <SelectField label="协议" values={fixedOutputLabels} value={protocolLabel(providerDraft.protocol)} onChange={(value) => setProviderDraft((current) => ({ ...current, protocol: protocolFromLabel(value), authType: 'api_key' }))} />
            {providerDraft.protocol === 'claude' && (
              <SelectField
                label="连接方式"
                values={['API Key', '登录 Claude 账号 (OAuth)']}
                value={providerDraft.authType === 'claude_oauth' ? '登录 Claude 账号 (OAuth)' : 'API Key'}
                onChange={(value) => setProviderDraft((current) => ({ ...current, authType: value === '登录 Claude 账号 (OAuth)' ? 'claude_oauth' : 'api_key' }))}
              />
            )}
            {providerDraft.protocol === 'openai_chat' && (
              <SelectField
                label="连接方式"
                values={['API Key', '登录 Cursor 账号 (OAuth)']}
                value={providerDraft.authType === 'cursor_oauth' ? '登录 Cursor 账号 (OAuth)' : 'API Key'}
                onChange={(value) => setProviderDraft((current) => ({ ...current, authType: value === '登录 Cursor 账号 (OAuth)' ? 'cursor_oauth' : 'api_key' }))}
              />
            )}
            {!(providerDraft.protocol === 'claude' && providerDraft.authType === 'claude_oauth') && !(providerDraft.protocol === 'openai_chat' && providerDraft.authType === 'cursor_oauth') && (
              <>
                <Field fullWidth label="Base URL" value={providerDraft.baseUrl} onChange={(value) => setProviderDraft((current) => ({ ...current, baseUrl: value }))} />
                <Field fullWidth label="API Key Source（可选）" value={providerDraft.apiKeySource} onChange={(value) => setProviderDraft((current) => ({ ...current, apiKeySource: value }))} />
              </>
            )}
            <Field label="兜底模型（可选）" value={providerDraft.defaultModel} onChange={(value) => setProviderDraft((current) => ({ ...current, defaultModel: value }))} />
            <div className="field"><label>默认思考深度（可选）</label><select value={providerDraft.defaultThinkingDepth} onChange={(event) => setProviderDraft((current) => ({ ...current, defaultThinkingDepth: event.target.value }))}>
              <option value="">（不指定）</option>
              <option value="low">low</option>
              <option value="medium">medium</option>
              <option value="high">high</option>
            </select></div>
          </div>
          {providerDraft.protocol === 'claude' && providerDraft.authType === 'claude_oauth' && (
            <div className="claude-oauth-panel">
              {!editingProviderID ? (
                <div className="hint-line">保存后即可连接 Claude 账号（OAuth 连接需要先创建 Provider 获得 ID）。</div>
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
                <div className="hint-line">保存后即可连接 Cursor 账号（OAuth 连接需要先创建 Provider 获得 ID）。需要本机安装 bun。</div>
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
          <div className="actions modal-actions"><button className="btn" onClick={() => { setProviderModalOpen(false); setEditingProviderID(''); resetClaudeOAuthFlowState(); resetCursorOAuthFlowState(); }}>取消</button><button className="btn primary" disabled={saving} onClick={() => void createProvider()}>{saving ? '保存中…' : editingProviderID ? '保存修改' : '创建 Provider'}</button></div>
        </Modal>
      )}

      {chatTestOpen && chatTestContext && (
        <Modal title={chatTestContext.title} description={chatTestContext.description} onClose={() => setChatTestOpen(false)}>
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
                <div className="hint-line">点击「运行测试」将同时执行主对话、Cache（两轮会话）与 Thinking 测试；后两者结果在弹窗中展示。</div>
                {chatTestProvider && providerThinkingPresets ? (
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
              {chatTestContext.kind === 'provider' && chatTestProvider?.authType !== 'claude_oauth' && chatTestProvider?.authType !== 'cursor_oauth' && !providerAuthPreview?.value ? (
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
        <Modal title={editingRouteID ? '编辑路由' : '创建路由'} description={editingRouteID ? '可临时切换输入 Provider 或输出协议，保存后立即生效。' : '路由只描述协议转发：自定义输入 Provider → 固定输出 Provider。模型与思考深度覆盖请在「API 密钥」页按密钥配置。'} onClose={() => { setRouteModalOpen(false); setEditingRouteID(''); }}>
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
              onChange={(value) => setApiKeyDraft((current) => ({ ...current, outputProtocol: protocolFromLabel(value), modelOverride: '' }))}
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
                <option value="">（不覆盖）</option>
                <option value="low">low</option>
                <option value="medium">medium</option>
                <option value="high">high</option>
              </select>
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
          <button className="icon-btn" onClick={(event) => { event.stopPropagation(); onTest(); }} title="输出 Provider 对话测试">对话测试</button>
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

function ClaudeOAuthUsagePanel({ providerId, connected, compact }: { providerId: string; connected?: boolean; compact?: boolean }) {
  const [report, setReport] = React.useState<ClaudeOAuthUsageReport | null>(null);
  const [loading, setLoading] = React.useState(false);

  React.useEffect(() => {
    if (!connected) {
      setReport(null);
      return undefined;
    }
    let cancelled = false;
    const load = async () => {
      setLoading(true);
      try {
        const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(providerId)}/claude-oauth/usage`);
        if (!response.ok) {
          throw new Error(`HTTP ${response.status}`);
        }
        const data = await response.json() as ClaudeOAuthUsageReport;
        if (!cancelled) setReport(data);
      } catch (error) {
        if (!cancelled) {
          setReport({ available: false, error: error instanceof Error ? error.message : '无法获取额度' });
        }
      } finally {
        if (!cancelled) setLoading(false);
      }
    };
    void load();
    const timer = window.setInterval(() => { void load(); }, 60_000);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [providerId, connected]);

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
        {loading ? <span className="claude-usage-status">刷新中…</span> : report?.fetchedAt ? <span className="claude-usage-status">更新于 {formatClaudeUsageResetAt(report.fetchedAt)}</span> : null}
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

function CursorOAuthUsagePanel({ providerId, connected, compact }: { providerId: string; connected?: boolean; compact?: boolean }) {
  const [report, setReport] = React.useState<CursorOAuthUsageReport | null>(null);
  const [loading, setLoading] = React.useState(false);

  React.useEffect(() => {
    if (!connected) {
      setReport(null);
      return undefined;
    }
    let cancelled = false;
    const load = async () => {
      setLoading(true);
      try {
        const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(providerId)}/cursor-oauth/usage`);
        if (!response.ok) {
          throw new Error(`HTTP ${response.status}`);
        }
        const data = await response.json() as CursorOAuthUsageReport;
        if (!cancelled) setReport(data);
      } catch (error) {
        if (!cancelled) {
          setReport({ available: false, error: error instanceof Error ? error.message : '无法获取额度' });
        }
      } finally {
        if (!cancelled) setLoading(false);
      }
    };
    void load();
    const timer = window.setInterval(() => { void load(); }, 60_000);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [providerId, connected]);

  if (!connected) return null;

  const buckets = report?.buckets || [];

  return (
    <div className={`claude-usage-panel cursor-usage-panel${compact ? ' compact' : ''}`} onClick={(event) => event.stopPropagation()}>
      <div className="claude-usage-title">
        <span>Cursor 订阅额度</span>
        {loading ? <span className="claude-usage-status">刷新中…</span> : report?.fetchedAt ? <span className="claude-usage-status">更新于 {formatClaudeUsageResetAt(report.fetchedAt)}</span> : null}
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

function ProviderCard({ active, selected, name, providerId, protocol, tone, url, usedCount, healthStatus, testing, isClaudeOAuth, claudeOAuthConnected, isCursorOAuth, cursorOAuthConnected, onToggleSelect, onClick, onTest, onChatTest, onEdit, onClone, onDelete }: { active?: boolean; selected?: boolean; name: string; providerId: string; protocol: string; tone: BadgeTone; url: string; usedCount: number; healthStatus: string; testing: boolean; isClaudeOAuth?: boolean; claudeOAuthConnected?: boolean; isCursorOAuth?: boolean; cursorOAuthConnected?: boolean; onToggleSelect: () => void; onClick: () => void; onTest: () => void; onChatTest: () => void; onEdit: () => void; onClone: () => void; onDelete: () => void }) {
  const oauthConnected = isClaudeOAuth ? claudeOAuthConnected : isCursorOAuth ? cursorOAuthConnected : false;
  const showOAuthBadge = isClaudeOAuth || isCursorOAuth;
  return (
    <div className={`provider-card clickable ${active ? 'active' : ''} ${selected ? 'selected' : ''}`} onClick={onClick} role="button" tabIndex={0} onKeyDown={(event) => { if (event.key === 'Enter') onClick(); }}>
      <div className="provider-head">
        <div className="provider-title-block">
          <label className="provider-select checkbox-field" onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()}>
            <input type="checkbox" checked={!!selected} onChange={onToggleSelect} aria-label={`选择 ${name}`} />
            <span className="provider-name">{name}</span>
          </label>
          <div className="provider-subtitle">{providerId} · {protocol}</div>
        </div>
        <div className="provider-badges">
          <Badge tone={tone}>{protocol}</Badge>
          {showOAuthBadge ? (
            <Badge tone={oauthConnected ? 'green' : 'amber'}>{oauthConnected ? 'OAuth 已连接' : 'OAuth 未连接'}</Badge>
          ) : (
            <Badge tone={healthTone(healthStatus)}>{healthStatusLabel(healthStatus)}</Badge>
          )}
          <Badge tone={usedCount > 0 ? 'amber' : 'slate'}>{usedCount} 个 API Key</Badge>
        </div>
      </div>
      <div className="provider-meta">{url}</div>
      {isClaudeOAuth && claudeOAuthConnected ? <ClaudeOAuthUsagePanel providerId={providerId} connected={claudeOAuthConnected} compact /> : null}
      {isCursorOAuth && cursorOAuthConnected ? <CursorOAuthUsagePanel providerId={providerId} connected={cursorOAuthConnected} compact /> : null}
      <div className="provider-actions">
        <button className="icon-btn" disabled={testing} onClick={(event) => { event.stopPropagation(); onTest(); }} title="从 Provider 接口获取可用模型">{testing ? '获取中' : '获取模型'}</button>
        <button className="icon-btn" onClick={(event) => { event.stopPropagation(); onChatTest(); }} title="直连上游对话接口测试">对话测试</button>
        <button className="icon-btn" onClick={(event) => { event.stopPropagation(); onEdit(); }} title="编辑 Provider">编辑</button>
        <button className="icon-btn" onClick={(event) => { event.stopPropagation(); onClone(); }} title="克隆为新 Provider">克隆</button>
        <button className="icon-btn danger" disabled={usedCount > 0} onClick={(event) => { event.stopPropagation(); onDelete(); }} title={usedCount > 0 ? '该 Provider 正被 API Key 引用' : '删除 Provider'}>删除</button>
      </div>
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
  onDelete,
  onClone,
  onRefreshModels,
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
  onUpdateField: (key: APIKey, field: 'name' | 'routeId' | 'modelOverride' | 'thinkingDepthOverride' | 'streamEnabled' | 'enabled', value: string | boolean) => Promise<void>;
  onUpdateBinding: (key: APIKey, providerId: string, outputProtocol: Protocol) => Promise<void>;
  onUpdateModelAliases: (key: APIKey, modelAliases: Record<string, string>) => Promise<void>;
  onDelete: (key: APIKey) => Promise<void>;
  onClone: (key: APIKey) => void;
  onRefreshModels: (providerId: string, providerName: string) => Promise<void>;
}) {
  const { route, binding, routeProvider, bindingAction } = getApiKeyBinding(keyItem, routes, providers);
  const modelOptions = routeProvider ? models.filter((model) => model.providerId === routeProvider.id) : [];
  const apiKeyClientURL = route ? apiKeyClientBaseURL(route, endpoints, tunnelRunning ? livePublicURL : '') : '';

  return (
    <div className="api-keys-detail card">
      <div className="route-top api-keys-detail-head">
        <div className="route-name">{keyItem.name}</div>
        <div className="route-actions">
          <Badge tone={keyItem.enabled ? 'green' : 'slate'}>{keyItem.enabled ? '启用' : '禁用'}</Badge>
          <Badge tone={bindingAction === '透传' ? 'green' : 'cyan'}>{bindingAction}</Badge>
          {route ? <CopyButton value={apiKeyClientURL} label="复制 URL" /> : null}
          <CopyButton value={keyItem.key} label="复制 Key" />
          <button className="icon-btn" onClick={() => onClone(keyItem)} title="克隆为新 API 密钥">克隆</button>
          <button className="icon-btn danger" onClick={() => void onDelete(keyItem)} title="删除">删除</button>
        </div>
      </div>
      <div className="form-grid compact">
        <ApiKeyNameField
          name={keyItem.name}
          disabled={saving}
          onSave={(name) => onUpdateField(keyItem, 'name', name)}
        />
        <div className="field">
          <label>输入 Provider</label>
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
            <option value="">（不覆盖）</option>
            <option value="low">low</option>
            <option value="medium">medium</option>
            <option value="high">high</option>
          </select>
        </div>
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
    </div>
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
        <select value={value} disabled={disabled} onChange={(event) => onChange(event.target.value)}>
          <option value="">（不替换，使用请求体 model）</option>
          {models.map((model) => <option key={model.id} value={model.id}>{model.id}</option>)}
        </select>
        <button className="mini-btn" type="button" disabled={disabled || refreshing} onClick={onRefresh} title="从绑定路由的 Provider 重新获取模型列表">
          {refreshing ? '刷新中…' : '刷新模型'}
        </button>
      </div>
      <div className="hint-line">设置后将忽略请求体中的 model，统一替换为所选模型。</div>
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

function CopyButton({ value, label = '复制' }: { value: string; label?: string }) {
  const [copied, setCopied] = React.useState(false);
  return (
    <button
      className={`mini-btn${copied ? ' copied' : ''}`}
      type="button"
      onClick={() => {
        void navigator.clipboard.writeText(value).then(() => {
          setCopied(true);
          window.setTimeout(() => setCopied(false), 2000);
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

function SelectField({ label, values, value, onChange }: { label: string; values: string[]; value?: string; onChange: (value: string) => void }) {
  return <div className="field"><label>{label}</label><select value={value || values[0] || ''} onChange={(event) => onChange(event.target.value)}>{values.map((item) => <option key={item}>{item}</option>)}</select></div>;
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

function Modal({ title, description, children, onClose, size = 'default' }: { title: string; description: string; children: React.ReactNode; onClose: () => void; size?: 'default' | 'wide' }) {
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
    <div className="modal-backdrop" style={{ zIndex: layerRef.current.zIndex }} onMouseDown={() => onCloseRef.current()}>
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
