package gatewayctl

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const defaultGatewayURL = "http://127.0.0.1:18093"

type storedConfig struct {
	BaseURL string        `json:"baseUrl,omitempty"`
	Cookies []http.Cookie `json:"cookies,omitempty"`
}

func defaultConfigPath() (string, error) {
	if override := strings.TrimSpace(os.Getenv("GATEWAYCTL_CONFIG")); override != "" {
		return filepath.Abs(override)
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "llm-protocol-gateway", "gatewayctl.json"), nil
}

func loadStoredConfig(path string) (storedConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return storedConfig{}, nil
		}
		return storedConfig{}, err
	}
	var cfg storedConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return storedConfig{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

func saveStoredConfig(path string, cfg storedConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".gatewayctl-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func normalizeGatewayURL(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		value = defaultGatewayURL
	}
	if !strings.Contains(value, "://") {
		value = "http://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("invalid gateway URL %q", raw)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("gateway URL must use http or https")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return strings.TrimRight(parsed.String(), "/"), nil
}

func newCookieJar(baseURL string, cookies []http.Cookie) (http.CookieJar, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	for index := range cookies {
		cookie := cookies[index]
		jar.SetCookies(parsed, []*http.Cookie{&cookie})
	}
	return jar, nil
}

func cookiesForURL(jar http.CookieJar, baseURL string) []http.Cookie {
	if jar == nil {
		return nil
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil
	}
	items := jar.Cookies(parsed)
	out := make([]http.Cookie, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		out = append(out, *item)
	}
	return out
}
