package plane

import (
	"io"
	"log/slog"
)

// noopLogger returns a logger that discards all output.
// Used in tests where logging output is not relevant.
func noopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
