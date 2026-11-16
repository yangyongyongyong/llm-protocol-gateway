package gateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// ProviderScriptRuntime 只负责把用户提供的转换脚本当成一个本地 HTTP
// Connector 启动。它不解析、不改写脚本内部协议，也不把脚本塞进网关 Go 进程；
// 脚本至少对外暴露 openai_chat，网关再用现有协议转换器承接 Responses/Claude。
//
// 运行约束：
//   - 只允许 loopback，禁止脚本监听公网；
//   - 每个 Provider 独立工作目录；
//   - start/stop 幂等；
//   - 每次请求前检查进程与健康端点；
//   - 日志落文件，API 最多返回尾部窗口。
type ProviderScriptRuntime struct {
	mu        sync.Mutex
	processes map[string]*providerScriptProcess
	rootDir   string
}

type providerScriptProcess struct {
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	dir     string
	logPath string
	port    int
	started time.Time
	done    chan struct{}
}

var providerScriptRuntimes = newProviderScriptRuntime()

func newProviderScriptRuntime() *ProviderScriptRuntime {
	return &ProviderScriptRuntime{
		processes: map[string]*providerScriptProcess{},
		rootDir:   providerScriptBaseDir(),
	}
}

func providerScriptBaseDir() string {
	if root := strings.TrimSpace(os.Getenv("GATEWAY_PROVIDER_SCRIPT_DIR")); root != "" {
		return root
	}
	if configDir, err := os.UserConfigDir(); err == nil && strings.TrimSpace(configDir) != "" {
		return filepath.Join(configDir, "llm-protocol-gateway", "provider-runtime")
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".llm-protocol-gateway", "provider-runtime")
	}
	return filepath.Join(os.TempDir(), "llm-protocol-gateway-provider-runtime")
}

// providerScriptPythonCommand 允许部署时显式指定解释器；默认使用 PATH 中的
// python3。校验接口和真实启动必须走同一个选择逻辑，否则会出现“校验通过但
// 启动找不到解释器”的假阳性。
func providerScriptPythonCommand() string {
	if python := strings.TrimSpace(os.Getenv("GATEWAY_PROVIDER_SCRIPT_PYTHON")); python != "" {
		return python
	}
	return "python3"
}

func (r *ProviderScriptRuntime) providerDir(providerID string) string {
	return filepath.Join(r.rootDir, safeScriptDirName(providerID))
}

func safeScriptDirName(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "provider"
	}
	var b strings.Builder
	for _, ch := range id {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' {
			b.WriteRune(ch)
			continue
		}
		b.WriteByte('_')
	}
	return b.String()
}

func (r *ProviderScriptRuntime) ensureDir(provider domain.Provider) (string, error) {
	if provider.ProviderScript == nil {
		return "", fmt.Errorf("provider %q has no providerScript", provider.ID)
	}
	if strings.TrimSpace(provider.ProviderScript.Source) == "" {
		return "", fmt.Errorf("providerScript.source is required")
	}
	dir := r.providerDir(provider.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	entry := strings.TrimSpace(provider.ProviderScript.Entrypoint)
	if entry == "" {
		entry = "adapter.py"
	}
	if filepath.Base(entry) != entry || strings.Contains(entry, "..") {
		return "", fmt.Errorf("providerScript.entrypoint must be a plain filename")
	}
	if err := os.WriteFile(filepath.Join(dir, entry), []byte(provider.ProviderScript.Source), 0o600); err != nil {
		return "", err
	}
	if req := strings.TrimSpace(provider.ProviderScript.Requirements); req != "" {
		if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte(req), 0o600); err != nil {
			return "", err
		}
	} else {
		// 用户清空依赖后，旧 requirements.txt 不应继续留在工作目录里误导排查。
		_ = os.Remove(filepath.Join(dir, "requirements.txt"))
	}
	return dir, nil
}

func (r *ProviderScriptRuntime) start(ctx context.Context, provider domain.Provider) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if p := r.processes[provider.ID]; p != nil && providerScriptProcessRunning(p) {
		return nil
	}
	script := provider.ProviderScript
	if script == nil {
		return fmt.Errorf("provider %q has no providerScript", provider.ID)
	}
	dir, err := r.ensureDir(provider)
	if err != nil {
		return err
	}
	port := script.ListenPort
	if port <= 0 {
		port = providerScriptPort(provider.ID)
	}
	if !providerScriptPortAvailable(port) {
		return fmt.Errorf("providerScript port %d is already in use; stop the old script or choose another provider id", port)
	}
	entry := strings.TrimSpace(script.Entrypoint)
	if entry == "" {
		entry = "adapter.py"
	}
	command, args, err := providerScriptCommand(script, entry)
	if err != nil {
		return err
	}
	// 脚本进程的生命周期属于 Provider，不属于触发启动的 HTTP 请求；使用
	// Background context，避免 handler 返回时把刚启动的脚本一起取消。
	runCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(runCtx, command, args...)
	cmd.Dir = dir
	scriptEnv := []string{
		"PROVIDER_SCRIPT_PORT=" + strconv.Itoa(port),
		"PROVIDER_SCRIPT_PROVIDER_ID=" + provider.ID,
		"PROVIDER_SCRIPT_PROTOCOL=" + string(provider.Protocol),
		"PROVIDER_SCRIPT_HEALTH_PATH=" + firstNonEmpty(script.HealthPath, "/health"),
		"PROVIDER_SCRIPT_MAX_CONCURRENCY=" + strconv.Itoa(max(script.MaxConcurrentRequests, 0)),
		"PROVIDER_SCRIPT_REQUEST_TIMEOUT_SEC=" + strconv.Itoa(max(script.RequestTimeoutSec, 0)),
		"PROVIDER_SCRIPT_ENTRYPOINT=" + entry,
		"UPSTREAM_BASE_URL=" + strings.TrimSpace(script.UpstreamBaseURL),
		"UPSTREAM_API_KEY=" + resolveProviderAuth(provider),
		"UPSTREAM_AUTH_HEADER=" + firstNonEmpty(provider.AuthHeader, "Authorization"),
	}
	cmd.Env = append(os.Environ(), scriptEnv...)
	logPath := filepath.Join(dir, "adapter.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		cancel()
		return err
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		cancel()
		return err
	}
	done := make(chan struct{})
	r.processes[provider.ID] = &providerScriptProcess{
		cmd: cmd, cancel: cancel, dir: dir, logPath: logPath, port: port, started: time.Now(), done: done,
	}
	go func() {
		_ = cmd.Wait()
		_ = logFile.Close()
		close(done)
		r.mu.Lock()
		if current := r.processes[provider.ID]; current != nil && current.cmd == cmd {
			delete(r.processes, provider.ID)
		}
		r.mu.Unlock()
	}()
	return nil
}

// providerScriptCommand 把 StartCommand 统一解析成“可执行文件 + 参数”。
// StartCommand 可写 python3、python3 -u，或用 {entrypoint} 占位符；没有占位符
// 时会自动把入口文件追加到最后。首版 UI 不暴露该字段，保留给 CLI/API 调试。
func providerScriptCommand(script *domain.ProviderScript, entry string) (string, []string, error) {
	raw := strings.TrimSpace(script.StartCommand)
	if raw != "" {
		fields := strings.Fields(raw)
		if len(fields) == 0 {
			return "", nil, fmt.Errorf("providerScript.startCommand is empty")
		}
		args := make([]string, 0, len(fields))
		hasEntry := false
		for _, field := range fields[1:] {
			if strings.Contains(field, "{entrypoint}") {
				field = strings.ReplaceAll(field, "{entrypoint}", entry)
				hasEntry = true
			} else if filepath.Base(field) == entry {
				// 用户直接写 python3 -u adapter.py 时不要再追加一次入口文件。
				hasEntry = true
			}
			args = append(args, field)
		}
		if !hasEntry {
			args = append(args, entry)
		}
		return fields[0], args, nil
	}
	switch strings.ToLower(strings.TrimSpace(script.Language)) {
	case "", "python", "python3":
		return providerScriptPythonCommand(), []string{entry}, nil
	default:
		return "", nil, fmt.Errorf("providerScript.language %q is not supported", script.Language)
	}
}

// StartProviderScripts 在网关启动时拉起所有已保存的脚本 Provider。失败时
// 保留数据库中的脚本，方便用户在 Provider 编辑页查看日志并修复。
func (s *Server) StartProviderScripts() {
	// 启动时把所有自定义类型脚本补齐到 App 数据目录。这样即使用户手动删了
	// 目录、或者从旧版本只迁移了数据库，运行前也会重新落盘一份可审计文件。
	for _, custom := range s.router.CustomProviders() {
		if _, err := writeCustomProviderScriptFiles(custom); err != nil {
			s.logs.AddApp("warn", "custom provider script file write failed", fmt.Sprintf("custom=%s: %v", custom.ID, err))
		}
	}
	for _, provider := range s.router.State().Providers {
		if provider.ProviderScript == nil {
			continue
		}
		p := provider
		go func() {
			if err := providerScriptRuntimes.start(context.Background(), p); err != nil {
				s.logs.AddApp("warn", "provider script restore start failed", fmt.Sprintf("provider=%s: %v", p.ID, err))
				return
			}
			if err := providerScriptRuntimes.waitHealthy(context.Background(), p, 15*time.Second); err != nil {
				s.logs.AddApp("warn", "provider script restore unhealthy", fmt.Sprintf("provider=%s: %v", p.ID, err))
				return
			}
			s.logs.AddApp("info", "provider script restored", p.ID)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			s.autoFetchProviderModels(ctx, p)
		}()
	}
}

// stopAll 在网关退出时回收所有脚本子进程，避免它们成为孤儿继续监听端口。
func (r *ProviderScriptRuntime) stopAll() {
	r.mu.Lock()
	ids := make([]string, 0, len(r.processes))
	for id := range r.processes {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	for _, id := range ids {
		r.stop(id)
	}
}

func (r *ProviderScriptRuntime) stop(providerID string) {
	r.mu.Lock()
	p := r.processes[providerID]
	delete(r.processes, providerID)
	r.mu.Unlock()
	if p == nil {
		return
	}
	if p.cmd != nil && p.cmd.Process != nil {
		// 先发 Ctrl-C/SIGINT，让脚本有机会停止接收新请求并释放上游连接；
		// 2 秒内不退出再由 cancel（CommandContext 默认 Kill）强制结束。
		_ = p.cmd.Process.Signal(os.Interrupt)
	}
	if p.done != nil {
		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
		}
	}
	if p.cancel != nil {
		p.cancel()
	}
	if p.done != nil {
		select {
		case <-p.done:
		case <-time.After(time.Second):
		}
	}
}

func (r *ProviderScriptRuntime) status(providerID string) map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.processes[providerID]
	logPath := filepath.Join(r.providerDir(providerID), "adapter.log")
	if p == nil || !providerScriptProcessRunning(p) {
		return map[string]any{"running": false, "logPath": logPath}
	}
	return map[string]any{
		"running":   true,
		"pid":       p.cmd.Process.Pid,
		"port":      p.port,
		"startedAt": p.started.Format(time.RFC3339),
		"logPath":   p.logPath,
	}
}

// providerScriptProcessRunning 通过 done channel 判断进程是否已经退出，避免
// 并发读取 exec.Cmd.ProcessState 造成数据竞争。
func providerScriptProcessRunning(p *providerScriptProcess) bool {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return false
	}
	if p.done == nil {
		return true
	}
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// providerScriptPortAvailable 在启动前探一次 127.0.0.1，给端口冲突一个明确
// 的错误。只检查 loopback，因为脚本禁止监听公网。
func providerScriptPortAvailable(port int) bool {
	listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}

// restart 先停旧进程再启动新配置。更新脚本时必须走这条路径，否则 Provider
// 数据库已经指向新代码，实际运行的仍是旧进程。
func (r *ProviderScriptRuntime) restart(ctx context.Context, provider domain.Provider) error {
	r.stop(provider.ID)
	return r.start(ctx, provider)
}

// providerScriptRuntimeConfigChanged 判断一次 PATCH 是否需要重启脚本。只比较
// 会注入进程的环境与脚本正文，改名称、分组等元数据不打断正在飞行的请求。
func providerScriptRuntimeConfigChanged(before, after domain.Provider) bool {
	if (before.ProviderScript == nil) != (after.ProviderScript == nil) {
		return true
	}
	if before.ProviderScript != nil && !reflect.DeepEqual(before.ProviderScript, after.ProviderScript) {
		return true
	}
	return before.APIKeySource != after.APIKeySource ||
		before.AuthHeader != after.AuthHeader ||
		before.Protocol != after.Protocol
}

func (r *ProviderScriptRuntime) baseURL(provider domain.Provider) string {
	if provider.ProviderScript == nil {
		return ""
	}
	port := provider.ProviderScript.ListenPort
	if port <= 0 {
		port = providerScriptPort(provider.ID)
	}
	return "http://127.0.0.1:" + strconv.Itoa(port)
}

// apiBaseURL 是 Provider.BaseURL 的值。脚本的 /health 在根路径，标准模型和
// 推理端点统一挂在 /v1 下，因此 Provider.BaseURL 指向 /v1，而健康检查使用
// baseURL + HealthPath。
func (r *ProviderScriptRuntime) apiBaseURL(provider domain.Provider) string {
	root := r.baseURL(provider)
	if root == "" {
		return ""
	}
	return root + "/v1"
}

// client 返回脚本 Provider 用的 HTTP 客户端。脚本永远走 loopback，不继承
// 用户浏览器代理；超时取请求级设置，0 表示交给上游流式读取的总超时控制。
func (r *ProviderScriptRuntime) client() *http.Client {
	return &http.Client{Timeout: 0, Transport: &http.Transport{
		Proxy:               nil,
		MaxIdleConns:        32,
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     90 * time.Second,
	}}
}

func providerScriptPort(providerID string) int {
	// 固定派生端口，避免重启后 baseUrl 抖动；范围避开常见开发端口。
	h := 0
	for _, ch := range providerID {
		h = h*31 + int(ch)
	}
	if h < 0 {
		h = -h
	}
	return 19000 + h%1000
}

func (r *ProviderScriptRuntime) waitHealthy(ctx context.Context, provider domain.Provider, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := probeProviderScriptHealth(ctx, r.baseURL(provider), provider.ProviderScript.HealthPath); err == nil {
			return nil
		} else {
			last = err
		}
		// 进程已经退出时不必等满超时：把 adapter.log 末尾带上。否则调用方
		// （以及 CI）只能看到一个没有上下文的 "context deadline exceeded"，
		// 既不知道脚本是崩了、解释器找不到，还是根本没监听。
		if !r.running(provider.ID) {
			if tail := r.logTail(provider.ID); tail != "" {
				return fmt.Errorf("provider script exited before becoming healthy: %w; adapter.log:\n%s", last, tail)
			}
			return fmt.Errorf("provider script exited before becoming healthy: %w", last)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if last == nil {
		last = errors.New("health check timed out")
	}
	if tail := r.logTail(provider.ID); tail != "" {
		return fmt.Errorf("%w; adapter.log:\n%s", last, tail)
	}
	return last
}

// running 报告这个 provider 的脚本进程是否还活着。进程退出后 goroutine 会把
// 条目从 processes 里删掉，所以这里同时覆盖「从没启动」和「已经退出」。
func (r *ProviderScriptRuntime) running(providerID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return providerScriptProcessRunning(r.processes[providerID])
}

// logTail 取 adapter.log 的末尾一段，供健康检查失败时定位原因。
func (r *ProviderScriptRuntime) logTail(providerID string) string {
	data, err := os.ReadFile(filepath.Join(r.providerDir(providerID), "adapter.log"))
	if err != nil || len(data) == 0 {
		return ""
	}
	const maxTail = 2000
	if len(data) > maxTail {
		data = data[len(data)-maxTail:]
	}
	return strings.TrimSpace(string(data))
}

func probeProviderScriptHealth(ctx context.Context, baseURL, healthPath string) error {
	if strings.TrimSpace(healthPath) == "" {
		healthPath = "/health"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+healthPath, nil)
	if err != nil {
		return err
	}
	// 脚本永远在 127.0.0.1 上：绝不能走环境里的 HTTP 代理（CI 与内网常见）。
	// 否则请求被代理接走，本地脚本的健康检查永远等不到响应。
	client := &http.Client{
		Timeout:   2 * time.Second,
		Transport: &http.Transport{Proxy: nil},
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("health status %d", resp.StatusCode)
	}
	return nil
}

// StopProviderScriptRuntimes 是应用退出时调用的导出包装。
func StopProviderScriptRuntimes() {
	providerScriptRuntimes.stopAll()
}
