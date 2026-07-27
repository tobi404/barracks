package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/tobi404/barracks/internal/lease"
	"github.com/tobi404/barracks/internal/spawn"
	"github.com/tobi404/barracks/internal/target"
)

func newSpawnCmd(env *Env) *cobra.Command {
	var (
		forDur   time.Duration
		global   bool
		targetID string
	)

	cmd := &cobra.Command{
		Use:   "spawn <loadout>",
		Short: "Spawn a loadout into the current repo",
		Long: strings.TrimSpace(`
Materialises a loadout's skills into the agent's skills directory.

Skills are symlinked from the shared store, so spawning is instant and every
repo on your machine shares one copy on disk. The created paths are registered
in .git/info/exclude - never in the committed .gitignore - so git status stays
clean.

By default the spawn lasts until you recall it. Give it a deadline with --for,
and it is removed automatically by whichever barracks command runs next after
the clock passes:

  barracks spawn frontend
  barracks spawn frontend --for 2h
  barracks spawn frontend --global
  barracks spawn frontend --target opencode

Use --global to install into the agent's user-level skills directory instead of
the current repo. To tie a spawn to a single command's lifetime, use
barracks run.`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env.reap()

			l, err := env.loadouts.Get(args[0])
			if err != nil {
				return err
			}
			tgt, err := target.Lookup(targetID)
			if err != nil {
				return err
			}

			req := spawn.Request{
				Loadout: l,
				Target:  tgt,
				Global:  global,
				Cwd:     env.Cwd,
				Kind:    lease.KindManual,
			}
			if forDur > 0 {
				req.Kind = lease.KindDeadline
				req.Duration = forDur
			}

			res, err := env.engine.Spawn(cmd.Context(), req)
			if err != nil {
				return err
			}
			printSpawn(env, res, tgt)
			return nil
		},
	}
	cmd.Flags().DurationVar(&forDur, "for", 0, "recall automatically after this long (e.g. 90m, 2h)")
	cmd.Flags().BoolVar(&global, "global", false, "spawn into the agent's user-level skills directory")
	cmd.Flags().StringVar(&targetID, "target", target.DefaultID, targetFlagHelp())
	return cmd
}

func printSpawn(env *Env, res *spawn.Result, tgt target.Target) {
	l := res.Lease
	fmt.Fprintf(env.Out, "spawned %s into %s (%s, %s)\n", l.Loadout, l.Dir, tgt.Display, l.Describe(env.now()))
	for _, s := range res.Skills {
		fmt.Fprintf(env.Out, "  + %s\n", s.Name)
	}
	if res.Fetched > 0 {
		fmt.Fprintf(env.Out, "  (%d %s fetched into the store)\n", res.Fetched, plural(res.Fetched, "source", "sources"))
	}
	for _, n := range res.Notices {
		fmt.Fprintf(env.Err, "! %s\n", n)
	}
}
