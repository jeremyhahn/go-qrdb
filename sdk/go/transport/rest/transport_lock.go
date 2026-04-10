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
	"net/http"
	"net/url"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.LockClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// Wire types for lock REST communication
// ---------------------------------------------------------------------------

// lockAcquireWireRequest matches the JSON payload for POST /lock/acquire
// and POST /lock/trylock.
type lockAcquireWireRequest struct {
	Key       string `json:"key"`
	HolderID  string `json:"holder_id,omitempty"`
	TTL       int64  `json:"ttl"`
	Reentrant bool   `json:"reentrant,omitempty"`
}

// lockReleaseWireRequest matches the JSON payload for POST /lock/release.
type lockReleaseWireRequest struct {
	Key          string `json:"key"`
	FencingToken int64  `json:"fencing_token"`
}

// lockExtendWireRequest matches the JSON payload for POST /lock/extend.
type lockExtendWireRequest struct {
	Key          string `json:"key"`
	FencingToken int64  `json:"fencing_token"`
}

// lockWireResponse matches the JSON lock shape returned by the REST server.
type lockWireResponse struct {
	Key          string `json:"key"`
	FencingToken int64  `json:"fencing_token"`
	LeaseID      int64  `json:"lease_id"`
	HolderID     string `json:"holder_id"`
	Reentrant    bool   `json:"reentrant"`
	AcquireCount int32  `json:"acquire_count"`
	TTL          int64  `json:"ttl"`
	AcquiredAt   int64  `json:"acquired_at"`
}

// locksWireResponse matches the JSON payload returned by GET /lock/list.
type locksWireResponse struct {
	Locks []*lockWireResponse `json:"locks"`
}

// ---------------------------------------------------------------------------
// LockClient implementation
// ---------------------------------------------------------------------------

// LockAcquire acquires a distributed lock, blocking until acquired or the
// context is cancelled. Maps to POST /lock/acquire.
func (t *Transport) LockAcquire(ctx context.Context, req *transport.LockAcquireRequest) (*transport.LockInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload := lockAcquireWireRequest{
		Key:       req.Key,
		HolderID:  req.HolderID,
		TTL:       req.TTL,
		Reentrant: req.Reentrant,
	}

	var wireResp lockWireResponse
	if err := t.postJSONDecode(ctx, "LockAcquire", "/lock/acquire", payload, &wireResp); err != nil {
		return nil, err
	}

	return wireLockToTransport(&wireResp), nil
}

// LockTryLock attempts to acquire the lock without blocking. Maps to
// POST /lock/trylock.
func (t *Transport) LockTryLock(ctx context.Context, req *transport.LockAcquireRequest) (*transport.LockInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload := lockAcquireWireRequest{
		Key:       req.Key,
		HolderID:  req.HolderID,
		TTL:       req.TTL,
		Reentrant: req.Reentrant,
	}

	var wireResp lockWireResponse
	if err := t.postJSONDecode(ctx, "LockTryLock", "/lock/trylock", payload, &wireResp); err != nil {
		return nil, err
	}

	return wireLockToTransport(&wireResp), nil
}

// LockRelease releases the distributed lock identified by key and fencing
// token. Maps to POST /lock/release.
func (t *Transport) LockRelease(ctx context.Context, req *transport.LockReleaseRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload := lockReleaseWireRequest{
		Key:          req.Key,
		FencingToken: req.FencingToken,
	}

	return t.postJSON(ctx, "LockRelease", "/lock/release", payload)
}

// LockExtend refreshes the TTL of an existing lock via lease keepalive.
// Maps to POST /lock/extend.
func (t *Transport) LockExtend(ctx context.Context, req *transport.LockExtendRequest) (*transport.LockInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload := lockExtendWireRequest{
		Key:          req.Key,
		FencingToken: req.FencingToken,
	}

	var wireResp lockWireResponse
	if err := t.postJSONDecode(ctx, "LockExtend", "/lock/extend", payload, &wireResp); err != nil {
		return nil, err
	}

	return wireLockToTransport(&wireResp), nil
}

// LockStatus returns the current lock state for key. Maps to
// GET /lock/status?key=<key>.
func (t *Transport) LockStatus(ctx context.Context, key string) (*transport.LockInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/lock/status?key=" + url.QueryEscape(key)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "LockStatus", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "LockStatus", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("LockStatus", resp)
	}

	var wireResp lockWireResponse
	if err := json.NewDecoder(resp.Body).Decode(&wireResp); err != nil {
		return nil, &transport.RequestError{Method: "LockStatus", Err: &jsonDecodeError{Err: err}}
	}

	return wireLockToTransport(&wireResp), nil
}

// LockList returns all active locks whose keys start with the given prefix.
// Maps to GET /lock/list?prefix=<prefix>.
func (t *Transport) LockList(ctx context.Context, prefix string) (*transport.LockListResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/lock/list"
	if prefix != "" {
		reqURL += "?prefix=" + url.QueryEscape(prefix)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "LockList", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "LockList", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("LockList", resp)
	}

	var wireResp locksWireResponse
	if err := json.NewDecoder(resp.Body).Decode(&wireResp); err != nil {
		return nil, &transport.RequestError{Method: "LockList", Err: &jsonDecodeError{Err: err}}
	}

	locks := make([]*transport.LockInfo, len(wireResp.Locks))
	for i, wl := range wireResp.Locks {
		locks[i] = wireLockToTransport(wl)
	}

	return &transport.LockListResponse{Locks: locks}, nil
}

// ---------------------------------------------------------------------------
// Wire conversion helpers
// ---------------------------------------------------------------------------

// wireLockToTransport converts a wire lock response to the transport LockInfo type.
func wireLockToTransport(wl *lockWireResponse) *transport.LockInfo {
	if wl == nil {
		return nil
	}
	return &transport.LockInfo{
		Key:          wl.Key,
		FencingToken: wl.FencingToken,
		LeaseID:      wl.LeaseID,
		HolderID:     wl.HolderID,
		Reentrant:    wl.Reentrant,
		AcquireCount: wl.AcquireCount,
		TTL:          wl.TTL,
		AcquiredAt:   wl.AcquiredAt,
	}
}
