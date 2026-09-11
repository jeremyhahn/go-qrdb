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

package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The MCP transport carries the guarded write verbs (go-qrdb#127).
//
// This assertion is the whole capability contract: transport.GuardedKVClient
// is detected by type assertion, so a consumer's `kv.(GuardedKVClient)` starts
// succeeding the moment this line compiles. It must therefore be added ONLY
// alongside methods that carry the verb's atomicity, never its name alone — a
// transport that answers the assertion and then emulates a guarded write with
// a read followed by a write is strictly worse than one that declines the
// assertion, because the consumer will build a distributed lock on top of it.
//
// The three methods below satisfy that: each makes ONE tool call and returns
// the server's answer. No qrdb_get precedes any of them and no read-then-write
// fallback follows. Server-side the matching handler calls service.KVServicer's
// atomic method directly (pkg/api/mcp's handlePutIfAbsent and siblings), so the
// condition is evaluated inside the state machine exactly as it is for the
// embedded, gRPC, unix, QUIC and REST transports.
var _ transport.GuardedKVClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// Wire types for the guarded MCP tools
// ---------------------------------------------------------------------------
//
// Each guarded tool answers with a single-field JSON object naming its own
// decision, mirroring the REST response bodies field for field
// (pkg/api/rest's putIfAbsentResponse and siblings). Three types rather than
// one shared {"ok": bool}: the field name is what tells a reader of a captured
// tool result WHICH question was answered, and an agent driving these tools by
// name is exactly the consumer that benefits from not having to remember.

// putIfAbsentResult matches the JSON answer from qrdb_put_if_absent.
type putIfAbsentResult struct {
	Stored bool `json:"stored"`
}

// compareAndDeleteResult matches the JSON answer from qrdb_compare_and_delete.
type compareAndDeleteResult struct {
	Deleted bool `json:"deleted"`
}

// compareAndSwapResult matches the JSON answer from qrdb_compare_and_swap.
type compareAndSwapResult struct {
	Swapped bool `json:"swapped"`
}

// ---------------------------------------------------------------------------
// GuardedKVClient implementation
// ---------------------------------------------------------------------------

// PutIfAbsent atomically stores value under key only when key is absent,
// returning whether it stored. Calls the qrdb_put_if_absent tool.
//
// A declined store is a successful tool result carrying {"stored": false}, not
// an error: losing the race is the normal outcome of a claim, and reporting it
// as an error would make a caller retry the one thing that can never succeed.
//
// No WriteOption: a conditional write must be linearizable to mean anything,
// so it is always synchronous — a relaxed-durability guarded write would be a
// guarantee it cannot keep.
func (t *Transport) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	var res putIfAbsentResult
	if err := t.guardedTool(ctx, "PutIfAbsent", "qrdb_put_if_absent", map[string]any{
		"key":   key,
		"value": base64.StdEncoding.EncodeToString(value),
	}, &res); err != nil {
		return false, err
	}
	return res.Stored, nil
}

// CompareAndDelete atomically removes key only when its stored value still
// equals expected, returning whether it deleted. Calls the
// qrdb_compare_and_delete tool. It is the release half of PutIfAbsent: an
// unguarded qrdb_delete here would let a caller that no longer holds a claim
// un-decide what the guarded create decided.
func (t *Transport) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	var res compareAndDeleteResult
	if err := t.guardedTool(ctx, "CompareAndDelete", "qrdb_compare_and_delete", map[string]any{
		"key":      key,
		"expected": base64.StdEncoding.EncodeToString(expected),
	}, &res); err != nil {
		return false, err
	}
	return res.Deleted, nil
}

// CompareAndSwap atomically replaces key's value with next only when its
// stored value still equals expected, returning whether it replaced. Calls the
// qrdb_compare_and_swap tool. An absent key is left absent — a swap is not a
// create.
//
// The comparison happens in the state machine, never on this side of the wire:
// emulating it with a qrdb_get and a qrdb_put would reopen the window the verb
// exists to close, and would do so across a network round trip.
func (t *Transport) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	var res compareAndSwapResult
	if err := t.guardedTool(ctx, "CompareAndSwap", "qrdb_compare_and_swap", map[string]any{
		"key":      key,
		"expected": base64.StdEncoding.EncodeToString(expected),
		"next":     base64.StdEncoding.EncodeToString(next),
	}, &res); err != nil {
		return false, err
	}
	return res.Swapped, nil
}

// guardedTool calls one guarded tool and decodes its single-field JSON answer,
// classifying an unregistered tool into the TYPED capability refusal.
//
// It exists instead of a bare callTool for that classification alone, and the
// classification is what makes the capability contract hold across a VERSION
// SKEW. An older MCP server has no qrdb_put_if_absent tool at all, and without
// this a consumer that had just type-asserted GuardedKVClient would get a
// generic RequestError and could not tell "the remote does not have these
// verbs" from "the request failed", so it would retry forever instead of
// degrading. Every other transport already gets this right: an older gRPC or
// unix server answers codes.Unimplemented, an older QUIC server's dispatch
// loop answers wire.StatusUnimplemented, and an older REST server answers
// 404/405 on the absent route (go-qrdb#124/#125/#126/#141).
//
// The classification is by TYPE, not by message text, and it covers the two
// JSON-RPC codes an MCP server has for "I do not have that tool":
// INVALID_PARAMS when the server serves tools but not this one — the shape an
// older qrdb server takes — and METHOD_NOT_FOUND when it serves no tools at
// all. Within a tools/call each code has exactly one producer in mcp-go, so
// the match is unambiguous. Everything else a qrdb MCP server can refuse
// arrives by a different route: an authorization denial and a service-layer
// failure are both TOOL RESULTS (callTool reports them as toolResultError),
// never a JSON-RPC error.
//
// The narrowness is deliberate and is a KNOWN LIMIT, matching the REST
// transport's guardedPost: the rest of this transport still flattens a
// server-side capability refusal into an untyped error, because MCP carries no
// error-code mapping for the service error vocabulary (conventions/errors.md
// §5). Widening that belongs with the go-qrdb#169 error-typing sweep, which
// owns the surface and the tests that pin the flattened shape.
func (t *Transport) guardedTool(ctx context.Context, method, tool string, args map[string]any, dest any) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, tool, args)
	if err != nil {
		if errors.Is(err, mcp.ErrInvalidParams) || errors.Is(err, mcp.ErrMethodNotFound) {
			return &transport.CapabilityUnsupportedError{
				Message: "mcp: tool " + tool + " is not registered on this server; " +
					"it does not carry the guarded write verbs",
			}
		}
		return err
	}

	if jsonErr := json.Unmarshal([]byte(text), dest); jsonErr != nil {
		return &transport.RequestError{Method: method, Err: &jsonDecodeError{Err: jsonErr}}
	}
	return nil
}
