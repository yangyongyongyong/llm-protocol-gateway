package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

func TestProviderScriptABIValidation(t *testing.T) {
	valid := scriptIsValid(domain.ProviderScript{Source: "print('ok')"})
	if !valid.Valid {
		t.Fatalf("valid script rejected: %+v", valid)
	}
	if valid.ABIVersion != providerScriptABIVersion || !strings.Contains(valid.ABI, "PROVIDER_SCRIPT_MAX_CONCURRENCY") || !strings.Contains(valid.ABI, "Retry-After") {
		t.Fatalf("ABI contract is incomplete: %+v", valid)
	}
	empty := scriptIsValid(domain.ProviderScript{})
	if empty.Valid || len(empty.Errors) == 0 {
		t.Fatalf("empty script accepted: %+v", empty)
	}
	badLanguage := scriptIsValid(domain.ProviderScript{Source: "x", Language: "node"})
	if badLanguage.Valid {
		t.Fatalf("unsupported language accepted: %+v", badLanguage)
	}
	gemini := validateProviderScript(domain.ProviderScript{Source: "print('ok')"}, domain.ProtocolGemini)
	if gemini.Valid {
		t.Fatalf("gemini script provider accepted: %+v", gemini)
	}
}

func TestProviderScriptValidationCompilesPython(t *testing.T) {
	if _, err := exec.LookPath(providerScriptPythonCommand()); err != nil {
		t.Skip("python3 is not installed")
	}
	bad := validateProviderScript(domain.ProviderScript{
		Language: "python", Entrypoint: "adapter.py", Source: "def broken(:\n    pass\n",
		UpstreamBaseURL: "https://vendor.example",
	}, domain.ProtocolOpenAIChat)
	if bad.Valid || !strings.Contains(strings.Join(bad.Errors, ";"), "语法校验失败") {
		t.Fatalf("invalid python was accepted: %+v", bad)
	}
	missingPort := validateProviderScript(domain.ProviderScript{
		Language: "python", Entrypoint: "adapter.py", Source: "print('ok')\n",
		UpstreamBaseURL: "https://vendor.example",
	}, domain.ProtocolOpenAIChat)
	if !missingPort.Valid {
		t.Fatalf("warnings should not reject a syntactically valid script: %+v", missingPort)
	}
	if !strings.Contains(strings.Join(missingPort.Warnings, ";"), "PROVIDER_SCRIPT_PORT") {
		t.Fatalf("missing port warning absent: %+v", missingPort)
	}
}

func TestProviderScriptPromptCoversOperationalContract(t *testing.T) {
	prompt := providerScriptPrompt()
	for _, want := range []string{
		"PROVIDER_SCRIPT_MAX_CONCURRENCY",
		"PROVIDER_SCRIPT_REQUEST_TIMEOUT_SEC",
		"data: [DONE]",
		"tool_calls",
		"Retry-After",
		"/v1/models",
		"SIGTERM",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("provider script prompt missing %q", want)
		}
	}
}

func TestRouterProviderScriptPresenceSemantics(t *testing.T) {
	router := NewRouter(domain.GatewayState{Providers: []domain.Provider{{
		ID: "script-router", Name: "Script Router", Protocol: domain.ProtocolOpenAIChat,
		BaseURL: "http://127.0.0.1:19001/v1", APIKeySource: "literal:key",
		ProviderScript: &domain.ProviderScript{Language: "python", Entrypoint: "adapter.py", Source: "print('old')"},
	}}})
	if _, err := router.UpdateProvider("script-router", domain.Provider{Name: "Renamed"}); err != nil {
		t.Fatal(err)
	}
	got, _ := router.ProviderByID("script-router")
	if got.ProviderScript == nil || got.ProviderScript.Source != "print('old')" {
		t.Fatalf("omitted providerScript cleared the script: %+v", got.ProviderScript)
	}
	newScript := &domain.ProviderScript{Language: "python", Entrypoint: "adapter.py", Source: "print('new')"}
	got, err := router.updateProviderWithScript("script-router", domain.Provider{ProviderScript: newScript}, true)
	if err != nil || got.ProviderScript == nil || got.ProviderScript.Source != "print('new')" {
		t.Fatalf("replace script: provider=%+v err=%v", got, err)
	}
	got, err = router.updateProviderWithScript("script-router", domain.Provider{BaseURL: "https://normal.example/v1", ProviderScript: nil}, true)
	if err != nil || got.ProviderScript != nil || got.BaseURL != "https://normal.example/v1" {
		t.Fatalf("clear script: provider=%+v err=%v", got, err)
	}
}

func TestProviderScriptRuntimeStartsAndStops(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	runtime := newProviderScriptRuntime()
	runtime.rootDir = t.TempDir()
	script := `from http.server import BaseHTTPRequestHandler, HTTPServer
import json, os

class H(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass
    def do_GET(self):
        if self.path == "/health":
            self.send_response(200); self.end_headers(); self.wfile.write(b"ok"); return
        if self.path == "/v1/models":
            self.send_response(200); self.send_header("Content-Type", "application/json"); self.end_headers()
            self.wfile.write(json.dumps({"data":[{"id":"script-model"}]}).encode()); return
        self.send_response(404); self.end_headers()
    def do_POST(self):
        if self.path != "/v1/chat/completions":
            self.send_response(404); self.end_headers(); return
        length = int(self.headers.get("Content-Length", "0"))
        _ = self.rfile.read(length)
        payload = {"id":"chatcmpl-script","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"script-ok"},"finish_reason":"stop"}]}
        self.send_response(200); self.send_header("Content-Type", "application/json"); self.end_headers()
        self.wfile.write(json.dumps(payload).encode())

server = HTTPServer(("127.0.0.1", int(os.environ["PROVIDER_SCRIPT_PORT"])), H)
server.serve_forever()
`
	provider := domain.Provider{
		ID: "script-test", Name: "script-test", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypeAPIKey, APIKeySource: "literal:sk-test",
		DefaultModel: "script-model",
		ProviderScript: &domain.ProviderScript{
			Language: "python", Entrypoint: "adapter.py", Source: script,
			UpstreamBaseURL: "https://vendor.example", ListenPort: port,
		},
	}
	if err := runtime.start(context.Background(), provider); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { runtime.stop(provider.ID) })
	if err := runtime.waitHealthy(context.Background(), provider, 30*time.Second); err != nil {
		t.Fatalf("health: %v", err)
	}
	status := runtime.status(provider.ID)
	if status["running"] != true {
		t.Fatalf("status = %#v", status)
	}
	resp, err := http.Get(runtime.baseURL(provider) + "/v1/models")
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "script-model") {
		t.Fatalf("models response: status=%d body=%s", resp.StatusCode, body)
	}
	post, _ := json.Marshal(map[string]any{"model": "script-model", "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	resp, err = http.Post(runtime.baseURL(provider)+"/v1/chat/completions", "application/json", strings.NewReader(string(post)))
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "script-ok") {
		t.Fatalf("chat response: status=%d body=%s", resp.StatusCode, body)
	}
	runtime.stop(provider.ID)
	if got := runtime.status(provider.ID)["running"]; got != false {
		t.Fatalf("running after stop = %#v", got)
	}
}

// testProviderScriptSource 生成一个最小但完整的脚本：健康检查、模型发现和
// Chat 入口都有，POST 一律返回成功，足以验证网关的启动/重启/模型发现链路。
func testProviderScriptSource(model string) string {
	script := `from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json, os
MODEL = "__MODEL__"
class H(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass
    def do_GET(self):
        if self.path == "/health":
            self.send_response(200); self.end_headers(); self.wfile.write(b"ok"); return
        if self.path == "/v1/models":
            body = {"data":[{"id":MODEL,"context_length":128000}]}
            self.send_response(200); self.send_header("Content-Type", "application/json"); self.end_headers()
            self.wfile.write(json.dumps(body).encode()); return
        self.send_response(404); self.end_headers()
    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        _ = self.rfile.read(length)
        if self.path == "/v1/chat/completions":
            body = {"id":"chatcmpl-script","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"script-ok"},"finish_reason":"stop"}]}
        else:
            body = {"ok": True}
        self.send_response(200); self.send_header("Content-Type", "application/json"); self.end_headers()
        self.wfile.write(json.dumps(body).encode())
server = ThreadingHTTPServer(("127.0.0.1", int(os.environ["PROVIDER_SCRIPT_PORT"])), H)
server.daemon_threads = True
server.serve_forever()
`
	return strings.ReplaceAll(script, "__MODEL__", model)
}

func TestProviderScriptRuntimeRestartUsesNewSource(t *testing.T) {
	if _, err := exec.LookPath(providerScriptPythonCommand()); err != nil {
		t.Skip("python3 is not installed")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	runtime := newProviderScriptRuntime()
	runtime.rootDir = t.TempDir()
	provider := domain.Provider{
		ID: "script-restart", Name: "script-restart", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypeAPIKey, APIKeySource: "literal:sk-test", DefaultModel: "model-v1",
		ProviderScript: &domain.ProviderScript{
			Language: "python", Entrypoint: "adapter.py", Source: testProviderScriptSource("model-v1"),
			UpstreamBaseURL: "https://vendor.example", ListenPort: port, MaxConcurrentRequests: 2,
		},
	}
	if err := runtime.start(context.Background(), provider); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runtime.stop(provider.ID) })
	if err := runtime.waitHealthy(context.Background(), provider, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	first := runtime.status(provider.ID)
	provider.ProviderScript.Source = testProviderScriptSource("model-v2")
	if err := runtime.restart(context.Background(), provider); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if err := runtime.waitHealthy(context.Background(), provider, 30*time.Second); err != nil {
		t.Fatalf("health after restart: %v", err)
	}
	second := runtime.status(provider.ID)
	if first["pid"] == second["pid"] {
		t.Fatalf("restart kept the same pid: first=%v second=%v", first["pid"], second["pid"])
	}
	resp, err := http.Get(runtime.baseURL(provider) + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(body), "model-v2") || strings.Contains(string(body), "model-v1") {
		t.Fatalf("new source not active after restart: %s", body)
	}
}

func TestProviderScriptLifecycleUpdateAndClear(t *testing.T) {
	if _, err := exec.LookPath(providerScriptPythonCommand()); err != nil {
		t.Skip("python3 is not installed")
	}
	oldRoot := providerScriptRuntimes.rootDir
	providerScriptRuntimes.rootDir = t.TempDir()
	t.Cleanup(func() {
		providerScriptRuntimes.stopAll()
		providerScriptRuntimes.rootDir = oldRoot
	})
	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	createBody, _ := json.Marshal(domain.Provider{
		Name: "script-contract-lifecycle", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypeAPIKey, APIKeySource: "literal:vendor-key",
		ProviderScript: &domain.ProviderScript{
			Language: "python", Entrypoint: "adapter.py", Source: testProviderScriptSource("model-v1"),
			UpstreamBaseURL: "https://vendor.example", MaxConcurrentRequests: 2, RequestTimeoutSec: 30,
		},
	})
	create := httptest.NewRequest(http.MethodPost, "/__providers", strings.NewReader(string(createBody)))
	createRec := httptest.NewRecorder()
	server.handleCreateProvider(createRec, create)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createRec.Code, createRec.Body.String())
	}
	var created domain.Provider
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ProviderScript == nil || created.DefaultModel != "model-v1" || len(created.Models) != 1 {
		t.Fatalf("create did not discover script model: %+v", created)
	}
	firstStatus := providerScriptRuntimes.status(created.ID)
	if firstStatus["running"] != true {
		t.Fatalf("script not running after create: %+v", firstStatus)
	}

	updateBody, _ := json.Marshal(domain.Provider{
		Name: created.Name, Protocol: created.Protocol, BaseURL: created.BaseURL,
		AuthType: domain.AuthTypeAPIKey, APIKeySource: created.APIKeySource,
		ProviderScript: &domain.ProviderScript{
			Language: "python", Entrypoint: "adapter.py", Source: testProviderScriptSource("model-v2"),
			UpstreamBaseURL: "https://vendor.example", MaxConcurrentRequests: 3, RequestTimeoutSec: 45,
		},
	})
	update := httptest.NewRequest(http.MethodPatch, "/__providers/"+created.ID, strings.NewReader(string(updateBody)))
	update.SetPathValue("id", created.ID)
	updateRec := httptest.NewRecorder()
	server.handleUpdateProvider(updateRec, update)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", updateRec.Code, updateRec.Body.String())
	}
	var updated domain.Provider
	if err := json.Unmarshal(updateRec.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.DefaultModel != "model-v2" {
		t.Fatalf("update did not activate new model: %+v", updated)
	}
	secondStatus := providerScriptRuntimes.status(created.ID)
	if firstStatus["pid"] == secondStatus["pid"] {
		t.Fatalf("script process was not replaced: first=%v second=%v", firstStatus, secondStatus)
	}

	clearPayload := map[string]any{
		"name": created.Name, "protocol": "openai_chat", "baseUrl": "https://normal.example/v1",
		"authType": "api_key", "apiKeySource": "literal:vendor-key", "providerScript": nil,
	}
	clearBody, _ := json.Marshal(clearPayload)
	clear := httptest.NewRequest(http.MethodPatch, "/__providers/"+created.ID, strings.NewReader(string(clearBody)))
	clear.SetPathValue("id", created.ID)
	clearRec := httptest.NewRecorder()
	server.handleUpdateProvider(clearRec, clear)
	if clearRec.Code != http.StatusOK {
		t.Fatalf("clear status=%d body=%s", clearRec.Code, clearRec.Body.String())
	}
	var cleared domain.Provider
	if err := json.Unmarshal(clearRec.Body.Bytes(), &cleared); err != nil {
		t.Fatal(err)
	}
	if cleared.ProviderScript != nil || cleared.BaseURL != "https://normal.example/v1" {
		t.Fatalf("script not cleared: %+v", cleared)
	}
	if providerScriptRuntimes.status(created.ID)["running"] != false {
		t.Fatal("script process still running after clear")
	}
}

func TestProviderScriptConcurrencyIsAppliedByGateway(t *testing.T) {
	cases := []struct {
		name   string
		row    int
		script int
		want   int
	}{
		{name: "script tighter", row: 8, script: 3, want: 3},
		{name: "row tighter", row: 2, script: 5, want: 2},
		{name: "only script", row: 0, script: 4, want: 4},
		{name: "neither", row: 0, script: 0, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := domain.Provider{MaxConcurrentRequests: tc.row}
			if tc.script > 0 {
				provider.ProviderScript = &domain.ProviderScript{MaxConcurrentRequests: tc.script}
			}
			if got := providerConcurrentCap(provider); got != tc.want {
				t.Fatalf("providerConcurrentCap = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestProviderScriptImportRebindsRuntimeURL(t *testing.T) {
	router := NewRouter(domain.GatewayState{})
	bundle := ProvidersExportBundle{
		Version: providersExportVersion,
		Providers: []domain.Provider{{
			ID: "import-script", Name: "Import Script", Protocol: domain.ProtocolOpenAIChat,
			BaseURL: "http://127.0.0.1:19000/v1", AuthType: domain.AuthTypeAPIKey,
			ProviderScript: &domain.ProviderScript{
				Language: "python", Entrypoint: "adapter.py", Source: "print('ok')",
				UpstreamBaseURL: "https://vendor.example",
			},
		}},
	}
	result := router.ImportProviders(bundle)
	if len(result.Created) != 1 || len(result.Errors) != 0 {
		t.Fatalf("import result: %+v", result)
	}
	got, err := router.ProviderByID("import-script")
	if err != nil {
		t.Fatal(err)
	}
	if got.ProviderScript == nil {
		t.Fatal("imported provider lost its script")
	}
	if want := providerScriptRuntimes.apiBaseURL(got); got.BaseURL != want {
		t.Fatalf("imported BaseURL = %q, want derived %q", got.BaseURL, want)
	}
}

func TestProviderScriptManualDefaultModelSurvivesModelsFailure(t *testing.T) {
	if _, err := exec.LookPath(providerScriptPythonCommand()); err != nil {
		t.Skip("python3 is not installed")
	}
	oldRoot := providerScriptRuntimes.rootDir
	providerScriptRuntimes.rootDir = t.TempDir()
	t.Cleanup(func() {
		providerScriptRuntimes.stopAll()
		providerScriptRuntimes.rootDir = oldRoot
	})
	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	source := strings.ReplaceAll(testProviderScriptSource("catalog-model"), `"/v1/models"`, `"/missing-models"`)
	body, _ := json.Marshal(domain.Provider{
		Name: "script-manual-default", Protocol: domain.ProtocolOpenAIChat,
		AuthType: domain.AuthTypeAPIKey, APIKeySource: "literal:vendor-key", DefaultModel: "manual-model",
		ProviderScript: &domain.ProviderScript{
			Language: "python", Entrypoint: "adapter.py", Source: source,
			UpstreamBaseURL: "https://vendor.example",
		},
	})
	request := httptest.NewRequest(http.MethodPost, "/__providers", strings.NewReader(string(body)))
	recorder := httptest.NewRecorder()
	server.handleCreateProvider(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var created domain.Provider
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.DefaultModel != "manual-model" {
		t.Fatalf("manual default was overwritten when /models failed: %+v", created)
	}
	if len(created.Models) != 1 || created.Models[0].ID != "manual-model" {
		t.Fatalf("manual fallback model not preserved: %+v", created.Models)
	}
}
