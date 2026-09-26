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

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The REST transport carries the guarded write verbs (go-qrdb#126).
//
// This assertion is the whole capability contract: transport.GuardedKVClient
// is detected by type assertion, so a consumer's `kv.(GuardedKVClient)` starts
// succeeding the moment this line compiles. It must therefore be added ONLY
// alongside methods that carry the verb's atomicity, never its name alone — a
// transport that answers the assertion and then emulates a guarded write with
// a read followed by a write is strictly worse than one that declines the
// assertion, because the consumer will build a distributed lock on top of it.
//
// The three methods below satisfy that: each POSTs one request document and
// returns the server's answer. No Get precedes any of them and no
// read-then-write fallback follows. Server-side the matching handler calls
// service.KVServicer's atomic method directly (pkg/api/rest's
// handleKVPutIfAbsent and siblings), so the condition is evaluated inside the
// state machine exactly as it is for the embedded and gRPC transports.
var _ transport.GuardedKVClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// Wire types for the guarded REST endpoints
// ---------------------------------------------------------------------------
//
// Values are []byte, which encoding/json renders as base64. This deliberately
// differs from the transaction endpoint's JSON-string values: a guard compares
// BYTES, and a value that did not survive a UTF-8 round trip would make
// CompareAndDelete refuse a release that should have succeeded — a lock that
// can be taken and never let go. The fields mirror pkg/api/rest's
// putIfAbsentRequest and siblings exactly.

// putIfAbsentWireRequest matches the JSON payload for POST /kv/put-if-absent.
type putIfAbsentWireRequest struct {
	Key   string `json:"key"`
	Value []byte `json:"value,omitempty"`
}

// putIfAbsentWireResponse matches the JSON payload returned by
// POST /kv/put-if-absent.
type putIfAbsentWireResponse struct {
	Stored bool `json:"stored"`
}

// compareAndDeleteWireRequest matches the JSON payload for
// POST /kv/compare-and-delete.
type compareAndDeleteWireRequest struct {
	Key      string `json:"key"`
	Expected []byte `json:"expected,omitempty"`
}

// compareAndDeleteWireResponse matches the JSON payload returned by
// POST /kv/compare-and-delete.
type compareAndDeleteWireResponse struct {
	Deleted bool `json:"deleted"`
}

// compareAndSwapWireRequest matches the JSON payload for
// POST /kv/compare-and-swap.
type compareAndSwapWireRequest struct {
	Key      string `json:"key"`
	Expected []byte `json:"expected,omitempty"`
	Next     []byte `json:"next,omitempty"`
}

// compareAndSwapWireResponse matches the JSON payload returned by
// POST /kv/compare-and-swap.
type compareAndSwapWireResponse struct {
	Swapped bool `json:"swapped"`
}

// ---------------------------------------------------------------------------
// GuardedKVClient implementation
// ---------------------------------------------------------------------------

// PutIfAbsent atomically stores value under key only when key is absent,
// returning whether it stored. Maps to POST /kv/put-if-absent.
//
// A declined store is a 200 carrying {"stored": false}, not an error: losing
// the race is the normal outcome of a claim, and reporting it as an error
// would make a caller retry the one thing that can never succeed.
//
// No WriteOption: a conditional write must be linearizable to mean anything,
// so it is always synchronous — a relaxed-durability guarded write would be a
// guarantee it cannot keep.
func (t *Transport) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if t.closed.Load() {
		return false, &transport.ClosedError{}
	}

	var resp putIfAbsentWireResponse
	if err := t.guardedPost(ctx, "PutIfAbsent", "/kv/put-if-absent",
		putIfAbsentWireRequest{Key: key, Value: value}, &resp); err != nil {
		return false, err
	}
	return resp.Stored, nil
}

// CompareAndDelete atomically removes key only when its stored value still
// equals expected, returning whether it deleted. Maps to
// POST /kv/compare-and-delete. It is the release half of PutIfAbsent: an
// unguarded DELETE /kv/{key} here would let a caller that no longer holds a
// claim un-decide what the guarded create decided.
func (t *Transport) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if t.closed.Load() {
		return false, &transport.ClosedError{}
	}

	var resp compareAndDeleteWireResponse
	if err := t.guardedPost(ctx, "CompareAndDelete", "/kv/compare-and-delete",
		compareAndDeleteWireRequest{Key: key, Expected: expected}, &resp); err != nil {
		return false, err
	}
	return resp.Deleted, nil
}

// CompareAndSwap atomically replaces key's value with next only when its
// stored value still equals expected, returning whether it replaced. Maps to
// POST /kv/compare-and-swap. An absent key is left absent — a swap is not a
// create.
//
// The comparison happens in the state machine, never on this side of the wire:
// emulating it with a GET and a PUT would reopen the window the verb exists to
// close, and would do so across a network round trip.
func (t *Transport) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if t.closed.Load() {
		return false, &transport.ClosedError{}
	}

	var resp compareAndSwapWireResponse
	if err := t.guardedPost(ctx, "CompareAndSwap", "/kv/compare-and-swap",
		compareAndSwapWireRequest{Key: key, Expected: expected, Next: next}, &resp); err != nil {
		return false, err
	}
	return resp.Swapped, nil
}

// guardedPost POSTs a guarded verb's request document and decodes its
// response, classifying a 501 into the TYPED capability refusal.
//
// It exists instead of postJSONDecode for that classification alone. The
// server answers 501 Not Implemented when the state machine behind the request
// has no handler for the guarded opcode or was built without the subsystem
// (pkg/api/rest.mapHTTPError maps service.CapabilityUnsupportedError there),
// and the shared decodeErrorResponse flattens every status into a
// RequestError wrapping the server's message STRING. A consumer that wants to
// degrade — fall back from CompareAndSwap to a transaction, or give up on a
// lock rather than retry forever — would then have to match message text, and
// would retry a call that can never succeed. The embedded, gRPC and unix
// transports all hand it transport.CapabilityUnsupportedError for the same
// server-side condition (go-qrdb#141); REST must not be the one that does not.
//
// Every other REST method types its 501 the same way, through
// decodeErrorResponse (go-qrdb#557); what this path adds is the 404/405
// classification, the version-skew half of the same contract — see the
// switch below.
func (t *Transport) guardedPost(ctx context.Context, method, path string, payload, dest any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return &transport.RequestError{Method: method, Err: &jsonEncodeError{Err: err}}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return &transport.RequestError{Method: method, Err: err}
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return &transport.RequestError{Method: method, Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	// 501 is a NEW server refusing the verb; 404/405 is an OLD server that
	// never carried it. Both mean "this node cannot do that", and both must
	// reach the caller as the same typed refusal.
	//
	// The 404/405 half is what makes the capability contract hold across a
	// version skew. An older REST server has no /kv/put-if-absent route at
	// all, so it answers 404 (or 405 where a sibling method matched the path)
	// — and without this a consumer that had just type-asserted
	// GuardedKVClient would get a generic RequestError and could not tell "the
	// remote does not have these verbs" from "the request failed", so it would
	// retry forever instead of degrading. Every other transport already gets
	// this right: an older gRPC or unix server answers codes.Unimplemented and
	// an older QUIC server's dispatch loop answers wire.StatusUnimplemented.
	//
	// The classification is unambiguous on THESE paths specifically: the key
	// travels in the body, never in the path, so a 404 here can only mean the
	// endpoint is absent — an absent KEY is a 200 carrying false.
	switch resp.StatusCode {
	case http.StatusNotImplemented:
		return capabilityUnsupportedFrom(resp)
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return &transport.CapabilityUnsupportedError{
			Message: "rest: " + path + " is not configured on this server; it does not carry the guarded write verbs",
		}
	}
	if resp.StatusCode >= 400 {
		return t.decodeErrorResponse(method, resp)
	}

	if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
		return &transport.RequestError{Method: method, Err: &jsonDecodeError{Err: err}}
	}
	return nil
}

// capabilityUnsupportedFrom builds the typed capability refusal from a 501
// response, preferring the server's JSON error message over the raw body so
// the refusal still names WHAT is missing — a typed refusal carrying no
// account of which opcode or subsystem is absent is not much better than an
// untyped one for whoever has to act on it.
func capabilityUnsupportedFrom(resp *http.Response) error {
	var errResp transport.ErrorResponse
	if json.NewDecoder(resp.Body).Decode(&errResp) == nil && errResp.Error != "" {
		return &transport.CapabilityUnsupportedError{Message: errResp.Error}
	}
	return &transport.CapabilityUnsupportedError{Message: resp.Status}
}
