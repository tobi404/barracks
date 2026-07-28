package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tobi404/barracks/internal/loadout"
	"github.com/tobi404/barracks/internal/upgrade"
)

func newUpgradeCmd(env *Env) *cobra.Command {
	var (
		dryRun         bool
		pin            bool
		includeRunning bool
	)

	cmd := &cobra.Command{
		Use:   "upgrade [loadout...]",
		Short: "Re-resolve loadout sources and relink live spawns",
		Long: strings.TrimSpace(`
Re-resolves each source's declared ref, fetches whatever it now points at, and
relinks every live spawn onto the new commit. With no loadout named, every
loadout is upgraded.

For each source barracks reports the old and new commits and which skills were
added, removed, or modified. Modified means the skill's content changed, not
merely that the repository moved: a commit that leaves every skill
byte-identical says so instead of claiming an update. A source pinned to an
exact commit has nothing to resolve and is reported as pinned.

Relinking obeys the same rule as recall. A spawned path is repointed or removed
only while it is still a symlink into the barracks store; a file or directory of
your own that has taken its place is left alone and reported. New skills are
registered in .git/info/exclude, so git status stays clean.

A spawn held by a process barracks started - a live "barracks run" session -
keeps the skills it started with, because changing them underneath a session
that has already read them is exactly the kind of surprise barracks exists not
to produce. Pass --include-running to relink those too.

  barracks upgrade
  barracks upgrade frontend --dry-run
  barracks upgrade frontend --pin`),
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			env.reap()

			targets, err := selectLoadouts(env, args)
			if err != nil {
				return err
			}
			if len(targets) == 0 {
				fmt.Fprintln(env.Out, "no loadouts trained yet - start with: barracks train <name>")
				return nil
			}

			eng := &upgrade.Engine{
				Store:    env.store,
				Loadouts: env.loadouts,
				Leases:   env.leases,
				Git:      env.Git,
				Prober:   env.Prober,
			}
			opts := upgrade.Options{Pin: pin, IncludeRunning: includeRunning}

			plans := eng.Plan(cmd.Context(), targets, opts)
			if !dryRun {
				eng.Apply(plans)
			}
			renderUpgrade(env, plans, dryRun)

			for _, p := range plans {
				if p.Failed() {
					return fmt.Errorf("some sources could not be upgraded")
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without changing it")
	cmd.Flags().BoolVar(&pin, "pin", false, "record the newly resolved commit as the source's declared ref")
	cmd.Flags().BoolVar(&includeRunning, "include-running", false, "also relink spawns held by a running process")
	return cmd
}

// selectLoadouts resolves the command's arguments to loadouts, refusing the
// whole run if one was named that does not exist. Upgrading three of four
// requested loadouts and reporting the fourth as a footnote would be worse than
// not starting.
func selectLoadouts(env *Env, names []string) ([]*loadout.Loadout, error) {
	if len(names) > 0 {
		out := make([]*loadout.Loadout, 0, len(names))
		for _, n := range names {
			l, err := env.loadouts.Get(n)
			if err != nil {
				return nil, err
			}
			out = append(out, l)
		}
		return out, nil
	}
	all, problems := env.loadouts.List()
	for _, p := range problems {
		fmt.Fprintf(env.Err, "! %v\n", p)
	}
	return all, nil
}

// renderUpgrade prints the plan. The body is written from the plan alone and is
// identical whether or not Apply ran, which is what makes --dry-run output
// something a cautious user can rely on.
func renderUpgrade(env *Env, plans []*upgrade.LoadoutPlan, dryRun bool) {
	if dryRun {
		fmt.Fprintln(env.Out, "dry run - resolving and fetching only, nothing else is changed")
	}
	for _, p := range plans {
		fmt.Fprintf(env.Out, "%s\n", p.Name)
		if len(p.Sources) == 0 {
			fmt.Fprintf(env.Out, "  no sources equipped\n")
		}
		for _, s := range p.Sources {
			renderSource(env.Out, s)
		}
		for i := range p.Spawns {
			renderSpawn(env, &p.Spawns[i])
		}
		for _, err := range p.Errs {
			fmt.Fprintf(env.Err, "! %v\n", err)
		}
	}
	if dryRun {
		fmt.Fprintln(env.Out, "dry run - nothing was changed")
	}
}

func renderSource(w io.Writer, s upgrade.SourcePlan) {
	switch s.Status {
	case upgrade.StatusPinned:
		fmt.Fprintf(w, "  %s  pinned at %s - nothing to resolve\n", s.Ident, upgrade.Short(s.OldCommit))
	case upgrade.StatusCurrent:
		fmt.Fprintf(w, "  %s  %s  already current\n", s.Ident, upgrade.Short(s.OldCommit))
	case upgrade.StatusSameContent:
		fmt.Fprintf(w, "  %s  %s -> %s  no skill changed\n",
			s.Ident, upgrade.Short(s.OldCommit), upgrade.Short(s.NewCommit))
	case upgrade.StatusUpgraded:
		fmt.Fprintf(w, "  %s  %s -> %s\n", s.Ident, upgrade.Short(s.OldCommit), upgrade.Short(s.NewCommit))
		renderDiff(w, s.Diff)
	case upgrade.StatusFailed:
		fmt.Fprintf(w, "  %s  could not be upgraded: %v\n", s.Ident, s.Err)
	}
	if s.NewIdent != s.Ident {
		fmt.Fprintf(w, "    pinned to %s\n", s.NewIdent)
	}
	for _, n := range s.Notes {
		fmt.Fprintf(w, "    note: %s\n", n)
	}
}

func renderDiff(w io.Writer, d upgrade.Diff) {
	for _, n := range d.Added {
		fmt.Fprintf(w, "    + %s\n", n)
	}
	for _, n := range d.Modified {
		fmt.Fprintf(w, "    ~ %s\n", n)
	}
	for _, n := range d.Removed {
		fmt.Fprintf(w, "    - %s\n", n)
	}
	if n := len(d.Unchanged); n > 0 {
		fmt.Fprintf(w, "    (%d %s unchanged)\n", n, plural(n, "skill", "skills"))
	}
	if d.ByName {
		fmt.Fprintf(w, "    (compared by name only)\n")
	}
}

func renderSpawn(env *Env, sp *upgrade.SpawnPlan) {
	l := sp.Lease
	if sp.Skip != "" {
		fmt.Fprintf(env.Out, "  %s  left as it is: %s\n", l.Dir, sp.Skip)
		return
	}
	// The header exists to head a list. Kept paths and notices carry their own
	// absolute path, so a spawn with nothing but those prints no header at all
	// rather than an empty heading on stdout.
	if len(sp.Ops) > 0 || sp.Recall {
		fmt.Fprintf(env.Out, "  %s  [%s]\n", l.Dir, l.Kind)
		// Same shape as the source diff above it: what arrived, what left, then
		// the bulk that merely moved.
		relinked := 0
		for _, op := range sp.Ops {
			if op.Kind == upgrade.OpAdd {
				fmt.Fprintf(env.Out, "    + %s\n", op.Skill)
			}
		}
		for _, op := range sp.Ops {
			switch op.Kind {
			case upgrade.OpRemove:
				fmt.Fprintf(env.Out, "    - %s\n", op.Skill)
			case upgrade.OpRelink:
				relinked++
			}
		}
		if relinked > 0 {
			fmt.Fprintf(env.Out, "    %d %s relinked\n", relinked, plural(relinked, "skill", "skills"))
		}
		if sp.Recall {
			fmt.Fprintf(env.Out, "    nothing left to deploy - the spawn is recalled\n")
		}
	}
	for _, n := range sp.Notes {
		fmt.Fprintf(env.Err, "! %s\n", n)
	}
	for _, k := range sp.Kept {
		fmt.Fprintf(env.Err, "! left in place (barracks did not create it): %s - %s\n", k.Path, k.Reason)
	}
	for _, err := range sp.Errs {
		fmt.Fprintf(env.Err, "! %v\n", err)
	}
}
