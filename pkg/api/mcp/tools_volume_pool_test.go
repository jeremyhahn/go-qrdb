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
	"errors"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/volume"
	"github.com/jeremyhahn/go-qrdb/pkg/volume/volumetest"
)

// invokeTool invokes a handler directly and returns its result and error.
func invokeTool(t *testing.T, h mcpserver.ToolHandlerFunc, tool string, args map[string]any) (*mcp.CallToolResult, error) {
	t.Helper()
	return h(context.Background(), buildCallToolRequest(t, tool, args))
}

// requireToolError asserts err is a *ToolError naming tool and wrapping target.
func requireToolError(t *testing.T, err error, tool string, target error) {
	t.Helper()
	var te *ToolError
	if !errors.As(err, &te) || te.Tool != tool {
		t.Fatalf("err = %v, want *ToolError for %s", err, tool)
	}
	if !errors.Is(err, target) {
		t.Fatalf("err = %v, want it to wrap %v", err, target)
	}
}

// requireArgumentRefusal asserts the handler refused the call as a tool
// result error (the caller's mistake), not as a Go error.
func requireArgumentRefusal(t *testing.T, res *mcp.CallToolResult, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("argument refusal returned a Go error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("result = %+v, want IsError", res)
	}
}

func TestVolumePoolTools_RegistersSixScopedTools(t *testing.T) {
	tools := volumePoolTools(&volumetest.Service{T: t})
	want := []string{
		"qrdb_volume_pool_list", "qrdb_volume_pool_get", "qrdb_volume_pool_create",
		"qrdb_volume_pool_update", "qrdb_volume_pool_delete", "qrdb_volume_pool_list_tenants",
	}
	if len(tools) != len(want) {
		t.Fatalf("tools = %d, want %d", len(tools), len(want))
	}
	for i, name := range want {
		if tools[i].Tool.Name != name || tools[i].Handler == nil || tools[i].Perm == "" {
			t.Fatalf("tool %d = %q perm %q handler-nil %v, want %q", i, tools[i].Tool.Name, tools[i].Perm, tools[i].Handler == nil, name)
		}
	}
}

func TestHandleVolumePoolList(t *testing.T) {
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	svc := &volumetest.Service{T: t, ListPoolsFn: func(context.Context) ([]volume.PoolView, error) {
		return []volume.PoolView{{Pool: volume.Pool{Name: "fast", Provider: "dir", CapacityBytes: 1024, CreatedAt: at}, TenantCount: 2}}, nil
	}}
	res, err := invokeTool(t, handleVolumePoolList(svc), "qrdb_volume_pool_list", nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var pools []volume.PoolView
	if err := json.Unmarshal([]byte(resultText(t, res)), &pools); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(pools) != 1 || pools[0].Name != "fast" || pools[0].CapacityBytes != 1024 || pools[0].TenantCount != 2 || !pools[0].CreatedAt.Equal(at) {
		t.Fatalf("pools = %+v", pools)
	}

	boom := &volume.PoolUnavailableError{Pool: "fast"}
	svc.ListPoolsFn = func(context.Context) ([]volume.PoolView, error) { return nil, boom }
	_, err = invokeTool(t, handleVolumePoolList(svc), "qrdb_volume_pool_list", nil)
	requireToolError(t, err, "qrdb_volume_pool_list", boom)
}

func TestHandleVolumePoolGet(t *testing.T) {
	svc := &volumetest.Service{T: t, GetPoolFn: func(_ context.Context, name string) (*volume.PoolDetail, error) {
		if name == "gone" {
			return nil, &volume.PoolNotFoundError{Pool: name}
		}
		return &volume.PoolDetail{
			PoolView:    volume.PoolView{Pool: volume.Pool{Name: name}},
			NodeReports: []volume.NodeReportRow{{NodeID: "n1", State: "ready", FreeBytes: 9}},
		}, nil
	}}
	h := handleVolumePoolGet(svc)

	res, err := invokeTool(t, h, "qrdb_volume_pool_get", map[string]any{"pool": "fast"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	var detail volume.PoolDetail
	if err := json.Unmarshal([]byte(resultText(t, res)), &detail); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if detail.Name != "fast" || len(detail.NodeReports) != 1 || detail.NodeReports[0].FreeBytes != 9 {
		t.Fatalf("detail = %+v", detail)
	}

	res, err = invokeTool(t, h, "qrdb_volume_pool_get", nil)
	requireArgumentRefusal(t, res, err)

	_, err = invokeTool(t, h, "qrdb_volume_pool_get", map[string]any{"pool": "gone"})
	var nf *volume.PoolNotFoundError
	if !errors.As(err, &nf) || nf.Pool != "gone" {
		t.Fatalf("err = %v, want PoolNotFoundError(gone)", err)
	}
}

func TestHandleVolumePoolCreate(t *testing.T) {
	var got volume.CreatePoolRequest
	svc := &volumetest.Service{T: t, CreatePoolFn: func(_ context.Context, req volume.CreatePoolRequest) (*volume.Pool, error) {
		if req.Name == "dup" {
			return nil, &volume.PoolAlreadyExistsError{Pool: req.Name}
		}
		got = req
		return &volume.Pool{Name: req.Name, Provider: "dir", CapacityBytes: req.CapacityBytes, Overcommit: req.Overcommit}, nil
	}}
	h := handleVolumePoolCreate(svc)

	res, err := invokeTool(t, h, "qrdb_volume_pool_create", map[string]any{
		"name": "fast", "provider": "dir", "description": "nvme", "capacity_bytes": float64(2048), "overcommit": 1.5,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	want := volume.CreatePoolRequest{Name: "fast", Provider: "dir", Description: "nvme", CapacityBytes: 2048, Overcommit: 1.5}
	if got != want {
		t.Fatalf("service saw %+v, want %+v", got, want)
	}
	var pool volume.Pool
	if err := json.Unmarshal([]byte(resultText(t, res)), &pool); err != nil || pool.Name != "fast" || pool.CapacityBytes != 2048 {
		t.Fatalf("pool = %+v err = %v", pool, err)
	}

	res, err = invokeTool(t, h, "qrdb_volume_pool_create", map[string]any{"provider": "dir"})
	requireArgumentRefusal(t, res, err)

	_, err = invokeTool(t, h, "qrdb_volume_pool_create", map[string]any{"name": "dup"})
	var exists *volume.PoolAlreadyExistsError
	if !errors.As(err, &exists) {
		t.Fatalf("err = %v, want PoolAlreadyExistsError", err)
	}
}

// TestHandleVolumePoolUpdate_PresenceDecidesWhatChanges proves an absent
// argument leaves the property nil (unchanged) while a present zero is
// forwarded as an explicit zero.
func TestHandleVolumePoolUpdate_PresenceDecidesWhatChanges(t *testing.T) {
	var got volume.UpdatePoolRequest
	svc := &volumetest.Service{T: t, UpdatePoolFn: func(_ context.Context, name string, req volume.UpdatePoolRequest) (*volume.Pool, error) {
		if name == "gone" {
			return nil, &volume.PoolNotFoundError{Pool: name}
		}
		got = req
		return &volume.Pool{Name: name, Overcommit: 1}, nil
	}}
	h := handleVolumePoolUpdate(svc)

	if _, err := invokeTool(t, h, "qrdb_volume_pool_update", map[string]any{"pool": "fast", "capacity_bytes": float64(0)}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.CapacityBytes == nil || *got.CapacityBytes != 0 || got.Description != nil || got.Overcommit != nil {
		t.Fatalf("capacity-only update = %+v", got)
	}

	if _, err := invokeTool(t, h, "qrdb_volume_pool_update", map[string]any{"pool": "fast", "description": "", "overcommit": 2.0}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.CapacityBytes != nil || got.Description == nil || *got.Description != "" || got.Overcommit == nil || *got.Overcommit != 2 {
		t.Fatalf("description+overcommit update = %+v", got)
	}

	res, err := invokeTool(t, h, "qrdb_volume_pool_update", map[string]any{"description": "x"})
	requireArgumentRefusal(t, res, err)

	_, err = invokeTool(t, h, "qrdb_volume_pool_update", map[string]any{"pool": "gone"})
	var nf *volume.PoolNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want PoolNotFoundError", err)
	}
}

func TestHandleVolumePoolDelete(t *testing.T) {
	var deleted []string
	svc := &volumetest.Service{T: t, DeletePoolFn: func(_ context.Context, name string) error {
		if name == "busy" {
			return &volume.PoolInUseError{Pool: name, Tenants: []string{"t1"}}
		}
		deleted = append(deleted, name)
		return nil
	}}
	h := handleVolumePoolDelete(svc)

	res, err := invokeTool(t, h, "qrdb_volume_pool_delete", map[string]any{"pool": "fast"})
	if err != nil || resultText(t, res) != "{}" {
		t.Fatalf("delete: res=%v err=%v", res, err)
	}
	res, err = invokeTool(t, h, "qrdb_volume_pool_delete", nil)
	requireArgumentRefusal(t, res, err)

	_, err = invokeTool(t, h, "qrdb_volume_pool_delete", map[string]any{"pool": "busy"})
	var inUse *volume.PoolInUseError
	if !errors.As(err, &inUse) || len(inUse.Tenants) != 1 {
		t.Fatalf("err = %v, want PoolInUseError naming t1", err)
	}
	if len(deleted) != 1 || deleted[0] != "fast" {
		t.Fatalf("deleted = %v, want [fast]", deleted)
	}
}

// TestHandleVolumePoolListTenants_WireShape proves the tool answers in the
// tenants/next_page_token shape every transport shares, not
// volume.AssignmentPage's own field names.
func TestHandleVolumePoolListTenants_WireShape(t *testing.T) {
	svc := &volumetest.Service{T: t, ListAssignmentsFn: func(_ context.Context, pool, token string, size int) (*volume.AssignmentPage, error) {
		if pool == "gone" {
			return nil, &volume.PoolNotFoundError{Pool: pool}
		}
		if pool != "fast" || token != "t1" || size != 2 {
			t.Fatalf("ListAssignments(%q, %q, %d)", pool, token, size)
		}
		return &volume.AssignmentPage{
			Assignments:   []volume.Assignment{{TenantID: "t2", Pool: "fast", Path: "t2", ReservedBytes: 10}},
			NextPageToken: "t2",
		}, nil
	}}
	h := handleVolumePoolListTenants(svc)

	res, err := invokeTool(t, h, "qrdb_volume_pool_list_tenants", map[string]any{"pool": "fast", "page_token": "t1", "page_size": float64(2)})
	if err != nil {
		t.Fatalf("list tenants: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(resultText(t, res)), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := raw["assignments"]; ok {
		t.Fatal("response leaked AssignmentPage's field name")
	}
	var page listVolumePoolTenantsResult
	if err := json.Unmarshal([]byte(resultText(t, res)), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if page.NextPageToken != "t2" || len(page.Tenants) != 1 || page.Tenants[0] != (volumePoolTenantRow{TenantID: "t2", ReservedBytes: 10}) {
		t.Fatalf("page = %+v", page)
	}

	res, err = invokeTool(t, h, "qrdb_volume_pool_list_tenants", nil)
	requireArgumentRefusal(t, res, err)

	_, err = invokeTool(t, h, "qrdb_volume_pool_list_tenants", map[string]any{"pool": "gone"})
	var nf *volume.PoolNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want PoolNotFoundError", err)
	}
}

// TestNewServer_WithVolumeServiceRegistersTools proves the option reaches the
// MCP server's tool table: the tool is callable through tools/call.
func TestNewServer_WithVolumeServiceRegistersTools(t *testing.T) {
	svc := &volumetest.Service{T: t, ListPoolsFn: func(context.Context) ([]volume.PoolView, error) {
		return []volume.PoolView{{Pool: volume.Pool{Name: "fast"}}}, nil
	}}
	s := NewServer(newMockKVService(), newMockAdminService(), ":0", WithVolumeService(svc))
	res := callToolThroughServer(t, s, "qrdb_volume_pool_list")
	if res.IsError {
		t.Fatalf("tools/call refused: %+v", res)
	}
	var pools []volume.PoolView
	if err := json.Unmarshal([]byte(resultText(t, res)), &pools); err != nil || len(pools) != 1 || pools[0].Name != "fast" {
		t.Fatalf("pools = %+v err = %v", pools, err)
	}

	without := NewServer(newMockKVService(), newMockAdminService(), ":0")
	if without.MCPServer().GetTool("qrdb_volume_pool_list") != nil {
		t.Fatal("volume pool tools registered without a volume service")
	}
}
