package resource

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The shipped catalogue is what every installation downloads from
// DefaultMarketURL. A malformed install plan there fails on the user's machine
// at install time rather than here, so hold it to the same rules Install
// applies, with the production HTTPS-only policy rather than the test default.
func TestShippedMarketplaceCatalogueIsInstallable(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "marketplace", "marketplace.json"))
	if err != nil {
		t.Skipf("marketplace catalogue is unavailable: %v", err)
	}
	var catalogue Marketplace
	if err := json.Unmarshal(data, &catalogue); err != nil {
		t.Fatalf("decode marketplace catalogue: %v", err)
	}
	if len(catalogue.Modules) == 0 {
		t.Fatal("marketplace catalogue declares no modules")
	}
	manager := newTestManager(t, DefaultMarketURL, func(options *Options) {
		options.AllowInsecureHTTP = false
	})

	seen := make(map[string]bool)
	for _, module := range catalogue.Modules {
		if seen[module.ID] {
			t.Errorf("duplicate module id %q", module.ID)
		}
		seen[module.ID] = true
		t.Run(module.ID, func(t *testing.T) {
			if err := manager.validateInstallPlan(module); err != nil {
				t.Fatalf("validateInstallPlan() error = %v", err)
			}
			if module.Version <= 0 {
				t.Errorf("version = %d, want a positive value so updates are detectable", module.Version)
			}
			assertDeclaredPathsAreProduced(t, module)
		})
	}
}

// assertDeclaredPathsAreProduced catches the typo that plan validation cannot:
// a runtime path that no install step ever creates. Only plans built purely
// from save_file steps are checkable, because an archive's contents are not
// known until it is downloaded.
func assertDeclaredPathsAreProduced(t *testing.T, module ModuleInfo) {
	t.Helper()
	saved := make(map[string]bool)
	for _, step := range module.InstallSteps {
		switch step.Type {
		case InstallStepSaveFile:
			cleaned, err := cleanRelativePath(step.SavePath, false)
			if err != nil {
				t.Fatalf("savePath %q: %v", step.SavePath, err)
			}
			saved[cleaned] = true
		case InstallStepExtract:
			return
		}
	}
	if len(saved) == 0 {
		return
	}
	for _, declared := range declaredRuntimePaths(module) {
		cleaned, err := cleanRelativePath(declared, false)
		if err != nil {
			t.Fatalf("runtime path %q: %v", declared, err)
		}
		if !saved[cleaned] {
			t.Errorf("runtime path %q is never produced by an install step", declared)
		}
	}
}

func declaredRuntimePaths(module ModuleInfo) []string {
	paths := append([]string(nil), module.Assemblies...)
	if module.SherpaOnnxModelPath != nil {
		paths = append(paths,
			module.SherpaOnnxModelPath.EncoderPath,
			module.SherpaOnnxModelPath.DecoderPath,
			module.SherpaOnnxModelPath.JoinerPath,
			module.SherpaOnnxModelPath.TokenPath,
		)
	}
	if module.PunctuationPath != nil {
		paths = append(paths, module.PunctuationPath.ModelPath)
	}
	if module.SherpaNcnnModelPath != nil {
		paths = append(paths,
			module.SherpaNcnnModelPath.EncoderParamPath,
			module.SherpaNcnnModelPath.EncoderBinPath,
			module.SherpaNcnnModelPath.DecoderParamPath,
			module.SherpaNcnnModelPath.DecoderBinPath,
			module.SherpaNcnnModelPath.JoinerParamPath,
			module.SherpaNcnnModelPath.JoinerBinPath,
			module.SherpaNcnnModelPath.TokenPath,
		)
	}
	return paths
}
