//go:build windows

package command

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const createNewProcessGroup = 0x00000200

type processGuard struct {
	mu       sync.Mutex
	job      windows.Handle
	assigned bool
}

// configureProcess preserves the legacy ProcessStartInfo.Arguments contract.
// Native executables receive the configured argument text verbatim after a
// safely quoted argv[0]. Batch files are the one exception: CreateProcess
// cannot execute them directly, so they are passed to %ComSpec% using cmd's
// documented /d /s /c quoting form while retaining the raw argument suffix.
func configureProcess(cmd *exec.Cmd, rawArguments string) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup,
		HideWindow:    true,
	}
	if isWindowsBatchFile(cmd.Path) {
		return configureBatchProcess(cmd, rawArguments)
	}
	if rawArguments != "" {
		cmd.SysProcAttr.CmdLine = syscall.EscapeArg(cmd.Path) + " " + rawArguments
	}
	return nil
}

func isWindowsBatchFile(path string) bool {
	extension := filepath.Ext(path)
	return strings.EqualFold(extension, ".bat") || strings.EqualFold(extension, ".cmd")
}

// interpreterBatchPath makes a relative batch path resolve against the working
// directory the user configured. cmd.exe looks a bare command name up in PATH
// and only falls back to the current directory while
// NoDefaultCurrentDirectoryInExePath is unset — a variable Windows itself sets
// in several environments, Git for Windows shells among them. Without the
// explicit ".\" the child dies with "not recognized as an internal or external
// command" on exactly those machines. The path has to stay relative, because
// the caller assigns cmd.Dir after this runs.
func interpreterBatchPath(path string) string {
	if filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return path
	}
	path = filepath.FromSlash(path)
	if strings.HasPrefix(path, `.\`) || strings.HasPrefix(path, `..\`) {
		return path
	}
	return `.\` + path
}

func configureBatchProcess(cmd *exec.Cmd, rawArguments string) error {
	batchPath := cmd.Path
	if strings.ContainsAny(batchPath, "\"\r\n") {
		return fmt.Errorf("invalid batch command path %q", batchPath)
	}
	commandInterpreter := os.Getenv("ComSpec")
	if commandInterpreter == "" {
		commandInterpreter = "cmd.exe"
	}
	resolvedInterpreter, err := exec.LookPath(commandInterpreter)
	if err != nil {
		return fmt.Errorf("locate Windows command interpreter %q: %w", commandInterpreter, err)
	}

	// Always quote the batch path for cmd.exe, even when it has no spaces:
	// valid Windows filenames may contain metacharacters such as '&'.
	batchCommand := `"` + interpreterBatchPath(batchPath) + `"`
	if rawArguments != "" {
		batchCommand += " " + rawArguments
	}
	cmd.Path = resolvedInterpreter
	cmd.Args = []string{resolvedInterpreter}
	// exec.Command may have recorded a LookPath error for a batch name that is
	// intentionally relative to cmd.Dir. The actual executable is now ComSpec,
	// so that stale lookup error must not prevent Start.
	cmd.Err = nil
	cmd.SysProcAttr.CmdLine = syscall.EscapeArg(resolvedInterpreter) + ` /d /s /c "` + batchCommand + `"`
	return nil
}

func newProcessGuard() (*processGuard, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	return &processGuard{job: job}, nil
}

func attachProcessGuard(guard *processGuard, cmd *exec.Cmd) error {
	if guard == nil || cmd.Process == nil {
		return errors.New("process guard or process is unavailable")
	}
	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)

	guard.mu.Lock()
	defer guard.mu.Unlock()
	if guard.job == 0 {
		return errors.New("process guard is already closed")
	}
	if err := windows.AssignProcessToJobObject(guard.job, process); err != nil {
		return err
	}
	guard.assigned = true
	return nil
}

func closeProcessGuard(guard *processGuard) error {
	if guard == nil {
		return nil
	}
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if guard.job == 0 {
		return nil
	}
	job := guard.job
	guard.job = 0
	guard.assigned = false
	return windows.CloseHandle(job)
}

func terminateProcess(cmd *exec.Cmd, guard *processGuard) error {
	if cmd.Process == nil {
		return nil
	}
	if guard != nil {
		guard.mu.Lock()
		if guard.job != 0 && guard.assigned {
			err := windows.TerminateJobObject(guard.job, 1)
			guard.mu.Unlock()
			if err == nil {
				return nil
			}
		} else {
			guard.mu.Unlock()
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	killer := exec.CommandContext(ctx, "taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
	killer.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	output, treeErr := killer.CombinedOutput()
	if treeErr == nil {
		return nil
	}
	leafErr := cmd.Process.Kill()
	if leafErr == nil || errors.Is(leafErr, os.ErrProcessDone) {
		return nil
	}
	return fmt.Errorf("taskkill failed (%v: %s); process kill failed: %w", treeErr, output, leafErr)
}
