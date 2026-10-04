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

//go:build qrdb_etcd

package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/api/etcd"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

var etcdListenCfg = &database.EtcdConfig{Enabled: true, ListenAddr: "127.0.0.1:0"}

func TestStartEtcd_Success(t *testing.T) {
	f := newEtcdScopeFixture(t, true)
	etcdSrv, err := startEtcd(f.srv, "app", etcdListenCfg)
	if err != nil {
		t.Fatalf("startEtcd failed: %v", err)
	}
	defer stopEtcd(etcdSrv)

	addr := etcdSrv.Addr()
	if addr == "" || addr == "127.0.0.1:0" {
		t.Fatalf("etcd server Addr = %q, want a bound address", addr)
	}
	if got := f.srv.EtcdAddr("app"); got != addr {
		t.Fatalf("EtcdAddr = %q, want %q", got, addr)
	}
}

func TestStartEtcd_Failure_PortOccupied(t *testing.T) {
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create blocker listener: %v", err)
	}
	defer func() { _ = blocker.Close() }()
	_, portStr, _ := net.SplitHostPort(blocker.Addr().String())
	blockedPort, _ := strconv.Atoi(portStr)

	f := newEtcdScopeFixture(t, true)
	etcdSrv, err := startEtcd(f.srv, "app", &database.EtcdConfig{
		Enabled:    true,
		ListenAddr: net.JoinHostPort("127.0.0.1", strconv.Itoa(blockedPort)),
	})
	if err == nil {
		stopEtcd(etcdSrv)
		t.Fatal("expected startEtcd to fail on occupied port")
	}
}

func TestStartEtcd_NilConfig(t *testing.T) {
	f := newEtcdScopeFixture(t, true)
	if etcdSrv, err := startEtcd(f.srv, "app", nil); err == nil {
		stopEtcd(etcdSrv)
		t.Fatal("expected startEtcd to fail with nil config")
	}
}

func TestStartEtcd_MissingListenAddr(t *testing.T) {
	f := newEtcdScopeFixture(t, true)
	if etcdSrv, err := startEtcd(f.srv, "app", &database.EtcdConfig{Enabled: true}); err == nil {
		stopEtcd(etcdSrv)
		t.Fatal("expected startEtcd to fail with empty ListenAddr")
	}
}

// Without TLS there is no client certificate to authenticate, so the
// listener is refused exactly as it is without an authorizer.
func TestStartEtcd_RefusedWithoutTLS(t *testing.T) {
	f := newEtcdScopeFixture(t, true)
	f.srv.opts.tlsConfig = nil
	etcdSrv, err := startEtcd(f.srv, "app", etcdListenCfg)
	var required *etcd.AuthRequiredError
	if !errors.As(err, &required) {
		if etcdSrv != nil {
			stopEtcd(etcdSrv)
		}
		t.Fatalf("startEtcd without TLS = %v, want *etcd.AuthRequiredError", err)
	}
}

// A listener that cannot be confined to its database is refused rather than
// served over the raw store.
func TestStartEtcd_RefusedWithoutScope(t *testing.T) {
	t.Run("no catalog", func(t *testing.T) {
		f := newEtcdScopeFixture(t, true)
		f.srv.opts.dbManager = nil
		etcdSrv, err := startEtcd(f.srv, "app", etcdListenCfg)
		var unscoped *etcd.ScopeUnavailableError
		if !errors.As(err, &unscoped) {
			if etcdSrv != nil {
				stopEtcd(etcdSrv)
			}
			t.Fatalf("err = %v, want *etcd.ScopeUnavailableError", err)
		}
	})
	t.Run("no registry", func(t *testing.T) {
		f := newEtcdScopeFixture(t, true)
		f.srv.opts.dbRegistry = nil
		etcdSrv, err := startEtcd(f.srv, "app", etcdListenCfg)
		var unscoped *etcd.ScopeUnavailableError
		if !errors.As(err, &unscoped) {
			if etcdSrv != nil {
				stopEtcd(etcdSrv)
			}
			t.Fatalf("err = %v, want *etcd.ScopeUnavailableError", err)
		}
	})
	t.Run("unknown database", func(t *testing.T) {
		f := newEtcdScopeFixture(t, true)
		if etcdSrv, err := startEtcd(f.srv, "nosuchdb", etcdListenCfg); err == nil {
			stopEtcd(etcdSrv)
			t.Fatal("startEtcd served a database that does not exist")
		}
	})
	// MVCC, leases and watches are raft-only on every transport; a standalone
	// node's registry refuses them, and so does its etcd listener, which
	// would otherwise serve them over the ungated registry.
	t.Run("standalone mode", func(t *testing.T) {
		f := newEtcdScopeFixture(t, true)
		f.srv.opts.standaloneMode = true
		etcdSrv, err := startEtcd(f.srv, "app", etcdListenCfg)
		var unsupported *service.CapabilityUnsupportedError
		if !errors.As(err, &unsupported) || !strings.Contains(err.Error(), StandaloneModeHint) {
			if etcdSrv != nil {
				stopEtcd(etcdSrv)
			}
			t.Fatalf("startEtcd in standalone mode = %v, want *service.CapabilityUnsupportedError naming %q", err, StandaloneModeHint)
		}
	})
	t.Run("sealed database", func(t *testing.T) {
		f := newEtcdScopeFixture(t, true)
		db, _ := f.mgr.Get("app")
		db.SetStatus(database.StatusSealed)
		if etcdSrv, err := startEtcd(f.srv, "app", etcdListenCfg); err == nil {
			stopEtcd(etcdSrv)
			t.Fatal("startEtcd served a sealed database")
		}
	})
}

func TestStopEtcd_Active(t *testing.T) {
	f := newEtcdScopeFixture(t, true)
	etcdSrv, err := startEtcd(f.srv, "app", etcdListenCfg)
	if err != nil {
		t.Fatalf("startEtcd failed: %v", err)
	}
	stopEtcd(etcdSrv)
}

func TestEtcdAddr_NotRunning(t *testing.T) {
	srv := New(testConfig(t, false, false), newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))
	if addr := srv.EtcdAddr("nonexistent"); addr != "" {
		t.Fatalf("expected empty EtcdAddr for non-existent database, got %q", addr)
	}
}

// An authenticated caller from another tenant is refused: the listener
// serves exactly one tenant's database.
func TestEtcdListener_RefusesOtherTenant(t *testing.T) {
	f := newEtcdScopeFixture(t, true)
	f.srv.opts.authMW = authmw.New(allowAll{}, authmw.WithAuthenticator(
		&etcdPeerIdentity{identity: &auth.Identity{Subject: "mallory", TenantID: "globex"}}))
	var logs lockedBuffer
	f.srv.logger = slog.New(slog.NewTextHandler(&logs, nil))
	etcdSrv, err := startEtcd(f.srv, "app", etcdListenCfg)
	if err != nil {
		t.Fatalf("startEtcd: %v", err)
	}
	t.Cleanup(func() { stopEtcd(etcdSrv) })
	kv := f.kv(t, etcdSrv.Addr(), f.clientTLS)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err = kv.Range(ctx, &pb.RangeRequest{Key: []byte("visible")})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("another tenant's Range: code %v (%v), want PermissionDenied", status.Code(err), err)
	}
	// The refusal must not say whose database this is: naming the owning
	// tenant or the database tells a stranger what exists on the port.
	msg := status.Convert(err).Message()
	for _, leak := range []string{"acme", "app"} {
		if strings.Contains(msg, leak) {
			t.Fatalf("the refusal %q names %q", msg, leak)
		}
	}
	// The operator still learns who knocked on which database.
	logged := logs.String()
	for _, want := range []string{"mallory", "globex", "acme", "app"} {
		if !strings.Contains(logged, want) {
			t.Fatalf("the server log %q does not record %q", logged, want)
		}
	}
}

// The etcd listener applies the one ownership rule (database.OwnedBy) in
// both directions: the system tenant is refused on acme's database, and is
// admitted on a database it owns -- one recorded with no owner included --
// while acme is refused there. The owner of each database reads its own key.
func TestEtcdListener_OwnerAdmittedEveryoneElseRefused(t *testing.T) {
	f := newEtcdScopeFixture(t, true)
	if _, err := f.mgr.Create(database.DatabaseConfig{Name: "sysapp", Engine: database.EngineKV, Backend: database.BackendMemory}); err != nil {
		t.Fatalf("create the untagged system database: %v", err)
	}
	f.shared.put("system/sysapp/visible", []byte("v"))
	callers := map[string]*auth.Identity{
		"acme":   {Subject: "alice", TenantID: "acme"},
		"system": {Subject: "root", TenantID: "system"},
	}
	cases := []struct {
		db, caller string
		admitted   bool
	}{
		{"app", "acme", true},
		{"app", "system", false},
		{"sysapp", "system", true},
		{"sysapp", "acme", false},
	}
	for _, c := range cases {
		t.Run(c.db+" as "+c.caller, func(t *testing.T) {
			f.srv.opts.authMW = authmw.New(allowAll{}, authmw.WithAuthenticator(&etcdPeerIdentity{identity: callers[c.caller]}))
			etcdSrv, err := startEtcd(f.srv, c.db, etcdListenCfg)
			if err != nil {
				t.Fatalf("startEtcd: %v", err)
			}
			t.Cleanup(func() { stopEtcd(etcdSrv) })
			kv := f.kv(t, etcdSrv.Addr(), f.clientTLS)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			resp, err := kv.Range(ctx, &pb.RangeRequest{Key: []byte("visible")})
			if !c.admitted {
				if status.Code(err) != codes.PermissionDenied {
					t.Fatalf("Range = %v, want PermissionDenied", err)
				}
				return
			}
			if err != nil || len(resp.Kvs) != 1 {
				t.Fatalf("the owner's Range = %v, %v; want its one key", resp, err)
			}
		})
	}
}

// The authorizer's refusal reaches the client, and nothing is written.
func TestEtcdListener_RefusesWithoutPermission(t *testing.T) {
	f := newEtcdScopeFixture(t, true)
	f.srv.opts.authMW = authmw.New(denyAll{}, authmw.WithAuthenticator(
		&etcdPeerIdentity{identity: &auth.Identity{Subject: "alice", TenantID: "acme"}}))
	etcdSrv, err := startEtcd(f.srv, "app", etcdListenCfg)
	if err != nil {
		t.Fatalf("startEtcd: %v", err)
	}
	t.Cleanup(func() { stopEtcd(etcdSrv) })
	kv := f.kv(t, etcdSrv.Addr(), f.clientTLS)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := kv.Put(ctx, &pb.PutRequest{Key: []byte("k"), Value: []byte("v")}); err == nil {
		t.Fatal("Put succeeded without kv:put")
	}
	f.shared.mu.Lock()
	_, written := f.shared.data["acme/app/k"]
	f.shared.mu.Unlock()
	if written {
		t.Fatal("a refused Put reached the store")
	}
}

// Sealing the database after the listener started refuses the next request:
// the listener re-resolves the database on every call.
func TestEtcdListener_SealedAfterStartFailsClosed(t *testing.T) {
	f := newEtcdScopeFixture(t, true)
	etcdSrv, err := startEtcd(f.srv, "app", etcdListenCfg)
	if err != nil {
		t.Fatalf("startEtcd: %v", err)
	}
	t.Cleanup(func() { stopEtcd(etcdSrv) })
	kv := f.kv(t, etcdSrv.Addr(), f.clientTLS)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db, _ := f.mgr.Get("app")
	db.SetStatus(database.StatusSealed)
	_, err = kv.Range(ctx, &pb.RangeRequest{Key: []byte("visible")})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("Range on a sealed database: code %v (%v), want Unavailable", status.Code(err), err)
	}
}

// denyAll is an authorizer refusing every permission.
type denyAll struct{}

func (denyAll) Authorize(context.Context, *auth.Identity, string, string) error {
	return auth.ErrUnauthorized
}
