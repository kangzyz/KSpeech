//go:build !windows

package audio

import (
	"context"

	"github.com/kangzyz/KSpeech/internal/plugin"
)

type unsupportedSource struct {
	metadata plugin.Metadata
}

func newSource(metadata plugin.Metadata, _ sourceKind) plugin.AudioSource {
	return &unsupportedSource{metadata: metadata}
}

func enumerateDevices(context.Context) ([]Device, error) { return nil, ErrNotSupported }

func (s *unsupportedSource) Metadata() plugin.Metadata          { return s.metadata }
func (s *unsupportedSource) Available() bool                    { return false }
func (s *unsupportedSource) LoadConfig([]byte) error            { return nil }
func (s *unsupportedSource) Init(context.Context) error         { return ErrNotSupported }
func (s *unsupportedSource) Close() error                       { return nil }
func (s *unsupportedSource) Start(context.Context) error        { return ErrNotSupported }
func (s *unsupportedSource) Stop() error                        { return nil }
func (s *unsupportedSource) SetCallbacks(plugin.AudioCallbacks) {}
