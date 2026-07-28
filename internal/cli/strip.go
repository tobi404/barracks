package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tobi404/barracks/internal/garrison"
	"github.com/tobi404/barracks/internal/lease"
	"github.com/tobi404/barracks/internal/loadout"
	"github.com/tobi404/barracks/internal/source"
	"github.com/tobi404/barracks/internal/upgrade"
)

func newStripCmd(env *Env) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "strip <loadout> <source>",
		Short: "Detach an equipped source and take its skills back out",
		Long: strings.TrimSpace(`
Detaches a git source from a loadout, and removes the skills it contributed from
every live spawn of that loadout and from this repository's garrison.

This is the inverse of barracks equip. Nothing else is touched: skills the
loadout's other sources provide stay exactly where they are, and a skill another
equipped source also provides is handed over to it rather than removed.

  barracks strip frontend gh:owner/skills
  barracks strip frontend github.com/owner/monorepo#main:packages/skills

Name the source however you like - the shorthand you equipped it with, or the
full label barracks list prints. A spelling that could mean two equipped sources
is refused rather than guessed at, because removing the wrong one would take
skills out of every repository this loadout is deployed in.

Removal obeys the same rule as recall: a spawned path is removed only while it is
still a symlink into the barracks store, and a committed file only while it still
matches the digest barracks.lock recorded. Anything else is kept and reported.

Stripping the last source is allowed and leaves an empty loadout - it is not
disbanded, so you can equip it with something else. Its spawns are recalled and
its garrison removed, because there is nothing left for them to hold.

Nothing is changed unless everything can be: a run that cannot finish leaves the
loadout, its spawns and its committed files exactly as they were.`),
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			env.reap()
			name, raw := args[0], args[1]

			l, err := env.loadouts.Get(name)
			if err != nil {
				return err
			}
			src, err := source.Parse(raw)
			if err != nil {
				return err
			}
			i, err := l.Find(src)
			if err != nil {
				return err
			}
			// A live `barracks run` session is refused rather than skipped. An
			// upgrade can leave one behind because the source is still equipped
			// and the next run plans the same move again; a stripped source is
			// gone from the definition, so nothing would ever come back for the
			// links it left, and the spawn would sit on skills the loadout no
			// longer has. Refusing keeps that impossible.
			if held := env.liveSessionsOf(l.Name); len(held) > 0 {
				return fmt.Errorf("%s is held by %s in %s; stripping a source would take skills out from under it.\nWait for that session to exit, or recall it first",
					l.Name, describeHolder(held[0]), held[0].Dir)
			}

			next := *l
			next.Equipment = append([]loadout.Equipment(nil), l.Equipment...)
			dropped := next.Strip(i)

			eng := &upgrade.Engine{
				Store:    env.store,
				Loadouts: env.loadouts,
				Leases:   env.leases,
				Git:      env.Git,
				Prober:   env.Prober,
			}
			plan, left, err := eng.PlanRemoval(cmd.Context(), &next, []loadout.Equipment{dropped})
			if err != nil {
				return err
			}

			// The committed half goes first, and only here: it is the one that
			// can refuse - over a vendored file somebody edited - and a refusal
			// has to arrive while the definition and every spawn are still
			// untouched. `upgrade` runs it last for the opposite reason: there it
			// has to land on pins the definition already records, and here no pin
			// moves at all.
			committed, err := env.stripGarrison(cmd.Context(), &next, left, force)
			if err != nil {
				return err
			}

			eng.Apply([]*upgrade.LoadoutPlan{plan})

			// Apply stops at the definition: relinking spawns onto sources the
			// saved loadout does not record would leave the two out of step, so
			// a save that failed means nothing was detached and no spawn moved.
			// Saying "stripped" here would be a report of something that did not
			// happen - and the committed half, which runs first, has already been
			// written without the source, so what the user has to hear is that
			// the repository and the loadout now disagree.
			if len(plan.Errs) > 0 {
				committed.render(env)
				for _, err := range plan.Errs {
					fmt.Fprintf(env.Err, "! %v\n", err)
				}
				return fmt.Errorf("%s was left as it was: its definition could not be saved, so %s is still equipped and no spawn moved%s",
					l.Name, dropped.Ident(), committed.stranded(l.Name))
			}

			fmt.Fprintf(env.Out, "stripped %s from %s\n", dropped.Ident(), l.Name)
			// The same marks the rest of the tool renders a change with: what is
			// gone takes '-', and a skill a surviving source also provides takes
			// '~', because it was handed over rather than removed. Naming the
			// survivor is the point - "it is still there somehow" would leave the
			// user checking for themselves.
			for _, s := range dropped.Skills {
				if by, still := left[s]; still {
					fmt.Fprintf(env.Out, "  ~ %s (still provided by %s)\n", s, by)
					continue
				}
				fmt.Fprintf(env.Out, "  - %s\n", s)
			}
			if len(next.Equipment) == 0 {
				fmt.Fprintf(env.Out, "  (no sources left - %s is empty; equip it again or disband it)\n", l.Name)
			} else {
				fmt.Fprintf(env.Out, "  (%d %s left, %d %s)\n",
					len(next.Equipment), plural(len(next.Equipment), "source", "sources"),
					len(left), plural(len(left), "skill", "skills"))
			}
			for s := range plan.Spawns {
				renderSpawn(env, &plan.Spawns[s])
			}
			committed.render(env)
			if plan.Failed() {
				return fmt.Errorf("the source was detached but some spawns could not be brought in line")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "replace vendored files that have been edited since they were committed")
	return cmd
}

// liveSessionsOf lists this loadout's spawns held by a process running right now.
func (e *Env) liveSessionsOf(name string) []*lease.Lease {
	leases, _ := e.leases.List()
	var out []*lease.Lease
	for _, l := range leases {
		if l.Loadout != name || l.Kind != lease.KindProcess {
			continue
		}
		if alive, _ := lease.OwnerAlive(e.Prober, l.Owner); alive {
			out = append(out, l)
		}
	}
	return out
}

func describeHolder(l *lease.Lease) string {
	if l.Owner == nil {
		return "a running session"
	}
	cmd := l.Owner.Command
	if cmd == "" {
		cmd = "process"
	}
	return fmt.Sprintf("a running session (pid %d, %s)", l.Owner.PID, cmd)
}

// committedChange is what a strip did to this repository's committed tier, if
// anything. Exactly one of updated and removed is ever set.
type committedChange struct {
	updated *garrison.Result
	removed *garrison.Report
}

// stripGarrison brings this repository's garrison of the loadout in line with
// the sources it has left, and is the only part of a strip that writes tracked
// files.
//
// Only this repository: a garrison's record travels with the repository rather
// than with the machine, so barracks knows of no others - the same limit
// `barracks upgrade` has, and for the same reason.
//
// A loadout with nothing left to vendor has its garrison removed rather than
// rewritten. A lockfile cannot record a garrison of no skills, and leaving the
// committed files behind with no entry describing them is the one state this
// tier must never be in.
func (e *Env) stripGarrison(ctx context.Context, next *loadout.Loadout, left map[string]string, force bool) (committedChange, error) {
	loc, inRepo := e.repoHere(ctx)
	if !inRepo {
		return committedChange{}, nil
	}
	m, err := garrison.Load(loc.Root)
	if err != nil {
		return committedChange{}, err
	}
	g := m.FindFor(next.ID, next.Name)
	if g == nil {
		return committedChange{}, nil
	}
	if len(left) == 0 {
		rep, err := e.garrisons.Remove(loc.Root, garrison.Ref{ID: g.ID, Loadout: g.Loadout})
		return committedChange{removed: rep}, err
	}
	res, err := e.garrisons.Reinstall(ctx, loc.Root, loc.GitDir, next, force)
	if err != nil {
		return committedChange{}, err
	}
	return committedChange{updated: res}, nil
}

// stranded names what this repository is left holding when the rest of the strip
// could not go through. The committed half runs first and is written by then, so
// a failure afterwards leaves the lockfile describing a loadout the definition
// still equips - and the way back is the ordinary command that puts a repository
// in step with its loadout again.
func (c committedChange) stranded(name string) string {
	if c.updated == nil && c.removed == nil {
		return ""
	}
	return fmt.Sprintf(".\nThis repository's committed files and %s were already rewritten without it; run `barracks garrison %s` to bring them back in step",
		garrison.LockName, name)
}

func (c committedChange) render(env *Env) {
	switch {
	case c.updated != nil:
		fmt.Fprintf(env.Out, "committed here (%s)\n", garrison.LockName)
		for _, p := range c.updated.Deleted {
			fmt.Fprintf(env.Out, "  - %s\n", p)
		}
		for _, p := range c.updated.Wrote {
			fmt.Fprintf(env.Out, "  + %s\n", p)
		}
		// A source can be dropped without a vendored file moving - another source
		// provides the same skills byte for byte - and the lockfile no longer
		// recording it is still a change somebody reviews. Saying so beats a
		// header with nothing under it.
		if c.updated.Changed() {
			fmt.Fprintf(env.Out, "  commit these files and %s together\n", garrison.LockName)
		} else {
			fmt.Fprintf(env.Out, "  no vendored file changed - commit %s\n", garrison.LockName)
		}
		for _, n := range c.updated.Notices {
			fmt.Fprintf(env.Err, "! %s\n", n)
		}
	case c.removed != nil:
		rep := c.removed
		fmt.Fprintf(env.Out, "committed here (%s)\n", garrison.LockName)
		fmt.Fprintf(env.Out, "  removed the %s garrison (%d %s, %s updated) - nothing was left to commit\n",
			rep.Loadout, len(rep.Removed), plural(len(rep.Removed), "file", "files"), garrison.LockName)
		for _, k := range rep.Kept {
			fmt.Fprintf(env.Err, "! left in place: %s - %s\n", k.Path, k.Reason)
		}
		for _, err := range rep.Errors {
			fmt.Fprintf(env.Err, "! %v\n", err)
		}
	}
}
