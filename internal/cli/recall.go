package cli

import (
	"bufio"
	"errors"
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
		yes       bool
	)

	cmd := &cobra.Command{
		Use:   "recall [loadout]",
		Short: "Recall a loadout from the current repo, spawned or committed",
		Long: strings.TrimSpace(`
Removes a loadout from this repository: its spawns, leaving the repo
exactly as it was, and its garrison if it has one here.

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

Removing committed files is asked about first: on a terminal, recall says how
many files it will remove and waits for a yes. Anywhere else - a script, a pipe,
CI - it refuses rather than guessing, and --yes is how a script says it meant it.
A recall that only touches spawns never asks.

  barracks recall frontend
  barracks recall frontend --yes
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
			// Asked before anything is touched, so a "no" - or a script that
			// never said yes - leaves both tiers exactly as they were.
			if len(garrisoned) > 0 && !yes {
				if err := env.confirmGarrisonRemoval(loc.Root, garrisoned); err != nil {
					return err
				}
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
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "remove a committed garrison without asking")
	return cmd
}

// errNotConfirmed is a garrison removal nobody said yes to. It is an error, not
// a quiet success, because nothing was recalled: the exit status has to say so,
// and cobra skipping PersistentPostRun on an error is what keeps a flavor line
// from following a command that changed nothing.
var errNotConfirmed = errors.New("nothing was recalled")

// confirmGarrisonRemoval asks before a recall removes committed files.
//
// A spawn is barracks' own symlinks and comes back with one command; a garrison
// is tracked files in somebody's checkout, and the roster will not remove one
// without the loadout's name typed out in full. The command asks too, rather
// than being the unguarded way round that card. Only a person at a terminal is
// asked: off one there is nobody to answer, so the command refuses and names the
// flag that says the removal was meant.
func (e *Env) confirmGarrisonRemoval(root string, refs []garrison.Ref) error {
	files := committedFiles(root, refs)
	names := make([]string, len(refs))
	for i, r := range refs {
		names[i] = r.Loadout
	}
	what := fmt.Sprintf("remove %d committed %s and rewrite %s",
		files, plural(files, "file", "files"), garrison.LockName)
	if !e.canAsk() {
		return fmt.Errorf("recalling the %s %s would %s; pass --yes to do that without a terminal to confirm on",
			strings.Join(names, ", "), plural(len(refs), "garrison", "garrisons"), what)
	}
	fmt.Fprintf(e.Out, "%s %s: %s? [y/N] ",
		strings.Join(names, ", "), plural(len(refs), "garrison", "garrisons"), what)
	answer, err := bufio.NewReader(e.In).ReadString('\n')
	if err != nil && answer == "" {
		// No answer at all is not a yes. The line the prompt left open is
		// closed, so whatever is printed next starts on a line of its own.
		fmt.Fprintln(e.Out)
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return nil
	}
	return errNotConfirmed
}

// committedFiles is how many files the lockfile records for these garrisons -
// what a removal would take out if nobody had edited any of them.
func committedFiles(root string, refs []garrison.Ref) int {
	m, err := garrison.Load(root)
	if err != nil {
		return 0
	}
	n := 0
	for _, r := range refs {
		if g := m.FindFor(r.ID, r.Loadout); g != nil {
			n += g.FileCount()
		}
	}
	return n
}
