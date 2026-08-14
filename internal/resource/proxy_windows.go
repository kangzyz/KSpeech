//go:build windows

package resource

import (
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// internetSettingsKey holds the per-user proxy Windows applies to programs
// that read the system settings. It is the same key the Settings app and the
// browsers write and read.
const internetSettingsKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

func lookupEnv(name string) string { return os.Getenv(name) }

func readSystemProxy() systemProxyConfig {
	key, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKey, registry.QUERY_VALUE)
	if err != nil {
		return systemProxyConfig{}
	}
	defer key.Close()

	enabled, _, err := key.GetIntegerValue("ProxyEnable")
	if err != nil || enabled == 0 {
		return systemProxyConfig{}
	}
	server, _, err := key.GetStringValue("ProxyServer")
	if err != nil || strings.TrimSpace(server) == "" {
		return systemProxyConfig{}
	}
	config := systemProxyConfig{Enabled: true, Servers: parseProxyServers(server)}
	if override, _, err := key.GetStringValue("ProxyOverride"); err == nil {
		config.Bypass = strings.Split(override, ";")
	}
	return config
}
