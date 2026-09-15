// @generated-from main.tsx — 由重构脚本拆分生成，请直接维护本文件。
import React from 'react';
import { APIKey, APIKeyDayStats, AdminAuthStatus, BadgeTone, CloudflareZoneOption, GatewayState, HostDiskTemp, HostFanSpeed, HostMetrics, LegacyRequestStatsSnapshot, LogEntry, Model, NavItemID, OutputEndpoint, Protocol, Provider, ProviderAuthPreview, ProviderCacheTestResult, ProviderChatTestOptions, ProviderConnectKind, ProviderThinkingTestResult, PublicAccessSettings, RequestAdapter, RequestStatsSnapshot, Route, RouteTestResult, SelfcheckCaseResult, SelfcheckPrefs } from './types';
export const REQUEST_ADAPTER_PRESETS: Array<{ id: string; label: string; hint: string; json: string }> = [
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

export function compactRequestAdapterJSON(raw: string): string {
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

export function previewRequestAdapterCurl(baseUrl: string, defaultModel: string, adapterJSON: string): string {
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

export const PROVIDER_CACHE_ROUND2_USER = '继续';

/** Codex 本地 model catalog 支持的档位：Codex 不认 max，会被折成 xhigh。 */
export const THINKING_DEPTH_OPTIONS = ['low', 'medium', 'high', 'xhigh'] as const;

/**
 * 控制台可选的完整推理强度阶梯。网关侧 normalizeReasoningEffort 一直支持 max，
 * Qoder（docs.qoder.com/cli/model 的 /effort：low/medium/high/xhigh/max）与
 * Anthropic Opus 4.6+ 也都认 max，所以下拉要给到 max。
 * 不认 max 的上游会在各自的 map 函数里自行降级到 high。
 */
export const SELECTABLE_THINKING_DEPTHS = ['low', 'medium', 'high', 'xhigh', 'max'] as const;

export const CODEX_MODEL_CATALOG_REL = '.codex/lpg-model-catalog.json';

export const CODEX_MODEL_CATALOG_DISPLAY = `~/${CODEX_MODEL_CATALOG_REL}`;

export const defaultProviderChatTestOptions: ProviderChatTestOptions = {
  systemPrompt: 'x=10',
  userPrompt: 'x+5等于几',
  thinkingField: 'reasoning_effort',
  thinkingValue: 'medium',
};

export function thinkingDepthSelectOptions(includeEmpty: { value: string; label: string }) {
  return (
    <>
      <option value={includeEmpty.value}>{includeEmpty.label}</option>
      {SELECTABLE_THINKING_DEPTHS.map((depth) => (
        <option key={depth} value={depth}>{depth}</option>
      ))}
    </>
  );
}

export function thinkingPresetsForProtocol(protocol: Protocol) {
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

export function defaultThinkingValueForField(protocol: Protocol, field: string) {
  const presets = thinkingPresetsForProtocol(protocol);
  const match = presets.fields.find((item) => item.key === field) || presets.fields[0];
  if (field === 'thinking.type') return 'enabled';
  if (field === 'reasoning_effort') return 'medium';
  return match?.presets[0] || '';
}

export const API_BASE = '';

// 后端连通性判定与轮询节奏。
//
// 背景：经公网自定义域名（Cloudflare 具名隧道）访问时，实测单次请求的 TLS 握手
// 约 1.0s、首字节约 1.5s，而本地直连只要约 1.7ms——差三个数量级。原先「任何一次
// fetch 抛异常就立刻标记未连接」在局域网下没问题，放到公网就会被偶发抖动、边缘
// 节点切换、标签页休眠频繁误触发，表现为界面一直在「重连中…」。
//
// 对策：① 连续失败达到阈值才判定断线；② 公网访问时放慢轮询；③ 每个探测请求
// 都带超时，避免慢请求堆积把后续轮询一起拖垮。
export const BACKEND_FAIL_STREAK_LIMIT = 3;

export const BACKEND_POLL_MS_LOCAL = 5000;

export const BACKEND_POLL_MS_REMOTE = 15000;

export const BACKEND_PROBE_TIMEOUT_MS = 8000;

/** 判断当前页面是否经公网域名访问（非 localhost / 内网地址）。 */
export function isRemoteOrigin() {
  if (typeof window === 'undefined') return false;
  const host = window.location.hostname;
  if (!host) return false;
  if (host === 'localhost' || host === '127.0.0.1' || host === '::1' || host.endsWith('.local')) return false;
  // 常见内网网段：10/8、192.168/16、172.16-31/12
  if (/^10\./.test(host) || /^192\.168\./.test(host)) return false;
  if (/^172\.(1[6-9]|2\d|3[01])\./.test(host)) return false;
  return true;
}

/** 带超时的 fetch。超时后抛 AbortError，调用方按普通失败处理。 */
export function fetchWithTimeout(input: string, init: RequestInit = {}, timeoutMs = BACKEND_PROBE_TIMEOUT_MS) {
  return fetch(input, { ...init, signal: AbortSignal.timeout(timeoutMs) });
}

/**
 * 解析连接类接口的响应，返回可直接展示的错误。
 *
 * 两类失败必须区分开：
 *
 * 1. 链路层：网关重启期间 Cloudflare 隧道未就绪、反代超时、会话过期跳登录页——
 *    响应体是 HTML，无条件 `response.json()` 会抛
 *    `Unexpected token '<', "<!DOCTYPE "...`，把真实原因盖住。
 * 2. 业务层：后端把上游失败也映射成 502（例如 PAT 格式错误时
 *    "qoder job token exchange failed with HTTP 400: invalid personal token
 *    format"），此时响应体是 JSON，必须原样透出后端消息——否则用户会被告知
 *    "隧道不可达，请稍后重试"，而实际上重试一万次也没用。
 *
 * 因此只以 content-type 作为分流依据，绝不单看状态码。
 */
/** 上报一条 UI 诊断事件到后端应用日志（GET /__app/logs 可见）。fire-and-forget。 */
export function reportUIDiag(event: string, detail: string) {
  try {
    void fetch(`${API_BASE}/__ui-log`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify({ event, detail }),
    }).catch(() => {});
  } catch {
    // 诊断通道自身绝不能影响业务
  }
}

/**
 * 挡住浏览器（尤其 Safari）顽固的账号自动填充。
 *
 * 实测（ui-diag 日志）：`autoComplete="one-time-code"` + `data-1p-ignore` 等属性
 * 组合仍被绕过——浏览器原生派发了 trusted=true、inputType=(none) 的填充事件，
 * 是自动填充的典型指纹，而非用户真实输入。
 *
 * 浏览器的自动填充扫描发生在页面加载 / DOM 变动那一刻；此时字段若是 `readOnly`
 * 会被直接跳过。真正聚焦（用户主动点击/Tab 进来）时才摘掉 `readOnly`，扫描窗口
 * 已经错过。这是绕过 autocomplete 属性失效时的可靠兜底。
 */
export function antiAutofillProps() {
  return {
    readOnly: true,
    onFocus: (event: React.FocusEvent<HTMLInputElement>) => {
      event.target.removeAttribute('readonly');
    },
  } as const;
}

/** 供筛选框 onChange 里调用：识别并上报疑似自动填充的变更（诊断用，不拦截）。 */
export function reportIfLooksLikeAutofill(diagName: string, previous: string, next: string, event: React.ChangeEvent<HTMLInputElement>) {
  if (previous === '' && next !== '' && event.nativeEvent instanceof InputEvent) {
    const native = event.nativeEvent;
    if (!native.inputType) {
      reportUIDiag(diagName, `value=${next.slice(0, 24)} trusted=${native.isTrusted} inputType=(none)`);
    }
  }
}

export async function readConnectResponse(response: Response, fallbackMessage: string) {
  const contentType = (response.headers.get('content-type') || '').toLowerCase();
  // 诊断:记录浏览器真实收到的状态/类型/体前缀,终结"到底是谁返回的 502"之争。
  {
    let preview = '';
    try { preview = (await response.clone().text()).slice(0, 160); } catch { /* 诊断不阻塞业务 */ }
    reportUIDiag('connect-response', `status=${response.status} ct=${contentType} body=${preview}`);
  }
  if (contentType.includes('json')) {
    // 后端应答（含它用 4xx/5xx 表达的业务错误）：原样透出真实原因。
    const data = await response.json().catch(() => null);
    if (!response.ok) {
      throw new Error(data?.error?.message || data?.error || `${fallbackMessage}（HTTP ${response.status}）`);
    }
    return data;
  }
  // 非 JSON：请求没到达后端，或到达前就被中间层截断。
  if (response.status === 502 || response.status === 503 || response.status === 504 || response.status === 522) {
    throw new Error(`网关暂时不可达（HTTP ${response.status}）。若刚重启过服务，公网隧道通常需要 30-60 秒重建，请稍后重试。`);
  }
  if (response.status === 401 || response.status === 403) {
    throw new Error(`登录状态已失效（HTTP ${response.status}），请刷新页面重新登录后再试。`);
  }
  throw new Error(`服务返回了非预期内容（HTTP ${response.status}），请稍后重试。`);
}

// Qoder 直连端点。后端 normalizeProvider 只在 baseUrl 留空时兜这个默认值
// （保留手动覆盖入口），所以切到 Qoder 时前端要主动把占位 URL 换掉，
// 否则会残留创建表单的 example.com 预填值。
export const QODER_DEFAULT_BASE_URL = 'https://api2-v2.qoder.sh/model/v1';

export const navItems = [
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

// 核心配置项：新用户只需依次配好这两个即可使用，侧边栏置顶并加底色区分。
export const coreNavIDs: NavItemID[] = ['input-providers', 'api-keys'];

// 侧边栏导航分组：组内所有项都被角色过滤掉时，整组（含组标题）不渲染。
export const navGroups: Array<{ label: string; ids: NavItemID[] }> = [
  { label: '核心配置', ids: ['input-providers', 'api-keys', 'output-providers'] },
  { label: '数据洞察', ids: ['models-menu', 'usage-stats', 'traffic-tokens'] },
  { label: '系统管理', ids: ['public-access', 'alerts', 'users', 'self-check', 'machine', 'settings'] },
];

// 普通用户仅可访问的页面（其余仅管理员可见）
export const userAllowedNavIDs: NavItemID[] = ['input-providers', 'models-menu', 'api-keys', 'traffic-tokens', 'usage-stats'];

// 侧边栏收起状态持久化 key（独立于主题，避免互相覆盖）
export const SIDEBAR_STORAGE_KEY = 'llm-gateway-sidebar';

export function readStoredSidebarCollapsed(): boolean {
  try {
    return localStorage.getItem(SIDEBAR_STORAGE_KEY) === '1';
  } catch {
    return false;
  }
}

export function writeStoredSidebarCollapsed(collapsed: boolean) {
  try {
    localStorage.setItem(SIDEBAR_STORAGE_KEY, collapsed ? '1' : '0');
  } catch {
    // ignore
  }
}

export function navPathForID(id: NavItemID) {
  return id === 'input-providers' ? '/' : `/${id}`;
}

export function navIDFromPath(pathname: string): NavItemID {
  const normalized = pathname.replace(/\/+$/, '') || '/';
  if (normalized === '/login') return 'settings';
  if (normalized === '/' || normalized === '/overview' || normalized === '/input-providers') return 'input-providers';
  const match = navItems.find((item) => normalized === `/${item.id}`);
  return match?.id ?? 'input-providers';
}

export const fixedOutputLabels = ['OpenAI Chat', 'OpenAI Responses', 'Claude'];

export const logLevelValues = ['debug', 'info', 'warn', 'error'];

export const defaultPublicAccess: PublicAccessSettings = {
  enabled: false,
  provider: 'cloudflare',
  mode: 'random_tunnel',
  exposeApi: true,
  exposeUi: true,
  expose: 'all',
  status: 'disabled',
  statusMessage: '公网访问未开启。可一键开启 Cloudflare 快速隧道，或绑定已购买的 Cloudflare 域名。',
};

export const fallbackState: GatewayState = {
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
export function normalizeGatewayState(data: Partial<GatewayState> | null | undefined, current?: GatewayState): GatewayState {
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

export const UI_CACHE_PREFIX = 'llm-gateway-ui-cache:v1:';

export const UI_CACHE_TTL_MS = 24 * 60 * 60 * 1000;

export const LOGS_PAGE_SIZE = 10;

// API 密钥列表每页最多显示 30 个，密钥多时翻页浏览。
export const API_KEYS_PAGE_SIZE = 30;

// 用户管理表格列宽（表头与数据行共用，保证对齐）。
export const USERS_TABLE_GRID = 'minmax(0,0.7fr) 48px minmax(0,1.2fr) minmax(0,0.8fr) minmax(0,0.9fr) minmax(0,0.9fr) 236px';

// 与后端 internal/gateway/user_isolation.go 里的 logOwnerFilterAdmin 保持一致。
export const LOG_OWNER_FILTER_ADMIN = '_admin';

export function uiCacheScope(auth?: Pick<AdminAuthStatus, 'userId' | 'role' | 'username'> | null) {
  return auth?.userId || auth?.username || auth?.role || 'anon';
}

export function readUICache<T>(scope: string, kind: string): T | null {
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

export function writeUICache(scope: string, kind: string, data: unknown) {
  try {
    localStorage.setItem(`${UI_CACHE_PREFIX}${scope}:${kind}`, JSON.stringify({ at: Date.now(), data }));
  } catch {
    // quota / private mode — ignore
  }
}

export function clearUICache(scope: string, kind: string) {
  try {
    localStorage.removeItem(`${UI_CACHE_PREFIX}${scope}:${kind}`);
  } catch {
    // ignore
  }
}

/** 自检页偏好：无 TTL，按登录身份永久保存在本机。 */
export const SELFCHECK_PREFS_PREFIX = 'llm-gateway-selfcheck-prefs:v1:';

export function readSelfcheckPrefs(scope: string): SelfcheckPrefs | null {
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

export function writeSelfcheckPrefs(scope: string, prefs: SelfcheckPrefs) {
  try {
    localStorage.setItem(`${SELFCHECK_PREFS_PREFIX}${scope}`, JSON.stringify(prefs));
  } catch {
    // quota / private mode — ignore
  }
}

export function defaultSelfcheckModelForProvider(provider: Provider, models: Model[]): string {
  const listed = models.filter((model) => model.providerId === provider.id);
  return listed[0]?.id?.trim()
    || provider.defaultModel?.trim()
    || provider.models?.find((model) => model.id?.trim())?.id?.trim()
    || '';
}

export function modelsForSelfcheckProvider(provider: Provider, allModels: Model[]): Model[] {
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

export function trafficRanksFromMap(map: Map<string, number>): Record<string, number> {
  const out: Record<string, number> = {};
  for (const [key, value] of map) {
    if (value > 0) out[key] = value;
  }
  return out;
}

export function trafficRanksEqual(a: Record<string, number>, b: Record<string, number>) {
  const aKeys = Object.keys(a);
  const bKeys = Object.keys(b);
  if (aKeys.length !== bKeys.length) return false;
  for (const key of aKeys) {
    if ((a[key] || 0) !== (b[key] || 0)) return false;
  }
  return true;
}

export function mapFromTrafficRanks(ranks: Record<string, number> | undefined) {
  const map = new Map<string, number>();
  if (!ranks) return map;
  for (const [key, value] of Object.entries(ranks)) {
    if (value > 0) map.set(key, value);
  }
  return map;
}

/** 上次已登录会话：用于刷新页面时跳过「正在检查登录」闪屏。 */
export function loadBootSession(): { auth: AdminAuthStatus | null; state: GatewayState | null } {
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

export function maskApiKey(key: string) {
  if (key.length <= 12) return key;
  return `${key.slice(0, 8)}…${key.slice(-4)}`;
}

export function formatJsonDisplay(raw?: string) {
  const text = raw?.trim();
  if (!text) return '';
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return text;
  }
}

export function trafficLogKeyLabel(log: LogEntry) {
  return log.apiKeyName?.trim() || '未绑定 Key';
}

/**
 * 悬浮提示：先给完整的当前密钥名（列宽不够时会被省略号截断），再补一行 ID。
 * apiKeyId 是创建时按当时名字生成的固定 slug，改名后就和名字不一致了，
 * 必须标上「ID:」前缀，否则看起来像是另一把不存在的密钥。
 */
export function trafficLogKeyTitle(log: LogEntry) {
  const name = trafficLogKeyLabel(log);
  const id = log.apiKeyId?.trim();
  return id && id !== name ? `${name}\nID: ${id}` : name;
}

/**
 * 「来源」悬浮提示：IP 列已从表格移除（密钥名更需要宽度），把客户端 IP 和
 * Host 收进来源列的提示里，详情弹窗中也仍然可见，信息不丢。
 */
export function trafficLogSourceTitle(log: LogEntry) {
  const parts = [
    log.clientIp?.trim() ? `IP: ${log.clientIp.trim()}` : '',
    log.clientHost?.trim() ? `Host: ${log.clientHost.trim()}` : '',
  ].filter(Boolean);
  return parts.length > 0 ? parts.join('\n') : undefined;
}

export function trafficLogProviderLabel(log: LogEntry, providers: Provider[]) {
  return providerUsageLabel(log.providerId || '_unknown', providers);
}

export function isTrafficLogError(log: LogEntry) {
  return log.status >= 400 || Boolean(log.errorDescription?.trim()) || Boolean(log.responseBody?.trim());
}

export function formatTrafficLogDetail(log: LogEntry, providers: Provider[] = []) {
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

export function formatSelfcheckCaseDetail(row: SelfcheckCaseResult) {
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

export function formatRouteTestDiagnostics(result: RouteTestResult) {
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

export function formatChatTestResponse(result: RouteTestResult) {
  if (!result.success) return formatRouteTestDiagnostics(result);
  if (result.error) return result.error;
  const raw = result.responseBody || result.preview || '';
  if (!raw) return '无响应预览';
  return formatJsonDisplay(raw);
}

export function formatProviderCacheTestDetail(result: ProviderCacheTestResult) {
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

export function formatProviderThinkingTestDetail(result: ProviderThinkingTestResult) {
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

export function protocolLabel(protocol: Protocol) {
  switch (protocol) {
    case 'openai_chat': return 'OpenAI Chat';
    case 'openai_responses': return 'OpenAI Responses';
    case 'claude': return 'Claude';
  }
}

export function protocolFromLabel(label: string): Protocol {
  const normalized = label.trim().toLowerCase();
  if (normalized === 'openai responses' || label === 'OpenAI 响应') return 'openai_responses';
  if (normalized === 'claude' || label === 'Claude 消息') return 'claude';
  if (normalized === 'openai chat' || label === 'OpenAI 对话') return 'openai_chat';
  return 'openai_chat';
}

export function actionLabel(action: string) {
  if (action === 'not_configured') return '未配置';
  if (action === 'pass_through' || action.includes('pass')) return '透传';
  if (action === 'convert' || action.includes('convert')) return '转换';
  return action;
}

export function routeActionLabel(action: string) {
  return action === 'pass_through' ? '透传' : '转换';
}

export function testResultBadge(success?: boolean) {
  return success ? '成功' : '失败 / 跳过';
}

// 卡片上只展示密钥掩码；完整值仅在编辑弹窗可见（编辑权限已限创建人/管理员）。
export function maskApiKeySource(source?: string): string {
  const value = (source || '').trim();
  if (!value) return '透传客户端 Authorization';
  if (value.startsWith('env:')) return value;
  const raw = value.startsWith('literal:') ? value.slice('literal:'.length) : value;
  if (raw.length <= 8) return '••••••';
  return `${raw.slice(0, 4)}••••${raw.slice(-4)}`;
}

// selfRegistrationEndpointSpec 描述每种协议下，用户自建服务需要实现的接口
// 形状：路径、上游协议名称，以及一句话的关键格式提示（供生成 Prompt 使用）。
// 「连接方式」下拉框里代表自助注册模式的选项文案；三种协议共用同一个选项
// （不像 OAuth 选项那样绑定单一协议）。
export const SELF_REGISTER_CONNECT_LABEL = '内网穿透自助注册（Bearer 令牌）';

// 「连接方式」下拉里 API Key 选项的文案。括号里点明适用范围：本网关只实现了
// OpenAI Chat / OpenAI Responses / Claude 三种上游协议（见 domain.Protocol），
// 填任意平台的 key 并不等于就能转发——上游必须原生兼容这三种之一。
export const API_KEY_CONNECT_LABEL = 'API Key（仅支持 OpenAI Chat / Responses / Claude 三种协议的上游）';

// selfRegisterPlaceholderBaseURL 是创建时的占位 baseUrl：真实地址由用户自己的
// 脚本在生成令牌后通过 self-register 接口写入，这里只是先满足"创建 Provider
// 必须填 baseUrl"的校验，明显带有 pending 字样，不会被当成真实可用的上游。
export function selfRegisterPlaceholderBaseURL(protocol: Protocol): string {
  return `https://pending-self-registration.example${selfRegistrationEndpointSpec(protocol).path}`;
}

export function selfRegistrationEndpointSpec(protocol: Protocol): { path: string; label: string; hint: string } {
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
export function buildSelfRegistrationPrompt(provider: Provider, rawToken: string, originBase: string): string {
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

export function healthStatusLabel(status: string) {
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
export function retrySecondsLabel(nextRetryAt: string | undefined, nowMs: number): string | null {
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
export function useNowTick(enabled: boolean, intervalMs = 1000): number {
  const [now, setNow] = React.useState(() => Date.now());
  React.useEffect(() => {
    if (!enabled) return undefined;
    const timer = window.setInterval(() => setNow(Date.now()), intervalMs);
    return () => window.clearInterval(timer);
  }, [enabled, intervalMs]);
  return now;
}

export function cursorBridgeStatusLabel(status?: string) {
  switch (status) {
    case 'healthy': return 'bridge 正常';
    case 'starting': return 'bridge 启动中';
    case 'restarting': return 'bridge 重启中';
    case 'unhealthy': return 'bridge 异常';
    case 'stopped': return 'bridge 未启动';
    default: return status ? `bridge ${status}` : 'bridge 未启动';
  }
}

export function cursorBridgeTone(status?: string): BadgeTone {
  if (status === 'healthy') return 'green';
  if (status === 'starting' || status === 'restarting') return 'amber';
  if (status === 'unhealthy') return 'red';
  return 'slate';
}

export function tunnelStatusLabel(status?: string) {
  switch (status) {
    case 'running': return '运行中';
    case 'starting': return '启动中';
    case 'stopped': return '已停止';
    case 'error': return '异常';
    default: return status || '已停止';
  }
}

export function publicAccessMetricValue(enabled: boolean, mode: string) {
  if (!enabled) return '仅局域网';
  if (mode === 'custom_domain') return '自有域名';
  return '随机隧道';
}

export function publicAccessStatusLabel(status: string) {
  switch (status) {
    case 'runtime_url_recorded': return '已记录公网地址';
    case 'configured_pending_tunnel': return '已配置，待启动隧道';
    case 'waiting_for_tunnel': return '等待隧道';
    case 'unsupported': return '不支持';
    case 'error': return '连接失败';
    default: return status;
  }
}

export function httpStatusLabel(status?: number) {
  return status ? `HTTP ${status}` : '无 HTTP 状态';
}

export function flowBadgeTone(item: string): BadgeTone {
  if (item === 'Claude' || item === 'Claude 消息') return 'purple';
  if (item.includes('Responses') || item.includes('响应')) return 'amber';
  if (item === '转换' || item === 'Convert') return 'cyan';
  return 'blue';
}

export function providerOptionLabel(provider: Provider) {
  return `${provider.name} (${protocolLabel(provider.protocol)})`;
}

export function providerConnectKind(provider: Provider): ProviderConnectKind {
  if (provider.authType === 'claude_oauth') return 'claude_oauth';
  if (provider.authType === 'cursor_oauth') return 'cursor_oauth';
  if (provider.authType === 'chatgpt_oauth') return 'chatgpt_oauth';
  if (provider.authType === 'qoder_pat') return 'qoder_pat';
  if (provider.selfRegistration) return 'self_register';
  return 'api_key';
}

export function providerConnectLabel(kind: ProviderConnectKind): string {
  switch (kind) {
    case 'claude_oauth': return '登录 Claude 账号 (OAuth)';
    case 'cursor_oauth': return '登录 Cursor 账号 (OAuth)';
    case 'chatgpt_oauth': return '登录 ChatGPT 账号 (OAuth)';
    case 'qoder_pat': return '连接 Qoder 账号 (PAT)';
    case 'self_register': return '内网穿透自助注册（Bearer 令牌）';
    default: return 'API Key';
  }
}

export const PROVIDER_CONNECT_FILTERS: Array<{ id: '' | ProviderConnectKind; label: string }> = [
  { id: '', label: '全部连接方式' },
  { id: 'api_key', label: 'API Key' },
  { id: 'self_register', label: '内网穿透自助注册' },
  { id: 'claude_oauth', label: 'Claude OAuth' },
  { id: 'cursor_oauth', label: 'Cursor OAuth' },
  { id: 'chatgpt_oauth', label: 'ChatGPT OAuth' },
  { id: 'qoder_pat', label: 'Qoder PAT' },
];

export function buildApiKeyPatchBody(key: APIKey, patch: Partial<APIKey> = {}) {
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

export function getApiKeyBinding(key: APIKey, routes: Route[], providers: Provider[]) {
  const route = routes.find((item) => item.id === key.routeId);
  const binding = apiKeyBindingFromRoute(route);
  const routeProvider = binding.providerId ? providers.find((item) => item.id === binding.providerId) : undefined;
  const bindingAction = route && routeProvider
    ? (route.outputProtocol === routeProvider.protocol ? '透传' : '转换')
    : '-';
  return { route, binding, routeProvider, bindingAction };
}

export function findRouteForBinding(routes: Route[], providerId: string, outputProtocol: Protocol) {
  return routes.find((route) => route.providerId === providerId && route.outputProtocol === outputProtocol);
}

export function apiKeyBindingFromRoute(route: Route | undefined) {
  return {
    providerId: route?.providerId || '',
    outputProtocol: (route?.outputProtocol || 'openai_chat') as Protocol,
  };
}

/** 首选绑定或备选列表中引用了该 Provider 的 API Key 都算引用。 */
export function apiKeyReferencesProvider(key: APIKey, routes: Route[], providerId: string) {
  if (!providerId) return false;
  const route = routes.find((item) => item.id === key.routeId);
  if (route?.providerId === providerId) return true;
  if ((key.fallbackProviderIds || []).includes(providerId)) return true;
  if (key.activeProviderId === providerId) return true;
  return false;
}

export function formatTokenSummary(stats: Pick<APIKeyDayStats, 'inputTokens' | 'outputTokens' | 'cacheTokens'>) {
  const { totalInput, cacheHits } = normalizePromptTokenStats(stats.inputTokens, stats.cacheTokens || 0);
  return `in ${formatTokenCount(totalInput)} · out ${formatTokenCount(stats.outputTokens)} · cache ${formatTokenCount(cacheHits)}`;
}

export function normalizeRequestStats(raw: RequestStatsSnapshot | LegacyRequestStatsSnapshot | null | undefined): RequestStatsSnapshot | null {
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

export function usageStatsForKey(snapshot: RequestStatsSnapshot | null, apiKeyId: string) {
  const today = snapshot?.today?.byApiKey.find((item) => item.apiKeyId === apiKeyId);
  const month = snapshot?.month?.byApiKey.find((item) => item.apiKeyId === apiKeyId);
  return { today, month };
}

export function usageStatsForProvider(snapshot: RequestStatsSnapshot | null, providerId: string) {
  const today = snapshot?.today?.byProvider?.find((item) => item.providerId === providerId);
  const month = snapshot?.month?.byProvider?.find((item) => item.providerId === providerId);
  return { today, month };
}

export function providerUsageLabel(providerId: string, providers: Provider[]) {
  if (providerId === '_unknown') return '未知 Provider';
  const provider = providers.find((item) => item.id === providerId);
  return provider ? provider.name : providerId;
}

export function buildRouteTestPayload(model: string, message: string) {
  const resolvedModel = model.trim() || 'request-model-not-set';
  const resolvedMessage = message.trim() || 'ping from Protocol Gateway route test';
  return {
    model: resolvedModel,
    stream: false,
    messages: [{ role: 'user', content: resolvedMessage }],
  };
}

export function routeGatewayTestURL(route: Route, endpoints: OutputEndpoint[]) {
  const endpoint = endpoints.find((item) => item.protocol === route.outputProtocol) || endpoints.find((item) => item.protocol === 'openai_chat');
  if (!endpoint) return `${API_BASE}/v1/chat/completions`;
  const localRoot = `http://${endpoint.listenHost}:${endpoint.listenPort}`;
  return routeGatewayURL(route, endpoints, localRoot);
}

export function routeGatewayURL(route: Route, endpoints: OutputEndpoint[], base: string) {
  const endpoint = endpoints.find((item) => item.protocol === route.outputProtocol);
  if (!endpoint || !base) return '';
  const root = `${base.replace(/\/$/, '')}${endpoint.basePath}`;
  if (route.outputProtocol === 'openai_chat') return `${root}/chat/completions`;
  if (route.outputProtocol === 'openai_responses') return `${root}/responses`;
  // Claude BasePath is /anthropic; clients append /v1/messages themselves.
  if (route.outputProtocol === 'claude') return `${root}/v1/messages`;
  return root;
}

export function apiKeyClientBaseURL(route: Route, endpoints: OutputEndpoint[], publicBase: string) {
  const base = publicBase || localGatewayRoot(endpoints);
  const endpoint = endpoints.find((item) => item.protocol === route.outputProtocol);
  if (!endpoint) return base.replace(/\/$/, '');
  return `${base.replace(/\/$/, '')}${endpoint.basePath}`;
}

export function apiKeyClientAuthHint(route: Route) {
  if (route.outputProtocol === 'claude') return 'Claude Code：Base URL 填到 /anthropic（不要带 /v1）+ x-api-key';
  if (route.outputProtocol === 'openai_responses') return 'OpenAI Responses 客户端：Base URL + Bearer Key';
  return 'OpenAI 客户端：Base URL（如 /v1）+ Bearer Key';
}

export function sanitizeClientConfigID(name: string) {
  const id = name.trim().toLowerCase().replace(/[^a-z0-9_-]+/g, '-').replace(/^-+|-+$/g, '');
  return id || 'gateway';
}

export function apiKeyGatewayRoot(endpoints: OutputEndpoint[], publicBase: string) {
  return (publicBase || localGatewayRoot(endpoints)).replace(/\/$/, '');
}

export function buildApiKeyOpenCodeConfig(key: APIKey, route: Route | undefined, endpoints: OutputEndpoint[], publicBase: string, provider?: Provider) {
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

export function openCodeModelLimit(modelID: string, provider?: Provider, aliases?: Record<string, string>) {
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

export function resolveCodexReasoningEffort(key: APIKey) {
  const raw = (key.thinkingDepthOverride || '').trim().toLowerCase();
  if (!raw) return 'medium';
  if (raw === 'max') return 'xhigh';
  if ((THINKING_DEPTH_OPTIONS as readonly string[]).includes(raw)) return raw;
  return 'medium';
}

/** Codex 本地 model catalog：消掉 “Model metadata for `xxx` not found” */
export function buildApiKeyCodexModelCatalogJSON(key: APIKey, provider?: Provider) {
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

export function buildApiKeyCodexConfig(key: APIKey, route: Route | undefined, endpoints: OutputEndpoint[], publicBase: string, provider?: Provider, keepOfficialLogin?: boolean) {
  const providerID = sanitizeClientConfigID(key.name);
  const model = resolveApiKeyModel(key, provider);
  const effort = resolveCodexReasoningEffort(key);
  const baseURL = `${apiKeyGatewayRoot(endpoints, publicBase)}/openai/v1`;
  const warning = !route || route.outputProtocol !== 'openai_responses'
    ? '# 注意：当前密钥输出协议不是 OpenAI Responses，请先改为「OpenAI Responses」\n'
    : '';
  // 保持账号登录：让该 provider 表在 Codex 眼里“长得像官方 openai” provider
  // （name 对齐官方形状），使 Codex 官方特性门控（插件市场、移动端远程控制等）
  // 继续命中；不改 base_url / experimental_bearer_token，实际模型流量仍打到
  // 本网关。全程不写 ~/.codex/auth.json（本工具从未写过该文件）。
  const providerName = keepOfficialLogin ? 'OpenAI' : (key.name || providerID);
  const keepOfficialComment = keepOfficialLogin
    ? '# 保持账号登录已开启：name 已对齐官方 provider 形状\n'
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
# 关闭沙箱、跳过审批确认（等价于 codex --dangerously-bypass-approvals-and-sandbox）：
# 每个操作都要手动确认太打断节奏，这里直接给最高权限、不做沙箱隔离。
approval_policy = "never"
sandbox_mode = "danger-full-access"

[model_providers.${providerID}]
name = "${providerName}"
base_url = "${baseURL}"
wire_api = "responses"
requires_openai_auth = true
# Codex 0.118+ 默认会先尝试 WebSocket 连 /v1/responses，这是 OpenAI 内部专用、
# 没有公开文档的传输协议，第三方网关根本接不住，只会 405 之后重试几轮才退回
# HTTPS（社区已知问题，如 github.com/openai/codex/issues/13103、#28503）。
# 显式关闭（即使不是 100% 生效，这是社区公认的缓解办法）。之前"保持账号登录"
# 会把这里设成 true 让 provider 更像官方，但这恰恰会让 Codex 更倾向于真的去
# 试 WebSocket，反而更容易触发这个问题——所以这里不再跟随 keepOfficialLogin。
supports_websockets = false
experimental_bearer_token = "${key.key}"
`;
}

/** 给 Claude 模型名补上 [1m] 后缀，让客户端请求 100 万上下文窗口（避免默认 200k 频繁压缩）。
 *  已带后缀 / 空值不重复加；后缀触发 anthropic-beta: context-1m-2025-08-07 头。 */
export function withClaude1MContext(model: string) {
  const trimmed = model.trim();
  if (!trimmed || trimmed === 'your-model') return trimmed;
  if (/\[[^\]]*\]\s*$/.test(trimmed)) return trimmed;
  return `${trimmed}[1m]`;
}

export function buildApiKeyClaudeConfig(key: APIKey, route: Route | undefined, endpoints: OutputEndpoint[], publicBase: string, provider?: Provider) {
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

export function clientConfigFilePath(client: 'opencode' | 'codex' | 'claude') {
  if (client === 'opencode') return '~/.config/opencode/opencode.json';
  if (client === 'codex') return '~/.codex/config.toml';
  return '~/.claude/settings.json';
}

export function clientConfigHomeRelativePath(client: 'opencode' | 'codex' | 'claude') {
  if (client === 'opencode') return '.config/opencode/opencode.json';
  if (client === 'codex') return '.codex/config.toml';
  return '.claude/settings.json';
}

export function clientConfigTitle(client: 'opencode' | 'codex' | 'claude') {
  if (client === 'opencode') return 'OpenCode 配置';
  if (client === 'codex') return 'Codex 配置';
  return 'Claude Code 配置';
}

// Codex config.toml 里，本工具管理的 provider 段的分界线（一对，多个 # 号）。
// 只有落在这两行之间的内容会被本工具增量替换；文件里其余任何区块
// （[features]、[memories]、personality 等用户自定义配置）永远原样保留、
// 不会被这份脚本触碰或挪动位置。注意 approval_policy / sandbox_mode 这两个
// 顶层键现在也由本工具写入这一段（关闭沙箱、跳过审批确认），不再算“用户自己
// 的配置”——见 buildApiKeyCodexConfig 里的注释。
export const CODEX_PROVIDER_BLOCK_BEGIN = '################ LPG-CODEX-PROVIDER-BEGIN ################';

export const CODEX_PROVIDER_BLOCK_END = '################ LPG-CODEX-PROVIDER-END ################';

// 复制脚本对应的动作名：三种客户端现在都走增量合并（"修改脚本"）——只更新本工具
// 管理的键/段，保留用户在同一配置文件里的其它设置（OpenCode 的其它 provider/主题/
// keybinds/mcp，Claude 的 permissions/hooks/model/statusLine 等）。
export function clientConfigScriptNoun(_client: 'opencode' | 'codex' | 'claude') {
  return '修改脚本';
}

/** UTF-8 → base64，供粘贴型 Python 脚本安全内嵌配置正文（避免引号/heredoc 坑）。 */
export function utf8ToBase64(text: string) {
  const bytes = new TextEncoder().encode(text);
  let binary = '';
  for (let i = 0; i < bytes.length; i += 1) binary += String.fromCharCode(bytes[i]);
  return btoa(binary);
}

/** macOS 终端可直接粘贴执行：外层仅一行 python3 heredoc，逻辑全在 Python。 */
export function wrapMacPythonScript(pyBody: string, headerComment: string) {
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

export const PY_HELPERS = `
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
 *   personality 等用户自己的配置一个字符都不会被改动，也不会被换位置。
 *   approval_policy / sandbox_mode 现在由本工具管理（关闭沙箱、跳过审批），
 *   见 strip_bare_legacy_content 的 managed_keys。
 * model catalog 附加文件仍是本工具独占的一个文件，按原方式整份覆盖，不受影响。
 */
export function buildApiKeyCodexConfigPatchScript(
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
    '# 迁移防呆：这个"标记包裹整段替换"的设计是后加的——早期版本直接裸写',
    '# model_provider/model/... 等顶层键和 [model_providers.<id>] 表，不带',
    '# BEGIN/END。老用户第一次用新版脚本时，strip_provider_block 只认标记，',
    '# 找不到那段裸内容，于是新内容插到顶部，旧的裸内容原样留在文件后面——',
    '# provider id 由 key 名派生、不会变，两边表名/顶层键撞在一起，TOML 解析报',
    '# duplicate key。这里额外扫一遍 rest：摘掉不在任何 [section] 内、且恰好是',
    '# 本工具管理的那些顶层键的裸赋值，以及表名等于本次要写 id 的裸表——只自愈',
    '# 这一种已知冲突，不触碰其余任何用户自定义内容（含同名 [section] 之后的字段）。',
    '# approval_policy/sandbox_mode 很多用户自己也会手动设置成裸顶层键，纳入',
    '# managed_keys 避免和本工具这次写入的新值撞成 duplicate key。',
    'import re',
    'def strip_bare_legacy_content(text: str, provider_id: str) -> str:',
    '    header = "[model_providers." + provider_id + "]"',
    '    managed_keys = {"model_provider", "model", "model_reasoning_effort", "model_catalog_json", "approval_policy", "sandbox_mode"}',
    '    out = []',
    '    seen_table = False',
    '    skip_table = False',
    '    for line in text.splitlines(keepends=True):',
    '        raw = line.rstrip("\\r\\n")',
    '        if skip_table:',
    '            if raw.startswith("["):',
    '                skip_table = False',
    '            else:',
    '                continue',
    '        if raw.startswith("["):',
    '            seen_table = True',
    '            if raw == header:',
    '                skip_table = True',
    '                continue',
    '            out.append(line)',
    '            continue',
    '        if not seen_table and raw.split("=", 1)[0].strip() in managed_keys:',
    '            continue',
    '        out.append(line)',
    '    return "".join(out)',
    '',
    'FILE.parent.mkdir(parents=True, exist_ok=True)',
    'rest = ""',
    'if FILE.exists():',
    '    _backup(FILE)',
    '    rest = strip_provider_block(FILE.read_text(encoding="utf-8"))',
    '    _match = re.search(r"^\\[model_providers\\.(.+)\\]$", NEW_BLOCK, re.MULTILINE)',
    '    if _match:',
    '        rest = strip_bare_legacy_content(rest, _match.group(1))',
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
export function buildApiKeyCodexRestoreOfficialScript() {
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
export function buildApiKeyClientConfigInstallScript(
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

export function buildApiKeyClientConfigExtras(
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

export function buildApiKeyClientConfig(
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

export function clientConfigProtocolHint(client: 'opencode' | 'codex' | 'claude', route?: Route) {
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

export function clientConfigCompatible(client: 'opencode' | 'codex' | 'claude', protocol?: Protocol) {
  if (!protocol) return false;
  if (client === 'opencode') return protocol === 'openai_chat' || protocol === 'openai_responses' || protocol === 'claude';
  if (client === 'codex') return protocol === 'openai_responses';
  return protocol === 'claude';
}

export function clientConfigsForProtocol(protocol?: Protocol): Array<'opencode' | 'codex' | 'claude'> {
  return (['opencode', 'codex', 'claude'] as const).filter((client) => clientConfigCompatible(client, protocol));
}

export function resolveApiKeyModel(key: APIKey, provider?: Provider) {
  return key.modelOverride?.trim() || provider?.defaultModel?.trim() || 'your-model';
}

export function buildApiKeyCallPayload(route: Route, model: string) {
  if (route.outputProtocol === 'claude') {
    return { model, max_tokens: 1024, stream: false, messages: [{ role: 'user', content: '你好' }] };
  }
  return { model, stream: false, messages: [{ role: 'user', content: '你好' }] };
}

export function localGatewayRoot(endpoints: OutputEndpoint[]) {
  const endpoint = endpoints[0];
  return endpoint ? `http://${endpoint.listenHost}:${endpoint.listenPort}` : 'http://127.0.0.1:18093';
}

export function activePublicBaseURL(publicAccess: PublicAccessSettings, tunnelRunning: boolean) {
  if (!tunnelRunning) return '';
  if (publicAccess.mode === 'custom_domain' && publicAccess.exposeApi === false) return '';
  const tunnel = publicAccess.tunnel;
  return tunnel?.publicUrl || publicAccess.publicBaseUrl || '';
}

export function activeUIPublicBaseURL(publicAccess: PublicAccessSettings, tunnelRunning: boolean) {
  if (!tunnelRunning) return '';
  if (publicAccess.mode === 'custom_domain' && publicAccess.exposeUi === false) return '';
  const tunnel = publicAccess.tunnel;
  return tunnel?.uiPublicUrl || publicAccess.uiPublicBaseUrl || (publicAccess.uiDomain ? `https://${publicAccess.uiDomain}` : '');
}

export function deriveUIDomainFromAPI(apiDomain: string) {
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

export function buildApiKeyPublicCurl(key: APIKey, route: Route, endpoints: OutputEndpoint[], publicBase: string, provider?: Provider) {
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

export function resolveProviderTestModel(model: string) {
  return model.trim() || 'request-model-not-set';
}

export function resolveProviderChatURL(provider: Provider, model: string) {
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

export function buildProviderChatMessages(options: ProviderChatTestOptions) {
  const messages: Array<{ role: string; content: string }> = [];
  const systemPrompt = options.systemPrompt.trim();
  const userPrompt = options.userPrompt.trim() || 'x+5等于几';
  if (systemPrompt) messages.push({ role: 'system', content: systemPrompt });
  messages.push({ role: 'user', content: userPrompt });
  return messages;
}

export function buildProviderCacheRound2Messages(options: ProviderChatTestOptions) {
  const messages = buildProviderChatMessages(options);
  messages.push({ role: 'assistant', content: '（第一轮 assistant 回复）' });
  messages.push({ role: 'user', content: PROVIDER_CACHE_ROUND2_USER });
  return messages;
}

export function buildProviderChatPayload(model: string, messages: Array<{ role: string; content: string }>, thinking?: Pick<ProviderChatTestOptions, 'thinkingField' | 'thinkingValue'>) {
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

export function buildChatTestCurl(url: string, payload: Record<string, unknown>, auth?: ProviderAuthPreview | null) {
  const body = JSON.stringify(payload, null, 2);
  const authLine = auth?.value ? `  -H '${auth.header}: ${auth.value}' \\\n` : '';
  return `curl -s '${url}' \\\n  -H 'Content-Type: application/json' \\\n${authLine}  -d '${body.replace(/'/g, `'\\''`)}'`;
}

export function buildRouteTestCurl(gatewayURL: string, model: string, message: string, apiKey?: string) {
  const payload = buildRouteTestPayload(model, message);
  const auth = apiKey ? { header: 'Authorization', value: `Bearer ${apiKey}` } : null;
  return buildChatTestCurl(gatewayURL, payload, auth);
}

export function buildClaudeOAuthChatCurl(provider: Provider, model: string, options: ProviderChatTestOptions) {
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

export function buildProviderChatCurl(provider: Provider, model: string, options: ProviderChatTestOptions, auth?: ProviderAuthPreview | null) {
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

export function protocolTone(protocol: Protocol): BadgeTone {
  switch (protocol) {
    case 'openai_chat': return 'blue';
    case 'openai_responses': return 'amber';
    case 'claude': return 'purple';
  }
}

export function actionTone(action: string): BadgeTone {
  if (action.includes('pass')) return 'green';
  if (action.includes('convert')) return 'amber';
  if (action.includes('error') || action.includes('fail')) return 'red';
  if (action.includes('test')) return 'cyan';
  return 'slate';
}

export function statusTone(status?: number): BadgeTone {
  if (!status) return 'slate';
  if (status >= 200 && status < 300) return 'green';
  if (status === 501) return 'amber';
  if (status >= 400) return 'red';
  return 'slate';
}

export function healthTone(status: string): BadgeTone {
  if (status === 'healthy') return 'green';
  if (status === 'failed') return 'red';
  if (status === 'unavailable') return 'red';
  if (status === 'degraded') return 'amber';
  if (status === 'standby') return 'blue';
  return 'slate';
}

export function normalizePromptTokenStats(input: number, cache: number) {
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

export function thermalPressureLabel(level?: string) {
  switch ((level || '').toLowerCase()) {
    case 'nominal': return '正常';
    case 'fair': return '偏高';
    case 'serious': return '较高';
    case 'critical': return '过高';
    default: return level || '—';
  }
}

export const TEMP_SOURCE_LABELS: Record<string, string> = {
  'iokit/cpu-cluster': 'CPU 核心簇传感器',
  'iokit/cpu': 'CPU 传感器',
  'iokit/soc-die': 'SoC 芯片传感器',
  'iokit/soc': 'SoC 封装传感器',
  'powermetrics/smc': 'powermetrics SMC',
};

export function tempSourceLabel(source?: string) {
  if (!source) return '已采样';
  return `来源 ${TEMP_SOURCE_LABELS[source] || source}`;
}

export function formatBytes(bytes?: number, digits = 1): string {
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

export function formatRate(bytesPerSecond?: number): string {
  if (bytesPerSecond == null || !Number.isFinite(bytesPerSecond) || bytesPerSecond < 0) return '—';
  return `${formatBytes(bytesPerSecond)}/s`;
}

export function formatDuration(seconds?: number): string {
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

export function hostTempMetric(hostMetrics: HostMetrics | null): { value: string; note: string } {
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

export function diskTempLabel(disk: HostDiskTemp, index: number, total: number): string {
  if (disk.internal === true) return '内置硬盘温度';
  if (disk.internal === false) return '外置硬盘温度';
  if (total > 1) return `硬盘温度 ${index + 1}`;
  return '硬盘温度';
}

export function diskTempNote(disk: HostDiskTemp): string {
  const parts: string[] = [];
  if (disk.model) parts.push(disk.model);
  if (disk.device) parts.push(disk.device);
  return parts.join(' · ') || (disk.source ? `来源 ${disk.source}` : 'SMART');
}

export function fanSpeedLabel(fan: HostFanSpeed, index: number, total: number): string {
  if (fan.name) return fan.name;
  if (total > 1) return `风扇 ${index + 1}`;
  return '风扇转速';
}

export function fanSpeedNote(fan: HostFanSpeed): string {
  const parts: string[] = [];
  if (fan.minRpm != null && fan.maxRpm != null && fan.maxRpm > 0) {
    parts.push(`${Math.round(fan.minRpm)}–${Math.round(fan.maxRpm)} RPM`);
  }
  if (Number.isFinite(fan.percent)) {
    parts.push(`${fan.percent.toFixed(0)}%`);
  }
  return parts.join(' · ') || (fan.source ? `来源 ${fan.source}` : 'SMC');
}

export function formatLocalISODate(date: Date) {
  const y = date.getFullYear();
  const m = String(date.getMonth() + 1).padStart(2, '0');
  const d = String(date.getDate()).padStart(2, '0');
  return `${y}-${m}-${d}`;
}

/** 单日且等于本地今天 → 视为「跟随今天」，跨日后侧边栏再进页会自动滚到新的今天。 */
export function isFollowingTodayRange(from: string, to: string, today = formatLocalISODate(new Date())) {
  return Boolean(from) && from === to && from === today;
}

/** 英文紧凑单位：k / M / B，保留一位小数。 */
export function formatEnCompact(value: number) {
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
export function pickUsageXTickIndexes(count: number) {
  if (count <= 0) return [] as number[];
  if (count <= 10) return Array.from({ length: count }, (_, i) => i);
  const step = Math.max(1, Math.ceil(count / 8));
  const indexes = new Set<number>([0, count - 1]);
  for (let i = 0; i < count; i += step) indexes.add(i);
  return [...indexes].sort((a, b) => a - b);
}

export function endpointURL(endpoint?: OutputEndpoint) {
  if (!endpoint) return API_BASE;
  return `http://${endpoint.listenHost}:${endpoint.listenPort}${endpoint.basePath}`;
}

export function normalizeDomainInput(value: string) {
  return value.trim().replace(/^https?:\/\//, '').replace(/\/+$/, '');
}

export function publicStatusTone(status: string): BadgeTone {
  if (status === 'runtime_url_recorded') return 'green';
  if (status === 'configured_pending_tunnel') return 'blue';
  if (status === 'waiting_for_tunnel') return 'amber';
  if (status === 'unsupported' || status === 'error') return 'red';
  return 'slate';
}

export function publicAccessURL(endpoint: OutputEndpoint | undefined, tunnelRunning: boolean) {
  if (!tunnelRunning || !endpoint?.publicUrl) return '隧道未启动，请先在「公网访问」页启动隧道';
  return endpoint.publicUrl;
}

export function tunnelStatusTone(status?: string): BadgeTone {
  if (status === 'running') return 'green';
  if (status === 'starting') return 'amber';
  if (status === 'error') return 'red';
  return 'slate';
}

export function splitCustomDomain(full?: string) {
  const normalized = normalizeDomainInput(full || '');
  if (!normalized) return { prefix: 'gateway', root: '' };
  const parts = normalized.split('.');
  if (parts.length <= 2) return { prefix: '', root: normalized };
  return { prefix: parts[0], root: parts.slice(1).join('.') };
}

export function pickCustomDomainRoot(preferred: string, zones: CloudflareZoneOption[]) {
  const names = zones.map((zone) => zone.name).filter(Boolean);
  if (preferred && names.includes(preferred)) return preferred;
  if (names.length === 1) return names[0];
  if (preferred && names.length === 0) return preferred;
  return names[0] || preferred || '';
}

export function composeCustomDomain(prefix: string, root: string) {
  const cleanPrefix = prefix.trim().replace(/\.$/, '').replace(/\s+/g, '');
  const cleanRoot = normalizeDomainInput(root);
  if (!cleanRoot) return '';
  return cleanPrefix ? `${cleanPrefix}.${cleanRoot}` : cleanRoot;
}

export function formatTokenCount(value: number) {
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`;
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)}K`;
  return String(value);
}

export function formatModelOutputBudget(n: number | undefined) {
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

// Qoder 模型分级选择器（docs.qoder.com/zh/user-guide/chat/model-tier-selector）
// 的 5 个档位及其 credit 消耗倍率，按倍率降序（与后端 defaultQoderModels 的
// 目录顺序一致）。按模型 id 匹配而非按 provider 类型：这 5 个是高度独有的
// 英文单词 id，和其他 provider 真实模型名（vendor-model-version 形式）撞车
// 的概率可忽略，换来的是零改动地在所有下拉框（SearchableModelSelect 及其
// 复用者）里生效，不必额外传 provider 上下文。
export const QODER_TIER_INFO: Record<string, { label: string; multiplier: string }> = {
  ultimate: { label: '极致', multiplier: '1.6x' },
  performance: { label: '性能', multiplier: '1.1x' },
  auto: { label: '智能路由', multiplier: '1.0x' },
  efficient: { label: '经济', multiplier: '0.3x' },
  lite: { label: '轻量', multiplier: '免费' },
};

export function modelSelectOptionLabel(model: Model) {
  const tier = QODER_TIER_INFO[model.id];
  const out = formatModelOutputBudget(model.maxOutputTokens);
  if (tier) {
    const base = `${model.id}（${tier.label} ${tier.multiplier}）`;
    return out ? `${base} · out ${out}` : base;
  }
  return out ? `${model.id} · out ${out}` : model.id;
}

export function formatCompactCount(value: number) {
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

export function formatTokenSummaryCompact(stats: Pick<APIKeyDayStats, 'inputTokens' | 'outputTokens' | 'cacheTokens'>) {
  const { totalInput, cacheHits } = normalizePromptTokenStats(stats.inputTokens, stats.cacheTokens || 0);
  return `in ${formatCompactCount(totalInput)} · out ${formatCompactCount(stats.outputTokens)} · cache ${formatCompactCount(cacheHits)}`;
}

// OAuth usage panels: poll only while the browser tab is visible, and keep the
// interval conservative so backgrounded / multi-provider UIs don't hammer
// Anthropic / Cursor quota APIs.
export const OAUTH_USAGE_POLL_MS = 3 * 60_000;

export const OAUTH_USAGE_STORAGE_PREFIX = 'oauth-usage:';

/**
 * ChatGPT plan_type → 友好名。上游 wham/usage 与 JWT claims 里是机器串
 * （self_serve_business_prolite 等），直接展示既长又看不懂档位。
 * 未知值原样返回，新套餐不会显示成空。
 */
const CHATGPT_PLAN_LABELS: Record<string, string> = {
  free: 'Free',
  plus: 'Plus',
  pro: 'Pro',
  pro_lite: 'Pro Lite',
  team: 'Team',
  business: 'Business',
  enterprise: 'Enterprise',
  edu: 'Edu',
  self_serve_business: 'Business',
  self_serve_business_lite: 'Business Lite',
  self_serve_business_pro: 'Business Pro',
  self_serve_business_prolite: 'Business Pro Lite',
  self_serve_business_pro_lite: 'Business Pro Lite',
  business_prolite: 'Business Pro Lite',
};

export function chatgptPlanLabel(planType?: string): string {
  const value = (planType || '').trim();
  if (!value) return '';
  return CHATGPT_PLAN_LABELS[value.toLowerCase()] ?? value;
}

/** 把 accountLabel（"email · plan_type"）里的 plan 段替换成友好名。 */
export function chatgptAccountLabelFriendly(label?: string): string {
  const value = (label || '').trim();
  if (!value) return '';
  const idx = value.lastIndexOf(' · ');
  if (idx < 0) return value;
  const email = value.slice(0, idx);
  const plan = value.slice(idx + 3);
  return plan ? `${email} · ${chatgptPlanLabel(plan)}` : email;
}
