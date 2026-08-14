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

package transport

import (
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// classifyWatchReason / ClassifyWatchError
// ---------------------------------------------------------------------------

func TestClassifyWatchError_Compacted(t *testing.T) {
	// The exact text pkg/watch.ErrCompacted carries, as it would arrive
	// wrapped through a transport-specific request error.
	src := errors.New("qrdb/watch: required start revision has been compacted")

	got := ClassifyWatchError(src)

	var wce *WatchCompactedError
	if !errors.As(got, &wce) {
		t.Fatalf("ClassifyWatchError() = %T (%v), want *WatchCompactedError", got, got)
	}
	if wce.Reason != src.Error() {
		t.Errorf("Reason = %q, want %q", wce.Reason, src.Error())
	}
}

func TestClassifyWatchError_StartRevisionUnsupported(t *testing.T) {
	src := errors.New("qrdb/watch: start revision replay is not supported by this store")

	got := ClassifyWatchError(src)

	var wsue *WatchStartRevisionUnsupportedError
	if !errors.As(got, &wsue) {
		t.Fatalf("ClassifyWatchError() = %T (%v), want *WatchStartRevisionUnsupportedError", got, got)
	}
}

func TestClassifyWatchError_WrappedInRequestError(t *testing.T) {
	// Simulates how the compacted condition actually arrives client-side:
	// wrapped several layers deep behind transport-specific error types
	// (e.g. REST's RequestError, gRPC's status error text). Contains-based
	// matching must still find it.
	inner := errors.New("qrdb/watch: required start revision has been compacted")
	wrapped := &RequestError{Method: "Watch", StatusCode: 416, Err: inner}

	got := ClassifyWatchError(wrapped)

	var wce *WatchCompactedError
	if !errors.As(got, &wce) {
		t.Fatalf("ClassifyWatchError() = %T (%v), want *WatchCompactedError", got, got)
	}
}

func TestClassifyWatchError_UnknownReasonUnchanged(t *testing.T) {
	src := errors.New("some other server failure")

	got := ClassifyWatchError(src)

	if got != src {
		t.Fatalf("ClassifyWatchError() = %v, want unchanged %v", got, src)
	}
}

func TestClassifyWatchError_Nil(t *testing.T) {
	if got := ClassifyWatchError(nil); got != nil {
		t.Errorf("ClassifyWatchError(nil) = %v, want nil", got)
	}
}

// ---------------------------------------------------------------------------
// WatchResponse.Err
// ---------------------------------------------------------------------------

func TestWatchResponse_Err_NotCanceled(t *testing.T) {
	resp := WatchResponse{Canceled: false, CancelReason: "should be ignored"}
	if err := resp.Err(); err != nil {
		t.Errorf("Err() = %v, want nil for a non-terminal response", err)
	}
}

func TestWatchResponse_Err_Compacted(t *testing.T) {
	resp := WatchResponse{
		Canceled:     true,
		CancelReason: "qrdb/watch: required start revision has been compacted",
	}
	err := resp.Err()
	var wce *WatchCompactedError
	if !errors.As(err, &wce) {
		t.Fatalf("Err() = %T (%v), want *WatchCompactedError", err, err)
	}
}

func TestWatchResponse_Err_StartRevisionUnsupported(t *testing.T) {
	resp := WatchResponse{
		Canceled:     true,
		CancelReason: "qrdb/watch: start revision replay is not supported by this store",
	}
	err := resp.Err()
	var wsue *WatchStartRevisionUnsupportedError
	if !errors.As(err, &wsue) {
		t.Fatalf("Err() = %T (%v), want *WatchStartRevisionUnsupportedError", err, err)
	}
}

func TestWatchResponse_Err_GenericCancel(t *testing.T) {
	resp := WatchResponse{Canceled: true, CancelReason: "server shutting down"}
	err := resp.Err()
	var wce *WatchCanceledError
	if !errors.As(err, &wce) {
		t.Fatalf("Err() = %T (%v), want *WatchCanceledError", err, err)
	}
	if wce.Reason != "server shutting down" {
		t.Errorf("Reason = %q, want %q", wce.Reason, "server shutting down")
	}
}

// ---------------------------------------------------------------------------
// Error() text sanity
// ---------------------------------------------------------------------------

func TestWatchCompactedError_Error(t *testing.T) {
	err := &WatchCompactedError{Reason: "boom"}
	if err.Error() == "" {
		t.Error("Error() must not be empty")
	}
}

func TestWatchStartRevisionUnsupportedError_Error(t *testing.T) {
	err := &WatchStartRevisionUnsupportedError{Reason: "boom"}
	if err.Error() == "" {
		t.Error("Error() must not be empty")
	}
}
