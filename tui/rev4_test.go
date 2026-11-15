package tui

import (
	"strings"
	"testing"
)

func TestRevSanitizeC1(t *testing.T) {
	for _, in := range []string{"a\u009b2Jb", "a\x9b2Jb", "a\u0085b", "a\x1b]8;;http://e\x07b"} {
		out := sanitize(in)
		for _, r := range out {
			if r < 0x20 && r != '\n' || r == 0x7f || r >= 0x80 && r <= 0x9f {
				t.Errorf("%q -> %q still contains control %U", in, out, r)
			}
		}
		if strings.ContainsRune(out, 0x1b) {
			t.Errorf("%q -> %q contains ESC", in, out)
		}
	}
}
