package gateway

import (
	"net/http"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// The task center's permission ladder. Every one of these paths used to fall
// through to the "-oauth/" catch-all and map to providers.secret, so a role that
// may run a daily task but not read an account's credentials was refused — and
// granting the credential permission just to press 保活 is over-privilege.
func TestWorkBuddyTaskCenterPermissionLadder(t *testing.T) {
	cases := []struct {
		method string
		path   string
		want   domain.Permission
	}{
		// Reading the list is the card itself.
		{http.MethodGet, "/__providers/p1/workbuddy-oauth/tasks", domain.PermProvidersView},
		// Running tasks is an action, like 签到.
		{http.MethodPost, "/__providers/p1/workbuddy-oauth/tasks/auto", domain.PermProvidersTest},
		{http.MethodGet, "/__providers/p1/workbuddy-oauth/tasks/run", domain.PermProvidersTest},
		{http.MethodPost, "/__providers/p1/workbuddy-oauth/activity", domain.PermProvidersTest},
		{http.MethodPost, "/__providers/p1/workbuddy-oauth/keepalive", domain.PermProvidersTest},
		// Turning the daily switch on changes configuration, like checkin/auto.
		{http.MethodPost, "/__providers/p1/workbuddy-oauth/automation", domain.PermProvidersEdit},
		// The OAuth flows themselves still touch credentials.
		{http.MethodPost, "/__providers/p1/workbuddy-oauth/start", domain.PermProvidersSecret},
		{http.MethodGet, "/__providers/p1/workbuddy-oauth/status", domain.PermProvidersSecret},
	}
	for _, testCase := range cases {
		got := routePermission(testCase.method, testCase.path)
		if got == nil {
			t.Errorf("%s %s: no permission", testCase.method, testCase.path)
			continue
		}
		if *got != testCase.want {
			t.Errorf("%s %s: permission = %s, want %s",
				testCase.method, testCase.path, *got, testCase.want)
		}
	}
}

// The built-in roles must line up with that ladder: 受限用户 may run a task on a
// provider granted to them, but not change its configuration.
func TestTaskCenterPermissionsMatchBuiltinRoles(t *testing.T) {
	user, ok := domain.BuiltinRole(domain.RoleIDUser)
	if !ok {
		t.Fatal("普通用户 role missing")
	}
	for _, permission := range []domain.Permission{
		domain.PermProvidersView, domain.PermProvidersTest, domain.PermProvidersEdit,
	} {
		if !user.Has(permission) {
			t.Errorf("普通用户 must hold %s to run its own task center", permission)
		}
	}

	grantee, ok := domain.BuiltinRole(domain.RoleIDGrantee)
	if !ok {
		t.Fatal("受限用户 role missing")
	}
	if !grantee.Has(domain.PermProvidersTest) {
		t.Error("受限用户 must hold providers.test to run a task on a granted provider")
	}
	if grantee.Has(domain.PermProvidersEdit) {
		t.Error("受限用户 must not hold providers.edit; the automation switch is configuration")
	}

	viewer, ok := domain.BuiltinRole(domain.RoleIDViewer)
	if !ok {
		t.Fatal("只读成员 role missing")
	}
	if !viewer.Has(domain.PermProvidersView) {
		t.Error("只读成员 must see the task list")
	}
	if viewer.Has(domain.PermProvidersTest) {
		t.Error("只读成员 must not run tasks")
	}
}
