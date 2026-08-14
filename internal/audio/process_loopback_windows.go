//go:build windows

package audio

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/moutend/go-wca/pkg/wca"
	"golang.org/x/sys/windows"
)

const (
	minimumProcessLoopbackBuild = 20348
	virtualProcessLoopbackPath  = `VAD\Process_Loopback`

	audioClientActivationTypeProcessLoopback = 1
	processLoopbackModeIncludeTargetTree     = 0
	variantTypeBlob                          = 65
	stillActive                              = 259

	hResultOK          = uintptr(0)
	hResultPointer     = uintptr(0x80004003)
	hResultNoInterface = uintptr(0x80004002)
)

var (
	procActivateAudioInterfaceAsync = windows.NewLazySystemDLL("mmdevapi.dll").NewProc("ActivateAudioInterfaceAsync")

	iidActivateAudioInterfaceCompletionHandler = ole.NewGUID("{41D949AB-9862-444A-80F6-C261334DA5EB}")
	iidAgileObject                             = ole.NewGUID("{94EA2B94-E9CC-49E0-C0FF-EE64CA8F5B90}")

	completionHandlers sync.Map
	completionVTable   struct {
		once  sync.Once
		value *activateCompletionVTable
	}
)

// These layouts mirror audioclientactivationparams.h and PROPVARIANT's
// VT_BLOB arm. Keeping them explicit also makes it possible to test the ABI
// without starting an audio stream.
type audioClientProcessLoopbackParams struct {
	TargetProcessID     uint32
	ProcessLoopbackMode int32
}

type audioClientActivationParams struct {
	ActivationType        int32
	ProcessLoopbackParams audioClientProcessLoopbackParams
}

type blob struct {
	Size uint32
	Data unsafe.Pointer
}

type blobPropVariant struct {
	VariantType uint16
	Reserved1   uint16
	Reserved2   uint16
	Reserved3   uint16
	Blob        blob
}

type activateCompletionVTable struct {
	QueryInterface    uintptr
	AddRef            uintptr
	Release           uintptr
	ActivateCompleted uintptr
}

// processCompletionHandlerObject is the ABI-visible COM object. Its single
// field is intentionally a uintptr (rather than a Go pointer), so the memory
// handed to Windows contains no Go pointers. processCompletionState retains
// the allocation until the COM reference count reaches zero.
type processCompletionHandlerObject struct {
	VTable uintptr
}

type activateAsyncOperation struct {
	VTable *activateAsyncOperationVTable
}

type activateAsyncOperationVTable struct {
	QueryInterface    uintptr
	AddRef            uintptr
	Release           uintptr
	GetActivateResult uintptr
}

type processActivationResult struct {
	client *wca.IAudioClient
	err    error
}

type processCompletionState struct {
	object *processCompletionHandlerObject
	refs   atomic.Uint32
	once   sync.Once
	result chan processActivationResult
}

func processLoopbackAvailable() bool {
	version := windows.RtlGetVersion()
	return version.MajorVersion >= 10 && version.BuildNumber >= minimumProcessLoopbackBuild &&
		procActivateAudioInterfaceAsync.Find() == nil
}

func parseProcessID(config string) (uint32, error) {
	value := strings.TrimSpace(config)
	if value == "" {
		return 0, errors.New("process ID is required for process-loopback capture")
	}
	pid, err := strconv.ParseUint(value, 10, 32)
	if err != nil || pid == 0 {
		return 0, fmt.Errorf("invalid process ID %q", value)
	}
	return uint32(pid), nil
}

func validateProcess(pid uint32) error {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return fmt.Errorf("open target process %d: %w", pid, err)
	}
	defer windows.CloseHandle(handle)

	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return fmt.Errorf("query target process %d: %w", pid, err)
	}
	if exitCode != stillActive {
		return fmt.Errorf("target process %d has exited", pid)
	}
	return nil
}

func activateProcessAudioClient(ctx context.Context, pid uint32) (*wca.IAudioClient, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !processLoopbackAvailable() {
		return nil, ErrProcessLoopbackUnsupported
	}

	handler, state, err := newProcessCompletionHandler()
	if err != nil {
		return nil, fmt.Errorf("create process-loopback completion handler: %w", err)
	}
	// Drop the reference owned by this function after Windows has completed its
	// callback (or after a synchronous activation failure).
	releaseHandler := true
	defer func() {
		if releaseHandler {
			completionHandlerRelease(handler)
		}
	}()

	params := audioClientActivationParams{
		ActivationType: audioClientActivationTypeProcessLoopback,
		ProcessLoopbackParams: audioClientProcessLoopbackParams{
			TargetProcessID:     pid,
			ProcessLoopbackMode: processLoopbackModeIncludeTargetTree,
		},
	}
	activationParams := blobPropVariant{
		VariantType: variantTypeBlob,
		Blob: blob{
			Size: uint32(unsafe.Sizeof(params)),
			Data: unsafe.Pointer(&params),
		},
	}
	devicePath, err := windows.UTF16PtrFromString(virtualProcessLoopbackPath)
	if err != nil {
		return nil, fmt.Errorf("encode process-loopback virtual device path: %w", err)
	}

	var operation *activateAsyncOperation
	hresult, _, _ := procActivateAudioInterfaceAsync.Call(
		uintptr(unsafe.Pointer(devicePath)),
		uintptr(unsafe.Pointer(wca.IID_IAudioClient)),
		uintptr(unsafe.Pointer(&activationParams)),
		handler,
		uintptr(unsafe.Pointer(&operation)),
	)
	if hresultFailed(hresult) {
		runtime.KeepAlive(params)
		runtime.KeepAlive(activationParams)
		runtime.KeepAlive(devicePath)
		return nil, hresultError(hresult)
	}
	if operation == nil {
		runtime.KeepAlive(params)
		runtime.KeepAlive(activationParams)
		runtime.KeepAlive(devicePath)
		return nil, errors.New("ActivateAudioInterfaceAsync returned no operation")
	}

	select {
	case result := <-state.result:
		operation.release()
		completionHandlerRelease(handler)
		releaseHandler = false
		runtime.KeepAlive(params)
		runtime.KeepAlive(activationParams)
		runtime.KeepAlive(devicePath)
		return result.client, result.err
	case <-ctx.Done():
		// ActivateAudioInterfaceAsync has no cancellation method. Its operation
		// and completion handler must stay alive until the callback runs, so move
		// the mandatory cleanup to a short-lived MTA goroutine.
		releaseHandler = false
		go finishCancelledProcessActivation(operation, handler, state, &params, &activationParams, devicePath)
		return nil, ctx.Err()
	}
}

func finishCancelledProcessActivation(
	operation *activateAsyncOperation,
	handler uintptr,
	state *processCompletionState,
	params *audioClientActivationParams,
	activationParams *blobPropVariant,
	devicePath *uint16,
) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	initialized := initializeCOMMultithreaded() == nil
	if initialized {
		defer ole.CoUninitialize()
	}
	result := <-state.result
	if result.client != nil {
		result.client.Release()
	}
	operation.release()
	completionHandlerRelease(handler)
	runtime.KeepAlive(params)
	runtime.KeepAlive(activationParams)
	runtime.KeepAlive(devicePath)
}

func newProcessCompletionHandler() (uintptr, *processCompletionState, error) {
	vtable := processCompletionVTable()
	object := &processCompletionHandlerObject{VTable: uintptr(unsafe.Pointer(vtable))}
	handler := uintptr(unsafe.Pointer(object))
	state := &processCompletionState{object: object, result: make(chan processActivationResult, 1)}
	state.refs.Store(1)
	completionHandlers.Store(handler, state)
	return handler, state, nil
}

func processCompletionVTable() *activateCompletionVTable {
	completionVTable.once.Do(func() {
		completionVTable.value = &activateCompletionVTable{
			QueryInterface:    syscall.NewCallback(completionHandlerQueryInterface),
			AddRef:            syscall.NewCallback(completionHandlerAddRef),
			Release:           syscall.NewCallback(completionHandlerRelease),
			ActivateCompleted: syscall.NewCallback(completionHandlerActivateCompleted),
		}
	})
	return completionVTable.value
}

func completionHandlerQueryInterface(this uintptr, interfaceID *ole.GUID, object *uintptr) uintptr {
	if this == 0 || interfaceID == nil || object == nil {
		return hResultPointer
	}
	*object = 0
	if !ole.IsEqualGUID(interfaceID, ole.IID_IUnknown) &&
		!ole.IsEqualGUID(interfaceID, iidActivateAudioInterfaceCompletionHandler) &&
		!ole.IsEqualGUID(interfaceID, iidAgileObject) {
		return hResultNoInterface
	}
	if completionHandlerAddRef(this) == 0 {
		return hResultPointer
	}
	*object = this
	return hResultOK
}

func completionHandlerAddRef(this uintptr) uintptr {
	value, ok := completionHandlers.Load(this)
	if !ok {
		return 0
	}
	return uintptr(value.(*processCompletionState).refs.Add(1))
}

func completionHandlerRelease(this uintptr) uintptr {
	value, ok := completionHandlers.Load(this)
	if !ok {
		return 0
	}
	state := value.(*processCompletionState)
	for {
		current := state.refs.Load()
		if current == 0 {
			return 0
		}
		if !state.refs.CompareAndSwap(current, current-1) {
			continue
		}
		remaining := current - 1
		if remaining == 0 {
			completionHandlers.Delete(this)
		}
		return uintptr(remaining)
	}
}

func completionHandlerActivateCompleted(this uintptr, operation *activateAsyncOperation) uintptr {
	value, ok := completionHandlers.Load(this)
	if !ok {
		return hResultPointer
	}
	state := value.(*processCompletionState)
	state.once.Do(func() {
		if operation == nil {
			state.result <- processActivationResult{err: errors.New("process-loopback activation completed without an operation")}
			return
		}
		client, err := operation.activateResult()
		state.result <- processActivationResult{client: client, err: err}
	})
	return hResultOK
}

func (operation *activateAsyncOperation) activateResult() (*wca.IAudioClient, error) {
	if operation == nil || operation.VTable == nil {
		return nil, errors.New("invalid process-loopback activation operation")
	}
	var activationResult int32
	var unknown *ole.IUnknown
	hresult, _, _ := syscall.SyscallN(
		operation.VTable.GetActivateResult,
		uintptr(unsafe.Pointer(operation)),
		uintptr(unsafe.Pointer(&activationResult)),
		uintptr(unsafe.Pointer(&unknown)),
	)
	if hresultFailed(hresult) {
		return nil, fmt.Errorf("get process-loopback activation result: %w", hresultError(hresult))
	}
	if activationResult < 0 {
		if unknown != nil {
			unknown.Release()
		}
		return nil, fmt.Errorf("process-loopback activation failed: %w", hresultError(uintptr(uint32(activationResult))))
	}
	if unknown == nil {
		return nil, errors.New("process-loopback activation returned no audio interface")
	}
	defer unknown.Release()

	var client *wca.IAudioClient
	if err := unknown.PutQueryInterface(wca.IID_IAudioClient, &client); err != nil {
		return nil, fmt.Errorf("query activated IAudioClient: %w", err)
	}
	if client == nil {
		return nil, errors.New("activated IAudioClient is nil")
	}
	return client, nil
}

func (operation *activateAsyncOperation) release() {
	if operation == nil || operation.VTable == nil {
		return
	}
	syscall.SyscallN(operation.VTable.Release, uintptr(unsafe.Pointer(operation)))
}

func hresultFailed(value uintptr) bool {
	return int32(uint32(value)) < 0
}

func hresultError(value uintptr) error {
	return ole.NewError(uintptr(uint32(value)))
}
