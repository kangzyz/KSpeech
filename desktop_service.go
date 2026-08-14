package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kangzyz/KSpeech/internal/assistant"
	"github.com/kangzyz/KSpeech/internal/audio"
	"github.com/kangzyz/KSpeech/internal/config"
	"github.com/kangzyz/KSpeech/internal/job"
	"github.com/kangzyz/KSpeech/internal/plugin"
	"github.com/kangzyz/KSpeech/internal/processlist"
	"github.com/kangzyz/KSpeech/internal/punctuation"
	"github.com/kangzyz/KSpeech/internal/recognizer/command"
	"github.com/kangzyz/KSpeech/internal/recognizer/sherpancnn"
	"github.com/kangzyz/KSpeech/internal/recognizer/sherpaonnx"
	"github.com/kangzyz/KSpeech/internal/resource"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

const (
	stateEvent        = "kspeech:state"
	liveStateEvent    = "kspeech:live"
	notificationEvent = "kspeech:notification"
	assistantEvent    = "kspeech:assistant"

	// The console is the single floating window: live captions and history on
	// the left, assistant insights in the middle, the assistant chat on the
	// right. Settings keep their own ordinary window.
	consoleWindowName  = "console"
	settingsWindowName = "settings"

	// A three-pane console needs more room than the old single caption line,
	// but it is still meant to float over other windows rather than fill the
	// screen. A stored size below the minimum is treated as a size from an
	// older version and replaced by the default.
	defaultConsoleWidth  = 940
	defaultConsoleHeight = 420
	minConsoleWidth      = 460
	minConsoleHeight     = 240
)

type BuildInfo struct {
	Version string
	Commit  string
}

type ConfigFieldOption struct {
	Value any    `json:"value"`
	Label string `json:"label"`
}

type ConfigField struct {
	Key     string              `json:"key"`
	Label   string              `json:"label"`
	Type    string              `json:"type"`
	Value   any                 `json:"value,omitempty"`
	Options []ConfigFieldOption `json:"options,omitempty"`
	Hint    string              `json:"hint,omitempty"`
}

type PluginOption struct {
	Key         string        `json:"key"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Available   bool          `json:"available"`
	NeedsAudio  *bool         `json:"needsAudio,omitempty"`
	Fields      []ConfigField `json:"fields,omitempty"`
	// Audio sources only: whether this input is captured, and the speaker label
	// drawn in front of its captions. A source that is not captured still
	// carries the label it would be given, so the settings page can offer it.
	Enabled bool   `json:"enabled,omitempty"`
	Label   string `json:"label,omitempty"`
}

// AudioChannelInput is one labeled audio input as the settings page sends it.
type AudioChannelInput struct {
	Source string `json:"source"`
	Label  string `json:"label"`
}

type ResourceItem struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Description    string  `json:"description"`
	DisplayVersion string  `json:"displayVersion"`
	Local          bool    `json:"local"`
	Removable      bool    `json:"removable"`
	NeedsUpdate    bool    `json:"needsUpdate"`
	Installable    bool    `json:"installable"`
	Busy           bool    `json:"busy,omitempty"`
	Progress       float64 `json:"progress,omitempty"`
	Error          string  `json:"error,omitempty"`
	moduleType     string
	// modelPath is the resolved file a punctuation resource offers. It is read
	// from disk once per refresh so building a snapshot stays free.
	modelPath string
}

type AppSnapshot struct {
	Status         job.Status         `json:"status"`
	RunningSeconds int64              `json:"runningSeconds"`
	Text           string             `json:"text"`
	Channels       []job.ChannelState `json:"channels"`
	Locked         bool               `json:"locked"`
	History        []job.HistoryEntry `json:"history"`
	Config         map[string]any     `json:"config"`
	AudioSources   []PluginOption     `json:"audioSources"`
	Recognizers    []PluginOption     `json:"recognizers"`
	Resources      []ResourceItem     `json:"resources"`
	// PunctuationModels are the installed punctuation resources, by resolved
	// model path, so the settings page can offer them instead of asking for a
	// path typed by hand.
	PunctuationModels []ConfigFieldOption `json:"punctuationModels"`
	Assistant         assistant.State     `json:"assistant"`
	Version           string              `json:"version"`
	Commit            string              `json:"commit"`
	Platform          string              `json:"platform"`
	LastError         string              `json:"lastError,omitempty"`
}

type UINotification struct {
	Title   string `json:"title"`
	Message string `json:"message"`
	Level   string `json:"level"`
}

type LiveState struct {
	Status         job.Status         `json:"status"`
	RunningSeconds int64              `json:"runningSeconds"`
	Text           string             `json:"text"`
	Channels       []job.ChannelState `json:"channels"`
	LastError      string             `json:"lastError,omitempty"`
}

type DesktopService struct {
	build     BuildInfo
	store     *config.Store
	registry  *plugin.Registry
	job       *job.Manager
	resources *resource.Manager
	assistant *assistant.Manager

	mu              sync.RWMutex
	app             *application.App
	notifier        *notifications.NotificationService
	consoleWindow   *application.WebviewWindow
	settingsWindow  *application.WebviewWindow
	ctx             context.Context
	locked          bool
	lastError       string
	devices         []audio.Device
	lastHistoryID   uint64
	jobRunning      bool
	resourceItems   []ResourceItem
	remoteResources map[string]resource.ModuleInfo
	resourceBusy    map[string]bool
	resourceRefresh atomic.Uint64
	refreshCancel   context.CancelFunc
	lastLivePublish time.Time
	livePublish     *time.Timer
	consoleSave     *time.Timer
	consoleSaveGen  uint64
	shuttingDown    bool
	publishMu       sync.Mutex
	consoleSaveMu   sync.Mutex
	lifecycleMu     sync.Mutex
	cancelJob       func()
	cancelNotice    func()
	cancelConfig    func()
	cancelAssistant func()
	operations      sync.WaitGroup
}

func NewDesktopService(build BuildInfo) (*DesktopService, error) {
	userDataDir, err := config.DefaultUserDataDir()
	if err != nil {
		return nil, err
	}
	executablePath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate executable: %w", err)
	}
	executableDir := filepath.Dir(executablePath)
	store, err := config.Open(userDataDir, filepath.Join(executableDir, "default_config.json"))
	if err != nil {
		return nil, err
	}
	registry := plugin.NewRegistry()
	service := &DesktopService{
		build:           build,
		store:           store,
		registry:        registry,
		remoteResources: make(map[string]resource.ModuleInfo),
		resourceBusy:    make(map[string]bool),
		resourceItems:   make([]ResourceItem, 0),
	}
	service.lastError = formatConfigIssues(store.Issues())
	resourceManager, err := resource.NewManager(resource.Options{
		ExecutableDir: executableDir,
		UserDataDir:   userDataDir,
		OnIssue:       service.reportResourceIssue,
	})
	if err != nil {
		return nil, err
	}
	service.resources = resourceManager
	if err := registerBuiltins(registry, service.sherpaModelResolver, service.sherpaNcnnModelResolver); err != nil {
		return nil, err
	}
	service.job = job.New(store, registry, job.WithRecognizerFactory(service.newRecognizer))
	service.assistant = assistant.New(store)
	if err := service.seedBuiltinRecognizerAssets(executableDir); err != nil {
		// Shipped extras are worth a line in the status bar, never a failed start.
		service.lastError = appendIssue(service.lastError, "内置热词与 ITN 文件未能写入配置："+err.Error())
	}
	return service, nil
}

const (
	// builtinHotwordsName and builtinRuleFstName ship beside the executable.
	// The hotwords list covers rail transit, AI, programming and general
	// technology terms; the rule file rewrites spoken numbers as digits.
	builtinHotwordsName = "hotwords.txt"
	builtinRuleFstName  = "itn_zh_number.fst"
	// builtinAssetsField records inside the recognizer's plugin configuration
	// that the shipped files were offered once. Without it, clearing either
	// field would be undone on the next launch.
	builtinAssetsField = "BuiltinAssets"
)

// seedBuiltinRecognizerAssets points the sherpa-onnx recognizer at the hotwords
// and ITN files shipped with the application. It fills them in once, so a user
// who clears either field keeps it cleared, and it repairs a stale built-in path
// after the application is reinstalled somewhere else.
func (s *DesktopService) seedBuiltinRecognizerAssets(executableDir string) error {
	key := config.PluginConfigKey(config.PluginKey(config.SherpaOnnxModule, config.SherpaOnnxID))
	values := parseJSONObject(s.store.String(key))
	hotwords := builtinAssetPath(executableDir, builtinHotwordsName)
	rules := builtinAssetPath(executableDir, builtinRuleFstName)

	patch := make(map[string]any)
	if repaired, ok := repairBuiltinAsset(values, "HotwordsFile", builtinHotwordsName, hotwords); ok {
		patch["HotwordsFile"] = repaired
		if repaired == "" {
			// Hotwords are the only reason this run needs beam search.
			patch["DecodingMethod"] = sherpaonnx.DefaultConfig().DecodingMethod
		}
	}
	if repaired, ok := repairBuiltinAsset(values, "RuleFsts", builtinRuleFstName, rules); ok {
		patch["RuleFsts"] = repaired
	}
	if _, seeded := values[builtinAssetsField]; !seeded {
		patch[builtinAssetsField] = 1
		if hotwords != "" && strings.TrimSpace(stringValue(values, "HotwordsFile")) == "" {
			patch["HotwordsFile"] = hotwords
			// sherpa-onnx only applies hotwords while decoding with beam
			// search, so the two settings have to arrive together.
			patch["DecodingMethod"] = "modified_beam_search"
		}
		if rules != "" && strings.TrimSpace(stringValue(values, "RuleFsts")) == "" {
			patch["RuleFsts"] = rules
		}
	}
	if len(patch) == 0 {
		return nil
	}
	encoded, err := mergePluginConfig(s.store.String(key), patch)
	if err != nil {
		return err
	}
	return s.store.Set(key, encoded)
}

// builtinAssetPath returns the shipped file's path, or empty when this build
// does not carry it — a plain `go build` output directory, for instance.
func builtinAssetPath(executableDir, name string) string {
	if executableDir == "" {
		return ""
	}
	path := filepath.Join(executableDir, name)
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return path
}

// repairBuiltinAsset reports a replacement for a configured path that used to
// be one of the shipped files and no longer exists. A path the user chose, and
// a list of several files, are left exactly as they are.
func repairBuiltinAsset(values map[string]any, field, name, current string) (string, bool) {
	value := strings.TrimSpace(stringValue(values, field))
	if value == "" || value == current || !strings.EqualFold(filepath.Base(value), name) {
		return "", false
	}
	if info, err := os.Stat(value); err == nil && info.Mode().IsRegular() {
		return "", false
	}
	return current, true
}

func appendIssue(existing, message string) string {
	if existing == "" {
		return message
	}
	return existing + "；" + message
}

// builtinPlugin pairs a registry key with a way to build that plugin. Building
// is kept separate from registering because capturing several audio inputs at
// once needs a second, third recognizer instance beyond the registered one.
type builtinPlugin struct {
	key   string
	build func() plugin.Plugin
}

func builtinPlugins(resolver sherpaonnx.ModelResolver, ncnnResolver sherpancnn.ModelResolver) []builtinPlugin {
	audioMetadata := func(id, name, description string) plugin.Metadata {
		return plugin.Metadata{ID: id, Name: name, Description: description, Version: "1"}
	}
	return []builtinPlugin{
		{
			key: config.PluginKey(config.AudioSourceWindowsModule, config.LoopbackAudioSourceID),
			build: func() plugin.Plugin {
				return audio.NewLoopback(audioMetadata(config.LoopbackAudioSourceID, "系统声音", "捕获 Windows 默认播放设备的声音，也就是会议里其他人的声音。"))
			},
		},
		{
			key: config.PluginKey(config.AudioSourceWindowsModule, config.MicrophoneAudioSourceID),
			build: func() plugin.Plugin {
				return audio.NewMicrophone(audioMetadata(config.MicrophoneAudioSourceID, "麦克风", "捕获默认或指定的 Windows 麦克风，也就是你自己的声音。"))
			},
		},
		{
			key: config.PluginKey(config.AudioSourceWindowsModule, config.ProcessAudioSourceID),
			build: func() plugin.Plugin {
				return audio.NewProcessLoopback(audioMetadata(config.ProcessAudioSourceID, "指定进程", "捕获指定进程及其子进程的声音。"))
			},
		},
		{
			key: config.PluginKey(config.CommandModule, config.CommandID),
			build: func() plugin.Plugin {
				return command.New(plugin.Metadata{
					ID:          config.CommandID,
					Name:        "外部命令",
					Description: "从外部程序的标准输出接收实时识别结果。",
					Version:     "1",
				})
			},
		},
		{
			key: config.PluginKey(config.SherpaOnnxModule, config.SherpaOnnxID),
			build: func() plugin.Plugin {
				return sherpaonnx.New(plugin.Metadata{
					ID:          config.SherpaOnnxID,
					Name:        "Sherpa ONNX",
					Description: "本地流式语音识别；发布构建通过 sherpa 标签启用原生后端。",
					Version:     "1",
				}, sherpaonnx.WithModelResolver(resolver))
			},
		},
		{
			key: config.PluginKey(config.SherpaNcnnModule, config.SherpaNcnnID),
			build: func() plugin.Plugin {
				return sherpancnn.New(plugin.Metadata{
					ID:          config.SherpaNcnnID,
					Name:        "Sherpa NCNN",
					Description: "兼容旧 NCNN 七文件模型；Windows 通过原生 C API 传递 Vulkan 请求，实际执行设备取决于随包运行库和驱动。",
					Version:     "1",
				}, sherpancnn.WithModelResolver(ncnnResolver))
			},
		},
	}
}

func registerBuiltins(registry *plugin.Registry, resolver sherpaonnx.ModelResolver, ncnnResolver sherpancnn.ModelResolver) error {
	for _, entry := range builtinPlugins(resolver, ncnnResolver) {
		if err := registry.Register(entry.key, entry.build()); err != nil {
			return err
		}
	}
	return nil
}

// newRecognizer builds one more instance of a built-in recognizer. The job
// manager asks for one per extra audio input; the instances are independent, so
// each input decodes its own speaker instead of both sharing one stream.
func (s *DesktopService) newRecognizer(key string) (plugin.Recognizer, error) {
	for _, entry := range builtinPlugins(s.sherpaModelResolver, s.sherpaNcnnModelResolver) {
		if entry.key != key {
			continue
		}
		recognizer, ok := entry.build().(plugin.Recognizer)
		if !ok {
			break
		}
		return recognizer, nil
	}
	return nil, fmt.Errorf("识别引擎 %q 无法为额外的音频输入创建实例", key)
}

func (s *DesktopService) sherpaModelResolver(ctx context.Context, modelID string) (sherpaonnx.ModelFiles, error) {
	local, exists, err := s.resources.Local(ctx, modelID)
	if err != nil {
		return sherpaonnx.ModelFiles{}, err
	}
	if !exists || local.LocalInfo == nil || local.LocalInfo.SherpaOnnxModelPath == nil {
		return sherpaonnx.ModelFiles{}, fmt.Errorf("sherpa-onnx model resource %q is not installed", modelID)
	}
	paths := local.LocalInfo.SherpaOnnxModelPath
	encoder, err := resolveConfinedModelFile(local.LocalDir, paths.EncoderPath)
	if err != nil {
		return sherpaonnx.ModelFiles{}, fmt.Errorf("encoder: %w", err)
	}
	decoder, err := resolveConfinedModelFile(local.LocalDir, paths.DecoderPath)
	if err != nil {
		return sherpaonnx.ModelFiles{}, fmt.Errorf("decoder: %w", err)
	}
	joiner, err := resolveConfinedModelFile(local.LocalDir, paths.JoinerPath)
	if err != nil {
		return sherpaonnx.ModelFiles{}, fmt.Errorf("joiner: %w", err)
	}
	tokens, err := resolveConfinedModelFile(local.LocalDir, paths.TokenPath)
	if err != nil {
		return sherpaonnx.ModelFiles{}, fmt.Errorf("tokens: %w", err)
	}
	return sherpaonnx.ModelFiles{
		Encoder: encoder,
		Decoder: decoder,
		Joiner:  joiner,
		Tokens:  tokens,
	}, nil
}

func (s *DesktopService) sherpaNcnnModelResolver(ctx context.Context, modelID string) (sherpancnn.ModelFiles, error) {
	local, exists, err := s.resources.Local(ctx, modelID)
	if err != nil {
		return sherpancnn.ModelFiles{}, err
	}
	if !exists || local.LocalInfo == nil {
		return sherpancnn.ModelFiles{}, fmt.Errorf("sherpa-ncnn model resource %q is not installed", modelID)
	}
	if paths := local.LocalInfo.SherpaNcnnModelPath; paths != nil {
		values := []struct {
			name  string
			value string
		}{
			{name: "encoder_param", value: paths.EncoderParamPath},
			{name: "encoder_bin", value: paths.EncoderBinPath},
			{name: "decoder_param", value: paths.DecoderParamPath},
			{name: "decoder_bin", value: paths.DecoderBinPath},
			{name: "joiner_param", value: paths.JoinerParamPath},
			{name: "joiner_bin", value: paths.JoinerBinPath},
			{name: "tokens", value: paths.TokenPath},
		}
		resolved := make([]string, len(values))
		for index, value := range values {
			path, resolveErr := resolveConfinedModelFile(local.LocalDir, value.value)
			if resolveErr != nil {
				return sherpancnn.ModelFiles{}, fmt.Errorf("%s: %w", value.name, resolveErr)
			}
			resolved[index] = path
		}
		return sherpancnn.ModelFiles{
			EncoderParam: resolved[0], EncoderBin: resolved[1],
			DecoderParam: resolved[2], DecoderBin: resolved[3],
			JoinerParam: resolved[4], JoinerBin: resolved[5], Tokens: resolved[6],
		}, nil
	}
	return discoverLegacyNcnnModelFiles(ctx, local.LocalDir)
}

func discoverLegacyNcnnModelFiles(ctx context.Context, root string) (sherpancnn.ModelFiles, error) {
	required := []string{"encoder.param", "encoder.bin", "decoder.param", "decoder.bin", "joiner.param", "joiner.bin", "tokens.txt"}
	byDirectory := make(map[string]map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("legacy sherpa-ncnn resource contains symlink %q", path)
		}
		if entry.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("legacy sherpa-ncnn resource contains non-regular file %q", path)
		}
		name := strings.ToLower(entry.Name())
		for _, expected := range required {
			if name == expected {
				directory := filepath.Dir(path)
				if byDirectory[directory] == nil {
					byDirectory[directory] = make(map[string]string)
				}
				byDirectory[directory][expected] = path
				break
			}
		}
		return nil
	})
	if err != nil {
		return sherpancnn.ModelFiles{}, fmt.Errorf("inspect legacy sherpa-ncnn model resource: %w", err)
	}
	var candidates []map[string]string
	for _, files := range byDirectory {
		if len(files) == len(required) {
			candidates = append(candidates, files)
		}
	}
	if len(candidates) != 1 {
		return sherpancnn.ModelFiles{}, fmt.Errorf("legacy sherpa-ncnn model resource must contain exactly one conventional seven-file model directory; found %d", len(candidates))
	}
	files := candidates[0]
	return sherpancnn.ModelFiles{
		EncoderParam: files[required[0]], EncoderBin: files[required[1]],
		DecoderParam: files[required[2]], DecoderBin: files[required[3]],
		JoinerParam: files[required[4]], JoinerBin: files[required[5]], Tokens: files[required[6]],
	}, nil
}

func (s *DesktopService) attachRuntime(
	app *application.App,
	notifier *notifications.NotificationService,
	consoleWindow, settingsWindow *application.WebviewWindow,
) {
	s.mu.Lock()
	s.app = app
	s.notifier = notifier
	s.consoleWindow = consoleWindow
	s.settingsWindow = settingsWindow
	s.mu.Unlock()
}

func (s *DesktopService) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()
	for _, key := range s.registry.Keys() {
		instance, _ := s.registry.Get(key)
		if err := instance.Init(ctx); err != nil {
			return fmt.Errorf("initialize plugin %q: %w", key, err)
		}
	}
	s.cancelJob = s.job.Subscribe(func(snapshot job.Snapshot) {
		if snapshot.LiveOnly() {
			s.publishLive(snapshot)
			return
		}
		s.observeTranscript(snapshot)
		s.publish()
	})
	s.cancelNotice = s.job.SubscribeNotifications(s.publishNotification)
	s.cancelAssistant = s.assistant.Subscribe(s.publishAssistant)
	changes, cancel := s.store.Subscribe(4)
	s.cancelConfig = cancel
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-changes:
				if !ok {
					return
				}
				s.publish()
			}
		}
	}()
	go s.refreshDevices(ctx)
	s.RefreshResources()
	if s.store.Bool(config.GeneralStartOnLaunch) {
		go func() {
			if err := s.Start(); err != nil {
				s.setLastError(err)
			}
		}()
	}
	return nil
}

func (s *DesktopService) ServiceShutdown() error {
	if s.cancelConfig != nil {
		s.cancelConfig()
	}
	if s.cancelNotice != nil {
		s.cancelNotice()
	}
	if s.cancelJob != nil {
		s.cancelJob()
	}
	if s.cancelAssistant != nil {
		s.cancelAssistant()
	}
	s.mu.Lock()
	s.shuttingDown = true
	s.consoleSaveGen++
	if s.consoleSave != nil {
		s.consoleSave.Stop()
		s.consoleSave = nil
	}
	if s.livePublish != nil {
		s.livePublish.Stop()
		s.livePublish = nil
	}
	if s.refreshCancel != nil {
		s.refreshCancel()
		s.refreshCancel = nil
	}
	s.mu.Unlock()
	// Wait for a debounce callback that had already begun before shutdown. It
	// observes shuttingDown before touching the native window or config store.
	s.consoleSaveMu.Lock()
	s.consoleSaveMu.Unlock()
	var result []error
	if err := s.job.Close(); err != nil {
		result = append(result, err)
	}
	if s.assistant != nil {
		s.assistant.Close()
	}
	s.operations.Wait()
	for _, key := range s.registry.Keys() {
		instance, _ := s.registry.Get(key)
		if err := instance.Close(); err != nil {
			result = append(result, fmt.Errorf("close plugin %q: %w", key, err))
		}
	}
	return errors.Join(result...)
}

func (s *DesktopService) Bootstrap() AppSnapshot { return s.snapshot() }

func (s *DesktopService) Start() error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.RLock()
	resourceOperation := len(s.resourceBusy) > 0
	s.mu.RUnlock()
	if resourceOperation {
		return errors.New("资源正在安装或移除，请稍后再开始识别")
	}
	if err := s.job.Start(s.runtimeContext()); err != nil {
		if errors.Is(err, context.Canceled) {
			s.clearLastError()
		} else {
			s.setLastError(err)
		}
		return err
	}
	s.clearLastError()
	return nil
}

func (s *DesktopService) Pause() error {
	err := s.job.Pause()
	if err != nil {
		s.setLastError(err)
	}
	return err
}

func (s *DesktopService) Stop() error {
	err := s.job.Stop()
	if err != nil {
		s.setLastError(err)
	}
	return err
}

// SetLocked pins the console where it is. The window keeps receiving mouse
// events — the message stream still scrolls and the assistant still answers —
// so locking only takes away moving and resizing. Dragging is a frontend drag
// region the console drops while locked; resizing is the frontend edge handler
// Wails only arms for a resizable window.
func (s *DesktopService) SetLocked(locked bool) error {
	s.mu.Lock()
	window := s.consoleWindow
	s.locked = locked
	s.mu.Unlock()
	if window == nil {
		return errors.New("console window is not ready")
	}
	window.SetResizable(!locked)
	if !locked {
		window.Show().Focus()
	}
	s.publish()
	return nil
}

func (s *DesktopService) SetConfig(key string, value any) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if _, ok := editableConfigKeys[key]; !ok {
		return fmt.Errorf("configuration key %q is not editable", key)
	}
	if s.job.Snapshot().Status == job.Running && isRunLockedConfig(key) {
		return errors.New("正在识别，停止后才能修改这项设置")
	}
	normalized, err := normalizeEditableConfigValue(key, value)
	if err != nil {
		return err
	}
	switch key {
	case config.AudioSource:
		if _, exists := s.registry.AudioSource(normalized.(string)); !exists {
			return fmt.Errorf("audio source %q is not registered", normalized)
		}
	case config.RecognizerSource:
		if _, exists := s.registry.Recognizer(normalized.(string)); !exists {
			return fmt.Errorf("recognizer %q is not registered", normalized)
		}
	}
	if key == config.AudioSource {
		// Choosing one source through the legacy key replaces the whole input
		// list; leaving the list in place would silently override the choice.
		if err := s.store.SetMany(map[string]any{
			config.AudioSource:   normalized,
			config.AudioChannels: "",
		}); err != nil {
			s.setLastError(err)
			return err
		}
		return nil
	}
	if err := s.store.Set(key, normalized); err != nil {
		s.setLastError(err)
		return err
	}
	return nil
}

// SetAudioChannels replaces the audio inputs captured together, in the order
// they should appear on screen. Every input gets its own recognizer while a run
// is active, so a second one costs another copy of the model in memory and
// another decoder on the CPU.
func (s *DesktopService) SetAudioChannels(channels []AudioChannelInput) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.job.Snapshot().Status == job.Running {
		return errors.New("正在识别，停止后才能修改音频输入")
	}
	if len(channels) == 0 {
		return errors.New("至少要选择一个音频输入")
	}
	if len(channels) > config.MaxAudioChannels {
		return fmt.Errorf("最多同时录制 %d 路音频", config.MaxAudioChannels)
	}
	resolved := make([]config.Channel, 0, len(channels))
	seen := make(map[string]bool, len(channels))
	for _, channel := range channels {
		source := strings.TrimSpace(channel.Source)
		instance, exists := s.registry.AudioSource(source)
		if !exists {
			return fmt.Errorf("audio source %q is not registered", source)
		}
		name := instance.Metadata().Name
		if !instance.Available() {
			return fmt.Errorf("音频输入「%s」在当前系统上不可用", name)
		}
		if seen[source] {
			return fmt.Errorf("音频输入「%s」重复了，同一个来源只能录一路", name)
		}
		seen[source] = true
		resolved = append(resolved, config.Channel{
			Source: source,
			Label:  config.NormalizeChannelLabel(channel.Label),
		})
	}
	if len(resolved) > 1 {
		// A nameless line cannot be told from the line under it, so an input
		// that arrives unnamed — a configuration written before multi-input
		// capture, for one — takes the name it would have been offered.
		for index := range resolved {
			if resolved[index].Label == "" {
				resolved[index].Label = config.DefaultChannelLabel(resolved[index].Source)
			}
		}
	}
	encoded, err := config.EncodeAudioChannels(resolved)
	if err != nil {
		return fmt.Errorf("encode audio channels: %w", err)
	}
	// Keep the legacy single-source key on the first input so an older reader
	// of config.json still finds a usable value there.
	if err := s.store.SetMany(map[string]any{
		config.AudioChannels: encoded,
		config.AudioSource:   resolved[0].Source,
	}); err != nil {
		s.setLastError(err)
		return err
	}
	return nil
}

func (s *DesktopService) SetPluginConfig(key string, values map[string]any) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.job.Snapshot().Status == job.Running {
		return errors.New("正在识别，停止后才能修改插件设置")
	}
	instance, exists := s.registry.Get(key)
	if !exists {
		return fmt.Errorf("plugin %q is not registered", key)
	}
	var encoded string
	switch key {
	case config.PluginKey(config.AudioSourceWindowsModule, config.MicrophoneAudioSourceID):
		encoded = strings.TrimSpace(fmt.Sprint(values["device"]))
	case config.PluginKey(config.AudioSourceWindowsModule, config.ProcessAudioSourceID):
		encoded = strings.TrimSpace(fmt.Sprint(values["process"]))
	default:
		data, err := mergePluginConfig(s.store.String(config.PluginConfigKey(key)), values)
		if err != nil {
			return fmt.Errorf("encode plugin configuration: %w", err)
		}
		encoded = data
	}
	if err := instance.LoadConfig([]byte(encoded)); err != nil {
		return fmt.Errorf("invalid plugin configuration: %w", err)
	}
	if err := s.store.Set(config.PluginConfigKey(key), encoded); err != nil {
		s.setLastError(err)
		return err
	}
	return nil
}

func (s *DesktopService) OpenSettings() {
	s.mu.RLock()
	window := s.settingsWindow
	s.mu.RUnlock()
	if window != nil {
		// Refresh dynamic choices such as the current process list immediately
		// before the settings window becomes visible.
		s.publish()
		window.Show().Focus()
	}
}

// AskAssistant queues a typed question. It returns as soon as the request is
// accepted; the answer arrives through the assistant event.
func (s *DesktopService) AskAssistant(question string) error {
	if s.assistant == nil {
		return errors.New("AI 助手不可用")
	}
	return s.assistant.Ask(question)
}

// TestAssistant sends one throwaway request so the user can verify an endpoint
// before enabling the assistant.
func (s *DesktopService) TestAssistant() (string, error) {
	if s.assistant == nil {
		return "", errors.New("AI 助手不可用")
	}
	s.operations.Add(1)
	defer s.operations.Done()
	return s.assistant.Test(s.runtimeContext())
}

func (s *DesktopService) ClearAssistant() {
	if s.assistant == nil {
		return
	}
	s.assistant.Clear()
}

func (s *DesktopService) ShowConsole() {
	s.mu.RLock()
	window := s.consoleWindow
	s.mu.RUnlock()
	if window != nil {
		window.Show().Focus()
	}
}

// HideConsole takes the console off the screen without stopping recognition.
// The tray icon brings it back, which is why the tray is created before the
// window can ever be hidden.
func (s *DesktopService) HideConsole() {
	s.mu.RLock()
	window := s.consoleWindow
	s.mu.RUnlock()
	if window != nil {
		window.Hide()
	}
}

func (s *DesktopService) ResetConsoleWindow() error {
	s.mu.RLock()
	window := s.consoleWindow
	s.mu.RUnlock()
	if window == nil {
		return errors.New("console window is not ready")
	}
	window.SetPosition(100, 100)
	window.SetSize(defaultConsoleWidth, defaultConsoleHeight)
	if err := s.store.Set(config.GeneralMainWindowLocation, []int{100, 100, defaultConsoleWidth, defaultConsoleHeight}); err != nil {
		return err
	}
	window.Show().Focus()
	return nil
}

func (s *DesktopService) CopyHistory() error {
	snapshot := s.job.Snapshot()
	lines := make([]string, 0, len(snapshot.History))
	for _, entry := range snapshot.History {
		lines = append(lines, job.FormatTranscriptLine(entry))
	}
	s.mu.RLock()
	app := s.app
	s.mu.RUnlock()
	if app == nil || !app.Clipboard.SetText(strings.Join(lines, "\n")) {
		return errors.New("无法写入剪贴板")
	}
	return nil
}

func (s *DesktopService) RefreshResources() {
	baseContext := s.runtimeContext()
	ctx, cancel := context.WithCancel(baseContext)
	generation := s.resourceRefresh.Add(1)
	s.mu.Lock()
	if s.refreshCancel != nil {
		s.refreshCancel()
	}
	s.refreshCancel = cancel
	s.mu.Unlock()
	s.operations.Add(1)
	go func() {
		defer s.operations.Done()
		defer cancel()
		s.refreshResources(ctx, generation)
		s.mu.Lock()
		if s.resourceRefresh.Load() == generation {
			s.refreshCancel = nil
		}
		s.mu.Unlock()
	}()
}

func (s *DesktopService) InstallResource(id string) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.job.Snapshot().Status == job.Running {
		return errors.New("正在识别，停止后才能安装或更新资源")
	}
	s.mu.Lock()
	module, exists := s.remoteResources[id]
	if !exists {
		s.mu.Unlock()
		return fmt.Errorf("marketplace resource %q was not found", id)
	}
	if isLegacyDotNetPlugin(module) {
		s.mu.Unlock()
		return fmt.Errorf("resource %q is a legacy .NET DLL plugin and cannot be loaded by the Go application", id)
	}
	if s.resourceBusy[id] {
		s.mu.Unlock()
		return fmt.Errorf("resource %q already has an operation in progress", id)
	}
	s.mu.Unlock()
	local, installed, err := s.resources.Local(s.runtimeContext(), id)
	if err != nil {
		return fmt.Errorf("check installed resource %q: %w", id, err)
	}
	if installed && local.LocalInfo != nil && local.LocalInfo.Version >= module.Version {
		return fmt.Errorf("resource %q is already at version %d", id, local.LocalInfo.Version)
	}
	s.mu.Lock()
	if s.resourceBusy[id] {
		s.mu.Unlock()
		return fmt.Errorf("resource %q already has an operation in progress", id)
	}
	s.resourceBusy[id] = true
	s.markResourceLocked(id, true, 0, "")
	s.mu.Unlock()
	s.publish()

	ctx := s.runtimeContext()
	s.operations.Add(1)
	go func() {
		defer s.operations.Done()
		err := s.resources.Install(ctx, module, func(progress resource.Progress) {
			value := resourceProgress(progress)
			s.mu.Lock()
			s.markResourceLocked(id, true, value, "")
			s.mu.Unlock()
			s.publish()
		})
		if err != nil {
			s.mu.Lock()
			delete(s.resourceBusy, id)
			s.markResourceLocked(id, false, 0, err.Error())
			s.mu.Unlock()
			s.setLastError(err)
			return
		}
		s.mu.Lock()
		delete(s.resourceBusy, id)
		s.mu.Unlock()
		s.RefreshResources()
	}()
	return nil
}

func (s *DesktopService) RemoveResource(id string) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.job.Snapshot().Status == job.Running {
		return errors.New("正在识别，停止后才能移除资源")
	}
	s.mu.Lock()
	if s.resourceBusy[id] {
		s.mu.Unlock()
		return fmt.Errorf("resource %q already has an operation in progress", id)
	}
	s.resourceBusy[id] = true
	s.markResourceLocked(id, true, 0, "")
	s.mu.Unlock()
	s.publish()

	ctx := s.runtimeContext()
	s.operations.Add(1)
	go func() {
		defer s.operations.Done()
		err := s.resources.Remove(ctx, id)
		if err != nil {
			s.mu.Lock()
			delete(s.resourceBusy, id)
			s.markResourceLocked(id, false, 0, err.Error())
			s.mu.Unlock()
			s.setLastError(err)
			return
		}
		s.mu.Lock()
		delete(s.resourceBusy, id)
		s.mu.Unlock()
		s.RefreshResources()
	}()
	return nil
}

func (s *DesktopService) Quit() {
	s.mu.RLock()
	app := s.app
	s.mu.RUnlock()
	if app != nil {
		app.Quit()
	}
}

func (s *DesktopService) consoleBounds() (x, y, width, height int, ok bool) {
	position := s.store.IntSlice(config.GeneralMainWindowLocation)
	if len(position) < 2 {
		return 0, 0, defaultConsoleWidth, defaultConsoleHeight, false
	}
	width, height = defaultConsoleWidth, defaultConsoleHeight
	if len(position) >= 4 && position[2] >= minConsoleWidth && position[3] >= minConsoleHeight {
		width, height = position[2], position[3]
	}
	return position[0], position[1], width, height, true
}

func (s *DesktopService) scheduleConsolePersistence() {
	s.mu.Lock()
	if s.shuttingDown {
		s.mu.Unlock()
		return
	}
	s.consoleSaveGen++
	generation := s.consoleSaveGen
	if s.consoleSave != nil {
		s.consoleSave.Stop()
	}
	s.consoleSave = time.AfterFunc(250*time.Millisecond, func() {
		s.consoleSaveMu.Lock()
		defer s.consoleSaveMu.Unlock()
		s.mu.RLock()
		closing := s.shuttingDown
		s.mu.RUnlock()
		if !closing {
			s.persistConsoleBounds()
		}
		s.mu.Lock()
		if s.consoleSaveGen == generation {
			s.consoleSave = nil
		}
		s.mu.Unlock()
	})
	s.mu.Unlock()
}

func (s *DesktopService) persistConsoleBounds() {
	s.mu.RLock()
	window := s.consoleWindow
	s.mu.RUnlock()
	if window == nil {
		return
	}
	x, y := window.Position()
	width, height := window.Size()
	if err := s.store.Set(config.GeneralMainWindowLocation, []int{x, y, width, height}); err != nil {
		s.setLastError(err)
	}
}

func (s *DesktopService) snapshot() AppSnapshot {
	jobSnapshot := s.job.Snapshot()
	s.mu.RLock()
	locked := s.locked
	lastError := s.lastError
	resources := append([]ResourceItem{}, s.resourceItems...)
	s.mu.RUnlock()
	if jobSnapshot.LastError != "" {
		lastError = jobSnapshot.LastError
	}
	return AppSnapshot{
		Status:            jobSnapshot.Status,
		RunningSeconds:    jobSnapshot.RunningSeconds,
		Text:              jobSnapshot.Text,
		Channels:          jobSnapshot.Channels,
		Locked:            locked,
		History:           jobSnapshot.History,
		Config:            s.store.Snapshot(),
		AudioSources:      s.audioOptions(),
		Recognizers:       s.recognizerOptions(),
		Resources:         resources,
		PunctuationModels: punctuationModelOptions(resources),
		Assistant:         s.assistantState(),
		Version:           s.build.Version,
		Commit:            s.build.Commit,
		Platform:          runtime.GOOS + "/" + runtime.GOARCH,
		LastError:         lastError,
	}
}

func (s *DesktopService) audioOptions() []PluginOption {
	plugins := s.registry.AudioSources()
	keys := make([]string, 0, len(plugins))
	for key := range plugins {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	labels := make(map[string]string, len(keys))
	enabled := make(map[string]bool, len(keys))
	for _, channel := range config.AudioChannelList(s.store) {
		labels[channel.Source] = channel.Label
		enabled[channel.Source] = true
	}
	result := make([]PluginOption, 0, len(keys))
	for _, key := range keys {
		instance := plugins[key]
		metadata := instance.Metadata()
		label, captured := labels[key], enabled[key]
		if !captured {
			// Offer the name this input would be given, so enabling it does not
			// start with an empty box.
			label = config.DefaultChannelLabel(key)
		}
		option := PluginOption{
			Key: key, Name: metadata.Name, Description: metadata.Description,
			Available: instance.Available(), Enabled: captured, Label: label,
		}
		switch key {
		case config.PluginKey(config.AudioSourceWindowsModule, config.MicrophoneAudioSourceID):
			s.mu.RLock()
			devices := append([]audio.Device(nil), s.devices...)
			s.mu.RUnlock()
			choices := []ConfigFieldOption{{Value: "", Label: "默认通信设备"}}
			for _, device := range devices {
				label := device.Name
				if device.Default {
					label += "（默认）"
				}
				choices = append(choices, ConfigFieldOption{Value: device.ID, Label: label})
			}
			option.Fields = []ConfigField{{
				Key: "device", Label: "麦克风设备", Type: "select",
				Value: s.store.String(config.PluginConfigKey(key)), Options: choices,
			}}
		case config.PluginKey(config.AudioSourceWindowsModule, config.ProcessAudioSourceID):
			choices := []ConfigFieldOption{{Value: "", Label: "请选择正在运行的应用"}}
			if processes, err := processlist.List(context.Background()); err == nil {
				for _, process := range processes {
					choices = append(choices, ConfigFieldOption{
						Value: strconv.FormatUint(uint64(process.PID), 10),
						Label: fmt.Sprintf("%s（PID %d）", process.Executable, process.PID),
					})
				}
			}
			current := s.store.String(config.PluginConfigKey(key))
			if current != "" && !containsConfigOption(choices, current) {
				choices = append([]ConfigFieldOption{{Value: current, Label: "当前配置（PID " + current + "，可能已退出）"}}, choices...)
			}
			option.Fields = []ConfigField{
				{Key: "availability", Type: "message", Hint: "捕获所选进程及其子进程的音频；需要 Windows 10 build 20348 或更高版本。重新打开设置可刷新进程列表。"},
				{Key: "process", Label: "目标应用", Type: "select", Value: current, Options: choices},
			}
		}
		result = append(result, option)
	}
	return result
}

func (s *DesktopService) recognizerOptions() []PluginOption {
	plugins := s.registry.Recognizers()
	keys := make([]string, 0, len(plugins))
	for key := range plugins {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]PluginOption, 0, len(keys)+1)
	for _, key := range keys {
		instance := plugins[key]
		metadata := instance.Metadata()
		needsAudio := instance.NeedsAudio()
		option := PluginOption{
			Key: key, Name: metadata.Name, Description: metadata.Description,
			Available: instance.Available(), NeedsAudio: &needsAudio,
		}
		values := parseJSONObject(s.store.String(config.PluginConfigKey(key)))
		switch key {
		case config.PluginKey(config.CommandModule, config.CommandID):
			option.Fields = []ConfigField{
				{Key: "Command", Label: "程序路径", Type: "file", Value: stringValue(values, "Command")},
				{Key: "Arguments", Label: "命令参数", Type: "text", Value: stringValue(values, "Arguments")},
				{Key: "WorkingDirectory", Label: "工作目录", Type: "folder", Value: stringValue(values, "WorkingDirectory")},
				{Key: "LogFile", Label: "错误输出日志", Type: "file", Value: stringValue(values, "LogFile"), Hint: "留空仍会持续排空 stderr，但不会写入文件。"},
			}
		case config.PluginKey(config.SherpaOnnxModule, config.SherpaOnnxID):
			modelOptions := s.modelOptions(resource.ModuleTypeSherpaOnnxModel)
			fields := []ConfigField{{Key: "backend", Type: "message", Hint: "发布构建需要 sherpa-onnx 原生 DLL 与对应模型文件。"}}
			if len(modelOptions) > 0 {
				modelOptions = append([]ConfigFieldOption{{Value: "", Label: "自定义模型文件"}}, modelOptions...)
				fields = append(fields, ConfigField{
					Key: "model", Label: "模型资源", Type: "select", Value: stringValue(values, "model"), Options: modelOptions,
					Hint: "只有选「自定义模型文件」时下面四个路径才生效，否则会被所选资源里的模型覆盖。",
				})
			} else {
				fields = append(fields, ConfigField{Key: "model", Label: "模型资源 ID（留空使用下方文件）", Type: "text", Value: stringValue(values, "model")})
			}
			fields = append(fields,
				ConfigField{Key: "encoder", Label: "Encoder 模型", Type: "file", Value: stringValue(values, "encoder")},
				ConfigField{Key: "decoder", Label: "Decoder 模型", Type: "file", Value: stringValue(values, "decoder")},
				ConfigField{Key: "joiner", Label: "Joiner 模型", Type: "file", Value: stringValue(values, "joiner")},
				ConfigField{Key: "tokens", Label: "Tokens 文件", Type: "file", Value: stringValue(values, "tokens")},
				ConfigField{
					Key: "DecodingMethod", Label: "解码方式", Type: "select", Value: decodingMethodValue(values),
					Options: []ConfigFieldOption{
						{Value: "greedy_search", Label: "greedy_search（快，默认）"},
						{Value: "modified_beam_search", Label: "modified_beam_search（较准，可用热词）"},
					},
					Hint: "beam search 对英文和专有名词更稳，代价是更高的 CPU 占用；因为内置热词只在它下面生效，新装默认就是它。换成 greedy_search 更省 CPU，热词会停用但路径仍然保留，改回来即可恢复。",
				},
				ConfigField{
					Key: "RuleFsts", Label: "ITN 规则文件", Type: "file", Value: stringValue(values, "RuleFsts"),
					Hint: "把「二零二五」这类口语数字改写成 2025。KSpeech 自带 itn_zh_number.fst 并默认启用；多个文件用英文逗号分隔，留空则保持模型原始输出。",
				},
				ConfigField{
					Key: "HotwordsFile", Label: "热词文件", Type: "file", Value: stringValue(values, "HotwordsFile"),
					Hint: hotwordsHint(values),
				},
				ConfigField{
					Key: "HotwordsScore", Label: "热词权重", Type: "number", Value: hotwordsScoreValue(values),
					Hint: "越大越倾向于识别成热词，默认 1.5；调得过大会把发音相近的普通词也吃掉。",
				},
				ConfigField{
					Key: "MaxTextLength", Label: "整句最长字数", Type: "number", Value: maxTextLengthValue(values),
					Hint: "一句话说到这个长度就强制定稿并另起一句，默认 80，0 表示不限制。字幕窗小或字号大的时候调小一些，屏幕上留下的整句会更短。",
				},
			)
			option.Fields = fields
		case config.PluginKey(config.SherpaNcnnModule, config.SherpaNcnnID):
			fields := []ConfigField{{Key: "backend", Type: "message", Hint: "Windows 发布后端保留旧版 Vulkan 请求；GPU 或驱动不可用时 NCNN 使用 CPU。发布构建需携带 4 个 NCNN 运行库。"}}
			modelOptions := s.modelOptions(resource.ModuleTypeSherpaNcnnModel)
			if len(modelOptions) > 0 {
				modelOptions = append([]ConfigFieldOption{{Value: "", Label: "自定义模型文件"}}, modelOptions...)
				fields = append(fields, ConfigField{Key: "model", Label: "模型资源", Type: "select", Value: stringValue(values, "model"), Options: modelOptions})
			} else {
				fields = append(fields, ConfigField{Key: "model", Label: "模型资源 ID（留空使用下方文件）", Type: "text", Value: stringValue(values, "model")})
			}
			fields = append(fields,
				ConfigField{Key: "encoder_param", Label: "Encoder 参数", Type: "file", Value: stringValue(values, "encoder_param")},
				ConfigField{Key: "encoder_bin", Label: "Encoder 模型", Type: "file", Value: stringValue(values, "encoder_bin")},
				ConfigField{Key: "decoder_param", Label: "Decoder 参数", Type: "file", Value: stringValue(values, "decoder_param")},
				ConfigField{Key: "decoder_bin", Label: "Decoder 模型", Type: "file", Value: stringValue(values, "decoder_bin")},
				ConfigField{Key: "joiner_param", Label: "Joiner 参数", Type: "file", Value: stringValue(values, "joiner_param")},
				ConfigField{Key: "joiner_bin", Label: "Joiner 模型", Type: "file", Value: stringValue(values, "joiner_bin")},
				ConfigField{Key: "tokens", Label: "Tokens 文件", Type: "file", Value: stringValue(values, "tokens")},
				ConfigField{
					Key: "MaxTextLength", Label: "整句最长字数", Type: "number",
					Value: ncnnMaxTextLengthValue(values),
					Hint:  "一句话说到这个长度就强制定稿并另起一句，默认 80，0 表示不限制。",
				},
			)
			option.Fields = fields
		}
		result = append(result, option)
	}
	return result
}

// punctuationModelOptions lists the installed punctuation resources by the file
// the punctuation pass has to load, which is what the configuration stores.
func punctuationModelOptions(items []ResourceItem) []ConfigFieldOption {
	result := make([]ConfigFieldOption, 0)
	for _, item := range items {
		if item.Local && item.moduleType == resource.ModuleTypePunctuationModel && item.modelPath != "" {
			result = append(result, ConfigFieldOption{Value: item.modelPath, Label: item.Name})
		}
	}
	return result
}

func (s *DesktopService) modelOptions(moduleType string) []ConfigFieldOption {
	s.mu.RLock()
	items := append([]ResourceItem(nil), s.resourceItems...)
	s.mu.RUnlock()
	result := make([]ConfigFieldOption, 0)
	for _, item := range items {
		if item.Local && item.moduleType == moduleType {
			result = append(result, ConfigFieldOption{Value: item.ID, Label: item.Name})
		}
	}
	return result
}

func (s *DesktopService) publish() {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	s.mu.RLock()
	app := s.app
	s.mu.RUnlock()
	if app == nil {
		return
	}
	snapshot := s.snapshot()
	app.Event.Emit(stateEvent, snapshot)
}

func (s *DesktopService) publishLive(job.Snapshot) {
	const minimumInterval = time.Second / 30
	now := time.Now()
	s.mu.Lock()
	remaining := minimumInterval - now.Sub(s.lastLivePublish)
	if s.lastLivePublish.IsZero() || remaining <= 0 {
		s.lastLivePublish = now
		s.mu.Unlock()
		s.emitLiveCurrent()
		return
	}
	if s.livePublish == nil {
		s.livePublish = time.AfterFunc(remaining, func() {
			s.mu.Lock()
			s.livePublish = nil
			s.lastLivePublish = time.Now()
			s.mu.Unlock()
			s.emitLiveCurrent()
		})
	}
	s.mu.Unlock()
}

func (s *DesktopService) emitLiveCurrent() {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	s.mu.RLock()
	app := s.app
	s.mu.RUnlock()
	if app == nil {
		return
	}
	snapshot := s.job.LiveSnapshot()
	app.Event.Emit(liveStateEvent, LiveState{
		Status:         snapshot.Status,
		RunningSeconds: snapshot.RunningSeconds,
		Text:           snapshot.Text,
		Channels:       snapshot.Channels,
		LastError:      snapshot.LastError,
	})
}

// observeTranscript feeds newly finalized captions to the assistant. The job
// snapshot always carries the whole session history, so the last observed entry
// ID is what turns it into an incremental feed.
func (s *DesktopService) observeTranscript(snapshot job.Snapshot) {
	if s.assistant == nil {
		return
	}
	s.mu.Lock()
	var fresh []job.HistoryEntry
	for _, entry := range snapshot.History {
		if entry.ID > s.lastHistoryID {
			fresh = append(fresh, entry)
		}
	}
	if len(fresh) > 0 {
		s.lastHistoryID = fresh[len(fresh)-1].ID
	}
	wasRunning := s.jobRunning
	s.jobRunning = snapshot.Status == job.Running
	s.mu.Unlock()

	for _, entry := range fresh {
		s.assistant.Observe(entry.Speaker, entry.Text, entry.Time)
	}
	if wasRunning && snapshot.Status != job.Running {
		// Summarize the tail of the session instead of dropping it.
		s.assistant.Flush()
	}
}

func (s *DesktopService) assistantState() assistant.State {
	if s.assistant == nil {
		return assistant.State{
			Tools:         make([]string, 0),
			Insights:      make([]assistant.Insight, 0),
			Conversations: make([]assistant.Conversation, 0),
		}
	}
	return s.assistant.State()
}

func (s *DesktopService) publishAssistant(state assistant.State) {
	s.mu.RLock()
	app := s.app
	s.mu.RUnlock()
	if app == nil {
		return
	}
	app.Event.Emit(assistantEvent, state)
}

func (s *DesktopService) publishNotification(value job.Notification) {
	if value.Level != job.NotificationError && s.store.Int(config.NotificationType) == 0 {
		return
	}
	s.mu.RLock()
	app := s.app
	notifier := s.notifier
	s.mu.RUnlock()
	if notifier != nil {
		level := notifications.InterruptionLevelActive
		if value.Level == job.NotificationWarning {
			level = notifications.InterruptionLevelPassive
		}
		if err := notifier.SendNotification(notifications.NotificationOptions{
			ID:                fmt.Sprintf("kspeech-%d", notificationSequence.Add(1)),
			Title:             value.Title,
			Body:              value.Message,
			ThreadID:          "kspeech-recognition",
			InterruptionLevel: level,
		}); err != nil {
			s.mu.Lock()
			s.lastError = "发送系统通知失败：" + err.Error()
			s.mu.Unlock()
		}
	}
	if app != nil {
		app.Event.Emit(notificationEvent, UINotification{
			Title: value.Title, Message: value.Message, Level: string(value.Level),
		})
	}
}

var notificationSequence atomic.Uint64

func (s *DesktopService) refreshDevices(ctx context.Context) {
	devices, err := audio.Devices(ctx)
	if err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, audio.ErrNotSupported) {
			s.setLastError(fmt.Errorf("枚举麦克风失败: %w", err))
		}
		return
	}
	s.mu.Lock()
	s.devices = devices
	s.mu.Unlock()
	s.publish()
}

func (s *DesktopService) refreshResources(ctx context.Context, generation uint64) {
	items, err := s.resources.List(ctx)
	view := make([]ResourceItem, 0, len(items))
	remote := make(map[string]resource.ModuleInfo)
	for _, item := range items {
		info := item.EffectiveInfo()
		if info == nil {
			continue
		}
		if item.RemoteInfo != nil {
			remote[info.ID] = *item.RemoteInfo
		}
		name := info.Name
		if name == "" {
			name = info.ID
		}
		installable := !isLegacyDotNetPlugin(*info)
		itemError := ""
		if !installable {
			itemError = "旧 .NET DLL 插件无法由 Go 版加载"
		}
		modelPath := ""
		if item.IsLocal() && info.Type == resource.ModuleTypePunctuationModel &&
			item.LocalInfo != nil && item.LocalInfo.PunctuationPath != nil {
			resolved, resolveErr := resolveConfinedModelFile(item.LocalDir, item.LocalInfo.PunctuationPath.ModelPath)
			if resolveErr != nil {
				itemError = "标点模型文件不可用：" + resolveErr.Error()
			} else {
				modelPath = resolved
			}
		}
		view = append(view, ResourceItem{
			ID: info.ID, Name: name, Description: info.Desc, DisplayVersion: info.DisplayVersion,
			Local: item.IsLocal(), Removable: item.CanRemove, NeedsUpdate: item.NeedsUpdate(),
			Installable: installable, Error: itemError, moduleType: info.Type, modelPath: modelPath,
		})
	}
	if s.resourceRefresh.Load() != generation {
		return
	}
	s.mu.Lock()
	if s.resourceRefresh.Load() != generation {
		s.mu.Unlock()
		return
	}
	for index := range view {
		if s.resourceBusy[view[index].ID] {
			view[index].Busy = true
		}
	}
	s.resourceItems = view
	s.remoteResources = remote
	if err != nil && !errors.Is(err, context.Canceled) {
		s.lastError = "刷新资源列表失败：" + err.Error()
		if errors.Is(err, context.DeadlineExceeded) {
			s.lastError = "刷新资源列表失败：连不上模型市场。已安装的资源仍可使用；请检查网络后重试。"
		}
	}
	s.mu.Unlock()
	s.publish()
}

func isLegacyDotNetPlugin(info resource.ModuleInfo) bool {
	return info.Type == resource.ModuleTypePlugin || len(info.Assemblies) > 0
}

func (s *DesktopService) markResourceLocked(id string, busy bool, progress float64, message string) {
	for index := range s.resourceItems {
		if s.resourceItems[index].ID == id {
			s.resourceItems[index].Busy = busy
			s.resourceItems[index].Progress = progress
			s.resourceItems[index].Error = message
			return
		}
	}
}

func (s *DesktopService) runtimeContext() context.Context {
	s.mu.RLock()
	ctx := s.ctx
	s.mu.RUnlock()
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (s *DesktopService) setLastError(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	s.lastError = err.Error()
	s.mu.Unlock()
	s.publish()
}

func (s *DesktopService) reportResourceIssue(err error) {
	if err == nil {
		return
	}
	s.setLastError(fmt.Errorf("资源清单无效: %w", err))
}

func resolveConfinedModelFile(root, manifestPath string) (string, error) {
	manifestPath = strings.TrimSpace(manifestPath)
	if manifestPath == "" || filepath.IsAbs(manifestPath) || filepath.VolumeName(manifestPath) != "" {
		return "", errors.New("model path must be relative to its resource directory")
	}
	rootPath, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve resource directory: %w", err)
	}
	candidate, err := filepath.Abs(filepath.Join(rootPath, manifestPath))
	if err != nil {
		return "", fmt.Errorf("resolve model path: %w", err)
	}
	if !pathWithinRoot(rootPath, candidate) {
		return "", fmt.Errorf("model path %q escapes its resource directory", manifestPath)
	}
	realRoot, err := filepath.EvalSymlinks(rootPath)
	if err != nil {
		return "", fmt.Errorf("resolve resource directory links: %w", err)
	}
	realCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve model file links: %w", err)
	}
	if !pathWithinRoot(realRoot, realCandidate) {
		return "", fmt.Errorf("model path %q resolves outside its resource directory", manifestPath)
	}
	info, err := os.Stat(realCandidate)
	if err != nil {
		return "", fmt.Errorf("inspect model file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("model path is not a regular file")
	}
	return realCandidate, nil
}

func pathWithinRoot(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func (s *DesktopService) clearLastError() {
	s.mu.Lock()
	s.lastError = ""
	s.mu.Unlock()
	s.publish()
}

func resourceProgress(value resource.Progress) float64 {
	if value.Total > 0 {
		return float64(value.Completed) / float64(value.Total)
	}
	if value.TotalSteps > 0 {
		return float64(value.Step) / float64(value.TotalSteps)
	}
	return 0
}

func parseJSONObject(value string) map[string]any {
	result := make(map[string]any)
	if strings.TrimSpace(value) == "" {
		return result
	}
	_ = json.Unmarshal([]byte(value), &result)
	return result
}

func mergePluginConfig(existing string, patch map[string]any) (string, error) {
	merged := make(map[string]any)
	if strings.TrimSpace(existing) != "" {
		if err := json.Unmarshal([]byte(existing), &merged); err != nil {
			return "", fmt.Errorf("decode existing plugin configuration: %w", err)
		}
		if merged == nil {
			return "", errors.New("existing plugin configuration is not a JSON object")
		}
	}
	for field, value := range patch {
		merged[field] = value
	}
	data, err := json.Marshal(merged)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func formatConfigIssues(issues []config.Issue) string {
	if len(issues) == 0 {
		return ""
	}
	messages := make([]string, 0, len(issues))
	for _, issue := range issues {
		var message string
		switch issue.Code {
		case config.IssueUserConfigRecovered:
			message = "检测到损坏的用户配置，已恢复上一版本"
		case config.IssueUserConfigQuarantined:
			message = "检测到损坏的用户配置，已隔离原文件并使用默认设置"
		case config.IssueBackupConfigRecovered:
			message = "检测到上次配置替换中断，已恢复上一版本"
		case config.IssueBackupConfigQuarantined:
			message = "检测到损坏的配置备份，已隔离并忽略"
		default:
			message = issue.Message
		}
		if issue.Path != "" {
			message += "：" + issue.Path
		}
		messages = append(messages, message)
	}
	return strings.Join(messages, "；")
}

func stringValue(values map[string]any, key string) string {
	value, exists := values[key]
	if !exists || value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

// decodingMethodValue and hotwordsScoreValue fall back to the recognizer's own
// defaults so an unset field renders as its effective value instead of blank.
func decodingMethodValue(values map[string]any) string {
	method := strings.TrimSpace(stringValue(values, "DecodingMethod"))
	if method == "" {
		return sherpaonnx.DefaultConfig().DecodingMethod
	}
	return method
}

// hotwordsHint describes the word list, and leads with the reason it is doing
// nothing when the configured decoding method never consults it. The list is
// deliberately kept in that case, so the note explains how to bring it back
// instead of asking the user to type the path again.
func hotwordsHint(values map[string]any) string {
	const rules = "每行一个词，随包自带一份中英词表（地铁、AI、编程、科技）并默认启用。三条硬规则：不能写注释，# 开头的行会让识别进程直接退出；英文必须全大写；数字要写成中文（五号线），带阿拉伯数字或标点的行会被整行丢弃。行末加「 :3.0」可单独调权重，英文词还需要模型目录里有 bpe.vocab。"
	if strings.TrimSpace(stringValue(values, "HotwordsFile")) != "" &&
		!sherpaonnx.HotwordsApply(decodingMethodValue(values)) {
		return "当前解码方式是 greedy_search，热词不生效；这里的路径会一直保留，解码方式改回 modified_beam_search 就会重新启用。" + rules
	}
	return rules
}

func maxTextLengthValue(values map[string]any) int {
	return storedTextLength(values, sherpaonnx.DefaultConfig().MaxTextLength)
}

func ncnnMaxTextLengthValue(values map[string]any) int {
	return storedTextLength(values, sherpancnn.DefaultConfig().MaxTextLength)
}

func storedTextLength(values map[string]any, fallback int) int {
	raw := strings.TrimSpace(stringValue(values, "MaxTextLength"))
	if raw == "" {
		return fallback
	}
	length, err := strconv.Atoi(raw)
	if err != nil || length < 0 {
		return fallback
	}
	return length
}

func hotwordsScoreValue(values map[string]any) float64 {
	fallback := float64(sherpaonnx.DefaultConfig().HotwordsScore)
	raw := strings.TrimSpace(stringValue(values, "HotwordsScore"))
	if raw == "" {
		return fallback
	}
	score, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fallback
	}
	return score
}

func containsConfigOption(options []ConfigFieldOption, value string) bool {
	for _, option := range options {
		if fmt.Sprint(option.Value) == value {
			return true
		}
	}
	return false
}

func isRunLockedConfig(key string) bool {
	// ShownLockUsage only records that the lock hint has been shown. It is not
	// read by a running job, so locking the console during recognition must
	// still be able to persist it.
	if key == config.NotificationShownLockUsage {
		return false
	}
	// A run loads its punctuator once at start, so these follow the audio and
	// recognizer choices and only apply to the next run.
	return key == config.AudioSource || key == config.RecognizerSource ||
		strings.HasPrefix(key, "notification.") || strings.HasPrefix(key, "punctuation.")
}

var editableConfigKeys = map[string]struct{}{
	config.GeneralLanguage:            {},
	config.GeneralLaunchOnStartup:     {},
	config.GeneralStartOnLaunch:       {},
	config.GeneralAutoUpdate:          {},
	config.GeneralResultLogPath:       {},
	config.AppearanceShadowColor:      {},
	config.AppearanceShadowSize:       {},
	config.AppearanceFontFamily:       {},
	config.AppearanceFontSize:         {},
	config.AppearanceFontColor:        {},
	config.AppearanceMouseHover:       {},
	config.AppearanceTextAlign:        {},
	config.AppearanceBackgroundColor:  {},
	config.PunctuationMode:            {},
	config.PunctuationModelPath:       {},
	config.NotificationType:           {},
	config.NotificationSensitiveWords: {},
	config.NotificationShownLockUsage: {},
	config.AssistantEnabled:           {},
	config.AssistantEndpoint:          {},
	config.AssistantAPIKey:            {},
	config.AssistantModel:             {},
	config.AssistantSummarize:         {},
	config.AssistantSummaryInterval:   {},
	config.AssistantAutoAnswer:        {},
	config.AssistantTools:             {},
	config.AssistantContextSentences:  {},
	config.AssistantBackground:        {},
	config.AssistantTimeout:           {},
	config.AudioSource:                {},
	config.RecognizerSource:           {},
}

// The assistant reads its settings again for every request, so unlike the audio
// and recognizer choices these stay editable while recognition runs.
const maxAssistantBackgroundRunes = 4000

func normalizeEditableConfigValue(key string, value any) (any, error) {
	switch key {
	case config.GeneralLaunchOnStartup, config.GeneralStartOnLaunch, config.GeneralAutoUpdate,
		config.NotificationShownLockUsage, config.AssistantEnabled, config.AssistantSummarize,
		config.AssistantAutoAnswer, config.AssistantTools:
		if typed, ok := value.(bool); ok {
			return typed, nil
		}
		return nil, fmt.Errorf("configuration %q expects a boolean", key)

	case config.AssistantEndpoint:
		typed, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("configuration %q expects text", key)
		}
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return "", nil
		}
		// Reject an unusable address while the settings page can still show why.
		if _, err := assistant.CompletionURL(trimmed); err != nil {
			return nil, err
		}
		return trimmed, nil

	case config.AssistantBackground:
		typed, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("configuration %q expects text", key)
		}
		if len([]rune(typed)) > maxAssistantBackgroundRunes {
			return nil, fmt.Errorf("补充背景最多 %d 个字符", maxAssistantBackgroundRunes)
		}
		return typed, nil

	case config.AssistantSummaryInterval:
		return rangedConfigInteger(key, value,
			int64(assistant.MinSummaryInterval/time.Second), int64(assistant.MaxSummaryInterval/time.Second))
	case config.AssistantContextSentences:
		return rangedConfigInteger(key, value, assistant.MinContext, assistant.MaxContext)
	case config.AssistantTimeout:
		return rangedConfigInteger(key, value,
			int64(assistant.MinTimeout/time.Second), int64(assistant.MaxTimeout/time.Second))

	case config.PunctuationMode:
		typed, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("configuration %q expects text", key)
		}
		mode, err := punctuation.ParseMode(typed)
		if err != nil {
			return nil, err
		}
		if mode == punctuation.ModeModel && !punctuation.ModelAvailable() {
			return nil, errors.New("当前构建不含 sherpa-onnx 原生后端，无法使用标点模型")
		}
		return string(mode), nil

	case config.GeneralLanguage, config.GeneralResultLogPath, config.AppearanceFontFamily,
		config.NotificationSensitiveWords, config.AssistantAPIKey, config.AssistantModel,
		config.AudioSource, config.RecognizerSource, config.PunctuationModelPath:
		typed, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("configuration %q expects text", key)
		}
		if key == config.GeneralLanguage && strings.TrimSpace(typed) == "" {
			return nil, fmt.Errorf("configuration %q must not be empty", key)
		}
		if len(typed) > 1<<20 {
			return nil, fmt.Errorf("configuration %q is too large", key)
		}
		return typed, nil

	case config.AppearanceShadowColor, config.AppearanceFontColor,
		config.AppearanceMouseHover, config.AppearanceBackgroundColor:
		integer, err := configInteger(value)
		if err != nil || integer < 0 || integer > math.MaxUint32 {
			return nil, fmt.Errorf("configuration %q expects an ARGB value from 0 to %d", key, uint64(math.MaxUint32))
		}
		return uint32(integer), nil

	case config.AppearanceShadowSize:
		return rangedConfigInteger(key, value, 0, 40)
	case config.AppearanceFontSize:
		return rangedConfigInteger(key, value, 12, 160)
	case config.AppearanceTextAlign:
		return rangedConfigInteger(key, value, 0, 3)
	case config.NotificationType:
		return rangedConfigInteger(key, value, 0, 1)
	default:
		return nil, fmt.Errorf("configuration key %q is not editable", key)
	}
}

func rangedConfigInteger(key string, value any, minimum, maximum int64) (int, error) {
	integer, err := configInteger(value)
	if err != nil || integer < minimum || integer > maximum {
		return 0, fmt.Errorf("configuration %q expects an integer from %d to %d", key, minimum, maximum)
	}
	return int(integer), nil
}

func configInteger(value any) (int64, error) {
	switch typed := value.(type) {
	case int:
		return int64(typed), nil
	case int32:
		return int64(typed), nil
	case int64:
		return typed, nil
	case uint32:
		return int64(typed), nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || math.Trunc(typed) != typed || typed < math.MinInt64 || typed > math.MaxInt64 {
			return 0, errors.New("not an integer")
		}
		return int64(typed), nil
	case json.Number:
		return strconv.ParseInt(typed.String(), 10, 64)
	default:
		return 0, errors.New("not an integer")
	}
}
