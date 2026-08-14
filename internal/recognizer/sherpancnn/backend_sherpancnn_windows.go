//go:build windows && (amd64 || 386) && sherpancnn && cgo

package sherpancnn

/*
#include <stdint.h>
#include <stdlib.h>

// These declarations mirror the public ABI in
// github.com/k2-fsa/sherpa-ncnn-go-windows/c-api.h. The module is blank-
// imported below so its architecture-specific CGO linker flags remain the
// single source of truth for library discovery.
typedef struct SherpaNcnnModelConfig {
  const char *encoder_param;
  const char *encoder_bin;
  const char *decoder_param;
  const char *decoder_bin;
  const char *joiner_param;
  const char *joiner_bin;
  const char *tokens;
  int32_t use_vulkan_compute;
  int32_t num_threads;
} SherpaNcnnModelConfig;

typedef struct SherpaNcnnDecoderConfig {
  const char *decoding_method;
  int32_t num_active_paths;
} SherpaNcnnDecoderConfig;

typedef struct SherpaNcnnFeatureExtractorConfig {
  float sampling_rate;
  int32_t feature_dim;
} SherpaNcnnFeatureExtractorConfig;

typedef struct SherpaNcnnRecognizerConfig {
  SherpaNcnnFeatureExtractorConfig feat_config;
  SherpaNcnnModelConfig model_config;
  SherpaNcnnDecoderConfig decoder_config;
  int32_t enable_endpoint;
  float rule1_min_trailing_silence;
  float rule2_min_trailing_silence;
  float rule3_min_utterance_length;
} SherpaNcnnRecognizerConfig;

typedef struct SherpaNcnnResult {
  const char *text;
  const char *tokens;
  float *timestamps;
  int32_t count;
} SherpaNcnnResult;

typedef struct SherpaNcnnRecognizer SherpaNcnnRecognizer;
typedef struct SherpaNcnnStream SherpaNcnnStream;

SherpaNcnnRecognizer *CreateRecognizer(const SherpaNcnnRecognizerConfig *config);
void DestroyRecognizer(SherpaNcnnRecognizer *recognizer);
SherpaNcnnStream *CreateStream(SherpaNcnnRecognizer *recognizer);
void DestroyStream(SherpaNcnnStream *stream);
void AcceptWaveform(SherpaNcnnStream *stream, float sample_rate,
                    const float *samples, int32_t count);
int32_t IsReady(SherpaNcnnRecognizer *recognizer, SherpaNcnnStream *stream);
void Decode(SherpaNcnnRecognizer *recognizer, SherpaNcnnStream *stream);
SherpaNcnnResult *GetResult(SherpaNcnnRecognizer *recognizer,
                            SherpaNcnnStream *stream);
void DestroyResult(const SherpaNcnnResult *result);
void Reset(SherpaNcnnRecognizer *recognizer, SherpaNcnnStream *stream);
void InputFinished(SherpaNcnnStream *stream);
int32_t IsEndpoint(SherpaNcnnRecognizer *recognizer, SherpaNcnnStream *stream);
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"unsafe"

	_ "github.com/k2-fsa/sherpa-ncnn-go-windows"
	"github.com/kangzyz/KSpeech/internal/plugin"
)

// ErrUnavailable remains defined in native builds so callers can use
// errors.Is without build-specific source.
var ErrUnavailable = errors.New("sherpa-ncnn recognizer is unavailable")

var (
	errCreateNativeRecognizer = errors.New("create native sherpa-ncnn recognizer")
	errNativeEngineClosed     = errors.New("sherpa-ncnn native engine is closed")
	errNativeInputFinished    = errors.New("sherpa-ncnn native input is already finished")
)

type compiledEngineFactory struct{}

func (compiledEngineFactory) Available() bool { return true }

func (compiledEngineFactory) New(ctx context.Context, config Config) (streamingEngine, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	nativeConfig, releaseConfig := makeNativeRecognizerConfig(config)
	defer releaseConfig()

	recognizer := C.CreateRecognizer(&nativeConfig)
	if recognizer == nil {
		return nil, errCreateNativeRecognizer
	}
	stream := C.CreateStream(recognizer)
	if stream == nil {
		C.DestroyRecognizer(recognizer)
		return nil, fmt.Errorf("%w: create online stream", errCreateNativeRecognizer)
	}
	return &nativeEngine{recognizer: recognizer, stream: stream}, nil
}

func makeNativeRecognizerConfig(config Config) (C.SherpaNcnnRecognizerConfig, func()) {
	nativeConfig := C.SherpaNcnnRecognizerConfig{}
	nativeConfig.feat_config.sampling_rate = C.float(plugin.AudioSampleRate)
	nativeConfig.feat_config.feature_dim = C.int32_t(config.FeatureDim)

	nativeConfig.model_config.encoder_param = C.CString(config.EncoderParam)
	nativeConfig.model_config.encoder_bin = C.CString(config.EncoderBin)
	nativeConfig.model_config.decoder_param = C.CString(config.DecoderParam)
	nativeConfig.model_config.decoder_bin = C.CString(config.DecoderBin)
	nativeConfig.model_config.joiner_param = C.CString(config.JoinerParam)
	nativeConfig.model_config.joiner_bin = C.CString(config.JoinerBin)
	nativeConfig.model_config.tokens = C.CString(config.Tokens)
	nativeConfig.model_config.use_vulkan_compute = C.int32_t(nativeBool(config.UseVulkanCompute))
	nativeConfig.model_config.num_threads = C.int32_t(config.NumThreads)

	nativeConfig.decoder_config.decoding_method = C.CString(config.DecodingMethod)
	nativeConfig.decoder_config.num_active_paths = C.int32_t(config.NumActivePaths)
	nativeConfig.enable_endpoint = C.int32_t(nativeBool(config.Endpoint))
	nativeConfig.rule1_min_trailing_silence = C.float(config.Rule1MinTrailingSilence)
	nativeConfig.rule2_min_trailing_silence = C.float(config.Rule2MinTrailingSilence)
	nativeConfig.rule3_min_utterance_length = C.float(config.Rule3MinUtteranceLength)

	cStrings := []*C.char{
		nativeConfig.model_config.encoder_param,
		nativeConfig.model_config.encoder_bin,
		nativeConfig.model_config.decoder_param,
		nativeConfig.model_config.decoder_bin,
		nativeConfig.model_config.joiner_param,
		nativeConfig.model_config.joiner_bin,
		nativeConfig.model_config.tokens,
		nativeConfig.decoder_config.decoding_method,
	}
	return nativeConfig, func() {
		for _, value := range cStrings {
			C.free(unsafe.Pointer(value))
		}
	}
}

func nativeBool(value bool) int32 {
	if value {
		return 1
	}
	return 0
}

type nativeEngine struct {
	recognizer *C.SherpaNcnnRecognizer
	stream     *C.SherpaNcnnStream
	finished   bool
	closed     bool
}

func (e *nativeEngine) AcceptWaveform(sampleRate int, samples []float32) error {
	if len(samples) == 0 {
		return nil
	}
	if err := e.ensureOpen(); err != nil {
		return err
	}
	if e.finished {
		return errNativeInputFinished
	}
	C.AcceptWaveform(
		e.stream,
		C.float(sampleRate),
		(*C.float)(unsafe.Pointer(&samples[0])),
		C.int32_t(len(samples)),
	)
	return nil
}

func (e *nativeEngine) InputFinished() error {
	if err := e.ensureOpen(); err != nil {
		return err
	}
	if !e.finished {
		C.InputFinished(e.stream)
		e.finished = true
	}
	return nil
}

func (e *nativeEngine) IsReady() bool {
	return e != nil && !e.closed && e.recognizer != nil && e.stream != nil &&
		C.IsReady(e.recognizer, e.stream) == 1
}

func (e *nativeEngine) IsEndpoint() bool {
	return e != nil && !e.closed && e.recognizer != nil && e.stream != nil &&
		C.IsEndpoint(e.recognizer, e.stream) == 1
}

func (e *nativeEngine) Result() string {
	if e == nil || e.closed || e.recognizer == nil || e.stream == nil {
		return ""
	}
	result := C.GetResult(e.recognizer, e.stream)
	if result == nil {
		return ""
	}
	defer C.DestroyResult(result)
	if result.text == nil {
		return ""
	}
	return C.GoString(result.text)
}

func (e *nativeEngine) Decode() error {
	if err := e.ensureOpen(); err != nil {
		return err
	}
	C.Decode(e.recognizer, e.stream)
	return nil
}

func (e *nativeEngine) Reset() error {
	if err := e.ensureOpen(); err != nil {
		return err
	}
	if e.finished {
		return errNativeInputFinished
	}
	C.Reset(e.recognizer, e.stream)
	return nil
}

func (e *nativeEngine) Close() error {
	if e == nil || e.closed {
		return nil
	}
	if e.stream != nil {
		C.DestroyStream(e.stream)
		e.stream = nil
	}
	if e.recognizer != nil {
		C.DestroyRecognizer(e.recognizer)
		e.recognizer = nil
	}
	e.finished = true
	e.closed = true
	return nil
}

func (e *nativeEngine) ensureOpen() error {
	if e == nil || e.closed || e.recognizer == nil || e.stream == nil {
		return errNativeEngineClosed
	}
	return nil
}
