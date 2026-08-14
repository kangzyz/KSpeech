// Package job coordinates the labeled audio inputs of one run with a
// recognizer for each of them.
package job

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kangzyz/KSpeech/internal/config"
	"github.com/kangzyz/KSpeech/internal/plugin"
	"github.com/kangzyz/KSpeech/internal/punctuation"
)

type Status string

const (
	Stopped Status = "stopped"
	Running Status = "running"
	Paused  Status = "paused"

	// Keep the live UI snapshot bounded during long-running sessions. The
	// recognition log remains the complete, durable record when it is enabled.
	maxHistoryEntries = 2000

	// Punctuation runs once per finished sentence, between utterances rather
	// than during them, so a second thread buys latency at a negligible cost.
	punctuationThreads = 2
)

var (
	ErrRecognizerNotFound     = errors.New("configured recognizer was not found")
	ErrRecognizerUnavailable  = errors.New("configured recognizer is unavailable in this build")
	ErrAudioSourceNotFound    = errors.New("configured audio source was not found")
	ErrAudioSourceUnavailable = errors.New("configured audio source is unavailable in this build")
	ErrNoAudioInput           = errors.New("no audio input is configured")
	ErrExtraInputUnsupported  = errors.New("this build cannot create one recognizer per audio input")
	ErrAlreadyRunning         = errors.New("recognition job is already running")
	ErrStopping               = errors.New("recognition job is stopping")
)

type Settings interface {
	String(key string) string
}

// RecognizerFactory builds an additional instance of the recognizer registered
// under key. Capturing several inputs at once needs one recognizer per input: a
// single streaming recognizer would interleave two speakers into one sentence.
type RecognizerFactory func(key string) (plugin.Recognizer, error)

// Option configures a Manager at construction time.
type Option func(*Manager)

// WithRecognizerFactory supplies the extra recognizer instances that capturing
// more than one audio input needs. Without it a run is limited to one input.
func WithRecognizerFactory(factory RecognizerFactory) Option {
	return func(m *Manager) { m.newRecognizer = factory }
}

type NotificationLevel string

const (
	NotificationInfo    NotificationLevel = "info"
	NotificationWarning NotificationLevel = "warning"
	NotificationError   NotificationLevel = "error"
)

type Notification struct {
	Title   string            `json:"title"`
	Message string            `json:"message"`
	Level   NotificationLevel `json:"level"`
}

// HistoryEntry is one finished sentence. Speaker is the label of the audio
// input it came from, and Channel that input's plugin key; both are empty when
// a session captures a single unlabeled input.
type HistoryEntry struct {
	ID      uint64    `json:"id"`
	Time    time.Time `json:"time"`
	Text    string    `json:"text"`
	Speaker string    `json:"speaker,omitempty"`
	Channel string    `json:"channel,omitempty"`
}

// ChannelState is one audio input's live caption line.
type ChannelState struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Text  string `json:"text"`
}

type Snapshot struct {
	Status         Status         `json:"status"`
	RunningSeconds int64          `json:"runningSeconds"`
	Text           string         `json:"text"`
	Channels       []ChannelState `json:"channels"`
	History        []HistoryEntry `json:"history"`
	LastError      string         `json:"lastError,omitempty"`
	liveOnly       bool
}

// LiveOnly reports that a listener snapshot contains only the high-frequency
// caption/status fields. Callers must retain their previous History value.
func (s Snapshot) LiveOnly() bool { return s.liveOnly }

type Listener func(Snapshot)
type NotificationListener func(Notification)

// channel is one audio input together with the recognizer that transcribes it.
// The caption fields survive a pause; the plugin fields are held only while a
// run owns them.
type channel struct {
	key   string
	label string

	// Guarded by Manager.mu.
	text         string
	sentenceOpen bool
	notified     bool
	dropped      bool

	source     plugin.AudioSource
	recognizer plugin.Recognizer
	owned      bool
}

type Manager struct {
	settings      Settings
	registry      *plugin.Registry
	newRecognizer RecognizerFactory
	now           func() time.Time

	mu              sync.Mutex
	status          Status
	runningSeconds  int64
	currentText     string
	channels        []*channel
	history         []HistoryEntry
	nextHistoryID   uint64
	lastError       string
	starting        bool
	stopping        bool
	stopRequests    int
	failureStopping bool
	sensitiveWords  []string
	activeRun       uint64
	cancel          context.CancelFunc
	timerDone       chan struct{}
	logFile         *os.File
	logDisabled     bool
	listeners       map[uint64]Listener
	notifications   map[uint64]NotificationListener
	nextListenerID  uint64
	transitionMu    sync.Mutex

	// The punctuator owns native model resources and is not safe for concurrent
	// use, so its own mutex both guards the field and serializes rewrites.
	punctuationMu sync.Mutex
	punctuator    punctuation.Punctuator
}

func New(settings Settings, registry *plugin.Registry, options ...Option) *Manager {
	manager := &Manager{
		settings:      settings,
		registry:      registry,
		now:           time.Now,
		status:        Stopped,
		currentText:   "欢迎使用 KSpeech",
		listeners:     make(map[uint64]Listener),
		notifications: make(map[uint64]NotificationListener),
	}
	for _, option := range options {
		if option != nil {
			option(manager)
		}
	}
	return manager
}

func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked()
}

// LiveSnapshot returns the current caption/status fields without copying the
// potentially large session history. It is intended for coalesced UI updates.
func (m *Manager) LiveSnapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.liveSnapshotLocked()
}

func (m *Manager) Subscribe(listener Listener) func() {
	if listener == nil {
		return func() {}
	}
	m.mu.Lock()
	m.nextListenerID++
	id := m.nextListenerID
	m.listeners[id] = listener
	snapshot := m.snapshotLocked()
	m.mu.Unlock()
	listener(snapshot)
	return func() {
		m.mu.Lock()
		delete(m.listeners, id)
		m.mu.Unlock()
	}
}

func (m *Manager) SubscribeNotifications(listener NotificationListener) func() {
	if listener == nil {
		return func() {}
	}
	m.mu.Lock()
	m.nextListenerID++
	id := m.nextListenerID
	m.notifications[id] = listener
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		delete(m.notifications, id)
		m.mu.Unlock()
	}
}

func (m *Manager) Start(parent context.Context) error {
	m.mu.Lock()
	if m.stopping {
		m.mu.Unlock()
		return ErrStopping
	}
	if m.status == Running || m.starting {
		m.mu.Unlock()
		return ErrAlreadyRunning
	}
	m.mu.Unlock()
	m.transitionMu.Lock()
	defer m.transitionMu.Unlock()
	if parent == nil {
		parent = context.Background()
	}
	if err := parent.Err(); err != nil {
		return err
	}

	m.mu.Lock()
	if m.stopping {
		m.mu.Unlock()
		return ErrStopping
	}
	if m.status == Running || m.starting {
		m.mu.Unlock()
		return ErrAlreadyRunning
	}
	previousStatus := m.status
	recognizerKey := m.settings.String(config.RecognizerSource)
	base, ok := m.registry.Recognizer(recognizerKey)
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrRecognizerNotFound, recognizerKey)
	}
	if !base.Available() {
		m.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrRecognizerUnavailable, recognizerKey)
	}
	channels, err := m.prepareChannelsLocked(parent, recognizerKey, base)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if previousStatus == Paused {
		// Resuming keeps every line on screen instead of blanking the caption
		// window until each input speaks again.
		carryCaptions(m.channels, channels)
	}

	m.activeRun++
	runID := m.activeRun
	ctx, cancel := context.WithCancel(parent)
	m.starting = true
	m.cancel = cancel
	m.channels = channels
	m.sensitiveWords = splitSensitiveWords(m.settings.String(config.NotificationSensitiveWords))
	m.lastError = ""
	m.logDisabled = false
	if previousStatus == Stopped {
		m.runningSeconds = 0
	}
	m.wireChannelsLocked(runID, channels)
	if err := m.openLogLocked(); err != nil {
		m.activeRun++
		m.cancel = nil
		m.starting = false
		m.closeLogLocked()
		owned := detachChannels(channels)
		m.channels = nil
		cancel()
		snapshot, listeners := m.snapshotAndListenersLocked()
		m.mu.Unlock()
		closeRecognizers(owned)
		m.publish(snapshot, listeners)
		return err
	}
	m.mu.Unlock()

	if err := m.openPunctuator(); err != nil {
		// Captions matter more than their punctuation: the run keeps going with
		// the rule pass, and the user is told why the model was not used.
		m.onPluginWarning(runID, "标点", err)
	}
	if err := startChannels(ctx, channels); err != nil {
		m.rollbackStart(runID, channels)
		return err
	}

	m.mu.Lock()
	if m.activeRun != runID {
		m.mu.Unlock()
		// Stop before detaching: detaching first would drop the very plugin
		// references this needs to shut down.
		stopChannels(channels)
		m.mu.Lock()
		owned := detachChannels(channels)
		m.mu.Unlock()
		closeRecognizers(owned)
		return context.Canceled
	}
	m.starting = false
	m.status = Running
	m.runningSeconds++ // Preserve the legacy timer's immediate first tick.
	timerDone := make(chan struct{})
	m.timerDone = timerDone
	snapshot, listeners := m.snapshotAndListenersLocked()
	m.mu.Unlock()
	m.publish(snapshot, listeners)
	go m.runTimer(ctx, runID, timerDone)
	return nil
}

// prepareChannelsLocked resolves the configured audio inputs and gives each one
// a recognizer. The first input reuses the registered recognizer so a
// single-input session behaves exactly as before; every further input gets its
// own instance, which is what keeps two speakers out of one sentence.
func (m *Manager) prepareChannelsLocked(ctx context.Context, recognizerKey string, base plugin.Recognizer) ([]*channel, error) {
	recognizerConfig := []byte(m.settings.String(config.PluginConfigKey(recognizerKey)))
	if !base.NeedsAudio() {
		// The recognizer captures its own audio, so labeling inputs would
		// describe something KSpeech does not control.
		if err := base.LoadConfig(recognizerConfig); err != nil {
			return nil, fmt.Errorf("load recognizer config: %w", err)
		}
		return []*channel{{recognizer: base}}, nil
	}

	requested := config.AudioChannelList(m.settings)
	if len(requested) == 0 {
		return nil, ErrNoAudioInput
	}
	channels := make([]*channel, 0, len(requested))
	fail := func(err error) ([]*channel, error) {
		closeRecognizers(detachChannels(channels))
		return nil, err
	}
	for _, requestedChannel := range requested {
		source, ok := m.registry.AudioSource(requestedChannel.Source)
		if !ok {
			return fail(fmt.Errorf("%w: %s", ErrAudioSourceNotFound, requestedChannel.Source))
		}
		if !source.Available() {
			return fail(fmt.Errorf("%w: %s", ErrAudioSourceUnavailable, requestedChannel.Source))
		}
		if err := source.LoadConfig([]byte(m.settings.String(config.PluginConfigKey(requestedChannel.Source)))); err != nil {
			return fail(fmt.Errorf("load audio source config: %w", err))
		}
		recognizer, owned := base, false
		if len(channels) > 0 {
			instance, err := m.extraRecognizer(ctx, recognizerKey)
			if err != nil {
				return fail(err)
			}
			recognizer, owned = instance, true
		}
		if err := recognizer.LoadConfig(recognizerConfig); err != nil {
			if owned {
				_ = recognizer.Close()
			}
			return fail(fmt.Errorf("load recognizer config: %w", err))
		}
		channels = append(channels, &channel{
			key:        requestedChannel.Source,
			label:      requestedChannel.Label,
			source:     source,
			recognizer: recognizer,
			owned:      owned,
		})
	}
	return channels, nil
}

func (m *Manager) extraRecognizer(ctx context.Context, key string) (plugin.Recognizer, error) {
	if m.newRecognizer == nil {
		return nil, fmt.Errorf("%w: %s", ErrExtraInputUnsupported, key)
	}
	instance, err := m.newRecognizer(key)
	if err != nil {
		return nil, fmt.Errorf("create a recognizer for the extra audio input: %w", err)
	}
	if instance == nil {
		return nil, fmt.Errorf("%w: %s", ErrExtraInputUnsupported, key)
	}
	if err := instance.Init(ctx); err != nil {
		_ = instance.Close()
		return nil, fmt.Errorf("initialize the recognizer for the extra audio input: %w", err)
	}
	return instance, nil
}

func (m *Manager) wireChannelsLocked(runID uint64, channels []*channel) {
	for index, current := range channels {
		index, recognizer, label := index, current.recognizer, current.label
		recognizer.SetCallbacks(plugin.RecognizerCallbacks{
			Partial: func(text plugin.Text) { m.onPartial(runID, index, text) },
			Final:   func(text plugin.Text) { m.onFinal(runID, index, text) },
			Warning: func(err error) { m.onPluginWarning(runID, componentName("识别器", label), err) },
			Error:   func(err error) { m.onPluginError(runID, componentName("识别器", label), err) },
		})
		if current.source == nil {
			continue
		}
		current.source.SetCallbacks(plugin.AudioCallbacks{
			Data: func(samples []float32) {
				err := recognizer.Feed(samples)
				if err == nil {
					return
				}
				// Backpressure costs a gap in one sentence; ending the session
				// over it would cost the rest of the meeting.
				if errors.Is(err, plugin.ErrAudioBackpressure) {
					m.onAudioDropped(runID, index, err)
					return
				}
				m.onPluginError(runID, componentName("音频数据", label), err)
			},
			Error: func(err error) { m.onPluginError(runID, componentName("音频源", label), err) },
		})
	}
}

func (m *Manager) Pause() error {
	return m.stopActive(Paused, false)
}

func (m *Manager) Stop() error {
	return m.stopActive(Stopped, true)
}

func (m *Manager) Close() error {
	return m.Stop()
}

func (m *Manager) stopActive(next Status, clearText bool) error {
	m.mu.Lock()
	// Mark every stop request before waiting for transitionMu. This both lets a
	// Stop cancel an in-flight Start and keeps Start rejected until all queued
	// Stop/Pause calls have completed their serialized transition.
	cancel := m.beginStopRequestLocked()
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return m.stopReserved(next, clearText)
}

// stopReserved completes a stop request that has already been made visible by
// beginStopRequestLocked. Error callbacks reserve synchronously before they
// publish or launch their asynchronous cleanup, closing the restart window.
func (m *Manager) stopReserved(next Status, clearText bool) error {
	m.transitionMu.Lock()

	m.mu.Lock()
	if m.status != Running && !m.starting {
		m.status = next
		m.failureStopping = false
		if clearText {
			m.currentText = ""
			m.channels = nil
		}
		snapshot, listeners := m.snapshotAndListenersLocked()
		m.finishStopRequestLocked()
		m.mu.Unlock()
		// Close while transitionMu is still held: a queued Start must not have
		// its fresh punctuator closed by this stop.
		m.closePunctuator()
		m.transitionMu.Unlock()
		m.publish(snapshot, listeners)
		return nil
	}
	cancel := m.cancel
	channels := append([]*channel(nil), m.channels...)
	runID := m.activeRun
	m.starting = false
	m.timerDone = nil
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	stopErrors := stopChannels(channels)

	m.mu.Lock()
	// A streaming recognizer may synchronously flush its final result from
	// Stop. Keep this run's callbacks valid until Stop returns, then fence any
	// late callback before publishing the stopped state.
	if m.activeRun == runID {
		m.activeRun++
	}
	m.cancel = nil
	for _, current := range channels {
		if !current.sentenceOpen || current.text == "" {
			continue
		}
		final := plugin.Text{Time: m.now(), Text: m.punctuate(current.text)}
		m.finishLocked(current, final)
		if !clearText {
			// A pause keeps the completed sentence on screen.
			current.text = final.Text
			m.currentText = final.Text
		}
	}
	owned := detachChannels(channels)
	m.status = next
	m.failureStopping = false
	if clearText {
		m.currentText = ""
		m.channels = nil
	}
	m.closeLogLocked()
	if len(stopErrors) > 0 {
		m.lastError = errors.Join(stopErrors...).Error()
	}
	snapshot, listeners := m.snapshotAndListenersLocked()
	m.finishStopRequestLocked()
	m.mu.Unlock()
	closeRecognizers(owned)
	m.closePunctuator()
	m.transitionMu.Unlock()
	m.publish(snapshot, listeners)
	return errors.Join(stopErrors...)
}

// startChannels starts every input's recognizer before its audio source, so no
// captured audio arrives at a recognizer that is not ready for it. A failure
// stops whatever already started; the caller rolls the run back.
func startChannels(ctx context.Context, channels []*channel) error {
	for _, current := range channels {
		if err := current.recognizer.Start(ctx); err != nil {
			stopChannels(channels)
			return fmt.Errorf("start recognizer: %w", err)
		}
		if err := ctx.Err(); err != nil {
			stopChannels(channels)
			return err
		}
		if current.source == nil {
			continue
		}
		if err := current.source.Start(ctx); err != nil {
			stopChannels(channels)
			return fmt.Errorf("start audio source: %w", err)
		}
		if err := ctx.Err(); err != nil {
			stopChannels(channels)
			return err
		}
	}
	return nil
}

func stopChannels(channels []*channel) []error {
	var stopErrors []error
	for _, current := range channels {
		if current.source == nil {
			continue
		}
		if err := current.source.Stop(); err != nil {
			stopErrors = append(stopErrors, fmt.Errorf("stop audio source: %w", err))
		}
	}
	for _, current := range channels {
		if current.recognizer == nil {
			continue
		}
		if err := current.recognizer.Stop(); err != nil {
			stopErrors = append(stopErrors, fmt.Errorf("stop recognizer: %w", err))
		}
	}
	return stopErrors
}

// detachChannels releases the plugin instances of a finished run and returns
// the recognizers this run created, which nothing else can close.
func detachChannels(channels []*channel) []plugin.Recognizer {
	var owned []plugin.Recognizer
	for _, current := range channels {
		if current.owned && current.recognizer != nil {
			owned = append(owned, current.recognizer)
		}
		current.source = nil
		current.recognizer = nil
		current.owned = false
	}
	return owned
}

func closeRecognizers(recognizers []plugin.Recognizer) {
	for _, recognizer := range recognizers {
		_ = recognizer.Close()
	}
}

func carryCaptions(previous, next []*channel) {
	texts := make(map[string]string, len(previous))
	for _, current := range previous {
		if current.key != "" && current.text != "" {
			texts[current.key] = current.text
		}
	}
	for _, current := range next {
		current.text = texts[current.key]
	}
}

func componentName(component, label string) string {
	if label == "" {
		return component
	}
	return component + "（" + label + "）"
}

// openPunctuator prepares the punctuation pass for one run. A configured model
// that cannot be loaded degrades to the rule pass and returns the reason, so a
// broken path costs commas instead of the whole session.
func (m *Manager) openPunctuator() error {
	mode, modeErr := punctuation.ParseMode(m.settings.String(config.PunctuationMode))
	punctuator, err := punctuation.New(punctuation.Config{
		Mode:       mode,
		ModelPath:  strings.TrimSpace(m.settings.String(config.PunctuationModelPath)),
		NumThreads: punctuationThreads,
	})
	if err != nil {
		punctuator = punctuation.Rules()
	}
	m.punctuationMu.Lock()
	previous := m.punctuator
	m.punctuator = punctuator
	m.punctuationMu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}
	return errors.Join(modeErr, err)
}

func (m *Manager) punctuate(text string) string {
	m.punctuationMu.Lock()
	defer m.punctuationMu.Unlock()
	if m.punctuator == nil {
		return text
	}
	return m.punctuator.Punctuate(text)
}

func (m *Manager) closePunctuator() {
	m.punctuationMu.Lock()
	punctuator := m.punctuator
	m.punctuator = nil
	m.punctuationMu.Unlock()
	if punctuator != nil {
		_ = punctuator.Close()
	}
}

func (m *Manager) beginStopRequestLocked() context.CancelFunc {
	m.stopRequests++
	m.stopping = true
	return m.cancel
}

func (m *Manager) finishStopRequestLocked() {
	m.stopRequests--
	m.stopping = m.stopRequests > 0
}

func (m *Manager) rollbackStart(runID uint64, channels []*channel) {
	m.mu.Lock()
	var owned []plugin.Recognizer
	if m.activeRun == runID {
		m.activeRun++
		if m.cancel != nil {
			m.cancel()
		}
		m.cancel = nil
		owned = detachChannels(channels)
		m.channels = nil
		m.starting = false
		m.status = Stopped
		m.closeLogLocked()
	}
	snapshot, listeners := m.snapshotAndListenersLocked()
	m.mu.Unlock()
	closeRecognizers(owned)
	m.closePunctuator()
	m.publish(snapshot, listeners)
}

// channelLocked resolves a callback's channel, or nil once the run it belongs
// to has been fenced.
func (m *Manager) channelLocked(runID uint64, index int) *channel {
	if m.activeRun != runID || index < 0 || index >= len(m.channels) {
		return nil
	}
	return m.channels[index]
}

func (m *Manager) onPartial(runID uint64, index int, text plugin.Text) {
	if text.Time.IsZero() {
		text.Time = m.now()
	}
	m.mu.Lock()
	current := m.channelLocked(runID, index)
	if current == nil {
		m.mu.Unlock()
		return
	}
	current.text = text.Text
	current.sentenceOpen = text.Text != ""
	m.currentText = text.Text
	var notification *Notification
	if !current.notified {
		for _, word := range m.sensitiveWords {
			if strings.Contains(text.Text, word) {
				current.notified = true
				value := Notification{
					Title:   "敏感词",
					Message: componentName("检测到敏感词："+word, current.label),
					Level:   NotificationWarning,
				}
				notification = &value
				break
			}
		}
	}
	snapshot, listeners := m.liveSnapshotAndListenersLocked()
	notificationListeners := m.notificationListenersLocked()
	m.mu.Unlock()
	m.publish(snapshot, listeners)
	if notification != nil {
		m.publishNotification(*notification, notificationListeners)
	}
}

func (m *Manager) onFinal(runID uint64, index int, text plugin.Text) {
	if text.Text == "" {
		return
	}
	if text.Time.IsZero() {
		text.Time = m.now()
	}
	// Punctuate outside the lock: a model pass costs tens of milliseconds and
	// must not hold up the caption window's snapshots for that long.
	text.Text = m.punctuate(text.Text)
	m.mu.Lock()
	current := m.channelLocked(runID, index)
	if current == nil || text.Text == "" {
		m.mu.Unlock()
		return
	}
	m.finishLocked(current, text)
	current.text = text.Text
	m.currentText = text.Text
	snapshot, listeners := m.snapshotAndListenersLocked()
	m.mu.Unlock()
	m.publish(snapshot, listeners)
}

// onAudioDropped reports the first dropped chunk of an input. Backpressure
// arrives in bursts, so repeating it for every chunk would bury the caption
// window in warnings.
func (m *Manager) onAudioDropped(runID uint64, index int, err error) {
	m.mu.Lock()
	current := m.channelLocked(runID, index)
	if current == nil || current.dropped {
		m.mu.Unlock()
		return
	}
	current.dropped = true
	label := current.label
	m.mu.Unlock()
	m.onPluginWarning(runID, componentName("音频数据", label),
		fmt.Errorf("识别跟不上音频，丢掉了一小段声音（%w）；同时识别多路音频时更容易发生", err))
}

func (m *Manager) onPluginError(runID uint64, component string, err error) {
	if err == nil {
		return
	}
	m.mu.Lock()
	if m.activeRun != runID {
		m.mu.Unlock()
		return
	}
	m.lastError = component + "：" + err.Error()
	stopAfterError := (m.status == Running || m.starting) && !m.failureStopping && !m.stopping
	if stopAfterError {
		m.failureStopping = true
		cancel := m.beginStopRequestLocked()
		if cancel != nil {
			cancel()
		}
	}
	notification := Notification{Title: "插件异常", Message: m.lastError, Level: NotificationError}
	snapshot, listeners := m.snapshotAndListenersLocked()
	notificationListeners := m.notificationListenersLocked()
	m.mu.Unlock()
	m.publish(snapshot, listeners)
	m.publishNotification(notification, notificationListeners)
	if stopAfterError {
		go func() {
			_ = m.stopReserved(Stopped, false)
		}()
	}
}

// onPluginWarning records and publishes a recoverable plugin problem without
// touching the run lifecycle. In particular, a warning never cancels an
// in-flight Start and never reserves or launches a stop for a running plugin.
func (m *Manager) onPluginWarning(runID uint64, component string, err error) {
	if err == nil {
		return
	}
	m.mu.Lock()
	if m.activeRun != runID {
		m.mu.Unlock()
		return
	}
	m.lastError = component + "：" + err.Error()
	notification := Notification{Title: "插件警告", Message: m.lastError, Level: NotificationWarning}
	snapshot, listeners := m.snapshotAndListenersLocked()
	notificationListeners := m.notificationListenersLocked()
	m.mu.Unlock()
	m.publish(snapshot, listeners)
	m.publishNotification(notification, notificationListeners)
}

// finishLocked commits one sentence. A nil channel records an unattributed
// sentence, which is what a session with a single unlabeled input produces.
func (m *Manager) finishLocked(current *channel, text plugin.Text) {
	if text.Text == "" {
		return
	}
	m.nextHistoryID++
	entry := HistoryEntry{ID: m.nextHistoryID, Time: text.Time, Text: text.Text}
	if current != nil {
		entry.Speaker = current.label
		entry.Channel = current.key
		current.text = ""
		current.sentenceOpen = false
		current.notified = false
	}
	m.history = append(m.history, entry)
	if len(m.history) > maxHistoryEntries {
		trimmed := make([]HistoryEntry, maxHistoryEntries)
		copy(trimmed, m.history[len(m.history)-maxHistoryEntries:])
		m.history = trimmed
	}
	if m.logFile != nil && !m.logDisabled {
		if _, err := fmt.Fprintf(m.logFile, "%s\n", FormatTranscriptLine(entry)); err != nil {
			m.logDisabled = true
			m.lastError = "写入识别日志失败：" + err.Error()
			_ = m.logFile.Close()
			m.logFile = nil
		}
	}
	m.currentText = ""
}

// FormatTranscriptLine keeps the legacy "time: text" layout for an
// unattributed sentence and names the speaker once a session captures labeled
// inputs. The recognition log and "copy all" share it so a copied transcript
// reads like the file on disk.
func FormatTranscriptLine(entry HistoryEntry) string {
	stamp := entry.Time.Format("15:04:05")
	if entry.Speaker == "" {
		return stamp + ": " + entry.Text
	}
	return stamp + " " + entry.Speaker + ": " + entry.Text
}

func (m *Manager) runTimer(ctx context.Context, runID uint64, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.mu.Lock()
			if m.activeRun != runID || m.status != Running {
				m.mu.Unlock()
				return
			}
			m.runningSeconds++
			snapshot, listeners := m.liveSnapshotAndListenersLocked()
			m.mu.Unlock()
			m.publish(snapshot, listeners)
		}
	}
}

func (m *Manager) openLogLocked() error {
	path := strings.TrimSpace(m.settings.String(config.GeneralResultLogPath))
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("create recognition log directory: %w", err)
	}
	fileName := m.now().Format("06-01-02-15-04-05") + ".txt"
	file, err := os.OpenFile(filepath.Join(path, fileName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open recognition log: %w", err)
	}
	m.logFile = file
	return nil
}

func (m *Manager) closeLogLocked() {
	if m.logFile != nil {
		_ = m.logFile.Close()
		m.logFile = nil
	}
}

func (m *Manager) snapshotLocked() Snapshot {
	history := append([]HistoryEntry{}, m.history...)
	return Snapshot{
		Status:         m.status,
		RunningSeconds: m.runningSeconds,
		Text:           m.currentText,
		Channels:       m.channelStatesLocked(),
		History:        history,
		LastError:      m.lastError,
	}
}

func (m *Manager) channelStatesLocked() []ChannelState {
	states := make([]ChannelState, 0, len(m.channels))
	for _, current := range m.channels {
		states = append(states, ChannelState{Key: current.key, Label: current.label, Text: current.text})
	}
	return states
}

func (m *Manager) snapshotAndListenersLocked() (Snapshot, []Listener) {
	listeners := make([]Listener, 0, len(m.listeners))
	for _, listener := range m.listeners {
		listeners = append(listeners, listener)
	}
	return m.snapshotLocked(), listeners
}

func (m *Manager) liveSnapshotAndListenersLocked() (Snapshot, []Listener) {
	listeners := make([]Listener, 0, len(m.listeners))
	for _, listener := range m.listeners {
		listeners = append(listeners, listener)
	}
	return m.liveSnapshotLocked(), listeners
}

func (m *Manager) liveSnapshotLocked() Snapshot {
	return Snapshot{
		Status:         m.status,
		RunningSeconds: m.runningSeconds,
		Text:           m.currentText,
		Channels:       m.channelStatesLocked(),
		LastError:      m.lastError,
		liveOnly:       true,
	}
}

func (m *Manager) notificationListenersLocked() []NotificationListener {
	listeners := make([]NotificationListener, 0, len(m.notifications))
	for _, listener := range m.notifications {
		listeners = append(listeners, listener)
	}
	return listeners
}

func (m *Manager) publish(snapshot Snapshot, listeners []Listener) {
	for _, listener := range listeners {
		listener(snapshot)
	}
}

func (m *Manager) publishNotification(notification Notification, listeners []NotificationListener) {
	for _, listener := range listeners {
		listener(notification)
	}
}

func splitSensitiveWords(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '，' || r == '\n' || r == '\r'
	})
}
