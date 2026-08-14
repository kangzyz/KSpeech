package resource

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallValidatesPluginRuntimeFiles(t *testing.T) {
	t.Run("plugin requires an assembly declaration", func(t *testing.T) {
		manager := newTestManager(t, "https://marketplace.invalid/marketplace.json")
		module := ModuleInfo{
			ID:           "plugin",
			Version:      1,
			Type:         ModuleTypePlugin,
			InstallSteps: []InstallStep{{Type: InstallStepWriteModuleJSON}},
		}
		if err := manager.Install(context.Background(), module, nil); !errors.Is(err, ErrInvalidModule) {
			t.Fatalf("Install() error = %v, want invalid module", err)
		}
	})

	t.Run("declared assembly must be installed", func(t *testing.T) {
		manager := newTestManager(t, "https://marketplace.invalid/marketplace.json")
		module := ModuleInfo{
			ID:           "plugin",
			Version:      1,
			Type:         ModuleTypePlugin,
			Assemblies:   []string{"bin/plugin.dll"},
			InstallSteps: []InstallStep{{Type: InstallStepWriteModuleJSON}},
		}
		err := manager.Install(context.Background(), module, nil)
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Install() error = %v, want missing runtime file", err)
		}
		if _, statErr := os.Stat(filepath.Join(manager.UserPluginsDir(), module.ID)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("invalid plugin was activated: %v", statErr)
		}
	})

	t.Run("declared assembly is a regular installed file", func(t *testing.T) {
		manager := newTestManager(t, "https://marketplace.invalid/marketplace.json")
		module := ModuleInfo{
			ID:         "plugin",
			Version:    1,
			Type:       ModuleTypePlugin,
			Assemblies: []string{"bin/plugin.dll"},
			InstallSteps: []InstallStep{
				{Type: InstallStepWriteFile, WritePath: "bin/plugin.dll", WriteContent: "assembly"},
				{Type: InstallStepWriteModuleJSON},
			},
		}
		if err := manager.Install(context.Background(), module, nil); err != nil {
			t.Fatalf("Install() error = %v", err)
		}
		if got := string(readTestFile(t, filepath.Join(manager.UserPluginsDir(), module.ID, "bin", "plugin.dll"))); got != "assembly" {
			t.Fatalf("installed assembly = %q", got)
		}
	})
}

func TestInstallValidatesSherpaOnnxRuntimeFiles(t *testing.T) {
	t.Run("model requires all path metadata", func(t *testing.T) {
		manager := newTestManager(t, "https://marketplace.invalid/marketplace.json")
		module := ModuleInfo{
			ID:           "model",
			Version:      1,
			Type:         ModuleTypeSherpaOnnxModel,
			InstallSteps: []InstallStep{{Type: InstallStepWriteModuleJSON}},
		}
		if err := manager.Install(context.Background(), module, nil); !errors.Is(err, ErrInvalidModule) {
			t.Fatalf("Install() error = %v, want invalid module", err)
		}
	})

	t.Run("model paths must point to installed regular files", func(t *testing.T) {
		manager := newTestManager(t, "https://marketplace.invalid/marketplace.json")
		paths := &SherpaOnnxModelPathInfo{
			EncoderPath: "model/encoder.onnx",
			DecoderPath: "model/decoder.onnx",
			JoinerPath:  "model/joiner.onnx",
			TokenPath:   "model/tokens.txt",
		}
		module := ModuleInfo{
			ID:                  "model",
			Version:             1,
			Type:                ModuleTypeSherpaOnnxModel,
			SherpaOnnxModelPath: paths,
			InstallSteps: []InstallStep{
				{Type: InstallStepWriteFile, WritePath: paths.EncoderPath, WriteContent: "encoder"},
				{Type: InstallStepWriteFile, WritePath: paths.DecoderPath, WriteContent: "decoder"},
				{Type: InstallStepWriteFile, WritePath: paths.JoinerPath, WriteContent: "joiner"},
				{Type: InstallStepWriteFile, WritePath: paths.TokenPath, WriteContent: "tokens"},
				{Type: InstallStepWriteModuleJSON},
			},
		}
		if err := manager.Install(context.Background(), module, nil); err != nil {
			t.Fatalf("Install() error = %v", err)
		}
		for _, relative := range []string{paths.EncoderPath, paths.DecoderPath, paths.JoinerPath, paths.TokenPath} {
			if info, err := os.Lstat(filepath.Join(manager.UserPluginsDir(), module.ID, filepath.FromSlash(relative))); err != nil || !info.Mode().IsRegular() {
				t.Errorf("runtime file %q: info=%v error=%v", relative, info, err)
			}
		}
	})
}
