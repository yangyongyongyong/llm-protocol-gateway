package gateway

import (
	"encoding/json"
	"testing"
	"time"
)

// A completed poll answer is parsed field for field, including the credit the account
// actually paid.
func TestParseWorkBuddyVideoTaskCompleted(t *testing.T) {
	payload := []byte(`{
		"id":"1312831544-WandVideo-ab12","status":"completed",
		"created_at":1793801279,"completed_at":1793801399,
		"data":[{"url":"https://cos.example/a_0.mp4?q-signature=x"}],
		"size":"768P",
		"usage":{"output_video_duration":4,"output_video_tokens":200000,
		         "output_video_coefficient":1,"credit":57.14}}`)
	task := parseWorkBuddyVideoTask(payload)
	if task.ID != "1312831544-WandVideo-ab12" || task.Status != "completed" {
		t.Fatalf("task = %+v", task)
	}
	if !task.Done() || !task.Succeeded() {
		t.Fatalf("completed should be a successful terminal state: %+v", task)
	}
	if len(task.URLs) != 1 || task.URLs[0] == "" {
		t.Fatalf("urls = %v", task.URLs)
	}
	if task.Size != "768P" || task.Duration != 4 || task.Tokens != 200000 || task.Credit != 57.14 {
		t.Fatalf("fields = %+v", task)
	}
	if task.CreatedAt == 0 || task.CompletedAt == 0 {
		t.Fatalf("timestamps = %+v", task)
	}
}

// The vendor's wordings differ between models: kling omits `size` and answers the
// address at the top level, and a failure carries its reason in `msg`/`error`.
func TestParseWorkBuddyVideoTaskToleratesTheOtherShapes(t *testing.T) {
	kling := []byte(`{"id":"t1","status":"running","video_url":"https://cos.example/v.mp4"}`)
	task := parseWorkBuddyVideoTask(kling)
	if task.Done() {
		t.Fatal("running is not terminal")
	}
	if len(task.URLs) != 1 || task.URLs[0] != "https://cos.example/v.mp4" {
		t.Fatalf("top-level video_url was not read: %v", task.URLs)
	}

	failed := []byte(`{"id":"t2","status":"failed","error":{"message":"prompt refused"}}`)
	task = parseWorkBuddyVideoTask(failed)
	if !task.Done() || task.Succeeded() {
		t.Fatalf("failed should be terminal and unsuccessful: %+v", task)
	}
	if task.ErrorText != "prompt refused" {
		t.Fatalf("error text = %q", task.ErrorText)
	}

	// Historical spellings count as a successful terminal state.
	for _, status := range []string{"succeeded", "success"} {
		if !parseWorkBuddyVideoTask([]byte(`{"status":"` + status + `"}`)).Succeeded() {
			t.Fatalf("%s should count as succeeded", status)
		}
	}

	// Garbage is zero-valued, never a panic.
	if task := parseWorkBuddyVideoTask([]byte("not json")); task.Status != "" {
		t.Fatalf("garbage parsed into %+v", task)
	}
}

// A submit answer names the task at the top level, which is what the client polls with.
func TestParseWorkBuddyVideoSubmit(t *testing.T) {
	id, status := parseWorkBuddyVideoSubmit([]byte(`{"id":"task-1","status":"queued"}`))
	if id != "task-1" || status != "queued" {
		t.Fatalf("id=%q status=%q", id, status)
	}
	if id, _ := parseWorkBuddyVideoSubmit([]byte(`{"task_id":"task-2"}`)); id != "task-2" {
		t.Fatalf("task_id fallback failed: %q", id)
	}
	if id, _ := parseWorkBuddyVideoSubmit([]byte("garbage")); id != "" {
		t.Fatalf("garbage gave %q", id)
	}
}

// A task can only be read by the account that submitted it, so the ledger has to
// remember which account that was — and forget it after the signed URL expires.
func TestWorkBuddyVideoLedgerIsScopedAndExpires(t *testing.T) {
	now := time.Now()
	rememberWorkBuddyVideoTask("task-ledger-1", "provider-v1", "minimax-video-h3")

	providerID, ok := workBuddyVideoTaskProvider("task-ledger-1", now)
	if !ok || providerID != "provider-v1" {
		t.Fatalf("ledger = %q ok=%v", providerID, ok)
	}
	if _, ok := workBuddyVideoTaskProvider("task-unknown", now); ok {
		t.Fatal("an unknown task resolved to an account")
	}
	// Past the TTL the task is forgotten rather than read on a stale account.
	if _, ok := workBuddyVideoTaskProvider("task-ledger-1", now.Add(workBuddyVideoLedgerTTL+time.Minute)); ok {
		t.Fatal("an expired task still resolved")
	}
	// An empty id is never recorded.
	rememberWorkBuddyVideoTask("", "provider-v1", "m")
	if _, ok := workBuddyVideoTaskProvider("", now); ok {
		t.Fatal("an empty id resolved")
	}
}

// The poll body is the shape the tasks route expects.
func TestWorkBuddyVideoTaskBody(t *testing.T) {
	body := workBuddyVideoTaskBody("task-9")
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if payload["task_id"] != "task-9" {
		t.Fatalf("body = %s", string(body))
	}
}

// The ledger is written from the submit answer, so a client polling with the id it was
// given finds its account.
func TestRecordWorkBuddyVideoTaskFromResponse(t *testing.T) {
	recordWorkBuddyVideoTaskFromResponse("provider-v2", "kling-video-v3",
		[]byte(`{"id":"task-from-response","status":"queued"}`))
	providerID, ok := workBuddyVideoTaskProvider("task-from-response", time.Now())
	if !ok || providerID != "provider-v2" {
		t.Fatalf("ledger = %q ok=%v", providerID, ok)
	}
	// A refusal carries no id and must not be recorded.
	recordWorkBuddyVideoTaskFromResponse("provider-v2", "m", []byte(`{"code":14407,"msg":"bad duration"}`))
	if _, ok := workBuddyVideoTaskProvider("", time.Now()); ok {
		t.Fatal("a refusal was recorded as a task")
	}
}

// The model is read off the body for the ledger.
func TestBodyModelOf(t *testing.T) {
	if got := bodyModelOf([]byte(`{"model":"minimax-video-h3","prompt":"x"}`)); got != "minimax-video-h3" {
		t.Fatalf("model = %q", got)
	}
	if got := bodyModelOf([]byte("garbage")); got != "" {
		t.Fatalf("garbage gave %q", got)
	}
}
