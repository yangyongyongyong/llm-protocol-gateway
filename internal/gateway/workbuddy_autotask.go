package gateway

// WorkBuddy's 一键完成: the actions that move a 成长任务, and the state machine
// that decides what actually happened.
//
// A task is only ever "done" when the vendor says its reward was claimed or its
// counter reached the target. Nothing here infers a finish from "the events were
// sent": the scoring is asynchronous and sometimes rolled back, so an action that
// ran is reported as awaiting_progress until a read-back proves otherwise. That
// rule is what keeps a run from burning the same action twice for nothing.
//
// Order matters. first_buddy is unlocked by the day's activity report, so its
// action reports activity first; Sequential_Tasks_* unlock one per midnight on
// the vendor's side and simply read as locked until then.
//
// Two things are deliberately cheap:
//   - a task whose counter already reached its target but was never claimed is
//     claimed without running the action (the reward is sitting there, and
//     re-running a real conversation would spend credits for nothing);
//   - a task this file has no action for still gets claimed once its counter is
//     full (see workBuddyClaimOnlyAction) — otherwise it stays blocked forever.
//
// International accounts are read-only: their growth center serves the list but
// turns every write away, so nothing here fires for them.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// The pacing every action keeps. They are vars so a test can shrink them; the
// production values are what the vendor's own scripts measured.
var (
	// workBuddyReportGap is the gap between two consecutive report calls. Bursts
	// are what the vendor's risk check looks for.
	workBuddyReportGap = 1050 * time.Millisecond
	// workBuddyMPEventGap is the miniprogram chat pace. Its five-chat task is
	// rollback-checked: events fired seconds apart are counted and then undone,
	// so the only pace that survives is a human one.
	workBuddyMPEventGap = 45 * time.Second
	// workBuddyMPActionGap separates accept / report / claim on an mp task.
	workBuddyMPActionGap = 2 * time.Second
	// workBuddyExpertSummonGap separates two expert summons.
	workBuddyExpertSummonGap = 6 * time.Second
	// workBuddyTemplateGap separates the five template reports.
	workBuddyTemplateGap = 300 * time.Millisecond
	// workBuddyAppearanceGap lets a theme apply settle before its event.
	workBuddyAppearanceGap = 2 * time.Second
)

// The vendor's task codes this gateway knows how to move, and the two campaign
// constants they name.
const (
	workBuddyThemeKey            = "theme-tkmw7j" // 和平精英激战金秋, the Hp_Appearance theme
	workBuddyLibraryIntroURL     = "https://www.workbuddy.cn/space/d/o0KWYeynteVv06UnAZqIFm"
	workBuddyLightCloudExpertID  = "ex_2cvvUZQhDyeJ" // 腾讯轻量云专家
	workBuddyLightCloudExpert    = "腾讯轻量云专家"
	workBuddyValorantExpertID    = "ex_xQHHD1u5Jkua" // 无畏契约拼豆专家
	workBuddyValorantExpert      = "无畏契约拼豆专家"
	workBuddyPlaybookCaseID      = "pm-gtm-launch-plan"
	workBuddyPlaybookCaseName    = "新产品上市 GTM 发布计划一页纸"
	workBuddyBuddyAppID          = "cb_y5Dy46tPQGGWtueMxXbe" // 企鹅教师助手
	workBuddyBuddyAppName        = "企鹅教师助手"
	workBuddyProbeModelCN        = "deepseek-v4.1-flash"
	workBuddyProbeModelIntl      = "fast-model"
	workBuddyGlobalReadOnlyNote  = "国际区任务仅支持查询，请在官方客户端操作"
	workBuddyTaskNeedsClientNote = "该任务需要客户端内交互，没有对应的接口，请在 WorkBuddy 客户端操作"
)

// workBuddyMPTaskCodes are the miniprogram-only tasks: the default list does not
// carry them, and accept/claim need X-Client-Platform: miniprogram.
var workBuddyMPTaskCodes = map[string]bool{
	"Sequential_Tasks_1": true, // 小程序内完成 1 次有效对话
	"Sequential_Tasks_2": true, // 小程序内选中专家并完成一次对话
	"Sequential_Tasks_3": true, // 小程序内完成 5 次有效对话
	"Sequential_Tasks_4": true, // 小程序内创建定时任务（每日解锁一环）
	"Sequential_Tasks_5": true, // 小程序内使用 GLM5.2
	"Sequential_Tasks_6": true, // 小程序内完成 10 次有效对话
	"Sequential_Tasks_7": true, // 小程序内体验灵感功能
}

// workBuddyMPTaskCode reports whether code is a miniprogram-only task.
func workBuddyMPTaskCode(code string) bool {
	return workBuddyMPTaskCodes[strings.TrimSpace(code)]
}

// workBuddyAction is one task this gateway can move.
type workBuddyAction struct {
	TaskCode string
	Desc     string
	// Attempt marks an action whose scoring the vendor has not confirmed for a
	// pure API client: it runs, but a run that stays at awaiting_progress is an
	// expected answer rather than a bug.
	Attempt bool
	// ClaimOnly actions run nothing: the executor only claims them once their
	// counter is full. It is the fallback for a task this file has no action for.
	ClaimOnly bool
	MP        bool
	run       func(ctx context.Context, sess *workBuddyAutoSession, action *workBuddyAction) (string, error)
}

// workBuddyActions is the action table. Its order is the run order: the tasks
// other tasks depend on come first.
//
// It is filled in by init rather than declared in place because reading a task
// list needs the table (a task's "automatable" flag comes from it), so a var
// initialiser here would be an initialisation cycle.
var workBuddyActions []workBuddyAction

func init() {
	workBuddyActions = []workBuddyAction{
		{TaskCode: "chat_5", Desc: "上报 5 条对话活跃事件（按差额补足）", run: runWorkBuddyChat5},
		{TaskCode: "first_buddy", Desc: "先报活跃解锁 → 同意协议 → 领取第一只 Buddy（+300）", run: runWorkBuddyFirstBuddy},
		{TaskCode: "RichMeow_Chat", Desc: "上报桌面端完整对话事件链", run: runWorkBuddyRichMeow},
		{TaskCode: "Buddy_App", Desc: "上报「进入 Buddy 应用」事件链", run: runWorkBuddyBuddyApp},
		{TaskCode: "Buddy_App_QQ", Desc: "上报「进入 Buddy 应用」事件链（同一组事件）", run: runWorkBuddyBuddyApp},
		{TaskCode: "automation_1", Desc: "上报「定时任务创建成功」事件", run: runWorkBuddyAutomationCreate},
		{TaskCode: "Library_read", Desc: "上报「打开资料库介绍」事件", run: runWorkBuddyLibraryRead},
		{TaskCode: "template_5", Desc: "上报「用模板创建任务」事件组 ×5", run: runWorkBuddyTemplateUse},
		{TaskCode: "playbook_prompt", Desc: "上报「灵感案例发送 Prompt」事件组", run: runWorkBuddyPlaybookPrompt},
		{TaskCode: "Hp_Appearance", Desc: "应用主题 + 上报皮肤生效事件", run: runWorkBuddyAppearance},
		{TaskCode: "skill_1", Desc: "真实对话 + 上报技能加载事件", Attempt: true, run: runWorkBuddySkill},
		{TaskCode: "expert_5", Desc: "真实专家召唤 + 使用链 ×5", Attempt: true, run: runWorkBuddyExpertUse},
		{TaskCode: "Expert_team_use_3", Desc: "专家团召唤 + 使用链 ×3", Attempt: true, run: runWorkBuddyExpertTeamUse},
		{TaskCode: "Expert_lighthouse", Desc: "腾讯轻量云专家召唤 + 使用链", Attempt: true, run: runWorkBuddyExpertCampaign(workBuddyLightCloudExpertID, workBuddyLightCloudExpert)},
		{TaskCode: "WorkBuddy_Valorant", Desc: "无畏契约拼豆专家召唤 + 使用链", Attempt: true, run: runWorkBuddyExpertCampaign(workBuddyValorantExpertID, workBuddyValorantExpert)},
		{TaskCode: "expert_use_maidehao", Desc: "中国电商运营专家召唤 + 使用链", Attempt: true, run: runWorkBuddyExpertCampaign("ex_GDl31WahyIH0", "中国电商运营专家")},
		{TaskCode: "expert_use_douyinyin", Desc: "抖音策略师召唤 + 使用链", Attempt: true, run: runWorkBuddyExpertCampaign("ex_FbY5nlWTqDod", "抖音策略师")},
		{TaskCode: "expert_use_shangjia_xuanchuan", Desc: "商家宣传页与海报设计师召唤 + 使用链", Attempt: true, run: runWorkBuddyExpertCampaign("ex_Weo3x2EZuSDm", "商家宣传页与海报设计师")},
		{TaskCode: "expert_use_bowangwang", Desc: "直播电商教练召唤 + 使用链", Attempt: true, run: runWorkBuddyExpertCampaign("ex_WZY136wV5aMt", "直播电商教练")},
		{TaskCode: "Sequential_Tasks_1", Desc: "小程序对话上报（小程序口径）", MP: true, run: runWorkBuddyMPChat},
		{TaskCode: "Sequential_Tasks_2", Desc: "小程序专家使用上报（小程序口径）", MP: true, run: runWorkBuddyMPExpert},
		{TaskCode: "Sequential_Tasks_3", Desc: "小程序对话上报 ×5（真人节奏）", MP: true, run: runWorkBuddyMPChat},
		{TaskCode: "Sequential_Tasks_4", Desc: "定时任务创建事件（小程序口径）", MP: true, run: runWorkBuddyMPAutomation},
		{TaskCode: "Sequential_Tasks_5", Desc: "带模型字段的小程序对话上报", MP: true, run: runWorkBuddyMPModelChat},
		{TaskCode: "Sequential_Tasks_6", Desc: "小程序对话上报 ×target（真人节奏）", MP: true, run: runWorkBuddyMPChat},
		{TaskCode: "Sequential_Tasks_7", Desc: "小程序灵感事件组上报", MP: true, run: runWorkBuddyMPPlaybook},
	}
}

// workBuddyActionFor finds a task's action, or nil when this gateway runs
// nothing for it.
func workBuddyActionFor(code string) *workBuddyAction {
	trimmed := strings.TrimSpace(code)
	for i := range workBuddyActions {
		if workBuddyActions[i].TaskCode == trimmed {
			return &workBuddyActions[i]
		}
	}
	return nil
}

// workBuddyQueuedActionFor is the action the run actually executes: a task with
// a known action runs it, and any other task gets the claim-only fallback, which
// runs nothing and only claims a full counter.
func workBuddyQueuedActionFor(code string) *workBuddyAction {
	if action := workBuddyActionFor(code); action != nil {
		return action
	}
	return &workBuddyAction{TaskCode: strings.TrimSpace(code), Desc: "补领：已达标未领奖（无自动动作）", ClaimOnly: true, MP: workBuddyMPTaskCode(code)}
}

// ---- the session -----------------------------------------------------------

// workBuddyAutoSession is one account's run: where it goes, who it is, and the
// credential every call carries.
type workBuddyAutoSession struct {
	server     *Server
	site       workBuddySite
	provider   domain.Provider
	credential domain.WorkBuddyCredential
}

// readOnly says the account is on the international build, whose growth center
// answers reads and refuses every write.
func (sess *workBuddyAutoSession) readOnly() bool { return sess.site.intl }

// workBuddyServerRequestIDPattern is the shape of a chat's server-side request
// id: 32 hex digits, optionally with the cmb- prefix.
var workBuddyServerRequestIDPattern = regexp.MustCompile(`^(cmb-)?[0-9a-f]{32}$`)

// workBuddyProbeModel is the model the probe conversation names: a cheap one of
// the build's own list.
func workBuddyProbeModel(site workBuddySite) string {
	if site.intl {
		return workBuddyProbeModelIntl
	}
	return workBuddyProbeModelCN
}

// workBuddyProbeChat runs one real, tiny conversation and returns the ids the
// vendor assigned: the expert and skill tasks join their events to a request id
// the server itself minted, and an invented one is not counted. The conversation
// is replayed with the desktop chat header family, so what it leaves behind looks
// like a real turn.
func workBuddyProbeChat(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential, expertID string) (string, string, error) {
	conversationID := workBuddyNewConversationID()
	requestID := workBuddyNewMessageID()
	body, err := json.Marshal(map[string]any{
		"model": workBuddyProbeModel(site),
		"messages": []any{
			map[string]any{"role": "system", "content": workBuddySystemPrompt},
			map[string]any{"role": "user", "content": "1+1等于几？直接回答。"},
		},
		"agent": "cli", "temperature": 1, "stream": true,
		"stream_options": map[string]any{"include_usage": true},
	})
	if err != nil {
		return "", "", err
	}
	probeCtx := withWorkBuddyTurnRequestID(withWorkBuddyConversationID(ctx, conversationID), requestID)
	request, err := http.NewRequestWithContext(probeCtx, http.MethodPost, site.chatBaseURL()+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	applyWorkBuddyChatHeaders(request, site, &credential, workBuddyAgentTypeMain)
	if id := strings.TrimSpace(expertID); id != "" {
		request.Header.Set("X-Expert-Id", id)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 300))
		return "", "", fmt.Errorf("对话返回 HTTP %d：%s", response.StatusCode, strings.TrimSpace(string(detail)))
	}
	// The server's id arrives in the stream; read no more than a display page of
	// it, then drop the rest (the turn itself is of no interest here).
	buffer := make([]byte, 0, 64<<10)
	chunk := make([]byte, 8192)
	searchFrom := 0
	for len(buffer) < 1<<20 {
		read, readErr := response.Body.Read(chunk)
		if read > 0 {
			buffer = append(buffer, chunk[:read]...)
			for {
				at := bytes.Index(buffer[searchFrom:], []byte(`"id":"`))
				if at < 0 {
					break
				}
				start := searchFrom + at + len(`"id":"`)
				end := bytes.IndexByte(buffer[start:], '"')
				if end < 0 {
					break
				}
				candidate := string(buffer[start : start+end])
				if workBuddyServerRequestIDPattern.MatchString(candidate) {
					return conversationID, candidate, nil
				}
				searchFrom = start
			}
		}
		if readErr != nil {
			break
		}
	}
	return "", "", fmt.Errorf("对话流里没有服务端的 requestId")
}

// waitWorkBuddyContext sleeps unless the run is cancelled first, and reports
// whether it may carry on.
func waitWorkBuddyContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// ---- the executor ----------------------------------------------------------

// The statuses a task's result carries. Status is the only completion evidence:
// done means the vendor confirmed the claim, and nothing else does.
const (
	workBuddyStatusDone             = "done"
	workBuddyStatusClaimPending     = "claim_pending"
	workBuddyStatusAwaitingProgress = "awaiting_progress"
	workBuddyStatusSkipped          = "skipped"
	workBuddyStatusError            = "error"
	workBuddyStatusAccepted         = "accepted"
)

// workBuddyAutoOutcome is what one task's run came to.
type workBuddyAutoOutcome struct {
	Status         string
	Message        string
	ProgressBefore string
	ProgressAfter  string
	Claimable      bool
	Claimed        bool
	Credit         float64
	Energy         float64
	ClaimError     string
	Attempt        bool
	ActionRun      bool
}

// resultMap is the JSON the console reads.
func (o workBuddyAutoOutcome) resultMap(action *workBuddyAction) map[string]any {
	result := map[string]any{
		"ok":             o.Status != workBuddyStatusError,
		"status":         o.Status,
		"message":        o.Message,
		"progressBefore": o.ProgressBefore,
		"progressAfter":  o.ProgressAfter,
		"claimable":      o.Claimable,
		"claimed":        o.Claimed,
		"attempt":        o.Attempt,
		"actionRun":      o.ActionRun,
	}
	if action != nil {
		result["taskCode"] = action.TaskCode
		result["desc"] = action.Desc
	}
	if o.Credit != 0 || o.Energy != 0 {
		result["credit"] = o.Credit
		result["energy"] = o.Energy
	}
	if o.ClaimError != "" {
		result["claimError"] = o.ClaimError
	}
	return result
}

// execute moves one task and says what came of it: read the task, skip what is
// gone or locked, claim what is already full, run the action, read back, claim
// only what the vendor confirms is full.
func (sess *workBuddyAutoSession) execute(ctx context.Context, action *workBuddyAction) workBuddyAutoOutcome {
	out := workBuddyAutoOutcome{Attempt: action.Attempt}
	if sess.readOnly() {
		out.Status = workBuddyStatusSkipped
		out.Message = workBuddyGlobalReadOnlyNote
		return out
	}
	before, err := workBuddyTaskByCode(ctx, sess.site, sess.credential, action.TaskCode, action.MP)
	if err != nil {
		out.Status = workBuddyStatusError
		out.Message = "查询任务失败：" + err.Error()
		return out
	}
	return sess.executeWith(ctx, action, before, out)
}

// executeWith finishes execute for a task that has already been read: the run
// hands in the task from its own snapshot, so one listing per pass serves every
// action instead of one per action (the growth endpoint is rate-limited, and a
// pass fires dozens of calls).
func (sess *workBuddyAutoSession) executeWith(ctx context.Context, action *workBuddyAction, before *workBuddyTask, out workBuddyAutoOutcome) workBuddyAutoOutcome {
	if before == nil {
		out.Status = workBuddyStatusSkipped
		out.Message = "该账号没有此任务"
		return out
	}
	out.ProgressBefore = workBuddyTaskProgressText(before)
	if before.Locked {
		out.Status = workBuddyStatusSkipped
		out.Message = "任务尚未解锁"
		return out
	}
	if before.Claimed {
		out.Status = workBuddyStatusDone
		out.Claimed = true
		out.Message = "该任务已领取过奖励"
		out.ProgressAfter = workBuddyTaskProgressText(before)
		return out
	}
	// first_buddy is the exception to "full counter means go claim": its reward
	// sits behind the adoption chain, so a full counter still needs the action.
	if workBuddyTaskProgressReached(before) && action.TaskCode != "first_buddy" {
		return sess.claim(ctx, action, "任务已达标", before, before)
	}
	if action.ClaimOnly || action.run == nil {
		out.Status = workBuddyStatusSkipped
		out.Message = "该任务没有可执行的自动动作"
		return out
	}
	message, runErr := action.run(ctx, sess, action)
	out.ActionRun = true
	if runErr != nil {
		out.Status = workBuddyStatusError
		out.Message = "执行失败：" + runErr.Error()
		return out
	}
	after, err := workBuddyTaskByCodeWaiting(ctx, sess.site, sess.credential, action.TaskCode, action.MP)
	if err != nil {
		out.Status = workBuddyStatusError
		out.Message = message + "；回读进度失败：" + err.Error()
		if after != nil {
			out.ProgressAfter = workBuddyTaskProgressText(after)
			out.Claimable = workBuddyTaskProgressReached(after)
		}
		return out
	}
	if after == nil {
		out.Status = workBuddyStatusAwaitingProgress
		out.Message = message + "；回读时未找到该任务，暂时无法确认完成"
		return out
	}
	out.ProgressAfter = workBuddyTaskProgressText(after)
	out.Claimable = workBuddyTaskProgressReached(after)
	if after.Claimed {
		out.Status = workBuddyStatusDone
		out.Claimed = true
		out.Message = message + "；服务端已确认领取"
		return out
	}
	if !out.Claimable {
		out.Status = workBuddyStatusAwaitingProgress
		out.Message = message + "；服务端尚未计分，等入账后自动补领"
		return out
	}
	return sess.claim(ctx, action, message, before, after)
}

// claim asks for a task's reward and reports what landed.
func (sess *workBuddyAutoSession) claim(ctx context.Context, action *workBuddyAction, message string, before, after *workBuddyTask) workBuddyAutoOutcome {
	out := workBuddyAutoOutcome{
		Status:         workBuddyStatusDone,
		Message:        message,
		ProgressBefore: workBuddyTaskProgressText(before),
		ProgressAfter:  workBuddyTaskProgressText(after),
		Claimable:      true,
		Attempt:        action.Attempt,
	}
	credit, energy, err := workBuddyClaimTask(ctx, sess.site, sess.credential, action.TaskCode, action.MP)
	if err != nil {
		out.Status = workBuddyStatusClaimPending
		out.ClaimError = err.Error()
		out.Message = message + "；已达标，但领奖失败：" + err.Error()
		return out
	}
	out.Claimed = true
	out.Credit, out.Energy = credit, energy
	switch {
	case credit > 0 || energy > 0:
		out.Message = fmt.Sprintf("%s；已自动领奖 +%g 积分 +%g 能量", message, credit, energy)
	default:
		out.Message = message + "；领奖接口已确认（奖励此前可能已领取）"
	}
	return out
}

// ---- one-click run ---------------------------------------------------------

// workBuddyAutoEvent is one progress line of a run: a phase, a task starting, or
// a task's result.
type workBuddyAutoEvent struct {
	Kind     string         `json:"kind"` // phase | task_start | result
	TaskCode string         `json:"taskCode,omitempty"`
	Message  string         `json:"message,omitempty"`
	Result   map[string]any `json:"result,omitempty"`
}

// runAll moves every task the account has that this gateway can move. A task's
// failure never stops the run: each one is read, run and reported on its own.
//
// The snapshot taken first serves two purposes — the tasks the account does not
// have are skipped without a call (one listing of ~26 tasks per account each
// would be a rate-limit problem of its own), and the ones already claimed or
// still locked are skipped the same way.
func (sess *workBuddyAutoSession) runAll(ctx context.Context, observe func(workBuddyAutoEvent)) []map[string]any {
	results := make([]map[string]any, 0, len(workBuddyActions))
	emit := func(event workBuddyAutoEvent) {
		if observe != nil {
			observe(event)
		}
	}
	if sess.readOnly() {
		return append(results, map[string]any{
			"taskCode": "(全部)", "status": workBuddyStatusSkipped, "message": workBuddyGlobalReadOnlyNote,
		})
	}

	var have, done map[string]bool
	// snapshot keeps the tasks themselves, not just their codes: the executor can
	// then start from the counter the listing already showed instead of spending
	// one more call per task.
	snapshot := map[string]*workBuddyTask{}
	record := func(tasks []workBuddyTask) {
		for i := range tasks {
			task := &tasks[i]
			have[task.TaskCode] = true
			snapshot[task.TaskCode] = task
			if task.Claimed || task.Locked {
				done[task.TaskCode] = true
			}
		}
	}

	// Phase 0: accept whatever has not been accepted yet. Accepting grants no
	// progress, so a failure here is reported and the run carries on.
	emit(workBuddyAutoEvent{Kind: "phase", TaskCode: "(批量接受)"})
	if tasks, err := workBuddyListTasks(ctx, sess.site, sess.credential, false); err == nil {
		have = map[string]bool{}
		done = map[string]bool{}
		record(tasks)
		if codes := workBuddyAcceptPendingCodes(tasks); len(codes) > 0 {
			if err := workBuddyAcceptTasks(ctx, sess.site, sess.credential, false, codes); err != nil {
				result := map[string]any{"taskCode": "(批量接受)", "status": workBuddyStatusError,
					"message": "接受任务失败（不影响后续）: " + err.Error()}
				results = append(results, result)
				emit(workBuddyAutoEvent{Kind: "result", TaskCode: "(批量接受)", Result: result})
			} else {
				result := map[string]any{"taskCode": "(批量接受)", "status": workBuddyStatusAccepted,
					"message": fmt.Sprintf("已报名 %d 个任务", len(codes))}
				results = append(results, result)
				emit(workBuddyAutoEvent{Kind: "result", TaskCode: "(批量接受)", Result: result})
				if !waitWorkBuddyContext(ctx, workBuddyReportGap) {
					return results
				}
			}
		}
	} else if ctx.Err() != nil {
		return results
	}

	// Phase 0b: the miniprogram list, accepted separately — it is a superset that
	// also carries tasks the default list does not have.
	emit(workBuddyAutoEvent{Kind: "phase", TaskCode: "(批量接受-小程序)"})
	if tasks, err := workBuddyListTasks(ctx, sess.site, sess.credential, true); err == nil {
		have = map[string]bool{}
		done = map[string]bool{}
		record(tasks)
		if codes := workBuddyAcceptPendingCodes(tasks); len(codes) > 0 {
			if err := workBuddyAcceptTasks(ctx, sess.site, sess.credential, true, codes); err == nil {
				result := map[string]any{"taskCode": "(批量接受-小程序)", "status": workBuddyStatusAccepted,
					"message": fmt.Sprintf("已报名 %d 个小程序任务", len(codes))}
				results = append(results, result)
				emit(workBuddyAutoEvent{Kind: "result", TaskCode: "(批量接受-小程序)", Result: result})
				if !waitWorkBuddyContext(ctx, workBuddyReportGap) {
					return results
				}
			}
		}
	}

	for i := range workBuddyActions {
		if ctx.Err() != nil {
			break
		}
		action := &workBuddyActions[i]
		if reason := workBuddySkipReason(action.TaskCode, have, done); reason != "" {
			result := map[string]any{"taskCode": action.TaskCode, "status": workBuddyStatusSkipped, "message": reason}
			results = append(results, result)
			emit(workBuddyAutoEvent{Kind: "result", TaskCode: action.TaskCode, Result: result})
			continue
		}
		emit(workBuddyAutoEvent{Kind: "task_start", TaskCode: action.TaskCode})
		outcome := sess.executeWith(ctx, action, snapshot[action.TaskCode], workBuddyAutoOutcome{Attempt: action.Attempt})
		result := outcome.resultMap(action)
		results = append(results, result)
		emit(workBuddyAutoEvent{Kind: "result", TaskCode: action.TaskCode, Result: result})
		if !waitWorkBuddyContext(ctx, workBuddyReportGap) {
			break
		}
	}
	return results
}

// workBuddySkipReason is the snapshot verdict for one task: the account does not
// have it, or it is already claimed or still locked. An empty string means run
// it. A nil have (the first listing failed) keeps everything eligible: one failed
// read must not skip a whole run.
func workBuddySkipReason(code string, have, done map[string]bool) string {
	if have != nil && !have[code] {
		return "该账号没有此任务"
	}
	if done[code] {
		return "任务已领取或尚未解锁"
	}
	return ""
}

// workBuddyAcceptPendingCodes are the tasks worth 报名: not claimed, not locked,
// not full, and not accepted yet.
func workBuddyAcceptPendingCodes(tasks []workBuddyTask) []string {
	codes := make([]string, 0, len(tasks))
	for i := range tasks {
		task := &tasks[i]
		if task.Claimed || task.Locked || workBuddyTaskProgressReached(task) {
			continue
		}
		if task.AcceptStatus == "accepted" || task.AcceptStatus == "completed" {
			continue
		}
		codes = append(codes, task.TaskCode)
	}
	return codes
}

// sweep is the 补领 pass: accept what is pending and claim what is full, running
// no action at all. It is what catches a task whose counter filled up after the
// run (the vendor scores asynchronously) and every task this file has no action
// for.
func (sess *workBuddyAutoSession) sweep(ctx context.Context) (accepted, claimed int, err error) {
	if sess.readOnly() {
		return 0, 0, nil
	}
	tasks, listErr := workBuddyListTasks(ctx, sess.site, sess.credential, false)
	if listErr != nil {
		return 0, 0, listErr
	}
	mpTasks, mpErr := workBuddyListTasks(ctx, sess.site, sess.credential, true)
	if mpErr != nil {
		mpTasks = nil
	}
	if codes := workBuddyAcceptPendingCodes(tasks); len(codes) > 0 {
		if acceptErr := workBuddyAcceptTasks(ctx, sess.site, sess.credential, false, codes); acceptErr == nil {
			accepted += len(codes)
		}
	}
	if codes := workBuddyAcceptPendingCodes(mpTasks); len(codes) > 0 {
		if acceptErr := workBuddyAcceptTasks(ctx, sess.site, sess.credential, true, codes); acceptErr == nil {
			accepted += len(codes)
		}
	}
	// The mp list is a superset, so the two are de-duplicated by code before
	// anything is claimed twice.
	onlyMP := map[string]bool{}
	for _, task := range mpTasks {
		onlyMP[task.TaskCode] = true
	}
	for _, task := range tasks {
		onlyMP[task.TaskCode] = false
	}
	seen := map[string]bool{}
	for _, task := range append(append([]workBuddyTask{}, tasks...), mpTasks...) {
		if task.TaskCode == "" || seen[task.TaskCode] {
			continue
		}
		seen[task.TaskCode] = true
		if !task.Claimable || task.Locked {
			continue
		}
		mp := workBuddyMPTaskCode(task.TaskCode) || onlyMP[task.TaskCode]
		if _, _, claimErr := workBuddyClaimTask(ctx, sess.site, sess.credential, task.TaskCode, mp); claimErr != nil {
			continue
		}
		claimed++
	}
	return accepted, claimed, nil
}

// ---- the actions -----------------------------------------------------------

// runWorkBuddyChat5 fills chat_5's counter: one activity report per missing
// chat, paced.
func runWorkBuddyChat5(ctx context.Context, sess *workBuddyAutoSession, action *workBuddyAction) (string, error) {
	task, err := workBuddyTaskByCode(ctx, sess.site, sess.credential, "chat_5", false)
	if err != nil {
		return "", err
	}
	if task == nil {
		return "", fmt.Errorf("任务不存在")
	}
	target := task.Target
	if target <= 0 {
		target = 5
	}
	need := int(target - task.Current)
	if need <= 0 {
		return "进度已达标，无需上报", nil
	}
	for i := 0; i < need; i++ {
		if err := workBuddyReportActivity(ctx, sess.site, sess.credential); err != nil {
			return "", fmt.Errorf("上报第 %d/%d 条失败: %w", i+1, need, err)
		}
		if i < need-1 && !waitWorkBuddyContext(ctx, workBuddyReportGap) {
			return "", ctx.Err()
		}
	}
	return fmt.Sprintf("已补报 %d 条对话活跃事件", need), nil
}

// runWorkBuddyFirstBuddy is 领养: the report that unlocks the task, the agreement,
// then the adoption itself.
func runWorkBuddyFirstBuddy(ctx context.Context, sess *workBuddyAutoSession, action *workBuddyAction) (string, error) {
	if err := workBuddyReportActivity(ctx, sess.site, sess.credential); err != nil {
		return "", fmt.Errorf("前置活跃上报: %w", err)
	}
	if !waitWorkBuddyContext(ctx, workBuddyReportGap) {
		return "", ctx.Err()
	}
	if err := workBuddyBuddyAgree(ctx, sess.site, sess.credential); err != nil {
		return "", fmt.Errorf("同意协议: %w", err)
	}
	if err := workBuddyBuddyFirst(ctx, sess.site, sess.credential); err != nil {
		if workBuddyBuddyIncomplete(err) {
			// The vendor wants a day of activity behind the adoption; the report
			// just sent is today's, so the next run picks it up.
			return "活跃已上报，领养门槛今天还没过（上游要求当日活跃），等下一轮再试", nil
		}
		return "", fmt.Errorf("领取 Buddy: %w", err)
	}
	return "已领取第一只 Buddy（+300 积分 +8 能量）", nil
}

// runWorkBuddyRichMeow reports a whole desktop conversation: the chain the
// RichMeow_Chat counter follows.
func runWorkBuddyRichMeow(ctx context.Context, sess *workBuddyAutoSession, action *workBuddyAction) (string, error) {
	conversationID := workBuddyNewConversationID()
	requestID := workBuddyNewMessageID()
	events := workBuddyChatSequence(conversationID, requestID, workBuddyNewMessageID(), workBuddyProbeModel(sess.site), workBuddyProbeModel(sess.site))
	if err := workBuddyReportDesktop(ctx, sess.site, sess.credential, events...); err != nil {
		return "", err
	}
	return "已上报桌面端完整对话事件链（agent_task_created → chat_response）", nil
}

// runWorkBuddyBuddyApp reports "entered a Buddy app": the five clicks.
func runWorkBuddyBuddyApp(ctx context.Context, sess *workBuddyAutoSession, action *workBuddyAction) (string, error) {
	if err := workBuddyReportDesktop(ctx, sess.site, sess.credential, workBuddyBuddyAppSequence(workBuddyBuddyAppID, workBuddyBuddyAppName)...); err != nil {
		return "", err
	}
	return "已上报进入 Buddy 应用事件链（同时覆盖 Buddy_App 与 Buddy_App_QQ）", nil
}

// runWorkBuddyAutomationCreate reports "a scheduled task was created".
func runWorkBuddyAutomationCreate(ctx context.Context, sess *workBuddyAutoSession, action *workBuddyAction) (string, error) {
	if err := workBuddyReportDesktop(ctx, sess.site, sess.credential, workBuddyAutomationCreateEvent("自动化")); err != nil {
		return "", err
	}
	return "已上报定时任务创建成功事件", nil
}

// runWorkBuddyLibraryRead reports the library intro being opened, which is the
// only event Library_read counts.
func runWorkBuddyLibraryRead(ctx context.Context, sess *workBuddyAutoSession, action *workBuddyAction) (string, error) {
	if err := workBuddyReportWebEvent(ctx, sess.site, sess.credential, "web_element_click",
		workBuddyLibraryIntroURL, "library_doc_intro_click", "WorkBuddy资料库介绍"); err != nil {
		return "", err
	}
	return "已上报资料库介绍阅读事件", nil
}

// runWorkBuddyTemplateUse reports five template-created tasks: template_5 counts
// groups, and one per template is what fills it.
func runWorkBuddyTemplateUse(ctx context.Context, sess *workBuddyAutoSession, action *workBuddyAction) (string, error) {
	templates := [][2]string{
		{"1", "深度研究"}, {"2", "周报生成"}, {"3", "竞品分析"},
		{"4", "活动策划"}, {"5", "代码评审"},
	}
	for i, template := range templates {
		events := workBuddyTemplateUseSequence(workBuddyNewConversationID(), workBuddyNewMessageID(), template[0], template[1])
		if err := workBuddyReportDesktop(ctx, sess.site, sess.credential, events...); err != nil {
			return "", fmt.Errorf("第 %d 组模板事件上报失败: %w", i+1, err)
		}
		if !waitWorkBuddyContext(ctx, workBuddyTemplateGap) {
			return "", ctx.Err()
		}
	}
	return "已上报 template_used ×5", nil
}

// runWorkBuddyPlaybookPrompt reports sending a prompt from an inspiration case.
// The prompt-send event is what counts; the two clicks lead up to it.
func runWorkBuddyPlaybookPrompt(ctx context.Context, sess *workBuddyAutoSession, action *workBuddyAction) (string, error) {
	events := workBuddyPlaybookPromptSequence(workBuddyNewConversationID(), workBuddyNewMessageID(),
		workBuddyPlaybookCaseID, workBuddyPlaybookCaseName)
	if err := workBuddyReportDesktop(ctx, sess.site, sess.credential, events...); err != nil {
		return "", err
	}
	return "已上报 playbook_cta_click + playbook_prompt_send", nil
}

// runWorkBuddyAppearance applies a theme and reports it taking effect. The theme
// alone does not score — the client reports the apply when it leaves settings.
func runWorkBuddyAppearance(ctx context.Context, sess *workBuddyAutoSession, action *workBuddyAction) (string, error) {
	if err := workBuddySetAppearanceTheme(ctx, sess.site, sess.credential, workBuddyThemeKey); err != nil {
		return "", fmt.Errorf("设置主题: %w", err)
	}
	if !waitWorkBuddyContext(ctx, workBuddyAppearanceGap) {
		return "", ctx.Err()
	}
	if err := workBuddyReportDesktop(ctx, sess.site, sess.credential, workBuddyAppearanceApplyEvent(workBuddyThemeKey)); err != nil {
		return "", err
	}
	return "已应用主题并上报皮肤生效事件", nil
}

// runWorkBuddySkill runs a real turn and reports a skill load joined to it.
func runWorkBuddySkill(ctx context.Context, sess *workBuddyAutoSession, action *workBuddyAction) (string, error) {
	conversationID, requestID, err := workBuddyProbeChat(ctx, sess.site, sess.credential, "")
	if err != nil {
		return "", fmt.Errorf("真实对话: %w", err)
	}
	messageID := "msg-" + requestID
	if len(requestID) > 8 {
		messageID = "msg-" + requestID[len(requestID)-8:]
	}
	events := workBuddyChatSequence(conversationID, requestID, messageID, workBuddyProbeModel(sess.site), workBuddyProbeModel(sess.site))
	for _, event := range events {
		if code, _ := event["eventCode"].(string); code == "chat_message_response" {
			event["finishReason"] = "tool_calls"
		}
	}
	events = append(events, workBuddySkillInfoEvent(conversationID, requestID, messageID))
	if err := workBuddyReportDesktop(ctx, sess.site, sess.credential, events...); err != nil {
		return "", fmt.Errorf("skill_info 事件: %w", err)
	}
	return "已上报真实对话 + skill_info 技能加载事件", nil
}

// runWorkBuddyExpertUse fills expert_5: five real market experts summoned and
// used.
func runWorkBuddyExpertUse(ctx context.Context, sess *workBuddyAutoSession, action *workBuddyAction) (string, error) {
	return runWorkBuddyExpertBatch(ctx, sess, "agent", 5)
}

// runWorkBuddyExpertTeamUse fills Expert_team_use_3: three expert teams.
func runWorkBuddyExpertTeamUse(ctx context.Context, sess *workBuddyAutoSession, action *workBuddyAction) (string, error) {
	return runWorkBuddyExpertBatch(ctx, sess, "team", 3)
}

// runWorkBuddyExpertBatch summons and uses count experts of one kind. A failed
// expert is skipped and the next one is tried: the counter only needs the count.
func runWorkBuddyExpertBatch(ctx context.Context, sess *workBuddyAutoSession, expertType string, count int) (string, error) {
	experts, err := workBuddyMarketExperts(ctx, sess.site, sess.credential, expertType, "")
	if err != nil {
		return "", fmt.Errorf("拉取专家市场: %w", err)
	}
	if len(experts) == 0 {
		return "", fmt.Errorf("专家市场列表为空")
	}
	ok, failed := 0, 0
	for i, expert := range experts {
		if ok >= count {
			break
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if err := workBuddyReportDesktop(ctx, sess.site, sess.credential, workBuddyExpertSummonSequence(expert)...); err != nil {
			failed++
			continue
		}
		conversationID, requestID, chatErr := workBuddyProbeChat(ctx, sess.site, sess.credential, expert.ExpertID)
		if chatErr != nil {
			failed++
			continue
		}
		events := workBuddyChatSequence(conversationID, requestID, "msg-"+requestID[len(requestID)-8:], workBuddyProbeModel(sess.site), workBuddyProbeModel(sess.site))
		events = append(events, workBuddyExpertActualUseEvent(expert, conversationID, requestID, "craft"))
		if err := workBuddyReportDesktop(ctx, sess.site, sess.credential, events...); err != nil {
			failed++
			continue
		}
		ok++
		if i < len(experts)-1 && !waitWorkBuddyContext(ctx, workBuddyExpertSummonGap) {
			return "", ctx.Err()
		}
	}
	if ok < count {
		return "", fmt.Errorf("专家召唤+使用链只完成 %d/%d（%d 个失败）", ok, count, failed)
	}
	return fmt.Sprintf("已对 %d 位真实专家完成召唤+使用链（%s）", ok, expertType), nil
}

// runWorkBuddyExpertCampaign is the "experience this expert" family: one named
// expert, summoned and used with mode LOCAL, which is the shape those campaign
// tasks score.
func runWorkBuddyExpertCampaign(expertID, expertName string) func(ctx context.Context, sess *workBuddyAutoSession, action *workBuddyAction) (string, error) {
	return func(ctx context.Context, sess *workBuddyAutoSession, action *workBuddyAction) (string, error) {
		expert := workBuddyMarketExpert{
			ExpertID: expertID, ExpertType: "agent",
			DisplayNameZH: expertName, ProfessionZH: expertName, Version: "1.0.0",
		}
		// The market entry carries the authoritative version; the task's own id is
		// what it is matched by.
		if experts, err := workBuddyMarketExperts(ctx, sess.site, sess.credential, "agent", ""); err == nil {
			if found := workBuddyFindExpert(experts, expertID, expertName); found != nil {
				expert = *found
			}
		}
		if err := workBuddyReportDesktop(ctx, sess.site, sess.credential, workBuddyExpertSummonSequence(expert)...); err != nil {
			return "", fmt.Errorf("召唤链: %w", err)
		}
		if !waitWorkBuddyContext(ctx, workBuddyExpertSummonGap) {
			return "", ctx.Err()
		}
		conversationID, requestID, err := workBuddyProbeChat(ctx, sess.site, sess.credential, expert.ExpertID)
		if err != nil {
			return "", fmt.Errorf("真实对话: %w", err)
		}
		messageID := "msg-" + requestID
		if len(requestID) > 8 {
			messageID = "msg-" + requestID[len(requestID)-8:]
		}
		events := workBuddyChatSequence(conversationID, requestID, messageID, workBuddyProbeModel(sess.site), workBuddyProbeModel(sess.site))
		for _, event := range events {
			if code, _ := event["eventCode"].(string); code == "agent_task_created" {
				event["has_expert"] = true
				event["expert_id"] = expert.ExpertID
				event["expert_name"] = expert.DisplayNameZH
				event["expert_industry_id"] = ""
			}
		}
		use := workBuddyExpertActualUseEvent(expert, conversationID, requestID, "LOCAL")
		// The captured samples of this family carry an empty type and no cost.
		use["type"] = ""
		use["cost"] = 0
		events = append(events, use)
		if err := workBuddyReportDesktop(ctx, sess.site, sess.credential, events...); err != nil {
			return "", fmt.Errorf("使用事件: %w", err)
		}
		return fmt.Sprintf("已上报「%s」召唤+使用链", expert.ProfessionZH), nil
	}
}

// ---- miniprogram tasks -----------------------------------------------------

// runWorkBuddyMPChat fills one of the miniprogram conversation counters up to its
// target, at the paced rate the vendor's rollback check accepts.
func runWorkBuddyMPChat(ctx context.Context, sess *workBuddyAutoSession, action *workBuddyAction) (string, error) {
	return workBuddyMPChatTask(ctx, sess, action, nil)
}

// runWorkBuddyMPModelChat does the same, naming GLM-5.2 on each event.
func runWorkBuddyMPModelChat(ctx context.Context, sess *workBuddyAutoSession, action *workBuddyAction) (string, error) {
	return workBuddyMPChatTask(ctx, sess, action, func(conversationID string) map[string]any {
		return workBuddyMiniChatModelEvent(conversationID, "glm-5.2", "GLM-5.2")
	})
}

// workBuddyMPChatTask drives one Sequential_Tasks_* conversation task: accept
// (verified), report the missing events, read back, claim.
//
// Before accepting, the task's progress is empty and target reads 0 — a run that
// trusted that would report one event and call a five-chat task done, so the
// target is re-read after accepting.
func workBuddyMPChatTask(ctx context.Context, sess *workBuddyAutoSession, action *workBuddyAction, build func(conversationID string) map[string]any) (string, error) {
	code := ""
	if action != nil {
		code = action.TaskCode
	}
	if code == "" {
		return "", fmt.Errorf("内部错误：小程序任务缺少任务号")
	}
	task, err := workBuddyTaskByCode(ctx, sess.site, sess.credential, code, true)
	if err != nil {
		return "", err
	}
	if task == nil {
		return "小程序口径没有下发该任务（活动可能已结束）", nil
	}
	if task.Claimed {
		return "已领取", nil
	}
	if task.AcceptStatus == "not_accepted" || task.AcceptStatus == "" {
		if err := workBuddyAcceptTasks(ctx, sess.site, sess.credential, true, []string{code}); err != nil {
			return "", fmt.Errorf("报名失败: %w", err)
		}
		if !waitWorkBuddyContext(ctx, workBuddyMPActionGap) {
			return "", ctx.Err()
		}
		if reread, readErr := workBuddyTaskByCode(ctx, sess.site, sess.credential, code, true); readErr == nil && reread != nil {
			task = reread
		}
	}
	target := task.Target
	if target <= 0 {
		target = 1
	}
	if task.Current >= target || task.AcceptStatus == "completed" {
		credit, energy, claimErr := workBuddyClaimTask(ctx, sess.site, sess.credential, code, true)
		if claimErr != nil {
			return "", claimErr
		}
		return fmt.Sprintf("已领取奖励（+%g 积分 +%g 能量）", credit, energy), nil
	}
	need := int(target - task.Current)
	for i := 0; i < need; i++ {
		if i > 0 || task.Current > 0 {
			// A human pace between events: bursts are counted and then rolled back.
			jitter := time.Duration(workBuddyMPEventGap / 5)
			if !waitWorkBuddyContext(ctx, workBuddyMPEventGap+jitter) {
				return "", ctx.Err()
			}
		}
		conversationID := workBuddyNewConversationID()
		event := map[string]any(nil)
		if build != nil {
			event = build(conversationID)
		} else {
			event = workBuddyMiniChatSendEvent(conversationID)
		}
		if err := workBuddyReportMP(ctx, sess.site, sess.credential, event); err != nil {
			return "", fmt.Errorf("完成 %d/%d 次上报后中断: %w", i, need, err)
		}
	}
	return fmt.Sprintf("已补报 %d 条小程序对话事件", need), nil
}

// runWorkBuddyMPExpert reports the miniprogram expert-use event, which needs a
// real expert from the market.
func runWorkBuddyMPExpert(ctx context.Context, sess *workBuddyAutoSession, action *workBuddyAction) (string, error) {
	experts, err := workBuddyMarketExperts(ctx, sess.site, sess.credential, "agent", "")
	if err != nil {
		return "", fmt.Errorf("拉取专家市场: %w", err)
	}
	if len(experts) == 0 {
		return "", fmt.Errorf("专家市场列表为空")
	}
	expert := experts[0]
	event := workBuddyMiniExpertUseEvent(expert.ExpertID, expert.DisplayNameZH, expert.ExpertType)
	if err := workBuddyReportMP(ctx, sess.site, sess.credential, event); err != nil {
		return "", err
	}
	return fmt.Sprintf("已上报小程序专家使用事件（%s）", expert.DisplayNameZH), nil
}

// runWorkBuddyMPAutomation reports the scheduled-task event for the miniprogram
// chain. The mp client never emits one, so this is the desktop event the
// equivalent PC task counts.
func runWorkBuddyMPAutomation(ctx context.Context, sess *workBuddyAutoSession, action *workBuddyAction) (string, error) {
	if err := workBuddyReportDesktop(ctx, sess.site, sess.credential, workBuddyAutomationCreateEvent("自动化")); err != nil {
		return "", err
	}
	return "已上报定时任务创建成功事件（小程序口径复用 PC 事件）", nil
}

// runWorkBuddyMPPlaybook reports the miniprogram inspiration pair.
func runWorkBuddyMPPlaybook(ctx context.Context, sess *workBuddyAutoSession, action *workBuddyAction) (string, error) {
	events := workBuddyMiniPlaybookEvents(workBuddyPlaybookCaseID, workBuddyPlaybookCaseName)
	if err := workBuddyReportMP(ctx, sess.site, sess.credential, events...); err != nil {
		return "", err
	}
	return "已上报小程序灵感事件组（playbook_cta_click + playbook_prompt_send）", nil
}
