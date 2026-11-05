package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The video routes were registered on the mux and still answered a plain 404
// ("page not found") on a local host, because the local/LAN branch of the host-split
// middleware only forwards paths on this allowlist. Registering a model route without
// adding its prefix to the list is the mistake these tests exist to catch — the same
// one embeddings made once already.
func TestIsModelProtocolPathVideos(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/v1/videos/generations", true},
		{"/v1/videos/tasks", true},
		{"/v1/videos/1312831544-WandVideo-ab12", true},
		{"/v1/videos/", true},
		{"/v1/images/generations", true},
		{"/v1/embeddings", true},
		// Still not a model call.
		{"/api/providers", false},
		{"/", false},
	}
	for _, testCase := range cases {
		if got := isModelProtocolPath(testCase.path); got != testCase.want {
			t.Fatalf("isModelProtocolPath(%q) = %v, want %v", testCase.path, got, testCase.want)
		}
	}
}

// The local/LAN host — the one this gateway actually serves on — must forward a video
// request to the API handler rather than answering the SPA fallback.
func TestHostSeparatedServingRoutesVideosLocally(t *testing.T) {
	hits := 0
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	})
	handler := withHostSeparatedServing(api, t.TempDir(), nil)
	for _, request := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/v1/videos/generations"},
		{http.MethodPost, "/v1/videos/tasks"},
		{http.MethodGet, "/v1/videos/task-1"},
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(request.method, "http://127.0.0.1:18090"+request.path, nil))
		if hits == 0 {
			t.Fatalf("%s %s did not reach the API handler (status %d)", request.method, request.path, recorder.Code)
		}
	}
}

// And the host split keeps its two promises for these paths: the API hostname serves
// them, the UI hostname refuses them instead of handing back the SPA.
func TestHostSeparatedServingRoutesVideos(t *testing.T) {
	hits := 0
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	})
	roleForHost := func(host string) string {
		switch host {
		case "api.example.com":
			return "api"
		case "ui.example.com":
			return "ui"
		}
		return ""
	}
	handler := withHostSeparatedServing(api, t.TempDir(), roleForHost)
	for _, path := range []string{"/v1/videos/generations", "/v1/videos/tasks", "/v1/videos/task-1"} {
		for _, testCase := range []struct {
			host      string
			wantToAPI bool
		}{
			{"api.example.com", true},
			{"ui.example.com", false},
		} {
			before := hits
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "http://"+testCase.host+path, nil))
			reached := hits > before
			if reached != testCase.wantToAPI {
				t.Fatalf("%s on host %s: reached api = %v, want %v (status %d)",
					path, testCase.host, reached, testCase.wantToAPI, recorder.Code)
			}
		}
	}
}
