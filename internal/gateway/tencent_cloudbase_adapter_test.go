package gateway

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 内置适配器必须能读取上游各种缓存字段，并把它们写进 Responses / Claude 的
// usage。CloudBase SDK 的字段名不固定，丢掉任何一个都会让统计长期显示 0。
func TestBuiltinCloudBaseAdapterPreservesCacheUsage(t *testing.T) {
	python := "python3"
	if _, err := exec.LookPath(python); err != nil {
		t.Skip("python3 not available")
	}
	source := string(mustReadBuiltinCloudBaseAdapter(t))
	for _, snippet := range []string{
		"def cache_read_tokens",
		"prompt_cache_hit_tokens",
		"input_tokens_details",
		"cache_read_input_tokens",
		"cache_creation_input_tokens",
	} {
		if !strings.Contains(source, snippet) {
			t.Fatalf("builtin adapter is missing %q", snippet)
		}
	}
	script := `import importlib.util, json
spec = importlib.util.spec_from_file_location("adapter", "__ADAPTER_PATH__")
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
usage = {"prompt_tokens": 500, "completion_tokens": 7, "total_tokens": 507, "prompt_cache_hit_tokens": 400, "cache_creation_input_tokens": 20}
tool_call = {"id":"call_1","type":"function","function":{"name":"shell","arguments":json.dumps({"cmd":"pwd"})}}
completion = {"id":"c1","model":"m","choices":[{"message":{"role":"assistant","content":"checking","tool_calls":[tool_call]},"finish_reason":"tool_calls"}],"usage":usage}
responses = m.completion_to_response(completion, "m", {"max_output_tokens":64,"input":"hi","store":False}, [{"role":"user","content":"hi"}])
claude = m.completion_to_claude(completion, "m")
print(json.dumps({"responses_usage":responses["usage"],"claude_usage":claude["usage"],"phases":[x.get("phase") for x in responses["output"]]}))
`
	path := filepath.Join(t.TempDir(), "adapter.py")
	if err := writeTestFile(path, mustReadBuiltinCloudBaseAdapter(t)); err != nil {
		t.Fatal(err)
	}
	script = strings.ReplaceAll(script, "__ADAPTER_PATH__", path)
	cmd := exec.Command(python, "-c", script)
	cmd.Env = append(cmd.Environ(), "PROVIDER_SCRIPT_PORT=19999")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("adapter cache usage check failed: %v\n%s", err, out)
	}
	got := strings.TrimSpace(string(out))
	if !strings.Contains(got, `"cached_tokens": 400`) ||
		!strings.Contains(got, `"cache_read_input_tokens": 400`) ||
		!strings.Contains(got, `"input_tokens": 80`) {
		t.Fatalf("unexpected normalized usage: %s", got)
	}
	if !strings.Contains(got, `"commentary"`) {
		t.Fatalf("preamble before tool call must be commentary: %s", got)
	}
}

func mustReadBuiltinCloudBaseAdapter(t *testing.T) []byte {
	t.Helper()
	providers := builtinCustomProviders()
	if len(providers) != 1 {
		t.Fatalf("builtin providers = %d", len(providers))
	}
	return []byte(providers[0].Script.Source)
}

func writeTestFile(path string, body []byte) error {
	return os.WriteFile(path, body, 0o600)
}
