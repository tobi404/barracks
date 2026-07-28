package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tobi404/barracks/internal/testutil"
	"github.com/tobi404/barracks/internal/voice"
)

// The flavor line is invisible to an ordinary test by design - it is off unless
// stdout is a terminal - so every test here forces that condition deliberately
// rather than hoping to observe it. A feature nothing can see is a feature
// nobody is checking.

// flavor returns the flavor lines in a stream, in order.
func flavor(stream string) []string {
	var out []string
	for _, line := range strings.Split(stream, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), voice.Marker) {
			out = append(out, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), voice.Marker)))
		}
	}
	return out
}

// equipped is a harness with a loadout trained, equipped, and ready to spawn,
// with the terminal condition forced on.
func equipped(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"))
	h.tty = true
	return h
}

// TestFlavorFollowsASuccessfulCommand is acceptance criterion 1: one line, on
// stderr, after the real report.
func TestFlavorFollowsASuccessfulCommand(t *testing.T) {
	h := equipped(t)

	out, errb, err := h.run("spawn", "frontend")
	if err != nil {
		t.Fatalf("spawn: %v\n%s\n%s", err, out, errb)
	}
	lines := flavor(errb)
	if len(lines) != 1 {
		t.Fatalf("want exactly one flavor line, got %d:\n%s", len(lines), errb)
	}
	if !strings.Contains(out, "spawned frontend") {
		t.Errorf("the real report is missing:\n%s", out)
	}
	// Data to stdout, flavor to stderr - so `barracks list | grep react` is
	// never polluted even interactively.
	if flavor(out) != nil {
		t.Errorf("flavor reached stdout:\n%s", out)
	}
}

// TestEveryStateChangingCommandSpeaks walks the commands that change something
// and confirms each one has a voice.
func TestEveryStateChangingCommandSpeaks(t *testing.T) {
	h := newHarness(t)
	h.tty = true

	steps := []struct {
		name string
		args []string
	}{
		{"train", []string{"train", "frontend"}},
		{"equip", []string{"equip", "frontend", h.sourceArg("skills")}},
		{"upgrade", []string{"upgrade", "frontend"}},
		// `run` spawns and recalls inside one invocation, so it has to come
		// while nothing else is occupying the skills directory.
		{"run", []string{"run", "frontend", "--", "sh", "-c", "true"}},
		{"spawn", []string{"spawn", "frontend"}},
		{"recall", []string{"recall", "frontend"}},
		{"garrison", []string{"garrison", "frontend"}},
	}

	for _, s := range steps {
		out, errb, err := h.run(s.args...)
		if err != nil {
			t.Fatalf("%s: %v\n%s\n%s", s.name, err, out, errb)
		}
		if got := flavor(errb); len(got) == 0 {
			t.Errorf("%s said nothing:\n%s", s.name, errb)
		}
	}
}

// TestDataCommandsStaySilent is the other half of acceptance criterion 1: a
// command that is purely a report gets run constantly, and a voice line there
// is noise rather than character.
func TestDataCommandsStaySilent(t *testing.T) {
	h := equipped(t)
	h.mustRun("spawn", "frontend")

	for _, args := range [][]string{
		{"list"},
		{"list", "--verbose"},
		{"deployed"},
		{"inspect"},
		{"targets"},
	} {
		out, errb, err := h.run(args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s\n%s", args, err, out, errb)
		}
		if got := flavor(errb); got != nil {
			t.Errorf("%v spoke when it should be silent: %v", args, got)
		}
	}
}

// TestNoFlavorWhenStdoutIsNotATerminal is acceptance criterion 2, and the
// keystone of the whole design: piped, redirected or CI means no flavor, with
// no flag needed.
func TestNoFlavorWhenStdoutIsNotATerminal(t *testing.T) {
	h := equipped(t)
	h.tty = false

	for _, args := range [][]string{
		{"spawn", "frontend"},
		{"upgrade", "frontend"},
		{"recall", "frontend"},
		{"garrison", "frontend"},
	} {
		out, errb, err := h.run(args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s\n%s", args, err, out, errb)
		}
		if got := flavor(errb); got != nil {
			t.Errorf("%v spoke off a terminal: %v", args, got)
		}
		if got := flavor(out); got != nil {
			t.Errorf("%v spoke on stdout: %v", args, got)
		}
	}
}

// TestOutputIsIdenticalWithAndWithoutATerminal is acceptance criterion 6: the
// flavor adds a line, it changes nothing. Anything parsing barracks output
// today must be unaffected.
func TestOutputIsIdenticalWithAndWithoutATerminal(t *testing.T) {
	h := equipped(t)
	h.mustRun("spawn", "frontend")

	// Commands that report the same thing however often they are run, so the
	// only difference between the two invocations can be the flavor.
	for _, args := range [][]string{
		{"upgrade", "frontend"},
		{"deployed"},
		{"list", "--verbose"},
		{"inspect"},
	} {
		h.tty = false
		wantOut, wantErr, err := h.run(args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		h.tty = true
		gotOut, gotErr, err := h.run(args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if gotOut != wantOut {
			t.Errorf("%v changed stdout on a terminal:\n got: %q\nwant: %q", args, gotOut, wantOut)
		}
		if got := strip(gotErr); got != wantErr {
			t.Errorf("%v changed stderr beyond the flavor line:\n got: %q\nwant: %q", args, got, wantErr)
		}
	}
}

// strip removes the flavor lines from a stream, leaving what barracks would
// have written without a voice.
func strip(stream string) string {
	var kept []string
	for _, line := range strings.Split(stream, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), voice.Marker) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// TestNoFlavorOnFailure is acceptance criterion 3. A quip on an error reads as
// the tool laughing at the user.
func TestNoFlavorOnFailure(t *testing.T) {
	h := equipped(t)

	for _, args := range [][]string{
		{"spawn", "nonexistent"},
		{"equip", "frontend", "not a source"},
		{"recall", "frontend"}, // nothing is deployed
		{"run", "frontend", "--", "sh", "-c", "exit 7"},
	} {
		out, errb, err := h.run(args...)
		if err == nil {
			t.Fatalf("%v should have failed:\n%s", args, out)
		}
		if got := flavor(errb); got != nil {
			t.Errorf("%v spoke on a failure: %v\n%s", args, got, errb)
		}
	}
}

// TestQuietSuppressesTheFlavor is acceptance criterion 4.
func TestQuietSuppressesTheFlavor(t *testing.T) {
	h := equipped(t)

	for _, flag := range []string{"--quiet", "-q"} {
		out, errb, err := h.run("spawn", "frontend", flag)
		if err != nil {
			t.Fatalf("%s: %v\n%s\n%s", flag, err, out, errb)
		}
		if got := flavor(errb); got != nil {
			t.Errorf("%s did not suppress the flavor: %v", flag, got)
		}
		if !strings.Contains(out, "spawned frontend") {
			t.Errorf("%s suppressed the real report too:\n%s", flag, out)
		}
		h.mustRun("recall", "frontend", "--quiet")
	}

	// And the environment variable turns it off permanently.
	for _, value := range []string{"1", "true", "yes", "anything"} {
		h.env[EnvQuiet] = value
		_, errb, err := h.run("spawn", "frontend")
		if err != nil {
			t.Fatalf("%s=%s: %v", EnvQuiet, value, err)
		}
		if got := flavor(errb); got != nil {
			t.Errorf("%s=%s did not suppress the flavor: %v", EnvQuiet, value, got)
		}
		h.mustRun("recall", "frontend")
	}
	// Values that conventionally mean "off" leave the voice on.
	for _, value := range []string{"", "0", "false", "no"} {
		h.env[EnvQuiet] = value
		_, errb, err := h.run("spawn", "frontend")
		if err != nil {
			t.Fatalf("%s=%q: %v", EnvQuiet, value, err)
		}
		if got := flavor(errb); got == nil {
			t.Errorf("%s=%q silenced the voice, want it left on", EnvQuiet, value)
		}
		h.mustRun("recall", "frontend")
	}
}

// TestRepeatingACommandEscalatesThenResets is acceptance criterion 5, driven
// through the real command tree.
func TestRepeatingACommandEscalatesThenResets(t *testing.T) {
	h := equipped(t)
	h.rnd = func() uint64 { return 0 }

	say := func() string {
		_, errb, err := h.run("upgrade", "frontend")
		if err != nil {
			t.Fatalf("upgrade: %v\n%s", err, errb)
		}
		lines := flavor(errb)
		if len(lines) != 1 {
			t.Fatalf("want one flavor line, got %d:\n%s", len(lines), errb)
		}
		h.now = h.now.Add(time.Second)
		return lines[0]
	}

	var seen []string
	for i := 0; i < 4; i++ {
		seen = append(seen, say())
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] == seen[i-1] {
			t.Errorf("repeat %d said the same thing as the one before it: %q", i, seen[i])
		}
	}

	// Returning after a quiet period greets you fresh rather than remembering
	// yesterday's pestering.
	h.now = h.now.Add(voice.Window)
	if got := say(); got != seen[0] {
		t.Errorf("after the quiet window: %q, want the fresh %q", got, seen[0])
	}
}

// TestAPreviewNeitherSpeaksNorEscalates: a command that by design changes
// nothing has nothing to acknowledge. Staying silent is only half of it - a
// preview that still spent an escalation step would answer the first genuine
// change with the wearier line, which is the same falsehood with the evidence
// hidden.
func TestAPreviewNeitherSpeaksNorEscalates(t *testing.T) {
	h := equipped(t)
	h.rnd = func() uint64 { return 0 }

	_, errb, err := h.run("upgrade", "frontend", "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, errb)
	}
	if got := flavor(errb); got != nil {
		t.Errorf("a preview spoke: %v", got)
	}
	h.now = h.now.Add(time.Second)

	_, errb, err = h.run("upgrade", "frontend")
	if err != nil {
		t.Fatalf("upgrade: %v\n%s", err, errb)
	}
	afterPreview := flavor(errb)
	if len(afterPreview) != 1 {
		t.Fatalf("want one flavor line, got %d:\n%s", len(afterPreview), errb)
	}

	// What a genuinely first upgrade says is what the one after a preview must
	// have said, so the quiet window gives us that line to compare against.
	h.now = h.now.Add(voice.Window)
	_, errb, err = h.run("upgrade", "frontend")
	if err != nil {
		t.Fatalf("upgrade: %v\n%s", err, errb)
	}
	fresh := flavor(errb)
	if len(fresh) != 1 {
		t.Fatalf("want one flavor line, got %d:\n%s", len(fresh), errb)
	}
	if afterPreview[0] != fresh[0] {
		t.Errorf("the command after a preview said %q, want the fresh %q", afterPreview[0], fresh[0])
	}
}

// TestEscalationIsPerLoadout: pestering one loadout must not make another one
// weary.
func TestEscalationIsPerLoadout(t *testing.T) {
	h := equipped(t)
	h.rnd = func() uint64 { return 0 }
	h.mustRun("train", "backend")
	h.mustRun("equip", "backend", h.sourceArg("skills"))

	first := func(name string) string {
		_, errb, err := h.run("upgrade", name)
		if err != nil {
			t.Fatalf("upgrade %s: %v", name, err)
		}
		h.now = h.now.Add(time.Second)
		return flavor(errb)[0]
	}

	fresh := first("frontend")
	first("frontend")
	first("frontend")
	if got := first("backend"); got != fresh {
		t.Errorf("a different loadout said %q, want the fresh %q", got, fresh)
	}
}

// TestEscalationIsPerRepository is the defect this all exists to prevent: the
// wearier lines make claims about a place, so spawning the same loadout into a
// second repository must start fresh. Being told "the same front again" about a
// repository the skills have never been in is a falsehood, not flavor.
func TestEscalationIsPerRepository(t *testing.T) {
	h := equipped(t)
	h.rnd = func() uint64 { return 0 }
	other := testutil.NewGitRepo(t, filepath.Join(h.root, "other"))
	testutil.WriteFile(t, filepath.Join(other.Dir, "README.md"), "hello\n")
	other.Commit(t, "initial")

	spawn := func() string {
		_, errb, err := h.run("spawn", "frontend")
		if err != nil {
			t.Fatalf("spawn in %s: %v\n%s", h.workingDir(), err, errb)
		}
		lines := flavor(errb)
		if len(lines) != 1 {
			t.Fatalf("want one flavor line, got %d:\n%s", len(lines), errb)
		}
		h.mustRun("recall", "frontend")
		h.now = h.now.Add(time.Second)
		return lines[0]
	}

	fresh := spawn()

	// A different repository is a first spawn there, however recently the same
	// loadout was spawned somewhere else.
	h.cwd = other.Dir
	if got := spawn(); got != fresh {
		t.Errorf("a second repository said %q, want a fresh %q", got, fresh)
	}

	// So is a --global install, which is not in a repository at all.
	h.cwd = ""
	if _, errb, err := h.run("spawn", "frontend", "--global"); err != nil {
		t.Fatalf("global spawn: %v\n%s", err, errb)
	} else if got := flavor(errb); len(got) != 1 || got[0] != fresh {
		t.Errorf("a global spawn said %v, want a fresh %q", got, fresh)
	}
	h.mustRun("recall", "frontend", "--global")
	h.now = h.now.Add(time.Second)

	// And repeating in the first repository still escalates.
	if got := spawn(); got == fresh {
		t.Errorf("a repeat in the same repository said %q again, want it wearier", got)
	}
}

// TestRunningFromASubdirectoryIsTheSamePlace: the escalation keys on the
// resolved repository root, so where in the tree you stand is not part of it.
func TestRunningFromASubdirectoryIsTheSamePlace(t *testing.T) {
	h := equipped(t)
	h.rnd = func() uint64 { return 0 }
	sub := filepath.Join(h.work.Dir, "packages", "web")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	spawn := func() string {
		_, errb, err := h.run("spawn", "frontend")
		if err != nil {
			t.Fatalf("spawn from %s: %v\n%s", h.workingDir(), err, errb)
		}
		h.mustRun("recall", "frontend")
		h.now = h.now.Add(time.Second)
		return flavor(errb)[0]
	}

	fresh := spawn()
	h.cwd = sub
	if got := spawn(); got == fresh {
		t.Errorf("a spawn from a subdirectory said %q, want the same repository's next step", got)
	}
}

// TestFlavorStateIsTheOnlyThingItTouches: the escalation record must never
// affect anything but which string is printed.
func TestFlavorStateIsTheOnlyThingItTouches(t *testing.T) {
	h := equipped(t)
	statusBefore := h.work.Status(t)
	excludeBefore := h.work.ReadExclude(t)

	for i := 0; i < 3; i++ {
		h.mustRun("spawn", "frontend")
		h.mustRun("recall", "frontend")
		h.now = h.now.Add(time.Second)
	}

	if got := h.work.Status(t); got != statusBefore {
		t.Errorf("git status changed:\n%s", got)
	}
	if got := h.work.ReadExclude(t); got != excludeBefore {
		t.Errorf(".git/info/exclude changed:\n%s", got)
	}
}
