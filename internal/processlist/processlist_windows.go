//go:build windows

package processlist

import (
	"context"
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func list(ctx context.Context) ([]Process, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("create process snapshot: %w", err)
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err := windows.Process32First(snapshot, &entry); err != nil {
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return []Process{}, nil
		}
		return nil, fmt.Errorf("read first process: %w", err)
	}
	currentPID := uint32(os.Getpid())
	result := make([]Process, 0, 64)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.ProcessID != currentPID {
			result = append(result, Process{
				PID:        entry.ProcessID,
				Executable: windows.UTF16ToString(entry.ExeFile[:]),
			})
		}
		err = windows.Process32Next(snapshot, &entry)
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read process snapshot: %w", err)
		}
	}
	return result, nil
}
