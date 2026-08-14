//go:build windows

package audio

import (
	"runtime"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/moutend/go-wca/pkg/wca"
	"golang.org/x/sys/windows"
)

func TestSafeDeviceIDRejectsInvalidCOMObjects(t *testing.T) {
	for _, test := range []struct {
		name   string
		device *wca.IMMDevice
	}{
		{name: "nil device"},
		{name: "nil vtable", device: &wca.IMMDevice{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := safeDeviceID(test.device); err == nil {
				t.Fatal("safeDeviceID unexpectedly succeeded")
			}
		})
	}
}

func TestSafeDeviceIDChecksHRESULTBeforeReadingNilOutput(t *testing.T) {
	callback := func(_ uintptr, output **uint16) uintptr {
		*output = nil
		return uintptr(0x80004005) // E_FAIL
	}
	device, vtable := testIMMDevice(syscall.NewCallback(callback))

	if _, err := safeDeviceID(device); err == nil {
		t.Fatal("safeDeviceID unexpectedly accepted a failed HRESULT")
	}
	runtime.KeepAlive(callback)
	runtime.KeepAlive(vtable)
}

func TestSafeDeviceIDRejectsSuccessfulNilOutput(t *testing.T) {
	callback := func(_ uintptr, output **uint16) uintptr {
		*output = nil
		return hResultOK
	}
	device, vtable := testIMMDevice(syscall.NewCallback(callback))

	if _, err := safeDeviceID(device); err == nil || !strings.Contains(err.Error(), "nil string") {
		t.Fatalf("safeDeviceID error = %v, want nil-string error", err)
	}
	runtime.KeepAlive(callback)
	runtime.KeepAlive(vtable)
}

func TestSafeDeviceIDConvertsCoTaskMemUTF16(t *testing.T) {
	const want = "Microphone 🎤"
	memory := allocateCoTaskMemUTF16(t, want)
	callback := func(_ uintptr, output **uint16) uintptr {
		*output = memory
		return hResultOK
	}
	device, vtable := testIMMDevice(syscall.NewCallback(callback))

	got, err := safeDeviceID(device)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("safeDeviceID() = %q, want %q", got, want)
	}
	runtime.KeepAlive(callback)
	runtime.KeepAlive(vtable)
}

func TestBoundedUTF16StringBoundaries(t *testing.T) {
	if _, err := boundedUTF16String(nil, 1); err == nil {
		t.Fatal("nil pointer unexpectedly accepted")
	}
	value := []uint16{'A', 'B', 0}
	if _, err := boundedUTF16String(&value[0], 0); err == nil {
		t.Fatal("zero limit unexpectedly accepted")
	}
	if _, err := boundedUTF16String(&value[0], 2); err == nil {
		t.Fatal("unterminated value inside the limit unexpectedly accepted")
	}
	if got, err := boundedUTF16String(&value[0], len(value)); err != nil || got != "AB" {
		t.Fatalf("boundedUTF16String() = %q, %v; want AB, nil", got, err)
	}
}

func testIMMDevice(getID uintptr) (*wca.IMMDevice, *wca.IMMDeviceVtbl) {
	vtable := &wca.IMMDeviceVtbl{GetId: getID}
	device := &wca.IMMDevice{
		IUnknown: ole.IUnknown{RawVTable: (*interface{})(unsafe.Pointer(vtable))},
	}
	return device, vtable
}

func allocateCoTaskMemUTF16(t *testing.T, value string) *uint16 {
	t.Helper()
	encoded, err := windows.UTF16PtrFromString(value)
	if err != nil {
		t.Fatal(err)
	}
	var memory *uint16
	procedure := windows.NewLazySystemDLL("shlwapi.dll").NewProc("SHStrDupW")
	hresult, _, _ := procedure.Call(
		uintptr(unsafe.Pointer(encoded)),
		uintptr(unsafe.Pointer(&memory)),
	)
	runtime.KeepAlive(encoded)
	if hresultFailed(hresult) || memory == nil {
		t.Fatalf("SHStrDupW failed with HRESULT %#x", uint32(hresult))
	}
	return memory
}
