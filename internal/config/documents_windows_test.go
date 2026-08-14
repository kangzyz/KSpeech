//go:build windows

package config

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestDefaultsUseWindowsDocumentsKnownFolder(t *testing.T) {
	documents, err := windows.KnownFolderPath(windows.FOLDERID_Documents, windows.KF_FLAG_DEFAULT)
	if err != nil {
		t.Skipf("Windows Documents known folder is unavailable: %v", err)
	}
	want := filepath.Join(documents, "KSpeechLogs")
	if got := Defaults()[GeneralResultLogPath]; got != want {
		t.Fatalf("default result log path = %q, want %q", got, want)
	}
}
