package gateway

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateForLogEmpty(t *testing.T) {
	if got := truncateForLog(nil, 8); got != "" {
		t.Fatalf("nil => %q, want empty", got)
	}
	if got := truncateForLog([]byte{}, 8); got != "" {
		t.Fatalf("empty => %q, want empty", got)
	}
}

func TestTruncateForLogUnderLimit(t *testing.T) {
	data := []byte("hello")
	if got := truncateForLog(data, 8); got != "hello" {
		t.Fatalf("under limit => %q, want %q", got, "hello")
	}
	// 恰好等于上限时不截断。
	if got := truncateForLog(data, 5); got != "hello" {
		t.Fatalf("equal limit => %q, want %q", got, "hello")
	}
}

func TestTruncateForLogASCII(t *testing.T) {
	data := []byte("abcdefghij")
	got := truncateForLog(data, 4)
	if !strings.HasPrefix(got, "abcd") {
		t.Fatalf("ascii truncate => %q, want prefix abcd", got)
	}
	if !strings.HasSuffix(got, "(truncated)") {
		t.Fatalf("ascii truncate => %q, want truncated suffix", got)
	}
}

// 截断点落在多字节字符中间时，必须回退到 rune 边界，产出合法 UTF-8，
// 避免历史上出现过的 UTF-8/JSON 解析报错。
func TestTruncateForLogUTF8Boundary(t *testing.T) {
	// 每个中文字符占 3 字节。构造 5 个字符（15 字节）。
	data := []byte("你好世界啊")
	for limit := 1; limit < len(data); limit++ {
		got := truncateForLog(data, limit)
		body := strings.TrimSuffix(got, "…(truncated)")
		if !utf8.ValidString(body) {
			t.Fatalf("limit=%d produced invalid utf8: %q", limit, body)
		}
		// 回退后长度不应超过 limit。
		if len(body) > limit {
			t.Fatalf("limit=%d body len=%d exceeds limit", limit, len(body))
		}
	}
}

func TestTruncateForLogUTF8ExactRune(t *testing.T) {
	data := []byte("你好世界啊") // 15 bytes, 3 bytes each
	// limit=6 恰好落在第二个字符结尾，应保留「你好」完整。
	got := truncateForLog(data, 6)
	body := strings.TrimSuffix(got, "…(truncated)")
	if body != "你好" {
		t.Fatalf("limit=6 => %q, want 你好", body)
	}
}
