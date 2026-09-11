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

// Deliberately unconstrained. The lock surface is served in every tag set this
// Makefile can produce, so the test that proves it resolves per (tenant,
// database) must compile in every one of them too. It therefore uses its own
// narrow stub rather than mocks_svc_test.go's fuller doubles, whose file
// carries the qrdb_ts/vector/mysql/pgsql/etcd constraint.

package quic

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
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

// mustMarshalLock encodes a lock request frame payload.
func mustMarshalLock(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatalf("proto.Marshal: %v", err)
	}
	return b
}

// lockFrames is every lock frame, so the assertions cover the whole surface
// rather than one verb -- an unresolved verb is an unconfined verb.
var lockFrames = []struct {
	name    string
	payload func(*testing.T) []byte
	handler func(*Server) func(context.Context, []byte) ([]byte, error)
	want    string
}{
	{"acquire",
		func(t *testing.T) []byte {
			return mustMarshalLock(t, &pb.LockAcquireRequest{Key: "deploy", HolderId: "w1", Ttl: 60})
		},
		func(s *Server) func(context.Context, []byte) ([]byte, error) { return s.handleLockAcquire },
		"lock:deploy"},
	{"trylock",
		func(t *testing.T) []byte {
			return mustMarshalLock(t, &pb.LockTryLockRequest{Key: "deploy", HolderId: "w1", Ttl: 60})
		},
		func(s *Server) func(context.Context, []byte) ([]byte, error) { return s.handleLockTryLock },
		"trylock:deploy"},
	{"release",
		func(t *testing.T) []byte {
			return mustMarshalLock(t, &pb.LockReleaseRequest{Key: "deploy", FencingToken: 1})
		},
		func(s *Server) func(context.Context, []byte) ([]byte, error) { return s.handleLockRelease },
		"unlock:deploy"},
	{"extend",
		func(t *testing.T) []byte {
			return mustMarshalLock(t, &pb.LockExtendRequest{Key: "deploy", FencingToken: 1})
		},
		func(s *Server) func(context.Context, []byte) ([]byte, error) { return s.handleLockExtend },
		"extend:deploy"},
	{"status",
		func(t *testing.T) []byte { return mustMarshalLock(t, &pb.LockStatusRequest{Key: "deploy"}) },
		func(s *Server) func(context.Context, []byte) ([]byte, error) { return s.handleLockStatus },
		"status:deploy"},
	{"list",
		func(t *testing.T) []byte { return mustMarshalLock(t, &pb.LockListRequest{Prefix: "jobs/"}) },
		func(s *Server) func(context.Context, []byte) ([]byte, error) { return s.handleLockList },
		"list:jobs/"},
}

// TestQUICLockResolvesThroughTheRegistry holds the transport half of
// go-qrdb#455 on QUIC. A lock frame names no database, so the database is the
// stream's scope -- bound to ctx by handleStream -- exactly as for KV.
func TestQUICLockResolvesThroughTheRegistry(t *testing.T) {
	for _, f := range lockFrames {
		t.Run(f.name, func(t *testing.T) {
			singleton := &scopedLockStub{}
			scoped := &scopedLockStub{}
			reg := &recordingRegistry{lock: scoped}
			s := &Server{registry: reg, lockSvc: singleton}

			if _, err := f.handler(s)(scopedCtx("acme", "db2"), f.payload(t)); err != nil {
				t.Fatalf("%s: %v", f.name, err)
			}

			if len(singleton.calls) != 0 {
				t.Fatalf("the process-wide singleton was used: %v", singleton.calls)
			}
			if len(scoped.calls) != 1 || scoped.calls[0] != f.want {
				t.Fatalf("scoped servicer saw %v, want [%s]", scoped.calls, f.want)
			}
			var sawLockResolution bool
			for _, c := range reg.calls {
				if c.service == "lock" {
					sawLockResolution = true
					if c.tenant != "acme" || c.db != "db2" {
						t.Fatalf("registry resolved (%q, %q), want (acme, db2)", c.tenant, c.db)
					}
				}
			}
			if !sawLockResolution {
				t.Fatal("the registry's LockService was never consulted")
			}
		})
	}
}

// TestQUICLockFallsBackToTheSingleton covers the registry-less composition,
// where the singleton is correct.
func TestQUICLockFallsBackToTheSingleton(t *testing.T) {
	for _, f := range lockFrames {
		t.Run(f.name, func(t *testing.T) {
			singleton := &scopedLockStub{}
			s := &Server{lockSvc: singleton}
			if _, err := f.handler(s)(context.Background(), f.payload(t)); err != nil {
				t.Fatalf("%s: %v", f.name, err)
			}
			if len(singleton.calls) != 1 || singleton.calls[0] != f.want {
				t.Fatalf("singleton saw %v, want [%s]", singleton.calls, f.want)
			}
		})
	}
}

// TestQUICLockWithNeitherIsRefused pins the fail-closed case.
func TestQUICLockWithNeitherIsRefused(t *testing.T) {
	s := &Server{}
	if _, err := s.lockSvcFor(context.Background()); err == nil {
		t.Fatal("expected an error when neither a registry nor a singleton is configured")
	} else if _, ok := err.(*ServiceNotConfiguredError); !ok {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}

	for _, f := range lockFrames {
		t.Run(f.name, func(t *testing.T) {
			if _, err := f.handler(&Server{})(context.Background(), f.payload(t)); err == nil {
				t.Fatalf("%s answered without a lock servicer", f.name)
			}
		})
	}
}
