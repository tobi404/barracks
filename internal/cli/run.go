package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/tobi404/barracks/internal/lease"
	"github.com/tobi404/barracks/internal/loadout"
	"github.com/tobi404/barracks/internal/spawn"
	"github.com/tobi404/barracks/internal/target"
)

func newRunCmd(env *Env) *cobra.Command {
	var (
		global    bool
		targetIDs []string
	)

	cmd := &cobra.Command{
		Use:   "run <loadout> -- <command> [args...]",
		Short: "Spawn a loadout, run a command, recall on exit",
		Long: strings.TrimSpace(`
Spawns a loadout, runs a command with those skills available, and recalls the
loadout the moment the command exits.

This is the throwaway-session case: the skills exist for exactly as long as the
process does, and nothing is left behind afterwards. The loadout reaches every
agent it installs into, so one run can serve a command that reads more than one.

When the command is an agent barracks knows, that agent is equipped even if the
repository shows no sign of it - running claude here installs into Claude Code.
A --target flag or a loadout's own declaration still decides on its own; if it
leaves out the agent being launched, barracks says so rather than overruling it.

  barracks run frontend -- claude
  barracks run review -- claude -p "review this diff"
  barracks run frontend --target cursor -- cursor-agent

The lease is tied to the command's process identity, not just its PID, so a
recycled PID can never keep a dead lease alive. Ctrl-C is forwarded to the
command and the loadout is recalled as usual. If barracks itself is killed
outright, the next barracks command reaps the lease.`),
		Args:               cobra.MinimumNArgs(2),
		DisableFlagParsing: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			env.reap()

			name := args[0]
			argv := args[1:]
			if len(argv) == 0 {
				return fmt.Errorf("give a command to run after --, e.g. barracks run %s -- claude", name)
			}

			l, err := env.loadouts.Get(name)
			if err != nil {
				return err
			}
			code, err := env.runSession(cmd.Context(), l, argv, targetIDs, global, os.Stdin, env.Out, env.Err)
			if err != nil {
				return err
			}
			if code != 0 {
				return &ExitError{Code: code}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&global, "global", false, "spawn into each agent's user-level skills directory")
	cmd.Flags().StringSliceVar(&targetIDs, "target", nil, targetFlagHelp("spawn for"))
	return cmd
}

// runSession is the body of `barracks run`: choose the targets, spawn, run the
// command, recall it when the command exits. It returns the command's exit
// code, and an error only when barracks itself could not do its part.
//
// Both the command and the roster go through it, so a session started from
// either is identical on disk and reports the same words. The child's streams
// are arguments rather than Env's because the two callers hand it different
// terminals: the command gives it barracks' own, and the roster gives it the
// terminal Bubble Tea has just handed back - which is the only terminal an
// interactive agent can be driven from.
func (e *Env) runSession(ctx context.Context, l *loadout.Loadout, argv, targetIDs []string, global bool, in io.Reader, out, errw io.Writer) (int, error) {
	// run is the one command that already knows which agent is about to read
	// the skills, and equipping that agent's session is the whole point of it.
	// So the launched program joins target selection - but only where selection
	// would otherwise be barracks' own guess. An unrecognised program (a
	// wrapper, `sh -c ...`) matches nothing and changes nothing.
	launched := target.ForCommand(argv[0])
	sel, err := e.selectTargetsFor(ctx, l, targetIDs, global, launched)
	if err != nil {
		return 0, err
	}
	// Like a spawn, a run is scoped to a repository - see actedIn.
	e.noteScope(ctx, global)

	// The lease is born owned by this process, which is certainly alive, and
	// handed to the child once it starts. There is never a moment where a live
	// lease names a dead process.
	//
	// An unidentifiable process cannot own a lease: without a start token the
	// reaper would be left trusting a bare PID, which is the one thing a
	// process lease must never do.
	selfPID := os.Getpid()
	selfToken, err := e.Prober.Identity(selfPID)
	if err != nil {
		return 0, fmt.Errorf("cannot identify this process (pid %d), so a process lease could not be verified later: %w", selfPID, err)
	}
	if selfToken == "" {
		return 0, fmt.Errorf("cannot identify this process (pid %d): the prober returned no identity token", selfPID)
	}

	e.announceSelection(sel)
	e.warnLaunchedAgentExcluded(sel, launched)
	results, err := e.spawnAll(ctx, spawn.Request{
		Loadout: l,
		Global:  global,
		Cwd:     e.Cwd,
		Kind:    lease.KindProcess,
		Owner: &lease.Owner{
			PID:        selfPID,
			StartToken: selfToken,
			Command:    "barracks run",
		},
	}, sel.Targets)
	if err != nil {
		return 0, err
	}
	leases := make([]*lease.Lease, 0, len(results))
	for i, res := range results {
		printSpawn(e, res, sel.Targets[i])
		leases = append(leases, res.Lease)
	}

	code, runErr := e.runChild(argv, leases, in, out, errw)

	for _, spawned := range leases {
		// The record can be rewritten while the session runs: `barracks
		// upgrade --include-running` relinks a live spawn and saves the new
		// targets. Recalling from the copy captured at spawn time would
		// compare the relinked symlink against a stale target, refuse to
		// remove a link barracks itself created, and leave the repository
		// dirty. So the record is re-read here, at the moment of revoke,
		// and never any earlier: a rewrite can land at any point until then.
		rec, confirmed := spawned, true
		if fresh, err := e.leases.Get(spawned.ID); err == nil {
			rec = fresh
		} else {
			confirmed = false
			fmt.Fprintf(e.Err, "! could not re-read the lease record: %v\n", err)
			fmt.Fprintln(e.Err, "! recalling from the copy this session started with, and keeping the record so the next barracks command can finish the job")
		}

		// Revoking from the captured copy removes nothing it cannot prove:
		// InspectLink compares against the recorded target, so a stale copy
		// keeps a path rather than deleting the wrong one. That makes the
		// fallback safe but possibly incomplete, which is why the record is
		// not deleted with it - it is the only thing a later reap can
		// finish the recall from.
		records := e.leases
		headline := "left in place (barracks did not create it)"
		if !confirmed {
			records = nil
			headline = "left for the next reap (the lease record could not be re-read, so this path could not be confirmed)"
		}
		rep := lease.Revoke(rec, e.store, records, "command exited")
		fmt.Fprintf(e.Out, "recalled %s from %s (%s, %d %s)\n",
			rec.Loadout, rec.Dir, displayOf(rec.Target),
			len(rep.Removed), plural(len(rep.Removed), "skill", "skills"))
		reportKeptAs(e.Err, rep, headline)
	}

	return code, runErr
}

// runChild starts argv, hands every lease over to it, forwards interrupts, and
// waits. It returns the child's exit code.
func (e *Env) runChild(argv []string, leases []*lease.Lease, in io.Reader, out, errw io.Writer) (int, error) {
	child := exec.Command(argv[0], argv[1:]...)
	child.Stdin = in
	child.Stdout = out
	child.Stderr = errw
	child.Dir = e.Cwd

	if err := child.Start(); err != nil {
		return 0, fmt.Errorf("run %s: %w", argv[0], err)
	}

	for _, l := range leases {
		e.handOverLease(l, child.Process.Pid, argv[0])
	}

	// Forward interrupts rather than dying on them, so the deferred recall in
	// the caller always gets to run.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigs)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-sigs:
				if child.Process != nil {
					_ = child.Process.Signal(s)
				}
			case <-done:
				return
			}
		}
	}()

	err := child.Wait()
	close(done)

	if err != nil {
		var ee *exec.ExitError
		if ok := asExecExitError(err, &ee); ok {
			return ee.ExitCode(), nil
		}
		return 0, fmt.Errorf("run %s: %w", argv[0], err)
	}
	return 0, nil
}

// handOverLease retargets the lease at the child process. The recorded start
// token is what makes the handover safe against PID reuse later.
func (e *Env) handOverLease(l *lease.Lease, pid int, command string) {
	token, err := e.Prober.Identity(pid)
	if err != nil || token == "" {
		// Could not identify the child; the lease stays owned by this process,
		// which still ends the lease correctly when the run finishes.
		return
	}
	l.Owner = &lease.Owner{PID: pid, StartToken: token, Command: command}
	if err := e.leases.Save(l); err != nil {
		fmt.Fprintf(e.Err, "! could not record process owner: %v\n", err)
	}
}
