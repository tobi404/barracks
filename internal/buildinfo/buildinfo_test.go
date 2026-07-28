package buildinfo

import (
	"runtime/debug"
	"strings"
	"testing"
)

// stamp sets the link-time variables for one test and restores them after.
func stamp(t *testing.T, version, commit, date string) {
	t.Helper()
	oldV, oldC, oldD := Version, Commit, Date
	Version, Commit, Date = version, commit, date
	t.Cleanup(func() { Version, Commit, Date = oldV, oldC, oldD })
}

func noBuildInfo() (*debug.BuildInfo, bool) { return nil, false }

func buildInfo(mainVersion string, settings ...debug.BuildSetting) func() (*debug.BuildInfo, bool) {
	return func() (*debug.BuildInfo, bool) {
		bi := &debug.BuildInfo{Settings: settings}
		bi.Main.Version = mainVersion
		return bi, true
	}
}

func TestStampedValuesWin(t *testing.T) {
	stamp(t, "v1.4.0", "abc1234", "2026-07-28T10:00:00Z")

	version, commit, date := resolve(buildInfo("v9.9.9", debug.BuildSetting{Key: "vcs.revision", Value: "deadbeef"}))

	if version != "v1.4.0" || commit != "abc1234" || date != "2026-07-28T10:00:00Z" {
		t.Errorf("resolve() = %q, %q, %q; want the link-time values", version, commit, date)
	}
}

func TestUnstampedFallsBackToModuleBuildInfo(t *testing.T) {
	stamp(t, "", "", "")

	version, commit, date := resolve(buildInfo(
		"v1.2.3",
		debug.BuildSetting{Key: "vcs.revision", Value: "cafebabe"},
		debug.BuildSetting{Key: "vcs.time", Value: "2026-01-02T03:04:05Z"},
		debug.BuildSetting{Key: "vcs.modified", Value: "false"},
	))

	if version != "v1.2.3" || commit != "cafebabe" || date != "2026-01-02T03:04:05Z" {
		t.Errorf("resolve() = %q, %q, %q; want the module build info", version, commit, date)
	}
}

// A working-tree build reports "(devel)", which is not a version anyone can
// install - it must read as a development build, not as a release.
func TestDevelModuleVersionIsNotReportedAsARelease(t *testing.T) {
	stamp(t, "", "", "")

	version, _, _ := resolve(buildInfo("(devel)"))

	if version != devVersion {
		t.Errorf("resolve() version = %q, want %q", version, devVersion)
	}
}

func TestNothingKnownIsSaidPlainly(t *testing.T) {
	stamp(t, "", "", "")

	version, commit, date := resolve(noBuildInfo)

	if version != devVersion || commit != unknown || date != unknown {
		t.Errorf("resolve() = %q, %q, %q; want %q, %q, %q",
			version, commit, date, devVersion, unknown, unknown)
	}
}

// A partial stamp is the realistic failure: one -X flag typo'd or dropped. The
// missing field must still be recovered rather than dragging the rest down.
func TestPartialStampIsCompletedFromBuildInfo(t *testing.T) {
	stamp(t, "v2.0.0", "", "")

	version, commit, date := resolve(buildInfo(
		"(devel)",
		debug.BuildSetting{Key: "vcs.revision", Value: "1234abcd"},
		debug.BuildSetting{Key: "vcs.time", Value: "2026-05-05T05:05:05Z"},
	))

	if version != "v2.0.0" || commit != "1234abcd" || date != "2026-05-05T05:05:05Z" {
		t.Errorf("resolve() = %q, %q, %q", version, commit, date)
	}
}

func TestStringNamesAllThreeFields(t *testing.T) {
	stamp(t, "v3.1.0", "0badc0de", "2026-06-06T06:06:06Z")

	got := String()

	for _, want := range []string{"v3.1.0", "commit 0badc0de", "built 2026-06-06T06:06:06Z"} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, missing %q", got, want)
		}
	}
}
