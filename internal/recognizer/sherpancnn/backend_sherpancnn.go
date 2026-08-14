//go:build !windows && sherpancnn && cgo

package sherpancnn

import (
	"context"
	"errors"
	"fmt"

	sherpa "github.com/k2-fsa/sherpa-ncnn-go/sherpa_ncnn"
	"github.com/kangzyz/KSpeech/internal/plugin"
)

// ErrUnavailable remains defined in native builds so callers can use
// errors.Is without build-specific source.
var ErrUnavailable = errors.New("sherpa-ncnn recognizer is unavailable")

var errCreateNativeRecognizer = errors.New("create native sherpa-ncnn recognizer")

type compiledEngineFactory struct{}

func (compiledEngineFactory) Available() bool { return true }

func (compiledEngineFactory) New(ctx context.Context, config Config) (streamingEngine, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if config.UseVulkanCompute {
		// The official cross-platform Go wrapper sets the C field to zero
		// unconditionally. Windows has a direct C-API backend; other platforms
		// remain explicitly CPU-only instead of silently ignoring the request.
		return nil, ErrVulkanUnsupported
	}
	enableEndpoint := 0
	if config.Endpoint {
		enableEndpoint = 1
	}
	nativeConfig := sherpa.RecognizerConfig{
		Feat: sherpa.FeatureConfig{
			SampleRate: plugin.AudioSampleRate,
			FeatureDim: config.FeatureDim,
		},
		Model: sherpa.ModelConfig{
			EncoderParam: config.EncoderParam,
			EncoderBin:   config.EncoderBin,
			DecoderParam: config.DecoderParam,
			DecoderBin:   config.DecoderBin,
			JoinerParam:  config.JoinerParam,
			JoinerBin:    config.JoinerBin,
			Tokens:       config.Tokens,
			NumThreads:   config.NumThreads,
		},
		Decoder: sherpa.DecoderConfig{
			DecodingMethod: config.DecodingMethod,
			NumActivePaths: config.NumActivePaths,
		},
		EnableEndpoint:          enableEndpoint,
		Rule1MinTrailingSilence: config.Rule1MinTrailingSilence,
		Rule2MinTrailingSilence: config.Rule2MinTrailingSilence,
		Rule3MinUtteranceLength: config.Rule3MinUtteranceLength,
	}
	recognizer := sherpa.NewRecognizer(&nativeConfig)
	if recognizer == nil {
		return nil, errCreateNativeRecognizer
	}
	stream := sherpa.NewStream(recognizer)
	if stream == nil {
		sherpa.DeleteRecognizer(recognizer)
		return nil, fmt.Errorf("%w: create online stream", errCreateNativeRecognizer)
	}
	return &nativeEngine{recognizer: recognizer, stream: stream}, nil
}

type nativeEngine struct {
	recognizer *sherpa.Recognizer
	stream     *sherpa.Stream
	finished   bool
	closed     bool
}

func (e *nativeEngine) AcceptWaveform(sampleRate int, samples []float32) error {
	if len(samples) == 0 {
		return nil // The upstream wrapper indexes samples[0].
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

func (e *nativeEngine) Result() string {
	result := e.recognizer.GetResult(e.stream)
	if result == nil {
		return ""
	}
	return result.Text
}

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
	if e.stream != nil {
		sherpa.DeleteStream(e.stream)
		e.stream = nil
	}
	if e.recognizer != nil {
		sherpa.DeleteRecognizer(e.recognizer)
		e.recognizer = nil
	}
	e.closed = true
	return nil
}
