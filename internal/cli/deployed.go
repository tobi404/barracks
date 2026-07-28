package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tobi404/barracks/internal/lease"
)

func newDeployedCmd(env *Env) *cobra.Command {
	var (
		everywhere bool
		global     bool
		targetIDs  []string
	)

	cmd := &cobra.Command{
		Use:   "deployed",
		Short: "Show what is currently spawned here",
		Long: strings.TrimSpace(`
Shows the loadouts currently deployed in this repo, which agent each one went
into, and how each one ends. The same loadout spawned into two agents shows up
once per agent, so it is always clear which is which.

Every barracks command reaps expired leases first, so what this prints is
already up to date - a deadline that has passed or a run whose process exited
will have been cleaned up before the list is drawn.

Use --everywhere to see every live spawn on the machine, including global ones
and other repos.

  barracks deployed
  barracks deployed --target cursor
  barracks deployed --global
  barracks deployed --everywhere`),
		Aliases: []string{"status"},
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			env.reap()

			leases, problems := env.leases.List()
			for _, p := range problems {
				fmt.Fprintf(env.Err, "! %v\n", p)
			}
			filter, err := resolveTargetFilter(targetIDs)
			if err != nil {
				return err
			}

			var shown []*lease.Lease
			var where string
			if everywhere {
				shown, where = leases, "on this machine"
			} else {
				loc, err := env.scopeOf(cmd.Context(), global)
				if err != nil {
					return err
				}
				shown = lease.FindInScope(leases, loc.Scope, loc.Root)
				where = scopeLabel(loc, global)
			}
			shown = lease.WithTargets(shown, filter)
			where += targetSuffix(filter)

			if len(shown) == 0 {
				fmt.Fprintf(env.Out, "nothing deployed %s\n", where)
				return nil
			}
			for _, l := range shown {
				fmt.Fprintf(env.Out, "%s  %d %s  [%s]  %s\n",
					l.Loadout, len(l.Links), plural(len(l.Links), "skill", "skills"),
					l.Kind, l.Describe(env.now()))
				fmt.Fprintf(env.Out, "    target: %s (%s)\n", l.Target, displayOf(l.Target))
				fmt.Fprintf(env.Out, "    %s: %s\n", l.Scope, l.Dir)
				fmt.Fprintf(env.Out, "    lease: %s\n", l.ID)
				for _, link := range l.Links {
					fmt.Fprintf(env.Out, "      %s  <- %s\n", link.Skill, link.Source)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&everywhere, "everywhere", false, "show every live spawn on this machine")
	cmd.Flags().BoolVar(&global, "global", false, "show spawns in your user-level skills directories")
	cmd.Flags().StringSliceVar(&targetIDs, "target", nil, targetFlagHelp("show")+"; default every agent")
	return cmd
}
