//go:build !sherpancnn || !cgo || (windows && !amd64 && !386)

package sherpancnn

import (
	"context"
	"errors"
)

// ErrUnavailable is returned when KSpeech was built without the opt-in
// sherpa-ncnn native backend.
var ErrUnavailable = errors.New("sherpa-ncnn recognizer is unavailable: rebuild with -tags sherpancnn and CGO_ENABLED=1")

type compiledEngineFactory struct{}

func (compiledEngineFactory) Available() bool { return false }

func (compiledEngineFactory) New(context.Context, Config) (streamingEngine, error) {
	return nil, ErrUnavailable
}
