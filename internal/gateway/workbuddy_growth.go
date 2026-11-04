package gateway

// WorkBuddy's 任务中心 (growth center): the task list, accepting a task, and
// claiming its reward.
//
// An account on a personal plan is handed a few dozen 成长任务 — "send 5 chats",
// "try the expert market", "read the library". Each one is worth credits, and
// each is scored by the vendor's own telemetry: the task is only a counter, and
// what moves it is an event the desktop/mp client reports to /v2/report. That is
// why this file only reads and claims: the events are reported by
// workbuddy_autotask.go, which knows which event a task counts.
//
// Endpoints, by what the vendor's own clients do:
//   - GET  /v2/activity/growth/tasks               the whole list, progress and all
//   - POST /v2/activity/growth/tasks/accept        {"task_codes":[...]} 报名; it
//     moves not_accepted → accepted and grants no progress
//   - POST /activity/growth/tasks/{code}/claim     the reward, on the web host
//     (the CLI host answers the same path with 400 "task not completed")
//
// The miniprogram-only tasks (Sequential_Tasks_*) are listed, accepted and
// claimed only with X-Client-Platform: miniprogram — without it the list does
// not contain them and accept answers "task not found".
//
// Everything here talks to the web host of the build (www.workbuddy.cn for the
// Chinese build, www.workbuddy.ai for the international one), which is where the
// vendor's clients put all of billing/growth/activity.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

const (
	workBuddyTasksPath       = "/v2/activity/growth/tasks"
	workBuddyTasksAcceptPath = "/v2/activity/growth/tasks/accept"
	// workBuddyTaskClaimPrefix is the web host's claim path; the task code sits
	// in the path rather than in a body.
	workBuddyTaskClaimPrefix = "/activity/growth/tasks/"
	workBuddyBuddyFirstPath  = "/activity/growth/buddy/first"
	workBuddyBuddyAgreePath  = "/activity/growth/buddy/agreement"
	workBuddyExpertListPath  = "/portal/operation-platform/market/expert/list"
	workBuddyAppearancePath  = "/v2/user-asset/appearance/set"
	workBuddyReportPath      = "/v2/report"

	// workBuddyMPPlatform is the miniprogram client marker. The Sequential_*
	// tasks exist only under it: the default list does not carry them, and an
	// accept without the header answers "task not found".
	workBuddyMPPlatform = "miniprogram"
	// The rest of the miniprogram signature its report carries.
	workBuddyMPWebPlatform = "mp-weixin"
	workBuddyMPProduct     = "workbuddy-mp"
	workBuddyMPVersion     = "2.4.0"
)

// The web hosts, vars so tests can point them at an httptest server. The
// Chinese build serves growth/billing/activity on www.workbuddy.cn — the CLI
// host (copilot.tencent.com) answers the claim path with 400.
var (
	workBuddyCNWebBase   = "https://www.workbuddy.cn"
	workBuddyIntlWebBase = "https://www.workbuddy.ai"
)

// workBuddyWebBase is the build's web host: where growth, reports and the
// expert market live.
func workBuddyWebBase(site workBuddySite) string {
	if site.intl {
		return strings.TrimRight(workBuddyIntlWebBase, "/")
	}
	return strings.TrimRight(workBuddyCNWebBase, "/")
}

// workBuddyWebHost is that host by name, for the X-Domain header (the vendor
// sends the bare hostname, not a URL).
func workBuddyWebHost(site workBuddySite) string {
	base := workBuddyWebBase(site)
	base = strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
	if cut := strings.IndexAny(base, "/?#"); cut >= 0 {
		base = base[:cut]
	}
	return base
}

// workBuddyGrowthHeaders are the headers a growth call carries: the account's
// bearer and identity, plus the miniprogram marker when mp is set.
func workBuddyGrowthHeaders(site workBuddySite, credential domain.WorkBuddyCredential, mp bool) map[string]string {
	headers := workBuddyAccountHeaders(site, credential)
	headers["X-Domain"] = workBuddyWebHost(site)
	if mp {
		headers["X-Client-Platform"] = workBuddyMPPlatform
	}
	return headers
}

// workBuddyGrowthCall asks one growth endpoint on the web host. dst reads the
// envelope's data, as everywhere else. extra carries headers only some paths
// send (the claim's x-client-platform: web is one).
func workBuddyGrowthCall(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential, mp bool, method, path string, body, dst any) error {
	return workBuddyGrowthCallExtra(ctx, site, credential, mp, nil, method, path, body, dst)
}

// workBuddyGrowthCallExtra is workBuddyGrowthCall with headers of its own.
func workBuddyGrowthCallExtra(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential, mp bool, extra map[string]string, method, path string, body, dst any) error {
	endpoint := workBuddyWebBase(site) + path
	headers := workBuddyGrowthHeaders(site, credential, mp)
	for key, value := range extra {
		headers[key] = value
	}
	if body == nil && method == http.MethodPost {
		// The vendor's clients send `{}` rather than an empty body on these
		// POSTs; an empty one reads as a malformed request.
		body = map[string]any{}
	}
	return workBuddyCall(ctx, site, method, endpoint, headers, body, dst)
}

// workBuddyRateLimited reports whether err is the vendor's soft rate limit (429).
// Only these are worth waiting out: a growth round fires dozens of calls, and a
// minute-long window is the whole reason a round would otherwise fail halfway.
func workBuddyRateLimited(err error) bool {
	if err == nil {
		return false
	}
	var target *workBuddyError
	if errors.As(err, &target) {
		return target.status == http.StatusTooManyRequests || target.code == http.StatusTooManyRequests
	}
	return strings.Contains(err.Error(), "429")
}

// The soft-rate budget: two retries, linear, capped so one Retry-After header
// cannot park a console request for minutes.
var (
	workBuddySoftRateRetries = 2
	workBuddySoftRateGap     = 15 * time.Second
	workBuddySoftRateGapMax  = 60 * time.Second
)

func workBuddySoftRateWait(attempt int) time.Duration {
	wait := time.Duration(attempt) * workBuddySoftRateGap
	if wait > workBuddySoftRateGapMax {
		return workBuddySoftRateGapMax
	}
	return wait
}

// workBuddyRetrySoftRate runs fn, waiting out a soft rate limit. Everything it
// wraps is idempotent (list, accept, claim, event report), so a retry is safe.
func workBuddyRetrySoftRate(ctx context.Context, fn func() error) error {
	err := fn()
	for attempt := 1; err != nil && workBuddyRateLimited(err) && attempt <= workBuddySoftRateRetries; attempt++ {
		timer := time.NewTimer(workBuddySoftRateWait(attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		err = fn()
	}
	return err
}

// ---- the task list ---------------------------------------------------------

// workBuddyTask is one 成长任务 as the console shows it: what it is worth, how
// far along it is, and whether this gateway knows how to move it.
type workBuddyTask struct {
	TaskCode    string  `json:"taskCode"`
	Title       string  `json:"title,omitempty"`
	Description string  `json:"description,omitempty"`
	TaskDesc    string  `json:"taskDesc,omitempty"`
	Credit      float64 `json:"credit"`
	Energy      float64 `json:"energy"`
	HasReward   bool    `json:"hasReward"`
	TaskType    string  `json:"taskType,omitempty"`
	Tag         string  `json:"tag,omitempty"`
	JumpURL     string  `json:"jumpUrl,omitempty"`
	Locked      bool    `json:"locked"`
	// ProgressKnown is false for a list shape that reports no progress at all
	// (the international build's), which is why Target/Current may be absent.
	ProgressKnown bool    `json:"progressKnown"`
	Target        float64 `json:"target,omitempty"`
	Current       float64 `json:"current,omitempty"`
	AcceptStatus  string  `json:"acceptStatus,omitempty"`
	Status        string  `json:"status,omitempty"`
	Claimable     bool    `json:"claimable"`
	Claimed       bool    `json:"claimed"`
	// Automatable says this gateway knows an action that moves the task;
	// Action is what it does, for the button's tooltip.
	Automatable bool   `json:"automatable"`
	Action      string `json:"action,omitempty"`
	MP          bool   `json:"mp,omitempty"`
}

// workBuddyListTasks reads the whole task list. mp asks for the miniprogram
// list, which is a superset of the default one (the two are merged by code
// before anything is executed).
func workBuddyListTasks(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential, mp bool) ([]workBuddyTask, error) {
	var data json.RawMessage
	err := workBuddyRetrySoftRate(ctx, func() error {
		return workBuddyGrowthCall(ctx, site, credential, mp, http.MethodGet, workBuddyTasksPath, nil, &data)
	})
	if err != nil {
		return nil, err
	}
	return workBuddyParseTasks(data)
}

// workBuddyParseTasks reads data.tasks[] in either shape the vendor sends:
// progress as {current,target}, or flat current/target, or not at all.
func workBuddyParseTasks(data json.RawMessage) ([]workBuddyTask, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var envelope struct {
		Tasks []map[string]json.RawMessage `json:"tasks"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, err
	}
	out := make([]workBuddyTask, 0, len(envelope.Tasks))
	for _, fields := range envelope.Tasks {
		task := workBuddyTask{
			TaskCode:     workBuddyTaskString(fields, "task_code"),
			Title:        workBuddyTaskString(fields, "title"),
			Description:  workBuddyTaskString(fields, "description"),
			TaskDesc:     workBuddyTaskString(fields, "task_desc"),
			TaskType:     workBuddyTaskString(fields, "task_type"),
			Tag:          workBuddyTaskString(fields, "tag"),
			JumpURL:      workBuddyTaskString(fields, "jump_url"),
			AcceptStatus: workBuddyTaskString(fields, "accept_status"),
			Status:       workBuddyTaskString(fields, "status"),
			Locked:       workBuddyTaskBool(fields, "locked"),
			HasReward:    workBuddyTaskBool(fields, "has_reward"),
		}
		if task.TaskCode == "" {
			// The international list names the field `code`.
			task.TaskCode = workBuddyTaskString(fields, "code")
		}
		task.Credit = workBuddyTaskNumber(fields, "reward_credit")
		task.Energy = workBuddyTaskNumber(fields, "reward_energy")
		// Progress: a nested object wins over the flat pair, and "the key is
		// there" (not "> 0") is what says progress is known — an explicit 0/0 is
		// a real answer.
		current, currentOK := workBuddyTaskNumberOK(fields, "current")
		target, targetOK := workBuddyTaskNumberOK(fields, "target")
		if raw, exists := fields["progress"]; exists && len(raw) > 0 && string(raw) != "null" {
			var progress map[string]json.RawMessage
			if json.Unmarshal(raw, &progress) == nil {
				if nestedCurrent, ok := workBuddyTaskNumberOK(progress, "current"); ok {
					current, currentOK = nestedCurrent, true
				}
				if nestedTarget, ok := workBuddyTaskNumberOK(progress, "target"); ok {
					target, targetOK = nestedTarget, true
				}
			}
		}
		task.ProgressKnown = currentOK && targetOK
		task.Target, task.Current = target, current
		task.Claimed = task.AcceptStatus == "claimed"
		task.Claimable = !task.Claimed && task.Target > 0 && task.Current >= task.Target
		if action := workBuddyActionFor(task.TaskCode); action != nil {
			task.Automatable = true
			task.Action = action.Desc
		}
		task.MP = workBuddyMPTaskCode(task.TaskCode)
		out = append(out, task)
	}
	return out, nil
}

func workBuddyTaskString(fields map[string]json.RawMessage, key string) string {
	raw, exists := fields[key]
	if !exists {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func workBuddyTaskBool(fields map[string]json.RawMessage, key string) bool {
	raw, exists := fields[key]
	if !exists {
		return false
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false
	}
	return value
}

func workBuddyTaskNumber(fields map[string]json.RawMessage, key string) float64 {
	value, _ := workBuddyTaskNumberOK(fields, key)
	return value
}

// workBuddyTaskNumberOK reads a number the vendor may send as a string, a bare
// number, or not at all; ok says the field was there (0 is a real answer).
func workBuddyTaskNumberOK(fields map[string]json.RawMessage, key string) (float64, bool) {
	raw, exists := fields[key]
	if !exists {
		return 0, false
	}
	var number workBuddyNumber
	if err := json.Unmarshal(raw, &number); err != nil {
		return 0, false
	}
	return float64(number), true
}

// workBuddyTaskByCode is one task from the account's list, or nil when the
// account does not have it (signed out of the campaign, or never in it).
func workBuddyTaskByCode(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential, code string, mp bool) (*workBuddyTask, error) {
	tasks, err := workBuddyListTasks(ctx, site, credential, mp)
	if err != nil {
		return nil, err
	}
	for i := range tasks {
		if strings.EqualFold(tasks[i].TaskCode, strings.TrimSpace(code)) {
			return &tasks[i], nil
		}
	}
	return nil, nil
}

// Bounded polling while the vendor scores an event: it is asynchronous, so a
// task read right after a report can still show the old count.
var (
	workBuddyClaimPollAttempts = 3
	workBuddyClaimPollGap      = 3 * time.Second
)

// workBuddyTaskByCodeWaiting re-reads a task until its progress shows what was
// just reported, or the budget runs out.
func workBuddyTaskByCodeWaiting(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential, code string, mp bool) (*workBuddyTask, error) {
	task, err := workBuddyTaskByCode(ctx, site, credential, code, mp)
	for attempt := 0; err == nil && attempt < workBuddyClaimPollAttempts; attempt++ {
		if task == nil || workBuddyTaskProgressReached(task) || task.Claimed {
			return task, nil
		}
		timer := time.NewTimer(workBuddyClaimPollGap)
		select {
		case <-ctx.Done():
			timer.Stop()
			return task, ctx.Err()
		case <-timer.C:
		}
		task, err = workBuddyTaskByCode(ctx, site, credential, code, mp)
	}
	return task, err
}

// workBuddyTaskProgressReached says the vendor has counted the task as done: the
// counter is at its target, or the status says complete.
func workBuddyTaskProgressReached(task *workBuddyTask) bool {
	if task == nil {
		return false
	}
	if task.Target > 0 && task.Current >= task.Target {
		return true
	}
	return task.AcceptStatus == "completed" || strings.EqualFold(task.Status, "complete")
}

// workBuddyTaskProgressText is the "3/5" the console shows, and what the result
// lines compare before and after.
func workBuddyTaskProgressText(task *workBuddyTask) string {
	if task == nil {
		return "?"
	}
	if task.Target > 0 {
		return fmt.Sprintf("%g/%g", task.Current, task.Target)
	}
	if task.Claimed {
		return "已领取"
	}
	if task.AcceptStatus != "" {
		return task.AcceptStatus
	}
	return "—"
}

// ---- accepting and claiming ------------------------------------------------

// workBuddyAcceptTasks 报名 the given tasks. Accepting grants no progress — it
// only puts the task in the accepted state — so it is safe to repeat, and a
// failure is never fatal to the run.
func workBuddyAcceptTasks(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential, mp bool, codes []string) error {
	if len(codes) == 0 {
		return nil
	}
	return workBuddyRetrySoftRate(ctx, func() error {
		return workBuddyGrowthCall(ctx, site, credential, mp, http.MethodPost, workBuddyTasksAcceptPath,
			map[string]any{"task_codes": codes}, nil)
	})
}

// workBuddyClaimTask claims one task's reward and reports what landed (0/0 when
// the reward had been claimed before — the vendor answers already_claimed and
// that is not an error).
func workBuddyClaimTask(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential, code string, mp bool) (credit, energy float64, err error) {
	path := workBuddyTaskClaimPrefix + code + "/claim"
	var claim workBuddyClaimReward
	claimOnce := func(withMP bool) error {
		claim = workBuddyClaimReward{}
		var extra map[string]string
		if !withMP {
			// The web form of the claim names the platform it comes from; the
			// miniprogram branch already carries its own marker.
			extra = map[string]string{"x-client-platform": "web"}
		}
		return workBuddyGrowthCallExtra(ctx, site, credential, withMP, extra, http.MethodPost, path, nil, &claim)
	}
	err = workBuddyRetrySoftRate(ctx, func() error {
		claimErr := claimOnce(mp)
		if claimErr == nil {
			return nil
		}
		// A miniprogram claim is served on the chat host for most tasks, but the
		// web form answers some of them; the CLI host refuses the path with 400.
		if mp && workBuddyBadRequest(claimErr) {
			return claimOnce(false)
		}
		return claimErr
	})
	if err != nil {
		return 0, 0, err
	}
	return float64(claim.Credit), float64(claim.Energy), nil
}

// workBuddyClaimReward is the claim response: what landed, and whether it had
// already been claimed.
type workBuddyClaimReward struct {
	AlreadyClaimed bool            `json:"already_claimed"`
	Credit         workBuddyNumber `json:"credit"`
	Energy         workBuddyNumber `json:"energy"`
}

// workBuddyBadRequest says err was the vendor answering 400, which is how a path
// it does not serve on that host shows up.
func workBuddyBadRequest(err error) bool {
	if err == nil {
		return false
	}
	var target *workBuddyError
	if errors.As(err, &target) {
		return target.status == http.StatusBadRequest || target.code == http.StatusBadRequest
	}
	return strings.Contains(err.Error(), "HTTP 400")
}

// ---- buddy (first_buddy) and appearance ------------------------------------

// workBuddyBuddyAgree accepts the buddy agreement. It is idempotent upstream.
func workBuddyBuddyAgree(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential) error {
	return workBuddyRetrySoftRate(ctx, func() error {
		return workBuddyGrowthCall(ctx, site, credential, false, http.MethodPost, workBuddyBuddyAgreePath,
			map[string]any{"agree": true}, nil)
	})
}

// workBuddyBuddyFirst adopts the account's first buddy: the +300 the first_buddy
// task is worth. The vendor turns it away with 400 "first_buddy task not
// completed yet" until the account has been active that day, which is why the
// action reports activity first and treats that answer as "try tomorrow".
func workBuddyBuddyFirst(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential) error {
	return workBuddyRetrySoftRate(ctx, func() error {
		return workBuddyGrowthCall(ctx, site, credential, false, http.MethodPost, workBuddyBuddyFirstPath,
			map[string]any{}, nil)
	})
}

// workBuddyBuddyIncomplete says err is that "not active yet" answer.
func workBuddyBuddyIncomplete(err error) bool {
	return err != nil && workBuddyBadRequest(err) &&
		strings.Contains(strings.ToLower(err.Error()), "first_buddy")
}

// workBuddySetAppearanceTheme applies a theme. The task only counts when the
// matching appearance_skin_apply event follows (see workbuddy_autotask.go), so
// this alone is half of it.
func workBuddySetAppearanceTheme(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential, resourceKey string) error {
	return workBuddyRetrySoftRate(ctx, func() error {
		return workBuddyGrowthCall(ctx, site, credential, false, http.MethodPost, workBuddyAppearancePath,
			map[string]any{"kind": "theme", "resource_key": resourceKey}, nil)
	})
}

// ---- the expert market -----------------------------------------------------

// workBuddyMarketExpert is one expert of the market. The id has to be a real
// one: the vendor does not count an expert_actual_use it cannot resolve.
type workBuddyMarketExpert struct {
	ExpertID      string `json:"expert_id"`
	ExpertType    string `json:"expert_type"`
	DisplayNameZH string `json:"display_name_zh"`
	ProfessionZH  string `json:"profession_zh"`
	Version       string `json:"version"`
	Categories    []any  `json:"categories"`
}

// workBuddyMarketExperts lists (or searches) the expert market. A keyword goes
// in `keyword` — the other spellings are ignored upstream and answer the whole
// market.
func workBuddyMarketExperts(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential, expertType, keyword string) ([]workBuddyMarketExpert, error) {
	body := map[string]any{"page": 1, "page_size": 20}
	switch {
	case strings.TrimSpace(keyword) != "":
		body["keyword"] = strings.TrimSpace(keyword)
	case strings.TrimSpace(expertType) != "":
		body["expert_type"] = strings.TrimSpace(expertType)
		body["sort_by"] = "reco_rank"
		body["sort_order"] = "desc"
	}
	var data struct {
		Experts []workBuddyMarketExpert `json:"experts"`
	}
	err := workBuddyRetrySoftRate(ctx, func() error {
		data.Experts = nil
		return workBuddyGrowthCall(ctx, site, credential, false, http.MethodPost, workBuddyExpertListPath, body, &data)
	})
	if err != nil {
		return nil, err
	}
	return data.Experts, nil
}

// workBuddyFindExpert picks the market entry a task names: an exact id wins,
// then the first whose nickname or profession matches the name.
func workBuddyFindExpert(experts []workBuddyMarketExpert, id, name string) *workBuddyMarketExpert {
	name = strings.TrimSpace(name)
	for i := range experts {
		if id != "" && strings.EqualFold(experts[i].ExpertID, id) {
			return &experts[i]
		}
	}
	for i := range experts {
		display := strings.TrimSpace(experts[i].DisplayNameZH)
		profession := strings.TrimSpace(experts[i].ProfessionZH)
		if name == "" {
			return &experts[i]
		}
		if strings.Contains(display, name) || strings.Contains(profession, name) {
			return &experts[i]
		}
	}
	return nil
}
