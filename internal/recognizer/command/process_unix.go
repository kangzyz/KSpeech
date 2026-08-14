//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package command

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

type processGuard struct {
	processGroupID int
}

func configureProcess(cmd *exec.Cmd, rawArguments string) error {
	arguments, err := splitArguments(rawArguments)
	if err != nil {
		return err
	}
	cmd.Args = append(cmd.Args[:1], arguments...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}

func newProcessGuard() (*processGuard, error) {
	return &processGuard{}, nil
}

func attachProcessGuard(guard *processGuard, cmd *exec.Cmd) error {
	guard.processGroupID = cmd.Process.Pid
	return nil
}

func closeProcessGuard(guard *processGuard) error {
	if guard == nil || guard.processGroupID == 0 {
		return nil
	}
	return killProcessGroup(guard.processGroupID)
}

func terminateProcess(cmd *exec.Cmd, guard *processGuard) error {
	if cmd.Process == nil {
		return nil
	}
	processGroupID := cmd.Process.Pid
	if guard != nil && guard.processGroupID != 0 {
		processGroupID = guard.processGroupID
	}
	err := killProcessGroup(processGroupID)
	if err == nil {
		return nil
	}
	err = cmd.Process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func killProcessGroup(processGroupID int) error {
	err := syscall.Kill(-processGroupID, syscall.SIGKILL)
	if err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
