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
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	secure "github.com/jeremyhahn/go-qrdb/sdk/go/secure"
)

// Process hardening cannot be undone, so no test here applies the real
// mechanism: TestMain installs a double over hardenSecureProcess, and each test
// below layers its own on top. pkg/secure proves the mechanism from a
// subprocess (go-qrdb#554).

// swapHardenSecureProcess overrides the start command's process-hardening
// seam for one test.
func swapHardenSecureProcess(t *testing.T, fn func(*slog.Logger) (secure.ProcessHardening, error)) {
	t.Helper()
	prev := hardenSecureProcess
	hardenSecureProcess = fn
	t.Cleanup(func() { hardenSecureProcess = prev })
}

// productionHardenSecureProcess is hardenSecureProcess as the package
// initialised it, captured at package initialisation — before TestMain
// installs its double.
var productionHardenSecureProcess = hardenSecureProcess

// The production seam must be the real hardening. A double that shipped would
// leave every node inspectable with nothing logged.
func TestHardenSecureProcess_ProductionSeamIsSecureHardenProcess(t *testing.T) {
	if reflect.ValueOf(productionHardenSecureProcess).Pointer() != reflect.ValueOf(secure.HardenProcess).Pointer() {
		t.Fatal("hardenSecureProcess is not initialised to secure.HardenProcess")
	}
	if reflect.ValueOf(hardenSecureProcess).Pointer() == reflect.ValueOf(secure.HardenProcess).Pointer() {
		t.Fatal("TestMain must double hardenSecureProcess: the real one cannot be undone in this binary")
	}
}

// executeStart runs the start command with args, discarding cobra's output.
func executeStart(t *testing.T, args ...string) (*startFlags, error) {
	t.Helper()
	cmd, f := newStartCmdWithFlags()
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	return f, cmd.Execute()
}

// Hardening is the FIRST thing the start command does — before it reads a
// configuration file, which may itself carry secret references. A missing
// config file would fail later; the refusal must come first.
func TestStartRunE_HardensBeforeLoadingConfig(t *testing.T) {
	refused := &secure.ProcessHardeningError{Mechanism: "test", Err: errors.New("operation not permitted")}
	swapHardenSecureProcess(t, func(logger *slog.Logger) (secure.ProcessHardening, error) {
		if logger == nil {
			t.Error("start must pass a logger to HardenProcess")
		}
		return 0, refused
	})
	allocated := false
	swapSecureLifecycle(t, func(int64) (*secure.Allocator, error) {
		allocated = true
		return nil, errors.New("unreachable")
	}, nil, nil, nil)

	_, err := executeStart(t, "--config", filepath.Join(t.TempDir(), "missing.yaml"))
	if !errors.Is(err, refused) {
		t.Fatalf("start = %v (%T), want the hardening refusal before any config is read", err, err)
	}
	if allocated {
		t.Fatal("start built a locked-memory budget after hardening was refused")
	}
}

// A malformed opt-out is a refusal too, through the real HardenProcess: it is
// refused before the mechanism would run, so this test binary is untouched.
func TestStartRunE_MalformedInspectionOptOutFailsStartup(t *testing.T) {
	t.Setenv(secure.AllowProcessInspectionEnv, "sometimes")
	swapHardenSecureProcess(t, secure.HardenProcess)
	_, err := executeStart(t, "--config", filepath.Join(t.TempDir(), "missing.yaml"))
	var optErr *secure.ProcessInspectionOptOutError
	if !errors.As(err, &optErr) {
		t.Fatalf("start = %v (%T), want *ProcessInspectionOptOutError", err, err)
	}
}

// The outcome is carried to runStart, which records an honoured opt-out in the
// compliance audit trail once that trail exists.
func TestStartRunE_CarriesTheHardeningOutcome(t *testing.T) {
	swapHardenSecureProcess(t, func(*slog.Logger) (secure.ProcessHardening, error) {
		return secure.ProcessInspectionAllowed, nil
	})
	f, err := executeStart(t, "--config", filepath.Join(t.TempDir(), "missing.yaml"))
	if err == nil {
		t.Fatal("a missing config file must still fail start")
	}
	if f.processHardening != secure.ProcessInspectionAllowed {
		t.Fatalf("processHardening = %v, want %v", f.processHardening, secure.ProcessInspectionAllowed)
	}
}

// failingAuditLogger refuses every event, the way a full disk refuses the
// chained audit file.
type failingAuditLogger struct{ err error }

var _ audit.AuditLogger = (*failingAuditLogger)(nil)

func (l *failingAuditLogger) Emit(context.Context, audit.AuditEvent) error { return l.err }
func (l *failingAuditLogger) Query(context.Context, audit.Filter) (*audit.Page, error) {
	return &audit.Page{}, nil
}
func (l *failingAuditLogger) Close() error { return nil }

func TestAuditInspectionOptOut_RecordsAnHonouredOptOut(t *testing.T) {
	ring := audit.NewRingBuffer(8)
	if err := auditInspectionOptOut(context.Background(), ring, secure.ProcessInspectionAllowed); err != nil {
		t.Fatalf("auditInspectionOptOut: %v", err)
	}
	events := ring.Snapshot()
	if len(events) != 1 {
		t.Fatalf("recorded %d events, want 1", len(events))
	}
	ev := events[0]
	if ev.Actor != "qrdbd" || ev.Resource != "process" || ev.Action != "disable-inspection-hardening" ||
		ev.Outcome != audit.OutcomeSuccess {
		t.Fatalf("event = %+v", ev)
	}
	if ev.Attributes["env"] != secure.AllowProcessInspectionEnv {
		t.Fatalf("event must name the opt-out variable; attributes = %v", ev.Attributes)
	}
}

func TestAuditInspectionOptOut_RecordsNothingOtherwise(t *testing.T) {
	for _, h := range []secure.ProcessHardening{secure.ProcessHardened, secure.ProcessHardeningUnavailable} {
		ring := audit.NewRingBuffer(8)
		if err := auditInspectionOptOut(context.Background(), ring, h); err != nil {
			t.Fatalf("auditInspectionOptOut(%v): %v", h, err)
		}
		if n := len(ring.Snapshot()); n != 0 {
			t.Fatalf("auditInspectionOptOut(%v) recorded %d events, want 0", h, n)
		}
	}
}

// A node must not run inspectable without the record that says so: a refused
// audit write fails start.
func TestAuditInspectionOptOut_AnUnrecordableOptOutFailsStart(t *testing.T) {
	sinkErr := errors.New("disk full")
	err := auditInspectionOptOut(context.Background(), &failingAuditLogger{err: sinkErr}, secure.ProcessInspectionAllowed)
	var auditErr *InspectionOptOutAuditError
	if !errors.As(err, &auditErr) || !errors.Is(err, sinkErr) {
		t.Fatalf("auditInspectionOptOut = %v (%T), want *InspectionOptOutAuditError wrapping the sink error", err, err)
	}
}
