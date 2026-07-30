package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/tobi404/barracks/internal/lease"
	"github.com/tobi404/barracks/internal/skill"
	"github.com/tobi404/barracks/internal/spawn"
	"github.com/tobi404/barracks/internal/target"
)

func newSpawnCmd(env *Env) *cobra.Command {
	var (
		forDur       time.Duration
		global       bool
		targetIDs    []string
		only, except []string
	)

	cmd := &cobra.Command{
		Use:   "spawn <loadout>",
		Short: "Spawn a loadout into the current repo",
		Long: strings.TrimSpace(`
Materialises a loadout's skills into every agent it installs into.

Skills are symlinked from the shared store, so spawning is instant and every
repo on your machine shares one copy on disk. The created paths are registered
in .git/info/exclude - never in the committed .gitignore - so git status stays
clean.

Which agents a spawn reaches is decided in this order: --target if given, then
the loadout's own declaration (barracks assign), then whichever agents already
have a configuration directory here, then the default target. A --target given
here applies to this spawn only and never changes what the loadout declares.

By default the spawn lasts until you recall it. Give it a deadline with --for,
and it is removed automatically by whichever barracks command runs next after
the clock passes:

  barracks spawn frontend
  barracks spawn frontend --for 2h
  barracks spawn frontend --global
  barracks spawn frontend --target cursor --target claude

Use --only and --except to send part of a loadout instead of all of it. They
take the same glob patterns barracks equip takes and match the same way, on a
skill's name or its path inside its source:

  barracks spawn frontend --only 'react-*'
  barracks spawn frontend --except deprecated-helper

The one difference from equip is the whole point of them: equip stores its
filter in the loadout, and these apply to this deployment only. The loadout is
not modified, and nothing remembers them afterwards - recall and spawn again
without them and the whole unit goes back out. An upgrade keeps a narrowed
deployment narrowed rather than quietly filling it in.

Use --global to install into each agent's user-level skills directory instead
of the current repo. To tie a spawn to a single command's lifetime, use
barracks run.`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env.reap()

			l, err := env.loadouts.Get(args[0])
			if err != nil {
				return err
			}
			sel, err := env.selectTargets(cmd.Context(), l, targetIDs, global)
			if err != nil {
				return err
			}
			// A spawn is scoped to a repository, and so is the flavor line's
			// escalation: the same loadout spawned somewhere new is a first
			// spawn there, not a repeat.
			env.noteScope(cmd.Context(), global)

			req := spawn.Request{
				Loadout: l,
				Global:  global,
				Cwd:     env.Cwd,
				Kind:    lease.KindManual,
				Skills:  skill.Selection{Only: only, Except: except},
			}
			if forDur > 0 {
				req.Kind = lease.KindDeadline
				req.Duration = forDur
			}

			env.announceSelection(sel)
			results, err := env.spawnAll(cmd.Context(), req, sel.Targets)
			if err != nil {
				return err
			}
			for i, res := range results {
				printSpawn(env, res, sel.Targets[i])
			}
			return nil
		},
	}
	cmd.Flags().DurationVar(&forDur, "for", 0, "recall automatically after this long (e.g. 90m, 2h)")
	cmd.Flags().BoolVar(&global, "global", false, "spawn into each agent's user-level skills directory")
	cmd.Flags().StringSliceVar(&targetIDs, "target", nil, targetFlagHelp("spawn for"))
	// The persistence is spelled out on the flag itself, not only in the long
	// help: these are the same words `barracks equip` uses for a filter it stores
	// forever, and a user who reaches for them from memory reads the one-liner.
	cmd.Flags().StringSliceVar(&only, "only", nil, "deploy only skills matching these glob patterns (this deployment only; the loadout is unchanged)")
	cmd.Flags().StringSliceVar(&except, "except", nil, "leave skills matching these glob patterns behind (this deployment only; the loadout is unchanged)")
	return cmd
}

func printSpawn(env *Env, res *spawn.Result, tgt target.Target) {
	l := res.Lease
	fmt.Fprintf(env.Out, "spawned %s into %s (%s, %s)\n", l.Loadout, l.Dir, tgt.Display, l.Describe(env.now()))
	for _, s := range res.Skills {
		fmt.Fprintf(env.Out, "  + %s\n", s.Name)
	}
	// A deployment that carries less than the unit does says so. Printing only
	// what went out reads identically to a loadout that has just those skills,
	// and the difference is the whole reason the flags exist.
	if res.Skipped > 0 {
		fmt.Fprintf(env.Out, "  (%d %s left behind - this deployment only, %s still carries them)\n",
			res.Skipped, plural(res.Skipped, "skill", "skills"), l.Loadout)
	}
	if res.Fetched > 0 {
		fmt.Fprintf(env.Out, "  (%d %s fetched into the store)\n", res.Fetched, plural(res.Fetched, "source", "sources"))
	}
	for _, n := range res.Notices {
		fmt.Fprintf(env.Err, "! %s\n", n)
	}
}
