package assistant

import (
	"strings"
	"unicode"
)

// Auto-answering is the one path that spends money on a sentence nobody asked
// to send, so this classifier is tuned for precision rather than recall: a
// question it misses can still be typed into the chat box, while a false
// positive costs a request and pushes an unrelated answer onto the screen in
// the middle of a meeting.
//
// Three things decide the verdict:
//   - Where the marker sits. Only the last clause is examined, because a
//     caption is often a statement that ends in a question, never the reverse.
//   - What precedes the marker. "不知道能不能提前" is a musing, not a question
//     aimed at anyone, and its marker reads exactly like a real one.
//   - How strong the marker is. "为什么" is a question wherever it lands;
//     "多少" and "怎么样" are ordinary words that need corroboration.
const (
	// A caption shorter than this is a recognition fragment ("嗯？").
	minQuestionRunes = 3
	// Past this length a caption is a paragraph of narration that happens to
	// contain an interrogative word. Real questions are short.
	maxQuestionCaptionRunes = 60
	// A weak marker only counts inside a short clause: "这个多少钱" asks,
	// "这块预算多少还是要再确认一下排期" states.
	maxWeakClauseRunes = 22
	// How far back of the marker is inspected for something that turns it into
	// reported speech or an intention.
	suppressorWindow = 6
)

// strongMarkers are interrogative wherever they appear.
var strongMarkers = []string{
	"为什么", "为何", "为啥", "怎么办", "怎么回事", "怎么看", "如何",
	"是不是", "是否", "有没有", "有无", "能不能", "能否", "可不可以", "可以吗",
	"行不行", "对不对", "好不好", "要不要", "什么时候", "什么意思", "什么情况",
	"多长时间", "多久", "哪一个", "哪位", "请问", "麻烦问", "想问", "问一下",
	"谁负责", "谁知道", "谁来", "谁能", "谁跟",
}

// weakMarkers are everyday words that only ask a question in a short clause.
var weakMarkers = []string{
	"怎么样", "怎么讲", "怎么说", "多少", "几个", "哪个", "哪些", "哪里", "哪边",
	"什么", "谁", "干嘛", "咋样",
}

// requestMarkers ask the listener to speak at length. They are the reason an
// interview is worth assisting at all ("介绍一下你做过的项目"), and also the
// most common false positive, because a speaker announces the same thing about
// themselves ("我先介绍一下我们团队"). They count only when the sentence is
// aimed at someone else.
var requestMarkers = []string{
	"介绍一下", "讲一下", "说一下", "聊一下", "谈一下", "解释一下", "说明一下",
	"展开讲", "展开说", "说说看", "讲讲", "聊聊", "举个例子", "举例说明",
}

// suppressors turn the marker that follows them into reported speech, a plan,
// or a hedge — never a question put to the listener.
var suppressors = []string{
	"不知道", "不清楚", "不确定", "不明白", "没搞懂", "不管", "不论", "无论",
	"看看", "想看看", "确认一下", "取决于", "视乎", "或多或少", "多多少少", "差不多",
}

// intentionHedges say the question will be put to somebody else later. They
// disqualify the whole clause rather than one marker, because the interrogative
// word that follows belongs to the question being planned, not to this one:
// "回头问一下他们接口什么时候能好" asks nobody in the room.
var intentionHedges = []string{
	"回头问", "回头再问", "待会问", "等下问", "晚点问", "会后问", "再问一下",
	"去问", "问问他", "问问她", "问问他们", "找他们问", "找她问", "找他问",
}

// firstPersonOpeners mark a speaker talking about their own next move.
var firstPersonOpeners = []string{"我来", "我先", "我这边", "我们先", "我们来", "咱们先", "让我", "由我"}

// listenerMarkers are the second-person forms that make a request a request.
var listenerMarkers = []string{"你", "您", "你们", "咱", "麻烦", "请"}

// trailingQuestionParticles only mean a question at the end of a clause.
var trailingQuestionParticles = []rune{'吗', '呢', '嘛'}

var englishQuestionOpeners = map[string]bool{
	"what": true, "why": true, "how": true, "when": true, "where": true,
	"who": true, "whom": true, "whose": true, "which": true,
	"can": true, "could": true, "would": true, "should": true, "shall": true,
	"do": true, "does": true, "did": true, "is": true, "are": true,
	"was": true, "were": true, "will": true, "may": true,
}

// clauseBreaks end one clause inside a caption. The punctuation pass adds them
// after a sentence is finalized, which is exactly when this runs.
var clauseBreaks = []rune{'。', '！', '；', '?', '？', '!', ';', '\n'}

// LooksLikeQuestion reports whether a finalized caption reads like a question
// somebody expects an answer to.
func LooksLikeQuestion(text string) bool {
	trimmed := strings.TrimSpace(text)
	length := len([]rune(trimmed))
	if length < minQuestionRunes || length > maxQuestionCaptionRunes {
		return false
	}
	clause := lastClause(trimmed)
	if clause == "" || containsAny(clause, intentionHedges) {
		return false
	}
	if endsWithQuestionMark(trimmed) {
		// The mark is not the speaker's: internal/punctuation appends it from a
		// marker list of its own, so a hedge it mistook for a question arrives
		// here already wearing a 问号. It has to clear the same check as an
		// unmarked caption.
		return !containsSuppressor(clause)
	}
	if hasLiveMarker(clause, strongMarkers) {
		return true
	}
	if endsWithQuestionParticle(clause) && !suppressedAt(clause, len([]rune(clause))) {
		return true
	}
	if addressesListener(clause) && hasLiveMarker(clause, requestMarkers) {
		return true
	}
	if len([]rune(clause)) <= maxWeakClauseRunes && hasLiveMarker(clause, weakMarkers) {
		// A weak marker in a short clause is still ambiguous on its own, so it
		// has to be either aimed at someone or left hanging at the end, the way
		// a spoken question is: "这个多少钱" / "你那边几个人".
		return addressesListener(clause) || endsNearMarker(clause, weakMarkers)
	}
	return looksLikeEnglishQuestion(clause)
}

// lastClause returns the final clause of a caption. A caption that states
// something and then asks ("方案我看过了，你觉得能赶上吗") asks in its last
// clause; the reverse ordering does not occur in speech.
func lastClause(text string) string {
	runes := []rune(text)
	for index := len(runes) - 1; index >= 0; index-- {
		if !isClauseBreak(runes[index]) {
			continue
		}
		// Trailing punctuation belongs to the clause before it.
		if index == len(runes)-1 || onlyPunctuationAfter(runes[index+1:]) {
			continue
		}
		return strings.TrimSpace(string(runes[index+1:]))
	}
	return strings.TrimSpace(text)
}

func isClauseBreak(r rune) bool {
	for _, candidate := range clauseBreaks {
		if r == candidate {
			return true
		}
	}
	return false
}

func onlyPunctuationAfter(runes []rune) bool {
	for _, r := range runes {
		if !unicode.IsSpace(r) && !unicode.IsPunct(r) && !unicode.IsSymbol(r) {
			return false
		}
	}
	return true
}

// hasLiveMarker reports whether one of markers occurs somewhere it still reads
// as a question rather than as part of a hedge or of the speaker's own plan.
func hasLiveMarker(clause string, markers []string) bool {
	for _, marker := range markers {
		for offset := 0; ; {
			index := strings.Index(clause[offset:], marker)
			if index < 0 {
				break
			}
			position := len([]rune(clause[:offset+index]))
			if !suppressedAt(clause, position) {
				return true
			}
			offset += index + len(marker)
			if offset >= len(clause) {
				break
			}
		}
	}
	return false
}

// containsSuppressor reports whether the clause hedges anywhere at all. It is
// the blunt form of suppressedAt, used when the evidence is a question mark
// that no marker position can be tied to.
func containsSuppressor(clause string) bool { return containsAny(clause, suppressors) }

func containsAny(text string, phrases []string) bool {
	for _, phrase := range phrases {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

// suppressedAt reports whether the few runes before a marker turn it into
// something other than a question.
func suppressedAt(clause string, position int) bool {
	runes := []rune(clause)
	if position > len(runes) {
		position = len(runes)
	}
	start := position - suppressorWindow
	if start < 0 {
		start = 0
	}
	before := string(runes[start:position])
	for _, suppressor := range suppressors {
		if strings.Contains(before, suppressor) {
			return true
		}
	}
	return false
}

// addressesListener reports that the clause is aimed at someone else. A clause
// the speaker opens with their own next move is about themselves however many
// request words it contains.
func addressesListener(clause string) bool {
	for _, opener := range firstPersonOpeners {
		if strings.Contains(clause, opener) {
			return false
		}
	}
	for _, marker := range listenerMarkers {
		if strings.Contains(clause, marker) {
			return true
		}
	}
	// An imperative drops the subject: "介绍一下项目背景" is still a request.
	return startsWithMarker(clause, requestMarkers)
}

func startsWithMarker(clause string, markers []string) bool {
	for _, marker := range markers {
		if strings.HasPrefix(clause, marker) {
			return true
		}
	}
	return false
}

// endsNearMarker reports that the clause trails off right after its marker,
// which is how a spoken question without a question mark ends: "预算多少",
// "负责人是谁".
func endsNearMarker(clause string, markers []string) bool {
	runes := []rune(strings.TrimRightFunc(clause, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)
	}))
	const tail = 4
	start := len(runes) - tail
	if start < 0 {
		start = 0
	}
	ending := string(runes[start:])
	for _, marker := range markers {
		if strings.Contains(ending, marker) {
			return true
		}
	}
	return false
}

func endsWithQuestionMark(text string) bool {
	for _, r := range reverseRunes(text) {
		switch r {
		case '?', '？', '﹖', '⁇':
			return true
		}
		if unicode.IsSpace(r) {
			continue
		}
		return false
	}
	return false
}

func endsWithQuestionParticle(clause string) bool {
	last, ok := lastLetter(clause)
	if !ok {
		return false
	}
	for _, particle := range trailingQuestionParticles {
		if last == particle {
			return true
		}
	}
	return false
}

// lastLetter returns the final rune that is not punctuation or whitespace.
func lastLetter(text string) (rune, bool) {
	for _, r := range reverseRunes(text) {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		return r, true
	}
	return 0, false
}

func reverseRunes(text string) []rune {
	runes := []rune(text)
	for left, right := 0, len(runes)-1; left < right; left, right = left+1, right-1 {
		runes[left], runes[right] = runes[right], runes[left]
	}
	return runes
}

// looksLikeEnglishQuestion accepts the inverted opening English questions use.
// A statement can open the same way ("can we ship it" versus "we can ship it"),
// so the clause also has to be short enough to be one spoken question.
func looksLikeEnglishQuestion(clause string) bool {
	fields := strings.FieldsFunc(strings.ToLower(clause), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(fields) < 2 || len(fields) > 14 {
		return false
	}
	return englishQuestionOpeners[fields[0]]
}
