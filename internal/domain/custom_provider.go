package domain

// CustomProvider 是可复用的自定义协议类型模板。它和具体 Provider 实例分开：
//   - CustomProvider 保存协议、鉴权头名和转换脚本；
//   - Provider 实例只保存自己的上游 URL、Key、模型与调度状态。
//
// 这样用户先定义一次 “ocean”，之后新增 Ocean Provider 时只填 URL/Key，
// 不必重复粘贴脚本，也不会把脚本正文散落在每个实例里。
type CustomProvider struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Protocol 是脚本对网关暴露的标准协议：openai_chat / openai_responses / claude。
	Protocol Protocol `json:"protocol"`
	// AuthHeader 是脚本访问私有上游时使用的鉴权头名，默认 Authorization。
	// 实例的 API Key 会通过 UPSTREAM_API_KEY 注入脚本。
	AuthHeader string `json:"authHeader,omitempty"`
	// DefaultModel 是新建实例时的默认模型。实例仍可在保存后通过模型页或
	// Provider 编辑调整；放在类型上是为了让“只填 URL + Key”也能得到合理默认值。
	DefaultModel string `json:"defaultModel,omitempty"`
	// Script 是脚本模板；其中 UpstreamBaseURL 在实例化时由具体 Provider 填入。
	Script ProviderScript `json:"script"`
	// ScriptDir 是脚本在 App 数据目录中的实际落盘位置（运行时展示字段）。
	ScriptDir string `json:"scriptDir,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}
