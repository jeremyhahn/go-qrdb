// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build !integration

package rest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Minimal credential service implementations used only in this test file.
// ---------------------------------------------------------------------------

// erroringCredentialService returns errors for Store and Retrieve.
type erroringCredentialService struct{}

var _ service.CredentialServicer = (*erroringCredentialService)(nil)

func (e *erroringCredentialService) CredentialStore(_ context.Context, _ string, _ []byte) error {
	return errors.New("storage error")
}
func (e *erroringCredentialService) CredentialRetrieve(_ context.Context, _ string) ([]byte, error) {
	return nil, errors.New("retrieve error")
}
func (e *erroringCredentialService) CredentialSubmit(_ context.Context, _ string, _ []byte) error {
	return nil
}
func (e *erroringCredentialService) CredentialDelete(_ context.Context, _ string) error { return nil }
func (e *erroringCredentialService) CredentialStrategy() string                         { return "test" }
func (e *erroringCredentialService) CredentialList(_ context.Context) ([]string, error) {
	return nil, nil
}
func (e *erroringCredentialService) CredentialRotate(_ context.Context, _ string, _ credentials.RotateOptions) (*credentials.RotateResult, error) {
	return nil, nil
}
func (e *erroringCredentialService) CredentialRetrieveVersion(_ context.Context, _ string, _ uint64) ([]byte, error) {
	return nil, nil
}
func (e *erroringCredentialService) CredentialListVersions(_ context.Context, _ string) ([]credentials.CredentialVersion, error) {
	return nil, nil
}
func (e *erroringCredentialService) CredentialGCExpired(_ context.Context, _ string) (int, error) {
	return 0, nil
}

// returnsValueCredentialService returns a configured value from Retrieve.
type returnsValueCredentialService struct {
	value []byte
}

var _ service.CredentialServicer = (*returnsValueCredentialService)(nil)

func (r *returnsValueCredentialService) CredentialStore(_ context.Context, _ string, _ []byte) error {
	return nil
}
func (r *returnsValueCredentialService) CredentialRetrieve(_ context.Context, _ string) ([]byte, error) {
	return r.value, nil
}
func (r *returnsValueCredentialService) CredentialSubmit(_ context.Context, _ string, _ []byte) error {
	return nil
}
func (r *returnsValueCredentialService) CredentialDelete(_ context.Context, _ string) error {
	return nil
}
func (r *returnsValueCredentialService) CredentialStrategy() string { return "test" }
func (r *returnsValueCredentialService) CredentialList(_ context.Context) ([]string, error) {
	return nil, nil
}
func (r *returnsValueCredentialService) CredentialRotate(_ context.Context, _ string, _ credentials.RotateOptions) (*credentials.RotateResult, error) {
	return nil, nil
}
func (r *returnsValueCredentialService) CredentialRetrieveVersion(_ context.Context, _ string, _ uint64) ([]byte, error) {
	return nil, nil
}
func (r *returnsValueCredentialService) CredentialListVersions(_ context.Context, _ string) ([]credentials.CredentialVersion, error) {
	return nil, nil
}
func (r *returnsValueCredentialService) CredentialGCExpired(_ context.Context, _ string) (int, error) {
	return 0, nil
}

// ---------------------------------------------------------------------------
// Extended mockDatabaseService with controllable seal/unseal/barrier funcs
// ---------------------------------------------------------------------------

type mockDatabaseServiceWithBarrier struct {
	mockDatabaseService
	sealFn          func(ctx context.Context, name string) error
	unsealFn        func(ctx context.Context, name string, creds seal.Credentials) error
	barrierStatusFn func(name string) (*seal.BarrierStatus, error)
}

func newMockDBWithBarrier() *mockDatabaseServiceWithBarrier {
	return &mockDatabaseServiceWithBarrier{
		mockDatabaseService: mockDatabaseService{},
		sealFn:              func(_ context.Context, _ string) error { return nil },
		unsealFn:            func(_ context.Context, _ string, _ seal.Credentials) error { return nil },
		barrierStatusFn: func(_ string) (*seal.BarrierStatus, error) {
			return &seal.BarrierStatus{
				Sealed:         false,
				Strategy:       "software",
				HardwareBacked: false,
				InitializedAt:  time.Now(),
			}, nil
		},
	}
}

func (m *mockDatabaseServiceWithBarrier) SealDatabase(ctx context.Context, name string) error {
	return m.sealFn(ctx, name)
}

func (m *mockDatabaseServiceWithBarrier) UnsealDatabase(ctx context.Context, name string, creds seal.Credentials) error {
	return m.unsealFn(ctx, name, creds)
}

func (m *mockDatabaseServiceWithBarrier) DatabaseBarrierStatus(_ context.Context, name string) (*seal.BarrierStatus, error) {
	return m.barrierStatusFn(name)
}

// ---------------------------------------------------------------------------
// handleSealDatabase
// ---------------------------------------------------------------------------

func TestHandleSealDatabase_Success(t *testing.T) {
	dbSvc := newMockDBWithBarrier()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithDatabaseService(dbSvc))

	req := httptest.NewRequest(http.MethodPost, "/v1/databases/mydb/seal", nil)
	rec := httptest.NewRecorder()
	srv.handleSealDatabase(rec, req, "mydb")
	assertStatus(t, rec, http.StatusNoContent)
}

func TestHandleSealDatabase_ServiceError(t *testing.T) {
	dbSvc := newMockDBWithBarrier()
	dbSvc.sealFn = func(_ context.Context, _ string) error {
		return errors.New("seal operation failed")
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithDatabaseService(dbSvc))

	req := httptest.NewRequest(http.MethodPost, "/v1/databases/mydb/seal", nil)
	rec := httptest.NewRecorder()
	srv.handleSealDatabase(rec, req, "mydb")
	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// handleUnsealDatabase
// ---------------------------------------------------------------------------

func TestHandleUnsealDatabase_Success(t *testing.T) {
	dbSvc := newMockDBWithBarrier()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithDatabaseService(dbSvc))

	body := `{"secret":"mysecret"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/databases/mydb/unseal", strings.NewReader(body))
	req.ContentLength = int64(len(body))
	rec := httptest.NewRecorder()
	srv.handleUnsealDatabase(rec, req, "mydb")
	assertStatus(t, rec, http.StatusNoContent)
}

func TestHandleUnsealDatabase_EmptyBody(t *testing.T) {
	dbSvc := newMockDBWithBarrier()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithDatabaseService(dbSvc))

	req := httptest.NewRequest(http.MethodPost, "/v1/databases/mydb/unseal", nil)
	req.ContentLength = 0
	rec := httptest.NewRecorder()
	srv.handleUnsealDatabase(rec, req, "mydb")
	assertStatus(t, rec, http.StatusNoContent)
}

func TestHandleUnsealDatabase_BadBody(t *testing.T) {
	dbSvc := newMockDBWithBarrier()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithDatabaseService(dbSvc))

	body := `not-json`
	req := httptest.NewRequest(http.MethodPost, "/v1/databases/mydb/unseal", strings.NewReader(body))
	req.ContentLength = int64(len(body))
	rec := httptest.NewRecorder()
	srv.handleUnsealDatabase(rec, req, "mydb")
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleUnsealDatabase_ServiceError(t *testing.T) {
	dbSvc := newMockDBWithBarrier()
	dbSvc.unsealFn = func(_ context.Context, _ string, _ seal.Credentials) error {
		return errors.New("wrong secret")
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithDatabaseService(dbSvc))

	req := httptest.NewRequest(http.MethodPost, "/v1/databases/mydb/unseal", strings.NewReader(`{"secret":"bad"}`))
	rec := httptest.NewRecorder()
	srv.handleUnsealDatabase(rec, req, "mydb")
	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// handleDatabaseBarrierStatus
// ---------------------------------------------------------------------------

func TestHandleDatabaseBarrierStatus_Success(t *testing.T) {
	dbSvc := newMockDBWithBarrier()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithDatabaseService(dbSvc))

	req := httptest.NewRequest(http.MethodGet, "/v1/databases/mydb/barrier/status", nil)
	rec := httptest.NewRecorder()
	srv.handleDatabaseBarrierStatus(rec, req, "mydb")
	assertStatus(t, rec, http.StatusOK)

	var resp databaseBarrierStatusResponse
	decodeJSON(t, rec, &resp)
	if resp.Sealed {
		t.Error("expected sealed=false")
	}
	if resp.Strategy != "software" {
		t.Errorf("strategy = %q, want software", resp.Strategy)
	}
}

func TestHandleDatabaseBarrierStatus_ZeroInitTime(t *testing.T) {
	dbSvc := newMockDBWithBarrier()
	dbSvc.barrierStatusFn = func(_ string) (*seal.BarrierStatus, error) {
		return &seal.BarrierStatus{
			Sealed:   true,
			Strategy: "none",
			// InitializedAt is zero — should be omitted.
		}, nil
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithDatabaseService(dbSvc))

	req := httptest.NewRequest(http.MethodGet, "/v1/databases/mydb/barrier/status", nil)
	rec := httptest.NewRecorder()
	srv.handleDatabaseBarrierStatus(rec, req, "mydb")
	assertStatus(t, rec, http.StatusOK)

	var resp databaseBarrierStatusResponse
	decodeJSON(t, rec, &resp)
	if resp.InitializedAt != "" {
		t.Errorf("initialized_at should be empty for zero time, got %q", resp.InitializedAt)
	}
}

func TestHandleDatabaseBarrierStatus_ServiceError(t *testing.T) {
	dbSvc := newMockDBWithBarrier()
	dbSvc.barrierStatusFn = func(_ string) (*seal.BarrierStatus, error) {
		return nil, errors.New("database not found")
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithDatabaseService(dbSvc))

	req := httptest.NewRequest(http.MethodGet, "/v1/databases/mydb/barrier/status", nil)
	rec := httptest.NewRecorder()
	srv.handleDatabaseBarrierStatus(rec, req, "mydb")
	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// Via dispatch: /v1/databases/{name}/seal, /unseal, /barrier/status
// ---------------------------------------------------------------------------

func TestDispatch_DatabaseSeal_Success(t *testing.T) {
	dbSvc := newMockDBWithBarrier()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithDatabaseService(dbSvc))

	req := httptest.NewRequest(http.MethodPost, "/v1/databases/mydb/seal", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNoContent)
}

func TestDispatch_DatabaseUnseal_Success(t *testing.T) {
	dbSvc := newMockDBWithBarrier()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithDatabaseService(dbSvc))

	req := httptest.NewRequest(http.MethodPost, "/v1/databases/mydb/unseal", strings.NewReader(`{"secret":"s"}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNoContent)
}

func TestDispatch_DatabaseBarrierStatus_Success(t *testing.T) {
	dbSvc := newMockDBWithBarrier()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithDatabaseService(dbSvc))

	req := httptest.NewRequest(http.MethodGet, "/v1/databases/mydb/barrier/status", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusOK)
}

// ---------------------------------------------------------------------------
// Stub custodian service
// ---------------------------------------------------------------------------

type stubCustodianSvc struct {
	registerFn func(ctx context.Context, req custodian.RegisterRequest) (*custodian.RegisterResponse, error)
	getFn      func(ctx context.Context, id string) (*custodian.Custodian, error)
	listFn     func(ctx context.Context, tenantID string) ([]*custodian.Custodian, error)
	revokeFn   func(ctx context.Context, id string) error
}

var _ custodianServicer = (*stubCustodianSvc)(nil)

func newStubCustodianSvc() *stubCustodianSvc {
	return &stubCustodianSvc{
		registerFn: func(_ context.Context, req custodian.RegisterRequest) (*custodian.RegisterResponse, error) {
			return &custodian.RegisterResponse{
				Custodian:      &custodian.Custodian{ID: "cust-1", Username: req.Username},
				SealedEnvelope: nil,
			}, nil
		},
		getFn: func(_ context.Context, _ string) (*custodian.Custodian, error) {
			return &custodian.Custodian{ID: "c1"}, nil
		},
		listFn:   func(_ context.Context, _ string) ([]*custodian.Custodian, error) { return nil, nil },
		revokeFn: func(_ context.Context, _ string) error { return nil },
	}
}

func (s *stubCustodianSvc) RegisterCustodian(ctx context.Context, req custodian.RegisterRequest) (*custodian.RegisterResponse, error) {
	return s.registerFn(ctx, req)
}
func (s *stubCustodianSvc) GetCustodian(ctx context.Context, id string) (*custodian.Custodian, error) {
	return s.getFn(ctx, id)
}
func (s *stubCustodianSvc) ListCustodians(ctx context.Context, tenantID string) ([]*custodian.Custodian, error) {
	return s.listFn(ctx, tenantID)
}
func (s *stubCustodianSvc) RevokeCustodian(ctx context.Context, id string) error {
	return s.revokeFn(ctx, id)
}

// ---------------------------------------------------------------------------
// handleEnrollCustodian
// ---------------------------------------------------------------------------

func TestHandleEnrollCustodian_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/enroll-custodian",
		strings.NewReader(`{"username":"u","tenantID":"t","enrollmentID":"e","publicKeyPEM":"pk"}`))
	rec := httptest.NewRecorder()
	srv.handleEnrollCustodian(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleEnrollCustodian_BadBody(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithCustodianService(newStubCustodianSvc()))
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/enroll-custodian",
		strings.NewReader(`not-json`))
	rec := httptest.NewRecorder()
	srv.handleEnrollCustodian(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleEnrollCustodian_MissingFields(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithCustodianService(newStubCustodianSvc()))
	// Missing publicKeyPEM.
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/enroll-custodian",
		strings.NewReader(`{"username":"u","tenantID":"t","enrollmentID":"e"}`))
	rec := httptest.NewRecorder()
	srv.handleEnrollCustodian(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleEnrollCustodian_Success(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithCustodianService(newStubCustodianSvc()))
	body := `{"username":"alice","tenantID":"tenant-1","enrollmentID":"enroll-1","publicKeyPEM":"-----BEGIN PUBLIC KEY-----"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/enroll-custodian",
		strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleEnrollCustodian(rec, req)
	assertStatus(t, rec, http.StatusOK)
}

func TestHandleEnrollCustodian_ServiceError(t *testing.T) {
	svc := newStubCustodianSvc()
	svc.registerFn = func(_ context.Context, _ custodian.RegisterRequest) (*custodian.RegisterResponse, error) {
		return nil, errors.New("enrollment failed")
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithCustodianService(svc))
	body := `{"username":"u","tenantID":"t","enrollmentID":"e","publicKeyPEM":"pk"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/enroll-custodian",
		strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleEnrollCustodian(rec, req)
	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// handleRevokeCustodian
// ---------------------------------------------------------------------------

func TestHandleRevokeCustodian_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/revoke-custodian",
		strings.NewReader(`{"custodianID":"cust-1"}`))
	rec := httptest.NewRecorder()
	srv.handleRevokeCustodian(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleRevokeCustodian_BadBody(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithCustodianService(newStubCustodianSvc()))
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/revoke-custodian",
		strings.NewReader(`bad`))
	rec := httptest.NewRecorder()
	srv.handleRevokeCustodian(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleRevokeCustodian_MissingID(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithCustodianService(newStubCustodianSvc()))
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/revoke-custodian",
		strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	srv.handleRevokeCustodian(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleRevokeCustodian_Success(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithCustodianService(newStubCustodianSvc()))
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/revoke-custodian",
		strings.NewReader(`{"custodianID":"cust-1"}`))
	rec := httptest.NewRecorder()
	srv.handleRevokeCustodian(rec, req)
	assertStatus(t, rec, http.StatusOK)
}

func TestHandleRevokeCustodian_ServiceError(t *testing.T) {
	svc := newStubCustodianSvc()
	svc.revokeFn = func(_ context.Context, _ string) error { return errors.New("not found") }
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithCustodianService(svc))
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/revoke-custodian",
		strings.NewReader(`{"custodianID":"cust-1"}`))
	rec := httptest.NewRecorder()
	srv.handleRevokeCustodian(rec, req)
	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// handleCustodianClaimShareBegin
// ---------------------------------------------------------------------------

func TestHandleCustodianClaimShareBegin_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/claim-share/begin",
		strings.NewReader(`{"custodianID":"c1"}`))
	rec := httptest.NewRecorder()
	srv.handleCustodianClaimShareBegin(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleCustodianClaimShareBegin_BadBody(t *testing.T) {
	coverageSvc := newCoverageCeremonyService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithCeremonyService(coverageSvc))
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/claim-share/begin",
		strings.NewReader(`bad`))
	rec := httptest.NewRecorder()
	srv.handleCustodianClaimShareBegin(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCustodianClaimShareBegin_MissingID(t *testing.T) {
	coverageSvc := newCoverageCeremonyService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithCeremonyService(coverageSvc))
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/claim-share/begin",
		strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	srv.handleCustodianClaimShareBegin(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCustodianClaimShareBegin_Success(t *testing.T) {
	// coverageCeremonyService.CeremonyClaimShareBegin returns (nil, nil) —
	// writeJSON(w, nil) still writes 200 with a null body.
	coverageSvc := newCoverageCeremonyService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithCeremonyService(coverageSvc))
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/claim-share/begin",
		strings.NewReader(`{"custodianID":"c1"}`))
	rec := httptest.NewRecorder()
	srv.handleCustodianClaimShareBegin(rec, req)
	assertStatus(t, rec, http.StatusOK)
}

// ---------------------------------------------------------------------------
// handleCustodianClaimShareComplete
// ---------------------------------------------------------------------------

func TestHandleCustodianClaimShareComplete_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/claim-share/complete",
		strings.NewReader(`{"custodianID":"c1","challengeID":"ch1"}`))
	rec := httptest.NewRecorder()
	srv.handleCustodianClaimShareComplete(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleCustodianClaimShareComplete_BadBody(t *testing.T) {
	coverageSvc := newCoverageCeremonyService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithCeremonyService(coverageSvc))
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/claim-share/complete",
		strings.NewReader(`bad`))
	rec := httptest.NewRecorder()
	srv.handleCustodianClaimShareComplete(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCustodianClaimShareComplete_MissingFields(t *testing.T) {
	coverageSvc := newCoverageCeremonyService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithCeremonyService(coverageSvc))
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/claim-share/complete",
		strings.NewReader(`{"custodianID":"c1"}`))
	rec := httptest.NewRecorder()
	srv.handleCustodianClaimShareComplete(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCustodianClaimShareComplete_Success(t *testing.T) {
	// coverageCeremonyService.CeremonyClaimShareComplete returns (nil, nil) —
	// writeJSON(w, nil) still writes 200 with a null body.
	coverageSvc := newCoverageCeremonyService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithCeremonyService(coverageSvc))
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/claim-share/complete",
		strings.NewReader(`{"custodianID":"c1","challengeID":"ch1"}`))
	rec := httptest.NewRecorder()
	srv.handleCustodianClaimShareComplete(rec, req)
	assertStatus(t, rec, http.StatusOK)
}

// ---------------------------------------------------------------------------
// handleUnsealShare
// ---------------------------------------------------------------------------

func TestHandleUnsealShare_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/unseal-share",
		strings.NewReader(`{"custodianID":"c1","shareBytes":"AAEC"}`))
	rec := httptest.NewRecorder()
	srv.handleUnsealShare(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleUnsealShare_BadBody(t *testing.T) {
	coverageSvc := newCoverageCeremonyService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithCeremonyService(coverageSvc))
	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/unseal-share",
		strings.NewReader(`bad`))
	rec := httptest.NewRecorder()
	srv.handleUnsealShare(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleUnsealShare_MissingFields(t *testing.T) {
	coverageSvc := newCoverageCeremonyService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithCeremonyService(coverageSvc))
	// Missing shareBytes.
	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/unseal-share",
		strings.NewReader(`{"custodianID":"c1"}`))
	rec := httptest.NewRecorder()
	srv.handleUnsealShare(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleUnsealShare_Success(t *testing.T) {
	// coverageCeremonyService.CeremonySubmitUnsealShare returns (nil, nil) —
	// writeJSON(w, nil) still writes 200 with a null body.
	coverageSvc := newCoverageCeremonyService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithCeremonyService(coverageSvc))
	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/unseal-share",
		strings.NewReader(`{"custodianID":"c1","shareBytes":"AAEC"}`))
	rec := httptest.NewRecorder()
	srv.handleUnsealShare(rec, req)
	assertStatus(t, rec, http.StatusOK)
}

// ---------------------------------------------------------------------------
// handleCredentialStore / handleCredentialRetrieve
// ---------------------------------------------------------------------------

func TestHandleCredentialStore_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodPost, "/v1/credentials/store",
		strings.NewReader(`{"name":"mykey","value":"czNjcjN0"}`))
	rec := httptest.NewRecorder()
	srv.handleCredentialStore(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleCredentialStore_BadBody(t *testing.T) {
	svc := newCoverageCredentialService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodPost, "/v1/credentials/store",
		strings.NewReader(`bad`))
	rec := httptest.NewRecorder()
	srv.handleCredentialStore(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCredentialStore_Success(t *testing.T) {
	svc := newCoverageCredentialService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodPost, "/v1/credentials/store",
		strings.NewReader(`{"name":"mykey","value":"czNjcjN0"}`))
	rec := httptest.NewRecorder()
	srv.handleCredentialStore(rec, req)
	assertStatus(t, rec, http.StatusNoContent)
}

func TestHandleCredentialStore_ServiceError(t *testing.T) {
	svc := &erroringCredentialService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodPost, "/v1/credentials/store",
		strings.NewReader(`{"name":"k","value":"dg=="}`))
	rec := httptest.NewRecorder()
	srv.handleCredentialStore(rec, req)
	assertStatus(t, rec, http.StatusInternalServerError)
}

func TestHandleCredentialRetrieve_Success(t *testing.T) {
	svc := &returnsValueCredentialService{value: []byte("my-value")}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodGet, "/v1/credentials/mykey/value", nil)
	rec := httptest.NewRecorder()
	srv.handleCredentialRetrieve(rec, req, "mykey")
	assertStatus(t, rec, http.StatusOK)

	var resp credentialValueResponse
	decodeJSON(t, rec, &resp)
	if string(resp.Value) != "my-value" {
		t.Errorf("value = %q, want my-value", resp.Value)
	}
}

func TestHandleCredentialRetrieve_ServiceError(t *testing.T) {
	svc := &erroringCredentialService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodGet, "/v1/credentials/mykey/value", nil)
	rec := httptest.NewRecorder()
	srv.handleCredentialRetrieve(rec, req, "mykey")
	assertStatus(t, rec, http.StatusInternalServerError)
}
