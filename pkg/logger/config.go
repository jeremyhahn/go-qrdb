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
	"strings"
)

// LogConfig contains configuration for the logger.
type LogConfig struct {
	// Level is the minimum log level (trace, debug, info, warn, error, fatal, panic)
	Level string `mapstructure:"level"`

	// Format is the log format (json or console)
	Format string `mapstructure:"format"`

	// Output is the output destination (stdout, stderr, file)
	Output string `mapstructure:"output"`

	// File is the log file path (used when Output is "file")
	File string `mapstructure:"file"`

	// Component is an optional component name to include in all log entries
	Component string `mapstructure:"component"`

	// NoColor disables color output for console format
	NoColor bool `mapstructure:"no_color"`

	// MaxSize is the maximum size in megabytes before log rotation
	MaxSize int `mapstructure:"max_size"`

	// MaxBackups is the maximum number of old log files to retain
	MaxBackups int `mapstructure:"max_backups"`

	// MaxAge is the maximum number of days to retain old log files
	MaxAge int `mapstructure:"max_age"`

	// Compress determines if rotated logs should be compressed
	Compress bool `mapstructure:"compress"`
}

// InvalidLogFormatError is returned when an unsupported log format is specified.
type InvalidLogFormatError struct {
	Format string
}

// Error implements the error interface.
func (e *InvalidLogFormatError) Error() string {
	return "invalid log format: " + e.Format + " (must be json or console)"
}

// InvalidLogOutputError is returned when an unsupported log output is specified.
type InvalidLogOutputError struct {
	Output string
}

// Error implements the error interface.
func (e *InvalidLogOutputError) Error() string {
	return "invalid log output: " + e.Output + " (must be stdout, stderr, or file)"
}

// LogFilePathRequiredError is returned when output is set to file but no path is provided.
type LogFilePathRequiredError struct{}

// Error implements the error interface.
func (e *LogFilePathRequiredError) Error() string {
	return "log file path required when output is file"
}

// InvalidLogRotationError is returned when a log rotation setting is invalid.
type InvalidLogRotationError struct {
	Field string
}

// Error implements the error interface.
func (e *InvalidLogRotationError) Error() string {
	return e.Field + " must be non-negative"
}

// Validate validates the log configuration.
func (c LogConfig) Validate() error {
	// Validate level
	validLevels := []string{"trace", "debug", "info", "warn", "error", "fatal", "panic"}
	if c.Level != "" {
		level := strings.ToLower(c.Level)
		valid := false
		for _, l := range validLevels {
			if level == l {
				valid = true
				break
			}
		}
		if !valid {
			return &InvalidLogLevelError{Level: c.Level}
		}
	}

	// Validate format
	if c.Format != "" && c.Format != "json" && c.Format != "console" {
		return &InvalidLogFormatError{Format: c.Format}
	}

	// Validate output
	if c.Output != "" && c.Output != "stdout" && c.Output != "stderr" && c.Output != "file" {
		return &InvalidLogOutputError{Output: c.Output}
	}

	// Validate file path when output is file
	if c.Output == "file" && c.File == "" {
		return &LogFilePathRequiredError{}
	}

	// Validate rotation settings
	if c.MaxSize < 0 {
		return &InvalidLogRotationError{Field: "max_size"}
	}
	if c.MaxBackups < 0 {
		return &InvalidLogRotationError{Field: "max_backups"}
	}
	if c.MaxAge < 0 {
		return &InvalidLogRotationError{Field: "max_age"}
	}

	return nil
}

// WithDefaults returns a copy of the config with defaults applied.
func (c LogConfig) WithDefaults() LogConfig {
	config := c

	if config.Level == "" {
		config.Level = "info"
	} else {
		config.Level = strings.ToLower(config.Level)
	}

	if config.Format == "" {
		config.Format = "json"
	}

	if config.Output == "" {
		config.Output = "stdout"
	}

	if config.MaxSize == 0 {
		config.MaxSize = 100 // 100MB
	}

	if config.MaxBackups == 0 {
		config.MaxBackups = 3
	}

	if config.MaxAge == 0 {
		config.MaxAge = 28 // 28 days
	}

	return config
}

// Default returns a default LogConfig.
func Default() LogConfig {
	return LogConfig{
		Level:      "info",
		Format:     "json",
		Output:     "stdout",
		NoColor:    false,
		MaxSize:    100,
		MaxBackups: 3,
		MaxAge:     28,
		Compress:   true,
	}
}

// Clone creates a copy of the config.
func (c LogConfig) Clone() LogConfig {
	return LogConfig{
		Level:      c.Level,
		Format:     c.Format,
		Output:     c.Output,
		File:       c.File,
		Component:  c.Component,
		NoColor:    c.NoColor,
		MaxSize:    c.MaxSize,
		MaxBackups: c.MaxBackups,
		MaxAge:     c.MaxAge,
		Compress:   c.Compress,
	}
}
