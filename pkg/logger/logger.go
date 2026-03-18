package logger

import (
	"context"
	"log/slog"
	"os"
	"runtime"
	"time"
)

// Logger wraps slog.Logger with component-scoped structured logging.
type Logger struct {
	inner     *slog.Logger
	component string
}

// New creates a Logger for the given component using JSON output.
func New(component string) *Logger {
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level:     slog.LevelInfo,
		AddSource: false,
	})
	return &Logger{
		inner:     slog.New(h),
		component: component,
	}
}

// WithHandler creates a Logger backed by a custom slog.Handler.
func WithHandler(component string, h slog.Handler) *Logger {
	return &Logger{
		inner:     slog.New(h),
		component: component,
	}
}

func (l *Logger) attrs(extra []slog.Attr) []slog.Attr {
	base := []slog.Attr{
		slog.String("component", l.component),
		slog.String("ts", time.Now().UTC().Format(time.RFC3339Nano)),
	}
	return append(base, extra...)
}

// Info logs at INFO level.
func (l *Logger) Info(msg string, attrs ...slog.Attr) {
	l.log(context.Background(), slog.LevelInfo, msg, attrs)
}

// Error logs at ERROR level with automatic stack capture.
func (l *Logger) Error(msg string, err error, attrs ...slog.Attr) {
	a := append(attrs, slog.String("error", err.Error()))
	a = append(a, slog.String("stack", captureStack(3)))
	l.log(context.Background(), slog.LevelError, msg, a)
}

// Warn logs at WARN level.
func (l *Logger) Warn(msg string, attrs ...slog.Attr) {
	l.log(context.Background(), slog.LevelWarn, msg, attrs)
}

// Debug logs at DEBUG level.
func (l *Logger) Debug(msg string, attrs ...slog.Attr) {
	l.log(context.Background(), slog.LevelDebug, msg, attrs)
}

func (l *Logger) log(ctx context.Context, level slog.Level, msg string, extra []slog.Attr) {
	all := l.attrs(extra)
	args := make([]any, len(all))
	for i, a := range all {
		args[i] = a
	}
	l.inner.Log(ctx, level, msg, args...)
}

func captureStack(skip int) string {
	const maxFrames = 10
	var pcs [maxFrames]uintptr
	n := runtime.Callers(skip, pcs[:])
	if n == 0 {
		return ""
	}
	frames := runtime.CallersFrames(pcs[:n])
	buf := make([]byte, 0, 512)
	for {
		frame, more := frames.Next()
		buf = append(buf, frame.Function...)
		buf = append(buf, '\n')
		if !more {
			break
		}
	}
	return string(buf)
}
