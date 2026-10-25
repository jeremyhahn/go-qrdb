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
	"strconv"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.VolumePoolClient = (*Transport)(nil)

// ListPools lists every pool in the catalog. Maps to GET /v1/volume-pools/list.
func (t *Transport) ListPools(ctx context.Context) ([]transport.VolumePool, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/v1/volume-pools/list", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListPools", Err: err}
	}
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListPools", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("ListPools", resp)
	}
	var out struct {
		Pools []transport.VolumePool `json:"pools"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, &transport.RequestError{Method: "ListPools", Err: &jsonDecodeError{Err: err}}
	}
	return out.Pools, nil
}

// GetPool inspects one pool. Maps to GET /v1/volume-pools/{pool}.
func (t *Transport) GetPool(ctx context.Context, name string) (*transport.VolumePoolDetail, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	reqURL := t.baseURL + "/v1/volume-pools/" + url.PathEscape(name)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetPool", Err: err}
	}
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetPool", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("GetPool", resp)
	}
	var detail transport.VolumePoolDetail
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		return nil, &transport.RequestError{Method: "GetPool", Err: &jsonDecodeError{Err: err}}
	}
	return &detail, nil
}

// CreatePool adds a pool to the catalog. Maps to POST /v1/volume-pools.
func (t *Transport) CreatePool(ctx context.Context, req *transport.CreateVolumePoolRequest) (*transport.VolumePool, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var pool transport.VolumePool
	if err := t.postJSONDecode(ctx, "CreatePool", "/v1/volume-pools", req, &pool); err != nil {
		return nil, err
	}
	return &pool, nil
}

// UpdatePool changes a pool's description, capacity or overcommit. Maps to
// PUT /v1/volume-pools/{pool}.
func (t *Transport) UpdatePool(ctx context.Context, req *transport.UpdateVolumePoolRequest) (*transport.VolumePool, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "UpdatePool", Err: &transport.NilRequestError{}}
	}
	var pool transport.VolumePool
	path := "/v1/volume-pools/" + url.PathEscape(req.Name)
	if err := t.putJSONDecode(ctx, "UpdatePool", path, req, &pool); err != nil {
		return nil, err
	}
	return &pool, nil
}

// DeletePool removes a pool from the catalog. Maps to
// DELETE /v1/volume-pools/{pool}.
func (t *Transport) DeletePool(ctx context.Context, name string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	return t.deleteNoContent(ctx, "DeletePool", "/v1/volume-pools/"+url.PathEscape(name))
}

// ListPoolTenants lists the tenants assigned to a pool. Maps to
// GET /v1/volume-pools/{pool}/tenants.
func (t *Transport) ListPoolTenants(ctx context.Context, req *transport.ListVolumePoolTenantsRequest) (*transport.ListVolumePoolTenantsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		req = &transport.ListVolumePoolTenantsRequest{}
	}
	q := url.Values{}
	if req.PageSize != 0 {
		q.Set("page_size", strconv.Itoa(req.PageSize))
	}
	if req.PageToken != "" {
		q.Set("page_token", req.PageToken)
	}
	reqURL := t.baseURL + "/v1/volume-pools/" + url.PathEscape(req.Pool) + "/tenants"
	if len(q) > 0 {
		reqURL += "?" + q.Encode()
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListPoolTenants", Err: err}
	}
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListPoolTenants", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("ListPoolTenants", resp)
	}
	out := &transport.ListVolumePoolTenantsResponse{}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return nil, &transport.RequestError{Method: "ListPoolTenants", Err: &jsonDecodeError{Err: err}}
	}
	return out, nil
}
