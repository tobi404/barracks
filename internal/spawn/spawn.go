// Package spawn materialises a loadout into a target directory as symlinks
// into the content-addressed store, and records a lease describing exactly
// what it created.
package spawn

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tobi404/barracks/internal/gitcmd"
	"github.com/tobi404/barracks/internal/gitexclude"
	"github.com/tobi404/barracks/internal/lease"
	"github.com/tobi404/barracks/internal/loadout"
	"github.com/tobi404/barracks/internal/skill"
	"github.com/tobi404/barracks/internal/store"
	"github.com/tobi404/barracks/internal/target"
)

// ErrAlreadySpawned means this loadout is already deployed in that directory.
var ErrAlreadySpawned = errors.New("loadout already spawned here")

// ErrOccupied means a path barracks would create is already taken by something
// it did not create. barracks never overwrites.
var ErrOccupied = errors.New("target path already occupied")

// Request describes one spawn.
type Request struct {
	Loadout  *loadout.Loadout
	Target   target.Target
	Global   bool
	Cwd      string
	Kind     lease.Kind
	Duration time.Duration
	Owner    *lease.Owner
}

// Result is what a spawn produced.
type Result struct {
	Lease   *lease.Lease
	Skills  []Placed
	Fetched int
	Notices []string
}

// Placed is one skill linked into the target directory.
type Placed struct {
	Name   string
	Path   string
	Target string
	Source string
}

// Engine carries the collaborators a spawn needs.
type Engine struct {
	Store  *store.Store
	Leases *lease.Store
	Git    gitcmd.Git
	Now    func() time.Time
	Env    func(string) string
	Home   func() (string, error)
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Location is the resolved destination of a spawn.
type Location struct {
	Scope Scope
	// Root is the repository root, empty for a global spawn.
	Root string
	// GitDir is the repository's .git directory, empty when there is none.
	GitDir string
	// Dir is the skills directory itself.
	Dir string
}

// Scope mirrors lease.Scope at the request layer.
type Scope = lease.Scope

// Resolve determines where a request materialises, without creating anything.
func (e *Engine) Resolve(ctx context.Context, req Request) (Location, error) {
	if req.Global {
		dir, err := req.Target.GlobalPath(e.Env, e.Home)
		if err != nil {
			return Location{}, err
		}
		return Location{Scope: lease.ScopeGlobal, Dir: dir}, nil
	}
	root, err := e.Git.RepoRoot(ctx, req.Cwd)
	if err != nil {
		// Outside a repository barracks still spawns, into the working
		// directory, but there is no .git/info/exclude to keep clean.
		abs, aerr := filepath.Abs(req.Cwd)
		if aerr != nil {
			return Location{}, aerr
		}
		return Location{Scope: lease.ScopeRepo, Root: abs, Dir: req.Target.RepoPath(abs)}, nil
	}
	gitDir, err := e.Git.GitDir(ctx, req.Cwd)
	if err != nil {
		gitDir = ""
	}
	return Location{Scope: lease.ScopeRepo, Root: root, GitDir: gitDir, Dir: req.Target.RepoPath(root)}, nil
}

// Plan is the set of skills a loadout would place, resolved against the store.
type Plan struct {
	Skills  []Placed
	Fetched int
}

// Materialise ensures every source in the loadout is in the store and returns
// the skills it contributes, with collisions rejected.
func (e *Engine) Materialise(ctx context.Context, l *loadout.Loadout, dir string) (Plan, error) {
	var plan Plan
	seen := map[string]string{}

	for _, eq := range l.Equipment {
		if eq.Commit == "" {
			return Plan{}, fmt.Errorf("source %s is not pinned to a commit; re-equip it", eq.Ident())
		}
		root, fetched, err := e.Store.Ensure(ctx, eq.Source, eq.Commit)
		if err != nil {
			return Plan{}, err
		}
		if fetched {
			plan.Fetched++
		}
		found, err := skill.Discover(root, eq.Subpath)
		if err != nil {
			return Plan{}, fmt.Errorf("scan %s: %w", eq.Ident(), err)
		}
		found, err = skill.Filter(found, eq.Only, eq.Except)
		if err != nil {
			return Plan{}, err
		}
		for _, s := range found {
			if prev, dup := seen[s.Name]; dup {
				return Plan{}, fmt.Errorf("skill %q is provided by both %s and %s; use --only or --except to disambiguate", s.Name, prev, eq.Ident())
			}
			seen[s.Name] = eq.Ident()
			plan.Skills = append(plan.Skills, Placed{
				Name:   s.Name,
				Path:   filepath.Join(dir, s.Name),
				Target: s.AbsPath,
				Source: eq.Ident(),
			})
		}
	}
	sort.Slice(plan.Skills, func(i, j int) bool { return plan.Skills[i].Name < plan.Skills[j].Name })
	return plan, nil
}

// Spawn materialises the loadout and writes its lease.
//
// Any failure part-way through unwinds everything this call created, so a
// failed spawn never leaves debris behind.
func (e *Engine) Spawn(ctx context.Context, req Request) (*Result, error) {
	loc, err := e.Resolve(ctx, req)
	if err != nil {
		return nil, err
	}

	existing, _ := e.Leases.List()
	for _, l := range existing {
		if l.Loadout == req.Loadout.Name && filepath.Clean(l.Dir) == filepath.Clean(loc.Dir) {
			return nil, fmt.Errorf("%w: %s is deployed in %s (lease %s)", ErrAlreadySpawned, req.Loadout.Name, loc.Dir, l.ID)
		}
	}

	plan, err := e.Materialise(ctx, req.Loadout, loc.Dir)
	if err != nil {
		return nil, err
	}
	if len(plan.Skills) == 0 {
		return nil, fmt.Errorf("loadout %q has no skills to spawn; equip it with a source first", req.Loadout.Name)
	}

	// Refuse before creating anything if a destination is taken.
	for _, s := range plan.Skills {
		if _, err := os.Lstat(s.Path); err == nil {
			return nil, fmt.Errorf("%w: %s already exists and was not created by barracks", ErrOccupied, s.Path)
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("inspect %s: %w", s.Path, err)
		}
	}

	l := &lease.Lease{
		ID:        lease.NewID(),
		Loadout:   req.Loadout.Name,
		Target:    req.Target.ID,
		Scope:     loc.Scope,
		Root:      loc.Root,
		Dir:       loc.Dir,
		Kind:      req.Kind,
		CreatedAt: e.now().UTC(),
		Owner:     req.Owner,
	}
	if l.Kind == "" {
		l.Kind = lease.KindManual
	}
	if l.Kind == lease.KindProcess {
		// Without a start token the reaper can only compare a bare PID, and a
		// recycled PID would keep a dead lease alive forever.
		if req.Owner == nil || req.Owner.PID <= 0 || req.Owner.StartToken == "" {
			return nil, fmt.Errorf("a process lease needs an owner process with an identity token")
		}
	}
	if l.Kind == lease.KindDeadline {
		if req.Duration <= 0 {
			return nil, fmt.Errorf("a deadline lease needs a positive duration")
		}
		exp := e.now().UTC().Add(req.Duration)
		l.ExpiresAt = &exp
	}

	undo := &unwinder{}
	defer func() {
		if err != nil {
			undo.run()
		}
	}()

	createdDirs, err := mkdirTracked(loc.Dir)
	if err != nil {
		return nil, err
	}
	undo.dirs = createdDirs
	// A second loadout spawning into the same directory creates nothing, so it
	// would record nothing to prune - and whichever lease is revoked last would
	// leave an empty directory behind. Inherit the chain another barracks lease
	// already claimed. Pruning still only ever removes empty directories that
	// some lease recorded creating, so nothing of the user's is at risk.
	l.CreatedDirs = withInheritedDirs(createdDirs, existing, loc.Dir)

	for _, s := range plan.Skills {
		if err = os.Symlink(s.Target, s.Path); err != nil {
			return nil, fmt.Errorf("link %s: %w", s.Path, err)
		}
		undo.links = append(undo.links, s.Path)
		l.Links = append(l.Links, lease.Link{Path: s.Path, Target: s.Target, Skill: s.Name, Source: s.Source})
	}

	result := &Result{Lease: l, Skills: plan.Skills, Fetched: plan.Fetched}

	// Register in .git/info/exclude, never .gitignore: a spawn must leave
	// `git status` clean without touching a committed file.
	if loc.Scope == lease.ScopeRepo && loc.GitDir != "" && loc.Root != "" {
		patterns := make([]string, 0, len(l.Links))
		for _, link := range l.Links {
			p, perr := gitexclude.Pattern(loc.Root, link.Path)
			if perr != nil {
				continue
			}
			patterns = append(patterns, p)
		}
		var rec *gitexclude.Record
		rec, err = gitexclude.Add(loc.GitDir, l.ID, patterns)
		if err != nil {
			return nil, err
		}
		l.Exclude = rec
		undo.exclude = rec
		undo.leaseID = l.ID
	} else if loc.Scope == lease.ScopeRepo {
		result.Notices = append(result.Notices,
			"not inside a git repository - skipped .git/info/exclude registration")
	}

	if err = e.Leases.Save(l); err != nil {
		return nil, fmt.Errorf("write lease: %w", err)
	}
	return result, nil
}

// RollbackError is a failed multi-target spawn together with what unwinding the
// targets it had already created could not undo.
//
// The reports travel with the error on purpose: revocation keeps anything it
// does not recognise as its own, and a caller must not be able to print the
// failure without also being handed everything the rollback left behind. A
// rollback that removed an exclude block but kept its symlink would otherwise
// dirty `git status` in complete silence.
type RollbackError struct {
	Err     error
	Reports []*lease.Report
}

func (e *RollbackError) Error() string { return e.Err.Error() }

func (e *RollbackError) Unwrap() error { return e.Err }

// SpawnAll materialises the loadout into every target in one go, and is
// all-or-nothing: if the third target fails, the first two are revoked before
// the error is returned.
//
// A loadout that installs into two agents is one user action, so a half-done
// result would be worse than no result - the user would have to work out which
// half happened before retrying.
func (e *Engine) SpawnAll(ctx context.Context, req Request, targets []target.Target) ([]*Result, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("no target to spawn %s into", req.Loadout.Name)
	}
	var done []*Result
	for _, tgt := range targets {
		perTarget := req
		perTarget.Target = tgt
		res, err := e.Spawn(ctx, perTarget)
		if err != nil {
			wrapped := fmt.Errorf("spawn into %s: %w", tgt.Display, err)
			var reports []*lease.Report
			for i := len(done) - 1; i >= 0; i-- {
				reports = append(reports, lease.Revoke(done[i].Lease, e.Store, e.Leases,
					"another target in the same spawn failed"))
			}
			return nil, &RollbackError{Err: wrapped, Reports: reports}
		}
		done = append(done, res)
	}
	return done, nil
}

// unwinder undoes a partially completed spawn.
type unwinder struct {
	links   []string
	dirs    []string
	exclude *gitexclude.Record
	leaseID string
}

func (u *unwinder) run() {
	if u.exclude != nil {
		_ = gitexclude.Remove(u.exclude, u.leaseID)
	}
	for i := len(u.links) - 1; i >= 0; i-- {
		fi, err := os.Lstat(u.links[i])
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			continue // only ever remove links this spawn just made
		}
		_ = os.Remove(u.links[i])
	}
	for i := len(u.dirs) - 1; i >= 0; i-- {
		_ = os.Remove(u.dirs[i])
	}
}

// mkdirTracked creates dir and returns only the directories it actually had to
// create, shallowest first. Pre-existing directories are never recorded, so
// revocation can never prune something that was already there.
func mkdirTracked(dir string) ([]string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	var missing []string
	for p := abs; ; p = filepath.Dir(p) {
		if _, err := os.Stat(p); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("inspect %s: %w", p, err)
		}
		missing = append(missing, p)
		if parent := filepath.Dir(p); parent == p {
			break
		}
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", abs, err)
	}
	// missing is deepest-first; store shallowest-first for readability.
	for i, j := 0, len(missing)-1; i < j; i, j = i+1, j-1 {
		missing[i], missing[j] = missing[j], missing[i]
	}
	return missing, nil
}

// withInheritedDirs adds directories on the path to dir that another lease
// already recorded creating, so the last lease to be revoked can finish the
// cleanup the first one could not.
func withInheritedDirs(created []string, others []*lease.Lease, dir string) []string {
	seen := map[string]bool{}
	for _, d := range created {
		seen[filepath.Clean(d)] = true
	}
	out := append([]string(nil), created...)
	for _, other := range others {
		for _, d := range other.CreatedDirs {
			clean := filepath.Clean(d)
			if seen[clean] || !isAncestorOrSelf(clean, dir) {
				continue
			}
			seen[clean] = true
			out = append(out, clean)
		}
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) < len(out[j]) })
	return out
}

// isAncestorOrSelf reports whether candidate is dir or one of its parents.
func isAncestorOrSelf(candidate, dir string) bool {
	rel, err := filepath.Rel(filepath.Clean(candidate), filepath.Clean(dir))
	if err != nil {
		return false
	}
	return rel == "." || !strings.HasPrefix(rel, "..")
}
