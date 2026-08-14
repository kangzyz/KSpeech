//go:build !windows

package resource

import "os"

func lookupEnv(name string) string { return os.Getenv(name) }

// readSystemProxy has no equivalent outside Windows: the environment variables
// already are the system-wide convention there, and they are handled before
// this is consulted.
func readSystemProxy() systemProxyConfig { return systemProxyConfig{} }
