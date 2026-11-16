# Connector Lab：自定义协议转换器全流程验收

Connector Lab 面向“厂商私有协议不进入主代码库”的场景：用户自己运行一个
Connector / Adapter 服务，把 Ocean、WorkBuddy 或其他私有协议转成网关支持的
三种标准协议之一。Lab 只负责验证这条链路，不执行用户脚本，也不新增第四种
`Protocol`。

## 支持的标准协议

- `openai_chat`
- `openai_responses`
- `claude`

至少实现一种即可；同一 Connector 也可以同时实现三种。Provider 的
`protocol` 必须是上述值之一，私有协议名不能写进 Provider.Protocol。

## 入口

```text
POST /__connectors/lab
```

该接口需要 `providers.test` 权限；普通用户可在自己的 Provider 作用域内调用。

## 请求

```json
{
  "name": "ocean-lab",
  "provider": {
    "name": "Ocean Connector",
    "protocol": "openai_chat",
    "baseUrl": "http://127.0.0.1:8790",
    "apiKeySource": "literal:connector-secret",
    "defaultModel": "gpt-5.5",
    "authType": "api_key"
  },
  "protocols": ["openai_chat", "openai_responses", "claude"],
  "model": "gpt-5.5",
  "message": "只回复 lab-ok",
  "keepOnFailure": false,
  "runProviderConformance": false
}
```

字段说明：

| 字段 | 说明 |
|---|---|
| `provider` | 临时创建的 Provider；`baseUrl` 指向用户自己运行的 Connector |
| `protocols` | 要验证的标准入口，默认取 `provider.protocol` |
| `model` | 不填时依次回退 `DefaultModel`、模型目录第一项 |
| `message` | 每条真实入口请求使用的提示词 |
| `keepOnFailure` | 失败时保留临时 Provider/Route/API Key 和 IDs，默认失败也清理 |
| `runProviderConformance` | 额外执行 Provider 层模型/非流式/流式/usage 检查 |

## 自动执行步骤

1. 创建临时 Provider
2. 尝试模型发现（建议项，不阻塞三协议测试）
3. 创建临时 Route
4. 创建临时 API Key
5. 通过真实网关入口逐协议请求：
   - `/v1/chat/completions`
   - `/openai/v1/responses`
   - `/anthropic/v1/messages`
6. 默认清理临时资源并返回逐步报告

返回中的 `steps` 会包含每步的 `passed`、`httpStatus`、`latencyMs`、`detail`
和诊断数据；失败时不需要靠猜日志定位阶段。

## 清理语义

- 成功：默认清理临时 Provider、Route、API Key。
- 失败：默认也清理；设置 `keepOnFailure=true` 时保留现场，并返回
  `providerId`、`routeId`、`apiKeyId`。
- `cleaned=false` 表示清理本身失败，报告会明确列出是哪一类资源。

## 安全边界

Lab 不执行上传脚本，不读取 Provider 的明文 Key，也不会把生产 Provider 的
凭据交给测试客户端。Connector 服务应独立运行，并只暴露三种标准协议之一，
这样用户脚本可以任意选择语言与实现，主网关仍只维护标准协议转换。

## Provider 页面不再维护模板式自定义适配

Provider 页面不再展示 `requestAdapter` 的 URL/Header/Body 模板编辑器。需要
私有协议转换时，先由管理员点击“添加自定义 Provider”定义一个类型（例如
`ocean`），之后在 Provider 目录选择该类型，只填上游 URL 和 Key。网关会启动
类型里保存的 `adapter.py`。原因不是后端不支持旧字段，而是模板式适配和
Connector / 类型脚本重叠：

- 私有协议转换：由独立 Connector 或脚本 Provider 负责；
- 标准协议入口：由网关直接承接；
- 存量 Provider 的 `requestAdapter`：后端继续兼容，普通 PATCH 不携带该字段时
  原样保留；只有显式传 `null` 或空对象才清空。

这样用户只需要理解两层：Connector / 脚本负责“私有协议 → 标准协议”，Provider
负责“标准协议连接 + 模型 + 调度”。脚本 Provider 的完整 ABI、并发、超时、
流式与 API 说明见 [provider-script.md](./provider-script.md)。

## 与普通 Provider API 配合

Lab 是一次性验收；需要长期保存时，再使用现有 CRUD API：

```text
POST   /__providers
PATCH  /__providers/{id}
POST   /__providers/{id}/test
POST   /__providers/{id}/detect-protocols
POST   /__providers/{id}/conformance
POST   /__routes
POST   /__apikeys
DELETE /__providers/{id}
```

Lab 的价值是把这些步骤串成可重复、可清理的单次运行；普通 CRUD 仍是持久化
配置的唯一来源。
