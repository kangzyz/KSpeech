package sherpancnn

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfigPreservesLegacyNCNNShape(t *testing.T) {
	config := DefaultConfig()
	if config.EncoderParam != `ncnn-model\encoder.param` || config.EncoderBin != `ncnn-model\encoder.bin` ||
		config.DecoderParam != `ncnn-model\decoder.param` || config.DecoderBin != `ncnn-model\decoder.bin` ||
		config.JoinerParam != `ncnn-model\joiner.param` || config.JoinerBin != `ncnn-model\joiner.bin` ||
		config.Tokens != `ncnn-model\tokens.txt` {
		t.Fatalf("legacy model defaults changed: %#v", config)
	}
	if config.NumThreads != 1 || config.FeatureDim != 80 || config.DecodingMethod != "greedy_search" || config.NumActivePaths != 4 {
		t.Fatalf("legacy decode defaults changed: %#v", config)
	}
	if !config.Endpoint || config.Rule1MinTrailingSilence != 2.4 || config.Rule2MinTrailingSilence != 1.2 || config.Rule3MinUtteranceLength != 20 {
		t.Fatalf("legacy endpoint defaults changed: %#v", config)
	}
	if config.UseVulkanCompute != defaultUseVulkanCompute {
		t.Fatalf("platform Vulkan default = %v, want %v", config.UseVulkanCompute, defaultUseVulkanCompute)
	}
}

func TestDecodeConfigAcceptsLegacyFieldsAndOptionalTuning(t *testing.T) {
	config, err := decodeConfig([]byte(`{
		"model":"resource-id",
		"encoder_param":"encoder.param",
		"encoder_bin":"encoder.bin",
		"decoder_param":"decoder.param",
		"decoder_bin":"decoder.bin",
		"joiner_param":"joiner.param",
		"joiner_bin":"joiner.bin",
		"tokens":"tokens.txt",
		"num_threads":3,
		"FeatureDim":64,
		"decoding_method":"modified_beam_search",
		"max_active_paths":8,
		"EnableEndpoint":0,
		"use_vulkan_compute":"1",
		"AudioQueueCapacity":7,
		"future_field":"ignored"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if config.Model != "resource-id" || config.EncoderParam != "encoder.param" || config.JoinerBin != "joiner.bin" {
		t.Fatalf("legacy model fields were not decoded: %#v", config)
	}
	if config.NumThreads != 3 || config.FeatureDim != 64 || config.DecodingMethod != "modified_beam_search" || config.NumActivePaths != 8 {
		t.Fatalf("tuning fields were not decoded: %#v", config)
	}
	if config.Endpoint || !config.UseVulkanCompute || config.AudioQueueCapacity != 7 {
		t.Fatalf("bool/queue fields were not decoded: %#v", config)
	}
}

func TestDecodeConfigRejectsInvalidValues(t *testing.T) {
	tests := []string{
		`null`,
		`[]`,
		`{"NumThreads":0}`,
		`{"FeatureDim":0}`,
		`{"Endpoint":"sometimes"}`,
		`{"AudioQueueCapacity":0}`,
		`{"Rule1MinTrailingSilence":-1}`,
		`{"DecodingMethod":"beam_search"}`,
		`{"endpoint":true,"EnableEndpoint":false}`,
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

func TestPrepareConfigUsesModelResolverAndValidatesAllFiles(t *testing.T) {
	files := makeNCNNModelFiles(t)
	config := DefaultConfig()
	config.Model = "resource-id"
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
		"encoder_param": prepared.EncoderParam,
		"encoder_bin":   prepared.EncoderBin,
		"decoder_param": prepared.DecoderParam,
		"decoder_bin":   prepared.DecoderBin,
		"joiner_param":  prepared.JoinerParam,
		"joiner_bin":    prepared.JoinerBin,
		"tokens":        prepared.Tokens,
	} {
		if !filepath.IsAbs(path) {
			t.Fatalf("%s path is not absolute: %q", name, path)
		}
	}

	files.JoinerBin = t.TempDir()
	_, err = prepareConfig(context.Background(), configWithNCNNFiles(files), nil)
	if !errors.Is(err, ErrModelFile) {
		t.Fatalf("directory model error = %v, want ErrModelFile", err)
	}
}

func makeNCNNModelFiles(t *testing.T) ModelFiles {
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
		EncoderParam: makeFile("encoder.param"),
		EncoderBin:   makeFile("encoder.bin"),
		DecoderParam: makeFile("decoder.param"),
		DecoderBin:   makeFile("decoder.bin"),
		JoinerParam:  makeFile("joiner.param"),
		JoinerBin:    makeFile("joiner.bin"),
		Tokens:       makeFile("tokens.txt"),
	}
}

func configWithNCNNFiles(files ModelFiles) Config {
	config := DefaultConfig()
	config.EncoderParam = files.EncoderParam
	config.EncoderBin = files.EncoderBin
	config.DecoderParam = files.DecoderParam
	config.DecoderBin = files.DecoderBin
	config.JoinerParam = files.JoinerParam
	config.JoinerBin = files.JoinerBin
	config.Tokens = files.Tokens
	return config
}
