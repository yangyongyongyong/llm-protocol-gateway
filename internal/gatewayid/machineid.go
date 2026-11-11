package gatewayid

import (
	"os"
	"strings"
)

// ReadMachineID 按平台读取一个稳定的本机标识。
//
// 返回值必须是"同一台机器上恒定、重装系统之外不变"的值；读不到就返回空串，
// 由调用方退化成持久化随机值。绝不返回一个会随进程/时间变化的东西（比如
// 用临时主机名拼出来的串）——那种值会让重装后的网关变成另一台机器。
//
// GATEWAY_MACHINE_ID 优先级最高：容器里 /etc/machine-id 常常是宿主机的，
// 自建部署也可能需要显式指定；测试同样靠它注入一个确定值。
//
// 各平台的实现分别在 machineid_<goos>.go 里。
func ReadMachineID() string {
	raw := strings.TrimSpace(os.Getenv("GATEWAY_MACHINE_ID"))
	if raw == "" {
		raw = readMachineID()
	}
	return normalizeMachineID(raw)
}

// normalizeMachineID 统一各平台读回来的原始文本：去空白、转小写。
//
// 小写是必要的：macOS 的 IOPlatformUUID 大写小写都出现过，而派生是按字节
// 求哈希的，大小写不同就是两个网关 ID——同一台机器升一次系统就"换了身份"。
func normalizeMachineID(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// readFileString 读取一个文件的全部内容并去掉首尾空白。
//
// 权限不足、文件不存在都返回空串：读机器标识失败不该让网关起不来，退化
// 成库里的随机值就好。
func readFileString(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}
