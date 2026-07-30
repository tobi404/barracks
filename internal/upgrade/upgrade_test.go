package upgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/tobi404/barracks/internal/gitcmd"
	"github.com/tobi404/barracks/internal/gitexclude"
	"github.com/tobi404/barracks/internal/lease"
	"github.com/tobi404/barracks/internal/loadout"
	"github.com/tobi404/barracks/internal/proc"
	"github.com/tobi404/barracks/internal/skill"
	"github.com/tobi404/barracks/internal/source"
	"github.com/tobi404/barracks/internal/store"
	"github.com/tobi404/barracks/internal/testutil"
)

// fakeProber answers the liveness question without needing a real process, so
// the running-session judgment call is testable on every platform.
type fakeProber struct{ alive map[int]string }

func (p *fakeProber) Identity(pid int) (string, error) {
	if tok, ok := p.alive[pid]; ok {
		return tok, nil
	}
	return "", proc.ErrNotRunning
}

// fixture is a whole barracks installation on disk: a store, a loadouts
// directory, a leases directory, and a git work tree to spawn into. Every
// repository it talks to is a local fixture, so nothing here touches the
// network.
type fixture struct {
	t      *testing.T
	root   string
	work   *testutil.GitRepo
	eng    *Engine
	prober *fakeProber
	ctx    context.Context
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	// On macOS the temp root is reached through a symlink, so git reports a
	// different absolute path than t.TempDir() hands out. Resolve it once and
	// every later comparison is between like and like.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp root: %v", err)
	}
	prober := &fakeProber{alive: map[int]string{}}
	f := &fixture{
		t:      t,
		root:   root,
		work:   testutil.NewGitRepo(t, filepath.Join(root, "work")),
		prober: prober,
		ctx:    context.Background(),
	}
	f.eng = &Engine{
		Store:    store.New(filepath.Join(root, "store"), filepath.Join(root, "mirrors"), gitcmd.Git{}),
		Loadouts: loadout.NewStore(filepath.Join(root, "loadouts")),
		Leases:   lease.NewStore(filepath.Join(root, "leases")),
		Git:      gitcmd.Git{},
		Prober:   prober,
	}
	return f
}

// source creates a fixture repository under the fixture's sources directory.
func (f *fixture) source(name string, skills ...testutil.Skill) *testutil.GitRepo {
	f.t.Helper()
	if len(skills) == 0 {
		skills = []testutil.Skill{
			{Path: "skills/react"},
			{Path: "skills/css"},
			{Path: "skills/legacy"},
		}
	}
	return testutil.NewSkillRepo(f.t, filepath.Join(f.root, "src", name), skills...)
}

func (f *fixture) skillsDir() string {
	return filepath.Join(f.work.Dir, ".claude", "skills")
}

func (f *fixture) train(name string) *loadout.Loadout {
	f.t.Helper()
	l, err := f.eng.Loadouts.Create(name, "", time.Unix(0, 0).UTC())
	if err != nil {
		f.t.Fatalf("train %s: %v", name, err)
	}
	return l
}

// equip attaches a source to the loadout exactly as `barracks equip` would:
// resolved to a concrete commit, fetched into the store, with the skills it
// exports recorded.
func (f *fixture) equip(l *loadout.Loadout, raw string, only ...string) {
	f.t.Helper()
	src, err := source.Parse(raw)
	if err != nil {
		f.t.Fatalf("parse %q: %v", raw, err)
	}
	commit, err := f.eng.Store.Resolve(f.ctx, src)
	if err != nil {
		f.t.Fatalf("resolve %q: %v", raw, err)
	}
	dir, _, err := f.eng.Store.Ensure(f.ctx, src, commit)
	if err != nil {
		f.t.Fatalf("fetch %q: %v", raw, err)
	}
	found, err := skill.Discover(dir, src.Subpath)
	if err != nil {
		f.t.Fatalf("scan %q: %v", raw, err)
	}
	selected, err := skill.Filter(found, only, nil)
	if err != nil {
		f.t.Fatalf("filter %q: %v", raw, err)
	}
	l.Equipment = append(l.Equipment, loadout.Equipment{
		Source:     src,
		Commit:     commit,
		Only:       only,
		Skills:     skill.Names(selected),
		EquippedAt: time.Unix(0, 0).UTC(),
	})
	if err := f.eng.Loadouts.Save(l); err != nil {
		f.t.Fatalf("save loadout: %v", err)
	}
}

// spawn materialises the loadout into dir the way `barracks spawn` does: one
// symlink per skill, a lease recording every path and the sources it came from,
// and a fenced block in .git/info/exclude.
func (f *fixture) spawn(l *loadout.Loadout, kind lease.Kind) *lease.Lease {
	f.t.Helper()
	dir := f.skillsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatalf("mkdir %s: %v", dir, err)
	}
	lz := &lease.Lease{
		Version:   lease.FormatVersion,
		ID:        lease.NewID(),
		Loadout:   l.Name,
		Target:    "claude-code",
		Scope:     lease.ScopeRepo,
		Root:      f.work.Dir,
		Dir:       dir,
		Kind:      kind,
		CreatedAt: time.Unix(0, 0).UTC(),
	}
	for _, eq := range l.Equipment {
		for _, s := range f.exports(eq) {
			p := filepath.Join(dir, s.Name)
			if err := os.Symlink(s.AbsPath, p); err != nil {
				f.t.Fatalf("link %s: %v", p, err)
			}
			lz.Links = append(lz.Links, lease.Link{Path: p, Target: s.AbsPath, Skill: s.Name, Source: eq.Ident()})
		}
		lz.Sources = append(lz.Sources, lease.SourceRef{Ident: eq.Ident(), Key: eq.RepoKey(), Subpath: eq.Subpath})
	}
	sort.Slice(lz.Links, func(i, j int) bool { return lz.Links[i].Skill < lz.Links[j].Skill })

	var patterns []string
	for _, link := range lz.Links {
		p, err := gitexclude.Pattern(lz.Root, link.Path)
		if err != nil {
			f.t.Fatalf("exclude pattern for %s: %v", link.Path, err)
		}
		patterns = append(patterns, p)
	}
	gitDir, err := f.eng.Git.GitDir(f.ctx, lz.Root)
	if err != nil {
		f.t.Fatalf("git dir for %s: %v", lz.Root, err)
	}
	rec, err := gitexclude.Add(gitDir, lz.ID, patterns)
	if err != nil {
		f.t.Fatalf("register exclude: %v", err)
	}
	lz.Exclude = rec

	if err := f.eng.Leases.Save(lz); err != nil {
		f.t.Fatalf("save lease: %v", err)
	}
	return lz
}

// exports is the skill set one equipment entry contributes at its pinned commit.
func (f *fixture) exports(eq loadout.Equipment) []skill.Skill {
	f.t.Helper()
	found, err := skill.Discover(f.eng.Store.Path(eq.Source, eq.Commit), eq.Subpath)
	if err != nil {
		f.t.Fatalf("scan %s: %v", eq.Ident(), err)
	}
	selected, err := skill.Filter(found, eq.Only, eq.Except)
	if err != nil {
		f.t.Fatalf("filter %s: %v", eq.Ident(), err)
	}
	return selected
}

// upgrade plans and applies, returning the plan for the named loadout as it was
// read fresh off disk - which is what a second `barracks upgrade` would see.
func (f *fixture) upgrade(name string, opts Options) *LoadoutPlan {
	f.t.Helper()
	p := f.plan(name, opts)
	f.eng.Apply([]*LoadoutPlan{p})
	return p
}

func (f *fixture) plan(name string, opts Options) *LoadoutPlan {
	f.t.Helper()
	l, err := f.eng.Loadouts.Get(name)
	if err != nil {
		f.t.Fatalf("load loadout %s: %v", name, err)
	}
	plans := f.eng.Plan(f.ctx, []*loadout.Loadout{l}, opts)
	if len(plans) != 1 {
		f.t.Fatalf("Plan returned %d plans, want 1", len(plans))
	}
	return plans[0]
}

// spawnPlan is the single spawn plan the run produced, or a fatal error.
func (f *fixture) spawnPlan(p *LoadoutPlan) *SpawnPlan {
	f.t.Helper()
	if len(p.Spawns) != 1 {
		f.t.Fatalf("plan has %d spawns, want 1", len(p.Spawns))
	}
	return &p.Spawns[0]
}

func (f *fixture) leaseOf(id string) *lease.Lease {
	f.t.Helper()
	l, err := f.eng.Leases.Get(id)
	if err != nil {
		f.t.Fatalf("reload lease %s: %v", id, err)
	}
	return l
}

// body is what the skill directory at path resolves to, failing if it dangles.
func body(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(path, skill.Manifest))
	if err != nil {
		t.Fatalf("read through %s: %v", path, err)
	}
	return string(b)
}

// occupantBody reads whatever the user left at path, whether that is a file of
// their own or a directory holding a skill.
func occupantBody(t *testing.T, path string) string {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if fi.IsDir() {
		return body(t, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func dest(t *testing.T, path string) string {
	t.Helper()
	d, err := os.Readlink(path)
	if err != nil {
		t.Fatalf("readlink %s: %v", path, err)
	}
	return d
}

// opsFor is every planned operation touching the named skill.
func opsFor(sp *SpawnPlan, name string) []Op {
	var out []Op
	for _, op := range sp.Ops {
		if op.Skill == name {
			out = append(out, op)
		}
	}
	return out
}

func keptPaths(sp *SpawnPlan) []string {
	out := make([]string, 0, len(sp.Kept))
	for _, k := range sp.Kept {
		out = append(out, k.Path)
	}
	return out
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// TestUpgradeFollowsAMovedRef is the happy path: the ref moves, the diff is
// established by content, and the spawn ends up on the new store entry.
func TestUpgradeFollowsAMovedRef(t *testing.T) {
	f := newFixture(t)
	repo := f.source("skills")
	l := f.train("frontend")
	f.equip(l, repo.Dir+"#main:skills")
	f.spawn(l, lease.KindManual)

	repo.AddSkills(t,
		testutil.Skill{Path: "skills/react", Body: "---\nname: react\n---\n\nversion two\n"},
		testutil.Skill{Path: "skills/hooks"},
	)
	repo.RemovePath(t, "skills/legacy")
	head := repo.Commit(t, "move on")

	p := f.upgrade("frontend", Options{})
	if p.Failed() {
		t.Fatalf("upgrade failed: %v", p.Errs)
	}
	sp := p.Sources[0]
	if sp.Status != StatusUpgraded {
		t.Fatalf("status = %q, want %q", sp.Status, StatusUpgraded)
	}
	if sp.NewCommit != head {
		t.Errorf("new commit = %s, want %s", sp.NewCommit, head)
	}
	if got := strings.Join(sp.Diff.Modified, ","); got != "react" {
		t.Errorf("modified = %v, want just react", sp.Diff.Modified)
	}
	if got := strings.Join(sp.Diff.Added, ","); got != "hooks" {
		t.Errorf("added = %v, want just hooks", sp.Diff.Added)
	}
	if got := strings.Join(sp.Diff.Removed, ","); got != "legacy" {
		t.Errorf("removed = %v, want just legacy", sp.Diff.Removed)
	}
	if got := strings.Join(sp.Diff.Unchanged, ","); got != "css" {
		t.Errorf("unchanged = %v, want just css", sp.Diff.Unchanged)
	}
	if sp.Diff.ByName {
		t.Error("the old commit was in the store, so the diff must not fall back to names")
	}

	if got := body(t, filepath.Join(f.skillsDir(), "react")); !strings.Contains(got, "version two") {
		t.Errorf("react still resolves to the old content: %q", got)
	}
	if _, err := os.Lstat(filepath.Join(f.skillsDir(), "hooks")); err != nil {
		t.Errorf("the new skill was not linked: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(f.skillsDir(), "legacy")); !os.IsNotExist(err) {
		t.Error("legacy survived its removal upstream")
	}
	if got := f.work.Status(t); got != "" {
		t.Errorf("upgrade dirtied git status:\n%s", got)
	}

	// The definition records the new commit, so a second run has nothing to do.
	next := f.plan("frontend", Options{})
	if next.Sources[0].Status != StatusCurrent {
		t.Errorf("second run status = %q, want %q", next.Sources[0].Status, StatusCurrent)
	}
	if len(next.Spawns) != 0 {
		t.Errorf("second run still wants to change the spawn: %+v", next.Spawns)
	}
}

// TestUpgradeHandsASkillFromOneSourceToAnother is the regression guard for the
// bug where a skill that migrates between two equipped sources was dropped by
// the very upgrade that performed the migration: the removal was planned, the
// addition was suppressed, and the spawn spent a whole interval without it.
func TestUpgradeHandsASkillFromOneSourceToAnother(t *testing.T) {
	f := newFixture(t)
	alpha := f.source("alpha", testutil.Skill{Path: "skills/react"}, testutil.Skill{Path: "skills/shared"})
	beta := f.source("beta", testutil.Skill{Path: "skills/other"})

	l := f.train("frontend")
	f.equip(l, alpha.Dir+"#main:skills")
	f.equip(l, beta.Dir+"#main:skills")
	lz := f.spawn(l, lease.KindManual)

	react := filepath.Join(f.skillsDir(), "react")
	before := dest(t, react)

	alpha.RemovePath(t, "skills/react")
	alpha.Commit(t, "hand react over")
	beta.AddSkills(t, testutil.Skill{Path: "skills/react", Body: "---\nname: react\n---\n\nreact from beta\n"})
	beta.Commit(t, "take react on")

	p := f.plan("frontend", Options{})
	sp := f.spawnPlan(p)

	ops := opsFor(sp, "react")
	if len(ops) != 1 {
		t.Fatalf("react ops = %+v, want exactly one", ops)
	}
	if ops[0].Kind != OpRelink {
		t.Errorf("react op kind = %q, want %q - a remove/add pair races itself", ops[0].Kind, OpRelink)
	}
	if ops[0].From != before {
		t.Errorf("relink verifies against %q, want the recorded target %q", ops[0].From, before)
	}
	if len(sp.Kept) != 0 {
		t.Errorf("nothing foreign is present, but paths were kept: %+v", sp.Kept)
	}

	f.eng.Apply([]*LoadoutPlan{p})

	if got := body(t, react); !strings.Contains(got, "react from beta") {
		t.Fatalf("react was not handed over in this run: %q", got)
	}
	if got := dest(t, react); !strings.Contains(got, "beta@") {
		t.Errorf("react points at %s, want an entry of the beta source", got)
	}
	for _, name := range []string{"shared", "other"} {
		if _, err := os.Lstat(filepath.Join(f.skillsDir(), name)); err != nil {
			t.Errorf("%s was lost during the handover: %v", name, err)
		}
	}

	// The lease now attributes react to the source that actually provides it.
	saved := f.leaseOf(lz.ID)
	for _, link := range saved.Links {
		if link.Skill != "react" {
			continue
		}
		if !strings.Contains(link.Source, "beta") {
			t.Errorf("react is recorded as coming from %q, want the beta source", link.Source)
		}
	}
	if got := f.work.Status(t); got != "" {
		t.Errorf("the handover dirtied git status:\n%s", got)
	}
}

// TestUpgradeNeverTouchesAForeignPath: a recorded path that is no longer the
// symlink barracks made is left exactly as it is and reported. Silence would be
// the bug.
func TestUpgradeNeverTouchesAForeignPath(t *testing.T) {
	takeOver := []struct {
		name string
		take func(t *testing.T, path string)
	}{
		{
			name: "a regular file",
			take: func(t *testing.T, path string) {
				testutil.WriteFile(t, path, "mine, not yours\n")
			},
		},
		{
			name: "a real directory",
			take: func(t *testing.T, path string) {
				testutil.WriteFile(t, filepath.Join(path, skill.Manifest), "mine, not yours\n")
			},
		},
		{
			name: "a symlink pointing elsewhere",
			take: func(t *testing.T, path string) {
				elsewhere := filepath.Join(filepath.Dir(filepath.Dir(path)), "elsewhere")
				testutil.WriteFile(t, filepath.Join(elsewhere, skill.Manifest), "mine, not yours\n")
				if err := os.Symlink(elsewhere, path); err != nil {
					t.Fatalf("link %s: %v", path, err)
				}
			},
		},
	}

	for _, tt := range takeOver {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			repo := f.source("skills")
			l := f.train("frontend")
			f.equip(l, repo.Dir+"#main:skills")
			f.spawn(l, lease.KindManual)

			taken := filepath.Join(f.skillsDir(), "react")
			if err := os.Remove(taken); err != nil {
				t.Fatalf("clear %s: %v", taken, err)
			}
			tt.take(t, taken)

			// One path the upgrade wants to repoint, and one it wants to create.
			occupied := filepath.Join(f.skillsDir(), "hooks")
			testutil.WriteFile(t, filepath.Join(occupied, skill.Manifest), "also mine\n")

			repo.AddSkills(t,
				testutil.Skill{Path: "skills/react", Body: "upstream react\n"},
				testutil.Skill{Path: "skills/hooks"},
			)
			repo.Commit(t, "move on")

			p := f.upgrade("frontend", Options{})
			if p.Failed() {
				t.Fatalf("a foreign path must be reported, not fail the run: %v", p.Errs)
			}
			sp := f.spawnPlan(p)
			for _, want := range []string{taken, occupied} {
				if !contains(keptPaths(sp), want) {
					t.Errorf("%s was not reported among the kept paths %v", want, keptPaths(sp))
				}
			}
			if got := occupantBody(t, taken); !strings.Contains(got, "mine, not yours") {
				t.Errorf("the user's path was replaced: %q", got)
			}
			if got := body(t, occupied); !strings.Contains(got, "also mine") {
				t.Errorf("the user's path was overwritten: %q", got)
			}
			// A link barracks really did create still moves on as normal.
			if !strings.Contains(dest(t, filepath.Join(f.skillsDir(), "css")), repo.Head(t)) {
				t.Error("a link barracks created was not relinked")
			}
		})
	}
}

// TestUpgradeKeepsOneRepoEquippedAtTwoRefsStable: a store path names a
// repository and a commit but never a ref, so one repo equipped twice produces
// two candidates for every link it holds. Attributing a link to the wrong one
// plans a removal the next run undoes, and the spawn oscillates forever.
func TestUpgradeKeepsOneRepoEquippedAtTwoRefsStable(t *testing.T) {
	f := newFixture(t)
	repo := f.source("skills", testutil.Skill{Path: "skills/alpha"}, testutil.Skill{Path: "skills/beta"})
	repo.CheckoutNew(t, "v1")
	repo.Checkout(t, "main")
	repo.AddSkills(t, testutil.Skill{Path: "skills/alpha", Body: "alpha moved on\n"})
	repo.Commit(t, "move main past v1")

	l := f.train("frontend")
	f.equip(l, repo.Dir+"#main:skills", "alpha")
	f.equip(l, repo.Dir+"#v1:skills", "beta")
	f.spawn(l, lease.KindManual)

	beta := filepath.Join(f.skillsDir(), "beta")
	want := dest(t, beta)

	for run := 1; run <= 2; run++ {
		p := f.upgrade("frontend", Options{})
		if p.Failed() {
			t.Fatalf("run %d failed: %v", run, p.Errs)
		}
		for _, sp := range p.Spawns {
			if ops := opsFor(&sp, "beta"); len(ops) != 0 {
				t.Errorf("run %d moved a link the other ref governs: %+v", run, ops)
			}
		}
		if got := dest(t, beta); got != want {
			t.Errorf("run %d repointed beta: %s -> %s", run, want, got)
		}
	}
	if got := dest(t, filepath.Join(f.skillsDir(), "alpha")); !strings.Contains(got, repo.Head(t)) {
		t.Errorf("alpha is not on main's commit: %s", got)
	}
}

// TestUpgradeFallsBackForALeaseWithoutProvenance: a record written before leases
// carried a source list must upgrade under the behaviour it was written under.
// Reading the missing field as "came from nothing" would turn every spawn
// already on disk into a mass removal.
func TestUpgradeFallsBackForALeaseWithoutProvenance(t *testing.T) {
	f := newFixture(t)
	repo := f.source("skills")
	l := f.train("frontend")
	f.equip(l, repo.Dir+"#main:skills")
	lz := f.spawn(l, lease.KindManual)

	aged := f.leaseOf(lz.ID)
	aged.Version = 0
	aged.Sources = nil
	if err := f.eng.Leases.Save(aged); err != nil {
		t.Fatalf("age the record: %v", err)
	}

	repo.AddSkills(t, testutil.Skill{Path: "skills/react", Body: "version two\n"})
	repo.Commit(t, "move on")

	p := f.upgrade("frontend", Options{})
	if p.Failed() {
		t.Fatalf("upgrade of an old record failed: %v", p.Errs)
	}
	sp := f.spawnPlan(p)
	for _, op := range sp.Ops {
		if op.Kind == OpRemove {
			t.Errorf("an old record had a link removed from it: %+v", op)
		}
	}
	for _, name := range []string{"react", "css", "legacy"} {
		if _, err := os.Lstat(filepath.Join(f.skillsDir(), name)); err != nil {
			t.Errorf("%s was removed from a spawn whose lease had no provenance: %v", name, err)
		}
	}
	if got := body(t, filepath.Join(f.skillsDir(), "react")); !strings.Contains(got, "version two") {
		t.Errorf("the old record was not relinked: %q", got)
	}

	// Having established it the only way an old record allows, upgrade records it.
	saved := f.leaseOf(lz.ID)
	if !saved.HasProvenance() {
		t.Error("upgrade did not bring the old record up to the current format")
	}
	if len(saved.Sources) != 1 {
		t.Errorf("recorded sources = %v, want the one equipped source", saved.Sources)
	}
}

// TestUpgradeReattachesASourceThatExportedNothing: a source exporting nothing
// loses every link that proves the spawn carries it. The recorded provenance is
// what lets the skills come back rather than the spawn diverging silently from
// the loadout that still declares the source.
func TestUpgradeReattachesASourceThatExportedNothing(t *testing.T) {
	f := newFixture(t)
	repo := f.source("skills", testutil.Skill{Path: "pack-a/alpha"}, testutil.Skill{Path: "pack-b/beta"})
	// pack-b must survive beta's removal as a directory, or the source stops
	// resolving altogether rather than merely exporting nothing.
	testutil.WriteFile(t, filepath.Join(repo.Dir, "pack-b", "README.md"), "not a skill\n")
	repo.Commit(t, "keep pack-b addressable")

	l := f.train("frontend")
	f.equip(l, repo.Dir+"#main:pack-a")
	f.equip(l, repo.Dir+"#main:pack-b")
	lz := f.spawn(l, lease.KindManual)

	beta := filepath.Join(f.skillsDir(), "beta")
	repo.RemovePath(t, "pack-b/beta")
	repo.Commit(t, "drop beta")

	p := f.upgrade("frontend", Options{})
	if p.Failed() {
		t.Fatalf("upgrade failed: %v", p.Errs)
	}
	if _, err := os.Lstat(beta); !os.IsNotExist(err) {
		t.Fatal("beta survived its removal upstream")
	}
	if _, err := os.Lstat(filepath.Join(f.skillsDir(), "alpha")); err != nil {
		t.Fatalf("the other source's skill was lost with it: %v", err)
	}
	if got := len(f.leaseOf(lz.ID).Sources); got != 2 {
		t.Fatalf("recorded sources = %d, want both - the empty one must not be forgotten", got)
	}

	repo.AddSkills(t, testutil.Skill{Path: "pack-b/beta", Body: "beta is back\n"})
	repo.Commit(t, "restore beta")

	p = f.upgrade("frontend", Options{})
	sp := f.spawnPlan(p)
	ops := opsFor(sp, "beta")
	if len(ops) != 1 || ops[0].Kind != OpAdd {
		t.Fatalf("beta ops = %+v, want one add", ops)
	}
	if got := body(t, beta); !strings.Contains(got, "beta is back") {
		t.Errorf("beta was not re-attached: %q", got)
	}
}

// TestUpgradeDoesNotAddFromASourceEquippedAfterTheSpawn is one half of the
// documented boundary: provenance gates additions, so a source at a repository
// the spawn was never made from is not silently materialised into it. Its
// companion below pins the other half - what the same repository and subpath at
// a different ref does - and the two together are exactly the guarantee
// README.md states.
func TestUpgradeDoesNotAddFromASourceEquippedAfterTheSpawn(t *testing.T) {
	f := newFixture(t)
	alpha := f.source("alpha", testutil.Skill{Path: "skills/alpha"})
	beta := f.source("beta", testutil.Skill{Path: "skills/beta"})

	l := f.train("frontend")
	f.equip(l, alpha.Dir+"#main:skills")
	f.spawn(l, lease.KindManual)

	l, err := f.eng.Loadouts.Get("frontend")
	if err != nil {
		t.Fatal(err)
	}
	f.equip(l, beta.Dir+"#main:skills")

	p := f.upgrade("frontend", Options{})
	if p.Failed() {
		t.Fatalf("upgrade failed: %v", p.Errs)
	}
	if _, err := os.Lstat(filepath.Join(f.skillsDir(), "beta")); !os.IsNotExist(err) {
		t.Error("a source equipped after the spawn was materialised into it")
	}
}

// TestUpgradeAddsFromTheSameRepoAndSubpathEquippedAtAnotherRef is the other half
// of that boundary, and is not a contradiction of it: spawn membership is
// recorded per repository and subpath, never per ref, so equipping the same
// repository and subpath at a second ref after the spawn counts as the source
// the spawn already carries and its skills do land there.
//
// The ref is left out of the match because `--pin` rewrites it; a ref-sensitive
// comparison would stop recognising a spawn's own source the moment it was
// pinned. README.md documents this case with the same three commands.
func TestUpgradeAddsFromTheSameRepoAndSubpathEquippedAtAnotherRef(t *testing.T) {
	f := newFixture(t)
	repo := f.source("skills", testutil.Skill{Path: "skills/react"}, testutil.Skill{Path: "skills/legacy"})
	repo.CheckoutNew(t, "v1")
	v1 := repo.Head(t)
	repo.Checkout(t, "main")
	repo.AddSkills(t, testutil.Skill{Path: "skills/react", Body: "main moved past v1\n"})
	repo.Commit(t, "move main past v1")

	l := f.train("frontend")
	f.equip(l, repo.Dir+"#main:skills", "react")
	f.spawn(l, lease.KindManual)

	legacy := filepath.Join(f.skillsDir(), "legacy")
	if _, err := os.Lstat(legacy); !os.IsNotExist(err) {
		t.Fatal("the spawn already carried legacy; the test proves nothing")
	}

	l, err := f.eng.Loadouts.Get("frontend")
	if err != nil {
		t.Fatal(err)
	}
	f.equip(l, repo.Dir+"#v1:skills", "legacy")

	p := f.upgrade("frontend", Options{})
	if p.Failed() {
		t.Fatalf("upgrade failed: %v", p.Errs)
	}
	sp := f.spawnPlan(p)
	ops := opsFor(sp, "legacy")
	if len(ops) != 1 || ops[0].Kind != OpAdd {
		t.Fatalf("legacy ops = %+v, want one add", ops)
	}
	if _, err := os.Lstat(legacy); err != nil {
		t.Fatalf("the second ref of the same repository and subpath was not materialised: %v", err)
	}
	if got := dest(t, legacy); !strings.Contains(got, v1) {
		t.Errorf("legacy points at %s, want the entry for the v1 commit %s", got, v1)
	}
	if got := body(t, legacy); got == "" {
		t.Error("legacy does not resolve to the store entry")
	}
	if got := f.work.Status(t); got != "" {
		t.Errorf("the addition dirtied git status:\n%s", got)
	}
}

// TestUpgradeLeavesARunningSessionAlone documents the judgment call, and that
// --include-running is the way past it.
func TestUpgradeLeavesARunningSessionAlone(t *testing.T) {
	f := newFixture(t)
	repo := f.source("skills")
	l := f.train("frontend")
	f.equip(l, repo.Dir+"#main:skills")
	lz := f.spawn(l, lease.KindProcess)

	lz.Owner = &lease.Owner{PID: 4242, StartToken: "tok", Command: "a-live-agent-session"}
	if err := f.eng.Leases.Save(lz); err != nil {
		t.Fatal(err)
	}
	f.prober.alive[4242] = "tok"

	repo.AddSkills(t, testutil.Skill{Path: "skills/react", Body: "version two\n"})
	repo.Commit(t, "move on")

	p := f.upgrade("frontend", Options{})
	sp := f.spawnPlan(p)
	if sp.Skip == "" {
		t.Fatal("a live session had its skills changed underneath it")
	}
	if !strings.Contains(sp.Skip, "4242") {
		t.Errorf("skip reason = %q, want it to name the holding process", sp.Skip)
	}
	if got := body(t, filepath.Join(f.skillsDir(), "react")); strings.Contains(got, "version two") {
		t.Error("the held spawn was relinked anyway")
	}
	// The definition did move on, so a later run can bring the spawn forward.
	if got := f.plan("frontend", Options{}).Sources[0].Status; got != StatusCurrent {
		t.Errorf("the loadout definition was not upgraded: status = %q", got)
	}

	p = f.upgrade("frontend", Options{IncludeRunning: true})
	if sp := f.spawnPlan(p); sp.Skip != "" {
		t.Fatalf("--include-running still refused: %s", sp.Skip)
	}
	if got := body(t, filepath.Join(f.skillsDir(), "react")); !strings.Contains(got, "version two") {
		t.Errorf("--include-running left the old content in place: %q", got)
	}
}

// TestUpgradeRelinksALeaseWhoseOwnerHasGone: barracks skips only what it can
// prove is live.
func TestUpgradeRelinksALeaseWhoseOwnerHasGone(t *testing.T) {
	f := newFixture(t)
	repo := f.source("skills")
	l := f.train("frontend")
	f.equip(l, repo.Dir+"#main:skills")
	lz := f.spawn(l, lease.KindProcess)
	lz.Owner = &lease.Owner{PID: 4243, StartToken: "tok"}
	if err := f.eng.Leases.Save(lz); err != nil {
		t.Fatal(err)
	}

	repo.AddSkills(t, testutil.Skill{Path: "skills/react", Body: "version two\n"})
	repo.Commit(t, "move on")

	p := f.upgrade("frontend", Options{})
	if sp := f.spawnPlan(p); sp.Skip != "" {
		t.Fatalf("a dead owner held the spawn back: %s", sp.Skip)
	}
	if got := body(t, filepath.Join(f.skillsDir(), "react")); !strings.Contains(got, "version two") {
		t.Errorf("react was not relinked: %q", got)
	}
}

// TestUpgradeNeverRecreatesADeletedSpawnDirectory: relinking must not resurrect
// a directory the user removed.
func TestUpgradeNeverRecreatesADeletedSpawnDirectory(t *testing.T) {
	f := newFixture(t)
	repo := f.source("skills")
	l := f.train("frontend")
	f.equip(l, repo.Dir+"#main:skills")
	f.spawn(l, lease.KindManual)

	if err := os.RemoveAll(filepath.Join(f.work.Dir, ".claude")); err != nil {
		t.Fatal(err)
	}
	repo.AddSkills(t, testutil.Skill{Path: "skills/react", Body: "new\n"})
	repo.Commit(t, "move on")

	p := f.upgrade("frontend", Options{})
	if sp := f.spawnPlan(p); sp.Skip != "target directory no longer exists" {
		t.Errorf("skip reason = %q", sp.Skip)
	}
	if _, err := os.Lstat(filepath.Join(f.work.Dir, ".claude")); !os.IsNotExist(err) {
		t.Error("upgrade recreated a directory the user deleted")
	}
}

// TestUpgradeRecallsASpawnLeftWithNoSkills: an empty lease and an empty
// directory are not something to leave behind.
func TestUpgradeRecallsASpawnLeftWithNoSkills(t *testing.T) {
	f := newFixture(t)
	repo := f.source("skills")
	l := f.train("frontend")
	f.equip(l, repo.Dir+"#main:skills", "legacy")
	lz := f.spawn(l, lease.KindManual)

	repo.RemovePath(t, "skills/legacy")
	repo.Commit(t, "drop legacy")

	p := f.upgrade("frontend", Options{})
	sp := f.spawnPlan(p)
	if !sp.Recall {
		t.Fatal("an emptied spawn was left in place")
	}
	if _, err := f.eng.Leases.Get(lz.ID); err == nil {
		t.Error("the emptied lease survived")
	}
	if _, err := os.Lstat(filepath.Join(f.skillsDir(), "legacy")); !os.IsNotExist(err) {
		t.Error("the removed skill's link survived")
	}
	if got := f.work.Status(t); got != "" {
		t.Errorf("git status not clean after the recall:\n%s", got)
	}
}

// TestUpgradePin freezes a moving source, whether or not its ref actually moved.
func TestUpgradePin(t *testing.T) {
	t.Run("after a move", func(t *testing.T) {
		f := newFixture(t)
		repo := f.source("skills")
		l := f.train("frontend")
		f.equip(l, repo.Dir+"#main:skills")

		repo.AddSkills(t, testutil.Skill{Path: "skills/hooks"})
		head := repo.Commit(t, "add hooks")

		p := f.upgrade("frontend", Options{Pin: true})
		if p.Sources[0].NewIdent == p.Sources[0].Ident {
			t.Error("--pin did not report a new declared ref")
		}
		saved, err := f.eng.Loadouts.Get("frontend")
		if err != nil {
			t.Fatal(err)
		}
		if saved.Equipment[0].Ref != head {
			t.Errorf("declared ref = %q, want %q", saved.Equipment[0].Ref, head)
		}
		if !strings.Contains(saved.Equipment[0].Raw, head) {
			t.Errorf("the hand-editable source string was not repinned: %q", saved.Equipment[0].Raw)
		}
		// Frozen: the branch moving again must not carry it along.
		repo.AddSkills(t, testutil.Skill{Path: "skills/forms"})
		repo.Commit(t, "add forms")
		if got := f.plan("frontend", Options{}).Sources[0].Status; got != StatusPinned {
			t.Errorf("status after --pin = %q, want %q", got, StatusPinned)
		}
	})

	t.Run("without a move", func(t *testing.T) {
		f := newFixture(t)
		repo := f.source("skills")
		l := f.train("frontend")
		f.equip(l, repo.Dir+"#main:skills")

		p := f.upgrade("frontend", Options{Pin: true})
		if p.Sources[0].Status != StatusCurrent {
			t.Errorf("status = %q, want %q", p.Sources[0].Status, StatusCurrent)
		}
		saved, err := f.eng.Loadouts.Get("frontend")
		if err != nil {
			t.Fatal(err)
		}
		if saved.Equipment[0].Ref != repo.Head(t) {
			t.Errorf("--pin left a moving ref %q in place", saved.Equipment[0].Ref)
		}
	})
}

// TestUpgradeReportsSameContentForACommitThatChangedNoSkill: a moved commit is
// not by itself an update.
func TestUpgradeReportsSameContentForACommitThatChangedNoSkill(t *testing.T) {
	f := newFixture(t)
	repo := f.source("skills")
	l := f.train("frontend")
	f.equip(l, repo.Dir+"#main:skills", "react")
	f.spawn(l, lease.KindManual)

	testutil.WriteFile(t, filepath.Join(repo.Dir, "README.md"), "unrelated\n")
	head := repo.Commit(t, "unrelated change")

	p := f.upgrade("frontend", Options{})
	if got := p.Sources[0].Status; got != StatusSameContent {
		t.Fatalf("status = %q, want %q", got, StatusSameContent)
	}
	if p.Sources[0].Diff.Changed() {
		t.Errorf("diff claims a change it cannot substantiate: %+v", p.Sources[0].Diff)
	}
	// The spawn still follows the pin onto the new store entry.
	if got := dest(t, filepath.Join(f.skillsDir(), "react")); !strings.Contains(got, head) {
		t.Errorf("the spawn was not moved onto the newly pinned commit: %s", got)
	}
}

// TestUpgradeComparesByNameWhenTheOldCommitIsGone: with nothing left to compare
// against, the plan says how it was reached rather than inventing a diff.
func TestUpgradeComparesByNameWhenTheOldCommitIsGone(t *testing.T) {
	f := newFixture(t)
	repo := f.source("skills")
	l := f.train("frontend")
	f.equip(l, repo.Dir+"#main:skills")
	f.spawn(l, lease.KindManual)

	repo.AddSkills(t, testutil.Skill{Path: "skills/hooks"})
	repo.RemovePath(t, "skills/legacy")
	repo.Commit(t, "move on")

	if err := os.RemoveAll(f.eng.Store.Root); err != nil {
		t.Fatal(err)
	}

	p := f.upgrade("frontend", Options{})
	d := p.Sources[0].Diff
	if !d.ByName {
		t.Fatal("the diff did not admit it compared names only")
	}
	if strings.Join(d.Added, ",") != "hooks" || strings.Join(d.Removed, ",") != "legacy" {
		t.Errorf("diff = %+v, want hooks added and legacy removed", d)
	}
	if len(d.Modified) != 0 {
		t.Errorf("a name-only comparison cannot establish a modification: %v", d.Modified)
	}
	if len(p.Sources[0].Notes) == 0 {
		t.Error("the fallback was not reported as a note")
	}
}

// TestPlanReportsFailures keeps the exit-code path meaning what it says.
func TestPlanReportsFailures(t *testing.T) {
	t.Run("unresolvable source", func(t *testing.T) {
		f := newFixture(t)
		repo := f.source("skills")
		l := f.train("frontend")
		f.equip(l, repo.Dir+"#main:skills")
		if err := os.RemoveAll(repo.Dir); err != nil {
			t.Fatal(err)
		}

		p := f.plan("frontend", Options{})
		if p.Sources[0].Status != StatusFailed {
			t.Fatalf("status = %q, want %q", p.Sources[0].Status, StatusFailed)
		}
		if p.Sources[0].Err == nil {
			t.Error("a failed source carried no error")
		}
		if !p.Failed() {
			t.Error("a source that could not be resolved must fail the run")
		}
	})

	t.Run("source with no commit", func(t *testing.T) {
		f := newFixture(t)
		repo := f.source("skills")
		l := f.train("frontend")
		f.equip(l, repo.Dir+"#main:skills")
		l.Equipment[0].Commit = ""
		if err := f.eng.Loadouts.Save(l); err != nil {
			t.Fatal(err)
		}

		p := f.plan("frontend", Options{})
		if p.Sources[0].Status != StatusFailed {
			t.Fatalf("status = %q, want %q", p.Sources[0].Status, StatusFailed)
		}
		if !strings.Contains(p.Sources[0].Err.Error(), "re-equip") {
			t.Errorf("error = %v, want it to say what to do", p.Sources[0].Err)
		}
	})
}

// TestUnreadableLeaseRecordDoesNotFailTheRun: a malformed file beside the leases
// is reported by the reaper that runs first, and is not this command's failure.
// Folding it into the plan would exit non-zero on a run in which every source
// resolved, fetched and relinked perfectly.
func TestUnreadableLeaseRecordDoesNotFailTheRun(t *testing.T) {
	f := newFixture(t)
	repo := f.source("skills")
	l := f.train("frontend")
	f.equip(l, repo.Dir+"#main:skills")
	f.spawn(l, lease.KindManual)

	testutil.WriteFile(t, filepath.Join(f.eng.Leases.Dir, "broken.yaml"), "- this is not a lease record\n")
	if _, problems := f.eng.Leases.List(); len(problems) == 0 {
		t.Fatal("the fixture record parsed cleanly; the test proves nothing")
	}

	repo.AddSkills(t, testutil.Skill{Path: "skills/react", Body: "version two\n"})
	repo.Commit(t, "move on")

	p := f.upgrade("frontend", Options{})
	if len(p.Errs) != 0 {
		t.Errorf("plan errors = %v, want none - an unreadable record is a notice", p.Errs)
	}
	if p.Failed() {
		t.Error("an unreadable lease record failed a run in which nothing failed")
	}
	if got := body(t, filepath.Join(f.skillsDir(), "react")); !strings.Contains(got, "version two") {
		t.Errorf("the readable spawn was not upgraded: %q", got)
	}
}

// TestPlanTouchGoesThroughInspectLink: every removal and every repoint is
// classified against the live path first.
func TestPlanTouchGoesThroughInspectLink(t *testing.T) {
	root := t.TempDir()
	st := store.New(filepath.Join(root, "store"), filepath.Join(root, "mirrors"), gitcmd.Git{})
	target := filepath.Join(st.Root, "local", "owner", "repo@abc", "react")
	testutil.WriteFile(t, filepath.Join(target, skill.Manifest), "in the store\n")
	next := filepath.Join(st.Root, "local", "owner", "repo@def", "react")
	testutil.WriteFile(t, filepath.Join(next, skill.Manifest), "newer\n")

	spawnDir := filepath.Join(root, "spawn")
	if err := os.MkdirAll(spawnDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ours := filepath.Join(spawnDir, "ours")
	if err := os.Symlink(target, ours); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(spawnDir, "foreign")
	testutil.WriteFile(t, foreign, "not a barracks link\n")
	gone := filepath.Join(spawnDir, "gone")

	tests := []struct {
		name     string
		path     string
		kind     OpKind
		wantOp   OpKind
		wantNone bool
		wantKept bool
	}{
		{name: "ours relinks", path: ours, kind: OpRelink, wantOp: OpRelink},
		{name: "ours removes", path: ours, kind: OpRemove, wantOp: OpRemove},
		{name: "gone is nothing to remove", path: gone, kind: OpRemove, wantNone: true},
		{name: "gone is made afresh", path: gone, kind: OpRelink, wantOp: OpAdd},
		{name: "foreign is kept", path: foreign, kind: OpRemove, wantKept: true},
		{name: "foreign is never repointed", path: foreign, kind: OpRelink, wantKept: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			link := lease.Link{Path: tt.path, Target: target, Skill: "react"}
			op, kept := planTouch(link, st, tt.kind, next)
			switch {
			case tt.wantKept:
				if op != nil {
					t.Errorf("a foreign path was planned for %+v", op)
				}
				if kept == nil {
					t.Error("a foreign path was left alone silently")
				}
			case tt.wantNone:
				if op != nil || kept != nil {
					t.Errorf("op = %+v, kept = %+v, want neither", op, kept)
				}
			default:
				if kept != nil {
					t.Fatalf("unexpectedly kept: %+v", kept)
				}
				if op == nil || op.Kind != tt.wantOp {
					t.Fatalf("op = %+v, want kind %q", op, tt.wantOp)
				}
				if op.To != next {
					t.Errorf("op target = %q, want %q", op.To, next)
				}
			}
		})
	}
}

// TestReconcileLinksRecordsWhatIsReallyThere: the lease must describe the disk,
// not what the plan hoped for, or a later recall acts on a fiction.
func TestReconcileLinksRecordsWhatIsReallyThere(t *testing.T) {
	root := t.TempDir()
	store := filepath.Join(root, "store")
	spawnDir := filepath.Join(root, "spawn")
	for _, d := range []string{store, spawnDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	newTarget := filepath.Join(store, "new")
	oldTarget := filepath.Join(store, "old")
	for _, d := range []string{newTarget, oldTarget} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// landed really moved; stuck did not; missing was never created; orphan is
	// not in the plan at all but is still on disk.
	landed := filepath.Join(spawnDir, "landed")
	if err := os.Symlink(newTarget, landed); err != nil {
		t.Fatal(err)
	}
	stuck := filepath.Join(spawnDir, "stuck")
	if err := os.Symlink(oldTarget, stuck); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(spawnDir, "missing")
	orphan := filepath.Join(spawnDir, "orphan")
	if err := os.Symlink(oldTarget, orphan); err != nil {
		t.Fatal(err)
	}

	planned := []lease.Link{
		{Path: landed, Target: newTarget, Skill: "landed"},
		{Path: stuck, Target: newTarget, Skill: "stuck"},
		{Path: missing, Target: newTarget, Skill: "missing"},
	}
	original := []lease.Link{
		{Path: landed, Target: oldTarget, Skill: "landed"},
		{Path: stuck, Target: oldTarget, Skill: "stuck"},
		{Path: orphan, Target: oldTarget, Skill: "orphan"},
	}

	got := reconcileLinks(planned, original)
	want := []lease.Link{
		{Path: landed, Target: newTarget, Skill: "landed"},
		{Path: orphan, Target: oldTarget, Skill: "orphan"},
		{Path: stuck, Target: oldTarget, Skill: "stuck"},
	}
	if len(got) != len(want) {
		t.Fatalf("reconciled = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("link %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestMatchScoreRanking(t *testing.T) {
	tests := []struct {
		name string
		a, b matchScore
		want bool
	}{
		{name: "commit decides first", a: matchScore{commit: 1}, b: matchScore{skill: 1, subpath: 99}, want: true},
		{name: "skill decides next", a: matchScore{commit: 1, skill: 1}, b: matchScore{commit: 1, subpath: 99}, want: true},
		{name: "then the longer subpath", a: matchScore{subpath: 6}, b: matchScore{subpath: 3}, want: true},
		{name: "an equal score does not beat", a: matchScore{commit: 1}, b: matchScore{commit: 1}, want: false},
		{name: "a worse commit never wins", a: matchScore{skill: 1, subpath: 9}, b: matchScore{commit: 1}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.beats(tt.b); got != tt.want {
				t.Errorf("%+v beats %+v = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestUnderSubpath(t *testing.T) {
	tests := []struct {
		rel, subpath string
		want         bool
	}{
		{rel: "skills/react", subpath: "", want: true},
		{rel: "skills/react", subpath: "skills", want: true},
		{rel: "skills", subpath: "skills", want: true},
		{rel: "skills-extra/react", subpath: "skills", want: false},
		{rel: "other/react", subpath: "skills", want: false},
	}
	for _, tt := range tests {
		if got := underSubpath(tt.rel, tt.subpath); got != tt.want {
			t.Errorf("underSubpath(%q, %q) = %v, want %v", tt.rel, tt.subpath, got, tt.want)
		}
	}
}

func TestDiffNames(t *testing.T) {
	d := diffNames([]string{"a", "b"}, []string{"b", "c"})
	if strings.Join(d.Unchanged, ",") != "b" {
		t.Errorf("unchanged = %v", d.Unchanged)
	}
	if strings.Join(d.Removed, ",") != "a" {
		t.Errorf("removed = %v", d.Removed)
	}
	if strings.Join(d.Added, ",") != "c" {
		t.Errorf("added = %v", d.Added)
	}
	if !d.Changed() {
		t.Error("a diff with an add and a remove must report a change")
	}
	if (Diff{Unchanged: []string{"a"}}).Changed() {
		t.Error("a diff of nothing but unchanged skills is not a change")
	}
}

func TestShort(t *testing.T) {
	if got := Short("0123456789abcdef"); got != "01234567" {
		t.Errorf("Short = %q", got)
	}
	if got := Short("abc"); got != "abc" {
		t.Errorf("Short of an already-short commit = %q", got)
	}
}

func TestDescribeOwner(t *testing.T) {
	tests := []struct {
		name  string
		owner *lease.Owner
		want  string
	}{
		{name: "no owner", owner: nil, want: "a running process"},
		{name: "named", owner: &lease.Owner{PID: 7, Command: "claude"}, want: "running pid 7 (claude)"},
		{name: "unnamed", owner: &lease.Owner{PID: 7}, want: "running pid 7 (process)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := describeOwner(&lease.Lease{Owner: tt.owner}); got != tt.want {
				t.Errorf("describeOwner = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSpawnPlanReportable(t *testing.T) {
	empty := &SpawnPlan{Lease: &lease.Lease{}}
	if empty.Reportable() || empty.Changed() {
		t.Error("a spawn that is already where it should be prints nothing")
	}
	skipped := &SpawnPlan{Lease: &lease.Lease{}, Skip: "held"}
	if !skipped.Reportable() {
		t.Error("a skipped spawn must be reported")
	}
	if skipped.Changed() {
		t.Error("a skipped spawn has nothing to apply")
	}
	kept := &SpawnPlan{Lease: &lease.Lease{}, Kept: []lease.Kept{{Path: "p"}}}
	if !kept.Reportable() {
		t.Error("a kept path must be reported")
	}
}

// TestUpgradeSkipsALoadoutWithNoSources: with nothing equipped there is no move,
// and therefore nothing to say about any spawn.
func TestUpgradeSkipsALoadoutWithNoSources(t *testing.T) {
	f := newFixture(t)
	f.train("frontend")

	p := f.upgrade("frontend", Options{})
	if len(p.Sources) != 0 || len(p.Spawns) != 0 {
		t.Errorf("plan = %+v, want an empty one", p)
	}
	if p.Failed() {
		t.Error("an unequipped loadout is not a failure")
	}
}

// TestUpgradeIgnoresSpawnsOfOtherLoadouts keeps the blast radius to what was
// asked for.
func TestUpgradeIgnoresSpawnsOfOtherLoadouts(t *testing.T) {
	f := newFixture(t)
	repo := f.source("skills")

	front := f.train("frontend")
	f.equip(front, repo.Dir+"#main:skills", "react")
	f.spawn(front, lease.KindManual)

	back := f.train("backend")
	f.equip(back, repo.Dir+"#main:skills", "css")
	f.spawn(back, lease.KindManual)

	repo.AddSkills(t,
		testutil.Skill{Path: "skills/react", Body: "new react\n"},
		testutil.Skill{Path: "skills/css", Body: "new css\n"},
	)
	repo.Commit(t, "move both")

	css := filepath.Join(f.skillsDir(), "css")
	before := dest(t, css)

	f.upgrade("frontend", Options{})

	if got := body(t, filepath.Join(f.skillsDir(), "react")); !strings.Contains(got, "new react") {
		t.Error("the named loadout was not upgraded")
	}
	if got := dest(t, css); got != before {
		t.Errorf("upgrading frontend moved backend's spawn: %s -> %s", before, got)
	}
}

// Actionable is the question "would carrying this out do anything", and it has
// to be answered from what Apply acts on rather than from what resolved.
//
// The case that matters is the last one: an earlier upgrade left a spawn alone
// because a live session was holding it, so the spawn is behind the commit the
// loadout is already pinned at. A later run in which nothing resolves still has
// that spawn to reconcile, and a plan that called itself empty would strand it
// there for good.
func TestAPlanIsActionableWhenApplyingItWouldDoSomething(t *testing.T) {
	var nothing LoadoutPlan
	if nothing.Actionable() {
		t.Error("an empty plan claimed there was something to carry out")
	}

	definition := LoadoutPlan{definitionChanged: true}
	if !definition.Actionable() {
		t.Error("a plan with a definition to save was called empty")
	}

	held := LoadoutPlan{Spawns: []SpawnPlan{{
		Lease: &lease.Lease{},
		Skip:  "held by a live session (pid 4242)",
		Ops:   []Op{{Kind: OpRelink}},
	}}}
	if held.Actionable() {
		t.Error("a plan whose only spawn is being left alone claimed work")
	}

	stranded := LoadoutPlan{
		Sources: []SourcePlan{{Status: StatusFailed, Err: errors.New("could not resolve main")}},
		Spawns:  []SpawnPlan{{Lease: &lease.Lease{}, Ops: []Op{{Kind: OpRelink}}}},
	}
	if !stranded.Actionable() {
		t.Error("a spawn behind its pin was called empty because nothing resolved")
	}
}

// spawnNarrowed materialises only part of a loadout, the way
// `barracks spawn --only` does: links for the named skills alone, and the
// selection recorded on the lease.
func (f *fixture) spawnNarrowed(l *loadout.Loadout, keep ...string) *lease.Lease {
	f.t.Helper()
	want := map[string]bool{}
	for _, n := range keep {
		want[n] = true
	}
	lz := f.spawn(l, lease.KindManual)

	var links []lease.Link
	for _, link := range lz.Links {
		if want[link.Skill] {
			links = append(links, link)
			continue
		}
		if err := os.Remove(link.Path); err != nil {
			f.t.Fatalf("unlink %s: %v", link.Path, err)
		}
	}
	if len(links) != len(keep) {
		f.t.Fatalf("the loadout does not carry all of %v", keep)
	}
	lz.Links = links
	lz.Selection = append([]string(nil), keep...)
	sort.Strings(lz.Selection)
	if err := f.eng.Leases.Save(lz); err != nil {
		f.t.Fatalf("save lease: %v", err)
	}
	return lz
}

// TestAnUpgradeKeepsANarrowedDeploymentNarrowed is the whole of the promise
// `spawn --only` makes about the future, and both halves of it fail differently.
//
// Widening is the loud one: an upgrade that installed the skills the user
// deliberately left behind would undo the choice at the moment they asked for
// something else entirely, and they would find out from the agent. Stranding is
// the quiet one, and it is the neighbourhood of the rule that a skip may never
// become permanent: a chosen skill that vanishes upstream and comes back has to
// come back here too, or the deployment decays a skill at a time and no command
// ever puts it right.
func TestAnUpgradeKeepsANarrowedDeploymentNarrowed(t *testing.T) {
	f := newFixture(t)
	src := f.source("kit",
		testutil.Skill{Path: "skills/react", Body: "v1"},
		testutil.Skill{Path: "skills/css", Body: "v1"},
		testutil.Skill{Path: "skills/legacy", Body: "v1"})

	l := f.train("frontend")
	f.equip(l, src.Dir+"#main:skills")
	lz := f.spawnNarrowed(l, "css", "react")

	// Upstream moves under it: react changes, css disappears, a new skill
	// arrives. Only react and css were ever chosen.
	src.AddSkills(t, testutil.Skill{Path: "skills/react", Body: "v2"}, testutil.Skill{Path: "skills/newcomer", Body: "v1"})
	src.RemovePath(t, "skills/css")
	src.Commit(t, "move on")

	f.upgrade("frontend", Options{})
	saved := f.leaseOf(lz.ID)

	if got := linkedSkills(saved); !reflect.DeepEqual(got, []string{"react"}) {
		t.Fatalf("after the upgrade the deployment carries %v, want react alone", got)
	}
	for _, gone := range []string{"legacy", "newcomer"} {
		if testutil.Exists(filepath.Join(f.skillsDir(), gone)) {
			t.Errorf("the upgrade widened a narrowed deployment with %s", gone)
		}
	}
	if got := body(t, filepath.Join(f.skillsDir(), "react")); got != "v2" {
		t.Errorf("the chosen skill was not moved forward: react reads %q", got)
	}
	// The selection is untouched by a skill going missing. It is what the
	// deployment chose, not what happens to be linked right now - and it is the
	// only thing that can bring css back.
	if !reflect.DeepEqual(saved.Selection, []string{"css", "react"}) {
		t.Fatalf("the recorded selection became %v", saved.Selection)
	}

	// css comes back upstream. A later upgrade re-links it, because it was
	// chosen - and still refuses the two that never were.
	src.AddSkills(t, testutil.Skill{Path: "skills/css", Body: "v3"})
	src.Commit(t, "css returns")

	f.upgrade("frontend", Options{})
	saved = f.leaseOf(lz.ID)
	if got := linkedSkills(saved); !reflect.DeepEqual(got, []string{"css", "react"}) {
		t.Fatalf("a chosen skill that came back was stranded: the deployment carries %v", got)
	}
	if got := body(t, filepath.Join(f.skillsDir(), "css")); got != "v3" {
		t.Errorf("the returned skill did not come back at the new commit: css reads %q", got)
	}
	for _, gone := range []string{"legacy", "newcomer"} {
		if testutil.Exists(filepath.Join(f.skillsDir(), gone)) {
			t.Errorf("the second upgrade widened the deployment with %s", gone)
		}
	}
}

// The same upgrade, over a spawn that was never narrowed, still installs what
// appeared upstream. The gate has to be the selection and not the mere fact
// that the links are fewer than the loadout's skills, or every ordinary spawn
// would freeze at the set it was made with.
func TestAnUpgradeStillWidensASpawnThatWasNeverNarrowed(t *testing.T) {
	f := newFixture(t)
	src := f.source("kit", testutil.Skill{Path: "skills/react"})

	l := f.train("frontend")
	f.equip(l, src.Dir+"#main:skills")
	lz := f.spawn(l, lease.KindManual)
	if lz.Narrowed() {
		t.Fatal("an ordinary spawn recorded a selection")
	}

	src.AddSkills(t, testutil.Skill{Path: "skills/newcomer"})
	src.Commit(t, "a new skill upstream")

	f.upgrade("frontend", Options{})
	if got := linkedSkills(f.leaseOf(lz.ID)); !reflect.DeepEqual(got, []string{"newcomer", "react"}) {
		t.Errorf("an unnarrowed spawn did not take the new skill: %v", got)
	}
}

func linkedSkills(l *lease.Lease) []string {
	out := make([]string, 0, len(l.Links))
	for _, link := range l.Links {
		out = append(out, link.Skill)
	}
	sort.Strings(out)
	return out
}
