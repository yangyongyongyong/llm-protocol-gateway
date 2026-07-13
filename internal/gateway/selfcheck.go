package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

const (
	selfcheckDefaultPrompt    = "1+1等于几"
	selfcheckDefaultTimeoutMs = 90_000
	selfcheckPreviewLimit     = 500
	selfcheckKeyPrefix        = "selfcheck-"
)

type selfcheckClientCase string

const (
	selfcheckClientOpenCode selfcheckClientCase = "opencode"
	selfcheckClientCodex    selfcheckClientCase = "codex"
	selfcheckClientClaude   selfcheckClientCase = "claude"
)

// selfcheckCaseKind distinguishes a plain-chat probe from a tool-call probe.
// The tool probe asks the CLI to create a random file under /tmp and write a
// token into it, then we verify the file on disk — a true end-to-end check that
// tool calling survives the gateway's protocol conversion.
type selfcheckCaseKind string

const (
	selfcheckKindChat selfcheckCaseKind = "chat"
	selfcheckKindTool selfcheckCaseKind = "tool"
)

// selfcheckClientsPerProvider drives Total accounting: 3 clients × 2 kinds.
const selfcheckCasesPerProvider = 6

type selfcheckStartRequest struct {
	ProviderIDs []string `json:"providerIds"`
	TimeoutMs   int      `json:"timeoutMs"`
	Prompt      string   `json:"prompt"`
}

type selfcheckCaseResult struct {
	CaseID       string `json:"caseId"`
	ProviderID   string `json:"providerId"`
	ProviderName string `json:"providerName"`
	Client       string `json:"client"`
	Kind         string `json:"kind"`
	Protocol     string `json:"protocol"`
	Model        string `json:"model,omitempty"`
	Success      bool   `json:"success"`
	ContentOK    bool   `json:"contentOK"`
	LatencyMs    int64  `json:"latencyMs"`
	OutputPreview string `json:"outputPreview,omitempty"`
	Error        string `json:"error,omitempty"`
	RouteID      string `json:"routeId,omitempty"`
	APIKeyName   string `json:"apiKeyName,omitempty"`
}

type selfcheckJob struct {
	ID         string               `json:"jobId"`
	Status     string               `json:"status"` // running | done | error
	Prompt     string               `json:"prompt"`
	TimeoutMs  int                  `json:"timeoutMs"`
	LANRoot    string               `json:"lanRoot"`
	StartedAt  string               `json:"startedAt"`
	FinishedAt string               `json:"finishedAt,omitempty"`
	Error      string               `json:"error,omitempty"`
	Results    []selfcheckCaseResult `json:"results"`
	Total      int                  `json:"total"`
	Completed  int                  `json:"completed"`

	mu sync.Mutex
	// cases retains the prepared route/key/model per CaseID so a single failing
	// case can be re-run without re-preparing the whole job. Guarded by mu.
	cases map[string]preparedCase
	// running tracks CaseIDs with an in-flight (re)run to reject duplicate retries.
	running map[string]struct{}
}

// preparedCase holds everything needed to (re)run one self-check case.
type preparedCase struct {
	caseID       string
	providerID   string
	providerName string
	client       selfcheckClientCase
	kind         selfcheckCaseKind
	protocol     domain.Protocol
	routeID      string
	apiKeyName   string
	apiKey       string
	model        string
	routeCreated bool
	setupErr     string
}

func selfcheckCaseID(providerID string, client selfcheckClientCase, kind selfcheckCaseKind) string {
	return fmt.Sprintf("%s|%s|%s", providerID, client, kind)
}

type selfcheckToolInfo struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Path    string `json:"path"`
	Found   bool   `json:"found"`
	Client  string `json:"client"`
	Protocol string `json:"protocol"`
}

func (s *Server) ensureSelfcheckStore() {
	s.selfcheckMu.Lock()
	defer s.selfcheckMu.Unlock()
	if s.selfcheckJobs == nil {
		s.selfcheckJobs = make(map[string]*selfcheckJob)
	}
}

func (s *Server) handleSelfcheckTools(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"tools": resolveSelfcheckTools(),
		"lanRoot": selfcheckLANRoot(s.router.State()),
	})
}

func (s *Server) handleSelfcheckStart(w http.ResponseWriter, r *http.Request) {
	var req selfcheckStartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid selfcheck json: "+err.Error())
		return
	}
	providerIDs := uniqueNonEmpty(req.ProviderIDs)
	if len(providerIDs) == 0 {
		writeOpenAIError(w, http.StatusBadRequest, "providerIds is required")
		return
	}
	timeoutMs := req.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = selfcheckDefaultTimeoutMs
	}
	if timeoutMs < 5_000 {
		timeoutMs = 5_000
	}
	if timeoutMs > 600_000 {
		timeoutMs = 600_000
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		prompt = selfcheckDefaultPrompt
	}

	state := s.router.State()
	for _, id := range providerIDs {
		if _, ok := findProvider(state, id); !ok {
			writeOpenAIError(w, http.StatusBadRequest, fmt.Sprintf("provider %q not found", id))
			return
		}
	}

	jobID, err := newSelfcheckJobID()
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to create job id: "+err.Error())
		return
	}
	job := &selfcheckJob{
		ID:        jobID,
		Status:    "running",
		Prompt:    prompt,
		TimeoutMs: timeoutMs,
		LANRoot:   selfcheckLANRoot(state),
		StartedAt: time.Now().UTC().Format(time.RFC3339),
		Results:   make([]selfcheckCaseResult, 0, len(providerIDs)*selfcheckCasesPerProvider),
		Total:     len(providerIDs) * selfcheckCasesPerProvider,
		cases:     make(map[string]preparedCase),
		running:   make(map[string]struct{}),
	}
	s.ensureSelfcheckStore()
	s.selfcheckMu.Lock()
	s.selfcheckJobs[jobID] = job
	s.selfcheckMu.Unlock()

	go s.runSelfcheckJob(job, providerIDs)

	writeJSON(w, http.StatusAccepted, map[string]any{"jobId": jobID})
}

func (s *Server) handleSelfcheckStatus(w http.ResponseWriter, r *http.Request) {
	jobID := strings.TrimSpace(r.PathValue("jobId"))
	if jobID == "" {
		writeOpenAIError(w, http.StatusBadRequest, "jobId is required")
		return
	}
	s.ensureSelfcheckStore()
	s.selfcheckMu.Lock()
	job := s.selfcheckJobs[jobID]
	s.selfcheckMu.Unlock()
	if job == nil {
		writeOpenAIError(w, http.StatusNotFound, "selfcheck job not found")
		return
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	results := make([]selfcheckCaseResult, len(job.Results))
	copy(results, job.Results)
	writeJSON(w, http.StatusOK, map[string]any{
		"jobId":      job.ID,
		"status":     job.Status,
		"prompt":     job.Prompt,
		"timeoutMs":  job.TimeoutMs,
		"lanRoot":    job.LANRoot,
		"startedAt":  job.StartedAt,
		"finishedAt": job.FinishedAt,
		"error":      job.Error,
		"results":    results,
		"total":      job.Total,
		"completed":  job.Completed,
	})
}

// handleSelfcheckRetry re-runs a single failed case within an existing job so
// the operator can retry transient failures without re-running everything.
func (s *Server) handleSelfcheckRetry(w http.ResponseWriter, r *http.Request) {
	jobID := strings.TrimSpace(r.PathValue("jobId"))
	caseID := strings.TrimSpace(r.PathValue("caseId"))
	if jobID == "" || caseID == "" {
		writeOpenAIError(w, http.StatusBadRequest, "jobId and caseId are required")
		return
	}
	s.ensureSelfcheckStore()
	s.selfcheckMu.Lock()
	job := s.selfcheckJobs[jobID]
	s.selfcheckMu.Unlock()
	if job == nil {
		writeOpenAIError(w, http.StatusNotFound, "selfcheck job not found")
		return
	}

	job.mu.Lock()
	item, ok := job.cases[caseID]
	if !ok {
		job.mu.Unlock()
		writeOpenAIError(w, http.StatusNotFound, "selfcheck case not found")
		return
	}
	if _, busy := job.running[caseID]; busy {
		job.mu.Unlock()
		writeOpenAIError(w, http.StatusConflict, "case retry already in progress")
		return
	}
	job.running[caseID] = struct{}{}
	// A retry re-opens a finished job so its aggregate status reflects the rerun.
	if job.Status != "running" {
		job.Status = "running"
		job.FinishedAt = ""
	}
	lanRoot := job.LANRoot
	job.mu.Unlock()
	if lanRoot == "" {
		lanRoot = selfcheckLANRoot(s.router.State())
	}

	// Retry needs a live route/key; the original ones were cleaned up when the
	// job finished. Re-prepare against the current provider state.
	if item.setupErr == "" {
		if provider, found := findProvider(s.router.State(), item.providerID); found {
			if route, key, model, created, err := s.ensureSelfcheckRouteAndKey(provider, item.protocol); err == nil {
				item.routeID = route.ID
				item.apiKeyName = key.Name
				item.apiKey = key.Key
				item.model = model
				item.routeCreated = created
			} else {
				item.setupErr = err.Error()
			}
		} else {
			item.setupErr = "provider not found"
		}
	}

	go func() {
		result := s.runPreparedSelfcheckCase(job, lanRoot, item)
		// Clean up the transient key/route this retry may have created.
		routeIDs := map[string]struct{}{}
		if item.routeCreated && item.routeID != "" {
			routeIDs[item.routeID] = struct{}{}
		}
		s.cleanupSelfcheckArtifacts(routeIDs)

		job.mu.Lock()
		replaced := false
		for i := range job.Results {
			if job.Results[i].CaseID == caseID {
				job.Results[i] = result
				replaced = true
				break
			}
		}
		if !replaced {
			job.Results = append(job.Results, result)
			if job.Completed < job.Total {
				job.Completed++
			}
		}
		delete(job.running, caseID)
		if len(job.running) == 0 && job.Completed >= job.Total {
			job.Status = "done"
			job.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		}
		job.mu.Unlock()
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{"jobId": jobID, "caseId": caseID})
}

func (s *Server) runSelfcheckJob(job *selfcheckJob, providerIDs []string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			job.mu.Lock()
			job.Status = "error"
			job.Error = fmt.Sprintf("panic: %v", recovered)
			job.FinishedAt = time.Now().UTC().Format(time.RFC3339)
			job.mu.Unlock()
		}
	}()

	cases := make([]preparedCase, 0, len(providerIDs)*selfcheckCasesPerProvider)
	// Prepare routes/keys serially to avoid races on router state.
	for _, providerID := range providerIDs {
		state := s.router.State()
		provider, ok := findProvider(state, providerID)
		providerName := providerID
		if ok {
			providerName = provider.Name
		}
		for _, pair := range []struct {
			client   selfcheckClientCase
			protocol domain.Protocol
		}{
			{selfcheckClientOpenCode, domain.ProtocolOpenAIChat},
			{selfcheckClientCodex, domain.ProtocolOpenAIResponses},
			{selfcheckClientClaude, domain.ProtocolClaude},
		} {
			var routeID, apiKeyName, apiKey, model, setupErr string
			routeCreated := false
			if !ok {
				setupErr = "provider not found"
			} else if route, key, m, created, err := s.ensureSelfcheckRouteAndKey(provider, pair.protocol); err != nil {
				setupErr = err.Error()
			} else {
				routeID = route.ID
				routeCreated = created
				apiKeyName = key.Name
				apiKey = key.Key
				model = m
			}
			// Each (provider, client) yields two cases: chat + tool call.
			for _, kind := range []selfcheckCaseKind{selfcheckKindChat, selfcheckKindTool} {
				cases = append(cases, preparedCase{
					caseID:       selfcheckCaseID(providerID, pair.client, kind),
					providerID:   providerID,
					providerName: providerName,
					client:       pair.client,
					kind:         kind,
					protocol:     pair.protocol,
					routeID:      routeID,
					apiKeyName:   apiKeyName,
					apiKey:       apiKey,
					model:        model,
					routeCreated: routeCreated,
					setupErr:     setupErr,
				})
			}
		}
	}

	// Retain prepared cases for single-case retry.
	job.mu.Lock()
	for _, item := range cases {
		job.cases[item.caseID] = item
	}
	job.mu.Unlock()

	lanRoot := job.LANRoot
	if lanRoot == "" {
		lanRoot = selfcheckLANRoot(s.router.State())
	}

	var wg sync.WaitGroup
	for _, item := range cases {
		wg.Add(1)
		go func(item preparedCase) {
			defer wg.Done()
			result := s.runPreparedSelfcheckCase(job, lanRoot, item)
			job.mu.Lock()
			job.Results = append(job.Results, result)
			job.Completed++
			job.mu.Unlock()
		}(item)
	}
	wg.Wait()

	// Clean up the transient routes/keys self-check relies on so the API-key list
	// doesn't accumulate `selfcheck-*` entries. We sweep *all* selfcheck-prefixed
	// keys (they are always throwaway), plus any routes this run created.
	routeIDs := make(map[string]struct{})
	for _, item := range cases {
		if item.routeCreated && item.routeID != "" {
			routeIDs[item.routeID] = struct{}{}
		}
	}
	s.cleanupSelfcheckArtifacts(routeIDs)

	job.mu.Lock()
	job.Status = "done"
	job.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	job.mu.Unlock()
	s.logs.AddApp("info", "selfcheck job finished", fmt.Sprintf("job=%s completed=%d", job.ID, job.Total))
}

// cleanupSelfcheckArtifacts removes every transient self-check API key (matched
// by the `selfcheck-` name prefix) and any routes the run created, keeping the
// console free of leftover entries. Failures are logged but never abort the job.
func (s *Server) cleanupSelfcheckArtifacts(routeIDs map[string]struct{}) {
	keyRemoved := false
	for _, key := range s.router.State().APIKeys {
		if !strings.HasPrefix(key.Name, selfcheckKeyPrefix) {
			continue
		}
		if err := s.router.DeleteAPIKey(key.ID); err != nil {
			s.logs.AddApp("warn", "selfcheck key cleanup failed", fmt.Sprintf("key=%s err=%v", key.ID, err))
			continue
		}
		keyRemoved = true
		if s.apiKeyStore != nil {
			if err := s.apiKeyStore.DeleteAPIKey(key.ID); err != nil {
				s.logs.AddApp("warn", "selfcheck key cleanup (db) failed", fmt.Sprintf("key=%s err=%v", key.ID, err))
			}
		}
		s.logs.AddApp("info", "selfcheck api key removed", key.ID)
	}
	routeRemoved := false
	for id := range routeIDs {
		if err := s.router.DeleteRoute(id); err != nil {
			s.logs.AddApp("warn", "selfcheck route cleanup failed", fmt.Sprintf("route=%s err=%v", id, err))
			continue
		}
		routeRemoved = true
		s.logs.AddApp("info", "selfcheck route removed", id)
	}
	// Persist deletions. apiKeyStore covers key rows directly; saveState covers
	// route removals (and key removals when there's no apiKeyStore).
	if routeRemoved || (keyRemoved && s.apiKeyStore == nil) {
		if err := s.saveState(); err != nil {
			s.logs.AddApp("warn", "selfcheck cleanup save failed", err.Error())
		}
	}
}

func (s *Server) runPreparedSelfcheckCase(
	job *selfcheckJob,
	lanRoot string,
	item preparedCase,
) selfcheckCaseResult {
	started := time.Now()
	result := selfcheckCaseResult{
		CaseID:       item.caseID,
		ProviderID:   item.providerID,
		ProviderName: item.providerName,
		Client:       string(item.client),
		Kind:         string(item.kind),
		Protocol:     string(item.protocol),
		RouteID:      item.routeID,
		APIKeyName:   item.apiKeyName,
		Model:        item.model,
	}
	if item.setupErr != "" {
		result.Error = item.setupErr
		result.LatencyMs = time.Since(started).Milliseconds()
		return result
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(job.TimeoutMs)*time.Millisecond)
	defer cancel()

	caseDir := filepath.Join(selfcheckCacheRoot(), job.ID, fmt.Sprintf("%s-%s-%s", item.providerID, item.client, item.kind))
	_ = os.MkdirAll(caseDir, 0o755)

	// Tool-call probe: ask the CLI to create a random /tmp file and write a
	// token, then verify on disk. Chat probe: the plain-text prompt.
	prompt := job.Prompt
	var toolProbe *selfcheckToolProbe
	if item.kind == selfcheckKindTool {
		probe, err := newSelfcheckToolProbe()
		if err != nil {
			result.Error = "prepare tool probe: " + err.Error()
			result.LatencyMs = time.Since(started).Milliseconds()
			return result
		}
		toolProbe = probe
		prompt = probe.prompt
		defer probe.cleanup()
	}

	var output string
	var runErr error
	switch item.client {
	case selfcheckClientOpenCode:
		output, runErr = runOpenCodeSelfcheck(ctx, caseDir, lanRoot, item.apiKey, item.model, prompt, item.kind)
	case selfcheckClientCodex:
		output, runErr = runCodexSelfcheck(ctx, caseDir, lanRoot, item.apiKey, item.model, prompt)
	case selfcheckClientClaude:
		output, runErr = runClaudeSelfcheck(ctx, caseDir, lanRoot, item.apiKey, item.model, prompt, item.kind)
	default:
		runErr = fmt.Errorf("unknown client %q", item.client)
	}

	result.LatencyMs = time.Since(started).Milliseconds()
	preview := truncateSelfcheckPreview(output)
	result.OutputPreview = preview
	if runErr != nil {
		result.Success = false
		result.Error = runErr.Error()
		if preview == "" {
			result.OutputPreview = truncateSelfcheckPreview(runErr.Error())
		}
		result.ContentOK = false
		return result
	}
	result.Success = true
	if item.kind == selfcheckKindTool {
		ok, detail := toolProbe.verify()
		result.ContentOK = ok
		if !ok && result.Error == "" {
			if toolErr := extractOpenCodeToolError(output); toolErr != "" {
				result.Error = detail + "; " + toolErr
			} else {
				result.Error = detail
			}
		}
		if ok && result.OutputPreview == "" {
			result.OutputPreview = detail
		}
	} else {
		result.ContentOK = contentLooksOK(output, job.Prompt)
		if !result.ContentOK && result.Error == "" {
			result.Error = "response content did not look correct"
		}
	}
	return result
}

func (s *Server) ensureSelfcheckRouteAndKey(provider domain.Provider, protocol domain.Protocol) (domain.Route, domain.APIKey, string, bool, error) {
	state := s.router.State()
	routeCreated := false
	route, found := findRouteForProviderProtocol(state, provider.ID, protocol)
	if !found {
		created, err := s.router.AddRoute(domain.Route{
			Name:           fmt.Sprintf("%s · %s", provider.Name, protocol.DisplayName()),
			ProviderID:     provider.ID,
			OutputProtocol: protocol,
			Mode:           domain.RouteModeAuto,
			Enabled:        true,
		})
		if err != nil {
			return domain.Route{}, domain.APIKey{}, "", false, fmt.Errorf("create route: %w", err)
		}
		if err := s.saveState(); err != nil {
			return domain.Route{}, domain.APIKey{}, "", false, fmt.Errorf("save route: %w", err)
		}
		route = created
		routeCreated = true
		s.logs.AddApp("info", "selfcheck route created", route.ID)
	}

	keyName := selfcheckKeyName(provider.ID, protocol)
	state = s.router.State()
	if key, ok := findAPIKeyByName(state, keyName); ok {
		if key.RouteID != route.ID || !key.Enabled {
			updated, err := s.router.UpdateAPIKey(key.ID, domain.APIKey{
				RouteID:       route.ID,
				Enabled:       true,
				StreamEnabled: true,
				ModelOverride: key.ModelOverride,
			})
			if err != nil {
				return domain.Route{}, domain.APIKey{}, "", false, fmt.Errorf("update selfcheck key: %w", err)
			}
			if s.apiKeyStore != nil {
				_ = s.apiKeyStore.UpdateAPIKey(updated)
			} else if err := s.saveState(); err != nil {
				return domain.Route{}, domain.APIKey{}, "", false, err
			}
			key = updated
		}
		model := resolveSelfcheckModel(key, provider)
		return route, key, model, routeCreated, nil
	}

	created, err := s.router.AddAPIKey(domain.APIKey{
		Name:          keyName,
		RouteID:       route.ID,
		Enabled:       true,
		StreamEnabled: true,
	})
	if err != nil {
		return domain.Route{}, domain.APIKey{}, "", false, fmt.Errorf("create selfcheck key: %w", err)
	}
	if s.apiKeyStore != nil {
		if err := s.apiKeyStore.CreateAPIKey(created); err != nil {
			return domain.Route{}, domain.APIKey{}, "", false, fmt.Errorf("persist selfcheck key: %w", err)
		}
	} else if err := s.saveState(); err != nil {
		return domain.Route{}, domain.APIKey{}, "", false, err
	}
	s.logs.AddApp("info", "selfcheck api key created", created.ID)
	model := resolveSelfcheckModel(created, provider)
	return route, created, model, routeCreated, nil
}

func resolveSelfcheckModel(key domain.APIKey, provider domain.Provider) string {
	if model := strings.TrimSpace(key.ModelOverride); model != "" {
		return model
	}
	if model := strings.TrimSpace(provider.DefaultModel); model != "" {
		return model
	}
	for _, model := range provider.Models {
		if id := strings.TrimSpace(model.ID); id != "" {
			return id
		}
	}
	return "default"
}

func selfcheckKeyName(providerID string, protocol domain.Protocol) string {
	return selfcheckKeyPrefix + providerID + "-" + string(protocol)
}

func findProvider(state domain.GatewayState, id string) (domain.Provider, bool) {
	for _, provider := range state.Providers {
		if provider.ID == id {
			return provider, true
		}
	}
	return domain.Provider{}, false
}

func findRouteForProviderProtocol(state domain.GatewayState, providerID string, protocol domain.Protocol) (domain.Route, bool) {
	var fallback domain.Route
	foundFallback := false
	for _, route := range state.Routes {
		if route.ProviderID != providerID || route.OutputProtocol != protocol {
			continue
		}
		if route.Enabled {
			return route, true
		}
		if !foundFallback {
			fallback = route
			foundFallback = true
		}
	}
	return fallback, foundFallback
}

func findAPIKeyByName(state domain.GatewayState, name string) (domain.APIKey, bool) {
	for _, key := range state.APIKeys {
		if key.Name == name {
			return key, true
		}
	}
	return domain.APIKey{}, false
}

func selfcheckLANRoot(state domain.GatewayState) string {
	for _, endpoint := range state.Endpoints {
		host := strings.TrimSpace(endpoint.ListenHost)
		if host == "" {
			host = "127.0.0.1"
		}
		port := endpoint.ListenPort
		if port <= 0 {
			port = 18093
		}
		return fmt.Sprintf("http://%s:%d", host, port)
	}
	return "http://127.0.0.1:18093"
}

func selfcheckCacheRoot() string {
	if cwd, err := os.Getwd(); err == nil && cwd != "" {
		return filepath.Join(cwd, ".cache", "selfcheck")
	}
	return filepath.Join(os.TempDir(), "llm-gateway-selfcheck")
}

func newSelfcheckJobID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "sc-" + hex.EncodeToString(buf), nil
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func truncateSelfcheckPreview(text string) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\x00", ""))
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= selfcheckPreviewLimit {
		return text
	}
	return text[:selfcheckPreviewLimit] + "…"
}

// contentLooksOK validates assistant output for the default 1+1 self-check prompt.
func contentLooksOK(text, prompt string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	lower := strings.ToLower(text)
	for _, bad := range []string{
		"connect error",
		"[error:",
		"not_found",
		"http 4",
		"http 5",
		"unauthorized",
		"invalid api key",
		"authentication",
	} {
		if strings.Contains(lower, bad) {
			return false
		}
	}
	_ = prompt // reserved for future prompt-specific validators
	return strings.Contains(text, "2") || strings.Contains(text, "二") || strings.Contains(text, "两")
}

// selfcheckToolProbe drives the tool-call self-check: the CLI is instructed to
// write a known token into a random /tmp file. Verifying the file on disk
// proves the model actually invoked a shell/file tool end-to-end through the
// gateway's protocol conversion (not just produced text).
type selfcheckToolProbe struct {
	path  string
	token string
	prompt string
}

func newSelfcheckToolProbe() (*selfcheckToolProbe, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	tokenBuf := make([]byte, 6)
	if _, err := rand.Read(tokenBuf); err != nil {
		return nil, err
	}
	name := "selfcheck-" + hex.EncodeToString(buf) + ".txt"
	path := filepath.Join(os.TempDir(), name)
	token := "OK-" + hex.EncodeToString(tokenBuf)
	// Remove any stale file so verify() can't read a leftover from a prior run.
	_ = os.Remove(path)
	prompt := fmt.Sprintf(
		"Use your shell/file tool to create the file %s and write exactly this text into it: %s. "+
			"Do not print the token in your reply; just perform the tool call. After writing, reply with the single word DONE.",
		path, token,
	)
	return &selfcheckToolProbe{path: path, token: token, prompt: prompt}, nil
}

// verify reports whether the probe file exists and contains the token.
func (p *selfcheckToolProbe) verify() (bool, string) {
	if p == nil {
		return false, "tool probe missing"
	}
	data, err := os.ReadFile(p.path)
	if err != nil {
		return false, "tool file not created: " + p.path
	}
	if !strings.Contains(string(data), p.token) {
		return false, "tool file present but token mismatch: " + p.path
	}
	return true, "tool file verified: " + p.path
}

func (p *selfcheckToolProbe) cleanup() {
	if p == nil {
		return
	}
	_ = os.Remove(p.path)
}

func resolveSelfcheckTools() []selfcheckToolInfo {
	return []selfcheckToolInfo{
		{
			ID:       "opencode",
			Label:    "OpenCode CLI",
			Client:   string(selfcheckClientOpenCode),
			Protocol: string(domain.ProtocolOpenAIChat),
			Path:     lookPathWithFallbacks("opencode", "/Users/thomas990p/.opencode/bin/opencode"),
			Found:    fileExecutable(lookPathWithFallbacks("opencode", "/Users/thomas990p/.opencode/bin/opencode")),
		},
		{
			ID:       "codex",
			Label:    "Codex CLI",
			Client:   string(selfcheckClientCodex),
			Protocol: string(domain.ProtocolOpenAIResponses),
			Path:     lookPathWithFallbacks("codex", "/Applications/ChatGPT.app/Contents/Resources/codex"),
			Found:    fileExecutable(lookPathWithFallbacks("codex", "/Applications/ChatGPT.app/Contents/Resources/codex")),
		},
		{
			ID:       "claude",
			Label:    "Claude CLI",
			Client:   string(selfcheckClientClaude),
			Protocol: string(domain.ProtocolClaude),
			Path:     lookPathWithFallbacks("claude", "/Users/thomas990p/.local/bin/claude"),
			Found:    fileExecutable(lookPathWithFallbacks("claude", "/Users/thomas990p/.local/bin/claude")),
		},
	}
}

func lookPathWithFallbacks(name string, fallbacks ...string) string {
	if path, err := exec.LookPath(name); err == nil && path != "" {
		return path
	}
	for _, candidate := range fallbacks {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if fileExecutable(candidate) {
			return candidate
		}
	}
	if len(fallbacks) > 0 {
		return fallbacks[0]
	}
	return name
}

func fileExecutable(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0o111 != 0
}

func runOpenCodeSelfcheck(ctx context.Context, caseDir, lanRoot, apiKey, model, prompt string, kind selfcheckCaseKind) (string, error) {
	bin := lookPathWithFallbacks("opencode", "/Users/thomas990p/.opencode/bin/opencode")
	if !fileExecutable(bin) {
		return "", fmt.Errorf("opencode binary not found: %s", bin)
	}
	xdgHome := caseDir
	configDir := filepath.Join(xdgHome, "opencode")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return "", err
	}
	baseURL := strings.TrimRight(lanRoot, "/") + "/v1"
	config := map[string]any{
		"model": "gateway/" + model,
		"provider": map[string]any{
			"gateway": map[string]any{
				"npm":  "@ai-sdk/openai-compatible",
				"name": "gateway",
				"options": map[string]any{
					"apiKey":  apiKey,
					"baseURL": baseURL,
				},
				"models": map[string]any{
					model: map[string]any{"name": model},
				},
			},
		},
	}
	configBytes, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(configDir, "opencode.json"), configBytes, 0o600); err != nil {
		return "", err
	}

	args := []string{"run", "--pure", "--format", "json", "-m", "gateway/" + model}
	// Tool probe must auto-approve write/bash; otherwise OpenCode records
	// tool_use with status=error and never creates the probe file.
	if kind == selfcheckKindTool {
		args = append(args, "--dangerously-skip-permissions")
	}
	args = append(args, prompt)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = caseDir
	cmd.Env = append(os.Environ(),
		"XDG_CONFIG_HOME="+xdgHome,
		"OPENAI_API_KEY="+apiKey,
	)
	cmd.Stdin = bytes.NewReader(nil)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	combined := strings.TrimSpace(stdout.String())
	if combined == "" {
		combined = strings.TrimSpace(stderr.String())
	}
	text := extractOpenCodeAssistantText(stdout.String())
	if text == "" {
		text = combined
	}
	if runErr != nil {
		if ctx.Err() != nil {
			return text, fmt.Errorf("opencode timed out: %w", ctx.Err())
		}
		return text, fmt.Errorf("opencode failed: %w; %s", runErr, truncateSelfcheckPreview(stderr.String()))
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("opencode returned empty output")
	}
	return text, nil
}

// extractOpenCodeToolError digs tool_use error details out of OpenCode JSON
// event lines so self-check failures explain why the write didn't happen.
func extractOpenCodeToolError(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		part, _ := event["part"].(map[string]any)
		if part == nil {
			continue
		}
		state, _ := part["state"].(map[string]any)
		if state == nil || stringValue(state["status"]) != "error" {
			continue
		}
		tool := firstNonEmpty(stringValue(part["tool"]), stringValue(event["tool"]))
		msg := firstNonEmpty(stringValue(state["error"]), stringValue(state["message"]), stringValue(state["text"]))
		if tool == "" && msg == "" {
			continue
		}
		if msg == "" {
			return "opencode tool " + tool + " failed"
		}
		return "opencode tool " + tool + " failed: " + truncateSelfcheckPreview(msg)
	}
	if strings.Contains(raw, `"status":"error"`) || strings.Contains(raw, `"status": "error"`) {
		return "opencode tool call reported status=error (likely permission denied)"
	}
	return ""
}

func extractOpenCodeAssistantText(raw string) string {
	var parts []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if text := openCodeEventText(event); text != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) > 0 {
		return strings.TrimSpace(strings.Join(parts, ""))
	}
	// Fallback: whole-document JSON
	var doc any
	if err := json.Unmarshal([]byte(raw), &doc); err == nil {
		if text := collectJSONText(doc); text != "" {
			return text
		}
	}
	return strings.TrimSpace(raw)
}

func openCodeEventText(event map[string]any) string {
	typ, _ := event["type"].(string)
	switch typ {
	case "text", "message.part", "part":
		if part, ok := event["part"].(map[string]any); ok {
			if text, ok := part["text"].(string); ok {
				return text
			}
		}
		if text, ok := event["text"].(string); ok {
			return text
		}
	case "message", "assistant", "text-delta":
		if text, ok := event["text"].(string); ok {
			return text
		}
		if delta, ok := event["delta"].(string); ok {
			return delta
		}
		if msg, ok := event["message"].(map[string]any); ok {
			if content, ok := msg["content"].(string); ok {
				return content
			}
		}
	}
	if text, ok := event["text"].(string); ok && (typ == "" || strings.Contains(strings.ToLower(typ), "text")) {
		return text
	}
	return ""
}

func collectJSONText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case map[string]any:
		if text, ok := typed["text"].(string); ok && text != "" {
			return text
		}
		if content, ok := typed["content"].(string); ok && content != "" {
			return content
		}
		var parts []string
		for _, key := range []string{"parts", "content", "messages", "output"} {
			if nested, ok := typed[key]; ok {
				if text := collectJSONText(nested); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "")
	case []any:
		var parts []string
		for _, item := range typed {
			if text := collectJSONText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "")
	default:
		return ""
	}
}

func runCodexSelfcheck(ctx context.Context, caseDir, lanRoot, apiKey, model, prompt string) (string, error) {
	bin := lookPathWithFallbacks("codex", "/Applications/ChatGPT.app/Contents/Resources/codex")
	if !fileExecutable(bin) {
		return "", fmt.Errorf("codex binary not found: %s", bin)
	}
	home := filepath.Join(caseDir, "codex-home")
	_ = os.RemoveAll(home)
	if err := os.MkdirAll(home, 0o755); err != nil {
		return "", err
	}
	outFile := filepath.Join(caseDir, "codex-out.txt")
	auth := map[string]string{"OPENAI_API_KEY": apiKey}
	authBytes, _ := json.Marshal(auth)
	if err := os.WriteFile(filepath.Join(home, "auth.json"), authBytes, 0o600); err != nil {
		return "", err
	}
	baseURL := strings.TrimRight(lanRoot, "/") + "/openai/v1"
	configTOML := fmt.Sprintf(`model_provider = "gateway"
model = %q
model_reasoning_effort = "low"
sandbox_mode = "danger-full-access"
[model_providers.gateway]
name = "llm-gateway"
base_url = %q
wire_api = "responses"
requires_openai_auth = true
`, model, baseURL)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(configTOML), 0o600); err != nil {
		return "", err
	}

	cmd := exec.CommandContext(ctx, bin, "exec",
		"--ephemeral",
		"--skip-git-repo-check",
		"-s", "danger-full-access",
		"-c", `model_reasoning_effort="low"`,
		"-c", "features.multi_agent=false",
		"-c", "features.memories=false",
		"-o", outFile,
		prompt,
	)
	cmd.Dir = caseDir
	cmd.Env = append(os.Environ(),
		"CODEX_HOME="+home,
		"OPENAI_API_KEY="+apiKey,
	)
	cmd.Stdin = bytes.NewReader(nil)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	outBytes, _ := os.ReadFile(outFile)
	text := strings.TrimSpace(string(outBytes))
	if text == "" {
		text = strings.TrimSpace(stdout.String())
	}
	if text == "" {
		text = strings.TrimSpace(stderr.String())
	}
	if runErr != nil {
		if ctx.Err() != nil {
			return text, fmt.Errorf("codex timed out: %w", ctx.Err())
		}
		return text, fmt.Errorf("codex failed: %w; %s", runErr, truncateSelfcheckPreview(stderr.String()))
	}
	if text == "" {
		return "", fmt.Errorf("codex returned empty output")
	}
	return text, nil
}

func runClaudeSelfcheck(ctx context.Context, caseDir, lanRoot, apiKey, model, prompt string, kind selfcheckCaseKind) (string, error) {
	bin := lookPathWithFallbacks("claude", "/Users/thomas990p/.local/bin/claude")
	if !fileExecutable(bin) {
		return "", fmt.Errorf("claude binary not found: %s", bin)
	}
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		return "", err
	}
	homeDir := filepath.Join(caseDir, "home")
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		return "", err
	}
	baseURL := strings.TrimRight(lanRoot, "/") + "/anthropic"
	systemPrompt := "You are a concise assistant. Answer briefly with text only."
	args := []string{
		"-p",
		"--bare",
		"--dangerously-skip-permissions",
	}
	if kind == selfcheckKindTool {
		// Tool probe: allow file/shell tools so the model can actually write the
		// probe file. Restrict to the minimum needed set.
		systemPrompt = "You are a helpful assistant with file and shell tools. Use them when asked."
		args = append(args, "--allowedTools", "Write,Edit,Bash")
	} else {
		// Chat probe: disable all tools so it stays a pure text round-trip.
		args = append(args, "--tools", "")
	}
	args = append(args,
		"--system-prompt", systemPrompt,
		"--output-format", "text",
	)
	if model != "" && model != "default" {
		args = append(args, "--model", model)
	}
	args = append(args, prompt)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = caseDir
	cmd.Env = append(filteredEnviron(
		"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL", "CLAUDE_API_KEY",
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy",
		"HOME",
	),
		"HOME="+homeDir,
		"ANTHROPIC_BASE_URL="+baseURL,
		"ANTHROPIC_API_KEY="+apiKey,
		"ANTHROPIC_MODEL="+model,
		"NO_PROXY=*",
		"no_proxy=*",
	)
	cmd.Stdin = bytes.NewReader(nil)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	text := strings.TrimSpace(stdout.String())
	if text == "" {
		text = strings.TrimSpace(stderr.String())
	}
	if runErr != nil {
		if ctx.Err() != nil {
			return text, fmt.Errorf("claude timed out: %w", ctx.Err())
		}
		return text, fmt.Errorf("claude failed: %w; %s", runErr, truncateSelfcheckPreview(stderr.String()))
	}
	if text == "" {
		return "", fmt.Errorf("claude returned empty output")
	}
	return text, nil
}

func filteredEnviron(dropKeys ...string) []string {
	drop := make(map[string]struct{}, len(dropKeys))
	for _, key := range dropKeys {
		drop[key] = struct{}{}
	}
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, item := range env {
		key, _, _ := strings.Cut(item, "=")
		if _, skip := drop[key]; skip {
			continue
		}
		out = append(out, item)
	}
	return out
}
