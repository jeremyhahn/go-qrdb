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

// What wire status the QUIC authorization gate answers (go-qrdb#335).
//
// No build constraint: these name only authzStatus, AuthorizationError and the
// wire constants, all unconstrained.

package quic

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// TestAuthzStatus_AuditFailureIsUnavailable is the regression test. The gate
// wrote wire.StatusPermissionDenied unconditionally, so a caller whose request
// this node could merely not RECORD was told it was not permitted -- the same
// mis-mapping REST answered 403 for.
func TestAuthzStatus_AuditFailureIsUnavailable(t *testing.T) {
	cause := &audit.GateError{Err: &audit.WriteError{Backend: "audit.ndjson", Err: errors.New("disk full")}}
	err := &AuthorizationError{Method: "barrier.Unseal", Reason: cause.Error(), Err: cause}

	if got := authzStatus(err); got != wire.StatusUnavailable {
		t.Fatalf("status = %d, want StatusUnavailable (%d)", got, wire.StatusUnavailable)
	}
}

// TestAuthzStatus_Classes walks every class the gate can produce.
func TestAuthzStatus_Classes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want uint16
	}{
		{"audit write failed", &audit.GateError{Err: errors.New("disk full")}, wire.StatusUnavailable},
		{"user store unavailable", &authmw.StoreUnavailableError{Err: errors.New("raft")}, wire.StatusUnavailable},
		{"revocation unreadable", &auth.RevocationCheckError{Err: errors.New("down")}, wire.StatusUnavailable},
		{"ordinary denial", auth.ErrUnauthorized, wire.StatusPermissionDenied},
		{"unauthenticated", auth.ErrUnauthenticated, wire.StatusUnauthenticated},
		// A throttled caller answers StatusResourceExhausted.
		//
		// This case asserted StatusUnavailable, under a named exclusion saying
		// the wire declared no rate-limit status. go-qrdb#437 (PR #461) added
		// one, so the exclusion was false and the assertion was pinning it.
		// Updated deliberately rather than deleted: the instruction the caller
		// acts on is still "retry", and what changed is that the reason is now
		// expressible instead of being flattened into Unavailable.
		{"rate limited", &authmw.RateLimitedError{
			Permission: permission.KVPut, Subject: "a", TenantID: "t", RetryAfter: time.Second,
		}, wire.StatusResourceExhausted},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := &AuthorizationError{Method: "m", Reason: tc.err.Error(), Err: tc.err}
			if got := authzStatus(err); got != tc.want {
				t.Errorf("status = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestAuthzStatus_FallbackIsPermissionDenied: an error the middleware does not
// classify keeps the frame this path sent before the mapping existed.
func TestAuthzStatus_FallbackIsPermissionDenied(t *testing.T) {
	err := &AuthorizationError{Method: "m", Reason: "x", Err: errors.New("unclassified")}
	if got := authzStatus(err); got != wire.StatusPermissionDenied {
		t.Fatalf("status = %d, want StatusPermissionDenied", got)
	}
	// A value with no cause at all must not panic or change the answer.
	if got := authzStatus(&AuthorizationError{Method: "m"}); got != wire.StatusPermissionDenied {
		t.Fatalf("nil cause: status = %d, want StatusPermissionDenied", got)
	}
}

// TestAuthorizationError_MessageFollowsTheClass. The frame's status is the
// machine-readable channel, but the message is what a human reads in a client
// log -- and "permission denied" is the sentence that sends an operator to fix
// permissions that were never the problem.
func TestAuthorizationError_MessageFollowsTheClass(t *testing.T) {
	retryable := &AuthorizationError{
		Method: "barrier.Unseal", Reason: "audit log failed",
		Err: &audit.GateError{Err: errors.New("disk full")},
	}
	if strings.Contains(retryable.Error(), "permission denied") {
		t.Errorf("a retryable fault must not be worded as a denial: %q", retryable.Error())
	}
	if !strings.Contains(retryable.Error(), "cannot authorize") {
		t.Errorf("got %q", retryable.Error())
	}

	denial := &AuthorizationError{Method: "kv.Put", Reason: "insufficient permissions", Err: auth.ErrUnauthorized}
	if !strings.Contains(denial.Error(), "permission denied for kv.Put") {
		t.Errorf("a real denial must keep its wording: %q", denial.Error())
	}
}

// TestAuthorizationError_Unwrap: the wrapper previously erased the typed
// cause, which is why the frame could not tell a denial from a node fault.
func TestAuthorizationError_Unwrap(t *testing.T) {
	cause := &audit.GateError{Err: errors.New("disk full")}
	err := &AuthorizationError{Method: "m", Reason: "r", Err: cause}

	var got *audit.GateError
	if !errors.As(err, &got) {
		t.Fatal("errors.As must reach the middleware's typed error through the QUIC wrapper")
	}
}

// ---------------------------------------------------------------------------
// The handler funnel
// ---------------------------------------------------------------------------

// TestMapServiceError_ClassifiesAuthFailures is the regression test for the
// eleven tenant-barrier handlers.
//
// They gate on the auth middleware and `return nil, err`, so their refusals
// arrive at mapServiceError -- NOT at the method-level gate in handleStream,
// which is the only place authzStatus was originally wired. Nothing in
// mapServiceError classified them, so *audit.GateError, *StoreUnavailableError,
// *RateLimitedError and every RBAC denial from the tenant-scoped gate all fell
// through to wire.StatusError, while docs/audit/architecture.md claimed QUIC
// answered StatusUnavailable on exactly those handlers.
//
// Fixing the mapper and leaving the callers on the old path is the shape this
// defect took once already, so the funnel the callers actually use is what is
// pinned here.
func TestMapServiceError_ClassifiesAuthFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want uint16
	}{
		{"audit write failed", &audit.GateError{
			Err: &audit.WriteError{Backend: "audit.ndjson", Err: errors.New("disk full")},
		}, wire.StatusUnavailable},
		{"user store unavailable", &authmw.StoreUnavailableError{Err: errors.New("raft")}, wire.StatusUnavailable},
		// StatusResourceExhausted since go-qrdb#437 -- see the note in
		// TestAuthzStatus_Classes. Unavailable here would now be a lost reason,
		// not a protocol limit.
		{"rate limited", &authmw.RateLimitedError{
			Permission: permission.KVPut, Subject: "a", TenantID: "t", RetryAfter: time.Second,
		}, wire.StatusResourceExhausted},
		{"ordinary denial", auth.ErrUnauthorized, wire.StatusPermissionDenied},
		{"unauthenticated", auth.ErrUnauthenticated, wire.StatusUnauthenticated},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, payload := mapServiceError(tc.err)
			if status != tc.want {
				t.Fatalf("status = %d, want %d", status, tc.want)
			}
			if len(payload) == 0 {
				t.Error("the payload must carry the reason")
			}
		})
	}
}

// TestMapServiceError_UnclassifiedIsUnchanged: StatusError stays the fallback,
// so nothing this package did not already recognise changes answer.
func TestMapServiceError_UnclassifiedIsUnchanged(t *testing.T) {
	status, _ := mapServiceError(errors.New("some service failure"))
	if status != wire.StatusError {
		t.Fatalf("status = %d, want StatusError (%d)", status, wire.StatusError)
	}
	if status, _ := mapServiceError(nil); status != wire.StatusOK {
		t.Fatalf("nil: status = %d, want StatusOK", status)
	}
}
