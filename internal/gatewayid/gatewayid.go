// Package gatewayid 派生本网关的稳定身份标识。
//
// 这个 ID 是父子网关功能的锚点：下游注册到上游时带着它，上游据此判断
// "这个下游是不是绕回了自己"（环路），运维也据此在两边对上同一台机器。
//
// 关键约束是稳定性：同一台电脑重装 App 之后必须还是同一个 ID，否则上游
// 那边会看到一台"全新的下游"，旧的授权、用量归属、环路祖先链全部对不上。
// 所以它不靠随机生成后存库——那种做法清一次数据目录就变了。这里改成从
// 机器标识和数据目录路径派生：机器不变、数据目录不变，ID 就不变。
//
// 加上数据目录是必要的：同一台机器上跑两套网关（不同 GATEWAY_CONFIG）
// 必须是两个身份，否则环路检测会把它们当成同一个节点，把正常的互相转发
// 误判成环。
package gatewayid

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// idPrefix 让这个值在日志和配置文件里一眼可辨。
const idPrefix = "gw_"

// deriveSalt 是派生时混入的固定串。改它等于让所有已部署网关换一次身份，
// 会把既有的父子授权全部打断，所以它必须是个常量。
const deriveSalt = "llm-protocol-gateway/peer-v1"

// idHexLen 是 ID 的十六进制长度：16 个 hex 字符 = 64 bit。对"一台机器上
// 有几个网关"这种规模来说碰撞概率可以忽略，而短到能在界面上一行放下。
const idHexLen = 16

// ID 是派生结果之外还要让调用方知道的两件事。
type ID struct {
	// Value 是 "gw_" 开头的稳定标识。
	Value string
	// Source 说明这个 ID 是怎么来的，只用于界面提示，不参与派生。
	Source Source
	// MachineIDReadable 为 false 时 Value 退化成数据库里存的随机值：
	// 这台机器读不到机器标识（容器、受限权限、不常见的系统），重装会换
	// 身份。界面必须把这件事说出来，否则出环路问题时无从查起。
	MachineIDReadable bool
}

// Source 是 ID 的来源，决定它有多稳定。
type Source string

const (
	// SourceMachine 表示从真机器标识派生：重装、清库、换版本都不变。
	SourceMachine Source = "machine"
	// SourcePersisted 表示读不到机器标识，用的是库里存的随机值：
	// 数据目录还在就不变，清掉就换。
	SourcePersisted Source = "persisted"
)

// Derive 从机器标识和一道"区分同机多实例"的盐派生网关 ID。
//
// machineID 为空会退化成 SourcePersisted：调用方此时必须自己生成并持久化
// 一个随机值，再把那个值当作 machineID 传回来。
func Derive(machineID, salt string) ID {
	machineID = strings.TrimSpace(machineID)
	if machineID == "" {
		return ID{Source: SourcePersisted, MachineIDReadable: false}
	}
	return ID{
		Value:             hashID(machineID, salt),
		Source:            SourceMachine,
		MachineIDReadable: true,
	}
}

// hashID 是派生本身：固定盐 + 机器标识 + 实例盐。
//
// 机器标识（macOS 的 IOPlatformUUID、Linux 的 /etc/machine-id、Windows 的
// MachineGuid）是可识别的设备指纹，绝不能原样发到上游。这里只发出 sha256
// 的前 16 个 hex 字符，既不泄露原值，也让上游算不出它——上游手里没有那串
// 机器标识。
//
// 大小写在派生内部统一：各平台读回来的原文大小写不一致，而这是按字节求
// 哈希的，不归一就会让同一台机器升级一次"换个身份"。
func hashID(machineID, salt string) string {
	sum := sha256.Sum256([]byte(deriveSalt + "\x00" + strings.ToLower(strings.TrimSpace(machineID)) + "\x00" + strings.TrimSpace(salt)))
	return idPrefix + hex.EncodeToString(sum[:])[:idHexLen]
}

// Fingerprint 把机器标识压成一个短摘要，用来在"数据目录还没变、机器标识
// 变了"（换主板、迁移虚拟机）时判断到底换没换机器。它不上行发送。
func Fingerprint(machineID string) string {
	machineID = strings.TrimSpace(machineID)
	if machineID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(machineID))
	return hex.EncodeToString(sum[:])[:8]
}

// IsWellFormed 校验一个值是不是本包生成的 ID，用于接收上游/下游传来的
// ID 时先行挡掉明显伪造的值。
func IsWellFormed(value string) bool {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, idPrefix) {
		return false
	}
	rest := strings.TrimPrefix(value, idPrefix)
	if len(rest) != idHexLen {
		return false
	}
	for _, r := range rest {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
