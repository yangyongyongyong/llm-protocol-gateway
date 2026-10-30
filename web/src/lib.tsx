// @generated-from main.tsx — 由重构脚本拆分生成，请直接维护本文件。
import React from 'react';
import {
  AdminAuthStatus,
  APIKey,
  APIKeyDayStats,
  BadgeTone,
  CloudflareZoneOption,
  GatewayState,
  LegacyRequestStatsSnapshot,
  LogEntry,
  Model,
  NavItemID,
  OutputEndpoint,
  Protocol,
  Provider,
  ProviderAuthPreview,
  ProviderCacheTestResult,
  ProviderChatTestOptions,
  ProviderConnectKind,
  ProviderPreset,
  ProviderThinkingTestResult,
  PublicAccessSettings,
  RequestAdapter,
  RequestStatsSnapshot,
  Route,
  RouteTestResult,
  SubscriptionCheckin,
  SubscriptionConnectKind,
  TimeHint
} from './types';

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

/**
 * errorMessageFromBody 从控制台的错误信封里取出人能读的那句话：
 * {"error":{"message":"invalid username or password","type":"gateway_error"}}、
 * {"error":"..."}、{"message":"..."} 或纯文本。取不到时返回 fallback。
 * 登录页曾把整个 JSON 原样 print 在终端样式的行里。
 */
export function errorMessageFromBody(text: string, fallback = ''): string {
  const trimmed = (text || '').trim();
  if (!trimmed) return fallback;
  try {
    const data = JSON.parse(trimmed) as { error?: { message?: string } | string; message?: string; detail?: string };
    const message = (typeof data?.error === 'object' ? data.error?.message : data?.error) || data?.message || data?.detail;
    if (typeof message === 'string' && message.trim()) return message.trim();
    return fallback || trimmed;
  } catch {
    return trimmed;
  }
}

/** authRejectionMessage 把非 2xx 的登录/改密应答变成一句话（错误信封或纯文本都认）。 */
export async function authRejectionMessage(response: Response, fallback: string): Promise<string> {
  let text = '';
  try { text = await response.text(); } catch { /* 读不到体就走兜底 */ }
  return errorMessageFromBody(text, `${fallback}（HTTP ${response.status}）`);
}

/** 对话测试的结论（RouteTestResult 的最小形状，见 types.ts）。 */
export type ChatTestVerdict = { success: boolean; status?: number; error?: string; httpStatus?: number };

/**
 * chatTestVerdictFromBody 把对话测试接口的应答变成结论。接口成功时给
 * {success,status,...}；失败（权限不足、路由不存在、请求体不合法）给的是
 * {"error":{"message":...}} 错误信封——此时既没有 success 也没有 status，旧代码
 * 于是弹出「对话测试未通过：undefined」。从信封里取那句话，取不到再退回状态码。
 */
export function chatTestVerdictFromBody(bodyText: string, httpStatus: number): ChatTestVerdict {
  let parsed: unknown = null;
  try {
    parsed = JSON.parse(bodyText);
  } catch {
    parsed = null;
  }
  if (!parsed || typeof parsed !== 'object') {
    const text = (bodyText || '').trim().slice(0, 200);
    return { success: false, httpStatus, error: text ? `HTTP ${httpStatus}：${text}` : `HTTP ${httpStatus}` };
  }
  const data = parsed as { success?: unknown; status?: number; error?: unknown };
  // 正常结果里 error 是字符串（或为空），成功与否看 success。
  if (typeof data.success === 'boolean' && typeof data.error !== 'object') {
    return { success: data.success, status: data.status, error: typeof data.error === 'string' ? data.error : undefined, httpStatus };
  }
  // 信封里没有可读的那句话：退回状态码，别把 "{}" 之类交给用户。
  const message = errorMessageFromBody(bodyText, '');
  const readable = message && message !== bodyText.trim() ? message : '';
  return { success: false, httpStatus, error: readable || `HTTP ${httpStatus}` };
}

/** chatTestToastText 给对话测试写一句提示：错误说原因，不再出现 undefined。 */
export function chatTestToastText(verdict: ChatTestVerdict) {
  if (verdict.success) return `对话测试成功：HTTP ${verdict.status ?? verdict.httpStatus ?? 200}`;
  const reason = (verdict.error || '').trim() || (verdict.status ? `HTTP ${verdict.status}` : '') || (verdict.httpStatus ? `HTTP ${verdict.httpStatus}` : '') || '未知错误';
  return `对话测试未通过：${reason}`;
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

// Qoder 的两个站点各自的推理主机。后端 normalizeProvider 只在 baseUrl 留空时
// 兜这个默认值（保留手动覆盖入口），所以切到 Qoder 时前端要主动把占位 URL 换掉，
// 否则会残留创建表单的 example.com 预填值。请求体由网关按 Qoder 客户端的方式
// 签名与编码，这两个地址只是账号所在的主机，不是可以直接 curl 的 OpenAI 端点。
export const QODER_GLOBAL_BASE_URL = 'https://api3.qoder.sh';
export const QODER_CN_BASE_URL = 'https://gateway.qoder.com.cn';

export function qoderDefaultBaseURL(kind: ProviderConnectKind | undefined): string {
  return kind === 'qoder_cn' ? QODER_CN_BASE_URL : QODER_GLOBAL_BASE_URL;
}

export const navItems = [
  { id: 'input-providers', label: '输入 Provider' },
  { id: 'models-menu', label: '模型列表' },
  { id: 'api-keys', label: 'API 密钥' },
  { id: 'output-providers', label: '接入地址' },
  { id: 'usage-stats', label: '用量统计' },
  { id: 'public-access', label: '公网访问' },
  { id: 'traffic-tokens', label: 'API 日志' },
  { id: 'monitoring', label: '监控中心' },
  { id: 'quota-keeper', label: '额度保活' },
  { id: 'users', label: '用户管理' },
  { id: 'roles', label: '角色管理' },
  { id: 'machine', label: '机器状态' },
  { id: 'settings', label: '设置' },
] as const;

export type NavItemIDValue = typeof navItems[number]['id'];

/**
 * 菜单层级：一个分组 = 一个功能板块（与后端角色编辑器的一级分类一致），
 * 分组可折叠，折叠状态按用户记住。顺序即板块优先级。
 */
export type NavSection = {
  id: string;
  label: string;
  /** 二级板块：用于跨板块聚合（如「权限与账户」下同时放用户与角色）。 */
  children: Array<{ id: NavItemIDValue }>;
};

export const navSections: NavSection[] = [
  { id: 'providers', label: '账号与模型', children: [{ id: 'input-providers' }, { id: 'models-menu' }] },
  { id: 'keys', label: '密钥与接入', children: [{ id: 'api-keys' }, { id: 'output-providers' }, { id: 'public-access' }] },
  { id: 'insights', label: '数据洞察', children: [{ id: 'usage-stats' }, { id: 'traffic-tokens' }] },
  { id: 'operations', label: '运维监控', children: [{ id: 'monitoring' }, { id: 'quota-keeper' }, { id: 'machine' }] },
  { id: 'admin', label: '权限与账户', children: [{ id: 'users' }, { id: 'roles' }] },
  { id: 'system', label: '系统', children: [{ id: 'settings' }] },
];

/** 核心菜单：登录后落地页与排序优先项。 */
export const coreNavIDs: NavItemIDValue[] = ['input-providers', 'api-keys'];

// 普通用户（旧版）可访问的页面；新版按权限点决定，仅用于没有权限列表的旧会话兜底。
export const userAllowedNavIDs: NavItemIDValue[] = ['input-providers', 'models-menu', 'api-keys', 'traffic-tokens', 'usage-stats', 'quota-keeper'];

/**
 * 每个菜单项需要的最小权限：有其一即可显示。'*' 代表管理员。
 * 与后端 internal/gateway/rbac.go 的 routePermission 表一一对应。
 */
export const navPermissions: Record<NavItemIDValue, string[]> = {
  'input-providers': ['providers.view'],
  'models-menu': ['models.view', 'providers.view'],
  'api-keys': ['keys.view'],
  'output-providers': ['endpoints.view', 'endpoints.edit'],
  'public-access': ['public.access'],
  'usage-stats': ['usage.view'],
  'traffic-tokens': ['logs.view'],
  'monitoring': ['monitor.view', 'monitor.manage'],
  'quota-keeper': ['quota.view', 'quota.manage'],
  'users': ['users.view', 'users.manage'],
  'roles': ['roles.manage'],
  'machine': ['system.machine'],
  'settings': ['system.settings', 'system.settings.write'],
};

/** 权限判定：管理员（'*'）通过一切；未登录/无列表时按旧的 user 规则兜底。 */
export function can(permissions: string[] | undefined, key: string): boolean {
  if (!permissions) return false;
  return permissions.includes('*') || permissions.includes(key);
}

/** 任一满足即可。 */
export function canAny(permissions: string[] | undefined, keys: string[]): boolean {
  if (!permissions) return false;
  if (permissions.includes('*')) return true;
  return keys.some((key) => permissions.includes(key));
}

/**
 * 按权限过滤菜单：返回 null 表示该项隐藏。没有权限列表（旧会话/本地直连）时
 * 回退到 userAllowedNavIDs 的旧行为，保证升级瞬间不会白屏。
 */
export function navVisibleFor(permissions: string[] | undefined, id: NavItemIDValue, legacyUser: boolean): boolean {
  if (permissions && permissions.length > 0) {
    return canAny(permissions, navPermissions[id]);
  }
  return legacyUser ? userAllowedNavIDs.includes(id) : true;
}

/** 侧栏折叠分组持久化（按用户 id 分开，换账号不串）。 */
export const NAV_COLLAPSE_STORAGE_KEY = 'llm-gateway-nav-collapsed';

export function readStoredNavCollapsed(userKey: string): string[] {
  try {
    const raw = localStorage.getItem(`${NAV_COLLAPSE_STORAGE_KEY}:${userKey}`);
    const parsed = raw ? JSON.parse(raw) : [];
    return Array.isArray(parsed) ? parsed.filter((item) => typeof item === 'string') : [];
  } catch {
    return [];
  }
}

export function writeStoredNavCollapsed(userKey: string, collapsed: string[]) {
  try {
    localStorage.setItem(`${NAV_COLLAPSE_STORAGE_KEY}:${userKey}`, JSON.stringify(collapsed));
  } catch {
    /* 隐私模式写不进去也无所谓 */
  }
}

// 侧边栏收起状态持久化 key（独立于主题，避免互相覆盖）
export const SIDEBAR_STORAGE_KEY = 'llm-gateway-sidebar';

// 日志页「调度决策」面板展开状态：默认收起，点开过就记住。
export const DECISION_PANEL_STORAGE_KEY = 'llm-gateway-logs-decision-open';

export function readStoredDecisionPanelOpen(): boolean {
  try {
    return localStorage.getItem(DECISION_PANEL_STORAGE_KEY) === '1';
  } catch {
    return false;
  }
}

export function writeStoredDecisionPanelOpen(open: boolean) {
  try {
    localStorage.setItem(DECISION_PANEL_STORAGE_KEY, open ? '1' : '0');
  } catch {
    /* 隐私模式写不进去也无所谓 */
  }
}

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

// Provider 卡片密度：纯本机视觉偏好，换账号不重置，和主题一样按浏览器记忆。
export type ProviderCardDensity = 'standard' | 'compact';

const PROVIDER_DENSITY_STORAGE_KEY = 'llm-gateway-provider-density';

export function readStoredProviderDensity(): ProviderCardDensity {
  try {
    return localStorage.getItem(PROVIDER_DENSITY_STORAGE_KEY) === 'compact' ? 'compact' : 'standard';
  } catch {
    return 'standard';
  }
}

export function writeStoredProviderDensity(density: ProviderCardDensity) {
  try {
    localStorage.setItem(PROVIDER_DENSITY_STORAGE_KEY, density);
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

// 密钥页视图模式（双栏聚焦 / 全量数据表）记在本地，刷新后保持。
export const API_KEY_VIEW_STORAGE_KEY = 'llm-gateway-api-key-view';

// 用户管理表格列宽（表头与数据行共用，保证对齐）。
export const USERS_TABLE_GRID = 'minmax(0,0.7fr) 64px 48px minmax(0,1fr) minmax(0,0.8fr) minmax(0,0.9fr) minmax(0,0.9fr) 236px';

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
  return providerDisplayLabel(
    providers.find((item) => item.id === log.providerId),
    providerUsageLabel(log.providerId || '_unknown', providers),
  );
}

export function isTrafficLogError(log: LogEntry) {
  return log.status >= 400 || Boolean(log.errorDescription?.trim());
}

export function parseSSEStreamContent(raw: string): string {
  if (!raw || typeof raw !== 'string') return '';
  const lines = raw.split(/\r?\n/);
  const chunks: string[] = [];
  for (const line of lines) {
    const trimmed = line.trim();
    if (!trimmed.startsWith('data:')) continue;
    const dataStr = trimmed.slice(5).trim();
    if (dataStr === '[DONE]') continue;
    try {
      const parsed = JSON.parse(dataStr);
      // OpenAI / standard chat format
      if (Array.isArray(parsed.choices)) {
        for (const choice of parsed.choices) {
          if (choice?.delta?.content) chunks.push(choice.delta.content);
          else if (choice?.delta?.text) chunks.push(choice.delta.text);
          else if (choice?.text) chunks.push(choice.text);
        }
      }
      // Anthropic Claude format
      else if (parsed.type === 'content_block_delta' && parsed.delta?.text) {
        chunks.push(parsed.delta.text);
      } else if (parsed.type === 'content_block_start' && parsed.content_block?.text) {
        chunks.push(parsed.content_block.text);
      }
      // Responses format
      else if (typeof parsed.delta === 'string') {
        chunks.push(parsed.delta);
      } else if (typeof parsed.output_text === 'string') {
        chunks.push(parsed.output_text);
      }
    } catch {
      // Skip non-JSON chunks
    }
  }
  return chunks.join('');
}

export function explainHttpStatus(status: number): { title: string; category: string; description: string; hint: string; tone: BadgeTone } {
  if (status >= 200 && status < 300) {
    return {
      title: '成功受理 (OK)',
      category: '正常',
      description: '上游模型已成功处理并正常生成回复。',
      hint: '链路健康，响应流完整返回。',
      tone: 'green',
    };
  }
  if (status === 400) {
    return {
      title: '请求格式或参数不合法 (Bad Request)',
      category: '客户端协议错误',
      description: '客户端传递的请求正文、参数、模型名称或上下文格式不符合上游规范。',
      hint: '请检查请求报文中的 messages、tools 结构以及 model 字段是否存在拼写错误。',
      tone: 'red',
    };
  }
  if (status === 401) {
    return {
      title: '鉴权未通过 (Unauthorized)',
      category: '身份验证失败',
      description: 'API 密钥缺失、已失效或上游 Provider 密钥凭据无效。',
      hint: '请核对调用方携带的 Authorization 密钥或在后台检查 Provider 的 API Key / OAuth 状态。',
      tone: 'red',
    };
  }
  if (status === 403) {
    return {
      title: '访问被拒绝 (Forbidden)',
      category: '权限不足',
      description: '当前 API 密钥未获得当前路由或 Provider 的使用授权，或已被禁用。',
      hint: '请在后台「API 密钥」中检查该密钥的路由绑定或在「用户」页面核对 Provider 授权分配。',
      tone: 'red',
    };
  }
  if (status === 404) {
    return {
      title: '资源或路由不存在 (Not Found)',
      category: '端点不存在',
      description: '访问的 URL 路径未在网关注册，或指定的模型不存在。',
      hint: '请核对请求路径（如 /v1/chat/completions 或 /v1/messages）。',
      tone: 'red',
    };
  }
  if (status === 429) {
    return {
      title: '速率或配额超限 (Rate Limited / Quota Exceeded)',
      category: '配额受限',
      description: '上游 Provider 账号额度耗尽、欠费，或当前并发触发了 RPM / TPM 速率上限。',
      hint: '请检查上游账号余额或在密钥中配置 Fallback 兜底链实现自动容灾降级。',
      tone: 'red',
    };
  }
  if (status === 499) {
    return {
      title: '客户端主动取消 (Client Closed Request)',
      category: '连接主动中断',
      description: '调用端提前切断了 HTTP/SSE 连接（如用户在界面点击了 Stop 停止生成，或客户端超时）。',
      hint: '请求在传输中被主动中断 (Context Canceled)，非网关或上游崩溃；若频繁出现请调大客户端 Client Timeout。',
      tone: 'slate',
    };
  }
  if (status === 502) {
    return {
      title: '上游网关错误 (Bad Gateway)',
      category: '上游连接异常',
      description: '网关无法连接至上游 Provider、DNS 解析异常或上游返回非标准 HTTP 报文。',
      hint: '请检查 Provider 的 Base URL 配置、网络连通性或当前网络是否需要代理。',
      tone: 'red',
    };
  }
  if (status === 503) {
    return {
      title: '服务暂时不可用 (Service Unavailable)',
      category: '服务不可用',
      description: '上游模型服务当前处于过载维护状态，无法承接新的流量。',
      hint: '建议开启多 Provider 轮询或配置兜底 Provider 自动重试。',
      tone: 'red',
    };
  }
  if (status === 504) {
    return {
      title: '上游响应超时 (Gateway Timeout)',
      category: '超时未响应',
      description: '等待上游模型生成首字或整体响应耗时超过了网络允许的时限。',
      hint: '思考模型 (Thinking) 可能耗时极长，可尝试配置 Stream 保持长连接心跳，或缩短历史上下文。',
      tone: 'red',
    };
  }
  return {
    title: `HTTP ${status} 异常`,
    category: '其他异常',
    description: '请求返回了非预期的 HTTP 状态码。',
    hint: '请结合请求报文与错误摘要深入排错。',
    tone: 'red',
  };
}

export function buildCurlCommand(log: LogEntry, host = window.location.origin): string {
  const url = `${host}${log.path}`;
  const headers: string[] = [
    `-H "Content-Type: application/json"`,
    `-H "Authorization: Bearer YOUR_API_KEY"`,
  ];
  if (log.path.includes('/messages')) {
    headers.push(`-H "anthropic-version: 2023-06-01"`);
  }
  if (log.requestBody) {
    const escaped = log.requestBody.replace(/'/g, `'\\''`);
    return `curl -X POST '${url}' \\\n  ${headers.join(' \\\n  ')} \\\n  --data '${escaped}'`;
  }
  return `curl -X POST '${url}' \\\n  ${headers.join(' \\\n  ')}`;
}

function protocolForPath(path: string): ReplayPlan['protocol'] {
  if (path.includes('/messages')) return 'Claude';
  if (path.includes('/responses')) return 'OpenAI Responses';
  if (path.includes('/chat/completions') || path.includes('/completions')) return 'OpenAI Chat';
  return '';
}

/** 重放计划：把一条历史日志还原成「客户端当时会发的那个请求」。 */
export type ReplayPlan = {
  /** 请求路径（取自日志的 path，去掉查询串）。 */
  path: string;
  /** 客户端协议，决定鉴权头与版本头怎么写。 */
  protocol: 'Claude' | 'OpenAI Chat' | 'OpenAI Responses' | '';
  /** 需要告知用户的额外说明（例如 Responses 重放会新存一条），没有则为空。 */
  note: string;
};

/**
 * 一条日志能不能重放、往哪儿重放。
 *
 * 日志的 `path` 就是网关当时收到的客户端路径，直接回放它保证走同一条协议入口
 * 与路由；只有旧日志缺 path 时才按协议流向推一个默认入口。流向记的是
 * 「上游 Provider 协议 -> 路由输出协议」，客户端说的是输出那一侧。
 */
export function replayPlan(log: LogEntry): ReplayPlan | null {
  if (!log.requestBody?.trim()) return null;
  const flow = log.protocolFlow || '';
  const index = flow.lastIndexOf('->');
  const output = (index >= 0 ? flow.slice(index + 2) : flow).trim();
  // action 为 compact 的日志是客户端调 /responses/compact 触发的，回放要回同一个入口。
  if (log.action === 'compact') return { path: '/v1/responses/compact', protocol: 'OpenAI Responses', note: '' };
  const path = (log.path || '').split('?')[0].trim();
  if (path) return { path, protocol: protocolForPath(path) || (output as ReplayPlan['protocol']) || '', note: '' };
  if (output === 'Claude') return { path: '/v1/messages', protocol: 'Claude', note: '' };
  if (output === 'OpenAI Responses') return { path: '/v1/responses', protocol: 'OpenAI Responses', note: '' };
  if (output === 'OpenAI Chat') return { path: '/v1/chat/completions', protocol: 'OpenAI Chat', note: '' };
  return null;
}

/** 重放请求头。密钥只进 Authorization，调用方负责不把它写进任何展示文本。 */
export function buildReplayHeaders(plan: ReplayPlan, key: string): Record<string, string> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json', Authorization: `Bearer ${key}` };
  if (plan.protocol === 'Claude') headers['anthropic-version'] = '2023-06-01';
  return headers;
}

/**
 * 把重放收到的 SSE / 普通响应收成一段可读文本：优先取回复正文，
 * 解析不出正文时退回原始报文，避免面板空白。
 */
export function summarizeReplayBody(raw: string, streamed: boolean): string {
  if (!raw) return '';
  if (!streamed) {
    try {
      const parsed = JSON.parse(raw) as { choices?: Array<{ message?: { content?: string } }>; output_text?: string; content?: Array<{ text?: string }>; error?: { message?: string } };
      const text = parsed.output_text
        || parsed.choices?.[0]?.message?.content
        || parsed.content?.map(part => part.text || '').join('')
        || parsed.error?.message;
      if (text) return text;
    } catch {
      // 非 JSON（例如纯文本错误）就原样返回。
    }
    return raw;
  }
  return parseSSEStreamContent(raw) || raw;
}

export function formatTrafficLogJSON(log: LogEntry, providers: Provider[] = []): string {
  const provider = providers.find((p) => p.id === log.providerId);
  const data = {
    id: log.id,
    time: log.time,
    status: log.status,
    latencyMs: log.latencyMs,
    ttftMs: log.ttftMs,
    endpoint: log.path,
    protocolFlow: log.protocolFlow,
    routeId: log.routeId,
    provider: {
      id: log.providerId,
      name: provider?.name || log.providerId,
      protocol: provider?.protocol || '-',
      baseUrl: provider?.baseUrl || '-',
    },
    model: {
      requested: log.model,
    },
    auth: {
      apiKeyId: log.apiKeyId,
      apiKeyName: log.apiKeyName,
      userName: log.userName,
    },
    client: {
      ip: log.clientIp,
      host: log.clientHost,
      source: log.accessSource,
    },
    timing: {
      prepMs: log.prepMs,
      preUpstreamMs: log.preUpstreamMs,
      upstreamTtfbMs: log.upstreamTtfbMs,
      gatewayOverheadMs: log.gatewayOverheadMs,
      convertOutMs: log.convertOutMs,
      postMs: log.postMs,
      flags: log.timingFlags,
    },
    tokens: {
      input: log.inputTokens,
      output: log.outputTokens,
      cache: log.cacheTokens,
    },
    error: log.errorDescription || undefined,
  };
  return JSON.stringify(data, null, 2);
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

/**
 * chatTestAnswer 把一次对话测试的响应读成「人先看的东西」：模型说了什么、想了什么、
 * 花了多少 token。三种上游协议（OpenAI Chat / Responses / Claude）与流式回放的
 * 形状都在这里收敛，弹窗只负责排版。
 */
export type ChatTestAnswer = {
  text: string;
  reasoning: string;
  usage: string;
  model: string;
  error: string;
  /** 响应体不是任何已知形状时为空：弹窗退回原始 JSON 视图。 */
  parsed: boolean;
};

function chatTestUsage(usage: Record<string, unknown> | undefined | null): string {
  if (!usage || typeof usage !== 'object') return '';
  const num = (...keys: string[]) => {
    for (const key of keys) {
      const value = (usage as Record<string, unknown>)[key];
      if (typeof value === 'number' && value > 0) return value;
    }
    return 0;
  };
  const parts: string[] = [];
  const input = num('prompt_tokens', 'input_tokens');
  const output = num('completion_tokens', 'output_tokens');
  const total = num('total_tokens');
  if (input) parts.push(`输入 ${input}`);
  if (output) parts.push(`输出 ${output}`);
  if (!input && !output && total) parts.push(`共 ${total}`);
  const cacheRead = num('cache_read_input_tokens', 'prompt_cache_hit_tokens', 'cached_tokens');
  const cacheWrite = num('cache_creation_input_tokens');
  if (cacheRead) parts.push(`缓存读 ${cacheRead}`);
  if (cacheWrite) parts.push(`缓存写 ${cacheWrite}`);
  const reasoningTokens = num('reasoning_tokens');
  if (reasoningTokens) parts.push(`思考 ${reasoningTokens}`);
  return parts.join(' · ');
}

// chatTestFromSSE 读流式回放：把每个 chunk 的正文与思考拼回来。
function chatTestFromSSE(raw: string): ChatTestAnswer {
  const text: string[] = [];
  const reasoning: string[] = [];
  let model = '';
  let usage = '';
  let sawEvent = false;
  for (const line of raw.split('\n')) {
    const trimmed = line.trim();
    if (!trimmed.startsWith('data:')) continue;
    const payload = trimmed.slice(5).trim();
    if (!payload || payload === '[DONE]') continue;
    let chunk: Record<string, unknown>;
    try { chunk = JSON.parse(payload) as Record<string, unknown>; } catch { continue; }
    sawEvent = true;
    if (typeof chunk.model === 'string') model = chunk.model;
    const choices = Array.isArray(chunk.choices) ? chunk.choices : [];
    const delta = (choices[0] as Record<string, unknown> | undefined)?.delta as Record<string, unknown> | undefined;
    if (delta) {
      if (typeof delta.content === 'string') text.push(delta.content);
      const think = typeof delta.reasoning_content === 'string' ? delta.reasoning_content
        : typeof delta.reasoning === 'string' ? delta.reasoning : '';
      if (think) reasoning.push(think);
    }
    if (chunk.type === 'content_block_delta') {
      const part = chunk.delta as Record<string, unknown> | undefined;
      if (part?.type === 'text_delta' && typeof part.text === 'string') text.push(part.text);
      if (part?.type === 'thinking_delta' && typeof part.thinking === 'string') reasoning.push(part.thinking);
    }
    const built = chatTestUsage(chunk.usage as Record<string, unknown>);
    if (built) usage = built;
  }
  if (!sawEvent) return { text: '', reasoning: '', usage: '', model: '', error: '', parsed: false };
  return { text: text.join(''), reasoning: reasoning.join(''), usage, model, error: '', parsed: true };
}

export function chatTestAnswer(result: RouteTestResult | null): ChatTestAnswer | null {
  if (!result) return null;
  const raw = (result.responseBody || result.preview || '').trim();
  if (!raw) {
    return { text: '', reasoning: '', usage: '', model: result.model || '', error: result.error || '', parsed: false };
  }
  if (raw.includes('data:') && !raw.startsWith('{')) {
    const streamed = chatTestFromSSE(raw);
    if (streamed.parsed) return streamed;
  }
  let body: Record<string, unknown>;
  try { body = JSON.parse(raw) as Record<string, unknown>; } catch { return null; }
  const errorNode = body.error as Record<string, unknown> | undefined;
  const error = typeof errorNode === 'string' ? errorNode
    : typeof errorNode?.message === 'string' ? errorNode.message
    : typeof body.message === 'string' && !result.success ? body.message : '';
  const answer: ChatTestAnswer = {
    text: '', reasoning: '', usage: chatTestUsage(body.usage as Record<string, unknown>),
    model: typeof body.model === 'string' ? body.model : (result.model || ''),
    error: result.success ? '' : (error || result.error || ''),
    parsed: false,
  };
  const choices = Array.isArray(body.choices) ? (body.choices as Record<string, unknown>[]) : [];
  const message = choices[0]?.message as Record<string, unknown> | undefined;
  if (message) {
    answer.parsed = true;
    if (typeof message.content === 'string') answer.text = message.content;
    if (typeof message.reasoning_content === 'string') answer.reasoning = message.reasoning_content;
  }
  const output = (message?.output || body.output) as Record<string, unknown>[] | undefined;
  if (Array.isArray(output)) {
    // Responses 协议：正文在 output[] 的 message 项里。
    for (const item of output) {
      answer.parsed = true;
      const parts = Array.isArray(item.content) ? (item.content as Record<string, unknown>[]) : [];
      for (const part of parts) {
        if (typeof part.text === 'string') answer.text += part.text;
      }
    }
  }
  const blocks = Array.isArray(body.content) ? (body.content as Record<string, unknown>[]) : [];
  if (blocks.length) {
    // Claude：content 是块数组，正文与思考分开。
    answer.parsed = true;
    for (const block of blocks) {
      if (block.type === 'text' && typeof block.text === 'string') answer.text += block.text;
      if (block.type === 'thinking' && typeof block.thinking === 'string') answer.reasoning += block.thinking;
    }
  }
  if (typeof body.output_text === 'string') { answer.text = body.output_text; answer.parsed = true; }
  return answer;
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
    case 'gemini': return 'Gemini';
  }
}

export function protocolFromLabel(label: string): Protocol {
  const normalized = label.trim().toLowerCase();
  if (normalized === 'openai responses' || label === 'OpenAI 响应') return 'openai_responses';
  if (normalized === 'claude' || label === 'Claude 消息') return 'claude';
  if (normalized === 'gemini' || label === 'Gemini 原生') return 'gemini';
  if (normalized === 'openai chat' || label === 'OpenAI 对话') return 'openai_chat';
  return 'openai_chat';
}

// 协议下拉的品牌图标：三种上游各自的厂商标，和连接方式下拉同一套资产。
export function protocolIcon(label: string): string {
  const protocol = protocolFromLabel(label);
  if (protocol === 'claude') return 'claude-color';
  if (protocol === 'openai_responses') return 'codex-color';
  if (protocol === 'gemini') return 'google-color';
  return 'openai';
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
    case 'gemini':
      return {
        path: '/v1beta/models/{model}:generateContent',
        label: 'Gemini generateContent',
        hint: '模型名在路径里，请求体含 contents（role user/model + parts）与 generationConfig；streamGenerateContent 以 `data: {...}` 分块返回 candidates。',
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
  const protocolMenu = (['openai_chat', 'openai_responses', 'claude', 'gemini'] as Protocol[])
    .map((protocol) => {
      const spec = selfRegistrationEndpointSpec(protocol);
      const header = protocol === 'claude' ? 'x-api-key: <SHARED_SECRET>' : protocol === 'gemini' ? 'x-goog-api-key: <SHARED_SECRET>' : 'Authorization: Bearer <SHARED_SECRET>';
      return `  \u00b7 protocol="${protocol}"：POST ${spec.path}（${spec.label} 协议格式），鉴权头默认 ${header}\n    ${spec.hint}`;
    })
    .join('\n');
  return `帮我实现一个本地服务 + 内网穿透 + 自动注册的完整方案，要求如下：

【1. 本地服务 —— 四选一，选你已有代码 / 更顺手的那种，不强制】
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
  四选一；网关这边不会预先假定协议，完全以这次调用声明的为准）：
  curl -X PATCH "${registerUrl}" \\
    -H "Authorization: Bearer ${tokenLine}" \\
    -H "Content-Type: application/json" \\
    -d '{"baseUrl": "https://xxxx.trycloudflare.com/<你第1步实现的路径，比如 /v1/chat/completions>", "apiKeySource": "literal:<第1步里设定的 SHARED_SECRET>", "protocol": "<openai_chat|openai_responses|claude|gemini 四选一>"}'
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

/**
 * 额度窗口的重置时刻，与积分的到期时刻，写成一行。
 *
 * 说法照 magpie：今天只给钟点，明天给「明天」，本周内（周一起算）给「周四」，
 * 再远才给「11月6日」；后面缀一句还有多久。时刻已经过去就不再给相对时间——
 * 一张写着「3 小时后」的卡，如果那个时间其实早过了，比不写更误导人。
 */
export function clockLabel(at: Date, now: Date = new Date()): string {
  const time = `${String(at.getHours()).padStart(2, '0')}:${String(at.getMinutes()).padStart(2, '0')}`;
  const dayOnly = (value: Date) => new Date(value.getFullYear(), value.getMonth(), value.getDate()).getTime();
  const days = Math.round((dayOnly(at) - dayOnly(now)) / 86_400_000);
  if (days <= 0) return time;
  if (days === 1) return `明天 ${time}`;
  if (days <= 6 - ((now.getDay() + 6) % 7)) return `${at.toLocaleDateString('zh-CN', { weekday: 'short' })} ${time}`;
  return `${at.getMonth() + 1}月${at.getDate()}日 ${time}`;
}

// untilLabel 是「3 小时后」这种粗略的剩余时间。这里一律向下取整：说「2 小时后」时
// 至少还有 2 小时，而不是 1 小时 31 分被四舍五入成 2 小时；不足 1 分钟就明说，不编成
// 「1 分钟后」。
export function untilLabel(at: Date, now: Date = new Date()): string {
  const minutes = (at.getTime() - now.getTime()) / 60_000;
  if (minutes < 1) return '不到 1 分钟';
  if (minutes < 60) return `${Math.floor(minutes)} 分钟`;
  const hours = Math.floor(minutes / 60);
  if (hours < 48) {
    const rest = Math.floor(minutes - hours * 60);
    return rest > 0 ? `${hours} 小时 ${rest} 分钟` : `${hours} 小时`;
  }
  return `${Math.floor(hours / 24)} 天`;
}

function parseTimeHint(value: string): Date | null {
  if (!value) return null;
  const at = new Date(value);
  return Number.isNaN(at.getTime()) ? null : at;
}

// resetHint 是窗口那一行「重置 明天 15:39 · 20 小时后」。厂商没给重置时间时返回
// null，卡片就不写这一行，而不是编一个。
export function resetHint(resetsAt: string | undefined, now: Date = new Date(), label = '重置'): TimeHint | null {
  const at = parseTimeHint(resetsAt || '');
  if (!at) return null;
  const full = at.toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' });
  if (at.getTime() <= now.getTime()) {
    return { text: `${label}时间已过`, title: `${label}时间已过（${full}）`, passed: true };
  }
  const when = clockLabel(at, now);
  const relative = `${untilLabel(at, now)}后`;
  return { text: `${label} ${when} · ${relative}`, title: `${label}：${full}`, passed: false, when, relative, label };
}

// creditsExpiryHint 从一组窗口里挑出最早作废的那批，写成一行。
export function creditsExpiryHint(windows: Array<{ runsOutAt?: string; runsOut?: number; unit?: string }> | undefined,
  now: Date = new Date()): TimeHint | null {
  const first = [...(windows || [])]
    .filter((window) => window.runsOutAt && (window.runsOut ?? 0) > 0)
    .sort((left, right) => Date.parse(left.runsOutAt!) - Date.parse(right.runsOutAt!))[0];
  if (!first) return null;
  return runsOutHint(first.runsOutAt, first.runsOut ?? 0, first.unit || '', now);
}

/**
 * runsOutHint 是「18.04 积分 将于 11月6日 15:39 过期 · 17 天后」：窗口自己的重置
 * 之外，还剩的这些会在更早的时候作废（WorkBuddy 的积分是按批发的，每批各有一个
 * 到期日）。厂商没说就不写，说了但已经过了也不写——那批已经不在余额里了。
 */
export function runsOutHint(runsOutAt: string | undefined, amount: number, unit: string, now: Date = new Date()): TimeHint | null {
  const at = parseTimeHint(runsOutAt || '');
  if (!at || at.getTime() <= now.getTime() || !(amount > 0)) return null;
  const count = Number.isInteger(amount) ? String(amount) : amount.toFixed(2).replace(/0+$/, '').replace(/\.$/, '');
  const full = at.toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' });
  const when = `${count}${unit ? ` ${unit}` : ''} 将于 ${clockLabel(at, now)} 过期`;
  const relative = `${untilLabel(at, now)}后`;
  return {
    text: `${when} · ${relative}`,
    title: `${count}${unit ? ` ${unit}` : ''} 在 ${full} 作废`,
    passed: false,
    when,
    relative,
  };
}

// useNowTick re-renders every second so retry countdowns stay live between// the 5s /__state polls, without needing per-provider timers.
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
  if (provider.authType === 'qoder_cn') return 'qoder_cn';
  if (provider.authType === 'workbuddy') return 'workbuddy';
  if (provider.authType === 'workbuddy_ai') return 'workbuddy_ai';
  if (provider.authType === 'zcode') return 'zcode';
  if (provider.authType === 'zcode_bigmodel') return 'zcode_bigmodel';
  if (provider.authType === 'trae_cn') return 'trae_cn';
  if (provider.authType === 'trae_global') return 'trae_global';
  if (provider.authType === 'opencode_zen_free') return 'opencode_zen_free';
  if (provider.selfRegistration) return 'self_register';
  return 'api_key';
}

// 上游凭证里有些 accountLabel 恒等于品牌名（实测 Cursor 就是 "Cursor"），
// 它对区分账号毫无价值，当成"没有标识"处理，免得占位还误导。
const GENERIC_ACCOUNT_LABELS = new Set(['cursor', 'qoder', 'workbuddy', 'trae', 'zcode', 'anthropic', 'openai']);

/**
 * 挑一个真正能区分「哪个账号」的标识出来。
 *
 * Provider 的名字来自接入目录，同一个服务商多登一个账号就多一条同名记录，
 * 备选链和 API 日志把它们并排摆出来时，只读名字分不出谁是谁。
 *
 * 昵称不一定可信：WorkBuddy 的 accountLabel 取自上游昵称，实测就有账号的昵称
 * 是 "1"，所以昵称没有区分度时退回账号 id 尾段——九个字符，但一定唯一。
 */
/**
 * 下拉分组：同一「服务商」的 Provider 归一组。订阅登录按连接方式归
 * （Claude / Cursor / WorkBuddy 各算一家，国际版和国内版也是一家）；API Key 类
 * 按端点主机名归——同一个反代上起的多个 Provider 是一条链路，域名不同才是别家。
 */
export function providerGroupLabel(provider: Provider): string {
  if (providerConnectKind(provider) !== 'api_key') return providerConnectKind(provider);
  try {
    return `api_key:${new URL(provider.baseUrl).host.toLowerCase() || provider.id}`;
  } catch {
    return `api_key:${provider.id}`;
  }
}

export function providerAccountLabel(provider: Provider): string {
  const pick = (label?: string, id?: string) => {
    const text = (label || '').trim();
    const uid = (id || '').trim();
    const tail = uid ? `#${uid.replace(/-/g, '').slice(0, 8)}` : '';
    if (text && !GENERIC_ACCOUNT_LABELS.has(text.toLowerCase())) {
      // 短到这种地步的昵称没有区分度（实测 WorkBuddy 的昵称就是 "1"），有账号 id 就让位给它。
      return tail && /^\d{0,3}$/.test(text) ? tail : text;
    }
    return tail;
  };
  switch (providerConnectKind(provider)) {
    case 'claude_oauth': return pick(provider.claudeOAuth?.accountLabel);
    case 'cursor_oauth': return pick(provider.cursorOAuth?.accountLabel);
    case 'chatgpt_oauth': return pick(chatgptAccountLabelFriendly(provider.chatgptOAuth?.accountLabel));
    case 'qoder_pat':
    case 'qoder_cn': return pick(provider.qoderPat?.email || provider.qoderPat?.accountLabel);
    case 'workbuddy':
    case 'workbuddy_ai': return pick(provider.workbuddyOAuth?.accountLabel, provider.workbuddyOAuth?.userId);
    case 'zcode':
    case 'zcode_bigmodel': return pick(provider.zcodeOAuth?.accountLabel);
    case 'trae_cn':
    case 'trae_global': return pick(provider.traeOAuth?.accountLabel, provider.traeOAuth?.userId);
    default: return '';
  }
}

/**
 * 行内展示用的短账号：邮箱只留 @ 之前的一段。
 * 「someone@example.com · Free」要 216px，日志那一列给不满；账号之间的差别
 * 本来就在本地部分，域名砍掉不影响认人，完整值仍留在悬浮提示和详情弹窗里。
 */
export function shortenAccountLabel(label: string): string {
  const at = label.indexOf('@');
  if (at <= 0) return label;
  const plan = label.indexOf(' · ', at);
  return plan < 0 ? label.slice(0, at) : label.slice(0, at) + label.slice(plan);
}

/** Provider 的完整署名：名字 + 账号标识，用来在名字撞车时区分是哪一条。 */
export function providerDisplayLabel(provider: Provider | undefined, missing = '未知 Provider') {
  if (!provider) return missing;
  const account = providerAccountLabel(provider);
  return account && account !== provider.name ? `${provider.name} · ${account}` : provider.name;
}

/**
 * API 日志的「这条请求实际由谁承接」。
 *
 * 后端把故障转移后真正服务的那条 Provider 写进 providerId（不是首选），所以
 * 拿日志的 routeId 反查路由首选、再对照密钥的备选顺序，就能在纯前端算出
 * "首选没服务、这条走了备选 N"，不用加库字段。
 */
export type DecisionAggregate = {
  providerId: string;
  /** 出现在候选里/被尝试的次数 */
  attempts: number;
  /** 真正服务了这条请求的次数 */
  served: number;
  /** 发出去但失败（回答被丢弃或报错）的次数 */
  failed: number;
  /** 根本没发出去（未授权 / 未锁定模型 / 已禁用）的次数 */
  skipped: number;
  /** 平均到服务成功的耗时（毫秒），只看 served 的尝试 */
  avgServedMs?: number;
};

/**
 * 把请求日志里的调度尝试聚合成"每个账号被怎么对待"的一张表：多选一次就换号、
 * AI 网关最常见的排障问题（谁老被跳过、谁老失败）用不着逐条翻详情。
 * 只统计带 attempts 的日志（调度路径），其余日志不进这张表。
 */
export function aggregateScheduleAttempts(logs: LogEntry[]): DecisionAggregate[] {
  const byProvider = new Map<string, DecisionAggregate & { servedMsTotal: number }>();
  for (const log of logs) {
    const attempts = log.analysis?.attempts;
    if (!attempts || attempts.length === 0) continue;
    for (const attempt of attempts) {
      const providerId = attempt.providerId?.trim();
      if (!providerId) continue;
      const row = byProvider.get(providerId) ?? { providerId, attempts: 0, served: 0, failed: 0, skipped: 0, servedMsTotal: 0 };
      row.attempts += 1;
      // 判定看这一次尝试本身的结果，不能只看 deferred/skipped：唯一被尝试、
      // 但上游回了 401/429 的那一次既不是"跳过"也不是"被丢弃"，按旧口径会被
      // 记成"服务"，把失败藏起来。
      const status = attempt.status;
      const failedStatus = typeof status === 'number' && (status < 200 || status >= 300);
      if (attempt.skipped) {
        row.skipped += 1;
      } else if (attempt.deferred || failedStatus) {
        row.failed += 1;
      } else {
        row.served += 1;
        if (typeof attempt.ms === 'number') row.servedMsTotal += attempt.ms;
      }
      byProvider.set(providerId, row);
    }
  }
  return Array.from(byProvider.values())
    .map(({ servedMsTotal, ...row }) => ({
      ...row,
      avgServedMs: row.served > 0 ? Math.round(servedMsTotal / row.served) : undefined,
    }))
    .sort((a, b) => b.attempts - a.attempts || a.providerId.localeCompare(b.providerId));
}

export function trafficLogServing(log: LogEntry, providers: Provider[], routes: Route[], apiKeys: APIKey[]) {
  const route = routes.find((item) => item.id === log.routeId);
  const primaryId = route?.providerId || '';
  const servedId = log.providerId || '';
  if (!primaryId || !servedId || primaryId === servedId) return null;
  const primary = providers.find((item) => item.id === primaryId);
  const fallbacks = (apiKeys.find((item) => item.id === log.apiKeyId) || {}).fallbackProviderIds || [];
  const rank = fallbacks.indexOf(servedId);
  return {
    primary,
    rank: rank >= 0 ? rank + 1 : 0,
  };
}

// isWorkBuddyConnectKind 判定两个 WorkBuddy 构建：它们只在端点与模型表上不同，
// 控制台里的展示、徽标与登录流程都走同一套分支。
export function isWorkBuddyConnectKind(kind: ProviderConnectKind): boolean {
  return kind === 'workbuddy' || kind === 'workbuddy_ai';
}

// isQoderConnectKind 判定 Qoder 的两个站点：协议一模一样，主机、client id 与
// 模型目录不同，所以是两种连接方式而不是一个开关。
export function isQoderConnectKind(kind: ProviderConnectKind | undefined): boolean {
  return kind === 'qoder_pat' || kind === 'qoder_cn';
}

export function isZCodeConnectKind(kind: ProviderConnectKind): boolean {
  return kind === 'zcode' || kind === 'zcode_bigmodel';
}

export function isTraeConnectKind(kind: ProviderConnectKind): boolean {
  return kind === 'trae_cn' || kind === 'trae_global';
}

// 需要浏览器登录的订阅：卡片与编辑器共用一套「发起 → 等待 → 已连接」流程，
// 只是后端换了一个端点前缀。OpenCode Zen 免费档不在其中——它没有登录这一步。
export function isSubscriptionConnectKind(kind: ProviderConnectKind): kind is SubscriptionConnectKind {
  return kind === 'workbuddy' || kind === 'workbuddy_ai'
    || kind === 'zcode' || kind === 'zcode_bigmodel'
    || kind === 'trae_cn' || kind === 'trae_global';
}

// PROVIDER_AUTH_TYPES 是表单可以提交给后端的全部连接方式：白名单之外一律退回
// api_key，避免前端多出一个 authType 时把它原样写进 Provider。
export const PROVIDER_AUTH_TYPES = ['api_key', 'claude_oauth', 'cursor_oauth', 'chatgpt_oauth', 'qoder_pat', 'qoder_cn', 'workbuddy', 'workbuddy_ai', 'zcode', 'zcode_bigmodel', 'trae_cn', 'trae_global', 'opencode_zen_free', 'self_register'] as const;

export function providerAuthTypeParam(value: string): string {
  return (PROVIDER_AUTH_TYPES as readonly string[]).includes(value) ? value : 'api_key';
}
// subscriptionOAuthPrefix 是订阅登录那几个端点共用的路径段。
export function subscriptionOAuthPrefix(kind: ProviderConnectKind): string {
  if (isQoderConnectKind(kind)) return 'qoder-oauth';
  if (isZCodeConnectKind(kind)) return 'zcode-oauth';
  if (isTraeConnectKind(kind)) return 'trae-oauth';
  return 'workbuddy-oauth';
}

/**
 * checkinReport 把签到结果写成卡片上的一行。
 *
 * 只说厂商给了的：连续天数只有 WorkBuddy 报，Qoder 的活动接口不报，所以 Qoder
 * 那行不会出现「连续 N 天」；积分为 0 时也不编一个「+0」。
 * 「立即签到」按钮在今天的还没到手时出现（dueToday），失败的那次也算还没到手。
 */
export function checkinReport(view?: SubscriptionCheckin): {
  tone: 'ok' | 'bad' | 'wait' | 'none';
  text: string;
  hint: string;
  dueToday: boolean;
} {
  if (!view?.supported) return { tone: 'none', text: '', hint: '', dueToday: false };
  const record = view.record;
  const today = record && record.day === view.today ? record : undefined;
  const last = !today && (record?.outcome === 'claimed' || record?.outcome === 'done')
    ? `上次签到 ${record?.day}`
    : '';
  const hint = record?.msg || '';
  if (today) {
    switch (today.outcome) {
      case 'claimed':
      case 'done': {
        const credit = (today.credit ?? 0) > 0 ? ` +${trimNumber(today.credit ?? 0)}` : '';
        const streak = (today.streak ?? 0) > 0 ? ` · 连续 ${today.streak} 天` : '';
        return { tone: 'ok', text: `今日已签到${credit}${streak}`, hint, dueToday: false };
      }
      case 'ineligible':
        return { tone: 'none', text: '这个账号不参与每日签到', hint, dueToday: false };
      case 'inactive':
        return { tone: 'none', text: '当前没有签到活动', hint, dueToday: false };
      case 'own-app':
        return { tone: 'none', text: '只能在厂商自己的应用里签到', hint, dueToday: false };
      case 'failed':
        return {
          tone: 'bad',
          text: [view.enabled ? '签到失败，稍后自动重试' : '签到失败', hint].filter(Boolean).join(' · '),
          hint,
          dueToday: true,
        };
    }
  }
  if (view.enabled) return { tone: 'wait', text: ['今日未签到', last].filter(Boolean).join(' · '), hint, dueToday: true };
  return { tone: 'none', text: ['自动签到已关闭', last].filter(Boolean).join(' · '), hint, dueToday: true };
}

// trimNumber 把 100.0 写成 100，把 0.5 原样留着：厂商给的额度有时是浮点。
function trimNumber(value: number): string {
  return Number.isInteger(value) ? String(value) : String(Number(value.toFixed(2)));
}

// presetMatchesProvider 判断一个 Provider 是否属于目录里的某个预设，用于磁贴上
// 的「已添加」标记与计数。订阅类按连接方式归组（同一账号族可以有多个 Provider）；
// API Key 类按端点主机归组，因为同一个厂商的多个 Provider 通常指向同一主机。
export function presetMatchesProvider(preset: ProviderPreset, provider: Provider): boolean {
  if (preset.authType === 'api_key') {
    if (providerConnectKind(provider) !== 'api_key') return false;
    const presetHost = urlHost(preset.baseUrl);
    return presetHost !== '' && presetHost === urlHost(provider.baseUrl);
  }
  return providerConnectKind(provider) === preset.authType;
}

function urlHost(value?: string): string {
  const raw = (value || '').trim();
  if (!raw) return '';
  // 目录有 50 个预设，每张 Provider 卡都要比对一遍主机名，而控制台是轮询刷新的；
  // 主机名对同一字符串永远不变，缓存掉这点重复解析。
  const cached = urlHostCache.get(raw);
  if (cached !== undefined) return cached;
  let host: string;
  try {
    host = new URL(raw).host.toLowerCase();
  } catch {
    // 相对地址或占位值（example.com/v1）：退化为取第一段主机名。
    host = raw.replace(/^https?:\/\//, '').split('/')[0].toLowerCase();
  }
  urlHostCache.set(raw, host);
  return host;
}

const urlHostCache = new Map<string, string>();

export function providerConnectLabel(kind: ProviderConnectKind): string {
  switch (kind) {
    case 'claude_oauth': return '登录 Claude 账号 (OAuth)';
    case 'cursor_oauth': return '登录 Cursor 账号 (OAuth)';
    case 'chatgpt_oauth': return '登录 ChatGPT 账号 (OAuth)';
    case 'qoder_pat': return '登录 Qoder 账号';
    case 'qoder_cn': return '登录 Qoder CN 账号';
    case 'workbuddy': return '登录 WorkBuddy 账号';
    case 'workbuddy_ai': return '登录 WorkBuddy AI 账号';
    case 'zcode': return '登录 ZCode（Z.ai 套餐）';
    case 'zcode_bigmodel': return '登录 ZCode（智谱 BigModel 套餐）';
    case 'trae_cn': return '登录 Trae CN 账号';
    case 'trae_global': return '登录 Trae 国际版账号';
    case 'opencode_zen_free': return 'OpenCode Zen 免费档（无需密钥）';
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
  { id: 'qoder_pat', label: 'Qoder' },
  { id: 'qoder_cn', label: 'Qoder CN' },
  { id: 'workbuddy', label: 'WorkBuddy' },
  { id: 'workbuddy_ai', label: 'WorkBuddy AI' },
  { id: 'zcode', label: 'ZCode (Z.ai)' },
  { id: 'zcode_bigmodel', label: 'ZCode (智谱)' },
  { id: 'trae_cn', label: 'Trae CN' },
  { id: 'trae_global', label: 'Trae 国际版' },
  { id: 'opencode_zen_free', label: 'OpenCode Zen 免费' },
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
    codexForceFast: patch.codexForceFast ?? key.codexForceFast ?? false,
    enabled: patch.enabled ?? key.enabled,
    fallbackProviderIds: patch.fallbackProviderIds ?? key.fallbackProviderIds ?? [],
    fallbackModelOverrides: patch.fallbackModelOverrides ?? key.fallbackModelOverrides ?? {},
    scheduleMode: patch.scheduleMode ?? key.scheduleMode ?? '',
    // 额度三件套必须一起重发：后端把「没带字段」当成「保留原值」，但控制台走的是
    // 全量重发，这里漏一个就等于把别的字段顺手抹掉。
    quotaWindow: patch.quotaWindow ?? key.quotaWindow ?? '',
    quotaTokens: patch.quotaTokens ?? key.quotaTokens ?? 0,
    quotaCostMicros: patch.quotaCostMicros ?? key.quotaCostMicros ?? 0,
  };
}

// QuotaWindowOptions 是密钥额度的重置口径。窗口按网关进程的本机时区算：
// 生产 VPS 固定 TZ=Asia/Shanghai，所以「每天 0 点」就是北京时间 0 点。
export const QuotaWindowOptions: { value: string; label: string; hint: string }[] = [
  { value: '', label: '不限额度', hint: '该密钥不受额度门约束（升级前所有密钥的默认状态）。' },
  { value: 'day', label: '每天（0 点重置）', hint: '按网关本机时区的自然日累计，跨过 0 点重新计。' },
  { value: 'month', label: '每月（1 号 0 点重置）', hint: '按网关本机时区的自然月累计。' },
];

export function quotaWindowLabel(value?: string) {
  return QuotaWindowOptions.find((option) => option.value === (value || ''))?.label ?? '不限额度';
}

// ScheduleModeLabels 是密钥「调度模式」的界面文案。顺序与后端接受的取值一致，
// 空值（老密钥）就是按链路顺序。
export const ScheduleModeOptions: { value: string; label: string; hint: string }[] = [
  { value: '', label: '按链路顺序（默认）', hint: '首选优先，故障时顺位下移；已在承接的备选保持承接。' },
  { value: 'rotate', label: '轮流分配（rotate）', hint: '每个请求换下一个候选，把流量摊到整条链上。' },
  { value: 'least_used', label: '最空闲优先（least_used）', hint: '按近几分钟各账号实际处理的 token 量排序，缓存命中不计入。' },
  { value: 'quota', label: '额度最多优先（quota）', hint: '读额度面板已缓存的窗口余量，剩得多的先用；读不到额度的账号排在中位。' },
  { value: 'spread', label: '会话分散（spread）', hint: '按每个账号当前挂着的活跃会话数排序：新会话落到会话最少的账号，之后钉在该账号两小时（Prompt Cache 最省）。不同会话并行在不同账号上。' },
];

export function scheduleModeLabel(mode?: string) {
  return ScheduleModeOptions.find((item) => item.value === (mode || ''))?.label || ScheduleModeOptions[0].label;
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

/**
 * fallbackAdd 把一批 Provider 追加到备选链末尾；已经在链上的跳过。
 * 备选链是有序的（先试的在前），新增一律排在末尾，不改动已有位次。
 */
export function fallbackAdd(ids: string[], add: string[]): string[] {
  const have = new Set(ids);
  const next = ids.slice();
  for (const id of add) {
    if (!id || have.has(id)) continue;
    have.add(id);
    next.push(id);
  }
  return next;
}

/**
 * fallbackRemove 把一批 Provider 从备选链移除，其余顺序不变。
 */
export function fallbackRemove(ids: string[], remove: string[]): string[] {
  if (remove.length === 0) return ids;
  const drop = new Set(remove);
  return ids.filter((id) => !drop.has(id));
}

/**
 * fallbackMove 把 fromId 挪到 toId 的前面（before）或后面（after）。
 * 只在两边都在链上时生效；位置没变就原样返回，调用方据此跳过保存。
 */
export function fallbackMove(ids: string[], fromId: string, toId: string, after: boolean): string[] {
  if (fromId === toId) return ids;
  const fromIndex = ids.indexOf(fromId);
  if (fromIndex < 0 || !ids.includes(toId)) return ids;
  const next = ids.filter((id) => id !== fromId);
  const targetIndex = next.indexOf(toId);
  next.splice(after ? targetIndex + 1 : targetIndex, 0, fromId);
  return next.every((id, index) => id === ids[index]) ? ids : next;
}

/**
 * providerMatchesQuery 是备选选择器里两侧共用的搜索口径：品牌名、账号、ID、
 * 协议名任一命中即可。多个词按 AND 收窄（"claude 4947" 只留 Claude 里账号含
 * 4947 的那条）。
 */
export function providerMatchesQuery(provider: Provider, query: string): boolean {
  const words = query.trim().toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return true;
  // providerAccountLabel 只在账号有区分度时才给值（昵称 "1" 这类会被换掉），
  // 但用户搜的往往正是那个昵称本身，所以各家的原始标签也一起进索引。
  const haystack = [
    providerOptionLabel(provider),
    provider.name,
    providerAccountLabel(provider),
    provider.claudeOAuth?.accountLabel,
    provider.cursorOAuth?.accountLabel,
    provider.chatgptOAuth?.accountLabel,
    provider.qoderPat?.accountLabel,
    provider.qoderPat?.email,
    provider.workbuddyOAuth?.accountLabel,
    provider.id,
    providerConnectKind(provider),
  ].filter(Boolean).join(' ').toLowerCase();
  return words.every((word) => haystack.includes(word));
}

/**
 * promoteApiKeyProvider 把一台备选提为首选，返回要一起提交的绑定字段。
 *
 * 「设为首选」是交换，不是删除：旧首选回到被提升者原来的位次，其余备选顺序不变。
 * 只改 routeId 会让后端把新首选从备选里剔掉（normalizeFallbackProviderIDs），
 * 旧首选又不补回来，于是每点一次就少一个节点。
 *
 * 固定的模型（fallbackModelOverrides 按 Provider 记）跟着 Provider 走：旧首选的
 * 条目要搬进新首选的备选条目，否则后端会把它当成已删除 Provider 的残留丢掉。
 */
export function promoteApiKeyProvider(
  promoteProviderId: string,
  fallbacks: { providerIds: string[]; modelOverrides: Record<string, string> },
  currentPrimaryProviderId: string,
): { providerId: string; fallbackProviderIds: string[]; fallbackModelOverrides: Record<string, string> } {
  const ids = fallbacks.providerIds.slice();
  const index = ids.indexOf(promoteProviderId);
  if (index < 0) {
    // 不是备选（界面上不会出现这个按钮）：原样交回，宁可不动也别拆掉别人。
    return { providerId: promoteProviderId, fallbackProviderIds: ids, fallbackModelOverrides: { ...fallbacks.modelOverrides } };
  }
  if (currentPrimaryProviderId && currentPrimaryProviderId !== promoteProviderId) {
    // 位次守恒：旧首选站在被提升者原来的位置，其余相对顺序不变。
    ids[index] = currentPrimaryProviderId;
  } else {
    ids.splice(index, 1);
  }
  const overrides: Record<string, string> = {};
  for (const id of ids) {
    const model = (fallbacks.modelOverrides[id] || '').trim();
    if (model) overrides[id] = model;
  }
  // 旧首选的固定模型跟着它搬到备选位置上：后端会丢掉不属于任何备选的条目，
  // 不搬就等于把原首选锁的模型顺手清空。
  return { providerId: promoteProviderId, fallbackProviderIds: ids, fallbackModelOverrides: overrides };
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

/**
 * 按 providerId 取展示名。同一个服务商多登一个账号就是一条独立记录、名字完全一样，
 * 所以这里必须带上账号标识——否则环形图、排行、筛选器里并排出现两行「WorkBuddy 订阅」，
 * 读的人分不清谁是谁。没有账号概念的 Provider（api_key 类）仍只有名字。
 */
export function providerUsageLabel(providerId: string, providers: Provider[]) {
  if (providerId === '_unknown') return '未知 Provider';
  const provider = providers.find((item) => item.id === providerId);
  return provider ? providerDisplayLabel(provider) : providerId;
}

/**
 * 窄卡片（环形图图例、排行条）用的两行标签：第一行名字、第二行账号。
 * 这些格子只有一百来像素宽，把「名称 · 账号」塞一行必然被截断，而被截掉的
 * 恰好是唯一能区分账号的那一段——分两行才既认得出厂商又认得出是谁。
 */
export function providerUsageParts(providerId: string, providers: Provider[]) {
  if (providerId === '_unknown') return { label: '未知 Provider', sub: '' };
  const provider = providers.find((item) => item.id === providerId);
  if (!provider) return { label: providerId, sub: '' };
  const account = providerAccountLabel(provider);
  const sub = account && account !== provider.name ? shortenAccountLabel(account) : '';
  return { label: provider.name, sub };
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
  // 示例要能被复制到别处直接跑：优先用当前访问控制台的域名，别给自报网卡 IP。
  const localRoot = browserAccessOrigin() || `http://${endpoint.listenHost}:${endpoint.listenPort}`;
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
  if (route.outputProtocol === 'gemini') return `${root}/models/{model}:generateContent`;
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
  if (route.outputProtocol === 'gemini') return 'Gemini SDK：Base URL 填到 /v1beta（Google 会拼 /models/{model}:generateContent）+ x-goog-api-key';
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
  } else if (protocol === 'gemini') {
    // Google 的 SDK 自己拼 /models/{model}:generateContent。
    npm = '@ai-sdk/google';
    baseURL = `${root}/v1beta`;
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

/**
 * 浏览器是从哪个地址打开控制台的，那也就是客户端真正能连通的地址：域名反代
 * （Caddy/Nginx）后面，网关自报的网卡 IP 可能已经不对公网开放。loopback 打开
 * （桌面端、服务器本机）没有可外发的地址，返回空串。
 */
export function browserAccessOrigin() {
  if (typeof window === 'undefined') return '';
  const {protocol, hostname, origin} = window.location;
  if (protocol !== 'https:' && protocol !== 'http:') return '';
  const host = hostname.replace(/^\[|\]$/g, '').toLowerCase();
  if (host === 'localhost' || host === '::1' || host.startsWith('127.')) return '';
  return origin.replace(/\/$/, '');
}

/** 客户端接入根地址：隧道/自定义域名优先，其次当前浏览器 origin，最后退回网卡地址。 */
export function clientGatewayRoot(endpoints: OutputEndpoint[], publicBase: string) {
  return (publicBase || browserAccessOrigin() || localGatewayRoot(endpoints)).replace(/\/$/, '');
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

/**
 * 客户端调网关该带哪个认证头。网关的 extractConsumerAPIKey 按
 * x-api-key → x-goog-api-key → Authorization: Bearer 的顺序认，所以：
 * - Gemini：Google SDK 只发 x-goog-api-key，示例必须用它；
 * - Claude：Anthropic 客户端发 x-api-key；
 * - OpenAI Chat / Responses：Bearer。上游 Provider 若配了 x-api-key 认证头，
 *   也不影响客户端这边——网关自己的密钥入口只认上面那套头。
 */
export function apiKeyClientAuth(key: APIKey, route: Route): { header: string; value: string } {
  if (route.outputProtocol === 'gemini') return { header: 'x-goog-api-key', value: key.key };
  if (route.outputProtocol === 'claude') return { header: 'x-api-key', value: key.key };
  return { header: 'Authorization', value: `Bearer ${key.key}` };
}

/** curl 示例里的模型：密钥/Provider 没配模型时给一个能直接改的占位名，不把 {model} 原样交出去。 */
export const API_KEY_SAMPLE_PLACEHOLDER_MODEL = 'your-model';
export function apiKeySampleModel(key: APIKey, provider?: Provider) {
  return resolveApiKeyModel(key, provider);
}

/**
 * 密钥详情里的「调用示例」：URL、请求体与认证头都按该密钥的输出协议拼。Gemini
 * 的 URL 带 {model} 占位，示例里换上示例模型名；模型本身也是占位时给出「替换
 * 模型名」的注释，用户复制后不会把字面量 {model} 发给网关。
 */
export function buildApiKeyPublicCurl(key: APIKey, route: Route, endpoints: OutputEndpoint[], publicBase: string, provider?: Provider) {
  const model = apiKeySampleModel(key, provider);
  const payload = buildApiKeyCallPayload(route, model);
  const template = publicBase
    ? routeGatewayURL(route, endpoints, publicBase)
    : routeGatewayURL(route, endpoints, localGatewayRoot(endpoints));
  const url = route.outputProtocol === 'gemini' ? geminiCurlURL(template, model) : template;
  const notes: string[] = [];
  if (route.outputProtocol === 'gemini') {
    if (model === API_KEY_SAMPLE_PLACEHOLDER_MODEL) {
      notes.push(`# ${model} 是占位：先给密钥固定一个 Gemini 模型，或把它换成真实模型名`);
    }
    if (!isGeminiGenerateContentPayload(payload)) {
      // 网关的 Gemini 端点要 Gemini 原生的请求体；给一条 OpenAI 形状的 JSON 只会收 400。
      // 路由的「测试调用」按该路由的输出协议发请求，这里与它保持一致。
      notes.push('# Gemini 原生端点要 Gemini 请求体：内容按 contents[].parts[] 组织（见路由「测试调用」）');
    }
  }
  const curl = buildChatTestCurl(url, payload, apiKeyClientAuth(key, route));
  return notes.length ? `${notes.join('\n')}\n${curl}` : curl;
}

function isGeminiGenerateContentPayload(payload: Record<string, unknown>) {
  return Array.isArray(payload.contents) && !('messages' in payload);
}

/** Gemini 的 :generateContent 路径里的 {model} 换成示例模型名（远端模型 id 可能带 models/ 前缀）。 */
function geminiCurlURL(template: string, model: string) {
  return template.split('{model}').join(model);
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
  if (isQoderConnectKind(provider.authType)) {
    const resolvedModel = model.trim() || provider.defaultModel || 'ultimate';
    const payload = buildProviderChatPayload(resolvedModel, buildProviderChatMessages(options));
    const body = JSON.stringify(payload, null, 2);
    // 这条命令照抄跑不通，所以直接说明：Qoder 的上游不是 OpenAI 兼容端点，请求要
    // 带 Qoder 客户端的 COSY 签名、正文走它自己的编码，curl 复现不了。
    return `# Qoder 的上游不是 OpenAI 兼容端点：网关会把这份请求改写成 agent_chat_generation，\n`
      + `# 并用账号令牌加上 COSY 签名，所以这条 curl 不能直接照抄。\n`
      + `# 想自己试，请打本网关的对话接口（地址在「路由与密钥」页）：\n`
      + `curl -s '<本网关的 openai_chat 地址>/chat/completions' \\\n  -H 'Content-Type: application/json' \\\n  -H 'Authorization: Bearer <本网关的 API 密钥>' \\\n  -d '${body.replace(/'/g, `'\\''`)}'`;
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
    case 'gemini': return 'green';
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
  'thermal_zone': 'sysfs 温控区',
  'hwmon': 'hwmon 传感器',
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

export function formatMiBps(bytesPerSecond: number | undefined): string {
  if (bytesPerSecond == null || !Number.isFinite(bytesPerSecond) || bytesPerSecond < 0) return '—';
  const mib = bytesPerSecond / (1024 * 1024);
  if (mib >= 100) return `${mib.toFixed(0)} MB/s`;
  return `${mib.toFixed(1)} MB/s`;
}

export function formatLoad(load: number | undefined): string {
  if (load == null || !Number.isFinite(load)) return '—';
  return load.toFixed(2);
}

/** 进程的 RSS 来自 /proc 还是 Go runtime（后者偏小，界面要说明来源）。 */
export function hostMetricsPollMs(): number {
  return isRemoteOrigin() ? 5000 : 3000;
}

export function formatLocalISODate(date: Date) {
  const y = date.getFullYear();
  const m = String(date.getMonth() + 1).padStart(2, '0');
  const d = String(date.getDate()).padStart(2, '0');
  return `${y}-${m}-${d}`;
}

/**
 * 把 YYYY-MM-DD 补成带 RFC3339 偏移的时间。不传 timeZone（或传 ""）时用「浏览器
 * 本地时区」的偏移；传网关的时区名（/__state 的 serverTimeZone）时按那个时区算。
 * 后端对纯日期按网关所在机器的时区解析；浏览器与网关不同时区时（如网关在美国、
 * 用户在北京），「今天」会错位。用量统计与日志筛选都该用 timeZone=网关时区，
 * 这样「今日」和每个密钥的「今日 N 次」才是同一天。
 * endOfDay=true 取当天 23:59:59.999（后端对 RFC3339 的 to 不再 +24h）。非日期格式原样返回。
 */
export function localDateToRFC3339(date: string, endOfDay = false, timeZone = '') {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(date)) return date;
  const time = endOfDay ? 'T23:59:59.999' : 'T00:00:00';
  // 偏移取日界那一刻的（夏令时切换当天也不会差一小时）。
  const zoneOffset = timeZone ? zoneOffsetMinutesAt(new Date(`${date}${endOfDay ? 'T23:59:59.999' : 'T00:00:00'}Z`), timeZone) : null;
  const offsetMin = zoneOffset ?? -new Date(`${date}${time}`).getTimezoneOffset();
  const sign = offsetMin < 0 ? '-' : '+';
  const abs = Math.abs(offsetMin);
  return `${date}${time}${sign}${String(Math.floor(abs / 60)).padStart(2, '0')}:${String(abs % 60).padStart(2, '0')}`;
}

/**
 * zoneOffsetMinutesAt 求 timeZone 在某一时刻的偏移分钟数（东正西负）。Intl 的
 * timeZoneName:'longOffset' 给的是 "GMT+08:00"；浏览器不认识这个时区名（或运行时
 * 不支持 longOffset）时返回 null，调用方退回浏览器时区。
 */
export function zoneOffsetMinutesAt(at: Date, timeZone: string): number | null {
  if (!timeZone) return null;
  try {
    const parts = new Intl.DateTimeFormat('en-US', { timeZone, timeZoneName: 'longOffset' }).formatToParts(at);
    const name = parts.find((part) => part.type === 'timeZoneName')?.value || '';
    const match = /^GMT(?:([+-])(\d{1,2})(?::(\d{2}))?)?$/.exec(name);
    if (!match) return null;
    const sign = match[1] === '-' ? -1 : 1;
    return sign * ((Number(match[2] || 0) * 60) + Number(match[3] || 0));
  } catch {
    return null;
  }
}

/**
 * serverDayBounds 给「今日」这类服务器日口径的查询算起止：按网关的时区挑出"服务器
 * 今天"的日期，再从那天的 00:00 到 23:59:59.999（带该时区偏移）。
 */
export function serverDayBounds(now: Date, timeZone: string): { from: string; to: string } {
  const day = formatDateInZone(now, timeZone);
  return { from: localDateToRFC3339(day, false, timeZone), to: localDateToRFC3339(day, true, timeZone) };
}

/** formatDateInZone 把时刻写成某个时区的 YYYY-MM-DD；时区名为空或无效时用浏览器本地时区。 */
export function formatDateInZone(date: Date, timeZone: string): string {
  if (!timeZone) return formatLocalISODate(date);
  try {
    const parts = new Intl.DateTimeFormat('en-US', { timeZone, year: 'numeric', month: '2-digit', day: '2-digit' }).formatToParts(date);
    const get = (type: string) => parts.find((part) => part.type === type)?.value || '';
    const year = get('year');
    if (!year || !get('month') || !get('day')) return formatLocalISODate(date);
    return `${year}-${get('month')}-${get('day')}`;
  } catch {
    return formatLocalISODate(date);
  }
}

/**
 * 健壮的剪贴板复制工具，兼容 HTTPS 与非安全上下文（如局域网 / 纯 IP HTTP 访问）。
 * 优先调用现代 navigator.clipboard.writeText；若不可用（如非安全上下文或无权限）则回退到
 * 隐藏 textarea + document.execCommand('copy')，确保局域网、内网穿透与各端浏览器均能正常复制。
 */
export async function copyToClipboard(text: string): Promise<boolean> {
  if (!text) return false;

  // 1. 尝试现代异步剪贴板 API（安全上下文 HTTPS / localhost 下可用）
  if (typeof navigator !== 'undefined' && navigator.clipboard && typeof navigator.clipboard.writeText === 'function') {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // 现代 API 被浏览器策略拦截时，继续向下尝试 execCommand
    }
  }

  // 2. 降级方案：创建隐藏 textarea 借助 document.execCommand('copy') 完成复制
  try {
    if (typeof document !== 'undefined') {
      const textarea = document.createElement('textarea');
      textarea.value = text;
      textarea.style.position = 'fixed';
      textarea.style.left = '-9999px';
      textarea.style.top = '0';
      textarea.style.opacity = '0';
      textarea.setAttribute('readonly', '');
      document.body.appendChild(textarea);
      textarea.focus();
      textarea.select();
      textarea.setSelectionRange(0, textarea.value.length);
      const successful = document.execCommand('copy');
      document.body.removeChild(textarea);
      if (successful) return true;
    }
  } catch {
    // 降级亦失败
  }

  return false;
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
  for (let i = 0; i < count; i += step) {
    // The last endpoint already has a label; skip regular ticks that would
    // stack right next to it.
    if (count - 1 - i < step) break;
    indexes.add(i);
  }
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

/**
 * 成本在网关侧以微美元（1e-6 USD）整数落库：按请求取整再相加才不会与日聚合漂移，
 * 所以换算只发生在这里。undefined 是「这一行没有成本字段」（旧数据或未计价），
 * 与 $0.0000（设了价、算出来就是零）必须区分开，调用方先用 priced 判过再传。
 */
export function formatCostMicros(micros: number | undefined): string {
  if (micros == null) return '—';
  const usd = micros / 1_000_000;
  if (Math.abs(usd) < 1) return `$${usd.toFixed(4)}`;
  return `$${usd.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
}

/**
 * 计价覆盖率。成本总额只累加了有价格行的请求，光看金额会把「大半没设价」读成
 * 「很便宜」，所以金额旁边必须跟着这个比例。
 */
export function formatCostCoverage(pricedCount: number | undefined, requestCount: number): string {
  const priced = pricedCount || 0;
  if (!requestCount) return '暂无请求';
  if (priced === 0) return '本区间全部未计价';
  if (priced === requestCount) return '全部请求已计价';
  return `已计价 ${priced.toLocaleString()} / ${requestCount.toLocaleString()} 次请求`;
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
  fast: { label: '快速', multiplier: '0.8x' },
  performance: { label: '性能', multiplier: '1.1x' },
  auto: { label: '智能路由', multiplier: '1.0x' },
  efficient: { label: '经济', multiplier: '0.3x' },
  lite: { label: '轻量', multiplier: '免费' },
};

/**
 * isFreeModel 判定"这个模型不花套餐积分"。除了上游明确标的 free，还认名字里
 * 带 free 的（magpie 的同一手：有些上游只在名字里说免费，不给价格字段）。
 */
export function isFreeModel(model: Model): boolean {
  if (model.free) return true;
  return /(^|[^a-z])free($|[^a-z])/i.test(`${model.id} ${model.name || ''}`);
}

// modelRateTimes 是倍率的写法：<0.01 一律收成 "<0.01"，否则两位小数去掉尾零
// （0.50→0.5、1.00→1）。上游按 0 报倍率等于没报，返回空不渲染。
function modelRateTimes(rate: number): string {
  if (!Number.isFinite(rate) || rate <= 0) return '';
  if (rate < 0.01) return '<0.01×';
  return `${Number(rate.toFixed(2))}×`;
}

/**
 * modelRateLabel 给出倍率与被划线的原价；只有原价确实高于现价时才带出原价，
 * 免费模型不显示 0×。
 */
export function modelRateLabel(model: Model): { rate: string; was: string } | null {
  const rate = modelRateTimes(model.free ? 0 : model.rate || 0);
  const wasValue = modelRateTimes(model.rateWas || 0);
  const was = rate && wasValue && (model.rate || 0) < (model.rateWas || 0) ? wasValue : '';
  if (!rate && !was) return null;
  return { rate, was };
}

/**
 * modelContextLabel 只在窗口到 1M 及以上、且名字里没写过 M 时给一个短标签：
 * 200K 这类窗口在列表里到处都有，标出来只会淹没真正的大窗口。
 */
export function modelContextLabel(model: Model): string {
  const context = model.contextLength || 0;
  if (context < 1_000_000) return '';
  if (/\b\d+(\.\d+)?m\b/i.test(`${model.id} ${model.name || ''}`)) return '';
  const millions = context / 1_000_000;
  return `${Number.isInteger(millions) ? millions : millions.toFixed(1)}M`;
}

/**
 * modelAlias 是上游给这个模型起的名字，只在它确实不同于 id 时给出。
 * id 仍是各处的主角（请求里用的就是它、点击即复制），别名只作次级说明：
 * Qoder 这类「按 key 路由、按品牌名展示」的上游，光看 id 是一串代号。
 */
export function modelAlias(model: Model): string {
  return model.name && model.name !== model.id ? model.name : '';
}

export function modelSelectOptionLabel(model: Model) {
  const tier = QODER_TIER_INFO[model.id];
  const out = formatModelOutputBudget(model.maxOutputTokens);
  // Qoder 按 key 路由（qmodel_38max），但它自己的界面写的是 Qwen3.8-Max：下拉框
  // 里给后者，请求里仍是前者。filterModelOptions 同时匹配 id，搜旧名字照样找得到。
  const label = modelAlias(model) || model.id;
  // 下拉框里也要能看出免费与倍率：选模型时最想知道的就是这个。
  const price = [isFreeModel(model) ? '免费' : '', modelRateLabel(model)?.rate || '', modelContextLabel(model)].filter(Boolean).join(' ');
  const suffix = [price, out ? `out ${out}` : ''].filter(Boolean).join(' · ');
  const tierText = tier ? `${tier.label} ${tier.multiplier}` : '';
  const head = tierText ? `${label}（${tierText}）` : label;
  return suffix ? `${head} · ${suffix}` : head;
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

/**
 * 智能识别模型是否具备深度思考 / 推理（Thinking / Reasoning）能力。
 * 覆盖主流厂商的推理模型族：
 * 1. 显式推理/思考关键词：thinking, reasoner, reasoning, thought, cot 等；
 * 2. OpenAI：o1, o3, o4 系列（如 o1, o1-mini, o1-preview, o3-mini, o4 等）；
 * 3. DeepSeek：deepseek-reasoner, deepseek-r1, r1 系列及各类蒸馏/微调模型（如 deepseek-ai/DeepSeek-R1-Distill-* 等）；
 * 4. Anthropic Claude：Claude 3.7 Sonnet（首款支持 Extended Thinking）及后续 Claude 4 / 5 代的 Sonnet、Opus 与 Mythos 系列；
 * 5. 智谱 GLM：glm-zero 系列（如 glm-zero-preview, glm-4-zero 等 Zero 推理族）、glm-4-airx；
 * 6. 通义千问 Qwen：qwq 系列（如 qwq-32b）、qvq；
 * 7. 月之暗面 Kimi：k1.5, kimi-k1.5 系列；
 * 8. 其他主流专用推理模型：MiniMax-Text-01, Marco-o1 等。
 */
export function isThinkingModel(modelID: string): boolean {
  if (!modelID) return false;
  const id = modelID.toLowerCase();

  // 1. 显式思考/推理词根 (thinking, reasoner, reasoning, thought, cot 等)
  if (/(?:thinking|reasoner|reasoning|thought|\bcot\b)/i.test(id)) {
    return true;
  }

  // 2. OpenAI o1 / o3 / o4 系列推理模型 (o1, o1-mini, o1-preview, o3, o3-mini, o4 等)
  if (/(?:^|[-_./])o[134](?:[-_./]|$)/i.test(id)) {
    return true;
  }

  // 3. DeepSeek R1 及其各类蒸馏 / 部署命名 (deepseek-reasoner, deepseek-r1, r1, r1-distill 等)
  if (/(?:^|[-_./])(?:r1|deepseek-r1)(?:[-_./]|$)/i.test(id) || /deepseek-reasoner/i.test(id)) {
    return true;
  }

  // 4. Anthropic Claude 系列 Extended Thinking 深度思考模型
  // 从 Claude 3.7 Sonnet 引入原生深度思考，以及 4.x / 5.x 世代的 Sonnet、Opus 与 Mythos 系列
  if (/(?:claude-)?(?:3[.-]7-sonnet|sonnet-[45]|opus-[45]|mythos)/i.test(id)) {
    return true;
  }

  // 5. 智谱 GLM 推理模型族 (glm-zero 系列、glm-4-zero、极速推理 glm-4-airx 等)
  if (/glm-(?:zero|4-zero|4-airx)/i.test(id) || /(?:^|[-_./])(?:glm-zero|zero-preview)(?:[-_./]|$)/i.test(id)) {
    return true;
  }

  // 6. 阿里通义千问 QwQ / QvQ 推理模型
  if (/(?:^|[-_./])q[wv]q(?:[-_./]|$)/i.test(id)) {
    return true;
  }

  // 7. 月之暗面 Kimi k1.5 推理模型
  if (/(?:^|[-_./])(?:k1\.5|kimi-k1\.5)(?:[-_./]|$)/i.test(id)) {
    return true;
  }

  // 8. 其他知名专有推理模型
  if (/(?:minimax-text-01|marco-o1)/i.test(id)) {
    return true;
  }

  return false;
}

/**
 * 智能识别模型是否具备视觉 / 多模态（Vision / Multimodal）输入能力。
 * 1. 显式视觉/多模态关键词：vision, visual, multimodal, pixtral, omni, vl 等；
 * 2. OpenAI：gpt-4o, gpt-4o-mini, gpt-4-turbo, gpt-4.5 等；
 * 3. Anthropic Claude：Claude 3/3.5/3.7/4/5 全系列的 sonnet, opus, haiku 均原生支持图片视觉多模态；
 * 4. Google Gemini：全系列原生多模态；
 * 5. 智谱 GLM：glm-4v, glm-edge-v 等视觉系列；
 * 6. 通义千问及开源多模态：qwen-vl, qvq, internvl, minicpm-v, llama-*-vision 等。
 */
export function isVisionModel(modelID: string): boolean {
  if (!modelID) return false;
  const id = modelID.toLowerCase();

  // 1. 显式视觉 / 多模态词根
  if (/(?:vision|visual|multimodal|pixtral|omni|[-_](?:vl|vlm|4v)|[-_]v\b|internvl|minicpm-v)/i.test(id)) {
    return true;
  }

  // 2. OpenAI 具备视觉能力的多模态模型
  if (/(?:4o|4-turbo|4\.5)/i.test(id)) {
    return true;
  }

  // 3. Anthropic Claude 3 及后续全系列（Sonnet, Opus, Haiku 原生均支持图像视觉多模态，排除仅文本的实验/创作型号）
  if (/(?:claude-(?:3|4|5|sonnet|opus|haiku)|(?:sonnet|opus|haiku)-[345])/i.test(id)) {
    if (id.includes('fable')) return false;
    return true;
  }

  // 4. Google Gemini 全系列原生多模态
  if (/gemini/i.test(id)) {
    return true;
  }

  // 5. 智谱 GLM 视觉系列 (glm-4v, glm-edge-v 等)
  if (/glm-(?:.*v|edge-v)/i.test(id)) {
    return true;
  }

  // 6. 通义千问及各类多模态模型 (qwen-vl, qvq 等)
  if (/(?:qwen.*vl|qvq)/i.test(id)) {
    return true;
  }

  return false;
}


// ---------------------------------------------------------------------------
// Cloudflare 授权轮询
// ---------------------------------------------------------------------------

/** 授权轮询一次检查的结果：还没授权就继续等，授权成功或超时则收尾。 */
export type CloudflareAuthPollOutcome = 'wait' | 'authorized' | 'timeout'

/**
 * cloudflareAuthPollOutcome 是授权轮询「这一次检查之后还要不要继续」的唯一判定。
 *
 * 早先这段判断写在定时器的回调里，而第一次检查（`void check()`）在定时器 id
 * 装进 ref 之前就跑了：控制台本来就已经授权时，第一次检查立刻成功，
 * `stopCloudflareLoginPoll()` 清掉的是一个还不存在的 id，随后装上来的 2 秒
 * 定时器再也没人停 —— 每一次都请求 status（成功后还会连带取 zones），
 * 并且每次都 setState 重渲染整个控制台，直到关掉标签页。把判定抽成纯函数，
 * 让「先检查、拿到结果再决定要不要装定时器」这件事可以被直接测到。
 */
export function cloudflareAuthPollOutcome(input: { authorized: boolean; elapsedMs: number; timeoutMs: number }): CloudflareAuthPollOutcome {
  if (input.authorized) return 'authorized'
  if (input.elapsedMs > input.timeoutMs) return 'timeout'
  return 'wait'
}
