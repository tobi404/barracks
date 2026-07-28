// Package loadout stores loadout definitions: named bundles of skill sources.
//
// One YAML file per loadout, deliberately plain, because users are expected to
// open and edit them without the CLI.
package loadout

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tobi404/barracks/internal/source"
	"gopkg.in/yaml.v3"
)

// ErrNotFound is returned when no loadout by that name has been trained.
var ErrNotFound = errors.New("loadout not found")

// ErrExists is returned when training over an existing loadout.
var ErrExists = errors.New("loadout already exists")

// ErrNoSuchSource is returned when a source spelling matches nothing equipped.
var ErrNoSuchSource = errors.New("source not equipped")

// ErrAmbiguousSource is returned when a source spelling matches more than one
// equipped entry. Removal never guesses which one was meant.
var ErrAmbiguousSource = errors.New("source matches more than one equipped entry")

var nameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// Equipment is one git source attached to a loadout, pinned to a commit so a
// spawn reproduces even after the ref moves.
type Equipment struct {
	source.Source `yaml:",inline"`
	// Commit is the resolved SHA the loadout is pinned to.
	Commit string `yaml:"commit"`
	// Only and Except are glob filters over discovered skill names.
	Only   []string `yaml:"only,omitempty"`
	Except []string `yaml:"except,omitempty"`
	// Skills is the set discovered at equip time, recorded for readability.
	Skills     []string  `yaml:"skills,omitempty"`
	EquippedAt time.Time `yaml:"equipped_at"`
}

// Loadout is a named bundle of skill sources.
type Loadout struct {
	Name string `yaml:"name"`
	// ID is the loadout's stable identity: minted once, never derived from the
	// name, and never changed by a rename.
	//
	// It exists because a committed barracks.lock lives in somebody else's
	// repository. barracks can rewrite the lockfiles it can reach, and it can
	// reach none of the checkouts on other machines, so a rename that keyed on
	// the name alone would orphan every garrison in them - the files would still
	// be there and nothing would ever recognise them again. Keying on an
	// identity the name cannot affect is what makes a rename a display change.
	//
	// Empty on a definition written before identities existed. Store.Get fills
	// that in; nothing may read the absence as a mismatch. See
	// garrison.Manifest.FindFor for the rule the other half obeys.
	ID          string    `yaml:"id,omitempty"`
	Description string    `yaml:"description,omitempty"`
	CreatedAt   time.Time `yaml:"created_at"`
	// Targets are the agent target IDs this loadout installs into. Empty means
	// barracks decides at spawn time from what the repository contains. The
	// choice belongs to the loadout, not to a machine-wide setting, so that one
	// loadout can be a Cursor loadout and another a Claude Code one.
	Targets   []string    `yaml:"targets,omitempty"`
	Equipment []Equipment `yaml:"equipment"`
}

// SetTargets replaces the declared target list, trimming blanks and repeats.
// An empty result clears the declaration, returning the loadout to detection.
func (l *Loadout) SetTargets(ids []string) {
	var out []string
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	l.Targets = out
}

// Equip attaches eq, replacing any equipment already attached from the same
// source identity - host, owner, repo, ref, and subpath all matching.
//
// Re-equipping a source is a re-pin, not a second copy. Two entries for one
// source would contribute the same skill names twice and make the loadout
// unspawnable, with a collision message naming the same source on both sides.
// A different ref or subpath is a different source and is kept alongside.
//
// It returns the equipment it replaced, or nil when eq is newly attached.
func (l *Loadout) Equip(eq Equipment) *Equipment {
	for i := range l.Equipment {
		if l.Equipment[i].Ident() != eq.Ident() {
			continue
		}
		previous := l.Equipment[i]
		l.Equipment[i] = eq
		return &previous
	}
	l.Equipment = append(l.Equipment, eq)
	return nil
}

// Strip detaches the equipment at index i and returns it.
//
// The inverse of Equip, and deliberately by index rather than by spelling: which
// entry a source spelling means is a question with three answers - none, one, or
// too many - and Find is where it is answered, so that nothing which removes
// things ever has to guess.
func (l *Loadout) Strip(i int) Equipment {
	dropped := l.Equipment[i]
	l.Equipment = append(l.Equipment[:i:i], l.Equipment[i+1:]...)
	return dropped
}

// Find resolves a source spelling to the one equipped entry it means.
//
// A user types what they remember, not what the definition recorded: `equip`
// expands gh:owner/repo into a full ident, and `upgrade --pin` can rewrite the
// ref afterwards to something nobody ever typed. So the match widens in steps
// and stops at the first that finds anything. Finding several at that step is an
// error, never a choice made here: removing the wrong source would take skills
// out of every repository the loadout is deployed in.
//
// Each step drops only a part the user could not reasonably reproduce. The ref
// goes first, because --pin owns it. The subpath is dropped only when the
// spelling gave none at all - a subpath that was typed and does not match is a
// different source, and answering it with the one the user did not name would be
// the worst kind of helpful.
func (l *Loadout) Find(src source.Source) (int, error) {
	steps := []func(Equipment) bool{
		func(eq Equipment) bool { return eq.Ident() == src.Ident() },
		func(eq Equipment) bool { return eq.RepoKey() == src.RepoKey() && eq.Subpath == src.Subpath },
		func(eq Equipment) bool { return src.Subpath == "" && eq.RepoKey() == src.RepoKey() },
	}
	for _, matches := range steps {
		var found []int
		for i, eq := range l.Equipment {
			if matches(eq) {
				found = append(found, i)
			}
		}
		switch len(found) {
		case 0:
			continue
		case 1:
			return found[0], nil
		default:
			return -1, fmt.Errorf("%w: %s could mean %s; name one of them exactly",
				ErrAmbiguousSource, src.Ident(), strings.Join(l.identsAt(found), " or "))
		}
	}
	if len(l.Equipment) == 0 {
		return -1, fmt.Errorf("%w: %s is not equipped on %s, which has no sources at all", ErrNoSuchSource, src.Ident(), l.Name)
	}
	return -1, fmt.Errorf("%w: %s is not equipped on %s, which has %s",
		ErrNoSuchSource, src.Ident(), l.Name, strings.Join(l.Idents(), ", "))
}

// Idents lists every equipped source's label, in definition order.
func (l *Loadout) Idents() []string {
	out := make([]string, 0, len(l.Equipment))
	for _, eq := range l.Equipment {
		out = append(out, eq.Ident())
	}
	return out
}

func (l *Loadout) identsAt(idx []int) []string {
	out := make([]string, 0, len(idx))
	for _, i := range idx {
		out = append(out, l.Equipment[i].Ident())
	}
	return out
}

// SkillCount is the number of skills recorded across every source.
func (l *Loadout) SkillCount() int {
	n := 0
	for _, e := range l.Equipment {
		n += len(e.Skills)
	}
	return n
}

// ValidateName rejects names that would not be safe as a filename.
func ValidateName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("invalid loadout name %q: use letters, digits, dot, dash, or underscore (max 64)", name)
	}
	return nil
}

// NewID mints a loadout identity.
//
// Random, never derived from the name: an identity computed from the name would
// change with the name and be no identity at all. Sixteen hex characters is the
// same shape a lease ID has, which is short enough to read out of a lockfile.
func NewID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// The entropy pool being unavailable must not stop a loadout being
		// trained. A nanosecond timestamp is unique enough for a value only ever
		// compared for equality against other loadouts on the same machine.
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// Store is the loadouts directory.
type Store struct{ Dir string }

// NewStore builds a Store over dir.
func NewStore(dir string) *Store { return &Store{Dir: dir} }

func (s *Store) path(name string) string { return filepath.Join(s.Dir, name+".yaml") }

// Create writes a new, empty loadout.
func (s *Store) Create(name, description string, now time.Time) (*Loadout, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	if _, err := os.Stat(s.path(name)); err == nil {
		return nil, fmt.Errorf("%w: %s", ErrExists, name)
	}
	l := &Loadout{Name: name, ID: NewID(), Description: description, CreatedAt: now.UTC()}
	if err := s.Save(l); err != nil {
		return nil, err
	}
	return l, nil
}

// Get loads a loadout by name.
//
// A definition written before identities existed is given one here and saved
// back, so the identity a lockfile is stamped with is the same one every later
// command reads. When that write fails the loadout is returned with no identity
// rather than a volatile one: a lockfile stamped with an identity that changes
// between runs could never be matched again, while no identity at all falls back
// to matching by name, exactly as everything did before this existed.
func (s *Store) Get(name string) (*Loadout, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(s.path(name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		return nil, err
	}
	var l Loadout
	if err := yaml.Unmarshal(b, &l); err != nil {
		return nil, fmt.Errorf("parse loadout %s: %w", name, err)
	}
	if l.Name == "" {
		l.Name = name
	}
	if l.ID == "" {
		l.ID = NewID()
		if err := s.Save(&l); err != nil {
			l.ID = ""
		}
	}
	return &l, nil
}

// Save writes a loadout, replacing any existing definition.
//
// It mints an identity for a loadout that has none, so that every path which
// writes a definition backfills one exactly once and nothing else has to
// remember to.
func (s *Store) Save(l *Loadout) error {
	if err := ValidateName(l.Name); err != nil {
		return err
	}
	if l.ID == "" {
		l.ID = NewID()
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	b, err := yaml.Marshal(l)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("# barracks loadout %q - edit by hand freely.\n", l.Name)
	return writeAtomic(s.path(l.Name), append([]byte(header), b...))
}

// Rename moves a loadout's definition to a new name, keeping its identity.
//
// It refuses rather than overwriting: a name already in use belongs to another
// loadout, and taking it would delete that loadout's definition to make room.
//
// The order is deliberate. The new definition is written first and the old one
// removed second, so an interrupted rename leaves two files rather than none -
// and when the removal fails the new file is taken back off disk, leaving the
// store exactly as it was found. Two definitions for one loadout is the state
// this must never produce: both would answer to `barracks list`, and every
// later command would act on whichever name it happened to be given.
func (s *Store) Rename(from, to string) (*Loadout, error) {
	if err := ValidateName(to); err != nil {
		return nil, err
	}
	l, err := s.Get(from)
	if err != nil {
		return nil, err
	}
	if from == to {
		return l, nil
	}
	if _, err := os.Stat(s.path(to)); err == nil {
		return nil, fmt.Errorf("%w: %s", ErrExists, to)
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	renamed := *l
	renamed.Name = to
	if err := s.Save(&renamed); err != nil {
		return nil, err
	}
	if err := s.Delete(from); err != nil {
		_ = os.Remove(s.path(to))
		return nil, fmt.Errorf("remove the old definition %s: %w", from, err)
	}
	return &renamed, nil
}

// Delete removes a loadout definition.
func (s *Store) Delete(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if err := os.Remove(s.path(name)); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		return err
	}
	return nil
}

// List returns every trained loadout, name-sorted. Unreadable files are
// reported rather than aborting the listing.
func (s *Store) List() ([]*Loadout, []error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []error{err}
	}
	var out []*Loadout
	var problems []error
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".yaml")
		l, err := s.Get(name)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, problems
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
