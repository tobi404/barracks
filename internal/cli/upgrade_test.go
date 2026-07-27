package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tobi404/barracks/internal/loadout"
	"github.com/tobi404/barracks/internal/testutil"
)

// loadout reads a definition straight off disk, to assert on what a command
// actually wrote rather than on what it printed.
func (h *harness) loadout(name string) (*loadout.Loadout, error) {
	h.t.Helper()
	return loadout.NewStore(h.layout.LoadoutsDir()).Get(name)
}

// resolved reports the file the symlink at path eventually points at, and fails
// the test if it dangles. It is how these tests prove a spawn really resolves to
// the new store entry rather than merely having been rewritten.
func resolved(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(path, "SKILL.md"))
	if err != nil {
		t.Fatalf("read through %s: %v", path, err)
	}
	return string(body)
}

// noDanglingLinks fails if anything in dir points at something that is not
// there. A skill removed upstream must leave no broken link behind.
func noDanglingLinks(t *testing.T, dir string) {
	t.Helper()
	for _, name := range testutil.Entries(t, dir) {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("dangling entry %s: %v", p, err)
		}
	}
}

func (h *harness) globalSkillsDir() string {
	return filepath.Join(h.home, ".claude", "skills")
}

// equipped trains and equips a loadout from the fixture's skills directory.
func (h *harness) equipped(name string, args ...string) {
	h.t.Helper()
	h.mustRun("train", name)
	h.mustRun(append([]string{"equip", name, h.sourceArg("skills")}, args...)...)
}

// TestUpgradeWithNothingUpstreamReportsAlreadyCurrent is acceptance criterion 1.
func TestUpgradeWithNothingUpstreamReportsAlreadyCurrent(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend")
	h.mustRun("spawn", "frontend")

	before := snapshotLinks(t, h.skillsDir())
	statusBefore := h.work.Status(t)
	excludeBefore := h.work.ReadExclude(t)

	out := h.mustRun("upgrade")
	if !strings.Contains(out, "already current") {
		t.Errorf("upgrade with nothing upstream should report already current:\n%s", out)
	}
	if strings.Contains(out, "->") {
		t.Errorf("upgrade claimed a commit move that did not happen:\n%s", out)
	}
	if got := snapshotLinks(t, h.skillsDir()); !sameMap(got, before) {
		t.Errorf("upgrade changed the spawned links\n got: %v\nwant: %v", got, before)
	}
	if got := h.work.Status(t); got != statusBefore {
		t.Errorf("git status changed:\n%s", got)
	}
	if got := h.work.ReadExclude(t); got != excludeBefore {
		t.Error(".git/info/exclude was rewritten for an upgrade that changed nothing")
	}
}

// TestUpgradeFollowsAMovedBranch is acceptance criteria 2 and 8: the source is
// refetched, the diff is real, every spawn resolves to the new content, and the
// repository's git state is untouched throughout.
func TestUpgradeFollowsAMovedBranch(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend")
	h.mustRun("spawn", "frontend")
	h.mustRun("spawn", "frontend", "--global")

	statusBefore := h.work.Status(t)

	// Upstream: react changes, a skill appears, css is left exactly as it was.
	h.src.AddSkills(t,
		testutil.Skill{Path: "skills/react", Body: "---\nname: react\n---\n\nversion two\n"},
		testutil.Skill{Path: "skills/hooks"},
	)
	h.src.Commit(t, "move react on")

	out, errb, err := h.run("upgrade", "frontend")
	if err != nil {
		t.Fatalf("upgrade failed: %v\n%s\n%s", err, out, errb)
	}
	for _, want := range []string{"+ hooks", "~ react", "->", "relinked"} {
		if !strings.Contains(out, want) {
			t.Errorf("upgrade output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "~ css") {
		t.Errorf("css was reported as modified although its content did not change:\n%s", out)
	}

	// Every live spawn now resolves to the new content, in both scopes.
	for _, dir := range []string{h.skillsDir(), h.globalSkillsDir()} {
		if body := resolved(t, filepath.Join(dir, "react")); !strings.Contains(body, "version two") {
			t.Errorf("%s/react still resolves to the old content: %q", dir, body)
		}
		if !testutil.IsSymlink(t, filepath.Join(dir, "hooks")) {
			t.Errorf("%s did not gain the new skill", dir)
		}
		noDanglingLinks(t, dir)
	}

	if got := h.work.Status(t); got != statusBefore {
		t.Errorf("upgrade dirtied git status:\n%s", got)
	}
	// The definition now records the new commit, and a re-run says so.
	out = h.mustRun("upgrade", "frontend")
	if !strings.Contains(out, "already current") {
		t.Errorf("a second upgrade should be a no-op:\n%s", out)
	}
}

// TestUpgradeLeavesAPinnedSourceAlone is acceptance criterion 3.
func TestUpgradeLeavesAPinnedSourceAlone(t *testing.T) {
	h := newHarness(t)
	pinned := h.src.Head(t)
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.src.Dir+"#"+pinned+":skills")

	// The branch moves; a pinned source must not follow it.
	h.src.AddSkills(t, testutil.Skill{Path: "skills/hooks"})
	h.src.Commit(t, "move on")

	out := h.mustRun("upgrade", "frontend")
	if !strings.Contains(out, "pinned at") || !strings.Contains(out, "nothing to resolve") {
		t.Errorf("a pinned source should be reported as pinned:\n%s", out)
	}
	if strings.Contains(out, "hooks") {
		t.Errorf("a pinned source followed the branch:\n%s", out)
	}
	if l, err := h.loadout("frontend"); err != nil {
		t.Fatal(err)
	} else if l.Equipment[0].Commit != pinned {
		t.Errorf("commit moved from %s to %s", pinned, l.Equipment[0].Commit)
	}
}

// TestUpgradeDryRunMatchesTheRealRun is acceptance criterion 4.
func TestUpgradeDryRunMatchesTheRealRun(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend")
	h.mustRun("spawn", "frontend")

	h.src.AddSkills(t,
		testutil.Skill{Path: "skills/react", Body: "changed\n"},
		testutil.Skill{Path: "skills/hooks"},
	)
	h.src.RemovePath(t, "skills/legacy")
	h.src.Commit(t, "reshuffle")

	before := snapshotLinks(t, h.skillsDir())
	dryOut, dryErr, err := h.run("upgrade", "--dry-run")
	if err != nil {
		t.Fatalf("dry run failed: %v\n%s", err, dryErr)
	}
	if !strings.Contains(dryOut, "dry run") {
		t.Errorf("a dry run must say so:\n%s", dryOut)
	}
	// A dry run resolves and fetches, and changes nothing else.
	if got := snapshotLinks(t, h.skillsDir()); !sameMap(got, before) {
		t.Errorf("dry run changed the spawned links\n got: %v\nwant: %v", got, before)
	}
	if l, err := h.loadout("frontend"); err != nil {
		t.Fatal(err)
	} else if l.Equipment[0].Commit == h.src.Head(t) {
		t.Error("dry run rewrote the loadout definition")
	}

	realOut, realErr, err := h.run("upgrade")
	if err != nil {
		t.Fatalf("upgrade failed: %v\n%s", err, realErr)
	}
	if got, want := realOut, stripDryRun(dryOut); got != want {
		t.Errorf("the real run did something other than what the dry run promised\n dry: %q\nreal: %q", want, got)
	}
	if realErr != dryErr {
		t.Errorf("dry run and real run disagreed on stderr\n dry: %q\nreal: %q", dryErr, realErr)
	}
}

// TestUpgradePinFreezesAMovingSource is acceptance criterion 5.
func TestUpgradePinFreezesAMovingSource(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend")
	h.mustRun("spawn", "frontend")

	h.src.AddSkills(t, testutil.Skill{Path: "skills/hooks"})
	at := h.src.Commit(t, "add hooks")

	out := h.mustRun("upgrade", "frontend", "--pin")
	if !strings.Contains(out, "pinned to") {
		t.Errorf("--pin did not report the new declared ref:\n%s", out)
	}
	l, err := h.loadout("frontend")
	if err != nil {
		t.Fatal(err)
	}
	if l.Equipment[0].Ref != at {
		t.Errorf("declared ref = %q, want the resolved commit %q", l.Equipment[0].Ref, at)
	}
	if !strings.Contains(l.Equipment[0].Raw, at) {
		t.Errorf("the hand-editable source string was not repinned: %q", l.Equipment[0].Raw)
	}

	// The branch moves again; the frozen source must not follow.
	h.src.AddSkills(t, testutil.Skill{Path: "skills/forms"})
	h.src.Commit(t, "add forms")

	out = h.mustRun("upgrade", "frontend")
	if !strings.Contains(out, "pinned at") {
		t.Errorf("a source frozen by --pin should later report as pinned:\n%s", out)
	}
	if testutil.Exists(filepath.Join(h.skillsDir(), "forms")) {
		t.Error("a pinned source followed the branch")
	}
	if !testutil.IsSymlink(t, filepath.Join(h.skillsDir(), "hooks")) {
		t.Error("--pin lost the skill it was pinned at")
	}
}

// TestUpgradeAlsoPinsASourceThatHasNotMoved covers --pin freezing a source whose
// branch happens to be up to date, which is when a cautious user reaches for it.
func TestUpgradeAlsoPinsASourceThatHasNotMoved(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend")
	at := h.src.Head(t)

	out := h.mustRun("upgrade", "frontend", "--pin")
	if !strings.Contains(out, "already current") {
		t.Errorf("output = %q", out)
	}
	l, err := h.loadout("frontend")
	if err != nil {
		t.Fatal(err)
	}
	if l.Equipment[0].Ref != at {
		t.Errorf("--pin left a moving ref %q in place", l.Equipment[0].Ref)
	}
}

// TestUpgradeRemovesASkillDeletedUpstream is acceptance criterion 6.
func TestUpgradeRemovesASkillDeletedUpstream(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend")
	h.mustRun("spawn", "frontend")
	h.mustRun("spawn", "frontend", "--global")

	statusBefore := h.work.Status(t)
	h.src.RemovePath(t, "skills/legacy")
	h.src.Commit(t, "drop legacy")

	out := h.mustRun("upgrade")
	if !strings.Contains(out, "- legacy") {
		t.Errorf("upgrade did not report the removal:\n%s", out)
	}
	for _, dir := range []string{h.skillsDir(), h.globalSkillsDir()} {
		if testutil.Exists(filepath.Join(dir, "legacy")) {
			t.Errorf("%s/legacy survived its removal upstream", dir)
		}
		noDanglingLinks(t, dir)
	}
	if got := h.work.Status(t); got != statusBefore {
		t.Errorf("git status changed:\n%s", got)
	}
	// The lease no longer claims it, so a recall says nothing about it.
	_, errb, err := h.run("recall", "frontend")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(errb, "legacy") {
		t.Errorf("recall still knew about the removed skill:\n%s", errb)
	}
}

// TestUpgradeRecallsASpawnLeftWithNoSkills covers the whole source disappearing.
func TestUpgradeRecallsASpawnLeftWithNoSkills(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend", "--only", "legacy")
	h.mustRun("spawn", "frontend")

	h.src.RemovePath(t, "skills/legacy")
	h.src.Commit(t, "drop legacy")

	out := h.mustRun("upgrade")
	if !strings.Contains(out, "the spawn is recalled") {
		t.Errorf("upgrade left an empty spawn behind:\n%s", out)
	}
	if testutil.Exists(filepath.Join(h.work.Dir, ".claude")) {
		t.Error("the emptied spawn's directories were not pruned")
	}
	if got := h.work.Status(t); got != "" {
		t.Errorf("git status not clean:\n%s", got)
	}
	if out := h.mustRun("deployed"); !strings.Contains(out, "nothing deployed") {
		t.Errorf("the emptied lease survived:\n%s", out)
	}
}

// TestUpgradeNeverTouchesUserFiles is acceptance criterion 7.
func TestUpgradeNeverTouchesUserFiles(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend")
	h.mustRun("spawn", "frontend")

	// One spawned link taken over by a directory of the user's, and a name the
	// upgrade is about to want for a new skill already occupied.
	taken := filepath.Join(h.skillsDir(), "react")
	if err := os.Remove(taken); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(taken, "SKILL.md"), "I took this one over\n")
	occupied := filepath.Join(h.skillsDir(), "hooks")
	testutil.WriteFile(t, filepath.Join(occupied, "SKILL.md"), "mine, not yours\n")

	h.src.AddSkills(t,
		testutil.Skill{Path: "skills/react", Body: "upstream react\n"},
		testutil.Skill{Path: "skills/hooks"},
	)
	h.src.Commit(t, "move on")

	out, errb, err := h.run("upgrade")
	if err != nil {
		t.Fatalf("upgrade failed instead of reporting: %v\n%s", err, errb)
	}
	if !strings.Contains(out, "~ react") {
		t.Errorf("upgrade should still report the upstream change:\n%s", out)
	}
	// Silence would be the bug: barracks must say what it left alone.
	for _, want := range []string{"left in place", "react", "hooks"} {
		if !strings.Contains(errb, want) {
			t.Errorf("upgrade did not report %q among the paths it kept:\n%s", want, errb)
		}
	}
	if body := resolved(t, taken); !strings.Contains(body, "took this one over") {
		t.Errorf("the user's directory was replaced: %q", body)
	}
	if body := resolved(t, occupied); !strings.Contains(body, "mine, not yours") {
		t.Errorf("the user's directory was overwritten: %q", body)
	}
	// css was ours all along, so it moved to the new store entry as normal.
	if !testutil.IsSymlink(t, filepath.Join(h.skillsDir(), "css")) {
		t.Error("a link barracks did create was not relinked")
	}
}

// TestUpgradeLeavesRunningSessionsAlone documents the judgment call: a spawn
// held by a live process keeps the skills that session started with.
func TestUpgradeLeavesRunningSessionsAlone(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend")
	h.mustRun("spawn", "frontend")

	store := leaseStore(t, h)
	leases, _ := store.List()
	l := leases[0]
	l.Kind = "process"
	l.Owner = ownerFor(4242, "a-live-agent-session")
	if err := store.Save(l); err != nil {
		t.Fatal(err)
	}
	h.prober.alive[4242] = "a-live-agent-session"

	h.src.AddSkills(t, testutil.Skill{Path: "skills/react", Body: "version two\n"})
	h.src.Commit(t, "move on")

	out := h.mustRun("upgrade")
	if !strings.Contains(out, "left as it is") || !strings.Contains(out, "4242") {
		t.Errorf("upgrade did not say it was leaving the running session alone:\n%s", out)
	}
	if body := resolved(t, filepath.Join(h.skillsDir(), "react")); strings.Contains(body, "version two") {
		t.Error("a live session had its skills changed underneath it")
	}

	// The store and the definition did move on, so the next spawn gets it.
	if l, err := h.loadout("frontend"); err != nil {
		t.Fatal(err)
	} else if l.Equipment[0].Commit != h.src.Head(t) {
		t.Error("the loadout definition was not upgraded")
	}

	// And --include-running is the way to have it now.
	out = h.mustRun("upgrade", "--include-running")
	if !strings.Contains(out, "relinked") {
		t.Errorf("--include-running did not relink the held spawn:\n%s", out)
	}
	if body := resolved(t, filepath.Join(h.skillsDir(), "react")); !strings.Contains(body, "version two") {
		t.Errorf("--include-running left the old content in place: %q", body)
	}
}

// TestUpgradeDoesNotClaimAnUpdateForIdenticalSkills covers a commit that moves
// without changing anything the loadout takes.
func TestUpgradeDoesNotClaimAnUpdateForIdenticalSkills(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend", "--only", "react")
	h.mustRun("spawn", "frontend")

	// A commit that touches a file no skill of ours contains.
	testutil.WriteFile(t, filepath.Join(h.src.Dir, "README.md"), "unrelated\n")
	h.src.Commit(t, "unrelated change")

	out := h.mustRun("upgrade")
	if !strings.Contains(out, "no skill changed") {
		t.Errorf("upgrade claimed an update it cannot substantiate:\n%s", out)
	}
	// The spawn still follows the pin, so the store entry it uses is the new one.
	link, err := os.Readlink(filepath.Join(h.skillsDir(), "react"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(link, h.src.Head(t)) {
		t.Errorf("the spawn was not moved onto the newly pinned commit: %s", link)
	}
	noDanglingLinks(t, h.skillsDir())
}

// TestUpgradeIgnoresSpawnsOfOtherLoadouts keeps the blast radius to what was
// asked for.
func TestUpgradeIgnoresSpawnsOfOtherLoadouts(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend", "--only", "react")
	h.equipped("backend", "--only", "css")
	h.mustRun("spawn", "frontend")
	h.mustRun("spawn", "backend")

	h.src.AddSkills(t,
		testutil.Skill{Path: "skills/react", Body: "new react\n"},
		testutil.Skill{Path: "skills/css", Body: "new css\n"},
	)
	h.src.Commit(t, "move both")

	cssBefore, err := os.Readlink(filepath.Join(h.skillsDir(), "css"))
	if err != nil {
		t.Fatal(err)
	}
	h.mustRun("upgrade", "frontend")

	if body := resolved(t, filepath.Join(h.skillsDir(), "react")); !strings.Contains(body, "new react") {
		t.Error("the named loadout was not upgraded")
	}
	cssAfter, err := os.Readlink(filepath.Join(h.skillsDir(), "css"))
	if err != nil {
		t.Fatal(err)
	}
	if cssAfter != cssBefore {
		t.Errorf("upgrading frontend moved backend's spawn: %s -> %s", cssBefore, cssAfter)
	}
}

// TestUpgradeSurvivesADeletedSpawnDirectory: relinking must never resurrect a
// directory the user removed.
func TestUpgradeSurvivesADeletedSpawnDirectory(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend")
	h.mustRun("spawn", "frontend")
	if err := os.RemoveAll(filepath.Join(h.work.Dir, ".claude")); err != nil {
		t.Fatal(err)
	}

	h.src.AddSkills(t, testutil.Skill{Path: "skills/react", Body: "new\n"})
	h.src.Commit(t, "move on")

	out := h.mustRun("upgrade")
	if !strings.Contains(out, "no longer exists") {
		t.Errorf("upgrade did not report the missing spawn directory:\n%s", out)
	}
	if testutil.Exists(filepath.Join(h.work.Dir, ".claude")) {
		t.Error("upgrade recreated a directory the user deleted")
	}
}

// TestUpgradeComparesByNameWhenTheOldCommitIsGone: with nothing left to compare
// against, the report says how it was reached rather than inventing a content
// diff it cannot substantiate.
func TestUpgradeComparesByNameWhenTheOldCommitIsGone(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend")
	h.mustRun("spawn", "frontend")

	h.src.AddSkills(t, testutil.Skill{Path: "skills/hooks"})
	h.src.RemovePath(t, "skills/legacy")
	h.src.Commit(t, "move on")

	if err := os.RemoveAll(h.layout.StoreDir()); err != nil {
		t.Fatal(err)
	}

	out := h.mustRun("upgrade")
	for _, want := range []string{"compared by name only", "+ hooks", "- legacy"} {
		if !strings.Contains(out, want) {
			t.Errorf("upgrade output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "~ react") {
		t.Errorf("upgrade claimed a content change it could not have established:\n%s", out)
	}
	noDanglingLinks(t, h.skillsDir())
	if body := resolved(t, filepath.Join(h.skillsDir(), "react")); body == "" {
		t.Error("the spawn does not resolve to the refetched store entry")
	}
}

// TestUpgradeHandlesTwoSubpathsOfOneRepo: links are matched to the source that
// produced them by where they point, and the more specific subpath wins.
func TestUpgradeHandlesTwoSubpathsOfOneRepo(t *testing.T) {
	h := newHarness(t, testutil.Skill{Path: "pack-a/alpha"}, testutil.Skill{Path: "pack-b/beta"})
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("pack-a"))
	h.mustRun("equip", "frontend", h.sourceArg("pack-b"))
	h.mustRun("spawn", "frontend")

	h.src.AddSkills(t, testutil.Skill{Path: "pack-a/alpha", Body: "new alpha\n"})
	h.src.Commit(t, "move alpha")

	out := h.mustRun("upgrade")
	if !strings.Contains(out, "~ alpha") {
		t.Errorf("the changed skill was not reported:\n%s", out)
	}
	if strings.Contains(out, "~ beta") {
		t.Errorf("beta did not change and must not be reported as modified:\n%s", out)
	}
	if !strings.Contains(out, "no skill changed") {
		t.Errorf("the untouched subpath should say so:\n%s", out)
	}
	head := h.src.Head(t)
	for _, name := range []string{"alpha", "beta"} {
		link, err := os.Readlink(filepath.Join(h.skillsDir(), name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(link, head) {
			t.Errorf("%s was not relinked onto the new commit: %s", name, link)
		}
	}
	noDanglingLinks(t, h.skillsDir())
}

func TestUpgradeErrors(t *testing.T) {
	h := newHarness(t)

	if out := h.mustRun("upgrade"); !strings.Contains(out, "no loadouts trained yet") {
		t.Errorf("output = %q", out)
	}
	if _, _, err := h.run("upgrade", "nope"); err == nil {
		t.Error("upgrading an unknown loadout should fail")
	}

	// An unresolvable source is reported and fails the run without taking the
	// rest of the report with it.
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"))
	if err := os.RemoveAll(h.src.Dir); err != nil {
		t.Fatal(err)
	}
	out, _, err := h.run("upgrade", "frontend")
	if err == nil {
		t.Error("an unresolvable source should fail the run")
	}
	if !strings.Contains(out, "could not be upgraded") {
		t.Errorf("output = %q", out)
	}
}

// TestUpgradeAnEmptyLoadout covers a trained but unequipped loadout.
func TestUpgradeAnEmptyLoadout(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")
	if out := h.mustRun("upgrade"); !strings.Contains(out, "no sources equipped") {
		t.Errorf("output = %q", out)
	}
}

func snapshotLinks(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, name := range testutil.Entries(t, dir) {
		dest, err := os.Readlink(filepath.Join(dir, name))
		if err != nil {
			dest = "<not a link>"
		}
		out[name] = dest
	}
	return out
}

func sameMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// stripDryRun removes the two lines that mark a dry run, leaving the body that
// must be identical to what a real run prints.
func stripDryRun(out string) string {
	var kept []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "dry run") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}
