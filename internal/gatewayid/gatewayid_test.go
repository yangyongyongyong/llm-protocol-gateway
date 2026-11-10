package gatewayid

import (
	"strings"
	"testing"
)

// 同一台机器 + 同一个数据目录必须永远得到同一个 ID：这是"重装 App 身份不变"
// 的全部内容，其它行为都建立在这条之上。
func TestDeriveIsStableForTheSameInputs(t *testing.T) {
	first := Derive("4c4c4544-0044-3510-8051-b4c04f4e4d32", "/Users/luca/.config/llm-protocol-gateway")
	second := Derive("4c4c4544-0044-3510-8051-b4c04f4e4d32", "/Users/luca/.config/llm-protocol-gateway")
	if first.Value != second.Value {
		t.Fatalf("same machine+dir must derive the same id: %q vs %q", first.Value, second.Value)
	}
	if !IsWellFormed(first.Value) {
		t.Fatalf("derived id is not well formed: %q", first.Value)
	}
	if first.Source != SourceMachine || !first.MachineIDReadable {
		t.Fatalf("expected a machine-derived id, got %+v", first)
	}
}

// 同机两个数据目录 = 两个网关身份。合并的话环路检测会把正常的互相转发
// 误判成环，而这是父子网关最怕的一种误伤。
func TestDeriveSeparatesTwoInstallsOnOneMachine(t *testing.T) {
	a := Derive("machine-1", "/data/gateway-a")
	b := Derive("machine-1", "/data/gateway-b")
	if a.Value == b.Value {
		t.Fatal("two data dirs on one machine must not share a gateway id")
	}
}

// 两台机器、同一个数据目录，同样要分开：复制一份数据目录到别的机器上
// 不该冒充原来那台。
func TestDeriveSeparatesTwoMachinesWithTheSameDataDir(t *testing.T) {
	a := Derive("machine-1", "/data/gateway")
	b := Derive("machine-2", "/data/gateway")
	if a.Value == b.Value {
		t.Fatal("two machines with the same data dir must not share a gateway id")
	}
}

// 读不到机器标识时不能凭空造一个看起来正常的 ID：调用方要能看出这是
// 退化的（重装会变），界面才能把风险说出来。
func TestDeriveWithoutMachineIDIsMarkedDegraded(t *testing.T) {
	id := Derive("", "/data/gateway")
	if id.MachineIDReadable {
		t.Fatal("an empty machine id must not be reported as readable")
	}
	if id.Source != SourcePersisted {
		t.Fatalf("Source = %q, want %q", id.Source, SourcePersisted)
	}
	if id.Value != "" {
		t.Fatalf("Derive must not invent an id without a machine id, got %q", id.Value)
	}
}

// 派生结果里不能出现机器标识本身：它是设备指纹，会随请求发到上游。
func TestDeriveDoesNotLeakTheMachineID(t *testing.T) {
	machine := "4c4c4544-0044-3510-8051-b4c04f4e4d32"
	id := Derive(machine, "/data/gateway")
	if strings.Contains(id.Value, machine) {
		t.Fatalf("derived id leaks the machine id: %q", id.Value)
	}
	// 连片段都不能露：短 UUID 片段也足以在一份日志里对上设备。
	for _, part := range strings.SplitN(machine, "-", 2) {
		if len(part) >= 4 && strings.Contains(id.Value, part) {
			t.Fatalf("derived id leaks a machine-id fragment %q: %q", part, id.Value)
		}
	}
}

func TestIsWellFormedRejectsJunk(t *testing.T) {
	good := Derive("machine-1", "/data").Value
	cases := map[string]bool{
		good:                         true,
		"":                           false,
		"gw_":                        false,
		"gw_ABCDEF0123456789":        false, // 大写不是本包生成的
		"gw_abcdef012345678":         false, // 少一位
		"gw_abcdef01234567890":       false, // 多一位
		"xx_abcdef0123456789":        false,
		"gw_abcdef012345678z":        false, // 非十六进制
		"gw_abcdef0123456789\nextra": false,
		"  gw_abcdef0123456789  ":    true,
	}
	for input, want := range cases {
		if got := IsWellFormed(input); got != want {
			t.Errorf("IsWellFormed(%q) = %v, want %v", input, got, want)
		}
	}
}

// ReadMachineID 的环境变量优先级：容器和测试都需要能显式指定，而且大小写
// 必须归一，否则同一台机器升级一次就得换身份。
func TestReadMachineIDPrefersTheEnvAndNormalizes(t *testing.T) {
	t.Setenv("GATEWAY_MACHINE_ID", "  ABCD-1234  ")
	got := ReadMachineID()
	if got != "abcd-1234" {
		t.Fatalf("ReadMachineID = %q, want %q", got, "abcd-1234")
	}
	if a, b := Derive("ABCD-1234", "/data").Value, Derive("abcd-1234", "/data").Value; a != b {
		t.Fatalf("case must not change the derived id: %q vs %q", a, b)
	}
}

func TestFingerprintIsShortAndStable(t *testing.T) {
	fp := Fingerprint("abcd-1234")
	if len(fp) != 8 {
		t.Fatalf("Fingerprint = %q, want 8 hex chars", fp)
	}
	if Fingerprint("abcd-1234") != fp {
		t.Fatal("Fingerprint must be deterministic")
	}
	if Fingerprint("") != "" {
		t.Fatal("Fingerprint of an empty id must be empty")
	}
}
