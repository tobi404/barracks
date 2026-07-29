package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/tobi404/barracks/internal/garrison"
	"github.com/tobi404/barracks/internal/lease"
	"github.com/tobi404/barracks/internal/loadout"
	"github.com/tobi404/barracks/internal/source"
)

// The whole point of the shape this package is written in: every screen below
// is driven with no terminal, no goroutine and no timing, because Update is a
// pure function of (model, message) and View is a pure function of the model.

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;:]*[a-zA-Z]`)

func plain(s string) string { return ansiRE.ReplaceAllString(s, "") }

// fakeRecords stands in for the stores. internal/cli's own tests drive the
// roster against the real ones; this is for the shapes those are awkward to put
// on disk - an unreadable definition, a spawn in another repository.
type fakeRecords struct {
	loadouts  []*loadout.Loadout
	leases    []*lease.Lease
	garrisons []garrison.Garrison
	root      string
	problems  []error
	lerr      []error
	gerr      error
}

func (f fakeRecords) Loadouts() ([]*loadout.Loadout, []error) { return f.loadouts, f.problems }
func (f fakeRecords) Leases() ([]*lease.Lease, []error)       { return f.leases, f.lerr }
func (f fakeRecords) Root() string                            { return f.root }
func (f fakeRecords) Garrisons(string) (*garrison.Manifest, error) {
	if f.gerr != nil {
		// Exactly what garrison.Load hands back when it cannot read the
		// lockfile: the error and nothing else.
		return nil, f.gerr
	}
	return &garrison.Manifest{Version: garrison.Version, Garrisons: f.garrisons}, nil
}

func unitLoadout(name string, skills ...string) *loadout.Loadout {
	l := &loadout.Loadout{
		Name:      name,
		ID:        "id-" + name,
		CreatedAt: time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC),
	}
	if len(skills) > 0 {
		l.Equipment = []loadout.Equipment{{
			Source: source.Source{Host: "github.com", Owner: "unit", Repo: name, Ref: "main"},
			Commit: "0123456789abcdef",
			Skills: skills,
		}}
	}
	return l
}

func spawnedLease(name, root, dir string, links int) *lease.Lease {
	l := &lease.Lease{
		ID: "lease-" + name + "-" + dir, Loadout: name, Target: "claude",
		Scope: lease.ScopeRepo, Root: root, Dir: dir, Kind: lease.KindManual,
	}
	for i := 0; i < links; i++ {
		l.Links = append(l.Links, lease.Link{Skill: fmt.Sprintf("skill-%d", i)})
	}
	return l
}

func cfgFor(r reader) Config {
	dark := true
	return Config{Records: r, Version: "v9.9.9 (test)", Dark: &dark}
}

func TestGatherSeparatesHereFromAfieldAndCommitted(t *testing.T) {
	g := garrison.Garrison{Loadout: "frontline", Targets: []string{"claude"}}
	st := gather(fakeRecords{
		root:      "/repo",
		loadouts:  []*loadout.Loadout{unitLoadout("siegeworks", "a"), unitLoadout("frontline", "b"), unitLoadout("reserves")},
		garrisons: []garrison.Garrison{g},
		leases: []*lease.Lease{
			spawnedLease("siegeworks", "/repo", "/repo/.claude/skills", 3),
			spawnedLease("siegeworks", "/elsewhere", "/elsewhere/.claude/skills", 1),
		},
	})

	if len(st.Units) != 3 {
		t.Fatalf("expected 3 units, got %d", len(st.Units))
	}
	if st.Units[0].Loadout.Name != "frontline" {
		t.Errorf("units are not name-sorted: %s first", st.Units[0].Loadout.Name)
	}
	byName := map[string]unit{}
	for _, u := range st.Units {
		byName[u.Loadout.Name] = u
	}
	if got := byName["siegeworks"]; len(got.Here) != 1 || got.Away != 1 {
		t.Errorf("siegeworks here=%d away=%d, want 1 and 1", len(got.Here), got.Away)
	}
	if byName["frontline"].Committed == nil {
		t.Error("the garrison did not attach to frontline")
	}
	if got := byName["frontline"].Status(); got != "held" {
		t.Errorf("frontline status = %q", got)
	}
	if got := byName["reserves"].Status(); got != "unequipped" {
		t.Errorf("reserves status = %q", got)
	}
	if byName["reserves"].Deployed() {
		t.Error("an unequipped loadout reported itself deployed")
	}
}

// Which lockfile entry belongs to a loadout is the lockfile's own rule, and the
// roster asks it rather than comparing names. A loadout garrisoned into two
// clones and then renamed leaves the other clone's barracks.lock carrying the
// old name and the same identity - `barracks deployed` matches that, and a
// roster that did not would call a committed unit "in reserve". The other half
// matters just as much: an entry written before identities existed carries none
// at all, and that absence must never read as a mismatch.
func TestAGarrisonIsMatchedTheWayTheLockfileMatchesIt(t *testing.T) {
	renamed := unitLoadout("vanguard", "a") // trained as "frontline", renamed here
	legacy := unitLoadout("siegeworks", "b")
	imposter := unitLoadout("scouts", "c")

	st := gather(fakeRecords{
		root:     "/repo",
		loadouts: []*loadout.Loadout{renamed, legacy, imposter},
		garrisons: []garrison.Garrison{
			{Loadout: "frontline", ID: renamed.ID, Targets: []string{"claude"}},
			{Loadout: "siegeworks", Targets: []string{"claude"}},
			{Loadout: "scouts", ID: "id-somebody-elses-scouts", Targets: []string{"claude"}},
		},
	})
	byName := map[string]unit{}
	for _, u := range st.Units {
		byName[u.Loadout.Name] = u
	}
	if got := byName["vanguard"].Committed; got == nil {
		t.Error("a renamed loadout did not find the garrison recorded under its old name")
	} else if got.Loadout != "frontline" {
		t.Errorf("the wrong garrison attached: %q", got.Loadout)
	}
	if byName["siegeworks"].Committed == nil {
		t.Error("an entry written before identities existed was read as a mismatch")
	}
	if byName["scouts"].Committed != nil {
		t.Error("a garrison whose identity disagrees was attributed to this loadout anyway")
	}
}

// A loadout the roster cannot read is the one thing a roster must not silently
// omit, so every unreadable record is surfaced instead.
func TestGatherSurfacesUnreadableRecords(t *testing.T) {
	st := gather(fakeRecords{
		root:     "/repo",
		problems: []error{errors.New("parse loadout broken: bad yaml")},
		lerr:     []error{errors.New("unreadable lease record")},
		gerr:     errors.New("barracks.lock was written by a newer barracks"),
	})
	if len(st.Problems) != 3 {
		t.Fatalf("expected 3 problems, got %v", st.Problems)
	}
	frame := plain(Frame(cfgFor(fakeRecords{root: "/repo", problems: st.errs()}), 120, 30))
	if !strings.Contains(frame, "bad yaml") {
		t.Errorf("an unreadable loadout never reached the screen:\n%s", frame)
	}
}

// errs re-exposes the gathered problems as errors, so the frame test above can
// feed them back through the same path a real read would.
func (s state) errs() []error {
	out := make([]error, 0, len(s.Problems))
	for _, p := range s.Problems {
		out = append(out, errors.New(p))
	}
	return out
}

func TestRosterAndDossierRenderTheRecord(t *testing.T) {
	r := fakeRecords{
		root:     "/repo",
		loadouts: []*loadout.Loadout{unitLoadout("frontline", "react-forms", "css-armory"), unitLoadout("reserves")},
		leases:   []*lease.Lease{spawnedLease("frontline", "/repo", "/repo/.claude/skills", 2)},
	}
	got := plain(Frame(cfgFor(r), 120, 30))
	for _, want := range []string{
		"B A R R A C K S", "v9.9.9", "ROSTER", "DOSSIER", "POSTURE",
		"frontline", "reserves", "deployed", "unequipped",
		"react-forms", "css-armory", "pinned 0123456",
		"./.claude/skills",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("frame is missing %q:\n%s", want, got)
		}
	}
	for _, line := range strings.Split(got, "\n") {
		if n := len([]rune(line)); n > 120 {
			t.Fatalf("a line is %d columns wide in a 120 column terminal: %q", n, line)
		}
	}
}

func TestRosterOutsideARepository(t *testing.T) {
	got := plain(Frame(cfgFor(fakeRecords{loadouts: []*loadout.Loadout{unitLoadout("frontline", "a")}}), 100, 24, "s"))
	if !strings.Contains(got, "no repository here") {
		t.Errorf("the header did not say there is no repository:\n%s", got)
	}
	if !strings.Contains(got, "nowhere here to deploy") {
		t.Errorf("deploying outside a repository said nothing:\n%s", got)
	}
}

func TestEmptyRosterSaysHowToFillIt(t *testing.T) {
	got := plain(Frame(cfgFor(fakeRecords{root: "/repo"}), 100, 24, "s", "r"))
	if !strings.Contains(got, "no units trained") || !strings.Contains(got, "No units on the roster") {
		t.Errorf("an empty roster is unhelpful:\n%s", got)
	}
	if !strings.Contains(got, "No unit selected") {
		t.Errorf("an order with nothing selected said nothing:\n%s", got)
	}
}

func TestCursorMovesAndWraps(t *testing.T) {
	r := fakeRecords{root: "/repo", loadouts: []*loadout.Loadout{
		unitLoadout("alpha"), unitLoadout("bravo"), unitLoadout("charlie"),
	}}
	for _, tc := range []struct {
		keys []string
		want string
	}{
		{[]string{}, "alpha"},
		{[]string{"j"}, "bravo"},
		{[]string{"down", "down"}, "charlie"},
		{[]string{"k"}, "charlie"}, // wraps off the top to the bottom
		{[]string{"j", "j", "j"}, "alpha"},
	} {
		got := plain(Frame(cfgFor(r), 100, 24, tc.keys...))
		// The dossier titles the selected unit, which is the unambiguous read.
		if !strings.Contains(got, "DOSSIER") || !strings.Contains(strings.SplitN(got, "DOSSIER", 2)[1], tc.want) {
			t.Errorf("keys %v did not select %s:\n%s", tc.keys, tc.want, got)
		}
	}
}

// A roster longer than the pane must scroll rather than stop at the bottom.
func TestLongRosterWindowsAroundTheCursor(t *testing.T) {
	var ls []*loadout.Loadout
	for i := 0; i < 40; i++ {
		ls = append(ls, unitLoadout(fmt.Sprintf("unit-%02d", i)))
	}
	r := fakeRecords{root: "/repo", loadouts: ls}

	top := plain(Frame(cfgFor(r), 100, 20))
	if !strings.Contains(top, "unit-00") || strings.Contains(top, "unit-39") {
		t.Errorf("the first frame is not showing the top of the roster:\n%s", top)
	}
	if !strings.Contains(top, "of 40") {
		t.Errorf("a windowed roster did not say how much it is hiding:\n%s", top)
	}

	keys := make([]string, 39)
	for i := range keys {
		keys[i] = "j"
	}
	bottom := plain(Frame(cfgFor(r), 100, 20, keys...))
	if !strings.Contains(bottom, "unit-39") {
		t.Errorf("the cursor left the window:\n%s", bottom)
	}
}

// A pane pads to the height it declares but never truncates, so a pane that
// draws more rows than it has grows the whole frame and the terminal cuts the
// bottom off: first the status line, which is the roster's only channel for a
// refusal, then the help bar. Everything that could push it over is on screen
// here at once - a roster too long to fit, an unreadable record, and a refused
// order - and all three have to survive on a terminal nobody would call large.
func TestTheFrameNeverOutgrowsTheTerminal(t *testing.T) {
	var ls []*loadout.Loadout
	for i := 0; i < 30; i++ {
		ls = append(ls, unitLoadout(fmt.Sprintf("unit-%02d", i)))
	}
	r := fakeRecords{
		root:     "/repo",
		loadouts: ls,
		problems: []error{errors.New("parse loadout broken: bad yaml")},
	}

	for _, size := range [][2]int{{80, 24}, {100, 20}, {120, 30}, {70, 16}} {
		w, h := size[0], size[1]
		// "s" on a unit that carries nothing is a refusal, and the status line
		// is the only place the roster can put one.
		got := plain(Frame(cfgFor(r), w, h, "s"))
		fits(t, got, w, h, fmt.Sprintf("%dx%d roster", w, h))
		for _, want := range []string{
			"carries nothing", // the status line
			"parse loadout",   // the unreadable record, which must never be dropped
			"of 30",           // the count that says the roster is windowed
		} {
			if !strings.Contains(got, want) {
				t.Errorf("%dx%d: %q fell off the frame:\n%s", w, h, want, got)
			}
		}
	}
}

// The footer must name the way out, on every terminal anyone actually uses.
//
// The help bar is elided from the end when it is wider than the screen, so which
// entries survive is decided by the order ShortHelp returns them in - and 80
// columns has room for about six of the seven. What must never be among the ones
// that go is `q dismissed` and `? orders`: the first is how you leave the roster,
// the second is the only other place every key is written down. This is a
// statement about what may not be elided, not about what the bar happens to hold
// today, so it is asserted at the width the question is really about.
func TestTheFooterAlwaysNamesTheWayOut(t *testing.T) {
	r := fakeRecords{root: "/repo", loadouts: []*loadout.Loadout{unitLoadout("alpha", "a")}}

	for _, size := range [][2]int{{80, 24}, {60, 20}, {70, 16}, {100, 30}, {160, 50}} {
		w, h := size[0], size[1]
		got := plain(Frame(cfgFor(r), w, h))
		fits(t, got, w, h, fmt.Sprintf("%dx%d footer", w, h))
		for _, want := range []string{"q dismissed", "? orders"} {
			if !strings.Contains(got, want) {
				t.Errorf("%dx%d: the footer never says %q, so the roster does not advertise how to leave it:\n%s", w, h, want, got)
			}
		}
	}
}

// fits is the invariant every screen has to hold: the frame is the size of the
// terminal it is drawn on. The alternate screen does not scroll a larger one,
// it clips it, and what falls off is the bottom and the right.
func fits(t *testing.T, frame string, w, h int, what string) {
	t.Helper()
	lines := strings.Split(frame, "\n")
	if len(lines) > h {
		t.Errorf("%s: the frame is %d rows tall, so the terminal clips %d of them:\n%s", what, len(lines), len(lines)-h, frame)
	}
	for i, line := range lines {
		if n := len([]rune(line)); n > w {
			t.Errorf("%s: row %d is %d columns wide in a %d column terminal: %q", what, i, n, w, line)
		}
	}
}

// A card is a layer over the roster, and the compositor's bounds are the union
// of its layers, so a card taller than the screen takes the whole frame with it
// - the same clipping the roster pane was fixed for, on the path the pane's own
// budget does not cover. A spawn from a source carrying twenty skills reports a
// line per skill, so this is the ordinary shape of a large deploy rather than a
// contrived one.
func TestAnOutcomeCardNeverOutgrowsTheTerminal(t *testing.T) {
	r := fakeRecords{root: "/repo/lab", loadouts: []*loadout.Loadout{unitLoadout("frontline", "a")}}

	var lines []string
	lines = append(lines, "targets: claude (detected)")
	lines = append(lines, "Claude Code  20 skills")
	for i := 0; i < 20; i++ {
		lines = append(lines, fmt.Sprintf("  + skill-%02d", i))
	}
	notices := []string{
		"left in place (barracks did not create it): /repo/lab/.claude/skills/react-forms - not a symlink barracks made",
		"left in place (barracks did not create it): /repo/lab/.claude/skills/css-armory - not a symlink barracks made",
	}

	cfg := cfgFor(r)
	cfg.Deploy = (&deployTracker{outcome: Outcome{
		Title: "frontline deployed", Lines: lines, Notices: notices,
	}}).deploy

	for _, tc := range []struct {
		w, h int
		want []string
	}{
		// The skill list is cut to fit; every notice, the headline and the way
		// out survive, and "more" is the card saying how much it stood down.
		{80, 24, []string{"FRONTLINE DEPLOYED", "react-forms", "css-armory", "any key to return to the roster", "more"}},
		// Room for all of it, so none of it is cut.
		{120, 40, []string{"FRONTLINE DEPLOYED", "react-forms", "css-armory", "any key to return to the roster"}},
		// A card with room for neither cannot show both, but it still says so:
		// the body shrinks to a count before a notice loses a single row.
		{90, 14, []string{"FRONTLINE DEPLOYED", "any key to return to the roster", "more"}},
	} {
		got := plain(Frame(cfg, tc.w, tc.h, "s", "y", "@pump"))
		fits(t, got, tc.w, tc.h, fmt.Sprintf("%dx%d outcome", tc.w, tc.h))
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Errorf("%dx%d: %q fell off the outcome card:\n%s", tc.w, tc.h, want, got)
			}
		}
	}
}

// A barracks error names a path, and wrap hard-breaks it, so a refusal is the
// other way an outcome card can outgrow the screen.
func TestARefusalCardNeverOutgrowsTheTerminal(t *testing.T) {
	r := fakeRecords{root: "/repo/lab", loadouts: []*loadout.Loadout{unitLoadout("frontline", "a")}}
	cfg := cfgFor(r)
	cfg.Deploy = (&deployTracker{outcome: Outcome{
		Err: errors.New("spawn into Claude Code: target path already occupied: " +
			strings.Repeat("/deeply/nested/directory", 20) + "/react-forms is committed to this repository"),
		Notices: []string{"left in place (barracks did not create it): /repo/lab/.claude/skills/css-armory"},
	}}).deploy

	for _, size := range [][2]int{{80, 24}, {100, 30}} {
		w, h := size[0], size[1]
		got := plain(Frame(cfg, w, h, "s", "y", "@pump"))
		fits(t, got, w, h, fmt.Sprintf("%dx%d refusal", w, h))
		for _, want := range []string{"REFUSED", "css-armory", "any key to return to the roster"} {
			if !strings.Contains(got, want) {
				t.Errorf("%dx%d: %q fell off the refusal card:\n%s", w, h, want, got)
			}
		}
	}
}

// The other two cards are short and fixed, but nothing may be exempt from the
// rule - a card that grew for any reason would clip the frame the same way.
func TestEveryOverlayFitsTheTerminal(t *testing.T) {
	r := fakeRecords{
		root:     "/repo/lab",
		loadouts: []*loadout.Loadout{unitLoadout("frontline", "a", "b")},
		leases:   []*lease.Lease{spawnedLease("frontline", "/repo/lab", "/repo/lab/.claude/skills", 2)},
	}
	cfg := cfgFor(r)
	cfg.Deploy = (&deployTracker{}).deploy
	cfg.Recall = func(context.Context, *loadout.Loadout) Outcome { return Outcome{Title: "frontline recalled"} }

	for _, size := range [][2]int{{80, 24}, {60, 16}, {140, 40}} {
		w, h := size[0], size[1]
		for _, script := range [][]string{{"?"}, {"s"}, {"r"}, {"s", "y"}} {
			got := plain(Frame(cfg, w, h, script...))
			fits(t, got, w, h, fmt.Sprintf("%dx%d overlay %v", w, h, script))
		}
	}
}

// The dossier counts things, and a count of one is not "1 skills".
//
// barracks says "1 skill" everywhere else it counts, so this is only ever the
// roster disagreeing with the rest of the product. Every count the dossier
// prints is asserted, not just the one that was noticed: a site that hardcodes
// the plural reads correctly for every number but one, so nothing short of
// looking at that number finds it.
func TestTheDossierCountsInTheSingular(t *testing.T) {
	r := fakeRecords{
		root:     "/repo/lab",
		loadouts: []*loadout.Loadout{unitLoadout("frontline", "css-ref")},
		leases:   []*lease.Lease{spawnedLease("frontline", "/repo/lab", "/repo/lab/.claude/skills", 1)},
		garrisons: []garrison.Garrison{{
			Loadout: "frontline", ID: "id-frontline", Targets: []string{"claude"},
			Skills: []garrison.Skill{{Name: "css-ref", Target: "claude"}},
		}},
	}
	got := plain(Frame(cfgFor(r), 120, 34))
	for _, want := range []string{
		"1 skill ", // the equipped source's pin line
		"committed to this repository · 1 skill", // the garrison
		"claude · 1 skill · ",                    // the live spawn
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the dossier never says %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "1 skills") {
		t.Errorf("the dossier counts one skill as \"1 skills\":\n%s", got)
	}
}

// TestACardHasACleanEdgeOverAnyDossier is the other half of "a card is a layer
// over the roster": the unit stays visible behind its order, but only in whole
// rows of it.
//
// A card is narrower than the screen, so whatever the dossier had on the rows it
// covers used to keep its tail beside the card - the end of a truncated word, an
// orphaned "reaped" floating past a REFUSED card. That reads as a rendering
// fault, not as context. The claim is about a line of any length, so the records
// here are built to overflow every card at every width rather than to reproduce
// the strings that happened to overflow when this was found.
func TestACardHasACleanEdgeOverAnyDossier(t *testing.T) {
	long := strings.Repeat("armoury-", 30) + "ref"
	l := unitLoadout("frontline", long, long+"-two", long+"-three")
	l.Description = strings.Repeat("a description that runs the whole width of the pane and then some ", 4)
	r := fakeRecords{
		root:      "/repo/lab",
		loadouts:  []*loadout.Loadout{l},
		leases:    []*lease.Lease{spawnedLease("frontline", "/repo/lab", "/repo/lab/"+long, 3)},
		garrisons: []garrison.Garrison{{Loadout: "frontline", ID: "id-frontline", Targets: []string{"claude"}}},
	}
	cfg := cfgFor(r)
	cfg.Deploy = (&deployTracker{outcome: Outcome{
		Err:     errors.New("spawn into Claude Code: " + long + " is committed to this repository"),
		Notices: []string{"left in place (barracks did not create it): /repo/lab/" + long},
	}}).deploy
	cfg.Recall = func(context.Context, *loadout.Loadout) Outcome { return Outcome{Title: "frontline recalled"} }

	for _, size := range [][2]int{{80, 24}, {100, 30}, {140, 40}, {60, 16}} {
		w, h := size[0], size[1]
		for _, script := range [][]string{{"?"}, {"s"}, {"r"}, {"s", "y"}, {"s", "y", "@pump"}} {
			frame := plain(Frame(cfg, w, h, script...))
			assertCleanCardEdge(t, frame, w, fmt.Sprintf("%dx%d overlay %v", w, h, script))
		}
	}
}

// assertCleanCardEdge checks that on every row a card occupies, nothing but
// blank space stands between its right border and the edge of the screen.
//
// The card is found by its own border rather than by recomputing where the
// compositor put it: what is being asserted is the frame the user sees.
func assertCleanCardEdge(t *testing.T, frame string, w int, what string) {
	t.Helper()
	lines := strings.Split(frame, "\n")
	// The card's corners say which rows and which column it owns. Its sides are
	// the same rune on both edges, so a row on its own cannot say where it ends.
	top, bottom := -1, -1
	for i, line := range lines {
		if top < 0 && strings.ContainsRune(line, '╔') {
			top = i
		}
		if strings.ContainsRune(line, '╚') {
			bottom = i
		}
	}
	if top < 0 {
		t.Errorf("%s: no card was drawn, so this proved nothing:\n%s", what, frame)
		return
	}
	if bottom < top {
		bottom = len(lines) - 1 // a card the frame cut the bottom off
	}
	head := []rune(lines[top])
	edge := -1
	for j, r := range head {
		if r == '╗' {
			edge = j
		}
	}
	if edge < 0 {
		// A card at least as wide as the terminal has its own right border cut
		// off by the frame, so there is no band beside it for anything to show
		// through. That the row runs to the very edge is what says so; a row
		// stopping short would mean the corner went missing some other way.
		if len(head) != w {
			t.Errorf("%s: the card's top border stops %d columns short of the screen and never closes: %q",
				what, w-len(head), lines[top])
		}
		return
	}
	for i := top; i <= bottom && i < len(lines); i++ {
		runes := []rune(lines[i])
		if len(runes) <= edge+1 {
			continue
		}
		if tail := strings.TrimSpace(string(runes[edge+1:])); tail != "" {
			t.Errorf("%s: row %d leaves %q past the card's right border: %q", what, i, tail, lines[i])
		}
	}
}

func TestHelpOverlayOpensAndCloses(t *testing.T) {
	r := fakeRecords{root: "/repo", loadouts: []*loadout.Loadout{unitLoadout("alpha")}}
	if got := plain(Frame(cfgFor(r), 100, 24, "?")); !strings.Contains(got, "ORDERS") {
		t.Errorf("? did not open the orders overlay:\n%s", got)
	}
	if got := plain(Frame(cfgFor(r), 100, 24, "?", "esc")); strings.Contains(got, "any key to return") {
		t.Errorf("the overlay did not close:\n%s", got)
	}
}

// No key exists that does not do something. The verbs the roster does not drive
// have no binding at all rather than a binding that answers "not in this build":
// a key advertised in the help still has to be explained, and a key that changes
// nothing and says nothing is worse. So an unbound key must leave the screen
// exactly as it was - not act, and not claim anything either.
func TestUnboundKeysDoNothingAndClaimNothing(t *testing.T) {
	r := fakeRecords{root: "/repo", loadouts: []*loadout.Loadout{unitLoadout("alpha", "a"), unitLoadout("bravo", "b")}}
	idle := plain(Frame(cfgFor(r), 100, 24))
	for _, k := range []string{"t", "e", "g", "u", "x", "z"} {
		got := plain(Frame(cfgFor(r), 100, 24, k))
		if got != idle {
			t.Errorf("%q changed the screen even though nothing is bound to it:\n%s", k, got)
		}
		if strings.Contains(got, "not wired") {
			t.Errorf("%q advertises itself as unwired:\n%s", k, got)
		}
	}
}

// Every key the roster advertises has to reach a branch that does something.
// The help is the contract - a footer promising a key barracks ignores is the
// failure this guards.
func TestEveryAdvertisedKeyIsHandled(t *testing.T) {
	r := fakeRecords{root: "/repo", loadouts: []*loadout.Loadout{unitLoadout("alpha", "a"), unitLoadout("bravo", "b")}}
	cfg := cfgFor(r)
	cfg.Deploy = (&deployTracker{}).deploy
	cfg.Recall = func(context.Context, *loadout.Loadout) Outcome { return Outcome{Title: "alpha recalled"} }

	k := defaultKeys()
	// Confirm and Cancel answer a modal, so they are driven from one.
	prefix := map[string][]string{"y": {"s"}, "enter": {"s"}, "n": {"s"}, "esc": {"s"}}
	for _, b := range append(k.ShortHelp(), k.Confirm, k.Cancel) {
		for _, name := range b.Keys() {
			if name == "q" || name == "ctrl+c" {
				// Quitting leaves the frame it was drawn on, so it is not
				// visible here. TestQuitReturnsTheQuitCommand drives it.
				continue
			}
			script := append(append([]string{}, prefix[name]...), name)
			before := plain(Frame(cfg, 100, 24, prefix[name]...))
			after := plain(Frame(cfg, 100, 24, script...))
			if before == after {
				t.Errorf("%q is advertised in the help but changed nothing (script %v)", name, script)
			}
		}
	}
}

func TestQuitReturnsTheQuitCommand(t *testing.T) {
	m := newModel(cfgFor(fakeRecords{root: "/repo"}))
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if cmd == nil {
		t.Fatal("q returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("q did not quit: %T", cmd())
	}
}

// deployTracker is a stand-in action, so the confirm/work/outcome flow can be
// driven without a store. internal/cli's tests drive the same flow through the
// real spawn engine.
type deployTracker struct {
	calls   int
	report  []string
	outcome Outcome
}

func (d *deployTracker) deploy(_ context.Context, l *loadout.Loadout, report func(string)) Outcome {
	d.calls++
	for _, line := range d.report {
		report(line)
	}
	out := d.outcome
	if out.Title == "" && out.Err == nil {
		out.Title = l.Name + " deployed"
	}
	return out
}

func TestDeployAsksBeforeItActs(t *testing.T) {
	r := fakeRecords{root: "/repo/lab", loadouts: []*loadout.Loadout{unitLoadout("frontline", "a", "b")}}
	d := &deployTracker{}
	cfg := cfgFor(r)
	cfg.Deploy = d.deploy

	got := plain(Frame(cfg, 110, 30, "s"))
	if !strings.Contains(got, "DEPLOY ORDER") || !strings.Contains(got, "Send frontline into lab?") {
		t.Fatalf("s did not raise a deploy order:\n%s", got)
	}
	if d.calls != 0 {
		t.Fatal("the order was carried out before it was confirmed")
	}

	if got := plain(Frame(cfg, 110, 30, "s", "n")); strings.Contains(got, "DEPLOY ORDER") {
		t.Errorf("n did not withdraw the order:\n%s", got)
	}
	if d.calls != 0 {
		t.Fatal("a withdrawn order was still carried out")
	}
}

// A deploy runs with the terminal handed back to it, so what it reports goes to
// the terminal and not to the screen the roster has given up. Both halves are
// the test: the in-flight screen is still what a frame drawn mid-order shows,
// and every line the order reported is on the terminal the user is looking at.
func TestDeployReportsOnTheTerminalItWasHandedAndTheOutcomeAfter(t *testing.T) {
	r := fakeRecords{root: "/repo/lab", loadouts: []*loadout.Loadout{unitLoadout("frontline", "a", "b")}}
	d := &deployTracker{report: []string{
		"github.com/unit/frontline  fetching…",
		"✓ github.com/unit/frontline  fetched 0123456",
	}}
	cfg := cfgFor(r)
	cfg.Deploy = d.deploy

	frame, released := FrameAndTerminal(cfg, 110, 30, "s", "y", "@work")
	if working := plain(frame); !strings.Contains(working, "MOVING OUT") {
		t.Fatalf("no in-flight screen:\n%s", working)
	}
	for _, line := range d.report {
		if !strings.Contains(released, line) {
			t.Errorf("progress line %q never reached the terminal:\n%s", line, released)
		}
	}
	if strings.Contains(plain(frame), "fetching") {
		t.Errorf("an order drew on the screen it had handed back:\n%s", plain(frame))
	}

	done := plain(Frame(cfg, 110, 30, "s", "y", "@pump"))
	if !strings.Contains(done, "FRONTLINE DEPLOYED") {
		t.Errorf("the outcome never reached the screen:\n%s", done)
	}
	if got := plain(Frame(cfg, 110, 30, "s", "y", "@pump", "enter")); strings.Contains(got, "any key to return") {
		t.Errorf("the outcome panel would not close:\n%s", got)
	}
}

// A refusal and anything barracks declined to touch both have to be shown. The
// second is the one a screen is most likely to swallow.
func TestOutcomeShowsRefusalsAndNotices(t *testing.T) {
	r := fakeRecords{root: "/repo/lab", loadouts: []*loadout.Loadout{unitLoadout("frontline", "a")}}
	cfg := cfgFor(r)
	cfg.Deploy = (&deployTracker{outcome: Outcome{
		Err: errors.New("spawn into Claude Code: target path already occupied: /repo/lab/.claude/skills/react-forms is committed to this repository by loadout frontline"),
	}}).deploy

	got := plain(Frame(cfg, 110, 30, "s", "y", "@pump"))
	if !strings.Contains(got, "REFUSED") || !strings.Contains(got, "already occupied") {
		t.Errorf("the refusal never reached the screen:\n%s", got)
	}

	cfg.Deploy = (&deployTracker{outcome: Outcome{
		Title:   "frontline deployed",
		Lines:   []string{"Claude Code  1 skill"},
		Notices: []string{"left in place (barracks did not create it): /repo/lab/.claude/skills/react-forms"},
	}}).deploy
	got = plain(Frame(cfg, 110, 30, "s", "y", "@pump"))
	if !strings.Contains(got, "left in place") {
		t.Errorf("a notice was swallowed by a successful outcome:\n%s", got)
	}
}

func TestRecallOnlyOffersItselfWhereSomethingIsDeployed(t *testing.T) {
	idle := fakeRecords{root: "/repo/lab", loadouts: []*loadout.Loadout{unitLoadout("frontline", "a")}}
	cfg := cfgFor(idle)
	cfg.Recall = func(context.Context, *loadout.Loadout) Outcome { return Outcome{Title: "frontline recalled"} }
	if got := plain(Frame(cfg, 110, 30, "r")); !strings.Contains(got, "is not deployed here") {
		t.Errorf("recalling an idle unit said nothing:\n%s", got)
	}

	live := idle
	live.leases = []*lease.Lease{spawnedLease("frontline", "/repo/lab", "/repo/lab/.claude/skills", 1)}
	cfg.Records = live
	if got := plain(Frame(cfg, 110, 30, "r")); !strings.Contains(got, "RECALL ORDER") {
		t.Errorf("r did not raise a recall order:\n%s", got)
	}
	if got := plain(Frame(cfg, 110, 30, "r", "y", "@pump")); !strings.Contains(got, "FRONTLINE RECALLED") {
		t.Errorf("the recall outcome never reached the screen:\n%s", got)
	}
}

// Work already underway is not interruptible: a half-applied spawn is the state
// this must never be left in, and the engine's rollback is what guarantees it.
func TestNothingInterruptsWorkUnderway(t *testing.T) {
	r := fakeRecords{root: "/repo/lab", loadouts: []*loadout.Loadout{unitLoadout("frontline", "a")}}
	cfg := cfgFor(r)
	cfg.Deploy = (&deployTracker{}).deploy
	got := plain(Frame(cfg, 110, 30, "s", "y", "esc", "q"))
	if !strings.Contains(got, "MOVING OUT") {
		t.Errorf("a key press left the in-flight screen:\n%s", got)
	}
}

func TestRefreshRereadsTheRecords(t *testing.T) {
	r := fakeRecords{root: "/repo", loadouts: []*loadout.Loadout{unitLoadout("alpha")}}
	got := plain(Frame(cfgFor(r), 100, 24, "R", "@pump"))
	if !strings.Contains(got, "Mustering") || !strings.Contains(got, "alpha") {
		t.Errorf("R did not re-muster:\n%s", got)
	}
}

// The palette is settled by the terminal rather than guessed, so the roster is
// readable on a light background without a flag.
func TestBackgroundColourPicksThePalette(t *testing.T) {
	m := newModel(Config{Records: fakeRecords{root: "/repo"}})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	darkBrass := m.th.brass
	m.Update(tea.BackgroundColorMsg{Color: lightWhite{}})
	if m.th.brass == darkBrass {
		t.Error("a light terminal did not change the palette")
	}
}

type lightWhite struct{}

func (lightWhite) RGBA() (r, g, b, a uint32) { return 0xffff, 0xffff, 0xffff, 0xffff }

func TestInitAsksForTheBackgroundAndTicks(t *testing.T) {
	if newModel(cfgFor(fakeRecords{})).Init() == nil {
		t.Fatal("Init returned no command")
	}
}

func TestResizeIsHandled(t *testing.T) {
	r := fakeRecords{root: "/repo", loadouts: []*loadout.Loadout{unitLoadout("alpha", "a")}}
	for _, size := range []string{"@size:60x18", "@size:200x60", "@size:40x12"} {
		got := plain(Frame(cfgFor(r), 120, 30, size))
		if !strings.Contains(got, "ROSTER") || !strings.Contains(got, "DOSSIER") {
			t.Errorf("%s lost a pane:\n%s", size, got)
		}
	}
}

// Run is the only part of this package that needs a program loop. Bubble Tea
// takes an input and an output, so even that is drivable in-process.
func TestRunOpensAndLeavesOnQ(t *testing.T) {
	var out bytes.Buffer
	cfg := cfgFor(fakeRecords{root: "/repo", loadouts: []*loadout.Loadout{unitLoadout("alpha")}})
	cfg.Input = strings.NewReader("q")
	cfg.Output = &out
	cfg.Width, cfg.Height = 110, 30

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := Run(ctx, cfg); err != nil {
		t.Fatalf("Run returned %v", err)
	}
	if !strings.Contains(plain(out.String()), "B A R R A C K S") {
		t.Errorf("Run drew nothing:\n%q", out.String())
	}
}

// The handover is Bubble Tea's, but what barracks makes of its result is not: a
// terminal that could not be released means the order never ran, and a terminal
// that came back imperfectly after it did is a notice about a spawn that is
// standing there either way.
func TestAHandoverFailureIsToldApartFromARefusal(t *testing.T) {
	boom := errors.New("could not release the terminal")

	never := &terminalJob{run: func(io.Writer) Outcome { return Outcome{Title: "unreachable"} }}
	msg := never.done(boom).(doneMsg)
	if msg.out.Err == nil || !strings.Contains(msg.out.Err.Error(), "release the terminal") {
		t.Errorf("an order that never ran was not reported as refused: %+v", msg.out)
	}
	if msg.out.Title != "" {
		t.Errorf("an order that never ran reported a title: %q", msg.out.Title)
	}

	ran := &terminalJob{run: func(w io.Writer) Outcome {
		fmt.Fprintln(w, "fetching…")
		return Outcome{Title: "frontline deployed"}
	}}
	ran.SetStdout(io.Discard)
	if err := ran.Run(); err != nil {
		t.Fatalf("a refusing order must not fail the handover: %v", err)
	}
	msg = ran.done(boom).(doneMsg)
	if msg.out.Err != nil {
		t.Errorf("a completed order was turned into a refusal: %v", msg.out.Err)
	}
	if len(msg.out.Notices) != 1 || !strings.Contains(msg.out.Notices[0], "release the terminal") {
		t.Errorf("the handover's own trouble was swallowed: %+v", msg.out.Notices)
	}
}

// An order the roster hands the terminal to must not become the interrupt the
// roster refuses to offer: on the alternate screen ^C is a key press the working
// screen ignores, and handing the terminal back restores the line discipline
// that turns it into a signal. A ^C at a child's prompt has to reach the child
// and leave barracks to finish - or roll back - what it started.
func TestAnInterruptDoesNotKillWorkUnderway(t *testing.T) {
	self, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Skipf("cannot signal this process: %v", err)
	}
	caught, release := holdInterrupts()
	defer release()

	// If this hold is ever lost, the signal below kills the test binary. That
	// is the failure, stated as loudly as it deserves: the same ^C would have
	// killed barracks halfway through writing somebody's skills.
	if err := self.Signal(os.Interrupt); err != nil {
		t.Skipf("this platform cannot raise an interrupt: %v", err)
	}
	select {
	case <-caught:
	case <-time.After(10 * time.Second):
		t.Fatal("the interrupt was never delivered")
	}
}

// A unit can be committed here and spawned here at once - a garrison and a
// spawn legitimately share a directory while owning different skills - and the
// posture column has to say both rather than picking one.
func TestPostureCoversEveryPlaceAUnitCanBeStanding(t *testing.T) {
	both := unitLoadout("frontline", "a")
	afield := unitLoadout("scouts", "b")
	st := gather(fakeRecords{
		root:      "/repo",
		loadouts:  []*loadout.Loadout{both, afield},
		garrisons: []garrison.Garrison{{Loadout: "frontline", Targets: []string{"claude"}}},
		leases: []*lease.Lease{
			spawnedLease("frontline", "/repo", "/repo/.claude/skills", 2),
			spawnedLease("scouts", "/elsewhere", "/elsewhere/.claude/skills", 1),
		},
	})
	byName := map[string]unit{}
	for _, u := range st.Units {
		byName[u.Loadout.Name] = u
	}
	if got := byName["frontline"].Status(); got != "held+out" {
		t.Errorf("a unit both committed and spawned here reads %q", got)
	}
	if got := byName["scouts"].Status(); got != "afield" {
		t.Errorf("a unit spawned only elsewhere reads %q", got)
	}

	frame := plain(Frame(cfgFor(fakeRecords{
		root:     "/repo",
		loadouts: []*loadout.Loadout{afield},
		leases:   []*lease.Lease{spawnedLease("scouts", "/elsewhere", "/elsewhere/.claude/skills", 1)},
	}), 110, 30))
	if !strings.Contains(frame, "○ afield") {
		t.Errorf("the afield badge never reached the screen:\n%s", frame)
	}
	if !strings.Contains(frame, "1 spawn elsewhere") {
		t.Errorf("the dossier did not count the spawn in another repository:\n%s", frame)
	}
}

// A loadout that declares its targets is deployed to those and not to whatever
// this repository happens to show, so the dossier has to name them rather than
// repeating the detection wording.
func TestDeclaredTargetsAreNamedRatherThanDetected(t *testing.T) {
	l := unitLoadout("frontline", "a")
	l.Targets = []string{"claude", "cursor"}
	got := plain(Frame(cfgFor(fakeRecords{root: "/repo", loadouts: []*loadout.Loadout{l}}), 110, 30))
	if !strings.Contains(got, "claude, cursor") {
		t.Errorf("the declared targets are missing:\n%s", got)
	}
	if strings.Contains(got, "detected per repository") {
		t.Errorf("a loadout with declared targets still claimed detection:\n%s", got)
	}
}

// A global spawn's directory is not under the repository the roster is standing
// in, and shortening it against that root would produce a path made of "..".
func TestADirectoryOutsideTheRepositoryIsShownWhole(t *testing.T) {
	got := plain(Frame(cfgFor(fakeRecords{
		root:     "/repo",
		loadouts: []*loadout.Loadout{unitLoadout("frontline", "a")},
		leases:   []*lease.Lease{spawnedLease("frontline", "/repo", "/somewhere/else/skills", 1)},
	}), 130, 30))
	if !strings.Contains(got, "/somewhere/else/skills") {
		t.Errorf("a directory outside the repository was mangled:\n%s", got)
	}
	if strings.Contains(got, "..") {
		t.Errorf("a path outside the repository was made relative to it:\n%s", got)
	}
}

// An empty roster has nothing to move the cursor onto, and every key that would
// move it has to be a no-op rather than an index out of range.
func TestMovingOnAnEmptyRosterIsHarmless(t *testing.T) {
	got := plain(Frame(cfgFor(fakeRecords{root: "/repo"}), 100, 24, "j", "k", "down", "up"))
	if !strings.Contains(got, "no units trained") {
		t.Errorf("an empty roster did not survive the cursor keys:\n%s", got)
	}
}

// A frame drawn before the terminal has said how big it is has nothing to draw
// into. It must come back empty rather than guessing a size.
func TestNothingIsDrawnBeforeTheSizeIsKnown(t *testing.T) {
	m := newModel(cfgFor(fakeRecords{root: "/repo", loadouts: []*loadout.Loadout{unitLoadout("alpha")}}))
	m.layout() // must not panic before a size has arrived
	if got := m.View().Content; got != "" {
		t.Errorf("the roster drew something before it knew its size: %q", got)
	}
}

func TestOrderVerbs(t *testing.T) {
	for o, want := range map[order]string{orderDeploy: "Deploy", orderRecall: "Recall", orderNone: ""} {
		if got := o.verb(); got != want {
			t.Errorf("order(%d).verb() = %q, want %q", o, got, want)
		}
	}
}

func TestTextHelpers(t *testing.T) {
	if got := truncate("siege-engineering", 8); got != "siege-e…" {
		t.Errorf("truncate = %q", got)
	}
	if got := truncate("short", 40); got != "short" {
		t.Errorf("truncate widened a short string: %q", got)
	}
	if got := pad("ab", 5); got != "ab   " {
		t.Errorf("pad = %q", got)
	}
	if got := pad("abcdef", 0); got != "" {
		t.Errorf("pad with no room = %q", got)
	}
	// One column has no room for both a character and the ellipsis that would
	// say something was cut, so it carries the character.
	if got := truncate("siege", 1); got != "s" {
		t.Errorf("truncate to one column = %q", got)
	}
	if got := truncate("siege", 0); got != "" {
		t.Errorf("truncate to no columns = %q", got)
	}
	if got := plural(2, "spawn", "spawns"); got != "spawns" {
		t.Errorf("plural(2) = %q", got)
	}
	if got := plural(1, "spawn", "spawns"); got != "spawn" {
		t.Errorf("plural(1) = %q", got)
	}
	// A path under the user's home shortens the way a prompt does; anything
	// else is left exactly as the record holds it.
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		if got := abbreviate(filepath.Join(home, "app")); got != filepath.Join("~", "app") {
			t.Errorf("abbreviate = %q", got)
		}
	}
	if got := abbreviate("/opt/app"); got != "/opt/app" {
		t.Errorf("abbreviate rewrote a path outside home: %q", got)
	}
	if got := shortCommit("0123456789abcdef"); got != "0123456" {
		t.Errorf("shortCommit = %q", got)
	}
	if got := shortCommit(""); got != "" {
		t.Errorf("shortCommit invented a commit: %q", got)
	}
	// The hard break is the point: a barracks error names a path, and a path is
	// one word.
	long := wrap("refused: /a/very/long/path/that/will/never/fit/in/the/modal/at/all", 20)
	for _, line := range strings.Split(long, "\n") {
		if len([]rune(line)) > 20 {
			t.Errorf("wrap left a %d column line: %q", len([]rune(line)), line)
		}
	}
	if !strings.Contains(wrap("a b c", 20), "a b c") {
		t.Error("wrap broke a line that fits")
	}
}

func TestKeyPressNames(t *testing.T) {
	for name, want := range map[string]rune{"enter": tea.KeyEnter, "esc": tea.KeyEscape, "up": tea.KeyUp, "down": tea.KeyDown, "j": 'j'} {
		if got := keyPress(name); got.Code != want {
			t.Errorf("keyPress(%q).Code = %v, want %v", name, got.Code, want)
		}
	}
	if got := keyPress("R"); got.Code != 'r' || got.ShiftedCode != 'R' {
		t.Errorf("keyPress(\"R\") = %+v", got)
	}
}
