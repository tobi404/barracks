# What it does to your repo

Exactly what barracks writes, where it writes it, how long it stays, and what it will never
touch. Back to the [README](../README.md).

---

## Spawn or garrison

The two tiers side by side. Which one to reach for is on the
[README](../README.md#personal-or-shared); this is what each one costs and guarantees.

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
| `barracks upgrade` | Relinks it in place | Rewrites the files and `barracks.lock` |
| Updating | Happens on each machine | A reviewable diff in a pull request |

## What each one writes

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
machine. That is why the two tiers refuse each other: a path registered both ways would
either hide the committed files from the team or leave every checkout dirty forever.

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

## On-disk layout

```text
~/.barracks/
├── loadouts/       # one hand-editable YAML file per loadout
├── store/          # <host>/<owner>/<repo>@<commit>/ - fetched once, shared by everything
├── mirrors/        # bare git mirrors, so a repo is cloned at most once
├── leases/         # one record per live spawn - never for a garrison
└── voice.yaml      # how put-upon the unit is - see output.md; delete it freely
```

A garrison keeps nothing here. Its record is `barracks.lock`, committed at the root of the
repository it belongs to:

```text
your-repo/
├── barracks.lock       # generated: identity, sources, commits, and a digest per vendored file
└── .claude/skills/     # real, committed skill directories
```

Each entry is keyed on the loadout's **stable identity**, not on its name, so
[renaming](./commands.md#barracks-rename-loadout-new-name) a loadout cannot orphan a garrison
in a checkout barracks will never see. Entries written before identities existed carry none
and are matched by name, exactly as they always were; `version:` at the top of the file is
what tells a future format change apart from this one.

`XDG_CONFIG_HOME` and `XDG_DATA_HOME` are honoured when set; `BARRACKS_HOME` overrides
everything.
