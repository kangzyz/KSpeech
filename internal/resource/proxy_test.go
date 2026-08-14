package resource

import (
	"net/http"
	"net/url"
	"testing"
)

func mustRequest(t *testing.T, rawURL string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

// Windows stores the proxy either as one address for everything or as a
// per-scheme list. HTTPS without its own entry rides the http one, because the
// tunnel is a plain CONNECT.
func TestParseProxyServers(t *testing.T) {
	t.Parallel()
	single := parseProxyServers("127.0.0.1:10808")
	if single[""] != "127.0.0.1:10808" || single["https"] != "" {
		t.Fatalf("single address = %#v", single)
	}

	perScheme := parseProxyServers("http=proxy:80;ftp=proxy:21")
	if perScheme["http"] != "proxy:80" || perScheme["https"] != "proxy:80" || perScheme["ftp"] != "proxy:21" {
		t.Fatalf("per-scheme = %#v", perScheme)
	}

	explicit := parseProxyServers("http=proxy:80;https=secure:443")
	if explicit["https"] != "secure:443" {
		t.Fatalf("explicit https entry was overwritten: %#v", explicit)
	}
}

func TestSystemProxyProxyFor(t *testing.T) {
	t.Parallel()
	config := systemProxyConfig{
		Enabled: true,
		Servers: map[string]string{"": "127.0.0.1:10808"},
		Bypass:  []string{"<local>", "*.corp.example", "skip.example.com"},
	}

	proxy, err := config.proxyFor(mustRequest(t, "https://raw.githubusercontent.com/x").URL)
	if err != nil {
		t.Fatal(err)
	}
	if proxy == nil || proxy.Host != "127.0.0.1:10808" || proxy.Scheme != "http" {
		t.Fatalf("proxy = %v, want the single address as an http URL", proxy)
	}

	for _, bypassed := range []string{
		"http://intranet/index.json",   // <local>: no dot
		"https://build.corp.example/x", // wildcard suffix
		"https://skip.example.com/x",   // exact host
		"http://localhost:8080/x",      // always direct
		"http://127.0.0.1:9000/x",      // always direct
	} {
		proxy, err := config.proxyFor(mustRequest(t, bypassed).URL)
		if err != nil {
			t.Fatal(err)
		}
		if proxy != nil {
			t.Fatalf("%s used proxy %v, want a direct connection", bypassed, proxy)
		}
	}

	// A disabled setting is ignored even though the address is still stored.
	disabled := config
	disabled.Enabled = false
	if proxy, err := disabled.proxyFor(mustRequest(t, "https://example.com").URL); err != nil || proxy != nil {
		t.Fatalf("disabled proxy = %v, %v, want none", proxy, err)
	}
}

// A per-scheme configuration must not send https traffic somewhere that was
// only declared for another scheme.
func TestSystemProxyPerSchemeSelection(t *testing.T) {
	t.Parallel()
	config := systemProxyConfig{Enabled: true, Servers: parseProxyServers("ftp=ftp-proxy:21")}
	if proxy, err := config.proxyFor(mustRequest(t, "https://example.com").URL); err != nil || proxy != nil {
		t.Fatalf("https proxy = %v, %v, want none", proxy, err)
	}
}

// A malformed system setting must degrade to a direct connection rather than
// failing every request the application makes.
func TestParseProxyAddressToleratesGarbage(t *testing.T) {
	t.Parallel()
	for _, address := range []string{"", "   ", "://", "http://"} {
		proxy, err := parseProxyAddress(address)
		if err != nil || proxy != nil {
			t.Fatalf("parseProxyAddress(%q) = %v, %v, want none", address, proxy, err)
		}
	}
	proxy, err := parseProxyAddress("socks5://127.0.0.1:1080")
	if err != nil || proxy == nil || proxy.Scheme != "socks5" {
		t.Fatalf("explicit scheme was not preserved: %v, %v", proxy, err)
	}
}

// The environment keeps priority. Go caches it inside ProxyFromEnvironment on
// first use, so both sources are injected here instead of being set through
// the process environment, which no test could then change back.
func TestResolveProxyPrefersTheEnvironment(t *testing.T) {
	for _, name := range proxyEnvironmentNames {
		t.Setenv(name, "")
	}
	systemProxy := func() systemProxyConfig {
		return systemProxyConfig{Enabled: true, Servers: map[string]string{"": "127.0.0.1:10808"}}
	}
	environment := func(address string) func(*http.Request) (*url.URL, error) {
		return func(*http.Request) (*url.URL, error) { return parseProxyAddress(address) }
	}

	// An exported proxy is used as-is.
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:3128")
	proxy, err := resolveProxy(mustRequest(t, "https://example.com"), environment("127.0.0.1:3128"), systemProxy)
	if err != nil {
		t.Fatal(err)
	}
	if proxy == nil || proxy.Host != "127.0.0.1:3128" {
		t.Fatalf("proxy = %v, want the environment's", proxy)
	}

	// NO_PROXY excluded this host: the system setting must not reinstate one.
	proxy, err = resolveProxy(mustRequest(t, "https://example.com"), environment(""), systemProxy)
	if err != nil {
		t.Fatal(err)
	}
	if proxy != nil {
		t.Fatalf("proxy = %v, want the environment's exclusion to hold", proxy)
	}

	// Nothing in the environment at all: this is the Explorer-launched case
	// the fallback exists for.
	for _, name := range proxyEnvironmentNames {
		t.Setenv(name, "")
	}
	proxy, err = resolveProxy(mustRequest(t, "https://example.com"), environment(""), systemProxy)
	if err != nil {
		t.Fatal(err)
	}
	if proxy == nil || proxy.Host != "127.0.0.1:10808" {
		t.Fatalf("proxy = %v, want the system setting", proxy)
	}
}

func TestProxyEnvironmentConfigured(t *testing.T) {
	for _, name := range proxyEnvironmentNames {
		t.Setenv(name, "")
	}
	if proxyEnvironmentConfigured() {
		t.Fatal("an empty environment was reported as configured")
	}
	t.Setenv("no_proxy", "example.com")
	if !proxyEnvironmentConfigured() {
		t.Fatal("NO_PROXY alone must count as a configured environment")
	}
}

// The manager's default client has to carry the resolver; without it nothing
// ever reads the system proxy. A caller-supplied client stays untouched.
func TestDefaultClientUsesTheSystemAwareProxy(t *testing.T) {
	manager, err := NewManager(Options{ExecutableDir: t.TempDir(), UserDataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := manager.client.Transport.(*http.Transport)
	if !ok || transport.Proxy == nil {
		t.Fatalf("default transport = %#v, want one with a proxy resolver", manager.client.Transport)
	}

	supplied := &http.Client{}
	manager, err = NewManager(Options{
		ExecutableDir: t.TempDir(), UserDataDir: t.TempDir(), HTTPClient: supplied,
	})
	if err != nil {
		t.Fatal(err)
	}
	if manager.client != supplied || manager.client.Transport != nil {
		t.Fatal("a caller-supplied client was replaced or rewritten")
	}
}
