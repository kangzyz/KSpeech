package sherpancnn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

const (
	defaultFeatureDim         = 80
	defaultNumThreads         = 1
	defaultAudioQueueCapacity = 32
	defaultMaxTextLength      = 80
)

var (
	// ErrInvalidConfig indicates that a JSON configuration cannot be used by
	// the streaming sherpa-ncnn recognizer.
	ErrInvalidConfig = errors.New("invalid sherpa-ncnn recognizer config")
	// ErrModelFile indicates that a required NCNN model file is missing or is
	// not a regular file.
	ErrModelFile = errors.New("invalid sherpa-ncnn model file")
	// ErrVulkanUnsupported is returned by native backends that cannot map the
	// Vulkan request to the sherpa-ncnn C API. The Windows backend supports it;
	// the official Go wrappers used on other platforms currently do not.
	ErrVulkanUnsupported = errors.New("sherpa-ncnn Vulkan is unavailable on this platform")
)

// Config accepts the original .NET plugin's lower_snake_case fields and a
// small set of optional Go tuning fields. UseVulkanCompute maps to the native
// C API on Windows; non-Windows builds reject it explicitly.
type Config struct {
	Model        string `json:"model"`
	EncoderParam string `json:"encoder_param"`
	EncoderBin   string `json:"encoder_bin"`
	DecoderParam string `json:"decoder_param"`
	DecoderBin   string `json:"decoder_bin"`
	JoinerParam  string `json:"joiner_param"`
	JoinerBin    string `json:"joiner_bin"`
	Tokens       string `json:"tokens"`

	NumThreads       int    `json:"NumThreads"`
	FeatureDim       int    `json:"FeatureDim"`
	DecodingMethod   string `json:"DecodingMethod"`
	NumActivePaths   int    `json:"NumActivePaths"`
	UseVulkanCompute bool   `json:"UseVulkanCompute"`

	Endpoint                bool    `json:"Endpoint"`
	Rule1MinTrailingSilence float32 `json:"Rule1MinTrailingSilence"`
	Rule2MinTrailingSilence float32 `json:"Rule2MinTrailingSilence"`
	Rule3MinUtteranceLength float32 `json:"Rule3MinUtteranceLength"`
	MaxTextLength           int     `json:"MaxTextLength"`
	AudioQueueCapacity      int     `json:"AudioQueueCapacity"`
}

// ModelFiles is the seven-file streaming transducer model set expected by
// sherpa-ncnn.
type ModelFiles struct {
	EncoderParam string
	EncoderBin   string
	DecoderParam string
	DecoderBin   string
	JoinerParam  string
	JoinerBin    string
	Tokens       string
}

// ModelResolver expands a legacy resource ID from Config.Model into concrete
// model paths. Keeping this adapter local avoids coupling the recognizer to the
// resource manager's manifest representation.
type ModelResolver func(context.Context, string) (ModelFiles, error)

// DefaultConfig preserves the legacy C# paths, decoding, endpoint, and thread
// defaults. Windows also preserves the old plugin's Vulkan request; other
// platforms default to CPU because their official Go wrappers force that mode.
func DefaultConfig() Config {
	return Config{
		EncoderParam:            `ncnn-model\encoder.param`,
		EncoderBin:              `ncnn-model\encoder.bin`,
		DecoderParam:            `ncnn-model\decoder.param`,
		DecoderBin:              `ncnn-model\decoder.bin`,
		JoinerParam:             `ncnn-model\joiner.param`,
		JoinerBin:               `ncnn-model\joiner.bin`,
		Tokens:                  `ncnn-model\tokens.txt`,
		NumThreads:              defaultNumThreads,
		FeatureDim:              defaultFeatureDim,
		DecodingMethod:          "greedy_search",
		NumActivePaths:          4,
		UseVulkanCompute:        defaultUseVulkanCompute,
		Endpoint:                true,
		Rule1MinTrailingSilence: 2.4,
		Rule2MinTrailingSilence: 1.2,
		Rule3MinUtteranceLength: 20,
		MaxTextLength:           defaultMaxTextLength,
		AudioQueueCapacity:      defaultAudioQueueCapacity,
	}
}

func decodeConfig(data []byte) (Config, error) {
	config := DefaultConfig()
	if len(bytes.TrimSpace(data)) == 0 {
		return config, nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return Config{}, fmt.Errorf("%w: decode JSON: %v", ErrInvalidConfig, err)
	}
	if fields == nil {
		return Config{}, fmt.Errorf("%w: expected a JSON object", ErrInvalidConfig)
	}

	seen := make(map[string]string)
	for originalName, raw := range fields {
		name := canonicalConfigKey(normalizeConfigKey(originalName))
		if previous, ok := seen[name]; ok {
			return Config{}, fmt.Errorf("%w: duplicate fields %q and %q", ErrInvalidConfig, previous, originalName)
		}
		seen[name] = originalName

		var err error
		switch name {
		case "model":
			err = decodeString(raw, &config.Model)
		case "encoderparam":
			err = decodeString(raw, &config.EncoderParam)
		case "encoderbin":
			err = decodeString(raw, &config.EncoderBin)
		case "decoderparam":
			err = decodeString(raw, &config.DecoderParam)
		case "decoderbin":
			err = decodeString(raw, &config.DecoderBin)
		case "joinerparam":
			err = decodeString(raw, &config.JoinerParam)
		case "joinerbin":
			err = decodeString(raw, &config.JoinerBin)
		case "tokens":
			err = decodeString(raw, &config.Tokens)
		case "numthreads":
			err = decodeInt(raw, &config.NumThreads)
		case "featuredim":
			err = decodeInt(raw, &config.FeatureDim)
		case "decodingmethod":
			err = decodeString(raw, &config.DecodingMethod)
		case "numactivepaths":
			err = decodeInt(raw, &config.NumActivePaths)
		case "usevulkancompute":
			err = decodeBool(raw, &config.UseVulkanCompute)
		case "endpoint":
			err = decodeBool(raw, &config.Endpoint)
		case "rule1mintrailingsilence":
			err = decodeFloat32(raw, &config.Rule1MinTrailingSilence)
		case "rule2mintrailingsilence":
			err = decodeFloat32(raw, &config.Rule2MinTrailingSilence)
		case "rule3minutterancelength":
			err = decodeFloat32(raw, &config.Rule3MinUtteranceLength)
		case "maxtextlength":
			err = decodeInt(raw, &config.MaxTextLength)
		case "audioqueuecapacity":
			err = decodeInt(raw, &config.AudioQueueCapacity)
		default:
			continue // Preserve forward compatibility with plugin-specific fields.
		}
		if err != nil {
			return Config{}, fmt.Errorf("%w: %s: %v", ErrInvalidConfig, originalName, err)
		}
	}
	if err := validateConfig(config); err != nil {
		return Config{}, err
	}
	return config, nil
}

func canonicalConfigKey(name string) string {
	switch name {
	case "token":
		return "tokens"
	case "maxactivepaths":
		return "numactivepaths"
	case "vulkan":
		return "usevulkancompute"
	case "enableendpoint":
		return "endpoint"
	default:
		return name
	}
}

func normalizeConfigKey(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, value)
}

func decodeString(raw json.RawMessage, target *string) error {
	if err := json.Unmarshal(raw, target); err != nil {
		return errors.New("expected a string")
	}
	return nil
}

func decodeInt(raw json.RawMessage, target *int) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var number json.Number
	if err := decoder.Decode(&number); err != nil {
		return errors.New("expected an integer")
	}
	value, err := strconv.ParseInt(number.String(), 10, 32)
	if err != nil {
		return errors.New("expected an integer")
	}
	*target = int(value)
	return nil
}

func decodeFloat32(raw json.RawMessage, target *float32) error {
	var value float32
	if err := json.Unmarshal(raw, &value); err != nil {
		return errors.New("expected a number")
	}
	*target = value
	return nil
}

func decodeBool(raw json.RawMessage, target *bool) error {
	var value bool
	if err := json.Unmarshal(raw, &value); err == nil {
		*target = value
		return nil
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err == nil {
		switch number.String() {
		case "0":
			*target = false
			return nil
		case "1":
			*target = true
			return nil
		}
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		switch strings.ToLower(strings.TrimSpace(text)) {
		case "false", "0":
			*target = false
			return nil
		case "true", "1":
			*target = true
			return nil
		}
	}
	return errors.New("expected true, false, 1, or 0")
}

func validateConfig(config Config) error {
	if config.NumThreads <= 0 {
		return fmt.Errorf("%w: NumThreads must be positive", ErrInvalidConfig)
	}
	if config.FeatureDim <= 0 {
		return fmt.Errorf("%w: FeatureDim must be positive", ErrInvalidConfig)
	}
	switch config.DecodingMethod {
	case "greedy_search":
	case "modified_beam_search":
		if config.NumActivePaths <= 0 {
			return fmt.Errorf("%w: NumActivePaths must be positive for modified_beam_search", ErrInvalidConfig)
		}
	default:
		return fmt.Errorf("%w: unsupported DecodingMethod %q", ErrInvalidConfig, config.DecodingMethod)
	}
	if config.Rule1MinTrailingSilence < 0 || config.Rule2MinTrailingSilence < 0 || config.Rule3MinUtteranceLength < 0 {
		return fmt.Errorf("%w: endpoint rule values must not be negative", ErrInvalidConfig)
	}
	if config.MaxTextLength < 0 {
		return fmt.Errorf("%w: MaxTextLength must not be negative", ErrInvalidConfig)
	}
	if config.AudioQueueCapacity <= 0 {
		return fmt.Errorf("%w: AudioQueueCapacity must be positive", ErrInvalidConfig)
	}
	return nil
}

func prepareConfig(ctx context.Context, config Config, resolver ModelResolver) (Config, error) {
	if err := validateConfig(config); err != nil {
		return Config{}, err
	}
	if strings.TrimSpace(config.Model) != "" && resolver != nil {
		files, err := resolver(ctx, config.Model)
		if err != nil {
			return Config{}, fmt.Errorf("resolve sherpa-ncnn model %q: %w", config.Model, err)
		}
		config.EncoderParam = files.EncoderParam
		config.EncoderBin = files.EncoderBin
		config.DecoderParam = files.DecoderParam
		config.DecoderBin = files.DecoderBin
		config.JoinerParam = files.JoinerParam
		config.JoinerBin = files.JoinerBin
		config.Tokens = files.Tokens
	}

	paths := []struct {
		name  string
		value *string
	}{
		{name: "encoder_param", value: &config.EncoderParam},
		{name: "encoder_bin", value: &config.EncoderBin},
		{name: "decoder_param", value: &config.DecoderParam},
		{name: "decoder_bin", value: &config.DecoderBin},
		{name: "joiner_param", value: &config.JoinerParam},
		{name: "joiner_bin", value: &config.JoinerBin},
		{name: "tokens", value: &config.Tokens},
	}
	for _, item := range paths {
		if err := ctx.Err(); err != nil {
			return Config{}, err
		}
		path := normalizeLegacyPath(strings.TrimSpace(*item.value))
		if path == "" || path == "." {
			return Config{}, fmt.Errorf("%w: %s path is empty", ErrModelFile, item.name)
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return Config{}, fmt.Errorf("%w: resolve %s path %q: %v", ErrModelFile, item.name, path, err)
		}
		info, err := os.Stat(absolute)
		if err != nil {
			return Config{}, fmt.Errorf("%w: %s %q: %v", ErrModelFile, item.name, absolute, err)
		}
		if !info.Mode().IsRegular() {
			return Config{}, fmt.Errorf("%w: %s %q is not a regular file", ErrModelFile, item.name, absolute)
		}
		*item.value = absolute
	}
	return config, nil
}

func normalizeLegacyPath(path string) string {
	if filepath.Separator != '\\' {
		path = strings.ReplaceAll(path, `\`, "/")
	}
	return filepath.Clean(path)
}
