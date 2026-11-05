package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// The route is built from the site's own root, not from the provider row: the row's
// base URL already ends in /v2 for chat, and appending the image path there produced
// /v2/v2/images/generations — the shape that made WorkBuddy renders impossible.
func TestWorkBuddyEndpointURLDoesNotDoubleTheChatPrefix(t *testing.T) {
	previousEndpoint := workBuddyCNEndpoint
	workBuddyCNEndpoint = "https://www.workbuddy.cn"
	defer func() { workBuddyCNEndpoint = previousEndpoint }()

	got := workBuddyEndpointURL(workBuddyCNSite, workBuddyImageGenerationsPath)
	want := "https://www.workbuddy.cn/v2/images/generations"
	if got != want {
		t.Fatalf("url = %q, want %q", got, want)
	}
	if strings.Contains(got, "/v2/v2") {
		t.Fatalf("the chat prefix was doubled: %q", got)
	}
}

// An edit goes to its own route; everything else is a generation.
func TestWorkBuddyImagePathFor(t *testing.T) {
	if got := workBuddyImagePathFor(context.Background()); got != workBuddyImageGenerationsPath {
		t.Fatalf("plain request -> %q", got)
	}
	if got := workBuddyImagePathFor(withWorkBuddyImagesEdits(context.Background())); got != workBuddyImageEditsPath {
		t.Fatalf("edit request -> %q", got)
	}
}

// A render carries the desktop client's whole head, not just the bearer: the account
// fields are what the upstream reads to decide which client asked.
func TestNewWorkBuddyEndpointRequestCarriesTheDesktopHead(t *testing.T) {
	credential := &domain.WorkBuddyCredential{AccessToken: "tok-1", UserID: "uid-1", Domain: "www.workbuddy.cn"}
	provider := domain.Provider{ID: "wb", AuthType: domain.AuthTypeWorkBuddy, WorkBuddy: credential}
	request, err := newWorkBuddyEndpointRequest(context.Background(), provider,
		"https://www.workbuddy.cn/v2/images/generations", []byte(`{"prompt":"x"}`), "", "")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	required := map[string]string{
		"Authorization":   "Bearer tok-1",
		"X-User-Id":       "uid-1",
		"X-Domain":        "www.workbuddy.cn",
		"X-Product":       "SaaS",
		"X-IDE-Type":      "WorkBuddy",
		"X-Agent-Intent":  workBuddyAgentIntentCraft,
		"X-Agent-Runtime": workBuddyAgentRuntimeLocal,
		"Content-Type":    "application/json",
		"Accept":          "application/json",
	}
	for name, want := range required {
		if got := request.Header.Get(name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
	if request.Header.Get("X-Conversation-ID") == "" || request.Header.Get("X-Request-ID") == "" {
		t.Fatalf("the conversation ids are missing: %v", request.Header)
	}
	if ua := request.Header.Get("User-Agent"); !strings.HasPrefix(ua, "WorkBuddy/") {
		t.Fatalf("User-Agent = %q, want the desktop prefix (10085 otherwise)", ua)
	}
}

// A caller that asks for another content type keeps it: an edit can be sent as
// multipart, and overriding that would break the upload.
func TestNewWorkBuddyEndpointRequestKeepsTheCallersTypes(t *testing.T) {
	provider := domain.Provider{ID: "wb", AuthType: domain.AuthTypeWorkBuddy,
		WorkBuddy: &domain.WorkBuddyCredential{AccessToken: "t", UserID: "u"}}
	request, err := newWorkBuddyEndpointRequest(context.Background(), provider,
		"https://www.workbuddy.cn/v2/images/edits", nil, "multipart/form-data; boundary=x", "*/*")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if got := request.Header.Get("Content-Type"); !strings.HasPrefix(got, "multipart/form-data") {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := request.Header.Get("Accept"); got != "*/*" {
		t.Fatalf("Accept = %q", got)
	}
}

// A provider that is not a WorkBuddy account is refused rather than sent with a
// half-built head.
func TestNewWorkBuddyEndpointRequestRefusesOtherProviders(t *testing.T) {
	provider := domain.Provider{ID: "cz", AuthType: domain.AuthTypeQoderPAT}
	if _, err := newWorkBuddyEndpointRequest(context.Background(), provider, "https://example.com", nil, "", ""); err == nil {
		t.Fatal("a non-WorkBuddy provider was accepted")
	}
}

// The render call goes out with the account's headers and hands the vendor's own
// answer back, refusal included.
func TestExecuteWorkBuddyImageTalksToTheRoute(t *testing.T) {
	var seenPath, seenUser, seenDomain string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seenPath = request.URL.Path
		seenUser = request.Header.Get("X-User-Id")
		seenDomain = request.Header.Get("X-Domain")
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"created":1,"data":[{"url":"https://example.com/a.png"}]}`))
	}))
	defer server.Close()

	previousEndpoint := workBuddyCNEndpoint
	workBuddyCNEndpoint = server.URL
	defer func() { workBuddyCNEndpoint = previousEndpoint }()

	gateway := &Server{}
	provider := domain.Provider{ID: "wb-1", AuthType: domain.AuthTypeWorkBuddy,
		WorkBuddy: &domain.WorkBuddyCredential{AccessToken: "tok", UserID: "uid-9", Domain: "www.workbuddy.cn"}}
	request := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"prompt":"cat"}`))

	status, _, payload, err := gateway.executeWorkBuddyImage(request, provider, workBuddyImageGenerationsPath, []byte(`{"prompt":"cat"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if seenPath != "/v2/images/generations" {
		t.Fatalf("upstream path = %q", seenPath)
	}
	if seenUser != "uid-9" || seenDomain != "www.workbuddy.cn" {
		t.Fatalf("identity head = user %q domain %q", seenUser, seenDomain)
	}
	if !strings.Contains(string(payload), "a.png") {
		t.Fatalf("payload = %s", string(payload))
	}
}

// An edit is sent to the edits route, which is the whole point of the marker.
func TestExecuteWorkBuddyImageUsesTheEditsRoute(t *testing.T) {
	var seenPath string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seenPath = request.URL.Path
		_, _ = writer.Write([]byte(`{"created":1,"data":[]}`))
	}))
	defer server.Close()
	previousEndpoint := workBuddyCNEndpoint
	workBuddyCNEndpoint = server.URL
	defer func() { workBuddyCNEndpoint = previousEndpoint }()

	gateway := &Server{}
	provider := domain.Provider{ID: "wb-2", AuthType: domain.AuthTypeWorkBuddy,
		WorkBuddy: &domain.WorkBuddyCredential{AccessToken: "tok", UserID: "uid"}}
	request := httptest.NewRequest(http.MethodPost, "/v1/images/edits", strings.NewReader(`{}`))
	if _, _, _, err := gateway.executeWorkBuddyImage(request, provider, workBuddyImagePathFor(withWorkBuddyImagesEdits(request.Context())), []byte(`{}`)); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if seenPath != "/v2/images/edits" {
		t.Fatalf("upstream path = %q", seenPath)
	}
}
