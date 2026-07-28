package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tobi404/barracks/internal/garrison"
	"github.com/tobi404/barracks/internal/testutil"
)

// lockfile reads the committed manifest straight off disk.
func (h *harness) lockfile(t *testing.T) *garrison.Manifest {
	t.Helper()
	m, err := garrison.Load(h.work.Dir)
	if err != nil {
		t.Fatalf("load %s: %v", garrison.LockName, err)
	}
	return m
}

// dropLockfileIdentities rewrites the lockfile as a barracks from before
// identities existed would have written it.
//
// This is the one path that cannot be fixed after release: a lockfile already in
// somebody's repository can never be reached to migrate it, so the fallback has
// to be right the first time.
func (h *harness) dropLockfileIdentities(t *testing.T) {
	t.Helper()
	b, err := os.ReadFile(h.lockPath())
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "id:") {
			continue
		}
		kept = append(kept, line)
	}
	if err := os.WriteFile(h.lockPath(), []byte(strings.Join(kept, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	if g := h.lockfile(t).Garrisons[0]; g.ID != "" {
		t.Fatalf("the lockfile still records an identity: %q", g.ID)
	}
}

// TestRenameUpdatesEveryLocalRecord is acceptance criterion 3.
func TestRenameUpdatesEveryLocalRecord(t *testing.T) {
	h := newHarness(t)
	second := testutil.NewGitRepo(t, filepath.Join(h.root, "work2"))
	h.equipped("frontend")
	statusBefore := h.work.Status(t)
	h.mustRun("spawn", "frontend")
	h.mustRun("spawn", "frontend", "--global")
	h.cwd = second.Dir
	h.mustRun("spawn", "frontend")
	h.cwd = ""

	before := snapshotLinks(t, h.skillsDir())

	out := h.mustRun("rename", "frontend", "web")
	if !strings.Contains(out, "renamed frontend to web") {
		t.Errorf("rename output = %q", out)
	}

	if _, err := h.loadout("frontend"); err == nil {
		t.Error("the old definition is still there")
	}
	l, err := h.loadout("web")
	if err != nil {
		t.Fatalf("the renamed definition is missing: %v", err)
	}
	if l.Name != "web" {
		t.Errorf("definition still names itself %q", l.Name)
	}

	// The spawns keep working, under the new name and nothing else.
	if got := snapshotLinks(t, h.skillsDir()); !sameMap(got, before) {
		t.Errorf("a rename moved the spawned links:\nbefore %v\nafter  %v", before, got)
	}
	noDanglingLinks(t, h.skillsDir())
	deployed := h.mustRun("deployed", "--everywhere")
	if strings.Contains(deployed, "frontend") {
		t.Errorf("a lease still records the old name:\n%s", deployed)
	}
	if n := strings.Count(deployed, "web "); n != 3 {
		t.Errorf("want three spawns under the new name, got:\n%s", deployed)
	}
	if got := h.work.Status(t); got != statusBefore {
		t.Errorf("git status dirty after a rename: %q", got)
	}

	// And the new name is what every other command answers to.
	h.mustRun("recall", "web")
	if testutil.Exists(h.skillsDir()) {
		t.Error("recall under the new name did not reach the spawn")
	}
}

// TestRenameKeepsTheIdentityAndCarriesTheGarrison is acceptance criterion 3 for
// the committed tier: the lockfile entry follows the loadout rather than being
// orphaned under a name nothing answers to.
func TestRenameKeepsTheIdentityAndCarriesTheGarrison(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend", "--except", "legacy")
	h.mustRun("garrison", "frontend")
	h.work.Commit(t, "garrison")

	was, err := h.loadout("frontend")
	if err != nil {
		t.Fatal(err)
	}
	if was.ID == "" {
		t.Fatal("a trained loadout has no identity")
	}

	h.mustRun("rename", "frontend", "web")

	now, err := h.loadout("web")
	if err != nil {
		t.Fatal(err)
	}
	if now.ID != was.ID {
		t.Errorf("the identity changed with the name: %q -> %q", was.ID, now.ID)
	}
	g := h.lockfile(t).Garrisons[0]
	if g.Loadout != "web" || g.ID != was.ID {
		t.Errorf("lockfile entry = %q/%q, want web/%s", g.Loadout, g.ID, was.ID)
	}
	if out := h.mustRun("inspect"); !strings.Contains(out, "identity: "+was.ID) {
		t.Errorf("inspect does not show the identity a lockfile keys on:\n%s", out)
	}
	// An update under the new name has to recognise its own committed files
	// rather than refusing over them.
	if out := h.mustRun("garrison", "web"); !strings.Contains(out, "updated garrison web") {
		t.Errorf("garrison after a rename = %q", out)
	}
	h.work.Commit(t, "rename")
	if got := h.work.Status(t); got != "" {
		t.Errorf("git status dirty after committing the rename: %q", got)
	}
}

// TestRenameResolvesALockfileWrittenBeforeIdentities is acceptance criterion 4.
//
// The entry carries no identity, so it can only be found by name - and a rename
// is exactly the moment that name stops being the loadout's. Finding it, moving
// it, and stamping it with the identity is what makes the next rename free.
func TestRenameResolvesALockfileWrittenBeforeIdentities(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend", "--except", "legacy")
	h.mustRun("garrison", "frontend")
	h.dropLockfileIdentities(t)
	h.work.Commit(t, "as an older barracks wrote it")

	// Read first: the whole point is that this still works before any rename.
	if out := h.mustRun("inspect"); !strings.Contains(out, "identity: none recorded") {
		t.Errorf("inspect on a pre-identity lockfile = %q", out)
	}

	h.mustRun("rename", "frontend", "web")

	l, err := h.loadout("web")
	if err != nil {
		t.Fatal(err)
	}
	g := h.lockfile(t).Garrisons[0]
	if g.Loadout != "web" {
		t.Fatalf("the pre-identity entry was left behind as %q", g.Loadout)
	}
	if g.ID != l.ID {
		t.Errorf("the entry was not stamped with the identity: %q, want %q", g.ID, l.ID)
	}
	// Nothing on disk moved, and the checkout still matches.
	if !testutil.Exists(h.garrisonPath(".claude/skills/react/SKILL.md")) {
		t.Error("a vendored file was removed by a rename")
	}
	if out := h.mustRun("inspect"); !strings.Contains(out, "ok") {
		t.Errorf("inspect after renaming a pre-identity garrison = %q", out)
	}
	if out := h.mustRun("garrison", "web"); !strings.Contains(out, "updated garrison web") {
		t.Errorf("garrison after renaming a pre-identity garrison = %q", out)
	}
}

// TestGarrisonMatchesAPreIdentityLockfileByName is the same fallback without a
// rename in the way: an older lockfile must not read as somebody else's files.
func TestGarrisonMatchesAPreIdentityLockfileByName(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend", "--except", "legacy")
	h.mustRun("garrison", "frontend")
	h.dropLockfileIdentities(t)
	h.work.Commit(t, "as an older barracks wrote it")

	out := h.mustRun("garrison", "frontend")
	if !strings.Contains(out, "updated garrison frontend") {
		t.Errorf("garrison over a pre-identity lockfile = %q", out)
	}
	if m := h.lockfile(t); len(m.Garrisons) != 1 {
		t.Errorf("the entry was duplicated rather than matched: %+v", m.Garrisons)
	}

	// Strip finds it the same way, and removing the last source takes it out
	// rather than leaving committed files nothing describes.
	h.dropLockfileIdentities(t)
	if out := h.mustRun("strip", "frontend", h.sourceArg("skills")); !strings.Contains(out, "nothing was left to commit") {
		t.Errorf("strip over a pre-identity lockfile = %q", out)
	}
	if testutil.Exists(h.lockPath()) {
		t.Error("the pre-identity lockfile survived with no garrison left in it")
	}
	h.mustRun("equip", "frontend", h.sourceArg("skills"), "--except", "legacy")
	h.mustRun("garrison", "frontend")
	h.dropLockfileIdentities(t)
	// Recall finds it too, and removes it rather than reporting nothing here.
	if out := h.mustRun("recall", "frontend"); !strings.Contains(out, "recalled the frontend garrison") {
		t.Errorf("recall over a pre-identity lockfile = %q", out)
	}
}

// TestRenameOntoAnExistingNameChangesNothing is acceptance criterion 5.
func TestRenameOntoAnExistingNameChangesNothing(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend", "--except", "legacy")
	h.mustRun("garrison", "frontend")
	h.work.Commit(t, "garrison")
	h.mustRun("train", "web")

	lockBefore := testutil.ReadFile(t, h.lockPath())

	_, _, err := h.run("rename", "frontend", "web")
	if err == nil {
		t.Fatal("rename took a name that was already in use")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("refusal = %v", err)
	}
	for _, name := range []string{"frontend", "web"} {
		if _, err := h.loadout(name); err != nil {
			t.Errorf("%s no longer exists after a refused rename: %v", name, err)
		}
	}
	if got := testutil.ReadFile(t, h.lockPath()); got != lockBefore {
		t.Errorf("%s changed across a refused rename:\n%s", garrison.LockName, got)
	}
	if got := h.work.Status(t); got != "" {
		t.Errorf("git status dirty after a refused rename: %q", got)
	}
}

// TestRenameToItsOwnNameIsRefused: nothing to do is said rather than done.
func TestRenameToItsOwnNameIsRefused(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend")
	if _, _, err := h.run("rename", "frontend", "frontend"); err == nil {
		t.Error("renaming a loadout to its own name was accepted")
	}
	if _, err := h.loadout("frontend"); err != nil {
		t.Errorf("the loadout was disturbed: %v", err)
	}
}

// TestRenameUndoesEveryRecordWhenOneCannotBeWritten is acceptance criterion 7.
//
// The lockfile is rewritten first, because it is the only record a rename can
// orphan. When the definition then cannot be moved, that write has to be taken
// back - a half-renamed loadout is worse than a refusal.
func TestRenameUndoesEveryRecordWhenOneCannotBeWritten(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend", "--except", "legacy")
	h.mustRun("garrison", "frontend")
	h.work.Commit(t, "garrison")

	lockBefore := testutil.ReadFile(t, h.lockPath())

	// A directory where the new definition has to be written makes Save fail
	// after the lockfile has already moved.
	blocked := filepath.Join(h.layout.LoadoutsDir(), "web.yaml")
	testutil.MkDir(t, blocked)

	_, errb, err := h.run("rename", "frontend", "web")
	if err == nil {
		t.Fatal("rename reported success though the definition could not be written")
	}
	if strings.Contains(errb, "could not put") {
		t.Errorf("the undo itself failed:\n%s", errb)
	}
	if _, err := h.loadout("frontend"); err != nil {
		t.Errorf("the loadout was lost: %v", err)
	}
	if got := testutil.ReadFile(t, h.lockPath()); got != lockBefore {
		t.Errorf("%s was left renamed:\n%s", garrison.LockName, got)
	}
	if got := h.work.Status(t); got != "" {
		t.Errorf("git status dirty after a failed rename: %q", got)
	}
	if out := h.mustRun("inspect"); !strings.Contains(out, "frontend") || !strings.Contains(out, "ok") {
		t.Errorf("inspect after a failed rename = %q", out)
	}
}
