//go:build windows

package config

import (
	"errors"
	"os"
)

func replaceFile(source, destination string) error {
	err := os.Rename(source, destination)
	if err == nil {
		return nil
	}
	if !errors.Is(err, os.ErrExist) && !errors.Is(err, os.ErrPermission) {
		return err
	}
	// Windows does not replace an existing file with os.Rename. Keep the
	// previous config recoverable until the new file has been fully written.
	backup := destination + ".previous"
	_ = os.Remove(backup)
	if moveErr := os.Rename(destination, backup); moveErr != nil && !errors.Is(moveErr, os.ErrNotExist) {
		return err
	}
	if moveErr := os.Rename(source, destination); moveErr != nil {
		_ = os.Rename(backup, destination)
		return moveErr
	}
	_ = os.Remove(backup)
	return nil
}
