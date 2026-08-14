package sherpaonnx

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
)

const (
	defaultFeatureDim         = 80
	defaultNumThreads         = 1
	defaultAudioQueueCapacity = 32
	defaultMaxTextLength      = 80
	// defaultHotwordsScore matches sherpa-onnx's own default boost.
	defaultHotwordsScore = 1.5
	// beamSearchDecoding is the only decoding method that applies hotwords.
	beamSearchDecoding = "modified_beam_search"

	// BpeVocabName is the BPE vocabulary a sherpa-onnx model ships beside its
	// tokens.txt when it can transcribe Latin script.
	BpeVocabName = "bpe.vocab"

	modelingUnitCJKChar = "cjkchar"
	modelingUnitBPE     = "cjkchar+bpe"
)

// bpeModelingUnits need a BPE vocabulary. Declaring one without it makes
// sherpa-onnx call a null encoder, which takes the whole process down.
var bpeModelingUnits = map[string]bool{"bpe": true, "bbpe": true, modelingUnitBPE: true}

var (
	// ErrInvalidConfig indicates that the JSON configuration cannot be used by
	// a streaming sherpa-onnx recognizer.
	ErrInvalidConfig = errors.New("invalid sherpa-onnx recognizer config")
	// ErrModelFile indicates that a configured model, rule, or hotwords file is
	// missing or is not a regular file.
	ErrModelFile = errors.New("invalid sherpa-onnx model file")
)

// Config accepts the original .NET plugin's lower-case model fields and
// optional tuning fields used by the Go implementation. Defaults intentionally
// match the old SherpaOnnxRecognizer.cs runtime configuration.
type Config struct {
	Model   string `json:"model"`
	Encoder string `json:"encoder"`
	Decoder string `json:"decoder"`
	Joiner  string `json:"joiner"`
	Tokens  string `json:"tokens"`

	NumThreads     int    `json:"NumThreads"`
	Provider       string `json:"Provider"`
	Debug          bool   `json:"Debug"`
	FeatureDim     int    `json:"FeatureDim"`
	DecodingMethod string `json:"DecodingMethod"`
	MaxActivePaths int    `json:"MaxActivePaths"`

	// Contextual biasing. sherpa-onnx only applies hotwords while decoding with
	// modified_beam_search. A file configured for a greedy run stays in the
	// configuration — switching the decoding method back must not cost the user
	// their word list — and is dropped by prepareConfig instead; the settings
	// page is where that is spelled out.
	HotwordsFile  string  `json:"HotwordsFile"`
	HotwordsScore float32 `json:"HotwordsScore"`

	// Hotword tokenization. sherpa-onnx encodes the hotwords file with the
	// model's modeling unit and defaults to cjkchar, which splits a Latin word
	// into single letters the model never emits — so English hotwords silently
	// do nothing. Both fields are normally left empty and derived from the
	// model: a model that ships bpe.vocab beside tokens.txt switches to
	// cjkchar+bpe, which encodes English the way the model decodes it.
	ModelingUnit string `json:"ModelingUnit"`
	BpeVocab     string `json:"BpeVocab"`

	// Inverse text normalization. These rewrite the model's spoken output into
	// written form, e.g. 二零二五 into 2025. sherpa-onnx accepts several files,
	// separated by commas.
	RuleFsts string `json:"RuleFsts"`
	RuleFars string `json:"RuleFars"`

	Endpoint                bool    `json:"Endpoint"`
	Rule1MinTrailingSilence float32 `json:"Rule1MinTrailingSilence"`
	Rule2MinTrailingSilence float32 `json:"Rule2MinTrailingSilence"`
	Rule3MinUtteranceLength float32 `json:"Rule3MinUtteranceLength"`
	MaxTextLength           int     `json:"MaxTextLength"`
	AudioQueueCapacity      int     `json:"AudioQueueCapacity"`
}

// ModelFiles is the resolved streaming transducer model file set.
type ModelFiles struct {
	Encoder string
	Decoder string
	Joiner  string
	Tokens  string
}

// ModelResolver expands a legacy resource ID from Config.Model into concrete
// model paths. The recognizer deliberately does not depend on the resource
// package; applications can adapt their resource manager with this function.
type ModelResolver func(context.Context, string) (ModelFiles, error)

// DefaultConfig returns the old C# recognizer's defaults plus a bounded feed
// queue for the non-blocking Go audio path.
func DefaultConfig() Config {
	return Config{
		Encoder:                 `models\encoder.onnx`,
		Decoder:                 `models\decoder.onnx`,
		Joiner:                  `models\joiner.onnx`,
		Tokens:                  `models\tokens.txt`,
		NumThreads:              defaultNumThreads,
		Provider:                "cpu",
		Debug:                   true,
		FeatureDim:              defaultFeatureDim,
		DecodingMethod:          "greedy_search",
		MaxActivePaths:          4,
		HotwordsScore:           defaultHotwordsScore,
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

	// Endpoint was introduced as a bool, while some callers use the underlying
	// sherpa name EnableEndpoint. Debug also commonly arrives as C-style 0/1.
	endpoint, hasEndpoint, err := removeBoolField(fields, "Endpoint", "EnableEndpoint")
	if err != nil {
		return Config{}, err
	}
	debug, hasDebug, err := removeBoolField(fields, "Debug")
	if err != nil {
		return Config{}, err
	}

	remainder, err := json.Marshal(fields)
	if err != nil {
		return Config{}, fmt.Errorf("%w: normalize JSON: %v", ErrInvalidConfig, err)
	}
	type plainConfig Config
	if err := json.Unmarshal(remainder, (*plainConfig)(&config)); err != nil {
		return Config{}, fmt.Errorf("%w: decode fields: %v", ErrInvalidConfig, err)
	}
	if hasEndpoint {
		config.Endpoint = endpoint
	}
	if hasDebug {
		config.Debug = debug
	}
	if err := validateConfig(config); err != nil {
		return Config{}, err
	}
	return config, nil
}

func removeBoolField(fields map[string]json.RawMessage, names ...string) (bool, bool, error) {
	var selected json.RawMessage
	selectedName := ""
	for _, preferred := range names {
		if value, ok := fields[preferred]; ok {
			selected = value
			selectedName = preferred
			break
		}
	}
	if selectedName == "" {
		for key, value := range fields {
			for _, name := range names {
				if strings.EqualFold(key, name) {
					selected = value
					selectedName = key
					break
				}
			}
			if selectedName != "" {
				break
			}
		}
	}
	for key := range fields {
		for _, name := range names {
			if strings.EqualFold(key, name) {
				delete(fields, key)
				break
			}
		}
	}
	if selectedName == "" {
		return false, false, nil
	}
	value, err := parseJSONBool(selected)
	if err != nil {
		return false, false, fmt.Errorf("%w: %s: %v", ErrInvalidConfig, selectedName, err)
	}
	return value, true, nil
}

func parseJSONBool(raw json.RawMessage) (bool, error) {
	var value bool
	if err := json.Unmarshal(raw, &value); err == nil {
		return value, nil
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err == nil {
		switch number.String() {
		case "0":
			return false, nil
		case "1":
			return true, nil
		}
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		parsed, parseErr := strconv.ParseBool(text)
		if parseErr == nil {
			return parsed, nil
		}
		if text == "0" {
			return false, nil
		}
		if text == "1" {
			return true, nil
		}
	}
	return false, errors.New("expected true, false, 1, or 0")
}

func validateConfig(config Config) error {
	if config.NumThreads <= 0 {
		return fmt.Errorf("%w: NumThreads must be positive", ErrInvalidConfig)
	}
	if strings.TrimSpace(config.Provider) == "" {
		return fmt.Errorf("%w: Provider is empty", ErrInvalidConfig)
	}
	if config.FeatureDim <= 0 {
		return fmt.Errorf("%w: FeatureDim must be positive", ErrInvalidConfig)
	}
	if strings.TrimSpace(config.DecodingMethod) == "" {
		return fmt.Errorf("%w: DecodingMethod is empty", ErrInvalidConfig)
	}
	switch config.DecodingMethod {
	case "greedy_search":
	case beamSearchDecoding:
		if config.MaxActivePaths <= 0 {
			return fmt.Errorf("%w: MaxActivePaths must be positive for modified_beam_search", ErrInvalidConfig)
		}
	default:
		return fmt.Errorf("%w: unsupported DecodingMethod %q", ErrInvalidConfig, config.DecodingMethod)
	}
	if config.HotwordsScore < 0 {
		return fmt.Errorf("%w: HotwordsScore must not be negative", ErrInvalidConfig)
	}
	modelingUnit := strings.TrimSpace(config.ModelingUnit)
	switch modelingUnit {
	case "", modelingUnitCJKChar, "bpe", "bbpe", modelingUnitBPE:
	default:
		return fmt.Errorf("%w: unsupported ModelingUnit %q", ErrInvalidConfig, config.ModelingUnit)
	}
	if bpeModelingUnits[modelingUnit] && strings.TrimSpace(config.BpeVocab) == "" {
		return fmt.Errorf("%w: ModelingUnit %q requires BpeVocab", ErrInvalidConfig, modelingUnit)
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
			return Config{}, fmt.Errorf("resolve sherpa-onnx model %q: %w", config.Model, err)
		}
		config.Encoder = files.Encoder
		config.Decoder = files.Decoder
		config.Joiner = files.Joiner
		config.Tokens = files.Tokens
	}

	paths := []struct {
		name  string
		value *string
	}{
		{name: "encoder", value: &config.Encoder},
		{name: "decoder", value: &config.Decoder},
		{name: "joiner", value: &config.Joiner},
		{name: "tokens", value: &config.Tokens},
	}
	for _, item := range paths {
		if err := ctx.Err(); err != nil {
			return Config{}, err
		}
		absolute, err := resolveExistingFile(item.name, *item.value)
		if err != nil {
			return Config{}, err
		}
		*item.value = absolute
	}

	// Hotwords and ITN rules are optional, so an empty value stays empty and
	// sherpa-onnx skips the feature. Under greedy search the file is dropped
	// here: sherpa-onnx would ignore it anyway, and a missing or unreadable
	// list must not fail a run that never consults it.
	if !HotwordsApply(config.DecodingMethod) {
		config.HotwordsFile = ""
	}
	if strings.TrimSpace(config.HotwordsFile) != "" {
		absolute, err := resolveExistingFile("hotwords", config.HotwordsFile)
		if err != nil {
			return Config{}, err
		}
		config.HotwordsFile = absolute
		if err := resolveHotwordTokenization(&config); err != nil {
			return Config{}, err
		}
	}
	lists := []struct {
		name  string
		value *string
	}{
		{name: "rule_fsts", value: &config.RuleFsts},
		{name: "rule_fars", value: &config.RuleFars},
	}
	for _, item := range lists {
		resolved, err := resolveExistingFileList(ctx, item.name, *item.value)
		if err != nil {
			return Config{}, err
		}
		*item.value = resolved
	}
	return config, nil
}

// HotwordsApply reports whether a decoding method biases the decoder with the
// hotwords file. Only modified_beam_search does; the application uses this to
// tell the user that a configured list is currently unused rather than leaving
// them to wonder why their words never appear.
func HotwordsApply(decodingMethod string) bool {
	return strings.TrimSpace(decodingMethod) == beamSearchDecoding
}

// resolveHotwordTokenization decides how the hotwords file is turned into model
// tokens. It runs only for a configured hotwords file, and prefers the model's
// own BPE vocabulary so Latin terms survive; without one the recognizer stays on
// the C API's cjkchar default, where Chinese hotwords still work and Latin ones
// are dropped by sherpa-onnx with a log line.
func resolveHotwordTokenization(config *Config) error {
	if strings.TrimSpace(config.BpeVocab) == "" && strings.TrimSpace(config.ModelingUnit) == "" {
		candidate := filepath.Join(filepath.Dir(config.Tokens), BpeVocabName)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			config.BpeVocab = candidate
		}
	}
	if strings.TrimSpace(config.BpeVocab) != "" {
		absolute, err := resolveExistingFile("bpe_vocab", config.BpeVocab)
		if err != nil {
			return err
		}
		config.BpeVocab = absolute
	}
	if strings.TrimSpace(config.ModelingUnit) == "" {
		if config.BpeVocab != "" {
			config.ModelingUnit = modelingUnitBPE
		} else {
			config.ModelingUnit = modelingUnitCJKChar
		}
	}
	// Re-check after derivation: a hand-written bpe modeling unit whose vocabulary
	// went missing must fail here rather than inside the native library.
	if bpeModelingUnits[strings.TrimSpace(config.ModelingUnit)] && config.BpeVocab == "" {
		return fmt.Errorf("%w: ModelingUnit %q requires BpeVocab", ErrInvalidConfig, config.ModelingUnit)
	}
	return nil
}

// resolveExistingFile turns one configured path into an absolute path after
// confirming it points at a regular file.
func resolveExistingFile(name, value string) (string, error) {
	path := normalizeLegacyPath(strings.TrimSpace(value))
	if path == "" || path == "." {
		return "", fmt.Errorf("%w: %s path is empty", ErrModelFile, name)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%w: resolve %s path %q: %v", ErrModelFile, name, path, err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("%w: %s %q: %v", ErrModelFile, name, absolute, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %s %q is not a regular file", ErrModelFile, name, absolute)
	}
	return absolute, nil
}

// resolveExistingFileList validates an optional comma-separated setting and
// rewrites every entry as an absolute path, the form sherpa-onnx expects.
func resolveExistingFileList(ctx context.Context, name, value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	entries := strings.Split(value, ",")
	resolved := make([]string, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		absolute, err := resolveExistingFile(name, entry)
		if err != nil {
			return "", err
		}
		resolved = append(resolved, absolute)
	}
	return strings.Join(resolved, ","), nil
}

func normalizeLegacyPath(path string) string {
	// Legacy configs were emitted on Windows. Treat their separators as path
	// separators on Unix too, so a copied relative config remains usable.
	if filepath.Separator != '\\' {
		path = strings.ReplaceAll(path, `\`, "/")
	}
	return filepath.Clean(path)
}
