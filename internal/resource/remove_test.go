package resource

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRemoveDeletesOnlyUserModuleAndRevealsBuiltIn(t *testing.T) {
	manager := newTestManager(t, "https://marketplace.invalid/marketplace.json")
	builtInDir := writeTestModule(t, manager.BuiltInPluginsDir(), "packaged", ModuleInfo{ID: "shared", Version: 1})
	userDir := writeTestModule(t, manager.UserPluginsDir(), "installed-update", ModuleInfo{ID: "shared", Version: 2})
	writeTestFile(t, filepath.Join(userDir, "payload.txt"), []byte("user payload"))

	if err := manager.Remove(context.Background(), "shared"); err != nil {
		t.Fatalf("Remove(user module) error = %v", err)
	}
	if _, err := os.Stat(userDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed user directory still exists or cannot be checked: %v", err)
	}
	if _, err := os.Stat(filepath.Join(builtInDir, ModuleJSONName)); err != nil {
		t.Fatalf("built-in manifest was removed: %v", err)
	}

	local, found, err := manager.Local(context.Background(), "shared")
	if err != nil {
		t.Fatalf("Local() error = %v", err)
	}
	if !found || local.CanRemove || local.LocalInfo.Version != 1 || local.LocalDir != builtInDir {
		t.Fatalf("Local() after removal = %+v, found=%v", local, found)
	}
	if err := manager.Remove(context.Background(), "shared"); !errors.Is(err, ErrNotRemovable) {
		t.Fatalf("Remove(built-in module) error = %v, want not removable", err)
	}
}

func TestRemoveReportsMissingAndInvalidModules(t *testing.T) {
	manager := newTestManager(t, "https://marketplace.invalid/marketplace.json")
	if err := manager.Remove(context.Background(), "missing"); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("Remove(missing) error = %v, want not installed", err)
	}
	for _, id := range []string{"", "../escape", `..\escape`} {
		if err := manager.Remove(context.Background(), id); !errors.Is(err, ErrInvalidModule) {
			t.Errorf("Remove(%q) error = %v, want invalid module", id, err)
		}
	}
	if err := manager.Remove(nil, "missing"); err == nil {
		t.Fatal("Remove(nil context) error = nil")
	}
}

func TestRemoveRejectsSymlinkedUserResourceRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating a directory symlink may require Windows developer mode")
	}
	root := t.TempDir()
	userData := filepath.Join(root, "user-data")
	external := filepath.Join(root, "external")
	if err := os.MkdirAll(userData, 0o755); err != nil {
		t.Fatal(err)
	}
	moduleDir := writeTestModule(t, external, "module", ModuleInfo{ID: "model", Version: 1})
	if err := os.Symlink(external, filepath.Join(userData, PluginDirName)); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(Options{
		ExecutableDir:  filepath.Join(root, "application"),
		UserDataDir:    userData,
		MarketplaceURL: "https://marketplace.invalid/marketplace.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Remove(context.Background(), "model"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Remove() error = %v, want ErrUnsafePath", err)
	}
	if _, err := os.Stat(filepath.Join(moduleDir, ModuleJSONName)); err != nil {
		t.Fatalf("external module changed despite rejected root: %v", err)
	}
}
