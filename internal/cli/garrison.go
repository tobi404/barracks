package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tobi404/barracks/internal/garrison"
	"github.com/tobi404/barracks/internal/loadout"
	"github.com/tobi404/barracks/internal/spawn"
)

// repoScope resolves the repository a committed-tier command acts on.
//
// The committed tier only makes sense inside a repository: its whole purpose is
// that a clone carries the skills, and there is nothing to clone without one.
func (e *Env) repoScope(ctx context.Context) (spawn.Location, error) {
	loc, err := e.scopeOf(ctx, false)
	if err != nil {
		return spawn.Location{}, err
	}
	if loc.GitDir == "" {
		return spawn.Location{}, fmt.Errorf("barracks garrison needs a git repository: a committed loadout is how a team shares one, and there is nothing here to commit it to.\nUse `barracks spawn` for a personal, uncommitted install")
	}
	return loc, nil
}

func newGarrisonCmd(env *Env) *cobra.Command {
	var (
		targetIDs []string
		force     bool
	)

	cmd := &cobra.Command{
		Use:   "garrison [loadout]",
		Short: "Commit a loadout into this repository for the whole team",
		Long: strings.TrimSpace(`
Stations a loadout in this repository permanently: real skill files, committed,
plus a barracks.lock recording exactly which sources and commits produced them.

This is the shared tier. A teammate clones the repository and their agent sees
the skills immediately - no barracks installed, nothing to set up. Contrast
barracks spawn, which is personal: symlinks into your own store, kept out of git
and governed by a lease with a lifetime.

  barracks garrison frontend            commit it, or bring it onto new pins
  barracks garrison                     put every garrison in barracks.lock back
  barracks garrison frontend --target cursor --target claude

Commit the skill files and barracks.lock together. They are not registered in
.git/info/exclude - the whole point is that git tracks them - so review the diff
and commit it as you would any other change.

Running it again on an already-garrisoned loadout is how an update happens: the
files and barracks.lock are rewritten together, so the change arrives as one
reviewable diff. Nothing is written until every check passes.

A vendored file you have edited since it was committed stops the update, naming
the file: barracks will not discard your edit, and will not leave barracks.lock
claiming content the file does not have. Restore it (git checkout -- <path>) or
pass --force to take the recorded source content anyway. A file barracks never
wrote is refused outright, and --force does not apply to it.

With no loadout named, every garrison barracks.lock records is materialised
again from the lockfile alone - the repair path, which needs no loadout trained
on this machine. Use barracks inspect to see whether a checkout needs it.`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env.reap()

			loc, err := env.repoScope(cmd.Context())
			if err != nil {
				return err
			}
			// The committed tier is a repository's own, so the flavor line's
			// escalation is too - see actedIn.
			env.actedIn(loc)

			if len(args) == 0 {
				if len(targetIDs) > 0 {
					return fmt.Errorf("--target needs a loadout to apply to: name one, or run `barracks garrison` alone to restore every garrison exactly as %s records it", garrison.LockName)
				}
				results, err := env.garrisons.Restore(cmd.Context(), loc.Root, loc.GitDir, nil, force)
				for _, res := range results {
					printGarrison(env, res)
				}
				return err
			}

			l, err := env.loadouts.Get(args[0])
			if err != nil {
				return err
			}
			sel, err := env.selectTargetsFor(cmd.Context(), l, targetIDs, false, nil)
			if err != nil {
				return err
			}
			// An existing garrison's recorded targets win over detection: an
			// update must not quietly stop installing into an agent the
			// repository already committed files for.
			if existing := env.garrisonedTargets(loc.Root, l); len(existing) > 0 && len(targetIDs) == 0 && len(l.Targets) == 0 {
				sel, err = env.selectTargetsFor(cmd.Context(), l, existing, false, nil)
				if err != nil {
					return err
				}
				fmt.Fprintf(env.Out, "targets: %s (recorded in %s)\n", strings.Join(sel.IDs(), ", "), garrison.LockName)
			} else {
				env.announceSelection(sel)
			}

			res, err := env.garrisons.Install(cmd.Context(), garrison.Request{
				Root:      loc.Root,
				GitDir:    loc.GitDir,
				Name:      l.Name,
				ID:        l.ID,
				Equipment: l.Equipment,
				Targets:   sel.Targets,
				Force:     force,
			})
			if err != nil {
				return err
			}
			printGarrison(env, res)
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&targetIDs, "target", nil, targetFlagHelp("garrison into"))
	cmd.Flags().BoolVar(&force, "force", false, "replace vendored files that have been edited since they were committed")
	return cmd
}

// garrisonedTargets is the target list the lockfile records for a loadout.
func (e *Env) garrisonedTargets(root string, l *loadout.Loadout) []string {
	m, err := garrison.Load(root)
	if err != nil {
		return nil
	}
	if g := m.FindFor(l.ID, l.Name); g != nil {
		return g.Targets
	}
	return nil
}

func printGarrison(env *Env, res *garrison.Result) {
	verb := "garrisoned"
	if !res.New {
		verb = "updated garrison"
	}
	fmt.Fprintf(env.Out, "%s %s in %s (%s)\n", verb, res.Loadout,
		strings.Join(res.Skills, ", "), strings.Join(res.Targets, ", "))
	for _, p := range res.Wrote {
		fmt.Fprintf(env.Out, "  + %s\n", p)
	}
	for _, p := range res.Deleted {
		fmt.Fprintf(env.Out, "  - %s\n", p)
	}
	if len(res.Unchanged) > 0 {
		fmt.Fprintf(env.Out, "  (%d %s already up to date)\n",
			len(res.Unchanged), plural(len(res.Unchanged), "file", "files"))
	}
	if res.Fetched > 0 {
		fmt.Fprintf(env.Out, "  (%d %s fetched into the store)\n", res.Fetched, plural(res.Fetched, "source", "sources"))
	}
	fmt.Fprintf(env.Out, "  wrote %s\n", garrison.LockName)
	if res.Changed() {
		fmt.Fprintf(env.Out, "  commit these files and %s together\n", garrison.LockName)
	}
	for _, n := range res.Notices {
		fmt.Fprintf(env.Err, "! %s\n", n)
	}
}

// garrisonsHere is the garrisons a command in this repository should act on:
// every one when all, otherwise the named loadout if it is garrisoned.
//
// The named lookup goes through the lockfile's own matching rather than
// comparing names, so a loadout renamed while this repository was not the one
// standing in front of barracks is still recognised by the identity its entry
// carries.
func (e *Env) garrisonsHere(root, name string, all bool) []garrison.Ref {
	if root == "" {
		return nil
	}
	m, err := garrison.Load(root)
	if err != nil {
		fmt.Fprintf(e.Err, "! %v\n", err)
		return nil
	}
	if all {
		return m.Refs()
	}
	if name == "" {
		return nil
	}
	var id string
	if l, lerr := e.loadouts.Get(name); lerr == nil {
		id = l.ID
	}
	if g := m.FindFor(id, name); g != nil {
		return []garrison.Ref{{ID: g.ID, Loadout: g.Loadout}}
	}
	return nil
}

func printGarrisonRemoval(env *Env, rep *garrison.Report) {
	fmt.Fprintf(env.Out, "recalled the %s garrison (%d %s removed, %s updated)\n",
		rep.Loadout, len(rep.Removed), plural(len(rep.Removed), "file", "files"), garrison.LockName)
	for _, k := range rep.Kept {
		fmt.Fprintf(env.Err, "! left in place: %s - %s\n", k.Path, k.Reason)
	}
	for _, err := range rep.Errors {
		fmt.Fprintf(env.Err, "! %v\n", err)
	}
}

func newInspectCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "inspect",
		Short: "Check this checkout against barracks.lock",
		Long: strings.TrimSpace(`
Verifies that the committed skill files in this repository are exactly what
barracks.lock says they should be.

barracks.lock records a digest for every file it wrote, so a file that was
edited, dropped in a rebase, half-merged, or added by hand inside a vendored
skill directory is reported rather than silently accepted.

  barracks inspect

It changes nothing - that is the point, and it is what makes it safe to run in
CI. Run barracks garrison to put a drifted checkout back.

A note about the loadout being pinned somewhere newer than barracks.lock is not
a mismatch: the files and the lockfile agree, which is exactly what a teammate
cloning this repository should get. It means the committed pins are behind the
loadout, and bringing them forward is a commit like any other.

Exits non-zero when anything does not match, so it can gate a build.`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			env.reap()

			loc, err := env.scopeOf(cmd.Context(), false)
			if err != nil {
				return err
			}
			insp, err := env.garrisons.Inspect(loc.Root)
			if err != nil {
				return err
			}
			if len(insp.Checks) == 0 {
				fmt.Fprintf(env.Out, "no %s here: nothing is garrisoned in this repository\n", garrison.LockName)
				return nil
			}

			for _, c := range insp.Checks {
				g := c.Garrison
				state := "ok"
				if !c.OK() {
					state = fmt.Sprintf("%d %s", len(c.Findings), plural(len(c.Findings), "problem", "problems"))
				}
				fmt.Fprintf(env.Out, "%s  %d %s, %d %s  [%s]  %s\n",
					g.Loadout, g.SkillCount(), plural(g.SkillCount(), "skill", "skills"),
					g.FileCount(), plural(g.FileCount(), "file", "files"),
					strings.Join(g.Targets, ", "), state)
				// The name above is a label; this is what the entry is really
				// keyed on, and the reason renaming the loadout leaves this
				// checkout working. An entry written before identities existed
				// says so rather than showing a blank.
				if g.ID != "" {
					fmt.Fprintf(env.Out, "  identity: %s\n", g.ID)
				} else {
					fmt.Fprintf(env.Out, "  identity: none recorded - matched by name (written before identities)\n")
				}
				for _, f := range c.Findings {
					fmt.Fprintf(env.Out, "  ! %s\n", f)
				}
				for _, n := range c.Notes {
					fmt.Fprintf(env.Out, "  - %s\n", n)
				}
			}
			if !insp.OK() {
				return fmt.Errorf("this checkout does not match %s: %d %s (run `barracks garrison` to put it back)",
					garrison.LockName, insp.Findings(), plural(insp.Findings(), "problem", "problems"))
			}
			return nil
		},
	}
}
