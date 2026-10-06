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

package embedded

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/volume"
	"github.com/jeremyhahn/go-qrdb/pkg/volume/volumetest"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// stubVolumeService is a minimal volume.Service double for ListPools/GetPool
// coverage: only the two methods these tests reach are implemented, since
// the rest panic rather than silently succeed if ever (wrongly) called.
type stubVolumeService struct {
	volume.Service
	pool   volume.PoolView
	detail volume.PoolDetail
}

func (s *stubVolumeService) ListPools(context.Context) ([]volume.PoolView, error) {
	return []volume.PoolView{s.pool}, nil
}

func (s *stubVolumeService) GetPool(_ context.Context, name string) (*volume.PoolDetail, error) {
	if name != s.detail.Name {
		return nil, &volume.PoolNotFoundError{Pool: name}
	}
	return &s.detail, nil
}

// TestListPools_ConvertsTheCatalogView pins ListPools' conversion through
// volumePoolFromCatalog, end to end: the catalog's PoolView fields must
// survive the round trip to transport.VolumePool unchanged.
func TestListPools_ConvertsTheCatalogView(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc := &stubVolumeService{pool: volume.PoolView{
		Pool: volume.Pool{
			Name: "pool-a", Provider: "local", Description: "primary",
			CapacityBytes: 1000, Overcommit: 1.5, ReservedBytes: 200,
			CreatedAt: now, UpdatedAt: now,
		},
		UsedBytes:   100,
		TenantCount: 3,
	}}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithVolumeService(svc))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	pools, err := tr.ListPools(context.Background())
	if err != nil {
		t.Fatalf("ListPools: %v", err)
	}
	if len(pools) != 1 {
		t.Fatalf("expected 1 pool, got %d", len(pools))
	}
	got := pools[0]
	if got.Name != "pool-a" || got.Provider != "local" || got.CapacityBytes != 1000 ||
		got.Overcommit != 1.5 || got.ReservedBytes != 200 || got.UsedBytes != 100 || got.TenantCount != 3 {
		t.Fatalf("ListPools conversion = %+v, want the catalog view's fields", got)
	}
}

// TestGetPool_ConvertsTheCatalogDetail pins GetPool's conversion through
// volumePoolDetailFromCatalog, including its per-node report slice.
func TestGetPool_ConvertsTheCatalogDetail(t *testing.T) {
	svc := &stubVolumeService{detail: volume.PoolDetail{
		PoolView: volume.PoolView{Pool: volume.Pool{Name: "pool-a"}},
		NodeReports: []volume.NodeReportRow{
			{NodeID: "node-1", State: "ready"},
		},
	}}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithVolumeService(svc))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	detail, err := tr.GetPool(context.Background(), "pool-a")
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	if detail.Name != "pool-a" {
		t.Fatalf("GetPool: Name = %q, want pool-a", detail.Name)
	}
	if len(detail.NodeReports) != 1 || detail.NodeReports[0].NodeID != "node-1" {
		t.Fatalf("GetPool: NodeReports = %+v, want one report for node-1", detail.NodeReports)
	}

	if _, err := tr.GetPool(context.Background(), "missing"); err == nil {
		t.Fatal("GetPool for an unknown pool: expected an error, got nil")
	}
}

// TestListPools_NilService pins the NilServiceError branch: without
// WithVolumeService, ListPools must refuse rather than panic on a nil
// t.volumeSvc.
func TestListPools_NilService(t *testing.T) {
	tr, err := New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = tr.ListPools(context.Background())
	var nse *NilServiceError
	if !errors.As(err, &nse) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestListPools_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{}, WithVolumeService(&stubVolumeService{}))
	_ = tr.Close()

	_, err := tr.ListPools(context.Background())
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestGetPool_NilService(t *testing.T) {
	tr, err := New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = tr.GetPool(context.Background(), "pool-a")
	var nse *NilServiceError
	if !errors.As(err, &nse) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestGetPool_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{}, WithVolumeService(&stubVolumeService{}))
	_ = tr.Close()

	_, err := tr.GetPool(context.Background(), "pool-a")
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// TestCreatePool_Success pins CreatePool's request/response conversion:
// the transport.CreateVolumePoolRequest's fields must reach volume.Service
// unchanged, and the returned volume.Pool must round-trip through
// volumePoolFromCatalog.
func TestCreatePool_Success(t *testing.T) {
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	var got volume.CreatePoolRequest
	svc := &volumetest.Service{CreatePoolFn: func(_ context.Context, req volume.CreatePoolRequest) (*volume.Pool, error) {
		got = req
		return &volume.Pool{Name: req.Name, Provider: req.Provider, Description: req.Description, CapacityBytes: req.CapacityBytes, Overcommit: req.Overcommit, CreatedAt: now, UpdatedAt: now}, nil
	}}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithVolumeService(svc))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	pool, err := tr.CreatePool(context.Background(), &transport.CreateVolumePoolRequest{Name: "pool-b", Provider: "local", Description: "secondary", CapacityBytes: 2000, Overcommit: 2.0})
	if err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	if got.Name != "pool-b" || got.Provider != "local" || got.Description != "secondary" || got.CapacityBytes != 2000 || got.Overcommit != 2.0 {
		t.Fatalf("service request = %+v", got)
	}
	if pool.Name != "pool-b" || pool.Provider != "local" || pool.CapacityBytes != 2000 || !pool.CreatedAt.Equal(now) {
		t.Fatalf("CreatePool conversion = %+v", pool)
	}
}

func TestCreatePool_NilService(t *testing.T) {
	tr, err := New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = tr.CreatePool(context.Background(), &transport.CreateVolumePoolRequest{Name: "pool-b"})
	var nse *NilServiceError
	if !errors.As(err, &nse) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestCreatePool_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{}, WithVolumeService(&stubVolumeService{}))
	_ = tr.Close()

	_, err := tr.CreatePool(context.Background(), &transport.CreateVolumePoolRequest{Name: "pool-b"})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestCreatePool_ServiceError(t *testing.T) {
	boom := errors.New("duplicate pool")
	svc := &volumetest.Service{CreatePoolFn: func(context.Context, volume.CreatePoolRequest) (*volume.Pool, error) { return nil, boom }}
	tr, _ := New(newMockKVService(), &mockAdminService{}, WithVolumeService(svc))

	if _, err := tr.CreatePool(context.Background(), &transport.CreateVolumePoolRequest{Name: "pool-b"}); !errors.Is(err, boom) {
		t.Fatalf("expected the service error to survive, got %v", err)
	}
}

// TestUpdatePool_Success pins UpdatePool's request/response conversion,
// including that the pointer fields (Description/CapacityBytes/Overcommit)
// reach volume.Service exactly as the caller set them.
func TestUpdatePool_Success(t *testing.T) {
	desc := "updated"
	newCapacity := int64(5000)
	overcommit := 1.25
	var gotName string
	var gotReq volume.UpdatePoolRequest
	svc := &volumetest.Service{UpdatePoolFn: func(_ context.Context, name string, req volume.UpdatePoolRequest) (*volume.Pool, error) {
		gotName, gotReq = name, req
		return &volume.Pool{Name: name, Description: *req.Description, CapacityBytes: *req.CapacityBytes, Overcommit: *req.Overcommit}, nil
	}}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithVolumeService(svc))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	pool, err := tr.UpdatePool(context.Background(), &transport.UpdateVolumePoolRequest{Name: "pool-a", Description: &desc, CapacityBytes: &newCapacity, Overcommit: &overcommit})
	if err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	if gotName != "pool-a" || gotReq.Description == nil || *gotReq.Description != desc || gotReq.CapacityBytes == nil || *gotReq.CapacityBytes != newCapacity || gotReq.Overcommit == nil || *gotReq.Overcommit != overcommit {
		t.Fatalf("service request = (%q, %+v)", gotName, gotReq)
	}
	if pool.Description != desc || pool.CapacityBytes != newCapacity || pool.Overcommit != overcommit {
		t.Fatalf("UpdatePool conversion = %+v", pool)
	}
}

func TestUpdatePool_NilService(t *testing.T) {
	tr, err := New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = tr.UpdatePool(context.Background(), &transport.UpdateVolumePoolRequest{Name: "pool-a"})
	var nse *NilServiceError
	if !errors.As(err, &nse) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestUpdatePool_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{}, WithVolumeService(&stubVolumeService{}))
	_ = tr.Close()

	_, err := tr.UpdatePool(context.Background(), &transport.UpdateVolumePoolRequest{Name: "pool-a"})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestUpdatePool_ServiceError(t *testing.T) {
	boom := errors.New("pool not found")
	svc := &volumetest.Service{UpdatePoolFn: func(context.Context, string, volume.UpdatePoolRequest) (*volume.Pool, error) { return nil, boom }}
	tr, _ := New(newMockKVService(), &mockAdminService{}, WithVolumeService(svc))

	if _, err := tr.UpdatePool(context.Background(), &transport.UpdateVolumePoolRequest{Name: "missing"}); !errors.Is(err, boom) {
		t.Fatalf("expected the service error to survive, got %v", err)
	}
}

func TestDeletePool_Success(t *testing.T) {
	var gotName string
	svc := &volumetest.Service{DeletePoolFn: func(_ context.Context, name string) error {
		gotName = name
		return nil
	}}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithVolumeService(svc))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := tr.DeletePool(context.Background(), "pool-a"); err != nil {
		t.Fatalf("DeletePool: %v", err)
	}
	if gotName != "pool-a" {
		t.Fatalf("service received %q, want pool-a", gotName)
	}
}

func TestDeletePool_NilService(t *testing.T) {
	tr, err := New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = tr.DeletePool(context.Background(), "pool-a")
	var nse *NilServiceError
	if !errors.As(err, &nse) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestDeletePool_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{}, WithVolumeService(&stubVolumeService{}))
	_ = tr.Close()

	err := tr.DeletePool(context.Background(), "pool-a")
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestDeletePool_ServiceError(t *testing.T) {
	boom := errors.New("pool has active assignments")
	svc := &volumetest.Service{DeletePoolFn: func(context.Context, string) error { return boom }}
	tr, _ := New(newMockKVService(), &mockAdminService{}, WithVolumeService(svc))

	if err := tr.DeletePool(context.Background(), "pool-a"); !errors.Is(err, boom) {
		t.Fatalf("expected the service error to survive, got %v", err)
	}
}

// TestListPoolTenants_Success pins the assignment-page conversion, including
// the next-page token.
func TestListPoolTenants_Success(t *testing.T) {
	var gotPool, gotToken string
	var gotSize int
	svc := &volumetest.Service{ListAssignmentsFn: func(_ context.Context, pool, token string, size int) (*volume.AssignmentPage, error) {
		gotPool, gotToken, gotSize = pool, token, size
		return &volume.AssignmentPage{
			Assignments:   []volume.Assignment{{TenantID: "acme", Pool: pool, ReservedBytes: 500}},
			NextPageToken: "next",
		}, nil
	}}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithVolumeService(svc))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	resp, err := tr.ListPoolTenants(context.Background(), &transport.ListVolumePoolTenantsRequest{Pool: "pool-a", PageToken: "cur", PageSize: 10})
	if err != nil {
		t.Fatalf("ListPoolTenants: %v", err)
	}
	if gotPool != "pool-a" || gotToken != "cur" || gotSize != 10 {
		t.Fatalf("service request = (%q, %q, %d)", gotPool, gotToken, gotSize)
	}
	if resp.NextPageToken != "next" || len(resp.Tenants) != 1 || resp.Tenants[0].TenantID != "acme" || resp.Tenants[0].ReservedBytes != 500 {
		t.Fatalf("ListPoolTenants conversion = %+v", resp)
	}
}

// TestListPoolTenants_NilRequest pins the nil-request default: a nil req
// must be treated as an empty one rather than panicking on req.Pool.
func TestListPoolTenants_NilRequest(t *testing.T) {
	called := false
	svc := &volumetest.Service{ListAssignmentsFn: func(_ context.Context, pool, token string, size int) (*volume.AssignmentPage, error) {
		called = true
		if pool != "" || token != "" || size != 0 {
			t.Errorf("expected zero-valued request fields, got (%q, %q, %d)", pool, token, size)
		}
		return &volume.AssignmentPage{}, nil
	}}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithVolumeService(svc))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := tr.ListPoolTenants(context.Background(), nil); err != nil {
		t.Fatalf("ListPoolTenants: %v", err)
	}
	if !called {
		t.Fatal("expected ListAssignments to be called")
	}
}

func TestListPoolTenants_NilService(t *testing.T) {
	tr, err := New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = tr.ListPoolTenants(context.Background(), &transport.ListVolumePoolTenantsRequest{Pool: "pool-a"})
	var nse *NilServiceError
	if !errors.As(err, &nse) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestListPoolTenants_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{}, WithVolumeService(&stubVolumeService{}))
	_ = tr.Close()

	_, err := tr.ListPoolTenants(context.Background(), &transport.ListVolumePoolTenantsRequest{Pool: "pool-a"})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestListPoolTenants_ServiceError(t *testing.T) {
	boom := errors.New("pool not found")
	svc := &volumetest.Service{ListAssignmentsFn: func(context.Context, string, string, int) (*volume.AssignmentPage, error) { return nil, boom }}
	tr, _ := New(newMockKVService(), &mockAdminService{}, WithVolumeService(svc))

	if _, err := tr.ListPoolTenants(context.Background(), &transport.ListVolumePoolTenantsRequest{Pool: "missing"}); !errors.Is(err, boom) {
		t.Fatalf("expected the service error to survive, got %v", err)
	}
}
