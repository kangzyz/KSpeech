package sherpaonnx

import "unicode/utf8"

type recognitionUpdate struct {
	partial     string
	emitPartial bool
	final       string
	emitFinal   bool
	reset       bool
}

// resultProcessor converts sherpa's accumulated stream text into stable
// partial/final events. It suppresses duplicate partials, finalizes endpoints,
// and preserves the legacy 80-character forced sentence boundary.
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
		// Reset even for an empty endpoint. Otherwise sherpa can keep reporting
		// the same endpoint without allowing the next utterance to begin.
		update.reset = true
		p.lastPartial = ""
	}
	return update
}

func (p *resultProcessor) finish(text string) recognitionUpdate {
	update := recognitionUpdate{}
	if text == "" {
		// A backend flush should retain its accumulated result, but keeping the
		// last delivered partial avoids losing a sentence if a backend reports an
		// empty terminal snapshot.
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
