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
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// These tests cover the CLIENT edge of the guarded plane over QUIC
// (go-qrdb#124). The end-to-end delegation — that the verb reaches the service
// layer's atomic implementation and that the stored/deleted/swapped answer is
// the service's decision — is proven by the go-qrdb#121 conformance matrix,
// which drives these methods against a real node on every engine and mode.
// What the matrix cannot see is what this transport put on the wire, and that
// is what is checked here:
//
//   - exactly ONE request per call, to the guarded method name. This is the
//     assertion that catches an emulated verb: a get-then-put would show up as
//     a kv.Get followed by a kv.Put and would still return a plausible boolean.
//   - the request payload carries the caller's exact bytes, with `expected`
//     and `next` in the right fields.
//   - wire.StatusUnimplemented arrives as the TYPED capability refusal.

func TestTransport_ImplementsGuardedKVClient(t *testing.T) {
	_, tr := startE2EServer(t)
	if _, ok := interface{}(tr).(transport.GuardedKVClient); !ok {
		t.Fatal("QUIC transport does not implement transport.GuardedKVClient")
	}
}

// guardedBinaryValue is deliberately not valid UTF-8, so a transport that
// round-tripped values through a string somewhere would corrupt it and the
// guard would then be comparing a different value than the caller passed.
var guardedBinaryValue = []byte{0x00, 0xff, 0xfe, 'a', 0x80, '\n', 0x7f}

// guardedRequests returns the requests the server saw for the guarded methods,
// ignoring the admin.Health call Connect makes.
func guardedRequests(srv *e2eServer) []recordedRequest {
	var out []recordedRequest
	for _, r := range srv.Recorded() {
		if r.Method != "admin.Health" {
			out = append(out, r)
		}
	}
	return out
}

// assertOneCall requires that the guarded verb was a single round trip to the
// named method. A read-then-write emulation is not a guarded verb — it is a
// race with a longer window — and this is what makes that visible.
func assertOneCall(t *testing.T, srv *e2eServer, method string) recordedRequest {
	t.Helper()
	reqs := guardedRequests(srv)
	if len(reqs) != 1 {
		t.Fatalf("the transport made %d data-plane calls (%+v); a guarded verb must be ONE "+
			"round trip to %s", len(reqs), reqs, method)
	}
	if reqs[0].Method != method {
		t.Fatalf("call was %q; want %q", reqs[0].Method, method)
	}
	return reqs[0]
}

func TestQUICGuarded_PutIfAbsent_OneCallAndBinarySafe(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("kv.PutIfAbsent", func([]byte) (uint16, []byte) {
		payload, err := proto.Marshal(&pb.PutIfAbsentResponse{Stored: true})
		if err != nil {
			return wire.StatusError, []byte(err.Error())
		}
		return wire.StatusOK, payload
	})

	stored, err := tr.PutIfAbsent(context.Background(), "claim", guardedBinaryValue)
	if err != nil {
		t.Fatalf("PutIfAbsent: %v", err)
	}
	if !stored {
		t.Error("the server reported stored=true and the client returned false")
	}

	got := assertOneCall(t, srv, "kv.PutIfAbsent")
	var sent pb.PutIfAbsentRequest
	if err := proto.Unmarshal(got.Payload, &sent); err != nil {
		t.Fatalf("decode sent payload: %v", err)
	}
	if sent.GetKey() != "claim" {
		t.Errorf("key on the wire = %q, want %q", sent.GetKey(), "claim")
	}
	if string(sent.GetValue()) != string(guardedBinaryValue) {
		t.Errorf("value on the wire = %v, want %v", sent.GetValue(), guardedBinaryValue)
	}
}

func TestQUICGuarded_PutIfAbsent_DeclinedIsNotAnError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("kv.PutIfAbsent", func([]byte) (uint16, []byte) {
		payload, _ := proto.Marshal(&pb.PutIfAbsentResponse{Stored: false})
		return wire.StatusOK, payload
	})

	// Losing the claim is the normal outcome of a race, not a failure. A
	// transport that reported it as an error would make a caller retry the one
	// thing that can never succeed.
	stored, err := tr.PutIfAbsent(context.Background(), "claim", []byte("globex"))
	if err != nil {
		t.Fatalf("a declined PutIfAbsent must not be an error: %v", err)
	}
	if stored {
		t.Error("the server reported stored=false and the client returned true")
	}
	assertOneCall(t, srv, "kv.PutIfAbsent")
}

func TestQUICGuarded_CompareAndDelete_OneCallAndBinarySafe(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("kv.CompareAndDelete", func([]byte) (uint16, []byte) {
		payload, _ := proto.Marshal(&pb.CompareAndDeleteResponse{Deleted: true})
		return wire.StatusOK, payload
	})

	deleted, err := tr.CompareAndDelete(context.Background(), "claim", guardedBinaryValue)
	if err != nil {
		t.Fatalf("CompareAndDelete: %v", err)
	}
	if !deleted {
		t.Error("the server reported deleted=true and the client returned false")
	}

	got := assertOneCall(t, srv, "kv.CompareAndDelete")
	var sent pb.CompareAndDeleteRequest
	if err := proto.Unmarshal(got.Payload, &sent); err != nil {
		t.Fatalf("decode sent payload: %v", err)
	}
	if string(sent.GetExpected()) != string(guardedBinaryValue) {
		t.Errorf("expected on the wire = %v, want %v", sent.GetExpected(), guardedBinaryValue)
	}
}

// The two byte fields of a swap are `expected` and `next`, and nothing but a
// wire assertion catches them being sent in the wrong order — swapping them
// makes the guard compare against the replacement and write the stale value,
// which still returns a plausible boolean.
func TestQUICGuarded_CompareAndSwap_SendsExpectedAndNextDistinctly(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("kv.CompareAndSwap", func([]byte) (uint16, []byte) {
		payload, _ := proto.Marshal(&pb.CompareAndSwapResponse{Swapped: true})
		return wire.StatusOK, payload
	})

	next := []byte{0x01, 0x02, 0xc3, 0x28}
	swapped, err := tr.CompareAndSwap(context.Background(), "claim", guardedBinaryValue, next)
	if err != nil {
		t.Fatalf("CompareAndSwap: %v", err)
	}
	if !swapped {
		t.Error("the server reported swapped=true and the client returned false")
	}

	got := assertOneCall(t, srv, "kv.CompareAndSwap")
	var sent pb.CompareAndSwapRequest
	if err := proto.Unmarshal(got.Payload, &sent); err != nil {
		t.Fatalf("decode sent payload: %v", err)
	}
	if string(sent.GetExpected()) != string(guardedBinaryValue) {
		t.Errorf("expected on the wire = %v, want %v", sent.GetExpected(), guardedBinaryValue)
	}
	if string(sent.GetNext()) != string(next) {
		t.Errorf("next on the wire = %v, want %v", sent.GetNext(), next)
	}
}

// wire.StatusUnimplemented is what the QUIC server answers when the state
// machine behind the request has no handler for the guarded opcode, and also
// what its dispatch loop answers for a method an older server build does not
// register at all. Both mean "this node cannot do that", and the client must
// hand back the TYPED refusal for either: the embedded, gRPC and unix
// transports all do for the same condition (go-qrdb#141), and a consumer that
// had to match message text instead would retry a call that can never succeed.
func TestQUICGuarded_CapabilityRefusalIsTyped(t *testing.T) {
	const detail = "service: statemachine: unknown opcode: 66"

	for _, tc := range []struct {
		name   string
		method string
		call   func(*Transport) error
	}{
		{"PutIfAbsent", "kv.PutIfAbsent", func(tr *Transport) error {
			_, err := tr.PutIfAbsent(context.Background(), "k", []byte("v"))
			return err
		}},
		{"CompareAndDelete", "kv.CompareAndDelete", func(tr *Transport) error {
			_, err := tr.CompareAndDelete(context.Background(), "k", []byte("v"))
			return err
		}},
		{"CompareAndSwap", "kv.CompareAndSwap", func(tr *Transport) error {
			_, err := tr.CompareAndSwap(context.Background(), "k", []byte("v"), []byte("w"))
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, tr := startE2EServer(t)
			srv.SetResponder(tc.method, func([]byte) (uint16, []byte) {
				return wire.StatusUnimplemented, []byte(detail)
			})

			err := tc.call(tr)
			var unsupported *transport.CapabilityUnsupportedError
			if !errors.As(err, &unsupported) {
				t.Fatalf("error = %T (%v), want *transport.CapabilityUnsupportedError", err, err)
			}
			if unsupported.Message != detail {
				t.Errorf("message = %q, want the server's own account %q", unsupported.Message, detail)
			}
		})
	}
}

// An OLDER server has no responder for the guarded methods at all, so its
// dispatch loop answers StatusUnimplemented. A new client that promoted the
// capability must learn "this node cannot do that" from it rather than a
// generic request failure it might retry forever — which is what makes the
// GuardedKVClient assertion safe to add to a transport whose peers upgrade
// independently.
func TestQUICGuarded_OlderServerWithoutTheVerbsRefusesTyped(t *testing.T) {
	_, tr := startE2EServer(t)

	_, err := tr.PutIfAbsent(context.Background(), "k", []byte("v"))
	var unsupported *transport.CapabilityUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("error = %T (%v), want *transport.CapabilityUnsupportedError", err, err)
	}
}

// A guarded call on a closed transport must fail before touching the network.
func TestQUICGuarded_ClosedTransport(t *testing.T) {
	_, tr := startE2EServer(t)
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var closed *transport.ClosedError
	if _, err := tr.PutIfAbsent(context.Background(), "k", nil); !errors.As(err, &closed) {
		t.Errorf("PutIfAbsent error = %T (%v), want *transport.ClosedError", err, err)
	}
	if _, err := tr.CompareAndDelete(context.Background(), "k", nil); !errors.As(err, &closed) {
		t.Errorf("CompareAndDelete error = %T (%v), want *transport.ClosedError", err, err)
	}
	if _, err := tr.CompareAndSwap(context.Background(), "k", nil, nil); !errors.As(err, &closed) {
		t.Errorf("CompareAndSwap error = %T (%v), want *transport.ClosedError", err, err)
	}
}
