# barracks

[![CI](https://github.com/tobi404/barracks/actions/workflows/ci.yml/badge.svg)](https://github.com/tobi404/barracks/actions/workflows/ci.yml)

Spawn agent skill loadouts into any repo - permanently, per project, or just for one session.

There are thousands of agent skills published across git repos. You want one set for a
throwaway session, another scoped to a project forever, and a third available everywhere.
Today that means hand-copying directories and losing track of what came from where.

barracks makes a skill set a named, versioned, spawnable unit.

```bash
barracks train frontend --target claude,cursor             # define a loadout
barracks equip frontend gh:owner/skills#main:skills        # attach a source
barracks spawn frontend                                    # materialise it here
barracks run frontend -- claude                            # or just for one session
```

---

## Where to start

| I want to… | Go to |
|---|---|
| Install it | [Install](#install) |
| Learn the commands | [Commands](#commands) |
| Understand source syntax | [Source syntax](#source-syntax) |
| Know what it touches on disk | [What it does to your repo](#what-it-does-to-your-repo) |
| See which agents are supported | [Targets](#targets) |
| Choose which agents a loadout installs into | [Choosing targets](#choosing-targets) |
| Add support for another agent | [Adding a target](#adding-a-target) |
| Work on barracks itself | [Development](#development) |
| Cut a release | [Releasing](#releasing) |

---

## Install

**Requires:** `git` on your PATH.

### Homebrew (macOS)

```bash
brew install tobi404/tap/barracks
```

Installs both `barracks` and `brk`. Upgrade with `brew upgrade barracks`.

### Go

Any platform with Go 1.24+:

```bash
go install github.com/tobi404/barracks@latest
go install github.com/tobi404/barracks/cmd/brk@latest   # short alias
```

### Direct download

Prebuilt binaries for macOS and Linux, `arm64` and `amd64`, are attached to every
[release](https://github.com/tobi404/barracks/releases), with a `checksums.txt` beside
them. Each archive contains both binaries:

```bash
tar xzf barracks_<version>_<os>_<arch>.tar.gz
install barracks brk /usr/local/bin/
```

Verify what you downloaded before installing it:

```bash
sha256sum --check --ignore-missing checksums.txt   # shasum -a 256 -c on macOS
```

### From a clone

```bash
make install
```

Both binaries are the same program; `brk` is there because you will type it a lot.
`barracks --version` reports the release it came from, the commit it was built at, and
when.

Homebrew on Linux is not an install path: barracks ships as a Homebrew *cask*, and casks
are macOS-only. On Linux use `go install` or the tarball.

---

## Commands

### `barracks train <name>`

Defines a new loadout. A loadout is a named bundle of agent skills. Training one only
creates its definition - it has no skills until you equip it, and it touches no repo
until you spawn it.

```bash
barracks train frontend
barracks train review --description "Skills for reviewing pull requests"
barracks train editor --target cursor --target windsurf
```

`--target` declares which agents the loadout installs into - see
[Choosing targets](#choosing-targets). Leave it out and barracks decides per repository.

The definition is a plain YAML file under `~/.barracks/loadouts/`. Open and edit it by
hand whenever you like.

### `barracks assign <loadout> [target...]`

Changes which agents a loadout installs into, after training.

```bash
barracks assign frontend                  # show the current declaration
barracks assign frontend claude cursor    # install into both from now on
barracks assign frontend --auto           # clear it and detect per repository
```

### `barracks equip <loadout> <source>`

Attaches a git source of skills to a loadout. The source is resolved to a concrete commit,
fetched once into a shared store, and scanned for skills - any directory containing a
`SKILL.md`. The commit is pinned in the definition, so a spawn reproduces the same skills
even after the branch moves on.

```bash
barracks equip frontend gh:owner/skills
barracks equip frontend gh:owner/monorepo#main:packages/skills
barracks equip frontend gh:owner/skills --only 'react-*,css-*'
barracks equip frontend gh:owner/skills --except deprecated-helper
```

`--only` and `--except` take glob patterns, so you can pull three skills out of a large
repo rather than all of them.

Equipping a source a loadout already has re-pins it to the newly resolved commit instead of
attaching a second copy. A different `#ref` or subpath is a different source and is kept
alongside.

### `barracks spawn <loadout>`

Materialises the loadout into the skills directory of every agent it installs into. Skills
are symlinked from the shared store, so spawning is instant and every repo on your machine
shares one copy on disk.

```bash
barracks spawn frontend                    # until you recall it
barracks spawn frontend --for 2h           # until the clock runs out
barracks spawn frontend --global           # into your user-level skills directories
barracks spawn frontend --target opencode  # just this once, for a different agent
```

A loadout declaring two agents reaches both in this one command. A `--target` given here
applies to this spawn only and never changes what the loadout declares. If any one target
fails, the whole spawn is rolled back - a two-agent spawn is one action, so it never half
happens.

### `barracks recall <loadout>`

Removes a spawned loadout, leaving the repo exactly as it was.

```bash
barracks recall frontend
barracks recall frontend --target cursor   # leave the other agents alone
barracks recall --all
```

One recall undoes one spawn: a loadout that went into two agents comes out of both. Narrow
that with `--target` when you want to keep one.

Recall removes only the symlinks the spawn recorded, and only after confirming each is
still a symlink pointing into the barracks store. Anything else - a real file, a directory,
a symlink you re-pointed - is left alone and reported.

### `barracks deployed`

Shows what is currently spawned here, which agent each one went into, and how each one
ends. The same loadout spawned into two agents shows up once per agent.

```bash
barracks deployed
barracks deployed --target cursor
barracks deployed --global       # your user-level skills directories
barracks deployed --everywhere   # every live spawn on this machine
```

### `barracks list`

Shows every loadout you have trained, with its sources, skill count, and the agents it
installs into.

```bash
barracks list
barracks list --verbose
```

### `barracks run <loadout> -- <cmd...>`

Spawns a loadout, runs a command with those skills available, and recalls the loadout the
moment the command exits.

```bash
barracks run frontend -- claude
barracks run review -- claude -p "review this diff"
barracks run frontend --target cursor -- cursor-agent
```

The skills exist for exactly as long as the process does. Ctrl-C is forwarded to the
command and the loadout is recalled as usual. If barracks itself is killed outright, the
next barracks command cleans up.

When the command is an agent barracks knows, that agent is equipped even if this repository
shows no sign of it - `barracks run frontend -- claude` reaches Claude Code whether or not
`.claude/` exists here. The program is matched on its base name, so `/usr/local/bin/claude`
counts too; anything else, a wrapper or `sh -c ...` included, falls back to the ordinary
rules with no guessing. See [Choosing targets](#choosing-targets).

### Also available

`barracks disband <name>` deletes a loadout definition, refusing while it is still
deployed. `barracks targets` lists the agents barracks can deploy to and marks the ones
this repository is already set up for.

---

## Source syntax

```
gh:owner/repo                      GitHub shorthand
github.com/owner/repo              any host
https://github.com/owner/repo.git
git@github.com:owner/repo.git
./path/to/local/repo               a repository on disk
```

Any form takes a `#ref` suffix to pin a branch, tag, or commit, and a `#ref:subpath`
suffix to scan only part of the repo:

```
gh:owner/repo#v1.2.0
gh:owner/repo#main:skills
```

A resolved source is always pinned to a concrete commit SHA in the loadout definition, so
a spawn is reproducible even when the ref moves.

---

## What it does to your repo

**Spawning creates symlinks**, never copies. Two loadouts sharing a source cost one fetch
and one copy on disk, however many repos they are spawned into.

**Spawning leaves `git status` clean.** Created paths are registered in
`.git/info/exclude`, never in the committed `.gitignore`. Recalling removes that block and
restores the file byte for byte - and only deletes the file if barracks was what created
it. Outside a git repository barracks still spawns into the working directory; it just
says there was no exclude file to register in.

**Lifetimes are governed by leases.** A spawn writes a record describing exactly what it
created and when it should end:

| Kind | Ends when |
|---|---|
| `manual` | you run `barracks recall` |
| `deadline` | the clock passes (`--for 2h`) |
| `process` | the process from `barracks run` exits |

**Reaping is lazy.** Every barracks command runs a cleanup pass before its own work:
expired deadlines are revoked, and process leases whose owner is gone are revoked. There is
no daemon and no shell integration to install. A process lease records the owner's identity,
not just its PID, so a recycled PID can never keep a dead lease alive.

**barracks never deletes what it did not create.** A lease records every path it made.
Revoking removes only those paths, and only after confirming each is still a symlink
resolving into the barracks store. Your own `.claude/skills/my-skill/` is safe, and if
barracks finds one of its paths taken over it says so rather than failing silently.

### On-disk layout

```
~/.barracks/
├── loadouts/       # one hand-editable YAML file per loadout
├── store/          # <host>/<owner>/<repo>@<commit>/ - fetched once, shared by everything
├── mirrors/        # bare git mirrors, so a repo is cloned at most once
└── leases/         # one record per live spawn
```

`XDG_CONFIG_HOME` and `XDG_DATA_HOME` are honoured when set; `BARRACKS_HOME` overrides
everything.

---

## Targets

A target is one agent's skills directory. Every supported agent reads the same artifact
barracks produces - a directory containing a `SKILL.md` - so nothing is translated or
rewritten on the way in.

| Target | In a repo | Global | Read by |
|---|---|---|---|
| `claude` (default) | `.claude/skills/` | `~/.claude/skills/` | [Claude Code](https://code.claude.com/docs/en/skills) |
| `agents` (alias `codex`) | `.agents/skills/` | `~/.agents/skills/` | [Codex](https://learn.chatgpt.com/docs/build-skills), opencode, Cursor |
| `cursor` | `.cursor/skills/` | `~/.cursor/skills/` | [Cursor](https://cursor.com/docs/context/skills) |
| `opencode` | `.opencode/skills/` | `$XDG_CONFIG_HOME/opencode/skills/` | [OpenCode](https://opencode.ai/docs/skills) |
| `windsurf` | `.windsurf/skills/` | `~/.codeium/windsurf/skills/` | [Windsurf](https://docs.devin.ai/desktop/cascade/skills) |

`barracks targets` prints this table for your machine, with the resolved global path, the
documentation each path was read from, and which agents are already configured in the
repository you are standing in.

`agents` is the cross-agent convention rather than one product: Codex reads `.agents/skills`
from the working directory up to the repository root and `~/.agents/skills` for the user,
and opencode and Cursor read the same two locations. One spawn there reaches all of them,
which is why Codex has no separate entry.

### Choosing targets

The choice belongs to the loadout, not to a machine-wide setting - one loadout can be a
Cursor loadout while another is a Claude Code one.

```bash
barracks train editor --target cursor --target windsurf   # at training time
barracks assign editor cursor windsurf                    # or change it later
barracks assign editor --auto                             # or hand it back to detection
```

The declaration is a `targets:` list in the loadout's YAML file, so it can also be edited
by hand. At spawn time barracks takes the first of these that applies:

1. `--target` given on this invocation - for this spawn only, never written back.
2. The loadout's own declaration.
3. The agent `barracks run` is about to launch, if the command names one barracks knows,
   together with whichever agents already have a configuration directory here (`.cursor/`,
   `.claude/`, and so on). A global spawn looks at the matching directory in your home
   instead.
4. The default target, `claude`.

When barracks decides for you - case 3 or 4 - it says so before it spawns, so a spawn never
lands somewhere unexpected in silence.

Only `barracks run` contributes case 3's first half, because it is the only command that
knows which agent is about to read the skills. It never widens an explicit choice: if
`--target` or the loadout's declaration installs nowhere the agent being launched reads,
barracks warns and installs where you asked anyway. Targets that share a directory count -
`--target agents -- opencode` installs somewhere opencode does read, so nothing is warned
about.

### Adding a target

The mapping lives in one declarative table (`internal/target/target.go`). Paths, aliases,
detection markers, the program names of the agent's own CLI, which other agents read the
same directory, and the documentation each of those came from are all fields on an entry;
no command logic knows an agent-specific path or program name. Adding another agent is a
new entry in that table, not a code change - `TestAddingATargetIsDataNotCode` proves it by
driving a whole spawn/recall lifecycle, an argv match, and a shared-read claim through an
agent invented entirely in the test.

Two questions the table answers are kept apart on purpose. *Which target does this agent
get?* decides where files land. *Would this agent see what is already going there?* decides
only whether `barracks run` prints its warning - it is what stops the warning firing on a
correct `barracks run frontend --target agents -- opencode`. Each shared-read claim carries
its own documentation link, shown by `barracks targets`, because it is a fact about
somebody else's tool and will drift like any other.

---

## Development

```bash
make build            # build both binaries into ./bin
make test             # go test -race ./...
make cover            # coverage report
make cover-check      # coverage report, failing under 80%
make lint             # gofmt check and go vet
make fmt-check        # gofmt check on its own, exactly as CI runs it
make fmt              # rewrite the tree with gofmt, what fmt-check tells you to run
make vet              # go vet on its own, exactly as CI runs it
make golangci         # the full linter suite, at the version CI pins
make release-check    # validate .goreleaser.yaml
make release-snapshot # build every platform into ./dist and publish nothing
```

Tests never touch the network: they build local git repository fixtures on disk and point
sources at those paths.

Every push to `main` and every pull request runs the same checks in GitHub Actions
(`.github/workflows/ci.yml`): build, `go vet`, `gofmt`, `go test -race` with an 80%
coverage floor, and `golangci-lint`. The test job runs on both Linux and macOS, because
`internal/proc` decides whether a lease's owner is still alive differently per operating
system. No secrets are needed to run it.

---

## Releasing

A release is one push of one tag. Everything else - building four platforms, attaching
archives and checksums to a GitHub release, and publishing the Homebrew cask - happens in
`.github/workflows/release.yml`, which runs GoReleaser at the version pinned in
`.goreleaser-version`.

### Tag format

`vMAJOR.MINOR.PATCH`, semver, always with the leading `v`:

```
v1.0.0        a release
v1.2.0-rc.1   a prerelease - any suffix marks the GitHub release as a prerelease
```

The workflow triggers on `v*` and nothing else. The tag is the version: `barracks
--version` reports it, and the Homebrew cask points at that tag's archives.

```bash
git tag -a v1.0.0 -m "v1.0.0"
git push origin v1.0.0
```

Never move or re-push a tag. Homebrew and the checksums file both pin to it; publish a
new patch version instead.

### One-time setup

The tap repository [`tobi404/homebrew-tap`](https://github.com/tobi404/homebrew-tap)
exists - public, default branch `main`. GoReleaser commits `Casks/barracks.rb` into it on
each release, which is what makes `brew install tobi404/tap/barracks` resolve. Nothing
needs to be done to it by hand.

One thing is still outstanding, and it cannot be created from this repository:

**The repository secret `HOMEBREW_TAP_GITHUB_TOKEN`.** The workflow's built-in
`GITHUB_TOKEN` is scoped to this repository alone and cannot push to the tap, so the
cask needs a token of its own:

- Create a **fine-grained personal access token** (GitHub → Settings → Developer settings
  → Personal access tokens → Fine-grained tokens).
- Resource owner `tobi404`, repository access **only** `tobi404/homebrew-tap`.
- Repository permission **Contents: Read and write**. Nothing else - no access to
  `tobi404/barracks`, no organisation or account permissions.
- Add it to this repository as a secret named exactly `HOMEBREW_TAP_GITHUB_TOKEN`
  (Settings → Secrets and variables → Actions → New repository secret).

The release workflow checks for it before it builds anything and fails immediately if it
is missing, naming the secret. A tag pushed without it produces no release at all, rather
than a release nobody can `brew install`.

### Verifying without releasing

```bash
make release-check      # is .goreleaser.yaml valid and free of deprecated keys?
make release-snapshot   # build all four platforms, archive, checksum, render the cask
```

`make release-snapshot` writes to `./dist` and publishes nothing: the archives, the
`checksums.txt`, and `dist/homebrew/Casks/barracks.rb` are exactly what a real tag would
produce, minus the upload.

---

## Not built yet

The committed/shared tier with a lockfile and `barracks upgrade` are separately queued.
