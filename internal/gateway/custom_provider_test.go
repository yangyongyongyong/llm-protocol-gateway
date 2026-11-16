package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

func TestCustomProviderHydrationAndDeleteGuard(t *testing.T) {
	router := NewRouter(domain.GatewayState{})
	custom, err := router.AddCustomProvider(domain.CustomProvider{
		ID: "ocean", Name: "ocean", Protocol: domain.ProtocolOpenAIChat, AuthHeader: "api-key", DefaultModel: "gpt-4o",
		Script: domain.ProviderScript{
			Language: "python", Entrypoint: "adapter.py", Source: "print('ocean')",
			HealthPath: "/health", MaxConcurrentRequests: 4, RequestTimeoutSec: 120,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{router: router}
	hydrated, err := server.hydrateProviderFromCustomProvider(domain.Provider{
		Name: "Ocean dev", APIKeySource: "literal:key",
	}, custom, "http://ocean-dev.example")
	if err != nil {
		t.Fatal(err)
	}
	if hydrated.CustomProviderID != "ocean" || hydrated.Protocol != domain.ProtocolOpenAIChat || hydrated.AuthHeader != "api-key" {
		t.Fatalf("hydrated provider = %+v", hydrated)
	}
	if hydrated.DefaultModel != "gpt-4o" {
		t.Fatalf("template default model not applied: %+v", hydrated)
	}
	if hydrated.ProviderScript == nil || hydrated.ProviderScript.UpstreamBaseURL != "http://ocean-dev.example" {
		t.Fatalf("hydrated script = %+v", hydrated.ProviderScript)
	}
	created, err := router.AddProvider(hydrated)
	if err != nil {
		t.Fatal(err)
	}
	if created.CustomProviderID != "ocean" || created.BaseURL == hydrated.BaseURL || created.ProviderScript == nil {
		t.Fatalf("created provider = %+v", created)
	}
	if err := router.DeleteCustomProvider("ocean"); err == nil {
		t.Fatal("deleting a custom provider still used by an instance must fail")
	}
	if err := router.DeleteProvider(created.ID); err != nil {
		t.Fatal(err)
	}
	if err := router.PurgeProvider(created.ID); err != nil {
		t.Fatal(err)
	}
	if err := router.DeleteCustomProvider("ocean"); err != nil {
		t.Fatalf("deleting unused custom provider: %v", err)
	}
}

func TestCustomProviderTemplateKeepsInstanceUpstreamSeparate(t *testing.T) {
	router := NewRouter(domain.GatewayState{})
	custom, err := router.AddCustomProvider(domain.CustomProvider{
		Name: "ocean", Protocol: domain.ProtocolOpenAIChat, AuthHeader: "api-key",
		Script: domain.ProviderScript{Language: "python", Entrypoint: "adapter.py", Source: "print('ocean')"},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{router: router}
	a, err := server.hydrateProviderFromCustomProvider(domain.Provider{Name: "A", APIKeySource: "literal:a"}, custom, "https://a.example")
	if err != nil {
		t.Fatal(err)
	}
	b, err := server.hydrateProviderFromCustomProvider(domain.Provider{Name: "B", APIKeySource: "literal:b"}, custom, "https://b.example")
	if err != nil {
		t.Fatal(err)
	}
	if a.ProviderScript.UpstreamBaseURL == b.ProviderScript.UpstreamBaseURL {
		t.Fatalf("instance URLs leaked into shared template: %#v %#v", a.ProviderScript, b.ProviderScript)
	}
}

func TestCustomProviderAPIInstantiatesWithURLAndKeyOnly(t *testing.T) {
	if _, err := exec.LookPath(providerScriptPythonCommand()); err != nil {
		t.Skip("python3 is not installed")
	}
	t.Setenv("GATEWAY_CUSTOM_PROVIDER_DIR", t.TempDir())
	oldRoot := providerScriptRuntimes.rootDir
	providerScriptRuntimes.rootDir = t.TempDir()
	t.Cleanup(func() {
		providerScriptRuntimes.stopAll()
		providerScriptRuntimes.rootDir = oldRoot
	})
	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	withAdmin := func(request *http.Request) *http.Request {
		return request.WithContext(context.WithValue(request.Context(), authContextKey{}, adminIdentity()))
	}
	createTypeBody, _ := json.Marshal(domain.CustomProvider{
		ID: "ocean-test", Name: "ocean-test", Protocol: domain.ProtocolOpenAIChat, AuthHeader: "api-key",
		Script: domain.ProviderScript{
			Language: "python", Entrypoint: "adapter.py", Source: testProviderScriptSource("ocean-model"),
			HealthPath: "/health", MaxConcurrentRequests: 2, RequestTimeoutSec: 30,
		},
	})
	typeRecorder := httptest.NewRecorder()
	server.handleCreateCustomProvider(typeRecorder, withAdmin(httptest.NewRequest(http.MethodPost, "/__custom-providers", strings.NewReader(string(createTypeBody)))))
	if typeRecorder.Code != http.StatusCreated {
		t.Fatalf("create custom provider: status=%d body=%s", typeRecorder.Code, typeRecorder.Body.String())
	}

	providerBody, _ := json.Marshal(domain.Provider{
		Name: "Ocean instance", CustomProviderID: "ocean-test", BaseURL: "https://vendor.example",
		APIKeySource: "literal:upstream-key",
	})
	providerRecorder := httptest.NewRecorder()
	server.handleCreateProvider(providerRecorder, withAdmin(httptest.NewRequest(http.MethodPost, "/__providers", strings.NewReader(string(providerBody)))))
	if providerRecorder.Code != http.StatusCreated {
		t.Fatalf("create provider instance: status=%d body=%s", providerRecorder.Code, providerRecorder.Body.String())
	}
	var clientView domain.Provider
	if err := json.Unmarshal(providerRecorder.Body.Bytes(), &clientView); err != nil {
		t.Fatal(err)
	}
	if clientView.CustomProviderID != "ocean-test" || clientView.ProviderScript == nil || clientView.ProviderScript.Source != "" {
		t.Fatalf("instance client view = %+v", clientView)
	}
	internal, err := server.router.ProviderByID(clientView.ID)
	if err != nil {
		t.Fatal(err)
	}
	if internal.ProviderScript == nil || internal.ProviderScript.Source == "" || internal.ProviderScript.UpstreamBaseURL != "https://vendor.example" {
		t.Fatalf("internal provider script = %+v", internal.ProviderScript)
	}
	if internal.AuthHeader != "api-key" || internal.Protocol != domain.ProtocolOpenAIChat {
		t.Fatalf("template metadata not applied: %+v", internal)
	}
	if len(internal.Models) == 0 || internal.Models[0].ID != "ocean-model" {
		t.Fatalf("model discovery did not run: %+v", internal.Models)
	}
}

func TestStateForUserHidesCustomProviderScriptSource(t *testing.T) {
	router := NewRouter(domain.GatewayState{CustomProviders: []domain.CustomProvider{{
		ID: "ocean", Name: "ocean", Protocol: domain.ProtocolOpenAIChat,
		Script: domain.ProviderScript{Language: "python", Entrypoint: "adapter.py", Source: "secret-ish script"},
	}}})
	server := &Server{router: router}
	userState := server.stateForUser(sessionIdentity{UserID: "u1", Username: "u1", Role: domain.UserRoleUser}, router.State())
	if len(userState.CustomProviders) != 1 || userState.CustomProviders[0].Script.Source != "" {
		t.Fatalf("normal user received custom provider source: %+v", userState.CustomProviders)
	}
	adminState := server.stateForUser(adminIdentity(), router.State())
	if len(adminState.CustomProviders) != 1 || adminState.CustomProviders[0].Script.Source != "secret-ish script" {
		t.Fatalf("admin lost custom provider source: %+v", adminState.CustomProviders)
	}
}

func TestCustomProviderScriptFilesLiveUnderAppDataDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GATEWAY_CUSTOM_PROVIDER_DIR", root)
	provider := domain.CustomProvider{
		ID: "my-vendor", Name: "my-vendor",
		Script: domain.ProviderScript{
			Language: "python", Entrypoint: "adapter.py", Source: "print('custom')",
			Requirements: "requests==2.32.0",
		},
	}
	dir, err := writeCustomProviderScriptFiles(provider)
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(root, "my-vendor") {
		t.Fatalf("script dir = %q", dir)
	}
	script, err := os.ReadFile(filepath.Join(dir, "adapter.py"))
	if err != nil || string(script) != "print('custom')" {
		t.Fatalf("script file = %q err=%v", script, err)
	}
	requirements, err := os.ReadFile(filepath.Join(dir, "requirements.txt"))
	if err != nil || string(requirements) != "requests==2.32.0" {
		t.Fatalf("requirements = %q err=%v", requirements, err)
	}
	if err := removeCustomProviderScriptFiles(provider.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("script dir still exists: %v", err)
	}
}

func TestCustomProviderCRUDWritesAndRemovesScriptFiles(t *testing.T) {
	if _, err := exec.LookPath(providerScriptPythonCommand()); err != nil {
		t.Skip("python3 is not installed")
	}
	root := t.TempDir()
	t.Setenv("GATEWAY_CUSTOM_PROVIDER_DIR", root)
	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	withAdmin := func(request *http.Request) *http.Request {
		return request.WithContext(context.WithValue(request.Context(), authContextKey{}, adminIdentity()))
	}
	createBody, _ := json.Marshal(domain.CustomProvider{
		Name: "vendor-x", Protocol: domain.ProtocolOpenAIChat, AuthHeader: "x-api-key",
		Script: domain.ProviderScript{Language: "python", Entrypoint: "adapter.py", Source: "print('v1')"},
	})
	createRecorder := httptest.NewRecorder()
	server.handleCreateCustomProvider(createRecorder, withAdmin(httptest.NewRequest(http.MethodPost, "/__custom-providers", strings.NewReader(string(createBody)))))
	if createRecorder.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createRecorder.Code, createRecorder.Body.String())
	}
	var created domain.CustomProvider
	if err := json.Unmarshal(createRecorder.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(root, created.ID, "adapter.py")
	if body, err := os.ReadFile(scriptPath); err != nil || string(body) != "print('v1')" {
		t.Fatalf("created script file = %q err=%v", body, err)
	}

	updateBody, _ := json.Marshal(domain.CustomProvider{
		Name: created.Name, Protocol: created.Protocol, AuthHeader: created.AuthHeader,
		Script: domain.ProviderScript{Language: "python", Entrypoint: "adapter.py", Source: "print('v2')"},
	})
	updateRequest := withAdmin(httptest.NewRequest(http.MethodPatch, "/__custom-providers/"+created.ID, strings.NewReader(string(updateBody))))
	updateRequest.SetPathValue("id", created.ID)
	updateRecorder := httptest.NewRecorder()
	server.handleUpdateCustomProvider(updateRecorder, updateRequest)
	if updateRecorder.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", updateRecorder.Code, updateRecorder.Body.String())
	}
	if body, err := os.ReadFile(scriptPath); err != nil || string(body) != "print('v2')" {
		t.Fatalf("updated script file = %q err=%v", body, err)
	}

	deleteRequest := withAdmin(httptest.NewRequest(http.MethodDelete, "/__custom-providers/"+created.ID, nil))
	deleteRequest.SetPathValue("id", created.ID)
	deleteRecorder := httptest.NewRecorder()
	server.handleDeleteCustomProvider(deleteRecorder, deleteRequest)
	if deleteRecorder.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", deleteRecorder.Code, deleteRecorder.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, created.ID)); !os.IsNotExist(err) {
		t.Fatalf("script directory survived delete: %v", err)
	}
}

func TestCustomProviderAIPromptCoversCRUDAndABI(t *testing.T) {
	server := NewServer(NewRouter(domain.GatewayState{}), monitor.NewStore())
	request := httptest.NewRequest(http.MethodGet, "/__custom-providers/ai-prompt", nil)
	request = request.WithContext(context.WithValue(request.Context(), authContextKey{}, adminIdentity()))
	recorder := httptest.NewRecorder()
	server.handleCustomProviderAIPrompt(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Prompt string `json:"prompt"`
		ABI    string `json:"abi"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"POST /__custom-providers",
		"PATCH /__custom-providers/{id}",
		"DELETE /__custom-providers/{id}",
		"POST /__providers",
		"POST /__routes",
		"POST /__apikeys",
		"DELETE /__apikeys/{id}",
		"DELETE /__routes/{id}",
		"/v1/chat/completions",
		"/openai/v1/responses",
		"/anthropic/v1/messages",
		"至少一种",
		"PROVIDER_SCRIPT_MAX_CONCURRENCY",
	} {
		if !strings.Contains(payload.Prompt, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
	if payload.ABI == "" || !strings.Contains(payload.ABI, "UPSTREAM_API_KEY") {
		t.Fatalf("ABI missing: %q", payload.ABI)
	}
}
