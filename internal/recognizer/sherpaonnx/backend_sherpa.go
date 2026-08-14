//go:build sherpa && cgo

package sherpaonnx

import (
	"context"
	"errors"
	"fmt"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
	"github.com/kangzyz/KSpeech/internal/plugin"
)

// ErrUnavailable is retained in sherpa builds so callers can use errors.Is
// without build-specific source. A tagged binary normally fails to load before
// main if its platform sherpa/onnxruntime shared libraries are unavailable.
var ErrUnavailable = errors.New("sherpa-onnx recognizer is unavailable")

var errCreateNativeRecognizer = errors.New("create native sherpa-onnx recognizer")

type compiledEngineFactory struct{}

func (compiledEngineFactory) Available() bool { return true }

func (compiledEngineFactory) New(ctx context.Context, config Config) (streamingEngine, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	debug := 0
	if config.Debug {
		debug = 1
	}
	enableEndpoint := 0
	if config.Endpoint {
		enableEndpoint = 1
	}
	nativeConfig := sherpa.OnlineRecognizerConfig{
		FeatConfig: sherpa.FeatureConfig{
			SampleRate: plugin.AudioSampleRate,
			FeatureDim: config.FeatureDim,
		},
		ModelConfig: sherpa.OnlineModelConfig{
			Transducer: sherpa.OnlineTransducerModelConfig{
				Encoder: config.Encoder,
				Decoder: config.Decoder,
				Joiner:  config.Joiner,
			},
			Tokens:     config.Tokens,
			NumThreads: config.NumThreads,
			Provider:   config.Provider,
			Debug:      debug,
			// Without these the C API falls back to cjkchar, which splits an
			// English hotword into single letters the model never emits.
			ModelingUnit: config.ModelingUnit,
			BpeVocab:     config.BpeVocab,
		},
		DecodingMethod:          config.DecodingMethod,
		MaxActivePaths:          config.MaxActivePaths,
		EnableEndpoint:          enableEndpoint,
		Rule1MinTrailingSilence: config.Rule1MinTrailingSilence,
		Rule2MinTrailingSilence: config.Rule2MinTrailingSilence,
		Rule3MinUtteranceLength: config.Rule3MinUtteranceLength,
		HotwordsFile:            config.HotwordsFile,
		HotwordsScore:           config.HotwordsScore,
		RuleFsts:                config.RuleFsts,
		RuleFars:                config.RuleFars,
	}
	recognizer := sherpa.NewOnlineRecognizer(&nativeConfig)
	if recognizer == nil {
		return nil, errCreateNativeRecognizer
	}
	stream := sherpa.NewOnlineStream(recognizer)
	if stream == nil {
		sherpa.DeleteOnlineRecognizer(recognizer)
		return nil, fmt.Errorf("%w: create online stream", errCreateNativeRecognizer)
	}
	return &nativeEngine{recognizer: recognizer, stream: stream}, nil
}

type nativeEngine struct {
	recognizer *sherpa.OnlineRecognizer
	stream     *sherpa.OnlineStream
	finished   bool
	closed     bool
}

func (e *nativeEngine) AcceptWaveform(sampleRate int, samples []float32) error {
	if len(samples) == 0 {
		return nil
	}
	e.stream.AcceptWaveform(sampleRate, samples)
	return nil
}

func (e *nativeEngine) InputFinished() error {
	if !e.finished {
		e.stream.InputFinished()
		e.finished = true
	}
	return nil
}

func (e *nativeEngine) IsReady() bool    { return e.recognizer.IsReady(e.stream) }
func (e *nativeEngine) IsEndpoint() bool { return e.recognizer.IsEndpoint(e.stream) }
func (e *nativeEngine) Result() string   { return e.recognizer.GetResult(e.stream).Text }

func (e *nativeEngine) Decode() error {
	e.recognizer.Decode(e.stream)
	return nil
}

func (e *nativeEngine) Reset() error {
	e.recognizer.Reset(e.stream)
	return nil
}

func (e *nativeEngine) Close() error {
	if e.closed {
		return nil
	}
	// A stream belongs to its recognizer and must be released first.
	if e.stream != nil {
		sherpa.DeleteOnlineStream(e.stream)
		e.stream = nil
	}
	if e.recognizer != nil {
		sherpa.DeleteOnlineRecognizer(e.recognizer)
		e.recognizer = nil
	}
	e.closed = true
	return nil
}
