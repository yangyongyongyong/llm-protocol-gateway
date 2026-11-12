package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// Key 级的等待窗口覆盖 Provider 级的：调用方最清楚自己这把钥匙要等多久，
// 同一个 Provider 既可能服务秒回的聊天，也可能服务先想两分钟的模型。
func TestEffectiveFirstPacketTimeoutKeyOverridesProvider(t *testing.T) {
	t.Parallel()
	provider := domain.Provider{FirstPacketTimeoutSec: 30}

	if got := effectiveFirstPacketTimeout(context.Background(), provider); got != 30*time.Second {
		t.Fatalf("没有 Key 设置时应当用 Provider 的：%v", got)
	}

	ctx := withAPIKeyFirstPacketTimeout(context.Background(), 240*time.Second)
	if got := effectiveFirstPacketTimeout(ctx, provider); got != 240*time.Second {
		t.Fatalf("Key 设置应当覆盖 Provider：%v", got)
	}
}

// 没设 Key 窗口时不能把 Provider 的值顶掉。
func TestEffectiveFirstPacketTimeoutFallsBackToProvider(t *testing.T) {
	t.Parallel()
	provider := domain.Provider{FirstPacketTimeoutSec: 45}
	// 0 值的 context 注入是空操作（withAPIKeyFirstPacketTimeout 对 <=0 直接返回原 ctx）。
	ctx := withAPIKeyFirstPacketTimeout(context.Background(), 0)
	if got := effectiveFirstPacketTimeout(ctx, provider); got != 45*time.Second {
		t.Fatalf("Key 没设时应当回落到 Provider：%v", got)
	}
}

// 越界值收敛到允许区间，0 保持「不设」。
func TestClampFirstPacketTimeoutSec(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want int }{
		{0, 0}, {-5, 0}, {30, 30}, {maxFirstPacketTimeoutSec + 500, maxFirstPacketTimeoutSec},
	}
	for _, tc := range cases {
		if got := clampFirstPacketTimeoutSec(tc.in); got != tc.want {
			t.Fatalf("clampFirstPacketTimeoutSec(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
