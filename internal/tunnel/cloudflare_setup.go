package tunnel

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/packaged"
)

const cloudflareLoginURL = "https://dash.cloudflare.com/argotunnel"

// CloudflareLoginURL is the official Cloudflare tunnel authorization page.
func CloudflareLoginURL() string {
	return cloudflareLoginURL
}

var (
	tunnelCreateCredPattern = regexp.MustCompile(`(?i)credentials written to (.+\.json)`)
	tunnelCreateIDPattern   = regexp.MustCompile(`(?i)with id ([0-9a-f-]{36})`)
	tunnelListRowPattern    = regexp.MustCompile(`^([0-9a-f-]{36})\s+(\S+)`)
	// cloudflared exits 0 with this even when the CNAME points at a different
	// (often deleted) tunnel UUID — so we must not trust it alone.
	alreadyRoutedTunnelPattern = regexp.MustCompile(`(?i)already configured to route to your tunnel\s+tunnelID=([0-9a-f-]{36})`)
)

// ProvisionResult holds the artifacts produced by a one-time Cloudflare setup.
type ProvisionResult struct {
	TunnelName      string `json:"tunnelName"`
	TunnelID        string `json:"tunnelId"`
	CredentialsFile string `json:"credentialsFile"`
	ConfigFile      string `json:"configFile"`
	// CustomDomain is the model-API hostname.
	CustomDomain string `json:"customDomain"`
	// UIDomain is the management-UI hostname (must differ from CustomDomain).
	UIDomain string `json:"uiDomain"`
}

// CloudflareZone is a DNS zone the origin cert can manage.
type CloudflareZone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type originCertToken struct {
	ZoneID    string `json:"zoneID"`
	AccountID string `json:"accountID"`
	APIToken  string `json:"apiToken"`
}

// CloudflareSetup automates cloudflared login and named-tunnel provisioning.
type CloudflareSetup struct {
	mu sync.Mutex

	lookPath func(string) (string, error)
	run      func(ctx context.Context, args ...string) (string, error)
	homeDir  func() (string, error)
	appDir   func() (string, error)
	httpDo   func(req *http.Request) (*http.Response, error)

	loginRunning bool
}

func NewCloudflareSetup() *CloudflareSetup {
	return &CloudflareSetup{
		lookPath: lookPathCloudflared,
		run:      defaultCloudflaredOutput,
		homeDir:  defaultCloudflaredHome,
		appDir:   defaultCloudflareAppDir,
		httpDo:   http.DefaultClient.Do,
	}
}

func defaultCloudflaredHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cloudflared"), nil
}

// CloudflareAppDirPath returns the gateway-owned Cloudflare config directory
// under the user Application Support folder (outside the .app bundle).
// It does not create the directory.
func CloudflareAppDirPath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "llm-protocol-gateway", "cloudflare"), nil
}

func defaultCloudflareAppDir() (string, error) {
	dir, err := CloudflareAppDirPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// CloudflaredHomeDir returns ~/.cloudflared (origin cert / credentials).
func CloudflaredHomeDir() (string, error) {
	return defaultCloudflaredHome()
}

// CloudflareAppDir returns the gateway-owned Cloudflare config directory,
// creating it if needed.
func CloudflareAppDir() (string, error) {
	return defaultCloudflareAppDir()
}

func defaultCloudflaredOutput(ctx context.Context, args ...string) (string, error) {
	bin, err := packaged.Cloudflared()
	if err != nil {
		return "", fmt.Errorf("cloudflared not found: install it (brew install cloudflared) or use a packaged App: %w", err)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err = cmd.Run()
	output := buf.String()
	if err != nil {
		msg := strings.TrimSpace(output)
		if msg == "" {
			msg = err.Error()
		}
		return output, fmt.Errorf("%s", msg)
	}
	return output, nil
}

func (c *CloudflareSetup) originCertPath() (string, error) {
	home, err := c.homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "cert.pem"), nil
}

// IsAuthorized reports whether cloudflared origin cert exists locally.
func (c *CloudflareSetup) IsAuthorized() bool {
	path, err := c.originCertPath()
	if err != nil {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

// ListZones returns Cloudflare zones available via the local origin cert.
// Prefer the zone selected during `cloudflared tunnel login`, then any other
// zones the cert token can list.
func (c *CloudflareSetup) ListZones(ctx context.Context) ([]CloudflareZone, error) {
	if !c.IsAuthorized() {
		return nil, fmt.Errorf("cloudflare is not authorized yet; complete browser login first")
	}
	path, err := c.originCertPath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	token, err := parseOriginCertToken(raw)
	if err != nil {
		return nil, err
	}
	return c.listZonesWithToken(ctx, token)
}

func parseOriginCertToken(raw []byte) (originCertToken, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return originCertToken{}, fmt.Errorf("invalid origin cert: missing PEM block")
	}
	if block.Type != "ARGO TUNNEL TOKEN" {
		return originCertToken{}, fmt.Errorf("invalid origin cert: unexpected PEM type %q", block.Type)
	}
	var token originCertToken
	if err := json.Unmarshal(block.Bytes, &token); err != nil {
		return originCertToken{}, fmt.Errorf("invalid origin cert token: %w", err)
	}
	if strings.TrimSpace(token.APIToken) == "" {
		return originCertToken{}, fmt.Errorf("invalid origin cert: missing apiToken")
	}
	return token, nil
}

func (c *CloudflareSetup) listZonesWithToken(ctx context.Context, token originCertToken) ([]CloudflareZone, error) {
	do := c.httpDo
	if do == nil {
		do = http.DefaultClient.Do
	}
	byID := map[string]CloudflareZone{}

	// The zone chosen on the Cloudflare authorization page is authoritative.
	if zoneID := strings.TrimSpace(token.ZoneID); zoneID != "" {
		zone, err := c.fetchZoneByID(ctx, do, token.APIToken, zoneID)
		if err == nil && zone.Name != "" {
			byID[zone.ID] = zone
		}
	}

	listed, err := c.fetchZones(ctx, do, token.APIToken)
	if err != nil && len(byID) == 0 {
		return nil, err
	}
	for _, zone := range listed {
		if zone.ID == "" || zone.Name == "" {
			continue
		}
		byID[zone.ID] = zone
	}
	if len(byID) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("no Cloudflare zones available for this authorization")
	}

	out := make([]CloudflareZone, 0, len(byID))
	for _, zone := range byID {
		out = append(out, zone)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (c *CloudflareSetup) fetchZoneByID(ctx context.Context, do func(*http.Request) (*http.Response, error), apiToken, zoneID string) (CloudflareZone, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.cloudflare.com/client/v4/zones/"+zoneID, nil)
	if err != nil {
		return CloudflareZone{}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := do(req)
	if err != nil {
		return CloudflareZone{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return CloudflareZone{}, err
	}
	var parsed struct {
		Success bool `json:"success"`
		Result  struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"result"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return CloudflareZone{}, fmt.Errorf("decode zone %s: %w", zoneID, err)
	}
	if resp.StatusCode >= 300 || !parsed.Success {
		msg := strings.TrimSpace(string(body))
		if len(parsed.Errors) > 0 && parsed.Errors[0].Message != "" {
			msg = parsed.Errors[0].Message
		}
		return CloudflareZone{}, fmt.Errorf("fetch zone %s: %s", zoneID, msg)
	}
	return CloudflareZone{ID: parsed.Result.ID, Name: parsed.Result.Name}, nil
}

func (c *CloudflareSetup) fetchZones(ctx context.Context, do func(*http.Request) (*http.Response, error), apiToken string) ([]CloudflareZone, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.cloudflare.com/client/v4/zones?per_page=50", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Success bool `json:"success"`
		Result  []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"result"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode zones: %w", err)
	}
	if resp.StatusCode >= 300 || !parsed.Success {
		msg := strings.TrimSpace(string(body))
		if len(parsed.Errors) > 0 && parsed.Errors[0].Message != "" {
			msg = parsed.Errors[0].Message
		}
		return nil, fmt.Errorf("list zones: %s", msg)
	}
	out := make([]CloudflareZone, 0, len(parsed.Result))
	for _, item := range parsed.Result {
		name := strings.TrimSpace(item.Name)
		id := strings.TrimSpace(item.ID)
		if name == "" || id == "" {
			continue
		}
		out = append(out, CloudflareZone{ID: id, Name: name})
	}
	return out, nil
}

// StartLogin launches `cloudflared tunnel login`, which opens the Cloudflare
// authorization page in the user's browser. The returned URL is a fallback when
// the browser does not open automatically.
func (c *CloudflareSetup) StartLogin(ctx context.Context) (string, error) {
	if c.IsAuthorized() {
		return cloudflareLoginURL, nil
	}
	c.mu.Lock()
	if c.loginRunning {
		c.mu.Unlock()
		return cloudflareLoginURL, nil
	}
	c.loginRunning = true
	c.mu.Unlock()

	go func() {
		defer func() {
			c.mu.Lock()
			c.loginRunning = false
			c.mu.Unlock()
		}()
		loginCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		_, _ = c.run(loginCtx, "tunnel", "login")
	}()

	return cloudflareLoginURL, nil
}

// Provision creates or reuses a named tunnel, routes DNS for the requested
// hostnames, and writes a config file that points those ingress hostnames at
// the local gateway port. Either API domain, UI domain, or both may be set.
func (c *CloudflareSetup) Provision(ctx context.Context, customDomain, uiDomain, tunnelName string, localPort int) (ProvisionResult, error) {
	customDomain = strings.TrimSpace(customDomain)
	uiDomain = strings.TrimSpace(uiDomain)
	if customDomain == "" && uiDomain == "" {
		return ProvisionResult{}, fmt.Errorf("at least one of api domain or ui domain is required")
	}
	if customDomain != "" && uiDomain != "" && strings.EqualFold(customDomain, uiDomain) {
		return ProvisionResult{}, fmt.Errorf("api domain and ui domain must be different (got %s for both)", customDomain)
	}
	if !c.IsAuthorized() {
		return ProvisionResult{}, fmt.Errorf("cloudflare is not authorized yet; complete browser login first")
	}
	if strings.TrimSpace(tunnelName) == "" {
		tunnelName = "llm-protocol-gateway"
	}
	if localPort <= 0 {
		localPort = 18093
	}

	tunnelID, credentialsFile, err := c.ensureTunnel(ctx, tunnelName)
	if err != nil {
		return ProvisionResult{}, err
	}
	if customDomain != "" {
		if err := c.routeDNS(ctx, tunnelName, tunnelID, customDomain); err != nil {
			return ProvisionResult{}, err
		}
	}
	if uiDomain != "" {
		if err := c.routeDNS(ctx, tunnelName, tunnelID, uiDomain); err != nil {
			return ProvisionResult{}, err
		}
	}

	originCert, err := c.originCertPath()
	if err != nil {
		return ProvisionResult{}, err
	}
	appDir, err := c.appDir()
	if err != nil {
		return ProvisionResult{}, err
	}
	configFile := filepath.Join(appDir, "tunnel-config.yml")
	var ingress strings.Builder
	if customDomain != "" {
		fmt.Fprintf(&ingress, "  - hostname: %s\n    service: http://127.0.0.1:%d\n", customDomain, localPort)
	}
	if uiDomain != "" {
		fmt.Fprintf(&ingress, "  - hostname: %s\n    service: http://127.0.0.1:%d\n", uiDomain, localPort)
	}
	config := fmt.Sprintf(`tunnel: %s
credentials-file: %s
origincert: %s
ingress:
%s  - service: http_status:404
`, tunnelID, credentialsFile, originCert, ingress.String())
	if err := os.WriteFile(configFile, []byte(config), 0o600); err != nil {
		return ProvisionResult{}, err
	}

	return ProvisionResult{
		TunnelName:      tunnelName,
		TunnelID:        tunnelID,
		CredentialsFile: credentialsFile,
		ConfigFile:      configFile,
		CustomDomain:    customDomain,
		UIDomain:        uiDomain,
	}, nil
}

// routeDNS creates a CNAME for hostname → tunnel. If the record already exists
// (Cloudflare API code 1003), retries with --overwrite-dns. Regardless of
// cloudflared's exit code, we then ensure the zone CNAME actually points at
// tunnelID — cloudflared often reports "already configured … tunnelID=<old>"
// with exit 0 (and even --overwrite-dns) when the hostname still targets a
// deleted/stale tunnel, which surfaces publicly as HTTP 530 / error 1033.
func (c *CloudflareSetup) routeDNS(ctx context.Context, tunnelName, tunnelID, hostname string) error {
	tunnelID = strings.TrimSpace(tunnelID)
	if tunnelID == "" {
		return fmt.Errorf("route dns for %s: missing tunnel id", hostname)
	}
	output, err := c.run(ctx, "tunnel", "route", "dns", tunnelName, hostname)
	if err != nil {
		combined := strings.ToLower(output + " " + err.Error())
		if !dnsRecordConflict(combined) {
			return fmt.Errorf("route dns for %s: %w", hostname, err)
		}
		// Hostname already has A/AAAA/CNAME — overwrite so it points at this tunnel.
		output, err = c.run(ctx, "tunnel", "route", "dns", "--overwrite-dns", tunnelName, hostname)
		if err != nil {
			return fmt.Errorf("route dns for %s (overwrite existing record): %w", hostname, err)
		}
	}
	if routedID, ok := parseAlreadyRoutedTunnelID(output); ok && !strings.EqualFold(routedID, tunnelID) {
		// Stale route reported; force CLI overwrite before API reconcile.
		if _, overwriteErr := c.run(ctx, "tunnel", "route", "dns", "--overwrite-dns", tunnelName, hostname); overwriteErr != nil {
			// Fall through to API ensure — CLI overwrite is best-effort here.
			_ = overwriteErr
		}
	}
	if err := c.ensureHostnameCNAME(ctx, hostname, tunnelID); err != nil {
		return fmt.Errorf("ensure dns for %s → %s: %w", hostname, tunnelID, err)
	}
	return nil
}

func parseAlreadyRoutedTunnelID(output string) (string, bool) {
	match := alreadyRoutedTunnelPattern.FindStringSubmatch(output)
	if len(match) < 2 {
		return "", false
	}
	return strings.TrimSpace(match[1]), true
}

func tunnelCNAMETarget(tunnelID string) string {
	return strings.TrimSpace(tunnelID) + ".cfargotunnel.com"
}

// ensureHostnameCNAME makes sure hostname is a proxied CNAME to the given
// tunnel UUID. This corrects the cloudflared "already configured" false success.
func (c *CloudflareSetup) ensureHostnameCNAME(ctx context.Context, hostname, tunnelID string) error {
	hostname = strings.TrimSpace(hostname)
	tunnelID = strings.TrimSpace(tunnelID)
	if hostname == "" || tunnelID == "" {
		return fmt.Errorf("hostname and tunnel id are required")
	}
	path, err := c.originCertPath()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	token, err := parseOriginCertToken(raw)
	if err != nil {
		return err
	}
	zoneID := strings.TrimSpace(token.ZoneID)
	if zoneID == "" {
		return fmt.Errorf("origin cert missing zoneID")
	}
	do := c.httpDo
	if do == nil {
		do = http.DefaultClient.Do
	}
	target := tunnelCNAMETarget(tunnelID)
	records, err := c.listDNSRecordsByName(ctx, do, token.APIToken, zoneID, hostname)
	if err != nil {
		return err
	}
	if len(records) == 0 {
		return c.createDNSRecord(ctx, do, token.APIToken, zoneID, map[string]any{
			"type":    "CNAME",
			"name":    hostname,
			"content": target,
			"proxied": true,
			"ttl":     1,
		})
	}
	for _, rec := range records {
		if strings.EqualFold(rec.Type, "CNAME") &&
			strings.EqualFold(rec.Content, target) &&
			rec.Proxied {
			continue
		}
		if err := c.updateDNSRecord(ctx, do, token.APIToken, zoneID, rec.ID, map[string]any{
			"type":    "CNAME",
			"name":    hostname,
			"content": target,
			"proxied": true,
			"ttl":     1,
		}); err != nil {
			return err
		}
	}
	return nil
}

type cloudflareDNSRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
}

func (c *CloudflareSetup) listDNSRecordsByName(ctx context.Context, do func(*http.Request) (*http.Response, error), apiToken, zoneID, name string) ([]cloudflareDNSRecord, error) {
	url := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records?name=%s", zoneID, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Success bool                  `json:"success"`
		Result  []cloudflareDNSRecord `json:"result"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode dns records: %w", err)
	}
	if resp.StatusCode >= 300 || !parsed.Success {
		msg := strings.TrimSpace(string(body))
		if len(parsed.Errors) > 0 && parsed.Errors[0].Message != "" {
			msg = parsed.Errors[0].Message
		}
		return nil, fmt.Errorf("list dns records: %s", msg)
	}
	return parsed.Result, nil
}

func (c *CloudflareSetup) createDNSRecord(ctx context.Context, do func(*http.Request) (*http.Response, error), apiToken, zoneID string, payload map[string]any) error {
	return c.writeDNSRecord(ctx, do, http.MethodPost, fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records", zoneID), apiToken, payload)
}

func (c *CloudflareSetup) updateDNSRecord(ctx context.Context, do func(*http.Request) (*http.Response, error), apiToken, zoneID, recordID string, payload map[string]any) error {
	return c.writeDNSRecord(ctx, do, http.MethodPut, fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records/%s", zoneID, recordID), apiToken, payload)
}

func (c *CloudflareSetup) writeDNSRecord(ctx context.Context, do func(*http.Request) (*http.Response, error), method, url, apiToken string, payload map[string]any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var parsed struct {
		Success bool `json:"success"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return fmt.Errorf("decode dns write: %w", err)
	}
	if resp.StatusCode >= 300 || !parsed.Success {
		msg := strings.TrimSpace(string(body))
		if len(parsed.Errors) > 0 && parsed.Errors[0].Message != "" {
			msg = parsed.Errors[0].Message
		}
		return fmt.Errorf("dns write: %s", msg)
	}
	return nil
}

func dnsRecordConflict(combinedLower string) bool {
	return strings.Contains(combinedLower, "already exists") ||
		strings.Contains(combinedLower, "record with that host already exists") ||
		strings.Contains(combinedLower, "code: 1003") ||
		strings.Contains(combinedLower, "overwrite-dns") ||
		(strings.Contains(combinedLower, "cname") && strings.Contains(combinedLower, "exists"))
}

func (c *CloudflareSetup) ensureTunnel(ctx context.Context, name string) (string, string, error) {
	output, err := c.run(ctx, "tunnel", "create", name)
	if err == nil {
		return parseTunnelCreateOutput(output)
	}
	lower := strings.ToLower(output + " " + err.Error())
	if !strings.Contains(lower, "already exists") {
		return "", "", fmt.Errorf("create tunnel %q: %w", name, err)
	}
	tunnelID, credentialsFile, findErr := c.findTunnelByName(ctx, name)
	if findErr == nil {
		return tunnelID, credentialsFile, nil
	}
	// Local credentials were lost (common after reinstall / cleaned ~/.cloudflared).
	// Delete the orphaned remote tunnel and recreate so bind can proceed.
	if !isMissingTunnelCredentials(findErr) {
		return "", "", findErr
	}
	if tunnelID == "" {
		if id, idErr := c.lookupTunnelIDByName(ctx, name); idErr == nil {
			tunnelID = id
		}
	}
	deleteTarget := name
	if tunnelID != "" {
		deleteTarget = tunnelID
	}
	if _, delErr := c.run(ctx, "tunnel", "delete", "-f", deleteTarget); delErr != nil {
		return "", "", fmt.Errorf("tunnel %q credentials missing (%v); failed to delete orphaned tunnel for recreate: %w", name, findErr, delErr)
	}
	output, err = c.run(ctx, "tunnel", "create", name)
	if err != nil {
		return "", "", fmt.Errorf("recreate tunnel %q after missing credentials: %w", name, err)
	}
	return parseTunnelCreateOutput(output)
}

func isMissingTunnelCredentials(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "credentials file is missing")
}

func parseTunnelCreateOutput(output string) (string, string, error) {
	credMatch := tunnelCreateCredPattern.FindStringSubmatch(output)
	idMatch := tunnelCreateIDPattern.FindStringSubmatch(output)
	if len(credMatch) < 2 || len(idMatch) < 2 {
		return "", "", fmt.Errorf("unexpected cloudflared create output: %s", strings.TrimSpace(output))
	}
	return idMatch[1], strings.TrimSpace(credMatch[1]), nil
}

func (c *CloudflareSetup) findTunnelByName(ctx context.Context, name string) (string, string, error) {
	tunnelID, err := c.lookupTunnelIDByName(ctx, name)
	if err != nil {
		return "", "", err
	}
	home, err := c.homeDir()
	if err != nil {
		return "", "", err
	}
	cred := filepath.Join(home, tunnelID+".json")
	if _, err := os.Stat(cred); err != nil {
		return tunnelID, "", fmt.Errorf("tunnel %q exists but credentials file is missing: %s", name, cred)
	}
	return tunnelID, cred, nil
}

func (c *CloudflareSetup) lookupTunnelIDByName(ctx context.Context, name string) (string, error) {
	output, err := c.run(ctx, "tunnel", "list")
	if err != nil {
		return "", fmt.Errorf("list tunnels: %w", err)
	}
	for _, line := range strings.Split(output, "\n") {
		match := tunnelListRowPattern.FindStringSubmatch(strings.TrimSpace(line))
		if len(match) < 3 {
			continue
		}
		if match[2] != name {
			continue
		}
		return match[1], nil
	}
	return "", fmt.Errorf("tunnel %q not found after create conflict", name)
}
