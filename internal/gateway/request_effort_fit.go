package gateway

import "strings"

// Claude Code 与各家 Chat 上游对「思考档位」的词汇并不统一：Claude 说
// low/medium/high/xhigh/max，GLM-5.3 只认 low/high/max，DeepSeek-V4-Pro 只
// 认 high/max。直接透传一个模型不认的档位，上游不会报错（实测 2026-10-28
// 对 copilot.tencent.com 六个档位全部 200），而是悄悄按自己最近的一档跑，
// 于是用户选了 max、实际却和 medium 差不多。qoderFitEffort 就是做这件事，
// 这里把它的规则复用到所有 OpenAI-Chat 上游（magpie 的 fitEffort）。

// modelEffortLevels 返回一个模型已知的思考档位，由低到高；未知返回 nil。
//
// 档位未知的模型一律原样透传（magpie 的约定）：没有可靠来源时把 max 压成
// high 等于替用户省钱，但那是客户端没要求的降级。
func modelEffortLevels(model string) []string {
	id := normalizeModelContextID(model)
	switch {
	case strings.Contains(id, "glm-5.3-flash"):
		return []string{"low", "high", "max"}
	case strings.Contains(id, "glm-5.3"), strings.Contains(id, "glm-5.2"):
		return []string{"low", "high", "max"}
	case strings.Contains(id, "deepseek-v4-pro"):
		return []string{"high", "max"}
	case strings.Contains(id, "deepseek"), strings.Contains(id, "kimi"), strings.Contains(id, "moonshot"):
		return []string{"low", "high", "max"}
	}
	// Claude 的档位取决于哪一代，取值照 magpie 的真机表
	// (internal/agent/claude_effort_test.go:112-124)：
	//   opus-4-5          → low medium high
	//   opus/sonnet-4-6   → low medium high max（没有 xhigh）
	//   opus-4-7 及之后    → low medium high xhigh max
	// opus-5-5 / sonnet-5-5 / fable-5-1 都在最后一行，xhigh 与 max 都合法。
	// 早先这里按「只能自适应思考」把 opus-5-5 封到 high，与真机不符：那等于替
	// 用户降档，客户端明确要的 max 送不上去。
	if rule, known := claudeThinkingOffOf(model); known {
		switch rule {
		case claudeOffNever, claudeOffBetweenTools:
			return []string{"low", "medium", "high", "xhigh", "max"}
		default:
			return []string{"low", "medium", "high", "xhigh", "max"}
		}
	}
	return nil
}

// effortRankOrder 把各家的档位名字从弱到强排开。
var effortRankOrder = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}

// fitModelEffort 把想要的档位对齐到模型已知的档位，取最近的一档，距离相同
// 时往上取；模型档位未知、或想要的档位本身就是模型认得的，原样返回。
func fitModelEffort(want, model string) string {
	want = normalizeReasoningEffort(want)
	if want == "" {
		return ""
	}
	levels := modelEffortLevels(model)
	if len(levels) == 0 {
		return want
	}
	for _, level := range levels {
		if level == want {
			return want
		}
	}
	at := effortRankIndex(want)
	if at < 0 {
		// 不认识的档位名：模型说不出它对应哪一档，交给上游。
		return want
	}
	best, distance := want, len(effortRankOrder)
	for _, level := range levels {
		index := effortRankIndex(level)
		if index < 0 || level == "none" {
			continue
		}
		gap := index - at
		if gap < 0 {
			gap = -gap
		}
		if gap < distance || (gap == distance && index > at) {
			best, distance = level, gap
		}
	}
	return best
}

func effortRankIndex(level string) int {
	for index, known := range effortRankOrder {
		if known == level {
			return index
		}
	}
	return -1
}

// claudePayloadEffort 是 Claude Messages 请求里客户端要求的思考档位：
// output_config.effort 优先，其次 thinking.effort（部分中转把它写在 thinking
// 里），最后按 budget_tokens 反推最近的档位。没有要思考时返回空。
//
// Claude Code 的「思考深度」发的是 thinking.type=adaptive + output_config.effort，
// 不带预算；只有预算的请求按 reasoningEffortBudgetTokens 的分档反推。
func claudePayloadEffort(payload map[string]any) string {
	thinking, _ := payload["thinking"].(map[string]any)
	switch strings.ToLower(strings.TrimSpace(stringValue(thinking["type"]))) {
	case "disabled", "between_tools":
		return ""
	}
	if effort := normalizeReasoningEffort(outputConfigEffort(payload)); effort != "" && effort != "none" {
		return effort
	}
	if effort := normalizeReasoningEffort(stringValue(thinking["effort"])); effort != "" && effort != "none" {
		return effort
	}
	if budget := int64FromAny(thinking["budget_tokens"]); budget > 0 {
		return effortFromBudget(int(budget))
	}
	return ""
}

// effortFromBudget 是 budget_tokens 落到最近档位（按 reasoningEffortBudgetTokens
// 的分界反推）。
func effortFromBudget(budget int) string {
	switch {
	case budget <= 0:
		return ""
	case budget <= reasoningEffortBudgetTokens("low"):
		return "low"
	case budget <= reasoningEffortBudgetTokens("medium"):
		return "medium"
	case budget <= reasoningEffortBudgetTokens("high"):
		return "high"
	}
	return "max"
}

// applyClaudeEffort 把请求的思考档位改写成 effort，按目标模型的档位对齐后
// 写回 Claude 的形状：自适应模型写 output_config.effort，老式预算模型写
// thinking.budget_tokens。只改本来就在思考的请求：关掉思考的、或压根没提
// 思考的（Claude Code 的会话标题/分类请求就是后者）都不动，凭空开思考会
// 让它们答不出正文。
//
// 返回是否改写了 payload。
func applyClaudeEffort(payload map[string]any, model, effort string) bool {
	if payload == nil {
		return false
	}
	thinking, _ := payload["thinking"].(map[string]any)
	kind := strings.ToLower(strings.TrimSpace(stringValue(thinking["type"])))
	if kind != "enabled" && kind != "adaptive" {
		return false
	}
	want := fitModelEffort(effort, model)
	if want == "" {
		return false
	}
	if claudeModelRequiresAdaptiveThinking(model) || strings.EqualFold(stringValue(thinking["type"]), "adaptive") {
		payload["thinking"] = map[string]any{"type": "adaptive"}
		setOutputConfigEffort(payload, want)
		return true
	}
	budget := claudeThinkingBudget(payload, reasoningEffortBudgetTokens(want))
	if maxTokens, ok := intFromAny(payload["max_tokens"]); ok && maxTokens > 0 && budget >= maxTokens {
		// 预算必须给正文留位置；压不出合法值就别动。
		return false
	}
	if budget < thinkingBudgetFloor {
		return false
	}
	payload["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
	return true
}
