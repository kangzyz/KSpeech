// Package sherpaonnx implements KSpeech's in-process streaming sherpa-onnx
// recognizer. Native support is opt-in through the sherpa build tag.
package sherpaonnx

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/kangzyz/KSpeech/internal/plugin"
)

var (
	// ErrClosed indicates an attempt to reuse a recognizer after Close.
	ErrClosed = errors.New("sherpa-onnx recognizer is closed")
	// ErrNotRunning indicates that Feed was called outside an active run.
	ErrNotRunning = errors.New("sherpa-onnx recognizer is not running")
	// ErrAudioQueueFull indicates that Feed refused audio instead of blocking an
	// audio capture callback behind native inference.
	ErrAudioQueueFull = fmt.Errorf("sherpa-onnx %w", plugin.ErrAudioBackpressure)
	// ErrConfigWhileRunning protects native resources from live reconfiguration.
	ErrConfigWhileRunning = errors.New("cannot load sherpa-onnx config while running")
	// ErrBackendFailure marks an asynchronous native recognition failure.
	ErrBackendFailure = errors.New("sherpa-onnx backend failure")
)

// Option customizes a Recognizer without coupling it to application services.
type Option func(*Recognizer)

// WithModelResolver supplies legacy resource-ID resolution for Config.Model.
// With no resolver, the encoder/decoder/joiner/tokens fields are used directly.
func WithModelResolver(resolver ModelResolver) Option {
	return func(recognizer *Recognizer) {
		recognizer.resolver = resolver
	}
}

// Recognizer converts normalized 16 kHz mono float32 PCM into partial and final
// text. One worker goroutine exclusively owns its streamingEngine and, in a
// sherpa build, all underlying C resources.
type Recognizer struct {
	metadata plugin.Metadata

	mu       sync.RWMutex
	config   Config
	resolver ModelResolver
	factory  engineFactory
	run      *recognizerRun
	closed   bool

	callbacksMu sync.RWMutex
	callbacks   plugin.RecognizerCallbacks
}

type recognizerRun struct {
	ctx    context.Context
	cancel context.CancelFunc
	audio  chan []float32

	startDone chan struct{}
	done      chan struct{}
	startErr  error
	runErr    error
	stopping  bool // guarded by Recognizer.mu
}

var _ plugin.Recognizer = (*Recognizer)(nil)

// New creates a streaming recognizer with the caller-supplied plugin metadata.
func New(metadata plugin.Metadata, options ...Option) *Recognizer {
	recognizer := &Recognizer{
		metadata: metadata,
		config:   DefaultConfig(),
		factory:  compiledEngineFactory{},
	}
	for _, option := range options {
		if option != nil {
			option(recognizer)
		}
	}
	return recognizer
}

// Metadata returns the metadata supplied to New.
func (r *Recognizer) Metadata() plugin.Metadata { return r.metadata }

// Available reports whether native sherpa support was compiled into this
// binary. It does not validate model files or runtime shared-library search
// paths; those are build/startup concerns respectively.
func (r *Recognizer) Available() bool { return r.factory.Available() }

// Init validates its context. Native resources are intentionally created by
// the Start worker so the same goroutine owns every native operation.
func (r *Recognizer) Init(ctx context.Context) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return ErrClosed
	}
	return nil
}

// LoadConfig accepts the legacy C# JSON shape and optional Go tuning fields.
// Configuration can only change while stopped.
func (r *Recognizer) LoadConfig(data []byte) error {
	config, err := decodeConfig(data)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrClosed
	}
	if r.run != nil {
		return ErrConfigWhileRunning
	}
	r.config = config
	return nil
}

// Config returns a point-in-time copy of the loaded configuration.
func (r *Recognizer) Config() Config {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.config
}

// NeedsAudio is true: Feed supplies normalized 16 kHz mono float32 PCM.
func (r *Recognizer) NeedsAudio() bool { return true }

// SetCallbacks atomically replaces partial, final, and asynchronous error
// callbacks. The worker reads the latest callback for every event.
func (r *Recognizer) SetCallbacks(callbacks plugin.RecognizerCallbacks) {
	r.callbacksMu.Lock()
	r.callbacks = callbacks
	r.callbacksMu.Unlock()
}

// Running reports whether a worker is starting, running, or stopping.
func (r *Recognizer) Running() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.run != nil
}

// Start creates the native recognizer on its owning worker goroutine. Repeated
// calls for the same active run wait for and return the same startup result.
func (r *Recognizer) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	for {
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			return ErrClosed
		}
		if !r.factory.Available() {
			r.mu.Unlock()
			return ErrUnavailable
		}
		if current := r.run; current != nil {
			if current.stopping {
				done := current.done
				r.mu.Unlock()
				select {
				case <-done:
					continue
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			started := current.startDone
			r.mu.Unlock()
			select {
			case <-started:
				return current.startErr
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		config := r.config
		resolver := r.resolver
		factory := r.factory
		runContext, cancel := context.WithCancel(ctx)
		run := &recognizerRun{
			ctx:       runContext,
			cancel:    cancel,
			audio:     make(chan []float32, config.AudioQueueCapacity),
			startDone: make(chan struct{}),
			done:      make(chan struct{}),
		}
		r.run = run
		r.mu.Unlock()

		go r.serve(run, config, resolver, factory)
		select {
		case <-run.startDone:
			return run.startErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Feed copies and queues one PCM chunk without waiting for inference. The
// bounded queue applies backpressure as ErrAudioQueueFull instead of stalling
// the audio capture callback. An empty chunk is a no-op for an active run.
func (r *Recognizer) Feed(samples []float32) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return ErrClosed
	}
	run := r.run
	if run == nil || run.stopping {
		return ErrNotRunning
	}

	select {
	case <-run.startDone:
		if run.startErr != nil {
			return run.startErr
		}
	default:
		return ErrNotRunning
	}
	select {
	case <-run.ctx.Done():
		return ErrNotRunning
	default:
	}
	if len(samples) == 0 {
		return nil
	}
	ownedSamples := append([]float32(nil), samples...)
	select {
	case run.audio <- ownedSamples:
		return nil
	case <-run.ctx.Done():
		return ErrNotRunning
	case <-run.done:
		return ErrNotRunning
	default:
		return ErrAudioQueueFull
	}
}

// Stop flushes and releases the active stream. It is safe to call repeatedly
// and concurrently.
func (r *Recognizer) Stop() error {
	r.mu.Lock()
	run := r.run
	if run == nil {
		r.mu.Unlock()
		return nil
	}
	run.stopping = true
	run.cancel()
	done := run.done
	r.mu.Unlock()
	<-done
	return run.runErr
}

// Close permanently closes the recognizer after stopping its active stream.
// Repeated calls wait for the same cleanup and return the same run error.
func (r *Recognizer) Close() error {
	r.mu.Lock()
	r.closed = true
	run := r.run
	if run == nil {
		r.mu.Unlock()
		return nil
	}
	run.stopping = true
	run.cancel()
	done := run.done
	r.mu.Unlock()
	<-done
	return run.runErr
}

func (r *Recognizer) serve(run *recognizerRun, config Config, resolver ModelResolver, factory engineFactory) {
	var engine streamingEngine
	startReported := false
	reportStart := func(err error) {
		if startReported {
			return
		}
		run.startErr = err
		close(run.startDone)
		startReported = true
	}
	defer func() {
		run.cancel()
		if engine != nil {
			if err := engine.Close(); err != nil {
				wrapped := fmt.Errorf("%w: close native engine: %v", ErrBackendFailure, err)
				run.runErr = errors.Join(run.runErr, wrapped)
				if startReported && run.startErr == nil {
					r.emitError(wrapped)
				}
			}
		}
		if !startReported {
			reportStart(fmt.Errorf("%w: worker exited during startup", ErrBackendFailure))
		}
		r.mu.Lock()
		if r.run == run {
			r.run = nil
		}
		r.mu.Unlock()
		close(run.done)
	}()

	prepared, err := prepareConfig(run.ctx, config, resolver)
	if err != nil {
		reportStart(err)
		return
	}
	engine, err = factory.New(run.ctx, prepared)
	if err != nil {
		reportStart(err)
		return
	}
	if engine == nil {
		reportStart(fmt.Errorf("%w: factory returned a nil engine", ErrBackendFailure))
		return
	}
	if err := run.ctx.Err(); err != nil {
		reportStart(err)
		return
	}
	reportStart(nil)

	processor := newResultProcessor(prepared.MaxTextLength)
	for {
		select {
		case <-run.ctx.Done():
			if err := r.finishRun(run, engine, processor); err != nil {
				r.reportRunError(run, err)
			}
			return
		default:
		}
		select {
		case <-run.ctx.Done():
			if err := r.finishRun(run, engine, processor); err != nil {
				r.reportRunError(run, err)
			}
			return
		case samples := <-run.audio:
			if err := r.processSamples(engine, processor, samples); err != nil {
				r.reportRunError(run, err)
				return
			}
		}
	}
}

func (r *Recognizer) processSamples(engine streamingEngine, processor *resultProcessor, samples []float32) error {
	if err := engine.AcceptWaveform(plugin.AudioSampleRate, samples); err != nil {
		return fmt.Errorf("accept waveform: %w", err)
	}
	if err := r.decodeReady(engine); err != nil {
		return err
	}
	return r.publishCurrent(engine, processor)
}

func (r *Recognizer) decodeReady(engine streamingEngine) error {
	for engine.IsReady() {
		if err := engine.Decode(); err != nil {
			return fmt.Errorf("decode stream: %w", err)
		}
	}
	return nil
}

func (r *Recognizer) publishCurrent(engine streamingEngine, processor *resultProcessor) error {
	update := processor.update(engine.Result(), engine.IsEndpoint())
	r.publish(update)
	if update.reset {
		if err := engine.Reset(); err != nil {
			return fmt.Errorf("reset stream: %w", err)
		}
	}
	return nil
}

func (r *Recognizer) finishStream(engine streamingEngine, processor *resultProcessor) error {
	if err := engine.InputFinished(); err != nil {
		return fmt.Errorf("finish stream input: %w", err)
	}
	if err := r.decodeReady(engine); err != nil {
		return err
	}
	r.publish(processor.finish(engine.Result()))
	return nil
}

func (r *Recognizer) finishRun(run *recognizerRun, engine streamingEngine, processor *resultProcessor) error {
	// Stop first prevents new Feed calls, then the worker drains the bounded
	// queue so already accepted audio is not lost at the utterance boundary.
	for {
		select {
		case samples := <-run.audio:
			if err := r.processSamples(engine, processor, samples); err != nil {
				return err
			}
		default:
			return r.finishStream(engine, processor)
		}
	}
}

func (r *Recognizer) publish(update recognitionUpdate) {
	now := time.Now()
	if update.emitPartial {
		r.callbacksMu.RLock()
		callback := r.callbacks.Partial
		r.callbacksMu.RUnlock()
		if callback != nil {
			callback(plugin.Text{Time: now, Text: update.partial})
		}
	}
	if update.emitFinal {
		r.callbacksMu.RLock()
		callback := r.callbacks.Final
		r.callbacksMu.RUnlock()
		if callback != nil {
			callback(plugin.Text{Time: now, Text: update.final})
		}
	}
}

func (r *Recognizer) reportRunError(run *recognizerRun, err error) {
	wrapped := fmt.Errorf("%w: %v", ErrBackendFailure, err)
	run.runErr = errors.Join(run.runErr, wrapped)
	r.emitError(wrapped)
}

func (r *Recognizer) emitError(err error) {
	r.callbacksMu.RLock()
	callback := r.callbacks.Error
	r.callbacksMu.RUnlock()
	if callback != nil {
		callback(err)
	}
}
