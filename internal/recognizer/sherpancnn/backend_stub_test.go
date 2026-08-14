//go:build !sherpancnn || !cgo || (windows && !amd64 && !386)

package sherpancnn

import (
	"context"
	"errors"
	"testing"

	"github.com/kangzyz/KSpeech/internal/plugin"
)

func TestDefaultBuildReportsNativeBackendUnavailable(t *testing.T) {
	recognizer := New(plugin.Metadata{ID: "ncnn"})
	if recognizer.Available() {
		t.Fatal("recognizer reports available without sherpancnn+cgo")
	}
	if err := recognizer.Start(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Start error = %v, want ErrUnavailable", err)
	}
}
