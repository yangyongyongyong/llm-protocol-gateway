package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/gatewayid"
)

// settingGatewayID 是"读不到机器标识"时退化存下来的随机身份。
//
// 它只在 GatewayIDPersisted 这条退路上用：容器和受限权限下读不到机器标识，
// 那就只能存一个随机值。存下来至少让数据目录还在时身份不变——虽然清库就会
// 换，界面上会把这件事说出来（见 GatewayIdentity.MachineIDReadable）。
const settingGatewayID = "gatewayId"
const settingGatewayIDSource = "gatewayIdSource"
const settingGatewayName = "gatewayName"

// GatewayIdentity 是 /__state 里给界面看的那部分身份信息。
type GatewayIdentity struct {
	ID string `json:"id,omitempty"`
	// Name 是人在界面上看到的名字，默认取主机名，可改。
	Name string `json:"name,omitempty"`
	// Source 是 machine（从机器标识派生）或 persisted（库里存的随机值）。
	Source string `json:"source,omitempty"`
	// MachineIDReadable 为 false 时重装会换身份，界面必须提示。
	MachineIDReadable bool `json:"machineIdReadable"`
}

// SetGatewayID 注入本网关身份，启动时由 runtime 调一次。
func (s *Server) SetGatewayID(id string) {
	if s == nil {
		return
	}
	s.gatewayID = strings.TrimSpace(id)
}

// SetGatewayIdentity 注入完整身份（含来源与是否派生于机器标识）。
//
// 和 SetGatewayID 分开是因为两者用途不同：环路检测只要 ID，而控制台要把
// "重装会不会换身份"这件事说出来——那需要 Source/MachineIDReadable。只调
// SetGatewayID 时���面按"来源未知"处理，不会误报成机器派生。
func (s *Server) SetGatewayIdentity(identity domain.GatewayIdentity) {
	if s == nil {
		return
	}
	id := strings.TrimSpace(identity.ID)
	if id == "" {
		return
	}
	identity.ID = id
	s.gatewayID = id
	s.gatewayIdentity = &identity
}

// GatewayIdentityForConsole 返回给控制台看的身份快照；未初始化时返回 nil，
// 界面据此隐藏父子对接入口，而不是显示一个空 ID 让人去复制。
func (s *Server) GatewayIdentityForConsole() *domain.GatewayIdentity {
	if s == nil {
		return nil
	}
	if s.gatewayIdentity != nil {
		copied := *s.gatewayIdentity
		return &copied
	}
	if id := strings.TrimSpace(s.gatewayID); id != "" {
		return &domain.GatewayIdentity{ID: id}
	}
	return nil
}

// GatewayID 返回本网关身份（空串表示尚未初始化）。
func (s *Server) GatewayID() string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(s.gatewayID)
}

// ResolveGatewayIdentity 算出本次启动用的身份。
//
// 顺序：能读到机器标识 → 从它派生（重装、清库都不变）；读不到 → 用库里已经
// 存下的随机值，没有就生成一个存进去（数据目录不变则身份不变）。两条路都
// 混入 salt——通常是数据目录的绝对路径——这样同一台机器上跑两套网关是两个
// 身份，环路检测不会把正常的互相转发误判成环。
func ResolveGatewayIdentity(store AdminAuthStore, salt string) GatewayIdentity {
	return resolveGatewayIdentity(gatewayid.ReadMachineID(), store, salt)
}

// resolveGatewayIdentity 是 ResolveGatewayIdentity 去掉读取机器标识之后的
// 纯决策部分。单独拆出来是为了能测"读不到机器标识"这条退化路径——真实环境
// 里几乎总读得到，靠 GATEWAY_MACHINE_ID 置空也只会回落到 /etc/machine-id，
// 测不到它，而这条路径恰恰是重装后身份会变的唯一来源。
func resolveGatewayIdentity(machineID string, store AdminAuthStore, salt string) GatewayIdentity {
	derived := gatewayid.Derive(machineID, salt)
	if derived.MachineIDReadable {
		return GatewayIdentity{
			ID:                derived.Value,
			Name:              storedGatewayName(store),
			Source:            string(derived.Source),
			MachineIDReadable: true,
		}
	}
	// 读不到机器标识：退化成持久化的随机值。
	persisted := ""
	if store != nil {
		persisted = strings.TrimSpace(store.Setting(settingGatewayID))
	}
	if persisted == "" {
		persisted = newRandomGatewaySeed()
		if store != nil {
			// 存失败也要继续跑：这一轮的身份仍然是稳定的随机值，只是重启后会
			// 变。让网关因为存不下一个 ID 而拒绝启动是更糟的取舍。
			_ = store.SetSetting(settingGatewayID, persisted)
			_ = store.SetSetting(settingGatewayIDSource, string(gatewayid.SourcePersisted))
		}
	}
	return GatewayIdentity{
		ID:                gatewayid.Derive(persisted, salt).Value,
		Name:              storedGatewayName(store),
		Source:            string(gatewayid.SourcePersisted),
		MachineIDReadable: false,
	}
}

func storedGatewayName(store AdminAuthStore) string {
	if store == nil {
		return ""
	}
	if name := strings.TrimSpace(store.Setting(settingGatewayName)); name != "" {
		return name
	}
	host, _ := os.Hostname()
	return strings.TrimSpace(host)
}

// SetGatewayName persists the user-visible name. The stable gateway ID does not
// change: peer registration and loop detection continue to use the ID, while
// the name is a mutable label shown on both sides.
func (s *Server) SetGatewayName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("本机名称不能为空")
	}
	if len([]rune(name)) > 80 {
		return fmt.Errorf("本机名称不能超过 80 个字符")
	}
	if s.gatewayIdentity != nil {
		s.gatewayIdentity.Name = name
	}
	if auth, ok := s.stateSaver.(AdminAuthStore); ok && auth != nil {
		if err := auth.SetSetting(settingGatewayName, name); err != nil {
			return err
		}
	}
	if s.adminAuth != nil {
		if err := s.adminAuth.SetSetting(settingGatewayName, name); err != nil {
			return err
		}
	}
	return nil
}

// GatewayName is the current display label used by local users and by the
// registration request sent to parent gateways.
func (s *Server) GatewayName() string {
	if s != nil && s.gatewayIdentity != nil {
		return strings.TrimSpace(s.gatewayIdentity.Name)
	}
	return ""
}

// newRandomGatewaySeed 生成一个 128 bit 的随机种子，只在退化路径上用。
func newRandomGatewaySeed() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand 不可用是灾难性的，但网关其余部分仍能工作；返回空串
		// 会让身份派生失败，所以退回到时间无关的常量级兜底没有意义——
		// 这里直接 panic 的替代是给出一个明显不是真随机的值，调用方在
		// 界面上一眼能看出异常。
		return "fallback-" + strings.Repeat("0", 16)
	}
	return hex.EncodeToString(buf)
}
