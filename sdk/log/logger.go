// Package log defines the minimal logging contract the SDK depends on.
//
// The SDK does NOT impose a logging implementation or a global logger.
// Consuming applications pass any value satisfying Logger. A standard
// *slog.Logger satisfies this interface directly, so callers can pass
// slog.Default() with no adapter:
//
//	poller := github.New(cfg, github.WithLogger(slog.Default()))
package log

// Logger is the structured-logging contract used across SDK integrations.
// It is intentionally a subset of *log/slog.Logger's method set so that a
// *slog.Logger satisfies it without a wrapper.
type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// Nop is a Logger that discards all records. Useful as a zero value when a
// consumer supplies no logger.
type Nop struct{}

func (Nop) Debug(string, ...any) {}
func (Nop) Info(string, ...any)  {}
func (Nop) Warn(string, ...any)  {}
func (Nop) Error(string, ...any) {}
