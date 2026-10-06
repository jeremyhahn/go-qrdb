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
var _ transport.VolumePoolClient = (*Transport)(nil)

// mcpListVolumePoolTenantsResult mirrors the "tenants"/"next_page_token"
// JSON shape returned by the qrdb_volume_pool_list_tenants tool (see
// pkg/api/mcp/tools_volume_pool.go's listVolumePoolTenantsResult).
type mcpListVolumePoolTenantsResult struct {
	Tenants []struct {
		TenantID      string `json:"tenantId"`
		ReservedBytes int64  `json:"reservedBytes"`
	} `json:"tenants"`
	NextPageToken string `json:"next_page_token,omitempty"`
}

// ListPools lists every pool in the catalog by calling the
// qrdb_volume_pool_list MCP tool.
func (t *Transport) ListPools(ctx context.Context) ([]transport.VolumePool, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	text, err := t.callTool(ctx, "qrdb_volume_pool_list", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListPools", Err: err}
	}
	var pools []transport.VolumePool
	if jsonErr := json.Unmarshal([]byte(text), &pools); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ListPools", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return pools, nil
}

// GetPool inspects one pool by calling the qrdb_volume_pool_get MCP tool.
func (t *Transport) GetPool(ctx context.Context, name string) (*transport.VolumePoolDetail, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	text, err := t.callTool(ctx, "qrdb_volume_pool_get", map[string]any{"pool": name})
	if err != nil {
		return nil, &transport.RequestError{Method: "GetPool", Err: err}
	}
	var detail transport.VolumePoolDetail
	if jsonErr := json.Unmarshal([]byte(text), &detail); jsonErr != nil {
		return nil, &transport.RequestError{Method: "GetPool", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return &detail, nil
}

// CreatePool adds a pool to the catalog by calling the
// qrdb_volume_pool_create MCP tool.
func (t *Transport) CreatePool(ctx context.Context, req *transport.CreateVolumePoolRequest) (*transport.VolumePool, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	args := map[string]any{"name": req.Name}
	if req.Provider != "" {
		args["provider"] = req.Provider
	}
	if req.Description != "" {
		args["description"] = req.Description
	}
	if req.CapacityBytes != 0 {
		args["capacity_bytes"] = req.CapacityBytes
	}
	if req.Overcommit != 0 {
		args["overcommit"] = req.Overcommit
	}
	text, err := t.callTool(ctx, "qrdb_volume_pool_create", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "CreatePool", Err: err}
	}
	var pool transport.VolumePool
	if jsonErr := json.Unmarshal([]byte(text), &pool); jsonErr != nil {
		return nil, &transport.RequestError{Method: "CreatePool", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return &pool, nil
}

// UpdatePool changes a pool's description, capacity or overcommit by
// calling the qrdb_volume_pool_update MCP tool.
func (t *Transport) UpdatePool(ctx context.Context, req *transport.UpdateVolumePoolRequest) (*transport.VolumePool, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	args := map[string]any{"pool": req.Name}
	if req.Description != nil {
		args["description"] = *req.Description
	}
	if req.CapacityBytes != nil {
		args["capacity_bytes"] = *req.CapacityBytes
	}
	if req.Overcommit != nil {
		args["overcommit"] = *req.Overcommit
	}
	text, err := t.callTool(ctx, "qrdb_volume_pool_update", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "UpdatePool", Err: err}
	}
	var pool transport.VolumePool
	if jsonErr := json.Unmarshal([]byte(text), &pool); jsonErr != nil {
		return nil, &transport.RequestError{Method: "UpdatePool", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return &pool, nil
}

// DeletePool removes a pool from the catalog by calling the
// qrdb_volume_pool_delete MCP tool.
func (t *Transport) DeletePool(ctx context.Context, name string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if _, err := t.callTool(ctx, "qrdb_volume_pool_delete", map[string]any{"pool": name}); err != nil {
		return &transport.RequestError{Method: "DeletePool", Err: err}
	}
	return nil
}

// ListPoolTenants lists the tenants assigned to a pool by calling the
// qrdb_volume_pool_list_tenants MCP tool.
func (t *Transport) ListPoolTenants(ctx context.Context, req *transport.ListVolumePoolTenantsRequest) (*transport.ListVolumePoolTenantsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		req = &transport.ListVolumePoolTenantsRequest{}
	}
	args := map[string]any{"pool": req.Pool}
	if req.PageSize != 0 {
		args["page_size"] = req.PageSize
	}
	if req.PageToken != "" {
		args["page_token"] = req.PageToken
	}
	text, err := t.callTool(ctx, "qrdb_volume_pool_list_tenants", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListPoolTenants", Err: err}
	}
	var result mcpListVolumePoolTenantsResult
	if jsonErr := json.Unmarshal([]byte(text), &result); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ListPoolTenants", Err: &jsonDecodeError{Err: jsonErr}}
	}
	out := &transport.ListVolumePoolTenantsResponse{
		Tenants:       make([]transport.VolumePoolTenant, len(result.Tenants)),
		NextPageToken: result.NextPageToken,
	}
	for i, tn := range result.Tenants {
		out.Tenants[i] = transport.VolumePoolTenant{TenantID: tn.TenantID, ReservedBytes: tn.ReservedBytes}
	}
	return out, nil
}
