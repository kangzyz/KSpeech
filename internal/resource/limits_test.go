package resource

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNewManagerRejectsPlaintextMarketplaceByDefault(t *testing.T) {
	root := t.TempDir()
	_, err := NewManager(Options{
		ExecutableDir:  filepath.Join(root, "application"),
		UserDataDir:    filepath.Join(root, "user-data"),
		MarketplaceURL: "http://127.0.0.1/marketplace.json",
	})
	if !errors.Is(err, ErrInsecureTransport) {
		t.Fatalf("NewManager() error = %v, want insecure transport", err)
	}

	if _, err := NewManager(Options{
		ExecutableDir:     filepath.Join(root, "application"),
		UserDataDir:       filepath.Join(root, "user-data-allowed"),
		MarketplaceURL:    "http://127.0.0.1/marketplace.json",
		AllowInsecureHTTP: true,
	}); err != nil {
		t.Fatalf("NewManager() with explicit HTTP opt-in error = %v", err)
	}
}

func TestInstallRejectsPlaintextDownloadByDefault(t *testing.T) {
	root := t.TempDir()
	manager, err := NewManager(Options{
		ExecutableDir:  filepath.Join(root, "application"),
		UserDataDir:    filepath.Join(root, "user-data"),
		MarketplaceURL: "https://marketplace.invalid/marketplace.json",
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	module := ModuleInfo{
		ID:      "plaintext-download",
		Version: 1,
		InstallSteps: []InstallStep{
			{Type: InstallStepDownload, DownloadURL: "http://127.0.0.1/module.zip"},
			{Type: InstallStepWriteModuleJSON},
		},
	}
	err = manager.Install(context.Background(), module, nil)
	if !errors.Is(err, ErrInsecureTransport) || !errors.Is(err, ErrInvalidModule) {
		t.Fatalf("Install() error = %v, want invalid module and insecure transport", err)
	}
}

func TestInstallRejectsHTTPSRedirectToPlaintext(t *testing.T) {
	var plaintextRequests atomic.Int32
	plaintext := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		plaintextRequests.Add(1)
		_, _ = writer.Write([]byte("should not be fetched"))
	}))
	defer plaintext.Close()
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, plaintext.URL+"/module.zip", http.StatusFound)
	}))
	defer tlsServer.Close()

	root := t.TempDir()
	manager, err := NewManager(Options{
		ExecutableDir:  filepath.Join(root, "application"),
		UserDataDir:    filepath.Join(root, "user-data"),
		MarketplaceURL: "https://marketplace.invalid/marketplace.json",
		HTTPClient:     tlsServer.Client(),
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	module := ModuleInfo{
		ID:      "redirect-downgrade",
		Version: 1,
		InstallSteps: []InstallStep{
			{Type: InstallStepDownload, DownloadURL: tlsServer.URL + "/module.zip"},
			{Type: InstallStepWriteModuleJSON},
		},
	}
	err = manager.Install(context.Background(), module, nil)
	if !errors.Is(err, ErrInsecureTransport) {
		t.Fatalf("Install() error = %v, want insecure transport", err)
	}
	if requests := plaintextRequests.Load(); requests != 0 {
		t.Fatalf("plaintext redirect target received %d requests, want zero", requests)
	}
}

func TestInstallEnforcesDownloadLimitWithoutContentLength(t *testing.T) {
	payload := strings.Repeat("x", 65)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		flusher, ok := writer.(http.Flusher)
		if !ok {
			t.Error("httptest response does not support flushing")
			return
		}
		flusher.Flush()
		_, _ = writer.Write([]byte(payload))
	}))
	defer server.Close()

	manager := newTestManager(t, "https://marketplace.invalid/marketplace.json", func(options *Options) {
		options.MaxDownloadBytes = 64
	})
	module := ModuleInfo{
		ID:      "download-limit",
		Version: 1,
		InstallSteps: []InstallStep{
			{Type: InstallStepDownload, DownloadURL: server.URL + "/module.bin"},
			{Type: InstallStepWriteModuleJSON},
		},
	}
	err := manager.Install(context.Background(), module, nil)
	if !errors.Is(err, ErrDownloadLimit) {
		t.Fatalf("Install() error = %v, want download limit", err)
	}
	assertNoInstalledModule(t, manager, module.ID)
}

func TestInstallEnforcesDeclaredDownloadLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Length", "65")
		_, _ = writer.Write([]byte(strings.Repeat("x", 65)))
	}))
	defer server.Close()

	manager := newTestManager(t, "https://marketplace.invalid/marketplace.json", func(options *Options) {
		options.MaxDownloadBytes = 64
	})
	module := ModuleInfo{
		ID:      "declared-download-limit",
		Version: 1,
		InstallSteps: []InstallStep{
			{Type: InstallStepDownload, DownloadURL: server.URL + "/module.bin"},
			{Type: InstallStepWriteModuleJSON},
		},
	}
	if err := manager.Install(context.Background(), module, nil); !errors.Is(err, ErrDownloadLimit) {
		t.Fatalf("Install() error = %v, want download limit", err)
	}
	assertNoInstalledModule(t, manager, module.ID)
}

func TestInstallEnforcesCumulativeTransactionDownloadLimit(t *testing.T) {
	payload := strings.Repeat("x", 40)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		flusher, ok := writer.(http.Flusher)
		if !ok {
			t.Error("httptest response does not support flushing")
			return
		}
		flusher.Flush()
		_, _ = writer.Write([]byte(payload))
	}))
	defer server.Close()

	manager := newTestManager(t, "https://marketplace.invalid/marketplace.json", func(options *Options) {
		options.MaxDownloadBytes = 64
		options.MaxTransactionDownloadBytes = 64
	})
	module := ModuleInfo{
		ID:      "cumulative-download-limit",
		Version: 1,
		InstallSteps: []InstallStep{
			{Type: InstallStepDownload, DownloadURL: server.URL + "/first.bin"},
			{Type: InstallStepDownload, DownloadURL: server.URL + "/second.bin"},
			{Type: InstallStepWriteModuleJSON},
		},
	}
	if err := manager.Install(context.Background(), module, nil); !errors.Is(err, ErrDownloadLimit) || errors.Is(err, ErrInstallSizeLimit) {
		t.Fatalf("Install() error = %v, want only cumulative download limit", err)
	}
	assertNoInstalledModule(t, manager, module.ID)
}

func TestInstallRejectsPlanOverStepLimitBeforeMutation(t *testing.T) {
	manager := newTestManager(t, "https://marketplace.invalid/marketplace.json", func(options *Options) {
		options.MaxInstallSteps = 2
	})
	module := ModuleInfo{
		ID:      "too-many-steps",
		Version: 1,
		InstallSteps: []InstallStep{
			{Type: InstallStepWriteFile, WritePath: "one.txt", WriteContent: "one"},
			{Type: InstallStepWriteFile, WritePath: "two.txt", WriteContent: "two"},
			{Type: InstallStepWriteModuleJSON},
		},
	}
	if err := manager.Install(context.Background(), module, nil); !errors.Is(err, ErrInvalidModule) {
		t.Fatalf("Install() error = %v, want invalid module", err)
	}
	if _, err := os.Lstat(manager.UserPluginsDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("overlong plan mutated user resource root: %v", err)
	}
}

func TestInstallEnforcesCumulativeArchiveLimit(t *testing.T) {
	for _, archiveType := range []string{"zip", "tar", "tar.gz"} {
		t.Run(archiveType, func(t *testing.T) {
			archive := makeTestArchive(t, archiveType, []testArchiveEntry{
				{name: "first.bin", contents: []byte(strings.Repeat("a", 40))},
				{name: "second.bin", contents: []byte(strings.Repeat("b", 25))},
			})
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write(archive)
			}))
			defer server.Close()

			manager := newTestManager(t, "https://marketplace.invalid/marketplace.json", func(options *Options) {
				options.MaxInstallBytes = 64
			})
			module := ModuleInfo{
				ID:      "archive-limit-" + strings.ReplaceAll(archiveType, ".", "-"),
				Version: 1,
				InstallSteps: []InstallStep{
					{Type: InstallStepDownload, DownloadURL: server.URL + "/module." + archiveType},
					{Type: InstallStepExtract, ExtractType: archiveType},
					{Type: InstallStepWriteModuleJSON},
				},
			}
			if err := manager.Install(context.Background(), module, nil); !errors.Is(err, ErrInstallSizeLimit) {
				t.Fatalf("Install() error = %v, want install size limit", err)
			}
			assertNoInstalledModule(t, manager, module.ID)
		})
	}
}

func TestInstallRejectsHighlyCompressedZIPOverLimit(t *testing.T) {
	archive := makeTestArchive(t, "zip", []testArchiveEntry{
		{name: "expanded.bin", contents: []byte(strings.Repeat("z", 1<<20))},
	})
	const outputLimit = 64 << 10
	if len(archive) >= outputLimit {
		t.Fatalf("zip fixture is not sufficiently compressed: %d bytes", len(archive))
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(archive)
	}))
	defer server.Close()
	manager := newTestManager(t, "https://marketplace.invalid/marketplace.json", func(options *Options) {
		options.MaxInstallBytes = outputLimit
	})
	module := ModuleInfo{
		ID:      "compressed-zip-limit",
		Version: 1,
		InstallSteps: []InstallStep{
			{Type: InstallStepDownload, DownloadURL: server.URL + "/module.zip"},
			{Type: InstallStepExtract},
			{Type: InstallStepWriteModuleJSON},
		},
	}
	if err := manager.Install(context.Background(), module, nil); !errors.Is(err, ErrInstallSizeLimit) {
		t.Fatalf("Install() error = %v, want install size limit", err)
	}
	assertNoInstalledModule(t, manager, module.ID)
}

func TestInstallEnforcesTarBzip2ArchiveLimit(t *testing.T) {
	const tarBzip2Base64 = "QlpoOTFBWSZTWeMARU0AAHtbgMqEQAH3AEAAdyfecAgIIAB0GlDE0A0epoaGQ2oJKQaaaAAAAPuXkCEE8SEIo4ZIVvc1AhgMMNXZQ1NE1ghrSQMVTzflitzSiZS2dCIhp2klsS9SuIsDfZogwdM7EeREB+LuSKcKEhxgCKmg"
	archive, err := base64.StdEncoding.DecodeString(tarBzip2Base64)
	if err != nil {
		t.Fatalf("decode tar.bz2 fixture: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(archive)
	}))
	defer server.Close()
	manager := newTestManager(t, "https://marketplace.invalid/marketplace.json", func(options *Options) {
		options.MaxInstallBytes = 8
	})
	module := ModuleInfo{
		ID:      "tar-bzip2-limit",
		Version: 1,
		InstallSteps: []InstallStep{
			{Type: InstallStepDownload, DownloadURL: server.URL + "/module.tar.bz2"},
			{Type: InstallStepExtract},
			{Type: InstallStepWriteModuleJSON},
		},
	}
	if err := manager.Install(context.Background(), module, nil); !errors.Is(err, ErrInstallSizeLimit) {
		t.Fatalf("Install() error = %v, want install size limit", err)
	}
	assertNoInstalledModule(t, manager, module.ID)
}

func TestInstallEnforcesCumulativeWriteFileLimit(t *testing.T) {
	manager := newTestManager(t, "https://marketplace.invalid/marketplace.json", func(options *Options) {
		options.MaxInstallBytes = 64
	})
	module := ModuleInfo{
		ID:      "write-limit",
		Version: 1,
		InstallSteps: []InstallStep{
			{Type: InstallStepWriteFile, WritePath: "first.txt", WriteContent: strings.Repeat("a", 40)},
			{Type: InstallStepWriteFile, WritePath: "second.txt", WriteContent: strings.Repeat("b", 25)},
			{Type: InstallStepWriteModuleJSON},
		},
	}
	if err := manager.Install(context.Background(), module, nil); !errors.Is(err, ErrInstallSizeLimit) {
		t.Fatalf("Install() error = %v, want install size limit", err)
	}
	assertNoInstalledModule(t, manager, module.ID)
}

func TestInstallSharesBudgetAcrossExtractAndWriteFile(t *testing.T) {
	archive := makeTestArchive(t, "zip", []testArchiveEntry{
		{name: "from-archive.bin", contents: []byte(strings.Repeat("a", 40))},
	})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(archive)
	}))
	defer server.Close()
	manager := newTestManager(t, "https://marketplace.invalid/marketplace.json", func(options *Options) {
		options.MaxInstallBytes = 64
	})
	module := ModuleInfo{
		ID:      "shared-install-budget",
		Version: 1,
		InstallSteps: []InstallStep{
			{Type: InstallStepDownload, DownloadURL: server.URL + "/module.zip"},
			{Type: InstallStepExtract},
			{Type: InstallStepWriteFile, WritePath: "written.bin", WriteContent: strings.Repeat("b", 25)},
			{Type: InstallStepWriteModuleJSON},
		},
	}
	if err := manager.Install(context.Background(), module, nil); !errors.Is(err, ErrInstallSizeLimit) {
		t.Fatalf("Install() error = %v, want shared install size limit", err)
	}
	assertNoInstalledModule(t, manager, module.ID)
}

func TestCustomExtractorFinalTreeIsLimited(t *testing.T) {
	manager := newTestManager(t, "https://marketplace.invalid/marketplace.json", func(options *Options) {
		options.MaxInstallBytes = 64
		options.Extractors = map[string]Extractor{
			"custom": func(_ context.Context, _, destination string) error {
				return os.WriteFile(filepath.Join(destination, "large.bin"), []byte(strings.Repeat("x", 65)), 0o644)
			},
		}
	})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("custom archive"))
	}))
	defer server.Close()
	module := ModuleInfo{
		ID:      "custom-limit",
		Version: 1,
		InstallSteps: []InstallStep{
			{Type: InstallStepDownload, DownloadURL: server.URL + "/module.custom"},
			{Type: InstallStepExtract, ExtractType: "custom"},
			{Type: InstallStepWriteModuleJSON},
		},
	}
	if err := manager.Install(context.Background(), module, nil); !errors.Is(err, ErrInstallSizeLimit) {
		t.Fatalf("Install() error = %v, want install size limit", err)
	}
	assertNoInstalledModule(t, manager, module.ID)
}

func TestNewManagerRejectsNegativeLimits(t *testing.T) {
	root := t.TempDir()
	base := Options{
		ExecutableDir:  filepath.Join(root, "application"),
		UserDataDir:    filepath.Join(root, "user-data"),
		MarketplaceURL: "https://marketplace.invalid/marketplace.json",
	}
	for name, configure := range map[string]func(*Options){
		"download":             func(options *Options) { options.MaxDownloadBytes = -1 },
		"transaction download": func(options *Options) { options.MaxTransactionDownloadBytes = -1 },
		"install":              func(options *Options) { options.MaxInstallBytes = -1 },
		"steps":                func(options *Options) { options.MaxInstallSteps = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			options := base
			configure(&options)
			if _, err := NewManager(options); err == nil {
				t.Fatal("NewManager() error = nil, want invalid limit")
			}
		})
	}
}

func assertNoInstalledModule(t *testing.T, manager *Manager, id string) {
	t.Helper()
	entries, err := os.ReadDir(manager.UserPluginsDir())
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatalf("ReadDir(user plugins) error = %v", err)
	}
	for _, entry := range entries {
		if entry.Name() == id {
			t.Fatalf("module %q was unexpectedly activated", id)
		}
		if !strings.HasPrefix(entry.Name(), ".kspeech-") {
			t.Logf("unrelated user plugin entry remains: %s", entry.Name())
		}
	}
}

func ExampleOptions_securityLimits() {
	_, _ = NewManager(Options{
		MarketplaceURL:              "https://example.test/marketplace.json",
		MaxDownloadBytes:            512 << 20,
		MaxTransactionDownloadBytes: 1 << 30,
		MaxInstallBytes:             1 << 30,
		MaxInstallSteps:             128,
	})
	fmt.Println("HTTPS-only with explicit byte limits")
	// Output: HTTPS-only with explicit byte limits
}
