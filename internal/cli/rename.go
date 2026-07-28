package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tobi404/barracks/internal/garrison"
	"github.com/tobi404/barracks/internal/lease"
	"github.com/tobi404/barracks/internal/loadout"
)

func newRenameCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "rename <loadout> <new-name>",
		Short: "Rename a loadout, keeping everything it is deployed into working",
		Long: strings.TrimSpace(`
Renames a loadout everywhere this machine records its name: the definition, every
live spawn's lease, and this repository's barracks.lock.

Nothing on disk moves. A spawned symlink and a committed skill file are named
after the skill, never after the loadout, so a rename changes records and leaves
deployments exactly where they are.

  barracks rename frontend web

A loadout carries a stable identity that a rename does not change, and
barracks.lock records it beside the name. That is what keeps a garrison working
in a checkout barracks cannot reach: the name in somebody else's clone goes stale,
and the identity still matches. A lockfile written before identities existed has
none, so it is matched by name - which is why renaming rewrites the lockfile here
rather than leaving it to be noticed later. Commit that change as you would any
other. Run barracks inspect to see the identity a lockfile keys on.

Renaming onto a name already in use is refused and changes nothing. So is a
rename that cannot be completed: if any record cannot be written, every record
already written is put back.`),
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			env.reap()
			from, to := args[0], args[1]

			if err := loadout.ValidateName(to); err != nil {
				return err
			}
			l, err := env.loadouts.Get(from)
			if err != nil {
				return err
			}
			if from == to {
				return fmt.Errorf("%s is already called that", from)
			}
			// Checked before anything is written, so the common refusal costs
			// nothing and leaves nothing to undo.
			if _, err := env.loadouts.Get(to); err == nil {
				return fmt.Errorf("%w: %s - disband it or choose another name", loadout.ErrExists, to)
			}

			r := &renamer{env: env, from: from, to: to, id: l.ID}
			if err := r.run(cmd.Context()); err != nil {
				r.undo()
				return err
			}

			fmt.Fprintf(env.Out, "renamed %s to %s (identity %s)\n", from, to, describeIdentity(l.ID))
			for _, line := range r.changed {
				fmt.Fprintf(env.Out, "  %s\n", line)
			}
			if r.lockfile {
				fmt.Fprintf(env.Out, "  commit %s\n", garrison.LockName)
			}
			return nil
		},
	}
}

// describeIdentity renders a loadout's identity for output, saying so plainly
// when there is none rather than printing a blank.
//
// An identity can genuinely be missing: Store.Get mints one for a definition
// written before they existed, and keeps it only if that write succeeded, so a
// read-only loadouts directory leaves the loadout matched by name exactly as it
// was before. Saying that out loud is better than implying there is one.
func describeIdentity(id string) string {
	if id == "" {
		return "none - matched by name"
	}
	return id
}

// renamer applies a rename across every record that holds the name, keeping
// enough behind to put each one back.
//
// The order is what makes a failure survivable. The lockfile goes first because
// it is the only record a rename can *orphan*: an entry written before
// identities existed is found by name alone, so leaving it behind under the old
// name would strand this repository's committed files. Everything after it is
// found by a key the rename does not touch - a lease by its ID, the definition
// by the file it is in - and could be reconciled by hand if it came to it.
type renamer struct {
	env  *Env
	from string
	to   string
	id   string

	changed  []string
	lockfile bool
	root     string
	lockWas  []byte
	leases   []*lease.Lease
	renamed  bool
}

func (r *renamer) run(ctx context.Context) error {
	if err := r.renameGarrison(ctx); err != nil {
		return err
	}
	if err := r.renameDefinition(); err != nil {
		return err
	}
	return r.renameLeases()
}

// renameGarrison rewrites this repository's lockfile entry. Only this
// repository: a garrison's record travels with the repository rather than with
// the machine, which is exactly why the identity beside the name exists.
func (r *renamer) renameGarrison(ctx context.Context) error {
	loc, inRepo := r.env.repoHere(ctx)
	if !inRepo {
		return nil
	}
	m, err := garrison.Load(loc.Root)
	if err != nil {
		return err
	}
	if !m.Rename(r.id, r.from, r.to) {
		return nil
	}
	was, err := garrison.ReadRaw(loc.Root)
	if err != nil {
		return err
	}
	if err := m.Save(loc.Root); err != nil {
		return fmt.Errorf("update %s: %w", garrison.LockName, err)
	}
	r.root, r.lockWas, r.lockfile = loc.Root, was, true
	r.changed = append(r.changed, fmt.Sprintf("%s in %s", garrison.LockName, loc.Root))
	return nil
}

func (r *renamer) renameDefinition() error {
	if _, err := r.env.loadouts.Rename(r.from, r.to); err != nil {
		return err
	}
	r.renamed = true
	r.changed = append(r.changed, "the loadout definition")
	return nil
}

// renameLeases rewrites the loadout name every live spawn records.
//
// A lease is found by its ID, never by the loadout's name, so a spawn goes on
// working either way - but `barracks recall`, `deployed` and `upgrade` all match
// on the name, and a lease left at the old one would be a spawn no command could
// name any more.
//
// The voice's escalation state is deliberately not migrated. It is keyed by name
// too, but it is decoration that forgets everything after ten quiet minutes, and
// a renamed loadout starting fresh is the right answer anyway.
func (r *renamer) renameLeases() error {
	all, _ := r.env.leases.List()
	for _, l := range all {
		if l.Loadout != r.from {
			continue
		}
		l.Loadout = r.to
		if err := r.env.leases.Save(l); err != nil {
			r.leases = append(r.leases, l) // written or not, it needs putting back
			return fmt.Errorf("update the lease for %s in %s: %w", r.from, l.Dir, err)
		}
		r.leases = append(r.leases, l)
	}
	if n := len(r.leases); n > 0 {
		r.changed = append(r.changed, fmt.Sprintf("%d live %s", n, plural(n, "spawn", "spawns")))
	}
	return nil
}

// undo puts back every record run had already rewritten, in reverse.
//
// Each step is the same write that made the change, with the names swapped, so
// there is no second code path that could put a record back differently from how
// it was found. What it cannot undo it reports: a half-renamed loadout the user
// does not know about is the worst outcome available here.
func (r *renamer) undo() {
	for _, l := range r.leases {
		l.Loadout = r.from
		if err := r.env.leases.Save(l); err != nil {
			fmt.Fprintf(r.env.Err, "! could not put the lease for %s in %s back: %v\n", l.Dir, r.to, err)
		}
	}
	if r.renamed {
		if _, err := r.env.loadouts.Rename(r.to, r.from); err != nil {
			fmt.Fprintf(r.env.Err, "! could not put the definition back to %s: %v\n", r.from, err)
		}
	}
	if r.lockfile {
		if err := garrison.WriteRaw(r.root, r.lockWas); err != nil {
			fmt.Fprintf(r.env.Err, "! could not put %s back to %s: %v\n", garrison.LockName, r.from, err)
		}
	}
}
