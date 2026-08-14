package sherpancnn

import "context"

// streamingEngine isolates the native Go binding from lifecycle and result
// semantics. A Recognizer calls every method from one owning goroutine.
type streamingEngine interface {
	AcceptWaveform(sampleRate int, samples []float32) error
	InputFinished() error
	IsReady() bool
	Decode() error
	IsEndpoint() bool
	Result() string
	Reset() error
	Close() error
}

type engineFactory interface {
	Available() bool
	New(context.Context, Config) (streamingEngine, error)
}
