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

// TestUpgradeBringsAGarrisonOntoTheNewPins is acceptance criterion 5: one command
// rewrites the vendored files and the lockfile together, and never leaves the two
// disagreeing.
func TestUpgradeBringsAGarrisonOntoTheNewPins(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend", "--except", "legacy")
	h.mustRun("garrison", "frontend")
	h.work.Commit(t, "garrison frontend")
	lockBefore := testutil.ReadFile(t, h.lockPath())

	// react changes, css disappears, tailwind arrives.
	testutil.WriteFile(t, filepath.Join(h.src.Dir, "skills", "react", "SKILL.md"), "---\nname: react\n---\n\nv2\n")
	if err := os.RemoveAll(filepath.Join(h.src.Dir, "skills", "css")); err != nil {
		t.Fatal(err)
	}
	h.src.AddSkills(t, testutil.Skill{Path: "skills/tailwind"})
	h.src.Commit(t, "v2")

	// A dry run describes the committed half and changes nothing.
	out := h.mustRun("upgrade", "frontend", "--dry-run")
	for _, want := range []string{"committed here", "frontend", "would rewrite the committed files"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run does not describe the committed tier (%q missing):\n%s", want, out)
		}
	}
	if got := testutil.ReadFile(t, h.lockPath()); got != lockBefore {
		t.Error("a dry run rewrote the lockfile")
	}
	if status := h.work.Status(t); status != "" {
		t.Errorf("a dry run changed the working tree:\n%s", status)
	}

	out = h.mustRun("upgrade", "frontend")
	for _, want := range []string{
		"committed here",
		"+ .claude/skills/react/SKILL.md",
		"+ .claude/skills/tailwind/SKILL.md",
		"- .claude/skills/css/SKILL.md",
		"commit these files and " + garrison.LockName,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("upgrade output missing %q:\n%s", want, out)
		}
	}

	// Files, lockfile, and loadout definition all name the same commits.
	h.mustRun("inspect")
	if got := testutil.ReadFile(t, h.garrisonPath(".claude/skills/react/SKILL.md")); !strings.Contains(got, "v2") {
		t.Errorf("the vendored file was not updated: %q", got)
	}
	if !testutil.Exists(h.garrisonPath(".claude/skills/tailwind/SKILL.md")) {
		t.Error("a new upstream skill was not committed")
	}
	if testutil.Exists(h.garrisonPath(".claude/skills/css")) {
		t.Error("a dropped skill was left behind")
	}
	if got := testutil.ReadFile(t, h.lockPath()); got == lockBefore {
		t.Error("the lockfile was not rewritten alongside the files")
	}

	// The whole change is one reviewable diff, and nothing is hidden from git.
	status := h.work.Status(t)
	for _, want := range []string{garrison.LockName, ".claude/skills/react/SKILL.md", ".claude/skills/css/SKILL.md"} {
		if !strings.Contains(status, want) {
			t.Errorf("git status does not show %s as part of the diff:\n%s", want, status)
		}
	}
	if got := h.work.ReadExclude(t); strings.Contains(got, "tailwind") {
		t.Errorf("upgrade excluded a committed path from git:\n%s", got)
	}

	// Nothing left to do: the second upgrade leaves the repository alone.
	h.work.Commit(t, "upgrade frontend")
	out = h.mustRun("upgrade", "frontend")
	if strings.Contains(out, "committed here") {
		t.Errorf("an upgrade with nothing to do still reported the committed tier:\n%s", out)
	}
	if status := h.work.Status(t); status != "" {
		t.Errorf("an upgrade with nothing to do dirtied the repository:\n%s", status)
	}
}

// TestUpgradeRefusesToOverwriteAnEditedVendoredFile: an upgrade is not a licence
// to discard somebody's edit, and it says which command is.
func TestUpgradeRefusesToOverwriteAnEditedVendoredFile(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend", "--only", "react")
	h.mustRun("garrison", "frontend")
	h.work.Commit(t, "garrison frontend")

	edited := h.garrisonPath(".claude/skills/react/SKILL.md")
	testutil.WriteFile(t, edited, "my own words\n")
	testutil.WriteFile(t, filepath.Join(h.src.Dir, "skills", "react", "SKILL.md"), "---\nname: react\n---\n\nv2\n")
	h.src.Commit(t, "v2")

	_, errOut, err := h.run("upgrade", "frontend")
	if err == nil {
		t.Fatal("upgrade succeeded over a locally edited vendored file")
	}
	if testutil.ReadFile(t, edited) != "my own words\n" {
		t.Error("upgrade discarded the edit")
	}
	if !strings.Contains(errOut, "barracks garrison frontend --force") {
		t.Errorf("upgrade did not say how to proceed:\n%s", errOut)
	}

	// The loadout still moved forward, so the fix is one command away and the
	// lockfile is the only thing still behind - which inspect reports as a note.
	h.mustRun("garrison", "frontend", "--force")
	h.mustRun("inspect")
}

// TestUpgradeLeavesARepositoryWithNoGarrisonAlone keeps the personal path
// unchanged: a repository that never garrisoned anything sees no new output.
func TestUpgradeLeavesARepositoryWithNoGarrisonAlone(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend", "--only", "react")
	h.mustRun("spawn", "frontend")

	testutil.WriteFile(t, filepath.Join(h.src.Dir, "skills", "react", "SKILL.md"), "---\nname: react\n---\n\nv2\n")
	h.src.Commit(t, "v2")

	out := h.mustRun("upgrade", "frontend")
	if strings.Contains(out, "committed here") {
		t.Errorf("upgrade reported a committed tier in a repository with none:\n%s", out)
	}
	if testutil.Exists(h.lockPath()) {
		t.Error("upgrade created a lockfile where nothing was garrisoned")
	}
}
