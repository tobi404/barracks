// Package lease records what a spawn created and when it should end.
//
// Every lease is a complete, self-contained undo log: the exact paths barracks
// made, the exclude block it wrote, and the directories it had to create. That
// record is the only thing revocation is allowed to act on.
package lease

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tobi404/barracks/internal/gitexclude"
	"gopkg.in/yaml.v3"
)

// ErrNotFound is returned when no lease has that ID.
var ErrNotFound = errors.New("lease not found")

// Kind is how a lease ends.
type Kind string

const (
	// KindManual never expires; it ends on `barracks recall`.
	KindManual Kind = "manual"
	// KindDeadline ends when the clock passes ExpiresAt.
	KindDeadline Kind = "deadline"
	// KindProcess ends when the owning process exits.
	KindProcess Kind = "process"
)

// Scope is where a spawn was materialised.
type Scope string

const (
	// ScopeRepo is the current repository's skills directory.
	ScopeRepo Scope = "repo"
	// ScopeGlobal is the agent's user-level skills directory.
	ScopeGlobal Scope = "global"
)

// Owner identifies the process a KindProcess lease is tied to.
//
// StartToken is what makes the record survive PID reuse: a live PID whose token
// differs is a different process, and the lease is dead.
type Owner struct {
	PID        int    `yaml:"pid"`
	StartToken string `yaml:"start_token"`
	Command    string `yaml:"command,omitempty"`
}

// Link is one symlink barracks created.
type Link struct {
	// Path is the symlink inside the target directory.
	Path string `yaml:"path"`
	// Target is the store directory it points at.
	Target string `yaml:"target"`
	// Skill is the skill's name, for output.
	Skill string `yaml:"skill"`
	// Source identifies where the skill came from.
	Source string `yaml:"source,omitempty"`
}

// Lease is one live spawn.
type Lease struct {
	ID        string     `yaml:"id"`
	Loadout   string     `yaml:"loadout"`
	Target    string     `yaml:"target"`
	Scope     Scope      `yaml:"scope"`
	Root      string     `yaml:"root,omitempty"`
	Dir       string     `yaml:"dir"`
	Kind      Kind       `yaml:"kind"`
	CreatedAt time.Time  `yaml:"created_at"`
	ExpiresAt *time.Time `yaml:"expires_at,omitempty"`
	Owner     *Owner     `yaml:"owner,omitempty"`

	Links []Link `yaml:"links"`
	// CreatedDirs are directories barracks made and may remove if they end up
	// empty again. Deepest last.
	CreatedDirs []string           `yaml:"created_dirs,omitempty"`
	Exclude     *gitexclude.Record `yaml:"exclude,omitempty"`
}

// NewID mints a random lease ID.
func NewID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// A time-based fallback keeps the tool usable if the entropy pool is
		// unavailable; collisions here are merely inconvenient, not unsafe.
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// Expired reports whether a deadline lease has run out.
func (l *Lease) Expired(now time.Time) bool {
	return l.Kind == KindDeadline && l.ExpiresAt != nil && !now.Before(*l.ExpiresAt)
}

// Describe renders the lifetime in one short phrase for command output.
func (l *Lease) Describe(now time.Time) string {
	switch l.Kind {
	case KindDeadline:
		if l.ExpiresAt == nil {
			return "until deadline"
		}
		left := l.ExpiresAt.Sub(now).Round(time.Second)
		if left <= 0 {
			return "expired"
		}
		return "expires in " + left.String()
	case KindProcess:
		if l.Owner == nil {
			return "tied to a process"
		}
		cmd := l.Owner.Command
		if cmd == "" {
			cmd = "process"
		}
		return fmt.Sprintf("while pid %d (%s) runs", l.Owner.PID, cmd)
	default:
		return "until recalled"
	}
}

// Store is the leases directory.
type Store struct{ Dir string }

// NewStore builds a Store over dir.
func NewStore(dir string) *Store { return &Store{Dir: dir} }

func (s *Store) path(id string) string { return filepath.Join(s.Dir, id+".yaml") }

// Save writes a lease record.
func (s *Store) Save(l *Lease) error {
	if l.ID == "" {
		return fmt.Errorf("lease has no id")
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	b, err := yaml.Marshal(l)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path(l.ID))
}

// Get loads a lease by ID.
func (s *Store) Get(id string) (*Lease, error) {
	b, err := os.ReadFile(s.path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return nil, err
	}
	var l Lease
	if err := yaml.Unmarshal(b, &l); err != nil {
		return nil, fmt.Errorf("parse lease %s: %w", id, err)
	}
	return &l, nil
}

// Delete removes a lease record.
func (s *Store) Delete(id string) error {
	err := os.Remove(s.path(id))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// List returns every lease, oldest first. Unreadable records are reported
// rather than aborting.
func (s *Store) List() ([]*Lease, []error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []error{err}
	}
	var out []*Lease
	var problems []error
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		l, err := s.Get(strings.TrimSuffix(e.Name(), ".yaml"))
		if err != nil {
			problems = append(problems, err)
			continue
		}
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, problems
}

// FindInDir returns the leases materialised into dir.
func FindInDir(leases []*Lease, dir string) []*Lease {
	var out []*Lease
	for _, l := range leases {
		if sameDir(l.Dir, dir) {
			out = append(out, l)
		}
	}
	return out
}

func sameDir(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}
