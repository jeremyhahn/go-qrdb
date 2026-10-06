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
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Minimal barrier service mock
// ---------------------------------------------------------------------------

type barrierSvcMini struct {
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

func (m *barrierSvcMini) BarrierInitializeShamir(_ context.Context, _, _ int, _ seal.FactorPolicy) (*service.ShamirInitInfo, error) {
	return m.shamirInitResult, m.shamirInitErr
}

func (m *barrierSvcMini) BarrierGenerateRecoveryKeys(_ context.Context, _ int, _ seal.FactorPolicy) (*service.RecoveryKeysInfo, error) {
	return m.recoveryGenResult, m.recoveryGenErr
}

func newBarrierSvcMini() *barrierSvcMini {
	return &barrierSvcMini{
		statusResult: &service.BarrierStatusInfo{
			Sealed:        false,
			Strategy:      "software",
			InitializedAt: time.Now(),
		},
		shamirInitResult:  &service.ShamirInitInfo{Threshold: 2, TotalShares: 3, ShareCount: 2},
		recoveryGenResult: &service.RecoveryKeysInfo{Threshold: 1, Total: 1, ShareCount: 1},
	}
}

func (m *barrierSvcMini) BarrierInitialize(_ context.Context, _ []byte) error { return m.initErr }
func (m *barrierSvcMini) BarrierUnseal(_ context.Context, _ []byte) error     { return m.unsealErr }
func (m *barrierSvcMini) BarrierSeal(_ context.Context) error                 { return m.sealErr }
func (m *barrierSvcMini) BarrierStatus(_ context.Context) (*service.BarrierStatusInfo, error) {
	return m.statusResult, m.statusErr
}
func (m *barrierSvcMini) BarrierDeleteRecoveryKeys(_ context.Context) error {
	return m.recoveryDeleteErr
}

// BarrierInitialized reports the double as never initialized: its
// initialize paths answer for themselves through their canned results.
func (m *barrierSvcMini) BarrierInitialized(_ context.Context) (bool, error) { return false, nil }

// BarrierClusterUnseal answers as a node without cluster sealing does.
func (m *barrierSvcMini) BarrierClusterUnseal(context.Context) (*clusterseal.Report, error) {
	return nil, clusterseal.ErrInactive
}

func (m *barrierSvcMini) BarrierHasRecoveryKeys(_ context.Context) (bool, error) {
	return m.recoveryHasResult, m.recoveryHasErr
}

// ---------------------------------------------------------------------------
// Minimal ceremony service mock
// ---------------------------------------------------------------------------

type ceremonySvcMini struct {
	initResult       *service.CeremonyInitResult
	initErr          error
	statusResult     *service.CeremonyStatusInfo
	statusErr        error
	beginErr         error
	completeErr      error
	claimShareResult []byte
	claimShareErr    error
	signCSRResult    *service.CeremonySignCSRResult
	signCSRErr       error
	enrollExtErr     error
}

func newCeremonySvcMini() *ceremonySvcMini {
	return &ceremonySvcMini{
		initResult: &service.CeremonyInitResult{
			SPKIPin: "pin", CACertPEM: []byte("ca"), TLSCertPEM: []byte("tls"),
			State: "initialized", ShareCount: 3,
		},
		statusResult:     &service.CeremonyStatusInfo{State: "initialized", SPKIPin: "pin"},
		claimShareResult: []byte("share-data"),
		signCSRResult:    &service.CeremonySignCSRResult{CertPEM: []byte("signed")},
	}
}

func (m *ceremonySvcMini) CeremonyInitialize(_ context.Context, _ ceremony.CeremonyCA, _ *service.CeremonyInitializeOptions) (*service.CeremonyInitResult, error) {
	return m.initResult, m.initErr
}
func (m *ceremonySvcMini) CeremonyStatus(_ context.Context) (*service.CeremonyStatusInfo, error) {
	return m.statusResult, m.statusErr
}
func (m *ceremonySvcMini) CeremonySignCSR(_ context.Context, _, _, _, _ string) (*service.CeremonySignCSRResult, error) {
	return m.signCSRResult, m.signCSRErr
}

// ---------------------------------------------------------------------------
// Minimal database service mock
// ---------------------------------------------------------------------------

type databaseSvcMini struct {
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

func newDatabaseSvcMini() *databaseSvcMini {
	return &databaseSvcMini{
		createResult: &database.DatabaseInfo{Name: "testdb", Engine: "mvcc", Backend: "pebble"},
		getResult:    &database.DatabaseInfo{Name: "testdb", Engine: "mvcc", Backend: "pebble"},
		listResult:   []database.DatabaseInfo{{Name: "testdb"}},
		updateResult: &database.DatabaseInfo{Name: "testdb"},
	}
}

func (m *databaseSvcMini) Create(_ database.DatabaseConfig) (*database.DatabaseInfo, error) {
	return m.createResult, m.createErr
}
func (m *databaseSvcMini) Get(_ string) (*database.DatabaseInfo, error) {
	return m.getResult, m.getErr
}
func (m *databaseSvcMini) List() ([]database.DatabaseInfo, error) {
	return m.listResult, m.listErr
}
func (m *databaseSvcMini) Drop(_ context.Context, _ string) error { return m.dropErr }
func (m *databaseSvcMini) UpdateProtocol(_ string, _ database.ProtocolConfig) (*database.DatabaseInfo, error) {
	return m.updateResult, m.updateErr
}
func (m *databaseSvcMini) UpdateEtcd(_ string, _ *database.EtcdConfig) (*database.DatabaseInfo, error) {
	return m.updateResult, m.updateErr
}
func (m *databaseSvcMini) UpdateRaft(_ string, _ *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	return m.updateResult, m.updateErr
}
func (m *databaseSvcMini) UpdateBarrier(_ string, _ *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	return m.updateResult, m.updateErr
}
func (m *databaseSvcMini) UpdateConnection(_ string, _ *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	return m.updateResult, m.updateErr
}
func (m *databaseSvcMini) SealDatabase(_ context.Context, _ string) error { return nil }
func (m *databaseSvcMini) UnsealDatabase(_ context.Context, _ string, _ seal.Credentials) error {
	return nil
}

// DatabaseBarrierStatus adapts databaseBarrierStatusSeal to the service
// interface, with no cluster seal state.
func (m *databaseSvcMini) DatabaseBarrierStatus(ctx context.Context, name string) (*service.DatabaseBarrierStatusInfo, error) {
	st, err := m.databaseBarrierStatusSeal(ctx, name)
	if st == nil {
		return nil, err
	}
	return &service.DatabaseBarrierStatusInfo{BarrierStatus: st}, err
}

func (m *databaseSvcMini) databaseBarrierStatusSeal(_ context.Context, _ string) (*seal.BarrierStatus, error) {
	return nil, nil
}
func (m *databaseSvcMini) CreateForTenant(_ context.Context, _ database.DatabaseConfig) (*database.DatabaseInfo, error) {
	return m.createResult, m.createErr
}
func (m *databaseSvcMini) GetForTenant(_ context.Context, _ string) (*database.DatabaseInfo, error) {
	return m.getResult, m.getErr
}
func (m *databaseSvcMini) ListForTenant(_ context.Context) ([]database.DatabaseInfo, error) {
	return m.listResult, m.listErr
}
func (m *databaseSvcMini) DropForTenant(_ context.Context, _ string) error { return m.dropErr }
func (m *databaseSvcMini) UpdateProtocolForTenant(_ context.Context, _ string, _ database.ProtocolConfig) (*database.DatabaseInfo, error) {
	return m.updateResult, m.updateErr
}
func (m *databaseSvcMini) UpdateEtcdForTenant(_ context.Context, _ string, _ *database.EtcdConfig) (*database.DatabaseInfo, error) {
	return m.updateResult, m.updateErr
}
func (m *databaseSvcMini) UpdateRaftForTenant(_ context.Context, _ string, _ *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	return m.updateResult, m.updateErr
}
func (m *databaseSvcMini) UpdateBarrierForTenant(_ context.Context, _ string, _ *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	return m.updateResult, m.updateErr
}
func (m *databaseSvcMini) UpdateConnectionForTenant(_ context.Context, _ string, _ *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	return m.updateResult, m.updateErr
}

func (m *databaseSvcMini) UpdateSQL(name string, sqlCfg *database.SQLConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name, SQL: sqlCfg}, nil
}

func (m *databaseSvcMini) UpdateSQLForTenant(_ context.Context, name string, sqlCfg *database.SQLConfig) (*database.DatabaseInfo, error) {
	return m.UpdateSQL(name, sqlCfg)
}

func (m *databaseSvcMini) UpdateCompactionForTenant(_ context.Context, name string, policy *compaction.Policy) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name, Compaction: policy}, nil
}
func (m *databaseSvcMini) ScaleForTenant(_ context.Context, _ string, _ int) (*database.DatabaseInfo, error) {
	return m.updateResult, m.updateErr
}

// ---------------------------------------------------------------------------
// Minimal collection service mock
// ---------------------------------------------------------------------------

type collectionSvcMini struct {
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

func newCollectionSvcMini() *collectionSvcMini {
	return &collectionSvcMini{
		createResult: &service.CollectionInfo{Name: "docs", Database: "db", CreatedAt: time.Now()},
		listResult: &service.ListCollectionsResponse{
			Collections: []service.CollectionInfo{{Name: "docs", Database: "db"}},
		},
		insertResult: &service.InsertDocumentResponse{ID: 1},
		getDocResult: &service.GetDocumentResponse{ID: 1, Document: map[string]interface{}{"k": "v"}},
		listDocResult: &service.ListDocumentsResponse{
			Documents:  []service.GetDocumentResponse{{ID: 1, Document: map[string]interface{}{"k": "v"}}},
			TotalCount: 1,
		},
	}
}

func (m *collectionSvcMini) CreateCollection(_ context.Context, _ *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
	return m.createResult, m.createErr
}
func (m *collectionSvcMini) GetCollectionInfo(_ context.Context, _, _ string) (*service.CollectionInfo, error) {
	if m.getInfoErr != nil {
		return nil, m.getInfoErr
	}
	return m.createResult, nil
}
func (m *collectionSvcMini) ListCollections(_ context.Context, _ *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
	return m.listResult, m.listErr
}
func (m *collectionSvcMini) DropCollection(_ context.Context, _ *service.DropCollectionRequest) error {
	return m.dropErr
}
func (m *collectionSvcMini) InsertDocument(_ context.Context, _ *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
	return m.insertResult, m.insertErr
}
func (m *collectionSvcMini) GetDocument(_ context.Context, _ *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
	return m.getDocResult, m.getDocErr
}
func (m *collectionSvcMini) UpdateDocument(_ context.Context, _ *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error) {
	return &service.UpdateDocumentResponse{}, m.updateDocErr
}

// UpsertDocument reports an insert: no test that uses this double drives
// the guarded upsert, which is exercised against the real service.
func (m *collectionSvcMini) UpsertDocument(_ context.Context, req *service.UpsertDocumentRequest) (*service.UpsertDocumentResponse, error) {
	return &service.UpsertDocumentResponse{ID: req.ID}, nil
}
func (m *collectionSvcMini) DeleteDocument(_ context.Context, _ *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error) {
	return &service.DeleteDocumentResponse{}, m.deleteDocErr
}
func (m *collectionSvcMini) ListDocuments(_ context.Context, _ *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
	return m.listDocResult, m.listDocErr
}

// ---------------------------------------------------------------------------
// Minimal PIN backend for barrier PIN tests
// ---------------------------------------------------------------------------

type pinBackendMini struct {
	soPINSet   bool
	verifyErr  error
	setSOErr   error
	setUserErr error
}

func (m *pinBackendMini) Strategy() pin.StrategyID             { return "software" }
func (m *pinBackendMini) SetSOPIN(_, newSO string) error       { return m.setSOErr }
func (m *pinBackendMini) SetUserPIN(so, _ string) error        { return m.setUserErr }
func (m *pinBackendMini) ChangeSOPIN(_, _ string) error        { return nil }
func (m *pinBackendMini) ChangeUserPIN(_, _ string) error      { return nil }
func (m *pinBackendMini) VerifySOPIN(_ string) error           { return m.verifyErr }
func (m *pinBackendMini) VerifyUserPIN(_ string) error         { return nil }
func (m *pinBackendMini) IsInitialized() bool                  { return true }
func (m *pinBackendMini) SOPINSet() bool                       { return m.soPINSet }
func (m *pinBackendMini) UserPINSet() bool                     { return false }
func (m *pinBackendMini) GetLockoutStatus() *pin.LockoutStatus { return nil }
func (m *pinBackendMini) ResetLockout(_ string) error          { return nil }

// ---------------------------------------------------------------------------
// Barrier handler tests
// ---------------------------------------------------------------------------

func TestBarrier_Initialize_Success(t *testing.T) {
	svc := newBarrierSvcMini()
	client, cleanup := startTestServerFull(t, WithBarrierService(svc))
	defer cleanup()

	_, err := client.BarrierInitialize(context.Background(), &pb.BarrierInitializeRequest{Secret: []byte("mysecret")})
	if err != nil {
		t.Fatalf("BarrierInitialize failed: %v", err)
	}
}

func TestBarrier_Initialize_NilService(t *testing.T) {
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

func TestBarrier_Initialize_Error(t *testing.T) {
	svc := newBarrierSvcMini()
	svc.initErr = &service.ProposeError{Err: errors.New("init failed")}
	client, cleanup := startTestServerFull(t, WithBarrierService(svc))
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

func TestBarrier_Initialize_WithPINBackend(t *testing.T) {
	svc := newBarrierSvcMini()
	backend := &pinBackendMini{}
	client, cleanup := startTestServerFull(t,
		WithBarrierService(svc),
		WithPINBackend(backend))
	defer cleanup()

	_, err := client.BarrierInitialize(context.Background(), &pb.BarrierInitializeRequest{
		Secret: []byte("s"), SoPin: "sopin", UserPin: "userpin",
	})
	if err != nil {
		t.Fatalf("BarrierInitialize with PINs failed: %v", err)
	}
}

func TestBarrier_Initialize_PINError(t *testing.T) {
	svc := newBarrierSvcMini()
	backend := &pinBackendMini{setSOErr: pin.ErrPINTooShort}
	client, cleanup := startTestServerFull(t,
		WithBarrierService(svc),
		WithPINBackend(backend))
	defer cleanup()

	_, err := client.BarrierInitialize(context.Background(), &pb.BarrierInitializeRequest{
		Secret: []byte("s"), SoPin: "x",
	})
	if err == nil {
		t.Fatal("expected error from PIN validation, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for too-short PIN, got %s", st.Code())
	}
}

func TestBarrier_Unseal_Success(t *testing.T) {
	svc := newBarrierSvcMini()
	client, cleanup := startTestServerFull(t, WithBarrierService(svc))
	defer cleanup()

	_, err := client.BarrierUnseal(context.Background(), &pb.BarrierUnsealRequest{Secret: []byte("s")})
	if err != nil {
		t.Fatalf("BarrierUnseal failed: %v", err)
	}
}

func TestBarrier_Unseal_Error(t *testing.T) {
	svc := newBarrierSvcMini()
	svc.unsealErr = &service.ProposeError{Err: errors.New("fail")}
	client, cleanup := startTestServerFull(t, WithBarrierService(svc))
	defer cleanup()

	_, err := client.BarrierUnseal(context.Background(), &pb.BarrierUnsealRequest{Secret: []byte("s")})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// startSealTestServer starts a server whose BarrierSeal is gated as a
// composer gates it: a PIN backend holding the SO PIN, which WithPINBackend
// decorates with the SO PIN lockout.
func startSealTestServer(t *testing.T, svc service.BarrierServicer) (pb.QRDBServiceClient, func()) {
	t.Helper()
	return startTestServerFull(t, WithBarrierService(svc), WithPINBackend(pintest.NewSOPINBackend(t)))
}

func TestBarrier_Seal_Success(t *testing.T) {
	svc := newBarrierSvcMini()
	client, cleanup := startSealTestServer(t, svc)
	defer cleanup()

	_, err := client.BarrierSeal(context.Background(), &pb.BarrierSealRequest{SoPin: pintest.ThrottledSOPIN})
	if err != nil {
		t.Fatalf("BarrierSeal failed: %v", err)
	}
}

func TestBarrier_Seal_Error(t *testing.T) {
	svc := newBarrierSvcMini()
	svc.sealErr = errors.New("seal failed")
	client, cleanup := startSealTestServer(t, svc)
	defer cleanup()

	// The SO PIN is right, so the error is the barrier service's, not the
	// PIN gate's.
	_, err := client.BarrierSeal(context.Background(), &pb.BarrierSealRequest{SoPin: pintest.ThrottledSOPIN})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if st, _ := status.FromError(err); st.Code() == codes.FailedPrecondition || st.Code() == codes.PermissionDenied || st.Code() == codes.Unauthenticated {
		t.Fatalf("BarrierSeal = %v, want the barrier service's error, not a PIN refusal", err)
	}
}

func TestBarrier_Seal_WrongSOPINRefused(t *testing.T) {
	svc := newBarrierSvcMini()
	client, cleanup := startSealTestServer(t, svc)
	defer cleanup()

	if _, err := client.BarrierSeal(context.Background(), &pb.BarrierSealRequest{SoPin: "wrong-pin-0000"}); err == nil {
		t.Fatal("BarrierSeal with a wrong SO PIN succeeded")
	}
}

func TestBarrier_Status_Success(t *testing.T) {
	svc := newBarrierSvcMini()
	client, cleanup := startTestServerFull(t, WithBarrierService(svc))
	defer cleanup()

	resp, err := client.BarrierStatus(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("BarrierStatus failed: %v", err)
	}
	if resp.GetStrategy() != "software" {
		t.Fatalf("expected strategy 'software', got %q", resp.GetStrategy())
	}
}

func TestBarrier_Status_Error(t *testing.T) {
	svc := newBarrierSvcMini()
	svc.statusErr = errors.New("status failed")
	client, cleanup := startTestServerFull(t, WithBarrierService(svc))
	defer cleanup()

	_, err := client.BarrierStatus(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestBarrier_ShamirInitialize_Success(t *testing.T) {
	svc := newBarrierSvcMini()
	client, cleanup := startTestServerFull(t, WithBarrierService(svc))
	defer cleanup()

	resp, err := client.BarrierShamirInitialize(context.Background(), &pb.BarrierShamirInitializeRequest{})
	if err != nil {
		t.Fatalf("BarrierShamirInitialize failed: %v", err)
	}
	if resp.GetThreshold() != 2 {
		t.Fatalf("expected threshold 2, got %d", resp.GetThreshold())
	}
}

func TestBarrier_ShamirInitialize_WithPINs(t *testing.T) {
	svc := newBarrierSvcMini()
	backend := &pinBackendMini{}
	client, cleanup := startTestServerFull(t,
		WithBarrierService(svc),
		WithPINBackend(backend))
	defer cleanup()

	_, err := client.BarrierShamirInitialize(context.Background(), &pb.BarrierShamirInitializeRequest{})
	if err != nil {
		t.Fatalf("BarrierShamirInitialize with PINs failed: %v", err)
	}
}

func TestBarrier_ShamirInitialize_Error(t *testing.T) {
	svc := newBarrierSvcMini()
	svc.shamirInitErr = errors.New("shamir init failed")
	client, cleanup := startTestServerFull(t, WithBarrierService(svc))
	defer cleanup()

	_, err := client.BarrierShamirInitialize(context.Background(), &pb.BarrierShamirInitializeRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestBarrier_ShamirInitialize_NilService(t *testing.T) {
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

func TestBarrier_RecoveryGenerate_Success(t *testing.T) {
	svc := newBarrierSvcMini()
	client, cleanup := startTestServerFull(t, WithBarrierService(svc), WithPINBackend(&pinBackendMini{}))
	defer cleanup()

	resp, err := client.BarrierRecoveryGenerate(context.Background(), &pb.BarrierRecoveryGenerateRequest{})
	if err != nil {
		t.Fatalf("BarrierRecoveryGenerate failed: %v", err)
	}
	if resp.GetShareCount() != 1 {
		t.Fatalf("expected share_count=1, got %d", resp.GetShareCount())
	}
}

func TestBarrier_RecoveryGenerate_WithPIN(t *testing.T) {
	svc := newBarrierSvcMini()
	backend := &pinBackendMini{soPINSet: true}
	client, cleanup := startTestServerFull(t,
		WithBarrierService(svc),
		WithPINBackend(backend))
	defer cleanup()

	_, err := client.BarrierRecoveryGenerate(context.Background(), &pb.BarrierRecoveryGenerateRequest{})
	if err != nil {
		t.Fatalf("BarrierRecoveryGenerate with PIN failed: %v", err)
	}
}

func TestBarrier_RecoveryGenerate_Error(t *testing.T) {
	svc := newBarrierSvcMini()
	svc.recoveryGenErr = errors.New("gen failed")
	client, cleanup := startTestServerFull(t, WithBarrierService(svc))
	defer cleanup()

	_, err := client.BarrierRecoveryGenerate(context.Background(), &pb.BarrierRecoveryGenerateRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestBarrier_RecoveryGenerate_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.BarrierRecoveryGenerate(context.Background(), &pb.BarrierRecoveryGenerateRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestBarrier_RecoveryDelete_Success(t *testing.T) {
	svc := newBarrierSvcMini()
	client, cleanup := startTestServerFull(t, WithBarrierService(svc))
	defer cleanup()

	_, err := client.BarrierRecoveryDelete(context.Background(), &pb.BarrierRecoveryDeleteRequest{})
	if err != nil {
		t.Fatalf("BarrierRecoveryDelete failed: %v", err)
	}
}

func TestBarrier_RecoveryDelete_Error(t *testing.T) {
	svc := newBarrierSvcMini()
	svc.recoveryDeleteErr = errors.New("delete failed")
	client, cleanup := startTestServerFull(t, WithBarrierService(svc))
	defer cleanup()

	_, err := client.BarrierRecoveryDelete(context.Background(), &pb.BarrierRecoveryDeleteRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestBarrier_RecoveryDelete_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.BarrierRecoveryDelete(context.Background(), &pb.BarrierRecoveryDeleteRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestBarrier_RecoveryStatus_Success(t *testing.T) {
	svc := newBarrierSvcMini()
	svc.recoveryHasResult = true
	client, cleanup := startTestServerFull(t, WithBarrierService(svc))
	defer cleanup()

	resp, err := client.BarrierRecoveryStatus(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("BarrierRecoveryStatus failed: %v", err)
	}
	if !resp.GetHasRecoveryKeys() {
		t.Fatal("expected has_recovery_keys=true")
	}
}

func TestBarrier_RecoveryStatus_Error(t *testing.T) {
	svc := newBarrierSvcMini()
	svc.recoveryHasErr = errors.New("status failed")
	client, cleanup := startTestServerFull(t, WithBarrierService(svc))
	defer cleanup()

	_, err := client.BarrierRecoveryStatus(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestBarrier_RecoveryStatus_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.BarrierRecoveryStatus(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// service_barrier_pin.go tests
// ---------------------------------------------------------------------------

func TestMapPINError_Nil(t *testing.T) {
	if mapServiceError(nil) != nil {
		t.Fatal("expected nil for nil input")
	}
}

func TestMapPINError_AllSentinels(t *testing.T) {
	tests := []struct {
		err  error
		want codes.Code
	}{
		{pin.ErrPINNotSet, codes.FailedPrecondition},
		{pin.ErrPINLocked, codes.ResourceExhausted},
		{pin.ErrPINInvalid, codes.Unauthenticated},
		{pin.ErrSOPINRequired, codes.Unauthenticated},
		{pin.ErrPINTooShort, codes.InvalidArgument},
		{pin.ErrPINAlreadySet, codes.AlreadyExists},
		{pin.ErrInvalidCurrentPIN, codes.Unauthenticated},
		{pin.ErrStateCorrupted, codes.DataLoss},
		{pin.ErrStrategyNotSet, codes.FailedPrecondition},
		{errors.New("unknown PIN error"), codes.Internal},
	}
	for _, tc := range tests {
		got := mapServiceError(tc.err)
		st, ok := status.FromError(got)
		if !ok {
			t.Fatalf("mapServiceError(%v) not a gRPC status", tc.err)
		}
		if st.Code() != tc.want {
			t.Errorf("mapServiceError(%v) = %s, want %s", tc.err, st.Code(), tc.want)
		}
	}
}

// TestApplyBarrierInitializePINs_NilBackend pins the fail-closed rule: PINs
// with no backend to hold them are refused, never silently dropped.
func TestApplyBarrierInitializePINs_NilBackend(t *testing.T) {
	if err := pin.Provision(context.Background(), nil, "so", "user"); !errors.Is(err, pin.ErrBackendNotConfigured) {
		t.Fatalf("expected ErrBackendNotConfigured for nil backend, got %v", err)
	}
}

func TestApplyBarrierInitializePINs_EmptyPINs(t *testing.T) {
	backend := &pinBackendMini{}
	if err := pin.Provision(context.Background(), backend, "", ""); err != nil {
		t.Fatalf("expected nil error for empty PINs, got %v", err)
	}
}

func TestApplyBarrierInitializePINs_SOPINError(t *testing.T) {
	backend := &pinBackendMini{setSOErr: pin.ErrPINTooShort}
	if err := pin.Provision(context.Background(), backend, "short", ""); err == nil {
		t.Fatal("expected error from SetSOPIN, got nil")
	}
}

func TestApplyBarrierInitializePINs_UserPINError(t *testing.T) {
	backend := &pinBackendMini{setUserErr: pin.ErrPINTooShort}
	if err := pin.Provision(context.Background(), backend, "", "x"); err == nil {
		t.Fatal("expected error from SetUserPIN, got nil")
	}
}

// TestVerifyBarrierSOPIN_NilBackend pins the fail-closed rule: with no PIN
// backend a PIN-gated operation is refused.
func TestVerifyBarrierSOPIN_NilBackend(t *testing.T) {
	if err := pin.RequireSOPIN(context.Background(), nil, "pin"); !errors.Is(err, pin.ErrBackendNotConfigured) {
		t.Fatalf("expected ErrBackendNotConfigured for nil backend, got %v", err)
	}
}

func TestVerifyBarrierSOPIN_SOPINNotSet(t *testing.T) {
	backend := &pinBackendMini{soPINSet: false}
	if err := pin.RequireSOPIN(context.Background(), backend, "pin"); err != nil {
		t.Fatalf("expected nil when SO PIN not set, got %v", err)
	}
}

func TestVerifyBarrierSOPIN_VerifyError(t *testing.T) {
	backend := &pinBackendMini{soPINSet: true, verifyErr: pin.ErrPINInvalid}
	err := pin.RequireSOPIN(context.Background(), backend, "wrong")
	if err == nil {
		t.Fatal("expected error from VerifySOPIN, got nil")
	}
}

// ---------------------------------------------------------------------------
// Ceremony handler tests
// ---------------------------------------------------------------------------

func TestCeremony_Initialize_Success(t *testing.T) {
	svc := newCeremonySvcMini()
	client, cleanup := startTestServerFull(t, WithCeremonyService(svc), WithCeremonyCA(&stubCeremonyCA{}))
	defer cleanup()

	resp, err := client.CeremonyInitialize(context.Background(), &pb.CeremonyInitializeRequest{})
	if err != nil {
		t.Fatalf("CeremonyInitialize failed: %v", err)
	}
	if resp.GetSpkiPin() != "pin" {
		t.Fatalf("expected SPKI pin 'pin', got %q", resp.GetSpkiPin())
	}
	if resp.GetShareCount() != 3 {
		t.Fatalf("expected share_count 3, got %d", resp.GetShareCount())
	}
}

func TestCeremony_Initialize_NilService(t *testing.T) {
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

func TestCeremony_Initialize_Error(t *testing.T) {
	svc := newCeremonySvcMini()
	svc.initErr = errors.New("ceremony init error")
	client, cleanup := startTestServerFull(t, WithCeremonyService(svc), WithCeremonyCA(&stubCeremonyCA{}))
	defer cleanup()

	_, err := client.CeremonyInitialize(context.Background(), &pb.CeremonyInitializeRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCeremony_Status_Success(t *testing.T) {
	svc := newCeremonySvcMini()
	client, cleanup := startTestServerFull(t, WithCeremonyService(svc))
	defer cleanup()

	resp, err := client.CeremonyStatus(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("CeremonyStatus failed: %v", err)
	}
	if resp.GetState() != "initialized" {
		t.Fatalf("expected state 'initialized', got %q", resp.GetState())
	}
}

func TestCeremony_Status_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.CeremonyStatus(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCeremony_Status_Error(t *testing.T) {
	svc := newCeremonySvcMini()
	svc.statusErr = errors.New("status failed")
	client, cleanup := startTestServerFull(t, WithCeremonyService(svc))
	defer cleanup()

	_, err := client.CeremonyStatus(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCeremony_SignCSR_Success(t *testing.T) {
	svc := newCeremonySvcMini()
	client, cleanup := startTestServerFull(t, WithCeremonyService(svc))
	defer cleanup()

	resp, err := client.CeremonySignCSR(context.Background(), &pb.CeremonySignCSRRequest{
		Username: "alice", SoPin: "pin", CsrPem: "csr", Role: "officer",
	})
	if err != nil {
		t.Fatalf("CeremonySignCSR failed: %v", err)
	}
	if resp.GetCertPem() == "" {
		t.Fatal("expected non-empty cert PEM")
	}
}

func TestCeremony_SignCSR_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.CeremonySignCSR(context.Background(), &pb.CeremonySignCSRRequest{Username: "a"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCeremony_SignCSR_Error(t *testing.T) {
	svc := newCeremonySvcMini()
	svc.signCSRErr = errors.New("sign error")
	client, cleanup := startTestServerFull(t, WithCeremonyService(svc))
	defer cleanup()

	_, err := client.CeremonySignCSR(context.Background(), &pb.CeremonySignCSRRequest{
		Username: "alice", SoPin: "pin", CsrPem: "csr", Role: "officer",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// SignatureDecodeError tests.
func TestSignatureDecodeError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("base64 decode error")
	e := &SignatureDecodeError{Err: inner}
	if e.Error() == "" {
		t.Fatal("expected non-empty error message")
	}
	if e.Unwrap() != inner {
		t.Fatal("Unwrap did not return inner error")
	}
}

// ---------------------------------------------------------------------------
// Database handler tests
// ---------------------------------------------------------------------------

func TestDatabase_Create_Success(t *testing.T) {
	svc := newDatabaseSvcMini()
	client, cleanup := startTestServerFull(t, WithDatabaseService(svc))
	defer cleanup()

	resp, err := client.DatabaseCreate(context.Background(), &pb.DatabaseCreateRequest{
		Name: "testdb", Engine: "mvcc", Backend: "pebble",
	})
	if err != nil {
		t.Fatalf("DatabaseCreate failed: %v", err)
	}
	if resp.GetName() != "testdb" {
		t.Fatalf("expected name 'testdb', got %q", resp.GetName())
	}
}

func TestDatabase_Create_NilService(t *testing.T) {
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

func TestDatabase_Create_Error(t *testing.T) {
	svc := newDatabaseSvcMini()
	svc.createErr = errors.New("create failed")
	client, cleanup := startTestServerFull(t, WithDatabaseService(svc))
	defer cleanup()

	_, err := client.DatabaseCreate(context.Background(), &pb.DatabaseCreateRequest{Name: "db"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDatabase_Get_Success(t *testing.T) {
	svc := newDatabaseSvcMini()
	client, cleanup := startTestServerFull(t, WithDatabaseService(svc))
	defer cleanup()

	resp, err := client.DatabaseGet(context.Background(), &pb.DatabaseGetRequest{Name: "testdb"})
	if err != nil {
		t.Fatalf("DatabaseGet failed: %v", err)
	}
	if resp.GetName() != "testdb" {
		t.Fatalf("expected name 'testdb', got %q", resp.GetName())
	}
}

func TestDatabase_Get_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.DatabaseGet(context.Background(), &pb.DatabaseGetRequest{Name: "db"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDatabase_Get_Error(t *testing.T) {
	svc := newDatabaseSvcMini()
	svc.getErr = &service.KeyNotFoundError{Key: "db"}
	client, cleanup := startTestServerFull(t, WithDatabaseService(svc))
	defer cleanup()

	_, err := client.DatabaseGet(context.Background(), &pb.DatabaseGetRequest{Name: "missing"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestDatabase_List_Success(t *testing.T) {
	svc := newDatabaseSvcMini()
	client, cleanup := startTestServerFull(t, WithDatabaseService(svc))
	defer cleanup()

	resp, err := client.DatabaseList(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("DatabaseList failed: %v", err)
	}
	if len(resp.GetDatabases()) != 1 {
		t.Fatalf("expected 1 database, got %d", len(resp.GetDatabases()))
	}
}

func TestDatabase_List_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.DatabaseList(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDatabase_List_Error(t *testing.T) {
	svc := newDatabaseSvcMini()
	svc.listErr = errors.New("list failed")
	client, cleanup := startTestServerFull(t, WithDatabaseService(svc))
	defer cleanup()

	_, err := client.DatabaseList(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDatabase_Drop_Success(t *testing.T) {
	svc := newDatabaseSvcMini()
	client, cleanup := startTestServerFull(t, WithDatabaseService(svc))
	defer cleanup()

	_, err := client.DatabaseDrop(context.Background(), &pb.DatabaseDropRequest{Name: "testdb"})
	if err != nil {
		t.Fatalf("DatabaseDrop failed: %v", err)
	}
}

func TestDatabase_Drop_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.DatabaseDrop(context.Background(), &pb.DatabaseDropRequest{Name: "db"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDatabase_Drop_Error(t *testing.T) {
	svc := newDatabaseSvcMini()
	svc.dropErr = errors.New("drop failed")
	client, cleanup := startTestServerFull(t, WithDatabaseService(svc))
	defer cleanup()

	_, err := client.DatabaseDrop(context.Background(), &pb.DatabaseDropRequest{Name: "testdb"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDatabase_UpdateProtocol_Success(t *testing.T) {
	svc := newDatabaseSvcMini()
	client, cleanup := startTestServerFull(t, WithDatabaseService(svc))
	defer cleanup()

	_, err := client.DatabaseUpdateProtocol(context.Background(), &pb.DatabaseUpdateProtocolRequest{Name: "db"})
	if err != nil {
		t.Fatalf("DatabaseUpdateProtocol failed: %v", err)
	}
}

func TestDatabase_UpdateProtocol_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.DatabaseUpdateProtocol(context.Background(), &pb.DatabaseUpdateProtocolRequest{Name: "db"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDatabase_UpdateProtocol_Error(t *testing.T) {
	svc := newDatabaseSvcMini()
	svc.updateErr = errors.New("update failed")
	client, cleanup := startTestServerFull(t, WithDatabaseService(svc))
	defer cleanup()

	_, err := client.DatabaseUpdateProtocol(context.Background(), &pb.DatabaseUpdateProtocolRequest{Name: "db"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDatabase_UpdateEtcd_Success(t *testing.T) {
	svc := newDatabaseSvcMini()
	client, cleanup := startTestServerFull(t, WithDatabaseService(svc))
	defer cleanup()

	_, err := client.DatabaseUpdateEtcd(context.Background(), &pb.DatabaseUpdateEtcdRequest{Name: "db"})
	if err != nil {
		t.Fatalf("DatabaseUpdateEtcd failed: %v", err)
	}
}

func TestDatabase_UpdateEtcd_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.DatabaseUpdateEtcd(context.Background(), &pb.DatabaseUpdateEtcdRequest{Name: "db"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDatabase_UpdateRaft_Success(t *testing.T) {
	svc := newDatabaseSvcMini()
	client, cleanup := startTestServerFull(t, WithDatabaseService(svc))
	defer cleanup()

	_, err := client.DatabaseUpdateRaft(context.Background(), &pb.DatabaseUpdateRaftRequest{Name: "db"})
	if err != nil {
		t.Fatalf("DatabaseUpdateRaft failed: %v", err)
	}
}

func TestDatabase_UpdateRaft_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.DatabaseUpdateRaft(context.Background(), &pb.DatabaseUpdateRaftRequest{Name: "db"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDatabase_UpdateBarrier_Success(t *testing.T) {
	svc := newDatabaseSvcMini()
	client, cleanup := startTestServerFull(t, WithDatabaseService(svc))
	defer cleanup()

	_, err := client.DatabaseUpdateBarrier(context.Background(), &pb.DatabaseUpdateBarrierRequest{Name: "db"})
	if err != nil {
		t.Fatalf("DatabaseUpdateBarrier failed: %v", err)
	}
}

func TestDatabase_UpdateBarrier_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.DatabaseUpdateBarrier(context.Background(), &pb.DatabaseUpdateBarrierRequest{Name: "db"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDatabase_UpdateConnection_Success(t *testing.T) {
	svc := newDatabaseSvcMini()
	client, cleanup := startTestServerFull(t, WithDatabaseService(svc))
	defer cleanup()

	_, err := client.DatabaseUpdateConnection(context.Background(), &pb.DatabaseUpdateConnectionRequest{Name: "db"})
	if err != nil {
		t.Fatalf("DatabaseUpdateConnection failed: %v", err)
	}
}

func TestDatabase_UpdateConnection_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.DatabaseUpdateConnection(context.Background(), &pb.DatabaseUpdateConnectionRequest{Name: "db"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// Collection handler tests
// ---------------------------------------------------------------------------

func TestCollection_Create_Success(t *testing.T) {
	svc := newCollectionSvcMini()
	client, cleanup := startTestServerFull(t, WithCollectionService(svc))
	defer cleanup()

	resp, err := client.CreateCollection(context.Background(), &pb.CreateCollectionRequest{
		Database: "db", Name: "docs",
	})
	if err != nil {
		t.Fatalf("CreateCollection failed: %v", err)
	}
	if resp.GetName() != "docs" {
		t.Fatalf("expected name 'docs', got %q", resp.GetName())
	}
}

func TestCollection_Create_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.CreateCollection(context.Background(), &pb.CreateCollectionRequest{Database: "db", Name: "docs"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCollection_Create_Error(t *testing.T) {
	svc := newCollectionSvcMini()
	svc.createErr = errors.New("create error")
	client, cleanup := startTestServerFull(t, WithCollectionService(svc))
	defer cleanup()

	_, err := client.CreateCollection(context.Background(), &pb.CreateCollectionRequest{Database: "db", Name: "docs"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCollection_GetInfo_Success(t *testing.T) {
	svc := newCollectionSvcMini()
	client, cleanup := startTestServerFull(t, WithCollectionService(svc))
	defer cleanup()

	resp, err := client.GetCollectionInfo(context.Background(), &pb.GetCollectionInfoRequest{
		Database: "db", Name: "docs",
	})
	if err != nil {
		t.Fatalf("GetCollectionInfo failed: %v", err)
	}
	if resp.GetName() != "docs" {
		t.Fatalf("expected name 'docs', got %q", resp.GetName())
	}
}

func TestCollection_GetInfo_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.GetCollectionInfo(context.Background(), &pb.GetCollectionInfoRequest{Database: "db", Name: "docs"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCollection_GetInfo_Error(t *testing.T) {
	svc := newCollectionSvcMini()
	svc.getInfoErr = errors.New("get info error")
	client, cleanup := startTestServerFull(t, WithCollectionService(svc))
	defer cleanup()

	_, err := client.GetCollectionInfo(context.Background(), &pb.GetCollectionInfoRequest{Database: "db", Name: "docs"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCollection_List_Success(t *testing.T) {
	svc := newCollectionSvcMini()
	client, cleanup := startTestServerFull(t, WithCollectionService(svc))
	defer cleanup()

	resp, err := client.ListCollections(context.Background(), &pb.ListCollectionsRequest{Database: "db"})
	if err != nil {
		t.Fatalf("ListCollections failed: %v", err)
	}
	if len(resp.GetCollections()) != 1 {
		t.Fatalf("expected 1 collection, got %d", len(resp.GetCollections()))
	}
}

func TestCollection_List_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.ListCollections(context.Background(), &pb.ListCollectionsRequest{Database: "db"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCollection_List_Error(t *testing.T) {
	svc := newCollectionSvcMini()
	svc.listErr = errors.New("list error")
	client, cleanup := startTestServerFull(t, WithCollectionService(svc))
	defer cleanup()

	_, err := client.ListCollections(context.Background(), &pb.ListCollectionsRequest{Database: "db"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCollection_Drop_Success(t *testing.T) {
	svc := newCollectionSvcMini()
	client, cleanup := startTestServerFull(t, WithCollectionService(svc))
	defer cleanup()

	_, err := client.DropCollection(context.Background(), &pb.DropCollectionRequest{Database: "db", Name: "docs"})
	if err != nil {
		t.Fatalf("DropCollection failed: %v", err)
	}
}

func TestCollection_Drop_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.DropCollection(context.Background(), &pb.DropCollectionRequest{Database: "db", Name: "docs"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCollection_Drop_Error(t *testing.T) {
	svc := newCollectionSvcMini()
	svc.dropErr = errors.New("drop error")
	client, cleanup := startTestServerFull(t, WithCollectionService(svc))
	defer cleanup()

	_, err := client.DropCollection(context.Background(), &pb.DropCollectionRequest{Database: "db", Name: "docs"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCollection_InsertDocument_Success(t *testing.T) {
	svc := newCollectionSvcMini()
	client, cleanup := startTestServerFull(t, WithCollectionService(svc))
	defer cleanup()

	resp, err := client.InsertDocument(context.Background(), &pb.InsertDocumentRequest{
		Database: "db", Collection: "docs", Document: `{"key":"value"}`,
	})
	if err != nil {
		t.Fatalf("InsertDocument failed: %v", err)
	}
	if resp.GetId() != 1 {
		t.Fatalf("expected ID 1, got %d", resp.GetId())
	}
}

func TestCollection_InsertDocument_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.InsertDocument(context.Background(), &pb.InsertDocumentRequest{Database: "db"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCollection_InsertDocument_Error(t *testing.T) {
	svc := newCollectionSvcMini()
	svc.insertErr = errors.New("insert error")
	client, cleanup := startTestServerFull(t, WithCollectionService(svc))
	defer cleanup()

	_, err := client.InsertDocument(context.Background(), &pb.InsertDocumentRequest{
		Database: "db", Collection: "docs", Document: `{"k":"v"}`,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCollection_GetDocument_Success(t *testing.T) {
	svc := newCollectionSvcMini()
	client, cleanup := startTestServerFull(t, WithCollectionService(svc))
	defer cleanup()

	resp, err := client.GetDocument(context.Background(), &pb.GetDocumentRequest{
		Database: "db", Collection: "docs", Id: 1,
	})
	if err != nil {
		t.Fatalf("GetDocument failed: %v", err)
	}
	if resp.GetId() != 1 {
		t.Fatalf("expected ID 1, got %d", resp.GetId())
	}
}

func TestCollection_GetDocument_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.GetDocument(context.Background(), &pb.GetDocumentRequest{Database: "db", Id: 1})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCollection_GetDocument_Error(t *testing.T) {
	svc := newCollectionSvcMini()
	svc.getDocErr = errors.New("get doc error")
	client, cleanup := startTestServerFull(t, WithCollectionService(svc))
	defer cleanup()

	_, err := client.GetDocument(context.Background(), &pb.GetDocumentRequest{Database: "db", Id: 1})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCollection_UpdateDocument_Success(t *testing.T) {
	svc := newCollectionSvcMini()
	client, cleanup := startTestServerFull(t, WithCollectionService(svc))
	defer cleanup()

	_, err := client.UpdateDocument(context.Background(), &pb.UpdateDocumentRequest{
		Database: "db", Collection: "docs", Id: 1, Document: `{"k":"new"}`,
	})
	if err != nil {
		t.Fatalf("UpdateDocument failed: %v", err)
	}
}

func TestCollection_UpdateDocument_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.UpdateDocument(context.Background(), &pb.UpdateDocumentRequest{Database: "db", Id: 1})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCollection_UpdateDocument_Error(t *testing.T) {
	svc := newCollectionSvcMini()
	svc.updateDocErr = errors.New("update error")
	client, cleanup := startTestServerFull(t, WithCollectionService(svc))
	defer cleanup()

	_, err := client.UpdateDocument(context.Background(), &pb.UpdateDocumentRequest{Database: "db", Id: 1})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCollection_DeleteDocument_Success(t *testing.T) {
	svc := newCollectionSvcMini()
	client, cleanup := startTestServerFull(t, WithCollectionService(svc))
	defer cleanup()

	_, err := client.DeleteDocument(context.Background(), &pb.DeleteDocumentRequest{
		Database: "db", Collection: "docs", Id: 1,
	})
	if err != nil {
		t.Fatalf("DeleteDocument failed: %v", err)
	}
}

func TestCollection_DeleteDocument_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.DeleteDocument(context.Background(), &pb.DeleteDocumentRequest{Database: "db", Id: 1})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCollection_DeleteDocument_Error(t *testing.T) {
	svc := newCollectionSvcMini()
	svc.deleteDocErr = errors.New("delete error")
	client, cleanup := startTestServerFull(t, WithCollectionService(svc))
	defer cleanup()

	_, err := client.DeleteDocument(context.Background(), &pb.DeleteDocumentRequest{Database: "db", Id: 1})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCollection_ListDocuments_Success(t *testing.T) {
	svc := newCollectionSvcMini()
	client, cleanup := startTestServerFull(t, WithCollectionService(svc))
	defer cleanup()

	resp, err := client.ListDocuments(context.Background(), &pb.ListDocumentsRequest{
		Database: "db", Collection: "docs",
	})
	if err != nil {
		t.Fatalf("ListDocuments failed: %v", err)
	}
	if len(resp.GetDocuments()) != 1 {
		t.Fatalf("expected 1 document, got %d", len(resp.GetDocuments()))
	}
}

func TestCollection_ListDocuments_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.ListDocuments(context.Background(), &pb.ListDocumentsRequest{Database: "db"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCollection_ListDocuments_Error(t *testing.T) {
	svc := newCollectionSvcMini()
	svc.listDocErr = errors.New("list doc error")
	client, cleanup := startTestServerFull(t, WithCollectionService(svc))
	defer cleanup()

	_, err := client.ListDocuments(context.Background(), &pb.ListDocumentsRequest{Database: "db"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
