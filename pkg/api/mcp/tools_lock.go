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
	"encoding/json"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// lockTools returns the MCP tool registrations for distributed lock operations.
func lockTools(lockSvc service.LockServicer) []serverTool {
	return []serverTool{
		{
			Tool:    toolLockAcquire(),
			Perm:    permission.LocksAcquire,
			Handler: handleLockAcquire(lockSvc),
		},
		{
			Tool:    toolLockTryLock(),
			Perm:    permission.LocksAcquire,
			Handler: handleLockTryLock(lockSvc),
		},
		{
			Tool:    toolLockRelease(),
			Perm:    permission.LocksRelease,
			Handler: handleLockRelease(lockSvc),
		},
		{
			Tool:    toolLockExtend(),
			Perm:    permission.LocksExtend,
			Handler: handleLockExtend(lockSvc),
		},
		{
			Tool:    toolLockStatus(),
			Perm:    permission.LocksStatus,
			Handler: handleLockStatus(lockSvc),
		},
		{
			Tool:    toolLockList(),
			Perm:    permission.LocksList,
			Handler: handleLockList(lockSvc),
		},
	}
}

// toolLockAcquire defines the qrdb_lock_acquire tool schema.
func toolLockAcquire() mcp.Tool {
	return mcp.NewTool("qrdb_lock_acquire",
		mcp.WithDescription("Acquire a distributed lock, blocking until acquired or the context is cancelled. Returns the lock with a fencing token for safe distributed coordination."),
		mcp.WithString("key",
			mcp.Required(),
			mcp.Description("The resource key to lock."),
		),
		mcp.WithString("holder_id",
			mcp.Required(),
			mcp.Description("Unique identifier for the lock holder."),
		),
		mcp.WithNumber("ttl",
			mcp.Required(),
			mcp.Description("Time-to-live in seconds for the lock."),
		),
		mcp.WithBoolean("reentrant",
			mcp.Description("Whether the same holder can re-acquire the lock."),
		),
	)
}

// handleLockAcquire returns a tool handler that delegates to lockSvc.Lock.
func handleLockAcquire(lockSvc service.LockServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		lockSvc, rerr := resolveLock(ctx, lockSvc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		key := mcp.ParseString(req, "key", "")
		if key == "" {
			return mcp.NewToolResultError("key is required"), nil
		}
		holderID := mcp.ParseString(req, "holder_id", "")
		if holderID == "" {
			return mcp.NewToolResultError("holder_id is required"), nil
		}
		ttl := mcp.ParseInt64(req, "ttl", 0)
		if ttl <= 0 {
			return mcp.NewToolResultError("ttl must be greater than 0"), nil
		}
		reentrant := mcp.ParseBoolean(req, "reentrant", false)

		l, err := lockSvc.Lock(ctx, key, holderID, time.Duration(ttl)*time.Second, reentrant)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_lock_acquire", Err: err}
		}

		data, jsonErr := json.Marshal(l)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_lock_acquire", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolLockTryLock defines the qrdb_lock_trylock tool schema.
func toolLockTryLock() mcp.Tool {
	return mcp.NewTool("qrdb_lock_trylock",
		mcp.WithDescription("Attempt to acquire a distributed lock immediately without blocking. Returns an error if the lock is held by another holder."),
		mcp.WithString("key",
			mcp.Required(),
			mcp.Description("The resource key to lock."),
		),
		mcp.WithString("holder_id",
			mcp.Required(),
			mcp.Description("Unique identifier for the lock holder."),
		),
		mcp.WithNumber("ttl",
			mcp.Required(),
			mcp.Description("Time-to-live in seconds for the lock."),
		),
		mcp.WithBoolean("reentrant",
			mcp.Description("Whether the same holder can re-acquire the lock."),
		),
	)
}

// handleLockTryLock returns a tool handler that delegates to lockSvc.TryLock.
func handleLockTryLock(lockSvc service.LockServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		lockSvc, rerr := resolveLock(ctx, lockSvc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		key := mcp.ParseString(req, "key", "")
		if key == "" {
			return mcp.NewToolResultError("key is required"), nil
		}
		holderID := mcp.ParseString(req, "holder_id", "")
		if holderID == "" {
			return mcp.NewToolResultError("holder_id is required"), nil
		}
		ttl := mcp.ParseInt64(req, "ttl", 0)
		if ttl <= 0 {
			return mcp.NewToolResultError("ttl must be greater than 0"), nil
		}
		reentrant := mcp.ParseBoolean(req, "reentrant", false)

		l, err := lockSvc.TryLock(ctx, key, holderID, time.Duration(ttl)*time.Second, reentrant)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_lock_trylock", Err: err}
		}

		data, jsonErr := json.Marshal(l)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_lock_trylock", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolLockRelease defines the qrdb_lock_release tool schema.
func toolLockRelease() mcp.Tool {
	return mcp.NewTool("qrdb_lock_release",
		mcp.WithDescription("Release a held distributed lock. The fencing token must match the current lock state to prevent stale unlocks."),
		mcp.WithString("key",
			mcp.Required(),
			mcp.Description("The resource key of the lock to release."),
		),
		mcp.WithNumber("fencing_token",
			mcp.Required(),
			mcp.Description("The fencing token returned when the lock was acquired."),
		),
	)
}

// handleLockRelease returns a tool handler that delegates to lockSvc.Unlock.
func handleLockRelease(lockSvc service.LockServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		lockSvc, rerr := resolveLock(ctx, lockSvc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		key := mcp.ParseString(req, "key", "")
		if key == "" {
			return mcp.NewToolResultError("key is required"), nil
		}
		fencingToken := mcp.ParseInt64(req, "fencing_token", 0)
		if fencingToken == 0 {
			return mcp.NewToolResultError("fencing_token is required"), nil
		}

		if err := lockSvc.Unlock(ctx, key, fencingToken); err != nil {
			return nil, &ToolError{Tool: "qrdb_lock_release", Err: err}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// toolLockExtend defines the qrdb_lock_extend tool schema.
func toolLockExtend() mcp.Tool {
	return mcp.NewTool("qrdb_lock_extend",
		mcp.WithDescription("Refresh the TTL on a held distributed lock without releasing it. The fencing token must match."),
		mcp.WithString("key",
			mcp.Required(),
			mcp.Description("The resource key of the lock to extend."),
		),
		mcp.WithNumber("fencing_token",
			mcp.Required(),
			mcp.Description("The fencing token returned when the lock was acquired."),
		),
	)
}

// handleLockExtend returns a tool handler that delegates to lockSvc.Extend.
func handleLockExtend(lockSvc service.LockServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		lockSvc, rerr := resolveLock(ctx, lockSvc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		key := mcp.ParseString(req, "key", "")
		if key == "" {
			return mcp.NewToolResultError("key is required"), nil
		}
		fencingToken := mcp.ParseInt64(req, "fencing_token", 0)
		if fencingToken == 0 {
			return mcp.NewToolResultError("fencing_token is required"), nil
		}

		l, err := lockSvc.Extend(ctx, key, fencingToken)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_lock_extend", Err: err}
		}

		data, jsonErr := json.Marshal(l)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_lock_extend", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolLockStatus defines the qrdb_lock_status tool schema.
func toolLockStatus() mcp.Tool {
	return mcp.NewTool("qrdb_lock_status",
		mcp.WithDescription("Return the current state of a distributed lock for a key, or nil if the key is not locked."),
		mcp.WithString("key",
			mcp.Required(),
			mcp.Description("The resource key to query."),
		),
	)
}

// handleLockStatus returns a tool handler that delegates to lockSvc.Status.
func handleLockStatus(lockSvc service.LockServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		lockSvc, rerr := resolveLock(ctx, lockSvc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		key := mcp.ParseString(req, "key", "")
		if key == "" {
			return mcp.NewToolResultError("key is required"), nil
		}

		l, err := lockSvc.Status(ctx, key)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_lock_status", Err: err}
		}

		data, jsonErr := json.Marshal(l)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_lock_status", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolLockList defines the qrdb_lock_list tool schema.
func toolLockList() mcp.Tool {
	return mcp.NewTool("qrdb_lock_list",
		mcp.WithDescription("Return all active distributed locks, optionally filtered by key prefix."),
		mcp.WithString("prefix",
			mcp.Description("Optional key prefix to filter locks."),
		),
	)
}

// handleLockList returns a tool handler that delegates to lockSvc.List.
func handleLockList(lockSvc service.LockServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		lockSvc, rerr := resolveLock(ctx, lockSvc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		prefix := mcp.ParseString(req, "prefix", "")

		locks, err := lockSvc.List(ctx, prefix)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_lock_list", Err: err}
		}

		data, jsonErr := json.Marshal(locks)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_lock_list", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}
