package gatewayctl

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func runCLI(t *testing.T, args []string, stdin string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Run(args, strings.NewReader(stdin), &out, &errOut, "test")
	return code, out.String(), errOut.String()
}

func TestUpstreamAddAndReconnectUseJSONAPI(t *testing.T) {
	var addBody map[string]any
	var reconnectBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/__peer-upstreams":
			_ = json.NewDecoder(r.Body).Decode(&addBody)
			_, _ = io.WriteString(w, `{"username":"peer-a","providers":[{"id":"up-1"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/__peer-upstreams":
			_, _ = io.WriteString(w, `{"gatewayId":"gw-self","upstreams":[{"providerId":"up-1","name":"vps1","parentUrl":"https://parent.example","loginUsername":"peer-a","credentialsStored":true}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/__peer-upstreams/up-1/reconnect":
			_ = json.NewDecoder(r.Body).Decode(&reconnectBody)
			_, _ = io.WriteString(w, `{"username":"peer-a","upstream":{"providerId":"up-1"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	configPath := filepath.Join(t.TempDir(), "gatewayctl.json")

	code, out, errOut := runCLI(t, []string{
		"--url", server.URL, "--config", configPath,
		"upstream", "add", "--url", "https://parent.example", "--username", "peer-a", "--password-stdin", "--name", "vps1", "--provider", "p1,p2",
	}, "secret-password\n")
	if code != 0 {
		t.Fatalf("add exit=%d stderr=%s", code, errOut)
	}
	if !json.Valid([]byte(out)) {
		t.Fatalf("add output is not json: %s", out)
	}
	if addBody["username"] != "peer-a" || addBody["password"] != "secret-password" || addBody["parentUrl"] != "https://parent.example" {
		t.Fatalf("add body = %#v", addBody)
	}
	requested, _ := addBody["requestedProviderIds"].([]any)
	if len(requested) != 2 || requested[0] != "p1" || requested[1] != "p2" {
		t.Fatalf("requested providers = %#v", addBody["requestedProviderIds"])
	}

	code, out, errOut = runCLI(t, []string{
		"--url", server.URL, "--config", configPath,
		"upstream", "reconnect", "--id", "up-1",
	}, "")
	if code != 0 {
		t.Fatalf("reconnect exit=%d stderr=%s", code, errOut)
	}
	if !json.Valid([]byte(out)) {
		t.Fatalf("reconnect output is not json: %s", out)
	}
	if len(reconnectBody) != 0 {
		t.Fatalf("reconnect without overrides must let the server auto-fill stored credentials, body=%#v", reconnectBody)
	}
}

func TestDownstreamApproveAndTunnelURL(t *testing.T) {
	var approveBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/__peers":
			_, _ = io.WriteString(w, `{"peers":[{"userId":"peer-user","username":"peer-a","gatewayId":"gw-child","enabled":false,"keyEnabled":false,"grantedProviderIds":[]}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/__peers/peer-user/approve":
			_ = json.NewDecoder(r.Body).Decode(&approveBody)
			_, _ = io.WriteString(w, `{"approved":true,"enabled":true,"credentialReady":true}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/__settings/peer-approval":
			_, _ = io.WriteString(w, `{"required":true}`)
		case r.Method == http.MethodGet && r.URL.Path == "/__public":
			_, _ = io.WriteString(w, `{"publicBaseUrl":"https://old.example","uiPublicBaseUrl":"https://ui-old.example","tunnel":{"publicUrl":"https://api.trycloudflare.com","uiPublicUrl":"https://ui.trycloudflare.com"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	configPath := filepath.Join(t.TempDir(), "gatewayctl.json")

	code, out, errOut := runCLI(t, []string{
		"--url", server.URL, "--config", configPath,
		"downstream", "approve", "--id", "peer-user", "--all",
	}, "")
	if code != 0 {
		t.Fatalf("approve exit=%d stderr=%s", code, errOut)
	}
	if !strings.Contains(out, `"approved": true`) || approveBody["grantMode"] != "all" {
		t.Fatalf("approve output=%s body=%#v", out, approveBody)
	}

	code, out, errOut = runCLI(t, []string{
		"--url", server.URL, "--config", configPath,
		"downstream", "policy", "--require-approval",
	}, "")
	if code != 0 || !strings.Contains(out, `"required": true`) {
		t.Fatalf("policy exit=%d output=%s stderr=%s", code, out, errOut)
	}

	code, out, errOut = runCLI(t, []string{
		"--url", server.URL, "--config", configPath,
		"tunnel", "url", "--raw",
	}, "")
	if code != 0 {
		t.Fatalf("tunnel url exit=%d stderr=%s", code, errOut)
	}
	if strings.TrimSpace(out) != "https://api.trycloudflare.com" {
		t.Fatalf("tunnel raw URL = %q", out)
	}
}

func TestLoginPersistsSessionCookie(t *testing.T) {
	const cookie = "gateway_admin_session=test-session"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/__auth/login":
			http.SetCookie(w, &http.Cookie{Name: "gateway_admin_session", Value: "test-session", Path: "/", HttpOnly: true})
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"authenticated":true}`)
		case "/__state":
			if requestCookie, err := r.Cookie("gateway_admin_session"); err != nil || requestCookie.Value != "test-session" {
				http.Error(w, `{"error":{"message":"authentication required"}}`, http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"providers":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	configPath := filepath.Join(t.TempDir(), "gatewayctl.json")

	code, _, errOut := runCLI(t, []string{
		"--url", server.URL, "--config", configPath,
		"login", "--username", "admin", "--password-stdin",
	}, "admin-password\n")
	if code != 0 {
		t.Fatalf("login exit=%d stderr=%s", code, errOut)
	}
	code, out, errOut := runCLI(t, []string{
		"--config", configPath, "provider", "list",
	}, "")
	if code != 0 {
		t.Fatalf("provider list exit=%d stderr=%s", code, errOut)
	}
	if !strings.Contains(out, `"providers": []`) {
		t.Fatalf("provider list output=%s", out)
	}
	cfg, err := loadStoredConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != server.URL || len(cfg.Cookies) == 0 || cfg.Cookies[0].Value != "test-session" {
		t.Fatalf("saved config = %+v (want cookie %s)", cfg, cookie)
	}
}

func TestExtractPublicURLsPrefersLiveTunnel(t *testing.T) {
	api, ui := extractPublicURLs(map[string]any{
		"publicBaseUrl":   "https://api.example.com",
		"uiPublicBaseUrl": "https://ui.example.com",
		"tunnel": map[string]any{
			"publicUrl":   "https://live-api.trycloudflare.com",
			"uiPublicUrl": "https://live-ui.trycloudflare.com",
		},
	})
	if api != "https://live-api.trycloudflare.com" || ui != "https://live-ui.trycloudflare.com" {
		t.Fatalf("URLs = %q / %q", api, ui)
	}
}

func TestAPIAcceptsDataFlagAfterPath(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/__settings/peer-max-hops" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"hops":4}`)
	}))
	defer server.Close()
	code, out, errOut := runCLI(t, []string{
		"--url", server.URL, "--config", filepath.Join(t.TempDir(), "gatewayctl.json"),
		"api", "PATCH", "/__settings/peer-max-hops", "--data", `{"hops":4}`,
	}, "")
	if code != 0 {
		t.Fatalf("api exit=%d stderr=%s", code, errOut)
	}
	if got["hops"] != float64(4) || !strings.Contains(out, `"hops": 4`) {
		t.Fatalf("body=%#v output=%s", got, out)
	}
}
