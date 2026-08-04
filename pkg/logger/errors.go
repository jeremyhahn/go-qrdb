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

// InvalidLogLevelError is returned when an unsupported log level is specified.
// NilHandlerError is returned by NewWithHandler when the supplied
// slog.Handler is nil.
type NilHandlerError struct{}

// Error implements the error interface.
func (e *NilHandlerError) Error() string {
	return "logger: handler must not be nil"
}

type InvalidLogLevelError struct {
	Level string
}

// Error implements the error interface.
func (e *InvalidLogLevelError) Error() string {
	return "invalid log level: " + e.Level
}
