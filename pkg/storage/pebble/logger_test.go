// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pebble

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestSlogPebbleLogger_InfofHiddenAtInfoLevel proves Pebble's operational
// chatter (Infof — "Found N WALs", "[JOB N] WAL ...") is routed to DEBUG and is
// therefore invisible at the default INFO level, while errors still surface.
func TestSlogPebbleLogger_InfofHiddenAtInfoLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	pl := NewLogger(logger)

	pl.Infof("Found %d WALs", 3)
	if buf.Len() != 0 {
		t.Fatalf("Infof produced output at INFO level (should be DEBUG-hidden): %q", buf.String())
	}

	pl.Errorf("background error: %s", "disk full")
	out := buf.String()
	if !strings.Contains(out, "background error: disk full") {
		t.Fatalf("Errorf not surfaced at ERROR: %q", out)
	}
	if !strings.Contains(out, "component=pebble") {
		t.Fatalf("expected component=pebble attribute, got: %q", out)
	}
}

// TestSlogPebbleLogger_InfofVisibleAtDebugLevel proves the chatter is recoverable
// by lowering the log level to DEBUG (it is hidden, not discarded).
func TestSlogPebbleLogger_InfofVisibleAtDebugLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	NewLogger(logger).Infof("Found %d WALs", 0)
	if !strings.Contains(buf.String(), "Found 0 WALs") {
		t.Fatalf("Infof not visible at DEBUG level: %q", buf.String())
	}
}

// TestNewLogger_NilUsesDefault proves a nil logger is accepted (defaults to
// slog.Default()) and the adapter satisfies pebble.Logger.
func TestNewLogger_NilUsesDefault(t *testing.T) {
	if NewLogger(nil) == nil {
		t.Fatal("NewLogger(nil) returned nil")
	}
}

// TestToPebbleOptions_SetsLogger proves the shared options builder always wires
// the quiet logger so the default store path never falls back to Pebble's noisy
// stdlib DefaultLogger.
func TestToPebbleOptions_SetsLogger(t *testing.T) {
	opts := DefaultConfig(t.TempDir()).ToPebbleOptions()
	if opts.Logger == nil {
		t.Fatal("ToPebbleOptions did not set opts.Logger")
	}
	if _, ok := opts.Logger.(*slogPebbleLogger); !ok {
		t.Fatalf("opts.Logger = %T, want *slogPebbleLogger", opts.Logger)
	}
}
