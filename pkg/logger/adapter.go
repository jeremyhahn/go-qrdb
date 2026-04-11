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

// Package logger provides the LoggerAdapter that implements interfaces.Logger.
package logger

import (
	"github.com/jeremyhahn/go-qrdb/pkg/interfaces"
)

// LoggerAdapter wraps *Logger to implement interfaces.Logger.
// This adapter is necessary because Go's interface satisfaction requires
// exact method signature matches, including return types.
type LoggerAdapter struct {
	*Logger
}

// NewAdapter creates a new LoggerAdapter from a Logger.
func NewAdapter(l *Logger) interfaces.Logger {
	if l == nil {
		return nil
	}
	return &LoggerAdapter{Logger: l}
}

// WithField creates a new logger with an additional field.
func (a *LoggerAdapter) WithField(key string, value interface{}) interfaces.Logger {
	return &LoggerAdapter{Logger: a.Logger.WithField(key, value)}
}

// WithFields creates a new logger with multiple additional fields.
func (a *LoggerAdapter) WithFields(fields map[string]interface{}) interfaces.Logger {
	return &LoggerAdapter{Logger: a.Logger.WithFields(fields)}
}

// WithError creates a new logger with an error field.
func (a *LoggerAdapter) WithError(err error) interfaces.Logger {
	return &LoggerAdapter{Logger: a.Logger.WithError(err)}
}

// Compile-time check that LoggerAdapter implements interfaces.Logger.
var _ interfaces.Logger = (*LoggerAdapter)(nil)
