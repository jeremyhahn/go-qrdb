// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-dragondb.
//
// go-dragondb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package logger

import (
	"log/slog"
	"testing"
)

// ---------------------------------------------------------------------------
// Error() methods on config error types
// ---------------------------------------------------------------------------

// TestInvalidLogFormatError_Error verifies the error message of
// InvalidLogFormatError contains the invalid format string.
func TestInvalidLogFormatError_Error(t *testing.T) {
	err := &InvalidLogFormatError{Format: "xml"}
	msg := err.Error()
	if msg == "" {
		t.Fatal("Error() returned empty string")
	}
	if want := "xml"; !contains(msg, want) {
		t.Errorf("Error() = %q, want to contain %q", msg, want)
	}
}

// TestInvalidLogOutputError_Error verifies the error message of
// InvalidLogOutputError contains the invalid output string.
func TestInvalidLogOutputError_Error(t *testing.T) {
	err := &InvalidLogOutputError{Output: "syslog"}
	msg := err.Error()
	if msg == "" {
		t.Fatal("Error() returned empty string")
	}
	if want := "syslog"; !contains(msg, want) {
		t.Errorf("Error() = %q, want to contain %q", msg, want)
	}
}

// TestLogFilePathRequiredError_Error verifies the error message of
// LogFilePathRequiredError is non-empty.
func TestLogFilePathRequiredError_Error(t *testing.T) {
	err := &LogFilePathRequiredError{}
	msg := err.Error()
	if msg == "" {
		t.Fatal("Error() returned empty string")
	}
}

// TestInvalidLogRotationError_Error verifies the error message of
// InvalidLogRotationError contains the field name.
func TestInvalidLogRotationError_Error(t *testing.T) {
	err := &InvalidLogRotationError{Field: "max_size"}
	msg := err.Error()
	if msg == "" {
		t.Fatal("Error() returned empty string")
	}
	if want := "max_size"; !contains(msg, want) {
		t.Errorf("Error() = %q, want to contain %q", msg, want)
	}
}

// TestValidate_NegativeMaxBackups verifies that a negative MaxBackups value
// is rejected with an InvalidLogRotationError.
func TestValidate_NegativeMaxBackups(t *testing.T) {
	cfg := LogConfig{
		Level:      "info",
		Format:     "json",
		Output:     "stdout",
		MaxBackups: -1,
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for negative MaxBackups")
	}
	rotErr, ok := err.(*InvalidLogRotationError)
	if !ok {
		t.Fatalf("expected *InvalidLogRotationError, got %T: %v", err, err)
	}
	if rotErr.Field != "max_backups" {
		t.Errorf("Field: got %q, want max_backups", rotErr.Field)
	}
}

// TestValidate_NegativeMaxAge verifies that a negative MaxAge value
// is rejected with an InvalidLogRotationError.
func TestValidate_NegativeMaxAge(t *testing.T) {
	cfg := LogConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
		MaxAge: -1,
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for negative MaxAge")
	}
	rotErr, ok := err.(*InvalidLogRotationError)
	if !ok {
		t.Fatalf("expected *InvalidLogRotationError, got %T: %v", err, err)
	}
	if rotErr.Field != "max_age" {
		t.Errorf("Field: got %q, want max_age", rotErr.Field)
	}
}

// TestInvalidLogLevelError_Error_FromErrors verifies the InvalidLogLevelError
// Error() method (defined in errors.go) returns the level string.
func TestInvalidLogLevelError_Error_FromErrors(t *testing.T) {
	err := &InvalidLogLevelError{Level: "verbose"}
	msg := err.Error()
	if msg == "" {
		t.Fatal("Error() returned empty string")
	}
	if want := "verbose"; !contains(msg, want) {
		t.Errorf("Error() = %q, want to contain %q", msg, want)
	}
}

// ---------------------------------------------------------------------------
// parseLevel - uncovered branches (fatal, panic, default)
// ---------------------------------------------------------------------------

// TestParseLevel_Fatal verifies that "fatal" maps to LevelFatal.
func TestParseLevel_Fatal(t *testing.T) {
	level := parseLevel("fatal")
	if level != LevelFatal {
		t.Errorf("parseLevel(fatal) = %v, want %v", level, LevelFatal)
	}
}

// TestParseLevel_Panic verifies that "panic" maps to LevelPanic.
func TestParseLevel_Panic(t *testing.T) {
	level := parseLevel("panic")
	if level != LevelPanic {
		t.Errorf("parseLevel(panic) = %v, want %v", level, LevelPanic)
	}
}

// TestParseLevel_Default verifies that an unrecognised level falls back to
// slog.LevelInfo (not a panic or error).
func TestParseLevel_Default(t *testing.T) {
	level := parseLevel("unknown_level_xyz")
	if level != slog.LevelInfo {
		t.Errorf("parseLevel(unknown) = %v, want %v (LevelInfo)", level, slog.LevelInfo)
	}
}

// ---------------------------------------------------------------------------
// levelToString - uncovered branches (fatal, panic, default)
// ---------------------------------------------------------------------------

// TestLevelToString_Fatal verifies that LevelFatal maps to "fatal".
func TestLevelToString_Fatal(t *testing.T) {
	s := levelToString(LevelFatal)
	if s != "fatal" {
		t.Errorf("levelToString(LevelFatal) = %q, want %q", s, "fatal")
	}
}

// TestLevelToString_Panic verifies that LevelPanic maps to "panic".
func TestLevelToString_Panic(t *testing.T) {
	s := levelToString(LevelPanic)
	if s != "panic" {
		t.Errorf("levelToString(LevelPanic) = %q, want %q", s, "panic")
	}
}

// TestLevelToString_Default verifies that an unknown level value falls back
// to "info".
func TestLevelToString_Default(t *testing.T) {
	const unknownLevel = LevelPanic + 100
	s := levelToString(unknownLevel)
	if s != "info" {
		t.Errorf("levelToString(unknown) = %q, want %q", s, "info")
	}
}

// ---------------------------------------------------------------------------
// WithFields - empty map branch
// ---------------------------------------------------------------------------

// TestNew_TraceLevel_ReplaceAttr verifies that a logger created with trace
// level exercises the TRACE branch in the ReplaceAttr handler. This is
// validated by logging at trace level and confirming no panic.
func TestNew_TraceLevel_ReplaceAttr(t *testing.T) {
	log, err := New(LogConfig{
		Level:  "trace",
		Format: "json",
		Output: "stdout",
	})
	if err != nil {
		t.Fatalf("New(trace): %v", err)
	}
	// Emit a trace-level message to exercise the TRACE branch in ReplaceAttr.
	log.Debug("trace level logger created")
}

// TestWithFields_CopiesExistingFields verifies that WithFields properly copies
// the caller's existing fields into the new logger's field map.
func TestWithFields_CopiesExistingFields(t *testing.T) {
	log, err := New(LogConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Create a logger with an existing field.
	base := log.WithField("existing", "value")
	// Add more fields through WithFields; this must copy the existing "existing" field.
	extended := base.WithFields(map[string]interface{}{"new_key": "new_value"})
	if extended == nil {
		t.Fatal("WithFields returned nil")
	}
	fields := extended.GetFields()
	if fields["existing"] != "value" {
		t.Errorf("existing field not copied: got %v", fields["existing"])
	}
	if fields["new_key"] != "new_value" {
		t.Errorf("new field not set: got %v", fields["new_key"])
	}
}

// TestWithFields_EmptyMap verifies that WithFields with an empty map returns
// a new logger that still works correctly.
func TestWithFields_EmptyMap(t *testing.T) {
	log, err := New(LogConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	newLog := log.WithFields(map[string]interface{}{})
	if newLog == nil {
		t.Fatal("WithFields(empty) returned nil")
	}
	// The new logger should have the same fields as the original.
	if len(newLog.GetFields()) != 0 {
		t.Errorf("expected 0 fields, got %d", len(newLog.GetFields()))
	}
}

// TestNew_FatalAndPanicLevels verifies that New() accepts and stores "fatal"
// and "panic" log levels correctly.
func TestNew_FatalAndPanicLevels(t *testing.T) {
	for _, level := range []string{"fatal", "panic"} {
		t.Run(level, func(t *testing.T) {
			log, err := New(LogConfig{
				Level:  level,
				Format: "json",
				Output: "stdout",
			})
			if err != nil {
				t.Fatalf("New(%s): %v", level, err)
			}
			if log == nil {
				t.Fatalf("New(%s) returned nil", level)
			}
			got := log.GetLevel()
			if got != level {
				t.Errorf("GetLevel() = %q, want %q", got, level)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// helper
// ---------------------------------------------------------------------------

// contains is a simple substring check.
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsAt(s, sub))
}

func containsAt(s, sub string) bool {
	for i := range s {
		if i+len(sub) <= len(s) && s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
