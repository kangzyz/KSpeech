// Package punctuation restores punctuation for finalized captions.
//
// Streaming speech models emit bare text, so the sentence marks have to be
// added afterwards. Two implementations share one contract: a dependency-free
// rule pass that only closes the sentence, and the sherpa-onnx CT-Transformer
// model, which also breaks the sentence up with commas but is compiled in only
// by native builds.
package punctuation

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Mode selects the implementation used for one recognition run.
type Mode string

const (
	ModeOff   Mode = "off"
	ModeRules Mode = "rules"
	ModeModel Mode = "model"
)

// DefaultMode punctuates every finished sentence without an extra download, so
// captions read as sentences out of the box.
const DefaultMode = ModeRules

var (
	// ErrUnavailable reports that the punctuation model backend was not
	// compiled into this binary.
	ErrUnavailable = errors.New("punctuation model support is unavailable in this build")
	// ErrModelFile reports a missing or unusable punctuation model file.
	ErrModelFile = errors.New("invalid punctuation model file")
	// ErrInvalidMode reports an unknown stored mode.
	ErrInvalidMode = errors.New("unknown punctuation mode")
)

// ParseMode maps one stored configuration value onto a mode. An empty value
// selects DefaultMode so a configuration file written before this setting
// existed still gets punctuation. An unknown value also falls back to
// DefaultMode, together with an error the caller can report.
func ParseMode(value string) (Mode, error) {
	switch Mode(strings.ToLower(strings.TrimSpace(value))) {
	case "":
		return DefaultMode, nil
	case ModeOff:
		return ModeOff, nil
	case ModeRules:
		return ModeRules, nil
	case ModeModel:
		return ModeModel, nil
	default:
		return DefaultMode, fmt.Errorf("%w: %q", ErrInvalidMode, value)
	}
}

// ModelAvailable reports whether ModeModel can be used by this build.
func ModelAvailable() bool { return modelBackendAvailable }

// Config describes the punctuation pass of one run.
type Config struct {
	Mode Mode
	// ModelPath is the CT-Transformer model.onnx used by ModeModel.
	ModelPath  string
	NumThreads int
	Provider   string
	Debug      bool
}

// Punctuator rewrites one finalized sentence. Implementations own native
// resources and are not safe for concurrent use; callers serialize calls and
// call Close exactly once when the run ends.
type Punctuator interface {
	Punctuate(string) string
	Close() error
}

// New builds the punctuator for one run. A configuration that cannot be
// honoured returns an error instead of silently degrading, so the caller
// decides between failing and falling back to Rules.
func New(config Config) (Punctuator, error) {
	switch config.Mode {
	case ModeOff:
		return Disabled(), nil
	case ModeRules:
		return Rules(), nil
	case ModeModel:
		if !modelBackendAvailable {
			return nil, ErrUnavailable
		}
		path, err := resolveModelFile(config.ModelPath)
		if err != nil {
			return nil, err
		}
		config.ModelPath = path
		if config.NumThreads <= 0 {
			config.NumThreads = 1
		}
		if strings.TrimSpace(config.Provider) == "" {
			config.Provider = "cpu"
		}
		return newModelPunctuator(config)
	default:
		return nil, fmt.Errorf("%w: %q", ErrInvalidMode, config.Mode)
	}
}

// Disabled returns a punctuator that returns every sentence unchanged.
func Disabled() Punctuator { return disabledPunctuator{} }

type disabledPunctuator struct{}

func (disabledPunctuator) Punctuate(text string) string { return text }
func (disabledPunctuator) Close() error                 { return nil }

func resolveModelFile(value string) (string, error) {
	path := strings.TrimSpace(value)
	if path == "" {
		return "", fmt.Errorf("%w: no model file is configured", ErrModelFile)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%w: resolve %q: %v", ErrModelFile, path, err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("%w: %q: %v", ErrModelFile, absolute, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %q is not a regular file", ErrModelFile, absolute)
	}
	return absolute, nil
}
