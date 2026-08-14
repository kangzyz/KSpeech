//go:build sherpa && cgo

package punctuation

import (
	"fmt"
	"strings"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

const modelBackendAvailable = true

// modelPunctuator wraps sherpa-onnx's offline CT-Transformer punctuation model.
// The model is offline by design: it sees one finished sentence at a time, not
// the partial results, so a caption is only rewritten once.
type modelPunctuator struct {
	handle   *sherpa.OfflinePunctuation
	fallback Punctuator
}

func newModelPunctuator(config Config) (Punctuator, error) {
	debug := 0
	if config.Debug {
		debug = 1
	}
	handle := sherpa.NewOfflinePunctuation(&sherpa.OfflinePunctuationConfig{
		Model: sherpa.OfflinePunctuationModelConfig{
			CtTransformer: config.ModelPath,
			NumThreads:    config.NumThreads,
			Debug:         debug,
			Provider:      config.Provider,
		},
	})
	if handle == nil {
		return nil, fmt.Errorf("%w: load CT-Transformer model %q", ErrModelFile, config.ModelPath)
	}
	return &modelPunctuator{handle: handle, fallback: Rules()}, nil
}

func (p *modelPunctuator) Punctuate(text string) string {
	trimmed := strings.TrimSpace(text)
	if p.handle == nil || trimmed == "" {
		return p.fallback.Punctuate(text)
	}
	// The native call reports failure as an empty string, which would otherwise
	// erase the caption.
	result := strings.TrimSpace(p.handle.AddPunct(trimmed))
	if result == "" {
		return p.fallback.Punctuate(trimmed)
	}
	// The model punctuates the inside of the sentence but does not always close
	// it; the rules add the missing final mark and leave the rest alone.
	return p.fallback.Punctuate(result)
}

func (p *modelPunctuator) Close() error {
	if p.handle != nil {
		sherpa.DeleteOfflinePunc(p.handle)
		p.handle = nil
	}
	return nil
}
