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
	"testing"
)

func TestNew(t *testing.T) {
	tests := []struct {
		name    string
		config  LogConfig
		wantErr bool
	}{
		{
			name: "valid json stdout config",
			config: LogConfig{
				Level:  "info",
				Format: "json",
				Output: "stdout",
			},
			wantErr: false,
		},
		{
			name: "valid console stderr config",
			config: LogConfig{
				Level:  "debug",
				Format: "console",
				Output: "stderr",
			},
			wantErr: false,
		},
		{
			name: "invalid level",
			config: LogConfig{
				Level:  "invalid",
				Format: "json",
				Output: "stdout",
			},
			wantErr: true,
		},
		{
			name: "invalid format",
			config: LogConfig{
				Level:  "info",
				Format: "invalid",
				Output: "stdout",
			},
			wantErr: true,
		},
		{
			name: "file output without file path",
			config: LogConfig{
				Level:  "info",
				Format: "json",
				Output: "file",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, err := New(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("New() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && logger == nil {
				t.Error("New() returned nil logger without error")
			}
		})
	}
}

func TestLogger_WithFields(t *testing.T) {
	logger, err := New(LogConfig{
		Level:  "debug",
		Format: "json",
		Output: "stdout",
	})
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}

	fields := map[string]interface{}{
		"user_id":    123,
		"request_id": "req-456",
	}

	newLogger := logger.WithFields(fields)

	// Verify original logger is unchanged
	if len(logger.fields) != 0 {
		t.Error("Original logger should not be modified")
	}

	// Verify new logger has fields
	if len(newLogger.fields) != len(fields) {
		t.Errorf("New logger has %d fields, want %d", len(newLogger.fields), len(fields))
	}

	for k, v := range fields {
		if newLogger.fields[k] != v {
			t.Errorf("Field %s = %v, want %v", k, newLogger.fields[k], v)
		}
	}
}

func TestLogger_WithField(t *testing.T) {
	logger, err := New(LogConfig{
		Level:  "debug",
		Format: "json",
		Output: "stdout",
	})
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}

	newLogger := logger.WithField("key", "value")

	if val, ok := newLogger.fields["key"]; !ok || val != "value" {
		t.Errorf("Field key = %v, want value", val)
	}
}

func TestLogger_WithError(t *testing.T) {
	logger, err := New(LogConfig{
		Level:  "debug",
		Format: "json",
		Output: "stdout",
	})
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}

	testErr := errors.New("test error")
	newLogger := logger.WithError(testErr)

	if newLogger == nil {
		t.Error("WithError() returned nil logger")
	}
}

func TestLogger_SetLevel(t *testing.T) {
	logger, err := New(LogConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
	})
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}

	if err := logger.SetLevel("debug"); err != nil {
		t.Errorf("SetLevel() error = %v", err)
	}

	if logger.GetLevel() != "debug" {
		t.Errorf("GetLevel() = %v, want debug", logger.GetLevel())
	}

	if err := logger.SetLevel("invalid"); err == nil {
		t.Error("SetLevel() with invalid level should return error")
	}
}

func TestLogger_GetFields(t *testing.T) {
	logger, err := New(LogConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
	})
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}

	fields := map[string]interface{}{
		"key1": "value1",
		"key2": 123,
	}

	newLogger := logger.WithFields(fields)
	gotFields := newLogger.GetFields()

	if len(gotFields) != len(fields) {
		t.Errorf("GetFields() returned %d fields, want %d", len(gotFields), len(fields))
	}

	for k, v := range fields {
		if gotFields[k] != v {
			t.Errorf("Field %s = %v, want %v", k, gotFields[k], v)
		}
	}

	// Verify returned map is a copy
	gotFields["key3"] = "value3"
	if len(newLogger.GetFields()) != len(fields) {
		t.Error("GetFields() should return a copy, not the original map")
	}
}

func TestLogger_Clone(t *testing.T) {
	logger, err := New(LogConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
	})
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}

	logger = logger.WithField("key", "value")
	cloned := logger.Clone()

	if len(cloned.fields) != len(logger.fields) {
		t.Error("Cloned logger should have same fields")
	}

	// Verify it's a copy
	cloned.fields["new_key"] = "new_value"
	if _, ok := logger.fields["new_key"]; ok {
		t.Error("Modifying clone should not affect original")
	}
}

func TestLogger_ToContext(t *testing.T) {
	logger, err := New(LogConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
	})
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}

	ctx := logger.ToContext(context.Background())
	retrieved := FromContext(ctx)

	if retrieved == nil {
		t.Error("FromContext() returned nil")
	}
}

func TestFromContext(t *testing.T) {
	t.Run("with logger in context", func(t *testing.T) {
		logger, err := New(LogConfig{
			Level:  "info",
			Format: "json",
			Output: "stdout",
		})
		if err != nil {
			t.Fatalf("Failed to create logger: %v", err)
		}

		ctx := logger.ToContext(context.Background())
		retrieved := FromContext(ctx)

		if retrieved == nil {
			t.Error("FromContext() returned nil")
		}
	})

	t.Run("without logger in context", func(t *testing.T) {
		ctx := context.Background()
		retrieved := FromContext(ctx)

		if retrieved == nil {
			t.Error("FromContext() should return default logger, not nil")
		}
	})

	t.Run("with nil context", func(t *testing.T) {
		retrieved := FromContext(nil) //nolint:staticcheck // SA1012: intentionally testing nil-context fallback

		if retrieved == nil {
			t.Error("FromContext() should return default logger, not nil")
		}
	})
}

func TestLogConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  LogConfig
		wantErr bool
	}{
		{
			name: "valid config",
			config: LogConfig{
				Level:  "info",
				Format: "json",
				Output: "stdout",
			},
			wantErr: false,
		},
		{
			name: "invalid level",
			config: LogConfig{
				Level:  "invalid",
				Format: "json",
				Output: "stdout",
			},
			wantErr: true,
		},
		{
			name: "invalid format",
			config: LogConfig{
				Level:  "info",
				Format: "invalid",
				Output: "stdout",
			},
			wantErr: true,
		},
		{
			name: "invalid output",
			config: LogConfig{
				Level:  "info",
				Format: "json",
				Output: "invalid",
			},
			wantErr: true,
		},
		{
			name: "file output without path",
			config: LogConfig{
				Level:  "info",
				Format: "json",
				Output: "file",
			},
			wantErr: true,
		},
		{
			name: "negative max size",
			config: LogConfig{
				Level:   "info",
				Format:  "json",
				Output:  "stdout",
				MaxSize: -1,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("LogConfig.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLogConfig_WithDefaults(t *testing.T) {
	config := LogConfig{}
	config = config.WithDefaults()

	if config.Level != "info" {
		t.Errorf("Default level = %v, want info", config.Level)
	}
	if config.Format != "json" {
		t.Errorf("Default format = %v, want json", config.Format)
	}
	if config.Output != "stdout" {
		t.Errorf("Default output = %v, want stdout", config.Output)
	}
	if config.MaxSize == 0 {
		t.Error("Default max_size should be set")
	}
}

func TestLogConfig_Clone(t *testing.T) {
	original := LogConfig{
		Level:  "debug",
		Format: "console",
		Output: "stderr",
		File:   "test.log",
	}

	cloned := original.Clone()

	if cloned.Level != original.Level {
		t.Error("Cloned config should match original")
	}

	// Modify clone
	cloned.Level = "info"

	if original.Level == cloned.Level {
		t.Error("Modifying clone should not affect original")
	}
}

func TestDefaultLogConfig(t *testing.T) {
	config := Default()

	if config.Level == "" {
		t.Error("Default config should have level set")
	}

	if config.Format == "" {
		t.Error("Default config should have format set")
	}

	if config.Output == "" {
		t.Error("Default config should have output set")
	}

	// Validate default config
	if err := config.Validate(); err != nil {
		t.Errorf("Default config should be valid: %v", err)
	}
}

func TestContextHelpers(t *testing.T) {
	logger, err := New(LogConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
	})
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}

	ctx := WithContext(context.Background(), logger)

	// Test WithFieldsContext
	ctx = WithFieldsContext(ctx, map[string]interface{}{
		"key1": "value1",
	})

	retrieved := FromContext(ctx)
	if len(retrieved.fields) == 0 {
		t.Error("WithFieldsContext should add fields to logger")
	}

	// Test WithFieldContext
	ctx = WithFieldContext(ctx, "key2", "value2")
	retrieved = FromContext(ctx)
	if len(retrieved.fields) < 2 {
		t.Error("WithFieldContext should add field to logger")
	}

	// Test WithErrorContext
	testErr := errors.New("test error")
	ctx = WithErrorContext(ctx, testErr)
	retrieved = FromContext(ctx)
	if retrieved == nil {
		t.Error("WithErrorContext should return valid context")
	}
}

func TestLogger_LogMethods(t *testing.T) {
	// Create logger - just ensure methods don't panic
	logger, err := New(LogConfig{
		Level:  "debug",
		Format: "json",
		Output: "stdout",
	})
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}

	// These should not panic
	logger.Debug("debug message")
	logger.Debugf("debug %s", "formatted")
	logger.Info("info message")
	logger.Infof("info %d", 123)
	logger.Warn("warn message")
	logger.Warnf("warn %v", true)
	logger.Error("error message")
	logger.Errorf("error %s %d", "test", 456)

	// Test context logging functions
	ctx := logger.ToContext(context.Background())
	DebugContext(ctx, "debug context")
	DebugfContext(ctx, "debug %s", "context formatted")
	InfoContext(ctx, "info context")
	InfofContext(ctx, "info %d", 123)
	WarnContext(ctx, "warn context")
	WarnfContext(ctx, "warn %v", true)
	ErrorContext(ctx, "error context")
	ErrorfContext(ctx, "error %s %d", "test", 456)
}
