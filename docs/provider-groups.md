# Provider 分组

Provider 分组是管理员控制台里的轻量归类维度，只用于 Provider 列表的筛选、批量移动和卡片标签。

## 权限边界

- 分组管理只对管理员开放。
- 管理员始终可以看到全部 Provider，分组不会限制管理员。
- 分组不参与 Provider 可见性、Route、API Key、故障转移或请求期鉴权。
- 普通用户看不到分组管理入口，也不需要在 Provider 页面感知分组。
- 管理员要给普通用户使用某个 Provider，仍然到「用户管理」里分配具体 Provider；分组不会隐式授权。

## 数据模型

```text
ProviderGroup
- id
- name
- position

Provider
- groupId   空表示未分组
```

每个 Provider 最多属于一个分组。分组与 Provider 归属保存在 SQLite `settings` 中：

- `providerGroups`
- `providerGroupAssignments`

这样不会修改 Provider 主表结构，后续增加备注、颜色等纯展示字段也不需要迁移表。

## 管理接口

```text
GET    /__provider-groups
POST   /__provider-groups
PATCH  /__provider-groups/{id}
DELETE /__provider-groups/{id}
POST   /__provider-groups/reorder
POST   /__provider-groups/assign
```

删除分组不会删除 Provider：组内 Provider 自动回到「未分组」，现有用户授权保持不变。

## 控制台行为

- Provider 页展示管理员分组筛选条和「管理分组」按钮。
- 创建/编辑 Provider 时可以选择分组。
- 选中多个 Provider 后可批量移入指定分组。
- 卡片只向管理员显示分组徽标。
- 分组内新增 Provider 不会产生任何额外授权或权限变化。
