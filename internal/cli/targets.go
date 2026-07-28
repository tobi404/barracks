package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tobi404/barracks/internal/loadout"
	"github.com/tobi404/barracks/internal/spawn"
	"github.com/tobi404/barracks/internal/target"
)

// targetFlagHelp keeps every command's --target help in sync with the map.
func targetFlagHelp(verb string) string {
	return fmt.Sprintf("agent to %s (repeatable; %s)", verb, strings.Join(target.IDs(), ", "))
}

// scopeOf resolves where "here" is for this invocation, without creating
// anything. Only the scope and the repository root are used, so the default
// target stands in for all of them.
func (e *Env) scopeOf(ctx context.Context, global bool) (spawn.Location, error) {
	return e.engine.Resolve(ctx, spawn.Request{Target: target.Default(), Global: global, Cwd: e.Cwd})
}

// selectTargets decides which agents this invocation installs into.
//
// The flag wins over the loadout's declaration, which wins over what is already
// on disk, which wins over the default. Nothing here knows an agent-specific
// path; detection is driven by the markers each registry entry declares.
func (e *Env) selectTargets(ctx context.Context, l *loadout.Loadout, override []string, global bool) (target.Selection, error) {
	return e.selectTargetsFor(ctx, l, override, global, nil)
}

// selectTargetsFor is selectTargets with the agent a `barracks run` is about to
// launch, which only that command knows. It changes nothing about precedence:
// launched agents join the branch that would otherwise only consult the
// repository, so an explicit --target or loadout declaration is never widened.
func (e *Env) selectTargetsFor(ctx context.Context, l *loadout.Loadout, override []string, global bool, launched []target.Target) (target.Selection, error) {
	var detected []target.Target
	if global {
		detected = target.DetectGlobal(e.Getenv, e.Home)
	} else if loc, err := e.scopeOf(ctx, false); err == nil && loc.Root != "" {
		detected = target.Detect(loc.Root)
	}
	return target.Select(override, l.Targets, detected, launched)
}

// announceSelection says which agents were picked whenever the user did not say
// so themselves. A spawn landing somewhere unexpected must never be silent.
func (e *Env) announceSelection(sel target.Selection) {
	if sel.Origin == target.OriginFlag || sel.Origin == target.OriginLoadout {
		return
	}
	fmt.Fprintf(e.Out, "targets: %s (%s)\n", strings.Join(sel.IDs(), ", "), sel.Reason())
}

// warnLaunchedAgentExcluded says so when an explicit choice sends the skills
// somewhere the agent being launched will not look.
//
// It only ever warns. A --target flag or a loadout declaration is the user's
// own decision and barracks does not overrule it - but letting a run start an
// agent that cannot see a single one of the skills it just installed, without a
// word, is the failure this exists to prevent.
//
// The test is whether any selected target is read by the launched agent, not
// whether the launched agent's own target was selected. Some conventions are
// shared on purpose, so an ID comparison would fire on a correct invocation and
// assert something untrue - and a warning that goes off when nothing is wrong
// teaches the user to ignore it, which is worse than never warning at all.
func (e *Env) warnLaunchedAgentExcluded(sel target.Selection, launched []target.Target) {
	if sel.Origin != target.OriginFlag && sel.Origin != target.OriginLoadout {
		return
	}
	for _, t := range launched {
		if target.AnyReadBy(sel.Targets, t) {
			continue
		}
		fmt.Fprintf(e.Err, "! %s does not read any of the selected targets (%s), so it will not see these skills\n",
			t.Display, strings.Join(sel.IDs(), ", "))
	}
}

// spawnAll materialises the loadout into every selected target and surfaces
// anything an all-or-nothing rollback could not undo.
//
// Every other revocation reports what it kept; a rollback is no different, and
// this is the one place both spawning commands go through.
func (e *Env) spawnAll(ctx context.Context, req spawn.Request, targets []target.Target) ([]*spawn.Result, error) {
	results, err := e.engine.SpawnAll(ctx, req, targets)
	var rollback *spawn.RollbackError
	if errors.As(err, &rollback) {
		for _, rep := range rollback.Reports {
			reportKept(e.Err, rep)
		}
	}
	return results, err
}

// declaredIDs validates the target spellings a user gave and returns the
// canonical IDs to store on a loadout, so the YAML file, `barracks list`, and
// the command's own output can never disagree about where a spawn will go.
func declaredIDs(ids []string) ([]string, error) {
	resolved, err := target.LookupAll(ids)
	if err != nil {
		return nil, err
	}
	return idsOf(resolved), nil
}

// resolveTargetFilter turns --target flags into the target IDs a command acts
// on. No flags means every agent, which is what makes one recall undo one
// two-agent spawn.
func resolveTargetFilter(ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	resolved, err := target.LookupAll(ids)
	if err != nil {
		return nil, err
	}
	return idsOf(resolved), nil
}

// scopeLabel names where a command is acting, for messages.
func scopeLabel(loc spawn.Location, global bool) string {
	if global {
		return "in your user-level skills directories"
	}
	return "in " + loc.Root
}

// targetSuffix qualifies a message with the agents it was narrowed to.
func targetSuffix(filter []string) string {
	if len(filter) == 0 {
		return ""
	}
	return " for " + strings.Join(filter, ", ")
}

// displayOf renders a recorded target ID for output, falling back to the ID
// itself so a lease written by a version that knew another agent still reads
// sensibly.
func displayOf(id string) string {
	t, err := target.Lookup(id)
	if err != nil || id == "" {
		return id
	}
	return t.Display
}

func newAssignCmd(env *Env) *cobra.Command {
	var auto bool

	cmd := &cobra.Command{
		Use:   "assign <loadout> [target...]",
		Short: "Set which agents a loadout installs into",
		Long: strings.TrimSpace(`
Declares the agents a loadout installs into.

The choice belongs to the loadout, not to a machine-wide setting: one loadout
can be a Cursor loadout and another a Claude Code one, and a loadout can name
several agents so a single spawn reaches all of them.

  barracks assign frontend                 show the current declaration
  barracks assign frontend claude cursor   install into both from now on
  barracks assign frontend --auto          clear it and detect per repository

A loadout that declares nothing is detected at spawn time from what the
repository already contains, falling back to the default target. Overriding a
spawn with --target does not change what is declared here.`),
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env.reap()
			name, ids := args[0], args[1:]

			l, err := env.loadouts.Get(name)
			if err != nil {
				return err
			}
			if auto && len(ids) > 0 {
				return fmt.Errorf("cannot combine target names with --auto: --auto clears the declaration")
			}

			if len(ids) == 0 && !auto {
				printAssignment(env, l)
				return nil
			}
			// Resolve before writing: a declaration barracks cannot resolve
			// would only fail later, at spawn time, far from the mistake, and
			// what is stored is the canonical ID rather than the spelling that
			// happened to be typed.
			declared, err := declaredIDs(ids)
			if err != nil {
				return err
			}
			l.SetTargets(declared)
			if err := env.loadouts.Save(l); err != nil {
				return err
			}
			printAssignment(env, l)
			return nil
		},
	}
	cmd.Flags().BoolVar(&auto, "auto", false, "clear the declaration and detect per repository instead")
	return cmd
}

func printAssignment(env *Env, l *loadout.Loadout) {
	if len(l.Targets) == 0 {
		fmt.Fprintf(env.Out, "%s declares no targets - detected per repository, falling back to %s\n", l.Name, target.DefaultID)
		return
	}
	resolved, err := target.LookupAll(l.Targets)
	if err != nil {
		fmt.Fprintf(env.Out, "%s -> %s\n", l.Name, strings.Join(l.Targets, ", "))
		fmt.Fprintf(env.Err, "! %v\n", err)
		return
	}
	fmt.Fprintf(env.Out, "%s -> %s\n", l.Name, strings.Join(idsOf(resolved), ", "))
	for _, t := range resolved {
		fmt.Fprintf(env.Out, "    %-10s %s\n", t.ID, t.Display)
	}
}

func idsOf(targets []target.Target) []string {
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		out = append(out, t.ID)
	}
	return out
}

func newTargetsCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "targets",
		Short: "List the agents barracks can deploy to",
		Long: strings.TrimSpace(`
Lists every agent barracks knows how to deploy to, where each one keeps its
skills, and the documentation those paths were read from.

Pass one of these IDs to --target, or declare them on a loadout with
barracks assign. A target marked "present here" already has its configuration
directory in this repository, so a loadout declaring no targets would be
installed into it.

  barracks targets`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			env.reap()

			present := map[string]bool{}
			if loc, err := env.scopeOf(cmd.Context(), false); err == nil && loc.Root != "" {
				for _, t := range target.Detect(loc.Root) {
					present[t.ID] = true
				}
			}

			for _, t := range target.Registry {
				global, err := t.GlobalPath(env.Getenv, env.Home)
				if err != nil {
					global = "(unresolvable: " + err.Error() + ")"
				}
				marker := " "
				if t.ID == target.DefaultID {
					marker = "*"
				}
				fmt.Fprintf(env.Out, "%s %-10s %s", marker, t.ID, t.Display)
				if len(t.Aliases) > 0 {
					fmt.Fprintf(env.Out, "  (also: %s)", strings.Join(t.Aliases, ", "))
				}
				if present[t.ID] {
					fmt.Fprint(env.Out, "  [present here]")
				}
				fmt.Fprintln(env.Out)
				fmt.Fprintf(env.Out, "    in repo:  <repo>/%s\n", t.RepoDir)
				fmt.Fprintf(env.Out, "    global:   %s\n", global)
				if t.Docs != "" {
					fmt.Fprintf(env.Out, "    docs:     %s\n", t.Docs)
				}
				for _, r := range t.AlsoReadBy {
					fmt.Fprintf(env.Out, "    also read by %s (%s)\n", displayOf(r.Target), r.Docs)
				}
			}
			fmt.Fprintf(env.Out, "\n* default target\n")
			return nil
		},
	}
}
