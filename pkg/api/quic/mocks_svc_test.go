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
	"crypto/x509"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// stubCeremonyCA
// ---------------------------------------------------------------------------

// stubCeremonyCA is a minimal ceremony.CeremonyCA implementation for tests
// that need to prove a real (non-nil) CA reference reaches the ceremony
// service layer, without exercising actual certificate issuance.
type stubCeremonyCA struct{}

var _ ceremony.CeremonyCA = (*stubCeremonyCA)(nil)

func (stubCeremonyCA) SignCSR(_ context.Context, _ []byte, _ *ceremony.SignCSROptions) (*x509.Certificate, error) {
	return nil, nil
}
func (stubCeremonyCA) CACertificate() (*x509.Certificate, error) { return nil, nil }
func (stubCeremonyCA) CABundle() ([]byte, error)                 { return []byte("ca-bundle"), nil }
func (stubCeremonyCA) IssueCertificate(_ context.Context, _ *ceremony.IssueCertRequest) (*ceremony.IssuedCert, error) {
	return &ceremony.IssuedCert{CertPEM: []byte("cert-pem"), KeyPEM: []byte("key-pem")}, nil
}

// ---------------------------------------------------------------------------
// mockBarrierService
// ---------------------------------------------------------------------------

type mockBarrierService struct {
	service.BarrierServicer
	barrierInitializeFn         func(ctx context.Context, secret []byte) error
	barrierUnsealFn             func(ctx context.Context, secret []byte) error
	barrierSealFn               func(ctx context.Context) error
	barrierStatusFn             func(ctx context.Context) (*service.BarrierStatusInfo, error)
	barrierInitializeShamirFn   func(ctx context.Context, threshold, totalShares int, policy seal.FactorPolicy) (*service.ShamirInitInfo, error)
	barrierUnsealWithSharesFn   func(ctx context.Context, shares []string) error
	barrierGenRecoveryKeysFn    func(ctx context.Context, threshold int, policy seal.FactorPolicy) (*service.RecoveryKeysInfo, error)
	barrierRecoverWithKeysFn    func(ctx context.Context, keys []string) error
	barrierDeleteRecoveryKeysFn func(ctx context.Context) error
	barrierHasRecoveryKeysFn    func(ctx context.Context) (bool, error)
	barrierGenerateRootTokenFn  func(ctx context.Context, shares []string) (*service.RootTokenInfo, error)
}

func (m *mockBarrierService) BarrierInitialize(ctx context.Context, secret []byte) error {
	if m.barrierInitializeFn != nil {
		return m.barrierInitializeFn(ctx, secret)
	}
	return nil
}

func (m *mockBarrierService) BarrierUnseal(ctx context.Context, secret []byte) error {
	if m.barrierUnsealFn != nil {
		return m.barrierUnsealFn(ctx, secret)
	}
	return nil
}

func (m *mockBarrierService) BarrierSeal(ctx context.Context) error {
	if m.barrierSealFn != nil {
		return m.barrierSealFn(ctx)
	}
	return nil
}

func (m *mockBarrierService) BarrierInitializeShamir(ctx context.Context, threshold, totalShares int, policy seal.FactorPolicy) (*service.ShamirInitInfo, error) {
	if m.barrierInitializeShamirFn != nil {
		return m.barrierInitializeShamirFn(ctx, threshold, totalShares, policy)
	}
	return &service.ShamirInitInfo{ShareCount: totalShares, Threshold: threshold, TotalShares: totalShares}, nil
}

func (m *mockBarrierService) BarrierGenerateRecoveryKeys(ctx context.Context, threshold int, policy seal.FactorPolicy) (*service.RecoveryKeysInfo, error) {
	if m.barrierGenRecoveryKeysFn != nil {
		return m.barrierGenRecoveryKeysFn(ctx, threshold, policy)
	}
	return &service.RecoveryKeysInfo{ShareCount: threshold + 1, Threshold: threshold, Total: threshold + 1}, nil
}

func (m *mockBarrierService) BarrierStatus(ctx context.Context) (*service.BarrierStatusInfo, error) {
	if m.barrierStatusFn != nil {
		return m.barrierStatusFn(ctx)
	}
	return &service.BarrierStatusInfo{
		Sealed:         false,
		Strategy:       "shamir",
		HardwareBacked: false,
		InitializedAt:  time.Now(),
	}, nil
}

func (m *mockBarrierService) BarrierDeleteRecoveryKeys(ctx context.Context) error {
	if m.barrierDeleteRecoveryKeysFn != nil {
		return m.barrierDeleteRecoveryKeysFn(ctx)
	}
	return nil
}

func (m *mockBarrierService) BarrierHasRecoveryKeys(ctx context.Context) (bool, error) {
	if m.barrierHasRecoveryKeysFn != nil {
		return m.barrierHasRecoveryKeysFn(ctx)
	}
	return true, nil
}

// ---------------------------------------------------------------------------
// mockCeremonyService
// ---------------------------------------------------------------------------

type mockCeremonyService struct {
	ceremonyInitializeFn func(ctx context.Context, ca ceremony.CeremonyCA) (*service.CeremonyInitResult, error)
	ceremonyClaimShareFn func(ctx context.Context, username string) ([]byte, error)
	ceremonySignCSRFn    func(ctx context.Context, username, soPin, csrPEM, role string) (*service.CeremonySignCSRResult, error)
	ceremonyStatusFn     func(ctx context.Context) (*service.CeremonyStatusInfo, error)
	capturedInitOpts     *service.CeremonyInitializeOptions
	capturedCA           ceremony.CeremonyCA
}

func (m *mockCeremonyService) CeremonyInitialize(ctx context.Context, ca ceremony.CeremonyCA, opts *service.CeremonyInitializeOptions) (*service.CeremonyInitResult, error) {
	m.capturedInitOpts = opts
	m.capturedCA = ca
	if m.ceremonyInitializeFn != nil {
		return m.ceremonyInitializeFn(ctx, ca)
	}
	return &service.CeremonyInitResult{
		SPKIPin:    "sha256/abc123",
		CACertPEM:  []byte("ca-cert-pem"),
		TLSCertPEM: []byte("tls-cert-pem"),
		State:      "initialized",
		ShareCount: 3,
	}, nil
}

func (m *mockCeremonyService) CeremonySignCSR(ctx context.Context, username, soPin, csrPEM, role string) (*service.CeremonySignCSRResult, error) {
	if m.ceremonySignCSRFn != nil {
		return m.ceremonySignCSRFn(ctx, username, soPin, csrPEM, role)
	}
	return &service.CeremonySignCSRResult{CertPEM: []byte("signed-cert-pem")}, nil
}

func (m *mockCeremonyService) CeremonyStatus(ctx context.Context) (*service.CeremonyStatusInfo, error) {
	if m.ceremonyStatusFn != nil {
		return m.ceremonyStatusFn(ctx)
	}
	return &service.CeremonyStatusInfo{State: "initialized", SPKIPin: "sha256/abc123"}, nil
}

// ---------------------------------------------------------------------------
// mockCollectionService
// ---------------------------------------------------------------------------

type mockCollectionService struct {
	createCollectionFn  func(ctx context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error)
	dropCollectionFn    func(ctx context.Context, req *service.DropCollectionRequest) error
	listCollectionsFn   func(ctx context.Context, req *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error)
	getCollectionInfoFn func(ctx context.Context, database, name string) (*service.CollectionInfo, error)
	insertDocumentFn    func(ctx context.Context, req *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error)
	getDocumentFn       func(ctx context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error)
	updateDocumentFn    func(ctx context.Context, req *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error)
	deleteDocumentFn    func(ctx context.Context, req *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error)
	listDocumentsFn     func(ctx context.Context, req *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error)
}

func (m *mockCollectionService) CreateCollection(ctx context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
	if m.createCollectionFn != nil {
		return m.createCollectionFn(ctx, req)
	}
	return &service.CollectionInfo{
		Name:     req.Name,
		Database: req.Database,
	}, nil
}

func (m *mockCollectionService) DropCollection(ctx context.Context, req *service.DropCollectionRequest) error {
	if m.dropCollectionFn != nil {
		return m.dropCollectionFn(ctx, req)
	}
	return nil
}

func (m *mockCollectionService) ListCollections(ctx context.Context, req *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
	if m.listCollectionsFn != nil {
		return m.listCollectionsFn(ctx, req)
	}
	return &service.ListCollectionsResponse{Collections: []service.CollectionInfo{}}, nil
}

func (m *mockCollectionService) GetCollectionInfo(ctx context.Context, db, name string) (*service.CollectionInfo, error) {
	if m.getCollectionInfoFn != nil {
		return m.getCollectionInfoFn(ctx, db, name)
	}
	return &service.CollectionInfo{Name: name, Database: db}, nil
}

func (m *mockCollectionService) InsertDocument(ctx context.Context, req *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
	if m.insertDocumentFn != nil {
		return m.insertDocumentFn(ctx, req)
	}
	return &service.InsertDocumentResponse{ID: 123}, nil
}

func (m *mockCollectionService) GetDocument(ctx context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
	if m.getDocumentFn != nil {
		return m.getDocumentFn(ctx, req)
	}
	return &service.GetDocumentResponse{ID: req.ID, Document: map[string]interface{}{"key": "value"}}, nil
}

func (m *mockCollectionService) UpdateDocument(ctx context.Context, req *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error) {
	if m.updateDocumentFn != nil {
		return m.updateDocumentFn(ctx, req)
	}
	return &service.UpdateDocumentResponse{}, nil
}

// UpsertDocument reports an insert: no test that uses this double drives
// the guarded upsert, which is exercised against the real service.
func (m *mockCollectionService) UpsertDocument(_ context.Context, req *service.UpsertDocumentRequest) (*service.UpsertDocumentResponse, error) {
	return &service.UpsertDocumentResponse{ID: req.ID}, nil
}

func (m *mockCollectionService) DeleteDocument(ctx context.Context, req *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error) {
	if m.deleteDocumentFn != nil {
		return m.deleteDocumentFn(ctx, req)
	}
	return &service.DeleteDocumentResponse{}, nil
}

func (m *mockCollectionService) ListDocuments(ctx context.Context, req *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
	if m.listDocumentsFn != nil {
		return m.listDocumentsFn(ctx, req)
	}
	return &service.ListDocumentsResponse{Documents: []service.GetDocumentResponse{}, NextPageToken: "", TotalCount: 0}, nil
}

// ---------------------------------------------------------------------------
// mockDatabaseService
// ---------------------------------------------------------------------------

type mockDatabaseService struct {
	createFn                    func(cfg database.DatabaseConfig) (*database.DatabaseInfo, error)
	getFn                       func(name string) (*database.DatabaseInfo, error)
	listFn                      func() ([]database.DatabaseInfo, error)
	dropFn                      func(name string) error
	updateProtocolFn            func(name string, protocol database.ProtocolConfig) (*database.DatabaseInfo, error)
	updateEtcdFn                func(name string, etcd *database.EtcdConfig) (*database.DatabaseInfo, error)
	updateRaftFn                func(name string, raft *database.DatabaseRaftConfig) (*database.DatabaseInfo, error)
	updateBarrierFn             func(name string, barrier *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error)
	updateConnectionFn          func(name string, conn *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error)
	sealDatabaseFn              func(ctx context.Context, name string) error
	unsealDatabaseFn            func(ctx context.Context, name string, creds seal.Credentials) error
	databaseBarrierStatusFn     func(name string) (*seal.BarrierStatus, error)
	createForTenantFn           func(ctx context.Context, cfg database.DatabaseConfig) (*database.DatabaseInfo, error)
	getForTenantFn              func(ctx context.Context, name string) (*database.DatabaseInfo, error)
	listForTenantFn             func(ctx context.Context) ([]database.DatabaseInfo, error)
	dropForTenantFn             func(ctx context.Context, name string) error
	updateProtocolForTenantFn   func(ctx context.Context, name string, protocol database.ProtocolConfig) (*database.DatabaseInfo, error)
	updateEtcdForTenantFn       func(ctx context.Context, name string, etcd *database.EtcdConfig) (*database.DatabaseInfo, error)
	updateRaftForTenantFn       func(ctx context.Context, name string, raft *database.DatabaseRaftConfig) (*database.DatabaseInfo, error)
	updateBarrierForTenantFn    func(ctx context.Context, name string, barrier *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error)
	updateConnectionForTenantFn func(ctx context.Context, name string, conn *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error)
}

func (m *mockDatabaseService) Create(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	if m.createFn != nil {
		return m.createFn(cfg)
	}
	return &database.DatabaseInfo{Name: cfg.Name}, nil
}

func (m *mockDatabaseService) Get(name string) (*database.DatabaseInfo, error) {
	if m.getFn != nil {
		return m.getFn(name)
	}
	return &database.DatabaseInfo{Name: name}, nil
}

func (m *mockDatabaseService) List() ([]database.DatabaseInfo, error) {
	if m.listFn != nil {
		return m.listFn()
	}
	return []database.DatabaseInfo{}, nil
}

func (m *mockDatabaseService) Drop(_ context.Context, name string) error {
	if m.dropFn != nil {
		return m.dropFn(name)
	}
	return nil
}

func (m *mockDatabaseService) UpdateProtocol(name string, protocol database.ProtocolConfig) (*database.DatabaseInfo, error) {
	if m.updateProtocolFn != nil {
		return m.updateProtocolFn(name, protocol)
	}
	return &database.DatabaseInfo{Name: name}, nil
}

func (m *mockDatabaseService) UpdateEtcd(name string, etcd *database.EtcdConfig) (*database.DatabaseInfo, error) {
	if m.updateEtcdFn != nil {
		return m.updateEtcdFn(name, etcd)
	}
	return &database.DatabaseInfo{Name: name}, nil
}

func (m *mockDatabaseService) UpdateRaft(name string, raft *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	if m.updateRaftFn != nil {
		return m.updateRaftFn(name, raft)
	}
	return &database.DatabaseInfo{Name: name}, nil
}

func (m *mockDatabaseService) UpdateBarrier(name string, barrier *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	if m.updateBarrierFn != nil {
		return m.updateBarrierFn(name, barrier)
	}
	return &database.DatabaseInfo{Name: name}, nil
}

func (m *mockDatabaseService) UpdateConnection(name string, conn *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	if m.updateConnectionFn != nil {
		return m.updateConnectionFn(name, conn)
	}
	return &database.DatabaseInfo{Name: name}, nil
}

func (m *mockDatabaseService) SealDatabase(ctx context.Context, name string) error {
	if m.sealDatabaseFn != nil {
		return m.sealDatabaseFn(ctx, name)
	}
	return nil
}

func (m *mockDatabaseService) UnsealDatabase(ctx context.Context, name string, creds seal.Credentials) error {
	if m.unsealDatabaseFn != nil {
		return m.unsealDatabaseFn(ctx, name, creds)
	}
	return nil
}

func (m *mockDatabaseService) DatabaseBarrierStatus(_ context.Context, name string) (*seal.BarrierStatus, error) {
	if m.databaseBarrierStatusFn != nil {
		return m.databaseBarrierStatusFn(name)
	}
	return &seal.BarrierStatus{}, nil
}

// The *ForTenant methods honor an explicit *ForTenantFn when set; otherwise
// they delegate to the plain method, mirroring the real DatabaseService's
// scope routing for the system tenant (the test context carries no tenant).
func (m *mockDatabaseService) CreateForTenant(ctx context.Context, cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	if m.createForTenantFn != nil {
		return m.createForTenantFn(ctx, cfg)
	}
	return m.Create(cfg)
}

func (m *mockDatabaseService) GetForTenant(ctx context.Context, name string) (*database.DatabaseInfo, error) {
	if m.getForTenantFn != nil {
		return m.getForTenantFn(ctx, name)
	}
	return m.Get(name)
}

func (m *mockDatabaseService) ListForTenant(ctx context.Context) ([]database.DatabaseInfo, error) {
	if m.listForTenantFn != nil {
		return m.listForTenantFn(ctx)
	}
	return m.List()
}

func (m *mockDatabaseService) DropForTenant(ctx context.Context, name string) error {
	if m.dropForTenantFn != nil {
		return m.dropForTenantFn(ctx, name)
	}
	return m.Drop(context.Background(), name)
}

func (m *mockDatabaseService) UpdateProtocolForTenant(ctx context.Context, name string, protocol database.ProtocolConfig) (*database.DatabaseInfo, error) {
	if m.updateProtocolForTenantFn != nil {
		return m.updateProtocolForTenantFn(ctx, name, protocol)
	}
	return m.UpdateProtocol(name, protocol)
}

func (m *mockDatabaseService) UpdateEtcdForTenant(ctx context.Context, name string, etcd *database.EtcdConfig) (*database.DatabaseInfo, error) {
	if m.updateEtcdForTenantFn != nil {
		return m.updateEtcdForTenantFn(ctx, name, etcd)
	}
	return m.UpdateEtcd(name, etcd)
}

func (m *mockDatabaseService) UpdateRaftForTenant(ctx context.Context, name string, raft *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	if m.updateRaftForTenantFn != nil {
		return m.updateRaftForTenantFn(ctx, name, raft)
	}
	return m.UpdateRaft(name, raft)
}

func (m *mockDatabaseService) UpdateBarrierForTenant(ctx context.Context, name string, barrier *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	if m.updateBarrierForTenantFn != nil {
		return m.updateBarrierForTenantFn(ctx, name, barrier)
	}
	return m.UpdateBarrier(name, barrier)
}

func (m *mockDatabaseService) UpdateConnectionForTenant(ctx context.Context, name string, conn *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	if m.updateConnectionForTenantFn != nil {
		return m.updateConnectionForTenantFn(ctx, name, conn)
	}
	return m.UpdateConnection(name, conn)
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

func (m *mockDatabaseService) ScaleForTenant(_ context.Context, name string, targetReplicas int) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name, Replicas: targetReplicas}, nil
}
