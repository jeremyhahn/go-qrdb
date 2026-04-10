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
	"net"
	"strconv"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

// ---------------------------------------------------------------------------
// startEtcd: success with port 0
// ---------------------------------------------------------------------------

func TestStartEtcd_Success(t *testing.T) {
	cfg := testConfig(false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	etcdCfg := &database.EtcdConfig{
		Enabled:    true,
		ListenAddr: "127.0.0.1:0",
	}

	etcdSrv, err := startEtcd(srv, "testdb", etcdCfg)
	if err != nil {
		t.Fatalf("startEtcd failed: %v", err)
	}
	defer stopEtcd(etcdSrv)

	if etcdSrv == nil {
		t.Fatal("expected non-nil EtcdProtocolServer")
	}
	addr := etcdSrv.Addr()
	if addr == "" {
		t.Fatal("expected non-empty Addr from etcd server")
	}
	if addr == "127.0.0.1:0" {
		t.Fatal("etcd server Addr returned unresolved :0")
	}
}

// ---------------------------------------------------------------------------
// startEtcd: failure with occupied port
// ---------------------------------------------------------------------------

func TestStartEtcd_Failure_PortOccupied(t *testing.T) {
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create blocker listener: %v", err)
	}
	defer func() { _ = blocker.Close() }()

	_, portStr, _ := net.SplitHostPort(blocker.Addr().String())
	blockedPort, _ := strconv.Atoi(portStr)

	cfg := testConfig(false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	etcdCfg := &database.EtcdConfig{
		Enabled:    true,
		ListenAddr: net.JoinHostPort("127.0.0.1", strconv.Itoa(blockedPort)),
	}

	etcdSrv, err := startEtcd(srv, "testdb", etcdCfg)
	if err == nil {
		stopEtcd(etcdSrv)
		t.Fatal("expected startEtcd to fail on occupied port")
	}
}

// ---------------------------------------------------------------------------
// startEtcd: with TLS option
// ---------------------------------------------------------------------------

func TestStartEtcd_WithTLS(t *testing.T) {
	tlsCfg := selfSignedTLSConfig(t)
	cfg := testConfig(false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithTLS(tlsCfg))

	etcdCfg := &database.EtcdConfig{
		Enabled:    true,
		ListenAddr: "127.0.0.1:0",
	}

	etcdSrv, err := startEtcd(srv, "testdb", etcdCfg)
	if err != nil {
		t.Fatalf("startEtcd with TLS failed: %v", err)
	}
	defer stopEtcd(etcdSrv)

	if etcdSrv == nil {
		t.Fatal("expected non-nil EtcdProtocolServer")
	}
}

// ---------------------------------------------------------------------------
// startEtcd: with all services
// ---------------------------------------------------------------------------

func TestStartEtcd_WithServices(t *testing.T) {
	cfg := testConfig(false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithMVCC(nil),
		WithTransaction(nil),
		WithWatch(nil),
		WithLease(nil),
	)

	etcdCfg := &database.EtcdConfig{
		Enabled:    true,
		ListenAddr: "127.0.0.1:0",
	}

	etcdSrv, err := startEtcd(srv, "testdb", etcdCfg)
	if err != nil {
		t.Fatalf("startEtcd with services failed: %v", err)
	}
	defer stopEtcd(etcdSrv)

	if etcdSrv == nil {
		t.Fatal("expected non-nil EtcdProtocolServer")
	}
}

// ---------------------------------------------------------------------------
// startEtcd: nil config
// ---------------------------------------------------------------------------

func TestStartEtcd_NilConfig(t *testing.T) {
	cfg := testConfig(false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	etcdSrv, err := startEtcd(srv, "testdb", nil)
	if err == nil {
		stopEtcd(etcdSrv)
		t.Fatal("expected startEtcd to fail with nil config")
	}
}

// ---------------------------------------------------------------------------
// startEtcd: missing listen address
// ---------------------------------------------------------------------------

func TestStartEtcd_MissingListenAddr(t *testing.T) {
	cfg := testConfig(false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	etcdCfg := &database.EtcdConfig{
		Enabled:    true,
		ListenAddr: "",
	}

	etcdSrv, err := startEtcd(srv, "testdb", etcdCfg)
	if err == nil {
		stopEtcd(etcdSrv)
		t.Fatal("expected startEtcd to fail with empty ListenAddr")
	}
}

// ---------------------------------------------------------------------------
// stopEtcd: active server (no panic)
// ---------------------------------------------------------------------------

func TestStopEtcd_Active(t *testing.T) {
	cfg := testConfig(false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	etcdCfg := &database.EtcdConfig{
		Enabled:    true,
		ListenAddr: "127.0.0.1:0",
	}

	etcdSrv, err := startEtcd(srv, "testdb", etcdCfg)
	if err != nil {
		t.Fatalf("startEtcd failed: %v", err)
	}

	// Stop should complete without panic.
	stopEtcd(etcdSrv)
}

// ---------------------------------------------------------------------------
// EtcdAddr: running server returns correct address
// ---------------------------------------------------------------------------

func TestEtcdAddr_Running(t *testing.T) {
	cfg := testConfig(false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	etcdCfg := &database.EtcdConfig{
		Enabled:    true,
		ListenAddr: "127.0.0.1:0",
	}

	etcdSrv, err := startEtcd(srv, "testdb", etcdCfg)
	if err != nil {
		t.Fatalf("startEtcd failed: %v", err)
	}
	defer stopEtcd(etcdSrv)

	addr := srv.EtcdAddr("testdb")
	if addr == "" {
		t.Fatal("expected non-empty EtcdAddr for running database")
	}
	if addr != etcdSrv.Addr() {
		t.Fatalf("expected EtcdAddr %q, got %q", etcdSrv.Addr(), addr)
	}
}

// ---------------------------------------------------------------------------
// EtcdAddr: non-existent database returns empty string
// ---------------------------------------------------------------------------

func TestEtcdAddr_NotRunning(t *testing.T) {
	cfg := testConfig(false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	addr := srv.EtcdAddr("nonexistent")
	if addr != "" {
		t.Fatalf("expected empty EtcdAddr for non-existent database, got %q", addr)
	}
}
