package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tobi404/barracks/internal/gitcmd"
	"github.com/tobi404/barracks/internal/paths"
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

// lineWriter is the bridge between a reporter that writes a stream and a model
// that holds lines. It is what keeps the padding, the blank lines and the
// partial writes of a stream out of a panel that has to lay them out itself.
func TestLineWriterHandsOverWholeTidyLines(t *testing.T) {
	var got []string
	w := lineWriter(func(l string) { got = append(got, l) })

	// One write carrying several lines, trailing padding, and a blank line the
	// reporter uses to separate a hint from the line it is about.
	chunk := []byte("github.com/unit/frontline  fetching…   \n\n✓ github.com/unit/frontline  fetched 0123456\n")
	n, err := w.Write(chunk)
	if err != nil {
		t.Fatalf("lineWriter refused a write: %v", err)
	}
	// An io.Writer that reports fewer bytes than it was given has, by contract,
	// failed - and the reporter would stop writing to it.
	if n != len(chunk) {
		t.Errorf("lineWriter consumed %d of %d bytes", n, len(chunk))
	}
	want := []string{
		"github.com/unit/frontline  fetching…",
		"✓ github.com/unit/frontline  fetched 0123456",
	}
	if len(got) != len(want) {
		t.Fatalf("lineWriter produced %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
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
	// no verb it cannot carry out, so `g`, `u` and the rest are simply absent.
	idle := h.frame(120, 32)
	for _, k := range []string{"t", "e", "g", "u", "x"} {
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
