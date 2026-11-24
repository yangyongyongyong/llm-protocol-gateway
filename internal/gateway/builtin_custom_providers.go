package gateway

import (
	"embed"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// 内置连接器随二进制一起发布：升级后所有人都能在「自定义连接器」目录里
// 直接看到，不需要手动导入 .custom-connector.json。脚本正文来自仓库
// connectors/tencent-cloudbase-growth 的 bundle，构建时嵌入程序。
//
//go:embed builtin/tencent_cloudbase_adapter.py
var builtinConnectorScripts embed.FS

const builtinTencentCloudBaseID = "tencent-cloudbase-growth"

func builtinCustomProviders() []domain.CustomProvider {
	source, err := builtinConnectorScripts.ReadFile("builtin/tencent_cloudbase_adapter.py")
	if err != nil {
		// embed 失败只可能是构建期漏文件；此时宁可不注册，也不能注册空脚本
		return nil
	}
	return []domain.CustomProvider{{
		ID:           builtinTencentCloudBaseID,
		Name:         "腾讯云 CloudBase 成长计划",
		Description:  "CloudBase 小程序成长计划：原生 Chat / Responses / Claude 三协议入口，经 aiProxy 云函数和 Node SDK 调用。",
		Protocol:     domain.ProtocolOpenAIChat,
		AuthHeader:   "Authorization",
		DefaultModel: "hy3",
		Builtin:      true,
		Script: domain.ProviderScript{
			Language:              "python",
			Entrypoint:            "adapter.py",
			Source:                string(source),
			HealthPath:            "/health",
			MaxConcurrentRequests: 4,
			RequestTimeoutSec:     150,
		},
	}}
}

// EnsureBuiltinCustomProviders 把内置模板放进状态，已存在的同 ID 项用最新
// 定义覆盖（保留 CreatedAt），这样升级后脚本和描述都会更新。
func EnsureBuiltinCustomProviders(state *domain.GatewayState) {
	now := nowRFC3339()
	for _, builtin := range builtinCustomProviders() {
		found := false
		for index := range state.CustomProviders {
			if state.CustomProviders[index].ID != builtin.ID {
				continue
			}
			builtin.CreatedAt = state.CustomProviders[index].CreatedAt
			if builtin.CreatedAt == "" {
				builtin.CreatedAt = now
			}
			builtin.UpdatedAt = now
			state.CustomProviders[index] = builtin
			found = true
			break
		}
		if found {
			continue
		}
		builtin.CreatedAt = now
		builtin.UpdatedAt = now
		state.CustomProviders = append(state.CustomProviders, builtin)
	}
}

// SyncBuiltinCustomProviderScripts 在每次启动时把内置脚本写到用户数据目录。
// 内置模板不允许修改，所以覆盖是安全的；也让新版本脚本真正生效。
func SyncBuiltinCustomProviderScripts() error {
	var firstErr error
	for _, provider := range builtinCustomProviders() {
		if _, err := writeCustomProviderScriptFiles(provider); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
