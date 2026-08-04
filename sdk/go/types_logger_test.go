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
	"context"
	"testing"
)

// TestLoggerContextRoundTrip verifies that LoggerFromContext returns the
// exact logger attached by ContextWithLogger.
func TestLoggerContextRoundTrip(t *testing.T) {
	log, err := NewLogger(DefaultLogConfig())
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}

	ctx := ContextWithLogger(context.Background(), log)

	got := LoggerFromContext(ctx)
	if got != log {
		t.Errorf("LoggerFromContext = %p, want the attached logger %p", got, log)
	}
}

// TestContextWithLoggerNilContext verifies that a nil context is treated as
// context.Background() rather than panicking.
func TestContextWithLoggerNilContext(t *testing.T) {
	log, err := NewLogger(DefaultLogConfig())
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}

	//nolint:staticcheck // intentionally passing nil to exercise the guard
	ctx := ContextWithLogger(nil, log)
	if ctx == nil {
		t.Fatal("ContextWithLogger(nil, log) returned nil context")
	}
	if got := LoggerFromContext(ctx); got != log {
		t.Errorf("LoggerFromContext = %p, want the attached logger %p", got, log)
	}
}

// TestLoggerFromContextMissing verifies that a context without a logger
// yields a usable default logger instead of nil.
func TestLoggerFromContextMissing(t *testing.T) {
	got := LoggerFromContext(context.Background())
	if got == nil {
		t.Fatal("LoggerFromContext on unbound context returned nil, want default logger")
	}
}
