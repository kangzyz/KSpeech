package resource

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func builtinExtractors() map[string]Extractor {
	return map[string]Extractor{
		"zip":     extractZIP,
		"tar":     extractTAR,
		"tar.gz":  extractTarGzip,
		"tar.bz2": extractTarBzip2,
	}
}

func normalizeArchiveType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, ".")
	switch value {
	case "application/zip", "zip":
		return "zip"
	case "application/x-tar", "tar":
		return "tar"
	case "application/gzip", "application/x-gzip", "gzip", "gz", "tgz", "tar.gz":
		return "tar.gz"
	case "application/x-bzip2", "bzip2", "bz2", "tbz", "tbz2", "tar.bz2":
		return "tar.bz2"
	default:
		return value
	}
}

func detectArchiveType(explicit, sourceURL, archivePath string) (string, error) {
	if normalized := normalizeArchiveType(explicit); normalized != "" {
		return normalized, nil
	}
	name := sourceURL
	if parsed, err := url.Parse(sourceURL); err == nil {
		name = parsed.Path
	}
	name = strings.ToLower(name)
	switch {
	case strings.HasSuffix(name, ".tar.bz2"), strings.HasSuffix(name, ".tbz2"), strings.HasSuffix(name, ".tbz"):
		return "tar.bz2", nil
	case strings.HasSuffix(name, ".tar.gz"), strings.HasSuffix(name, ".tgz"):
		return "tar.gz", nil
	case strings.HasSuffix(name, ".tar"):
		return "tar", nil
	case strings.HasSuffix(name, ".zip"):
		return "zip", nil
	}

	file, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	header := make([]byte, 512)
	read, _ := io.ReadFull(file, header)
	header = header[:read]
	if len(header) >= 4 && string(header[:2]) == "PK" && (string(header[2:4]) == "\x03\x04" || string(header[2:4]) == "\x05\x06" || string(header[2:4]) == "\x07\x08") {
		return "zip", nil
	}
	if len(header) >= 3 && header[0] == 0x1f && header[1] == 0x8b && header[2] == 0x08 {
		return "tar.gz", nil
	}
	if len(header) >= 3 && string(header[:3]) == "BZh" {
		return "tar.bz2", nil
	}
	if len(header) >= 262 && string(header[257:262]) == "ustar" {
		return "tar", nil
	}
	return "", fmt.Errorf("%w: cannot determine type for %q", ErrUnsupportedArchive, sourceURL)
}

func extractZIP(ctx context.Context, archivePath, destination string) error {
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open zip archive: %w", err)
	}
	defer archive.Close()

	for _, entry := range archive.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		mode := entry.Mode()
		if mode&os.ModeSymlink != 0 || (!mode.IsRegular() && !mode.IsDir()) {
			return fmt.Errorf("%w: zip entry %q is a symlink or special file", ErrUnsafePath, entry.Name)
		}
		path, err := safeJoin(destination, entry.Name, true)
		if err != nil {
			return fmt.Errorf("zip entry %q: %w", entry.Name, err)
		}
		if entry.FileInfo().IsDir() || strings.HasSuffix(strings.ReplaceAll(entry.Name, `\`, "/"), "/") {
			if err := mkdirAllSafe(destination, path, 0o755); err != nil {
				return err
			}
			continue
		}
		if path == destination {
			return fmt.Errorf("%w: zip entry %q has no file name", ErrUnsafePath, entry.Name)
		}
		if err := budgetFromContext(ctx).canConsume(entry.UncompressedSize64); err != nil {
			return fmt.Errorf("zip entry %q: %w", entry.Name, err)
		}
		if err := mkdirAllSafe(destination, filepath.Dir(path), 0o755); err != nil {
			return err
		}
		reader, err := entry.Open()
		if err != nil {
			return fmt.Errorf("open zip entry %q: %w", entry.Name, err)
		}
		permission := mode.Perm()
		if permission == 0 {
			permission = 0o644
		}
		err = writeArchiveFile(ctx, destination, path, reader, permission)
		closeErr := reader.Close()
		if err != nil {
			return fmt.Errorf("extract zip entry %q: %w", entry.Name, err)
		}
		if closeErr != nil {
			return fmt.Errorf("close zip entry %q: %w", entry.Name, closeErr)
		}
	}
	return nil
}

func extractTAR(ctx context.Context, archivePath, destination string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open tar archive: %w", err)
	}
	defer file.Close()
	return extractTarReader(ctx, tar.NewReader(file), destination)
}

func extractTarGzip(ctx context.Context, archivePath, destination string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open tar.gz archive: %w", err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("open gzip stream: %w", err)
	}
	defer gzipReader.Close()
	return extractTarReader(ctx, tar.NewReader(gzipReader), destination)
}

func extractTarBzip2(ctx context.Context, archivePath, destination string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open tar.bz2 archive: %w", err)
	}
	defer file.Close()
	return extractTarReader(ctx, tar.NewReader(bzip2.NewReader(file)), destination)
}

func extractTarReader(ctx context.Context, archive *tar.Reader, destination string) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}
		path, err := safeJoin(destination, header.Name, true)
		if err != nil {
			return fmt.Errorf("tar entry %q: %w", header.Name, err)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := mkdirAllSafe(destination, path, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if path == destination {
				return fmt.Errorf("%w: tar entry %q has no file name", ErrUnsafePath, header.Name)
			}
			if header.Size < 0 {
				return fmt.Errorf("invalid tar entry %q size", header.Name)
			}
			if err := budgetFromContext(ctx).canConsume(uint64(header.Size)); err != nil {
				return fmt.Errorf("tar entry %q: %w", header.Name, err)
			}
			if err := mkdirAllSafe(destination, filepath.Dir(path), 0o755); err != nil {
				return err
			}
			permission := os.FileMode(header.Mode).Perm()
			if permission == 0 {
				permission = 0o644
			}
			if err := writeArchiveFile(ctx, destination, path, io.LimitReader(archive, header.Size), permission); err != nil {
				return fmt.Errorf("extract tar entry %q: %w", header.Name, err)
			}
		case tar.TypeXHeader, tar.TypeXGlobalHeader:
			// archive/tar consumes the extended header itself.
		default:
			return fmt.Errorf("%w: tar entry %q is a link or special file", ErrUnsafePath, header.Name)
		}
	}
}

func writeArchiveFile(ctx context.Context, root, path string, source io.Reader, mode os.FileMode) error {
	reader := &contextReader{ctx: ctx, reader: source}
	return writeRegularFileSafeLimited(root, path, reader, mode, false, budgetFromContext(ctx))
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}
