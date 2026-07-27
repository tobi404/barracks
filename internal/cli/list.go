package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tobi404/barracks/internal/target"
)

func newListCmd(env *Env) *cobra.Command {
	var verbose bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List every loadout in the barracks",
		Long: strings.TrimSpace(`
Lists every loadout you have trained, with its sources and skill count.

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

func newTargetsCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "targets",
		Short: "List the agents barracks can deploy to",
		Long: strings.TrimSpace(`
Lists every agent barracks knows how to deploy to, and where each one keeps its
skills. Pass one of these IDs to spawn or run with --target.

  barracks targets`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, t := range target.Registry {
				global, err := t.GlobalPath(env.Getenv, env.Home)
				if err != nil {
					global = "(unresolvable: " + err.Error() + ")"
				}
				marker := " "
				if t.ID == target.DefaultID {
					marker = "*"
				}
				fmt.Fprintf(env.Out, "%s %-10s %s\n", marker, t.ID, t.Display)
				fmt.Fprintf(env.Out, "    in repo:  <repo>/%s\n", t.RepoDir)
				fmt.Fprintf(env.Out, "    global:   %s\n", global)
			}
			fmt.Fprintf(env.Out, "\n* default target\n")
			return nil
		},
	}
}
