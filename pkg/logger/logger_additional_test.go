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

package logger

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestNew_FileOutputError tests the New function with an invalid file path.
func TestNew_FileOutputError(t *testing.T) {
	// Try to create a logger with a file output in an unwritable directory
	config := LogConfig{
		Level:  "info",
		Format: "json",
		Output: "file",
		File:   "/root/nonexistent/directory/test.log",
	}

	_, err := New(config)
	if err == nil {
		t.Error("New() should return error when file path is not writable")
	}
}

// TestNew_FileOutputSuccess tests the New function with a valid file path.
func TestNew_FileOutputSuccess(t *testing.T) {
	// Create a temporary file path
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "test.log")

	config := LogConfig{
		Level:  "info",
		Format: "json",
		Output: "file",
		File:   logFile,
	}

	logger, err := New(config)
	if err != nil {
		t.Errorf("New() error = %v, want nil", err)
	}
	if logger == nil {
		t.Error("New() returned nil logger")
	}

	// Write a log message to ensure the file is created
	logger.Info("test message")

	// Check if file exists
	if _, err := os.Stat(logFile); os.IsNotExist(err) {
		t.Error("Log file was not created")
	}
}

// TestNew_WithComponent tests the New function with a component name.
func TestNew_WithComponent(t *testing.T) {
	config := LogConfig{
		Level:     "info",
		Format:    "json",
		Output:    "stdout",
		Component: "test-component",
	}

	logger, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}
	if logger == nil {
		t.Fatal("New() returned nil logger")
	}

	// Verify the config is stored
	if logger.config.Component != "test-component" {
		t.Errorf("Component = %v, want test-component", logger.config.Component)
	}
}

// TestLogger_Panic tests the Panic method.
func TestLogger_Panic(t *testing.T) {
	logger, err := New(LogConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
	})
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}

	defer func() {
		if r := recover(); r == nil {
			t.Error("Panic() should have panicked")
		}
	}()

	logger.Panic("panic test message")
}

// TestLogger_Panicf tests the Panicf method.
func TestLogger_Panicf(t *testing.T) {
	logger, err := New(LogConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
	})
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}

	defer func() {
		if r := recover(); r == nil {
			t.Error("Panicf() should have panicked")
		}
	}()

	logger.Panicf("panic test %s %d", "formatted", 123)
}

// subprocessCoverArgs appends -test.gocoverdir to args when GOCOVERDIR is set
// in the environment, so a child re-exec of this test binary has its own
// coverage captured too.
//
// Without this the child's execution is INVISIBLE to `go tool covdata`,
// empirically confirmed rather than assumed: a child that only inherits
// GOCOVERDIR via its environment, with no -test.gocoverdir flag of its own,
// writes no counter file at all — go's own -cover instrumentation needs the
// flag, not just the variable, to know where to write (go-qrdb#114 review,
// round 4). os.Exit inside the child does not change this: go's coverage
// runtime registers an exit hook that still fires, confirmed separately —
// only a signal the child cannot catch (SIGKILL, or a test timeout that
// kills rather than exits) skips it, which is
// pkg/seal/encfs/pebble_sigkill_test.go's documented, unfixable case, not
// this one.
func subprocessCoverArgs(args ...string) []string {
	if dir := os.Getenv("GOCOVERDIR"); dir != "" {
		args = append(args, "-test.gocoverdir="+dir)
	}
	return args
}

// TestLogger_Fatal tests the Fatal method using subprocess pattern.
func TestLogger_Fatal(t *testing.T) {
	if os.Getenv("BE_FATAL") == "1" {
		logger, err := New(LogConfig{
			Level:  "info",
			Format: "json",
			Output: "stdout",
		})
		if err != nil {
			panic(err)
		}
		logger.Fatal("fatal test message")
		return
	}

	// Run the test in a subprocess
	// #nosec G204,G702 -- re-exec of this test binary: argv[0] is os.Args[0], the path `go test` invoked, and every other argument is a compile-time literal. No shell is involved and nothing from outside the process reaches this argv.
	cmd := exec.Command(os.Args[0], subprocessCoverArgs("-test.run=TestLogger_Fatal")...)
	cmd.Env = append(os.Environ(), "BE_FATAL=1")
	err := cmd.Run()

	// Fatal should cause the process to exit with non-zero status
	var e *exec.ExitError
	if errors.As(err, &e) && !e.Success() {
		// Expected behavior - process exited with non-zero status
		return
	}

	t.Error("Fatal() should have caused process to exit with non-zero status")
}

// TestLogger_Fatalf tests the Fatalf method using subprocess pattern.
func TestLogger_Fatalf(t *testing.T) {
	if os.Getenv("BE_FATALF") == "1" {
		logger, err := New(LogConfig{
			Level:  "info",
			Format: "json",
			Output: "stdout",
		})
		if err != nil {
			panic(err)
		}
		logger.Fatalf("fatal test %s %d", "formatted", 456)
		return
	}

	// Run the test in a subprocess
	// #nosec G204,G702 -- re-exec of this test binary: argv[0] is os.Args[0], the path `go test` invoked, and every other argument is a compile-time literal. No shell is involved and nothing from outside the process reaches this argv.
	cmd := exec.Command(os.Args[0], subprocessCoverArgs("-test.run=TestLogger_Fatalf")...)
	cmd.Env = append(os.Environ(), "BE_FATALF=1")
	err := cmd.Run()

	// Fatalf should cause the process to exit with non-zero status
	var e *exec.ExitError
	if errors.As(err, &e) && !e.Success() {
		// Expected behavior - process exited with non-zero status
		return
	}

	t.Error("Fatalf() should have caused process to exit with non-zero status")
}

// TestWithContext_NilContext tests WithContext with a nil context.
func TestWithContext_NilContext(t *testing.T) {
	logger, err := New(LogConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
	})
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}

	// WithContext should create a background context if nil is passed
	ctx := WithContext(nil, logger) //nolint:staticcheck // SA1012: intentionally testing nil-context fallback
	if ctx == nil {
		t.Error("WithContext() should return a valid context even when passed nil")
	}

	// Verify we can retrieve the logger
	retrieved := FromContext(ctx)
	if retrieved == nil {
		t.Error("Should be able to retrieve logger from context created with nil input")
	}
}

// TestWithContext_ValidContext tests WithContext with a valid context.
func TestWithContext_ValidContext(t *testing.T) {
	logger, err := New(LogConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
	})
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}

	// Create a context with some values
	ctx := context.Background()
	type testLogKey struct{}
	ctx = context.WithValue(ctx, testLogKey{}, "test_value")

	// Add logger to context
	ctx = WithContext(ctx, logger)

	// Verify logger is in context
	retrieved := FromContext(ctx)
	if retrieved == nil {
		t.Error("Should be able to retrieve logger from context")
	}

	// Verify original context values are preserved
	if val := ctx.Value(testLogKey{}); val != "test_value" {
		t.Error("WithContext should preserve existing context values")
	}
}

// TestLogger_AllLevels tests logger with all valid log levels.
func TestLogger_AllLevels(t *testing.T) {
	levels := []string{"trace", "debug", "info", "warn", "error"}

	for _, level := range levels {
		t.Run(level, func(t *testing.T) {
			config := LogConfig{
				Level:  level,
				Format: "json",
				Output: "stdout",
			}

			logger, err := New(config)
			if err != nil {
				t.Errorf("New() with level %s error = %v", level, err)
			}
			if logger == nil {
				t.Errorf("New() with level %s returned nil", level)
			}

			// Verify the level was set correctly
			if logger.GetLevel() != level {
				t.Errorf("GetLevel() = %v, want %s", logger.GetLevel(), level)
			}
		})
	}
}

// TestLogger_WithAttrs tests the strongly-typed slog attributes method.
func TestLogger_WithAttrs(t *testing.T) {
	config := LogConfig{
		Level:  "debug",
		Format: "json",
		Output: "stdout",
	}

	logger, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// Test WithAttrs with various slog attribute types
	attrLogger := logger.WithAttrs(
		slog.String("service", "qrdb"),
		slog.Int("port", 8080),
		slog.Bool("debug", true),
	)

	if attrLogger == nil {
		t.Error("WithAttrs() returned nil logger")
	}

	// Verify fields are stored
	fields := attrLogger.GetFields()
	if fields["service"] != "qrdb" {
		t.Errorf("WithAttrs() service = %v, want qrdb", fields["service"])
	}
	if fields["port"] != int64(8080) {
		t.Errorf("WithAttrs() port = %v, want 8080", fields["port"])
	}
	if fields["debug"] != true {
		t.Errorf("WithAttrs() debug = %v, want true", fields["debug"])
	}
}

// TestLogger_InfoAttrs tests info logging with strongly-typed attributes.
func TestLogger_InfoAttrs(t *testing.T) {
	config := LogConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
	}

	logger, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// Should not panic
	logger.InfoAttrs("test message",
		slog.String("key", "value"),
		slog.Int("count", 42),
		slog.Duration("elapsed", time.Second),
	)
}

// TestLogger_DebugAttrs tests debug logging with strongly-typed attributes.
func TestLogger_DebugAttrs(t *testing.T) {
	config := LogConfig{
		Level:  "debug",
		Format: "json",
		Output: "stdout",
	}

	logger, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// Should not panic
	logger.DebugAttrs("debug message",
		slog.String("component", "test"),
		slog.Int64("bytes", 1024),
	)
}

// TestLogger_WarnAttrs tests warning logging with strongly-typed attributes.
func TestLogger_WarnAttrs(t *testing.T) {
	config := LogConfig{
		Level:  "warn",
		Format: "json",
		Output: "stdout",
	}

	logger, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// Should not panic
	logger.WarnAttrs("warning message",
		slog.String("reason", "test"),
		slog.Float64("threshold", 0.95),
	)
}

// TestLogger_ErrorAttrs tests error logging with strongly-typed attributes.
func TestLogger_ErrorAttrs(t *testing.T) {
	config := LogConfig{
		Level:  "error",
		Format: "json",
		Output: "stdout",
	}

	logger, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// Should not panic
	logger.ErrorAttrs("error message",
		slog.String("error_code", "E001"),
		slog.Any("details", map[string]string{"key": "value"}),
	)
}

// TestLogger_WithAttrsChaining tests chaining WithAttrs calls.
func TestLogger_WithAttrsChaining(t *testing.T) {
	config := LogConfig{
		Level:  "debug",
		Format: "json",
		Output: "stdout",
	}

	logger, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// Chain multiple WithAttrs calls
	chainedLogger := logger.
		WithAttrs(slog.String("service", "qrdb")).
		WithAttrs(slog.Int("version", 1)).
		WithAttrs(slog.Bool("production", false))

	fields := chainedLogger.GetFields()
	if fields["service"] != "qrdb" {
		t.Errorf("chained WithAttrs() service = %v, want qrdb", fields["service"])
	}
	if fields["version"] != int64(1) {
		t.Errorf("chained WithAttrs() version = %v, want 1", fields["version"])
	}
	if fields["production"] != false {
		t.Errorf("chained WithAttrs() production = %v, want false", fields["production"])
	}
}

// TestLogger_WithAttrsEmpty tests WithAttrs with no attributes.
func TestLogger_WithAttrsEmpty(t *testing.T) {
	config := LogConfig{
		Level:  "debug",
		Format: "json",
		Output: "stdout",
	}

	logger, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// Call WithAttrs with no attributes
	emptyLogger := logger.WithAttrs()
	if emptyLogger == nil {
		t.Error("WithAttrs() with no args returned nil")
	}

	// Should still work
	emptyLogger.Info("message without attrs")
}

// TestNewAdapter_NilLogger tests NewAdapter with nil logger.
func TestNewAdapter_NilLogger(t *testing.T) {
	adapter := NewAdapter(nil)
	if adapter != nil {
		t.Error("NewAdapter(nil) should return nil")
	}
}

// TestLoggerAdapter_WithField tests the WithField method of LoggerAdapter.
func TestLoggerAdapter_WithField(t *testing.T) {
	logger, err := New(LogConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	adapter := NewAdapter(logger)
	result := adapter.WithField("key", "value")

	if result == nil {
		t.Error("WithField() returned nil")
	}

	// Verify it's still a LoggerAdapter
	resultAdapter, ok := result.(*LoggerAdapter)
	if !ok {
		t.Error("WithField() should return *LoggerAdapter")
	}

	// Verify the field was added
	fields := resultAdapter.GetFields()
	if fields["key"] != "value" {
		t.Errorf("WithField() key = %v, want 'value'", fields["key"])
	}
}

// TestLoggerAdapter_WithFields tests the WithFields method of LoggerAdapter.
func TestLoggerAdapter_WithFields(t *testing.T) {
	logger, err := New(LogConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	adapter := NewAdapter(logger)
	fields := map[string]interface{}{
		"key1": "value1",
		"key2": 123,
		"key3": true,
	}
	result := adapter.WithFields(fields)

	if result == nil {
		t.Error("WithFields() returned nil")
	}

	// Verify it's still a LoggerAdapter
	resultAdapter, ok := result.(*LoggerAdapter)
	if !ok {
		t.Error("WithFields() should return *LoggerAdapter")
	}

	// Verify fields were added
	resultFields := resultAdapter.GetFields()
	if resultFields["key1"] != "value1" {
		t.Errorf("WithFields() key1 = %v, want 'value1'", resultFields["key1"])
	}
}

// TestLoggerAdapter_Chaining tests method chaining on LoggerAdapter.
func TestLoggerAdapter_Chaining(t *testing.T) {
	logger, err := New(LogConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
	})
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}

	adapter := NewAdapter(logger)
	result := adapter.
		WithField("key1", "value1").
		WithField("key2", "value2").
		WithError(errors.New("test error"))

	if result == nil {
		t.Error("Chained operations returned nil")
	}

	// Verify it's still a LoggerAdapter
	resultAdapter, ok := result.(*LoggerAdapter)
	if !ok {
		t.Error("Chained operations should return *LoggerAdapter")
	}

	// Verify fields are present
	fields := resultAdapter.GetFields()
	if fields["key1"] != "value1" {
		t.Errorf("Chained key1 = %v, want 'value1'", fields["key1"])
	}
	if fields["key2"] != "value2" {
		t.Errorf("Chained key2 = %v, want 'value2'", fields["key2"])
	}
}
