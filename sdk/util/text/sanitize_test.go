package text

import "testing"

// Invisible runes built from numeric code points so this source file stays
// pure ASCII. A literal U+FEFF (BOM), for instance, is rejected by the Go
// scanner even inside a string literal.
var (
	zwsp = string(rune(0x200B)) // zero-width space
	zwnj = string(rune(0x200C)) // zero-width non-joiner
	zwj  = string(rune(0x200D)) // zero-width joiner
	bom  = string(rune(0xFEFF)) // byte order mark
	rlo  = string(rune(0x202E)) // right-to-left override (bidi)
	vs16 = string(rune(0xFE0F)) // variation selector-16
	tagA = string(rune(0xE0041))
	emo  = string(rune(0x1F44D)) // thumbs-up base emoji
)

func TestSanitizeUntrusted(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantClean string
		wantStrip int
	}{
		{"empty", "", "", 0},
		{"clean ascii", "hello world", "hello world", 0},
		{"preserve whitespace", "a\tb\nc\rd", "a\tb\nc\rd", 0},
		{"zero width space", "a" + zwsp + "b", "ab", 1},
		{"bom", bom + "hello", "hello", 1},
		{"bidi override", "a" + rlo + "b", "ab", 1},
		{"tag block", "x" + tagA + "y", "xy", 1},
		{"variation selector", "a" + vs16 + "b", "ab", 1},
		{"multiple zero-width", zwsp + "a" + zwnj + "b" + zwj + "c", "abc", 3},
		{"preserve emoji base", "ok " + emo + " done", "ok " + emo + " done", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clean, stripped := SanitizeUntrusted(tt.in)
			if clean != tt.wantClean {
				t.Errorf("clean = %q, want %q", clean, tt.wantClean)
			}
			if stripped != tt.wantStrip {
				t.Errorf("stripped = %d, want %d", stripped, tt.wantStrip)
			}
		})
	}
}

func TestSanitizeUntrustedString(t *testing.T) {
	in := "a" + zwsp + "b"
	if got := SanitizeUntrustedString(in); got != "ab" {
		t.Errorf("SanitizeUntrustedString = %q, want %q", got, "ab")
	}
}
