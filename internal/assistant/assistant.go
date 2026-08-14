package assistant

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	// The transcript is only kept as request context; the recognition history
	// window remains the complete record.
	maxTranscriptLines = 400
	maxInsights        = 200
	maxConversations   = 100

	// The question thread has no turn limit: a follow-up an hour into a meeting
	// still refers to what was answered at the start. What is bounded is the
	// request — the newest turns go verbatim up to this many runes and
	// everything older is folded into a rolling digest, so a long conversation
	// costs a roughly constant number of tokens instead of a growing one.
	maxThreadRunes = 2000
	maxDigestRunes = 400
	// Folding one turn at a time would spend a request per answer. Waiting for
	// a couple of them keeps compression rare.
	minDigestTurns = 2

	maxConcurrentAnswers = 2
	// Two questions in a row usually mean one question restated, and a meeting
	// can ask faster than a model answers. The gap bounds both.
	minAutoAnswerGap = 8 * time.Second
	// How far back a detected question is compared against the ones already
	// answered. A meeting restates a question several times over.
	autoAnswerLookback = 6
	// One question inside another counts as a restatement only from this length
	// on; a short one is a fragment that many questions happen to contain.
	minRestatementRunes = 6
	// A summary request costs the same whether it carries one filler word or a
	// full paragraph, so wait until there is something to summarize.
	minSummaryRunes  = 24
	maxQuestionRunes = 2000
)

const (
	SourceAuto   = "auto"
	SourceManual = "manual"

	StatusPending = "pending"
	StatusReady   = "ready"
	StatusFailed  = "failed"
)

// Line is one finalized caption kept as context for later requests. Speaker is
// the label of the audio input it came from, empty when a session captures a
// single unlabeled input.
type Line struct {
	Time    time.Time `json:"time"`
	Speaker string    `json:"speaker,omitempty"`
	Text    string    `json:"text"`
}

// Insight is one key point extracted from a stretch of the transcript.
type Insight struct {
	ID   uint64    `json:"id"`
	Time time.Time `json:"time"`
	Text string    `json:"text"`
}

// Conversation is a detected or typed question together with its answer.
type Conversation struct {
	ID       uint64    `json:"id"`
	Time     time.Time `json:"time"`
	Question string    `json:"question"`
	Answer   string    `json:"answer"`
	Source   string    `json:"source"`
	Status   string    `json:"status"`
	Error    string    `json:"error,omitempty"`
}

// State is the whole assistant view of the world, published to the UI.
type State struct {
	Enabled       bool           `json:"enabled"`
	Configured    bool           `json:"configured"`
	Summarize     bool           `json:"summarize"`
	AutoAnswer    bool           `json:"autoAnswer"`
	Model         string         `json:"model"`
	Provider      string         `json:"provider,omitempty"`
	Tools         []string       `json:"tools"`
	ToolNote      string         `json:"toolNote,omitempty"`
	Summarizing   bool           `json:"summarizing"`
	Answering     bool           `json:"answering"`
	PendingLines  int            `json:"pendingLines"`
	Insights      []Insight      `json:"insights"`
	Conversations []Conversation `json:"conversations"`
	// ThreadDigest is the rolling summary that stands in for the turns which no
	// longer fit a request verbatim, and DigestedTurns is how many turns it
	// covers. Both are published so the chat pane can say that the older part of
	// the conversation is still being carried, only compressed.
	ThreadDigest  string `json:"threadDigest,omitempty"`
	DigestedTurns int    `json:"digestedTurns,omitempty"`
	ConfigError   string `json:"configError,omitempty"`
	LastError     string `json:"lastError,omitempty"`
}

type Listener func(State)

type Option func(*Manager)

// WithCompleter replaces the HTTP endpoint client, mainly for tests.
func WithCompleter(completer Completer) Option {
	return func(m *Manager) {
		if completer != nil {
			m.completer = completer
		}
	}
}

func WithClock(now func() time.Time) Option {
	return func(m *Manager) {
		if now != nil {
			m.now = now
		}
	}
}

type Manager struct {
	settings  Settings
	completer Completer
	now       func() time.Time

	ctx      context.Context
	cancel   context.CancelFunc
	inflight sync.WaitGroup

	mu               sync.Mutex
	transcript       []Line
	pending          []Line
	pendingSince     time.Time
	summarizing      bool
	answering        int
	lastAutoAnswerAt time.Time
	insights         []Insight
	conversations    []Conversation
	threadDigest     string
	digestedID       uint64
	digestedTurns    int
	compressing      bool
	nextID           uint64
	lastError        string
	toolWarning      string
	listeners        map[uint64]Listener
	nextListenerID   uint64
	closed           bool
}

func New(settings Settings, options ...Option) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	manager := &Manager{
		settings:      settings,
		completer:     &HTTPCompleter{Client: &http.Client{}},
		now:           time.Now,
		ctx:           ctx,
		cancel:        cancel,
		insights:      make([]Insight, 0),
		conversations: make([]Conversation, 0),
		listeners:     make(map[uint64]Listener),
	}
	for _, option := range options {
		option(manager)
	}
	return manager
}

func (m *Manager) Subscribe(listener Listener) func() {
	if listener == nil {
		return func() {}
	}
	m.mu.Lock()
	m.nextListenerID++
	id := m.nextListenerID
	m.listeners[id] = listener
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		delete(m.listeners, id)
		m.mu.Unlock()
	}
}

func (m *Manager) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stateLocked()
}

// Observe records one finalized caption and starts a summary or an answer when
// the current settings call for it. It never blocks on the network. speaker is
// the label of the audio input the sentence came from, so a summary can tell
// what the user said from what everyone else said.
func (m *Manager) Observe(speaker, text string, at time.Time) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	cfg := Resolve(m.settings)
	if !cfg.Enabled {
		return
	}
	now := m.now()
	if at.IsZero() {
		at = now
	}
	configured := cfg.Validate() == nil

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	line := Line{Time: at, Speaker: strings.TrimSpace(speaker), Text: text}
	m.transcript = appendBounded(m.transcript, line, maxTranscriptLines)
	if cfg.Summarize {
		if len(m.pending) == 0 {
			m.pendingSince = now
		}
		m.pending = appendBounded(m.pending, line, maxTranscriptLines)
	}
	if configured && cfg.Summarize && m.shouldSummarizeLocked(cfg, now) {
		m.startSummaryLocked(cfg)
	}
	if configured && cfg.AutoAnswer && m.canAutoAnswerLocked(now) &&
		LooksLikeQuestion(text) && !m.alreadyAnsweredLocked(text) {
		m.lastAutoAnswerAt = now
		m.startAnswerLocked(cfg, text, SourceAuto, at)
	}
	state, listeners := m.stateAndListenersLocked()
	m.mu.Unlock()
	publish(state, listeners)
}

// Flush summarizes whatever is still buffered, ignoring the interval. It runs
// when recognition stops so the tail of a session is not lost.
func (m *Manager) Flush() {
	cfg := Resolve(m.settings)
	if !cfg.Enabled || !cfg.Summarize || cfg.Validate() != nil {
		return
	}
	m.mu.Lock()
	if m.closed || m.summarizing || len(m.pending) == 0 || totalRunes(m.pending) < minSummaryRunes {
		m.mu.Unlock()
		return
	}
	m.startSummaryLocked(cfg)
	state, listeners := m.stateAndListenersLocked()
	m.mu.Unlock()
	publish(state, listeners)
}

// Ask answers a question typed by the user. It returns once the request has
// been queued; the answer arrives through the published state.
func (m *Manager) Ask(question string) error {
	question = strings.TrimSpace(question)
	if question == "" {
		return errors.New("请输入要提问的内容")
	}
	if len([]rune(question)) > maxQuestionRunes {
		return errors.New("提问内容过长，请精简后再试")
	}
	cfg := Resolve(m.settings)
	if !cfg.Enabled {
		return errors.New("AI 助手尚未开启，请先在设置里打开并填写模型 API 地址")
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("AI 助手已关闭")
	}
	if m.answering >= maxConcurrentAnswers {
		m.mu.Unlock()
		return errors.New("正在回答上一个问题，请稍候再试")
	}
	m.startAnswerLocked(cfg, question, SourceManual, m.now())
	state, listeners := m.stateAndListenersLocked()
	m.mu.Unlock()
	publish(state, listeners)
	return nil
}

// Test sends the smallest possible request so the user can verify an endpoint
// before turning the assistant on.
func (m *Manager) Test(ctx context.Context) (string, error) {
	cfg := Resolve(m.settings)
	if err := cfg.Validate(); err != nil {
		return "", err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	reply, err := m.completer.Complete(ctx, Request{
		Config:      cfg,
		Messages:    []Message{{Role: RoleUser, Content: "请只回复四个字：连接正常"}},
		MaxTokens:   64,
		Temperature: 0,
	})
	if err != nil {
		m.mu.Lock()
		m.lastError = err.Error()
		state, listeners := m.stateAndListenersLocked()
		m.mu.Unlock()
		publish(state, listeners)
		return "", err
	}
	m.mu.Lock()
	m.lastError = ""
	state, listeners := m.stateAndListenersLocked()
	m.mu.Unlock()
	publish(state, listeners)
	return reply.Content, nil
}

// recordToolOutcomeLocked remembers whether the endpoint actually accepted the
// hosted tools. The completer falls back to a tool-less retry so an answer
// still arrives, which would otherwise hide a provider that never searches.
func (m *Manager) recordToolOutcomeLocked(declared, dropped bool) {
	if !declared {
		return
	}
	if dropped {
		m.toolWarning = "模型接口拒绝了内置工具，这次回答没有联网搜索。"
		return
	}
	m.toolWarning = ""
}

// Clear empties the key points and the question list. The transcript context
// is kept so an answer right after a clear still knows what was said; the
// conversation thread is not, because clearing the list is how a user starts a
// new one and a digest of questions they just removed would outlive it.
func (m *Manager) Clear() {
	m.mu.Lock()
	m.insights = make([]Insight, 0)
	m.conversations = make([]Conversation, 0)
	m.threadDigest = ""
	m.digestedID = 0
	m.digestedTurns = 0
	m.lastError = ""
	m.toolWarning = ""
	state, listeners := m.stateAndListenersLocked()
	m.mu.Unlock()
	publish(state, listeners)
}

// Close cancels in-flight requests and waits for their goroutines.
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	m.mu.Unlock()
	m.cancel()
	m.inflight.Wait()
}

func (m *Manager) shouldSummarizeLocked(cfg Config, now time.Time) bool {
	if m.summarizing || len(m.pending) == 0 || m.pendingSince.IsZero() {
		return false
	}
	if totalRunes(m.pending) < minSummaryRunes {
		return false
	}
	return now.Sub(m.pendingSince) >= cfg.SummaryInterval
}

func (m *Manager) canAutoAnswerLocked(now time.Time) bool {
	if m.answering >= maxConcurrentAnswers {
		return false
	}
	return m.lastAutoAnswerAt.IsZero() || now.Sub(m.lastAutoAnswerAt) >= minAutoAnswerGap
}

// alreadyAnsweredLocked reports that this question is one of the last few that
// were already sent. A speaker restates a question while waiting for an answer,
// and recognition itself re-emits a sentence with one character changed; both
// would otherwise buy the same answer twice.
func (m *Manager) alreadyAnsweredLocked(question string) bool {
	asked := normalizeQuestion(question)
	if asked == "" {
		return false
	}
	start := len(m.conversations) - autoAnswerLookback
	if start < 0 {
		start = 0
	}
	for _, item := range m.conversations[start:] {
		previous := normalizeQuestion(item.Question)
		if previous == "" {
			continue
		}
		if previous == asked {
			return true
		}
		// One sentence inside the other is a restatement only when there is
		// enough of it to be sure; "为什么" alone sits inside half the questions
		// a meeting asks.
		if minRunes(previous, asked) < minRestatementRunes {
			continue
		}
		if strings.Contains(previous, asked) || strings.Contains(asked, previous) {
			return true
		}
	}
	return false
}

// normalizeQuestion reduces a caption to what makes it the same question:
// spacing and punctuation vary between two recognitions of one sentence.
func normalizeQuestion(text string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(text) {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

func minRunes(first, second string) int {
	left, right := len([]rune(first)), len([]rune(second))
	if left < right {
		return left
	}
	return right
}

func (m *Manager) startSummaryLocked(cfg Config) {
	lines := m.pending
	m.pending = nil
	m.pendingSince = time.Time{}
	m.summarizing = true
	request := Request{
		Config:      cfg,
		Messages:    summaryMessages(cfg, recentInsightTexts(m.insights, 6), lines),
		MaxTokens:   400,
		Temperature: 0.2,
	}
	at := lines[len(lines)-1].Time
	m.launch(cfg.Timeout, func(ctx context.Context) {
		reply, err := m.completer.Complete(ctx, request)
		m.mu.Lock()
		m.summarizing = false
		if err != nil {
			// Keep the transcript for the next attempt, but restart the
			// interval so a broken endpoint is not retried on every sentence.
			m.restorePendingLocked(lines)
			m.lastError = "生成关键要点失败：" + err.Error()
		} else {
			m.lastError = ""
			m.appendInsightsLocked(ParseInsights(reply.Content), at)
		}
		state, listeners := m.stateAndListenersLocked()
		m.mu.Unlock()
		publish(state, listeners)
	})
}

func (m *Manager) startAnswerLocked(cfg Config, question, source string, at time.Time) {
	// The thread is read before the new turn joins it, so the question is not
	// also sent as its own history.
	history := m.threadLocked()
	m.nextID++
	id := m.nextID
	m.conversations = appendBounded(m.conversations, Conversation{
		ID: id, Time: at, Question: question, Source: source, Status: StatusPending,
	}, maxConversations)
	m.answering++
	request := Request{
		Config:   cfg,
		Messages: answerMessages(cfg, recentLines(m.transcript, cfg.ContextSentences), history, question),
		// Only answers carry the hosted tools. A summary condenses what was
		// just said, so searching the web for it would spend a paid call on
		// something the transcript already contains.
		Tools:       ActiveTools(cfg),
		MaxTokens:   800,
		Temperature: 0.3,
	}
	m.launch(cfg.Timeout, func(ctx context.Context) {
		reply, err := m.completer.Complete(ctx, request)
		m.mu.Lock()
		if m.answering > 0 {
			m.answering--
		}
		if err != nil {
			m.updateConversationLocked(id, "", StatusFailed, err.Error())
			m.lastError = "回答失败：" + err.Error()
		} else {
			m.updateConversationLocked(id, strings.TrimSpace(reply.Content), StatusReady, "")
			m.lastError = ""
			m.recordToolOutcomeLocked(len(request.Tools) > 0, reply.ToolsDropped)
			m.compressThreadLocked(cfg)
		}
		state, listeners := m.stateAndListenersLocked()
		m.mu.Unlock()
		publish(state, listeners)
	})
}

// threadLocked returns the conversation as the next request will carry it: the
// running digest, plus the newest finished turns that fit the verbatim budget.
// Turns beyond the budget are left out even when compression has not caught up
// yet, so one request can never outgrow the budget.
func (m *Manager) threadLocked() thread {
	_, keep := m.splitThreadLocked()
	turns := make([]Message, 0, len(keep)*2)
	for _, item := range keep {
		turns = append(turns,
			Message{Role: RoleUser, Content: item.Question},
			Message{Role: RoleAssistant, Content: item.Answer},
		)
	}
	return thread{digest: m.threadDigest, turns: turns}
}

// splitThreadLocked divides the turns that are not in the digest yet into the
// oldest ones to fold and the newest ones that still fit verbatim. The newest
// turn is always kept, however long it is: a follow-up refers to it.
func (m *Manager) splitThreadLocked() (fold, keep []Conversation) {
	pending := make([]Conversation, 0, len(m.conversations))
	for _, item := range m.conversations {
		if item.ID <= m.digestedID || item.Status != StatusReady || item.Answer == "" {
			continue
		}
		pending = append(pending, item)
	}
	budget := maxThreadRunes
	kept := 0
	for index := len(pending) - 1; index >= 0; index-- {
		cost := len([]rune(pending[index].Question)) + len([]rune(pending[index].Answer))
		if kept > 0 && cost > budget {
			break
		}
		budget -= cost
		kept++
	}
	return pending[:len(pending)-kept], pending[len(pending)-kept:]
}

// compressThreadLocked folds the turns that no longer fit into the rolling
// digest, so a long conversation keeps its thread instead of losing its start.
// The chat pane is untouched: only what the model receives is compressed.
func (m *Manager) compressThreadLocked(cfg Config) {
	if m.compressing {
		return
	}
	fold, _ := m.splitThreadLocked()
	if len(fold) < minDigestTurns {
		return
	}
	m.compressing = true
	lastID := fold[len(fold)-1].ID
	folded := len(fold)
	request := Request{
		Config:      cfg,
		Messages:    digestMessages(cfg, m.threadDigest, fold),
		MaxTokens:   600,
		Temperature: 0.2,
	}
	m.launch(cfg.Timeout, func(ctx context.Context) {
		reply, err := m.completer.Complete(ctx, request)
		m.mu.Lock()
		m.compressing = false
		if digest := strings.TrimSpace(reply.Content); err == nil && digest != "" {
			m.threadDigest = clampRunes(digest, maxDigestRunes)
			m.digestedID = lastID
			m.digestedTurns += folded
		}
		// A failed compression is not worth an error banner: those turns simply
		// stay out of the next request until the fold succeeds, and the answer
		// the user was waiting for already arrived.
		state, listeners := m.stateAndListenersLocked()
		m.mu.Unlock()
		publish(state, listeners)
	})
}

func (m *Manager) launch(timeout time.Duration, task func(ctx context.Context)) {
	m.inflight.Add(1)
	go func() {
		defer m.inflight.Done()
		ctx, cancel := context.WithTimeout(m.ctx, timeout)
		defer cancel()
		task(ctx)
	}()
}

func (m *Manager) restorePendingLocked(lines []Line) {
	m.pending = trimFront(append(append([]Line(nil), lines...), m.pending...), maxTranscriptLines)
	if len(m.pending) > 0 {
		m.pendingSince = m.now()
	}
}

func (m *Manager) appendInsightsLocked(points []string, at time.Time) {
	known := recentInsightTexts(m.insights, 10)
	for _, point := range points {
		if containsText(known, point) {
			continue
		}
		m.nextID++
		m.insights = appendBounded(m.insights, Insight{ID: m.nextID, Time: at, Text: point}, maxInsights)
		known = append(known, point)
	}
}

func (m *Manager) updateConversationLocked(id uint64, answer, status, failure string) {
	for index := range m.conversations {
		if m.conversations[index].ID != id {
			continue
		}
		m.conversations[index].Answer = answer
		m.conversations[index].Status = status
		m.conversations[index].Error = failure
		return
	}
}

func (m *Manager) stateLocked() State {
	cfg := Resolve(m.settings)
	tools := DescribeTools(cfg)
	state := State{
		Enabled:       cfg.Enabled,
		Summarize:     cfg.Summarize,
		AutoAnswer:    cfg.AutoAnswer,
		Model:         cfg.Model,
		Provider:      tools.Provider,
		Tools:         tools.Tools,
		ToolNote:      tools.Note,
		Summarizing:   m.summarizing,
		Answering:     m.answering > 0,
		PendingLines:  len(m.pending),
		Insights:      append(make([]Insight, 0, len(m.insights)), m.insights...),
		Conversations: append(make([]Conversation, 0, len(m.conversations)), m.conversations...),
		ThreadDigest:  m.threadDigest,
		DigestedTurns: m.digestedTurns,
		LastError:     m.lastError,
	}
	// A rejection observed on the wire outranks what the catalog expects.
	if m.toolWarning != "" {
		state.ToolNote = m.toolWarning
	}
	if err := cfg.Validate(); err != nil {
		if cfg.Enabled {
			state.ConfigError = err.Error()
		}
	} else {
		state.Configured = true
	}
	return state
}

func (m *Manager) stateAndListenersLocked() (State, []Listener) {
	listeners := make([]Listener, 0, len(m.listeners))
	for _, listener := range m.listeners {
		listeners = append(listeners, listener)
	}
	return m.stateLocked(), listeners
}

func publish(state State, listeners []Listener) {
	for _, listener := range listeners {
		listener(state)
	}
}

func containsText(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}
