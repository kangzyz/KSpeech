//go:build !sherpa || !cgo

package punctuation

const modelBackendAvailable = false

// newModelPunctuator is never reached: New rejects ModeModel before calling it
// in a build without the native sherpa-onnx backend.
func newModelPunctuator(Config) (Punctuator, error) { return nil, ErrUnavailable }
