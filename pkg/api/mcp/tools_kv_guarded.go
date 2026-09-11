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

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// The guarded KV write verbs on the MCP tool surface.
//
// Each of the three handlers below makes exactly ONE call, to the matching
// atomic method on service.KVServicer. There is no Get before it and no
// read-then-write fallback after it, which is the only thing that makes the
// verb worth carrying: the condition is evaluated inside the state machine, so
// two proposers cannot both win and replicas cannot diverge on the decision.
// A handler that reconstructed one of these from qrdb_get plus qrdb_put would
// return the same booleans in sequence and arbitrate nothing.
//
// The tools mirror the REST endpoints one for one — /kv/put-if-absent,
// /kv/compare-and-delete, /kv/compare-and-swap (pkg/api/rest's
// handlers_guarded.go) — down to the permission each requires and the field
// each answer carries.

// guardedKVTools returns the MCP tool registrations for the guarded
// (conditional) KV write verbs. They are registered alongside the plain KV
// tools, unconditionally: whether the state machine behind the service can
// carry a guarded opcode is the service's answer to give, not a registration
// decision, and a tool that vanished on some deployments would leave a client
// unable to tell an unwired subsystem from an old server.
func guardedKVTools(kvSvc service.KVServicer) []serverTool {
	return []serverTool{
		{
			Tool:    toolPutIfAbsent(),
			Perm:    permission.KVPut,
			Handler: handlePutIfAbsent(kvSvc),
		},
		{
			Tool:    toolCompareAndDelete(),
			Perm:    permission.KVDelete,
			Handler: handleCompareAndDelete(kvSvc),
		},
		{
			Tool:    toolCompareAndSwap(),
			Perm:    permission.KVPut,
			Handler: handleCompareAndSwap(kvSvc),
		},
	}
}

// toolPutIfAbsent defines the qrdb_put_if_absent tool schema.
func toolPutIfAbsent() mcp.Tool {
	return mcp.NewTool("qrdb_put_if_absent",
		mcp.WithDescription("Atomically store a key-value pair only when the key is absent. "+
			"The condition is evaluated inside the state machine, so exactly one of several "+
			"concurrent callers can win. Returns a JSON object {\"stored\": true|false}; "+
			"stored=false means another writer already holds the key, which is a normal "+
			"outcome and not an error. Always synchronous — it takes no write mode."),
		mcp.WithString("key",
			mcp.Required(),
			mcp.Description("The key to claim."),
		),
		mcp.WithString("value",
			mcp.Description("The value to store, base64-encoded (RFC 4648). Absent or empty stores an empty value."),
		),
	)
}

// handlePutIfAbsent returns a tool handler that delegates to
// kvSvc.PutIfAbsent — one call, no preceding read.
func handlePutIfAbsent(kvSvc service.KVServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		kvSvc, key, value, bad := guardedArgs(ctx, kvSvc, req, "value")
		if bad != nil {
			return bad, nil
		}

		stored, err := kvSvc.PutIfAbsent(ctx, key, value)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_put_if_absent", Err: err}
		}
		return guardedOutcome("qrdb_put_if_absent", "stored", stored)
	}
}

// toolCompareAndDelete defines the qrdb_compare_and_delete tool schema.
func toolCompareAndDelete() mcp.Tool {
	return mcp.NewTool("qrdb_compare_and_delete",
		mcp.WithDescription("Atomically delete a key only when its stored value still equals "+
			"the expected value. It is the release half of qrdb_put_if_absent: an unguarded "+
			"delete would let a caller that no longer holds a claim un-decide what the guarded "+
			"create decided. Returns a JSON object {\"deleted\": true|false}; deleted=false "+
			"means the value had changed or the key was gone, which is a normal outcome and "+
			"not an error."),
		mcp.WithString("key",
			mcp.Required(),
			mcp.Description("The key to release."),
		),
		mcp.WithString("expected",
			mcp.Description("The value the key must still hold, base64-encoded (RFC 4648). Absent or empty compares against an empty value."),
		),
	)
}

// handleCompareAndDelete returns a tool handler that delegates to
// kvSvc.CompareAndDelete — one call, no preceding read.
func handleCompareAndDelete(kvSvc service.KVServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		kvSvc, key, expected, bad := guardedArgs(ctx, kvSvc, req, "expected")
		if bad != nil {
			return bad, nil
		}

		deleted, err := kvSvc.CompareAndDelete(ctx, key, expected)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_compare_and_delete", Err: err}
		}
		return guardedOutcome("qrdb_compare_and_delete", "deleted", deleted)
	}
}

// toolCompareAndSwap defines the qrdb_compare_and_swap tool schema.
func toolCompareAndSwap() mcp.Tool {
	return mcp.NewTool("qrdb_compare_and_swap",
		mcp.WithDescription("Atomically replace a key's value only when its stored value still "+
			"equals the expected value. An absent key is left absent — a swap is not a create. "+
			"Returns a JSON object {\"swapped\": true|false}; swapped=false means the value had "+
			"changed under the caller, which is a normal outcome and not an error."),
		mcp.WithString("key",
			mcp.Required(),
			mcp.Description("The key to edit."),
		),
		mcp.WithString("expected",
			mcp.Description("The value the key must still hold, base64-encoded (RFC 4648). Absent or empty compares against an empty value."),
		),
		mcp.WithString("next",
			mcp.Description("The value to store when the comparison holds, base64-encoded (RFC 4648). Absent or empty stores an empty value."),
		),
	)
}

// handleCompareAndSwap returns a tool handler that delegates to
// kvSvc.CompareAndSwap — one call, no preceding read.
func handleCompareAndSwap(kvSvc service.KVServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		kvSvc, key, expected, bad := guardedArgs(ctx, kvSvc, req, "expected")
		if bad != nil {
			return bad, nil
		}
		next, decodeErr := guardedBytesArg(req, "next")
		if decodeErr != nil {
			return mcp.NewToolResultError(decodeErr.Error()), nil
		}

		swapped, err := kvSvc.CompareAndSwap(ctx, key, expected, next)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_compare_and_swap", Err: err}
		}
		return guardedOutcome("qrdb_compare_and_swap", "swapped", swapped)
	}
}

// guardedArgs resolves the scoped KV service and reads the two arguments every
// guarded verb takes: the required key, and one byte-valued guard argument
// named by valueField ("value" for the create, "expected" for the compares).
//
// It returns a non-nil result ONLY when the request cannot be served, and that
// result is always an argument-level tool error — never a guarded answer. The
// distinction is the whole contract of these verbs: "the guard did not hold"
// is a successful call reporting false, so a malformed request must never be
// able to look like one.
func guardedArgs(ctx context.Context, fallback service.KVServicer, req mcp.CallToolRequest, valueField string) (service.KVServicer, string, []byte, *mcp.CallToolResult) {
	kvSvc, rerr := resolveKV(ctx, fallback)
	if rerr != nil {
		return nil, "", nil, mcp.NewToolResultError(rerr.Error())
	}
	key := mcp.ParseString(req, "key", "")
	if key == "" {
		return nil, "", nil, mcp.NewToolResultError("key is required")
	}
	value, decodeErr := guardedBytesArg(req, valueField)
	if decodeErr != nil {
		return nil, "", nil, mcp.NewToolResultError(decodeErr.Error())
	}
	return kvSvc, key, value, nil
}

// guardedBytesArg decodes an optional base64 (RFC 4648) argument into the
// bytes a guard is decided against.
//
// An absent argument is the EMPTY value rather than an error, unlike
// qrdb_put's required value. A guard compares bytes, and comparing against an
// empty value is a meaningful thing to ask for — rejecting it would make the
// empty value the one value a claim could never be released from. Values
// travel base64-encoded for the same reason they do on REST: a value that did
// not survive a UTF-8 round trip would make CompareAndDelete refuse a release
// that should have succeeded, which is a lock that can be taken and never let
// go.
func guardedBytesArg(req mcp.CallToolRequest, field string) ([]byte, error) {
	arg := mcp.ParseString(req, field, "")
	if arg == "" {
		return nil, nil
	}
	value, err := base64.StdEncoding.DecodeString(arg)
	if err != nil {
		return nil, &ValueDecodeError{Field: field, Err: err}
	}
	return value, nil
}

// guardedOutcome renders a guarded verb's decision as the single-field JSON
// document the SDK transport decodes.
//
// A refused guard is a SUCCESSFUL tool result carrying false, never a tool
// error — the same contract REST states as a 200 carrying {"stored": false}
// and gRPC as an OK response carrying stored=false. Losing the race is the
// normal outcome of a claim, and a caller told the call "failed" would retry
// the one thing that can never succeed.
func guardedOutcome(tool, field string, decided bool) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(map[string]bool{field: decided})
	if err != nil {
		return nil, &ToolError{Tool: tool, Err: err}
	}
	return mcp.NewToolResultText(string(data)), nil
}
