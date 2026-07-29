package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/tobi404/barracks/internal/garrison"
	"github.com/tobi404/barracks/internal/lease"
	"github.com/tobi404/barracks/internal/loadout"
	"github.com/tobi404/barracks/internal/progress"
	"github.com/tobi404/barracks/internal/spawn"
	"github.com/tobi404/barracks/internal/tui"
)

// records hands the roster the same stores every command reads. It exists so
// internal/tui never resolves a path of its own; a second way of finding a
// lease record is a second thing that can be wrong.
type records struct {
	env  *Env
	root string
}

func (r records) Loadouts() ([]*loadout.Loadout, []error) { return r.env.loadouts.List() }
func (r records) Leases() ([]*lease.Lease, []error)       { return r.env.leases.List() }
func (r records) Root() string                            { return r.root }

func (r records) Garrisons(root string) ([]garrison.Garrison, error) {
	m, err := garrison.Load(root)
	if err != nil {
		return nil, err
	}
	return m.Garrisons, nil
}

// runTUI opens the full-screen roster.
//
// Everything barracks normally writes to a stream is captured while the roster
// owns the terminal: the progress indicator, the flavor line and every report a
// command prints are all designed for a scrolling terminal, and any of them
// reaching the alternate screen would paint over the roster. What they say is
// not discarded - it is folded into the outcome panel the roster draws itself.
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
	return tui.Config{
		Records: records{env: e, root: root},
		Version: Version,
		Deploy: func(ctx context.Context, l *loadout.Loadout, report func(string)) tui.Outcome {
			return e.tuiDeploy(ctx, l, report)
		},
		Recall: func(ctx context.Context, l *loadout.Loadout) tui.Outcome {
			return e.tuiRecall(ctx, l)
		},
	}
}

// tuiDeploy is `barracks spawn <loadout>` with its report captured rather than
// printed. It goes through the same engine and the same target selection, so a
// spawn made here is indistinguishable on disk from one made at the prompt.
func (e *Env) tuiDeploy(ctx context.Context, l *loadout.Loadout, report func(string)) tui.Outcome {
	restore := e.captureStreams()
	defer restore()

	// The store's progress reporter normally animates on stderr. Here it writes
	// into the roster instead, one plain line at a time - Live is false, so it
	// emits no escape sequence that could reach the alternate screen.
	//
	// The reveal delay is dropped to nothing on purpose. It exists so a fast
	// operation in a scrolling terminal does not flash a line and erase it; the
	// roster has already put a modal up and committed the space, so there is
	// nothing left to flicker and every step is worth showing.
	previous := e.store.Progress
	e.store.Progress = &progress.Reporter{W: lineWriter(report), Live: false, Reveal: time.Nanosecond}
	defer func() { e.store.Progress = previous }()

	sel, err := e.selectTargets(ctx, l, nil, false)
	if err != nil {
		return tui.Outcome{Err: err, Notices: capturedLines(e)}
	}
	results, err := e.spawnAll(ctx, spawn.Request{
		Loadout: l,
		Cwd:     e.Cwd,
		Kind:    lease.KindManual,
	}, sel.Targets)
	if err != nil {
		return tui.Outcome{Err: err, Notices: capturedLines(e)}
	}

	out := tui.Outcome{Title: fmt.Sprintf("%s deployed", l.Name)}
	out.Lines = append(out.Lines, fmt.Sprintf("targets: %s (%s)", strings.Join(sel.IDs(), ", "), sel.Reason()))
	for i, res := range results {
		out.Lines = append(out.Lines, fmt.Sprintf("%s  %d skills", sel.Targets[i].Display, len(res.Skills)))
		for _, s := range res.Skills {
			out.Lines = append(out.Lines, "  + "+s.Name)
		}
		out.Notices = append(out.Notices, res.Notices...)
	}
	out.Notices = append(out.Notices, capturedLines(e)...)
	return out
}

// tuiRecall is `barracks recall <loadout>` for the spawns in this repository.
//
// The committed tier is deliberately out of scope here: `barracks recall` also
// removes a garrison, and removing tracked files from somebody's checkout is not
// something the roster offers behind a single key.
func (e *Env) tuiRecall(ctx context.Context, l *loadout.Loadout) tui.Outcome {
	restore := e.captureStreams()
	defer restore()

	loc, err := e.scopeOf(ctx, false)
	if err != nil {
		return tui.Outcome{Err: err, Notices: capturedLines(e)}
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
		out.Lines = append(out.Lines, fmt.Sprintf("%s  %d skills removed", displayOf(ls.Target), len(rep.Removed)))
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
			Notices: append(notices, capturedLines(e)...),
		}
	}
	out.Notices = append(notices, capturedLines(e)...)
	return out
}

// captureStreams redirects the command streams into a buffer for the duration
// of an action, and returns the function that puts them back.
func (e *Env) captureStreams() func() {
	prevOut, prevErr, prevBuf := e.Out, e.Err, e.captured
	buf := &bytes.Buffer{}
	e.Out, e.Err = buf, buf
	e.captured = buf
	// The buffer goes back with the streams. Leaving it behind would let a
	// later reader pick up a buffer nothing is writing to any more and report
	// its contents a second time.
	return func() { e.Out, e.Err, e.captured = prevOut, prevErr, prevBuf }
}

// capturedLines is whatever the action wrote to the captured streams, so
// anything barracks refused to touch still reaches the user.
func capturedLines(e *Env) []string {
	if e.captured == nil {
		return nil
	}
	text := strings.TrimSpace(e.captured.String())
	e.captured.Reset()
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// lineWriter turns the progress reporter's stream into one call per line.
type lineWriter func(string)

func (w lineWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(string(p), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			w(line)
		}
	}
	return len(p), nil
}

var _ io.Writer = lineWriter(nil)

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
