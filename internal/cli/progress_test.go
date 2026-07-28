package cli

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tobi404/barracks/internal/progress"
	"github.com/tobi404/barracks/internal/testutil"
)

// Like the flavor line, the progress indicator is invisible to an ordinary test
// by design: nothing is announced until an operation has been running longer
// than the reveal threshold, and nothing is animated unless stderr is a
// terminal. So every test here forces both conditions deliberately - a feature
// nothing can see is a feature nobody is checking.

// watched is a harness with a loadout trained and equipped, wound so that any
// real work at all is slow enough to be announced, on a terminal.
func watched(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"))
	h.errTty = true
	h.progressAfter = time.Millisecond
	return h
}

// escapes reports whether a stream carries anything that would corrupt a log
// file: an escape sequence or a carriage return used to rewrite a line.
func escapes(stream string) bool {
	return strings.Contains(stream, "\x1b") || strings.Contains(stream, "\r")
}

var ansi = regexp.MustCompile("\x1b\\[[0-9?]*[A-Za-z]")

// completed returns the permanent lines a run left behind, in order - what a
// user still sees once the animation has scrolled away.
func completed(stream string) []string {
	var out []string
	for _, line := range strings.Split(stream, "\n") {
		// A live run rewrites its line, so what survives on a row is whatever
		// follows the last carriage return on it.
		if i := strings.LastIndex(line, "\r"); i >= 0 {
			line = line[i+1:]
		}
		line = strings.TrimSpace(ansi.ReplaceAllString(line, ""))
		if rest, ok := strings.CutPrefix(line, progress.DoneMarker); ok {
			out = append(out, strings.TrimSpace(rest))
		}
	}
	return out
}

// TestSlowWorkIsAnnouncedAndFastWorkIsNot is acceptance criterion 1, from the
// command tree down. Both halves matter: an indicator that never appeared would
// pass the second half on its own.
func TestSlowWorkIsAnnouncedAndFastWorkIsNot(t *testing.T) {
	h := newHarness(t)
	h.errTty = true
	h.mustRun("train", "frontend")

	// Slow: anything that has to reach the store at all.
	h.progressAfter = time.Millisecond
	_, errb, err := h.run("equip", "frontend", h.sourceArg("skills"))
	if err != nil {
		t.Fatalf("equip: %v\n%s", err, errb)
	}
	if !strings.Contains(errb, "fetching…") {
		t.Errorf("a fetch was never announced:\n%q", errb)
	}
	if got := completed(errb); len(got) == 0 {
		t.Errorf("the fetch left no permanent line:\n%q", errb)
	}

	// Fast: the same work, under a threshold nothing here can exceed. This is
	// the warm-store case a user sees constantly, and it must look instant.
	h.progressAfter = time.Hour
	h.mustRun("train", "backend")
	_, errb, err = h.run("equip", "backend", h.sourceArg("skills"))
	if err != nil {
		t.Fatalf("equip backend: %v\n%s", err, errb)
	}
	if strings.Contains(errb, "fetching…") || completed(errb) != nil || escapes(errb) {
		t.Errorf("fast work printed something:\n%q", errb)
	}
}

// TestNoEscapeSequencesOffATerminal is acceptance criterion 2, and it is the
// one that breaks tools rather than merely annoying people: an escape code in a
// CI log or a redirected file is corruption. It is checked on every command,
// because the indicator lives below all of them.
func TestNoEscapeSequencesOffATerminal(t *testing.T) {
	h := newHarness(t)
	h.errTty = false
	h.progressAfter = time.Millisecond
	h.mustRun("train", "frontend")

	var firstEquip string
	for _, args := range [][]string{
		{"equip", "frontend", h.sourceArg("skills")},
		{"upgrade", "frontend"},
		{"upgrade", "frontend", "--dry-run"},
		{"spawn", "frontend"},
		{"recall", "frontend"},
		{"run", "frontend", "--", "sh", "-c", "true"},
		{"list", "--verbose"},
		{"deployed"},
		// garrison comes last: a garrisoned path cannot also be spawned into,
		// so anything after it here would be refused for that reason alone.
		{"garrison", "frontend"},
		{"inspect"},
	} {
		out, errb, err := h.run(args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s\n%s", args, err, out, errb)
		}
		if escapes(errb) {
			t.Errorf("%v wrote escape sequences to a redirected stderr:\n%q", args, errb)
		}
		if escapes(out) {
			t.Errorf("%v wrote escape sequences to stdout:\n%q", args, out)
		}
		if firstEquip == "" {
			firstEquip = errb
		}
	}

	// And the plain lines are still there, which is the whole point of
	// degrading rather than falling silent: a redirected run is still readable,
	// it just is not animated.
	if !strings.Contains(firstEquip, "fetching…") {
		t.Errorf("a redirected fetch said nothing at all:\n%q", firstEquip)
	}
	if got := completed(firstEquip); len(got) == 0 {
		t.Errorf("a redirected fetch left no completed line:\n%q", firstEquip)
	}
}

// TestProgressNeverReachesStdout: stdout carries data. `barracks list | grep
// react` and anything parsing a report must be untouched.
func TestProgressNeverReachesStdout(t *testing.T) {
	h := watched(t)
	h.mustRun("train", "backend")

	out, errb, err := h.run("equip", "backend", h.sourceArg(""))
	if err != nil {
		t.Fatalf("equip: %v\n%s", err, errb)
	}
	if strings.Contains(out, "fetching…") || completed(out) != nil || escapes(out) {
		t.Errorf("progress reached stdout:\n%q", out)
	}
	if !strings.Contains(out, "equipped backend") {
		t.Errorf("the real report is missing:\n%q", out)
	}
	if completed(errb) == nil {
		t.Errorf("the fetch left no permanent line on stderr:\n%q", errb)
	}
}

// TestStdoutIsUnchangedByProgress is acceptance criterion 6 of the original
// brief restated for this feature: this adds progress reporting and nothing
// else. Exit codes and stdout are untouched.
func TestStdoutIsUnchangedByProgress(t *testing.T) {
	h := watched(t)
	h.mustRun("spawn", "frontend")

	for _, args := range [][]string{
		{"upgrade", "frontend"},
		{"deployed"},
		{"list", "--verbose"},
		{"inspect"},
	} {
		h.errTty, h.progressAfter = false, time.Hour
		wantOut, _, err := h.run(args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		h.errTty, h.progressAfter = true, time.Millisecond
		gotOut, _, err := h.run(args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if gotOut != wantOut {
			t.Errorf("%v changed stdout:\n got: %q\nwant: %q", args, gotOut, wantOut)
		}
	}
}

// TestQuietSuppressesProgress is acceptance criterion 5. The environment
// variable covers it too: --quiet is already one switch for both the flavor
// line and this, so the standing form of that flag has to mean the same thing.
func TestQuietSuppressesProgress(t *testing.T) {
	h := watched(t)

	silent := func(what string, args ...string) {
		t.Helper()
		h.mustRun("train", what)
		out, errb, err := h.run(append([]string{"equip", what, h.sourceArg("skills")}, args...)...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, errb)
		}
		if errb != "" {
			t.Errorf("%v still reported progress:\n%q", args, errb)
		}
		if !strings.Contains(out, "equipped "+what) {
			t.Errorf("%v suppressed the real report too:\n%q", args, out)
		}
	}

	silent("a", "--quiet")
	silent("b", "-q")

	for _, value := range []string{"1", "true", "yes", "anything"} {
		h.env[EnvQuiet] = value
		silent("env-" + value)
	}
	// And a value that conventionally means "off" leaves it on.
	h.env[EnvQuiet] = "0"
	h.mustRun("train", "still-on")
	_, errb, err := h.run("equip", "still-on", h.sourceArg(""))
	if err != nil {
		t.Fatalf("equip: %v\n%s", err, errb)
	}
	if errb == "" {
		t.Errorf("%s=0 silenced progress, want it left on", EnvQuiet)
	}
}

// TestMultiSourceUpgradeLeavesOneLinePerSource is acceptance criterion 6: the
// display is a sequence of completed lines with a single live one at the end,
// not a dashboard.
func TestMultiSourceUpgradeLeavesOneLinePerSource(t *testing.T) {
	h := watched(t)
	second := testutil.NewSkillRepo(t, filepath.Join(h.root, "second"), testutil.Skill{Path: "skills/vue"})
	h.mustRun("equip", "frontend", second.Dir)

	_, errb, err := h.run("upgrade", "frontend")
	if err != nil {
		t.Fatalf("upgrade: %v\n%s", err, errb)
	}

	lines := completed(errb)
	if len(lines) != 2 {
		t.Fatalf("want one permanent line per source, got %d:\n%q\n%v", len(lines), errb, lines)
	}
	if lines[0] == lines[1] {
		t.Errorf("both permanent lines describe the same source: %v", lines)
	}
	// Nothing may address a line other than the one being animated, which is
	// what makes the completed lines above it permanent.
	for _, forbidden := range []string{"\x1b[A", "\x1b[B", "\x1b[s", "\x1b[u", "\x1b[J"} {
		if strings.Contains(errb, forbidden) {
			t.Errorf("the display moved the cursor off its own line (%q):\n%q", forbidden, errb)
		}
	}
	// And the cursor is handed back however many steps ran.
	if got, want := strings.Count(errb, "\x1b[?25l"), strings.Count(errb, "\x1b[?25h"); got != want {
		t.Errorf("hid the cursor %d times and showed it %d:\n%q", got, want, errb)
	}
}

// TestAFailedFetchLeavesNoProgressLine: the command prints the error, and a
// second, differently-worded report of one failure is worse than none.
func TestAFailedFetchLeavesNoProgressLine(t *testing.T) {
	h := watched(t)

	out, errb, err := h.run("equip", "frontend", h.sourceArg("")+"#no-such-ref")
	if err == nil {
		t.Fatalf("equipping a missing ref should have failed:\n%s", out)
	}
	if got := completed(errb); got != nil {
		t.Errorf("a failure left a completed line: %v\n%q", got, errb)
	}
	if strings.Count(errb, "\x1b[?25l") != strings.Count(errb, "\x1b[?25h") {
		t.Errorf("a failure left the cursor hidden:\n%q", errb)
	}
}
