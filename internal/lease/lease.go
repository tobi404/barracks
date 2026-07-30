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

// FormatVersion is the lease record format this build writes. Version 1 is the
// original record and is written without the field at all, so a lease loaded
// with Version 0 is a version-1 record.
//
// Nothing may ask whether a record is *current*; bumping this for one new field
// would then declare every other field missing from every record on disk. A
// reader asks whether a record is new enough to carry the field it wants, by
// comparing against the version that field landed in - provenanceSince below,
// and one such constant per field added from here on.
// Version 3 added Selection. It carries no <field>Since constant of its own on
// purpose, and that is the exception rather than a lapse: a reader needs one
// only when the *absence* of a field means something different from its zero
// value. Sources absent means "this record predates provenance", which is not
// an empty set. Selection absent means the spawn was never narrowed, which is
// exactly what an empty Selection means - so every reader gets the right answer
// from the field alone, and a constant nothing consults would be decoration.
const FormatVersion = 3

// provenanceSince is the record version Lease.Sources was introduced in. It is
// deliberately not FormatVersion: the two must be free to diverge the moment an
// unrelated field bumps the format, which is what Selection has now done.
const provenanceSince = 2

// SourceRef records one source a spawn was materialised from.
//
// It is provenance, not an undo record: Links remains the complete list of
// paths barracks created and the only thing revocation acts on. This exists
// because the links are also the only evidence that a spawn belongs to a
// source, and a source that momentarily exports no skills destroys that
// evidence - after which nothing could ever re-attach it.
//
// Key and Subpath are what a lookup matches on, because `upgrade --pin`
// rewrites a source's ref and therefore its Ident. Ident is kept as the label a
// human reads, and is refreshed whenever the definition changes underneath it.
type SourceRef struct {
	// Ident is the human label, e.g. "github.com/tobi404/skills#main:skills".
	Ident string `yaml:"ident,omitempty"`
	// Key is the repository identity: host/owner/repo.
	Key string `yaml:"key"`
	// Subpath restricts the source to a directory inside the repository.
	Subpath string `yaml:"subpath,omitempty"`
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
	// Version is the record format. Absent means version 1, written before
	// Sources existed.
	Version   int        `yaml:"version,omitempty"`
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
	// Sources are the sources this spawn was materialised from, independently of
	// which of them still contribute a link. Empty on a record older than
	// provenanceSince, which is not the same as "carries nothing" - see
	// HasProvenance.
	Sources []SourceRef `yaml:"sources,omitempty"`
	// Selection is the skills this deployment was deliberately narrowed to.
	// Empty means the whole loadout, which is what every spawn made before
	// `spawn --only` existed was.
	//
	// It is the skill *names* the narrowing resolved to, not the glob patterns
	// that produced them. Re-running a pattern later is how a narrowed
	// deployment would silently widen: `--only 'react-*'` re-evaluated after
	// react-native appeared upstream would install a skill the user never chose.
	//
	// Like Sources it is provenance and gates *additions* only - see
	// CarriesSkill. Links remains the complete record of what is on disk and the
	// only thing revocation acts on. A removal that consulted this would start
	// deleting on the strength of a record, which is the one direction provenance
	// may never travel.
	Selection []string `yaml:"selection,omitempty"`
	// CreatedDirs are directories barracks made and may remove if they end up
	// empty again. Deepest last.
	CreatedDirs []string           `yaml:"created_dirs,omitempty"`
	Exclude     *gitexclude.Record `yaml:"exclude,omitempty"`
}

// Narrowed reports whether this deployment carries only part of its loadout.
func (l *Lease) Narrowed() bool { return len(l.Selection) > 0 }

// CarriesSkill reports whether this deployment's selection admits a skill.
//
// An un-narrowed spawn admits everything, which is what keeps an ordinary
// upgrade free to install a skill that appeared upstream. A narrowed one admits
// exactly what it was deployed with - so a skill that vanished and came back is
// re-linked rather than stranded, and one the user left behind is never
// installed by an upgrade they only asked to move the others forward.
func (l *Lease) CarriesSkill(name string) bool {
	if len(l.Selection) == 0 {
		return true
	}
	for _, s := range l.Selection {
		if s == name {
			return true
		}
	}
	return false
}

// HasProvenance reports whether Sources can be trusted as the complete set of
// sources this spawn was made from.
//
// A record older than provenanceSince has no Sources field, and reading its
// absence as an empty set would say the spawn came from nothing. Callers must
// fall back to whatever they did before - for upgrade, inspecting the links -
// rather than act on it.
func (l *Lease) HasProvenance() bool { return l.Version >= provenanceSince }

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

// FindInScope returns the leases belonging to one place rather than one
// directory: every target inside the repository rooted at root, or every
// user-level spawn when scope is global.
//
// A loadout may be deployed into several agents at once, so commands that act
// on "here" have to reason about the repository, not about one agent's skills
// directory.
func FindInScope(leases []*Lease, scope Scope, root string) []*Lease {
	var out []*Lease
	for _, l := range leases {
		if l.Scope != scope {
			continue
		}
		if scope == ScopeRepo && !sameDir(l.Root, root) {
			continue
		}
		out = append(out, l)
	}
	return out
}

// WithTargets keeps only the leases deployed into one of the given target IDs.
// An empty list means every target, so a caller can pass a --target filter
// straight through.
func WithTargets(leases []*Lease, ids []string) []*Lease {
	if len(ids) == 0 {
		return leases
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	var out []*Lease
	for _, l := range leases {
		if want[l.Target] {
			out = append(out, l)
		}
	}
	return out
}

func sameDir(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}
