package lgr

import (
	"bytes"
	"io"
	"os"
	"strings"
	"sync"
)

// Level represents log severity.
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
	LevelSilent
)

func ParseLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return LevelDebug
	case "info", "":
		return LevelInfo
	case "warn", "warning":
		return LevelWarn
	case "error":
		return LevelError
	case "silent":
		return LevelSilent
	default:
		return LevelInfo
	}
}

// LevelWriter is an io.Writer that filters log output by level tags.
// Usage: log.SetOutput(logger.NewLevelWriter("info"))
type LevelWriter struct {
	mu    sync.Mutex
	out   io.Writer
	level Level
}

func NewLevelWriter(levelStr string) *LevelWriter {
	return &LevelWriter{
		out:   os.Stderr,
		level: ParseLevel(levelStr),
	}
}

// SetLevel changes the minimum log level at runtime.
func (w *LevelWriter) SetLevel(levelStr string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.level = ParseLevel(levelStr)
}

// Write implements io.Writer. It checks the first tag in the message
// (e.g. "[DEBUG]", "[INFO]") against the configured level.
func (w *LevelWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	lvl := w.level
	w.mu.Unlock()

	msgLevel := detectLevel(p)
	if msgLevel < lvl {
		return len(p), nil
	}
	return w.out.Write(p)
}

var levelTags = []struct {
	tag   []byte
	level Level
}{
	{[]byte("[DEBUG]"), LevelDebug},
	{[]byte("[INFO]"), LevelInfo},
	{[]byte("[WARN]"), LevelWarn},
	{[]byte("[ERROR]"), LevelError},
}

func detectLevel(line []byte) Level {
	for _, lt := range levelTags {
		if bytes.Contains(line, lt.tag) {
			return lt.level
		}
	}
	return LevelInfo
}