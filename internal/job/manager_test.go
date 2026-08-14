package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kangzyz/KSpeech/internal/config"
	"github.com/kangzyz/KSpeech/internal/plugin"
)

type fakeSettings map[string]string

func (s fakeSettings) String(key string) string { return s[key] }

type fakeRecognizer struct {
	metadata          plugin.Metadata
	needsAudio        bool
	callbacks         plugin.RecognizerCallbacks
	started           bool
	feedCount         int
	mu                sync.Mutex
	startGate         <-chan struct{}
	startSeen         chan<- struct{}
	available         *bool
	ignoreStartCancel bool
	errorOnStart      error
	warningOnStart    error
	stopGate          <-chan struct{}
	stopSeen          chan<- struct{}
	stopCalls         int
	finalOnStop       string
	feedErr           error
	closed            bool
}

func (f *fakeRecognizer) Metadata() plugin.Metadata { return f.metadata }
func (f *fakeRecognizer) Available() bool {
	return f.available == nil || *f.available
}
func (f *fakeRecognizer) LoadConfig([]byte) error    { return nil }
func (f *fakeRecognizer) Init(context.Context) error { return nil }
func (f *fakeRecognizer) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return f.Stop()
}
func (f *fakeRecognizer) NeedsAudio() bool                              { return f.needsAudio }
func (f *fakeRecognizer) SetCallbacks(value plugin.RecognizerCallbacks) { f.callbacks = value }
func (f *fakeRecognizer) Start(ctx context.Context) error {
	if f.startSeen != nil {
		f.startSeen <- struct{}{}
	}
	if f.startGate != nil {
		if f.ignoreStartCancel {
			<-f.startGate
			f.started = true
			return nil
		}
		select {
		case <-f.startGate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.started = true
	if f.warningOnStart != nil && f.callbacks.Warning != nil {
		f.callbacks.Warning(f.warningOnStart)
	}
	if f.errorOnStart != nil && f.callbacks.Error != nil {
		f.callbacks.Error(f.errorOnStart)
	}
	return nil
}
func (f *fakeRecognizer) Stop() error {
	f.mu.Lock()
	f.stopCalls++
	stopGate, stopSeen, finalOnStop := f.stopGate, f.stopSeen, f.finalOnStop
	f.mu.Unlock()
	if stopSeen != nil {
		stopSeen <- struct{}{}
	}
	if stopGate != nil {
		<-stopGate
	}
	if finalOnStop != "" && f.callbacks.Final != nil {
		f.callbacks.Final(plugin.Text{Text: finalOnStop})
	}
	f.mu.Lock()
	f.started = false
	f.mu.Unlock()
	return nil
}

func TestStopKeepsRecognizerFinalFlush(t *testing.T) {
	manager, recognizer, _, _ := setupManager(t, false)
	recognizer.finalOnStop = "停止时排空的最终结果"
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.Stop(); err != nil {
		t.Fatal(err)
	}
	snapshot := manager.Snapshot()
	if len(snapshot.History) != 1 || snapshot.History[0].Text != recognizer.finalOnStop {
		t.Fatalf("history = %#v, want flushed final result", snapshot.History)
	}
	if snapshot.Text != "" {
		t.Fatalf("stopped text = %q, want cleared", snapshot.Text)
	}
}
func (f *fakeRecognizer) Feed([]float32) error {
	f.mu.Lock()
	f.feedCount++
	err := f.feedErr
	f.mu.Unlock()
	return err
}

func (f *fakeRecognizer) feeds() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.feedCount
}

type fakeAudio struct {
	metadata  plugin.Metadata
	callbacks plugin.AudioCallbacks
	started   bool
	startErr  error
}

func (f *fakeAudio) Metadata() plugin.Metadata                { return f.metadata }
func (f *fakeAudio) Available() bool                          { return true }
func (f *fakeAudio) LoadConfig([]byte) error                  { return nil }
func (f *fakeAudio) Init(context.Context) error               { return nil }
func (f *fakeAudio) Close() error                             { return f.Stop() }
func (f *fakeAudio) SetCallbacks(value plugin.AudioCallbacks) { f.callbacks = value }
func (f *fakeAudio) Start(context.Context) error {
	if f.startErr != nil {
		return f.startErr
	}
	f.started = true
	return nil
}
func (f *fakeAudio) Stop() error                              { f.started = false; return nil }

func setupManager(t *testing.T, needsAudio bool) (*Manager, *fakeRecognizer, *fakeAudio, string) {
	t.Helper()
	registry := plugin.NewRegistry()
	recognizer := &fakeRecognizer{needsAudio: needsAudio}
	audio := &fakeAudio{}
	if err := registry.Register("recognizer", recognizer); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("audio", audio); err != nil {
		t.Fatal(err)
	}
	logDir := t.TempDir()
	settings := fakeSettings{
		config.RecognizerSource:           "recognizer",
		config.AudioSource:                "audio",
		config.GeneralResultLogPath:       logDir,
		config.NotificationSensitiveWords: "alert, 提醒",
		// Most tests assert the recognized text verbatim; the punctuation pass
		// has its own tests below.
		config.PunctuationMode: "off",
	}
	manager := New(settings, registry)
	manager.now = func() time.Time { return time.Date(2026, 8, 12, 15, 4, 5, 0, time.Local) }
	return manager, recognizer, audio, logDir
}

// setupChannels wires two labeled audio inputs onto one registered recognizer
// plus one built on demand, which is the microphone + system sound layout.
func setupChannels(t *testing.T) (*Manager, *fakeRecognizer, *fakeRecognizer, *fakeAudio, *fakeAudio, string) {
	t.Helper()
	registry := plugin.NewRegistry()
	first := &fakeRecognizer{needsAudio: true}
	second := &fakeRecognizer{needsAudio: true}
	microphone := &fakeAudio{}
	loopback := &fakeAudio{}
	for key, instance := range map[string]plugin.Plugin{
		"recognizer": first, "microphone": microphone, "loopback": loopback,
	} {
		if err := registry.Register(key, instance); err != nil {
			t.Fatal(err)
		}
	}
	logDir := t.TempDir()
	settings := fakeSettings{
		config.RecognizerSource:     "recognizer",
		config.AudioSource:          "loopback",
		config.AudioChannels:        `[{"source":"microphone","label":"我"},{"source":"loopback","label":"其他人"}]`,
		config.GeneralResultLogPath: logDir,
		config.PunctuationMode:      "off",
	}
	manager := New(settings, registry, WithRecognizerFactory(func(key string) (plugin.Recognizer, error) {
		if key != "recognizer" {
			t.Errorf("extra recognizer key = %q, want the configured recognizer", key)
		}
		return second, nil
	}))
	manager.now = func() time.Time { return time.Date(2026, 8, 12, 15, 4, 5, 0, time.Local) }
	return manager, first, second, microphone, loopback, logDir
}

// Two inputs must decode independently: a shared recognizer would splice the
// person at this computer and everyone else into one sentence.
func TestEachAudioInputDecodesOnItsOwnRecognizer(t *testing.T) {
	manager, first, second, microphone, loopback, logDir := setupChannels(t)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !microphone.started || !loopback.started {
		t.Fatalf("audio sources started: microphone=%v loopback=%v", microphone.started, loopback.started)
	}
	microphone.callbacks.Data([]float32{0.1, 0.2})
	if first.feeds() != 1 || second.feeds() != 0 {
		t.Fatalf("microphone audio reached %d/%d recognizers, want only the first", first.feeds(), second.feeds())
	}
	loopback.callbacks.Data([]float32{0.3})
	if second.feeds() != 1 {
		t.Fatalf("loopback feed count = %d, want 1", second.feeds())
	}

	first.callbacks.Partial(plugin.Text{Text: "我这边下周"})
	second.callbacks.Partial(plugin.Text{Text: "那接口文档"})
	channels := manager.Snapshot().Channels
	want := []ChannelState{
		{Key: "microphone", Label: "我", Text: "我这边下周"},
		{Key: "loopback", Label: "其他人", Text: "那接口文档"},
	}
	if len(channels) != len(want) || channels[0] != want[0] || channels[1] != want[1] {
		t.Fatalf("live channels = %#v, want %#v", channels, want)
	}

	first.callbacks.Final(plugin.Text{Text: "我这边下周就能交", Time: manager.now()})
	second.callbacks.Final(plugin.Text{Text: "那接口文档谁来写", Time: manager.now()})
	history := manager.Snapshot().History
	if len(history) != 2 {
		t.Fatalf("history = %#v, want one sentence per input", history)
	}
	if history[0].Speaker != "我" || history[0].Channel != "microphone" {
		t.Fatalf("first sentence = %#v, want it attributed to the microphone", history[0])
	}
	if history[1].Speaker != "其他人" || history[1].Channel != "loopback" {
		t.Fatalf("second sentence = %#v, want it attributed to the loopback", history[1])
	}

	if err := manager.Stop(); err != nil {
		t.Fatal(err)
	}
	second.mu.Lock()
	closed := second.closed
	second.mu.Unlock()
	if !closed {
		t.Fatal("the recognizer built for the extra input was not closed with the run")
	}
	data, err := os.ReadFile(filepath.Join(logDir, "26-08-12-15-04-05.txt"))
	if err != nil {
		t.Fatal(err)
	}
	want2 := "15:04:05 我: 我这边下周就能交\n15:04:05 其他人: 那接口文档谁来写\n"
	if string(data) != want2 {
		t.Fatalf("log = %q, want %q", string(data), want2)
	}
}

// A build with no way to create a second recognizer must say so instead of
// quietly recording both speakers on one.
func TestExtraAudioInputNeedsARecognizerFactory(t *testing.T) {
	manager, _, _, _, _, _ := setupChannels(t)
	manager.newRecognizer = nil
	if err := manager.Start(context.Background()); !errors.Is(err, ErrExtraInputUnsupported) {
		t.Fatalf("Start error = %v, want ErrExtraInputUnsupported", err)
	}
	if got := manager.Snapshot().Status; got != Stopped {
		t.Fatalf("status = %q, want stopped", got)
	}
}

// Two recognizers share one CPU, so a full audio queue is expected. It costs a
// gap in one sentence and must not end the meeting.
func TestDroppedAudioWarnsOnceAndKeepsRunning(t *testing.T) {
	manager, recognizer, _, audio, _, _ := setupChannels(t)
	var notificationsMu sync.Mutex
	var notifications []Notification
	manager.SubscribeNotifications(func(value Notification) {
		notificationsMu.Lock()
		notifications = append(notifications, value)
		notificationsMu.Unlock()
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	recognizer.mu.Lock()
	recognizer.feedErr = fmt.Errorf("sherpa-onnx %w", plugin.ErrAudioBackpressure)
	recognizer.mu.Unlock()

	audio.callbacks.Data([]float32{0.1})
	audio.callbacks.Data([]float32{0.2})
	if got := manager.Snapshot().Status; got != Running {
		t.Fatalf("status = %q after dropped audio, want running", got)
	}
	notificationsMu.Lock()
	count := len(notifications)
	first := Notification{}
	if count > 0 {
		first = notifications[0]
	}
	notificationsMu.Unlock()
	if count != 1 || first.Level != NotificationWarning {
		t.Fatalf("notifications = %d %#v, want one warning", count, first)
	}
	if !strings.Contains(first.Message, "音频数据（我）") {
		t.Fatalf("warning = %q, want it to name the input that dropped audio", first.Message)
	}
	if err := manager.Stop(); err != nil {
		t.Fatal(err)
	}
}

// One input failing to open must not leave the other one recording, and the
// recognizer built for the failed run has to be released.
func TestFailedInputRollsBackTheWholeRun(t *testing.T) {
	manager, _, second, microphone, loopback, _ := setupChannels(t)
	loopback.startErr = errors.New("device in use")
	err := manager.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "device in use") {
		t.Fatalf("Start error = %v, want the audio source failure", err)
	}
	if microphone.started {
		t.Fatal("the first input stayed recording after the second one failed to open")
	}
	if got := manager.Snapshot().Status; got != Stopped {
		t.Fatalf("status = %q, want stopped", got)
	}
	second.mu.Lock()
	closed := second.closed
	second.mu.Unlock()
	if !closed {
		t.Fatal("the recognizer built for the failed run was not closed")
	}
	if got := manager.Snapshot().Channels; len(got) != 0 {
		t.Fatalf("channels after a failed start = %#v, want none", got)
	}
}

// A configured input that no longer resolves must stop the run before any
// audio device is opened.
func TestMissingInputIsReportedBeforeAnythingStarts(t *testing.T) {
	manager, _, _, microphone, _, _ := setupChannels(t)
	manager.settings.(fakeSettings)[config.AudioChannels] =
		`[{"source":"microphone","label":"我"},{"source":"missing","label":"其他人"}]`
	if err := manager.Start(context.Background()); !errors.Is(err, ErrAudioSourceNotFound) {
		t.Fatalf("Start error = %v, want ErrAudioSourceNotFound", err)
	}
	if microphone.started {
		t.Fatal("an audio device was opened for a run that could not start")
	}
	if got := manager.Snapshot().Status; got != Stopped {
		t.Fatalf("status = %q, want stopped", got)
	}
}

func TestSelfCapturingRecognizerDoesNotStartAudio(t *testing.T) {
	manager, recognizer, audio, _ := setupManager(t, false)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !recognizer.started {
		t.Fatal("recognizer did not start")
	}
	if audio.started {
		t.Fatal("audio source started for self-capturing recognizer")
	}
	if err := manager.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestAudioFeedsRecognizer(t *testing.T) {
	manager, recognizer, audio, _ := setupManager(t, true)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	audio.callbacks.Data([]float32{0.1, 0.2})
	recognizer.mu.Lock()
	count := recognizer.feedCount
	recognizer.mu.Unlock()
	if count != 1 {
		t.Fatalf("Feed count = %d", count)
	}
	_ = manager.Stop()
}

func TestFinalAddsHistoryAndWritesCompatibleLog(t *testing.T) {
	manager, recognizer, _, logDir := setupManager(t, false)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	recognizer.callbacks.Partial(plugin.Text{Text: "你好"})
	recognizer.callbacks.Final(plugin.Text{Text: "你好世界", Time: manager.now()})
	snapshot := manager.Snapshot()
	if len(snapshot.History) != 1 || snapshot.History[0].Text != "你好世界" {
		t.Fatalf("history = %#v", snapshot.History)
	}
	if snapshot.Text != "你好世界" {
		t.Fatalf("current text = %q, want final caption", snapshot.Text)
	}
	if err := manager.Stop(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(logDir, "26-08-12-15-04-05.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "15:04:05: 你好世界\n"; got != want {
		t.Fatalf("log = %q, want %q", got, want)
	}
}

func TestStopCommitsOutstandingPartialOnce(t *testing.T) {
	manager, recognizer, _, _ := setupManager(t, false)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	recognizer.callbacks.Partial(plugin.Text{Text: "未完成"})
	if err := manager.Stop(); err != nil {
		t.Fatal(err)
	}
	if got := manager.Snapshot().History; len(got) != 1 || got[0].Text != "未完成" {
		t.Fatalf("history = %#v", got)
	}
	if err := manager.Stop(); err != nil {
		t.Fatal(err)
	}
	if got := manager.Snapshot().History; len(got) != 1 {
		t.Fatalf("history duplicated: %#v", got)
	}
}

func TestSensitiveWordNotifiesOncePerSentence(t *testing.T) {
	manager, recognizer, _, _ := setupManager(t, false)
	var notifications []Notification
	manager.SubscribeNotifications(func(value Notification) { notifications = append(notifications, value) })
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	recognizer.callbacks.Partial(plugin.Text{Text: "alert one"})
	recognizer.callbacks.Partial(plugin.Text{Text: "alert two"})
	if len(notifications) != 1 {
		t.Fatalf("notifications = %#v", notifications)
	}
	recognizer.callbacks.Final(plugin.Text{Text: "alert two"})
	recognizer.callbacks.Partial(plugin.Text{Text: "alert three"})
	if len(notifications) != 2 {
		t.Fatalf("notifications after final = %#v", notifications)
	}
	_ = manager.Stop()
}

func TestMissingRecognizerIsActionable(t *testing.T) {
	manager := New(fakeSettings{config.RecognizerSource: "missing"}, plugin.NewRegistry())
	err := manager.Start(context.Background())
	if !errors.Is(err, ErrRecognizerNotFound) {
		t.Fatalf("error = %v", err)
	}
}

func TestUnavailableRecognizerIsDistinguishedFromMissing(t *testing.T) {
	registry := plugin.NewRegistry()
	available := false
	recognizer := &fakeRecognizer{available: &available}
	if err := registry.Register("recognizer", recognizer); err != nil {
		t.Fatal(err)
	}
	manager := New(fakeSettings{config.RecognizerSource: "recognizer"}, registry)
	err := manager.Start(context.Background())
	if !errors.Is(err, ErrRecognizerUnavailable) {
		t.Fatalf("error = %v, want ErrRecognizerUnavailable", err)
	}
	if errors.Is(err, ErrRecognizerNotFound) {
		t.Fatalf("unavailable recognizer reported as missing: %v", err)
	}
}

func TestEmptyHistorySerializesAsArray(t *testing.T) {
	manager := New(fakeSettings{}, plugin.NewRegistry())
	data, err := json.Marshal(manager.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"history":null`) {
		t.Fatalf("snapshot contains null history: %s", data)
	}
}

func TestHistorySnapshotIsBoundedToNewestEntries(t *testing.T) {
	manager := New(fakeSettings{}, plugin.NewRegistry())
	for i := 0; i < maxHistoryEntries+3; i++ {
		manager.finishLocked(nil, plugin.Text{Text: fmt.Sprintf("item-%d", i)})
	}

	history := manager.Snapshot().History
	if len(history) != maxHistoryEntries {
		t.Fatalf("history length = %d, want %d", len(history), maxHistoryEntries)
	}
	if got, want := history[0].Text, "item-3"; got != want {
		t.Fatalf("oldest retained history = %q, want %q", got, want)
	}
	if got, want := history[len(history)-1].Text, fmt.Sprintf("item-%d", maxHistoryEntries+2); got != want {
		t.Fatalf("newest retained history = %q, want %q", got, want)
	}
}

func TestPartialListenerUpdateOmitsHistory(t *testing.T) {
	manager, recognizer, _, _ := setupManager(t, false)
	var updatesMu sync.Mutex
	var updates []Snapshot
	manager.Subscribe(func(snapshot Snapshot) {
		updatesMu.Lock()
		updates = append(updates, snapshot)
		updatesMu.Unlock()
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	recognizer.callbacks.Final(plugin.Text{Text: "completed"})
	recognizer.callbacks.Partial(plugin.Text{Text: "live"})

	updatesMu.Lock()
	latest := updates[len(updates)-1]
	updatesMu.Unlock()
	if !latest.LiveOnly() {
		t.Fatal("partial listener update was not marked live-only")
	}
	if latest.History != nil {
		t.Fatalf("live-only history = %#v, want nil", latest.History)
	}
	if latest.Text != "live" {
		t.Fatalf("live-only text = %q, want live", latest.Text)
	}
	if err := manager.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentStartIsRejectedWhilePluginStarts(t *testing.T) {
	manager, recognizer, _, _ := setupManager(t, false)
	gate := make(chan struct{})
	seen := make(chan struct{}, 1)
	recognizer.startGate = gate
	recognizer.startSeen = seen

	firstDone := make(chan error, 1)
	go func() { firstDone <- manager.Start(context.Background()) }()
	<-seen
	if err := manager.Start(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Start error = %v, want ErrAlreadyRunning", err)
	}
	close(gate)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := manager.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestStopCancelsPluginStart(t *testing.T) {
	manager, recognizer, _, _ := setupManager(t, false)
	seen := make(chan struct{}, 1)
	recognizer.startGate = make(chan struct{})
	recognizer.startSeen = seen

	startDone := make(chan error, 1)
	go func() { startDone <- manager.Start(context.Background()) }()
	<-seen
	if err := manager.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := <-startDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start error = %v, want context cancellation", err)
	}
	if got := manager.Snapshot().Status; got != Stopped {
		t.Fatalf("status = %q, want %q", got, Stopped)
	}
}

func TestRestartCannotOvertakeCancelledStart(t *testing.T) {
	manager, recognizer, _, _ := setupManager(t, false)
	seen := make(chan struct{}, 1)
	gate := make(chan struct{})
	recognizer.startGate = gate
	recognizer.startSeen = seen
	recognizer.ignoreStartCancel = true

	firstDone := make(chan error, 1)
	go func() { firstDone <- manager.Start(context.Background()) }()
	<-seen
	stopDone := make(chan error, 1)
	go func() { stopDone <- manager.Stop() }()

	deadline := time.Now().Add(time.Second)
	for {
		manager.mu.Lock()
		stopping := manager.stopping
		manager.mu.Unlock()
		if stopping {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("manager did not enter stopping state")
		}
		time.Sleep(time.Millisecond)
	}
	if err := manager.Start(context.Background()); !errors.Is(err, ErrStopping) {
		t.Fatalf("restart error = %v, want ErrStopping", err)
	}
	close(gate)
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("first Start error = %v, want context cancellation", err)
	}
	if err := <-stopDone; err != nil {
		t.Fatal(err)
	}
}

func TestPluginErrorStopsActiveRun(t *testing.T) {
	manager, recognizer, _, _ := setupManager(t, false)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	recognizer.callbacks.Error(errors.New("decoder exited"))
	deadline := time.Now().Add(time.Second)
	for manager.Snapshot().Status != Stopped {
		if time.Now().After(deadline) {
			t.Fatalf("status remained %q", manager.Snapshot().Status)
		}
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(manager.Snapshot().LastError, "decoder exited") {
		t.Fatalf("last error = %q", manager.Snapshot().LastError)
	}
}

func TestPluginWarningDuringStartDoesNotCancelRun(t *testing.T) {
	manager, recognizer, _, _ := setupManager(t, false)
	recognizer.warningOnStart = errors.New("GPU 不可用，已回退 CPU")
	var notifications []Notification
	manager.SubscribeNotifications(func(value Notification) {
		notifications = append(notifications, value)
	})

	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start after recoverable warning: %v", err)
	}
	snapshot := manager.Snapshot()
	if snapshot.Status != Running {
		t.Fatalf("status = %q, want running", snapshot.Status)
	}
	if got, want := snapshot.LastError, "识别器：GPU 不可用，已回退 CPU"; got != want {
		t.Fatalf("last error = %q, want %q", got, want)
	}
	recognizer.mu.Lock()
	stopCalls := recognizer.stopCalls
	recognizer.mu.Unlock()
	if stopCalls != 0 {
		t.Fatalf("recognizer Stop calls = %d after start warning, want 0", stopCalls)
	}
	if len(notifications) != 1 || notifications[0].Level != NotificationWarning || notifications[0].Title != "插件警告" || notifications[0].Message != snapshot.LastError {
		t.Fatalf("notifications = %#v, want one visible warning", notifications)
	}

	recognizer.callbacks.Final(plugin.Text{Text: "回退后仍可识别"})
	if got := manager.Snapshot().History; len(got) != 1 || got[0].Text != "回退后仍可识别" {
		t.Fatalf("history after start warning = %#v", got)
	}
	if err := manager.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestPluginWarningWhileRunningDoesNotStopRun(t *testing.T) {
	manager, recognizer, _, _ := setupManager(t, false)
	var notifications []Notification
	manager.SubscribeNotifications(func(value Notification) {
		notifications = append(notifications, value)
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	recognizer.callbacks.Warning(errors.New("临时解码延迟"))
	snapshot := manager.Snapshot()
	if snapshot.Status != Running {
		t.Fatalf("status = %q after warning, want running", snapshot.Status)
	}
	if got, want := snapshot.LastError, "识别器：临时解码延迟"; got != want {
		t.Fatalf("last error = %q, want %q", got, want)
	}
	recognizer.mu.Lock()
	stopCalls := recognizer.stopCalls
	recognizer.mu.Unlock()
	if stopCalls != 0 {
		t.Fatalf("recognizer Stop calls = %d after running warning, want 0", stopCalls)
	}
	if len(notifications) != 1 || notifications[0].Level != NotificationWarning || notifications[0].Title != "插件警告" || notifications[0].Message != snapshot.LastError {
		t.Fatalf("notifications = %#v, want one visible warning", notifications)
	}

	recognizer.callbacks.Partial(plugin.Text{Text: "仍在工作"})
	if got := manager.Snapshot(); got.Status != Running || got.Text != "仍在工作" {
		t.Fatalf("snapshot after continued recognition = %#v", got)
	}
	if err := manager.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestPluginErrorAndManualStopSerializeBeforeRestart(t *testing.T) {
	manager, recognizer, _, _ := setupManager(t, false)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	stopGate := make(chan struct{})
	stopSeen := make(chan struct{}, 1)
	recognizer.mu.Lock()
	recognizer.stopGate = stopGate
	recognizer.stopSeen = stopSeen
	recognizer.mu.Unlock()

	recognizer.callbacks.Error(errors.New("decoder exited"))
	select {
	case <-stopSeen:
	case <-time.After(time.Second):
		t.Fatal("plugin-error stop did not reach recognizer Stop")
	}

	manualStopDone := make(chan error, 1)
	go func() { manualStopDone <- manager.Stop() }()
	deadline := time.Now().Add(time.Second)
	for {
		manager.mu.Lock()
		pending := manager.stopRequests
		manager.mu.Unlock()
		if pending == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending stop requests = %d, want 2", pending)
		}
		time.Sleep(time.Millisecond)
	}

	if err := manager.Start(context.Background()); !errors.Is(err, ErrStopping) {
		t.Fatalf("restart during serialized stops = %v, want ErrStopping", err)
	}
	close(stopGate)
	if err := <-manualStopDone; err != nil {
		t.Fatal(err)
	}

	manager.mu.Lock()
	stopping, pending := manager.stopping, manager.stopRequests
	manager.mu.Unlock()
	if stopping || pending != 0 {
		t.Fatalf("stop state after both transitions: stopping=%v pending=%d", stopping, pending)
	}
	recognizer.mu.Lock()
	stopCalls := recognizer.stopCalls
	recognizer.stopGate = nil
	recognizer.stopSeen = nil
	recognizer.mu.Unlock()
	if stopCalls != 1 {
		t.Fatalf("recognizer Stop calls = %d, want 1", stopCalls)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("restart after serialized stops: %v", err)
	}
	if err := manager.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestPluginErrorReservesStopBeforeAsyncCleanup(t *testing.T) {
	manager, recognizer, _, _ := setupManager(t, false)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	notificationSeen := make(chan struct{})
	releaseNotification := make(chan struct{})
	manager.SubscribeNotifications(func(Notification) {
		close(notificationSeen)
		<-releaseNotification
	})

	errorCallbackDone := make(chan struct{})
	go func() {
		recognizer.callbacks.Error(errors.New("decoder exited"))
		close(errorCallbackDone)
	}()
	select {
	case <-notificationSeen:
	case <-time.After(time.Second):
		t.Fatal("plugin-error notification was not published")
	}

	// The error callback is deliberately blocked before it can launch cleanup.
	// A manual Stop may finish, but the reserved error Stop must still prevent a
	// new run from overtaking that pending transition.
	if err := manager.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background()); !errors.Is(err, ErrStopping) {
		t.Fatalf("restart before async error cleanup = %v, want ErrStopping", err)
	}

	close(releaseNotification)
	select {
	case <-errorCallbackDone:
	case <-time.After(time.Second):
		t.Fatal("plugin-error callback did not finish")
	}
	deadline := time.Now().Add(time.Second)
	for {
		manager.mu.Lock()
		stopping, pending := manager.stopping, manager.stopRequests
		manager.mu.Unlock()
		if !stopping && pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("reserved stop did not finish: stopping=%v pending=%d", stopping, pending)
		}
		time.Sleep(time.Millisecond)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("restart after async error cleanup: %v", err)
	}
	if err := manager.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestPluginErrorDuringStartCannotBecomeRunning(t *testing.T) {
	manager, recognizer, _, _ := setupManager(t, false)
	recognizer.errorOnStart = errors.New("exited during start")
	err := manager.Start(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Start error = %v, want context cancellation", err)
	}
	if got := manager.Snapshot().Status; got != Stopped {
		t.Fatalf("status = %q, want stopped", got)
	}
}

func TestLogOpenFailureDoesNotLeaveManagerStarting(t *testing.T) {
	manager, _, _, logDir := setupManager(t, false)
	filePath := filepath.Join(logDir, "not-a-directory")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager.settings = fakeSettings{
		config.RecognizerSource:     "recognizer",
		config.GeneralResultLogPath: filePath,
	}
	if err := manager.Start(context.Background()); err == nil {
		t.Fatal("Start succeeded with an invalid log directory")
	}
	manager.mu.Lock()
	starting := manager.starting
	manager.mu.Unlock()
	if starting {
		t.Fatal("manager remained in starting state")
	}
	manager.settings = fakeSettings{
		config.RecognizerSource:     "recognizer",
		config.GeneralResultLogPath: logDir,
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("restart after log error: %v", err)
	}
	if err := manager.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestPunctuationClosesFinalizedSentences(t *testing.T) {
	manager, recognizer, _, logDir := setupManager(t, false)
	manager.settings.(fakeSettings)[config.PunctuationMode] = "rules"
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	recognizer.callbacks.Partial(plugin.Text{Text: "今天我们讨论排期"})
	if got := manager.Snapshot().Text; got != "今天我们讨论排期" {
		t.Fatalf("partial caption = %q, want the raw recognizer text", got)
	}
	recognizer.callbacks.Final(plugin.Text{Text: "今天我们讨论排期", Time: manager.now()})
	snapshot := manager.Snapshot()
	if len(snapshot.History) != 1 || snapshot.History[0].Text != "今天我们讨论排期。" {
		t.Fatalf("history = %#v, want a closed sentence", snapshot.History)
	}
	if snapshot.Text != "今天我们讨论排期。" {
		t.Fatalf("caption = %q, want the punctuated sentence", snapshot.Text)
	}
	if err := manager.Stop(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(logDir, "26-08-12-15-04-05.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "15:04:05: 今天我们讨论排期。\n"; got != want {
		t.Fatalf("log = %q, want %q", got, want)
	}
}

func TestPunctuationCommitsOutstandingPartialOnStop(t *testing.T) {
	manager, recognizer, _, _ := setupManager(t, false)
	manager.settings.(fakeSettings)[config.PunctuationMode] = "rules"
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	recognizer.callbacks.Partial(plugin.Text{Text: "这句还没说完就停止了"})
	if err := manager.Stop(); err != nil {
		t.Fatal(err)
	}
	if got := manager.Snapshot().History; len(got) != 1 || got[0].Text != "这句还没说完就停止了。" {
		t.Fatalf("history = %#v, want the punctuated tail sentence", got)
	}
}

// A model that cannot be loaded must cost commas, not the session: the run
// starts on the rule pass and says why.
func TestPunctuationModelFailureWarnsAndFallsBack(t *testing.T) {
	manager, recognizer, _, _ := setupManager(t, false)
	settings := manager.settings.(fakeSettings)
	settings[config.PunctuationMode] = "model"
	settings[config.PunctuationModelPath] = filepath.Join(t.TempDir(), "missing.onnx")
	var notifications []Notification
	manager.SubscribeNotifications(func(value Notification) { notifications = append(notifications, value) })
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start with an unusable punctuation model: %v", err)
	}
	if len(notifications) != 1 || notifications[0].Level != NotificationWarning {
		t.Fatalf("notifications = %#v, want one warning", notifications)
	}
	if !strings.Contains(notifications[0].Message, "标点") {
		t.Fatalf("warning = %q, want it to name the punctuation pass", notifications[0].Message)
	}
	recognizer.callbacks.Final(plugin.Text{Text: "模型没加载上", Time: manager.now()})
	if got := manager.Snapshot().History; len(got) != 1 || got[0].Text != "模型没加载上。" {
		t.Fatalf("history = %#v, want the rule pass to punctuate", got)
	}
	if err := manager.Stop(); err != nil {
		t.Fatal(err)
	}
}
