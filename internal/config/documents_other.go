//go:build !windows

package config

func documentsDirectory() string {
	return fallbackDocumentsDirectory()
}
