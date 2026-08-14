package resource

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Install executes a marketplace module's install plan in an isolated
// same-volume staging directory. The currently installed user module is not
// moved until every step and the resulting manifest have been validated.
// Download SHA256 values remain optional for legacy manifests: when omitted,
// the default HTTPS policy protects transport but this API pins no artifact
// identity.
func (m *Manager) Install(ctx context.Context, module ModuleInfo, progress ProgressFunc) error {
	if ctx == nil {
		return errors.New("install resource: nil context")
	}
	if err := m.validateInstallPlan(module); err != nil {
		return err
	}
	budget := newInstallBudget(m.maxInstallBytes)
	ctx = withInstallBudget(ctx, budget)
	ctx = withDownloadBudget(ctx, newDownloadBudget(m.maxTransactionDownloadBytes))
	release, err := m.acquireModule(ctx, module.ID)
	if err != nil {
		return fmt.Errorf("install resource %q: %w", module.ID, err)
	}
	defer release()
	target, err := m.installTarget(ctx, module.ID)
	if err != nil {
		return fmt.Errorf("resolve install target for %q: %w", module.ID, err)
	}
	if err := ensurePathChainNoSymlinks(m.userDataDir, m.userRoot, true); err != nil {
		return fmt.Errorf("validate user resource root: %w", err)
	}

	notifyProgress(progress, Progress{Stage: ProgressPreparing, TotalSteps: len(module.InstallSteps)})
	if err := os.MkdirAll(m.userRoot, 0o755); err != nil {
		return fmt.Errorf("create user resource directory: %w", err)
	}
	workDir, err := createManagedArtifact(m.userRoot, managedArtifactInstall, module.ID, "", time.Now())
	if err != nil {
		return fmt.Errorf("create resource staging directory: %w", err)
	}
	markInactive := m.markManagedArtifactActive(workDir)
	defer markInactive()
	defer os.RemoveAll(workDir)
	moduleRoot := filepath.Join(workDir, "module")
	downloadRoot := filepath.Join(workDir, "downloads")
	if err := os.Mkdir(moduleRoot, 0o755); err != nil {
		return fmt.Errorf("create module staging directory: %w", err)
	}
	if err := os.Mkdir(downloadRoot, 0o700); err != nil {
		return fmt.Errorf("create download staging directory: %w", err)
	}

	downloads := make(map[int]downloadArtifact)
	for index := range module.InstallSteps {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("install resource %q: %w", module.ID, err)
		}
		step := module.InstallSteps[index]
		switch step.Type {
		case InstallStepDownload:
			artifact, err := m.download(ctx, index, len(module.InstallSteps), step, downloadRoot, progress)
			if err != nil {
				return fmt.Errorf("install resource %q step %d (%s): %w", module.ID, index, step.Type, err)
			}
			downloads[index] = artifact
		case InstallStepExtract:
			notifyProgress(progress, Progress{Stage: ProgressExtracting, Step: index, TotalSteps: len(module.InstallSteps)})
			if err := m.extractStep(ctx, index, step, downloads, moduleRoot); err != nil {
				return fmt.Errorf("install resource %q step %d (%s): %w", module.ID, index, step.Type, err)
			}
		case InstallStepSaveFile:
			notifyProgress(progress, Progress{Stage: ProgressWriting, Step: index, TotalSteps: len(module.InstallSteps)})
			if err := m.saveFileStep(ctx, index, step, downloads, moduleRoot); err != nil {
				return fmt.Errorf("install resource %q step %d (%s): %w", module.ID, index, step.Type, err)
			}
		case InstallStepWriteFile:
			notifyProgress(progress, Progress{Stage: ProgressWriting, Step: index, TotalSteps: len(module.InstallSteps)})
			if err := writeFileStepWithBudget(moduleRoot, step, budget); err != nil {
				return fmt.Errorf("install resource %q step %d (%s): %w", module.ID, index, step.Type, err)
			}
		case InstallStepWriteModuleJSON:
			notifyProgress(progress, Progress{Stage: ProgressWriting, Step: index, TotalSteps: len(module.InstallSteps)})
			if err := writeModuleJSONWithBudget(moduleRoot, module, budget); err != nil {
				return fmt.Errorf("install resource %q step %d (%s): %w", module.ID, index, step.Type, err)
			}
		default:
			return fmt.Errorf("install resource %q step %d: %w: unknown install step %q", module.ID, index, ErrInvalidModule, step.Type)
		}
	}

	stagedBytes, err := validateExtractedTreeSize(moduleRoot, m.maxInstallBytes)
	if err != nil {
		return fmt.Errorf("validate staged resource %q: %w", module.ID, err)
	}
	if err := budget.ensureAtLeast(stagedBytes); err != nil {
		return fmt.Errorf("validate staged resource %q: %w", module.ID, err)
	}
	stagedInfo, err := readModuleInfo(filepath.Join(moduleRoot, ModuleJSONName))
	if err != nil {
		return fmt.Errorf("validate staged resource %q manifest: %w", module.ID, err)
	}
	if stagedInfo.ID != module.ID {
		return fmt.Errorf("validate staged resource: %w: manifest id %q does not match %q", ErrInvalidModule, stagedInfo.ID, module.ID)
	}
	if stagedInfo.Version != module.Version {
		return fmt.Errorf("validate staged resource: %w: manifest version %d does not match %d", ErrInvalidModule, stagedInfo.Version, module.Version)
	}
	if err := validateModuleRuntimePaths(*stagedInfo); err != nil {
		return fmt.Errorf("validate staged resource %q: %w: %v", module.ID, ErrInvalidModule, err)
	}
	if err := validateInstalledRuntimeFiles(moduleRoot, *stagedInfo); err != nil {
		return fmt.Errorf("validate staged resource %q: %w", module.ID, err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("install resource %q: %w", module.ID, err)
	}

	notifyProgress(progress, Progress{Stage: ProgressActivating, Step: len(module.InstallSteps), TotalSteps: len(module.InstallSteps)})
	m.fsMu.Lock()
	if rootErr := ensurePathChainNoSymlinks(m.userDataDir, m.userRoot, false); rootErr != nil {
		m.fsMu.Unlock()
		return fmt.Errorf("validate user resource root before activation: %w", rootErr)
	}
	cleanupIssue, err := activateDirectory(moduleRoot, target, m.userRoot, module.ID)
	m.fsMu.Unlock()
	if err != nil {
		return fmt.Errorf("activate resource %q: %w", module.ID, err)
	}
	if cleanupIssue != nil {
		m.reportIssue(cleanupIssue)
	}
	notifyProgress(progress, Progress{Stage: ProgressComplete, Step: len(module.InstallSteps), TotalSteps: len(module.InstallSteps)})
	return nil
}

func (m *Manager) installTarget(ctx context.Context, id string) (string, error) {
	canonical, err := safeJoin(m.userRoot, id, false)
	if err != nil {
		return "", err
	}
	m.fsMu.Lock()
	recoveryIssues, recoveryErr := m.recoverManagedArtifactsLocked(ctx)
	if recoveryErr != nil {
		m.fsMu.Unlock()
		return "", fmt.Errorf("recover resource transactions: %w", recoveryErr)
	}
	resources, _, scanErr := m.scanRoot(ctx, m.userRoot, true)
	m.fsMu.Unlock()
	for _, issue := range recoveryIssues {
		m.reportIssue(issue)
	}
	if scanErr != nil {
		return "", scanErr
	}
	for index := range resources {
		if resources[index].ID() == id {
			return safeExistingChild(m.userRoot, resources[index].LocalDir)
		}
	}
	return canonical, nil
}

func (m *Manager) validateInstallPlan(module ModuleInfo) error {
	if err := validateModuleID(module.ID); err != nil {
		return err
	}
	if err := validateModuleRuntimePaths(module); err != nil {
		return fmt.Errorf("%w %q: %v", ErrInvalidModule, module.ID, err)
	}
	if len(module.InstallSteps) == 0 {
		return fmt.Errorf("%w %q: install plan is empty", ErrInvalidModule, module.ID)
	}
	if len(module.InstallSteps) > m.maxInstallSteps {
		return fmt.Errorf("%w %q: install plan has %d steps, limit is %d", ErrInvalidModule, module.ID, len(module.InstallSteps), m.maxInstallSteps)
	}
	downloadSteps := make(map[int]struct{})
	for index, step := range module.InstallSteps {
		switch step.Type {
		case InstallStepDownload:
			if _, err := validateResourceURL(step.DownloadURL, m.allowInsecureHTTP); err != nil {
				return fmt.Errorf("%w %q step %d: invalid download URL: %w", ErrInvalidModule, module.ID, index, err)
			}
			if step.SHA256 != "" {
				if _, err := parseSHA256(step.SHA256); err != nil {
					return fmt.Errorf("%w %q step %d: %v", ErrInvalidModule, module.ID, index, err)
				}
			}
			downloadSteps[index] = struct{}{}
		case InstallStepExtract:
			sourceStep := index - 1
			if step.ExtractStep != nil {
				sourceStep = *step.ExtractStep
			}
			if sourceStep < 0 || sourceStep >= index {
				return fmt.Errorf("%w %q step %d: extractStep %d must reference an earlier download", ErrInvalidModule, module.ID, index, sourceStep)
			}
			if _, exists := downloadSteps[sourceStep]; !exists {
				return fmt.Errorf("%w %q step %d: extractStep %d is not a download", ErrInvalidModule, module.ID, index, sourceStep)
			}
			if step.ExtractTo != "" {
				if _, err := cleanRelativePath(step.ExtractTo, true); err != nil {
					return fmt.Errorf("%w %q step %d extractTo: %v", ErrInvalidModule, module.ID, index, err)
				}
			}
		case InstallStepSaveFile:
			sourceStep := index - 1
			if step.SaveStep != nil {
				sourceStep = *step.SaveStep
			}
			if sourceStep < 0 || sourceStep >= index {
				return fmt.Errorf("%w %q step %d: saveStep %d must reference an earlier download", ErrInvalidModule, module.ID, index, sourceStep)
			}
			if _, exists := downloadSteps[sourceStep]; !exists {
				return fmt.Errorf("%w %q step %d: saveStep %d is not a download", ErrInvalidModule, module.ID, index, sourceStep)
			}
			if _, err := cleanRelativePath(step.SavePath, false); err != nil {
				return fmt.Errorf("%w %q step %d savePath: %v", ErrInvalidModule, module.ID, index, err)
			}
		case InstallStepWriteFile:
			if _, err := cleanRelativePath(step.WritePath, false); err != nil {
				return fmt.Errorf("%w %q step %d writePath: %v", ErrInvalidModule, module.ID, index, err)
			}
		case InstallStepWriteModuleJSON:
		default:
			return fmt.Errorf("%w %q step %d: unknown install step %q", ErrInvalidModule, module.ID, index, step.Type)
		}
	}
	return nil
}

func validateModuleRuntimePaths(module ModuleInfo) error {
	if module.Type == ModuleTypePlugin && len(module.Assemblies) == 0 {
		return errors.New("plugin has no assemblies")
	}
	for index, assembly := range module.Assemblies {
		if _, err := cleanRelativePath(assembly, false); err != nil {
			return fmt.Errorf("assembly %d: %w", index, err)
		}
	}
	if module.SherpaOnnxModelPath == nil {
		if module.Type == ModuleTypeSherpaOnnxModel {
			return errors.New("sherpaonnx_model has no sherpaonnx paths")
		}
	} else {
		paths := []struct {
			name  string
			value string
		}{
			{name: "encoder", value: module.SherpaOnnxModelPath.EncoderPath},
			{name: "decoder", value: module.SherpaOnnxModelPath.DecoderPath},
			{name: "joiner", value: module.SherpaOnnxModelPath.JoinerPath},
			{name: "token", value: module.SherpaOnnxModelPath.TokenPath},
		}
		for _, modelPath := range paths {
			if _, err := cleanRelativePath(modelPath.value, false); err != nil {
				return fmt.Errorf("sherpaonnx %s: %w", modelPath.name, err)
			}
		}
	}
	if module.PunctuationPath == nil {
		if module.Type == ModuleTypePunctuationModel {
			return errors.New("punctuation_model has no punctuation paths")
		}
	} else if _, err := cleanRelativePath(module.PunctuationPath.ModelPath, false); err != nil {
		return fmt.Errorf("punctuation model: %w", err)
	}
	if module.SherpaNcnnModelPath == nil {
		// Old NCNN resources never had a path DTO. Keep them readable; the app
		// resolver requires one unambiguous conventional seven-file directory.
		return nil
	}
	ncnnPaths := []struct {
		name  string
		value string
	}{
		{name: "encoder_param", value: module.SherpaNcnnModelPath.EncoderParamPath},
		{name: "encoder_bin", value: module.SherpaNcnnModelPath.EncoderBinPath},
		{name: "decoder_param", value: module.SherpaNcnnModelPath.DecoderParamPath},
		{name: "decoder_bin", value: module.SherpaNcnnModelPath.DecoderBinPath},
		{name: "joiner_param", value: module.SherpaNcnnModelPath.JoinerParamPath},
		{name: "joiner_bin", value: module.SherpaNcnnModelPath.JoinerBinPath},
		{name: "tokens", value: module.SherpaNcnnModelPath.TokenPath},
	}
	for _, modelPath := range ncnnPaths {
		if _, err := cleanRelativePath(modelPath.value, false); err != nil {
			return fmt.Errorf("sherpancnn %s: %w", modelPath.name, err)
		}
	}
	return nil
}

func validateInstalledRuntimeFiles(moduleRoot string, module ModuleInfo) error {
	paths := append([]string(nil), module.Assemblies...)
	if module.SherpaOnnxModelPath != nil {
		paths = append(paths,
			module.SherpaOnnxModelPath.EncoderPath,
			module.SherpaOnnxModelPath.DecoderPath,
			module.SherpaOnnxModelPath.JoinerPath,
			module.SherpaOnnxModelPath.TokenPath,
		)
	}
	if module.PunctuationPath != nil {
		paths = append(paths, module.PunctuationPath.ModelPath)
	}
	if module.SherpaNcnnModelPath != nil {
		paths = append(paths,
			module.SherpaNcnnModelPath.EncoderParamPath,
			module.SherpaNcnnModelPath.EncoderBinPath,
			module.SherpaNcnnModelPath.DecoderParamPath,
			module.SherpaNcnnModelPath.DecoderBinPath,
			module.SherpaNcnnModelPath.JoinerParamPath,
			module.SherpaNcnnModelPath.JoinerBinPath,
			module.SherpaNcnnModelPath.TokenPath,
		)
	}
	for _, relative := range paths {
		path, err := safeJoin(moduleRoot, relative, false)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("runtime file %q: %w", relative, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("%w: runtime path %q is not a regular file", ErrUnsafePath, relative)
		}
	}
	return nil
}

type downloadArtifact struct {
	path string
	url  string
}

func (m *Manager) download(
	ctx context.Context,
	stepIndex int,
	totalSteps int,
	step InstallStep,
	downloadRoot string,
	progress ProgressFunc,
) (downloadArtifact, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, step.DownloadURL, nil)
	if err != nil {
		return downloadArtifact{}, fmt.Errorf("create download request: %w", err)
	}
	request.Header.Set("Accept", "application/octet-stream, */*")
	// Disable transparent HTTP content decoding so size and SHA256 apply to the
	// exact artifact bytes stored on disk.
	request.Header.Set("Accept-Encoding", "identity")
	response, err := m.doRequest(request)
	if err != nil {
		return downloadArtifact{}, fmt.Errorf("download: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		return downloadArtifact{}, fmt.Errorf("download: unexpected HTTP status %s", response.Status)
	}
	if response.ContentLength > m.maxDownloadBytes {
		return downloadArtifact{}, fmt.Errorf("%w: server declared %d bytes, limit is %d bytes", ErrDownloadLimit, response.ContentLength, m.maxDownloadBytes)
	}
	transactionBudget := downloadBudgetFromContext(ctx)
	if response.ContentLength >= 0 {
		if err := transactionBudget.canConsume(uint64(response.ContentLength)); err != nil {
			return downloadArtifact{}, err
		}
	}

	destination := filepath.Join(downloadRoot, fmt.Sprintf("%d.download", stepIndex))
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return downloadArtifact{}, fmt.Errorf("create download file: %w", err)
	}
	hasher := sha256.New()
	total := response.ContentLength
	if total < 0 {
		total = 0
	}
	notifyProgress(progress, Progress{Stage: ProgressDownloading, Step: stepIndex, TotalSteps: totalSteps, Total: total})
	var downloadBody io.Reader = response.Body
	if m.maxDownloadBytes < math.MaxInt64 {
		downloadBody = io.LimitReader(response.Body, m.maxDownloadBytes+1)
	}
	downloadDestination := io.Writer(file)
	if transactionBudget != nil {
		downloadDestination = &downloadBudgetWriter{destination: file, budget: transactionBudget}
	}
	written, copyErr := copyDownload(ctx, io.MultiWriter(downloadDestination, hasher), downloadBody, total, func(completed int64) {
		notifyProgress(progress, Progress{
			Stage: ProgressDownloading, Step: stepIndex, TotalSteps: totalSteps,
			Completed: completed, Total: total,
		})
	})
	if copyErr == nil && written > m.maxDownloadBytes {
		copyErr = fmt.Errorf("%w: received more than %d bytes", ErrDownloadLimit, m.maxDownloadBytes)
	}
	if copyErr == nil {
		copyErr = file.Sync()
	}
	closeErr := file.Close()
	if copyErr != nil {
		return downloadArtifact{}, copyErr
	}
	if closeErr != nil {
		return downloadArtifact{}, fmt.Errorf("close download file: %w", closeErr)
	}
	if response.ContentLength >= 0 && written != response.ContentLength {
		return downloadArtifact{}, fmt.Errorf("download size mismatch: received %d bytes, expected %d", written, response.ContentLength)
	}
	if step.SHA256 != "" {
		expected, err := parseSHA256(step.SHA256)
		if err != nil {
			return downloadArtifact{}, err
		}
		actual := hasher.Sum(nil)
		if subtle.ConstantTimeCompare(expected, actual) != 1 {
			return downloadArtifact{}, fmt.Errorf("%w: expected %s, got %s", ErrChecksumMismatch, hex.EncodeToString(expected), hex.EncodeToString(actual))
		}
	}
	return downloadArtifact{path: destination, url: step.DownloadURL}, nil
}

func copyDownload(ctx context.Context, destination io.Writer, source io.Reader, total int64, callback func(int64)) (int64, error) {
	buffer := make([]byte, 128<<10)
	var written int64
	lastUpdate := time.Time{}
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		read, readErr := source.Read(buffer)
		if read > 0 {
			count, writeErr := destination.Write(buffer[:read])
			written += int64(count)
			if writeErr != nil {
				return written, fmt.Errorf("write download: %w", writeErr)
			}
			if count != read {
				return written, io.ErrShortWrite
			}
			now := time.Now()
			if lastUpdate.IsZero() || now.Sub(lastUpdate) >= 100*time.Millisecond || (total > 0 && written == total) {
				callback(written)
				lastUpdate = now
			}
		}
		if errors.Is(readErr, io.EOF) {
			callback(written)
			return written, nil
		}
		if readErr != nil {
			return written, fmt.Errorf("read download: %w", readErr)
		}
	}
}

func parseSHA256(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if len(value) >= len("sha256:") && strings.EqualFold(value[:len("sha256:")], "sha256:") {
		value = value[len("sha256:"):]
	}
	if len(value) != sha256.Size*2 {
		return nil, fmt.Errorf("sha256 must contain exactly %d hexadecimal characters", sha256.Size*2)
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("sha256 is not hexadecimal: %w", err)
	}
	return decoded, nil
}

func (m *Manager) extractStep(ctx context.Context, index int, step InstallStep, downloads map[int]downloadArtifact, moduleRoot string) error {
	sourceStep := index - 1
	if step.ExtractStep != nil {
		sourceStep = *step.ExtractStep
	}
	artifact, exists := downloads[sourceStep]
	if !exists {
		return fmt.Errorf("%w: extractStep %d does not reference a completed download", ErrInvalidModule, sourceStep)
	}
	destination := moduleRoot
	var err error
	if step.ExtractTo != "" {
		destination, err = safeJoin(moduleRoot, step.ExtractTo, true)
		if err != nil {
			return fmt.Errorf("extractTo: %w", err)
		}
	}
	if err := mkdirAllSafe(moduleRoot, destination, 0o755); err != nil {
		return fmt.Errorf("create extraction destination: %w", err)
	}
	archiveType, err := detectArchiveType(step.ExtractType, artifact.url, artifact.path)
	if err != nil {
		return err
	}
	extractor := m.extractors[archiveType]
	if extractor == nil {
		return fmt.Errorf("%w: %s", ErrUnsupportedArchive, archiveType)
	}
	if err := extractor(ctx, artifact.path, destination); err != nil {
		return err
	}
	stagedBytes, err := validateExtractedTreeSize(moduleRoot, m.maxInstallBytes)
	if err != nil {
		return err
	}
	return budgetFromContext(ctx).ensureAtLeast(stagedBytes)
}

// saveFileStep stores a downloaded artifact under the module unchanged. It
// copies rather than renames so the artifact stays available to any later step
// that references the same download, which mirrors how extract leaves its
// archive in place. The copy is bounded by the shared install budget and the
// staged tree is remeasured afterwards, exactly as extraction is.
func (m *Manager) saveFileStep(ctx context.Context, index int, step InstallStep, downloads map[int]downloadArtifact, moduleRoot string) error {
	sourceStep := index - 1
	if step.SaveStep != nil {
		sourceStep = *step.SaveStep
	}
	artifact, exists := downloads[sourceStep]
	if !exists {
		return fmt.Errorf("%w: saveStep %d does not reference a completed download", ErrInvalidModule, sourceStep)
	}
	destination, err := safeJoin(moduleRoot, step.SavePath, false)
	if err != nil {
		return err
	}
	if err := mkdirAllSafe(moduleRoot, filepath.Dir(destination), 0o755); err != nil {
		return fmt.Errorf("create save destination: %w", err)
	}
	source, err := os.Open(artifact.path)
	if err != nil {
		return fmt.Errorf("open downloaded artifact: %w", err)
	}
	defer source.Close()
	if err := writeRegularFileSafeLimited(moduleRoot, destination, source, 0o644, true, budgetFromContext(ctx)); err != nil {
		return err
	}
	stagedBytes, err := validateExtractedTreeSize(moduleRoot, m.maxInstallBytes)
	if err != nil {
		return err
	}
	return budgetFromContext(ctx).ensureAtLeast(stagedBytes)
}

func writeFileStep(moduleRoot string, step InstallStep) error {
	return writeFileStepWithBudget(moduleRoot, step, nil)
}

func writeFileStepWithBudget(moduleRoot string, step InstallStep, budget *installBudget) error {
	destination, err := safeJoin(moduleRoot, step.WritePath, false)
	if err != nil {
		return err
	}
	if err := mkdirAllSafe(moduleRoot, filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	return writeRegularFileSafeLimited(moduleRoot, destination, strings.NewReader(step.WriteContent), 0o644, true, budget)
}

func writeModuleJSON(moduleRoot string, module ModuleInfo) error {
	return writeModuleJSONWithBudget(moduleRoot, module, nil)
}

func writeModuleJSONWithBudget(moduleRoot string, module ModuleInfo, budget *installBudget) error {
	data, err := json.MarshalIndent(module, "", "  ")
	if err != nil {
		return fmt.Errorf("encode module manifest: %w", err)
	}
	data = append(data, '\n')
	destination, err := safeJoin(moduleRoot, ModuleJSONName, false)
	if err != nil {
		return err
	}
	return writeRegularFileSafeLimited(moduleRoot, destination, strings.NewReader(string(data)), 0o644, true, budget)
}

func activateDirectory(staged, target, userRoot, expectedID string) (cleanupIssue, err error) {
	if err := ensureNoSymlinkComponents(userRoot, filepath.Dir(target), true); err != nil {
		return nil, err
	}
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil, os.Rename(staged, target)
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("%w: existing target is not a regular directory", ErrUnsafePath)
	}
	currentInfo, err := readModuleInfo(filepath.Join(target, ModuleJSONName))
	if err != nil {
		return nil, fmt.Errorf("refuse to replace unrecognized target %q: %w", target, err)
	}
	if currentInfo.ID != expectedID {
		return nil, fmt.Errorf("%w: target %q belongs to module %q, not %q", ErrInvalidModule, target, currentInfo.ID, expectedID)
	}

	backupHolder, err := createManagedArtifact(
		userRoot,
		managedArtifactBackup,
		expectedID,
		filepath.Base(target),
		time.Now(),
	)
	if err != nil {
		return nil, fmt.Errorf("create activation backup directory: %w", err)
	}
	backup := filepath.Join(backupHolder, "previous")
	if err := os.Rename(target, backup); err != nil {
		_ = removeEmptyManagedBackup(backupHolder)
		return nil, fmt.Errorf("back up current resource: %w", err)
	}
	if err := os.Rename(staged, target); err != nil {
		rollbackErr := os.Rename(backup, target)
		cleanupErr := removeEmptyManagedBackup(backupHolder)
		if rollbackErr != nil {
			return nil, errors.Join(fmt.Errorf("activate staged resource: %w", err), fmt.Errorf("restore previous resource: %w", rollbackErr), cleanupErr)
		}
		return nil, errors.Join(fmt.Errorf("activate staged resource: %w", err), cleanupErr)
	}
	if err := validateManagedBackupShape(backupHolder, true); err != nil {
		return fmt.Errorf("clean old resource backup %q: %w", backupHolder, err), nil
	}
	if err := os.RemoveAll(backupHolder); err != nil {
		// Activation has already succeeded. Returning an installation failure
		// here would invite a caller to retry an operation that is actually
		// complete. Surface the stale backup as a non-fatal maintenance issue.
		return fmt.Errorf("clean old resource backup %q: %w", backupHolder, err), nil
	}
	return nil, nil
}

// activateDirectory is kept as a small receiver wrapper for focused package
// tests. Install uses the lower-level form directly so it can release fsMu
// before invoking the user-provided issue callback.
func (m *Manager) activateDirectory(staged, target string) error {
	expectedID := filepath.Base(target)
	if info, err := readModuleInfo(filepath.Join(target, ModuleJSONName)); err == nil {
		expectedID = info.ID
	}
	m.fsMu.Lock()
	cleanupIssue, err := activateDirectory(staged, target, m.userRoot, expectedID)
	m.fsMu.Unlock()
	if cleanupIssue != nil {
		m.reportIssue(cleanupIssue)
	}
	return err
}

// Remove deletes only a user-installed resource. Built-in modules are never
// removed, even when their manifest ID is supplied directly.
func (m *Manager) Remove(ctx context.Context, id string) error {
	if ctx == nil {
		return errors.New("remove resource: nil context")
	}
	if err := validateModuleID(id); err != nil {
		return err
	}
	release, err := m.acquireModule(ctx, id)
	if err != nil {
		return err
	}
	defer release()

	m.fsMu.Lock()
	recoveryIssues, recoveryErr := m.recoverManagedArtifactsLocked(ctx)
	if recoveryErr != nil {
		m.fsMu.Unlock()
		return fmt.Errorf("recover resource transactions: %w", recoveryErr)
	}
	userResources, _, err := m.scanRoot(ctx, m.userRoot, true)
	m.fsMu.Unlock()
	for _, issue := range recoveryIssues {
		m.reportIssue(issue)
	}
	if err != nil {
		return err
	}
	var installed *Resource
	for index := range userResources {
		if userResources[index].ID() == id {
			candidate := userResources[index]
			installed = &candidate
			break
		}
	}
	if installed == nil {
		builtInResources, _, scanErr := m.scanRoot(ctx, m.builtInRoot, false)
		if scanErr != nil {
			return scanErr
		}
		for index := range builtInResources {
			if builtInResources[index].ID() == id {
				return fmt.Errorf("%w: %s", ErrNotRemovable, id)
			}
		}
		return fmt.Errorf("%w: %s", ErrNotInstalled, id)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	directory, err := safeExistingChild(m.userRoot, installed.LocalDir)
	if err != nil {
		return err
	}

	m.fsMu.Lock()
	defer m.fsMu.Unlock()
	if err := ensurePathChainNoSymlinks(m.userDataDir, m.userRoot, false); err != nil {
		return fmt.Errorf("validate user resource root before removal: %w", err)
	}
	// Revalidate after waiting for readers so a path changed by another
	// filesystem actor is never removed based only on stale scan metadata.
	directory, err = safeExistingChild(m.userRoot, directory)
	if err != nil {
		return err
	}
	quarantine, err := os.MkdirTemp(m.userRoot, ".kspeech-remove-*")
	if err != nil {
		return fmt.Errorf("create removal staging directory: %w", err)
	}
	staged := filepath.Join(quarantine, "module")
	if err := os.Rename(directory, staged); err != nil {
		_ = os.Remove(quarantine)
		return fmt.Errorf("stage resource removal: %w", err)
	}
	if err := os.RemoveAll(quarantine); err != nil {
		rollbackErr := os.Rename(staged, directory)
		if rollbackErr != nil {
			return errors.Join(fmt.Errorf("delete resource: %w", err), fmt.Errorf("restore resource: %w", rollbackErr))
		}
		return fmt.Errorf("delete resource: %w", err)
	}
	return nil
}

func notifyProgress(callback ProgressFunc, progress Progress) {
	if callback != nil {
		callback(progress)
	}
}
