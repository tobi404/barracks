// Package progress tells the user that a slow operation is still working.
//
// The display is one line at a time. Completed steps print permanently and
// scroll away with everything else; only the step that is still running is
// animated, and it is animated by rewriting its own line and nothing else. That
// shape is what makes it survive a window resize, keep full scrollback, and
// degrade to the same plain lines with no spinner when there is no terminal to
// animate on.
//
// The wording is plain - "resolving", "fetching", "unpacking". The unit voice in
// internal/voice carries the personality; this is the one place a user is trying
// to work out why something is slow, and clarity beats character.
//
// Three rules govern everything here, and none of them may be traded away:
//
//   - Escape sequences are only ever written to a terminal. A reporter with Live
//     false emits nothing but plain text and newlines, because an escape code in
//     a CI log or a redirected file is corruption.
//   - The terminal is always restored. Hiding the cursor is paired with showing
//     it again on every exit path: success, failure, panic, and SIGINT/SIGTERM.
//   - Nothing repaints over a line another process may be writing to. A step
//     whose child process can reach the terminal - ssh opening /dev/tty for a
//     passphrase or a host-key confirmation, or a credential helper prompting
//     for its own - runs in the same append-only mode as a redirected stream, so
//     the prompt stays readable and the user's typing works.
package progress

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The thresholds. They are named here rather than spelled into the code so that
// the one place to reconsider them is the one place they are written.
const (
	// RevealAfter is how long work must run before it is announced at all.
	//
	// Below roughly a third of a second a wait does not register as one, and a
	// cached fetch takes less than that - so a warm run stays completely clean
	// instead of flashing a line the user cannot read anyway.
	RevealAfter = 400 * time.Millisecond

	// ElapsedAfter is how long before the elapsed counter joins the line. A
	// counter that starts at "0s" is decoration; by two seconds the user has
	// started wondering how long this has been going, which is when a number
	// answers a question rather than filling space.
	ElapsedAfter = 2 * time.Second

	// LongWaitAfter is how long before the hint naming the likely causes is
	// printed. It has to be long enough that a healthy fetch never trips it: a
	// cold clone of an ordinary skills repository is seconds, and a large one on
	// a slow link can legitimately take ten or more. Twenty seconds is past all
	// of that and is about where "is this broken?" starts.
	LongWaitAfter = 20 * time.Second

	// FrameEvery is the repaint period. Ten frames a second reads as motion
	// without making the line hard to read.
	FrameEvery = 100 * time.Millisecond
)

// LongWait is the hint. It is the most useful thing this package prints: it
// turns "is this broken?" into a thought the user can act on.
const LongWait = "(large repository, slow network, or git waiting on credentials)"

// DoneMarker opens the permanent line a completed step leaves behind.
const DoneMarker = "✓"

// frames is the spinner. Braille cells are one column wide in every terminal
// that renders them, so the line never changes width as it turns.
var frames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// The only escape sequences this package knows. clearLine returns to column
// zero and erases the line; nothing here ever moves the cursor to another line.
const (
	clearLine  = "\r\x1b[2K"
	hideCursor = "\x1b[?25l"
	showCursor = "\x1b[?25h"
)

const (
	// subjectWidth pads the subject so the phase and the summary line up
	// between rows. A longer subject overflows rather than being cut: which
	// repository is being fetched is the whole point of the line.
	subjectWidth = 26
	// maxWidth caps an animated line. A line that wraps cannot be repainted -
	// clearLine only erases the row the cursor is on - so the last row would
	// repaint while the rows above it turned into litter. Reading the terminal
	// width would need an ioctl and a dependency this module does not have, so
	// the line is kept short enough to fit an 80-column terminal instead.
	maxWidth = 72
)

// Reporter is where slow work announces itself. A nil *Reporter is silent, which
// is what lets a caller hold one unconditionally.
type Reporter struct {
	// W is where the display goes. It must be stderr: stdout carries data.
	W io.Writer

	// Live turns on in-place animation. It must be false whenever W is not a
	// terminal - that is the whole of rule one, and it is decided by the caller
	// because only the caller knows what W is attached to.
	Live bool

	// Reveal, Elapsed and LongWait override the thresholds above. Zero means
	// the default. Tests pin them; nothing else should set them.
	Reveal   time.Duration
	Elapsed  time.Duration
	LongWait time.Duration
}

// Animates reports whether this reporter would animate a step at all: it has
// somewhere to write, and that somewhere is a terminal.
//
// It is what lets a caller skip work only an animated display needs the answer
// to - deciding Work.SharesTerminal can cost a subprocess, and a redirected run
// has nothing to paint over either way. A nil *Reporter never animates.
func (r *Reporter) Animates() bool { return r != nil && r.W != nil && r.Live }

// Work describes one unit of slow work.
type Work struct {
	// Subject is what is being worked on - a repository, not a file.
	Subject string
	// Phase is the plain present participle of what is happening to it:
	// "resolving", "fetching", "unpacking".
	Phase string
	// SharesTerminal says a child process of this work may write to the user's
	// terminal or read from it: ssh opening /dev/tty for a passphrase or a
	// host-key confirmation, or an interactive credential helper doing the
	// same. Either bypasses every stream barracks captured and lands on the
	// exact line the spinner is repainting. A step that says so is never
	// animated, so the prompt survives and the answer reaches the program
	// waiting for it.
	SharesTerminal bool
}

// Step is one announced unit of work. A nil *Step is a working no-op, so a
// caller never needs a nil check around reporting.
type Step struct {
	r     *Reporter
	work  Work
	start time.Time
	stop  chan struct{}
	wg    sync.WaitGroup

	mu     sync.Mutex
	phase  string
	live   bool // animate in place, rather than append plain lines
	shown  bool // something has been printed for this step
	hinted bool
	ended  bool
	frame  int
	sigs   chan os.Signal
}

// Step announces work and starts watching it. The returned Step must be ended,
// with Done or Fail; a deferred Fail is the safe way to guarantee it, since it
// does nothing once Done has been called.
func (r *Reporter) Step(work Work) *Step {
	if r == nil || r.W == nil {
		return nil
	}
	s := &Step{
		r:     r,
		work:  work,
		start: time.Now(),
		stop:  make(chan struct{}),
		phase: work.Phase,
		// A step whose child can reach the terminal is never animated. See
		// Work.SharesTerminal.
		live: r.Live && !work.SharesTerminal,
	}
	s.wg.Add(1)
	go s.watch()
	return s
}

// Phase renames what the step is doing. In live mode the next frame picks it
// up; in plain mode it is a new appended line, because nothing else would ever
// report that a long fetch has moved on to unpacking.
func (s *Step) Phase(phase string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended || phase == "" || phase == s.phase {
		return
	}
	s.phase = phase
	if s.shown && !s.live {
		s.emit(s.plainLine())
	}
}

// Done ends the step successfully. summary is the right-hand column of the
// permanent line it leaves behind - the commit it fetched, say. Nothing is
// printed at all if the step finished before it was ever announced, which is
// what keeps a fast operation looking instant.
func (s *Step) Done(summary string) { s.end(summary, true) }

// Fail ends the step without a permanent line.
//
// The command is about to print the error itself, and this package must never
// end up as a second, differently-worded report of the same failure. All Fail
// owes the user is the terminal back.
func (s *Step) Fail() { s.end("", false) }

func (s *Step) end(summary string, ok bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	if s.shown {
		if s.live {
			// Erase the animation and give the cursor back in one write, so no
			// exit path can perform half of it.
			s.emit(clearLine + showCursor)
		}
		if ok {
			s.emit(s.doneLine(summary))
		}
	}
	s.mu.Unlock()

	close(s.stop)
	s.wg.Wait()
	// After the watchers are done nothing else touches sigs.
	if s.sigs != nil {
		signal.Stop(s.sigs)
	}
}

// watch drives the display. It is the only goroutine that paints.
//
// The reveal is its own timer rather than something a repaint tick happens to
// notice, so an operation that runs just past the threshold is announced right
// then instead of up to a frame later.
func (s *Step) watch() {
	defer s.wg.Done()
	reveal := time.NewTimer(s.r.reveal())
	defer reveal.Stop()
	select {
	case <-s.stop:
		return
	case <-reveal.C:
	}

	t := time.NewTicker(s.r.interval())
	defer t.Stop()
	for {
		s.tick()
		select {
		case <-s.stop:
			return
		case <-t.C:
		}
	}
}

func (s *Step) tick() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	elapsed := s.elapsed()
	if !s.shown {
		s.shown = true
		if s.live {
			// Taking the cursor and arming the restore happen together: there
			// must be no window in which the cursor is hidden and a signal
			// would not put it back.
			s.arm()
			s.emit(hideCursor)
		} else {
			s.emit(s.plainLine())
		}
	}
	if !s.hinted && elapsed >= s.r.longWait() {
		s.hinted = true
		if s.live {
			s.emit(clearLine)
		}
		// The hint is permanent and the animation resumes below it, so the
		// spinner still owns exactly one line and nothing has to move up.
		s.emit("  " + LongWait + "\n")
	}
	if s.live {
		s.emit(clearLine + s.frameLine(elapsed))
	}
}

// arm makes Ctrl-C and SIGTERM restore the terminal before they kill barracks.
//
// A process that exits with the cursor hidden leaves the user typing blind until
// they run `reset`, so the handler is installed at the same moment the cursor is
// taken and removed when the step ends. It restores, then puts the signal's
// default behaviour back and re-raises it, so barracks dies exactly as it would
// have without a handler: same exit status, same semantics.
func (s *Step) arm() {
	s.sigs = make(chan os.Signal, 1)
	signal.Notify(s.sigs, os.Interrupt, syscall.SIGTERM)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		select {
		case sig := <-s.sigs:
			s.interrupted(sig)
		case <-s.stop:
		}
	}()
}

func (s *Step) interrupted(sig os.Signal) {
	s.mu.Lock()
	if s.shown && s.live && !s.ended {
		s.emit(clearLine + showCursor)
	}
	// Nothing may paint again: the next frame would re-hide the cursor in the
	// window between here and the process actually dying.
	s.ended = true
	s.mu.Unlock()

	signal.Stop(s.sigs)
	signal.Reset(sig)
	if p, err := os.FindProcess(os.Getpid()); err == nil {
		_ = p.Signal(sig)
	}
}

// emit writes one piece of the display. Errors are dropped: a terminal that
// cannot be written to is not a reason for a command that is doing real work to
// fail.
func (s *Step) emit(text string) {
	_, _ = io.WriteString(s.r.W, text)
}

func (s *Step) elapsed() time.Duration { return time.Since(s.start) }

// frameLine is the animated line: spinner, subject, phase, and - once the wait
// is worth counting - how long it has been.
func (s *Step) frameLine(elapsed time.Duration) string {
	frame := frames[s.frame%len(frames)]
	s.frame++
	line := fmt.Sprintf("%s %s  %s…", frame, pad(s.work.Subject), s.phase)
	if elapsed >= s.r.elapsedAfter() {
		line += " " + humanize(elapsed)
	}
	return clip(line)
}

// plainLine is what an unanimated step announces itself with. It carries no
// escape sequence of any kind, which is what makes it safe in a log file, in a
// pipe, and on a line an ssh prompt may be about to land on.
func (s *Step) plainLine() string {
	return trim(fmt.Sprintf("  %s  %s…", pad(s.work.Subject), s.phase)) + "\n"
}

func (s *Step) doneLine(summary string) string {
	line := fmt.Sprintf("%s %s  %s", DoneMarker, pad(s.work.Subject), summary)
	if elapsed := s.elapsed(); elapsed >= s.r.elapsedAfter() {
		line += "  " + humanize(elapsed)
	}
	return trim(line) + "\n"
}

// interval is the repaint period. It never outruns the reveal, so a reporter
// tuned to announce work almost immediately still updates the line and reaches
// the long-wait hint at the pace it was asked for.
func (r *Reporter) interval() time.Duration {
	if d := r.reveal(); d < FrameEvery {
		return d
	}
	return FrameEvery
}

func (r *Reporter) reveal() time.Duration {
	if r.Reveal > 0 {
		return r.Reveal
	}
	return RevealAfter
}

func (r *Reporter) elapsedAfter() time.Duration {
	if r.Elapsed > 0 {
		return r.Elapsed
	}
	return ElapsedAfter
}

func (r *Reporter) longWait() time.Duration {
	if r.LongWait > 0 {
		return r.LongWait
	}
	return LongWaitAfter
}

func pad(subject string) string {
	if n := len([]rune(subject)); n < subjectWidth {
		return subject + strings.Repeat(" ", subjectWidth-n)
	}
	return subject
}

func trim(line string) string { return strings.TrimRight(line, " ") }

// clip keeps an animated line on one row. See maxWidth.
func clip(line string) string {
	r := []rune(line)
	if len(r) <= maxWidth {
		return line
	}
	return string(r[:maxWidth-1]) + "…"
}

// humanize renders a wait the way somebody watching it would say it.
func humanize(d time.Duration) string {
	secs := int(d.Round(time.Second) / time.Second)
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	return fmt.Sprintf("%dm%02ds", secs/60, secs%60)
}
