// Copyright (c) 2026 Jeremy Hahn
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

package secure

import (
	"bytes"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"testing"
)

// These tests drive HardenProcess's decisions IN-PROCESS against a double of
// the platform mechanism, because the real one changes the test binary itself
// and cannot be undone. The real mechanism is proven from a subprocess in
// harden_linux_test.go.

// swapDenyInspection installs fn as the platform mechanism for one test and
// counts the calls that reach it.
func swapDenyInspection(t *testing.T, fn func() error) *int {
	t.Helper()
	calls := new(int)
	prev := denyInspection
	denyInspection = func() error {
		*calls++
		return fn()
	}
	t.Cleanup(func() { denyInspection = prev })
	return calls
}

// captureLogger returns a text logger writing into the returned buffer.
func captureLogger() (*slog.Logger, *bytes.Buffer) {
	var out bytes.Buffer
	return slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug})), &out
}

func TestHardenProcess_AppliesTheMechanismWhenNotOptedOut(t *testing.T) {
	for _, value := range []string{"", "false", "0", "FALSE"} {
		t.Run(strconv.Quote(value), func(t *testing.T) {
			t.Setenv(AllowProcessInspectionEnv, value)
			calls := swapDenyInspection(t, func() error { return nil })
			logger, out := captureLogger()
			if err := HardenProcess(logger); err != nil {
				t.Fatalf("HardenProcess: %v", err)
			}
			if *calls != 1 {
				t.Fatalf("mechanism applied %d times, want 1", *calls)
			}
			if out.Len() != 0 {
				t.Fatalf("a hardened process logged %q; only the opt-out warns", out.String())
			}
		})
	}
}

func TestHardenProcess_OptOutSkipsTheMechanismAndWarns(t *testing.T) {
	for _, value := range []string{"1", "true", "TRUE", "t"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(AllowProcessInspectionEnv, value)
			calls := swapDenyInspection(t, func() error { return nil })
			logger, out := captureLogger()
			if err := HardenProcess(logger); err != nil {
				t.Fatalf("HardenProcess: %v", err)
			}
			if *calls != 0 {
				t.Fatalf("the opt-out still applied the mechanism %d times", *calls)
			}
			got := out.String()
			if !strings.Contains(got, "level=WARN") || !strings.Contains(got, AllowProcessInspectionEnv) {
				t.Fatalf("the opt-out must log a WARN naming %s; got %q", AllowProcessInspectionEnv, got)
			}
		})
	}
}

func TestHardenProcess_RefusesAMalformedOptOut(t *testing.T) {
	t.Setenv(AllowProcessInspectionEnv, "yes-please")
	calls := swapDenyInspection(t, func() error { return nil })
	err := HardenProcess(nil)
	var optErr *ProcessInspectionOptOutError
	if !errors.As(err, &optErr) {
		t.Fatalf("HardenProcess = %v (%T), want *ProcessInspectionOptOutError", err, err)
	}
	if optErr.Value != "yes-please" {
		t.Fatalf("Value = %q, want the rejected text", optErr.Value)
	}
	var numErr *strconv.NumError
	if !errors.As(err, &numErr) {
		t.Fatalf("the parse error must stay reachable through Unwrap; got %v", err)
	}
	if !strings.Contains(err.Error(), AllowProcessInspectionEnv) {
		t.Fatalf("the error must name the variable; got %q", err.Error())
	}
	if *calls != 0 {
		t.Fatalf("a malformed opt-out is refused before anything is applied; mechanism ran %d times", *calls)
	}
}

func TestHardenProcess_ReportsAPlatformRefusal(t *testing.T) {
	t.Setenv(AllowProcessInspectionEnv, "")
	refused := errors.New("operation not permitted")
	swapDenyInspection(t, func() error { return refused })
	err := HardenProcess(nil)
	var hardErr *ProcessHardeningError
	if !errors.As(err, &hardErr) {
		t.Fatalf("HardenProcess = %v (%T), want *ProcessHardeningError", err, err)
	}
	if !errors.Is(err, refused) {
		t.Fatalf("the platform error must stay reachable through Unwrap; got %v", err)
	}
	if hardErr.Mechanism != inspectionMechanism {
		t.Fatalf("Mechanism = %q, want %q", hardErr.Mechanism, inspectionMechanism)
	}
	if !strings.Contains(err.Error(), inspectionMechanism) || !strings.Contains(err.Error(), refused.Error()) {
		t.Fatalf("the error must name the mechanism and the cause; got %q", err.Error())
	}
}

func TestHardenProcess_NilLoggerUsesTheDefault(t *testing.T) {
	t.Setenv(AllowProcessInspectionEnv, "true")
	swapDenyInspection(t, func() error { return nil })
	logger, out := captureLogger()
	prev := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(prev) })
	if err := HardenProcess(nil); err != nil {
		t.Fatalf("HardenProcess: %v", err)
	}
	if !strings.Contains(out.String(), AllowProcessInspectionEnv) {
		t.Fatalf("a nil logger must fall back to slog.Default; got %q", out.String())
	}
}
