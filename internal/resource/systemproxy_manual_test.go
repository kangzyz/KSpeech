package resource

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestSystemProxyAgainstTheRealMarketplace is a manual check that the running
// machine's own proxy configuration reaches the real index. It is skipped
// unless KSPEECH_PROXY_CHECK is set, because it needs the network.
func TestSystemProxyAgainstTheRealMarketplace(t *testing.T) {
	if os.Getenv("KSPEECH_PROXY_CHECK") == "" {
		t.Skip("set KSPEECH_PROXY_CHECK=1 to run the live proxy check")
	}
	request, err := http.NewRequest(http.MethodGet, DefaultMarketURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := ProxyFromEnvironmentOrSystem(request)
	t.Logf("HTTPS_PROXY=%q -> resolved proxy %v (err=%v)", os.Getenv("HTTPS_PROXY"), proxy, err)

	manager, err := NewManager(Options{ExecutableDir: t.TempDir(), UserDataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	modules, err := manager.FetchMarketplace(context.Background())
	if err != nil {
		t.Fatalf("fetch failed after %s: %v", time.Since(start).Round(time.Millisecond), err)
	}
	t.Logf("fetched %d modules in %s", len(modules), time.Since(start).Round(time.Millisecond))
}
