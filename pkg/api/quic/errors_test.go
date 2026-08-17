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
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
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
// AcceptError
// ---------------------------------------------------------------------------

func TestAcceptError_Error(t *testing.T) {
	inner := errors.New("connection reset by peer")
	ae := &AcceptError{Err: inner}
	msg := ae.Error()
	if !strings.Contains(msg, "quic: failed to accept connection") {
		t.Fatalf("expected prefix in message, got %q", msg)
	}
	if !strings.Contains(msg, "connection reset by peer") {
		t.Fatalf("expected inner error in message, got %q", msg)
	}
}

func TestAcceptError_Unwrap(t *testing.T) {
	inner := errors.New("timeout")
	ae := &AcceptError{Err: inner}
	if ae.Unwrap() != inner {
		t.Fatal("Unwrap did not return the underlying error")
	}
}

func TestAcceptError_ErrorsAs(t *testing.T) {
	inner := errors.New("closed")
	ae := &AcceptError{Err: inner}
	var target *AcceptError
	if !errors.As(ae, &target) {
		t.Fatal("errors.As failed to match *AcceptError")
	}
	if errors.Unwrap(ae) != inner {
		t.Fatal("errors.Unwrap did not return the underlying error")
	}
}

// ---------------------------------------------------------------------------
// StreamError
// ---------------------------------------------------------------------------

func TestStreamError_Error(t *testing.T) {
	inner := errors.New("stream closed")
	se := &StreamError{Err: inner}
	msg := se.Error()
	if !strings.Contains(msg, "quic: stream error") {
		t.Fatalf("expected prefix in message, got %q", msg)
	}
	if !strings.Contains(msg, "stream closed") {
		t.Fatalf("expected inner error in message, got %q", msg)
	}
}

func TestStreamError_Unwrap(t *testing.T) {
	inner := errors.New("read deadline exceeded")
	se := &StreamError{Err: inner}
	if se.Unwrap() != inner {
		t.Fatal("Unwrap did not return the underlying error")
	}
}

func TestStreamError_ErrorsAs(t *testing.T) {
	inner := errors.New("reset")
	se := &StreamError{Err: inner}
	var target *StreamError
	if !errors.As(se, &target) {
		t.Fatal("errors.As failed to match *StreamError")
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
// HandlerError
// ---------------------------------------------------------------------------

func TestHandlerError_Error_ContainsMethod(t *testing.T) {
	inner := errors.New("service unavailable")
	he := &HandlerError{Method: "kv.Put", Err: inner}
	msg := he.Error()
	if !strings.Contains(msg, "kv.Put") {
		t.Fatalf("expected method name in message, got %q", msg)
	}
	if !strings.Contains(msg, "service unavailable") {
		t.Fatalf("expected inner error in message, got %q", msg)
	}
	if !strings.Contains(msg, "quic: handler error for") {
		t.Fatalf("expected prefix in message, got %q", msg)
	}
}

func TestHandlerError_Unwrap(t *testing.T) {
	inner := errors.New("not leader")
	he := &HandlerError{Method: "admin.Status", Err: inner}
	if he.Unwrap() != inner {
		t.Fatal("Unwrap did not return the underlying error")
	}
}

func TestHandlerError_ErrorsAs(t *testing.T) {
	inner := errors.New("timeout")
	he := &HandlerError{Method: "kv.Get", Err: inner}
	var target *HandlerError
	if !errors.As(he, &target) {
		t.Fatal("errors.As failed to match *HandlerError")
	}
	if target.Method != "kv.Get" {
		t.Fatalf("expected method 'kv.Get', got %q", target.Method)
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

func TestAuthorizationError_NoUnwrap(t *testing.T) {
	ae := &AuthorizationError{Method: "kv.Delete", Reason: "read only"}
	var unwrapper interface{ Unwrap() error }
	if errors.As(ae, &unwrapper) {
		t.Fatal("AuthorizationError should not implement Unwrap")
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

func TestMapServiceError_KeyNotFoundError(t *testing.T) {
	err := &service.KeyNotFoundError{Key: "missing-key"}
	statusCode, payload := mapServiceError(err)
	if statusCode != wire.StatusError {
		t.Fatalf("expected StatusError (%d), got %d", wire.StatusError, statusCode)
	}
	if !strings.Contains(string(payload), "missing-key") {
		t.Fatalf("expected payload to contain key name, got %q", string(payload))
	}
}

func TestMapServiceError_NotLeaderError(t *testing.T) {
	err := &service.NotLeaderError{
		ShardID:       1,
		LeaderID:      2,
		LeaderAddress: "127.0.0.2:63001",
	}
	statusCode, payload := mapServiceError(err)
	if statusCode != wire.StatusError {
		t.Fatalf("expected StatusError (%d), got %d", wire.StatusError, statusCode)
	}
	if !strings.Contains(string(payload), "not leader") {
		t.Fatalf("expected payload to contain 'not leader', got %q", string(payload))
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
	err := &HandlerError{Method: "kv.Get", Err: inner}
	// mapServiceError uses errors.As which follows the Unwrap chain.
	statusCode, payload := mapServiceError(err)
	if statusCode != wire.StatusError {
		t.Fatalf("expected StatusError (%d), got %d", wire.StatusError, statusCode)
	}
	if !strings.Contains(string(payload), "wrapped-key") {
		t.Fatalf("expected payload to contain key name from wrapped error, got %q", string(payload))
	}
}

func TestMapServiceError_WrappedNotLeaderError(t *testing.T) {
	inner := &service.NotLeaderError{ShardID: 5, LeaderID: 3, LeaderAddress: "10.0.0.3:6000"}
	err := &HandlerError{Method: "kv.Put", Err: inner}
	statusCode, payload := mapServiceError(err)
	if statusCode != wire.StatusError {
		t.Fatalf("expected StatusError (%d), got %d", wire.StatusError, statusCode)
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
	err := &HandlerError{Method: "kv.Get", Err: inner}
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
// start revision maps to StatusError (the same status handleWatchStream
// writes on any watcher error), with the sentinel's message text preserved
// so the SDK client's ClassifyWatchError can recognize it downstream.
func TestMapServiceError_WatchCompacted(t *testing.T) {
	statusCode, payload := mapServiceError(watch.ErrCompacted)
	if statusCode != wire.StatusError {
		t.Fatalf("expected StatusError (%d), got %d", wire.StatusError, statusCode)
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
