# Targets

Which agents barracks can deploy to, how it decides which ones a given loadout installs
into, and how to add another one. Back to the [README](../README.md).

---

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

## Choosing targets

The choice belongs to the loadout, not to a machine-wide setting - one loadout can be a
Cursor loadout while another is a Claude Code one.

```bash
barracks train editor --target cursor --target windsurf   # at training time
barracks assign editor cursor windsurf                    # or change it later
barracks assign editor --auto                             # or hand it back to detection
```

The declaration is a `targets:` list in the loadout's YAML file, so it can also be edited
by hand. At spawn time barracks takes the first of these that applies:

1. `--target` given on this invocation, or the agents ticked by hand in
   [the roster](./roster.md)'s deploy picker - for this spawn only, never written back.
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

Only `barracks run` - and [the roster](./roster.md)'s `L`, which starts the same session -
contributes case 3's first half, because starting the agent is the only way barracks knows
which one is about to read the skills. It never widens an explicit choice: if `--target` or
the loadout's declaration installs nowhere the agent being launched reads, barracks warns
and installs where you asked anyway. Targets that share a directory count -
`--target agents -- opencode` installs somewhere opencode does read, so nothing is warned
about.

## Adding a target

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
