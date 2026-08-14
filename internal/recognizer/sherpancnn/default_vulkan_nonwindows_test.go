//go:build !windows

package sherpancnn

import "testing"

func TestNonWindowsDefaultConfigRemainsCPUOnly(t *testing.T) {
	if DefaultConfig().UseVulkanCompute {
		t.Fatal("non-Windows default must remain CPU-only")
	}
}
