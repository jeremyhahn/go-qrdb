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
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/volume"
	"github.com/jeremyhahn/go-qrdb/pkg/volume/volumetest"
)

var volumePoolTestTime = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func newVolumePoolTestServer(svc volume.Service) *Server {
	return NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithVolumeService(svc))
}

func volumePoolRequest(method, path, pool, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if pool != "" {
		req.SetPathValue("pool", pool)
	}
	return req
}

func decodeVolumePoolBody[T any](t *testing.T, rr *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rr.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rr.Body.String(), err)
	}
	return v
}

func TestVolumePoolHandlers_NoServiceIsNotImplemented(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0")
	cases := []struct {
		name    string
		handler http.HandlerFunc
		method  string
	}{
		{"list", srv.handleListVolumePools, http.MethodGet},
		{"create", srv.handleCreateVolumePool, http.MethodPost},
		{"by-name", srv.handleVolumePoolByName, http.MethodGet},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			tc.handler(rr, volumePoolRequest(tc.method, "/v1/volume-pools/fast", "fast", "{}"))
			if rr.Code != http.StatusNotImplemented {
				t.Fatalf("status = %d, want 501: %s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestHandleListVolumePools_MapsEveryField(t *testing.T) {
	svc := &volumetest.Service{T: t, ListPoolsFn: func(context.Context) ([]volume.PoolView, error) {
		return []volume.PoolView{
			{
				Pool: volume.Pool{
					Name: "fast", Provider: "dir", Description: "nvme",
					CapacityBytes: 1 << 30, ReservedBytes: 1 << 20, Overcommit: 1.5,
					CreatedAt: volumePoolTestTime, UpdatedAt: volumePoolTestTime.Add(time.Hour),
				},
				UsedBytes: 4096, TenantCount: 3,
				NodeCounts: volume.NodeHealthCounts{Ready: 2, Locked: 1, Unavailable: 1},
			},
			{Pool: volume.Pool{Name: "bulk", Provider: "dir", Overcommit: 1}},
		}, nil
	}}
	rr := httptest.NewRecorder()
	newVolumePoolTestServer(svc).handleListVolumePools(rr, volumePoolRequest(http.MethodGet, "/v1/volume-pools", "", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	resp := decodeVolumePoolBody[listVolumePoolsResponse](t, rr)
	if len(resp.Pools) != 2 {
		t.Fatalf("pools = %d, want 2", len(resp.Pools))
	}
	got := resp.Pools[0]
	want := volumePoolResponse{
		Name: "fast", Provider: "dir", Description: "nvme",
		CapacityBytes: 1 << 30, ReservedBytes: 1 << 20, UsedBytes: 4096, TenantCount: 3,
		Nodes:      volumePoolNodeCounts{Ready: 2, Locked: 1, Unavailable: 1},
		Overcommit: 1.5,
		CreatedAt:  "2026-09-01T12:00:00Z", UpdatedAt: "2026-09-01T13:00:00Z",
	}
	if got != want {
		t.Fatalf("pool[0] = %+v, want %+v", got, want)
	}
	if resp.Pools[1].CreatedAt != "" || resp.Pools[1].UpdatedAt != "" {
		t.Fatalf("zero timestamps must be omitted, got %+v", resp.Pools[1])
	}
}

func TestHandleListVolumePools_ServiceErrorMapsStatus(t *testing.T) {
	svc := &volumetest.Service{T: t, ListPoolsFn: func(context.Context) ([]volume.PoolView, error) {
		return nil, &volume.PoolUnavailableError{Pool: "fast"}
	}}
	rr := httptest.NewRecorder()
	newVolumePoolTestServer(svc).handleListVolumePools(rr, volumePoolRequest(http.MethodGet, "/v1/volume-pools", "", ""))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
}

func TestHandleCreateVolumePool_PassesRequestAndReturns201(t *testing.T) {
	var got volume.CreatePoolRequest
	svc := &volumetest.Service{T: t, CreatePoolFn: func(_ context.Context, req volume.CreatePoolRequest) (*volume.Pool, error) {
		got = req
		return &volume.Pool{Name: req.Name, Provider: "dir", CapacityBytes: req.CapacityBytes, Overcommit: 2, CreatedAt: volumePoolTestTime}, nil
	}}
	body := `{"name":"fast","provider":"dir","description":"nvme","capacityBytes":1024,"overcommit":2}`
	rr := httptest.NewRecorder()
	newVolumePoolTestServer(svc).handleCreateVolumePool(rr, volumePoolRequest(http.MethodPost, "/v1/volume-pools", "", body))
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rr.Code, rr.Body.String())
	}
	want := volume.CreatePoolRequest{Name: "fast", Provider: "dir", Description: "nvme", CapacityBytes: 1024, Overcommit: 2}
	if got != want {
		t.Fatalf("service saw %+v, want %+v", got, want)
	}
	resp := decodeVolumePoolBody[volumePoolResponse](t, rr)
	if resp.Name != "fast" || resp.CapacityBytes != 1024 || resp.CreatedAt != "2026-09-01T12:00:00Z" {
		t.Fatalf("response = %+v", resp)
	}
}

func TestHandleCreateVolumePool_RejectsBadBodyAndServiceErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
		err  error
		want int
	}{
		{"malformed json", `{"name":`, nil, http.StatusBadRequest},
		{"already exists", `{"name":"fast"}`, &volume.PoolAlreadyExistsError{Pool: "fast"}, http.StatusConflict},
		{"invalid request", `{"name":""}`, &volume.InvalidPoolRequestError{Reason: "empty name"}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &volumetest.Service{T: t, CreatePoolFn: func(context.Context, volume.CreatePoolRequest) (*volume.Pool, error) {
				return nil, tc.err
			}}
			rr := httptest.NewRecorder()
			newVolumePoolTestServer(svc).handleCreateVolumePool(rr, volumePoolRequest(http.MethodPost, "/v1/volume-pools", "", tc.body))
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

func TestHandleVolumePoolByName_GetRendersNodeReports(t *testing.T) {
	svc := &volumetest.Service{T: t, GetPoolFn: func(_ context.Context, name string) (*volume.PoolDetail, error) {
		if name != "fast" {
			t.Fatalf("GetPool(%q), want fast", name)
		}
		return &volume.PoolDetail{
			PoolView: volume.PoolView{Pool: volume.Pool{Name: "fast", Provider: "dir"}, TenantCount: 1},
			NodeReports: []volume.NodeReportRow{
				{NodeID: "n1", State: "ready", SizeBytes: 100, FreeBytes: 60, Detail: "ok", ReportedAt: volumePoolTestTime},
				// A stale row must not leak its last-known numbers.
				{NodeID: "n2", State: volume.NodeStateNoReport, SizeBytes: 100, FreeBytes: 10, Detail: "stale"},
			},
		}, nil
	}}
	rr := httptest.NewRecorder()
	newVolumePoolTestServer(svc).handleVolumePoolByName(rr, volumePoolRequest(http.MethodGet, "/v1/volume-pools/fast", "fast", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	resp := decodeVolumePoolBody[volumePoolDetailResponse](t, rr)
	if resp.Name != "fast" || resp.TenantCount != 1 || len(resp.NodeReports) != 2 {
		t.Fatalf("response = %+v", resp)
	}
	want0 := volumePoolNodeReportResponse{NodeID: "n1", State: "ready", SizeBytes: 100, FreeBytes: 60, Detail: "ok", ReportedAt: "2026-09-01T12:00:00Z"}
	if resp.NodeReports[0] != want0 {
		t.Fatalf("row 0 = %+v, want %+v", resp.NodeReports[0], want0)
	}
	want1 := volumePoolNodeReportResponse{NodeID: "n2", State: volume.NodeStateNoReport}
	if resp.NodeReports[1] != want1 {
		t.Fatalf("no_report row = %+v, want %+v (stale numbers zeroed)", resp.NodeReports[1], want1)
	}
}

func TestHandleVolumePoolByName_GetNotFound(t *testing.T) {
	svc := &volumetest.Service{T: t, GetPoolFn: func(_ context.Context, name string) (*volume.PoolDetail, error) {
		return nil, &volume.PoolNotFoundError{Pool: name}
	}}
	rr := httptest.NewRecorder()
	newVolumePoolTestServer(svc).handleVolumePoolByName(rr, volumePoolRequest(http.MethodGet, "/v1/volume-pools/nope", "nope", ""))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleVolumePoolByName_MissingPoolAndBadMethod(t *testing.T) {
	srv := newVolumePoolTestServer(&volumetest.Service{T: t})

	rr := httptest.NewRecorder()
	srv.handleVolumePoolByName(rr, volumePoolRequest(http.MethodGet, "/v1/volume-pools/", "", ""))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("missing pool: status = %d, want 400", rr.Code)
	}

	rr = httptest.NewRecorder()
	srv.handleVolumePoolByName(rr, volumePoolRequest(http.MethodPatch, "/v1/volume-pools/fast", "fast", ""))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PATCH: status = %d, want 405", rr.Code)
	}
}

func TestHandleVolumePoolByName_UpdateSendsOnlySetFields(t *testing.T) {
	var got volume.UpdatePoolRequest
	svc := &volumetest.Service{T: t, UpdatePoolFn: func(_ context.Context, name string, req volume.UpdatePoolRequest) (*volume.Pool, error) {
		got = req
		return &volume.Pool{Name: name, Provider: "dir", CapacityBytes: *req.CapacityBytes, Overcommit: 1}, nil
	}}
	rr := httptest.NewRecorder()
	newVolumePoolTestServer(svc).handleVolumePoolByName(rr, volumePoolRequest(http.MethodPut, "/v1/volume-pools/fast", "fast", `{"capacityBytes":0}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	if got.CapacityBytes == nil || *got.CapacityBytes != 0 {
		t.Fatalf("explicit zero capacity must reach the service as non-nil 0, got %v", got.CapacityBytes)
	}
	if got.Description != nil || got.Overcommit != nil {
		t.Fatalf("absent fields must stay nil, got %+v", got)
	}
	if resp := decodeVolumePoolBody[volumePoolResponse](t, rr); resp.Name != "fast" {
		t.Fatalf("response = %+v", resp)
	}
}

func TestHandleVolumePoolByName_UpdateErrors(t *testing.T) {
	svc := &volumetest.Service{T: t, UpdatePoolFn: func(_ context.Context, name string, _ volume.UpdatePoolRequest) (*volume.Pool, error) {
		return nil, &volume.PoolNotFoundError{Pool: name}
	}}
	srv := newVolumePoolTestServer(svc)

	rr := httptest.NewRecorder()
	srv.handleVolumePoolByName(rr, volumePoolRequest(http.MethodPut, "/v1/volume-pools/fast", "fast", `not json`))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad body: status = %d, want 400", rr.Code)
	}

	rr = httptest.NewRecorder()
	srv.handleVolumePoolByName(rr, volumePoolRequest(http.MethodPut, "/v1/volume-pools/gone", "gone", `{}`))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("missing pool: status = %d, want 404", rr.Code)
	}
}

func TestHandleVolumePoolByName_Delete(t *testing.T) {
	var deleted string
	svc := &volumetest.Service{T: t, DeletePoolFn: func(_ context.Context, name string) error {
		if name == "busy" {
			return &volume.PoolInUseError{Pool: name, Tenants: []string{"t1"}}
		}
		deleted = name
		return nil
	}}
	srv := newVolumePoolTestServer(svc)

	rr := httptest.NewRecorder()
	srv.handleVolumePoolByName(rr, volumePoolRequest(http.MethodDelete, "/v1/volume-pools/fast", "fast", ""))
	if rr.Code != http.StatusNoContent || deleted != "fast" {
		t.Fatalf("status = %d deleted = %q, want 204 fast", rr.Code, deleted)
	}

	rr = httptest.NewRecorder()
	srv.handleVolumePoolByName(rr, volumePoolRequest(http.MethodDelete, "/v1/volume-pools/busy", "busy", ""))
	if rr.Code != http.StatusConflict {
		t.Fatalf("pool in use: status = %d, want 409", rr.Code)
	}
}

func TestHandleVolumePoolByName_ListTenantsPaginates(t *testing.T) {
	svc := &volumetest.Service{T: t, ListAssignmentsFn: func(_ context.Context, pool, token string, size int) (*volume.AssignmentPage, error) {
		if pool != "fast" || token != "t1" || size != 2 {
			t.Fatalf("ListAssignments(%q, %q, %d)", pool, token, size)
		}
		return &volume.AssignmentPage{
			Assignments:   []volume.Assignment{{TenantID: "t2", Pool: "fast", ReservedBytes: 10}, {TenantID: "t3", Pool: "fast", ReservedBytes: 20}},
			NextPageToken: "t3",
		}, nil
	}}
	rr := httptest.NewRecorder()
	newVolumePoolTestServer(svc).handleVolumePoolByName(rr,
		volumePoolRequest(http.MethodGet, "/v1/volume-pools/fast/tenants?page_size=2&page_token=t1", "fast", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	resp := decodeVolumePoolBody[listVolumePoolTenantsResponse](t, rr)
	if resp.NextPageToken != "t3" || len(resp.Tenants) != 2 ||
		resp.Tenants[0] != (volumePoolTenantResponse{TenantID: "t2", ReservedBytes: 10}) {
		t.Fatalf("response = %+v", resp)
	}
}

func TestHandleVolumePoolByName_ListTenantsErrors(t *testing.T) {
	svc := &volumetest.Service{T: t, ListAssignmentsFn: func(_ context.Context, pool, _ string, _ int) (*volume.AssignmentPage, error) {
		return nil, &volume.PoolNotFoundError{Pool: pool}
	}}
	srv := newVolumePoolTestServer(svc)

	rr := httptest.NewRecorder()
	srv.handleVolumePoolByName(rr, volumePoolRequest(http.MethodGet, "/v1/volume-pools/fast/tenants?page_size=many", "fast", ""))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("non-integer page_size: status = %d, want 400", rr.Code)
	}

	rr = httptest.NewRecorder()
	srv.handleVolumePoolByName(rr, volumePoolRequest(http.MethodGet, "/v1/volume-pools/gone/tenants", "gone", ""))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("missing pool: status = %d, want 404", rr.Code)
	}
}
