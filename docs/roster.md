# The roster

The full-screen screen a bare `barracks` opens: every loadout on this machine, what each one
carries, where each one is standing, and the keys that deploy it. For the commands the same
orders spell out, see [Commands](./commands.md). Back to the [README](../README.md).

---

Run `barracks` with no arguments and you get the roster: every loadout on this machine, what
each one carries, and where each one is standing, on one screen.

```bash
barracks        # the roster
barracks tui    # the same screen, named explicitly
```

The left pane lists your units with their source count, skill count and posture - spawned
here, committed here, standing in another repository, or in reserve. The right pane is the
dossier for whichever unit the cursor is on: its description, its sources with the commit
each is pinned at and the skills each contributed, and every place it is currently deployed.

From it you can deploy the unit under the cursor five ways: spawn it (`s`), recall it (`r`),
commit it into the repository (`g`), bring its sources forward (`u`), or run an agent with it
(`L`). Every one puts the order in front of you before anything in this repository changes,
and every one runs the same engine the commands do - the same target detection, the same
symlinks, the same `.git/info/exclude` registration, the same all-or-nothing rollback, the
same refusals. A spawn made from the roster is indistinguishable on disk from one made at
the prompt, and anything barracks declines to touch is reported in the roster's own panel
rather than swallowed by it.

The deploy order lets you choose where it goes. It opens on exactly the agents a plain
`barracks spawn` would have picked, and if you leave it alone that is what happens - the
loadout's own declaration and what this repository shows stay in charge. Tick something and
you have overridden both, for this spawn only.

`u` shows you the plan first. It re-resolves every source and reports what carrying it
through would change - the same body `barracks upgrade --dry-run` prints, because it is the
same plan - and nothing moves until you say so. Standing it down leaves everything as it was.

`L` runs an agent with the loadout spawned for the session and recalls it the moment the
agent exits, exactly as `barracks run` does. It offers the agents barracks knows that are
actually installed here.

Any of these may have to fetch, and a fetch can ask you something barracks cannot answer for
you - an SSH key passphrase, a host-key confirmation, a credential helper wanting a password.
An agent needs the keyboard outright. So the roster hands the terminal back for the whole
order: the screen steps aside, the work reports plainly where you can read it, any prompt it
raises is visible and answerable, and the roster comes back with the outcome when it is done.
A `Ctrl-C` at such a prompt cancels the prompt - barracks itself keeps the terminal handed
over until the order has either finished or rolled itself back, because a half-applied spawn
is the one state this tier must never be left in.

| Key | Does |
|---|---|
| `↑`/`k`, `↓`/`j` | move up and down the line |
| `s` | spawn the selected unit here, choosing its targets |
| `r` | recall its spawns from here |
| `g` | garrison it into this repository |
| `u` | plan an upgrade of its sources, then carry it out |
| `L` | run an agent with it, and recall it when the agent exits |
| `space` | choose, on an order that offers a choice |
| `R` | re-read every record |
| `?` | the orders overlay |
| `q` | leave |

`r` covers your own spawns and leaves a garrison exactly where it is - removing tracked files
from a checkout is not something that should sit behind a single key press. Use
`barracks recall <loadout>` for that.

The roster does not train, equip, strip or rename. Those stay commands, and no key is bound
to them - a key that announces it does not work is still a key you have to learn.

**Bare `barracks` opens the roster only when stdout is a terminal.** Anywhere else - a pipe,
a redirect, a CI job - `barracks` prints the help it has always printed plus the one line for
`tui`, and not one escape sequence. A full-screen program in a pipe would write
alternate-screen and cursor sequences into whatever is reading and then wait forever for a key
that is never coming, so `barracks | head` and `barracks` in a script keep working the way
they always did.

`barracks tui` is the explicit spelling of the same screen. Because it is an explicit request
for something interactive, off a terminal it refuses and says so rather than filling your
file with escape codes:

```text
$ barracks tui > log
barracks: the roster needs a terminal and stdout here is not one; for a script use
`barracks list`, `barracks deployed` or `barracks inspect`
```

Nothing is printed after the roster gives the terminal back - no [flavor line](./output.md#voice), no
[progress](./output.md#progress) - because a session at the roster may have changed nothing at all.
