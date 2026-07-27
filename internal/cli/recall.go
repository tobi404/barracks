package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tobi404/barracks/internal/lease"
	"github.com/tobi404/barracks/internal/spawn"
	"github.com/tobi404/barracks/internal/target"
)

func newRecallCmd(env *Env) *cobra.Command {
	var (
		global   bool
		targetID string
		all      bool
	)

	cmd := &cobra.Command{
		Use:   "recall [loadout]",
		Short: "Recall a spawned loadout from the current repo",
		Long: strings.TrimSpace(`
Removes a spawned loadout, leaving the repo exactly as it was.

Recall removes only the symlinks the spawn recorded, and only after confirming
each is still a symlink pointing into the barracks store. Anything else - a
real file, a directory, a symlink you re-pointed - is left alone and reported.
A skill directory you made yourself cannot be destroyed by a recall.

The .git/info/exclude entries are removed too, so the repo goes back byte for
byte.

  barracks recall frontend
  barracks recall frontend --global
  barracks recall --all`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env.reap()

			if !all && len(args) == 0 {
				return fmt.Errorf("name a loadout to recall, or pass --all")
			}
			// Recall is the one command whose job is removal, so an ambiguous
			// invocation is refused rather than interpreted: guessing wrong
			// takes away more than was asked for.
			if all && len(args) > 0 {
				return fmt.Errorf("cannot combine the loadout name %q with --all: use `barracks recall %s` to recall just that loadout, or `barracks recall --all` to recall every loadout deployed here", args[0], args[0])
			}
			tgt, err := target.Lookup(targetID)
			if err != nil {
				return err
			}
			loc, err := env.engine.Resolve(cmd.Context(), spawn.Request{Target: tgt, Global: global, Cwd: env.Cwd})
			if err != nil {
				return err
			}

			leases, problems := env.leases.List()
			for _, p := range problems {
				fmt.Fprintf(env.Err, "! %v\n", p)
			}
			here := lease.FindInDir(leases, loc.Dir)

			var matched []*lease.Lease
			for _, l := range here {
				if all || l.Loadout == args[0] {
					matched = append(matched, l)
				}
			}
			if len(matched) == 0 {
				if all {
					return fmt.Errorf("nothing is deployed in %s", loc.Dir)
				}
				return fmt.Errorf("%s is not deployed in %s", args[0], loc.Dir)
			}

			for _, l := range matched {
				rep := lease.Revoke(l, env.store, env.leases, "recalled")
				fmt.Fprintf(env.Out, "recalled %s from %s (%d %s)\n",
					l.Loadout, l.Dir, len(rep.Removed), plural(len(rep.Removed), "skill", "skills"))
				reportKept(env.Err, rep)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&global, "global", false, "recall from the agent's user-level skills directory")
	cmd.Flags().StringVar(&targetID, "target", target.DefaultID, targetFlagHelp())
	cmd.Flags().BoolVar(&all, "all", false, "recall every loadout deployed here")
	return cmd
}
