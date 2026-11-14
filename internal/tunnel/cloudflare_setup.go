package tunnel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// NamedTunnelName derives a stable, collision-resistant cloudflared tunnel
// name from the gateway's stable identity. 16 hex chars (64 bits) keeps names
// readable while making collisions between two machines implausible. The name
// must stay stable across restarts so DNS reuse and credentials keep working.
func NamedTunnelName(gatewayID string) string {
	identity := strings.TrimSpace(gatewayID)
	if identity == "" {
		identity = "unknown-gateway"
	}
	sum := sha256.Sum256([]byte(identity))
	return "llm-protocol-gateway-" + hex.EncodeToString(sum[:12])
}

// namedTunnelConfigFile gives each gateway its own config file under the shared
// Application Support directory. Two machines may share one Cloudflare account;
// they must not overwrite each other's ingress file.
func namedTunnelConfigFile(appDir, gatewayID string) string {
	suffix := strings.TrimPrefix(NamedTunnelName(gatewayID), "llm-protocol-gateway-")
	return filepath.Join(appDir, "tunnel-"+suffix+".yml")
}

// legacySharedTunnelName is the old fixed default. Existing installations may
// still carry it in SQLite; treating it as an explicit identity would keep two
// machines contending forever, so the gateway migrates it to the derived name.
const legacySharedTunnelName = "llm-protocol-gateway"

// effectiveTunnelName returns an explicitly chosen non-legacy name or derives a
// per-gateway name from the stable identity. This migration is intentionally
// one-way: once a tunnel is provisioned under the derived name, it persists.
func effectiveTunnelName(configured, gatewayID string) string {
	configured = strings.TrimSpace(configured)
	if configured == "" || strings.EqualFold(configured, legacySharedTunnelName) {
		return NamedTunnelName(gatewayID)
	}
	return configured
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
func (c *CloudflareSetup) Provision(ctx context.Context, customDomain, uiDomain, tunnelName, gatewayID string, localPort int) (ProvisionResult, error) {
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
	tunnelName = effectiveTunnelName(tunnelName, gatewayID)
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
	configFile := namedTunnelConfigFile(appDir, gatewayID)
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

// CustomDomainConfigReusable reports whether an existing named-tunnel config can
// be started without calling Cloudflare Provision again (create/list/DNS).
func CustomDomainConfigReusable(configFile, credentialsFile, apiDomain, uiDomain string, localPort int) bool {
	configFile = strings.TrimSpace(configFile)
	credentialsFile = strings.TrimSpace(credentialsFile)
	apiDomain = strings.TrimSpace(apiDomain)
	uiDomain = strings.TrimSpace(uiDomain)
	if configFile == "" || credentialsFile == "" || localPort <= 0 {
		return false
	}
	if apiDomain == "" && uiDomain == "" {
		return false
	}
	if _, err := os.Stat(credentialsFile); err != nil {
		return false
	}
	raw, err := os.ReadFile(configFile)
	if err != nil {
		return false
	}
	text := string(raw)
	if !strings.Contains(text, credentialsFile) {
		return false
	}
	wantService := fmt.Sprintf("http://127.0.0.1:%d", localPort)
	if !strings.Contains(text, wantService) {
		return false
	}
	if apiDomain != "" && !strings.Contains(text, "hostname: "+apiDomain) {
		return false
	}
	if uiDomain != "" && !strings.Contains(text, "hostname: "+uiDomain) {
		return false
	}
	return true
}

// routeDNS creates a CNAME for hostname → tunnel. A pre-existing record is
// never silently stolen from another tunnel: if the final CNAME targets a
// different tunnel id, this returns an explicit conflict. This is what keeps
// two machines on one Cloudflare account from fighting over the same hostname.
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
		// cloudflared offers --overwrite-dns, but that would steal a hostname
		// already used by another computer. Inspect the current CNAME and allow
		// the update only when it already points at this same tunnel (for
		// example a stale record left by an earlier run of this machine).
		currentTunnelID, owned, inspectErr := c.hostnameCNAMETunnelID(ctx, hostname)
		if inspectErr == nil && owned && strings.EqualFold(currentTunnelID, tunnelID) {
			output, err = c.run(ctx, "tunnel", "route", "dns", "--overwrite-dns", tunnelName, hostname)
			if err != nil {
				return fmt.Errorf("route dns for %s (repair same tunnel): %w", hostname, err)
			}
		} else if owned {
			return fmt.Errorf("域名 %s 已被另一条隧道占用（%s）。为避免抢走另一台电脑的流量，不会自动覆盖 DNS；请换成本机自己的子域名。", hostname, currentTunnelID)
		} else if inspectErr != nil {
			return fmt.Errorf("域名 %s 已有 DNS 记录，但无法确认归属：%v。为避免覆盖另一台电脑，不会自动接管。", hostname, inspectErr)
		} else {
			return fmt.Errorf("域名 %s 已有非隧道 DNS 记录。为避免覆盖现有服务，不会自动接管；请换成本机自己的子域名。", hostname)
		}
	}
	if err := c.ensureHostnameCNAME(ctx, hostname, tunnelID); err != nil {
		return fmt.Errorf("ensure dns for %s → %s: %w", hostname, tunnelID, err)
	}
	return nil
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
	records, err := c.listDNSRecordsByName(ctx, do, token.APIToken, zoneID, hostname)
	if err != nil {
		return err
	}
	return c.ensureHostnameCNAMEWithRecords(ctx, do, hostname, tunnelID, token.APIToken, zoneID, records)
}

func (c *CloudflareSetup) ensureHostnameCNAMEWithRecords(
	ctx context.Context,
	do func(*http.Request) (*http.Response, error),
	hostname, tunnelID, apiToken, zoneID string,
	records []cloudflareDNSRecord,
) error {
	target := tunnelCNAMETarget(tunnelID)
	if len(records) == 0 {
		return c.createDNSRecord(ctx, do, apiToken, zoneID, map[string]any{
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
			return nil
		}
	}
	return fmt.Errorf("域名 %s 已经指向另一条隧道（当前 %s，本机 %s）。为避免抢走另一台电脑的流量，不会自动覆盖 DNS；请换成本机自己的子域名。", hostname, strings.TrimSpace(records[0].Content), target)
}

type cloudflareDNSRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
}

// hostnameCNAMETunnelID returns the tunnel UUID currently targeted by hostname.
// It returns owned=false when no CNAME exists (the caller can create one).
func (c *CloudflareSetup) hostnameCNAMETunnelID(ctx context.Context, hostname string) (string, bool, error) {
	path, err := c.originCertPath()
	if err != nil {
		return "", false, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	token, err := parseOriginCertToken(raw)
	if err != nil {
		return "", false, err
	}
	do := c.httpDo
	if do == nil {
		do = http.DefaultClient.Do
	}
	records, err := c.listDNSRecordsByName(ctx, do, token.APIToken, token.ZoneID, hostname)
	if err != nil {
		return "", false, err
	}
	for _, rec := range records {
		if !strings.EqualFold(rec.Type, "CNAME") {
			continue
		}
		content := strings.TrimSpace(rec.Content)
		if !strings.HasSuffix(strings.ToLower(content), ".cfargotunnel.com") {
			return "", false, nil
		}
		id := strings.TrimSuffix(content, ".cfargotunnel.com")
		return id, true, nil
	}
	return "", false, nil
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
	// Prefer reuse: listing first avoids a slow Cloudflare "create" round-trip on
	// every gateway restart when the named tunnel already exists.
	tunnelID, credentialsFile, findErr := c.findTunnelByName(ctx, name)
	if findErr == nil {
		return tunnelID, credentialsFile, nil
	}
	if isMissingTunnelCredentials(findErr) {
		// A tunnel with this name exists in the Cloudflare account, but its
		// credentials are not on this machine. That almost always means another
		// computer created it. Deleting it here would disconnect that machine;
		// fail with an actionable conflict instead.
		return "", "", fmt.Errorf("隧道 %q 已存在于该 Cloudflare 账号，但本机没有它的凭据。为避免顶掉另一台电脑，不会自动删除远端隧道；请给本机使用不同的隧道名，或先在原机器停止并显式清理该隧道。", name)
	}

	output, err := c.run(ctx, "tunnel", "create", name)
	if err == nil {
		return parseTunnelCreateOutput(output)
	}
	lower := strings.ToLower(output + " " + err.Error())
	if !strings.Contains(lower, "already exists") {
		return "", "", fmt.Errorf("create tunnel %q: %w", name, err)
	}
	tunnelID, credentialsFile, findErr = c.findTunnelByName(ctx, name)
	if findErr == nil {
		return tunnelID, credentialsFile, nil
	}
	if isMissingTunnelCredentials(findErr) {
		return "", "", fmt.Errorf("隧道 %q 已存在，但本机没有它的凭据；不会自动删除其它电脑的隧道。请换一个本机专用隧道名。", name)
	}
	return "", "", findErr
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
