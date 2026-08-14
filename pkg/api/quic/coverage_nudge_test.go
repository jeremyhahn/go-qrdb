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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package quic

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// watchSvcFor resolver
// ---------------------------------------------------------------------------

func TestWatchSvcFor_Registry(t *testing.T) {
	reg := &recordingRegistry{}
	s := &Server{registry: reg}

	if _, err := s.watchSvcFor(scopedCtx("acme", "db2")); err != nil {
		t.Fatalf("watchSvcFor: %v", err)
	}
	if len(reg.calls) != 1 || reg.calls[0].service != "watch" {
		t.Fatalf("expected one watch registry call, got %+v", reg.calls)
	}
	if reg.calls[0].tenant != "acme" || reg.calls[0].db != "db2" {
		t.Errorf("call %+v: want tenant=acme db=db2", reg.calls[0])
	}
}

func TestWatchSvcFor_FallbackSingleton(t *testing.T) {
	ws := &mockWatchService{}
	s := &Server{watchSvc: ws}

	got, err := s.watchSvcFor(scopedCtx("acme", "db2"))
	if err != nil {
		t.Fatalf("watchSvcFor: %v", err)
	}
	if got != service.WatchServicer(ws) {
		t.Errorf("watchSvcFor returned %p, want singleton %p", got, ws)
	}
}

func TestWatchSvcFor_NotConfigured(t *testing.T) {
	s := &Server{}
	_, err := s.watchSvcFor(context.Background())
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("error type = %T, want *ServiceNotConfiguredError", err)
	}
}

// resolverProbe drives one resolver through its three branches: registry,
// singleton fallback, and not-configured error.
type resolverProbe struct {
	name        string
	registry    func(s *Server, ctx context.Context) error
	singleton   func() *Server
	resolveSing func(s *Server, ctx context.Context) error
	empty       func(s *Server, ctx context.Context) error
}

func TestResolversAllBranches(t *testing.T) {
	ctx := scopedCtx("acme", "db2")
	probes := []resolverProbe{
		{
			name:        "tx",
			registry:    func(s *Server, c context.Context) error { _, e := s.txSvcFor(c); return e },
			singleton:   func() *Server { return &Server{txSvc: &mockTransactionService{}} },
			resolveSing: func(s *Server, c context.Context) error { _, e := s.txSvcFor(c); return e },
			empty:       func(s *Server, c context.Context) error { _, e := s.txSvcFor(c); return e },
		},
		{
			name:        "batch",
			registry:    func(s *Server, c context.Context) error { _, e := s.batchSvcFor(c); return e },
			singleton:   func() *Server { return &Server{batchSvc: &mockBatchService{}} },
			resolveSing: func(s *Server, c context.Context) error { _, e := s.batchSvcFor(c); return e },
			empty:       func(s *Server, c context.Context) error { _, e := s.batchSvcFor(c); return e },
		},
		{
			name:        "lease",
			registry:    func(s *Server, c context.Context) error { _, e := s.leaseSvcFor(c); return e },
			singleton:   func() *Server { return &Server{leaseSvc: &mockLeaseService{}} },
			resolveSing: func(s *Server, c context.Context) error { _, e := s.leaseSvcFor(c); return e },
			empty:       func(s *Server, c context.Context) error { _, e := s.leaseSvcFor(c); return e },
		},
		{
			name:        "index",
			registry:    func(s *Server, c context.Context) error { _, e := s.indexSvcFor(c); return e },
			singleton:   func() *Server { return &Server{indexSvc: &mockIndexService{}} },
			resolveSing: func(s *Server, c context.Context) error { _, e := s.indexSvcFor(c); return e },
			empty:       func(s *Server, c context.Context) error { _, e := s.indexSvcFor(c); return e },
		},
		{
			name:        "vector",
			registry:    func(s *Server, c context.Context) error { _, e := s.vectorSvcFor(c); return e },
			singleton:   func() *Server { return &Server{vectorSvc: &mockVectorService{}} },
			resolveSing: func(s *Server, c context.Context) error { _, e := s.vectorSvcFor(c); return e },
			empty:       func(s *Server, c context.Context) error { _, e := s.vectorSvcFor(c); return e },
		},
		{
			name:        "mvcc",
			registry:    func(s *Server, c context.Context) error { _, e := s.mvccSvcFor(c); return e },
			singleton:   func() *Server { return &Server{mvccSvc: &mockMVCCKVService{}} },
			resolveSing: func(s *Server, c context.Context) error { _, e := s.mvccSvcFor(c); return e },
			empty:       func(s *Server, c context.Context) error { _, e := s.mvccSvcFor(c); return e },
		},
		{
			name:        "ts",
			registry:    func(s *Server, c context.Context) error { _, e := s.tsSvcFor(c); return e },
			singleton:   func() *Server { return &Server{tsSvc: &mockTimeSeriesService{}} },
			resolveSing: func(s *Server, c context.Context) error { _, e := s.tsSvcFor(c); return e },
			empty:       func(s *Server, c context.Context) error { _, e := s.tsSvcFor(c); return e },
		},
	}

	for _, p := range probes {
		t.Run(p.name+"/registry", func(t *testing.T) {
			s := &Server{registry: &recordingRegistry{}}
			if err := p.registry(s, ctx); err != nil {
				t.Fatalf("%s registry: %v", p.name, err)
			}
		})
		t.Run(p.name+"/singleton", func(t *testing.T) {
			s := p.singleton()
			if err := p.resolveSing(s, ctx); err != nil {
				t.Fatalf("%s singleton: %v", p.name, err)
			}
		})
		t.Run(p.name+"/notconfigured", func(t *testing.T) {
			s := &Server{}
			err := p.empty(s, context.Background())
			var snc *ServiceNotConfiguredError
			if !errors.As(err, &snc) {
				t.Fatalf("%s: error type = %T, want *ServiceNotConfiguredError", p.name, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// WithServiceRegistry option
// ---------------------------------------------------------------------------

func TestWithServiceRegistry_RoutesResolvers(t *testing.T) {
	reg := &recordingRegistry{kv: &mockKVService{}}
	srv := newTestServer(nil, &mockAdminService{}, WithServiceRegistry(reg))

	if srv.registry == nil {
		t.Fatal("WithServiceRegistry did not set the registry")
	}
	if _, err := srv.kvSvcFor(scopedCtx("acme", "db1")); err != nil {
		t.Fatalf("kvSvcFor: %v", err)
	}
	if len(reg.calls) != 1 || reg.calls[0].tenant != "acme" || reg.calls[0].db != "db1" {
		t.Fatalf("expected resolver to route through registry, got %+v", reg.calls)
	}
}

// ---------------------------------------------------------------------------
// handleTenantUpdateEntitlements
// ---------------------------------------------------------------------------

func TestHandleTenantUpdateEntitlements_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.TenantUpdateEntitlementsRequest{
		Id:           "acme",
		Entitlements: &pb.TenantEntitlements{AllowDedicatedRaft: true},
	})

	_, err := srv.handleTenantUpdateEntitlements(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleTenantUpdateEntitlements_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(&mockTenantService{}))

	_, err := srv.handleTenantUpdateEntitlements(context.Background(), []byte{0xff, 0xfe})
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

func TestHandleTenantUpdateEntitlements_NilEntitlements(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(&mockTenantService{}))
	payload, _ := proto.Marshal(&pb.TenantUpdateEntitlementsRequest{Id: "acme"})

	_, err := srv.handleTenantUpdateEntitlements(context.Background(), payload)
	var ee *tenantEntitlementError
	if !errors.As(err, &ee) {
		t.Fatalf("expected *tenantEntitlementError, got %T", err)
	}
	if ee.Error() == "" {
		t.Error("expected non-empty error message")
	}
}

func TestHandleTenantUpdateEntitlements_Success(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(&mockTenantService{}))
	payload, _ := proto.Marshal(&pb.TenantUpdateEntitlementsRequest{
		Id: "acme",
		Entitlements: &pb.TenantEntitlements{
			AllowedIsolationModes: []string{"barrier", "prefix"},
			AllowDedicatedRaft:    true,
			AllowVolumeStorage:    true,
		},
	})

	resp, err := srv.handleTenantUpdateEntitlements(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var tenantResp pb.TenantResponse
	if err := proto.Unmarshal(resp, &tenantResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if tenantResp.GetId() != "acme" {
		t.Errorf("expected id 'acme', got %q", tenantResp.GetId())
	}
}

// TestHandleTenantUpdateEntitlements_BarrierModesAndWALRoundTrip is a
// regression test for the QUIC entitlements round-trip bug: AllowedBarrierModes
// and AllowDedicatedWal were dropped both on decode (handleTenantUpdateEntitlements)
// and on encode (tenantInfoToProto), so a `tenant set-entitlement
// --allowed-barrier-modes ...` call followed by `tenant get` over QUIC always
// reported an empty list even though the value round-trips correctly on
// REST/gRPC/Unix. mockTenantService.UpdateEntitlements echoes back exactly the
// *quota.Entitlements it was handed, so this test exercises the full
// decode -> service -> encode path.
func TestHandleTenantUpdateEntitlements_BarrierModesAndWALRoundTrip(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(&mockTenantService{}))
	payload, _ := proto.Marshal(&pb.TenantUpdateEntitlementsRequest{
		Id: "acme",
		Entitlements: &pb.TenantEntitlements{
			AllowedBarrierModes: []string{"none", "passphrase", "shamir"},
			AllowDedicatedWal:   true,
		},
	})

	resp, err := srv.handleTenantUpdateEntitlements(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var tenantResp pb.TenantResponse
	if err := proto.Unmarshal(resp, &tenantResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if tenantResp.GetEntitlements() == nil {
		t.Fatal("expected non-nil entitlements in response")
	}
	got := tenantResp.GetEntitlements().GetAllowedBarrierModes()
	want := []string{"none", "passphrase", "shamir"}
	if len(got) != len(want) {
		t.Fatalf("expected AllowedBarrierModes %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected AllowedBarrierModes %v, got %v", want, got)
		}
	}
	if !tenantResp.GetEntitlements().GetAllowDedicatedWal() {
		t.Error("expected AllowDedicatedWal true in response")
	}
}
