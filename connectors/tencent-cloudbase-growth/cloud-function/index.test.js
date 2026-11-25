'use strict'

const assert = require('node:assert/strict')
const { readFileSync } = require('node:fs')
const { join } = require('node:path')
const { test } = require('node:test')
const { runInNewContext } = require('node:vm')

const source = readFileSync(join(__dirname, 'index.js'), 'utf8')
const tools = ['lookup', 'patch'].map((name) => ({ type: 'function', function: { name } }))
const calls = [
  { id: 'call_lookup', type: 'function', function: { name: 'lookup', arguments: '{"id":1}' } },
  { type: 'function', function: { name: 'patch', arguments: '{"input":"  原文\n"}'.replace('\n', '\\n') } },
]
const messages = [
  { role: 'user', content: '开始' },
  { role: 'assistant', content: '历史回复' },
]

function makeProxy(generateText) {
  /** 在隔离的上下文中加载云函数，模型 SDK 由测试固定实现替换。 */
  const exports = {}
  let requests = 0
  runInNewContext(source, {
    exports,
    process: { env: { TCB_ENV: 'test-only' } },
    require(id) {
      /** 禁止测试加载真实 CloudBase SDK。 */
      assert.equal(id, '@cloudbase/node-sdk')
      return { init: () => ({ ai: () => ({ createModel: () => ({ generateText(input) {
        /** 记录次数，检查失败不会改走其它通道把同一轮再跑一次。 */
        requests++
        return generateText(input)
      } }) }) }) }
    },
  })
  return { main: exports.main, requests: () => requests }
}

function chat(proxy, override) {
  /** 用同一请求历史模拟独立的 CloudBase 调用，转换跨虚拟机的对象原型。 */
  return proxy.main({ model: 'hy3', messages, tools, ...override }).then((value) => JSON.parse(JSON.stringify(value)))
}

test('单次请求不代跑工具，恢复原始响应的全部并行调用和 usage', async () => {
  const proxy = makeProxy(async (input) => {
    /** 同 SDK 的真实行为一样，step.toolCall 只提供一个工具。 */
    assert.equal(input.maxSteps, 1)
    assert.equal(input.tools.length, 2)
    assert.equal(input.messages.length, messages.length)
    const assistant = { role: 'assistant', content: '先检查', tool_calls: calls }
    input.onStepFinish({ finishReason: 'stop', toolCall: calls[0], messages: [...messages, { ...assistant, tool_calls: [calls[0]] }] })
    return {
      text: assistant.content,
      usage: { prompt_tokens: 500, completion_tokens: 7 },
      rawResponses: [{ choices: [{ message: assistant, finish_reason: 'stop' }], usage: {
        prompt_tokens: 500, completion_tokens: 7, total_tokens: 507,
        prompt_tokens_details: { cached_tokens: 400 }, cache_creation_input_tokens: 20,
      } }],
    }
  })
  const response = await chat(proxy, { maxSteps: 99 })
  assert.equal(proxy.requests(), 1)
  assert.equal(response.choices[0].finish_reason, 'tool_calls')
  assert.equal(response.choices[0].message.content, '先检查')
  assert.deepEqual(response.choices[0].message.tool_calls, calls)
  assert.equal(response.usage.prompt_tokens_details.cached_tokens, 400)
  assert.equal(response.usage.cache_creation_input_tokens, 20)
})

test('没有原始响应时只使用 SDK 本次追加的 assistant', async () => {
  const assistant = { role: 'assistant', content: '', tool_calls: calls }
  const proxy = makeProxy(async ({ onStepFinish }) => {
    /** step 的 messages 携带完整工具数组，不能只读 toolCall。 */
    onStepFinish({ finishReason: 'tool_calls', toolCall: calls[0], messages: [...messages, assistant] })
    return { messages, text: '', usage: { prompt_tokens: 3, completion_tokens: 1 } }
  })
  const response = await chat(proxy)
  assert.deepEqual(response.choices[0].message.tool_calls, calls)
  assert.equal(response.usage.total_tokens, 4)

  const historical = makeProxy(async ({ onStepFinish }) => {
    /** SDK 未追加新消息时，历史里的工具不属于本次生成。 */
    onStepFinish({ finishReason: 'tool_calls', messages: [messages[0], assistant], toolCall: calls[0] })
    return { messages: [messages[0], assistant] }
  })
  assert.equal((await chat(historical)).error.code, 'INVALID_UPSTREAM_RESPONSE')
  assert.equal(historical.requests(), 1)
})

test('截断不交付残缺调用，畸形并行结果报错', async () => {
  const partial = { role: 'assistant', content: '尚未完成', tool_calls: [{ function: { arguments: '{' } }] }
  for (const reason of ['length', 'stop']) {
    const proxy = makeProxy(async () => {
      /** 同一份损坏的工具数组分别搭配截断和正常结束标记。 */
      return { rawResponses: [{ choices: [{ message: partial, finish_reason: reason }] }] }
    })
    const response = await chat(proxy)
    if (reason === 'length') {
      assert.equal(response.choices[0].finish_reason, 'length')
      assert.equal(response.choices[0].message.tool_calls, undefined)
    } else {
      assert.equal(response.error.code, 'INVALID_UPSTREAM_RESPONSE')
    }
    assert.equal(proxy.requests(), 1)
  }
})
