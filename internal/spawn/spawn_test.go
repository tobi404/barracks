package spawn

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tobi404/barracks/internal/gitcmd"
	"github.com/tobi404/barracks/internal/lease"
	"github.com/tobi404/barracks/internal/loadout"
	"github.com/tobi404/barracks/internal/source"
	"github.com/tobi404/barracks/internal/store"
	"github.com/tobi404/barracks/internal/target"
	"github.com/tobi404/barracks/internal/testutil"
)

func ctx() context.Context { return context.Background() }

var fixedNow = time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)

// scene wires an engine over temp directories, a fixture source repo, and a
// consumer repo to spawn into.
type scene struct {
	root   string
	src    *testutil.GitRepo
	work   *testutil.GitRepo
	engine *Engine
	leases *lease.Store
	store  *store.Store
	tgt    target.Target
}

func newScene(t *testing.T, skills ...testutil.Skill) *scene {
	t.Helper()
	if len(skills) == 0 {
		skills = []testutil.Skill{{Path: "skills/react"}, {Path: "skills/css"}}
	}
	root := t.TempDir()
	s := &scene{
		root: root,
		src:  testutil.NewSkillRepo(t, filepath.Join(root, "src"), skills...),
		work: testutil.NewGitRepo(t, filepath.Join(root, "work")),
	}
	testutil.WriteFile(t, filepath.Join(s.work.Dir, "README.md"), "hello\n")
	s.work.Commit(t, "initial")

	s.store = store.New(filepath.Join(root, "store"), filepath.Join(root, "mirrors"), gitcmd.Git{})
	s.leases = lease.NewStore(filepath.Join(root, "leases"))
	s.engine = &Engine{
		Store:  s.store,
		Leases: s.leases,
		Git:    gitcmd.Git{},
		Now:    func() time.Time { return fixedNow },
		Env:    func(string) string { return "" },
		Home:   func() (string, error) { return filepath.Join(root, "home"), nil },
	}
	var err error
	s.tgt, err = target.Lookup("claude")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// loadout builds a loadout equipped with the fixture repo.
func (s *scene) loadout(t *testing.T, name, subpath string, only, except []string) *loadout.Loadout {
	t.Helper()
	raw := s.src.Dir
	if subpath != "" {
		raw += "#main:" + subpath
	}
	src, err := source.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := s.store.Resolve(ctx(), src)
	if err != nil {
		t.Fatal(err)
	}
	return &loadout.Loadout{
		Name:      name,
		CreatedAt: fixedNow,
		Equipment: []loadout.Equipment{{Source: src, Commit: commit, Only: only, Except: except}},
	}
}

func (s *scene) request(l *loadout.Loadout) Request {
	return Request{Loadout: l, Target: s.tgt, Cwd: s.work.Dir, Kind: lease.KindManual}
}

func (s *scene) skillsDir() string { return filepath.Join(s.work.Dir, ".claude", "skills") }

// TestProcessLeaseNeedsAnIdentifiableOwner refuses the one lease the reaper
// cannot judge later: without a start token it would be left comparing a bare
// PID, which a recycled PID makes meaningless.
func TestProcessLeaseNeedsAnIdentifiableOwner(t *testing.T) {
	tests := []struct {
		name  string
		owner *lease.Owner
	}{
		{"no owner at all", nil},
		{"owner without a start token", &lease.Owner{PID: 4242, Command: "claude"}},
		{"owner without a pid", &lease.Owner{StartToken: "tok", Command: "claude"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newScene(t)
			req := s.request(s.loadout(t, "frontend", "", nil, nil))
			req.Kind = lease.KindProcess
			req.Owner = tt.owner

			_, err := s.engine.Spawn(ctx(), req)
			if err == nil {
				t.Fatal("Spawn accepted a process lease with no verifiable owner")
			}
			if !strings.Contains(err.Error(), "identity token") {
				t.Errorf("err = %v, want it to name the missing identity token", err)
			}
			if testutil.Exists(s.skillsDir()) {
				t.Error("the refused spawn left something behind")
			}
		})
	}
}

func TestSpawnCreatesSymlinksIntoTheStore(t *testing.T) {
	s := newScene(t)
	res, err := s.engine.Spawn(ctx(), s.request(s.loadout(t, "frontend", "", nil, nil)))
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if len(res.Skills) != 2 {
		t.Fatalf("spawned %d skills, want 2", len(res.Skills))
	}
	for _, sk := range res.Skills {
		if !testutil.IsSymlink(t, sk.Path) {
			t.Errorf("%s is not a symlink; spawning must not copy files", sk.Path)
		}
		dest, err := os.Readlink(sk.Path)
		if err != nil {
			t.Fatal(err)
		}
		if !s.store.Contains(dest) {
			t.Errorf("%s points outside the store, at %s", sk.Path, dest)
		}
		if _, err := os.Stat(filepath.Join(sk.Path, "SKILL.md")); err != nil {
			t.Errorf("spawned skill %s does not resolve to real content: %v", sk.Name, err)
		}
	}
	if res.Lease.Kind != lease.KindManual || res.Lease.ExpiresAt != nil {
		t.Errorf("lease = %+v, want a manual lease with no expiry", res.Lease)
	}
	if res.Lease.Scope != lease.ScopeRepo {
		t.Errorf("scope = %q, want repo", res.Lease.Scope)
	}
}

// TestSpawnLeavesGitStatusClean is acceptance criterion 2's first half.
func TestSpawnLeavesGitStatusClean(t *testing.T) {
	s := newScene(t)
	excludeBefore := s.work.ReadExclude(t)

	res, err := s.engine.Spawn(ctx(), s.request(s.loadout(t, "frontend", "", nil, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if status := s.work.Status(t); status != "" {
		t.Errorf("git status is dirty after spawn:\n%s", status)
	}

	exclude := s.work.ReadExclude(t)
	if exclude == excludeBefore {
		t.Error("spawn did not register anything in .git/info/exclude")
	}
	for _, want := range []string{"/.claude/skills/react", "/.claude/skills/css"} {
		if !strings.Contains(exclude, want) {
			t.Errorf(".git/info/exclude missing %q:\n%s", want, exclude)
		}
	}
	// Never the committed .gitignore.
	if testutil.Exists(filepath.Join(s.work.Dir, ".gitignore")) {
		t.Error("spawn created a .gitignore; it must only ever touch .git/info/exclude")
	}

	// And the round trip restores it exactly.
	lease.Revoke(res.Lease, s.store, s.leases, "test")
	if got := s.work.ReadExclude(t); got != excludeBefore {
		t.Errorf(".git/info/exclude not restored\n got: %q\nwant: %q", got, excludeBefore)
	}
	if status := s.work.Status(t); status != "" {
		t.Errorf("git status is dirty after recall:\n%s", status)
	}
	if testutil.Exists(filepath.Join(s.work.Dir, ".claude")) {
		t.Error(".claude directory survived the recall")
	}
}

func TestSpawnWithADeadline(t *testing.T) {
	s := newScene(t)
	req := s.request(s.loadout(t, "frontend", "", nil, nil))
	req.Kind = lease.KindDeadline
	req.Duration = 2 * time.Hour

	res, err := s.engine.Spawn(ctx(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Lease.ExpiresAt == nil {
		t.Fatal("deadline lease has no expiry")
	}
	if want := fixedNow.Add(2 * time.Hour); !res.Lease.ExpiresAt.Equal(want) {
		t.Errorf("expiry = %v, want %v", res.Lease.ExpiresAt, want)
	}
}

func TestSpawnRejectsADeadlineWithoutADuration(t *testing.T) {
	s := newScene(t)
	req := s.request(s.loadout(t, "frontend", "", nil, nil))
	req.Kind = lease.KindDeadline

	if _, err := s.engine.Spawn(ctx(), req); err == nil {
		t.Fatal("a deadline lease with no duration should be rejected")
	}
	if testutil.Exists(s.skillsDir()) {
		t.Error("a rejected spawn created directories anyway")
	}
}

func TestSpawnRefusesToOverwriteAForeignPath(t *testing.T) {
	s := newScene(t)
	mine := filepath.Join(s.skillsDir(), "react")
	testutil.WriteFile(t, filepath.Join(mine, "SKILL.md"), "my own react skill")

	_, err := s.engine.Spawn(ctx(), s.request(s.loadout(t, "frontend", "", nil, nil)))
	if !errors.Is(err, ErrOccupied) {
		t.Fatalf("Spawn = %v, want ErrOccupied", err)
	}
	body, readErr := os.ReadFile(filepath.Join(mine, "SKILL.md"))
	if readErr != nil || string(body) != "my own react skill" {
		t.Fatal("spawn damaged a user-created skill it should have refused to touch")
	}
	// The refusal must also not leave the other skill behind.
	if testutil.Exists(filepath.Join(s.skillsDir(), "css")) {
		t.Error("a refused spawn left a partial deployment behind")
	}
}

func TestSpawnRefusesADuplicate(t *testing.T) {
	s := newScene(t)
	l := s.loadout(t, "frontend", "", nil, nil)
	if _, err := s.engine.Spawn(ctx(), s.request(l)); err != nil {
		t.Fatal(err)
	}
	_, err := s.engine.Spawn(ctx(), s.request(l))
	if !errors.Is(err, ErrAlreadySpawned) {
		t.Fatalf("second Spawn = %v, want ErrAlreadySpawned", err)
	}
}

func TestSpawnRejectsAnEmptyLoadout(t *testing.T) {
	s := newScene(t)
	empty := &loadout.Loadout{Name: "empty", CreatedAt: fixedNow}
	_, err := s.engine.Spawn(ctx(), s.request(empty))
	if err == nil || !strings.Contains(err.Error(), "equip it") {
		t.Fatalf("Spawn of an empty loadout = %v, want a helpful error", err)
	}
}

func TestSpawnRejectsUnpinnedEquipment(t *testing.T) {
	s := newScene(t)
	l := s.loadout(t, "frontend", "", nil, nil)
	l.Equipment[0].Commit = ""

	_, err := s.engine.Spawn(ctx(), s.request(l))
	if err == nil || !strings.Contains(err.Error(), "not pinned") {
		t.Fatalf("Spawn = %v, want a complaint about the missing commit pin", err)
	}
}

func TestSpawnRejectsCollidingSkillNames(t *testing.T) {
	s := newScene(t, testutil.Skill{Path: "a/react"}, testutil.Skill{Path: "b/react"})
	l := s.loadout(t, "frontend", "", nil, nil)

	_, err := s.engine.Spawn(ctx(), s.request(l))
	if err == nil || !strings.Contains(err.Error(), "react") {
		t.Fatalf("Spawn = %v, want a collision error naming the skill", err)
	}
	if testutil.Exists(s.skillsDir()) {
		t.Error("a rejected spawn left directories behind")
	}
}

func TestSpawnAppliesFilters(t *testing.T) {
	s := newScene(t,
		testutil.Skill{Path: "skills/react-hooks"},
		testutil.Skill{Path: "skills/react-forms"},
		testutil.Skill{Path: "skills/css-grid"},
	)
	tests := []struct {
		name   string
		only   []string
		except []string
		want   []string
	}{
		{"no filters", nil, nil, []string{"css-grid", "react-forms", "react-hooks"}},
		{"only", []string{"react-*"}, nil, []string{"react-forms", "react-hooks"}},
		{"except", nil, []string{"css-*"}, []string{"react-forms", "react-hooks"}},
		{"both", []string{"react-*"}, []string{"react-forms"}, []string{"react-hooks"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc := newScene(t,
				testutil.Skill{Path: "skills/react-hooks"},
				testutil.Skill{Path: "skills/react-forms"},
				testutil.Skill{Path: "skills/css-grid"},
			)
			res, err := sc.engine.Spawn(ctx(), sc.request(sc.loadout(t, "f", "skills", tt.only, tt.except)))
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, sk := range res.Skills {
				got = append(got, sk.Name)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("spawned %v, want %v", got, tt.want)
			}
		})
	}
	_ = s
}

func TestSpawnGlobalUsesTheTargetMap(t *testing.T) {
	s := newScene(t)
	req := s.request(s.loadout(t, "frontend", "", nil, nil))
	req.Global = true

	res, err := s.engine.Spawn(ctx(), req)
	if err != nil {
		t.Fatalf("Spawn --global: %v", err)
	}
	wantDir := filepath.Join(s.root, "home", ".claude", "skills")
	if res.Lease.Dir != wantDir {
		t.Errorf("global spawn dir = %q, want %q", res.Lease.Dir, wantDir)
	}
	if res.Lease.Scope != lease.ScopeGlobal {
		t.Errorf("scope = %q, want global", res.Lease.Scope)
	}
	// A global spawn is outside any repository, so nothing is excluded.
	if res.Lease.Exclude != nil {
		t.Error("a global spawn should not touch a git exclude file")
	}
}

func TestSpawnHonoursEveryRegisteredTarget(t *testing.T) {
	for _, tgt := range target.Registry {
		t.Run(tgt.ID, func(t *testing.T) {
			s := newScene(t)
			s.tgt = tgt
			res, err := s.engine.Spawn(ctx(), s.request(s.loadout(t, "frontend", "", nil, nil)))
			if err != nil {
				t.Fatalf("Spawn for target %q: %v", tgt.ID, err)
			}
			// git reports the real path, so compare against the resolved root
			// (on macOS /tmp is itself a symlink to /private/tmp).
			realRoot, err := filepath.EvalSymlinks(s.work.Dir)
			if err != nil {
				t.Fatal(err)
			}
			want := filepath.Join(realRoot, tgt.RepoDir)
			if res.Lease.Dir != want {
				t.Errorf("spawn dir = %q, want %q from the target map", res.Lease.Dir, want)
			}
			if res.Lease.Target != tgt.ID {
				t.Errorf("lease target = %q, want %q", res.Lease.Target, tgt.ID)
			}
		})
	}
}

func TestSpawnOutsideAGitRepository(t *testing.T) {
	s := newScene(t)
	plain := filepath.Join(t.TempDir(), "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	req := s.request(s.loadout(t, "frontend", "", nil, nil))
	req.Cwd = plain

	res, err := s.engine.Spawn(ctx(), req)
	if err != nil {
		t.Fatalf("Spawn outside a repository: %v", err)
	}
	if len(res.Notices) == 0 {
		t.Error("spawning outside a repository should say that no exclude was registered")
	}
	if res.Lease.Exclude != nil {
		t.Error("there is no .git here, so nothing should have been excluded")
	}
	if !testutil.IsSymlink(t, filepath.Join(plain, ".claude", "skills", "react")) {
		t.Error("the skills were not spawned")
	}
}

func TestSpawnRecordsOnlyTheDirectoriesItCreated(t *testing.T) {
	s := newScene(t)
	// .claude already exists; only skills/ is new.
	existing := filepath.Join(s.work.Dir, ".claude")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := s.engine.Spawn(ctx(), s.request(s.loadout(t, "frontend", "", nil, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range res.Lease.CreatedDirs {
		if d == existing {
			t.Fatalf("spawn recorded %q as created, but it was already there", d)
		}
	}
	lease.Revoke(res.Lease, s.store, s.leases, "test")
	if !testutil.Exists(existing) {
		t.Error("recall removed a directory the user already had")
	}
}

func TestMaterialiseReportsFetchCount(t *testing.T) {
	s := newScene(t)
	l := s.loadout(t, "frontend", "", nil, nil)

	plan, err := s.engine.Materialise(ctx(), l, s.skillsDir())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Fetched != 1 {
		t.Errorf("first Materialise fetched %d sources, want 1", plan.Fetched)
	}
	plan, err = s.engine.Materialise(ctx(), l, s.skillsDir())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Fetched != 0 {
		t.Errorf("second Materialise fetched %d sources, want 0 - the store is shared", plan.Fetched)
	}
}

func TestResolveOutsideARepositoryFallsBackToTheWorkingDirectory(t *testing.T) {
	s := newScene(t)
	plain := t.TempDir()
	loc, err := s.engine.Resolve(ctx(), Request{Target: s.tgt, Cwd: plain})
	if err != nil {
		t.Fatal(err)
	}
	if loc.GitDir != "" {
		t.Errorf("GitDir = %q, want empty outside a repository", loc.GitDir)
	}
	if loc.Dir != filepath.Join(plain, s.tgt.RepoDir) {
		t.Errorf("Dir = %q, want it under the working directory", loc.Dir)
	}
}

func TestEngineDefaultsToWallClock(t *testing.T) {
	e := &Engine{}
	if e.now().IsZero() {
		t.Fatal("an engine with no clock injected should fall back to time.Now")
	}
}

func TestMkdirTracked(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b", "c")

	created, err := mkdirTracked(deep)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(root, "a"), filepath.Join(root, "a", "b"), deep}
	if len(created) != len(want) {
		t.Fatalf("created = %v, want %v", created, want)
	}
	for i := range want {
		if created[i] != want[i] {
			t.Fatalf("created = %v, want %v (shallowest first)", created, want)
		}
	}

	// A second call creates nothing, so it records nothing.
	created, err = mkdirTracked(deep)
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 0 {
		t.Errorf("created = %v, want nothing on an existing path", created)
	}
}

// TestUnwinderUndoesAPartialSpawn covers the rollback path directly: a spawn
// that fails half way must leave nothing behind, and must never remove
// anything it did not itself create.
func TestUnwinderUndoesAPartialSpawn(t *testing.T) {
	root := t.TempDir()
	skills := filepath.Join(root, ".claude", "skills")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatal(err)
	}
	storeDir := filepath.Join(root, "store", "react")
	testutil.WriteFile(t, filepath.Join(storeDir, "SKILL.md"), "# react")

	mine := filepath.Join(skills, "mine")
	testutil.WriteFile(t, filepath.Join(mine, "SKILL.md"), "user content")

	ours := filepath.Join(skills, "react")
	if err := os.Symlink(storeDir, ours); err != nil {
		t.Fatal(err)
	}

	u := &unwinder{
		// "mine" is deliberately listed to prove the unwinder still refuses to
		// remove anything that is not a symlink it made.
		links: []string{ours, mine},
		dirs:  []string{filepath.Join(root, ".claude"), skills},
	}
	u.run()

	if testutil.Exists(ours) {
		t.Error("the unwinder left its own symlink behind")
	}
	if !testutil.Exists(filepath.Join(mine, "SKILL.md")) {
		t.Fatal("the unwinder destroyed a real directory")
	}
	// Directory pruning stops at the non-empty directory.
	if !testutil.Exists(skills) {
		t.Error("a directory holding user content was pruned")
	}
}

func TestUnwinderPrunesTheDirectoriesItCreated(t *testing.T) {
	root := t.TempDir()
	skills := filepath.Join(root, ".claude", "skills")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatal(err)
	}
	u := &unwinder{dirs: []string{filepath.Join(root, ".claude"), skills}}
	u.run()

	if testutil.Exists(filepath.Join(root, ".claude")) {
		t.Error("the unwinder left empty directories behind")
	}
}

func TestIsAncestorOrSelf(t *testing.T) {
	tests := []struct {
		name      string
		candidate string
		dir       string
		want      bool
	}{
		{"same path", "/a/b", "/a/b", true},
		{"direct parent", "/a", "/a/b", true},
		{"distant ancestor", "/", "/a/b/c", true},
		{"child is not an ancestor", "/a/b/c", "/a/b", false},
		{"unrelated", "/x/y", "/a/b", false},
		{"prefix but not ancestor", "/a/bb", "/a/b", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAncestorOrSelf(tt.candidate, tt.dir); got != tt.want {
				t.Errorf("isAncestorOrSelf(%q, %q) = %v, want %v", tt.candidate, tt.dir, got, tt.want)
			}
		})
	}
}

func TestWithInheritedDirs(t *testing.T) {
	dir := filepath.Join("/repo", ".claude", "skills")
	others := []*lease.Lease{
		{CreatedDirs: []string{filepath.Join("/repo", ".claude"), dir}},
		{CreatedDirs: []string{filepath.Join("/elsewhere", ".claude")}},
		{CreatedDirs: []string{filepath.Join(dir, "deeper")}},
	}

	got := withInheritedDirs(nil, others, dir)
	want := []string{filepath.Join("/repo", ".claude"), dir}
	if len(got) != len(want) {
		t.Fatalf("withInheritedDirs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("withInheritedDirs = %v, want %v (shallowest first)", got, want)
		}
	}

	// Already-recorded directories are not duplicated.
	got = withInheritedDirs([]string{dir}, others, dir)
	seen := map[string]int{}
	for _, d := range got {
		seen[d]++
	}
	for d, n := range seen {
		if n > 1 {
			t.Errorf("directory %q recorded %d times", d, n)
		}
	}
}

// TestTwoLoadoutsInOneDirectoryCleanUpFully is the regression this inheritance
// exists for: whichever lease goes last must be able to prune the directory.
func TestTwoLoadoutsInOneDirectoryCleanUpFully(t *testing.T) {
	s := newScene(t, testutil.Skill{Path: "skills/react"}, testutil.Skill{Path: "skills/css"})

	first := s.loadout(t, "frontend", "skills", []string{"react"}, nil)
	second := s.loadout(t, "backend", "skills", []string{"css"}, nil)

	resA, err := s.engine.Spawn(ctx(), s.request(first))
	if err != nil {
		t.Fatal(err)
	}
	resB, err := s.engine.Spawn(ctx(), s.request(second))
	if err != nil {
		t.Fatal(err)
	}

	lease.Revoke(resA.Lease, s.store, s.leases, "test")
	if !testutil.Exists(s.skillsDir()) {
		t.Fatal("the directory was pruned while the second loadout still had links in it")
	}
	lease.Revoke(resB.Lease, s.store, s.leases, "test")
	if testutil.Exists(filepath.Join(s.work.Dir, ".claude")) {
		t.Error("the last recall left empty directories behind")
	}
}
