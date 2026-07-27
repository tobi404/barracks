package lease

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tobi404/barracks/internal/proc"
	"github.com/tobi404/barracks/internal/testutil"
)

// fakeProber answers from a table so PID-identity behaviour is testable without
// spawning real processes.
type fakeProber struct {
	// alive maps pid -> start token.
	alive map[int]string
	// fail maps pid -> an error that is neither nil nor ErrNotRunning.
	fail map[int]error
	// calls counts Identity invocations.
	calls int
}

func (p *fakeProber) Identity(pid int) (string, error) {
	p.calls++
	if err, ok := p.fail[pid]; ok {
		return "", err
	}
	tok, ok := p.alive[pid]
	if !ok {
		return "", proc.ErrNotRunning
	}
	return tok, nil
}

// TestOwnerAlive is the PID-identity check. A bare PID is never enough: a live
// PID whose start token differs is a different process, and the lease is dead.
func TestOwnerAlive(t *testing.T) {
	prober := &fakeProber{
		alive: map[int]string{
			100: "start-token-100",
			200: "start-token-200",
		},
		fail: map[int]error{
			300: errors.New("permission denied reading process table"),
		},
	}

	tests := []struct {
		name       string
		owner      *Owner
		wantAlive  bool
		wantReason string
	}{
		{
			name:      "live pid with matching identity",
			owner:     &Owner{PID: 100, StartToken: "start-token-100"},
			wantAlive: true,
		},
		{
			name:       "pid reused by a different process",
			owner:      &Owner{PID: 100, StartToken: "start-token-from-a-dead-process"},
			wantAlive:  false,
			wantReason: "reused",
		},
		{
			name:       "pid no longer exists",
			owner:      &Owner{PID: 999, StartToken: "whatever"},
			wantAlive:  false,
			wantReason: "exited",
		},
		{
			name:       "no owner recorded",
			owner:      nil,
			wantAlive:  false,
			wantReason: "no owner process recorded",
		},
		{
			name:       "zero pid",
			owner:      &Owner{PID: 0, StartToken: "x"},
			wantAlive:  false,
			wantReason: "no owner process recorded",
		},
		{
			name:      "prober cannot tell - treated as alive",
			owner:     &Owner{PID: 300, StartToken: "anything"},
			wantAlive: true,
		},
		{
			name:      "live pid with no recorded token is trusted",
			owner:     &Owner{PID: 200},
			wantAlive: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			alive, reason := OwnerAlive(prober, tt.owner)
			if alive != tt.wantAlive {
				t.Errorf("alive = %v (%q), want %v", alive, reason, tt.wantAlive)
			}
			if tt.wantReason != "" && !strings.Contains(reason, tt.wantReason) {
				t.Errorf("reason = %q, want it to mention %q", reason, tt.wantReason)
			}
		})
	}
}

func TestOwnerAliveWithoutAProberIsConservative(t *testing.T) {
	alive, _ := OwnerAlive(nil, &Owner{PID: 1, StartToken: "x"})
	if !alive {
		t.Fatal("with no prober available barracks must assume the owner is alive")
	}
}

// reapScene wires a lease store, a store guard, and a fake clock.
type reapScene struct {
	root   string
	store  string
	skills string
	leases *Store
	now    time.Time
	prober *fakeProber
}

func newReapScene(t *testing.T) *reapScene {
	t.Helper()
	root := t.TempDir()
	s := &reapScene{
		root:   root,
		store:  filepath.Join(root, "store"),
		skills: filepath.Join(root, "repo", ".claude", "skills"),
		leases: NewStore(filepath.Join(root, "leases")),
		now:    time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC),
		prober: &fakeProber{alive: map[int]string{}},
	}
	if err := os.MkdirAll(s.skills, 0o755); err != nil {
		t.Fatal(err)
	}
	return s
}

func (s *reapScene) reaper() *Reaper {
	return &Reaper{
		Leases: s.leases,
		Guard:  fakeGuard{Root: s.store},
		Now:    func() time.Time { return s.now },
		Prober: s.prober,
	}
}

// spawn creates a lease with one real symlink into the store.
func (s *reapScene) spawn(t *testing.T, id string, mutate func(*Lease)) *Lease {
	t.Helper()
	target := filepath.Join(s.store, "src@abc", id)
	testutil.WriteFile(t, filepath.Join(target, "SKILL.md"), "# "+id)
	path := filepath.Join(s.skills, id)
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	l := &Lease{
		ID:        id,
		Loadout:   id,
		Dir:       s.skills,
		Kind:      KindManual,
		CreatedAt: s.now,
		Links:     []Link{{Path: path, Target: target, Skill: id}},
	}
	mutate(l)
	if err := s.leases.Save(l); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestReapRevokesOnlyDeadLeases(t *testing.T) {
	past := time.Date(2026, 7, 27, 11, 0, 0, 0, time.UTC)
	future := time.Date(2026, 7, 27, 13, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		mutate     func(s *reapScene, l *Lease)
		wantReaped bool
		wantReason string
	}{
		{
			name:       "manual lease is never reaped",
			mutate:     func(_ *reapScene, l *Lease) { l.Kind = KindManual },
			wantReaped: false,
		},
		{
			name: "deadline in the future survives",
			mutate: func(_ *reapScene, l *Lease) {
				l.Kind, l.ExpiresAt = KindDeadline, &future
			},
			wantReaped: false,
		},
		{
			name: "deadline in the past is reaped",
			mutate: func(_ *reapScene, l *Lease) {
				l.Kind, l.ExpiresAt = KindDeadline, &past
			},
			wantReaped: true,
			wantReason: "deadline passed",
		},
		{
			name: "deadline exactly now is reaped",
			mutate: func(s *reapScene, l *Lease) {
				exp := s.now
				l.Kind, l.ExpiresAt = KindDeadline, &exp
			},
			wantReaped: true,
			wantReason: "deadline passed",
		},
		{
			name: "process lease with a live owner survives",
			mutate: func(s *reapScene, l *Lease) {
				s.prober.alive[500] = "tok-500"
				l.Kind = KindProcess
				l.Owner = &Owner{PID: 500, StartToken: "tok-500"}
			},
			wantReaped: false,
		},
		{
			name: "process lease whose owner exited is reaped",
			mutate: func(_ *reapScene, l *Lease) {
				l.Kind = KindProcess
				l.Owner = &Owner{PID: 501, StartToken: "tok-501"}
			},
			wantReaped: true,
			wantReason: "exited",
		},
		{
			name: "process lease whose pid was reused is reaped",
			mutate: func(s *reapScene, l *Lease) {
				s.prober.alive[502] = "a-completely-different-process"
				l.Kind = KindProcess
				l.Owner = &Owner{PID: 502, StartToken: "tok-502"}
			},
			wantReaped: true,
			wantReason: "reused",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newReapScene(t)
			l := s.spawn(t, "sk", func(l *Lease) { tt.mutate(s, l) })

			reports, problems := s.reaper().Reap()
			if len(problems) != 0 {
				t.Fatalf("unexpected problems: %v", problems)
			}

			gotReaped := len(reports) == 1
			if gotReaped != tt.wantReaped {
				t.Fatalf("reaped = %v, want %v", gotReaped, tt.wantReaped)
			}
			if tt.wantReaped {
				if !strings.Contains(reports[0].Reason, tt.wantReason) {
					t.Errorf("reason = %q, want it to mention %q", reports[0].Reason, tt.wantReason)
				}
				if testutil.Exists(l.Links[0].Path) {
					t.Error("reaped lease left its symlink behind")
				}
				if _, err := s.leases.Get(l.ID); err == nil {
					t.Error("reaped lease left its record behind")
				}
			} else {
				if !testutil.Exists(l.Links[0].Path) {
					t.Error("a live lease had its symlink removed")
				}
				if _, err := s.leases.Get(l.ID); err != nil {
					t.Errorf("a live lease lost its record: %v", err)
				}
			}
		})
	}
}

func TestReapHandlesManyLeasesIndependently(t *testing.T) {
	s := newReapScene(t)
	past := time.Date(2026, 7, 27, 11, 0, 0, 0, time.UTC)

	dead := s.spawn(t, "dead", func(l *Lease) { l.Kind, l.ExpiresAt = KindDeadline, &past })
	live := s.spawn(t, "live", func(l *Lease) { l.Kind = KindManual })

	reports, _ := s.reaper().Reap()
	if len(reports) != 1 || reports[0].Lease.ID != "dead" {
		t.Fatalf("reports = %+v, want only the expired lease", reports)
	}
	if testutil.Exists(dead.Links[0].Path) {
		t.Error("expired lease survived the reap")
	}
	if !testutil.Exists(live.Links[0].Path) {
		t.Error("reaping an expired lease disturbed a live one")
	}
}

// TestReapReportsForeignPathsRatherThanFailing covers the case where a reap
// meets a path the user has taken over: it must say so, not fail silently and
// not delete.
func TestReapReportsForeignPathsRatherThanFailing(t *testing.T) {
	s := newReapScene(t)
	past := time.Date(2026, 7, 27, 11, 0, 0, 0, time.UTC)
	l := s.spawn(t, "sk", func(l *Lease) { l.Kind, l.ExpiresAt = KindDeadline, &past })

	if err := os.Remove(l.Links[0].Path); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(l.Links[0].Path, "SKILL.md"), "now mine")

	reports, _ := s.reaper().Reap()
	if len(reports) != 1 {
		t.Fatalf("reports = %+v, want one", reports)
	}
	if !reports[0].Foreign() {
		t.Fatal("reap did not report the foreign path")
	}
	if !testutil.Exists(filepath.Join(l.Links[0].Path, "SKILL.md")) {
		t.Fatal("reap destroyed a user-created skill")
	}
	// The lease record still goes away; there is nothing left to track.
	if _, err := s.leases.Get(l.ID); err == nil {
		t.Error("lease record should be cleared even when a path was kept")
	}
}

func TestReapReportsUnreadableRecords(t *testing.T) {
	s := newReapScene(t)
	testutil.WriteFile(t, filepath.Join(s.leases.Dir, "broken.yaml"), "{{{ not yaml")

	_, problems := s.reaper().Reap()
	if len(problems) != 1 {
		t.Fatalf("problems = %v, want one unreadable record reported", problems)
	}
}

func TestReaperDefaultsToWallClock(t *testing.T) {
	s := newReapScene(t)
	r := s.reaper()
	r.Now = nil
	if r.now().IsZero() {
		t.Fatal("reaper with no clock injected should fall back to time.Now")
	}
}
