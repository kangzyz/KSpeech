package resource

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSaveFileAgainstTheRealHost is a manual check that a save_file plan works
// against the host the catalogue actually points at. HuggingFace redirects
// every file to a signed CDN URL, so this covers redirect validation, the
// identity Accept-Encoding, the Content-Length match and SHA256 pinning in one
// go. It downloads only tokens.txt from the X-ASR entry, roughly 60 KB, rather
// than the 593 MB encoder. Skipped unless KSPEECH_INSTALL_CHECK is set.
func TestSaveFileAgainstTheRealHost(t *testing.T) {
	if os.Getenv("KSPEECH_INSTALL_CHECK") == "" {
		t.Skip("set KSPEECH_INSTALL_CHECK=1 to run the live install check")
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "marketplace", "marketplace.json"))
	if err != nil {
		t.Fatalf("read marketplace catalogue: %v", err)
	}
	var catalogue Marketplace
	if err := json.Unmarshal(data, &catalogue); err != nil {
		t.Fatalf("decode marketplace catalogue: %v", err)
	}
	const moduleID = "x-asr.streaming-zipformer-zh-en-480ms"
	var module ModuleInfo
	for _, candidate := range catalogue.Modules {
		if candidate.ID == moduleID {
			module = candidate
			break
		}
	}
	if module.ID == "" {
		t.Fatalf("catalogue has no module %q", moduleID)
	}

	// Keep the tokens download/save pair and the manifest write, drop the three
	// ONNX pairs. The runtime paths would then point at missing files, so clear
	// them too; this exercises transport and save_file, not path validation.
	var steps []InstallStep
	for index, step := range module.InstallSteps {
		switch {
		case step.Type == InstallStepWriteModuleJSON:
			steps = append(steps, step)
		case step.Type == InstallStepDownload && filepath.Base(step.DownloadURL) == "tokens.txt":
			steps = append(steps, step, module.InstallSteps[index+1])
		}
	}
	module.InstallSteps = steps
	module.Type = ""
	module.SherpaOnnxModelPath = nil

	manager, err := NewManager(Options{ExecutableDir: t.TempDir(), UserDataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := manager.Install(context.Background(), module, nil); err != nil {
		t.Fatalf("install failed after %s: %v", time.Since(start).Round(time.Millisecond), err)
	}
	saved := filepath.Join(manager.UserPluginsDir(), module.ID, "x-asr-zh-en-480ms", "tokens.txt")
	info, err := os.Stat(saved)
	if err != nil {
		t.Fatalf("stat saved tokens: %v", err)
	}
	t.Logf("installed %s (%d bytes) in %s", saved, info.Size(), time.Since(start).Round(time.Millisecond))
	if info.Size() != 58806 {
		t.Errorf("saved tokens.txt = %d bytes, want 58806", info.Size())
	}
}
