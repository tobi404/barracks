package upgrade

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tobi404/barracks/internal/gitexclude"
	"github.com/tobi404/barracks/internal/lease"
	"github.com/tobi404/barracks/internal/loadout"
	"github.com/tobi404/barracks/internal/source"
)

// OpKind is one change to a spawned symlink.
type OpKind string

const (
	// OpAdd creates a link for a skill that appeared upstream.
	OpAdd OpKind = "add"
	// OpRemove deletes the link of a skill that disappeared upstream. A skill
	// that no longer exists must not be left behind as a dangling symlink.
	OpRemove OpKind = "remove"
	// OpRelink repoints a link at the new store entry.
	OpRelink OpKind = "relink"
)

// Op is one planned change to one spawned path.
type Op struct {
	Kind  OpKind
	Skill string
	Path  string
	From  string
	To    string
	Err   error
}

// SpawnPlan is what an upgrade would do to one live spawn.
type SpawnPlan struct {
	Lease *lease.Lease
	Ops   []Op
	// Kept is every recorded path barracks refused to touch, and why.
	Kept []lease.Kept
	// Skip is non-empty when the whole spawn was left alone, with the reason.
	Skip  string
	Notes []string
	Errs  []error
	// Recall is set when the upgrade leaves this spawn with no skills at all.
	Recall bool

	links    []lease.Link
	sources  []lease.SourceRef
	patterns []string
	gitDir   string
}

// Changed reports whether this spawn has anything to apply.
func (s *SpawnPlan) Changed() bool {
	return s.Skip == "" && (len(s.Ops) > 0 || !linksEqual(s.Lease.Links, s.links))
}

// Reportable reports whether this spawn is worth printing.
func (s *SpawnPlan) Reportable() bool {
	return s.Skip != "" || len(s.Ops) > 0 || len(s.Kept) > 0 || len(s.Notes) > 0 || len(s.Errs) > 0
}

// planSpawns works out what each live spawn of this loadout needs.
func (e *Engine) planSpawns(ctx context.Context, name string, equipment []loadout.Equipment, leases []*lease.Lease, moves []move, opts Options) []SpawnPlan {
	if len(moves) == 0 {
		return nil
	}
	var out []SpawnPlan
	for _, l := range leases {
		if l.Loadout != name {
			continue
		}
		sp := e.planSpawn(ctx, l, equipment, moves, opts)
		if sp.Reportable() || sp.Changed() {
			out = append(out, sp)
		}
	}
	return out
}

// planSpawn brings one spawn's links in line with the commits its loadout is
// pinned at, and decides whether it may.
//
// A judgment call, made here and documented in the README: a spawn whose lease
// is held by a process that is running right now is NOT relinked by default.
// That lease exists because `barracks run` started a session, and swapping
// skill directories underneath a session that has already read them is exactly
// the kind of surprise this tool exists not to produce. The session keeps what
// it started with, its lease is revoked when it exits, and the next upgrade or
// spawn brings it forward. `--include-running` overrides it.
//
// barracks skips only what it can prove is live. A manual or deadline lease may
// have a session sitting on it and barracks has no way to know, so those are
// relinked; the README says exactly that rather than implying more.
func (e *Engine) planSpawn(ctx context.Context, l *lease.Lease, equipment []loadout.Equipment, moves []move, opts Options) SpawnPlan {
	sp := SpawnPlan{Lease: l, sources: e.carriedSources(l, equipment)}

	recorded := map[string]bool{}
	for _, link := range l.Links {
		recorded[link.Skill] = true
	}

	// Every skill the sources this spawn carries provide once the upgrade is
	// done, attributed to the first source that provides it.
	//
	// A skill can leave one equipped source and appear in another. Handing the
	// path over in a single relink is what keeps the decision Plan makes equal
	// to what Apply finds on disk: a removal and an addition planned separately
	// would have the add weighed against a path the removal has not happened to
	// yet, and the skill would be dropped for the run.
	provided := map[string]*move{}
	for i := range moves {
		mv := &moves[i]
		if !carries(sp.sources, mv) {
			continue
		}
		for name := range mv.skills {
			if _, dup := provided[name]; !dup {
				provided[name] = mv
			}
		}
	}
	// What a link may be handed to when its own source stops providing its
	// skill. The same map as provided for an upgrade, and every source in the
	// plan for a removal - see Options.handOverToAnySource for why the two
	// questions differ. It never feeds the additions below.
	handover := provided
	if opts.handOverToAnySource {
		handover = map[string]*move{}
		for name, mv := range provided {
			handover[name] = mv
		}
		for i := range moves {
			mv := &moves[i]
			for name := range mv.skills {
				if _, dup := handover[name]; !dup {
					handover[name] = mv
				}
			}
		}
	}

	var final []lease.Link
	for _, link := range l.Links {
		mv := e.matchMove(link, moves)
		if mv == nil {
			final = append(final, link) // not a link any source of ours governs
			continue
		}
		target, still := mv.skills[link.Skill]
		if !still {
			// Gone from the source that put it here, but another source of this
			// spawn now provides it: one relink, over a path still proven ours.
			if hand := handover[link.Skill]; hand != nil {
				mv, target, still = hand, hand.skills[link.Skill], true
			}
		}
		if !still {
			if op, kept := planTouch(link, e.Store, OpRemove, ""); kept != nil {
				sp.Kept = append(sp.Kept, *kept)
				final = append(final, link)
			} else if op != nil {
				sp.Ops = append(sp.Ops, *op)
			}
			continue
		}
		if filepath.Clean(target) == filepath.Clean(link.Target) {
			link.Source = mv.ident // the label may be stale after --pin
			final = append(final, link)
			continue
		}
		op, kept := planTouch(link, e.Store, OpRelink, target)
		if kept != nil {
			sp.Kept = append(sp.Kept, *kept)
			final = append(final, link)
			continue
		}
		if op != nil {
			sp.Ops = append(sp.Ops, *op)
		}
		final = append(final, lease.Link{Path: link.Path, Target: target, Skill: link.Skill, Source: mv.ident})
	}

	// Skills that appeared upstream, for sources this spawn was made from.
	// provided already answers which carried source supplies each skill, so that
	// rule has exactly one owner and the handoff above and the additions here
	// can never attribute the same skill to different sources. By name, so the
	// order is the same on every run and on every platform.
	//
	// A deployment narrowed at spawn time is narrowed here too, and this is the
	// only place its selection is consulted: it gates additions exactly as
	// provenance does, and for the same reason - to stop an upgrade materialising
	// something this spawn was never made with. Every loop above is a relink or a
	// removal over a path already proven ours, and none of them may ask, or a
	// record would start deciding what gets deleted.
	for _, name := range sortedKeys(provided) {
		if recorded[name] || !l.CarriesSkill(name) {
			continue
		}
		mv := provided[name]
		path := filepath.Join(l.Dir, name)
		if occupied, reason := pathOccupied(path); occupied {
			sp.Kept = append(sp.Kept, lease.Kept{Path: path, Reason: reason})
			continue
		}
		sp.Ops = append(sp.Ops, Op{Kind: OpAdd, Skill: name, Path: path, To: mv.skills[name]})
		final = append(final, lease.Link{Path: path, Target: mv.skills[name], Skill: name, Source: mv.ident})
		recorded[name] = true
	}

	sort.Slice(final, func(i, j int) bool { return final[i].Skill < final[j].Skill })
	sp.links = final

	// Refusals are decided only once there is something to refuse, so a spawn
	// that is already where it should be prints nothing at all.
	if sp.Changed() {
		if reason := e.hold(l, opts); reason != "" {
			return SpawnPlan{
				Lease:   l,
				Skip:    reason,
				links:   append([]lease.Link(nil), l.Links...),
				sources: sp.sources,
			}
		}
	}
	sp.Recall = len(final) == 0
	sp.gitDir, sp.patterns, sp.Notes = e.planExclude(ctx, l, final)
	return sp
}

// hold reports why this spawn must be left exactly as it is, or "".
func (e *Engine) hold(l *lease.Lease, opts Options) string {
	if !opts.IncludeRunning && l.Kind == lease.KindProcess {
		if alive, _ := lease.OwnerAlive(e.Prober, l.Owner); alive {
			return fmt.Sprintf("held by %s - it keeps the skills it started with", describeOwner(l))
		}
	}
	// A target directory that is gone is not one barracks may recreate.
	// Relinking must never resurrect a directory the user deleted.
	if !dirExists(l.Dir) {
		return "target directory no longer exists"
	}
	return ""
}

// planTouch decides whether a recorded link may be acted on. Every removal and
// every repoint goes through lease.InspectLink first, so nothing barracks did
// not create is ever touched.
func planTouch(link lease.Link, guard lease.StoreGuard, kind OpKind, to string) (*Op, *lease.Kept) {
	state, kept := lease.InspectLink(link, guard)
	switch state {
	case lease.LinkForeign:
		return nil, kept
	case lease.LinkGone:
		if kind == OpRemove {
			return nil, nil // already gone; nothing to do and nothing to report
		}
		// A link the lease claims but that is not there can be made afresh.
		return &Op{Kind: OpAdd, Skill: link.Skill, Path: link.Path, To: to}, nil
	default:
		return &Op{Kind: kind, Skill: link.Skill, Path: link.Path, From: link.Target, To: to}, nil
	}
}

// matchMove finds the source a recorded link came from, by where it points
// rather than by the label it carries.
//
// The store path is the fact on disk; the label is only a string, and --pin
// rewrites it. Reading the commit out of the path is also what lets a spawn
// left behind at an older commit be recognised and brought forward.
//
// A store path names a repository and a commit but never a ref, so one repo
// equipped twice at two different refs produces two candidates for every link
// it holds. Picking the first would attribute a link to a source that does not
// provide its skill and plan a removal for it, which the next run would undo.
// Candidates are therefore ranked: the move whose commits the link actually
// sits on wins first, then the one that still provides the skill, and only then
// the longest matching subpath - so a repo equipped at both its root and a
// subdirectory still resolves to the more specific of the two.
func (e *Engine) matchMove(link lease.Link, moves []move) *move {
	var best *move
	var bestScore matchScore
	for i := range moves {
		commit, rel, ok := e.Store.Locate(moves[i].src, link.Target)
		if !ok || !underSubpath(rel, moves[i].subpath) {
			continue
		}
		score := matchScore{
			commit:  boolScore(commit == moves[i].from || commit == moves[i].to),
			skill:   boolScore(hasSkill(moves[i].skills, link.Skill)),
			subpath: len(moves[i].subpath),
		}
		if best == nil || score.beats(bestScore) {
			best, bestScore = &moves[i], score
		}
	}
	return best
}

// matchScore ranks the sources a link could have come from, most decisive
// field first.
type matchScore struct {
	commit  int
	skill   int
	subpath int
}

func (s matchScore) beats(other matchScore) bool {
	switch {
	case s.commit != other.commit:
		return s.commit > other.commit
	case s.skill != other.skill:
		return s.skill > other.skill
	default:
		return s.subpath > other.subpath
	}
}

func boolScore(b bool) int {
	if b {
		return 1
	}
	return 0
}

func hasSkill(skills map[string]string, name string) bool {
	_, ok := skills[name]
	return ok
}

// carriedSources is the set of sources this spawn was materialised from, which
// is what makes an upstream addition belong here. A source at a repository or
// subpath this spawn was never made from is not silently materialised into it
// by an upgrade. See carries for the granularity that match has.
//
// A lease records its provenance, so the answer survives a source that
// momentarily exports no skills: every link from it is removed, and the record
// is still there to re-attach the skills when they come back. Proving it from
// the links instead would destroy exactly the evidence needed.
//
// A lease written before the record existed has no Sources field, and reading
// its absence as "carries nothing" would strand every spawn already on disk.
// Those fall back to inspecting the links, which is the behaviour they were
// written under, and are given the record on the way past.
func (e *Engine) carriedSources(l *lease.Lease, equipment []loadout.Equipment) []lease.SourceRef {
	if l.HasProvenance() {
		return refreshIdents(l.Sources, equipment)
	}
	var out []lease.SourceRef
	for _, eq := range equipment {
		if !e.linksFrom(l.Links, eq.Source, eq.Subpath) {
			continue
		}
		out = append(out, lease.SourceRef{Ident: eq.Ident(), Key: eq.RepoKey(), Subpath: eq.Subpath})
	}
	return out
}

// linksFrom reports whether any recorded link points inside that source.
func (e *Engine) linksFrom(links []lease.Link, src source.Source, subpath string) bool {
	for _, link := range links {
		if _, rel, ok := e.Store.Locate(src, link.Target); ok && underSubpath(rel, subpath) {
			return true
		}
	}
	return false
}

// refreshIdents brings the recorded labels back in step with the definition.
// `--pin` rewrites a source's ref and therefore its Ident; the repository and
// subpath it is matched on cannot change, so the entry is found regardless.
func refreshIdents(recorded []lease.SourceRef, equipment []loadout.Equipment) []lease.SourceRef {
	if len(recorded) == 0 {
		return nil
	}
	out := append([]lease.SourceRef(nil), recorded...)
	for i := range out {
		for _, eq := range equipment {
			if out[i].Key == eq.RepoKey() && out[i].Subpath == eq.Subpath {
				out[i].Ident = eq.Ident()
				break
			}
		}
	}
	return out
}

// carries reports whether the spawn's provenance includes this source.
//
// The match is on repository and subpath and deliberately not on the ref, so
// one repository and subpath equipped twice at two refs is one source here and
// either entry's skills may be added to a spawn made from the other. That is
// the granularity README.md documents, and it is a consequence of `--pin`:
// pinning rewrites a source's declared ref, and a ref-sensitive comparison
// would stop recognising a spawn's own source the moment it was pinned and
// wrongly skip every later addition. Repository and subpath are the parts
// `--pin` cannot rewrite. Narrow the document, not this comparison.
func carries(sources []lease.SourceRef, mv *move) bool {
	key := mv.src.RepoKey()
	for _, s := range sources {
		if s.Key == key && s.Subpath == mv.subpath {
			return true
		}
	}
	return false
}

// underSubpath reports whether a source-relative path sits inside subpath.
func underSubpath(rel, subpath string) bool {
	if subpath == "" {
		return true
	}
	return rel == subpath || strings.HasPrefix(rel, subpath+"/")
}

// planExclude works out the .git/info/exclude patterns the spawn will need once
// relinked, so a new skill never shows up in `git status`.
func (e *Engine) planExclude(ctx context.Context, l *lease.Lease, links []lease.Link) (gitDir string, patterns []string, notes []string) {
	if l.Scope != lease.ScopeRepo || l.Root == "" {
		return "", nil, nil
	}
	if l.Exclude != nil && l.Exclude.File != "" {
		gitDir = filepath.Dir(filepath.Dir(l.Exclude.File))
	} else if dir, err := e.Git.GitDir(ctx, l.Root); err == nil {
		gitDir = dir
	}
	for _, link := range links {
		p, err := gitexclude.Pattern(l.Root, link.Path)
		if err != nil {
			continue
		}
		patterns = append(patterns, p)
	}
	if gitDir == "" && !sameStrings(patterns, excludePatterns(l)) {
		notes = append(notes, "not inside a git repository - skipped .git/info/exclude update")
	}
	return gitDir, patterns, notes
}

func excludePatterns(l *lease.Lease) []string {
	if l.Exclude == nil {
		return nil
	}
	return l.Exclude.Patterns
}

func describeOwner(l *lease.Lease) string {
	if l.Owner == nil {
		return "a running process"
	}
	cmd := l.Owner.Command
	if cmd == "" {
		cmd = "process"
	}
	return fmt.Sprintf("running pid %d (%s)", l.Owner.PID, cmd)
}

func linksEqual(a, b []lease.Link) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
