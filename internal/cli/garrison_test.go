package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tobi404/barracks/internal/garrison"
	"github.com/tobi404/barracks/internal/testutil"
)

// garrisonDir is the committed skills directory for the default target.
func (h *harness) garrisonPath(rel string) string {
	return filepath.Join(h.work.Dir, filepath.FromSlash(rel))
}

func (h *harness) lockPath() string { return filepath.Join(h.work.Dir, garrison.LockName) }

// equipped trains and equips a loadout from the fixture source repository.
func (h *harness) equipped(name string, args ...string) {
	h.t.Helper()
	h.mustRun("train", name)
	h.mustRun(append([]string{"equip", name, h.sourceArg("skills")}, args...)...)
}

// TestGarrisonLifecycle is the committed tier end to end through the real
// command tree: commit, verify, update, and remove.
func TestGarrisonLifecycle(t *testing.T) {
	h := newHarness(t)
	excludeBefore := h.work.ReadExclude(t)
	h.equipped("frontend", "--except", "legacy")

	out := h.mustRun("garrison", "frontend")
	for _, want := range []string{"garrisoned frontend", ".claude/skills/react/SKILL.md", garrison.LockName, "commit these files"} {
		if !strings.Contains(out, want) {
			t.Errorf("garrison output missing %q:\n%s", want, out)
		}
	}
	if testutil.IsSymlink(t, h.garrisonPath(".claude/skills/react")) {
		t.Error("a committed skill was materialised as a symlink")
	}
	if !testutil.Exists(h.lockPath()) {
		t.Fatal("no lockfile was written")
	}

	// Acceptance criterion 2: never excluded, and clean once committed.
	if got := h.work.ReadExclude(t); got != excludeBefore {
		t.Errorf(".git/info/exclude changed; committed files must be visible to git:\n%s", got)
	}
	h.work.Commit(t, "garrison frontend")
	if status := h.work.Status(t); status != "" {
		t.Errorf("git status dirty after committing the garrison:\n%s", status)
	}

	out = h.mustRun("inspect")
	if !strings.Contains(out, "frontend") || !strings.Contains(out, "ok") {
		t.Errorf("inspect on a clean checkout = %q", out)
	}

	out = h.mustRun("deployed")
	for _, want := range []string{"frontend", "[committed]", "never reaped"} {
		if !strings.Contains(out, want) {
			t.Errorf("deployed output missing %q:\n%s", want, out)
		}
	}

	// disband refuses while it is garrisoned here.
	if _, _, err := h.run("disband", "frontend"); err == nil {
		t.Error("disband succeeded while the loadout was garrisoned")
	}

	out = h.mustRun("recall", "frontend")
	if !strings.Contains(out, "recalled the frontend garrison") {
		t.Errorf("recall output = %q", out)
	}
	if testutil.Exists(h.garrisonPath(".claude")) {
		t.Error(".claude survived the recall of the only garrison")
	}
	if testutil.Exists(h.lockPath()) {
		t.Error("the lockfile survived the recall of its only garrison")
	}
	out = h.mustRun("deployed")
	if !strings.Contains(out, "nothing deployed") {
		t.Errorf("deployed after recall = %q", out)
	}
}

// TestInspectExitsNonZeroOnDrift is acceptance criterion 6 at the command layer:
// a mismatch has to be something a build can fail on.
func TestInspectExitsNonZeroOnDrift(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend", "--only", "react")
	h.mustRun("garrison", "frontend")

	testutil.WriteFile(t, h.garrisonPath(".claude/skills/react/SKILL.md"), "edited by hand\n")
	out, _, err := h.run("inspect")
	if err == nil {
		t.Fatal("inspect succeeded on a drifted checkout")
	}
	if !strings.Contains(out, "modified") {
		t.Errorf("inspect output does not name the problem:\n%s", out)
	}
	if !strings.Contains(err.Error(), garrison.LockName) {
		t.Errorf("inspect error = %v, want it to name the lockfile", err)
	}
}

// TestGarrisonRefusesAnEditedFileAndForceOverridesIt is the documented answer to
// a teammate having edited a vendored file.
func TestGarrisonRefusesAnEditedFileAndForceOverridesIt(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend", "--only", "react")
	h.mustRun("garrison", "frontend")

	edited := h.garrisonPath(".claude/skills/react/SKILL.md")
	testutil.WriteFile(t, edited, "my own words\n")

	_, _, err := h.run("garrison", "frontend")
	if err == nil {
		t.Fatal("garrison overwrote a locally edited file")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error does not say how to proceed: %v", err)
	}
	if testutil.ReadFile(t, edited) != "my own words\n" {
		t.Error("the refusal did not preserve the edit")
	}

	h.mustRun("garrison", "frontend", "--force")
	if testutil.ReadFile(t, edited) == "my own words\n" {
		t.Error("--force did not replace the edited file")
	}
	h.mustRun("inspect")
}

// TestGarrisonAndSpawnCannotHoldTheSamePath covers both directions of the rule
// that a repository must never register one path two ways.
func TestGarrisonAndSpawnCannotHoldTheSamePath(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend", "--only", "react")
	h.mustRun("garrison", "frontend")

	h.mustRun("train", "personal")
	h.mustRun("equip", "personal", h.sourceArg("skills"), "--only", "react")
	_, _, err := h.run("spawn", "personal")
	if err == nil {
		t.Fatal("a personal spawn landed on committed paths")
	}
	if !strings.Contains(err.Error(), "committed to this repository") {
		t.Errorf("spawn error does not explain the clash: %v", err)
	}
	if got := h.work.ReadExclude(t); strings.Contains(got, ".claude/skills/react") {
		t.Errorf("the refused spawn still registered an exclude pattern:\n%s", got)
	}

	// And the other way round: garrison over a live personal spawn.
	h.mustRun("spawn", "personal", "--target", "cursor")
	_, _, err = h.run("garrison", "personal", "--target", "cursor")
	if err == nil {
		t.Fatal("a garrison landed on a personal spawn")
	}
	if !strings.Contains(err.Error(), "recall") {
		t.Errorf("garrison error does not say what to do: %v", err)
	}
}

// TestRecallLeavesAGarrisonAloneWhenNarrowed: a garrison is one committed unit
// with no per-target record, so a --target or --global recall must not touch it.
func TestRecallLeavesAGarrisonAloneWhenNarrowed(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend", "--only", "react")
	h.mustRun("garrison", "frontend", "--target", "claude")
	h.mustRun("spawn", "frontend", "--target", "cursor")

	out := h.mustRun("recall", "frontend", "--target", "cursor")
	if strings.Contains(out, "garrison") {
		t.Errorf("a narrowed recall touched the garrison:\n%s", out)
	}
	if !testutil.Exists(h.garrisonPath(".claude/skills/react/SKILL.md")) {
		t.Error("a --target recall removed committed files")
	}
	h.mustRun("inspect")

	// Unnarrowed, --all reaches it.
	out = h.mustRun("recall", "--all")
	if !strings.Contains(out, "recalled the frontend garrison") {
		t.Errorf("recall --all missed the garrison:\n%s", out)
	}
}

// TestGarrisonRepairsFromTheLockfileWithNoLoadout is the teammate path: barracks
// installed, this loadout never trained, checkout damaged.
func TestGarrisonRepairsFromTheLockfileWithNoLoadout(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend", "--only", "react")
	h.mustRun("garrison", "frontend")

	// Stand in for a teammate's machine: barracks is installed, the loadout
	// definition is not there, and the checkout has lost its vendored files.
	if err := os.Remove(filepath.Join(h.layout.LoadoutsDir(), "frontend.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(h.garrisonPath(".claude")); err != nil {
		t.Fatal(err)
	}
	out := h.mustRun("garrison")
	if !strings.Contains(out, ".claude/skills/react/SKILL.md") {
		t.Errorf("repair did not rewrite the vendored file:\n%s", out)
	}
	h.mustRun("inspect")

	if _, _, err := h.run("garrison", "--target", "cursor"); err == nil {
		t.Error("--target with no loadout named was accepted")
	}
}

// TestGarrisonReachesEveryDeclaredTarget and keeps them on an update, so an
// agent the repository has already committed files for is never dropped
// silently.
func TestGarrisonReachesEveryDeclaredTarget(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend", "--only", "react")
	h.mustRun("garrison", "frontend", "--target", "claude", "--target", "cursor")
	for _, rel := range []string{".claude/skills/react/SKILL.md", ".cursor/skills/react/SKILL.md"} {
		if !testutil.Exists(h.garrisonPath(rel)) {
			t.Errorf("%s was not committed", rel)
		}
	}

	out := h.mustRun("garrison", "frontend")
	if !strings.Contains(out, "recorded in "+garrison.LockName) {
		t.Errorf("an update did not reuse the recorded targets:\n%s", out)
	}
	if !testutil.Exists(h.garrisonPath(".cursor/skills/react/SKILL.md")) {
		t.Error("an update dropped a target the lockfile recorded")
	}
}

// TestInspectWithNothingGarrisoned says so plainly rather than failing.
func TestInspectWithNothingGarrisoned(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("inspect")
	if !strings.Contains(out, "nothing is garrisoned") {
		t.Errorf("inspect output = %q", out)
	}
	if _, _, err := h.run("garrison"); err == nil {
		t.Error("garrison with nothing recorded and no loadout named succeeded")
	}
}
