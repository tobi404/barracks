package tui

import (
	"io"
	"os"
	"os/signal"

	tea "charm.land/bubbletea/v2"
)

// terminalJob is one order run with the terminal handed back to it.
//
// It exists because a deploy fetches, and a fetch can raise a prompt barracks
// neither controls nor can forward: ssh opens /dev/tty itself for a key
// passphrase or a host-key confirmation, and a credential helper is a separate
// process free to do the same. Drawn over the alternate screen that prompt is
// invisible, and read from the same terminal the roster is draining it is
// unanswerable - a hang with no way out but killing barracks from another
// terminal. So the order runs the way Bubble Tea runs an editor: the program
// stops rendering, leaves the alternate screen, puts the terminal back as it
// found it, runs this, and restores everything afterwards - on the failure path
// as well as the successful one.
//
// While the terminal is handed back it is where the order reports, because the
// roster is not on screen to report into. Sending into the program instead
// would deadlock it: the event loop is blocked inside this Run.
type terminalJob struct {
	run func(io.Writer) Outcome

	out    io.Writer
	ran    bool
	result Outcome
}

func (j *terminalJob) SetStdin(io.Reader) {}

func (j *terminalJob) SetStdout(w io.Writer) { j.out = w }

func (j *terminalJob) SetStderr(io.Writer) {}

func (j *terminalJob) Run() error {
	_, release := holdInterrupts()
	defer release()

	j.ran = true
	w := j.out
	if w == nil {
		w = io.Discard
	}
	j.result = j.run(w)
	// An order that refused is not a handover that failed. Returning the
	// refusal here would have Bubble Tea report a broken exec; a barracks
	// refusal belongs in the outcome panel, in barracks' own words.
	return nil
}

// done turns the handover's own error into the outcome the roster shows.
func (j *terminalJob) done(err error) tea.Msg {
	out := j.result
	switch {
	case err != nil && !j.ran:
		// The terminal could not be released, so the order never ran at all.
		out = Outcome{Err: err}
	case err != nil:
		// The work is done and the terminal came back imperfectly. That is a
		// notice and never a refusal: what landed on disk landed either way,
		// and calling it a failure would send somebody looking for a spawn that
		// is standing right there.
		out.Notices = append(out.Notices, err.Error())
	}
	return doneMsg{out}
}

// holdInterrupts keeps a ^C from killing barracks while an order runs with the
// terminal handed back. It returns what it caught, which nothing but a test
// reads, and the function that puts the default behaviour back.
//
// On the alternate screen ^C arrives as a key press and the working screen
// ignores it: work already underway is not interruptible, because a half
// applied spawn is the state this tier must never be left in. Handing the
// terminal back restores the line discipline that turns ^C into a signal, so
// without this the handover would quietly become the interrupt the roster
// refuses to offer. Registering a channel is what takes the signal off its
// default disposition; nothing drains it because there is nothing to do with
// it. Children are unaffected - they receive the same ^C from the terminal - so
// cancelling ssh's passphrase prompt still fails the fetch, and the engine
// still rolls the spawn back and reports it.
func holdInterrupts() (<-chan os.Signal, func()) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	return c, func() { signal.Stop(c) }
}
