package gateway

// The console side of WorkBuddy's 任务中心: the task list, the one-click run,
// the activity report, the keep-alive, and the two daily switches behind them.
//
// A run is asynchronous. One pass walks the whole action table, and several of
// those actions pace themselves the way the vendor's risk check expects (the
// miniprogram conversation task waits ~45 s between events, the expert chain 6 s
// between summons), so a pass over one account takes minutes, not seconds. The
// console start it, polls it, and reads the per-task results as they land —
// which is also how the run explains itself when a task stays at
// awaiting_progress because the vendor scores asynchronously.
//
// 保活 is the same idea one level up: rotate the account's token, report the
// day's activity, press the check-in when the build has one, and sweep the
// rewards that filled up since. That is what keeps a subscription usable when
// nobody is chatting through it.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// workBuddyAutomationSettingPrefix names one provider's automation switches
	// in the settings table.
	workBuddyAutomationSettingPrefix = "workbuddy_automation:"
	// workBuddyAutomationDefaultHour is the Beijing hour a daily pass fires —
	// late enough that the vendor's day has turned everywhere.
	workBuddyAutomationDefaultHour = 9
	// workBuddyAutomationTick is how often the loop looks for a provider whose
	// daily pass is due.
	workBuddyAutomationTick = 5 * time.Minute
	// workBuddyAutomationStartDelay lets the gateway settle first.
	workBuddyAutomationStartDelay = 3 * time.Minute
	// workBuddyTaskRunLimit bounds one asynchronous run. The pacing inside the
	// actions is what needs it (five miniprogram chats at 45 s each).
	workBuddyTaskRunLimit = 45 * time.Minute
)

// workBuddyAutomation is one provider's automation settings, kept in the
// settings table as JSON so a provider patch cannot reset it (the same reason
// the check-in switch has its own endpoint).
type workBuddyAutomation struct {
	// DailyTasks runs the action table once a day.
	DailyTasks bool `json:"dailyTasks"`
	// DailyKeepalive rotates the token and reports activity once a day.
	DailyKeepalive bool `json:"dailyKeepalive"`
	// Hour is the Beijing hour the daily pass fires at (0–23).
	Hour int `json:"hour"`
	// LastRunDay is the Beijing day of the last automatic pass, which is what
	// keeps one pass per day.
	LastRunDay string `json:"lastRunDay,omitempty"`
	LastRunAt  string `json:"lastRunAt,omitempty"`
	// LastSummary is what the last automatic pass came to, for the card.
	LastSummary string `json:"lastSummary,omitempty"`
}

// workBuddyAutomationView is the automation block the console renders.
type workBuddyAutomationView struct {
	workBuddyAutomation
	// Today is the Beijing day the record is measured against.
	Today string `json:"today"`
}

// workBuddyRunView is one run, as the console polls it.
type workBuddyRunView struct {
	ID         string               `json:"id"`
	ProviderID string               `json:"providerId"`
	Running    bool                 `json:"running"`
	StartedAt  string               `json:"startedAt"`
	FinishedAt string               `json:"finishedAt,omitempty"`
	Events     []workBuddyAutoEvent `json:"events"`
	Results    []map[string]any     `json:"results"`
	Error      string               `json:"error,omitempty"`
}

// workBuddyRun is the mutable state behind a run view.
type workBuddyRun struct {
	mu         sync.Mutex
	id         string
	providerID string
	running    bool
	startedAt  time.Time
	finishedAt time.Time
	events     []workBuddyAutoEvent
	results    []map[string]any
	err        string
}

// record appends one progress event.
func (run *workBuddyRun) record(event workBuddyAutoEvent) {
	run.mu.Lock()
	defer run.mu.Unlock()
	// The event list is what the console renders as a log; cap it so a long run
	// cannot grow without bound.
	if len(run.events) < 400 {
		run.events = append(run.events, event)
	}
	if event.Result != nil {
		run.results = append(run.results, event.Result)
	}
}

// finish closes the run.
func (run *workBuddyRun) finish(err error) {
	run.mu.Lock()
	defer run.mu.Unlock()
	run.running = false
	run.finishedAt = time.Now()
	if err != nil {
		run.err = err.Error()
	}
}

// view renders the run for the console.
func (run *workBuddyRun) view() workBuddyRunView {
	run.mu.Lock()
	defer run.mu.Unlock()
	out := workBuddyRunView{
		ID: run.id, ProviderID: run.providerID, Running: run.running,
		StartedAt: run.startedAt.UTC().Format(time.RFC3339),
		Events:    append([]workBuddyAutoEvent{}, run.events...),
		Results:   append([]map[string]any{}, run.results...),
		Error:     run.err,
	}
	if !run.finishedAt.IsZero() {
		out.FinishedAt = run.finishedAt.UTC().Format(time.RFC3339)
	}
	return out
}

// ---- settings --------------------------------------------------------------

// workBuddyAutomationFor reads one provider's automation settings.
func (s *Server) workBuddyAutomationFor(providerID string) workBuddyAutomation {
	settings := workBuddyAutomation{Hour: workBuddyAutomationDefaultHour}
	if s.adminAuth == nil {
		return settings
	}
	raw := strings.TrimSpace(s.adminAuth.Setting(workBuddyAutomationSettingPrefix + providerID))
	if raw == "" {
		return settings
	}
	var stored workBuddyAutomation
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return settings
	}
	if stored.Hour < 0 || stored.Hour > 23 {
		stored.Hour = workBuddyAutomationDefaultHour
	}
	return stored
}

// saveWorkBuddyAutomation writes one provider's automation settings.
func (s *Server) saveWorkBuddyAutomation(providerID string, settings workBuddyAutomation) error {
	if s.adminAuth == nil {
		return errors.New("设置存储不可用")
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	return s.adminAuth.SetSetting(workBuddyAutomationSettingPrefix+providerID, string(encoded))
}

// ---- run registry ----------------------------------------------------------

// workBuddyRunByID finds a run the console started.
func (s *Server) workBuddyRunByID(id string) *workBuddyRun {
	s.workBuddyRunMu.Lock()
	defer s.workBuddyRunMu.Unlock()
	return s.workBuddyRuns[strings.TrimSpace(id)]
}

// workBuddyActiveRun is the run currently executing for a provider, if any.
func (s *Server) workBuddyActiveRun(providerID string) *workBuddyRun {
	s.workBuddyRunMu.Lock()
	defer s.workBuddyRunMu.Unlock()
	id := s.workBuddyProviderRuns[providerID]
	if id == "" {
		return nil
	}
	run := s.workBuddyRuns[id]
	if run == nil {
		return nil
	}
	run.mu.Lock()
	running := run.running
	run.mu.Unlock()
	if !running {
		return nil
	}
	return run
}

// lastWorkBuddyRun is the provider's most recent run, finished or not.
func (s *Server) lastWorkBuddyRun(providerID string) *workBuddyRun {
	s.workBuddyRunMu.Lock()
	defer s.workBuddyRunMu.Unlock()
	return s.workBuddyRuns[s.workBuddyProviderRuns[providerID]]
}

// startWorkBuddyRun registers a run and returns it. The caller starts it.
func (s *Server) startWorkBuddyRun(providerID string) *workBuddyRun {
	run := &workBuddyRun{
		id: workBuddyRequestID(), providerID: providerID,
		running: true, startedAt: time.Now(),
	}
	s.workBuddyRunMu.Lock()
	if s.workBuddyRuns == nil {
		s.workBuddyRuns = map[string]*workBuddyRun{}
	}
	if s.workBuddyProviderRuns == nil {
		s.workBuddyProviderRuns = map[string]string{}
	}
	s.workBuddyRuns[run.id] = run
	s.workBuddyProviderRuns[providerID] = run.id
	// Keep the registry from growing across a long-lived process: only the last
	// few runs of a provider are ever polled.
	for id, candidate := range s.workBuddyRuns {
		candidate.mu.Lock()
		stale := !candidate.running && time.Since(candidate.finishedAt) > 24*time.Hour
		candidate.mu.Unlock()
		if stale {
			delete(s.workBuddyRuns, id)
		}
	}
	s.workBuddyRunMu.Unlock()
	return run
}

// ---- the session -----------------------------------------------------------

// workBuddySessionFor builds an account session for a provider, refreshing the
// token first: every call below it carries that bearer.
func (s *Server) workBuddySessionFor(providerID string) (*workBuddyAutoSession, error) {
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		return nil, err
	}
	site, ok := workBuddySiteForAuthType(provider.AuthType)
	if !ok {
		return nil, errors.New("该 Provider 不是 WorkBuddy 连接方式")
	}
	if provider.WorkBuddy == nil {
		return nil, errors.New("WorkBuddy 尚未登录")
	}
	fresh, refreshErr := s.ensureFreshWorkBuddyToken(provider)
	if refreshErr != nil {
		return nil, refreshErr
	}
	if fresh.WorkBuddy != nil {
		provider = fresh
	}
	return &workBuddyAutoSession{
		server: s, site: site, provider: provider, credential: *provider.WorkBuddy,
	}, nil
}

// ---- handlers --------------------------------------------------------------

// handleWorkBuddyTasks reports the account's task list, the automation switches
// and the state of the last run in one reply, so opening the panel is one call.
func (s *Server) handleWorkBuddyTasks(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.requireProviderAccessForUser(w, r, providerID) {
		return
	}
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if !isWorkBuddyProvider(provider) {
		writeOpenAIError(w, http.StatusConflict, "该 Provider 不是 WorkBuddy 连接方式")
		return
	}
	view := workBuddyTaskCenterView{
		Supported:  true,
		Connected:  provider.WorkBuddy != nil,
		Automation: workBuddyAutomationView{workBuddyAutomation: s.workBuddyAutomationFor(providerID), Today: checkinDay(time.Now())},
	}
	if key, keyErr := checkinAccountKey(provider); keyErr == nil {
		view.AccountKey = key
	}
	if run := s.lastWorkBuddyRun(providerID); run != nil {
		rendered := run.view()
		view.Run = &rendered
	}
	if !view.Connected {
		view.Error = "WorkBuddy 尚未登录"
		writeJSON(w, http.StatusOK, view)
		return
	}
	session, sessionErr := s.workBuddySessionFor(providerID)
	if sessionErr != nil {
		view.Error = sessionErr.Error()
		writeJSON(w, http.StatusOK, view)
		return
	}
	view.ReadOnly = session.readOnly()
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	tasks, listErr := workBuddyListTasks(ctx, session.site, session.credential, false)
	if listErr != nil {
		view.Error = "拉取任务列表失败：" + listErr.Error()
		writeJSON(w, http.StatusOK, view)
		return
	}
	// The miniprogram list is a superset: merge the tasks only it carries, so the
	// console shows one row per task.
	if extra, extraErr := workBuddyListTasks(ctx, session.site, session.credential, true); extraErr == nil {
		seen := make(map[string]bool, len(tasks))
		for _, task := range tasks {
			seen[task.TaskCode] = true
		}
		for _, task := range extra {
			if !seen[task.TaskCode] {
				tasks = append(tasks, task)
			}
		}
	}
	view.Tasks = tasks
	view.FetchedAt = time.Now().UTC().Format(time.RFC3339)
	writeJSON(w, http.StatusOK, view)
}

// workBuddyTaskCenterView is the task center's whole reply.
type workBuddyTaskCenterView struct {
	Supported  bool                    `json:"supported"`
	Connected  bool                    `json:"connected"`
	ReadOnly   bool                    `json:"readOnly"`
	AccountKey string                  `json:"accountKey,omitempty"`
	Tasks      []workBuddyTask         `json:"tasks"`
	Error      string                  `json:"error,omitempty"`
	FetchedAt  string                  `json:"fetchedAt,omitempty"`
	Automation workBuddyAutomationView `json:"automation"`
	Run        *workBuddyRunView       `json:"run,omitempty"`
}

// handleWorkBuddyTasksAuto starts a run: one task when taskCodes names them, the
// whole action table when it does not.
func (s *Server) handleWorkBuddyTasksAuto(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.requireProviderAccessForUser(w, r, providerID) {
		return
	}
	var payload struct {
		TaskCodes []string `json:"taskCodes"`
	}
	_ = json.NewDecoder(r.Body).Decode(&payload)
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if !isWorkBuddyProvider(provider) {
		writeOpenAIError(w, http.StatusConflict, "该 Provider 不是 WorkBuddy 连接方式")
		return
	}
	if provider.WorkBuddy == nil {
		writeOpenAIError(w, http.StatusConflict, "WorkBuddy 尚未登录")
		return
	}
	if site, ok := workBuddySiteForAuthType(provider.AuthType); ok && site.intl {
		writeOpenAIError(w, http.StatusConflict, workBuddyGlobalReadOnlyNote)
		return
	}
	if s.workBuddyActiveRun(providerID) != nil {
		writeOpenAIError(w, http.StatusConflict, "这个账号已经有任务在跑，等这一轮结束")
		return
	}
	session, sessionErr := s.workBuddySessionFor(providerID)
	if sessionErr != nil {
		writeOpenAIError(w, http.StatusBadGateway, sessionErr.Error())
		return
	}
	actions := workBuddyResolveActions(payload.TaskCodes)
	run := s.startWorkBuddyRun(providerID)
	go s.executeWorkBuddyRun(session, run, actions)
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "runId": run.id, "tasks": len(actions)})
}

// workBuddyResolveActions maps requested task codes to actions: nothing asked for
// means the whole table, and an unknown code still gets the claim-only fallback
// so a "claim this one" request works for any task.
func workBuddyResolveActions(codes []string) []*workBuddyAction {
	requested := make([]string, 0, len(codes))
	for _, code := range codes {
		if trimmed := strings.TrimSpace(code); trimmed != "" {
			requested = append(requested, trimmed)
		}
	}
	if len(requested) == 0 {
		actions := make([]*workBuddyAction, 0, len(workBuddyActions))
		for i := range workBuddyActions {
			actions = append(actions, &workBuddyActions[i])
		}
		return actions
	}
	actions := make([]*workBuddyAction, 0, len(requested))
	seen := map[string]bool{}
	for _, code := range requested {
		if seen[code] {
			continue
		}
		seen[code] = true
		actions = append(actions, workBuddyQueuedActionFor(code))
	}
	return actions
}

// executeWorkBuddyRun runs the actions one at a time and closes the run whatever
// happens.
func (s *Server) executeWorkBuddyRun(session *workBuddyAutoSession, run *workBuddyRun, actions []*workBuddyAction) {
	ctx, cancel := context.WithTimeout(context.Background(), workBuddyTaskRunLimit)
	defer cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			run.finish(errors.New("任务执行异常中断"))
			return
		}
	}()
	for _, action := range actions {
		if ctx.Err() != nil {
			run.record(workBuddyAutoEvent{Kind: "phase", Message: "运行超时，已停止"})
			break
		}
		run.record(workBuddyAutoEvent{Kind: "task_start", TaskCode: action.TaskCode})
		outcome := session.execute(ctx, action)
		result := outcome.resultMap(action)
		run.record(workBuddyAutoEvent{Kind: "result", TaskCode: action.TaskCode, Result: result})
		if !waitWorkBuddyContext(ctx, workBuddyReportGap) {
			break
		}
	}
	if ctx.Err() != nil {
		run.finish(ctx.Err())
		return
	}
	run.finish(nil)
	s.logs.AddApp("info", "workbuddy tasks run finished", run.providerID)
}

// handleWorkBuddyRunStatus reports one run's progress.
func (s *Server) handleWorkBuddyRunStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireProviderAccessForUser(w, r, r.PathValue("id")) {
		return
	}
	runID := strings.TrimSpace(r.URL.Query().Get("runId"))
	if runID == "" {
		writeOpenAIError(w, http.StatusBadRequest, "runId is required")
		return
	}
	run := s.workBuddyRunByID(runID)
	if run == nil {
		writeOpenAIError(w, http.StatusNotFound, "unknown run")
		return
	}
	writeJSON(w, http.StatusOK, run.view())
}

// handleWorkBuddyActivity reports the day's activity: the one event that lights
// the streak and unlocks first_buddy.
func (s *Server) handleWorkBuddyActivity(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.requireProviderAccessForUser(w, r, providerID) {
		return
	}
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if !isWorkBuddyProvider(provider) || provider.WorkBuddy == nil {
		writeOpenAIError(w, http.StatusConflict, "WorkBuddy 尚未登录")
		return
	}
	session, sessionErr := s.workBuddySessionFor(providerID)
	if sessionErr != nil {
		writeOpenAIError(w, http.StatusBadGateway, sessionErr.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if reportErr := workBuddyReportActivity(ctx, session.site, session.credential); reportErr != nil {
		writeOpenAIError(w, http.StatusBadGateway, "活跃上报失败："+reportErr.Error())
		return
	}
	s.logs.AddApp("info", "workbuddy activity reported", providerID)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "message": "活跃已上报（连登天数与 first_buddy 的门槛都看这一条）",
		"at": time.Now().UTC().Format(time.RFC3339),
	})
}

// workBuddyKeepaliveResult is what one keep-alive came to.
type workBuddyKeepaliveResult struct {
	Refreshed     bool   `json:"refreshed"`
	ExpiresAt     string `json:"expiresAt,omitempty"`
	Activity      bool   `json:"activity"`
	Checkin       string `json:"checkin,omitempty"`
	SweepAccepted int    `json:"sweepAccepted"`
	SweepClaimed  int    `json:"sweepClaimed"`
	Message       string `json:"message,omitempty"`
	Error         string `json:"error,omitempty"`
}

// keepaliveWorkBuddy rotates the token, reports activity, presses the check-in
// when the build has one, and sweeps the rewards that filled up. Every step is
// optional: one that fails is reported and the rest still runs, because each is
// worth having on its own.
func (s *Server) keepaliveWorkBuddy(ctx context.Context, providerID string) workBuddyKeepaliveResult {
	result := workBuddyKeepaliveResult{}
	updated, refreshErr := s.forceRefreshWorkBuddyToken(providerID)
	if refreshErr != nil {
		result.Error = "刷新令牌失败：" + refreshErr.Error()
	} else {
		result.Refreshed = true
		if updated.WorkBuddy != nil {
			result.ExpiresAt = updated.WorkBuddy.ExpiresAt
		}
	}
	session, sessionErr := s.workBuddySessionFor(providerID)
	if sessionErr != nil {
		if result.Error == "" {
			result.Error = sessionErr.Error()
		}
		return result
	}
	if reportErr := workBuddyReportActivity(ctx, session.site, session.credential); reportErr != nil {
		if result.Error == "" {
			result.Error = "活跃上报失败：" + reportErr.Error()
		}
	} else {
		result.Activity = true
	}
	// The check-in is a step of its own: the build may not run one (WorkBuddy AI
	// ships without it), and a gateway started without a check-in store has
	// nowhere to keep its answer.
	if _, supported := checkinVendorOf(session.provider); supported && checkinUsable(session.provider) && s.checkinStoreReady() {
		if _, record, ran := s.checkinProviderNow(ctx, providerID, true); ran {
			result.Checkin = record.Outcome
		}
	}
	if accepted, claimed, sweepErr := session.sweep(ctx); sweepErr == nil {
		result.SweepAccepted, result.SweepClaimed = accepted, claimed
	} else if result.Error == "" {
		result.Error = "补领失败：" + sweepErr.Error()
	}
	parts := make([]string, 0, 4)
	if result.Refreshed {
		parts = append(parts, "令牌已续期")
	}
	if result.Activity {
		parts = append(parts, "活跃已上报")
	}
	if result.Checkin != "" {
		parts = append(parts, "签到："+result.Checkin)
	}
	if result.SweepClaimed > 0 {
		parts = append(parts, "补领 "+strconv.Itoa(result.SweepClaimed)+" 个任务")
	}
	if len(parts) == 0 {
		parts = append(parts, "保活已执行")
	}
	result.Message = strings.Join(parts, " · ")
	return result
}

// handleWorkBuddyKeepalive runs one keep-alive now.
func (s *Server) handleWorkBuddyKeepalive(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.requireProviderAccessForUser(w, r, providerID) {
		return
	}
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if !isWorkBuddyProvider(provider) || provider.WorkBuddy == nil {
		writeOpenAIError(w, http.StatusConflict, "WorkBuddy 尚未登录")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	result := s.keepaliveWorkBuddy(ctx, providerID)
	s.logs.AddApp("info", "workbuddy keepalive", providerID+" "+result.Message)
	status := http.StatusOK
	if !result.Activity && result.Error != "" {
		status = http.StatusBadGateway
	}
	writeJSON(w, status, result)
}

// handleWorkBuddyAutomation switches the two daily passes of one subscription.
func (s *Server) handleWorkBuddyAutomation(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	if !s.requireProviderAccessForUser(w, r, providerID) {
		return
	}
	var payload struct {
		DailyTasks     *bool `json:"dailyTasks"`
		DailyKeepalive *bool `json:"dailyKeepalive"`
		Hour           *int  `json:"hour"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, `body must be {"dailyTasks":true|false,"dailyKeepalive":true|false,"hour":0-23}`)
		return
	}
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if !isWorkBuddyProvider(provider) {
		writeOpenAIError(w, http.StatusConflict, "该 Provider 不是 WorkBuddy 连接方式")
		return
	}
	if (payload.DailyTasks != nil && *payload.DailyTasks) || (payload.DailyKeepalive != nil && *payload.DailyKeepalive) {
		if provider.WorkBuddy == nil {
			writeOpenAIError(w, http.StatusConflict, "WorkBuddy 尚未登录")
			return
		}
		if site, ok := workBuddySiteForAuthType(provider.AuthType); ok && site.intl && payload.DailyTasks != nil && *payload.DailyTasks {
			writeOpenAIError(w, http.StatusConflict, workBuddyGlobalReadOnlyNote)
			return
		}
	}
	settings := s.workBuddyAutomationFor(providerID)
	if payload.DailyTasks != nil {
		settings.DailyTasks = *payload.DailyTasks
	}
	if payload.DailyKeepalive != nil {
		settings.DailyKeepalive = *payload.DailyKeepalive
	}
	if payload.Hour != nil && *payload.Hour >= 0 && *payload.Hour <= 23 {
		settings.Hour = *payload.Hour
	}
	if err := s.saveWorkBuddyAutomation(providerID, settings); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "保存自动化设置失败: "+err.Error())
		return
	}
	verb := "已更新自动化设置"
	switch {
	case settings.DailyTasks && settings.DailyKeepalive:
		verb = "已开启每日自动任务与每日保活"
	case settings.DailyTasks:
		verb = "已开启每日自动任务"
	case settings.DailyKeepalive:
		verb = "已开启每日保活"
	case payload.DailyTasks != nil || payload.DailyKeepalive != nil:
		verb = "已关闭自动化"
	}
	s.logs.AddApp("info", verb, providerID)
	writeJSON(w, http.StatusOK, workBuddyAutomationView{workBuddyAutomation: settings, Today: checkinDay(time.Now())})
}

// ---- the daily loop --------------------------------------------------------

// StartWorkBuddyAutomation runs each subscription's daily passes at its hour.
// It ticks every few minutes and asks nothing until a provider is due, so a
// gateway with automation off costs one provider listing per tick.
func (s *Server) StartWorkBuddyAutomation(ctx context.Context) {
	go func() {
		timer := time.NewTimer(workBuddyAutomationStartDelay)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			s.runDueWorkBuddyAutomation(ctx)
			timer.Reset(workBuddyAutomationTick)
		}
	}()
}

// runDueWorkBuddyAutomation starts a pass for every provider whose day has come
// and whose settings ask for one.
func (s *Server) runDueWorkBuddyAutomation(ctx context.Context) {
	now := time.Now()
	state := s.router.State()
	for _, provider := range state.Providers {
		if provider.Disabled || !isWorkBuddyProvider(provider) || provider.WorkBuddy == nil {
			continue
		}
		settings := s.workBuddyAutomationFor(provider.ID)
		if !settings.DailyTasks && !settings.DailyKeepalive {
			continue
		}
		today := checkinDay(now)
		if settings.LastRunDay == today {
			continue
		}
		hour := settings.Hour
		if hour < 0 || hour > 23 {
			hour = workBuddyAutomationDefaultHour
		}
		if now.In(beijingZone).Hour() < hour {
			continue
		}
		go s.runWorkBuddyDaily(ctx, provider.ID, settings)
	}
}

// runWorkBuddyDaily is one provider's daily pass: the keep-alive and/or the task
// run, then the day is recorded so it is not done twice.
func (s *Server) runWorkBuddyDaily(ctx context.Context, providerID string, settings workBuddyAutomation) {
	// The day is claimed before the work starts: a pass that panics or is cut
	// short must not be retried on every tick for the rest of the day.
	settings.LastRunDay = checkinDay(time.Now())
	settings.LastRunAt = time.Now().UTC().Format(time.RFC3339)
	if err := s.saveWorkBuddyAutomation(providerID, settings); err != nil {
		s.logs.AddApp("warn", "workbuddy automation: claim day failed", err.Error())
	}
	runCtx, cancel := context.WithTimeout(ctx, workBuddyTaskRunLimit)
	defer cancel()
	summary := make([]string, 0, 3)
	if settings.DailyKeepalive {
		result := s.keepaliveWorkBuddy(runCtx, providerID)
		summary = append(summary, result.Message)
	}
	if settings.DailyTasks && runCtx.Err() == nil {
		if s.workBuddyActiveRun(providerID) == nil {
			if session, err := s.workBuddySessionFor(providerID); err == nil {
				run := s.startWorkBuddyRun(providerID)
				results := session.runAll(runCtx, run.record)
				run.finish(nil)
				done, claimed := 0, 0
				for _, result := range results {
					if status, _ := result["status"].(string); status == workBuddyStatusDone {
						done++
					}
					if claimedFlag, _ := result["claimed"].(bool); claimedFlag {
						claimed++
					}
				}
				summary = append(summary, "任务完成 "+strconv.Itoa(done)+" 项（领奖 "+strconv.Itoa(claimed)+" 项）")
			} else {
				summary = append(summary, "任务运行失败："+err.Error())
			}
		}
	}
	settings.LastSummary = strings.Join(summary, " · ")
	if err := s.saveWorkBuddyAutomation(providerID, settings); err != nil {
		s.logs.AddApp("warn", "workbuddy automation: save state failed", err.Error())
		return
	}
	s.logs.AddApp("info", "workbuddy 每日自动化完成", providerID+" "+settings.LastSummary)
}
