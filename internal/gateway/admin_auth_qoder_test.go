package gateway

import (
	"net/http"
	"testing"
)

// Qoder PAT connect endpoints must be reachable by the provider's owner, not
// just admins — the console middleware whitelists paths before handlers run, so
// omitting a family here surfaces as a bare "permission denied" on the UI.
// Ownership itself is enforced inside each handler via
// requireProviderOwnerForUser.
func TestQoderPATPathsAllowedForOwners(t *testing.T) {
	allowed := []struct{ method, path string }{
		{http.MethodPost, "/__providers/p1/qoder-pat/complete"},
		{http.MethodGet, "/__providers/p1/qoder-pat/status"},
		{http.MethodPost, "/__providers/p1/qoder-pat/disconnect"},
	}
	for _, c := range allowed {
		if !isUserAllowedPath(c.method, c.path) {
			t.Errorf("%s %s should be allowed for role=user owners", c.method, c.path)
		}
	}

	// Wrong verbs and unknown sub-paths stay denied.
	denied := []struct{ method, path string }{
		{http.MethodGet, "/__providers/p1/qoder-pat/complete"},
		{http.MethodPost, "/__providers/p1/qoder-pat/status"},
		{http.MethodPost, "/__providers/p1/qoder-pat/bogus"},
		{http.MethodDelete, "/__providers/p1/qoder-pat/disconnect"},
	}
	for _, c := range denied {
		if isUserAllowedPath(c.method, c.path) {
			t.Errorf("%s %s must stay denied", c.method, c.path)
		}
	}
}

// Every connect family the console exposes should be consistent; a new family
// added to the UI without updating the whitelist is the bug this guards.
func TestAllProviderConnectFamiliesWhitelisted(t *testing.T) {
	for _, family := range []string{"claude-oauth", "cursor-oauth", "chatgpt-oauth", "qoder-pat"} {
		if !isUserAllowedPath(http.MethodPost, "/__providers/p1/"+family+"/complete") {
			t.Errorf("family %q missing from the role=user whitelist", family)
		}
	}
}
