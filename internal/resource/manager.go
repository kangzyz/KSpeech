package resource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxManifestBytes    = 4 << 20
	maxMarketplaceBytes = 16 << 20
)

type moduleLock struct {
	semaphore chan struct{}
	users     int
}

type Manager struct {
	userDataDir                 string
	builtInRoot                 string
	userRoot                    string
	marketplaceURL              string
	client                      *http.Client
	marketplaceTimeout          time.Duration
	allowInsecureHTTP           bool
	maxDownloadBytes            int64
	maxTransactionDownloadBytes int64
	maxInstallBytes             int64
	maxInstallSteps             int
	extractors                  map[string]Extractor
	onIssue                     func(error)

	fsMu                   sync.RWMutex
	lockMu                 sync.Mutex
	moduleLocks            map[string]*moduleLock
	activeManagedArtifacts map[string]struct{}
}

func NewManager(options Options) (*Manager, error) {
	executableDir := options.ExecutableDir
	if executableDir == "" {
		executablePath, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("locate executable: %w", err)
		}
		executableDir = filepath.Dir(executablePath)
	}

	userDataDir := options.UserDataDir
	if userDataDir == "" {
		var err error
		userDataDir, err = defaultUserDataDir()
		if err != nil {
			return nil, err
		}
	}

	builtInRoot, err := filepath.Abs(filepath.Join(executableDir, PluginDirName))
	if err != nil {
		return nil, fmt.Errorf("resolve built-in resource directory: %w", err)
	}
	userRoot, err := filepath.Abs(filepath.Join(userDataDir, PluginDirName))
	if err != nil {
		return nil, fmt.Errorf("resolve user resource directory: %w", err)
	}

	marketplaceURL := strings.TrimSpace(options.MarketplaceURL)
	if marketplaceURL == "" {
		marketplaceURL = DefaultMarketURL
	}
	if _, err := validateResourceURL(marketplaceURL, options.AllowInsecureHTTP); err != nil {
		return nil, fmt.Errorf("invalid marketplace URL: %w", err)
	}
	timeout := options.MarketplaceTimeout
	if timeout == 0 {
		timeout = defaultHTTPTimeout
	}
	if timeout < 0 {
		return nil, fmt.Errorf("marketplace timeout must not be negative")
	}
	client := options.HTTPClient
	if client == nil {
		// Marketplace requests carry their own context deadline. A client
		// without a global timeout lets large model downloads run until their
		// caller-provided context is cancelled.
		//
		// The transport follows the system proxy as well as the environment:
		// on Windows the proxy is normally configured in the settings, which
		// Go's default transport never reads, so a user running a proxy client
		// would otherwise watch every GitHub request go out directly.
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = ProxyFromEnvironmentOrSystem
		client = &http.Client{Transport: transport}
	}
	maxDownloadBytes := options.MaxDownloadBytes
	if maxDownloadBytes == 0 {
		maxDownloadBytes = DefaultMaxDownloadBytes
	}
	if maxDownloadBytes < 0 {
		return nil, fmt.Errorf("maximum download bytes must not be negative")
	}
	maxTransactionDownloadBytes := options.MaxTransactionDownloadBytes
	if maxTransactionDownloadBytes == 0 {
		maxTransactionDownloadBytes = DefaultMaxTransactionDownloadBytes
	}
	if maxTransactionDownloadBytes < 0 {
		return nil, fmt.Errorf("maximum transaction download bytes must not be negative")
	}
	maxInstallBytes := options.MaxInstallBytes
	if maxInstallBytes == 0 {
		maxInstallBytes = DefaultMaxInstallBytes
	}
	if maxInstallBytes < 0 {
		return nil, fmt.Errorf("maximum install bytes must not be negative")
	}
	maxInstallSteps := options.MaxInstallSteps
	if maxInstallSteps == 0 {
		maxInstallSteps = DefaultMaxInstallSteps
	}
	if maxInstallSteps < 0 {
		return nil, fmt.Errorf("maximum install steps must not be negative")
	}

	extractors := builtinExtractors()
	for archiveType, extractor := range options.Extractors {
		key := normalizeArchiveType(archiveType)
		if key == "" || extractor == nil {
			return nil, fmt.Errorf("invalid extractor registration for %q", archiveType)
		}
		extractors[key] = extractor
	}

	return &Manager{
		userDataDir:                 filepath.Clean(userDataDir),
		builtInRoot:                 filepath.Clean(builtInRoot),
		userRoot:                    filepath.Clean(userRoot),
		marketplaceURL:              marketplaceURL,
		client:                      client,
		marketplaceTimeout:          timeout,
		allowInsecureHTTP:           options.AllowInsecureHTTP,
		maxDownloadBytes:            maxDownloadBytes,
		maxTransactionDownloadBytes: maxTransactionDownloadBytes,
		maxInstallBytes:             maxInstallBytes,
		maxInstallSteps:             maxInstallSteps,
		extractors:                  extractors,
		onIssue:                     options.OnIssue,
		moduleLocks:                 make(map[string]*moduleLock),
		activeManagedArtifacts:      make(map[string]struct{}),
	}, nil
}

func (m *Manager) markManagedArtifactActive(directory string) func() {
	directory = filepath.Clean(directory)
	m.lockMu.Lock()
	m.activeManagedArtifacts[directory] = struct{}{}
	m.lockMu.Unlock()
	return func() {
		m.lockMu.Lock()
		delete(m.activeManagedArtifacts, directory)
		m.lockMu.Unlock()
	}
}

func (m *Manager) managedArtifactActive(directory string) bool {
	m.lockMu.Lock()
	_, active := m.activeManagedArtifacts[filepath.Clean(directory)]
	m.lockMu.Unlock()
	return active
}

func defaultUserDataDir() (string, error) {
	base := os.Getenv("APPDATA")
	if base == "" {
		var err error
		base, err = os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("locate user data directory: %w", err)
		}
	}
	return filepath.Join(base, "KSpeech"), nil
}

func (m *Manager) BuiltInPluginsDir() string { return m.builtInRoot }
func (m *Manager) UserPluginsDir() string    { return m.userRoot }

// ScanLocal reads one directory level beneath both plugin roots. A malformed
// individual manifest is reported through OnIssue and skipped so that one bad
// plugin cannot make every other local resource unavailable.
func (m *Manager) ScanLocal(ctx context.Context) ([]Resource, error) {
	if ctx == nil {
		return nil, errors.New("scan resources: nil context")
	}
	m.fsMu.Lock()
	recoveryIssues, recoveryErr := m.recoverManagedArtifactsLocked(ctx)
	if recoveryErr != nil {
		m.fsMu.Unlock()
		return nil, fmt.Errorf("recover resource transactions: %w", recoveryErr)
	}
	builtIn, builtInIssues, err := m.scanRoot(ctx, m.builtInRoot, false)
	if err != nil {
		m.fsMu.Unlock()
		return nil, err
	}
	user, userIssues, err := m.scanRoot(ctx, m.userRoot, true)
	m.fsMu.Unlock()
	if err != nil {
		return nil, err
	}
	issues := append(recoveryIssues, builtInIssues...)
	issues = append(issues, userIssues...)
	for _, issue := range issues {
		m.reportIssue(issue)
	}

	// A user-installed update shadows the packaged copy with the same exact,
	// case-sensitive manifest ID. This also makes that update removable.
	byID := make(map[string]Resource, len(builtIn)+len(user))
	for _, candidate := range builtIn {
		mergeSameRoot(byID, candidate)
	}
	for _, candidate := range user {
		byID[candidate.ID()] = candidate
	}

	resources := make([]Resource, 0, len(byID))
	for _, item := range byID {
		resources = append(resources, item)
	}
	sort.Slice(resources, func(i, j int) bool { return resources[i].ID() < resources[j].ID() })
	return resources, nil
}

func (m *Manager) scanRoot(ctx context.Context, root string, removable bool) ([]Resource, []error, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("scan resource directory %q: %w", root, err)
	}

	byID := make(map[string]Resource)
	var issues []error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, issues, err
		}
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		directory := filepath.Join(root, entry.Name())
		manifestPath := filepath.Join(directory, ModuleJSONName)
		info, err := readModuleInfo(manifestPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			issues = append(issues, fmt.Errorf("load resource manifest %q: %w", manifestPath, err))
			continue
		}
		if err := validateModuleID(info.ID); err != nil {
			issues = append(issues, fmt.Errorf("load resource manifest %q: %w", manifestPath, err))
			continue
		}
		candidate := Resource{CanRemove: removable, LocalInfo: info, LocalDir: directory}
		mergeSameRoot(byID, candidate)
	}

	result := make([]Resource, 0, len(byID))
	for _, item := range byID {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID() < result[j].ID() })
	return result, issues, nil
}

func mergeSameRoot(resources map[string]Resource, candidate Resource) {
	id := candidate.ID()
	current, exists := resources[id]
	if !exists || candidate.LocalInfo.Version > current.LocalInfo.Version ||
		(candidate.LocalInfo.Version == current.LocalInfo.Version && candidate.LocalDir < current.LocalDir) {
		resources[id] = candidate
	}
}

func (m *Manager) reportIssue(err error) {
	if m.onIssue != nil {
		m.onIssue(err)
	}
}

func readModuleInfo(path string) (*ModuleInfo, error) {
	fileInfo, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if fileInfo.Mode()&os.ModeSymlink != 0 || !fileInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("manifest is not a regular file")
	}
	if fileInfo.Size() > maxManifestBytes {
		return nil, fmt.Errorf("manifest exceeds %d bytes", maxManifestBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var info ModuleInfo
	if err := decodeOneJSON(io.LimitReader(file, maxManifestBytes+1), &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func decodeOneJSON(reader io.Reader, destination any) error {
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

// FetchMarketplace obtains the configured marketplace using both the caller's
// context and a per-attempt request deadline. A failed attempt is retried once,
// because the usual failure is a connection that never completes rather than a
// rejection. It returns an error for a non-success status, malformed JSON, or a
// module without an ID.
func (m *Manager) FetchMarketplace(ctx context.Context) ([]ModuleInfo, error) {
	if ctx == nil {
		return nil, errors.New("fetch marketplace: nil context")
	}
	var lastErr error
	for attempt := 0; attempt < marketplaceAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("fetch marketplace: %w", ctx.Err())
			case <-time.After(marketplaceRetryDelay):
			}
		}
		modules, err := m.fetchMarketplaceOnce(ctx)
		if err == nil {
			return modules, nil
		}
		lastErr = err
		// The caller gave up, or the answer will not change on a second read.
		if ctx.Err() != nil || !errors.Is(err, errMarketplaceUnreachable) {
			break
		}
	}
	return nil, lastErr
}

// errMarketplaceUnreachable marks a failure that is worth another attempt: the
// request never produced a usable response. A rejected or malformed answer is
// deterministic and is not retried.
var errMarketplaceUnreachable = errors.New("marketplace is unreachable")

func (m *Manager) fetchMarketplaceOnce(ctx context.Context) ([]ModuleInfo, error) {
	requestCtx, cancel := context.WithTimeout(ctx, m.marketplaceTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, m.marketplaceURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create marketplace request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := m.doRequest(request)
	if err != nil {
		if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			// Name the cause the user can act on. The bare deadline message
			// says nothing about the network or a proxy that is not in use.
			err = fmt.Errorf("连接 %s 超时（%s）；请检查网络，或确认代理已在 Windows 代理设置里开启",
				request.URL.Host, m.marketplaceTimeout)
		}
		return nil, fmt.Errorf("fetch marketplace: %w: %w", errMarketplaceUnreachable, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		return nil, fmt.Errorf("fetch marketplace: unexpected HTTP status %s", response.Status)
	}

	data, err := io.ReadAll(io.LimitReader(response.Body, maxMarketplaceBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read marketplace: %w", err)
	}
	if len(data) > maxMarketplaceBytes {
		return nil, fmt.Errorf("read marketplace: response exceeds %d bytes", maxMarketplaceBytes)
	}
	var marketplace Marketplace
	if err := decodeOneJSON(bytes.NewReader(data), &marketplace); err != nil {
		return nil, fmt.Errorf("decode marketplace: %w", err)
	}
	for index := range marketplace.Modules {
		if err := validateModuleID(marketplace.Modules[index].ID); err != nil {
			return nil, fmt.Errorf("decode marketplace module %d: %w", index, err)
		}
	}
	return marketplace.Modules, nil
}

// List merges installed resources with the highest marketplace version for
// each exact ID. If the marketplace is unavailable, local resources are still
// returned alongside the fetch error.
func (m *Manager) List(ctx context.Context) ([]Resource, error) {
	local, err := m.ScanLocal(ctx)
	if err != nil {
		return nil, err
	}
	remote, fetchErr := m.FetchMarketplace(ctx)
	if fetchErr != nil {
		return local, fetchErr
	}

	byID := make(map[string]*Resource, len(local)+len(remote))
	for index := range local {
		item := local[index]
		byID[item.ID()] = &item
	}
	latestRemote := make(map[string]ModuleInfo, len(remote))
	for index := range remote {
		module := remote[index]
		current, exists := latestRemote[module.ID]
		if !exists || module.Version > current.Version {
			latestRemote[module.ID] = module
		}
	}
	for id, module := range latestRemote {
		moduleCopy := module
		if item, exists := byID[id]; exists {
			item.RemoteInfo = &moduleCopy
		} else {
			byID[id] = &Resource{RemoteInfo: &moduleCopy}
		}
	}

	result := make([]Resource, 0, len(byID))
	for _, item := range byID {
		result = append(result, *item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID() < result[j].ID() })
	return result, nil
}

// Local returns the installed resource with the exact, case-sensitive ID.
func (m *Manager) Local(ctx context.Context, id string) (Resource, bool, error) {
	resources, err := m.ScanLocal(ctx)
	if err != nil {
		return Resource{}, false, err
	}
	index := sort.Search(len(resources), func(index int) bool { return resources[index].ID() >= id })
	if index < len(resources) && resources[index].ID() == id {
		return resources[index], true, nil
	}
	return Resource{}, false, nil
}

func (m *Manager) acquireModule(ctx context.Context, id string) (func(), error) {
	m.lockMu.Lock()
	lock := m.moduleLocks[id]
	if lock == nil {
		lock = &moduleLock{semaphore: make(chan struct{}, 1)}
		m.moduleLocks[id] = lock
	}
	lock.users++
	m.lockMu.Unlock()

	select {
	case lock.semaphore <- struct{}{}:
		return func() { m.releaseModule(id, lock) }, nil
	case <-ctx.Done():
		m.lockMu.Lock()
		lock.users--
		if lock.users == 0 {
			delete(m.moduleLocks, id)
		}
		m.lockMu.Unlock()
		return nil, ctx.Err()
	}
}

func (m *Manager) releaseModule(id string, lock *moduleLock) {
	<-lock.semaphore
	m.lockMu.Lock()
	lock.users--
	if lock.users == 0 {
		delete(m.moduleLocks, id)
	}
	m.lockMu.Unlock()
}
