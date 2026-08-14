package resource

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	managedArtifactOwner   = "kspeech-resource-manager"
	managedArtifactVersion = 1
	managedArtifactMarker  = ".kspeech-transaction.json"
	managedInstallPrefix   = ".kspeech-install-v1-"
	managedBackupPrefix    = ".kspeech-backup-v1-"
	managedTokenBytes      = 16
	maxArtifactMarkerBytes = 4 << 10

	// A live installation can legitimately take a long time for multi-gigabyte
	// models. Cleanup therefore only touches fully identifiable transactions
	// that have been abandoned for at least seven days.
	staleManagedArtifactAge = 7 * 24 * time.Hour
)

type managedArtifactKind string

const (
	managedArtifactInstall managedArtifactKind = "install"
	managedArtifactBackup  managedArtifactKind = "backup"
)

type managedArtifactMetadata struct {
	Owner       string              `json:"owner"`
	Version     int                 `json:"version"`
	Kind        managedArtifactKind `json:"kind"`
	Token       string              `json:"token"`
	CreatedUnix int64               `json:"createdUnix"`
	ModuleID    string              `json:"moduleId"`
	TargetName  string              `json:"targetName,omitempty"`
}

func managedArtifactPrefix(kind managedArtifactKind) (string, error) {
	switch kind {
	case managedArtifactInstall:
		return managedInstallPrefix, nil
	case managedArtifactBackup:
		return managedBackupPrefix, nil
	default:
		return "", fmt.Errorf("unknown managed resource transaction kind %q", kind)
	}
}

func createManagedArtifact(root string, kind managedArtifactKind, moduleID, targetName string, createdAt time.Time) (string, error) {
	if err := validateModuleID(moduleID); err != nil {
		return "", err
	}
	if kind == managedArtifactBackup {
		if err := validatePortableComponent(targetName); err != nil {
			return "", fmt.Errorf("invalid backup target name %q: %w", targetName, err)
		}
	} else if targetName != "" {
		return "", fmt.Errorf("install transaction must not declare a target name")
	}
	prefix, err := managedArtifactPrefix(kind)
	if err != nil {
		return "", err
	}
	if createdAt.IsZero() {
		createdAt = time.Now()
	}

	for attempt := 0; attempt < 16; attempt++ {
		tokenBytes := make([]byte, managedTokenBytes)
		if _, err := rand.Read(tokenBytes); err != nil {
			return "", fmt.Errorf("create resource transaction token: %w", err)
		}
		token := hex.EncodeToString(tokenBytes)
		directory, err := safeJoin(root, prefix+token, false)
		if err != nil {
			return "", err
		}
		if err := os.Mkdir(directory, 0o700); errors.Is(err, os.ErrExist) {
			continue
		} else if err != nil {
			return "", err
		}

		metadata := managedArtifactMetadata{
			Owner:       managedArtifactOwner,
			Version:     managedArtifactVersion,
			Kind:        kind,
			Token:       token,
			CreatedUnix: createdAt.UTC().Unix(),
			ModuleID:    moduleID,
			TargetName:  targetName,
		}
		if err := writeManagedArtifactMetadata(directory, metadata); err != nil {
			_ = os.Remove(directory)
			return "", err
		}
		return directory, nil
	}
	return "", errors.New("create resource transaction directory: token collisions exhausted")
}

func writeManagedArtifactMetadata(directory string, metadata managedArtifactMetadata) error {
	data, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode resource transaction marker: %w", err)
	}
	data = append(data, '\n')
	path := filepath.Join(directory, managedArtifactMarker)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create resource transaction marker: %w", err)
	}
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		_ = os.Remove(path)
		return fmt.Errorf("write resource transaction marker: %w", writeErr)
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return fmt.Errorf("close resource transaction marker: %w", closeErr)
	}
	return nil
}

func parseManagedArtifactName(name string) (managedArtifactKind, string, bool) {
	for _, candidate := range []struct {
		kind   managedArtifactKind
		prefix string
	}{
		{managedArtifactInstall, managedInstallPrefix},
		{managedArtifactBackup, managedBackupPrefix},
	} {
		if !strings.HasPrefix(name, candidate.prefix) {
			continue
		}
		token := strings.TrimPrefix(name, candidate.prefix)
		decoded, err := hex.DecodeString(token)
		if err != nil || len(decoded) != managedTokenBytes {
			return "", "", false
		}
		return candidate.kind, token, true
	}
	return "", "", false
}

func readManagedArtifactMetadata(root, directory string) (managedArtifactMetadata, error) {
	var metadata managedArtifactMetadata
	directory, err := safeExistingChild(root, directory)
	if err != nil {
		return metadata, err
	}
	kind, token, ok := parseManagedArtifactName(filepath.Base(directory))
	if !ok {
		return metadata, errors.New("directory name is not a managed resource transaction")
	}
	markerPath := filepath.Join(directory, managedArtifactMarker)
	info, err := os.Lstat(markerPath)
	if err != nil {
		return metadata, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return metadata, fmt.Errorf("%w: resource transaction marker is not a regular file", ErrUnsafePath)
	}
	if info.Size() < 0 || info.Size() > maxArtifactMarkerBytes {
		return metadata, fmt.Errorf("resource transaction marker exceeds %d bytes", maxArtifactMarkerBytes)
	}
	file, err := os.Open(markerPath)
	if err != nil {
		return metadata, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxArtifactMarkerBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return managedArtifactMetadata{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return managedArtifactMetadata{}, errors.New("resource transaction marker contains multiple JSON values")
		}
		return managedArtifactMetadata{}, err
	}
	if metadata.Owner != managedArtifactOwner || metadata.Version != managedArtifactVersion || metadata.Kind != kind || metadata.Token != token {
		return managedArtifactMetadata{}, errors.New("resource transaction marker does not match its directory")
	}
	if metadata.CreatedUnix <= 0 {
		return managedArtifactMetadata{}, errors.New("resource transaction marker has an invalid creation time")
	}
	if err := validateModuleID(metadata.ModuleID); err != nil {
		return managedArtifactMetadata{}, err
	}
	if kind == managedArtifactBackup {
		if err := validatePortableComponent(metadata.TargetName); err != nil {
			return managedArtifactMetadata{}, fmt.Errorf("invalid backup target name %q: %w", metadata.TargetName, err)
		}
	} else if metadata.TargetName != "" {
		return managedArtifactMetadata{}, errors.New("install transaction declares an unexpected target name")
	}
	return metadata, nil
}

func (m *Manager) recoverManagedArtifactsLocked(ctx context.Context) ([]error, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ensurePathChainNoSymlinks(m.userDataDir, m.userRoot, true); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(m.userRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	now := time.Now()
	var issues []error
	backups := make(map[string][]managedBackupCandidate)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return issues, err
		}
		if _, _, ok := parseManagedArtifactName(entry.Name()); !ok {
			continue
		}
		directory := filepath.Join(m.userRoot, entry.Name())
		if m.managedArtifactActive(directory) {
			continue
		}
		metadata, err := readManagedArtifactMetadata(m.userRoot, directory)
		if err != nil {
			issues = append(issues, fmt.Errorf("ignore unrecognized resource transaction %q: %w", directory, err))
			continue
		}
		switch metadata.Kind {
		case managedArtifactInstall:
			if now.Sub(time.Unix(metadata.CreatedUnix, 0)) < staleManagedArtifactAge {
				continue
			}
			if err := removeManagedInstallArtifact(m.userRoot, directory, metadata); err != nil {
				issues = append(issues, fmt.Errorf("clean stale resource staging %q: %w", directory, err))
			}
		case managedArtifactBackup:
			backups[metadata.TargetName] = append(backups[metadata.TargetName], managedBackupCandidate{
				directory: directory,
				metadata:  metadata,
			})
		}
	}
	for targetName, candidates := range backups {
		target, targetErr := safeJoin(m.userRoot, targetName, false)
		if targetErr != nil {
			issues = append(issues, fmt.Errorf("resolve resource backup target %q: %w", targetName, targetErr))
			continue
		}
		_, targetErr = os.Lstat(target)
		if errors.Is(targetErr, os.ErrNotExist) && len(candidates) != 1 {
			issues = append(issues, fmt.Errorf("refuse ambiguous recovery of missing resource target %q from %d backups", target, len(candidates)))
			continue
		}
		for _, candidate := range candidates {
			for _, issue := range recoverManagedBackup(m.userRoot, candidate.directory, candidate.metadata, now, m.maxInstallBytes) {
				issues = append(issues, issue)
			}
		}
	}
	return issues, nil
}

type managedBackupCandidate struct {
	directory string
	metadata  managedArtifactMetadata
}

func recoverManagedBackup(root, directory string, metadata managedArtifactMetadata, now time.Time, maxInstallBytes int64) []error {
	previous := filepath.Join(directory, "previous")
	target, err := safeJoin(root, metadata.TargetName, false)
	if err != nil {
		return []error{fmt.Errorf("recover resource backup %q: %w", directory, err)}
	}
	targetInfo, targetErr := os.Lstat(target)
	if targetErr != nil && !errors.Is(targetErr, os.ErrNotExist) {
		return []error{fmt.Errorf("inspect resource backup target %q: %w", target, targetErr)}
	}
	previousInfo, previousErr := os.Lstat(previous)
	if previousErr != nil && !errors.Is(previousErr, os.ErrNotExist) {
		return []error{fmt.Errorf("inspect previous resource %q: %w", previous, previousErr)}
	}

	var previousManifest *ModuleInfo
	if previousErr == nil {
		if previousInfo.Mode()&os.ModeSymlink != 0 || !previousInfo.IsDir() {
			return []error{fmt.Errorf("%w: previous resource %q is not a regular directory", ErrUnsafePath, previous)}
		}
		previousManifest, err = readModuleInfo(filepath.Join(previous, ModuleJSONName))
		if err != nil {
			return []error{fmt.Errorf("validate previous resource %q: %w", previous, err)}
		}
		if err := validateModuleID(previousManifest.ID); err != nil || previousManifest.ID != metadata.ModuleID {
			if err == nil {
				err = fmt.Errorf("manifest id %q does not match transaction module %q", previousManifest.ID, metadata.ModuleID)
			}
			return []error{fmt.Errorf("validate previous resource %q: %w", previous, err)}
		}
		if err := validateModuleRuntimePaths(*previousManifest); err != nil {
			return []error{fmt.Errorf("validate previous resource %q runtime paths: %w", previous, err)}
		}
		if err := validateInstalledRuntimeFiles(previous, *previousManifest); err != nil {
			return []error{fmt.Errorf("validate previous resource %q runtime files: %w", previous, err)}
		}
		if _, err := validateExtractedTreeSize(previous, maxInstallBytes); err != nil {
			return []error{fmt.Errorf("validate previous resource %q tree: %w", previous, err)}
		}
	}

	if errors.Is(targetErr, os.ErrNotExist) && previousManifest != nil {
		if err := validateManagedBackupShape(directory, true); err != nil {
			return []error{fmt.Errorf("validate resource backup %q: %w", directory, err)}
		}
		if err := ensureNoSymlinkComponents(root, filepath.Dir(target), false); err != nil {
			return []error{fmt.Errorf("validate resource restore target %q: %w", target, err)}
		}
		if err := os.Rename(previous, target); err != nil {
			return []error{fmt.Errorf("restore resource %q from backup: %w", metadata.ModuleID, err)}
		}
		if err := removeEmptyManagedBackup(directory); err != nil {
			return []error{fmt.Errorf("clean restored resource backup %q: %w", directory, err)}
		}
		return nil
	}

	stale := now.Sub(time.Unix(metadata.CreatedUnix, 0)) >= staleManagedArtifactAge
	if !stale {
		return nil
	}
	if targetErr == nil {
		if targetInfo.Mode()&os.ModeSymlink != 0 || !targetInfo.IsDir() {
			return []error{fmt.Errorf("%w: backup target %q is not a regular directory", ErrUnsafePath, target)}
		}
		targetManifest, err := readModuleInfo(filepath.Join(target, ModuleJSONName))
		if err != nil || targetManifest.ID != metadata.ModuleID {
			if err == nil {
				err = fmt.Errorf("manifest id %q does not match transaction module %q", targetManifest.ID, metadata.ModuleID)
			}
			return []error{fmt.Errorf("refuse to clean backup while target %q is unrecognized: %w", target, err)}
		}
		if err := validateModuleRuntimePaths(*targetManifest); err != nil {
			return []error{fmt.Errorf("refuse to clean backup while target %q has invalid runtime paths: %w", target, err)}
		}
		if err := validateInstalledRuntimeFiles(target, *targetManifest); err != nil {
			return []error{fmt.Errorf("refuse to clean backup while target %q has invalid runtime files: %w", target, err)}
		}
		if _, err := validateExtractedTreeSize(target, maxInstallBytes); err != nil {
			return []error{fmt.Errorf("refuse to clean backup while target %q has an invalid tree: %w", target, err)}
		}
	}
	if err := validateManagedBackupShape(directory, previousErr == nil); err != nil {
		return []error{fmt.Errorf("refuse to clean resource backup %q: %w", directory, err)}
	}
	if err := os.RemoveAll(directory); err != nil {
		return []error{fmt.Errorf("clean stale resource backup %q: %w", directory, err)}
	}
	return nil
}

func validateManagedBackupShape(directory string, hasPrevious bool) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	allowed := map[string]bool{managedArtifactMarker: true}
	if hasPrevious {
		allowed["previous"] = true
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return fmt.Errorf("unexpected top-level entry %q", entry.Name())
		}
	}
	return nil
}

func removeEmptyManagedBackup(directory string) error {
	if err := os.Remove(filepath.Join(directory, managedArtifactMarker)); err != nil {
		return err
	}
	return os.Remove(directory)
}

func removeManagedInstallArtifact(root, directory string, metadata managedArtifactMetadata) error {
	current, err := readManagedArtifactMetadata(root, directory)
	if err != nil {
		return err
	}
	if current != metadata {
		return errors.New("resource transaction marker changed during cleanup")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	allowed := map[string]bool{
		managedArtifactMarker: true,
		"module":              true,
		"downloads":           true,
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return fmt.Errorf("unexpected top-level entry %q", entry.Name())
		}
		if entry.Name() != managedArtifactMarker && !entry.IsDir() {
			return fmt.Errorf("expected staging directory %q", entry.Name())
		}
	}
	return os.RemoveAll(directory)
}
