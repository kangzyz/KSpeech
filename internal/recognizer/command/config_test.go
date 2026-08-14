package command

import (
	"reflect"
	"testing"

	"github.com/kangzyz/KSpeech/internal/plugin"
)

func TestLoadConfigAcceptsLegacyJSONFields(t *testing.T) {
	recognizer := New(plugin.Metadata{Name: "caller metadata"})
	data := []byte(`{
		"Command":"python.exe",
		"Arguments":"-u \"script name.py\"",
		"WorkingDirectory":"C:\\speech",
		"LogFile":"C:\\logs\\stderr.log"
	}`)
	if err := recognizer.LoadConfig(data); err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	want := Config{
		Command:          "python.exe",
		Arguments:        `-u "script name.py"`,
		WorkingDirectory: `C:\speech`,
		LogFile:          `C:\logs\stderr.log`,
	}
	if got := recognizer.Config(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Config() = %#v, want %#v", got, want)
	}
	if got := recognizer.Metadata(); got.Name != "caller metadata" {
		t.Fatalf("Metadata() = %#v; constructor metadata was not retained", got)
	}
	if recognizer.NeedsAudio() {
		t.Fatal("NeedsAudio() = true, want false")
	}
	if err := recognizer.Feed([]float32{0.1, -0.1}); err != nil {
		t.Fatalf("Feed() error = %v", err)
	}
}

func TestLoadConfigMalformedDoesNotReplacePreviousConfig(t *testing.T) {
	recognizer := New(plugin.Metadata{})
	if err := recognizer.LoadConfig([]byte(`{"Command":"valid"}`)); err != nil {
		t.Fatal(err)
	}
	if err := recognizer.LoadConfig([]byte(`{"Command":`)); err == nil {
		t.Fatal("LoadConfig() accepted malformed JSON")
	}
	if got := recognizer.Config().Command; got != "valid" {
		t.Fatalf("Command = %q after malformed JSON, want previous value", got)
	}
}

func TestSplitArguments(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"", nil},
		{"-u script.py", []string{"-u", "script.py"}},
		{`--model "models/speech model" --name '中文 模型'`, []string{"--model", "models/speech model", "--name", "中文 模型"}},
		{`--empty "" C:\models\speech`, []string{"--empty", "", `C:\models\speech`}},
		{`--share \\server\speech`, []string{"--share", `\\server\speech`}},
		{`escaped\ value quote\"value`, []string{"escaped value", `quote"value`}},
	}
	for _, test := range tests {
		got, err := splitArguments(test.input)
		if err != nil {
			t.Fatalf("splitArguments(%q) error = %v", test.input, err)
		}
		if !reflect.DeepEqual(got, test.want) {
			t.Fatalf("splitArguments(%q) = %#v, want %#v", test.input, got, test.want)
		}
	}
	if _, err := splitArguments(`"unterminated`); err == nil {
		t.Fatal("splitArguments() accepted an unterminated quote")
	}
}
