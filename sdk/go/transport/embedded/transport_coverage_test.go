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

package embedded

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// Mock: CollectionServicer
// ===========================================================================

type mockCollectionService struct {
	createFn    func(ctx context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error)
	dropFn      func(ctx context.Context, req *service.DropCollectionRequest) error
	listFn      func(ctx context.Context, req *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error)
	getInfoFn   func(ctx context.Context, database, name string) (*service.CollectionInfo, error)
	insertFn    func(ctx context.Context, req *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error)
	getDocFn    func(ctx context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error)
	updateDocFn func(ctx context.Context, req *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error)
	deleteDocFn func(ctx context.Context, req *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error)
	listDocsFn  func(ctx context.Context, req *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error)
}

func (m *mockCollectionService) CreateCollection(ctx context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
	if m.createFn != nil {
		return m.createFn(ctx, req)
	}
	return &service.CollectionInfo{Name: req.Name, Database: req.Database, CreatedAt: time.Unix(1700000000, 0).UTC()}, nil
}

func (m *mockCollectionService) DropCollection(ctx context.Context, req *service.DropCollectionRequest) error {
	if m.dropFn != nil {
		return m.dropFn(ctx, req)
	}
	return nil
}

func (m *mockCollectionService) ListCollections(ctx context.Context, req *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
	if m.listFn != nil {
		return m.listFn(ctx, req)
	}
	return &service.ListCollectionsResponse{
		Collections: []service.CollectionInfo{
			{Name: "users", Database: req.Database, CreatedAt: time.Unix(1700000000, 0).UTC()},
		},
	}, nil
}

func (m *mockCollectionService) GetCollectionInfo(ctx context.Context, database, name string) (*service.CollectionInfo, error) {
	if m.getInfoFn != nil {
		return m.getInfoFn(ctx, database, name)
	}
	return &service.CollectionInfo{Name: name, Database: database, CreatedAt: time.Unix(1700000000, 0).UTC()}, nil
}

func (m *mockCollectionService) InsertDocument(ctx context.Context, req *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
	if m.insertFn != nil {
		return m.insertFn(ctx, req)
	}
	return &service.InsertDocumentResponse{ID: 42}, nil
}

func (m *mockCollectionService) GetDocument(ctx context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
	if m.getDocFn != nil {
		return m.getDocFn(ctx, req)
	}
	return &service.GetDocumentResponse{ID: req.ID, Document: map[string]interface{}{"name": "alice"}}, nil
}

func (m *mockCollectionService) UpdateDocument(ctx context.Context, req *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error) {
	if m.updateDocFn != nil {
		return m.updateDocFn(ctx, req)
	}
	return &service.UpdateDocumentResponse{ID: req.ID}, nil
}

// UpsertDocument reports an insert: no test that uses this double drives
// the guarded upsert, which is exercised against the real service.
func (m *mockCollectionService) UpsertDocument(_ context.Context, req *service.UpsertDocumentRequest) (*service.UpsertDocumentResponse, error) {
	return &service.UpsertDocumentResponse{ID: req.ID}, nil
}

func (m *mockCollectionService) DeleteDocument(ctx context.Context, req *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error) {
	if m.deleteDocFn != nil {
		return m.deleteDocFn(ctx, req)
	}
	return &service.DeleteDocumentResponse{Deleted: true}, nil
}

func (m *mockCollectionService) ListDocuments(ctx context.Context, req *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
	if m.listDocsFn != nil {
		return m.listDocsFn(ctx, req)
	}
	return &service.ListDocumentsResponse{
		Documents:  []service.GetDocumentResponse{{ID: 1, Document: map[string]interface{}{"k": "v"}}},
		TotalCount: 1,
	}, nil
}

var _ service.CollectionServicer = (*mockCollectionService)(nil)

// ===========================================================================
// Collection tests
// ===========================================================================

func TestEmbeddedCollection_CreateCollection(t *testing.T) {
	tr := newExtTransport(t, WithCollectionService(&mockCollectionService{}))
	info, err := tr.CreateCollection(context.Background(), transport.CreateCollectionRequest{Database: "db", Name: "users"})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if info.Name != "users" || info.Database != "db" {
		t.Fatalf("unexpected info: %+v", info)
	}
}

func TestEmbeddedCollection_CreateCollection_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.CreateCollection(context.Background(), transport.CreateCollectionRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedCollection_CreateCollection_ServiceError(t *testing.T) {
	tr := newExtTransport(t, WithCollectionService(&mockCollectionService{
		createFn: func(_ context.Context, _ *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
			return nil, errSentinel
		},
	}))
	_, err := tr.CreateCollection(context.Background(), transport.CreateCollectionRequest{Database: "db", Name: "x"})
	if !errors.Is(err, errSentinel) {
		t.Fatalf("expected sentinel, got %v", err)
	}
}

func TestEmbeddedCollection_GetCollectionInfo(t *testing.T) {
	tr := newExtTransport(t, WithCollectionService(&mockCollectionService{}))
	info, err := tr.GetCollectionInfo(context.Background(), "db", "users")
	if err != nil {
		t.Fatalf("GetCollectionInfo: %v", err)
	}
	if info.Name != "users" {
		t.Fatalf("unexpected name %q", info.Name)
	}
}

func TestEmbeddedCollection_GetCollectionInfo_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.GetCollectionInfo(context.Background(), "db", "x")
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedCollection_GetCollectionInfo_ServiceError(t *testing.T) {
	tr := newExtTransport(t, WithCollectionService(&mockCollectionService{
		getInfoFn: func(_ context.Context, _, _ string) (*service.CollectionInfo, error) { return nil, errSentinel },
	}))
	_, err := tr.GetCollectionInfo(context.Background(), "db", "x")
	if !errors.Is(err, errSentinel) {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestEmbeddedCollection_ListCollections(t *testing.T) {
	tr := newExtTransport(t, WithCollectionService(&mockCollectionService{}))
	resp, err := tr.ListCollections(context.Background(), "db")
	if err != nil {
		t.Fatalf("ListCollections: %v", err)
	}
	if len(resp.Collections) != 1 || resp.Collections[0].Name != "users" {
		t.Fatalf("unexpected: %+v", resp)
	}
}

func TestEmbeddedCollection_ListCollections_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.ListCollections(context.Background(), "db")
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedCollection_ListCollections_ServiceError(t *testing.T) {
	tr := newExtTransport(t, WithCollectionService(&mockCollectionService{
		listFn: func(_ context.Context, _ *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
			return nil, errSentinel
		},
	}))
	_, err := tr.ListCollections(context.Background(), "db")
	if !errors.Is(err, errSentinel) {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestEmbeddedCollection_DropCollection(t *testing.T) {
	tr := newExtTransport(t, WithCollectionService(&mockCollectionService{}))
	if err := tr.DropCollection(context.Background(), "db", "users"); err != nil {
		t.Fatalf("DropCollection: %v", err)
	}
}

func TestEmbeddedCollection_DropCollection_NilService(t *testing.T) {
	tr := newExtTransport(t)
	err := tr.DropCollection(context.Background(), "db", "users")
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedCollection_InsertDocument(t *testing.T) {
	tr := newExtTransport(t, WithCollectionService(&mockCollectionService{}))
	resp, err := tr.InsertDocument(context.Background(), transport.InsertDocumentRequest{Database: "db", Collection: "users", Document: map[string]interface{}{"x": 1}})
	if err != nil {
		t.Fatalf("InsertDocument: %v", err)
	}
	if resp.ID != 42 {
		t.Fatalf("ID = %d", resp.ID)
	}
}

func TestEmbeddedCollection_InsertDocument_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.InsertDocument(context.Background(), transport.InsertDocumentRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedCollection_InsertDocument_ServiceError(t *testing.T) {
	tr := newExtTransport(t, WithCollectionService(&mockCollectionService{
		insertFn: func(_ context.Context, _ *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
			return nil, errSentinel
		},
	}))
	_, err := tr.InsertDocument(context.Background(), transport.InsertDocumentRequest{})
	if !errors.Is(err, errSentinel) {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestEmbeddedCollection_GetDocument(t *testing.T) {
	tr := newExtTransport(t, WithCollectionService(&mockCollectionService{}))
	doc, err := tr.GetDocument(context.Background(), "db", "users", 7)
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if doc.ID != 7 {
		t.Fatalf("ID = %d", doc.ID)
	}
}

func TestEmbeddedCollection_GetDocument_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.GetDocument(context.Background(), "db", "x", 1)
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedCollection_GetDocument_ServiceError(t *testing.T) {
	tr := newExtTransport(t, WithCollectionService(&mockCollectionService{
		getDocFn: func(_ context.Context, _ *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
			return nil, errSentinel
		},
	}))
	_, err := tr.GetDocument(context.Background(), "db", "x", 1)
	if !errors.Is(err, errSentinel) {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestEmbeddedCollection_UpdateDocument(t *testing.T) {
	tr := newExtTransport(t, WithCollectionService(&mockCollectionService{}))
	if err := tr.UpdateDocument(context.Background(), transport.UpdateDocumentRequest{Database: "db", Collection: "users", ID: 1}); err != nil {
		t.Fatalf("UpdateDocument: %v", err)
	}
}

func TestEmbeddedCollection_UpdateDocument_NilService(t *testing.T) {
	tr := newExtTransport(t)
	err := tr.UpdateDocument(context.Background(), transport.UpdateDocumentRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedCollection_DeleteDocument(t *testing.T) {
	tr := newExtTransport(t, WithCollectionService(&mockCollectionService{}))
	if err := tr.DeleteDocument(context.Background(), "db", "users", 1); err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}
}

func TestEmbeddedCollection_DeleteDocument_NilService(t *testing.T) {
	tr := newExtTransport(t)
	err := tr.DeleteDocument(context.Background(), "db", "users", 1)
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedCollection_ListDocuments(t *testing.T) {
	tr := newExtTransport(t, WithCollectionService(&mockCollectionService{}))
	resp, err := tr.ListDocuments(context.Background(), transport.ListDocumentsRequest{Database: "db", Collection: "users"})
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if resp.TotalCount != 1 || len(resp.Documents) != 1 {
		t.Fatalf("unexpected: %+v", resp)
	}
}

func TestEmbeddedCollection_ListDocuments_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.ListDocuments(context.Background(), transport.ListDocumentsRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedCollection_ListDocuments_ServiceError(t *testing.T) {
	tr := newExtTransport(t, WithCollectionService(&mockCollectionService{
		listDocsFn: func(_ context.Context, _ *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
			return nil, errSentinel
		},
	}))
	_, err := tr.ListDocuments(context.Background(), transport.ListDocumentsRequest{})
	if !errors.Is(err, errSentinel) {
		t.Fatalf("unexpected: %v", err)
	}
}

// ===========================================================================
// Mock: AuthFactorServicer
// ===========================================================================

type mockAuthFactorService struct {
	enrollFn    func(ctx context.Context, factorName, subject string, params authfactors.EnrollParams) (*authfactors.Enrollment, error)
	challengeFn func(ctx context.Context, enrollmentID string) (*authfactors.Challenge, error)
	verifyFn    func(ctx context.Context, factorName, challengeID string, response *authfactors.Response) error
	listFn      func(ctx context.Context, subject string) ([]*authfactors.Enrollment, error)
	revokeFn    func(ctx context.Context, enrollmentID string) error
}

func (m *mockAuthFactorService) EnrollFactor(ctx context.Context, factorName, subject string, params authfactors.EnrollParams) (*authfactors.Enrollment, error) {
	if m.enrollFn != nil {
		return m.enrollFn(ctx, factorName, subject, params)
	}
	return &authfactors.Enrollment{
		EnrollmentID: "enr-1",
		FactorName:   factorName,
		UserID:       subject,
		TenantID:     params.TenantID,
		Metadata:     map[string]string{"k": "v"},
		EnrolledAt:   time.Unix(1700000000, 0).UTC(),
	}, nil
}

func (m *mockAuthFactorService) ChallengeFactor(ctx context.Context, enrollmentID string) (*authfactors.Challenge, error) {
	if m.challengeFn != nil {
		return m.challengeFn(ctx, enrollmentID)
	}
	return &authfactors.Challenge{
		ChallengeID:  "ch-1",
		EnrollmentID: enrollmentID,
		Nonce:        []byte("nonce"),
		ExpiresAt:    time.Unix(1700001000, 0).UTC(),
		MethodData:   map[string]any{"method": "pkcs11"},
	}, nil
}

func (m *mockAuthFactorService) VerifyFactor(ctx context.Context, factorName, challengeID string, response *authfactors.Response) error {
	if m.verifyFn != nil {
		return m.verifyFn(ctx, factorName, challengeID, response)
	}
	return nil
}

func (m *mockAuthFactorService) ListFactors(ctx context.Context, subject string) ([]*authfactors.Enrollment, error) {
	if m.listFn != nil {
		return m.listFn(ctx, subject)
	}
	return []*authfactors.Enrollment{
		{EnrollmentID: "enr-1", FactorName: "pkcs11", UserID: subject, EnrolledAt: time.Unix(1700000000, 0).UTC()},
	}, nil
}

func (m *mockAuthFactorService) RevokeFactor(ctx context.Context, enrollmentID string) error {
	if m.revokeFn != nil {
		return m.revokeFn(ctx, enrollmentID)
	}
	return nil
}

var _ service.AuthFactorServicer = (*mockAuthFactorService)(nil)

// ===========================================================================
// AuthFactor tests
// ===========================================================================

func TestEmbeddedAuthFactor_Enroll(t *testing.T) {
	tr := newExtTransport(t, WithAuthFactorService(&mockAuthFactorService{}))
	resp, err := tr.EnrollFactor(context.Background(), &transport.EnrollFactorRequest{
		FactorName: "pkcs11",
		Subject:    "alice",
		TenantID:   "t1",
	})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}
	if resp.EnrollmentID != "enr-1" {
		t.Fatalf("EnrollmentID = %q", resp.EnrollmentID)
	}
	if resp.UserID != "alice" {
		t.Fatalf("UserID = %q", resp.UserID)
	}
}

func TestEmbeddedAuthFactor_Enroll_NilMethodParams(t *testing.T) {
	var got authfactors.EnrollParams
	tr := newExtTransport(t, WithAuthFactorService(&mockAuthFactorService{
		enrollFn: func(_ context.Context, _, _ string, params authfactors.EnrollParams) (*authfactors.Enrollment, error) {
			got = params
			return &authfactors.Enrollment{EnrollmentID: "x"}, nil
		},
	}))
	_, err := tr.EnrollFactor(context.Background(), &transport.EnrollFactorRequest{Subject: "alice"})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}
	if got.MethodParams == nil {
		t.Fatalf("MethodParams should be initialized when nil")
	}
}

func TestEmbeddedAuthFactor_Enroll_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.EnrollFactor(context.Background(), &transport.EnrollFactorRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedAuthFactor_Enroll_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithAuthFactorService(&mockAuthFactorService{}))
	_, err := tr.EnrollFactor(context.Background(), &transport.EnrollFactorRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedAuthFactor_Enroll_ServiceError(t *testing.T) {
	tr := newExtTransport(t, WithAuthFactorService(&mockAuthFactorService{
		enrollFn: func(_ context.Context, _, _ string, _ authfactors.EnrollParams) (*authfactors.Enrollment, error) {
			return nil, errSentinel
		},
	}))
	_, err := tr.EnrollFactor(context.Background(), &transport.EnrollFactorRequest{Subject: "alice"})
	if !errors.Is(err, errSentinel) {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestEmbeddedAuthFactor_Challenge(t *testing.T) {
	tr := newExtTransport(t, WithAuthFactorService(&mockAuthFactorService{}))
	resp, err := tr.ChallengeFactor(context.Background(), &transport.ChallengeFactorRequest{EnrollmentID: "enr-1"})
	if err != nil {
		t.Fatalf("ChallengeFactor: %v", err)
	}
	if resp.ChallengeID != "ch-1" {
		t.Fatalf("ChallengeID = %q", resp.ChallengeID)
	}
}

func TestEmbeddedAuthFactor_Challenge_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.ChallengeFactor(context.Background(), &transport.ChallengeFactorRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedAuthFactor_Challenge_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithAuthFactorService(&mockAuthFactorService{}))
	_, err := tr.ChallengeFactor(context.Background(), &transport.ChallengeFactorRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedAuthFactor_Challenge_ServiceError(t *testing.T) {
	tr := newExtTransport(t, WithAuthFactorService(&mockAuthFactorService{
		challengeFn: func(_ context.Context, _ string) (*authfactors.Challenge, error) { return nil, errSentinel },
	}))
	_, err := tr.ChallengeFactor(context.Background(), &transport.ChallengeFactorRequest{EnrollmentID: "x"})
	if !errors.Is(err, errSentinel) {
		t.Fatalf("unexpected: %v", err)
	}
}

// TestEmbeddedAuthFactor_SealedAfterResolve asserts the embedded transport
// hands a caller the barrier's own sentinel, unchanged, on every verb when a
// tenant seals between Resolve and the store operation — the error
// pkg/service's real-barrier test proves the routed service returns. The SDK
// exports it as sdk.ErrSealed (an alias of seal.ErrSealed), which is what the
// auth-factors API documentation tells an embedded caller to match.
func TestEmbeddedAuthFactor_SealedAfterResolve(t *testing.T) {
	tr := newExtTransport(t, WithAuthFactorService(&mockAuthFactorService{
		enrollFn: func(context.Context, string, string, authfactors.EnrollParams) (*authfactors.Enrollment, error) {
			return nil, seal.ErrSealed
		},
		challengeFn: func(context.Context, string) (*authfactors.Challenge, error) { return nil, seal.ErrSealed },
		verifyFn:    func(context.Context, string, string, *authfactors.Response) error { return seal.ErrSealed },
		listFn: func(context.Context, string) ([]*authfactors.Enrollment, error) {
			return nil, &service.AuthFactorListReadError{Key: "__sys:authfactor/e1", Err: seal.ErrSealed}
		},
		revokeFn: func(context.Context, string) error { return seal.ErrSealed },
	}))
	ctx := context.Background()
	calls := map[string]func() error{
		"EnrollFactor": func() error {
			_, err := tr.EnrollFactor(ctx, &transport.EnrollFactorRequest{FactorName: "piv", Subject: "u1"})
			return err
		},
		"ChallengeFactor": func() error {
			_, err := tr.ChallengeFactor(ctx, &transport.ChallengeFactorRequest{EnrollmentID: "e1"})
			return err
		},
		"VerifyFactor": func() error {
			return tr.VerifyFactor(ctx, &transport.VerifyFactorRequest{ChallengeID: "000000000001.c"})
		},
		"ListFactors": func() error {
			_, err := tr.ListFactors(ctx, &transport.ListFactorsRequest{})
			return err
		},
		"RevokeFactor": func() error {
			return tr.RevokeFactor(ctx, &transport.RevokeFactorRequest{EnrollmentID: "e1"})
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, seal.ErrSealed) {
				t.Errorf("%s = %v, want an error matching seal.ErrSealed", name, err)
			}
		})
	}
}

func TestEmbeddedAuthFactor_Verify(t *testing.T) {
	var capturedFactor, capturedCh string
	var capturedResp *authfactors.Response
	tr := newExtTransport(t, WithAuthFactorService(&mockAuthFactorService{
		verifyFn: func(_ context.Context, factor, ch string, r *authfactors.Response) error {
			capturedFactor = factor
			capturedCh = ch
			capturedResp = r
			return nil
		},
	}))
	if err := tr.VerifyFactor(context.Background(), &transport.VerifyFactorRequest{
		FactorName:  "pkcs11",
		ChallengeID: "ch-1",
		Value:       []byte("sig"),
	}); err != nil {
		t.Fatalf("VerifyFactor: %v", err)
	}
	if capturedFactor != "pkcs11" || capturedCh != "ch-1" {
		t.Fatalf("unexpected: %q %q", capturedFactor, capturedCh)
	}
	if capturedResp == nil || string(capturedResp.Value) != "sig" {
		t.Fatalf("unexpected response: %+v", capturedResp)
	}
	if capturedResp.Metadata == nil {
		t.Fatalf("Metadata should be initialized when nil")
	}
}

func TestEmbeddedAuthFactor_Verify_NilService(t *testing.T) {
	tr := newExtTransport(t)
	err := tr.VerifyFactor(context.Background(), &transport.VerifyFactorRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedAuthFactor_Verify_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithAuthFactorService(&mockAuthFactorService{}))
	err := tr.VerifyFactor(context.Background(), &transport.VerifyFactorRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedAuthFactor_Verify_ServiceError(t *testing.T) {
	tr := newExtTransport(t, WithAuthFactorService(&mockAuthFactorService{
		verifyFn: func(_ context.Context, _, _ string, _ *authfactors.Response) error { return errSentinel },
	}))
	err := tr.VerifyFactor(context.Background(), &transport.VerifyFactorRequest{})
	if !errors.Is(err, errSentinel) {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestEmbeddedAuthFactor_List(t *testing.T) {
	tr := newExtTransport(t, WithAuthFactorService(&mockAuthFactorService{}))
	resp, err := tr.ListFactors(context.Background(), &transport.ListFactorsRequest{Subject: "alice"})
	if err != nil {
		t.Fatalf("ListFactors: %v", err)
	}
	if resp.Total != 1 || len(resp.Enrollments) != 1 {
		t.Fatalf("unexpected: %+v", resp)
	}
}

func TestEmbeddedAuthFactor_List_NilRequest(t *testing.T) {
	tr := newExtTransport(t, WithAuthFactorService(&mockAuthFactorService{}))
	resp, err := tr.ListFactors(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListFactors: %v", err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
}

func TestEmbeddedAuthFactor_List_WithRevoked(t *testing.T) {
	tr := newExtTransport(t, WithAuthFactorService(&mockAuthFactorService{
		listFn: func(_ context.Context, _ string) ([]*authfactors.Enrollment, error) {
			return []*authfactors.Enrollment{
				{EnrollmentID: "a", EnrolledAt: time.Unix(1700000000, 0), RevokedAt: time.Unix(1700001000, 0)},
				{EnrollmentID: "b", EnrolledAt: time.Unix(1700000000, 0)},
			}, nil
		},
	}))
	resp, err := tr.ListFactors(context.Background(), &transport.ListFactorsRequest{Subject: "alice"})
	if err != nil {
		t.Fatalf("ListFactors: %v", err)
	}
	if resp.Enrollments[0].RevokedAt == nil {
		t.Fatal("expected RevokedAt set for revoked enrollment")
	}
	if resp.Enrollments[1].RevokedAt != nil {
		t.Fatal("expected RevokedAt nil for active enrollment")
	}
}

func TestEmbeddedAuthFactor_List_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.ListFactors(context.Background(), &transport.ListFactorsRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedAuthFactor_List_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithAuthFactorService(&mockAuthFactorService{}))
	_, err := tr.ListFactors(context.Background(), &transport.ListFactorsRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedAuthFactor_List_ServiceError(t *testing.T) {
	tr := newExtTransport(t, WithAuthFactorService(&mockAuthFactorService{
		listFn: func(_ context.Context, _ string) ([]*authfactors.Enrollment, error) { return nil, errSentinel },
	}))
	_, err := tr.ListFactors(context.Background(), &transport.ListFactorsRequest{})
	if !errors.Is(err, errSentinel) {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestEmbeddedAuthFactor_Revoke(t *testing.T) {
	tr := newExtTransport(t, WithAuthFactorService(&mockAuthFactorService{}))
	if err := tr.RevokeFactor(context.Background(), &transport.RevokeFactorRequest{EnrollmentID: "enr-1"}); err != nil {
		t.Fatalf("RevokeFactor: %v", err)
	}
}

func TestEmbeddedAuthFactor_Revoke_NilService(t *testing.T) {
	tr := newExtTransport(t)
	err := tr.RevokeFactor(context.Background(), &transport.RevokeFactorRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedAuthFactor_Revoke_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithAuthFactorService(&mockAuthFactorService{}))
	err := tr.RevokeFactor(context.Background(), &transport.RevokeFactorRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedAuthFactor_WithAuthFactorService_Option(t *testing.T) {
	svc := &mockAuthFactorService{}
	tr := newExtTransport(t, WithAuthFactorService(svc))
	if tr.authFactorSvc != svc {
		t.Fatal("WithAuthFactorService did not assign service")
	}
}

// ===========================================================================
// Mock: CeremonyServicer + CeremonyCA stub
// ===========================================================================

type mockCeremonyService struct {
	initFn       func(ctx context.Context, ca ceremony.CeremonyCA) (*service.CeremonyInitResult, error)
	lastInitOpts *service.CeremonyInitializeOptions
	initOptsSeen bool
	claimShareFn func(ctx context.Context, username string) ([]byte, error)
	signCSRFn    func(ctx context.Context, username, soPin, csrPEM, role string) (*service.CeremonySignCSRResult, error)
	statusFn     func(ctx context.Context) (*service.CeremonyStatusInfo, error)
}

func (m *mockCeremonyService) CeremonyInitialize(ctx context.Context, ca ceremony.CeremonyCA, opts *service.CeremonyInitializeOptions) (*service.CeremonyInitResult, error) {
	m.lastInitOpts = opts
	m.initOptsSeen = true
	if m.initFn != nil {
		return m.initFn(ctx, ca)
	}
	return &service.CeremonyInitResult{
		SPKIPin:    "pin",
		CACertPEM:  []byte("CA"),
		TLSCertPEM: []byte("TLS"),
		State:      "initialized",
		ShareCount: 3,
	}, nil
}

func (m *mockCeremonyService) CeremonySignCSR(ctx context.Context, username, soPin, csrPEM, role string) (*service.CeremonySignCSRResult, error) {
	if m.signCSRFn != nil {
		return m.signCSRFn(ctx, username, soPin, csrPEM, role)
	}
	return &service.CeremonySignCSRResult{CertPEM: []byte("CERT")}, nil
}

func (m *mockCeremonyService) CeremonyStatus(ctx context.Context) (*service.CeremonyStatusInfo, error) {
	if m.statusFn != nil {
		return m.statusFn(ctx)
	}
	return &service.CeremonyStatusInfo{State: "initialized", SPKIPin: "pin"}, nil
}

var _ service.CeremonyServicer = (*mockCeremonyService)(nil)

// Minimal CeremonyCA stub satisfying the interface.
type mockCeremonyCA struct{}

func (m *mockCeremonyCA) SignCSR(_ context.Context, _ []byte, _ *ceremony.SignCSROptions) (*x509.Certificate, error) {
	return nil, errors.New("not used")
}

func (m *mockCeremonyCA) CACertificate(context.Context) (*x509.Certificate, error) {
	return nil, errors.New("not used")
}

func (m *mockCeremonyCA) CABundle() ([]byte, error) {
	return nil, errors.New("not used")
}

func (m *mockCeremonyCA) IssueCertificate(_ context.Context, _ *ceremony.IssueCertRequest) (*ceremony.IssuedCert, error) {
	return nil, errors.New("not used")
}

var _ ceremony.CeremonyCA = (*mockCeremonyCA)(nil)

// ===========================================================================
// Ceremony tests
// ===========================================================================

func TestEmbeddedCeremony_Initialize(t *testing.T) {
	tr := newExtTransport(t,
		WithCeremonyService(&mockCeremonyService{}),
		WithCeremonyCA(&mockCeremonyCA{}),
	)
	resp, err := tr.CeremonyInitialize(context.Background(), &transport.CeremonyInitializeRequest{})
	if err != nil {
		t.Fatalf("CeremonyInitialize: %v", err)
	}
	if resp.SPKIPin != "pin" || resp.State != "initialized" {
		t.Fatalf("unexpected: %+v", resp)
	}
	if resp.ShareCount != 3 {
		t.Fatalf("ShareCount = %d", resp.ShareCount)
	}
}

func TestEmbeddedCeremony_Initialize_NilService(t *testing.T) {
	tr := newExtTransport(t, WithCeremonyCA(&mockCeremonyCA{}))
	_, err := tr.CeremonyInitialize(context.Background(), &transport.CeremonyInitializeRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

// TestEmbeddedCeremony_Initialize_NilCAReachesTheService proves a transport
// with a ceremony service but no CeremonyCA (WithCeremonyCA never applied,
// as a BYO-TLS deployment configures it) reaches the ceremony service layer
// with ca == nil rather than being refused by the transport.
// ceremony.CeremonyService.Initialize is nil-safe by construction -- a
// BYO-TLS node pins its own configured leaf instead of self-issuing one --
// so refusing here (the pre-fix behavior, go-qrdb#560 review item 5) only
// blocked `qrdb init` on a BYO-TLS embedded deployment, for no safety
// reason.
func TestEmbeddedCeremony_Initialize_NilCAReachesTheService(t *testing.T) {
	mock := &mockCeremonyService{}
	tr := newExtTransport(t, WithCeremonyService(mock))
	if _, err := tr.CeremonyInitialize(context.Background(), &transport.CeremonyInitializeRequest{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !mock.initOptsSeen {
		t.Fatal("expected the service to be reached with a nil CA (BYO-TLS)")
	}
}

func TestEmbeddedCeremony_Initialize_Closed(t *testing.T) {
	tr := closedExtTransport(t,
		WithCeremonyService(&mockCeremonyService{}),
		WithCeremonyCA(&mockCeremonyCA{}),
	)
	_, err := tr.CeremonyInitialize(context.Background(), &transport.CeremonyInitializeRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedCeremony_Initialize_ServiceError(t *testing.T) {
	tr := newExtTransport(t,
		WithCeremonyService(&mockCeremonyService{
			initFn: func(_ context.Context, _ ceremony.CeremonyCA) (*service.CeremonyInitResult, error) {
				return nil, errSentinel
			},
		}),
		WithCeremonyCA(&mockCeremonyCA{}),
	)
	_, err := tr.CeremonyInitialize(context.Background(), &transport.CeremonyInitializeRequest{})
	if !errors.Is(err, errSentinel) {
		t.Fatalf("unexpected: %v", err)
	}
}

// TestEmbeddedCeremony_Initialize_ForwardsPins proves the embedded transport
// no longer discards the request: the SO and user PINs supplied on the
// CeremonyInitializeRequest reach the service layer as CeremonyInitializeOptions.
// This is the regression guard for the gap where the embedded transport called
// the service with the injected CA only and dropped req entirely.
func TestEmbeddedCeremony_Initialize_ForwardsPins(t *testing.T) {
	mock := &mockCeremonyService{}
	tr := newExtTransport(t,
		WithCeremonyService(mock),
		WithCeremonyCA(&mockCeremonyCA{}),
	)

	_, err := tr.CeremonyInitialize(context.Background(), &transport.CeremonyInitializeRequest{
		SOPin:   "so-secret-pin",
		UserPin: "user-secret-pin",
	})
	if err != nil {
		t.Fatalf("CeremonyInitialize: %v", err)
	}
	if !mock.initOptsSeen {
		t.Fatal("service was not invoked")
	}
	if mock.lastInitOpts == nil {
		t.Fatal("expected non-nil CeremonyInitializeOptions to reach the service; pins were dropped")
	}
	if mock.lastInitOpts.SOPin != "so-secret-pin" {
		t.Fatalf("SOPin not forwarded: got %q", mock.lastInitOpts.SOPin)
	}
	if mock.lastInitOpts.UserPin != "user-secret-pin" {
		t.Fatalf("UserPin not forwarded: got %q", mock.lastInitOpts.UserPin)
	}
}

// TestEmbeddedCeremony_Initialize_ForwardsAllFields is the regression guard
// for the bug where the embedded transport only forwarded SOPin/UserPin into
// service.CeremonyInitializeOptions, silently dropping
// CredentialSealStrategy, Threshold, Officers, Hostname, and SANs from the
// CeremonyInitializeRequest.
func TestEmbeddedCeremony_Initialize_ForwardsAllFields(t *testing.T) {
	mock := &mockCeremonyService{}
	tr := newExtTransport(t,
		WithCeremonyService(mock),
		WithCeremonyCA(&mockCeremonyCA{}),
	)

	_, err := tr.CeremonyInitialize(context.Background(), &transport.CeremonyInitializeRequest{
		SOPin:                  "so-pin",
		UserPin:                "user-pin",
		CredentialSealStrategy: "shamir",
		Threshold:              2,
		Officers: []transport.CeremonyOfficerRequest{
			{Username: "officer-1", CSRPEM: "csr1"},
			{Username: "officer-2", CSRPEM: "csr2"},
		},
		Hostname: "override.example",
		SANs:     []string{"alt1.example", "alt2.example"},
	})
	if err != nil {
		t.Fatalf("CeremonyInitialize: %v", err)
	}

	got := mock.lastInitOpts
	if got == nil {
		t.Fatal("expected non-nil CeremonyInitializeOptions to reach the service")
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

// TestEmbeddedCeremony_Initialize_WipesAdminKeyPEM proves the generate
// path's private key is wiped from the service-layer result
// (CeremonyInitResult.WipeAdminKeyPEM) once this transport has built its
// own, independent copy for the response -- go-qrdb#560 review item 7:
// "WipeAdminKeyPEM runs on every transport, not only REST." The caller
// must still receive the correct, un-zeroed key: proving the wipe happens
// AFTER holderwire.CeremonyInitTo's bytes.Clone, never before it.
func TestEmbeddedCeremony_Initialize_WipesAdminKeyPEM(t *testing.T) {
	result := &service.CeremonyInitResult{
		SPKIPin:               "pin",
		State:                 "initialized",
		AdminCertPEM:          []byte("admin-cert"),
		AdminKeyPEM:           []byte("admin-key"),
		AdminCredentialSource: "generated",
	}
	mock := &mockCeremonyService{
		initFn: func(_ context.Context, _ ceremony.CeremonyCA) (*service.CeremonyInitResult, error) {
			return result, nil
		},
	}
	tr := newExtTransport(t, WithCeremonyService(mock), WithCeremonyCA(&mockCeremonyCA{}))

	resp, err := tr.CeremonyInitialize(context.Background(), &transport.CeremonyInitializeRequest{})
	if err != nil {
		t.Fatalf("CeremonyInitialize: %v", err)
	}
	if string(resp.AdminKeyPEM) != "admin-key" {
		t.Fatalf("caller must still receive the un-zeroed key, got %q", resp.AdminKeyPEM)
	}
	if result.AdminKeyPEM != nil {
		t.Fatalf("expected the service result's AdminKeyPEM to be wiped (nil) after the call, got %q", result.AdminKeyPEM)
	}
}

// TestEmbeddedCeremony_Initialize_NilRequest ensures the embedded transport
// rejects a nil request rather than dereferencing it.
func TestEmbeddedCeremony_Initialize_NilRequest(t *testing.T) {
	tr := newExtTransport(t,
		WithCeremonyService(&mockCeremonyService{}),
		WithCeremonyCA(&mockCeremonyCA{}),
	)
	_, err := tr.CeremonyInitialize(context.Background(), nil)
	var ire *InvalidRequestError
	if !errors.As(err, &ire) {
		t.Fatalf("expected InvalidRequestError, got %T (%v)", err, err)
	}
}

func TestEmbeddedCeremony_Status(t *testing.T) {
	tr := newExtTransport(t, WithCeremonyService(&mockCeremonyService{}))
	resp, err := tr.CeremonyStatus(context.Background())
	if err != nil {
		t.Fatalf("CeremonyStatus: %v", err)
	}
	if resp.State != "initialized" || resp.SPKIPin != "pin" {
		t.Fatalf("unexpected: %+v", resp)
	}
}

func TestEmbeddedCeremony_Status_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.CeremonyStatus(context.Background())
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedCeremony_Status_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithCeremonyService(&mockCeremonyService{}))
	_, err := tr.CeremonyStatus(context.Background())
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedCeremony_Status_ServiceError(t *testing.T) {
	tr := newExtTransport(t, WithCeremonyService(&mockCeremonyService{
		statusFn: func(_ context.Context) (*service.CeremonyStatusInfo, error) { return nil, errSentinel },
	}))
	_, err := tr.CeremonyStatus(context.Background())
	if !errors.Is(err, errSentinel) {
		t.Fatalf("unexpected: %v", err)
	}
}

// ctxWithEmbeddedPeerCert returns a context carrying a self-signed leaf as the
// mTLS peer certificate, mirroring what a real transport installs.
func ctxWithEmbeddedPeerCert(t *testing.T) context.Context {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "embedded-officer"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return auth.WithTLSState(context.Background(), &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
	})
}

func TestEmbeddedCeremony_SignCSR(t *testing.T) {
	tr := newExtTransport(t, WithCeremonyService(&mockCeremonyService{}))
	resp, err := tr.CeremonySignCSR(context.Background(), &transport.SignCSRInitRequest{
		Username: "alice",
		SOPin:    "1234",
		CSRPEM:   "csr",
		Role:     "officer",
	})
	if err != nil {
		t.Fatalf("CeremonySignCSR: %v", err)
	}
	if resp.CertPEM != "CERT" {
		t.Fatalf("CertPEM = %q", resp.CertPEM)
	}
}

func TestEmbeddedCeremony_SignCSR_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.CeremonySignCSR(context.Background(), &transport.SignCSRInitRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedCeremony_SignCSR_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithCeremonyService(&mockCeremonyService{}))
	_, err := tr.CeremonySignCSR(context.Background(), &transport.SignCSRInitRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedCeremony_SignCSR_ServiceError(t *testing.T) {
	tr := newExtTransport(t, WithCeremonyService(&mockCeremonyService{
		signCSRFn: func(_ context.Context, _, _, _, _ string) (*service.CeremonySignCSRResult, error) {
			return nil, errSentinel
		},
	}))
	_, err := tr.CeremonySignCSR(context.Background(), &transport.SignCSRInitRequest{})
	if !errors.Is(err, errSentinel) {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestEmbeddedCeremony_WithCeremonyService_Option(t *testing.T) {
	svc := &mockCeremonyService{}
	tr := newExtTransport(t, WithCeremonyService(svc))
	if tr.ceremonySvc != svc {
		t.Fatal("WithCeremonyService did not assign service")
	}
}

func TestEmbeddedCeremony_WithCeremonyCA_Option(t *testing.T) {
	ca := &mockCeremonyCA{}
	tr := newExtTransport(t, WithCeremonyCA(ca))
	if tr.ceremonyCA != ca {
		t.Fatal("WithCeremonyCA did not assign CA")
	}
}

// ===========================================================================
// Ceremony dispatch handler coverage
// ===========================================================================

// ===========================================================================
// Mock: LeaseServicer
// ===========================================================================

type mockLeaseService struct {
	grantFn     func(ctx context.Context, ttl int64) (*lease.Lease, error)
	revokeFn    func(ctx context.Context, id int64) error
	keepAliveFn func(ctx context.Context, id int64) (*lease.Lease, error)
	ttlFn       func(ctx context.Context, id int64) (*lease.Lease, error)
	leasesFn    func(ctx context.Context) ([]*lease.Lease, error)
}

func (m *mockLeaseService) Grant(ctx context.Context, ttl int64) (*lease.Lease, error) {
	if m.grantFn != nil {
		return m.grantFn(ctx, ttl)
	}
	return &lease.Lease{ID: 42, TTL: ttl, GrantedTTL: ttl, RemainingTTL: ttl}, nil
}

func (m *mockLeaseService) Revoke(ctx context.Context, id int64) error {
	if m.revokeFn != nil {
		return m.revokeFn(ctx, id)
	}
	return nil
}

func (m *mockLeaseService) KeepAlive(ctx context.Context, id int64) (*lease.Lease, error) {
	if m.keepAliveFn != nil {
		return m.keepAliveFn(ctx, id)
	}
	return &lease.Lease{ID: id, TTL: 60, RemainingTTL: 60}, nil
}

func (m *mockLeaseService) TimeToLive(ctx context.Context, id int64) (*lease.Lease, error) {
	if m.ttlFn != nil {
		return m.ttlFn(ctx, id)
	}
	return &lease.Lease{ID: id, TTL: 60, RemainingTTL: 30}, nil
}

func (m *mockLeaseService) Leases(ctx context.Context) ([]*lease.Lease, error) {
	if m.leasesFn != nil {
		return m.leasesFn(ctx)
	}
	return []*lease.Lease{{ID: 1, TTL: 60}, {ID: 2, TTL: 120}}, nil
}

var _ lease.LeaseServicer = (*mockLeaseService)(nil)

// ===========================================================================
// Lease tests
// ===========================================================================

func TestEmbeddedLease_Grant(t *testing.T) {
	tr := newExtTransport(t, WithLeaseService(&mockLeaseService{}))
	info, err := tr.LeaseGrant(context.Background(), 60)
	if err != nil {
		t.Fatalf("LeaseGrant: %v", err)
	}
	if info.ID != 42 || info.TTL != 60 {
		t.Fatalf("unexpected: %+v", info)
	}
}

func TestEmbeddedLease_Grant_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.LeaseGrant(context.Background(), 60)
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedLease_Grant_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithLeaseService(&mockLeaseService{}))
	_, err := tr.LeaseGrant(context.Background(), 60)
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedLease_Grant_ServiceError(t *testing.T) {
	tr := newExtTransport(t, WithLeaseService(&mockLeaseService{
		grantFn: func(_ context.Context, _ int64) (*lease.Lease, error) { return nil, errSentinel },
	}))
	_, err := tr.LeaseGrant(context.Background(), 60)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestEmbeddedLease_Revoke(t *testing.T) {
	tr := newExtTransport(t, WithLeaseService(&mockLeaseService{}))
	if err := tr.LeaseRevoke(context.Background(), 42); err != nil {
		t.Fatalf("LeaseRevoke: %v", err)
	}
}

func TestEmbeddedLease_Revoke_NilService(t *testing.T) {
	tr := newExtTransport(t)
	err := tr.LeaseRevoke(context.Background(), 42)
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedLease_Revoke_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithLeaseService(&mockLeaseService{}))
	err := tr.LeaseRevoke(context.Background(), 42)
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedLease_KeepAlive(t *testing.T) {
	tr := newExtTransport(t, WithLeaseService(&mockLeaseService{}))
	info, err := tr.LeaseKeepAlive(context.Background(), 42)
	if err != nil {
		t.Fatalf("LeaseKeepAlive: %v", err)
	}
	if info.ID != 42 {
		t.Fatalf("ID = %d", info.ID)
	}
}

func TestEmbeddedLease_KeepAlive_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.LeaseKeepAlive(context.Background(), 42)
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedLease_KeepAlive_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithLeaseService(&mockLeaseService{}))
	_, err := tr.LeaseKeepAlive(context.Background(), 42)
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedLease_KeepAlive_ServiceError(t *testing.T) {
	tr := newExtTransport(t, WithLeaseService(&mockLeaseService{
		keepAliveFn: func(_ context.Context, _ int64) (*lease.Lease, error) { return nil, errSentinel },
	}))
	_, err := tr.LeaseKeepAlive(context.Background(), 42)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestEmbeddedLease_TimeToLive(t *testing.T) {
	tr := newExtTransport(t, WithLeaseService(&mockLeaseService{}))
	info, err := tr.LeaseTimeToLive(context.Background(), 42)
	if err != nil {
		t.Fatalf("LeaseTimeToLive: %v", err)
	}
	if info.ID != 42 {
		t.Fatalf("ID = %d", info.ID)
	}
}

func TestEmbeddedLease_TimeToLive_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.LeaseTimeToLive(context.Background(), 42)
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedLease_TimeToLive_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithLeaseService(&mockLeaseService{}))
	_, err := tr.LeaseTimeToLive(context.Background(), 42)
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedLease_TimeToLive_ServiceError(t *testing.T) {
	tr := newExtTransport(t, WithLeaseService(&mockLeaseService{
		ttlFn: func(_ context.Context, _ int64) (*lease.Lease, error) { return nil, errSentinel },
	}))
	_, err := tr.LeaseTimeToLive(context.Background(), 42)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestEmbeddedLease_List(t *testing.T) {
	tr := newExtTransport(t, WithLeaseService(&mockLeaseService{}))
	leases, err := tr.LeaseList(context.Background())
	if err != nil {
		t.Fatalf("LeaseList: %v", err)
	}
	if len(leases) != 2 {
		t.Fatalf("got %d leases", len(leases))
	}
}

func TestEmbeddedLease_List_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.LeaseList(context.Background())
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedLease_List_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithLeaseService(&mockLeaseService{}))
	_, err := tr.LeaseList(context.Background())
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedLease_List_ServiceError(t *testing.T) {
	tr := newExtTransport(t, WithLeaseService(&mockLeaseService{
		leasesFn: func(_ context.Context) ([]*lease.Lease, error) { return nil, errSentinel },
	}))
	_, err := tr.LeaseList(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// Lease dispatch handler coverage
// ===========================================================================

func TestEmbeddedLease_Dispatch(t *testing.T) {
	tr := newExtTransport(t, WithLeaseService(&mockLeaseService{}))

	t.Run("Grant", func(t *testing.T) {
		var resp *transport.LeaseInfo
		if err := tr.Request(context.Background(), "LeaseGrant", &leaseGrantRequest{TTL: 60}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		if resp == nil {
			t.Fatal("nil resp")
		}
	})
	t.Run("GrantBadReq", func(t *testing.T) {
		err := tr.Request(context.Background(), "LeaseGrant", "bad", nil)
		var ie *InvalidRequestError
		if !errors.As(err, &ie) {
			t.Fatalf("expected InvalidRequestError, got %T", err)
		}
	})
	t.Run("Revoke", func(t *testing.T) {
		if err := tr.Request(context.Background(), "LeaseRevoke", &leaseRevokeRequest{LeaseID: 1}, nil); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("RevokeBadReq", func(t *testing.T) {
		err := tr.Request(context.Background(), "LeaseRevoke", "bad", nil)
		var ie *InvalidRequestError
		if !errors.As(err, &ie) {
			t.Fatalf("expected InvalidRequestError, got %T", err)
		}
	})
	t.Run("KeepAlive", func(t *testing.T) {
		var resp *transport.LeaseInfo
		if err := tr.Request(context.Background(), "LeaseKeepAlive", &leaseKeepAliveRequest{LeaseID: 1}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("KeepAliveBadReq", func(t *testing.T) {
		err := tr.Request(context.Background(), "LeaseKeepAlive", "bad", nil)
		var ie *InvalidRequestError
		if !errors.As(err, &ie) {
			t.Fatalf("expected InvalidRequestError, got %T", err)
		}
	})
	t.Run("TimeToLive", func(t *testing.T) {
		var resp *transport.LeaseInfo
		if err := tr.Request(context.Background(), "LeaseTimeToLive", &leaseTimeToLiveRequest{LeaseID: 1}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("TimeToLiveBadReq", func(t *testing.T) {
		err := tr.Request(context.Background(), "LeaseTimeToLive", "bad", nil)
		var ie *InvalidRequestError
		if !errors.As(err, &ie) {
			t.Fatalf("expected InvalidRequestError, got %T", err)
		}
	})
	t.Run("List", func(t *testing.T) {
		var resp []*transport.LeaseInfo
		if err := tr.Request(context.Background(), "LeaseList", nil, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
}

// ===========================================================================
// Mock: WatchServicer
// ===========================================================================

// stubWatcher implements watch.Watcher for tests.
type stubWatcher struct {
	ch chan watch.WatchResponse
}

func newStubWatcher() *stubWatcher {
	w := &stubWatcher{ch: make(chan watch.WatchResponse, 4)}
	return w
}

func (s *stubWatcher) Events() <-chan watch.WatchResponse { return s.ch }
func (s *stubWatcher) WatchID() int64                     { return 1 }
func (s *stubWatcher) Close() error                       { return nil }
func (s *stubWatcher) Err() error                         { return nil }

type mockWatchService struct {
	watchFn       func(ctx context.Context, key string, opts ...watch.WatchOption) (watch.Watcher, error)
	watchPrefixFn func(ctx context.Context, prefix string, opts ...watch.WatchOption) (watch.Watcher, error)
}

func (m *mockWatchService) Watch(ctx context.Context, key string, opts ...watch.WatchOption) (watch.Watcher, error) {
	if m.watchFn != nil {
		return m.watchFn(ctx, key, opts...)
	}
	w := newStubWatcher()
	close(w.ch)
	return w, nil
}

func (m *mockWatchService) WatchPrefix(ctx context.Context, prefix string, opts ...watch.WatchOption) (watch.Watcher, error) {
	if m.watchPrefixFn != nil {
		return m.watchPrefixFn(ctx, prefix, opts...)
	}
	w := newStubWatcher()
	close(w.ch)
	return w, nil
}

// WatchEntity / WatchEntityType are required by service.WatchServicer interface,
// but the embedded transport doesn't use them so they return errors.
func (m *mockWatchService) WatchEntity(_ context.Context, _ *service.WatchEntityRequest) (<-chan service.WatchEntityEvent, error) {
	return nil, errors.New("not implemented")
}

func (m *mockWatchService) WatchEntityType(_ context.Context, _ *service.WatchEntityTypeRequest) (<-chan service.WatchEntityEvent, error) {
	return nil, errors.New("not implemented")
}

var _ service.WatchServicer = (*mockWatchService)(nil)

// ===========================================================================
// Watch tests
// ===========================================================================

func TestEmbeddedWatch_Watch_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.Watch(context.Background(), "k", transport.WatchOptions{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedWatch_Watch_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithWatchService(&mockWatchService{}))
	_, err := tr.Watch(context.Background(), "k", transport.WatchOptions{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedWatch_Watch_Success(t *testing.T) {
	tr := newExtTransport(t, WithWatchService(&mockWatchService{}))
	ch, err := tr.Watch(context.Background(), "k", transport.WatchOptions{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	// Drain the channel — it should close immediately.
	for range ch {
	}
}

func TestEmbeddedWatch_Watch_ServiceError(t *testing.T) {
	tr := newExtTransport(t, WithWatchService(&mockWatchService{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return nil, errSentinel
		},
	}))
	_, err := tr.Watch(context.Background(), "k", transport.WatchOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestEmbeddedWatch_WatchPrefix_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.WatchPrefix(context.Background(), "p", transport.WatchOptions{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedWatch_WatchPrefix_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithWatchService(&mockWatchService{}))
	_, err := tr.WatchPrefix(context.Background(), "p", transport.WatchOptions{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedWatch_WatchPrefix_Success(t *testing.T) {
	tr := newExtTransport(t, WithWatchService(&mockWatchService{}))
	ch, err := tr.WatchPrefix(context.Background(), "p", transport.WatchOptions{StartRevision: 1, PrevKV: true, ProgressNotify: true})
	if err != nil {
		t.Fatalf("WatchPrefix: %v", err)
	}
	for range ch {
	}
}

func TestEmbeddedWatch_WatchPrefix_ServiceError(t *testing.T) {
	tr := newExtTransport(t, WithWatchService(&mockWatchService{
		watchPrefixFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return nil, errSentinel
		},
	}))
	_, err := tr.WatchPrefix(context.Background(), "p", transport.WatchOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// Mock: TimeSeriesServicer
// ===========================================================================

type mockTSService struct {
	writeFn       func(ctx context.Context, metric string, value float64, ts time.Time, tags map[string]string) error
	writeBatchFn  func(ctx context.Context, points []timeseries.DataPoint) error
	queryFn       func(ctx context.Context, metric string, start, end time.Time, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error)
	aggregateFn   func(ctx context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration) ([]timeseries.AggResult, error)
	writePointFn  func(ctx context.Context, point *timeseries.DataPoint) error
	queryRichFn   func(ctx context.Context, req *timeseries.QueryRequest) (*timeseries.QueryResult, error)
	deleteFn      func(ctx context.Context, req *timeseries.DeleteRequest) error
	getRetFn      func(metric string) (*timeseries.RetentionPolicy, error)
	setRetFn      func(metric string, policy *timeseries.RetentionPolicy) error
	listMetricsFn func(prefix string, limit int) ([]string, error)
}

func (m *mockTSService) Write(ctx context.Context, metric string, value float64, ts time.Time, tags map[string]string) error {
	if m.writeFn != nil {
		return m.writeFn(ctx, metric, value, ts, tags)
	}
	return nil
}

func (m *mockTSService) WriteBatch(ctx context.Context, points []timeseries.DataPoint) error {
	if m.writeBatchFn != nil {
		return m.writeBatchFn(ctx, points)
	}
	return nil
}

func (m *mockTSService) Query(ctx context.Context, metric string, start, end time.Time, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	if m.queryFn != nil {
		return m.queryFn(ctx, metric, start, end, opts...)
	}
	return []timeseries.DataPoint{{Metric: metric, Value: 1.0, Timestamp: start}}, nil
}

func (m *mockTSService) QueryWithConsistency(ctx context.Context, metric string, start, end time.Time, _ consistency.ReadOptions, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	return m.Query(ctx, metric, start, end, opts...)
}

func (m *mockTSService) Aggregate(ctx context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration) ([]timeseries.AggResult, error) {
	if m.aggregateFn != nil {
		return m.aggregateFn(ctx, metric, start, end, fn, interval)
	}
	return []timeseries.AggResult{{Timestamp: start, Value: 100.0, Count: 1}}, nil
}

func (m *mockTSService) AggregateWithConsistency(ctx context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration, _ consistency.ReadOptions) ([]timeseries.AggResult, error) {
	return m.Aggregate(ctx, metric, start, end, fn, interval)
}

func (m *mockTSService) WritePoint(ctx context.Context, point *timeseries.DataPoint) error {
	if m.writePointFn != nil {
		return m.writePointFn(ctx, point)
	}
	return nil
}

func (m *mockTSService) QueryRich(ctx context.Context, req *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
	if m.queryRichFn != nil {
		return m.queryRichFn(ctx, req)
	}
	return &timeseries.QueryResult{Metric: req.Metric, Points: []*timeseries.DataPoint{{Metric: req.Metric, Value: 1.0, Timestamp: req.StartTime}}}, nil
}

func (m *mockTSService) QueryRichWithConsistency(ctx context.Context, req *timeseries.QueryRequest, _ consistency.ReadOptions) (*timeseries.QueryResult, error) {
	return m.QueryRich(ctx, req)
}

func (m *mockTSService) DeleteSeries(ctx context.Context, req *timeseries.DeleteRequest) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, req)
	}
	return nil
}

func (m *mockTSService) GetRetentionPolicy(metric string) (*timeseries.RetentionPolicy, error) {
	if m.getRetFn != nil {
		return m.getRetFn(metric)
	}
	return &timeseries.RetentionPolicy{Duration: 24 * time.Hour}, nil
}

func (m *mockTSService) SetRetentionPolicy(_ context.Context, metric string, policy *timeseries.RetentionPolicy) error {
	if m.setRetFn != nil {
		return m.setRetFn(metric, policy)
	}
	return nil
}

func (m *mockTSService) ListMetrics(prefix string, limit int) ([]string, error) {
	if m.listMetricsFn != nil {
		return m.listMetricsFn(prefix, limit)
	}
	return []string{"cpu.usage", "mem.free"}, nil
}

func (m *mockTSService) ListMetricsWithConsistency(_ context.Context, prefix string, limit int, _ consistency.ReadOptions) ([]string, error) {
	return m.ListMetrics(prefix, limit)
}

var _ timeseries.TimeSeriesServicer = (*mockTSService)(nil)

// ===========================================================================
// TimeSeries tests
// ===========================================================================

func TestEmbeddedTS_Write(t *testing.T) {
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	if err := tr.TSWrite(context.Background(), &transport.TSDataPoint{Metric: "m", Value: 1.0, TimestampNs: 1700000000000000000}); err != nil {
		t.Fatalf("TSWrite: %v", err)
	}
}

func TestEmbeddedTS_Write_NilService(t *testing.T) {
	tr := newExtTransport(t)
	err := tr.TSWrite(context.Background(), &transport.TSDataPoint{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedTS_Write_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	err := tr.TSWrite(context.Background(), &transport.TSDataPoint{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedTS_WriteBatch(t *testing.T) {
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	if err := tr.TSWriteBatch(context.Background(), []transport.TSDataPoint{
		{Metric: "m", Value: 1.0, TimestampNs: 1700000000000000000},
	}); err != nil {
		t.Fatalf("TSWriteBatch: %v", err)
	}
}

func TestEmbeddedTS_WriteBatch_NilService(t *testing.T) {
	tr := newExtTransport(t)
	err := tr.TSWriteBatch(context.Background(), nil)
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedTS_WriteBatch_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	err := tr.TSWriteBatch(context.Background(), nil)
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedTS_Query(t *testing.T) {
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	resp, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{
		Metric:     "m",
		StartNs:    1700000000000000000,
		EndNs:      1700001000000000000,
		Tags:       []transport.TSTag{{Key: "host", Value: "a"}},
		Limit:      10,
		Descending: true,
	})
	if err != nil {
		t.Fatalf("TSQuery: %v", err)
	}
	if len(resp.Points) != 1 {
		t.Fatalf("got %d points", len(resp.Points))
	}
}

func TestEmbeddedTS_Query_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedTS_Query_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	_, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedTS_Query_ServiceError(t *testing.T) {
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{
		queryFn: func(_ context.Context, _ string, _, _ time.Time, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
			return nil, errSentinel
		},
	}))
	_, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestEmbeddedTS_Aggregate(t *testing.T) {
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	resp, err := tr.TSAggregate(context.Background(), &transport.TSAggregateRequest{
		Metric:     "m",
		StartNs:    1700000000000000000,
		EndNs:      1700001000000000000,
		Function:   transport.AggAvg,
		IntervalNs: int64(time.Minute),
	})
	if err != nil {
		t.Fatalf("TSAggregate: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("got %d results", len(resp.Results))
	}
}

func TestEmbeddedTS_Aggregate_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.TSAggregate(context.Background(), &transport.TSAggregateRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedTS_Aggregate_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	_, err := tr.TSAggregate(context.Background(), &transport.TSAggregateRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedTS_WritePoint(t *testing.T) {
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	if err := tr.TSWritePoint(context.Background(), &transport.TSDataPoint{Metric: "m", Value: 1.0, TimestampNs: 1700000000000000000}); err != nil {
		t.Fatalf("TSWritePoint: %v", err)
	}
}

func TestEmbeddedTS_WritePoint_NilService(t *testing.T) {
	tr := newExtTransport(t)
	err := tr.TSWritePoint(context.Background(), &transport.TSDataPoint{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedTS_WritePoint_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	err := tr.TSWritePoint(context.Background(), &transport.TSDataPoint{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedTS_RichQuery(t *testing.T) {
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	resp, err := tr.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{
		Metric:      "m",
		StartNs:     1700000000000000000,
		EndNs:       1700001000000000000,
		Aggregation: transport.AggAvg,
		IntervalNs:  int64(time.Minute),
		GroupBy:     []string{"host"},
		Limit:       100,
	})
	if err != nil {
		t.Fatalf("TSRichQuery: %v", err)
	}
	if resp.Metric != "m" {
		t.Fatalf("Metric = %q", resp.Metric)
	}
}

func TestEmbeddedTS_RichQuery_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedTS_RichQuery_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	_, err := tr.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedTS_DeleteSeries(t *testing.T) {
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	resp, err := tr.TSDeleteSeries(context.Background(), &transport.TSDeleteRequest{Metric: "m"})
	if err != nil {
		t.Fatalf("TSDeleteSeries: %v", err)
	}
	if resp == nil {
		t.Fatal("nil resp")
	}
}

func TestEmbeddedTS_DeleteSeries_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.TSDeleteSeries(context.Background(), &transport.TSDeleteRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedTS_DeleteSeries_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	_, err := tr.TSDeleteSeries(context.Background(), &transport.TSDeleteRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedTS_GetRetention(t *testing.T) {
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	resp, err := tr.TSGetRetention(context.Background(), &transport.TSGetRetentionRequest{Metric: "m"})
	if err != nil {
		t.Fatalf("TSGetRetention: %v", err)
	}
	if resp == nil {
		t.Fatal("nil resp")
	}
}

func TestEmbeddedTS_GetRetention_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.TSGetRetention(context.Background(), &transport.TSGetRetentionRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedTS_SetRetention(t *testing.T) {
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	if err := tr.TSSetRetention(context.Background(), &transport.TSSetRetentionRequest{
		Metric: "m",
		Policy: &transport.TSRetentionPolicy{DurationNs: int64(24 * time.Hour)},
	}); err != nil {
		t.Fatalf("TSSetRetention: %v", err)
	}
}

func TestEmbeddedTS_SetRetention_NilService(t *testing.T) {
	tr := newExtTransport(t)
	err := tr.TSSetRetention(context.Background(), &transport.TSSetRetentionRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedTS_ListMetrics(t *testing.T) {
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	resp, err := tr.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{Prefix: "cpu", Limit: 10})
	if err != nil {
		t.Fatalf("TSListMetrics: %v", err)
	}
	if len(resp.Metrics) != 2 {
		t.Fatalf("got %d metrics", len(resp.Metrics))
	}
}

func TestEmbeddedTS_ListMetrics_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

// ===========================================================================
// Mock: BatchServicer (for ImportFile only)
// ===========================================================================

// ===========================================================================
// ImportFile tests via existing BatchServicer interface (use a fully-typed mock).
// ===========================================================================

// fullBatchService satisfies service.BatchServicer with a configurable Import.
type fullBatchService struct {
	importFn func(ctx context.Context, r io.Reader, opts service.ImportOptions) (*service.ImportResult, error)
}

func (b *fullBatchService) Execute(_ context.Context, _ []service.Op) ([]service.OpResponse, error) {
	return nil, errors.New("not implemented")
}

func (b *fullBatchService) ExecuteWithMode(_ context.Context, _ []service.Op, _ writemode.Mode) ([]service.OpResponse, error) {
	return nil, errors.New("not implemented")
}

func (b *fullBatchService) ExecuteEntity(_ context.Context, _ *service.BatchEntityRequest) (*service.BatchEntityResponse, error) {
	return nil, errors.New("not implemented")
}

func (b *fullBatchService) Import(ctx context.Context, r io.Reader, opts service.ImportOptions) (*service.ImportResult, error) {
	if b.importFn != nil {
		return b.importFn(ctx, r, opts)
	}
	return &service.ImportResult{Total: 1, Succeeded: 1}, nil
}

// SetImportKeyGuard satisfies service.ImportKeyGuardSetter as a no-op:
// this double tests ImportFile's wire-level plumbing (the transport's
// mapping between transport.ImportResult and service.ImportResult), never
// the hard-confinement feature, and its inputs below ("k,v\n1,2", "")
// never name the confined key -- so declaring it here (rather than
// leaving NewHardConfinedBatch to refuse Import outright for a
// BatchServicer with no confinement of its own) is a deliberate,
// visible opt-out for this one concern, not an accidental one.
func (b *fullBatchService) SetImportKeyGuard(func(string) error) {}

// ===========================================================================
// ImportFile tests
// ===========================================================================

func TestEmbeddedImportFile_Success(t *testing.T) {
	tr := newExtTransport(t, WithBatchService(&fullBatchService{}))
	result, err := tr.ImportFile(context.Background(), "data.csv", strings.NewReader("k,v\n1,2"), transport.ImportOptions{Format: "csv"})
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("Total = %d", result.Total)
	}
}

func TestEmbeddedImportFile_NilResult(t *testing.T) {
	tr := newExtTransport(t, WithBatchService(&fullBatchService{
		importFn: func(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
			return nil, nil
		},
	}))
	result, err := tr.ImportFile(context.Background(), "data.csv", strings.NewReader(""), transport.ImportOptions{})
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if result == nil {
		t.Fatal("nil result")
	}
}

func TestEmbeddedImportFile_WithErrors(t *testing.T) {
	tr := newExtTransport(t, WithBatchService(&fullBatchService{
		importFn: func(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
			return &service.ImportResult{
				Total:     3,
				Succeeded: 1,
				Failed:    2,
				Errors: []service.ImportError{
					{Row: 2, Key: "k1", Message: "duplicate"},
					{Row: 3, Key: "k2", Message: "invalid"},
				},
			}, nil
		},
	}))
	result, err := tr.ImportFile(context.Background(), "data.csv", strings.NewReader(""), transport.ImportOptions{})
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if result.Failed != 2 || len(result.Errors) != 2 {
		t.Fatalf("unexpected: %+v", result)
	}
	if result.Errors[0].Row != 2 || result.Errors[0].Message != "duplicate" {
		t.Fatalf("error[0] = %+v", result.Errors[0])
	}
}

func TestEmbeddedImportFile_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.ImportFile(context.Background(), "x", strings.NewReader(""), transport.ImportOptions{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedImportFile_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithBatchService(&fullBatchService{}))
	_, err := tr.ImportFile(context.Background(), "x", strings.NewReader(""), transport.ImportOptions{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedImportFile_ServiceError(t *testing.T) {
	tr := newExtTransport(t, WithBatchService(&fullBatchService{
		importFn: func(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
			return nil, errSentinel
		},
	}))
	_, err := tr.ImportFile(context.Background(), "x", strings.NewReader(""), transport.ImportOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// Error type coverage: server.go error types
// ===========================================================================

func TestEmbeddedServerErrors_StorageCreateError(t *testing.T) {
	wrapped := errors.New("underlying")
	e := &StorageCreateError{Backend: "memory", Err: wrapped}
	if !strings.Contains(e.Error(), "memory") {
		t.Fatalf("Error() = %q", e.Error())
	}
	if !errors.Is(e, wrapped) {
		t.Fatal("Unwrap should match underlying")
	}
}

func TestEmbeddedServerErrors_EngineCreateError(t *testing.T) {
	wrapped := errors.New("engine boom")
	e := &EngineCreateError{Engine: "pebble", Err: wrapped}
	if !strings.Contains(e.Error(), "pebble") {
		t.Fatalf("Error() = %q", e.Error())
	}
	if !errors.Is(e, wrapped) {
		t.Fatal("Unwrap")
	}
}

func TestEmbeddedServerErrors_BarrierCreateError(t *testing.T) {
	wrapped := errors.New("barrier boom")
	e := &BarrierCreateError{Err: wrapped}
	if !strings.Contains(e.Error(), "barrier") {
		t.Fatalf("Error() = %q", e.Error())
	}
	if !errors.Is(e, wrapped) {
		t.Fatal("Unwrap")
	}
}

func TestEmbeddedServerErrors_ServerTransportCreateError(t *testing.T) {
	wrapped := errors.New("transport boom")
	e := &ServerTransportCreateError{Err: wrapped}
	if !strings.Contains(e.Error(), "transport") {
		t.Fatalf("Error() = %q", e.Error())
	}
	if !errors.Is(e, wrapped) {
		t.Fatal("Unwrap")
	}
}

func TestEmbeddedServerErrors_ServerClientCreateError(t *testing.T) {
	wrapped := errors.New("client boom")
	e := &ServerClientCreateError{Err: wrapped}
	if !strings.Contains(e.Error(), "client") {
		t.Fatalf("Error() = %q", e.Error())
	}
	if !errors.Is(e, wrapped) {
		t.Fatal("Unwrap")
	}
}

func TestEmbeddedServerErrors_ShutdownError(t *testing.T) {
	wrapped := errors.New("shutdown boom")
	e := &ShutdownError{Err: wrapped}
	if !strings.Contains(e.Error(), "shutdown") {
		t.Fatalf("Error() = %q", e.Error())
	}
	if !errors.Is(e, wrapped) {
		t.Fatal("Unwrap")
	}
}

func TestEmbeddedServerErrors_BarrierSealedError(t *testing.T) {
	// nil err returns sentinel
	e := &BarrierSealedError{}
	if e.Error() == "" {
		t.Fatal("empty error")
	}
	if e.Unwrap() == nil {
		t.Fatal("expected ErrSealed unwrap fallback")
	}
	// with err
	wrapped := errors.New("inner")
	e2 := &BarrierSealedError{Err: wrapped}
	if !strings.Contains(e2.Error(), "inner") {
		t.Fatalf("Error() = %q", e2.Error())
	}
	if !errors.Is(e2, wrapped) {
		t.Fatal("Unwrap")
	}
}

func TestEmbeddedServerErrors_BarrierUnsealError(t *testing.T) {
	wrapped := errors.New("unseal boom")
	e := &BarrierUnsealError{Err: wrapped}
	if !strings.Contains(e.Error(), "unseal") {
		t.Fatalf("Error() = %q", e.Error())
	}
	if !errors.Is(e, wrapped) {
		t.Fatal("Unwrap")
	}
}

func TestEmbeddedServerErrors_SealSentinelError(t *testing.T) {
	wrapped := errors.New("sentinel boom")
	e := &SealSentinelError{Err: wrapped}
	if !strings.Contains(e.Error(), "sentinel") {
		t.Fatalf("Error() = %q", e.Error())
	}
	if !errors.Is(e, wrapped) {
		t.Fatal("Unwrap")
	}
}

func TestEmbeddedServerErrors_ServerConfigError(t *testing.T) {
	e := &ServerConfigError{Field: "DataDir", Message: "is required"}
	if !strings.Contains(e.Error(), "DataDir") {
		t.Fatalf("Error() = %q", e.Error())
	}
}

// ===========================================================================
// Coverage of errors.go alternative paths
// ===========================================================================

func TestEmbeddedNilServiceError_Error(t *testing.T) {
	e := &NilServiceError{Service: "FooServicer"}
	if !strings.Contains(e.Error(), "FooServicer") {
		t.Fatalf("Error() = %q", e.Error())
	}
}

// ===========================================================================
// translateBackupError coverage - exercise each branch
// ===========================================================================

func TestEmbeddedTranslateBackupError(t *testing.T) {
	t.Run("Nil", func(t *testing.T) {
		if translateBackupError(nil) != nil {
			t.Fatal("expected nil")
		}
	})
	t.Run("NotFound", func(t *testing.T) {
		got := translateBackupError(&service.BackupNotFoundError{ID: "b1"})
		var want *transport.BackupNotFoundError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
	})
	t.Run("EmptyDestination", func(t *testing.T) {
		got := translateBackupError(&service.BackupEmptyDestinationError{})
		var want *transport.BackupEmptyDestinationError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
	})
	t.Run("EmptyID", func(t *testing.T) {
		got := translateBackupError(&service.BackupEmptyIDError{})
		var want *transport.BackupEmptyIDError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
	})
	t.Run("CreateError", func(t *testing.T) {
		inner := errors.New("inner")
		got := translateBackupError(&service.BackupCreateError{Err: inner})
		var want *transport.BackupCreateError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
	})
	t.Run("RestoreError", func(t *testing.T) {
		inner := errors.New("inner")
		got := translateBackupError(&service.BackupRestoreError{BackupID: "b1", Err: inner})
		var want *transport.BackupRestoreError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
	})
	t.Run("ChecksumMismatch", func(t *testing.T) {
		got := translateBackupError(&service.BackupChecksumMismatchError{Expected: "a", Actual: "b"})
		var want *transport.BackupChecksumMismatchError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
	})
	t.Run("EncryptionKeyRequired", func(t *testing.T) {
		got := translateBackupError(&service.BackupEncryptionKeyRequiredError{})
		var want *transport.BackupCreateError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
	})
	t.Run("DecryptionFailed", func(t *testing.T) {
		got := translateBackupError(&service.BackupDecryptionFailedError{BackupID: "b1", Err: errors.New("wrong key")})
		var want *transport.BackupDecryptionFailedError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
		if want.BackupID != "b1" {
			t.Fatalf("BackupID = %q, want %q", want.BackupID, "b1")
		}
	})
	t.Run("Passthrough", func(t *testing.T) {
		want := errors.New("other")
		got := translateBackupError(want)
		if !errors.Is(got, want) {
			t.Fatalf("expected passthrough, got %v", got)
		}
	})
}
