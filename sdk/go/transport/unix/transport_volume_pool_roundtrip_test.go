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

package unix

import (
	"context"
	"errors"
	"testing"
	"time"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
	"github.com/jeremyhahn/go-qrdb/pkg/volume/volumetest"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func requireUnixRequestError(t *testing.T, err error, method string) {
	t.Helper()
	var re *transport.RequestError
	if !errors.As(err, &re) || re.Method != method {
		t.Fatalf("%s: err = %v, want *transport.RequestError for %s", method, err, method)
	}
}

func TestUnixRT_VolumePool_ListAndGet(t *testing.T) {
	created := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	updated := created.Add(time.Hour)
	reported := created.Add(2 * time.Hour)
	svc := &volumetest.Service{T: t,
		ListPoolsFn: func(context.Context) ([]volume.PoolView, error) {
			return []volume.PoolView{{
				Pool: volume.Pool{Name: "fast", Provider: "dir", CapacityBytes: 100, Overcommit: 1.5,
					ReservedBytes: 40, CreatedAt: created, UpdatedAt: updated},
				UsedBytes:   7,
				TenantCount: 2,
				NodeCounts:  volume.NodeHealthCounts{Ready: 3, Locked: 1, Unavailable: 1, Unmapped: 2},
			}}, nil
		},
		GetPoolFn: func(_ context.Context, name string) (*volume.PoolDetail, error) {
			if name != "fast" {
				return nil, &volume.PoolNotFoundError{Pool: name}
			}
			return &volume.PoolDetail{
				PoolView: volume.PoolView{Pool: volume.Pool{Name: "fast", Provider: "dir"}},
				NodeReports: []volume.NodeReportRow{
					{NodeID: "n1", State: "ready", SizeBytes: 50, FreeBytes: 20, Detail: "ok", ReportedAt: reported},
					{NodeID: "n2", State: "no_report"},
				},
			}, nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithVolumeService(svc))
	defer cleanup()
	ctx := context.Background()

	pools, err := tr.ListPools(ctx)
	if err != nil {
		t.Fatalf("ListPools: %v", err)
	}
	if len(pools) != 1 {
		t.Fatalf("ListPools = %+v", pools)
	}
	p := pools[0]
	if p.Name != "fast" || p.Provider != "dir" || p.CapacityBytes != 100 || p.Overcommit != 1.5 ||
		p.ReservedBytes != 40 || p.UsedBytes != 7 || p.TenantCount != 2 {
		t.Fatalf("ListPools = %+v", p)
	}
	if p.Nodes != (transport.VolumePoolNodeCounts{Ready: 3, Locked: 1, Unavailable: 1, Unmapped: 2}) {
		t.Fatalf("node counts = %+v", p.Nodes)
	}
	if !p.CreatedAt.Equal(created) || !p.UpdatedAt.Equal(updated) {
		t.Fatalf("timestamps = %v / %v, want %v / %v", p.CreatedAt, p.UpdatedAt, created, updated)
	}

	detail, err := tr.GetPool(ctx, "fast")
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	if detail.Name != "fast" || len(detail.NodeReports) != 2 {
		t.Fatalf("GetPool = %+v", detail)
	}
	r := detail.NodeReports[0]
	if r.NodeID != "n1" || r.State != "ready" || r.SizeBytes != 50 || r.FreeBytes != 20 || r.Detail != "ok" ||
		!r.ReportedAt.Equal(reported) {
		t.Fatalf("node report = %+v", r)
	}
	if !detail.NodeReports[1].ReportedAt.IsZero() {
		t.Fatalf("a row with no report carries a time: %+v", detail.NodeReports[1])
	}

	var nf *transport.KeyNotFoundError
	if _, err := tr.GetPool(ctx, "gone"); !errors.As(err, &nf) {
		t.Fatalf("GetPool(gone): err = %v, want *transport.KeyNotFoundError", err)
	}
}

func TestUnixRT_VolumePool_CreateAndUpdate(t *testing.T) {
	var gotCreate volume.CreatePoolRequest
	var gotName string
	var gotUpdate volume.UpdatePoolRequest
	svc := &volumetest.Service{T: t,
		CreatePoolFn: func(_ context.Context, req volume.CreatePoolRequest) (*volume.Pool, error) {
			if req.Name == "dup" {
				return nil, &volume.PoolAlreadyExistsError{Pool: req.Name}
			}
			gotCreate = req
			return &volume.Pool{Name: req.Name, Provider: req.Provider, Description: req.Description,
				CapacityBytes: req.CapacityBytes, Overcommit: req.Overcommit}, nil
		},
		UpdatePoolFn: func(_ context.Context, name string, req volume.UpdatePoolRequest) (*volume.Pool, error) {
			gotName, gotUpdate = name, req
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
	tr, cleanup := startAuthServer(t, unixserver.WithVolumeService(svc))
	defer cleanup()
	ctx := context.Background()

	pool, err := tr.CreatePool(ctx, &transport.CreateVolumePoolRequest{
		Name: "fast", Provider: "dir", Description: "ssd", CapacityBytes: 1 << 20, Overcommit: 2,
	})
	if err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	want := volume.CreatePoolRequest{Name: "fast", Provider: "dir", Description: "ssd", CapacityBytes: 1 << 20, Overcommit: 2}
	if gotCreate != want {
		t.Fatalf("service got %+v, want %+v", gotCreate, want)
	}
	if pool.Name != "fast" || pool.Description != "ssd" || pool.CapacityBytes != 1<<20 || pool.Overcommit != 2 {
		t.Fatalf("CreatePool = %+v", pool)
	}
	if _, err := tr.CreatePool(ctx, &transport.CreateVolumePoolRequest{Name: "dup"}); err == nil {
		t.Fatal("CreatePool(dup) succeeded")
	}

	desc, capacity, over := "new", int64(0), 3.0
	pool, err = tr.UpdatePool(ctx, &transport.UpdateVolumePoolRequest{
		Name: "fast", Description: &desc, CapacityBytes: &capacity, Overcommit: &over,
	})
	if err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	if gotName != "fast" || gotUpdate.Description == nil || *gotUpdate.Description != "new" ||
		gotUpdate.CapacityBytes == nil || *gotUpdate.CapacityBytes != 0 ||
		gotUpdate.Overcommit == nil || *gotUpdate.Overcommit != 3 {
		t.Fatalf("service got %q %+v", gotName, gotUpdate)
	}
	if pool.Description != "new" || pool.Overcommit != 3 {
		t.Fatalf("UpdatePool = %+v", pool)
	}

	// A field the caller left nil stays absent on the wire: an explicit
	// zero capacity and an unset one are different updates.
	if _, err := tr.UpdatePool(ctx, &transport.UpdateVolumePoolRequest{Name: "fast", Description: &desc}); err != nil {
		t.Fatalf("UpdatePool(description): %v", err)
	}
	if gotUpdate.CapacityBytes != nil || gotUpdate.Overcommit != nil {
		t.Fatalf("absent fields reached the service as set: %+v", gotUpdate)
	}
}

func TestUnixRT_VolumePool_DeleteAndListTenants(t *testing.T) {
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
			if pool == "broken" {
				return nil, &volume.PoolInUseError{Pool: pool}
			}
			if pool == "" {
				return &volume.AssignmentPage{}, nil
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
	tr, cleanup := startAuthServer(t, unixserver.WithVolumeService(svc))
	defer cleanup()
	ctx := context.Background()

	if err := tr.DeletePool(ctx, "fast"); err != nil {
		t.Fatalf("DeletePool: %v", err)
	}
	if deleted != "fast" {
		t.Fatalf("deleted = %q", deleted)
	}
	requireUnixRequestError(t, tr.DeletePool(ctx, "busy"), "DeletePool")

	page, err := tr.ListPoolTenants(ctx, &transport.ListVolumePoolTenantsRequest{Pool: "fast", PageToken: "t1", PageSize: 2})
	if err != nil {
		t.Fatalf("ListPoolTenants: %v", err)
	}
	if page.NextPageToken != "t2" || len(page.Tenants) != 1 ||
		page.Tenants[0] != (transport.VolumePoolTenant{TenantID: "t2", ReservedBytes: 10}) {
		t.Fatalf("ListPoolTenants = %+v", page)
	}

	// A nil request is the zero request, not a panic.
	page, err = tr.ListPoolTenants(ctx, nil)
	if err != nil || len(page.Tenants) != 0 {
		t.Fatalf("ListPoolTenants(nil) = %+v, %v", page, err)
	}
	_, err = tr.ListPoolTenants(ctx, &transport.ListVolumePoolTenantsRequest{Pool: "broken"})
	requireUnixRequestError(t, err, "ListPoolTenants")
}

func TestUnixRT_VolumePool_UnconfiguredAndClosed(t *testing.T) {
	tr, cleanup := startAuthServer(t)
	defer cleanup()
	ctx := context.Background()

	if _, err := tr.ListPools(ctx); err == nil {
		t.Fatal("ListPools succeeded against a server with no volume service")
	}
	if _, err := tr.GetPool(ctx, "fast"); err == nil {
		t.Fatal("GetPool succeeded against a server with no volume service")
	}
	if _, err := tr.CreatePool(ctx, &transport.CreateVolumePoolRequest{Name: "x"}); err == nil {
		t.Fatal("CreatePool succeeded against a server with no volume service")
	}
	if _, err := tr.UpdatePool(ctx, &transport.UpdateVolumePoolRequest{Name: "x"}); err == nil {
		t.Fatal("UpdatePool succeeded against a server with no volume service")
	}

	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err := tr.ListPools(ctx)
	assertClosed(t, err)
	_, err = tr.GetPool(ctx, "fast")
	assertClosed(t, err)
	_, err = tr.CreatePool(ctx, &transport.CreateVolumePoolRequest{Name: "x"})
	assertClosed(t, err)
	_, err = tr.UpdatePool(ctx, &transport.UpdateVolumePoolRequest{Name: "x"})
	assertClosed(t, err)
	assertClosed(t, tr.DeletePool(ctx, "x"))
	_, err = tr.ListPoolTenants(ctx, nil)
	assertClosed(t, err)
}
