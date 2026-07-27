package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tobi404/barracks/internal/gitcmd"
	"github.com/tobi404/barracks/internal/lease"
	"github.com/tobi404/barracks/internal/paths"
	"github.com/tobi404/barracks/internal/proc"
	"github.com/tobi404/barracks/internal/testutil"
)

// harness runs the real command tree in-process against temp directories, a
// fake clock, and a fake process prober. Nothing here touches the network or
// the user's home directory.
type harness struct {
	t      *testing.T
	root   string
	src    *testutil.GitRepo
	work   *testutil.GitRepo
	layout paths.Layout
	now    time.Time
	prober *stubProber
	env    map[string]string
	home   string
}

type stubProber struct {
	alive map[int]string
	// unknowable stands in for a prober that cannot tell: no ps on PATH, no
	// permission, no /proc.
	unknowable map[int]bool
}

func (p *stubProber) Identity(pid int) (string, error) {
	if p.unknowable[pid] {
		return "", errors.New("cannot identify this process here")
	}
	if tok, ok := p.alive[pid]; ok {
		return tok, nil
	}
	// Fall back to the real prober so `run` can identify its own child.
	return proc.OSProber{}.Identity(pid)
}

func newHarness(t *testing.T, skills ...testutil.Skill) *harness {
	t.Helper()
	if len(skills) == 0 {
		skills = []testutil.Skill{{Path: "skills/react"}, {Path: "skills/css"}, {Path: "skills/legacy"}}
	}
	root := t.TempDir()
	h := &harness{
		t:      t,
		root:   root,
		src:    testutil.NewSkillRepo(t, filepath.Join(root, "src"), skills...),
		work:   testutil.NewGitRepo(t, filepath.Join(root, "work")),
		layout: paths.Layout{Config: filepath.Join(root, "brk"), Data: filepath.Join(root, "brk")},
		now:    time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC),
		prober: &stubProber{alive: map[int]string{}, unknowable: map[int]bool{}},
		env:    map[string]string{},
		home:   filepath.Join(root, "home"),
	}
	testutil.WriteFile(t, filepath.Join(h.work.Dir, "README.md"), "hello\n")
	h.work.Commit(t, "initial")
	return h
}

// run executes one barracks invocation and returns stdout, stderr, and error.
func (h *harness) run(args ...string) (string, string, error) {
	h.t.Helper()
	var out, errb bytes.Buffer
	env := &Env{
		Out:    &out,
		Err:    &errb,
		Cwd:    h.work.Dir,
		Layout: h.layout,
		Now:    func() time.Time { return h.now },
		Prober: h.prober,
		Git:    gitcmd.Git{},
		Getenv: func(k string) string { return h.env[k] },
		Home:   func() (string, error) { return h.home, nil },
	}
	cmd := New(env)
	cmd.SetArgs(args)
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	err := cmd.Execute()
	return out.String(), errb.String(), err
}

// mustRun fails the test if the invocation errors.
func (h *harness) mustRun(args ...string) string {
	h.t.Helper()
	out, errb, err := h.run(args...)
	if err != nil {
		h.t.Fatalf("barracks %s failed: %v\nstdout: %s\nstderr: %s", strings.Join(args, " "), err, out, errb)
	}
	return out
}

func (h *harness) sourceArg(subpath string) string {
	if subpath == "" {
		return h.src.Dir
	}
	return h.src.Dir + "#main:" + subpath
}

func (h *harness) skillsDir() string { return filepath.Join(h.work.Dir, ".claude", "skills") }

// TestFullLifecycle is acceptance criteria 1 and 2: every command works end to
// end, and spawn followed by recall leaves the repository exactly as it was.
func TestFullLifecycle(t *testing.T) {
	h := newHarness(t)
	excludeBefore := h.work.ReadExclude(t)
	statusBefore := h.work.Status(t)

	out := h.mustRun("train", "frontend", "--description", "web skills")
	if !strings.Contains(out, "trained loadout") {
		t.Errorf("train output = %q", out)
	}

	out = h.mustRun("equip", "frontend", h.sourceArg("skills"), "--except", "legacy")
	for _, want := range []string{"equipped frontend", "+ react", "+ css"} {
		if !strings.Contains(out, want) {
			t.Errorf("equip output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "+ legacy") {
		t.Errorf("--except did not exclude the legacy skill:\n%s", out)
	}

	out = h.mustRun("list", "--verbose")
	if !strings.Contains(out, "frontend") || !strings.Contains(out, "web skills") {
		t.Errorf("list output = %q", out)
	}

	out = h.mustRun("spawn", "frontend")
	if !strings.Contains(out, "spawned frontend") || !strings.Contains(out, "until recalled") {
		t.Errorf("spawn output = %q", out)
	}
	for _, name := range []string{"react", "css"} {
		if !testutil.IsSymlink(t, filepath.Join(h.skillsDir(), name)) {
			t.Errorf("skill %q was not spawned as a symlink", name)
		}
	}
	if status := h.work.Status(t); status != statusBefore {
		t.Errorf("git status changed after spawn:\n%s", status)
	}

	out = h.mustRun("deployed")
	if !strings.Contains(out, "frontend") || !strings.Contains(out, "manual") {
		t.Errorf("deployed output = %q", out)
	}

	out = h.mustRun("recall", "frontend")
	if !strings.Contains(out, "recalled frontend") {
		t.Errorf("recall output = %q", out)
	}

	// Byte for byte back to the start.
	if got := h.work.ReadExclude(t); got != excludeBefore {
		t.Errorf(".git/info/exclude not restored\n got: %q\nwant: %q", got, excludeBefore)
	}
	if status := h.work.Status(t); status != statusBefore {
		t.Errorf("git status not restored:\n%s", status)
	}
	if testutil.Exists(filepath.Join(h.work.Dir, ".claude")) {
		t.Error(".claude survived the recall")
	}

	out = h.mustRun("deployed")
	if !strings.Contains(out, "nothing deployed") {
		t.Errorf("deployed after recall = %q", out)
	}
}

// TestDeadlineLeaseIsReapedByTheNextCommand is acceptance criterion 3.
func TestDeadlineLeaseIsReapedByTheNextCommand(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"))
	h.mustRun("spawn", "frontend", "--for", "1s")

	if !testutil.IsSymlink(t, filepath.Join(h.skillsDir(), "react")) {
		t.Fatal("spawn --for did not create the skills")
	}

	// The clock moves past the deadline; no daemon runs in between.
	h.now = h.now.Add(2 * time.Second)

	out := h.mustRun("list")
	if !strings.Contains(out, "reaped frontend") {
		t.Errorf("the next command should have reaped the expired lease:\n%s", out)
	}
	if testutil.Exists(filepath.Join(h.work.Dir, ".claude")) {
		t.Error("the expired spawn was not removed")
	}
	if got := h.work.ReadExclude(t); strings.Contains(got, "barracks:") {
		t.Errorf("reaping left an exclude block behind:\n%s", got)
	}
}

// TestRunRecallsWhenTheCommandExits is acceptance criterion 4's happy path.
func TestRunRecallsWhenTheCommandExits(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"))

	marker := filepath.Join(h.root, "seen.txt")
	out := h.mustRun("run", "frontend", "--", "sh", "-c", "ls .claude/skills > "+marker)

	seen, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("the command did not run: %v", err)
	}
	for _, name := range []string{"react", "css"} {
		if !strings.Contains(string(seen), name) {
			t.Errorf("skill %q was not visible to the command:\n%s", name, seen)
		}
	}
	if !strings.Contains(out, "recalled frontend") {
		t.Errorf("run did not recall on exit:\n%s", out)
	}
	if testutil.Exists(filepath.Join(h.work.Dir, ".claude")) {
		t.Error("run left the skills behind after the command exited")
	}
}

func TestRunPropagatesTheExitCode(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"))

	_, _, err := h.run("run", "frontend", "--", "sh", "-c", "exit 7")
	var exit *ExitError
	if !asExitError(err, &exit) {
		t.Fatalf("run returned %v, want an ExitError", err)
	}
	if exit.Code != 7 {
		t.Errorf("exit code = %d, want 7", exit.Code)
	}
	// Even on a non-zero exit the loadout is recalled.
	if testutil.Exists(filepath.Join(h.work.Dir, ".claude")) {
		t.Error("a failing command left the skills behind")
	}
}

// TestRunRefusesWhenThisProcessCannotBeIdentified guards the lease model's
// premise. A lease recorded without a start token would leave the reaper
// comparing a bare PID, so it could be kept alive forever by an unrelated
// process that later inherited the number.
func TestRunRefusesWhenThisProcessCannotBeIdentified(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"))

	h.prober.unknowable[os.Getpid()] = true
	_, _, err := h.run("run", "frontend", "--", "sh", "-c", "true")
	if err == nil || !strings.Contains(err.Error(), "cannot identify this process") {
		t.Fatalf("run with an unidentifiable process = %v, want a refusal", err)
	}
	if testutil.Exists(filepath.Join(h.work.Dir, ".claude")) {
		t.Error("the refused run spawned anyway")
	}
}

// TestRunLeaseSurvivesToTheReaperWhenTheOwnerDies is acceptance criterion 4's
// crash path: the process lease is cleaned up by the next command.
func TestRunLeaseSurvivesToTheReaperWhenTheOwnerDies(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"))

	// Simulate a `barracks run` that was killed outright: spawn a process
	// lease by hand, owned by a PID that is not running.
	h.mustRun("spawn", "frontend")
	leases, _ := leaseStore(t, h).List()
	if len(leases) != 1 {
		t.Fatalf("expected one lease, got %d", len(leases))
	}
	l := leases[0]
	l.Kind = "process"
	l.Owner = ownerFor(0, "gone")
	if err := leaseStore(t, h).Save(l); err != nil {
		t.Fatal(err)
	}

	out := h.mustRun("deployed")
	if !strings.Contains(out, "reaped frontend") {
		t.Errorf("the reaper did not clean up after a dead owner:\n%s", out)
	}
	if testutil.Exists(filepath.Join(h.work.Dir, ".claude")) {
		t.Error("a dead process lease left its skills behind")
	}
}

// TestPIDReuseDoesNotKeepALeaseAlive covers the identity check through the CLI.
func TestPIDReuseDoesNotKeepALeaseAlive(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"))
	h.mustRun("spawn", "frontend")

	store := leaseStore(t, h)
	leases, _ := store.List()
	l := leases[0]
	l.Kind = "process"
	l.Owner = ownerFor(4242, "the-original-process")
	if err := store.Save(l); err != nil {
		t.Fatal(err)
	}
	// PID 4242 is live, but it is a different process now.
	h.prober.alive[4242] = "some-other-process-entirely"

	out := h.mustRun("deployed")
	if !strings.Contains(out, "reused") {
		t.Errorf("a reused PID should not keep the lease alive:\n%s", out)
	}
	if testutil.Exists(filepath.Join(h.work.Dir, ".claude")) {
		t.Error("the lease was not revoked despite the PID being reused")
	}

	// And the matching identity keeps it alive.
	h.mustRun("spawn", "frontend")
	leases, _ = store.List()
	l = leases[0]
	l.Kind = "process"
	l.Owner = ownerFor(4242, "some-other-process-entirely")
	if err := store.Save(l); err != nil {
		t.Fatal(err)
	}
	h.mustRun("deployed")
	if !testutil.IsSymlink(t, filepath.Join(h.skillsDir(), "react")) {
		t.Error("a live owner with a matching identity had its lease revoked")
	}
}

// TestUserFilesAreNeverRemoved is acceptance criterion 5.
func TestUserFilesAreNeverRemoved(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"))
	h.mustRun("spawn", "frontend")

	// A skill the user wrote themselves, sitting alongside the spawned ones.
	mine := filepath.Join(h.skillsDir(), "my-own-skill")
	testutil.WriteFile(t, filepath.Join(mine, "SKILL.md"), "mine, do not touch")

	// And one of the spawned links replaced by the user's own directory.
	taken := filepath.Join(h.skillsDir(), "react")
	if err := os.Remove(taken); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(taken, "SKILL.md"), "I took this one over")

	out, errb, err := h.run("recall", "frontend")
	if err != nil {
		t.Fatalf("recall failed instead of reporting: %v", err)
	}
	if !strings.Contains(out, "recalled frontend") {
		t.Errorf("recall output = %q", out)
	}
	// The tool must say so rather than fail silently.
	if !strings.Contains(errb, "left in place") || !strings.Contains(errb, "react") {
		t.Errorf("recall did not report the path it refused to remove:\n%s", errb)
	}

	for _, p := range []string{mine, taken} {
		body, err := os.ReadFile(filepath.Join(p, "SKILL.md"))
		if err != nil {
			t.Fatalf("recall destroyed %s: %v", p, err)
		}
		if len(body) == 0 {
			t.Errorf("%s was emptied", p)
		}
	}
	if !testutil.Exists(h.skillsDir()) {
		t.Error("a directory containing user files was pruned")
	}
}

// TestSharedSourceIsFetchedOnce is acceptance criterion 6.
func TestSharedSourceIsFetchedOnce(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")
	h.mustRun("train", "backend")

	out := h.mustRun("equip", "frontend", h.sourceArg("skills"), "--only", "react")
	if !strings.Contains(out, "fetched source") {
		t.Errorf("the first equip should have fetched:\n%s", out)
	}
	out = h.mustRun("equip", "backend", h.sourceArg("skills"), "--only", "css")
	if !strings.Contains(out, "reused cached source") {
		t.Errorf("the second equip refetched a source the store already has:\n%s", out)
	}

	// Exactly one store entry exists for that repository at that commit.
	var entries []string
	err := filepath.Walk(h.layout.StoreDir(), func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() && strings.Contains(filepath.Base(p), "@") {
			entries = append(entries, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("store holds %d copies of the source, want 1: %v", len(entries), entries)
	}
}

func TestSpawnGlobalUsesTheTargetMap(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"), "--only", "react")

	tests := []struct {
		name   string
		target string
		want   string
	}{
		{"claude", "claude", filepath.Join(h.home, ".claude", "skills", "react")},
		{"agents", "agents", filepath.Join(h.home, ".agents", "skills", "react")},
		{"codex is an alias for agents", "codex", filepath.Join(h.home, ".agents", "skills", "react")},
		{"cursor", "cursor", filepath.Join(h.home, ".cursor", "skills", "react")},
		{"opencode", "opencode", filepath.Join(h.home, ".config", "opencode", "skills", "react")},
		{"windsurf", "windsurf", filepath.Join(h.home, ".codeium", "windsurf", "skills", "react")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h.mustRun("spawn", "frontend", "--global", "--target", tt.target)
			if !testutil.IsSymlink(t, tt.want) {
				t.Fatalf("global spawn for %q did not land at %s", tt.target, tt.want)
			}
			h.mustRun("recall", "frontend", "--global", "--target", tt.target)
			if testutil.Exists(tt.want) {
				t.Errorf("global recall for %q left %s behind", tt.target, tt.want)
			}
		})
	}
}

func TestTargetsCommandListsTheMap(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("targets")
	for _, want := range []string{"claude", "Claude Code", ".claude/skills", "opencode", "OpenCode"} {
		if !strings.Contains(out, want) {
			t.Errorf("targets output missing %q:\n%s", want, out)
		}
	}
}

func TestCommandErrors(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")

	tests := []struct {
		name string
		args []string
		want string
	}{
		{"train an existing loadout", []string{"train", "frontend"}, "already exists"},
		{"train an unsafe name", []string{"train", "../escape"}, "invalid loadout name"},
		{"equip an unknown loadout", []string{"equip", "nope", "gh:o/r"}, "loadout not found"},
		{"equip with a bad source", []string{"equip", "frontend", "not a source"}, "parse source"},
		{"spawn an unknown loadout", []string{"spawn", "nope"}, "loadout not found"},
		{"spawn an empty loadout", []string{"spawn", "frontend"}, "equip it with a source"},
		{"spawn for an unknown target", []string{"spawn", "frontend", "--target", "emacs"}, "unknown target"},
		{"recall nothing", []string{"recall", "frontend"}, "not deployed"},
		{"recall with no name", []string{"recall"}, "--all"},
		{"recall all with nothing deployed", []string{"recall", "--all"}, "nothing is deployed"},
		{"recall a name together with --all", []string{"recall", "frontend", "--all"}, "cannot combine"},
		{"run with no command", []string{"run", "frontend"}, "arg"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := h.run(tt.args...)
			if err == nil {
				t.Fatalf("barracks %s should have failed", strings.Join(tt.args, " "))
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestEquipFilterErrors(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")

	_, _, err := h.run("equip", "frontend", h.sourceArg("skills"), "--only", "nothing-matches-*")
	if err == nil || !strings.Contains(err.Error(), "filters matched none") {
		t.Errorf("err = %v, want a report of which skills were available", err)
	}

	// A source with no skills at all.
	empty := testutil.NewGitRepo(t, filepath.Join(h.root, "empty"))
	testutil.WriteFile(t, filepath.Join(empty.Dir, "README.md"), "nothing here")
	empty.Commit(t, "initial")

	_, _, err = h.run("equip", "frontend", empty.Dir)
	if err == nil || !strings.Contains(err.Error(), "no skills found") {
		t.Errorf("err = %v, want a complaint that the source has no SKILL.md directories", err)
	}
}

func TestListWithNoLoadouts(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("list")
	if !strings.Contains(out, "no loadouts trained yet") {
		t.Errorf("list output = %q", out)
	}
}

func TestDeployedEverywhere(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"), "--only", "react")
	h.mustRun("spawn", "frontend", "--global")

	// Not visible from the repo scope.
	out := h.mustRun("deployed")
	if !strings.Contains(out, "nothing deployed") {
		t.Errorf("a global spawn should not show as deployed in the repo:\n%s", out)
	}
	out = h.mustRun("deployed", "--everywhere")
	if !strings.Contains(out, "frontend") || !strings.Contains(out, "global") {
		t.Errorf("deployed --everywhere output = %q", out)
	}
}

func TestRecallAll(t *testing.T) {
	h := newHarness(t)
	for _, name := range []string{"frontend", "backend"} {
		h.mustRun("train", name)
	}
	h.mustRun("equip", "frontend", h.sourceArg("skills"), "--only", "react")
	h.mustRun("equip", "backend", h.sourceArg("skills"), "--only", "css")
	h.mustRun("spawn", "frontend")
	h.mustRun("spawn", "backend")

	// Narrowing an --all invocation with a name is ambiguous, and recall is the
	// one command whose job is removal, so it is refused rather than guessed at.
	_, _, err := h.run("recall", "frontend", "--all")
	if err == nil || !strings.Contains(err.Error(), "cannot combine") {
		t.Fatalf("recall frontend --all = %v, want a refusal naming the conflict", err)
	}
	for _, name := range []string{"react", "css"} {
		if !testutil.IsSymlink(t, filepath.Join(h.skillsDir(), name)) {
			t.Errorf("the refused recall removed %s anyway", name)
		}
	}

	out := h.mustRun("recall", "--all")
	for _, want := range []string{"recalled frontend", "recalled backend"} {
		if !strings.Contains(out, want) {
			t.Errorf("recall --all output missing %q:\n%s", want, out)
		}
	}
	if testutil.Exists(filepath.Join(h.work.Dir, ".claude")) {
		t.Error("recall --all left the directory behind")
	}
}

func TestDisband(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"), "--only", "react")
	h.mustRun("spawn", "frontend")

	_, _, err := h.run("disband", "frontend")
	if err == nil || !strings.Contains(err.Error(), "still deployed") {
		t.Fatalf("disband while deployed = %v, want a refusal", err)
	}

	h.mustRun("recall", "frontend")
	out := h.mustRun("disband", "frontend")
	if !strings.Contains(out, "disbanded") {
		t.Errorf("disband output = %q", out)
	}
	if _, _, err := h.run("spawn", "frontend"); err == nil {
		t.Error("the disbanded loadout is still usable")
	}
}

func TestSpawnTwiceIsRefused(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"), "--only", "react")
	h.mustRun("spawn", "frontend")

	_, _, err := h.run("spawn", "frontend")
	if err == nil || !strings.Contains(err.Error(), "already spawned") {
		t.Fatalf("second spawn = %v, want a refusal", err)
	}
	// The first spawn is untouched.
	if !testutil.IsSymlink(t, filepath.Join(h.skillsDir(), "react")) {
		t.Error("the refused second spawn damaged the first")
	}
}

func TestEquipPinsTheCommitSoSpawnsReproduce(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"))

	// The branch moves on after equipping.
	h.src.AddSkills(t, testutil.Skill{Path: "skills/brand-new"})
	h.src.Commit(t, "add a skill after equipping")

	h.mustRun("spawn", "frontend")
	if testutil.Exists(filepath.Join(h.skillsDir(), "brand-new")) {
		t.Error("spawn picked up a commit later than the pin; the loadout is not reproducible")
	}
	if !testutil.IsSymlink(t, filepath.Join(h.skillsDir(), "react")) {
		t.Error("spawn did not use the pinned commit")
	}
}

// TestReEquippingTheSameSourceRePinsInPlace covers the natural repeat action:
// equipping a source a loadout already carries must re-pin it, not attach a
// second copy. Two entries for one source collide on every skill they provide
// and leave the loadout unspawnable.
func TestReEquippingTheSameSourceRePinsInPlace(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"))

	out := h.mustRun("equip", "frontend", h.sourceArg("skills"))
	if !strings.Contains(out, "already equipped") || !strings.Contains(out, "still pinned at") {
		t.Errorf("re-equipping an unchanged source = %q, want it reported as already equipped", out)
	}

	// The branch moves on; re-equipping is how a user re-pins.
	h.src.AddSkills(t, testutil.Skill{Path: "skills/brand-new"})
	h.src.Commit(t, "add a skill after equipping")

	out = h.mustRun("equip", "frontend", h.sourceArg("skills"))
	if !strings.Contains(out, "re-pinned") || !strings.Contains(out, "->") {
		t.Errorf("re-equipping after the ref moved = %q, want the new pin reported", out)
	}

	if listOut := h.mustRun("list"); !strings.Contains(listOut, "1 source") {
		t.Errorf("the loadout carries the source more than once:\n%s", listOut)
	}

	// The proof that matters: the loadout is still spawnable, at the new pin.
	h.mustRun("spawn", "frontend")
	if !testutil.IsSymlink(t, filepath.Join(h.skillsDir(), "brand-new")) {
		t.Error("the re-pinned commit was not used by the spawn")
	}
}

// TestEquippingADifferentRefIsADistinctSource is the other half of the rule:
// the same repository at another ref is not the same source and must be kept
// alongside, not collapsed into the existing entry.
func TestEquippingADifferentRefIsADistinctSource(t *testing.T) {
	h := newHarness(t)
	h.src.Tag(t, "v1.0.0")
	h.mustRun("train", "frontend")

	h.mustRun("equip", "frontend", h.src.Dir+"#main:skills", "--only", "react")
	out := h.mustRun("equip", "frontend", h.src.Dir+"#v1.0.0:skills", "--only", "css")
	if strings.Contains(out, "already equipped") {
		t.Errorf("a different ref was collapsed into the existing source:\n%s", out)
	}
	if listOut := h.mustRun("list"); !strings.Contains(listOut, "2 sources") {
		t.Errorf("list = %s, want both refs recorded as separate sources", listOut)
	}
}

func TestEquipPinnedRef(t *testing.T) {
	h := newHarness(t)
	h.src.Tag(t, "v1.0.0")
	h.src.AddSkills(t, testutil.Skill{Path: "skills/after-tag"})
	h.src.Commit(t, "post-tag work")

	h.mustRun("train", "frontend")
	out := h.mustRun("equip", "frontend", h.src.Dir+"#v1.0.0:skills")
	if strings.Contains(out, "after-tag") {
		t.Errorf("equipping a tag picked up later commits:\n%s", out)
	}
	if !strings.Contains(out, "+ react") {
		t.Errorf("equipping a tag missed the skills at that tag:\n%s", out)
	}
}

// TestHelpUsesTheRTSVocabulary is acceptance criterion 8.
func TestHelpUsesTheRTSVocabulary(t *testing.T) {
	h := newHarness(t)

	root := h.mustRun("--help")
	for _, want := range []string{"train", "equip", "spawn", "recall", "deployed", "list", "run", "assign", "targets"} {
		if !strings.Contains(root, want) {
			t.Errorf("root help does not mention %q:\n%s", want, root)
		}
	}

	tests := []struct {
		command string
		wants   []string
	}{
		{"train", []string{"loadout", "bundle of agent skills", "barracks train"}},
		{"equip", []string{"git source", "SKILL.md", "gh:owner/repo", "--only"}},
		{"spawn", []string{"symlinked", ".git/info/exclude", "--for", "--global"}},
		{"recall", []string{"exactly as it was", "barracks store", "left alone and reported"}},
		{"deployed", []string{"deployed in this repo", "reaps expired leases"}},
		{"list", []string{"loadout", "barracks list"}},
		{"run", []string{"recalls the", "process identity", "barracks run"}},
		{"assign", []string{"belongs to the loadout", "--auto", "barracks assign"}},
		{"targets", []string{"documentation those paths were read from", "barracks assign", "present here"}},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			out := h.mustRun(tt.command, "--help")
			for _, want := range tt.wants {
				if !strings.Contains(out, want) {
					t.Errorf("`barracks %s --help` does not mention %q:\n%s", tt.command, want, out)
				}
			}
			if len(out) < 200 {
				t.Errorf("`barracks %s --help` is too thin to be useful:\n%s", tt.command, out)
			}
		})
	}
}

func TestVersionIsReported(t *testing.T) {
	h := newHarness(t)
	Version = "1.2.3-test"
	defer func() { Version = "dev" }()
	out := h.mustRun("--version")
	if !strings.Contains(out, "1.2.3-test") {
		t.Errorf("--version output = %q", out)
	}
}

func TestDefaultEnvIsUsable(t *testing.T) {
	t.Setenv(paths.EnvHome, t.TempDir())
	env, err := DefaultEnv()
	if err != nil {
		t.Fatalf("DefaultEnv: %v", err)
	}
	if env.Out == nil || env.Err == nil || env.Prober == nil || env.Now == nil {
		t.Errorf("DefaultEnv left a collaborator unset: %+v", env)
	}
	if err := env.init(); err != nil {
		t.Fatalf("init: %v", err)
	}
}

func TestMainReturnsAnExitCode(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(paths.EnvHome, dir)
	if code := Main([]string{"list"}, "test"); code != 0 {
		t.Errorf("Main(list) = %d, want 0", code)
	}
	if code := Main([]string{"spawn", "does-not-exist"}, "test"); code == 0 {
		t.Error("Main should report a non-zero code when a command fails")
	}
}

func TestPlural(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{{0, "skills"}, {1, "skill"}, {2, "skills"}}
	for _, tt := range tests {
		if got := plural(tt.n, "skill", "skills"); got != tt.want {
			t.Errorf("plural(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestReapReportsBrokenLeaseRecords(t *testing.T) {
	h := newHarness(t)
	h.mustRun("list") // creates the directories
	testutil.WriteFile(t, filepath.Join(h.layout.LeasesDir(), "broken.yaml"), "{{{")

	_, errb, err := h.run("list")
	if err != nil {
		t.Fatalf("a broken lease record should not break the command: %v", err)
	}
	if !strings.Contains(errb, "unreadable lease record") {
		t.Errorf("stderr should report the broken record:\n%s", errb)
	}
}

func TestListReportsBrokenLoadoutFiles(t *testing.T) {
	h := newHarness(t)
	h.mustRun("list")
	testutil.WriteFile(t, filepath.Join(h.layout.LoadoutsDir(), "broken.yaml"), "equipment: [oh no")

	_, errb, err := h.run("list")
	if err != nil {
		t.Fatalf("a broken loadout file should not break list: %v", err)
	}
	if !strings.Contains(errb, "broken") {
		t.Errorf("stderr should name the unreadable loadout:\n%s", errb)
	}
}

// leaseStore opens the harness's lease store directly, for tests that need to
// forge a lease record.
func leaseStore(t *testing.T, h *harness) *lease.Store {
	t.Helper()
	return lease.NewStore(h.layout.LeasesDir())
}

func ownerFor(pid int, token string) *lease.Owner {
	return &lease.Owner{PID: pid, StartToken: token, Command: "test"}
}
