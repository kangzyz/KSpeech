// Package command implements the legacy external-command speech recognizer.
package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kangzyz/KSpeech/internal/plugin"
)

const (
	processStopTimeout = 3 * time.Second
	pipeDrainTimeout   = 2 * time.Second
)

var (
	// ErrAlreadyRunning indicates that Start was called for a live process.
	ErrAlreadyRunning = errors.New("command recognizer is already running")
	// ErrConfigWhileRunning indicates an unsafe attempt to replace live config.
	ErrConfigWhileRunning = errors.New("cannot load command recognizer config while running")
	// ErrCommandRequired indicates that Config.Command is empty.
	ErrCommandRequired = errors.New("command recognizer command is required")
	// ErrUnexpectedExit marks an external process exit not caused by Stop or by
	// cancellation of the Start context.
	ErrUnexpectedExit = errors.New("command recognizer process exited unexpectedly")
	// ErrStopTimeout indicates that a process did not stop within the bounded wait.
	ErrStopTimeout = errors.New("timed out stopping command recognizer process")
)

// UnexpectedExitError describes an external process that ended without a
// user-initiated stop. Cause is nil when the process returned exit code zero.
type UnexpectedExitError struct {
	Cause error
}

func (e *UnexpectedExitError) Error() string {
	if e.Cause == nil {
		return ErrUnexpectedExit.Error()
	}
	return fmt.Sprintf("%s: %v", ErrUnexpectedExit, e.Cause)
}

// Unwrap lets callers match both ErrUnexpectedExit and an exec.ExitError cause.
func (e *UnexpectedExitError) Unwrap() []error {
	if e.Cause == nil {
		return []error{ErrUnexpectedExit}
	}
	return []error{ErrUnexpectedExit, e.Cause}
}

type processRun struct {
	cmd         *exec.Cmd
	guard       *processGuard
	stdout      *os.File
	stderr      *os.File
	log         *os.File
	stopping    atomic.Bool
	readers     sync.WaitGroup
	readersDone chan struct{}
	done        chan struct{}
	reportOnce  sync.Once
}

// Recognizer obtains speech results from an external process's UTF-8 stdout.
type Recognizer struct {
	metadata plugin.Metadata

	lifecycleMu sync.Mutex
	stateMu     sync.RWMutex
	config      Config
	run         *processRun

	callbacksMu sync.RWMutex
	callbacks   plugin.RecognizerCallbacks
}

var _ plugin.Recognizer = (*Recognizer)(nil)

// New creates a command recognizer with caller-supplied plugin metadata.
func New(metadata plugin.Metadata) *Recognizer {
	return &Recognizer{metadata: metadata}
}

// Metadata returns the metadata supplied to New.
func (r *Recognizer) Metadata() plugin.Metadata { return r.metadata }

// Available reports that the built-in implementation is available. Whether a
// configured executable can start is validated by Start.
func (r *Recognizer) Available() bool { return true }

// Init validates the initialization context. The external process is started
// separately by Start.
func (r *Recognizer) Init(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// Close releases the external process, if any.
func (r *Recognizer) Close() error { return r.Stop() }

// LoadConfig loads the original .NET JSON field shape. Configuration may only
// be replaced while the recognizer is stopped.
func (r *Recognizer) LoadConfig(data []byte) error {
	config, err := decodeConfig(data)
	if err != nil {
		return err
	}

	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if r.run != nil {
		return ErrConfigWhileRunning
	}
	r.config = config
	return nil
}

// Config returns a point-in-time copy of the active configuration.
func (r *Recognizer) Config() Config {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return r.config
}

// NeedsAudio is false because the external program captures its own audio.
func (r *Recognizer) NeedsAudio() bool { return false }

// Feed intentionally ignores application audio for legacy compatibility.
func (r *Recognizer) Feed([]float32) error { return nil }

// SetCallbacks atomically replaces result and error callbacks.
func (r *Recognizer) SetCallbacks(callbacks plugin.RecognizerCallbacks) {
	r.callbacksMu.Lock()
	r.callbacks = callbacks
	r.callbacksMu.Unlock()
}

// Running reports whether an external process is still owned by this recognizer.
func (r *Recognizer) Running() bool {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return r.run != nil
}

// Start starts the configured external command and its stdout/stderr drains.
func (r *Recognizer) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	r.lifecycleMu.Lock()
	var startWarning error
	defer func() {
		r.lifecycleMu.Unlock()
		if startWarning != nil {
			r.emitWarning(startWarning)
		}
	}()

	r.stateMu.RLock()
	if r.run != nil {
		r.stateMu.RUnlock()
		return ErrAlreadyRunning
	}
	config := r.config
	r.stateMu.RUnlock()

	if config.Command == "" {
		return ErrCommandRequired
	}
	cmd := exec.Command(config.Command)
	if err := configureProcess(cmd, config.Arguments); err != nil {
		return err
	}
	logFile, err := openLogFile(config.LogFile)
	if err != nil {
		// A diagnostic sink must never prevent recognition. Report the degraded
		// logging state, then keep draining stderr to io.Discard so the child
		// cannot block on a full pipe.
		startWarning = err
		logFile = nil
	}
	closeLog := true
	defer func() {
		if closeLog && logFile != nil {
			_ = logFile.Close()
		}
	}()

	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create command stdout pipe: %w", err)
	}
	defer func() {
		if stdoutWriter != nil {
			_ = stdoutWriter.Close()
		}
		if closeLog {
			_ = stdoutReader.Close()
		}
	}()
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create command stderr pipe: %w", err)
	}
	defer func() {
		if stderrWriter != nil {
			_ = stderrWriter.Close()
		}
		if closeLog {
			_ = stderrReader.Close()
		}
	}()

	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter
	cmd.Dir = config.WorkingDirectory
	guard, err := newProcessGuard()
	if err != nil {
		return fmt.Errorf("create command recognizer process guard: %w", err)
	}
	guardOwned := true
	defer func() {
		if guardOwned {
			_ = closeProcessGuard(guard)
		}
	}()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start command recognizer process: %w", err)
	}
	_ = stdoutWriter.Close()
	stdoutWriter = nil
	_ = stderrWriter.Close()
	stderrWriter = nil
	if err := attachProcessGuard(guard, cmd); err != nil {
		cleanupErr := terminateProcess(cmd, guard)
		if cleanupErr != nil && cmd.Process != nil {
			leafErr := cmd.Process.Kill()
			if leafErr == nil || errors.Is(leafErr, os.ErrProcessDone) {
				cleanupErr = nil
			} else {
				cleanupErr = errors.Join(cleanupErr, leafErr)
			}
		}
		_ = cmd.Wait()
		closeErr := closeProcessGuard(guard)
		guardOwned = false
		return errors.Join(
			fmt.Errorf("attach command recognizer process guard: %w", err),
			cleanupErr,
			closeErr,
		)
	}

	run := &processRun{
		cmd:         cmd,
		guard:       guard,
		stdout:      stdoutReader,
		stderr:      stderrReader,
		log:         logFile,
		readersDone: make(chan struct{}),
		done:        make(chan struct{}),
	}
	r.stateMu.Lock()
	r.run = run
	r.stateMu.Unlock()
	closeLog = false
	guardOwned = false

	run.readers.Add(2)
	go r.readStdout(run)
	go r.readStderr(run)
	go func() {
		run.readers.Wait()
		close(run.readersDone)
	}()
	go r.waitForExit(run)
	go r.stopWhenContextDone(ctx, run)
	return nil
}

// Stop terminates the current process tree and waits for reader cleanup. It is
// idempotent and never reports the requested termination as an abnormal exit.
func (r *Recognizer) Stop() error {
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	r.stateMu.RLock()
	run := r.run
	r.stateMu.RUnlock()
	if run == nil {
		return nil
	}
	return r.stopRunLocked(run)
}

func (r *Recognizer) stopWhenContextDone(ctx context.Context, run *processRun) {
	select {
	case <-ctx.Done():
		r.lifecycleMu.Lock()
		r.stateMu.RLock()
		isCurrent := r.run == run
		r.stateMu.RUnlock()
		if isCurrent {
			_ = r.stopRunLocked(run)
		}
		r.lifecycleMu.Unlock()
	case <-run.done:
	}
}

func (r *Recognizer) stopRunLocked(run *processRun) error {
	run.stopping.Store(true)
	terminateErr := terminateProcess(run.cmd, run.guard)
	if terminateErr != nil && run.cmd.Process != nil {
		leafErr := run.cmd.Process.Kill()
		if leafErr == nil || errors.Is(leafErr, os.ErrProcessDone) {
			terminateErr = nil
		} else {
			terminateErr = errors.Join(terminateErr, leafErr)
		}
	}
	timer := time.NewTimer(processStopTimeout)
	defer timer.Stop()
	select {
	case <-run.done:
		return terminateErr
	case <-timer.C:
	}

	// Closing our read ends releases stuck reader goroutines if a descendant
	// inherited a pipe despite process-tree termination.
	_ = run.stdout.Close()
	_ = run.stderr.Close()
	if run.cmd.Process != nil {
		_ = run.cmd.Process.Kill()
	}
	select {
	case <-run.done:
		return terminateErr
	case <-time.After(time.Second):
		if terminateErr != nil {
			return errors.Join(ErrStopTimeout, terminateErr)
		}
		return ErrStopTimeout
	}
}

func (r *Recognizer) readStdout(run *processRun) {
	defer run.readers.Done()
	defer run.stdout.Close()
	parser := newStdoutParser(r.emitPartial, r.emitFinal)
	if _, err := io.Copy(parser, run.stdout); err != nil && !run.stopping.Load() {
		r.reportRunError(run, fmt.Errorf("read command recognizer stdout: %w", err))
	}
}

func (r *Recognizer) readStderr(run *processRun) {
	defer run.readers.Done()
	defer run.stderr.Close()
	destination := io.Writer(io.Discard)
	if run.log != nil {
		destination = run.log
	}
	if _, err := io.Copy(destination, run.stderr); err != nil && !run.stopping.Load() {
		r.reportRunError(run, fmt.Errorf("drain command recognizer stderr: %w", err))
	}
}

func (r *Recognizer) waitForExit(run *processRun) {
	waitErr := run.cmd.Wait()
	// Releasing the process guard must happen before waiting for pipe readers:
	// descendants can inherit stdout/stderr and otherwise keep those readers
	// alive after their direct parent has exited.
	guardErr := closeProcessGuard(run.guard)
	if guardErr != nil {
		waitErr = errors.Join(waitErr, fmt.Errorf("release command recognizer process guard: %w", guardErr))
	}
	timer := time.NewTimer(pipeDrainTimeout)
	select {
	case <-run.readersDone:
		if !timer.Stop() {
			<-timer.C
		}
	case <-timer.C:
		_ = run.stdout.Close()
		_ = run.stderr.Close()
		<-run.readersDone
	}
	if run.log != nil {
		_ = run.log.Close()
	}

	r.stateMu.Lock()
	if r.run == run {
		r.run = nil
	}
	r.stateMu.Unlock()

	if !run.stopping.Load() {
		r.reportRunError(run, &UnexpectedExitError{Cause: waitErr})
	}
	close(run.done)
}

func (r *Recognizer) reportRunError(run *processRun, err error) {
	run.reportOnce.Do(func() { r.emitError(err) })
}

func (r *Recognizer) emitPartial(text string) {
	r.callbacksMu.RLock()
	callback := r.callbacks.Partial
	r.callbacksMu.RUnlock()
	if callback != nil {
		callback(plugin.Text{Time: time.Now(), Text: text})
	}
}

func (r *Recognizer) emitFinal(text string) {
	r.callbacksMu.RLock()
	callback := r.callbacks.Final
	r.callbacksMu.RUnlock()
	if callback != nil {
		callback(plugin.Text{Time: time.Now(), Text: text})
	}
}

func (r *Recognizer) emitError(err error) {
	r.callbacksMu.RLock()
	callback := r.callbacks.Error
	r.callbacksMu.RUnlock()
	if callback != nil {
		callback(err)
	}
}

func (r *Recognizer) emitWarning(err error) {
	r.callbacksMu.RLock()
	callback := r.callbacks.Warning
	r.callbacksMu.RUnlock()
	if callback != nil {
		callback(err)
	}
}

func openLogFile(path string) (*os.File, error) {
	if path == "" {
		return nil, nil
	}
	directory := filepath.Dir(path)
	if directory != "." {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return nil, fmt.Errorf("create command recognizer log directory: %w", err)
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open command recognizer stderr log: %w", err)
	}
	return file, nil
}
