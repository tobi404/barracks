# Project agent memory

This file is the project's committed home for project-intrinsic agent knowledge: build, test, release, architecture, and sharp-edge notes that should travel with the code.

barracks is a Go CLI that manages named bundles of agent skills ("loadouts") sourced from
git repos and materialises them into any repo. `README.md` covers what it does and the
command surface; this file covers what a contributor needs that the code does not show.

## Build and test

`make build` / `make test` / `make cover` / `make lint` - see `Makefile`. Two binaries ship
from one program: `barracks` (root) and `brk` (`cmd/brk`).

CI (`.github/workflows/ci.yml`) runs build, `gofmt`, `go vet`, and `make cover-check` on
both `ubuntu-latest` and `macos-latest`, plus `golangci-lint` on Linux only. The matrix is
not decoration: `internal/proc` dispatches on `runtime.GOOS`, so Linux-only CI would leave
the darwin liveness probe untested. `make golangci` runs the same linter at the version in
`.golangci-lint-version`; keep that file and the action in sync. `noctx` is disabled in
`.golangci.yml` on purpose - the reasoning is written in the config, next to the setting.
Coverage floor is 80% (`COVER_MIN` in the `Makefile`). Add release automation as a separate
workflow file rather than extending this one.

**Tests must never touch the network.** Build local git fixtures with
`internal/testutil` (`NewSkillRepo` git-inits a temp dir with `SKILL.md` directories) and
point sources at those paths. `internal/source` treats a filesystem path as a first-class
source form precisely so this works.

## Invariants that must not be broken

These are the reasons the design looks the way it does. Changing any of them needs a
deliberate decision, not a refactor.

- **Target paths live only in `internal/target/target.go`.** No command logic may spell out
  `.claude/skills` or any agent path. Supporting a new agent is a new `Registry` entry.
  `TestRegistryIsExercised` enforces that at least two entries exist.
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
