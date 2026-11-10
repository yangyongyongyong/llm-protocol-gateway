//go:build windows

package gatewayid

import (
	"golang.org/x/sys/windows/registry"
)

// readMachineID 在 Windows 上读注册表的 MachineGuid。
//
// 它是系统安装时写入、此后不变的 GUID，正是"同一台电脑重装 App 身份要一样"
// 需要的那个值。读不到（受限权限、精简系统）返回空串，由调用方回退。
func readMachineID() string {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return ""
	}
	defer key.Close()
	value, _, err := key.GetStringValue("MachineGuid")
	if err != nil {
		return ""
	}
	return value
}
