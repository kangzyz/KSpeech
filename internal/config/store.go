package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

type Change struct {
	Keys []string
}

const (
	IssueUserConfigRecovered     = "user_config_recovered"
	IssueUserConfigQuarantined   = "user_config_quarantined"
	IssueBackupConfigRecovered   = "backup_config_recovered"
	IssueBackupConfigQuarantined = "backup_config_quarantined"
)

// Issue is a non-fatal configuration warning discovered while opening a
// store. Paths point to the preserved source of the warning when applicable.
type Issue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
}

type Store struct {
	mu          sync.RWMutex
	path        string
	values      map[string]any
	issues      []Issue
	subscribers map[chan Change]struct{}
}

func DefaultUserDataDir() (string, error) {
	base := os.Getenv("APPDATA")
	if base == "" {
		var err error
		base, err = os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("locate user config directory: %w", err)
		}
	}
	return filepath.Join(base, "KSpeech"), nil
}

// Open loads defaults, applies an optional packaged default_config.json, and
// finally applies the user's existing flat config.json. The merge keeps old
// partial configuration files usable while retaining every legacy key.
func Open(userDataDir, packagedDefaultsPath string) (*Store, error) {
	if userDataDir == "" {
		var err error
		userDataDir, err = DefaultUserDataDir()
		if err != nil {
			return nil, err
		}
	}

	s := &Store{
		path:        filepath.Join(userDataDir, "config.json"),
		values:      cloneMap(Defaults()),
		subscribers: make(map[chan Change]struct{}),
	}

	if packagedDefaultsPath != "" {
		if err := mergeJSONFile(s.values, packagedDefaultsPath, true); err != nil {
			return nil, fmt.Errorf("load packaged defaults: %w", err)
		}
	}
	if err := s.loadUserConfig(); err != nil {
		return nil, fmt.Errorf("load user config: %w", err)
	}
	return s, nil
}

func (s *Store) Path() string { return s.path }

// Issues returns an immutable point-in-time copy of non-fatal warnings found
// while opening the store.
func (s *Store) Issues() []Issue {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Issue(nil), s.issues...)
}

func (s *Store) Snapshot() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneMap(s.values)
}

func (s *Store) Raw(key string) (any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.values[key]
	return v, ok
}

func (s *Store) String(key string) string {
	v, _ := s.Raw(key)
	switch value := v.(type) {
	case string:
		return value
	case json.Number:
		return value.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(value)
	}
}

func (s *Store) Bool(key string) bool {
	v, _ := s.Raw(key)
	switch value := v.(type) {
	case bool:
		return value
	case string:
		parsed, _ := strconv.ParseBool(value)
		return parsed
	case json.Number:
		parsed, _ := strconv.ParseInt(value.String(), 10, 64)
		return parsed != 0
	default:
		return false
	}
}

func (s *Store) Int(key string) int {
	v, _ := s.Raw(key)
	switch value := v.(type) {
	case int:
		return value
	case float64:
		return int(value)
	case json.Number:
		parsed, _ := strconv.ParseInt(value.String(), 10, 64)
		return int(parsed)
	case string:
		parsed, _ := strconv.Atoi(value)
		return parsed
	default:
		return 0
	}
}

func (s *Store) Uint32(key string) uint32 {
	v, _ := s.Raw(key)
	switch value := v.(type) {
	case uint32:
		return value
	case int:
		return uint32(value)
	case float64:
		return uint32(value)
	case json.Number:
		parsed, _ := strconv.ParseUint(value.String(), 10, 32)
		return uint32(parsed)
	case string:
		parsed, _ := strconv.ParseUint(value, 0, 32)
		return uint32(parsed)
	default:
		return 0
	}
}

func (s *Store) IntSlice(key string) []int {
	v, _ := s.Raw(key)
	items, ok := v.([]any)
	if !ok {
		if values, ok := v.([]int); ok {
			return append([]int(nil), values...)
		}
		return nil
	}
	result := make([]int, 0, len(items))
	for _, item := range items {
		switch value := item.(type) {
		case float64:
			result = append(result, int(value))
		case json.Number:
			parsed, err := strconv.Atoi(value.String())
			if err == nil {
				result = append(result, parsed)
			}
		case int:
			result = append(result, value)
		}
	}
	return result
}

func (s *Store) Set(key string, value any) error {
	return s.SetMany(map[string]any{key: value})
}

func (s *Store) SetMany(changes map[string]any) error {
	if len(changes) == 0 {
		return nil
	}
	s.mu.Lock()
	previous := cloneMap(s.values)
	keys := make([]string, 0, len(changes))
	for key, value := range changes {
		s.values[key] = value
		keys = append(keys, key)
	}
	if err := s.saveLocked(); err != nil {
		s.values = previous
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()
	s.publish(Change{Keys: keys})
	return nil
}

func (s *Store) Delete(key string) error {
	s.mu.Lock()
	previous, existed := s.values[key]
	delete(s.values, key)
	if err := s.saveLocked(); err != nil {
		if existed {
			s.values[key] = previous
		}
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()
	s.publish(Change{Keys: []string{key}})
	return nil
}

func (s *Store) Subscribe(buffer int) (<-chan Change, func()) {
	if buffer < 1 {
		buffer = 1
	}
	ch := make(chan Change, buffer)
	s.mu.Lock()
	s.subscribers[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		if _, ok := s.subscribers[ch]; ok {
			delete(s.subscribers, ch)
			close(ch)
		}
		s.mu.Unlock()
	}
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	data, err := json.MarshalIndent(s.values, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(s.path), "config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("flush temporary config: %w", err)
	}
	if err = tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err = replaceFile(tmpName, s.path); err != nil {
		cleanup()
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

func (s *Store) publish(change Change) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for ch := range s.subscribers {
		select {
		case ch <- change:
		default:
		}
	}
}

func mergeJSONFile(dst map[string]any, path string, missingOK bool) error {
	configFile, err := readConfigFile(path, missingOK)
	if err != nil {
		return err
	}
	if !configFile.exists {
		return nil
	}
	mergeValues(dst, configFile.values)
	return nil
}

type configFile struct {
	data   []byte
	values map[string]any
	exists bool
}

type configContentError struct {
	cause error
}

func (e *configContentError) Error() string { return e.cause.Error() }
func (e *configContentError) Unwrap() error { return e.cause }

func readConfigFile(path string, missingOK bool) (configFile, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && missingOK {
		return configFile{}, nil
	}
	if err != nil {
		return configFile{}, err
	}
	values, err := decodeConfig(data)
	result := configFile{data: data, values: values, exists: true}
	if err != nil {
		return result, &configContentError{cause: err}
	}
	return result, nil
}

func decodeConfig(data []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var values map[string]any
	if err := decoder.Decode(&values); err != nil {
		return nil, err
	}
	if values == nil {
		return nil, errors.New("config must be a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("config contains multiple JSON values")
		}
		return nil, fmt.Errorf("invalid trailing config data: %w", err)
	}
	return values, nil
}

func (s *Store) loadUserConfig() error {
	main, err := readConfigFile(s.path, true)
	if err == nil {
		if main.exists {
			mergeValues(s.values, main.values)
			return nil
		}
		return s.recoverMissingUserConfig()
	}
	var contentErr *configContentError
	if !errors.As(err, &contentErr) {
		return err
	}
	return s.recoverInvalidUserConfig(main.data, contentErr)
}

func (s *Store) recoverMissingUserConfig() error {
	backupPath := s.path + ".previous"
	backup, err := readConfigFile(backupPath, true)
	if err == nil {
		if !backup.exists {
			return nil
		}
		if err := os.Rename(backupPath, s.path); err != nil {
			return fmt.Errorf("restore previous config: %w", err)
		}
		mergeValues(s.values, backup.values)
		s.issues = append(s.issues, Issue{
			Code:    IssueBackupConfigRecovered,
			Message: "Recovered the user configuration after an interrupted replacement.",
			Path:    s.path,
		})
		return nil
	}
	var contentErr *configContentError
	if !errors.As(err, &contentErr) {
		return fmt.Errorf("inspect previous config: %w", err)
	}
	quarantined, quarantineErr := quarantineConfigFile(backupPath, backup.data)
	if quarantineErr != nil {
		return fmt.Errorf("quarantine invalid previous config: %w", quarantineErr)
	}
	s.issues = append(s.issues, Issue{
		Code:    IssueBackupConfigQuarantined,
		Message: fmt.Sprintf("Ignored an invalid previous configuration: %v", contentErr),
		Path:    quarantined,
	})
	return nil
}

func (s *Store) recoverInvalidUserConfig(data []byte, contentErr error) error {
	backupPath := s.path + ".previous"
	backup, backupErr := readConfigFile(backupPath, true)
	if backupErr != nil {
		var backupContentErr *configContentError
		if !errors.As(backupErr, &backupContentErr) {
			return fmt.Errorf("inspect previous config: %w", backupErr)
		}
	}

	quarantined, err := quarantineConfigFile(s.path, data)
	if err != nil {
		return fmt.Errorf("quarantine invalid config: %w", err)
	}
	if backupErr == nil && backup.exists {
		if err := os.Rename(backupPath, s.path); err != nil {
			_ = os.Rename(quarantined, s.path)
			return fmt.Errorf("restore previous config: %w", err)
		}
		mergeValues(s.values, backup.values)
		s.issues = append(s.issues, Issue{
			Code: IssueUserConfigRecovered,
			Message: fmt.Sprintf(
				"The user configuration was invalid and a valid previous version was restored: %v",
				contentErr,
			),
			Path: quarantined,
		})
		return nil
	}

	message := fmt.Sprintf(
		"The user configuration was invalid and defaults were loaded; the original was preserved: %v",
		contentErr,
	)
	if backupErr != nil {
		message += fmt.Sprintf(" The previous configuration was also invalid and was not used: %v", backupErr)
	}
	s.issues = append(s.issues, Issue{
		Code:    IssueUserConfigQuarantined,
		Message: message,
		Path:    quarantined,
	})
	return nil
}

func quarantineConfigFile(path string, data []byte) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("config path is not a regular file: %s", path)
	}
	quarantined, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".corrupt-*")
	if err != nil {
		return "", err
	}
	quarantinedPath := quarantined.Name()
	cleanup := func() { _ = os.Remove(quarantinedPath) }
	if err := quarantined.Chmod(info.Mode().Perm()); err != nil {
		_ = quarantined.Close()
		cleanup()
		return "", err
	}
	if _, err := quarantined.Write(data); err != nil {
		_ = quarantined.Close()
		cleanup()
		return "", err
	}
	if err := quarantined.Sync(); err != nil {
		_ = quarantined.Close()
		cleanup()
		return "", err
	}
	if err := quarantined.Close(); err != nil {
		cleanup()
		return "", err
	}
	if err := os.Remove(path); err != nil {
		cleanup()
		return "", err
	}
	return quarantinedPath, nil
}

func mergeValues(dst, values map[string]any) {
	for key, value := range values {
		dst[key] = value
	}
}

func cloneMap(values map[string]any) map[string]any {
	result := make(map[string]any, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
