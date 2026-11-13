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

## 下游归属与授权审计

下游网关注册时会创建一行 `users(kind=peer)`，但这只是网关身份，不等于具体某个人。
为了让人和用量都能对上，网关额外维护：

- `users.owner_user_id`：仅 peer 行使用，表示这个下游网关归属哪个真实用户。
- `peer_grant_audits`：每次管理员手工调整下游授权时，记录授权人、授权时间、
  授权模式以及调整前后的 Provider 列表。

口径：

- 下游第一次被管理员调整授权时，如果还没有归属用户，默认归属到本次操作人。
- 后续其他管理员调整授权只追加审计，不会自动转移归属。
- 归属可以在「网关互联」中单独修改，也可以清空。
- 无归属的下游流量只进入 Provider 全量 Token，不计入任何人的个人 Token。
- 有归属的下游流量，其 peer 凭据产生的 Token 会计入所属用户的个人 Token。

## 下游网关用量统计

「用量统计」会把下游网关送进本机的 ingress 流量单独聚合，展示请求数、输入 / 输出
Token、缓存读取 / 写入 Token、已计价成本与计价覆盖率。口径如下：

- 只统计 `peer_direction=ingress` 的流量；本机转发给上游父网关的 `egress` 流量
  不会出现在“下游网关”排行里，避免上下游重复记账。
- 管理员看到所有下游网关；普通用户只看到自己名下 Key 对应的下游网关，包含
  已经通过 `users.owner_user_id` 归属给自己的 peer 账号。
- 网关名称按当前 peer 账号实时解析；请求日志与日聚合只保存稳定的
  `gateway_id`，改显示名不会改写历史数据。
- 日聚合使用 `downstream_gateway` 全量桶和 `key_downstream_gateway` Key 维度桶，
  永久保留。升级到 schema v4 时会从未过期的请求日志回放补齐这两类桶；超出请求
  日志保留期的历史只能保留原有总量，无法补出当时的下游网关归属。
- 无归属的下游网关仍进入管理员的全量统计，但不进入任何普通用户的个人统计。

## 实现口径

- 数据库的 `user_resource_grants` 只在动态模式下保存一个内部标记
  `peer:all-providers`，不会把当前 Provider 全量写死。
- 授权读取时按实时 Provider 集合展开，因此新增、恢复 Provider 不需要回写历史授权。
- 该内部标记只对 `kind=peer` 的下游网关账号生效。普通用户即使误带同名字符串，
  也不会获得额外 Provider。
- 子网关的后台同步按 `upstreamKey` 去重：同一个父网关下的多个成员只查询一次清单。

## 控制台接口

`GET /__peers/owners`

返回可选的本地用户列表，用于设置下游网关所属用户。

`PATCH /__peers/{id}/owner`

```json
{
  "ownerUserId": "user-id"
}
```

传空字符串表示清空归属。

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

卡片上的今日 Token 也按上下游语义分开：

- 总量由父网关在 `/__peer/providers` 响应里下发，口径是父网关当天该 Provider 的
  全量 Token（输入 + 输出），包含父端所有用户与下游流量。
- 「我的用量」只统计子网关本机、当前用户名下 Key 在该成员 Provider 上的流量。
- 旧版父网关没有下发今日总量时，子端显示 `— / 我的用量`，绝不拿子网关自己的
  转发量冒充父端全量。
- 列表按今日 Token 倒序时，父端总量未知不会按 0 处理：排序会回退到本机已观测
  的转发量；父端总量已下发时优先使用它，并在快照暂时落后时取两者较大值。
