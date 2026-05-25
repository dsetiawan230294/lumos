// Package logging provides a small wrapper around slog with a consistent
// configuration across the Lumos binary.
package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// New returns a slog.Logger writing to w at the given level.
// level accepts "debug", "info", "warn", "error" (case-insensitive); unknown
// values default to info.
func New(w io.Writer, level string) *slog.Logger {
	if w == nil {
		w = os.Stderr
	}
	var lvl slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	h := slog.NewTextHandler(w, &slog.HandlerOptions{Level: lvl})
	return slog.New(h)
}

// Default returns a logger writing to stderr at info level.
func Default() *slog.Logger { return New(os.Stderr, "info") }
