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
	"strconv"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.LeaseClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// Wire types for lease REST communication
// ---------------------------------------------------------------------------

// leaseGrantWireRequest matches the JSON payload for POST /lease/grant.
type leaseGrantWireRequest struct {
	TTL int64 `json:"ttl"`
}

// leaseIDWireRequest matches the JSON payload for POST /lease/revoke
// and POST /lease/keepalive.
type leaseIDWireRequest struct {
	LeaseID int64 `json:"lease_id"`
}

// leaseWireResponse matches the JSON lease shape returned by the REST
// server. The server encodes keys as strings (not []byte).
type leaseWireResponse struct {
	ID           int64    `json:"id"`
	TTL          int64    `json:"ttl"`
	GrantedTTL   int64    `json:"granted_ttl"`
	RemainingTTL int64    `json:"remaining_ttl"`
	Keys         []string `json:"keys"`
}

// leasesWireResponse matches the JSON payload returned by GET /lease/list.
type leasesWireResponse struct {
	Leases []*leaseWireResponse `json:"leases"`
}

// ---------------------------------------------------------------------------
// LeaseClient implementation
// ---------------------------------------------------------------------------

// LeaseGrant creates a new lease with the specified TTL in seconds. Maps
// to POST /lease/grant with a JSON body containing the TTL.
func (t *Transport) LeaseGrant(ctx context.Context, ttl int64) (*transport.LeaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload := leaseGrantWireRequest{TTL: ttl}

	var wireResp leaseWireResponse
	if err := t.postJSONDecode(ctx, "LeaseGrant", "/lease/grant", payload, &wireResp); err != nil {
		return nil, err
	}

	return wireLeaseToTransport(&wireResp), nil
}

// LeaseRevoke immediately expires a lease and deletes attached keys.
// Maps to POST /lease/revoke with a JSON body containing the lease ID.
func (t *Transport) LeaseRevoke(ctx context.Context, leaseID int64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload := leaseIDWireRequest{LeaseID: leaseID}
	return t.postJSON(ctx, "LeaseRevoke", "/lease/revoke", payload)
}

// LeaseKeepAlive resets the TTL on an existing lease. Maps to
// POST /lease/keepalive with a JSON body containing the lease ID.
func (t *Transport) LeaseKeepAlive(ctx context.Context, leaseID int64) (*transport.LeaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload := leaseIDWireRequest{LeaseID: leaseID}

	var wireResp leaseWireResponse
	if err := t.postJSONDecode(ctx, "LeaseKeepAlive", "/lease/keepalive", payload, &wireResp); err != nil {
		return nil, err
	}

	return wireLeaseToTransport(&wireResp), nil
}

// LeaseTimeToLive returns the current state of a lease. Maps to
// GET /lease/ttl?lease_id=N.
func (t *Transport) LeaseTimeToLive(ctx context.Context, leaseID int64) (*transport.LeaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/lease/ttl?lease_id=" + strconv.FormatInt(leaseID, 10)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "LeaseTimeToLive", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "LeaseTimeToLive", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, &transport.LeaseNotFoundError{LeaseID: leaseID}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("LeaseTimeToLive", resp)
	}

	var wireResp leaseWireResponse
	if err := json.NewDecoder(resp.Body).Decode(&wireResp); err != nil {
		return nil, &transport.RequestError{Method: "LeaseTimeToLive", Err: &jsonDecodeError{Err: err}}
	}

	return wireLeaseToTransport(&wireResp), nil
}

// LeaseList returns all active leases. Maps to GET /lease/list.
func (t *Transport) LeaseList(ctx context.Context) ([]*transport.LeaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/lease/list", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "LeaseList", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "LeaseList", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("LeaseList", resp)
	}

	var wireResp leasesWireResponse
	if err := json.NewDecoder(resp.Body).Decode(&wireResp); err != nil {
		return nil, &transport.RequestError{Method: "LeaseList", Err: &jsonDecodeError{Err: err}}
	}

	leases := make([]*transport.LeaseInfo, len(wireResp.Leases))
	for i, wl := range wireResp.Leases {
		leases[i] = wireLeaseToTransport(wl)
	}

	return leases, nil
}

// ---------------------------------------------------------------------------
// Wire conversion helpers
// ---------------------------------------------------------------------------

// wireLeaseToTransport converts a wire lease response to the transport
// LeaseInfo type. The server encodes keys as strings which are converted
// to [][]byte for the SDK representation.
func wireLeaseToTransport(wl *leaseWireResponse) *transport.LeaseInfo {
	if wl == nil {
		return nil
	}

	var keys [][]byte
	if len(wl.Keys) > 0 {
		keys = make([][]byte, len(wl.Keys))
		for i, k := range wl.Keys {
			keys[i] = []byte(k)
		}
	}

	return &transport.LeaseInfo{
		ID:           wl.ID,
		TTL:          wl.TTL,
		GrantedTTL:   wl.GrantedTTL,
		RemainingTTL: wl.RemainingTTL,
		Keys:         keys,
	}
}
