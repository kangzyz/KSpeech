package resource

import (
	"archive/tar"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallDownloadsAndExtractsSupportedArchives(t *testing.T) {
	const tarBzip2Base64 = "QlpoOTFBWSZTWeMARU0AAHtbgMqEQAH3AEAAdyfecAgIIAB0GlDE0A0epoaGQ2oJKQaaaAAAAPuXkCEE8SEIo4ZIVvc1AhgMMNXZQ1NE1ghrSQMVTzflitzSiZS2dCIhp2klsS9SuIsDfZogwdM7EeREB+LuSKcKEhxgCKmg"
	tarBzip2, err := base64.StdEncoding.DecodeString(tarBzip2Base64)
	if err != nil {
		t.Fatalf("decode tar.bz2 fixture: %v", err)
	}

	tests := []struct {
		name    string
		format  string
		suffix  string
		archive func(*testing.T) []byte
	}{
		{
			name:   "zip",
			format: "zip",
			suffix: ".zip",
			archive: func(t *testing.T) []byte {
				return makeTestArchive(t, "zip", []testArchiveEntry{{name: "payload/file.txt", contents: []byte("installed from zip")}})
			},
		},
		{
			name:   "tar",
			format: "tar",
			suffix: ".tar",
			archive: func(t *testing.T) []byte {
				return makeTestArchive(t, "tar", []testArchiveEntry{{name: "payload/file.txt", contents: []byte("installed from tar")}})
			},
		},
		{
			name:   "tar.gz",
			format: "tar.gz",
			suffix: ".tar.gz",
			archive: func(t *testing.T) []byte {
				return makeTestArchive(t, "tar.gz", []testArchiveEntry{{name: "payload/file.txt", contents: []byte("installed from tar.gz")}})
			},
		},
		{
			name:   "tar.bz2",
			format: "tar.bz2",
			suffix: ".tar.bz2",
			archive: func(*testing.T) []byte {
				return tarBzip2
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archive := test.archive(t)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/module"+test.suffix {
					http.NotFound(writer, request)
					return
				}
				_, _ = writer.Write(archive)
			}))
			t.Cleanup(server.Close)

			manager := newTestManager(t, server.URL)
			module := ModuleInfo{
				ID:      "archive-" + strings.ReplaceAll(test.name, ".", "-"),
				Version: 7,
				Name:    "Archive fixture",
				InstallSteps: []InstallStep{
					{Type: InstallStepDownload, DownloadURL: server.URL + "/module" + test.suffix, SHA256: "sha256:" + testSHA256(archive)},
					{Type: InstallStepExtract},
					{Type: InstallStepWriteModuleJSON},
				},
			}
			var progress []Progress
			if err := manager.Install(context.Background(), module, func(update Progress) { progress = append(progress, update) }); err != nil {
				t.Fatalf("Install() error = %v", err)
			}

			wantContents := "installed from " + test.format
			gotContents := string(readTestFile(t, filepath.Join(manager.UserPluginsDir(), module.ID, "payload", "file.txt")))
			if gotContents != wantContents {
				t.Fatalf("installed payload = %q, want %q", gotContents, wantContents)
			}
			installed, err := readModuleInfo(filepath.Join(manager.UserPluginsDir(), module.ID, ModuleJSONName))
			if err != nil {
				t.Fatalf("read installed manifest: %v", err)
			}
			if installed.ID != module.ID || installed.Version != module.Version {
				t.Fatalf("installed manifest = %+v, want ID=%q Version=%d", installed, module.ID, module.Version)
			}
			seen := make(map[ProgressStage]bool)
			for _, update := range progress {
				seen[update.Stage] = true
			}
			for _, stage := range []ProgressStage{ProgressPreparing, ProgressDownloading, ProgressExtracting, ProgressWriting, ProgressActivating, ProgressComplete} {
				if !seen[stage] {
					t.Errorf("progress did not report stage %q: %+v", stage, progress)
				}
			}
		})
	}
}

func TestInstallChecksumMismatchPreservesInstalledVersion(t *testing.T) {
	archive := makeTestArchive(t, "zip", []testArchiveEntry{{name: "payload/new.txt", contents: []byte("new")}})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(archive)
	}))
	t.Cleanup(server.Close)

	manager := newTestManager(t, server.URL)
	oldDir := writeTestModule(t, manager.UserPluginsDir(), "module", ModuleInfo{ID: "module", Version: 1})
	writeTestFile(t, filepath.Join(oldDir, "old.txt"), []byte("keep me"))
	module := ModuleInfo{
		ID:      "module",
		Version: 2,
		InstallSteps: []InstallStep{
			{Type: InstallStepDownload, DownloadURL: server.URL + "/module.zip", SHA256: strings.Repeat("0", 64)},
			{Type: InstallStepExtract},
			{Type: InstallStepWriteModuleJSON},
		},
	}

	err := manager.Install(context.Background(), module, nil)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("Install() error = %v, want checksum mismatch", err)
	}
	installed, readErr := readModuleInfo(filepath.Join(oldDir, ModuleJSONName))
	if readErr != nil {
		t.Fatalf("read preserved manifest: %v", readErr)
	}
	if installed.Version != 1 {
		t.Fatalf("preserved manifest version = %d, want 1", installed.Version)
	}
	if got := string(readTestFile(t, filepath.Join(oldDir, "old.txt"))); got != "keep me" {
		t.Fatalf("old payload = %q, want keep me", got)
	}
}

func TestInstallRejectsTraversalAndArchiveLinks(t *testing.T) {
	tests := []struct {
		name    string
		format  string
		suffix  string
		entries []testArchiveEntry
	}{
		{name: "zip parent traversal", format: "zip", suffix: ".zip", entries: []testArchiveEntry{{name: "../escape.txt", contents: []byte("escape")}}},
		{name: "zip Windows traversal", format: "zip", suffix: ".zip", entries: []testArchiveEntry{{name: `..\escape.txt`, contents: []byte("escape")}}},
		{name: "tar parent traversal", format: "tar", suffix: ".tar", entries: []testArchiveEntry{{name: "../escape.txt", contents: []byte("escape")}}},
		{name: "zip symlink", format: "zip", suffix: ".zip", entries: []testArchiveEntry{{name: "payload/link", mode: os.ModeSymlink | 0o777, linkname: "../outside"}}},
		{name: "tar symlink", format: "tar", suffix: ".tar", entries: []testArchiveEntry{{name: "payload/link", typeflag: tar.TypeSymlink, linkname: "../outside"}}},
		{name: "tar hard link", format: "tar", suffix: ".tar", entries: []testArchiveEntry{{name: "payload/link", typeflag: tar.TypeLink, linkname: "payload/target"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archive := makeTestArchive(t, test.format, test.entries)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write(archive)
			}))
			t.Cleanup(server.Close)
			manager := newTestManager(t, server.URL)
			module := ModuleInfo{
				ID:      "unsafe",
				Version: 1,
				InstallSteps: []InstallStep{
					{Type: InstallStepDownload, DownloadURL: server.URL + "/unsafe" + test.suffix},
					{Type: InstallStepExtract},
					{Type: InstallStepWriteModuleJSON},
				},
			}
			err := manager.Install(context.Background(), module, nil)
			if !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("Install() error = %v, want unsafe path", err)
			}
			if _, statErr := os.Stat(filepath.Join(manager.UserPluginsDir(), module.ID)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("unsafe module target exists or cannot be checked: %v", statErr)
			}
		})
	}
}

func TestInstallWriteFileIsConfinedToModule(t *testing.T) {
	t.Run("writes nested regular file", func(t *testing.T) {
		manager := newTestManager(t, "https://marketplace.invalid/marketplace.json")
		module := ModuleInfo{
			ID:      "write-file",
			Version: 1,
			InstallSteps: []InstallStep{
				{Type: InstallStepWriteFile, WritePath: "config/settings.ini", WriteContent: "enabled=true\n"},
				{Type: InstallStepWriteModuleJSON},
			},
		}
		if err := manager.Install(context.Background(), module, nil); err != nil {
			t.Fatalf("Install() error = %v", err)
		}
		path := filepath.Join(manager.UserPluginsDir(), module.ID, "config", "settings.ini")
		if got := string(readTestFile(t, path)); got != "enabled=true\n" {
			t.Fatalf("written file = %q", got)
		}
	})

	paths := []string{"../escape.txt", `..\escape.txt`, "/absolute.txt", `\\server\share\escape.txt`}
	if runtime.GOOS == "windows" {
		paths = append(paths, `C:\escape.txt`)
	}
	for _, path := range paths {
		t.Run(strings.ReplaceAll(path, "\\", "_"), func(t *testing.T) {
			manager := newTestManager(t, "https://marketplace.invalid/marketplace.json")
			module := ModuleInfo{
				ID:      "write-file",
				Version: 1,
				InstallSteps: []InstallStep{
					{Type: InstallStepWriteFile, WritePath: path, WriteContent: "escape"},
					{Type: InstallStepWriteModuleJSON},
				},
			}
			err := manager.Install(context.Background(), module, nil)
			if !errors.Is(err, ErrInvalidModule) {
				t.Fatalf("Install() error = %v, want invalid module for WritePath %q", err, path)
			}
		})
	}
}

// A model published as loose files rather than one archive is installed with a
// download/save_file pair per file. This is the shape the X-ASR zh-en entries
// use, so the test asserts the whole plan, including that the saved files
// satisfy the sherpaonnx runtime path check.
func TestInstallSaveFileStoresLooseModelFiles(t *testing.T) {
	files := map[string][]byte{
		"/encoder.onnx": []byte("encoder bytes"),
		"/decoder.onnx": []byte("decoder bytes"),
		"/joiner.onnx":  []byte("joiner bytes"),
		"/tokens.txt":   []byte("<blk> 0\n"),
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		contents, exists := files[request.URL.Path]
		if !exists {
			http.NotFound(writer, request)
			return
		}
		_, _ = writer.Write(contents)
	}))
	t.Cleanup(server.Close)

	manager := newTestManager(t, server.URL)
	var steps []InstallStep
	for _, name := range []string{"encoder.onnx", "decoder.onnx", "joiner.onnx", "tokens.txt"} {
		steps = append(steps,
			InstallStep{Type: InstallStepDownload, DownloadURL: server.URL + "/" + name, SHA256: "sha256:" + testSHA256(files["/"+name])},
			InstallStep{Type: InstallStepSaveFile, SavePath: "model/" + name},
		)
	}
	steps = append(steps, InstallStep{Type: InstallStepWriteModuleJSON})
	module := ModuleInfo{
		ID:      "loose-model",
		Version: 20260729,
		Name:    "Loose model fixture",
		Type:    ModuleTypeSherpaOnnxModel,
		SherpaOnnxModelPath: &SherpaOnnxModelPathInfo{
			EncoderPath: "model/encoder.onnx",
			DecoderPath: "model/decoder.onnx",
			JoinerPath:  "model/joiner.onnx",
			TokenPath:   "model/tokens.txt",
		},
		InstallSteps: steps,
	}
	if err := manager.Install(context.Background(), module, nil); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	for path, contents := range files {
		installed := filepath.Join(manager.UserPluginsDir(), module.ID, "model", strings.TrimPrefix(path, "/"))
		if got := string(readTestFile(t, installed)); got != string(contents) {
			t.Errorf("installed %s = %q, want %q", path, got, contents)
		}
	}
}

// save_file reuses the download artifact rather than consuming it, so two save
// steps may point at the same download.
func TestInstallSaveFileCanReuseOneDownload(t *testing.T) {
	payload := []byte("shared artifact")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(payload)
	}))
	t.Cleanup(server.Close)

	manager := newTestManager(t, server.URL)
	source := 0
	module := ModuleInfo{
		ID:      "shared-download",
		Version: 1,
		Name:    "Shared download fixture",
		InstallSteps: []InstallStep{
			{Type: InstallStepDownload, DownloadURL: server.URL + "/artifact.bin"},
			{Type: InstallStepSaveFile, SavePath: "first.bin"},
			{Type: InstallStepSaveFile, SaveStep: &source, SavePath: "nested/second.bin"},
			{Type: InstallStepWriteModuleJSON},
		},
	}
	if err := manager.Install(context.Background(), module, nil); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	for _, relative := range []string{"first.bin", filepath.Join("nested", "second.bin")} {
		got := readTestFile(t, filepath.Join(manager.UserPluginsDir(), module.ID, relative))
		if string(got) != string(payload) {
			t.Errorf("installed %s = %q, want %q", relative, got, payload)
		}
	}
}

func TestInstallSaveFileRejectsUnsafePlans(t *testing.T) {
	savePaths := []string{"", "../escape.bin", `..\escape.bin`, "/absolute.bin", `\\server\share\escape.bin`}
	if runtime.GOOS == "windows" {
		savePaths = append(savePaths, `C:\escape.bin`)
	}
	for _, savePath := range savePaths {
		t.Run("savePath "+strings.ReplaceAll(savePath, "\\", "_"), func(t *testing.T) {
			manager := newTestManager(t, "https://marketplace.invalid/marketplace.json")
			module := ModuleInfo{
				ID:      "unsafe-save",
				Version: 1,
				InstallSteps: []InstallStep{
					{Type: InstallStepDownload, DownloadURL: "https://marketplace.invalid/artifact.bin"},
					{Type: InstallStepSaveFile, SavePath: savePath},
					{Type: InstallStepWriteModuleJSON},
				},
			}
			err := manager.Install(context.Background(), module, nil)
			if !errors.Is(err, ErrInvalidModule) {
				t.Fatalf("Install() error = %v, want invalid module for savePath %q", err, savePath)
			}
			if _, statErr := os.Stat(filepath.Join(manager.UserPluginsDir(), module.ID)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("unsafe module target exists or cannot be checked: %v", statErr)
			}
		})
	}

	forward := 2
	sourceSteps := map[string]*int{
		"references a non-download step": nil,
		"references a later step":        &forward,
	}
	for name, saveStep := range sourceSteps {
		t.Run(name, func(t *testing.T) {
			manager := newTestManager(t, "https://marketplace.invalid/marketplace.json")
			module := ModuleInfo{
				ID:      "unsafe-save-step",
				Version: 1,
				InstallSteps: []InstallStep{
					{Type: InstallStepWriteFile, WritePath: "note.txt", WriteContent: "not a download"},
					{Type: InstallStepSaveFile, SaveStep: saveStep, SavePath: "copy.bin"},
					{Type: InstallStepWriteModuleJSON},
				},
			}
			if err := manager.Install(context.Background(), module, nil); !errors.Is(err, ErrInvalidModule) {
				t.Fatalf("Install() error = %v, want invalid module", err)
			}
		})
	}
}

func TestSaveFileStepRejectsSymlinkComponents(t *testing.T) {
	moduleRoot := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(moduleRoot, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks are not available: %v", err)
	}
	downloadRoot := t.TempDir()
	artifact := filepath.Join(downloadRoot, "0.download")
	writeTestFile(t, artifact, []byte("escape"))

	manager := newTestManager(t, "https://marketplace.invalid/marketplace.json")
	downloads := map[int]downloadArtifact{0: {path: artifact, url: "https://marketplace.invalid/artifact.bin"}}
	err := manager.saveFileStep(context.Background(), 1, InstallStep{SavePath: "linked/escape.bin"}, downloads, moduleRoot)
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("saveFileStep() error = %v, want unsafe path", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "escape.bin")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("save escaped through symlink, Stat() error = %v", statErr)
	}
}

func TestWriteFileStepRejectsSymlinkComponents(t *testing.T) {
	moduleRoot := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(moduleRoot, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks are not available: %v", err)
	}
	err := writeFileStep(moduleRoot, InstallStep{WritePath: "linked/escape.txt", WriteContent: "escape"})
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("writeFileStep() error = %v, want unsafe path", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "escape.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("write escaped through symlink, Stat() error = %v", statErr)
	}
}

func TestActivateDirectoryRollsBackWhenActivationFails(t *testing.T) {
	manager := newTestManager(t, "https://marketplace.invalid/marketplace.json")
	if err := os.MkdirAll(manager.UserPluginsDir(), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	target := writeTestModule(t, manager.UserPluginsDir(), "module", ModuleInfo{ID: "module", Version: 1})
	writeTestFile(t, filepath.Join(target, "old.txt"), []byte("preserved"))

	// Keeping the staged directory beneath the target makes its original path
	// disappear when activation backs up the target. The second rename must
	// fail, exercising the real rollback branch without platform-specific hooks.
	staged := filepath.Join(target, "staged")
	if err := os.Mkdir(staged, 0o755); err != nil {
		t.Fatalf("Mkdir(%q) error = %v", staged, err)
	}
	if err := manager.activateDirectory(staged, target); err == nil {
		t.Fatal("activateDirectory() error = nil, want activation failure")
	}
	if got := string(readTestFile(t, filepath.Join(target, "old.txt"))); got != "preserved" {
		t.Fatalf("old payload after rollback = %q, want preserved", got)
	}
	info, err := readModuleInfo(filepath.Join(target, ModuleJSONName))
	if err != nil {
		t.Fatalf("read rolled-back manifest: %v", err)
	}
	if info.Version != 1 {
		t.Fatalf("rolled-back manifest version = %d, want 1", info.Version)
	}
	backups, err := filepath.Glob(filepath.Join(manager.UserPluginsDir(), ".kspeech-backup-*"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(backups) != 0 {
		t.Fatalf("rollback left backup directories: %v", backups)
	}
}

func TestInstallReportsUnsupportedCustomArchive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("not a supported archive"))
	}))
	t.Cleanup(server.Close)
	manager := newTestManager(t, server.URL)
	module := ModuleInfo{
		ID:      "unsupported",
		Version: 1,
		InstallSteps: []InstallStep{
			{Type: InstallStepDownload, DownloadURL: server.URL + "/module.bin"},
			{Type: InstallStepExtract, ExtractType: "rar"},
			{Type: InstallStepWriteModuleJSON},
		},
	}
	if err := manager.Install(context.Background(), module, nil); !errors.Is(err, ErrUnsupportedArchive) {
		t.Fatalf("Install() error = %v, want unsupported archive", err)
	}
}

// A punctuation resource carries one model file. Installing it must keep that
// path, and a manifest that declares the type without the path must be refused
// before anything is downloaded.
func TestInstallPunctuationModelKeepsItsModelPath(t *testing.T) {
	archive := makeTestArchive(t, "tar.gz", []testArchiveEntry{
		{name: "punct-model/model.int8.onnx", contents: []byte("onnx")},
	})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(archive)
	}))
	t.Cleanup(server.Close)

	manager := newTestManager(t, server.URL)
	module := ModuleInfo{
		ID:              "punct-fixture",
		Version:         20240412,
		Name:            "Punctuation fixture",
		Type:            ModuleTypePunctuationModel,
		PunctuationPath: &PunctuationModelPathInfo{ModelPath: "punct-model/model.int8.onnx"},
		InstallSteps: []InstallStep{
			{Type: InstallStepDownload, DownloadURL: server.URL + "/module.tar.gz", SHA256: "sha256:" + testSHA256(archive)},
			{Type: InstallStepExtract},
			{Type: InstallStepWriteModuleJSON},
		},
	}
	if err := manager.Install(context.Background(), module, nil); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	installed, err := readModuleInfo(filepath.Join(manager.UserPluginsDir(), module.ID, ModuleJSONName))
	if err != nil {
		t.Fatal(err)
	}
	if installed.PunctuationPath == nil || installed.PunctuationPath.ModelPath != module.PunctuationPath.ModelPath {
		t.Fatalf("installed punctuation path = %+v, want %q", installed.PunctuationPath, module.PunctuationPath.ModelPath)
	}

	incomplete := module
	incomplete.ID = "punct-without-path"
	incomplete.PunctuationPath = nil
	if err := manager.Install(context.Background(), incomplete, nil); !errors.Is(err, ErrInvalidModule) {
		t.Fatalf("Install() error = %v, want ErrInvalidModule", err)
	}
}
