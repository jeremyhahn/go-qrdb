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

// The audit-failure posture (go-qrdb#335).
//
// NO BUILD CONSTRAINT, deliberately. This is the proof for a security
// property that ships in every build, and the helpers it uses --
// testRESTRoutes (rest_routes_helper_test.go), mockAuthenticator /
// mockAuthorizer / mockAuditLogger (middleware_test.go) and
// restErrorResponse (rest.go) -- are all unconstrained, so nothing here
// straddles a tag. Constraining this file would delete the proof from the
// tag sets that still enforce the behaviour.

package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/util/metrics"
)

// ---------------------------------------------------------------------------
// THE DEFAULT IS THE SECURITY PROPERTY
//
// These assert the default directly rather than inheriting it from a struct
// literal nobody checks. A security default that is only ever observed
// through the option that sets it is a default that can silently flip.
// ---------------------------------------------------------------------------

// TestAuthMiddleware_ZeroValueIsFailClosed pins the representation, not only
// the behaviour. A zero AuthMiddleware -- built by a future constructor, a
// test, or an embedder that never mentions the field -- must be fail-closed by
// construction.
//
// The posture TYPE's own zero value, its spellings and its parser are pinned
// where they live, in pkg/audit (gate_test.go). They moved there with the type
// in go-qrdb#445, because pkg/auth/middleware imports pkg/auth and a policy
// owned here could never have reached the security-officer gate.
func TestAuthMiddleware_ZeroValueIsFailClosed(t *testing.T) {
	var m AuthMiddleware
	if m.auditFailure != audit.FailClosed {
		t.Fatalf("a zero AuthMiddleware is %v, want audit.FailClosed", m.auditFailure)
	}
}

// TestAuthorize_DefaultPolicyFailsClosed is the one that matters: a
// middleware built the ordinary way, with an audit logger and WITHOUT any
// policy option, refuses a decision it could not record.
func TestAuthorize_DefaultPolicyFailsClosed(t *testing.T) {
	m := New(&mockAuthorizer{}, WithAuditLogger(&mockAuditLogger{err: errors.New("disk full")}))

	if m.auditFailure != audit.FailClosed {
		t.Fatalf("unset policy resolved to %v, want audit.FailClosed", m.auditFailure)
	}

	err := m.authorize(context.Background(),
		&auth.Identity{Subject: "alice", TenantID: "t1"}, permission.KVPut)
	var auditErr *audit.GateError
	if !errors.As(err, &auditErr) {
		t.Fatalf("the DEFAULT posture must refuse; got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// The deliberate escape, and its warning
// ---------------------------------------------------------------------------

// TestWithAuditFailurePolicy_FailOpenWarnsAtStartup: the option is the one
// place the operator's choice becomes a running middleware, so it is where
// the warning belongs -- once per startup, every startup. A security posture
// that can be weakened silently will be.
func TestWithAuditFailurePolicy_FailOpenWarnsAtStartup(t *testing.T) {
	var buf bytes.Buffer
	restore := swapDefaultLogger(t, &buf)
	defer restore()

	m := New(&mockAuthorizer{},
		WithAuditLogger(&mockAuditLogger{}),
		WithAuditFailurePolicy(audit.FailOpen))

	if m.auditFailure != audit.FailOpen {
		t.Fatalf("policy = %v, want audit.FailOpen", m.auditFailure)
	}

	out := buf.String()
	if !strings.Contains(out, "level=WARN") {
		t.Fatalf("selecting fail-open must WARN; log was %q", out)
	}
	for _, want := range []string{"FAIL-OPEN", "PERMITTED", "audit record"} {
		if !strings.Contains(out, want) {
			t.Errorf("the warning must name the property given up (missing %q): %q", want, out)
		}
	}
}

// TestWithAuditFailurePolicy_FailClosedIsSilent: the secure default is not a
// warning. Warning on it would train operators to ignore the one that matters.
func TestWithAuditFailurePolicy_FailClosedIsSilent(t *testing.T) {
	var buf bytes.Buffer
	restore := swapDefaultLogger(t, &buf)
	defer restore()

	New(&mockAuthorizer{}, WithAuditLogger(&mockAuditLogger{}), WithAuditFailurePolicy(audit.FailClosed))

	if strings.Contains(buf.String(), "WARN") {
		t.Fatalf("fail-closed must not warn; log was %q", buf.String())
	}
}

// ---------------------------------------------------------------------------
// THE END-TO-END PROOF
//
// A real request, a real AuthMiddleware, a real audit.ChainFileLogger on a
// real file, and a real write failure at os.File.Write. The point of
// go-qrdb#335 is that audit.WriteError and middleware.AuditError were types
// that existed, had tests, and could not be produced by any code path. A test
// that constructs one proves the constructor works; only this proves a
// request produces one.
// ---------------------------------------------------------------------------

// faultedChainSink returns a decision logger over a REAL ChainFileLogger
// whose file has been closed, which is how the daemon's audit sink fails
// while in-flight requests are still draining. The append then fails at
// os.File.Write and the sink returns *audit.WriteError.
func faultedChainSink(t *testing.T) (audit.Logger, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit-events.ndjson")
	chain, err := audit.NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	if err := chain.Close(); err != nil {
		t.Fatalf("close (the injected fault): %v", err)
	}
	return audit.NewDecisionLogger(chain), path
}

// TestAuthorize_RealChainFileFailure_DeniesWithAuditErrorWrappingWriteError
// is the assertion the issue asks for, spelled out: a real authorization
// call, over a real file sink that cannot write, is refused with an
// *audit.GateError that unwraps to an *audit.WriteError naming the file.
func TestAuthorize_RealChainFileFailure_DeniesWithAuditErrorWrappingWriteError(t *testing.T) {
	sink, path := faultedChainSink(t)
	m := New(&mockAuthorizer{}, WithAuditLogger(sink))

	err := m.authorize(context.Background(),
		&auth.Identity{Subject: "alice", TenantID: "t1"}, permission.KVPut)
	if err == nil {
		t.Fatal("a grant that could not be recorded must be refused")
	}

	var auditErr *audit.GateError
	if !errors.As(err, &auditErr) {
		t.Fatalf("expected *audit.GateError, got %T: %v", err, err)
	}
	var writeErr *audit.WriteError
	if !errors.As(err, &writeErr) {
		t.Fatalf("AuditError must unwrap to *audit.WriteError, got %v", err)
	}
	if writeErr.Backend != path {
		t.Errorf("WriteError.Backend = %q, want the failing audit file %q", writeErr.Backend, path)
	}
	// The chain reaches all the way to the operating system's own refusal,
	// so nothing in the middle invented the failure.
	//
	// os.ErrClosed, previously os.ErrInvalid: the sink now refuses at
	// ensureOpen, which distinguishes "the owner closed this" from "a failed
	// rotation lost the handle" and reopens only the second. Before that it
	// fell through to (*os.File)(nil).Write, whose ErrInvalid said nothing
	// about which of the two had happened.
	if !errors.Is(err, os.ErrClosed) {
		t.Errorf("the OS error must survive both wraps, got %v", err)
	}
	t.Logf("denied request produced: %v", err)
}

// TestRESTMiddleware_RealChainFileFailure_Returns503 drives the same fault
// through an actual HTTP request. The handler must never run: the whole point
// is that the operation does not happen unless its record did.
func TestRESTMiddleware_RESTRequestDeniedByAuditFailure(t *testing.T) {
	sink, _ := faultedChainSink(t)
	id := &auth.Identity{Subject: "alice", TenantID: "t1"}
	m := New(&mockAuthorizer{},
		WithAuthenticator(&mockAuthenticator{name: "test-authn", identity: id}),
		WithAuditLogger(sink))

	handlerRan := false
	handler := m.RESTMiddleware(testRESTRoutes(), http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		handlerRan = true
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))

	if handlerRan {
		t.Fatal("the handler ran: the request was served without an audit record")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (retryable server-side condition, not a denial)", rec.Code)
	}
	var body restErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if !strings.Contains(body.Error, "audit log failed") {
		t.Errorf("the response must say why, got %q", body.Error)
	}
	t.Logf("HTTP %d %s", rec.Code, body.Error)
}

// TestRESTMiddleware_RealChainFileSuccess_ServesAndRecords is the control:
// the SAME wiring with a healthy file sink serves the request and leaves the
// record on disk. Without it, a middleware that denied everything
// unconditionally would pass the test above.
func TestRESTMiddleware_HealthyChainFileServesAndRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit-events.ndjson")
	chain, err := audit.NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	defer func() { _ = chain.Close() }()

	id := &auth.Identity{Subject: "alice", TenantID: "t1"}
	m := New(&mockAuthorizer{},
		WithAuthenticator(&mockAuthenticator{name: "test-authn", identity: id}),
		WithAuditLogger(audit.NewDecisionLogger(chain)))

	handlerRan := false
	handler := m.RESTMiddleware(testRESTRoutes(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerRan = true
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))

	if !handlerRan || rec.Code != http.StatusOK {
		t.Fatalf("a recordable decision must be served; ran=%v code=%d", handlerRan, rec.Code)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read the trail: %v", readErr)
	}
	if !strings.Contains(string(data), "auth.authorize") {
		t.Fatalf("the decision is not in the trail: %q", string(data))
	}
}

// TestGRPCAuthError_AuditErrorMapsToUnavailable: the gRPC half of the same
// mapping. ResourceExhausted or PermissionDenied would tell a client library
// never to retry something a recovered audit sink answers perfectly well.
func TestGRPCAuthError_AuditErrorMapsToUnavailable(t *testing.T) {
	err := mapGRPCAuthError(&audit.GateError{Err: &audit.WriteError{
		Backend: "audit-events.ndjson", Err: errors.New("no space left on device"),
	}})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected a gRPC status, got %T", err)
	}
	if st.Code() != codes.Unavailable {
		t.Errorf("code = %v, want Unavailable", st.Code())
	}
}

func TestHTTPAuthError_AuditErrorMapsTo503(t *testing.T) {
	got := mapHTTPAuthError(&audit.GateError{Err: &audit.WriteError{
		Backend: "audit-events.ndjson", Err: errors.New("no space left on device"),
	}})
	if got != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", got)
	}
}

// ---------------------------------------------------------------------------
// THE AUDIT OF THE AUDIT
//
// The refusal caused by an audit outage cannot itself be audited -- the only
// sink is the one that just failed. These assert the two planes that do not
// depend on the audit plane: the counter and the operational log.
// ---------------------------------------------------------------------------

// TestAuditDecision_FailClosedIsCountedAndLogged. Without this, a security
// control that stops working when its recorder is down does so in silence,
// which is the worst case.
func TestAuditDecision_FailClosedIsCountedAndLogged(t *testing.T) {
	var buf bytes.Buffer
	restore := swapDefaultLogger(t, &buf)
	defer restore()

	before := auditFailureCount(t, auditOutcomeDenied)

	m := New(&mockAuthorizer{}, WithAuditLogger(&mockAuditLogger{err: errors.New("disk full")}))
	_ = m.authorize(context.Background(), &auth.Identity{Subject: "alice", TenantID: "t1"}, permission.KVPut)

	if after := auditFailureCount(t, auditOutcomeDenied); after != before+1 {
		t.Fatalf(`qrdb_auth_audit_write_failures_total{outcome="denied"} = %v, want %v`, after, before+1)
	}
	out := buf.String()
	if !strings.Contains(out, "audit log failed") {
		t.Fatalf("the refusal must reach the operational log stream: %q", out)
	}
	if !strings.Contains(out, "policy=fail-closed") {
		t.Errorf("the log must name the posture that produced the refusal: %q", out)
	}
}

// TestAuditDecision_FailOpenIsCounted: the fail-open counter is the exact
// size of the gap the operator accepted in the trail.
func TestAuditDecision_FailOpenIsCounted(t *testing.T) {
	before := auditFailureCount(t, auditOutcomePermitted)

	m := New(&mockAuthorizer{},
		WithAuditLogger(&mockAuditLogger{err: errors.New("disk full")}),
		WithAuditFailurePolicy(audit.FailOpen))
	if err := m.authorize(context.Background(),
		&auth.Identity{Subject: "alice", TenantID: "t1"}, permission.KVPut); err != nil {
		t.Fatalf("fail-open must permit: %v", err)
	}

	if after := auditFailureCount(t, auditOutcomePermitted); after != before+1 {
		t.Fatalf(`qrdb_auth_audit_write_failures_total{outcome="permitted"} = %v, want %v`, after, before+1)
	}
}

// TestAuditDecision_SuccessIsNotCounted: the counter must mean "the trail has
// a hole", not "a decision happened".
func TestAuditDecision_SuccessIsNotCounted(t *testing.T) {
	before := auditFailureCount(t, auditOutcomeDenied)

	m := New(&mockAuthorizer{}, WithAuditLogger(&mockAuditLogger{}))
	if err := m.authorize(context.Background(),
		&auth.Identity{Subject: "alice", TenantID: "t1"}, permission.KVPut); err != nil {
		t.Fatalf("a recordable decision must stand: %v", err)
	}

	if after := auditFailureCount(t, auditOutcomeDenied); after != before {
		t.Fatalf("a successful write moved the failure counter: %v -> %v", before, after)
	}
}

func TestAuditFailureOutcomes_CoverEveryPolicy(t *testing.T) {
	for _, p := range []audit.FailurePolicy{audit.FailClosed, audit.FailOpen} {
		if _, ok := auditFailureOutcomes[p]; !ok {
			t.Errorf("no metric label for policy %v: the counter and the branch have drifted", p)
		}
	}
}

// ---------------------------------------------------------------------------
// The other authorize() exits
// ---------------------------------------------------------------------------

// TestAuthorize_IdentityGated_FailsClosedOnAuditFailure. The IdentityGated
// surface is the sealed-window bootstrap path, where the handler enforces the
// real binding check -- but the DECISION to let the caller reach that handler
// is still an authorization decision, and it is still audited.
func TestAuthorize_IdentityGated_FailsClosedOnAuditFailure(t *testing.T) {
	m := New(&mockAuthorizer{}, WithAuditLogger(&mockAuditLogger{err: errors.New("disk full")}))

	err := m.authorize(context.Background(),
		&auth.Identity{Subject: "so", TenantID: "t1"}, permission.IdentityGated)
	var auditErr *audit.GateError
	if !errors.As(err, &auditErr) {
		t.Fatalf("expected *audit.GateError, got %T: %v", err, err)
	}
}

func TestAuthorize_IdentityGated_SucceedsWhenRecordable(t *testing.T) {
	m := New(&mockAuthorizer{}, WithAuditLogger(&mockAuditLogger{}))
	if err := m.authorize(context.Background(),
		&auth.Identity{Subject: "so", TenantID: "t1"}, permission.IdentityGated); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestAuthorize_NoAuditLogger_NeverRefuses: a middleware with no audit logger
// has nothing that could fail, so the posture must be inert. Otherwise every
// embedder that wires no audit sink would deny every request.
func TestAuthorize_NoAuditLogger_NeverRefuses(t *testing.T) {
	for _, p := range []audit.FailurePolicy{audit.FailClosed, audit.FailOpen} {
		m := New(&mockAuthorizer{})
		m.auditFailure = p
		if err := m.authorize(context.Background(),
			&auth.Identity{Subject: "alice", TenantID: "t1"}, permission.KVPut); err != nil {
			t.Fatalf("policy %v with no audit logger refused: %v", p, err)
		}
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// swapDefaultLogger redirects slog.Default to buf and returns the restore
// func. The middleware logs through slog.Default, which is what an operator
// actually reads.
func swapDefaultLogger(t *testing.T, buf *bytes.Buffer) func() {
	t.Helper()
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return func() { slog.SetDefault(prev) }
}

// auditFailureCount scrapes the registry the way Prometheus does, rather than
// reaching into the counter, so the test proves the number is actually
// EXPOSED and not merely incremented.
func auditFailureCount(t *testing.T, outcome string) float64 {
	t.Helper()
	auditWriteFailures() // ensure registration
	families, err := metrics.Registry().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() != "qrdb_auth_audit_write_failures_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "outcome" && l.GetValue() == outcome {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// ---------------------------------------------------------------------------
// The nil identity
// ---------------------------------------------------------------------------

// TestAuditDecision_NilIdentityIsRecordedNotDereferenced. authorize() admits a
// nil identity -- it guards on it three times and then reaches auditDecision,
// where reading identity.Subject would panic. No transport reaches it today
// (every call site refuses a nil identity first), but a function that defends
// against nil three times and then dereferences is one edit away from being
// wrong, and this change makes a wired audit logger the universal case.
func TestAuditDecision_NilIdentityIsRecordedNotDereferenced(t *testing.T) {
	al := &mockAuditLogger{}
	m := New(&mockAuthorizer{}, WithAuditLogger(al))

	if err := m.auditDecision(context.Background(), nil, permission.KVPut, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if al.calls.Load() != 1 {
		t.Fatalf("the decision must still be RECORDED, not skipped; got %d calls", al.calls.Load())
	}
	if got := al.entries[0].Subject; got != anonymousActor {
		t.Errorf("Subject = %q, want the %q sentinel -- an empty subject already occurs "+
			"legitimately and must stay distinguishable", got, anonymousActor)
	}
	if got := al.entries[0].Resource; got != permission.KVPut.String() {
		t.Errorf("Resource = %q, want %q", got, permission.KVPut.String())
	}
}

// TestAuditDecision_NilIdentityStillFailsClosed: the posture does not depend
// on there being an identity to name.
func TestAuditDecision_NilIdentityStillFailsClosed(t *testing.T) {
	m := New(&mockAuthorizer{}, WithAuditLogger(&mockAuditLogger{err: errors.New("disk full")}))

	err := m.auditDecision(context.Background(), nil, permission.KVPut, nil)
	var auditErr *audit.GateError
	if !errors.As(err, &auditErr) {
		t.Fatalf("expected *audit.GateError, got %T: %v", err, err)
	}
}

// TestAuthorize_NilIdentityDoesNotPanic drives the whole of authorize() with a
// nil identity, which is the shape that would actually crash a node.
func TestAuthorize_NilIdentityDoesNotPanic(t *testing.T) {
	m := New(&mockAuthorizer{}, WithAuditLogger(&mockAuditLogger{}))
	// Returns whatever the authorizer says; the assertion is that it RETURNS.
	_ = m.authorize(context.Background(), nil, permission.KVPut)
}

// TestAnonymousActor_CannotBeForged: the sentinel uses characters that cannot
// appear in a certificate CN, a SPIFFE SAN or a username, so a caller cannot
// name itself this and impersonate an unattributed decision.
func TestAnonymousActor_CannotBeForged(t *testing.T) {
	if !strings.HasPrefix(anonymousActor, "<") || !strings.HasSuffix(anonymousActor, ">") {
		t.Fatalf("anonymousActor = %q; the delimiters are what make it unforgeable", anonymousActor)
	}
}

// ---------------------------------------------------------------------------
// The bootstrap gates must not treat an audit failure as an RBAC verdict
// ---------------------------------------------------------------------------

// countingBarrierStatus records whether the bootstrap fallback consulted it.
// Reaching it at all means the gate mistook "could not record" for "RBAC said
// no" and went looking for a bootstrap window.
type countingBarrierStatus struct{ calls int }

func (c *countingBarrierStatus) TenantBarrierStatus(_ context.Context, _ string) (*tenant.TenantBarrierStatus, error) {
	c.calls++
	return &tenant.TenantBarrierStatus{Initialized: false}, nil
}

// TestAuthorizeTenantBarrierInit_AuditFailureDoesNotEnterTheWindow.
//
// AuthorizeTenant can return *audit.GateError instead of a decision, and this
// function read any non-nil error as an RBAC denial -- so it entered the
// bootstrap window on a request RBAC never refused, cost a barrier-status
// call, and ran a SECOND auditDecision: another write to the sink that had
// just failed, and a second outcome="denied" increment for ONE decision.
//
// The counter's stated meaning is that a non-zero rate is the EXACT size of
// the gap in the trail. Double-counting makes it a multiple.
func TestAuthorizeTenantBarrierInit_AuditFailureDoesNotEnterTheWindow(t *testing.T) {
	al := &mockAuditLogger{err: errors.New("disk full")}
	m := New(&mockAuthorizer{}, WithAuditLogger(al))
	status := &countingBarrierStatus{}

	before := auditFailureCount(t, auditOutcomeDenied)

	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "acme-admin", TenantID: "acme"})
	err := m.AuthorizeTenantBarrierInit(ctx, "acme", status)

	var auditErr *audit.GateError
	if !errors.As(err, &auditErr) {
		t.Fatalf("expected *audit.GateError, got %T: %v", err, err)
	}
	if status.calls != 0 {
		t.Errorf("the bootstrap window was consulted %d times; an audit failure is not an RBAC verdict", status.calls)
	}
	if al.calls.Load() != 1 {
		t.Errorf("the sink was written %d times for one decision; the second is a retry on a sink that just failed", al.calls.Load())
	}
	if after := auditFailureCount(t, auditOutcomeDenied); after != before+1 {
		t.Errorf("counter moved by %v for one decision, want 1", after-before)
	}
}

func TestAuthorizeTenantBarrierSealedOp_AuditFailureDoesNotEnterTheWindow(t *testing.T) {
	al := &mockAuditLogger{err: errors.New("disk full")}
	m := New(&mockAuthorizer{}, WithAuditLogger(al))

	before := auditFailureCount(t, auditOutcomeDenied)

	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "acme-admin", TenantID: "acme"})
	err := m.AuthorizeTenantBarrierSealedOp(ctx, "acme", permission.TenantBarrierUnseal)

	var auditErr *audit.GateError
	if !errors.As(err, &auditErr) {
		t.Fatalf("expected *audit.GateError, got %T: %v", err, err)
	}
	if al.calls.Load() != 1 {
		t.Errorf("the sink was written %d times for one decision", al.calls.Load())
	}
	if after := auditFailureCount(t, auditOutcomeDenied); after != before+1 {
		t.Errorf("counter moved by %v for one decision, want 1", after-before)
	}
}

// TestIsAuditFailure_IsNarrowerThanTheUnavailableClass is the control for the
// bug I nearly introduced fixing this: guarding on AuthFailureUnavailable
// instead of *audit.GateError closes the sealed window on the very signal it exists
// to act on -- a tenant whose IAM is encrypted behind the barrier it is trying
// to unseal arrives as *StoreUnavailableError wrapping tenant.ErrTenantSealed.
func TestIsAuditFailure_IsNarrowerThanTheUnavailableClass(t *testing.T) {
	sealed := &StoreUnavailableError{Err: tenant.ErrTenantSealed}
	if ClassifyAuthError(sealed) != AuthFailureUnavailable {
		t.Fatal("precondition: the sealed-tenant signal is in the Unavailable class")
	}
	if isAuditFailure(sealed) {
		t.Fatal("the sealed-tenant signal must NOT be treated as an audit failure; " +
			"it is what opens the sealed window")
	}
	if !isAuditFailure(&audit.GateError{Err: errors.New("disk full")}) {
		t.Fatal("an *audit.GateError must be recognised")
	}
}
