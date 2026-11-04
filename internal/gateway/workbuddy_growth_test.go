package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// workBuddyTestSite points the Chinese build's endpoints at one test server and
// restores them afterwards: the growth surface lives on the web host, so both
// the web host and the API root have to move.
func workBuddyTestSite(t *testing.T, server *httptest.Server) workBuddySite {
	t.Helper()
	previousEndpoint, previousWeb := workBuddyCNEndpoint, workBuddyCNWebBase
	previousChat := workBuddyCNChatEndpoint
	workBuddyCNEndpoint, workBuddyCNWebBase = server.URL, server.URL
	workBuddyCNChatEndpoint = server.URL
	t.Cleanup(func() {
		workBuddyCNEndpoint, workBuddyCNWebBase = previousEndpoint, previousWeb
		workBuddyCNChatEndpoint = previousChat
	})
	return workBuddyCNSite
}

// shrinkWorkBuddyPacing makes every gap in a run negligible, so a test exercises
// the flow rather than the pacing.
func shrinkWorkBuddyPacing(t *testing.T) {
	t.Helper()
	previousGap, previousMP, previousMPAction := workBuddyReportGap, workBuddyMPEventGap, workBuddyMPActionGap
	previousTemplate, previousAppearance, previousPoll := workBuddyTemplateGap, workBuddyAppearanceGap, workBuddyClaimPollGap
	workBuddyReportGap = time.Millisecond
	workBuddyMPEventGap = time.Millisecond
	workBuddyMPActionGap = time.Millisecond
	workBuddyTemplateGap = time.Millisecond
	workBuddyAppearanceGap = time.Millisecond
	workBuddyClaimPollGap = time.Millisecond
	workBuddyClaimPollAttempts = 1
	t.Cleanup(func() {
		workBuddyReportGap, workBuddyMPEventGap, workBuddyMPActionGap = previousGap, previousMP, previousMPAction
		workBuddyTemplateGap, workBuddyAppearanceGap, workBuddyClaimPollGap = previousTemplate, previousAppearance, previousPoll
		workBuddyClaimPollAttempts = 3
	})
}

func workBuddyTestCredential() domain.WorkBuddyCredential {
	return domain.WorkBuddyCredential{AccessToken: "at", RefreshToken: "rt", UserID: "u-1", AccountLabel: "lxw"}
}

// TestWorkBuddyParseTasks covers the list shapes the vendor sends: progress as
// an object, flat, and not at all, plus the claimed/full/locked flags the
// executor's decisions hang on.
func TestWorkBuddyParseTasks(t *testing.T) {
	raw := json.RawMessage(`{"tasks":[
		{"task_code":"chat_5","title":"对话 5 次","reward_credit":100,"reward_energy":5,
		 "progress":{"current":3,"target":5},"accept_status":"accepted","locked":false},
		{"task_code":"first_buddy","reward_credit":"300","current":1,"target":1,"accept_status":"completed"},
		{"task_code":"template_5","reward_credit":100,"current":5,"target":5,"accept_status":"claimed"},
		{"task_code":"Sequential_Tasks_1","reward_credit":100,"locked":true},
		{"code":"global_task","reward_credit":0}
	]}`)
	tasks, err := workBuddyParseTasks(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(tasks) != 5 {
		t.Fatalf("got %d tasks, want 5", len(tasks))
	}
	chat := tasks[0]
	if chat.Current != 3 || chat.Target != 5 || !chat.ProgressKnown {
		t.Fatalf("nested progress = %+v", chat)
	}
	if chat.Claimable {
		t.Fatal("3/5 must not be claimable")
	}
	if !chat.Automatable || chat.Action == "" {
		t.Fatalf("chat_5 should be automatable: %+v", chat)
	}
	if buddy := tasks[1]; buddy.Credit != 300 || !buddy.Claimable {
		t.Fatalf("flat progress / string credit = %+v", buddy)
	}
	if claimed := tasks[2]; !claimed.Claimed || claimed.Claimable {
		t.Fatalf("claimed task = %+v", claimed)
	}
	if locked := tasks[3]; !locked.Locked || !locked.MP {
		t.Fatalf("locked mp task = %+v", locked)
	}
	if global := tasks[4]; global.TaskCode != "global_task" || global.ProgressKnown {
		t.Fatalf("global task = %+v", global)
	}
	if global := tasks[4]; global.Automatable {
		t.Fatalf("an unknown task must not claim to be automatable: %+v", global)
	}
}

// TestWorkBuddyTaskFlow drives list → accept → claim against a server that
// answers the three endpoints, and checks what the claim reports.
func TestWorkBuddyTaskFlow(t *testing.T) {
	var mu sync.Mutex
	var accepted []string
	var claimCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/v2/activity/growth/tasks") && r.Method == http.MethodGet:
			if got := r.Header.Get("X-User-Id"); got != "u-1" {
				t.Errorf("list X-User-Id = %q", got)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer at" {
				t.Errorf("list Authorization = %q", got)
			}
			_, _ = io.WriteString(w, `{"code":0,"data":{"tasks":[{"task_code":"chat_5","reward_credit":100,"current":5,"target":5,"accept_status":"completed"}]}}`)
		case strings.HasSuffix(r.URL.Path, "/v2/activity/growth/tasks/accept"):
			var body struct {
				Codes []string `json:"task_codes"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			accepted = body.Codes
			mu.Unlock()
			_, _ = io.WriteString(w, `{"code":0}`)
		case strings.HasSuffix(r.URL.Path, "/activity/growth/tasks/chat_5/claim"):
			mu.Lock()
			claimCalls++
			mu.Unlock()
			if got := r.Header.Get("x-client-platform"); got != "web" && got != "" {
				t.Errorf("claim platform = %q", got)
			}
			_, _ = io.WriteString(w, `{"code":0,"data":{"already_claimed":false,"credit":100,"energy":5}}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	site := workBuddyTestSite(t, server)
	credential := workBuddyTestCredential()
	ctx := context.Background()

	tasks, err := workBuddyListTasks(ctx, site, credential, false)
	if err != nil || len(tasks) != 1 || !tasks[0].Claimable {
		t.Fatalf("list: %+v err %v", tasks, err)
	}
	if err := workBuddyAcceptTasks(ctx, site, credential, false, []string{"chat_5"}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	mu.Lock()
	gotAccepted := append([]string{}, accepted...)
	mu.Unlock()
	if len(gotAccepted) != 1 || gotAccepted[0] != "chat_5" {
		t.Fatalf("accept body = %v", gotAccepted)
	}
	credit, energy, err := workBuddyClaimTask(ctx, site, credential, "chat_5", false)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if credit != 100 || energy != 5 {
		t.Fatalf("claim = %v/%v", credit, energy)
	}
	if claimCalls != 1 {
		t.Fatalf("claim calls = %d", claimCalls)
	}
}

// TestWorkBuddyClaimMPFallsBackToWeb covers the miniprogram claim: the CLI host
// answers the path with 400 for some tasks, and the web form then serves it.
func TestWorkBuddyClaimMPFallsBackToWeb(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		platform := r.Header.Get("X-Client-Platform")
		calls = append(calls, platform)
		if platform == workBuddyMPPlatform {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"code":400,"msg":"not served here"}`)
			return
		}
		_, _ = io.WriteString(w, `{"code":0,"data":{"credit":300,"energy":5}}`)
	}))
	defer server.Close()
	site := workBuddyTestSite(t, server)
	credit, energy, err := workBuddyClaimTask(context.Background(), site, workBuddyTestCredential(), "Sequential_Tasks_3", true)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if credit != 300 || energy != 5 {
		t.Fatalf("claim = %v/%v", credit, energy)
	}
	if len(calls) != 2 || calls[0] != workBuddyMPPlatform || calls[1] != "web" {
		t.Fatalf("claim attempts = %v (want the miniprogram form, then the web form)", calls)
	}
}

// TestWorkBuddyReportActivityShape checks the activity report carries the whole
// event the vendor's own client sends, fingerprint included: a report without
// userId is answered 200 and dropped, which is why it is asserted here.
func TestWorkBuddyReportActivityShape(t *testing.T) {
	var captured []map[string]any
	var headers http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header.Clone()
		_ = json.NewDecoder(r.Body).Decode(&captured)
		_, _ = io.WriteString(w, `{"code":0}`)
	}))
	defer server.Close()
	site := workBuddyTestSite(t, server)
	if err := workBuddyReportActivity(context.Background(), site, workBuddyTestCredential()); err != nil {
		t.Fatalf("report: %v", err)
	}
	if len(captured) != 1 {
		t.Fatalf("events = %d", len(captured))
	}
	event := captured[0]
	if event["eventCode"] != "chat_request_send" {
		t.Fatalf("eventCode = %v", event["eventCode"])
	}
	if event["userId"] != "u-1" {
		t.Fatalf("userId = %v (a report without it is dropped silently)", event["userId"])
	}
	if event["extName"] != "workbuddy-desktop" {
		t.Fatalf("extName = %v", event["extName"])
	}
	conversation, _ := event["conversationId"].(string)
	if !strings.Contains(conversation, "-") {
		t.Fatalf("conversationId = %q (want a UUID)", conversation)
	}
	if event["qimei36"] == nil || event["qimei36"] == "" {
		t.Fatal("the Chinese build's report carries qimei36")
	}
	if got := headers.Get("User-Agent"); !strings.HasPrefix(got, "WorkBuddy/") {
		t.Fatalf("User-Agent = %q", got)
	}
	if got := headers.Get("X-Domain"); strings.Contains(got, "://") {
		t.Fatalf("X-Domain = %q (must be a bare host)", got)
	}
}

// TestWorkBuddyChatSequenceShape pins the six events of a desktop turn, which the
// chat, template, playbook, skill and expert actions all build on.
func TestWorkBuddyChatSequenceShape(t *testing.T) {
	events := workBuddyChatSequence("conv-1", "req-1", "msg-1", "fast-model", "fast-model")
	want := []string{
		"agent_task_created", "chat_message_send", "chat_request_send",
		"chat_message_response", "chat_message_status", "chat_request_response",
	}
	if len(events) != len(want) {
		t.Fatalf("events = %d, want %d", len(events), len(want))
	}
	for i, code := range want {
		if events[i]["eventCode"] != code {
			t.Fatalf("event %d = %v, want %v", i, events[i]["eventCode"], code)
		}
	}
	if events[0]["conversationId"] != "conv-1" || events[2]["rootRequestId"] != "req-1" {
		t.Fatalf("ids not carried: %+v", events[0])
	}
	if events[3]["isSuccessful"] != true {
		t.Fatal("chat_message_response must be a successful answer")
	}
}

// workBuddyAutoTestServer answers the task list from a mutable map: the executor
// reads, acts, and reads again, so the test controls both answers.
type workBuddyAutoTestState struct {
	mu       sync.Mutex
	tasks    map[string]map[string]any
	listHits int
	claims   map[string]int
}

func (state *workBuddyAutoTestState) set(code string, current, target float64, acceptStatus string, locked bool) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.tasks[code] = map[string]any{
		"task_code": code, "current": current, "target": target,
		"accept_status": acceptStatus, "locked": locked, "reward_credit": 100,
	}
}

func (state *workBuddyAutoTestState) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/v2/activity/growth/tasks"):
			state.mu.Lock()
			state.listHits++
			tasks := make([]map[string]any, 0, len(state.tasks))
			for _, task := range state.tasks {
				tasks = append(tasks, task)
			}
			state.mu.Unlock()
			payload, _ := json.Marshal(map[string]any{"code": 0, "data": map[string]any{"tasks": tasks}})
			_, _ = w.Write(payload)
		case strings.HasSuffix(r.URL.Path, "/accept"):
			_, _ = io.WriteString(w, `{"code":0}`)
		case strings.Contains(r.URL.Path, "/claim"):
			code := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path[strings.LastIndex(r.URL.Path, "tasks/")+len("tasks/"):], ""), "/claim")
			state.mu.Lock()
			if state.claims == nil {
				state.claims = map[string]int{}
			}
			state.claims[code]++
			state.mu.Unlock()
			_, _ = io.WriteString(w, `{"code":0,"data":{"credit":100,"energy":5}}`)
		case strings.HasSuffix(r.URL.Path, "/v2/report"):
			_, _ = io.WriteString(w, `{"code":0}`)
		default:
			t.Logf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

// TestWorkBuddyExecuteAutoTaskStateMachine is the contract that keeps a run
// honest: only a confirmed claim is done, and nothing else claims to be.
func TestWorkBuddyExecuteAutoTaskStateMachine(t *testing.T) {
	shrinkWorkBuddyPacing(t)
	cases := []struct {
		name             string
		code             string
		current          float64
		target           float64
		accept           string
		locked           bool
		run              func(ctx context.Context, sess *workBuddyAutoSession, action *workBuddyAction) (string, error)
		wantStatus       string
		wantClaimed      bool
		wantClaimedCount int
	}{
		{
			name: "claimed already", code: "chat_5", current: 5, target: 5, accept: "claimed",
			run: nil, wantStatus: workBuddyStatusDone, wantClaimed: true,
		},
		{
			name: "locked", code: "chat_5", current: 0, target: 5, accept: "not_accepted", locked: true,
			run: nil, wantStatus: workBuddyStatusSkipped,
		},
		{
			name: "full but unclaimed claims without running", code: "chat_5", current: 5, target: 5, accept: "completed",
			run: func(context.Context, *workBuddyAutoSession, *workBuddyAction) (string, error) {
				t.Fatal("a full counter must not run the action again")
				return "", nil
			},
			wantStatus: workBuddyStatusDone, wantClaimed: true, wantClaimedCount: 1,
		},
		{
			name: "action runs and the vendor scores it", code: "chat_5", current: 0, target: 5, accept: "accepted",
			run: func(context.Context, *workBuddyAutoSession, *workBuddyAction) (string, error) {
				return "reported", nil
			},
			wantStatus: workBuddyStatusAwaitingProgress,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			state := &workBuddyAutoTestState{tasks: map[string]map[string]any{}}
			state.set(testCase.code, testCase.current, testCase.target, testCase.accept, testCase.locked)
			server := httptest.NewServer(state.handler(t))
			defer server.Close()
			site := workBuddyTestSite(t, server)
			sess := &workBuddyAutoSession{site: site, credential: workBuddyTestCredential()}
			action := &workBuddyAction{TaskCode: testCase.code, Desc: "test", run: testCase.run}
			outcome := sess.execute(context.Background(), action)
			if outcome.Status != testCase.wantStatus {
				t.Fatalf("status = %q, want %q (message %q)", outcome.Status, testCase.wantStatus, outcome.Message)
			}
			if outcome.Claimed != testCase.wantClaimed {
				t.Fatalf("claimed = %v, want %v", outcome.Claimed, testCase.wantClaimed)
			}
			state.mu.Lock()
			claims := state.claims[testCase.code]
			state.mu.Unlock()
			if claims != testCase.wantClaimedCount {
				t.Fatalf("claim calls = %d, want %d", claims, testCase.wantClaimedCount)
			}
		})
	}
}

// TestWorkBuddyExecuteAutoTaskSkipsAbsentTask covers a task the account does not
// have: the action table still names it, and the run reports it as skipped
// rather than failing.
func TestWorkBuddyExecuteAutoTaskSkipsAbsentTask(t *testing.T) {
	shrinkWorkBuddyPacing(t)
	state := &workBuddyAutoTestState{tasks: map[string]map[string]any{}}
	state.set("chat_5", 0, 5, "accepted", false)
	server := httptest.NewServer(state.handler(t))
	defer server.Close()
	site := workBuddyTestSite(t, server)
	sess := &workBuddyAutoSession{site: site, credential: workBuddyTestCredential()}
	action := &workBuddyAction{TaskCode: "first_buddy", run: func(context.Context, *workBuddyAutoSession, *workBuddyAction) (string, error) {
		t.Fatal("an absent task must not run")
		return "", nil
	}}
	outcome := sess.execute(context.Background(), action)
	if outcome.Status != workBuddyStatusSkipped || outcome.Message != "该账号没有此任务" {
		t.Fatalf("outcome = %+v", outcome)
	}
}

// TestWorkBuddyRunAllUsesSnapshot checks the run reads the list once per pass
// instead of once per task: the vendor's growth endpoint is rate-limited, and a
// per-task listing is what used to burn through it.
func TestWorkBuddyRunAllUsesSnapshot(t *testing.T) {
	shrinkWorkBuddyPacing(t)
	state := &workBuddyAutoTestState{tasks: map[string]map[string]any{}}
	state.set("chat_5", 5, 5, "completed", false)
	server := httptest.NewServer(state.handler(t))
	defer server.Close()
	site := workBuddyTestSite(t, server)
	sess := &workBuddyAutoSession{site: site, credential: workBuddyTestCredential()}
	previous := workBuddyActions
	workBuddyActions = []workBuddyAction{
		{TaskCode: "chat_5", Desc: "test", run: nil},
		{TaskCode: "RichMeow_Chat", Desc: "test", run: nil},
	}
	t.Cleanup(func() { workBuddyActions = previous })
	results := sess.runAll(context.Background(), nil)
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2: %+v", len(results), results)
	}
	statuses := map[string]string{}
	for _, result := range results {
		code, _ := result["taskCode"].(string)
		status, _ := result["status"].(string)
		statuses[code] = status
	}
	if statuses["chat_5"] != workBuddyStatusDone {
		t.Fatalf("chat_5 = %q (want done, it is full and unclaimed): %+v", statuses["chat_5"], results)
	}
	if statuses["RichMeow_Chat"] != workBuddyStatusSkipped {
		t.Fatalf("RichMeow_Chat = %q (the account does not have it)", statuses["RichMeow_Chat"])
	}
	// One listing for the default shape, one for the miniprogram shape: nothing
	// per task.
	if state.listHits > 2 {
		t.Fatalf("list calls = %d, want at most 2", state.listHits)
	}
}

// TestWorkBuddyReadOnlyBuild covers the international build: its growth center
// serves the list and refuses every write, so a run must decide before it fires.
func TestWorkBuddyReadOnlyBuild(t *testing.T) {
	site := workBuddyIntlSite
	sess := &workBuddyAutoSession{site: site, credential: workBuddyTestCredential()}
	outcome := sess.execute(context.Background(), &workBuddyAction{TaskCode: "chat_5"})
	if outcome.Status != workBuddyStatusSkipped || outcome.Message != workBuddyGlobalReadOnlyNote {
		t.Fatalf("outcome = %+v", outcome)
	}
	results := sess.runAll(context.Background(), nil)
	if len(results) != 1 || results[0]["status"] != workBuddyStatusSkipped {
		t.Fatalf("results = %+v", results)
	}
}

// TestWorkBuddyAutomationSettingsRoundTrip covers the switches the console writes:
// they persist per provider and survive a partial update.
func TestWorkBuddyAutomationSettingsRoundTrip(t *testing.T) {
	store := newFakeSettingsStore()
	server := &Server{adminAuth: store}
	settings := server.workBuddyAutomationFor("p1")
	if settings.DailyTasks || settings.DailyKeepalive || settings.Hour != workBuddyAutomationDefaultHour {
		t.Fatalf("defaults = %+v", settings)
	}
	settings.DailyTasks = true
	settings.Hour = 7
	if err := server.saveWorkBuddyAutomation("p1", settings); err != nil {
		t.Fatalf("save: %v", err)
	}
	read := server.workBuddyAutomationFor("p1")
	if !read.DailyTasks || read.DailyKeepalive || read.Hour != 7 {
		t.Fatalf("read = %+v", read)
	}
	// Another provider is untouched.
	if other := server.workBuddyAutomationFor("p2"); other.DailyTasks {
		t.Fatalf("p2 inherited p1's settings: %+v", other)
	}
}

// fakeSettingsStore is the settings half of AdminAuthStore, in memory.
type fakeSettingsStore struct {
	mu     sync.Mutex
	values map[string]string
}

func newFakeSettingsStore() *fakeSettingsStore {
	return &fakeSettingsStore{values: map[string]string{}}
}

func (store *fakeSettingsStore) Setting(key string) string {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.values[key]
}

func (store *fakeSettingsStore) SetSetting(key, value string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.values[key] = value
	return nil
}

// TestWorkBuddyResolveActions pins what a POST means: no codes is the whole
// action table, a named code is that one action, an unknown code still gets the
// claim-only fallback (otherwise a full counter could never be claimed), and a
// repeated code runs once.
func TestWorkBuddyResolveActions(t *testing.T) {
	all := workBuddyResolveActions(nil)
	if len(all) != len(workBuddyActions) {
		t.Fatalf("no codes = %d actions, want %d", len(all), len(workBuddyActions))
	}
	one := workBuddyResolveActions([]string{" chat_5 ", "chat_5"})
	if len(one) != 1 || one[0].TaskCode != "chat_5" || one[0].ClaimOnly {
		t.Fatalf("named code = %+v", one)
	}
	unknown := workBuddyResolveActions([]string{"some_new_task"})
	if len(unknown) != 1 || !unknown[0].ClaimOnly || unknown[0].TaskCode != "some_new_task" {
		t.Fatalf("unknown code = %+v", unknown)
	}
	mp := workBuddyResolveActions([]string{"Sequential_Tasks_2"})
	if len(mp) != 1 || !mp[0].MP {
		t.Fatalf("miniprogram code = %+v", mp)
	}
}

// TestWorkBuddyKeepaliveStepsAreIndependent covers the keep-alive's contract: an
// account that cannot rotate its token still reports activity, and the result
// says what worked rather than failing as a whole.
func TestWorkBuddyKeepaliveStepsAreIndependent(t *testing.T) {
	shrinkWorkBuddyPacing(t)
	var reported bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/v2/report"):
			reported = true
			_, _ = io.WriteString(w, `{"code":0}`)
		case strings.HasSuffix(r.URL.Path, "/v2/activity/growth/tasks"):
			_, _ = io.WriteString(w, `{"code":0,"data":{"tasks":[{"task_code":"chat_5","reward_credit":100,"current":5,"target":5,"accept_status":"completed"}]}}`)
		case strings.Contains(r.URL.Path, "/claim"):
			_, _ = io.WriteString(w, `{"code":0,"data":{"credit":100}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	site := workBuddyTestSite(t, server)
	provider := domain.Provider{
		ID: "p1", Name: "wb", AuthType: domain.AuthTypeWorkBuddy,
		WorkBuddy: &domain.WorkBuddyCredential{AccessToken: "at", RefreshToken: "rt", UserID: "u-1"},
	}
	router := NewRouter(domain.GatewayState{Providers: []domain.Provider{provider}})
	s := NewServer(router, monitor.NewStore())
	s.adminAuth = newFakeSettingsStore()
	_ = site
	// The token rotation is refused (the refresh endpoint is not served here),
	// which is exactly the case this test is about.
	result := s.keepaliveWorkBuddy(context.Background(), "p1")
	if result.Refreshed {
		t.Fatal("a refused refresh must not report a rotation")
	}
	if !result.Activity || !reported {
		t.Fatalf("activity must still be reported: %+v", result)
	}
	if result.SweepClaimed != 1 {
		t.Fatalf("the full reward should have been claimed: %+v", result)
	}
	if result.Message == "" {
		t.Fatal("the result must say what happened")
	}
}
