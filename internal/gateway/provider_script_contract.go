package gateway

// Provider 脚本 ABI 的版本号。以后如果环境变量或端点语义发生不兼容变化，
// 在这里递增；前端和外部自动化可以据此决定是否重新生成 adapter。
const providerScriptABIVersion = "1"

// providerScriptABI 是自定义协议 Provider 的稳定契约。这里的文本同时用于
// 控制台展示和给 AI 的提示词，必须写清楚“必要参数 + 运行语义”，不能只列
// 三个 URL。网关只负责进程生命周期与标准协议入口，脚本负责私有协议转换。
const providerScriptABI = `LLM Protocol Gateway · Provider Script ABI v1

一、边界
- 脚本是一个独立进程，负责把厂商私有协议转换成网关支持的三种标准协议之一：
  openai_chat / openai_responses / claude。
- 脚本不进入网关 Go 进程；网关不解析私有协议，也不为单个厂商修改主转换流程。
- Provider.Protocol 决定网关对脚本调用哪一种标准协议。最简单的实现是选择
  openai_chat，只实现 OpenAI Chat，网关会承接其他客户端协议到 Chat 的转换。

二、运行环境
网关启动脚本时注入以下环境变量：
  PROVIDER_SCRIPT_PORT                 必须监听的 loopback 端口（整数）
  PROVIDER_SCRIPT_PROVIDER_ID          Provider ID
  PROVIDER_SCRIPT_PROTOCOL             openai_chat / openai_responses / claude
  PROVIDER_SCRIPT_HEALTH_PATH          健康检查路径，默认 /health
  PROVIDER_SCRIPT_MAX_CONCURRENCY      网关并发上限，0 表示未配置
  PROVIDER_SCRIPT_REQUEST_TIMEOUT_SEC  单次请求超时秒数，0 表示未配置
  PROVIDER_SCRIPT_ENTRYPOINT           入口文件名，通常 adapter.py
  UPSTREAM_BASE_URL                    私有厂商协议根地址
  UPSTREAM_API_KEY                     私有厂商 API Key，可能为空
  UPSTREAM_AUTH_HEADER                 上游鉴权头名，默认 Authorization

鉴权约定：
- UPSTREAM_AUTH_HEADER 为 Authorization 时，若 UPSTREAM_API_KEY 不以 Bearer
  开头，脚本应发送 "Bearer " + key；为 x-api-key 等头时发送原始值。
- 不要把 Key 写入源码、日志、异常或响应。Key 只允许出现在发往私有上游的请求头。

三、必须实现的 HTTP 接口
- GET  /health
- GET  /v1/models
- 按 PROVIDER_SCRIPT_PROTOCOL 至少实现下面一个推理入口：
  - openai_chat:       POST /v1/chat/completions
  - openai_responses:  POST /v1/responses
  - claude:            POST /v1/messages

接口要求：
- 只监听 127.0.0.1:PROVIDER_SCRIPT_PORT，禁止监听公网，禁止自行改端口。
- /health 返回 2xx 表示进程、配置和必要依赖已就绪；不要在校验中消耗推理额度。
- /v1/models 返回 {"data":[{"id":"model-id","context_length":200000}]}；每个模型
  必须稳定可寻址，不能返回空 id。
- /v1/chat/completions 非流式必须返回标准 OpenAI Chat completion JSON。
- /v1/responses 和 /v1/messages 必须返回对应标准的完整 JSON 形状。

四、并发、超时与取消
- 使用线程安全 HTTP 服务（Python 推荐 ThreadingHTTPServer 或成熟 ASGI server）。
- PROVIDER_SCRIPT_MAX_CONCURRENCY > 0 时，脚本必须用信号量或队列把同时在飞的
  上游请求限制在该值以内；网关还会在 Provider 层施加同一上限，两层取小。
- 达到并发上限时可以短暂排队；排队超过 PROVIDER_SCRIPT_REQUEST_TIMEOUT_SEC 或
  无法排队时必须返回 429，并带 JSON error 和 Retry-After，不能无限堆积。
- PROVIDER_SCRIPT_REQUEST_TIMEOUT_SEC > 0 时必须同时约束上游连接、首包和总等待；
  0 表示由脚本设置合理默认值，不能真的无限等待。
- 客户端断开或网关取消请求时，脚本必须停止读取上游、关闭连接并释放并发槽位。
- 不得用一个全局锁把整个进程串行化；健康检查和不同会话请求不应互相阻塞。

五、流式协议
- stream=true 时必须返回 Content-Type: text/event-stream，并逐帧 flush，不能先
  收完整答案再一次性输出。
- OpenAI Chat：
  - 每个增量使用 data: {"object":"chat.completion.chunk", ...}。
  - 文本放 choices[0].delta.content；工具调用放 choices[0].delta.tool_calls。
  - 最后一帧带 finish_reason（stop / length / tool_calls 等），随后输出
    data: [DONE] 并以空行结束。
- OpenAI Responses：
  - 按 Responses 流事件输出，至少正确发出 response.created、
    response.output_text.delta、response.completed（或上游对应的标准事件）。
  - 不能把 Chat Completions 的 chunk 直接冒充 Responses 事件。
- Claude Messages：
  - 按 Anthropic SSE 事件输出 message_start、content_block_start、
    content_block_delta、content_block_stop、message_delta、message_stop。
  - 不要给 Claude 流额外伪造 data: [DONE]；结束帧必须是 message_stop。
- 所有流都必须处理心跳/空行，不能因为模型长时间思考就让代理层误判断流。

六、工具调用与推理字段
- tool_calls 必须使用稳定 index；id、type、function.name 在首帧确定，
  function.arguments 可以分多帧增量拼接。
- 工具调用结束时 finish_reason 必须是 tool_calls；不要把 arguments 拼成非法 JSON。
- 正确处理 tool role、tool_call_id、tools、tool_choice 和并行工具调用。
- 若厂商返回 reasoning/thinking，应映射到对应标准协议字段或安全丢弃，不能把
  reasoning 文本混进普通 content。
- 未知字段应保守透传或忽略；不能把私有协议字段直接暴露给网关客户端。

七、usage、缓存与计费
- 非流式和流式结束帧应尽量返回 usage：
  - OpenAI Chat：prompt_tokens / completion_tokens / total_tokens；
    prompt_tokens_details.cached_tokens。
  - OpenAI Responses：input_tokens / output_tokens / total_tokens；
    input_tokens_details.cached_tokens。
  - Claude：input_tokens / output_tokens / cache_read_input_tokens /
    cache_creation_input_tokens。
- 缓存命中必须如实返回，不能用 0 冒充；没有该字段时应省略而不是编造。
- 不要求脚本计算价格；网关会按模型目录和 usage 做统计。

八、错误与可观测性
- 上游 400/401/403/404/429/5xx 应保留 HTTP 状态、Content-Type、JSON 错误和
  Retry-After（如果有），不要静默改成 200，也不要把厂商错误包成纯文本。
- 网络超时返回 504，连接失败返回 502；错误响应至少包含
  {"error":{"message":"...","type":"..."}}。
- 日志写 stderr，最多记录 provider id、模型、状态码、耗时和错误码；禁止记录
  API Key、Authorization、完整 prompt、完整响应或用户隐私。
- stdout/stderr 都不能输出炫技 banner；网关会把它们写入 adapter.log。

九、生命周期与验收
- 进程启动后应在 15 秒内通过 /health；收到 SIGTERM/SIGINT 时应停止接收新请求，
  等待在飞请求短暂结束后退出。
- 网关会在启动后自动请求 /v1/models；若发现模型会回填 DefaultModel。
- 网关的“测试”会依次验证 health、models 和一次最小推理请求，默认提示词只
  要求回复 script-ok。
- 推荐先在本地用独立端口运行并用 curl 自测，再交给网关保存。
`

// providerScriptPrompt 是控制台“复制 AI Prompt”按钮使用的完整任务描述。
// 用户只需粘贴私有协议文档/curl，再让 AI 按此契约生成脚本。
func providerScriptPrompt() string {
	return `你是私有协议适配器工程师。请为 LLM Protocol Gateway 生成一个可直接运行的 Python 3 Provider Script ABI v1 适配器。

【我的私有厂商资料】
- 上游 Base URL：<填写>
- 鉴权方式/头名：<填写，例如 Authorization: Bearer xxx 或 api-key: xxx>
- 协议文档或 curl：<粘贴完整 curl、请求体和响应样例>
- 需要的模型列表：<填写，或说明如何从上游获取>
- 是否需要流式/tool_calls/缓存字段：<填写>

【硬性要求】
1. 输出完整 adapter.py（以及必要时 requirements.txt），不要只给伪代码或片段。
2. 只监听 127.0.0.1:${PROVIDER_SCRIPT_PORT}，提供：
   GET /health
   GET /v1/models
   POST /v1/chat/completions（至少这一种；如选择 Responses/Claude，则提供对应入口）
3. 从环境变量读取并正确使用：
   PROVIDER_SCRIPT_PORT、PROVIDER_SCRIPT_PROVIDER_ID、PROVIDER_SCRIPT_PROTOCOL、
   PROVIDER_SCRIPT_HEALTH_PATH、PROVIDER_SCRIPT_MAX_CONCURRENCY、
   PROVIDER_SCRIPT_REQUEST_TIMEOUT_SEC、UPSTREAM_BASE_URL、UPSTREAM_API_KEY、
   UPSTREAM_AUTH_HEADER。
4. 使用线程安全并发模型；严格遵守 PROVIDER_SCRIPT_MAX_CONCURRENCY。达到上限时
   可短排队，超时必须返回 429 + Retry-After，不能无限堆积。
5. 遵守 PROVIDER_SCRIPT_REQUEST_TIMEOUT_SEC；客户端断开时立即取消上游并释放槽位。
6. 非流式返回标准 OpenAI Chat completion JSON。
7. stream=true 时必须逐帧输出 SSE：
   - Chat 使用 data: {...}，文本增量在 choices[0].delta.content；
   - tool_calls 使用稳定 index 和增量 function.arguments；
   - 最后带 finish_reason，并以 data: [DONE] 结束。
8. 正确映射 tool role、tool_choice、并行 tool_calls、reasoning/thinking、usage 和
   缓存命中字段（cached_tokens / cache_read_input_tokens 等）。
9. 上游 400/401/403/404/429/5xx 保留状态码、JSON 错误和 Retry-After；超时映射 504，
   连接失败映射 502。禁止静默改成 200。
10. 不把 API Key、Authorization、完整 prompt 或隐私写入日志/响应。日志只写 stderr。
11. 不修改网关代码、不新增第四种 Protocol；所有私有协议细节只存在于脚本内。
12. 优先 Python 标准库；若必须第三方依赖，明确列出 requirements.txt，并说明安装方式。
13. 处理 SIGTERM/SIGINT，停止接收新请求并优雅退出。
14. 代码中不要硬编码 URL、Key、模型名或端口；必须从环境变量或集中常量读取。

【交付物】
1. adapter.py 完整代码。
2. requirements.txt（如有）。
3. 本地启动命令和 3 条自测 curl：/health、/v1/models、非流式推理。
4. 一段流式 curl 自测命令，并说明如何确认收到结束帧。
5. 简短列出你做了哪些协议映射，以及还有哪些信息不足/需要我补充。

请先检查资料是否足够；缺少关键 URL、鉴权或请求/响应样例时，先列出最少问题，不要凭空猜测。`
}
