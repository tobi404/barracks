package cli

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/tobi404/barracks/internal/lease"
	"github.com/tobi404/barracks/internal/spawn"
	"github.com/tobi404/barracks/internal/target"
)

func newRunCmd(env *Env) *cobra.Command {
	var (
		global   bool
		targetID string
	)

	cmd := &cobra.Command{
		Use:   "run <loadout> -- <command> [args...]",
		Short: "Spawn a loadout, run a command, recall on exit",
		Long: strings.TrimSpace(`
Spawns a loadout, runs a command with those skills available, and recalls the
loadout the moment the command exits.

This is the throwaway-session case: the skills exist for exactly as long as the
process does, and nothing is left behind afterwards.

  barracks run frontend -- claude
  barracks run review -- claude -p "review this diff"

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
			tgt, err := target.Lookup(targetID)
			if err != nil {
				return err
			}

			// The lease is born owned by this process, which is certainly
			// alive, and handed to the child once it starts. There is never a
			// moment where a live lease names a dead process.
			selfPID := os.Getpid()
			selfToken, _ := env.Prober.Identity(selfPID)

			res, err := env.engine.Spawn(cmd.Context(), spawn.Request{
				Loadout: l,
				Target:  tgt,
				Global:  global,
				Cwd:     env.Cwd,
				Kind:    lease.KindProcess,
				Owner: &lease.Owner{
					PID:        selfPID,
					StartToken: selfToken,
					Command:    "barracks run",
				},
			})
			if err != nil {
				return err
			}
			printSpawn(env, res, tgt)

			code, runErr := env.runChild(argv, res.Lease)

			rep := lease.Revoke(res.Lease, env.store, env.leases, "command exited")
			fmt.Fprintf(env.Out, "recalled %s from %s (%d %s)\n",
				res.Lease.Loadout, res.Lease.Dir, len(rep.Removed), plural(len(rep.Removed), "skill", "skills"))
			reportKept(env.Err, rep)

			if runErr != nil {
				return runErr
			}
			if code != 0 {
				return &ExitError{Code: code}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&global, "global", false, "spawn into the agent's user-level skills directory")
	cmd.Flags().StringVar(&targetID, "target", target.DefaultID, targetFlagHelp())
	return cmd
}

// runChild starts argv, hands the lease over to it, forwards interrupts, and
// waits. It returns the child's exit code.
func (e *Env) runChild(argv []string, l *lease.Lease) (int, error) {
	child := exec.Command(argv[0], argv[1:]...)
	child.Stdin = os.Stdin
	child.Stdout = e.Out
	child.Stderr = e.Err
	child.Dir = e.Cwd

	if err := child.Start(); err != nil {
		return 0, fmt.Errorf("run %s: %w", argv[0], err)
	}

	e.handOverLease(l, child.Process.Pid, argv[0])

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
	if err != nil {
		// Could not identify the child; the lease stays owned by this process,
		// which still ends the lease correctly when the run finishes.
		return
	}
	l.Owner = &lease.Owner{PID: pid, StartToken: token, Command: command}
	if err := e.leases.Save(l); err != nil {
		fmt.Fprintf(e.Err, "! could not record process owner: %v\n", err)
	}
}
