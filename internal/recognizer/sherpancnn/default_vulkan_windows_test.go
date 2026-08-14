//go:build windows

package sherpancnn

import "testing"

func TestWindowsDefaultConfigPreservesLegacyVulkanRequest(t *testing.T) {
	if !DefaultConfig().UseVulkanCompute {
		t.Fatal("Windows default must preserve the legacy recognizer's Vulkan request")
	}
}
