package command

import (
	"reflect"
	"testing"
)

func parseChunks(chunks ...[]byte) (partials, finals []string) {
	parser := newStdoutParser(
		func(text string) { partials = append(partials, text) },
		func(text string) { finals = append(finals, text) },
	)
	for _, chunk := range chunks {
		_, _ = parser.Write(chunk)
	}
	return partials, finals
}

func TestStdoutParserProtocol(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantPartials []string
		wantFinals   []string
	}{
		{
			name:         "single LF is partial",
			input:        "hello\n",
			wantPartials: []string{"hello"},
		},
		{
			name:         "second consecutive LF finalizes previous partial",
			input:        "hello\n\n",
			wantPartials: []string{"hello"},
			wantFinals:   []string{"hello"},
		},
		{
			name:         "CRLF and standalone CR are ignored",
			input:        "你\r好\r\n\r\n",
			wantPartials: []string{"你好"},
			wantFinals:   []string{"你好"},
		},
		{
			name:         "new text replaces repeated partial",
			input:        "语\n语音\n语音识别\n\n",
			wantPartials: []string{"语", "语音", "语音识别"},
			wantFinals:   []string{"语音识别"},
		},
		{
			name:  "empty lines emit no empty events",
			input: "\n\n\n\n",
		},
		{
			name:         "more than two LFs do not repeat final",
			input:        "one\n\n\n\n",
			wantPartials: []string{"one"},
			wantFinals:   []string{"one"},
		},
		{
			name:         "ordinary byte resets consecutive LF count",
			input:        "old\nnew\n\n",
			wantPartials: []string{"old", "new"},
			wantFinals:   []string{"new"},
		},
		{
			name:         "unterminated text is not emitted",
			input:        "waiting",
			wantPartials: nil,
			wantFinals:   nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			partials, finals := parseChunks([]byte(test.input))
			if !reflect.DeepEqual(partials, test.wantPartials) {
				t.Fatalf("partials = %#v, want %#v", partials, test.wantPartials)
			}
			if !reflect.DeepEqual(finals, test.wantFinals) {
				t.Fatalf("finals = %#v, want %#v", finals, test.wantFinals)
			}
		})
	}
}

func TestStdoutParserPreservesSplitUTF8(t *testing.T) {
	input := []byte("中文识别\n\n")
	chunks := make([][]byte, 0, len(input))
	for index := range input {
		chunks = append(chunks, input[index:index+1])
	}
	partials, finals := parseChunks(chunks...)
	if want := []string{"中文识别"}; !reflect.DeepEqual(partials, want) {
		t.Fatalf("partials = %#v, want %#v", partials, want)
	}
	if want := []string{"中文识别"}; !reflect.DeepEqual(finals, want) {
		t.Fatalf("finals = %#v, want %#v", finals, want)
	}
}

func TestStdoutParserReplacesInvalidUTF8(t *testing.T) {
	partials, finals := parseChunks([]byte{'a', 0xff, '\n', '\n'})
	if want := []string{"a\uFFFD"}; !reflect.DeepEqual(partials, want) {
		t.Fatalf("partials = %#v, want %#v", partials, want)
	}
	if want := []string{"a\uFFFD"}; !reflect.DeepEqual(finals, want) {
		t.Fatalf("finals = %#v, want %#v", finals, want)
	}
}
