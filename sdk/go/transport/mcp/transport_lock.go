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

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.LockClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// Server-side JSON decode types for lock responses
// ---------------------------------------------------------------------------

// serverLock mirrors lock.Lock for JSON decoding from the MCP server.
// The server-side struct has no JSON tags, so Go's default PascalCase
// encoding is used.
type serverLock struct {
	Key          string `json:"Key"`
	FencingToken int64  `json:"FencingToken"`
	LeaseID      int64  `json:"LeaseID"`
	HolderID     string `json:"HolderID"`
	Reentrant    bool   `json:"Reentrant"`
	AcquireCount int32  `json:"AcquireCount"`
	TTL          int64  `json:"TTL"`
	AcquiredAt   int64  `json:"AcquiredAt"`
}

// serverLockToTransport converts a server-side Lock to the SDK
// transport representation.
func serverLockToTransport(sl *serverLock) *transport.LockInfo {
	return &transport.LockInfo{
		Key:          sl.Key,
		FencingToken: sl.FencingToken,
		LeaseID:      sl.LeaseID,
		HolderID:     sl.HolderID,
		Reentrant:    sl.Reentrant,
		AcquireCount: sl.AcquireCount,
		TTL:          sl.TTL,
		AcquiredAt:   sl.AcquiredAt,
	}
}

// ---------------------------------------------------------------------------
// LockClient implementation
// ---------------------------------------------------------------------------

// LockAcquire acquires a distributed lock, blocking until acquired or the
// context is cancelled. Calls the qrdb_lock_acquire MCP tool.
func (t *Transport) LockAcquire(ctx context.Context, req *transport.LockAcquireRequest) (*transport.LockInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_lock_acquire", map[string]any{
		"key":       req.Key,
		"holder_id": req.HolderID,
		"ttl":       req.TTL,
		"reentrant": req.Reentrant,
	})
	if err != nil {
		return nil, err
	}

	var raw serverLock
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "LockAcquire", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return serverLockToTransport(&raw), nil
}

// LockTryLock attempts to acquire the lock without blocking. Returns an
// error if the lock is already held. Calls the qrdb_lock_trylock MCP tool.
func (t *Transport) LockTryLock(ctx context.Context, req *transport.LockAcquireRequest) (*transport.LockInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_lock_trylock", map[string]any{
		"key":       req.Key,
		"holder_id": req.HolderID,
		"ttl":       req.TTL,
		"reentrant": req.Reentrant,
	})
	if err != nil {
		return nil, err
	}

	var raw serverLock
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "LockTryLock", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return serverLockToTransport(&raw), nil
}

// LockRelease releases the distributed lock identified by key and fencing
// token. Calls the qrdb_lock_release MCP tool.
func (t *Transport) LockRelease(ctx context.Context, req *transport.LockReleaseRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.callTool(ctx, "qrdb_lock_release", map[string]any{
		"key":           req.Key,
		"fencing_token": req.FencingToken,
	})
	return err
}

// LockExtend refreshes the TTL of an existing lock. Calls the
// qrdb_lock_extend MCP tool.
func (t *Transport) LockExtend(ctx context.Context, req *transport.LockExtendRequest) (*transport.LockInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_lock_extend", map[string]any{
		"key":           req.Key,
		"fencing_token": req.FencingToken,
	})
	if err != nil {
		return nil, err
	}

	var raw serverLock
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "LockExtend", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return serverLockToTransport(&raw), nil
}

// LockStatus returns the current lock state for key. Returns nil if no lock
// is held. Calls the qrdb_lock_status MCP tool.
func (t *Transport) LockStatus(ctx context.Context, key string) (*transport.LockInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_lock_status", map[string]any{
		"key": key,
	})
	if err != nil {
		return nil, err
	}

	if text == "null" {
		return nil, nil
	}

	var raw serverLock
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "LockStatus", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return serverLockToTransport(&raw), nil
}

// LockList returns all active locks whose keys start with the given prefix.
// Calls the qrdb_lock_list MCP tool.
func (t *Transport) LockList(ctx context.Context, prefix string) (*transport.LockListResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{}
	if prefix != "" {
		args["prefix"] = prefix
	}

	text, err := t.callTool(ctx, "qrdb_lock_list", args)
	if err != nil {
		return nil, err
	}

	var raw []*serverLock
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "LockList", Err: &jsonDecodeError{Err: jsonErr}}
	}

	locks := make([]*transport.LockInfo, len(raw))
	for i, sl := range raw {
		locks[i] = serverLockToTransport(sl)
	}

	return &transport.LockListResponse{Locks: locks}, nil
}
