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

// kvTools returns the MCP tool registrations for KV data-plane operations —
// the plain verbs here, plus the guarded (conditional) write verbs from
// guardedKVTools appended to them. One registration site for the whole KV
// surface: a guarded tool must not be reachable on a server whose plain KV
// tools are absent, since the guard is decided against the same keyspace.
func kvTools(kvSvc service.KVServicer) []serverTool {
	return append([]serverTool{
		{
			Tool:    toolPut(),
			Perm:    permission.KVPut,
			Handler: handlePut(kvSvc),
		},
		{
			Tool:    toolGet(),
			Perm:    permission.KVGet,
			Handler: handleGet(kvSvc),
		},
		{
			Tool:    toolDelete(),
			Perm:    permission.KVDelete,
			Handler: handleDelete(kvSvc),
		},
		{
			Tool:    toolList(),
			Perm:    permission.KVList,
			Handler: handleList(kvSvc),
		},
		{
			Tool:    toolScan(),
			Perm:    permission.KVRange,
			Handler: handleScan(kvSvc),
		},
	}, guardedKVTools(kvSvc)...)
}

// toolPut defines the qrdb_put tool schema.
func toolPut() mcp.Tool {
	return mcp.NewTool("qrdb_put",
		mcp.WithDescription("Store a key-value pair through Raft consensus."),
		mcp.WithString("key",
			mcp.Required(),
			mcp.Description("The key to store."),
		),
		mcp.WithString("value",
			mcp.Required(),
			mcp.Description("The value to store, base64-encoded (RFC 4648). Encoding as base64 preserves arbitrary binary content byte-for-byte; a UTF-8 text value must also be base64-encoded before calling this tool."),
		),
		mcp.WithString("write_mode",
			mcp.Description("Write durability mode: sync, wal_durable, or batched. Absent or empty uses sync."),
		),
	)
}

// handlePut returns a tool handler that delegates to kvSvc.Put or
// kvSvc.PutWithMode when a write_mode arg is present.
func handlePut(kvSvc service.KVServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		kvSvc, rerr := resolveKV(ctx, kvSvc)
		if rerr != nil {
			return nil, rerr
		}
		key := mcp.ParseString(req, "key", "")
		if key == "" {
			return mcp.NewToolResultError("key is required"), nil
		}
		valueArg := mcp.ParseString(req, "value", "")
		if valueArg == "" {
			return mcp.NewToolResultError("value is required"), nil
		}
		value, decodeErr := base64.StdEncoding.DecodeString(valueArg)
		if decodeErr != nil {
			return mcp.NewToolResultError((&ValueDecodeError{Field: "value", Err: decodeErr}).Error()), nil
		}

		mode, modeSet, modeErr := writeModeFromArg(req)
		if modeErr != nil {
			return mcp.NewToolResultError("invalid write_mode: " + modeErr.Error()), nil
		}

		var err error
		if modeSet {
			err = kvSvc.PutWithMode(ctx, key, value, mode)
		} else {
			err = kvSvc.Put(ctx, key, value)
		}
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_put", Err: err}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// toolGet defines the qrdb_get tool schema.
func toolGet() mcp.Tool {
	return mcp.NewTool("qrdb_get",
		mcp.WithDescription("Retrieve a value by key with a linearizable read. The returned value is base64-encoded (RFC 4648) to preserve arbitrary binary content byte-for-byte."),
		mcp.WithString("key",
			mcp.Required(),
			mcp.Description("The key to retrieve."),
		),
		mcp.WithString("consistency",
			mcp.Description("Read consistency level: strong, linearizable, or eventual. Absent or empty uses linearizable."),
		),
	)
}

// handleGet returns a tool handler that delegates to kvSvc.Get or
// kvSvc.GetWithConsistency when a consistency arg is present.
func handleGet(kvSvc service.KVServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		kvSvc, rerr := resolveKV(ctx, kvSvc)
		if rerr != nil {
			return nil, rerr
		}
		key := mcp.ParseString(req, "key", "")
		if key == "" {
			return mcp.NewToolResultError("key is required"), nil
		}

		opts, optsSet, optsErr := readOptsFromArg(req)
		if optsErr != nil {
			return mcp.NewToolResultError("invalid consistency: " + optsErr.Error()), nil
		}

		var (
			data []byte
			err  error
		)
		if optsSet {
			data, err = kvSvc.GetWithConsistency(ctx, key, opts)
		} else {
			data, err = kvSvc.Get(ctx, key)
		}
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_get", Err: err}
		}

		return mcp.NewToolResultText(base64.StdEncoding.EncodeToString(data)), nil
	}
}

// toolDelete defines the qrdb_delete tool schema.
func toolDelete() mcp.Tool {
	return mcp.NewTool("qrdb_delete",
		mcp.WithDescription("Delete a key through Raft consensus."),
		mcp.WithString("key",
			mcp.Required(),
			mcp.Description("The key to delete."),
		),
		mcp.WithString("write_mode",
			mcp.Description("Write durability mode: sync, wal_durable, or batched. Absent or empty uses sync."),
		),
	)
}

// handleDelete returns a tool handler that delegates to kvSvc.Delete or
// kvSvc.DeleteWithMode when a write_mode arg is present.
func handleDelete(kvSvc service.KVServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		kvSvc, rerr := resolveKV(ctx, kvSvc)
		if rerr != nil {
			return nil, rerr
		}
		key := mcp.ParseString(req, "key", "")
		if key == "" {
			return mcp.NewToolResultError("key is required"), nil
		}

		mode, modeSet, modeErr := writeModeFromArg(req)
		if modeErr != nil {
			return mcp.NewToolResultError("invalid write_mode: " + modeErr.Error()), nil
		}

		var err error
		if modeSet {
			err = kvSvc.DeleteWithMode(ctx, key, mode)
		} else {
			err = kvSvc.Delete(ctx, key)
		}
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_delete", Err: err}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// toolList defines the qrdb_list tool schema.
func toolList() mcp.Tool {
	return mcp.NewTool("qrdb_list",
		mcp.WithDescription("List keys matching a prefix with a linearizable read. An empty prefix returns all keys."),
		mcp.WithString("prefix",
			mcp.Description("Optional key prefix filter."),
		),
		mcp.WithString("consistency",
			mcp.Description("Read consistency level: strong, linearizable, or eventual. Absent or empty uses linearizable."),
		),
	)
}

// handleList returns a tool handler that delegates to kvSvc.List or
// kvSvc.ListWithConsistency when a consistency arg is present.
func handleList(kvSvc service.KVServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		kvSvc, rerr := resolveKV(ctx, kvSvc)
		if rerr != nil {
			return nil, rerr
		}
		prefix := mcp.ParseString(req, "prefix", "")

		opts, optsSet, optsErr := readOptsFromArg(req)
		if optsErr != nil {
			return mcp.NewToolResultError("invalid consistency: " + optsErr.Error()), nil
		}

		var (
			keys []string
			err  error
		)
		if optsSet {
			keys, err = kvSvc.ListWithConsistency(ctx, prefix, opts)
		} else {
			keys, err = kvSvc.List(ctx, prefix)
		}
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_list", Err: err}
		}

		data, jsonErr := json.Marshal(keys)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_list", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolScan defines the qrdb_scan tool schema.
func toolScan() mcp.Tool {
	return mcp.NewTool("qrdb_scan",
		mcp.WithDescription("Scan key-value pairs matching a prefix. Returns a JSON map of key to base64-encoded value (RFC 4648)."),
		mcp.WithString("prefix",
			mcp.Description("Optional key prefix filter."),
		),
		mcp.WithString("consistency",
			mcp.Description("Read consistency level: strong, linearizable, or eventual. Absent or empty uses linearizable."),
		),
	)
}

// handleScan returns a tool handler that delegates to kvSvc.Scan or
// kvSvc.ScanWithConsistency when a consistency arg is present.
func handleScan(kvSvc service.KVServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		kvSvc, rerr := resolveKV(ctx, kvSvc)
		if rerr != nil {
			return nil, rerr
		}
		prefix := mcp.ParseString(req, "prefix", "")

		opts, optsSet, optsErr := readOptsFromArg(req)
		if optsErr != nil {
			return mcp.NewToolResultError("invalid consistency: " + optsErr.Error()), nil
		}

		var (
			pairs map[string][]byte
			err   error
		)
		if optsSet {
			pairs, err = kvSvc.ScanWithConsistency(ctx, prefix, opts)
		} else {
			pairs, err = kvSvc.Scan(ctx, prefix)
		}
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_scan", Err: err}
		}

		// pairs is map[string][]byte; encoding/json base64-encodes []byte map
		// values automatically, preserving arbitrary binary content
		// byte-for-byte (matching the REST /kv/scan wire representation).
		data, jsonErr := json.Marshal(pairs)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_scan", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}
