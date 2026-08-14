package sherpancnn

import "unicode/utf8"

type recognitionUpdate struct {
	partial     string
	emitPartial bool
	final       string
	emitFinal   bool
	reset       bool
}

// resultProcessor converts the accumulated native stream result into stable
// KSpeech events. It retains the legacy 80-rune forced sentence boundary.
type resultProcessor struct {
	lastPartial   string
	maxTextLength int
}

func newResultProcessor(maxTextLength int) *resultProcessor {
	return &resultProcessor{maxTextLength: maxTextLength}
}

func (p *resultProcessor) update(text string, endpoint bool) recognitionUpdate {
	update := recognitionUpdate{}
	if text != "" && text != p.lastPartial {
		update.partial = text
		update.emitPartial = true
		p.lastPartial = text
	}

	tooLong := text != "" && p.maxTextLength > 0 && utf8.RuneCountInString(text) >= p.maxTextLength
	if text != "" && (endpoint || tooLong) {
		update.final = text
		update.emitFinal = true
		update.reset = true
		p.lastPartial = ""
		return update
	}
	if endpoint {
		update.reset = true
		p.lastPartial = ""
	}
	return update
}

func (p *resultProcessor) finish(text string) recognitionUpdate {
	update := recognitionUpdate{}
	if text == "" {
		text = p.lastPartial
	}
	if text != "" && text != p.lastPartial {
		update.partial = text
		update.emitPartial = true
	}
	if text != "" {
		update.final = text
		update.emitFinal = true
	}
	p.lastPartial = ""
	return update
}
