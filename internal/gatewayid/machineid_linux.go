//go:build linux

package gatewayid

// readMachineID 在 Linux 上读 /etc/machine-id。
//
// 它是 systemd 首次启动时生成并此后不变的 32 位十六进制串，容器镜像里通常
// 会被清空或每次重建——那种情况返回空串，由调用方退化成库里的随机值。
//
// 备选的 /var/lib/dbus/machine-id 是老发行版的位置，两者内容相同。
func readMachineID() string {
	if id := readFileString("/etc/machine-id"); id != "" {
		return id
	}
	if id := readFileString("/var/lib/dbus/machine-id"); id != "" {
		return id
	}
	return readFileString("/sys/class/dmi/id/product_uuid")
}
