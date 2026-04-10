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

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
)

// ---------------------------------------------------------------------------
// handleLeaseGrant
// ---------------------------------------------------------------------------

func TestHandleLeaseGrant_Success(t *testing.T) {
	leaseSvc := &mockLeaseService{
		grantFn: func(_ context.Context, ttl int64) (*lease.Lease, error) {
			return &lease.Lease{
				ID:           1001,
				TTL:          ttl,
				GrantedTTL:   ttl,
				RemainingTTL: ttl,
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLease(leaseSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.LeaseGrantRequest{Ttl: 60})
	if err != nil {
		t.Fatalf("failed to marshal LeaseGrantRequest: %v", err)
	}

	resp, err := srv.handleLeaseGrant(ctx, payload)
	if err != nil {
		t.Fatalf("handleLeaseGrant returned unexpected error: %v", err)
	}

	var grantResp pb.LeaseGrantResponse
	if err := proto.Unmarshal(resp, &grantResp); err != nil {
		t.Fatalf("failed to unmarshal LeaseGrantResponse: %v", err)
	}
	if grantResp.GetLease().GetId() != 1001 {
		t.Errorf("expected lease id 1001, got %d", grantResp.GetLease().GetId())
	}
	if grantResp.GetLease().GetTtl() != 60 {
		t.Errorf("expected ttl 60, got %d", grantResp.GetLease().GetTtl())
	}
}

func TestHandleLeaseGrant_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "grant failed"}
	leaseSvc := &mockLeaseService{
		grantFn: func(_ context.Context, _ int64) (*lease.Lease, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLease(leaseSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.LeaseGrantRequest{Ttl: 10})
	if err != nil {
		t.Fatalf("failed to marshal LeaseGrantRequest: %v", err)
	}

	_, err = srv.handleLeaseGrant(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleLeaseGrant, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleLeaseGrant_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleLeaseGrant(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil lease service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
	if snc.Service != "lease" {
		t.Errorf("expected service %q, got %q", "lease", snc.Service)
	}
}

func TestHandleLeaseGrant_UnmarshalError(t *testing.T) {
	leaseSvc := &mockLeaseService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLease(leaseSvc))
	ctx := context.Background()

	_, err := srv.handleLeaseGrant(ctx, []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error for invalid payload, got nil")
	}

	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleLeaseRevoke
// ---------------------------------------------------------------------------

func TestHandleLeaseRevoke_Success(t *testing.T) {
	var capturedLeaseID int64
	leaseSvc := &mockLeaseService{
		revokeFn: func(_ context.Context, leaseID int64) error {
			capturedLeaseID = leaseID
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLease(leaseSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.LeaseRevokeRequest{LeaseId: 555})
	if err != nil {
		t.Fatalf("failed to marshal LeaseRevokeRequest: %v", err)
	}

	resp, err := srv.handleLeaseRevoke(ctx, payload)
	if err != nil {
		t.Fatalf("handleLeaseRevoke returned unexpected error: %v", err)
	}

	if capturedLeaseID != 555 {
		t.Errorf("expected lease_id 555, got %d", capturedLeaseID)
	}
	if len(resp) != 0 {
		t.Errorf("expected empty response, got %d bytes", len(resp))
	}
}

func TestHandleLeaseRevoke_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "revoke failed"}
	leaseSvc := &mockLeaseService{
		revokeFn: func(_ context.Context, _ int64) error {
			return svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLease(leaseSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.LeaseRevokeRequest{LeaseId: 1})
	if err != nil {
		t.Fatalf("failed to marshal LeaseRevokeRequest: %v", err)
	}

	_, err = srv.handleLeaseRevoke(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleLeaseRevoke, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleLeaseRevoke_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleLeaseRevoke(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil lease service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleLeaseKeepAlive
// ---------------------------------------------------------------------------

func TestHandleLeaseKeepAlive_Success(t *testing.T) {
	leaseSvc := &mockLeaseService{
		keepAliveFn: func(_ context.Context, leaseID int64) (*lease.Lease, error) {
			return &lease.Lease{
				ID:           leaseID,
				TTL:          120,
				GrantedTTL:   120,
				RemainingTTL: 120,
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLease(leaseSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.LeaseKeepAliveRequest{LeaseId: 777})
	if err != nil {
		t.Fatalf("failed to marshal LeaseKeepAliveRequest: %v", err)
	}

	resp, err := srv.handleLeaseKeepAlive(ctx, payload)
	if err != nil {
		t.Fatalf("handleLeaseKeepAlive returned unexpected error: %v", err)
	}

	var kaResp pb.LeaseKeepAliveResponse
	if err := proto.Unmarshal(resp, &kaResp); err != nil {
		t.Fatalf("failed to unmarshal LeaseKeepAliveResponse: %v", err)
	}
	if kaResp.GetLease().GetId() != 777 {
		t.Errorf("expected lease_id 777, got %d", kaResp.GetLease().GetId())
	}
	if kaResp.GetLease().GetTtl() != 120 {
		t.Errorf("expected ttl 120, got %d", kaResp.GetLease().GetTtl())
	}
}

func TestHandleLeaseKeepAlive_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "keep alive failed"}
	leaseSvc := &mockLeaseService{
		keepAliveFn: func(_ context.Context, _ int64) (*lease.Lease, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLease(leaseSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.LeaseKeepAliveRequest{LeaseId: 1})
	if err != nil {
		t.Fatalf("failed to marshal LeaseKeepAliveRequest: %v", err)
	}

	_, err = srv.handleLeaseKeepAlive(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleLeaseKeepAlive, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleLeaseKeepAlive_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleLeaseKeepAlive(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil lease service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleLeaseTimeToLive
// ---------------------------------------------------------------------------

func TestHandleLeaseTimeToLive_Success(t *testing.T) {
	leaseSvc := &mockLeaseService{
		timeToLiveFn: func(_ context.Context, leaseID int64) (*lease.Lease, error) {
			return &lease.Lease{
				ID:           leaseID,
				TTL:          60,
				GrantedTTL:   120,
				RemainingTTL: 45,
				Keys:         [][]byte{[]byte("key1"), []byte("key2")},
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLease(leaseSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.LeaseTimeToLiveRequest{LeaseId: 888})
	if err != nil {
		t.Fatalf("failed to marshal LeaseTimeToLiveRequest: %v", err)
	}

	resp, err := srv.handleLeaseTimeToLive(ctx, payload)
	if err != nil {
		t.Fatalf("handleLeaseTimeToLive returned unexpected error: %v", err)
	}

	var ttlResp pb.LeaseTimeToLiveResponse
	if err := proto.Unmarshal(resp, &ttlResp); err != nil {
		t.Fatalf("failed to unmarshal LeaseTimeToLiveResponse: %v", err)
	}
	if ttlResp.GetLease().GetId() != 888 {
		t.Errorf("expected lease_id 888, got %d", ttlResp.GetLease().GetId())
	}
	if ttlResp.GetLease().GetRemainingTtl() != 45 {
		t.Errorf("expected remaining_ttl 45, got %d", ttlResp.GetLease().GetRemainingTtl())
	}
	if len(ttlResp.GetLease().GetKeys()) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(ttlResp.GetLease().GetKeys()))
	}
}

func TestHandleLeaseTimeToLive_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "time to live failed"}
	leaseSvc := &mockLeaseService{
		timeToLiveFn: func(_ context.Context, _ int64) (*lease.Lease, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLease(leaseSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.LeaseTimeToLiveRequest{LeaseId: 1})
	if err != nil {
		t.Fatalf("failed to marshal LeaseTimeToLiveRequest: %v", err)
	}

	_, err = srv.handleLeaseTimeToLive(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleLeaseTimeToLive, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleLeaseTimeToLive_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleLeaseTimeToLive(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil lease service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleLeaseList
// ---------------------------------------------------------------------------

func TestHandleLeaseList_Success(t *testing.T) {
	leaseSvc := &mockLeaseService{
		leasesFn: func(_ context.Context) ([]*lease.Lease, error) {
			return []*lease.Lease{
				{ID: 1, TTL: 60, GrantedTTL: 60, RemainingTTL: 30},
				{ID: 2, TTL: 120, GrantedTTL: 120, RemainingTTL: 100},
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLease(leaseSvc))
	ctx := context.Background()

	resp, err := srv.handleLeaseList(ctx, nil)
	if err != nil {
		t.Fatalf("handleLeaseList returned unexpected error: %v", err)
	}

	var listResp pb.LeaseListResponse
	if err := proto.Unmarshal(resp, &listResp); err != nil {
		t.Fatalf("failed to unmarshal LeaseListResponse: %v", err)
	}
	if len(listResp.GetLeases()) != 2 {
		t.Fatalf("expected 2 leases, got %d", len(listResp.GetLeases()))
	}
	if listResp.GetLeases()[0].GetId() != 1 {
		t.Errorf("expected first lease id 1, got %d", listResp.GetLeases()[0].GetId())
	}
	if listResp.GetLeases()[1].GetId() != 2 {
		t.Errorf("expected second lease id 2, got %d", listResp.GetLeases()[1].GetId())
	}
}

func TestHandleLeaseList_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "list leases failed"}
	leaseSvc := &mockLeaseService{
		leasesFn: func(_ context.Context) ([]*lease.Lease, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLease(leaseSvc))
	ctx := context.Background()

	_, err := srv.handleLeaseList(ctx, nil)
	if err == nil {
		t.Fatal("expected error from handleLeaseList, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleLeaseList_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleLeaseList(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil lease service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestHandleLeaseList_Empty(t *testing.T) {
	leaseSvc := &mockLeaseService{
		leasesFn: func(_ context.Context) ([]*lease.Lease, error) {
			return []*lease.Lease{}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLease(leaseSvc))
	ctx := context.Background()

	resp, err := srv.handleLeaseList(ctx, nil)
	if err != nil {
		t.Fatalf("handleLeaseList returned unexpected error: %v", err)
	}

	var listResp pb.LeaseListResponse
	if err := proto.Unmarshal(resp, &listResp); err != nil {
		t.Fatalf("failed to unmarshal LeaseListResponse: %v", err)
	}
	if len(listResp.GetLeases()) != 0 {
		t.Errorf("expected 0 leases, got %d", len(listResp.GetLeases()))
	}
}

// ---------------------------------------------------------------------------
// Unmarshal error tests for remaining lease handlers
// ---------------------------------------------------------------------------

func TestHandleLeaseRevoke_UnmarshalError(t *testing.T) {
	leaseSvc := &mockLeaseService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLease(leaseSvc))
	ctx := context.Background()

	_, err := srv.handleLeaseRevoke(ctx, []byte("invalid-protobuf"))
	if err == nil {
		t.Fatal("expected error for invalid protobuf")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleLeaseKeepAlive_UnmarshalError(t *testing.T) {
	leaseSvc := &mockLeaseService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLease(leaseSvc))
	ctx := context.Background()

	_, err := srv.handleLeaseKeepAlive(ctx, []byte("invalid-protobuf"))
	if err == nil {
		t.Fatal("expected error for invalid protobuf")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleLeaseTimeToLive_UnmarshalError(t *testing.T) {
	leaseSvc := &mockLeaseService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLease(leaseSvc))
	ctx := context.Background()

	_, err := srv.handleLeaseTimeToLive(ctx, []byte("invalid-protobuf"))
	if err == nil {
		t.Fatal("expected error for invalid protobuf")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}
