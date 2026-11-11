package gateway

import (
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// Only a failure the vendor calls temporary is worth another attempt. A business refusal
// fails identically every time, and two more requests a day for nothing is the trap the
// reference implementation calls out.
func TestWorkBuddyCheckinTransientClassification(t *testing.T) {
	cases := []struct {
		name    string
		record  domain.CheckinRecord
		want    bool
		explain string
	}{
		{"busy, chinese", domain.CheckinRecord{Outcome: domain.CheckinFailed, Message: "系统繁忙，请稍后重试"}, true, ""},
		{"processing", domain.CheckinRecord{Outcome: domain.CheckinFailed, Message: "请求处理中"}, true, ""},
		{"english", domain.CheckinRecord{Outcome: domain.CheckinFailed, Message: "Temporarily Unavailable"}, true, ""},
		{"5xx", domain.CheckinRecord{Outcome: domain.CheckinFailed, Message: "gateway answered HTTP 502"}, true, ""},
		// Answers, not failures.
		{"done", domain.CheckinRecord{Outcome: domain.CheckinDone}, false, ""},
		{"claimed", domain.CheckinRecord{Outcome: domain.CheckinClaimed}, false, ""},
		{"ineligible", domain.CheckinRecord{Outcome: domain.CheckinIneligible, Message: "请稍后重试"}, false, "an answer stays an answer"},
		{"own app", domain.CheckinRecord{Outcome: domain.CheckinOwnApp}, false, ""},
		// A business refusal that happens to be a failure.
		{"enterprise refusal", domain.CheckinRecord{Outcome: domain.CheckinFailed, Message: "不支持该操作"}, false, "retrying cannot fix it"},
		{"bad parameter", domain.CheckinRecord{Outcome: domain.CheckinFailed, Message: "invalid parameter"}, false, ""},
		// Offline has its own minute-by-minute loop; queueing it here would double traffic.
		{"offline", domain.CheckinRecord{Outcome: domain.CheckinFailed, Offline: true, Message: "请稍后重试"}, false, ""},
		// No message is not evidence.
		{"empty message", domain.CheckinRecord{Outcome: domain.CheckinFailed}, false, ""},
		{"no outcome", domain.CheckinRecord{}, false, ""},
	}
	for _, testCase := range cases {
		got := workBuddyCheckinTransient(testCase.record)
		if got != testCase.want {
			t.Fatalf("%s: got %v, want %v (%s)", testCase.name, got, testCase.want, testCase.explain)
		}
	}
}

// A second queue for the same account is a no-op: the first already covers the day, and a
// duplicate would race the check-in lock.
func TestWorkBuddyCheckinRetryQueueIsIdempotent(t *testing.T) {
	providerID := "provider-retry-dedupe"
	workBuddyCheckinRetries.Delete(providerID)
	if _, loaded := workBuddyCheckinRetries.LoadOrStore(providerID, struct{}{}); loaded {
		t.Fatal("the guard was already set")
	}
	// While the guard is held, queueing again must not start a second worker.
	server := &Server{}
	server.queueWorkBuddyCheckinRetry(providerID)
	if _, running := workBuddyCheckinRetries.Load(providerID); !running {
		t.Fatal("the original guard disappeared")
	}
	workBuddyCheckinRetries.Delete(providerID)
}

// An empty provider id is never queued.
func TestWorkBuddyCheckinRetryIgnoresEmptyID(t *testing.T) {
	server := &Server{}
	server.queueWorkBuddyCheckinRetry("   ")
	if _, running := workBuddyCheckinRetries.Load(""); running {
		t.Fatal("an empty id was queued")
	}
}
