# Commands

Every barracks command, what it writes, and the flags it takes - plus [source
syntax](#source-syntax) and [what is not built yet](#not-built-yet). For the full-screen
alternative to typing these, see [the roster](./roster.md). Back to the
[README](../README.md).

---

## `barracks train <name>`

Defines a new loadout. A loadout is a named bundle of agent skills. Training one only
creates its definition - it has no skills until you equip it, and it touches no repo
until you spawn it.

```bash
barracks train frontend
barracks train review --description "Skills for reviewing pull requests"
barracks train editor --target cursor --target windsurf
```

`--target` declares which agents the loadout installs into - see
[Choosing targets](./targets.md#choosing-targets). Leave it out and barracks decides per
repository.

The definition is a plain YAML file under `~/.barracks/loadouts/`. Open and edit it by
hand whenever you like.

## `barracks assign <loadout> [target...]`

Changes which agents a loadout installs into, after training.

```bash
barracks assign frontend                  # show the current declaration
barracks assign frontend claude cursor    # install into both from now on
barracks assign frontend --auto           # clear it and detect per repository
```

## `barracks equip <loadout> <source>`

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
use [`barracks upgrade`](#barracks-upgrade-loadout); to take one back out again, use
[`barracks strip`](#barracks-strip-loadout-source).

## `barracks strip <loadout> <source>`

Detaches a source from a loadout, and takes the skills it contributed back out of
every live spawn of that loadout and out of this repository's garrison. The inverse of
`barracks equip`.

```bash
barracks strip frontend gh:owner/skills
barracks strip frontend github.com/owner/monorepo#main:packages/skills
```

Everything the loadout's other sources provide stays exactly where it is, and a skill
another equipped source also provides is **handed over to it** rather than removed. The
report says which is which, with the same marks the rest of barracks uses:

```text
stripped github.com/owner/skills#main:skills from frontend
  - css
  ~ react (still provided by github.com/other/skills#main:skills)
```

Name the source however you like - the shorthand you equipped it with, or the full label
`barracks list` prints. The ref is ignored when it does not match, because
[`--pin`](#barracks-upgrade-loadout) rewrites it. A spelling that could mean two equipped
sources is refused rather than guessed at:

```text
barracks: source matches more than one equipped entry: github.com/owner/skills could mean
github.com/owner/skills#main:skills or github.com/owner/skills#v1:skills; name one of them exactly
```

Removal obeys the same rule as [recall](#barracks-recall-loadout): a spawned path is removed
only while it is still a symlink into the barracks store, and a committed file only while it
still matches the digest `barracks.lock` recorded. Anything else is kept and reported. A
vendored file you have edited stops the whole thing; `--force` replaces it.

Stripping the **last** source is allowed and leaves an empty loadout - it is not disbanded, so
you can equip it with something else. Its spawns are recalled and its garrison removed,
because there is nothing left for them to hold.

A spawn held by a live `barracks run` session refuses the strip rather than being skipped.
An upgrade can leave a session alone because the source is still equipped and the next run
picks it up again; a stripped source is gone from the definition, so nothing would ever come
back for the links it left. Wait for the session to exit, or recall it first.

Only *this* repository's garrison is reached - `barracks.lock` travels with the repository
rather than with the machine, which is the same limit `barracks upgrade` has.

## `barracks rename <loadout> <new-name>`

Renames a loadout everywhere this machine records its name: the definition, every live
spawn's lease, and this repository's `barracks.lock`.

```bash
barracks rename frontend web
```

Nothing on disk moves. A spawned symlink and a committed skill file are named after the
*skill*, never after the loadout, so a rename changes records and leaves deployments exactly
where they are - `git status` included.

**A loadout carries a stable identity that a rename does not change**, minted once when it is
trained and recorded in `barracks.lock` beside the name. That is what keeps a garrison working
in a checkout barracks cannot reach: the name in a teammate's clone goes stale, and the
identity still matches. `barracks inspect` prints it.

A lockfile written before identities existed carries none, so it is matched by name - which is
why renaming rewrites the lockfile here rather than leaving it to be noticed later. Commit that
change like any other. Nothing ever reads a missing identity as a mismatch.

Renaming onto a name already in use is refused and changes nothing. So is a rename that cannot
be completed: if any record cannot be written, every record already written is put back.

## `barracks spawn <loadout>`

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

## `barracks garrison [<loadout>]`

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

```text
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

## `barracks inspect`

Verifies that the committed skill files in this repository are exactly what `barracks.lock`
says they should be. It changes nothing, and exits non-zero on any mismatch, so it can gate
a build.

```bash
barracks inspect
```

```text
frontend  3 skills, 5 files  [claude, cursor]  3 problems
  identity: 940f8b3821e4c07d
  ! .claude/skills/css/SKILL.md: missing
  ! .claude/skills/react/SKILL.md: modified
  ! .claude/skills/react/notes.md: not in the lockfile
```

The identity is what the lockfile entry is really keyed on; the name beside it is a label a
[rename](#barracks-rename-loadout-new-name) can change. A lockfile written before identities
existed says `none recorded` and is matched by name.

`barracks.lock` records a digest per file, so a file edited by hand, dropped in a rebase,
half-merged, or added inside a vendored skill directory is reported rather than silently
accepted. `barracks garrison` is what puts a drifted checkout back.

A note that the loadout is pinned somewhere newer than `barracks.lock` is **not** a
mismatch - the files and the lockfile agree, which is exactly what a teammate cloning the
repository should get. It means the committed pins are behind the loadout, and bringing them
forward is a commit like any other.

## `barracks upgrade [<loadout>...]`

Re-resolves each source's declared ref, fetches whatever it now points at, and
relinks every live spawn onto the new commit. With no loadout named, every
loadout is upgraded.

```bash
barracks upgrade
barracks upgrade frontend --dry-run
barracks upgrade frontend --pin
```

Each source is reported with its old and new commits and a real per-skill diff:

```text
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

**A garrisoned loadout is upgraded too**, and reported separately, because it is
the one part of an upgrade somebody else will read:

```text
frontend
  github.com/owner/skills#main  940f8b38 -> f6183d1c
    + hooks
    ~ react
committed here (barracks.lock)
  frontend
    github.com/owner/skills#main  940f8b38 -> f6183d1c
    + .claude/skills/hooks/SKILL.md
    + .claude/skills/react/SKILL.md
    commit these files and barracks.lock together
```

The vendored files and `barracks.lock` are rewritten together onto the new pins,
so the skill update becomes a reviewable diff instead of something that happens
invisibly on each machine. That is the whole reason the committed tier exists.
The comparison is against the lockfile rather than against whether a source moved
in this run, so a garrison an earlier upgrade left behind is recognised and
brought forward - and an upgrade with nothing to do leaves the repository clean.

A vendored file you have edited stops it, exactly as `barracks garrison` would,
and barracks names the command that overrides it. The loadout definition still
moves forward, so the lockfile is the only thing left behind; `barracks inspect`
reports that as a note until you run `barracks garrison <loadout> --force`.

## `barracks recall <loadout>`

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

```text
recalled the frontend garrison (4 files removed, barracks.lock updated)
! left in place: .claude/skills/react/reference.md - edited since it was committed - your change is kept
! left in place: .claude/skills/react/handwritten.md - barracks has no record of putting it there
```

The removal is a change to tracked files, so it appears in `git status` for review like any
other - and is recoverable with `git checkout` if it was not what you meant.

## `barracks deployed`

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

## `barracks list`

Shows every loadout you have trained, with its sources, skill count, and the agents it
installs into.

```bash
barracks list
barracks list --verbose
```

## `barracks run <loadout> -- <cmd...>`

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
rules with no guessing. See [Choosing targets](./targets.md#choosing-targets).

## Also available

`barracks disband <name>` deletes a loadout definition, refusing while it is still
deployed or garrisoned here - to empty a loadout without deleting it, strip its sources
instead. `barracks targets` lists the agents barracks can deploy to and marks the ones this
repository is already set up for.

---

## Source syntax

```text
gh:owner/repo                      GitHub shorthand
github.com/owner/repo              any host
https://github.com/owner/repo.git
git@github.com:owner/repo.git
./path/to/local/repo               a repository on disk
```

Any form takes a `#ref` suffix to pin a branch, tag, or commit, and a `#ref:subpath`
suffix to scan only part of the repo:

```text
gh:owner/repo#v1.2.0
gh:owner/repo#main:skills
```

A resolved source is always pinned to a concrete commit SHA in the loadout definition, so
a spawn is reproducible even when the ref moves.

---

## Not built yet

Store garbage collection is separately queued. `barracks upgrade` and the committed/shared
tier with a lockfile both landed.
