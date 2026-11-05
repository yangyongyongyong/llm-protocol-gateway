package gateway

import "time"

// QoderCOSYHeadersForProbe 暴露 COSY 签名给本地分析工具使用（cmd/qoderprobe）。
// wireBody 为 POST 的线上 body（GET 传空串）；它参与签名，必须与实际发送体一致。
func QoderCOSYHeadersForProbe(rawURL, uid, token, machineID, wireBody string, ts int64) (map[string]string, error) {
	if ts == 0 {
		ts = time.Now().Unix()
	}
	return qoderCOSYHeaders(rawURL, qoderAccount{
		UID:       uid,
		Token:     token,
		MachineID: machineID,
	}, wireBody, ts)
}
