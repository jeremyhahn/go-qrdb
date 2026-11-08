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

package quic

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
	"github.com/jeremyhahn/go-qrdb/pkg/volume/volumetest"
)

func TestHandleVolumePool_NotConfigured(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	handlers := map[string]func(context.Context, []byte) ([]byte, error){
		"list":         srv.handleVolumePoolList,
		"get":          srv.handleVolumePoolGet,
		"create":       srv.handleVolumePoolCreate,
		"update":       srv.handleVolumePoolUpdate,
		"delete":       srv.handleVolumePoolDelete,
		"list-tenants": srv.handleVolumePoolListTenants,
	}
	for name, h := range handlers {
		t.Run(name, func(t *testing.T) {
			_, err := h(context.Background(), nil)
			var nc *ServiceNotConfiguredError
			if !errors.As(err, &nc) || nc.Service != "volume" {
				t.Fatalf("err = %v, want *ServiceNotConfiguredError{volume}", err)
			}
		})
	}
}

func TestHandleVolumePool_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithVolumeService(&volumetest.Service{T: t}))
	handlers := map[string]func(context.Context, []byte) ([]byte, error){
		"get":          srv.handleVolumePoolGet,
		"create":       srv.handleVolumePoolCreate,
		"update":       srv.handleVolumePoolUpdate,
		"delete":       srv.handleVolumePoolDelete,
		"list-tenants": srv.handleVolumePoolListTenants,
	}
	for name, h := range handlers {
		t.Run(name, func(t *testing.T) {
			_, err := h(context.Background(), badProto)
			var ue *UnmarshalError
			if !errors.As(err, &ue) {
				t.Fatalf("err = %v, want *UnmarshalError", err)
			}
		})
	}
}

func TestHandleVolumePool_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "volume refused"}
	svc := &volumetest.Service{
		T:                 t,
		ListPoolsFn:       func(context.Context) ([]volume.PoolView, error) { return nil, svcErr },
		GetPoolFn:         func(context.Context, string) (*volume.PoolDetail, error) { return nil, svcErr },
		CreatePoolFn:      func(context.Context, volume.CreatePoolRequest) (*volume.Pool, error) { return nil, svcErr },
		UpdatePoolFn:      func(context.Context, string, volume.UpdatePoolRequest) (*volume.Pool, error) { return nil, svcErr },
		DeletePoolFn:      func(context.Context, string) error { return svcErr },
		ListAssignmentsFn: func(context.Context, string, string, int) (*volume.AssignmentPage, error) { return nil, svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithVolumeService(svc))
	handlers := map[string]func(context.Context, []byte) ([]byte, error){
		"list":         srv.handleVolumePoolList,
		"get":          srv.handleVolumePoolGet,
		"create":       srv.handleVolumePoolCreate,
		"update":       srv.handleVolumePoolUpdate,
		"delete":       srv.handleVolumePoolDelete,
		"list-tenants": srv.handleVolumePoolListTenants,
	}
	for name, h := range handlers {
		t.Run(name, func(t *testing.T) {
			if _, err := h(context.Background(), nil); !errors.Is(err, svcErr) {
				t.Fatalf("err = %v, want the service error", err)
			}
		})
	}
}

func TestHandleVolumePoolCreate_Success(t *testing.T) {
	var got volume.CreatePoolRequest
	svc := &volumetest.Service{T: t, CreatePoolFn: func(_ context.Context, req volume.CreatePoolRequest) (*volume.Pool, error) {
		got = req
		return &volume.Pool{Name: req.Name, Provider: req.Provider}, nil
	}}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithVolumeService(svc))
	out, err := srv.handleVolumePoolCreate(context.Background(), mustMarshal(t, &pb.VolumePoolCreateRequest{
		Name: "fast", Provider: "dir", Description: "d", CapacityBytes: 1 << 20, Overcommit: 1.5,
	}))
	if err != nil {
		t.Fatalf("handleVolumePoolCreate: %v", err)
	}
	if got.Name != "fast" || got.Provider != "dir" || got.Description != "d" || got.CapacityBytes != 1<<20 || got.Overcommit != 1.5 {
		t.Fatalf("service got %+v", got)
	}
	var pool pb.VolumePool
	if err := proto.Unmarshal(out, &pool); err != nil || pool.GetName() != "fast" {
		t.Fatalf("pool = %+v, err = %v", &pool, err)
	}
}

func TestHandleVolumePoolUpdate_FieldPresence(t *testing.T) {
	tests := map[string]struct {
		req      *pb.VolumePoolUpdateRequest
		wantDesc bool
		wantCap  bool
		wantOver bool
	}{
		"none set": {req: &pb.VolumePoolUpdateRequest{Name: "fast"}},
		"all set": {
			req: &pb.VolumePoolUpdateRequest{
				Name:          "fast",
				Description:   wrapperspb.String("new"),
				CapacityBytes: wrapperspb.Int64(42),
				Overcommit:    wrapperspb.Double(2),
			},
			wantDesc: true, wantCap: true, wantOver: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var got volume.UpdatePoolRequest
			svc := &volumetest.Service{T: t, UpdatePoolFn: func(_ context.Context, n string, req volume.UpdatePoolRequest) (*volume.Pool, error) {
				got = req
				return &volume.Pool{Name: n}, nil
			}}
			srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithVolumeService(svc))
			if _, err := srv.handleVolumePoolUpdate(context.Background(), mustMarshal(t, tc.req)); err != nil {
				t.Fatalf("handleVolumePoolUpdate: %v", err)
			}
			if (got.Description != nil) != tc.wantDesc || (got.CapacityBytes != nil) != tc.wantCap || (got.Overcommit != nil) != tc.wantOver {
				t.Fatalf("update = %+v", got)
			}
			if tc.wantDesc && (*got.Description != "new" || *got.CapacityBytes != 42 || *got.Overcommit != 2) {
				t.Fatalf("update values = %q %d %v", *got.Description, *got.CapacityBytes, *got.Overcommit)
			}
		})
	}
}
