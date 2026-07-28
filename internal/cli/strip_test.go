package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tobi404/barracks/internal/garrison"
	"github.com/tobi404/barracks/internal/lease"
	"github.com/tobi404/barracks/internal/testutil"
)

// secondSource builds another fixture repository beside the harness's own, so a
// removal can be proved to touch one source and leave the other standing.
func (h *harness) secondSource(skills ...testutil.Skill) *testutil.GitRepo {
	h.t.Helper()
	return testutil.NewSkillRepo(h.t, filepath.Join(h.root, "src2"), skills...)
}

// TestStripRemovesOnlyItsOwnSkills is acceptance criterion 1: a source is
// detached, the skills it contributed leave every live spawn, and everything
// another source put there stays exactly where it was.
func TestStripRemovesOnlyItsOwnSkills(t *testing.T) {
	h := newHarness(t)
	other := h.secondSource(testutil.Skill{Path: "skills/hooks"})

	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"), "--except", "legacy")
	h.mustRun("equip", "frontend", other.Dir+"#main:skills")
	h.mustRun("spawn", "frontend")

	statusBefore := h.work.Status(t)
	for _, name := range []string{"react", "css", "hooks"} {
		if !testutil.Exists(filepath.Join(h.skillsDir(), name)) {
			t.Fatalf("%s was not spawned to begin with", name)
		}
	}

	out := h.mustRun("strip", "frontend", h.sourceArg("skills"))
	for _, want := range []string{"stripped", "frontend", "- react", "- css", "1 source left"} {
		if !strings.Contains(out, want) {
			t.Errorf("strip output missing %q:\n%s", want, out)
		}
	}

	for _, gone := range []string{"react", "css"} {
		if testutil.Exists(filepath.Join(h.skillsDir(), gone)) {
			t.Errorf("%s survived the removal of the source that provided it", gone)
		}
	}
	if !testutil.Exists(filepath.Join(h.skillsDir(), "hooks")) {
		t.Error("hooks was removed, but its source is still equipped")
	}
	noDanglingLinks(t, h.skillsDir())

	l, err := h.loadout("frontend")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Equipment) != 1 || !strings.Contains(l.Equipment[0].Ident(), "src2") {
		t.Errorf("definition kept the wrong sources: %v", l.Idents())
	}
	// Spawning promises git status stays clean, and a removal is no different.
	if got := h.work.Status(t); got != statusBefore {
		t.Errorf("git status changed across a strip:\nbefore %q\nafter  %q", statusBefore, got)
	}
}

// TestStripKeepsASkillTwoSourcesProvide is acceptance criterion 2, and the case
// the brief calls the one most likely to be got wrong.
//
// The link is not removed and re-added: it is handed over in a single relink, so
// the skill never stops existing, and it resolves to the surviving source's copy
// afterwards rather than to the store entry of a source nobody has any more.
func TestStripKeepsASkillTwoSourcesProvide(t *testing.T) {
	h := newHarness(t, testutil.Skill{Path: "skills/react", Body: "from the first source\n"})
	other := h.secondSource(
		testutil.Skill{Path: "skills/react", Body: "from the second source\n"},
		testutil.Skill{Path: "skills/hooks"},
	)

	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"))
	h.mustRun("spawn", "frontend")
	// Equipped after the spawn on purpose: two sources providing one skill is a
	// collision a fresh spawn refuses, so this is the only way the state arises.
	h.mustRun("equip", "frontend", other.Dir+"#main:skills")

	h.mustRun("strip", "frontend", h.sourceArg("skills"))

	link := filepath.Join(h.skillsDir(), "react")
	if !testutil.Exists(link) {
		t.Fatal("react vanished, though a second equipped source provides it")
	}
	if got := resolved(t, link); !strings.Contains(got, "from the second source") {
		t.Errorf("react resolves to %q, want the surviving source's copy", got)
	}
	// Stripping must never *add* anything: hooks belongs to a source this spawn
	// was never made from, and a removal is not the moment to materialise it.
	if testutil.Exists(filepath.Join(h.skillsDir(), "hooks")) {
		t.Error("stripping a source added a skill from one this spawn was never made from")
	}
	noDanglingLinks(t, h.skillsDir())
}

// TestStripReachesEveryRepositoryAndGlobal: a source belongs to the loadout, not
// to a place, so detaching it reaches every spawn of that loadout there is.
func TestStripReachesEveryRepositoryAndGlobal(t *testing.T) {
	h := newHarness(t)
	other := h.secondSource(testutil.Skill{Path: "skills/hooks"})
	second := testutil.NewGitRepo(t, filepath.Join(h.root, "work2"))

	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"), "--except", "legacy")
	h.mustRun("equip", "frontend", other.Dir+"#main:skills")
	h.mustRun("spawn", "frontend")
	h.mustRun("spawn", "frontend", "--global")

	h.cwd = second.Dir
	h.mustRun("spawn", "frontend")
	h.cwd = ""

	h.mustRun("strip", "frontend", h.sourceArg("skills"))

	dirs := map[string]string{
		"this repository":      h.skillsDir(),
		"the other repository": filepath.Join(second.Dir, ".claude", "skills"),
		"global":               h.globalSkillsDir(),
	}
	for where, dir := range dirs {
		if testutil.Exists(filepath.Join(dir, "react")) {
			t.Errorf("react survived in %s", where)
		}
		if !testutil.Exists(filepath.Join(dir, "hooks")) {
			t.Errorf("hooks was removed from %s, but its source is still equipped", where)
		}
		noDanglingLinks(t, dir)
	}
}

// TestStripTheLastSourceEmptiesTheLoadoutWithoutDisbandingIt: an empty loadout
// is a legitimate state, so its spawns end and its definition stays.
func TestStripTheLastSourceEmptiesTheLoadoutWithoutDisbandingIt(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend")
	statusBefore := h.work.Status(t)
	excludeBefore := h.work.ReadExclude(t)
	h.mustRun("spawn", "frontend")

	out := h.mustRun("strip", "frontend", h.sourceArg("skills"))
	if !strings.Contains(out, "no sources left") {
		t.Errorf("strip of the last source = %q", out)
	}

	l, err := h.loadout("frontend")
	if err != nil {
		t.Fatalf("the loadout was disbanded rather than emptied: %v", err)
	}
	if len(l.Equipment) != 0 {
		t.Errorf("loadout kept %d sources", len(l.Equipment))
	}
	if testutil.Exists(h.skillsDir()) {
		t.Error("the spawn directory survived with nothing left to deploy")
	}
	if got := h.work.ReadExclude(t); got != excludeBefore {
		t.Errorf(".git/info/exclude not restored:\n%s", got)
	}
	if got := h.work.Status(t); got != statusBefore {
		t.Errorf("git status dirty after the last source was stripped: %q", got)
	}
	if out := h.mustRun("deployed"); !strings.Contains(out, "nothing deployed") {
		t.Errorf("deployed after emptying the loadout = %q", out)
	}
}

// TestStripUpdatesTheGarrison covers the committed half: the vendored files and
// barracks.lock are rewritten together, and the other source's files stay.
func TestStripUpdatesTheGarrison(t *testing.T) {
	h := newHarness(t)
	other := h.secondSource(testutil.Skill{Path: "skills/hooks"})

	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"), "--except", "legacy")
	h.mustRun("equip", "frontend", other.Dir+"#main:skills")
	h.mustRun("garrison", "frontend")
	h.work.Commit(t, "garrison")

	out := h.mustRun("strip", "frontend", other.Dir+"#main:skills")
	if !strings.Contains(out, garrison.LockName) || !strings.Contains(out, ".claude/skills/hooks/SKILL.md") {
		t.Errorf("strip did not report the committed change:\n%s", out)
	}
	if testutil.Exists(h.garrisonPath(".claude/skills/hooks")) {
		t.Error("the stripped source's committed files survived")
	}
	for _, kept := range []string{"react", "css"} {
		if !testutil.Exists(h.garrisonPath(".claude/skills/" + kept + "/SKILL.md")) {
			t.Errorf("%s was removed, but its source is still equipped", kept)
		}
	}
	if out := h.mustRun("inspect"); !strings.Contains(out, "ok") {
		t.Errorf("inspect after a strip = %q", out)
	}
	// The removal is a change to tracked files, exactly as garrison's own
	// updates are, so it is there to review.
	if got := h.work.Status(t); !strings.Contains(got, garrison.LockName) {
		t.Errorf("the lockfile change is not in git status: %q", got)
	}
}

// TestStripTheLastSourceRemovesTheGarrison: a lockfile cannot record a garrison
// of no skills, and files nothing describes is the one state this tier must
// never be left in.
func TestStripTheLastSourceRemovesTheGarrison(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend")
	h.mustRun("garrison", "frontend")
	h.work.Commit(t, "garrison")

	out := h.mustRun("strip", "frontend", h.sourceArg("skills"))
	if !strings.Contains(out, "nothing was left to commit") {
		t.Errorf("strip of the last garrisoned source = %q", out)
	}
	if testutil.Exists(h.lockPath()) {
		t.Error("the lockfile survived with no garrison left in it")
	}
	if testutil.Exists(h.garrisonPath(".claude")) {
		t.Error("the vendored files survived")
	}
	if _, err := h.loadout("frontend"); err != nil {
		t.Errorf("the loadout was disbanded: %v", err)
	}
}

// TestStripKeepsAndReportsWhatItDidNotWrite is acceptance criterion 6, in both
// tiers at once: a symlink somebody replaced with a directory of their own, and
// a file left inside a committed skill directory the removal drops.
func TestStripKeepsAndReportsWhatItDidNotWrite(t *testing.T) {
	h := newHarness(t)
	other := h.secondSource(testutil.Skill{Path: "skills/hooks"})
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"), "--except", "legacy")
	h.mustRun("equip", "frontend", other.Dir+"#main:skills")
	h.mustRun("spawn", "frontend")

	// A spawned path that is no longer a barracks symlink.
	taken := filepath.Join(h.skillsDir(), "css")
	if err := os.Remove(taken); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(taken, "SKILL.md"), "mine\n")

	_, errb, err := h.run("strip", "frontend", h.sourceArg("skills"))
	if err != nil {
		t.Fatalf("strip: %v\n%s", err, errb)
	}
	if !strings.Contains(errb, "left in place") || !strings.Contains(errb, taken) {
		t.Errorf("a replaced path was not reported:\n%s", errb)
	}
	if got := testutil.ReadFile(t, filepath.Join(taken, "SKILL.md")); got != "mine\n" {
		t.Errorf("a directory barracks did not create was destroyed: %q", got)
	}
}

// TestStripRefusesOverALocallyEditedVendoredFile is acceptance criterion 7 for
// the committed half: the refusal has to arrive with the definition and every
// spawn still untouched.
func TestStripRefusesOverALocallyEditedVendoredFile(t *testing.T) {
	h := newHarness(t)
	other := h.secondSource(testutil.Skill{Path: "skills/hooks"})
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"), "--except", "legacy")
	h.mustRun("equip", "frontend", other.Dir+"#main:skills")
	h.mustRun("garrison", "frontend")
	h.work.Commit(t, "garrison")

	edited := h.garrisonPath(".claude/skills/react/SKILL.md")
	testutil.WriteFile(t, edited, "a teammate edited this\n")

	_, _, err := h.run("strip", "frontend", other.Dir+"#main:skills")
	if err == nil {
		t.Fatal("strip discarded an edited vendored file")
	}
	if !strings.Contains(err.Error(), "modified locally") {
		t.Errorf("refusal = %v", err)
	}
	l, lerr := h.loadout("frontend")
	if lerr != nil || len(l.Equipment) != 2 {
		t.Errorf("the definition moved despite the refusal: %v %v", l, lerr)
	}
	if !testutil.Exists(h.garrisonPath(".claude/skills/hooks/SKILL.md")) {
		t.Error("the source's committed files were removed despite the refusal")
	}
	if got := testutil.ReadFile(t, edited); got != "a teammate edited this\n" {
		t.Errorf("the edit was discarded: %q", got)
	}

	// --force is the user saying to take it anyway.
	h.mustRun("strip", "frontend", other.Dir+"#main:skills", "--force")
	if testutil.Exists(h.garrisonPath(".claude/skills/hooks")) {
		t.Error("--force did not complete the removal")
	}
}

// TestStripRefusesWhileASessionHoldsTheSpawn: an upgrade may leave a live
// session alone because the source is still equipped and the next run plans the
// same move again. A stripped source is gone from the definition, so nothing
// would ever come back for the links it left - refusing keeps that impossible.
func TestStripRefusesWhileASessionHoldsTheSpawn(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend")
	h.mustRun("spawn", "frontend")

	// A process lease whose owner the prober still reports as alive, exactly as
	// a live `barracks run` session leaves one.
	store := leaseStore(t, h)
	held, _ := store.List()
	held[0].Kind = lease.KindProcess
	held[0].Owner = ownerFor(4242, "a-live-agent-session")
	if err := store.Save(held[0]); err != nil {
		t.Fatal(err)
	}
	h.prober.alive[4242] = "a-live-agent-session"

	_, _, err := h.run("strip", "frontend", h.sourceArg("skills"))
	if err == nil {
		t.Fatal("strip took skills out from under a live session")
	}
	if !strings.Contains(err.Error(), "held by a running session") {
		t.Errorf("refusal = %v", err)
	}
	l, lerr := h.loadout("frontend")
	if lerr != nil || len(l.Equipment) != 1 {
		t.Errorf("the definition moved despite the refusal: %v %v", l, lerr)
	}
}

// TestStripRefusesASpellingItCannotResolve: removal never guesses.
func TestStripRefusesASpellingItCannotResolve(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend")
	h.mustRun("equip", "frontend", h.src.Dir+"#main:skills/react")

	cases := map[string]struct{ arg, want string }{
		"not equipped at all":         {"gh:nobody/nothing", "not equipped"},
		"a subpath that is not there": {h.src.Dir + "#main:nowhere", "not equipped"},
		"could be either entry":       {h.src.Dir, "more than one"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := h.run("strip", "frontend", tc.arg)
			if err == nil {
				t.Fatalf("%s was accepted", tc.arg)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
			l, _ := h.loadout("frontend")
			if len(l.Equipment) != 2 {
				t.Errorf("a refused strip changed the definition: %v", l.Idents())
			}
		})
	}
}
