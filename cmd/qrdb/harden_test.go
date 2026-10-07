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
	"bytes"
	"errors"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/sdk/go/secure"
)

// None of these tests lets the real mechanism run against this test binary: a
// non-dumpable process cannot be made dumpable again. pkg/secure proves the
// mechanism itself from a subprocess (go-qrdb#554).

// swapHardenProcess replaces the seam for one test.
func swapHardenProcess(t *testing.T, fn func(*slog.Logger) (secure.ProcessHardening, error)) {
	t.Helper()
	prev := hardenProcess
	hardenProcess = fn
	t.Cleanup(func() { hardenProcess = prev })
}

// The production seam must be the real hardening; a double that shipped would
// leave the desktop and CLI binary inspectable with nothing logged.
func TestHardenProcess_ProductionSeamIsSecureHardenProcess(t *testing.T) {
	if reflect.ValueOf(hardenProcess).Pointer() != reflect.ValueOf(secure.HardenProcess).Pointer() {
		t.Fatal("hardenProcess is not secure.HardenProcess")
	}
}

// run refuses to start, before the GUI/CLI branch, when hardening is refused.
func TestRun_HardeningRefusalExitsNonZero(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })
	os.Args = []string{"qrdb", "version"}

	refused := &secure.ProcessHardeningError{Mechanism: "test", Err: errors.New("operation not permitted")}
	called := false
	swapHardenProcess(t, func(logger *slog.Logger) (secure.ProcessHardening, error) {
		called = true
		if logger == nil {
			t.Error("hardenAtStartup must hand HardenProcess a logger")
		}
		return 0, refused
	})
	if code := run(); code != 1 {
		t.Fatalf("run() = %d after a hardening refusal, want 1", code)
	}
	if !called {
		t.Fatal("run() did not harden the process")
	}
}

// The opt-out warning goes to the writer hardenAtStartup is given (stderr in
// production), never to the CLI's stdout. It runs the REAL HardenProcess, which
// is safe here only because the opt-out skips the mechanism. Under a FIPS
// build (the nightly FIPS job runs this suite so) the opt-out is refused
// instead, and that answer is asserted exactly.
func TestHardenAtStartup_OptOutWarnsOnTheGivenWriter(t *testing.T) {
	t.Setenv(secure.AllowProcessInspectionEnv, "true")
	var out bytes.Buffer
	err := hardenAtStartup(&out)
	if seal.FIPSEnabled() {
		var refused *secure.ProcessInspectionOptOutRefusedError
		if !errors.As(err, &refused) {
			t.Fatalf("hardenAtStartup in FIPS mode = %v; want *ProcessInspectionOptOutRefusedError", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("hardenAtStartup: %v", err)
	}
	if !strings.Contains(out.String(), "level=WARN") ||
		!strings.Contains(out.String(), secure.AllowProcessInspectionEnv) {
		t.Fatalf("the opt-out must warn on the given writer, naming the variable; got %q", out.String())
	}
}

// A malformed opt-out is refused through the real HardenProcess before any
// mechanism runs.
func TestHardenAtStartup_MalformedOptOutIsRefused(t *testing.T) {
	t.Setenv(secure.AllowProcessInspectionEnv, "perhaps")
	var optErr *secure.ProcessInspectionOptOutError
	if err := hardenAtStartup(&bytes.Buffer{}); !errors.As(err, &optErr) {
		t.Fatalf("hardenAtStartup = %v (%T), want *ProcessInspectionOptOutError", err, err)
	}
}
