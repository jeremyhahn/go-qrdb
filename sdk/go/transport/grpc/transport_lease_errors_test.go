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
	"errors"
	"net"
	"testing"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// go-qrdb#169: carry service.LeaseNotFoundError / service.LeaseExpiredError
// across the gRPC seam as transport.LeaseNotFoundError / LeaseExpiredError,
// completing #141 for the lease family the same way #289 did for the user
// family. errorLeaseServicer returns the exact service-layer types the
// engine is expected to produce (see mapLeaseAbsent's doc comment in
// transport_lease.go for what is NOT yet established about that production
// path); this test exercises the gRPC wire mapping in isolation from it,
// the same way pkg/api/rest/rest_new_test.go's mocked LeaseServicer does for
// REST.
type errorLeaseServicer struct {
	revokeErr    error
	keepAliveErr error
	ttlErr       error
}

var _ lease.LeaseServicer = (*errorLeaseServicer)(nil)

func (f *errorLeaseServicer) Grant(_ context.Context, ttl int64) (*lease.Lease, error) {
	return &lease.Lease{ID: 42, TTL: ttl, GrantedTTL: ttl, RemainingTTL: ttl}, nil
}

func (f *errorLeaseServicer) Revoke(_ context.Context, _ int64) error {
	return f.revokeErr
}

func (f *errorLeaseServicer) KeepAlive(_ context.Context, id int64) (*lease.Lease, error) {
	if f.keepAliveErr != nil {
		return nil, f.keepAliveErr
	}
	return &lease.Lease{ID: id, TTL: 60, RemainingTTL: 60}, nil
}

func (f *errorLeaseServicer) TimeToLive(_ context.Context, id int64) (*lease.Lease, error) {
	if f.ttlErr != nil {
		return nil, f.ttlErr
	}
	return &lease.Lease{ID: id, TTL: 60, RemainingTTL: 30}, nil
}

func (f *errorLeaseServicer) Leases(_ context.Context) ([]*lease.Lease, error) {
	return []*lease.Lease{{ID: 1, TTL: 60}}, nil
}

func startErrorLeaseServer(t *testing.T, svc lease.LeaseServicer) (string, func()) {
	t.Helper()
	srv := googlegrpc.NewServer()
	grpcpkg.RegisterService(srv, nil, nil, grpcpkg.WithRegisterLease(svc))

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	return lis.Addr().String(), func() { srv.Stop() }
}

func connectErrorLeaseTransport(t *testing.T, addr string) (*Transport, func()) {
	t.Helper()
	conn, err := googlegrpc.NewClient(addr, googlegrpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	tr := &Transport{
		config: &transport.Config{Address: addr},
		conn:   conn,
		client: pb.NewQRDBServiceClient(conn),
	}
	return tr, func() { _ = conn.Close() }
}

func TestGRPCLeaseRevoke_NotFound(t *testing.T) {
	addr, stop := startErrorLeaseServer(t, &errorLeaseServicer{revokeErr: &service.LeaseNotFoundError{LeaseID: 42}})
	defer stop()
	tr, cleanup := connectErrorLeaseTransport(t, addr)
	defer cleanup()

	err := tr.LeaseRevoke(context.Background(), 42)
	var notFound *transport.LeaseNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected *transport.LeaseNotFoundError, got %T: %v", err, err)
	}
	if notFound.LeaseID != 42 {
		t.Errorf("LeaseID: got %d, want 42", notFound.LeaseID)
	}
}

func TestGRPCLeaseRevoke_Expired(t *testing.T) {
	addr, stop := startErrorLeaseServer(t, &errorLeaseServicer{revokeErr: &service.LeaseExpiredError{LeaseID: 42}})
	defer stop()
	tr, cleanup := connectErrorLeaseTransport(t, addr)
	defer cleanup()

	err := tr.LeaseRevoke(context.Background(), 42)
	var expired *transport.LeaseExpiredError
	if !errors.As(err, &expired) {
		t.Fatalf("expected *transport.LeaseExpiredError, got %T: %v", err, err)
	}
	if expired.LeaseID != 42 {
		t.Errorf("LeaseID: got %d, want 42", expired.LeaseID)
	}
}

func TestGRPCLeaseKeepAlive_NotFound(t *testing.T) {
	addr, stop := startErrorLeaseServer(t, &errorLeaseServicer{keepAliveErr: &service.LeaseNotFoundError{LeaseID: 7}})
	defer stop()
	tr, cleanup := connectErrorLeaseTransport(t, addr)
	defer cleanup()

	_, err := tr.LeaseKeepAlive(context.Background(), 7)
	var notFound *transport.LeaseNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected *transport.LeaseNotFoundError, got %T: %v", err, err)
	}
	if notFound.LeaseID != 7 {
		t.Errorf("LeaseID: got %d, want 7", notFound.LeaseID)
	}
}

func TestGRPCLeaseKeepAlive_Expired(t *testing.T) {
	addr, stop := startErrorLeaseServer(t, &errorLeaseServicer{keepAliveErr: &service.LeaseExpiredError{LeaseID: 7}})
	defer stop()
	tr, cleanup := connectErrorLeaseTransport(t, addr)
	defer cleanup()

	_, err := tr.LeaseKeepAlive(context.Background(), 7)
	var expired *transport.LeaseExpiredError
	if !errors.As(err, &expired) {
		t.Fatalf("expected *transport.LeaseExpiredError, got %T: %v", err, err)
	}
	if expired.LeaseID != 7 {
		t.Errorf("LeaseID: got %d, want 7", expired.LeaseID)
	}
}

func TestGRPCLeaseTimeToLive_NotFound(t *testing.T) {
	addr, stop := startErrorLeaseServer(t, &errorLeaseServicer{ttlErr: &service.LeaseNotFoundError{LeaseID: 99}})
	defer stop()
	tr, cleanup := connectErrorLeaseTransport(t, addr)
	defer cleanup()

	_, err := tr.LeaseTimeToLive(context.Background(), 99)
	var notFound *transport.LeaseNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected *transport.LeaseNotFoundError, got %T: %v", err, err)
	}
	if notFound.LeaseID != 99 {
		t.Errorf("LeaseID: got %d, want 99", notFound.LeaseID)
	}
}

func TestGRPCLeaseTimeToLive_Expired(t *testing.T) {
	addr, stop := startErrorLeaseServer(t, &errorLeaseServicer{ttlErr: &service.LeaseExpiredError{LeaseID: 99}})
	defer stop()
	tr, cleanup := connectErrorLeaseTransport(t, addr)
	defer cleanup()

	_, err := tr.LeaseTimeToLive(context.Background(), 99)
	var expired *transport.LeaseExpiredError
	if !errors.As(err, &expired) {
		t.Fatalf("expected *transport.LeaseExpiredError, got %T: %v", err, err)
	}
	if expired.LeaseID != 99 {
		t.Errorf("LeaseID: got %d, want 99", expired.LeaseID)
	}
}

// TestGRPCLeaseTimeToLive_NonNotFoundNotReclassified is the mutation-check
// companion: mapLeaseAbsent's codes.NotFound gate must actually gate. A
// non-NotFound condition from the same call -- service.NotLeaderError maps
// to codes.Unavailable -- must fall through to mapGRPCError's RequestError,
// not become a lease error just because the call was LeaseTimeToLive. This
// is what would go red if the codes.NotFound check in mapLeaseAbsent were
// deleted (or widened to "any error at all"): the assertion would then see
// a *transport.LeaseNotFoundError instead of a *transport.RequestError.
func TestGRPCLeaseTimeToLive_NonNotFoundNotReclassified(t *testing.T) {
	addr, stop := startErrorLeaseServer(t, &errorLeaseServicer{ttlErr: &service.NotLeaderError{ShardID: 1}})
	defer stop()
	tr, cleanup := connectErrorLeaseTransport(t, addr)
	defer cleanup()

	_, err := tr.LeaseTimeToLive(context.Background(), 99)
	var notFound *transport.LeaseNotFoundError
	var expired *transport.LeaseExpiredError
	if errors.As(err, &notFound) || errors.As(err, &expired) {
		t.Fatalf("expected a non-lease error, got %T: %v", err, err)
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}
