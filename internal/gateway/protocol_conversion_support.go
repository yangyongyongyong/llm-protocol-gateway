package gateway

import "github.com/luca/llm-protocol-gateway/internal/domain"

// attemptServesClientProtocol 报告一次 failover 尝试到底能不能把这条请求发出去。
//
// 这里的“能发”比 RouteDecision.Action 更宽一层：即使声明协议与客户端协议不同、
// 决策先写成了 convert，只要这个 provider 的实测 ProtocolSupport 里有客户端协议，
// 调度仍会先尝试同协议直通。不能因为最终兜底转换器缺失，就提前跳过一次本来可以
// 成功的同协议直通。
func attemptServesClientProtocol(decision domain.RouteDecision, clientProtocol domain.Protocol, provider domain.Provider) bool {
	if decision.Action == "pass_through" {
		return true
	}
	if _, ok := nativeProtocolForProvider(provider, clientProtocol); ok {
		return true
	}
	return protocolConversionSupported(clientProtocol, decision.InputProtocol)
}

// protocolConversionSupported 报告「客户端协议 → provider 协议」这一对有没有
// 转换器。
//
// 它和 executeProtocolFlow 里的那张 switch 表是同一份事实：那张表只列了跨协议
// 的组合，两端协议相同的格子一个都没有（同协议走的是 pass_through，不需要转换）。
// 所以这里也不给同协议开例外——调用方问的是"转换器在不在"，不是"请求能不能成"。
// 以前「跨协议备选缺锁定模型」被当成跳过条件，恰好把这张表盖住了；模型兜底放开
// 之后，这里必须自己说清楚，免得每一次失败都往上打一条注定 500 的请求。
func protocolConversionSupported(from, to domain.Protocol) bool {
	switch from {
	case domain.ProtocolGemini:
		return to == domain.ProtocolOpenAIChat || to == domain.ProtocolClaude || to == domain.ProtocolOpenAIResponses
	case domain.ProtocolOpenAIChat:
		return to == domain.ProtocolClaude || to == domain.ProtocolOpenAIResponses
	case domain.ProtocolClaude:
		return to == domain.ProtocolOpenAIChat || to == domain.ProtocolOpenAIResponses
	case domain.ProtocolOpenAIResponses:
		return to == domain.ProtocolOpenAIChat || to == domain.ProtocolClaude
	}
	return false
}
