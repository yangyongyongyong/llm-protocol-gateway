package gateway

import (
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

func TestResolveLogKeyNameFilterMatchesRenamedHistory(t *testing.T) {
	keys := []domain.APIKey{
		{ID: "luca-claude", Name: "luca-claude-opencode"},
		{ID: "other", Name: "other-key"},
	}

	// Filtering by the current name must become an ID filter, so log rows still
	// carrying the pre-rename name ("luca-claude") are matched too.
	got := resolveLogKeyNameFilter(keys, monitor.RequestLogQuery{APIKeyName: "luca-claude-opencode"})
	if got.APIKeyName != "" {
		t.Fatalf("name filter should be cleared, got %q", got.APIKeyName)
	}
	if len(got.APIKeyIDs) != 1 || got.APIKeyIDs[0] != "luca-claude" {
		t.Fatalf("ids=%v", got.APIKeyIDs)
	}

	// Unknown name (e.g. a deleted key) keeps the substring match as fallback.
	fallback := resolveLogKeyNameFilter(keys, monitor.RequestLogQuery{APIKeyName: "selfcheck-foo"})
	if fallback.APIKeyName != "selfcheck-foo" || fallback.APIKeyIDs != nil {
		t.Fatalf("got %+v", fallback)
	}

	// Empty filter is untouched.
	if out := resolveLogKeyNameFilter(keys, monitor.RequestLogQuery{}); out.APIKeyName != "" || out.APIKeyIDs != nil {
		t.Fatalf("got %+v", out)
	}
}

func TestResolveLogKeyNameFilterKeepsUserIsolation(t *testing.T) {
	keys := []domain.APIKey{
		{ID: "mine", Name: "shared-prefix-a"},
		{ID: "theirs", Name: "shared-prefix-b"},
	}
	// A normal user is already restricted to their own keys; the name filter may
	// only narrow that set, never widen it.
	got := resolveLogKeyNameFilter(keys, monitor.RequestLogQuery{
		APIKeyName: "shared-prefix",
		APIKeyIDs:  []string{"mine"},
	})
	if len(got.APIKeyIDs) != 1 || got.APIKeyIDs[0] != "mine" {
		t.Fatalf("ids=%v", got.APIKeyIDs)
	}

	// Non-nil empty set (user owns nothing) must stay "match nothing", not nil.
	empty := resolveLogKeyNameFilter(keys, monitor.RequestLogQuery{
		APIKeyName: "shared-prefix",
		APIKeyIDs:  []string{},
	})
	if empty.APIKeyIDs == nil || len(empty.APIKeyIDs) != 0 {
		t.Fatalf("ids=%v (want non-nil empty)", empty.APIKeyIDs)
	}
}

func TestCurrentAPIKeyNamesByID(t *testing.T) {
	names := currentAPIKeyNamesByID([]domain.APIKey{
		{ID: "a", Name: " renamed "},
		{ID: "b", Name: ""}, // unnamed keys keep their log snapshot
	})
	if names["a"] != "renamed" {
		t.Fatalf("names=%v", names)
	}
	if _, ok := names["b"]; ok {
		t.Fatalf("blank name should not override, names=%v", names)
	}
}

func TestFillRequestLogKeyNamesUsesCurrentName(t *testing.T) {
	s := &Server{router: NewRouter(domain.GatewayState{APIKeys: []domain.APIKey{
		{ID: "luca-response-家里", Name: "luca-response-macmini"},
	}})}
	logs := []monitor.RequestLog{
		{APIKeyID: "luca-response-家里", APIKeyName: "luca-response-家里"},
		{APIKeyID: "selfcheck-x", APIKeyName: "selfcheck-x"}, // no live key: keep snapshot
		{APIKeyID: "", APIKeyName: "Route Test"},
	}
	s.fillRequestLogKeyNames(logs)
	if logs[0].APIKeyName != "luca-response-macmini" {
		t.Fatalf("renamed key not resolved: %q", logs[0].APIKeyName)
	}
	if logs[1].APIKeyName != "selfcheck-x" || logs[2].APIKeyName != "Route Test" {
		t.Fatalf("fallback broken: %+v", logs[1:])
	}
}

func TestFillUsageKeyNamesUsesCurrentName(t *testing.T) {
	s := &Server{router: NewRouter(domain.GatewayState{APIKeys: []domain.APIKey{
		{ID: "luca-claude", Name: "luca-claude-opencode"},
	}})}
	snapshot := monitor.UsageStatsSnapshot{
		Today: monitor.TodayStatsSnapshot{ByAPIKey: []monitor.APIKeyDayStats{
			{APIKeyID: "luca-claude", APIKeyName: "luca-claude"},
		}},
		Month: monitor.PeriodStatsSnapshot{ByAPIKey: []monitor.APIKeyDayStats{
			{APIKeyID: "luca-claude", APIKeyName: "luca-claude"},
		}},
		Range: &monitor.PeriodStatsSnapshot{ByAPIKey: []monitor.APIKeyDayStats{
			{APIKeyID: "luca-claude", APIKeyName: "luca-claude"},
			{APIKeyID: "gone", APIKeyName: "deleted-key"},
		}},
	}
	s.fillUsageKeyNames(&snapshot)
	if snapshot.Today.ByAPIKey[0].APIKeyName != "luca-claude-opencode" ||
		snapshot.Month.ByAPIKey[0].APIKeyName != "luca-claude-opencode" ||
		snapshot.Range.ByAPIKey[0].APIKeyName != "luca-claude-opencode" {
		t.Fatalf("usage names not resolved: %+v", snapshot)
	}
	if snapshot.Range.ByAPIKey[1].APIKeyName != "deleted-key" {
		t.Fatalf("deleted key should keep snapshot name: %+v", snapshot.Range.ByAPIKey[1])
	}
}
