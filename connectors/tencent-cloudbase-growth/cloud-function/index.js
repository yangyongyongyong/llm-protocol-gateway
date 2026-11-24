'use strict'

/**
 * 腾讯云 CloudBase aiProxy 云函数。
 *
 * 这个函数部署在用户的 CloudBase 环境内，用 @cloudbase/node-sdk 的
 * “云开发 SDK 通道”调用成长计划模型。公网只负责函数调用，不直接访问
 * /v1/ai/*；提交给云函数的是标准 Chat 请求、工具定义和 providerHints。
 *
 * 关键点：generateText 默认会自动执行 FunctionTool；这里把 maxSteps 固定为 1，
 * 并把工具定义作为 ModelTool 传入，让模型只返回 tool_calls，由客户端执行工具后
 * 再发下一轮，符合 OpenAI/Anthropic 客户端的工具调用语义。
 */

const tcb = require('@cloudbase/node-sdk')

const DEFAULT_MODELS = [
  'hy3',
  'hy3-preview',
  'hy4-flash',
  'hy4',
  'hy4-preview',
  'deepseek-v4.1-flash',
  'deepseek-v4.1-flash-1017',
]

let app
let initializedEnv = ''

function splitList(value) {
  /** 把逗号分隔的环境变量拆成去空、去重后的字符串数组。 */
  return [...new Set(String(value || '').split(',').map((item) => item.trim()).filter(Boolean))]
}

function envId() {
  /** 云函数运行时应由 TCB_ENV/ENV_ID 自动提供环境 ID。 */
  return String(process.env.TCB_ENV || process.env.ENV_ID || '').trim()
}

function allowedModels() {
  /** 模型白名单可由环境变量覆盖，避免部署后任意模型名都透传。 */
  return new Set(splitList(process.env.AI_PROXY_ALLOWED_MODELS || DEFAULT_MODELS.join(',')))
}

function providerCandidates(event, model) {
  /** 按调用方提示、环境变量、模型系列依次选择 CloudBase SDK provider。 */
  const requested = []
  if (Array.isArray(event.providerHints)) requested.push(...event.providerHints)
  if (typeof event.provider === 'string') requested.push(event.provider)
  requested.push(...splitList(process.env.AI_PROXY_PROVIDERS))
  if (requested.length === 0) {
    requested.push(...(model.toLowerCase().startsWith('hy') ? ['hunyuan-v3', 'cloudbase'] : ['cloudbase', 'hunyuan-v3']))
  }
  return [...new Set(requested.map((item) => String(item || '').trim()).filter(Boolean))]
}

function errorCode(error) {
  /** 从 Node SDK / 云开发错误对象里提取稳定的错误码。 */
  return String(error?.code || error?.errCode || error?.error?.code || error?.cause?.code || 'UPSTREAM_ERROR')
}

function errorMessage(error) {
  /** 提取可读错误信息，不把凭据或请求正文拼进去。 */
  return String(error?.message || error?.error?.message || error || 'CloudBase model call failed')
}

function validateMessages(messages) {
  /** 按插件约定限制消息体量，防止公网函数被大请求拖死。 */
  if (!Array.isArray(messages) || messages.length === 0) return 'messages is required'
  if (messages.length > 200) return 'too many messages'
  let total = 0
  for (const message of messages) {
    if (!message || typeof message !== 'object') return 'message must be an object'
    if (typeof message.role !== 'string' || !message.role) return 'message.role is required'
    const text = typeof message.content === 'string' ? message.content : JSON.stringify(message.content || '')
    if (text.length > 100000) return 'a message is too large'
    total += text.length
  }
  if (total > 400000) return 'messages are too large'
  return ''
}

function normalizeTools(tools) {
  /** 只保留 OpenAI Chat 形状的 ModelTool，拒绝 FunctionTool 的 fn 字段。 */
  if (!Array.isArray(tools)) return []
  const out = []
  for (const tool of tools) {
    if (!tool || typeof tool !== 'object') continue
    const fn = tool.function && typeof tool.function === 'object' ? tool.function : null
    if (!fn || typeof fn.name !== 'string' || !fn.name.trim()) continue
    const parameters = fn.parameters && typeof fn.parameters === 'object' ? fn.parameters : { type: 'object', properties: {} }
    out.push({
      type: 'function',
      function: {
        name: fn.name.trim(),
        description: String(fn.description || ''),
        parameters,
      },
    })
  }
  return out
}

function normalizeToolChoice(value) {
  /** SDK 只接受 none/auto/custom；required 和指定工具统一退化为 auto。 */
  if (value === 'none') return 'none'
  if (value === 'auto' || value === 'required' || value === 'custom') return 'auto'
  if (value && typeof value === 'object') return 'auto'
  return undefined
}

function completion(model, text, usage, toolCalls) {
  /** 把 Node SDK 的 generateText 结果包装成 OpenAI Chat completion。 */
  const prompt = Number(usage?.prompt_tokens || usage?.input_tokens || 0)
  const completionTokens = Number(usage?.completion_tokens || usage?.output_tokens || 0)
  const calls = Array.isArray(toolCalls) ? toolCalls : []
  const message = { role: 'assistant', content: String(text || '') }
  if (calls.length) message.tool_calls = calls
  return {
    id: `chatcmpl-cloudbase-${Date.now()}`,
    object: 'chat.completion',
    created: Math.floor(Date.now() / 1000),
    model,
    choices: [{
      index: 0,
      message,
      finish_reason: calls.length ? 'tool_calls' : 'stop',
    }],
    usage: {
      prompt_tokens: prompt,
      completion_tokens: completionTokens,
      total_tokens: Number(usage?.total_tokens || prompt + completionTokens),
    },
  }
}

exports.main = async (event = {}) => {
  /** 云函数入口：ping 不消耗模型额度，chat 只走允许的 SDK 通道。 */
  if (event.action === 'ping') {
    return { ok: true, envId: envId(), models: [...allowedModels()], protocols: ['chat', 'responses', 'claude'] }
  }

  const model = String(event.model || '').trim()
  if (!model) return { error: { code: 'INVALID_REQUEST', message: 'model is required' } }
  if (!allowedModels().has(model)) {
    return { error: { code: 'MODEL_NOT_ALLOWED', message: `model ${model} is not allowed by AI_PROXY_ALLOWED_MODELS` } }
  }
  const messages = event.messages
  const invalid = validateMessages(messages)
  if (invalid) return { error: { code: 'INVALID_REQUEST', message: invalid } }

  const currentEnv = envId()
  if (!currentEnv) return { error: { code: 'TCB_ENV_MISSING', message: 'TCB_ENV or ENV_ID is not available in the cloud function' } }
  if (!app || initializedEnv !== currentEnv) {
    app = tcb.init({ env: currentEnv, timeout: 120000 })
    initializedEnv = currentEnv
  }
  const ai = app.ai()
  let lastError = null
  const tools = normalizeTools(event.tools)
  const toolChoice = normalizeToolChoice(event.tool_choice)

  const input = { model, messages, maxSteps: 1 }
  if (tools.length) input.tools = tools
  if (toolChoice) input.tool_choice = toolChoice
  if (typeof event.temperature === 'number') input.temperature = event.temperature
  if (typeof event.top_p === 'number') input.top_p = event.top_p
  if (typeof event.max_tokens === 'number' && event.max_tokens > 0) input.max_tokens = event.max_tokens

  for (const provider of providerCandidates(event, model)) {
    try {
      const result = await ai.createModel(provider).generateText(input)
      if (result?.error) throw result.error
      const resultMessages = Array.isArray(result?.messages) ? result.messages : []
      const last = resultMessages.length ? resultMessages[resultMessages.length - 1] : null
      const toolCalls = Array.isArray(last?.tool_calls) ? last.tool_calls : []
      const text = String(result?.text || last?.content || '')
      return completion(model, text, result?.usage || {}, toolCalls)
    } catch (error) {
      lastError = error
    }
  }

  return {
    error: {
      code: errorCode(lastError),
      message: errorMessage(lastError),
      providerHints: providerCandidates(event, model),
    },
  }
}
