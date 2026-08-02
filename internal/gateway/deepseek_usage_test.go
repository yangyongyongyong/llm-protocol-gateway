package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsDeepSeekBaseURL(t *testing.T) {
	for _, url := range []string{
		"https://api.deepseek.com",
		"https://api.deepseek.com/v1",
		"HTTPS://API.DEEPSEEK.COM/v1",
	} {
		if !isDeepSeekBaseURL(url) {
			t.Fatalf("expected %q to be recognized as DeepSeek", url)
		}
	}
	// Relays that merely serve deepseek-* models do not implement /user/balance.
	for _, url := range []string{
		"", "https://api.anthropic.com", "https://api.z.ai/api/coding/paas/v4",
		"https://my-relay.example.com/v1",
	} {
		if isDeepSeekBaseURL(url) {
			t.Fatalf("did not expect %q to be recognized as DeepSeek", url)
		}
	}
}

// A provider configured with a versioned path must still resolve to the bare
// host, because /user/balance lives at the root.
func TestDeepSeekBalanceBaseStripsPath(t *testing.T) {
	cases := map[string]string{
		"https://api.deepseek.com":         "https://api.deepseek.com",
		"https://api.deepseek.com/":        "https://api.deepseek.com",
		"https://api.deepseek.com/v1":      "https://api.deepseek.com",
		"https://api.deepseek.com/v1/chat": "https://api.deepseek.com",
		"":                                 "https://api.deepseek.com",
	}
	for input, want := range cases {
		if got := deepSeekBalanceBase(input); got != want {
			t.Fatalf("deepSeekBalanceBase(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestFetchDeepSeekBalanceParsesResponse(t *testing.T) {
	var gotPath, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"is_available":true,"balance_infos":[
			{"currency":"CNY","total_balance":"110.00","granted_balance":"10.00","topped_up_balance":"100.00"}]}`))
	}))
	defer server.Close()

	report, err := fetchDeepSeekBalance(context.Background(), server.URL, "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/user/balance" {
		t.Fatalf("unexpected path %q", gotPath)
	}
	// Unlike Zhipu, DeepSeek requires the Bearer prefix.
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("unexpected auth header %q", gotAuth)
	}
	if !report.Available || !report.IsAvailable {
		t.Fatalf("expected an available report, got %+v", report)
	}
	if len(report.BalanceInfos) != 1 {
		t.Fatalf("expected 1 balance info, got %d", len(report.BalanceInfos))
	}
	info := report.BalanceInfos[0]
	// Amounts must stay verbatim strings so money keeps its exact decimals.
	if info.Currency != "CNY" || info.TotalBalance != "110.00" ||
		info.GrantedBalance != "10.00" || info.ToppedUpBalance != "100.00" {
		t.Fatalf("unexpected balance info: %+v", info)
	}
	if report.FetchedAt == "" {
		t.Fatal("expected fetchedAt to be set")
	}
}

// is_available:false means the account is out of credit — still a successful
// read, so Available stays true while IsAvailable goes false.
func TestFetchDeepSeekBalanceExhaustedAccount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"is_available":false,"balance_infos":[{"currency":"CNY","total_balance":"0.00"}]}`))
	}))
	defer server.Close()

	report, err := fetchDeepSeekBalance(context.Background(), server.URL, "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if !report.Available {
		t.Fatal("reading the balance succeeded, so Available must be true")
	}
	if report.IsAvailable {
		t.Fatal("expected IsAvailable to be false for an exhausted account")
	}
}

func TestFetchDeepSeekBalanceAuthFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	report, err := fetchDeepSeekBalance(context.Background(), server.URL, "sk-bad")
	if err != nil {
		t.Fatalf("auth failures are reported in-band, not as errors: %v", err)
	}
	if report.Available || report.Unsupported {
		t.Fatalf("expected an unavailable report, got %+v", report)
	}
	if !strings.Contains(report.Error, "401") {
		t.Fatalf("expected the status code in the error, got %q", report.Error)
	}
}

// A relay without the endpoint returns 404: mark Unsupported so the console
// stops polling and shows an explanatory note instead of an error.
func TestFetchDeepSeekBalanceUnsupportedOn404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	report, err := fetchDeepSeekBalance(context.Background(), server.URL, "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if !report.Unsupported || report.Available {
		t.Fatalf("expected unsupported, got %+v", report)
	}
}

func TestFetchDeepSeekBalanceRequiresAPIKey(t *testing.T) {
	report, err := fetchDeepSeekBalance(context.Background(), "https://api.deepseek.com", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if report.Available || report.Error == "" {
		t.Fatalf("expected a missing-key report, got %+v", report)
	}
}

func TestFetchDeepSeekBalanceMalformedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json at all`))
	}))
	defer server.Close()

	report, err := fetchDeepSeekBalance(context.Background(), server.URL, "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if report.Available {
		t.Fatalf("expected parse failure to be unavailable, got %+v", report)
	}
}

// Non-admin owners may read their own provider's balance, matching Zhipu.
func TestDeepSeekUsagePathAllowedForUsers(t *testing.T) {
	if !isUserProviderUsagePath("/__providers/p1/deepseek/usage") {
		t.Fatal("expected the DeepSeek usage path to be readable by owners")
	}
	if isUserProviderUsagePath("/__providers/p1/deepseek/other") {
		t.Fatal("only the usage sub-path should be allowed")
	}
}
