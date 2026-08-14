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
	"time"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/kangzyz/KSpeech/internal/plugin"
	"github.com/moutend/go-wca/pkg/wca"
)

const (
	waveFormatIEEEFloat = 3
	bufferDuration      = wca.REFERENCE_TIME(1_000_000) // 100 ms in 100 ns units.
	pollInterval        = 10 * time.Millisecond
)

type windowsSource struct {
	metadata plugin.Metadata
	kind     sourceKind

	mu        sync.Mutex
	config    string
	cancel    context.CancelFunc
	done      chan struct{}
	callbacks plugin.AudioCallbacks
}

func newSource(metadata plugin.Metadata, kind sourceKind) plugin.AudioSource {
	return &windowsSource{metadata: metadata, kind: kind}
}

func (s *windowsSource) Metadata() plugin.Metadata { return s.metadata }

func (s *windowsSource) Available() bool {
	if s.kind == sourceProcess {
		return processLoopbackAvailable()
	}
	return true
}

func (s *windowsSource) LoadConfig(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return errors.New("cannot change audio source configuration while running")
	}
	s.config = strings.TrimSpace(string(data))
	if s.kind == sourceProcess && s.config != "" {
		if pid, err := strconv.ParseUint(s.config, 10, 32); err != nil || pid == 0 {
			return fmt.Errorf("invalid process ID %q", s.config)
		}
	}
	return nil
}

func (s *windowsSource) Init(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func (s *windowsSource) Close() error { return s.Stop() }

func (s *windowsSource) SetCallbacks(callbacks plugin.AudioCallbacks) {
	s.mu.Lock()
	s.callbacks = callbacks
	s.mu.Unlock()
}

func (s *windowsSource) Start(parent context.Context) error {
	if s.kind == sourceProcess && !processLoopbackAvailable() {
		return ErrProcessLoopbackUnsupported
	}
	if parent == nil {
		parent = context.Background()
	}
	if err := parent.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return ErrAlreadyRunning
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	ready := make(chan error, 1)
	s.cancel = cancel
	s.done = done
	config := s.config
	s.mu.Unlock()

	go s.run(ctx, config, ready, done)
	select {
	case err := <-ready:
		if err != nil {
			<-done
		}
		return err
	case <-parent.Done():
		cancel()
		<-done
		return parent.Err()
	}
}

func (s *windowsSource) Stop() error {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-time.After(3 * time.Second):
		return errors.New("timed out stopping WASAPI capture")
	}
}

func (s *windowsSource) run(ctx context.Context, config string, ready chan<- error, done chan struct{}) {
	defer close(done)
	defer func() {
		s.mu.Lock()
		if s.done == done {
			s.cancel = nil
			s.done = nil
		}
		s.mu.Unlock()
	}()

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := initializeCOMMultithreaded(); err != nil {
		ready <- fmt.Errorf("initialize Windows audio COM: %w", err)
		return
	}
	defer ole.CoUninitialize()

	var client *wca.IAudioClient
	if s.kind == sourceProcess {
		pid, err := parseProcessID(config)
		if err != nil {
			ready <- err
			return
		}
		if err := validateProcess(pid); err != nil {
			ready <- err
			return
		}
		client, err = activateProcessAudioClient(ctx, pid)
		if err != nil {
			ready <- fmt.Errorf("activate Windows process-loopback audio client for PID %d: %w", pid, err)
			return
		}
	} else {
		device, err := openDevice(s.kind, config)
		if err != nil {
			ready <- err
			return
		}
		defer device.Release()

		if err := device.Activate(wca.IID_IAudioClient, wca.CLSCTX_ALL, nil, &client); err != nil {
			ready <- fmt.Errorf("activate Windows audio client: %w", err)
			return
		}
	}
	defer client.Release()

	format := &wca.WAVEFORMATEX{
		WFormatTag:      waveFormatIEEEFloat,
		NChannels:       plugin.AudioChannels,
		NSamplesPerSec:  plugin.AudioSampleRate,
		NAvgBytesPerSec: plugin.AudioSampleRate * 4,
		NBlockAlign:     4,
		WBitsPerSample:  32,
	}
	flags := uint32(wca.AUDCLNT_STREAMFLAGS_AUTOCONVERTPCM | wca.AUDCLNT_STREAMFLAGS_SRC_DEFAULT_QUALITY)
	if s.kind == sourceLoopback || s.kind == sourceProcess {
		flags |= wca.AUDCLNT_STREAMFLAGS_LOOPBACK
	}
	if err := client.Initialize(wca.AUDCLNT_SHAREMODE_SHARED, flags, bufferDuration, 0, format, nil); err != nil {
		ready <- fmt.Errorf("initialize Windows audio stream as float32/16kHz/mono: %w", err)
		return
	}

	var capture *wca.IAudioCaptureClient
	if err := client.GetService(wca.IID_IAudioCaptureClient, &capture); err != nil {
		ready <- fmt.Errorf("open Windows audio capture service: %w", err)
		return
	}
	defer capture.Release()
	if err := client.Start(); err != nil {
		ready <- fmt.Errorf("start Windows audio capture: %w", err)
		return
	}
	defer client.Stop()
	ready <- nil

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.readAvailable(capture); err != nil {
				s.emitError(err)
				return
			}
		}
	}
}

func (s *windowsSource) readAvailable(capture *wca.IAudioCaptureClient) error {
	for {
		var packetFrames uint32
		if err := capture.GetNextPacketSize(&packetFrames); err != nil {
			return fmt.Errorf("read Windows audio packet size: %w", err)
		}
		if packetFrames == 0 {
			return nil
		}

		var data *byte
		var frames, flags uint32
		var devicePosition, qpcPosition uint64
		if err := capture.GetBuffer(&data, &frames, &flags, &devicePosition, &qpcPosition); err != nil {
			return fmt.Errorf("acquire Windows audio buffer: %w", err)
		}
		samples := make([]float32, frames)
		if flags&wca.AUDCLNT_BUFFERFLAGS_SILENT == 0 && data != nil && frames > 0 {
			copy(samples, unsafe.Slice((*float32)(unsafe.Pointer(data)), int(frames)))
		}
		if err := capture.ReleaseBuffer(frames); err != nil {
			return fmt.Errorf("release Windows audio buffer: %w", err)
		}
		if len(samples) > 0 {
			s.emitData(samples)
		}
	}
}

func (s *windowsSource) emitData(samples []float32) {
	s.mu.Lock()
	callback := s.callbacks.Data
	s.mu.Unlock()
	if callback != nil {
		callback(samples)
	}
}

func (s *windowsSource) emitError(err error) {
	s.mu.Lock()
	callback := s.callbacks.Error
	s.mu.Unlock()
	if callback != nil {
		callback(err)
	}
}

func openDevice(kind sourceKind, deviceID string) (*wca.IMMDevice, error) {
	var enumerator *wca.IMMDeviceEnumerator
	if err := wca.CoCreateInstance(wca.CLSID_MMDeviceEnumerator, 0, wca.CLSCTX_ALL, wca.IID_IMMDeviceEnumerator, &enumerator); err != nil {
		return nil, fmt.Errorf("create Windows audio device enumerator: %w", err)
	}
	defer enumerator.Release()

	flow := uint32(wca.ECapture)
	role := uint32(wca.ECommunications)
	if kind == sourceLoopback {
		flow = wca.ERender
		role = wca.EConsole
	}
	if deviceID == "" || kind == sourceLoopback {
		var device *wca.IMMDevice
		if err := enumerator.GetDefaultAudioEndpoint(flow, role, &device); err != nil {
			return nil, fmt.Errorf("open default Windows audio endpoint: %w", err)
		}
		return device, nil
	}

	var collection *wca.IMMDeviceCollection
	if err := enumerator.EnumAudioEndpoints(flow, wca.DEVICE_STATE_ACTIVE, &collection); err != nil {
		return nil, fmt.Errorf("enumerate Windows audio endpoints: %w", err)
	}
	defer collection.Release()
	var count uint32
	if err := collection.GetCount(&count); err != nil {
		return nil, fmt.Errorf("count Windows audio endpoints: %w", err)
	}
	for index := uint32(0); index < count; index++ {
		var device *wca.IMMDevice
		if err := collection.Item(index, &device); err != nil {
			continue
		}
		id, err := safeDeviceID(device)
		if err == nil && id == deviceID {
			return device, nil
		}
		device.Release()
	}
	return nil, fmt.Errorf("Windows microphone device %q was not found", deviceID)
}

func enumerateDevices(ctx context.Context) ([]Device, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	result := make(chan struct {
		devices []Device
		err     error
	}, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := initializeCOMMultithreaded(); err != nil {
			result <- struct {
				devices []Device
				err     error
			}{nil, err}
			return
		}
		defer ole.CoUninitialize()
		devices, err := enumerateCaptureDevices()
		result <- struct {
			devices []Device
			err     error
		}{devices, err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case value := <-result:
		return value.devices, value.err
	}
}

func enumerateCaptureDevices() ([]Device, error) {
	var enumerator *wca.IMMDeviceEnumerator
	if err := wca.CoCreateInstance(wca.CLSID_MMDeviceEnumerator, 0, wca.CLSCTX_ALL, wca.IID_IMMDeviceEnumerator, &enumerator); err != nil {
		return nil, fmt.Errorf("create Windows audio device enumerator: %w", err)
	}
	defer enumerator.Release()

	defaultID := ""
	var defaultDevice *wca.IMMDevice
	if err := enumerator.GetDefaultAudioEndpoint(wca.ECapture, wca.ECommunications, &defaultDevice); err == nil {
		defaultID, _ = safeDeviceID(defaultDevice)
		defaultDevice.Release()
	}
	var collection *wca.IMMDeviceCollection
	if err := enumerator.EnumAudioEndpoints(wca.ECapture, wca.DEVICE_STATE_ACTIVE, &collection); err != nil {
		return nil, fmt.Errorf("enumerate Windows microphone endpoints: %w", err)
	}
	defer collection.Release()
	var count uint32
	if err := collection.GetCount(&count); err != nil {
		return nil, fmt.Errorf("count Windows microphone endpoints: %w", err)
	}
	devices := make([]Device, 0, count)
	for index := uint32(0); index < count; index++ {
		var endpoint *wca.IMMDevice
		if err := collection.Item(index, &endpoint); err != nil {
			continue
		}
		id, name := "", ""
		id, _ = safeDeviceID(endpoint)
		var store *wca.IPropertyStore
		if err := endpoint.OpenPropertyStore(wca.STGM_READ, &store); err == nil {
			var value wca.PROPVARIANT
			if err := store.GetValue(&wca.PKEY_Device_FriendlyName, &value); err == nil {
				// go-wca's PROPVARIANT.String copies the UTF-16 value and frees
				// the COM allocation itself. Calling PropVariantClear afterwards
				// would free the same pointer twice.
				name = value.String()
			}
			store.Release()
		}
		endpoint.Release()
		if id == "" {
			continue
		}
		if name == "" {
			name = id
		}
		devices = append(devices, Device{ID: id, Name: name, Default: id == defaultID})
	}
	return devices, nil
}

func initializeCOMMultithreaded() error {
	err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED)
	if err == nil {
		return nil
	}
	// go-ole treats every non-zero HRESULT as an error, but S_FALSE means
	// COM was already initialized with the requested model. It is successful
	// and still requires a matching CoUninitialize call.
	if oleError, ok := err.(*ole.OleError); ok && uint32(oleError.Code()) == 1 {
		return nil
	}
	return err
}
