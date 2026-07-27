// Package proc answers one question safely: is the process that owns this
// lease still the process that took it out?
//
// A bare PID is not an answer. PIDs are reused, and a reaper that trusts a bare
// PID will either leave dead leases forever or, worse, keep a lease alive
// because some unrelated program inherited the number. Every identity here
// pairs the PID with a start token that distinguishes that exact process.
package proc

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// ErrNotRunning means no process currently holds that PID.
var ErrNotRunning = errors.New("process not running")

// Prober reports an identity token for a live PID.
//
// The token is not a timestamp with any particular precision; it is an opaque
// string that is stable for one process and differs between processes. Callers
// compare it for equality and nothing else.
//
// Implementations must return ErrNotRunning when the PID is free, and a
// non-nil, non-ErrNotRunning error when they cannot tell. Callers treat
// "cannot tell" as alive, because deleting on uncertainty is the one failure
// mode barracks refuses to have.
type Prober interface {
	Identity(pid int) (string, error)
}

// OSProber reads process start times from the running system.
type OSProber struct{}

// Identity returns a token that changes when the PID is reused.
func (OSProber) Identity(pid int) (string, error) {
	if pid <= 0 {
		return "", ErrNotRunning
	}
	if !exists(pid) {
		return "", ErrNotRunning
	}
	switch runtime.GOOS {
	case "linux":
		return linuxStartToken(pid)
	default:
		return psStartToken(pid)
	}
}

func exists(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	// EPERM means the process exists but belongs to someone else.
	return errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EPERM)
}

// linuxStartToken builds a token from /proc/<pid>/stat: the process start time
// in clock ticks since boot, plus the parent PID.
//
// The comm field can contain spaces and parentheses, so parsing starts after
// the last ')'.
func linuxStartToken(pid int) (string, error) {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNotRunning
		}
		return "", err
	}
	return parseLinuxStat(string(b))
}

func parseLinuxStat(stat string) (string, error) {
	i := strings.LastIndex(stat, ")")
	if i < 0 {
		return "", fmt.Errorf("unparseable /proc stat line")
	}
	fields := strings.Fields(stat[i+1:])
	// After ')' the fields are state(3), ppid(4), ... starttime(22), so the
	// zero-based indices here are 1 and 19.
	const (
		ppidIdx      = 1
		startTimeIdx = 19
	)
	if len(fields) <= startTimeIdx {
		return "", fmt.Errorf("unparseable /proc stat line")
	}
	return fields[startTimeIdx] + ":" + fields[ppidIdx], nil
}

// psStartToken builds a token from ps, for darwin and any other unix without
// /proc.
//
// It deliberately reads three fields rather than the start time alone. `ps`
// reports lstart with one-second resolution, which on its own cannot tell apart
// two processes that started in the same second. Folding in the parent PID and
// the command name means a recycled PID has to match all three to be mistaken
// for the original - and a PID is only recycled after the whole PID space has
// wrapped, by which time the parent and command have almost certainly changed.
func psStartToken(pid int) (string, error) {
	out, err := exec.Command("ps", "-o", "lstart=,ppid=,comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		// ps exits non-zero when the pid is gone.
		return "", ErrNotRunning
	}
	token := strings.Join(strings.Fields(string(out)), " ")
	if token == "" {
		return "", ErrNotRunning
	}
	return token, nil
}

// Self returns the identity of the current process.
func Self(p Prober) (int, string, error) {
	pid := os.Getpid()
	tok, err := p.Identity(pid)
	if err != nil {
		return pid, "", err
	}
	return pid, tok, nil
}
