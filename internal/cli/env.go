// Package cli wires the barracks commands together.
//
// Everything a command needs - streams, clock, working directory, process
// prober - arrives through Env, so the whole CLI is exercisable in-process by
// tests without touching the real home directory or the network.
package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/tobi404/barracks/internal/gitcmd"
	"github.com/tobi404/barracks/internal/lease"
	"github.com/tobi404/barracks/internal/loadout"
	"github.com/tobi404/barracks/internal/paths"
	"github.com/tobi404/barracks/internal/proc"
	"github.com/tobi404/barracks/internal/spawn"
	"github.com/tobi404/barracks/internal/store"
)

// Env is the injectable environment a barracks invocation runs in.
type Env struct {
	Out    io.Writer
	Err    io.Writer
	Cwd    string
	Layout paths.Layout
	Now    func() time.Time
	Prober proc.Prober
	Git    gitcmd.Git
	Getenv func(string) string
	Home   func() (string, error)

	loadouts *loadout.Store
	leases   *lease.Store
	store    *store.Store
	engine   *spawn.Engine
}

func (e *Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Env) init() error {
	if err := e.Layout.EnsureDirs(); err != nil {
		return err
	}
	e.loadouts = loadout.NewStore(e.Layout.LoadoutsDir())
	e.leases = lease.NewStore(e.Layout.LeasesDir())
	e.store = store.New(e.Layout.StoreDir(), e.Layout.MirrorsDir(), e.Git)
	e.engine = &spawn.Engine{
		Store:  e.store,
		Leases: e.leases,
		Git:    e.Git,
		Now:    e.now,
		Env:    e.Getenv,
		Home:   e.Home,
	}
	return nil
}

// reap runs the lazy reaper. Every command calls this before its own work: an
// expired deadline or a dead owner process is cleaned up by whichever command
// happens to run next, which is why barracks needs no daemon.
//
// A reap always reports what it removed. Skills disappearing from a repository
// is never something the user should have to discover for themselves, whichever
// command happened to trigger the pass.
func (e *Env) reap() {
	r := &lease.Reaper{
		Leases: e.leases,
		Guard:  e.store,
		Now:    e.now,
		Prober: e.Prober,
	}
	reports, problems := r.Reap()
	for _, p := range problems {
		fmt.Fprintf(e.Err, "! unreadable lease record: %v\n", p)
	}
	for _, rep := range reports {
		fmt.Fprintf(e.Out, "reaped %s from %s (%s): %d %s recalled\n",
			rep.Lease.Loadout, rep.Lease.Dir, rep.Reason, len(rep.Removed), plural(len(rep.Removed), "skill", "skills"))
		reportKept(e.Err, rep)
	}
}

// reportKept surfaces anything revocation refused to touch. Leaving a foreign
// path alone must always be visible, never silent.
func reportKept(w io.Writer, rep *lease.Report) {
	reportKeptAs(w, rep, "left in place (barracks did not create it)")
}

// reportKeptAs is reportKept with the headline spelled out, for the one caller
// that must not claim a kept path is foreign: a recall working from a lease
// record it could not re-read has kept the path because it cannot confirm it,
// which is a different statement from "someone else created this".
func reportKeptAs(w io.Writer, rep *lease.Report, headline string) {
	for _, k := range rep.Kept {
		fmt.Fprintf(w, "! %s: %s - %s\n", headline, k.Path, k.Reason)
	}
	for _, err := range rep.Errors {
		fmt.Fprintf(w, "! %v\n", err)
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
