package garrison

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
	"github.com/tobi404/barracks/internal/proc"
	"github.com/tobi404/barracks/internal/source"
	"github.com/tobi404/barracks/internal/store"
	"github.com/tobi404/barracks/internal/target"
	"github.com/tobi404/barracks/internal/testutil"
)

// fixture is a source repository, a work repository, and a store, all on disk.
// Nothing here touches the network.
type fixture struct {
	t        *testing.T
	src      *testutil.GitRepo
	work     *testutil.GitRepo
	root     string
	store    *store.Store
	leases   *lease.Store
	loadouts *loadout.Store
	engine   *Engine
}

func newFixture(t *testing.T, skills ...testutil.Skill) *fixture {
	t.Helper()
	if len(skills) == 0 {
		skills = []testutil.Skill{{Path: "skills/react"}, {Path: "skills/css"}}
	}
	base := t.TempDir()
	src := testutil.NewSkillRepo(t, filepath.Join(base, "src"), skills...)
	work := testutil.NewGitRepo(t, filepath.Join(base, "work"))
	testutil.WriteFile(t, filepath.Join(work.Dir, "README.md"), "hello\n")
	work.Commit(t, "initial")

	// git reports a resolved root; on macOS /tmp is a symlink to /private/tmp.
	root, err := filepath.EvalSymlinks(work.Dir)
	if err != nil {
		t.Fatalf("resolve work root: %v", err)
	}

	f := &fixture{
		t:        t,
		src:      src,
		work:     work,
		root:     root,
		store:    store.New(filepath.Join(base, "store"), filepath.Join(base, "mirrors"), gitcmd.Git{}),
		leases:   lease.NewStore(filepath.Join(base, "leases")),
		loadouts: loadout.NewStore(filepath.Join(base, "loadouts")),
	}
	f.engine = &Engine{
		Store:    f.store,
		Leases:   f.leases,
		Git:      gitcmd.Git{},
		Now:      func() time.Time { return time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC) },
		Loadouts: f.loadouts,
	}
	return f
}

func (f *fixture) gitDir() string { return filepath.Join(f.root, ".git") }

// loadout builds a loadout pinned at the source repository's current HEAD.
func (f *fixture) loadout(name string, only ...string) *loadout.Loadout {
	f.t.Helper()
	src, err := source.Parse(f.src.Dir + "#main:skills")
	if err != nil {
		f.t.Fatalf("parse source: %v", err)
	}
	commit, err := f.store.Resolve(context.Background(), src)
	if err != nil {
		f.t.Fatalf("resolve source: %v", err)
	}
	l := &loadout.Loadout{
		Name:      name,
		Equipment: []loadout.Equipment{{Source: src, Commit: commit, Only: only}},
	}
	if err := f.loadouts.Save(l); err != nil {
		f.t.Fatalf("save loadout: %v", err)
	}
	return l
}

func (f *fixture) request(l *loadout.Loadout, force bool, targetIDs ...string) Request {
	f.t.Helper()
	if len(targetIDs) == 0 {
		targetIDs = []string{"claude"}
	}
	targets, err := target.LookupAll(targetIDs)
	if err != nil {
		f.t.Fatalf("lookup targets: %v", err)
	}
	return Request{Root: f.root, GitDir: f.gitDir(), Name: l.Name, Equipment: l.Equipment, Targets: targets, Force: force}
}

func (f *fixture) install(l *loadout.Loadout, targetIDs ...string) *Result {
	f.t.Helper()
	res, err := f.engine.Install(context.Background(), f.request(l, false, targetIDs...))
	if err != nil {
		f.t.Fatalf("install %s: %v", l.Name, err)
	}
	return res
}

func (f *fixture) path(rel string) string { return filepath.Join(f.root, filepath.FromSlash(rel)) }

func (f *fixture) lock() string { return testutil.ReadFile(f.t, Path(f.root)) }

// mustInspect fails unless the working tree matches the lockfile exactly.
func (f *fixture) mustInspect() {
	f.t.Helper()
	insp, err := f.engine.Inspect(f.root)
	if err != nil {
		f.t.Fatalf("inspect: %v", err)
	}
	if !insp.OK() {
		for _, c := range insp.Checks {
			for _, fd := range c.Findings {
				f.t.Errorf("unexpected finding: %s", fd)
			}
		}
	}
}

// TestInstallVendorsRealFiles is the core promise of the committed tier: file
// content that survives a clone, and a lockfile that describes it.
func TestInstallVendorsRealFiles(t *testing.T) {
	f := newFixture(t)
	testutil.WriteFile(t, filepath.Join(f.src.Dir, "skills", "react", "reference.md"), "reference\n")
	f.src.Commit(t, "add a second file")

	res := f.install(f.loadout("frontend"))
	if !res.New {
		t.Error("first install did not report itself as new")
	}

	for _, rel := range []string{".claude/skills/react/SKILL.md", ".claude/skills/react/reference.md", ".claude/skills/css/SKILL.md"} {
		if !testutil.Exists(f.path(rel)) {
			t.Errorf("%s was not vendored", rel)
			continue
		}
		if testutil.IsSymlink(t, f.path(rel)) {
			t.Errorf("%s is a symlink; a committed skill must be real file content", rel)
		}
		if testutil.ReadFile(t, f.path(rel)) == "" {
			t.Errorf("%s was vendored empty", rel)
		}
	}
	if !testutil.IsDir(f.path(".claude/skills/react")) {
		t.Error(".claude/skills/react must be a real directory, not a link into the store")
	}
	if !testutil.Exists(Path(f.root)) {
		t.Fatal("no lockfile was written")
	}
	if lock := f.lock(); !strings.Contains(lock, "digest: sha256:") || !strings.Contains(lock, ".claude/skills/react") {
		t.Errorf("lockfile does not record digests and paths:\n%s", lock)
	}

	// Acceptance criterion 2: a committed spawn is never excluded from git.
	if got := f.work.ReadExclude(t); strings.Contains(got, "claude") {
		t.Errorf(".git/info/exclude mentions the garrison; committed files must be visible to git:\n%s", got)
	}
	f.mustInspect()
}

// TestInstallIsIdempotent proves a repeat run is a no-op down to the bytes of
// the lockfile, so `barracks garrison` never dirties a repository for nothing.
func TestInstallIsIdempotent(t *testing.T) {
	f := newFixture(t)
	l := f.loadout("frontend")
	f.install(l)
	before := f.lock()

	res, err := f.engine.Install(context.Background(), f.request(l, false))
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if res.Changed() {
		t.Errorf("second install reported changes: wrote %v deleted %v", res.Wrote, res.Deleted)
	}
	if len(res.Unchanged) != 2 {
		t.Errorf("unchanged = %v, want both files", res.Unchanged)
	}
	if after := f.lock(); after != before {
		t.Errorf("lockfile changed on a no-op install:\n%s", after)
	}
}

// TestUpdateRewritesFilesAndLockfileTogether is acceptance criterion 5: after an
// upstream move the tree and the lockfile agree, and the change is a plain diff.
func TestUpdateRewritesFilesAndLockfileTogether(t *testing.T) {
	f := newFixture(t)
	l := f.loadout("frontend")
	f.install(l)
	f.work.Commit(t, "garrison frontend")

	// react changes, css disappears, tailwind arrives.
	testutil.WriteFile(t, filepath.Join(f.src.Dir, "skills", "react", "SKILL.md"), "---\nname: react\n---\n\nv2\n")
	if err := os.RemoveAll(filepath.Join(f.src.Dir, "skills", "css")); err != nil {
		t.Fatal(err)
	}
	f.src.AddSkills(t, testutil.Skill{Path: "skills/tailwind"})
	f.src.Commit(t, "v2")

	res := f.install(f.loadout("frontend"))
	if res.New {
		t.Error("update reported itself as a new garrison")
	}
	if got := testutil.ReadFile(t, f.path(".claude/skills/react/SKILL.md")); !strings.Contains(got, "v2") {
		t.Errorf("react not updated: %q", got)
	}
	if !testutil.Exists(f.path(".claude/skills/tailwind/SKILL.md")) {
		t.Error("new skill not vendored")
	}
	if testutil.Exists(f.path(".claude/skills/css")) {
		t.Error("a dropped skill's directory was left behind")
	}
	f.mustInspect()

	// The whole change is visible to review, and nothing is hidden from git.
	status := f.work.Status(t)
	for _, want := range []string{"barracks.lock", ".claude/skills/react/SKILL.md", ".claude/skills/css/SKILL.md"} {
		if !strings.Contains(status, want) {
			t.Errorf("git status does not mention %s:\n%s", want, status)
		}
	}
}

// TestUpdateRefusesALocallyEditedFile is the case the brief singles out: a
// teammate has edited a vendored file. Neither overwriting it silently nor
// leaving the lockfile lying about it is acceptable, so the update is refused.
func TestUpdateRefusesALocallyEditedFile(t *testing.T) {
	f := newFixture(t)
	l := f.loadout("frontend")
	f.install(l)

	edited := f.path(".claude/skills/react/SKILL.md")
	testutil.WriteFile(t, edited, "my own words\n")
	testutil.WriteFile(t, filepath.Join(f.src.Dir, "skills", "react", "SKILL.md"), "---\nname: react\n---\n\nv2\n")
	f.src.Commit(t, "v2")

	_, err := f.engine.Install(context.Background(), f.request(f.loadout("frontend"), false))
	if !errors.Is(err, ErrLocallyModified) {
		t.Fatalf("update error = %v, want ErrLocallyModified", err)
	}
	if !strings.Contains(err.Error(), ".claude/skills/react/SKILL.md") {
		t.Errorf("error does not name the file: %v", err)
	}
	if got := testutil.ReadFile(t, edited); got != "my own words\n" {
		t.Errorf("the edit was not preserved by the refusal: %q", got)
	}
	// The refusal is total: css must not have been touched either.
	if !testutil.Exists(f.path(".claude/skills/css/SKILL.md")) {
		t.Error("a refused update still changed the tree")
	}

	// --force is the user saying to take the new content anyway.
	res, err := f.engine.Install(context.Background(), f.request(f.loadout("frontend"), true))
	if err != nil {
		t.Fatalf("forced update: %v", err)
	}
	if got := testutil.ReadFile(t, edited); !strings.Contains(got, "v2") {
		t.Errorf("--force did not replace the edited file: %q", got)
	}
	if len(res.Wrote) == 0 {
		t.Error("forced update reported no writes")
	}
	f.mustInspect()
}

// TestInstallRefusesAFileItNeverWrote proves --force is narrow: it discards the
// user's edits to barracks' own files, never a file barracks has no record of.
func TestInstallRefusesAFileItNeverWrote(t *testing.T) {
	f := newFixture(t)
	l := f.loadout("frontend", "react")
	f.install(l)

	// A hand-written file inside a managed skill directory, then a source that
	// wants that same path.
	stray := f.path(".claude/skills/react/reference.md")
	testutil.WriteFile(t, stray, "mine, not barracks'\n")
	testutil.WriteFile(t, filepath.Join(f.src.Dir, "skills", "react", "reference.md"), "upstream\n")
	f.src.Commit(t, "add reference")

	for _, force := range []bool{false, true} {
		_, err := f.engine.Install(context.Background(), f.request(f.loadout("frontend", "react"), force))
		if !errors.Is(err, ErrOccupied) {
			t.Fatalf("force=%v: error = %v, want ErrOccupied", force, err)
		}
		if got := testutil.ReadFile(t, stray); got != "mine, not barracks'\n" {
			t.Fatalf("force=%v: the file was overwritten: %q", force, got)
		}
	}
}

// TestInstallRefusesOnTopOfADirectoryItDoesNotKnow keeps a hand-made skill
// directory safe: barracks never adopts one by writing into it.
func TestInstallRefusesOnTopOfADirectoryItDoesNotKnow(t *testing.T) {
	f := newFixture(t)
	mine := f.path(".claude/skills/react/SKILL.md")
	testutil.WriteFile(t, mine, "my own skill\n")

	_, err := f.engine.Install(context.Background(), f.request(f.loadout("frontend"), true))
	if !errors.Is(err, ErrOccupied) {
		t.Fatalf("error = %v, want ErrOccupied", err)
	}
	if got := testutil.ReadFile(t, mine); got != "my own skill\n" {
		t.Errorf("a hand-made skill was overwritten: %q", got)
	}
}

// TestRemoveTakesOnlyWhatTheLockfileRecords is acceptance criterion 3.
func TestRemoveTakesOnlyWhatTheLockfileRecords(t *testing.T) {
	f := newFixture(t)
	testutil.WriteFile(t, filepath.Join(f.src.Dir, "skills", "react", "reference.md"), "reference\n")
	f.src.Commit(t, "add reference")
	f.install(f.loadout("frontend"))

	edited := f.path(".claude/skills/react/reference.md")
	testutil.WriteFile(t, edited, "a teammate edited this\n")
	foreign := f.path(".claude/skills/react/handwritten.md")
	testutil.WriteFile(t, foreign, "never barracks'\n")

	rep, err := f.engine.Remove(f.root, Ref{Loadout: "frontend"})
	if err != nil {
		t.Fatalf("remove: %v", err)
	}

	if testutil.Exists(f.path(".claude/skills/react/SKILL.md")) {
		t.Error("a file matching the lockfile was not removed")
	}
	if testutil.Exists(f.path(".claude/skills/css")) {
		t.Error("an untouched skill directory was not pruned")
	}
	if got := testutil.ReadFile(t, edited); got != "a teammate edited this\n" {
		t.Errorf("an edited file was destroyed: %q", got)
	}
	if got := testutil.ReadFile(t, foreign); got != "never barracks'\n" {
		t.Errorf("a foreign file was destroyed: %q", got)
	}
	if !rep.Foreign() {
		t.Fatal("removal kept files but reported nothing; silence here is a bug")
	}
	reported := map[string]string{}
	for _, k := range rep.Kept {
		reported[k.Path] = k.Reason
	}
	if reason, ok := reported[".claude/skills/react/reference.md"]; !ok || !strings.Contains(reason, "edited") {
		t.Errorf("the edited file was not reported as such: %v", rep.Kept)
	}
	if _, ok := reported[".claude/skills/react/handwritten.md"]; !ok {
		t.Errorf("the foreign file was not reported: %v", rep.Kept)
	}
	if testutil.Exists(Path(f.root)) {
		t.Error("the lockfile survived the removal of its only garrison")
	}
}

// TestRemoveTakesTheDirectoriesInsideASkillWithIt: a lockfile records files, so
// a directory barracks itself made *inside* a skill is only ever implied by one.
// Reporting it as somebody else's work would be a lie, and leaving it standing
// would leave an empty tree behind where a garrison used to be.
func TestRemoveTakesTheDirectoriesInsideASkillWithIt(t *testing.T) {
	f := newFixture(t)
	testutil.WriteFile(t, filepath.Join(f.src.Dir, "skills", "css", "ref", "deep", "notes.md"), "reference\n")
	f.src.Commit(t, "add a nested reference")
	f.install(f.loadout("frontend"))
	if !testutil.Exists(f.path(".claude/skills/css/ref/deep/notes.md")) {
		t.Fatal("the nested file was never vendored")
	}

	rep, err := f.engine.Remove(f.root, Ref{Loadout: "frontend"})
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if rep.Foreign() {
		t.Errorf("barracks' own directories were reported as somebody else's: %v", rep.Kept)
	}
	if testutil.Exists(f.path(".claude")) {
		t.Error("an empty chain of barracks' own directories was left standing")
	}
}

// TestRemoveLeavesOtherGarrisonsAlone proves the lockfile is per-loadout, so one
// removal is not a reset of the whole file.
func TestRemoveLeavesOtherGarrisonsAlone(t *testing.T) {
	f := newFixture(t)
	f.install(f.loadout("frontend", "react"))
	f.install(f.loadout("styles", "css"))

	rep, err := f.engine.Remove(f.root, Ref{Loadout: "frontend"})
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	// The other garrison's directory holds the shared parent open, which is
	// correct and unremarkable. Reporting it as a file barracks has no record of
	// would be a false alarm about the one thing that report exists to mean.
	if rep.Foreign() {
		t.Errorf("another garrison's files were reported as somebody's own work: %v", rep.Kept)
	}
	if testutil.Exists(f.path(".claude/skills/react")) {
		t.Error("the removed garrison's files survived")
	}
	if !testutil.Exists(f.path(".claude/skills/css/SKILL.md")) {
		t.Error("the other garrison's files were removed")
	}
	m, err := Load(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if m.FindFor("", "frontend") != nil || m.FindFor("", "styles") == nil {
		t.Errorf("lockfile entries wrong after one removal: %+v", m.Garrisons)
	}
	if _, err := f.engine.Remove(f.root, Ref{Loadout: "frontend"}); !errors.Is(err, ErrNotGarrisoned) {
		t.Errorf("removing twice = %v, want ErrNotGarrisoned", err)
	}
}

// TestRemoveOfTheSecondGarrisonPrunesTheSharedDirectory is the inherited-chain
// rule: only the first install creates .claude/skills, so whichever garrison is
// removed last has to be able to prune it.
func TestRemoveOfTheSecondGarrisonPrunesTheSharedDirectory(t *testing.T) {
	f := newFixture(t)
	f.install(f.loadout("frontend", "react"))
	f.install(f.loadout("styles", "css"))

	for _, name := range []string{"frontend", "styles"} {
		if _, err := f.engine.Remove(f.root, Ref{Loadout: name}); err != nil {
			t.Fatalf("remove %s: %v", name, err)
		}
	}
	if testutil.Exists(f.path(".claude")) {
		t.Error(".claude survived after both garrisons were removed")
	}
}

// TestReaperCannotRemoveAGarrison is acceptance criterion 4, tested explicitly
// and adversarially: leases of every kind are fabricated pointing straight at
// the garrisoned paths, all of them already dead, and a full reap is run.
//
// A garrison takes no lease out, so this state cannot arise from barracks
// itself. It is constructed anyway, because "the reaper can never remove a
// shared spawn, however leases are configured" has to hold by construction and
// not by the absence of a record.
func TestReaperCannotRemoveAGarrison(t *testing.T) {
	f := newFixture(t)
	f.install(f.loadout("frontend"))
	f.work.Commit(t, "garrison frontend")

	dir := f.path(".claude/skills")
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	links := []lease.Link{
		{Path: filepath.Join(dir, "react"), Target: f.storePathOf("react"), Skill: "react"},
		{Path: filepath.Join(dir, "css"), Target: f.storePathOf("css"), Skill: "css"},
	}
	for _, l := range []*lease.Lease{
		{ID: "expired", Loadout: "frontend", Target: "claude", Scope: lease.ScopeRepo, Root: f.root, Dir: dir,
			Kind: lease.KindDeadline, ExpiresAt: &past, Links: links, CreatedDirs: []string{dir, f.path(".claude")}},
		{ID: "deadproc", Loadout: "frontend", Target: "claude", Scope: lease.ScopeRepo, Root: f.root, Dir: dir,
			Kind: lease.KindProcess, Owner: &lease.Owner{PID: 999999, StartToken: "gone"}, Links: links},
		{ID: "noowner", Loadout: "frontend", Target: "claude", Scope: lease.ScopeRepo, Root: f.root, Dir: dir,
			Kind: lease.KindProcess, Links: links},
	} {
		l.CreatedAt = past
		if err := f.leases.Save(l); err != nil {
			t.Fatal(err)
		}
	}

	reaper := &lease.Reaper{
		Leases: f.leases,
		Guard:  f.store,
		Now:    func() time.Time { return time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC) },
		Prober: deadProber{},
	}
	reports, problems := reaper.Reap()
	if len(problems) > 0 {
		t.Fatalf("reap problems: %v", problems)
	}
	if len(reports) != 3 {
		t.Fatalf("reaped %d leases, want all 3 recognised as dead", len(reports))
	}
	for _, rep := range reports {
		if len(rep.Removed) > 0 {
			t.Errorf("lease %s removed %v from a garrison", rep.Lease.ID, rep.Removed)
		}
		if !rep.Foreign() {
			t.Errorf("lease %s kept nothing and reported nothing", rep.Lease.ID)
		}
	}

	f.mustInspect()
	if status := f.work.Status(t); status != "" {
		t.Errorf("a reap changed the committed tree:\n%s", status)
	}
	if !testutil.IsDir(f.path(".claude/skills/react")) {
		t.Error("the reaper removed a garrisoned skill directory")
	}
}

// storePathOf is the store directory a personal spawn of that skill would point
// at, so a fabricated lease is as convincing as a real one.
func (f *fixture) storePathOf(name string) string {
	f.t.Helper()
	src, err := source.Parse(f.src.Dir + "#main:skills")
	if err != nil {
		f.t.Fatal(err)
	}
	commit, err := f.store.Resolve(context.Background(), src)
	if err != nil {
		f.t.Fatal(err)
	}
	return filepath.Join(f.store.Path(src, commit), "skills", name)
}

// deadProber reports every process as gone, so every process lease is dead.
type deadProber struct{}

func (deadProber) Identity(int) (string, error) { return "", proc.ErrNotRunning }

// TestInstallRefusesWhileAPersonalSpawnHoldsThePaths stops a repository ending
// up with one path registered both ways.
func TestInstallRefusesWhileAPersonalSpawnHoldsThePaths(t *testing.T) {
	f := newFixture(t)
	dir := f.path(".claude/skills")
	if err := f.leases.Save(&lease.Lease{
		ID: "personal", Loadout: "other", Target: "claude", Scope: lease.ScopeRepo,
		Root: f.root, Dir: dir, Kind: lease.KindManual, CreatedAt: time.Now(),
		Links: []lease.Link{{Path: filepath.Join(dir, "react"), Target: f.storePathOf("react"), Skill: "react"}},
	}); err != nil {
		t.Fatal(err)
	}

	_, err := f.engine.Install(context.Background(), f.request(f.loadout("frontend"), true))
	if !errors.Is(err, ErrPersonalSpawn) {
		t.Fatalf("error = %v, want ErrPersonalSpawn", err)
	}
	if testutil.Exists(Path(f.root)) {
		t.Error("a refused install still wrote a lockfile")
	}
}

// TestGuardClaimsWhatTheLockfileRecords is the other half of that refusal: the
// hook internal/spawn uses to keep a personal spawn off committed paths.
func TestGuardClaimsWhatTheLockfileRecords(t *testing.T) {
	f := newFixture(t)
	f.install(f.loadout("frontend"))
	guard := Guard{}

	if by, ok := guard.Claims(f.root, f.path(".claude/skills/react")); !ok || by != "frontend" {
		t.Errorf("Claims(vendored skill) = %q, %v; want frontend, true", by, ok)
	}
	if _, ok := guard.Claims(f.root, f.path(".claude/skills/react/SKILL.md")); !ok {
		t.Error("Claims did not recognise a file inside a vendored skill")
	}
	if _, ok := guard.Claims(f.root, f.path(".cursor/skills/react")); ok {
		t.Error("Claims matched a path in another agent's directory")
	}
	if _, ok := guard.Claims("", f.path(".claude/skills/react")); ok {
		t.Error("Claims answered for an empty root")
	}

	// A lockfile barracks cannot read must not be able to block every spawn in
	// the repository; `barracks inspect` is what surfaces that instead.
	testutil.WriteFile(t, Path(f.root), "\tnot: [valid\n  yaml\n")
	if _, ok := guard.Claims(f.root, f.path(".claude/skills/react")); ok {
		t.Error("an unreadable lockfile claimed a path")
	}
}

// TestInspectFindsEveryKindOfDrift is acceptance criterion 6.
func TestInspectFindsEveryKindOfDrift(t *testing.T) {
	f := newFixture(t)
	testutil.WriteFile(t, filepath.Join(f.src.Dir, "skills", "react", "reference.md"), "reference\n")
	f.src.Commit(t, "add reference")
	f.install(f.loadout("frontend"))
	f.mustInspect()

	testutil.WriteFile(t, f.path(".claude/skills/react/SKILL.md"), "edited\n")
	if err := os.Remove(f.path(".claude/skills/css/SKILL.md")); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, f.path(".claude/skills/react/notes.md"), "added by hand\n")
	if err := os.Remove(f.path(".claude/skills/react/reference.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(f.path(".claude/skills/react/reference.md"), 0o755); err != nil {
		t.Fatal(err)
	}

	insp, err := f.engine.Inspect(f.root)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if insp.OK() {
		t.Fatal("inspect accepted a drifted checkout")
	}
	got := map[string]Problem{}
	for _, c := range insp.Checks {
		for _, fd := range c.Findings {
			got[fd.Path] = fd.Problem
		}
	}
	want := map[string]Problem{
		".claude/skills/react/SKILL.md":     ProblemModified,
		".claude/skills/css/SKILL.md":       ProblemMissing,
		".claude/skills/react/notes.md":     ProblemUnrecorded,
		".claude/skills/react/reference.md": ProblemWrongKind,
	}
	for path, problem := range want {
		if got[path] != problem {
			t.Errorf("finding for %s = %q, want %q", path, got[path], problem)
		}
	}
	if insp.Findings() != len(want) {
		t.Errorf("found %d problems, want %d: %v", insp.Findings(), len(want), got)
	}
}

// TestInspectNotesADriftedPinButDoesNotFailOnIt keeps the two questions apart: a
// checkout that matches its lockfile is correct even when the loadout has moved
// on, because matching the lockfile is exactly what a teammate gets.
func TestInspectNotesADriftedPinButDoesNotFailOnIt(t *testing.T) {
	f := newFixture(t)
	f.install(f.loadout("frontend"))

	testutil.WriteFile(t, filepath.Join(f.src.Dir, "skills", "react", "SKILL.md"), "---\nname: react\n---\n\nv2\n")
	f.src.Commit(t, "v2")
	f.loadout("frontend") // re-pins the definition, leaving the lockfile behind

	insp, err := f.engine.Inspect(f.root)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if !insp.OK() {
		t.Errorf("a pin ahead of the lockfile was reported as a mismatch: %v", insp.Checks[0].Findings)
	}
	notes := strings.Join(insp.Checks[0].Notes, "\n")
	if !strings.Contains(notes, "loadout is pinned at") || !strings.Contains(notes, "barracks garrison frontend") {
		t.Errorf("no note about the drifted pin:\n%s", notes)
	}
}

// TestRestoreRebuildsFromTheLockfileAlone is the teammate case: a machine that
// has barracks but has never trained this loadout can still repair a checkout.
func TestRestoreRebuildsFromTheLockfileAlone(t *testing.T) {
	f := newFixture(t)
	f.install(f.loadout("frontend"))
	if err := f.loadouts.Delete("frontend"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(f.path(".claude")); err != nil {
		t.Fatal(err)
	}

	results, err := f.engine.Restore(context.Background(), f.root, f.gitDir(), nil, false)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(results) != 1 || len(results[0].Wrote) != 2 {
		t.Fatalf("restore wrote %+v", results)
	}
	f.mustInspect()

	if _, err := f.engine.Restore(context.Background(), f.root, f.gitDir(), []string{"nope"}, false); err == nil {
		t.Error("restoring a loadout the lockfile does not record succeeded")
	}
}

// TestReinstallIsTheUpgradeHook covers the entry point `barracks upgrade` uses:
// one call brings the vendored files and the lockfile onto the loadout's new
// pins together, keeping the targets the lockfile recorded.
func TestReinstallIsTheUpgradeHook(t *testing.T) {
	f := newFixture(t)
	f.install(f.loadout("frontend"), "claude", "cursor")

	testutil.WriteFile(t, filepath.Join(f.src.Dir, "skills", "react", "SKILL.md"), "---\nname: react\n---\n\nv2\n")
	f.src.Commit(t, "v2")
	upgraded := f.loadout("frontend")

	res, err := f.engine.Reinstall(context.Background(), f.root, f.gitDir(), upgraded, false)
	if err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	if len(res.Targets) != 2 {
		t.Errorf("targets = %v, want the two the lockfile recorded", res.Targets)
	}
	for _, rel := range []string{".claude/skills/react/SKILL.md", ".cursor/skills/react/SKILL.md"} {
		if got := testutil.ReadFile(t, f.path(rel)); !strings.Contains(got, "v2") {
			t.Errorf("%s = %q, want the new content", rel, got)
		}
	}
	f.mustInspect()

	if _, err := f.engine.Reinstall(context.Background(), f.root, f.gitDir(), &loadout.Loadout{Name: "absent"}, false); !errors.Is(err, ErrNotGarrisoned) {
		t.Errorf("reinstalling a loadout that is not garrisoned = %v, want ErrNotGarrisoned", err)
	}
}

// TestSymlinkInASkillIsReportedNotVendored: a link is the one thing that cannot
// survive the trip through a clone onto a machine with no store.
func TestSymlinkInASkillIsReportedNotVendored(t *testing.T) {
	f := newFixture(t, testutil.Skill{Path: "skills/react"})
	if err := os.Symlink("SKILL.md", filepath.Join(f.src.Dir, "skills", "react", "alias.md")); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
	f.src.Commit(t, "add a symlink")

	res := f.install(f.loadout("frontend"))
	if testutil.Exists(f.path(".claude/skills/react/alias.md")) {
		t.Error("a symlink was vendored into the committed tree")
	}
	if !strings.Contains(strings.Join(res.Notices, "\n"), "alias.md") {
		t.Errorf("the skipped symlink was not reported: %v", res.Notices)
	}
	f.mustInspect()
}

// TestInstallStopsBeforeWritingWhenTheLockfileIsUnreadable: an install decides
// everything from the lockfile, so one it cannot read is a refusal, not a fresh
// start that would overwrite whatever the unreadable file was recording.
func TestInstallStopsBeforeWritingWhenTheLockfileIsUnreadable(t *testing.T) {
	f := newFixture(t)
	if err := os.Mkdir(Path(f.root), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := f.engine.Install(context.Background(), f.request(f.loadout("frontend"), false))
	if err == nil {
		t.Fatal("install succeeded with an unreadable lockfile")
	}
	if testutil.Exists(f.path(".claude/skills/react/SKILL.md")) {
		t.Error("vendored files were written before the lockfile could be read")
	}
	if testutil.Exists(f.path(".claude/skills")) {
		t.Error("directories were created before the lockfile could be read")
	}
}

// TestApplyUndoesItselfExactly covers the rollback that keeps the tree and the
// lockfile from ever disagreeing.
//
// The plan is built by hand because the state it defends against - a write that
// fails part-way - cannot be provoked through Install without a fault injected
// into the filesystem. What matters is that the machinery restores overwritten
// content byte for byte, not just that it deletes what it made.
func TestApplyUndoesItselfExactly(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "a", "keep.md")
	testutil.WriteFile(t, existing, "original\n")
	replacement := filepath.Join(root, "source.md")
	testutil.WriteFile(t, replacement, "replacement\n")

	created := filepath.Join(root, "b", "new.md")
	plan := &writePlan{root: root, dirs: []string{"b"}, ops: []writeOp{
		{rel: "a/keep.md", abs: existing, src: replacement, mode: modeFile},
		{rel: "b/new.md", abs: created, src: replacement, mode: modeFile},
	}}

	if err := plan.apply(root); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := testutil.ReadFile(t, existing); got != "replacement\n" {
		t.Fatalf("apply did not overwrite: %q", got)
	}
	if !testutil.Exists(created) {
		t.Fatal("apply did not create the new file")
	}

	plan.undo()
	if got := testutil.ReadFile(t, existing); got != "original\n" {
		t.Errorf("undo did not restore the overwritten file: %q", got)
	}
	if testutil.Exists(created) {
		t.Error("undo left behind a file it had created")
	}
	if testutil.Exists(filepath.Join(root, "b")) {
		t.Error("undo left behind a directory it had created")
	}
	for _, name := range testutil.Entries(t, root) {
		if strings.HasPrefix(name, ".barracks-undo-") {
			t.Errorf("undo left its scratch directory behind: %s", name)
		}
	}
}

// TestApplyRollsBackOnAFailedWrite: a copy that cannot be made must leave the
// tree exactly as it was, not half updated.
func TestApplyRollsBackOnAFailedWrite(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "keep.md")
	testutil.WriteFile(t, existing, "original\n")
	good := filepath.Join(root, "source.md")
	testutil.WriteFile(t, good, "replacement\n")

	plan := &writePlan{root: root, ops: []writeOp{
		{rel: "keep.md", abs: existing, src: good, mode: modeFile},
		{rel: "gone.md", abs: filepath.Join(root, "gone.md"), src: filepath.Join(root, "not-there.md"), mode: modeFile},
	}}
	if err := plan.apply(root); err == nil {
		t.Fatal("apply succeeded with a missing source file")
	}
	if got := testutil.ReadFile(t, existing); got != "original\n" {
		t.Errorf("a failed apply left the tree half updated: %q", got)
	}
	if testutil.Exists(filepath.Join(root, "gone.md")) {
		t.Error("a failed apply left a partial file behind")
	}
}

// TestInspectionCountsAndFindingsRender covers what the command layer prints.
func TestInspectionCountsAndFindingsRender(t *testing.T) {
	f := newFixture(t)
	f.install(f.loadout("frontend"))
	insp, err := f.engine.Inspect(f.root)
	if err != nil {
		t.Fatal(err)
	}
	g := insp.Checks[0].Garrison
	if g.SkillCount() != 2 || g.FileCount() != 2 {
		t.Errorf("counts = %d skills, %d files; want 2 and 2", g.SkillCount(), g.FileCount())
	}

	plain := Finding{Path: "a/b", Problem: ProblemMissing}
	detailed := Finding{Path: "a/b", Problem: ProblemWrongKind, Detail: "a symlink"}
	if plain.String() != "a/b: missing" {
		t.Errorf("Finding.String() = %q", plain.String())
	}
	if detailed.String() != "a/b: wrong kind of file (a symlink)" {
		t.Errorf("Finding.String() = %q", detailed.String())
	}
}

// TestInstallNeedsARepositoryAndSources checks the refusals that stop a garrison
// being created somewhere it could never be shared from.
func TestInstallNeedsARepositoryAndSources(t *testing.T) {
	f := newFixture(t)
	claude, err := target.LookupAll([]string{"claude"})
	if err != nil {
		t.Fatal(err)
	}
	l := f.loadout("frontend")

	cases := map[string]Request{
		"no repository": {Name: "frontend", Equipment: l.Equipment, Targets: claude},
		"no target":     {Root: f.root, Name: "frontend", Equipment: l.Equipment},
		"no source":     {Root: f.root, Name: "frontend", Targets: claude},
		"unpinned source": {Root: f.root, Name: "frontend", Targets: claude,
			Equipment: []loadout.Equipment{{Source: l.Equipment[0].Source}}},
	}
	for name, req := range cases {
		if _, err := f.engine.Install(context.Background(), req); err == nil {
			t.Errorf("%s: install succeeded", name)
		}
	}
}

// TestLockfileFromANewerFormatIsRefused: a lockfile this build cannot fully
// understand must never be rewritten from a partial reading of it.
func TestLockfileFromANewerFormatIsRefused(t *testing.T) {
	f := newFixture(t)
	testutil.WriteFile(t, Path(f.root), "version: 99\ngarrisons: []\n")

	if _, err := Load(f.root); err == nil || !strings.Contains(err.Error(), "newer barracks") {
		t.Errorf("Load of a future lockfile = %v, want a refusal naming the version", err)
	}
	if _, err := f.engine.Inspect(f.root); err == nil {
		t.Error("Inspect accepted a future lockfile")
	}
}

// TestLoadOfAMissingLockfileIsNotAnError: not being garrisoned is a normal state.
func TestLoadOfAMissingLockfileIsNotAnError(t *testing.T) {
	f := newFixture(t)
	m, err := Load(f.root)
	if err != nil {
		t.Fatalf("Load with no lockfile: %v", err)
	}
	if len(m.Garrisons) != 0 {
		t.Errorf("empty manifest has %d garrisons", len(m.Garrisons))
	}
	names, err := Garrisoned(f.root)
	if err != nil || len(names) != 0 {
		t.Errorf("Garrisoned = %v, %v; want empty", names, err)
	}
	insp, err := f.engine.Inspect(f.root)
	if err != nil || !insp.OK() || len(insp.Checks) != 0 {
		t.Errorf("Inspect with no lockfile = %+v, %v", insp, err)
	}
}

// TestInstallWarnsWhenGitWouldIgnoreTheFiles is the one silent failure this mode
// has: the install looks perfect, the commit carries nothing, and the teammate
// who clones gets no skills at all.
func TestInstallWarnsWhenGitWouldIgnoreTheFiles(t *testing.T) {
	f := newFixture(t)
	testutil.WriteFile(t, filepath.Join(f.root, ".gitignore"), ".claude/\n")
	f.work.Commit(t, "ignore .claude")

	res := f.install(f.loadout("frontend", "react"))
	notices := strings.Join(res.Notices, "\n")
	if !strings.Contains(notices, "git ignores") || !strings.Contains(notices, ".claude/skills/react") {
		t.Errorf("no warning that the committed files are ignored:\n%s", notices)
	}
	if !strings.Contains(notices, "barracks spawn") {
		t.Errorf("the warning does not offer the alternative:\n%s", notices)
	}
}

// TestInstallOutsideAGitRepositorySaysSo: files on disk that nothing will carry
// to a team are not a garrison, and barracks says so rather than implying one.
func TestInstallOutsideAGitRepositorySaysSo(t *testing.T) {
	f := newFixture(t)
	req := f.request(f.loadout("frontend", "react"), false)
	req.GitDir = ""

	res, err := f.engine.Install(context.Background(), req)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !strings.Contains(strings.Join(res.Notices, "\n"), "nothing will carry them to your team") {
		t.Errorf("no notice about being outside a repository: %v", res.Notices)
	}
}

// TestFindForMatchesByIdentityThenName is the whole rule the committed tier's
// rename safety rests on, and the one that cannot be fixed after release: a
// lockfile already in somebody's repository can never be reached to migrate it.
func TestFindForMatchesByIdentityThenName(t *testing.T) {
	m := &Manifest{Garrisons: []Garrison{
		{Loadout: "frontend", ID: "aaa"},
		{Loadout: "legacy"}, // written before identities existed
		{Loadout: "styles", ID: "ccc"},
	}}

	tests := []struct {
		name    string
		id, ask string
		want    string // the entry's Loadout, or "" for no match
	}{
		{"identity wins over the name", "aaa", "renamed-since", "frontend"},
		{"identity found under a stale name", "ccc", "anything", "styles"},
		{"a pre-identity entry falls back to its name", "bbb", "legacy", "legacy"},
		{"a caller with no identity still matches by name", "", "frontend", "frontend"},
		{"a name nothing carries", "zzz", "nowhere", ""},
		// Two machines can train different loadouts under one name. A recorded
		// identity that disagrees proves that, and is the only case where a name
		// match is declined - anything looser would install one loadout's skills
		// over another's records.
		{"a different loadout sharing the name", "zzz", "frontend", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := m.FindFor(tt.id, tt.ask)
			switch {
			case tt.want == "" && g != nil:
				t.Errorf("matched %q, want no match", g.Loadout)
			case tt.want != "" && g == nil:
				t.Errorf("no match, want %q", tt.want)
			case tt.want != "" && g.Loadout != tt.want:
				t.Errorf("matched %q, want %q", g.Loadout, tt.want)
			}
		})
	}
}

// TestUpsertAndDropFollowTheSameMatching: a renamed loadout must replace its own
// entry rather than appending a second one, and drop the same one.
func TestUpsertAndDropFollowTheSameMatching(t *testing.T) {
	m := &Manifest{Garrisons: []Garrison{
		{Loadout: "frontend", ID: "aaa", Targets: []string{"claude"}},
		{Loadout: "legacy", Targets: []string{"claude"}},
	}}

	m.Upsert(Garrison{Loadout: "web", ID: "aaa", Targets: []string{"cursor"}})
	if len(m.Garrisons) != 2 {
		t.Fatalf("upsert under a new name appended: %+v", m.Garrisons)
	}
	if g := m.FindFor("aaa", "web"); g == nil || g.Targets[0] != "cursor" {
		t.Errorf("upsert did not replace the entry: %+v", m.Garrisons)
	}

	// A pre-identity entry is upserted by name and gains the identity.
	m.Upsert(Garrison{Loadout: "legacy", ID: "bbb"})
	if len(m.Garrisons) != 2 {
		t.Fatalf("upsert over a pre-identity entry appended: %+v", m.Garrisons)
	}

	if !m.Drop("aaa", "web") || len(m.Garrisons) != 1 {
		t.Fatalf("drop by identity left %+v", m.Garrisons)
	}
	if m.Drop("nothing", "gone") {
		t.Error("drop reported removing an entry that was not there")
	}
}

// TestRenameStampsThePreIdentityEntry: renaming is the moment a lockfile written
// before identities existed stops being findable by name, so it is also the
// moment it has to be given one.
func TestRenameStampsThePreIdentityEntry(t *testing.T) {
	m := &Manifest{Garrisons: []Garrison{{Loadout: "frontend"}}}

	if !m.Rename("aaa", "frontend", "web") {
		t.Fatal("rename did not find the entry by name")
	}
	g := m.Garrisons[0]
	if g.Loadout != "web" || g.ID != "aaa" {
		t.Errorf("entry = %q/%q, want web/aaa", g.Loadout, g.ID)
	}
	// And it is found by identity afterwards, whatever the name says.
	if m.FindFor("aaa", "anything") == nil {
		t.Error("the stamped entry is not findable by identity")
	}
	if m.Rename("zzz", "web", "other") {
		t.Error("renamed an entry whose identity disagrees")
	}
}

// TestRawRoundTripsTheLockfileExactly: an undo has to put back the bytes that
// were there, not an equivalent re-marshalling of them.
func TestRawRoundTripsTheLockfileExactly(t *testing.T) {
	f := newFixture(t)

	if b, err := ReadRaw(f.root); err != nil || b != nil {
		t.Fatalf("ReadRaw with no lockfile = %q, %v; want nil", b, err)
	}
	f.install(f.loadout("frontend"))
	before, err := ReadRaw(f.root)
	if err != nil || len(before) == 0 {
		t.Fatalf("ReadRaw = %q, %v", before, err)
	}

	m, err := Load(f.root)
	if err != nil {
		t.Fatal(err)
	}
	m.Rename("", "frontend", "web")
	if err := m.Save(f.root); err != nil {
		t.Fatal(err)
	}
	if after, _ := ReadRaw(f.root); string(after) == string(before) {
		t.Fatal("the rename did not change the file, so the undo proves nothing")
	}

	if err := WriteRaw(f.root, before); err != nil {
		t.Fatal(err)
	}
	if got, _ := ReadRaw(f.root); string(got) != string(before) {
		t.Errorf("WriteRaw did not restore the file byte for byte:\n%s", got)
	}

	// Nil means there was no lockfile, so the file goes rather than being
	// emptied - a husk nobody can tell is inert is exactly what Save avoids too.
	if err := WriteRaw(f.root, nil); err != nil {
		t.Fatal(err)
	}
	if testutil.Exists(Path(f.root)) {
		t.Error("WriteRaw(nil) left a lockfile behind")
	}
}

// TestDroppingASkillReportsWhatIsHoldingItsDirectoryOpen: a skill leaving a
// garrison - dropped upstream, or its source detached - takes its own files and
// nothing else. A file no record accounts for is kept, and saying so is not
// optional: barracks is leaving somebody's work inside a directory it used to
// manage, and that must never be something they find out for themselves.
func TestDroppingASkillReportsWhatIsHoldingItsDirectoryOpen(t *testing.T) {
	f := newFixture(t)
	f.install(f.loadout("frontend"))

	mine := f.path(".claude/skills/css/notes.md")
	testutil.WriteFile(t, mine, "mine\n")

	res := f.install(f.loadout("frontend", "react"))

	var said bool
	for _, n := range res.Notices {
		if strings.Contains(n, "notes.md") && strings.Contains(n, "no record") {
			said = true
		}
	}
	if !said {
		t.Errorf("a file left behind in a dropped skill directory was not reported: %v", res.Notices)
	}
	if got := testutil.ReadFile(t, mine); got != "mine\n" {
		t.Errorf("the file was destroyed: %q", got)
	}
	if testutil.Exists(f.path(".claude/skills/css/SKILL.md")) {
		t.Error("the dropped skill's own file survived")
	}
}

// TestDroppingASkillTakesItsOwnDirectoriesWithIt is the other half of that rule,
// on the update path: what a dropped skill leaves behind is barracks' own
// directories, not the user's, and calling them foreign would report a problem
// that does not exist while leaving an empty directory in the diff.
func TestDroppingASkillTakesItsOwnDirectoriesWithIt(t *testing.T) {
	f := newFixture(t)
	testutil.WriteFile(t, filepath.Join(f.src.Dir, "skills", "css", "ref", "deep", "notes.md"), "reference\n")
	f.src.Commit(t, "add a nested reference")
	f.install(f.loadout("frontend"))

	res := f.install(f.loadout("frontend", "react"))

	for _, n := range res.Notices {
		if strings.Contains(n, "no record") {
			t.Errorf("barracks' own directory was reported as somebody else's: %q", n)
		}
	}
	if testutil.Exists(f.path(".claude/skills/css")) {
		t.Error("the dropped skill left an empty chain of its own directories behind")
	}
	f.mustInspect()
}

// TestADroppedSkillStillReportsWhatIsNestedInsideIt: the directories going with
// the skill must not take the reporting with them - a file of the user's, however
// deep, is still kept and still said out loud.
func TestADroppedSkillStillReportsWhatIsNestedInsideIt(t *testing.T) {
	f := newFixture(t)
	testutil.WriteFile(t, filepath.Join(f.src.Dir, "skills", "css", "ref", "notes.md"), "reference\n")
	f.src.Commit(t, "add a nested reference")
	f.install(f.loadout("frontend"))

	mine := f.path(".claude/skills/css/ref/mine.md")
	testutil.WriteFile(t, mine, "mine\n")

	res := f.install(f.loadout("frontend", "react"))

	var said bool
	for _, n := range res.Notices {
		if strings.Contains(n, "ref/mine.md") && strings.Contains(n, "no record") {
			said = true
		}
	}
	if !said {
		t.Errorf("a file nested inside a dropped skill was not reported: %v", res.Notices)
	}
	if got := testutil.ReadFile(t, mine); got != "mine\n" {
		t.Errorf("it was destroyed instead: %q", got)
	}
}
