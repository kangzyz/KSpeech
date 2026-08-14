package assistant

import (
	"strings"
	"testing"
	"time"
)

func TestFormatLinesNamesTheSpeaker(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 12, 15, 4, 5, 0, time.Local)
	got := formatLines([]Line{
		{Time: at, Speaker: "我", Text: "我这边下周就能交"},
		{Time: at, Text: "没有说话人标签的一句"},
		{Speaker: "其他人", Text: "没有时间的一句"},
	})
	want := "[15:04:05 我] 我这边下周就能交\n[15:04:05] 没有说话人标签的一句\n其他人：没有时间的一句"
	if got != want {
		t.Fatalf("formatLines() = %q, want %q", got, want)
	}
}

// The model can only tell "I said" from "they said" if the labels are
// explained, and a single-input session has nothing to explain.
func TestSpeakerNoteFollowsTheTranscript(t *testing.T) {
	t.Parallel()
	labeled := []Line{{Text: "第一句"}, {Speaker: "其他人", Text: "第二句"}}
	messages := summaryMessages(Config{}, nil, labeled)
	if !strings.Contains(messages[0].Content, speakerNote) {
		t.Fatalf("labeled summary system prompt = %q, want the speaker note", messages[0].Content)
	}
	messages = answerMessages(Config{}, labeled, thread{}, "谁负责接口文档？")
	if !strings.Contains(messages[0].Content, speakerNote) {
		t.Fatalf("labeled answer system prompt = %q, want the speaker note", messages[0].Content)
	}
	messages = summaryMessages(Config{}, nil, []Line{{Text: "只有一路音频"}})
	if strings.Contains(messages[0].Content, speakerNote) {
		t.Fatalf("unlabeled summary system prompt = %q, want no speaker note", messages[0].Content)
	}
}

// Telling the model to fall back on its own knowledge reads as an instruction
// not to search, so the permission to search has to be stated — and only where
// there is a tool to search with.
func TestSearchNoteFollowsTheHostedTools(t *testing.T) {
	t.Parallel()
	searching := Config{Tools: true, Endpoint: "https://api.openai.com/v1", Model: "gpt-4o-mini"}
	messages := answerMessages(searching, nil, thread{}, "今天的汇率是多少")
	if !strings.Contains(messages[0].Content, searchNote) {
		t.Fatalf("system prompt = %q, want the search note", messages[0].Content)
	}

	// The same endpoint with the switch off, and an endpoint with no hosted
	// tool at all, must not promise a tool the request never declares.
	off := searching
	off.Tools = false
	if messages = answerMessages(off, nil, thread{}, "今天的汇率是多少"); strings.Contains(messages[0].Content, searchNote) {
		t.Fatalf("system prompt = %q, want no search note while the switch is off", messages[0].Content)
	}
	deepseek := Config{Tools: true, Endpoint: "https://api.deepseek.com/v1", Model: "deepseek-chat"}
	if messages = answerMessages(deepseek, nil, thread{}, "今天的汇率是多少"); strings.Contains(messages[0].Content, searchNote) {
		t.Fatalf("system prompt = %q, want no search note without a hosted tool", messages[0].Content)
	}
}
