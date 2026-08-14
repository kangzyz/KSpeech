package sherpaonnx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodeConfigLegacyShapeKeepsRuntimeDefaults(t *testing.T) {
	config, err := decodeConfig([]byte(`{
		"model":"legacy-model",
		"encoder":"custom/encoder.onnx",
		"decoder":"custom/decoder.onnx",
		"joiner":"custom/joiner.onnx",
		"tokens":"custom/tokens.txt"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if config.Model != "legacy-model" || config.Encoder != "custom/encoder.onnx" || config.Tokens != "custom/tokens.txt" {
		t.Fatalf("legacy fields were not decoded: %#v", config)
	}
	if config.NumThreads != 1 || config.Provider != "cpu" || !config.Debug {
		t.Fatalf("legacy runtime defaults changed: %#v", config)
	}
	if !config.Endpoint || config.Rule1MinTrailingSilence != 2.4 || config.Rule2MinTrailingSilence != 1.2 || config.Rule3MinUtteranceLength != 20 {
		t.Fatalf("legacy endpoint defaults changed: %#v", config)
	}
	if config.FeatureDim != 80 || config.DecodingMethod != "greedy_search" || config.MaxTextLength != 80 {
		t.Fatalf("recognition defaults changed: %#v", config)
	}
}

func TestDecodeConfigAcceptsOptionalCasingAndBoolRepresentations(t *testing.T) {
	config, err := decodeConfig([]byte(`{
		"NumThreads":3,
		"Provider":"cuda",
		"Debug":"false",
		"FeatureDim":64,
		"DecodingMethod":"modified_beam_search",
		"MaxActivePaths":8,
		"EnableEndpoint":0,
		"Rule1MinTrailingSilence":3.5,
		"Rule2MinTrailingSilence":1.5,
		"Rule3MinUtteranceLength":30,
		"MaxTextLength":0,
		"AudioQueueCapacity":7
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if config.NumThreads != 3 || config.Provider != "cuda" || config.Debug {
		t.Fatalf("runtime tuning was not decoded: %#v", config)
	}
	if config.Endpoint || config.FeatureDim != 64 || config.MaxActivePaths != 8 || config.AudioQueueCapacity != 7 {
		t.Fatalf("optional tuning was not decoded: %#v", config)
	}
	if config.MaxTextLength != 0 {
		t.Fatalf("zero must disable the forced text boundary: %#v", config)
	}
}

func TestDecodeConfigRejectsInvalidValues(t *testing.T) {
	tests := []string{
		`null`,
		`[]`,
		`{"NumThreads":0}`,
		`{"Endpoint":"sometimes"}`,
		`{"AudioQueueCapacity":0}`,
		`{"Rule1MinTrailingSilence":-1}`,
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			_, err := decodeConfig([]byte(input))
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("decodeConfig(%s) error = %v, want ErrInvalidConfig", input, err)
			}
		})
	}
}

func TestPrepareConfigUsesOptionalModelResolver(t *testing.T) {
	files := makeModelFiles(t)
	config := DefaultConfig()
	config.Model = "resource-id"
	config.Encoder = "ignored-encoder"
	config.Decoder = "ignored-decoder"
	config.Joiner = "ignored-joiner"
	config.Tokens = "ignored-tokens"

	called := false
	prepared, err := prepareConfig(context.Background(), config, func(ctx context.Context, model string) (ModelFiles, error) {
		called = true
		if model != "resource-id" {
			t.Fatalf("model = %q", model)
		}
		return files, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("model resolver was not called")
	}
	for name, path := range map[string]string{
		"encoder": prepared.Encoder,
		"decoder": prepared.Decoder,
		"joiner":  prepared.Joiner,
		"tokens":  prepared.Tokens,
	} {
		if !filepath.IsAbs(path) {
			t.Fatalf("%s path is not absolute: %q", name, path)
		}
	}
}

func TestPrepareConfigUsesDirectPathsWithoutResolver(t *testing.T) {
	files := makeModelFiles(t)
	config := configWithModelFiles(files)
	config.Model = "preserved-resource-id"
	prepared, err := prepareConfig(context.Background(), config, nil)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Model != config.Model || prepared.Encoder != files.Encoder {
		t.Fatalf("direct paths were not preserved: %#v", prepared)
	}
}

func TestPrepareConfigRequiresRegularModelFiles(t *testing.T) {
	files := makeModelFiles(t)
	files.Joiner = t.TempDir()
	_, err := prepareConfig(context.Background(), configWithModelFiles(files), nil)
	if !errors.Is(err, ErrModelFile) {
		t.Fatalf("error = %v, want ErrModelFile", err)
	}
}

func TestPrepareConfigLeavesOptionalFilesEmpty(t *testing.T) {
	prepared, err := prepareConfig(context.Background(), configWithModelFiles(makeModelFiles(t)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.HotwordsFile != "" || prepared.RuleFsts != "" || prepared.RuleFars != "" {
		t.Fatalf("optional files must stay empty so sherpa skips them: %#v", prepared)
	}
}

func TestPrepareConfigResolvesCommaSeparatedRuleFiles(t *testing.T) {
	directory := t.TempDir()
	first := writeTempFile(t, directory, "itn_zh_number.fst")
	second := writeTempFile(t, directory, "itn_extra.fst")

	config := configWithModelFiles(makeModelFiles(t))
	// A relative entry with surrounding spaces is what a hand-edited config
	// looks like; every entry must still come back absolute.
	config.RuleFsts = first + " , " + second
	prepared, err := prepareConfig(context.Background(), config, nil)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(prepared.RuleFsts, ",")
	if len(parts) != 2 {
		t.Fatalf("RuleFsts = %q, want two entries", prepared.RuleFsts)
	}
	for _, part := range parts {
		if !filepath.IsAbs(part) {
			t.Fatalf("rule path is not absolute: %q", part)
		}
		if strings.TrimSpace(part) != part {
			t.Fatalf("rule path keeps padding: %q", part)
		}
	}
}

func TestPrepareConfigRejectsMissingOptionalFiles(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.fst")
	tests := map[string]func(*Config){
		"rule_fsts": func(c *Config) { c.RuleFsts = missing },
		"rule_fars": func(c *Config) { c.RuleFars = missing },
		"hotwords": func(c *Config) {
			c.DecodingMethod = beamSearchDecoding
			c.HotwordsFile = missing
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			config := configWithModelFiles(makeModelFiles(t))
			mutate(&config)
			if _, err := prepareConfig(context.Background(), config, nil); !errors.Is(err, ErrModelFile) {
				t.Fatalf("error = %v, want ErrModelFile", err)
			}
		})
	}
}

func TestDecodeConfigKeepsHotwordsWithoutBeamSearch(t *testing.T) {
	// The pair is a legitimate state: the settings page lets the decoding method
	// be switched back and forth, and dropping the word list on the way past
	// greedy search would make the user type its path in again.
	config, err := decodeConfig([]byte(`{"HotwordsFile":"hotwords.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if config.HotwordsFile != "hotwords.txt" || config.DecodingMethod != "greedy_search" {
		t.Fatalf("hotwords fields were not decoded: %#v", config)
	}

	config, err = decodeConfig([]byte(`{
		"HotwordsFile":"hotwords.txt",
		"DecodingMethod":"modified_beam_search",
		"HotwordsScore":2.5
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if config.HotwordsFile != "hotwords.txt" || config.HotwordsScore != 2.5 {
		t.Fatalf("hotwords fields were not decoded: %#v", config)
	}
}

// sherpa-onnx only biases modified_beam_search, so a greedy run must not carry
// the file into the native configuration — not even to fail on it when the list
// has since been deleted.
func TestPrepareConfigDropsHotwordsOutsideBeamSearch(t *testing.T) {
	config := configWithModelFiles(makeModelFiles(t))
	config.HotwordsFile = writeTempFile(t, t.TempDir(), "hotwords.txt")
	prepared, err := prepareConfig(context.Background(), config, nil)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.HotwordsFile != "" || prepared.ModelingUnit != "" {
		t.Fatalf("greedy search kept hotword settings: %#v", prepared)
	}
	if !HotwordsApply(beamSearchDecoding) || HotwordsApply("greedy_search") {
		t.Fatal("HotwordsApply does not follow the decoding method")
	}

	config.HotwordsFile = filepath.Join(t.TempDir(), "absent.txt")
	if _, err := prepareConfig(context.Background(), config, nil); err != nil {
		t.Fatalf("error = %v, want an unused list to be ignored", err)
	}
}

// Without a BPE vocabulary sherpa-onnx encodes hotwords per CJK character,
// which turns an English term into single letters the model never emits. A
// model that ships one must therefore switch the modeling unit.
func TestPrepareConfigDerivesHotwordTokenization(t *testing.T) {
	files := makeModelFiles(t)
	vocabulary := writeTempFile(t, filepath.Dir(files.Tokens), BpeVocabName)
	config := configWithModelFiles(files)
	config.DecodingMethod = beamSearchDecoding
	config.HotwordsFile = writeTempFile(t, t.TempDir(), "hotwords.txt")

	prepared, err := prepareConfig(context.Background(), config, nil)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.ModelingUnit != modelingUnitBPE || prepared.BpeVocab != vocabulary {
		t.Fatalf("tokenization = %q/%q, want %q with the model's vocabulary",
			prepared.ModelingUnit, prepared.BpeVocab, modelingUnitBPE)
	}

	// A Chinese-only model has no vocabulary to offer, and must stay on the
	// character path instead of pointing sherpa-onnx at a missing file.
	plain := configWithModelFiles(makeModelFiles(t))
	plain.DecodingMethod = beamSearchDecoding
	plain.HotwordsFile = config.HotwordsFile
	prepared, err = prepareConfig(context.Background(), plain, nil)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.ModelingUnit != modelingUnitCJKChar || prepared.BpeVocab != "" {
		t.Fatalf("tokenization = %q/%q, want the character default",
			prepared.ModelingUnit, prepared.BpeVocab)
	}

	// No hotwords, nothing to encode: leave both fields to the C API defaults.
	untouched, err := prepareConfig(context.Background(), configWithModelFiles(files), nil)
	if err != nil {
		t.Fatal(err)
	}
	if untouched.ModelingUnit != "" || untouched.BpeVocab != "" {
		t.Fatalf("tokenization = %q/%q, want both empty", untouched.ModelingUnit, untouched.BpeVocab)
	}
}

// A BPE modeling unit without a vocabulary makes sherpa-onnx call a null
// encoder and take the process down, so it is refused here.
func TestConfigRejectsBpeModelingUnitWithoutVocabulary(t *testing.T) {
	_, err := decodeConfig([]byte(`{"ModelingUnit":"cjkchar+bpe"}`))
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v, want ErrInvalidConfig", err)
	}
	if _, err := decodeConfig([]byte(`{"ModelingUnit":"pinyin"}`)); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v, want ErrInvalidConfig for an unknown modeling unit", err)
	}

	files := makeModelFiles(t)
	config := configWithModelFiles(files)
	config.DecodingMethod = beamSearchDecoding
	config.HotwordsFile = writeTempFile(t, t.TempDir(), "hotwords.txt")
	config.ModelingUnit = modelingUnitBPE
	config.BpeVocab = filepath.Join(t.TempDir(), "absent.vocab")
	if _, err := prepareConfig(context.Background(), config, nil); !errors.Is(err, ErrModelFile) {
		t.Fatalf("error = %v, want ErrModelFile for a missing vocabulary", err)
	}
}

func TestDecodeConfigDefaultsHotwordsScore(t *testing.T) {
	config, err := decodeConfig([]byte(`{"RuleFsts":"itn.fst"}`))
	if err != nil {
		t.Fatal(err)
	}
	if config.HotwordsScore != defaultHotwordsScore {
		t.Fatalf("HotwordsScore = %v, want %v", config.HotwordsScore, defaultHotwordsScore)
	}
	if config.RuleFsts != "itn.fst" {
		t.Fatalf("RuleFsts was not decoded: %#v", config)
	}
}

func writeTempFile(t *testing.T, directory, name string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func makeModelFiles(t *testing.T) ModelFiles {
	t.Helper()
	directory := t.TempDir()
	makeFile := func(name string) string {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	return ModelFiles{
		Encoder: makeFile("encoder.onnx"),
		Decoder: makeFile("decoder.onnx"),
		Joiner:  makeFile("joiner.onnx"),
		Tokens:  makeFile("tokens.txt"),
	}
}

func configWithModelFiles(files ModelFiles) Config {
	config := DefaultConfig()
	config.Encoder = files.Encoder
	config.Decoder = files.Decoder
	config.Joiner = files.Joiner
	config.Tokens = files.Tokens
	return config
}
