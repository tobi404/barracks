// Package upgrade re-resolves a loadout's declared refs, fetches whatever they
// now point at, and brings live spawns onto the new store entries.
//
// The package is split in two halves on purpose. Plan does every read - the
// resolve, the fetch, the content diff, the inspection of each spawned symlink -
// and returns a description of the change with nothing written outside the
// content-addressed store. Apply performs exactly what that description says.
// `--dry-run` is simply Plan without Apply, which is what makes its output
// trustworthy rather than a separately-maintained guess.
package upgrade

import (
	"context"
	"fmt"
	"sort"

	"github.com/tobi404/barracks/internal/gitcmd"
	"github.com/tobi404/barracks/internal/lease"
	"github.com/tobi404/barracks/internal/loadout"
	"github.com/tobi404/barracks/internal/proc"
	"github.com/tobi404/barracks/internal/skill"
	"github.com/tobi404/barracks/internal/source"
	"github.com/tobi404/barracks/internal/store"
)

// Status is what happened to one equipped source.
type Status string

const (
	// StatusPinned means the declared ref is already a concrete commit. There
	// is nothing to re-resolve, and barracks will not silently refetch it.
	StatusPinned Status = "pinned"
	// StatusCurrent means the ref re-resolved to the commit already pinned.
	StatusCurrent Status = "current"
	// StatusSameContent means the commit moved but every skill is byte-identical.
	StatusSameContent Status = "same-content"
	// StatusUpgraded means the commit moved and the skills changed with it.
	StatusUpgraded Status = "upgraded"
	// StatusFailed means the source could not be resolved or fetched.
	StatusFailed Status = "failed"
)

// Options are the caller's choices for one upgrade.
type Options struct {
	// Pin records the newly resolved commit as the source's declared ref,
	// turning a moving source into a fixed one.
	Pin bool
	// IncludeRunning relinks spawns whose lease is held by a live process.
	// Off by default: see the package's decision in relink.go.
	IncludeRunning bool
}

// Diff is the per-skill change between two commits of one source.
//
// Modified means the skill's content changed, established by comparing content
// digests - not merely that the repository commit moved.
type Diff struct {
	Added     []string
	Removed   []string
	Modified  []string
	Unchanged []string
	// ByName is set when the previous commit is no longer in the store and the
	// comparison had to fall back to the skill names the loadout recorded.
	ByName bool
}

// Changed reports whether any skill actually differs.
func (d Diff) Changed() bool {
	return len(d.Added) > 0 || len(d.Removed) > 0 || len(d.Modified) > 0
}

// SourcePlan is what an upgrade would do to one equipped source.
type SourcePlan struct {
	Index     int    // position in the loadout's equipment list
	Ident     string // the source's identity before the upgrade
	NewIdent  string // its identity afterwards; differs only under --pin
	Status    Status
	OldCommit string
	NewCommit string
	Diff      Diff
	Notes     []string
	Err       error

	skills map[string]string // new skill name -> absolute path in the store
}

// move is one source's live spawns reconciled onto the commit it is pinned at
// once this upgrade is done.
//
// A move is planned for every source, not only the ones whose ref moved,
// because a spawn can be behind its pin for reasons other than an upstream
// change - most often because an earlier upgrade deliberately left it alone
// while a session was using it. Reconciling to the pin every time is what makes
// that skip recoverable instead of permanent.
type move struct {
	src     source.Source
	subpath string
	ident   string
	skills  map[string]string // skill name -> absolute path at the pinned commit
	// from and to are the commits this move spans: the one the source was
	// pinned at before the upgrade and the one it is pinned at after. A store
	// path names a repository and a commit but never a ref, so these are the
	// only thing that tells two equipment entries for one repository at
	// different refs apart when a link has to be attributed to one of them.
	from string
	to   string
}

// LoadoutPlan is what an upgrade would do to one loadout.
type LoadoutPlan struct {
	Name string
	// Next is the loadout as it will be saved. It is a copy: planning never
	// mutates the definition on disk.
	Next    *loadout.Loadout
	Sources []SourcePlan
	Spawns  []SpawnPlan
	Errs    []error

	definitionChanged bool
}

// Failed reports whether anything went wrong, so the CLI can exit non-zero
// rather than let a failure scroll past in an otherwise successful report.
func (p *LoadoutPlan) Failed() bool {
	for _, s := range p.Sources {
		if s.Status == StatusFailed {
			return true
		}
	}
	for i := range p.Spawns {
		if len(p.Spawns[i].Errs) > 0 {
			return true
		}
	}
	return len(p.Errs) > 0
}

// Engine carries the collaborators an upgrade needs.
type Engine struct {
	Store    *store.Store
	Loadouts *loadout.Store
	Leases   *lease.Store
	Git      gitcmd.Git
	Prober   proc.Prober
}

// Plan works out what upgrading these loadouts would do.
//
// It resolves refs and fetches new commits into the content-addressed store,
// because a per-skill diff cannot be produced without the content to compare.
// Nothing else is written: no loadout definition, no symlink, no lease, no
// exclude file. Adding an entry to the store is invisible and shared, which is
// what lets `--dry-run` tell the exact truth instead of an estimate.
func (e *Engine) Plan(ctx context.Context, loadouts []*loadout.Loadout, opts Options) []*LoadoutPlan {
	// An unreadable lease record is not this command's failure. The reaper every
	// command runs first already reports it, and folding it in here would both
	// print it twice and turn a run in which every source resolved, fetched and
	// relinked cleanly into a non-zero exit.
	leases, _ := e.Leases.List()

	plans := make([]*LoadoutPlan, 0, len(loadouts))
	for _, l := range loadouts {
		plans = append(plans, e.planLoadout(ctx, l, leases, opts))
	}
	return plans
}

func (e *Engine) planLoadout(ctx context.Context, l *loadout.Loadout, leases []*lease.Lease, opts Options) *LoadoutPlan {
	next := *l
	next.Equipment = append([]loadout.Equipment(nil), l.Equipment...)
	p := &LoadoutPlan{Name: l.Name, Next: &next}

	var moves []move
	for i, eq := range l.Equipment {
		sp := e.planSource(ctx, eq, opts)
		sp.Index = i
		p.Sources = append(p.Sources, sp)

		// effective is the equipment as it will be saved.
		effective := eq
		switch sp.Status {
		case StatusUpgraded, StatusSameContent:
			effective.Commit = sp.NewCommit
			effective.Skills = sortedNames(sp.skills)
			if opts.Pin {
				effective.Source = eq.WithRef(sp.NewCommit)
			}
			next.Equipment[i] = effective
			p.definitionChanged = true
		case StatusCurrent:
			// Nothing moved, but --pin still has work: freezing a source that
			// happens to be up to date is exactly what a cautious user means.
			if opts.Pin && !source.IsCommitish(eq.Ref) {
				effective.Source = eq.WithRef(sp.NewCommit)
				next.Equipment[i] = effective
				p.definitionChanged = true
			}
		}

		if mv := e.moveFor(effective, sp); mv != nil {
			moves = append(moves, *mv)
		}
	}

	p.Spawns = e.planSpawns(ctx, l.Name, next.Equipment, leases, moves, opts)
	return p
}

// moveFor describes where this source's links should end up.
//
// It returns nil when the pinned commit is not in the store: without its tree
// there is no way to tell what should be linked, and guessing would mean
// planning removals for skills that are only unreadable, not gone.
func (e *Engine) moveFor(eq loadout.Equipment, sp SourcePlan) *move {
	mv := &move{
		src:     eq.Source,
		subpath: eq.Subpath,
		ident:   eq.Ident(),
		skills:  sp.skills,
		from:    sp.OldCommit,
		to:      eq.Commit,
	}
	if mv.skills != nil {
		return mv // already discovered at the commit we just fetched
	}
	if eq.Commit == "" || !e.Store.Has(eq.Source, eq.Commit) {
		return nil
	}
	found, err := skill.Discover(e.Store.Path(eq.Source, eq.Commit), eq.Subpath)
	if err != nil {
		return nil
	}
	selected, err := skill.Filter(found, eq.Only, eq.Except)
	if err != nil {
		return nil
	}
	mv.skills = map[string]string{}
	for _, s := range selected {
		mv.skills[s.Name] = s.AbsPath
	}
	return mv
}

// planSource re-resolves one equipped source and diffs it against its pin.
func (e *Engine) planSource(ctx context.Context, eq loadout.Equipment, opts Options) SourcePlan {
	sp := SourcePlan{
		Ident:     eq.Ident(),
		NewIdent:  eq.Ident(),
		OldCommit: eq.Commit,
		NewCommit: eq.Commit,
	}

	// A source pinned to an exact commit has nothing to resolve. Refetching it
	// would be busywork at best and a silent change of meaning at worst.
	if source.IsCommitish(eq.Ref) {
		sp.Status = StatusPinned
		return sp
	}
	if eq.Commit == "" {
		sp.Status = StatusFailed
		sp.Err = fmt.Errorf("source %s is not pinned to a commit; re-equip it", eq.Ident())
		return sp
	}

	commit, err := e.Store.Resolve(ctx, eq.Source)
	if err != nil {
		sp.Status = StatusFailed
		sp.Err = err
		return sp
	}
	sp.NewCommit = commit
	if opts.Pin {
		sp.NewIdent = eq.WithRef(commit).Ident()
	}
	if commit == eq.Commit {
		sp.Status = StatusCurrent
		return sp
	}

	dir, _, err := e.Store.Ensure(ctx, eq.Source, commit)
	if err != nil {
		sp.Status = StatusFailed
		sp.Err = err
		return sp
	}
	found, err := skill.Discover(dir, eq.Subpath)
	if err != nil {
		sp.Status = StatusFailed
		sp.Err = fmt.Errorf("scan %s@%s: %w", eq.Ident(), Short(commit), err)
		return sp
	}
	selected, err := skill.Filter(found, eq.Only, eq.Except)
	if err != nil {
		sp.Status = StatusFailed
		sp.Err = err
		return sp
	}
	sp.skills = map[string]string{}
	for _, s := range selected {
		sp.skills[s.Name] = s.AbsPath
	}

	sp.Diff, sp.Notes = e.diff(eq, selected)
	if sp.Diff.Changed() {
		sp.Status = StatusUpgraded
	} else {
		sp.Status = StatusSameContent
	}
	return sp
}

// diff compares the skills at the pinned commit with those at the new one.
//
// When the old commit is still in the store the comparison is by content
// digest, so "modified" means the skill really changed. When it is not - the
// user cleared the store, say - the loadout's recorded skill names are all
// there is to go on, and the result says so rather than pretending.
func (e *Engine) diff(eq loadout.Equipment, now []skill.Skill) (Diff, []string) {
	newIndex := skill.Index(now)

	if !e.Store.Has(eq.Source, eq.Commit) {
		d := diffNames(eq.Skills, skill.Names(now))
		d.ByName = true
		return d, []string{"commit " + Short(eq.Commit) + " is no longer in the store - compared recorded skill names only"}
	}

	oldDir := e.Store.Path(eq.Source, eq.Commit)
	found, err := skill.Discover(oldDir, eq.Subpath)
	if err != nil {
		d := diffNames(eq.Skills, skill.Names(now))
		d.ByName = true
		return d, []string{"could not rescan " + Short(eq.Commit) + ": " + err.Error()}
	}
	before, err := skill.Filter(found, eq.Only, eq.Except)
	if err != nil {
		d := diffNames(eq.Skills, skill.Names(now))
		d.ByName = true
		return d, []string{"could not refilter " + Short(eq.Commit) + ": " + err.Error()}
	}
	oldIndex := skill.Index(before)

	var d Diff
	var notes []string
	for _, name := range sortedKeys(oldIndex) {
		nw, ok := newIndex[name]
		if !ok {
			d.Removed = append(d.Removed, name)
			continue
		}
		same, err := sameContent(oldIndex[name].AbsPath, nw.AbsPath)
		if err != nil {
			notes = append(notes, "could not compare "+name+": "+err.Error())
			d.Modified = append(d.Modified, name) // unsure means "assume it moved"
			continue
		}
		if same {
			d.Unchanged = append(d.Unchanged, name)
		} else {
			d.Modified = append(d.Modified, name)
		}
	}
	for _, name := range sortedKeys(newIndex) {
		if _, ok := oldIndex[name]; !ok {
			d.Added = append(d.Added, name)
		}
	}
	return d, notes
}

func sameContent(a, b string) (bool, error) {
	da, err := skill.Digest(a)
	if err != nil {
		return false, err
	}
	db, err := skill.Digest(b)
	if err != nil {
		return false, err
	}
	return da == db, nil
}

func diffNames(before, after []string) Diff {
	old := map[string]bool{}
	for _, n := range before {
		old[n] = true
	}
	nw := map[string]bool{}
	for _, n := range after {
		nw[n] = true
	}
	var d Diff
	for _, n := range before {
		if nw[n] {
			d.Unchanged = append(d.Unchanged, n)
		} else {
			d.Removed = append(d.Removed, n)
		}
	}
	for _, n := range after {
		if !old[n] {
			d.Added = append(d.Added, n)
		}
	}
	return d
}

// Short renders a commit the way the rest of the CLI does.
func Short(commit string) string {
	if len(commit) > 8 {
		return commit[:8]
	}
	return commit
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedNames(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	return sortedKeys(m)
}
