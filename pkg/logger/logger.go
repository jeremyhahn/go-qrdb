// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

// Package logger provides structured logging for QRDB using log/slog.
// It supports context-aware logging, multiple output formats, and log levels.
package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Custom log levels for trace, fatal, and panic.
const (
	LevelTrace = slog.Level(-8)
	LevelFatal = slog.Level(12)
	LevelPanic = slog.Level(16)
)

// Logger wraps slog.Logger with additional functionality.
// It implements the interfaces.Logger interface through structural typing.
type Logger struct {
	slog   *slog.Logger
	level  *slog.LevelVar
	fields map[string]interface{}
	config LogConfig
}

// contextKey is the type for context keys to avoid collisions.
type contextKey string

const (
	// loggerContextKey is the key for storing logger in context.
	loggerContextKey contextKey = "logger"
)

// New creates a new Logger with the given configuration.
func New(config LogConfig) (*Logger, error) {
	// Validate and apply defaults
	if err := config.Validate(); err != nil {
		return nil, err
	}
	config = config.WithDefaults()

	// Configure output
	var output io.Writer
	switch config.Output {
	case "stdout":
		output = os.Stdout
	case "stderr":
		output = os.Stderr
	case "file":
		if config.File == "" {
			config.File = "qrdb.log"
		}
		file, err := os.OpenFile(config.File, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, err
		}
		output = file
	default:
		output = os.Stdout
	}

	// Parse log level
	level := parseLevel(config.Level)
	levelVar := new(slog.LevelVar)
	levelVar.Set(level)

	// Configure handler based on format
	var handler slog.Handler
	opts := &slog.HandlerOptions{
		Level:     levelVar,
		AddSource: true,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// Custom level names for trace, fatal, panic
			if a.Key == slog.LevelKey {
				level, ok := a.Value.Any().(slog.Level)
				if ok {
					switch level {
					case LevelTrace:
						a.Value = slog.StringValue("TRACE")
					case LevelFatal:
						a.Value = slog.StringValue("FATAL")
					case LevelPanic:
						a.Value = slog.StringValue("PANIC")
					}
				}
			}
			return a
		},
	}

	if config.Format == "console" {
		handler = slog.NewTextHandler(output, opts)
	} else {
		handler = slog.NewJSONHandler(output, opts)
	}

	// Create slog logger
	slogger := slog.New(handler)

	// Add component if specified
	if config.Component != "" {
		slogger = slogger.With("component", config.Component)
	}

	return &Logger{
		slog:   slogger,
		level:  levelVar,
		fields: make(map[string]interface{}),
		config: config,
	}, nil
}

// NewWithHandler creates a Logger that emits through the caller-supplied
// slog.Handler instead of the internally-constructed stdout/stderr/file
// handler. This is the injection seam downstream consumers (e.g.
// go-trusted-platform's logging facade) use to route log records into
// custom sinks — test writers, multi-handler fan-outs, JSON-line stores —
// while keeping the shared Logger surface. The config contributes Level
// (tracked in the returned Logger's LevelVar for SetLevel support) and
// Component; Output/Format/File are ignored since the handler owns
// rendering.
func NewWithHandler(h slog.Handler, config LogConfig) (*Logger, error) {
	if h == nil {
		return nil, &NilHandlerError{}
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	config = config.WithDefaults()

	levelVar := new(slog.LevelVar)
	levelVar.Set(parseLevel(config.Level))

	slogger := slog.New(h)
	if config.Component != "" {
		slogger = slogger.With("component", config.Component)
	}

	return &Logger{
		slog:   slogger,
		level:  levelVar,
		fields: make(map[string]interface{}),
		config: config,
	}, nil
}

// Slog returns the underlying *slog.Logger for interoperation with code
// that consumes the standard library logger directly (third-party
// libraries, slog-native services). Mutating the returned logger does not
// affect this Logger.
func (l *Logger) Slog() *slog.Logger {
	return l.slog
}

// parseLevel converts a string level to slog.Level.
func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "trace":
		return LevelTrace
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	case "fatal":
		return LevelFatal
	case "panic":
		return LevelPanic
	default:
		return slog.LevelInfo
	}
}

// levelToString converts slog.Level to string.
func levelToString(level slog.Level) string {
	switch level {
	case LevelTrace:
		return "trace"
	case slog.LevelDebug:
		return "debug"
	case slog.LevelInfo:
		return "info"
	case slog.LevelWarn:
		return "warn"
	case slog.LevelError:
		return "error"
	case LevelFatal:
		return "fatal"
	case LevelPanic:
		return "panic"
	default:
		return "info"
	}
}

// WithFields creates a new logger with additional fields.
func (l *Logger) WithFields(fields map[string]interface{}) *Logger {
	newFields := make(map[string]interface{}, len(l.fields)+len(fields))
	for k, v := range l.fields {
		newFields[k] = v
	}
	for k, v := range fields {
		newFields[k] = v
	}

	// Convert to slog.Attr slice
	attrs := make([]any, 0, len(fields)*2)
	for k, v := range fields {
		attrs = append(attrs, k, v)
	}

	return &Logger{
		slog:   l.slog.With(attrs...),
		level:  l.level,
		fields: newFields,
		config: l.config,
	}
}

// WithField creates a new logger with a single additional field.
func (l *Logger) WithField(key string, value interface{}) *Logger {
	newFields := make(map[string]interface{}, len(l.fields)+1)
	for k, v := range l.fields {
		newFields[k] = v
	}
	newFields[key] = value

	return &Logger{
		slog:   l.slog.With(key, value),
		level:  l.level,
		fields: newFields,
		config: l.config,
	}
}

// WithError creates a new logger with an error field.
func (l *Logger) WithError(err error) *Logger {
	newFields := make(map[string]interface{}, len(l.fields)+1)
	for k, v := range l.fields {
		newFields[k] = v
	}
	newFields["error"] = err

	return &Logger{
		slog:   l.slog.With("error", err),
		level:  l.level,
		fields: newFields,
		config: l.config,
	}
}

// WithAttrs creates a new logger with strongly-typed slog attributes.
// This is the preferred method for adding context - use slog.String, slog.Int, etc.
func (l *Logger) WithAttrs(attrs ...slog.Attr) *Logger {
	newFields := make(map[string]interface{}, len(l.fields)+len(attrs))
	for k, v := range l.fields {
		newFields[k] = v
	}
	for _, attr := range attrs {
		newFields[attr.Key] = attr.Value.Any()
	}

	// Convert attrs to any slice for slog.With
	args := make([]any, len(attrs))
	for i, attr := range attrs {
		args[i] = attr
	}

	return &Logger{
		slog:   l.slog.With(args...),
		level:  l.level,
		fields: newFields,
		config: l.config,
	}
}

// InfoAttrs logs an info message with strongly-typed attributes.
func (l *Logger) InfoAttrs(msg string, attrs ...slog.Attr) {
	l.slog.LogAttrs(context.Background(), slog.LevelInfo, msg, attrs...)
}

// DebugAttrs logs a debug message with strongly-typed attributes.
func (l *Logger) DebugAttrs(msg string, attrs ...slog.Attr) {
	l.slog.LogAttrs(context.Background(), slog.LevelDebug, msg, attrs...)
}

// WarnAttrs logs a warning message with strongly-typed attributes.
func (l *Logger) WarnAttrs(msg string, attrs ...slog.Attr) {
	l.slog.LogAttrs(context.Background(), slog.LevelWarn, msg, attrs...)
}

// ErrorAttrs logs an error message with strongly-typed attributes.
func (l *Logger) ErrorAttrs(msg string, attrs ...slog.Attr) {
	l.slog.LogAttrs(context.Background(), slog.LevelError, msg, attrs...)
}

// Debug logs a debug message with optional structured key-value pairs.
func (l *Logger) Debug(msg string, args ...any) {
	l.slog.Debug(msg, args...)
}

// Debugf logs a formatted debug message.
func (l *Logger) Debugf(format string, args ...interface{}) {
	l.slog.Debug(fmt.Sprintf(format, args...))
}

// Info logs an info message with optional structured key-value pairs.
func (l *Logger) Info(msg string, args ...any) {
	l.slog.Info(msg, args...)
}

// Infof logs a formatted info message.
func (l *Logger) Infof(format string, args ...interface{}) {
	l.slog.Info(fmt.Sprintf(format, args...))
}

// Warn logs a warning message with optional structured key-value pairs.
func (l *Logger) Warn(msg string, args ...any) {
	l.slog.Warn(msg, args...)
}

// Warnf logs a formatted warning message.
func (l *Logger) Warnf(format string, args ...interface{}) {
	l.slog.Warn(fmt.Sprintf(format, args...))
}

// Error logs an error message with optional structured key-value pairs.
func (l *Logger) Error(msg string, args ...any) {
	l.slog.Error(msg, args...)
}

// Errorf logs a formatted error message.
func (l *Logger) Errorf(format string, args ...interface{}) {
	l.slog.Error(fmt.Sprintf(format, args...))
}

// Fatal logs a fatal message and exits (use sparingly).
func (l *Logger) Fatal(msg string) {
	l.slog.Log(context.Background(), LevelFatal, msg)
	os.Exit(1)
}

// Fatalf logs a formatted fatal message and exits (use sparingly).
func (l *Logger) Fatalf(format string, args ...interface{}) {
	l.slog.Log(context.Background(), LevelFatal, fmt.Sprintf(format, args...))
	os.Exit(1)
}

// Panic logs a panic message and panics (use sparingly, prefer returning errors).
func (l *Logger) Panic(msg string) {
	l.slog.Log(context.Background(), LevelPanic, msg)
	panic(msg)
}

// Panicf logs a formatted panic message and panics (use sparingly, prefer returning errors).
func (l *Logger) Panicf(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	l.slog.Log(context.Background(), LevelPanic, msg)
	panic(msg)
}

// GetLevel returns the current log level.
func (l *Logger) GetLevel() string {
	return levelToString(l.level.Level())
}

// SetLevel sets the log level.
func (l *Logger) SetLevel(level string) error {
	// Validate level
	validLevels := []string{"trace", "debug", "info", "warn", "error", "fatal", "panic"}
	valid := false
	for _, v := range validLevels {
		if strings.ToLower(level) == v {
			valid = true
			break
		}
	}
	if !valid {
		return &InvalidLogLevelError{Level: level}
	}

	l.level.Set(parseLevel(level))
	return nil
}

// GetFields returns a copy of the logger's fields.
func (l *Logger) GetFields() map[string]interface{} {
	fields := make(map[string]interface{}, len(l.fields))
	for k, v := range l.fields {
		fields[k] = v
	}
	return fields
}

// ToContext adds the logger to a context.
func (l *Logger) ToContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, loggerContextKey, l)
}

// FromContext retrieves a logger from context, or returns a default logger.
func FromContext(ctx context.Context) *Logger {
	if ctx == nil {
		return defaultLogger()
	}

	if l, ok := ctx.Value(loggerContextKey).(*Logger); ok {
		return l
	}

	return defaultLogger()
}

// defaultLogger returns a basic default logger.
func defaultLogger() *Logger {
	levelVar := new(slog.LevelVar)
	levelVar.Set(slog.LevelInfo)

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: levelVar,
	})

	return &Logger{
		slog:   slog.New(handler),
		level:  levelVar,
		fields: make(map[string]interface{}),
		config: LogConfig{
			Level:  "info",
			Format: "json",
			Output: "stdout",
		},
	}
}

// Clone creates a copy of the logger.
func (l *Logger) Clone() *Logger {
	fields := make(map[string]interface{}, len(l.fields))
	for k, v := range l.fields {
		fields[k] = v
	}

	return &Logger{
		slog:   l.slog,
		level:  l.level,
		fields: fields,
		config: l.config,
	}
}
