package gateway

// WorkBuddy's activity reporting: the /v2/report channel, the desktop client's
// fingerprint that rides on every event, and the event groups each task counts.
//
// A 成长任务 is a counter, and only the vendor's own telemetry moves it. The
// client batches these events to {web host}/v2/report as a JSON array; each
// event carries the account's device fingerprint (machineId, sessionId,
// qimei36, the desktop build triple) alongside its own fields, and the vendor
// drops an event whose userId is missing without saying so — an answer of 200
// is not proof anything was counted.
//
// Three families, each with its own signature:
//   - desktop: extName=workbuddy-desktop, the client's three-segment UA. What
//     the chat, template, playbook, buddy-app, automation, expert and skill
//     tasks count.
//   - web: pageURL/elementId, for the few tasks a browser session scores
//     (the library intro click).
//   - miniprogram: X-Client-Platform: mp-weixin on the request and
//     extName=workbuddy-mp in the body. The Sequential_Tasks_* chain counts
//     only these, and it wants a human pace: events fired seconds apart are
//     counted and then rolled back (see workBuddyMLEventGap).

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// The build triple a report claims. It is the client's, not this host's, which
// is why it is fixed rather than measured: a report that names the host's own
// platform is the one thing no real client does.
const (
	workBuddyReportCommit      = "b7f4a19c0e5d3f8126a4cbb0d9e77f13"
	workBuddyReportReleaseDate = int64(1761696000000) // the build's publish date
	workBuddyReportOSVersion   = "10.0.28000"
	workBuddyReportCPUModel    = "11th Gen Intel(R) Core(TM) i5-11320H @ 3.20GHz"
	workBuddyReportCPUCores    = 8
	workBuddyReportMemorySize  = 16
)

// ---- ids -------------------------------------------------------------------

// workBuddyNewConversationID is a conversation id in the vendor's shape: a v4 UUID.
func workBuddyNewConversationID() string {
	raw := make([]byte, 16)
	if _, err := cryptorand.Read(raw); err != nil {
		return workBuddyRequestID()
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	hexed := hex.EncodeToString(raw)
	return hexed[0:8] + "-" + hexed[8:12] + "-" + hexed[12:16] + "-" + hexed[16:20] + "-" + hexed[20:32]
}

// workBuddyNewMessageID is a message id: the millisecond clock in hex, then random
// digits — the shape the client's ids have (a bare UUID has no time in it).
func workBuddyNewMessageID() string {
	id := fmt.Sprintf("%012x", time.Now().UnixMilli()) + workBuddyRequestID()[:20]
	return id[:32]
}

// workBuddyNewTraceID is a trace id: 32 hex digits with no time in them, which is
// what the client's report requests carry (its chat requests do embed the
// clock; the two chains differ).
func workBuddyNewTraceID() string { return workBuddyRequestID() }

// ---- the fingerprint -------------------------------------------------------

// workBuddyDerivedID is a stable id for this account: the same value every
// restart, different between accounts. The vendor reads these as device
// identifiers, so deriving them from the account is what keeps ten accounts on
// one gateway looking like ten devices rather than one.
func workBuddyDerivedID(credential domain.WorkBuddyCredential, salt string, width int) string {
	derived := workBuddyStableHex(salt + ":" + strings.TrimSpace(credential.UserID))
	if width <= 0 || width > len(derived) {
		return derived
	}
	return derived[:width]
}

// workBuddyReportFingerprint is the device block every desktop event carries.
func workBuddyReportFingerprint(site workBuddySite, credential domain.WorkBuddyCredential) map[string]any {
	now := time.Now().UnixMilli()
	version := site.clientVersion()
	// The build triple is per-realm: see workbuddy_report_build.go for why the commit
	// and releaseDate are configurable and what is missing for the intl build.
	build := workBuddyReportBuildFor(site)
	fingerprint := map[string]any{
		"timezone":     "Asia/Shanghai",
		"reportDelay":  2000,
		"userId":       credential.UserID,
		"username":     credential.AccountLabel,
		"userNickname": credential.AccountLabel,
		"product":      "SaaS",
		"releaseDate":  build.ReleaseDate,
		"commit":       build.Commit,
		"ideName":      "WorkBuddy",
		"ideType":      "WorkBuddy",
		"ideVersion":   version,
		"machineId":    workBuddyDerivedID(credential, "machine", 32),
		"sessionId":    workBuddyDerivedID(credential, "session", 32),
		"extName":      "workbuddy-desktop",
		"extVersion":   version,
		"os":           "win32",
		"arch":         "x64",
		"osVersion":    workBuddyReportOSVersion,
		"cpuModel":     workBuddyReportCPUModel,
		"cpuCores":     workBuddyReportCPUCores,
		"memorySize":   workBuddyReportMemorySize,
		"timestamp":    now,
		"presentAt":    now,
	}
	// qimei36 is Tencent's device id. The Chinese build's client sends it on
	// every report and the international one on none, so adding it to the intl
	// build would be the forgery.
	if !site.intl {
		fingerprint["qimei36"] = workBuddyDerivedID(credential, "qimei", 36)
	}
	return fingerprint
}

// workBuddyMPFingerprint is the event block the miniprogram reports carry.
func workBuddyMPFingerprint(credential domain.WorkBuddyCredential) map[string]any {
	return map[string]any{
		"timestamp":    time.Now().UnixMilli(),
		"ideType":      "WorkBuddy_MP",
		"ideVersion":   workBuddyMPVersion,
		"extName":      workBuddyMPProduct,
		"extVersion":   workBuddyMPVersion,
		"product":      "SaaS",
		"ideName":      "wx_app_cloud",
		"platform":     "mini_program",
		"os":           "windows",
		"osVersion":    "11",
		"arch":         "x64",
		"machineId":    "0655736a-607f-4d9d-b430-58176ee9a090",
		"timezone":     "Asia/Shanghai",
		"userId":       credential.UserID,
		"userNickname": credential.AccountLabel,
	}
}

// ---- the channel -----------------------------------------------------------

// workBuddyReportDesktop posts events as the desktop client does, each merged
// over the account's fingerprint (an event's own field wins).
func workBuddyReportDesktop(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential, events ...map[string]any) error {
	return workBuddyPostEvents(ctx, site, credential, workBuddyReportFingerprint(site, credential), workBuddyDesktopReportHeaders(site, credential), events...)
}

// workBuddyReportMP posts events as the miniprogram client does. The mp report
// is scored by a different pipeline, so it takes its own fingerprint and its own
// request headers.
func workBuddyReportMP(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential, events ...map[string]any) error {
	headers := map[string]string{
		"Authorization":       "Bearer " + credential.AccessToken,
		"X-User-Id":           credential.UserID,
		"X-Domain":            workBuddyWebHost(site),
		"X-Client-Product":    workBuddyMPProduct,
		"X-Client-Version":    workBuddyMPVersion,
		"X-Client-Platform":   workBuddyMPWebPlatform,
		"X-Platform":          "wechatmp",
		"X-Client-Product-V2": workBuddyMPProduct,
	}
	return workBuddyPostEvents(ctx, site, credential, workBuddyMPFingerprint(credential), headers, events...)
}

// workBuddyDesktopReportHeaders are the eleven headers the client's own report
// carries. There is deliberately no X-Requested-With here: only the renderer's
// UA family sends that one, and this channel speaks as the CLI-SDK family.
func workBuddyDesktopReportHeaders(site workBuddySite, credential domain.WorkBuddyCredential) map[string]string {
	return map[string]string{
		"Authorization":  "Bearer " + credential.AccessToken,
		"Accept":         "application/json, text/plain, */*",
		"Content-Type":   "application/json;charset=UTF-8",
		"User-Agent":     site.chatUserAgent(),
		"X-Request-ID":   workBuddyNewTraceID(),
		"X-Product":      "SaaS",
		"X-User-Id":      credential.UserID,
		"X-Domain":       workBuddyWebHost(site),
		"X-Product-Name": "WorkBuddy",
	}
}

// workBuddyPostEvents sends a batch to /v2/report. A report that the vendor
// answered 200 to is not proof anything was counted — but a batch it refused is
// worth retrying, and the soft-rate window is the usual reason.
func workBuddyPostEvents(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential, fingerprint map[string]any, headers map[string]string, events ...map[string]any) error {
	if len(events) == 0 {
		return nil
	}
	batch := make([]map[string]any, 0, len(events))
	for _, event := range events {
		merged := make(map[string]any, len(fingerprint)+len(event))
		for key, value := range fingerprint {
			merged[key] = value
		}
		for key, value := range event {
			merged[key] = value
		}
		batch = append(batch, merged)
	}
	raw, err := json.Marshal(batch)
	if err != nil {
		return err
	}
	return workBuddyRetrySoftRate(ctx, func() error {
		return workBuddyCallRaw(ctx, site, http.MethodPost, workBuddyWebBase(site)+workBuddyReportPath, headers, raw, nil)
	})
}

// workBuddyCallRaw is workBuddyCall for a body that is already encoded — the
// report channel takes a JSON array, which no map represents.
func workBuddyCallRaw(ctx context.Context, site workBuddySite, method, endpoint string, headers map[string]string, body []byte, dst any) error {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", site.rpcUserAgent())
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	return workBuddyDo(request, dst)
}

// workBuddyReportWebEvent reports one browser-side event: the few tasks scored
// by a web page (the library intro) count this and nothing else.
func workBuddyReportWebEvent(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential, eventCode, pageURL, elementID, elementName string) error {
	event := map[string]any{
		"eventCode": eventCode, "timestamp": time.Now().UnixMilli(), "reportDelay": 0,
		"pageURL": pageURL, "elementId": elementID, "elementName": elementName,
		"os": "Win32", "arch": "", "osVersion": "10.0", "userAgent": site.chatUserAgent(),
		"machineId": workBuddyDerivedID(credential, "webmachine", 32),
		"userId":    credential.UserID, "userNickname": credential.AccountLabel,
	}
	raw, err := json.Marshal([]map[string]any{event})
	if err != nil {
		return err
	}
	headers := map[string]string{
		"Authorization": "Bearer " + credential.AccessToken,
		"Accept":        "application/json, text/plain, */*",
		"Content-Type":  "application/json;charset=UTF-8",
		"User-Agent":    site.chatUserAgent(),
		"X-Request-ID":  workBuddyNewTraceID(),
		"X-Product":     "SaaS",
		"X-User-Id":     credential.UserID,
		"X-Domain":      workBuddyWebHost(site),
	}
	endpoint := workBuddyWebBase(site) + workBuddyReportPath
	return workBuddyRetrySoftRate(ctx, func() error {
		return workBuddyCallRaw(ctx, site, http.MethodPost, endpoint, headers, raw, nil)
	})
}

// workBuddyReportActivity reports the one event 成长 counts as activity for the
// day: a chat_request_send, shaped exactly as the client sends it. It lights the
// streak and is what unlocks first_buddy.
func workBuddyReportActivity(ctx context.Context, site workBuddySite, credential domain.WorkBuddyCredential) error {
	return workBuddyReportDesktop(ctx, site, credential, workBuddyChatActivityEvent("", "", "deepseek-v4.1-flash", "DeepSeek-V4.1-Flash"))
}

// ---- desktop event groups --------------------------------------------------

// workBuddyChatActivityEvent is one chat_request_send: the event a single 活跃上报
// sends, and the first of the chat chain below. conversationID/requestID may be
// empty, in which case they are made here.
func workBuddyChatActivityEvent(conversationID, requestID, modelID, modelName string) map[string]any {
	if strings.TrimSpace(conversationID) == "" {
		conversationID = workBuddyNewConversationID()
	}
	if strings.TrimSpace(requestID) == "" {
		requestID = workBuddyNewMessageID()
	}
	if strings.TrimSpace(modelID) == "" {
		modelID = "deepseek-v4.1-flash"
	}
	if strings.TrimSpace(modelName) == "" {
		modelName = modelID
	}
	traceID := workBuddyNewTraceID()
	return map[string]any{
		"eventCode": "chat_request_send", "mode": "craft",
		"conversationId": conversationID, "requestId": requestID,
		"inputLength": 12, "requestModelId": modelID, "requestModelName": modelName,
		"isPlan": false, "isAutoExecuteTerminal": false, "isAutoModify": false,
		"codebaseEnable": false, "isMaxMode": true, "source": "work",
		"mentionContexts": []any{}, "knowledgeId": []any{}, "knowledgeName": []any{},
		"codebaseId": "", "mentionContextCount": 0, "command": "",
		"inputAttemptId": workBuddyNewConversationID(), "recommendId": "", "skillId": "",
		"traceId": traceID, "rootRequestId": requestID,
		"parentConversationId": conversationID,
		"agentName":            "cli", "agentType": "main",
		"vcsType": "unknown", "vcsRepo": "", "vcsBranchName": "", "vcsRevId": "",
		"buddyId": "", "buddyName": "",
		"codebuddy.session_id":              conversationID,
		"codebuddy.conversation_request_id": requestID,
	}
}

// workBuddyChatSequence is a whole successful desktop turn: the six events the
// client emits from "task created" to "response received". RichMeow_Chat counts
// this chain, and the template/playbook/skill/expert groups build on it.
func workBuddyChatSequence(conversationID, requestID, messageID, modelID, modelName string) []map[string]any {
	if strings.TrimSpace(requestID) == "" {
		requestID = workBuddyNewMessageID()
	}
	firstToken := time.Now().UnixMilli()
	return []map[string]any{
		{
			"eventCode": "agent_task_created", "source": "LOCAL", "name": "working",
			"task_target": "local", "mode": "craft",
			"requestModelId": modelID, "requestModelName": modelName,
			"has_repo": false, "repo_type": "none", "workspace_type": "empty",
			"has_connector": false, "connector_types": []any{},
			"has_mention": false, "mention_types": []any{},
			"has_template": false, "action": "", "template_name": "",
			"has_expert": false, "expert_id": "", "expert_name": "", "expert_industry_id": "",
			"has_skill": false, "skill_names": []any{},
			"conversationId": conversationID, "messageId": messageID,
			"buddyId": "", "buddyName": "",
		},
		{
			"eventCode": "chat_message_send", "messageId": messageID + "-assistant",
			"historyCount": 0, "isContextTruncated": false, "currentStepCount": 1,
			"traceId": requestID, "rootRequestId": requestID,
			"parentConversationId": conversationID,
			"agentName":            "cli", "agentType": "main",
		},
		{
			"eventCode": "chat_request_send", "inputLength": 24, "isPlan": false,
			"isAutoExecuteTerminal": false, "isAutoModify": false, "codebaseEnable": false,
			"maxToken": 0, "maxSteps": 500, "temperature": 0, "maxRetries": 0,
			"mentionContexts": []any{}, "knowledgeId": []any{}, "knowledgeName": []any{},
			"codebaseId": "", "mentionContextCount": 0, "command": "",
			"recommendId": "", "skillId": "", "skillCount": 0, "totalCount": 0,
			"traceId": requestID, "rootRequestId": requestID,
			"parentConversationId": conversationID,
			"agentName":            "cli", "agentType": "main",
			"codebuddy.session_id":              conversationID,
			"codebuddy.conversation_request_id": requestID,
		},
		{
			"eventCode": "chat_message_response", "messageId": messageID + "-assistant",
			"responseModelId": modelID, "inputToken": 120, "outputToken": 80, "totalToken": 200,
			"cachedTokens": 0, "cachedWriteTokens": 0, "cachedMissTokens": 0,
			"isSuccessful": true, "messageErrorCode": "", "finishReason": "stop",
			"firstTokenAt": firstToken, "traceId": requestID,
			"conversationId": conversationID, "rootRequestId": requestID,
			"parentConversationId": conversationID,
			"agentName":            "cli", "agentType": "main",
			"codebuddy.session_id":              conversationID,
			"codebuddy.conversation_request_id": requestID,
		},
		{
			"eventCode": "chat_message_status", "messageId": messageID + "-assistant",
			"messageErrorCode": "0", "traceId": requestID, "rootRequestId": requestID,
			"parentConversationId": conversationID,
			"agentName":            "cli", "agentType": "main",
		},
		{
			"eventCode": "chat_request_response", "mode": "craft", "toolCallCount": 0,
			"inputToken": 120, "outputToken": 80, "totalToken": 200,
			"cachedTokens": 0, "cachedWriteTokens": 0, "cachedMissTokens": 0,
			"isSuccessful": true, "messageErrorCode": "", "finishReason": "stop",
			"rootRequestId": requestID, "parentConversationId": conversationID,
		},
	}
}

// workBuddyBuddyAppSequence is "enter a Buddy app": the five clicks the client
// reports. Buddy_App and Buddy_App_QQ both count this one app.
func workBuddyBuddyAppSequence(buddyID, buddyName string) []map[string]any {
	makeEvent := func(code string, extra map[string]any) map[string]any {
		event := map[string]any{"eventCode": code, "mode": "LOCAL", "buddyId": buddyID, "buddyName": buddyName}
		for key, value := range extra {
			event[key] = value
		}
		return event
	}
	return []map[string]any{
		makeEvent("buddyapp_discover_click", nil),
		makeEvent("buddyapp_show", map[string]any{"elementId": buddyID, "elementName": buddyName, "position": 2}),
		makeEvent("buddyapp_enter_click", map[string]any{"elementId": buddyID, "elementName": buddyName, "position": 2, "isFirstPage": "1"}),
		makeEvent("buddyapp_auth_confirm_click", map[string]any{"elementId": buddyID, "elementName": buddyName}),
		makeEvent("buddyapp_bindaccount_skip_click", map[string]any{"elementId": buddyID, "elementName": buddyName}),
	}
}

// workBuddyAutomationCreateEvent is "a scheduled task was created".
func workBuddyAutomationCreateEvent(name string) map[string]any {
	return map[string]any{
		"eventCode": "automated_task_create_suc", "name": name, "source": "manually",
		"modelId": "fast-model", "modelIsThinking": true,
		"connectorCount": 0, "skills": "", "skillCount": 0,
		"scheduleType": "once", "mode": "LOCAL",
	}
}

// workBuddyTemplateUseSequence is "a task was created from a template": the chat
// chain plus the two template events, which is what template_5 counts.
func workBuddyTemplateUseSequence(conversationID, requestID, templateID, templateName string) []map[string]any {
	events := workBuddyChatSequence(conversationID, requestID, "msg-"+templateID, "fast-model", "fast-model")
	return append(events,
		map[string]any{
			"eventCode": "agent_task_created_with_template", "mode": "working",
			"isCustomModel": false, "id": templateID, "name": templateName, "requestId": requestID,
		},
		map[string]any{"eventCode": "template_used", "template_id": templateID, "task_mode": "working"},
	)
}

// workBuddyPlaybookPromptSequence is "sent a prompt from an inspiration case".
// The prompt-send event is what counts; the two clicks lead up to it.
func workBuddyPlaybookPromptSequence(conversationID, requestID, caseID, caseName string) []map[string]any {
	events := workBuddyChatSequence(conversationID, requestID, "msg-pb", "fast-model", "fast-model")
	caseFields := map[string]any{
		"id": caseID, "name": caseName, "type": "document",
		"categoryId": "", "categoryName": "",
	}
	withCase := func(event map[string]any) map[string]any {
		for key, value := range caseFields {
			event[key] = value
		}
		return event
	}
	return append(events,
		map[string]any{
			"eventCode": "web_element_click", "pageName": "playbook_detail",
			"elementId": "playbook_ctaClick", "elementName": caseName, "source": "discover",
		},
		withCase(map[string]any{"eventCode": "playbook_cta_click", "source": "discover", "position": 0}),
		withCase(map[string]any{"eventCode": "playbook_prompt_send", "conversationId": conversationID, "requestId": requestID}),
	)
}

// workBuddySkillInfoEvent is the skill load the skill_1 task counts: a skill_info
// joined to a real turn.
func workBuddySkillInfoEvent(conversationID, requestID, messageID string) map[string]any {
	return map[string]any{
		"eventCode": "skill_info", "id": "润泽小馆·日报撰写", "skillId": "skill_2097350077599879168",
		"skillVersion": "1.0.0", "toolStatus": "success", "fileCount": 56,
		"source":         "workbuddy-desktop",
		"conversationId": conversationID, "requestId": requestID, "messageId": messageID,
		"requestModelId": "fast-model", "requestModelName": "fast-model",
		"traceId": requestID,
	}
}

// workBuddyAppearanceApplyEvent is the theme-apply event Hp_Appearance counts.
// The client reports it when it leaves the settings page with a theme in force,
// which is why setting the theme alone does not score.
func workBuddyAppearanceApplyEvent(resourceKey string) map[string]any {
	return map[string]any{
		"eventCode": "appearance_skin_apply", "action": "apply", "source": "settings_close",
		"id": resourceKey, "vipLevel": 0, "series": "", "type": "unknown",
	}
}

// workBuddyExpertSummonSequence is "summoned a market expert": the click, the
// summon and the confirmation.
func workBuddyExpertSummonSequence(expert workBuddyMarketExpert) []map[string]any {
	category := "expert-all"
	if len(expert.Categories) > 0 {
		if name, ok := expert.Categories[0].(string); ok && strings.TrimSpace(name) != "" {
			category = name
		}
	}
	version := expert.Version
	if strings.TrimSpace(version) == "" {
		version = "1.0.0"
	}
	return []map[string]any{
		{
			"eventCode": "web_element_click", "source": expert.ExpertID, "type": category, "version": version,
			"elementId": "expert_summon_click", "elementName": "立即召唤",
			"pageURL": "/C:/Program%20Files/WorkBuddy/resources/app.asar/renderer/index.html",
		},
		{
			"eventCode": "expert_summon_click", "id": expert.ExpertID, "name": expert.DisplayNameZH,
			"expertTitle": expert.ProfessionZH, "type": "expert-all", "position": 0,
			"expertType": expert.ExpertType, "version": version, "mode": "LOCAL",
		},
		{
			"eventCode": "expert_summoned", "id": expert.ExpertID, "name": expert.DisplayNameZH,
			"expertTitle": expert.ProfessionZH, "type": "expert-all",
		},
	}
}

// workBuddyExpertActualUseEvent is "used the expert for real". mode is craft for
// the expert_5 / Expert_team_use_3 counters and LOCAL for the single-expert
// campaign tasks, which the vendor distinguishes.
func workBuddyExpertActualUseEvent(expert workBuddyMarketExpert, conversationID, requestID, mode string) map[string]any {
	category := "expert-all"
	if len(expert.Categories) > 0 {
		if name, ok := expert.Categories[0].(string); ok && strings.TrimSpace(name) != "" {
			category = name
		}
	}
	version := expert.Version
	if strings.TrimSpace(version) == "" {
		version = "1.0.0"
	}
	if strings.TrimSpace(mode) == "" {
		mode = "craft"
	}
	messageID := "msg-" + requestID
	if len(requestID) > 8 {
		messageID = "msg-" + requestID[len(requestID)-8:]
	}
	return map[string]any{
		"eventCode": "expert_actual_use", "id": expert.ExpertID, "name": expert.DisplayNameZH,
		"expertTitle": expert.ProfessionZH, "type": category, "expertType": expert.ExpertType,
		"source": "builtin", "version": version, "mode": mode,
		"cost": 9000, "characterCount": 14,
		"conversationId": conversationID, "requestId": requestID, "messageId": messageID,
		"requestModelId": "fast-model", "requestModelName": "fast-model",
	}
}

// ---- miniprogram events ----------------------------------------------------

// workBuddyMiniChatSendEvent is one miniprogram chat_request_send: what the
// Sequential_Tasks_1/3/6 counters add up.
func workBuddyMiniChatSendEvent(conversationID string) map[string]any {
	requestID := workBuddyNewMessageID()
	messageID := "msg-" + requestID
	if len(requestID) > 8 {
		messageID = "msg-" + requestID[len(requestID)-8:]
	}
	return map[string]any{
		"eventCode": "chat_request_send", "inputLength": 14, "isPlan": false,
		"isAutoExecuteTerminal": false, "isAutoModify": false, "codebaseEnable": false,
		"maxToken": 0, "maxSteps": 500, "temperature": 0, "maxRetries": 0,
		"mentionContexts": []any{}, "knowledgeId": []any{}, "knowledgeName": []any{},
		"codebaseId": "", "mentionContextCount": 0, "command": "",
		"recommendId": "", "skillId": "", "skillCount": 0, "totalCount": 0,
		"traceId": requestID, "rootRequestId": requestID,
		"parentConversationId": conversationID, "conversationId": conversationID,
		"messageId": messageID, "agentName": "mp", "agentType": "main",
		"codebuddy.session_id":              conversationID,
		"codebuddy.conversation_request_id": requestID,
	}
}

// workBuddyMiniChatModelEvent is that event with a model named on it, which is
// what "use GLM in the miniprogram" counts.
func workBuddyMiniChatModelEvent(conversationID, modelID, modelName string) map[string]any {
	event := workBuddyMiniChatSendEvent(conversationID)
	event["requestModelId"] = modelID
	event["requestModelName"] = modelName
	return event
}

// workBuddyMiniExpertUseEvent is "picked an expert in the miniprogram". The id
// must be a real market expert; an invented one is not counted.
func workBuddyMiniExpertUseEvent(expertID, expertName, expertType string) map[string]any {
	if strings.TrimSpace(expertType) == "" {
		expertType = "agent"
	}
	if strings.TrimSpace(expertName) == "" {
		expertName = expertID
	}
	return map[string]any{
		"eventCode": "expert_actual_use", "reportDelay": 0,
		"extVersion": "2.2.8", "source": "mini_program",
		"id": expertID, "name": expertID,
		"expertTitle": expertName, "type": "send_message",
		"characterCount": 12, "expertType": expertType,
	}
}

// workBuddyMiniPlaybookEvents is the miniprogram's inspiration pair: the click
// and the prompt it sent.
func workBuddyMiniPlaybookEvents(caseID, caseName string) []map[string]any {
	caseFields := map[string]any{
		"id": caseID, "name": caseName, "type": "document",
		"categoryId": "", "categoryName": "",
		"skills": "", "skillNames": "",
	}
	withCase := func(event map[string]any) map[string]any {
		for key, value := range caseFields {
			event[key] = value
		}
		return event
	}
	return []map[string]any{
		withCase(map[string]any{
			"eventCode": "playbook_cta_click", "source": "discover", "position": 1,
			"extVersion": "2.2.8",
		}),
		withCase(map[string]any{
			"eventCode": "playbook_prompt_send", "source": "discover",
			"promptLength": 96, "isOfficial": 1,
			"conversationId": workBuddyNewConversationID(), "extVersion": "2.2.8",
		}),
	}
}
