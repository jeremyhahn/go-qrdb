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

// Deliberately unconstrained, like scope_test.go beside it: the lock surface
// is served in every tag set, so the test that proves it resolves per
// (tenant, database) must compile in every one of them.

package grpc

import (
	"context"
	"testing"
	"time"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// scopedLockStub is the narrowest LockServicer that can answer "which servicer
// did the handler reach, and with what?".
type scopedLockStub struct {
	service.LockServicer
	calls []string
}

func (s *scopedLockStub) Lock(_ context.Context, key, holderID string, _ time.Duration, _ bool) (*lock.Lock, error) {
	s.calls = append(s.calls, "lock:"+key)
	return &lock.Lock{Key: key, HolderID: holderID, FencingToken: 1}, nil
}

func (s *scopedLockStub) TryLock(_ context.Context, key, holderID string, _ time.Duration, _ bool) (*lock.Lock, error) {
	s.calls = append(s.calls, "trylock:"+key)
	return &lock.Lock{Key: key, HolderID: holderID, FencingToken: 1}, nil
}

func (s *scopedLockStub) Unlock(_ context.Context, key string, _ int64) error {
	s.calls = append(s.calls, "unlock:"+key)
	return nil
}

func (s *scopedLockStub) Extend(_ context.Context, key string, _ int64) (*lock.Lock, error) {
	s.calls = append(s.calls, "extend:"+key)
	return &lock.Lock{Key: key, FencingToken: 2}, nil
}

func (s *scopedLockStub) Status(_ context.Context, key string) (*lock.Lock, error) {
	s.calls = append(s.calls, "status:"+key)
	return &lock.Lock{Key: key, HolderID: "w1", FencingToken: 1}, nil
}

func (s *scopedLockStub) List(_ context.Context, prefix string) ([]*lock.Lock, error) {
	s.calls = append(s.calls, "list:"+prefix)
	return nil, nil
}

// lockRPCs is every lock RPC, so the assertions cover the whole surface rather
// than one verb -- an unresolved verb is an unconfined verb.
var lockRPCs = []struct {
	name string
	call func(context.Context, *qrdbServiceServer) error
	want string
}{
	{"acquire", func(ctx context.Context, s *qrdbServiceServer) error {
		_, err := s.LockAcquire(ctx, &pb.LockAcquireRequest{Key: "deploy", HolderId: "w1", Ttl: 60})
		return err
	}, "lock:deploy"},
	{"trylock", func(ctx context.Context, s *qrdbServiceServer) error {
		_, err := s.LockTryLock(ctx, &pb.LockTryLockRequest{Key: "deploy", HolderId: "w1", Ttl: 60})
		return err
	}, "trylock:deploy"},
	{"release", func(ctx context.Context, s *qrdbServiceServer) error {
		_, err := s.LockRelease(ctx, &pb.LockReleaseRequest{Key: "deploy", FencingToken: 1})
		return err
	}, "unlock:deploy"},
	{"extend", func(ctx context.Context, s *qrdbServiceServer) error {
		_, err := s.LockExtend(ctx, &pb.LockExtendRequest{Key: "deploy", FencingToken: 1})
		return err
	}, "extend:deploy"},
	{"status", func(ctx context.Context, s *qrdbServiceServer) error {
		_, err := s.LockStatus(ctx, &pb.LockStatusRequest{Key: "deploy"})
		return err
	}, "status:deploy"},
	{"list", func(ctx context.Context, s *qrdbServiceServer) error {
		_, err := s.LockList(ctx, &pb.LockListRequest{Prefix: "jobs/"})
		return err
	}, "list:jobs/"},
}

// TestGRPCLockResolvesThroughTheRegistry holds the transport half of
// go-qrdb#455 on gRPC -- and therefore on the Unix socket transport, which
// registers this same service. A lock names no database in its request
// message, so the database is the request's scope (x-qrdb-database, bound to
// ctx by the scope interceptor), exactly as for KV.
func TestGRPCLockResolvesThroughTheRegistry(t *testing.T) {
	for _, rpc := range lockRPCs {
		t.Run(rpc.name, func(t *testing.T) {
			singleton := &scopedLockStub{}
			scoped := &scopedLockStub{}
			reg := &recordingRegistry{lock: scoped}
			s := &qrdbServiceServer{registry: reg, lockSvc: singleton}

			ctx := withDatabase(tenant.WithTenantID(context.Background(), "acme"), "db2")
			if err := rpc.call(ctx, s); err != nil {
				t.Fatalf("%s: %v", rpc.name, err)
			}

			if len(singleton.calls) != 0 {
				t.Fatalf("the process-wide singleton was used: %v", singleton.calls)
			}
			if len(scoped.calls) != 1 || scoped.calls[0] != rpc.want {
				t.Fatalf("scoped servicer saw %v, want [%s]", scoped.calls, rpc.want)
			}
			if reg.gotTenant != "acme" || reg.gotDB != "db2" {
				t.Fatalf("registry resolved (%q, %q), want (acme, db2)", reg.gotTenant, reg.gotDB)
			}
		})
	}
}

// TestGRPCLockFallsBackToTheSingleton covers the registry-less composition,
// where the singleton is correct.
func TestGRPCLockFallsBackToTheSingleton(t *testing.T) {
	for _, rpc := range lockRPCs {
		t.Run(rpc.name, func(t *testing.T) {
			singleton := &scopedLockStub{}
			s := &qrdbServiceServer{lockSvc: singleton}
			if err := rpc.call(context.Background(), s); err != nil {
				t.Fatalf("%s: %v", rpc.name, err)
			}
			if len(singleton.calls) != 1 || singleton.calls[0] != rpc.want {
				t.Fatalf("singleton saw %v, want [%s]", singleton.calls, rpc.want)
			}
		})
	}
}

// TestGRPCLockWithNeitherIsRefused pins the fail-closed case.
func TestGRPCLockWithNeitherIsRefused(t *testing.T) {
	s := &qrdbServiceServer{}
	if _, err := s.lockSvcFor(context.Background()); err == nil {
		t.Fatal("expected an error when neither a registry nor a singleton is configured")
	} else if _, ok := err.(*ServiceNotConfiguredError); !ok {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}

	// And every RPC surfaces it rather than dereferencing nil.
	for _, rpc := range lockRPCs {
		t.Run(rpc.name, func(t *testing.T) {
			if err := rpc.call(context.Background(), &qrdbServiceServer{}); err == nil {
				t.Fatalf("%s answered without a lock servicer", rpc.name)
			}
		})
	}
}
