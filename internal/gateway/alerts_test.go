package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
	"github.com/luca/llm-protocol-gateway/internal/store"
)

// The store must satisfy AlertStore: the wiring in NewServer is an optional type
// assertion, so a signature drift would silently disable alerting instead of
// failing the build.
func TestStoreSatisfiesAlertStore(t *testing.T) {
	var _ AlertStore = (*store.Store)(nil)
}

func TestAlertSettingsNormalizeFillsDefaults(t *testing.T) {
	settings := domain.AlertSettings{}
	settings.Normalize()
	if settings.MultiIPWindowMinutes != 10 || settings.MultiIPThreshold != 5 || settings.CooldownMinutes != 60 {
		t.Fatalf("zero values not defaulted: %+v", settings)
	}
}

func TestAlertSettingsNormalizeClampsOutOfRange(t *testing.T) {
	settings := domain.AlertSettings{
		MultiIPWindowMinutes: 99999,
		// A threshold of 1 would alert on every single key, every scan.
		MultiIPThreshold: 1,
		CooldownMinutes:  -5,
	}
	settings.Normalize()
	if settings.MultiIPWindowMinutes != 1440 {
		t.Fatalf("window not clamped: %d", settings.MultiIPWindowMinutes)
	}
	if settings.MultiIPThreshold != 2 {
		t.Fatalf("threshold must be at least 2, got %d", settings.MultiIPThreshold)
	}
	if settings.CooldownMinutes != 60 {
		t.Fatalf("negative cooldown not defaulted: %d", settings.CooldownMinutes)
	}
}

// Guard for the security boundary: the bot token must never reach the browser.
func TestRedactAlertSettingsHidesBotToken(t *testing.T) {
	const token = "123456789:AAHsuperSecretTelegramTokenValue"
	view := redactAlertSettingsForClient(domain.AlertSettings{
		MultiIPEnabled: true,
		Telegram:       domain.TelegramSettings{Enabled: true, BotToken: token, ChatID: "42"},
	})
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), token) {
		t.Fatalf("bot token leaked to client payload: %s", raw)
	}
	if !view.Telegram.BotTokenConfigured {
		t.Fatal("expected botTokenConfigured to be true")
	}
	if view.Telegram.BotTokenPreview != "alue" {
		t.Fatalf("expected 4-char tail preview, got %q", view.Telegram.BotTokenPreview)
	}
	if view.Telegram.ChatID != "42" {
		t.Fatalf("chat id should survive redaction, got %q", view.Telegram.ChatID)
	}
}

func TestRedactAlertSettingsWithoutToken(t *testing.T) {
	view := redactAlertSettingsForClient(domain.AlertSettings{})
	if view.Telegram.BotTokenConfigured {
		t.Fatal("expected botTokenConfigured to be false")
	}
	if view.Telegram.BotTokenPreview != "" {
		t.Fatalf("expected empty preview, got %q", view.Telegram.BotTokenPreview)
	}
}

func TestHasNewIP(t *testing.T) {
	if hasNewIP([]string{"1.1.1.1", "2.2.2.2"}, []string{"1.1.1.1"}) {
		t.Fatal("subset must not count as a new event")
	}
	if hasNewIP([]string{"1.1.1.1"}, []string{"1.1.1.1"}) {
		t.Fatal("identical sets must not count as a new event")
	}
	if !hasNewIP([]string{"1.1.1.1"}, []string{"1.1.1.1", "3.3.3.3"}) {
		t.Fatal("a brand-new IP must count as a new event")
	}
	if !hasNewIP(nil, []string{"1.1.1.1"}) {
		t.Fatal("any IP is new when there is no previous set")
	}
}

func TestSendTelegramMessagePostsExpectedPayload(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	original := telegramAPIBase
	telegramAPIBase = server.URL
	defer func() { telegramAPIBase = original }()

	err := sendTelegramMessage(domain.TelegramSettings{
		Enabled: true, BotToken: "tok123", ChatID: "555",
	}, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/bottok123/sendMessage" {
		t.Fatalf("unexpected path %q", gotPath)
	}
	if gotBody["chat_id"] != "555" || gotBody["text"] != "hello" {
		t.Fatalf("unexpected body %+v", gotBody)
	}
	if gotBody["parse_mode"] != "HTML" {
		t.Fatalf("expected HTML parse mode, got %v", gotBody["parse_mode"])
	}
}

// Error strings are persisted on the alert row and shown in the console, so the
// token (which lives in the request URL) must never appear in them.
func TestSendTelegramMessageErrorHidesToken(t *testing.T) {
	const token = "secretToken999"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		// A hostile/naive upstream echoing the tokenized URL must still be scrubbed.
		_, _ = w.Write([]byte(`{"description":"Unauthorized for /bot` + token + `/sendMessage"}`))
	}))
	defer server.Close()

	original := telegramAPIBase
	telegramAPIBase = server.URL
	defer func() { telegramAPIBase = original }()

	err := sendTelegramMessage(domain.TelegramSettings{
		Enabled: true, BotToken: token, ChatID: "1",
	}, "hi")
	if err == nil {
		t.Fatal("expected an error for HTTP 401")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("token leaked into error: %v", err)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected status code in error, got %v", err)
	}
}

func TestSendTelegramMessageRequiresTokenAndChat(t *testing.T) {
	if err := sendTelegramMessage(domain.TelegramSettings{ChatID: "1"}, "x"); err == nil {
		t.Fatal("expected error when bot token is empty")
	}
	if err := sendTelegramMessage(domain.TelegramSettings{BotToken: "t"}, "x"); err == nil {
		t.Fatal("expected error when chat id is empty")
	}
}

// An empty incoming BotToken means "keep the stored one" so the console can
// render an empty password field without wiping a working credential.
func TestUpdateAlertSettingsKeepsTokenWhenBlank(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	server := &Server{logs: monitor.NewStore(), alertStore: db}

	if _, err := server.UpdateAlertSettings(domain.AlertSettings{
		MultiIPEnabled: true,
		Telegram:       domain.TelegramSettings{Enabled: true, BotToken: "original-token", ChatID: "1"},
	}); err != nil {
		t.Fatal(err)
	}

	// Second save carries no token (blank password field) but changes chat id.
	updated, err := server.UpdateAlertSettings(domain.AlertSettings{
		MultiIPEnabled: true,
		Telegram:       domain.TelegramSettings{Enabled: true, BotToken: "", ChatID: "2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Telegram.BotToken != "original-token" {
		t.Fatalf("blank token must preserve the stored one, got %q", updated.Telegram.BotToken)
	}
	if updated.Telegram.ChatID != "2" {
		t.Fatalf("chat id should have been updated, got %q", updated.Telegram.ChatID)
	}

	// A non-blank token replaces it.
	replaced, err := server.UpdateAlertSettings(domain.AlertSettings{
		MultiIPEnabled: true,
		Telegram:       domain.TelegramSettings{Enabled: true, BotToken: "new-token", ChatID: "2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Telegram.BotToken != "new-token" {
		t.Fatalf("expected token replacement, got %q", replaced.Telegram.BotToken)
	}

	// Reloading from disk must return the persisted token, normalized.
	if reloaded := server.AlertSettings(); reloaded.Telegram.BotToken != "new-token" {
		t.Fatalf("token did not survive reload, got %q", reloaded.Telegram.BotToken)
	}
}

// Cooldown suppression: inside the window the same IP set must not re-alert,
// but a brand-new IP is a genuinely new event and must alert immediately.
func TestShouldRaiseMultiIPAlertCooldown(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	server := &Server{logs: monitor.NewStore(), alertStore: db}
	settings := domain.DefaultAlertSettings()

	hit := monitor.MultiIPHit{
		APIKeyID: "k1", APIKeyName: "main",
		IPs: []string{"1.1.1.1", "2.2.2.2", "3.3.3.3"}, IPCount: 3, RequestCount: 9,
	}

	// Never alerted before → must raise.
	if !server.shouldRaiseAlert(domain.AlertRuleAPIKeyMultiIP, hit.APIKeyID, hit.IPs, settings) {
		t.Fatal("first detection must raise an alert")
	}
	server.raiseMultiIPAlert(hit, settings)

	// Same IP set, still inside cooldown → suppressed.
	if server.shouldRaiseAlert(domain.AlertRuleAPIKeyMultiIP, hit.APIKeyID, hit.IPs, settings) {
		t.Fatal("identical IP set inside cooldown must be suppressed")
	}

	// A brand-new IP appears → new event, alert again despite cooldown.
	grown := append(append([]string{}, hit.IPs...), "9.9.9.9")
	if !server.shouldRaiseAlert(domain.AlertRuleAPIKeyMultiIP, hit.APIKeyID, grown, settings) {
		t.Fatal("a brand-new IP must re-alert even inside cooldown")
	}

	// Cooldown is per-rule: the overlap rule has its own history, so it must not
	// be suppressed by the multi-IP alert we just raised.
	if !server.shouldRaiseAlert(domain.AlertRuleAPIKeyConcurrentIP, hit.APIKeyID, hit.IPs, settings) {
		t.Fatal("cooldown must be tracked per rule, not per key")
	}

	// The suppressed and re-alert checks above are pure predicate calls, so only
	// the single raiseMultiIPAlert call should have persisted a row.
	page, err := db.QueryAlerts(monitor.AlertQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 {
		t.Fatalf("expected exactly 1 persisted alert, got %d", page.Total)
	}
	if page.Items[0].PushStatus != domain.AlertPushDisabled {
		t.Fatalf("telegram is off, expected push status %q, got %q",
			domain.AlertPushDisabled, page.Items[0].PushStatus)
	}
	if page.Items[0].Status != domain.AlertStatusUnread {
		t.Fatalf("new alerts must start unread, got %q", page.Items[0].Status)
	}
}

func monitorAlertFixture(keyName string, ips []string) monitor.Alert {
	return monitor.Alert{
		Time:          time.Now(),
		Rule:          domain.AlertRuleAPIKeyMultiIP,
		Severity:      "warn",
		APIKeyID:      "k1",
		APIKeyName:    keyName,
		IPs:           ips,
		IPCount:       len(ips),
		WindowMinutes: 10,
		RequestCount:  len(ips),
		Status:        domain.AlertStatusUnread,
	}
}

func TestFormatAlertMessageEscapesAndTruncates(t *testing.T) {
	ips := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		ips = append(ips, "10.0.0."+string(rune('a'+i)))
	}
	message := formatAlertMessage(monitorAlertFixture("<b>key</b> & co", ips))
	if strings.Contains(message, "<b>key</b>") {
		t.Fatalf("key name must be HTML-escaped: %s", message)
	}
	if !strings.Contains(message, "&amp;") {
		t.Fatalf("ampersand must be escaped: %s", message)
	}
	if !strings.Contains(message, "等 20 个") {
		t.Fatalf("expected truncation notice for long IP lists: %s", message)
	}
}

func TestConcurrentIPDefaultsAndClamp(t *testing.T) {
	settings := domain.DefaultAlertSettings()
	if !settings.ConcurrentIPEnabled {
		t.Fatal("overlap rule should be enabled by default")
	}
	if settings.ConcurrentIPThreshold != 4 {
		t.Fatalf("expected default concurrent threshold 4, got %d", settings.ConcurrentIPThreshold)
	}
	if settings.ConcurrentIPWindowMinutes != 10 {
		t.Fatalf("expected default concurrent window 10, got %d", settings.ConcurrentIPWindowMinutes)
	}

	clamped := domain.AlertSettings{ConcurrentIPThreshold: 1, ConcurrentIPWindowMinutes: 99999}
	clamped.Normalize()
	if clamped.ConcurrentIPThreshold != 2 {
		t.Fatalf("threshold must clamp to >=2, got %d", clamped.ConcurrentIPThreshold)
	}
	if clamped.ConcurrentIPWindowMinutes != 1440 {
		t.Fatalf("window must clamp to 1440, got %d", clamped.ConcurrentIPWindowMinutes)
	}
}

// The two rules must produce visibly different pushes so the operator can tell
// a "possible" leak from a near-certain one.
func TestFormatAlertMessageDistinguishesRules(t *testing.T) {
	concurrent := monitorAlertFixture("main", []string{"1.1.1.1", "2.2.2.2", "3.3.3.3"})
	concurrent.Rule = domain.AlertRuleAPIKeyConcurrentIP
	concurrent.ConcurrentAt = time.Date(2026, 8, 3, 4, 5, 6, 0, time.UTC)
	message := formatAlertMessage(concurrent)
	if !strings.Contains(message, "同一时刻并发 IP") {
		t.Fatalf("overlap message missing concurrency wording: %s", message)
	}
	// 04:05:06 UTC is 12:05:06 in UTC+8.
	if !strings.Contains(message, "12:05:06") {
		t.Fatalf("overlap instant should render in UTC+8: %s", message)
	}

	multi := monitorAlertFixture("main", []string{"1.1.1.1"})
	if strings.Contains(formatAlertMessage(multi), "同一时刻并发 IP") {
		t.Fatal("multi-IP message must not use overlap wording")
	}
}

// concurrent_at must survive the DB round trip, since the console and the push
// text both display it.
func TestConcurrentAtPersists(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	server := &Server{logs: monitor.NewStore(), alertStore: db}
	at := time.Now().Add(-time.Minute).UTC().Truncate(time.Millisecond)

	server.raiseConcurrentIPAlert(monitor.ConcurrentIPHit{
		APIKeyID: "k1", APIKeyName: "main", At: at,
		IPs: []string{"1.1.1.1", "2.2.2.2", "3.3.3.3"}, IPCount: 3, RequestCount: 3,
	}, domain.DefaultAlertSettings())

	page, err := db.QueryAlerts(monitor.AlertQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 {
		t.Fatalf("expected 1 alert, got %d", page.Total)
	}
	got := page.Items[0]
	if got.Rule != domain.AlertRuleAPIKeyConcurrentIP {
		t.Fatalf("unexpected rule %q", got.Rule)
	}
	if !got.ConcurrentAt.Equal(at) {
		t.Fatalf("concurrentAt did not persist: want %s got %s", at, got.ConcurrentAt)
	}
	if got.Severity != "error" {
		t.Fatalf("overlap alerts should be severity error, got %q", got.Severity)
	}
}
