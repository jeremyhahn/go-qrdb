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
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/jeremyhahn/go-qrdb/pkg/api/dbwire"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/internal/collectionpb"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// ---------------------------------------------------------------------------
// Mock BarrierServicer
// ---------------------------------------------------------------------------

type mockBarrierService struct {
	service.BarrierServicer
	initErr           error
	unsealErr         error
	sealErr           error
	statusResult      *service.BarrierStatusInfo
	statusErr         error
	shamirInitResult  *service.ShamirInitInfo
	shamirInitErr     error
	recoveryGenResult *service.RecoveryKeysInfo
	recoveryGenErr    error
	recoveryDeleteErr error
	recoveryHasResult bool
	recoveryHasErr    error
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
		recoveryGenResult: &service.RecoveryKeysInfo{Threshold: 1, Total: 1, ShareCount: 1},
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

func (m *mockBarrierService) BarrierInitializeShamir(_ context.Context, _, _ int, _ seal.FactorPolicy) (*service.ShamirInitInfo, error) {
	return m.shamirInitResult, m.shamirInitErr
}
func (m *mockBarrierService) BarrierGenerateRecoveryKeys(_ context.Context, _ int, _ seal.FactorPolicy) (*service.RecoveryKeysInfo, error) {
	return m.recoveryGenResult, m.recoveryGenErr
}
func (m *mockBarrierService) BarrierDeleteRecoveryKeys(_ context.Context) error {
	return m.recoveryDeleteErr
}

// BarrierInitializePrecondition admits every initialize: the double's
// initialize paths answer for themselves through their canned results.
func (m *mockBarrierService) BarrierInitializePrecondition(_ context.Context) error { return nil }

// BarrierClusterUnseal answers as a node without cluster sealing does.
func (m *mockBarrierService) BarrierClusterUnseal(context.Context) (*clusterseal.Report, error) {
	return nil, clusterseal.ErrInactive
}

func (m *mockBarrierService) BarrierHasRecoveryKeys(_ context.Context) (bool, error) {
	if m.recoveryHasErr != nil {
		return false, m.recoveryHasErr
	}
	return m.recoveryHasResult, nil
}

// ---------------------------------------------------------------------------
// Mock CeremonyServicer
// ---------------------------------------------------------------------------

type mockCeremonyService struct {
	initResult       *service.CeremonyInitResult
	initErr          error
	capturedInitOpts *service.CeremonyInitializeOptions
	capturedSecret   []byte
	capturedCA       ceremony.CeremonyCA
	statusResult     *service.CeremonyStatusInfo
	statusErr        error
	beginErr         error
	completeErr      error
	claimShareResult []byte
	claimShareErr    error
	signCSRResult    *service.CeremonySignCSRResult
	signCSRErr       error
}

func newMockCeremonyService() *mockCeremonyService {
	return &mockCeremonyService{
		initResult: &service.CeremonyInitResult{
			SPKIPin:               "pin123",
			CACertPEM:             []byte("ca-cert"),
			TLSCertPEM:            []byte("tls-cert"),
			State:                 "initialized",
			ShareCount:            3,
			AdminCertPEM:          []byte("admin-cert"),
			AdminKeyPEM:           []byte("admin-key"),
			AdminCredentialSource: "generated",
		},
		statusResult:     &service.CeremonyStatusInfo{State: "initialized", SPKIPin: "pin123"},
		claimShareResult: []byte("share-value"),
		signCSRResult:    &service.CeremonySignCSRResult{CertPEM: []byte("signed-cert")},
	}
}

func (m *mockCeremonyService) CeremonyInitialize(_ context.Context, ca ceremony.CeremonyCA, opts *service.CeremonyInitializeOptions) (*service.CeremonyInitResult, error) {
	// The handler destroys opts.Secret once it has finished using it, which
	// runs before this call returns to the test -- capturedSecret clones the
	// plaintext out while the Buffer is still live, so the test can still
	// inspect what was forwarded after the handler's own Destroy has already
	// run.
	captured := *opts
	m.capturedSecret = nil
	if opts.Secret != nil {
		_ = opts.Secret.WithBytes(func(b []byte) error {
			m.capturedSecret = append([]byte(nil), b...)
			return nil
		})
	}
	m.capturedInitOpts = &captured
	m.capturedCA = ca
	if m.initErr != nil {
		return nil, m.initErr
	}
	return m.initResult, nil
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
		createResult: &database.DatabaseInfo{Name: "testdb", Backend: "pebble"},
		getResult:    &database.DatabaseInfo{Name: "testdb", Backend: "pebble"},
		listResult:   []database.DatabaseInfo{{Name: "testdb"}},
		updateResult: &database.DatabaseInfo{Name: "testdb", Backend: "pebble"},
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

// DatabaseBarrierStatus adapts databaseBarrierStatusSeal to the service
// interface, with no cluster seal state.
func (m *mockDatabaseService) DatabaseBarrierStatus(ctx context.Context, name string) (*service.DatabaseBarrierStatusInfo, error) {
	st, err := m.databaseBarrierStatusSeal(ctx, name)
	if st == nil {
		return nil, err
	}
	return &service.DatabaseBarrierStatusInfo{BarrierStatus: st}, err
}

func (m *mockDatabaseService) databaseBarrierStatusSeal(_ context.Context, _ string) (*seal.BarrierStatus, error) {
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

func (m *mockDatabaseService) UpdateSQL(name string, sqlCfg *database.SQLConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name, SQL: sqlCfg}, nil
}

func (m *mockDatabaseService) UpdateSQLForTenant(_ context.Context, name string, sqlCfg *database.SQLConfig) (*database.DatabaseInfo, error) {
	return m.UpdateSQL(name, sqlCfg)
}

func (m *mockDatabaseService) UpdateCompactionForTenant(_ context.Context, name string, policy *compaction.Policy) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name, Compaction: policy}, nil
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
// richQueryTSService returns non-nil QueryResult for TSRichQuery tests.
// ---------------------------------------------------------------------------

type richQueryTSService struct {
	mockTimeSeriesService
	result *timeseries.QueryResult
}

func (m *richQueryTSService) QueryRich(_ context.Context, _ *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
	return m.result, nil
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
	if cfg.Raft.ReplicaID != 1 {
		t.Fatalf("expected replica_id=1, got %d", cfg.Raft.ReplicaID)
	}
	// The wire carries no shard ID: the cluster issues it at create.
	if cfg.Raft.ShardID != 0 {
		t.Fatalf("a create request produced shard ID %d", cfg.Raft.ShardID)
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
	result := dbwire.InfoProto(info)
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
	result := dbwire.InfoProto(info)
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
func (e *errorTSService) SetRetentionPolicy(ctx context.Context, metric string, p *timeseries.RetentionPolicy) error {
	if e.setRetErr != nil {
		return e.setRetErr
	}
	return e.inner.SetRetentionPolicy(ctx, metric, p)
}
func (e *errorTSService) ListMetrics(prefix string, limit int) ([]string, error) {
	if e.listMetErr != nil {
		return nil, e.listMetErr
	}
	return e.inner.ListMetrics(prefix, limit)
}
func (e *errorTSService) ApplyRetention(ctx context.Context, metric string, cutoff time.Time) error {
	return e.inner.ApplyRetention(ctx, metric, cutoff)
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
