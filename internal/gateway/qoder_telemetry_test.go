package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// zeroQoderTelemetry clears the counters so one test cannot see another's.
func zeroQoderTelemetry(t *testing.T) {
	t.Helper()
	qoderRisk = qoderRiskStats{}
	qoderInner = qoderInnerErrors{}
	t.Cleanup(func() {
		qoderRisk = qoderRiskStats{}
		qoderInner = qoderInnerErrors{}
	})
}

// Each kind of pushback is counted as what it is: a busy model, a rate limit, an
// empty balance and a version gate are four different problems, and a pool that
// counts them as one looks healthy while it fails.
func TestQoderRiskCountsByKind(t *testing.T) {
	zeroQoderTelemetry(t)
	qoderNoteRefusal(http.StatusServiceUnavailable, "Qoder 已把请求排进队列，请 31s 后重试", 30*time.Second)
	qoderNoteRefusal(http.StatusTooManyRequests, "额度已用完：credits used up", 0)
	qoderNoteRefusal(http.StatusBadRequest, "Qoder 客户端 / 网关版本过低，请升级（430）", 0)
	qoderNoteRefusal(http.StatusBadRequest, "Qoder 拒绝了这个请求（406）：内容不适合", 0)

	risk := qoderRisk.snapshot()
	if risk.Queue != 1 || risk.Rate != 1 || risk.VersionGate != 1 || risk.Rejected != 1 {
		t.Fatalf("counters = %+v, want one of each", risk)
	}
	if risk.LastKind != "rejected" || risk.LastAt == "" || risk.LastDetail == "" {
		t.Fatalf("the last refusal must be remembered: %+v", risk)
	}
}

// An error hidden inside a 200 is counted and classified: a content refusal is
// not something a retry fixes, a throttle is.
func TestQoderInnerErrorKinds(t *testing.T) {
	cases := []struct{ message, want string }{
		{"DataInspectionFailed", "content_policy"},
		{"rate_limit exceeded", "rate_limit"},
		{"invalid_parameter_error", "invalid_request"},
		{"token expired", "auth"},
		{"something else entirely", "other"},
	}
	for _, tc := range cases {
		if got := qoderInnerErrorKind(tc.message); got != tc.want {
			t.Errorf("qoderInnerErrorKind(%q) = %q, want %q", tc.message, got, tc.want)
		}
	}
}

func TestQoderInnerErrorCountsAndWarnsOnce(t *testing.T) {
	zeroQoderTelemetry(t)
	if kind, first := noteQoderInnerError("rate_limit hit"); !first || kind != "rate_limit" {
		t.Fatalf("first of a kind must be reported as new, got %q %v", kind, first)
	}
	if _, first := noteQoderInnerError("rate_limit again"); first {
		t.Fatal("the second of a kind must not be reported as new")
	}
	if _, first := noteQoderInnerError("invalid_parameter_error"); !first {
		t.Fatal("a different kind is new again")
	}
	snapshot := qoderInner.snapshot()
	if snapshot.Total != 3 || snapshot.Kinds["rate_limit"] != 2 || snapshot.Kinds["invalid_request"] != 1 {
		t.Fatalf("counters = %+v", snapshot)
	}
	if len(snapshot.Warned) != 2 {
		t.Fatalf("warned = %v, want the two kinds seen", snapshot.Warned)
	}
}

// A refusal the client sees inside the stream is counted where it happens: no
// HTTP status ever carried it.
func TestQoderStreamCountsAnEnvelopeRefusal(t *testing.T) {
	zeroQoderTelemetry(t)
	reader := newQoderChunkReaderWithTools(strings.NewReader(qoderQueuedFrame(t, 5)), "ultimate", nil)
	if _, err := io.ReadAll(reader); err != nil {
		t.Fatal(err)
	}
	if risk := qoderRisk.snapshot(); risk.Queue != 1 {
		t.Fatalf("the queued refusal was not counted: %+v", risk)
	}
}

// An error carried inside an otherwise normal chunk is counted, and the turn
// still ends on whatever the frame's own status said.
func TestQoderStreamCountsAnInnerError(t *testing.T) {
	zeroQoderTelemetry(t)
	inner := `{"error":{"message":"DataInspectionFailed","type":"content_policy"},"choices":[]}`
	reply := qoderFrame(t, map[string]any{"statusCodeValue": 200, "body": inner})
	reader := newQoderChunkReaderWithTools(strings.NewReader(reply), "ultimate", nil)
	if _, err := io.ReadAll(reader); err != nil {
		t.Fatal(err)
	}
	snapshot := qoderInner.snapshot()
	if snapshot.Total != 1 || snapshot.Kinds["content_policy"] != 1 {
		t.Fatalf("inner counters = %+v", snapshot)
	}
}

// The telemetry endpoint is admin-only and reports both halves.
func TestQoderTelemetryEndpoint(t *testing.T) {
	zeroQoderTelemetry(t)
	server, _ := monitorTestServer(t)
	qoderNoteRefusal(http.StatusTooManyRequests, "额度已用完", 0)

	denied := monitorCall(t, server.handleQoderTelemetry, http.MethodGet, "/__qoder/telemetry", "", nil, monitorUserID())
	if denied.Code != http.StatusForbidden && denied.Code != http.StatusUnauthorized {
		t.Fatalf("a non-admin reached the counters: %d", denied.Code)
	}
	allowed := monitorCall(t, server.handleQoderTelemetry, http.MethodGet, "/__qoder/telemetry", "", nil, monitorAdminID())
	if allowed.Code != http.StatusOK {
		t.Fatalf("admin telemetry = %d body=%s", allowed.Code, allowed.Body.String())
	}
	var view struct {
		Risk struct {
			Rate int `json:"rate"`
		} `json:"risk"`
		InnerErrors struct {
			Total int `json:"total"`
		} `json:"innerErrors"`
		QueueBudget struct {
			Rounds int `json:"rounds"`
		} `json:"queueBudget"`
	}
	if err := json.Unmarshal(allowed.Body.Bytes(), &view); err != nil {
		t.Fatalf("telemetry is not JSON: %v", err)
	}
	if view.Risk.Rate != 1 || view.QueueBudget.Rounds != qoderQueueMaxRounds {
		t.Fatalf("telemetry view = %+v", view)
	}
}

// The probe refuses to run on anything it cannot probe.
func TestQoderQueueDiagValidatesItsInput(t *testing.T) {
	provider := domain.Provider{
		ID: "qoder-diag", Name: "qoder-diag", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypeQoderPAT, QoderPAT: &domain.QoderCredential{UID: "u", AccessToken: "t"},
	}
	other := domain.Provider{
		ID: "plain", Name: "plain", Protocol: domain.ProtocolOpenAIChat, AuthType: "none",
	}
	server, _ := monitorTestServer(t, provider, other)

	missing := monitorCall(t, server.handleQoderQueueDiag, http.MethodPost, "/__qoder/diag/queue", `{}`, nil, monitorAdminID())
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("a probe without a provider = %d, want 400", missing.Code)
	}
	unknown := monitorCall(t, server.handleQoderQueueDiag, http.MethodPost, "/__qoder/diag/queue", `{"providerId":"nope"}`, nil, monitorAdminID())
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("an unknown provider = %d, want 404", unknown.Code)
	}
	notQoder := monitorCall(t, server.handleQoderQueueDiag, http.MethodPost, "/__qoder/diag/queue", `{"providerId":"plain"}`, nil, monitorAdminID())
	if notQoder.Code != http.StatusBadRequest {
		t.Fatalf("a non-Qoder provider = %d, want 400", notQoder.Code)
	}
	badBody := monitorCall(t, server.handleQoderQueueDiag, http.MethodPost, "/__qoder/diag/queue", `not json`, nil, monitorAdminID())
	if badBody.Code != http.StatusBadRequest {
		t.Fatalf("a malformed probe = %d, want 400", badBody.Code)
	}
}

// What the probe reports, and what it tells the operator to do about it.
func TestQoderDiagKindAndAdvice(t *testing.T) {
	cases := []struct {
		status  int
		message string
		wait    time.Duration
		kind    string
		advice  string
	}{
		{http.StatusServiceUnavailable, "Qoder 已把请求排进队列（模型 x），请 31s 后重试", 30 * time.Second, "queued", "排队中"},
		{http.StatusTooManyRequests, "额度已用完：credits used up", 0, "quota_exhausted", "额度已用完"},
		{http.StatusTooManyRequests, "额度已用完（今日次数已达上限，次日恢复）", 0, "daily_limit", "次日恢复"},
		{http.StatusUnauthorized, "Qoder 的登录已失效，请重新登录", 0, "auth", "重新登录"},
		{http.StatusBadRequest, "Qoder 客户端 / 网关版本过低，请升级（430）", 0, "version_gate", "版本"},
		{http.StatusBadGateway, "Qoder：unexpected", 0, "error", "detail"},
	}
	for _, tc := range cases {
		kind := qoderDiagKind(tc.status, tc.message, tc.wait)
		if kind != tc.kind {
			t.Errorf("qoderDiagKind(%d, %q) = %q, want %q", tc.status, tc.message, kind, tc.kind)
		}
		if advice := qoderDiagAdvice(kind, tc.wait); !strings.Contains(advice, tc.advice) {
			t.Errorf("advice for %q = %q, want it to mention %q", kind, advice, tc.advice)
		}
	}
	if advice := qoderDiagAdvice("ok", 0); !strings.Contains(advice, "可用") {
		t.Errorf("advice for a healthy model = %q", advice)
	}
}
