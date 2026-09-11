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
var _ transport.LeaseClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// Server-side JSON decode types for lease responses
// ---------------------------------------------------------------------------

// serverLease mirrors lease.Lease for JSON decoding from the MCP
// server. The server-side struct has no JSON tags, so Go's default
// PascalCase encoding is used.
type serverLease struct {
	ID           int64    `json:"ID"`
	TTL          int64    `json:"TTL"`
	GrantedTTL   int64    `json:"GrantedTTL"`
	RemainingTTL int64    `json:"RemainingTTL"`
	Keys         [][]byte `json:"Keys"`
}

// serverLeaseToTransport converts a server-side Lease to the SDK
// transport representation.
func serverLeaseToTransport(sl *serverLease) *transport.LeaseInfo {
	return &transport.LeaseInfo{
		ID:           sl.ID,
		TTL:          sl.TTL,
		GrantedTTL:   sl.GrantedTTL,
		RemainingTTL: sl.RemainingTTL,
		Keys:         sl.Keys,
	}
}

// ---------------------------------------------------------------------------
// LeaseClient implementation
// ---------------------------------------------------------------------------

// LeaseGrant creates a new lease with the specified TTL in seconds.
// Keys attached to the lease are automatically deleted when it expires.
// Calls the qrdb_lease_grant MCP tool.
func (t *Transport) LeaseGrant(ctx context.Context, ttl int64) (*transport.LeaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_lease_grant", map[string]any{
		"ttl": ttl,
	})
	if err != nil {
		return nil, err
	}

	var raw serverLease
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "LeaseGrant", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return serverLeaseToTransport(&raw), nil
}

// LeaseRevoke immediately expires a lease and deletes all keys
// attached to it. Calls the qrdb_lease_revoke MCP tool.
func (t *Transport) LeaseRevoke(ctx context.Context, leaseID int64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.callTool(ctx, "qrdb_lease_revoke", map[string]any{
		"lease_id": leaseID,
	})
	if err != nil {
		return mapLeaseAbsent(leaseID, err)
	}
	return nil
}

// LeaseKeepAlive resets the TTL on an existing lease, preventing it
// from expiring. Calls the qrdb_lease_keepalive MCP tool.
func (t *Transport) LeaseKeepAlive(ctx context.Context, leaseID int64) (*transport.LeaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_lease_keepalive", map[string]any{
		"lease_id": leaseID,
	})
	if err != nil {
		return nil, mapLeaseAbsent(leaseID, err)
	}

	var raw serverLease
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "LeaseKeepAlive", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return serverLeaseToTransport(&raw), nil
}

// LeaseTimeToLive returns the current state of a lease including its
// remaining TTL and attached keys. Calls the qrdb_lease_ttl MCP tool.
func (t *Transport) LeaseTimeToLive(ctx context.Context, leaseID int64) (*transport.LeaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_lease_ttl", map[string]any{
		"lease_id": leaseID,
	})
	if err != nil {
		return nil, mapLeaseAbsent(leaseID, err)
	}

	var raw serverLease
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "LeaseTimeToLive", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return serverLeaseToTransport(&raw), nil
}

// LeaseList returns all active leases. Calls the qrdb_lease_list MCP
// tool and parses the JSON array of lease objects.
func (t *Transport) LeaseList(ctx context.Context) ([]*transport.LeaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_lease_list", nil)
	if err != nil {
		return nil, err
	}

	var raw []serverLease
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "LeaseList", Err: &jsonDecodeError{Err: jsonErr}}
	}

	leases := make([]*transport.LeaseInfo, len(raw))
	for i := range raw {
		leases[i] = serverLeaseToTransport(&raw[i])
	}

	return leases, nil
}
