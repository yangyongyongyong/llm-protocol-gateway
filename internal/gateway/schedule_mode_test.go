package gateway

import (
	"strings"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func scheduleModeRouter(t *testing.T) *Router {
	t.Helper()
	provider := domain.Provider{ID: "p1", Name: "p1", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://a.example/v1"}
	backup := domain.Provider{ID: "p2", Name: "p2", Protocol: domain.ProtocolOpenAIChat, BaseURL: "https://b.example/v1"}
	route := domain.Route{ID: "r1", Name: "r1", ProviderID: provider.ID, OutputProtocol: domain.ProtocolOpenAIChat, Enabled: true}
	key := domain.APIKey{ID: "k1", Name: "k1", Key: "sk-gw-schedule", RouteID: route.ID, Enabled: true, StreamEnabled: true}
	return NewRouter(domain.GatewayState{
		Providers: []domain.Provider{provider, backup},
		Routes:    []domain.Route{route},
		APIKeys:   []domain.APIKey{key},
	})
}

func TestAddAPIKeyRejectsUnknownScheduleMode(t *testing.T) {
	t.Parallel()
	router := scheduleModeRouter(t)
	if _, err := router.AddAPIKey(domain.APIKey{Name: "k2", Key: "sk-k2", RouteID: "r1", ScheduleMode: "round_robin"}); err == nil {
		t.Fatal("an invented mode must be rejected, not silently mapped to order")
	}
	created, err := router.AddAPIKey(domain.APIKey{Name: "k3", Key: "sk-k3", RouteID: "r1", ScheduleMode: " Quota "})
	if err != nil {
		t.Fatal(err)
	}
	if created.ScheduleMode != "quota" {
		t.Fatalf("expected the trimmed lowercase mode, got %q", created.ScheduleMode)
	}
}

func TestUpdateAPIKeyScheduleMode(t *testing.T) {
	t.Parallel()
	router := scheduleModeRouter(t)
	patch := domain.APIKey{Enabled: true, StreamEnabled: true, ScheduleMode: scheduleModeLeastUsed}
	updated, err := router.UpdateAPIKey("k1", patch)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ScheduleMode != scheduleModeLeastUsed {
		t.Fatalf("got %q", updated.ScheduleMode)
	}
	// Clearing goes back to the default order, which is the pre-existing
	// behavior these keys had before the field existed.
	cleared, err := router.UpdateAPIKey("k1", domain.APIKey{Enabled: true, StreamEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.ScheduleMode != "" {
		t.Fatalf("expected an empty mode, got %q", cleared.ScheduleMode)
	}
	if _, err := router.UpdateAPIKey("k1", domain.APIKey{ScheduleMode: "smart"}); err == nil {
		t.Fatal("an unknown mode must be rejected")
	}
	still, ok := router.APIKeyByToken("sk-gw-schedule")
	if !ok {
		t.Fatal("the key disappeared")
	}
	if still.ScheduleMode != "" {
		t.Fatalf("a rejected patch must not change the key, got %q", still.ScheduleMode)
	}
}

// A scheme (profile) carries its own scheduling policy: switching schemes must
// not leave a key on the mode the previous scheme asked for.
func TestScheduleModeFollowsTheActiveProfile(t *testing.T) {
	t.Parallel()
	router := scheduleModeRouter(t)
	if _, err := router.UpdateAPIKey("k1", domain.APIKey{Enabled: true, StreamEnabled: true, ScheduleMode: scheduleModeRotate}); err != nil {
		t.Fatal(err)
	}
	quotaProfile, err := router.AddKeyProfile("k1", domain.KeyProfile{
		Name: "quota scheme", RouteID: "r1", StreamEnabled: true,
		ScheduleMode:        scheduleModeQuota,
		FallbackProviderIDs: []string{"p2"},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if quotaProfile.ScheduleMode != scheduleModeQuota {
		t.Fatalf("activating a profile must set its mode, got %q", quotaProfile.ScheduleMode)
	}
	// The active profile snapshot keeps its own copy so editing the form edits
	// the scheme, not just the live fields.
	var profile domain.KeyProfile
	for _, candidate := range quotaProfile.Profiles {
		if candidate.ID == quotaProfile.ActiveProfileID {
			profile = candidate
		}
	}
	if profile.ScheduleMode != scheduleModeQuota {
		t.Fatalf("the profile snapshot lost its mode, got %q", profile.ScheduleMode)
	}
	// Back to the first scheme: its snapshot recorded rotate, so the key goes
	// back to rotate.
	restored, err := router.SwitchKeyProfile("k1", quotaProfile.Profiles[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.ScheduleMode != scheduleModeRotate {
		t.Fatalf("switching schemes should restore their own mode, got %q", restored.ScheduleMode)
	}
}

func TestSnapshotKeyProfileCapturesScheduleMode(t *testing.T) {
	t.Parallel()
	router := scheduleModeRouter(t)
	if _, err := router.UpdateAPIKey("k1", domain.APIKey{Enabled: true, StreamEnabled: true, ScheduleMode: scheduleModeLeastUsed}); err != nil {
		t.Fatal(err)
	}
	snapshotted, err := router.SnapshotKeyProfile("k1", "current", false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, profile := range snapshotted.Profiles {
		if strings.EqualFold(profile.Name, "current") {
			found = true
			if profile.ScheduleMode != scheduleModeLeastUsed {
				t.Fatalf("snapshot lost the mode, got %q", profile.ScheduleMode)
			}
		}
	}
	if !found {
		t.Fatal("the snapshot profile is missing")
	}
}

// A scheme may carry the spread policy too: switching to it must move the key
// onto conversation-spreading, and switching away must not leave it behind.
func TestScheduleModeSpreadSurvivesASchemeSwitch(t *testing.T) {
	t.Parallel()
	router := scheduleModeRouter(t)
	spreadProfile, err := router.AddKeyProfile("k1", domain.KeyProfile{
		Name: "spread scheme", RouteID: "r1", StreamEnabled: true,
		ScheduleMode:        scheduleModeSpread,
		FallbackProviderIDs: []string{"p2"},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if spreadProfile.ScheduleMode != scheduleModeSpread {
		t.Fatalf("activating the scheme must apply spread, got %q", spreadProfile.ScheduleMode)
	}
	restored, err := router.SwitchKeyProfile("k1", spreadProfile.Profiles[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.ScheduleMode == scheduleModeSpread {
		t.Fatal("switching back must not keep spread")
	}
}
