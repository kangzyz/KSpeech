//go:build !sherpa || !cgo

package sherpaonnx

import (
	"context"
	"errors"
	"testing"

	"github.com/kangzyz/KSpeech/internal/plugin"
)

func TestDefaultBuildIsClearUnavailableStub(t *testing.T) {
	recognizer := New(plugin.Metadata{ID: "sherpa"})
	if recognizer.Available() {
		t.Fatal("recognizer reports available without sherpa+cgo build")
	}
	if err := recognizer.Start(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Start error = %v, want ErrUnavailable", err)
	}
	if err := recognizer.Stop(); err != nil {
		t.Fatalf("idempotent Stop: %v", err)
	}
	if err := recognizer.Close(); err != nil {
		t.Fatalf("idempotent Close: %v", err)
	}
}
