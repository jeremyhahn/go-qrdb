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

// mvccTools returns the MCP tool registrations for MVCC key-value operations.
func mvccTools(mvccSvc service.MVCCKVServicer) []serverTool {
	return []serverTool{
		{
			Tool:    toolMVCCPutWithLease(),
			Perm:    permission.MVCCPutWithLease,
			Handler: handleMVCCPutWithLease(mvccSvc),
		},
		{
			Tool:    toolMVCCGetRevision(),
			Perm:    permission.MVCCGetRevision,
			Handler: handleMVCCGetRevision(mvccSvc),
		},
		{
			Tool:    toolMVCCGetAtRevision(),
			Perm:    permission.MVCCGetAtRevision,
			Handler: handleMVCCGetAtRevision(mvccSvc),
		},
		{
			Tool:    toolMVCCRange(),
			Perm:    permission.MVCCRange,
			Handler: handleMVCCRange(mvccSvc),
		},
		{
			Tool:    toolMVCCCompact(),
			Perm:    permission.MVCCCompact,
			Handler: handleMVCCCompact(mvccSvc),
		},
		{
			Tool:    toolMVCCCurrentRevision(),
			Perm:    permission.MVCCCurrentRevision,
			Handler: handleMVCCCurrentRevision(mvccSvc),
		},
		{
			Tool:    toolMVCCRestoreToRevision(),
			Perm:    permission.MVCCRestore,
			Handler: handleMVCCRestoreToRevision(mvccSvc),
		},
	}
}

// toolMVCCPutWithLease defines the qrdb_mvcc_put_with_lease tool schema.
func toolMVCCPutWithLease() mcp.Tool {
	return mcp.NewTool("qrdb_mvcc_put_with_lease",
		mcp.WithDescription("Store a key-value pair with an attached lease through MVCC-aware Raft consensus. When the lease expires, the key is automatically deleted. The returned key-value's \"key\" and \"value\" fields are base64-encoded (RFC 4648)."),
		mcp.WithString("key",
			mcp.Required(),
			mcp.Description("The key to store."),
		),
		mcp.WithString("value",
			mcp.Required(),
			mcp.Description("The value to store, base64-encoded (RFC 4648). Encoding as base64 preserves arbitrary binary content byte-for-byte; a UTF-8 text value must also be base64-encoded before calling this tool."),
		),
		mcp.WithNumber("lease_id",
			mcp.Required(),
			mcp.Description("The lease ID to attach to the key."),
		),
		mcp.WithString("write_mode",
			mcp.Description("Write durability mode: sync, wal_durable, or batched. Absent or empty uses sync."),
		),
	)
}

// handleMVCCPutWithLease returns a tool handler that delegates to
// mvccSvc.PutWithLease or mvccSvc.PutWithLeaseAndMode when a write_mode
// arg is present.
func handleMVCCPutWithLease(mvccSvc service.MVCCKVServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		mvccSvc, rerr := resolveMVCC(ctx, mvccSvc)
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
		leaseID := mcp.ParseInt64(req, "lease_id", 0)

		mode, modeSet, modeErr := writeModeFromArg(req)
		if modeErr != nil {
			return mcp.NewToolResultError("invalid write_mode: " + modeErr.Error()), nil
		}

		var (
			kvResult interface{}
			err      error
		)
		if modeSet {
			kvResult, err = mvccSvc.PutWithLeaseAndMode(ctx, key, value, leaseID, mode)
		} else {
			kvResult, err = mvccSvc.PutWithLease(ctx, key, value, leaseID)
		}
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_mvcc_put_with_lease", Err: err}
		}

		data, jsonErr := json.Marshal(kvResult)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_mvcc_put_with_lease", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolMVCCGetRevision defines the qrdb_mvcc_get_revision tool schema.
func toolMVCCGetRevision() mcp.Tool {
	return mcp.NewTool("qrdb_mvcc_get_revision",
		mcp.WithDescription("Retrieve a key-value pair with full MVCC revision metadata at the latest revision. The returned \"key\" and \"value\" fields are base64-encoded (RFC 4648)."),
		mcp.WithString("key",
			mcp.Required(),
			mcp.Description("The key to retrieve."),
		),
		mcp.WithString("consistency",
			mcp.Description("Read consistency level: strong, linearizable, or eventual. Absent or empty uses linearizable."),
		),
	)
}

// handleMVCCGetRevision returns a tool handler that delegates to
// mvccSvc.GetRevision or mvccSvc.GetRevisionWithConsistency when a
// consistency arg is present.
func handleMVCCGetRevision(mvccSvc service.MVCCKVServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		mvccSvc, rerr := resolveMVCC(ctx, mvccSvc)
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
			kv  interface{}
			err error
		)
		if optsSet {
			kv, err = mvccSvc.GetRevisionWithConsistency(ctx, key, opts)
		} else {
			kv, err = mvccSvc.GetRevision(ctx, key)
		}
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_mvcc_get_revision", Err: err}
		}

		data, jsonErr := json.Marshal(kv)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_mvcc_get_revision", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolMVCCGetAtRevision defines the qrdb_mvcc_get_at_revision tool schema.
func toolMVCCGetAtRevision() mcp.Tool {
	return mcp.NewTool("qrdb_mvcc_get_at_revision",
		mcp.WithDescription("Retrieve a key-value pair as it existed at a specific historical MVCC revision. The returned \"key\" and \"value\" fields are base64-encoded (RFC 4648)."),
		mcp.WithString("key",
			mcp.Required(),
			mcp.Description("The key to retrieve."),
		),
		mcp.WithNumber("revision",
			mcp.Required(),
			mcp.Description("The historical MVCC revision to read at."),
		),
		mcp.WithString("consistency",
			mcp.Description("Read consistency level: strong, linearizable, or eventual. Absent or empty uses linearizable."),
		),
	)
}

// handleMVCCGetAtRevision returns a tool handler that delegates to
// mvccSvc.GetAtRevision or mvccSvc.GetAtRevisionWithConsistency when a
// consistency arg is present.
func handleMVCCGetAtRevision(mvccSvc service.MVCCKVServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		mvccSvc, rerr := resolveMVCC(ctx, mvccSvc)
		if rerr != nil {
			return nil, rerr
		}
		key := mcp.ParseString(req, "key", "")
		if key == "" {
			return mcp.NewToolResultError("key is required"), nil
		}
		revision := mcp.ParseInt64(req, "revision", 0)
		if revision == 0 {
			return mcp.NewToolResultError("revision is required"), nil
		}

		opts, optsSet, optsErr := readOptsFromArg(req)
		if optsErr != nil {
			return mcp.NewToolResultError("invalid consistency: " + optsErr.Error()), nil
		}

		var (
			kv  interface{}
			err error
		)
		if optsSet {
			kv, err = mvccSvc.GetAtRevisionWithConsistency(ctx, key, revision, opts)
		} else {
			kv, err = mvccSvc.GetAtRevision(ctx, key, revision)
		}
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_mvcc_get_at_revision", Err: err}
		}

		data, jsonErr := json.Marshal(kv)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_mvcc_get_at_revision", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolMVCCRange defines the qrdb_mvcc_range tool schema.
func toolMVCCRange() mcp.Tool {
	return mcp.NewTool("qrdb_mvcc_range",
		mcp.WithDescription("Retrieve key-value pairs in the half-open interval [start, end) with MVCC revision metadata. Each returned key_values entry's \"key\" and \"value\" fields are base64-encoded (RFC 4648)."),
		mcp.WithString("start",
			mcp.Required(),
			mcp.Description("The start key of the range (inclusive)."),
		),
		mcp.WithString("end",
			mcp.Required(),
			mcp.Description("The end key of the range (exclusive)."),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of key-values to return. 0 for unlimited."),
		),
		mcp.WithNumber("revision",
			mcp.Description("Read key-values at this historical revision. 0 for latest."),
		),
		mcp.WithBoolean("count_only",
			mcp.Description("Return only the count of matching keys without values."),
		),
		mcp.WithBoolean("keys_only",
			mcp.Description("Return keys without their values."),
		),
		mcp.WithString("sort_order",
			mcp.Description("Sort order: none, ascend, or descend. Defaults to none (scan order)."),
		),
		mcp.WithString("sort_target",
			mcp.Description("Sort target: key, version, create_revision, mod_revision, or value. Defaults to key."),
		),
		mcp.WithString("consistency",
			mcp.Description("Read consistency level: strong, linearizable, or eventual. Absent or empty uses linearizable."),
		),
	)
}

// mvccSortOrderMap maps the qrdb_mvcc_range tool's sort_order argument to
// the service SortOrder constant it selects.
var mvccSortOrderMap = map[string]service.SortOrder{
	"":        service.SortNone,
	"none":    service.SortNone,
	"ascend":  service.SortAscend,
	"descend": service.SortDescend,
}

// mvccSortTargetMap maps the qrdb_mvcc_range tool's sort_target argument to
// the service SortTarget constant it selects.
var mvccSortTargetMap = map[string]service.SortTarget{
	"":                service.SortByKey,
	"key":             service.SortByKey,
	"version":         service.SortByVersion,
	"create_revision": service.SortByCreateRevision,
	"mod_revision":    service.SortByModRevision,
	"value":           service.SortByValue,
}

// unsupportedSortOrderError is returned when a qrdb_mvcc_range tool call
// carries a sort_order argument outside mvccSortOrderMap's known values.
type unsupportedSortOrderError struct {
	Value string
}

// Error returns a human-readable description of the unrecognized value.
func (e *unsupportedSortOrderError) Error() string {
	return "unsupported sort_order: " + e.Value + " (want one of: none, ascend, descend)"
}

// unsupportedSortTargetError is returned when a qrdb_mvcc_range tool call
// carries a sort_target argument outside mvccSortTargetMap's known values.
type unsupportedSortTargetError struct {
	Value string
}

// Error returns a human-readable description of the unrecognized value.
func (e *unsupportedSortTargetError) Error() string {
	return "unsupported sort_target: " + e.Value + " (want one of: key, version, create_revision, mod_revision, value)"
}

// mvccRangeResponse is the JSON envelope for MVCC range results.
type mvccRangeResponse struct {
	KeyValues json.RawMessage `json:"key_values"`
	Count     int64           `json:"count"`
	Revision  int64           `json:"revision"`
}

// handleMVCCRange returns a tool handler that delegates to mvccSvc.Range or
// mvccSvc.RangeWithConsistency when a consistency arg is present.
func handleMVCCRange(mvccSvc service.MVCCKVServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		mvccSvc, rerr := resolveMVCC(ctx, mvccSvc)
		if rerr != nil {
			return nil, rerr
		}
		start := mcp.ParseString(req, "start", "")
		if start == "" {
			return mcp.NewToolResultError("start is required"), nil
		}
		end := mcp.ParseString(req, "end", "")
		if end == "" {
			return mcp.NewToolResultError("end is required"), nil
		}

		var opts []service.RangeOption
		if limit := mcp.ParseInt64(req, "limit", 0); limit > 0 {
			opts = append(opts, service.WithLimit(limit))
		}
		if revision := mcp.ParseInt64(req, "revision", 0); revision > 0 {
			opts = append(opts, service.WithRevision(revision))
		}
		if mcp.ParseBoolean(req, "count_only", false) {
			opts = append(opts, service.WithCountOnly())
		}
		if mcp.ParseBoolean(req, "keys_only", false) {
			opts = append(opts, service.WithKeysOnly())
		}

		sortOrderArg := mcp.ParseString(req, "sort_order", "")
		sortOrder, ok := mvccSortOrderMap[sortOrderArg]
		if !ok {
			return mcp.NewToolResultError((&unsupportedSortOrderError{Value: sortOrderArg}).Error()), nil
		}
		sortTargetArg := mcp.ParseString(req, "sort_target", "")
		sortTarget, ok := mvccSortTargetMap[sortTargetArg]
		if !ok {
			return mcp.NewToolResultError((&unsupportedSortTargetError{Value: sortTargetArg}).Error()), nil
		}
		if sortOrder != service.SortNone || sortTarget != service.SortByKey {
			opts = append(opts, service.WithSort(sortOrder, sortTarget))
		}

		ropts, roptsSet, roptsErr := readOptsFromArg(req)
		if roptsErr != nil {
			return mcp.NewToolResultError("invalid consistency: " + roptsErr.Error()), nil
		}

		var (
			kvs      interface{}
			count    int64
			revision int64
			err      error
		)
		if roptsSet {
			kvs, count, revision, err = mvccSvc.RangeWithConsistency(ctx, []byte(start), []byte(end), ropts, opts...)
		} else {
			kvs, count, revision, err = mvccSvc.Range(ctx, []byte(start), []byte(end), opts...)
		}
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_mvcc_range", Err: err}
		}

		kvsJSON, jsonErr := json.Marshal(kvs)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_mvcc_range", Err: jsonErr}
		}

		resp := mvccRangeResponse{
			KeyValues: kvsJSON,
			Count:     count,
			Revision:  revision,
		}

		data, jsonErr := json.Marshal(resp)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_mvcc_range", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolMVCCCompact defines the qrdb_mvcc_compact tool schema.
func toolMVCCCompact() mcp.Tool {
	return mcp.NewTool("qrdb_mvcc_compact",
		mcp.WithDescription("Discard all MVCC revisions older than the specified revision to reclaim storage."),
		mcp.WithNumber("revision",
			mcp.Required(),
			mcp.Description("Compact all revisions older than this value."),
		),
	)
}

// handleMVCCCompact returns a tool handler that delegates to
// mvccSvc.Compact.
func handleMVCCCompact(mvccSvc service.MVCCKVServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		mvccSvc, rerr := resolveMVCC(ctx, mvccSvc)
		if rerr != nil {
			return nil, rerr
		}
		revision := mcp.ParseInt64(req, "revision", 0)
		if revision == 0 {
			return mcp.NewToolResultError("revision is required"), nil
		}

		if err := mvccSvc.Compact(ctx, revision); err != nil {
			return nil, &ToolError{Tool: "qrdb_mvcc_compact", Err: err}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// toolMVCCCurrentRevision defines the qrdb_mvcc_current_revision tool schema.
func toolMVCCCurrentRevision() mcp.Tool {
	return mcp.NewTool("qrdb_mvcc_current_revision",
		mcp.WithDescription("Return the latest committed MVCC revision."),
	)
}

// handleMVCCCurrentRevision returns a tool handler that delegates to
// mvccSvc.CurrentRevision.
func handleMVCCCurrentRevision(mvccSvc service.MVCCKVServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		mvccSvc, rerr := resolveMVCC(ctx, mvccSvc)
		if rerr != nil {
			return nil, rerr
		}
		rev, err := mvccSvc.CurrentRevision(ctx)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_mvcc_current_revision", Err: err}
		}

		data, jsonErr := json.Marshal(map[string]int64{"revision": rev})
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_mvcc_current_revision", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// ---------------------------------------------------------------------------
// Restore to revision
// ---------------------------------------------------------------------------

// mvccRestoreKeyChangeResponse is the JSON representation of one restore
// plan entry. Key is []byte so it is base64-encoded (RFC 4648) on the wire,
// preserving an arbitrary byte-valued key exactly -- the restored value
// itself is never carried, only its length (ValueBytes).
type mvccRestoreKeyChangeResponse struct {
	Key          []byte                `json:"key"`
	Action       service.RestoreAction `json:"action"`
	FromRevision int64                 `json:"from_revision,omitempty"`
	ToRevision   int64                 `json:"to_revision,omitempty"`
	ValueBytes   int64                 `json:"value_bytes,omitempty"`
}

// mvccRestoreResultResponse is the JSON envelope returned by the
// qrdb_mvcc_restore_to_revision tool.
type mvccRestoreResultResponse struct {
	Revision   int64                          `json:"revision"`
	Changes    []mvccRestoreKeyChangeResponse `json:"changes"`
	Conflicts  []mvccRestoreKeyChangeResponse `json:"conflicts,omitempty"`
	Applied    int                            `json:"applied"`
	Skipped    int                            `json:"skipped"`
	Conflicted int                            `json:"conflicted"`
	DryRun     bool                           `json:"dry_run"`
}

// restoreChangesToToolResponse converts a slice of service RestoreKeyChange
// to the JSON response representation.
func restoreChangesToToolResponse(changes []service.RestoreKeyChange) []mvccRestoreKeyChangeResponse {
	out := make([]mvccRestoreKeyChangeResponse, len(changes))
	for i, c := range changes {
		out[i] = mvccRestoreKeyChangeResponse{
			Key:          []byte(c.Key),
			Action:       c.Action,
			FromRevision: c.FromRevision,
			ToRevision:   c.ToRevision,
			ValueBytes:   c.ValueBytes,
		}
	}
	return out
}

// restoreResultToToolResponse converts a service RestoreRevisionResult to
// the JSON response representation.
func restoreResultToToolResponse(result *service.RestoreRevisionResult) mvccRestoreResultResponse {
	return mvccRestoreResultResponse{
		Revision:   result.Revision,
		Changes:    restoreChangesToToolResponse(result.Changes),
		Conflicts:  restoreChangesToToolResponse(result.Conflicts),
		Applied:    result.Applied,
		Skipped:    result.Skipped,
		Conflicted: result.Conflicted,
		DryRun:     result.DryRun,
	}
}

// toolMVCCRestoreToRevision defines the qrdb_mvcc_restore_to_revision tool
// schema.
func toolMVCCRestoreToRevision() mcp.Tool {
	return mcp.NewTool("qrdb_mvcc_restore_to_revision",
		mcp.WithDescription("Return a bounded set of keys to the value they held at an earlier MVCC revision by proposing new writes -- a logical undo, never a Raft rewind. Exactly one of prefix or keys must be set. The \"prefix\" argument and each element of \"keys\" are base64-encoded (RFC 4648); each returned change's \"key\" field is also base64-encoded. This operation requires the shard to run raft.sm_type: mvcc."),
		mcp.WithNumber("revision",
			mcp.Required(),
			mcp.Description("The target MVCC revision to restore the scoped keys to."),
		),
		mcp.WithString("prefix",
			mcp.Description("Restore every key beginning with this prefix, base64-encoded (RFC 4648). Mutually exclusive with keys."),
		),
		mcp.WithArray("keys",
			mcp.Description("Restore this explicit list of keys, each base64-encoded (RFC 4648). Mutually exclusive with prefix."),
		),
		mcp.WithBoolean("dry_run",
			mcp.Description("Compute and return the plan without writing anything."),
		),
		mcp.WithBoolean("force",
			mcp.Description("Apply the plan unconditionally, bypassing the default compare-and-swap guard on each key. A key that changed since the plan was computed is reported in \"conflicts\" rather than applied when force is false (the default)."),
		),
	)
}

// handleMVCCRestoreToRevision returns a tool handler that delegates to
// mvccSvc.RestoreToRevision.
func handleMVCCRestoreToRevision(mvccSvc service.MVCCKVServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		mvccSvc, rerr := resolveMVCC(ctx, mvccSvc)
		if rerr != nil {
			return nil, rerr
		}
		revision := mcp.ParseInt64(req, "revision", 0)
		if revision == 0 {
			return mcp.NewToolResultError("revision is required"), nil
		}

		var prefix string
		if prefixArg := mcp.ParseString(req, "prefix", ""); prefixArg != "" {
			decoded, decodeErr := base64.StdEncoding.DecodeString(prefixArg)
			if decodeErr != nil {
				return mcp.NewToolResultError((&ValueDecodeError{Field: "prefix", Err: decodeErr}).Error()), nil
			}
			prefix = string(decoded)
		}

		var keys []string
		if rawKeys := mcp.ParseArgument(req, "keys", nil); rawKeys != nil {
			keysSlice, ok := rawKeys.([]interface{})
			if !ok {
				return mcp.NewToolResultError("keys must be an array of base64-encoded strings"), nil
			}
			keys = make([]string, 0, len(keysSlice))
			for _, v := range keysSlice {
				s, ok := v.(string)
				if !ok {
					return mcp.NewToolResultError("each element of keys must be a base64-encoded string"), nil
				}
				decoded, decodeErr := base64.StdEncoding.DecodeString(s)
				if decodeErr != nil {
					return mcp.NewToolResultError((&ValueDecodeError{Field: "keys", Err: decodeErr}).Error()), nil
				}
				keys = append(keys, string(decoded))
			}
		}

		result, err := mvccSvc.RestoreToRevision(ctx, &service.RestoreRevisionRequest{
			Revision: revision,
			Prefix:   prefix,
			Keys:     keys,
			DryRun:   mcp.ParseBoolean(req, "dry_run", false),
			Force:    mcp.ParseBoolean(req, "force", false),
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_mvcc_restore_to_revision", Err: err}
		}

		return marshalToolResult("qrdb_mvcc_restore_to_revision", restoreResultToToolResponse(result))
	}
}
