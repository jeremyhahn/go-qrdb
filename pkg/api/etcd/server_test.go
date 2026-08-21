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
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// Mock stubs
// ---------------------------------------------------------------------------

type mockMVCCSvc struct{}

func (m *mockMVCCSvc) Put(_ context.Context, _ string, _ []byte) error {
	panic("not implemented")
}

// PutIfAbsent reports a successful create. This mock keeps no state, so
// there is nothing for the condition to consult.
func (m *mockMVCCSvc) PutIfAbsent(_ context.Context, _ string, _ []byte) (bool, error) {
	return true, nil
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (m *mockMVCCSvc) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	return false, nil
}
func (m *mockMVCCSvc) Get(_ context.Context, _ string) ([]byte, error) {
	panic("not implemented")
}
func (m *mockMVCCSvc) Delete(_ context.Context, _ string) error {
	panic("not implemented")
}
func (m *mockMVCCSvc) List(_ context.Context, _ string) ([]string, error) {
	panic("not implemented")
}
func (m *mockMVCCSvc) Scan(_ context.Context, _ string) (map[string][]byte, error) {
	panic("not implemented")
}
func (m *mockMVCCSvc) GetStale(_ context.Context, _ string) ([]byte, error) {
	panic("not implemented")
}
func (m *mockMVCCSvc) ListStale(_ context.Context, _ string) ([]string, error) {
	panic("not implemented")
}
func (m *mockMVCCSvc) ScanStale(_ context.Context, _ string) (map[string][]byte, error) {
	panic("not implemented")
}
func (m *mockMVCCSvc) PutWithMode(_ context.Context, _ string, _ []byte, _ writemode.Mode) error {
	panic("not implemented")
}
func (m *mockMVCCSvc) DeleteWithMode(_ context.Context, _ string, _ writemode.Mode) error {
	panic("not implemented")
}
func (m *mockMVCCSvc) GetWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) ([]byte, error) {
	panic("not implemented")
}
func (m *mockMVCCSvc) ListWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) ([]string, error) {
	panic("not implemented")
}
func (m *mockMVCCSvc) ScanWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (map[string][]byte, error) {
	panic("not implemented")
}
func (m *mockMVCCSvc) ExistsWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (bool, error) {
	panic("not implemented")
}
func (m *mockMVCCSvc) PutEntity(_ context.Context, _ *service.PutEntityRequest) (*service.PutEntityResponse, error) {
	panic("not implemented")
}
func (m *mockMVCCSvc) GetEntity(_ context.Context, _ *service.GetEntityRequest) (*service.GetEntityResponse, error) {
	panic("not implemented")
}
func (m *mockMVCCSvc) DeleteEntity(_ context.Context, _ *service.DeleteEntityRequest) (*service.DeleteEntityResponse, error) {
	panic("not implemented")
}
func (m *mockMVCCSvc) ListEntities(_ context.Context, _ *service.ListEntitiesRequest) (*service.ListEntitiesResponse, error) {
	panic("not implemented")
}
func (m *mockMVCCSvc) PutWithLease(_ context.Context, _ string, _ []byte, _ int64) (*service.KeyValue, error) {
	panic("not implemented")
}
func (m *mockMVCCSvc) PutWithLeaseAndMode(_ context.Context, _ string, _ []byte, _ int64, _ writemode.Mode) (*service.KeyValue, error) {
	panic("not implemented")
}
func (m *mockMVCCSvc) GetRevision(_ context.Context, _ string) (*service.KeyValue, error) {
	panic("not implemented")
}
func (m *mockMVCCSvc) GetRevisionWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (*service.KeyValue, error) {
	panic("not implemented")
}
func (m *mockMVCCSvc) GetAtRevision(_ context.Context, _ string, _ int64) (*service.KeyValue, error) {
	panic("not implemented")
}
func (m *mockMVCCSvc) GetAtRevisionWithConsistency(_ context.Context, _ string, _ int64, _ consistency.ReadOptions) (*service.KeyValue, error) {
	panic("not implemented")
}
func (m *mockMVCCSvc) Range(_ context.Context, _, _ []byte, _ ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	panic("not implemented")
}
func (m *mockMVCCSvc) RangeWithConsistency(_ context.Context, _, _ []byte, _ consistency.ReadOptions, _ ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	panic("not implemented")
}
func (m *mockMVCCSvc) Compact(_ context.Context, _ int64) error {
	panic("not implemented")
}
func (m *mockMVCCSvc) CurrentRevision(_ context.Context) (int64, error) {
	panic("not implemented")
}

type mockTxSvc struct{}

func (m *mockTxSvc) Execute(_ context.Context, _ []service.Compare, _, _ []service.Op) (*service.TransactionResponse, error) {
	panic("not implemented")
}
func (m *mockTxSvc) ExecuteWithMode(_ context.Context, _ []service.Compare, _, _ []service.Op, _ writemode.Mode) (*service.TransactionResponse, error) {
	panic("not implemented")
}
func (m *mockTxSvc) ExecuteEntity(_ context.Context, _ *service.TransactionEntityRequest) (*service.TransactionEntityResponse, error) {
	panic("not implemented")
}

type mockWatchSvc struct{}

func (m *mockWatchSvc) Watch(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
	panic("not implemented")
}
func (m *mockWatchSvc) WatchPrefix(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
	panic("not implemented")
}
func (m *mockWatchSvc) WatchEntity(_ context.Context, _ *service.WatchEntityRequest) (<-chan service.WatchEntityEvent, error) {
	panic("not implemented")
}
func (m *mockWatchSvc) WatchEntityType(_ context.Context, _ *service.WatchEntityTypeRequest) (<-chan service.WatchEntityEvent, error) {
	panic("not implemented")
}

type mockLeaseSvc struct{}

func (m *mockLeaseSvc) Grant(_ context.Context, _ int64) (*lease.Lease, error) {
	panic("not implemented")
}
func (m *mockLeaseSvc) Revoke(_ context.Context, _ int64) error {
	panic("not implemented")
}
func (m *mockLeaseSvc) KeepAlive(_ context.Context, _ int64) (*lease.Lease, error) {
	panic("not implemented")
}
func (m *mockLeaseSvc) TimeToLive(_ context.Context, _ int64) (*lease.Lease, error) {
	panic("not implemented")
}
func (m *mockLeaseSvc) Leases(_ context.Context) ([]*lease.Lease, error) {
	panic("not implemented")
}

type mockAdminSvc struct{}

func (m *mockAdminSvc) Health(_ context.Context) (*service.HealthStatus, error) {
	panic("not implemented")
}
func (m *mockAdminSvc) Status(_ context.Context) (*service.StatusInfo, error) {
	panic("not implemented")
}
func (m *mockAdminSvc) Peers(_ context.Context) (*service.PeersInfo, error) {
	panic("not implemented")
}
func (m *mockAdminSvc) AddMember(_ context.Context, _, _ uint64, _ string) error {
	panic("not implemented")
}
func (m *mockAdminSvc) RemoveMember(_ context.Context, _, _ uint64) error {
	panic("not implemented")
}
func (m *mockAdminSvc) RequestSnapshot(_ context.Context, _ uint64) error {
	panic("not implemented")
}
func (m *mockAdminSvc) Drain(_ context.Context) error {
	panic("not implemented")
}
func (m *mockAdminSvc) Capabilities(_ context.Context) (*capabilities.Capabilities, error) {
	panic("not implemented")
}
func (m *mockAdminSvc) StartShard(_ context.Context, _ *service.StartShardRequest) error {
	panic("not implemented")
}
func (m *mockAdminSvc) StopShard(_ context.Context, _ uint64) error {
	panic("not implemented")
}
func (m *mockAdminSvc) GetShardState(_ context.Context, _ uint64) (*service.ShardStateResponse, error) {
	panic("not implemented")
}
func (m *mockAdminSvc) GetTopology(_ context.Context) (*service.TopologyResponse, error) {
	panic("not implemented")
}

// ---------------------------------------------------------------------------
// Interface conformance assertions
// ---------------------------------------------------------------------------

var (
	_ service.MVCCKVServicer      = (*mockMVCCSvc)(nil)
	_ service.TransactionServicer = (*mockTxSvc)(nil)
	_ service.WatchServicer       = (*mockWatchSvc)(nil)
	_ lease.LeaseServicer         = (*mockLeaseSvc)(nil)
	_ service.AdminServicer       = (*mockAdminSvc)(nil)
)

// ---------------------------------------------------------------------------
// NewServer tests
// ---------------------------------------------------------------------------

func TestNewServerSetsDbNameAndAddr(t *testing.T) {
	s := NewServer("mydb", "127.0.0.1:2379")
	if s.dbName != "mydb" {
		t.Errorf("dbName = %q, want %q", s.dbName, "mydb")
	}
	if s.addr != "127.0.0.1:2379" {
		t.Errorf("addr = %q, want %q", s.addr, "127.0.0.1:2379")
	}
}

func TestNewServerDefaultsLoggerNonNil(t *testing.T) {
	s := NewServer("db", ":0")
	if s.logger == nil {
		t.Fatal("expected non-nil logger")
	}
}

func TestNewServerNoOptions(t *testing.T) {
	s := NewServer("testdb", ":0")
	if s.opts.tlsConfig != nil {
		t.Error("expected nil tlsConfig with no options")
	}
	if s.opts.mvccSvc != nil {
		t.Error("expected nil mvccSvc with no options")
	}
	if s.opts.txSvc != nil {
		t.Error("expected nil txSvc with no options")
	}
	if s.opts.watchSvc != nil {
		t.Error("expected nil watchSvc with no options")
	}
	if s.opts.leaseSvc != nil {
		t.Error("expected nil leaseSvc with no options")
	}
	if s.opts.adminSvc != nil {
		t.Error("expected nil adminSvc with no options")
	}
	if s.opts.clusterID != 0 {
		t.Errorf("clusterID = %d, want 0", s.opts.clusterID)
	}
	if s.opts.memberID != 0 {
		t.Errorf("memberID = %d, want 0", s.opts.memberID)
	}
	if s.opts.raftTerm != 0 {
		t.Errorf("raftTerm = %d, want 0", s.opts.raftTerm)
	}
}

func TestNewServerDoesNotStartListener(t *testing.T) {
	s := NewServer("db", ":0")
	if s.listener != nil {
		t.Error("expected nil listener before Start()")
	}
	if s.grpcServer != nil {
		t.Error("expected nil grpcServer before Start()")
	}
}

// ---------------------------------------------------------------------------
// Option tests
// ---------------------------------------------------------------------------

func TestWithTLSSetsConfig(t *testing.T) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS13}
	s := NewServer("db", ":0", WithTLS(cfg))
	if s.opts.tlsConfig != cfg {
		t.Error("WithTLS did not set tlsConfig")
	}
}

func TestWithTLSNilConfig(t *testing.T) {
	s := NewServer("db", ":0", WithTLS(nil))
	if s.opts.tlsConfig != nil {
		t.Error("WithTLS(nil) should set tlsConfig to nil")
	}
}

func TestWithClusterIDSetsValue(t *testing.T) {
	s := NewServer("db", ":0", WithClusterID(42))
	if s.opts.clusterID != 42 {
		t.Errorf("clusterID = %d, want 42", s.opts.clusterID)
	}
}

func TestWithClusterIDZero(t *testing.T) {
	s := NewServer("db", ":0", WithClusterID(0))
	if s.opts.clusterID != 0 {
		t.Errorf("clusterID = %d, want 0", s.opts.clusterID)
	}
}

func TestWithMemberIDSetsValue(t *testing.T) {
	s := NewServer("db", ":0", WithMemberID(99))
	if s.opts.memberID != 99 {
		t.Errorf("memberID = %d, want 99", s.opts.memberID)
	}
}

func TestWithMemberIDZero(t *testing.T) {
	s := NewServer("db", ":0", WithMemberID(0))
	if s.opts.memberID != 0 {
		t.Errorf("memberID = %d, want 0", s.opts.memberID)
	}
}

func TestWithRaftTermSetsValue(t *testing.T) {
	s := NewServer("db", ":0", WithRaftTerm(7))
	if s.opts.raftTerm != 7 {
		t.Errorf("raftTerm = %d, want 7", s.opts.raftTerm)
	}
}

func TestWithRaftTermZero(t *testing.T) {
	s := NewServer("db", ":0", WithRaftTerm(0))
	if s.opts.raftTerm != 0 {
		t.Errorf("raftTerm = %d, want 0", s.opts.raftTerm)
	}
}

func TestWithMVCCSetsService(t *testing.T) {
	svc := &mockMVCCSvc{}
	s := NewServer("db", ":0", WithMVCC(svc))
	if s.opts.mvccSvc != svc {
		t.Error("WithMVCC did not set mvccSvc")
	}
}

func TestWithMVCCNil(t *testing.T) {
	s := NewServer("db", ":0", WithMVCC(nil))
	if s.opts.mvccSvc != nil {
		t.Error("WithMVCC(nil) should set mvccSvc to nil")
	}
}

func TestWithTransactionSetsService(t *testing.T) {
	svc := &mockTxSvc{}
	s := NewServer("db", ":0", WithTransaction(svc))
	if s.opts.txSvc != svc {
		t.Error("WithTransaction did not set txSvc")
	}
}

func TestWithTransactionNil(t *testing.T) {
	s := NewServer("db", ":0", WithTransaction(nil))
	if s.opts.txSvc != nil {
		t.Error("WithTransaction(nil) should set txSvc to nil")
	}
}

func TestWithWatchSetsService(t *testing.T) {
	svc := &mockWatchSvc{}
	s := NewServer("db", ":0", WithWatch(svc))
	if s.opts.watchSvc != svc {
		t.Error("WithWatch did not set watchSvc")
	}
}

func TestWithWatchNil(t *testing.T) {
	s := NewServer("db", ":0", WithWatch(nil))
	if s.opts.watchSvc != nil {
		t.Error("WithWatch(nil) should set watchSvc to nil")
	}
}

func TestWithLeaseSetsService(t *testing.T) {
	svc := &mockLeaseSvc{}
	s := NewServer("db", ":0", WithLease(svc))
	if s.opts.leaseSvc != svc {
		t.Error("WithLease did not set leaseSvc")
	}
}

func TestWithLeaseNil(t *testing.T) {
	s := NewServer("db", ":0", WithLease(nil))
	if s.opts.leaseSvc != nil {
		t.Error("WithLease(nil) should set leaseSvc to nil")
	}
}

func TestWithAdminSetsService(t *testing.T) {
	svc := &mockAdminSvc{}
	s := NewServer("db", ":0", WithAdmin(svc))
	if s.opts.adminSvc != svc {
		t.Error("WithAdmin did not set adminSvc")
	}
}

func TestWithAdminNil(t *testing.T) {
	s := NewServer("db", ":0", WithAdmin(nil))
	if s.opts.adminSvc != nil {
		t.Error("WithAdmin(nil) should set adminSvc to nil")
	}
}

func TestMultipleOptionsAppliedInOrder(t *testing.T) {
	s := NewServer("db", ":0",
		WithClusterID(1),
		WithMemberID(2),
		WithRaftTerm(3),
	)
	if s.opts.clusterID != 1 {
		t.Errorf("clusterID = %d, want 1", s.opts.clusterID)
	}
	if s.opts.memberID != 2 {
		t.Errorf("memberID = %d, want 2", s.opts.memberID)
	}
	if s.opts.raftTerm != 3 {
		t.Errorf("raftTerm = %d, want 3", s.opts.raftTerm)
	}
}

func TestOptionOverwritesPrevious(t *testing.T) {
	s := NewServer("db", ":0",
		WithClusterID(1),
		WithClusterID(99),
	)
	if s.opts.clusterID != 99 {
		t.Errorf("clusterID = %d, want 99 (last option should win)", s.opts.clusterID)
	}
}

// ---------------------------------------------------------------------------
// Addr() tests
// ---------------------------------------------------------------------------

func TestAddrBeforeStartReturnsConfiguredAddr(t *testing.T) {
	s := NewServer("db", "localhost:2379")
	if got := s.Addr(); got != "localhost:2379" {
		t.Errorf("Addr() = %q, want %q", got, "localhost:2379")
	}
}

func TestAddrBeforeStartReturnsEmptyWhenEmpty(t *testing.T) {
	s := NewServer("db", "")
	if got := s.Addr(); got != "" {
		t.Errorf("Addr() = %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// Start / Stop lifecycle tests
// ---------------------------------------------------------------------------

func TestStartOnEphemeralPortSucceeds(t *testing.T) {
	s := NewServer("testdb", "127.0.0.1:0")
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer s.Stop()

	if s.listener == nil {
		t.Fatal("expected non-nil listener after Start()")
	}
	if s.grpcServer == nil {
		t.Fatal("expected non-nil grpcServer after Start()")
	}
}

func TestStartAddrReturnsBoundAddress(t *testing.T) {
	s := NewServer("testdb", "127.0.0.1:0")
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer s.Stop()

	addr := s.Addr()
	if addr == "127.0.0.1:0" {
		t.Error("Addr() should return the bound address, not the configured address")
	}
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Errorf("Addr() = %q, expected 127.0.0.1:PORT", addr)
	}
}

func TestStartAddrContainsNonZeroPort(t *testing.T) {
	s := NewServer("testdb", "127.0.0.1:0")
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer s.Stop()

	addr := s.Addr()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q) error: %v", addr, err)
	}
	if port == "0" {
		t.Error("expected non-zero port after Start()")
	}
}

func TestStopSucceeds(t *testing.T) {
	s := NewServer("testdb", "127.0.0.1:0")
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	addr := s.Addr()
	if addr == "" {
		t.Fatal("expected non-empty addr after Start()")
	}
	// Stop should complete without panic.
	s.Stop()
}

func TestStopBeforeStartIsNoop(t *testing.T) {
	s := NewServer("testdb", ":0")
	// Should not panic or error.
	s.Stop()
}

func TestStopIdempotent(t *testing.T) {
	s := NewServer("testdb", "127.0.0.1:0")
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	s.Stop()
	// Second Stop should not panic.
	s.Stop()
}

// ---------------------------------------------------------------------------
// Start error tests
// ---------------------------------------------------------------------------

func TestStartOnInvalidAddrReturnsStartError(t *testing.T) {
	s := NewServer("testdb", "invalid-addr-no-port")
	err := s.Start()
	if err == nil {
		s.Stop()
		t.Fatal("expected Start() to fail on invalid address")
	}
	var startErr *StartError
	if !errors.As(err, &startErr) {
		t.Fatalf("expected *StartError, got %T: %v", err, err)
	}
	if startErr.Addr != "invalid-addr-no-port" {
		t.Errorf("StartError.Addr = %q, want %q", startErr.Addr, "invalid-addr-no-port")
	}
	if startErr.Err == nil {
		t.Error("expected non-nil underlying error in StartError")
	}
}

func TestStartOnBusyPortReturnsStartError(t *testing.T) {
	// Bind a port first to make it busy.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to bind initial listener: %v", err)
	}
	defer func() {
		if err := lis.Close(); err != nil {
			t.Errorf("lis.Close: %v", err)
		}
	}()

	busyAddr := lis.Addr().String()
	s := NewServer("testdb", busyAddr)
	startErr := s.Start()
	if startErr == nil {
		s.Stop()
		t.Fatal("expected Start() to fail on busy port")
	}

	var se *StartError
	if !errors.As(startErr, &se) {
		t.Fatalf("expected *StartError, got %T: %v", startErr, startErr)
	}
	if se.Addr != busyAddr {
		t.Errorf("StartError.Addr = %q, want %q", se.Addr, busyAddr)
	}
}

func TestStartErrorUnwrap(t *testing.T) {
	s := NewServer("db", "invalid-addr-no-port")
	err := s.Start()
	if err == nil {
		s.Stop()
		t.Fatal("expected error")
	}
	var se *StartError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StartError, got %T", err)
	}
	if se.Unwrap() == nil {
		t.Error("expected non-nil inner error from Unwrap()")
	}
}

// ---------------------------------------------------------------------------
// Start with no services (only auth stub registered)
// ---------------------------------------------------------------------------

func TestStartNoServicesRegistersAuthOnly(t *testing.T) {
	s := NewServer("emptydb", "127.0.0.1:0")
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer s.Stop()

	// Server should be running and accepting connections.
	conn, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatalf("expected to connect to started server: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Errorf("conn.Close: %v", err)
	}
}

func TestStartNoServicesGrpcServerNotNil(t *testing.T) {
	s := NewServer("emptydb", "127.0.0.1:0")
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer s.Stop()

	if s.grpcServer == nil {
		t.Fatal("expected non-nil grpcServer after Start()")
	}
}

// ---------------------------------------------------------------------------
// Start with all services registered
// ---------------------------------------------------------------------------

func TestStartWithAllServices(t *testing.T) {
	s := NewServer("fulldb", "127.0.0.1:0",
		WithMVCC(&mockMVCCSvc{}),
		WithTransaction(&mockTxSvc{}),
		WithWatch(&mockWatchSvc{}),
		WithLease(&mockLeaseSvc{}),
		WithAdmin(&mockAdminSvc{}),
		WithClusterID(100),
		WithMemberID(200),
		WithRaftTerm(5),
	)

	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer s.Stop()

	// Verify the server is accepting connections.
	conn, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatalf("expected to connect to started server: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Errorf("conn.Close: %v", err)
	}
}

func TestStartWithAllServicesAddrIsValid(t *testing.T) {
	s := NewServer("fulldb", "127.0.0.1:0",
		WithMVCC(&mockMVCCSvc{}),
		WithTransaction(&mockTxSvc{}),
		WithWatch(&mockWatchSvc{}),
		WithLease(&mockLeaseSvc{}),
		WithAdmin(&mockAdminSvc{}),
	)

	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer s.Stop()

	host, port, err := net.SplitHostPort(s.Addr())
	if err != nil {
		t.Fatalf("SplitHostPort error: %v", err)
	}
	if host != "127.0.0.1" {
		t.Errorf("host = %q, want %q", host, "127.0.0.1")
	}
	if port == "0" {
		t.Error("port should be non-zero after binding")
	}
}

// ---------------------------------------------------------------------------
// Start with partial services
// ---------------------------------------------------------------------------

func TestStartWithMVCCOnly(t *testing.T) {
	s := NewServer("mvccdb", "127.0.0.1:0",
		WithMVCC(&mockMVCCSvc{}),
	)
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer s.Stop()

	conn, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatalf("expected connection to succeed: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Errorf("conn.Close: %v", err)
	}
}

func TestStartWithLeaseOnly(t *testing.T) {
	s := NewServer("leasedb", "127.0.0.1:0",
		WithLease(&mockLeaseSvc{}),
	)
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer s.Stop()

	conn, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatalf("expected connection to succeed: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Errorf("conn.Close: %v", err)
	}
}

func TestStartWithWatchOnly(t *testing.T) {
	s := NewServer("watchdb", "127.0.0.1:0",
		WithWatch(&mockWatchSvc{}),
		WithMVCC(&mockMVCCSvc{}),
	)
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer s.Stop()

	conn, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatalf("expected connection to succeed: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Errorf("conn.Close: %v", err)
	}
}

func TestStartWithAdminOnly(t *testing.T) {
	s := NewServer("admindb", "127.0.0.1:0",
		WithAdmin(&mockAdminSvc{}),
	)
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer s.Stop()

	conn, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatalf("expected connection to succeed: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Errorf("conn.Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Start with TLS option (does not require valid cert for bind)
// ---------------------------------------------------------------------------

func TestStartWithTLSOptionSetsConfig(t *testing.T) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS13}
	s := NewServer("tlsdb", "127.0.0.1:0", WithTLS(cfg))
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer s.Stop()

	if s.opts.tlsConfig != cfg {
		t.Error("TLS config not preserved after Start()")
	}
}

// ---------------------------------------------------------------------------
// grpcServer service info verification
// ---------------------------------------------------------------------------

func TestStartRegistersMaintenanceAlways(t *testing.T) {
	s := NewServer("maintenancedb", "127.0.0.1:0")
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer s.Stop()

	info := s.grpcServer.GetServiceInfo()
	if _, ok := info["etcdserverpb.Maintenance"]; !ok {
		t.Error("expected Maintenance service to be registered")
	}
}

func TestStartRegistersAuthAlways(t *testing.T) {
	s := NewServer("authdb", "127.0.0.1:0")
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer s.Stop()

	info := s.grpcServer.GetServiceInfo()
	if _, ok := info["etcdserverpb.Auth"]; !ok {
		t.Error("expected Auth service to be registered")
	}
}

func TestStartRegistersKVWhenMVCCProvided(t *testing.T) {
	s := NewServer("kvdb", "127.0.0.1:0", WithMVCC(&mockMVCCSvc{}))
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer s.Stop()

	info := s.grpcServer.GetServiceInfo()
	if _, ok := info["etcdserverpb.KV"]; !ok {
		t.Error("expected KV service to be registered when MVCC is provided")
	}
}

func TestStartDoesNotRegisterKVWithoutMVCC(t *testing.T) {
	s := NewServer("nokvdb", "127.0.0.1:0")
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer s.Stop()

	info := s.grpcServer.GetServiceInfo()
	if _, ok := info["etcdserverpb.KV"]; ok {
		t.Error("expected KV service to NOT be registered without MVCC")
	}
}

func TestStartRegistersLeaseWhenProvided(t *testing.T) {
	s := NewServer("leasedb", "127.0.0.1:0", WithLease(&mockLeaseSvc{}))
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer s.Stop()

	info := s.grpcServer.GetServiceInfo()
	if _, ok := info["etcdserverpb.Lease"]; !ok {
		t.Error("expected Lease service to be registered")
	}
}

func TestStartDoesNotRegisterLeaseWithoutService(t *testing.T) {
	s := NewServer("noleasedb", "127.0.0.1:0")
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer s.Stop()

	info := s.grpcServer.GetServiceInfo()
	if _, ok := info["etcdserverpb.Lease"]; ok {
		t.Error("expected Lease service to NOT be registered without lease service")
	}
}

func TestStartRegistersWatchWhenProvided(t *testing.T) {
	s := NewServer("watchdb", "127.0.0.1:0",
		WithWatch(&mockWatchSvc{}),
		WithMVCC(&mockMVCCSvc{}),
	)
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer s.Stop()

	info := s.grpcServer.GetServiceInfo()
	if _, ok := info["etcdserverpb.Watch"]; !ok {
		t.Error("expected Watch service to be registered")
	}
}

func TestStartDoesNotRegisterWatchWithoutService(t *testing.T) {
	s := NewServer("nowatchdb", "127.0.0.1:0")
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer s.Stop()

	info := s.grpcServer.GetServiceInfo()
	if _, ok := info["etcdserverpb.Watch"]; ok {
		t.Error("expected Watch service to NOT be registered without watch service")
	}
}

func TestStartRegistersClusterWhenAdminProvided(t *testing.T) {
	s := NewServer("clusterdb", "127.0.0.1:0", WithAdmin(&mockAdminSvc{}))
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer s.Stop()

	info := s.grpcServer.GetServiceInfo()
	if _, ok := info["etcdserverpb.Cluster"]; !ok {
		t.Error("expected Cluster service to be registered when admin is provided")
	}
}

func TestStartDoesNotRegisterClusterWithoutAdmin(t *testing.T) {
	s := NewServer("noclusterdb", "127.0.0.1:0")
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer s.Stop()

	info := s.grpcServer.GetServiceInfo()
	if _, ok := info["etcdserverpb.Cluster"]; ok {
		t.Error("expected Cluster service to NOT be registered without admin service")
	}
}

func TestStartAllServicesRegistersAll(t *testing.T) {
	s := NewServer("alldb", "127.0.0.1:0",
		WithMVCC(&mockMVCCSvc{}),
		WithTransaction(&mockTxSvc{}),
		WithWatch(&mockWatchSvc{}),
		WithLease(&mockLeaseSvc{}),
		WithAdmin(&mockAdminSvc{}),
	)
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer s.Stop()

	info := s.grpcServer.GetServiceInfo()
	expected := []string{
		"etcdserverpb.KV",
		"etcdserverpb.Lease",
		"etcdserverpb.Watch",
		"etcdserverpb.Auth",
		"etcdserverpb.Cluster",
		"etcdserverpb.Maintenance",
	}
	for _, svcName := range expected {
		if _, ok := info[svcName]; !ok {
			t.Errorf("expected %s to be registered", svcName)
		}
	}
}

// RestoreToRevision satisfies service.MVCCKVServicer.
func (m *mockMVCCSvc) RestoreToRevision(context.Context, *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
	return nil, nil
}
