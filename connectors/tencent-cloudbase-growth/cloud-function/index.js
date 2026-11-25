'use strict'

/**
 * 腾讯云 CloudBase aiProxy 云函数。
 *
 * 这个函数部署在用户的 CloudBase 环境内，用 @cloudbase/node-sdk 的
 * “云开发 SDK 通道”调用成长计划模型。公网只负责函数调用，不直接访问
 * /v1/ai/*；提交给云函数的是标准 Chat 请求、工具定义和 providerHints。
 *
 * 工具定义作为 ModelTool 传入，模型返回 tool_calls 后由客户端执行并继续下一轮。
 * 每个 HTTP 请求只生成一步（maxSteps=1），工具由客户端执行；这不是整段对话的步数上限。
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

function configuredModels() {
  /** 只有显式设置 AI_PROXY_ALLOWED_MODELS 才限制模型；默认不拦截。 */
  const raw = String(process.env.AI_PROXY_ALLOWED_MODELS || '').trim()
  return raw ? new Set(splitList(raw)) : null
}

function allowedModels() {
  /** 给 /ping 展示用的模型列表；未配置白名单时给出默认模型名。 */
  return [...(configuredModels() || DEFAULT_MODELS)]
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
  /** 只校验结构，不限制消息条数或正文长度。 */
  if (!Array.isArray(messages) || messages.length === 0) return 'messages is required'
  for (const message of messages) {
    if (!message || typeof message !== 'object') return 'message must be an object'
    if (typeof message.role !== 'string' || !message.role) return 'message.role is required'
  }
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

function chatUsage(raw, sdk) {
  /** SDK 汇总只保留三项计数；以原始响应的计数为准，包括混元的 TitleCase 字段。 */
  const count = (...values) => {
    /** 取第一个有效的上游 token 计数，保留显式的零。 */
    for (const value of values) {
      if (value == null || value === '') continue
      const number = Number(value)
      if (Number.isSafeInteger(number) && number >= 0) return number
    }
    return 0
  }
  const prompt = count(raw.prompt_tokens, raw.PromptTokens, raw.input_tokens, raw.InputTokens, sdk?.prompt_tokens)
  const output = count(raw.completion_tokens, raw.CompletionTokens, raw.output_tokens, raw.OutputTokens, sdk?.completion_tokens)
  // 不同模型 / provider 可能给 OpenAI 风格、Responses 风格或 Claude 风格的
  // 缓存字段。这里保留原始计数，让本地 adapter 归一到各协议的 usage。
  const cacheRead = count(
    raw.cache_read_input_tokens, raw.CacheReadInputTokens,
    raw.prompt_cache_hit_tokens, raw.PromptCacheHitTokens,
    raw.prompt_tokens_details?.cached_tokens, raw.PromptTokensDetails?.CachedTokens,
    raw.input_tokens_details?.cached_tokens, raw.InputTokensDetails?.CachedTokens,
    raw.CachedTokens, sdk?.cache_read_input_tokens, sdk?.prompt_cache_hit_tokens,
    sdk?.prompt_tokens_details?.cached_tokens, sdk?.input_tokens_details?.cached_tokens,
  )
  const cacheCreation = count(
    raw.cache_creation_input_tokens, raw.CacheCreationInputTokens,
    raw.cache_write_input_tokens, raw.CacheWriteInputTokens,
    sdk?.cache_creation_input_tokens, sdk?.cache_write_input_tokens,
  )
  const reasoning = count(raw.reasoning_tokens, raw.ReasoningTokens,
    raw.completion_tokens_details?.reasoning_tokens, raw.CompletionTokensDetails?.ReasoningTokens,
    raw.output_tokens_details?.reasoning_tokens, raw.OutputTokensDetails?.ReasoningTokens)
  return {
    prompt_tokens: prompt,
    completion_tokens: output,
    total_tokens: count(raw.total_tokens, raw.TotalTokens, sdk?.total_tokens, prompt + output),
    prompt_cache_hit_tokens: cacheRead,
    cache_read_input_tokens: cacheRead,
    cache_creation_input_tokens: cacheCreation,
    prompt_tokens_details: { cached_tokens: cacheRead },
    input_tokens_details: { cached_tokens: cacheRead },
    ...(reasoning ? { reasoning_tokens: reasoning, completion_tokens_details: { reasoning_tokens: reasoning } } : {}),
  }
}

function completion(model, text, usage, toolCalls, finishReason, reasoning) {
  /** 把已经校验的本轮结果包装成 OpenAI Chat completion。 */
  const message = { role: 'assistant', content: String(text || '') }
  if (toolCalls.length) message.tool_calls = toolCalls
  if (typeof reasoning === 'string' && reasoning) message.reasoning_content = reasoning
  return {
    id: `chatcmpl-cloudbase-${Date.now()}`,
    object: 'chat.completion',
    created: Math.floor(Date.now() / 1000),
    model,
    choices: [{
      index: 0,
      message,
      finish_reason: finishReason,
    }],
    usage,
  }
}

function invalidResponse(message) {
  /** 结构不合法不是模型正常结束，也不应触发另一个 provider 再生成一次。 */
  throw Object.assign(new Error(message), { code: 'INVALID_UPSTREAM_RESPONSE' })
}

function normalizedFinishReason(value) {
  /** 统一 Chat 和混元的结束原因；保留未知值，交给调用方判错。 */
  const reason = String(value || '').trim().toLowerCase()
  if (['max_tokens', 'max_output_tokens'].includes(reason)) return 'length'
  if (reason === 'sensitive') return 'content_filter'
  if (['tool_call', 'function_call'].includes(reason)) return 'tool_calls'
  return reason
}

function appendedAssistant(messages, historyLength) {
  /** 只从 SDK 本次追加的消息恢复，绝不把请求里的上一条 assistant 当成本次输出。 */
  if (!Array.isArray(messages) || messages.length <= historyLength) return null
  const last = messages[messages.length - 1]
  return last?.role === 'assistant' ? last : null
}

function normalizedCalls(message) {
  /** 接受两种工具字段名并保留完整并行列表；缺 ID 由 adapter 统一补齐。 */
  const list = message?.tool_calls ?? message?.ToolCalls ?? []
  if (!Array.isArray(list)) invalidResponse('model returned an invalid tool_calls list')
  return list.map((call) => {
    const fn = call?.function ?? call?.Function
    const name = fn?.name ?? fn?.Name
    const args = fn?.arguments ?? fn?.Arguments
    if (typeof name !== 'string' || !name.trim() || (args != null && typeof args !== 'string' &&
      (typeof args !== 'object' || Array.isArray(args)))) {
      invalidResponse('model returned an invalid tool call')
    }
    const id = call.id ?? call.Id ?? call.ID
    return {
      ...(typeof id === 'string' && id.trim() ? { id } : {}),
      type: 'function',
      function: { name: name.trim(), arguments: typeof args === 'string' ? args : JSON.stringify(args ?? {}) },
    }
  })
}

function generationCompletion(model, result, step, historyLength) {
  /** 从 SDK 原始响应恢复结束原因、工具和缓存；SDK 汇总 usage 会丢弃缓存字段。 */
  const raws = Array.isArray(result?.rawResponses) ? result.rawResponses : []
  const raw = raws.length ? raws[raws.length - 1] : null
  const envelope = raw?.Response || raw || {}
  if (envelope.error || envelope.Error) invalidResponse('model returned an error instead of a completion')
  const choice = (envelope.choices || envelope.Choices || [])[0] || {}
  const rawMessage = choice.message || choice.Message
  const assistant = appendedAssistant(result?.messages, historyLength) || appendedAssistant(step?.messages, historyLength)
  const role = rawMessage?.role ?? rawMessage?.Role
  if (rawMessage != null && (typeof rawMessage !== 'object' || Array.isArray(rawMessage) ||
    (role != null && role !== 'assistant'))) {
    invalidResponse('model returned an invalid assistant message')
  }
  let reason = normalizedFinishReason(choice.finish_reason || choice.FinishReason || step?.finishReason)
  const incomplete = reason === 'length' || reason === 'content_filter'
  if (reason && !incomplete && reason !== 'stop' && reason !== 'tool_calls') {
    invalidResponse('model returned an unsupported finish reason')
  }
  // 部分输出的工具参数可能不完整，不允许客户端执行。SDK 的 step.toolCall 只有首项，不能用来恢复并行调用。
  let calls = incomplete ? [] : normalizedCalls(rawMessage)
  if (!incomplete && !calls.length) calls = normalizedCalls(assistant)
  if (calls.length) reason = 'tool_calls'
  if (reason === 'tool_calls' && !calls.length) invalidResponse('model requested tool execution but returned no tool_calls')
  const text = rawMessage?.content ?? rawMessage?.Content ?? assistant?.content ?? result?.text ?? ''
  if (typeof text !== 'string' || !reason || (reason === 'stop' && !text)) {
    invalidResponse('model returned no complete assistant output or finish reason')
  }
  const usage = chatUsage(envelope.usage || envelope.Usage || {}, result?.usage || step?.stepUsage)
  const reasoning = rawMessage?.reasoning_content ?? rawMessage?.ReasoningContent ?? assistant?.reasoning_content
  return completion(model, text, usage, calls, reason, reasoning)
}

exports.main = async (event = {}) => {
  /** 云函数入口：ping 不消耗模型额度，chat 只走允许的 SDK 通道。 */
  if (event.action === 'ping') {
    return { ok: true, envId: envId(), models: [...allowedModels()], protocols: ['chat', 'responses', 'claude'] }
  }

  const model = String(event.model || '').trim()
  if (!model) return { error: { code: 'INVALID_REQUEST', message: 'model is required' } }
  const allowed = configuredModels()
  if (allowed && !allowed.has(model)) {
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

  // SDK 默认会尝试执行工具（最多 10 步），但本函数没有客户端工具的 fn。
  // 必须只生成当前一步，把全部 tool_calls 交还 Codex，而不是在云函数里续跑。
  const input = { model, messages, maxSteps: 1 }
  if (tools.length) input.tools = tools
  if (toolChoice) input.tool_choice = toolChoice
  if (typeof event.temperature === 'number') input.temperature = event.temperature
  if (typeof event.top_p === 'number') input.top_p = event.top_p
  if (typeof event.max_tokens === 'number' && event.max_tokens > 0) input.max_tokens = event.max_tokens

  for (const provider of providerCandidates(event, model)) {
    try {
      let step
      const historyLength = messages.length
      const result = await ai.createModel(provider).generateText({
        ...input,
        messages: [...messages],
        onStepFinish: (value) => {
          /** 记录 SDK 归一化后的结束原因，不记录正文或凭据。 */
          step = value
        },
      })
      if (result?.error) throw result.error
      return generationCompletion(model, result, step, historyLength)
    } catch (error) {
      lastError = error
      if (errorCode(error) === 'INVALID_UPSTREAM_RESPONSE') break
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
