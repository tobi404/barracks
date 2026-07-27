package lease

import (
	"errors"
	"fmt"
	"time"

	"github.com/tobi404/barracks/internal/proc"
)

// Reaper revokes leases whose lifetime has ended.
//
// Reaping is lazy by design: every barracks command runs a pass before doing
// its own work. There is no daemon and no shell integration to install, so
// there is nothing for the user to forget to set up. `barracks run` revokes its
// own lease on exit, which makes the common case immediate; the reaper is the
// crash-safety net behind it.
type Reaper struct {
	Leases *Store
	Guard  StoreGuard
	Now    func() time.Time
	Prober proc.Prober
}

// Reap revokes every dead lease and returns one report per revocation.
func (r *Reaper) Reap() ([]*Report, []error) {
	leases, problems := r.Leases.List()
	var reports []*Report
	for _, l := range leases {
		dead, reason := r.dead(l)
		if !dead {
			continue
		}
		reports = append(reports, Revoke(l, r.Guard, r.Leases, reason))
	}
	return reports, problems
}

func (r *Reaper) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// dead decides whether a lease's lifetime has ended.
func (r *Reaper) dead(l *Lease) (bool, string) {
	switch l.Kind {
	case KindDeadline:
		if l.Expired(r.now()) {
			return true, "deadline passed"
		}
		return false, ""
	case KindProcess:
		alive, reason := OwnerAlive(r.Prober, l.Owner)
		if alive {
			return false, ""
		}
		return true, reason
	default:
		return false, ""
	}
}

// OwnerAlive reports whether a process lease's owner is still the process that
// took the lease out.
//
// Three ways to be dead: no owner recorded, the PID is free, or the PID is
// live but belongs to a different process than the one recorded. The last case
// is PID reuse, and it is why a bare PID is never trusted.
//
// When the prober cannot tell, the lease is treated as alive. barracks would
// rather leave a stale symlink than remove one it is not certain about.
func OwnerAlive(p proc.Prober, o *Owner) (bool, string) {
	if o == nil || o.PID <= 0 {
		return false, "no owner process recorded"
	}
	if p == nil {
		return true, ""
	}
	token, err := p.Identity(o.PID)
	if errors.Is(err, proc.ErrNotRunning) {
		return false, fmt.Sprintf("owner process %d exited", o.PID)
	}
	if err != nil {
		return true, ""
	}
	if o.StartToken != "" && token != o.StartToken {
		return false, fmt.Sprintf("pid %d was reused by a different process", o.PID)
	}
	return true, ""
}
