package gatewayctl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type apiClient struct {
	baseURL string
	http    *http.Client
	jar     http.CookieJar
}

type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("gateway returned HTTP %d", e.Status)
	}
	return e.Message
}

func newAPIClient(baseURL string, timeout time.Duration, jar http.CookieJar) *apiClient {
	return &apiClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: timeout, Jar: jar},
		jar:     jar,
	}
}

func (c *apiClient) request(ctx context.Context, method, path string, body any) ([]byte, error) {
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("API path must start with /")
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(method), c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &apiError{Status: resp.StatusCode, Message: apiErrorMessage(raw)}
	}
	return raw, nil
}

func apiErrorMessage(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return ""
	}
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &envelope) == nil {
		if msg := strings.TrimSpace(envelope.Error.Message); msg != "" {
			return msg
		}
		if msg := strings.TrimSpace(envelope.Message); msg != "" {
			return msg
		}
	}
	return text
}

func (c *apiClient) login(ctx context.Context, username, password string) ([]byte, error) {
	if strings.TrimSpace(username) == "" {
		username = "admin"
	}
	return c.request(ctx, http.MethodPost, "/__auth/login", map[string]any{
		"username":    strings.TrimSpace(username),
		"password":    password,
		"trustDevice": true,
	})
}
