package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
	"github.com/luca/llm-protocol-gateway/internal/store"
)

const (
	alertSettingsKey  = "alerts"
	alertScanInterval = 2 * time.Minute
	// alertScanFirstDelay lets startup work (usage rebuild, log restore) finish
	// before the first scan competes for the same SQLite reader pool.
	alertScanFirstDelay = 2 * time.Minute
	// telegramMaxIPsInMessage caps the IP list in the push text; a leaked key
	// hit by a proxy pool can produce dozens and Telegram rejects long bodies.
	telegramMaxIPsInMessage = 12
	telegramTimeout         = 15 * time.Second
)

// AlertStore is the persistence surface the alerting feature needs. It mirrors
// the optional-interface style used for RequestLogStore so tests can construct
// a Server without alert persistence.
type AlertStore interface {
	DetectMultiIPKeys(time.Time, int) ([]monitor.MultiIPHit, error)
	InsertAlert(monitor.Alert) (int64, error)
	QueryAlerts(monitor.AlertQuery) (store.AlertPage, error)
	AlertByID(int64) (monitor.Alert, bool, error)
	LatestAlertForKey(string, string) (monitor.Alert, bool, error)
	UpdateAlertStatus(int64, string) error
	UpdateAlertPush(int64, string, string) error
	Setting(string) string
	SetSetting(string, string) error
}

// AlertSettings returns the persisted alert config, normalized. Missing config
// yields defaults so a fresh install already monitors for multi-IP usage.
func (s *Server) AlertSettings() domain.AlertSettings {
	settings := domain.DefaultAlertSettings()
	if s.alertStore != nil {
		if raw := strings.TrimSpace(s.alertStore.Setting(alertSettingsKey)); raw != "" {
			var stored domain.AlertSettings
			if err := json.Unmarshal([]byte(raw), &stored); err == nil {
				settings = stored
			}
		}
	}
	settings.Normalize()
	return settings
}

// UpdateAlertSettings persists new alert config. An empty incoming BotToken
// means "keep the existing token" so the console can render an empty password
// field without wiping a configured credential.
func (s *Server) UpdateAlertSettings(incoming domain.AlertSettings) (domain.AlertSettings, error) {
	if s.alertStore == nil {
		return domain.AlertSettings{}, fmt.Errorf("alert storage unavailable")
	}
	current := s.AlertSettings()
	incoming.Normalize()
	if incoming.Telegram.BotToken == "" {
		incoming.Telegram.BotToken = current.Telegram.BotToken
	}
	raw, err := json.Marshal(incoming)
	if err != nil {
		return domain.AlertSettings{}, err
	}
	if err := s.alertStore.SetSetting(alertSettingsKey, string(raw)); err != nil {
		return domain.AlertSettings{}, err
	}
	return incoming, nil
}

// alertSettingsView is the browser-facing shape of AlertSettings. The bot token
// is represented only by a configured flag plus a 4-char tail, never in full —
// same "rebuild the struct, don't copy the secret" rule as
// redactProviderForClient.
type alertSettingsView struct {
	MultiIPEnabled       bool                `json:"multiIpEnabled"`
	MultiIPWindowMinutes int                 `json:"multiIpWindowMinutes"`
	MultiIPThreshold     int                 `json:"multiIpThreshold"`
	CooldownMinutes      int                 `json:"cooldownMinutes"`
	Telegram             telegramSettingsView `json:"telegram"`
}

type telegramSettingsView struct {
	Enabled            bool   `json:"enabled"`
	ChatID             string `json:"chatId"`
	BotTokenConfigured bool   `json:"botTokenConfigured"`
	BotTokenPreview    string `json:"botTokenPreview,omitempty"`
}

func redactAlertSettingsForClient(settings domain.AlertSettings) alertSettingsView {
	token := strings.TrimSpace(settings.Telegram.BotToken)
	preview := ""
	if len(token) >= 4 {
		preview = token[len(token)-4:]
	}
	return alertSettingsView{
		MultiIPEnabled:       settings.MultiIPEnabled,
		MultiIPWindowMinutes: settings.MultiIPWindowMinutes,
		MultiIPThreshold:     settings.MultiIPThreshold,
		CooldownMinutes:      settings.CooldownMinutes,
		Telegram: telegramSettingsView{
			Enabled:            settings.Telegram.Enabled,
			ChatID:             settings.Telegram.ChatID,
			BotTokenConfigured: token != "",
			BotTokenPreview:    preview,
		},
	}
}

// StartAlertScan runs the periodic leak detection sweep.
func (s *Server) StartAlertScan(ctx context.Context) {
	if s.alertStore == nil {
		return
	}
	go func() {
		timer := time.NewTimer(alertScanFirstDelay)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				s.runAlertScan()
				timer.Reset(alertScanInterval)
			}
		}
	}()
}

// runAlertScan performs one detection pass: find keys used from too many IPs,
// drop the ones still inside their cooldown, persist the rest, and push.
func (s *Server) runAlertScan() {
	if s.alertStore == nil {
		return
	}
	settings := s.AlertSettings()
	if !settings.MultiIPEnabled {
		return
	}
	since := time.Now().Add(-time.Duration(settings.MultiIPWindowMinutes) * time.Minute)
	hits, err := s.alertStore.DetectMultiIPKeys(since, settings.MultiIPThreshold)
	if err != nil {
		slog.Warn("alert scan failed", "error", err)
		return
	}
	for _, hit := range hits {
		if !s.shouldRaiseMultiIPAlert(hit, settings) {
			continue
		}
		s.raiseMultiIPAlert(hit, settings)
	}
}

// shouldRaiseMultiIPAlert applies cooldown suppression. Inside the cooldown an
// alert repeats only when a brand-new IP shows up, which is a genuinely new
// event rather than the same one still in progress.
func (s *Server) shouldRaiseMultiIPAlert(hit monitor.MultiIPHit, settings domain.AlertSettings) bool {
	previous, ok, err := s.alertStore.LatestAlertForKey(domain.AlertRuleAPIKeyMultiIP, hit.APIKeyID)
	if err != nil {
		slog.Warn("alert cooldown lookup failed", "error", err, "apiKeyId", hit.APIKeyID)
		return false
	}
	if !ok {
		return true
	}
	if time.Since(previous.Time) >= time.Duration(settings.CooldownMinutes)*time.Minute {
		return true
	}
	return hasNewIP(previous.IPs, hit.IPs)
}

// hasNewIP reports whether current contains an IP absent from previous.
func hasNewIP(previous, current []string) bool {
	seen := make(map[string]struct{}, len(previous))
	for _, ip := range previous {
		seen[ip] = struct{}{}
	}
	for _, ip := range current {
		if _, ok := seen[ip]; !ok {
			return true
		}
	}
	return false
}

func (s *Server) raiseMultiIPAlert(hit monitor.MultiIPHit, settings domain.AlertSettings) {
	ips := append([]string{}, hit.IPs...)
	sort.Strings(ips)
	alert := monitor.Alert{
		Time:          time.Now(),
		Rule:          domain.AlertRuleAPIKeyMultiIP,
		Severity:      "warn",
		APIKeyID:      hit.APIKeyID,
		APIKeyName:    hit.APIKeyName,
		IPs:           ips,
		IPCount:       hit.IPCount,
		WindowMinutes: settings.MultiIPWindowMinutes,
		RequestCount:  hit.RequestCount,
		Status:        domain.AlertStatusUnread,
	}
	id, err := s.alertStore.InsertAlert(alert)
	if err != nil {
		slog.Warn("persist alert failed", "error", err, "apiKeyId", hit.APIKeyID)
		return
	}
	alert.ID = id
	s.logs.AddApp("warn", "api key used from multiple IPs",
		fmt.Sprintf("key=%s ipCount=%d windowMinutes=%d", hit.APIKeyName, hit.IPCount, settings.MultiIPWindowMinutes))
	s.pushAlert(alert, settings.Telegram)
}

// pushAlert delivers one alert to Telegram and records the outcome on the alert
// row. Delivery is never retried automatically: a bad token would otherwise
// hammer the API every scan. The console offers a manual retry instead.
func (s *Server) pushAlert(alert monitor.Alert, telegram domain.TelegramSettings) {
	if !telegram.Enabled || telegram.BotToken == "" || telegram.ChatID == "" {
		if err := s.alertStore.UpdateAlertPush(alert.ID, domain.AlertPushDisabled, ""); err != nil {
			slog.Warn("record alert push state failed", "error", err)
		}
		return
	}
	if err := sendTelegramMessage(telegram, formatAlertMessage(alert)); err != nil {
		if updateErr := s.alertStore.UpdateAlertPush(alert.ID, domain.AlertPushFailed, err.Error()); updateErr != nil {
			slog.Warn("record alert push failure failed", "error", updateErr)
		}
		s.logs.AddApp("warn", "alert telegram push failed", err.Error())
		return
	}
	if err := s.alertStore.UpdateAlertPush(alert.ID, domain.AlertPushSent, ""); err != nil {
		slog.Warn("record alert push success failed", "error", err)
	}
}

// beijingTime renders timestamps in UTC+8, matching the console's display.
func beijingTime(t time.Time) string {
	return t.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05")
}

func formatAlertMessage(alert monitor.Alert) string {
	ips := alert.IPs
	suffix := ""
	if len(ips) > telegramMaxIPsInMessage {
		suffix = fmt.Sprintf(" 等 %d 个", len(ips))
		ips = ips[:telegramMaxIPsInMessage]
	}
	keyName := alert.APIKeyName
	if strings.TrimSpace(keyName) == "" {
		keyName = alert.APIKeyID
	}
	var builder strings.Builder
	builder.WriteString("⚠️ <b>API Key 可能泄露</b>\n\n")
	fmt.Fprintf(&builder, "密钥: <b>%s</b>\n", escapeTelegramHTML(keyName))
	fmt.Fprintf(&builder, "独立 IP: <b>%d</b> 个（%d 分钟内）\n", alert.IPCount, alert.WindowMinutes)
	fmt.Fprintf(&builder, "请求数: %d\n", alert.RequestCount)
	fmt.Fprintf(&builder, "IP: %s%s\n", escapeTelegramHTML(strings.Join(ips, ", ")), suffix)
	fmt.Fprintf(&builder, "时间: %s (UTC+8)", beijingTime(alert.Time))
	return builder.String()
}

// escapeTelegramHTML escapes the three characters Telegram's HTML parse mode
// treats as markup. Key names and IPs are echoed into the message body, so an
// unescaped "&" or "<" would make Telegram reject the whole push.
func escapeTelegramHTML(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	return strings.ReplaceAll(text, ">", "&gt;")
}

// telegramAPIBase is overridden in tests to point at a local httptest server.
var telegramAPIBase = "https://api.telegram.org"

// sendTelegramMessage posts one message via the Bot API.
//
// The bot token sits in the request URL, so no error path here may include the
// URL — error strings land in the alerts table and the console.
func sendTelegramMessage(cfg domain.TelegramSettings, text string) error {
	token := strings.TrimSpace(cfg.BotToken)
	chatID := strings.TrimSpace(cfg.ChatID)
	if token == "" {
		return fmt.Errorf("telegram bot token is empty")
	}
	if chatID == "" {
		return fmt.Errorf("telegram chat id is empty")
	}
	payload, err := json.Marshal(map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	})
	if err != nil {
		return err
	}
	request, err := http.NewRequest(http.MethodPost,
		telegramAPIBase+"/bot"+token+"/sendMessage", bytes.NewReader(payload))
	if err != nil {
		// Never surface err here: it embeds the tokenized URL.
		return fmt.Errorf("build telegram request failed")
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: telegramTimeout}
	response, err := client.Do(request)
	if err != nil {
		// url.Error wraps the tokenized URL, so report only the cause.
		return fmt.Errorf("telegram request failed: %s", scrubTelegramToken(err.Error(), token))
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 8*1024))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("telegram HTTP %d: %s", response.StatusCode,
			scrubTelegramToken(strings.TrimSpace(string(body)), token))
	}
	return nil
}

// scrubTelegramToken is a defense-in-depth pass over any third-party string
// before it is persisted, in case a transport error echoes the request URL.
func scrubTelegramToken(text, token string) string {
	if token == "" {
		return text
	}
	return strings.ReplaceAll(text, token, "***")
}
