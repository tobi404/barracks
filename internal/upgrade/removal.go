package upgrade

import (
	"context"
	"fmt"

	"github.com/tobi404/barracks/internal/lease"
	"github.com/tobi404/barracks/internal/loadout"
	"github.com/tobi404/barracks/internal/skill"
)

// PlanRemoval works out what dropping a source from a loadout does to every live
// spawn of it, without resolving a ref or moving a pin.
//
// It is Plan with the network half taken out. `strip` changes which sources a
// loadout has, never which commit any of them sits at, so there is nothing to
// re-resolve - but the reconciliation afterwards is the same problem exactly,
// and sharing planSpawns is what makes the case most likely to be got wrong come
// out right: when two sources both provide a skill and only one is dropped, the
// link is *handed over* to the survivor in a single relink rather than removed
// and re-added. That handover lives in one place, and this reaches it by
// describing the dropped source as a move to nothing.
//
// Apply then performs the plan unchanged, so a removal and an upgrade leave a
// spawn recorded the same way.
//
// It also reports the skills the loadout still provides once the removal is
// done, each attributed to the surviving source that provides it - the first
// one, the same rule planSpawn attributes by, so the source named here is the
// one a handed-over link ends up pointing into. The answer is a by-product of
// reading every surviving source's tree, and the caller needs it for more than a
// count: a skill the dropped source contributed which another still provides is
// handed over rather than removed, and reporting the two the same way would tell
// the user their skill is gone when it is not.
//
// Deliberately read from the loadout's sources rather than from the spawn plans:
// the answer has to be the same for a loadout garrisoned but never spawned, or
// deployed nowhere at all.
func (e *Engine) PlanRemoval(ctx context.Context, next *loadout.Loadout, dropped []loadout.Equipment) (*LoadoutPlan, map[string]string, error) {
	p := &LoadoutPlan{Name: next.Name, Next: next, definitionChanged: true}

	var moves []move
	remaining := map[string]string{}
	for _, eq := range next.Equipment {
		mv, err := e.moveAt(ctx, eq)
		if err != nil {
			return nil, nil, err
		}
		for name := range mv.skills {
			if _, dup := remaining[name]; !dup {
				remaining[name] = mv.ident
			}
		}
		moves = append(moves, *mv)
	}
	for _, eq := range dropped {
		// A move to nothing. Every link this source put down is one it no longer
		// provides; whether the path goes or is handed to a surviving source is
		// planSpawn's decision, made from the moves above.
		moves = append(moves, move{
			src:     eq.Source,
			subpath: eq.Subpath,
			ident:   eq.Ident(),
			skills:  map[string]string{},
			from:    eq.Commit,
			to:      eq.Commit,
		})
	}

	leases, _ := e.Leases.List()
	// IncludeRunning, because a spawn held by a live session cannot be left
	// behind here the way an upgrade leaves one behind. An upgrade's skip is
	// recoverable - the source is still equipped, so the next run plans the same
	// move again - while a source that has been stripped is gone from the
	// definition, and nothing would ever come back for the links it left. `strip`
	// refuses up front rather than relying on this; see cli.liveSessionsOf.
	p.Spawns = e.planSpawns(ctx, next.Name, next.Equipment, leases, moves, Options{
		IncludeRunning:      true,
		handOverToAnySource: true,
	})

	for i := range p.Spawns {
		p.Spawns[i].sources = withoutSources(p.Spawns[i].sources, dropped)
	}
	return p, remaining, nil
}

// moveAt describes where a source's links belong at the commit it is already
// pinned at, materialising that commit if the store does not hold it.
//
// It refuses rather than returning nothing, which is the whole point. moveFor
// returns nil for a source it cannot read, and an upgrade can afford that: no
// move means no plan for those links, and they are left exactly where they are.
// A removal cannot - a surviving source whose tree could not be read is one
// barracks cannot tell provides the dropped skill too, and the difference
// between "hand this link over" and "delete it" is precisely that answer.
func (e *Engine) moveAt(ctx context.Context, eq loadout.Equipment) (*move, error) {
	if eq.Commit == "" {
		return nil, fmt.Errorf("source %s is not pinned to a commit; re-equip it", eq.Ident())
	}
	dir, _, err := e.Store.Ensure(ctx, eq.Source, eq.Commit)
	if err != nil {
		return nil, fmt.Errorf("%s is still equipped and its skills have to be read before anything is removed: %w", eq.Ident(), err)
	}
	found, err := skill.Discover(dir, eq.Subpath)
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", eq.Ident(), err)
	}
	selected, err := skill.Filter(found, eq.Only, eq.Except)
	if err != nil {
		return nil, err
	}
	mv := &move{
		src:     eq.Source,
		subpath: eq.Subpath,
		ident:   eq.Ident(),
		skills:  map[string]string{},
		from:    eq.Commit,
		to:      eq.Commit,
	}
	for _, s := range selected {
		mv.skills[s.Name] = s.AbsPath
	}
	return mv, nil
}

// withoutSources drops the stripped sources from a spawn's provenance.
//
// Provenance answers "which sources was this spawn made from", and it is read to
// decide what an upgrade may *add*. A source the loadout no longer has must stop
// being an answer, or a later equip of the same repository at another ref would
// find a spawn that claims to carry a source nobody equipped.
func withoutSources(recorded []lease.SourceRef, dropped []loadout.Equipment) []lease.SourceRef {
	if len(recorded) == 0 || len(dropped) == 0 {
		return recorded
	}
	gone := make(map[string]bool, len(dropped))
	for _, eq := range dropped {
		gone[eq.RepoKey()+"\x00"+eq.Subpath] = true
	}
	out := make([]lease.SourceRef, 0, len(recorded))
	for _, s := range recorded {
		if gone[s.Key+"\x00"+s.Subpath] {
			continue
		}
		out = append(out, s)
	}
	return out
}
