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

//go:build !integration

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// ---------------------------------------------------------------------------
// NotConnectedError
// ---------------------------------------------------------------------------

func TestNotConnectedError_Error(t *testing.T) {
	e := &NotConnectedError{}
	expected := "qrdb: not connected to server"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestNotConnectedError_ErrorsAs(t *testing.T) {
	var target *NotConnectedError
	e := &NotConnectedError{}
	if !errors.As(e, &target) {
		t.Fatal("expected errors.As to match *NotConnectedError")
	}
}

// ---------------------------------------------------------------------------
// GUIUnavailableError
// ---------------------------------------------------------------------------

func TestGUIUnavailableError_Error(t *testing.T) {
	e := &GUIUnavailableError{}
	expected := "qrdb: GUI unavailable (built without desktop tag)"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestGUIUnavailableError_ErrorsAs(t *testing.T) {
	var target *GUIUnavailableError
	e := &GUIUnavailableError{}
	if !errors.As(e, &target) {
		t.Fatal("expected errors.As to match *GUIUnavailableError")
	}
}

// ---------------------------------------------------------------------------
// FeatureDisabledError
// ---------------------------------------------------------------------------

func TestFeatureDisabledError_Error(t *testing.T) {
	e := &FeatureDisabledError{Feature: "timeseries"}
	expected := "qrdb: feature disabled: timeseries"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestFeatureDisabledError_Error_Vector(t *testing.T) {
	e := &FeatureDisabledError{Feature: "vector"}
	expected := "qrdb: feature disabled: vector"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestFeatureDisabledError_ErrorsAs(t *testing.T) {
	var target *FeatureDisabledError
	e := &FeatureDisabledError{Feature: "sql"}
	if !errors.As(e, &target) {
		t.Fatal("expected errors.As to match *FeatureDisabledError")
	}
	if target.Feature != "sql" {
		t.Errorf("expected Feature=%q, got %q", "sql", target.Feature)
	}
}

// ---------------------------------------------------------------------------
// EmbeddedServerError
// ---------------------------------------------------------------------------

func TestEmbeddedServerError_Error(t *testing.T) {
	inner := errors.New("startup failure")
	e := &EmbeddedServerError{Op: "start", Err: inner}
	expected := "qrdb: embedded server start: startup failure"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestEmbeddedServerError_Error_Stop(t *testing.T) {
	inner := errors.New("already stopped")
	e := &EmbeddedServerError{Op: "stop", Err: inner}
	expected := "qrdb: embedded server stop: already stopped"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestEmbeddedServerError_Unwrap_ErrorsIs(t *testing.T) {
	sentinel := errors.New("sentinel")
	e := &EmbeddedServerError{Op: "start", Err: sentinel}
	if !errors.Is(e, sentinel) {
		t.Fatal("expected errors.Is to match sentinel via Unwrap")
	}
}

// ---------------------------------------------------------------------------
// AlreadyRunningError
// ---------------------------------------------------------------------------

func TestAlreadyRunningError_Error(t *testing.T) {
	e := &AlreadyRunningError{}
	expected := "qrdb: embedded server is already running"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestAlreadyRunningError_ErrorsAs(t *testing.T) {
	var target *AlreadyRunningError
	e := &AlreadyRunningError{}
	if !errors.As(e, &target) {
		t.Fatal("expected errors.As to match *AlreadyRunningError")
	}
}

// ---------------------------------------------------------------------------
// NotRunningError
// ---------------------------------------------------------------------------

func TestNotRunningError_Error(t *testing.T) {
	e := &NotRunningError{}
	expected := "qrdb: embedded server is not running"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestNotRunningError_ErrorsAs(t *testing.T) {
	var target *NotRunningError
	e := &NotRunningError{}
	if !errors.As(e, &target) {
		t.Fatal("expected errors.As to match *NotRunningError")
	}
}

// ---------------------------------------------------------------------------
// UnknownBackendError (additional tests)
// ---------------------------------------------------------------------------

func TestUnknownBackendError_Error(t *testing.T) {
	e := &UnknownBackendError{Backend: "rocksdb"}
	expected := "qrdb: unknown storage backend: rocksdb"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestUnknownBackendError_ErrorsAs(t *testing.T) {
	var target *UnknownBackendError
	e := &UnknownBackendError{Backend: "baddb"}
	if !errors.As(e, &target) {
		t.Fatal("expected errors.As to match *UnknownBackendError")
	}
	if target.Backend != "baddb" {
		t.Errorf("expected Backend=%q, got %q", "baddb", target.Backend)
	}
}

// ---------------------------------------------------------------------------
// SQLExecutionError (additional coverage)
// ---------------------------------------------------------------------------

func TestSQLExecutionError_ErrorsIs(t *testing.T) {
	sentinel := errors.New("sentinel-sql")
	e := &SQLExecutionError{Query: "SELECT 1", Err: sentinel}
	if !errors.Is(e, sentinel) {
		t.Fatal("expected errors.Is to match via Unwrap")
	}
}

func TestSQLExecutionError_ErrorsAs(t *testing.T) {
	var target *SQLExecutionError
	inner := errors.New("table not found")
	e := &SQLExecutionError{Query: "SELECT * FROM missing", Err: inner}
	if !errors.As(e, &target) {
		t.Fatal("expected errors.As to match *SQLExecutionError")
	}
}

// ---------------------------------------------------------------------------
// InstanceAlreadyExistsError
// ---------------------------------------------------------------------------

func TestInstanceAlreadyExistsError_Error(t *testing.T) {
	e := &InstanceAlreadyExistsError{Name: "mydb"}
	expected := `qrdb: database instance already exists: "mydb"`
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestInstanceAlreadyExistsError_Error_DifferentName(t *testing.T) {
	e := &InstanceAlreadyExistsError{Name: "otherdb"}
	msg := e.Error()
	if !containsStr(msg, "otherdb") {
		t.Fatalf("expected name in error, got: %q", msg)
	}
}

func TestInstanceAlreadyExistsError_ErrorsAs(t *testing.T) {
	var target *InstanceAlreadyExistsError
	e := &InstanceAlreadyExistsError{Name: "x"}
	if !errors.As(e, &target) {
		t.Fatal("expected errors.As to match *InstanceAlreadyExistsError")
	}
	if target.Name != "x" {
		t.Errorf("expected Name=%q, got %q", "x", target.Name)
	}
}

// ---------------------------------------------------------------------------
// InstanceNotFoundError
// ---------------------------------------------------------------------------

func TestInstanceNotFoundError_Error(t *testing.T) {
	e := &InstanceNotFoundError{Name: "ghost"}
	expected := `qrdb: database instance not found: "ghost"`
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestInstanceNotFoundError_Error_DifferentName(t *testing.T) {
	e := &InstanceNotFoundError{Name: "missing-db"}
	msg := e.Error()
	if !containsStr(msg, "missing-db") {
		t.Fatalf("expected name in error, got: %q", msg)
	}
}

func TestInstanceNotFoundError_ErrorsAs(t *testing.T) {
	var target *InstanceNotFoundError
	e := &InstanceNotFoundError{Name: "y"}
	if !errors.As(e, &target) {
		t.Fatal("expected errors.As to match *InstanceNotFoundError")
	}
}

// ---------------------------------------------------------------------------
// DaemonAlreadyRunningError
// ---------------------------------------------------------------------------

func TestDaemonAlreadyRunningError_Error(t *testing.T) {
	e := &DaemonAlreadyRunningError{PID: 1234}
	expected := "qrdb: daemon already running with PID 1234"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestDaemonAlreadyRunningError_Error_DifferentPID(t *testing.T) {
	e := &DaemonAlreadyRunningError{PID: 42}
	msg := e.Error()
	if !containsStr(msg, "42") {
		t.Fatalf("expected PID in error message, got: %q", msg)
	}
}

func TestDaemonAlreadyRunningError_ErrorsAs(t *testing.T) {
	var target *DaemonAlreadyRunningError
	e := &DaemonAlreadyRunningError{PID: 5678}
	if !errors.As(e, &target) {
		t.Fatal("expected errors.As to match *DaemonAlreadyRunningError")
	}
	if target.PID != 5678 {
		t.Errorf("expected PID=5678, got %d", target.PID)
	}
}

// ---------------------------------------------------------------------------
// DaemonNotRunningError
// ---------------------------------------------------------------------------

func TestDaemonNotRunningError_Error(t *testing.T) {
	e := &DaemonNotRunningError{}
	expected := "qrdb: daemon is not running"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestDaemonNotRunningError_ErrorsAs(t *testing.T) {
	var target *DaemonNotRunningError
	e := &DaemonNotRunningError{}
	if !errors.As(e, &target) {
		t.Fatal("expected errors.As to match *DaemonNotRunningError")
	}
}

// ---------------------------------------------------------------------------
// DaemonStartError
// ---------------------------------------------------------------------------

func TestDaemonStartError_Error(t *testing.T) {
	inner := errors.New("exec: not found")
	e := &DaemonStartError{Err: inner}
	expected := "qrdb: daemon start failed: exec: not found"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestDaemonStartError_Unwrap(t *testing.T) {
	inner := errors.New("no such file")
	e := &DaemonStartError{Err: inner}
	if unwrapped := e.Unwrap(); unwrapped != inner {
		t.Fatalf("expected unwrapped %v, got %v", inner, unwrapped)
	}
}

func TestDaemonStartError_ErrorsIs(t *testing.T) {
	sentinel := errors.New("sentinel")
	e := &DaemonStartError{Err: sentinel}
	if !errors.Is(e, sentinel) {
		t.Fatal("expected errors.Is to match via Unwrap")
	}
}

func TestDaemonStartError_ErrorsAs(t *testing.T) {
	var target *DaemonStartError
	e := &DaemonStartError{Err: errors.New("x")}
	if !errors.As(e, &target) {
		t.Fatal("expected errors.As to match *DaemonStartError")
	}
}

// ---------------------------------------------------------------------------
// DaemonStopError
// ---------------------------------------------------------------------------

func TestDaemonStopError_Error(t *testing.T) {
	inner := errors.New("process not found")
	e := &DaemonStopError{Err: inner}
	expected := "qrdb: daemon stop failed: process not found"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestDaemonStopError_Unwrap(t *testing.T) {
	inner := errors.New("signal failed")
	e := &DaemonStopError{Err: inner}
	if unwrapped := e.Unwrap(); unwrapped != inner {
		t.Fatalf("expected unwrapped %v, got %v", inner, unwrapped)
	}
}

func TestDaemonStopError_ErrorsIs(t *testing.T) {
	sentinel := errors.New("sentinel")
	e := &DaemonStopError{Err: sentinel}
	if !errors.Is(e, sentinel) {
		t.Fatal("expected errors.Is to match via Unwrap")
	}
}

func TestDaemonStopError_ErrorsAs(t *testing.T) {
	var target *DaemonStopError
	e := &DaemonStopError{Err: errors.New("x")}
	if !errors.As(e, &target) {
		t.Fatal("expected errors.As to match *DaemonStopError")
	}
}

// ---------------------------------------------------------------------------
// UnknownEngineError
// ---------------------------------------------------------------------------

func TestUnknownEngineError_Error(t *testing.T) {
	e := &UnknownEngineError{Engine: "cassandra"}
	expected := "qrdb: unknown store engine: cassandra"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestUnknownEngineError_Error_DifferentEngine(t *testing.T) {
	e := &UnknownEngineError{Engine: "mongo"}
	msg := e.Error()
	if !containsStr(msg, "mongo") {
		t.Fatalf("expected engine name in error, got: %q", msg)
	}
}

func TestUnknownEngineError_ErrorsAs(t *testing.T) {
	var target *UnknownEngineError
	e := &UnknownEngineError{Engine: "badengine"}
	if !errors.As(e, &target) {
		t.Fatal("expected errors.As to match *UnknownEngineError")
	}
	if target.Engine != "badengine" {
		t.Errorf("expected Engine=%q, got %q", "badengine", target.Engine)
	}
}

// ---------------------------------------------------------------------------
// DaemonBinding.Restart
// ---------------------------------------------------------------------------

func TestDaemonBinding_Restart_NotRunning_ThenStartFails(t *testing.T) {
	d := newTestDaemonBinding(t)

	// Restart when not running just calls Start with an invalid config path.
	// Since qrdbd is not on PATH in tests, it should return a DaemonStartError.
	err := d.Restart("/nonexistent/config.yaml")
	if err == nil {
		t.Fatal("expected error from Restart with invalid daemon path")
	}
	var dse *DaemonStartError
	if !errors.As(err, &dse) {
		t.Fatalf("expected DaemonStartError, got %T: %v", err, err)
	}
}

func TestDaemonBinding_Restart_NotRunning_ErrorMessage(t *testing.T) {
	d := newTestDaemonBinding(t)

	err := d.Restart("/nonexistent/config.yaml")
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !containsStr(msg, "daemon start failed") {
		t.Errorf("expected 'daemon start failed' in error, got: %q", msg)
	}
}

// ---------------------------------------------------------------------------
// DaemonBinding.cleanupPIDFile
// ---------------------------------------------------------------------------

func TestDaemonBinding_CleanupPIDFile_RemovesFile(t *testing.T) {
	d := newTestDaemonBinding(t)

	// Write a PID file.
	pidPath := filepath.Join(d.configDir, "qrdbd.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	d.cleanupPIDFile()

	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("expected PID file to be removed after cleanupPIDFile")
	}
}

func TestDaemonBinding_CleanupPIDFile_NoFileIsNoOp(t *testing.T) {
	d := newTestDaemonBinding(t)

	// No PID file exists; should not panic or error.
	d.cleanupPIDFile()
}

// ---------------------------------------------------------------------------
// DaemonBinding.resolveConfigDir
// ---------------------------------------------------------------------------

func TestDaemonBinding_ResolveConfigDir_WithOverride(t *testing.T) {
	d := newTestDaemonBinding(t)

	dir, err := d.resolveConfigDir()
	if err != nil {
		t.Fatalf("resolveConfigDir: %v", err)
	}
	if dir != d.configDir {
		t.Errorf("expected dir=%q, got %q", d.configDir, dir)
	}
}

func TestDaemonBinding_ResolveConfigDir_CreatesDirectory(t *testing.T) {
	d := newTestDaemonBinding(t)
	// Point to a new subdirectory that doesn't exist yet.
	d.configDir = filepath.Join(t.TempDir(), "newsubdir")

	dir, err := d.resolveConfigDir()
	if err != nil {
		t.Fatalf("resolveConfigDir: %v", err)
	}
	if dir == "" {
		t.Error("expected non-empty directory path")
	}
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Errorf("expected directory to exist: %v", statErr)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// containsStr returns true when s contains substr.
func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || findStr(s, substr))
}

func findStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
