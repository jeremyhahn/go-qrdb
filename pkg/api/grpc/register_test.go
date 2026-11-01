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
	"testing"

	"google.golang.org/grpc"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// Non-nil dependencies whose identity the option tests compare; none of
// their methods is called.
type (
	stubBarrierService     struct{ service.BarrierServicer }
	stubCeremonyService    struct{ service.CeremonyServicer }
	stubDatabaseService    struct{ service.DatabaseServicer }
	stubCollectionService  struct{ service.CollectionServicer }
	stubCredentialService  struct{ service.CredentialServicer }
	stubEncryptionMigrator struct{ service.EncryptionMigrator }
	stubDEKService         struct{ service.DEKServicer }
	stubCertEnrollment     struct{ service.CertEnrollmentServicer }
	stubInvitationService  struct{ service.InvitationServicer }
	stubTenantOnboarding   struct{ service.TenantOnboardingServicer }
	stubSQLService         struct{ service.SQLServicer }
	stubServiceRegistry    struct{ service.Registry }
)

// TestRegisterOptions_AttachServices asserts each WithRegister* option
// stores exactly the dependency it was given on the server.
func TestRegisterOptions_AttachServices(t *testing.T) {
	mvcc := &mockMVCCService{}
	tx := &mockTransactionService{}
	batch := &mockBatchService{}
	watch := &mockWatchServiceImpl{}
	lse := &mockLeaseService{}
	idx := &mockIndexService{}
	ts := &mockTimeSeriesService{}
	barrier := &stubBarrierService{}
	cer := &stubCeremonyService{}
	db := &stubDatabaseService{}
	certProv := &mockCertProvider{}
	coll := &stubCollectionService{}
	cred := &stubCredentialService{}
	encMigrator := &stubEncryptionMigrator{}
	dekSvc := &stubDEKService{}
	certEnroll := &stubCertEnrollment{}
	invitationSvc := &stubInvitationService{}
	onboarding := &stubTenantOnboarding{}
	mw := &authmw.AuthMiddleware{}
	sqlSvc := &stubSQLService{}
	registry := &stubServiceRegistry{}

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
		{"EncryptionMigrator", WithRegisterEncryptionMigrator(encMigrator), func(s *qrdbServiceServer) any { return s.encMigrator }, encMigrator},
		{"DEK", WithRegisterDEKService(dekSvc), func(s *qrdbServiceServer) any { return s.dekSvc }, dekSvc},
		{"CertEnrollment", WithRegisterCertEnrollment(certEnroll), func(s *qrdbServiceServer) any { return s.certEnroll }, certEnroll},
		{"Invitation", WithRegisterInvitationService(invitationSvc), func(s *qrdbServiceServer) any { return s.invitationSvc }, invitationSvc},
		{"TenantOnboarding", WithRegisterTenantOnboarding(onboarding), func(s *qrdbServiceServer) any { return s.onboarding }, onboarding},
		{"AuthMiddleware", WithRegisterAuthMiddleware(mw), func(s *qrdbServiceServer) any { return s.authMW }, mw},
		{"SQL", WithRegisterSQLService(sqlSvc), func(s *qrdbServiceServer) any { return s.sqlSvc }, sqlSvc},
		{"ServiceRegistry", WithRegisterServiceRegistry(registry), func(s *qrdbServiceServer) any { return s.registry }, registry},
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

// TestRegisterService_RegistersQRDBService asserts RegisterService puts the
// QRDBService on the gRPC server when optional services are attached.
func TestRegisterService_RegistersQRDBService(t *testing.T) {
	grpcSrv := grpc.NewServer()
	defer grpcSrv.Stop()
	RegisterService(grpcSrv, newMockKVService(), newMockAdminService(),
		WithRegisterEncryptionMigrator(&stubEncryptionMigrator{}),
		WithRegisterDEKService(&stubDEKService{}),
		WithRegisterCertEnrollment(&stubCertEnrollment{}),
		WithRegisterInvitationService(&stubInvitationService{}),
		WithRegisterTenantOnboarding(&stubTenantOnboarding{}),
		WithRegisterAuthMiddleware(&authmw.AuthMiddleware{}),
		WithRegisterSQLService(&stubSQLService{}),
		WithRegisterServiceRegistry(&stubServiceRegistry{}),
	)
	if _, ok := grpcSrv.GetServiceInfo()[pb.QRDBService_ServiceDesc.ServiceName]; !ok {
		t.Fatalf("QRDBService not registered: %v", grpcSrv.GetServiceInfo())
	}
}
