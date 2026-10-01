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

// These tests drive HardenProcess's decisions IN-PROCESS against doubles of
// the platform mechanism, the tracer lookup and the FIPS predicate, because
// the real mechanism changes the test binary itself and cannot be undone. The
// real mechanism is proven from a subprocess in harden_linux_test.go.

// hardenSeams installs doubles for every seam HardenProcess reads, for one
// test, and counts the calls that reach the mechanism.
type hardenSeams struct {
	denyCalls   int
	tracerCalls int
}

func swapHardenSeams(t *testing.T, deny func() error, tracer func() (int, error), fips bool) *hardenSeams {
	t.Helper()
	s := &hardenSeams{}
	prevDeny, prevTracer, prevFIPS := denyInspection, processTracer, fipsMode
	denyInspection = func() error {
		s.denyCalls++
		return deny()
	}
	processTracer = func() (int, error) {
		s.tracerCalls++
		return tracer()
	}
	fipsMode = func() bool { return fips }
	t.Cleanup(func() { denyInspection, processTracer, fipsMode = prevDeny, prevTracer, prevFIPS })
	return s
}

func notTraced() (int, error) { return 0, nil }
func noError() error          { return nil }

// captureLogger returns a text logger writing into the returned buffer.
func captureLogger() (*slog.Logger, *bytes.Buffer) {
	var out bytes.Buffer
	return slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug})), &out
}

func TestHardenProcess_AppliesTheMechanismWhenNotOptedOut(t *testing.T) {
	for _, value := range []string{"", "false", "0", "FALSE"} {
		t.Run(strconv.Quote(value), func(t *testing.T) {
			t.Setenv(AllowProcessInspectionEnv, value)
			s := swapHardenSeams(t, noError, notTraced, false)
			logger, out := captureLogger()
			got, err := HardenProcess(logger)
			if err != nil {
				t.Fatalf("HardenProcess: %v", err)
			}
			if got != ProcessHardened {
				t.Fatalf("HardenProcess = %v, want %v", got, ProcessHardened)
			}
			if s.denyCalls != 1 || s.tracerCalls != 1 {
				t.Fatalf("mechanism applied %d times and tracer checked %d times, want 1 and 1", s.denyCalls, s.tracerCalls)
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
			s := swapHardenSeams(t, noError, func() (int, error) { return 4242, nil }, false)
			logger, out := captureLogger()
			got, err := HardenProcess(logger)
			if err != nil {
				t.Fatalf("HardenProcess: %v", err)
			}
			if got != ProcessInspectionAllowed {
				t.Fatalf("HardenProcess = %v, want %v", got, ProcessInspectionAllowed)
			}
			if s.denyCalls != 0 || s.tracerCalls != 0 {
				t.Fatalf("the opt-out still applied the mechanism (%d) or the tracer check (%d)", s.denyCalls, s.tracerCalls)
			}
			if !strings.Contains(out.String(), "level=WARN") || !strings.Contains(out.String(), AllowProcessInspectionEnv) {
				t.Fatalf("the opt-out must log a WARN naming %s; got %q", AllowProcessInspectionEnv, out.String())
			}
		})
	}
}

// In FIPS 140-3 mode the opt-out is refused outright: the process neither
// runs inspectable nor quietly hardens against the operator's stated intent.
func TestHardenProcess_RefusesTheOptOutInFIPSMode(t *testing.T) {
	t.Setenv(AllowProcessInspectionEnv, "true")
	s := swapHardenSeams(t, noError, notTraced, true)
	got, err := HardenProcess(nil)
	var refused *ProcessInspectionOptOutRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("HardenProcess = %v (%T), want *ProcessInspectionOptOutRefusedError", err, err)
	}
	if got != 0 {
		t.Fatalf("a refusal reported outcome %v", got)
	}
	if !strings.Contains(err.Error(), "FIPS") || !strings.Contains(err.Error(), AllowProcessInspectionEnv) {
		t.Fatalf("the refusal must name FIPS mode and the variable; got %q", err.Error())
	}
	if s.denyCalls != 0 {
		t.Fatalf("a refused opt-out must not be followed by anything; mechanism ran %d times", s.denyCalls)
	}
}

// FIPS mode does not change an ordinary start.
func TestHardenProcess_FIPSModeHardensNormally(t *testing.T) {
	t.Setenv(AllowProcessInspectionEnv, "")
	swapHardenSeams(t, noError, notTraced, true)
	if got, err := HardenProcess(nil); err != nil || got != ProcessHardened {
		t.Fatalf("HardenProcess = %v, %v; want %v, nil", got, err, ProcessHardened)
	}
}

// A tracer attached BEFORE the process hardened keeps its access: marking the
// process non-dumpable stops new attaches, not existing ones. So a traced
// process refuses to continue.
func TestHardenProcess_RefusesAnAlreadyTracedProcess(t *testing.T) {
	t.Setenv(AllowProcessInspectionEnv, "")
	s := swapHardenSeams(t, noError, func() (int, error) { return 4242, nil }, false)
	_, err := HardenProcess(nil)
	var traced *ProcessTracedError
	if !errors.As(err, &traced) {
		t.Fatalf("HardenProcess = %v (%T), want *ProcessTracedError", err, err)
	}
	if traced.TracerPID != 4242 {
		t.Fatalf("TracerPID = %d, want 4242", traced.TracerPID)
	}
	if !strings.Contains(err.Error(), "4242") || !strings.Contains(err.Error(), AllowProcessInspectionEnv) {
		t.Fatalf("the error must name the tracer and the opt-out; got %q", err.Error())
	}
	if s.denyCalls != 1 {
		t.Fatalf("the mechanism must be applied before the tracer is checked; ran %d times", s.denyCalls)
	}
}

// A tracer lookup that fails is a refusal: the process cannot show it is not
// being traced.
func TestHardenProcess_TracerLookupFailureIsARefusal(t *testing.T) {
	t.Setenv(AllowProcessInspectionEnv, "")
	lookup := errors.New("status unreadable")
	swapHardenSeams(t, noError, func() (int, error) { return 0, lookup }, false)
	_, err := HardenProcess(nil)
	var hardErr *ProcessHardeningError
	if !errors.As(err, &hardErr) || !errors.Is(err, lookup) {
		t.Fatalf("HardenProcess = %v (%T), want *ProcessHardeningError wrapping the lookup error", err, err)
	}
}

func TestHardenProcess_RefusesAMalformedOptOut(t *testing.T) {
	t.Setenv(AllowProcessInspectionEnv, "yes-please")
	s := swapHardenSeams(t, noError, notTraced, false)
	_, err := HardenProcess(nil)
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
	if s.denyCalls != 0 {
		t.Fatalf("a malformed opt-out is refused before anything is applied; mechanism ran %d times", s.denyCalls)
	}
}

func TestHardenProcess_ReportsAPlatformRefusal(t *testing.T) {
	t.Setenv(AllowProcessInspectionEnv, "")
	refused := errors.New("operation not permitted")
	s := swapHardenSeams(t, func() error { return refused }, notTraced, false)
	_, err := HardenProcess(nil)
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
	if s.tracerCalls != 0 {
		t.Fatalf("a refused mechanism must not go on to the tracer check")
	}
}

func TestHardenProcess_NilLoggerUsesTheDefault(t *testing.T) {
	t.Setenv(AllowProcessInspectionEnv, "true")
	swapHardenSeams(t, noError, notTraced, false)
	logger, out := captureLogger()
	prev := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(prev) })
	if _, err := HardenProcess(nil); err != nil {
		t.Fatalf("HardenProcess: %v", err)
	}
	if !strings.Contains(out.String(), AllowProcessInspectionEnv) {
		t.Fatalf("a nil logger must fall back to slog.Default; got %q", out.String())
	}
}

func TestProcessHardening_String(t *testing.T) {
	cases := map[ProcessHardening]string{
		ProcessHardened:             "hardened",
		ProcessInspectionAllowed:    "inspection-allowed",
		ProcessHardeningUnavailable: "unavailable",
		ProcessHardening(0):         "unknown",
		ProcessHardening(99):        "unknown",
	}
	for h, want := range cases {
		if got := h.String(); got != want {
			t.Errorf("ProcessHardening(%d).String() = %q, want %q", int(h), got, want)
		}
	}
}
