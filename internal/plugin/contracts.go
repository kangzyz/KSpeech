// Package plugin defines the stable contracts shared by KSpeech plugins.
package plugin

import (
	"context"
	"errors"
	"time"
)

const (
	// AudioSampleRate is the sample rate expected by recognizers that need audio.
	AudioSampleRate = 16_000
	// AudioChannels documents that audio samples are normalized mono PCM.
	AudioChannels = 1
)

// ErrAudioBackpressure marks a Feed call that refused audio instead of blocking
// the capture callback behind inference. Recognizers wrap it so callers can tell
// a dropped chunk, which costs a gap in one sentence, apart from a failure that
// has to end the run. It matters most when several inputs are captured at once,
// because they share the machine's CPU.
var ErrAudioBackpressure = errors.New("audio queue is full")

// Metadata describes a plugin. Metadata is supplied by the caller that wires a
// plugin into the application; implementations do not invent registry keys or
// deployment metadata themselves.
type Metadata struct {
	ID             string
	Name           string
	Description    string
	Version        string
	SupportVersion string
	Author         string
	URL            string
	License        string
	Note           string
}

// Plugin is the common lifecycle and configuration contract for all plugins.
// LoadConfig receives the plugin-specific JSON value stored by KSpeech.
type Plugin interface {
	Metadata() Metadata
	Available() bool
	LoadConfig([]byte) error
	Init(context.Context) error
	Close() error
}

// Runner is implemented by plugins that own a running resource.
type Runner interface {
	Start(context.Context) error
	Stop() error
}

// Text is one partial or final recognition result.
type Text struct {
	Time time.Time
	Text string
}

// RecognizerCallbacks receives recognition results, recoverable warnings, and
// asynchronous failures. Warning reports a degraded but still usable
// recognizer and must not be used for failures that require the active run to
// stop; Error retains that fatal meaning. Implementations must permit
// SetCallbacks while stopped or running.
type RecognizerCallbacks struct {
	Partial func(Text)
	Final   func(Text)
	Warning func(error)
	Error   func(error)
}

// Recognizer converts audio into text. Feed contains normalized 16 kHz mono
// float32 PCM. Recognizers for external programs may return false from
// NeedsAudio and intentionally ignore Feed.
type Recognizer interface {
	Plugin
	Runner
	NeedsAudio() bool
	Feed([]float32) error
	SetCallbacks(RecognizerCallbacks)
}

// AudioCallbacks receives normalized 16 kHz mono float32 PCM and asynchronous
// source failures.
type AudioCallbacks struct {
	Data  func([]float32)
	Error func(error)
}

// AudioSource captures normalized 16 kHz mono audio for a Recognizer.
type AudioSource interface {
	Plugin
	Runner
	SetCallbacks(AudioCallbacks)
}

// Translator translates completed recognition text.
type Translator interface {
	Plugin
	Translate(context.Context, string) (string, error)
}
