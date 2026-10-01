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

package rest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// These tests cover the CLIENT edge of the guarded plane over REST
// (go-qrdb#126). The end-to-end delegation — that the verb reaches the service
// layer's atomic implementation and that its decision is the service's — is
// proven by the go-qrdb#121 conformance matrix, which drives these methods
// against a real node on every engine and mode. What the matrix cannot see is
// what this transport put on the wire, and that is what is checked here:
//
//   - exactly ONE request per call, to the guarded endpoint. This is the
//     assertion that catches an emulated verb: a get-then-put would show up as
//     two requests, or as a request to /kv/{key}, and would still return a
//     plausible boolean.
//   - arbitrary BYTES survive the round trip. REST sends these values as JSON
//     []byte (base64) rather than as the JSON strings /tx uses, because a
//     guard compares bytes; a value mangled by a UTF-8 round trip would make
//     CompareAndDelete refuse a release that should have succeeded, which is a
//     lock that can be taken and never let go.
//   - a 501 arrives as the TYPED capability refusal, so a caller can degrade
//     instead of retrying a call that can never succeed.

// recorder captures every request the transport made, so a test can assert not
// only that the right call happened but that no OTHER call did.
type recorder struct {
	requests []recordedRequest
}

type recordedRequest struct {
	method string
	path   string
	body   []byte
}

// record installs a handler that captures the request and replies with resp.
func (rec *recorder) record(rs *routerServer, method, path string, resp any) {
	rs.handle(exactPath(method, path), func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.requests = append(rec.requests, recordedRequest{method: r.Method, path: r.URL.Path, body: body})
		writeJSON(w, resp)
	})
}

// catchAll records any request that reached no other route. Without it an
// emulated verb's extra GET would 404 and the test would report a confusing
// decode failure instead of naming the real problem.
func (rec *recorder) catchAll(rs *routerServer) {
	rs.handle(func(*http.Request) bool { return true }, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.requests = append(rec.requests, recordedRequest{method: r.Method, path: r.URL.Path, body: body})
		http.NotFound(w, r)
	})
}

// only asserts the transport made exactly one request, to method+path.
func (rec *recorder) only(t *testing.T, method, path string) recordedRequest {
	t.Helper()
	if len(rec.requests) != 1 {
		t.Fatalf("the transport made %d requests (%+v); a guarded verb must be ONE round trip — "+
			"a read-then-write emulation is not a guarded verb, it is a race with a longer window",
			len(rec.requests), rec.requests)
	}
	got := rec.requests[0]
	if got.method != method || got.path != path {
		t.Fatalf("request was %s %s; want %s %s", got.method, got.path, method, path)
	}
	return got
}

func TestTransport_ImplementsGuardedKVClient(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	if _, ok := interface{}(rt).(transport.GuardedKVClient); !ok {
		t.Fatal("REST transport does not implement transport.GuardedKVClient")
	}
}

// binaryValue is deliberately not valid UTF-8. Sending it as a JSON string
// would replace the invalid bytes with U+FFFD, and every assertion about the
// guard's decision would then be about a different value than the caller
// passed.
var binaryValue = []byte{0x00, 0xff, 0xfe, 'a', 0x80, '\n', 0x7f}

func TestRESTGuarded_PutIfAbsent_OneRoundTripAndBinarySafe(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rec := &recorder{}
	rec.record(rs, http.MethodPost, "/kv/put-if-absent", putIfAbsentWireResponse{Stored: true})
	rec.catchAll(rs)

	stored, err := rt.PutIfAbsent(context.Background(), "claim", binaryValue)
	if err != nil {
		t.Fatalf("PutIfAbsent: %v", err)
	}
	if !stored {
		t.Error("the server reported stored=true and the client returned false")
	}

	got := rec.only(t, http.MethodPost, "/kv/put-if-absent")
	var sent putIfAbsentWireRequest
	if err := json.Unmarshal(got.body, &sent); err != nil {
		t.Fatalf("decode sent body %q: %v", got.body, err)
	}
	if sent.Key != "claim" {
		t.Errorf("key on the wire = %q, want %q", sent.Key, "claim")
	}
	if string(sent.Value) != string(binaryValue) {
		t.Errorf("value on the wire = %v, want %v — the guarded plane must carry the caller's "+
			"exact bytes", sent.Value, binaryValue)
	}
}

func TestRESTGuarded_PutIfAbsent_DeclinedIsNotAnError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rec := &recorder{}
	rec.record(rs, http.MethodPost, "/kv/put-if-absent", putIfAbsentWireResponse{Stored: false})
	rec.catchAll(rs)

	// Losing the claim is the normal outcome of a race, not a failure. A
	// transport that reported it as an error would make a caller retry the one
	// thing that can never succeed.
	stored, err := rt.PutIfAbsent(context.Background(), "claim", []byte("globex"))
	if err != nil {
		t.Fatalf("a declined PutIfAbsent must not be an error: %v", err)
	}
	if stored {
		t.Error("the server reported stored=false and the client returned true")
	}
	rec.only(t, http.MethodPost, "/kv/put-if-absent")
}

func TestRESTGuarded_CompareAndDelete_OneRoundTripAndBinarySafe(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rec := &recorder{}
	rec.record(rs, http.MethodPost, "/kv/compare-and-delete", compareAndDeleteWireResponse{Deleted: true})
	rec.catchAll(rs)

	deleted, err := rt.CompareAndDelete(context.Background(), "claim", binaryValue)
	if err != nil {
		t.Fatalf("CompareAndDelete: %v", err)
	}
	if !deleted {
		t.Error("the server reported deleted=true and the client returned false")
	}

	got := rec.only(t, http.MethodPost, "/kv/compare-and-delete")
	var sent compareAndDeleteWireRequest
	if err := json.Unmarshal(got.body, &sent); err != nil {
		t.Fatalf("decode sent body %q: %v", got.body, err)
	}
	if string(sent.Expected) != string(binaryValue) {
		t.Errorf("expected on the wire = %v, want %v", sent.Expected, binaryValue)
	}
}

// The two byte fields of a swap are `expected` and `next`, and nothing but a
// wire assertion catches them being sent in the wrong order — swapping them
// makes the guard compare against the replacement and write the stale value,
// which still returns a plausible boolean.
func TestRESTGuarded_CompareAndSwap_SendsExpectedAndNextDistinctly(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rec := &recorder{}
	rec.record(rs, http.MethodPost, "/kv/compare-and-swap", compareAndSwapWireResponse{Swapped: true})
	rec.catchAll(rs)

	next := []byte{0x01, 0x02, 0xc3, 0x28}
	swapped, err := rt.CompareAndSwap(context.Background(), "claim", binaryValue, next)
	if err != nil {
		t.Fatalf("CompareAndSwap: %v", err)
	}
	if !swapped {
		t.Error("the server reported swapped=true and the client returned false")
	}

	got := rec.only(t, http.MethodPost, "/kv/compare-and-swap")
	var sent compareAndSwapWireRequest
	if err := json.Unmarshal(got.body, &sent); err != nil {
		t.Fatalf("decode sent body %q: %v", got.body, err)
	}
	if string(sent.Expected) != string(binaryValue) {
		t.Errorf("expected on the wire = %v, want %v", sent.Expected, binaryValue)
	}
	if string(sent.Next) != string(next) {
		t.Errorf("next on the wire = %v, want %v", sent.Next, next)
	}
}

// A 501 is what the REST server answers when the state machine behind the
// request has no handler for the guarded opcode (pkg/api/rest.mapHTTPError
// maps service.CapabilityUnsupportedError there). The client must hand that
// back as the TYPED refusal: the embedded, gRPC and unix transports all do for
// the same server-side condition (go-qrdb#141), and a consumer that had to
// match message text instead would retry a call that can never succeed.
func TestRESTGuarded_CapabilityRefusalIsTyped(t *testing.T) {
	const detail = "service: statemachine: unknown opcode: 66"

	for _, tc := range []struct {
		name string
		path string
		call func(*Transport) error
	}{
		{"PutIfAbsent", "/kv/put-if-absent", func(rt *Transport) error {
			_, err := rt.PutIfAbsent(context.Background(), "k", []byte("v"))
			return err
		}},
		{"CompareAndDelete", "/kv/compare-and-delete", func(rt *Transport) error {
			_, err := rt.CompareAndDelete(context.Background(), "k", []byte("v"))
			return err
		}},
		{"CompareAndSwap", "/kv/compare-and-swap", func(rt *Transport) error {
			_, err := rt.CompareAndSwap(context.Background(), "k", []byte("v"), []byte("w"))
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, rs, _ := newExtTransport(t)
			rs.handle(exactPath(http.MethodPost, tc.path), func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotImplemented)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": detail})
			})

			err := tc.call(rt)
			var unsupported *transport.CapabilityUnsupportedError
			if !errors.As(err, &unsupported) {
				t.Fatalf("error = %T (%v), want *transport.CapabilityUnsupportedError", err, err)
			}
			// The refusal must still name what is missing; a typed refusal
			// carrying no account of WHICH capability is absent is not much
			// better than an untyped one for whoever has to act on it.
			if unsupported.Message != detail {
				t.Errorf("message = %q, want the server's own account %q", unsupported.Message, detail)
			}
		})
	}
}

// A database-scoped client's guarded write must land in THAT database. The
// scope travels as the X-QRDB-Database header, applied by the transport's
// round tripper only to paths transport.IsDataPlanePath recognises — so a
// guarded endpoint that fell outside that set would silently write a claim
// into the system database while the caller believed it was scoped, and two
// tenants' claims would collide on one key.
func TestRESTGuarded_CarriesTheDatabaseScope(t *testing.T) {
	rt, rs, _ := newExtTransport(t)

	var gotScope string
	rs.handle(exactPath(http.MethodPost, "/kv/put-if-absent"), func(w http.ResponseWriter, r *http.Request) {
		gotScope = r.Header.Get(transport.DatabaseHeader)
		writeJSON(w, putIfAbsentWireResponse{Stored: true})
	})

	var scopedTr transport.Transport = rt.withDatabase("orders")
	scoped, ok := scopedTr.(transport.GuardedKVClient)
	if !ok {
		t.Fatal("the database-scoped clone does not implement transport.GuardedKVClient")
	}
	if _, err := scoped.PutIfAbsent(context.Background(), "claim", []byte("v")); err != nil {
		t.Fatalf("PutIfAbsent: %v", err)
	}
	if gotScope != "orders" {
		t.Errorf("%s = %q, want %q", transport.DatabaseHeader, gotScope, "orders")
	}
}

// An OLDER server has no guarded routes at all, so it answers 404 (or 405
// where a sibling method matched the path). A new client that promoted the
// GuardedKVClient capability must learn "this node cannot do that" from that,
// not a generic request failure it might retry forever — which is what makes
// the assertion safe to add to a transport whose peers upgrade independently.
//
// Every other transport already gets this right: an older gRPC or unix server
// answers codes.Unimplemented, and an older QUIC server's dispatch loop
// answers wire.StatusUnimplemented. REST was the only one that did not.
func TestRESTGuarded_OlderServerWithoutTheRoutesRefusesTyped(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusMethodNotAllowed} {
		for _, tc := range []struct {
			name string
			path string
			call func(*Transport) error
		}{
			{"PutIfAbsent", "/kv/put-if-absent", func(rt *Transport) error {
				_, err := rt.PutIfAbsent(context.Background(), "k", []byte("v"))
				return err
			}},
			{"CompareAndDelete", "/kv/compare-and-delete", func(rt *Transport) error {
				_, err := rt.CompareAndDelete(context.Background(), "k", []byte("v"))
				return err
			}},
			{"CompareAndSwap", "/kv/compare-and-swap", func(rt *Transport) error {
				_, err := rt.CompareAndSwap(context.Background(), "k", []byte("v"), []byte("w"))
				return err
			}},
		} {
			t.Run(fmt.Sprintf("%d/%s", status, tc.name), func(t *testing.T) {
				rt, rs, _ := newExtTransport(t)
				rs.handle(exactPath(http.MethodPost, tc.path), func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(status)
				})

				err := tc.call(rt)
				var unsupported *transport.CapabilityUnsupportedError
				if !errors.As(err, &unsupported) {
					t.Fatalf("error = %T (%v), want *transport.CapabilityUnsupportedError", err, err)
				}
				if !strings.Contains(unsupported.Message, tc.path) {
					t.Errorf("message %q does not name the absent endpoint", unsupported.Message)
				}
			})
		}
	}
}

// The control: a 404 on some OTHER path is not reclassified, because only the
// guarded endpoints carry the "the key is never in the path" property that
// makes a 404 unambiguous. This is what keeps the classification from becoming
// "any 404 means unsupported".
func TestRESTGuarded_OtherStatusesAreNotReclassified(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/kv/put-if-absent"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		writeJSON(w, map[string]string{"error": "propose failed: disk full"})
	})

	_, err := rt.PutIfAbsent(context.Background(), "k", []byte("v"))
	var unsupported *transport.CapabilityUnsupportedError
	if errors.As(err, &unsupported) {
		t.Fatalf("a server FAULT was reported as an unsupported capability (%v); the caller "+
			"would stop retrying something that may well succeed on the next attempt", err)
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) || reqErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("error = %T (%v), want *transport.RequestError with status 500", err, err)
	}
}

// A guarded call on a closed transport must fail before touching the network.
func TestRESTGuarded_ClosedTransport(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	if err := rt.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var closed *transport.ClosedError
	if _, err := rt.PutIfAbsent(context.Background(), "k", nil); !errors.As(err, &closed) {
		t.Errorf("PutIfAbsent error = %T (%v), want *transport.ClosedError", err, err)
	}
	if _, err := rt.CompareAndDelete(context.Background(), "k", nil); !errors.As(err, &closed) {
		t.Errorf("CompareAndDelete error = %T (%v), want *transport.ClosedError", err, err)
	}
	if _, err := rt.CompareAndSwap(context.Background(), "k", nil, nil); !errors.As(err, &closed) {
		t.Errorf("CompareAndSwap error = %T (%v), want *transport.ClosedError", err, err)
	}
}
