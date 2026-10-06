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

package grpc

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
	"github.com/jeremyhahn/go-qrdb/pkg/volume/volumetest"
)

var volumePoolTestTime = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func TestVolumePoolRPCs_NoServiceIsUnimplemented(t *testing.T) {
	s := &qrdbServiceServer{}
	ctx := context.Background()
	calls := map[string]func() error{
		"list":   func() error { _, err := s.VolumePoolList(ctx, &emptypb.Empty{}); return err },
		"get":    func() error { _, err := s.VolumePoolGet(ctx, &pb.VolumePoolGetRequest{Name: "p"}); return err },
		"create": func() error { _, err := s.VolumePoolCreate(ctx, &pb.VolumePoolCreateRequest{Name: "p"}); return err },
		"update": func() error { _, err := s.VolumePoolUpdate(ctx, &pb.VolumePoolUpdateRequest{Name: "p"}); return err },
		"delete": func() error { _, err := s.VolumePoolDelete(ctx, &pb.VolumePoolDeleteRequest{Name: "p"}); return err },
		"tenants": func() error {
			_, err := s.VolumePoolListTenants(ctx, &pb.VolumePoolListTenantsRequest{Pool: "p"})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) { requireCode(t, call(), codes.Unimplemented) })
	}
}

func TestVolumePoolList_MapsEveryField(t *testing.T) {
	s := &qrdbServiceServer{volumeSvc: &volumetest.Service{T: t, ListPoolsFn: func(context.Context) ([]volume.PoolView, error) {
		return []volume.PoolView{
			{
				Pool: volume.Pool{
					Name: "fast", Provider: "dir", Description: "nvme",
					CapacityBytes: 1 << 30, ReservedBytes: 1 << 20, Overcommit: 1.5,
					CreatedAt: volumePoolTestTime, UpdatedAt: volumePoolTestTime.Add(time.Hour),
				},
				UsedBytes: 4096, TenantCount: 3,
				NodeCounts: volume.NodeHealthCounts{Ready: 2, Locked: 1, Unavailable: 1, Unmapped: 4},
			},
			{Pool: volume.Pool{Name: "bulk", Provider: "dir", Overcommit: 1}},
		}, nil
	}}}
	resp, err := s.VolumePoolList(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("VolumePoolList: %v", err)
	}
	if len(resp.GetPools()) != 2 {
		t.Fatalf("pools = %d, want 2", len(resp.GetPools()))
	}
	p := resp.GetPools()[0]
	if p.GetName() != "fast" || p.GetProvider() != "dir" || p.GetDescription() != "nvme" ||
		p.GetCapacityBytes() != 1<<30 || p.GetReservedBytes() != 1<<20 || p.GetUsedBytes() != 4096 ||
		p.GetTenantCount() != 3 || p.GetOvercommit() != 1.5 {
		t.Fatalf("pool = %+v", p)
	}
	n := p.GetNodes()
	if n.GetReady() != 2 || n.GetLocked() != 1 || n.GetUnavailable() != 1 || n.GetUnmapped() != 4 {
		t.Fatalf("node counts = %+v", n)
	}
	if p.GetCreatedAt() != "2026-09-01T12:00:00Z" || p.GetUpdatedAt() != "2026-09-01T13:00:00Z" {
		t.Fatalf("timestamps = %q %q", p.GetCreatedAt(), p.GetUpdatedAt())
	}
	if b := resp.GetPools()[1]; b.GetCreatedAt() != "" || b.GetUpdatedAt() != "" {
		t.Fatalf("zero timestamps must stay empty, got %q %q", b.GetCreatedAt(), b.GetUpdatedAt())
	}
}

func TestVolumePoolGet_NodeReportsHideStaleNumbers(t *testing.T) {
	s := &qrdbServiceServer{volumeSvc: &volumetest.Service{T: t, GetPoolFn: func(_ context.Context, name string) (*volume.PoolDetail, error) {
		if name == "gone" {
			return nil, &volume.PoolNotFoundError{Pool: name}
		}
		return &volume.PoolDetail{
			PoolView: volume.PoolView{Pool: volume.Pool{Name: name, Provider: "dir"}},
			NodeReports: []volume.NodeReportRow{
				{NodeID: "n1", State: "ready", SizeBytes: 100, FreeBytes: 60, Detail: "ok", ReportedAt: volumePoolTestTime},
				{NodeID: "n2", State: volume.NodeStateNoReport, SizeBytes: 100, FreeBytes: 10, Detail: "stale"},
			},
		}, nil
	}}}
	resp, err := s.VolumePoolGet(context.Background(), &pb.VolumePoolGetRequest{Name: "fast"})
	if err != nil {
		t.Fatalf("VolumePoolGet: %v", err)
	}
	if resp.GetPool().GetName() != "fast" || len(resp.GetNodeReports()) != 2 {
		t.Fatalf("detail = %+v", resp)
	}
	r0, r1 := resp.GetNodeReports()[0], resp.GetNodeReports()[1]
	if r0.GetNodeId() != "n1" || r0.GetSizeBytes() != 100 || r0.GetFreeBytes() != 60 || r0.GetDetail() != "ok" ||
		r0.GetReportedAt() != "2026-09-01T12:00:00Z" {
		t.Fatalf("ready row = %+v", r0)
	}
	if r1.GetState() != volume.NodeStateNoReport || r1.GetSizeBytes() != 0 || r1.GetFreeBytes() != 0 ||
		r1.GetDetail() != "" || r1.GetReportedAt() != "" {
		t.Fatalf("no_report row leaked stale values: %+v", r1)
	}

	_, err = s.VolumePoolGet(context.Background(), &pb.VolumePoolGetRequest{Name: "gone"})
	requireCode(t, err, codes.NotFound)
}

func TestVolumePoolCreate_PassesRequestAndMapsErrors(t *testing.T) {
	var got volume.CreatePoolRequest
	s := &qrdbServiceServer{volumeSvc: &volumetest.Service{T: t, CreatePoolFn: func(_ context.Context, req volume.CreatePoolRequest) (*volume.Pool, error) {
		if req.Name == "dup" {
			return nil, &volume.PoolAlreadyExistsError{Pool: req.Name}
		}
		got = req
		return &volume.Pool{Name: req.Name, Provider: "dir", CapacityBytes: req.CapacityBytes, Overcommit: req.Overcommit}, nil
	}}}
	resp, err := s.VolumePoolCreate(context.Background(), &pb.VolumePoolCreateRequest{
		Name: "fast", Provider: "dir", Description: "nvme", CapacityBytes: 1024, Overcommit: 2,
	})
	if err != nil {
		t.Fatalf("VolumePoolCreate: %v", err)
	}
	want := volume.CreatePoolRequest{Name: "fast", Provider: "dir", Description: "nvme", CapacityBytes: 1024, Overcommit: 2}
	if got != want {
		t.Fatalf("service saw %+v, want %+v", got, want)
	}
	if resp.GetName() != "fast" || resp.GetCapacityBytes() != 1024 || resp.GetOvercommit() != 2 {
		t.Fatalf("response = %+v", resp)
	}

	_, err = s.VolumePoolCreate(context.Background(), &pb.VolumePoolCreateRequest{Name: "dup"})
	requireCode(t, err, codes.AlreadyExists)
}

// TestVolumePoolUpdate_WrappersDistinguishUnsetFromZero proves the wrapper
// fields reach the service as nil when absent and as a pointer when present,
// so an explicit zero capacity (return to derived capacity) is not lost.
func TestVolumePoolUpdate_WrappersDistinguishUnsetFromZero(t *testing.T) {
	var got volume.UpdatePoolRequest
	s := &qrdbServiceServer{volumeSvc: &volumetest.Service{T: t, UpdatePoolFn: func(_ context.Context, name string, req volume.UpdatePoolRequest) (*volume.Pool, error) {
		if name == "gone" {
			return nil, &volume.PoolNotFoundError{Pool: name}
		}
		got = req
		return &volume.Pool{Name: name, Provider: "dir", Overcommit: 1}, nil
	}}}

	if _, err := s.VolumePoolUpdate(context.Background(), &pb.VolumePoolUpdateRequest{
		Name: "fast", CapacityBytes: wrapperspb.Int64(0),
	}); err != nil {
		t.Fatalf("VolumePoolUpdate: %v", err)
	}
	if got.CapacityBytes == nil || *got.CapacityBytes != 0 || got.Description != nil || got.Overcommit != nil {
		t.Fatalf("capacity-only update = %+v", got)
	}

	if _, err := s.VolumePoolUpdate(context.Background(), &pb.VolumePoolUpdateRequest{
		Name: "fast", Description: wrapperspb.String("ssd"), Overcommit: wrapperspb.Double(1.25),
	}); err != nil {
		t.Fatalf("VolumePoolUpdate: %v", err)
	}
	if got.CapacityBytes != nil || got.Description == nil || *got.Description != "ssd" ||
		got.Overcommit == nil || *got.Overcommit != 1.25 {
		t.Fatalf("description+overcommit update = %+v", got)
	}

	_, err := s.VolumePoolUpdate(context.Background(), &pb.VolumePoolUpdateRequest{Name: "gone"})
	requireCode(t, err, codes.NotFound)
}

func TestVolumePoolDelete_RefusesPoolInUse(t *testing.T) {
	var deleted []string
	s := &qrdbServiceServer{volumeSvc: &volumetest.Service{T: t, DeletePoolFn: func(_ context.Context, name string) error {
		if name == "busy" {
			return &volume.PoolInUseError{Pool: name, Tenants: []string{"t1"}}
		}
		deleted = append(deleted, name)
		return nil
	}}}
	if _, err := s.VolumePoolDelete(context.Background(), &pb.VolumePoolDeleteRequest{Name: "fast"}); err != nil {
		t.Fatalf("VolumePoolDelete: %v", err)
	}
	_, err := s.VolumePoolDelete(context.Background(), &pb.VolumePoolDeleteRequest{Name: "busy"})
	requireCode(t, err, codes.FailedPrecondition)
	if len(deleted) != 1 || deleted[0] != "fast" {
		t.Fatalf("deleted = %v, want [fast]", deleted)
	}
}

func TestVolumePoolListTenants_Paginates(t *testing.T) {
	s := &qrdbServiceServer{volumeSvc: &volumetest.Service{T: t, ListAssignmentsFn: func(_ context.Context, pool, token string, size int) (*volume.AssignmentPage, error) {
		if pool == "gone" {
			return nil, &volume.PoolNotFoundError{Pool: pool}
		}
		if pool != "fast" || token != "t1" || size != 2 {
			t.Fatalf("ListAssignments(%q, %q, %d)", pool, token, size)
		}
		return &volume.AssignmentPage{
			Assignments:   []volume.Assignment{{TenantID: "t2", ReservedBytes: 10}, {TenantID: "t3", ReservedBytes: 20}},
			NextPageToken: "t3",
		}, nil
	}}}
	resp, err := s.VolumePoolListTenants(context.Background(), &pb.VolumePoolListTenantsRequest{Pool: "fast", PageToken: "t1", PageSize: 2})
	if err != nil {
		t.Fatalf("VolumePoolListTenants: %v", err)
	}
	if resp.GetNextPageToken() != "t3" || len(resp.GetTenants()) != 2 ||
		resp.GetTenants()[1].GetTenantId() != "t3" || resp.GetTenants()[1].GetReservedBytes() != 20 {
		t.Fatalf("response = %+v", resp)
	}

	_, err = s.VolumePoolListTenants(context.Background(), &pb.VolumePoolListTenantsRequest{Pool: "gone"})
	requireCode(t, err, codes.NotFound)
}

func TestVolumePoolExportedConverters(t *testing.T) {
	view := &volume.PoolView{Pool: volume.Pool{Name: "fast", Provider: "dir"}, TenantCount: 2}
	if p := VolumePoolToProto(view); p.GetName() != "fast" || p.GetTenantCount() != 2 {
		t.Fatalf("VolumePoolToProto = %+v", p)
	}
	detail := &volume.PoolDetail{PoolView: *view, NodeReports: []volume.NodeReportRow{{NodeID: "n1", State: "locked", Detail: "sealed"}}}
	d := VolumePoolDetailToProto(detail)
	if d.GetPool().GetName() != "fast" || len(d.GetNodeReports()) != 1 || d.GetNodeReports()[0].GetDetail() != "sealed" {
		t.Fatalf("VolumePoolDetailToProto = %+v", d)
	}
}

// TestNewServer_WithVolumeServiceServesRPC proves the server option reaches
// the registered service over a real connection, not just a struct field.
func TestNewServer_WithVolumeServiceServesRPC(t *testing.T) {
	svc := &volumetest.Service{T: t, ListPoolsFn: func(context.Context) ([]volume.PoolView, error) {
		return []volume.PoolView{{Pool: volume.Pool{Name: "fast", Provider: "dir"}}}, nil
	}}
	client, cleanup := startTestServerFull(t, WithVolumeService(svc))
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := client.VolumePoolList(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatalf("VolumePoolList: %v", err)
	}
	if len(resp.GetPools()) != 1 || resp.GetPools()[0].GetName() != "fast" {
		t.Fatalf("pools = %+v", resp.GetPools())
	}
}

// TestRegisterService_WithRegisterVolumeServiceServesRPC is the same proof
// for the shared RegisterService path other transports (Unix socket) use.
func TestRegisterService_WithRegisterVolumeServiceServesRPC(t *testing.T) {
	svc := &volumetest.Service{T: t, DeletePoolFn: func(_ context.Context, name string) error {
		return &volume.PoolNotFoundError{Pool: name}
	}}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcSrv := grpc.NewServer()
	RegisterService(grpcSrv, newMockKVService(), newMockAdminService(), WithRegisterVolumeService(svc))
	go func() { _ = grpcSrv.Serve(lis) }()
	defer grpcSrv.Stop()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = pb.NewQRDBServiceClient(conn).VolumePoolDelete(ctx, &pb.VolumePoolDeleteRequest{Name: "gone"})
	requireCode(t, err, codes.NotFound)
}
