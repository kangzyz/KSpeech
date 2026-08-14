package config

import (
	"strings"
	"testing"
)

type stubSettings map[string]string

func (s stubSettings) String(key string) string { return s[key] }

// A configuration written before multi-input capture has no audio.channels at
// all, and its single input must stay unlabeled so its captions look the same.
func TestAudioChannelListFallsBackToTheLegacySource(t *testing.T) {
	t.Parallel()
	channels := AudioChannelList(stubSettings{AudioSource: "loopback"})
	if len(channels) != 1 || channels[0].Source != "loopback" || channels[0].Label != "" {
		t.Fatalf("channels = %#v, want the legacy source unlabeled", channels)
	}
	if got := AudioChannelList(stubSettings{}); got != nil {
		t.Fatalf("channels without any configuration = %#v, want none", got)
	}
}

func TestAudioChannelListDropsRepeatsAndBoundsCount(t *testing.T) {
	t.Parallel()
	settings := stubSettings{
		AudioSource: "loopback",
		AudioChannels: `[{"source":"microphone","label":" 我 "},{"source":"microphone","label":"我又来了"},
			{"source":"loopback","label":"其他人"},{"source":" ","label":"空"},
			{"source":"a"},{"source":"b"},{"source":"c"}]`,
	}
	channels := AudioChannelList(settings)
	if len(channels) != MaxAudioChannels {
		t.Fatalf("channels = %#v, want %d", channels, MaxAudioChannels)
	}
	if channels[0].Source != "microphone" || channels[0].Label != "我" {
		t.Fatalf("first channel = %#v, want the trimmed microphone label", channels[0])
	}
	if channels[1].Source != "loopback" || channels[1].Label != "其他人" {
		t.Fatalf("second channel = %#v", channels[1])
	}
	for index, channel := range channels {
		for _, other := range channels[index+1:] {
			if channel.Source == other.Source {
				t.Fatalf("source %q was captured twice: %#v", channel.Source, channels)
			}
		}
	}
}

// A damaged value must cost the labels, not recognition itself.
func TestAudioChannelListIgnoresDamagedValue(t *testing.T) {
	t.Parallel()
	settings := stubSettings{AudioSource: "loopback", AudioChannels: `[{"source":`}
	channels := AudioChannelList(settings)
	if len(channels) != 1 || channels[0].Source != "loopback" {
		t.Fatalf("channels = %#v, want the legacy fallback", channels)
	}
}

func TestNormalizeChannelLabelBoundsLength(t *testing.T) {
	t.Parallel()
	if got := NormalizeChannelLabel("  我  "); got != "我" {
		t.Fatalf("label = %q, want it trimmed", got)
	}
	long := strings.Repeat("说话人", 10)
	if got := []rune(NormalizeChannelLabel(long)); len(got) > MaxChannelLabelRunes {
		t.Fatalf("label length = %d, want at most %d", len(got), MaxChannelLabelRunes)
	}
}

func TestEncodeAudioChannelsRoundTrips(t *testing.T) {
	t.Parallel()
	want := []Channel{{Source: "microphone", Label: "我"}, {Source: "loopback", Label: "其他人"}}
	encoded, err := EncodeAudioChannels(want)
	if err != nil {
		t.Fatal(err)
	}
	got := AudioChannelList(stubSettings{AudioChannels: encoded})
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("round trip = %#v, want %#v", got, want)
	}
	if encoded, err := EncodeAudioChannels(nil); err != nil || encoded != "" {
		t.Fatalf("empty encode = %q, %v", encoded, err)
	}
}

func TestDefaultChannelLabelNamesTheSpeaker(t *testing.T) {
	t.Parallel()
	microphone := PluginKey(AudioSourceWindowsModule, MicrophoneAudioSourceID)
	if got := DefaultChannelLabel(microphone); got != "我" {
		t.Fatalf("microphone label = %q, want 我", got)
	}
	loopback := PluginKey(AudioSourceWindowsModule, LoopbackAudioSourceID)
	if got := DefaultChannelLabel(loopback); got != "其他人" {
		t.Fatalf("loopback label = %q, want 其他人", got)
	}
	if got := DefaultChannelLabel("something-else"); got != "" {
		t.Fatalf("unknown source label = %q, want empty", got)
	}
}
