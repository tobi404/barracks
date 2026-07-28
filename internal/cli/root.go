package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// Version is what `--version` prints. Main overwrites it with the line each
// binary's entry point hands in; internal/buildinfo is where that line is built
// and where the link-time stamping lives.
var Version = "dev"

// ExitError carries a child process's exit code out through cobra.
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }

// New builds the barracks command tree bound to env.
func New(env *Env) *cobra.Command {
	root := &cobra.Command{
		Use:   "barracks",
		Short: "Train, equip, and spawn loadouts of agent skills",
		Long: strings.TrimSpace(`
barracks turns a pile of agent skills scattered across git repos into named,
versioned units you can deploy on demand.

Train a loadout, equip it with skill sources, then spawn it into whatever repo
you are working in - permanently, until a deadline, or only for as long as one
command runs. Recall it and the repo is exactly as you found it.

  barracks train frontend
  barracks equip frontend gh:owner/skills#main:skills --only 'react-*'
  barracks spawn frontend
  barracks run frontend -- claude

Skills are fetched once into a shared store and spawned as symlinks, so every
repo on your machine shares one copy. Nothing barracks did not create is ever
removed, and spawning leaves git status clean.`),
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       Version,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return env.init()
		},
	}
	root.SetOut(env.Out)
	root.SetErr(env.Err)
	root.CompletionOptions.DisableDefaultCmd = true

	root.AddCommand(
		newTrainCmd(env),
		newEquipCmd(env),
		newListCmd(env),
		newSpawnCmd(env),
		newUpgradeCmd(env),
		newRecallCmd(env),
		newDeployedCmd(env),
		newRunCmd(env),
		newDisbandCmd(env),
		newAssignCmd(env),
		newTargetsCmd(env),
	)
	return root
}

// Main is the process entry point shared by both binaries.
func Main(args []string, version string) int {
	Version = version
	env, err := DefaultEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "barracks: %v\n", err)
		return 1
	}
	cmd := New(env)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		var exit *ExitError
		if ok := asExitError(err, &exit); ok {
			return exit.Code
		}
		fmt.Fprintf(env.Err, "barracks: %v\n", err)
		return 1
	}
	return 0
}
