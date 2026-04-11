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
)

// WithContext creates a new context with the logger attached.
func WithContext(ctx context.Context, logger *Logger) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return logger.ToContext(ctx)
}

// DebugContext logs a debug message using the logger from context.
func DebugContext(ctx context.Context, msg string) {
	FromContext(ctx).Debug(msg)
}

// DebugfContext logs a formatted debug message using the logger from context.
func DebugfContext(ctx context.Context, format string, args ...interface{}) {
	FromContext(ctx).Debugf(format, args...)
}

// InfoContext logs an info message using the logger from context.
func InfoContext(ctx context.Context, msg string) {
	FromContext(ctx).Info(msg)
}

// InfofContext logs a formatted info message using the logger from context.
func InfofContext(ctx context.Context, format string, args ...interface{}) {
	FromContext(ctx).Infof(format, args...)
}

// WarnContext logs a warning message using the logger from context.
func WarnContext(ctx context.Context, msg string) {
	FromContext(ctx).Warn(msg)
}

// WarnfContext logs a formatted warning message using the logger from context.
func WarnfContext(ctx context.Context, format string, args ...interface{}) {
	FromContext(ctx).Warnf(format, args...)
}

// ErrorContext logs an error message using the logger from context.
func ErrorContext(ctx context.Context, msg string) {
	FromContext(ctx).Error(msg)
}

// ErrorfContext logs a formatted error message using the logger from context.
func ErrorfContext(ctx context.Context, format string, args ...interface{}) {
	FromContext(ctx).Errorf(format, args...)
}

// WithFieldsContext adds fields to the logger in context and returns a new context.
func WithFieldsContext(ctx context.Context, fields map[string]interface{}) context.Context {
	logger := FromContext(ctx).WithFields(fields)
	return WithContext(ctx, logger)
}

// WithFieldContext adds a field to the logger in context and returns a new context.
func WithFieldContext(ctx context.Context, key string, value interface{}) context.Context {
	logger := FromContext(ctx).WithField(key, value)
	return WithContext(ctx, logger)
}

// WithErrorContext adds an error to the logger in context and returns a new context.
func WithErrorContext(ctx context.Context, err error) context.Context {
	logger := FromContext(ctx).WithError(err)
	return WithContext(ctx, logger)
}
