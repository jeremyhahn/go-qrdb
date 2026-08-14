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
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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
