package audio

import (
	"context"
	"runtime"
	"testing"

	"github.com/kangzyz/KSpeech/internal/plugin"
)

func TestSourcesExposeCallerMetadata(t *testing.T) {
	t.Parallel()
	metadata := plugin.Metadata{ID: "source", Name: "Source"}
	sources := []plugin.AudioSource{
		NewMicrophone(metadata),
		NewLoopback(metadata),
		NewProcessLoopback(metadata),
	}
	for _, source := range sources {
		if got := source.Metadata(); got != metadata {
			t.Fatalf("Metadata() = %#v", got)
		}
	}
}

func TestProcessSourceAvailabilityIsPlatformBound(t *testing.T) {
	t.Parallel()
	source := NewProcessLoopback(plugin.Metadata{})
	if runtime.GOOS != "windows" && source.Available() {
		t.Fatal("process capture must not report available outside Windows")
	}
	if runtime.GOOS != "windows" {
		if err := source.Start(context.Background()); err != ErrNotSupported {
			t.Fatalf("Start() error = %v, want %v", err, ErrNotSupported)
		}
	}
}
