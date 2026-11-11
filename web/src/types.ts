// @generated-from main.tsx — 由重构脚本拆分生成，请直接维护本文件。
import {navItems} from './lib';

export type BadgeTone = 'green' | 'blue' | 'cyan' | 'amber' | 'red' | 'purple' | 'slate';

export type Protocol = 'openai_chat' | 'openai_responses' | 'claude' | 'gemini';

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
  roleId?: string;
  roleName?: string;
  displayName?: string;
  avatarUrl?: string;
  scope?: 'own' | 'granted' | 'all';
  /**
   * 当前角色持有的权限点；管理员是 ['*']。后端 /__auth/status 下发，
   * 控制台用它决定菜单与按钮是否可见（见 lib.tsx can()）。
   */
  permissions?: string[];
};

export type ConsoleUser = {
  id: string;
  username: string;
  role: string;
  roleId: string;
  roleName?: string;
  roleScope?: 'own' | 'granted' | 'all';
  displayName?: string;
  avatarUrl?: string;
  /** 仅 scope=granted 时有意义：管理员分配给该用户的 Provider。 */
  grantedProviderIds?: string[];
  enabled: boolean;
  createdAt: string;
  lastLoginAt?: string;
  // 最近活跃：用户浏览器最近一次控制台接口请求时间（内存精确值，
  // 后端最多 5 分钟落库一次）。
  lastActiveAt?: string;
  /** 账号种类：'peer' = 注册到本机的下游网关，不填 = 本地账号。 */
  kind?: 'peer' | string;
  /** kind=peer 才有：这个下游网关的稳定身份（gw_…）。 */
  gatewayId?: string;
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
  cpuLoadPercent?: number;
  cpuCount: number;
  cpuMhz?: number;
  cpuPhysicalCores?: number;
  cpuModel?: string;
  cpuCores?: HostCPUCore[];
  cpuCoresAvailable?: boolean;
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
  memCached?: number;
  memAnon?: number;
  memSlab?: number;
  hostMemHitRate?: number;
  swapTotal?: number;
  swapUsed?: number;
  swapAvailable?: boolean;
  diskTotal?: number;
  diskUsed?: number;
  diskPercent?: number;
  diskAvailable?: boolean;
  diskDevice?: string;
  volumes?: HostDisk[];
  diskIO?: HostDiskIO;
  diskReadRate?: number;
  diskWriteRate?: number;
  diskIOReady?: boolean;
  diskTemps?: HostDiskTemp[];
  fans?: HostFanSpeed[];
  netRxBytes?: number;
  netTxBytes?: number;
  netRxRate?: number;
  netTxRate?: number;
  netRxPacketsRate?: number;
  netTxPacketsRate?: number;
  netRxErrors?: number;
  netTxErrors?: number;
  netDrops?: number;
  netRateReady?: boolean;
  netAvailable?: boolean;
  netInterfaces?: number;
  pressureCpu?: number;
  pressureCpuFull?: number;
  pressureMem?: number;
  pressureMemFull?: number;
  pressureIo?: number;
  pressureIoFull?: number;
  pressureAvailable?: boolean;
  hostname?: string;
  platform?: string;
  osName?: string;
  kernel?: string;
  bootTime?: string;
  uptimeSeconds?: number;
  processUptimeSeconds?: number;
  processStartedAt?: string;
  processRestarts?: number;
  goroutines?: number;
  osThreads?: number;
  processHeapMiB?: number;
  processSysMiB?: number;
  processRssMiB?: number;
  processRssFromRuntime?: boolean;
  processPeakMiB?: number;
  processThreads?: number;
  processFds?: number;
  processCpuPercent?: number;
  dbFileBytes?: number;
  dbFileUpdated?: string;
  cgroupMemLimit?: number;
  cgroupMemUsed?: number;
  sampleCostMs?: number;
  collectedAt?: string;
};

export type HostCPUCore = {
  id: number;
  percent: number;
};

export type HostDisk = {
  mount: string;
  device?: string;
  fstype?: string;
  total: number;
  used: number;
  percent: number;
};

export type HostDiskIO = {
  device: string;
  readRate: number;
  writeRate: number;
  util: number;
  awaitMs: number;
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

export type WorkBuddyOAuthInfo = {
  connected?: boolean;
  expiresAt?: string;
  accountLabel?: string;
  /** 订阅账号 id（X-User-Id），非敏感，用于区分不同账号。 */
  userId?: string;
  /** 账号所属域（X-Domain），非敏感。 */
  domain?: string;
};

export type WorkBuddyUsageWindow = {
  name: string;
  /** 已用百分比 0-100。 */
  used: number;
  amount: number;
  limit: number;
  unit: string;
  /** 团队版周期的重置时刻。 */
  resetsAt?: string;
  /** 还剩的这些积分什么时候作废（个人版按批发，每批一个到期日）。 */
  runsOutAt?: string;
  runsOut?: number;
};

export type WorkBuddyUsage = {
  plan?: string;
  user?: string;
  windows: WorkBuddyUsageWindow[];
  error?: string;
  fetchedAt?: string;
  checkin?: SubscriptionCheckin;
};

/**
 * 任务中心里的一个成长任务（后端 workBuddyTask 的投影）。
 *
 * progressKnown=false 是国际版的列表形态：它根本不报进度，target/current 因此
 * 可以缺省；claimable/claimed 是后端按厂商计数推算的，不是猜的。
 */
export type WorkBuddyTaskItem = {
  taskCode: string;
  title?: string;
  description?: string;
  taskDesc?: string;
  credit: number;
  energy: number;
  hasReward?: boolean;
  taskType?: string;
  tag?: string;
  jumpUrl?: string;
  locked?: boolean;
  progressKnown: boolean;
  target?: number;
  current?: number;
  acceptStatus?: string;
  status?: string;
  claimable?: boolean;
  claimed?: boolean;
  /** 网关知道怎么推进这个任务（有对应动作）。 */
  automatable?: boolean;
  /** 动作说明，做按钮的 tooltip。 */
  action?: string;
  /** 小程序口径专属任务。 */
  mp?: boolean;
};

/** 一个任务的结果：done 只表示服务端确认领奖，其余状态各自说明。 */
export type WorkBuddyTaskResult = {
  ok?: boolean;
  status?: string;
  message?: string;
  progressBefore?: string;
  progressAfter?: string;
  claimable?: boolean;
  claimed?: boolean;
  credit?: number;
  energy?: number;
  claimError?: string;
  attempt?: boolean;
  actionRun?: boolean;
  taskCode?: string;
  desc?: string;
};

export type WorkBuddyRunEvent = {
  kind: 'phase' | 'task_start' | 'result' | string;
  taskCode?: string;
  message?: string;
  result?: WorkBuddyTaskResult;
};

/** 一次执行（异步，按 runId 轮询）。 */
export type WorkBuddyRun = {
  id: string;
  providerId: string;
  running: boolean;
  startedAt: string;
  finishedAt?: string;
  events: WorkBuddyRunEvent[];
  results: WorkBuddyTaskResult[];
  error?: string;
};

/** 每个 Provider 的每日自动化设置（设置表里按 provider 存）。 */
export type WorkBuddyAutomation = {
  dailyTasks: boolean;
  dailyKeepalive: boolean;
  /** 每日执行时刻（北京时间 0-23）。 */
  hour: number;
  lastRunDay?: string;
  lastRunAt?: string;
  lastSummary?: string;
  /** 判定「今天」用的北京日期。 */
  today: string;
};

export type WorkBuddyTaskCenter = {
  supported: boolean;
  connected: boolean;
  /** 国际版：成长中心只读，写入一律被拒。 */
  readOnly: boolean;
  accountKey?: string;
  tasks: WorkBuddyTaskItem[];
  error?: string;
  fetchedAt?: string;
  automation: WorkBuddyAutomation;
  run?: WorkBuddyRun;
};

/** 一次保活的结果：续期 / 活跃上报 / 签到 / 补领。 */
export type WorkBuddyKeepaliveResult = {
  refreshed?: boolean;
  expiresAt?: string;
  activity?: boolean;
  checkin?: string;
  sweepAccepted?: number;
  sweepClaimed?: number;
  message?: string;
  error?: string;
};

export type ZCodeOAuthInfo = {
  connected?: boolean;
  /** 用户点了「暂停」：后端仍存有 ZCode 铸造的项目密钥，只是暂停转发。 */
  disconnected?: boolean;
  expiresAt?: string;
  accountLabel?: string;
  /** 账号所在的站点（zai / bigmodel），非敏感。 */
  site?: string;
  /** 套餐名，如 "GLM Coding Pro"。 */
  plan?: string;
  /** 团队席位所在的项目，非敏感。 */
  organizationId?: string;
  projectId?: string;
};

export type TraeOAuthInfo = {
  connected?: boolean;
  disconnected?: boolean;
  expiresAt?: string;
  accountLabel?: string;
  /** 账号 uid（x-uid），非敏感，用于区分同站点的两个账号。 */
  userId?: string;
  /** 登录时建立的 device id，非敏感；Trae 按设备计数会话。 */
  deviceId?: string;
  /** Trae 国际版的分区（"US" 或空 = 新加坡）。 */
  region?: string;
};

export type SubscriptionUsageWindow = {
  name: string;
  /** 已用百分比 0-100。 */
  used: number;
  amount?: number;
  limit?: number;
  unit?: string;
  resetsAt?: string;
  /** 还剩的这些什么时候作废（WorkBuddy 的积分按批发，每批一个到期日）。 */
  runsOutAt?: string;
  runsOut?: number;
  /** 侧路窗口（不计入套餐上限，如 ZCode 的 MCP 月额度）。 */
  aside?: boolean;
};

export type SubscriptionUsage = {
  plan?: string;
  windows: SubscriptionUsageWindow[];
  /** 账号级时间：Qoder 报套餐有效期到什么时候。 */
  until?: string;
  error?: string;
  fetchedAt?: string;
  checkin?: SubscriptionCheckin;
};

/**
 * 一行时间提示：整句文案、完整时刻的 title、「其实已经过了」，以及拆开的两段
 * ——绝对时刻可以被窄卡片截掉，「还有多久」是更该留下的那半句。
 */
export type TimeHint = {
  text: string;
  title: string;
  passed: boolean;
  /** 「明天 01:45」这一半：绝对时刻，窄卡片先截它。 */
  when?: string;
  /** 「2 小时后」这一半，比绝对时刻更该留下。 */
  relative?: string;
  /** 前缀词（「重置」）。紧凑卡片省掉它，行下的位置已经说明它是重置。 */
  label?: string;
};

/** 一次签到的结果。day 是北京日，网关算好，浏览器不再按时区猜「今天」。 */
export type CheckinRecord = {  day?: string;
  at?: string;
  outcome?: 'claimed' | 'done' | 'ineligible' | 'inactive' | 'failed' | 'own-app';
  credit?: number;
  /** 厂商自己数的连续天数；没给就是 0，前端不编。 */
  streak?: number;
  msg?: string;
};

/** 订阅额度卡片上的签到块，跟着 usage 一起回来。 */
export type SubscriptionCheckin = {
  /** 厂商有没有每日签到活动；没有的订阅整个块都不出现。 */
  supported: boolean;
  enabled: boolean;
  today: string;
  record?: CheckinRecord;
};

export type QoderPATInfo = {
  connected?: boolean;
  /** 这条连接是怎么来的：device = 浏览器授权，pat = 粘贴个人访问令牌。 */
  signInMode?: 'device' | 'pat';
  /** Qoder 的账号接口报出的邮箱；没有它就只能显示 uid 或令牌尾号。 */
  email?: string;
  /** Qoder CN 拒绝换发作业令牌时，账号改用设备令牌直接对话（CLI 的形态）。 */
  deviceChat?: boolean;
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

/**
 * ClaudeOAuthUsageLimit 是用量响应 limits 数组的一项。
 *
 * 官方 App 的「当前会话 / 所有模型 / 仅限 Fable」三条就是它：kind 为 session、
 * weekly_all、weekly_scoped，带 model scope 的那条即「仅限 X」。
 */
export type ClaudeOAuthUsageLimit = {
  kind: string;
  group?: string;
  percent: number;
  resetsAt?: string;
  label?: string;
  allowancePercentOfWeekly?: number;
  active?: boolean;
};

export type ClaudeOAuthUsageReport = {
  available: boolean;
  error?: string;
  fetchedAt?: string;
  five_hour?: ClaudeOAuthUsageBucket;
  seven_day?: ClaudeOAuthUsageBucket;
  seven_day_opus?: ClaudeOAuthUsageBucket;
  seven_day_sonnet?: ClaudeOAuthUsageBucket;
  limits?: ClaudeOAuthUsageLimit[];
  extra_usage?: Record<string, unknown>;
};

export type ZhipuUsageBucket = {
  utilization: number;
  resets_at?: string;
};

export type ZhipuResetCredits = {
  count: number;
  fiveHour?: number;
  weekly?: number;
  earliest?: string;
};

export type ZhipuUsageReport = {
  available: boolean;
  unsupported?: boolean;
  error?: string;
  fetchedAt?: string;
  level?: string;
  five_hour?: ZhipuUsageBucket;
  weekly?: ZhipuUsageBucket;
  /** 重置卡只读展示：使用要去智谱 / Z.ai 官方页面。 */
  resets?: ZhipuResetCredits;
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

export type ChatGPTResetConsumeResult = {
  code: string;
  windowsReset?: number;
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
  // RTK 工具输出压缩开关（管理员，通过 POST /__providers/{id}/rtk 单独切换）：
  // 开启后网关转发前用 rtk pipe 过滤 tool_result 里的命令输出。
  rtkCompress?: boolean;
  // 首包超时（秒，管理员，通过 POST /__providers/{id}/first-packet-timeout 单独保存）：
  // 网关等这台上游返回响应头的最长时间，超时即换下一个 Provider。0/缺省 = 不限制，
  // 且只约束开流前的等待，不会切断已经在输出的回答。
  firstPacketTimeoutSec?: number;
  // 并发上限与排队等待（管理员，通过 POST /__providers/{id}/concurrency 单独保存）：
  // 这台上游同时在飞的最大请求数，0/缺省 = 不限制；排队秒数只在设了上限时有意义。
  maxConcurrentRequests?: number;
  queueWaitSeconds?: number;
  // 每分钟请求预算（管理员，同上接口保存）：滚动一分钟内最多准入的请求数，
  // 0/缺省 = 不限速；可以只限速不限并发，超出返回 429。
  requestsPerMinute?: number;
  // 账号分组（管理员，同上接口保存）：上面三个上限属于哪个账号。留空 = 这行
  // 自己一份额度；几行填同一个名字 = 共用一份额度（同一个订阅的多把密钥）。
  laneGroup?: string;
  // 自助注册（内网穿透场景）：owner/admin 生成一个 Provider 专属令牌后，
  // 用户自己的脚本可用该令牌调用 PATCH /__providers/{id}/self-register
  // 更新 baseUrl/apiKeySource，无需登录控制台。原始令牌只在生成那一刻返回，
  // 这里只有非敏感的展示信息。
  selfRegistration?: {
    tokenPreview?: string;
    createdAt?: string;
    lastSeenAt?: string;
  };
  authType?: 'api_key' | 'claude_oauth' | 'cursor_oauth' | 'chatgpt_oauth' | 'qoder_pat' | 'qoder_cn' | 'workbuddy' | 'workbuddy_ai' | 'zcode' | 'zcode_bigmodel' | 'trae_cn' | 'trae_global' | 'opencode_zen_free' | 'self_register' | 'peer';
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
  workbuddyOAuth?: WorkBuddyOAuthInfo;
  zcodeOAuth?: ZCodeOAuthInfo;
  traeOAuth?: TraeOAuthInfo;
  requestAdapter?: RequestAdapter;
};

export type ProvidersImportResult = {
  created: string[];
  updated: string[];
  skipped: string[];
  errors: string[];
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
  /** 运行期：最近一次真正返回响应的 Provider（「在服务」看它，而不是 activeProviderId）。 */
  servingProviderId?: string;
  /** 运行期：最近一次真正返回响应的 Provider（「在服务」看它，而不是 activeProviderId）。 */
  /** 调度模式：'' / order 按链路顺序，rotate 轮流，least_used 挑近几分钟干活最少的，quota 挑额度剩得最多的，spread 把不同会话分到不同账号（每会话独占一个账号两小时）。会话粘性始终优先。 */
  scheduleMode?: string;
  /** 额度窗口：'' 不限 / 'day' 每天（网关本机时区 0 点重置）/ 'month' 每月 1 号 0 点。 */
  quotaWindow?: string;
  /** 窗口内 token 上限（输入含缓存 + 输出，与用量页同口径）。0 = 不限。 */
  quotaTokens?: number;
  /** 窗口内成本上限，微美元（1e-6 USD），只累计已计价请求。0 = 不限。 */
  quotaCostMicros?: number;
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
  scheduleMode?: string;
};

export type Model = {
  id: string;
  providerId: string;
  /** 上游给用户看的名字；请求仍然只认 id。空表示上游没有别名。 */
  name?: string;
  protocol: Protocol;
  contextLength: number;
  /** 运维给这个模型单独定的上下文窗口；0/缺省 = 按厂商已知值或上游声明值算。
   *  和 contextLength 分开存：后者记录「上游怎么说」，每次目录同步都会重写它。 */
  contextOverride?: number;
  maxOutputTokens?: number;
  inMenu: boolean;
  /** 上游标出的价格：free = 该套餐下不消耗积分；rate = 每次请求的积分倍率；
   *  rateWas = 打折时被划线的原价。三者都是展示用，上游不给价格时全为假值。 */
  free?: boolean;
  rate?: number;
  rateWas?: number;
};

/**
 * 网关自己的成本账本价格行（USD / 百万 Token），与 ModelPriceTags 展示的「上游
 * 积分倍率」是两回事：这里是我们给请求记账用的单价。
 * inputUsdPerMTok 只算未命中缓存的那部分 prompt——cacheTokens/cacheWriteTokens
 * 已经含在 inputTokens 里，重复按全价计会翻倍。
 * modelId "*" 是该范围的默认行；providerId "*" 是全局范围，所有没有自己价格行的
 * Provider 都共用它（官方 Claude 各账号因此只需维护一套价）。没有行的 Provider/模型
 * 一律「未计价」，不是 $0。订阅制（按积分计费）的 Provider 应当留空。
 */
export type ModelUSDPrice = {
  providerId: string;
  modelId: string;
  inputUsdPerMTok: number;
  outputUsdPerMTok: number;
  cacheReadUsdPerMTok: number;
  cacheWriteUsdPerMTok: number;
  updatedAt?: string;
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

// ---- 网关互联（父子网关） ----

/** 注册到本机的一个下游网关。凭据的任何形态都不在这里。 */
export type PeerGateway = {
  userId: string;
  username: string;
  /** 下游的稳定身份（gw_…），重装 app 不变。 */
  gatewayId: string;
  name?: string;
  enabled: boolean;
  createdAt?: string;
  /** 它那把对端凭据的最后使用时间。 */
  lastSeenAt?: string;
  keyId?: string;
  /** 吊销后用户行还在（保留授权），凭据已删，两者分开显示。 */
  keyEnabled: boolean;
  grantedProviderIds: string[];
  /** 最近这段时间它送进来的请求数，用来一眼看出活跃/挂空。 */
  ingressRequests: number;
};

export type PeersResponse = {
  peers: PeerGateway[];
  /** 本机身份：下游注册时用的就是它。 */
  gatewayId: string;
  maxHops: number;
  registrationAllowCidrs: string;
  registrationAllowsPrivate: boolean;
};

/** 本机注册到的一个上游网关。它在本机就是一条 authType=peer 的 provider。 */
export type PeerUpstream = {
  providerId: string;
  name: string;
  parentUrl: string;
  protocol: Protocol;
  enabled: boolean;
  healthStatus?: string;
  /** 上游授权、且与本条协议对得上的模型。 */
  models: string[];
  /**
   * 上游授权给本机的 provider 个数（不限协议）。它和 models 是两个口径：
   * 一条链路只说一种协议，所以协议对不上的授权不会出现在 models 里。
   * grantsSynced 为 false 时这个数是零值，不代表"授权了 0 个"。
   */
  grantCount: number;
  /** grantCount 是不是真的问到过上游。false = 还没同步过，别说"0 个"。 */
  grantsSynced: boolean;
  /** 授权快照的取得时间（RFC3339）；从未同步过时为空。 */
  grantsSyncedAt?: string;
  /** 上游为这台机器建的账号名，对得上上游用户管理页那一行。 */
  peerUsername?: string;
  /** 上游网关自己的身份。 */
  parentGatewayId?: string;
};

export type PeerUpstreamsResponse = {
  upstreams: PeerUpstream[];
  /** 本机身份；界面把它显示出来，运维才能核对"上游那一行是不是我"。 */
  gatewayId: string;
  protocols: Protocol[];
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
  /** 网关进程的本地时区名（如 Asia/Shanghai）；「今日」与日界按它算，和密钥的今日计数同一天。 */
  serverTimeZone?: string;
  /** When true, successful (2xx) requests also persist their bodies. */
  log2xxBodies?: boolean;
  /** Async usage-stat batching thresholds (0 = backend defaults). */
  usageBatchMaxSize?: number;
  usageBatchMaxWaitSeconds?: number;
  /** When true, HTTP binds 0.0.0.0 (LAN / tunnel). When false, loopback only. */
  webExposed?: boolean;
  dataPaths?: DataPaths;
  cursorBridge?: CursorBridgeRuntime;
  /** 本网关的稳定身份；重装不变。父子对接靠它互相识别。 */
  gatewayIdentity?: GatewayIdentity;
};

/**
 * 本网关身份。ID 从机器标识派生，因此重装、清库都不变——下游注册记录
 * 不会因为重装而全部失效。
 */
export type GatewayIdentity = {
  id: string;
  name?: string;
  /** machine = 从机器标识派生；persisted = 库里存的随机值（读不到机器标识时）。 */
  source?: string;
  /** false 时重装会换身份，界面必须提示。 */
  machineIdReadable: boolean;
};

/** 一次请求碰到过的每个 Provider：谁在服务、谁被跳过、为什么换下一个。 */
export type ScheduleAttempt = {
  providerId: string;
  model?: string;
  status?: number;
  reason?: string;
  error?: string;
  ms?: number;
  /** 回答被丢弃、继续下一个候选 */
  deferred?: boolean;
  /** 根本没发出去（未授权 / 未锁定模型 / Provider 已删除等） */
  skipped?: boolean;
};

export type RequestAnalysis = {
  sessionId?: string; stream: boolean; captured: boolean; messages: number; tools: number; toolResults: number;
  systemBytes: number; messageBytes: number; toolBytes: number;
  // 调度轨迹（早于该功能的日志没有这些字段）
  scheduleMode?: string;
  /** 这次是按哪条链排的 */
  candidates?: string[];
  /** 会话粘滞命中的 Provider，空表示这次没有粘滞 */
  sessionPinned?: string;
  attempts?: ScheduleAttempt[];
};
export type UsageAnalysisResponse = { items: LogEntry[]; total: number; from: string; to: string; retentionDays: number };

export type LogEntry = {
  analysis?: RequestAnalysis;
  id?: number;
  time: string;
  apiKeyId?: string;
  apiKeyName?: string;
  userName?: string;
  routeId: string;
  providerId: string;
  model: string;
  /** 上游实际收到的推理设置：medium / adaptive / enabled·8000，未设置为空。 */
  reasoningEffort?: string;
  action: string;
  protocolFlow: string;
  path: string;
  status: number;
  inputTokens: number;
  outputTokens: number;
  cacheTokens?: number;
  /** 缓存写入（cache_creation），已含在 inputTokens 内，单独展示因为单价不同。 */
  cacheWriteTokens?: number;
  /** 该请求按网关价格表算出的成本，单位微美元（1e-6 USD）。 */
  costMicros?: number;
  /** true 表示当时存在价格行；缺省即未计价，绝不能当成 $0.00 的免费模型。 */
  priced?: boolean;
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
  // 父子网关透传的链路标记。三者都空 = 一次纯本地请求（和引入父子功能之前
  // 完全一样），界面据此决定要不要显示链路。
  /** 把这次请求送进来的下游网关 ID（空 = 客户端直连本机）。 */
  ingress?: string;
  /** 本机把这次请求转发出去的上游网关 ID（空 = 本机直连 provider）。 */
  egress?: string;
  /** 这次透传要对账的对端网关 ID：有 ingress 取 ingress，否则取 egress。 */
  peerGatewayId?: string;
  peerDirection?: 'ingress' | 'egress' | string;
  /** 三级链路里真正的起点网关（X-Gateway-Origin 一路带过来的）。 */
  peerOrigin?: string;
  /** 请求已经过的网关 ID 链（发起方开始、不含本机）。三级及以上排障用。 */
  peerPath?: string;
  errorDescription?: string;
  requestBody?: string;
  responseBody?: string;
};

export type LogPage = {
  items: LogEntry[];
  total: number;
  page: number;
};

export type APIKeyDayStats = {
  apiKeyId: string;
  apiKeyName: string;
  requestCount: number;
  inputTokens: number;
  outputTokens: number;
  cacheTokens: number;
  /** 缓存写入 token（已含在 inputTokens 内，单价不同）。旧数据缺省。 */
  cacheWriteTokens?: number;
  /** 按网关价格表算出的成本（微美元）。旧数据与未计价一律缺省，不显示成 0。 */
  costMicros?: number;
  /** 有价格行的请求数：成本除以它才是覆盖率，光看成本会把未计价当成便宜。 */
  pricedCount?: number;
};

export type ProviderDayStats = {
  providerId: string;
  requestCount: number;
  inputTokens: number;
  outputTokens: number;
  cacheTokens: number;
  /** 缓存写入 token（已含在 inputTokens 内，单价不同）。旧数据缺省。 */
  cacheWriteTokens?: number;
  /** 按网关价格表算出的成本（微美元）。旧数据与未计价一律缺省，不显示成 0。 */
  costMicros?: number;
  /** 有价格行的请求数：成本除以它才是覆盖率，光看成本会把未计价当成便宜。 */
  pricedCount?: number;
};

export type ModelDayStats = {
  model: string;
  requestCount: number;
  inputTokens: number;
  outputTokens: number;
  cacheTokens: number;
  /** 缓存写入 token（已含在 inputTokens 内，单价不同）。旧数据缺省。 */
  cacheWriteTokens?: number;
  /** 按网关价格表算出的成本（微美元）。旧数据与未计价一律缺省，不显示成 0。 */
  costMicros?: number;
  /** 有价格行的请求数：成本除以它才是覆盖率，光看成本会把未计价当成便宜。 */
  pricedCount?: number;
};

export type ProtocolDayStats = {
  protocol: string;
  requestCount: number;
  inputTokens: number;
  outputTokens: number;
  cacheTokens: number;
  /** 缓存写入 token（已含在 inputTokens 内，单价不同）。旧数据缺省。 */
  cacheWriteTokens?: number;
  /** 按网关价格表算出的成本（微美元）。旧数据与未计价一律缺省，不显示成 0。 */
  costMicros?: number;
  /** 有价格行的请求数：成本除以它才是覆盖率，光看成本会把未计价当成便宜。 */
  pricedCount?: number;
};

export type UserDayStats = {
  userId: string;
  userName: string;
  requestCount: number;
  inputTokens: number;
  outputTokens: number;
  cacheTokens: number;
  /** 缓存写入 token（已含在 inputTokens 内，单价不同）。旧数据缺省。 */
  cacheWriteTokens?: number;
  /** 按网关价格表算出的成本（微美元）。旧数据与未计价一律缺省，不显示成 0。 */
  costMicros?: number;
  /** 有价格行的请求数：成本除以它才是覆盖率，光看成本会把未计价当成便宜。 */
  pricedCount?: number;
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
  /** 缓存写入 token（已含在 inputTokens 内，单价不同）。旧数据缺省。 */
  cacheWriteTokens?: number;
  /** 按网关价格表算出的成本（微美元）。旧数据与未计价一律缺省，不显示成 0。 */
  costMicros?: number;
  /** 有价格行的请求数：成本除以它才是覆盖率，光看成本会把未计价当成便宜。 */
  pricedCount?: number;
};

export type StatusBucketStats = {
  class: string;
  requestCount: number;
};

export type HourlyBucketStats = {
  hour: number;
  requestCount: number;
  inputTokens: number;
  outputTokens: number;
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
  hourly?: HourlyBucketStats[];
};

export type RTKStatsDailyPoint = {
  date: string;
  commands: number;
  originalTokens: number;
  compressedTokens: number;
  savedTokens: number;
};

export type RTKProviderStats = {
  providerId: string;
  commands: number;
  originalTokens: number;
  compressedTokens: number;
  savedTokens: number;
  savedPercent: number;
};

/** 网关侧 RTK 压缩节省统计（按天/按 Provider 聚合，Token 为文本估算值）。 */
export type RTKStatsResponse = {
  available: boolean;
  version?: string;
  days: number;
  from?: string;
  commands: number;
  originalBytes: number;
  compressedBytes: number;
  originalTokens: number;
  compressedTokens: number;
  savedTokens: number;
  savedPercent: number;
  daily: RTKStatsDailyPoint[];
  byProvider?: RTKProviderStats[];
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

export type RolePermissionMeta = { key: string; label: string };

export type RolePermissionModule = {
  id: string;
  label: string;
  description: string;
  permissions: RolePermissionMeta[];
};

export type RoleScope = 'own' | 'granted' | 'all';

export type Role = {
  id: string;
  name: string;
  description?: string;
  builtin: boolean;
  scope: RoleScope;
  permissions: string[];
  createdAt?: string;
  updatedAt?: string;
  /** /__roles 列表里带的在用人数。 */
  userCount?: number;
};

export type RoleCatalog = {
  modules: RolePermissionModule[];
  scopes: Array<{ id: RoleScope; label: string; description: string }>;
};

export type GrantTarget = {
  id: string;
  name: string;
  ownerId: string;
  ownerName: string;
  protocol: string;
  disabled: boolean;
};

export type TrafficRankCache = {
  providers: Record<string, number>;
  models: Record<string, number>;
};

export type ProviderConnectKind = 'api_key' | 'self_register' | 'peer' | 'claude_oauth' | 'cursor_oauth' | 'chatgpt_oauth' | 'qoder_pat' | 'qoder_cn' | 'workbuddy' | 'workbuddy_ai' | 'zcode' | 'zcode_bigmodel' | 'trae_cn' | 'trae_global' | 'opencode_zen_free';

/** 需要浏览器登录的订阅连接方式：它们共用一套「发起 → 等待 → 已连接」流程。 */
export type SubscriptionConnectKind = 'workbuddy' | 'workbuddy_ai' | 'zcode' | 'zcode_bigmodel' | 'trae_cn' | 'trae_global';

/** 表单里可选的连接方式：与 ProviderConnectKind 同一集合，命名上区分提交值与展示分类。 */
export type ProviderAuthType = ProviderConnectKind;

/** 添加 Provider 目录：一个可点选的接入预设（订阅登录、厂商、中转、本机服务）。 */
export type ProviderPresetSection = 'subscription' | 'vendor' | 'relay' | 'local';

export type ProviderPreset = {
  id: string;
  name: string;
  section: ProviderPresetSection;
  /** 点选后写入草稿的连接方式；厂商/中转都是 api_key。 */
  authType: ProviderConnectKind;
  protocol?: Protocol;
  baseUrl?: string;
  /** 厂商控制台/取密钥入口，sheet 里作为次级信息展示。 */
  keysUrl?: string;
  website?: string;
  note?: string;
  /** 订阅类预设需要浏览器登录，草稿保存后才能开始。 */
  needsSignIn?: boolean;
  /**
   * 图标基名（web/public/icons/<icon>.svg）。以 -color 结尾的是品牌彩色版，按
   * <img> 渲染；否则是 currentColor 单色版，必须用 CSS mask 才能跟着主题着色。
   */
  icon?: string;
};

export type LegacyRequestStatsSnapshot = {
  date: string;
  total: APIKeyDayStats;
  lastRequest?: LogEntry;
  byApiKey: APIKeyDayStats[];
};

export type CloudflareZoneOption = { id: string; name: string };

export type SearchableModelOption = { id: string; label: string };

// icon 指向 web/public/icons 下的品牌图标名，筛选下拉里也能一眼认出服务商。
export type MultiFilterOption = { id: string; label: string; icon?: string };

export type ModalLayer = {
  id: number;
  onClose: () => void;
};

// 额度保活（quota keeper）：定时对选中的 Provider 发一次极短的对话请求，把
// 上游那扇「第一次请求开始计时、5 小时后重置」的窗口钉在用户挑的小时上。
export type ScheduleMode = 'daily_times' | 'every_hours' | 'every_minutes';

export type ScheduledTask = {
  id: string;
  name: string;
  enabled: boolean;
  ownerUserId?: string;
  mode: ScheduleMode;
  /** "HH:MM" 按这个 IANA 时区解释；空 = 网关所在时区（旧任务的默认）。 */
  timeZone?: string;
  /** daily_times 用，"HH:MM" 列表。 */
  times?: string[];
  /** 滚动策略用：every_hours 是小时，every_minutes 是分钟。 */
  interval: number;
  /** 滚动策略的起点，留空 = 任务创建时间。 */
  anchorAt?: string;
  providerIds: string[];
  /** 按 provider 固定探测模型，留空用 provider 自己的默认模型。 */
  modelOverrides?: Record<string, string>;
  /** 静默窗口分钟数；负数 = 不做静默判断，每次都发。 */
  quietMinutes: number;
  probePrompt?: string;
  lastRunAt?: string;
  nextRunAt?: string;
  createdAt?: string;
};

export type ScheduledTaskRun = {
  id: number;
  taskId: string;
  providerId: string;
  batchId: string;
  firedAt: string;
  outcome: 'fired' | 'skipped' | 'failed';
  status?: number;
  latencyMs?: number;
  model?: string;
  reason?: string;
};

export type QuotaPreviewFire = { at: string; daily: string; clock: string };

export type QuotaTaskPage = {
  tasks: ScheduledTask[];
  recentRuns: ScheduledTaskRun[];
};
