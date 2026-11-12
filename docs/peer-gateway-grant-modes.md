# 网关互联：下游 Provider 授权模式

父网关在「网关互联 → 注册到本机的下游网关」里管理每个下游能使用哪些输入 Provider。
授权有两种模式：

## 指定 Provider

- 保存当前勾选的 Provider 列表。
- 之后父网关新增 Provider 不会自动授权给下游。
- 适合需要严格限制下游资源范围的场景。

## 全部 Provider · 动态

- 授权当前以及未来新增的所有可启用 Provider。
- 父网关后续新增 Provider 时不需要重新编辑授权。
- 子网关会每 5 分钟同步一次父网关清单；手动点「测试连接」也会立即同步。
- 下游请求始终由父网关按当下 Provider 集合判断，授权收回或 Provider 禁用会立即影响新请求。

## 实现口径

- 数据库的 `user_resource_grants` 只在动态模式下保存一个内部标记
  `peer:all-providers`，不会把当前 Provider 全量写死。
- 授权读取时按实时 Provider 集合展开，因此新增、恢复 Provider 不需要回写历史授权。
- 该内部标记只对 `kind=peer` 的下游网关账号生效。普通用户即使误带同名字符串，
  也不会获得额外 Provider。
- 子网关的后台同步按 `upstreamKey` 去重：同一个父网关下的多个成员只查询一次清单。

## 控制台接口

`PATCH /__peers/{id}/grants`

指定列表：

```json
{
  "grantMode": "selected",
  "providerIds": ["provider-a", "provider-b"]
}
```

动态全量：

```json
{
  "grantMode": "all"
}
```

为兼容旧版控制台，未携带 `grantMode` 时仍按 `selected` 处理；此时
`providerIds` 必填，空数组表示收回全部授权。

## 重启与自动续签

网关互联使用两把独立凭据：

- `gwpeer-…`：流量 bearer，只用于 `/__peer/*` 模型与清单请求。
- `gwrefresh-…`：续签凭据，只能调用 `POST /__peer/renew`，不能作为模型密钥。

父子任一端重启后，流量 bearer 仍然从 SQLite 恢复；如果 bearer 被轮换、误删或
临时失效，子网关会：

1. 用 `gwrefresh-…` 调用父网关 `/__peer/renew`；
2. 原子换发新的 bearer + refresh；
3. 更新同一上游的全部成员 Provider；
4. 自动重放刚才那次请求。

旧版只有单凭据的记录会在下一次后台刷新时调用 `POST /__peer/refresh-token`，
用仍然有效的 bearer 一次性升级为双凭据；用户不需要重新填写用户名密码。

续签凭据存储在 peer Provider 的隐藏字段中，使用 `oauth_refresh_token` 列持久化，
不会出现在控制台、Provider 导出或 API 响应里。父端吊销账号时会同时删除两类凭据。

## 卡片展示继承

父网关通过 `/__peer/providers` 额外下发一份安全的卡片快照，包括：

- 上游连接类型（如 WorkBuddy、Qoder、Claude OAuth）。
- 可展示的账号标签。
- 厂商分组，用于复用父端品牌图标。
- 最近一次额度缓存（WorkBuddy credits、Claude/ChatGPT/Cursor 窗口、智谱、OpenCode Go、DeepSeek 余额等）。

子网关把快照保存在对应成员 Provider 的快照中，输入 Provider 卡片会沿用同样的图标、
账号副标题、连接徽章和额度进度条样式。额度卡片明确标记“数据来自上游网关，只读”，
不会在下游重复请求 WorkBuddy 等厂商接口。父端额度缓存未 warm 时只隐藏额度部分，
不会把未知状态画成 0%。
