package gateway

import (
	"net/http"
	"testing"
	"time"
)

// The code table's whole point: a model-level refusal must not be read as an
// account-level one.
func TestClassifyWorkBuddyFailurePutsModelCodesOnTheModel(t *testing.T) {
	cases := []struct {
		body string
		want failoverClass
	}{
		{`{"code":6004,"msg":"model rate limited"}`, failoverBusy},
		{`{"code":14003,"msg":"model rate limited"}`, failoverBusy},
		{`{"code":11102,"msg":"request filtered"}`, failoverBusy},
		{`{"code":11115,"msg":"model unavailable"}`, failoverBusy},
		{`{"code":11135,"msg":"unsupported"}`, failoverBusy},
		{`{"code":11148,"msg":"limited"}`, failoverBusy},
		{`{"code":6020,"msg":"upstream busy"}`, failoverBusy},
		{`{"code":6021,"msg":"upstream busy"}`, failoverBusy},
		{`{"code":11128,"msg":"Illegal API invocation from an unapproved channel"}`, failoverPromptRefusal},
		{`{"code":11140,"msg":"unapproved channel"}`, failoverPromptRefusal},
		{`{"code":12153,"msg":"session expired"}`, failoverHard},
		{`{"code":14017,"msg":"payment required"}`, failoverHard},
		{`{"code":14018,"msg":"payment required"}`, failoverHard},
		{`{"code":10085,"msg":"请求不合法"}`, failoverHard},
		// Repaired in the request path, so advancing the chain would only repeat it.
		{`{"code":11101,"msg":"Non-stream chat request is currently not supported"}`, failoverNone},
		{`{"code":11153,"msg":"must end with a user message"}`, failoverNone},
		{`{"code":11159,"msg":"Invalid signature in thinking block"}`, failoverNone},
	}
	for _, testCase := range cases {
		class, reason, ok := classifyWorkBuddyFailure(http.StatusBadRequest, []byte(testCase.body))
		if !ok {
			t.Fatalf("%s: not classified", testCase.body)
		}
		if class != testCase.want {
			t.Fatalf("%s: class = %v (%s), want %v", testCase.body, class, reason, testCase.want)
		}
	}
}

// An unknown code, or no code at all, leaves the generic classifier in charge.
func TestClassifyWorkBuddyFailureLeavesUnknownBodiesAlone(t *testing.T) {
	for _, body := range []string{
		``,
		`{"msg":"something else"}`,
		`{"code":99999,"msg":"unknown"}`,
		`{"error":{"type":"invalid_request_error"}}`,
	} {
		if _, _, ok := classifyWorkBuddyFailure(http.StatusBadRequest, []byte(body)); ok {
			t.Fatalf("%q was classified by the WorkBuddy table", body)
		}
	}
}

// 402 carries no code this table knows, and is still the vendor's spent-plan answer.
func TestClassifyWorkBuddyFailureReadsPaymentRequired(t *testing.T) {
	class, reason, ok := classifyWorkBuddyFailure(http.StatusPaymentRequired, []byte(`{}`))
	if !ok || class != failoverHard {
		t.Fatalf("402: class = %v ok = %v, want hard", class, ok)
	}
	if reason == "" {
		t.Fatal("402: no reason")
	}
}

// Codes arrive as numbers and as strings; both are the same code.
func TestWorkBuddyFailureCodeAcceptsBothForms(t *testing.T) {
	for _, body := range []string{`{"code":6004}`, `{"code":"6004"}`} {
		code, ok := workBuddyFailureCode([]byte(body))
		if !ok || code != 6004 {
			t.Fatalf("%s: code = %d ok = %v, want 6004", body, code, ok)
		}
	}
	if _, ok := workBuddyFailureCode([]byte(`{"code":0}`)); ok {
		t.Fatal("a successful envelope was read as a failure code")
	}
}

// A rest is per provider and per model, expires on its own, and never leaks
// across accounts.
func TestWorkBuddyModelRestsAreScopedAndExpire(t *testing.T) {
	now := time.Now()
	restWorkBuddyModel("provider-a", "glm-5.3", now, now.Add(time.Minute))
	if !workBuddyModelRested("provider-a", "glm-5.3", now) {
		t.Fatal("the rest was not recorded")
	}
	if workBuddyModelRested("provider-b", "glm-5.3", now) {
		t.Fatal("a rest leaked to another provider")
	}
	if workBuddyModelRested("provider-a", "glm-5.2", now) {
		t.Fatal("a rest leaked to another model")
	}
	if !workBuddyModelRested("provider-a", "GLM-5.3", now) {
		t.Fatal("model matching is not case-insensitive")
	}
	if workBuddyModelRested("provider-a", "glm-5.3", now.Add(2*time.Minute)) {
		t.Fatal("an expired rest still reported as rested")
	}
}

// Only a model-level verdict rests a model; an account-level one never does.
func TestNoteWorkBuddyModelRestOnlyForModelLevelCodes(t *testing.T) {
	now := time.Now()
	if !noteWorkBuddyModelRest("provider-r1", "m", http.StatusBadRequest, []byte(`{"code":6004}`), now) {
		t.Fatal("a model-level code did not rest the model")
	}
	if !noteWorkBuddyModelRest("provider-r2", "m", http.StatusBadRequest, []byte(`{"code":6021}`), now) {
		t.Fatal("a busy code did not rest the model")
	}
	for _, body := range []string{`{"code":12153}`, `{"code":11128}`, `{"code":11153}`} {
		if noteWorkBuddyModelRest("provider-r3", "m", http.StatusBadRequest, []byte(body), now) {
			t.Fatalf("%s rested a model; it blames the account or the client shape", body)
		}
	}
	if workBuddyModelRested("provider-r3", "m", now.Add(-time.Minute)) {
		t.Fatal("nothing should be resting")
	}
	if workBuddyModelRested("provider-r3", "m", now) {
		t.Fatal("a non-model code left a rest behind")
	}
}

// The console reads a model's rest state through one accessor.
func TestWorkBuddyModelStateOf(t *testing.T) {
	now := time.Now()
	restWorkBuddyModel("provider-s", "m1", now, now.Add(5*time.Minute))
	state := workBuddyModelStateOf("provider-s", "m1", now)
	if !state.Rested || state.RestTill.IsZero() {
		t.Fatalf("state = %+v, want a rest", state)
	}
	if got := workBuddyModelStateOf("provider-s", "m2", now); got.Rested {
		t.Fatalf("m2 = %+v, want no rest", got)
	}
}
