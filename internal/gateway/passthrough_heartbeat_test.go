package gateway

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// 转发的 SSE 流在「上游静默」期间必须把心跳真的送到客户端。passThroughOpeningWriter
// 是 passThroughOpeningWriter → sseHeartbeatResponseWriter → 客户端 这条链上的底层
// 包装，却唯独它没有 Flush：心跳被写进了它的缓冲就再没出去，静默的思考阶段客户端
// 什么都收不到，Cloudflare 这类边缘代理的 100 秒空闲限制会直接把这一轮掐掉。
func TestPassthroughHeartbeatReachesTheClient(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	opening := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"model\":\"x\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"
	response := &http.Response{
		StatusCode:    http.StatusOK,
		ContentLength: -1,
		Header:        http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:          &silentUntilReleased{opening: []byte(opening), release: release},
	}
	recorder := newSyncRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _, _ = writePassThroughResponse(recorder, response, true, usageStreamClaude, ParseClaudeUsage, claudeStreamInterrupter{})
	}()

	// 等到超过一个心跳间隔：上游一直不出声，客户端这段时间只可能收到心跳。
	deadline := time.Now().Add(sseHeartbeatInterval + 2*time.Second)
	for time.Now().Before(deadline) {
		flushes, body := recorder.snapshot()
		if flushes > 0 && strings.Contains(body, ": ping") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	flushes, body := recorder.snapshot()
	t.Fatalf("during %s of upstream silence the client received no keep-alive: flushes=%d body=%q",
		sseHeartbeatInterval, flushes, body)
}

// silentUntilReleased hands out its opening bytes and then blocks until release
// is closed — an upstream that opened the stream and went quiet.
type silentUntilReleased struct {
	opening []byte
	release chan struct{}
}

func (s *silentUntilReleased) Read(p []byte) (int, error) {
	if len(s.opening) > 0 {
		n := copy(p, s.opening)
		s.opening = s.opening[n:]
		return n, nil
	}
	<-s.release
	return 0, io.EOF
}

func (s *silentUntilReleased) Close() error { return nil }

// 客户端在「上游刚开口就静默」的阶段断开时，这次请求持有的并发槽位必须很快放掉：
// 心跳真的发到已断开的连接上会写失败，读循环随即结束、defer 里的 release 跑掉。
// 心跳不出去（见上面的测试）时这里只能等上游自己结束或 600 秒空闲守卫，同一账号
// （maxConcurrentRequests=1）后续请求全被 429。
func TestClientGoneReleasesTheProviderSlotQuickly(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"model\":\"claude-x\",\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}\n\n")
		w.(http.Flusher).Flush()
		started <- struct{}{}
		select { // 静默
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	defer close(release)

	state := domain.GatewayState{
		Providers: []domain.Provider{{
			ID: "p1", Name: "P1", Protocol: domain.ProtocolClaude, AuthType: domain.AuthTypeAPIKey,
			BaseURL: upstream.URL, APIKeySource: "sk-upstream-secret", MaxConcurrentRequests: 1, QueueWaitSeconds: 0,
		}},
		Models:  []domain.Model{{ID: "claude-x", ProviderID: "p1", InMenu: true}},
		Routes:  []domain.Route{{ID: "r1", Name: "R1", ProviderID: "p1", OutputProtocol: domain.ProtocolClaude, Enabled: true}},
		APIKeys: []domain.APIKey{{ID: "k1", Name: "K1", Key: "gw-test-key-1", RouteID: "r1", Enabled: true, StreamEnabled: true}},
	}
	server := NewServer(NewRouter(state), monitor.NewStore())
	front := httptest.NewServer(server.Handler())
	defer front.Close()

	body := `{"model":"claude-x","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	conn, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprintf(conn, "POST /anthropic/v1/messages HTTP/1.1\r\nHost: gateway.example.com\r\nContent-Type: application/json\r\nX-Api-Key: gw-test-key-1\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the first request never reached the upstream")
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _ = bufio.NewReader(conn).ReadString('\n') // the response is on the wire
	_ = conn.Close()

	// 一个心跳间隔之后（写入已失败、处理函数已返回）再发第二个请求：等待时间远小于
	// 上游静默时的 600 秒空闲守卫，所以只有「断开被及时察觉」才可能拿到回答。
	time.Sleep(sseHeartbeatInterval + 2*time.Second)
	request, _ := http.NewRequest(http.MethodPost, front.URL+"/anthropic/v1/messages", strings.NewReader(body))
	request.Host = "gateway.example.com"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Api-Key", "gw-test-key-1")
	client := &http.Client{Timeout: 4 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("the maxConcurrentRequests=1 account answered nothing 4s after the first client left: the slot is still held by a handler parked in the silent upstream read (%v)", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusTooManyRequests {
		t.Fatalf("the maxConcurrentRequests=1 account refused the next request with 429: the slot is still held by a handler parked in the silent upstream read")
	}
}
