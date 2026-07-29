package tui

import (
	"sort"
	"strings"

	"github.com/tobi404/barracks/internal/garrison"
	"github.com/tobi404/barracks/internal/lease"
	"github.com/tobi404/barracks/internal/loadout"
)

// unit is one loadout as the roster sees it: the definition plus everywhere it
// is currently standing.
//
// Nothing here is derived from a display string. Each field is read from the
// same records the commands read - definitions from the loadouts directory,
// spawns from lease records, the committed tier from barracks.lock - so a row
// that says "deployed" is saying it because a record on disk says so.
type unit struct {
	Loadout *loadout.Loadout
	// Here are the live spawns of this loadout in the repository the TUI was
	// launched from.
	Here []*lease.Lease
	// Away is how many live spawns this loadout has somewhere else on the
	// machine - other repositories, or a global install.
	Away int
	// Committed is this loadout's entry in this repository's barracks.lock, if
	// it has one. A garrison has no lease and is never reaped.
	Committed *garrison.Garrison
}

// Deployed reports whether this unit is standing anywhere at all.
func (u unit) Deployed() bool { return len(u.Here) > 0 || u.Away > 0 || u.Committed != nil }

// SkillCount is how many skills the definition currently records.
func (u unit) SkillCount() int { return u.Loadout.SkillCount() }

// Status is the one-word posture shown in the roster.
func (u unit) Status() string {
	switch {
	case u.Committed != nil && len(u.Here) > 0:
		return "held+out"
	case u.Committed != nil:
		return "held"
	case len(u.Here) > 0:
		return "deployed"
	case u.Away > 0:
		return "afield"
	case len(u.Loadout.Equipment) == 0:
		return "unequipped"
	default:
		return "in reserve"
	}
}

// state is everything one draw of the roster is built from.
type state struct {
	Units []unit
	// Root is the repository the TUI is standing in, empty outside one.
	Root string
	// Problems are records that could not be read. They are surfaced rather
	// than dropped: a loadout that has become unreadable is exactly the thing a
	// roster must not quietly omit.
	Problems []string
}

// reader is the set of records the roster is built from. It is an interface so
// the model can be driven in a test without a barracks home, and so nothing in
// this package reaches for a path of its own.
type reader interface {
	Loadouts() ([]*loadout.Loadout, []error)
	Leases() ([]*lease.Lease, []error)
	Garrisons(root string) ([]garrison.Garrison, error)
	Root() string
}

// gather reads every record once and assembles the roster.
func gather(r reader) state {
	st := state{Root: r.Root()}

	loadouts, problems := r.Loadouts()
	for _, p := range problems {
		st.Problems = append(st.Problems, p.Error())
	}

	leases, lproblems := r.Leases()
	for _, p := range lproblems {
		st.Problems = append(st.Problems, p.Error())
	}

	var committed []garrison.Garrison
	if st.Root != "" {
		g, err := r.Garrisons(st.Root)
		if err != nil {
			st.Problems = append(st.Problems, err.Error())
		}
		committed = g
	}

	here := lease.FindInScope(leases, lease.ScopeRepo, st.Root)
	inScope := map[string]bool{}
	for _, l := range here {
		inScope[l.ID] = true
	}

	for _, l := range loadouts {
		u := unit{Loadout: l}
		for _, ls := range here {
			if ls.Loadout == l.Name {
				u.Here = append(u.Here, ls)
			}
		}
		for _, ls := range leases {
			if ls.Loadout == l.Name && !inScope[ls.ID] {
				u.Away++
			}
		}
		for i := range committed {
			if committed[i].Loadout == l.Name {
				u.Committed = &committed[i]
				break
			}
		}
		st.Units = append(st.Units, u)
	}
	sort.SliceStable(st.Units, func(i, j int) bool {
		return st.Units[i].Loadout.Name < st.Units[j].Loadout.Name
	})
	return st
}

// shortCommit is the commit a source is pinned at, cut to the length a roster
// row can carry. An empty pin stays empty rather than becoming a row of dashes.
func shortCommit(c string) string {
	if len(c) <= 7 {
		return c
	}
	return c[:7]
}

// targetLabel renders a loadout's declared targets, or says that it has none.
func targetLabel(l *loadout.Loadout) string {
	if len(l.Targets) == 0 {
		return "detected per repository"
	}
	return strings.Join(l.Targets, ", ")
}
