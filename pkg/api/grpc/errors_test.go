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

package grpc

import (
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// TestMapServiceError_WatchCompacted verifies a watch requesting a
// compacted start revision maps to OutOfRange, the same status family as
// service.RevisionCompactedError, so a gRPC client can distinguish "retry
// with a fresh read" from a generic Internal failure.
func TestMapServiceError_WatchCompacted(t *testing.T) {
	err := mapServiceError(watch.ErrCompacted)

	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.OutOfRange {
		t.Fatalf("code = %s, want OutOfRange", st.Code())
	}
	if st.Message() != watch.ErrCompacted.Error() {
		t.Errorf("message = %q, want %q", st.Message(), watch.ErrCompacted.Error())
	}
}

// TestMapServiceError_WatchStartRevisionUnsupported verifies a watch
// requesting a start revision against a store with no history replayer
// attached maps to Unimplemented.
func TestMapServiceError_WatchStartRevisionUnsupported(t *testing.T) {
	err := mapServiceError(watch.ErrStartRevisionUnsupported)

	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.Unimplemented {
		t.Fatalf("code = %s, want Unimplemented", st.Code())
	}
}

// TestMapServiceError_WatchCompacted_Wrapped verifies the mapping still
// applies when the sentinel is wrapped (errors.Is semantics), matching how
// it actually arrives from hub.Watch/WatchPrefix via the service layer.
func TestMapServiceError_WatchCompacted_Wrapped(t *testing.T) {
	err := mapServiceError(&watchCreateTestError{cause: watch.ErrCompacted})

	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.OutOfRange {
		t.Fatalf("code = %s, want OutOfRange", st.Code())
	}
}

// watchCreateTestError is a minimal Unwrap-able wrapper used only to
// exercise errors.Is-based matching in mapServiceError.
type watchCreateTestError struct {
	cause error
}

func (e *watchCreateTestError) Error() string { return "wrapped: " + e.cause.Error() }
func (e *watchCreateTestError) Unwrap() error { return e.cause }

// TestGRPCServiceNotConfiguredErrorHint verifies the optional Hint is appended to the message
// so refusals can name the remedy (e.g. "requires mode: raft").
func TestGRPCServiceNotConfiguredErrorHint(t *testing.T) {
	err := &ServiceNotConfiguredError{Service: "watch", Hint: "requires mode: raft"}
	if got := err.Error(); !strings.Contains(got, "watch") || !strings.Contains(got, "requires mode: raft") {
		t.Fatalf("Error() = %q, want the service name and the hint", got)
	}
	bare := &ServiceNotConfiguredError{Service: "watch"}
	if strings.Contains(bare.Error(), ":  ") || strings.HasSuffix(bare.Error(), ": ") {
		t.Fatalf("Error() without hint = %q, must not carry a dangling separator", bare.Error())
	}
}

// An unencodable request is the caller's fault, so it must map to
// InvalidArgument and not to Internal.
//
// The rule has to sit ABOVE the ProposeError rule: service.EncodeRequestError
// is not a ProposeError, but if the ordering were ever reversed by a wrapper
// the oversized request would be reported as a broken cluster — an operational
// alert, and a retry of an input that can never succeed (go-qrdb#52).
func TestMapServiceError_EncodeRequestErrorIsInvalidArgument(t *testing.T) {
	err := mapServiceError(&service.EncodeRequestError{
		Err: &protocol.FieldTooLongError{Field: "key", Length: 70000, Max: protocol.MaxUint16Field},
	})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("code = %v, want %v", got, codes.InvalidArgument)
	}
}

// A genuine proposal failure is still Internal, so the new rule did not
// reclassify the case it sits in front of.
func TestMapServiceError_ProposeErrorIsStillInternal(t *testing.T) {
	err := mapServiceError(&service.ProposeError{Err: errors.New("raft is down")})
	if got := status.Code(err); got != codes.Internal {
		t.Fatalf("code = %v, want %v", got, codes.Internal)
	}
}

// A write that met a closed store is the node going away underneath the
// request, not a broken server. Unavailable is retryable and routable;
// Internal fires an operational alert for an orderly shutdown (go-qrdb#371).
//
// The rule sits ABOVE the ProposeError rule because the closed-store error
// arrives wrapped in one, so a reversed ordering would classify it Internal.
func TestMapServiceError_StorageClosedIsUnavailable(t *testing.T) {
	err := mapServiceError(&service.ProposeError{
		Err: &storage.OpError{Op: "set", Err: &storage.ClosedError{}},
	})
	if got := status.Code(err); got != codes.Unavailable {
		t.Fatalf("code = %v, want %v", got, codes.Unavailable)
	}
}

// The same holds on the read path, where the wrapper is ReadError.
func TestMapServiceError_StorageClosedOnReadIsUnavailable(t *testing.T) {
	err := mapServiceError(&service.ReadError{
		Err: &storage.BatchError{Op: "get", Err: &storage.ClosedError{}},
	})
	if got := status.Code(err); got != codes.Unavailable {
		t.Fatalf("code = %v, want %v", got, codes.Unavailable)
	}
}

// The backend's own refusal reports the same condition, so it must reach the
// same code: a caller must not have to know whether the storage engine or the
// local backend lost the shutdown race.
func TestMapServiceError_BackendClosedIsUnavailable(t *testing.T) {
	err := mapServiceError(&service.ProposeError{Err: &backend.BackendClosedError{}})
	if got := status.Code(err); got != codes.Unavailable {
		t.Fatalf("code = %v, want %v", got, codes.Unavailable)
	}
}
