package garrison

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/tobi404/barracks/internal/gitcmd"
	"github.com/tobi404/barracks/internal/lease"
	"github.com/tobi404/barracks/internal/loadout"
	"github.com/tobi404/barracks/internal/skill"
	"github.com/tobi404/barracks/internal/store"
	"github.com/tobi404/barracks/internal/target"
)

// ErrOccupied means a path a garrison would write is already taken by something
// barracks did not put there. barracks never overwrites such a path.
var ErrOccupied = errors.New("target path already occupied")

// ErrLocallyModified means a vendored file has been edited since it was
// committed. An update refuses rather than discarding the edit; --force is the
// user saying to take it anyway.
var ErrLocallyModified = errors.New("vendored file modified locally")

// ErrPersonalSpawn means the same paths are already held by a personal spawn.
// A repository must never have one path registered both ways: excluded from git
// as a symlink and committed as a file.
var ErrPersonalSpawn = errors.New("a personal spawn holds these paths")

// ErrNotGarrisoned means the lockfile records no garrison for that loadout.
var ErrNotGarrisoned = errors.New("not garrisoned in this repository")

// Engine carries the collaborators the committed tier needs.
//
// Deliberately no lease store for writing: a garrison never takes a lease out.
// Leases is read-only here, used only to refuse installing on top of a personal
// spawn.
type Engine struct {
	Store  *store.Store
	Leases *lease.Store
	Git    gitcmd.Git
	Now    func() time.Time
	// Loadouts is optional and read-only: Inspect uses it to say when a loadout
	// definition has moved on from what the lockfile pins. A teammate who never
	// trained the loadout has none, and everything else still works.
	Loadouts *loadout.Store
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Request describes one garrison install or update.
type Request struct {
	// Root is the repository root. A garrison is meaningless outside one.
	Root string
	// GitDir is the repository's .git directory, used only to check that the
	// files being committed are not ignored.
	GitDir string
	// Name is the loadout's name, the key the lockfile records it under.
	Name string
	// Equipment is what to materialise. It comes from a loadout definition
	// normally, and from the lockfile itself when repairing a checkout.
	Equipment []loadout.Equipment
	// Targets are the agents to install into.
	Targets []target.Target
	// Force overwrites vendored files that have been edited locally.
	Force bool
}

// Result is what an install did.
type Result struct {
	Loadout string
	Targets []string
	// New reports whether this loadout was not garrisoned here before.
	New bool
	// Wrote, Deleted, and Unchanged are repo-relative file paths, so the output
	// reads like the diff the user is about to review.
	Wrote     []string
	Deleted   []string
	Unchanged []string
	Skills    []string
	Fetched   int
	Notices   []string
	Garrison  Garrison
}

// Changed reports whether the tree or the lockfile moved.
func (r *Result) Changed() bool { return len(r.Wrote) > 0 || len(r.Deleted) > 0 }

// placement is one skill resolved in the store, before it is bound to a target.
type placement struct {
	name     string
	source   string
	storeDir string
	files    []File
}

// Install materialises the loadout into every requested target as real files and
// records the result in the lockfile.
//
// It is idempotent, which is what makes one verb enough for three jobs: the
// first install, an update after the loadout's pins moved, and repairing a
// checkout that drifted. Nothing is written until every check has passed, and a
// failure part-way through the write is rolled back, so the files and the
// lockfile can never be left disagreeing.
func (e *Engine) Install(ctx context.Context, req Request) (*Result, error) {
	if req.Root == "" {
		return nil, fmt.Errorf("a garrison needs a repository: run this inside one")
	}
	if len(req.Targets) == 0 {
		return nil, fmt.Errorf("no target to garrison %s into", req.Name)
	}
	if len(req.Equipment) == 0 {
		return nil, fmt.Errorf("loadout %q has no skills to garrison; equip it with a source first", req.Name)
	}

	m, err := Load(req.Root)
	if err != nil {
		return nil, err
	}
	prev := m.Find(req.Name)

	res := &Result{Loadout: req.Name, Targets: idsOf(req.Targets), New: prev == nil}

	places, fetched, err := e.resolve(ctx, req)
	if err != nil {
		return nil, err
	}
	res.Fetched = fetched

	next, notices, err := e.compose(req, places)
	if err != nil {
		return nil, err
	}
	res.Notices = append(res.Notices, notices...)

	if err := e.checkForeign(req, prev, next); err != nil {
		return nil, err
	}
	plan, err := planWrite(req, prev, next, places)
	if err != nil {
		return nil, err
	}
	plan.dirs = inheritDirs(plan.dirs, e.claimedDirs(req, prev), next.Skills)
	res.Notices = append(res.Notices, plan.notices...)

	if err := plan.apply(req.Root); err != nil {
		return nil, err
	}

	next.Dirs = plan.dirs
	// A run that changes nothing must leave the lockfile byte-identical. A
	// timestamp that moves every time turns `barracks garrison` into a command
	// that always dirties the repository, which would make the one signal that
	// matters - "did anything actually change" - unreadable in review.
	if prev != nil && sameRecord(*prev, next) {
		next.UpdatedAt = prev.UpdatedAt
	}
	m.Upsert(next)
	if err := m.Save(req.Root); err != nil {
		// The tree is written but the lockfile is not, which is the one state
		// this mode must not be left in. Undo the tree.
		plan.undo()
		return nil, fmt.Errorf("write %s: %w", LockName, err)
	}
	plan.done()

	res.Wrote, res.Deleted, res.Unchanged = plan.wrote, plan.deleted, plan.unchanged
	res.Garrison = next
	for _, s := range next.Skills {
		res.Skills = append(res.Skills, s.Dir)
	}
	res.Notices = append(res.Notices, e.ignoreWarnings(ctx, req, next)...)
	return res, nil
}

// resolve materialises every source in the store and reads the skills it
// contributes, with the same collision rule a personal spawn uses.
func (e *Engine) resolve(ctx context.Context, req Request) ([]placement, int, error) {
	var places []placement
	seen := map[string]string{}
	fetched := 0

	for _, eq := range req.Equipment {
		if eq.Commit == "" {
			return nil, 0, fmt.Errorf("source %s is not pinned to a commit; re-equip it", eq.Ident())
		}
		root, didFetch, err := e.Store.Ensure(ctx, eq.Source, eq.Commit)
		if err != nil {
			return nil, 0, err
		}
		if didFetch {
			fetched++
		}
		found, err := skill.Discover(root, eq.Subpath)
		if err != nil {
			return nil, 0, fmt.Errorf("scan %s: %w", eq.Ident(), err)
		}
		found, err = skill.Filter(found, eq.Only, eq.Except)
		if err != nil {
			return nil, 0, err
		}
		for _, s := range found {
			if prev, dup := seen[s.Name]; dup {
				return nil, 0, fmt.Errorf("skill %q is provided by both %s and %s; use --only or --except to disambiguate", s.Name, prev, eq.Ident())
			}
			seen[s.Name] = eq.Ident()
			places = append(places, placement{name: s.Name, source: eq.Ident(), storeDir: s.AbsPath})
		}
	}
	if len(places) == 0 {
		return nil, 0, fmt.Errorf("loadout %q has no skills to garrison; its sources contribute none", req.Name)
	}
	sort.Slice(places, func(i, j int) bool { return places[i].name < places[j].name })
	return places, fetched, nil
}

// compose turns resolved store skills into the garrison entry the lockfile will
// hold, digesting every file that will be vendored.
func (e *Engine) compose(req Request, places []placement) (Garrison, []string, error) {
	g := Garrison{
		Loadout:   req.Name,
		Targets:   idsOf(req.Targets),
		UpdatedAt: e.now().UTC().Truncate(time.Second),
	}
	for _, eq := range req.Equipment {
		g.Sources = append(g.Sources, Source{
			Source: eq.Source,
			Commit: eq.Commit,
			Only:   eq.Only,
			Except: eq.Except,
		})
	}

	var notices []string
	for i := range places {
		files, skipped, err := scan(places[i].storeDir)
		if err != nil {
			return Garrison{}, nil, fmt.Errorf("read skill %s: %w", places[i].name, err)
		}
		if len(files) == 0 {
			return Garrison{}, nil, fmt.Errorf("skill %s has no regular files to vendor", places[i].name)
		}
		for _, s := range skipped {
			notices = append(notices, fmt.Sprintf("skill %s: not vendored - %s; a committed skill has to work with no barracks and no store, and a link cannot survive a clone", places[i].name, s))
		}
		places[i].files = files
	}

	for _, t := range req.Targets {
		dir := t.RepoPath(req.Root)
		for _, p := range places {
			rel, err := relDir(req.Root, filepath.Join(dir, p.name))
			if err != nil {
				return Garrison{}, nil, err
			}
			g.Skills = append(g.Skills, Skill{
				Name:   p.name,
				Target: t.ID,
				Dir:    rel,
				Source: p.source,
				Files:  p.files,
			})
		}
	}
	return g, notices, nil
}

// checkForeign refuses an install that would land on somebody else's paths.
//
// Two directions matter, and both would leave the repository with one path
// registered two ways. A personal spawn's symlink is registered in
// .git/info/exclude, so committing a file over it would either hide the
// committed file from the team or leave the checkout permanently dirty. And
// another loadout's garrison already owns its files in the lockfile.
func (e *Engine) checkForeign(req Request, prev *Garrison, next Garrison) error {
	dirs := make([]string, 0, len(next.Skills))
	for _, s := range next.Skills {
		dirs = append(dirs, s.Dir)
	}

	if e.Leases != nil {
		leases, _ := e.Leases.List()
		var clashes []string
		for _, l := range lease.FindInScope(leases, lease.ScopeRepo, req.Root) {
			for _, link := range l.Links {
				rel, err := relDir(req.Root, link.Path)
				if err != nil {
					continue
				}
				if overlapsAny(dirs, rel) {
					clashes = append(clashes, fmt.Sprintf("%s (loadout %s, lease %s)", rel, l.Loadout, l.ID))
				}
			}
		}
		if len(clashes) > 0 {
			return fmt.Errorf("%w: %s\nrecall the personal spawn first: barracks recall - a path cannot be both excluded from git as a symlink and committed as a file",
				ErrPersonalSpawn, strings.Join(dedupe(clashes), ", "))
		}
	}

	m, err := Load(req.Root)
	if err != nil {
		return err
	}
	for i := range m.Garrisons {
		other := &m.Garrisons[i]
		if other.Loadout == req.Name {
			continue
		}
		for _, d := range dirs {
			if other.Claims(d) {
				return fmt.Errorf("%w: %s is already garrisoned by loadout %s", ErrOccupied, d, other.Loadout)
			}
		}
	}

	// Anything on disk at a skill directory this install has no record of.
	for _, d := range dirs {
		if prev != nil && prev.Claims(d) {
			continue
		}
		abs := filepath.Join(req.Root, filepath.FromSlash(d))
		fi, err := os.Lstat(abs)
		if err != nil {
			continue // absent, which is the normal case
		}
		what := describeMode(fi.Mode())
		if fi.IsDir() {
			what = "a directory barracks has no record of"
		}
		return fmt.Errorf("%w: %s already exists (%s) and was not created by barracks", ErrOccupied, d, what)
	}
	return nil
}

// claimedDirs lists the directory chains other barracks records already claim in
// this repository: the other garrisons in the lockfile, and the leases of any
// personal spawn deployed here.
//
// Both tiers can legitimately share a parent - one loadout garrisoned into
// .claude/skills/a while another is spawned into .claude/skills/b - so whichever
// record is removed last has to be the one able to prune the parent it did not
// itself create.
func (e *Engine) claimedDirs(req Request, prev *Garrison) [][]string {
	var out [][]string
	if prev != nil {
		out = append(out, prev.Dirs)
	}
	if m, err := Load(req.Root); err == nil {
		for i := range m.Garrisons {
			if m.Garrisons[i].Loadout == req.Name {
				continue
			}
			out = append(out, m.Garrisons[i].Dirs)
		}
	}
	if e.Leases != nil {
		leases, _ := e.Leases.List()
		for _, l := range lease.FindInScope(leases, lease.ScopeRepo, req.Root) {
			var rels []string
			for _, d := range l.CreatedDirs {
				if rel, err := relDir(req.Root, d); err == nil {
					rels = append(rels, rel)
				}
			}
			out = append(out, rels)
		}
	}
	return out
}

// ignoreWarnings reports vendored paths git would ignore.
//
// A committed tier that git refuses to track is the one silent failure of this
// mode: the install looks perfect, the commit contains nothing, and the teammate
// who clones gets no skills at all.
func (e *Engine) ignoreWarnings(ctx context.Context, req Request, g Garrison) []string {
	if req.GitDir == "" {
		return []string{"not inside a git repository - these files are on disk but nothing will carry them to your team"}
	}
	var ignored []string
	for _, s := range g.Skills {
		if _, err := e.Git.Run(ctx, req.Root, "check-ignore", "-q", "--", s.Dir); err == nil {
			ignored = append(ignored, s.Dir)
		}
	}
	if len(ignored) == 0 {
		return nil
	}
	return []string{fmt.Sprintf("git ignores %s - these files cannot be committed, so a teammate cloning this repository would not get them; remove the .gitignore rule or use `barracks spawn` instead",
		strings.Join(ignored, ", "))}
}

// Restore re-materialises the garrisons the lockfile records, from the lockfile
// alone.
//
// This is the repair path, and it deliberately does not consult the loadout
// definitions: a teammate who never trained these loadouts must still be able to
// put a drifted checkout back exactly as the lockfile says.
func (e *Engine) Restore(ctx context.Context, root, gitDir string, only []string, force bool) ([]*Result, error) {
	m, err := Load(root)
	if err != nil {
		return nil, err
	}
	if len(m.Garrisons) == 0 {
		return nil, fmt.Errorf("no %s here: nothing is garrisoned in this repository", LockName)
	}
	wanted := map[string]bool{}
	for _, n := range only {
		wanted[n] = true
	}

	var out []*Result
	for i := range m.Garrisons {
		g := m.Garrisons[i]
		if len(wanted) > 0 && !wanted[g.Loadout] {
			continue
		}
		targets, err := target.LookupAll(g.Targets)
		if err != nil {
			return out, fmt.Errorf("%s records a target barracks does not know for loadout %s: %w", LockName, g.Loadout, err)
		}
		res, err := e.Install(ctx, Request{
			Root:      root,
			GitDir:    gitDir,
			Name:      g.Loadout,
			Equipment: g.Equipment(),
			Targets:   targets,
			Force:     force,
		})
		if err != nil {
			return out, err
		}
		out = append(out, res)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s records no garrison for %s", LockName, strings.Join(only, ", "))
	}
	return out, nil
}

// Reinstall brings one already-garrisoned loadout onto the commits its
// definition is now pinned at, keeping the targets the lockfile records.
//
// This is the hook `barracks upgrade` uses: after upgrade re-resolves a
// loadout's sources, one call here rewrites the vendored files and the lockfile
// together, so the change arrives as a single reviewable diff.
func (e *Engine) Reinstall(ctx context.Context, root, gitDir string, l *loadout.Loadout, force bool) (*Result, error) {
	m, err := Load(root)
	if err != nil {
		return nil, err
	}
	g := m.Find(l.Name)
	if g == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotGarrisoned, l.Name)
	}
	targets, err := target.LookupAll(g.Targets)
	if err != nil {
		return nil, fmt.Errorf("%s records a target barracks does not know for loadout %s: %w", LockName, l.Name, err)
	}
	return e.Install(ctx, Request{
		Root:      root,
		GitDir:    gitDir,
		Name:      l.Name,
		Equipment: l.Equipment,
		Targets:   targets,
		Force:     force,
	})
}

// Garrisoned lists the loadouts the repository's lockfile records.
func Garrisoned(root string) ([]string, error) {
	m, err := Load(root)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(m.Garrisons))
	for _, g := range m.Garrisons {
		out = append(out, g.Loadout)
	}
	sort.Strings(out)
	return out, nil
}

// Guard answers whether a repository's committed tier claims a path.
//
// internal/spawn holds it behind an interface so that a personal spawn refuses
// to land on committed files without the spawn package having to know what a
// lockfile is.
type Guard struct{}

// Claims reports whether the lockfile at root records path, and names the
// loadout that does.
//
// An unreadable lockfile is reported as claiming nothing: this guard only ever
// adds a refusal, and a broken lockfile must not be able to block every spawn in
// the repository. `barracks inspect` is what surfaces that.
func (Guard) Claims(root, path string) (string, bool) {
	if root == "" {
		return "", false
	}
	m, err := Load(root)
	if err != nil {
		return "", false
	}
	rel, err := relDir(root, path)
	if err != nil {
		return "", false
	}
	return m.Claims(rel)
}

// sameRecord compares two garrison entries by everything except when they were
// written.
func sameRecord(a, b Garrison) bool {
	a.UpdatedAt, b.UpdatedAt = time.Time{}, time.Time{}
	return reflect.DeepEqual(a, b)
}

func idsOf(targets []target.Target) []string {
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		out = append(out, t.ID)
	}
	return out
}

// overlapsAny reports whether rel is, contains, or sits inside any of dirs.
func overlapsAny(dirs []string, rel string) bool {
	rel = strings.TrimSuffix(filepath.ToSlash(rel), "/")
	for _, d := range dirs {
		if d == rel || strings.HasPrefix(rel, d+"/") || strings.HasPrefix(d+"/", rel+"/") {
			return true
		}
	}
	return false
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
