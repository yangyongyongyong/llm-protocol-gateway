package gateway

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
)

// The desktop build triple a report claims to come from.
//
// Every desktop event carries `commit`, `releaseDate` and `ideVersion`, and a report
// whose triple disagrees with itself is a worse signal than one whose values are merely
// stale: the build is presented as a real installed app, so the three fields have to
// describe the same app.
//
// The two builds are separate releases. `ideVersion` already follows the realm
// (5.9.3 CN / 5.9.4 intl, see workBuddySite.clientVersion). The **commit and
// releaseDate do not**: they are single constants captured from the China build, so an
// international account currently stamps a China build's commit — the mismatch the
// reference implementation calls out (magpie packages/workbuddy, where the triple is
// per-realm).
//
// There is no captured international triple here, and inventing a commit hash is worse
// than reusing a measured one: a hash that never shipped is a device claiming a build
// that does not exist. So this file makes the triple per-realm and **configurable**,
// keeps the measured China values as the China default, and says out loud — once per
// process — when an intl account is reporting with China's values. Whoever captures the
// international build sets two environment variables and the mismatch is gone, with no
// rebuild:
//
//	WORKBUDDY_REPORT_COMMIT_INTL=<commit>
//	WORKBUDDY_REPORT_RELEASEDATE_INTL=<ms epoch>
//	WORKBUDDY_REPORT_COMMIT_CN=<commit>            (optional, same idea)
//	WORKBUDDY_REPORT_RELEASEDATE_CN=<ms epoch>
const (
	workBuddyReportCommitCNEnv        = "WORKBUDDY_REPORT_COMMIT_CN"
	workBuddyReportReleaseDateCNEnv   = "WORKBUDDY_REPORT_RELEASEDATE_CN"
	workBuddyReportCommitIntlEnv      = "WORKBUDDY_REPORT_COMMIT_INTL"
	workBuddyReportReleaseDateIntlEnv = "WORKBUDDY_REPORT_RELEASEDATE_INTL"
)

// workBuddyReportBuild is one realm's build triple.
type workBuddyReportBuild struct {
	Commit      string
	ReleaseDate int64
	// Measured is false when the commit/releaseDate are the other realm's values
	// standing in for a build nobody captured.
	Measured bool
}

// workBuddyIntlTripleWarned keeps the "unmeasured" warning to one per process.
var workBuddyIntlTripleWarned sync.Once

// workBuddyReportBuildFor returns the triple a report from this build should claim.
//
// Order: the realm's own environment override, then the measured default for that
// realm, then — for intl only — the China values, flagged as unmeasured.
func workBuddyReportBuildFor(site workBuddySite) workBuddyReportBuild {
	cn := workBuddyReportBuild{
		Commit:      workBuddyReportCommit,
		ReleaseDate: workBuddyReportReleaseDate,
		Measured:    true,
	}
	if custom := strings.TrimSpace(os.Getenv(workBuddyReportCommitCNEnv)); custom != "" {
		cn.Commit = custom
	}
	if millis, ok := workBuddyEnvMillis(workBuddyReportReleaseDateCNEnv); ok {
		cn.ReleaseDate = millis
	}
	if !site.intl {
		return cn
	}

	intl := workBuddyReportBuild{}
	if custom := strings.TrimSpace(os.Getenv(workBuddyReportCommitIntlEnv)); custom != "" {
		intl.Commit = custom
	}
	if millis, ok := workBuddyEnvMillis(workBuddyReportReleaseDateIntlEnv); ok {
		intl.ReleaseDate = millis
	}
	if intl.Commit != "" && intl.ReleaseDate != 0 {
		intl.Measured = true
		return intl
	}
	// Nothing was captured for this build. Reusing the China triple keeps the report
	// acceptable today; the warning is what makes the gap visible instead of hidden.
	workBuddyIntlTripleWarned.Do(func() {
		warnWorkBuddyIntlBuildTripleUnmeasured()
	})
	// Returned as unmeasured: the values are China's, standing in for a build nobody
	// captured, and a caller that looks must be able to see that.
	return workBuddyReportBuild{Commit: cn.Commit, ReleaseDate: cn.ReleaseDate, Measured: false}
}

// workBuddyEnvMillis reads a millisecond epoch from the environment.
func workBuddyEnvMillis(name string) (int64, bool) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0, false
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, false
	}
	return value, true
}

// warnWorkBuddyIntlBuildTripleUnmeasured says it once, in the log an operator reads.
func warnWorkBuddyIntlBuildTripleUnmeasured() {
	slog.Warn("workbuddy: international build triple not captured, reporting with the China values",
		"fix", workBuddyReportCommitIntlEnv+" + "+workBuddyReportReleaseDateIntlEnv,
		"effect", "the intl account stamps a build it did not install")
}
