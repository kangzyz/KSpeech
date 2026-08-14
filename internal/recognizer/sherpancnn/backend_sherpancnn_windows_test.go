//go:build windows && (amd64 || 386) && sherpancnn && cgo

package sherpancnn

import (
	"testing"

	"github.com/kangzyz/KSpeech/internal/plugin"
)

func TestWindowsNativeConfigMapsVulkanAndEndpointFlags(t *testing.T) {
	config := DefaultConfig()
	config.UseVulkanCompute = true
	config.Endpoint = false

	nativeConfig, release := makeNativeRecognizerConfig(config)
	defer release()

	if got := int32(nativeConfig.model_config.use_vulkan_compute); got != 1 {
		t.Fatalf("use_vulkan_compute = %d, want 1", got)
	}
	if got := int32(nativeConfig.enable_endpoint); got != 0 {
		t.Fatalf("enable_endpoint = %d, want 0", got)
	}
	if got := int(nativeConfig.feat_config.sampling_rate); got != plugin.AudioSampleRate {
		t.Fatalf("sampling_rate = %d, want %d", got, plugin.AudioSampleRate)
	}

	config.UseVulkanCompute = false
	nativeConfig, release = makeNativeRecognizerConfig(config)
	defer release()
	if got := int32(nativeConfig.model_config.use_vulkan_compute); got != 0 {
		t.Fatalf("use_vulkan_compute = %d, want 0", got)
	}
}

func TestWindowsNativeEngineProtectsEmptySamplesAndClose(t *testing.T) {
	engine := &nativeEngine{}
	if err := engine.AcceptWaveform(plugin.AudioSampleRate, nil); err != nil {
		t.Fatalf("empty samples: %v", err)
	}
	if err := engine.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := engine.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}
