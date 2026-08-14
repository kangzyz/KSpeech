package assistant

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kangzyz/KSpeech/internal/config"
)

type fakeSettings struct {
	mu     sync.RWMutex
	values map[string]any
}

func newFakeSettings() *fakeSettings {
	values := make(map[string]any)
	for key, value := range config.Defaults() {
		values[key] = value
	}
	return &fakeSettings{values: values}
}

func (f *fakeSettings) set(key string, value any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[key] = value
}

func (f *fakeSettings) raw(key string) any {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.values[key]
}

func (f *fakeSettings) String(key string) string {
	value, _ := f.raw(key).(string)
	return value
}

func (f *fakeSettings) Bool(key string) bool {
	value, _ := f.raw(key).(bool)
	return value
}

func (f *fakeSettings) Int(key string) int {
	switch value := f.raw(key).(type) {
	case int:
		return value
	case float64:
		return int(value)
	case string:
		parsed, _ := strconv.Atoi(value)
		return parsed
	default:
		return 0
	}
}

// configuredSettings enables the assistant against an endpoint that no test
// ever reaches: the fake completer stands in for the network.
func configuredSettings() *fakeSettings {
	settings := newFakeSettings()
	settings.set(config.AssistantEnabled, true)
	settings.set(config.AssistantEndpoint, "https://api.example.com/v1")
	settings.set(config.AssistantAPIKey, "sk-test")
	settings.set(config.AssistantModel, "test-model")
	settings.set(config.AssistantSummaryInterval, 15)
	return settings
}

type fakeCompleter struct {
	mu       sync.Mutex
	requests []Request
	replies  []string
	dropped  bool
	err      error
}

func (f *fakeCompleter) Complete(_ context.Context, request Request) (Reply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request)
	if f.err != nil {
		return Reply{}, f.err
	}
	reply := "默认要点"
	if len(f.replies) > 0 {
		reply, f.replies = f.replies[0], f.replies[1:]
	}
	return Reply{Content: reply, ToolsDropped: f.dropped}, nil
}

func (f *fakeCompleter) calls() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.requests...)
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(delta time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(delta)
}

func newTestManager(t *testing.T, settings Settings, completer Completer) (*Manager, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, 8, 13, 10, 0, 0, 0, time.Local)}
	manager := New(settings, WithCompleter(completer), WithClock(clock.Now))
	t.Cleanup(manager.Close)
	return manager, clock
}

func waitForState(t *testing.T, manager *Manager, predicate func(State) bool, what string) State {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		state := manager.State()
		if predicate(state) {
			return state
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; state = %+v", what, state)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestObserveStaysOfflineUntilEnabledAndConfigured(t *testing.T) {
	t.Parallel()
	completer := &fakeCompleter{}
	settings := newFakeSettings()
	manager, clock := newTestManager(t, settings, completer)

	manager.Observe("", "这是一句默认关闭时不该外发的转写内容，足够长以触发汇总条件", clock.Now())
	manager.Flush()
	if calls := completer.calls(); len(calls) != 0 {
		t.Fatalf("disabled assistant sent %d requests", len(calls))
	}

	settings.set(config.AssistantEnabled, true)
	manager.Observe("", "开启但没有填写地址时同样不应该发起请求，这段文本一样足够长", clock.Now())
	manager.Flush()
	if calls := completer.calls(); len(calls) != 0 {
		t.Fatalf("unconfigured assistant sent %d requests", len(calls))
	}
	if state := manager.State(); state.Configured || state.ConfigError == "" {
		t.Fatalf("state should report the missing endpoint: %+v", state)
	}
}

func TestSummaryRunsOnceIntervalElapsed(t *testing.T) {
	t.Parallel()
	completer := &fakeCompleter{replies: []string{"下周三评审\n接口改由张三负责"}}
	settings := configuredSettings()
	settings.set(config.AssistantAutoAnswer, false)
	manager, clock := newTestManager(t, settings, completer)

	manager.Observe("", "我们把评审安排在下周三上午十点", clock.Now())
	if calls := completer.calls(); len(calls) != 0 {
		t.Fatalf("summary ran before the interval elapsed: %d requests", len(calls))
	}
	clock.Advance(20 * time.Second)
	manager.Observe("", "接口这块后面改成张三负责对接", clock.Now())

	state := waitForState(t, manager, func(state State) bool { return len(state.Insights) == 2 }, "two key points")
	if state.Insights[0].Text != "下周三评审" || state.Insights[1].Text != "接口改由张三负责" {
		t.Fatalf("insights = %#v", state.Insights)
	}
	if state.PendingLines != 0 {
		t.Fatalf("pending lines = %d, want the buffer to be consumed", state.PendingLines)
	}
	calls := completer.calls()
	if len(calls) != 1 {
		t.Fatalf("request count = %d, want 1", len(calls))
	}
	prompt := calls[0].Messages[len(calls[0].Messages)-1].Content
	if !strings.Contains(prompt, "下周三上午十点") || !strings.Contains(prompt, "张三负责对接") {
		t.Fatalf("summary prompt lost transcript lines: %q", prompt)
	}
}

func TestSummaryFailureKeepsTranscriptAndBacksOff(t *testing.T) {
	t.Parallel()
	completer := &fakeCompleter{err: context.DeadlineExceeded}
	settings := configuredSettings()
	settings.set(config.AssistantAutoAnswer, false)
	manager, clock := newTestManager(t, settings, completer)

	manager.Observe("", "第一句需要被汇总的内容，长度足够触发一次请求", clock.Now())
	clock.Advance(20 * time.Second)
	manager.Observe("", "第二句同样会进入待汇总缓冲区", clock.Now())

	state := waitForState(t, manager, func(state State) bool { return state.LastError != "" }, "the failure to surface")
	if !strings.Contains(state.LastError, "生成关键要点失败") {
		t.Fatalf("last error = %q", state.LastError)
	}
	if state.PendingLines != 2 {
		t.Fatalf("pending lines = %d, want the transcript to be kept for a retry", state.PendingLines)
	}

	// The retry window restarts, so the very next sentence must not fire again.
	manager.Observe("", "第三句紧接着到达", clock.Now())
	if calls := completer.calls(); len(calls) != 1 {
		t.Fatalf("request count = %d, want the failed endpoint to back off", len(calls))
	}
}

func TestAutoAnswerDetectsQuestionsAndSpacesThemOut(t *testing.T) {
	t.Parallel()
	completer := &fakeCompleter{replies: []string{"由张三负责，评审定在下周三。", "预算是十二万。"}}
	settings := configuredSettings()
	settings.set(config.AssistantSummarize, false)
	manager, clock := newTestManager(t, settings, completer)

	manager.Observe("", "接口这块进度谁跟？", clock.Now())
	state := waitForState(t, manager, func(state State) bool {
		return len(state.Conversations) == 1 && state.Conversations[0].Status == StatusReady
	}, "the first answer")
	if state.Conversations[0].Source != SourceAuto {
		t.Fatalf("conversation = %#v", state.Conversations[0])
	}
	if state.Conversations[0].Answer != "由张三负责，评审定在下周三。" {
		t.Fatalf("answer = %q", state.Conversations[0].Answer)
	}

	// A restated question inside the cooldown must not spend a second request.
	manager.Observe("", "那这块到底谁跟？", clock.Now())
	if state := manager.State(); len(state.Conversations) != 1 {
		t.Fatalf("cooldown ignored: %#v", state.Conversations)
	}

	clock.Advance(minAutoAnswerGap + time.Second)
	manager.Observe("", "预算大概多少？", clock.Now())
	waitForState(t, manager, func(state State) bool {
		return len(state.Conversations) == 2 && state.Conversations[1].Status == StatusReady
	}, "the second answer")
}

func TestAskValidatesInputAndUsesTranscriptContext(t *testing.T) {
	t.Parallel()
	completer := &fakeCompleter{replies: []string{"评审定在下周三上午十点。"}}
	settings := configuredSettings()
	settings.set(config.AssistantSummarize, false)
	settings.set(config.AssistantAutoAnswer, false)
	manager, clock := newTestManager(t, settings, completer)

	if err := manager.Ask("   "); err == nil {
		t.Fatal("an empty question was accepted")
	}
	manager.Observe("", "评审安排在下周三上午十点", clock.Now())
	if err := manager.Ask("评审是什么时候"); err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	state := waitForState(t, manager, func(state State) bool {
		return len(state.Conversations) == 1 && state.Conversations[0].Status == StatusReady
	}, "the manual answer")
	if state.Conversations[0].Source != SourceManual {
		t.Fatalf("conversation = %#v", state.Conversations[0])
	}
	prompt := completer.calls()[0].Messages[1].Content
	if !strings.Contains(prompt, "评审安排在下周三上午十点") || !strings.Contains(prompt, "评审是什么时候") {
		t.Fatalf("answer prompt = %q", prompt)
	}

	settings.set(config.AssistantEnabled, false)
	if err := manager.Ask("还能问吗"); err == nil {
		t.Fatal("a disabled assistant answered a question")
	}
}

// A follow-up only makes sense if the turn it follows is in the request, so
// finished turns go out as ordinary user/assistant messages.
func TestAnswersCarryTheConversationThread(t *testing.T) {
	t.Parallel()
	completer := &fakeCompleter{replies: []string{"由张三负责。", "他这周就能给出排期。"}}
	settings := configuredSettings()
	settings.set(config.AssistantSummarize, false)
	settings.set(config.AssistantAutoAnswer, false)
	manager, clock := newTestManager(t, settings, completer)

	manager.Observe("", "接口这块由张三负责", clock.Now())
	if err := manager.Ask("接口谁负责"); err != nil {
		t.Fatal(err)
	}
	waitForState(t, manager, func(state State) bool {
		return len(state.Conversations) == 1 && state.Conversations[0].Status == StatusReady
	}, "the first answer")
	if err := manager.Ask("那他什么时候能给排期"); err != nil {
		t.Fatal(err)
	}
	waitForState(t, manager, func(state State) bool {
		return len(state.Conversations) == 2 && state.Conversations[1].Status == StatusReady
	}, "the follow-up answer")

	messages := completer.calls()[1].Messages
	if len(messages) != 4 {
		t.Fatalf("follow-up carried %d messages, want system + one turn + the question", len(messages))
	}
	if messages[1].Role != RoleUser || messages[1].Content != "接口谁负责" {
		t.Fatalf("previous question = %#v", messages[1])
	}
	if messages[2].Role != RoleAssistant || messages[2].Content != "由张三负责。" {
		t.Fatalf("previous answer = %#v", messages[2])
	}
	if !strings.Contains(messages[3].Content, "那他什么时候能给排期") {
		t.Fatalf("current question = %q", messages[3].Content)
	}
	// The transcript rides with the newest question only: repeating it under
	// every earlier turn would make each follow-up cost more than the last.
	if strings.Contains(messages[1].Content, "接口这块由张三负责") {
		t.Fatalf("an earlier turn carried its own transcript: %q", messages[1].Content)
	}
}

// The thread has no turn limit, so the oldest turns are folded into a digest
// once they stop fitting rather than being dropped.
func TestLongThreadIsCompressedInsteadOfDropped(t *testing.T) {
	t.Parallel()
	first, second, third := strings.Repeat("甲", 1200), strings.Repeat("乙", 1200), strings.Repeat("丙", 1200)
	completer := &fakeCompleter{replies: []string{first, second, third, "张三负责接口，排期定在下周三", "预算是十二万。"}}
	settings := configuredSettings()
	settings.set(config.AssistantSummarize, false)
	settings.set(config.AssistantAutoAnswer, false)
	manager, _ := newTestManager(t, settings, completer)

	for index, question := range []string{"第一个问题", "第二个问题", "第三个问题"} {
		if err := manager.Ask(question); err != nil {
			t.Fatal(err)
		}
		want := index + 1
		waitForState(t, manager, func(state State) bool {
			return len(state.Conversations) == want && state.Conversations[want-1].Status == StatusReady
		}, "answer "+question)
	}

	state := waitForState(t, manager, func(state State) bool { return state.DigestedTurns == 2 }, "the folded turns")
	if state.ThreadDigest != "张三负责接口，排期定在下周三" {
		t.Fatalf("digest = %q", state.ThreadDigest)
	}
	// Compression changes the request, never the chat pane.
	if len(state.Conversations) != 3 {
		t.Fatalf("compression removed turns from the list: %#v", state.Conversations)
	}

	if err := manager.Ask("那预算呢"); err != nil {
		t.Fatal(err)
	}
	waitForState(t, manager, func(state State) bool {
		return len(state.Conversations) == 4 && state.Conversations[3].Status == StatusReady
	}, "the answer after compression")

	calls := completer.calls()
	messages := calls[len(calls)-1].Messages
	if !strings.Contains(messages[0].Content, threadDigestHeading) ||
		!strings.Contains(messages[0].Content, "张三负责接口，排期定在下周三") {
		t.Fatalf("system prompt lost the digest: %q", messages[0].Content)
	}
	joined := ""
	for _, message := range messages {
		joined += message.Content
	}
	if strings.Contains(joined, first) || strings.Contains(joined, second) {
		t.Fatal("a folded turn was still sent verbatim")
	}
	if !strings.Contains(joined, third) {
		t.Fatal("the newest turn was dropped instead of kept verbatim")
	}

	manager.Clear()
	if state := manager.State(); state.ThreadDigest != "" || state.DigestedTurns != 0 {
		t.Fatalf("clearing the list left the digest behind: %+v", state)
	}
}

// A speaker restates a question while waiting for the answer, and recognition
// re-emits sentences; neither should buy the same answer twice.
func TestAutoAnswerSkipsRestatedQuestions(t *testing.T) {
	t.Parallel()
	completer := &fakeCompleter{replies: []string{"由张三负责。", "预算是十二万。"}}
	settings := configuredSettings()
	settings.set(config.AssistantSummarize, false)
	manager, clock := newTestManager(t, settings, completer)

	manager.Observe("", "接口这块进度谁跟？", clock.Now())
	waitForState(t, manager, func(state State) bool {
		return len(state.Conversations) == 1 && state.Conversations[0].Status == StatusReady
	}, "the first answer")

	clock.Advance(minAutoAnswerGap + time.Second)
	manager.Observe("", "接口这块进度谁跟。", clock.Now())
	if state := manager.State(); len(state.Conversations) != 1 {
		t.Fatalf("a restated question was answered again: %#v", state.Conversations)
	}

	clock.Advance(minAutoAnswerGap + time.Second)
	manager.Observe("", "预算大概多少？", clock.Now())
	waitForState(t, manager, func(state State) bool { return len(state.Conversations) == 2 }, "a different question")
}

func TestFlushSummarizesTheRemainder(t *testing.T) {
	t.Parallel()
	completer := &fakeCompleter{replies: []string{"会议结束前确认了发布时间"}}
	settings := configuredSettings()
	settings.set(config.AssistantAutoAnswer, false)
	manager, clock := newTestManager(t, settings, completer)

	manager.Observe("", "最后再确认一下这次发布的时间安排，下周三上午十点开始评审", clock.Now())
	manager.Flush()
	waitForState(t, manager, func(state State) bool { return len(state.Insights) == 1 }, "the flushed key point")
}

func TestClearKeepsContextAndStateMarshalsWithoutNulls(t *testing.T) {
	t.Parallel()
	completer := &fakeCompleter{}
	settings := configuredSettings()
	settings.set(config.AssistantSummarize, false)
	settings.set(config.AssistantAutoAnswer, false)
	manager, clock := newTestManager(t, settings, completer)

	manager.Observe("", "预算是十二万", clock.Now())
	manager.Clear()
	state := manager.State()
	if len(state.Insights) != 0 || len(state.Conversations) != 0 {
		t.Fatalf("clear left state behind: %+v", state)
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "null") {
		t.Fatalf("state marshals null slices: %s", data)
	}

	if err := manager.Ask("预算多少"); err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	waitForState(t, manager, func(state State) bool {
		return len(state.Conversations) == 1 && state.Conversations[0].Status == StatusReady
	}, "the answer")
	if prompt := completer.calls()[0].Messages[1].Content; !strings.Contains(prompt, "预算是十二万") {
		t.Fatalf("clear dropped the transcript context: %q", prompt)
	}
}

func TestSubscribePublishesStateChanges(t *testing.T) {
	t.Parallel()
	completer := &fakeCompleter{}
	settings := configuredSettings()
	settings.set(config.AssistantSummarize, false)
	settings.set(config.AssistantAutoAnswer, false)
	manager, _ := newTestManager(t, settings, completer)

	states := make(chan State, 8)
	cancel := manager.Subscribe(func(state State) {
		select {
		case states <- state:
		default:
		}
	})
	manager.Clear()
	select {
	case state := <-states:
		if !state.Enabled {
			t.Fatalf("published state = %+v", state)
		}
	case <-time.After(time.Second):
		t.Fatal("no state was published")
	}
	cancel()
	manager.Clear()
	select {
	case state := <-states:
		t.Fatalf("a cancelled subscriber still received %+v", state)
	case <-time.After(50 * time.Millisecond):
	}
}
