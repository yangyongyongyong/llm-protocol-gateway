package gateway

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

func TestOwnedKeyIDsForUsageIgnoresOwnerlessDownstreamKeys(t *testing.T) {
	t.Parallel()
	server := NewServer(NewRouter(domain.GatewayState{APIKeys: []domain.APIKey{
		{ID: "admin-key", OwnerUserID: "admin"},
		{ID: "peer-key", OwnerUserID: "peer-gateway"},
		{ID: "unassigned-peer-key", OwnerUserID: ""},
		{ID: "other-key", OwnerUserID: "other-user"},
	}}), monitor.NewStore())
	server.userStore = newMemoryUserStore(domain.User{
		ID: "peer-gateway", Username: "sub-gw", Kind: domain.UserKindPeer,
		GatewayID: "gw_child", OwnerUserID: "admin", Enabled: true,
	})
	got := strings.Join(server.ownedKeyIDsForUsage("admin"), ",")
	if got != "admin-key,peer-key" {
		t.Fatalf("owned keys = %q, want admin key plus explicitly owned downstream", got)
	}
}

func TestRequestStatsKeepsTodaySeparateFromSelectedRange(t *testing.T) {
	for _, role := range []domain.UserRole{domain.UserRoleAdmin, domain.UserRoleUser} {
		t.Run(string(role), func(t *testing.T) {
			now := time.Now().Local()
			today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
			logs := monitor.NewStore()
			key := domain.APIKey{ID: "period-key", Name: "Period key", OwnerUserID: "period-user"}
			otherKey := domain.APIKey{ID: "other-key", Name: "Other key", OwnerUserID: "other-user"}
			for _, at := range []time.Time{today.AddDate(0, 0, -1).Add(time.Hour), today.AddDate(0, 0, -1).Add(2 * time.Hour), now} {
				log := monitor.RequestLog{Time: at, APIKeyID: key.ID, APIKeyName: key.Name, ProviderID: "provider", Model: "model", Status: 200, InputTokens: 10}
				logs.Add(log)
				logs.ApplyUsageEventSync(monitor.UsageEvent{Time: at, APIKeyID: key.ID, APIKeyName: key.Name, UserID: key.OwnerUserID, ProviderID: log.ProviderID, Model: log.Model, Status: 200, InputTokens: 10})
			}
			logs.ApplyUsageEventSync(monitor.UsageEvent{Time: now, APIKeyID: otherKey.ID, APIKeyName: otherKey.Name, UserID: otherKey.OwnerUserID, ProviderID: "provider", Model: "model", Status: 200, InputTokens: 90, OutputTokens: 9})
			server := NewServer(NewRouter(domain.GatewayState{APIKeys: []domain.APIKey{key, otherKey}}), logs, nil)
			req := httptest.NewRequest("GET", "/__request-stats?from="+today.AddDate(0, 0, -1).Format("2006-01-02")+"&to="+today.Format("2006-01-02"), nil)
			req = req.WithContext(context.WithValue(req.Context(), authContextKey{}, sessionIdentity{UserID: key.OwnerUserID, Role: role}))
			rec := httptest.NewRecorder()
			server.handleRequestStats(rec, req)
			var snapshot monitor.UsageStatsSnapshot
			if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
				t.Fatal(err)
			}
			wantRangeRequests := int64(3)
			wantTodayInput := int64(10)
			wantKeyBuckets := 1
			if role == domain.UserRoleAdmin {
				wantRangeRequests = 4
				wantTodayInput = 100
				wantKeyBuckets = 2
			}
			if snapshot.Range == nil || snapshot.Range.Total.RequestCount != wantRangeRequests {
				t.Fatalf("range = %+v, want %d requests", snapshot.Range, wantRangeRequests)
			}
			if snapshot.Today.Total.RequestCount != int64(wantKeyBuckets) || snapshot.Today.Total.InputTokens != wantTodayInput {
				t.Fatalf("today = %+v, want %d requests / %d input tokens", snapshot.Today.Total, wantKeyBuckets, wantTodayInput)
			}
			if len(snapshot.Today.ByAPIKey) != wantKeyBuckets {
				t.Fatalf("today key breakdown = %+v, want %d buckets", snapshot.Today.ByAPIKey, wantKeyBuckets)
			}
			if len(snapshot.Today.MyByProvider) != 1 || snapshot.Today.MyByProvider[0].InputTokens != 10 || snapshot.Today.MyByProvider[0].OutputTokens != 0 {
				t.Fatalf("my provider breakdown = %+v, want only the current user's key", snapshot.Today.MyByProvider)
			}
			if role == domain.UserRoleAdmin {
				if len(snapshot.Today.ByProvider) != 1 || snapshot.Today.ByProvider[0].InputTokens != 100 {
					t.Fatalf("admin provider total = %+v, want both users' traffic", snapshot.Today.ByProvider)
				}
			}
		})
	}
}
