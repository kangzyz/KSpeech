//go:build windows

package audio

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/kangzyz/KSpeech/internal/plugin"
)

func TestProcessLoopbackABILayouts(t *testing.T) {
	if got := unsafe.Sizeof(audioClientActivationParams{}); got != 12 {
		t.Fatalf("AUDIOCLIENT_ACTIVATION_PARAMS size = %d, want 12", got)
	}
	wantBlobSize := uintptr(4 + unsafe.Sizeof(uintptr(0)))
	if unsafe.Sizeof(uintptr(0)) == 8 {
		wantBlobSize = 16 // pointer alignment inserts four bytes after cbSize
	}
	if got := unsafe.Sizeof(blob{}); got != wantBlobSize {
		t.Fatalf("BLOB size = %d, want %d", got, wantBlobSize)
	}
	if got, want := unsafe.Sizeof(blobPropVariant{}), uintptr(8)+wantBlobSize; got != want {
		t.Fatalf("PROPVARIANT(VT_BLOB) size = %d, want %d", got, want)
	}
}

func TestCompletionHandlerSupportsRequiredInterfaces(t *testing.T) {
	handler, state, err := newProcessCompletionHandler()
	if err != nil {
		t.Fatal(err)
	}
	if got := state.refs.Load(); got != 1 {
		t.Fatalf("initial refs = %d, want 1", got)
	}

	for _, interfaceID := range []*ole.GUID{
		ole.IID_IUnknown,
		iidActivateAudioInterfaceCompletionHandler,
		iidAgileObject,
	} {
		var object uintptr
		if hresult := completionHandlerQueryInterface(handler, interfaceID, &object); hresult != hResultOK {
			t.Fatalf("QueryInterface(%s) HRESULT = %#x", interfaceID.String(), hresult)
		}
		if object != handler {
			t.Fatalf("QueryInterface(%s) object = %#x, want %#x", interfaceID.String(), object, handler)
		}
		completionHandlerRelease(object)
	}

	unsupported := ole.NewGUID("{11111111-2222-3333-4444-555555555555}")
	object := uintptr(1)
	if hresult := completionHandlerQueryInterface(handler, unsupported, &object); hresult != hResultNoInterface {
		t.Fatalf("unsupported QueryInterface HRESULT = %#x, want %#x", hresult, hResultNoInterface)
	}
	if object != 0 {
		t.Fatalf("unsupported QueryInterface object = %#x, want nil", object)
	}

	if refs := completionHandlerRelease(handler); refs != 0 {
		t.Fatalf("final Release refs = %d, want 0", refs)
	}
	if _, ok := completionHandlers.Load(handler); ok {
		t.Fatal("completion handler state remains after final Release")
	}
}

func TestParseAndValidateCurrentProcess(t *testing.T) {
	pid, err := parseProcessID(strconv.Itoa(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	if pid != uint32(os.Getpid()) {
		t.Fatalf("PID = %d, want %d", pid, os.Getpid())
	}
	if err := validateProcess(pid); err != nil {
		t.Fatalf("validate current process: %v", err)
	}
	for _, value := range []string{"", "0", "abc", "-1"} {
		if _, err := parseProcessID(value); err == nil {
			t.Fatalf("parseProcessID(%q) unexpectedly succeeded", value)
		}
	}
}

func TestCOMInitializationAcceptsSFalse(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := initializeCOMMultithreaded(); err != nil {
		t.Fatal(err)
	}
	defer ole.CoUninitialize()
	// A second call on the same apartment returns S_FALSE. go-ole exposes it
	// as an error, while our helper must preserve the Win32 success semantics.
	if err := initializeCOMMultithreaded(); err != nil {
		t.Fatalf("second COM initialization: %v", err)
	}
	ole.CoUninitialize()
}

func TestBuiltinWASAPIActivationSmoke(t *testing.T) {
	if os.Getenv("KSPEECH_TEST_WASAPI") != "1" {
		t.Skip("set KSPEECH_TEST_WASAPI=1 to activate the default microphone and system loopback")
	}

	tests := []struct {
		name   string
		source plugin.AudioSource
	}{
		{name: "microphone", source: NewMicrophone(plugin.Metadata{})},
		{name: "system-loopback", source: NewLoopback(plugin.Metadata{})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := test.source.Start(ctx); err != nil {
				t.Fatalf("activate %s: %v", test.name, err)
			}
			if err := test.source.Stop(); err != nil {
				t.Fatalf("stop %s: %v", test.name, err)
			}
		})
	}
}

func TestProcessLoopbackActivationSmoke(t *testing.T) {
	if os.Getenv("KSPEECH_TEST_PROCESS_LOOPBACK") != "1" {
		t.Skip("set KSPEECH_TEST_PROCESS_LOOPBACK=1 to exercise Windows process-loopback activation")
	}
	if !processLoopbackAvailable() {
		t.Skip("process-loopback capture requires Windows build 20348 or newer")
	}

	source := NewProcessLoopback(plugin.Metadata{})
	if err := source.LoadConfig([]byte(strconv.Itoa(os.Getpid()))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := source.Start(ctx); err != nil {
		t.Fatalf("start process-loopback capture: %v", err)
	}
	if err := source.Stop(); err != nil {
		t.Fatalf("stop process-loopback capture: %v", err)
	}
}
