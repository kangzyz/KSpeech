//go:build !sherpa || !cgo

package sherpaonnx

import (
	"context"
	"errors"
)

// ErrUnavailable is returned by native lifecycle operations when this package
// was built without the sherpa build tag (or with cgo disabled).
var ErrUnavailable = errors.New("sherpa-onnx recognizer is unavailable: rebuild with -tags sherpa and CGO_ENABLED=1")

type compiledEngineFactory struct{}

func (compiledEngineFactory) Available() bool { return false }

func (compiledEngineFactory) New(context.Context, Config) (streamingEngine, error) {
	return nil, ErrUnavailable
}
