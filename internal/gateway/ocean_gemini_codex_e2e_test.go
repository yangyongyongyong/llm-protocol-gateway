package gateway

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// TestOceanGeminiCodexToolRoundTrip 运行真实 Codex CLI，经隔离网关转发
// Gemini 生成的 Responses SSE，验证正文不会提前结束且工具结果进入下一轮。
// 仅在显式传入独立假代理地址时运行，不使用本机正在运行的网关或真实凭据。
func TestOceanGeminiCodexToolRoundTrip(t *testing.T) {
	base := strings.TrimSpace(os.Getenv("OCEAN_CODEX_FIXTURE_PROXY"))
	if base == "" {
		t.Skip("仅在显式设置 OCEAN_CODEX_FIXTURE_PROXY 时运行隔离 Codex 集成测试")
	}
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("本机未安装 codex CLI")
	}
	provider := domain.Provider{
		ID: "fixture-ocean", Name: "fixture ocean", Protocol: domain.ProtocolOpenAIResponses,
		AuthType: domain.AuthTypeAPIKey, BaseURL: base + "/v1/responses",
		APIKeySource: "literal:fixture-upstream", HealthStatus: "healthy",
	}
	route := domain.Route{ID: "fixture-route", Name: "fixture-route", ProviderID: provider.ID,
		OutputProtocol: domain.ProtocolOpenAIResponses, Enabled: true}
	key := domain.APIKey{ID: "fixture-key", Name: "fixture-key", Key: "sk-fixture-gateway",
		RouteID: route.ID, Enabled: true, StreamEnabled: true}
	router := NewRouter(domain.GatewayState{Providers: []domain.Provider{provider}, Routes: []domain.Route{route}, APIKeys: []domain.APIKey{key}})
	gateway := httptest.NewServer(NewServer(router, monitor.NewStore()).Handler())
	defer gateway.Close()

	dir := t.TempDir()
	home := filepath.Join(dir, "codex")
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	conf := fmt.Sprintf(`model = "gemini-4-hydrogen"
model_provider = "fixture"
approval_policy = "never"
sandbox_mode = "workspace-write"
[model_providers.fixture]
name = "fixture"
base_url = %q
wire_api = "responses"
env_key = "FIXTURE_GATEWAY_KEY"
`, gateway.URL+"/openai/v1")
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(codex, "exec", "--disable", "plugins", "--skip-git-repo-check", "--ephemeral", "--json", "-C", work,
		"Use the tool to print ISOLATED_CODEX_TOOL_OK, then say the final confirmation.")
	cmd.Stdin = strings.NewReader("")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+home, "HOME="+dir, "TERM=dumb", "FIXTURE_GATEWAY_KEY="+key.Key)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("隔离 Codex CLI 运行失败: %v\n%s", err, out)
	}
	var commandRan, final bool
	for _, line := range strings.Split(string(out), "\n") {
		var event struct {
			Type string `json:"type"`
			Item struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				ExitCode *int   `json:"exit_code"`
			} `json:"item"`
		}
		if json.Unmarshal([]byte(line), &event) != nil || event.Type != "item.completed" {
			continue
		}
		if event.Item.Type == "command_execution" && event.Item.ExitCode != nil && *event.Item.ExitCode == 0 {
			commandRan = true
		}
		if event.Item.Type == "agent_message" && event.Item.Text == "ISOLATED_CODEX_FINAL_OK" {
			final = true
		}
	}
	if !commandRan || !final {
		t.Fatalf("Codex 未完成工具与下一轮: command=%v final=%v\n%s", commandRan, final, out)
	}
}
