package gatewayctl

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type rootOptions struct {
	url        string
	configPath string
	timeout    time.Duration
	showHelp   bool
	showVer    bool
}

type app struct {
	out        io.Writer
	errOut     io.Writer
	in         io.Reader
	version    string
	configPath string
	config     storedConfig
	baseURL    string
	client     *apiClient
}

func Run(args []string, in io.Reader, out, errOut io.Writer, version string) int {
	opts, rest, err := parseRootOptions(args)
	if err != nil {
		fmt.Fprintln(errOut, "gatewayctl:", err)
		return 2
	}
	if opts.showVer {
		fmt.Fprintln(out, version)
		return 0
	}
	if opts.showHelp || len(rest) == 0 {
		writeUsage(out)
		return 0
	}
	configPath := opts.configPath
	if strings.TrimSpace(configPath) == "" {
		configPath, err = defaultConfigPath()
		if err != nil {
			fmt.Fprintln(errOut, "gatewayctl:", err)
			return 1
		}
	}
	cfg, err := loadStoredConfig(configPath)
	if err != nil {
		fmt.Fprintln(errOut, "gatewayctl:", err)
		return 1
	}
	baseURL := firstNonEmpty(opts.url, os.Getenv("GATEWAYCTL_URL"), os.Getenv("GATEWAY_URL"), cfg.BaseURL, defaultGatewayURL)
	baseURL, err = normalizeGatewayURL(baseURL)
	if err != nil {
		fmt.Fprintln(errOut, "gatewayctl:", err)
		return 1
	}
	jar, err := newCookieJar(baseURL, cfg.Cookies)
	if err != nil {
		fmt.Fprintln(errOut, "gatewayctl:", err)
		return 1
	}
	timeout := opts.timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	a := &app{
		out: out, errOut: errOut, in: in, version: version,
		configPath: configPath, config: cfg, baseURL: baseURL,
		client: newAPIClient(baseURL, timeout, jar),
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := a.execute(ctx, rest); err != nil {
		fmt.Fprintln(errOut, "gatewayctl:", err)
		return 1
	}
	return 0
}

func parseRootOptions(args []string) (rootOptions, []string, error) {
	opts := rootOptions{timeout: 30 * time.Second}
	for len(args) > 0 {
		arg := args[0]
		if arg == "--" {
			return opts, args[1:], nil
		}
		if !strings.HasPrefix(arg, "-") {
			break
		}
		name, value, hasValue := strings.Cut(arg, "=")
		next := func() (string, error) {
			if hasValue {
				return value, nil
			}
			if len(args) < 2 {
				return "", fmt.Errorf("%s requires a value", name)
			}
			args = args[1:]
			return args[0], nil
		}
		switch name {
		case "-h", "--help":
			opts.showHelp = true
		case "--version":
			opts.showVer = true
		case "--url", "--gateway-url":
			got, err := next()
			if err != nil {
				return opts, nil, err
			}
			opts.url = got
		case "--config":
			got, err := next()
			if err != nil {
				return opts, nil, err
			}
			opts.configPath = got
		case "--timeout":
			got, err := next()
			if err != nil {
				return opts, nil, err
			}
			parsed, err := time.ParseDuration(got)
			if err != nil || parsed <= 0 {
				return opts, nil, fmt.Errorf("invalid --timeout %q", got)
			}
			opts.timeout = parsed
		default:
			return opts, nil, fmt.Errorf("unknown global option %s", name)
		}
		args = args[1:]
	}
	return opts, args, nil
}

func (a *app) execute(ctx context.Context, args []string) error {
	switch args[0] {
	case "help":
		writeUsage(a.out)
		return nil
	case "login":
		return a.commandLogin(ctx, args[1:])
	case "logout":
		return a.commandLogout(ctx)
	case "status":
		return a.commandStatus(ctx)
	case "api":
		return a.commandAPI(ctx, args[1:])
	case "provider", "providers":
		return a.commandProvider(ctx, args[1:])
	case "upstream", "upstreams", "inherit":
		return a.commandUpstream(ctx, args[1:])
	case "downstream", "downstreams":
		return a.commandDownstream(ctx, args[1:])
	case "tunnel", "public":
		return a.commandTunnel(ctx, args[1:])
	default:
		return fmt.Errorf("unknown command %q; run gatewayctl help", args[0])
	}
}

func (a *app) commandLogin(ctx context.Context, args []string) error {
	fs := newFlagSet("login")
	username := fs.String("username", "admin", "console username")
	password := fs.String("password", "", "console password (prefer --password-stdin)")
	passwordStdin := fs.Bool("password-stdin", false, "read console password from stdin")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pass, err := a.passwordValue(*password, *passwordStdin)
	if err != nil {
		return err
	}
	raw, err := a.client.login(ctx, *username, pass)
	if err != nil {
		return err
	}
	if err := a.persistSession(); err != nil {
		return fmt.Errorf("login succeeded but session was not saved: %w", err)
	}
	return a.writeRawJSONOrText(raw)
}

func (a *app) commandLogout(ctx context.Context) error {
	raw, reqErr := a.requestOnce(ctx, http.MethodPost, "/__auth/logout", nil)
	a.config.BaseURL = a.baseURL
	a.config.Cookies = nil
	if err := saveStoredConfig(a.configPath, a.config); err != nil {
		return err
	}
	if reqErr != nil {
		var apiErr *apiError
		if !errors.As(reqErr, &apiErr) || apiErr.Status != http.StatusUnauthorized {
			return reqErr
		}
	}
	if len(raw) == 0 {
		return a.writeJSON(map[string]any{"ok": true, "sessionCleared": true})
	}
	return a.writeRawJSONOrText(raw)
}

func (a *app) commandStatus(ctx context.Context) error {
	auth, err := a.getJSON(ctx, "/__auth/status")
	if err != nil {
		return err
	}
	versionRaw, err := a.request(ctx, http.MethodGet, "/__app/version", nil)
	if err != nil {
		return err
	}
	publicRaw, err := a.request(ctx, http.MethodGet, "/__public", nil)
	if err != nil {
		return err
	}
	var version any
	if err := json.Unmarshal(versionRaw, &version); err != nil {
		return err
	}
	var public any
	if err := json.Unmarshal(publicRaw, &public); err != nil {
		return err
	}
	apiURL, uiURL := extractPublicURLs(public)
	return a.writeJSON(map[string]any{
		"gatewayUrl": a.baseURL,
		"auth":       auth,
		"version":    version,
		"public":     public,
		"apiUrl":     apiURL,
		"uiUrl":      uiURL,
	})
}

func (a *app) commandAPI(ctx context.Context, args []string) error {
	method, path := "", ""
	data, dataFile := "", ""
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch arg {
		case "--data":
			if index+1 >= len(args) {
				return fmt.Errorf("--data requires a value")
			}
			index++
			data = args[index]
		case "--data-file":
			if index+1 >= len(args) {
				return fmt.Errorf("--data-file requires a value")
			}
			index++
			dataFile = args[index]
		default:
			if strings.HasPrefix(arg, "--data=") {
				data = strings.TrimPrefix(arg, "--data=")
			} else if strings.HasPrefix(arg, "--data-file=") {
				dataFile = strings.TrimPrefix(arg, "--data-file=")
			} else if method == "" {
				method = arg
			} else if path == "" {
				path = arg
			} else {
				return fmt.Errorf("unexpected argument %q", arg)
			}
		}
	}
	if method == "" || path == "" {
		return fmt.Errorf("usage: gatewayctl api METHOD /path [--data JSON]")
	}
	bodyText := strings.TrimSpace(data)
	if dataFile != "" {
		if bodyText != "" {
			return fmt.Errorf("--data and --data-file cannot be combined")
		}
		raw, err := os.ReadFile(dataFile)
		if err != nil {
			return err
		}
		bodyText = string(raw)
	}
	var body any
	if bodyText != "" {
		if !json.Valid([]byte(bodyText)) {
			return fmt.Errorf("request body is not valid JSON")
		}
		body = json.RawMessage(bodyText)
	}
	raw, err := a.request(ctx, strings.ToUpper(method), path, body)
	if err != nil {
		return err
	}
	return a.writeRawJSONOrText(raw)
}

func (a *app) commandProvider(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "list" {
		raw, err := a.request(ctx, http.MethodGet, "/__state", nil)
		if err != nil {
			return err
		}
		var state struct {
			Providers []struct {
				ID           string `json:"id"`
				Name         string `json:"name"`
				Protocol     string `json:"protocol"`
				BaseURL      string `json:"baseUrl"`
				AuthType     string `json:"authType,omitempty"`
				HealthStatus string `json:"healthStatus,omitempty"`
				Disabled     bool   `json:"disabled,omitempty"`
			} `json:"providers"`
		}
		if err := json.Unmarshal(raw, &state); err != nil {
			return err
		}
		return a.writeJSON(map[string]any{"providers": state.Providers, "count": len(state.Providers)})
	}
	if args[0] != "add" {
		return fmt.Errorf("usage: gatewayctl provider list|add")
	}
	fs := newFlagSet("provider add")
	name := fs.String("name", "", "provider display name")
	protocol := fs.String("protocol", "", "openai_chat, openai_responses, claude, or gemini")
	baseURL := fs.String("base-url", "", "upstream base URL")
	apiKey := fs.String("api-key", "", "upstream API key (prefer --api-key-stdin)")
	apiKeyStdin := fs.Bool("api-key-stdin", false, "read upstream API key from stdin")
	defaultModel := fs.String("default-model", "", "default model")
	authHeader := fs.String("auth-header", "", "authentication header, default Authorization")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	key, err := a.passwordValue(*apiKey, *apiKeyStdin)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*name) == "" || strings.TrimSpace(*protocol) == "" || strings.TrimSpace(*baseURL) == "" {
		return fmt.Errorf("--name, --protocol and --base-url are required")
	}
	payload := map[string]any{
		"name":         strings.TrimSpace(*name),
		"protocol":     strings.TrimSpace(*protocol),
		"baseUrl":      strings.TrimSpace(*baseURL),
		"apiKeySource": "literal:" + key,
	}
	if strings.TrimSpace(*defaultModel) != "" {
		payload["defaultModel"] = strings.TrimSpace(*defaultModel)
	}
	if strings.TrimSpace(*authHeader) != "" {
		payload["authHeader"] = strings.TrimSpace(*authHeader)
	}
	raw, err := a.request(ctx, http.MethodPost, "/__providers", payload)
	if err != nil {
		return err
	}
	return a.writeRawJSONOrText(raw)
}

type peerUpstreamView struct {
	ProviderID        string `json:"providerId"`
	Name              string `json:"name"`
	ParentURL         string `json:"parentUrl"`
	UpstreamKey       string `json:"upstreamKey,omitempty"`
	UpstreamName      string `json:"upstreamName,omitempty"`
	LoginUsername     string `json:"loginUsername,omitempty"`
	PeerUsername      string `json:"peerUsername,omitempty"`
	CredentialsStored bool   `json:"credentialsStored,omitempty"`
}

type peerUpstreamsResponse struct {
	Upstreams []peerUpstreamView `json:"upstreams"`
	GatewayID string             `json:"gatewayId"`
}

func (a *app) commandUpstream(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "list" {
		raw, err := a.request(ctx, http.MethodGet, "/__peer-upstreams", nil)
		if err != nil {
			return err
		}
		return a.writeRawJSONOrText(raw)
	}
	action := args[0]
	switch action {
	case "add":
		return a.commandUpstreamAdd(ctx, args[1:])
	case "reconnect", "refresh", "remove", "credentials":
		return a.commandUpstreamAction(ctx, action, args[1:])
	default:
		return fmt.Errorf("usage: gatewayctl upstream list|add|reconnect|refresh|credentials|remove")
	}
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(value string) error {
	for _, item := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			*s = append(*s, trimmed)
		}
	}
	return nil
}

func (a *app) commandUpstreamAdd(ctx context.Context, args []string) error {
	fs := newFlagSet("upstream add")
	parentURL := fs.String("url", "", "parent gateway URL")
	username := fs.String("username", "", "registration username")
	password := fs.String("password", "", "registration password (prefer --password-stdin)")
	passwordStdin := fs.Bool("password-stdin", false, "read registration password from stdin")
	name := fs.String("name", "", "local upstream name")
	protocol := fs.String("protocol", "", "legacy protocol hint")
	var requested stringList
	fs.Var(&requested, "provider", "requested provider id (repeatable or comma-separated)")
	fs.Var(&requested, "providers", "requested provider ids (comma-separated)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pass, err := a.passwordValue(*password, *passwordStdin)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*parentURL) == "" || strings.TrimSpace(*username) == "" || strings.TrimSpace(pass) == "" {
		return fmt.Errorf("--url, --username and a password are required")
	}
	payload := map[string]any{
		"parentUrl": strings.TrimSpace(*parentURL),
		"username":  strings.TrimSpace(*username),
		"password":  pass,
	}
	if strings.TrimSpace(*name) != "" {
		payload["name"] = strings.TrimSpace(*name)
	}
	if strings.TrimSpace(*protocol) != "" {
		payload["protocol"] = strings.TrimSpace(*protocol)
	}
	if len(requested) > 0 {
		payload["requestedProviderIds"] = []string(requested)
	}
	raw, err := a.request(ctx, http.MethodPost, "/__peer-upstreams", payload)
	if err != nil {
		return err
	}
	return a.writeRawJSONOrText(raw)
}

func (a *app) commandUpstreamAction(ctx context.Context, action string, args []string) error {
	fs := newFlagSet("upstream " + action)
	id := fs.String("id", "", "provider/upstream id (optional when exactly one upstream exists)")
	username := fs.String("username", "", "override registration username")
	password := fs.String("password", "", "override registration password")
	passwordStdin := fs.Bool("password-stdin", false, "read override password from stdin")
	var requested stringList
	fs.Var(&requested, "provider", "requested provider id (repeatable or comma-separated)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	selector := strings.TrimSpace(*id)
	if selector == "" && len(fs.Args()) > 0 {
		selector = fs.Args()[0]
	}
	upstream, err := a.resolveUpstream(ctx, selector)
	if err != nil {
		return err
	}
	path := "/__peer-upstreams/" + urlPathEscape(upstream.ProviderID)
	switch action {
	case "credentials":
		raw, err := a.request(ctx, http.MethodGet, path+"/credentials", nil)
		if err != nil {
			return err
		}
		return a.writeRawJSONOrText(raw)
	case "refresh":
		raw, err := a.request(ctx, http.MethodPost, path+"/refresh", nil)
		if err != nil {
			return err
		}
		return a.writeRawJSONOrText(raw)
	case "remove":
		raw, err := a.request(ctx, http.MethodDelete, path, nil)
		if err != nil {
			return err
		}
		return a.writeRawJSONOrText(raw)
	case "reconnect":
		payload := map[string]any{}
		if strings.TrimSpace(*username) != "" {
			payload["username"] = strings.TrimSpace(*username)
		}
		if strings.TrimSpace(*password) != "" || *passwordStdin {
			pass, err := a.passwordValue(*password, *passwordStdin)
			if err != nil {
				return err
			}
			payload["password"] = pass
		}
		if len(requested) > 0 {
			payload["requestedProviderIds"] = []string(requested)
		}
		raw, err := a.request(ctx, http.MethodPost, path+"/reconnect", payload)
		if err != nil {
			return err
		}
		return a.writeRawJSONOrText(raw)
	default:
		return fmt.Errorf("unsupported upstream action %s", action)
	}
}

type peerDownstreamView struct {
	UserID     string   `json:"userId"`
	Username   string   `json:"username"`
	GatewayID  string   `json:"gatewayId"`
	Name       string   `json:"name,omitempty"`
	Enabled    bool     `json:"enabled"`
	KeyEnabled bool     `json:"keyEnabled"`
	GrantMode  string   `json:"grantMode,omitempty"`
	GrantedIDs []string `json:"grantedProviderIds"`
}

func (a *app) commandDownstream(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "list" {
		raw, err := a.request(ctx, http.MethodGet, "/__peers", nil)
		if err != nil {
			return err
		}
		return a.writeRawJSONOrText(raw)
	}
	action := args[0]
	if action == "policy" {
		fs := newFlagSet("downstream policy")
		requireApproval := fs.Bool("require-approval", false, "new downstream registrations stay pending until approved")
		autoApprove := fs.Bool("auto-approve", false, "new downstream registrations are immediately enabled")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *requireApproval == *autoApprove {
			return fmt.Errorf("pass exactly one of --require-approval or --auto-approve")
		}
		raw, err := a.request(ctx, http.MethodPatch, "/__settings/peer-approval", map[string]any{"required": *requireApproval})
		if err != nil {
			return err
		}
		return a.writeRawJSONOrText(raw)
	}
	if action != "approve" && action != "authorize" && action != "revoke" {
		return fmt.Errorf("usage: gatewayctl downstream list|approve|authorize|revoke|policy")
	}
	fs := newFlagSet("downstream " + action)
	id := fs.String("id", "", "downstream userId (optional when exactly one peer exists)")
	all := fs.Bool("all", false, "grant all current and future providers")
	var providers stringList
	fs.Var(&providers, "provider", "provider id (repeatable or comma-separated)")
	fs.Var(&providers, "providers", "provider ids (comma-separated)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	selector := strings.TrimSpace(*id)
	if selector == "" && len(fs.Args()) > 0 {
		selector = fs.Args()[0]
	}
	peer, err := a.resolveDownstream(ctx, selector)
	if err != nil {
		return err
	}
	path := "/__peers/" + urlPathEscape(peer.UserID)
	if action == "revoke" {
		raw, err := a.request(ctx, http.MethodDelete, path, nil)
		if err != nil {
			return err
		}
		return a.writeRawJSONOrText(raw)
	}
	payload := map[string]any{}
	if *all {
		payload["grantMode"] = "all"
	} else if len(providers) > 0 {
		payload["providerIds"] = []string(providers)
	}
	if action == "authorize" && !*all && len(providers) == 0 {
		return fmt.Errorf("--all or at least one --provider is required")
	}
	method := http.MethodPatch
	requestPath := path + "/grants"
	if action == "approve" {
		method = http.MethodPost
		requestPath = path + "/approve"
	}
	raw, err := a.request(ctx, method, requestPath, payload)
	if err != nil {
		return err
	}
	return a.writeRawJSONOrText(raw)
}

func (a *app) commandTunnel(ctx context.Context, args []string) error {
	action := "status"
	rawMode := false
	ui := false
	filtered := make([]string, 0, len(args))
	for _, arg := range args {
		switch arg {
		case "--raw":
			rawMode = true
		case "--ui":
			ui = true
		default:
			filtered = append(filtered, arg)
		}
	}
	if len(filtered) > 0 {
		action = filtered[0]
	}
	switch action {
	case "status":
		raw, err := a.request(ctx, http.MethodGet, "/__public", nil)
		if err != nil {
			return err
		}
		return a.writeRawJSONOrText(raw)
	case "url":
		publicRaw, err := a.request(ctx, http.MethodGet, "/__public", nil)
		if err != nil {
			return err
		}
		var public any
		if err := json.Unmarshal(publicRaw, &public); err != nil {
			return err
		}
		apiURL, uiURL := extractPublicURLs(public)
		if rawMode {
			value := apiURL
			if ui {
				value = uiURL
			}
			if value == "" {
				return fmt.Errorf("public %s URL is not available", map[bool]string{true: "UI", false: "API"}[ui])
			}
			fmt.Fprintln(a.out, value)
			return nil
		}
		return a.writeJSON(map[string]any{"apiUrl": apiURL, "uiUrl": uiURL, "public": public})
	case "start", "stop":
		raw, err := a.request(ctx, http.MethodPost, "/__public/"+action, map[string]any{})
		if err != nil {
			return err
		}
		return a.writeRawJSONOrText(raw)
	default:
		return fmt.Errorf("usage: gatewayctl tunnel status|url|start|stop")
	}
}

func (a *app) resolveUpstream(ctx context.Context, selector string) (peerUpstreamView, error) {
	raw, err := a.request(ctx, http.MethodGet, "/__peer-upstreams", nil)
	if err != nil {
		return peerUpstreamView{}, err
	}
	var payload peerUpstreamsResponse
	if err := json.Unmarshal(raw, &payload); err != nil {
		return peerUpstreamView{}, err
	}
	return selectUpstream(payload.Upstreams, selector)
}

func selectUpstream(items []peerUpstreamView, selector string) (peerUpstreamView, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		if len(items) == 1 {
			return items[0], nil
		}
		return peerUpstreamView{}, fmt.Errorf("multiple upstreams found; pass --id or a selector")
	}
	matches := make([]peerUpstreamView, 0, 1)
	for _, item := range items {
		for _, candidate := range []string{item.ProviderID, item.UpstreamKey, item.Name, item.UpstreamName, item.ParentURL} {
			if strings.EqualFold(strings.TrimSpace(candidate), selector) {
				matches = append(matches, item)
				break
			}
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) == 0 {
		return peerUpstreamView{}, fmt.Errorf("upstream %q not found", selector)
	}
	ids := make([]string, 0, len(matches))
	for _, match := range matches {
		ids = append(ids, match.ProviderID)
	}
	return peerUpstreamView{}, fmt.Errorf("upstream selector %q is ambiguous: %s", selector, strings.Join(ids, ", "))
}

func (a *app) resolveDownstream(ctx context.Context, selector string) (peerDownstreamView, error) {
	raw, err := a.request(ctx, http.MethodGet, "/__peers", nil)
	if err != nil {
		return peerDownstreamView{}, err
	}
	var payload struct {
		Peers []peerDownstreamView `json:"peers"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return peerDownstreamView{}, err
	}
	selector = strings.TrimSpace(selector)
	if selector == "" {
		if len(payload.Peers) == 1 {
			return payload.Peers[0], nil
		}
		return peerDownstreamView{}, fmt.Errorf("multiple downstreams found; pass --id or a selector")
	}
	matches := make([]peerDownstreamView, 0, 1)
	for _, item := range payload.Peers {
		for _, candidate := range []string{item.UserID, item.GatewayID, item.Username, item.Name} {
			if strings.EqualFold(strings.TrimSpace(candidate), selector) {
				matches = append(matches, item)
				break
			}
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) == 0 {
		return peerDownstreamView{}, fmt.Errorf("downstream %q not found", selector)
	}
	ids := make([]string, 0, len(matches))
	for _, match := range matches {
		ids = append(ids, match.UserID)
	}
	return peerDownstreamView{}, fmt.Errorf("downstream selector %q is ambiguous: %s", selector, strings.Join(ids, ", "))
}

func (a *app) request(ctx context.Context, method, path string, body any) ([]byte, error) {
	raw, err := a.requestOnce(ctx, method, path, body)
	if err == nil {
		return raw, nil
	}
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		return nil, err
	}
	username := strings.TrimSpace(os.Getenv("GATEWAYCTL_USERNAME"))
	password := os.Getenv("GATEWAYCTL_PASSWORD")
	if username == "" || password == "" {
		return nil, err
	}
	if _, loginErr := a.client.login(ctx, username, password); loginErr != nil {
		return nil, loginErr
	}
	if saveErr := a.persistSession(); saveErr != nil {
		return nil, fmt.Errorf("auto-login succeeded but session was not saved: %w", saveErr)
	}
	return a.requestOnce(ctx, method, path, body)
}

func (a *app) requestOnce(ctx context.Context, method, path string, body any) ([]byte, error) {
	return a.client.request(ctx, method, path, body)
}

func (a *app) getJSON(ctx context.Context, path string) (any, error) {
	raw, err := a.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (a *app) persistSession() error {
	a.config.BaseURL = a.baseURL
	a.config.Cookies = cookiesForURL(a.client.jar, a.baseURL)
	return saveStoredConfig(a.configPath, a.config)
}

func (a *app) passwordValue(password string, fromStdin bool) (string, error) {
	if fromStdin {
		if strings.TrimSpace(password) != "" {
			return "", fmt.Errorf("--password and --password-stdin cannot be combined")
		}
		raw, err := io.ReadAll(io.LimitReader(a.in, 1<<20))
		if err != nil {
			return "", err
		}
		return strings.TrimRight(string(raw), "\r\n"), nil
	}
	if strings.TrimSpace(password) == "" {
		return "", fmt.Errorf("password is required; pass --password-stdin to avoid shell history")
	}
	return password, nil
}

func (a *app) writeJSON(value any) error {
	encoder := json.NewEncoder(a.out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func (a *app) writeRawJSONOrText(raw []byte) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil
	}
	if json.Valid([]byte(trimmed)) {
		var value any
		if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
			return err
		}
		return a.writeJSON(value)
	}
	_, err := fmt.Fprintln(a.out, trimmed)
	return err
}

func extractPublicURLs(public any) (string, string) {
	root, _ := public.(map[string]any)
	apiURL := stringField(root, "publicBaseUrl")
	uiURL := stringField(root, "uiPublicBaseUrl")
	if tunnel, ok := root["tunnel"].(map[string]any); ok {
		if value := stringField(tunnel, "publicUrl"); value != "" {
			apiURL = value
		}
		if value := stringField(tunnel, "uiPublicUrl"); value != "" {
			uiURL = value
		}
	}
	if uiURL == "" {
		uiURL = apiURL
	}
	return apiURL, uiURL
}

func stringField(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

func urlPathEscape(value string) string {
	return url.PathEscape(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func writeUsage(out io.Writer) {
	fmt.Fprint(out, `gatewayctl - LLM Protocol Gateway CLI for AI and scripts

Usage:
  gatewayctl [--url URL] [--config FILE] [--timeout 30s] <command> [flags]

Authentication:
  Local requests to 127.0.0.1 use the gateway's loopback admin bypass.
  Remote requests: gatewayctl login --username admin --password-stdin
  The saved session cookie is stored in the user config directory with mode 0600.

Commands:
  status                              health/version/auth/public URL summary
  login | logout                      save or clear a remote console session
  api METHOD /path [--data JSON]       generic authenticated JSON API call
  provider list                       list configured providers
  provider add --name N --protocol P --base-url URL [--api-key-stdin]
  upstream list                       list inherited parent gateways
  upstream add --url URL --username U --password-stdin [--name N] [--provider ID]
  upstream reconnect [--id ID]        reconnect; saved username/password auto-fill
  upstream credentials [--id ID]      reveal/copy locally saved login credentials
  upstream refresh|remove [--id ID]   sync grants or disconnect an upstream
  downstream list                     list registered downstream gateways
  downstream approve [--id ID] [--provider ID|--all]
  downstream authorize --id ID --provider ID[,ID] | --all
  downstream revoke [--id ID]
  downstream policy --require-approval | --auto-approve
  tunnel url [--ui] [--raw]           get live Cloudflare/public API or UI URL
  tunnel status|start|stop

Environment:
  GATEWAYCTL_URL, GATEWAYCTL_CONFIG, GATEWAYCTL_USERNAME, GATEWAYCTL_PASSWORD

All read/action commands print JSON by default, making the output safe for AI
tool calls. Passwords are only printed by the explicit "upstream credentials"
command and the matching console endpoint, both requiring peers.manage.
`)
}
