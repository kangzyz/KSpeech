package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kangzyz/KSpeech/internal/assistant"
	"github.com/kangzyz/KSpeech/internal/config"
	"github.com/kangzyz/KSpeech/internal/job"
	"github.com/kangzyz/KSpeech/internal/plugin"
	"github.com/kangzyz/KSpeech/internal/punctuation"
	"github.com/kangzyz/KSpeech/internal/recognizer/sherpancnn"
	"github.com/kangzyz/KSpeech/internal/recognizer/sherpaonnx"
	"github.com/kangzyz/KSpeech/internal/resource"
)

func TestNormalizeEditableConfigValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		key     string
		value   any
		want    any
		wantErr bool
	}{
		{name: "boolean", key: config.GeneralStartOnLaunch, value: true, want: true},
		{name: "reject coerced boolean", key: config.GeneralStartOnLaunch, value: "true", wantErr: true},
		{name: "font size", key: config.AppearanceFontSize, value: float64(48), want: 48},
		{name: "json integer", key: config.AppearanceTextAlign, value: json.Number("2"), want: 2},
		{name: "reject fractional", key: config.AppearanceFontSize, value: 12.5, wantErr: true},
		{name: "reject range", key: config.AppearanceShadowSize, value: 41, wantErr: true},
		{name: "argb", key: config.AppearanceFontColor, value: float64(math.MaxUint32), want: uint32(math.MaxUint32)},
		{name: "reject oversized argb", key: config.AppearanceFontColor, value: float64(math.MaxUint32) + 1, wantErr: true},
		{name: "source", key: config.AudioSource, value: "source-key", want: "source-key"},
		{name: "reject object", key: config.AudioSource, value: map[string]any{"bad": true}, wantErr: true},
		{name: "assistant switch", key: config.AssistantEnabled, value: true, want: true},
		{name: "assistant endpoint", key: config.AssistantEndpoint, value: " https://api.deepseek.com/v1 ", want: "https://api.deepseek.com/v1"},
		{name: "assistant endpoint cleared", key: config.AssistantEndpoint, value: "  ", want: ""},
		{name: "reject cleartext endpoint", key: config.AssistantEndpoint, value: "http://api.deepseek.com/v1", wantErr: true},
		{name: "reject malformed endpoint", key: config.AssistantEndpoint, value: "api.deepseek.com", wantErr: true},
		{name: "assistant interval", key: config.AssistantSummaryInterval, value: float64(120), want: 120},
		{name: "reject short interval", key: config.AssistantSummaryInterval, value: 5, wantErr: true},
		{name: "reject wide context", key: config.AssistantContextSentences, value: 500, wantErr: true},
		{name: "punctuation mode", key: config.PunctuationMode, value: " Rules ", want: "rules"},
		{name: "reject unknown punctuation mode", key: config.PunctuationMode, value: "commas-everywhere", wantErr: true},
		{name: "punctuation model path", key: config.PunctuationModelPath, value: `D:\models\punct\model.onnx`, want: `D:\models\punct\model.onnx`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeEditableConfigValue(test.key, test.value)
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, test.wantErr)
			}
			if !test.wantErr && got != test.want {
				t.Fatalf("value = %#v, want %#v", got, test.want)
			}
		})
	}
}

// The model mode needs the native sherpa-onnx backend, so a stub build refuses
// to store it instead of letting the next Start fall back silently.
func TestPunctuationModelModeFollowsTheBuild(t *testing.T) {
	t.Parallel()
	got, err := normalizeEditableConfigValue(config.PunctuationMode, "model")
	if punctuation.ModelAvailable() {
		if err != nil || got != "model" {
			t.Fatalf("value = %#v, error = %v, want %q", got, err, "model")
		}
		return
	}
	if err == nil {
		t.Fatalf("value = %#v, want a rejection without the native backend", got)
	}
}

func TestMergePluginConfigPreservesHiddenFields(t *testing.T) {
	merged, err := mergePluginConfig(
		`{"model":"old","NumThreads":6,"Provider":"cuda","AudioQueueCapacity":64}`,
		map[string]any{"model": "new", "encoder": "encoder.onnx"},
	)
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err := json.Unmarshal([]byte(merged), &values); err != nil {
		t.Fatal(err)
	}
	if values["model"] != "new" || values["encoder"] != "encoder.onnx" {
		t.Fatalf("visible fields were not updated: %#v", values)
	}
	if values["NumThreads"] != float64(6) || values["Provider"] != "cuda" || values["AudioQueueCapacity"] != float64(64) {
		t.Fatalf("hidden tuning fields were lost: %#v", values)
	}
	if _, err := mergePluginConfig(`{"NumThreads":`, map[string]any{"model": "new"}); err == nil {
		t.Fatal("malformed existing plugin configuration was silently replaced")
	}
}

func TestNcnnMissingResourceModelRemainsEditable(t *testing.T) {
	store, err := config.Open(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	key := config.PluginKey(config.SherpaNcnnModule, config.SherpaNcnnID)
	if err := store.Set(config.PluginConfigKey(key), `{"model":"missing-resource"}`); err != nil {
		t.Fatal(err)
	}
	registry := plugin.NewRegistry()
	if err := registry.Register(key, sherpancnn.New(plugin.Metadata{ID: config.SherpaNcnnID, Name: "NCNN"})); err != nil {
		t.Fatal(err)
	}
	service := &DesktopService{store: store, registry: registry, resourceItems: []ResourceItem{}}
	options := service.recognizerOptions()
	if len(options) != 1 {
		t.Fatalf("recognizer options = %#v", options)
	}
	for _, field := range options[0].Fields {
		if field.Key == "model" {
			if field.Type != "text" || field.Value != "missing-resource" {
				t.Fatalf("model field = %#v", field)
			}
			return
		}
	}
	t.Fatal("missing model field")
}

// stubAudioSource stands in for a Windows capture endpoint so the settings
// write path can be exercised on any platform.
type stubAudioSource struct {
	metadata  plugin.Metadata
	available bool
}

func (s *stubAudioSource) Metadata() plugin.Metadata          { return s.metadata }
func (s *stubAudioSource) Available() bool                    { return s.available }
func (s *stubAudioSource) LoadConfig([]byte) error            { return nil }
func (s *stubAudioSource) Init(context.Context) error         { return nil }
func (s *stubAudioSource) Close() error                       { return nil }
func (s *stubAudioSource) SetCallbacks(plugin.AudioCallbacks) {}
func (s *stubAudioSource) Start(context.Context) error        { return nil }
func (s *stubAudioSource) Stop() error                        { return nil }

func audioChannelService(t *testing.T) (*DesktopService, *config.Store) {
	t.Helper()
	store, err := config.Open(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	registry := plugin.NewRegistry()
	sources := map[string]*stubAudioSource{
		"microphone": {metadata: plugin.Metadata{Name: "麦克风"}, available: true},
		"loopback":   {metadata: plugin.Metadata{Name: "系统声音"}, available: true},
		"process":    {metadata: plugin.Metadata{Name: "指定进程"}, available: false},
	}
	for key, source := range sources {
		if err := registry.Register(key, source); err != nil {
			t.Fatal(err)
		}
	}
	return &DesktopService{store: store, registry: registry, job: job.New(store, registry)}, store
}

func TestSetAudioChannelsStoresLabelsAndKeepsTheLegacyKey(t *testing.T) {
	service, store := audioChannelService(t)
	err := service.SetAudioChannels([]AudioChannelInput{
		{Source: "microphone", Label: "  我 "},
		{Source: "loopback", Label: "其他人"},
	})
	if err != nil {
		t.Fatal(err)
	}
	channels := config.AudioChannelList(store)
	if len(channels) != 2 || channels[0] != (config.Channel{Source: "microphone", Label: "我"}) {
		t.Fatalf("channels = %#v", channels)
	}
	// A reader that only knows the old key must still find the first input.
	if got := store.String(config.AudioSource); got != "microphone" {
		t.Fatalf("legacy audio source = %q, want the first input", got)
	}

	// An input that joins without a name would otherwise be indistinguishable
	// from the line under it.
	loopback := config.PluginKey(config.AudioSourceWindowsModule, config.LoopbackAudioSourceID)
	if err := service.registry.Register(loopback, &stubAudioSource{
		metadata: plugin.Metadata{Name: "系统声音"}, available: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.SetAudioChannels([]AudioChannelInput{
		{Source: "microphone", Label: "我"},
		{Source: loopback, Label: ""},
	}); err != nil {
		t.Fatal(err)
	}
	if got := config.AudioChannelList(store); len(got) != 2 || got[1].Label != "其他人" {
		t.Fatalf("channels = %#v, want the unnamed input to take its default name", got)
	}

	// Choosing a single source through the legacy key drops the list, otherwise
	// the list would silently override that choice.
	if err := service.SetConfig(config.AudioSource, "loopback"); err != nil {
		t.Fatal(err)
	}
	if got := config.AudioChannelList(store); len(got) != 1 || got[0].Source != "loopback" {
		t.Fatalf("channels after a legacy write = %#v", got)
	}
}

func TestSetAudioChannelsRejectsUnusableInputs(t *testing.T) {
	service, _ := audioChannelService(t)
	if err := service.SetAudioChannels(nil); err == nil {
		t.Fatal("an empty input list was accepted")
	}
	if err := service.SetAudioChannels([]AudioChannelInput{
		{Source: "microphone"}, {Source: "microphone"},
	}); err == nil {
		t.Fatal("the same source was accepted twice")
	}
	if err := service.SetAudioChannels([]AudioChannelInput{{Source: "process"}}); err == nil {
		t.Fatal("an unavailable source was accepted")
	}
	if err := service.SetAudioChannels([]AudioChannelInput{{Source: "missing"}}); err == nil {
		t.Fatal("an unregistered source was accepted")
	}
}

// The settings page offers every input with the name it would be given, and
// marks the ones that are actually captured.
func TestAudioOptionsReportCaptureAndLabels(t *testing.T) {
	service, _ := audioChannelService(t)
	if err := service.SetAudioChannels([]AudioChannelInput{{Source: "microphone", Label: "我"}}); err != nil {
		t.Fatal(err)
	}
	options := make(map[string]PluginOption)
	for _, option := range service.audioOptions() {
		options[option.Key] = option
	}
	if got := options["microphone"]; !got.Enabled || got.Label != "我" {
		t.Fatalf("microphone option = %#v, want it captured as 我", got)
	}
	if got := options["loopback"]; got.Enabled {
		t.Fatalf("loopback option = %#v, want it left out", got)
	}
}

func TestSeedBuiltinRecognizerAssets(t *testing.T) {
	executableDir := t.TempDir()
	for _, name := range []string{builtinHotwordsName, builtinRuleFstName} {
		if err := os.WriteFile(filepath.Join(executableDir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store, err := config.Open(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	service := &DesktopService{store: store}
	key := config.PluginConfigKey(config.PluginKey(config.SherpaOnnxModule, config.SherpaOnnxID))
	recognizerConfig := func() map[string]any { return parseJSONObject(store.String(key)) }

	if err := service.seedBuiltinRecognizerAssets(executableDir); err != nil {
		t.Fatal(err)
	}
	values := recognizerConfig()
	if got := stringValue(values, "HotwordsFile"); got != filepath.Join(executableDir, builtinHotwordsName) {
		t.Fatalf("HotwordsFile = %q, want the shipped list", got)
	}
	if got := stringValue(values, "RuleFsts"); got != filepath.Join(executableDir, builtinRuleFstName) {
		t.Fatalf("RuleFsts = %q, want the shipped rule file", got)
	}
	// Hotwords are ignored outside beam search, so both must be set together.
	if got := stringValue(values, "DecodingMethod"); got != "modified_beam_search" {
		t.Fatalf("DecodingMethod = %q, want modified_beam_search", got)
	}

	// Clearing the list is a decision, not a defect: seeding must not undo it.
	cleared, err := mergePluginConfig(store.String(key), map[string]any{
		"HotwordsFile": "", "DecodingMethod": "greedy_search",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(key, cleared); err != nil {
		t.Fatal(err)
	}
	if err := service.seedBuiltinRecognizerAssets(executableDir); err != nil {
		t.Fatal(err)
	}
	if got := stringValue(recognizerConfig(), "HotwordsFile"); got != "" {
		t.Fatalf("HotwordsFile = %q, want it to stay cleared", got)
	}

	// Reinstalling elsewhere leaves an absolute path behind that no longer
	// resolves; the shipped file at the new location replaces it.
	moved := t.TempDir()
	for _, name := range []string{builtinHotwordsName, builtinRuleFstName} {
		if err := os.WriteFile(filepath.Join(moved, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(executableDir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.seedBuiltinRecognizerAssets(moved); err != nil {
		t.Fatal(err)
	}
	if got := stringValue(recognizerConfig(), "RuleFsts"); got != filepath.Join(moved, builtinRuleFstName) {
		t.Fatalf("RuleFsts = %q, want the relocated rule file", got)
	}
}

// A build that ships no hotwords list must clear the stale path instead of
// letting the next run fail on a missing file.
func TestSeedBuiltinRecognizerAssetsClearsVanishedFiles(t *testing.T) {
	store, err := config.Open(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	key := config.PluginConfigKey(config.PluginKey(config.SherpaOnnxModule, config.SherpaOnnxID))
	stale := filepath.Join(t.TempDir(), builtinHotwordsName)
	if err := store.Set(key, `{"BuiltinAssets":1,"HotwordsFile":"`+
		strings.ReplaceAll(stale, `\`, `\\`)+`","DecodingMethod":"modified_beam_search"}`); err != nil {
		t.Fatal(err)
	}
	service := &DesktopService{store: store}
	if err := service.seedBuiltinRecognizerAssets(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	values := parseJSONObject(store.String(key))
	if got := stringValue(values, "HotwordsFile"); got != "" {
		t.Fatalf("HotwordsFile = %q, want it cleared", got)
	}
	if got := stringValue(values, "DecodingMethod"); got != "greedy_search" {
		t.Fatalf("DecodingMethod = %q, want the cheaper default back", got)
	}
}

// The shipped word list is seeded into every fresh installation, so rejecting
// hotwords outside beam search made the decoding method a dropdown nobody could
// change: every attempt came back as a failed save.
func TestSetPluginConfigSwitchesDecodingMethodWithHotwordsConfigured(t *testing.T) {
	store, err := config.Open(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	key := config.PluginKey(config.SherpaOnnxModule, config.SherpaOnnxID)
	registry := plugin.NewRegistry()
	if err := registry.Register(key, sherpaonnx.New(plugin.Metadata{ID: config.SherpaOnnxID, Name: "Sherpa ONNX"})); err != nil {
		t.Fatal(err)
	}
	hotwords := filepath.Join(t.TempDir(), builtinHotwordsName)
	service := &DesktopService{store: store, registry: registry, job: job.New(store, registry), resourceItems: []ResourceItem{}}
	if err := service.SetPluginConfig(key, map[string]any{
		"HotwordsFile": hotwords, "DecodingMethod": "modified_beam_search",
	}); err != nil {
		t.Fatal(err)
	}

	if err := service.SetPluginConfig(key, map[string]any{
		"HotwordsFile": hotwords, "DecodingMethod": "greedy_search",
	}); err != nil {
		t.Fatalf("switching to greedy search failed: %v", err)
	}
	values := parseJSONObject(store.String(config.PluginConfigKey(key)))
	// Keeping the path is what makes the switch reversible without retyping it.
	if got := stringValue(values, "HotwordsFile"); got != hotwords {
		t.Fatalf("HotwordsFile = %q, want the list kept for the way back", got)
	}
	if !strings.Contains(hotwordsHint(values), "热词不生效") {
		t.Fatalf("hotwords hint = %q, want it to say the list is unused", hotwordsHint(values))
	}

	if err := service.SetPluginConfig(key, map[string]any{
		"HotwordsFile": hotwords, "DecodingMethod": "modified_beam_search",
	}); err != nil {
		t.Fatal(err)
	}
	if got := hotwordsHint(parseJSONObject(store.String(config.PluginConfigKey(key)))); strings.Contains(got, "热词不生效") {
		t.Fatalf("hotwords hint = %q, want the warning gone under beam search", got)
	}
}

func TestFormatConfigIssuesReportsRecoveryPath(t *testing.T) {
	got := formatConfigIssues([]config.Issue{{
		Code: config.IssueUserConfigRecovered,
		Path: `C:\Users\test\AppData\Roaming\KSpeech\config.json.corrupt-1`,
	}})
	if !strings.Contains(got, "已恢复上一版本") || !strings.Contains(got, "config.json.corrupt-1") {
		t.Fatalf("formatConfigIssues() = %q", got)
	}
}

func TestResolveConfinedModelFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	modelDir := filepath.Join(root, "models")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	modelPath := filepath.Join(modelDir, "encoder.onnx")
	if err := os.WriteFile(modelPath, []byte("model"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := resolveConfinedModelFile(root, filepath.Join("models", "encoder.onnx"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(modelPath)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("resolved path = %q, want %q", got, want)
	}
	if _, err := resolveConfinedModelFile(root, filepath.Join("..", "outside.onnx")); err == nil {
		t.Fatal("traversal path was accepted")
	}
	if _, err := resolveConfinedModelFile(root, modelDir); err == nil {
		t.Fatal("absolute path was accepted")
	}
}

func TestDiscoverLegacyNcnnModelFilesRequiresOneCompleteLayout(t *testing.T) {
	root := t.TempDir()
	modelDir := filepath.Join(root, "legacy-model")
	if err := os.Mkdir(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"encoder.param", "encoder.bin", "decoder.param", "decoder.bin", "joiner.param", "joiner.bin", "tokens.txt"} {
		if err := os.WriteFile(filepath.Join(modelDir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files, err := discoverLegacyNcnnModelFiles(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if files.EncoderParam != filepath.Join(modelDir, "encoder.param") || files.Tokens != filepath.Join(modelDir, "tokens.txt") {
		t.Fatalf("discovered files = %#v", files)
	}

	second := filepath.Join(root, "ambiguous")
	if err := os.Mkdir(second, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"encoder.param", "encoder.bin", "decoder.param", "decoder.bin", "joiner.param", "joiner.bin", "tokens.txt"} {
		if err := os.WriteFile(filepath.Join(second, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := discoverLegacyNcnnModelFiles(context.Background(), root); err == nil || !strings.Contains(err.Error(), "found 2") {
		t.Fatalf("ambiguous discovery error = %v", err)
	}
}

func TestConsoleBoundsRestorePositionAndSize(t *testing.T) {
	store, err := config.Open(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	service := &DesktopService{store: store}
	if x, y, width, height, ok := service.consoleBounds(); ok || x != 0 || y != 0 ||
		width != defaultConsoleWidth || height != defaultConsoleHeight {
		t.Fatalf("default bounds = (%d,%d %dx%d, %v)", x, y, width, height, ok)
	}
	if err := store.Set(config.GeneralMainWindowLocation, []int{11, 22, 900, 410}); err != nil {
		t.Fatal(err)
	}
	if x, y, width, height, ok := service.consoleBounds(); !ok || x != 11 || y != 22 || width != 900 || height != 410 {
		t.Fatalf("stored bounds = (%d,%d %dx%d, %v)", x, y, width, height, ok)
	}

	// A size left behind by the single-line caption window cannot hold three
	// panes, so the position is kept and the size falls back to the default.
	if err := store.Set(config.GeneralMainWindowLocation, []int{11, 22, 800, 180}); err != nil {
		t.Fatal(err)
	}
	if x, y, width, height, ok := service.consoleBounds(); !ok || x != 11 || y != 22 ||
		width != defaultConsoleWidth || height != defaultConsoleHeight {
		t.Fatalf("legacy caption bounds = (%d,%d %dx%d, %v)", x, y, width, height, ok)
	}
}

func TestSnapshotUsesEmptyArrays(t *testing.T) {
	t.Parallel()
	store, err := config.Open(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	registry := plugin.NewRegistry()
	service := &DesktopService{
		store:         store,
		registry:      registry,
		job:           job.New(store, registry),
		resourceItems: make([]ResourceItem, 0),
	}
	data, err := json.Marshal(service.snapshot())
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"history":null`, `"resources":null`, `"insights":null`, `"conversations":null`} {
		if strings.Contains(string(data), field) {
			t.Fatalf("snapshot contains %s: %s", field, data)
		}
	}
}

func TestObserveTranscriptFeedsTheAssistantOnce(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"choices":[{"message":{"content":"由张三负责。"}}]}`))
	}))
	defer server.Close()

	store, err := config.Open(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]any{
		config.AssistantEnabled:   true,
		config.AssistantEndpoint:  server.URL + "/v1",
		config.AssistantModel:     "test-model",
		config.AssistantSummarize: false,
	} {
		if err := store.Set(key, value); err != nil {
			t.Fatal(err)
		}
	}
	service := &DesktopService{store: store, assistant: assistant.New(store)}
	defer service.assistant.Close()

	now := time.Now()
	first := job.Snapshot{Status: job.Running, History: []job.HistoryEntry{
		{ID: 1, Time: now, Text: "这块进度谁跟？"},
	}}
	service.observeTranscript(first)
	deadline := time.Now().Add(3 * time.Second)
	for requests.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the detected question never reached the endpoint")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The job snapshot repeats the whole history, so a replay must not ask again.
	replay := job.Snapshot{Status: job.Running, History: []job.HistoryEntry{
		{ID: 1, Time: now, Text: "这块进度谁跟？"},
		{ID: 2, Time: now, Text: "好的，我记一下"},
	}}
	service.observeTranscript(replay)
	time.Sleep(50 * time.Millisecond)
	if got := requests.Load(); got != 1 {
		t.Fatalf("endpoint received %d requests, want 1", got)
	}
	if state := service.assistantState(); len(state.Conversations) != 1 {
		t.Fatalf("conversations = %#v", state.Conversations)
	}
}

func TestInstallResourceRejectsLegacyDotNetPlugin(t *testing.T) {
	store, err := config.Open(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	registry := plugin.NewRegistry()
	service := &DesktopService{
		store:           store,
		registry:        registry,
		job:             job.New(store, registry),
		remoteResources: map[string]resource.ModuleInfo{"legacy": {ID: "legacy", Type: resource.ModuleTypePlugin}},
		resourceBusy:    make(map[string]bool),
	}
	if err := service.InstallResource("legacy"); err == nil || !strings.Contains(err.Error(), ".NET DLL") {
		t.Fatalf("InstallResource() error = %v, want legacy plugin rejection", err)
	}
}

func TestOlderResourceRefreshCannotOverwriteNewerState(t *testing.T) {
	t.Parallel()
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"version":1,"modules":[]}`))
	}))
	defer server.Close()

	executableDir := t.TempDir()
	userDataDir := t.TempDir()
	manager, err := resource.NewManager(resource.Options{
		ExecutableDir:      executableDir,
		UserDataDir:        userDataDir,
		MarketplaceURL:     server.URL,
		MarketplaceTimeout: 5 * time.Second,
		AllowInsecureHTTP:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	service := &DesktopService{
		resources:       manager,
		resourceItems:   make([]ResourceItem, 0),
		remoteResources: make(map[string]resource.ModuleInfo),
		resourceBusy:    make(map[string]bool),
	}

	firstGeneration := service.resourceRefresh.Add(1)
	firstDone := make(chan struct{})
	go func() {
		service.refreshResources(context.Background(), firstGeneration)
		close(firstDone)
	}()
	<-firstStarted

	moduleDir := filepath.Join(userDataDir, resource.PluginDirName, "model")
	if err := os.MkdirAll(moduleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"model","version":1,"name":"Model","type":"sherpaonnx_model"}`
	if err := os.WriteFile(filepath.Join(moduleDir, resource.ModuleJSONName), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	secondGeneration := service.resourceRefresh.Add(1)
	service.refreshResources(context.Background(), secondGeneration)
	close(releaseFirst)
	<-firstDone

	service.mu.RLock()
	defer service.mu.RUnlock()
	if len(service.resourceItems) != 1 || service.resourceItems[0].ID != "model" || !service.resourceItems[0].Local {
		t.Fatalf("resource state was overwritten: %#v", service.resourceItems)
	}
}
