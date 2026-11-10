//go:build darwin

package gatewayid

import (
	"os/exec"
	"strings"
)

// readMachineID 在 macOS 上读 IOPlatformUUID——硬件 UUID，重装系统也不变，
// 正是"同一台电脑重装 App 身份要一样"需要的那个值。
//
// 不用 hardware serial：苹果不再可靠地返回它，而且它是更直接的设备身份标识。
// 也不用 hostname：用户可以随时改。
func readMachineID() string {
	out, err := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		return ""
	}
	return extractPlistStringValue(string(out), "IOPlatformUUID")
}

// extractPlistStringValue 从 ioreg 的文本输出里取一个 "键" = "值" 对。
//
// ioreg 的输出是类 plist 的纯文本，每家机器上键的顺序都不一样，所以必须按
// 键名取而不是按位置取。找不到就返回空串，交给调用方回退。
func extractPlistStringValue(output, key string) string {
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, `"`+key+`"`) {
			continue
		}
		eq := strings.Index(trimmed, "=")
		if eq < 0 {
			continue
		}
		value := strings.TrimSpace(trimmed[eq+1:])
		return strings.Trim(strings.TrimSpace(value), `"`)
	}
	return ""
}
