package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Thinking 签名整流器（借鉴 cc-switch Claude Rectifier, PR #595）。
//
// 背景：Claude 扩展思考（extended thinking）产生的 thinking / redacted_thinking
// 块带有 Anthropic 用账号私钥签发的 signature。跨账号 / 跨 Provider 故障转移
// （如 claude→glm→claude）后，客户端会把上一账号生成的历史 thinking 块连同旧
// 签名回传，新账号校验失败 → HTTP 400 "Invalid `signature` in `thinking` block"。
//
// 网关侧拿不到签名私钥，无法伪造合法签名，唯一可行方案是：捕获该类 400 后，
// 剥离历史 thinking / redacted_thinking 块及残留 signature 字段，对同一 Provider
// 重试一次。历史轮次的 thinking 只是"草稿纸"，模型下一轮不会重新阅读它来推理，
// 真正的上下文（最终 text 回答 / tool_use / tool_result）全部保留，故对有效上
// 下文零损失。

// thinkingRectifyResult 记录一次整流实际改动了什么，用于判断是否值得重试。
type thinkingRectifyResult struct {
	applied                       bool
	removedThinkingBlocks         int
	removedRedactedThinkingBlocks int
	removedSignatureFields        int
	removedTopLevelThinking       bool
}

// shouldRectifyThinkingSignature 从上游响应体判断是否命中"thinking 签名类"错误。
// 兼容纯文本错误、Anthropic JSON 错误体以及第三方网关常见的嵌套 JSON（把
// Anthropic 原始错误再包一层 message 字符串）。判断在小写归一化后做子串匹配。
func shouldRectifyThinkingSignature(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	lower := strings.ToLower(string(body))

	// 场景1：thinking 块中的签名无效
	// 例："messages.1.content.0: Invalid `signature` in `thinking` block"
	if strings.Contains(lower, "invalid") &&
		strings.Contains(lower, "signature") &&
		strings.Contains(lower, "thinking") &&
		strings.Contains(lower, "block") {
		return true
	}
	// 场景2：assistant 消息必须以 thinking 块开头
	// 例："a final `assistant` message must start with a thinking block"
	if strings.Contains(lower, "must start with a thinking block") {
		return true
	}
	// 场景3：期望 thinking/redacted_thinking，却是别的块类型
	// 例："Expected `thinking` or `redacted_thinking`, but found `tool_use`."
	if strings.Contains(lower, "expected") &&
		(strings.Contains(lower, "thinking") || strings.Contains(lower, "redacted_thinking")) &&
		strings.Contains(lower, "found") {
		return true
	}
	// 场景4：signature 字段必需但缺失
	// 例："...signature: Field required"
	if strings.Contains(lower, "signature") && strings.Contains(lower, "field required") {
		return true
	}
	return false
}

// rectifyClaudeThinkingRequest 对已解析的 Claude 请求体做最小侵入整流（原地修改）：
//   - 删除 messages[*].content 中的 thinking / redacted_thinking 块
//   - 删除其它块上残留的 signature 字段
//   - 特定条件下删除顶层 thinking 字段（thinking=enabled 且最后一条 assistant
//     消息首块不是 thinking/redacted_thinking，但含 tool_use，此时保留顶层
//     thinking 会触发 "must start with a thinking block"）
//
// 只处理带 content 数组的消息；字符串 content 无 thinking 块，天然跳过。
func rectifyClaudeThinkingRequest(req map[string]any) thinkingRectifyResult {
	var result thinkingRectifyResult
	if req == nil {
		return result
	}
	messages, ok := req["messages"].([]any)
	if !ok {
		return result
	}

	for _, item := range messages {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		blocks, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		newBlocks := make([]any, 0, len(blocks))
		modified := false
		for _, blockItem := range blocks {
			block, ok := blockItem.(map[string]any)
			if !ok {
				newBlocks = append(newBlocks, blockItem)
				continue
			}
			switch stringValue(block["type"]) {
			case "thinking":
				result.removedThinkingBlocks++
				modified = true
				continue
			case "redacted_thinking":
				result.removedRedactedThinkingBlocks++
				modified = true
				continue
			}
			if _, has := block["signature"]; has {
				delete(block, "signature")
				result.removedSignatureFields++
				modified = true
			}
			newBlocks = append(newBlocks, block)
		}
		if modified {
			result.applied = true
			msg["content"] = newBlocks
		}
	}

	if shouldRemoveTopLevelThinking(req, messages) {
		delete(req, "thinking")
		result.applied = true
		result.removedTopLevelThinking = true
	}
	return result
}

// shouldRemoveTopLevelThinking 判断是否需要删除顶层 thinking 字段：thinking 已启用
// 且最后一条 assistant 消息首块不是 thinking/redacted_thinking，但包含 tool_use。
func shouldRemoveTopLevelThinking(req map[string]any, messages []any) bool {
	thinking, ok := req["thinking"].(map[string]any)
	if !ok {
		return false
	}
	if stringValue(thinking["type"]) != "enabled" {
		return false
	}

	// 找到最后一条 assistant 消息。
	var lastAssistant map[string]any
	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		if stringValue(msg["role"]) == "assistant" {
			lastAssistant = msg
			break
		}
	}
	if lastAssistant == nil {
		return false
	}
	blocks, ok := lastAssistant["content"].([]any)
	if !ok || len(blocks) == 0 {
		return false
	}
	first, ok := blocks[0].(map[string]any)
	if !ok {
		return false
	}
	firstType := stringValue(first["type"])
	if firstType == "thinking" || firstType == "redacted_thinking" {
		return false
	}
	for _, blockItem := range blocks {
		block, ok := blockItem.(map[string]any)
		if ok && stringValue(block["type"]) == "tool_use" {
			return true
		}
	}
	return false
}

// rectifyClaudeThinkingBody 是 []byte 便捷封装：解析 → 整流 → 重新编码。
// 供已持有原始字节而非 map 的调用点使用。
func rectifyClaudeThinkingBody(body []byte) ([]byte, thinkingRectifyResult) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body, thinkingRectifyResult{}
	}
	result := rectifyClaudeThinkingRequest(req)
	if !result.applied {
		return body, result
	}
	rectified, err := json.Marshal(req)
	if err != nil {
		return body, thinkingRectifyResult{}
	}
	return rectified, result
}

// dumpThinkingRectify writes a full (un-truncated) diagnostic bundle for one
// rectify+retry cycle when the LPG_DEBUG_THINKING_DUMP env var points at a
// directory. It captures the original request body, the rectified body, the
// upstream 400 error body, and the retry outcome (status + body). This is a
// temporary self-verification aid: with the env var unset it is a complete
// no-op with zero overhead. Reading the retry response body here is safe
// because the body is restored before returning so passthrough still works.
func dumpThinkingRectify(providerID string, original, rectified, errBody []byte, retryResp *http.Response, retryErr error, result thinkingRectifyResult) {
	dir := strings.TrimSpace(os.Getenv("LPG_DEBUG_THINKING_DUMP"))
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}

	retryStatus := 0
	var retryBody []byte
	if retryResp != nil {
		body, _ := io.ReadAll(retryResp.Body)
		retryResp.Body.Close()
		retryResp.Body = io.NopCloser(bytes.NewReader(body))
		retryStatus = retryResp.StatusCode
		retryBody = body
	}

	retryErrStr := ""
	if retryErr != nil {
		retryErrStr = retryErr.Error()
	}

	bundle := map[string]any{
		"time":        time.Now().Format(time.RFC3339Nano),
		"provider_id": providerID,
		"rectify_result": map[string]any{
			"applied":                          result.applied,
			"removed_thinking_blocks":          result.removedThinkingBlocks,
			"removed_redacted_thinking_blocks": result.removedRedactedThinkingBlocks,
			"removed_signature_fields":         result.removedSignatureFields,
			"removed_top_level_thinking":       result.removedTopLevelThinking,
		},
		"upstream_400_body": string(errBody),
		"retry_status":      retryStatus,
		"retry_error":       retryErrStr,
		"retry_body":        string(retryBody),
		"original_request":  json.RawMessage(original),
		"rectified_request": json.RawMessage(rectified),
	}
	encoded, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return
	}
	name := filepath.Join(dir, fmt.Sprintf("rectify-%d.json", time.Now().UnixNano()))
	_ = os.WriteFile(name, encoded, 0o644)
}
