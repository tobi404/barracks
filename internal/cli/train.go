package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func newTrainCmd(env *Env) *cobra.Command {
	var (
		description string
		targetIDs   []string
	)

	cmd := &cobra.Command{
		Use:   "train <name>",
		Short: "Train a new loadout",
		Long: strings.TrimSpace(`
Trains a new, empty loadout.

A loadout is a named bundle of agent skills. Training one only creates its
definition - it has no skills until you equip it with a source, and it does
nothing to any repo until you spawn it.

Use --target to declare which agents it installs into. A loadout that declares
nothing is detected per repository at spawn time, and the declaration can be
changed later with barracks assign.

The definition is a plain YAML file you are welcome to open and edit by hand.

  barracks train frontend
  barracks train review --description "Skills for reviewing pull requests"
  barracks train editor --target cursor --target windsurf`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env.reap()
			name := args[0]
			// Resolve before creating: a loadout left holding a target barracks
			// cannot resolve would only fail later, at spawn time.
			declared, err := declaredIDs(targetIDs)
			if err != nil {
				return err
			}
			l, err := env.loadouts.Create(name, description, env.now())
			if err != nil {
				return err
			}
			if len(declared) > 0 {
				l.SetTargets(declared)
				if err := env.loadouts.Save(l); err != nil {
					return err
				}
			}
			fmt.Fprintf(env.Out, "trained loadout %q\n", l.Name)
			printAssignment(env, l)
			fmt.Fprintf(env.Out, "  equip it with:  barracks equip %s gh:owner/repo\n", l.Name)
			return nil
		},
	}
	cmd.Flags().StringVarP(&description, "description", "d", "", "what this loadout is for")
	cmd.Flags().StringSliceVar(&targetIDs, "target", nil, targetFlagHelp("install into"))
	return cmd
}

func newDisbandCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "disband <name>",
		Short: "Delete a loadout definition",
		Long: strings.TrimSpace(`
Deletes a loadout's definition.

Disbanding refuses while the loadout is still spawned anywhere, or garrisoned in
this repository - recall it first. Nothing is removed from the shared store, so
other loadouts using the same sources are unaffected.

  barracks disband frontend`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env.reap()
			name := args[0]
			leases, _ := env.leases.List()
			for _, l := range leases {
				if l.Loadout == name {
					return fmt.Errorf("loadout %q is still deployed in %s; recall it first", name, l.Dir)
				}
			}
			// A garrison keeps working without the definition - the lockfile
			// records everything it needs - but deleting the definition of
			// something visibly deployed here would be a surprise, and it is the
			// definition an update reads from.
			if loc, err := env.scopeOf(cmd.Context(), false); err == nil && loc.Root != "" {
				if len(env.garrisonsHere(loc.Root, name, false)) > 0 {
					return fmt.Errorf("loadout %q is garrisoned in %s; recall it first", name, loc.Root)
				}
			}
			if err := env.loadouts.Delete(name); err != nil {
				return err
			}
			fmt.Fprintf(env.Out, "disbanded loadout %q\n", name)
			return nil
		},
	}
}
