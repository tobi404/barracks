package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tobi404/barracks/internal/garrison"
	"github.com/tobi404/barracks/internal/lease"
)

// first is the loadout name a command was given, or "" when it was given none.
func first(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func newRecallCmd(env *Env) *cobra.Command {
	var (
		global    bool
		targetIDs []string
		all       bool
	)

	cmd := &cobra.Command{
		Use:   "recall [loadout]",
		Short: "Recall a spawned loadout from the current repo",
		Long: strings.TrimSpace(`
Removes a spawned loadout, leaving the repo exactly as it was.

A loadout spawned into two agents is recalled from both by one command, because
it was one spawn. Narrow that with --target when you want to leave one behind.

Recall removes only the symlinks the spawn recorded, and only after confirming
each is still a symlink pointing into the barracks store. Anything else - a
real file, a directory, a symlink you re-pointed - is left alone and reported.
A skill directory you made yourself cannot be destroyed by a recall.

The .git/info/exclude entries are removed too, so the repo goes back byte for
byte.

A loadout garrisoned into this repository is recalled too, because it is
deployed here just as much as a spawn is. Its committed files are removed only
where they still match the digest barracks.lock recorded; a file edited since it
was committed is kept and reported. The removal is a change to tracked files, so
it shows up in git status for review like any other.

  barracks recall frontend
  barracks recall frontend --target cursor
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
			filter, err := resolveTargetFilter(targetIDs)
			if err != nil {
				return err
			}
			loc, err := env.scopeOf(cmd.Context(), global)
			if err != nil {
				return err
			}
			// A recall undoes a spawn, so it is scoped the same way - see
			// actedIn.
			env.actedIn(loc)

			leases, problems := env.leases.List()
			for _, p := range problems {
				fmt.Fprintf(env.Err, "! %v\n", p)
			}
			here := lease.WithTargets(lease.FindInScope(leases, loc.Scope, loc.Root), filter)

			var matched []*lease.Lease
			for _, l := range here {
				if all || l.Loadout == args[0] {
					matched = append(matched, l)
				}
			}
			// A garrison is held by no lease and has no per-target record to
			// narrow by: it is one committed unit, so a --target or --global
			// recall leaves it alone rather than half-removing it.
			var garrisoned []garrison.Ref
			if len(filter) == 0 && !global {
				garrisoned = env.garrisonsHere(loc.Root, first(args), all)
			}

			if len(matched) == 0 && len(garrisoned) == 0 {
				where := scopeLabel(loc, global) + targetSuffix(filter)
				if all {
					return fmt.Errorf("nothing is deployed %s", where)
				}
				return fmt.Errorf("%s is not deployed %s", args[0], where)
			}
			for _, ref := range garrisoned {
				rep, err := env.garrisons.Remove(loc.Root, ref)
				if err != nil {
					return err
				}
				printGarrisonRemoval(env, rep)
			}

			for _, l := range matched {
				rep := lease.Revoke(l, env.store, env.leases, "recalled")
				fmt.Fprintf(env.Out, "recalled %s from %s (%s, %d %s)\n",
					l.Loadout, l.Dir, displayOf(l.Target),
					len(rep.Removed), plural(len(rep.Removed), "skill", "skills"))
				reportKept(env.Err, rep)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&global, "global", false, "recall from each agent's user-level skills directory")
	cmd.Flags().StringSliceVar(&targetIDs, "target", nil, targetFlagHelp("recall from")+"; default every agent")
	cmd.Flags().BoolVar(&all, "all", false, "recall every loadout deployed here")
	return cmd
}
