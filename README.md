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
barracks spawn frontend                                    # materialise it here, for you
barracks garrison frontend                                 # or commit it, for the whole team
barracks run frontend -- claude                            # or just for one session
```

---

## Where to start

| I want to… | Go to |
|---|---|
| Install it | [Install](#install) |
| Learn the commands | [Commands](#commands) |
| Keep skills up to date | [`barracks upgrade`](#barracks-upgrade-loadout) |
| Share one skill set with my team | [Personal or shared](#personal-or-shared) |
| Check a checkout matches what was committed | [`barracks inspect`](#barracks-inspect) |
| Understand source syntax | [Source syntax](#source-syntax) |
| Know what it touches on disk | [What it does to your repo](#what-it-does-to-your-repo) |
| See which agents are supported | [Targets](#targets) |
| Choose which agents a loadout installs into | [Choosing targets](#choosing-targets) |
| Add support for another agent | [Adding a target](#adding-a-target) |
| Work on barracks itself | [Development](#development) |
| Cut a release | [Releasing](#releasing) |
| Know the terms of use | [License](#license) |

---

## Install

**Requires:** `git` on your PATH.

### Homebrew (macOS)

```bash
brew install tobi404/tap/barracks
```

Installs both `barracks` and `brk`, and installs `git` if you do not have it - barracks
shells out to git for every fetch and every resolve. Upgrade with `brew upgrade barracks`.

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
alongside. To move a whole loadout forward later, and every repo it is spawned into with it,
use [`barracks upgrade`](#barracks-upgrade-loadout).

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

A spawn is yours alone: symlinks into your own store, kept out of git, gone when the lease
ends. To give a whole team one skill set, [garrison it](#barracks-garrison-loadout) instead.

### `barracks garrison [<loadout>]`

Stations a loadout in this repository permanently: **real skill files, committed**, plus a
`barracks.lock` at the repository root recording exactly which sources and commits produced
them.

```bash
barracks garrison frontend                 # commit it, or bring it onto new pins
barracks garrison frontend --target cursor --target claude
barracks garrison                          # put every garrison in barracks.lock back
```

Commit the skill files and `barracks.lock` together. They are deliberately **not** in
`.git/info/exclude` - the whole point is that git tracks them - so `barracks garrison`
leaves the working tree dirty on purpose, with a diff to review and commit. Once committed,
`git status` is clean and stays clean.

A teammate clones the repository and their agent sees the skills immediately: no barracks
installed, nothing to set up, no network. Whether they have barracks decides only whether
they can *check* and *change* the skill set, never whether they can use it.

**Running it again is how an update happens.** The vendored files and `barracks.lock` are
rewritten together, so a skill update arrives as one reviewable diff in a pull request
instead of happening invisibly on each machine. Nothing is written until every check has
passed, and a failure part-way through is rolled back - the files and the lockfile can never
be left disagreeing. A run that changes nothing leaves `barracks.lock` byte-identical.

**A vendored file you have edited stops the update**, naming the file. barracks will not
discard your edit, and will not leave `barracks.lock` claiming content the file does not
have:

```
barracks: vendored file modified locally: .claude/skills/react/SKILL.md
these files have been edited since they were committed. Restore them
(git checkout -- <path>) to keep the edit out of the way, or pass --force to
replace them with the recorded source content
```

`--force` replaces them. A file barracks *never wrote* is refused outright and `--force`
does not apply to it: force means "discard my edits to barracks' files", never "delete a
file barracks has no record of".

With no loadout named, every garrison `barracks.lock` records is materialised again from the
lockfile alone. That is the repair path, and it needs no loadout trained on the machine
running it - the lockfile carries every source and commit it needs.

### `barracks inspect`

Verifies that the committed skill files in this repository are exactly what `barracks.lock`
says they should be. It changes nothing, and exits non-zero on any mismatch, so it can gate
a build.

```bash
barracks inspect
```

```
frontend  3 skills, 5 files  [claude, cursor]  3 problems
  ! .claude/skills/css/SKILL.md: missing
  ! .claude/skills/react/SKILL.md: modified
  ! .claude/skills/react/notes.md: not in the lockfile
```

`barracks.lock` records a digest per file, so a file edited by hand, dropped in a rebase,
half-merged, or added inside a vendored skill directory is reported rather than silently
accepted. `barracks garrison` is what puts a drifted checkout back.

A note that the loadout is pinned somewhere newer than `barracks.lock` is **not** a
mismatch - the files and the lockfile agree, which is exactly what a teammate cloning the
repository should get. It means the committed pins are behind the loadout, and bringing them
forward is a commit like any other.

### `barracks upgrade [<loadout>...]`

Re-resolves each source's declared ref, fetches whatever it now points at, and
relinks every live spawn onto the new commit. With no loadout named, every
loadout is upgraded.

```bash
barracks upgrade
barracks upgrade frontend --dry-run
barracks upgrade frontend --pin
```

Each source is reported with its old and new commits and a real per-skill diff:

```
frontend
  github.com/owner/skills#main  940f8b38 -> f6183d1c
    + hooks
    ~ react
    - legacy
    (1 skill unchanged)
  /home/you/work/.claude/skills  [manual]
    + hooks
    - legacy
    2 skills relinked
```

`~` means the skill's content changed, established by fingerprinting both
trees - not merely that the repository commit moved. A commit that leaves every
skill byte-identical says `no skill changed` rather than claiming an update. A
source pinned to an exact commit SHA has nothing to resolve and is reported as
pinned, never silently refetched.

Relinking obeys the same rule as recall: a spawned path is repointed or removed
only while it is still a symlink into the barracks store. A file or directory of
your own that has taken its place is left alone and reported. A skill that no
longer exists upstream has its symlink removed rather than left dangling, and
new skills are registered in `.git/info/exclude`, so `git status` stays clean
before and after.

Upgrading moves a spawn forward along the sources it was made from. Membership
is recorded per **repository and subpath**, never per ref, so re-equipping the
same repository and subpath at another ref counts as the *same* source and its
skills do land in a spawn that already exists:

```bash
barracks equip frontend gh:owner/skills#main:skills
barracks spawn frontend
barracks equip frontend gh:owner/skills#v1:skills
barracks upgrade frontend       # the #v1 entry's skills land in that spawn
```

A source at a different repository, or at a different subpath of the same
repository, equipped *after* a repo was spawned into is not materialised there
by an upgrade - run `barracks spawn` again to pick that one up.

The ref is left out of the match on purpose: `--pin` rewrites a source's
declared ref, and a ref-sensitive comparison would stop recognising a spawn's
own source the moment you pinned it, silently skipping every later addition.
The repository and subpath are the parts `--pin` cannot rewrite.

| Flag | Effect |
|---|---|
| `--dry-run` | Report exactly what would change and change nothing |
| `--pin` | Record the newly resolved commit as the source's declared ref |
| `--include-running` | Also relink spawns held by a running process |

`--dry-run` resolves and fetches into the shared store, because comparing two
commits is the only way to tell you what actually changed. It writes nothing
else: no loadout definition, no symlink, no lease, no exclude file. Store entries
are content-addressed and shared, so that fetch is invisible and the second run
reuses it - which is why the dry run's report and the real run's report are the
same text.

**A running session keeps the skills it started with.** A spawn whose lease is
held by a live `barracks run` process is not relinked; barracks says so and moves
on. Changing skill directories underneath a session that has already read them
is exactly the kind of surprise this tool exists not to produce. The session's
lease is revoked when it exits, and the next `upgrade` or `spawn` brings that
directory forward - a skipped spawn is never stranded. Pass `--include-running`
to relink it now anyway.

barracks skips only what it can *prove* is live. A `manual` or `deadline` spawn
may well have an agent session sitting on it, and barracks has no way to know,
so those are relinked. If you are mid-session in a repo with a manual spawn,
`--dry-run` first.

Old store entries are left behind after an upgrade. Disk is cheap, and deleting
a commit something might still be pointing at is not a decision to make
implicitly.

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

A garrisoned loadout is recalled by the same command, because it is deployed here just as
much as a spawn is. Its committed files are removed only where they still match the digest
`barracks.lock` recorded; a file edited since it was committed is kept and reported, and so
is anything barracks never put there. A garrison is one committed unit with no per-target
record, so `--target` and `--global` leave it alone rather than half-removing it.

```
recalled the frontend garrison (4 files removed, barracks.lock updated)
! left in place: .claude/skills/react/reference.md - edited since it was committed - your change is kept
! left in place: .claude/skills/react/handwritten.md - barracks has no record of putting it there
```

The removal is a change to tracked files, so it appears in `git status` for review like any
other - and is recoverable with `git checkout` if it was not what you meant.

### `barracks deployed`

Shows what is currently spawned here, which agent each one went into, and how each one
ends. The same loadout spawned into two agents shows up once per agent.

```bash
barracks deployed
barracks deployed --target cursor
barracks deployed --global       # your user-level skills directories
barracks deployed --everywhere   # every live spawn on this machine
```

Anything garrisoned here is listed too, marked `[committed]`, because it is deployed in this
repository as much as a spawn is - more permanently, in fact. It is read from
`barracks.lock` rather than from a lease, so `--everywhere` still only knows about the
repository you are standing in: barracks keeps no machine-wide index of garrisons.

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
deployed or garrisoned here. `barracks targets` lists the agents barracks can deploy to and
marks the ones this repository is already set up for.

---

## Personal or shared

There are two ways to put a loadout in a repository, and they are for different jobs.

|  | `barracks spawn` | `barracks garrison` |
|---|---|---|
| What lands on disk | Symlinks into your store | Real file content |
| In git | Excluded via `.git/info/exclude` | **Committed**, with `barracks.lock` |
| Survives a clone | No | Yes |
| Needs barracks to use | Yes | **No** |
| Who gets it | You, on this machine | Everyone who clones |
| Lifetime | A lease: `manual`, `--for`, or `barracks run` | Until someone recalls it, in a commit |
| Reaped automatically | Yes, when the lease ends | **Never** - it has no lease |
| Disk cost | One copy per machine | One copy per repository |
| Updating | Happens on each machine | A reviewable diff in a pull request |

**Spawn** when the skill set is your own preference: a set you like in every repo you touch,
a throwaway session, an experiment. Nobody else has to agree with you, and nothing appears
in anyone's diff.

**Garrison** when the skill set belongs to the repository: the review checklist this codebase
expects, the house conventions, the skills every agent working here should have. Everyone
gets the same skills at the same commits, a new joiner needs nothing installed, and changing
the set is a pull request somebody reviews.

The two never share a path. Garrisoning over a personal spawn is refused, and so is spawning
onto committed paths - a path registered both ways would either hide the committed files from
the team or leave every checkout dirty forever. Recall one first; the error says which.

Different loadouts in one repository are fine, and so is spawning your own loadout beside a
garrisoned one, as long as they do not want the same skill name in the same agent directory.

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

**Garrisoning is the exact opposite, deliberately.** It writes real file content and
registers nothing in `.git/info/exclude`, because a committed skill set that git ignores is
worthless: the install would look perfect and the commit would carry nothing. barracks checks
for that and warns if a `.gitignore` rule would swallow the files. A garrison never takes a
lease out, so no lifetime governs it and no reaper pass can reach it - `barracks.lock` in the
repository is the whole record, and it travels with the repository rather than with the
machine.

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
Upgrading applies the same check before it repoints or removes anything.

The committed tier obeys the same rule with a different proof. A symlink can be checked by
where it points; a vendored file cannot, so `barracks.lock` records a digest for each one and
that digest is the proof. A file whose content still matches is barracks' own and may be
removed. Anything else - edited, replaced, or never recorded at all - is kept and reported.

### On-disk layout

```
~/.barracks/
├── loadouts/       # one hand-editable YAML file per loadout
├── store/          # <host>/<owner>/<repo>@<commit>/ - fetched once, shared by everything
├── mirrors/        # bare git mirrors, so a repo is cloned at most once
└── leases/         # one record per live spawn - never for a garrison
```

A garrison keeps nothing here. Its record is `barracks.lock`, committed at the root of the
repository it belongs to:

```
your-repo/
├── barracks.lock       # generated: sources, commits, and a digest per vendored file
└── .claude/skills/     # real, committed skill directories
```

`XDG_CONFIG_HOME` and `XDG_DATA_HOME` are honoured when set; `BARRACKS_HOME` overrides
everything.

---

## Targets

A target is one agent's skills directory. Every supported agent reads the same artifact
barracks produces - a directory containing a `SKILL.md` - so nothing is translated or
rewritten on the way in, and the same table serves both a personal spawn and a garrison.

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

`barracks garrison` follows the same order, with one addition between 2 and 3: a loadout this
repository has already garrisoned keeps the agents `barracks.lock` records for it. An update
must never quietly stop installing into an agent the repository has committed files for, and
it says which agents it is reusing and why.

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

A prerelease still publishes its GitHub release with archives and checksums, but the
Homebrew cask is not pushed, so `brew install tobi404/tap/barracks` keeps serving the
latest stable version.

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

The release workflow checks it before it builds anything and fails immediately if the
secret is missing, or if the token no longer reaches the tap with write access - an
expired or revoked token is caught here, not halfway through publishing. A tag pushed
without a working token produces no release at all, rather than a release nobody can
`brew install`. The token expires, so re-issuing it is part of releasing.

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

Store garbage collection is separately queued. `barracks upgrade` and the committed/shared
tier with a lockfile both landed.

---

## License

MIT - see [`LICENSE`](./LICENSE). Every release archive carries a copy.
