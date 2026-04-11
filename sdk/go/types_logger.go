// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sdk

import (
	"log/slog"

	"github.com/jeremyhahn/go-qrdb/pkg/logger"
)

// LogConfig holds configuration for the structured logger.
type LogConfig = logger.LogConfig

// ConcreteLogger is the concrete structured logger implementation.
// Use this when a concrete type (not the Logger interface) is required.
type ConcreteLogger = logger.Logger

// InvalidLogLevelError is returned when an unsupported log level is specified.
type InvalidLogLevelError = logger.InvalidLogLevelError

// NewLogger creates a new Logger with the given configuration.
var NewLogger = logger.New

// NewLoggerAdapter wraps a concrete *logger.Logger as an interfaces.Logger.
var NewLoggerAdapter = logger.NewAdapter

// DefaultLogConfig returns a LogConfig with sensible defaults applied.
var DefaultLogConfig = logger.Default

// LoggerFromContext retrieves the logger stored in a context.
// If no logger is found, a default logger is returned.
var LoggerFromContext = logger.FromContext

// Log level constants for use outside the logger package.
const (
	LevelTrace = slog.Level(-8)
	LevelFatal = slog.Level(12)
	LevelPanic = slog.Level(16)
)
