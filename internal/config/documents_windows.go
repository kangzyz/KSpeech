//go:build windows

package config

import "golang.org/x/sys/windows"

func documentsDirectory() string {
	documents, err := windows.KnownFolderPath(windows.FOLDERID_Documents, windows.KF_FLAG_DEFAULT)
	if err == nil && documents != "" {
		return documents
	}
	return fallbackDocumentsDirectory()
}
