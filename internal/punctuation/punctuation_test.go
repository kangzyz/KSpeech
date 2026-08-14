package punctuation

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestParseMode(t *testing.T) {
	cases := []struct {
		in      string
		want    Mode
		wantErr bool
	}{
		{in: "", want: DefaultMode},
		{in: "off", want: ModeOff},
		{in: " Rules ", want: ModeRules},
		{in: "model", want: ModeModel},
		{in: "punctuate-everything", want: DefaultMode, wantErr: true},
	}
	for _, test := range cases {
		mode, err := ParseMode(test.in)
		if mode != test.want {
			t.Fatalf("ParseMode(%q) = %q, want %q", test.in, mode, test.want)
		}
		if test.wantErr != (err != nil) {
			t.Fatalf("ParseMode(%q) error = %v, wantErr %v", test.in, err, test.wantErr)
		}
		if test.wantErr && !errors.Is(err, ErrInvalidMode) {
			t.Fatalf("ParseMode(%q) error = %v, want ErrInvalidMode", test.in, err)
		}
	}
}

func TestNewDisabledLeavesTextAlone(t *testing.T) {
	punctuator, err := New(Config{Mode: ModeOff})
	if err != nil {
		t.Fatal(err)
	}
	defer punctuator.Close()
	const text = "这句不加标点"
	if got := punctuator.Punctuate(text); got != text {
		t.Fatalf("Punctuate(%q) = %q, want it unchanged", text, got)
	}
}

func TestNewRules(t *testing.T) {
	punctuator, err := New(Config{Mode: ModeRules})
	if err != nil {
		t.Fatal(err)
	}
	defer punctuator.Close()
	if got := punctuator.Punctuate("这句要加标点"); got != "这句要加标点。" {
		t.Fatalf("Punctuate() = %q, want a full stop", got)
	}
}

func TestNewRejectsUnknownMode(t *testing.T) {
	if _, err := New(Config{Mode: Mode("later")}); !errors.Is(err, ErrInvalidMode) {
		t.Fatalf("New() error = %v, want ErrInvalidMode", err)
	}
}

// The model path is validated before the backend is asked for anything, so a
// stub build and a native build reject the same misconfiguration.
func TestNewModelChecksConfiguration(t *testing.T) {
	directory := t.TempDir()
	missing := filepath.Join(directory, "model.onnx")
	_, err := New(Config{Mode: ModeModel, ModelPath: missing})
	if ModelAvailable() {
		if !errors.Is(err, ErrModelFile) {
			t.Fatalf("New() error = %v, want ErrModelFile", err)
		}
	} else if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("New() error = %v, want ErrUnavailable", err)
	}

	if _, err := New(Config{Mode: ModeModel}); err == nil {
		t.Fatal("New() with an empty model path returned no error")
	}

	if ModelAvailable() {
		if err := os.WriteFile(missing, []byte("not a model"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := New(Config{Mode: ModeModel, ModelPath: directory}); !errors.Is(err, ErrModelFile) {
			t.Fatalf("New() with a directory error = %v, want ErrModelFile", err)
		}
	}
}
