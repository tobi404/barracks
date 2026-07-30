package cli

import (
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/tobi404/barracks/internal/lease"
	"github.com/tobi404/barracks/internal/testutil"
)

// deployedSkills is what a repository is actually carrying, read off disk
// rather than out of a record - which is the only reading that can contradict
// the record.
func (h *harness) deployedSkills() []string {
	h.t.Helper()
	names := testutil.Entries(h.t, h.skillsDir())
	sort.Strings(names)
	return names
}

// liveLeases is every lease record on this machine, oldest first.
func (h *harness) liveLeases() []*lease.Lease {
	h.t.Helper()
	leases, problems := lease.NewStore(h.layout.LeasesDir()).List()
	for _, p := range problems {
		h.t.Fatalf("unreadable lease: %v", p)
	}
	return leases
}

func (h *harness) onlyLease() *lease.Lease {
	h.t.Helper()
	leases := h.liveLeases()
	if len(leases) != 1 {
		h.t.Fatalf("want exactly one lease, got %d", len(leases))
	}
	return leases[0]
}

// TestSpawnOnlyDeploysPartOfALoadoutWithoutRedefiningIt is the command half of
// the capability, end to end.
//
// The two things it holds apart are the whole point: what went out is narrower,
// and the loadout is exactly what it was. A user who reaches for --only from
// `barracks equip`'s vocabulary must not find that they have permanently
// rewritten the unit.
func TestSpawnOnlyDeploysPartOfALoadoutWithoutRedefiningIt(t *testing.T) {
	h := newHarness(t)
	statusBefore := h.work.Status(t)
	excludeBefore := h.work.ReadExclude(t)

	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"))

	out := h.mustRun("spawn", "frontend", "--only", "react,css")
	for _, want := range []string{"+ react", "+ css", "1 skill left behind"} {
		if !strings.Contains(out, want) {
			t.Errorf("spawn --only output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "+ legacy") {
		t.Errorf("--only deployed a skill it excluded:\n%s", out)
	}

	if got := h.deployedSkills(); !reflect.DeepEqual(got, []string{"css", "react"}) {
		t.Fatalf("the repository carries %v, want css and react", got)
	}
	if status := h.work.Status(t); status != statusBefore {
		t.Errorf("a narrowed spawn dirtied git status:\n%s", status)
	}

	// The lease is the record of what is standing, and it says exactly that.
	lz := h.onlyLease()
	if !reflect.DeepEqual(linkNames(lz), []string{"css", "react"}) {
		t.Errorf("the lease records links %v", linkNames(lz))
	}
	if !reflect.DeepEqual(lz.Selection, []string{"css", "react"}) {
		t.Errorf("the lease records selection %v", lz.Selection)
	}

	// The loadout still carries all three, and `barracks list` still says so:
	// the deployment was narrowed, not the unit.
	list := h.mustRun("list", "--verbose")
	for _, want := range []string{"react", "css", "legacy"} {
		if !strings.Contains(list, want) {
			t.Errorf("the loadout lost %s from its definition:\n%s", want, list)
		}
	}

	// And `deployed` reports the narrowing rather than a count that reads as a
	// deployment which has lost a skill.
	dep := h.mustRun("deployed")
	if !strings.Contains(dep, "2 skills") || !strings.Contains(dep, "narrowed at spawn") {
		t.Errorf("deployed does not describe the narrowing:\n%s", dep)
	}

	// A recall removes exactly what was deployed - no more, because the skill
	// left behind was never barracks' to remove, and no less, because a link
	// left standing is a lease nothing will ever clean up.
	h.mustRun("recall", "frontend")
	if got := h.deployedSkills(); len(got) != 0 {
		t.Errorf("recall left %v behind", got)
	}
	if got := h.work.ReadExclude(t); got != excludeBefore {
		t.Errorf(".git/info/exclude not restored\n got: %q\nwant: %q", got, excludeBefore)
	}
	if status := h.work.Status(t); status != statusBefore {
		t.Errorf("git status not restored after recalling a narrowed spawn:\n%s", status)
	}
	if testutil.Exists(filepath.Join(h.work.Dir, ".claude")) {
		t.Error(".claude survived the recall of a narrowed spawn")
	}
	if len(h.liveLeases()) != 0 {
		t.Error("the lease survived the recall")
	}

	// Spawn again with nothing said and the whole unit goes back out. Nothing
	// remembered the narrowing, which is the definition of "this deployment
	// only".
	h.mustRun("spawn", "frontend")
	if got := h.deployedSkills(); !reflect.DeepEqual(got, []string{"css", "legacy", "react"}) {
		t.Errorf("a plain spawn after a narrowed one carries %v", got)
	}
	if h.onlyLease().Narrowed() {
		t.Error("a plain spawn recorded a selection")
	}
}

// --except is the other spelling and reaches the same place.
func TestSpawnExceptLeavesNamedSkillsBehind(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"))
	h.mustRun("spawn", "frontend", "--except", "legacy")

	if got := h.deployedSkills(); !reflect.DeepEqual(got, []string{"css", "react"}) {
		t.Errorf("--except deployed %v", got)
	}
	if !reflect.DeepEqual(h.onlyLease().Selection, []string{"css", "react"}) {
		t.Errorf("the lease recorded %v", h.onlyLease().Selection)
	}
}

// A selection that keeps nothing is refused clearly, and refused before
// anything is created.
func TestSpawnRefusesASelectionThatKeepsNoSkills(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"))

	_, _, err := h.run("spawn", "frontend", "--only", "no-such-skill")
	if err == nil {
		t.Fatal("a selection matching nothing was deployed anyway")
	}
	for _, want := range []string{"no skills selected", "react", "css", "legacy"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
	if testutil.Exists(h.skillsDir()) {
		t.Error("a refused spawn created a skills directory")
	}
	if len(h.liveLeases()) != 0 {
		t.Error("a refused spawn wrote a lease")
	}
}

// TestANarrowedSpawnIsNotDriftAndAnUpgradeKeepsIt is the two commands that read
// a deployment rather than making one.
//
// `inspect` verifies the committed tier against barracks.lock, and a narrowed
// spawn is not part of that tier at all - it is symlinks under a lease. A
// checkout carrying one must still come back clean, or the one command a team
// runs in CI would fail over somebody else's personal deployment.
//
// `upgrade` reaches both tiers, and it must move the chosen skills forward
// without filling in the ones that were left behind.
func TestANarrowedSpawnIsNotDriftAndAnUpgradeKeepsIt(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"))
	h.mustRun("train", "backline")
	h.mustRun("equip", "backline", h.sourceArg("skills"), "--only", "legacy")
	// A garrison alongside, so inspect has something real to check rather than
	// passing because the repository has no committed tier at all.
	h.mustRun("garrison", "backline")
	h.mustRun("spawn", "frontend", "--only", "react")

	out := h.mustRun("inspect")
	if !strings.Contains(out, "ok") {
		t.Fatalf("inspect over a narrowed spawn = %q", out)
	}
	for _, never := range []string{"css", "missing", "not in the lockfile"} {
		if strings.Contains(out, never) {
			t.Errorf("inspect reported a narrowed spawn as drift (%q):\n%s", never, out)
		}
	}

	// Upstream gains a skill and changes the chosen one.
	h.src.AddSkills(t, testutil.Skill{Path: "skills/react", Body: "second edition"},
		testutil.Skill{Path: "skills/newcomer"})
	h.src.Commit(t, "move on")

	h.mustRun("upgrade", "frontend")
	if got := h.deployedSkills(); !reflect.DeepEqual(got, []string{"legacy", "react"}) {
		// legacy is the garrison's committed file, not a spawn of frontend.
		t.Fatalf("after the upgrade the repository carries %v, want the garrison plus react", got)
	}
	if got := testutil.ReadFile(t, filepath.Join(h.skillsDir(), "react", "SKILL.md")); got != "second edition" {
		t.Errorf("the chosen skill did not move forward: %q", got)
	}
	if !reflect.DeepEqual(h.onlyLease().Selection, []string{"react"}) {
		t.Errorf("the upgrade rewrote the selection to %v", h.onlyLease().Selection)
	}
	if out := h.mustRun("inspect"); !strings.Contains(out, "ok") {
		t.Errorf("inspect after upgrading a narrowed spawn = %q", out)
	}
}

func linkNames(l *lease.Lease) []string {
	out := make([]string, 0, len(l.Links))
	for _, link := range l.Links {
		out = append(out, link.Skill)
	}
	sort.Strings(out)
	return out
}

// TestTheFlagAndThePickerNarrowADeploymentIdentically is the claim that makes
// this one capability rather than two.
//
// AGENTS.md's rule is that the roster and the commands must be incapable of
// disagreeing, and a picker that produced a different deployment from the flag
// that means the same thing is exactly the class of defect that rule exists for.
// So the same narrowing is expressed both ways against the same repository, and
// what is left on disk and in the record is compared - not the words either
// surface used to describe it.
func TestTheFlagAndThePickerNarrowADeploymentIdentically(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"))

	h.mustRun("spawn", "frontend", "--only", "css,react")
	byFlag := struct {
		skills    []string
		links     []string
		selection []string
		dir       string
		target    string
	}{h.deployedSkills(), linkNames(h.onlyLease()), h.onlyLease().Selection, h.onlyLease().Dir, h.onlyLease().Target}
	h.mustRun("recall", "frontend")
	if len(h.liveLeases()) != 0 {
		t.Fatal("the flag's spawn was not fully recalled before the picker's")
	}

	// The same choice on the roster: the picker opens with every skill ticked,
	// so the equivalent of --only css,react is unticking legacy. The registry's
	// five agents come first and the loadout's skills follow in name order, so
	// legacy is the seventh row.
	before := plain(h.frame(120, 40, "s", "j", "j", "j", "j", "j", "j"))
	if !strings.Contains(before, "▸ [x] legacy") {
		t.Fatalf("the cursor is not on the skill this test unticks:\n%s", before)
	}
	if !strings.Contains(before, "SKILLS") || !strings.Contains(before, "TARGETS") {
		t.Fatalf("the deploy card does not offer both bands:\n%s", before)
	}
	card := plain(h.frame(120, 40, "s", "j", "j", "j", "j", "j", "j", "space", "y", "@pump"))
	if !strings.Contains(card, "FRONTLINE") && !strings.Contains(card, "FRONTEND DEPLOYED") {
		t.Fatalf("the roster's deploy did not report success:\n%s", card)
	}

	lz := h.onlyLease()
	if got := h.deployedSkills(); !reflect.DeepEqual(got, byFlag.skills) {
		t.Errorf("the picker put %v on disk, the flag put %v", got, byFlag.skills)
	}
	if got := linkNames(lz); !reflect.DeepEqual(got, byFlag.links) {
		t.Errorf("the picker recorded links %v, the flag recorded %v", got, byFlag.links)
	}
	if !reflect.DeepEqual(lz.Selection, byFlag.selection) {
		t.Errorf("the picker recorded selection %v, the flag recorded %v", lz.Selection, byFlag.selection)
	}
	if lz.Dir != byFlag.dir || lz.Target != byFlag.target {
		t.Errorf("the picker deployed to %s/%s, the flag to %s/%s", lz.Target, lz.Dir, byFlag.target, byFlag.dir)
	}

	// And the report says the same thing about what was left behind.
	if !strings.Contains(card, "1 skill left behind") {
		t.Errorf("the roster did not say a skill was left behind:\n%s", card)
	}
}

// A deploy with every skill unticked is refused on the card, in the same shape
// the empty-target refusal already had - and refused before the engine is asked
// to do anything.
func TestTheRosterRefusesADeployWithNoSkillsChosen(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"))

	// Untick all three skills: rows six, seven and eight.
	card := plain(h.frame(120, 40,
		"s", "j", "j", "j", "j", "j", "space", "j", "space", "j", "space", "y", "@pump"))
	if !strings.Contains(card, "Choose at least one skill") {
		t.Errorf("the refusal never reached the card:\n%s", card)
	}
	if !strings.Contains(card, "DEPLOY ORDER") {
		t.Errorf("the order was withdrawn instead of refused:\n%s", card)
	}
	if len(h.liveLeases()) != 0 {
		t.Error("a deploy with no skills chosen was carried out anyway")
	}
	if testutil.Exists(h.skillsDir()) {
		t.Error("a refused deploy created a skills directory")
	}
}

// A narrowed deployment is visible on the roster without opening the dossier,
// and the dossier names exactly which skills are standing.
//
// A posture column reading "deployed" over three of a unit's eight skills is
// the roster asserting something untrue, and the user finds out from the skills
// the agent cannot see - the same failure class as an upgrade screen reporting
// success over a failure.
func TestTheRosterShowsThatADeploymentIsPartial(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"))

	whole := plain(h.frame(120, 40))
	if !strings.Contains(rosterRow(t, whole, "frontend"), "· in reserve") {
		t.Fatalf("a unit deployed nowhere does not read as in reserve:\n%s", whole)
	}

	h.mustRun("spawn", "frontend", "--only", "react")
	partial := plain(h.frame(120, 40))
	// The row itself, not the frame: the posture key spells out every glyph, so
	// searching the whole screen for one would pass over any roster at all.
	if row := rosterRow(t, partial, "frontend"); !strings.Contains(row, "◐ partial") {
		t.Errorf("the roster row does not mark a partial deployment: %q\n%s", row, partial)
	}
	// The dossier says how much of the unit is standing, and which of it. It is
	// read from DEPLOYMENTS down, because the EQUIPMENT section above lists
	// every skill the unit carries and would answer this question wrongly.
	standing := after(t, partial, "DEPLOYMENTS")
	if !strings.Contains(standing, "1 of 3 skills") {
		t.Errorf("the dossier does not count the deployment against the unit:\n%s", standing)
	}
	if !strings.Contains(standing, "react") {
		t.Errorf("the dossier does not name the skill that is standing:\n%s", standing)
	}
	for _, absent := range []string{"css", "legacy"} {
		if strings.Contains(standing, absent) {
			t.Errorf("the dossier lists %s as standing when it was left behind:\n%s", absent, standing)
		}
	}

	// A whole deployment is the other reading, and must not have become
	// partial by accident.
	h.mustRun("recall", "frontend")
	h.mustRun("spawn", "frontend")
	full := plain(h.frame(120, 40))
	row := rosterRow(t, full, "frontend")
	if !strings.Contains(row, "● deployed") || strings.Contains(row, "◐ partial") {
		t.Errorf("a whole deployment does not read as deployed: %q", row)
	}
	if got := after(t, full, "DEPLOYMENTS"); !strings.Contains(got, "3 skills") {
		t.Errorf("a whole deployment is not counted plainly:\n%s", got)
	}
}

// rosterRow is the unit's own line in the roster pane, found by the cursor
// marker rather than by the name alone - the dossier and the posture key both
// repeat words a name search would land on.
func rosterRow(t *testing.T, frame, name string) string {
	t.Helper()
	for _, line := range strings.Split(frame, "\n") {
		if strings.Contains(line, "▸ "+name) {
			return strings.TrimSpace(line)
		}
	}
	t.Fatalf("no roster row for %s in:\n%s", name, frame)
	return ""
}

// after is everything from the line carrying marker to the end of the frame.
func after(t *testing.T, frame, marker string) string {
	t.Helper()
	i := strings.Index(frame, marker)
	if i < 0 {
		t.Fatalf("no %s in:\n%s", marker, frame)
	}
	return frame[i:]
}
