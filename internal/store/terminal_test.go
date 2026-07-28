package store

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tobi404/barracks/internal/gitcmd"
	"github.com/tobi404/barracks/internal/progress"
	"github.com/tobi404/barracks/internal/source"
)

// The rule these tests hold: barracks never animates over something that might
// be talking to the user. Every one of them asserts what the stream actually
// carries - escape sequences or plain lines - rather than the flag behind it,
// because the flag is not what erases a prompt.

const fakeCommit = "0123456789abcdef0123456789abcdef01234567"

// display is a store wired to a live reporter, watching a buffer.
type display struct {
	store *Store
	out   *bytes.Buffer
	calls string // where the git wrapper logs each `git config` invocation
}

// watched builds a store whose display is live and announces work almost
// immediately, with git's credential-helper configuration pinned to config.
//
// Pinning matters: the ambient machine has its own opinion - the Xcode-shipped
// system gitconfig sets osxkeychain, an ubuntu runner sets nothing - and a test
// that read it would assert a different thing in each place. GIT_CONFIG_NOSYSTEM
// is what actually drops the system scope; Apple's git goes on reading its own
// copy through GIT_CONFIG_SYSTEM.
func watched(t *testing.T, config string) *display {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(cfg, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)

	calls := filepath.Join(dir, "config-calls")
	st := New(filepath.Join(dir, "store"), filepath.Join(dir, "mirrors"), gitcmd.Git{Bin: fakeGit(t, calls)})
	st.Workdir = dir
	out := &bytes.Buffer{}
	st.Progress = &progress.Reporter{W: out, Live: true, Reveal: time.Millisecond}
	return &display{store: st, out: out, calls: calls}
}

// fakeGit writes a git wrapper that answers ls-remote itself and hands
// everything else - `git config` above all - to the real git.
//
// That is what makes this decision observable without a network: which helpers
// are configured is read by the real git out of a real config file, in the real
// output format, while the fetch that reading guards never leaves the machine.
// The wrapper is deliberately slow enough that every case announces a step.
func fakeGit(t *testing.T, calls string) string {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git is required for this test: %v", err)
	}
	path := filepath.Join(t.TempDir(), "git")
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "config" ]; then
	echo read >> %q
fi
if [ "$1" = "ls-remote" ]; then
	sleep 0.05
	printf '%%s\tHEAD\n' %q
	exit 0
fi
exec %q "$@"
`, calls, fakeCommit, real)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// resolve drives one announced step and returns what the user would have seen.
func (d *display) resolve(t *testing.T, raw string) string {
	t.Helper()
	src, err := source.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := d.store.Resolve(ctx(), src)
	if err != nil {
		t.Fatalf("Resolve %s: %v", raw, err)
	}
	if commit != fakeCommit {
		t.Fatalf("Resolve %s = %q, want %q", raw, commit, fakeCommit)
	}
	out := d.out.String()
	d.out.Reset()
	if !strings.Contains(out, "resolved") {
		t.Fatalf("the step was never announced at all:\n%q", out)
	}
	return out
}

func name(t transport) string {
	switch t {
	case viaSSH:
		return "viaSSH"
	case onDisk:
		return "onDisk"
	default:
		return "viaNetwork"
	}
}

func animated(stream string) bool {
	return strings.Contains(stream, "\x1b") || strings.Contains(stream, "\r")
}

// TestAnUnrecognisedCredentialHelperIsNeverAnimated is the https half of the
// one rule: a credential helper is a separate program git spawns, and one that
// opens /dev/tty for itself is not bound by GIT_TERMINAL_PROMPT. Erasing that
// prompt ten times a second is the failure being prevented, so anything not
// known to stay silent runs in the same append-only mode as a redirected
// stream.
func TestAnUnrecognisedCredentialHelperIsNeverAnimated(t *testing.T) {
	for _, tc := range []struct {
		name     string
		config   string
		animated bool
	}{
		// The regression the exit-code trap would cause: `git config
		// --get-regexp` exits 1 with no output when nothing matches, which says
		// no helper is configured - the safest state there is.
		{"nothing configured", "", true},
		{"a helper that cannot prompt", "[credential]\nhelper = osxkeychain\n", true},
		{"arguments cannot make a listed helper interactive", "[credential]\nhelper = cache --timeout=3600\n", true},
		{"a helper named by path", "[credential]\nhelper = /usr/lib/git-core/git-credential-store --file=/tmp/c\n", true},
		{"the gh helper as `gh auth setup-git` writes it", "[credential]\nhelper = \"!/opt/homebrew/bin/gh auth git-credential\"\n", true},
		{"a url-scoped helper", "[credential \"https://example.com\"]\nhelper = osxkeychain\n", true},
		{"the reset directive alongside a known helper", "[credential]\nhelper = \nhelper = store\n", true},
		// Git Credential Manager prompts on the terminal when no GUI is there,
		// and answers to its own setting rather than to GIT_TERMINAL_PROMPT.
		{"an interactive helper", "[credential]\nhelper = manager-core\n", false},
		{"a helper nobody has checked", "[credential]\nhelper = corporate-sso\n", false},
		{"a shell snippet", "[credential]\nhelper = \"!my-askpass.sh\"\n", false},
		{"one unrecognised helper among several", "[credential]\nhelper = osxkeychain\nhelper = corporate-sso\nhelper = store\n", false},
		{"a url-scoped helper nobody has checked", "[credential \"https://example.com\"]\nhelper = corporate-sso\n", false},
		// Configuration barracks cannot read is not a clean bill of health. It
		// is not an error either: the fetch is what matters.
		{"configuration that cannot be read", "this is not a config file\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := watched(t, tc.config)
			out := d.resolve(t, "https://example.com/owner/repo.git")
			if got := animated(out); got != tc.animated {
				t.Errorf("animated = %v, want %v:\n%q", got, tc.animated, out)
			}
			if !tc.animated && !strings.Contains(out, "resolving…") {
				t.Errorf("an unanimated step said nothing while it worked:\n%q", out)
			}
		})
	}
}

// TestSSHIsNeverAnimatedWhateverTheHelpersAre is the other half. ssh opens
// /dev/tty itself, so no credential configuration can make it safe to paint on.
func TestSSHIsNeverAnimatedWhateverTheHelpersAre(t *testing.T) {
	for _, raw := range []string{
		"git@example.com:owner/repo.git",
		"ssh://git@example.com/owner/repo.git",
		"git+ssh://git@example.com/owner/repo.git",
	} {
		d := watched(t, "[credential]\nhelper = osxkeychain\n")
		if out := d.resolve(t, raw); animated(out) {
			t.Errorf("%s was animated:\n%q", raw, out)
		}
	}
}

// TestALocalSourceIsAlwaysAnimated: git consults no credential helper for a
// repository on disk, so there is no child that could prompt and nothing to be
// cautious about. Reading the configuration anyway would silence the indicator
// for the commonest case in exchange for nothing.
func TestALocalSourceIsAlwaysAnimated(t *testing.T) {
	d := watched(t, "[credential]\nhelper = corporate-sso\n")
	if out := d.resolve(t, filepath.Join(t.TempDir(), "fixtures")); !animated(out) {
		t.Errorf("a local source was not animated:\n%q", out)
	}
	if read(t, d.calls) != 0 {
		t.Error("a local source read the credential configuration")
	}
}

// TestTheCredentialConfigurationIsReadAtMostOncePerRun: this must not become a
// git subprocess per source, and a command that never animates must not pay for
// one at all.
func TestTheCredentialConfigurationIsReadAtMostOncePerRun(t *testing.T) {
	d := watched(t, "[credential]\nhelper = osxkeychain\n")
	d.resolve(t, "https://example.com/owner/repo.git")
	d.resolve(t, "https://example.com/owner/other.git")
	if got := read(t, d.calls); got != 1 {
		t.Errorf("read the configuration %d times, want 1", got)
	}

	quiet := watched(t, "[credential]\nhelper = osxkeychain\n")
	quiet.store.Progress.Live = false
	if _, err := quiet.store.Resolve(ctx(), mustParse(t, "https://example.com/owner/repo.git")); err != nil {
		t.Fatal(err)
	}
	if got := read(t, quiet.calls); got != 0 {
		t.Errorf("a run with nothing to animate read the configuration %d times, want 0", got)
	}
}

func read(t *testing.T, calls string) int {
	t.Helper()
	b, err := os.ReadFile(calls)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(strings.Fields(string(b)))
}

func mustParse(t *testing.T, raw string) source.Source {
	t.Helper()
	src, err := source.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

// TestTransportOfDecidesWhoCouldBeTalking pins the classification the rule
// above rests on. Getting it wrong in either direction is a real cost: too
// eager and the indicator is off for everyone, too lax and it paints over a
// prompt.
func TestTransportOfDecidesWhoCouldBeTalking(t *testing.T) {
	for _, tc := range []struct {
		cloneURL string
		want     transport
	}{
		{"https://github.com/owner/repo.git", viaNetwork},
		{"http://example.com/owner/repo.git", viaNetwork},
		{"git://example.com/owner/repo.git", viaNetwork},
		{"/home/you/fixtures/skills", onDisk},
		{"./fixtures/skills", onDisk},
		{"file:///home/you/fixtures/skills", onDisk},
		{"ssh://git@github.com/owner/repo.git", viaSSH},
		{"SSH://git@github.com/owner/repo.git", viaSSH},
		{"git+ssh://git@example.com/owner/repo.git", viaSSH},
		{"git@github.com:owner/repo.git", viaSSH},
		{"example.com:owner/repo.git", viaSSH},
	} {
		if got := transportOf(tc.cloneURL); got != tc.want {
			t.Errorf("transportOf(%q) = %s, want %s", tc.cloneURL, name(got), name(tc.want))
		}
	}
}
