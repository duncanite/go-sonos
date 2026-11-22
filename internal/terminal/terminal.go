// Package terminal writes text a device chose so that a terminal shows it
// and obeys none of it. What a speaker says in refusing an action is
// whatever the speaker, or whoever answers for it, wrote: a line's end in
// it forges a line of the program's output, and a control or a direction
// mark moves, hides or reorders what is on the screen.
package terminal

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Text is text with everything a terminal would not simply show written
// out as Go writes it in a string: a control or a format character, a
// line's end and a tab among them, a space that is not the plain one, and
// a byte that is no text. What is left is shown as it is, accents and all.
func Text(text string) string {
	plain := true

	for _, char := range text {
		if !unicode.IsPrint(char) {
			plain = false

			break
		}
	}

	if plain && utf8.ValidString(text) {
		return text
	}

	var out strings.Builder

	for index := 0; index < len(text); {
		char, size := utf8.DecodeRuneInString(text[index:])

		switch {
		case char == utf8.RuneError && size == 1:
			out.WriteString(`\x` + strconv.FormatUint(uint64(text[index]), 16))
		case unicode.IsPrint(char):
			out.WriteRune(char)
		default:
			quoted := strconv.QuoteRune(char)
			out.WriteString(quoted[1 : len(quoted)-1])
		}

		index += size
	}

	return out.String()
}
