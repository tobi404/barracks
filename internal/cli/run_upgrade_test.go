package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tobi404/barracks/internal/lease"
	"github.com/tobi404/barracks/internal/testutil"
)

// liveSession is a `barracks run` invocation that is genuinely in flight: a
// real child process holds the lease until the test releases it, so another
// invocation can act on that lease mid-session.
//
// A hand-forged process lease cannot stand in here. The failure these tests
// guard against is about the lease copy `run` is holding in memory, and only a
// real run has one.
type liveSession struct {
	h       *harness
	release string
	done    chan struct{}
	out     string
	errb    string
	err     error
}

// startLiveSession runs the loadout against a command that blocks until
// finish() is called, and returns once the child is known to be up.
func startLiveSession(h *harness, name string) *liveSession {
	h.t.Helper()
	started := filepath.Join(h.root, "session-started")
	s := &liveSession{h: h, release: filepath.Join(h.root, "session-release"), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		s.out, s.errb, s.err = h.run("run", name, "--", "sh", "-c",
			`: > "$1"; while [ ! -f "$2" ]; do sleep 0.02; done`, "sh", started, s.release)
	}()
	waitForFile(h.t, started, "the run session never started")
	return s
}

// finish releases the child and waits for the run invocation to return.
func (s *liveSession) finish() {
	s.h.t.Helper()
	testutil.WriteFile(s.h.t, s.release, "go\n")
	select {
	case <-s.done:
	case <-time.After(30 * time.Second):
		s.h.t.Fatal("the run session did not exit after being released")
	}
	if s.err != nil {
		s.h.t.Fatalf("run failed: %v\nstdout: %s\nstderr: %s", s.err, s.out, s.errb)
	}
}

func waitForFile(t *testing.T, path, msg string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if testutil.Exists(path) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}

// liveLease returns the single lease record on disk.
func liveLease(t *testing.T, h *harness) *lease.Lease {
	t.Helper()
	leases, err := leaseStore(t, h).List()
	if err != nil {
		t.Fatalf("list leases: %v", err)
	}
	if len(leases) != 1 {
		t.Fatalf("expected exactly one lease, got %d", len(leases))
	}
	return leases[0]
}

// TestRunRecallsAfterUpgradeRelinkedItsSpawn is the guarantee `upgrade
// --include-running` must not be allowed to break: whatever happened to the
// spawn during the session, the session's own exit still leaves the repository
// exactly as it found it.
//
// The lease record is rewritten by the upgrade. Recalling from the copy `run`
// captured at spawn time compares the relinked symlink against a stale target,
// so barracks refuses to remove a link it created itself - and says a path it
// certainly created was not created by it.
func TestRunRecallsAfterUpgradeRelinkedItsSpawn(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend")
	statusBefore := h.work.Status(t)

	session := startLiveSession(h, "frontend")

	h.src.AddSkills(t, testutil.Skill{Path: "skills/react", Body: "---\nname: react\n---\n\nversion two\n"})
	h.src.Commit(t, "move react on")

	out := h.mustRun("upgrade", "frontend", "--include-running")
	if !strings.Contains(out, "relinked") {
		t.Fatalf("--include-running did not relink the live spawn:\n%s", out)
	}

	session.finish()

	if testutil.Exists(filepath.Join(h.work.Dir, ".claude")) {
		t.Errorf("the session left its spawn behind after being relinked:\n%s",
			strings.Join(testutil.Entries(t, h.skillsDir()), " "))
	}
	if got := h.work.Status(t); got != statusBefore {
		t.Errorf("git status is not clean after the session exited:\n%s", got)
	}
	if strings.Contains(session.errb, "barracks did not create it") {
		t.Errorf("a path barracks created was reported as foreign:\n%s", session.errb)
	}
}

// TestRunFallsBackWhenTheLeaseRecordCannotBeReRead covers the other side of
// that re-read: an unreadable record must neither remove the wrong path nor
// pass in silence, and what it says must be true.
func TestRunFallsBackWhenTheLeaseRecordCannotBeReRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: an unreadable file cannot be simulated with permissions")
	}
	h := newHarness(t)
	h.equipped("frontend")
	statusBefore := h.work.Status(t)

	session := startLiveSession(h, "frontend")

	h.src.AddSkills(t, testutil.Skill{Path: "skills/react", Body: "---\nname: react\n---\n\nversion two\n"})
	h.src.Commit(t, "move react on")
	h.mustRun("upgrade", "frontend", "--include-running")

	// The record now describes the relinked spawn, and the copy the session is
	// holding does not. Take the record away from the exit-time re-read.
	record := filepath.Join(h.layout.LeasesDir(), liveLease(t, h).ID+".yaml")
	if err := os.Chmod(record, 0o000); err != nil {
		t.Fatalf("chmod lease record: %v", err)
	}

	session.finish()

	if !strings.Contains(session.errb, "could not re-read the lease record") {
		t.Errorf("the fallback was silent about reading the record:\n%s", session.errb)
	}
	if strings.Contains(session.errb, "barracks did not create it") {
		t.Errorf("an unconfirmed path was reported as foreign:\n%s", session.errb)
	}
	if !strings.Contains(session.errb, "next barracks command") {
		t.Errorf("the fallback did not say what happens to what it could not remove:\n%s", session.errb)
	}
	// The relinked link is barracks', but the stale copy cannot prove it, so it
	// stays exactly where it is rather than being removed on a guess.
	if !testutil.IsSymlink(t, filepath.Join(h.skillsDir(), "react")) {
		t.Error("the fallback removed a path it could not confirm")
	}

	// And it really is left for the next reap: the record survived, so once it
	// can be read again the owner is gone and the cleanup completes.
	if err := os.Chmod(record, 0o644); err != nil {
		t.Fatalf("restore lease record: %v", err)
	}
	out := h.mustRun("deployed")
	if !strings.Contains(out, "reaped frontend") {
		t.Errorf("the next command did not finish the recall:\n%s", out)
	}
	if testutil.Exists(filepath.Join(h.work.Dir, ".claude")) {
		t.Error("the reap left the spawn behind")
	}
	if got := h.work.Status(t); got != statusBefore {
		t.Errorf("git status is not clean after the reap:\n%s", got)
	}
}

// TestReaperRecallsARelinkedSpawnAfterTheSessionDies guards the crash path,
// which works because the reaper reads the lease off disk rather than carrying
// a copy: a session killed outright after an upgrade relinked its spawn is
// still cleaned up completely by the next command.
func TestReaperRecallsARelinkedSpawnAfterTheSessionDies(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend")
	statusBefore := h.work.Status(t)
	h.mustRun("spawn", "frontend")

	store := leaseStore(t, h)
	l := liveLease(t, h)
	l.Kind = lease.KindProcess
	l.Owner = ownerFor(4242, "a-live-agent-session")
	if err := store.Save(l); err != nil {
		t.Fatal(err)
	}
	h.prober.alive[4242] = "a-live-agent-session"

	h.src.AddSkills(t, testutil.Skill{Path: "skills/react", Body: "---\nname: react\n---\n\nversion two\n"})
	h.src.Commit(t, "move react on")
	h.mustRun("upgrade", "frontend", "--include-running")

	// The session is killed outright: no exit-time recall ever runs.
	delete(h.prober.alive, 4242)

	out := h.mustRun("deployed")
	if !strings.Contains(out, "reaped frontend") {
		t.Fatalf("the reaper did not recall the relinked spawn:\n%s", out)
	}
	if testutil.Exists(filepath.Join(h.work.Dir, ".claude")) {
		t.Error("the reaper left the relinked spawn behind")
	}
	if got := h.work.Status(t); got != statusBefore {
		t.Errorf("git status is not clean after the reap:\n%s", got)
	}
}
