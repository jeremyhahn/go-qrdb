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
var _ transport.DEKClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// DEKClient implementation
// ---------------------------------------------------------------------------

// DEKInitialize initializes the cluster DEK with the given algorithm and key
// size by calling the qrdb_dek_initialize MCP tool.
func (t *Transport) DEKInitialize(ctx context.Context, req *transport.DEKInitializeRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	if _, err := t.callTool(ctx, "qrdb_dek_initialize", map[string]any{
		"algorithm": req.Algorithm,
		"key_bytes": req.KeyBytes,
	}); err != nil {
		return &transport.RequestError{Method: "DEKInitialize", Err: err}
	}

	return nil
}

// DEKGetStatus returns the current cluster DEK status by calling the
// qrdb_dek_status MCP tool.
func (t *Transport) DEKGetStatus(ctx context.Context) (*transport.DEKStatusResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_dek_status", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "DEKGetStatus", Err: err}
	}

	var resp transport.DEKStatusResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "DEKGetStatus", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &resp, nil
}

// DEKRotate rotates the cluster DEK to a new version by calling the
// qrdb_dek_rotate MCP tool.
func (t *Transport) DEKRotate(ctx context.Context) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	if _, err := t.callTool(ctx, "qrdb_dek_rotate", nil); err != nil {
		return &transport.RequestError{Method: "DEKRotate", Err: err}
	}

	return nil
}

// DEKGetCurrent returns the current DEK metadata by calling the
// qrdb_dek_current MCP tool.
func (t *Transport) DEKGetCurrent(ctx context.Context) (*transport.DEKCurrentResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_dek_current", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "DEKGetCurrent", Err: err}
	}

	var resp transport.DEKCurrentResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "DEKGetCurrent", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &resp, nil
}

// DEKSyncToNode synchronizes the DEK to a specific node by calling the
// qrdb_dek_sync MCP tool.
func (t *Transport) DEKSyncToNode(ctx context.Context, req *transport.DEKSyncRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	if _, err := t.callTool(ctx, "qrdb_dek_sync", map[string]any{
		"node_id": req.NodeID,
	}); err != nil {
		return &transport.RequestError{Method: "DEKSyncToNode", Err: err}
	}

	return nil
}

// TenantDEKGetOrCreate returns the DEK for a tenant, creating if needed,
// by calling the qrdb_dek_tenant_get_or_create MCP tool.
func (t *Transport) TenantDEKGetOrCreate(ctx context.Context, req *transport.TenantDEKRequest) (*transport.TenantDEKResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_dek_tenant_get_or_create", map[string]any{
		"tenant_id": req.TenantID,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantDEKGetOrCreate", Err: err}
	}

	var resp transport.TenantDEKResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "TenantDEKGetOrCreate", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &resp, nil
}

// TenantDEKRotate rotates the DEK for a specific tenant by calling the
// qrdb_dek_tenant_rotate MCP tool.
func (t *Transport) TenantDEKRotate(ctx context.Context, req *transport.TenantDEKRotateRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	if _, err := t.callTool(ctx, "qrdb_dek_tenant_rotate", map[string]any{
		"tenant_id": req.TenantID,
	}); err != nil {
		return &transport.RequestError{Method: "TenantDEKRotate", Err: err}
	}

	return nil
}

// TenantDEKDelete deletes a tenant's DEK by calling the
// qrdb_dek_tenant_delete MCP tool.
func (t *Transport) TenantDEKDelete(ctx context.Context, req *transport.TenantDEKDeleteRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	if _, err := t.callTool(ctx, "qrdb_dek_tenant_delete", map[string]any{
		"tenant_id": req.TenantID,
	}); err != nil {
		return &transport.RequestError{Method: "TenantDEKDelete", Err: err}
	}

	return nil
}

// TenantDEKList returns all tenant IDs that have DEKs by calling the
// qrdb_dek_tenant_list MCP tool.
func (t *Transport) TenantDEKList(ctx context.Context) (*transport.TenantDEKListResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_dek_tenant_list", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantDEKList", Err: err}
	}

	var resp transport.TenantDEKListResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "TenantDEKList", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &resp, nil
}

// TenantDEKGetStatus returns the status of a tenant's DEK by calling the
// qrdb_dek_tenant_status MCP tool.
func (t *Transport) TenantDEKGetStatus(ctx context.Context, req *transport.TenantDEKRequest) (*transport.TenantDEKStatusResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_dek_tenant_status", map[string]any{
		"tenant_id": req.TenantID,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantDEKGetStatus", Err: err}
	}

	var resp transport.TenantDEKStatusResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "TenantDEKGetStatus", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &resp, nil
}
