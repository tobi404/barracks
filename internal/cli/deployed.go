package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tobi404/barracks/internal/lease"
	"github.com/tobi404/barracks/internal/spawn"
	"github.com/tobi404/barracks/internal/target"
)

func newDeployedCmd(env *Env) *cobra.Command {
	var (
		everywhere bool
		targetID   string
	)

	cmd := &cobra.Command{
		Use:   "deployed",
		Short: "Show what is currently spawned here",
		Long: strings.TrimSpace(`
Shows the loadouts currently deployed in this repo, and how each one ends.

Every barracks command reaps expired leases first, so what this prints is
already up to date - a deadline that has passed or a run whose process exited
will have been cleaned up before the list is drawn.

Use --everywhere to see every live spawn on the machine, including global ones
and other repos.

  barracks deployed
  barracks deployed --everywhere`),
		Aliases: []string{"status"},
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			env.reap()

			leases, problems := env.leases.List()
			for _, p := range problems {
				fmt.Fprintf(env.Err, "! %v\n", p)
			}

			var shown []*lease.Lease
			var scopeLabel string
			if everywhere {
				shown, scopeLabel = leases, "on this machine"
			} else {
				tgt, err := target.Lookup(targetID)
				if err != nil {
					return err
				}
				loc, err := env.engine.Resolve(cmd.Context(), spawn.Request{Target: tgt, Cwd: env.Cwd})
				if err != nil {
					return err
				}
				shown = lease.FindInDir(leases, loc.Dir)
				scopeLabel = "in " + loc.Dir
			}

			if len(shown) == 0 {
				fmt.Fprintf(env.Out, "nothing deployed %s\n", scopeLabel)
				return nil
			}
			for _, l := range shown {
				fmt.Fprintf(env.Out, "%s  %d %s  [%s]  %s\n",
					l.Loadout, len(l.Links), plural(len(l.Links), "skill", "skills"),
					l.Kind, l.Describe(env.now()))
				fmt.Fprintf(env.Out, "    %s: %s\n", l.Scope, l.Dir)
				fmt.Fprintf(env.Out, "    target: %s   lease: %s\n", l.Target, l.ID)
				for _, link := range l.Links {
					fmt.Fprintf(env.Out, "      %s  <- %s\n", link.Skill, link.Source)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&everywhere, "everywhere", false, "show every live spawn on this machine")
	cmd.Flags().StringVar(&targetID, "target", target.DefaultID, targetFlagHelp())
	return cmd
}
