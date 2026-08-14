package resource

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// ProxyFunc selects the proxy for one request, matching the signature of
// http.Transport.Proxy.
type ProxyFunc func(*http.Request) (*url.URL, error)

// systemProxyConfig is the proxy Windows applies to programs that read the
// system settings, which is where browsers get theirs.
type systemProxyConfig struct {
	// Enabled reports whether the user turned the manual proxy on. A disabled
	// configuration is ignored even when an address is still stored.
	Enabled bool
	// Servers maps a scheme to its proxy address. The empty key is the proxy
	// used for every scheme, which is what the single-address form produces.
	Servers map[string]string
	// Bypass lists hosts that must not be proxied, in Windows' own syntax
	// including the "<local>" token.
	Bypass []string
}

// ProxyFromEnvironmentOrSystem resolves a proxy the way a Windows desktop
// application is expected to: the standard environment variables win, and a
// process that has none — every application started from Explorer — falls back
// to the proxy configured in Windows itself. Go's own ProxyFromEnvironment
// only reads the environment, so without this a user running a proxy client
// would still see KSpeech connect directly and fail.
//
// Both sources are read once per process. Go caches the environment inside
// ProxyFromEnvironment, and the system setting is cached here for the same
// reason: it is consulted on every request and is not changed mid-session in
// practice. Non-Windows builds have no such setting and keep the environment
// behaviour.
func ProxyFromEnvironmentOrSystem(request *http.Request) (*url.URL, error) {
	return resolveProxy(request, http.ProxyFromEnvironment, cachedSystemProxy)
}

// resolveProxy holds the decision itself, with both sources supplied by the
// caller so it can be exercised without a process-wide environment.
func resolveProxy(
	request *http.Request,
	environmentProxy func(*http.Request) (*url.URL, error),
	system func() systemProxyConfig,
) (*url.URL, error) {
	proxy, err := environmentProxy(request)
	if err != nil || proxy != nil {
		return proxy, err
	}
	if proxyEnvironmentConfigured() {
		// The environment is configured and deliberately selected no proxy for
		// this host, e.g. through NO_PROXY. Respect that instead of overriding
		// it with the system setting.
		return nil, nil
	}
	return system().proxyFor(request.URL)
}

var (
	systemProxyOnce  sync.Once
	systemProxyValue systemProxyConfig
)

func cachedSystemProxy() systemProxyConfig {
	systemProxyOnce.Do(func() { systemProxyValue = readSystemProxy() })
	return systemProxyValue
}

var proxyEnvironmentNames = []string{
	"HTTP_PROXY", "http_proxy",
	"HTTPS_PROXY", "https_proxy",
	"ALL_PROXY", "all_proxy",
	"NO_PROXY", "no_proxy",
}

func proxyEnvironmentConfigured() bool {
	for _, name := range proxyEnvironmentNames {
		if strings.TrimSpace(lookupEnv(name)) != "" {
			return true
		}
	}
	return false
}

// proxyFor returns the proxy for one target URL, or nil to connect directly.
func (c systemProxyConfig) proxyFor(target *url.URL) (*url.URL, error) {
	if !c.Enabled || target == nil || len(c.Servers) == 0 {
		return nil, nil
	}
	scheme := strings.ToLower(target.Scheme)
	address, ok := c.Servers[scheme]
	if !ok {
		address, ok = c.Servers[""]
	}
	if !ok || strings.TrimSpace(address) == "" {
		return nil, nil
	}
	if c.bypasses(target) {
		return nil, nil
	}
	return parseProxyAddress(address)
}

func (c systemProxyConfig) bypasses(target *url.URL) bool {
	host := strings.ToLower(target.Hostname())
	if host == "" {
		return false
	}
	for _, entry := range c.Bypass {
		entry = strings.ToLower(strings.TrimSpace(entry))
		switch {
		case entry == "":
			continue
		case entry == "<local>":
			// Windows treats any name without a dot as local, which covers
			// localhost and intranet short names.
			if !strings.Contains(host, ".") || host == "localhost" {
				return true
			}
		case entry == host:
			return true
		case strings.HasPrefix(entry, "*"):
			if strings.HasSuffix(host, strings.TrimPrefix(entry, "*")) {
				return true
			}
		}
	}
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// parseProxyAddress accepts the address forms Windows stores: a bare
// host:port, and a full URL when the user typed one.
func parseProxyAddress(address string) (*url.URL, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return nil, nil
	}
	if !strings.Contains(address, "://") {
		address = "http://" + address
	}
	parsed, err := url.Parse(address)
	if err != nil || parsed.Host == "" {
		// A malformed system setting must not fail every request: connecting
		// directly is what the application did before it read the setting.
		return nil, nil
	}
	return parsed, nil
}

// parseProxyServers reads Windows' ProxyServer value, which is either one
// address for every scheme or a per-scheme list such as
// "http=host:80;https=host:443".
func parseProxyServers(value string) map[string]string {
	servers := make(map[string]string)
	for _, entry := range strings.Split(value, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		scheme, address, found := strings.Cut(entry, "=")
		if !found {
			servers[""] = entry
			continue
		}
		scheme = strings.ToLower(strings.TrimSpace(scheme))
		address = strings.TrimSpace(address)
		if scheme != "" && address != "" {
			servers[scheme] = address
		}
	}
	// Windows sends HTTPS traffic through the "http" entry when no dedicated
	// https proxy exists, because the tunnel itself is a plain CONNECT.
	if _, ok := servers["https"]; !ok {
		if http, ok := servers["http"]; ok {
			servers["https"] = http
		}
	}
	return servers
}
