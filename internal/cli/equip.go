package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tobi404/barracks/internal/loadout"
	"github.com/tobi404/barracks/internal/skill"
	"github.com/tobi404/barracks/internal/source"
)

func newEquipCmd(env *Env) *cobra.Command {
	var only, except []string

	cmd := &cobra.Command{
		Use:   "equip <loadout> <source>",
		Short: "Equip a loadout with a git skill source",
		Long: strings.TrimSpace(`
Attaches a git source of skills to a loadout.

The source is resolved to a concrete commit and fetched once into the shared
store, then scanned for skills - any directory containing a SKILL.md. The
commit is pinned in the loadout definition, so a spawn reproduces the same
skills even after the branch moves on.

Source forms:

  gh:owner/repo                     shorthand for GitHub
  github.com/owner/repo             any host
  https://github.com/owner/repo.git
  git@github.com:owner/repo.git
  ./path/to/local/repo              a repository on disk

Any form takes a "#ref" suffix to pin a branch, tag, or commit, and a
"#ref:subpath" suffix to scan only part of the repo:

  barracks equip frontend gh:owner/skills#v1.2.0
  barracks equip frontend gh:owner/monorepo#main:packages/skills

Use --only and --except to take a few skills out of a large repo:

  barracks equip frontend gh:owner/skills --only 'react-*,css-*'
  barracks equip frontend gh:owner/skills --except deprecated-helper`),
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
			if err := src.Validate(); err != nil {
				return err
			}

			ctx := cmd.Context()
			commit, err := env.store.Resolve(ctx, src)
			if err != nil {
				return err
			}
			dir, fetched, err := env.store.Ensure(ctx, src, commit)
			if err != nil {
				return err
			}

			found, err := skill.Discover(dir, src.Subpath)
			if err != nil {
				return fmt.Errorf("scan %s: %w", src.Ident(), err)
			}
			selected, err := skill.Filter(found, only, except)
			if err != nil {
				return err
			}
			if len(selected) == 0 {
				if len(found) == 0 {
					return fmt.Errorf("no skills found in %s (looked for directories containing %s)", src.Ident(), skill.Manifest)
				}
				return fmt.Errorf("filters matched none of the %d skills in %s: %s",
					len(found), src.Ident(), strings.Join(skill.Names(found), ", "))
			}

			eq := loadout.Equipment{
				Source:     src,
				Commit:     commit,
				Only:       only,
				Except:     except,
				Skills:     skill.Names(selected),
				EquippedAt: env.now().UTC(),
			}
			previous := l.Equip(eq)
			if err := env.loadouts.Save(l); err != nil {
				return err
			}

			verb := "reused cached"
			if fetched {
				verb = "fetched"
			}
			switch {
			case previous == nil:
				fmt.Fprintf(env.Out, "equipped %s with %s@%s (%s source)\n", l.Name, src.Ident(), shortSHA(commit), verb)
			case previous.Commit == commit:
				fmt.Fprintf(env.Out, "%s was already equipped with %s, still pinned at %s\n", l.Name, src.Ident(), shortSHA(commit))
			default:
				fmt.Fprintf(env.Out, "%s was already equipped with %s, re-pinned %s -> %s\n", l.Name, src.Ident(), shortSHA(previous.Commit), shortSHA(commit))
			}
			for _, s := range selected {
				fmt.Fprintf(env.Out, "  + %s\n", s.Name)
			}
			if skipped := len(found) - len(selected); skipped > 0 {
				fmt.Fprintf(env.Out, "  (%d %s filtered out)\n", skipped, plural(skipped, "skill", "skills"))
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&only, "only", nil, "take only skills matching these glob patterns")
	cmd.Flags().StringSliceVar(&except, "except", nil, "skip skills matching these glob patterns")
	return cmd
}

func shortSHA(commit string) string {
	if len(commit) > 8 {
		return commit[:8]
	}
	return commit
}
