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
	"context"
	"crypto/x509"
	"encoding/base64"
	"net"
	"testing"
	"time"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// fakeCeremonyServicer
// ===========================================================================

type fakeCeremonyServicer struct{}

var _ service.CeremonyServicer = (*fakeCeremonyServicer)(nil)

func (f *fakeCeremonyServicer) CeremonyInitialize(_ context.Context, _ ceremony.CeremonyCA, _ *service.CeremonyInitializeOptions) (*service.CeremonyInitResult, error) {
	return &service.CeremonyInitResult{SPKIPin: "pin", State: "initialized", CACertPEM: []byte("CA")}, nil
}

func (f *fakeCeremonyServicer) CeremonyBeginClaimCert(_ context.Context, _ string) (*service.CeremonyChallenge, error) {
	return &service.CeremonyChallenge{NonceHex: "dead", ExpiresAt: time.Unix(1700001000, 0)}, nil
}

func (f *fakeCeremonyServicer) CeremonyCompleteClaimCert(_ context.Context, _, _ string, _ []byte) (*service.CeremonyClaimCertResult, error) {
	return &service.CeremonyClaimCertResult{CertPEM: []byte("CERT"), CACertPEM: []byte("CA")}, nil
}

func (f *fakeCeremonyServicer) CeremonyBeginClaimShare(_ context.Context, _, _ string) (*service.CeremonyChallenge, error) {
	return &service.CeremonyChallenge{NonceHex: "abcd", ExpiresAt: time.Now().Add(time.Minute)}, nil
}

func (f *fakeCeremonyServicer) CeremonyCompleteClaimShare(_ context.Context, _, _ string, _ []byte, _ string) (*service.CeremonyShareClaimResult, error) {
	return &service.CeremonyShareClaimResult{ShareIndex: 1, ShareBytes: []byte(`{"x":1}`)}, nil
}

func (f *fakeCeremonyServicer) CeremonySignCSR(_ context.Context, _, _, _, _ string) (*service.CeremonySignCSRResult, error) {
	return &service.CeremonySignCSRResult{CertPEM: []byte("CERT")}, nil
}

func (f *fakeCeremonyServicer) CeremonyStatus(_ context.Context) (*service.CeremonyStatusInfo, error) {
	return &service.CeremonyStatusInfo{State: "initialized", SPKIPin: "pin"}, nil
}

func (f *fakeCeremonyServicer) CeremonyClaimShareBegin(_ context.Context, _ string) (*service.CeremonyCustodianChallenge, error) {
	return nil, nil
}

func (f *fakeCeremonyServicer) CeremonyClaimShareComplete(_ context.Context, _, _ string, _ []byte) (*custodian.Custodian, error) {
	return nil, nil
}

func (f *fakeCeremonyServicer) CeremonySubmitUnsealShare(_ context.Context, _, _ string, _, _ []byte) (*service.CeremonyUnsealShareResult, error) {
	return nil, nil
}

func (f *fakeCeremonyServicer) CeremonyEnrollExternalOfficer(_ context.Context, _ *service.CeremonyEnrollExternalOfficerRequest) (*service.CeremonyEnrollExternalOfficerResponse, error) {
	return &service.CeremonyEnrollExternalOfficerResponse{SealedShare: "sealed"}, nil
}

// ===========================================================================
// fakeCollectionServicer
// ===========================================================================

type fakeCollectionServicer struct{}

var _ service.CollectionServicer = (*fakeCollectionServicer)(nil)

func (f *fakeCollectionServicer) CreateCollection(_ context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
	return &service.CollectionInfo{Name: req.Name, Database: req.Database, CreatedAt: time.Unix(1700000000, 0).UTC()}, nil
}

func (f *fakeCollectionServicer) DropCollection(_ context.Context, _ *service.DropCollectionRequest) error {
	return nil
}

func (f *fakeCollectionServicer) ListCollections(_ context.Context, req *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
	return &service.ListCollectionsResponse{
		Collections: []service.CollectionInfo{
			{Name: "users", Database: req.Database, CreatedAt: time.Unix(1700000000, 0).UTC()},
		},
	}, nil
}

func (f *fakeCollectionServicer) GetCollectionInfo(_ context.Context, database, name string) (*service.CollectionInfo, error) {
	return &service.CollectionInfo{Name: name, Database: database, CreatedAt: time.Unix(1700000000, 0).UTC()}, nil
}

func (f *fakeCollectionServicer) InsertDocument(_ context.Context, _ *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
	return &service.InsertDocumentResponse{ID: 42}, nil
}

func (f *fakeCollectionServicer) GetDocument(_ context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
	return &service.GetDocumentResponse{ID: req.ID, Document: map[string]interface{}{"k": "v"}}, nil
}

func (f *fakeCollectionServicer) UpdateDocument(_ context.Context, req *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error) {
	return &service.UpdateDocumentResponse{ID: req.ID}, nil
}

func (f *fakeCollectionServicer) DeleteDocument(_ context.Context, _ *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error) {
	return &service.DeleteDocumentResponse{Deleted: true}, nil
}

func (f *fakeCollectionServicer) ListDocuments(_ context.Context, _ *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
	return &service.ListDocumentsResponse{
		Documents:  []service.GetDocumentResponse{{ID: 1, Document: map[string]interface{}{"k": "v"}}},
		TotalCount: 1,
	}, nil
}

// ===========================================================================
// fakeUserAuthFactorServicer
// ===========================================================================

type fakeUserAuthFactorServicer struct{}

var _ service.UserAuthFactorServicer = (*fakeUserAuthFactorServicer)(nil)

func (f *fakeUserAuthFactorServicer) Add(_ context.Context, userID uint64, factor *auth.AuthFactor) error {
	factor.ID = "f1"
	factor.UserID = userID
	factor.RegisteredAt = time.Unix(1700000000, 0)
	return nil
}

func (f *fakeUserAuthFactorServicer) List(_ context.Context, userID uint64) ([]*auth.AuthFactor, error) {
	return []*auth.AuthFactor{{ID: "f1", UserID: userID, Type: "mtls", RegisteredAt: time.Unix(1700000000, 0)}}, nil
}

func (f *fakeUserAuthFactorServicer) Get(_ context.Context, factorID string) (*auth.AuthFactor, error) {
	return &auth.AuthFactor{ID: factorID, Type: "mtls", RegisteredAt: time.Unix(1700000000, 0)}, nil
}

func (f *fakeUserAuthFactorServicer) LookupByCredentialKey(_ context.Context, t auth.AuthFactorType, _ []byte) (*auth.AuthFactor, error) {
	return &auth.AuthFactor{ID: "f1", UserID: 1, Type: t, RegisteredAt: time.Unix(1700000000, 0)}, nil
}

func (f *fakeUserAuthFactorServicer) Remove(_ context.Context, _ string, _ bool) error {
	return nil
}

func (f *fakeUserAuthFactorServicer) Rotate(_ context.Context, _ string, _ *auth.AuthFactor) error {
	return nil
}

func (f *fakeUserAuthFactorServicer) Relabel(_ context.Context, _ string, _ string) error {
	return nil
}

func (f *fakeUserAuthFactorServicer) MarkUsed(_ context.Context, _ string, _ time.Time) error {
	return nil
}

// ===========================================================================
// Server setup
// ===========================================================================

// stubCeremonyCA is a non-nil CeremonyCA placeholder. The ceremony handler
// requires a configured CA (fail-closed since #54's nil-guard) before it
// delegates to the servicer; the fake servicer ignores the CA argument, so
// these stub methods are never actually invoked.
type stubCeremonyCA struct{}

func (stubCeremonyCA) SignCSR(context.Context, []byte, *ceremony.SignCSROptions) (*x509.Certificate, error) {
	return nil, nil
}
func (stubCeremonyCA) CACertificate() (*x509.Certificate, error) { return nil, nil }
func (stubCeremonyCA) CABundle() ([]byte, error)                 { return nil, nil }
func (stubCeremonyCA) IssueCertificate(context.Context, *ceremony.IssueCertRequest) (*ceremony.IssuedCert, error) {
	return nil, nil
}

func startMiscServer(t *testing.T) (string, func()) {
	t.Helper()
	srv := googlegrpc.NewServer()
	grpc.RegisterService(srv, nil, nil,
		grpc.WithRegisterCeremonyService(&fakeCeremonyServicer{}),
		grpc.WithRegisterCeremonyCA(stubCeremonyCA{}),
		grpc.WithRegisterCollectionService(&fakeCollectionServicer{}),
	)
	grpc.RegisterUserAuthFactorService(srv, &fakeUserAuthFactorServicer{}, nil)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	return lis.Addr().String(), func() { srv.Stop() }
}

func connectMiscTransport(t *testing.T, addr string) (*Transport, func()) {
	t.Helper()
	conn, err := googlegrpc.NewClient(addr, googlegrpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	tr := &Transport{
		config: &transport.Config{Address: addr},
		conn:   conn,
		client: pb.NewQRDBServiceClient(conn),
	}
	return tr, func() { _ = conn.Close() }
}

// ===========================================================================
// Ceremony success
// ===========================================================================

func TestGRPCCeremony_AllMethods_Success(t *testing.T) {
	addr, stop := startMiscServer(t)
	defer stop()
	tr, cleanup := connectMiscTransport(t, addr)
	defer cleanup()
	ctx := context.Background()

	t.Run("Initialize", func(t *testing.T) {
		if _, err := tr.CeremonyInitialize(ctx, &transport.CeremonyInitializeRequest{}); err != nil {
			t.Fatalf("CeremonyInitialize: %v", err)
		}
	})
	t.Run("Status", func(t *testing.T) {
		if _, err := tr.CeremonyStatus(ctx); err != nil {
			t.Fatalf("CeremonyStatus: %v", err)
		}
	})
	t.Run("ClaimCertBegin", func(t *testing.T) {
		if _, err := tr.CeremonyClaimCertBegin(ctx, &transport.ClaimCertBeginRequest{Username: "alice"}); err != nil {
			t.Fatalf("CeremonyClaimCertBegin: %v", err)
		}
	})
	t.Run("ClaimCertComplete", func(t *testing.T) {
		if _, err := tr.CeremonyClaimCertComplete(ctx, &transport.ClaimCertCompleteRequest{
			Username:  "alice",
			NonceHex:  "dead",
			Signature: base64.StdEncoding.EncodeToString([]byte("sig")),
		}); err != nil {
			t.Fatalf("CeremonyClaimCertComplete: %v", err)
		}
	})
	t.Run("ClaimShare", func(t *testing.T) {
		if _, err := tr.CeremonyBeginClaimShare(ctx, &transport.BeginClaimShareRequest{Username: "alice"}); err == nil {
			t.Fatal("CeremonyBeginClaimShare: expected refusal without a client certificate")
		}
	})
	t.Run("SignCSR", func(t *testing.T) {
		if _, err := tr.CeremonySignCSR(ctx, &transport.SignCSRInitRequest{Username: "alice"}); err != nil {
			t.Fatalf("CeremonySignCSR: %v", err)
		}
	})
	t.Run("EnrollExternalOfficer", func(t *testing.T) {
		if _, err := tr.CeremonyEnrollExternalOfficer(ctx, &transport.EnrollExternalOfficerRequest{Username: "alice"}); err != nil {
			t.Fatalf("CeremonyEnrollExternalOfficer: %v", err)
		}
	})
}

// ===========================================================================
// Collection success
// ===========================================================================

func TestGRPCCollection_AllMethods_Success(t *testing.T) {
	addr, stop := startMiscServer(t)
	defer stop()
	tr, cleanup := connectMiscTransport(t, addr)
	defer cleanup()
	ctx := context.Background()

	t.Run("CreateCollection", func(t *testing.T) {
		if _, err := tr.CreateCollection(ctx, transport.CreateCollectionRequest{Database: "db", Name: "users"}); err != nil {
			t.Fatalf("CreateCollection: %v", err)
		}
	})
	t.Run("GetCollectionInfo", func(t *testing.T) {
		if _, err := tr.GetCollectionInfo(ctx, "db", "users"); err != nil {
			t.Fatalf("GetCollectionInfo: %v", err)
		}
	})
	t.Run("ListCollections", func(t *testing.T) {
		if _, err := tr.ListCollections(ctx, "db"); err != nil {
			t.Fatalf("ListCollections: %v", err)
		}
	})
	t.Run("DropCollection", func(t *testing.T) {
		if err := tr.DropCollection(ctx, "db", "users"); err != nil {
			t.Fatalf("DropCollection: %v", err)
		}
	})
	t.Run("InsertDocument", func(t *testing.T) {
		if _, err := tr.InsertDocument(ctx, transport.InsertDocumentRequest{Database: "db", Collection: "users", Document: map[string]interface{}{"x": 1}}); err != nil {
			t.Fatalf("InsertDocument: %v", err)
		}
	})
	t.Run("GetDocument", func(t *testing.T) {
		if _, err := tr.GetDocument(ctx, "db", "users", 1); err != nil {
			t.Fatalf("GetDocument: %v", err)
		}
	})
	t.Run("UpdateDocument", func(t *testing.T) {
		if err := tr.UpdateDocument(ctx, transport.UpdateDocumentRequest{Database: "db", Collection: "users", ID: 1}); err != nil {
			t.Fatalf("UpdateDocument: %v", err)
		}
	})
	t.Run("DeleteDocument", func(t *testing.T) {
		if err := tr.DeleteDocument(ctx, "db", "users", 1); err != nil {
			t.Fatalf("DeleteDocument: %v", err)
		}
	})
	t.Run("ListDocuments", func(t *testing.T) {
		if _, err := tr.ListDocuments(ctx, transport.ListDocumentsRequest{Database: "db", Collection: "users"}); err != nil {
			t.Fatalf("ListDocuments: %v", err)
		}
	})
}

// ===========================================================================
// UserAuthFactor success
// ===========================================================================

func TestGRPCUserAuthFactor_AllMethods_Success(t *testing.T) {
	addr, stop := startMiscServer(t)
	defer stop()
	tr, cleanup := connectMiscTransport(t, addr)
	defer cleanup()
	ctx := context.Background()

	t.Run("AddAuthFactor", func(t *testing.T) {
		if _, err := tr.AddAuthFactor(ctx, &transport.AuthFactorAddRequest{UserID: 1, Type: "mtls", Credential: []byte("c")}); err != nil {
			t.Fatalf("AddAuthFactor: %v", err)
		}
	})
	t.Run("ListAuthFactors", func(t *testing.T) {
		if _, err := tr.ListAuthFactors(ctx, &transport.AuthFactorListRequest{UserID: 1}); err != nil {
			t.Fatalf("ListAuthFactors: %v", err)
		}
	})
	t.Run("GetAuthFactor", func(t *testing.T) {
		if _, err := tr.GetAuthFactor(ctx, &transport.AuthFactorGetRequest{FactorID: "f1"}); err != nil {
			t.Fatalf("GetAuthFactor: %v", err)
		}
	})
	t.Run("RemoveAuthFactor", func(t *testing.T) {
		if _, err := tr.RemoveAuthFactor(ctx, &transport.AuthFactorRemoveRequest{FactorID: "f1"}); err != nil {
			t.Fatalf("RemoveAuthFactor: %v", err)
		}
	})
	t.Run("RotateAuthFactor", func(t *testing.T) {
		if _, err := tr.RotateAuthFactor(ctx, &transport.AuthFactorRotateRequest{
			OldFactorID: "f1",
			NewFactor:   transport.AuthFactorAddRequest{UserID: 1, Type: "mtls", Credential: []byte("c2")},
		}); err != nil {
			t.Fatalf("RotateAuthFactor: %v", err)
		}
	})
	t.Run("RelabelAuthFactor", func(t *testing.T) {
		if _, err := tr.RelabelAuthFactor(ctx, &transport.AuthFactorRelabelRequest{FactorID: "f1", Label: "L"}); err != nil {
			t.Fatalf("RelabelAuthFactor: %v", err)
		}
	})
	t.Run("LoginAuthFactor", func(t *testing.T) {
		if _, err := tr.LoginAuthFactor(ctx, &transport.AuthFactorLoginRequest{Type: "mtls", LookupKey: []byte("k")}); err != nil {
			t.Fatalf("LoginAuthFactor: %v", err)
		}
	})
}
