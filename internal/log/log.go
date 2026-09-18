// Package log is the single logging facility for OKEGuiDX.
//
// Do not introduce alternative loggers (log.Printf, fmt.Println) in library
// code. Everything goes through this package so that the daemon can configure
// one output, one format and one level for the whole process.
package log

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// LevelTrace is below slog.LevelDebug. The legacy NLog configuration used a
// Trace level for raw child-process output, so it is kept here.
const LevelTrace = slog.Level(-8)

var (
	mu      sync.RWMutex
	logger  = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: LevelTrace}))
	enabled = true
)

// SetOutput redirects all logging to w.
func SetOutput(w io.Writer) {
	mu.Lock()
	defer mu.Unlock()
	h := slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: LevelTrace,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.LevelKey {
				if lv, ok := a.Value.Any().(slog.Level); ok && lv == LevelTrace {
					a.Value = slog.StringValue("TRACE")
				}
			}
			return a
		},
	})
	logger = slog.New(h)
}

// SetLevel sets the minimum level that is emitted. Accepted values match the
// strings used by the legacy OKEGuiConfig.json "logLevel" field: TRACE, DEBUG,
// INFO, WARN, ERROR, FATAL (case-insensitive).
func SetLevel(level string) {
	var lv slog.Level
	switch strings.ToUpper(strings.TrimSpace(level)) {
	case "TRACE":
		lv = LevelTrace
	case "DEBUG", "":
		lv = slog.LevelDebug
	case "INFO":
		lv = slog.LevelInfo
	case "WARN", "WARNING":
		lv = slog.LevelWarn
	case "ERROR", "FATAL":
		lv = slog.LevelError
	default:
		lv = slog.LevelDebug
	}
	mu.Lock()
	defer mu.Unlock()
	logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: lv,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.LevelKey {
				if v, ok := a.Value.Any().(slog.Level); ok && v == LevelTrace {
					a.Value = slog.StringValue("TRACE")
				}
			}
			return a
		},
	}))
}

// SetEnabled turns all logging on or off without touching the configured level.
func SetEnabled(v bool) {
	mu.Lock()
	defer mu.Unlock()
	enabled = v
}

// L returns the process-wide logger.
func L() *slog.Logger {
	mu.RLock()
	defer mu.RUnlock()
	return logger
}

func logAt(ctx context.Context, lv slog.Level, msg string, args ...any) {
	mu.RLock()
	on := enabled
	lg := logger
	mu.RUnlock()
	if !on {
		return
	}
	lg.Log(ctx, lv, msg, args...)
}

// Trace logs raw child-process output and other high-volume detail.
func Trace(msg string, args ...any) { logAt(context.Background(), LevelTrace, msg, args...) }

// Debug logs developer-facing detail.
func Debug(msg string, args ...any) { logAt(context.Background(), slog.LevelDebug, msg, args...) }

// Info logs normal progress.
func Info(msg string, args ...any) { logAt(context.Background(), slog.LevelInfo, msg, args...) }

// Warn logs recoverable problems.
func Warn(msg string, args ...any) { logAt(context.Background(), slog.LevelWarn, msg, args...) }

// Error logs failures.
func Error(msg string, args ...any) { logAt(context.Background(), slog.LevelError, msg, args...) }

// With returns a logger carrying the given attributes. It is safe to store the
// result; it is not affected by later SetOutput/SetLevel calls.
func With(args ...any) *slog.Logger { return L().With(args...) }

// Ctx returns a logger carrying attributes from ctx, when one is present.
func Ctx(ctx context.Context) *slog.Logger {
	if ctx == nil {
		return L()
	}
	return L()
}
