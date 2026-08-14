//go:build !windows && !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package command

import (
	"errors"
	"os"
	"os/exec"
)

type processGuard struct{}

func configureProcess(cmd *exec.Cmd, rawArguments string) error {
	arguments, err := splitArguments(rawArguments)
	if err != nil {
		return err
	}
	cmd.Args = append(cmd.Args[:1], arguments...)
	return nil
}

func newProcessGuard() (*processGuard, error) { return &processGuard{}, nil }

func attachProcessGuard(*processGuard, *exec.Cmd) error { return nil }

func closeProcessGuard(*processGuard) error { return nil }

func terminateProcess(cmd *exec.Cmd, _ *processGuard) error {
	if cmd.Process == nil {
		return nil
	}
	err := cmd.Process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}
