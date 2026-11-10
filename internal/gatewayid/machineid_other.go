//go:build !darwin && !linux && !windows

package gatewayid

// readMachineID 在没有专门实现的平台上返回空串，让调用方退化成库里的
// 随机值。宁可退化，也不要在这里猜一个"大概稳定"的值：猜错的代价是重装
// 后身份悄悄变了，而上游已经按旧身份授权过。
func readMachineID() string {
	return ""
}
