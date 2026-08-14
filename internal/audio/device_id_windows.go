//go:build windows

package audio

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/moutend/go-wca/pkg/wca"
)

const maxDeviceIDUTF16Units = 32 * 1024

// safeDeviceID replaces go-wca's IMMDevice.GetId implementation. go-wca
// dereferences the returned string pointer even when IMMDevice::GetId fails,
// which panics when the COM method leaves that pointer nil.
func safeDeviceID(device *wca.IMMDevice) (string, error) {
	if device == nil {
		return "", errors.New("Windows audio endpoint is nil")
	}
	if device.RawVTable == nil {
		return "", errors.New("Windows audio endpoint has no COM vtable")
	}
	vtable := device.VTable()
	if vtable == nil || vtable.GetId == 0 {
		return "", errors.New("Windows audio endpoint has no GetId method")
	}
	defer runtime.KeepAlive(device)

	var value *uint16
	hresult, _, _ := syscall.SyscallN(
		vtable.GetId,
		uintptr(unsafe.Pointer(device)),
		uintptr(unsafe.Pointer(&value)),
	)
	if hresultFailed(hresult) {
		return "", fmt.Errorf("get Windows audio endpoint ID: %w", hresultError(hresult))
	}
	if value == nil {
		return "", errors.New("Windows audio endpoint GetId returned a nil string")
	}
	defer ole.CoTaskMemFree(uintptr(unsafe.Pointer(value)))

	return boundedUTF16String(value, maxDeviceIDUTF16Units)
}

func boundedUTF16String(value *uint16, maxUnits int) (string, error) {
	if value == nil {
		return "", errors.New("UTF-16 string pointer is nil")
	}
	if maxUnits <= 0 {
		return "", errors.New("UTF-16 string limit must be positive")
	}

	units := make([]uint16, 0, min(maxUnits, 256))
	for index := 0; index < maxUnits; index++ {
		unit := *(*uint16)(unsafe.Add(unsafe.Pointer(value), uintptr(index)*unsafe.Sizeof(*value)))
		if unit == 0 {
			return string(utf16.Decode(units)), nil
		}
		units = append(units, unit)
	}
	return "", fmt.Errorf("UTF-16 string is not terminated within %d code units", maxUnits)
}
