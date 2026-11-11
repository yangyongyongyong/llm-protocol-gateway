package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// 下游这一侧的客户端。
//
// 上游（父）提供 /__peer/*，本文件是它的对称面：本机（子）拿这些函数去注册、
// 探活、取 provider 清单、把请求送上去。抽成一个文件而不是散在 handler 里，
// 是因为「注册」和「转发」共享同一套头与错误约定，两处各写一遍必然会漂移。

// peerClientTimeout 是下游对上游的管理类调用（注册/探活/取清单）的超时。
//
// 和转发请求分开：转发可能是一次几分钟的长对话，管理类调用必须快速失败，
// 否则一次上游不可达会让控制台的「测试连接」卡在那里。
const peerClientTimeout = 20 * time.Second

// peerRegistration 是下游保存到本地的一侧记录。
//
// Token 是本机在上游的凭据：它就是 provider 的 apiKeySource，所以直接存在
// provider 上，不另建表——一把凭据配一个 provider，多一张表只会多一处会不同步
// 的地方。
type peerRegistration struct {
	// ParentURL 是上游的根地址（例如 https://vps1.example.com）。
	ParentURL string `json:"parentUrl"`
	// Token 是上游发的对端凭据（gwpeer-…）。
	Token string `json:"token"`
	// UserID / Username 是上游为这台机器建的账号，排查时能直接对上。
	UserID   string `json:"userId"`
	Username string `json:"username"`
	// GrantedProviderIDs 是上游最近一次告诉我们的授权清单。
	GrantedProviderIDs []string `json:"grantedProviderIds"`
	// ParentGatewayID 是上游自己的网关身份，即"谁在为我们服务"。
	//
	// 注意别和注册响应里的 GatewayID 搞混：那个回显的是本机自己的身份，
	// 也就是上游把我们记成了谁。两个方向刚好相反。
	ParentGatewayID string `json:"parentGatewayId"`
}

// peerRegisterClientRequest 是下游发起注册时发的内容。
type peerRegisterClientRequest struct {
	GatewayID            string   `json:"gatewayId"`
	Name                 string   `json:"name,omitempty"`
	Username             string   `json:"username"`
	Password             string   `json:"password"`
	RequestedProviderIDs []string `json:"requestedProviderIds,omitempty"`
}

// peerClient 是对上游 /__peer/* 的最小客户端。
type peerClient struct {
	httpClient *http.Client
}

func newPeerClient() *peerClient {
	return &peerClient{httpClient: &http.Client{Timeout: peerClientTimeout}}
}

// normalizePeerParentURL 把用户输入的上游地址整理成可拼接的根地址。
//
// 允许只填主机名：运维心里的"那个网关"就是 vps1.example.com，让他先想清楚
// 要不要写 https:// 只会多一次试错。默认 https——对面通常在公网。
func normalizePeerParentURL(raw string) (string, error) {
	if root, ok := trimPeerParentURL(raw); ok {
		return root, nil
	}
	return "", fmt.Errorf("上游网关地址不能为空")
}

// trimPeerParentURL 是 normalizePeerParentURL 的不报错形式：畸形输入返回
// 空串，交给调用方决定是报错还是回退。
func trimPeerParentURL(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", false
	}
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	value = strings.TrimRight(value, "/")
	// 端点由我们自己拼，用户粘进来的完整路径必须去掉，否则会得到
	// https://host/__peer/register/__peer/register 这种地址。
	for _, suffix := range []string{
		peerEndpointPrefix + "register", peerEndpointPrefix + "probe", peerEndpointPrefix + "me",
		peerEndpointPrefix + "providers", peerEndpointPrefix + "chat/completions",
	} {
		if strings.HasSuffix(value, suffix) {
			value = strings.TrimSuffix(value, suffix)
			value = strings.TrimRight(value, "/")
			break
		}
	}
	return value, true
}

// resolvePeerParentURL 返回对端 provider 的上游根地址。取不到时返回空串，
// 让请求在上游那一侧报出「invalid URL」而不是在这里 panic。
func resolvePeerParentURL(provider domain.Provider) string {
	root, _ := trimPeerParentURL(provider.BaseURL)
	return root
}

// register 用「网关身份 + 用户名 + 密码」换一把对端凭据。
func (c *peerClient) register(ctx context.Context, parentURL, selfID, name, username, password string, requested []string) (peerRegistration, error) {
	root, err := normalizePeerParentURL(parentURL)
	if err != nil {
		return peerRegistration{}, err
	}
	payload := peerRegisterClientRequest{
		GatewayID:            selfID,
		Name:                 strings.TrimSpace(name),
		Username:             strings.TrimSpace(username),
		Password:             password,
		RequestedProviderIDs: requested,
	}
	var response peerRegisterResponse
	if err := c.postJSON(ctx, root+peerEndpointPrefix+"register", "", payload, &response); err != nil {
		return peerRegistration{}, err
	}
	return peerRegistration{
		ParentURL:          root,
		Token:              response.Token,
		UserID:             response.UserID,
		Username:           response.Username,
		GrantedProviderIDs: response.GrantedProviderIDs,
		// 上游自己的身份，界面显示出来，运维才能核对上游那一侧到底是哪台机器。
		// 注册响应里的 GatewayID 是本机身份，两个方向别拿反。
		ParentGatewayID: response.ParentGatewayID,
	}, nil
}

// peerProbeResult 是上游 /__peer/probe 的回答：能连上、且注册这条路通不通。
type peerProbeResult struct {
	// Gateway 为 true 说明对面确实是一个实现了互联协议的网关。
	Gateway bool `json:"gateway"`
	// GatewayID 是上游自己的身份，用来在测试结果里说清连到了哪一台。
	GatewayID string `json:"gatewayId"`
	// RegistrationAllowed 是上游按它自己的来源白名单给本次来源的结论。
	RegistrationAllowed bool `json:"registrationAllowed"`
	// RegistrationAllowCIDRs 是上游配的白名单原文（空 = 不限来源），出错时
	// 提示语要能直接告诉运维上游实际限制了什么。
	RegistrationAllowCIDRs string `json:"registrationAllowCidrs"`
	Message                string `json:"message"`
}

// probe 问一下这个地址背后是不是一个能接受注册的上游网关。
//
// 不带凭据、不建账号、不换凭据：它只服务于表单上的「测试连接」。老版本上游
// 没有这个端点，会回 404/405，调用方据此提示「版本太旧，仍可直接注册」。
func (c *peerClient) probe(ctx context.Context, parentURL string) (peerProbeResult, error) {
	root, err := normalizePeerParentURL(parentURL)
	if err != nil {
		return peerProbeResult{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, root+peerEndpointPrefix+"probe", nil)
	if err != nil {
		return peerProbeResult{}, err
	}
	httpResponse, err := c.httpClient.Do(request)
	if err != nil {
		return peerProbeResult{}, err
	}
	defer func() { _ = httpResponse.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(httpResponse.Body, 1<<20))
	if err != nil {
		return peerProbeResult{}, err
	}
	if httpResponse.StatusCode != http.StatusOK {
		return peerProbeResult{}, fmt.Errorf("上游返回 %d: %s", httpResponse.StatusCode, peerErrorText(body))
	}
	var result peerProbeResult
	if err := json.Unmarshal(body, &result); err != nil {
		// 200 但不是我们的形状：多半是搭在这条域名上的别的东西（反代默认页、
		// 另一个服务）。把它说成"不是网关"比原样抛 JSON 解析错误有用。
		return peerProbeResult{}, fmt.Errorf("这个地址有响应，但不像网关互联入口（%v）", err)
	}
	if !result.Gateway {
		return peerProbeResult{}, fmt.Errorf("这个地址有响应，但不是网关互联入口")
	}
	return result, nil
}

// providers 取上游当前授权给本机的 provider 清单。
func (c *peerClient) providers(ctx context.Context, parentURL, token string) ([]peerProviderView, error) {
	root, err := normalizePeerParentURL(parentURL)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, root+peerEndpointPrefix+"providers", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	httpResponse, err := c.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = httpResponse.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(httpResponse.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if httpResponse.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("上游返回 %d: %s", httpResponse.StatusCode, peerErrorText(body))
	}
	var payload struct {
		Providers []peerProviderView `json:"providers"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("无法解析上游的 provider 清单: %w", err)
	}
	return payload.Providers, nil
}

// postJSON 是所有 JSON 调用的共同部分：填头、发请求、检查状态、解正文。
func (c *peerClient) postJSON(ctx context.Context, url, token string, payload any, out any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	httpResponse, err := c.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = httpResponse.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(httpResponse.Body, 1<<20))
	if err != nil {
		return err
	}
	if httpResponse.StatusCode != http.StatusOK {
		return fmt.Errorf("上游返回 %d: %s", httpResponse.StatusCode, peerErrorText(body))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

// peerErrorText 从上游的错误正文里取出可读的那一句。
//
// 上游用的是网关统一的 OpenAI 风格错误体；解不出来就退回原文，运维至少要
// 看到对面到底回了什么，而不是一句"注册失败"。
func peerErrorText(body []byte) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && strings.TrimSpace(parsed.Error.Message) != "" {
		return strings.TrimSpace(parsed.Error.Message)
	}
	text := strings.TrimSpace(string(body))
	if len(text) > 300 {
		text = text[:300] + "…"
	}
	if text == "" {
		return "（无正文）"
	}
	return text
}

// 下游转发用的包体。上游只认这个形状，多包一层是为了让「建议用哪个
// provider」「原样的模型请求」「这份请求是什么协议」各自独立：payload 里的
// 字段一律不动。
type peerForwardEnvelope struct {
	ProviderID string `json:"providerId,omitempty"`
	// Protocol 是 payload 的协议。上游靠它把这份请求交给读得懂它的 provider
	// 和转换器：下游的 openai_chat 请求到了上游，可能只有 Claude provider 可用，
	// 那就要转换，而转换只能发生在知道源协议的前提下。空表示按 payload 里的
	// model 反查或交给上游兜底（老版本下游不带这个字段）。
	Protocol string          `json:"protocol,omitempty"`
	Payload  json.RawMessage `json:"payload"`
}

// buildPeerForwardBody 组装一次透传的请求体。
func buildPeerForwardBody(providerID string, protocol domain.Protocol, payload []byte) ([]byte, error) {
	if !json.Valid(payload) {
		return nil, fmt.Errorf("请求体不是合法 JSON")
	}
	return json.Marshal(peerForwardEnvelope{
		ProviderID: strings.TrimSpace(providerID),
		Protocol:   string(protocol),
		Payload:    payload,
	})
}
