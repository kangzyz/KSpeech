//go:build windows

package sherpancnn

// The legacy Windows C# recognizer always requested Vulkan. The Windows Go
// backend calls the C API directly, so it can preserve that default.
const defaultUseVulkanCompute = true
