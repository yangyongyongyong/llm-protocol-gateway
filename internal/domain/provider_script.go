package domain

// ProviderScript 是自定义协议 Provider 的转换脚本。脚本由用户提供，网关只
// 负责落盘、启动、健康检查、停止和测试；它不进入网关协议转换器，也不成为
// 第四种 Protocol。脚本至少把上游私有协议转换成 openai_chat，网关再用现有
// 标准协议转换器接 OpenAI Responses / Claude 客户端。
type ProviderScript struct {
	// Language 目前支持 python；保留字段方便以后增加 node 等运行时。
	Language string `json:"language,omitempty"`
	// Entrypoint 是脚本入口文件名，例如 adapter.py。
	Entrypoint string `json:"entrypoint,omitempty"`
	// Source 是用户提交的脚本正文。脚本不得把私有上游 Key 写死在正文里；
	// Key 由 Provider.APIKeySource 通过 UPSTREAM_API_KEY 环境变量注入。
	Source string `json:"source,omitempty"`
	// UpstreamBaseURL 是私有厂商协议的根地址，由网关作为 UPSTREAM_BASE_URL
	// 注入脚本；Provider.BaseURL 则指向脚本自己的标准协议 HTTP 服务。
	UpstreamBaseURL string `json:"upstreamBaseUrl,omitempty"`
	// Requirements 可选，python 场景下写入 requirements.txt。
	Requirements string `json:"requirements,omitempty"`
	// ListenPort 是脚本应监听的回环端口。0 表示由网关分配。
	ListenPort int `json:"listenPort,omitempty"`
	// StartCommand 可选；为空时根据 Language/Entrypoint 推导。
	StartCommand string `json:"startCommand,omitempty"`
	// HealthPath 是脚本健康检查路径，默认 /health。
	HealthPath string `json:"healthPath,omitempty"`
	// MaxConcurrentRequests 是脚本自身声明的并发上限；网关也会用 Provider
	// 的并发旋钮做第二层准入，二者取小。
	MaxConcurrentRequests int `json:"maxConcurrentRequests,omitempty"`
	// RequestTimeoutSec 是单次上游转换允许的最长等待；0 使用网关默认。
	RequestTimeoutSec int `json:"requestTimeoutSec,omitempty"`
}
