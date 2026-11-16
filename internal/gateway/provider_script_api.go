package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// 自定义协议 Provider 的脚本 ABI 与生命周期 API。脚本只负责把私有协议
// 转成一个标准协议；Provider 对外仍然是 openai_chat / openai_responses /
// claude，不新增第四种核心 Protocol。

type providerScriptValidateRequest struct {
	Script   domain.ProviderScript `json:"script"`
	Protocol domain.Protocol       `json:"protocol,omitempty"`
}

type providerScriptValidateResult struct {
	Valid      bool     `json:"valid"`
	Errors     []string `json:"errors,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
	ABIVersion string   `json:"abiVersion"`
	ABI        string   `json:"abi"`
}

// decodeProviderScriptField 解析 PATCH 里 providerScript 的“字段是否存在”。
// encoding/json 反序列化到 *ProviderScript 时无法区分字段缺省和显式 null；
// 但这两者对脚本生命周期完全不同：缺省保留旧进程，null 必须停止并清空。
func decodeProviderScriptField(body []byte) (bool, *domain.ProviderScript, error) {
	var raw struct {
		ProviderScript json.RawMessage `json:"providerScript"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return false, nil, err
	}
	if raw.ProviderScript == nil {
		return false, nil, nil
	}
	if strings.TrimSpace(string(raw.ProviderScript)) == "null" {
		return true, nil, nil
	}
	var script domain.ProviderScript
	if err := json.Unmarshal(raw.ProviderScript, &script); err != nil {
		return true, nil, fmt.Errorf("invalid providerScript: %w", err)
	}
	return true, &script, nil
}

func scriptIsValid(script domain.ProviderScript) providerScriptValidateResult {
	// 兼容旧调用方：没有 Provider.Protocol 时只做通用检查。真正创建或修改
	// Provider 的入口会调用 validateProviderScript，并带上协议做端点提示。
	return validateProviderScript(script, "")
}

// validateProviderScript 在不执行脚本业务逻辑的前提下做静态验收：检查
// 必要参数、协议端点、Python 语法和基础安全边界。端点字符串检查只给
// warning，因为动态路由、变量拼接和框架注册都可能无法被静态识别。
func validateProviderScript(script domain.ProviderScript, protocol domain.Protocol) providerScriptValidateResult {
	result := providerScriptValidateResult{ABIVersion: providerScriptABIVersion, ABI: providerScriptABI}
	source := strings.TrimSpace(script.Source)
	if source == "" {
		result.Errors = append(result.Errors, "转换脚本不能为空")
	}
	entry := strings.TrimSpace(script.Entrypoint)
	if entry == "" {
		entry = "adapter.py"
		result.Warnings = append(result.Warnings, "entrypoint 为空，默认使用 adapter.py")
	}
	if filepath.Base(entry) != entry || strings.Contains(entry, "..") || strings.ContainsAny(entry, `/\`) {
		result.Errors = append(result.Errors, "entrypoint 必须是纯文件名，例如 adapter.py")
	}
	switch strings.ToLower(strings.TrimSpace(script.Language)) {
	case "", "python", "python3":
	default:
		result.Errors = append(result.Errors, "当前只支持 Python 脚本")
	}
	if script.MaxConcurrentRequests < 0 {
		result.Errors = append(result.Errors, "maxConcurrentRequests 不能为负数")
	} else if script.MaxConcurrentRequests > providerConcurrencyCap {
		result.Errors = append(result.Errors, fmt.Sprintf("maxConcurrentRequests 不能超过 %d", providerConcurrencyCap))
	}
	if script.RequestTimeoutSec < 0 {
		result.Errors = append(result.Errors, "requestTimeoutSec 不能为负数")
	} else if script.RequestTimeoutSec > 3600 {
		result.Errors = append(result.Errors, "requestTimeoutSec 不能超过 3600 秒")
	}
	if script.ListenPort < 0 || script.ListenPort > 65535 {
		result.Errors = append(result.Errors, "listenPort 必须在 0-65535 之间；通常留 0 由网关分配")
	} else if script.ListenPort > 0 && script.ListenPort < 1024 {
		result.Warnings = append(result.Warnings, "listenPort 小于 1024，非 root 用户通常无法监听")
	}
	healthPath := strings.TrimSpace(script.HealthPath)
	if healthPath == "" {
		healthPath = "/health"
	}
	if !strings.HasPrefix(healthPath, "/") {
		result.Errors = append(result.Errors, "healthPath 必须以 / 开头")
	}
	if upstream := strings.TrimSpace(script.UpstreamBaseURL); upstream != "" {
		parsed, err := url.Parse(upstream)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			result.Errors = append(result.Errors, "upstreamBaseUrl 必须是完整的 http/https URL")
		}
	} else {
		result.Warnings = append(result.Warnings, "upstreamBaseUrl 为空：只有脚本内部固定了上游地址时才可这样配置")
	}
	switch protocol {
	case "", domain.ProtocolOpenAIChat, domain.ProtocolOpenAIResponses, domain.ProtocolClaude:
	case domain.ProtocolGemini:
		result.Errors = append(result.Errors, "脚本 Provider 当前只支持 openai_chat / openai_responses / claude，不支持 gemini")
	default:
		result.Errors = append(result.Errors, fmt.Sprintf("不支持的 Provider 协议 %q", protocol))
	}
	if source != "" {
		requiredPaths := []string{"/health", "/v1/models"}
		switch protocol {
		case domain.ProtocolOpenAIResponses:
			requiredPaths = append(requiredPaths, "/v1/responses")
		case domain.ProtocolClaude:
			requiredPaths = append(requiredPaths, "/v1/messages")
		case domain.ProtocolOpenAIChat, "":
			requiredPaths = append(requiredPaths, "/v1/chat/completions")
		}
		for _, path := range requiredPaths {
			if !strings.Contains(source, path) {
				result.Warnings = append(result.Warnings, fmt.Sprintf("脚本正文未出现 %s；如果由路由变量或框架动态注册，可忽略此提示", path))
			}
		}
		if !strings.Contains(source, "PROVIDER_SCRIPT_PORT") {
			result.Warnings = append(result.Warnings, "脚本正文未读取 PROVIDER_SCRIPT_PORT；网关会按该端口做健康检查")
		}
		if script.MaxConcurrentRequests > 0 && !strings.Contains(source, "PROVIDER_SCRIPT_MAX_CONCURRENCY") {
			result.Warnings = append(result.Warnings, "已配置最大并发，但脚本正文未读取 PROVIDER_SCRIPT_MAX_CONCURRENCY")
		}
		if script.RequestTimeoutSec > 0 && !strings.Contains(source, "PROVIDER_SCRIPT_REQUEST_TIMEOUT_SEC") {
			result.Warnings = append(result.Warnings, "已配置请求超时，但脚本正文未读取 PROVIDER_SCRIPT_REQUEST_TIMEOUT_SEC")
		}
		if _, err := exec.LookPath(providerScriptPythonCommand()); err != nil {
			// 校验接口跑在可能没有 Python 的控制机上也应能保存配置；真正启动
			// 脚本时仍会因为解释器缺失而失败，并在日志里给出明确错误。
			result.Warnings = append(result.Warnings, "未找到 Python 3 运行时；请安装 python3 或设置 GATEWAY_PROVIDER_SCRIPT_PYTHON，否则启动脚本会失败")
		} else if ok, detail := validateProviderScriptPython(entry, script.Source); !ok {
			result.Errors = append(result.Errors, detail)
		}
	}
	if strings.TrimSpace(script.Requirements) != "" {
		result.Warnings = append(result.Warnings, "requirements.txt 会随脚本落盘；当前版本不会自动 pip install，请确认运行环境已安装依赖或优先使用标准库")
	}
	result.Valid = len(result.Errors) == 0
	return result
}

// validateProviderScriptPython 只执行 python -m py_compile，不导入、不运行
// 用户脚本。这样既能尽早发现语法错误，也不会在“校验”按钮里触发业务请求。
func validateProviderScriptPython(entry, source string) (bool, string) {
	python := providerScriptPythonCommand()
	if _, err := exec.LookPath(python); err != nil {
		return false, "未找到 Python 3 运行时；请安装 python3 或设置 GATEWAY_PROVIDER_SCRIPT_PYTHON"
	}
	dir, err := os.MkdirTemp("", "llm-protocol-gateway-script-check-*")
	if err != nil {
		return false, "无法创建脚本校验临时目录: " + err.Error()
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, entry)
	if err := os.WriteFile(file, []byte(source), 0o600); err != nil {
		return false, "无法写入脚本校验临时文件: " + err.Error()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "-m", "py_compile", entry)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return false, "Python 语法校验超时（10 秒）"
	}
	if err == nil {
		return true, ""
	}
	detail := strings.TrimSpace(string(output))
	detail = strings.ReplaceAll(detail, dir, "<script>")
	detail = strings.ReplaceAll(detail, "\n", "; ")
	if detail == "" {
		detail = err.Error()
	}
	if len(detail) > 1200 {
		detail = detail[:1200] + "…"
	}
	return false, "Python 语法校验失败: " + detail
}

func (s *Server) handleProviderScriptValidate(w http.ResponseWriter, r *http.Request) {
	var payload providerScriptValidateRequest
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid script json: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, validateProviderScript(payload.Script, payload.Protocol))
}

func (s *Server) handleProviderScriptABI(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":       providerScriptABIVersion,
		"abi":           providerScriptABI,
		"examplePrompt": providerScriptPrompt(),
		"protocols":     []domain.Protocol{domain.ProtocolOpenAIChat, domain.ProtocolOpenAIResponses, domain.ProtocolClaude},
		"requiredEnv": []string{
			"PROVIDER_SCRIPT_PORT",
			"PROVIDER_SCRIPT_PROVIDER_ID",
			"PROVIDER_SCRIPT_PROTOCOL",
			"PROVIDER_SCRIPT_HEALTH_PATH",
			"PROVIDER_SCRIPT_MAX_CONCURRENCY",
			"PROVIDER_SCRIPT_REQUEST_TIMEOUT_SEC",
			"PROVIDER_SCRIPT_ENTRYPOINT",
			"UPSTREAM_BASE_URL",
			"UPSTREAM_API_KEY",
			"UPSTREAM_AUTH_HEADER",
		},
	})
}

func (s *Server) handleProviderScriptStart(w http.ResponseWriter, r *http.Request) {
	provider, ok := s.providerScriptProvider(w, r)
	if !ok {
		return
	}
	updated, discovery, err := s.startAndDiscoverProviderScript(r.Context(), provider, false)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":        true,
		"status":         providerScriptRuntimes.status(provider.ID),
		"defaultModel":   updated.DefaultModel,
		"models":         discovery.Models,
		"modelDiscovery": discovery,
	})
}

func (s *Server) handleProviderScriptRestart(w http.ResponseWriter, r *http.Request) {
	provider, ok := s.providerScriptProvider(w, r)
	if !ok {
		return
	}
	updated, discovery, err := s.startAndDiscoverProviderScript(r.Context(), provider, true)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":        true,
		"status":         providerScriptRuntimes.status(provider.ID),
		"defaultModel":   updated.DefaultModel,
		"models":         discovery.Models,
		"modelDiscovery": discovery,
	})
}

func (s *Server) handleProviderScriptStop(w http.ResponseWriter, r *http.Request) {
	provider, ok := s.providerScriptProvider(w, r)
	if !ok {
		return
	}
	providerScriptRuntimes.stop(provider.ID)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "status": providerScriptRuntimes.status(provider.ID)})
}

func (s *Server) handleProviderScriptStatus(w http.ResponseWriter, r *http.Request) {
	provider, ok := s.providerScriptProvider(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, providerScriptRuntimes.status(provider.ID))
}

func (s *Server) handleProviderScriptLogs(w http.ResponseWriter, r *http.Request) {
	provider, ok := s.providerScriptProvider(w, r)
	if !ok {
		return
	}
	status := providerScriptRuntimes.status(provider.ID)
	logPath, _ := status["logPath"].(string)
	if logPath == "" {
		writeJSON(w, http.StatusOK, map[string]any{"logs": "", "running": status["running"]})
		return
	}
	f, err := os.Open(logPath)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	defer f.Close()
	stat, _ := f.Stat()
	const tail = 64 * 1024
	if stat != nil && stat.Size() > tail {
		_, _ = f.Seek(-tail, io.SeekEnd)
	}
	data, _ := io.ReadAll(io.LimitReader(f, tail))
	writeJSON(w, http.StatusOK, map[string]any{"logs": string(data), "running": status["running"], "logPath": logPath})
}

func (s *Server) providerScriptProvider(w http.ResponseWriter, r *http.Request) (domain.Provider, bool) {
	id := r.PathValue("id")
	if !s.requireProviderOwnerForUser(w, r, id) {
		return domain.Provider{}, false
	}
	provider, err := s.router.ProviderByID(id)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return domain.Provider{}, false
	}
	if provider.ProviderScript == nil {
		writeOpenAIError(w, http.StatusBadRequest, "provider has no providerScript")
		return domain.Provider{}, false
	}
	return provider, true
}

func (s *Server) handleProviderScriptTest(w http.ResponseWriter, r *http.Request) {
	provider, ok := s.providerScriptProvider(w, r)
	if !ok {
		return
	}
	started := time.Now()
	updated, discovery, err := s.startAndDiscoverProviderScript(r.Context(), provider, false)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	model := strings.TrimSpace(updated.DefaultModel)
	if model == "" && len(discovery.Models) > 0 {
		model = strings.TrimSpace(discovery.Models[0].ID)
	}
	response := map[string]any{
		"success":        true,
		"providerId":     updated.ID,
		"defaultModel":   updated.DefaultModel,
		"models":         discovery.Models,
		"modelDiscovery": discovery,
		"scriptStatus":   providerScriptRuntimes.status(updated.ID),
	}
	if model == "" {
		// 健康检查和模型目录都成功时，没有默认模型仍然算“脚本可用”。测试接口
		// 不伪造一次带空 model 的推理请求，而是把需要用户处理的事项明确返回。
		response["warning"] = "脚本已启动，但 /v1/models 未返回可用模型；请实现模型目录或在 Provider 中填写默认模型"
		writeJSON(w, http.StatusOK, response)
		return
	}
	result, status := s.testProviderChat(r, updated.ID, providerChatTestRequest{Model: model, UserPrompt: "只回复 script-ok", MaxTokens: 64}, started)
	for key, value := range result {
		response[key] = value
	}
	writeJSON(w, status, response)
}

// startAndDiscoverProviderScript 统一处理“启动 → 健康检查 → /v1/models →
// 回填 DefaultModel”。创建、更新、手动启动和测试都复用这条链路，避免某个入口
// 启动了脚本却忘了同步模型目录。forceRestart 用于“重启”按钮。
func (s *Server) startAndDiscoverProviderScript(ctx context.Context, provider domain.Provider, forceRestart bool) (domain.Provider, providerTestResult, error) {
	var err error
	if forceRestart {
		err = providerScriptRuntimes.restart(ctx, provider)
	} else {
		err = providerScriptRuntimes.start(ctx, provider)
	}
	if err != nil {
		return provider, providerTestResult{}, fmt.Errorf("script start failed: %w", err)
	}
	if err := providerScriptRuntimes.waitHealthy(ctx, provider, 15*time.Second); err != nil {
		providerScriptRuntimes.stop(provider.ID)
		return provider, providerTestResult{}, fmt.Errorf("script health check failed: %w", err)
	}
	updated, result := s.discoverProviderScriptModels(ctx, provider)
	return updated, result, nil
}

// discoverProviderScriptModels 复用普通 Provider 的模型发现逻辑，因此脚本的
// /v1/models、认证头和模型窗口解析都与“获取模型”按钮完全一致。发现成功后立即
// 落盘；失败只记 warning，不把已经通过健康检查的脚本强行判成启动失败。
func (s *Server) discoverProviderScriptModels(ctx context.Context, provider domain.Provider) (domain.Provider, providerTestResult) {
	started := time.Now()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "/__providers/"+provider.ID+"/test", nil)
	if err != nil {
		return provider, providerTestResult{Success: false, Provider: provider.ID, LatencyMs: time.Since(started).Milliseconds(), Error: err.Error(), Models: []domain.Model{}}
	}
	result := s.fetchProviderModels(request, provider, started)
	if !result.Success || len(result.Models) == 0 {
		if s.logs != nil {
			s.logs.AddApp("warn", "provider script models not discovered", fmt.Sprintf("provider=%s: %s", provider.ID, firstNonEmpty(result.Error, "models endpoint returned no models")))
		}
		return provider, result
	}
	updated, err := s.router.UpdateProviderModels(provider.ID, result.Models, "healthy")
	if err != nil {
		if s.logs != nil {
			s.logs.AddApp("warn", "provider script models not saved", fmt.Sprintf("provider=%s: %v", provider.ID, err))
		}
		return provider, result
	}
	// 脚本更新后模型目录可能整体换了一代；旧的 DefaultModel 即使非空，只要
	// 已不在新目录里就必须改到新目录第一项，否则测试和真实请求会继续发一个
	// 已经下线的模型名。普通 Provider 的“手动默认值不覆盖”规则不在这里适用。
	defaultStillPresent := false
	for _, model := range result.Models {
		if strings.EqualFold(strings.TrimSpace(model.ID), strings.TrimSpace(updated.DefaultModel)) {
			defaultStillPresent = true
			break
		}
	}
	if !defaultStillPresent && len(result.Models) > 0 {
		if refreshed, defaultErr := s.router.SetProviderDefaultModel(provider.ID, result.Models[0].ID); defaultErr == nil {
			updated = refreshed
		} else if s.logs != nil {
			s.logs.AddApp("warn", "provider script default model not updated", fmt.Sprintf("provider=%s: %v", provider.ID, defaultErr))
		}
	}
	if err := s.saveState(); err != nil && s.logs != nil {
		s.logs.AddApp("warn", "provider script models not persisted", fmt.Sprintf("provider=%s: %v", provider.ID, err))
	}
	if s.logs != nil {
		s.logs.AddApp("info", "provider script models discovered", fmt.Sprintf("provider=%s models=%d", provider.ID, len(result.Models)))
	}
	return updated, result
}

// providerScriptRuntimeURL returns the loopback URL the gateway should put into
// the Provider.BaseURL after the script starts. A script Provider never points
// at the private vendor directly.
func providerScriptRuntimeURL(provider domain.Provider) string {
	return providerScriptRuntimes.apiBaseURL(provider)
}
