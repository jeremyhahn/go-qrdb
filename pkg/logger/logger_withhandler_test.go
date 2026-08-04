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

package logger

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// TestNewWithHandler_RoutesThroughInjectedHandler proves the injection seam:
// records emitted via the Logger land in the caller-supplied handler's
// writer, and Component is applied.
func TestNewWithHandler_RoutesThroughInjectedHandler(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})

	log, err := NewWithHandler(h, LogConfig{Level: "debug", Component: "seam-test"})
	if err != nil {
		t.Fatalf("NewWithHandler: %v", err)
	}
	log.Info("hello", "k", "v")

	out := buf.String()
	if !strings.Contains(out, "hello") || !strings.Contains(out, "seam-test") {
		t.Errorf("injected handler did not receive the record: %s", out)
	}
}

// TestNewWithHandler_NilHandler verifies the error path.
func TestNewWithHandler_NilHandler(t *testing.T) {
	_, err := NewWithHandler(nil, LogConfig{Level: "info"})
	var nilErr *NilHandlerError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected *NilHandlerError, got %T: %v", err, err)
	}
}

// TestSlog_ReturnsUsableLogger verifies the accessor hands back the live
// slog logger.
func TestSlog_ReturnsUsableLogger(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, nil)
	log, err := NewWithHandler(h, LogConfig{Level: "info"})
	if err != nil {
		t.Fatalf("NewWithHandler: %v", err)
	}
	s := log.Slog()
	if s == nil {
		t.Fatal("Slog() returned nil")
	}
	s.Info("direct")
	if !strings.Contains(buf.String(), "direct") {
		t.Error("Slog() logger did not emit through the injected handler")
	}
}

// TestSlog_OnConfigConstructedLogger verifies the accessor on the standard
// constructor path too.
func TestSlog_OnConfigConstructedLogger(t *testing.T) {
	log, err := New(LogConfig{Level: "info", Output: "stderr"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if log.Slog() == nil {
		t.Error("Slog() returned nil on config-constructed logger")
	}
}
