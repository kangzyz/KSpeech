//go:build !windows

package sherpancnn

// The official non-Windows Go wrappers hard-code CPU inference.
const defaultUseVulkanCompute = false
