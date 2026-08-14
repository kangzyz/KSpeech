package config

import (
	"encoding/json"
	"strings"
)

const (
	// MaxAudioChannels bounds one run's simultaneous audio inputs. Every extra
	// channel costs its own recognizer instance, so the limit keeps a
	// hand-edited configuration from loading the speech model four times over.
	MaxAudioChannels = 4
	// MaxChannelLabelRunes bounds a speaker label. Labels are drawn in front of
	// every caption line, where a long one would push the words off screen.
	MaxChannelLabelRunes = 12
)

// Channel is one labeled audio input. Source is an audio source plugin key and
// Label is the speaker name shown in front of that input's captions; an empty
// label prints no prefix, which is what a single-input session wants.
type Channel struct {
	Source string `json:"source"`
	Label  string `json:"label,omitempty"`
}

// Settings is the read-only configuration view channel resolution needs. It is
// the same shape the job manager already depends on.
type Settings interface {
	String(key string) string
}

// AudioChannelList resolves the labeled audio inputs for the next run. An
// unset or unusable audio.channels value falls back to the single legacy
// audio.source key, so a configuration written before multi-input capture keeps
// running unchanged — and keeps its captions unlabeled.
func AudioChannelList(settings Settings) []Channel {
	channels := make([]Channel, 0, MaxAudioChannels)
	seen := make(map[string]bool)
	for _, channel := range decodeChannels(settings.String(AudioChannels)) {
		source := strings.TrimSpace(channel.Source)
		// One source can only be captured once: both instances would open the
		// same endpoint and transcribe the same words twice.
		if source == "" || seen[source] || len(channels) >= MaxAudioChannels {
			continue
		}
		seen[source] = true
		channels = append(channels, Channel{Source: source, Label: NormalizeChannelLabel(channel.Label)})
	}
	if len(channels) > 0 {
		return channels
	}
	source := strings.TrimSpace(settings.String(AudioSource))
	if source == "" {
		return nil
	}
	return []Channel{{Source: source}}
}

// EncodeAudioChannels renders channels for storage under audio.channels. The
// flat store keeps one JSON string per key, the same way plugin configuration
// is stored.
func EncodeAudioChannels(channels []Channel) (string, error) {
	if len(channels) == 0 {
		return "", nil
	}
	data, err := json.Marshal(channels)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// NormalizeChannelLabel trims a speaker label and bounds its length.
func NormalizeChannelLabel(label string) string {
	label = strings.TrimSpace(label)
	if runes := []rune(label); len(runes) > MaxChannelLabelRunes {
		return strings.TrimSpace(string(runes[:MaxChannelLabelRunes]))
	}
	return label
}

// DefaultChannelLabel is the speaker name proposed when an input is first
// enabled. The microphone is the person at this computer; everything else is
// sound the computer is playing, which in a meeting is everyone else.
func DefaultChannelLabel(source string) string {
	switch source {
	case PluginKey(AudioSourceWindowsModule, MicrophoneAudioSourceID):
		return "我"
	case PluginKey(AudioSourceWindowsModule, LoopbackAudioSourceID),
		PluginKey(AudioSourceWindowsModule, ProcessAudioSourceID):
		return "其他人"
	default:
		return ""
	}
}

func decodeChannels(value string) []Channel {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	var channels []Channel
	if err := json.Unmarshal([]byte(value), &channels); err != nil {
		// A damaged value must not silence recognition: the caller falls back
		// to the single legacy audio source instead.
		return nil
	}
	return channels
}
