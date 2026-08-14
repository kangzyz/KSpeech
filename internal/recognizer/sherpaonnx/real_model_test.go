//go:build sherpa && cgo

package sherpaonnx

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kangzyz/KSpeech/internal/plugin"
)

const onnxRealModelLayout = `KSPEECH_REAL_MODEL_ROOT/
  test.wav                 (PCM16, mono, 16000 Hz)
  onnx/
    encoder.onnx
    decoder.onnx
    joiner.onnx
    tokens.txt`

func TestRealModelRecognition(t *testing.T) {
	root := realONNXModelRoot(t)
	modelDir := filepath.Join(root, "onnx")
	paths := map[string]string{
		"encoder.onnx": filepath.Join(modelDir, "encoder.onnx"),
		"decoder.onnx": filepath.Join(modelDir, "decoder.onnx"),
		"joiner.onnx":  filepath.Join(modelDir, "joiner.onnx"),
		"tokens.txt":   filepath.Join(modelDir, "tokens.txt"),
		"test.wav":     filepath.Join(root, "test.wav"),
	}
	for name, path := range paths {
		requireONNXRealModelFile(t, name, path)
	}

	samples, err := readONNXPCM16Mono16KWAV(paths["test.wav"])
	if err != nil {
		t.Fatalf("read real-model fixture %q: %v\nrequired layout:\n%s", paths["test.wav"], err, onnxRealModelLayout)
	}

	config := DefaultConfig()
	config.Encoder = paths["encoder.onnx"]
	config.Decoder = paths["decoder.onnx"]
	config.Joiner = paths["joiner.onnx"]
	config.Tokens = paths["tokens.txt"]
	config.Endpoint = false
	config.Debug = false
	configJSON, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal real-model config: %v", err)
	}

	recognizer := New(plugin.Metadata{ID: "test-real-sherpa-onnx", Name: "Real Sherpa ONNX"})
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = recognizer.Close()
		}
	})
	if err := recognizer.LoadConfig(configJSON); err != nil {
		t.Fatalf("load real-model config: %v\nrequired layout:\n%s", err, onnxRealModelLayout)
	}
	if recognizer.Config().Endpoint {
		t.Fatal("real-model test requires Endpoint=false")
	}

	var resultsMu sync.Mutex
	var partials []string
	var finals []string
	var callbackErrors []error
	recognizer.SetCallbacks(plugin.RecognizerCallbacks{
		Partial: func(text plugin.Text) {
			resultsMu.Lock()
			partials = append(partials, text.Text)
			resultsMu.Unlock()
		},
		Final: func(text plugin.Text) {
			resultsMu.Lock()
			finals = append(finals, text.Text)
			resultsMu.Unlock()
		},
		Error: func(err error) {
			resultsMu.Lock()
			callbackErrors = append(callbackErrors, err)
			resultsMu.Unlock()
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := recognizer.Start(ctx); err != nil {
		t.Fatalf("start real sherpa-onnx recognizer: %v\nrequired layout:\n%s", err, onnxRealModelLayout)
	}
	feedONNXRealModelAudio(t, recognizer, samples)
	if err := recognizer.Stop(); err != nil {
		t.Fatalf("stop real sherpa-onnx recognizer: %v", err)
	}
	if err := recognizer.Close(); err != nil {
		t.Fatalf("close real sherpa-onnx recognizer: %v", err)
	}
	closed = true

	resultsMu.Lock()
	gotPartials := append([]string(nil), partials...)
	gotFinals := append([]string(nil), finals...)
	gotErrors := append([]error(nil), callbackErrors...)
	resultsMu.Unlock()
	if len(gotErrors) != 0 {
		t.Fatalf("Error callbacks = %v, want none", gotErrors)
	}
	if len(gotPartials) == 0 {
		t.Fatal("partial callbacks = 0, want at least one")
	}
	if len(gotFinals) == 0 {
		t.Fatal("final callbacks = 0, want at least one")
	}
	combinedFinal := strings.Join(gotFinals, "")
	for _, phrase := range []string{"对我做了介绍", "研究感兴趣"} {
		if !strings.Contains(combinedFinal, phrase) {
			t.Fatalf("combined final result %q does not contain %q (all finals: %q)", combinedFinal, phrase, gotFinals)
		}
	}
}

func realONNXModelRoot(t *testing.T) string {
	t.Helper()
	root := strings.TrimSpace(os.Getenv("KSPEECH_REAL_MODEL_ROOT"))
	if root == "" {
		t.Skipf("KSPEECH_REAL_MODEL_ROOT is unset; real sherpa-onnx test requires:\n%s", onnxRealModelLayout)
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("resolve KSPEECH_REAL_MODEL_ROOT %q: %v\nrequired layout:\n%s", root, err, onnxRealModelLayout)
	}
	return absolute
}

func requireONNXRealModelFile(t *testing.T, name, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("required real-model file %s at %q: %v\nrequired layout:\n%s", name, path, err, onnxRealModelLayout)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("required real-model file %s at %q is not a regular file\nrequired layout:\n%s", name, path, onnxRealModelLayout)
	}
}

func feedONNXRealModelAudio(t *testing.T, recognizer *Recognizer, samples []float32) {
	t.Helper()
	const chunkSamples = plugin.AudioSampleRate / 5 // 200 ms
	for offset := 0; offset < len(samples); offset += chunkSamples {
		end := min(offset+chunkSamples, len(samples))
		deadline := time.Now().Add(30 * time.Second)
		for {
			err := recognizer.Feed(samples[offset:end])
			if err == nil {
				break
			}
			if errors.Is(err, ErrAudioQueueFull) && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
				continue
			}
			t.Fatalf("feed 200 ms chunk at sample %d: %v", offset, err)
		}
	}
}

func readONNXPCM16Mono16KWAV(path string) ([]float32, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, errors.New("expected a RIFF/WAVE file")
	}
	declaredSize := uint64(binary.LittleEndian.Uint32(data[4:8])) + 8
	if declaredSize != uint64(len(data)) {
		return nil, fmt.Errorf("RIFF size is %d bytes, file size is %d", declaredSize, len(data))
	}

	var formatSeen bool
	var audioData []byte
	for offset := 12; offset < len(data); {
		if len(data)-offset < 8 {
			return nil, fmt.Errorf("truncated chunk header at byte %d", offset)
		}
		chunkID := string(data[offset : offset+4])
		chunkSize := uint64(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		payloadStart := uint64(offset + 8)
		payloadEnd := payloadStart + chunkSize
		if payloadEnd > uint64(len(data)) {
			return nil, fmt.Errorf("chunk %q at byte %d exceeds file size", chunkID, offset)
		}
		payload := data[int(payloadStart):int(payloadEnd)]
		switch chunkID {
		case "fmt ":
			if formatSeen {
				return nil, errors.New("duplicate fmt chunk")
			}
			if len(payload) < 16 {
				return nil, fmt.Errorf("fmt chunk is %d bytes, want at least 16", len(payload))
			}
			formatSeen = true
			format := binary.LittleEndian.Uint16(payload[0:2])
			channels := binary.LittleEndian.Uint16(payload[2:4])
			sampleRate := binary.LittleEndian.Uint32(payload[4:8])
			byteRate := binary.LittleEndian.Uint32(payload[8:12])
			blockAlign := binary.LittleEndian.Uint16(payload[12:14])
			bitsPerSample := binary.LittleEndian.Uint16(payload[14:16])
			if format != 1 || channels != 1 || sampleRate != plugin.AudioSampleRate || byteRate != 32_000 || blockAlign != 2 || bitsPerSample != 16 {
				return nil, fmt.Errorf("want PCM16 mono 16000 Hz (format=1 channels=1 sampleRate=16000 byteRate=32000 blockAlign=2 bits=16), got format=%d channels=%d sampleRate=%d byteRate=%d blockAlign=%d bits=%d", format, channels, sampleRate, byteRate, blockAlign, bitsPerSample)
			}
		case "data":
			if audioData != nil {
				return nil, errors.New("duplicate data chunk")
			}
			audioData = payload
		}

		next := payloadEnd
		if chunkSize%2 != 0 {
			next++
		}
		if next > uint64(len(data)) {
			return nil, fmt.Errorf("chunk %q is missing its padding byte", chunkID)
		}
		offset = int(next)
	}
	if !formatSeen {
		return nil, errors.New("missing fmt chunk")
	}
	if len(audioData) == 0 {
		return nil, errors.New("missing or empty data chunk")
	}
	if len(audioData)%2 != 0 {
		return nil, fmt.Errorf("PCM16 data size %d is not divisible by 2", len(audioData))
	}

	samples := make([]float32, len(audioData)/2)
	for index := range samples {
		value := int16(binary.LittleEndian.Uint16(audioData[index*2 : index*2+2]))
		samples[index] = float32(value) / 32768
	}
	return samples, nil
}
