package gateway

import (
	"os"
	"strings"
)

// The client versions the gateway presents to WorkBuddy as environment
// overrides, so a build that starts being refused can be answered with a
// restart instead of a rebuild.
//
// The defaults below are the versions this gateway was last told about. For
// comparison, the reference gateway maintained against captured traffic
// (ardeyouxipianyi/workbuddy2api-hub, wb_identity.py) records:
//
//	intl desktop 5.8.4  — mitmproxy, 2026-10-16
//	CN desktop   5.8.3  — two-region desktop run, 2026-10-18
//	built-in CLI 2.161.0
//
// Upstream rejects an unrecognised User-Agent with code 10085 "请求不合法", so
// when that starts appearing, set these and restart:
//
//	WORKBUDDY_VERSION_CN=5.8.3 WORKBUDDY_VERSION_INTL=5.8.4 WORKBUDDY_VERSION_CLI=2.161.0
const (
	workBuddyVersionCNEnv   = "WORKBUDDY_VERSION_CN"
	workBuddyVersionIntlEnv = "WORKBUDDY_VERSION_INTL"
	workBuddyVersionCLIEnv  = "WORKBUDDY_VERSION_CLI"
)

// workBuddyVersionOverride reads one version override, falling back to the
// default when it is unset or blank.
func workBuddyVersionOverride(envName, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
		return value
	}
	return fallback
}

// workBuddyDesktopVersion is the desktop build version for one realm.
func workBuddyDesktopVersion(intl bool) string {
	if intl {
		return workBuddyVersionOverride(workBuddyVersionIntlEnv, workBuddyClientVersionIntl)
	}
	return workBuddyVersionOverride(workBuddyVersionCNEnv, workBuddyClientVersionCN)
}

// workBuddyCLIVersionValue is the version of the CLI the desktop client bundles,
// which the User-Agent's last segment and the sign-in page both name.
func workBuddyCLIVersionValue() string {
	return workBuddyVersionOverride(workBuddyVersionCLIEnv, workBuddyCLIVersion)
}
