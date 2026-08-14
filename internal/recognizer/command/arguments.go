package command

import (
	"fmt"
	"strings"
	"unicode"
)

// splitArguments converts the legacy single Arguments string to the argument
// vector required by os/exec. It supports whitespace grouping, single and
// double quotes, empty quoted arguments, and escaped quote/whitespace. A
// backslash before an ordinary character is preserved so Windows paths remain
// intact.
func splitArguments(input string) ([]string, error) {
	var (
		args         []string
		current      strings.Builder
		quote        rune
		tokenStarted bool
	)
	runes := []rune(input)
	for index := 0; index < len(runes); index++ {
		char := runes[index]
		if quote == 0 && unicode.IsSpace(char) {
			if tokenStarted {
				args = append(args, current.String())
				current.Reset()
				tokenStarted = false
			}
			continue
		}
		if char == '\'' || char == '"' {
			switch {
			case quote == 0:
				quote = char
				tokenStarted = true
			case quote == char:
				quote = 0
			default:
				current.WriteRune(char)
			}
			continue
		}
		if char == '\\' && index+1 < len(runes) {
			next := runes[index+1]
			if next == '\'' || next == '"' || unicode.IsSpace(next) {
				current.WriteRune(next)
				tokenStarted = true
				index++
				continue
			}
		}
		current.WriteRune(char)
		tokenStarted = true
	}
	if quote != 0 {
		return nil, fmt.Errorf("parse command arguments: unterminated %q quote", quote)
	}
	if tokenStarted {
		args = append(args, current.String())
	}
	return args, nil
}
