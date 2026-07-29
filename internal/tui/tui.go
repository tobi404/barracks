// Package tui is the full-screen roster: one screen from which the loadouts on
// this machine can be read and deployed, instead of a remembered flag.
//
// It is an addition to the command surface, never a replacement for it. Nothing
// in here reaches for a path, a store or a git command of its own: every record
// it reads and every action it takes arrives through Config, which internal/cli
// fills in from the same objects the commands use. That is what keeps the two
// surfaces incapable of disagreeing, and it is what lets the whole screen be
// driven in a test with no terminal attached.
package tui

import (
	"context"
	"io"

	tea "charm.land/bubbletea/v2"

	"github.com/tobi404/barracks/internal/loadout"
)

// Session is the terminal an order owns while it runs.
//
// An order that fetches, and an order that starts an agent, both run with the
// roster's screen handed back to them - see terminalJob. These are the streams
// of the terminal they now own: what an order writes here is what the user is
// looking at, and what it reads here is what the user types. Nothing an order
// says may go into the model while it holds this, because the event loop is
// blocked waiting for it to finish.
type Session struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// TargetOption is one agent the deploy picker offers.
type TargetOption struct {
	// ID is the target's registry ID, which is what a chosen selection is
	// expressed in.
	ID string
	// Display is the agent's human name.
	Display string
	// Present is whether this repository already shows signs of that agent.
	Present bool
}

// Launcher is one agent program a run can start.
type Launcher struct {
	// Command is the program, exactly as it will be executed.
	Command string
	// Display is the agent's human name.
	Display string
}

// Config is everything the roster needs from the rest of barracks.
type Config struct {
	// Records is where the roster is read from.
	Records reader

	// Deploy spawns a loadout into the repository the TUI was launched from.
	//
	// targets is what the picker chose, and is nil when the user left it alone:
	// nil is "decide the way `barracks spawn` decides", which is not the same
	// statement as naming the same agents by hand, because it leaves the
	// loadout's declaration and the repository's own evidence in charge.
	//
	// It runs with the terminal handed back to it - see terminalJob - so it
	// reports to Session rather than drawing on the roster, and anything it
	// starts may prompt there and be answered.
	Deploy func(ctx context.Context, l *loadout.Loadout, targets []string, s Session) Outcome
	// Recall removes every spawn of a loadout in this scope. The committed tier
	// is deliberately not part of it - see the roster's recall order.
	Recall func(ctx context.Context, l *loadout.Loadout) Outcome
	// Garrison commits a loadout into this repository: real files plus
	// barracks.lock, for everyone who clones it. It fetches, so it too runs
	// with the terminal handed back.
	Garrison func(ctx context.Context, l *loadout.Loadout, s Session) Outcome
	// Upgrade re-resolves a loadout's sources and reports what carrying that
	// through would change, without changing any of it. What comes back carries
	// the plan itself, so applying it applies exactly what was shown rather
	// than a second answer to the same question.
	Upgrade func(ctx context.Context, l *loadout.Loadout, s Session) Preview
	// Launch spawns a loadout, runs an agent with those skills, and recalls it
	// the moment the agent exits. The agent owns the terminal for its whole
	// life, which is what Session is for.
	Launch func(ctx context.Context, l *loadout.Loadout, program Launcher, s Session) Outcome

	// Targets is every agent barracks can deploy to, in the order the picker
	// offers them.
	Targets []TargetOption
	// Selection is where a deploy of this loadout would go if nobody chose
	// otherwise, and why - the same answer the commands print. The picker opens
	// on it, so leaving the picker alone deploys exactly where the command
	// would have.
	//
	// It answers with an error for the same loadouts the command refuses, and
	// that error is a refusal here too. "Nowhere" and "barracks cannot read this
	// definition" are different answers, and a picker that opened empty on the
	// second would let a ticked box turn a broken definition into an explicit
	// override that quietly succeeds.
	Selection func(l *loadout.Loadout) (ids []string, reason string, err error)
	// Launchers are the agent programs a run can start on this machine.
	Launchers []Launcher

	// Version is the build line shown in the header.
	Version string

	// Input, Output and Width/Height override the terminal. Only tests set
	// them: without a terminal there is no size to ask for, and a program that
	// never learns its size never draws.
	Input         io.Reader
	Output        io.Writer
	Width, Height int
	// Dark forces the palette rather than asking the terminal. Only tests set
	// it; a real run asks and repaints when the answer arrives.
	Dark *bool
}

// Outcome is what an action left behind, in the words the screen will use.
//
// An action reports rather than returning an error, because a barracks action
// has three results and not two: it worked, it refused, or it worked and left
// something behind that the user has to be told about. Notices carry the third.
type Outcome struct {
	// Title is the headline, e.g. "frontend deployed".
	Title string
	// Lines are the body: the skills that moved, one per line.
	Lines []string
	// Notices are anything barracks declined to touch, verbatim.
	Notices []string
	// Err is set when the action refused or failed.
	Err error
}

// Preview is a plan shown before it is carried out.
//
// It is how `upgrade` reaches the roster without a second implementation of
// what an upgrade would do: the body below is the plan, and Apply carries out
// that same plan rather than re-deciding it. An Apply of nil means there is
// nothing to offer - the plan refused, or there was never anything to do - and
// the roster shows the body as an outcome instead of as an order.
type Preview struct {
	Outcome
	Apply func(ctx context.Context, s Session) Outcome
}

// Run opens the roster and blocks until the user leaves it.
func Run(ctx context.Context, cfg Config) error {
	m := newModel(cfg)

	opts := []tea.ProgramOption{tea.WithContext(ctx)}
	if cfg.Input != nil {
		opts = append(opts, tea.WithInput(cfg.Input))
	}
	if cfg.Output != nil {
		opts = append(opts, tea.WithOutput(cfg.Output))
	}
	if cfg.Width > 0 && cfg.Height > 0 {
		opts = append(opts, tea.WithWindowSize(cfg.Width, cfg.Height))
	}
	_, err := tea.NewProgram(m, opts...).Run()
	return err
}
