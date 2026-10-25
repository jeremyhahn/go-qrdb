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
	"errors"
	"testing"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
	"github.com/jeremyhahn/go-qrdb/pkg/volume/volumetest"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// connectServerInProcess connects a Transport to the real MCP server built
// with opts over the mock KV and admin services, in process.
func connectServerInProcess(t *testing.T, opts ...mcpapi.ServerOption) *Transport {
	t.Helper()
	srv := mcpapi.NewServer(newMockKVService(), newMockAdminService(), ":0", opts...)
	tr, err := NewInProcess(srv.MCPServer())
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

// requireRequestError fails the test unless err is a *transport.RequestError
// for method.
func requireRequestError(t *testing.T, err error, method string) {
	t.Helper()
	var re *transport.RequestError
	if !errors.As(err, &re) {
		t.Fatalf("err = %T %v, want *transport.RequestError", err, err)
	}
	if re.Method != method {
		t.Fatalf("RequestError.Method = %q, want %q", re.Method, method)
	}
}

// requireClosed fails the test unless err is a *transport.ClosedError.
func requireClosed(t *testing.T, err error) {
	t.Helper()
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %T %v, want *transport.ClosedError", err, err)
	}
}

func TestMCP_VolumePool_ListAndGet(t *testing.T) {
	svc := &volumetest.Service{T: t,
		ListPoolsFn: func(context.Context) ([]volume.PoolView, error) {
			return []volume.PoolView{{
				Pool:        volume.Pool{Name: "fast", Provider: "dir", CapacityBytes: 100, Overcommit: 1.5},
				UsedBytes:   7,
				TenantCount: 2,
			}}, nil
		},
		GetPoolFn: func(_ context.Context, name string) (*volume.PoolDetail, error) {
			if name != "fast" {
				return nil, &volume.PoolNotFoundError{Pool: name}
			}
			return &volume.PoolDetail{
				PoolView:    volume.PoolView{Pool: volume.Pool{Name: "fast", Provider: "dir"}},
				NodeReports: []volume.NodeReportRow{{NodeID: "n1", State: "ready"}},
			}, nil
		},
	}
	tr := connectServerInProcess(t, mcpapi.WithVolumeService(svc))
	ctx := context.Background()

	pools, err := tr.ListPools(ctx)
	if err != nil {
		t.Fatalf("ListPools: %v", err)
	}
	if len(pools) != 1 || pools[0].Name != "fast" || pools[0].CapacityBytes != 100 ||
		pools[0].Overcommit != 1.5 || pools[0].UsedBytes != 7 || pools[0].TenantCount != 2 {
		t.Fatalf("ListPools = %+v", pools)
	}

	detail, err := tr.GetPool(ctx, "fast")
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	if detail.Name != "fast" || len(detail.NodeReports) != 1 || detail.NodeReports[0].NodeID != "n1" {
		t.Fatalf("GetPool = %+v", detail)
	}

	_, err = tr.GetPool(ctx, "gone")
	requireRequestError(t, err, "GetPool")
}

func TestMCP_VolumePool_CreateSendsOnlySetFields(t *testing.T) {
	var got volume.CreatePoolRequest
	svc := &volumetest.Service{T: t,
		CreatePoolFn: func(_ context.Context, req volume.CreatePoolRequest) (*volume.Pool, error) {
			if req.Name == "dup" {
				return nil, &volume.PoolAlreadyExistsError{Pool: req.Name}
			}
			got = req
			return &volume.Pool{Name: req.Name, Provider: "dir", Description: req.Description,
				CapacityBytes: req.CapacityBytes, Overcommit: req.Overcommit}, nil
		},
	}
	tr := connectServerInProcess(t, mcpapi.WithVolumeService(svc))
	ctx := context.Background()

	pool, err := tr.CreatePool(ctx, &transport.CreateVolumePoolRequest{
		Name: "fast", Provider: "dir", Description: "ssd", CapacityBytes: 1 << 20, Overcommit: 2,
	})
	if err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	want := volume.CreatePoolRequest{Name: "fast", Provider: "dir", Description: "ssd", CapacityBytes: 1 << 20, Overcommit: 2}
	if got != want {
		t.Fatalf("service got %+v, want %+v", got, want)
	}
	if pool.Name != "fast" || pool.Description != "ssd" || pool.CapacityBytes != 1<<20 || pool.Overcommit != 2 {
		t.Fatalf("CreatePool = %+v", pool)
	}

	if _, err := tr.CreatePool(ctx, &transport.CreateVolumePoolRequest{Name: "min"}); err != nil {
		t.Fatalf("CreatePool(min): %v", err)
	}
	if got != (volume.CreatePoolRequest{Name: "min"}) {
		t.Fatalf("zero fields reached the service: %+v", got)
	}

	_, err = tr.CreatePool(ctx, &transport.CreateVolumePoolRequest{Name: "dup"})
	requireRequestError(t, err, "CreatePool")
}

func TestMCP_VolumePool_UpdatePresenceDecidesWhatChanges(t *testing.T) {
	var got volume.UpdatePoolRequest
	svc := &volumetest.Service{T: t,
		UpdatePoolFn: func(_ context.Context, name string, req volume.UpdatePoolRequest) (*volume.Pool, error) {
			if name == "gone" {
				return nil, &volume.PoolNotFoundError{Pool: name}
			}
			got = req
			p := &volume.Pool{Name: name, Provider: "dir", Overcommit: 1}
			if req.Description != nil {
				p.Description = *req.Description
			}
			if req.CapacityBytes != nil {
				p.CapacityBytes = *req.CapacityBytes
			}
			if req.Overcommit != nil {
				p.Overcommit = *req.Overcommit
			}
			return p, nil
		},
	}
	tr := connectServerInProcess(t, mcpapi.WithVolumeService(svc))
	ctx := context.Background()

	desc, capacity, over := "new", int64(0), 3.0
	pool, err := tr.UpdatePool(ctx, &transport.UpdateVolumePoolRequest{
		Name: "fast", Description: &desc, CapacityBytes: &capacity, Overcommit: &over,
	})
	if err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	if got.Description == nil || *got.Description != "new" || got.CapacityBytes == nil || *got.CapacityBytes != 0 ||
		got.Overcommit == nil || *got.Overcommit != 3 {
		t.Fatalf("service got %+v", got)
	}
	if pool.Description != "new" || pool.Overcommit != 3 {
		t.Fatalf("UpdatePool = %+v", pool)
	}

	if _, err := tr.UpdatePool(ctx, &transport.UpdateVolumePoolRequest{Name: "fast", Description: &desc}); err != nil {
		t.Fatalf("UpdatePool(description): %v", err)
	}
	if got.CapacityBytes != nil || got.Overcommit != nil {
		t.Fatalf("absent fields reached the service as set: %+v", got)
	}

	_, err = tr.UpdatePool(ctx, &transport.UpdateVolumePoolRequest{Name: "gone", Description: &desc})
	requireRequestError(t, err, "UpdatePool")
}

func TestMCP_VolumePool_DeleteAndListTenants(t *testing.T) {
	var deleted string
	svc := &volumetest.Service{T: t,
		DeletePoolFn: func(_ context.Context, name string) error {
			if name == "busy" {
				return &volume.PoolInUseError{Pool: name, Tenants: []string{"t1"}}
			}
			deleted = name
			return nil
		},
		ListAssignmentsFn: func(_ context.Context, pool, token string, size int) (*volume.AssignmentPage, error) {
			if pool == "gone" {
				return nil, &volume.PoolNotFoundError{Pool: pool}
			}
			if pool != "fast" || token != "t1" || size != 2 {
				t.Errorf("ListAssignments(%q, %q, %d)", pool, token, size)
			}
			return &volume.AssignmentPage{
				Assignments:   []volume.Assignment{{TenantID: "t2", Pool: "fast", Path: "t2", ReservedBytes: 10}},
				NextPageToken: "t2",
			}, nil
		},
	}
	tr := connectServerInProcess(t, mcpapi.WithVolumeService(svc))
	ctx := context.Background()

	if err := tr.DeletePool(ctx, "fast"); err != nil {
		t.Fatalf("DeletePool: %v", err)
	}
	if deleted != "fast" {
		t.Fatalf("deleted = %q", deleted)
	}
	requireRequestError(t, tr.DeletePool(ctx, "busy"), "DeletePool")

	page, err := tr.ListPoolTenants(ctx, &transport.ListVolumePoolTenantsRequest{Pool: "fast", PageToken: "t1", PageSize: 2})
	if err != nil {
		t.Fatalf("ListPoolTenants: %v", err)
	}
	if page.NextPageToken != "t2" || len(page.Tenants) != 1 ||
		page.Tenants[0] != (transport.VolumePoolTenant{TenantID: "t2", ReservedBytes: 10}) {
		t.Fatalf("ListPoolTenants = %+v", page)
	}

	_, err = tr.ListPoolTenants(ctx, nil)
	requireRequestError(t, err, "ListPoolTenants")
	_, err = tr.ListPoolTenants(ctx, &transport.ListVolumePoolTenantsRequest{Pool: "gone"})
	requireRequestError(t, err, "ListPoolTenants")
}

func TestMCP_VolumePool_ToolsAbsentAndClosed(t *testing.T) {
	tr := connectServerInProcess(t)
	ctx := context.Background()

	if _, err := tr.ListPools(ctx); err == nil {
		t.Fatal("ListPools succeeded against a server with no volume service")
	}

	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err := tr.ListPools(ctx)
	requireClosed(t, err)
	_, err = tr.GetPool(ctx, "fast")
	requireClosed(t, err)
	_, err = tr.CreatePool(ctx, &transport.CreateVolumePoolRequest{Name: "x"})
	requireClosed(t, err)
	_, err = tr.UpdatePool(ctx, &transport.UpdateVolumePoolRequest{Name: "x"})
	requireClosed(t, err)
	requireClosed(t, tr.DeletePool(ctx, "x"))
	_, err = tr.ListPoolTenants(ctx, nil)
	requireClosed(t, err)
}
