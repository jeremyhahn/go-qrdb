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

package interfaces

// Logger defines the interface for structured logging.
// It provides leveled logging with field support for contextual information.
type Logger interface {
	// Leveled logging methods

	// Debug logs a debug-level message with optional structured key-value pairs.
	Debug(msg string, args ...any)

	// Debugf logs a formatted debug-level message.
	Debugf(format string, args ...interface{})

	// Info logs an info-level message with optional structured key-value pairs.
	Info(msg string, args ...any)

	// Infof logs a formatted info-level message.
	Infof(format string, args ...interface{})

	// Warn logs a warning-level message with optional structured key-value pairs.
	Warn(msg string, args ...any)

	// Warnf logs a formatted warning-level message.
	Warnf(format string, args ...interface{})

	// Error logs an error-level message with optional structured key-value pairs.
	Error(msg string, args ...any)

	// Errorf logs a formatted error-level message.
	Errorf(format string, args ...interface{})

	// Fatal logs a fatal-level message and exits the application.
	Fatal(msg string)

	// Fatalf logs a formatted fatal-level message and exits the application.
	Fatalf(format string, args ...interface{})

	// Structured logging with fields

	// WithField creates a new logger with an additional field.
	// Returns a new logger instance; the original is unchanged.
	WithField(key string, value interface{}) Logger

	// WithFields creates a new logger with multiple additional fields.
	// Returns a new logger instance; the original is unchanged.
	WithFields(fields map[string]interface{}) Logger

	// WithError creates a new logger with an error field.
	// Returns a new logger instance; the original is unchanged.
	WithError(err error) Logger
}

// LogLevel represents the logging level.
type LogLevel int

const (
	// LogLevelDebug enables all log messages.
	LogLevelDebug LogLevel = iota

	// LogLevelInfo enables info, warn, error, and fatal messages.
	LogLevelInfo

	// LogLevelWarn enables warn, error, and fatal messages.
	LogLevelWarn

	// LogLevelError enables error and fatal messages.
	LogLevelError

	// LogLevelFatal enables only fatal messages.
	LogLevelFatal
)

// String returns the string representation of the log level.
func (l LogLevel) String() string {
	switch l {
	case LogLevelDebug:
		return "debug"
	case LogLevelInfo:
		return "info"
	case LogLevelWarn:
		return "warn"
	case LogLevelError:
		return "error"
	case LogLevelFatal:
		return "fatal"
	default:
		return "unknown"
	}
}
