package sherpancnn

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/kangzyz/KSpeech/internal/plugin"
)

type fakeStep struct {
	text      string
	endpoint  bool
	decodeErr error
}

type fakeEngine struct {
	mu sync.Mutex

	steps       []fakeStep
	nextStep    int
	pending     *fakeStep
	ready       bool
	result      string
	endpoint    bool
	finishText  string
	accepted    [][]float32
	resetCount  int
	finishCount int
	closeCount  int

	acceptEntered chan struct{}
	acceptBlock   chan struct{}
	acceptedEvent chan struct{}
	acceptOnce    sync.Once
}

func (e *fakeEngine) AcceptWaveform(sampleRate int, samples []float32) error {
	if sampleRate != plugin.AudioSampleRate {
		return errors.New("unexpected sample rate")
	}
	if e.acceptEntered != nil {
		e.acceptOnce.Do(func() { close(e.acceptEntered) })
	}
	if e.acceptBlock != nil {
		<-e.acceptBlock
	}
	e.mu.Lock()
	e.accepted = append(e.accepted, append([]float32(nil), samples...))
	if e.nextStep < len(e.steps) {
		step := e.steps[e.nextStep]
		e.nextStep++
		e.pending = &step
		e.ready = true
	}
	e.mu.Unlock()
	if e.acceptedEvent != nil {
		select {
		case e.acceptedEvent <- struct{}{}:
		default:
		}
	}
	return nil
}

func (e *fakeEngine) InputFinished() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.finishCount++
	if e.finishText != "" {
		e.result = e.finishText
		e.endpoint = false
	}
	return nil
}

func (e *fakeEngine) IsReady() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ready
}

func (e *fakeEngine) Decode() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pending == nil {
		return errors.New("decode without pending input")
	}
	step := *e.pending
	e.pending = nil
	e.ready = false
	if step.decodeErr != nil {
		return step.decodeErr
	}
	e.result = step.text
	e.endpoint = step.endpoint
	return nil
}

func (e *fakeEngine) IsEndpoint() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.endpoint
}

func (e *fakeEngine) Result() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.result
}

func (e *fakeEngine) Reset() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.resetCount++
	e.result = ""
	e.endpoint = false
	return nil
}

func (e *fakeEngine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closeCount++
	return nil
}

func (e *fakeEngine) counts() (accepted, reset, finished, closed int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.accepted), e.resetCount, e.finishCount, e.closeCount
}

func (e *fakeEngine) firstAccepted() []float32 {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.accepted) == 0 {
		return nil
	}
	return append([]float32(nil), e.accepted[0]...)
}

type fakeFactory struct {
	mu       sync.Mutex
	engine   streamingEngine
	err      error
	newCount int
	configs  []Config
}

func (f *fakeFactory) Available() bool { return true }

func (f *fakeFactory) New(ctx context.Context, config Config) (streamingEngine, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.newCount++
	f.configs = append(f.configs, config)
	f.mu.Unlock()
	return f.engine, f.err
}

func (f *fakeFactory) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.newCount
}

func newRecognizerWithFake(t *testing.T, engine *fakeEngine, mutate func(*Config), options ...Option) (*Recognizer, *fakeFactory) {
	t.Helper()
	config := configWithNCNNFiles(makeNCNNModelFiles(t))
	if mutate != nil {
		mutate(&config)
	}
	factory := &fakeFactory{engine: engine}
	recognizer := New(plugin.Metadata{ID: "test-ncnn", Name: "Test NCNN"}, options...)
	recognizer.config = config
	recognizer.factory = factory
	return recognizer, factory
}

type callbackEvent struct {
	kind string
	text string
}

func TestRecognizerLifecycleFeedPartialFinalAndEndpoint(t *testing.T) {
	engine := &fakeEngine{steps: []fakeStep{
		{text: "你"},
		{text: "你好", endpoint: true},
	}}
	recognizer, factory := newRecognizerWithFake(t, engine, nil)
	if !recognizer.NeedsAudio() || recognizer.Metadata().ID != "test-ncnn" {
		t.Fatalf("plugin contract mismatch: metadata=%#v", recognizer.Metadata())
	}
	events := make(chan callbackEvent, 8)
	recognizer.SetCallbacks(plugin.RecognizerCallbacks{
		Partial: func(text plugin.Text) { events <- callbackEvent{kind: "partial", text: text.Text} },
		Final:   func(text plugin.Text) { events <- callbackEvent{kind: "final", text: text.Text} },
	})
	if err := recognizer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := recognizer.Start(context.Background()); err != nil {
		t.Fatalf("idempotent Start: %v", err)
	}
	if factory.count() != 1 {
		t.Fatalf("factory calls = %d, want 1", factory.count())
	}
	if err := recognizer.LoadConfig([]byte(`{}`)); !errors.Is(err, ErrConfigWhileRunning) {
		t.Fatalf("LoadConfig while running = %v", err)
	}

	if err := recognizer.Feed([]float32{0.1}); err != nil {
		t.Fatal(err)
	}
	assertCallbackEvent(t, events, callbackEvent{kind: "partial", text: "你"})
	if err := recognizer.Feed([]float32{0.2}); err != nil {
		t.Fatal(err)
	}
	assertCallbackEvent(t, events, callbackEvent{kind: "partial", text: "你好"})
	assertCallbackEvent(t, events, callbackEvent{kind: "final", text: "你好"})

	if err := recognizer.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := recognizer.Stop(); err != nil {
		t.Fatalf("idempotent Stop: %v", err)
	}
	accepted, reset, finished, closed := engine.counts()
	if accepted != 2 || reset != 1 || finished != 1 || closed != 1 {
		t.Fatalf("engine counts = accepted:%d reset:%d finished:%d closed:%d", accepted, reset, finished, closed)
	}
	if err := recognizer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := recognizer.Start(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Start after Close = %v", err)
	}
}

func TestStopFlushesOutstandingPartialAsFinal(t *testing.T) {
	engine := &fakeEngine{steps: []fakeStep{{text: "尾巴"}}, finishText: "尾巴"}
	recognizer, _ := newRecognizerWithFake(t, engine, nil)
	events := make(chan callbackEvent, 4)
	recognizer.SetCallbacks(plugin.RecognizerCallbacks{
		Partial: func(text plugin.Text) { events <- callbackEvent{kind: "partial", text: text.Text} },
		Final:   func(text plugin.Text) { events <- callbackEvent{kind: "final", text: text.Text} },
	})
	if err := recognizer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := recognizer.Feed([]float32{0.1}); err != nil {
		t.Fatal(err)
	}
	assertCallbackEvent(t, events, callbackEvent{kind: "partial", text: "尾巴"})
	if err := recognizer.Stop(); err != nil {
		t.Fatal(err)
	}
	assertCallbackEvent(t, events, callbackEvent{kind: "final", text: "尾巴"})
}

func TestFeedIsBoundedNonBlockingAndCopiesSamples(t *testing.T) {
	engine := &fakeEngine{
		acceptEntered: make(chan struct{}),
		acceptBlock:   make(chan struct{}),
		acceptedEvent: make(chan struct{}, 1),
	}
	recognizer, _ := newRecognizerWithFake(t, engine, func(config *Config) {
		config.AudioQueueCapacity = 1
	})
	if err := recognizer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := []float32{0.25, -0.5}
	if err := recognizer.Feed(first); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, engine.acceptEntered, "engine accepting first chunk")
	first[0] = 99
	if err := recognizer.Feed([]float32{0.75}); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := recognizer.Feed([]float32{1}); !errors.Is(err, ErrAudioQueueFull) {
		t.Fatalf("full queue error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("full Feed blocked for %v", elapsed)
	}
	close(engine.acceptBlock)
	waitSignal(t, engine.acceptedEvent, "engine recording first chunk")
	if got, want := engine.firstAccepted(), []float32{0.25, -0.5}; !reflect.DeepEqual(got, want) {
		t.Fatalf("accepted samples = %v, want %v", got, want)
	}
	if err := recognizer.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestStartResolvesLegacyModelResource(t *testing.T) {
	files := makeNCNNModelFiles(t)
	called := make(chan string, 1)
	resolver := func(ctx context.Context, model string) (ModelFiles, error) {
		called <- model
		return files, nil
	}
	recognizer, factory := newRecognizerWithFake(t, &fakeEngine{}, func(config *Config) {
		config.Model = "legacy-ncnn-model"
	}, WithModelResolver(resolver))
	if err := recognizer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case model := <-called:
		if model != "legacy-ncnn-model" {
			t.Fatalf("resolved model = %q", model)
		}
	case <-time.After(time.Second):
		t.Fatal("model resolver was not called")
	}
	factory.mu.Lock()
	prepared := factory.configs[0]
	factory.mu.Unlock()
	if prepared.EncoderParam != files.EncoderParam || prepared.JoinerBin != files.JoinerBin || prepared.Tokens != files.Tokens {
		t.Fatalf("resolved config = %#v", prepared)
	}
	if err := recognizer.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeFailureUsesAsynchronousErrorCallback(t *testing.T) {
	engine := &fakeEngine{steps: []fakeStep{{decodeErr: errors.New("decode exploded")}}}
	recognizer, _ := newRecognizerWithFake(t, engine, nil)
	errorsSeen := make(chan error, 1)
	recognizer.SetCallbacks(plugin.RecognizerCallbacks{Error: func(err error) { errorsSeen <- err }})
	if err := recognizer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := recognizer.Feed([]float32{0.1}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errorsSeen:
		if !errors.Is(err, ErrBackendFailure) {
			t.Fatalf("callback error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for error callback")
	}
	deadline := time.Now().Add(time.Second)
	for recognizer.Running() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if recognizer.Running() {
		t.Fatal("worker remained running after backend failure")
	}
}

func assertCallbackEvent(t *testing.T, events <-chan callbackEvent, want callbackEvent) {
	t.Helper()
	select {
	case got := <-events:
		if got != want {
			t.Fatalf("event = %#v, want %#v", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %#v", want)
	}
}

func waitSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}
