package sherpaonnx

import "context"

// streamingEngine is intentionally private. Its implementation wraps native C
// resources in sherpa builds, while tests use a deterministic in-memory engine.
// A Recognizer calls every method from one worker goroutine.
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
