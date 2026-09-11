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

// The SO-PIN verification gate fails closed (go-qrdb#445).
//
// NO BUILD CONSTRAINT, deliberately, matching pkg/auth's other test files and
// pkg/auth/middleware/audit_failure_test.go: this is the proof for a security
// property that ships in every build. Every helper it uses is either defined
// in this file or is real production code (kvstore.NewMemoryKVStore,
// audit.NewFileLogger), so nothing here straddles a tag.
//
// THE DOUBLES ARE THE REAL THINGS. The property is "the operation is refused
// because the record did not reach the sink's durability boundary", and a
// stubbed sink that returns a canned error proves only that the branch runs.
// These drive a real *auth.SOAuthenticator over a real
// *kvstore.MemoryKVStore, recording into a real *audit.ChainFileLogger on a
// real file, faulted at os.File.Write.
//
// NO PIN VALUE IS EVER LOGGED by these tests. The PINs below are literals in
// the source; they are not echoed into t.Logf, and the production code they
// exercise wipes them into a secure.Buffer on entry.

package auth_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

const (
	soTestTenant = "t1"
	soTestPIN    = "correct horse battery staple"
	soWrongPIN   = "hunter2 and then some"
)

// soChain returns a REAL ChainFileLogger on a real file, plus the path.
func soChain(t *testing.T) (*audit.ChainFileLogger, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit-events.ndjson")
	chain, err := audit.NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	t.Cleanup(func() { _ = chain.Close() })
	return chain, path
}

// newGatedSOAuth builds a real SOAuthenticator over a real in-memory KV store and
// the given sink, with the PIN already committed for soTestTenant.
//
// The commit happens BEFORE any fault is injected, so every test below starts
// from a node whose SO PIN is genuinely set -- which is what makes "the PIN was
// correct and the verification still refused" a meaningful assertion.
func newGatedSOAuth(t *testing.T, sink audit.AuditLogger, opts ...auth.SOOption) *auth.SOAuthenticator {
	t.Helper()
	a, err := auth.NewSOAuthenticator(kvstore.NewMemoryKVStore(), sink, slog.Default(), opts...)
	if err != nil {
		t.Fatalf("NewSOAuthenticator: %v", err)
	}
	if err := a.SetSOPIN(context.Background(), soTestTenant, []byte(soTestPIN)); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}
	return a
}

// ---------------------------------------------------------------------------
// THE PROOF
//
// Before go-qrdb#445 this sequence returned nil -- the SO factor SUCCEEDED,
// its record failed to write, and the caller was told the second factor had
// passed. That is the defect, and this is the test that would have caught it.
// ---------------------------------------------------------------------------

// TestVerifySOPIN_RealChainFileFailure_RefusesWithGateError: a real,
// CORRECT PIN, verified against a real commit, over a real file sink that
// cannot write, is refused with an *audit.GateError that unwraps to an
// *audit.WriteError naming the file and onward to the OS's own refusal.
func TestVerifySOPIN_RealChainFileFailure_RefusesWithGateError(t *testing.T) {
	chain, path := soChain(t)
	a := newGatedSOAuth(t, chain)

	// Fault the sink the way the daemon's fails while in-flight requests are
	// still draining: the file handle goes, the append fails at
	// os.File.Write.
	if err := chain.Close(); err != nil {
		t.Fatalf("close (the injected fault): %v", err)
	}

	err := a.VerifySOPIN(context.Background(), soTestTenant, []byte(soTestPIN))
	if err == nil {
		t.Fatal("an SO factor that could not be recorded must be refused")
	}

	var gateErr *audit.GateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("expected *audit.GateError, got %T: %v", err, err)
	}
	if gateErr.Component != "so-auth" {
		t.Errorf("Component = %q, want %q", gateErr.Component, "so-auth")
	}
	var writeErr *audit.WriteError
	if !errors.As(err, &writeErr) {
		t.Fatalf("GateError must unwrap to *audit.WriteError, got %v", err)
	}
	if writeErr.Backend != path {
		t.Errorf("WriteError.Backend = %q, want the failing audit file %q", writeErr.Backend, path)
	}
	if !errors.Is(err, os.ErrClosed) {
		t.Errorf("the OS error must survive every wrap, got %v", err)
	}

	// AND NOT ErrSOPINInvalid. Flattening the two would tell a security
	// officer holding a perfectly good PIN that the PIN was wrong, on a node
	// whose audit disk was full -- terminal in every client.
	if errors.Is(err, auth.ErrSOPINInvalid) {
		t.Error("an audit outage must not be reported as an invalid PIN")
	}
}

// TestVerifySOPIN_HealthyChainFileSucceedsAndRecords is the CONTROL. Without
// it a VerifySOPIN that refused everything unconditionally would pass the test
// above.
func TestVerifySOPIN_HealthyChainFileSucceedsAndRecords(t *testing.T) {
	chain, path := soChain(t)
	a := newGatedSOAuth(t, chain)

	if err := a.VerifySOPIN(context.Background(), soTestTenant, []byte(soTestPIN)); err != nil {
		t.Fatalf("a recordable verification must succeed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the trail: %v", err)
	}
	trail := string(data)
	if !strings.Contains(trail, "so-pin") || !strings.Contains(trail, "verify") {
		t.Fatalf("the verification is not in the trail: %q", trail)
	}
	if strings.Contains(trail, soTestPIN) {
		t.Fatal("the PIN itself must never reach the audit trail")
	}
}

// TestVerifySOPIN_DefaultPostureIsFailClosed: an SOAuthenticator built the
// ordinary way, WITHOUT any policy option, refuses. The default IS the
// security property, so it is asserted directly rather than inherited from the
// option that sets it.
func TestVerifySOPIN_DefaultPostureIsFailClosed(t *testing.T) {
	chain, _ := soChain(t)
	a := newGatedSOAuth(t, chain) // no auth.WithSOAuditFailurePolicy
	if err := chain.Close(); err != nil {
		t.Fatalf("close (the injected fault): %v", err)
	}

	err := a.VerifySOPIN(context.Background(), soTestTenant, []byte(soTestPIN))
	var gateErr *audit.GateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("the DEFAULT posture must refuse; got %T: %v", err, err)
	}
}

// TestVerifySOPIN_FailOpenProceeds is the documented escape, on the SAME
// operator lever the authorization middleware reads. The record is still lost
// and still counted; what changes is that the officer is served.
func TestVerifySOPIN_FailOpenProceeds(t *testing.T) {
	chain, _ := soChain(t)
	a := newGatedSOAuth(t, chain, auth.WithSOAuditFailurePolicy(audit.FailOpen))
	if err := chain.Close(); err != nil {
		t.Fatalf("close (the injected fault): %v", err)
	}

	if err := a.VerifySOPIN(context.Background(), soTestTenant, []byte(soTestPIN)); err != nil {
		t.Fatalf("fail-open must let the verification stand, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// THE REFUSAL BRANCHES ARE GATED TOO
//
// "We answer only what we could record" is one rule a reader can check. "We
// answer only what we could record, except when we were going to refuse
// anyway" is two rules that will drift. Both refusals are therefore reported
// as audit failures rather than as PIN verdicts -- the caller is refused
// either way, and under an audit outage "retry" is the honest instruction.
// ---------------------------------------------------------------------------

func TestVerifySOPIN_InvalidPINUnrecordable_ReportsTheAuditFailure(t *testing.T) {
	chain, _ := soChain(t)
	a := newGatedSOAuth(t, chain)
	if err := chain.Close(); err != nil {
		t.Fatalf("close (the injected fault): %v", err)
	}

	err := a.VerifySOPIN(context.Background(), soTestTenant, []byte(soWrongPIN))
	var gateErr *audit.GateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("expected *audit.GateError, got %T: %v", err, err)
	}
}

// TestVerifySOPIN_InvalidPINRecordable_IsStillErrSOPINInvalid is the control
// for the test above: with a healthy sink a wrong PIN is still a wrong PIN.
func TestVerifySOPIN_InvalidPINRecordable_IsStillErrSOPINInvalid(t *testing.T) {
	chain, _ := soChain(t)
	a := newGatedSOAuth(t, chain)

	err := a.VerifySOPIN(context.Background(), soTestTenant, []byte(soWrongPIN))
	if !errors.Is(err, auth.ErrSOPINInvalid) {
		t.Fatalf("a wrong PIN on a healthy node is ErrSOPINInvalid, got %T: %v", err, err)
	}
}

func TestVerifySOPIN_NotSetUnrecordable_ReportsTheAuditFailure(t *testing.T) {
	chain, _ := soChain(t)
	a, err := auth.NewSOAuthenticator(kvstore.NewMemoryKVStore(), chain, slog.Default())
	if err != nil {
		t.Fatalf("NewSOAuthenticator: %v", err)
	}
	if err := chain.Close(); err != nil {
		t.Fatalf("close (the injected fault): %v", err)
	}

	verr := a.VerifySOPIN(context.Background(), "never-set", []byte(soTestPIN))
	var gateErr *audit.GateError
	if !errors.As(verr, &gateErr) {
		t.Fatalf("expected *audit.GateError, got %T: %v", verr, verr)
	}
}

// TestVerifySOPIN_NotSetRecordable_IsStillErrSOPINNotSet is that test's
// control.
func TestVerifySOPIN_NotSetRecordable_IsStillErrSOPINNotSet(t *testing.T) {
	chain, _ := soChain(t)
	a, err := auth.NewSOAuthenticator(kvstore.NewMemoryKVStore(), chain, slog.Default())
	if err != nil {
		t.Fatalf("NewSOAuthenticator: %v", err)
	}

	verr := a.VerifySOPIN(context.Background(), "never-set", []byte(soTestPIN))
	if !errors.Is(verr, auth.ErrSOPINNotSet) {
		t.Fatalf("want ErrSOPINNotSet on a healthy node, got %T: %v", verr, verr)
	}
}

// ---------------------------------------------------------------------------
// ROTATION: THE GATE IS FAIL-CLOSED, THE MUTATION RECORD IS NOT
// ---------------------------------------------------------------------------

// TestRotateSOPIN_InheritsTheVerificationGate: the old-PIN check delegates to
// VerifySOPIN, so an unrecordable verification refuses the rotation BEFORE
// anything is written. The assertion that matters is the second one: the
// commit did not happen.
func TestRotateSOPIN_InheritsTheVerificationGate(t *testing.T) {
	chain, _ := soChain(t)
	store := kvstore.NewMemoryKVStore()
	a, err := auth.NewSOAuthenticator(store, chain, slog.Default())
	if err != nil {
		t.Fatalf("NewSOAuthenticator: %v", err)
	}
	ctx := context.Background()
	if err := a.SetSOPIN(ctx, soTestTenant, []byte(soTestPIN)); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}
	before, err := store.Get(ctx, "__sys:so-pin-commit/"+soTestTenant)
	if err != nil {
		t.Fatalf("read the commit: %v", err)
	}
	if err := chain.Close(); err != nil {
		t.Fatalf("close (the injected fault): %v", err)
	}

	rerr := a.RotateSOPIN(ctx, soTestTenant, []byte(soTestPIN), []byte(soWrongPIN))
	var gateErr *audit.GateError
	if !errors.As(rerr, &gateErr) {
		t.Fatalf("expected *audit.GateError, got %T: %v", rerr, rerr)
	}
	after, err := store.Get(ctx, "__sys:so-pin-commit/"+soTestTenant)
	if err != nil {
		t.Fatalf("re-read the commit: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("the rotation committed despite being refused at the gate")
	}
}

// TestSetSOPIN_PostCommitRecordStaysBestEffort pins the DELIBERATE half of
// this split, so a later reader does not "fix" it into a gate.
//
// SetSOPIN is an administrative MUTATION, not a gate: its success record is
// written after store.Put has already replicated the commit. Propagating a
// failed write there would report a completed operation as failed -- the
// operator retries, gets ErrSOPINAlreadySet, and now believes the PIN is both
// unset and already set. There is no rollback an emitter may perform, so the
// honest posture is EmitBestEffort's: log, count
// qrdb_audit_write_failures_total{component="so-auth"}, and let the truthful
// result stand.
//
// The test asserts BOTH halves of that claim: the call reports success, AND
// the PIN really is committed.
func TestSetSOPIN_PostCommitRecordStaysBestEffort(t *testing.T) {
	chain, _ := soChain(t)
	store := kvstore.NewMemoryKVStore()
	a, err := auth.NewSOAuthenticator(store, chain, slog.Default())
	if err != nil {
		t.Fatalf("NewSOAuthenticator: %v", err)
	}
	if err := chain.Close(); err != nil {
		t.Fatalf("close (the injected fault): %v", err)
	}

	ctx := context.Background()
	if err := a.SetSOPIN(ctx, soTestTenant, []byte(soTestPIN)); err != nil {
		t.Fatalf("a committed PIN must not be reported as failed: %v", err)
	}
	record, err := store.Get(ctx, "__sys:so-pin-commit/"+soTestTenant)
	if err != nil || len(record) == 0 {
		t.Fatalf("the PIN was reported set and is not in the store (err=%v)", err)
	}

	// And the operation really did complete, which is why refusing would have
	// been a lie: a second SetSOPIN is rejected as already set.
	if err := a.SetSOPIN(ctx, soTestTenant, []byte(soWrongPIN)); !errors.Is(err, auth.ErrSOPINAlreadySet) {
		t.Fatalf("want ErrSOPINAlreadySet, got %v", err)
	}
}
