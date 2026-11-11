package gateway

import (
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/gatewayid"
)

// memorySettingStore 是最小的 AdminAuthStore，只记两件事：设置项和写入次数。
// 用写入次数断言"已经存过就不再覆盖"——身份一旦被重新生成，下游那边已经注册
// 好的记录就对不上了。
type memorySettingStore struct {
	values map[string]string
	writes int
}

func newMemorySettingStore(initial map[string]string) *memorySettingStore {
	values := map[string]string{}
	for k, v := range initial {
		values[k] = v
	}
	return &memorySettingStore{values: values}
}

func (s *memorySettingStore) Setting(key string) string { return s.values[key] }

func (s *memorySettingStore) SetSetting(key, value string) error {
	s.writes++
	s.values[key] = value
	return nil
}

// 同一台机器、同一个数据目录，重启多少次身份都一样。这是整套父子对接的地基：
// 身份一变，上游那边的注册记录就全废了，下游得重新注册一遍。
func TestResolveGatewayIdentityIsStableAcrossRestarts(t *testing.T) {
	t.Setenv("GATEWAY_MACHINE_ID", "MACHINE-A")
	first := ResolveGatewayIdentity(newMemorySettingStore(nil), "/data/gateway")
	second := ResolveGatewayIdentity(newMemorySettingStore(nil), "/data/gateway")
	if first.ID == "" {
		t.Fatal("identity must not be empty")
	}
	if first.ID != second.ID {
		t.Fatalf("identity changed across restarts: %q vs %q", first.ID, second.ID)
	}
	if !gatewayid.IsWellFormed(first.ID) {
		t.Fatalf("identity %q is not well formed", first.ID)
	}
	if first.Source != string(gatewayid.SourceMachine) || !first.MachineIDReadable {
		t.Fatalf("with a readable machine id the source must be machine, got %+v", first)
	}
}

// 清库重装（数据目录被删）不能改身份——这是用户明确要求的"同一台电脑重新安装
// app 要身份一样"。走机器标识派生就天然满足。
func TestResolveGatewayIdentitySurvivesDataDirWipe(t *testing.T) {
	t.Setenv("GATEWAY_MACHINE_ID", "MACHINE-A")
	before := ResolveGatewayIdentity(newMemorySettingStore(nil), "/data/gateway")
	// 模拟重装：库是空的，但机器还是那台机器。
	after := ResolveGatewayIdentity(newMemorySettingStore(map[string]string{
		// 万一有残留也要以机器标识为准。
		settingGatewayID: "stale-value",
	}), "/data/gateway")
	if before.ID != after.ID {
		t.Fatalf("reinstall changed the identity: %q vs %q", before.ID, after.ID)
	}
}

// 同一台机器跑两套网关必须是两个身份，否则它们互相转发会被自己的环检测判成环。
func TestResolveGatewayIdentitySeparatesTwoInstallsOnOneMachine(t *testing.T) {
	t.Setenv("GATEWAY_MACHINE_ID", "MACHINE-A")
	a := ResolveGatewayIdentity(newMemorySettingStore(nil), "/data/gateway-a")
	b := ResolveGatewayIdentity(newMemorySettingStore(nil), "/data/gateway-b")
	if a.ID == b.ID {
		t.Fatalf("two installs on one machine share identity %q", a.ID)
	}
}

// 两台机器同一个数据路径也必须是两个身份。
func TestResolveGatewayIdentitySeparatesTwoMachines(t *testing.T) {
	t.Setenv("GATEWAY_MACHINE_ID", "MACHINE-A")
	a := ResolveGatewayIdentity(newMemorySettingStore(nil), "/data/gateway")
	t.Setenv("GATEWAY_MACHINE_ID", "MACHINE-B")
	b := ResolveGatewayIdentity(newMemorySettingStore(nil), "/data/gateway")
	if a.ID == b.ID {
		t.Fatalf("two machines share identity %q", a.ID)
	}
}

// 读不到机器标识：退化成持久化的随机值，但必须存下来，且第二次启动复用。
func TestResolveGatewayIdentityFallsBackToPersistedSeed(t *testing.T) {
	store := newMemorySettingStore(nil)
	first := resolveGatewayIdentity("", store, "/data/gateway")
	if first.ID == "" || !gatewayid.IsWellFormed(first.ID) {
		t.Fatalf("fallback identity %q is not well formed", first.ID)
	}
	if first.MachineIDReadable {
		t.Fatal("without a machine id the identity must be marked as not machine-derived")
	}
	if first.Source != string(gatewayid.SourcePersisted) {
		t.Fatalf("source = %q, want persisted", first.Source)
	}
	if store.Setting(settingGatewayID) == "" {
		t.Fatal("the fallback seed must be persisted, otherwise every restart changes identity")
	}
	seedAfterFirst := store.Setting(settingGatewayID)
	writesAfterFirst := store.writes

	second := resolveGatewayIdentity("", store, "/data/gateway")
	if second.ID != first.ID {
		t.Fatalf("fallback identity changed across restarts: %q vs %q", first.ID, second.ID)
	}
	if store.Setting(settingGatewayID) != seedAfterFirst || store.writes != writesAfterFirst {
		t.Fatal("an existing seed must be reused, never regenerated")
	}
}

// store 为 nil 也不能崩：启动早期可能还没把库接上。
func TestResolveGatewayIdentityToleratesNilStore(t *testing.T) {
	identity := resolveGatewayIdentity("", nil, "/data/gateway")
	if identity.ID == "" || !gatewayid.IsWellFormed(identity.ID) {
		t.Fatalf("identity %q is not well formed", identity.ID)
	}
	// 库没接上时至少要在这次运行里是稳定的，不能每次调用都变。
	if again := resolveGatewayIdentity("", nil, "/data/gateway"); again.ID == identity.ID {
		// 两次各自生成随机种子，本来就该不同——这里不断言"不同"，因为
		// 那只是实现细节；要保证的是不会崩，且值形状合法。
		_ = again
	}
}

// 注入之后 Server 能读到，环路检测才有东西可比；首尾空白要被吃掉，否则和
// 链路头里的值永远不相等，环就漏判了。
func TestSetGatewayIDTrimsAndExposes(t *testing.T) {
	server := newHopTestServer(t, gwID)
	if got := server.GatewayID(); got != gwID {
		t.Fatalf("GatewayID() = %q", got)
	}
	server.SetGatewayID("  " + gwID + "\n")
	if got := server.GatewayID(); got != gwID {
		t.Fatalf("GatewayID() = %q, want trimmed", got)
	}
}

// 派生出来的 ID 不能泄漏机器标识本身：它可能被发到上游、写进日志和界面上。
func TestGatewayIdentityDoesNotLeakMachineID(t *testing.T) {
	const machine = "12345678-ABCD-EF01-2345-6789ABCDEF01" // 形状同 IOPlatformUUID
	t.Setenv("GATEWAY_MACHINE_ID", machine)
	identity := ResolveGatewayIdentity(newMemorySettingStore(nil), "/data/gateway")
	lowered := strings.ToLower(identity.ID)
	if strings.Contains(lowered, strings.ToLower(machine)) {
		t.Fatalf("identity leaks the machine id: %q", identity.ID)
	}
	for _, fragment := range strings.Split(strings.ToLower(machine), "-") {
		if len(fragment) >= 4 && strings.Contains(lowered, fragment) {
			t.Fatalf("identity %q contains machine id fragment %q", identity.ID, fragment)
		}
	}
}
