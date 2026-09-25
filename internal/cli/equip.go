package cli

import (
	"context"
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
  barracks equip frontend gh:owner/skills --except deprecated-helper

A skill is installed under its name, so two sources may not both provide it.
Equipping a source whose every skill the loadout already carries is refused -
to switch to it, strip the old source first. A source that shares only some of
them is equipped with a warning naming the --except that skips them.`),
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			env.reap()
			return env.equip(cmd.Context(), args[0], args[1], only, except)
		},
	}
	cmd.Flags().StringSliceVar(&only, "only", nil, "take only skills matching these glob patterns")
	cmd.Flags().StringSliceVar(&except, "except", nil, "skip skills matching these glob patterns")
	return cmd
}

// equip attaches a source to a loadout and reports what it carried in. It is
// the whole of `barracks equip`, and the roster's equip order runs exactly this,
// so the two surfaces share one set of rules about what may be equipped.
//
// The loadout is read back by name rather than taken from a caller's copy: a
// definition held on a screen may be older than the one on disk, and saving
// the stale one over it would silently drop whatever changed in between.
func (e *Env) equip(ctx context.Context, name, raw string, only, except []string) error {
	l, err := e.loadouts.Get(name)
	if err != nil {
		return err
	}
	src, err := parseSource(raw)
	if err != nil {
		return err
	}

	commit, err := e.store.Resolve(ctx, src)
	if err != nil {
		return err
	}
	dir, fetched, err := e.store.Ensure(ctx, src, commit)
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
		EquippedAt: e.now().UTC(),
	}
	// A skill two sources provide refuses every spawn and garrison of
	// the loadout, so it is caught here, where the source that caused
	// it is the one being typed. Nothing is saved when this source adds
	// nothing the loadout does not already carry.
	overlap := l.Overlap(eq)
	if len(overlap) == distinctCount(eq.Skills) {
		return fullOverlapError(l.Name, eq, overlap)
	}
	previous := l.Equip(eq)
	if err := e.loadouts.Save(l); err != nil {
		return err
	}

	verb := "reused cached"
	if fetched {
		verb = "fetched"
	}
	switch {
	case previous == nil:
		fmt.Fprintf(e.Out, "equipped %s with %s@%s (%s source)\n", l.Name, src.Ident(), shortSHA(commit), verb)
	case previous.Commit == commit:
		fmt.Fprintf(e.Out, "%s was already equipped with %s, still pinned at %s\n", l.Name, src.Ident(), shortSHA(commit))
	default:
		fmt.Fprintf(e.Out, "%s was already equipped with %s, re-pinned %s -> %s\n", l.Name, src.Ident(), shortSHA(previous.Commit), shortSHA(commit))
	}
	for _, s := range selected {
		fmt.Fprintf(e.Out, "  + %s\n", s.Name)
	}
	if skipped := len(found) - len(selected); skipped > 0 {
		fmt.Fprintf(e.Out, "  (%d %s filtered out)\n", skipped, plural(skipped, "skill", "skills"))
	}
	if len(overlap) > 0 {
		warnPartialOverlap(e, l.Name, eq, overlap)
	}
	return nil
}

// parseSource is the source syntax `barracks equip` accepts, and the only
// statement of it: the roster asks the same question before it hands the
// terminal over for a fetch, so a typo is answered on the prompt it was typed
// into rather than after the screen has stepped aside.
func parseSource(raw string) (source.Source, error) {
	src, err := source.Parse(raw)
	if err != nil {
		return source.Source{}, err
	}
	if err := src.Validate(); err != nil {
		return source.Source{}, err
	}
	return src, nil
}

func shortSHA(commit string) string {
	if len(commit) > 8 {
		return commit[:8]
	}
	return commit
}

// fullOverlapError refuses a source that would add nothing but clashes: every
// skill it offers, another equipped source already provides. That is almost
// always a retry - the same repository again with a subpath or a #ref - and the
// way to switch to it is to strip the one it would clash with.
func fullOverlapError(name string, eq loadout.Equipment, overlap []loadout.Collision) error {
	var providers, strips []string
	for _, p := range providersOf(overlap) {
		providers = append(providers, p.Ident())
		strips = append(strips, fmt.Sprintf("`barracks strip %s %s`", name, loadout.ShellArg(p.Spelling())))
	}
	return fmt.Errorf("every skill %s offers is already provided by %s: %s; a skill can come from one source only, so %s could not be deployed with both\nTo switch to %s, run %s first, then equip it again",
		eq.Ident(), strings.Join(providers, " and "), loadout.ListNames(loadout.SkillsOf(overlap)),
		name, eq.Ident(), strings.Join(strips, " and "))
}

// warnPartialOverlap reports the skills a newly equipped source shares with the
// loadout's other equipment. The source is kept for the skills only it provides;
// the shared ones would refuse every spawn and garrison until one side skips
// them, and --except on this source is the repair that loses nothing.
func warnPartialOverlap(env *Env, name string, eq loadout.Equipment, overlap []loadout.Collision) {
	shared := loadout.SkillsOf(overlap)
	var providers []string
	for _, p := range providersOf(overlap) {
		providers = append(providers, p.Ident())
	}
	fmt.Fprintf(env.Err, "! %d %s %s offers %s also provided by %s: %s\n",
		len(shared), plural(len(shared), "skill", "skills"), eq.Ident(), plural(len(shared), "is", "are"),
		strings.Join(providers, " and "), loadout.ListNames(shared))
	fmt.Fprintf(env.Err, "  a skill can come from one source only, so %s cannot be deployed until one side skips %s:\n  %s\n",
		name, plural(len(shared), "it", "them"), loadout.ReEquipCommand(name, eq, shared))
}

// providersOf is every already-equipped source in overlap, once each, in the
// order they first provide a shared skill. Overlap lists the new source last.
func providersOf(overlap []loadout.Collision) []loadout.Equipment {
	var out []loadout.Equipment
	seen := map[string]bool{}
	for _, c := range overlap {
		for _, p := range c.Sources[:len(c.Sources)-1] {
			if !seen[p.Ident()] {
				seen[p.Ident()] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// distinctCount is how many different names are in names. One source may hold
// two directories of the same name, and Overlap reports each name once.
func distinctCount(names []string) int {
	seen := map[string]bool{}
	for _, n := range names {
		seen[n] = true
	}
	return len(seen)
}
