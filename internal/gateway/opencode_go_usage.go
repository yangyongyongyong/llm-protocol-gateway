package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OpenCode Go 套餐额度查询。
//
// OpenCode Go 没有公开的额度文档，但其控制台使用的同源接口为：
//
//	GET https://opencode.ai/zen/go/v1/usage
//	Authorization: Bearer <OpenCode API Key>
//
// 返回 rolling / weekly / monthly 三个百分比窗口，分别对应 5 小时、7 天和
// 月额度。该接口与 Magpie 当前读取 OpenCode Go 额度的方式一致。
const openCodeGoUsagePath = "/zen/go/v1/usage"

// openCodeGoUsageURL 返回额度接口地址。额度接口固定在 Go 部署的 /v1/usage，
// 这里保留 Provider 配置里的 scheme 与 host，路径则统一归一到官方端点。
func openCodeGoUsageURL(baseURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err == nil && parsed.Scheme != "" && parsed.Host != "" {
		return parsed.Scheme + "://" + parsed.Host + openCodeGoUsagePath
	}
	return "https://opencode.ai" + openCodeGoUsagePath
}

// OpenCodeGoUsageBucket 是 OpenCode Go 的一个百分比额度窗口。
type OpenCodeGoUsageBucket struct {
	Status      string  `json:"status,omitempty"`
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at,omitempty"`
}

// OpenCodeGoUsageReport 是返回给控制台的 OpenCode Go 额度快照。
type OpenCodeGoUsageReport struct {
	Available bool                   `json:"available"`
	Error     string                 `json:"error,omitempty"`
	FetchedAt string                 `json:"fetchedAt,omitempty"`
	FiveHour  *OpenCodeGoUsageBucket `json:"five_hour,omitempty"`
	Weekly    *OpenCodeGoUsageBucket `json:"weekly,omitempty"`
	Monthly   *OpenCodeGoUsageBucket `json:"monthly,omitempty"`
}

// openCodeGoRawUsageBucket 对应接口里的 usage.rolling / weekly / monthly。
type openCodeGoRawUsageBucket struct {
	Status   string  `json:"status"`
	Percent  float64 `json:"percent"`
	ResetsAt string  `json:"resetsAt"`
}

// openCodeGoUsageFromBody 解析 OpenCode Go 的额度响应。字段按指针处理，
// 这样某个窗口缺失时不会在控制台误画成 0%。
func openCodeGoUsageFromBody(body []byte) (OpenCodeGoUsageReport, error) {
	var parsed struct {
		Usage struct {
			Rolling *openCodeGoRawUsageBucket `json:"rolling"`
			Weekly  *openCodeGoRawUsageBucket `json:"weekly"`
			Monthly *openCodeGoRawUsageBucket `json:"monthly"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return OpenCodeGoUsageReport{}, fmt.Errorf("解析 OpenCode Go 额度响应失败: %w", err)
	}

	// convert 把接口原始窗口转成对控制台稳定的字段名与 JSON 形状。
	convert := func(raw *openCodeGoRawUsageBucket) *OpenCodeGoUsageBucket {
		if raw == nil {
			return nil
		}
		return &OpenCodeGoUsageBucket{
			Status:      strings.TrimSpace(raw.Status),
			Utilization: raw.Percent,
			ResetsAt:    strings.TrimSpace(raw.ResetsAt),
		}
	}

	report := OpenCodeGoUsageReport{
		Available: true,
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
		FiveHour:  convert(parsed.Usage.Rolling),
		Weekly:    convert(parsed.Usage.Weekly),
		Monthly:   convert(parsed.Usage.Monthly),
	}
	if report.FiveHour == nil && report.Weekly == nil && report.Monthly == nil {
		return OpenCodeGoUsageReport{Available: false, Error: "OpenCode Go 未返回额度窗口"}, nil
	}
	return report, nil
}

// fetchOpenCodeGoUsage 读取 OpenCode Go 账号当前的三档额度。
func fetchOpenCodeGoUsage(ctx context.Context, baseURL, apiKey string) (OpenCodeGoUsageReport, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return OpenCodeGoUsageReport{Available: false, Error: "缺少 OpenCode Go API Key"}, nil
	}
	if strings.HasPrefix(strings.ToLower(apiKey), "bearer ") {
		apiKey = strings.TrimSpace(apiKey[len("bearer "):])
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openCodeGoUsageURL(baseURL), nil)
	if err != nil {
		return OpenCodeGoUsageReport{}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second, Transport: usageProbeTransport}
	resp, err := client.Do(req)
	if err != nil {
		return OpenCodeGoUsageReport{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return OpenCodeGoUsageReport{Available: false, Error: fmt.Sprintf("OpenCode Go 鉴权失败（HTTP %d）", resp.StatusCode)}, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return OpenCodeGoUsageReport{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 不把上游响应体回传控制台：错误响应可能回显请求头或凭据。
		return OpenCodeGoUsageReport{Available: false, Error: fmt.Sprintf("OpenCode Go 额度接口返回 HTTP %d", resp.StatusCode)}, nil
	}
	report, err := openCodeGoUsageFromBody(body)
	if err != nil {
		return OpenCodeGoUsageReport{Available: false, Error: err.Error()}, nil
	}
	return report, nil
}
