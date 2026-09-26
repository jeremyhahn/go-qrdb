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

package quic

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ---------------------------------------------------------------------------
// StartError
// ---------------------------------------------------------------------------

func TestStartError_Error_ContainsAddr(t *testing.T) {
	inner := errors.New("bind: address already in use")
	se := &StartError{Addr: "127.0.0.1:9090", Err: inner}
	msg := se.Error()
	if !strings.Contains(msg, "127.0.0.1:9090") {
		t.Fatalf("expected address in error message, got %q", msg)
	}
	if !strings.Contains(msg, "bind: address already in use") {
		t.Fatalf("expected inner error in message, got %q", msg)
	}
	if !strings.Contains(msg, "quic: server failed to start on") {
		t.Fatalf("expected prefix in message, got %q", msg)
	}
}

func TestStartError_Unwrap(t *testing.T) {
	inner := errors.New("listen failed")
	se := &StartError{Addr: ":0", Err: inner}
	if se.Unwrap() != inner {
		t.Fatal("Unwrap did not return the underlying error")
	}
}

func TestStartError_ErrorsAs(t *testing.T) {
	inner := errors.New("permission denied")
	se := &StartError{Addr: ":443", Err: inner}
	var target *StartError
	if !errors.As(se, &target) {
		t.Fatal("errors.As failed to match *StartError")
	}
	if target.Addr != ":443" {
		t.Fatalf("expected addr ':443', got %q", target.Addr)
	}
}

// ---------------------------------------------------------------------------
// DispatchError
// ---------------------------------------------------------------------------

func TestDispatchError_Error_ContainsMethod(t *testing.T) {
	de := &DispatchError{Method: "kv.Unknown"}
	msg := de.Error()
	if !strings.Contains(msg, "kv.Unknown") {
		t.Fatalf("expected method name in message, got %q", msg)
	}
	if !strings.Contains(msg, "quic: unknown method") {
		t.Fatalf("expected prefix in message, got %q", msg)
	}
}

func TestDispatchError_Error_EmptyMethod(t *testing.T) {
	de := &DispatchError{Method: ""}
	msg := de.Error()
	if !strings.Contains(msg, "quic: unknown method") {
		t.Fatalf("expected prefix even with empty method, got %q", msg)
	}
}

func TestDispatchError_NoUnwrap(t *testing.T) {
	de := &DispatchError{Method: "invalid.Method"}
	// DispatchError has no Unwrap method; verify it does not implement
	// the unwrap interface.
	var unwrapper interface{ Unwrap() error }
	if errors.As(de, &unwrapper) {
		t.Fatal("DispatchError should not implement Unwrap")
	}
}

// ---------------------------------------------------------------------------
// ServiceNotConfiguredError
// ---------------------------------------------------------------------------

func TestServiceNotConfiguredError_Error_ContainsServiceName(t *testing.T) {
	snc := &ServiceNotConfiguredError{Service: "mvcc"}
	msg := snc.Error()
	if !strings.Contains(msg, "mvcc") {
		t.Fatalf("expected service name in message, got %q", msg)
	}
	if !strings.Contains(msg, "service is not configured") {
		t.Fatalf("expected suffix in message, got %q", msg)
	}
}

func TestServiceNotConfiguredError_Error_DifferentService(t *testing.T) {
	snc := &ServiceNotConfiguredError{Service: "vector"}
	msg := snc.Error()
	if !strings.Contains(msg, "vector") {
		t.Fatalf("expected 'vector' in message, got %q", msg)
	}
	if !strings.Contains(msg, "quic: vector service is not configured") {
		t.Fatalf("expected exact message format, got %q", msg)
	}
}

func TestServiceNotConfiguredError_NoUnwrap(t *testing.T) {
	snc := &ServiceNotConfiguredError{Service: "lease"}
	var unwrapper interface{ Unwrap() error }
	if errors.As(snc, &unwrapper) {
		t.Fatal("ServiceNotConfiguredError should not implement Unwrap")
	}
}

// ---------------------------------------------------------------------------
// InvalidArgumentError
// ---------------------------------------------------------------------------

func TestInvalidArgumentError_Error_ContainsReason(t *testing.T) {
	iae := &InvalidArgumentError{Reason: "key must not be empty"}
	msg := iae.Error()
	if !strings.Contains(msg, "key must not be empty") {
		t.Fatalf("expected reason in message, got %q", msg)
	}
	if !strings.Contains(msg, "quic: invalid argument") {
		t.Fatalf("expected prefix in message, got %q", msg)
	}
}

func TestInvalidArgumentError_Error_DifferentReason(t *testing.T) {
	iae := &InvalidArgumentError{Reason: "value exceeds maximum size"}
	msg := iae.Error()
	if !strings.Contains(msg, "value exceeds maximum size") {
		t.Fatalf("expected reason in message, got %q", msg)
	}
}

func TestInvalidArgumentError_NoUnwrap(t *testing.T) {
	iae := &InvalidArgumentError{Reason: "missing field"}
	var unwrapper interface{ Unwrap() error }
	if errors.As(iae, &unwrapper) {
		t.Fatal("InvalidArgumentError should not implement Unwrap")
	}
}

// ---------------------------------------------------------------------------
// MarshalError
// ---------------------------------------------------------------------------

func TestMarshalError_Error(t *testing.T) {
	inner := errors.New("proto: invalid message")
	me := &MarshalError{Err: inner}
	msg := me.Error()
	if !strings.Contains(msg, "quic: failed to marshal response") {
		t.Fatalf("expected prefix in message, got %q", msg)
	}
	if !strings.Contains(msg, "proto: invalid message") {
		t.Fatalf("expected inner error in message, got %q", msg)
	}
}

func TestMarshalError_Unwrap(t *testing.T) {
	inner := errors.New("encoding failure")
	me := &MarshalError{Err: inner}
	if me.Unwrap() != inner {
		t.Fatal("Unwrap did not return the underlying error")
	}
}

func TestMarshalError_ErrorsAs(t *testing.T) {
	inner := errors.New("nil message")
	me := &MarshalError{Err: inner}
	var target *MarshalError
	if !errors.As(me, &target) {
		t.Fatal("errors.As failed to match *MarshalError")
	}
}

// ---------------------------------------------------------------------------
// UnmarshalError
// ---------------------------------------------------------------------------

func TestUnmarshalError_Error(t *testing.T) {
	inner := errors.New("unexpected wire type")
	ue := &UnmarshalError{Err: inner}
	msg := ue.Error()
	if !strings.Contains(msg, "quic: failed to unmarshal request") {
		t.Fatalf("expected prefix in message, got %q", msg)
	}
	if !strings.Contains(msg, "unexpected wire type") {
		t.Fatalf("expected inner error in message, got %q", msg)
	}
}

func TestUnmarshalError_Unwrap(t *testing.T) {
	inner := errors.New("bad protobuf")
	ue := &UnmarshalError{Err: inner}
	if ue.Unwrap() != inner {
		t.Fatal("Unwrap did not return the underlying error")
	}
}

func TestUnmarshalError_ErrorsAs(t *testing.T) {
	inner := errors.New("truncated input")
	ue := &UnmarshalError{Err: inner}
	var target *UnmarshalError
	if !errors.As(ue, &target) {
		t.Fatal("errors.As failed to match *UnmarshalError")
	}
}

// ---------------------------------------------------------------------------
// AuthenticationError
// ---------------------------------------------------------------------------

func TestAuthenticationError_Error(t *testing.T) {
	ae := &AuthenticationError{Reason: "no client certificate provided"}
	msg := ae.Error()
	if !strings.Contains(msg, "quic: authentication failed") {
		t.Fatalf("expected prefix in message, got %q", msg)
	}
	if !strings.Contains(msg, "no client certificate provided") {
		t.Fatalf("expected reason in message, got %q", msg)
	}
}

func TestAuthenticationError_Error_DifferentReason(t *testing.T) {
	ae := &AuthenticationError{Reason: "certificate expired"}
	msg := ae.Error()
	if !strings.Contains(msg, "certificate expired") {
		t.Fatalf("expected reason in message, got %q", msg)
	}
}

func TestAuthenticationError_NoUnwrap(t *testing.T) {
	ae := &AuthenticationError{Reason: "invalid token"}
	var unwrapper interface{ Unwrap() error }
	if errors.As(ae, &unwrapper) {
		t.Fatal("AuthenticationError should not implement Unwrap")
	}
}

// ---------------------------------------------------------------------------
// AuthorizationError
// ---------------------------------------------------------------------------

func TestAuthorizationError_Error_ContainsMethod(t *testing.T) {
	ae := &AuthorizationError{Method: "kv.Put", Reason: "insufficient permissions"}
	msg := ae.Error()
	if !strings.Contains(msg, "quic: permission denied for") {
		t.Fatalf("expected prefix in message, got %q", msg)
	}
	if !strings.Contains(msg, "kv.Put") {
		t.Fatalf("expected method name in message, got %q", msg)
	}
	if !strings.Contains(msg, "insufficient permissions") {
		t.Fatalf("expected reason in message, got %q", msg)
	}
}

func TestAuthorizationError_Error_DifferentMethod(t *testing.T) {
	ae := &AuthorizationError{Method: "admin.Drain", Reason: "admin role required"}
	msg := ae.Error()
	if !strings.Contains(msg, "admin.Drain") {
		t.Fatalf("expected method name in message, got %q", msg)
	}
	if !strings.Contains(msg, "admin role required") {
		t.Fatalf("expected reason in message, got %q", msg)
	}
}

// TestAuthorizationError_UnwrapsItsCause replaces TestAuthorizationError_NoUnwrap,
// which asserted that this type does NOT implement Unwrap.
//
// That test described the type as it was rather than a property it had to
// have, and the absence it pinned turned out to be a defect: with no Unwrap
// the wrapper ERASED the middleware's typed error, so the QUIC gate could not
// tell "you may not" from "this node could not answer" and wrote
// permission-denied for both -- including for a failed audit write
// (go-qrdb#335). The type now carries and exposes its cause.
//
// A value built with no cause still unwraps to nil, which is what keeps the
// zero-value behaviour of every existing call site unchanged.
func TestAuthorizationError_UnwrapsItsCause(t *testing.T) {
	cause := errors.New("the middleware's own error")
	ae := &AuthorizationError{Method: "kv.Delete", Reason: "read only", Err: cause}
	if !errors.Is(ae, cause) {
		t.Fatal("AuthorizationError must expose its cause: the wire status depends on classifying it")
	}

	bare := &AuthorizationError{Method: "kv.Delete", Reason: "read only"}
	if errors.Unwrap(bare) != nil {
		t.Fatal("an AuthorizationError with no cause must unwrap to nil")
	}
}

// ---------------------------------------------------------------------------
// tlsRequiredError
// ---------------------------------------------------------------------------

func TestTLSRequiredError_Error(t *testing.T) {
	te := &tlsRequiredError{}
	msg := te.Error()
	if !strings.Contains(msg, "quic: TLS is required for QUIC transport") {
		t.Fatalf("expected TLS required message, got %q", msg)
	}
}

func TestTLSRequiredError_Error_ExactMessage(t *testing.T) {
	te := &tlsRequiredError{}
	expected := "quic: TLS is required for QUIC transport"
	if te.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, te.Error())
	}
}

// ---------------------------------------------------------------------------
// mapServiceError
// ---------------------------------------------------------------------------

func TestMapServiceError_NilError(t *testing.T) {
	statusCode, payload := mapServiceError(nil)
	if statusCode != wire.StatusOK {
		t.Fatalf("expected StatusOK (%d), got %d", wire.StatusOK, statusCode)
	}
	if payload != nil {
		t.Fatalf("expected nil payload for nil error, got %v", payload)
	}
}

// TestMapServiceError_KeyNotFoundError. This asserted StatusError until
// go-qrdb#437, because the wire had no not-found status and every absence had
// to ride the generic one. It is now StatusNotFound, which is what let the SDK
// client delete its strings.Contains(msg, "key not found") test.
func TestMapServiceError_KeyNotFoundError(t *testing.T) {
	err := &service.KeyNotFoundError{Key: "missing-key"}
	statusCode, payload := mapServiceError(err)
	if statusCode != wire.StatusNotFound {
		t.Fatalf("expected StatusNotFound (%d), got %d", wire.StatusNotFound, statusCode)
	}
	if !strings.Contains(string(payload), "missing-key") {
		t.Fatalf("expected payload to contain key name, got %q", string(payload))
	}
}

// TestMapServiceError_AuthFactorTenantMismatch asserts the ceremony-time
// auth-factor tenant refusal (go-qrdb#464) classifies as StatusInvalidArgument
// rather than falling through to the generic StatusError: a request naming a
// tenant other than the caller's context is the caller's input being wrong,
// not a server fault, matching REST's 400 and gRPC's InvalidArgument.
func TestMapServiceError_AuthFactorTenantMismatch(t *testing.T) {
	err := &service.AuthFactorTenantMismatchError{Requested: "globex", Context: "acme"}
	statusCode, payload := mapServiceError(err)
	if statusCode != wire.StatusInvalidArgument {
		t.Fatalf("expected StatusInvalidArgument (%d), got %d", wire.StatusInvalidArgument, statusCode)
	}
	if !strings.Contains(string(payload), "globex") || !strings.Contains(string(payload), "acme") {
		t.Fatalf("expected payload to name both tenants, got %q", string(payload))
	}
}

// TestMapServiceError_WrappedAuthFactorTenantMismatch asserts the
// classification survives being wrapped, matching how every other clause in
// this ladder is tested against a wrapped instance.
func TestMapServiceError_WrappedAuthFactorTenantMismatch(t *testing.T) {
	inner := &service.AuthFactorTenantMismatchError{Requested: "globex", Context: "acme"}
	wrapped := fmt.Errorf("authfactor.Enroll: %w", inner)
	statusCode, _ := mapServiceError(wrapped)
	if statusCode != wire.StatusInvalidArgument {
		t.Fatalf("expected StatusInvalidArgument (%d), got %d", wire.StatusInvalidArgument, statusCode)
	}
}

func TestMapServiceError_NotLeaderError(t *testing.T) {
	err := &service.NotLeaderError{
		ShardID:       1,
		LeaderID:      2,
		LeaderAddress: "127.0.0.2:63001",
	}
	statusCode, payload := mapServiceError(err)
	// StatusUnavailable since go-qrdb#437, matching the gRPC transport: a
	// non-leader is a routing condition the caller retries, not a fault. It
	// answered the generic StatusError only because that was the whole
	// vocabulary.
	if statusCode != wire.StatusUnavailable {
		t.Fatalf("expected StatusUnavailable (%d), got %d", wire.StatusUnavailable, statusCode)
	}
	if !strings.Contains(string(payload), "not leader") {
		t.Fatalf("expected payload to contain 'not leader', got %q", string(payload))
	}
}

// TestMapServiceError_TakenIdentity pins the taken-identity family —
// username, email, and go-qrdb#555's generated user id — to
// StatusAlreadyExists, the answer REST (409) and gRPC give each, rather than
// the generic StatusError. The payload must carry the conflicting handle.
func TestMapServiceError_TakenIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		needle string
	}{
		{name: "username", err: &user.UsernameExistsError{Username: "alice", TenantID: "t1"}, needle: "alice"},
		{name: "email", err: &user.EmailExistsError{Email: "a@example.com", TenantID: "t1"}, needle: "a@example.com"},
		{name: "generated user id", err: &user.UserIDExistsError{ID: 42, TenantID: "t1"}, needle: "42"},
		{name: "wrapped by the store", err: &user.StoreError{Op: "create", Err: &user.UsernameExistsError{Username: "bob"}}, needle: "bob"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			statusCode, payload := mapServiceError(tc.err)
			if statusCode != wire.StatusAlreadyExists {
				t.Fatalf("status = %d, want StatusAlreadyExists (%d)", statusCode, wire.StatusAlreadyExists)
			}
			if !strings.Contains(string(payload), tc.needle) {
				t.Errorf("payload %q does not name %q", payload, tc.needle)
			}
		})
	}
}

// TestMapServiceError_IndexCompensationOutranksTakenIdentity pins the arm
// order: a create whose rollback FAILED after a taken-identity collision is
// DataLoss, not the AlreadyExists the collision alone would produce, exactly
// as REST (500) and gRPC (DataLoss) report it.
func TestMapServiceError_IndexCompensationOutranksTakenIdentity(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
	}{
		{name: "username", cause: &user.UsernameExistsError{Username: "alice"}},
		{name: "email", cause: &user.EmailExistsError{Email: "a@example.com"}},
		{name: "generated user id", cause: &user.UserIDExistsError{ID: 7}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := &user.IndexCompensationError{
				Op:       "create",
				UserID:   7,
				Cause:    tc.cause,
				Failures: []error{errors.New("release failed")},
			}
			if statusCode, _ := mapServiceError(err); statusCode != wire.StatusDataLoss {
				t.Fatalf("status = %d, want StatusDataLoss (%d)", statusCode, wire.StatusDataLoss)
			}
		})
	}
}

func TestMapServiceError_ServiceNotConfiguredError(t *testing.T) {
	err := &ServiceNotConfiguredError{Service: "mvcc"}
	statusCode, payload := mapServiceError(err)
	if statusCode != wire.StatusUnimplemented {
		t.Fatalf("expected StatusUnimplemented (%d), got %d", wire.StatusUnimplemented, statusCode)
	}
	if !strings.Contains(string(payload), "mvcc") {
		t.Fatalf("expected payload to contain service name, got %q", string(payload))
	}
}

func TestMapServiceError_UnknownError(t *testing.T) {
	err := errors.New("something unexpected happened")
	statusCode, payload := mapServiceError(err)
	if statusCode != wire.StatusError {
		t.Fatalf("expected StatusError (%d), got %d", wire.StatusError, statusCode)
	}
	if !strings.Contains(string(payload), "something unexpected happened") {
		t.Fatalf("expected payload to contain original message, got %q", string(payload))
	}
}

func TestMapServiceError_WrappedKeyNotFoundError(t *testing.T) {
	inner := &service.KeyNotFoundError{Key: "wrapped-key"}
	err := fmt.Errorf("kv.Get: %w", inner)
	// mapServiceError uses errors.As which follows the Unwrap chain.
	statusCode, payload := mapServiceError(err)
	if statusCode != wire.StatusNotFound {
		t.Fatalf("expected StatusNotFound (%d), got %d", wire.StatusNotFound, statusCode)
	}
	if !strings.Contains(string(payload), "wrapped-key") {
		t.Fatalf("expected payload to contain key name from wrapped error, got %q", string(payload))
	}
}

func TestMapServiceError_WrappedNotLeaderError(t *testing.T) {
	inner := &service.NotLeaderError{ShardID: 5, LeaderID: 3, LeaderAddress: "10.0.0.3:6000"}
	err := fmt.Errorf("kv.Put: %w", inner)
	statusCode, payload := mapServiceError(err)
	if statusCode != wire.StatusUnavailable {
		t.Fatalf("expected StatusUnavailable (%d), got %d", wire.StatusUnavailable, statusCode)
	}
	if !strings.Contains(string(payload), "not leader") {
		t.Fatalf("expected payload to contain 'not leader', got %q", string(payload))
	}
}

// ClientCertRequiredError.Error() coverage.
func TestClientCertRequiredError_Error(t *testing.T) {
	e := &ClientCertRequiredError{}
	if e.Error() == "" {
		t.Fatal("expected non-empty error message")
	}
}

// TestMapServiceError_BarrierSealedError verifies that a data-plane
// service.BarrierSealedError maps to wire.StatusUnavailable (not the
// default StatusError), matching the gRPC (codes.Unavailable) and REST
// (503) BarrierSealedError mapping. The SDK QUIC client interprets
// StatusUnavailable as "barrier sealed".
func TestMapServiceError_BarrierSealedError(t *testing.T) {
	err := &service.BarrierSealedError{TenantID: "tenant-1"}
	statusCode, payload := mapServiceError(err)
	if statusCode != wire.StatusUnavailable {
		t.Fatalf("expected StatusUnavailable (%d), got %d", wire.StatusUnavailable, statusCode)
	}
	if !strings.Contains(string(payload), "tenant-1") {
		t.Fatalf("expected payload to contain tenant ID, got %q", string(payload))
	}
}

// TestMapServiceError_WrappedBarrierSealedError verifies the BarrierSealedError
// mapping follows the Unwrap chain via errors.As, matching the pattern used
// for the other wrapped-error test cases in this file.
func TestMapServiceError_WrappedBarrierSealedError(t *testing.T) {
	inner := &service.BarrierSealedError{TenantID: "tenant-2"}
	err := fmt.Errorf("kv.Get: %w", inner)
	statusCode, payload := mapServiceError(err)
	if statusCode != wire.StatusUnavailable {
		t.Fatalf("expected StatusUnavailable (%d), got %d", wire.StatusUnavailable, statusCode)
	}
	if !strings.Contains(string(payload), "tenant-2") {
		t.Fatalf("expected payload to contain tenant ID, got %q", string(payload))
	}
}

// ---------------------------------------------------------------------------
// watch.ErrCompacted / watch.ErrStartRevisionUnsupported
// ---------------------------------------------------------------------------

// TestMapServiceError_WatchCompacted verifies a watch requesting a compacted
// start revision maps to StatusOutOfRange -- the condition is an address below
// the floor the server still holds, and go-qrdb#437 gave this wire a status
// that says so. It rode StatusError until then, indistinguishable from the
// watcher breaking. The sentinel's message text is still preserved so the SDK
// client's ClassifyWatchError keeps recognizing it downstream.
func TestMapServiceError_WatchCompacted(t *testing.T) {
	statusCode, payload := mapServiceError(watch.ErrCompacted)
	if statusCode != wire.StatusOutOfRange {
		t.Fatalf("expected StatusOutOfRange (%d), got %d", wire.StatusOutOfRange, statusCode)
	}
	if string(payload) != watch.ErrCompacted.Error() {
		t.Fatalf("payload = %q, want %q", string(payload), watch.ErrCompacted.Error())
	}
}

// TestMapServiceError_WatchStartRevisionUnsupported verifies a watch
// requesting a start revision against a store with no history replayer
// attached maps to StatusUnimplemented.
func TestMapServiceError_WatchStartRevisionUnsupported(t *testing.T) {
	statusCode, payload := mapServiceError(watch.ErrStartRevisionUnsupported)
	if statusCode != wire.StatusUnimplemented {
		t.Fatalf("expected StatusUnimplemented (%d), got %d", wire.StatusUnimplemented, statusCode)
	}
	if string(payload) != watch.ErrStartRevisionUnsupported.Error() {
		t.Fatalf("payload = %q, want %q", string(payload), watch.ErrStartRevisionUnsupported.Error())
	}
}

// TestQUICServiceNotConfiguredErrorHint verifies the optional Hint is appended to the message
// so refusals can name the remedy (e.g. "requires mode: raft").
func TestQUICServiceNotConfiguredErrorHint(t *testing.T) {
	err := &ServiceNotConfiguredError{Service: "watch", Hint: "requires mode: raft"}
	if got := err.Error(); !strings.Contains(got, "watch") || !strings.Contains(got, "requires mode: raft") {
		t.Fatalf("Error() = %q, want the service name and the hint", got)
	}
	bare := &ServiceNotConfiguredError{Service: "watch"}
	if strings.Contains(bare.Error(), ":  ") || strings.HasSuffix(bare.Error(), ": ") {
		t.Fatalf("Error() without hint = %q, must not carry a dangling separator", bare.Error())
	}
}
