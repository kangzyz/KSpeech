package resource

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
)

func validateModuleID(id string) error {
	if id == "" || id == "." || id == ".." || id != filepath.Base(id) || filepath.IsAbs(id) || filepath.VolumeName(id) != "" {
		return fmt.Errorf("%w: invalid module id %q", ErrInvalidModule, id)
	}
	if strings.ContainsAny(id, `/\\`) || strings.IndexByte(id, 0) >= 0 {
		return fmt.Errorf("%w: invalid module id %q", ErrInvalidModule, id)
	}
	if err := validatePortableComponent(id); err != nil {
		return fmt.Errorf("%w: invalid module id %q: %v", ErrInvalidModule, id, err)
	}
	return nil
}

func cleanRelativePath(value string, allowEmpty bool) (string, error) {
	if strings.IndexByte(value, 0) >= 0 {
		return "", fmt.Errorf("%w: path contains NUL", ErrUnsafePath)
	}
	// Archive and marketplace paths are slash-oriented. Normalizing both
	// separators before filepath operations is essential on Unix as well, so a
	// Windows traversal payload cannot become dangerous after installation.
	value = strings.ReplaceAll(value, `\`, "/")
	if value == "" || value == "." {
		if allowEmpty {
			return "", nil
		}
		return "", fmt.Errorf("%w: path is empty", ErrUnsafePath)
	}
	if strings.HasPrefix(value, "/") || filepath.IsAbs(filepath.FromSlash(value)) || filepath.VolumeName(filepath.FromSlash(value)) != "" {
		return "", fmt.Errorf("%w: absolute path %q", ErrUnsafePath, value)
	}
	segments := strings.Split(value, "/")
	for _, segment := range segments {
		if segment == ".." {
			return "", fmt.Errorf("%w: parent traversal in %q", ErrUnsafePath, value)
		}
		if segment != "" && segment != "." {
			if err := validatePortableComponent(segment); err != nil {
				return "", fmt.Errorf("%w: invalid component %q: %v", ErrUnsafePath, segment, err)
			}
		}
	}
	cleaned := filepath.Clean(filepath.FromSlash(value))
	if cleaned == "." {
		if allowEmpty {
			return "", nil
		}
		return "", fmt.Errorf("%w: path is empty", ErrUnsafePath)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: parent traversal in %q", ErrUnsafePath, value)
	}
	return cleaned, nil
}

// validatePortableComponent applies Windows' stricter file-name rules even
// when tests or build tooling run on another OS. KSpeech is primarily a
// Windows application, and rejecting an alternate-data-stream or device name
// at manifest-validation time is safer than accepting a platform-dependent
// path that could later be installed on Windows.
func validatePortableComponent(value string) error {
	if value == "" || value == "." || value == ".." {
		return errors.New("empty or dot component")
	}
	if strings.ContainsAny(value, `<>:"/\|?*`) || strings.IndexByte(value, 0) >= 0 {
		return errors.New("component contains a reserved character")
	}
	for _, character := range value {
		if character < 32 {
			return errors.New("component contains a control character")
		}
	}
	if strings.HasSuffix(value, ".") || strings.HasSuffix(value, " ") {
		return errors.New("component ends with a dot or space")
	}
	base := value
	if index := strings.IndexByte(base, '.'); index >= 0 {
		base = base[:index]
	}
	switch strings.ToUpper(base) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return errors.New("component is a reserved device name")
	}
	return nil
}

func safeJoin(root, relative string, allowEmpty bool) (string, error) {
	cleaned, err := cleanRelativePath(relative, allowEmpty)
	if err != nil {
		return "", err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", err
	}
	destination := root
	if cleaned != "" {
		destination = filepath.Join(root, cleaned)
	}
	rel, err := filepath.Rel(root, destination)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: %q escapes module root", ErrUnsafePath, relative)
	}
	return destination, nil
}

func mkdirAllSafe(root, destination string, mode os.FileMode) error {
	if err := ensureNoSymlinkComponents(root, destination, true); err != nil {
		return err
	}
	if err := os.MkdirAll(destination, mode); err != nil {
		return err
	}
	return ensureNoSymlinkComponents(root, destination, false)
}

func ensureNoSymlinkComponents(root, destination string, missingOK bool) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, destination)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("%w: destination escapes root", ErrUnsafePath)
	}
	current := root
	components := []string{}
	if rel != "." {
		components = strings.Split(rel, string(filepath.Separator))
	}
	for _, component := range components {
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) && missingOK {
			continue
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symlink component %q", ErrUnsafePath, current)
		}
		if current != destination && !info.IsDir() {
			return fmt.Errorf("%w: non-directory path component %q", ErrUnsafePath, current)
		}
	}
	return nil
}

// ensurePathChainNoSymlinks validates every existing component from ancestor
// through destination, including the root itself. This is used before resource
// mutations because checking only children would still follow a user-created
// plugins symlink/junction into an unrelated directory.
func ensurePathChainNoSymlinks(ancestor, destination string, missingOK bool) error {
	ancestor, err := filepath.Abs(ancestor)
	if err != nil {
		return err
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(ancestor, destination)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return fmt.Errorf("%w: resource root escapes its data directory", ErrUnsafePath)
	}
	paths := []string{ancestor}
	current := ancestor
	if relative != "." {
		for _, component := range strings.Split(relative, string(filepath.Separator)) {
			current = filepath.Join(current, component)
			paths = append(paths, current)
		}
	}
	for _, path := range paths {
		info, statErr := os.Lstat(path)
		if errors.Is(statErr, os.ErrNotExist) && missingOK {
			continue
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: resource path component %q is a symlink or reparse point", ErrUnsafePath, path)
		}
		if !info.IsDir() {
			return fmt.Errorf("%w: resource path component %q is not a directory", ErrUnsafePath, path)
		}
	}
	return nil
}

func writeRegularFileSafe(root, destination string, source io.Reader, mode os.FileMode, overwrite bool) error {
	return writeRegularFileSafeLimited(root, destination, source, mode, overwrite, nil)
}

func writeRegularFileSafeLimited(root, destination string, source io.Reader, mode os.FileMode, overwrite bool, budget *installBudget) error {
	if err := ensureNoSymlinkComponents(root, filepath.Dir(destination), false); err != nil {
		return err
	}
	if info, err := os.Lstat(destination); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("%w: destination %q is not a regular file", ErrUnsafePath, destination)
		}
		if !overwrite {
			return fmt.Errorf("destination already exists: %q", destination)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if !overwrite {
		flags = os.O_CREATE | os.O_WRONLY | os.O_EXCL
	}
	file, err := os.OpenFile(destination, flags, mode)
	if err != nil {
		return err
	}
	var writer io.Writer = file
	if budget != nil {
		writer = &budgetWriter{destination: file, budget: budget}
	}
	_, copyErr := io.Copy(writer, source)
	if copyErr == nil {
		copyErr = file.Sync()
	}
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func safeExistingChild(root, child string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	child, err = filepath.Abs(child)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, child)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: resource directory escapes user root", ErrUnsafePath)
	}
	if strings.Contains(rel, string(filepath.Separator)) {
		return "", fmt.Errorf("%w: resource is not an immediate child", ErrUnsafePath)
	}
	info, err := os.Lstat(child)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("%w: resource directory is not a regular directory", ErrUnsafePath)
	}
	return child, nil
}

func validateExtractedTree(root string) error {
	_, err := validateExtractedTreeSize(root, math.MaxInt64)
	return err
}

func validateExtractedTreeSize(root string, maxBytes int64) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: extracted symlink %q", ErrUnsafePath, path)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()
		if mode&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: extracted symlink %q", ErrUnsafePath, path)
		}
		if info.IsDir() {
			return nil
		}
		if !mode.IsRegular() {
			return fmt.Errorf("%w: extracted special file %q", ErrUnsafePath, path)
		}
		size := info.Size()
		if size < 0 || size > maxBytes-total {
			return fmt.Errorf("%w: staged tree exceeds %d bytes", ErrInstallSizeLimit, maxBytes)
		}
		total += size
		return nil
	})
	return total, err
}
