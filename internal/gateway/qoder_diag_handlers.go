package gateway

// The console's view of what Qoder has been saying.
//
// Two endpoints, both admin-only:
//
//   GET  /__qoder/telemetry     how often the vendor pushed back, by kind, plus
//                               the errors it hid inside a 200
//   POST /__qoder/diag/queue    probe one model and report whether it is queued,
//                               so an operator can tell a busy model from a
//                               broken account without guessing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// handleQoderTelemetry reports the counters.
func (s *Server) handleQoderTelemetry(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"risk":        qoderRisk.snapshot(),
		"innerErrors": qoderInner.snapshot(),
		"queueBudget": map[string]any{
			"rounds":         qoderQueueMaxRounds,
			"waitMaxSecs":    int(qoderQueueWaitMax.Seconds()),
			"budgetSecs":     int(qoderQueueBudget.Seconds()),
			"singleWaitSecs": int(qoderQueueSingleWait.Seconds()),
		},
	})
}

// qoderQueueDiagRequest names what to probe.
type qoderQueueDiagRequest struct {
	ProviderID string `json:"providerId"`
	Model      string `json:"model"`
}

// handleQoderQueueDiag sends one tiny request and reports what came back.
//
// The point is to answer "is this model queued right now, or is this account
// broken?" without waiting for a user to hit it: a queue is the model being busy
// and no amount of account juggling fixes it, while a refused credential looks
// completely different once you ask it a question.
func (s *Server) handleQoderQueueDiag(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	var request qoderQueueDiagRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&request); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "请求体不是合法 JSON："+err.Error())
		return
	}
	providerID := strings.TrimSpace(request.ProviderID)
	if providerID == "" {
		writeOpenAIError(w, http.StatusBadRequest, "请指定要探测的 providerId")
		return
	}
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, "找不到该 Provider")
		return
	}
	if !isQoderProvider(provider) {
		writeOpenAIError(w, http.StatusBadRequest, "该 Provider 不是 Qoder")
		return
	}
	model := strings.TrimSpace(request.Model)
	if model == "" {
		model = strings.TrimSpace(provider.DefaultModel)
	}
	started := time.Now()
	view := map[string]any{
		"providerId": provider.ID,
		"provider":   provider.Name,
		"model":      model,
		"realm":      qoderSiteOf(provider).Name,
	}
	if provider.QoderPAT != nil {
		// Which device identity the probe is signed with: a stored one came from
		// the sign-in, a derived one is computed from the uid.
		if strings.TrimSpace(provider.QoderPAT.MachineID) != "" {
			view["identitySource"] = "stored"
		} else {
			view["identitySource"] = "derived"
		}
	}
	// A one-turn probe: the smallest request the endpoint will answer.
	body, marshalErr := json.Marshal(map[string]any{
		"model":      model,
		"stream":     false,
		"max_tokens": 8,
		"messages":   []any{map[string]any{"role": "user", "content": "ping"}},
	})
	if marshalErr != nil {
		writeOpenAIError(w, http.StatusInternalServerError, marshalErr.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	response, sendErr := s.sendQoderChat(ctx, provider, model, body)
	if sendErr != nil {
		view["ok"] = false
		view["kind"] = "error"
		view["detail"] = sendErr.Error()
		view["elapsedMs"] = time.Since(started).Milliseconds()
		view["advice"] = qoderDiagAdvice("error", 0)
		writeJSON(w, http.StatusOK, view)
		return
	}
	defer response.Body.Close()
	view["status"] = response.StatusCode
	view["elapsedMs"] = time.Since(started).Milliseconds()
	if response.StatusCode/100 == 2 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		view["ok"] = true
		view["kind"] = "ok"
		view["advice"] = qoderDiagAdvice("ok", 0)
		writeJSON(w, http.StatusOK, view)
		return
	}
	payload, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	_, message, wait := qoderFailure(response.StatusCode, payload)
	kind := qoderDiagKind(response.StatusCode, message, wait)
	view["ok"] = false
	view["kind"] = kind
	view["detail"] = message
	if wait > 0 {
		view["retryAfterSeconds"] = int(wait.Seconds())
	}
	view["advice"] = qoderDiagAdvice(kind, wait)
	writeJSON(w, http.StatusOK, view)
}

// qoderDiagKind names what the probe found.
func qoderDiagKind(status int, message string, wait time.Duration) string {
	switch {
	case wait > 0 || strings.Contains(message, "排进队列"):
		return "queued"
	case strings.Contains(message, "版本过低"):
		return "version_gate"
	case strings.Contains(message, "次日恢复"):
		return "daily_limit"
	case strings.Contains(message, "额度已用完"):
		return "quota_exhausted"
	case status == http.StatusUnauthorized:
		return "auth"
	}
	return "error"
}

// qoderDiagAdvice is what an operator should do about it.
func qoderDiagAdvice(kind string, wait time.Duration) string {
	switch kind {
	case "ok":
		return "模型可用，请求会正常返回。"
	case "queued":
		seconds := int(wait.Seconds())
		if seconds <= 0 {
			seconds = 30
		}
		return fmt.Sprintf("上游模型排队中（10605 / RESOURCE_LIMIT）：这与你账号的凭证和 Key 都无关，官方客户端同一时刻也可能排队。"+
			"网关会在原地等（默认最多 %d 轮、共 %d 秒），约 %d 秒后重试通常就放行；急用可换模型或换区域。",
			qoderQueueMaxRounds, int(qoderQueueBudget.Seconds()), seconds)
	case "quota_exhausted":
		return "账号额度已用完：换一个账号，或等额度恢复。再重试同一个账号不会有不同结果。"
	case "daily_limit":
		return "账号今日请求次数已达上限：次日恢复，或换一个账号。"
	case "version_gate":
		return "上游要求更高的客户端版本：把 qoderCOSYVersion 对齐到官方客户端当前版本后重试。"
	case "auth":
		return "登录已失效：重新登录该账号。"
	}
	return "上游返回了别的原因，先看 detail 再决定是换号还是改请求。"
}
