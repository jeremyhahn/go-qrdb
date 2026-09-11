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

// The audit-failure posture and the gating emit (go-qrdb#335, go-qrdb#445).
//
// NO BUILD CONSTRAINT, deliberately, for the same reason
// pkg/auth/middleware/audit_failure_test.go carries none: this is the proof
// for a security property that ships in every build, and every helper it uses
// is defined in this file. Constraining it would delete the proof from the tag
// sets that still enforce the behaviour.

package audit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// THE DEFAULT IS THE SECURITY PROPERTY
//
// These assert the default directly rather than inheriting it from a struct
// literal nobody checks. A security default that is only ever observed
// through the option that sets it is a default that can silently flip.
// ---------------------------------------------------------------------------

// TestFailurePolicy_ZeroValueIsFailClosed pins the REPRESENTATION, not only
// the behaviour. FailClosed must be the zero value so that every call site
// built by a future constructor, a test, or an embedder that never mentions
// the field is fail-closed by construction -- the AuthMiddleware, the
// SOAuthenticator and the REST server all rely on exactly this.
func TestFailurePolicy_ZeroValueIsFailClosed(t *testing.T) {
	var p FailurePolicy
	if p != FailClosed {
		t.Fatalf("the zero FailurePolicy is %v, want FailClosed", p)
	}
	if p.String() != "fail-closed" {
		t.Errorf("String() = %q, want %q", p.String(), "fail-closed")
	}
}

// TestFailurePolicy_String covers both spellings, since the string is what
// reaches an operator's log line and their configuration file alike.
func TestFailurePolicy_String(t *testing.T) {
	if got := FailOpen.String(); got != "fail-open" {
		t.Errorf("FailOpen.String() = %q, want %q", got, "fail-open")
	}
	if got := FailClosed.String(); got != "fail-closed" {
		t.Errorf("FailClosed.String() = %q, want %q", got, "fail-closed")
	}
}

// TestParseFailurePolicy_EmptyIsFailClosed: an omitted configuration key
// resolves to the secure posture, through the SAME table every other spelling
// goes through rather than a separate empty-string branch a later edit could
// forget.
func TestParseFailurePolicy_EmptyIsFailClosed(t *testing.T) {
	p, err := ParseFailurePolicy("")
	if err != nil {
		t.Fatalf("an empty value must resolve, not fail: %v", err)
	}
	if p != FailClosed {
		t.Fatalf("empty resolved to %v, want FailClosed", p)
	}
}

func TestParseFailurePolicy_Spellings(t *testing.T) {
	tests := []struct {
		in   string
		want FailurePolicy
	}{
		{"fail-closed", FailClosed},
		{"FAIL-CLOSED", FailClosed},
		{"  fail-closed  ", FailClosed},
		{"failclosed", FailClosed},
		{"closed", FailClosed},
		{"fail-open", FailOpen},
		{"Fail-Open", FailOpen},
		{"failopen", FailOpen},
		{"open", FailOpen},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseFailurePolicy(tc.in)
			if err != nil {
				t.Fatalf("ParseFailurePolicy(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ParseFailurePolicy(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestParseFailurePolicy_InvalidFailsTheBoot: a typo must not resolve to
// EITHER posture. Guessing fail-closed denies a fleet the operator meant to
// keep serving; guessing fail-open weakens the posture on a typo.
func TestParseFailurePolicy_InvalidFailsTheBoot(t *testing.T) {
	for _, in := range []string{"fail_open", "yes", "true", "permissive", "fail-opne"} {
		t.Run(in, func(t *testing.T) {
			p, err := ParseFailurePolicy(in)
			if err == nil {
				t.Fatalf("ParseFailurePolicy(%q) resolved to %v; an unrecognised value must fail", in, p)
			}
			var invalid *InvalidFailurePolicyError
			if !errors.As(err, &invalid) {
				t.Fatalf("expected *InvalidFailurePolicyError, got %T", err)
			}
			if invalid.Value != in {
				t.Errorf("InvalidFailurePolicyError.Value = %q, want %q", invalid.Value, in)
			}
			if !strings.Contains(invalid.Error(), "fail-closed") {
				t.Errorf("the error must name the accepted values, got %q", invalid.Error())
			}
			// Even the value returned on the error path is the secure one, so
			// a caller that ignores err is not thereby opened up.
			if p != FailClosed {
				t.Errorf("the error path returned %v, want FailClosed", p)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// GateError
// ---------------------------------------------------------------------------

// TestGateError_MessageNamesTheComponent: the component is what tells an
// operator WHICH gate refused, and it reaches them through this string.
func TestGateError_MessageNamesTheComponent(t *testing.T) {
	inner := errors.New("disk full")
	e := &GateError{Component: "so-auth", Err: inner}
	want := "audit: so-auth: audit log failed, refusing operation: disk full"
	if e.Error() != want {
		t.Errorf("got %q, want %q", e.Error(), want)
	}
	if !errors.Is(e, inner) {
		t.Error("GateError must unwrap to the sink's own error")
	}
}

// TestGateError_ZeroComponentStillReads: a GateError built without a
// component -- which a struct literal in a test or an embedder can do -- must
// not produce a message with a dangling colon.
func TestGateError_ZeroComponentStillReads(t *testing.T) {
	e := &GateError{Err: errors.New("disk full")}
	want := "audit: audit log failed, refusing operation: disk full"
	if e.Error() != want {
		t.Errorf("got %q, want %q", e.Error(), want)
	}
	if e.Unwrap() == nil {
		t.Error("Unwrap must return the inner error")
	}
}

// ---------------------------------------------------------------------------
// EmitGated -- against the REAL ChainFileLogger, not a double
//
// The property under test is "the operation is refused because the record did
// not reach the sink's durability boundary". A double that returns a canned
// error proves the branch runs; only a real sink on a real file proves the
// branch is reachable from a real failure.
// ---------------------------------------------------------------------------

// faultedChain returns a REAL ChainFileLogger whose file has been closed,
// which is how the daemon's audit sink fails while in-flight requests are
// still draining. The append then fails at os.File.Write and the sink returns
// *WriteError.
func faultedChain(t *testing.T) (*ChainFileLogger, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit-events.ndjson")
	chain, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	if err := chain.Close(); err != nil {
		t.Fatalf("close (the injected fault): %v", err)
	}
	return chain, path
}

func gateEvent() AuditEvent {
	return AuditEvent{
		Actor:    "so",
		TenantID: "t1",
		Resource: "so-pin",
		Action:   "verify",
		Outcome:  OutcomeSuccess,
	}
}

// TestEmitGated_FailClosedReturnsGateErrorWrappingWriteError is the assertion
// the posture exists for: a real sink that cannot write produces a
// *GateError that unwraps all the way to the operating system's own refusal,
// so nothing in the middle invented the failure.
func TestEmitGated_FailClosedReturnsGateErrorWrappingWriteError(t *testing.T) {
	chain, path := faultedChain(t)

	err := EmitGated(context.Background(), chain, "so-auth", FailClosed, gateEvent())
	if err == nil {
		t.Fatal("a gating record that could not be written must be reported")
	}

	var gateErr *GateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("expected *GateError, got %T: %v", err, err)
	}
	if gateErr.Component != "so-auth" {
		t.Errorf("Component = %q, want %q", gateErr.Component, "so-auth")
	}
	var writeErr *WriteError
	if !errors.As(err, &writeErr) {
		t.Fatalf("GateError must unwrap to *WriteError, got %v", err)
	}
	if writeErr.Backend != path {
		t.Errorf("WriteError.Backend = %q, want the failing audit file %q", writeErr.Backend, path)
	}
	if !errors.Is(err, os.ErrClosed) {
		t.Errorf("the OS error must survive both wraps, got %v", err)
	}
}

// TestEmitGated_FailOpenReturnsNil: the documented escape. The record is still
// lost and still counted; what changes is that the caller proceeds.
func TestEmitGated_FailOpenReturnsNil(t *testing.T) {
	chain, _ := faultedChain(t)

	if err := EmitGated(context.Background(), chain, "so-auth", FailOpen, gateEvent()); err != nil {
		t.Fatalf("fail-open must let the operation proceed, got %v", err)
	}
}

// TestEmitGated_HealthySinkRecordsAndReturnsNil is the CONTROL. Without it a
// function that refused unconditionally would pass every test above.
func TestEmitGated_HealthySinkRecordsAndReturnsNil(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit-events.ndjson")
	chain, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	defer func() { _ = chain.Close() }()

	for _, p := range []FailurePolicy{FailClosed, FailOpen} {
		if err := EmitGated(context.Background(), chain, "so-auth", p, gateEvent()); err != nil {
			t.Fatalf("policy %v: a recordable event must not be refused: %v", p, err)
		}
	}

	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read the trail: %v", readErr)
	}
	if !strings.Contains(string(data), "so-pin") {
		t.Fatalf("the gating record is not in the trail: %q", string(data))
	}
}

// TestEmitGated_NilSinkIsInert: with no sink wired nothing could fail, so
// nothing is refused. A fail-closed posture that denied every request on a
// node with auditing unconfigured would be a denial of service dressed as a
// security control.
func TestEmitGated_NilSinkIsInert(t *testing.T) {
	for _, p := range []FailurePolicy{FailClosed, FailOpen} {
		if err := EmitGated(context.Background(), nil, "so-auth", p, gateEvent()); err != nil {
			t.Fatalf("policy %v: a nil sink must be inert, got %v", p, err)
		}
	}
}

// TestEmitGated_DoesNotRetryTheSink pins the rule that the refusal is not
// itself audited: exactly ONE Emit reaches the sink, whatever the posture. A
// second write would be a retry on a sink that has already said no, which is
// the fallback this feature forbids, and it would double-count the gap.
func TestEmitGated_DoesNotRetryTheSink(t *testing.T) {
	for _, p := range []FailurePolicy{FailClosed, FailOpen} {
		sink := &countingSink{err: errors.New("disk full")}
		_ = EmitGated(context.Background(), sink, "so-auth", p, gateEvent())
		if sink.calls != 1 {
			t.Fatalf("policy %v: sink was written %d times, want exactly 1", p, sink.calls)
		}
	}
}

// countingSink counts Emit calls and fails them all. It is used ONLY for the
// call-count property, which a real sink cannot report; every behavioural
// assertion above runs against a real ChainFileLogger.
type countingSink struct {
	err   error
	calls int
}

func (s *countingSink) Emit(context.Context, AuditEvent) error {
	s.calls++
	return s.err
}

func (s *countingSink) Query(context.Context, Filter) (*Page, error) { return &Page{}, nil }

func (s *countingSink) Close() error { return nil }

var _ AuditLogger = (*countingSink)(nil)
