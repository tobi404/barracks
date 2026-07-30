package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
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

// orders is the registry and the agents a machine would offer, so a card that
// asks the user to choose has something to choose from. It is deliberately not
// part of cfgFor: a roster given no menu draws no picker, which is what every
// test written before the picker existed is still asserting.
func withMenus(cfg Config) Config {
	cfg.Targets = []TargetOption{
		{ID: "claude", Display: "Claude Code", Present: true},
		{ID: "agents", Display: "AGENTS.md agents (Codex, opencode, Cursor)"},
		{ID: "cursor", Display: "Cursor"},
		{ID: "opencode", Display: "OpenCode"},
		{ID: "windsurf", Display: "Windsurf"},
	}
	cfg.Selection = func(*loadout.Loadout) ([]string, string, error) {
		return []string{"claude"}, "detected in this repository", nil
	}
	cfg.Launchers = []Launcher{
		{Command: "claude", Display: "Claude Code"},
		{Command: "cursor-agent", Display: "Cursor"},
	}
	return cfg
}

// withActions wires every order to a stand-in, so a screen that depends on one
// existing can be driven. internal/cli's tests drive the same screens through
// the real engines.
func withActions(cfg Config) Config {
	cfg = withMenus(cfg)
	cfg.Deploy = (&deployTracker{}).deploy
	cfg.Recall = func(context.Context, *loadout.Loadout) Outcome { return Outcome{Title: "recalled"} }
	cfg.Garrison = func(_ context.Context, l *loadout.Loadout, _ Session) Outcome {
		return Outcome{Title: l.Name + " garrisoned"}
	}
	cfg.Upgrade = func(_ context.Context, l *loadout.Loadout, _ Session) Preview {
		return Preview{
			Outcome: Outcome{Title: l.Name + " upgrade plan", Lines: []string{"nothing moved"}},
			Apply:   func(context.Context, Session) Outcome { return Outcome{Title: l.Name + " upgraded"} },
		}
	}
	cfg.Launch = func(_ context.Context, l *loadout.Loadout, p Launcher, _ Session) Outcome {
		return Outcome{Title: l.Name + " session ended", Lines: []string{"ran " + p.Command}}
	}
	return cfg
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
	cfg := withActions(cfgFor(r))

	for _, size := range [][2]int{{80, 24}, {60, 16}, {140, 40}} {
		w, h := size[0], size[1]
		for _, script := range everyOverlay {
			got := plain(Frame(cfg, w, h, script...))
			fits(t, got, w, h, fmt.Sprintf("%dx%d overlay %v", w, h, script))
		}
	}
}

// everyOverlay is a script per card the roster can put in front of the roster.
// A card is the easiest way to grow the frame past the terminal, so a new one
// belongs here rather than in whichever test happened to notice it.
var everyOverlay = [][]string{
	{"?"},                   // the orders overlay
	{"s"},                   // the deploy order, with its target picker
	{"s", "space", "space"}, // the same picker after it has been used
	{"r"},                   // the recall order
	{"g"},                   // the garrison order
	{"L"},                   // the launch order, with its agent picker
	{"s", "y"},              // work underway
	{"u", "@pump"},          // an upgrade's plan, waiting to be confirmed
	{"u", "@pump", "y"},     // that plan being carried out
	{"g", "y", "@pump"},     // an outcome
	{"u", "@pump", "y", "@pump"},
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
	cfg := withActions(cfgFor(r))
	cfg.Deploy = (&deployTracker{outcome: Outcome{
		Err:     errors.New("spawn into Claude Code: " + long + " is committed to this repository"),
		Notices: []string{"left in place (barracks did not create it): /repo/lab/" + long},
	}}).deploy

	for _, size := range [][2]int{{80, 24}, {100, 30}, {140, 40}, {60, 16}} {
		w, h := size[0], size[1]
		for _, script := range append(everyOverlay, []string{"s", "y", "@pump"}) {
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
//
// The keys this drives are derived rather than written down, because a list
// somebody types out only ever covers the keys somebody already thought of.
// Letters bound nowhere are the easy half; the half that actually bit is the
// viewport behind the dossier, which carries a keymap of its own that the
// roster advertises nothing about. Taking that half from viewport.DefaultKeyMap
// by reflection means a key bubbles binds in a future version is covered the
// day it lands, not the day somebody notices the pane looking broken.
func TestUnboundKeysDoNothingAndClaimNothing(t *testing.T) {
	alpha := unitLoadout("alpha", "a")
	// A dossier with a line wider than its pane, so a horizontal scroll would
	// show. The precondition below is what holds that true.
	alpha.Description = "the loadout every clone of this repository is expected to be carrying"
	r := fakeRecords{root: "/repo", loadouts: []*loadout.Loadout{alpha, unitLoadout("bravo", "b")}}
	cfg := withActions(cfgFor(r))
	const w, h = 80, 24

	// This fixture has to be able to show both failures, or the loop below is
	// just pressing keys. A dossier that fits its pane cannot be scrolled at
	// all, and one that overflows it would be scrolled *legitimately* by the
	// paging keys - which is behaviour the roster wants and this test must not
	// be reading as a regression.
	probe := newModel(cfg)
	probe.Update(tea.WindowSizeMsg{Width: w, Height: h})
	probe.vp.SetXOffset(1)
	if probe.vp.XOffset() == 0 {
		t.Fatal("no dossier line is wider than its pane, so nothing here could scroll sideways")
	}
	if n, height := probe.vp.TotalLineCount(), probe.vp.Height(); n > height {
		t.Fatalf("the dossier is %d lines in a %d-line pane, so the paging keys would scroll it for good reason", n, height)
	}

	bound := map[string]bool{}
	k := defaultKeys()
	for _, b := range append(k.ShortHelp(), flatten(k.FullHelp())...) {
		for _, name := range b.Keys() {
			bound[name] = true
		}
	}
	keys := []string{"t", "e", "x", "z", "w", "v"}
	for _, name := range widgetKeys(viewport.DefaultKeyMap()) {
		if !bound[name] {
			keys = append(keys, name)
		}
	}
	if len(keys) <= 6 {
		t.Fatal("the viewport's keymap yielded no keys of its own, so its half of this was not driven")
	}

	idle := plain(Frame(cfg, w, h))
	for _, name := range keys {
		got := plain(Frame(cfg, w, h, name))
		if got != idle {
			t.Errorf("%q changed the screen even though nothing is bound to it:\n%s", name, got)
		}
		if strings.Contains(got, "not wired") {
			t.Errorf("%q advertises itself as unwired:\n%s", name, got)
		}
	}
}

// The test above names its keys rather than typing them, so the harness has to
// refuse a name it cannot press instead of sending that name's first letter -
// which for "right" is `r`, the recall key, and would have turned a test about
// a key doing nothing into a test of a key doing something else entirely.
func TestTheHarnessRefusesAKeyItCannotPress(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a key name the harness does not know was pressed as something else")
		}
	}()
	keyPress("f10")
}

// The other half of the same rule, and the half the test above cannot see. Its
// fixture is deliberately short, so a change that stopped the viewport from
// scrolling *at all* - swallowing its keys, or dropping the widget - would go on
// passing while a dossier longer than its pane quietly lost everything below the
// fold. Only the horizontal axis had no business being reachable; the vertical
// one is what the pane is for.
//
// The key is taken from the viewport's own keymap rather than written down, for
// the same reason the test above derives its list: this must follow the widget
// if bubbles ever respells it.
func TestALongDossierStillScrollsDownButNeverSideways(t *testing.T) {
	alpha := unitLoadout("alpha", "a")
	alpha.Description = "the loadout every clone of this repository is expected to be carrying"
	for i := range 24 {
		alpha.Equipment[0].Skills = append(alpha.Equipment[0].Skills, fmt.Sprintf("skill-%02d", i))
	}
	cfg := withActions(cfgFor(fakeRecords{root: "/repo", loadouts: []*loadout.Loadout{alpha}}))
	const w, h = 80, 24

	m := newModel(cfg)
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	if n, height := m.vp.TotalLineCount(), m.vp.Height(); n <= height {
		t.Fatalf("the dossier is %d lines in a %d-line pane, so there is nothing below the fold to scroll to", n, height)
	}
	if strings.Contains(plain(m.View().Content), "skill-23") {
		t.Fatal("the whole dossier is already on screen, so paging it proves nothing")
	}

	down := viewport.DefaultKeyMap().PageDown.Keys()
	if len(down) == 0 {
		t.Fatal("the viewport advertises no way to page down, so nothing here was driven")
	}
	// Enough pages to reach the foot of this fixture, so the assertion is about
	// the last line of the dossier rather than about how tall a page happens to be.
	script := []string{down[0], down[0], down[0], down[0]}
	if paged := plain(Frame(cfg, w, h, script...)); !strings.Contains(paged, "skill-23") {
		t.Errorf("%q did not scroll the dossier down to what was below the fold:\n%s", down[0], paged)
	}

	// And the axis that was turned off stays off, on a dossier long enough that
	// the widget has every reason to move.
	for _, name := range viewport.DefaultKeyMap().Right.Keys() {
		if got := plain(Frame(cfg, w, h, name)); got != plain(Frame(cfg, w, h)) {
			t.Errorf("%q scrolled the dossier sideways:\n%s", name, got)
		}
	}
}

// widgetKeys reads every key.Binding field off a widget's keymap. It is
// reflective on purpose: a keymap the roster does not own can grow a field, and
// a hand-written list of fields would go on passing without it.
func widgetKeys(keymap any) []string {
	var names []string
	v := reflect.ValueOf(keymap)
	for i := 0; i < v.NumField(); i++ {
		b, ok := v.Field(i).Interface().(key.Binding)
		if !ok {
			continue
		}
		names = append(names, b.Keys()...)
	}
	return names
}

func flatten(rows [][]key.Binding) []key.Binding {
	var out []key.Binding
	for _, row := range rows {
		out = append(out, row...)
	}
	return out
}

// Every key the roster advertises has to reach a branch that does something.
// The help is the contract - a footer promising a key barracks ignores is the
// failure this guards, and it is the reason the roster grew no key for a verb
// it could not carry out.
//
// Every binding is driven, not only the footer's: the orders overlay is where a
// narrow terminal sends the user to find the keys the footer had to elide, so a
// key that only appears there is advertised just as loudly.
func TestEveryAdvertisedKeyIsHandled(t *testing.T) {
	r := fakeRecords{
		root:     "/repo",
		loadouts: []*loadout.Loadout{unitLoadout("alpha", "a"), unitLoadout("bravo", "b")},
		leases:   []*lease.Lease{spawnedLease("alpha", "/repo", "/repo/.claude/skills", 1)},
	}
	cfg := withActions(cfgFor(r))

	k := defaultKeys()
	// Confirm, Cancel and the picker's own key answer a card, so they are
	// driven from one - the deploy order, which is the card that has a picker.
	prefix := map[string][]string{"y": {"s"}, "enter": {"s"}, "n": {"s"}, "esc": {"s"}, "space": {"s"}}
	seen := map[string]bool{}
	bindings := append([]key.Binding{}, k.ShortHelp()...)
	for _, row := range k.FullHelp() {
		bindings = append(bindings, row...)
	}
	for _, b := range bindings {
		for _, name := range b.Keys() {
			if name == "q" || name == "ctrl+c" {
				// Quitting leaves the frame it was drawn on, so it is not
				// visible here. TestQuitReturnsTheQuitCommand drives it.
				continue
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			script := append(append([]string{}, prefix[name]...), name)
			before := plain(Frame(cfg, 100, 24, prefix[name]...))
			after := plain(Frame(cfg, 100, 24, script...))
			if before == after {
				t.Errorf("%q is advertised in the help but changed nothing (script %v)", name, script)
			}
		}
	}
	// The list above is only worth anything if it really covered the new
	// verbs; a binding dropped from both help views would otherwise be
	// "checked" by never being reached.
	for _, name := range []string{"s", "r", "g", "u", "L", "space"} {
		if !seen[name] {
			t.Errorf("%q is bound but appears in neither help view, so nothing advertises it", name)
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
	calls int
	// targets is what the last call was handed. Nil and empty are different
	// answers here - nil is "the picker was not touched" - so the two are kept
	// apart rather than counted.
	targets []string
	// skills is the other band, and nil and empty are two different answers
	// here for the same reason: nil is the whole loadout, and a list is a
	// deliberately narrowed deployment.
	skills  []string
	got     bool
	report  []string
	outcome Outcome
}

func (d *deployTracker) deploy(_ context.Context, l *loadout.Loadout, targets, skills []string, s Session) Outcome {
	d.calls++
	d.targets, d.skills, d.got = targets, skills, true
	for _, line := range d.report {
		fmt.Fprintln(s.Out, line)
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

	never := &terminalJob{run: func(Session) Preview {
		return Preview{Outcome: Outcome{Title: "unreachable"}, Apply: func(context.Context, Session) Outcome { return Outcome{} }}
	}}
	msg := never.done(boom).(doneMsg)
	if msg.p.Err == nil || !strings.Contains(msg.p.Err.Error(), "release the terminal") {
		t.Errorf("an order that never ran was not reported as refused: %+v", msg.p.Outcome)
	}
	if msg.p.Title != "" {
		t.Errorf("an order that never ran reported a title: %q", msg.p.Title)
	}
	// Nothing may be offered to carry out on this path either: there is no plan
	// behind an order that never ran, so a plan card would be a card whose one
	// key applies nothing.
	if msg.p.Apply != nil {
		t.Error("an order that never ran still offered a plan to apply")
	}

	ran := &terminalJob{run: func(s Session) Preview {
		fmt.Fprintln(s.Out, "fetching…")
		return Preview{Outcome: Outcome{Title: "frontline deployed"}}
	}}
	ran.SetStdout(io.Discard)
	if err := ran.Run(); err != nil {
		t.Fatalf("a refusing order must not fail the handover: %v", err)
	}
	msg = ran.done(boom).(doneMsg)
	if msg.p.Err != nil {
		t.Errorf("a completed order was turned into a refusal: %v", msg.p.Err)
	}
	if len(msg.p.Notices) != 1 || !strings.Contains(msg.p.Notices[0], "release the terminal") {
		t.Errorf("the handover's own trouble was swallowed: %+v", msg.p.Notices)
	}
}

// An order the roster hands the terminal to has to be able to read it as well
// as write to it: a `run` order starts an agent, and an agent that cannot see
// the keyboard is not an agent. A job the handover gave no stream at all must
// still be safe to write to rather than panic on a nil writer.
func TestAHandoverCarriesEveryStreamAndSurvivesNone(t *testing.T) {
	var got Session
	j := &terminalJob{run: func(s Session) Preview {
		got = s
		fmt.Fprint(s.Out, "out")
		fmt.Fprint(s.Err, "err")
		body, _ := io.ReadAll(s.In)
		return Preview{Outcome: Outcome{Title: string(body)}}
	}}
	var out, errb bytes.Buffer
	j.SetStdin(strings.NewReader("typed"))
	j.SetStdout(&out)
	j.SetStderr(&errb)
	if err := j.Run(); err != nil {
		t.Fatalf("the job failed: %v", err)
	}
	if out.String() != "out" || errb.String() != "err" {
		t.Errorf("the order wrote to the wrong streams: out %q err %q", out.String(), errb.String())
	}
	if j.result.Title != "typed" {
		t.Errorf("the order could not read the terminal it was handed: %q", j.result.Title)
	}
	if got.In == nil || got.Out == nil || got.Err == nil {
		t.Errorf("a stream was dropped on the way in: %+v", got)
	}

	bare := &terminalJob{run: func(s Session) Preview {
		fmt.Fprint(s.Out, "nowhere")
		fmt.Fprint(s.Err, "nowhere")
		return Preview{}
	}}
	if err := bare.Run(); err != nil {
		t.Fatalf("a job with no streams failed: %v", err)
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
	for o, want := range map[order]string{
		orderDeploy:   "Deploy",
		orderRecall:   "Recall",
		orderGarrison: "Garrison",
		orderUpgrade:  "Upgrade",
		orderLaunch:   "Launch",
		orderNone:     "",
	} {
		if got := o.verb(); got != want {
			t.Errorf("order(%d).verb() = %q, want %q", o, got, want)
		}
	}
	// The in-flight card is on screen for as long as a fetch takes, so it says
	// which order is taking it. Every order has to have an answer, and no two
	// that do different things may share one.
	for o := orderDeploy; o <= orderLaunch; o++ {
		if o.working() == "" {
			t.Errorf("order(%d) has no in-flight headline", o)
		}
	}
	if orderGarrison.working() == orderDeploy.working() {
		t.Error("garrisoning and spawning show the same in-flight headline")
	}
	if orderUpgrade.working() == orderDeploy.working() {
		t.Error("upgrading and spawning show the same in-flight headline")
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

// The picker opens on exactly where a plain `barracks spawn` would have sent
// the loadout, and leaving it alone must reach the action as no choice at all.
//
// Those are one claim, not two: nil and "the same list, chosen by hand" are
// different instructions to the spawn engine. The first leaves the loadout's
// declaration and the repository's own evidence in charge, and the second
// overrides both - so a picker that reported its opening state as a choice
// would quietly pin every deploy to whatever was detected the day it ran.
func TestThePickerOpensOnWhatBarracksWouldChooseAndOverridesNothing(t *testing.T) {
	r := fakeRecords{root: "/repo/lab", loadouts: []*loadout.Loadout{unitLoadout("frontline", "a")}}
	d := &deployTracker{}
	cfg := withMenus(cfgFor(r))
	cfg.Deploy = d.deploy

	card := plain(Frame(cfg, 100, 30, "s"))
	if !strings.Contains(card, "[x] Claude Code") {
		t.Errorf("the picker did not open on the detected target:\n%s", card)
	}
	if !strings.Contains(card, "[ ] Cursor") {
		t.Errorf("the picker did not offer the targets that were not detected:\n%s", card)
	}
	if !strings.Contains(card, "present here") {
		t.Errorf("the picker does not say which agent this repository already shows:\n%s", card)
	}

	plain(Frame(cfg, 100, 30, "s", "y", "@pump"))
	if d.calls != 1 {
		t.Fatalf("the deploy ran %d times, want 1", d.calls)
	}
	if !d.got {
		t.Fatal("the deploy was never handed a target list")
	}
	if d.targets != nil {
		t.Errorf("an untouched picker overrode the selection with %v", d.targets)
	}
}

// Choosing is the whole point of the picker: what the user ticks is what the
// order is given, in the picker's own order.
func TestChoosingTargetsIsWhatTheDeployIsGiven(t *testing.T) {
	r := fakeRecords{root: "/repo/lab", loadouts: []*loadout.Loadout{unitLoadout("frontline", "a")}}
	d := &deployTracker{}
	cfg := withMenus(cfgFor(r))
	cfg.Deploy = d.deploy

	// The cursor opens on the one already chosen (claude); move down twice to
	// Cursor and take it as well.
	card := plain(Frame(cfg, 100, 30, "s", "j", "j", "space"))
	if !strings.Contains(card, "[x] Cursor") {
		t.Fatalf("space did not choose the target under the cursor:\n%s", card)
	}
	plain(Frame(cfg, 100, 30, "s", "j", "j", "space", "y", "@pump"))
	want := []string{"claude", "cursor"}
	if len(d.targets) != len(want) {
		t.Fatalf("the deploy was given %v, want %v", d.targets, want)
	}
	for i := range want {
		if d.targets[i] != want[i] {
			t.Fatalf("the deploy was given %v, want %v", d.targets, want)
		}
	}

	// And un-ticking the one it opened on leaves the deploy going somewhere
	// else entirely, rather than quietly adding to the detected set.
	plain(Frame(cfg, 100, 30, "s", "space", "j", "j", "space", "y", "@pump"))
	if len(d.targets) != 1 || d.targets[0] != "cursor" {
		t.Errorf("un-choosing the detected target left it in: %v", d.targets)
	}
}

// A deploy with nothing ticked cannot be carried out, and the refusal has to be
// on the card the user is looking at. The status line is behind that card and
// may be covered by it, so a refusal put there is a key that did nothing.
func TestADeployWithNothingChosenIsRefusedOnTheCard(t *testing.T) {
	r := fakeRecords{root: "/repo/lab", loadouts: []*loadout.Loadout{unitLoadout("frontline", "a")}}
	d := &deployTracker{}
	cfg := withMenus(cfgFor(r))
	cfg.Deploy = d.deploy

	got := plain(Frame(cfg, 100, 30, "s", "space", "y", "@pump"))
	if d.calls != 0 {
		t.Fatal("a deploy with no target chosen was carried out anyway")
	}
	if !strings.Contains(got, "Choose at least one") {
		t.Errorf("the refusal never reached the card:\n%s", got)
	}
	// And the card is still there to choose on, rather than having been
	// withdrawn out from under the user.
	if !strings.Contains(got, "DEPLOY ORDER") {
		t.Errorf("the order was withdrawn instead of refused:\n%s", got)
	}
}

// A choice you cannot see is a choice you cannot make, so the picker keeps the
// cursor on screen however short the terminal is, and says how much of the list
// it is showing when it cannot show all of it.
func TestThePickerStaysUsableOnASmallTerminal(t *testing.T) {
	r := fakeRecords{root: "/repo/lab", loadouts: []*loadout.Loadout{unitLoadout("frontline", "a")}}
	cfg := withMenus(cfgFor(r))
	cfg.Deploy = (&deployTracker{}).deploy

	for _, size := range [][2]int{{80, 24}, {70, 18}, {60, 14}, {80, 12}} {
		w, h := size[0], size[1]
		// Four presses of j from the first option lands on the last one.
		script := []string{"s", "j", "j", "j", "j"}
		got := plain(Frame(cfg, w, h, script...))
		fits(t, got, w, h, fmt.Sprintf("%dx%d picker", w, h))
		if !strings.Contains(got, "Windsurf") {
			t.Errorf("%dx%d: the option the cursor is on is not on screen:\n%s", w, h, got)
		}
		if !strings.Contains(got, "stand down") {
			t.Errorf("%dx%d: the card no longer says how to leave it:\n%s", w, h, got)
		}
		// Either every option is drawn, or the card says how many it is not
		// drawing. Silently showing three of six reads as a registry with
		// three agents in it. Six is the five agents plus the loadout's one
		// skill: both bands are one list, so the count covers both.
		if !strings.Contains(got, "OpenCode") && !strings.Contains(got, "of 6") {
			t.Errorf("%dx%d: the picker hid options without saying so:\n%s", w, h, got)
		}
	}
}

// The garrison order commits real files into somebody's checkout, so it goes
// through the same handover a deploy does - it fetches - and its report reaches
// the terminal it was handed rather than the screen the roster gave up.
func TestGarrisonRunsWithTheTerminalHandedBack(t *testing.T) {
	r := fakeRecords{root: "/repo/lab", loadouts: []*loadout.Loadout{unitLoadout("frontline", "a")}}
	calls := 0
	cfg := withActions(cfgFor(r))
	cfg.Garrison = func(_ context.Context, l *loadout.Loadout, s Session) Outcome {
		calls++
		fmt.Fprintln(s.Out, "github.com/unit/frontline  fetching…")
		return Outcome{Title: l.Name + " garrisoned", Lines: []string{"+ .claude/skills/react/SKILL.md"}}
	}

	card := plain(Frame(cfg, 100, 30, "g"))
	if !strings.Contains(card, "GARRISON ORDER") || !strings.Contains(card, "Commit frontline into lab?") {
		t.Fatalf("g did not raise a garrison order:\n%s", card)
	}
	if !strings.Contains(card, "barracks.lock") {
		t.Errorf("the card does not say the committed tier writes a lockfile:\n%s", card)
	}
	if calls != 0 {
		t.Fatal("the garrison was carried out before it was confirmed")
	}

	frame, released := FrameAndTerminal(cfg, 100, 30, "g", "y", "@work")
	if !strings.Contains(plain(frame), "DIGGING IN") {
		t.Errorf("no in-flight screen for the garrison:\n%s", plain(frame))
	}
	if !strings.Contains(released, "fetching") {
		t.Errorf("the garrison's progress never reached the terminal:\n%s", released)
	}
	if strings.Contains(plain(frame), "fetching") {
		t.Errorf("the garrison drew on the screen it had handed back:\n%s", plain(frame))
	}

	done := plain(Frame(cfg, 100, 30, "g", "y", "@pump"))
	if !strings.Contains(done, "FRONTLINE GARRISONED") || !strings.Contains(done, "SKILL.md") {
		t.Errorf("the garrison outcome never reached the screen:\n%s", done)
	}
}

// The upgrade order shows the plan and then carries out that same plan.
//
// Both halves are the claim. Nothing may be applied before the user has read
// the plan and said so - that is the whole reason the roster asks the action
// for a plan rather than a result - and standing the plan down must leave
// nothing applied at all.
func TestUpgradeShowsItsPlanBeforeCarryingItOut(t *testing.T) {
	r := fakeRecords{root: "/repo/lab", loadouts: []*loadout.Loadout{unitLoadout("frontline", "a")}}
	planned, applied := 0, 0
	cfg := withActions(cfgFor(r))
	cfg.Upgrade = func(_ context.Context, l *loadout.Loadout, s Session) Preview {
		planned++
		fmt.Fprintln(s.Out, "github.com/unit/frontline  resolving…")
		return Preview{
			Outcome: Outcome{
				Title: l.Name + " upgrade plan",
				Lines: []string{"github.com/unit/frontline  0123456 -> 89abcde", "  ~ react-forms"},
			},
			Apply: func(context.Context, Session) Outcome {
				applied++
				return Outcome{Title: l.Name + " upgraded", Lines: []string{"  ~ react-forms"}}
			},
		}
	}

	plan := plain(Frame(cfg, 100, 30, "u", "@pump"))
	if planned != 1 {
		t.Fatalf("u planned %d times, want 1", planned)
	}
	if applied != 0 {
		t.Fatal("the upgrade was applied before the plan was shown")
	}
	for _, want := range []string{"FRONTLINE UPGRADE PLAN", "0123456 -> 89abcde", "react-forms", "y carry it out", "n stand down"} {
		if !strings.Contains(plan, want) {
			t.Errorf("the plan card is missing %q:\n%s", want, plan)
		}
	}

	// Stood down: read and not carried out.
	stood := plain(Frame(cfg, 100, 30, "u", "@pump", "n"))
	if applied != 0 {
		t.Fatal("standing the plan down applied it anyway")
	}
	if !strings.Contains(stood, "nothing applied") {
		t.Errorf("standing the plan down said nothing:\n%s", stood)
	}
	if strings.Contains(stood, "y carry it out") {
		t.Errorf("the plan card would not close:\n%s", stood)
	}

	done := plain(Frame(cfg, 100, 30, "u", "@pump", "y", "@pump"))
	if applied != 1 {
		t.Fatalf("confirming the plan applied it %d times, want 1", applied)
	}
	if !strings.Contains(done, "FRONTLINE UPGRADED") {
		t.Errorf("the upgrade outcome never reached the screen:\n%s", done)
	}
}

// An upgrade that has nothing to offer is a report, not an order: the roster
// must not put a card in front of the user whose one key applies nothing.
func TestAnUpgradeWithNothingToApplyIsShownAsAnOutcome(t *testing.T) {
	r := fakeRecords{root: "/repo/lab", loadouts: []*loadout.Loadout{unitLoadout("frontline", "a")}}
	cfg := withActions(cfgFor(r))
	cfg.Upgrade = func(_ context.Context, l *loadout.Loadout, _ Session) Preview {
		return Preview{Outcome: Outcome{Err: errors.New("github.com/unit/frontline: could not resolve main")}}
	}
	got := plain(Frame(cfg, 100, 30, "u", "@pump"))
	if !strings.Contains(got, "REFUSED") || !strings.Contains(got, "could not resolve") {
		t.Errorf("a refused upgrade was not reported:\n%s", got)
	}
	if strings.Contains(got, "carry it out") {
		t.Errorf("a refused upgrade still offered to be applied:\n%s", got)
	}
	if !strings.Contains(got, "any key to return to the roster") {
		t.Errorf("the refusal card does not say how to leave it:\n%s", got)
	}
}

// An upgrade needs no repository: it re-resolves sources and relinks spawns
// wherever they are. The three orders that put files somewhere do need one, and
// refusing all four the same way would take the roster's only machine-wide verb
// away from anyone not standing in a checkout.
func TestUpgradeIsTheOneOrderThatNeedsNoRepository(t *testing.T) {
	r := fakeRecords{loadouts: []*loadout.Loadout{unitLoadout("frontline", "a")}}
	planned := 0
	cfg := withActions(cfgFor(r))
	cfg.Upgrade = func(_ context.Context, l *loadout.Loadout, _ Session) Preview {
		planned++
		return Preview{Outcome: Outcome{Title: "frontline upgrade plan"}}
	}
	if got := plain(Frame(cfg, 100, 30, "u", "@pump")); !strings.Contains(got, "FRONTLINE UPGRADE PLAN") {
		t.Errorf("an upgrade outside a repository was refused:\n%s", got)
	}
	if planned != 1 {
		t.Fatalf("the upgrade ran %d times outside a repository, want 1", planned)
	}
	for _, k := range []string{"s", "g", "L"} {
		got := plain(Frame(cfg, 100, 30, k))
		if !strings.Contains(got, "nowhere here to deploy") {
			t.Errorf("%q outside a repository did not say why it cannot run:\n%s", k, got)
		}
	}
}

// A run starts exactly one program, so its picker chooses one and only one -
// and what it chose is what is started.
func TestLaunchStartsTheOneAgentThatWasChosen(t *testing.T) {
	r := fakeRecords{root: "/repo/lab", loadouts: []*loadout.Loadout{unitLoadout("frontline", "a")}}
	var started []string
	cfg := withActions(cfgFor(r))
	cfg.Launch = func(_ context.Context, l *loadout.Loadout, p Launcher, s Session) Outcome {
		started = append(started, p.Command)
		fmt.Fprintln(s.Out, "spawned "+l.Name)
		return Outcome{Title: l.Name + " session ended"}
	}

	card := plain(Frame(cfg, 100, 30, "L"))
	if !strings.Contains(card, "LAUNCH ORDER") || !strings.Contains(card, "[x] Claude Code") {
		t.Fatalf("L did not raise a launch order on the first agent:\n%s", card)
	}

	// Moving to the second agent and choosing it must move the choice rather
	// than add to it: two agents cannot be started by one run.
	chosen := plain(Frame(cfg, 100, 30, "L", "j", "space"))
	if !strings.Contains(chosen, "[x] Cursor") || strings.Contains(chosen, "[x] Claude Code") {
		t.Fatalf("the launch picker took two agents at once:\n%s", chosen)
	}

	frame, released := FrameAndTerminal(cfg, 100, 30, "L", "j", "space", "y", "@pump")
	if len(started) != 1 || started[0] != "cursor-agent" {
		t.Fatalf("the launch started %v, want [cursor-agent]", started)
	}
	if !strings.Contains(released, "spawned frontline") {
		t.Errorf("the session's own output never reached the terminal:\n%s", released)
	}
	if !strings.Contains(plain(frame), "FRONTLINE SESSION ENDED") {
		t.Errorf("the session outcome never reached the screen:\n%s", plain(frame))
	}
}

// A machine with no agent barracks knows on its PATH has nothing to launch, and
// the order says so rather than offering an empty list to choose from.
func TestLaunchSaysSoWhenThereIsNothingToStart(t *testing.T) {
	r := fakeRecords{root: "/repo/lab", loadouts: []*loadout.Loadout{unitLoadout("frontline", "a")}}
	cfg := withActions(cfgFor(r))
	cfg.Launchers = nil
	got := plain(Frame(cfg, 100, 30, "L"))
	if !strings.Contains(got, "No agent barracks knows") {
		t.Errorf("a launch with nothing to launch said nothing:\n%s", got)
	}
	if strings.Contains(got, "LAUNCH ORDER") {
		t.Errorf("a launch with nothing to launch still raised an order:\n%s", got)
	}
}

// Recall from the roster covers the personal tier only, and the card says so.
//
// The garrison order sits one key away from it, so a recall card that said
// nothing would read as "this removes the loadout" - which for a unit that is
// both committed and spawned here would be wrong in the direction that costs
// somebody an afternoon looking for files that never went anywhere.
func TestTheRecallCardSaysItLeavesTheCommittedTierAlone(t *testing.T) {
	r := fakeRecords{
		root:      "/repo/lab",
		loadouts:  []*loadout.Loadout{unitLoadout("frontline", "a")},
		leases:    []*lease.Lease{spawnedLease("frontline", "/repo/lab", "/repo/lab/.claude/skills", 1)},
		garrisons: []garrison.Garrison{{Loadout: "frontline", ID: "id-frontline", Targets: []string{"claude"}}},
	}
	got := plain(Frame(withActions(cfgFor(r)), 100, 30, "r"))
	if !strings.Contains(got, "RECALL ORDER") {
		t.Fatalf("r did not raise a recall order:\n%s", got)
	}
	if !strings.Contains(got, "Spawns only") {
		t.Errorf("the recall card does not say the garrison stays:\n%s", got)
	}
	if !strings.Contains(got, "barracks recall frontline") {
		t.Errorf("the recall card does not name what removes the garrison:\n%s", got)
	}
}

// A loadout barracks cannot work out a destination for is a broken definition,
// and `barracks spawn` refuses it by name. The roster has to refuse the same
// loadout in the same words rather than open a picker with nothing ticked:
// "nothing is selected" is what a user answers by ticking a box, and the moment
// they do, the declaration barracks could not read has been replaced by an
// explicit override and the deploy quietly succeeds.
func TestADeployRefusesADefinitionBarracksCannotRead(t *testing.T) {
	r := fakeRecords{root: "/repo/lab", loadouts: []*loadout.Loadout{unitLoadout("frontline", "a")}}
	d := &deployTracker{}
	cfg := withMenus(cfgFor(r))
	cfg.Deploy = d.deploy
	cfg.Selection = func(*loadout.Loadout) ([]string, string, error) {
		return nil, "", errors.New("loadout declares a target barracks does not know: retired")
	}

	got := plain(Frame(cfg, 100, 30, "s"))
	if !strings.Contains(got, "REFUSED") || !strings.Contains(got, "does not know") {
		t.Fatalf("the roster hid the definition error instead of refusing on it:\n%s", got)
	}
	if strings.Contains(got, "DEPLOY ORDER") {
		t.Errorf("a definition barracks cannot read still raised a deploy order:\n%s", got)
	}
	if strings.Contains(got, "TARGETS") {
		t.Errorf("the refusal still opened a picker to tick a box on:\n%s", got)
	}

	// And pressing on cannot turn it into a deploy: the card is a report, so
	// the next key closes it and lands back on the roster.
	after := plain(Frame(cfg, 100, 30, "s", "space", "y", "@pump"))
	if d.calls != 0 {
		t.Fatalf("the deploy ran %d times after a refusal, want 0", d.calls)
	}
	if strings.Contains(after, "any key to return to the roster") {
		t.Errorf("the refusal card would not close:\n%s", after)
	}
}

// The program a launch starts is the one whose key the user chose, never the
// one sitting at the same position in the menu.
//
// The launch picker is built one row per launcher, so the two agree by
// coincidence today - which is exactly why a positional lookup survives every
// test that drives the real card, and why this one hands the model a picker in
// a different order, as any filtering of that menu would.
func TestTheLaunchStartsTheAgentThatWasChosenNotTheRowItSatOn(t *testing.T) {
	m := newModel(withActions(cfgFor(fakeRecords{root: "/repo/lab"})))
	m.pick = newPicker(band{ID: groupAgent, Title: "AGENT", Noun: "agent", Chosen: []string{"claude"}, Options: []choice{
		{Key: "cursor-agent", Label: "Cursor"},
		{Key: "claude", Label: "Claude Code"},
	}})

	if got := m.launcher(); got.Command != "claude" {
		t.Errorf("the launch would start %q, want claude", got.Command)
	}

	// A key no launcher answers to starts nothing rather than the first thing
	// on the list.
	m.pick = newPicker(band{ID: groupAgent, Title: "AGENT", Noun: "agent",
		Options: []choice{{Key: "windsurf", Label: "Windsurf"}}, Chosen: []string{"windsurf"}})
	if got := m.launcher(); got.Command != "" {
		t.Errorf("a choice no launcher matches started %q", got.Command)
	}

	// And a launch never reads the deploy card's bands. A skill called `claude`
	// is a directory somebody may legitimately have, and a positional or
	// namespace-blind lookup would start an agent because a skill was ticked.
	m.pick = newPicker(
		band{ID: groupTargets, Title: "TARGETS", Noun: "target", Multi: true,
			Options: []choice{{Key: "claude", Label: "Claude Code"}}, Chosen: []string{"claude"}},
		band{ID: groupSkills, Title: "SKILLS", Noun: "skill", Multi: true,
			Options: []choice{{Key: "cursor-agent", Label: "cursor-agent"}}, Chosen: []string{"cursor-agent"}},
	)
	if got := m.launcher(); got.Command != "" {
		t.Errorf("a launch read the deploy card's bands and would start %q", got.Command)
	}
}

// A picker is a list with a cursor, and the two are the whole of it: the keys
// wrap, a single-choice band can never be emptied, and nothing at all is
// reported until the user has actually chosen.
func TestPickerKeeps(t *testing.T) {
	options := []choice{{Key: "a", Label: "A"}, {Key: "b", Label: "B"}, {Key: "c", Label: "C"}}
	one := func(chosen []string, multi bool) picker {
		return newPicker(band{ID: groupTargets, Title: "TARGETS", Noun: "target",
			Multi: multi, Options: options, Chosen: chosen})
	}

	multi := one([]string{"b"}, true)
	if multi.cursor != 1 {
		t.Errorf("the picker did not open on the chosen option: cursor %d", multi.cursor)
	}
	if multi.chosen(groupTargets) != nil {
		t.Errorf("an untouched picker reported a choice: %v", multi.chosen(groupTargets))
	}
	if got := multi.keys(groupTargets); len(got) != 1 || got[0] != "b" {
		t.Errorf("the picker did not open on b: %v", got)
	}
	multi.move(-1)
	multi.toggle()
	if got := multi.chosen(groupTargets); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("chosen keys are not in the picker's own order: %v", got)
	}
	multi.move(-1) // wraps to the end
	if multi.cursor != 2 {
		t.Errorf("the cursor did not wrap off the top: %d", multi.cursor)
	}
	multi.move(1)
	if multi.cursor != 0 {
		t.Errorf("the cursor did not wrap off the bottom: %d", multi.cursor)
	}
	multi.toggle() // a off again
	multi.move(1)
	multi.toggle() // b off
	if _, empty := multi.emptyBand(); !empty {
		t.Errorf("un-choosing everything did not leave the band empty: %v", multi.keys(groupTargets))
	}

	single := one([]string{"a"}, false)
	single.move(2)
	single.toggle()
	if got := single.keys(groupTargets); len(got) != 1 || got[0] != "c" {
		t.Errorf("a single-choice band took more than one: %v", got)
	}
	single.toggle()
	if _, empty := single.emptyBand(); empty {
		t.Error("a single-choice band emptied itself")
	}

	// An empty picker has no cursor to move and nothing to toggle.
	var none picker
	none.move(1)
	none.toggle()
	if _, empty := none.emptyBand(); empty || none.chosen(groupTargets) != nil {
		t.Error("an empty picker invented a choice")
	}
}

// A picker's bands are independent, and everything about them is: what a band
// opens on, whether it has been touched, and what it reports.
//
// Touched is per band because the two bands mean different things by "left
// alone". An untouched targets band leaves the loadout's declaration and the
// repository's evidence in charge; ticking a *skill* must not turn that into an
// explicit list of agents, or narrowing a deployment would silently pin it to
// whatever was detected that day. And the two namespaces overlap: a loadout may
// carry a skill called "cursor", which must not open the Cursor agent ticked.
func TestPickerBandsAnswerForThemselvesAlone(t *testing.T) {
	p := newPicker(
		band{ID: groupTargets, Title: "TARGETS", Noun: "target", Multi: true, Chosen: []string{"claude"},
			Options: []choice{{Key: "claude", Label: "Claude Code"}, {Key: "cursor", Label: "Cursor"}}},
		band{ID: groupSkills, Title: "SKILLS", Noun: "skill", Multi: true, Chosen: []string{"cursor", "react"},
			Options: []choice{{Key: "cursor", Label: "cursor"}, {Key: "react", Label: "react"}}},
	)

	if got := p.keys(groupTargets); len(got) != 1 || got[0] != "claude" {
		t.Fatalf("a skill named cursor ticked the Cursor agent: %v", got)
	}
	if got := p.keys(groupSkills); len(got) != 2 {
		t.Fatalf("the skills band did not open on everything: %v", got)
	}

	// Untick a skill. The skills band now answers, and the targets band still
	// does not.
	p.cursor = 2
	p.toggle()
	if got := p.chosen(groupSkills); len(got) != 1 || got[0] != "react" {
		t.Errorf("the skills band reported %v, want just react", got)
	}
	if got := p.chosen(groupTargets); got != nil {
		t.Errorf("ticking a skill turned the untouched targets band into a choice: %v", got)
	}

	// Untick the last skill and the refusal names the band that is empty, not
	// the other one.
	p.cursor = 3
	p.toggle()
	g, empty := p.emptyBand()
	if !empty || g.Noun != "skill" {
		t.Errorf("an empty skills band reported %+v (empty=%v)", g, empty)
	}
	// A band nothing carries answers with nothing rather than with everything.
	if got := p.keys("nosuchband"); got != nil {
		t.Errorf("an unknown band answered with %v", got)
	}
}

// A card's own prose is written to fit the narrowest terminal the roster claims
// to work on, so it is never cut off mid-sentence.
//
// Unlike the body of a report, a sentence cannot be counted instead of read: a
// card that says "Real files plus barracks.lock, tracked by git. Everyone…"
// reads as a rendering fault, and the half of the sentence that explains what
// the order is about to do to somebody else's checkout is the half that goes.
// The card is drawn tall enough here that nothing may legitimately be elided,
// so any ellipsis on it is a line that did not fit.
func TestNoCardCutsItsOwnProseInHalf(t *testing.T) {
	r := fakeRecords{
		root:     "/repo/lab",
		loadouts: []*loadout.Loadout{unitLoadout("frontline", "a")},
		leases:   []*lease.Lease{spawnedLease("frontline", "/repo/lab", "/repo/lab/.claude/skills", 1)},
	}
	cfg := withActions(cfgFor(r))

	for _, w := range []int{60, 80, 100} {
		for _, script := range [][]string{{"s"}, {"r"}, {"g"}, {"L"}} {
			frame := plain(Frame(cfg, w, 34, script...))
			for _, line := range strings.Split(frame, "\n") {
				if !strings.ContainsRune(line, '║') {
					continue
				}
				if strings.ContainsRune(line, '…') {
					t.Errorf("%d columns, %v: a card cut its own prose: %q", w, script, strings.TrimSpace(line))
				}
			}
		}
	}

	// And the budget those lines are written to is the one the narrowest
	// terminal really has, rather than a number that drifted away from it.
	narrow := newModel(cfgFor(r))
	narrow.Update(tea.WindowSizeMsg{Width: 60, Height: 24})
	if got := narrow.cardText(); got != cardProse {
		t.Errorf("a card has %d columns of text at 60 wide, but its prose is written for %d", got, cardProse)
	}
}

// TestTheDeployCardHoldsTheFrameWithAVeryLongSkillList is the layout claim
// asserted where it actually has to hold.
//
// The lab this feature was designed against uses a source carrying fifteen
// hundred skills, and 80x24 is the smallest terminal the roster claims to work
// on. A picker written for a short list grows the card past the screen, the
// compositor's bounds are the union of its layers, and the terminal clips the
// bottom - taking the one line that says how to leave the card. Asserting this
// at a comfortable width proves nothing at all.
func TestTheDeployCardHoldsTheFrameWithAVeryLongSkillList(t *testing.T) {
	var skills []string
	for i := 0; i < 1500; i++ {
		skills = append(skills, fmt.Sprintf("skill-%04d-with-a-long-enough-name-to-wrap-a-narrow-card", i))
	}
	r := fakeRecords{root: "/repo/lab", loadouts: []*loadout.Loadout{unitLoadout("frontline", skills...)}}
	cfg := withActions(cfgFor(r))

	for _, size := range [][2]int{{80, 24}, {60, 20}, {80, 14}, {120, 40}} {
		w, h := size[0], size[1]
		for _, script := range [][]string{
			{"s"},
			// Down into the middle of the skills band, and to the very end.
			append([]string{"s"}, repeat("j", 800)...),
			{"s", "k"},
		} {
			got := plain(Frame(cfg, w, h, script...))
			what := fmt.Sprintf("%dx%d after %d keys", w, h, len(script))
			fits(t, got, w, h, what)
			if !strings.Contains(got, "stand down") {
				t.Errorf("%s: the card no longer says how to leave it:\n%s", what, got)
			}
			// Whatever it is showing, it says how much it is not showing. A
			// picker silently offering eight of fifteen hundred reads as a unit
			// with eight skills.
			if !strings.Contains(got, "of 1505") {
				t.Errorf("%s: the picker hid options without saying so:\n%s", what, got)
			}
			if !strings.Contains(got, "▸ [") {
				t.Errorf("%s: the row the cursor is on is not on screen:\n%s", what, got)
			}
		}
	}
}

func repeat(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

// The skills band is the loadout's own recorded skills, name-sorted and without
// repeats, and it opens with every one of them ticked - because deploying the
// whole unit is what a deploy is.
func TestTheSkillsBandIsTheLoadoutsOwnSkills(t *testing.T) {
	l := &loadout.Loadout{Name: "frontline", ID: "id-frontline"}
	l.Equipment = []loadout.Equipment{
		{Source: source.Source{Host: "github.com", Owner: "a", Repo: "one", Ref: "main"}, Commit: "c1", Skills: []string{"react", "css"}},
		{Source: source.Source{Host: "github.com", Owner: "a", Repo: "two", Ref: "main"}, Commit: "c2", Skills: []string{"css", "armory"}},
	}
	if got := skillNames(l); !reflect.DeepEqual(got, []string{"armory", "css", "react"}) {
		t.Fatalf("skillNames = %v", got)
	}

	r := fakeRecords{root: "/repo/lab", loadouts: []*loadout.Loadout{l}}
	d := &deployTracker{}
	cfg := withMenus(cfgFor(r))
	cfg.Deploy = d.deploy

	card := plain(Frame(cfg, 100, 34, "s"))
	if !strings.Contains(card, "SKILLS") {
		t.Fatalf("the deploy card offers no skills band:\n%s", card)
	}
	for _, want := range []string{"[x] armory", "[x] css", "[x] react"} {
		if !strings.Contains(card, want) {
			t.Errorf("the skills band did not open on %q:\n%s", want, card)
		}
	}

	// Untouched is nil, which reaches the engine as no narrowing at all rather
	// than as a hand-written list of everything.
	Frame(cfg, 100, 34, "s", "y", "@pump")
	if d.skills != nil {
		t.Errorf("an untouched skills band narrowed the deployment to %v", d.skills)
	}

	// Ticking is the whole point: five targets come first, then armory, css,
	// react - so eight presses of j lands on css.
	Frame(cfg, 100, 34, append(append([]string{"s"}, repeat("j", 6)...), "space", "y", "@pump")...)
	if !reflect.DeepEqual(d.skills, []string{"armory", "react"}) {
		t.Errorf("unticking css gave the deploy %v", d.skills)
	}

	// And unticking a skill leaves the targets band untouched, so where the
	// deploy goes is still barracks' own answer.
	if d.targets != nil {
		t.Errorf("choosing a skill overrode the target selection with %v", d.targets)
	}
}

// A loadout whose definition records no skills gets no skills band at all,
// rather than an empty one that could only ever refuse.
func TestALoadoutWithNoRecordedSkillsOffersNoSkillsBand(t *testing.T) {
	l := unitLoadout("frontline")
	l.Equipment = []loadout.Equipment{{
		Source: source.Source{Host: "github.com", Owner: "a", Repo: "one", Ref: "main"}, Commit: "c1",
	}}
	r := fakeRecords{root: "/repo/lab", loadouts: []*loadout.Loadout{l}}
	d := &deployTracker{}
	cfg := withMenus(cfgFor(r))
	cfg.Deploy = d.deploy

	card := plain(Frame(cfg, 100, 34, "s"))
	if strings.Contains(card, "SKILLS") {
		t.Errorf("a loadout recording no skills was offered a skills band:\n%s", card)
	}
	Frame(cfg, 100, 34, "s", "y", "@pump")
	if d.calls != 1 || d.skills != nil {
		t.Errorf("the deploy ran %d times with skills %v", d.calls, d.skills)
	}
}

// A partial deployment is distinguishable in the roster row and named in the
// dossier, without either being derived from a display string.
func TestAPartialDeploymentReadsDifferentlyFromAWholeOne(t *testing.T) {
	whole := spawnedLease("frontline", "/repo/lab", "/repo/lab/.claude/skills", 3)
	part := spawnedLease("siegeworks", "/repo/lab", "/repo/lab/.claude/skills", 1)
	part.Selection = []string{"skill-0"}

	r := fakeRecords{
		root: "/repo/lab",
		loadouts: []*loadout.Loadout{
			unitLoadout("frontline", "skill-0", "skill-1", "skill-2"),
			unitLoadout("siegeworks", "skill-0", "skill-1", "skill-2"),
		},
		leases: []*lease.Lease{whole, part},
	}
	st := gather(r)
	for _, u := range st.Units {
		want := "deployed"
		if u.Loadout.Name == "siegeworks" {
			want = "partial"
		}
		if got := u.Status(); got != want {
			t.Errorf("%s posture = %q, want %q", u.Loadout.Name, got, want)
		}
	}

	frame := plain(Frame(cfgFor(r), 110, 34, "j"))
	for _, want := range []string{"◐ partial", "1 of 3 skills"} {
		if !strings.Contains(frame, want) {
			t.Errorf("the roster does not show %q for a partial deployment:\n%s", want, frame)
		}
	}
	// The dossier names what is standing rather than making the user work it
	// out from a count.
	dossier := frame[strings.Index(frame, "DEPLOYMENTS"):]
	if !strings.Contains(dossier, "skill-0") {
		t.Errorf("the dossier does not name the standing skill:\n%s", dossier)
	}
}
