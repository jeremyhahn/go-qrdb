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

//go:build !integration

package etcd

import (
	"context"
	"errors"
	"io"
	"testing"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
)

// mockLeaseServicer implements lease.LeaseServicer with function fields
// for test control.
type mockLeaseServicer struct {
	grantFn      func(ctx context.Context, ttl int64) (*lease.Lease, error)
	revokeFn     func(ctx context.Context, leaseID int64) error
	keepAliveFn  func(ctx context.Context, leaseID int64) (*lease.Lease, error)
	timeToLiveFn func(ctx context.Context, leaseID int64) (*lease.Lease, error)
	leasesFn     func(ctx context.Context) ([]*lease.Lease, error)
}

func (m *mockLeaseServicer) Grant(ctx context.Context, ttl int64) (*lease.Lease, error) {
	return m.grantFn(ctx, ttl)
}

func (m *mockLeaseServicer) Revoke(ctx context.Context, leaseID int64) error {
	return m.revokeFn(ctx, leaseID)
}

func (m *mockLeaseServicer) KeepAlive(ctx context.Context, leaseID int64) (*lease.Lease, error) {
	return m.keepAliveFn(ctx, leaseID)
}

func (m *mockLeaseServicer) TimeToLive(ctx context.Context, leaseID int64) (*lease.Lease, error) {
	return m.timeToLiveFn(ctx, leaseID)
}

func (m *mockLeaseServicer) Leases(ctx context.Context) ([]*lease.Lease, error) {
	return m.leasesFn(ctx)
}

// mockKeepAliveStream implements pb.Lease_LeaseKeepAliveServer for testing
// the bidirectional LeaseKeepAlive RPC.
type mockKeepAliveStream struct {
	ctx       context.Context
	recvErr   error
	sent      []*pb.LeaseKeepAliveResponse
	sendErr   error
	recvCount int
	recvMax   int
	requests  []*pb.LeaseKeepAliveRequest
}

func newMockKeepAliveStream(ctx context.Context, requests []*pb.LeaseKeepAliveRequest) *mockKeepAliveStream {
	return &mockKeepAliveStream{
		ctx:      ctx,
		requests: requests,
		recvMax:  len(requests),
	}
}

func (m *mockKeepAliveStream) Context() context.Context {
	return m.ctx
}

func (m *mockKeepAliveStream) Recv() (*pb.LeaseKeepAliveRequest, error) {
	if m.recvErr != nil {
		return nil, m.recvErr
	}
	if m.recvCount >= m.recvMax {
		return nil, io.EOF
	}
	req := m.requests[m.recvCount]
	m.recvCount++
	return req, nil
}

func (m *mockKeepAliveStream) Send(resp *pb.LeaseKeepAliveResponse) error {
	if m.sendErr != nil {
		return m.sendErr
	}
	m.sent = append(m.sent, resp)
	return nil
}

func (m *mockKeepAliveStream) SetHeader(metadata.MD) error  { return nil }
func (m *mockKeepAliveStream) SendHeader(metadata.MD) error { return nil }
func (m *mockKeepAliveStream) SetTrailer(metadata.MD)       {}
func (m *mockKeepAliveStream) SendMsg(any) error            { return nil }
func (m *mockKeepAliveStream) RecvMsg(any) error            { return nil }

func newTestLeaseServer(svc lease.LeaseServicer) *leaseServer {
	return &leaseServer{
		leaseSvc:  svc,
		clusterID: 1000,
		memberID:  2000,
	}
}

// ---------------------------------------------------------------------------
// LeaseGrant
// ---------------------------------------------------------------------------

func TestLeaseGrant_Success(t *testing.T) {
	svc := &mockLeaseServicer{
		grantFn: func(_ context.Context, ttl int64) (*lease.Lease, error) {
			return &lease.Lease{
				ID:  42,
				TTL: ttl,
			}, nil
		},
	}
	srv := newTestLeaseServer(svc)

	resp, err := srv.LeaseGrant(context.Background(), &pb.LeaseGrantRequest{TTL: 60})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ID != 42 {
		t.Fatalf("ID = %d, want 42", resp.ID)
	}
	if resp.TTL != 60 {
		t.Fatalf("TTL = %d, want 60", resp.TTL)
	}
	if resp.Header == nil {
		t.Fatal("expected non-nil header")
	}
	if resp.Header.ClusterId != 1000 {
		t.Fatalf("ClusterId = %d, want 1000", resp.Header.ClusterId)
	}
	if resp.Header.MemberId != 2000 {
		t.Fatalf("MemberId = %d, want 2000", resp.Header.MemberId)
	}
}

func TestLeaseGrant_ErrorPropagation(t *testing.T) {
	svc := &mockLeaseServicer{
		grantFn: func(_ context.Context, _ int64) (*lease.Lease, error) {
			return nil, errors.New("grant failed")
		},
	}
	srv := newTestLeaseServer(svc)

	resp, err := srv.LeaseGrant(context.Background(), &pb.LeaseGrantRequest{TTL: 10})
	if resp != nil {
		t.Fatal("expected nil response on error")
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Internal {
		t.Fatalf("code = %v, want Internal", st.Code())
	}
}

func TestLeaseGrant_LargeTTL(t *testing.T) {
	svc := &mockLeaseServicer{
		grantFn: func(_ context.Context, ttl int64) (*lease.Lease, error) {
			return &lease.Lease{
				ID:  99,
				TTL: ttl,
			}, nil
		},
	}
	srv := newTestLeaseServer(svc)

	resp, err := srv.LeaseGrant(context.Background(), &pb.LeaseGrantRequest{TTL: 86400})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.TTL != 86400 {
		t.Fatalf("TTL = %d, want 86400", resp.TTL)
	}
}

// ---------------------------------------------------------------------------
// LeaseRevoke
// ---------------------------------------------------------------------------

func TestLeaseRevoke_Success(t *testing.T) {
	svc := &mockLeaseServicer{
		revokeFn: func(_ context.Context, id int64) error {
			if id != 42 {
				t.Fatalf("revoke called with id %d, want 42", id)
			}
			return nil
		},
	}
	srv := newTestLeaseServer(svc)

	resp, err := srv.LeaseRevoke(context.Background(), &pb.LeaseRevokeRequest{ID: 42})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Header == nil {
		t.Fatal("expected non-nil header")
	}
	if resp.Header.ClusterId != 1000 {
		t.Fatalf("ClusterId = %d, want 1000", resp.Header.ClusterId)
	}
}

func TestLeaseRevoke_ErrorReturnsInternal(t *testing.T) {
	svc := &mockLeaseServicer{
		revokeFn: func(_ context.Context, _ int64) error {
			return errors.New("not found")
		},
	}
	srv := newTestLeaseServer(svc)

	resp, err := srv.LeaseRevoke(context.Background(), &pb.LeaseRevokeRequest{ID: 999})
	if resp != nil {
		t.Fatal("expected nil response on error")
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Internal {
		t.Fatalf("code = %v, want Internal", st.Code())
	}
}

func TestLeaseRevoke_PassesCorrectID(t *testing.T) {
	var receivedID int64
	svc := &mockLeaseServicer{
		revokeFn: func(_ context.Context, id int64) error {
			receivedID = id
			return nil
		},
	}
	srv := newTestLeaseServer(svc)

	_, err := srv.LeaseRevoke(context.Background(), &pb.LeaseRevokeRequest{ID: 777})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedID != 777 {
		t.Fatalf("received ID = %d, want 777", receivedID)
	}
}

// ---------------------------------------------------------------------------
// LeaseKeepAlive
// ---------------------------------------------------------------------------

func TestLeaseKeepAlive_SingleRequest(t *testing.T) {
	svc := &mockLeaseServicer{
		keepAliveFn: func(_ context.Context, id int64) (*lease.Lease, error) {
			return &lease.Lease{
				ID:  id,
				TTL: 30,
			}, nil
		},
	}
	srv := newTestLeaseServer(svc)

	stream := newMockKeepAliveStream(context.Background(), []*pb.LeaseKeepAliveRequest{
		{ID: 42},
	})

	err := srv.LeaseKeepAlive(stream)
	if err != io.EOF {
		t.Fatalf("expected io.EOF, got %v", err)
	}
	if len(stream.sent) != 1 {
		t.Fatalf("sent %d responses, want 1", len(stream.sent))
	}
	if stream.sent[0].ID != 42 {
		t.Fatalf("response ID = %d, want 42", stream.sent[0].ID)
	}
	if stream.sent[0].TTL != 30 {
		t.Fatalf("response TTL = %d, want 30", stream.sent[0].TTL)
	}
	if stream.sent[0].Header == nil {
		t.Fatal("expected non-nil header in response")
	}
}

func TestLeaseKeepAlive_MultipleRequests(t *testing.T) {
	svc := &mockLeaseServicer{
		keepAliveFn: func(_ context.Context, id int64) (*lease.Lease, error) {
			return &lease.Lease{
				ID:  id,
				TTL: 60,
			}, nil
		},
	}
	srv := newTestLeaseServer(svc)

	stream := newMockKeepAliveStream(context.Background(), []*pb.LeaseKeepAliveRequest{
		{ID: 10},
		{ID: 20},
		{ID: 30},
	})

	err := srv.LeaseKeepAlive(stream)
	if err != io.EOF {
		t.Fatalf("expected io.EOF, got %v", err)
	}
	if len(stream.sent) != 3 {
		t.Fatalf("sent %d responses, want 3", len(stream.sent))
	}
	for i, wantID := range []int64{10, 20, 30} {
		if stream.sent[i].ID != wantID {
			t.Fatalf("sent[%d].ID = %d, want %d", i, stream.sent[i].ID, wantID)
		}
	}
}

func TestLeaseKeepAlive_ServiceError(t *testing.T) {
	svc := &mockLeaseServicer{
		keepAliveFn: func(_ context.Context, _ int64) (*lease.Lease, error) {
			return nil, errors.New("lease expired")
		},
	}
	srv := newTestLeaseServer(svc)

	stream := newMockKeepAliveStream(context.Background(), []*pb.LeaseKeepAliveRequest{
		{ID: 42},
	})

	err := srv.LeaseKeepAlive(stream)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Internal {
		t.Fatalf("code = %v, want Internal", st.Code())
	}
}

func TestLeaseKeepAlive_RecvError(t *testing.T) {
	svc := &mockLeaseServicer{}
	srv := newTestLeaseServer(svc)

	stream := newMockKeepAliveStream(context.Background(), nil)
	stream.recvErr = errors.New("connection reset")

	err := srv.LeaseKeepAlive(stream)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "connection reset" {
		t.Fatalf("error = %q, want %q", err.Error(), "connection reset")
	}
}

func TestLeaseKeepAlive_SendError(t *testing.T) {
	svc := &mockLeaseServicer{
		keepAliveFn: func(_ context.Context, id int64) (*lease.Lease, error) {
			return &lease.Lease{
				ID:  id,
				TTL: 30,
			}, nil
		},
	}
	srv := newTestLeaseServer(svc)

	stream := newMockKeepAliveStream(context.Background(), []*pb.LeaseKeepAliveRequest{
		{ID: 42},
	})
	stream.sendErr = errors.New("send failed")

	err := srv.LeaseKeepAlive(stream)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "send failed" {
		t.Fatalf("error = %q, want %q", err.Error(), "send failed")
	}
}

// ---------------------------------------------------------------------------
// LeaseTimeToLive
// ---------------------------------------------------------------------------

func TestLeaseTimeToLive_SuccessWithKeys(t *testing.T) {
	svc := &mockLeaseServicer{
		timeToLiveFn: func(_ context.Context, id int64) (*lease.Lease, error) {
			return &lease.Lease{
				ID:           id,
				TTL:          60,
				GrantedTTL:   120,
				RemainingTTL: 45,
				Keys:         [][]byte{[]byte("key1"), []byte("key2")},
			}, nil
		},
	}
	srv := newTestLeaseServer(svc)

	resp, err := srv.LeaseTimeToLive(context.Background(), &pb.LeaseTimeToLiveRequest{
		ID:   42,
		Keys: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ID != 42 {
		t.Fatalf("ID = %d, want 42", resp.ID)
	}
	if resp.TTL != 45 {
		t.Fatalf("TTL = %d, want 45 (RemainingTTL)", resp.TTL)
	}
	if resp.GrantedTTL != 120 {
		t.Fatalf("GrantedTTL = %d, want 120", resp.GrantedTTL)
	}
	if len(resp.Keys) != 2 {
		t.Fatalf("Keys length = %d, want 2", len(resp.Keys))
	}
	if string(resp.Keys[0]) != "key1" {
		t.Fatalf("Keys[0] = %q, want %q", resp.Keys[0], "key1")
	}
	if string(resp.Keys[1]) != "key2" {
		t.Fatalf("Keys[1] = %q, want %q", resp.Keys[1], "key2")
	}
	if resp.Header == nil {
		t.Fatal("expected non-nil header")
	}
}

func TestLeaseTimeToLive_SuccessWithoutKeys(t *testing.T) {
	svc := &mockLeaseServicer{
		timeToLiveFn: func(_ context.Context, id int64) (*lease.Lease, error) {
			return &lease.Lease{
				ID:           id,
				TTL:          60,
				GrantedTTL:   120,
				RemainingTTL: 30,
				Keys:         [][]byte{[]byte("secret-key")},
			}, nil
		},
	}
	srv := newTestLeaseServer(svc)

	resp, err := srv.LeaseTimeToLive(context.Background(), &pb.LeaseTimeToLiveRequest{
		ID:   42,
		Keys: false,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ID != 42 {
		t.Fatalf("ID = %d, want 42", resp.ID)
	}
	if resp.TTL != 30 {
		t.Fatalf("TTL = %d, want 30", resp.TTL)
	}
	if resp.GrantedTTL != 120 {
		t.Fatalf("GrantedTTL = %d, want 120", resp.GrantedTTL)
	}
	if resp.Keys != nil {
		t.Fatalf("Keys = %v, want nil when Keys=false", resp.Keys)
	}
}

func TestLeaseTimeToLive_NotFoundError(t *testing.T) {
	svc := &mockLeaseServicer{
		timeToLiveFn: func(_ context.Context, _ int64) (*lease.Lease, error) {
			return nil, errors.New("lease 999 not found")
		},
	}
	srv := newTestLeaseServer(svc)

	resp, err := srv.LeaseTimeToLive(context.Background(), &pb.LeaseTimeToLiveRequest{
		ID: 999,
	})
	if resp != nil {
		t.Fatal("expected nil response on error")
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.NotFound {
		t.Fatalf("code = %v, want NotFound", st.Code())
	}
}

func TestLeaseTimeToLive_WithKeysEmptySlice(t *testing.T) {
	svc := &mockLeaseServicer{
		timeToLiveFn: func(_ context.Context, id int64) (*lease.Lease, error) {
			return &lease.Lease{
				ID:           id,
				TTL:          60,
				GrantedTTL:   60,
				RemainingTTL: 55,
				Keys:         [][]byte{},
			}, nil
		},
	}
	srv := newTestLeaseServer(svc)

	resp, err := srv.LeaseTimeToLive(context.Background(), &pb.LeaseTimeToLiveRequest{
		ID:   1,
		Keys: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Keys) != 0 {
		t.Fatalf("Keys length = %d, want 0", len(resp.Keys))
	}
}

// ---------------------------------------------------------------------------
// LeaseLeases
// ---------------------------------------------------------------------------

func TestLeaseLeases_SuccessMultiple(t *testing.T) {
	svc := &mockLeaseServicer{
		leasesFn: func(_ context.Context) ([]*lease.Lease, error) {
			return []*lease.Lease{
				{ID: 1},
				{ID: 2},
				{ID: 3},
			}, nil
		},
	}
	srv := newTestLeaseServer(svc)

	resp, err := srv.LeaseLeases(context.Background(), &pb.LeaseLeasesRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Header == nil {
		t.Fatal("expected non-nil header")
	}
	if len(resp.Leases) != 3 {
		t.Fatalf("Leases length = %d, want 3", len(resp.Leases))
	}
	for i, wantID := range []int64{1, 2, 3} {
		if resp.Leases[i].ID != wantID {
			t.Fatalf("Leases[%d].ID = %d, want %d", i, resp.Leases[i].ID, wantID)
		}
	}
}

func TestLeaseLeases_EmptyList(t *testing.T) {
	svc := &mockLeaseServicer{
		leasesFn: func(_ context.Context) ([]*lease.Lease, error) {
			return []*lease.Lease{}, nil
		},
	}
	srv := newTestLeaseServer(svc)

	resp, err := srv.LeaseLeases(context.Background(), &pb.LeaseLeasesRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Header == nil {
		t.Fatal("expected non-nil header")
	}
	if len(resp.Leases) != 0 {
		t.Fatalf("Leases length = %d, want 0", len(resp.Leases))
	}
}

func TestLeaseLeases_ErrorPropagation(t *testing.T) {
	svc := &mockLeaseServicer{
		leasesFn: func(_ context.Context) ([]*lease.Lease, error) {
			return nil, errors.New("storage unavailable")
		},
	}
	srv := newTestLeaseServer(svc)

	resp, err := srv.LeaseLeases(context.Background(), &pb.LeaseLeasesRequest{})
	if resp != nil {
		t.Fatal("expected nil response on error")
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Internal {
		t.Fatalf("code = %v, want Internal", st.Code())
	}
}

func TestLeaseLeases_NilListFromService(t *testing.T) {
	svc := &mockLeaseServicer{
		leasesFn: func(_ context.Context) ([]*lease.Lease, error) {
			return nil, nil
		},
	}
	srv := newTestLeaseServer(svc)

	resp, err := srv.LeaseLeases(context.Background(), &pb.LeaseLeasesRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Header == nil {
		t.Fatal("expected non-nil header")
	}
	if len(resp.Leases) != 0 {
		t.Fatalf("Leases length = %d, want 0", len(resp.Leases))
	}
}

func TestLeaseLeases_SingleLease(t *testing.T) {
	svc := &mockLeaseServicer{
		leasesFn: func(_ context.Context) ([]*lease.Lease, error) {
			return []*lease.Lease{
				{ID: 555},
			}, nil
		},
	}
	srv := newTestLeaseServer(svc)

	resp, err := srv.LeaseLeases(context.Background(), &pb.LeaseLeasesRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Leases) != 1 {
		t.Fatalf("Leases length = %d, want 1", len(resp.Leases))
	}
	if resp.Leases[0].ID != 555 {
		t.Fatalf("Leases[0].ID = %d, want 555", resp.Leases[0].ID)
	}
}
