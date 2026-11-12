import type {Provider, ProviderConnectKind, ProviderPreset, ProviderPresetSection} from './types';
import {presetMatchesProvider, providerConnectLabel} from './lib';

// 添加 Provider 的可选目录：点一个磁贴即填好连接方式、协议与接入地址，
// 省掉手抄 URL。端点取自上游厂商公开文档（与参考项目 magpie 的预设表一致），
// 全部可在表单里改：厂商换域名时应改配置而不是改代码。
//
// icon 指向 web/public/icons/<name>.svg，同样取自 magpie 打包的品牌图标（各厂商
// 自有商标资产）：-color 结尾的是彩色版，其余是 currentColor 单色版。
//
// 本项目上游只实现 OpenAI Chat / OpenAI Responses / Claude 三种协议，所以这里
// 一律给该厂商最常用的一种；同厂商的其它协议端点写在 note 里，需要时改协议即可。

export const PROVIDER_PRESETS: ProviderPreset[] = [
  // ---- 订阅登录：无需密钥，浏览器登录后由网关持有账号令牌 ----
  { id: 'sub-claude', name: 'Claude 订阅账号', section: 'subscription', authType: 'claude_oauth', protocol: 'claude', baseUrl: 'https://api.anthropic.com', needsSignIn: true, note: 'Anthropic 订阅 OAuth', icon: 'claude-color' },
  { id: 'sub-cursor', name: 'Cursor 订阅账号', section: 'subscription', authType: 'cursor_oauth', protocol: 'openai_chat', needsSignIn: true, note: '经本地 gRPC Bridge 转发', icon: 'cursor' },
  { id: 'sub-chatgpt', name: 'ChatGPT / Codex 订阅账号', section: 'subscription', authType: 'chatgpt_oauth', protocol: 'openai_responses', baseUrl: 'https://chatgpt.com/backend-api/codex/responses', needsSignIn: true, note: 'Codex CLI 上游，Responses 协议', icon: 'codex-color' },
  { id: 'sub-qoder', name: 'Qoder 账号', section: 'subscription', authType: 'qoder_pat', protocol: 'openai_chat', baseUrl: 'https://api3.qoder.sh', needsSignIn: true, website: 'https://qoder.com', note: '浏览器授权登录（qoder.com 账号）· 无浏览器时可粘贴个人访问令牌', keysUrl: 'https://qoder.com/account/integrations', icon: 'qoder' },
  { id: 'sub-qoder-cn', name: 'Qoder CN 账号', section: 'subscription', authType: 'qoder_cn', protocol: 'openai_chat', baseUrl: 'https://gateway.qoder.com.cn', needsSignIn: true, website: 'https://qoder.cn', note: '同一套协议的国内站点 · 账号用阿里云或手机号，与 qoder.com 不通用', keysUrl: 'https://qoder.cn/account/integrations', icon: 'qoder' },
  { id: 'sub-workbuddy', name: 'WorkBuddy 订阅', section: 'subscription', authType: 'workbuddy', protocol: 'openai_chat', baseUrl: 'https://copilot.tencent.com/v2', needsSignIn: true, website: 'https://www.codebuddy.cn', note: '腾讯 CodeBuddy 套餐 · 国内版', icon: 'workbuddy-color' },
  { id: 'sub-workbuddy-ai', name: 'WorkBuddy AI 订阅', section: 'subscription', authType: 'workbuddy_ai', protocol: 'openai_chat', baseUrl: 'https://www.workbuddy.ai/v2', needsSignIn: true, website: 'https://www.workbuddy.ai', note: '同一套 API 的国际版构建', icon: 'workbuddy-color' },

  { id: 'sub-zcode', name: 'ZCode 编程套餐（Z.ai）', section: 'subscription', authType: 'zcode', protocol: 'claude', baseUrl: 'https://api.z.ai/api/anthropic', needsSignIn: true, website: 'https://zcode.z.ai', note: 'GLM Coding Plan · 登录铸造项目密钥', icon: 'zcode.png' },
  { id: 'sub-zcode-bigmodel', name: 'ZCode 编程套餐（智谱）', section: 'subscription', authType: 'zcode_bigmodel', protocol: 'claude', baseUrl: 'https://open.bigmodel.cn/api/anthropic', needsSignIn: true, website: 'https://zcode.z.ai', note: '同一套登录，BigModel 站点的套餐与端点', icon: 'zcode.png' },
  { id: 'sub-trae-cn', name: 'Trae CN 订阅', section: 'subscription', authType: 'trae_cn', protocol: 'openai_chat', baseUrl: 'https://trae-api-cn.mchost.guru', needsSignIn: true, website: 'https://www.trae.cn', note: '字节 Trae IDE 协议 · 含免费档', icon: 'trae' },
  { id: 'sub-trae-global', name: 'Trae 国际版订阅', section: 'subscription', authType: 'trae_global', protocol: 'openai_chat', baseUrl: 'https://coresg-normal.trae.ai', needsSignIn: true, website: 'https://www.trae.ai', note: '同一套协议的国际部署（SG/US 随账号）', icon: 'trae' },
  { id: 'sub-opencode-zen-free', name: 'OpenCode Zen 免费档', section: 'subscription', authType: 'opencode_zen_free', protocol: 'openai_chat', baseUrl: 'https://opencode.ai/zen/v1', website: 'https://opencode.ai/docs/zen', note: '免费模型无需密钥；填入自己的密钥可同卡访问付费模型', icon: 'opencode' },

  // ---- 厂商：自己的模型与密钥 ----
  { id: 'anthropic', name: 'Anthropic', section: 'vendor', authType: 'api_key', protocol: 'claude', baseUrl: 'https://api.anthropic.com', website: 'https://console.anthropic.com', keysUrl: 'https://console.anthropic.com/settings/keys', icon: 'anthropic' },
  { id: 'openai', name: 'OpenAI', section: 'vendor', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://api.openai.com/v1', website: 'https://platform.openai.com', keysUrl: 'https://platform.openai.com/api-keys', note: 'Responses 协议同端点', icon: 'openai' },
  { id: 'google', name: 'Google Gemini', section: 'vendor', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://generativelanguage.googleapis.com/v1beta/openai', website: 'https://aistudio.google.com', keysUrl: 'https://aistudio.google.com/apikey', note: 'Gemini Developer API', icon: 'google-color' },
  { id: 'deepseek', name: 'DeepSeek', section: 'vendor', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://api.deepseek.com/v1', website: 'https://platform.deepseek.com', keysUrl: 'https://platform.deepseek.com/api_keys', note: 'Claude 端点：/anthropic', icon: 'deepseek-color' },
  { id: 'xai', name: 'xAI', section: 'vendor', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://api.x.ai/v1', website: 'https://console.x.ai', keysUrl: 'https://console.x.ai', icon: 'xai' },
  { id: 'moonshot', name: 'Kimi', section: 'vendor', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://api.moonshot.ai/v1', website: 'https://platform.moonshot.ai', keysUrl: 'https://platform.moonshot.ai/console/api-keys', note: 'Claude 端点：/anthropic', icon: 'moonshot' },
  { id: 'moonshot-cn', name: 'Kimi（国内）', section: 'vendor', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://api.moonshot.cn/v1', website: 'https://platform.moonshot.cn', keysUrl: 'https://platform.moonshot.cn/console/api-keys', icon: 'moonshot' },
  { id: 'kimi-code', name: 'Kimi Code 会员', section: 'vendor', authType: 'api_key', protocol: 'claude', baseUrl: 'https://api.kimi.ai/coding', website: 'https://www.kimi.com/code', keysUrl: 'https://www.kimi.com/code/console', note: 'Chat 端点：/coding/v1', icon: 'kimi' },
  { id: 'kimi-code-cn', name: 'Kimi Code 会员（国内）', section: 'vendor', authType: 'api_key', protocol: 'claude', baseUrl: 'https://api.kimi.com/coding', website: 'https://www.kimi.com/code', keysUrl: 'https://www.kimi.com/code/console', note: 'Chat 端点：/coding/v1', icon: 'kimi' },
  { id: 'zhipu', name: '智谱 GLM', section: 'vendor', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://open.bigmodel.cn/api/paas/v4', website: 'https://open.bigmodel.cn', keysUrl: 'https://open.bigmodel.cn/usercenter/proj-mgmt/apikeys', note: '编程套餐端点：/api/coding/paas/v4；额度可在卡片里查', icon: 'zhipu-color' },
  { id: 'zai', name: 'Z.ai（GLM 国际）', section: 'vendor', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://api.z.ai/api/paas/v4', website: 'https://z.ai', keysUrl: 'https://z.ai/manage-apikey/apikey-list', note: '编程套餐端点：/api/coding/paas/v4', icon: 'zai' },
  { id: 'minimax', name: 'MiniMax', section: 'vendor', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://api.minimax.io/v1', website: 'https://platform.minimax.io', note: 'Claude 端点：/anthropic', icon: 'minimax-color' },
  { id: 'minimax-cn', name: 'MiniMax（国内）', section: 'vendor', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://api.minimaxi.com/v1', website: 'https://platform.minimaxi.com', note: 'Claude 端点：/anthropic', icon: 'minimax-color' },
  { id: 'stepfun', name: '阶跃星辰 StepFun', section: 'vendor', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://api.stepfun.ai/v1', website: 'https://platform.stepfun.ai', note: 'Step Plan 端点：/step_plan/v1', icon: 'stepfun-color' },
  { id: 'stepfun-cn', name: '阶跃星辰（国内）', section: 'vendor', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://api.stepfun.com/v1', website: 'https://platform.stepfun.com', note: 'Step Plan 端点：/step_plan/v1', icon: 'stepfun-color' },
  { id: 'qwen', name: '通义千问 Qwen', section: 'vendor', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://dashscope-intl.aliyuncs.com/compatible-mode/v1', website: 'https://modelstudio.console.alibabacloud.com', note: 'Claude 端点：/apps/anthropic', icon: 'qwen-color' },
  { id: 'qwen-cn', name: '通义千问（国内）', section: 'vendor', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://dashscope.aliyuncs.com/compatible-mode/v1', website: 'https://bailian.console.aliyun.com', note: 'Claude 端点：/apps/anthropic', icon: 'qwen-color' },
  { id: 'volcengine', name: '火山方舟 Ark', section: 'vendor', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://ark.cn-beijing.volces.com/api/coding/v3', website: 'https://www.volcengine.com/docs/82379/1925114', note: 'Coding / Agent Plan 端点；按量付费为 /api/v3', icon: 'volcengine-color' },
  { id: 'tencent-cloud', name: '腾讯云 TokenHub', section: 'vendor', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://api.lkeap.cloud.tencent.com/plan/v3', website: 'https://cloud.tencent.com/document/product/1823/130060', note: 'Claude 端点：/plan/anthropic', icon: 'tencentcloud-color' },
  { id: 'huaweicloud', name: '华为云 MaaS', section: 'vendor', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://api.modelarts-maas.com/plan/v2', website: 'https://support.huaweicloud.com/Token-plan-maas/tokenplan-maas-0001.html', icon: 'huaweicloud-color' },
  { id: 'baidu-qianfan', name: '百度千帆', section: 'vendor', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://qianfan.baidubce.com/v2', website: 'https://cloud.baidu.com/doc/qianfan/s/Dmrabu8b6', note: 'Token Plan 端点：/v2/tokenplan/personal', icon: 'baiducloud-color' },
  { id: 'mistral', name: 'Mistral', section: 'vendor', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://api.mistral.ai/v1', website: 'https://console.mistral.ai', keysUrl: 'https://console.mistral.ai/api-keys', icon: 'mistral-color' },
  { id: 'groq', name: 'Groq', section: 'vendor', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://api.groq.com/openai/v1', website: 'https://console.groq.com', keysUrl: 'https://console.groq.com/keys', icon: 'groq' },
  { id: 'nous', name: 'Nous Portal', section: 'vendor', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://inference-api.nousresearch.com/v1', website: 'https://portal.nousresearch.com', icon: 'nousresearch' },

  // ---- 中转：一把密钥访问多家模型 ----
  { id: 'openrouter', name: 'OpenRouter', section: 'relay', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://openrouter.ai/api/v1', website: 'https://openrouter.ai', note: 'Claude 端点：/api', icon: 'openrouter' },
  { id: 'aihubmix', name: 'AiHubMix', section: 'relay', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://aihubmix.com/v1', website: 'https://aihubmix.com', keysUrl: 'https://console.aihubmix.com/token', icon: 'aihubmix-color' },
  { id: '302ai', name: '302.AI', section: 'relay', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://api.302.ai/v1', website: 'https://302.ai', keysUrl: 'https://302.ai/api-keys/list', icon: 'ai302-color' },
  { id: 'siliconflow', name: '硅基流动 SiliconFlow', section: 'relay', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://api.siliconflow.cn/v1', website: 'https://cloud.siliconflow.cn', keysUrl: 'https://cloud.siliconflow.cn/account/ak', icon: 'siliconcloud-color' },
  { id: 'together', name: 'Together AI', section: 'relay', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://api.together.xyz/v1', website: 'https://api.together.ai', icon: 'together-color' },
  { id: 'fireworks', name: 'Fireworks', section: 'relay', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://api.fireworks.ai/inference/v1', website: 'https://fireworks.ai', icon: 'fireworks-color' },
  { id: 'nvidia', name: 'NVIDIA NIM', section: 'relay', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://integrate.api.nvidia.com/v1', website: 'https://build.nvidia.com', icon: 'nvidia-color' },
  { id: 'modelscope', name: '魔搭 API-Inference', section: 'relay', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://api-inference.modelscope.cn/v1', website: 'https://modelscope.cn/docs/model-service/API-Inference/intro', icon: 'modelscope-color' },
  { id: 'cherryin', name: 'CherryIN', section: 'relay', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://open.cherryin.ai/v1', website: 'https://open.cherryin.ai', icon: 'cherryin-color' },
  { id: 'yylx', name: '鱼鱼连线', section: 'relay', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://app.yylx.io/v1', website: 'https://yylx.io', icon: 'yylx' },
  { id: 'commandcode', name: 'Command Code', section: 'relay', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://api.commandcode.ai/provider/v1', website: 'https://commandcode.ai/docs/provider', icon: 'commandcode' },
  { id: 'opencode-zen', name: 'OpenCode Zen', section: 'relay', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://opencode.ai/zen/v1', website: 'https://opencode.ai/zen', note: '部分模型无需密钥', icon: 'opencode' },
  { id: 'opencode-go', name: 'OpenCode Go', section: 'relay', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://opencode.ai/zen/go/v1', website: 'https://opencode.ai/docs/go', icon: 'opencode', headers: {'x-opencode-session': 'lpg-gateway-session'}, note: '需填密钥；卡片展示 5 小时 / 周 / 月额度，并自动带上 x-opencode-session 头' },
  { id: 'kilo', name: 'Kilo Gateway', section: 'relay', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://api.kilo.ai/api/openrouter', note: '部分模型无需密钥', icon: 'kilo' },
  { id: 'clinepass', name: 'ClinePass', section: 'relay', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://api.cline.bot/api/v1', website: 'https://docs.cline.bot/getting-started/clinepass', icon: 'cline' },
  { id: 'pipellm', name: 'PipeLLM', section: 'relay', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://api.pipellm.ai/openai/v1', website: 'https://www.pipellm.ai', icon: 'pipellm-color' },

  // ---- 本机 / 自建服务 ----
  { id: 'ollama', name: 'Ollama（本机）', section: 'local', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'http://localhost:11434/v1', website: 'https://ollama.com', note: '本机模型，通常无需密钥', icon: 'ollama' },
  { id: 'lmstudio', name: 'LM Studio（本机）', section: 'local', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'http://localhost:1234/v1', website: 'https://lmstudio.ai', note: '默认端口 1234', icon: 'lmstudio' },
  { id: 'custom', name: '自定义端点', section: 'local', authType: 'api_key', protocol: 'openai_chat', baseUrl: 'https://example.com/v1/chat/completions', note: '任何 OpenAI / Claude 兼容地址' },
  { id: 'self-register', name: '内网穿透自助注册', section: 'local', authType: 'self_register', note: '由你的脚本用专属令牌声明地址与协议' },
];

const presetById = new Map(PROVIDER_PRESETS.map((preset) => [preset.id, preset]));

export function findProviderPreset(id: string): ProviderPreset | undefined {
  return presetById.get(id);
}

/**
 * 给一个已存在的 Provider 找品牌图标：目录里第一个匹配上的预设就是它的厂商。
 * 匹配不中（自建反代、换过域名的厂商）返回空，由 BrandIcon 退回名称首字。
 */
export function resolveProviderIcon(provider: Provider): string {
  // 网关互联成员继承父端卡片图标；否则 peer provider 的 baseUrl 是父网关，
  // 按主机名永远匹配不到真正的 WorkBuddy / Claude / Qoder 品牌。
  const inheritedVendor = (provider.peerGrants?.card?.vendor || '').trim().toLowerCase();
  if (inheritedVendor) {
    const inheritedIcon = PROVIDER_ICON_BY_VENDOR[inheritedVendor];
    if (inheritedIcon) return inheritedIcon;
  }
  const preset = PROVIDER_PRESETS.find((item) => item.icon && presetMatchesProvider(item, provider));
  return preset?.icon || '';
}

const PROVIDER_ICON_BY_VENDOR: Record<string, string> = {
  anthropic: 'anthropic',
  chatgpt: 'codex-color',
  claude: 'claude-color',
  cursor: 'cursor',
  deepseek: 'deepseek-color',
  minimax: 'minimax-color',
  moonshot: 'moonshot',
  opencode: 'opencode',
  qoder: 'qoder',
  qwen: 'qwen-color',
  stepfun: 'stepfun-color',
  trae: 'trae',
  volcengine: 'volcengine-color',
  workbuddy: 'workbuddy-color',
  zai: 'zai',
  zhipu: 'zhipu-color',
  zcode: 'zcode.png',
};

/**
 * 连接方式（订阅账号类）的品牌图标：从订阅预设里反查，和目录弹窗、Provider 卡
 * 共用同一张表，避免"同一个厂商在三个地方是三个图标"。
 * API Key 与自助注册没有厂商可指（前者是任意端点），返回空即纯文字胶囊。
 * peer 同理：它指向的是另一台本网关，不是某个服务商。
 */
export function providerConnectIcon(kind: '' | ProviderConnectKind): string {
  if (kind === '' || kind === 'api_key' || kind === 'self_register' || kind === 'peer') return '';
  return PROVIDER_PRESETS.find((preset) => preset.section === 'subscription' && preset.authType === kind)?.icon || '';
}

// 连接方式下拉用：标签 + 品牌图标成对给出，图标表与目录磁贴共用同一张表，
// 避免同一个厂商在目录里是一个图标、在编辑弹窗里又是空白。
export type ProviderConnectChoice = {value: ProviderConnectKind; label: string; icon: string};

export type ProviderPresetGroup = {
  id: ProviderPresetSection | 'all';
  title: string;
  desc: string;
  presets: ProviderPreset[];
};

export const PROVIDER_CONNECT_CHOICES: ProviderConnectChoice[] = [
  {value: 'api_key', label: providerConnectLabel('api_key'), icon: 'openai'},
  {value: 'claude_oauth', label: providerConnectLabel('claude_oauth'), icon: providerConnectIcon('claude_oauth')},
  {value: 'cursor_oauth', label: providerConnectLabel('cursor_oauth'), icon: providerConnectIcon('cursor_oauth')},
  {value: 'chatgpt_oauth', label: providerConnectLabel('chatgpt_oauth'), icon: providerConnectIcon('chatgpt_oauth')},
  {value: 'qoder_pat', label: providerConnectLabel('qoder_pat'), icon: providerConnectIcon('qoder_pat')},
  {value: 'qoder_cn', label: providerConnectLabel('qoder_cn'), icon: providerConnectIcon('qoder_cn')},
  {value: 'workbuddy', label: providerConnectLabel('workbuddy'), icon: providerConnectIcon('workbuddy')},
  {value: 'workbuddy_ai', label: providerConnectLabel('workbuddy_ai'), icon: providerConnectIcon('workbuddy_ai')},
  {value: 'zcode', label: providerConnectLabel('zcode'), icon: providerConnectIcon('zcode')},
  {value: 'zcode_bigmodel', label: providerConnectLabel('zcode_bigmodel'), icon: providerConnectIcon('zcode_bigmodel')},
  {value: 'trae_cn', label: providerConnectLabel('trae_cn'), icon: providerConnectIcon('trae_cn')},
  {value: 'trae_global', label: providerConnectLabel('trae_global'), icon: providerConnectIcon('trae_global')},
  {value: 'opencode_zen_free', label: providerConnectLabel('opencode_zen_free'), icon: providerConnectIcon('opencode_zen_free')},
  {value: 'self_register', label: providerConnectLabel('self_register'), icon: 'yylx'},
];

const SECTION_META: Array<{ id: ProviderPresetSection; title: string; desc: string }> = [
  { id: 'subscription', title: '订阅账号', desc: '登录一次即可，无需粘贴密钥' },
  { id: 'vendor', title: '模型厂商', desc: '厂商自己的 API 与密钥' },
  { id: 'relay', title: '中转服务', desc: '一把密钥访问多家模型' },
  { id: 'local', title: '本机与自定义', desc: '本地推理服务、任意兼容端点、脚本自注册' },
];

/** 目录分区 + 关键字过滤（名称 / id / 端点主机 / 备注），保持分区顺序。 */
export function groupProviderPresets(query: string): ProviderPresetGroup[] {
  const needle = query.trim().toLowerCase();
  const matched = needle
    ? PROVIDER_PRESETS.filter((preset) => [preset.name, preset.id, preset.baseUrl, preset.note, preset.website, preset.keysUrl]
        .some((field) => (field || '').toLowerCase().includes(needle)))
    : PROVIDER_PRESETS;
  return SECTION_META
    .map((section) => ({
      ...section,
      presets: matched.filter((preset) => preset.section === section.id),
    }))
    .filter((group) => group.presets.length > 0);
}
