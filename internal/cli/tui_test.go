package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tobi404/barracks/internal/garrison"
	"github.com/tobi404/barracks/internal/gitcmd"
	"github.com/tobi404/barracks/internal/paths"
	"github.com/tobi404/barracks/internal/target"
	"github.com/tobi404/barracks/internal/testutil"
	"github.com/tobi404/barracks/internal/tui"
)

// ansiRE matches the styling a frame carries. Assertions read the frame without
// it: what is being checked is the layout and the words, and a colour change in
// the middle of a word would otherwise break every assertion in this file.
var ansiRE = regexp.MustCompile(`\x1b\[[0-9;:]*[a-zA-Z]`)

func plain(s string) string { return ansiRE.ReplaceAllString(s, "") }

// envFor builds the same Env a real invocation runs in, for the tests that need
// to drive the roster rather than a command.
func (h *harness) envFor(out, errb *bytes.Buffer) *Env {
	h.t.Helper()
	env := &Env{
		Out:    out,
		Err:    errb,
		Cwd:    h.workingDir(),
		Layout: h.layout,
		Now:    func() time.Time { return h.now },
		Prober: h.prober,
		Git:    gitcmd.Git{},
		Getenv: func(k string) string { return h.env[k] },
		Home:   func() (string, error) { return h.home, nil },
		Tty:    func() bool { return h.tty },
		ErrTty: func() bool { return h.errTty },

		ProgressAfter: h.progressAfter,
	}
	if err := env.init(); err != nil {
		h.t.Fatal(err)
	}
	return env
}

// frame renders one frame of the roster from the real records this harness has
// on disk.
func (h *harness) frame(w, hh int, script ...string) string {
	h.t.Helper()
	frame, _ := h.frameAndTerminal(w, hh, script...)
	return frame
}

// frameAndTerminal is frame plus whatever an order wrote to the terminal the
// roster handed back to it while it ran.
func (h *harness) frameAndTerminal(w, hh int, script ...string) (string, string) {
	h.t.Helper()
	var out, errb bytes.Buffer
	env := h.envFor(&out, &errb)
	dark := true
	cfg := env.tuiConfig(context.Background())
	cfg.Dark = &dark
	// The frame is read without its styling, but the terminal stream is handed
	// back exactly as it was written: whether it carries an escape sequence at
	// all is one of the things worth asserting about it.
	frame, released := tui.FrameAndTerminal(cfg, w, hh, script...)
	return plain(frame), released
}

// envUpdateGolden rewrites the recorded help instead of comparing against it,
// for the one case where the change to that output was the point.
//
// An environment variable rather than a test flag: a bare `barracks` in this
// suite reaches cobra with no argument list of its own, so cobra falls back to
// the test binary's own os.Args. pflag ignores anything spelled `-test.…` and
// nothing else, so a flag added here would be parsed as barracks' and fail the
// very invocation this test is about.
const envUpdateGolden = "BARRACKS_TEST_UPDATE_GOLDEN"

// TestBareBarracksKeepsPrintingHelpOffATerminal is the non-TTY contract.
//
// barracks is run in scripts, pipes and CI. A full-screen program there would
// write alternate-screen and cursor sequences into whatever is reading, then
// wait forever for a key that is never coming. So the roster opens only when
// stdout is a terminal, and every other bare invocation prints the help it
// printed before the roster existed, plus the one line for the command the
// roster added.
//
// The comparison is against the whole recorded body rather than a handful of
// substrings on purpose. A substring assertion cannot see a line arriving or
// leaving, which is exactly how `barracks [flags]` - a side effect of making
// the root command runnable, wanted by nothing - reached this output and was
// only caught by diffing against a binary built from the commit before. Any
// change to what a bare barracks says off a terminal now has to be made here
// too, deliberately: regenerate with BARRACKS_TEST_UPDATE_GOLDEN=1 set, and
// read the diff it leaves in the working tree.
func TestBareBarracksKeepsPrintingHelpOffATerminal(t *testing.T) {
	h := newHarness(t)
	h.tty = false

	out, errb, err := h.run()
	if err != nil {
		t.Fatalf("bare barracks failed: %v (stderr %s)", err, errb)
	}
	if errb != "" {
		t.Errorf("bare barracks off a terminal wrote to stderr: %q", errb)
	}
	if strings.Contains(out, "\x1b") || strings.Contains(errb, "\x1b") {
		t.Errorf("bare barracks off a terminal emitted an escape sequence:\nstdout %q\nstderr %q", out, errb)
	}

	golden := filepath.Join("testdata", "bare-help.golden")
	if os.Getenv(envUpdateGolden) != "" {
		if err := os.WriteFile(golden, []byte(out), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Logf("rewrote %s", golden)
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("no recorded help to compare against: %v", err)
	}
	if out != string(want) {
		t.Errorf("the help a bare barracks prints off a terminal changed:\n%s",
			lineDiff(string(want), out))
	}
}

// lineDiff reports the lines that differ between two bodies. The whole help is
// forty lines, so a test that just printed both would bury the one line that
// moved.
func lineDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	var b strings.Builder
	for i := 0; i < len(w) || i < len(g); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			fmt.Fprintf(&b, "line %d:\n  recorded %q\n  printed  %q\n", i+1, wl, gl)
		}
	}
	if b.Len() == 0 {
		return "(the bodies differ only in trailing content)"
	}
	return b.String()
}

// Printing help touches nothing. A bare barracks off a terminal does exactly
// what it did before the roster existed, and creating the barracks directories
// is not part of that: on a machine where they cannot be created, asking for
// help must still answer with help rather than with `barracks: <err>` and a
// non-zero exit.
func TestBareBarracksOffATerminalInitialisesNothing(t *testing.T) {
	h := newHarness(t)

	// A regular file where the data directory would go, so anything that tried
	// to create one would fail rather than quietly succeed.
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.layout = paths.Layout{Config: filepath.Join(blocked, "brk"), Data: filepath.Join(blocked, "brk")}
	h.tty = false

	out, errb, err := h.run()
	if err != nil {
		t.Fatalf("bare barracks could not print help without a writable home: %v (stderr %s)", err, errb)
	}
	if !strings.Contains(out, "Available Commands:") {
		t.Errorf("bare barracks printed no help:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(blocked, "brk")); err == nil {
		t.Error("printing help created the barracks data directories")
	}
}

// TestTheRosterNeverOpensOntoTheNullDevice is the one case the character-device
// test the flavor line uses gets wrong, and it gets it wrong expensively.
//
// os.DevNull is a character device, so `barracks > /dev/null` from a shell with
// a controlling terminal passes that test: the roster would open onto nothing
// and wait forever for a key, which is precisely the hang the non-TTY rule
// exists to prevent. Confirmed by hand on a pty before this test was written -
// the suite cannot see an alternate screen, but it can see the decision.
func TestTheRosterNeverOpensOntoTheNullDevice(t *testing.T) {
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Skipf("no %s on this machine: %v", os.DevNull, err)
	}
	defer null.Close()

	h := newHarness(t)
	h.mustRun("train", "frontline")

	var roster openedRoster
	var errb bytes.Buffer
	env := &Env{
		Out: null, Err: &errb, Cwd: h.workingDir(), Layout: h.layout,
		Now:    func() time.Time { return h.now },
		Prober: h.prober,
		Git:    gitcmd.Git{},
		Getenv: func(k string) string { return h.env[k] },
		Home:   func() (string, error) { return h.home, nil },
		// Exactly what the real Env answers with stdout on /dev/null: a
		// character device, so the flavor line's own test says yes.
		Tty:        func() bool { return true },
		ErrTty:     func() bool { return false },
		openRoster: roster.open,
	}
	if !env.isTerminal() {
		t.Fatal("the harness did not reproduce the condition: isTerminal must say yes here")
	}
	if env.canOpenTheRoster() {
		t.Fatal("barracks would open a full screen onto /dev/null and hang")
	}

	cmd := New(env)
	cmd.SetArgs(nil)
	cmd.SetOut(null)
	cmd.SetErr(&errb)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("bare barracks onto /dev/null failed: %v", err)
	}
	if roster.opens != 0 {
		t.Errorf("bare barracks opened the roster onto /dev/null")
	}
}

// openedRoster records that the roster was opened, and with what. It stands in
// for the program loop and nothing else: the config it captures is the real one
// env.tuiConfig built, so a test can render the very screen the binary would.
type openedRoster struct {
	opens int
	cfg   tui.Config
}

func (o *openedRoster) open(_ context.Context, cfg tui.Config) error {
	o.opens++
	o.cfg = cfg
	return nil
}

// TestBareBarracksOpensTheRosterOnATerminal is the other half of the non-TTY
// contract: on a terminal, a bare invocation is the roster rather than the help.
func TestBareBarracksOpensTheRosterOnATerminal(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontline", "--description", "Field kit for the forward squad")

	var roster openedRoster
	h.roster = roster.open
	h.tty = true

	out, errb, err := h.run()
	if err != nil {
		t.Fatalf("bare barracks on a terminal failed: %v (stderr %s)", err, errb)
	}
	if roster.opens != 1 {
		t.Fatalf("bare barracks on a terminal opened the roster %d times, want 1", roster.opens)
	}
	if strings.Contains(out, "Available Commands:") {
		t.Errorf("bare barracks on a terminal printed help as well as opening the roster:\n%s", out)
	}
	// The roster was handed the real records, not an empty config: the screen
	// it would have drawn shows the loadout that was just trained.
	dark := true
	cfg := roster.cfg
	cfg.Dark = &dark
	if frame := plain(tui.Frame(cfg, 120, 32)); !strings.Contains(frame, "Field kit for") {
		t.Errorf("the roster was opened without the records on disk:\n%s", frame)
	}
	// The flavor line is printed from PersistentPostRun, after the roster has
	// already given the terminal back, and would be the last thing on screen
	// after a session in which nothing changed. runTUI declares the invocation
	// a preview, so nothing follows the roster out.
	if errb != "" {
		t.Errorf("something followed the roster onto the terminal: %q", errb)
	}
}

// TestTUICommandIsTheExplicitSpelling covers `barracks tui` on both sides of the
// terminal question. It refuses in barracks' own wording rather than letting the
// UI library fail with its, or - far worse - succeeding and writing a full
// screen of escape sequences into whatever stdout is.
func TestTUICommandIsTheExplicitSpelling(t *testing.T) {
	h := newHarness(t)

	var roster openedRoster
	h.roster = roster.open
	h.tty = true
	if _, errb, err := h.run("tui"); err != nil {
		t.Fatalf("barracks tui on a terminal failed: %v (stderr %s)", err, errb)
	}
	if roster.opens != 1 {
		t.Fatalf("barracks tui on a terminal opened the roster %d times, want 1", roster.opens)
	}

	h.tty = false
	out, errb, err := h.run("tui")
	if err == nil {
		// A non-nil error here is exactly what Main turns into exit 1, so this
		// is the non-zero exit a script would see.
		t.Fatal("barracks tui off a terminal succeeded, so the process would exit 0")
	}
	if roster.opens != 1 {
		t.Errorf("barracks tui off a terminal still opened the roster")
	}
	if !strings.Contains(err.Error(), "the roster needs a terminal") {
		t.Errorf("the refusal is not in barracks' wording: %v", err)
	}
	for _, want := range []string{"barracks list", "barracks deployed", "barracks inspect"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q as the scriptable alternative: %v", want, err)
		}
	}
	if strings.Contains(out, "\x1b") || strings.Contains(errb, "\x1b") {
		t.Errorf("a refused roster emitted an escape sequence:\nstdout %q\nstderr %q", out, errb)
	}
}

// TestRosterReadsTheRecordsOnDisk is the claim the whole roster rests on:
// the screen is drawn from the same records the commands write, not from
// anything the TUI invented.
func TestRosterReadsTheRecordsOnDisk(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontline", "--description", "Field kit for the forward squad")
	h.mustRun("train", "reserves")
	h.mustRun("equip", "frontline", h.sourceArg("skills"), "--except", "legacy")
	h.mustRun("spawn", "frontline")

	got := h.frame(120, 32)
	for _, want := range []string{
		"B A R R A C K S", "ROSTER", "DOSSIER",
		"frontline", "reserves",
		"deployed",      // the spawn just made, read back out of the lease
		"unequipped",    // the loadout that carries nothing yet
		"Field kit for", // the description, from the definition
		"react",         // a skill name, from the equipment record
	} {
		if !strings.Contains(got, want) {
			t.Errorf("roster frame is missing %q:\n%s", want, got)
		}
	}
}

// TestRosterDeploysThroughTheRealEngine drives the whole state-changing flow -
// select, order, confirm, work, outcome - and then checks the disk.
func TestRosterDeploysThroughTheRealEngine(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontline")
	h.mustRun("equip", "frontline", h.sourceArg("skills"), "--except", "legacy")

	confirm := h.frame(120, 32, "s")
	if !strings.Contains(confirm, "DEPLOY ORDER") || !strings.Contains(confirm, "Send frontline into") {
		t.Fatalf("s did not raise the deploy order:\n%s", confirm)
	}

	outcome := h.frame(120, 32, "s", "y", "@pump")
	for _, want := range []string{"FRONTLINE DEPLOYED", "+ react", "+ css", "Claude Code"} {
		if !strings.Contains(outcome, want) {
			t.Errorf("outcome frame is missing %q:\n%s", want, outcome)
		}
	}
	for _, name := range []string{"react", "css"} {
		link := filepath.Join(h.skillsDir(), name)
		if _, err := os.Lstat(link); err != nil {
			t.Errorf("the roster reported a deploy that did not happen: %v", err)
		}
	}
	if status := h.work.Status(t); status != "" {
		t.Errorf("a deploy from the roster dirtied git status:\n%s", status)
	}
}

// TestRosterShowsBarracksOwnProgressLines is the stream rule seen end to end:
// the real internal/progress reporter, unmodified, writing barracks' own words
// onto the terminal the roster handed back for the fetch - never onto the
// alternate screen, and never nowhere.
//
// A deploy fetches, and a fetch can put a prompt on that terminal that barracks
// neither raised nor can forward - ssh asking for a key passphrase, a
// credential helper asking for a password. So the screen is given up for the
// whole order, and what the order reports goes where the user is looking.
func TestRosterShowsBarracksOwnProgressLines(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontline")
	h.mustRun("equip", "frontline", h.sourceArg("skills"), "--except", "legacy")
	// equip already populated the store, so a spawn would reuse it and report
	// nothing. Clearing it makes the deploy do the slow work a first one does.
	if err := os.RemoveAll(h.layout.StoreDir()); err != nil {
		t.Fatal(err)
	}

	got, released := h.frameAndTerminal(120, 32, "s", "y", "@work")
	if !strings.Contains(got, "MOVING OUT") {
		t.Fatalf("no in-flight screen:\n%s", got)
	}
	if !strings.Contains(released, "unpacking") {
		t.Errorf("the progress reporter's own words never reached the terminal:\n%s", released)
	}
	if strings.Contains(released, "\x1b") {
		t.Errorf("the reporter wrote an escape sequence onto a terminal that may be carrying a prompt: %q", released)
	}
}

// The capture buffers are what the roster's outcome panel is built from, and
// the two streams have to stay two: a command reports on stdout and says what
// it could not do on stderr, which is exactly the body/notice split the card
// draws. Folding them into one buffer would file every report as a problem.
//
// Draining is the other half. What has been shown once must not be shown again
// by the next order, and a buffer left behind with the streams would let a
// later reader pick up output nothing is writing to any more.
func TestCapturedStreamsStaySeparateAndDrain(t *testing.T) {
	h := newHarness(t)
	var out, errb bytes.Buffer
	env := h.envFor(&out, &errb)

	restore := env.captureStreams()
	fmt.Fprintln(env.Out, "garrisoned frontline")
	fmt.Fprintln(env.Err, "! left in place: /repo/.claude/skills/react")

	report, notices := env.capturedReport(), env.capturedNotices()
	if len(report) != 1 || report[0] != "garrisoned frontline" {
		t.Errorf("the report is not what was written to stdout: %q", report)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "left in place") {
		t.Errorf("the notices are not what was written to stderr: %q", notices)
	}
	if again := env.capturedReport(); again != nil {
		t.Errorf("a drained buffer reported its contents a second time: %q", again)
	}

	restore()
	if env.capturedOut != nil || env.capturedErr != nil {
		t.Error("the capture buffers outlived the streams they belong to")
	}
	if out.Len() != 0 || errb.Len() != 0 {
		t.Errorf("captured output escaped onto the real streams: %q %q", out.String(), errb.String())
	}
	// And the streams really are back: what is written now reaches the caller.
	fmt.Fprintln(env.Out, "after")
	if !strings.Contains(out.String(), "after") {
		t.Errorf("the streams were not put back: %q", out.String())
	}
}

// TestRosterRefusesAnEmptyLoadout proves a refusal reaches the screen rather
// than being swallowed: an order that cannot be carried out has to say so.
func TestRosterRefusesAnEmptyLoadout(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "reserves")

	got := h.frame(120, 32, "s")
	if !strings.Contains(got, "carries nothing") {
		t.Errorf("deploying an unequipped loadout said nothing:\n%s", got)
	}
	if strings.Contains(got, "DEPLOY ORDER") {
		t.Errorf("an unequipped loadout still raised a deploy order:\n%s", got)
	}
}

// TestRosterRecallsWhatItDeployed closes the loop, through the same lease
// revocation `barracks recall` uses.
func TestRosterRecallsWhatItDeployed(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontline")
	h.mustRun("equip", "frontline", h.sourceArg("skills"), "--except", "legacy")
	h.mustRun("spawn", "frontline")

	got := h.frame(120, 32, "r", "y", "@pump")
	if !strings.Contains(got, "FRONTLINE RECALLED") {
		t.Fatalf("recall outcome missing:\n%s", got)
	}
	if _, err := os.Lstat(filepath.Join(h.skillsDir(), "react")); !os.IsNotExist(err) {
		t.Errorf("the roster reported a recall that did not happen: %v", err)
	}
}

// The outcome card counts what an order moved, and a count of one is not
// "1 skills".
//
// The dossier's own counts are held by internal/tui's TestTheDossierCountsInTheSingular,
// but the line an outcome card prints is built here, from the engine's results -
// so it is a second set of sites with the same failure mode, and the roster
// reporting "1 skills removed" reads exactly as wrong as the dossier doing it.
// Both directions of the order are asserted because each builds its own line.
func TestTheOutcomeCardCountsInTheSingular(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "scouts")
	h.mustRun("equip", "scouts", h.sourceArg("skills"), "--only", "react")

	deployed := h.frame(120, 32, "s", "y", "@pump")
	if !strings.Contains(deployed, "SCOUTS DEPLOYED") {
		t.Fatalf("the deploy never happened, so this proved nothing:\n%s", deployed)
	}
	if !strings.Contains(deployed, "1 skill ") || strings.Contains(deployed, "1 skills") {
		t.Errorf("a one-skill deploy is not reported as \"1 skill\":\n%s", deployed)
	}

	recalled := h.frame(120, 32, "r", "y", "@pump")
	if !strings.Contains(recalled, "SCOUTS RECALLED") {
		t.Fatalf("the recall never happened, so this proved nothing:\n%s", recalled)
	}
	if !strings.Contains(recalled, "1 skill removed") || strings.Contains(recalled, "1 skills") {
		t.Errorf("a one-skill recall is not reported as \"1 skill removed\":\n%s", recalled)
	}
}

// TestRosterMovesAndCancels covers the parts of the screen that change nothing.
func TestRosterMovesAndCancels(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "alpha")
	h.mustRun("train", "bravo")

	if got := h.frame(120, 32, "j"); !strings.Contains(got, "▸ bravo") {
		t.Errorf("j did not move the cursor:\n%s", got)
	}
	if got := h.frame(120, 32, "j", "k"); !strings.Contains(got, "▸ alpha") {
		t.Errorf("k did not move the cursor back:\n%s", got)
	}
	if got := h.frame(120, 32, "?"); !strings.Contains(got, "ORDERS") {
		t.Errorf("? did not open the orders overlay:\n%s", got)
	}
	// A key nothing is bound to leaves the screen alone. The roster advertises
	// no verb it cannot carry out, so the loadout-editing verbs - train, equip,
	// strip, rename - have no key at all rather than one that explains itself.
	idle := h.frame(120, 32)
	for _, k := range []string{"t", "e", "x", "z", "w"} {
		if got := h.frame(120, 32, k); got != idle {
			t.Errorf("%q is bound to something the roster does not advertise:\n%s", k, got)
		}
	}
	if got := h.frame(120, 32, "R", "@pump"); !strings.Contains(got, "alpha") {
		t.Errorf("refresh lost the roster:\n%s", got)
	}
	if got := h.frame(60, 20); !strings.Contains(got, "ROSTER") {
		t.Errorf("the roster does not survive a narrow terminal:\n%s", got)
	}
}

// TestRosterGarrisonsThroughTheRealEngine is the committed tier from the
// roster, checked on disk rather than on the screen: real files, a lockfile,
// and nothing registered in .git/info/exclude - the whole point of this tier is
// that git tracks it.
func TestRosterGarrisonsThroughTheRealEngine(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontline")
	h.mustRun("equip", "frontline", h.sourceArg("skills"), "--except", "legacy")
	excludeBefore := h.work.ReadExclude(t)

	card := h.frame(120, 32, "g")
	if !strings.Contains(card, "GARRISON ORDER") || !strings.Contains(card, "Commit frontline into") {
		t.Fatalf("g did not raise the garrison order:\n%s", card)
	}
	if testutil.Exists(filepath.Join(h.work.Dir, garrison.LockName)) {
		t.Fatal("the order was carried out before it was confirmed")
	}

	outcome := h.frame(120, 32, "g", "y", "@pump")
	if !strings.Contains(outcome, "FRONTLINE GARRISONED") {
		t.Fatalf("the garrison outcome never reached the screen:\n%s", outcome)
	}
	for _, name := range []string{"react", "css"} {
		path := filepath.Join(h.skillsDir(), name, "SKILL.md")
		if !testutil.Exists(path) {
			t.Errorf("the roster reported a garrison that did not happen: %s is missing", path)
		}
		if testutil.IsSymlink(t, filepath.Join(h.skillsDir(), name)) {
			t.Errorf("%s was symlinked rather than committed", name)
		}
	}
	if !testutil.Exists(filepath.Join(h.work.Dir, garrison.LockName)) {
		t.Error("no lockfile was written")
	}
	if got := h.work.ReadExclude(t); got != excludeBefore {
		t.Errorf("a garrison registered itself in .git/info/exclude:\n%s", got)
	}
	// The card's body is printGarrison's own report, so the roster and the
	// command say the same thing about the same result.
	for _, want := range []string{"garrisoned frontline", "wrote " + garrison.LockName, "commit these files"} {
		if !strings.Contains(outcome, want) {
			t.Errorf("the outcome card is missing the command's own line %q:\n%s", want, outcome)
		}
	}
}

// The two tiers refuse each other, and the roster has to go through that
// refusal rather than around it: a path both excluded from git as a symlink and
// committed as a file hides the files from the team or dirties every checkout
// forever.
func TestRosterGarrisonRefusesOverASpawn(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontline")
	h.mustRun("equip", "frontline", h.sourceArg("skills"), "--except", "legacy")
	h.mustRun("spawn", "frontline")

	got := h.frame(120, 32, "g", "y", "@pump")
	if !strings.Contains(got, "REFUSED") {
		t.Fatalf("garrisoning over a spawn was not refused:\n%s", got)
	}
	if testutil.Exists(filepath.Join(h.work.Dir, garrison.LockName)) {
		t.Error("a refused garrison still wrote a lockfile")
	}
	if !testutil.IsSymlink(t, filepath.Join(h.skillsDir(), "react")) {
		t.Error("a refused garrison replaced the spawn it refused over")
	}
}

// TestTheRostersUpgradePlanIsTheCommandsDryRun is the invariant this feature is
// held to, asserted as an equality rather than as a family resemblance.
//
// `barracks upgrade --dry-run` and a real upgrade must print the same body, and
// the roster gets that for free only if it goes through the same Plan and the
// same renderer. So the plan the card is built from is compared line for line
// against what the command prints, with the two banner lines a dry run adds
// around it removed. Anything that recomputed the plan, reworded it, or
// reordered it fails here.
func TestTheRostersUpgradePlanIsTheCommandsDryRun(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontline")
	h.mustRun("equip", "frontline", h.sourceArg("skills"), "--except", "legacy")
	h.mustRun("spawn", "frontline")
	h.src.AddSkills(t,
		testutil.Skill{Path: "skills/react", Body: "---\nname: react\n---\n\nversion two\n"},
		testutil.Skill{Path: "skills/hooks"})
	h.src.Commit(t, "move react forward")

	var out, errb bytes.Buffer
	env := h.envFor(&out, &errb)
	l, err := env.loadouts.Get("frontline")
	if err != nil {
		t.Fatal(err)
	}
	var released bytes.Buffer
	preview := env.tuiUpgrade(context.Background(), l, tui.Session{Out: &released, Err: &released})
	if preview.Apply == nil {
		t.Fatal("the roster offered no plan to carry out")
	}

	dry := h.mustRun("upgrade", "frontline", "--dry-run")
	var want []string
	for _, line := range strings.Split(strings.TrimSpace(dry), "\n") {
		if strings.HasPrefix(line, "dry run") {
			continue
		}
		want = append(want, line)
	}
	if len(want) == 0 {
		t.Fatal("the dry run printed no body, so this compared nothing")
	}
	if len(preview.Lines) != len(want) {
		t.Fatalf("the roster's plan is %d lines and the dry run's is %d:\nroster:\n%s\ncommand:\n%s",
			len(preview.Lines), len(want), strings.Join(preview.Lines, "\n"), strings.Join(want, "\n"))
	}
	for i := range want {
		if preview.Lines[i] != want[i] {
			t.Errorf("line %d differs:\n roster  %q\n command %q", i+1, preview.Lines[i], want[i])
		}
	}
}

// The plan is shown before anything moves, and carrying it out is what moves
// it. Both halves are checked on disk: the skill the upgrade would change still
// has its old content while the card is up, and the new one afterwards.
func TestRosterUpgradeChangesNothingUntilThePlanIsConfirmed(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontline")
	h.mustRun("equip", "frontline", h.sourceArg("skills"), "--only", "react")
	h.mustRun("spawn", "frontline")

	link := filepath.Join(h.skillsDir(), "react", "SKILL.md")
	before := testutil.ReadFile(t, link)

	h.src.AddSkills(t, testutil.Skill{Path: "skills/react", Body: "---\nname: react\n---\n\nversion two\n"})
	h.src.Commit(t, "move react forward")

	plan := h.frame(120, 32, "u", "@pump")
	if !strings.Contains(plan, "UPGRADE PLAN") || !strings.Contains(plan, "y carry it out") {
		t.Fatalf("u did not put a plan in front of the user:\n%s", plan)
	}
	if got := testutil.ReadFile(t, link); got != before {
		t.Fatalf("showing the plan already changed the spawn:\n%s", got)
	}

	// Standing the plan down leaves it exactly as it was.
	h.frame(120, 32, "u", "@pump", "n")
	if got := testutil.ReadFile(t, link); got != before {
		t.Fatalf("a plan that was stood down was applied anyway:\n%s", got)
	}

	done := h.frame(120, 32, "u", "@pump", "y", "@pump")
	if !strings.Contains(done, "FRONTLINE UPGRADED") {
		t.Fatalf("the upgrade outcome never reached the screen:\n%s", done)
	}
	if got := testutil.ReadFile(t, link); !strings.Contains(got, "version two") {
		t.Errorf("the confirmed plan did not relink the spawn:\n%s", got)
	}
	if status := h.work.Status(t); status != "" {
		t.Errorf("an upgrade from the roster dirtied git status:\n%s", status)
	}
}

// An upgrade reaches both tiers, and the committed half is the one somebody
// else reads. It has to be planned before anything is applied and applied after
// the definitions are saved, exactly as the command does it.
func TestRosterUpgradeBringsTheCommittedTierForward(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontline")
	h.mustRun("equip", "frontline", h.sourceArg("skills"), "--only", "react")
	h.mustRun("garrison", "frontline")

	vendored := filepath.Join(h.skillsDir(), "react", "SKILL.md")
	h.src.AddSkills(t, testutil.Skill{Path: "skills/react", Body: "---\nname: react\n---\n\nversion two\n"})
	h.src.Commit(t, "move react forward")

	plan := h.frame(120, 32, "u", "@pump")
	if !strings.Contains(plan, garrison.LockName) {
		t.Fatalf("the plan says nothing about the committed tier:\n%s", plan)
	}
	if got := testutil.ReadFile(t, vendored); strings.Contains(got, "version two") {
		t.Fatal("planning already rewrote the committed file")
	}

	h.frame(120, 32, "u", "@pump", "y", "@pump")
	if got := testutil.ReadFile(t, vendored); !strings.Contains(got, "version two") {
		t.Errorf("the committed file was not brought onto the new pin:\n%s", got)
	}
	// The lockfile and the files have to name the same commit, which is what
	// inspect is for.
	if _, _, err := h.run("inspect"); err != nil {
		t.Errorf("the checkout does not match %s after an upgrade from the roster: %v", garrison.LockName, err)
	}
}

// The picker is only worth having if what it chooses is where the skills land,
// so this drives it through the real spawn engine and looks on disk.
func TestTheRosterPickerSendsTheSpawnWhereItWasTold(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontline")
	h.mustRun("equip", "frontline", h.sourceArg("skills"), "--only", "react")

	card := h.frame(120, 32, "s")
	if !strings.Contains(card, "[x] Claude Code") {
		t.Fatalf("the picker did not open on where a plain spawn would go:\n%s", card)
	}

	// Down to Cursor - claude, agents, cursor is the registry's own order - and
	// take it as well as the one already chosen.
	chosen := h.frame(120, 32, "s", "j", "j", "space")
	if !strings.Contains(chosen, "[x] Cursor") {
		t.Fatalf("space did not choose Cursor:\n%s", chosen)
	}
	outcome := h.frame(120, 32, "s", "j", "j", "space", "y", "@pump")
	if !strings.Contains(outcome, "FRONTLINE DEPLOYED") {
		t.Fatalf("the deploy never happened:\n%s", outcome)
	}
	for _, dir := range []string{
		filepath.Join(h.work.Dir, ".claude", "skills", "react"),
		filepath.Join(h.work.Dir, ".cursor", "skills", "react"),
	} {
		if !testutil.IsSymlink(t, dir) {
			t.Errorf("the chosen target was not spawned into: %s", dir)
		}
	}
	// A chosen selection is the user's own, and the roster says so in words
	// that are true of the surface it was chosen on. target.Selection's own
	// wording for an explicit choice is "given on the command line", which
	// describes a flag; there is no command line in front of somebody looking
	// at a full-screen picker.
	if !strings.Contains(outcome, "chosen on the roster") {
		t.Errorf("a picked selection was not reported as one:\n%s", outcome)
	}
	if strings.Contains(outcome, "command line") {
		t.Errorf("the roster told the user about a command line they never used:\n%s", outcome)
	}
	// And a deploy nobody picked reports exactly what the command reports,
	// because it made exactly the same decision.
	h.mustRun("recall", "frontline")
	var want string
	for _, line := range strings.Split(h.mustRun("spawn", "frontline"), "\n") {
		if strings.HasPrefix(line, "targets: ") {
			want = line
		}
	}
	if want == "" {
		t.Fatal("the command printed no target line, so this compared nothing")
	}
	h.mustRun("recall", "frontline")
	untouched := h.frame(120, 32, "s", "y", "@pump")
	if !strings.Contains(untouched, want) {
		t.Errorf("an untouched picker did not report what the command reports (%q):\n%s", want, untouched)
	}
	if status := h.work.Status(t); status != "" {
		t.Errorf("a picked deploy dirtied git status:\n%s", status)
	}
}

// TestRosterLaunchSpawnsRunsAndRecalls is `barracks run` from the roster,
// through the same session the command drives.
//
// The agent stands in for a real one, and what it checks is the only thing that
// matters about a run: the skills were there while it ran. The recall after it
// is checked on disk, because a session that left the skills behind would be a
// spawn nobody asked for.
func TestRosterLaunchSpawnsRunsAndRecalls(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontline")
	h.mustRun("equip", "frontline", h.sourceArg("skills"), "--only", "react")

	saw := filepath.Join(h.root, "saw-the-skill")
	agent := testutil.WriteScript(t, filepath.Join(h.root, "agent"),
		"test -e "+filepath.Join(h.skillsDir(), "react")+" && echo yes > "+saw+"\n"+
			"echo the agent ran")

	var out, errb bytes.Buffer
	env := h.envFor(&out, &errb)
	l, err := env.loadouts.Get("frontline")
	if err != nil {
		t.Fatal(err)
	}
	var released bytes.Buffer
	outcome := env.tuiLaunch(context.Background(), l, tui.Launcher{Command: agent, Display: "test agent"},
		tui.Session{In: strings.NewReader(""), Out: &released, Err: &released})

	if outcome.Err != nil {
		t.Fatalf("the session refused: %v", outcome.Err)
	}
	if !testutil.Exists(saw) {
		t.Error("the agent could not see the skills the session spawned for it")
	}
	if !strings.Contains(released.String(), "the agent ran") {
		t.Errorf("the agent's own output never reached the terminal:\n%s", released.String())
	}
	// barracks' half of the session is on that terminal too - the user is
	// looking at it - and is also what the card shows on the way back.
	if !strings.Contains(released.String(), "spawned frontline") {
		t.Errorf("barracks' own report never reached the terminal:\n%s", released.String())
	}
	if !strings.Contains(strings.Join(outcome.Lines, "\n"), "recalled frontline") {
		t.Errorf("the card does not say the session was recalled:\n%s", strings.Join(outcome.Lines, "\n"))
	}
	if testutil.Exists(filepath.Join(h.skillsDir(), "react")) {
		t.Error("the session left its skills behind")
	}
	if status := h.work.Status(t); status != "" {
		t.Errorf("a session from the roster dirtied git status:\n%s", status)
	}
}

// An agent that exits non-zero is not a barracks refusal: the loadout was
// spawned, the agent ran, and the loadout was recalled exactly as asked.
// Reporting REFUSED would send somebody looking for a spawn that never stayed.
func TestALaunchedAgentsExitStatusIsNotARefusal(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontline")
	h.mustRun("equip", "frontline", h.sourceArg("skills"), "--only", "react")
	agent := testutil.WriteScript(t, filepath.Join(h.root, "failing-agent"), "exit 3")

	var out, errb bytes.Buffer
	env := h.envFor(&out, &errb)
	l, err := env.loadouts.Get("frontline")
	if err != nil {
		t.Fatal(err)
	}
	var released bytes.Buffer
	outcome := env.tuiLaunch(context.Background(), l, tui.Launcher{Command: agent},
		tui.Session{In: strings.NewReader(""), Out: &released, Err: &released})
	if outcome.Err != nil {
		t.Fatalf("an agent's exit status was turned into a barracks refusal: %v", outcome.Err)
	}
	if !strings.Contains(strings.Join(outcome.Lines, "\n"), "exited with status 3") {
		t.Errorf("the exit status was swallowed:\n%s", strings.Join(outcome.Lines, "\n"))
	}
	if testutil.Exists(filepath.Join(h.skillsDir(), "react")) {
		t.Error("a failing session left its skills behind")
	}
}

// A launch with no agent behind it must refuse rather than exec the empty
// string, which is what a picker with nothing in it would otherwise reach.
func TestALaunchWithNoAgentRefuses(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontline")

	var out, errb bytes.Buffer
	env := h.envFor(&out, &errb)
	l, err := env.loadouts.Get("frontline")
	if err != nil {
		t.Fatal(err)
	}
	outcome := env.tuiLaunch(context.Background(), l, tui.Launcher{}, tui.Session{})
	if outcome.Err == nil {
		t.Fatal("a launch with no agent chosen was carried out")
	}
}

// The launch menu is the registry's own Binaries filtered by what is actually
// on the PATH. Both halves matter: an entry barracks does not know would send
// the skills nowhere it understands, and an entry that is not installed is a
// key that fails one step later with "executable file not found".
func TestLaunchersAreKnownAgentsThatAreActuallyInstalled(t *testing.T) {
	known := map[string]bool{}
	for _, tg := range target.Registry {
		for _, bin := range tg.Binaries {
			known[bin] = true
		}
	}
	seen := map[string]bool{}
	for _, l := range launchers() {
		if !known[l.Command] {
			t.Errorf("%q is offered but is not any registry entry's own CLI", l.Command)
		}
		if _, err := exec.LookPath(l.Command); err != nil {
			t.Errorf("%q is offered but is not on the PATH: %v", l.Command, err)
		}
		if seen[l.Command] {
			t.Errorf("%q is offered twice", l.Command)
		}
		seen[l.Command] = true
		if l.Display == "" {
			t.Errorf("%q is offered with no agent name to show", l.Command)
		}
	}

	// And the menu really is filtered rather than empty by accident: a registry
	// binary planted on the PATH is offered.
	dir := t.TempDir()
	testutil.WriteScript(t, filepath.Join(dir, "claude"), "exit 0")
	t.Setenv("PATH", dir)
	found := false
	for _, l := range launchers() {
		if l.Command == "claude" {
			found = true
		}
	}
	if !found {
		t.Errorf("an installed agent was not offered: %v", launchers())
	}
}

// The deploy picker is built from the registry, so a new agent appears in it by
// being a new entry - and it says which of them this repository already shows,
// because that is what makes a choice other than the default an informed one.
func TestTheTargetMenuIsTheRegistry(t *testing.T) {
	h := newHarness(t)
	testutil.MkDir(t, filepath.Join(h.work.Dir, ".cursor"))

	got := targetOptions(h.work.Dir)
	if len(got) != len(target.Registry) {
		t.Fatalf("the menu offers %d targets and the registry has %d", len(got), len(target.Registry))
	}
	for i, o := range got {
		if o.ID != target.Registry[i].ID || o.Display != target.Registry[i].Display {
			t.Errorf("option %d is %+v, want the registry's %s", i, o, target.Registry[i].ID)
		}
	}
	present := map[string]bool{}
	for _, o := range got {
		present[o.ID] = o.Present
	}
	if !present["cursor"] {
		t.Error("the agent this repository shows was not marked as present")
	}
	if present["windsurf"] {
		t.Error("an agent this repository does not show was marked as present")
	}
	// Outside a repository nothing can be present, and asking must not panic.
	for _, o := range targetOptions("") {
		if o.Present {
			t.Errorf("%s was marked present with no repository to be present in", o.ID)
		}
	}
}

// An upgrade that failed is never headlined as one that worked.
//
// `barracks upgrade` exits non-zero when a source could not be resolved, and
// the roster has to reach the same verdict from the same place. A green
// FRONTLINE UPGRADED over a failed upgrade is a report barracks cannot stand
// behind, and the cost of it is delayed: the user finds out later, from the
// skills that never moved.
func TestARosterUpgradeThatFailedIsNotReportedAsASuccess(t *testing.T) {
	h := newHarness(t)
	other := h.secondSource(testutil.Skill{Path: "skills/hooks"})
	h.mustRun("train", "frontline")
	h.mustRun("equip", "frontline", h.sourceArg("skills"), "--only", "react")
	h.mustRun("equip", "frontline", other.Dir+"#main:skills")
	h.mustRun("spawn", "frontline")

	// One source moves forward and the other is taken away entirely, so the
	// plan has real work in it and still cannot be carried out in full - the
	// case a "did anything change" reading of the result would call a success.
	other.AddSkills(t, testutil.Skill{Path: "skills/hooks", Body: "---\nname: hooks\n---\n\nversion two\n"})
	other.Commit(t, "move hooks forward")
	if err := os.RemoveAll(h.src.Dir); err != nil {
		t.Fatal(err)
	}

	got := h.frame(120, 32, "u", "@pump", "y", "@pump")
	if strings.Contains(got, "FRONTLINE UPGRADED") {
		t.Errorf("a failed upgrade was headlined as a success:\n%s", got)
	}
	if !strings.Contains(got, "REFUSED") {
		t.Errorf("a failed upgrade was not reported as a failure:\n%s", got)
	}
	if !strings.Contains(got, "some sources could not be upgraded") {
		t.Errorf("the card does not say what the command exits on:\n%s", got)
	}

	// The command is what the card has to agree with, so this is only a
	// comparison for as long as the command still exits non-zero on it.
	if _, _, err := h.run("upgrade", "frontline"); err == nil {
		t.Fatal("the command no longer fails on an unresolvable source, so this compared nothing")
	}
}

// A plan whose every source failed has nothing to carry out, so the roster must
// not offer to carry it out. The one key on that card would do no work and then
// report the same failure a second time.
func TestAnUpgradeThatResolvedNothingIsNotOfferedToBeCarriedOut(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontline")
	h.mustRun("equip", "frontline", h.sourceArg("skills"), "--only", "react")
	h.mustRun("spawn", "frontline")
	if err := os.RemoveAll(h.src.Dir); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	env := h.envFor(&out, &errb)
	l, err := env.loadouts.Get("frontline")
	if err != nil {
		t.Fatal(err)
	}
	var released bytes.Buffer
	preview := env.tuiUpgrade(context.Background(), l, tui.Session{Out: &released, Err: &released})
	if preview.Apply != nil {
		t.Error("a plan that resolved nothing was still offered to be applied")
	}
	if preview.Err == nil {
		t.Error("a plan that resolved nothing was not a refusal")
	}

	got := h.frame(120, 32, "u", "@pump")
	if strings.Contains(got, "carry it out") {
		t.Errorf("the roster offered to carry out a plan with nothing in it:\n%s", got)
	}
	if !strings.Contains(got, "REFUSED") || !strings.Contains(got, "could not be upgraded") {
		t.Errorf("a plan that resolved nothing was not reported as a refusal:\n%s", got)
	}
}

// A loadout declaring a target the registry does not know is a broken
// definition. `barracks spawn` refuses it and says so, and the roster has to do
// the same rather than open a picker with nothing ticked - which reads as a
// choice to be made, and turns the broken declaration into an explicit
// per-spawn override the moment somebody makes it.
func TestTheRosterRefusesALoadoutWhoseDeclaredTargetIsUnknown(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontline")
	h.mustRun("equip", "frontline", h.sourceArg("skills"), "--only", "react")

	var out, errb bytes.Buffer
	env := h.envFor(&out, &errb)
	l, err := env.loadouts.Get("frontline")
	if err != nil {
		t.Fatal(err)
	}
	l.Targets = []string{"retired-agent"}
	if err := env.loadouts.Save(l); err != nil {
		t.Fatal(err)
	}

	if _, _, err := h.run("spawn", "frontline"); err == nil {
		t.Fatal("the command accepted a target the registry does not know, so this compared nothing")
	}

	got := h.frame(120, 32, "s")
	if !strings.Contains(got, "REFUSED") || !strings.Contains(got, "does not know") {
		t.Fatalf("the roster hid the broken declaration:\n%s", got)
	}
	if strings.Contains(got, "DEPLOY ORDER") {
		t.Errorf("a broken declaration still raised a deploy order:\n%s", got)
	}

	// Pressing on cannot turn the refusal into a deploy that lands anywhere.
	h.frame(120, 32, "s", "space", "y", "@pump")
	if _, err := os.Stat(h.skillsDir()); !os.IsNotExist(err) {
		t.Errorf("a refused loadout was deployed anyway: %v", err)
	}
}

// An upgrade in which nothing resolved still has work to do when a spawn is
// behind the commit the loadout is already pinned at.
//
// That is not a corner case: it is what an upgrade that deliberately left a
// live session alone leaves behind, and AGENTS.md carries the invariant that
// such a skip can never become permanent. `barracks upgrade` reconciles the
// spawn and then exits non-zero, so the roster must offer the same work rather
// than reading "every source failed" as "there is nothing to carry out".
func TestARosterUpgradeThatResolvedNothingStillReconcilesAStrandedSpawn(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontline")
	h.mustRun("equip", "frontline", h.sourceArg("skills"), "--only", "react")
	h.mustRun("spawn", "frontline")

	// A live session holds the spawn, so the upgrade moves the definition on
	// and leaves the links exactly where they are.
	store := leaseStore(t, h)
	leases, _ := store.List()
	held := leases[0]
	held.Kind = "process"
	held.Owner = ownerFor(4242, "a-live-agent-session")
	if err := store.Save(held); err != nil {
		t.Fatal(err)
	}
	h.prober.alive[4242] = "a-live-agent-session"

	h.src.AddSkills(t, testutil.Skill{Path: "skills/react", Body: "---\nname: react\n---\n\nversion two\n"})
	h.src.Commit(t, "move react forward")
	if out := h.mustRun("upgrade", "frontline"); !strings.Contains(out, "left as it is") {
		t.Fatalf("the upgrade did not leave the held spawn alone, so nothing is stranded:\n%s", out)
	}
	link := filepath.Join(h.skillsDir(), "react")
	if body := resolved(t, link); strings.Contains(body, "version two") {
		t.Fatalf("the held spawn was relinked, so nothing is stranded to recover: %q", body)
	}

	// The session ends and the source goes away entirely, so nothing can be
	// resolved - and the spawn is still behind the pin the loadout records.
	delete(h.prober.alive, 4242)
	if err := os.RemoveAll(h.src.Dir); err != nil {
		t.Fatal(err)
	}

	plan := h.frame(120, 32, "u", "@pump")
	if !strings.Contains(plan, "y carry it out") {
		t.Fatalf("the roster declined to offer work the command would still do:\n%s", plan)
	}
	h.frame(120, 32, "u", "@pump", "y", "@pump")
	if body := resolved(t, link); !strings.Contains(body, "version two") {
		t.Errorf("the stranded spawn was never brought forward: %q", body)
	}
}

// The deploy picker opens on where a plain `barracks spawn` would send the
// loadout, and it has to still be true after the roster has deployed something.
//
// A deploy into an agent this repository did not show before makes that agent
// detected, so the answer the card opened on a moment ago is no longer the
// answer the command would give. Leaving the picker alone passes nothing
// through as an override, so a card showing a stale set would install
// somewhere the user was never shown.
func TestThePickerOpensOnWhereTheSpawnWouldGoAfterAnOrderHasLanded(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontline")
	h.mustRun("equip", "frontline", h.sourceArg("skills"), "--only", "react")

	// Untick Claude, take Cursor instead - claude, agents, cursor is the
	// registry's own order - then dismiss the outcome and ask again.
	again := h.frame(120, 32, "s", "space", "j", "j", "space", "y", "@pump", "esc", "s")
	if !testutil.IsSymlink(t, filepath.Join(h.work.Dir, ".cursor", "skills", "react")) {
		t.Fatal("the deploy never landed, so the second card had nothing to notice")
	}
	if !strings.Contains(again, "[x] Cursor") {
		t.Errorf("the picker did not open on the agent this repository now shows:\n%s", again)
	}
	if strings.Contains(again, "[x] Claude Code") {
		t.Errorf("the picker opened on an answer the deploy had already made untrue:\n%s", again)
	}
}
