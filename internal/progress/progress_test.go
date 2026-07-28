package progress

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The thresholds are wound right down here so a test costs milliseconds rather
// than the tens of seconds the real ones describe. Everything else - the
// escape sequences, the ordering, the restore - is exactly what ships.
const (
	testReveal   = 10 * time.Millisecond
	testLongWait = 80 * time.Millisecond
)

// buf is a writer the painting goroutine and the test can both touch.
type buf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *buf) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *buf) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

// await polls until the stream satisfies want, so a test never has to guess how
// many frames it takes for something to appear.
func await(t *testing.T, w *buf, what string, want func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := w.String()
		if want(got) {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; stream was:\n%q", what, got)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func contains(sub string) func(string) bool {
	return func(s string) bool { return strings.Contains(s, sub) }
}

func liveReporter(w *buf) *Reporter {
	return &Reporter{W: w, Live: true, Reveal: testReveal, Elapsed: time.Hour, LongWait: testLongWait}
}

func plainReporter(w *buf) *Reporter {
	return &Reporter{W: w, Reveal: testReveal, Elapsed: time.Hour, LongWait: testLongWait}
}

// TestFastWorkSaysNothing is the rule that keeps the feature out of the way: a
// cached fetch is instant, so it must look instant. Anything that flashed for a
// frame would be worse than silence.
func TestFastWorkSaysNothing(t *testing.T) {
	w := &buf{}
	r := &Reporter{W: w, Live: true, Reveal: time.Hour}
	s := r.Step(Work{Subject: "github.com/acme/skills", Phase: "fetching"})
	s.Phase("unpacking")
	s.Done("fetched a3f91c2")

	if got := w.String(); got != "" {
		t.Errorf("a fast step printed %q, want nothing at all", got)
	}
}

// TestSlowWorkSpinsThenLeavesALine is acceptance criterion 1.
func TestSlowWorkSpinsThenLeavesALine(t *testing.T) {
	w := &buf{}
	s := liveReporter(w).Step(Work{Subject: "github.com/big/monorepo", Phase: "fetching"})

	spinning := await(t, w, "the spinner", contains("fetching…"))
	if !strings.Contains(spinning, hideCursor) {
		t.Errorf("the cursor was never hidden:\n%q", spinning)
	}
	if !strings.Contains(spinning, "github.com/big/monorepo") {
		t.Errorf("the subject is missing:\n%q", spinning)
	}
	if !strings.ContainsAny(spinning, strings.Join(frames, "")) {
		t.Errorf("no spinner frame was drawn:\n%q", spinning)
	}

	// The phase is live: a fetch that moves on to unpacking says so.
	s.Phase("unpacking")
	await(t, w, "the new phase", contains("unpacking…"))

	s.Done("fetched 7b2e4d1")
	got := w.String()
	if !strings.Contains(got, DoneMarker+" github.com/big/monorepo") {
		t.Errorf("no permanent line for the completed step:\n%q", got)
	}
	if !strings.Contains(got, "fetched 7b2e4d1") {
		t.Errorf("the summary is missing:\n%q", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("the permanent line was not terminated:\n%q", got)
	}
	assertCursorRestored(t, got)
}

// TestEveryLineIsRepaintedInPlace is the shape of the display: past lines are
// permanent and only the current one is rewritten, so scrollback survives and a
// window resize cannot corrupt anything.
func TestEveryLineIsRepaintedInPlace(t *testing.T) {
	w := &buf{}
	r := liveReporter(w)

	first := r.Step(Work{Subject: "github.com/acme/skills", Phase: "fetching"})
	await(t, w, "the first spinner", contains("fetching…"))
	first.Done("fetched a3f91c2")

	second := r.Step(Work{Subject: "github.com/big/monorepo", Phase: "fetching"})
	await(t, w, "the second spinner", contains("github.com/big/monorepo"))
	second.Done("fetched 7b2e4d1")

	got := w.String()
	if n := strings.Count(got, DoneMarker); n != 2 {
		t.Errorf("want one permanent line per completed step, got %d:\n%q", n, got)
	}
	// Nothing here may address a line other than the one the cursor is on.
	for _, forbidden := range []string{"\x1b[A", "\x1b[B", "\x1b[s", "\x1b[u", "\x1b[J"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("the display moved the cursor off its own line (%q):\n%q", forbidden, got)
		}
	}
}

// TestPlainModeEmitsNoEscapeSequences is acceptance criterion 2 at the level
// this package owns it. An escape code in a log file or a redirected stream is
// corruption, so a reporter that is not Live must be incapable of writing one.
func TestPlainModeEmitsNoEscapeSequences(t *testing.T) {
	w := &buf{}
	s := plainReporter(w).Step(Work{Subject: "github.com/big/monorepo", Phase: "fetching"})
	await(t, w, "the plain announcement", contains("fetching…"))
	s.Phase("unpacking")
	await(t, w, "the plain phase change", contains("unpacking…"))
	await(t, w, "the long-wait hint", contains(LongWait))
	s.Done("fetched 7b2e4d1")

	got := w.String()
	if strings.Contains(got, "\x1b") || strings.Contains(got, "\r") {
		t.Errorf("plain mode wrote an escape sequence:\n%q", got)
	}
	if !strings.Contains(got, DoneMarker+" github.com/big/monorepo") {
		t.Errorf("the completed line is missing:\n%q", got)
	}
	for _, line := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		if line != strings.TrimRight(line, " ") {
			t.Errorf("trailing whitespace in %q", line)
		}
	}
}

// TestWorkThatSharesTheTerminalIsNeverAnimated is the rule that keeps a git
// credential prompt readable. ssh opens /dev/tty for a passphrase, which lands
// on whatever line the cursor is on - so a step that can spawn it must never own
// a line it repaints, however good the terminal is.
func TestWorkThatSharesTheTerminalIsNeverAnimated(t *testing.T) {
	w := &buf{}
	s := liveReporter(w).Step(Work{
		Subject:        "github.com/acme/private",
		Phase:          "fetching",
		SharesTerminal: true,
	})
	await(t, w, "the plain announcement", contains("fetching…"))
	await(t, w, "the long-wait hint", contains(LongWait))
	s.Done("fetched 7b2e4d1")

	if got := w.String(); strings.Contains(got, "\x1b") || strings.Contains(got, "\r") {
		t.Errorf("a step sharing the terminal repainted over it:\n%q", got)
	}
}

// TestTheHintWaitsForAGenuinelyLongWait is acceptance criterion 7.
func TestTheHintWaitsForAGenuinelyLongWait(t *testing.T) {
	w := &buf{}
	r := &Reporter{W: w, Live: true, Reveal: testReveal, Elapsed: time.Hour, LongWait: time.Hour}
	s := r.Step(Work{Subject: "github.com/acme/skills", Phase: "fetching"})
	await(t, w, "the spinner", contains("fetching…"))
	s.Done("fetched a3f91c2")
	if strings.Contains(w.String(), LongWait) {
		t.Errorf("the hint fired on a normal wait:\n%q", w.String())
	}

	// And once it is due it appears exactly once, as a permanent line, with the
	// spinner carrying on below it.
	w2 := &buf{}
	s2 := liveReporter(w2).Step(Work{Subject: "github.com/big/monorepo", Phase: "fetching"})
	await(t, w2, "the long-wait hint", contains(LongWait))
	time.Sleep(4 * testLongWait)
	s2.Done("fetched 7b2e4d1")

	got := w2.String()
	if n := strings.Count(got, LongWait); n != 1 {
		t.Errorf("the hint appeared %d times, want exactly one:\n%q", n, got)
	}
	if i, j := strings.Index(got, LongWait), strings.LastIndex(got, "fetching…"); i > j {
		t.Errorf("the hint was not left above the spinner:\n%q", got)
	}
}

// TestTheElapsedCounterWaitsAFewSeconds: a counter that starts at 0s is
// decoration, so it joins the line only once the wait is worth a number.
func TestTheElapsedCounterWaitsAFewSeconds(t *testing.T) {
	w := &buf{}
	r := &Reporter{W: w, Live: true, Reveal: testReveal, Elapsed: time.Hour, LongWait: time.Hour}
	s := r.Step(Work{Subject: "github.com/acme/skills", Phase: "fetching"})
	await(t, w, "the spinner", contains("fetching…"))
	s.Done("fetched a3f91c2")
	if strings.Contains(w.String(), "0s") {
		t.Errorf("the counter appeared straight away:\n%q", w.String())
	}

	w2 := &buf{}
	r2 := &Reporter{W: w2, Live: true, Reveal: testReveal, Elapsed: testReveal, LongWait: time.Hour}
	s2 := r2.Step(Work{Subject: "github.com/acme/skills", Phase: "fetching"})
	await(t, w2, "the elapsed counter", contains("fetching… 0s"))
	s2.Done("fetched a3f91c2")
	if !strings.Contains(w2.String(), DoneMarker) {
		t.Errorf("the completed line is missing:\n%q", w2.String())
	}
}

// TestFailureLeavesNoLineButRestoresTheTerminal: the command is about to print
// the error itself, and a second, differently-worded report of one failure is
// worse than none. All a failure owes the user is the cursor back.
func TestFailureLeavesNoLineButRestoresTheTerminal(t *testing.T) {
	w := &buf{}
	s := liveReporter(w).Step(Work{Subject: "github.com/acme/skills", Phase: "fetching"})
	await(t, w, "the spinner", contains("fetching…"))
	s.Fail()

	got := w.String()
	if strings.Contains(got, DoneMarker) {
		t.Errorf("a failure left a completed line:\n%q", got)
	}
	assertCursorRestored(t, got)
	if !strings.HasSuffix(got, clearLine+showCursor) {
		t.Errorf("a failure left its half-drawn line behind:\n%q", got)
	}
}

// TestEndingTwiceIsSafe: every caller ends a step with a deferred Fail as well
// as an explicit Done, so the second one has to be a no-op rather than a second
// line or a closed-channel panic.
func TestEndingTwiceIsSafe(t *testing.T) {
	w := &buf{}
	s := liveReporter(w).Step(Work{Subject: "github.com/acme/skills", Phase: "fetching"})
	await(t, w, "the spinner", contains("fetching…"))
	s.Done("fetched a3f91c2")
	s.Fail()
	s.Done("again")
	s.Phase("still going")

	got := w.String()
	if n := strings.Count(got, DoneMarker); n != 1 {
		t.Errorf("want one permanent line, got %d:\n%q", n, got)
	}
	if strings.Contains(got, "still going") {
		t.Errorf("an ended step kept painting:\n%q", got)
	}
	assertCursorRestored(t, got)
}

// TestNilIsSilent: a caller holds a reporter unconditionally, so every entry
// point has to work on a nil one.
func TestNilIsSilent(t *testing.T) {
	var r *Reporter
	s := r.Step(Work{Subject: "github.com/acme/skills", Phase: "fetching"})
	if s != nil {
		t.Fatalf("a nil reporter handed out a step")
	}
	s.Phase("unpacking")
	s.Done("fetched a3f91c2")
	s.Fail()

	// So does a reporter with nowhere to write.
	if got := (&Reporter{}).Step(Work{Subject: "x"}); got != nil {
		t.Errorf("a reporter with no writer handed out a step")
	}
}

// TestLongSubjectsAndLinesStayOnOneRow: a line that wraps cannot be repainted -
// only the last row would be erased, and the rows above it would turn into
// litter that survives to the end of the command.
func TestLongSubjectsAndLinesStayOnOneRow(t *testing.T) {
	w := &buf{}
	long := "github.com/an-organisation-with-a-long-name/a-repository-with-a-long-name"
	s := liveReporter(w).Step(Work{Subject: long, Phase: "fetching"})
	await(t, w, "the spinner", contains("⠋"))
	s.Fail()

	for _, chunk := range strings.Split(w.String(), clearLine) {
		body := strings.TrimSuffix(strings.TrimPrefix(chunk, hideCursor), showCursor)
		if n := len([]rune(body)); n > maxWidth {
			t.Errorf("an animated line was %d columns wide, want at most %d:\n%q", n, maxWidth, body)
		}
	}
}

func TestHumanize(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{900 * time.Millisecond, "1s"},
		{8 * time.Second, "8s"},
		{59 * time.Second, "59s"},
		{72 * time.Second, "1m12s"},
		{135 * time.Second, "2m15s"},
	} {
		if got := humanize(tc.in); got != tc.want {
			t.Errorf("humanize(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// assertCursorRestored is the rule that hurts users most when it is broken: a
// process that exits with the cursor hidden leaves them typing blind.
func assertCursorRestored(t *testing.T, stream string) {
	t.Helper()
	hidden := strings.Count(stream, hideCursor)
	shown := strings.Count(stream, showCursor)
	if hidden != shown {
		t.Errorf("hid the cursor %d times and showed it %d:\n%q", hidden, shown, stream)
	}
	if hidden > 0 && !strings.Contains(stream[strings.LastIndex(stream, hideCursor):], showCursor) {
		t.Errorf("the stream ends with the cursor hidden:\n%q", stream)
	}
}

// --- the signal path, driven through a real process -------------------------

// TestASignalRestoresTheTerminalAndStillKills is acceptance criterion 3, and it
// needs a real process: the handler restores the terminal, puts the signal's
// default behaviour back and re-raises it, so barracks dies exactly as it would
// have with no handler at all. Asserting that in-process would kill the test
// binary, so a child does the dying.
func TestASignalRestoresTheTerminalAndStillKills(t *testing.T) {
	for _, tc := range []struct {
		name string
		sig  syscall.Signal
	}{
		{"interrupt", syscall.SIGINT},
		{"terminate", syscall.SIGTERM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=TestSpinUntilSignalled")
			cmd.Env = append(os.Environ(), "BARRACKS_PROGRESS_SPIN=1")
			stderr, err := cmd.StderrPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}

			// Read until the child has taken the cursor: signalling before that
			// would prove nothing about giving it back.
			seen := &bytes.Buffer{}
			readUntil(t, stderr, seen, hideCursor)
			if err := cmd.Process.Signal(tc.sig); err != nil {
				t.Fatal(err)
			}

			rest, _ := io.ReadAll(stderr)
			seen.Write(rest)
			err = cmd.Wait()

			got := seen.String()
			if !strings.HasSuffix(got, clearLine+showCursor) {
				t.Errorf("%s left the terminal with the cursor hidden:\n%q", tc.name, got)
			}
			if strings.Count(got, hideCursor) != strings.Count(got, showCursor) {
				t.Errorf("%s did not balance hide and show:\n%q", tc.name, got)
			}

			// And the process still died of the signal, with the status the
			// shell would have seen without any handler.
			var ee *exec.ExitError
			if !asExit(err, &ee) {
				t.Fatalf("%s: child exited with %v, want death by signal", tc.name, err)
			}
			ws, ok := ee.Sys().(syscall.WaitStatus)
			if !ok || !ws.Signaled() || ws.Signal() != tc.sig {
				t.Errorf("%s: child exited %v, want signalled with %v", tc.name, ee, tc.sig)
			}
		})
	}
}

// TestSpinUntilSignalled is the child of the test above: it spins on stderr and
// waits to be killed. Outside that harness it does nothing.
func TestSpinUntilSignalled(t *testing.T) {
	if os.Getenv("BARRACKS_PROGRESS_SPIN") == "" {
		t.Skip("helper for TestASignalRestoresTheTerminalAndStillKills")
	}
	r := &Reporter{W: os.Stderr, Live: true, Reveal: 5 * time.Millisecond, Elapsed: time.Hour, LongWait: time.Hour}
	defer r.Step(Work{Subject: "github.com/big/monorepo", Phase: "fetching"}).Fail()
	time.Sleep(time.Minute)
}

// readUntil fills into from r until it holds marker.
func readUntil(t *testing.T, r io.Reader, into *bytes.Buffer, marker string) {
	t.Helper()
	chunk := make([]byte, 512)
	deadline := time.Now().Add(20 * time.Second)
	for !strings.Contains(into.String(), marker) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q; read so far:\n%q", marker, into.String())
		}
		n, err := r.Read(chunk)
		into.Write(chunk[:n])
		if err != nil {
			t.Fatalf("reading the child's stderr: %v; read so far:\n%q", err, into.String())
		}
	}
}

func asExit(err error, target **exec.ExitError) bool { return errors.As(err, target) }
