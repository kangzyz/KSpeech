package command

import "strings"

// stdoutParser implements the legacy line protocol without relying on scanner
// token limits. It buffers UTF-8 bytes until LF, ignores CR, and tolerates a
// multi-byte rune being split across writes.
type stdoutParser struct {
	current       []byte
	previous      string
	consecutiveLF int
	onPartial     func(string)
	onFinal       func(string)
}

func newStdoutParser(onPartial, onFinal func(string)) *stdoutParser {
	return &stdoutParser{onPartial: onPartial, onFinal: onFinal}
}

func (p *stdoutParser) Write(data []byte) (int, error) {
	for _, char := range data {
		switch char {
		case '\r':
			// CR is ignored and therefore does not break consecutive LF state.
		case '\n':
			p.consecutiveLF++
			switch p.consecutiveLF {
			case 1:
				text := strings.ToValidUTF8(string(p.current), "\uFFFD")
				p.previous = text
				p.current = p.current[:0]
				if text != "" && p.onPartial != nil {
					p.onPartial(text)
				}
			case 2:
				if p.previous != "" && p.onFinal != nil {
					p.onFinal(p.previous)
				}
			}
		default:
			p.consecutiveLF = 0
			p.current = append(p.current, char)
		}
	}
	return len(data), nil
}
