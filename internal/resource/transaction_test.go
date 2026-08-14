package resource

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScanLocalRestoresValidatedBackupAfterActivationCrash(t *testing.T) {
	manager := newTestManager(t, "https://marketplace.invalid/marketplace.json")
	if err := os.MkdirAll(manager.UserPluginsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	target := writeTestModule(t, manager.UserPluginsDir(), "custom-directory", ModuleInfo{ID: "speech-model", Version: 1})
	writeTestFile(t, filepath.Join(target, "model.bin"), []byte("preserved"))
	backup := newTestManagedArtifact(t, manager, managedArtifactBackup, "speech-model", "custom-directory", time.Now())
	if err := os.Rename(target, filepath.Join(backup, "previous")); err != nil {
		t.Fatal(err)
	}

	resources, err := manager.ScanLocal(context.Background())
	if err != nil {
		t.Fatalf("ScanLocal() error = %v", err)
	}
	if len(resources) != 1 || resources[0].ID() != "speech-model" || resources[0].LocalDir != target {
		t.Fatalf("restored resources = %+v, want speech-model at %q", resources, target)
	}
	if got := string(readTestFile(t, filepath.Join(target, "model.bin"))); got != "preserved" {
		t.Fatalf("restored payload = %q", got)
	}
	if _, err := os.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restored backup holder still exists: %v", err)
	}
}

func TestScanLocalCleansOnlyOldValidatedManagedArtifacts(t *testing.T) {
	var issues []error
	manager := newTestManager(t, "https://marketplace.invalid/marketplace.json", func(options *Options) {
		options.OnIssue = func(err error) { issues = append(issues, err) }
	})
	if err := os.MkdirAll(manager.UserPluginsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-staleManagedArtifactAge - time.Hour)
	target := writeTestModule(t, manager.UserPluginsDir(), "module", ModuleInfo{ID: "module", Version: 2})
	writeTestFile(t, filepath.Join(target, "current.bin"), []byte("current"))
	backup := newTestManagedArtifact(t, manager, managedArtifactBackup, "module", "module", old)
	writeTestModule(t, backup, "previous", ModuleInfo{ID: "module", Version: 1})

	staleInstall := newTestManagedArtifact(t, manager, managedArtifactInstall, "module", "", old)
	if err := os.Mkdir(filepath.Join(staleInstall, "module"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(staleInstall, "downloads"), 0o700); err != nil {
		t.Fatal(err)
	}
	freshInstall := newTestManagedArtifact(t, manager, managedArtifactInstall, "module", "", time.Now())

	malformed := filepath.Join(manager.UserPluginsDir(), managedInstallPrefix+strings.Repeat("a", managedTokenBytes*2))
	if err := os.Mkdir(malformed, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(malformed, managedArtifactMarker), []byte(`{"owner":"someone-else"}`))
	writeTestFile(t, filepath.Join(malformed, "user.txt"), []byte("keep"))
	legacyUnknown := filepath.Join(manager.UserPluginsDir(), ".kspeech-install-user-data")
	if err := os.Mkdir(legacyUnknown, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := manager.ScanLocal(context.Background()); err != nil {
		t.Fatalf("ScanLocal() error = %v", err)
	}
	for _, removed := range []string{backup, staleInstall} {
		if _, err := os.Lstat(removed); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("old validated artifact %q was not removed: %v", removed, err)
		}
	}
	for _, preserved := range []string{target, freshInstall, malformed, legacyUnknown} {
		if _, err := os.Lstat(preserved); err != nil {
			t.Errorf("unowned or fresh path %q was removed: %v", preserved, err)
		}
	}
	if len(issues) == 0 {
		t.Fatal("malformed manager-looking directory was preserved without reporting an issue")
	}
}

func TestScanLocalRefusesUnsafeOrAmbiguousBackupRecovery(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, manager *Manager) []string
	}{
		{
			name: "manifest id mismatch",
			setup: func(t *testing.T, manager *Manager) []string {
				backup := newTestManagedArtifact(t, manager, managedArtifactBackup, "expected", "expected", time.Now())
				writeTestModule(t, backup, "previous", ModuleInfo{ID: "other", Version: 1})
				return []string{backup}
			},
		},
		{
			name: "unexpected backup entry",
			setup: func(t *testing.T, manager *Manager) []string {
				backup := newTestManagedArtifact(t, manager, managedArtifactBackup, "expected", "expected", time.Now())
				writeTestModule(t, backup, "previous", ModuleInfo{ID: "expected", Version: 1})
				writeTestFile(t, filepath.Join(backup, "do-not-delete.txt"), []byte("user data"))
				return []string{backup}
			},
		},
		{
			name: "multiple matching backups",
			setup: func(t *testing.T, manager *Manager) []string {
				var backups []string
				for version := int64(1); version <= 2; version++ {
					backup := newTestManagedArtifact(t, manager, managedArtifactBackup, "expected", "expected", time.Now())
					writeTestModule(t, backup, "previous", ModuleInfo{ID: "expected", Version: version})
					backups = append(backups, backup)
				}
				return backups
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var issues []error
			manager := newTestManager(t, "https://marketplace.invalid/marketplace.json", func(options *Options) {
				options.OnIssue = func(err error) { issues = append(issues, err) }
			})
			if err := os.MkdirAll(manager.UserPluginsDir(), 0o755); err != nil {
				t.Fatal(err)
			}
			backups := test.setup(t, manager)
			resources, err := manager.ScanLocal(context.Background())
			if err != nil {
				t.Fatalf("ScanLocal() error = %v", err)
			}
			if len(resources) != 0 {
				t.Fatalf("unsafe backup was restored: %+v", resources)
			}
			if _, err := os.Lstat(filepath.Join(manager.UserPluginsDir(), "expected")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unsafe backup created target: %v", err)
			}
			for _, backup := range backups {
				if _, err := os.Lstat(backup); err != nil {
					t.Errorf("unsafe or ambiguous backup %q was removed: %v", backup, err)
				}
			}
			if len(issues) == 0 {
				t.Fatal("unsafe backup was rejected without an issue")
			}
		})
	}
}

func newTestManagedArtifact(t *testing.T, manager *Manager, kind managedArtifactKind, moduleID, targetName string, createdAt time.Time) string {
	t.Helper()
	directory, err := createManagedArtifact(manager.UserPluginsDir(), kind, moduleID, targetName, createdAt)
	if err != nil {
		t.Fatalf("createManagedArtifact() error = %v", err)
	}
	return directory
}
