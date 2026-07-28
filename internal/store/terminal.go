package store

import (
	"context"
	"path/filepath"
	"strings"
)

// sharesTerminal answers the one question the display needs about a fetch: can
// a child process of it reach the user's terminal?
//
// If it can, an animated step would be repainting the very line that child is
// writing on, ten times a second, at the moment the user is most stuck. That is
// the failure progress.Work.SharesTerminal exists to prevent, and it does not
// become acceptable because it arrived through a different door.
//
// The two mistakes do not cost the same. Being over-cautious costs a user the
// animation and nothing else - they still get every line, exactly as anyone
// redirecting to a file does. Being under-cautious costs them a prompt they
// cannot see. So anything not known to stay silent is treated as if it talks.
//
// There are two ways a child gets to the terminal, and they are two instances
// of that one question rather than two special cases:
//
//   - ssh opens /dev/tty itself for a key passphrase or a host-key
//     confirmation, so no amount of stream capture reaches it.
//   - a credential helper is a separate program git spawns. gitcmd sets
//     GIT_TERMINAL_PROMPT=0, which git passes down and which governs its own
//     prompt and every helper that asks through it - but a helper is free to
//     open the terminal itself, and the interactive ones do.
func (s *Store) sharesTerminal(ctx context.Context, cloneURL string) bool {
	switch transportOf(cloneURL) {
	case viaSSH:
		return true
	case onDisk:
		// git consults no credential helper for a repository on disk, so there
		// is no child here that could prompt. Asking anyway would silence the
		// indicator for the common fixture case in exchange for nothing.
		return false
	default:
		return !s.credentialHelpersStaySilent(ctx)
	}
}

// transport is how git will reach a source, as far as who can talk to the
// terminal is concerned. It is not a full classification of URL forms.
type transport int

const (
	viaNetwork transport = iota
	viaSSH
	onDisk
)

func transportOf(cloneURL string) transport {
	u := strings.TrimSpace(cloneURL)
	if i := strings.Index(u, "://"); i >= 0 {
		switch scheme := strings.ToLower(u[:i]); {
		case scheme == "ssh" || strings.HasSuffix(scheme, "+ssh"):
			return viaSSH
		case scheme == "file":
			return onDisk
		default:
			return viaNetwork
		}
	}
	if filepath.IsAbs(u) || strings.HasPrefix(u, ".") {
		return onDisk // a local repository; source.Parse resolves these to absolute paths
	}
	if strings.Contains(u, ":") {
		// The scp-like form git hands to ssh: git@github.com:owner/repo.git.
		return viaSSH
	}
	return viaNetwork
}

// credentialHelperKeys matches every spelling of the setting at every scope:
// plain credential.helper and the url-scoped credential.<url>.helper.
const credentialHelperKeys = `^credential\.(.*\.)?helper$`

// silentHelpers are the credential helpers established to answer git without
// ever touching the terminal, by the program name git invokes them under.
//
// Each entry records why it is here so a future reader can audit the claim
// rather than trust it. Do not add one that has not been checked the same way:
// the whole value of the list is that an unrecognised helper costs an animation
// while a wrongly listed one costs an invisible prompt.
var silentHelpers = map[string]bool{
	// Uses the macOS Security framework and carries no tty, askpass or prompt
	// string at all in the shipped binary (checked with strings(1) against the
	// Xcode-shipped helper). A locked keychain raises a GUI dialog, never a
	// terminal prompt.
	"osxkeychain": true,
	// Reads and writes ~/.git-credentials and nothing else. Its /dev/tty string
	// comes from git's own prompt.c, linked in through libgit.a, and every path
	// there is gated by GIT_TERMINAL_PROMPT - which gitcmd sets to 0 and which
	// git propagates to helper subprocesses through the environment.
	"store": true,
	// Talks to git-credential-cache--daemon over a unix socket. Same libgit.a
	// linkage and the same GIT_TERMINAL_PROMPT gate as store.
	"cache": true,
}

// Deliberately absent, and not to be added back: manager-core (and manager).
// Git Credential Manager is explicitly interactive - it renders terminal menus
// and prompts when no GUI is available - and it answers to its own
// GCM_INTERACTIVE / credential.interactive setting rather than to
// GIT_TERMINAL_PROMPT. It could not be established as silent, so it is treated
// as one more helper barracks does not recognise.

// credentialHelpersStaySilent reports whether every credential helper git is
// configured with is known to answer without touching the terminal.
//
// Every scope is read in one go and a single unrecognised helper anywhere
// settles it, rather than working out which helper would apply to this
// particular URL. That is both simpler and the more cautious of the two.
//
// The read happens at most once per run, and only the first time a step gets
// far enough to need the answer, so a command that never reaches the network
// never pays for a git subprocess it has no use for.
func (s *Store) credentialHelpersStaySilent(ctx context.Context) bool {
	s.helpersOnce.Do(func() {
		values, err := s.Git.ConfigMatching(ctx, s.Workdir, credentialHelperKeys)
		if err != nil {
			// Configuration barracks could not read is not an error - the fetch
			// is what matters, the spinner is not, so nothing is reported and
			// nothing fails - but it is not a clean bill of health either.
			return
		}
		for _, value := range values {
			if !helperStaysSilent(value) {
				return
			}
		}
		s.helpersSilent = true
	})
	return s.helpersSilent
}

// helperStaysSilent classifies one configured credential.helper value.
func helperStaysSilent(value string) bool {
	v := strings.TrimSpace(value)
	if v == "" {
		// git's documented reset directive: an empty value clears the helpers
		// accumulated so far. It is skipped rather than counted as unknown, and
		// deliberately not modelled any further than that - ignoring what it
		// clears can only ever leave barracks more cautious, never less.
		return true
	}
	fields := strings.Fields(strings.TrimPrefix(v, "!"))
	if len(fields) == 0 {
		return false
	}
	program := filepath.Base(fields[0])
	if strings.HasPrefix(v, "!") {
		// A shell snippet can do anything at all, so only the one form that was
		// actually verified is accepted: what `gh auth setup-git` writes, e.g.
		// "!/opt/homebrew/bin/gh auth git-credential". Asked for a credential on
		// stdin, gh answers immediately and touches no terminal.
		return program == "gh" && len(fields) >= 3 && fields[1] == "auth" && fields[2] == "git-credential"
	}
	// Trailing arguments - "cache --timeout=3600", "store --file=..." - cannot
	// make a listed helper interactive, so only the program is looked at.
	return silentHelpers[strings.TrimPrefix(program, "git-credential-")]
}
