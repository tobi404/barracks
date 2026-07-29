package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
func (f fakeRecords) Garrisons(string) ([]garrison.Garrison, error) {
	return f.garrisons, f.gerr
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

func TestDeployShowsProgressWhileItRunsAndTheOutcomeAfter(t *testing.T) {
	r := fakeRecords{root: "/repo/lab", loadouts: []*loadout.Loadout{unitLoadout("frontline", "a", "b")}}
	d := &deployTracker{report: []string{
		"github.com/unit/frontline  fetching…",
		"✓ github.com/unit/frontline  fetched 0123456",
	}}
	cfg := cfgFor(r)
	cfg.Deploy = d.deploy

	working := plain(Frame(cfg, 110, 30, "s", "y", "@work"))
	if !strings.Contains(working, "MOVING OUT") {
		t.Fatalf("no in-flight screen:\n%s", working)
	}
	for _, line := range d.report {
		if !strings.Contains(working, strings.TrimPrefix(line, "✓ ")[:20]) {
			t.Errorf("progress line %q never reached the screen:\n%s", line, working)
		}
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

func TestEmitterDropsWhatItCannotDeliver(t *testing.T) {
	var e emitter
	e.emit(progressMsg{"nobody is listening"}) // must not panic

	got := make(chan tea.Msg, 1)
	e.bind(func(m tea.Msg) { got <- m })
	e.emit(progressMsg{"heard"})
	if msg := <-got; msg.(progressMsg).line != "heard" {
		t.Errorf("emitter delivered %v", msg)
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
