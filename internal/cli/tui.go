package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/tobi404/barracks/internal/garrison"
	"github.com/tobi404/barracks/internal/lease"
	"github.com/tobi404/barracks/internal/loadout"
	"github.com/tobi404/barracks/internal/progress"
	"github.com/tobi404/barracks/internal/spawn"
	"github.com/tobi404/barracks/internal/target"
	"github.com/tobi404/barracks/internal/tui"
	"github.com/tobi404/barracks/internal/upgrade"
)

// records hands the roster the same stores every command reads. It exists so
// internal/tui never resolves a path of its own; a second way of finding a
// lease record is a second thing that can be wrong.
type records struct {
	env  *Env
	root string
	// targets is what the deploy picker opens on, held here so a muster can
	// throw it away.
	targets *deployTargets
}

// Loadouts is the first read of a muster, and a muster is the roster asking the
// world again. Anything worked out from the last one goes with it: `R` exists
// precisely for the case where something changed that barracks did not do.
func (r records) Loadouts() ([]*loadout.Loadout, []error) {
	r.targets.forget()
	return r.env.loadouts.List()
}

func (r records) Leases() ([]*lease.Lease, []error) { return r.env.leases.List() }
func (r records) Root() string                      { return r.root }

func (r records) Garrisons(root string) (*garrison.Manifest, error) {
	return garrison.Load(root)
}

// runTUI opens the full-screen roster.
//
// Everything barracks normally writes to a stream is captured while the roster
// owns the terminal: the progress indicator, the flavor line and every report a
// command prints are all designed for a scrolling terminal, and any of them
// reaching the alternate screen would paint over the roster. What they say is
// not discarded - it is folded into the outcome panel the roster draws itself.
//
// The exception is an order the roster has handed the terminal back to. While
// that is running there is no alternate screen to protect, and the user is
// looking straight at the stream, so it is where the order reports.
func (e *Env) runTUI(ctx context.Context) error {
	// The flavor line is printed from PersistentPostRun, after the roster has
	// already given the terminal back. It would be the last thing on screen
	// after a session in which nothing may have changed at all, so this
	// invocation declares itself a preview and stays silent.
	e.previews()
	return e.openRoster(ctx, e.tuiConfig(ctx))
}

// tuiConfig is what the roster is given. It is separate from runTUI so a test
// can drive the same screen the binary draws without opening a terminal.
func (e *Env) tuiConfig(ctx context.Context) tui.Config {
	root := ""
	if loc, ok := e.repoHere(ctx); ok {
		root = loc.Root
	}
	targets := e.deployTargets(ctx)
	return tui.Config{
		Records:   records{env: e, root: root, targets: targets},
		Version:   Version,
		Targets:   targetOptions(root),
		Launchers: launchers(),
		Selection: targets.of,
		Deploy: func(ctx context.Context, l *loadout.Loadout, chosen []string, s tui.Session) tui.Outcome {
			defer targets.forget()
			return e.tuiDeploy(ctx, l, chosen, s)
		},
		Recall: func(ctx context.Context, l *loadout.Loadout) tui.Outcome {
			defer targets.forget()
			return e.tuiRecall(ctx, l)
		},
		Garrison: func(ctx context.Context, l *loadout.Loadout, s tui.Session) tui.Outcome {
			defer targets.forget()
			return e.tuiGarrison(ctx, l, s)
		},
		Upgrade: func(ctx context.Context, l *loadout.Loadout, s tui.Session) tui.Preview {
			// Planning writes nothing outside the store, so only the half that
			// carries the plan out is a change worth forgetting anything for.
			p := e.tuiUpgrade(ctx, l, s)
			if p.Apply != nil {
				carryOut := p.Apply
				p.Apply = func(ctx context.Context, s tui.Session) tui.Outcome {
					defer targets.forget()
					return carryOut(ctx, s)
				}
			}
			return p
		},
		Launch: func(ctx context.Context, l *loadout.Loadout, program tui.Launcher, s tui.Session) tui.Outcome {
			defer targets.forget()
			return e.tuiLaunch(ctx, l, program, s)
		},
	}
}

// deployTargets answers where a deploy of a loadout would go if nobody chose
// otherwise - the same question `barracks spawn` answers, from the same call,
// including when the answer is a refusal. A loadout that declares a target the
// registry does not know is a broken definition and the command says so; the
// roster has to say the same thing rather than open a picker with nothing
// ticked, which reads as "choose something" and turns a refusal into an
// explicit per-spawn override the moment the user does.
//
// The answer is remembered because it is asked from inside the roster's event
// loop, where the screen does not repaint until it returns, and answering it
// runs two git subprocesses and then walks the repository for evidence of each
// agent. What it is remembered until is the whole of the design: see forget.
type deployTargets struct {
	ask     func(*loadout.Loadout) ([]string, string, error)
	settled map[string]targetAnswer
}

type targetAnswer struct {
	ids    []string
	reason string
	err    error
}

func (e *Env) deployTargets(ctx context.Context) *deployTargets {
	return &deployTargets{
		settled: map[string]targetAnswer{},
		ask: func(l *loadout.Loadout) ([]string, string, error) {
			sel, err := e.selectTargets(ctx, l, nil, false)
			if err != nil {
				return nil, "", err
			}
			return sel.IDs(), sel.Reason(), nil
		},
	}
}

func (d *deployTargets) of(l *loadout.Loadout) ([]string, string, error) {
	a, known := d.settled[l.Name]
	if !known {
		ids, reason, err := d.ask(l)
		a = targetAnswer{ids: ids, reason: reason, err: err}
		d.settled[l.Name] = a
	}
	return a.ids, a.reason, a.err
}

// forget drops every remembered answer, and is called from both events that can
// make one untrue: an order barracks carried out, and a muster.
//
// It is the order that matters most. A deploy into an agent this repository did
// not show before makes that agent detected, so the next card has to open on it
// - the picker promises that leaving it alone deploys exactly where the command
// would, and an answer remembered across the write that changed it would break
// that promise silently, in the direction of installing somewhere the user was
// never shown.
func (d *deployTargets) forget() {
	if d == nil {
		return
	}
	clear(d.settled)
}

// targetOptions is the menu the deploy picker offers: every agent barracks can
// deploy to, marked with whether this repository already shows it.
//
// It is built from the registry rather than from anything the roster knows, so
// a new agent appears in the picker by being a new registry entry, exactly as
// it appears everywhere else.
func targetOptions(root string) []tui.TargetOption {
	present := map[string]bool{}
	if root != "" {
		for _, t := range target.Detect(root) {
			present[t.ID] = true
		}
	}
	out := make([]tui.TargetOption, 0, len(target.Registry))
	for _, t := range target.Registry {
		out = append(out, tui.TargetOption{ID: t.ID, Display: t.Display, Present: present[t.ID]})
	}
	return out
}

// launchers are the agent programs a run started from the roster can start.
//
// The names come from the registry's own Binaries - the same field
// `barracks run` matches a command against - so the roster can never offer to
// launch something barracks does not otherwise know. Only what is on the PATH
// is offered: a menu entry that fails with "executable file not found" is a
// key that does nothing, one step later.
func launchers() []tui.Launcher {
	var out []tui.Launcher
	seen := map[string]bool{}
	for _, t := range target.Registry {
		for _, bin := range t.Binaries {
			if seen[bin] {
				continue
			}
			seen[bin] = true
			if _, err := exec.LookPath(bin); err != nil {
				continue
			}
			out = append(out, tui.Launcher{Command: bin, Display: t.Display})
		}
	}
	return out
}

// tuiDeploy is `barracks spawn <loadout>` with its report captured rather than
// printed. It goes through the same engine and the same target selection, so a
// spawn made here is indistinguishable on disk from one made at the prompt.
//
// targets is what the card's picker chose, and nil when the user left it alone.
// Passing nil straight through is the point: it reaches target.Select as no
// override at all, so the loadout's declaration and the repository's own
// evidence decide exactly as they would for `barracks spawn`.
//
// The roster runs this with the terminal handed back to it, so the session is
// the terminal rather than the screen; internal/tui owns that decision and this
// only says what happened.
func (e *Env) tuiDeploy(ctx context.Context, l *loadout.Loadout, targets []string, s tui.Session) tui.Outcome {
	restore := e.captureStreams()
	defer restore()
	defer e.reportTo(s)()

	sel, err := e.selectTargets(ctx, l, targets, false)
	if err != nil {
		return tui.Outcome{Err: err, Notices: e.capturedNotices()}
	}
	results, err := e.spawnAll(ctx, spawn.Request{
		Loadout: l,
		Cwd:     e.Cwd,
		Kind:    lease.KindManual,
	}, sel.Targets)
	if err != nil {
		return tui.Outcome{Err: err, Notices: e.capturedNotices()}
	}

	// Where the skills went, and why. target.Selection's own wording for an
	// explicit choice is "given on the command line", which is a true sentence
	// about a flag and a false one about a picker on a full screen - there is no
	// command line in front of the user. The engine is unchanged either way;
	// only the sentence the roster prints about its own surface is its own.
	reason := sel.Reason()
	if targets != nil {
		reason = "chosen on the roster"
	}
	out := tui.Outcome{Title: fmt.Sprintf("%s deployed", l.Name)}
	out.Lines = append(out.Lines, fmt.Sprintf("targets: %s (%s)", strings.Join(sel.IDs(), ", "), reason))
	for i, res := range results {
		out.Lines = append(out.Lines, fmt.Sprintf("%s  %d %s",
			sel.Targets[i].Display, len(res.Skills), plural(len(res.Skills), "skill", "skills")))
		for _, sk := range res.Skills {
			out.Lines = append(out.Lines, "  + "+sk.Name)
		}
		out.Notices = append(out.Notices, res.Notices...)
	}
	out.Notices = append(out.Notices, e.capturedNotices()...)
	return out
}

// tuiRecall is `barracks recall <loadout>` for the spawns in this repository.
//
// The committed tier is deliberately out of scope here: `barracks recall` also
// removes a garrison, and removing tracked files from somebody's checkout is not
// something the roster offers behind a single key. The recall card says so, so
// the asymmetry with the garrison order beside it is stated rather than left to
// be discovered.
func (e *Env) tuiRecall(ctx context.Context, l *loadout.Loadout) tui.Outcome {
	restore := e.captureStreams()
	defer restore()

	loc, err := e.scopeOf(ctx, false)
	if err != nil {
		return tui.Outcome{Err: err, Notices: e.capturedNotices()}
	}
	leases, problems := e.leases.List()
	var notices []string
	for _, p := range problems {
		notices = append(notices, p.Error())
	}

	out := tui.Outcome{Title: fmt.Sprintf("%s recalled", l.Name)}
	found := false
	for _, ls := range lease.FindInScope(leases, loc.Scope, loc.Root) {
		if ls.Loadout != l.Name {
			continue
		}
		found = true
		rep := lease.Revoke(ls, e.store, e.leases, "recalled")
		out.Lines = append(out.Lines, fmt.Sprintf("%s  %d %s removed",
			displayOf(ls.Target), len(rep.Removed), plural(len(rep.Removed), "skill", "skills")))
		for _, k := range rep.Kept {
			notices = append(notices, fmt.Sprintf("left in place (barracks did not create it): %s - %s", k.Path, k.Reason))
		}
		for _, err := range rep.Errors {
			notices = append(notices, err.Error())
		}
	}
	if !found {
		// The notices come with the refusal. An unreadable lease record is the
		// likeliest reason a spawn that is standing here could not be found, so
		// it is the last thing that may be dropped on this path.
		return tui.Outcome{
			Err:     fmt.Errorf("%s is not deployed in %s", l.Name, loc.Root),
			Notices: append(notices, e.capturedNotices()...),
		}
	}
	out.Notices = append(notices, e.capturedNotices()...)
	return out
}

// tuiGarrison is `barracks garrison <loadout>`: the committed tier, from the
// roster.
//
// It goes through repoScope and the same target rule the command uses, so every
// guard the command has - a repository is required, an existing garrison's
// recorded targets win over detection, a locally edited vendored file stops the
// update - applies here without being restated. The report is the command's own
// printGarrison, captured: there is one renderer of a garrison result.
func (e *Env) tuiGarrison(ctx context.Context, l *loadout.Loadout, s tui.Session) tui.Outcome {
	restore := e.captureStreams()
	defer restore()
	defer e.reportTo(s)()

	loc, err := e.repoScope(ctx)
	if err != nil {
		return tui.Outcome{Err: err, Notices: e.capturedNotices()}
	}
	sel, err := e.garrisonSelection(ctx, loc, l, nil)
	if err != nil {
		return tui.Outcome{Err: err, Notices: e.capturedNotices()}
	}
	res, err := e.garrisons.Install(ctx, garrison.Request{
		Root:      loc.Root,
		GitDir:    loc.GitDir,
		Name:      l.Name,
		ID:        l.ID,
		Equipment: l.Equipment,
		Targets:   sel.Targets,
		Force:     false,
	})
	if err != nil {
		return tui.Outcome{Err: err, Notices: e.capturedNotices()}
	}
	printGarrison(e, res)

	verb := "garrisoned"
	if !res.New {
		verb = "updated"
	}
	out := tui.Outcome{Title: fmt.Sprintf("%s %s", l.Name, verb)}
	out.Lines = e.capturedReport()
	out.Notices = e.capturedNotices()
	return out
}

// tuiUpgrade is `barracks upgrade <loadout>`, split at the seam the command
// already has: Plan decides everything and writes nothing, Apply executes.
//
// The roster shows the plan and then applies that same plan, so the body a user
// reads before confirming is the body `--dry-run` prints - not a second answer
// to the same question. Both halves fetch or write, so both run with the
// terminal handed back.
func (e *Env) tuiUpgrade(ctx context.Context, l *loadout.Loadout, s tui.Session) tui.Preview {
	restore := e.captureStreams()
	defer restore()
	defer e.reportTo(s)()

	eng := &upgrade.Engine{
		Store:    e.store,
		Loadouts: e.loadouts,
		Leases:   e.leases,
		Git:      e.Git,
		Prober:   e.Prober,
	}
	plans := eng.Plan(ctx, []*loadout.Loadout{l}, upgrade.Options{})
	// The committed tier is planned before anything is applied, so the card
	// describes it from the same reads the real run acts on.
	stages := e.planGarrisonUpgrades(ctx, plans)

	renderUpgrade(e, plans, false)
	renderGarrisonUpgrades(e, stages, true)

	p := tui.Preview{Outcome: tui.Outcome{
		Title:   fmt.Sprintf("%s upgrade plan", l.Name),
		Lines:   e.capturedReport(),
		Notices: e.capturedNotices(),
	}}
	if !upgradeActionable(plans, stages) {
		// There is nothing to carry out, so there is nothing to offer: an Apply
		// here would be a key that writes nothing and then reports what the plan
		// already said. What that plan says stands on its own - a refusal when a
		// source could not be resolved, and otherwise a report that every source
		// is where the loadout already has it.
		p.Err = upgradeVerdict(plans, true)
		return p
	}
	p.Apply = func(ctx context.Context, s tui.Session) tui.Outcome {
		restore := e.captureStreams()
		defer restore()
		defer e.reportTo(s)()

		eng.Apply(plans)
		// After the definitions are saved, never before: the committed files,
		// barracks.lock and the loadout must all name the same commits.
		e.applyGarrisonUpgrades(ctx, stages)
		renderUpgrade(e, plans, false)
		ok := renderGarrisonUpgrades(e, stages, false)

		// The verdict comes from the same function the command exits on. An
		// upgrade that failed must never be headlined as one that worked: what
		// the user believes moved forward and what did are then different, and
		// they find that out later, from the skills.
		return tui.Outcome{
			Title:   fmt.Sprintf("%s upgraded", l.Name),
			Lines:   e.capturedReport(),
			Notices: e.capturedNotices(),
			Err:     upgradeVerdict(plans, ok),
		}
	}
	return p
}

// tuiLaunch is `barracks run <loadout> -- <agent>` from the roster.
//
// The agent owns the terminal outright for the whole session, so nothing is
// captured here: barracks' own report and the agent's output both go to the
// session, which is the terminal the user is looking at. What the card shows
// afterwards is a copy of barracks' half of it, so returning to the roster does
// not lose the record of what was spawned and recalled.
func (e *Env) tuiLaunch(ctx context.Context, l *loadout.Loadout, program tui.Launcher, s tui.Session) tui.Outcome {
	if program.Command == "" {
		return tui.Outcome{Err: fmt.Errorf("no agent was chosen to run")}
	}
	prevOut, prevErr := e.Out, e.Err
	var report, problems bytes.Buffer
	e.Out = io.MultiWriter(s.Out, &report)
	e.Err = io.MultiWriter(s.Err, &problems)
	defer func() { e.Out, e.Err = prevOut, prevErr }()
	defer e.reportTo(s)()

	code, err := e.runSession(ctx, l, []string{program.Command}, nil, false, s.In, s.Out, s.Err)
	out := tui.Outcome{
		Title:   fmt.Sprintf("%s session ended", l.Name),
		Lines:   splitLines(report.String()),
		Notices: splitLines(problems.String()),
	}
	if err != nil {
		return tui.Outcome{Err: err, Lines: out.Lines, Notices: out.Notices}
	}
	if code != 0 {
		// Not a barracks refusal: the loadout was spawned, the agent ran, and
		// the loadout was recalled. Saying REFUSED here would send somebody
		// looking for a spawn that was made and stood down exactly as asked.
		out.Lines = append(out.Lines, fmt.Sprintf("%s exited with status %d", program.Command, code))
	}
	return out
}

// reportTo points the store's progress reporter at the terminal an order has
// been handed, and returns the function that puts the previous one back.
//
// The reporter normally animates on stderr. Here it writes one plain line at a
// time instead - Live is false, so it emits no escape sequence at all, which is
// what a terminal that may be about to carry a child's password prompt needs:
// nothing barracks writes may erase it.
//
// The reveal delay is dropped to nothing on purpose. It exists so a fast
// operation in a scrolling terminal does not flash a line and erase it, and an
// append-only reporter erases nothing; a user watching a screen barracks has
// just taken away from them is owed every step of what it is doing.
func (e *Env) reportTo(s tui.Session) func() {
	previous := e.store.Progress
	e.store.Progress = &progress.Reporter{W: s.Out, Live: false, Reveal: time.Nanosecond}
	return func() { e.store.Progress = previous }
}

// captureStreams redirects the command streams into buffers for the duration of
// an action, and returns the function that puts them back.
func (e *Env) captureStreams() func() {
	prevOut, prevErr := e.Out, e.Err
	prevOutBuf, prevErrBuf := e.capturedOut, e.capturedErr
	out, errb := &bytes.Buffer{}, &bytes.Buffer{}
	e.Out, e.capturedOut = out, out
	e.Err, e.capturedErr = errb, errb
	// The buffers go back with the streams. Leaving one behind would let a
	// later reader pick up a buffer nothing is writing to any more and report
	// its contents a second time.
	return func() {
		e.Out, e.Err = prevOut, prevErr
		e.capturedOut, e.capturedErr = prevOutBuf, prevErrBuf
	}
}

// capturedReport is what the action reported on stdout: the body of the card.
func (e *Env) capturedReport() []string { return drain(e.capturedOut) }

// capturedNotices is what the action wrote to stderr, so anything barracks
// refused to touch still reaches the user.
func (e *Env) capturedNotices() []string { return drain(e.capturedErr) }

// drain empties a capture buffer into lines. Emptying it is the point: what has
// been reported once must not be reported again by the next reader.
func drain(buf *bytes.Buffer) []string {
	if buf == nil {
		return nil
	}
	lines := splitLines(buf.String())
	buf.Reset()
	return lines
}

func splitLines(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// newTUICmd is the explicit spelling of the roster, for anyone who would rather
// name it than rely on what a bare `barracks` does.
func newTUICmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Open the full-screen roster",
		Long: strings.TrimSpace(`
Opens the roster: every loadout on this machine, what each one carries, and
where each one is standing, on one screen.

This is the same screen a bare ` + "`barracks`" + ` opens when stdout is a terminal. The
difference is only what happens when stdout is not one: a bare barracks prints
help, and this refuses and says why.`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Refused here rather than left to the UI library, which would fail
			// with its own wording about /dev/tty - or, worse, succeed and write
			// a full screen of escape sequences into the file stdout is.
			if !env.canOpenTheRoster() {
				return fmt.Errorf("the roster needs a terminal and stdout here is not one; for a script use `barracks list`, `barracks deployed` or `barracks inspect`")
			}
			env.reap()
			return env.runTUI(cmd.Context())
		},
	}
}
