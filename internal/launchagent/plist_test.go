package launchagent

import (
	"os"
	"strings"
	"testing"
)

// 与 scripts/com.luca.llm-protocol-gateway.plist.template 渲染后同构。
const samplePlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.luca.llm-protocol-gateway</string>
  <key>ProgramArguments</key>
  <array>
    <string>/bin/bash</string>
    <string>/Users/luca/llm-protocol-gateway/scripts/run-gateway-service.sh</string>
  </array>
  <key>WorkingDirectory</key>
  <string>/Users/luca/llm-protocol-gateway</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>GATEWAY_ADDR</key>
    <string>0.0.0.0:18093</string>
    <key>PATH</key>
    <string>/usr/bin:/bin</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>/Users/luca/llm-protocol-gateway/.cache/gateway-launchd.out.log</string>
  <key>StandardErrorPath</key>
  <string>/Users/luca/llm-protocol-gateway/.cache/gateway-launchd.err.log</string>
  <key>ProcessType</key>
  <string>Background</string>
</dict>
</plist>
`

func TestParseReadsTheFieldsWeCleanBy(t *testing.T) {
	plist, err := Parse([]byte(samplePlist))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if plist.Label != "com.luca.llm-protocol-gateway" {
		t.Errorf("Label = %q", plist.Label)
	}
	want := []string{"/bin/bash", "/Users/luca/llm-protocol-gateway/scripts/run-gateway-service.sh"}
	if len(plist.ProgramArguments) != len(want) || plist.ProgramArguments[0] != want[0] || plist.ProgramArguments[1] != want[1] {
		t.Errorf("ProgramArguments = %v, want %v", plist.ProgramArguments, want)
	}
	// 这两个路径就是那 51MB 日志的来源，必须能读出来才好回收。
	if plist.StandardErrorPath != "/Users/luca/llm-protocol-gateway/.cache/gateway-launchd.err.log" {
		t.Errorf("StandardErrorPath = %q", plist.StandardErrorPath)
	}
	if plist.StandardOutPath != "/Users/luca/llm-protocol-gateway/.cache/gateway-launchd.out.log" {
		t.Errorf("StandardOutPath = %q", plist.StandardOutPath)
	}
}

// 嵌套的 dict / array / true 都不能把顶层解析带偏：EnvironmentVariables 里也有
// 一堆 key，读成顶层的 Label 就全错了。
func TestParseIgnoresNestedKeys(t *testing.T) {
	plist, err := Parse([]byte(samplePlist))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if strings.Contains(plist.Label, "18093") || plist.Label != "com.luca.llm-protocol-gateway" {
		t.Fatalf("nested EnvironmentVariables leaked into Label: %q", plist.Label)
	}
}

func TestMissingProgramDetectsADeletedScript(t *testing.T) {
	plist, err := Parse([]byte(samplePlist))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// 源码目录还在：不该判成坏的。
	allThere := func(string) bool { return true }
	if missing := plist.MissingProgram(allThere); missing != "" {
		t.Fatalf("a healthy plist must not be flagged, got %q", missing)
	}

	// 只少那个脚本：正是用户机器上的状况。
	gone := "/Users/luca/llm-protocol-gateway/scripts/run-gateway-service.sh"
	scriptGone := func(path string) bool { return path != gone }
	if missing := plist.MissingProgram(scriptGone); missing != gone {
		t.Fatalf("MissingProgram = %q, want %q", missing, gone)
	}
}

// 相对路径不参与判断：它相对 WorkingDirectory，猜错了会误删正常的 plist。
func TestMissingProgramSkipsRelativePaths(t *testing.T) {
	plist := Plist{ProgramArguments: []string{"gateway", "--serve"}}
	if missing := plist.MissingProgram(func(string) bool { return false }); missing != "" {
		t.Fatalf("relative args must be skipped, got %q", missing)
	}
}

func TestParseToleratesGarbage(t *testing.T) {
	// 坏文件不能把调用方吓死：已经读到的字段照常返回，清理流程按「没有可删的」
	// 走，而不是整个启动流程因为一个坏 plist 卡住。
	plist, err := Parse([]byte("not a plist at all"))
	if err != nil && plist.Label != "" {
		t.Fatalf("unexpected state: %+v %v", plist, err)
	}
	empty, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse(nil): %v", err)
	}
	if empty.Label != "" || len(empty.ProgramArguments) != 0 {
		t.Fatalf("empty input must parse to an empty plist: %+v", empty)
	}
}

// 真实文件里查到的那段 stderr 就是这么来的：launchd 直接 exec /bin/bash，
// bash 找不到脚本才打印它自己的一句 No such file or directory。
func TestMissingProgramMessageMatchesTheLog(t *testing.T) {
	plist, err := Parse([]byte(samplePlist))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	missing := plist.MissingProgram(func(path string) bool { return !strings.Contains(path, "run-gateway-service.sh") })
	if !strings.HasSuffix(missing, "run-gateway-service.sh") {
		t.Fatalf("missing = %q; the log line names this script", missing)
	}
	// 顺带确认我们没把 /bin/bash 当成缺失项（它一定存在，误判会删掉好 plist）。
	if _, err := os.Stat("/bin/bash"); err == nil && missing == "/bin/bash" {
		t.Fatal("/bin/bash must never be reported missing")
	}
}
