package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyPluginKeys(t *testing.T) {
	t.Parallel()
	got := PluginKey("KSpeech.AudioSource.Windows", LoopbackAudioSourceID)
	want := "KSpeech:AudioSource:Windows!" + LoopbackAudioSourceID
	if got != want {
		t.Fatalf("PluginKey() = %q, want %q", got, want)
	}
	if got := PluginKey("vendor:name.with.dots", "id"); got != "vendor::name:with:dots!id" {
		t.Fatalf("escaped plugin key = %q", got)
	}
}

func TestDefaultsUseLegacyPluginKeys(t *testing.T) {
	t.Parallel()
	defaults := Defaults()
	if got := defaults[GeneralStartOnLaunch]; got != false {
		t.Fatalf("clean-install StartOnLaunch = %#v, want false", got)
	}
	if got, want := defaults[AudioSource], "KSpeech:AudioSource:Windows!"+LoopbackAudioSourceID; got != want {
		t.Fatalf("default audio source = %q, want %q", got, want)
	}
	if got, want := defaults[RecognizerSource], "KSpeech:Recognizer:SherpaOnnx!"+SherpaOnnxID; got != want {
		t.Fatalf("default recognizer = %q, want %q", got, want)
	}
}

func TestOpenMergesLegacyFlatConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	legacy := `{
  "general.StartOnLaunch": false,
  "appearance.FontColor": 4278255360,
  "general.MainWindowLocation": [10, 20, 800, 180],
  "plugin.KSpeech:Recognizer:Command!A1B2C3D4-5E6F-7890-ABCD-EF1234567890.config": "{\"Command\":\"python.exe\"}"
}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if store.Bool(GeneralStartOnLaunch) {
		t.Fatal("legacy boolean was not loaded")
	}
	if got := store.Uint32(AppearanceFontColor); got != 0xFF00FF00 {
		t.Fatalf("font color = %#x", got)
	}
	location := store.IntSlice(GeneralMainWindowLocation)
	if len(location) != 4 || location[2] != 800 {
		t.Fatalf("window location = %#v", location)
	}
	if store.String(GeneralLanguage) != "zh-cn" {
		t.Fatal("missing legacy keys must retain Go defaults")
	}
}

func TestSetIsPersistedAndPublished(t *testing.T) {
	t.Parallel()
	store, err := Open(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	changes, cancel := store.Subscribe(1)
	defer cancel()
	if err := store.Set(GeneralStartOnLaunch, false); err != nil {
		t.Fatal(err)
	}
	change := <-changes
	if len(change.Keys) != 1 || change.Keys[0] != GeneralStartOnLaunch {
		t.Fatalf("change = %#v", change)
	}
	reloaded, err := Open(filepath.Dir(store.Path()), "")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Bool(GeneralStartOnLaunch) {
		t.Fatal("saved setting was not reloaded")
	}
}

func TestOpenRecoversInterruptedWindowsStyleReplace(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	backupPath := filepath.Join(dir, "config.json.previous")
	if err := os.WriteFile(backupPath, []byte(`{"general.StartOnLaunch":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if store.Bool(GeneralStartOnLaunch) {
		t.Fatal("backup config was not recovered")
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
		t.Fatalf("recovered config missing: %v", err)
	}
	issues := store.Issues()
	if len(issues) != 1 || issues[0].Code != IssueBackupConfigRecovered {
		t.Fatalf("Issues() = %#v, want backup recovery warning", issues)
	}
}

func TestOpenRecoversMalformedConfigFromValidPrevious(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "config.json")
	badConfig := []byte(`{"general.StartOnLaunch":`)
	if err := os.WriteFile(mainPath, badConfig, 0o640); err != nil {
		t.Fatal(err)
	}
	previous := []byte(`{"general.StartOnLaunch":true}`)
	if err := os.WriteFile(mainPath+".previous", previous, 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := Open(dir, "")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if !store.Bool(GeneralStartOnLaunch) {
		t.Fatal("valid previous config was not loaded")
	}
	if got, err := os.ReadFile(mainPath); err != nil || !bytes.Equal(got, previous) {
		t.Fatalf("restored config = %q, %v; want %q", got, err, previous)
	}
	if _, err := os.Stat(mainPath + ".previous"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("previous config still exists or stat failed: %v", err)
	}
	issues := store.Issues()
	if len(issues) != 1 || issues[0].Code != IssueUserConfigRecovered {
		t.Fatalf("Issues() = %#v, want user config recovery warning", issues)
	}
	if got, err := os.ReadFile(issues[0].Path); err != nil || !bytes.Equal(got, badConfig) {
		t.Fatalf("quarantined config = %q, %v; want exact bytes %q", got, err, badConfig)
	}

	// The returned slice is a copy and cannot mutate the store's warnings.
	issues[0].Code = "changed"
	if got := store.Issues()[0].Code; got != IssueUserConfigRecovered {
		t.Fatalf("Issues() exposed mutable state: %q", got)
	}
}

func TestOpenQuarantinesMalformedConfigAndUsesLayeredDefaults(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "config.json")
	badConfig := []byte("{} trailing-data")
	if err := os.WriteFile(mainPath, badConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	oldQuarantine := mainPath + ".corrupt-existing"
	if err := os.WriteFile(oldQuarantine, []byte("older evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	packagedPath := filepath.Join(dir, "default_config.json")
	if err := os.WriteFile(packagedPath, []byte(`{"general.Language":"en-us"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := Open(dir, packagedPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if got := store.String(GeneralLanguage); got != "en-us" {
		t.Fatalf("packaged default language = %q, want en-us", got)
	}
	if got := store.Bool(GeneralStartOnLaunch); got {
		t.Fatal("built-in default was not retained")
	}
	if _, err := os.Stat(mainPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid main config still exists or stat failed: %v", err)
	}
	if got, err := os.ReadFile(oldQuarantine); err != nil || string(got) != "older evidence" {
		t.Fatalf("older quarantine was overwritten: %q, %v", got, err)
	}
	issues := store.Issues()
	if len(issues) != 1 || issues[0].Code != IssueUserConfigQuarantined {
		t.Fatalf("Issues() = %#v, want user config quarantine warning", issues)
	}
	if issues[0].Path == oldQuarantine {
		t.Fatal("new invalid config reused an existing quarantine path")
	}
	if got, err := os.ReadFile(issues[0].Path); err != nil || !bytes.Equal(got, badConfig) {
		t.Fatalf("quarantined config = %q, %v; want exact bytes %q", got, err, badConfig)
	}
}

func TestOpenQuarantinesMalformedPreviousWhenMainIsMissing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	previousPath := filepath.Join(dir, "config.json.previous")
	badPrevious := []byte(`{"truncated":`)
	if err := os.WriteFile(previousPath, badPrevious, 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := Open(dir, "")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	issues := store.Issues()
	if len(issues) != 1 || issues[0].Code != IssueBackupConfigQuarantined {
		t.Fatalf("Issues() = %#v, want invalid backup warning", issues)
	}
	if got, err := os.ReadFile(issues[0].Path); err != nil || !bytes.Equal(got, badPrevious) {
		t.Fatalf("quarantined previous = %q, %v; want exact bytes %q", got, err, badPrevious)
	}
	if got := store.String(GeneralLanguage); got != "zh-cn" {
		t.Fatalf("default language = %q, want zh-cn", got)
	}
}

func TestOpenReturnsHardErrorForNonFileConfigPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "config.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, ""); err == nil {
		t.Fatal("Open() error = nil for a config path that is a directory")
	}
}
