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

// The SO dual-factor gate fails closed (go-qrdb#445).
//
// NO BUILD CONSTRAINT, deliberately, for the same reason
// audit_failure_test.go carries none: this is the proof for a security
// property that ships in every build, and the helpers it uses --
// testRESTRoutes (rest_routes_helper_test.go), restErrorResponse (rest.go) and
// discardLogger (below) -- are all unconstrained. Constraining this file would
// delete the proof from the tag sets that still enforce the behaviour.
//
// discardLogger LIVES HERE rather than in so_middleware_test.go, which carries
// //go:build !integration. The helper is now called from an unconstrained
// file, so its own constraint has to be the SUPERSET: the helper moves UP, the
// constraint is not pushed DOWN onto this proof.
//
// THE VERIFIER IS THE REAL ONE. *auth.SOAuthenticator over a real
// *kvstore.MemoryKVStore, recording into a real *audit.ChainFileLogger on a
// real file -- not a stub returning a canned error. The property under test is
// that an SO-gated HANDLER DOES NOT RUN when the factor's record did not reach
// the sink's durability boundary, and a double that never touches a file
// cannot show that.
//
// NO PIN VALUE IS EVER LOGGED. The PIN below is a source literal; it is never
// echoed into t.Logf or into an assertion message.

package middleware

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// discardLogger returns a slog.Logger that discards all output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

const (
	soGateTenant = tenantid.System
	soGatePIN    = "correct horse battery staple"
	// soGateRoute is mapped to permission.SealRekey in testRESTRoutes, which
	// is in SOPermissions -- so the middleware gates it.
	soGateRoute = "/v1/barrier/rekey"
)

// soGateFixture wires the real pieces: a real chain-file sink on a real file,
// a real SOAuthenticator over a real in-memory KV store with the PIN already
// committed, and the middleware under test wrapping a handler that records
// whether it ran.
type soGateFixture struct {
	chain    *audit.ChainFileLogger
	path     string
	handler  http.Handler
	ran      *bool
	soRoleID string
}

func newSOGateFixture(t *testing.T, policy audit.FailurePolicy) *soGateFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit-events.ndjson")
	chain, err := audit.NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	t.Cleanup(func() { _ = chain.Close() })

	verifier, err := auth.NewSOAuthenticator(kvstore.NewMemoryKVStore(), chain, discardLogger(),
		auth.WithSOAuditFailurePolicy(policy))
	if err != nil {
		t.Fatalf("NewSOAuthenticator: %v", err)
	}
	if err := verifier.SetSOPIN(context.Background(), soGateTenant, []byte(soGatePIN)); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}

	roles, soRoleID := seededSORoles(t)
	ran := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		ran = true
		w.WriteHeader(http.StatusOK)
	})
	return &soGateFixture{
		soRoleID: soRoleID,
		chain:    chain,
		path:     path,
		handler:  SOMiddleware(testRESTRoutes(), roles, verifier, chain, policy, discardLogger(), next),
		ran:      &ran,
	}
}

// fault closes the real sink's file, which is how the daemon's audit sink
// fails while in-flight requests are still draining: the append then fails at
// os.File.Write and the sink returns *audit.WriteError.
func (f *soGateFixture) fault(t *testing.T) {
	t.Helper()
	if err := f.chain.Close(); err != nil {
		t.Fatalf("close (the injected fault): %v", err)
	}
}

// serve drives one real SO-gated request through the middleware.
func (f *soGateFixture) serve(pin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, soGateRoute, nil)
	req.TLS = &tls.ConnectionState{}
	if pin != "" {
		req.Header.Set(SOPINHeader, pin)
	}
	req = req.WithContext(auth.WithIdentity(req.Context(), &auth.Identity{
		Subject:  "officer",
		TenantID: soGateTenant,
		Roles:    []string{f.soRoleID},
	}))
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body restErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return body.Error
}

// ---------------------------------------------------------------------------
// THE PROOF
//
// Before go-qrdb#445 this request was SERVED: the SO factor passed, its record
// failed to write, and the handler ran with nothing in the trail to say the
// second factor had been presented at all.
// ---------------------------------------------------------------------------

func TestSOMiddleware_RealChainFileFailure_RefusesAndHandlerDoesNotRun(t *testing.T) {
	f := newSOGateFixture(t, audit.FailClosed)
	// The trail already holds the fixture's own SetSOPIN record, written while
	// the sink was healthy. What must not grow is what comes AFTER the fault.
	before, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatalf("read the trail: %v", err)
	}
	f.fault(t)

	rec := f.serve(soGatePIN)

	if *f.ran {
		t.Fatal("the handler ran: an SO operation was served without its factor being recorded")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (retryable node-side fault, not a denial)", rec.Code)
	}
	msg := decodeError(t, rec)
	if !strings.Contains(msg, "audit log failed") {
		t.Errorf("the response must say why, got %q", msg)
	}
	// 503 and NOT 401: telling an officer holding a valid PIN that the PIN was
	// wrong is terminal in every client and is the precise misdiagnosis the
	// class mapping exists to prevent.
	if strings.Contains(msg, "SO PIN verification failed") {
		t.Errorf("an audit outage must not be reported as a bad PIN, got %q", msg)
	}
	after, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatalf("re-read the trail: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("the faulted sink recorded %d new bytes", len(after)-len(before))
	}
}

// TestSOMiddleware_HealthyChainFileServesAndRecords is the CONTROL. Without it
// a middleware that refused every SO request unconditionally would pass the
// test above.
func TestSOMiddleware_HealthyChainFileServesAndRecords(t *testing.T) {
	f := newSOGateFixture(t, audit.FailClosed)

	rec := f.serve(soGatePIN)

	if !*f.ran || rec.Code != http.StatusOK {
		t.Fatalf("a recordable SO verification must be served; ran=%v code=%d", *f.ran, rec.Code)
	}
	data, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatalf("read the trail: %v", err)
	}
	trail := string(data)
	// Both assertions of the SO request are in the trail: the authenticator's
	// own so-pin verify record and the middleware's dual-factor record naming
	// the route.
	for _, want := range []string{"so-pin", strings.ToLower(soGateRoute)} {
		if !strings.Contains(trail, want) {
			t.Errorf("the trail is missing %q: %q", want, trail)
		}
	}
	if strings.Contains(trail, soGatePIN) {
		t.Fatal("the PIN itself must never reach the audit trail")
	}
}

// TestSOMiddleware_FailOpenServesUnrecorded is the documented escape, on the
// SAME operator lever the authorization middleware reads. There is no second
// configuration key: this policy value came from auth.audit_failure_policy.
func TestSOMiddleware_FailOpenServesUnrecorded(t *testing.T) {
	f := newSOGateFixture(t, audit.FailOpen)
	f.fault(t)

	rec := f.serve(soGatePIN)

	if !*f.ran || rec.Code != http.StatusOK {
		t.Fatalf("fail-open must serve the request; ran=%v code=%d", *f.ran, rec.Code)
	}
}

// TestSOMiddleware_DefaultPolicyIsFailClosed pins the zero value at THIS
// layer. SOMiddleware takes the posture as a positional parameter precisely so
// that a caller which has not been updated passes the zero value and gets the
// secure posture.
func TestSOMiddleware_DefaultPolicyIsFailClosed(t *testing.T) {
	var zero audit.FailurePolicy
	if zero != audit.FailClosed {
		t.Fatalf("the zero policy is %v, want audit.FailClosed", zero)
	}

	f := newSOGateFixture(t, zero)
	f.fault(t)

	if rec := f.serve(soGatePIN); rec.Code != http.StatusServiceUnavailable || *f.ran {
		t.Fatalf("the zero policy must refuse; ran=%v code=%d", *f.ran, rec.Code)
	}
}

// ---------------------------------------------------------------------------
// THE REFUSAL BRANCHES ARE GATED TOO
//
// "We answer only what we could record" is one rule a reader can check.
// Carving out the branches that were going to refuse anyway makes it several
// rules that will drift, and buys nothing: the caller is refused either way.
// What changes is that it is told to RETRY rather than told its credential was
// bad, which under an audit outage is the truth.
// ---------------------------------------------------------------------------

func TestSOMiddleware_MissingPINUnrecordable_Returns503NotUnauthorized(t *testing.T) {
	f := newSOGateFixture(t, audit.FailClosed)
	f.fault(t)

	rec := f.serve("")

	if *f.ran {
		t.Fatal("the handler ran on a request with no SO PIN")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if msg := decodeError(t, rec); !strings.Contains(msg, "audit log failed") {
		t.Errorf("the response must name the audit failure, got %q", msg)
	}
}

// TestSOMiddleware_MissingPINRecordable_Returns401 is that test's control: on
// a healthy node a missing PIN is still a 401.
func TestSOMiddleware_MissingPINRecordable_Returns401(t *testing.T) {
	f := newSOGateFixture(t, audit.FailClosed)

	rec := f.serve("")

	if *f.ran {
		t.Fatal("the handler ran on a request with no SO PIN")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// TestSOMiddleware_WrongPINRecordable_Returns401 is the other half of the
// control: the verifier's real ErrSOPINInvalid still renders 401, so the
// concrete-type guard added for *audit.GateError has not swallowed the
// ordinary refusal.
func TestSOMiddleware_WrongPINRecordable_Returns401(t *testing.T) {
	f := newSOGateFixture(t, audit.FailClosed)

	rec := f.serve("not the pin")

	if *f.ran {
		t.Fatal("the handler ran on a wrong SO PIN")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if msg := decodeError(t, rec); !strings.Contains(msg, "SO PIN verification failed") {
		t.Errorf("a wrong PIN must say so, got %q", msg)
	}
}

// TestSOMiddleware_VerifierGateErrorIsNotFlattenedIntoInvalidPIN is the
// isAuditFailure guard, tested as behaviour.
//
// auth.SOAuthenticator.VerifySOPIN now returns *audit.GateError instead of
// ErrSOPINInvalid when it cannot record its own outcome, and this middleware's
// verify branch used to answer 401 for EVERY error the verifier returned. The
// guard is on the narrow AuthFailureAuditUnavailable class (isAuditFailure)
// and NOT on AuthFailureUnavailable: that class carries
// *StoreUnavailableError wrapping tenant.ErrTenantSealed, which is a signal
// the sealed-window fallbacks act on rather than one to reinterpret here.
// go-qrdb#460 split the two classes apart for exactly this reason.
func TestSOMiddleware_VerifierGateErrorIsNotFlattenedIntoInvalidPIN(t *testing.T) {
	// The sink the MIDDLEWARE writes through is healthy; only the VERIFIER's
	// sink is faulted. So the 503 can only come from the verifier's own gate
	// error being classified rather than flattened.
	healthyPath := filepath.Join(t.TempDir(), "middleware-audit.ndjson")
	healthy, err := audit.NewFileLogger(healthyPath)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	defer func() { _ = healthy.Close() }()

	broken, err := audit.NewFileLogger(filepath.Join(t.TempDir(), "verifier-audit.ndjson"))
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	verifier, err := auth.NewSOAuthenticator(kvstore.NewMemoryKVStore(), broken, discardLogger())
	if err != nil {
		t.Fatalf("NewSOAuthenticator: %v", err)
	}
	if err := verifier.SetSOPIN(context.Background(), soGateTenant, []byte(soGatePIN)); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}
	if err := broken.Close(); err != nil {
		t.Fatalf("close (the injected fault): %v", err)
	}

	ran := false
	roles, soRoleID := seededSORoles(t)
	h := SOMiddleware(testRESTRoutes(), roles, verifier, healthy, audit.FailClosed, discardLogger(),
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			ran = true
			w.WriteHeader(http.StatusOK)
		}))

	req := httptest.NewRequest(http.MethodPost, soGateRoute, nil)
	req.TLS = &tls.ConnectionState{}
	req.Header.Set(SOPINHeader, soGatePIN)
	req = req.WithContext(auth.WithIdentity(req.Context(), &auth.Identity{
		Subject: "officer", TenantID: soGateTenant, Roles: []string{soRoleID},
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if ran {
		t.Fatal("the handler ran although the verifier could not record the factor")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; a verifier audit failure must not be flattened into 401", rec.Code)
	}
	msg := decodeError(t, rec)
	if !strings.Contains(msg, "so-auth") {
		t.Errorf("the response must name the gate that refused, got %q", msg)
	}
}

// TestSOMiddleware_NonSORouteUnaffectedByFaultedSink: the posture must not
// change behaviour outside SOPermissions. A gate that started refusing
// unrelated traffic when the audit disk filled would be a far larger outage
// than the one it prevents.
func TestSOMiddleware_NonSORouteUnaffectedByFaultedSink(t *testing.T) {
	f := newSOGateFixture(t, audit.FailClosed)
	f.fault(t)

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	req.TLS = &tls.ConnectionState{}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)

	if !*f.ran || rec.Code != http.StatusOK {
		t.Fatalf("a non-SO route must pass through untouched; ran=%v code=%d", *f.ran, rec.Code)
	}
}
