package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/tobi404/barracks/internal/garrison"
	"github.com/tobi404/barracks/internal/loadout"
	"github.com/tobi404/barracks/internal/upgrade"
)

// garrisonStage is the committed tier's half of one upgrade: what this
// repository has garrisoned, and where its lockfile stands against the loadout's
// pins once the upgrade has re-resolved them.
//
// The two tiers are upgraded by the same command because a user thinks of one
// skill set moving forward, not of two mechanisms. They are reported separately
// because what happens to them is genuinely different: a spawn is relinked in
// place and nobody else sees it, while a garrison is rewritten into tracked
// files and the change goes to a reviewer.
type garrisonStage struct {
	Loadout string
	// Behind is one line per source whose lockfile commit differs from the pin
	// the loadout now carries. Empty means the committed files are already
	// where this upgrade would put them.
	Behind []string
	// Blocked is set when the loadout's own upgrade failed, so bringing the
	// committed files onto pins the definition may not record is refused.
	Blocked bool

	next   *loadout.Loadout
	result *garrison.Result
	err    error
}

// Changed reports whether this stage has anything to do or to say.
func (g *garrisonStage) Changed() bool { return len(g.Behind) > 0 || g.Blocked }

// planGarrisonUpgrades reads what the upgrade means for this repository's
// committed tier, writing nothing.
//
// Every read here is one `barracks inspect` could make, which is what lets a
// `--dry-run` describe the committed half as accurately as the real run performs
// it. The comparison is against the lockfile rather than against whether a source
// moved in this run, so a garrison left behind by an earlier upgrade - or by a
// teammate who never pushed the lockfile - is recognised and brought forward.
// That mirrors what the upgrade package already does for spawns, where a move is
// planned for every source so a skip can never become permanent.
func (e *Env) planGarrisonUpgrades(ctx context.Context, plans []*upgrade.LoadoutPlan) []*garrisonStage {
	loc, err := e.scopeOf(ctx, false)
	if err != nil || loc.Root == "" || loc.GitDir == "" {
		return nil
	}
	m, err := garrison.Load(loc.Root)
	if err != nil {
		fmt.Fprintf(e.Err, "! %v\n", err)
		return nil
	}

	var out []*garrisonStage
	for _, p := range plans {
		g := m.FindFor(p.Next.ID, p.Name)
		if g == nil {
			continue
		}
		stage := &garrisonStage{Loadout: p.Name, next: p.Next, Blocked: p.Failed()}
		if !stage.Blocked {
			stage.Behind = lockfileBehind(g, p.Next)
		}
		if stage.Changed() {
			out = append(out, stage)
		}
	}
	return out
}

// lockfileBehind lists the sources whose committed commit is not the one the
// loadout is now pinned at.
func lockfileBehind(g *garrison.Garrison, next *loadout.Loadout) []string {
	committed := map[string]string{}
	for _, s := range g.Sources {
		committed[s.RepoKey()+"\x00"+s.Subpath] = s.Commit
	}
	var behind []string
	for _, eq := range next.Equipment {
		key := eq.RepoKey() + "\x00" + eq.Subpath
		was, known := committed[key]
		switch {
		case !known:
			behind = append(behind, fmt.Sprintf("%s  not in %s yet", eq.Ident(), garrison.LockName))
		case was != eq.Commit:
			behind = append(behind, fmt.Sprintf("%s  %s -> %s", eq.Ident(), upgrade.Short(was), upgrade.Short(eq.Commit)))
		}
	}
	return behind
}

// applyGarrisonUpgrades rewrites the vendored files and the lockfile together.
//
// It runs after the upgrade has saved the loadout definitions, so it installs the
// pins the definition now records rather than the ones it had a moment ago -
// files and lockfile and definition all naming the same commits, which is the
// whole point of doing this in one command instead of leaving it to be
// remembered.
func (e *Env) applyGarrisonUpgrades(ctx context.Context, stages []*garrisonStage) {
	loc, err := e.scopeOf(ctx, false)
	if err != nil {
		return
	}
	for _, stage := range stages {
		if stage.Blocked {
			continue
		}
		stage.result, stage.err = e.garrisons.Reinstall(ctx, loc.Root, loc.GitDir, stage.next, false)
	}
}

// renderGarrisonUpgrades prints the committed half of the report.
//
// The dry-run and real wordings come from this one function so they cannot drift
// apart: the same planned lines are printed either way, and only the outcome of
// actually writing is added underneath.
func renderGarrisonUpgrades(env *Env, stages []*garrisonStage, dryRun bool) bool {
	if len(stages) == 0 {
		return true
	}
	ok := true
	fmt.Fprintf(env.Out, "committed here (%s)\n", garrison.LockName)
	for _, stage := range stages {
		if stage.Blocked {
			fmt.Fprintf(env.Out, "  %s  left as it is: its sources could not be upgraded\n", stage.Loadout)
			continue
		}
		fmt.Fprintf(env.Out, "  %s\n", stage.Loadout)
		for _, line := range stage.Behind {
			fmt.Fprintf(env.Out, "    %s\n", line)
		}
		if dryRun {
			fmt.Fprintf(env.Out, "    would rewrite the committed files and %s together\n", garrison.LockName)
			continue
		}
		if stage.err != nil {
			ok = false
			fmt.Fprintf(env.Err, "! %s: %v\n", stage.Loadout, stage.err)
			if errors.Is(stage.err, garrison.ErrLocallyModified) {
				fmt.Fprintf(env.Err, "! run `barracks garrison %s --force` to take the new content anyway\n", stage.Loadout)
			}
			continue
		}
		if stage.result == nil {
			continue
		}
		for _, p := range stage.result.Wrote {
			fmt.Fprintf(env.Out, "    + %s\n", p)
		}
		for _, p := range stage.result.Deleted {
			fmt.Fprintf(env.Out, "    - %s\n", p)
		}
		fmt.Fprintf(env.Out, "    commit these files and %s together\n", garrison.LockName)
		for _, n := range stage.result.Notices {
			fmt.Fprintf(env.Err, "! %s\n", n)
		}
	}
	return ok
}
