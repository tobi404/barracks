# barracks

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

---

## Install

**Requires:** Go 1.24+ and `git` on your PATH.

```bash
go install github.com/tobi404/barracks@latest
go install github.com/tobi404/barracks/cmd/brk@latest   # short alias
```

Or from a clone:

```bash
make install
```

Both binaries are the same program; `brk` is there because you will type it a lot.

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

Shows every loadout you have trained, with its sources and skill count.

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
```

The skills exist for exactly as long as the process does. Ctrl-C is forwarded to the
command and the loadout is recalled as usual. If barracks itself is killed outright, the
next barracks command cleans up.

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
3. Whichever agents already have a configuration directory here (`.cursor/`, `.claude/`,
   and so on). A global spawn looks at the matching directory in your home instead.
4. The default target, `claude`.

When barracks decides for you - case 3 or 4 - it says so before it spawns, so a spawn never
lands somewhere unexpected in silence.

### Adding a target

The mapping lives in one declarative table (`internal/target/target.go`). Paths, aliases,
detection markers, and the documentation each path came from are all fields on an entry;
no command logic knows an agent-specific path. Adding another agent is a new entry in that
table, not a code change - `TestAddingATargetIsDataNotCode` proves it by driving a whole
spawn/recall lifecycle through an agent invented entirely in the test.

---

## Development

```bash
make build     # build both binaries into ./bin
make test      # go test -race ./...
make cover     # coverage report
make lint      # gofmt check and go vet
```

Tests never touch the network: they build local git repository fixtures on disk and point
sources at those paths.

---

## Not built yet

The committed/shared tier with a lockfile, `barracks upgrade`, and release packaging are
separately queued.
