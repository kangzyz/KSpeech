package resource

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

type testArchiveEntry struct {
	name     string
	contents []byte
	mode     os.FileMode
	typeflag byte
	linkname string
}

func newTestManager(t *testing.T, marketplaceURL string, configure ...func(*Options)) *Manager {
	t.Helper()
	root := t.TempDir()
	options := Options{
		ExecutableDir:  filepath.Join(root, "application"),
		UserDataDir:    filepath.Join(root, "user-data"),
		MarketplaceURL: marketplaceURL,
		// httptest.Server is plaintext by default. Production remains HTTPS-only;
		// this shared test fixture opts in explicitly.
		AllowInsecureHTTP: true,
	}
	for _, apply := range configure {
		apply(&options)
	}
	manager, err := NewManager(options)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	return manager
}

func writeTestModule(t *testing.T, root, directory string, info ModuleInfo) string {
	t.Helper()
	moduleDir := filepath.Join(root, directory)
	if err := os.MkdirAll(moduleDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", moduleDir, err)
	}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("json.Marshal(%+v) error = %v", info, err)
	}
	if err := os.WriteFile(filepath.Join(moduleDir, ModuleJSONName), data, 0o644); err != nil {
		t.Fatalf("write manifest for %q: %v", info.ID, err)
	}
	return moduleDir
}

func writeTestFile(t *testing.T, path string, contents []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	return data
}

func makeTestArchive(t *testing.T, format string, entries []testArchiveEntry) []byte {
	t.Helper()
	switch format {
	case "zip":
		return makeTestZIP(t, entries)
	case "tar":
		return makeTestTAR(t, entries, false)
	case "tar.gz":
		return makeTestTAR(t, entries, true)
	default:
		t.Fatalf("unsupported test archive format %q", format)
		return nil
	}
}

func makeTestZIP(t *testing.T, entries []testArchiveEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		mode := entry.mode
		if mode == 0 {
			mode = 0o644
		}
		header.SetMode(mode)
		writer, err := archive.CreateHeader(header)
		if err != nil {
			t.Fatalf("CreateHeader(%q) error = %v", entry.name, err)
		}
		contents := entry.contents
		if mode&os.ModeSymlink != 0 && len(contents) == 0 {
			contents = []byte(entry.linkname)
		}
		if _, err := writer.Write(contents); err != nil {
			t.Fatalf("write zip entry %q: %v", entry.name, err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatalf("close zip archive: %v", err)
	}
	return buffer.Bytes()
}

func makeTestTAR(t *testing.T, entries []testArchiveEntry, compressed bool) []byte {
	t.Helper()
	var buffer bytes.Buffer
	var destination io.Writer = &buffer
	var compressor *gzip.Writer
	if compressed {
		compressor = gzip.NewWriter(&buffer)
		destination = compressor
	}
	archive := tar.NewWriter(destination)
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		mode := entry.mode.Perm()
		if mode == 0 {
			mode = 0o644
		}
		header := &tar.Header{
			Name:     entry.name,
			Mode:     int64(mode),
			Typeflag: typeflag,
			Linkname: entry.linkname,
		}
		if typeflag == tar.TypeReg || typeflag == tar.TypeRegA {
			header.Size = int64(len(entry.contents))
		}
		if err := archive.WriteHeader(header); err != nil {
			t.Fatalf("WriteHeader(%q) error = %v", entry.name, err)
		}
		if header.Size > 0 {
			if _, err := archive.Write(entry.contents); err != nil {
				t.Fatalf("write tar entry %q: %v", entry.name, err)
			}
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatalf("close tar archive: %v", err)
	}
	if compressor != nil {
		if err := compressor.Close(); err != nil {
			t.Fatalf("close gzip stream: %v", err)
		}
	}
	return buffer.Bytes()
}

func testSHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
