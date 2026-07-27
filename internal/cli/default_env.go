package cli

import (
	"errors"
	"os"
	"os/exec"
	"time"

	"github.com/tobi404/barracks/internal/gitcmd"
	"github.com/tobi404/barracks/internal/paths"
	"github.com/tobi404/barracks/internal/proc"
)

// DefaultEnv builds the environment a real invocation runs in.
func DefaultEnv() (*Env, error) {
	layout, err := paths.Resolve(os.Getenv, os.UserHomeDir)
	if err != nil {
		return nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return &Env{
		Out:    os.Stdout,
		Err:    os.Stderr,
		Cwd:    cwd,
		Layout: layout,
		Now:    time.Now,
		Prober: proc.OSProber{},
		Git:    gitcmd.Git{},
		Getenv: os.Getenv,
		Home:   os.UserHomeDir,
	}, nil
}

func asExitError(err error, target **ExitError) bool {
	return errors.As(err, target)
}

func asExecExitError(err error, target **exec.ExitError) bool {
	return errors.As(err, target)
}
