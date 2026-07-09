package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseTunnelCreateOutput(t *testing.T) {
	output := "Tunnel credentials written to /Users/me/.cloudflared/e40aea68-e223-4d75-964e-3647de59d6d2.json\nCreated tunnel llm-protocol-gateway with id e40aea68-e223-4d75-964e-3647de59d6d2\n"
	id, cred, err := parseTunnelCreateOutput(output)
	if err != nil {
		t.Fatal(err)
	}
	if id != "e40aea68-e223-4d75-964e-3647de59d6d2" {
		t.Fatalf("id=%q", id)
	}
	if cred != "/Users/me/.cloudflared/e40aea68-e223-4d75-964e-3647de59d6d2.json" {
		t.Fatalf("cred=%q", cred)
	}
}

func TestFindTunnelByNameFromList(t *testing.T) {
	tmp := t.TempDir()
	credPath := filepath.Join(tmp, "e40aea68-e223-4d75-964e-3647de59d6d2.json")
	if err := os.WriteFile(credPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	setup := NewCloudflareSetup()
	setup.homeDir = func() (string, error) { return tmp, nil }
	setup.run = func(ctx context.Context, args ...string) (string, error) {
		return "ID                                   NAME    CREATED              CONNECTIONS\n" +
			"e40aea68-e223-4d75-964e-3647de59d6d2 llm-protocol-gateway 2026-07-04T00:11:38Z\n", nil
	}
	id, cred, err := setup.findTunnelByName(ctxBackground(), "llm-protocol-gateway")
	if err != nil {
		t.Fatal(err)
	}
	if id != "e40aea68-e223-4d75-964e-3647de59d6d2" {
		t.Fatalf("id=%q", id)
	}
	if cred != credPath {
		t.Fatalf("cred=%q", cred)
	}
}

func TestEnsureTunnelRecreatesWhenCredentialsMissing(t *testing.T) {
	tmp := t.TempDir()
	newCred := filepath.Join(tmp, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.json")
	var calls []string
	setup := NewCloudflareSetup()
	setup.homeDir = func() (string, error) { return tmp, nil }
	setup.run = func(ctx context.Context, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		calls = append(calls, joined)
		switch {
		case joined == "tunnel create llm-protocol-gateway" && len(calls) == 1:
			return "", fmt.Errorf("tunnel with name already exists")
		case joined == "tunnel list":
			return "ID                                   NAME                 CREATED\n" +
				"ff2f197e-d383-40f7-9fc8-50772d7e9c26 llm-protocol-gateway 2026-07-08T08:55:14Z\n", nil
		case joined == "tunnel delete -f ff2f197e-d383-40f7-9fc8-50772d7e9c26":
			return "deleted", nil
		case joined == "tunnel create llm-protocol-gateway":
			return "Tunnel credentials written to " + newCred + "\nCreated tunnel llm-protocol-gateway with id aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee\n", nil
		default:
			return "", fmt.Errorf("unexpected args: %v", args)
		}
	}
	id, cred, err := setup.ensureTunnel(ctxBackground(), "llm-protocol-gateway")
	if err != nil {
		t.Fatal(err)
	}
	if id != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Fatalf("id=%q", id)
	}
	if cred != newCred {
		t.Fatalf("cred=%q", cred)
	}
	if len(calls) != 4 {
		t.Fatalf("calls=%v", calls)
	}
}

func ctxBackground() context.Context {
	return context.Background()
}

func TestDNSRecordConflict(t *testing.T) {
	msg := strings.ToLower("Failed to add route: code: 1003, reason: Failed to create record gateway.lucadesign.uk with err An A, AAAA, or CNAME record with that host already exists.")
	if !dnsRecordConflict(msg) {
		t.Fatal("expected conflict detection")
	}
	if dnsRecordConflict("permission denied") {
		t.Fatal("should not treat unrelated errors as conflict")
	}
}

func TestRouteDNSOverwritesExisting(t *testing.T) {
	tmp := t.TempDir()
	cert := []byte(`-----BEGIN ARGO TUNNEL TOKEN-----
eyJ6b25lSUQiOiJ6b25lLTEiLCJhY2NvdW50SUQiOiJhY2MtMSIsImFwaVRva2Vu
IjoiY2Z1dF90ZXN0In0=
-----END ARGO TUNNEL TOKEN-----
`)
	if err := os.WriteFile(filepath.Join(tmp, "cert.pem"), cert, 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	putCalls := 0
	wantID := "ff2f197e-d383-40f7-9fc8-50772d7e9c26"
	setup := NewCloudflareSetup()
	setup.homeDir = func() (string, error) { return tmp, nil }
	setup.run = func(ctx context.Context, args ...string) (string, error) {
		calls++
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "--overwrite-dns") {
			return "added route", nil
		}
		return "Failed to add route: code: 1003, reason: An A, AAAA, or CNAME record with that host already exists.", fmt.Errorf("exit status 1")
	}
	setup.httpDo = func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/dns_records"):
			return jsonHTTPResponse(200, map[string]any{
				"success": true,
				"result": []map[string]any{
					{
						"id":      "rec-1",
						"type":    "CNAME",
						"name":    "gateway.lucadesign.uk",
						"content": "old-tunnel.cfargotunnel.com",
						"proxied": true,
					},
				},
			}), nil
		case req.Method == http.MethodPut:
			putCalls++
			var body map[string]any
			raw, _ := io.ReadAll(req.Body)
			_ = json.Unmarshal(raw, &body)
			if body["content"] != wantID+".cfargotunnel.com" {
				t.Fatalf("put content=%v", body["content"])
			}
			return jsonHTTPResponse(200, map[string]any{"success": true, "result": body}), nil
		default:
			return jsonHTTPResponse(404, map[string]any{"success": false}), nil
		}
	}
	if err := setup.routeDNS(ctxBackground(), "llm-protocol-gateway", wantID, "gateway.lucadesign.uk"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d want 2 (create then overwrite)", calls)
	}
	if putCalls != 1 {
		t.Fatalf("putCalls=%d want 1", putCalls)
	}
}

func TestRouteDNSFixesStaleAlreadyConfigured(t *testing.T) {
	tmp := t.TempDir()
	cert := []byte(`-----BEGIN ARGO TUNNEL TOKEN-----
eyJ6b25lSUQiOiJ6b25lLTEiLCJhY2NvdW50SUQiOiJhY2MtMSIsImFwaVRva2Vu
IjoiY2Z1dF90ZXN0In0=
-----END ARGO TUNNEL TOKEN-----
`)
	if err := os.WriteFile(filepath.Join(tmp, "cert.pem"), cert, 0o600); err != nil {
		t.Fatal(err)
	}
	staleID := "72fe97d4-d5da-4de4-9bcb-603cf8aeb2ae"
	wantID := "ff2f197e-d383-40f7-9fc8-50772d7e9c26"
	cliCalls := 0
	putCalls := 0
	setup := NewCloudflareSetup()
	setup.homeDir = func() (string, error) { return tmp, nil }
	setup.run = func(ctx context.Context, args ...string) (string, error) {
		cliCalls++
		// cloudflared exits 0 even when pointing at a deleted tunnel.
		return fmt.Sprintf("gateway.lucadesign.uk is already configured to route to your tunnel tunnelID=%s", staleID), nil
	}
	setup.httpDo = func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/dns_records"):
			return jsonHTTPResponse(200, map[string]any{
				"success": true,
				"result": []map[string]any{
					{
						"id":      "rec-1",
						"type":    "CNAME",
						"name":    "gateway.lucadesign.uk",
						"content": staleID + ".cfargotunnel.com",
						"proxied": true,
					},
				},
			}), nil
		case req.Method == http.MethodPut:
			putCalls++
			var body map[string]any
			raw, _ := io.ReadAll(req.Body)
			_ = json.Unmarshal(raw, &body)
			if body["content"] != wantID+".cfargotunnel.com" {
				t.Fatalf("put content=%v", body["content"])
			}
			return jsonHTTPResponse(200, map[string]any{"success": true, "result": body}), nil
		default:
			return jsonHTTPResponse(404, map[string]any{"success": false}), nil
		}
	}
	if err := setup.routeDNS(ctxBackground(), "llm-protocol-gateway", wantID, "gateway.lucadesign.uk"); err != nil {
		t.Fatal(err)
	}
	if cliCalls < 2 {
		t.Fatalf("cliCalls=%d want >=2 (route + overwrite after stale id)", cliCalls)
	}
	if putCalls != 1 {
		t.Fatalf("putCalls=%d want 1", putCalls)
	}
	id, ok := parseAlreadyRoutedTunnelID("INF gateway.lucadesign.uk is already configured to route to your tunnel tunnelID=" + staleID)
	if !ok || id != staleID {
		t.Fatalf("parseAlreadyRoutedTunnelID=%q ok=%v", id, ok)
	}
}

func TestParseOriginCertToken(t *testing.T) {
	raw := []byte(`-----BEGIN ARGO TUNNEL TOKEN-----
eyJ6b25lSUQiOiJ6b25lLTEiLCJhY2NvdW50SUQiOiJhY2MtMSIsImFwaVRva2Vu
IjoiY2Z1dF90ZXN0In0=
-----END ARGO TUNNEL TOKEN-----
`)
	token, err := parseOriginCertToken(raw)
	if err != nil {
		t.Fatal(err)
	}
	if token.ZoneID != "zone-1" || token.AccountID != "acc-1" || token.APIToken != "cfut_test" {
		t.Fatalf("token=%+v", token)
	}
}

func TestListZonesUsesOriginCert(t *testing.T) {
	tmp := t.TempDir()
	cert := []byte(`-----BEGIN ARGO TUNNEL TOKEN-----
eyJ6b25lSUQiOiJ6b25lLTEiLCJhY2NvdW50SUQiOiJhY2MtMSIsImFwaVRva2Vu
IjoiY2Z1dF90ZXN0In0=
-----END ARGO TUNNEL TOKEN-----
`)
	if err := os.WriteFile(filepath.Join(tmp, "cert.pem"), cert, 0o600); err != nil {
		t.Fatal(err)
	}
	setup := NewCloudflareSetup()
	setup.homeDir = func() (string, error) { return tmp, nil }
	setup.httpDo = func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/zones/zone-1"):
			return jsonHTTPResponse(200, map[string]any{
				"success": true,
				"result":  map[string]any{"id": "zone-1", "name": "example.com"},
			}), nil
		case strings.HasSuffix(req.URL.Path, "/zones"):
			return jsonHTTPResponse(200, map[string]any{
				"success": true,
				"result": []map[string]any{
					{"id": "zone-2", "name": "other.dev"},
					{"id": "zone-1", "name": "example.com"},
				},
			}), nil
		default:
			return jsonHTTPResponse(404, map[string]any{"success": false}), nil
		}
	}
	zones, err := setup.ListZones(ctxBackground())
	if err != nil {
		t.Fatal(err)
	}
	if len(zones) != 2 {
		t.Fatalf("zones=%v", zones)
	}
	if zones[0].Name != "example.com" || zones[1].Name != "other.dev" {
		t.Fatalf("sorted zones=%v", zones)
	}
}

func TestListZonesRequiresAuthorization(t *testing.T) {
	tmp := t.TempDir()
	setup := NewCloudflareSetup()
	setup.homeDir = func() (string, error) { return tmp, nil }
	if _, err := setup.ListZones(ctxBackground()); err == nil {
		t.Fatal("expected unauthorized error")
	}
}

func TestProvisionWritesSplitHostIngress(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	app := filepath.Join(tmp, "app")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	cert := []byte(`-----BEGIN ARGO TUNNEL TOKEN-----
eyJ6b25lSUQiOiJ6b25lLTEiLCJhY2NvdW50SUQiOiJhY2MtMSIsImFwaVRva2Vu
IjoiY2Z1dF90ZXN0In0=
-----END ARGO TUNNEL TOKEN-----
`)
	if err := os.WriteFile(filepath.Join(home, "cert.pem"), cert, 0o600); err != nil {
		t.Fatal(err)
	}
	cred := filepath.Join(home, "e40aea68-e223-4d75-964e-3647de59d6d2.json")
	if err := os.WriteFile(cred, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	var dnsHosts []string
	setup := NewCloudflareSetup()
	setup.homeDir = func() (string, error) { return home, nil }
	setup.appDir = func() (string, error) { return app, nil }
	setup.run = func(ctx context.Context, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(joined, "tunnel create"):
			return "Tunnel credentials written to " + cred + "\nCreated tunnel llm-protocol-gateway with id e40aea68-e223-4d75-964e-3647de59d6d2\n", nil
		case strings.Contains(joined, "tunnel route dns"):
			dnsHosts = append(dnsHosts, args[len(args)-1])
			return "added", nil
		default:
			return "", fmt.Errorf("unexpected args: %v", args)
		}
	}
	setup.httpDo = func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/dns_records"):
			return jsonHTTPResponse(200, map[string]any{"success": true, "result": []any{}}), nil
		case req.Method == http.MethodPost && strings.Contains(req.URL.Path, "/dns_records"):
			return jsonHTTPResponse(200, map[string]any{"success": true, "result": map[string]any{}}), nil
		case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/zones"):
			return jsonHTTPResponse(200, map[string]any{
				"success": true,
				"result":  []map[string]any{{"id": "zone-1", "name": "lucadesign.uk"}},
			}), nil
		default:
			return jsonHTTPResponse(404, map[string]any{"success": false}), nil
		}
	}

	result, err := setup.Provision(ctxBackground(), "gateway.lucadesign.uk", "console.lucadesign.uk", "llm-protocol-gateway", 18093)
	if err != nil {
		t.Fatal(err)
	}
	if result.CustomDomain != "gateway.lucadesign.uk" || result.UIDomain != "console.lucadesign.uk" {
		t.Fatalf("result=%+v", result)
	}
	if len(dnsHosts) != 2 || dnsHosts[0] != "gateway.lucadesign.uk" || dnsHosts[1] != "console.lucadesign.uk" {
		t.Fatalf("dnsHosts=%v", dnsHosts)
	}
	raw, err := os.ReadFile(result.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	config := string(raw)
	if !strings.Contains(config, "hostname: gateway.lucadesign.uk") || !strings.Contains(config, "hostname: console.lucadesign.uk") {
		t.Fatalf("config missing split hostnames:\n%s", config)
	}
}

func TestProvisionRejectsSameHost(t *testing.T) {
	setup := NewCloudflareSetup()
	setup.run = func(ctx context.Context, args ...string) (string, error) {
		t.Fatalf("should not run cloudflared: %v", args)
		return "", nil
	}
	if _, err := setup.Provision(ctxBackground(), "gateway.lucadesign.uk", "gateway.lucadesign.uk", "llm-protocol-gateway", 18093); err == nil {
		t.Fatal("expected same-host rejection")
	}
}

func TestProvisionAllowsUIOnly(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	app := filepath.Join(tmp, "app")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	cert := []byte(`-----BEGIN ARGO TUNNEL TOKEN-----
eyJ6b25lSUQiOiJ6b25lLTEiLCJhY2NvdW50SUQiOiJhY2MtMSIsImFwaVRva2Vu
IjoiY2Z1dF90ZXN0In0=
-----END ARGO TUNNEL TOKEN-----
`)
	if err := os.WriteFile(filepath.Join(home, "cert.pem"), cert, 0o600); err != nil {
		t.Fatal(err)
	}
	cred := filepath.Join(home, "e40aea68-e223-4d75-964e-3647de59d6d2.json")
	if err := os.WriteFile(cred, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	setup := NewCloudflareSetup()
	setup.homeDir = func() (string, error) { return home, nil }
	setup.appDir = func() (string, error) { return app, nil }
	setup.run = func(ctx context.Context, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(joined, "tunnel create"):
			return "Tunnel credentials written to " + cred + "\nCreated tunnel llm-protocol-gateway with id e40aea68-e223-4d75-964e-3647de59d6d2\n", nil
		case strings.Contains(joined, "tunnel route dns"):
			return "added", nil
		default:
			return "", fmt.Errorf("unexpected args: %v", args)
		}
	}
	setup.httpDo = func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/dns_records") {
			return jsonHTTPResponse(200, map[string]any{"success": true, "result": []any{}}), nil
		}
		if req.Method == http.MethodPost && strings.Contains(req.URL.Path, "/dns_records") {
			return jsonHTTPResponse(200, map[string]any{"success": true, "result": map[string]any{}}), nil
		}
		return jsonHTTPResponse(404, map[string]any{"success": false}), nil
	}
	result, err := setup.Provision(ctxBackground(), "", "console.lucadesign.uk", "llm-protocol-gateway", 18093)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(result.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	config := string(raw)
	if strings.Contains(config, "hostname: gateway.") {
		t.Fatalf("api hostname should be omitted:\n%s", config)
	}
	if !strings.Contains(config, "hostname: console.lucadesign.uk") {
		t.Fatalf("missing ui hostname:\n%s", config)
	}
}

func jsonHTTPResponse(status int, body any) *http.Response {
	payload, _ := json.Marshal(body)
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(string(payload))),
		Header:     make(http.Header),
	}
}
