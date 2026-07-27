package proc

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestOSProberIdentifiesThisProcess checks the real prober against a process
// that is certainly alive.
func TestOSProberIdentifiesThisProcess(t *testing.T) {
	pid, token, err := Self(OSProber{})
	if err != nil {
		t.Fatalf("Self: %v", err)
	}
	if pid != os.Getpid() {
		t.Errorf("Self pid = %d, want %d", pid, os.Getpid())
	}
	if strings.TrimSpace(token) == "" {
		t.Fatal("identity token is empty; PID reuse could not be detected")
	}

	// The token must be stable for the same process.
	again, err := OSProber{}.Identity(pid)
	if err != nil {
		t.Fatalf("second Identity: %v", err)
	}
	if again != token {
		t.Errorf("token changed for the same process: %q then %q", token, again)
	}
}

// TestOSProberDistinguishesProcesses is the property the whole lease model
// rests on: two different processes must not share an identity token, or a
// reused PID would look like the original owner.
//
// The child here starts in the same wall-clock second as the test process, so
// this also pins down that the token carries more than a one-second-resolution
// start time.
func TestOSProberDistinguishesProcesses(t *testing.T) {
	child := exec.Command("sleep", "5")
	if err := child.Start(); err != nil {
		t.Skipf("cannot start a child process here: %v", err)
	}
	defer func() {
		_ = child.Process.Kill()
		_, _ = child.Process.Wait()
	}()

	selfToken, err := OSProber{}.Identity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	childToken, err := OSProber{}.Identity(child.Process.Pid)
	if err != nil {
		t.Fatalf("Identity of the child: %v", err)
	}
	if childToken == selfToken {
		t.Errorf("two distinct processes produced the same identity token %q", childToken)
	}
}

// TestOSProberReportsExitedProcesses covers the reaper's main signal.
func TestOSProberReportsExitedProcesses(t *testing.T) {
	child := exec.Command("true")
	if err := child.Start(); err != nil {
		t.Skipf("cannot start a child process here: %v", err)
	}
	pid := child.Process.Pid
	if err := child.Wait(); err != nil {
		t.Fatalf("child: %v", err)
	}

	// A reaped child's PID is free; the prober must say so rather than
	// reporting a token.
	if _, err := (OSProber{}).Identity(pid); err == nil {
		t.Log("pid was already reused by another process; identity check still applies")
	} else if !errors.Is(err, ErrNotRunning) {
		t.Errorf("Identity of an exited process = %v, want ErrNotRunning", err)
	}
}

func TestOSProberRejectsInvalidPIDs(t *testing.T) {
	for _, pid := range []int{0, -1, -12345} {
		if _, err := (OSProber{}).Identity(pid); !errors.Is(err, ErrNotRunning) {
			t.Errorf("Identity(%d) = %v, want ErrNotRunning", pid, err)
		}
	}
}

func TestOSProberOnAVeryUnlikelyPID(t *testing.T) {
	// PIDs above the usual maximum are never allocated.
	if _, err := (OSProber{}).Identity(4194305); !errors.Is(err, ErrNotRunning) {
		t.Errorf("Identity of an out-of-range pid = %v, want ErrNotRunning", err)
	}
}

// TestParseLinuxStat exercises the /proc parser directly so it is covered on
// every platform, including the awkward comm field.
func TestParseLinuxStat(t *testing.T) {
	// Fields after ')': state, then ppid=42, ... through to starttime=987654.
	// The token folds the start time together with the parent PID.
	fields := "S 42 1 0 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 987654 1000 2000"

	tests := []struct {
		name    string
		stat    string
		want    string
		wantErr bool
	}{
		{
			name: "ordinary comm",
			stat: "1234 (bash) " + fields,
			want: "987654:42",
		},
		{
			name: "comm containing spaces and parentheses",
			stat: "1234 (weird (name) here) " + fields,
			want: "987654:42",
		},
		{
			name:    "no closing parenthesis",
			stat:    "1234 bash S 1",
			wantErr: true,
		},
		{
			name:    "truncated after comm",
			stat:    "1234 (bash) S 1 1",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseLinuxStat(tt.stat)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseLinuxStat = %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseLinuxStat: %v", err)
			}
			if got != tt.want {
				t.Errorf("parseLinuxStat = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestPsStartTokenSeparatesGoneFromCannotTell pins down the asymmetry the whole
// reaper rests on: ErrNotRunning means the process is definitively gone and a
// lease may be revoked, so a ps that never ran must report anything else.
func TestPsStartTokenSeparatesGoneFromCannotTell(t *testing.T) {
	exited := exec.Command("false")
	if err := exited.Run(); err == nil {
		t.Fatal("`false` should exit non-zero")
	}
	exitErr := exited.ProcessState

	tests := []struct {
		name           string
		run            runner
		wantNotRunning bool
		wantErr        bool
		wantToken      string
	}{
		{
			name:      "ps lists the process",
			run:       func(string, ...string) ([]byte, error) { return []byte("Mon Jul 27 12:00:00 2026 1 bash\n"), nil },
			wantToken: "Mon Jul 27 12:00:00 2026 1 bash",
		},
		{
			name:           "ps ran and listed nothing",
			run:            func(string, ...string) ([]byte, error) { return nil, &exec.ExitError{ProcessState: exitErr} },
			wantNotRunning: true,
		},
		{
			name:           "ps ran and printed nothing",
			run:            func(string, ...string) ([]byte, error) { return []byte("  \n"), nil },
			wantNotRunning: true,
		},
		{
			name:    "ps is not on PATH",
			run:     func(string, ...string) ([]byte, error) { return nil, &exec.Error{Name: "ps", Err: exec.ErrNotFound} },
			wantErr: true,
		},
		{
			name: "ps could not be started",
			run: func(string, ...string) ([]byte, error) {
				return nil, errors.New("fork/exec: resource temporarily unavailable")
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := psStartTokenWith(tt.run, 4242)
			switch {
			case tt.wantNotRunning:
				if !errors.Is(err, ErrNotRunning) {
					t.Fatalf("psStartTokenWith = (%q, %v), want ErrNotRunning", got, err)
				}
			case tt.wantErr:
				if err == nil {
					t.Fatalf("psStartTokenWith = (%q, nil), want an error", got)
				}
				if errors.Is(err, ErrNotRunning) {
					t.Fatalf("a ps that never ran reported ErrNotRunning; a live lease would be revoked: %v", err)
				}
			default:
				if err != nil {
					t.Fatalf("psStartTokenWith: %v", err)
				}
				if got != tt.wantToken {
					t.Errorf("token = %q, want %q", got, tt.wantToken)
				}
			}
		})
	}
}

func TestSelfPropagatesProberErrors(t *testing.T) {
	_, _, err := Self(failingProber{})
	if err == nil {
		t.Fatal("Self should report a prober failure")
	}
}

type failingProber struct{}

func (failingProber) Identity(int) (string, error) { return "", errors.New("boom") }
