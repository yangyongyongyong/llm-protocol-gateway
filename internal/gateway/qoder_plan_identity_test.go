package gateway

import (
	"encoding/json"
	"testing"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// The userinfo endpoint answers the plan identity under two different spellings
// depending on the site. Both have to be read: everything downstream — the COSY
// blob, the campaign filter, the model list — is decided by what that identity
// claims.
func TestQoderUserInfoReadsEitherPlanSpelling(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"snake case", `{"id":"u1","user_type":"enterprise","organization_id":"org-1","organization_name":"Acme"}`},
		{"camel case", `{"id":"u1","userType":"enterprise","organizationId":"org-1","organizationName":"Acme"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var info qoderUserInfo
			if err := json.Unmarshal([]byte(tc.body), &info); err != nil {
				t.Fatal(err)
			}
			if got := info.userType(); got != "enterprise" {
				t.Fatalf("userType = %q", got)
			}
			if got := info.organizationID(); got != "org-1" {
				t.Fatalf("organizationID = %q", got)
			}
			if got := info.organizationName(); got != "Acme" {
				t.Fatalf("organizationName = %q", got)
			}
		})
	}
}

// A personal account names none of these, and applying it must not wipe what an
// earlier sign-in already learned.
func TestQoderPlanIdentityLeavesUnknownsAlone(t *testing.T) {
	credential := domain.QoderCredential{UserType: "enterprise", OrganizationID: "org-1", OrganizationName: "Acme"}
	var personal qoderUserInfo
	personal.applyPlanIdentity(&credential)
	if credential.UserType != "enterprise" || credential.OrganizationID != "org-1" || credential.OrganizationName != "Acme" {
		t.Fatalf("an answer without plan fields must leave the credential alone: %+v", credential)
	}
}

// The credential's plan identity is what the COSY envelope is signed for, so it
// has to survive the hop from the stored provider to the signing account.
func TestQoderAccountCarriesThePlanIdentity(t *testing.T) {
	provider := domain.Provider{
		ID:       "q1",
		AuthType: domain.AuthTypeQoderPAT,
		QoderPAT: &domain.QoderCredential{
			UID:              "uid-1",
			AccessToken:      "token",
			UserType:         "enterprise",
			OrganizationID:   "org-1",
			OrganizationName: "Acme",
		},
	}
	account := qoderAccountOf(provider)
	if account.UserType != "enterprise" || account.OrganizationID != "org-1" || account.OrganizationName != "Acme" {
		t.Fatalf("the signing account lost the plan identity: %+v", account)
	}
}
