package cli

import (
	"context"
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
	var detected []target.Target
	if global {
		detected = target.DetectGlobal(e.Getenv, e.Home)
	} else if loc, err := e.scopeOf(ctx, false); err == nil && loc.Root != "" {
		detected = target.Detect(loc.Root)
	}
	return target.Select(override, l.Targets, detected)
}

// announceSelection says which agents were picked whenever the user did not say
// so themselves. A spawn landing somewhere unexpected must never be silent.
func (e *Env) announceSelection(sel target.Selection) {
	if sel.Origin == target.OriginFlag || sel.Origin == target.OriginLoadout {
		return
	}
	fmt.Fprintf(e.Out, "targets: %s (%s)\n", strings.Join(sel.IDs(), ", "), sel.Reason())
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
			if !auto {
				// Validate before writing: a declaration barracks cannot resolve
				// would only fail later, at spawn time, far from the mistake.
				if _, err := target.LookupAll(ids); err != nil {
					return err
				}
			}
			l.SetTargets(ids)
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
			}
			fmt.Fprintf(env.Out, "\n* default target\n")
			return nil
		},
	}
}
