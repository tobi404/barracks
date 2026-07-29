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

Loadouts are editable after the fact: `barracks strip` takes a source back out of one and out
of everywhere it is deployed, and `barracks rename` renames it without breaking a single
deployment.

---

## Install

**Requires:** `git` on your PATH.

```bash
brew install tobi404/tap/barracks                       # macOS
go install github.com/tobi404/barracks@latest           # any platform with Go 1.25+
```

Homebrew installs both `barracks` and `brk`, and installs `git` if you do not have it -
barracks shells out to git for every fetch and every resolve; upgrade with
`brew upgrade barracks`. With `go install`, add
`go install github.com/tobi404/barracks/cmd/brk@latest` for the short alias. Both binaries
are the same program; `brk` is there because you will type it a lot. `barracks --version`
reports the release it came from, the commit it was built at, and when.

Prebuilt archives and building from a clone: [Development](./docs/development.md#installing-from-source-or-from-a-release-archive).

---

## Personal or shared

One loadout, three ways to put it somewhere.

- **[`barracks spawn`](./docs/commands.md#barracks-spawn-loadout) - yours.** Symlinks into
  your own store, kept out of git via `.git/info/exclude`, gone when the lease ends. Use it
  when the skill set is your own preference: nobody else has to agree, and nothing appears in
  anyone's diff.
- **[`barracks garrison`](./docs/commands.md#barracks-garrison-loadout) - your team's.** Real
  file content, **committed**, with a `barracks.lock` recording every source, commit and file
  digest. Everyone who clones gets the same skills without installing barracks at all, and
  changing the set is a pull request somebody reviews.
- **[`barracks run`](./docs/commands.md#barracks-run-loadout----cmd) - one session.** Spawned
  for the length of a command and recalled the moment it exits.

The two tiers never share a path: garrisoning over a spawn is refused, and so is spawning
onto committed paths. Recall one first; the error says which. Different loadouts in one
repository are fine, and so is spawning your own loadout beside a garrisoned one, as long as
they do not want the same skill name in the same agent directory.

---

## Where to start

| I want to… | Go to |
|---|---|
| Learn the commands | [Commands](./docs/commands.md) |
| See everything on one screen | [The roster](./docs/roster.md) |
| Know what it touches on disk | [What it does to your repo](./docs/on-disk.md) |
| See which agents are supported, or add one | [Targets](./docs/targets.md) |
| Make it quieter | [Progress and voice](./docs/output.md) |
| Work on barracks itself, or cut a release | [Development](./docs/development.md) |
| Know the terms of use | [License](#license) |

---

## Documentation

| Doc | Covers |
|---|---|
| [`docs/commands.md`](./docs/commands.md) | Every command and flag, source syntax, what is not built yet |
| [`docs/roster.md`](./docs/roster.md) | The full-screen screen a bare `barracks` opens, and its keys |
| [`docs/targets.md`](./docs/targets.md) | Supported agents, how targets are chosen, adding another |
| [`docs/on-disk.md`](./docs/on-disk.md) | What barracks writes, leases and reaping, the on-disk layout |
| [`docs/output.md`](./docs/output.md) | The progress indicator, the flavor line, and turning both off |
| [`docs/development.md`](./docs/development.md) | Make targets, CI, installing from source, releasing |

---

## Contributing

Issues and pull requests are welcome at
[tobi404/barracks](https://github.com/tobi404/barracks). Before opening one, run
`make lint` and `make cover-check` - CI runs the same targets, and a new package without
its own tests fails the 80% coverage floor for everybody. See
[Development](./docs/development.md) for the full set.

---

## License

MIT - see [`LICENSE`](./LICENSE). Every release archive carries a copy.
