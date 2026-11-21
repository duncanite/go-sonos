package terminal_test

import (
	"testing"

	"github.com/duncanite/go-sonos/internal/terminal"
)

// TestText: what a terminal only draws is left as it is; what it obeys,
// and what is no text, is written out.
func TestText(t *testing.T) {
	t.Parallel()

	for _, row := range []struct{ name, text, want string }{
		{"a name", "Living Room", "Living Room"},
		{"nothing", "", ""},
		{"accents and other scripts", "Séjour 台所 кухня", "Séjour 台所 кухня"}, //nolint:gosmopolitan // text in other scripts is the case
		{"a picture", "Den 🔊", "Den 🔊"},
		{"a backslash", `a\b`, `a\b`},
		{"an escape sequence", "Den\x1b[2J", `Den\x1b[2J`},
		{"a line's end", "Den\nsonos: done", `Den\nsonos: done`},
		{"a carriage return", "Den\rdone", `Den\rdone`},
		{"a tab", "a\tb", `a\tb`},
		{"a bell and a NUL", "a\x07\x00b", `a\a\x00b`},
		{"a delete", "a\x7fb", `a\x7fb`},
		{"a control above ASCII", "a\u009b2Jb", `a\u009b2Jb`},
		{"a direction override", "abc\u202edef", `abc\u202edef`},
		{"a width of nothing", "a\u200bb", `a\u200bb`},
		{"a space that is not the plain one", "a\u00a0b\u2028c", `a\u00a0b\u2028c`},
		{"a byte that is no text", "a\xffb", `a\xffb`},
		{"the mark of what is no text, which is drawn", "a\ufffdb", "a\ufffdb"},
		{"no text after text that is plain", "plain\xc3", `plain\xc3`},
	} {
		if got := terminal.Text(row.text); got != row.want {
			t.Errorf("%s: Text(%q) = %q, want %q", row.name, row.text, got, row.want)
		}
	}
}
