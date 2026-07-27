# Project agent memory

This file is the project's committed home for project-intrinsic agent knowledge: build, test, release, architecture, and sharp-edge notes that should travel with the code.

barracks is a Go CLI that manages named bundles of agent skills ("loadouts") sourced from
git repos and materialises them into any repo. `README.md` covers what it does and the
command surface; this file covers what a contributor needs that the code does not show.

## Build and test

`make build` / `make test` / `make cover` / `make lint` - see `Makefile`. Two binaries ship
from one program: `barracks` (root) and `brk` (`cmd/brk`).

**Tests must never touch the network.** Build local git fixtures with
`internal/testutil` (`NewSkillRepo` git-inits a temp dir with `SKILL.md` directories) and
point sources at those paths. `internal/source` treats a filesystem path as a first-class
source form precisely so this works.

## Invariants that must not be broken

These are the reasons the design looks the way it does. Changing any of them needs a
deliberate decision, not a refactor.

- **Target paths live only in `internal/target/target.go`.** No command logic may spell out
  `.claude/skills` or any agent path. Paths, aliases, detection markers, and the primary
  source each path came from are all fields on a `Registry` entry, so supporting a new agent
  is a new entry. `TestRegistryIsExercised` guards the entry shape;
  `cli.TestAddingATargetIsDataNotCode` drives a full lifecycle through an agent invented
  purely in the test, and is the real proof the claim still holds.
- **Every registry path is quoted from that agent's own current documentation**, recorded in
  the entry's `Docs` field. These conventions move; do not fill one in from memory. Every
  supported agent consumes the same artifact - a directory containing a `SKILL.md` - so
  barracks never translates a skill into another format. An agent that wants a different
  artifact (a rule file with its own extension and frontmatter) is a product decision, not a
  map edit: report it rather than inventing a conversion.
- **A spawn reaches every target the loadout installs into, and is all-or-nothing.**
  `spawn.Engine.SpawnAll` revokes what it already created if a later target fails, and one
  `recall` undoes every lease in the scope. Commands find leases with `lease.FindInScope`
  (repository root, or all global) rather than one target's directory, which is what makes
  a single recall undo a two-agent spawn.
- **Revocation never removes what barracks did not create.** `lease.Revoke` removes a
  recorded path only when it is still a symlink pointing at the exact store directory the
  lease recorded, and that target is inside the store. Everything else is kept and
  *reported* - silence is a bug. Directory pruning only touches empty directories the lease
  recorded creating.
- **A process lease never trusts a bare PID.** `lease.Owner` carries a start token from
  `internal/proc`; a live PID with a different token is a dead lease. When the prober cannot
  tell, the lease is treated as alive - barracks would rather leak a symlink than delete one
  it is unsure about.
- **Spawning must leave `git status` clean.** Paths go in `.git/info/exclude` via a
  lease-keyed fenced block (`internal/gitexclude`), never `.gitignore`. The record tracks
  whether the file held nothing but barracks blocks and whether a trailing newline was
  added, so removal restores it byte for byte and only deletes a file barracks itself
  brought into being.
- **The store is content-addressed and shared.** `store/<host>/<owner>/<repo>@<commit>/`.
  Exports land in a temp dir and are renamed, so a partial fetch can never look complete.

## Sharp edges

- `internal/proc` uses `ps -o lstart=,ppid=,comm=` on darwin because `lstart` alone has
  one-second resolution and cannot distinguish two processes started in the same second.
  On linux it reads `/proc/<pid>/stat` (start time + ppid). Keep the extra discriminators.
- `git ls-remote` only emits the peeled `^{}` line for an annotated tag when a pattern
  matches it explicitly - `gitcmd.ResolveRef` passes those spellings deliberately.
- `source.Validate` must reject `.` and `..` segments explicitly; they otherwise pass the
  safe-character regex and would escape the store.
- On macOS `/tmp` is a symlink to `/private/tmp`, so git-reported repo roots differ from
  `t.TempDir()` paths. Compare against `filepath.EvalSymlinks` output in tests.
- When two loadouts spawn into one directory, the second records no created directories.
  `spawn.withInheritedDirs` copies the chain from the existing lease so whichever lease is
  revoked last can finish the cleanup.

## Maintaining this file

Keep this file for knowledge useful to almost every future agent session in this project.
Do not repeat what the codebase already shows; point to the authoritative file or command instead.
Prefer rewriting or pruning existing entries over appending new ones.
When updating this file, preserve this bar for all agents and keep entries concise.
