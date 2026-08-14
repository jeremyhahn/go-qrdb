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

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// newTestDaemonBinding creates a DaemonBinding with its config directory
// pointed at a temporary directory so tests never touch the real filesystem.
func newTestDaemonBinding(t *testing.T) *DaemonBinding {
	t.Helper()
	log := testLogger(t)
	d := NewDaemonBinding(log, "")
	d.configDir = t.TempDir()
	d.startup(context.Background())
	return d
}

// ---------------------------------------------------------------------------
// Status
// ---------------------------------------------------------------------------

// TestDaemonBinding_Status_NotRunning verifies that Status returns a
// not-running status when no process is tracked and no PID file exists.
func TestDaemonBinding_Status_NotRunning(t *testing.T) {
	d := newTestDaemonBinding(t)

	status, err := d.Status()
	if err != nil {
		t.Fatalf("Status returned unexpected error: %v", err)
	}
	if status.Running {
		t.Error("expected Running to be false")
	}
	if status.PID != 0 {
		t.Errorf("expected PID 0, got %d", status.PID)
	}
	if status.LogFile == "" {
		t.Error("expected LogFile to be set")
	}
}

// TestDaemonBinding_Status_WithPidFile verifies that Status detects a
// stale PID file (process no longer running) and reports not-running.
func TestDaemonBinding_Status_WithPidFile(t *testing.T) {
	d := newTestDaemonBinding(t)

	// Write a PID file referencing a PID that almost certainly does not
	// exist. PID 2^22 - 1 is safe on Linux (max PID is typically 2^22).
	stalePID := 4194303
	pidPath := filepath.Join(d.configDir, "qrdbd.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(stalePID)), 0o600); err != nil {
		t.Fatalf("failed to write PID file: %v", err)
	}

	status, err := d.Status()
	if err != nil {
		t.Fatalf("Status returned unexpected error: %v", err)
	}

	// The stale PID should not be reported as running because signal(0)
	// will fail for a non-existent process.
	if status.Running {
		t.Error("expected Running to be false for stale PID")
	}
	if status.PID != 0 {
		t.Errorf("expected PID 0 for stale PID, got %d", status.PID)
	}

	// The stale PID file should have been cleaned up.
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("expected stale PID file to be removed")
	}
}

// TestDaemonBinding_Status_WithInvalidPidFile verifies that Status
// handles a PID file containing non-numeric content gracefully.
func TestDaemonBinding_Status_WithInvalidPidFile(t *testing.T) {
	d := newTestDaemonBinding(t)

	pidPath := filepath.Join(d.configDir, "qrdbd.pid")
	if err := os.WriteFile(pidPath, []byte("not-a-number"), 0o600); err != nil {
		t.Fatalf("failed to write PID file: %v", err)
	}

	status, err := d.Status()
	if err != nil {
		t.Fatalf("Status returned unexpected error: %v", err)
	}
	if status.Running {
		t.Error("expected Running to be false for invalid PID file")
	}
	if status.PID != 0 {
		t.Errorf("expected PID 0, got %d", status.PID)
	}
}

// ---------------------------------------------------------------------------
// GetLogs
// ---------------------------------------------------------------------------

// TestDaemonBinding_GetLogs_NoLogFile verifies that GetLogs returns an
// empty string when the log file does not exist.
func TestDaemonBinding_GetLogs_NoLogFile(t *testing.T) {
	d := newTestDaemonBinding(t)

	logs, err := d.GetLogs(100)
	if err != nil {
		t.Fatalf("GetLogs returned unexpected error: %v", err)
	}
	if logs != "" {
		t.Errorf("expected empty string, got %q", logs)
	}
}

// TestDaemonBinding_GetLogs_WithContent verifies that GetLogs returns
// the last N lines from an existing log file.
func TestDaemonBinding_GetLogs_WithContent(t *testing.T) {
	d := newTestDaemonBinding(t)

	logPath := filepath.Join(d.configDir, "qrdbd.log")
	content := "line1\nline2\nline3\nline4\nline5\n"
	if err := os.WriteFile(logPath, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write log file: %v", err)
	}

	// Request only the last 3 lines.
	logs, err := d.GetLogs(3)
	if err != nil {
		t.Fatalf("GetLogs returned unexpected error: %v", err)
	}

	expected := "line3\nline4\nline5"
	if logs != expected {
		t.Errorf("expected %q, got %q", expected, logs)
	}
}

// TestDaemonBinding_GetLogs_AllLines verifies that GetLogs returns all
// lines when maxLines exceeds the file length.
func TestDaemonBinding_GetLogs_AllLines(t *testing.T) {
	d := newTestDaemonBinding(t)

	logPath := filepath.Join(d.configDir, "qrdbd.log")
	content := "alpha\nbeta\n"
	if err := os.WriteFile(logPath, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write log file: %v", err)
	}

	logs, err := d.GetLogs(100)
	if err != nil {
		t.Fatalf("GetLogs returned unexpected error: %v", err)
	}

	expected := "alpha\nbeta"
	if logs != expected {
		t.Errorf("expected %q, got %q", expected, logs)
	}
}

// ---------------------------------------------------------------------------
// Stop
// ---------------------------------------------------------------------------

// TestDaemonBinding_Stop_NotRunning verifies that Stop returns a
// DaemonNotRunningError when no process is being tracked.
func TestDaemonBinding_Stop_NotRunning(t *testing.T) {
	d := newTestDaemonBinding(t)

	err := d.Stop()
	if err == nil {
		t.Fatal("expected error from Stop when not running")
	}

	var notRunning *DaemonNotRunningError
	if !errors.As(err, &notRunning) {
		t.Errorf("expected DaemonNotRunningError, got %T: %v", err, err)
	}
}

// TestDaemonBinding_Stop_NotRunning_ErrorMessage verifies the error
// message content of DaemonNotRunningError.
func TestDaemonBinding_Stop_NotRunning_ErrorMessage(t *testing.T) {
	d := newTestDaemonBinding(t)

	err := d.Stop()
	if err == nil {
		t.Fatal("expected error from Stop when not running")
	}

	expected := "qrdb: daemon is not running"
	if err.Error() != expected {
		t.Errorf("expected error message %q, got %q", expected, err.Error())
	}
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// TestDaemonBinding_Startup_Shutdown verifies the startup and shutdown
// lifecycle hooks execute without error.
func TestDaemonBinding_Startup_Shutdown(t *testing.T) {
	log := testLogger(t)
	d := NewDaemonBinding(log, "")
	d.configDir = t.TempDir()

	ctx := context.Background()
	d.startup(ctx)

	if d.ctx != ctx {
		t.Error("expected ctx to be set after startup")
	}

	// shutdown with no tracked process should be a no-op.
	d.shutdown(ctx)
}

// TestDaemonBinding_Constructor verifies NewDaemonBinding returns a
// valid, non-nil binding with the logger set.
func TestDaemonBinding_Constructor(t *testing.T) {
	log := testLogger(t)
	d := NewDaemonBinding(log, "")

	if d == nil {
		t.Fatal("expected non-nil DaemonBinding")
	}
	if d.logger != log {
		t.Error("expected logger to be set")
	}
	if d.process.Load() != nil {
		t.Error("expected process to be nil initially")
	}
}
