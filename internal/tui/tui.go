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

// Config is everything the roster needs from the rest of barracks.
type Config struct {
	// Records is where the roster is read from.
	Records reader
	// Deploy spawns a loadout into the repository the TUI was launched from.
	// Progress lines the operation reports are handed to report as they happen.
	//
	// It runs with the terminal handed back to it - see terminalJob - so report
	// is written to the terminal rather than drawn on the roster, and anything
	// it starts may prompt there and be answered.
	Deploy func(ctx context.Context, l *loadout.Loadout, report func(string)) Outcome
	// Recall removes every spawn of a loadout in this scope.
	Recall func(ctx context.Context, l *loadout.Loadout) Outcome
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
