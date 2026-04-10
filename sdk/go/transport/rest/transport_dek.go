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

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.DEKClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// DEKClient implementation
// ---------------------------------------------------------------------------

// DEKInitialize initializes the cluster DEK with the given algorithm and key
// size. Maps to POST /v1/dek/initialize (204 No Content).
func (t *Transport) DEKInitialize(ctx context.Context, req *transport.DEKInitializeRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	return t.postJSONDecode(ctx, "DEKInitialize", "/v1/dek/initialize", req, nil)
}

// DEKGetStatus returns the current cluster DEK status.
// Maps to GET /v1/dek/status.
func (t *Transport) DEKGetStatus(ctx context.Context) (*transport.DEKStatusResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/v1/dek/status", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "DEKGetStatus", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "DEKGetStatus", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("DEKGetStatus", resp)
	}

	var statusResp transport.DEKStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&statusResp); err != nil {
		return nil, &transport.RequestError{Method: "DEKGetStatus", Err: &jsonDecodeError{Err: err}}
	}

	return &statusResp, nil
}

// DEKRotate rotates the cluster DEK to a new version.
// Maps to POST /v1/dek/rotate (204 No Content).
func (t *Transport) DEKRotate(ctx context.Context) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	return t.postJSONDecode(ctx, "DEKRotate", "/v1/dek/rotate", nil, nil)
}

// DEKGetCurrent returns the current DEK metadata.
// Maps to GET /v1/dek/current.
func (t *Transport) DEKGetCurrent(ctx context.Context) (*transport.DEKCurrentResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/v1/dek/current", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "DEKGetCurrent", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "DEKGetCurrent", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("DEKGetCurrent", resp)
	}

	var currentResp transport.DEKCurrentResponse
	if err := json.NewDecoder(resp.Body).Decode(&currentResp); err != nil {
		return nil, &transport.RequestError{Method: "DEKGetCurrent", Err: &jsonDecodeError{Err: err}}
	}

	return &currentResp, nil
}

// DEKSyncToNode synchronizes the DEK to a specific node.
// Maps to POST /v1/dek/sync (204 No Content).
func (t *Transport) DEKSyncToNode(ctx context.Context, req *transport.DEKSyncRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	return t.postJSONDecode(ctx, "DEKSyncToNode", "/v1/dek/sync", req, nil)
}

// TenantDEKGetOrCreate returns the DEK for a tenant, creating if needed.
// Maps to POST /v1/dek/tenant.
func (t *Transport) TenantDEKGetOrCreate(ctx context.Context, req *transport.TenantDEKRequest) (*transport.TenantDEKResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var tenantResp transport.TenantDEKResponse
	if err := t.postJSONDecode(ctx, "TenantDEKGetOrCreate", "/v1/dek/tenant", req, &tenantResp); err != nil {
		return nil, err
	}

	return &tenantResp, nil
}

// TenantDEKRotate rotates the DEK for a specific tenant.
// Maps to POST /v1/dek/tenant/rotate (204 No Content).
func (t *Transport) TenantDEKRotate(ctx context.Context, req *transport.TenantDEKRotateRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	return t.postJSONDecode(ctx, "TenantDEKRotate", "/v1/dek/tenant/rotate", req, nil)
}

// TenantDEKDelete deletes a tenant's DEK.
// Maps to POST /v1/dek/tenant/delete (204 No Content).
func (t *Transport) TenantDEKDelete(ctx context.Context, req *transport.TenantDEKDeleteRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	return t.postJSONDecode(ctx, "TenantDEKDelete", "/v1/dek/tenant/delete", req, nil)
}

// TenantDEKList returns all tenant IDs that have DEKs.
// Maps to GET /v1/dek/tenants.
func (t *Transport) TenantDEKList(ctx context.Context) (*transport.TenantDEKListResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/v1/dek/tenants", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantDEKList", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantDEKList", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("TenantDEKList", resp)
	}

	var listResp transport.TenantDEKListResponse
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		return nil, &transport.RequestError{Method: "TenantDEKList", Err: &jsonDecodeError{Err: err}}
	}

	return &listResp, nil
}

// TenantDEKGetStatus returns the status of a tenant's DEK.
// Maps to POST /v1/dek/tenant/status.
func (t *Transport) TenantDEKGetStatus(ctx context.Context, req *transport.TenantDEKRequest) (*transport.TenantDEKStatusResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var statusResp transport.TenantDEKStatusResponse
	if err := t.postJSONDecode(ctx, "TenantDEKGetStatus", "/v1/dek/tenant/status", req, &statusResp); err != nil {
		return nil, err
	}

	return &statusResp, nil
}
