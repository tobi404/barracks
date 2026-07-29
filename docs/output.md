# Progress and voice

How barracks talks to you while it works, and how to make it quieter. Both of these are
decoration, both stay off anything but a terminal, and one switch turns off both. Back to the
[README](../README.md).

---

## Progress

Fetching a source can take a while, and a blinking cursor cannot tell you whether barracks
is working, stuck, or waiting for a passphrase. So anything that has to reach the network -
`equip`, `upgrade`, and the first `spawn`, `garrison`, `strip` or `run` that has to populate
the store rather than reuse it - says what it is doing while it does it.

```text
✓ github.com/obra/superpowers    resolved 7b2e4d1  3s
⠹ github.com/big/monorepo        fetching… 24s
```

Completed steps stay put and scroll away with everything else; only the current line spins.
After a genuinely long wait it adds one line naming the likely causes, so a hang turns into
something you can act on:

```text
  (large repository, slow network, or git waiting on credentials)
⠹ github.com/big/monorepo        fetching… 24s
```

Like the voice, it is decoration and keeps out of the way:

- **Nothing appears for the first fraction of a second**, so a warm store still looks
  instant - because it is.
- **Only on stderr**, so stdout carries nothing but the report.
- **Escape sequences only on a terminal.** Redirect stderr or run in CI and you get the same
  lines as plain text with no spinner and no cursor tricks, because an escape code in a log
  file is corruption.
- **It never takes the terminal from git.** Anything that might ask you a question gets the
  terminal to itself: fetching over SSH is not animated at all, and neither is a fetch where
  git is configured with a credential helper barracks cannot show is silent. A passphrase,
  host-key or credential prompt stays readable and answerable - those are written straight to
  your terminal, and nothing here will paint over them. You still get every line, just
  without the spinner.
- **The cursor always comes back**, on success, on failure, and on Ctrl-C.

`--quiet` (`-q`) and `BARRACKS_QUIET=1` turn it off along with the voice. One switch for
both: the flag has always meant both, and the variable is just the standing form of it.

---

## Voice

`train`, `equip`, `strip`, `spawn`, `recall`, `upgrade`, `garrison` and `run` each sign off
with one short line from the unit that just took the order. Ask for the same thing again and
again and it gets progressively more put-upon; leave it alone for a while and it greets you
fresh. Asking somewhere else does not count as asking again: `spawn frontend` in a second
repository is a first spawn there, and starts over.

```text
$ barracks spawn frontend
spawned frontend into /home/you/app/.claude/skills (Claude Code, until recalled)
  + react
  + css
  ▸ Off to the front.
```

It is decoration, so it keeps out of the way of everything that matters:

- **Only on a terminal.** Piped, redirected, or running in CI there is no line at all, and
  no flag is needed to get that.
- **Only on stderr**, so `barracks list | grep react` is never polluted even interactively.
- **Never on a failure**, and never from a command that only reports - `list`, `deployed`,
  `inspect` and `targets` say nothing, and neither does a preview like
  `upgrade --dry-run`, which also leaves the unit as fresh as it found it.

Turn it off for one command with `--quiet` (`-q`), or for good with `BARRACKS_QUIET=1`.
Both also turn off the [progress indicator](#progress).
