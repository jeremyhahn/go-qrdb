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
	"context"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// captureIndexCreateRequest drives the QUIC client's IndexCreate with config
// and returns the protobuf message it actually put on the wire.
//
// The message is decoded from the server's recorded payload rather than
// unmarshalled inside the responder. The responder runs on the server's
// per-stream goroutine, so a message it wrote into and this goroutine read was
// a data race with nothing ordering the two: IndexCreate returning means the
// CLIENT is done, not that the stream handler has finished writing
// (go-qrdb#131, reproduced by the race detector on 2 of 5 runs).
//
// The recording path is already safe and already correctly ordered:
// handleStream appends a defensive copy of the payload under recordMu BEFORE
// dispatching to the responder, LastRequest reads under the same mutex, and
// the responder's return is what releases the response the client is waiting
// on. So by the time IndexCreate returns, the payload is recorded, and the
// mutex supplies the happens-before edge. Decoding here also keeps the
// protobuf message owned by a single goroutine for its whole life.
func captureIndexCreateRequest(t *testing.T, config transport.IndexConfig) *pb.IndexCreateRequest {
	t.Helper()
	srv, tr := startE2EServer(t)

	srv.SetResponder("index.Create", func([]byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})

	if err := tr.IndexCreate(context.Background(), "by_email", "email", config); err != nil {
		t.Fatalf("IndexCreate: %v", err)
	}

	// Never return a zero-valued message for a request that was not captured.
	// Every flag on this wire is a positive claim, so an empty message passes
	// the zero-config polarity assertion below for entirely the wrong reason —
	// which is the same way a torn read of a message mid-proto.Reset used to
	// pass it.
	rec := srv.LastRequest("index.Create")
	if rec == nil {
		t.Fatal("the server recorded no index.Create request; IndexCreate returned without " +
			"putting one on the wire, and an empty message would silently satisfy the " +
			"zero-config assertion")
	}

	var got pb.IndexCreateRequest
	if err := proto.Unmarshal(rec.Payload, &got); err != nil {
		t.Fatalf("unmarshal IndexCreateRequest: %v", err)
	}
	return &got
}

// TestE2E_IndexCreate_EveryConfigFieldReachesTheWire covers go-qrdb#34 on the
// QUIC client. The marshalled request carried Unique and Sparse only: Numeric
// was a transport.IndexConfig field no request ever serialised, and
// case-insensitivity could not be expressed at all. A config field the client
// silently drops makes the server build a different index while the caller is
// told it succeeded.
func TestE2E_IndexCreate_EveryConfigFieldReachesTheWire(t *testing.T) {
	got := captureIndexCreateRequest(t, transport.IndexConfig{
		Unique:          true,
		Sparse:          true,
		Numeric:         true,
		CaseInsensitive: true,
	})

	if got.GetName() != "by_email" || got.GetFieldName() != "email" {
		t.Errorf("name/field = %q/%q; want by_email/email", got.GetName(), got.GetFieldName())
	}
	if !got.GetUnique() || !got.GetSparse() {
		t.Errorf("unique/sparse lost: %+v", got)
	}
	if !got.GetNumeric() {
		t.Error("Numeric never reached the wire")
	}
	if !got.GetCaseInsensitive() {
		t.Error("CaseInsensitive never reached the wire")
	}
}

// TestE2E_IndexCreate_ZeroConfigSendsNoClaims is the control and the polarity
// guard: every flag on this wire marks a POSITIVE claim, so a zero config must
// leave all of them unset — an ordinary index, case-SENSITIVE, string-ordered.
func TestE2E_IndexCreate_ZeroConfigSendsNoClaims(t *testing.T) {
	got := captureIndexCreateRequest(t, transport.IndexConfig{})

	// Identify the request before judging its flags. Every assertion below is
	// an absence, and absences are exactly what an empty or half-decoded
	// message satisfies — so this test needs positive evidence that it is
	// looking at the request the client actually sent before "no claims set"
	// means anything.
	if got.GetName() != "by_email" || got.GetFieldName() != "email" {
		t.Fatalf("captured name/field = %q/%q; want by_email/email — the assertions below "+
			"cannot be trusted against a message this test did not identify",
			got.GetName(), got.GetFieldName())
	}

	if got.GetUnique() || got.GetSparse() || got.GetNumeric() || got.GetCaseInsensitive() {
		t.Errorf("a zero config sent %+v; want no claims set", got)
	}
}
