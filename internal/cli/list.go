package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func newListCmd(env *Env) *cobra.Command {
	var verbose bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List every loadout in the barracks",
		Long: strings.TrimSpace(`
Lists every loadout you have trained, with its sources, skill count, and the
agents it installs into.

Use --verbose to see each source's pinned commit and the skills it contributes.

  barracks list
  barracks list --verbose`),
		Aliases: []string{"ls"},
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			env.reap()
			loadouts, problems := env.loadouts.List()
			for _, p := range problems {
				fmt.Fprintf(env.Err, "! %v\n", p)
			}
			if len(loadouts) == 0 {
				fmt.Fprintln(env.Out, "no loadouts trained yet - start with: barracks train <name>")
				return nil
			}
			for _, l := range loadouts {
				fmt.Fprintf(env.Out, "%s  (%d %s from %d %s)",
					l.Name,
					l.SkillCount(), plural(l.SkillCount(), "skill", "skills"),
					len(l.Equipment), plural(len(l.Equipment), "source", "sources"))
				if l.Description != "" {
					fmt.Fprintf(env.Out, "  - %s", l.Description)
				}
				fmt.Fprintln(env.Out)
				if len(l.Targets) > 0 {
					fmt.Fprintf(env.Out, "    -> %s\n", strings.Join(l.Targets, ", "))
				} else {
					fmt.Fprintf(env.Out, "    -> detected per repository\n")
				}
				for _, eq := range l.Equipment {
					fmt.Fprintf(env.Out, "    %s@%s\n", eq.Ident(), shortSHA(eq.Commit))
					if verbose {
						for _, s := range eq.Skills {
							fmt.Fprintf(env.Out, "      + %s\n", s)
						}
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "list each source's skills")
	return cmd
}
