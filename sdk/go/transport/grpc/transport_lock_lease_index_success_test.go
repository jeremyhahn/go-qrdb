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

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// fakeLockServicer
// ===========================================================================

type fakeLockServicer struct{}

var _ service.LockServicer = (*fakeLockServicer)(nil)

func (f *fakeLockServicer) Lock(_ context.Context, key, holderID string, ttl time.Duration, _ bool) (*lock.Lock, error) {
	return &lock.Lock{Key: key, FencingToken: 1, LeaseID: 42, HolderID: holderID, TTL: int64(ttl.Seconds())}, nil
}
func (f *fakeLockServicer) TryLock(_ context.Context, key, holderID string, ttl time.Duration, _ bool) (*lock.Lock, error) {
	return &lock.Lock{Key: key, FencingToken: 1, LeaseID: 42, HolderID: holderID, TTL: int64(ttl.Seconds())}, nil
}
func (f *fakeLockServicer) Unlock(_ context.Context, _ string, _ int64) error { return nil }
func (f *fakeLockServicer) Extend(_ context.Context, key string, fencing int64) (*lock.Lock, error) {
	return &lock.Lock{Key: key, FencingToken: fencing}, nil
}
func (f *fakeLockServicer) Status(_ context.Context, key string) (*lock.Lock, error) {
	return &lock.Lock{Key: key, FencingToken: 1}, nil
}
func (f *fakeLockServicer) List(_ context.Context, _ string) ([]*lock.Lock, error) {
	return []*lock.Lock{{Key: "k1", FencingToken: 1}}, nil
}

// ===========================================================================
// fakeIndexServicer
// ===========================================================================

type fakeIndexServicer struct{}

var _ index.IndexServicer = (*fakeIndexServicer)(nil)

func (f *fakeIndexServicer) CreateIndex(_ context.Context, _, _ string, _ index.IndexConfig) error {
	return nil
}
func (f *fakeIndexServicer) DropIndex(_ context.Context, _ string) error {
	return nil
}

// DropOwnedIndex answers as DropIndex does: this double records no
// ownership, and no test that uses it exercises an owner-checked drop.
func (f *fakeIndexServicer) DropOwnedIndex(ctx context.Context, name, _ string) error {
	return f.DropIndex(ctx, name)
}
func (f *fakeIndexServicer) Query(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return [][]byte{[]byte("k1")}, nil
}
func (f *fakeIndexServicer) Scan(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return map[string][][]byte{"v1": {[]byte("k1")}}, nil
}
func (f *fakeIndexServicer) ListIndexes(_ context.Context) ([]string, error) {
	return []string{"idx1"}, nil
}

// ===========================================================================
// Server setup
// ===========================================================================

func startLockLeaseIndexServer(t *testing.T) (string, func()) {
	t.Helper()
	srv := googlegrpc.NewServer()
	grpcpkg.RegisterService(srv, nil, nil,
		grpcpkg.WithRegisterLockService(&fakeLockServicer{}),
		grpcpkg.WithRegisterIndex(&fakeIndexServicer{}),
	)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	return lis.Addr().String(), func() { srv.Stop() }
}

func connectLLITransport(t *testing.T, addr string) (*Transport, func()) {
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

// ===========================================================================
// Lock success tests
// ===========================================================================

func TestGRPCLock_AllMethods_Success(t *testing.T) {
	addr, stop := startLockLeaseIndexServer(t)
	defer stop()
	tr, cleanup := connectLLITransport(t, addr)
	defer cleanup()
	ctx := context.Background()

	// LockTryLock, LockStatus, LockExtend and LockRelease run against a real
	// gRPC node in conformance's TestCoordinationFamilies_SameOnStandaloneAndRaft
	// "lock" subtest; the two verbs it does not drive stay here.
	t.Run("LockAcquire", func(t *testing.T) {
		if _, err := tr.LockAcquire(ctx, &transport.LockAcquireRequest{Key: "k1", HolderID: "h1", TTL: 60}); err != nil {
			t.Fatalf("LockAcquire: %v", err)
		}
	})
	t.Run("LockList", func(t *testing.T) {
		if _, err := tr.LockList(ctx, "prefix"); err != nil {
			t.Fatalf("LockList: %v", err)
		}
	})
}

// ===========================================================================
// Index success tests
// ===========================================================================

func TestGRPCIndex_AllMethods_Success(t *testing.T) {
	addr, stop := startLockLeaseIndexServer(t)
	defer stop()
	tr, cleanup := connectLLITransport(t, addr)
	defer cleanup()
	ctx := context.Background()

	t.Run("IndexCreate", func(t *testing.T) {
		if err := tr.IndexCreate(ctx, "idx1", "name", transport.IndexConfig{}); err != nil {
			t.Fatalf("IndexCreate: %v", err)
		}
	})
	t.Run("IndexDrop", func(t *testing.T) {
		if err := tr.IndexDrop(ctx, "idx1"); err != nil {
			t.Fatalf("IndexDrop: %v", err)
		}
	})
	t.Run("IndexQuery", func(t *testing.T) {
		if _, err := tr.IndexQuery(ctx, "idx1", "v"); err != nil {
			t.Fatalf("IndexQuery: %v", err)
		}
	})
	t.Run("IndexScan", func(t *testing.T) {
		if _, err := tr.IndexScan(ctx, "idx1", spIdx("a"), spIdx("z")); err != nil {
			t.Fatalf("IndexScan: %v", err)
		}
	})
	t.Run("IndexList", func(t *testing.T) {
		if _, err := tr.IndexList(ctx); err != nil {
			t.Fatalf("IndexList: %v", err)
		}
	})
}
