package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// Video generation, which is a submit-and-poll pair rather than one call.
//
// Protocol measured against the CN pool (misakano7545/workbuddy2api-panel
// internal/upstream/videos.go, "2026-11-04 直连 cn 池复核"):
//
//	POST /v2/videos/generations
//	  {prompt, model, seconds, negative_prompt, watermark, image_url, last_image_url,
//	   extra_parameters:{resolution, enable_audio, aspect_ratio}}
//	  → {"id":"<task>","status":"queued","created_at":…,"request_id":"…"}
//	POST /v2/videos/tasks {"task_id":…}
//	  → {"id":…,"status":"completed","data":[{"url":…}],"size":"768P",
//	     "usage":{"output_video_duration":4,"output_video_tokens":200000,
//	              "output_video_coefficient":1,"credit":57.14}}
//
// Two things a gateway has to get right:
//
//   - **A task can only be read by the account that submitted it.** The upstream keys
//     the task to the account, so a poll must go out on the same provider. That is why
//     there is a ledger here: an OpenAI-style `GET /v1/videos/{id}` carries no account,
//     so the id has to be enough to find one.
//   - **Status is passed through, not normalised.** `queued` / `running` /
//     `completed` / `failed` are the vendor's words; `Done`/`Succeeded` accept the
//     historical spellings the same way the reference does.
//
// Bounds the upstream enforces and this gateway deliberately does not duplicate:
// duration outside 4-15 seconds is 14407, an unknown model is "route config not
// found", a missing prompt is 10001. The vendor's own message goes back to the client.

const (
	workBuddyVideoGenerationsPath = "/v2/videos/generations"
	workBuddyVideoTasksPath       = "/v2/videos/tasks"
)

// workBuddyVideoTask is one poll answer.
type workBuddyVideoTask struct {
	ID          string
	Status      string
	CreatedAt   int64
	CompletedAt int64
	Size        string
	URLs        []string
	Duration    int64
	Tokens      int64
	Coefficient float64
	Credit      float64
	ErrorText   string
}

// Done reports a terminal state.
func (task workBuddyVideoTask) Done() bool {
	switch task.Status {
	case "completed", "failed", "succeeded", "success":
		return true
	}
	return false
}

// Succeeded reports a terminal state that produced a video.
func (task workBuddyVideoTask) Succeeded() bool {
	switch task.Status {
	case "completed", "succeeded", "success":
		return true
	}
	return false
}

// parseWorkBuddyVideoTask reads a poll answer tolerantly: a missing field is zero, not
// an error, and the video address is accepted from both shapes the upstream uses.
func parseWorkBuddyVideoTask(payload []byte) workBuddyVideoTask {
	var task workBuddyVideoTask
	var envelope struct {
		ID        string          `json:"id"`
		TaskID    string          `json:"task_id"`
		Status    string          `json:"status"`
		CreatedAt json.Number     `json:"created_at"`
		Completed json.Number     `json:"completed_at"`
		Size      string          `json:"size"`
		URL       string          `json:"url"`
		VideoURL  string          `json:"video_url"`
		Msg       string          `json:"msg"`
		Message   string          `json:"message"`
		Error     json.RawMessage `json:"error"`
		Data      []struct {
			URL string `json:"url"`
		} `json:"data"`
		Usage struct {
			Duration    json.Number `json:"output_video_duration"`
			Tokens      json.Number `json:"output_video_tokens"`
			Coefficient json.Number `json:"output_video_coefficient"`
			Credit      json.Number `json:"credit"`
		} `json:"usage"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return task
	}
	task.ID = envelope.ID
	if task.ID == "" {
		task.ID = envelope.TaskID
	}
	task.Status = strings.TrimSpace(envelope.Status)
	task.CreatedAt = workBuddyInt64Of(envelope.CreatedAt)
	task.CompletedAt = workBuddyInt64Of(envelope.Completed)
	task.Size = envelope.Size
	task.Duration = workBuddyInt64Of(envelope.Usage.Duration)
	task.Tokens = workBuddyInt64Of(envelope.Usage.Tokens)
	task.Coefficient = workBuddyAnyFloat(envelope.Usage.Coefficient)
	task.Credit = workBuddyAnyFloat(envelope.Usage.Credit)
	task.ErrorText = strings.TrimSpace(envelope.Msg)
	if task.ErrorText == "" {
		task.ErrorText = strings.TrimSpace(envelope.Message)
	}
	if task.ErrorText == "" && len(envelope.Error) > 0 {
		var text string
		if json.Unmarshal(envelope.Error, &text) == nil {
			task.ErrorText = text
		} else {
			var object struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(envelope.Error, &object) == nil {
				task.ErrorText = object.Message
			}
		}
	}
	for _, item := range envelope.Data {
		if item.URL != "" {
			task.URLs = append(task.URLs, item.URL)
		}
	}
	if len(task.URLs) == 0 {
		if envelope.VideoURL != "" {
			task.URLs = append(task.URLs, envelope.VideoURL)
		} else if envelope.URL != "" {
			task.URLs = append(task.URLs, envelope.URL)
		}
	}
	return task
}

// parseWorkBuddyVideoSubmit reads the task id out of a submit answer.
func parseWorkBuddyVideoSubmit(payload []byte) (string, string) {
	var envelope struct {
		ID     string `json:"id"`
		TaskID string `json:"task_id"`
		Status string `json:"status"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return "", ""
	}
	id := envelope.ID
	if id == "" {
		id = envelope.TaskID
	}
	return id, strings.TrimSpace(envelope.Status)
}

// workBuddyInt64Of reads an int that may arrive as a number or a string.
func workBuddyInt64Of(value json.Number) int64 {
	if value == "" {
		return 0
	}
	if parsed, err := value.Int64(); err == nil {
		return parsed
	}
	return int64(workBuddyAnyFloat(value))
}

// ---- the task ledger --------------------------------------------------------
//
// A video task belongs to the account that submitted it, so a poll with only the task
// id has to find that account. The ledger is in memory: losing it on restart costs a
// poll the client can re-issue by submitting again, and persisting it would put task
// bookkeeping in the settings table for a feature with a 12-hour signed URL anyway.

const (
	// workBuddyVideoLedgerCap bounds the ledger.
	workBuddyVideoLedgerCap = 4096
	// workBuddyVideoLedgerTTL forgets a task long after its signed URL has expired.
	workBuddyVideoLedgerTTL = 24 * time.Hour
)

var workBuddyVideoLedger sync.Map // taskID -> workBuddyVideoLedgerEntry

type workBuddyVideoLedgerEntry struct {
	providerID string
	model      string
	at         time.Time
}

// rememberWorkBuddyVideoTask records which account submitted a task.
func rememberWorkBuddyVideoTask(taskID, providerID, model string) {
	if strings.TrimSpace(taskID) == "" {
		return
	}
	workBuddyVideoLedger.Store(strings.TrimSpace(taskID), workBuddyVideoLedgerEntry{
		providerID: providerID, model: model, at: time.Now(),
	})
	pruneWorkBuddyVideoLedger(time.Now())
}

// workBuddyVideoTaskProvider is the account that submitted a task, if it is still
// remembered.
func workBuddyVideoTaskProvider(taskID string, now time.Time) (string, bool) {
	cached, ok := workBuddyVideoLedger.Load(strings.TrimSpace(taskID))
	if !ok {
		return "", false
	}
	entry, typed := cached.(workBuddyVideoLedgerEntry)
	if !typed || now.Sub(entry.at) > workBuddyVideoLedgerTTL {
		workBuddyVideoLedger.Delete(strings.TrimSpace(taskID))
		return "", false
	}
	return entry.providerID, entry.providerID != ""
}

// pruneWorkBuddyVideoLedger drops expired entries and enforces the cap.
func pruneWorkBuddyVideoLedger(now time.Time) {
	count := 0
	workBuddyVideoLedger.Range(func(key, value any) bool {
		count++
		entry, ok := value.(workBuddyVideoLedgerEntry)
		if !ok || now.Sub(entry.at) > workBuddyVideoLedgerTTL {
			workBuddyVideoLedger.Delete(key)
		}
		return true
	})
	if count <= workBuddyVideoLedgerCap {
		return
	}
	workBuddyVideoLedger.Range(func(key, _ any) bool {
		workBuddyVideoLedger.Delete(key)
		count--
		return count > workBuddyVideoLedgerCap
	})
}

// ---- request routing --------------------------------------------------------

// workBuddyMediaRouteKey marks a request that targets one of the media routes rather
// than an image render. The image handler is reused for authentication and route
// resolution; this only carries which upstream path and which body to use.
type workBuddyMediaRouteKey struct{}

// workBuddyMediaRoute is one media request's target.
type workBuddyMediaRoute struct {
	path   string
	taskID string
}

// withWorkBuddyMediaRoute marks the request.
func withWorkBuddyMediaRoute(ctx context.Context, route workBuddyMediaRoute) context.Context {
	return context.WithValue(ctx, workBuddyMediaRouteKey{}, route)
}

// workBuddyMediaRouteOf reads the mark.
func workBuddyMediaRouteOf(ctx context.Context) (workBuddyMediaRoute, bool) {
	route, ok := ctx.Value(workBuddyMediaRouteKey{}).(workBuddyMediaRoute)
	return route, ok
}

// workBuddyVideoTaskBody is the poll body the tasks route expects.
func workBuddyVideoTaskBody(taskID string) []byte {
	encoded, err := json.Marshal(map[string]any{"task_id": taskID})
	if err != nil {
		return nil
	}
	return encoded
}

// workBuddyProviderByID resolves a provider for the ledger path.
func (s *Server) workBuddyProviderByID(providerID string) (domain.Provider, bool) {
	if s.router == nil || strings.TrimSpace(providerID) == "" {
		return domain.Provider{}, false
	}
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		return domain.Provider{}, false
	}
	if !isWorkBuddyProvider(provider) {
		return domain.Provider{}, false
	}
	return provider, true
}

// handleOpenAIVideosGenerations serves POST /v1/videos/generations.
//
// It reuses the image handler for authentication and route resolution — the two are
// the same problem — and only says which upstream path to post to. The vendor's own
// answer goes back (task id included), because that id is what the client polls with.
func (s *Server) handleOpenAIVideosGenerations(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, workBuddyContinueBodyCap))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	// Remember the task against the account that is about to submit it. The handler
	// below resolves the account, so the id is recorded from the response instead —
	// see recordWorkBuddyVideoTaskFromResponse, called after the send.
	r = r.WithContext(withWorkBuddyMediaRoute(r.Context(), workBuddyMediaRoute{path: workBuddyVideoGenerationsPath}))
	r.Body = io.NopCloser(bytesReader(body))
	s.handleOpenAIImagesGenerations(w, r)
}

// handleOpenAIVideosTask serves POST /v1/videos/tasks {task_id}.
func (s *Server) handleOpenAIVideosTask(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	var payload struct {
		TaskID string `json:"task_id"`
	}
	if json.Unmarshal(body, &payload) != nil || strings.TrimSpace(payload.TaskID) == "" {
		writeOpenAIError(w, http.StatusBadRequest, "task_id is required")
		return
	}
	s.serveWorkBuddyVideoPoll(w, r, payload.TaskID)
}

// handleOpenAIVideoTaskByID serves GET /v1/videos/{taskID}.
func (s *Server) handleOpenAIVideoTaskByID(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimSpace(r.PathValue("taskID"))
	if taskID == "" {
		writeOpenAIError(w, http.StatusBadRequest, "task id is required")
		return
	}
	s.serveWorkBuddyVideoPoll(w, r, taskID)
}

// serveWorkBuddyVideoPoll polls one task on the account that submitted it.
//
// The id alone has to find an account: a task is only readable by its submitter, and
// this route carries no key-to-account mapping for a task that was submitted earlier.
// An id the ledger does not know cannot be polled, and saying so is better than
// guessing at an account and reading someone else's task.
func (s *Server) serveWorkBuddyVideoPoll(w http.ResponseWriter, r *http.Request, taskID string) {
	providerID, ok := workBuddyVideoTaskProvider(taskID, time.Now())
	if !ok {
		writeOpenAIError(w, http.StatusNotFound, "unknown video task; submit it through this gateway first")
		return
	}
	provider, ok := s.workBuddyProviderByID(providerID)
	if !ok {
		writeOpenAIError(w, http.StatusNotFound, "the account that submitted this task is gone")
		return
	}
	status, payload, err := s.workBuddyEndpointCall(r.Context(), provider,
		workBuddyEndpointURL(mustWorkBuddySite(provider), workBuddyVideoTasksPath),
		workBuddyVideoTaskBody(taskID), "application/json", "application/json")
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}

// mustWorkBuddySite resolves the site of a provider already known to be an account.
func mustWorkBuddySite(provider domain.Provider) workBuddySite {
	site, _ := workBuddySiteForAuthType(provider.AuthType)
	return site
}

// bytesReader is a small helper so the submit handler can rebuild the body it read.
func bytesReader(body []byte) *bytes.Reader { return bytes.NewReader(body) }

// recordWorkBuddyVideoTaskFromResponse remembers the task id a submit answered with.
func recordWorkBuddyVideoTaskFromResponse(providerID, model string, payload []byte) {
	taskID, _ := parseWorkBuddyVideoSubmit(payload)
	if taskID == "" {
		return
	}
	rememberWorkBuddyVideoTask(taskID, providerID, model)
}
