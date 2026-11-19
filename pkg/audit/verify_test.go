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

package audit

import (
	"context"
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// noVerifyLogger is a minimal AuditLogger that does NOT implement Verifier —
// standing in for RingBuffer, NoopLogger, and SlogSink.
type noVerifyLogger struct{}

func (noVerifyLogger) Emit(_ context.Context, _ AuditEvent) error       { return nil }
func (noVerifyLogger) Query(_ context.Context, _ Filter) (*Page, error) { return &Page{Total: -1}, nil }
func (noVerifyLogger) Close() error                                     { return nil }

var _ AuditLogger = noVerifyLogger{}

// verifyingLogger is an AuditLogger + Verifier double whose VerifyChainSet
// result is entirely test-controlled.
type verifyingLogger struct {
	noVerifyLogger
	verifyFn func(ctx context.Context) (*ChainVerification, error)
}

func (v *verifyingLogger) VerifyChainSet(ctx context.Context) (*ChainVerification, error) {
	return v.verifyFn(ctx)
}

var _ Verifier = (*verifyingLogger)(nil)

// ---------------------------------------------------------------------------
// Verify
// ---------------------------------------------------------------------------

func TestVerify_UnsupportedBackend(t *testing.T) {
	_, err := Verify(context.Background(), noVerifyLogger{})
	var unsupported *VerificationUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected *VerificationUnsupportedError, got %v", err)
	}
	if unsupported.Error() == "" {
		t.Error("expected a non-empty error message")
	}
}

func TestVerify_Verified(t *testing.T) {
	l := &verifyingLogger{verifyFn: func(_ context.Context) (*ChainVerification, error) {
		return &ChainVerification{
			Files: 3, Records: 42, Genesis: true,
			Order: []string{"a.ndjson", "b.ndjson"}, Seed: "", Handoff: "cafe",
		}, nil
	}}
	report, err := Verify(context.Background(), l)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.Verified {
		t.Error("expected Verified=true")
	}
	if report.Reason != "" {
		t.Errorf("expected empty Reason, got %q", report.Reason)
	}
	if report.Files != 3 || report.Records != 42 || !report.Genesis {
		t.Errorf("unexpected report: %+v", report)
	}
	if report.Handoff != "cafe" {
		t.Errorf("expected Handoff=cafe, got %q", report.Handoff)
	}
}

// TestVerify_ChainBroken asserts that a *ChainBrokenError is folded into
// the report as a tamper verdict, not returned as an error.
func TestVerify_ChainBroken(t *testing.T) {
	l := &verifyingLogger{verifyFn: func(_ context.Context) (*ChainVerification, error) {
		return &ChainVerification{Files: 1, Records: 5},
			&ChainBrokenError{Path: "audit-events.ndjson", Index: 2, Expected: "aa", Got: "bb"}
	}}
	report, err := Verify(context.Background(), l)
	if err != nil {
		t.Fatalf("expected a report carrying the verdict, got error: %v", err)
	}
	if report.Verified {
		t.Error("expected Verified=false")
	}
	if report.Reason == "" {
		t.Error("expected a non-empty Reason")
	}
	if report.Files != 1 || report.Records != 5 {
		t.Errorf("expected the partial ChainVerification to be preserved: %+v", report)
	}
}

// TestVerify_ChainLink asserts that a *ChainLinkError is likewise folded
// into the report as a tamper verdict, not returned as an error.
func TestVerify_ChainLink(t *testing.T) {
	l := &verifyingLogger{verifyFn: func(_ context.Context) (*ChainVerification, error) {
		return &ChainVerification{Files: 2},
			&ChainLinkError{Path: "a.ndjson", Reason: LinkNoPredecessor}
	}}
	report, err := Verify(context.Background(), l)
	if err != nil {
		t.Fatalf("expected a report carrying the verdict, got error: %v", err)
	}
	if report.Verified {
		t.Error("expected Verified=false")
	}
	if report.Reason == "" {
		t.Error("expected a non-empty Reason")
	}
}

// TestVerify_InfrastructureErrorsPropagate asserts that errors which answer
// no question about the trail's integrity are returned as errors, not
// folded into the report where they would read as a tamper verdict.
func TestVerify_InfrastructureErrorsPropagate(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"concurrent rotation", &ConcurrentRotationError{Path: "p", Attempts: 5}},
		{"file error", &FileError{Path: "p", Err: errors.New("boom")}},
		{"file locked", &FileLockedError{Path: "p"}},
		{"verification canceled", &VerificationCanceledError{Path: "p", Err: context.Canceled}},
		{"verification busy", &VerificationBusyError{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := &verifyingLogger{verifyFn: func(_ context.Context) (*ChainVerification, error) {
				return nil, tc.err
			}}
			report, err := Verify(context.Background(), l)
			if err == nil {
				t.Fatalf("expected the infrastructure error to propagate, got report %+v", report)
			}
			if !errors.Is(err, tc.err) {
				t.Errorf("expected the original error to propagate unwrapped, got %v", err)
			}
			if report != nil {
				t.Errorf("expected a nil report on an infrastructure error, got %+v", report)
			}
		})
	}
}

// TestVerificationErrors_Error asserts each verification-path error names
// its origin and, where it has one, the trail it concerns.
func TestVerificationErrors_Error(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"file locked", &FileLockedError{Path: "/trail/audit.log"}, "audit: file locked: /trail/audit.log"},
		{"verification canceled", &VerificationCanceledError{Path: "/trail/audit.log", Err: context.Canceled},
			"audit: verification of /trail/audit.log canceled: context canceled"},
		{"verification busy", &VerificationBusyError{}, "audit: a chain verification is already in progress"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Fatalf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// aggregateLogger.VerifyChainSet
// ---------------------------------------------------------------------------

func TestAggregateLogger_VerifyChainSet_DelegatesToVerifyingChild(t *testing.T) {
	called := false
	verifying := &verifyingLogger{verifyFn: func(_ context.Context) (*ChainVerification, error) {
		called = true
		return &ChainVerification{Files: 1, Records: 1, Genesis: true}, nil
	}}
	agg := NewAggregateLogger(NewRingBuffer(10), verifying, NewSlogSink())

	report, err := Verify(context.Background(), agg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected the verifying child's VerifyChainSet to be called")
	}
	if !report.Verified {
		t.Error("expected Verified=true")
	}
}

// TestAggregateLogger_VerifyChainSet_FirstVerifyingChildWins asserts that
// when multiple children implement Verifier, the FIRST one in configuration
// order answers -- mirroring cmd/qrdbd wiring the ChainFileLogger first.
func TestAggregateLogger_VerifyChainSet_FirstVerifyingChildWins(t *testing.T) {
	firstCalled, secondCalled := false, false
	first := &verifyingLogger{verifyFn: func(_ context.Context) (*ChainVerification, error) {
		firstCalled = true
		return &ChainVerification{Files: 1}, nil
	}}
	second := &verifyingLogger{verifyFn: func(_ context.Context) (*ChainVerification, error) {
		secondCalled = true
		return &ChainVerification{Files: 2}, nil
	}}
	agg := NewAggregateLogger(first, second)

	report, err := Verify(context.Background(), agg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !firstCalled || secondCalled {
		t.Errorf("expected only the first verifying child to be called, got first=%v second=%v",
			firstCalled, secondCalled)
	}
	if report.Files != 1 {
		t.Errorf("expected the first child's result, got %+v", report)
	}
}

func TestAggregateLogger_VerifyChainSet_NoVerifyingChild(t *testing.T) {
	agg := NewAggregateLogger(NewRingBuffer(10), NewSlogSink(), NewNoopLogger())

	_, err := Verify(context.Background(), agg)
	var unsupported *VerificationUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected *VerificationUnsupportedError, got %v", err)
	}
}

// TestAggregateLogger_VerifyChainSet_FindsNestedVerifyingGrandchild pins
// go-qrdb#453's review finding: a NESTED aggregate implements Verifier
// unconditionally, once the method exists on the type, even when none of ITS
// OWN children can verify. NewAggregateLogger(NewAggregateLogger(ring),
// chainFile) must not stop at the inner aggregate on that basis alone and
// report VerificationUnsupportedError -- it must keep looking and find
// chainFile, the sibling one level up.
func TestAggregateLogger_VerifyChainSet_FindsNestedVerifyingGrandchild(t *testing.T) {
	inner := NewAggregateLogger(NewRingBuffer(10)) // implements Verifier, but has no real sink
	called := false
	verifying := &verifyingLogger{verifyFn: func(_ context.Context) (*ChainVerification, error) {
		called = true
		return &ChainVerification{Files: 1, Records: 1, Genesis: true}, nil
	}}
	outer := NewAggregateLogger(inner, verifying)

	report, err := Verify(context.Background(), outer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected the outer aggregate to skip the sink-less nested aggregate and reach the real verifying child")
	}
	if !report.Verified {
		t.Error("expected Verified=true")
	}
}

// TestAggregateLogger_VerifyChainSet_AllNestedSinkless is the negative
// control: an aggregate nested arbitrarily deep, with no real verifying sink
// anywhere in the tree, must still resolve to VerificationUnsupportedError
// rather than delegating to a level that answers "unsupported" on its own
// and mistaking that for the tree's final answer at a level that DID have
// alternatives.
func TestAggregateLogger_VerifyChainSet_AllNestedSinkless(t *testing.T) {
	innermost := NewAggregateLogger(NewRingBuffer(10), NewSlogSink())
	outer := NewAggregateLogger(NewNoopLogger(), innermost)

	_, err := Verify(context.Background(), outer)
	var unsupported *VerificationUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected *VerificationUnsupportedError, got %v", err)
	}
}
