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

package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Mock AuthFactorServicer for handler branch coverage
// ---------------------------------------------------------------------------

type stubAuthFactorSvc struct {
	enrollment   *authfactors.Enrollment
	challenge    *authfactors.Challenge
	enrollments  []*authfactors.Enrollment
	enrollErr    error
	challengeErr error
	verifyErr    error
	listErr      error
	revokeErr    error
}

var _ service.AuthFactorServicer = (*stubAuthFactorSvc)(nil)

func (s *stubAuthFactorSvc) EnrollFactor(_ context.Context, _ string, _ string, _ authfactors.EnrollParams) (*authfactors.Enrollment, error) {
	return s.enrollment, s.enrollErr
}

func (s *stubAuthFactorSvc) ChallengeFactor(_ context.Context, _ string) (*authfactors.Challenge, error) {
	return s.challenge, s.challengeErr
}

func (s *stubAuthFactorSvc) VerifyFactor(_ context.Context, _ string, _ string, _ *authfactors.Response) error {
	return s.verifyErr
}

func (s *stubAuthFactorSvc) ListFactors(_ context.Context, _ string) ([]*authfactors.Enrollment, error) {
	return s.enrollments, s.listErr
}

func (s *stubAuthFactorSvc) RevokeFactor(_ context.Context, _ string) error {
	return s.revokeErr
}

// ---------------------------------------------------------------------------
// Mock audit logger for handleAuditQuery tests
// ---------------------------------------------------------------------------

type stubAuditLogger struct {
	queryErr error
	page     *audit.Page
}

func (s *stubAuditLogger) Emit(_ context.Context, _ audit.AuditEvent) error { return nil }

func (s *stubAuditLogger) Query(_ context.Context, _ audit.Filter) (*audit.Page, error) {
	if s.queryErr != nil {
		return nil, s.queryErr
	}
	if s.page != nil {
		return s.page, nil
	}
	return &audit.Page{Events: []audit.AuditEvent{}, Total: 0}, nil
}

func (s *stubAuditLogger) Close() error { return nil }

// ---------------------------------------------------------------------------
// mapAuthFactorError
// ---------------------------------------------------------------------------

func TestMapAuthFactorError_NilReturnsOK(t *testing.T) {
	if code := mapAuthFactorError(nil); code != http.StatusOK {
		t.Errorf("mapAuthFactorError(nil) = %d, want %d", code, http.StatusOK)
	}
}

func TestMapAuthFactorError_EnrollmentNotFound(t *testing.T) {
	err := &authfactors.EnrollmentNotFoundError{EnrollmentID: "e1"}
	if code := mapAuthFactorError(err); code != http.StatusNotFound {
		t.Errorf("EnrollmentNotFoundError = %d, want 404", code)
	}
}

func TestMapAuthFactorError_EnrollmentRevoked(t *testing.T) {
	err := &authfactors.EnrollmentRevokedError{EnrollmentID: "e1"}
	if code := mapAuthFactorError(err); code != http.StatusGone {
		t.Errorf("EnrollmentRevokedError = %d, want 410", code)
	}
}

func TestMapAuthFactorError_EnrollmentInvalid(t *testing.T) {
	err := &authfactors.EnrollmentInvalidError{Reason: "bad"}
	if code := mapAuthFactorError(err); code != http.StatusBadRequest {
		t.Errorf("EnrollmentInvalidError = %d, want 400", code)
	}
}

func TestMapAuthFactorError_ChallengeExpired(t *testing.T) {
	err := &authfactors.ChallengeExpiredError{ChallengeID: "c1"}
	if code := mapAuthFactorError(err); code != http.StatusGone {
		t.Errorf("ChallengeExpiredError = %d, want 410", code)
	}
}

func TestMapAuthFactorError_VerificationFailed(t *testing.T) {
	err := &authfactors.VerificationFailedError{Reason: "mismatch"}
	if code := mapAuthFactorError(err); code != http.StatusUnauthorized {
		t.Errorf("VerificationFailedError = %d, want 401", code)
	}
}

func TestMapAuthFactorError_FactorNotFound(t *testing.T) {
	err := &authfactors.FactorNotFoundError{Name: "totp"}
	if code := mapAuthFactorError(err); code != http.StatusNotFound {
		t.Errorf("FactorNotFoundError = %d, want 404", code)
	}
}

func TestMapAuthFactorError_HardwareUnavailable(t *testing.T) {
	err := &authfactors.HardwareUnavailableError{Factor: "yubikey", Reason: "no reader"}
	if code := mapAuthFactorError(err); code != http.StatusServiceUnavailable {
		t.Errorf("HardwareUnavailableError = %d, want 503", code)
	}
}

func TestMapAuthFactorError_ErrFactorDisabled(t *testing.T) {
	if code := mapAuthFactorError(authfactors.ErrFactorDisabled); code != http.StatusNotImplemented {
		t.Errorf("ErrFactorDisabled = %d, want 501", code)
	}
}

func TestMapAuthFactorError_ErrHardwareUnavailable(t *testing.T) {
	if code := mapAuthFactorError(authfactors.ErrHardwareUnavailable); code != http.StatusServiceUnavailable {
		t.Errorf("ErrHardwareUnavailable = %d, want 503", code)
	}
}

// ---------------------------------------------------------------------------
// extractAuthFactorName
// ---------------------------------------------------------------------------

func TestExtractAuthFactorName_EnrollSuffix(t *testing.T) {
	name := extractAuthFactorName("/v1/authfactors/fido2/enroll", "/enroll")
	if name != "fido2" {
		t.Errorf("expected fido2, got %q", name)
	}
}

func TestExtractAuthFactorName_ChallengeSuffix(t *testing.T) {
	name := extractAuthFactorName("/v1/authfactors/enrollment-abc/challenge", "/challenge")
	if name != "enrollment-abc" {
		t.Errorf("expected enrollment-abc, got %q", name)
	}
}

func TestExtractAuthFactorName_VerifySuffix(t *testing.T) {
	name := extractAuthFactorName("/v1/authfactors/enrollment-xyz/verify", "/verify")
	if name != "enrollment-xyz" {
		t.Errorf("expected enrollment-xyz, got %q", name)
	}
}

func TestExtractAuthFactorName_MissingSuffix(t *testing.T) {
	name := extractAuthFactorName("/v1/authfactors/fido2/other", "/enroll")
	if name != "" {
		t.Errorf("expected empty string, got %q", name)
	}
}

func TestExtractAuthFactorName_WrongPrefix(t *testing.T) {
	name := extractAuthFactorName("/v2/authfactors/fido2/enroll", "/enroll")
	// /v2 prefix is not stripped, so the result still has it embedded
	// but the suffix IS stripped, so it returns a non-empty (but wrong) string.
	// This just verifies it does not panic.
	_ = name
}

// ---------------------------------------------------------------------------
// enrollmentToResponse
// ---------------------------------------------------------------------------

func TestEnrollmentToResponse_Fields(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	e := &authfactors.Enrollment{
		EnrollmentID: "enroll-1",
		FactorName:   "fido2",
		UserID:       "user-1",
		TenantID:     "tenant-1",
		Metadata:     map[string]string{"k": "v"},
		EnrolledAt:   now,
	}
	resp := enrollmentToResponse(e)

	if resp.EnrollmentID != "enroll-1" {
		t.Errorf("EnrollmentID = %q, want enroll-1", resp.EnrollmentID)
	}
	if resp.FactorName != "fido2" {
		t.Errorf("FactorName = %q, want fido2", resp.FactorName)
	}
	if resp.UserID != "user-1" {
		t.Errorf("UserID = %q, want user-1", resp.UserID)
	}
	if resp.TenantID != "tenant-1" {
		t.Errorf("TenantID = %q, want tenant-1", resp.TenantID)
	}
	if resp.Metadata["k"] != "v" {
		t.Errorf("Metadata[k] = %q, want v", resp.Metadata["k"])
	}
	if resp.EnrolledAt == "" {
		t.Error("expected non-empty EnrolledAt")
	}
}

// ---------------------------------------------------------------------------
// enrollmentToEntry
// ---------------------------------------------------------------------------

func TestEnrollmentToEntry_ActiveEnrollment(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	e := &authfactors.Enrollment{
		EnrollmentID: "entry-1",
		FactorName:   "piv",
		UserID:       "user-2",
		EnrolledAt:   now,
	}
	entry := enrollmentToEntry(e)

	if entry.EnrollmentID != "entry-1" {
		t.Errorf("EnrollmentID = %q, want entry-1", entry.EnrollmentID)
	}
	if entry.FactorName != "piv" {
		t.Errorf("FactorName = %q, want piv", entry.FactorName)
	}
	if entry.RevokedAt != "" {
		t.Errorf("RevokedAt should be empty for active enrollment, got %q", entry.RevokedAt)
	}
}

func TestEnrollmentToEntry_RevokedEnrollment(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	revoked := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	e := &authfactors.Enrollment{
		EnrollmentID: "entry-2",
		FactorName:   "fido2",
		UserID:       "user-3",
		EnrolledAt:   now,
		RevokedAt:    revoked,
	}
	entry := enrollmentToEntry(e)

	if entry.RevokedAt == "" {
		t.Error("expected non-empty RevokedAt for revoked enrollment")
	}
}

// ---------------------------------------------------------------------------
// handleAuthFactorEnroll — with service: missing factor name, missing body,
// missing user_id, and success path
// ---------------------------------------------------------------------------

func TestHandleAuthFactorEnroll_MissingFactorName(t *testing.T) {
	svc := &stubAuthFactorSvc{}
	srv := NewServer(nil, nil, "127.0.0.1:0", WithAuthFactorService(svc))

	// Path without a factor segment before /enroll.
	req := httptest.NewRequest(http.MethodPost, "/v1/authfactors//enroll", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	srv.handleAuthFactorEnroll(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty factor name = %d, want 400", rec.Code)
	}
}

func TestHandleAuthFactorEnroll_InvalidBody(t *testing.T) {
	svc := &stubAuthFactorSvc{}
	srv := NewServer(nil, nil, "127.0.0.1:0", WithAuthFactorService(svc))

	req := httptest.NewRequest(http.MethodPost, "/v1/authfactors/fido2/enroll", bytes.NewReader([]byte(`not-json`)))
	rec := httptest.NewRecorder()
	srv.handleAuthFactorEnroll(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad body = %d, want 400", rec.Code)
	}
}

func TestHandleAuthFactorEnroll_MissingUserID(t *testing.T) {
	svc := &stubAuthFactorSvc{}
	srv := NewServer(nil, nil, "127.0.0.1:0", WithAuthFactorService(svc))

	body := `{"tenant_id":"t1"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/authfactors/fido2/enroll", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	srv.handleAuthFactorEnroll(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing user_id = %d, want 400", rec.Code)
	}
}

func TestHandleAuthFactorEnroll_Success(t *testing.T) {
	now := time.Now().UTC()
	svc := &stubAuthFactorSvc{enrollment: &authfactors.Enrollment{
		EnrollmentID: "enroll-1",
		FactorName:   "fido2",
		UserID:       "user-1",
		EnrolledAt:   now,
	}}
	srv := NewServer(nil, nil, "127.0.0.1:0", WithAuthFactorService(svc))

	body := `{"user_id":"user-1","tenant_id":"t1"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/authfactors/fido2/enroll", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	srv.handleAuthFactorEnroll(rec, req)
	if rec.Code != http.StatusCreated {
		t.Errorf("success = %d, want 201", rec.Code)
	}
}

func TestHandleAuthFactorEnroll_ServiceError(t *testing.T) {
	svc := &stubAuthFactorSvc{enrollErr: &authfactors.FactorNotFoundError{Name: "totp"}}
	srv := NewServer(nil, nil, "127.0.0.1:0", WithAuthFactorService(svc))

	body := `{"user_id":"user-1"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/authfactors/fido2/enroll", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	srv.handleAuthFactorEnroll(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("factor not found = %d, want 404", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// handleAuthFactorChallenge — with service branches
// ---------------------------------------------------------------------------

func TestHandleAuthFactorChallenge_MissingEnrollmentID(t *testing.T) {
	svc := &stubAuthFactorSvc{}
	srv := NewServer(nil, nil, "127.0.0.1:0", WithAuthFactorService(svc))

	req := httptest.NewRequest(http.MethodPost, "/v1/authfactors//challenge", nil)
	rec := httptest.NewRecorder()
	srv.handleAuthFactorChallenge(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty enrollment_id = %d, want 400", rec.Code)
	}
}

func TestHandleAuthFactorChallenge_Success(t *testing.T) {
	svc := &stubAuthFactorSvc{challenge: &authfactors.Challenge{
		ChallengeID:  "chall-1",
		EnrollmentID: "enroll-1",
		Nonce:        []byte("nonce"),
		ExpiresAt:    time.Now().Add(5 * time.Minute).UTC(),
	}}
	srv := NewServer(nil, nil, "127.0.0.1:0", WithAuthFactorService(svc))

	req := httptest.NewRequest(http.MethodPost, "/v1/authfactors/enroll-1/challenge", nil)
	rec := httptest.NewRecorder()
	srv.handleAuthFactorChallenge(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("success = %d, want 200", rec.Code)
	}
}

func TestHandleAuthFactorChallenge_ServiceError(t *testing.T) {
	svc := &stubAuthFactorSvc{challengeErr: &authfactors.EnrollmentNotFoundError{EnrollmentID: "e1"}}
	srv := NewServer(nil, nil, "127.0.0.1:0", WithAuthFactorService(svc))

	req := httptest.NewRequest(http.MethodPost, "/v1/authfactors/e1/challenge", nil)
	rec := httptest.NewRecorder()
	srv.handleAuthFactorChallenge(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("not found = %d, want 404", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// handleAuthFactorVerify — with service branches
// ---------------------------------------------------------------------------

func TestHandleAuthFactorVerify_MissingEnrollmentID(t *testing.T) {
	svc := &stubAuthFactorSvc{}
	srv := NewServer(nil, nil, "127.0.0.1:0", WithAuthFactorService(svc))

	req := httptest.NewRequest(http.MethodPost, "/v1/authfactors//verify", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	srv.handleAuthFactorVerify(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty enrollment_id = %d, want 400", rec.Code)
	}
}

func TestHandleAuthFactorVerify_InvalidBody(t *testing.T) {
	svc := &stubAuthFactorSvc{}
	srv := NewServer(nil, nil, "127.0.0.1:0", WithAuthFactorService(svc))

	req := httptest.NewRequest(http.MethodPost, "/v1/authfactors/e1/verify", bytes.NewReader([]byte(`not-json`)))
	rec := httptest.NewRecorder()
	srv.handleAuthFactorVerify(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad body = %d, want 400", rec.Code)
	}
}

func TestHandleAuthFactorVerify_MissingChallengeID(t *testing.T) {
	svc := &stubAuthFactorSvc{}
	srv := NewServer(nil, nil, "127.0.0.1:0", WithAuthFactorService(svc))

	body := `{"value":"dGVzdA=="}`
	req := httptest.NewRequest(http.MethodPost, "/v1/authfactors/e1/verify", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	srv.handleAuthFactorVerify(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing challenge_id = %d, want 400", rec.Code)
	}
}

func TestHandleAuthFactorVerify_Success(t *testing.T) {
	svc := &stubAuthFactorSvc{}
	srv := NewServer(nil, nil, "127.0.0.1:0", WithAuthFactorService(svc))

	body := `{"challenge_id":"chall-1","value":"dGVzdA=="}`
	req := httptest.NewRequest(http.MethodPost, "/v1/authfactors/e1/verify", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	srv.handleAuthFactorVerify(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("success = %d, want 200", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// handleAuthFactorList — with service branches
// ---------------------------------------------------------------------------

func TestHandleAuthFactorList_Success(t *testing.T) {
	now := time.Now().UTC()
	svc := &stubAuthFactorSvc{enrollments: []*authfactors.Enrollment{
		{EnrollmentID: "e1", FactorName: "fido2", UserID: "user-1", EnrolledAt: now},
	}}
	srv := NewServer(nil, nil, "127.0.0.1:0", WithAuthFactorService(svc))

	req := httptest.NewRequest(http.MethodGet, "/v1/authfactors/list?subject=user-1", nil)
	rec := httptest.NewRecorder()
	srv.handleAuthFactorList(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("list success = %d, want 200", rec.Code)
	}
	var resp listFactorsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("total = %d, want 1", resp.Total)
	}
}

func TestHandleAuthFactorList_ServiceError(t *testing.T) {
	svc := &stubAuthFactorSvc{listErr: errors.New("store error")}
	srv := NewServer(nil, nil, "127.0.0.1:0", WithAuthFactorService(svc))

	req := httptest.NewRequest(http.MethodGet, "/v1/authfactors/list", nil)
	rec := httptest.NewRecorder()
	srv.handleAuthFactorList(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("list error = %d, want 500", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// handleAuthFactorRevoke — with service branches
// ---------------------------------------------------------------------------

func TestHandleAuthFactorRevoke_MissingID(t *testing.T) {
	svc := &stubAuthFactorSvc{}
	srv := NewServer(nil, nil, "127.0.0.1:0", WithAuthFactorService(svc))

	req := httptest.NewRequest(http.MethodDelete, "/v1/authfactors/", nil)
	rec := httptest.NewRecorder()
	srv.handleAuthFactorRevoke(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing id = %d, want 400", rec.Code)
	}
}

func TestHandleAuthFactorRevoke_Success(t *testing.T) {
	svc := &stubAuthFactorSvc{}
	srv := NewServer(nil, nil, "127.0.0.1:0", WithAuthFactorService(svc))

	req := httptest.NewRequest(http.MethodDelete, "/v1/authfactors/enroll-1", nil)
	rec := httptest.NewRecorder()
	srv.handleAuthFactorRevoke(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("revoke success = %d, want 204", rec.Code)
	}
}

func TestHandleAuthFactorRevoke_ServiceError(t *testing.T) {
	svc := &stubAuthFactorSvc{revokeErr: &authfactors.EnrollmentNotFoundError{EnrollmentID: "e1"}}
	srv := NewServer(nil, nil, "127.0.0.1:0", WithAuthFactorService(svc))

	req := httptest.NewRequest(http.MethodDelete, "/v1/authfactors/e1", nil)
	rec := httptest.NewRecorder()
	srv.handleAuthFactorRevoke(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("not found = %d, want 404", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// handleAuthFactorEnroll — nil service → 501
// ---------------------------------------------------------------------------

func TestHandleAuthFactorEnroll_NilService_NotImplemented(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0")
	// authFactorSvc is nil by default.
	req := httptest.NewRequest(http.MethodPost, "/v1/authfactors/fido2/enroll", nil)
	rec := httptest.NewRecorder()
	srv.handleAuthFactorEnroll(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("nil authFactorSvc = %d, want 501", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// handleAuthFactorChallenge — nil service → 501
// ---------------------------------------------------------------------------

func TestHandleAuthFactorChallenge_NilService_NotImplemented(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodPost, "/v1/authfactors/enroll-id/challenge", nil)
	rec := httptest.NewRecorder()
	srv.handleAuthFactorChallenge(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("nil authFactorSvc = %d, want 501", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// handleAuthFactorVerify — nil service → 501
// ---------------------------------------------------------------------------

func TestHandleAuthFactorVerify_NilService_NotImplemented(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodPost, "/v1/authfactors/enroll-id/verify", nil)
	rec := httptest.NewRecorder()
	srv.handleAuthFactorVerify(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("nil authFactorSvc = %d, want 501", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// handleAuthFactorList — nil service → 501
// ---------------------------------------------------------------------------

func TestHandleAuthFactorList_NilService_NotImplemented(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodGet, "/v1/authfactors/list", nil)
	rec := httptest.NewRecorder()
	srv.handleAuthFactorList(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("nil authFactorSvc = %d, want 501", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// handleAuthFactorRevoke — nil service → 501
// ---------------------------------------------------------------------------

func TestHandleAuthFactorRevoke_NilService_NotImplemented(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodDelete, "/v1/authfactors/enroll-id", nil)
	rec := httptest.NewRecorder()
	srv.handleAuthFactorRevoke(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("nil authFactorSvc = %d, want 501", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// handleAuthFactorByPath — dispatch to correct handler, method not allowed
// ---------------------------------------------------------------------------

func TestHandleAuthFactorByPath_NilService_EnrollRoute(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodPost, "/v1/authfactors/fido2/enroll", nil)
	rec := httptest.NewRecorder()
	srv.handleAuthFactorByPath(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("enroll via byPath (nil svc) = %d, want 501", rec.Code)
	}
}

func TestHandleAuthFactorByPath_NilService_ChallengeRoute(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodPost, "/v1/authfactors/enroll-id/challenge", nil)
	rec := httptest.NewRecorder()
	srv.handleAuthFactorByPath(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("challenge via byPath (nil svc) = %d, want 501", rec.Code)
	}
}

func TestHandleAuthFactorByPath_NilService_VerifyRoute(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodPost, "/v1/authfactors/enroll-id/verify", nil)
	rec := httptest.NewRecorder()
	srv.handleAuthFactorByPath(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("verify via byPath (nil svc) = %d, want 501", rec.Code)
	}
}

func TestHandleAuthFactorByPath_NilService_DeleteRoute(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodDelete, "/v1/authfactors/enroll-id", nil)
	rec := httptest.NewRecorder()
	srv.handleAuthFactorByPath(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("delete via byPath (nil svc) = %d, want 501", rec.Code)
	}
}

func TestHandleAuthFactorByPath_MethodNotAllowed(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodGet, "/v1/authfactors/fido2/enroll", nil)
	rec := httptest.NewRecorder()
	srv.handleAuthFactorByPath(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("unsupported method = %d, want 405", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// handleEnrollCustodian — nil service → 501
// ---------------------------------------------------------------------------

func TestHandleEnrollCustodian_NilService_NotImplemented(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/enroll-custodian", nil)
	rec := httptest.NewRecorder()
	srv.handleEnrollCustodian(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("nil custodianSvc = %d, want 501", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// handleRevokeCustodian — nil service → 501
// ---------------------------------------------------------------------------

func TestHandleRevokeCustodian_NilService_NotImplemented(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/revoke-custodian", nil)
	rec := httptest.NewRecorder()
	srv.handleRevokeCustodian(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("nil custodianSvc = %d, want 501", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// handleCustodianClaimShareBegin — nil service → 501
// ---------------------------------------------------------------------------

func TestHandleCustodianClaimShareBegin_NilService_NotImplemented(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/claim-share/begin", nil)
	rec := httptest.NewRecorder()
	srv.handleCustodianClaimShareBegin(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("nil ceremonySvc = %d, want 501", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// handleCustodianClaimShareComplete — nil service → 501
// ---------------------------------------------------------------------------

func TestHandleCustodianClaimShareComplete_NilService_NotImplemented(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/claim-share/complete", nil)
	rec := httptest.NewRecorder()
	srv.handleCustodianClaimShareComplete(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("nil ceremonySvc = %d, want 501", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// handleUnsealShare — nil service → 501
// ---------------------------------------------------------------------------

func TestHandleUnsealShare_NilService_NotImplemented(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/unseal-share", nil)
	rec := httptest.NewRecorder()
	srv.handleUnsealShare(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("nil ceremonySvc = %d, want 501", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// handleAuditQuery — nil audit logger → 501
// ---------------------------------------------------------------------------

func TestHandleAuditQuery_NilAuditLogger_NotImplemented(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0")
	// auditLogger is nil by default.
	req := httptest.NewRequest(http.MethodGet, "/v1/audit/events", nil)
	rec := httptest.NewRecorder()
	srv.handleAuditQuery(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("nil auditLogger = %d, want 501", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// handleAuditQuery — non-nil audit logger branch coverage
// ---------------------------------------------------------------------------

func TestHandleAuditQuery_Success(t *testing.T) {
	al := &stubAuditLogger{page: &audit.Page{
		Events: []audit.AuditEvent{{Actor: "alice", Action: "kv:put"}},
		Total:  1,
	}}
	srv := NewServer(nil, nil, "127.0.0.1:0", WithAuditLogger(al))

	req := httptest.NewRequest(http.MethodGet, "/v1/audit/events", nil)
	rec := httptest.NewRecorder()
	srv.handleAuditQuery(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	var resp auditQueryResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("total = %d, want 1", resp.Total)
	}
}

func TestHandleAuditQuery_WithQueryParams(t *testing.T) {
	al := &stubAuditLogger{}
	srv := NewServer(nil, nil, "127.0.0.1:0", WithAuditLogger(al))

	req := httptest.NewRequest(http.MethodGet, "/v1/audit/events?actor=alice&resource=kv&action=put&outcome=success&tenant_id=t1&limit=50&cursor=tok", nil)
	rec := httptest.NewRecorder()
	srv.handleAuditQuery(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestHandleAuditQuery_WithFromTo(t *testing.T) {
	al := &stubAuditLogger{}
	srv := NewServer(nil, nil, "127.0.0.1:0", WithAuditLogger(al))

	from := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	to := time.Now().UTC().Format(time.RFC3339)
	req := httptest.NewRequest(http.MethodGet, "/v1/audit/events?from="+from+"&to="+to, nil)
	rec := httptest.NewRecorder()
	srv.handleAuditQuery(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAuditQuery_InvalidFrom_BadRequest(t *testing.T) {
	al := &stubAuditLogger{}
	srv := NewServer(nil, nil, "127.0.0.1:0", WithAuditLogger(al))

	req := httptest.NewRequest(http.MethodGet, "/v1/audit/events?from=not-a-time", nil)
	rec := httptest.NewRecorder()
	srv.handleAuditQuery(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleAuditQuery_InvalidTo_BadRequest(t *testing.T) {
	al := &stubAuditLogger{}
	srv := NewServer(nil, nil, "127.0.0.1:0", WithAuditLogger(al))

	req := httptest.NewRequest(http.MethodGet, "/v1/audit/events?to=not-a-time", nil)
	rec := httptest.NewRecorder()
	srv.handleAuditQuery(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleAuditQuery_InvalidLimit_BadRequest(t *testing.T) {
	al := &stubAuditLogger{}
	srv := NewServer(nil, nil, "127.0.0.1:0", WithAuditLogger(al))

	req := httptest.NewRequest(http.MethodGet, "/v1/audit/events?limit=abc", nil)
	rec := httptest.NewRecorder()
	srv.handleAuditQuery(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleAuditQuery_QueryError_InternalServerError(t *testing.T) {
	al := &stubAuditLogger{queryErr: errors.New("store unavailable")}
	srv := NewServer(nil, nil, "127.0.0.1:0", WithAuditLogger(al))

	req := httptest.NewRequest(http.MethodGet, "/v1/audit/events", nil)
	rec := httptest.NewRecorder()
	srv.handleAuditQuery(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAuditQuery_EmptyEvents_ReturnsEmptySlice(t *testing.T) {
	al := &stubAuditLogger{page: &audit.Page{Events: nil, Total: 0}}
	srv := NewServer(nil, nil, "127.0.0.1:0", WithAuditLogger(al))

	req := httptest.NewRequest(http.MethodGet, "/v1/audit/events", nil)
	rec := httptest.NewRecorder()
	srv.handleAuditQuery(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	var resp auditQueryResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Events == nil {
		t.Error("expected non-nil events slice (empty, not nil)")
	}
}
