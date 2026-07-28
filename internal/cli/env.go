// Package cli wires the barracks commands together.
//
// Everything a command needs - streams, clock, working directory, process
// prober - arrives through Env, so the whole CLI is exercisable in-process by
// tests without touching the real home directory or the network.
package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/tobi404/barracks/internal/garrison"
	"github.com/tobi404/barracks/internal/gitcmd"
	"github.com/tobi404/barracks/internal/lease"
	"github.com/tobi404/barracks/internal/loadout"
	"github.com/tobi404/barracks/internal/paths"
	"github.com/tobi404/barracks/internal/proc"
	"github.com/tobi404/barracks/internal/spawn"
	"github.com/tobi404/barracks/internal/store"
	"github.com/tobi404/barracks/internal/voice"
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

	// Tty reports whether stdout is a terminal. It is what keeps the flavor
	// line out of pipes, redirects and CI without anyone passing a flag. A nil
	// Tty means "not a terminal", so a test sees no flavor unless it asks for
	// it deliberately.
	Tty func() bool
	// Rand picks between a step's interchangeable flavor lines. Tests set it to
	// make the choice deterministic.
	Rand func() uint64

	loadouts  *loadout.Store
	leases    *lease.Store
	store     *store.Store
	engine    *spawn.Engine
	garrisons *garrison.Engine
	speaker   *voice.Speaker
	place     string
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
		// A personal spawn must refuse to land on a path this repository has
		// committed, and the committed tier must refuse the reverse. Both
		// refusals are wired here so neither can be forgotten.
		Committed: garrison.Guard{},
	}
	e.garrisons = &garrison.Engine{
		Store:    e.store,
		Leases:   e.leases,
		Git:      e.Git,
		Now:      e.now,
		Loadouts: e.loadouts,
	}
	e.speaker = &voice.Speaker{
		Path: voice.StatePath(e.Layout.Data),
		Now:  e.now,
		Rand: e.Rand,
	}
	return nil
}

// speak prints the flavor line for a command that has just succeeded.
//
// Every reason to stay quiet is gathered here, and all of them are checked
// before a single byte is written: the line is decoration, so it must be
// impossible for it to reach anything that is reading barracks rather than
// watching it.
func (e *Env) speak(command, subject string) {
	if e.speaker == nil || !e.isTerminal() || e.voiceOffByEnv() {
		return
	}
	line := e.speaker.Line(command, subject, e.place)
	if line == "" {
		return
	}
	// stderr, always: `barracks list | grep react` must never see this, and
	// neither must anything parsing the report the command just printed.
	fmt.Fprintf(e.Err, "  %s %s\n", voice.Marker, line)
}

func (e *Env) isTerminal() bool {
	return e.Tty != nil && e.Tty()
}

// actedIn records where a repository-scoped command did its work, so the flavor
// line escalates per repository as well as per loadout.
//
// A command has to say this for itself rather than have it inferred: `spawn`,
// `recall`, `garrison` and `run` act on a repository, while `train` and `equip`
// act on the loadout wherever you happen to be standing, and `upgrade`
// re-resolves sources for every spawn on the machine. Getting that wrong makes
// the wearier lines describe a place the unit has never been.
//
// It is recorded from the location the command resolved, never from the raw
// working directory, so running from a subdirectory is the same place.
func (e *Env) actedIn(loc spawn.Location) {
	if loc.Scope == lease.ScopeGlobal || loc.Root == "" {
		// A global install is not in a repository at all. One stable name of
		// its own beats whichever directory it was launched from.
		e.place = "global"
		return
	}
	e.place = loc.Root
}

// noteScope is actedIn for the two commands that do not otherwise need the
// location. Failing to resolve it costs the escalation and nothing else.
func (e *Env) noteScope(ctx context.Context, global bool) {
	if loc, err := e.scopeOf(ctx, global); err == nil {
		e.actedIn(loc)
	}
}

// EnvQuiet turns the flavor line off permanently, for anyone who wants it gone
// rather than gone-for-this-invocation.
const EnvQuiet = "BARRACKS_QUIET"

func (e *Env) voiceOffByEnv() bool {
	if e.Getenv == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(e.Getenv(EnvQuiet))) {
	case "", "0", "false", "no":
		return false
	default:
		return true
	}
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
