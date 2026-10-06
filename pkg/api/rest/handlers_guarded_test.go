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
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	quicraftproto "github.com/jeremyhahn/go-quicraft/pkg/proto"

	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	grpcapi "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// The guarded verbs over REST (go-qrdb#126). These tests hold the server half
// of the contract: the route is registered under the right permission, the
// handler DELEGATES to service.KVServicer's atomic method with the caller's
// arguments unchanged, and the service's answer is what comes back. Nothing
// here reads the key first — a guarded verb emulated as get-then-put is a race
// with a longer window, and it would satisfy any test that only checked the
// returned boolean.

// guardedBinaryValue is deliberately not valid UTF-8. The endpoints carry
// values as JSON []byte (base64) rather than the JSON strings /tx uses,
// because a guard compares BYTES: a value mangled by a UTF-8 round trip would
// make CompareAndDelete refuse a release that should have succeeded, which is
// a lock that can be taken and never let go.
var guardedBinaryValue = []byte{0x00, 0xff, 0xfe, 'a', 0x80, '\n', 0x7f}

// postGuarded drives one guarded endpoint through the router, so route
// registration is exercised rather than the handler being called directly.
func postGuarded(t *testing.T, srv *Server, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	return rec
}

// A route that is not registered is unreachable, and a route registered under
// the wrong permission is reachable by the wrong callers. The expected
// permissions are the ones qrdb.proto declares on the matching gRPC RPCs: a
// guarded put is still a put and a guarded delete is still a delete, so an
// identity that can reach the verb over gRPC reaches it over REST too.
func TestRegisterRoutes_GuardedVerbs(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	for path, want := range map[string]permission.Permission{
		"/kv/put-if-absent":      permission.KVPut,
		"/kv/compare-and-delete": permission.KVDelete,
		"/kv/compare-and-swap":   permission.KVPut,
	} {
		got, kind, ok := srv.routePerms.Lookup(http.MethodPost, path)
		if !ok {
			t.Errorf("POST %s declares no permission; default-deny makes it unreachable", path)
			continue
		}
		if kind != authmw.RESTRoutePermission {
			t.Errorf("POST %s is registered as route kind %v; a guarded write must be "+
				"permission-gated, not public or merely authenticated", path, kind)
		}
		if got != want {
			t.Errorf("POST %s requires %q, want %q — the same permission its gRPC RPC declares",
				path, got, want)
		}
	}
}

func TestHandleKVPutIfAbsent_DelegatesAndReturnsStored(t *testing.T) {
	for _, stored := range []bool{true, false} {
		var gotKey string
		var gotValue []byte
		kvSvc := &mockKVService{
			putIfAbsentFn: func(_ context.Context, key string, v []byte) (bool, error) {
				gotKey, gotValue = key, v
				return stored, nil
			},
			// A guarded verb must not consult the store itself. If the handler
			// ever grew a read-then-write emulation it would come through
			// here, and failing loudly is what makes that visible.
			getFn: func(context.Context, string) ([]byte, error) {
				t.Error("handleKVPutIfAbsent read the key; the guard belongs in the state machine")
				return nil, nil
			},
		}
		srv := NewServer(kvSvc, newDefaultAdminSvc(), "127.0.0.1:0")

		rec := postGuarded(t, srv, "/kv/put-if-absent",
			putIfAbsentRequest{Key: "claim", Value: guardedBinaryValue})

		// A declined store is a 200, not an error status: losing the race is
		// the normal outcome of a claim, and a caller told it "failed" would
		// retry the one thing that can never succeed.
		assertStatus(t, rec, http.StatusOK)
		if gotKey != "claim" {
			t.Errorf("service received key %q, want %q", gotKey, "claim")
		}
		if !bytes.Equal(gotValue, guardedBinaryValue) {
			t.Errorf("service received value %v, want %v — the endpoint must carry the "+
				"caller's exact bytes", gotValue, guardedBinaryValue)
		}

		var resp putIfAbsentResponse
		decodeJSON(t, rec, &resp)
		if resp.Stored != stored {
			t.Errorf("stored = %v, want the service's answer %v", resp.Stored, stored)
		}
	}
}

func TestHandleKVCompareAndDelete_DelegatesAndReturnsDeleted(t *testing.T) {
	for _, deleted := range []bool{true, false} {
		var gotKey string
		var gotExpected []byte
		kvSvc := &mockKVService{
			compareAndDeleteFn: func(_ context.Context, key string, e []byte) (bool, error) {
				gotKey, gotExpected = key, e
				return deleted, nil
			},
			getFn: func(context.Context, string) ([]byte, error) {
				t.Error("handleKVCompareAndDelete read the key; the guard belongs in the state machine")
				return nil, nil
			},
		}
		srv := NewServer(kvSvc, newDefaultAdminSvc(), "127.0.0.1:0")

		rec := postGuarded(t, srv, "/kv/compare-and-delete",
			compareAndDeleteRequest{Key: "claim", Expected: guardedBinaryValue})

		assertStatus(t, rec, http.StatusOK)
		if gotKey != "claim" {
			t.Errorf("service received key %q, want %q", gotKey, "claim")
		}
		if !bytes.Equal(gotExpected, guardedBinaryValue) {
			t.Errorf("service received expected %v, want %v", gotExpected, guardedBinaryValue)
		}

		var resp compareAndDeleteResponse
		decodeJSON(t, rec, &resp)
		if resp.Deleted != deleted {
			t.Errorf("deleted = %v, want the service's answer %v", resp.Deleted, deleted)
		}
	}
}

// `expected` and `next` are two byte fields of one request, and nothing but
// distinct values catches them being handed to the service the wrong way round
// — that makes the guard compare against the replacement and write the stale
// value, which still returns a plausible boolean.
func TestHandleKVCompareAndSwap_DelegatesExpectedAndNextDistinctly(t *testing.T) {
	next := []byte{0x02, 0xc3, 0x28}

	for _, swapped := range []bool{true, false} {
		var gotKey string
		var gotExpected, gotNext []byte
		kvSvc := &mockKVService{
			compareAndSwapFn: func(_ context.Context, key string, e, n []byte) (bool, error) {
				gotKey, gotExpected, gotNext = key, e, n
				return swapped, nil
			},
			getFn: func(context.Context, string) ([]byte, error) {
				t.Error("handleKVCompareAndSwap read the key; the guard belongs in the state machine")
				return nil, nil
			},
		}
		srv := NewServer(kvSvc, newDefaultAdminSvc(), "127.0.0.1:0")

		rec := postGuarded(t, srv, "/kv/compare-and-swap",
			compareAndSwapRequest{Key: "claim", Expected: guardedBinaryValue, Next: next})

		assertStatus(t, rec, http.StatusOK)
		if gotKey != "claim" {
			t.Errorf("service received key %q, want %q", gotKey, "claim")
		}
		if !bytes.Equal(gotExpected, guardedBinaryValue) {
			t.Errorf("service received expected %v, want %v", gotExpected, guardedBinaryValue)
		}
		if !bytes.Equal(gotNext, next) {
			t.Errorf("service received next %v, want %v", gotNext, next)
		}

		var resp compareAndSwapResponse
		decodeJSON(t, rec, &resp)
		if resp.Swapped != swapped {
			t.Errorf("swapped = %v, want the service's answer %v", resp.Swapped, swapped)
		}
	}
}

// A state machine with no handler for the guarded opcode refuses with
// service.CapabilityUnsupportedError, and that must leave REST as 501 so the
// SDK can hand the caller a typed transport.CapabilityUnsupportedError instead
// of a message string it would have to match.
func TestHandleGuarded_CapabilityRefusalIsNotImplemented(t *testing.T) {
	refuse := &service.CapabilityUnsupportedError{Message: "statemachine: unknown opcode: 66"}

	for _, tc := range []struct {
		name string
		path string
		svc  *mockKVService
		body any
	}{
		{
			name: "PutIfAbsent",
			path: "/kv/put-if-absent",
			svc: &mockKVService{putIfAbsentFn: func(context.Context, string, []byte) (bool, error) {
				return false, refuse
			}},
			body: putIfAbsentRequest{Key: "k", Value: []byte("v")},
		},
		{
			name: "CompareAndDelete",
			path: "/kv/compare-and-delete",
			svc: &mockKVService{compareAndDeleteFn: func(context.Context, string, []byte) (bool, error) {
				return false, refuse
			}},
			body: compareAndDeleteRequest{Key: "k", Expected: []byte("v")},
		},
		{
			name: "CompareAndSwap",
			path: "/kv/compare-and-swap",
			svc: &mockKVService{compareAndSwapFn: func(context.Context, string, []byte, []byte) (bool, error) {
				return false, refuse
			}},
			body: compareAndSwapRequest{Key: "k", Expected: []byte("v"), Next: []byte("w")},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer(tc.svc, newDefaultAdminSvc(), "127.0.0.1:0")
			rec := postGuarded(t, srv, tc.path, tc.body)

			assertStatus(t, rec, http.StatusNotImplemented)
			if !bytes.Contains(rec.Body.Bytes(), []byte("unknown opcode: 66")) {
				t.Errorf("body %q does not name the absent opcode; a refusal that does not "+
					"say WHAT is missing is little better than an untyped one", rec.Body)
			}
		})
	}
}

// A server built with no KV service must answer 501, not panic and not 404.
// The routes are registered unconditionally (the handlers nil-check the
// service) so that a caller learns the operation is unsupported by THIS server
// rather than that the path does not exist.
func TestHandleGuarded_NilKVServiceIsNotImplemented(t *testing.T) {
	for _, tc := range []struct {
		path string
		body any
	}{
		{"/kv/put-if-absent", putIfAbsentRequest{Key: "k", Value: []byte("v")}},
		{"/kv/compare-and-delete", compareAndDeleteRequest{Key: "k", Expected: []byte("v")}},
		{"/kv/compare-and-swap", compareAndSwapRequest{Key: "k", Expected: []byte("v"), Next: []byte("w")}},
	} {
		srv := NewServer(nil, nil, "127.0.0.1:0")
		rec := postGuarded(t, srv, tc.path, tc.body)
		assertStatus(t, rec, http.StatusNotImplemented)
	}
}

// The WIRE bound, at both edges. An unbounded read let any caller holding
// kv:put or kv:delete make the server allocate a whole body before JSON
// decoding or any service check ran — an HTTP read timeout bounds how LONG a
// request takes, never how many BYTES it claims.
//
// The limits are passed in rather than read from the constants so the
// rejecting half costs a few bytes instead of the production bound's worth of
// memory — allocating that to prove a memory guard would be self-defeating.
// The accepting half is what stops the fix from being "reject everything",
// which would refuse legitimate writes just as effectively as the DoS would;
// and it is placed exactly AT the limit, because an off-by-one in a size guard
// is invisible to a merely-large body.
func TestDecodeGuardedRequest_WireBound(t *testing.T) {
	// Padding goes INSIDE the object, before the closing brace, so the decoder
	// must read through it. Trailing bytes after the closing brace would not
	// work: json.Decoder stops at the end of the first complete value, so a
	// body "one byte longer" in trailing whitespace is never read and the
	// bound is never reached — a fixture that would have made this test pass
	// while measuring nothing.
	padded := func(spaces int) []byte {
		body, err := json.Marshal(putIfAbsentRequest{Key: "k", Value: []byte("v")})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return append(append(body[:len(body)-1], bytes.Repeat([]byte(" "), spaces)...), '}')
	}

	atLimit := padded(8)
	limit := int64(len(atLimit))

	t.Run("at the limit is accepted", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/kv/put-if-absent", bytes.NewReader(atLimit))
		rec := httptest.NewRecorder()

		decoded, ok := decodeGuardedRequest[putIfAbsentRequest](rec, req, limit, guardedEntryCeiling)
		if !ok {
			t.Fatalf("a body of exactly the limit was refused: %s", rec.Body)
		}
		if decoded.Key != "k" || string(decoded.Value) != "v" {
			t.Errorf("decoded %+v, want key=k value=v", decoded)
		}
	})

	t.Run("one byte over is 413", func(t *testing.T) {
		over := padded(9)
		if int64(len(over)) != limit+1 {
			t.Fatalf("fixture is %d bytes, want exactly %d", len(over), limit+1)
		}
		req := httptest.NewRequest(http.MethodPost, "/kv/put-if-absent", bytes.NewReader(over))
		rec := httptest.NewRecorder()

		if _, ok := decodeGuardedRequest[putIfAbsentRequest](rec, req, limit, guardedEntryCeiling); ok {
			t.Fatal("a body one byte over the limit was accepted")
		}
		assertStatus(t, rec, http.StatusRequestEntityTooLarge)
		if !bytes.Contains(rec.Body.Bytes(), []byte("limit")) {
			t.Errorf("body %q does not name the limit; a caller that cannot see the bound "+
				"cannot choose a size that will be accepted", rec.Body)
		}
	})

	t.Run("an endless body is refused rather than read", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/kv/put-if-absent", newEndlessJSONBody())
		req.ContentLength = -1
		rec := httptest.NewRecorder()

		if _, ok := decodeGuardedRequest[putIfAbsentRequest](rec, req, limit, guardedEntryCeiling); ok {
			t.Fatal("an unbounded body was accepted")
		}
		assertStatus(t, rec, http.StatusRequestEntityTooLarge)
	})
}

// endlessJSONBody is a body that opens a valid JSON string and never closes
// it, so a decoder has no reason to stop and the BOUND is what stops it.
// Writing a real 10 MiB request into a test would spend the very memory the
// bound exists to protect.
type endlessJSONBody struct{ prefix []byte }

func newEndlessJSONBody() *endlessJSONBody {
	return &endlessJSONBody{prefix: []byte(`{"key":"`)}
}

func (b *endlessJSONBody) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) && len(b.prefix) > 0 {
		p[n] = b.prefix[0]
		b.prefix = b.prefix[1:]
		n++
	}
	for ; n < len(p); n++ {
		p[n] = 'a'
	}
	return n, nil
}

// The AGGREGATE payload bound, at both edges and across both value fields.
//
// This is the check the first cut of this fix did not have. With only a wire
// bound, and with that bound budgeted per FIELD, a CompareAndSwap could claim
// ~267 MiB encoded and another ~200 MiB decoded — the field count multiplied
// the ceiling. Summing is the fix, and the two-field case is what proves it:
// each value alone is comfortably under the limit and together they are over.
func TestDecodeGuardedRequest_PayloadBound(t *testing.T) {
	const payloadLimit = 64

	half := bytes.Repeat([]byte("x"), payloadLimit/2)

	t.Run("two fields that each fit but together do not", func(t *testing.T) {
		body, err := json.Marshal(compareAndSwapRequest{Key: "k", Expected: half, Next: half})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, "/kv/compare-and-swap", bytes.NewReader(body))
		rec := httptest.NewRecorder()

		if _, ok := decodeGuardedRequest[compareAndSwapRequest](rec, req, maxGuardedRequestBytes, payloadLimit+compareAndSwapOverheadBytes); ok {
			t.Fatal("expected + next summed past the limit and were accepted; the bound is " +
				"per field, which multiplies the ceiling by the field count")
		}
		assertStatus(t, rec, http.StatusRequestEntityTooLarge)
		if !bytes.Contains(rec.Body.Bytes(), []byte("key and values total")) {
			t.Errorf("body %q does not distinguish the decoded-content limit from the wire "+
				"limit; a caller told only that its body was too big would shrink the "+
				"wrong thing", rec.Body)
		}
	})

	t.Run("exactly at the limit is accepted", func(t *testing.T) {
		// key + expected + next == payloadLimit exactly.
		value := bytes.Repeat([]byte("x"), payloadLimit-len("k")-1)
		req0 := compareAndSwapRequest{Key: "k", Expected: value, Next: []byte("y")}
		if got := req0.payloadBytes(); got != payloadLimit {
			t.Fatalf("fixture is %d bytes, want exactly %d", got, payloadLimit)
		}
		body, err := json.Marshal(req0)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, "/kv/compare-and-swap", bytes.NewReader(body))
		rec := httptest.NewRecorder()

		if _, ok := decodeGuardedRequest[compareAndSwapRequest](rec, req, maxGuardedRequestBytes, payloadLimit+compareAndSwapOverheadBytes); !ok {
			t.Fatalf("a payload of exactly the limit was refused: %s", rec.Body)
		}
	})

	t.Run("one byte over is 413", func(t *testing.T) {
		value := bytes.Repeat([]byte("x"), payloadLimit-len("k"))
		req0 := compareAndSwapRequest{Key: "k", Expected: value, Next: []byte("y")}
		if got := req0.payloadBytes(); got != payloadLimit+1 {
			t.Fatalf("fixture is %d bytes, want exactly %d", got, payloadLimit+1)
		}
		body, err := json.Marshal(req0)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, "/kv/compare-and-swap", bytes.NewReader(body))
		rec := httptest.NewRecorder()

		if _, ok := decodeGuardedRequest[compareAndSwapRequest](rec, req, maxGuardedRequestBytes, payloadLimit+compareAndSwapOverheadBytes); ok {
			t.Fatal("a payload one byte over the limit was accepted")
		}
		assertStatus(t, rec, http.StatusRequestEntityTooLarge)
	})
}

// payloadBytes must SUM, per shape. A shape that returned only its largest
// field would satisfy every assertion above that uses one value.
func TestGuardedRequests_PayloadBytesSum(t *testing.T) {
	if got := (putIfAbsentRequest{Key: "ab", Value: []byte("cde")}).payloadBytes(); got != 5 {
		t.Errorf("putIfAbsent payloadBytes = %d, want 5", got)
	}
	if got := (compareAndDeleteRequest{Key: "ab", Expected: []byte("cde")}).payloadBytes(); got != 5 {
		t.Errorf("compareAndDelete payloadBytes = %d, want 5", got)
	}
	if got := (compareAndSwapRequest{Key: "ab", Expected: []byte("cde"), Next: []byte("fghi")}).payloadBytes(); got != 9 {
		t.Errorf("compareAndSwap payloadBytes = %d, want 9 — the two values must be SUMMED, "+
			"not budgeted separately", got)
	}
}

// Trailing data must be REFUSED, not silently ignored.
//
// A JSON decoder stops at the end of the first top-level value, so
// `{"key":"claim",...}{"key":"other"}` would decode as the first object and the
// handler would perform a guarded MUTATION on malformed input and report
// success. That is a fail-open parse path on a write endpoint, and the earlier
// version of these tests avoided trailing bytes rather than asserting they are
// rejected — which is how the gap survived a review round.
func TestDecodeGuardedRequest_RefusesTrailingData(t *testing.T) {
	first, err := json.Marshal(putIfAbsentRequest{Key: "claim", Value: []byte("v")})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	second, err := json.Marshal(putIfAbsentRequest{Key: "other", Value: []byte("w")})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"a second object", append(append([]byte(nil), first...), second...)},
		{"a stray token", append(append([]byte(nil), first...), []byte("null")...)},
		{"a stray brace", append(append([]byte(nil), first...), '}')},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/kv/put-if-absent", bytes.NewReader(tc.body))
			rec := httptest.NewRecorder()

			if _, ok := decodeGuardedRequest[putIfAbsentRequest](rec, req, maxGuardedRequestBytes, guardedEntryCeiling); ok {
				t.Fatal("a body with trailing data was accepted; the handler would have " +
					"mutated the store on malformed input and reported success")
			}
			assertStatus(t, rec, http.StatusBadRequest)
		})
	}

	// The controls. Trailing WHITESPACE is legal JSON framing and every
	// encoder emits at least a newline, so requiring EOF must not become
	// "reject anything with whitespace at the end" — that would be the same
	// class of error as the two it fixes, in the rejecting direction.
	for _, trailer := range []string{"\n", " ", "\n\n", " \t\r\n  "} {
		body := append(append([]byte(nil), first...), trailer...)
		req := httptest.NewRequest(http.MethodPost, "/kv/put-if-absent", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		if _, ok := decodeGuardedRequest[putIfAbsentRequest](rec, req, maxGuardedRequestBytes, guardedEntryCeiling); !ok {
			t.Errorf("a single value followed by %q was refused: %s", trailer, rec.Body)
		}
	}
}

// An unknown JSON member must be REFUSED, on all three verbs.
//
// Ignoring one is a fail-open parse path on a state-changing endpoint: a
// misspelled `value` decodes as nil and PutIfAbsent stores an EMPTY value while
// reporting success, and a misspelled `expected` makes the guard compare
// against nil — a typo performing a mutation. The repository is pre-release and
// promises no wire compatibility, so there is no older-server argument for
// tolerating it.
func TestDecodeGuardedRequest_RefusesUnknownFields(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		call func(*httptest.ResponseRecorder, *http.Request) bool
	}{
		{
			name: "PutIfAbsent, misspelled value",
			body: `{"key":"claim","vale":"dg=="}`,
			call: func(rec *httptest.ResponseRecorder, r *http.Request) bool {
				_, ok := decodeGuardedRequest[putIfAbsentRequest](rec, r, maxGuardedRequestBytes, guardedEntryCeiling)
				return ok
			},
		},
		{
			name: "CompareAndDelete, misspelled expected",
			body: `{"key":"claim","expcted":"dg=="}`,
			call: func(rec *httptest.ResponseRecorder, r *http.Request) bool {
				_, ok := decodeGuardedRequest[compareAndDeleteRequest](rec, r, maxGuardedRequestBytes, guardedEntryCeiling)
				return ok
			},
		},
		{
			name: "CompareAndSwap, misspelled next",
			body: `{"key":"claim","expected":"dg==","nxt":"dw=="}`,
			call: func(rec *httptest.ResponseRecorder, r *http.Request) bool {
				_, ok := decodeGuardedRequest[compareAndSwapRequest](rec, r, maxGuardedRequestBytes, guardedEntryCeiling)
				return ok
			},
		},
		{
			name: "an extra member alongside correct ones",
			body: `{"key":"claim","value":"dg==","ttl":30}`,
			call: func(rec *httptest.ResponseRecorder, r *http.Request) bool {
				_, ok := decodeGuardedRequest[putIfAbsentRequest](rec, r, maxGuardedRequestBytes, guardedEntryCeiling)
				return ok
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/kv/put-if-absent", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()

			if tc.call(rec, req) {
				t.Fatal("an unknown JSON member was accepted; the handler would have mutated " +
					"the store with a zero-value field and reported success")
			}
			assertStatus(t, rec, http.StatusBadRequest)
		})
	}

	// The control: the exact documented members are still accepted. Refusing
	// unknown fields must not become refusing the schema.
	for _, tc := range []struct {
		name string
		body string
		call func(*httptest.ResponseRecorder, *http.Request) bool
	}{
		{"PutIfAbsent", `{"key":"claim","value":"dg=="}`, func(rec *httptest.ResponseRecorder, r *http.Request) bool {
			_, ok := decodeGuardedRequest[putIfAbsentRequest](rec, r, maxGuardedRequestBytes, guardedEntryCeiling)
			return ok
		}},
		{"CompareAndDelete", `{"key":"claim","expected":"dg=="}`, func(rec *httptest.ResponseRecorder, r *http.Request) bool {
			_, ok := decodeGuardedRequest[compareAndDeleteRequest](rec, r, maxGuardedRequestBytes, guardedEntryCeiling)
			return ok
		}},
		{"CompareAndSwap", `{"key":"claim","expected":"dg==","next":"dw=="}`, func(rec *httptest.ResponseRecorder, r *http.Request) bool {
			_, ok := decodeGuardedRequest[compareAndSwapRequest](rec, r, maxGuardedRequestBytes, guardedEntryCeiling)
			return ok
		}},
		// Omitting an optional value is not the same as misspelling it: a
		// PutIfAbsent of an empty value is a legal request.
		{"PutIfAbsent with no value", `{"key":"claim"}`, func(rec *httptest.ResponseRecorder, r *http.Request) bool {
			_, ok := decodeGuardedRequest[putIfAbsentRequest](rec, r, maxGuardedRequestBytes, guardedEntryCeiling)
			return ok
		}},
	} {
		t.Run("accepts/"+tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/kv/put-if-absent", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			if !tc.call(rec, req) {
				t.Fatalf("a well-formed request was refused: %s", rec.Body)
			}
		})
	}
}

// The key ceiling is a PROTOCOL limit — the command encoders frame the key
// with a two-byte length prefix — so it is reported as an over-long key rather
// than folded into the request-size error. A caller told only "too large"
// would shrink the value, which is not the field at fault.
func TestDecodeGuardedRequest_KeyCeiling(t *testing.T) {
	t.Run("at the ceiling is accepted", func(t *testing.T) {
		body, err := json.Marshal(putIfAbsentRequest{
			Key: strings.Repeat("k", maxGuardedKeyBytes), Value: []byte("v")})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, "/kv/put-if-absent", bytes.NewReader(body))
		rec := httptest.NewRecorder()

		if _, ok := decodeGuardedRequest[putIfAbsentRequest](rec, req, maxGuardedRequestBytes, guardedEntryCeiling); !ok {
			t.Fatalf("a key of exactly the protocol ceiling was refused: %s", rec.Body)
		}
	})

	t.Run("one byte over is 413 naming the key", func(t *testing.T) {
		body, err := json.Marshal(putIfAbsentRequest{
			Key: strings.Repeat("k", maxGuardedKeyBytes+1), Value: []byte("v")})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, "/kv/put-if-absent", bytes.NewReader(body))
		rec := httptest.NewRecorder()

		if _, ok := decodeGuardedRequest[putIfAbsentRequest](rec, req, maxGuardedRequestBytes, guardedEntryCeiling); ok {
			t.Fatal("a key over the protocol ceiling was accepted; the command encoder " +
				"would have failed at propose time instead")
		}
		assertStatus(t, rec, http.StatusRequestEntityTooLarge)
		if !bytes.Contains(rec.Body.Bytes(), []byte("key is")) {
			t.Errorf("body %q does not say the KEY was too long; a caller would shrink "+
				"the value, which is not the field at fault", rec.Body)
		}
	})
}

// A legal key that JSON escaping expands must still get through the coarse
// wire bound. The first cut sized that bound as if every payload byte expanded
// like base64 (4/3); a 65,535-byte key of quotes doubles on the wire and a key
// of control bytes expands sixfold, so a request whose DECODED content was
// legal could be refused with 413 before the precise check ever ran — and
// refused only on REST, while gRPC, unix and QUIC carried it.
func TestDecodeGuardedRequest_LegalKeyThatEscapesIsNotRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
	}{
		{"quotes, two bytes each on the wire", strings.Repeat(`"`, maxGuardedKeyBytes)},
		{"backslashes, two bytes each", strings.Repeat(`\`, maxGuardedKeyBytes)},
		{"control bytes, six bytes each", strings.Repeat("\x01", maxGuardedKeyBytes)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(putIfAbsentRequest{Key: tc.key, Value: []byte("v")})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if int64(len(body)) > maxGuardedRequestBytes {
				t.Fatalf("the encoded body is %d bytes, over the %d-byte wire bound — a legal "+
					"key was made unsendable by the coarse guard", len(body), maxGuardedRequestBytes)
			}

			req := httptest.NewRequest(http.MethodPost, "/kv/put-if-absent", bytes.NewReader(body))
			rec := httptest.NewRecorder()

			decoded, ok := decodeGuardedRequest[putIfAbsentRequest](rec, req, maxGuardedRequestBytes, guardedEntryCeiling)
			if !ok {
				t.Fatalf("a legal key was refused: %s", rec.Body)
			}
			if decoded.Key != tc.key {
				t.Error("the key did not survive the round trip")
			}
		})
	}
}

// The ceilings, asserted against THE ENCODERS rather than against constants
// this file also wrote.
//
// This is the test the previous four rounds did not have. Each of those
// asserted the bound against arithmetic written alongside it, so a wrong
// derivation and a right one were indistinguishable — the test agreed with
// whatever the constant said. Here the largest payload each verb's ceiling
// admits is handed to that verb's REAL encoder and required to fit one Raft
// entry, and one byte more is required not to.
//
// It is also the cross-transport assertion, in the only form that is
// decisive for what it still claims: no GUARDED VERB's own precise ceiling
// ever asks the state machine to propose more than one Raft entry, on any
// transport, regardless of that transport's own wire-level request limit.
//
// Every transport's wire-level limit now sits BELOW the entry ceiling,
// because go-qrdb#576 gave all of them the same small pre-auth message cap
// (REST's DefaultMaxBodyBytes, gRPC/Unix's DefaultMaxMessageBytes, QUIC's
// wire.DefaultMaxPayloadBytes, MCP's own body cap -- all 4 MiB by default),
// applied before any guarded-verb-specific, Raft-entry-derived coarse bound
// (REST's maxGuardedRequestBytes) ever runs. TestGuardedBounds_NotMorePermissiveThanGRPC
// proves REST's cap and gRPC's cap are the same constant, closing the gap a
// previous revision of this comment left open for the chunked-import
// redesign to resolve: the pre-auth cap is that resolution, and a caller
// with a larger payload (bulk import) now uses the chunked import session
// API instead of a single oversized request on any transport.
//
// A payload that encodes within one entry is accepted by all of them; a payload
// that does not is refused by all of them. Standing up two servers to exchange
// an 8 MiB request would measure the same fact less directly and much more
// expensively.
//
// The residual difference, stated rather than hidden: REST refuses at the edge
// with 413, the others fail at encode time. No request that would have
// succeeded is refused anywhere.
func TestGuardedCeilings_AgreeWithTheEncoders(t *testing.T) {
	entry := int(quicraftproto.MaxEntrySize)

	// A key at the protocol ceiling, so the ceiling is exercised with the
	// largest key any of these commands can frame.
	key := strings.Repeat("k", maxGuardedKeyBytes)

	for _, tc := range []struct {
		verb string
		req  guardedRequest
		// encode builds the command the service layer would propose for a
		// request whose payload (key + values) totals payload bytes.
		encode func(key string, valueBytes int) ([]byte, error)
	}{
		{
			verb: "PutIfAbsent",
			req:  putIfAbsentRequest{},
			encode: func(key string, n int) ([]byte, error) {
				return kv.EncodePutIfAbsent([]byte(key), make([]byte, n))
			},
		},
		{
			verb: "CompareAndDelete",
			req:  compareAndDeleteRequest{},
			encode: func(key string, n int) ([]byte, error) {
				return protocol.EncodeCompareAndDelete([]byte(key), make([]byte, n))
			},
		},
		{
			verb: "CompareAndSwap",
			req:  compareAndSwapRequest{},
			encode: func(key string, n int) ([]byte, error) {
				// The two values share the payload budget; split them so both
				// fields are exercised and `expected` carries its length frame.
				return protocol.EncodeCompareAndSwap([]byte(key), make([]byte, n/2), make([]byte, n-n/2))
			},
		},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			ceiling := entry - tc.req.commandOverheadBytes()

			cmd, err := tc.encode(key, ceiling-len(key))
			if err != nil {
				t.Fatalf("encoding a payload at the ceiling failed: %v", err)
			}
			if len(cmd) != entry {
				t.Errorf("a payload at the ceiling encodes to %d bytes, want exactly the entry "+
					"ceiling %d — the limit is leaving room unused (so REST refuses what the "+
					"other transports carry) or exceeding it (so REST admits what nothing can "+
					"propose)", len(cmd), entry)
			}

			over, err := tc.encode(key, ceiling-len(key)+1)
			if err != nil {
				// An encoder that refuses outright is also a correct answer:
				// the payload is not encodable, which is the point.
				return
			}
			if len(over) <= entry {
				t.Errorf("one byte past the ceiling still encodes to %d bytes, within the entry "+
					"ceiling %d; the limit is too strict and REST refuses a request the other "+
					"transports accept", len(over), entry)
			}
		})
	}
}

// The coarse bound must admit the worst request the precise checks accept.
// MEASURED, not computed: the arithmetic that used to justify this number is
// exactly what kept being wrong, so the relationship is established by
// building the request and looking at it.
func TestMaxGuardedRequestBytes_AdmitsTheWorstLegalRequest(t *testing.T) {
	// The largest key, made entirely of the byte that escapes worst inside a
	// JSON string (\u0001 — six bytes on the wire for one), with the values
	// filling the rest of the verb's payload budget.
	req := compareAndSwapRequest{Key: strings.Repeat("\x01", maxGuardedKeyBytes)}
	remaining := guardedEntryCeiling - compareAndSwapOverheadBytes - len(req.Key)
	req.Expected = make([]byte, remaining/2)
	req.Next = make([]byte, remaining-remaining/2)

	if req.payloadBytes() != guardedEntryCeiling-compareAndSwapOverheadBytes {
		t.Fatalf("the fixture is %d payload bytes, not the ceiling %d",
			req.payloadBytes(), guardedEntryCeiling-compareAndSwapOverheadBytes)
	}

	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if int64(len(body)) > maxGuardedRequestBytes {
		t.Fatalf("the worst legal request encodes to %d bytes, over the %d-byte coarse bound — "+
			"REST would refuse it before the precise, per-verb checks ran even if the "+
			"universal pre-auth cap (DefaultMaxBodyBytes) were raised enough to let it "+
			"reach this check at all", len(body), maxGuardedRequestBytes)
	}
	t.Logf("worst legal request: %d encoded bytes against a %d-byte coarse bound",
		len(body), maxGuardedRequestBytes)
}

// The parity claim, asserted rather than stated.
//
// go-qrdb#576 gave every transport the SAME small pre-auth message cap
// (DefaultMaxBodyBytes here, grpcapi.DefaultMaxMessageBytes on gRPC and
// Unix, wire.DefaultMaxPayloadBytes on QUIC), applied before the guarded
// handler's own, larger, Raft-entry-derived coarse bound
// (maxGuardedRequestBytes) ever runs. That earlier bound is what used to
// make the two transports agree ("neither can carry a guarded request past
// one Raft entry, so the entry ceiling is the operative limit on both");
// now the universal pre-auth cap is tighter than the entry ceiling on
// every transport, so IT is the operative limit everywhere, and it is by
// construction identical across transports -- there is no transport to be
// more permissive than.
//
// What still has to hold, and what this test still asserts: the
// guarded-specific per-verb ceilings must stay under what the state
// machine can propose, so a request this package's OWN finer-grained
// checks accept is never one Raft refuses regardless of which transport
// carried it.
func TestGuardedBounds_NotMorePermissiveThanGRPC(t *testing.T) {
	if DefaultMaxBodyBytes != int64(grpcapi.DefaultMaxMessageBytes) {
		t.Fatalf("REST's pre-auth body cap (%d) and gRPC's pre-auth message cap (%d) have "+
			"diverged; go-qrdb#576 requires one small cap, identical on every transport",
			DefaultMaxBodyBytes, grpcapi.DefaultMaxMessageBytes)
	}
	// REST must not admit a payload the state machine cannot propose, which
	// is the only way it could be more permissive than another transport
	// reaching the same state machine. The per-verb ceilings are all
	// strictly under one entry by construction;
	// TestGuardedCeilings_AgreeWithTheEncoders proves it against the encoders.
	for _, req := range []guardedRequest{
		putIfAbsentRequest{}, compareAndDeleteRequest{}, compareAndSwapRequest{},
	} {
		if guardedEntryCeiling-req.commandOverheadBytes() >= int(quicraftproto.MaxEntrySize) {
			t.Errorf("%T would admit a guarded payload the state machine cannot propose", req)
		}
	}
}

// A malformed body is the CALLER's error, not the server's.
func TestHandleGuarded_MalformedBodyIsBadRequest(t *testing.T) {
	for _, path := range []string{
		"/kv/put-if-absent", "/kv/compare-and-delete", "/kv/compare-and-swap",
	} {
		srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte("{not json")))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.dispatch(rec, req)

		assertStatus(t, rec, http.StatusBadRequest)
	}
}
