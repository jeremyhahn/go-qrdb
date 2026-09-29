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

package grpc

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/internal/collectionpb"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// ---------------------------------------------------------------------------
// Mock BarrierServicer
// ---------------------------------------------------------------------------

type mockBarrierService struct {
	initErr            error
	unsealErr          error
	sealErr            error
	statusResult       *service.BarrierStatusInfo
	statusErr          error
	shamirInitResult   *service.ShamirInitInfo
	shamirInitErr      error
	unsealShareResult  *service.UnsealShareProgress
	unsealShareErr     error
	unsealSharesErr    error
	rekeyResult        *service.ShamirInitInfo
	rekeyErr           error
	recoveryGenResult  *service.RecoveryKeysInfo
	recoveryGenErr     error
	recoveryRecoverErr error
	recoveryDeleteErr  error
	recoveryHasResult  bool
	recoveryHasErr     error
	rootTokenResult    *service.RootTokenInfo
	rootTokenErr       error
}

func newMockBarrierService() *mockBarrierService {
	return &mockBarrierService{
		statusResult: &service.BarrierStatusInfo{
			Sealed:         false,
			Strategy:       "shamir",
			HardwareBacked: false,
			InitializedAt:  time.Now(),
		},
		shamirInitResult:  &service.ShamirInitInfo{Threshold: 2, TotalShares: 3, ShareCount: 2},
		unsealShareResult: &service.UnsealShareProgress{Required: 2, Submitted: 1, Complete: false},
		rekeyResult:       &service.ShamirInitInfo{Threshold: 2, TotalShares: 3, ShareCount: 2},
		recoveryGenResult: &service.RecoveryKeysInfo{Threshold: 1, Total: 1, ShareCount: 1},
		rootTokenResult:   &service.RootTokenInfo{Token: "root-token-hex", CreatedAt: time.Now()},
	}
}

func (m *mockBarrierService) BarrierInitialize(_ context.Context, _ []byte) error {
	return m.initErr
}
func (m *mockBarrierService) BarrierUnseal(_ context.Context, _ []byte) error {
	return m.unsealErr
}
func (m *mockBarrierService) BarrierSeal(_ context.Context) error {
	return m.sealErr
}
func (m *mockBarrierService) BarrierStatus(_ context.Context) (*service.BarrierStatusInfo, error) {
	if m.statusErr != nil {
		return nil, m.statusErr
	}
	return m.statusResult, nil
}
func (m *mockBarrierService) BarrierInitializeShamir(_ context.Context, _ []byte, _, _ int) (*service.ShamirInitInfo, error) {
	if m.shamirInitErr != nil {
		return nil, m.shamirInitErr
	}
	return m.shamirInitResult, nil
}
func (m *mockBarrierService) BarrierUnsealWithShare(_ context.Context, _ string) (*service.UnsealShareProgress, error) {
	if m.unsealShareErr != nil {
		return nil, m.unsealShareErr
	}
	return m.unsealShareResult, nil
}
func (m *mockBarrierService) BarrierUnsealWithShares(_ context.Context, _ []string) error {
	return m.unsealSharesErr
}

func (m *mockBarrierService) BarrierRekey(_ context.Context, _, _ int) (*service.ShamirInitInfo, error) {
	if m.rekeyErr != nil {
		return nil, m.rekeyErr
	}
	return m.rekeyResult, nil
}
func (m *mockBarrierService) BarrierGenerateRecoveryKeys(_ context.Context, _, _ int) (*service.RecoveryKeysInfo, error) {
	if m.recoveryGenErr != nil {
		return nil, m.recoveryGenErr
	}
	return m.recoveryGenResult, nil
}
func (m *mockBarrierService) BarrierRecoverWithKeys(_ context.Context, _ []string) error {
	return m.recoveryRecoverErr
}
func (m *mockBarrierService) BarrierDeleteRecoveryKeys(_ context.Context) error {
	return m.recoveryDeleteErr
}
func (m *mockBarrierService) BarrierHasRecoveryKeys(_ context.Context) (bool, error) {
	if m.recoveryHasErr != nil {
		return false, m.recoveryHasErr
	}
	return m.recoveryHasResult, nil
}
func (m *mockBarrierService) BarrierGenerateRootToken(_ context.Context, _ []string) (*service.RootTokenInfo, error) {
	if m.rootTokenErr != nil {
		return nil, m.rootTokenErr
	}
	return m.rootTokenResult, nil
}
func (m *mockBarrierService) BarrierOfficerRegister(_ context.Context, _ *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	return &service.BarrierOfficerRegisterResult{}, nil
}
func (m *mockBarrierService) BarrierOfficerList(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
	return nil, nil
}
func (m *mockBarrierService) BarrierOfficerClaimStatus(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
	return nil, nil
}
func (m *mockBarrierService) BarrierShareClaimBegin(_ context.Context, _, _ string) (*service.BarrierShareClaimChallenge, error) {
	return &service.BarrierShareClaimChallenge{}, nil
}
func (m *mockBarrierService) BarrierShareClaimComplete(_ context.Context, _, _ string, _ []byte, _ string) (*service.BarrierShareClaimResult, error) {
	return &service.BarrierShareClaimResult{}, nil
}
func (m *mockBarrierService) BarrierShamirSealVote(_ context.Context, _ string) (*service.UnsealShareProgress, error) {
	return &service.UnsealShareProgress{}, nil
}
func (m *mockBarrierService) BarrierRecoveryOfficerRegister(_ context.Context, _ *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	return &service.BarrierOfficerRegisterResult{}, nil
}
func (m *mockBarrierService) BarrierRecoveryShareClaimBegin(_ context.Context, _, _ string) (*service.BarrierShareClaimChallenge, error) {
	return &service.BarrierShareClaimChallenge{}, nil
}
func (m *mockBarrierService) BarrierRecoveryShareClaimComplete(_ context.Context, _, _ string, _ []byte, _ string) (*service.BarrierShareClaimResult, error) {
	return &service.BarrierShareClaimResult{}, nil
}

// ---------------------------------------------------------------------------
// Mock CeremonyServicer
// ---------------------------------------------------------------------------

type mockCeremonyService struct {
	initResult       *service.CeremonyInitResult
	initErr          error
	capturedInitOpts *service.CeremonyInitializeOptions
	capturedCA       ceremony.CeremonyCA
	statusResult     *service.CeremonyStatusInfo
	statusErr        error
	beginResult      *service.CeremonyChallenge
	beginErr         error
	completeResult   *service.CeremonyClaimCertResult
	completeErr      error
	claimShareResult []byte
	claimShareErr    error
	signCSRResult    *service.CeremonySignCSRResult
	signCSRErr       error
}

func newMockCeremonyService() *mockCeremonyService {
	return &mockCeremonyService{
		initResult: &service.CeremonyInitResult{
			SPKIPin:    "pin123",
			CACertPEM:  []byte("ca-cert"),
			TLSCertPEM: []byte("tls-cert"),
			State:      "initialized",
			ShareCount: 3,
		},
		statusResult: &service.CeremonyStatusInfo{State: "initialized", SPKIPin: "pin123"},
		beginResult:  &service.CeremonyChallenge{NonceHex: "deadbeef", ExpiresAt: time.Now().Add(time.Minute)},
		completeResult: &service.CeremonyClaimCertResult{
			CertPEM:   []byte("officer-cert"),
			CACertPEM: []byte("ca-cert"),
		},
		claimShareResult: []byte("share-value"),
		signCSRResult:    &service.CeremonySignCSRResult{CertPEM: []byte("signed-cert")},
	}
}

func (m *mockCeremonyService) CeremonyInitialize(_ context.Context, ca ceremony.CeremonyCA, opts *service.CeremonyInitializeOptions) (*service.CeremonyInitResult, error) {
	m.capturedInitOpts = opts
	m.capturedCA = ca
	if m.initErr != nil {
		return nil, m.initErr
	}
	return m.initResult, nil
}
func (m *mockCeremonyService) CeremonyBeginClaimCert(_ context.Context, _ string) (*service.CeremonyChallenge, error) {
	if m.beginErr != nil {
		return nil, m.beginErr
	}
	return m.beginResult, nil
}
func (m *mockCeremonyService) CeremonyCompleteClaimCert(_ context.Context, _, _ string, _ []byte) (*service.CeremonyClaimCertResult, error) {
	if m.completeErr != nil {
		return nil, m.completeErr
	}
	return m.completeResult, nil
}
func (m *mockCeremonyService) CeremonyBeginClaimShare(_ context.Context, _, _ string) (*service.CeremonyChallenge, error) {
	if m.claimShareErr != nil {
		return nil, m.claimShareErr
	}
	return &service.CeremonyChallenge{NonceHex: "abcd", ExpiresAt: time.Now().Add(time.Minute)}, nil
}
func (m *mockCeremonyService) CeremonyCompleteClaimShare(_ context.Context, _, _ string, _ []byte, _ string) (*service.CeremonyShareClaimResult, error) {
	if m.claimShareErr != nil {
		return nil, m.claimShareErr
	}
	return &service.CeremonyShareClaimResult{ShareIndex: 1, ShareBytes: m.claimShareResult}, nil
}
func (m *mockCeremonyService) CeremonySignCSR(_ context.Context, _, _, _, _ string) (*service.CeremonySignCSRResult, error) {
	if m.signCSRErr != nil {
		return nil, m.signCSRErr
	}
	return m.signCSRResult, nil
}
func (m *mockCeremonyService) CeremonyStatus(_ context.Context) (*service.CeremonyStatusInfo, error) {
	if m.statusErr != nil {
		return nil, m.statusErr
	}
	return m.statusResult, nil
}

// CeremonyClaimShareBegin is a stub satisfying the Phase 4 custodian-ceremony
// extension of service.CeremonyServicer. The gRPC coverage tests do not
// exercise factor-based claim flows.
func (m *mockCeremonyService) CeremonyClaimShareBegin(_ context.Context, _ string) (*service.CeremonyCustodianChallenge, error) {
	return nil, fmt.Errorf("not implemented in mock")
}

// CeremonyClaimShareComplete is a stub for the custodian claim-complete flow.
func (m *mockCeremonyService) CeremonyClaimShareComplete(_ context.Context, _, _ string, _ []byte) (*custodian.Custodian, error) {
	return nil, fmt.Errorf("not implemented in mock")
}

// CeremonySubmitUnsealShare is a stub for custodian unseal-share submission.
func (m *mockCeremonyService) CeremonySubmitUnsealShare(_ context.Context, _, _ string, _, _ []byte) (*service.CeremonyUnsealShareResult, error) {
	return nil, fmt.Errorf("not implemented in mock")
}

// CeremonyEnrollExternalOfficer is a stub for external-officer enrollment.
func (m *mockCeremonyService) CeremonyEnrollExternalOfficer(_ context.Context, _ *service.CeremonyEnrollExternalOfficerRequest) (*service.CeremonyEnrollExternalOfficerResponse, error) {
	return nil, fmt.Errorf("not implemented in mock")
}

// ---------------------------------------------------------------------------
// Mock DatabaseServicer
// ---------------------------------------------------------------------------

type mockDatabaseService struct {
	createResult *database.DatabaseInfo
	createErr    error
	getResult    *database.DatabaseInfo
	getErr       error
	listResult   []database.DatabaseInfo
	listErr      error
	dropErr      error
	updateResult *database.DatabaseInfo
	updateErr    error
}

func newMockDatabaseService() *mockDatabaseService {
	return &mockDatabaseService{
		createResult: &database.DatabaseInfo{Name: "testdb", Engine: "mvcc", Backend: "pebble"},
		getResult:    &database.DatabaseInfo{Name: "testdb", Engine: "mvcc", Backend: "pebble"},
		listResult:   []database.DatabaseInfo{{Name: "testdb"}},
		updateResult: &database.DatabaseInfo{Name: "testdb", Engine: "mvcc", Backend: "pebble"},
	}
}

func (m *mockDatabaseService) Create(_ database.DatabaseConfig) (*database.DatabaseInfo, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	return m.createResult, nil
}
func (m *mockDatabaseService) Get(_ string) (*database.DatabaseInfo, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	return m.getResult, nil
}
func (m *mockDatabaseService) List() ([]database.DatabaseInfo, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.listResult, nil
}
func (m *mockDatabaseService) Drop(_ context.Context, _ string) error {
	return m.dropErr
}
func (m *mockDatabaseService) UpdateProtocol(_ string, _ database.ProtocolConfig) (*database.DatabaseInfo, error) {
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	return m.updateResult, nil
}
func (m *mockDatabaseService) UpdateEtcd(_ string, _ *database.EtcdConfig) (*database.DatabaseInfo, error) {
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	return m.updateResult, nil
}
func (m *mockDatabaseService) UpdateRaft(_ string, _ *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	return m.updateResult, nil
}
func (m *mockDatabaseService) UpdateBarrier(_ string, _ *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	return m.updateResult, nil
}
func (m *mockDatabaseService) UpdateConnection(_ string, _ *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	return m.updateResult, nil
}
func (m *mockDatabaseService) SealDatabase(_ context.Context, _ string) error { return nil }
func (m *mockDatabaseService) UnsealDatabase(_ context.Context, _ string, _ seal.Credentials) error {
	return nil
}
func (m *mockDatabaseService) DatabaseBarrierStatus(_ context.Context, _ string) (*seal.BarrierStatus, error) {
	return nil, nil
}
func (m *mockDatabaseService) CreateForTenant(_ context.Context, _ database.DatabaseConfig) (*database.DatabaseInfo, error) {
	return m.createResult, m.createErr
}
func (m *mockDatabaseService) GetForTenant(_ context.Context, _ string) (*database.DatabaseInfo, error) {
	return m.getResult, m.getErr
}
func (m *mockDatabaseService) ListForTenant(_ context.Context) ([]database.DatabaseInfo, error) {
	return m.listResult, m.listErr
}
func (m *mockDatabaseService) DropForTenant(_ context.Context, _ string) error { return m.dropErr }
func (m *mockDatabaseService) UpdateProtocolForTenant(_ context.Context, _ string, _ database.ProtocolConfig) (*database.DatabaseInfo, error) {
	return m.updateResult, m.updateErr
}
func (m *mockDatabaseService) UpdateEtcdForTenant(_ context.Context, _ string, _ *database.EtcdConfig) (*database.DatabaseInfo, error) {
	return m.updateResult, m.updateErr
}
func (m *mockDatabaseService) UpdateRaftForTenant(_ context.Context, _ string, _ *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	return m.updateResult, m.updateErr
}
func (m *mockDatabaseService) UpdateBarrierForTenant(_ context.Context, _ string, _ *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	return m.updateResult, m.updateErr
}
func (m *mockDatabaseService) UpdateConnectionForTenant(_ context.Context, _ string, _ *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	return m.updateResult, m.updateErr
}
func (m *mockDatabaseService) ScaleForTenant(_ context.Context, _ string, _ int) (*database.DatabaseInfo, error) {
	return m.updateResult, m.updateErr
}

// ---------------------------------------------------------------------------
// Mock CollectionServicer
// ---------------------------------------------------------------------------

type mockCollectionService struct {
	createResult  *service.CollectionInfo
	createErr     error
	getInfoErr    error
	listResult    *service.ListCollectionsResponse
	listErr       error
	dropErr       error
	insertResult  *service.InsertDocumentResponse
	insertErr     error
	getDocResult  *service.GetDocumentResponse
	getDocErr     error
	updateDocErr  error
	deleteDocErr  error
	listDocResult *service.ListDocumentsResponse
	listDocErr    error
}

func newMockCollectionService() *mockCollectionService {
	return &mockCollectionService{
		createResult: &service.CollectionInfo{
			Name:      "docs",
			Database:  "testdb",
			CreatedAt: time.Now(),
		},
		listResult: &service.ListCollectionsResponse{
			Collections: []service.CollectionInfo{
				{Name: "docs", Database: "testdb", CreatedAt: time.Now()},
			},
		},
		insertResult: &service.InsertDocumentResponse{ID: 1},
		getDocResult: &service.GetDocumentResponse{
			ID:       1,
			Document: map[string]interface{}{"key": "value"},
		},
		listDocResult: &service.ListDocumentsResponse{
			Documents: []service.GetDocumentResponse{
				{ID: 1, Document: map[string]interface{}{"key": "value"}},
			},
			TotalCount: 1,
		},
	}
}

func (m *mockCollectionService) CreateCollection(_ context.Context, _ *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	return m.createResult, nil
}
func (m *mockCollectionService) DropCollection(_ context.Context, _ *service.DropCollectionRequest) error {
	return m.dropErr
}
func (m *mockCollectionService) ListCollections(_ context.Context, _ *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.listResult, nil
}
func (m *mockCollectionService) GetCollectionInfo(_ context.Context, _, _ string) (*service.CollectionInfo, error) {
	if m.getInfoErr != nil {
		return nil, m.getInfoErr
	}
	return m.createResult, nil
}
func (m *mockCollectionService) InsertDocument(_ context.Context, _ *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
	if m.insertErr != nil {
		return nil, m.insertErr
	}
	return m.insertResult, nil
}
func (m *mockCollectionService) GetDocument(_ context.Context, _ *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
	if m.getDocErr != nil {
		return nil, m.getDocErr
	}
	return m.getDocResult, nil
}
func (m *mockCollectionService) UpdateDocument(_ context.Context, _ *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error) {
	if m.updateDocErr != nil {
		return nil, m.updateDocErr
	}
	return &service.UpdateDocumentResponse{}, nil
}

// UpsertDocument reports an insert: no test that uses this double drives
// the guarded upsert, which is exercised against the real service.
func (m *mockCollectionService) UpsertDocument(_ context.Context, req *service.UpsertDocumentRequest) (*service.UpsertDocumentResponse, error) {
	return &service.UpsertDocumentResponse{ID: req.ID}, nil
}
func (m *mockCollectionService) DeleteDocument(_ context.Context, _ *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error) {
	if m.deleteDocErr != nil {
		return nil, m.deleteDocErr
	}
	return &service.DeleteDocumentResponse{}, nil
}
func (m *mockCollectionService) ListDocuments(_ context.Context, _ *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
	if m.listDocErr != nil {
		return nil, m.listDocErr
	}
	return m.listDocResult, nil
}

// ---------------------------------------------------------------------------
// startTestServerWithAll creates a server with all optional services wired.
// ---------------------------------------------------------------------------

func startTestServerWithAll(t *testing.T, kvSvc service.KVServicer, adminSvc service.AdminServicer, opts ...ServerOption) (pb.QRDBServiceClient, func()) {
	t.Helper()
	tlsCfg := testServerTLSConfig(t)
	srv := NewServer(kvSvc, adminSvc, "127.0.0.1:0", append([]ServerOption{WithTLS(tlsCfg)}, opts...)...)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	conn, err := grpc.NewClient(srv.Addr(), grpc.WithTransportCredentials(testClientCreds(t, tlsCfg)))
	if err != nil {
		srv.Stop()
		t.Fatalf("grpc.NewClient failed: %v", err)
	}
	cleanup := func() {
		_ = conn.Close()
		srv.Stop()
	}
	return pb.NewQRDBServiceClient(conn), cleanup
}

// ---------------------------------------------------------------------------
// RegisterService tests
// ---------------------------------------------------------------------------

func TestRegisterService_Basic(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	grpcSrv := grpc.NewServer()
	RegisterService(grpcSrv, kvSvc, adminSvc)
	grpcSrv.Stop()
}

func TestRegisterService_WithAllOptions(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	grpcSrv := grpc.NewServer()
	RegisterService(grpcSrv, kvSvc, adminSvc,
		WithRegisterMVCC(&mockMVCCService{}),
		WithRegisterTransaction(&mockTransactionService{}),
		WithRegisterBatch(&mockBatchService{}),
		WithRegisterWatch(&mockWatchServiceImpl{}),
		WithRegisterLease(&mockLeaseService{}),
		WithRegisterIndex(&mockIndexService{}),
		WithRegisterTimeSeries(&mockTimeSeriesService{}),
		WithRegisterBarrierService(newMockBarrierService()),
		WithRegisterCeremonyService(newMockCeremonyService()),
		WithRegisterDatabaseService(newMockDatabaseService()),
		WithRegisterCollectionService(newMockCollectionService()),
	)
	grpcSrv.Stop()
}

func TestWithRegisterCertProvider(t *testing.T) {
	grpcSrv := grpc.NewServer()
	// nil cert provider still exercises the option.
	RegisterService(grpcSrv, newMockKVService(), newMockAdminService(),
		WithRegisterCertProvider(nil),
	)
	grpcSrv.Stop()
}

// ---------------------------------------------------------------------------
// ServerOption tests for 0%-covered options
// ---------------------------------------------------------------------------

func TestWithDatabaseService(t *testing.T) {
	svc := newMockDatabaseService()
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithDatabaseService(svc))
	if srv.databaseSvc == nil {
		t.Fatal("expected databaseSvc to be set")
	}
}

func TestWithDatabaseService_Nil(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithDatabaseService(nil))
	if srv.databaseSvc != nil {
		t.Fatal("expected databaseSvc to be nil")
	}
}

func TestWithBarrierService(t *testing.T) {
	svc := newMockBarrierService()
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithBarrierService(svc))
	if srv.barrierSvc == nil {
		t.Fatal("expected barrierSvc to be set")
	}
}

func TestWithBarrierService_Nil(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithBarrierService(nil))
	if srv.barrierSvc != nil {
		t.Fatal("expected barrierSvc to be nil")
	}
}

func TestWithCeremonyService(t *testing.T) {
	svc := newMockCeremonyService()
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithCeremonyService(svc))
	if srv.ceremonySvc == nil {
		t.Fatal("expected ceremonySvc to be set")
	}
}

func TestWithCeremonyService_Nil(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithCeremonyService(nil))
	if srv.ceremonySvc != nil {
		t.Fatal("expected ceremonySvc to be nil")
	}
}

func TestWithCredentialService(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithCredentialService(nil))
	if srv.credentialSvc != nil {
		t.Fatal("expected credentialSvc to be nil")
	}
}

func TestWithCollectionService(t *testing.T) {
	svc := newMockCollectionService()
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithCollectionService(svc))
	if srv.collectionSvc == nil {
		t.Fatal("expected collectionSvc to be set")
	}
}

func TestWithCollectionService_Nil(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithCollectionService(nil))
	if srv.collectionSvc != nil {
		t.Fatal("expected collectionSvc to be nil")
	}
}

func TestWithAuthMiddleware(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithAuthMiddleware(nil))
	if srv.authMW != nil {
		t.Fatal("expected authMW to be nil")
	}
}

// ---------------------------------------------------------------------------
// Scan handler tests (75% coverage)
// ---------------------------------------------------------------------------

func TestGRPC_Scan_WithPrefix_Success(t *testing.T) {
	kvSvc := newMockKVService()
	_ = kvSvc.Put(context.Background(), "prefix:a", []byte("val-a"))
	_ = kvSvc.Put(context.Background(), "prefix:b", []byte("val-b"))
	client, cleanup := startTestServer(t, kvSvc, newMockAdminService())
	defer cleanup()

	resp, err := client.Scan(context.Background(), &pb.ScanRequest{Prefix: "prefix:"})
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(resp.GetPairs()) != 2 {
		t.Fatalf("expected 2 pairs, got %d", len(resp.GetPairs()))
	}
}

func TestGRPC_Scan_EmptyPrefix(t *testing.T) {
	kvSvc := newMockKVService()
	_ = kvSvc.Put(context.Background(), "key1", []byte("v1"))
	client, cleanup := startTestServer(t, kvSvc, newMockAdminService())
	defer cleanup()

	resp, err := client.Scan(context.Background(), &pb.ScanRequest{Prefix: ""})
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(resp.GetPairs()) != 1 {
		t.Fatalf("expected 1 pair, got %d", len(resp.GetPairs()))
	}
}

// ---------------------------------------------------------------------------
// StartShard / StopShard / GetShardState / GetTopology handler tests
// ---------------------------------------------------------------------------

func TestGRPC_StartShard_Success(t *testing.T) {
	client, cleanup := startTestServer(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_, err := client.StartShard(context.Background(), &pb.StartShardRequest{
		ShardId:   1,
		ReplicaId: 1,
		Members:   map[uint64]string{1: "127.0.0.1:63001"},
		Join:      false,
	})
	if err != nil {
		t.Fatalf("StartShard failed: %v", err)
	}
}

func TestGRPC_StartShard_Error(t *testing.T) {
	adminSvc := newMockAdminService()
	// Override StartShard to return an error by using a custom mock.
	errAdmin := &erroringAdminService{
		mockAdminService: adminSvc,
		startShardErr:    &service.ProposeError{Err: errors.New("shard start failed")},
	}
	client, cleanup := startTestServer(t, newMockKVService(), errAdmin)
	defer cleanup()

	_, err := client.StartShard(context.Background(), &pb.StartShardRequest{ShardId: 1, ReplicaId: 1})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestGRPC_StopShard_Success(t *testing.T) {
	client, cleanup := startTestServer(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_, err := client.StopShard(context.Background(), &pb.StopShardRequest{ShardId: 1})
	if err != nil {
		t.Fatalf("StopShard failed: %v", err)
	}
}

func TestGRPC_StopShard_Error(t *testing.T) {
	errAdmin := &erroringAdminService{
		mockAdminService: newMockAdminService(),
		stopShardErr:     &service.ProposeError{Err: errors.New("stop failed")},
	}
	client, cleanup := startTestServer(t, newMockKVService(), errAdmin)
	defer cleanup()

	_, err := client.StopShard(context.Background(), &pb.StopShardRequest{ShardId: 1})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestGRPC_GetShardState_Success(t *testing.T) {
	client, cleanup := startTestServer(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	resp, err := client.GetShardState(context.Background(), &pb.GetShardStateRequest{ShardId: 1})
	if err != nil {
		t.Fatalf("GetShardState failed: %v", err)
	}
	if resp.GetShardId() != 1 {
		t.Fatalf("expected shard_id=1, got %d", resp.GetShardId())
	}
}

func TestGRPC_GetShardState_Error(t *testing.T) {
	errAdmin := &erroringAdminService{
		mockAdminService: newMockAdminService(),
		getShardStateErr: &service.ReadError{Err: errors.New("state error")},
	}
	client, cleanup := startTestServer(t, newMockKVService(), errAdmin)
	defer cleanup()

	_, err := client.GetShardState(context.Background(), &pb.GetShardStateRequest{ShardId: 1})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestGRPC_GetTopology_Success(t *testing.T) {
	client, cleanup := startTestServer(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	resp, err := client.GetTopology(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("GetTopology failed: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestGRPC_GetTopology_Error(t *testing.T) {
	errAdmin := &erroringAdminService{
		mockAdminService: newMockAdminService(),
		getTopologyErr:   &service.ReadError{Err: errors.New("topology error")},
	}
	client, cleanup := startTestServer(t, newMockKVService(), errAdmin)
	defer cleanup()

	_, err := client.GetTopology(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

// erroringAdminService extends mockAdminService to support per-method error injection
// for methods that the base mock always succeeds on.
type erroringAdminService struct {
	*mockAdminService
	startShardErr    error
	stopShardErr     error
	getShardStateErr error
	getTopologyErr   error
}

func (e *erroringAdminService) StartShard(_ context.Context, _ *service.StartShardRequest) error {
	return e.startShardErr
}

func (e *erroringAdminService) StopShard(_ context.Context, _ uint64) error {
	return e.stopShardErr
}

func (e *erroringAdminService) GetShardState(_ context.Context, shardID uint64) (*service.ShardStateResponse, error) {
	if e.getShardStateErr != nil {
		return nil, e.getShardStateErr
	}
	return e.mockAdminService.GetShardState(nil, shardID)
}

func (e *erroringAdminService) GetTopology(_ context.Context) (*service.TopologyResponse, error) {
	if e.getTopologyErr != nil {
		return nil, e.getTopologyErr
	}
	return &service.TopologyResponse{}, nil
}

// ---------------------------------------------------------------------------
// IndexList handler tests
// ---------------------------------------------------------------------------

func TestGRPC_IndexList_Success(t *testing.T) {
	indexSvc := &mockIndexService{listResult: []string{"idx1", "idx2"}}
	client, cleanup := startTestServerFull(t, WithIndex(indexSvc))
	defer cleanup()

	resp, err := client.IndexList(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("IndexList failed: %v", err)
	}
	if len(resp.GetNames()) != 2 {
		t.Fatalf("expected 2 names, got %d", len(resp.GetNames()))
	}
}

func TestGRPC_IndexList_Error(t *testing.T) {
	indexSvc := &mockIndexService{listErr: &service.ReadError{Err: errors.New("list failed")}}
	client, cleanup := startTestServerFull(t, WithIndex(indexSvc))
	defer cleanup()

	_, err := client.IndexList(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestGRPC_IndexList_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.IndexList(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// richQueryTSService returns non-nil QueryResult for TSRichQuery tests.
// ---------------------------------------------------------------------------

type richQueryTSService struct {
	mockTimeSeriesService
	result *timeseries.QueryResult
}

func (m *richQueryTSService) QueryRich(_ context.Context, _ *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
	return m.result, nil
}

// retentionTSService returns a non-nil RetentionPolicy for TSGetRetention tests.
type retentionTSService struct {
	mockTimeSeriesService
	policy *timeseries.RetentionPolicy
}

func (m *retentionTSService) GetRetentionPolicy(_ string) (*timeseries.RetentionPolicy, error) {
	return m.policy, nil
}

// ---------------------------------------------------------------------------
// TimeSeries extended handler tests
// ---------------------------------------------------------------------------

func TestGRPC_TSWritePoint_Success(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	_, err := client.TSWritePoint(context.Background(), &pb.TSWriteRequest{
		Point: &pb.TSDataPoint{
			Metric:      "cpu",
			Value:       55.0,
			TimestampNs: time.Now().UnixNano(),
		},
	})
	if err != nil {
		t.Fatalf("TSWritePoint failed: %v", err)
	}
}

func TestGRPC_TSWritePoint_NilPoint(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	_, err := client.TSWritePoint(context.Background(), &pb.TSWriteRequest{})
	if err == nil {
		t.Fatal("expected error for nil point, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestGRPC_TSWritePoint_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.TSWritePoint(context.Background(), &pb.TSWriteRequest{
		Point: &pb.TSDataPoint{Metric: "cpu", Value: 1.0, TimestampNs: 1000},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_TSRichQuery_Success(t *testing.T) {
	tsSvc := &richQueryTSService{
		result: &timeseries.QueryResult{
			Metric: "cpu",
			Points: []*timeseries.DataPoint{},
		},
	}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	resp, err := client.TSRichQuery(context.Background(), &pb.TSRichQueryRequest{
		Metric:  "cpu",
		StartNs: time.Now().Add(-time.Hour).UnixNano(),
		EndNs:   time.Now().UnixNano(),
	})
	if err != nil {
		t.Fatalf("TSRichQuery failed: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestGRPC_TSRichQuery_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.TSRichQuery(context.Background(), &pb.TSRichQueryRequest{Metric: "cpu"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_TSDeleteSeries_Success(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	_, err := client.TSDeleteSeries(context.Background(), &pb.TSDeleteSeriesRequest{
		Metric:  "cpu",
		StartNs: time.Now().Add(-time.Hour).UnixNano(),
		EndNs:   time.Now().UnixNano(),
	})
	if err != nil {
		t.Fatalf("TSDeleteSeries failed: %v", err)
	}
}

func TestGRPC_TSDeleteSeries_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.TSDeleteSeries(context.Background(), &pb.TSDeleteSeriesRequest{Metric: "cpu"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_TSGetRetention_Success(t *testing.T) {
	tsSvc := &retentionTSService{
		policy: &timeseries.RetentionPolicy{
			Duration:    time.Hour * 24 * 7,
			Resolution:  time.Minute,
			Aggregation: timeseries.AggAvg,
		},
	}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	resp, err := client.TSGetRetention(context.Background(), &pb.TSGetRetentionRequest{Metric: "cpu"})
	if err != nil {
		t.Fatalf("TSGetRetention failed: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestGRPC_TSGetRetention_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.TSGetRetention(context.Background(), &pb.TSGetRetentionRequest{Metric: "cpu"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_TSSetRetention_Success(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	_, err := client.TSSetRetention(context.Background(), &pb.TSSetRetentionRequest{
		Metric:     "cpu",
		DurationNs: int64(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("TSSetRetention failed: %v", err)
	}
}

func TestGRPC_TSSetRetention_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.TSSetRetention(context.Background(), &pb.TSSetRetentionRequest{Metric: "cpu"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_TSListMetrics_Success(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	resp, err := client.TSListMetrics(context.Background(), &pb.TSListMetricsRequest{Prefix: "cpu"})
	if err != nil {
		t.Fatalf("TSListMetrics failed: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestGRPC_TSListMetrics_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.TSListMetrics(context.Background(), &pb.TSListMetricsRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// Barrier handler tests
// ---------------------------------------------------------------------------

func TestGRPC_BarrierInitialize_Success(t *testing.T) {
	barrierSvc := newMockBarrierService()
	client, cleanup := startTestServerFull(t, WithBarrierService(barrierSvc))
	defer cleanup()

	_, err := client.BarrierInitialize(context.Background(), &pb.BarrierInitializeRequest{Secret: []byte("mysecret")})
	if err != nil {
		t.Fatalf("BarrierInitialize failed: %v", err)
	}
}

func TestGRPC_BarrierInitialize_Error(t *testing.T) {
	barrierSvc := newMockBarrierService()
	barrierSvc.initErr = &service.ProposeError{Err: errors.New("init failed")}
	client, cleanup := startTestServerFull(t, WithBarrierService(barrierSvc))
	defer cleanup()

	_, err := client.BarrierInitialize(context.Background(), &pb.BarrierInitializeRequest{Secret: []byte("s")})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestGRPC_BarrierInitialize_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.BarrierInitialize(context.Background(), &pb.BarrierInitializeRequest{Secret: []byte("s")})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_BarrierUnseal_Success(t *testing.T) {
	barrierSvc := newMockBarrierService()
	client, cleanup := startTestServerFull(t, WithBarrierService(barrierSvc))
	defer cleanup()

	_, err := client.BarrierUnseal(context.Background(), &pb.BarrierUnsealRequest{Secret: []byte("mysecret")})
	if err != nil {
		t.Fatalf("BarrierUnseal failed: %v", err)
	}
}

func TestGRPC_BarrierUnseal_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.BarrierUnseal(context.Background(), &pb.BarrierUnsealRequest{Secret: []byte("s")})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_BarrierSeal_Success(t *testing.T) {
	barrierSvc := newMockBarrierService()
	client, cleanup := startTestServerFull(t, WithBarrierService(barrierSvc))
	defer cleanup()

	_, err := client.BarrierSeal(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("BarrierSeal failed: %v", err)
	}
}

func TestGRPC_BarrierSeal_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.BarrierSeal(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_BarrierStatus_Success(t *testing.T) {
	barrierSvc := newMockBarrierService()
	client, cleanup := startTestServerFull(t, WithBarrierService(barrierSvc))
	defer cleanup()

	resp, err := client.BarrierStatus(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("BarrierStatus failed: %v", err)
	}
	if resp.GetStrategy() != "shamir" {
		t.Fatalf("expected strategy 'shamir', got %q", resp.GetStrategy())
	}
}

func TestGRPC_BarrierStatus_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.BarrierStatus(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_BarrierShamirInitialize_Success(t *testing.T) {
	barrierSvc := newMockBarrierService()
	client, cleanup := startTestServerFull(t, WithBarrierService(barrierSvc))
	defer cleanup()

	resp, err := client.BarrierShamirInitialize(context.Background(), &pb.BarrierShamirInitializeRequest{
		Secret:      []byte("secret"),
		Threshold:   2,
		TotalShares: 3,
	})
	if err != nil {
		t.Fatalf("BarrierShamirInitialize failed: %v", err)
	}
	if resp.GetThreshold() != 2 {
		t.Fatalf("expected threshold 2, got %d", resp.GetThreshold())
	}
}

func TestGRPC_BarrierShamirInitialize_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.BarrierShamirInitialize(context.Background(), &pb.BarrierShamirInitializeRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_BarrierShamirUnsealShare_Success(t *testing.T) {
	barrierSvc := newMockBarrierService()
	client, cleanup := startTestServerFull(t, WithBarrierService(barrierSvc))
	defer cleanup()

	resp, err := client.BarrierShamirUnsealShare(context.Background(), &pb.BarrierShamirUnsealShareRequest{Share: "share1"})
	if err != nil {
		t.Fatalf("BarrierShamirUnsealShare failed: %v", err)
	}
	if resp.GetRequired() != 2 {
		t.Fatalf("expected required=2, got %d", resp.GetRequired())
	}
}

func TestGRPC_BarrierShamirUnsealShare_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.BarrierShamirUnsealShare(context.Background(), &pb.BarrierShamirUnsealShareRequest{Share: "s"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_BarrierShamirUnsealShares_Success(t *testing.T) {
	barrierSvc := newMockBarrierService()
	client, cleanup := startTestServerFull(t, WithBarrierService(barrierSvc))
	defer cleanup()

	_, err := client.BarrierShamirUnsealShares(context.Background(), &pb.BarrierShamirUnsealSharesRequest{
		Shares: []string{"share1", "share2"},
	})
	if err != nil {
		t.Fatalf("BarrierShamirUnsealShares failed: %v", err)
	}
}

func TestGRPC_BarrierShamirUnsealShares_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.BarrierShamirUnsealShares(context.Background(), &pb.BarrierShamirUnsealSharesRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_BarrierRekey_Success(t *testing.T) {
	barrierSvc := newMockBarrierService()
	client, cleanup := startTestServerFull(t, WithBarrierService(barrierSvc))
	defer cleanup()

	resp, err := client.BarrierRekey(context.Background(), &pb.BarrierRekeyRequest{Threshold: 2, Total: 3})
	if err != nil {
		t.Fatalf("BarrierRekey failed: %v", err)
	}
	// INV-1/INV-11: shares never in rekey response; assert share_count only.
	if resp.GetShareCount() != 2 {
		t.Fatalf("expected share_count=2, got %d", resp.GetShareCount())
	}
}

func TestGRPC_BarrierRekey_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.BarrierRekey(context.Background(), &pb.BarrierRekeyRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_BarrierRecoveryGenerate_Success(t *testing.T) {
	barrierSvc := newMockBarrierService()
	client, cleanup := startTestServerFull(t, WithBarrierService(barrierSvc))
	defer cleanup()

	resp, err := client.BarrierRecoveryGenerate(context.Background(), &pb.BarrierRecoveryGenerateRequest{
		Threshold: 1,
		Total:     1,
	})
	if err != nil {
		t.Fatalf("BarrierRecoveryGenerate failed: %v", err)
	}
	// INV-1/INV-11: recovery key bytes never in response; assert share_count only.
	if resp.GetShareCount() != 1 {
		t.Fatalf("expected share_count=1, got %d", resp.GetShareCount())
	}
}

func TestGRPC_BarrierRecoveryGenerate_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.BarrierRecoveryGenerate(context.Background(), &pb.BarrierRecoveryGenerateRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_BarrierRecoveryRecover_Success(t *testing.T) {
	barrierSvc := newMockBarrierService()
	client, cleanup := startTestServerFull(t, WithBarrierService(barrierSvc))
	defer cleanup()

	_, err := client.BarrierRecoveryRecover(context.Background(), &pb.BarrierRecoveryRecoverRequest{Keys: []string{"k1"}})
	if err != nil {
		t.Fatalf("BarrierRecoveryRecover failed: %v", err)
	}
}

func TestGRPC_BarrierRecoveryRecover_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.BarrierRecoveryRecover(context.Background(), &pb.BarrierRecoveryRecoverRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_BarrierRecoveryDelete_Success(t *testing.T) {
	barrierSvc := newMockBarrierService()
	client, cleanup := startTestServerFull(t, WithBarrierService(barrierSvc))
	defer cleanup()

	_, err := client.BarrierRecoveryDelete(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("BarrierRecoveryDelete failed: %v", err)
	}
}

func TestGRPC_BarrierRecoveryDelete_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.BarrierRecoveryDelete(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_BarrierRecoveryStatus_Success(t *testing.T) {
	barrierSvc := newMockBarrierService()
	barrierSvc.recoveryHasResult = true
	client, cleanup := startTestServerFull(t, WithBarrierService(barrierSvc))
	defer cleanup()

	resp, err := client.BarrierRecoveryStatus(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("BarrierRecoveryStatus failed: %v", err)
	}
	if !resp.GetHasRecoveryKeys() {
		t.Fatal("expected HasRecoveryKeys=true")
	}
}

func TestGRPC_BarrierRecoveryStatus_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.BarrierRecoveryStatus(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_BarrierRootToken_Success(t *testing.T) {
	barrierSvc := newMockBarrierService()
	client, cleanup := startTestServerFull(t, WithBarrierService(barrierSvc))
	defer cleanup()

	resp, err := client.BarrierRootToken(context.Background(), &pb.BarrierRootTokenRequest{
		Shares: []string{"s1", "s2"},
	})
	if err != nil {
		t.Fatalf("BarrierRootToken failed: %v", err)
	}
	if resp.GetToken() != "root-token-hex" {
		t.Fatalf("expected token 'root-token-hex', got %q", resp.GetToken())
	}
}

func TestGRPC_BarrierRootToken_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.BarrierRootToken(context.Background(), &pb.BarrierRootTokenRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// Ceremony handler tests
// ---------------------------------------------------------------------------

func TestGRPC_CeremonyInitialize_Success(t *testing.T) {
	ceremonySvc := newMockCeremonyService()
	ca := &stubCeremonyCA{}
	client, cleanup := startTestServerFull(t, WithCeremonyService(ceremonySvc), WithCeremonyCA(ca))
	defer cleanup()

	resp, err := client.CeremonyInitialize(context.Background(), &pb.CeremonyInitializeRequest{})
	if err != nil {
		t.Fatalf("CeremonyInitialize failed: %v", err)
	}
	if resp.GetSpkiPin() != "pin123" {
		t.Fatalf("expected spki_pin 'pin123', got %q", resp.GetSpkiPin())
	}
	if resp.GetShareCount() != 3 {
		t.Fatalf("expected share_count=3, got %d", resp.GetShareCount())
	}
	if ceremonySvc.capturedCA != ca {
		t.Errorf("expected the configured CeremonyCA to reach the service layer, got %#v", ceremonySvc.capturedCA)
	}
}

// TestGRPC_CeremonyInitialize_NilCeremonyCA proves that a server with a
// ceremony service but no CeremonyCA (WithCeremonyCA never applied) fails
// closed with a typed *CeremonyCANotConfiguredError, mapped to
// codes.Unimplemented, instead of forwarding a nil CA into the ceremony
// service layer, which would otherwise nil-pointer panic when it calls
// ca.IssueCertificate.
func TestGRPC_CeremonyInitialize_NilCeremonyCA(t *testing.T) {
	ceremonySvc := newMockCeremonyService()
	client, cleanup := startTestServerFull(t, WithCeremonyService(ceremonySvc))
	defer cleanup()

	_, err := client.CeremonyInitialize(context.Background(), &pb.CeremonyInitializeRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
	if !strings.Contains(st.Message(), "ceremony CA is not configured") {
		t.Errorf("expected 'ceremony CA is not configured' in status message, got %q", st.Message())
	}
	if ceremonySvc.capturedCA != nil {
		t.Fatal("ceremony service must not be reached when the CA is not configured")
	}
}

// TestGRPC_CeremonyInitialize_ForwardsAllFields is the regression guard for
// the bug where the gRPC CeremonyInitialize handler only forwarded
// SoPin/UserPin into service.CeremonyInitializeOptions, silently dropping
// CredentialSealStrategy, Threshold, Officers, Hostname, and Sans from the
// wire request.
func TestGRPC_CeremonyInitialize_ForwardsAllFields(t *testing.T) {
	ceremonySvc := newMockCeremonyService()
	client, cleanup := startTestServerFull(t, WithCeremonyService(ceremonySvc), WithCeremonyCA(&stubCeremonyCA{}))
	defer cleanup()

	_, err := client.CeremonyInitialize(context.Background(), &pb.CeremonyInitializeRequest{
		SoPin:                  "pin",
		UserPin:                "upin",
		CredentialSealStrategy: "shamir",
		Threshold:              2,
		Officers: []*pb.CeremonyOfficer{
			{Username: "officer-1", CsrPem: "csr1"},
			{Username: "officer-2", CsrPem: "csr2"},
		},
		Hostname: "override.example",
		Sans:     []string{"alt1.example", "alt2.example"},
	})
	if err != nil {
		t.Fatalf("CeremonyInitialize failed: %v", err)
	}

	got := ceremonySvc.capturedInitOpts
	if got == nil {
		t.Fatal("expected CeremonyInitialize to be called with non-nil opts")
	}
	if got.SOPin != "pin" || got.UserPin != "upin" {
		t.Errorf("SOPin/UserPin not forwarded: %+v", got)
	}
	if got.CredentialSealStrategy != "shamir" {
		t.Errorf("CredentialSealStrategy = %q, want %q", got.CredentialSealStrategy, "shamir")
	}
	if got.Threshold != 2 {
		t.Errorf("Threshold = %d, want 2", got.Threshold)
	}
	if got.Hostname != "override.example" {
		t.Errorf("Hostname = %q, want %q", got.Hostname, "override.example")
	}
	if len(got.SANs) != 2 || got.SANs[0] != "alt1.example" {
		t.Errorf("SANs = %v", got.SANs)
	}
	if len(got.Officers) != 2 || got.Officers[0].Username != "officer-1" || got.Officers[0].CSRPEM != "csr1" {
		t.Errorf("Officers = %+v", got.Officers)
	}
}

func TestGRPC_CeremonyInitialize_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.CeremonyInitialize(context.Background(), &pb.CeremonyInitializeRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_CeremonyStatus_Success(t *testing.T) {
	ceremonySvc := newMockCeremonyService()
	client, cleanup := startTestServerFull(t, WithCeremonyService(ceremonySvc))
	defer cleanup()

	resp, err := client.CeremonyStatus(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("CeremonyStatus failed: %v", err)
	}
	if resp.GetState() != "initialized" {
		t.Fatalf("expected state 'initialized', got %q", resp.GetState())
	}
}

func TestGRPC_CeremonyStatus_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.CeremonyStatus(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_CeremonyClaimCertBegin_Success(t *testing.T) {
	ceremonySvc := newMockCeremonyService()
	client, cleanup := startTestServerFull(t, WithCeremonyService(ceremonySvc))
	defer cleanup()

	resp, err := client.CeremonyClaimCertBegin(context.Background(), &pb.CeremonyClaimCertBeginRequest{Username: "officer1"})
	if err != nil {
		t.Fatalf("CeremonyClaimCertBegin failed: %v", err)
	}
	if resp.GetNonceHex() != "deadbeef" {
		t.Fatalf("expected nonce 'deadbeef', got %q", resp.GetNonceHex())
	}
}

func TestGRPC_CeremonyClaimCertBegin_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.CeremonyClaimCertBegin(context.Background(), &pb.CeremonyClaimCertBeginRequest{Username: "u"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_CeremonyClaimCertComplete_Success(t *testing.T) {
	ceremonySvc := newMockCeremonyService()
	client, cleanup := startTestServerFull(t, WithCeremonyService(ceremonySvc))
	defer cleanup()

	sig := base64.StdEncoding.EncodeToString([]byte("fake-signature"))
	resp, err := client.CeremonyClaimCertComplete(context.Background(), &pb.CeremonyClaimCertCompleteRequest{
		Username:  "officer1",
		NonceHex:  "deadbeef",
		Signature: sig,
	})
	if err != nil {
		t.Fatalf("CeremonyClaimCertComplete failed: %v", err)
	}
	if string(resp.GetCertPem()) != "officer-cert" {
		t.Fatalf("expected cert 'officer-cert', got %q", resp.GetCertPem())
	}
}

func TestGRPC_CeremonyClaimCertComplete_InvalidSignature(t *testing.T) {
	ceremonySvc := newMockCeremonyService()
	client, cleanup := startTestServerFull(t, WithCeremonyService(ceremonySvc))
	defer cleanup()

	_, err := client.CeremonyClaimCertComplete(context.Background(), &pb.CeremonyClaimCertCompleteRequest{
		Username:  "officer1",
		NonceHex:  "deadbeef",
		Signature: "not-valid-base64!!!",
	})
	if err == nil {
		t.Fatal("expected error for invalid base64 signature, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestGRPC_CeremonyClaimCertComplete_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	sig := base64.StdEncoding.EncodeToString([]byte("sig"))
	_, err := client.CeremonyClaimCertComplete(context.Background(), &pb.CeremonyClaimCertCompleteRequest{
		Username: "u", NonceHex: "nonce", Signature: sig,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// TestGRPC_CeremonyBeginClaimShare_RefusesWithoutClientCert proves the share
// claim is unreachable over a connection that carries no mTLS peer
// certificate: the caller thumbprint cannot be derived, so the handler fails
// closed before the ceremony service is consulted.
func TestGRPC_CeremonyBeginClaimShare_RefusesWithoutClientCert(t *testing.T) {
	ceremonySvc := newMockCeremonyService()
	client, cleanup := startTestServerFull(t, WithCeremonyService(ceremonySvc))
	defer cleanup()

	_, err := client.CeremonyBeginClaimShare(context.Background(), &pb.CeremonyBeginClaimShareRequest{Username: "officer1"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %s", st.Code())
	}
}

// TestGRPC_CeremonyCompleteClaimShare_RefusesWithoutClientCert proves the
// completing half is equally unreachable and, critically, that no share bytes
// are returned on the refusal path.
func TestGRPC_CeremonyCompleteClaimShare_RefusesWithoutClientCert(t *testing.T) {
	ceremonySvc := newMockCeremonyService()
	client, cleanup := startTestServerFull(t, WithCeremonyService(ceremonySvc))
	defer cleanup()

	resp, err := client.CeremonyCompleteClaimShare(context.Background(), &pb.CeremonyCompleteClaimShareRequest{
		Username:  "officer1",
		NonceHex:  "abcd",
		Signature: []byte("sig"),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if resp.GetShare() != "" {
		t.Fatalf("share bytes leaked on refusal: %q", resp.GetShare())
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %s", st.Code())
	}
}

func TestGRPC_CeremonyClaimShare_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.CeremonyBeginClaimShare(context.Background(), &pb.CeremonyBeginClaimShareRequest{Username: "u"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_CeremonySignCSR_Success(t *testing.T) {
	ceremonySvc := newMockCeremonyService()
	client, cleanup := startTestServerFull(t, WithCeremonyService(ceremonySvc))
	defer cleanup()

	resp, err := client.CeremonySignCSR(context.Background(), &pb.CeremonySignCSRRequest{
		Username: "officer1",
		SoPin:    "pin",
		CsrPem:   "-----BEGIN CERTIFICATE REQUEST-----\n...",
		Role:     "officer",
	})
	if err != nil {
		t.Fatalf("CeremonySignCSR failed: %v", err)
	}
	if string(resp.GetCertPem()) != "signed-cert" {
		t.Fatalf("expected 'signed-cert', got %q", resp.GetCertPem())
	}
}

func TestGRPC_CeremonySignCSR_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.CeremonySignCSR(context.Background(), &pb.CeremonySignCSRRequest{Username: "u"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// SignatureDecodeError and CeremonyError type tests
func TestSignatureDecodeError_Error(t *testing.T) {
	e := &SignatureDecodeError{Err: errors.New("bad encoding")}
	msg := e.Error()
	if !strings.Contains(msg, "bad encoding") {
		t.Fatalf("expected error to contain 'bad encoding', got %q", msg)
	}
	if !strings.Contains(msg, "decode") {
		t.Fatalf("expected error to contain 'decode', got %q", msg)
	}
}

func TestSignatureDecodeError_Unwrap(t *testing.T) {
	inner := errors.New("inner")
	e := &SignatureDecodeError{Err: inner}
	if e.Unwrap() != inner {
		t.Fatal("Unwrap did not return inner error")
	}
}

// ---------------------------------------------------------------------------
// Database handler tests
// ---------------------------------------------------------------------------

func TestGRPC_DatabaseCreate_Success(t *testing.T) {
	dbSvc := newMockDatabaseService()
	client, cleanup := startTestServerFull(t, WithDatabaseService(dbSvc))
	defer cleanup()

	resp, err := client.DatabaseCreate(context.Background(), &pb.DatabaseCreateRequest{
		Name:    "testdb",
		Engine:  "mvcc",
		Backend: "pebble",
	})
	if err != nil {
		t.Fatalf("DatabaseCreate failed: %v", err)
	}
	if resp.GetName() != "testdb" {
		t.Fatalf("expected name 'testdb', got %q", resp.GetName())
	}
}

func TestGRPC_DatabaseCreate_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.DatabaseCreate(context.Background(), &pb.DatabaseCreateRequest{Name: "db"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_DatabaseGet_Success(t *testing.T) {
	dbSvc := newMockDatabaseService()
	client, cleanup := startTestServerFull(t, WithDatabaseService(dbSvc))
	defer cleanup()

	resp, err := client.DatabaseGet(context.Background(), &pb.DatabaseGetRequest{Name: "testdb"})
	if err != nil {
		t.Fatalf("DatabaseGet failed: %v", err)
	}
	if resp.GetName() != "testdb" {
		t.Fatalf("expected name 'testdb', got %q", resp.GetName())
	}
}

func TestGRPC_DatabaseGet_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.DatabaseGet(context.Background(), &pb.DatabaseGetRequest{Name: "db"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_DatabaseList_Success(t *testing.T) {
	dbSvc := newMockDatabaseService()
	client, cleanup := startTestServerFull(t, WithDatabaseService(dbSvc))
	defer cleanup()

	resp, err := client.DatabaseList(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("DatabaseList failed: %v", err)
	}
	if len(resp.GetDatabases()) != 1 {
		t.Fatalf("expected 1 database, got %d", len(resp.GetDatabases()))
	}
}

func TestGRPC_DatabaseList_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.DatabaseList(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_DatabaseDrop_Success(t *testing.T) {
	dbSvc := newMockDatabaseService()
	client, cleanup := startTestServerFull(t, WithDatabaseService(dbSvc))
	defer cleanup()

	_, err := client.DatabaseDrop(context.Background(), &pb.DatabaseDropRequest{Name: "testdb"})
	if err != nil {
		t.Fatalf("DatabaseDrop failed: %v", err)
	}
}

func TestGRPC_DatabaseDrop_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.DatabaseDrop(context.Background(), &pb.DatabaseDropRequest{Name: "db"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_DatabaseUpdateProtocol_Success(t *testing.T) {
	dbSvc := newMockDatabaseService()
	client, cleanup := startTestServerFull(t, WithDatabaseService(dbSvc))
	defer cleanup()

	resp, err := client.DatabaseUpdateProtocol(context.Background(), &pb.DatabaseUpdateProtocolRequest{
		Name: "testdb",
	})
	if err != nil {
		t.Fatalf("DatabaseUpdateProtocol failed: %v", err)
	}
	if resp.GetName() != "testdb" {
		t.Fatalf("expected name 'testdb', got %q", resp.GetName())
	}
}

func TestGRPC_DatabaseUpdateProtocol_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.DatabaseUpdateProtocol(context.Background(), &pb.DatabaseUpdateProtocolRequest{Name: "db"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_DatabaseUpdateEtcd_Success(t *testing.T) {
	dbSvc := newMockDatabaseService()
	client, cleanup := startTestServerFull(t, WithDatabaseService(dbSvc))
	defer cleanup()

	resp, err := client.DatabaseUpdateEtcd(context.Background(), &pb.DatabaseUpdateEtcdRequest{
		Name:       "testdb",
		ListenAddr: "127.0.0.1:2379",
	})
	if err != nil {
		t.Fatalf("DatabaseUpdateEtcd failed: %v", err)
	}
	if resp.GetName() != "testdb" {
		t.Fatalf("expected name 'testdb', got %q", resp.GetName())
	}
}

func TestGRPC_DatabaseUpdateEtcd_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.DatabaseUpdateEtcd(context.Background(), &pb.DatabaseUpdateEtcdRequest{Name: "db"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_DatabaseUpdateRaft_Success(t *testing.T) {
	dbSvc := newMockDatabaseService()
	client, cleanup := startTestServerFull(t, WithDatabaseService(dbSvc))
	defer cleanup()

	resp, err := client.DatabaseUpdateRaft(context.Background(), &pb.DatabaseUpdateRaftRequest{
		Name: "testdb",
	})
	if err != nil {
		t.Fatalf("DatabaseUpdateRaft failed: %v", err)
	}
	if resp.GetName() != "testdb" {
		t.Fatalf("expected name 'testdb', got %q", resp.GetName())
	}
}

func TestGRPC_DatabaseUpdateRaft_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.DatabaseUpdateRaft(context.Background(), &pb.DatabaseUpdateRaftRequest{Name: "db"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_DatabaseUpdateBarrier_Success(t *testing.T) {
	dbSvc := newMockDatabaseService()
	client, cleanup := startTestServerFull(t, WithDatabaseService(dbSvc))
	defer cleanup()

	resp, err := client.DatabaseUpdateBarrier(context.Background(), &pb.DatabaseUpdateBarrierRequest{
		Name: "testdb",
	})
	if err != nil {
		t.Fatalf("DatabaseUpdateBarrier failed: %v", err)
	}
	if resp.GetName() != "testdb" {
		t.Fatalf("expected name 'testdb', got %q", resp.GetName())
	}
}

func TestGRPC_DatabaseUpdateBarrier_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.DatabaseUpdateBarrier(context.Background(), &pb.DatabaseUpdateBarrierRequest{Name: "db"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_DatabaseUpdateConnection_Success(t *testing.T) {
	dbSvc := newMockDatabaseService()
	client, cleanup := startTestServerFull(t, WithDatabaseService(dbSvc))
	defer cleanup()

	resp, err := client.DatabaseUpdateConnection(context.Background(), &pb.DatabaseUpdateConnectionRequest{
		Name: "testdb",
	})
	if err != nil {
		t.Fatalf("DatabaseUpdateConnection failed: %v", err)
	}
	if resp.GetName() != "testdb" {
		t.Fatalf("expected name 'testdb', got %q", resp.GetName())
	}
}

func TestGRPC_DatabaseUpdateConnection_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.DatabaseUpdateConnection(context.Background(), &pb.DatabaseUpdateConnectionRequest{Name: "db"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// Database type conversion helper tests
func TestProtoDatabaseCreateToConfig_WithProtocol(t *testing.T) {
	req := &pb.DatabaseCreateRequest{
		Name:    "db",
		Engine:  "mvcc",
		Backend: "pebble",
		Protocol: &pb.DatabaseProtocolConfig{
			Mysql:      true,
			Postgresql: false,
			Etcd: &pb.DatabaseEtcdConfig{
				Enabled:    true,
				ListenAddr: "127.0.0.1:2379",
			},
		},
	}
	cfg := protoDatabaseCreateToConfig(req)
	if cfg.Name != "db" {
		t.Fatalf("expected name 'db', got %q", cfg.Name)
	}
	if !cfg.Protocol.MySQL {
		t.Fatal("expected MySQL=true")
	}
	if cfg.Protocol.Etcd == nil {
		t.Fatal("expected non-nil Etcd config")
	}
}

func TestProtoDatabaseCreateToConfig_WithRaft(t *testing.T) {
	req := &pb.DatabaseCreateRequest{
		Name: "db",
		Raft: &pb.DatabaseRaftConfig{
			ShardId:     1,
			ReplicaId:   1,
			RaftAddress: "127.0.0.1:63001",
			Static: &pb.DatabaseStaticDiscovery{
				Peers:        []string{"127.0.0.1:63001"},
				SharedSecret: "secret",
			},
		},
	}
	cfg := protoDatabaseCreateToConfig(req)
	if cfg.Raft == nil {
		t.Fatal("expected non-nil Raft config")
	}
	if cfg.Raft.ShardID != 1 {
		t.Fatalf("expected shard_id=1, got %d", cfg.Raft.ShardID)
	}
	if cfg.Raft.Static == nil {
		t.Fatal("expected non-nil Static discovery config")
	}
}

func TestProtoRaftConfigToDomain_NilInput(t *testing.T) {
	result := protoRaftConfigToDomain(nil)
	if result != nil {
		t.Fatal("expected nil result for nil input")
	}
}

func TestProtoRaftConfigToDomain_WithMulticast(t *testing.T) {
	r := &pb.DatabaseRaftConfig{
		ShardId: 1,
		Multicast: &pb.DatabaseMulticastDiscovery{
			SharedSecret:   "secret",
			MulticastGroup: "224.0.0.1",
			MinPeers:       3,
		},
	}
	result := protoRaftConfigToDomain(r)
	if result.Multicast == nil {
		t.Fatal("expected non-nil Multicast config")
	}
	if result.Multicast.MulticastGroup != "224.0.0.1" {
		t.Fatalf("expected MulticastGroup '224.0.0.1', got %q", result.Multicast.MulticastGroup)
	}
}

func TestProtoRaftConfigToDomain_WithDNS(t *testing.T) {
	r := &pb.DatabaseRaftConfig{
		Dns: &pb.DatabaseDNSDiscovery{
			Service: "_raft",
			Domain:  "example.com",
		},
	}
	result := protoRaftConfigToDomain(r)
	if result.DNS == nil {
		t.Fatal("expected non-nil DNS config")
	}
	if result.DNS.Domain != "example.com" {
		t.Fatalf("expected Domain 'example.com', got %q", result.DNS.Domain)
	}
}

func TestProtoRaftConfigToDomain_WithToken(t *testing.T) {
	r := &pb.DatabaseRaftConfig{
		Token: &pb.DatabaseTokenDiscovery{
			Endpoint:    "http://discovery:8080",
			Token:       "tok",
			ClusterSize: 3,
		},
	}
	result := protoRaftConfigToDomain(r)
	if result.Token == nil {
		t.Fatal("expected non-nil Token config")
	}
	if result.Token.ClusterSize != 3 {
		t.Fatalf("expected ClusterSize=3, got %d", result.Token.ClusterSize)
	}
}

func TestProtoBarrierConfigToDomain_Nil(t *testing.T) {
	result := protoBarrierConfigToDomain(nil)
	if result != nil {
		t.Fatal("expected nil result for nil input")
	}
}

func TestProtoBarrierConfigToDomain_Enabled(t *testing.T) {
	b := &pb.DatabaseBarrierConfig{Enabled: true}
	result := protoBarrierConfigToDomain(b)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if !result.Enabled {
		t.Fatal("expected Enabled=true")
	}
}

func TestProtoConnectionConfigToDomain_Nil(t *testing.T) {
	result := protoConnectionConfigToDomain(nil)
	if result != nil {
		t.Fatal("expected nil result for nil input")
	}
}

func TestProtoConnectionConfigToDomain_WithValues(t *testing.T) {
	c := &pb.DatabaseConnectionConfig{
		Transport:     "grpc",
		RemoteAddress: "127.0.0.1:9000",
		CaFile:        "/etc/ca.pem",
	}
	result := protoConnectionConfigToDomain(c)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Transport != "grpc" {
		t.Fatalf("expected Transport 'grpc', got %q", result.Transport)
	}
	if result.Address != "127.0.0.1:9000" {
		t.Fatalf("expected Address '127.0.0.1:9000', got %q", result.Address)
	}
}

func TestDatabaseInfoToProto_WithRaftAndBarrierAndConnection(t *testing.T) {
	info := &database.DatabaseInfo{
		Name:    "db",
		Engine:  "mvcc",
		Backend: "pebble",
		Raft: &database.DatabaseRaftConfig{
			ShardID:   1,
			ReplicaID: 1,
			Static: &database.StaticDiscoveryConfig{
				Peers: []string{"127.0.0.1"},
			},
		},
		Barrier: &database.DatabaseBarrierConfig{Enabled: true},
		Connection: &database.DatabaseConnectionConfig{
			Transport: "grpc",
			Address:   "remote:9000",
		},
	}
	result := databaseInfoToProto(info)
	if result.GetRaft() == nil {
		t.Fatal("expected non-nil Raft in proto response")
	}
	if result.GetBarrier() == nil {
		t.Fatal("expected non-nil Barrier in proto response")
	}
	if result.GetConnection() == nil {
		t.Fatal("expected non-nil Connection in proto response")
	}
}

// ---------------------------------------------------------------------------
// Collection handler tests
// ---------------------------------------------------------------------------

func TestGRPC_CreateCollection_Success(t *testing.T) {
	collSvc := newMockCollectionService()
	client, cleanup := startTestServerFull(t, WithCollectionService(collSvc))
	defer cleanup()

	resp, err := client.CreateCollection(context.Background(), &pb.CreateCollectionRequest{
		Database: "testdb",
		Name:     "docs",
	})
	if err != nil {
		t.Fatalf("CreateCollection failed: %v", err)
	}
	if resp.GetName() != "docs" {
		t.Fatalf("expected name 'docs', got %q", resp.GetName())
	}
}

func TestGRPC_CreateCollection_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.CreateCollection(context.Background(), &pb.CreateCollectionRequest{Database: "db", Name: "c"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_GetCollectionInfo_Success(t *testing.T) {
	collSvc := newMockCollectionService()
	client, cleanup := startTestServerFull(t, WithCollectionService(collSvc))
	defer cleanup()

	resp, err := client.GetCollectionInfo(context.Background(), &pb.GetCollectionInfoRequest{
		Database: "testdb",
		Name:     "docs",
	})
	if err != nil {
		t.Fatalf("GetCollectionInfo failed: %v", err)
	}
	if resp.GetName() != "docs" {
		t.Fatalf("expected name 'docs', got %q", resp.GetName())
	}
}

func TestGRPC_GetCollectionInfo_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.GetCollectionInfo(context.Background(), &pb.GetCollectionInfoRequest{Database: "db", Name: "c"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_ListCollections_Success(t *testing.T) {
	collSvc := newMockCollectionService()
	client, cleanup := startTestServerFull(t, WithCollectionService(collSvc))
	defer cleanup()

	resp, err := client.ListCollections(context.Background(), &pb.ListCollectionsRequest{Database: "testdb"})
	if err != nil {
		t.Fatalf("ListCollections failed: %v", err)
	}
	if len(resp.GetCollections()) != 1 {
		t.Fatalf("expected 1 collection, got %d", len(resp.GetCollections()))
	}
}

func TestGRPC_ListCollections_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.ListCollections(context.Background(), &pb.ListCollectionsRequest{Database: "db"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_DropCollection_Success(t *testing.T) {
	collSvc := newMockCollectionService()
	client, cleanup := startTestServerFull(t, WithCollectionService(collSvc))
	defer cleanup()

	_, err := client.DropCollection(context.Background(), &pb.DropCollectionRequest{Database: "testdb", Name: "docs"})
	if err != nil {
		t.Fatalf("DropCollection failed: %v", err)
	}
}

func TestGRPC_DropCollection_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.DropCollection(context.Background(), &pb.DropCollectionRequest{Database: "db", Name: "c"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_InsertDocument_Success(t *testing.T) {
	collSvc := newMockCollectionService()
	client, cleanup := startTestServerFull(t, WithCollectionService(collSvc))
	defer cleanup()

	resp, err := client.InsertDocument(context.Background(), &pb.InsertDocumentRequest{
		Database:   "testdb",
		Collection: "docs",
		Document:   `{"name":"test","value":42}`,
	})
	if err != nil {
		t.Fatalf("InsertDocument failed: %v", err)
	}
	if resp.GetId() != 1 {
		t.Fatalf("expected id 1, got %d", resp.GetId())
	}
}

func TestGRPC_InsertDocument_InvalidJSON(t *testing.T) {
	collSvc := newMockCollectionService()
	client, cleanup := startTestServerFull(t, WithCollectionService(collSvc))
	defer cleanup()

	_, err := client.InsertDocument(context.Background(), &pb.InsertDocumentRequest{
		Database:   "testdb",
		Collection: "docs",
		Document:   "not-valid-json",
	})
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestGRPC_InsertDocument_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.InsertDocument(context.Background(), &pb.InsertDocumentRequest{
		Database: "db", Collection: "c", Document: `{}`,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_GetDocument_Success(t *testing.T) {
	collSvc := newMockCollectionService()
	client, cleanup := startTestServerFull(t, WithCollectionService(collSvc))
	defer cleanup()

	resp, err := client.GetDocument(context.Background(), &pb.GetDocumentRequest{
		Database:   "testdb",
		Collection: "docs",
		Id:         1,
	})
	if err != nil {
		t.Fatalf("GetDocument failed: %v", err)
	}
	if resp.GetId() != 1 {
		t.Fatalf("expected id 1, got %d", resp.GetId())
	}
	if resp.GetDocument() == "" {
		t.Fatal("expected non-empty document JSON")
	}
}

func TestGRPC_GetDocument_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.GetDocument(context.Background(), &pb.GetDocumentRequest{Database: "db", Collection: "c", Id: 1})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_UpdateDocument_Success(t *testing.T) {
	collSvc := newMockCollectionService()
	client, cleanup := startTestServerFull(t, WithCollectionService(collSvc))
	defer cleanup()

	_, err := client.UpdateDocument(context.Background(), &pb.UpdateDocumentRequest{
		Database:   "testdb",
		Collection: "docs",
		Id:         1,
		Document:   `{"name":"updated"}`,
	})
	if err != nil {
		t.Fatalf("UpdateDocument failed: %v", err)
	}
}

func TestGRPC_UpdateDocument_InvalidJSON(t *testing.T) {
	collSvc := newMockCollectionService()
	client, cleanup := startTestServerFull(t, WithCollectionService(collSvc))
	defer cleanup()

	_, err := client.UpdateDocument(context.Background(), &pb.UpdateDocumentRequest{
		Database: "testdb", Collection: "docs", Id: 1,
		Document: "{{invalid}}",
	})
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestGRPC_UpdateDocument_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.UpdateDocument(context.Background(), &pb.UpdateDocumentRequest{
		Database: "db", Collection: "c", Id: 1, Document: `{}`,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_DeleteDocument_Success(t *testing.T) {
	collSvc := newMockCollectionService()
	client, cleanup := startTestServerFull(t, WithCollectionService(collSvc))
	defer cleanup()

	_, err := client.DeleteDocument(context.Background(), &pb.DeleteDocumentRequest{
		Database:   "testdb",
		Collection: "docs",
		Id:         1,
	})
	if err != nil {
		t.Fatalf("DeleteDocument failed: %v", err)
	}
}

func TestGRPC_DeleteDocument_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.DeleteDocument(context.Background(), &pb.DeleteDocumentRequest{Database: "db", Collection: "c", Id: 1})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_ListDocuments_Success(t *testing.T) {
	collSvc := newMockCollectionService()
	client, cleanup := startTestServerFull(t, WithCollectionService(collSvc))
	defer cleanup()

	resp, err := client.ListDocuments(context.Background(), &pb.ListDocumentsRequest{
		Database:   "testdb",
		Collection: "docs",
		PageSize:   10,
	})
	if err != nil {
		t.Fatalf("ListDocuments failed: %v", err)
	}
	if len(resp.GetDocuments()) != 1 {
		t.Fatalf("expected 1 document, got %d", len(resp.GetDocuments()))
	}
	if resp.GetTotalCount() != 1 {
		t.Fatalf("expected total_count=1, got %d", resp.GetTotalCount())
	}
}

func TestGRPC_ListDocuments_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.ListDocuments(context.Background(), &pb.ListDocumentsRequest{Database: "db", Collection: "c"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// Collection error type tests
func TestDocumentUnmarshalError_Error(t *testing.T) {
	e := &DocumentUnmarshalError{Err: errors.New("json error")}
	msg := e.Error()
	if !strings.Contains(msg, "unmarshal") {
		t.Fatalf("expected message to contain 'unmarshal', got %q", msg)
	}
	if !strings.Contains(msg, "json error") {
		t.Fatalf("expected message to contain 'json error', got %q", msg)
	}
}

func TestDocumentUnmarshalError_Unwrap(t *testing.T) {
	inner := errors.New("inner")
	e := &DocumentUnmarshalError{Err: inner}
	if e.Unwrap() != inner {
		t.Fatal("Unwrap did not return inner error")
	}
}

func TestDocumentMarshalError_Error(t *testing.T) {
	e := &DocumentMarshalError{Err: errors.New("marshal error")}
	msg := e.Error()
	if !strings.Contains(msg, "marshal") {
		t.Fatalf("expected message to contain 'marshal', got %q", msg)
	}
}

func TestDocumentMarshalError_Unwrap(t *testing.T) {
	inner := errors.New("inner")
	e := &DocumentMarshalError{Err: inner}
	if e.Unwrap() != inner {
		t.Fatal("Unwrap did not return inner error")
	}
}

// collectionInfoToProto helper test
func TestCollectionInfoToProto(t *testing.T) {
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	info := &service.CollectionInfo{
		Name:          "docs",
		Database:      "testdb",
		IDStrategy:    "uuid",
		FieldHash:     "sha256",
		DocumentCount: 42,
		CreatedAt:     ts,
	}
	result := collectionpb.InfoToProto(info)
	if result.GetName() != "docs" {
		t.Fatalf("expected name 'docs', got %q", result.GetName())
	}
	if result.GetDocumentCount() != 42 {
		t.Fatalf("expected count 42, got %d", result.GetDocumentCount())
	}
	if result.GetIdStrategy() != "uuid" {
		t.Fatalf("expected id_strategy 'uuid', got %q", result.GetIdStrategy())
	}
}

// ---------------------------------------------------------------------------
// Server.Start TLS path coverage
// ---------------------------------------------------------------------------

func TestServer_Start_WithTLS_NilConfig(t *testing.T) {
	// WithTLS(nil) leaves tls unset. The gRPC transport is TLS-only, so Start
	// fails closed with a *TLSNotConfiguredError rather than serve plaintext.
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithTLS(nil))
	err := srv.Start()
	var tlsErr *TLSNotConfiguredError
	if !errors.As(err, &tlsErr) {
		srv.Stop()
		t.Fatalf("expected *TLSNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// domainRaftConfigToProto coverage (via DatabaseUpdateRaft with raft data)
// ---------------------------------------------------------------------------

func TestDatabaseInfoToProto_WithRaftMulticastAndDNS(t *testing.T) {
	info := &database.DatabaseInfo{
		Name: "db",
		Raft: &database.DatabaseRaftConfig{
			ShardID: 2,
			Multicast: &database.MulticastDiscoveryConfig{
				MulticastGroup: "239.0.0.1",
			},
			DNS: &database.DNSDiscoveryConfig{
				Domain: "local",
			},
			Token: &database.TokenDiscoveryConfig{
				Endpoint:    "http://ep",
				ClusterSize: 5,
			},
		},
	}
	result := databaseInfoToProto(info)
	pbRaft := result.GetRaft()
	if pbRaft == nil {
		t.Fatal("expected non-nil Raft in proto response")
	}
	if pbRaft.GetMulticast() == nil {
		t.Fatal("expected non-nil Multicast in raft proto")
	}
	if pbRaft.GetDns() == nil {
		t.Fatal("expected non-nil DNS in raft proto")
	}
	if pbRaft.GetToken() == nil {
		t.Fatal("expected non-nil Token in raft proto")
	}
}

// ---------------------------------------------------------------------------
// TimeSeries mock extensions — ListMetrics and GetRetentionPolicy
// ---------------------------------------------------------------------------

func TestGRPC_TSGetRetention_Error(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	// GetRetentionPolicy returns nil,nil which causes a nil pointer dereference
	// in the handler — this exercises the error path when the service returns nil policy.
	// The mock returns nil so let's inject an error via a custom wrapper.
	errTSSvc := &errorTSService{inner: tsSvc, retentionErr: errors.New("not found")}
	client2, cleanup2 := startTestServerFull(t, WithTimeSeries(errTSSvc))
	defer cleanup2()

	_, err := client2.TSGetRetention(context.Background(), &pb.TSGetRetentionRequest{Metric: "cpu"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	_ = client // suppress unused warning
}

// errorTSService wraps mockTimeSeriesService to inject errors for specific methods.
type errorTSService struct {
	inner         *mockTimeSeriesService
	retentionErr  error
	setRetErr     error
	listMetErr    error
	writePointErr error
	richQueryErr  error
	deleteSerErr  error
}

func (e *errorTSService) Write(ctx context.Context, m string, v float64, ts time.Time, tags map[string]string) error {
	return e.inner.Write(ctx, m, v, ts, tags)
}
func (e *errorTSService) WriteBatch(ctx context.Context, pts []timeseries.DataPoint) error {
	return e.inner.WriteBatch(ctx, pts)
}
func (e *errorTSService) Query(ctx context.Context, m string, s, end time.Time, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	return e.inner.Query(ctx, m, s, end, opts...)
}
func (e *errorTSService) Aggregate(ctx context.Context, m string, s, end time.Time, fn timeseries.AggFunc, interval time.Duration) ([]timeseries.AggResult, error) {
	return e.inner.Aggregate(ctx, m, s, end, fn, interval)
}
func (e *errorTSService) WritePoint(ctx context.Context, dp *timeseries.DataPoint) error {
	if e.writePointErr != nil {
		return e.writePointErr
	}
	return e.inner.WritePoint(ctx, dp)
}
func (e *errorTSService) QueryRich(ctx context.Context, req *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
	if e.richQueryErr != nil {
		return nil, e.richQueryErr
	}
	return e.inner.QueryRich(ctx, req)
}
func (e *errorTSService) DeleteSeries(ctx context.Context, req *timeseries.DeleteRequest) error {
	if e.deleteSerErr != nil {
		return e.deleteSerErr
	}
	return e.inner.DeleteSeries(ctx, req)
}
func (e *errorTSService) GetRetentionPolicy(metric string) (*timeseries.RetentionPolicy, error) {
	if e.retentionErr != nil {
		return nil, e.retentionErr
	}
	return e.inner.GetRetentionPolicy(metric)
}
func (e *errorTSService) SetRetentionPolicy(metric string, p *timeseries.RetentionPolicy) error {
	if e.setRetErr != nil {
		return e.setRetErr
	}
	return e.inner.SetRetentionPolicy(metric, p)
}
func (e *errorTSService) ListMetrics(prefix string, limit int) ([]string, error) {
	if e.listMetErr != nil {
		return nil, e.listMetErr
	}
	return e.inner.ListMetrics(prefix, limit)
}

func (e *errorTSService) QueryWithConsistency(ctx context.Context, metric string, start, end time.Time, _ consistency.ReadOptions, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	return e.inner.Query(ctx, metric, start, end, opts...)
}

func (e *errorTSService) AggregateWithConsistency(ctx context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration, _ consistency.ReadOptions) ([]timeseries.AggResult, error) {
	return e.inner.Aggregate(ctx, metric, start, end, fn, interval)
}

func (e *errorTSService) QueryRichWithConsistency(ctx context.Context, req *timeseries.QueryRequest, _ consistency.ReadOptions) (*timeseries.QueryResult, error) {
	return e.inner.QueryRich(ctx, req)
}

func (e *errorTSService) ListMetricsWithConsistency(_ context.Context, prefix string, limit int, _ consistency.ReadOptions) ([]string, error) {
	return e.inner.ListMetrics(prefix, limit)
}
