package domain

// Permission is a single capability a console role can grant. Permissions are
// grouped into modules (see PermissionModules) so the console can render one
// card per functional area instead of a flat checkbox wall.
type Permission string

const (
	// 仪表盘 / 概览
	PermDashboardView Permission = "dashboard.view"

	// 输入 Provider（上游账号）
	PermProvidersView   Permission = "providers.view"
	PermProvidersCreate Permission = "providers.create"
	PermProvidersEdit   Permission = "providers.edit"
	PermProvidersDelete Permission = "providers.delete"
	PermProvidersTest   Permission = "providers.test"
	PermProvidersSecret Permission = "providers.secret" // 查看/改写 API Key、接入账号（OAuth/PAT）
	PermProvidersToggle Permission = "providers.toggle" // 启用停用、并发、首包超时、RTK

	// 模型与路由
	PermModelsView  Permission = "models.view"
	PermRoutesWrite Permission = "routes.write"

	// API 密钥
	PermKeysView   Permission = "keys.view"
	PermKeysCreate Permission = "keys.create"
	PermKeysEdit   Permission = "keys.edit"
	PermKeysDelete Permission = "keys.delete"
	PermKeysQuota  Permission = "keys.quota" // 成本账本·密钥额度

	// 接入地址 / 公网访问
	PermEndpointsView Permission = "endpoints.view"
	PermEndpointsEdit Permission = "endpoints.edit"
	PermPublicAccess  Permission = "public.access"

	// 用量与日志
	PermUsageView   Permission = "usage.view"
	PermUsageAll    Permission = "usage.all" // 查看所有人的用量，而非仅自己
	PermLogsView    Permission = "logs.view"
	PermLogsAll     Permission = "logs.all"
	PermAppLogsView Permission = "logs.app"
	PermPricing     Permission = "usage.pricing"

	// 监控中心
	PermMonitorView   Permission = "monitor.view"
	PermMonitorManage Permission = "monitor.manage"

	// 额度保活
	PermQuotaKeeperView   Permission = "quota.view"
	PermQuotaKeeperManage Permission = "quota.manage"

	// 用户与角色
	PermUsersView   Permission = "users.view"
	PermUsersManage Permission = "users.manage"
	PermRolesManage Permission = "roles.manage"

	// 网关互联（父子网关）：查看已连接的上游与已注册的下游，以及注册/吊销。
	PermPeersView   Permission = "peers.view"
	PermPeersManage Permission = "peers.manage"

	// 系统
	PermMachineView  Permission = "system.machine"
	PermSettingsView Permission = "system.settings"
	PermSettingsEdit Permission = "system.settings.write"
)

// PermissionMeta describes one permission for the console's role editor.
type PermissionMeta struct {
	Key   Permission `json:"key"`
	Label string     `json:"label"`
}

// PermissionModule is one functional block of the console: a named group of
// permissions that maps 1:1 to a sidebar section.
type PermissionModule struct {
	ID          string           `json:"id"`
	Label       string           `json:"label"`
	Description string           `json:"description"`
	Permissions []PermissionMeta `json:"permissions"`
}

// PermissionCatalog is the authoritative list of every grantable permission,
// ordered the way the role editor renders it. Adding a capability here is the
// only place needed for it to show up in the console.
func PermissionCatalog() []PermissionModule {
	m := func(key Permission, label string) PermissionMeta {
		return PermissionMeta{Key: key, Label: label}
	}
	return []PermissionModule{
		{
			ID: "overview", Label: "仪表盘", Description: "首页概览与健康状况",
			Permissions: []PermissionMeta{m(PermDashboardView, "查看仪表盘")},
		},
		{
			ID: "providers", Label: "输入 Provider", Description: "上游账号、模型与路由",
			Permissions: []PermissionMeta{
				m(PermProvidersView, "查看 Provider"),
				m(PermProvidersCreate, "新建 Provider"),
				m(PermProvidersEdit, "编辑 Provider"),
				m(PermProvidersDelete, "删除 Provider"),
				m(PermProvidersTest, "对话测试/获取模型"),
				m(PermProvidersSecret, "查看与修改凭据、接入账号"),
				m(PermProvidersToggle, "启用停用与调度参数"),
				m(PermModelsView, "查看模型列表"),
				m(PermRoutesWrite, "维护路由"),
			},
		},
		{
			ID: "keys", Label: "API 密钥", Description: "对外发放的密钥与额度",
			Permissions: []PermissionMeta{
				m(PermKeysView, "查看密钥"),
				m(PermKeysCreate, "新建密钥"),
				m(PermKeysEdit, "编辑密钥"),
				m(PermKeysDelete, "删除密钥"),
				m(PermKeysQuota, "设置密钥额度"),
			},
		},
		{
			ID: "endpoints", Label: "接入与公网", Description: "接入地址、公网隧道的暴露方式",
			Permissions: []PermissionMeta{
				m(PermEndpointsView, "查看接入地址"),
				m(PermEndpointsEdit, "修改接入地址"),
				m(PermPublicAccess, "公网访问开关与隧穿"),
			},
		},
		{
			ID: "usage", Label: "用量与日志", Description: "调用统计、请求明细、应用日志与价格",
			Permissions: []PermissionMeta{
				m(PermUsageView, "查看用量"),
				m(PermUsageAll, "查看全部用户的用量"),
				m(PermLogsView, "查看请求日志"),
				m(PermLogsAll, "查看全部用户的请求日志"),
				m(PermAppLogsView, "查看应用日志"),
				m(PermPricing, "维护价格表"),
			},
		},
		{
			ID: "monitor", Label: "监控中心", Description: "账号监控、告警渠道与探测",
			Permissions: []PermissionMeta{
				m(PermMonitorView, "查看监控"),
				m(PermMonitorManage, "维护告警与探测规则"),
			},
		},
		{
			ID: "quota", Label: "额度保活", Description: "定时探测与保活任务",
			Permissions: []PermissionMeta{
				m(PermQuotaKeeperView, "查看任务"),
				m(PermQuotaKeeperManage, "维护任务"),
			},
		},
		{
			ID: "admin", Label: "用户与角色", Description: "账号、角色与权限分配",
			Permissions: []PermissionMeta{
				m(PermUsersView, "查看用户"),
				m(PermUsersManage, "管理用户"),
				m(PermRolesManage, "管理角色与权限"),
			},
		},
		{
			ID: "peers", Label: "网关互联", Description: "注册到上游网关，以及管理注册到本机的下游网关",
			Permissions: []PermissionMeta{
				m(PermPeersView, "查看互联网关"),
				m(PermPeersManage, "注册/吊销互联网关"),
			},
		},
		{
			ID: "system", Label: "系统", Description: "机器状态与网关设置",
			Permissions: []PermissionMeta{
				m(PermMachineView, "查看机器状态"),
				m(PermSettingsView, "查看设置"),
				m(PermSettingsEdit, "修改设置"),
			},
		},
	}
}

// AllPermissions returns every permission key in catalog order.
func AllPermissions() []Permission {
	modules := PermissionCatalog()
	out := make([]Permission, 0, 48)
	for _, module := range modules {
		for _, perm := range module.Permissions {
			out = append(out, perm.Key)
		}
	}
	return out
}

// IsKnownPermission reports whether key is in the catalog.
func IsKnownPermission(key Permission) bool {
	for _, known := range AllPermissions() {
		if known == key {
			return true
		}
	}
	return false
}

// ResourceScope says which resources a role's permissions apply to.
type ResourceScope string

const (
	// ScopeOwn: keys the user owns plus providers the user owns or an admin
	// assigned on the users page. Grants are additive, so a normal user keeps
	// whatever it created while also reaching the shared providers.
	ScopeOwn ResourceScope = "own"
	// ScopeAll: every resource on the instance.
	ScopeAll ResourceScope = "all"
	// ScopeGranted is the legacy "granted only" scope. Grants are additive now,
	// so it is read back as ScopeOwn to keep old rows working.
	ScopeGranted ResourceScope = "granted"
)

func ParseResourceScope(raw string) (ResourceScope, bool) {
	switch ResourceScope(raw) {
	case ScopeOwn, ScopeGranted:
		return ScopeOwn, true
	case ScopeAll:
		return ScopeAll, true
	}
	return "", false
}

// Built-in role IDs. Custom roles get generated IDs; their permissions live in
// the roles table, while the two built-ins are answered from code so an upgrade
// can never lock the last administrator out.
const (
	RoleIDAdmin   = "admin"
	RoleIDUser    = "user"
	RoleIDViewer  = "viewer"
	RoleIDGrantee = "grantee" // 只能用被分配 Provider 的普通用户（不能自建）
)

// BuiltinRoleIDs are roles that may not be deleted.
func BuiltinRoleIDs() []string { return []string{RoleIDAdmin, RoleIDUser, RoleIDViewer, RoleIDGrantee} }

// IsBuiltinRole reports whether id is a built-in role.
func IsBuiltinRole(id string) bool {
	for _, builtin := range BuiltinRoleIDs() {
		if builtin == id {
			return true
		}
	}
	return false
}

// BuiltinRole returns the definition of a built-in role.
func BuiltinRole(id string) (Role, bool) {
	switch id {
	case RoleIDAdmin:
		return Role{
			ID: RoleIDAdmin, Name: "管理员", Description: "全部功能与全部资源，不可删除",
			Builtin: true, Scope: ScopeAll, Permissions: []Permission{PermAll},
		}, true
	case RoleIDUser:
		return Role{
			ID: RoleIDUser, Name: "普通用户", Description: "自建 Provider 与密钥，维护额度保活；看不到别人的资源",
			Builtin: true, Scope: ScopeOwn, Permissions: []Permission{
				PermDashboardView,
				PermProvidersView, PermProvidersCreate, PermProvidersEdit, PermProvidersDelete,
				PermProvidersTest, PermProvidersSecret, PermProvidersToggle,
				PermModelsView, PermRoutesWrite,
				PermKeysView, PermKeysCreate, PermKeysEdit, PermKeysDelete,
				PermEndpointsView,
				PermUsageView, PermLogsView,
				PermQuotaKeeperView, PermQuotaKeeperManage,
			},
		}, true
	case RoleIDViewer:
		return Role{
			ID: RoleIDViewer, Name: "只读成员", Description: "只能查看，不能改动任何配置",
			Builtin: true, Scope: ScopeOwn, Permissions: []Permission{
				PermDashboardView,
				PermProvidersView, PermModelsView,
				PermKeysView,
				PermEndpointsView,
				PermUsageView, PermLogsView,
			},
		}, true
	case RoleIDGrantee:
		return Role{
			ID: RoleIDGrantee, Name: "受限用户", Description: "只能使用管理员分配的 Provider，不能自建",
			Builtin: true, Scope: ScopeOwn, Permissions: []Permission{
				PermDashboardView,
				PermProvidersView, PermProvidersTest, PermModelsView,
				PermKeysView, PermKeysCreate, PermKeysEdit, PermKeysDelete,
				PermEndpointsView,
				PermUsageView, PermLogsView,
			},
		}, true
	}
	return Role{}, false
}

// BuiltinRoles returns every built-in role, in menu order.
func BuiltinRoles() []Role {
	out := make([]Role, 0, 4)
	for _, id := range BuiltinRoleIDs() {
		if role, ok := BuiltinRole(id); ok {
			out = append(out, role)
		}
	}
	return out
}

// PermAll is the wildcard the administrator role carries. A role holding it
// passes every permission check; it is never persisted for custom roles.
const PermAll Permission = "*"

// Role is a console role: a named permission set plus the resource scope those
// permissions apply to.
type Role struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Builtin     bool          `json:"builtin"`
	Scope       ResourceScope `json:"scope"`
	Permissions []Permission  `json:"permissions"`
	CreatedAt   string        `json:"createdAt,omitempty"`
	UpdatedAt   string        `json:"updatedAt,omitempty"`
}

// Has reports whether the role carries perm (or the wildcard).
func (r Role) Has(perm Permission) bool {
	for _, granted := range r.Permissions {
		if granted == perm || granted == PermAll {
			return true
		}
	}
	return false
}

// EffectiveScope normalises an empty scope to the built-in default and maps the
// legacy "granted" scope onto "own" (grants are additive).
func (r Role) EffectiveScope() ResourceScope {
	if r.Scope == ScopeAll || (r.Scope == "" && r.ID == RoleIDAdmin) {
		return ScopeAll
	}
	return ScopeOwn
}
