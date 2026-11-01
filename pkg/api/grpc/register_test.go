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

//go:build !integration && !qrdb_ts

package grpc

import (
	"context"
	"testing"

	"google.golang.org/grpc"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// stubCredentialService is a non-nil CredentialServicer whose identity the
// option tests compare; none of its methods is called.
type stubCredentialService struct {
	service.CredentialServicer
}

// TestRegisterOptions_AttachServices asserts each WithRegister* service
// option stores exactly the dependency it was given on the server.
func TestRegisterOptions_AttachServices(t *testing.T) {
	mvcc := &mockMVCCService{}
	tx := &mockTransactionService{}
	batch := &mockBatchService{}
	watch := &mockWatchServiceImpl{}
	lse := &mockLeaseService{}
	idx := &mockIndexService{}
	ts := &mockTimeSeriesService{}
	barrier := newMockBarrierService()
	cer := newMockCeremonyService()
	db := newMockDatabaseService()
	certProv := &mockCertProvider{}
	coll := newMockCollectionService()
	cred := &stubCredentialService{}

	tests := []struct {
		name string
		opt  RegisterOption
		got  func(*qrdbServiceServer) any
		want any
	}{
		{"MVCC", WithRegisterMVCC(mvcc), func(s *qrdbServiceServer) any { return s.mvccSvc }, mvcc},
		{"Transaction", WithRegisterTransaction(tx), func(s *qrdbServiceServer) any { return s.txSvc }, tx},
		{"Batch", WithRegisterBatch(batch), func(s *qrdbServiceServer) any { return s.batchSvc }, batch},
		{"Watch", WithRegisterWatch(watch), func(s *qrdbServiceServer) any { return s.watchSvc }, watch},
		{"Lease", WithRegisterLease(lse), func(s *qrdbServiceServer) any { return s.leaseSvc }, lse},
		{"Index", WithRegisterIndex(idx), func(s *qrdbServiceServer) any { return s.indexSvc }, idx},
		{"TimeSeries", WithRegisterTimeSeries(ts), func(s *qrdbServiceServer) any { return s.tsSvc }, ts},
		{"Barrier", WithRegisterBarrierService(barrier), func(s *qrdbServiceServer) any { return s.barrierSvc }, barrier},
		{"Ceremony", WithRegisterCeremonyService(cer), func(s *qrdbServiceServer) any { return s.ceremonySvc }, cer},
		{"Database", WithRegisterDatabaseService(db), func(s *qrdbServiceServer) any { return s.databaseSvc }, db},
		{"CertProvider", WithRegisterCertProvider(certProv), func(s *qrdbServiceServer) any { return s.certProv }, certProv},
		{"Collection", WithRegisterCollectionService(coll), func(s *qrdbServiceServer) any { return s.collectionSvc }, coll},
		{"Credential", WithRegisterCredentialService(cred), func(s *qrdbServiceServer) any { return s.credentialSvc }, cred},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &qrdbServiceServer{}
			tt.opt(s)
			if got := tt.got(s); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// TestRegisterAuthFactorService_RegistersOnlyWithService asserts the
// AuthFactorService is registered for a non-nil service and skipped for nil.
func TestRegisterAuthFactorService_RegistersOnlyWithService(t *testing.T) {
	const name = "qrdb.v1.AuthFactorService"

	without := grpc.NewServer()
	RegisterAuthFactorService(without, nil)
	if _, ok := without.GetServiceInfo()[name]; ok {
		t.Fatalf("%s registered for a nil service", name)
	}

	with := grpc.NewServer()
	RegisterAuthFactorService(with, newMockAuthFactorService())
	if _, ok := with.GetServiceInfo()[name]; !ok {
		t.Fatalf("%s not registered for a non-nil service", name)
	}
}

// TestServer_WithAuthFactorService_ServesEnroll asserts a Server built with
// WithAuthFactorService answers AuthFactorService RPCs from that service.
func TestServer_WithAuthFactorService_ServesEnroll(t *testing.T) {
	tlsCfg := testServerTLSConfig(t)
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithTLS(tlsCfg), WithAuthFactorService(newMockAuthFactorService()))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient(srv.Addr(), grpc.WithTransportCredentials(testClientCreds(t, tlsCfg)))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	resp, err := pb.NewAuthFactorServiceClient(conn).EnrollFactor(context.Background(), &pb.EnrollFactorRequest{
		FactorName: "pkcs11", Subject: "alice", TenantId: "t1",
	})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}
	if resp.GetEnrollmentId() != "enroll-1" || resp.GetUserId() != "alice" || resp.GetTenantId() != "t1" {
		t.Fatalf("response = %+v", resp)
	}
}
