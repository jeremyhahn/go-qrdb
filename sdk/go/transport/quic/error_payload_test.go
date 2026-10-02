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

// No build constraint: errorBody is shared by tests in every build of this
// package.

package quic

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// errorBody is msg as the server frames it in an error frame with status st.
func errorBody(t *testing.T, st uint16, msg string) []byte {
	t.Helper()
	body, err := wire.EncodeErrorPayload(st, wire.ErrorPayload{Message: msg})
	if err != nil {
		t.Fatalf("encode error payload: %v", err)
	}
	return body
}

// TestMapWireError_ReadsTheMessageOutOfTheErrorPayload: the client shows the
// payload's message, not the encoded bytes, and a delay in the payload does
// not change the classification, which is the frame status's alone.
func TestMapWireError_ReadsTheMessageOutOfTheErrorPayload(t *testing.T) {
	body, err := wire.EncodeErrorPayload(wire.StatusResourceExhausted, wire.ErrorPayload{
		Message: "auth: SO PIN locked after repeated failures", RetryAfter: 3 * time.Second, HasRetryAfter: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := mapWireError("barrier.RekeyStart", wire.StatusResourceExhausted, body)
	var reqErr *transport.RequestError
	if !errors.As(got, &reqErr) || reqErr.StatusCode != int(wire.StatusResourceExhausted) {
		t.Fatalf("err = %T %v, want a RequestError carrying RESOURCE_EXHAUSTED", got, got)
	}
	if want := "resource exhausted: auth: SO PIN locked after repeated failures"; reqErr.Err.Error() != want {
		t.Fatalf("message %q, want %q", reqErr.Err.Error(), want)
	}
}

// TestMapWireError_MalformedBodyIsReportedNotShown: a body that is not an
// error payload -- a server speaking the retired bare-text frames, say -- is a
// RequestError carrying the decode failure and the status, never its bytes
// passed off as the server's message, and never a typed condition.
func TestMapWireError_MalformedBodyIsReportedNotShown(t *testing.T) {
	got := mapWireError("kv.Get", wire.StatusNotFound, []byte("\xffkey not found"))
	var decodeErr *wire.ErrorPayloadDecodeError
	if !errors.As(got, &decodeErr) {
		t.Fatalf("err = %T %v, want it to carry *wire.ErrorPayloadDecodeError", got, got)
	}
	if isWireNotFound(got) {
		t.Fatal("an undecodable body was classified as absence")
	}
	var reqErr *transport.RequestError
	if !errors.As(got, &reqErr) || reqErr.StatusCode != int(wire.StatusNotFound) {
		t.Fatalf("err = %v, want a RequestError carrying the frame's status", got)
	}
}

// TestWatchCancelReason: a watch error frame's reason is its payload's
// message; an undecodable one names the decode failure instead of its bytes.
func TestWatchCancelReason(t *testing.T) {
	if got := watchCancelReason(errorBody(t, wire.StatusOutOfRange, "compacted")); got != "compacted" {
		t.Fatalf("reason %q, want \"compacted\"", got)
	}
	if got := watchCancelReason([]byte("\xffbare")); !strings.Contains(got, "error payload decode failed") {
		t.Fatalf("reason %q, want the decode failure", got)
	}
}
