package store

import (
	"path/filepath"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

func TestPeerOwnerAndGrantAuditRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ownerA := domain.User{ID: "owner-a", Username: "owner-a", DisplayName: "Owner A", Role: domain.UserRoleAdmin, RoleID: domain.RoleIDAdmin, Enabled: true, CreatedAt: "2026-11-13T00:00:00Z"}
	ownerB := domain.User{ID: "owner-b", Username: "owner-b", DisplayName: "Owner B", Role: domain.UserRoleUser, RoleID: domain.RoleIDUser, Enabled: true, CreatedAt: "2026-11-13T00:00:00Z"}
	peer := domain.User{ID: "peer-1", Username: "sub-gw", DisplayName: "下游 1", Role: domain.UserRoleUser, RoleID: domain.RoleIDUser, Enabled: true, CreatedAt: "2026-11-13T00:00:00Z", Kind: domain.UserKindPeer, GatewayID: "gw_child", OwnerUserID: ownerA.ID}
	for _, user := range []domain.User{ownerA, ownerB, peer} {
		if err := store.CreateUser(user); err != nil {
			t.Fatal(err)
		}
	}

	loaded, err := store.UserByID(peer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.OwnerUserID != ownerA.ID {
		t.Fatalf("owner = %q, want %q", loaded.OwnerUserID, ownerA.ID)
	}

	for _, audit := range []domain.PeerGrantAudit{
		{PeerUserID: peer.ID, ActorUserID: ownerA.ID, ActorUsername: ownerA.Username, GrantMode: "selected", BeforeProviderIDs: []string{}, AfterProviderIDs: []string{"p1"}, CreatedAt: "2026-11-13T01:00:00Z"},
		{PeerUserID: peer.ID, ActorUserID: ownerB.ID, ActorUsername: ownerB.Username, GrantMode: "all", BeforeProviderIDs: []string{"p1"}, AfterProviderIDs: []string{"p1", "p2"}, CreatedAt: "2026-11-13T02:00:00Z"},
	} {
		if err := store.AddPeerGrantAudit(audit); err != nil {
			t.Fatal(err)
		}
	}
	latest, err := store.LatestPeerGrantAudits()
	if err != nil {
		t.Fatal(err)
	}
	if got := latest[peer.ID]; got.ActorUserID != ownerB.ID || got.GrantMode != "all" || len(got.AfterProviderIDs) != 2 {
		t.Fatalf("latest audit = %+v", got)
	}

	loaded.OwnerUserID = ownerB.ID
	if err := store.UpdateUser(loaded); err != nil {
		t.Fatal(err)
	}
	loaded, _ = store.UserByID(peer.ID)
	if loaded.OwnerUserID != ownerB.ID {
		t.Fatalf("updated owner = %q, want %q", loaded.OwnerUserID, ownerB.ID)
	}

	if err := store.DeleteUser(ownerB.ID); err != nil {
		t.Fatal(err)
	}
	loaded, _ = store.UserByID(peer.ID)
	if loaded.OwnerUserID != "" {
		t.Fatalf("deleted owner must leave peer unassigned, got %q", loaded.OwnerUserID)
	}
}
