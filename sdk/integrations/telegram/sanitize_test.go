package telegram

import (
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestSanitizeMessageText(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	tests := []struct {
		name    string
		input   string
		want    string
		wantLen int
	}{
		{
			name:  "clean text unchanged",
			input: "Hello, world!",
			want:  "Hello, world!",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "newlines preserved",
			input: "line1\nline2\r\nline3",
			want:  "line1\nline2\r\nline3",
		},
		{
			name:  "tabs preserved",
			input: "col1\tcol2",
			want:  "col1\tcol2",
		},
		{
			// U+200B ZERO WIDTH SPACE (Cf)
			name:    "zero-width space stripped",
			input:   "hello\u200bworld",
			want:    "helloworld",
			wantLen: len("helloworld"),
		},
		{
			// U+200D ZERO WIDTH JOINER (Cf)
			name:    "zero-width joiner stripped",
			input:   "hello\u200dworld",
			want:    "helloworld",
			wantLen: len("helloworld"),
		},
		{
			// U+FEFF BOM (Cf)
			name:    "BOM stripped",
			input:   "\ufefftext",
			want:    "text",
			wantLen: len("text"),
		},
		{
			// U+202E RIGHT-TO-LEFT OVERRIDE (Cf)
			name:    "bidi override stripped",
			input:   "normal\u202etext",
			want:    "normaltext",
			wantLen: len("normaltext"),
		},
		{
			// U+E0041 Tag Latin Small Letter A (Tag block)
			name:    "tag block stripped",
			input:   "a\U000E0041b",
			want:    "ab",
			wantLen: len("ab"),
		},
		{
			// U+FE00 VARIATION SELECTOR-1 (not Cf but explicitly blocked)
			name:    "variation selector stripped",
			input:   "a\ufe00b",
			want:    "ab",
			wantLen: len("ab"),
		},
		{
			name:  "command with slash preserved",
			input: "/run task-123",
			want:  "/run task-123",
		},
		{
			// U+1F44D THUMBS UP (not Cf, must be preserved)
			name:  "unicode emoji preserved",
			input: "done \U0001F44D",
			want:  "done \U0001F44D",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeMessageText(tt.input, logger)
			if got != tt.want {
				t.Errorf("sanitizeMessageText(%q) = %q, want %q", tt.input, got, tt.want)
			}
			if tt.wantLen != 0 && len([]rune(got)) != tt.wantLen {
				t.Errorf("result rune len = %d, want %d", len([]rune(got)), tt.wantLen)
			}
		})
	}
}

// TestSanitizeMessageTextWarnsOnStrip verifies the warn path fires with
// correct log fields.
func TestSanitizeMessageTextWarnsOnStrip(t *testing.T) {
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// U+200B ZERO WIDTH SPACE embedded mid-word
	input := "inject\u200bhere"
	got := sanitizeMessageText(input, logger)

	if got != "injecthere" {
		t.Errorf("got %q, want %q", got, "injecthere")
	}
	if !strings.Contains(buf.String(), "invisible_unicode_stripped") {
		t.Errorf("expected warning log to contain invisible_unicode_stripped, got: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "telegram") {
		t.Errorf("expected warning log to contain source=telegram, got: %s", buf.String())
	}
}
