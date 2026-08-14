package punctuation

import (
	"strings"
	"unicode"
)

// A shorter fragment is usually a false trigger ("嗯", "the") rather than a
// sentence, so it keeps whatever the model produced.
const minSentenceRunes = 2

// Marks that already end a sentence: nothing is appended after them.
const terminalMarks = "。．.！!？?…〜~"

// Marks that only separate clauses. A sentence that stops on one of them is
// closed by replacing it, so captions never end on a dangling comma.
const clauseMarks = "，,、；;：:"

// Question wording, deliberately narrow: a wrong 问号 is more noticeable than a
// missing one. internal/assistant carries a wider list for a different
// decision — whether a caption is worth sending to a model.
var questionMarkers = []string{
	"为什么", "为何", "为啥", "怎么办", "怎么样", "怎么回事", "怎么讲", "怎么说",
	"是不是", "是否", "有没有", "能不能", "可不可以", "可以吗", "行不行",
	"对不对", "好不好", "要不要", "什么时候", "什么意思", "什么情况",
	"多长时间", "多少钱", "哪一个", "请问", "麻烦问",
}

// Sentence-final particles only mean a question at the end of the sentence.
const questionParticles = "吗呢嘛"

var englishQuestionOpeners = map[string]bool{
	"what": true, "why": true, "how": true, "when": true, "where": true,
	"who": true, "whom": true, "whose": true, "which": true,
	"can": true, "could": true, "would": true, "should": true, "shall": true,
	"do": true, "does": true, "did": true, "is": true, "are": true,
	"was": true, "were": true, "will": true,
}

// Rules returns the dependency-free punctuator. It only decides the mark that
// closes a finished sentence: without timing information or a language model,
// guessing where the commas go inside the sentence reads worse than leaving
// them out.
func Rules() Punctuator { return rulePunctuator{} }

type rulePunctuator struct{}

func (rulePunctuator) Close() error { return nil }

func (rulePunctuator) Punctuate(text string) string {
	trimmed := strings.TrimSpace(text)
	runes := []rune(trimmed)
	if len(runes) < minSentenceRunes {
		return text
	}
	last := runes[len(runes)-1]
	if strings.ContainsRune(terminalMarks, last) {
		return trimmed
	}
	if strings.ContainsRune(clauseMarks, last) {
		trimmed = strings.TrimSpace(string(runes[:len(runes)-1]))
		if trimmed == "" {
			return text
		}
	}
	return trimmed + terminalMark(trimmed)
}

func terminalMark(text string) string {
	if containsHan(text) {
		if looksLikeQuestion(text) {
			return "？"
		}
		return "。"
	}
	if looksLikeQuestion(text) {
		return "?"
	}
	return "."
}

// containsHan decides between full-width and ASCII marks. It looks at the whole
// sentence rather than its last rune so a Chinese sentence ending in a number
// or an English term still gets 。 instead of a lone dot.
func containsHan(text string) bool {
	for _, r := range text {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func looksLikeQuestion(text string) bool {
	for _, marker := range questionMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	if last, ok := lastLetter(text); ok && strings.ContainsRune(questionParticles, last) {
		return true
	}
	return startsWithEnglishQuestionWord(text)
}

// lastLetter returns the final rune that is neither punctuation nor whitespace.
func lastLetter(text string) (rune, bool) {
	runes := []rune(text)
	for index := len(runes) - 1; index >= 0; index-- {
		r := runes[index]
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		return r, true
	}
	return 0, false
}

func startsWithEnglishQuestionWord(text string) bool {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(fields) < 2 {
		return false
	}
	return englishQuestionOpeners[fields[0]]
}
