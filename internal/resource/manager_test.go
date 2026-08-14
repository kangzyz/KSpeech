package resource

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanLocalMergesRootsAndUserModulesShadowBuiltIns(t *testing.T) {
	var issues []error
	manager := newTestManager(t, "https://marketplace.invalid/marketplace.json", func(options *Options) {
		options.OnIssue = func(err error) { issues = append(issues, err) }
	})

	writeTestModule(t, manager.BuiltInPluginsDir(), "alpha-old", ModuleInfo{ID: "alpha", Version: 1})
	writeTestModule(t, manager.BuiltInPluginsDir(), "alpha-new", ModuleInfo{ID: "alpha", Version: 5})
	writeTestModule(t, manager.BuiltInPluginsDir(), "capital-alpha", ModuleInfo{ID: "Alpha", Version: 1})
	writeTestModule(t, manager.BuiltInPluginsDir(), "beta-z", ModuleInfo{ID: "beta", Version: 3})
	writeTestModule(t, manager.BuiltInPluginsDir(), "beta-a", ModuleInfo{ID: "beta", Version: 3})
	writeTestModule(t, manager.UserPluginsDir(), "user-alpha", ModuleInfo{ID: "alpha", Version: 2})
	writeTestModule(t, manager.UserPluginsDir(), "gamma", ModuleInfo{ID: "gamma", Version: 4})

	writeTestFile(t, filepath.Join(manager.BuiltInPluginsDir(), "broken", ModuleJSONName), []byte(`{"id":`))
	writeTestModule(t, manager.BuiltInPluginsDir(), "empty-id", ModuleInfo{Version: 8})
	writeTestModule(t, manager.BuiltInPluginsDir(), "unsafe-id", ModuleInfo{ID: "../unsafe", Version: 1})
	writeTestModule(t, filepath.Join(manager.BuiltInPluginsDir(), "container"), "nested", ModuleInfo{ID: "nested", Version: 1})
	writeTestFile(t, filepath.Join(manager.BuiltInPluginsDir(), "not-a-directory"), []byte("ignored"))

	resources, err := manager.ScanLocal(context.Background())
	if err != nil {
		t.Fatalf("ScanLocal() error = %v", err)
	}
	wantIDs := []string{"Alpha", "alpha", "beta", "gamma"}
	if len(resources) != len(wantIDs) {
		t.Fatalf("ScanLocal() returned %d resources, want %d: %+v", len(resources), len(wantIDs), resources)
	}
	for index, wantID := range wantIDs {
		if resources[index].ID() != wantID {
			t.Errorf("resources[%d].ID() = %q, want %q", index, resources[index].ID(), wantID)
		}
	}

	byID := make(map[string]Resource, len(resources))
	for _, item := range resources {
		byID[item.ID()] = item
	}
	if got := byID["alpha"]; !got.CanRemove || got.LocalInfo.Version != 2 || filepath.Base(got.LocalDir) != "user-alpha" {
		t.Errorf("user alpha did not shadow newer built-in alpha: %+v", got)
	}
	if got := byID["beta"]; got.CanRemove || got.LocalInfo.Version != 3 || filepath.Base(got.LocalDir) != "beta-a" {
		t.Errorf("same-root duplicate selection is wrong: %+v", got)
	}
	if got := byID["Alpha"]; got.CanRemove {
		t.Errorf("case-distinct built-in module unexpectedly removable: %+v", got)
	}
	if got := byID["gamma"]; !got.CanRemove {
		t.Errorf("user-only module unexpectedly non-removable: %+v", got)
	}
	if len(issues) != 3 {
		t.Fatalf("OnIssue received %d issues, want 3: %v", len(issues), issues)
	}
}

func TestListMergesHighestMarketplaceVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept header = %q, want application/json", got)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"version":9,
			"modules":[
				{"id":"alpha","version":4,"name":"older remote","type":"plugin"},
				{"id":"alpha","version":6,"name":"latest remote","type":"plugin"},
				{"id":"beta","version":1,"name":"remote beta"},
				{"id":"delta","version":7,"name":"remote only"}
			]
		}`))
	}))
	t.Cleanup(server.Close)

	manager := newTestManager(t, server.URL)
	writeTestModule(t, manager.UserPluginsDir(), "alpha", ModuleInfo{ID: "alpha", Version: 2, Name: "local alpha"})
	writeTestModule(t, manager.BuiltInPluginsDir(), "beta", ModuleInfo{ID: "beta", Version: 3, Name: "local beta"})

	resources, err := manager.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	wantIDs := []string{"alpha", "beta", "delta"}
	if len(resources) != len(wantIDs) {
		t.Fatalf("List() returned %d resources, want %d: %+v", len(resources), len(wantIDs), resources)
	}
	for index, wantID := range wantIDs {
		if resources[index].ID() != wantID {
			t.Errorf("resources[%d].ID() = %q, want %q", index, resources[index].ID(), wantID)
		}
	}
	if got := resources[0]; got.RemoteInfo == nil || got.RemoteInfo.Version != 6 || got.EffectiveInfo().Name != "latest remote" || !got.NeedsUpdate() || !got.CanRemove {
		t.Errorf("merged alpha = %+v", got)
	}
	if got := resources[1]; got.RemoteInfo == nil || got.RemoteInfo.Version != 1 || got.NeedsUpdate() || got.CanRemove {
		t.Errorf("merged beta = %+v", got)
	}
	if got := resources[2]; got.LocalInfo != nil || got.RemoteInfo == nil || got.IsLocal() || got.CanRemove {
		t.Errorf("remote-only delta = %+v", got)
	}
}

func TestFetchMarketplaceErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/status":
			writer.WriteHeader(http.StatusBadGateway)
		case "/malformed":
			_, _ = writer.Write([]byte(`{"version":1,"modules":[`))
		case "/multiple":
			_, _ = writer.Write([]byte(`{"version":1,"modules":[]} {}`))
		case "/missing-id":
			_, _ = writer.Write([]byte(`{"version":1,"modules":[{"version":2}]}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)

	tests := []struct {
		name       string
		path       string
		wantTarget error
	}{
		{name: "non-success status", path: "/status"},
		{name: "malformed JSON", path: "/malformed"},
		{name: "multiple JSON values", path: "/multiple"},
		{name: "module without ID", path: "/missing-id", wantTarget: ErrInvalidModule},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := newTestManager(t, server.URL+test.path)
			modules, err := manager.FetchMarketplace(context.Background())
			if err == nil {
				t.Fatalf("FetchMarketplace() = %+v, nil error", modules)
			}
			if test.wantTarget != nil && !errors.Is(err, test.wantTarget) {
				t.Fatalf("FetchMarketplace() error = %v, want errors.Is(..., %v)", err, test.wantTarget)
			}
		})
	}
}

func TestFetchMarketplaceHonorsCallerContext(t *testing.T) {
	requestStarted := make(chan struct{})
	requestCanceled := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		close(requestStarted)
		<-request.Context().Done()
		requestCanceled <- struct{}{}
	}))
	t.Cleanup(server.Close)

	manager := newTestManager(t, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	errorResult := make(chan error, 1)
	go func() {
		_, err := manager.FetchMarketplace(ctx)
		errorResult <- err
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("marketplace request did not reach the server")
	}
	cancel()
	if err := <-errorResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("FetchMarketplace() error = %v, want context canceled", err)
	}
	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("server request context was not canceled")
	}
}

func TestListReturnsLocalResourcesWhenMarketplaceFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	manager := newTestManager(t, server.URL)
	writeTestModule(t, manager.BuiltInPluginsDir(), "local", ModuleInfo{ID: "local", Version: 1})

	resources, err := manager.List(context.Background())
	if err == nil {
		t.Fatal("List() error = nil, want marketplace failure")
	}
	if len(resources) != 1 || resources[0].ID() != "local" || resources[0].RemoteInfo != nil {
		t.Fatalf("List() resources = %+v, want local fallback", resources)
	}
}

func TestScanLocalRejectsNilContext(t *testing.T) {
	manager := newTestManager(t, "https://marketplace.invalid/marketplace.json")
	if _, err := manager.ScanLocal(nil); err == nil {
		t.Fatal("ScanLocal(nil) error = nil")
	}
}

func TestScanLocalIgnoresDirectorySymlinks(t *testing.T) {
	manager := newTestManager(t, "https://marketplace.invalid/marketplace.json")
	target := writeTestModule(t, t.TempDir(), "linked", ModuleInfo{ID: "linked", Version: 1})
	if err := os.MkdirAll(manager.BuiltInPluginsDir(), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	link := filepath.Join(manager.BuiltInPluginsDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("directory symlinks are not available: %v", err)
	}
	resources, err := manager.ScanLocal(context.Background())
	if err != nil {
		t.Fatalf("ScanLocal() error = %v", err)
	}
	if len(resources) != 0 {
		t.Fatalf("ScanLocal() followed a directory symlink: %+v", resources)
	}
}
