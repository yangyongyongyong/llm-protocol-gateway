package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Every refusal a client can see has to say what it is. The three that are not
// about the account carry an English tail, because a client's own classifier
// looks for those words before it looks at the status: without them a busy
// model, an empty balance and a daily cap are all shown as "API key invalid".
func TestQoderFailureExplainsItselfToTheClient(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		payload    string
		wantStatus int
		wantType   string
		contains   string
	}{
		{
			name:       "queue carries a rate-limit hint",
			status:     http.StatusForbidden,
			payload:    `{"code":10605,"isQueued":true,"retryAfterSeconds":20}`,
			wantStatus: http.StatusServiceUnavailable,
			contains:   qoderQueuedHint,
		},
		{
			name:       "credits exhausted",
			status:     http.StatusForbidden,
			payload:    `{"code":112,"message":"credits used up","pricingUrl":"https://qoder.sh/pricing"}`,
			wantStatus: http.StatusTooManyRequests,
			contains:   qoderQuotaHint,
		},
		{
			name:       "daily request cap",
			status:     http.StatusForbidden,
			payload:    `{"code":110,"message":"Billing daily count exceeded"}`,
			wantStatus: http.StatusTooManyRequests,
			contains:   qoderDailyHint,
		},
		{
			// A daily cap answered as a bare 403 is still not a lapsed sign-in.
			name:       "daily cap named only in the message",
			status:     http.StatusForbidden,
			payload:    `{"message":"Billing daily count exceeded"}`,
			wantStatus: http.StatusTooManyRequests,
			contains:   "次日恢复",
		},
		{
			name:       "lapsed session by marker",
			status:     http.StatusForbidden,
			payload:    `{"code":403,"message":"TOKEN_EXPIRE"}`,
			wantStatus: http.StatusUnauthorized,
			contains:   qoderSessionDeadHint,
		},
		{
			name:       "lapsed session by code",
			status:     http.StatusMethodNotAllowed,
			payload:    `{"code":12153,"message":"session gone"}`,
			wantStatus: http.StatusUnauthorized,
			contains:   "重新登录",
		},
		{
			// 418 is Qoder's front end wrapping its own provider failure: the
			// account is fine, so the client is told a transient upstream fault
			// and the failover loop is allowed to move on.
			name:       "provider wrapped as 418",
			status:     qoderTransientStatus,
			payload:    `{"message":"provider_error"}`,
			wantStatus: http.StatusBadGateway,
			contains:   "上游暂时不可用",
		},
		{
			name:       "version gate",
			status:     http.StatusBadRequest,
			payload:    `{"code":430,"message":"version too low"}`,
			wantStatus: http.StatusBadRequest,
			contains:   "版本过低",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, message, _ := qoderFailure(tc.status, []byte(tc.payload))
			if status != tc.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", status, tc.wantStatus, message)
			}
			if !strings.Contains(message, tc.contains) {
				t.Fatalf("message = %q, want it to contain %q", message, tc.contains)
			}
		})
	}
}

// A body the endpoint's proxy would drop is refused locally, and the refusal
// says which kind of failure it is instead of looking like a server fault.
func TestQoderFailureResponseNamesAPayloadRefusal(t *testing.T) {
	response := qoderFailureResponse(http.StatusRequestEntityTooLarge, "请求体过大", 0)
	defer response.Body.Close()
	payload, _ := io.ReadAll(response.Body)
	var envelope struct {
		Error struct {
			Type string `json:"type"`
			Code int    `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("error body is not JSON: %s", payload)
	}
	if envelope.Error.Type != "payload_too_large" || envelope.Error.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("payload refusal wrong: %s", payload)
	}
}

// The English tail is what a client's classifier reads; the Chinese half is
// what its user reads. Both have to be in the same message.
func TestQoderQueuedHintIsEnglishAndTheMessageIsNot(t *testing.T) {
	if !strings.Contains(qoderQueuedHint, "rate limit") {
		t.Fatalf("the hint has to be the phrase a classifier looks for: %q", qoderQueuedHint)
	}
	_, message, _ := qoderFailure(http.StatusForbidden, []byte(`{"code":10605,"isQueued":true,"retryAfterSeconds":5}`))
	if !strings.Contains(message, "请") {
		t.Fatalf("the user-facing half is missing: %q", message)
	}
}
