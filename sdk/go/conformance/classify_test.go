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

package conformance

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The typed-refusal cells of the matrix rest entirely on classifyRefusal. If
// it accepted any non-nil error, those cells would be satisfied by a timeout,
// an authorization failure or a lost connection — and the worst of those is a
// timeout, because every call in the matrix is bounded, so a cell whose shard
// WEDGED returns DeadlineExceeded. The gate would then go green on the exact
// go-qrdb#129 regression it was written to pin.
//
// So the rejections are tested directly, not assumed. This is the test that
// makes the matrix's refusal cells mean something.
func TestClassifyRefusal(t *testing.T) {
	// wrapped mimics how the SDK transports present a remote failure: the gRPC
	// status error carried inside the transport's own error type, which is why
	// classification has to walk the chain rather than inspect only the top.
	wrapped := func(code codes.Code, msg string) error {
		return fmt.Errorf("transport: request Foo failed: %w", status.Error(code, msg))
	}

	rejected := []struct {
		name string
		err  error
	}{
		{"nil — a silent success", nil},
		{"deadline exceeded — a WEDGED cell", context.DeadlineExceeded},
		{"deadline wrapped by the transport",
			fmt.Errorf("transport: request embedded failed: %w", context.DeadlineExceeded)},
		{"context canceled", context.Canceled},
		{"not-leader — the shard never settled", &transport.NotLeaderError{ShardID: 1}},
		{"gRPC Unauthenticated", wrapped(codes.Unauthenticated, "no client certificate")},
		{"gRPC PermissionDenied", wrapped(codes.PermissionDenied, "kv:put denied")},
		{"gRPC Unavailable", wrapped(codes.Unavailable, "connection refused")},
		{"gRPC DeadlineExceeded", wrapped(codes.DeadlineExceeded, "too slow")},
		{"an unrelated internal fault", errors.New("service: propose failed: disk full")},
		{"an UNTYPED Internal that merely mentions an opcode — the pre-#141 shape",
			wrapped(codes.Internal, "service: statemachine: unknown opcode: 67")},
		{"a typed refusal that names nothing",
			&transport.CapabilityUnsupportedError{Message: "something went wrong"}},
		{"a storage error that names nothing", errors.New("service: read failed")},
	}
	for _, tc := range rejected {
		t.Run("rejects/"+tc.name, func(t *testing.T) {
			if reason := classifyRefusal(tc.err); reason == nil {
				t.Errorf("classifyRefusal(%v) accepted it as a capability refusal; a "+
					"typed-refusal cell would pass on this, which is how a gate goes green "+
					"on the defect it exists to catch", tc.err)
			}
		})
	}

	// Since go-qrdb#141 a refusal is typed on both transports, so the accepted
	// set is built from that type rather than from prose. The gRPC entries are
	// constructed the way mapGRPCError builds them from a codes.Unimplemented
	// status, so this stays honest about what actually crosses the wire.
	accepted := []struct {
		name string
		err  error
	}{
		{"absent CompareAndDelete opcode",
			&transport.CapabilityUnsupportedError{Message: "service: statemachine: unknown opcode: 66"}},
		{"absent CompareAndSwap opcode",
			&transport.CapabilityUnsupportedError{Message: "service: statemachine: unknown opcode: 67"}},
		{"no MVCC store for transactions",
			&transport.CapabilityUnsupportedError{Message: "service: statemachine: mvcc not configured"}},
		{"wrapped by a transport, as a caller sees it",
			fmt.Errorf("transport: request CompareAndSwap failed: %w",
				&transport.CapabilityUnsupportedError{Message: "service: statemachine: unknown opcode: 67"})},
	}
	for _, tc := range accepted {
		t.Run("accepts/"+tc.name, func(t *testing.T) {
			if reason := classifyRefusal(tc.err); reason != nil {
				t.Errorf("classifyRefusal rejected a genuine capability refusal (%v): %v", tc.err, reason)
			}
		})
	}
}

// TestClassifyRefusal_NamedExclusion pins the go-qrdb#126 exclusion at both
// edges: what it lets through, and — more importantly — what it still rejects.
//
// An exclusion that quietly accepted any error on the excused verb would be a
// hole in the gate rather than a documented limit, so the wedge and
// authorization cases are asserted against the EXCUSED path too. Those are the
// failures the refusal cells exist to catch, and neither is a 501.
func TestClassifyRefusal_NamedExclusion(t *testing.T) {
	notImplemented := func(msg string) error {
		return &transport.RequestError{
			Method:     "ExecuteTransaction",
			StatusCode: http.StatusNotImplemented,
			Err:        errors.New(msg),
		}
	}

	// Without the exclusion, REST's untyped 501 is correctly rejected: that is
	// the behaviour every other verb and transport is still held to.
	if reason := classifyRefusalWith(notImplemented("service: statemachine: mvcc not configured"), false); reason == nil {
		t.Error("an untyped 501 was accepted WITHOUT the exclusion; the exclusion would then " +
			"be doing nothing and every transport would have silently lost its typed-refusal gate")
	}

	// With it, the same error is accepted — this is the whole exclusion.
	if reason := classifyRefusalWith(notImplemented("service: statemachine: mvcc not configured"), true); reason != nil {
		t.Errorf("the excused verb's 501 was rejected: %v", reason)
	}

	rejectedEvenWhenExcused := []struct {
		name string
		err  error
	}{
		{"a WEDGED cell — the go-qrdb#129 failure mode", context.DeadlineExceeded},
		{"an authorization failure", &transport.RequestError{
			Method: "ExecuteTransaction", StatusCode: http.StatusForbidden,
			Err: errors.New("kv:put denied")}},
		{"a server fault", &transport.RequestError{
			Method: "ExecuteTransaction", StatusCode: http.StatusInternalServerError,
			Err: errors.New("service: propose failed")}},
		{"a 501 that names nothing missing", notImplemented("something went wrong")},
		{"no error at all", nil},
	}
	for _, tc := range rejectedEvenWhenExcused {
		t.Run("still-rejects/"+tc.name, func(t *testing.T) {
			if reason := classifyRefusalWith(tc.err, true); reason == nil {
				t.Errorf("the exclusion accepted %v; it must excuse only the MISSING TYPE, "+
					"never the difference between a refusal and a fault", tc.err)
			}
		})
	}
}

// grpcStatusOf must see through the transport's wrapper. If it inspected only
// the outermost error it would find no status, every remote classification
// would silently never fire, and the gRPC refusal cells would fall through to
// the message check alone — passing an Unauthenticated failure whose message
// happened to contain the marker.
func TestGRPCStatusOf_WalksTheErrorChain(t *testing.T) {
	inner := status.Error(codes.PermissionDenied, "denied")
	st, ok := grpcStatusOf(fmt.Errorf("transport: request Foo failed: %w", inner))
	if !ok {
		t.Fatal("no status found through the transport wrapper; every remote classification " +
			"in classifyRefusal would be dead code")
	}
	if st.Code() != codes.PermissionDenied {
		t.Errorf("code = %s, want %s", st.Code(), codes.PermissionDenied)
	}

	if _, ok := grpcStatusOf(errors.New("plain error")); ok {
		t.Error("reported a gRPC status for an error that carries none")
	}
}
