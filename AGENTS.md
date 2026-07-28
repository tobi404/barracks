# Project agent memory

This file is the project's committed home for project-intrinsic agent knowledge: build, test, release, architecture, and sharp-edge notes that should travel with the code.

barracks is a Go CLI that manages named bundles of agent skills ("loadouts") sourced from
git repos and materialises them into any repo, in one of two tiers: personal (symlinks,
lease-governed, kept out of git - `internal/spawn`) or committed (real files plus
`barracks.lock`, shared by everyone who clones - `internal/garrison`). `README.md` covers
what it does and the command surface; this file covers what a contributor needs that the
code does not show.

## Build and test

`make build` / `make test` / `make cover` / `make lint` - see `Makefile`. Two binaries ship
from one program: `barracks` (root) and `brk` (`cmd/brk`).

CI (`.github/workflows/ci.yml`) runs build, `make fmt-check`, `make vet`, and
`make cover-check` on both `ubuntu-latest` and `macos-latest`, plus `golangci-lint` on
Linux only. Every check lives in exactly one Makefile target that both CI and `make lint`
invoke - never restate a check's command inline in the workflow. A make recipe runs under
`/bin/sh` without `-e`, so a check target must capture the tool's exit status explicitly:
`unformatted=$(gofmt -l .)` alone discards it and reports a clean tree when gofmt in fact
could not parse a file or was missing entirely. `internal/buildcheck` runs the real
`fmt-check` target against fixtures to keep that silent success from returning. The matrix is
not decoration: `internal/proc` dispatches on `runtime.GOOS`, so Linux-only CI would leave
the darwin liveness probe untested. `.golangci-lint-version` is the only place the linter
version is written: `make golangci` reads it, and the action reads the same file through
its `version-file` input - bump it there, never inline. `noctx` is disabled in
`.golangci.yml` on purpose - the reasoning is written in the config, next to the setting.
Coverage floor is 80% (`COVER_MIN` in the `Makefile`). Release automation lives in its own
workflow (`.github/workflows/release.yml`); never fold it into `ci.yml`.

**Tests must never touch the network.** Build local git fixtures with
`internal/testutil` (`NewSkillRepo` git-inits a temp dir with `SKILL.md` directories) and
point sources at those paths. `internal/source` treats a filesystem path as a first-class
source form precisely so this works.

**Drive the real binary end to end before believing a feature works.** `make build` then run
`bin/brk` against throwaway git repos in a scratch directory. This has now caught two real
bugs that a full green unit suite did not: once in the upgrade work, and once in the
committed tier, where every legitimate update was misreported as a locally edited file
because the on-disk content was compared against the wrong record. Unit tests written from
the same mental model as the code share its mistakes.

## Release

Tagging `v*` runs `.github/workflows/release.yml`, which is GoReleaser
(`.goreleaser.yaml`) at the version in `.goreleaser-version` - the only place that version
is written, read by both `make release-check`/`make release-snapshot` and the workflow.
`README.md`'s Releasing section is the human-facing contract (tag format, the
`HOMEBREW_TAP_GITHUB_TOKEN` secret); the notes below are what the files themselves do not say.

- **Prove release changes with `make release-snapshot`, never with a tag.** It produces
  every archive, the checksums, and `dist/homebrew/Casks/barracks.rb` without publishing.
  A tag is not a test fixture: Homebrew and `checksums.txt` both pin to it, so a pushed
  tag can never be moved or reused.
- **A local release run proves less than it looks, because `GOTOOLCHAIN` differs.** The
  release targets reach GoReleaser through `$(GORELEASER)`, which defaults to
  `go run ...@$(GORL_VER)` - a *source* build whose Go requirement tracks GoReleaser's own
  go directive, always far ahead of what this module compiles against. Locally
  `GOTOOLCHAIN=auto` silently fetches whatever that is, so the target passes on any
  machine. `actions/setup-go` pins `GOTOOLCHAIN=local`, where the same command can only
  fail - which is exactly how `v0.1.0` died at `make release-check` having published
  nothing. So the workflow installs the pinned GoReleaser *binary*
  (`goreleaser-action` with `install-only: true`) and calls
  `make release-check GORELEASER=goreleaser`; the target stays the single owner of the
  command. Reproduce a release step honestly by prefixing `GOTOOLCHAIN=local` and passing
  a real binary in - a bare `make release-check` cannot see this class of failure.
- **The tool's Go and the module's Go are separate concerns.** `setup-go` reads
  `go-version-file: go.mod`, and that Go is what compiles the shipped binaries (`go version
  -m` on a release binary reports it). Never raise `go.mod` to satisfy GoReleaser, golangci-lint,
  or any other tool: that raises the floor for everyone importing the module for a reason
  that has nothing to do with them. Install the tool as a binary instead.
- **The binaries reproduce; the archives do not.** `-trimpath`, `CGO_ENABLED=0` and
  `mod_timestamp: {{ .CommitTimestamp }}` make two builds of one tag produce bit-identical
  binaries (verified by diffing their hashes across three runs). The `.tar.gz` around them
  still varies, because GoReleaser does not fix the order of entries in the archive. Do
  not promise archive-level reproducibility; the published `checksums.txt` is what a
  downloader verifies against.
- **The token guard is the first step in the release job on purpose.** GoReleaser creates
  the GitHub release before it pushes the Homebrew cask, so a broken
  `HOMEBREW_TAP_GITHUB_TOKEN` discovered later would leave a published release that no
  `brew install` can find. It must stay a *working*-token check, not a non-empty check: a
  fine-grained PAT always carries an expiry, so it asks GitHub about the tap with the
  token and requires the reported push permission. Anything else that can be checked
  before publishing belongs above the GoReleaser step too.
- **Version, commit, and date reach the binaries through `internal/buildinfo`, not
  `main`.** Two `main` packages ship from this module; stamping an internal package means
  one set of `-X` flags covers both. When the linker leaves them empty, `buildinfo`
  recovers what it can from `debug.ReadBuildInfo` so a `go install`-ed binary still
  reports its module version - hence the `(devel)` special case. Note that Go emits no
  `vcs.*` settings from a linked git worktree, so that fallback looks dead when tested
  from one; it is not.
- **Homebrew is a cask (`homebrew_casks`), not a formula.** GoReleaser deprecated `brews`
  and `goreleaser check` fails on it. Casks are macOS-only, which is why the README sends
  Linux users to `go install` or the tarball, and why the cask carries a `postflight`
  clearing `com.apple.quarantine` from both unsigned binaries. `skip_upload` must stay the
  quoted literal `"auto"` - GoReleaser skips the cask push for a prerelease tag on that
  exact string and nothing else, so `true`, an unquoted `auto`, or an empty field all end
  up handing a release candidate to every `brew install`. `release.footer` branches on
  `{{ if .Prerelease }}` for the same reason: a prerelease's notes must not print the
  `brew install` line the tap will not serve. `goreleaser check` does not render
  templates, so a footer change is only proved by reading the notes a tagged dry run
  produces in a throwaway clone.
- **The cask's git requirement is `dependencies: [- formula: git]`, not Homebrew's own
  `depends_on`.** GoReleaser renders the latter from the former
  (`internal/pipe/cask/templates/cask.rb`); the field name and the formula/cask split are in
  `pkg/config/config.go`. Verify a rendered cask by copying `dist/homebrew/Casks/barracks.rb`
  into a throwaway tap under `$(brew --repository)/Library/Taps/` - Homebrew refuses to look
  at a cask outside one - and reading `brew info --cask`, which reports the dependency it
  actually parsed. `brew style` flags GoReleaser's `depends_on` array indentation and stanza
  grouping; those are RuboCop layout complaints about generated Ruby, not something
  `brew install` cares about, and the rendered cask already carried one such offence before
  the dependency existed. Do not hand-write the stanza through `custom_block` to silence them.

## Invariants that must not be broken

These are the reasons the design looks the way it does. Changing any of them needs a
deliberate decision, not a refactor.

- **Target paths live only in `internal/target/target.go`.** No command logic may spell out
  `.claude/skills`, any agent path, or any agent program name. Paths, aliases, detection
  markers, the `Binaries` an agent's own CLI is invoked as, and the primary source each path
  came from are all fields on a `Registry` entry, so supporting a new agent is a new entry.
  `TestRegistryIsExercised` guards the entry shape; `cli.TestAddingATargetIsDataNotCode`
  drives a full lifecycle through an agent invented purely in the test, and is the real
  proof the claim still holds.
- **`barracks run` equips the agent it launches, and only where selection was a guess.**
  `target.ForCommand` matches argv's base name against `Binaries`, and the match joins the
  detection branch of `target.Select` as `OriginLaunched`. A `--target` flag or a loadout
  declaration is never widened by argv - when one of those excludes the launched agent,
  barracks warns on stderr and obeys the user. An unrecognised program (a wrapper, `sh -c`)
  matches nothing and must behave exactly as it did before. `Binaries` is optional: leave it
  empty rather than filling in a CLI name the entry's `Docs` does not record.
- **"Which target does this agent get" and "would this agent see what is already going
  there" are separate queries.** `target.ForCommand` answers the first and decides where
  files land; `Target.IsReadBy` / `target.AnyReadBy` answer the second and decide only
  whether `warnLaunchedAgentExcluded` prints. Never widen the first to fix the second - that
  moves a spawn in order to change a message. `AlsoReadBy` is what makes the second exact
  for a convention several products share on purpose, and each claim carries its own `Docs`
  because it is a fact about somebody else's tool; leave a claim out rather than guess it.
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
- **Nothing barracks did not create is ever removed or replaced.** The check lives in
  `lease.InspectLink`: a recorded path is acted on only when it is still a symlink pointing
  at the exact store directory the lease recorded, and that target is inside the store.
  Revocation *and* upgrade relinking both go through it; a new mutation path must too.
  Everything else is kept and *reported* - silence is a bug. Directory pruning only touches
  empty directories the lease recorded creating. That covers the rollback inside
  `spawn.Engine.SpawnAll` too: it returns a `spawn.RollbackError` carrying every
  `lease.Report`, and `cli.Env.spawnAll` is the one choke point that prints them with
  `reportKept`.
- **`Lease.Links` is the undo record; `Lease.Sources` is provenance.** Only `Links` is ever
  acted on by revocation. `Sources` records which sources a spawn was materialised from so
  upgrade can re-attach one that momentarily exported no skills - the links that would
  otherwise prove it are exactly what such a source destroys. It gates *additions* only;
  no removal may ever consult it, or provenance would start widening what gets deleted.
- **The lease record is versioned, and a reader asks what a version *carries*.** Compare
  against the version the field landed in (`lease.provenanceSince`), never against
  `lease.FormatVersion` - bumping the format for one new field would otherwise declare every
  other field missing from every record on disk. A record too old for a field falls back to
  the behaviour it was written under; reading the absence as an empty set would turn every
  pre-existing lease into a mass deletion.
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
  `store.Locate` reads that path back, which is how upgrade tells which source and commit a
  spawned link belongs to - never the `Source` label on the lease record, which `--pin`
  rewrites. The path encodes a commit but never a *ref*, so one repo equipped at two refs
  gives every link two candidates; `upgrade.matchMove` ranks them by commit, then skill
  membership, then subpath depth rather than trusting `Locate` alone.
- **`upgrade --dry-run` and a real run must print the same body.** `upgrade.Plan` does every
  read and decision; `upgrade.Apply` only executes and records failures. Anything that
  decides at apply time breaks that guarantee. Plan does fetch into the store - a per-skill
  diff needs both trees - and nothing else.
- **The committed tier is the deliberate inverse of the personal one, and the two must never
  share a path.** `internal/garrison` writes real file content, registers *nothing* in
  `.git/info/exclude`, and takes no lease out. Its whole record is `barracks.lock` at the
  repository root - so no reaper pass can reach it, by construction rather than by policy
  (`TestReaperCannotRemoveAGarrison` fabricates dead leases over garrisoned paths to prove
  it). The mutual refusal is wired in `cli.Env.init`: `garrison.Engine` reads leases to
  refuse installing over a spawn, and `spawn.Engine.Committed` (satisfied by
  `garrison.Guard`) refuses the reverse. Never drop one half - a path both excluded from git
  as a symlink and committed as a file either hides the files from the team or dirties every
  checkout forever.
- **The lockfile's per-file digest is the committed tier's proof of ownership.** A symlink can
  be checked by where it points; a vendored file cannot, so `garrison.File.Digest` is what
  makes "barracks wrote this" decidable. Two questions must stay separate, and conflating them
  is a bug that refuses every legitimate update: *has somebody edited this* compares the file
  against the **previous** record, *does it need writing* compares against the **new** one.
  An update refuses on a locally edited file (`--force` overrides); a removal keeps it and
  reports it and offers no override, because nothing has to stay coherent after a removal. A
  file barracks never recorded is refused outright and `--force` never applies to it.
- **A garrison write is all-or-nothing.** `garrison.planWrite` decides everything before a
  byte is written; `writePlan.apply` moves each file it will replace into a temp directory
  inside the repository, so `undo` restores overwritten content byte for byte, and the
  lockfile is written last. Files on disk that no lockfile describes is the one state this
  tier must never be left in.
- **`upgrade` reaches both tiers, and the committed half runs last.**
  `cli.Env.planGarrisonUpgrades` reads before anything is applied, so `--dry-run` describes
  the committed tier from the same reads the real run acts on;
  `cli.Env.applyGarrisonUpgrades` calls `garrison.Engine.Reinstall` *after* `upgrade.Apply`
  has saved the loadout definitions, so files, lockfile, and definition all name the same
  commits. Keep that order. It compares the lockfile against the loadout's pins rather than
  against whether a source moved in this run - the same reason `upgrade` plans a move for
  every source - so a garrison an earlier run left behind is recoverable rather than
  stranded, and a no-op upgrade still leaves the repository clean. `upgrade` deliberately has
  no `--force`: a locally edited vendored file stops the committed half and barracks names
  `barracks garrison <loadout> --force` instead of growing a second spelling of it.
- **The voice is decoration and must never become information.** `internal/voice` owns the
  lines; which commands speak *is* the pool map, so a command with no entry is silent by
  construction rather than by an `if`. Every reason to stay quiet is gathered in
  `cli.Env.speak` - a new gate belongs there, never at a call site. It writes to stderr only,
  only when stdout is a character device (`cli.stdoutIsTerminal`), and only from cobra's
  `PersistentPostRun`, which cobra skips once `RunE` has returned an error: that is what makes
  "never on a failure" structural. Nothing about the escalation state may affect anything but
  which string is printed. **An escalated line may only express weariness at being asked
  again, never assert that the deployment is already in place** - barracks cannot stand behind
  the latter, because a spawn, a recall and a second spawn escalate too. For the same reason
  the escalation key carries *where* a command acted (`cli.Env.actedIn`, from the resolved
  repository root, with `--global` its own place): repository-scoped commands are `spawn`,
  `recall`, `garrison` and `run`, while `train`, `equip` and `upgrade` are not, and each says
  so for itself rather than having it inferred. That gating leaves the suite blind to it by
  default, so
  `internal/cli/voice_test.go` forces `Env.Tty` on deliberately and pins `Env.Rand`; a voice
  change proved only by a passing suite is not proved at all. Lines must be **original** -
  this is a public repo, so no verbatim
  Blizzard dialogue - and `voice.TestLinesMeetTheHouseStyle` holds the rest of the bar.

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
- `upgrade` reconciles spawns onto the commit each source is *pinned at*, not just onto a ref
  that moved. That is what lets a spawn it deliberately skipped (one held by a live process)
  be brought forward by a later run instead of being stranded at an old commit forever.
- A lease record can be rewritten *while its holder runs*: `upgrade --include-running`
  relinks a live spawn and saves the new targets. So `cli/run` re-reads the record at exit
  rather than revoking from the copy it captured at spawn time - the stale copy cannot prove
  the relinked symlink is barracks', so it would keep it and call it foreign. When the
  re-read fails, the copy is still safe to revoke from but incomplete, so the record is kept
  for the next reap. `TestRunRecallsAfterUpgradeRelinkedItsSpawn` guards this.
- When two loadouts spawn into one directory, the second records no created directories.
  `spawn.withInheritedDirs` copies the chain from the existing lease so whichever lease is
  revoked last can finish the cleanup. `garrison.inheritDirs` does the same across both
  tiers - it reads the other garrisons *and* the leases in scope - because a garrison and a
  spawn can legitimately share `.claude/skills` while owning different skills under it.
- `barracks.lock`'s `updated_at` is only bumped when the recorded entry otherwise changes
  (`garrison.sameRecord`). Without that, every `barracks garrison` would dirty the repository
  and the one signal that matters in review - did anything actually change - would be
  unreadable.

## Maintaining this file

Keep this file for knowledge useful to almost every future agent session in this project.
Do not repeat what the codebase already shows; point to the authoritative file or command instead.
Prefer rewriting or pruning existing entries over appending new ones.
When updating this file, preserve this bar for all agents and keep entries concise.
