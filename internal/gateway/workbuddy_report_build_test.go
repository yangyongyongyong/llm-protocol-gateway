package gateway

import "testing"

// The China build reports the measured triple.
func TestWorkBuddyReportBuildForChinaUsesTheMeasuredValues(t *testing.T) {
	t.Setenv(workBuddyReportCommitCNEnv, "")
	t.Setenv(workBuddyReportReleaseDateCNEnv, "")
	build := workBuddyReportBuildFor(workBuddyCNSite)
	if build.Commit != workBuddyReportCommit {
		t.Fatalf("commit = %q", build.Commit)
	}
	if build.ReleaseDate != workBuddyReportReleaseDate {
		t.Fatalf("releaseDate = %d", build.ReleaseDate)
	}
	if !build.Measured {
		t.Fatal("the China triple is measured, not a stand-in")
	}
}

// The two realms are separate releases, so the triple is looked up per realm: an intl
// account must be able to report a different build without a rebuild.
func TestWorkBuddyReportBuildForIntlUsesItsOwnConfiguration(t *testing.T) {
	t.Setenv(workBuddyReportCommitIntlEnv, "0123456789abcdef0123456789abcdef")
	t.Setenv(workBuddyReportReleaseDateIntlEnv, "1793577600000")
	build := workBuddyReportBuildFor(workBuddyIntlSite)
	if build.Commit != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("commit = %q", build.Commit)
	}
	if build.ReleaseDate != 1793577600000 {
		t.Fatalf("releaseDate = %d", build.ReleaseDate)
	}
	if !build.Measured {
		t.Fatal("a configured triple counts as measured")
	}
}

// With nothing captured for intl, the China triple stands in — and says so, rather than
// pretending the value is the intl build's.
func TestWorkBuddyReportBuildForIntlFallsBackAndSaysUnmeasured(t *testing.T) {
	t.Setenv(workBuddyReportCommitIntlEnv, "")
	t.Setenv(workBuddyReportReleaseDateIntlEnv, "")
	build := workBuddyReportBuildFor(workBuddyIntlSite)
	if build.Commit != workBuddyReportCommit || build.ReleaseDate != workBuddyReportReleaseDate {
		t.Fatalf("fallback = %+v, want the China values", build)
	}
	if build.Measured {
		t.Fatal("an unconfigured intl triple must not claim to be measured")
	}
}

// A half-configured intl build is not a build: both fields are needed, so an incomplete
// pair falls back instead of mixing one real value with the other realm's.
func TestWorkBuddyReportBuildForIntlNeedsBothFields(t *testing.T) {
	t.Setenv(workBuddyReportCommitIntlEnv, "0123456789abcdef0123456789abcdef")
	t.Setenv(workBuddyReportReleaseDateIntlEnv, "")
	build := workBuddyReportBuildFor(workBuddyIntlSite)
	if build.Measured {
		t.Fatal("a commit without a release date counted as measured")
	}
	if build.Commit != workBuddyReportCommit {
		t.Fatalf("commit = %q, want the consistent fallback pair", build.Commit)
	}
}

// The China values are overridable too, so a newer CN build can be reported without a
// release.
func TestWorkBuddyReportBuildForChinaHonoursOverrides(t *testing.T) {
	t.Setenv(workBuddyReportCommitCNEnv, "feedfacefeedfacefeedfacefeedface")
	t.Setenv(workBuddyReportReleaseDateCNEnv, "1800000000000")
	build := workBuddyReportBuildFor(workBuddyCNSite)
	if build.Commit != "feedfacefeedfacefeedfacefeedface" || build.ReleaseDate != 1800000000000 {
		t.Fatalf("build = %+v", build)
	}

	// A malformed value is ignored rather than written into a report.
	t.Setenv(workBuddyReportReleaseDateCNEnv, "not-a-number")
	if got := workBuddyReportBuildFor(workBuddyCNSite).ReleaseDate; got != workBuddyReportReleaseDate {
		t.Fatalf("malformed override was used: %d", got)
	}
}
