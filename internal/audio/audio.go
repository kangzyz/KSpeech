// Package audio implements the built-in KSpeech audio sources.
package audio

import (
	"context"
	"errors"

	"github.com/kangzyz/KSpeech/internal/plugin"
)

var (
	ErrAlreadyRunning             = errors.New("audio source is already running")
	ErrNotSupported               = errors.New("audio source is not supported on this platform")
	ErrProcessLoopbackUnsupported = errors.New("per-process loopback capture is not available in this build")
)

type Device struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Default bool   `json:"default"`
}

type sourceKind uint8

const (
	sourceMicrophone sourceKind = iota
	sourceLoopback
	sourceProcess
)

// NewMicrophone returns the built-in Windows microphone source. Its legacy
// configuration is the MMDevice ID as a plain string; an empty ID uses the
// default communications capture endpoint.
func NewMicrophone(metadata plugin.Metadata) plugin.AudioSource {
	return newSource(metadata, sourceMicrophone)
}

// NewLoopback returns the built-in source that captures the default system
// render endpoint through WASAPI loopback.
func NewLoopback(metadata plugin.Metadata) plugin.AudioSource {
	return newSource(metadata, sourceLoopback)
}

// NewProcessLoopback returns a source for the target PID and its descendants.
// Availability is platform/build dependent and is reported by Available.
func NewProcessLoopback(metadata plugin.Metadata) plugin.AudioSource {
	return newSource(metadata, sourceProcess)
}

// Devices lists active microphone endpoints. Non-Windows builds return
// ErrNotSupported.
func Devices(ctx context.Context) ([]Device, error) {
	return enumerateDevices(ctx)
}
