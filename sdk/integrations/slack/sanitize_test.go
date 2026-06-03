package slack

import (
	"log/slog"
	"strings"
	"testing"
)

func TestSanitizeMessageText(t *testing.T) {
	logger := slog.Default()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "clean text unchanged",
			input: "hello world",
			want:  "hello world",
		},
		{
			name:  "empty string unchanged",
			input: "",
			want:  "",
		},
		{
			name:  "normal unicode unchanged",
			input: "привет мир",
			want:  "привет мир",
		},
		{
			name:  "invisible unicode stripped",
			input: "hello​world",
			want:  "helloworld",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeMessageText(tt.input, logger)
			if got != tt.want {
				t.Errorf("sanitizeMessageText(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSanitizeMessageTextLogsOnStrip(t *testing.T) {
	var logBuf strings.Builder
	handler := slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})
	logger := slog.New(handler)

	input := "hello​world"
	_ = sanitizeMessageText(input, logger)

	logged := logBuf.String()
	if !strings.Contains(logged, "invisible_unicode_stripped") {
		t.Errorf("expected invisible_unicode_stripped in log, got: %q", logged)
	}
	if !strings.Contains(logged, "slack") {
		t.Errorf("expected source=slack in log, got: %q", logged)
	}
}
