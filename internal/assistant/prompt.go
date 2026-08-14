package assistant

import (
	"fmt"
	"strings"
)

const (
	maxInsightRunes    = 120
	maxInsightsPerCall = 5
	// "无" is the agreed answer for "this stretch of talk carries nothing worth
	// recording", so the assistant stays quiet during small talk.
	emptyInsightMarker = "无"
)

const summarySystem = `你是会议记录助手，正在处理实时语音转写文本。转写可能有同音字错误或缺字，请结合上下文理解。
请从「新的转写内容」里提取值得记录的关键信息：结论、决定、待办事项、时间节点、数字、责任人。
输出要求：
- 每行一条，不要编号、不要项目符号、不要额外解释
- 最多 3 条，每条不超过 40 字
- 只写新增信息，不要重复「已记录要点」里已有的内容
- 如果这段内容只是寒暄、口水话或没有实质信息，只输出一个字：无`

const answerSystem = `你是会议中的实时助手。下面会给你最近的语音转写内容（可能有识别错误）和一个需要回答的问题。
请用中文给出可以直接说出口的回答：
- 先给结论，再补一句依据，总长不超过 150 字
- 优先使用转写内容里的事实；转写内容不足以回答时，用你自己的知识回答，并说明这是补充
- 之前的问答就在这次对话里，用户经常在追问，「这个」「刚才那个」「再详细讲讲」指的都是上一轮的内容
- 不要复述问题，不要输出任何前缀或格式标记`

// searchNote is added only when the endpoint actually accepts a hosted search
// tool. Without it the model is told to fall back on its own knowledge, which
// reads as an instruction not to search.
const searchNote = `问题涉及实时信息、外部事实或转写内容里没有的资料时，请使用可用的联网搜索工具查证后再回答，并在结论里点明信息来自搜索。`

// threadDigestHeading introduces the rolling digest of turns that no longer fit
// verbatim. It is folded into the system message because several
// OpenAI-compatible gateways only accept one system message.
const threadDigestHeading = "【更早对话的摘要】"

const digestSystem = `你在维护一段会议问答的滚动摘要，供后续追问使用。
请把「已有摘要」和「新的问答」合并成一份新的摘要：
- 保留结论、数字、时间节点、责任人，以及提问者关心的主题
- 丢掉寒暄、重复和已经作废的内容
- 每行一条，不要编号，总长不超过 %d 字
- 只输出摘要正文`

// thread is the conversation so far as one request will carry it: everything
// older already folded into digest, the newest turns verbatim.
type thread struct {
	digest string
	turns  []Message
}

func summaryMessages(cfg Config, previous []string, lines []Line) []Message {
	var builder strings.Builder
	if len(previous) > 0 {
		builder.WriteString("已记录要点：\n")
		for _, item := range previous {
			builder.WriteString("- " + item + "\n")
		}
		builder.WriteString("\n")
	}
	builder.WriteString("新的转写内容：\n")
	builder.WriteString(formatLines(lines))
	return []Message{
		{Role: RoleSystem, Content: withBackground(withSpeakers(summarySystem, lines), cfg.Background)},
		{Role: RoleUser, Content: builder.String()},
	}
}

// answerMessages lays out one answer request: the instructions, then the
// conversation so far as ordinary user/assistant turns, then the question with
// the transcript as it stands right now. Earlier turns deliberately carry no
// transcript of their own — it has moved on, and repeating it would make every
// follow-up more expensive than the last.
func answerMessages(cfg Config, context []Line, history thread, question string) []Message {
	system := withBackground(withSpeakers(answerSystem, context), cfg.Background)
	if len(ActiveTools(cfg)) > 0 {
		system += "\n\n" + searchNote
	}
	if history.digest != "" {
		system += "\n\n" + threadDigestHeading + "\n" + history.digest
	}

	var builder strings.Builder
	if len(context) > 0 {
		builder.WriteString("【最近的转写内容】\n")
		builder.WriteString(formatLines(context))
		builder.WriteString("\n\n")
	}
	builder.WriteString("【需要回答的问题】\n")
	builder.WriteString(question)

	messages := make([]Message, 0, len(history.turns)+2)
	messages = append(messages, Message{Role: RoleSystem, Content: system})
	messages = append(messages, history.turns...)
	return append(messages, Message{Role: RoleUser, Content: builder.String()})
}

// digestMessages folds turns that no longer fit into the running digest. The
// previous digest goes in with them, so nothing is lost by being compressed
// twice.
func digestMessages(cfg Config, digest string, turns []Conversation) []Message {
	var builder strings.Builder
	if digest != "" {
		builder.WriteString("已有摘要：\n")
		builder.WriteString(digest)
		builder.WriteString("\n\n")
	}
	builder.WriteString("新的问答：\n")
	for _, turn := range turns {
		builder.WriteString("问：" + turn.Question + "\n")
		builder.WriteString("答：" + turn.Answer + "\n")
	}
	return []Message{
		{Role: RoleSystem, Content: withBackground(fmt.Sprintf(digestSystem, maxDigestRunes), cfg.Background)},
		{Role: RoleUser, Content: strings.TrimRight(builder.String(), "\n")},
	}
}

// clampRunes keeps a model reply inside the length its prompt asked for.
func clampRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

func withBackground(prompt, background string) string {
	if background == "" {
		return prompt
	}
	return prompt + "\n\n补充背景（由用户提供，优先采信其中的专有名词写法）：\n" + background
}

// withSpeakers explains the speaker labels only when the transcript carries
// them. A single-input session has nothing to explain.
func withSpeakers(prompt string, lines []Line) string {
	for _, line := range lines {
		if line.Speaker != "" {
			return prompt + "\n\n" + speakerNote
		}
	}
	return prompt
}

const speakerNote = `转写来自同时录制的多路音频：方括号里时间后面的词是说话人标签，「我」指使用这台电脑的人，其他标签是会议里的其他人。请分清每句话是谁说的。`

func formatLines(lines []Line) string {
	var builder strings.Builder
	for _, line := range lines {
		if line.Time.IsZero() {
			if line.Speaker != "" {
				builder.WriteString(line.Speaker + "：" + line.Text + "\n")
				continue
			}
			builder.WriteString(line.Text + "\n")
			continue
		}
		if line.Speaker != "" {
			builder.WriteString(fmt.Sprintf("[%s %s] %s\n", line.Time.Format("15:04:05"), line.Speaker, line.Text))
			continue
		}
		builder.WriteString(fmt.Sprintf("[%s] %s\n", line.Time.Format("15:04:05"), line.Text))
	}
	return strings.TrimRight(builder.String(), "\n")
}

// ParseInsights turns the model's reply into key points. Models drift between
// bullets, numbering and plain lines, so every common prefix is stripped.
func ParseInsights(reply string) []string {
	result := make([]string, 0, maxInsightsPerCall)
	for _, raw := range strings.Split(reply, "\n") {
		point := strings.TrimSpace(raw)
		point = strings.TrimLeft(point, "-*•·・‧+>《【 \t")
		point = trimNumberPrefix(point)
		point = strings.TrimSpace(strings.Trim(point, "》】"))
		if point == "" || strings.Trim(point, "。.") == emptyInsightMarker {
			continue
		}
		if runes := []rune(point); len(runes) > maxInsightRunes {
			point = string(runes[:maxInsightRunes]) + "…"
		}
		result = append(result, point)
		if len(result) == maxInsightsPerCall {
			break
		}
	}
	return result
}

func trimNumberPrefix(point string) string {
	index := 0
	runes := []rune(point)
	for index < len(runes) && runes[index] >= '0' && runes[index] <= '9' {
		index++
	}
	if index == 0 || index >= len(runes) {
		return point
	}
	switch runes[index] {
	case '.', '、', '）', ')', '：', ':':
		return strings.TrimSpace(string(runes[index+1:]))
	}
	return point
}

func recentLines(lines []Line, count int) []Line {
	if count <= 0 || len(lines) <= count {
		return append([]Line(nil), lines...)
	}
	return append([]Line(nil), lines[len(lines)-count:]...)
}

func recentInsightTexts(insights []Insight, count int) []string {
	if count > len(insights) {
		count = len(insights)
	}
	result := make([]string, 0, count)
	for _, insight := range insights[len(insights)-count:] {
		result = append(result, insight.Text)
	}
	return result
}

func totalRunes(lines []Line) int {
	total := 0
	for _, line := range lines {
		total += len([]rune(line.Text))
	}
	return total
}

func appendBounded[T any](items []T, item T, limit int) []T {
	items = append(items, item)
	if len(items) <= limit {
		return items
	}
	trimmed := make([]T, limit)
	copy(trimmed, items[len(items)-limit:])
	return trimmed
}

func trimFront[T any](items []T, limit int) []T {
	if len(items) <= limit {
		return items
	}
	trimmed := make([]T, limit)
	copy(trimmed, items[len(items)-limit:])
	return trimmed
}
