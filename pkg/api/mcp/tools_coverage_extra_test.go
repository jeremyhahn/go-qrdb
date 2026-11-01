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

//go:build codec_json && raft

package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Mock: CeremonyServicer
// ---------------------------------------------------------------------------

type covMockCeremonySvc struct {
	initializeFn func(ctx context.Context, ca ceremony.CeremonyCA) (*service.CeremonyInitResult, error)
	statusFn     func(ctx context.Context) (*service.CeremonyStatusInfo, error)
	claimShareFn func(ctx context.Context, username string) ([]byte, error)
	signCSRFn    func(ctx context.Context, username, soPin, csrPEM, role string) (*service.CeremonySignCSRResult, error)
}

var _ service.CeremonyServicer = (*covMockCeremonySvc)(nil)

func newCovMockCeremonySvc() *covMockCeremonySvc { return &covMockCeremonySvc{} }

func (m *covMockCeremonySvc) CeremonyInitialize(ctx context.Context, ca ceremony.CeremonyCA, _ *service.CeremonyInitializeOptions) (*service.CeremonyInitResult, error) {
	if m.initializeFn != nil {
		return m.initializeFn(ctx, ca)
	}
	return &service.CeremonyInitResult{SPKIPin: "pin123"}, nil
}
func (m *covMockCeremonySvc) CeremonyStatus(ctx context.Context) (*service.CeremonyStatusInfo, error) {
	if m.statusFn != nil {
		return m.statusFn(ctx)
	}
	return &service.CeremonyStatusInfo{SPKIPin: "pin123"}, nil
}
func (m *covMockCeremonySvc) CeremonySignCSR(ctx context.Context, username, soPin, csrPEM, role string) (*service.CeremonySignCSRResult, error) {
	if m.signCSRFn != nil {
		return m.signCSRFn(ctx, username, soPin, csrPEM, role)
	}
	return &service.CeremonySignCSRResult{CertPEM: []byte("-----BEGIN CERTIFICATE-----")}, nil
}

// ---------------------------------------------------------------------------
// Mock: CredentialServicer
// ---------------------------------------------------------------------------

type covMockCredentialSvc struct {
	storeFn           func(ctx context.Context, name string, value []byte) error
	retrieveFn        func(ctx context.Context, name string) ([]byte, error)
	submitFn          func(ctx context.Context, name string, value []byte) error
	deleteFn          func(ctx context.Context, name string) error
	strategyFn        func() string
	listFn            func(ctx context.Context) ([]string, error)
	rotateFn          func(ctx context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error)
	retrieveVersionFn func(ctx context.Context, name string, version uint64) ([]byte, error)
	listVersionsFn    func(ctx context.Context, name string) ([]credentials.CredentialVersion, error)
	gcExpiredFn       func(ctx context.Context, name string) (int, error)
}

var _ service.CredentialServicer = (*covMockCredentialSvc)(nil)

func newCovMockCredentialSvc() *covMockCredentialSvc { return &covMockCredentialSvc{} }

func (m *covMockCredentialSvc) CredentialStore(ctx context.Context, name string, value []byte) error {
	if m.storeFn != nil {
		return m.storeFn(ctx, name, value)
	}
	return nil
}
func (m *covMockCredentialSvc) retrieveValue(ctx context.Context, name string) ([]byte, error) {
	if m.retrieveFn != nil {
		return m.retrieveFn(ctx, name)
	}
	return []byte("secret-value"), nil
}

func (m *covMockCredentialSvc) CredentialRetrieveBuffer(ctx context.Context, name string) (*secure.Buffer, error) {
	v, err := m.retrieveValue(ctx, name)
	if err != nil {
		return nil, err
	}
	return secure.NewBufferFromBytes(append([]byte(nil), v...))
}

func (m *covMockCredentialSvc) CredentialRetrieveVersionBuffer(ctx context.Context, name string, version uint64) (*secure.Buffer, error) {
	v, err := m.retrieveVersionValue(ctx, name, version)
	if err != nil {
		return nil, err
	}
	return secure.NewBufferFromBytes(append([]byte(nil), v...))
}
func (m *covMockCredentialSvc) CredentialSubmit(ctx context.Context, name string, value []byte) error {
	if m.submitFn != nil {
		return m.submitFn(ctx, name, value)
	}
	return nil
}
func (m *covMockCredentialSvc) CredentialDelete(ctx context.Context, name string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, name)
	}
	return nil
}
func (m *covMockCredentialSvc) CredentialStrategy(context.Context) (string, error) {
	return m.credentialStrategy(), nil
}

func (m *covMockCredentialSvc) credentialStrategy() string {
	if m.strategyFn != nil {
		return m.strategyFn()
	}
	return "barrier"
}
func (m *covMockCredentialSvc) CredentialList(ctx context.Context) ([]string, error) {
	if m.listFn != nil {
		return m.listFn(ctx)
	}
	return []string{"cred-a", "cred-b"}, nil
}
func (m *covMockCredentialSvc) CredentialRotate(ctx context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error) {
	if m.rotateFn != nil {
		return m.rotateFn(ctx, name, opts)
	}
	return &credentials.RotateResult{
		Name:            name,
		PreviousVersion: 1,
		NewVersion:      2,
	}, nil
}
func (m *covMockCredentialSvc) retrieveVersionValue(ctx context.Context, name string, version uint64) ([]byte, error) {
	if m.retrieveVersionFn != nil {
		return m.retrieveVersionFn(ctx, name, version)
	}
	return []byte("versioned-value"), nil
}
func (m *covMockCredentialSvc) CredentialListVersions(ctx context.Context, name string) ([]credentials.CredentialVersion, error) {
	if m.listVersionsFn != nil {
		return m.listVersionsFn(ctx, name)
	}
	return []credentials.CredentialVersion{
		{Version: 1, IsCurrent: false, CreatedAt: time.Now().Add(-time.Hour), GraceUntil: time.Now().Add(time.Hour)},
		{Version: 2, IsCurrent: true, CreatedAt: time.Now()},
	}, nil
}
func (m *covMockCredentialSvc) CredentialGCExpired(ctx context.Context, name string) (int, error) {
	if m.gcExpiredFn != nil {
		return m.gcExpiredFn(ctx, name)
	}
	return 3, nil
}

func (m *covMockCredentialSvc) CredentialSetRotationPolicy(_ context.Context, name string, _ credentials.RotationPolicy) error {
	if name == "missing" {
		return credentials.ErrCredentialNotFound
	}
	return nil
}

func (m *covMockCredentialSvc) CredentialRotationPolicy(_ context.Context, name string) (*service.CredentialRotationPolicyStatus, error) {
	if name == "missing" {
		return nil, credentials.ErrCredentialNotFound
	}
	return &service.CredentialRotationPolicyStatus{Policy: credentials.RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 2}}, nil
}

func (m *covMockCredentialSvc) CredentialMigrationConflicts(_ context.Context) ([]credentials.CredentialMigrationConflictError, error) {
	return []credentials.CredentialMigrationConflictError{{Name: "db", Value: true, Policy: true}}, nil
}

func (m *covMockCredentialSvc) CredentialResolveMigrationConflict(_ context.Context, name string, _ credentials.MigrationResolution) error {
	if name == "missing" {
		return credentials.ErrMigrationConflictNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// Mock: LockServicer
// ---------------------------------------------------------------------------

type covMockLockSvc struct {
	lockFn    func(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error)
	tryLockFn func(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error)
	unlockFn  func(ctx context.Context, key string, fencingToken int64) error
	extendFn  func(ctx context.Context, key string, fencingToken int64) (*lock.Lock, error)
	statusFn  func(ctx context.Context, key string) (*lock.Lock, error)
	listFn    func(ctx context.Context, prefix string) ([]*lock.Lock, error)
}

var _ service.LockServicer = (*covMockLockSvc)(nil)

func newCovMockLockSvc() *covMockLockSvc { return &covMockLockSvc{} }

func (m *covMockLockSvc) Lock(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error) {
	if m.lockFn != nil {
		return m.lockFn(ctx, key, holderID, ttl, reentrant)
	}
	return &lock.Lock{Key: key, HolderID: holderID, FencingToken: 42}, nil
}
func (m *covMockLockSvc) TryLock(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error) {
	if m.tryLockFn != nil {
		return m.tryLockFn(ctx, key, holderID, ttl, reentrant)
	}
	return &lock.Lock{Key: key, HolderID: holderID, FencingToken: 99}, nil
}
func (m *covMockLockSvc) Unlock(ctx context.Context, key string, fencingToken int64) error {
	if m.unlockFn != nil {
		return m.unlockFn(ctx, key, fencingToken)
	}
	return nil
}
func (m *covMockLockSvc) Extend(ctx context.Context, key string, fencingToken int64) (*lock.Lock, error) {
	if m.extendFn != nil {
		return m.extendFn(ctx, key, fencingToken)
	}
	return &lock.Lock{Key: key, FencingToken: fencingToken}, nil
}
func (m *covMockLockSvc) Status(ctx context.Context, key string) (*lock.Lock, error) {
	if m.statusFn != nil {
		return m.statusFn(ctx, key)
	}
	return &lock.Lock{Key: key, HolderID: "holder-1", FencingToken: 7}, nil
}
func (m *covMockLockSvc) List(ctx context.Context, prefix string) ([]*lock.Lock, error) {
	if m.listFn != nil {
		return m.listFn(ctx, prefix)
	}
	return []*lock.Lock{
		{Key: "resource/a", HolderID: "proc-1", FencingToken: 1},
	}, nil
}

// ---------------------------------------------------------------------------
// Mock: PINBackend
// ---------------------------------------------------------------------------

type covMockPINBackend struct {
	soPINSet   bool
	soPIN      string
	userPIN    string
	setSOPINFn func(old, newPIN string) error
	setUserFn  func(soPin, newPIN string) error
	verifySOFn func(soPin string) error
}

var _ pin.PINBackend = (*covMockPINBackend)(nil)

func (m *covMockPINBackend) Strategy() pin.StrategyID             { return "software" }
func (m *covMockPINBackend) SOPINSet() bool                       { return m.soPINSet }
func (m *covMockPINBackend) UserPINSet() bool                     { return m.userPIN != "" }
func (m *covMockPINBackend) IsInitialized() bool                  { return m.soPINSet }
func (m *covMockPINBackend) GetLockoutStatus() *pin.LockoutStatus { return nil }
func (m *covMockPINBackend) ResetLockout(_ string) error          { return nil }
func (m *covMockPINBackend) ChangeSOPIN(_, _ string) error        { return nil }
func (m *covMockPINBackend) ChangeUserPIN(_, _ string) error      { return nil }

func (m *covMockPINBackend) SetSOPIN(old, newPIN string) error {
	if m.setSOPINFn != nil {
		return m.setSOPINFn(old, newPIN)
	}
	m.soPIN = newPIN
	m.soPINSet = true
	return nil
}
func (m *covMockPINBackend) SetUserPIN(soPin, newPIN string) error {
	if m.setUserFn != nil {
		return m.setUserFn(soPin, newPIN)
	}
	m.userPIN = newPIN
	return nil
}
func (m *covMockPINBackend) VerifySOPIN(soPin string) error {
	if m.verifySOFn != nil {
		return m.verifySOFn(soPin)
	}
	if soPin != m.soPIN {
		return errors.New("invalid SO PIN")
	}
	return nil
}
func (m *covMockPINBackend) VerifyUserPIN(userPin string) error {
	if userPin != m.userPIN {
		return errors.New("invalid user PIN")
	}
	return nil
}

// ===========================================================================
// ceremony tools
// ===========================================================================

func TestCovCeremonyTools_Registration(t *testing.T) {
	svc := newCovMockCeremonySvc()
	tools := ceremonyTools(svc, nil)
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools))
	}
}

// TestCovHandleCeremonyInitialize_NilCAReachesTheService proves a nil CA
// (BYO-TLS: WithCeremonyCA never applied) reaches the ceremony service
// layer rather than being refused by this tool. ceremony.CeremonyService.
// Initialize is nil-safe by construction -- a BYO-TLS node pins its own
// configured leaf instead of self-issuing one -- so refusing here (the
// pre-fix behavior, go-qrdb#560 review item 5) only blocked `qrdb init` on
// a BYO-TLS deployment using this tool, for no safety reason.
func TestCovHandleCeremonyInitialize_NilCAReachesTheService(t *testing.T) {
	var capturedCA ceremony.CeremonyCA
	capturedSentinel := errors.New("sentinel")
	svc := newCovMockCeremonySvc()
	svc.initializeFn = func(_ context.Context, ca ceremony.CeremonyCA) (*service.CeremonyInitResult, error) {
		capturedCA = ca
		return nil, capturedSentinel
	}
	handler := handleCeremonyInitialize(svc, nil)
	req := buildCallToolRequest(t, "qrdb_ceremony_initialize", map[string]any{})
	_, err := handler(context.Background(), req)
	if !errors.Is(err, capturedSentinel) {
		t.Fatalf("expected the service's own error to propagate, got %v", err)
	}
	if capturedCA != nil {
		t.Fatalf("expected the service to be called with a nil CA (BYO-TLS), got %#v", capturedCA)
	}
}

// TestCovHandleCeremonyInitialize_ServiceError proves a genuine service
// failure (unrelated to the CA) propagates as a *ToolError.
func TestCovHandleCeremonyInitialize_ServiceError(t *testing.T) {
	svc := newCovMockCeremonySvc()
	svc.initializeFn = func(_ context.Context, _ ceremony.CeremonyCA) (*service.CeremonyInitResult, error) {
		return nil, errors.New("already initialized")
	}
	handler := handleCeremonyInitialize(svc, nil)
	req := buildCallToolRequest(t, "qrdb_ceremony_initialize", map[string]any{})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

// TestCovHandleCeremonyStatus_Success decodes the canonical snake_case wire
// shape handleCeremonyStatus emits (a {"state":...,"spki_pin":...} map --
// see tools_ceremony.go's handleCeremonyStatus), NOT the raw
// service.CeremonyStatusInfo's PascalCase Go field names the handler used
// to marshal directly before that was fixed for MCP/SDK-client parity.
func TestCovHandleCeremonyStatus_Success(t *testing.T) {
	handler := handleCeremonyStatus(newCovMockCeremonySvc())
	req := buildCallToolRequest(t, "qrdb_ceremony_status", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
	var status struct {
		State   string `json:"state"`
		SPKIPin string `json:"spki_pin"`
	}
	if jerr := json.Unmarshal([]byte(resultText(t, result)), &status); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if status.SPKIPin == "" {
		t.Error("expected non-empty SPKIPin")
	}
}

func TestCovHandleCeremonyStatus_ServiceError(t *testing.T) {
	svc := newCovMockCeremonySvc()
	svc.statusFn = func(_ context.Context) (*service.CeremonyStatusInfo, error) {
		return nil, errors.New("not initialized")
	}
	handler := handleCeremonyStatus(svc)
	req := buildCallToolRequest(t, "qrdb_ceremony_status", map[string]any{})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleCeremonySignCSR_Success(t *testing.T) {
	handler := handleCeremonySignCSR(newCovMockCeremonySvc())
	req := buildCallToolRequest(t, "qrdb_ceremony_sign_csr", map[string]any{
		"username": "alice",
		"so_pin":   "1234",
		"csr_pem":  "-----BEGIN CERTIFICATE REQUEST-----",
		"role":     "security-officer",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}

func TestCovHandleCeremonySignCSR_MissingUsername(t *testing.T) {
	handler := handleCeremonySignCSR(newCovMockCeremonySvc())
	req := buildCallToolRequest(t, "qrdb_ceremony_sign_csr", map[string]any{
		"so_pin": "1234", "csr_pem": "pem", "role": "r",
	})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleCeremonySignCSR_MissingSOPin(t *testing.T) {
	handler := handleCeremonySignCSR(newCovMockCeremonySvc())
	req := buildCallToolRequest(t, "qrdb_ceremony_sign_csr", map[string]any{
		"username": "alice", "csr_pem": "pem", "role": "r",
	})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleCeremonySignCSR_MissingCSR(t *testing.T) {
	handler := handleCeremonySignCSR(newCovMockCeremonySvc())
	req := buildCallToolRequest(t, "qrdb_ceremony_sign_csr", map[string]any{
		"username": "alice", "so_pin": "1234", "role": "r",
	})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleCeremonySignCSR_MissingRole(t *testing.T) {
	handler := handleCeremonySignCSR(newCovMockCeremonySvc())
	req := buildCallToolRequest(t, "qrdb_ceremony_sign_csr", map[string]any{
		"username": "alice", "so_pin": "1234", "csr_pem": "pem",
	})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleCeremonySignCSR_ServiceError(t *testing.T) {
	svc := newCovMockCeremonySvc()
	svc.signCSRFn = func(_ context.Context, _, _, _, _ string) (*service.CeremonySignCSRResult, error) {
		return nil, errors.New("sign failed")
	}
	handler := handleCeremonySignCSR(svc)
	req := buildCallToolRequest(t, "qrdb_ceremony_sign_csr", map[string]any{
		"username": "alice", "so_pin": "1234", "csr_pem": "pem", "role": "r",
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// credential tools
// ===========================================================================

func TestCovCredentialTools_Registration(t *testing.T) {
	svc := newCovMockCredentialSvc()
	tools := credentialTools(svc)
	if len(tools) != 14 {
		t.Fatalf("expected 14 tools, got %d", len(tools))
	}
}

func TestCovHandleCredentialDelete_Success(t *testing.T) {
	handler := handleCredentialDelete(newCovMockCredentialSvc())
	req := buildCallToolRequest(t, "qrdb_credential_delete", map[string]any{"name": "my-cred"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
	if resultText(t, result) != "credential deleted" {
		t.Errorf("unexpected text: %s", resultText(t, result))
	}
}

func TestCovHandleCredentialDelete_MissingName(t *testing.T) {
	handler := handleCredentialDelete(newCovMockCredentialSvc())
	req := buildCallToolRequest(t, "qrdb_credential_delete", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleCredentialDelete_ServiceError(t *testing.T) {
	svc := newCovMockCredentialSvc()
	svc.deleteFn = func(_ context.Context, _ string) error { return errors.New("not found") }
	handler := handleCredentialDelete(svc)
	req := buildCallToolRequest(t, "qrdb_credential_delete", map[string]any{"name": "x"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
	var te *ToolError
	if !errors.As(err, &te) || te.Tool != "qrdb_credential_delete" {
		t.Errorf("expected ToolError qrdb_credential_delete, got %v", err)
	}
}

func TestCovHandleCredentialList_Success(t *testing.T) {
	handler := handleCredentialList(newCovMockCredentialSvc())
	req := buildCallToolRequest(t, "qrdb_credential_list", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var resp struct {
		Names []string `json:"names"`
	}
	if jerr := json.Unmarshal([]byte(resultText(t, result)), &resp); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if len(resp.Names) != 2 {
		t.Errorf("expected 2 names, got %d", len(resp.Names))
	}
}

func TestCovHandleCredentialList_NilNames(t *testing.T) {
	svc := newCovMockCredentialSvc()
	svc.listFn = func(_ context.Context) ([]string, error) { return nil, nil }
	handler := handleCredentialList(svc)
	req := buildCallToolRequest(t, "qrdb_credential_list", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var resp struct {
		Names []string `json:"names"`
	}
	if jerr := json.Unmarshal([]byte(resultText(t, result)), &resp); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if resp.Names == nil {
		t.Error("expected non-nil empty array")
	}
}

func TestCovHandleCredentialList_ServiceError(t *testing.T) {
	svc := newCovMockCredentialSvc()
	svc.listFn = func(_ context.Context) ([]string, error) { return nil, errors.New("err") }
	handler := handleCredentialList(svc)
	req := buildCallToolRequest(t, "qrdb_credential_list", map[string]any{})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleCredentialRotate_Success(t *testing.T) {
	handler := handleCredentialRotate(newCovMockCredentialSvc())
	req := buildCallToolRequest(t, "qrdb_credential_rotate", map[string]any{
		"name": "my-cred", "new_value": base64.StdEncoding.EncodeToString([]byte("newpass")), "grace_period_seconds": float64(60),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}

func TestCovHandleCredentialRotate_WithGraceUntil(t *testing.T) {
	svc := newCovMockCredentialSvc()
	svc.rotateFn = func(_ context.Context, name string, _ credentials.RotateOptions) (*credentials.RotateResult, error) {
		return &credentials.RotateResult{
			Name:            name,
			PreviousVersion: 1,
			NewVersion:      2,
			GraceUntil:      time.Now().Add(time.Hour),
		}, nil
	}
	handler := handleCredentialRotate(svc)
	req := buildCallToolRequest(t, "qrdb_credential_rotate", map[string]any{
		"name": "cred", "new_value": base64.StdEncoding.EncodeToString([]byte("v2")),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
	var resp struct {
		GraceUntilUnix int64 `json:"grace_until_unix,omitempty"`
	}
	if jerr := json.Unmarshal([]byte(resultText(t, result)), &resp); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if resp.GraceUntilUnix == 0 {
		t.Error("expected non-zero grace_until_unix")
	}
}

func TestCovHandleCredentialRotate_MissingName(t *testing.T) {
	handler := handleCredentialRotate(newCovMockCredentialSvc())
	req := buildCallToolRequest(t, "qrdb_credential_rotate", map[string]any{"new_value": base64.StdEncoding.EncodeToString([]byte("v"))})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleCredentialRotate_MissingNewValue(t *testing.T) {
	handler := handleCredentialRotate(newCovMockCredentialSvc())
	req := buildCallToolRequest(t, "qrdb_credential_rotate", map[string]any{"name": "c"})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleCredentialRotate_NegativeGrace(t *testing.T) {
	handler := handleCredentialRotate(newCovMockCredentialSvc())
	req := buildCallToolRequest(t, "qrdb_credential_rotate", map[string]any{
		"name": "c", "new_value": base64.StdEncoding.EncodeToString([]byte("v")), "grace_period_seconds": float64(-1),
	})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result for negative grace")
	}
}

func TestCovHandleCredentialRotate_ServiceError(t *testing.T) {
	svc := newCovMockCredentialSvc()
	svc.rotateFn = func(_ context.Context, _ string, _ credentials.RotateOptions) (*credentials.RotateResult, error) {
		return nil, errors.New("rotate err")
	}
	handler := handleCredentialRotate(svc)
	req := buildCallToolRequest(t, "qrdb_credential_rotate", map[string]any{"name": "c", "new_value": base64.StdEncoding.EncodeToString([]byte("v"))})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleCredentialRetrieveVersion_Success(t *testing.T) {
	handler := handleCredentialRetrieveVersion(newCovMockCredentialSvc())
	req := buildCallToolRequest(t, "qrdb_credential_retrieve_version", map[string]any{
		"name": "my-cred", "version": float64(2),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}

func TestCovHandleCredentialRetrieveVersion_MissingName(t *testing.T) {
	handler := handleCredentialRetrieveVersion(newCovMockCredentialSvc())
	req := buildCallToolRequest(t, "qrdb_credential_retrieve_version", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleCredentialRetrieveVersion_ServiceError(t *testing.T) {
	svc := newCovMockCredentialSvc()
	svc.retrieveVersionFn = func(_ context.Context, _ string, _ uint64) ([]byte, error) {
		return nil, errors.New("not found")
	}
	handler := handleCredentialRetrieveVersion(svc)
	req := buildCallToolRequest(t, "qrdb_credential_retrieve_version", map[string]any{"name": "c"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleCredentialListVersions_Success(t *testing.T) {
	handler := handleCredentialListVersions(newCovMockCredentialSvc())
	req := buildCallToolRequest(t, "qrdb_credential_list_versions", map[string]any{"name": "my-cred"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
	var resp struct {
		Name     string `json:"name"`
		Versions []any  `json:"versions"`
	}
	if jerr := json.Unmarshal([]byte(resultText(t, result)), &resp); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if resp.Name != "my-cred" {
		t.Errorf("expected name 'my-cred', got %q", resp.Name)
	}
	if len(resp.Versions) != 2 {
		t.Errorf("expected 2 versions, got %d", len(resp.Versions))
	}
}

func TestCovHandleCredentialListVersions_MissingName(t *testing.T) {
	handler := handleCredentialListVersions(newCovMockCredentialSvc())
	req := buildCallToolRequest(t, "qrdb_credential_list_versions", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleCredentialListVersions_ServiceError(t *testing.T) {
	svc := newCovMockCredentialSvc()
	svc.listVersionsFn = func(_ context.Context, _ string) ([]credentials.CredentialVersion, error) {
		return nil, errors.New("err")
	}
	handler := handleCredentialListVersions(svc)
	req := buildCallToolRequest(t, "qrdb_credential_list_versions", map[string]any{"name": "c"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleCredentialGCExpired_Success(t *testing.T) {
	handler := handleCredentialGCExpired(newCovMockCredentialSvc())
	req := buildCallToolRequest(t, "qrdb_credential_gc_expired", map[string]any{"name": "my-cred"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
	var resp struct {
		Reaped int `json:"reaped"`
	}
	if jerr := json.Unmarshal([]byte(resultText(t, result)), &resp); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if resp.Reaped != 3 {
		t.Errorf("expected reaped=3, got %d", resp.Reaped)
	}
}

func TestCovHandleCredentialGCExpired_MissingName(t *testing.T) {
	handler := handleCredentialGCExpired(newCovMockCredentialSvc())
	req := buildCallToolRequest(t, "qrdb_credential_gc_expired", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleCredentialGCExpired_ServiceError(t *testing.T) {
	svc := newCovMockCredentialSvc()
	svc.gcExpiredFn = func(_ context.Context, _ string) (int, error) { return 0, errors.New("err") }
	handler := handleCredentialGCExpired(svc)
	req := buildCallToolRequest(t, "qrdb_credential_gc_expired", map[string]any{"name": "c"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// lock tools
// ===========================================================================

func TestCovLockTools_Registration(t *testing.T) {
	svc := newCovMockLockSvc()
	tools := lockTools(svc)
	if len(tools) != 6 {
		t.Fatalf("expected 6 tools, got %d", len(tools))
	}
}

func TestCovHandleLockAcquire_Success(t *testing.T) {
	handler := handleLockAcquire(newCovMockLockSvc())
	req := buildCallToolRequest(t, "qrdb_lock_acquire", map[string]any{
		"key": "res/a", "holder_id": "proc-1", "ttl": float64(30),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
	var l lock.Lock
	if jerr := json.Unmarshal([]byte(resultText(t, result)), &l); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if l.Key != "res/a" {
		t.Errorf("expected key 'res/a', got %q", l.Key)
	}
}

func TestCovHandleLockAcquire_MissingKey(t *testing.T) {
	handler := handleLockAcquire(newCovMockLockSvc())
	req := buildCallToolRequest(t, "qrdb_lock_acquire", map[string]any{
		"holder_id": "p", "ttl": float64(30),
	})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result for missing key")
	}
}

func TestCovHandleLockAcquire_MissingHolder(t *testing.T) {
	handler := handleLockAcquire(newCovMockLockSvc())
	req := buildCallToolRequest(t, "qrdb_lock_acquire", map[string]any{
		"key": "k", "ttl": float64(30),
	})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result for missing holder_id")
	}
}

func TestCovHandleLockAcquire_ZeroTTL(t *testing.T) {
	handler := handleLockAcquire(newCovMockLockSvc())
	req := buildCallToolRequest(t, "qrdb_lock_acquire", map[string]any{
		"key": "k", "holder_id": "p", "ttl": float64(0),
	})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result for zero ttl")
	}
}

func TestCovHandleLockAcquire_ServiceError(t *testing.T) {
	svc := newCovMockLockSvc()
	svc.lockFn = func(_ context.Context, _, _ string, _ time.Duration, _ bool) (*lock.Lock, error) {
		return nil, errors.New("lock held")
	}
	handler := handleLockAcquire(svc)
	req := buildCallToolRequest(t, "qrdb_lock_acquire", map[string]any{
		"key": "k", "holder_id": "p", "ttl": float64(10),
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleLockRelease_Success(t *testing.T) {
	handler := handleLockRelease(newCovMockLockSvc())
	req := buildCallToolRequest(t, "qrdb_lock_release", map[string]any{
		"key": "res/a", "fencing_token": float64(42),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}

func TestCovHandleLockRelease_MissingKey(t *testing.T) {
	handler := handleLockRelease(newCovMockLockSvc())
	req := buildCallToolRequest(t, "qrdb_lock_release", map[string]any{"fencing_token": float64(1)})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleLockRelease_ZeroFencingToken(t *testing.T) {
	handler := handleLockRelease(newCovMockLockSvc())
	req := buildCallToolRequest(t, "qrdb_lock_release", map[string]any{"key": "k"})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result for zero fencing token")
	}
}

func TestCovHandleLockRelease_ServiceError(t *testing.T) {
	svc := newCovMockLockSvc()
	svc.unlockFn = func(_ context.Context, _ string, _ int64) error { return errors.New("stale token") }
	handler := handleLockRelease(svc)
	req := buildCallToolRequest(t, "qrdb_lock_release", map[string]any{
		"key": "k", "fencing_token": float64(99),
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleLockExtend_Success(t *testing.T) {
	handler := handleLockExtend(newCovMockLockSvc())
	req := buildCallToolRequest(t, "qrdb_lock_extend", map[string]any{
		"key": "res/a", "fencing_token": float64(42),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}

func TestCovHandleLockExtend_MissingKey(t *testing.T) {
	handler := handleLockExtend(newCovMockLockSvc())
	req := buildCallToolRequest(t, "qrdb_lock_extend", map[string]any{"fencing_token": float64(1)})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleLockExtend_ZeroFencingToken(t *testing.T) {
	handler := handleLockExtend(newCovMockLockSvc())
	req := buildCallToolRequest(t, "qrdb_lock_extend", map[string]any{"key": "k"})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleLockExtend_ServiceError(t *testing.T) {
	svc := newCovMockLockSvc()
	svc.extendFn = func(_ context.Context, _ string, _ int64) (*lock.Lock, error) {
		return nil, errors.New("err")
	}
	handler := handleLockExtend(svc)
	req := buildCallToolRequest(t, "qrdb_lock_extend", map[string]any{
		"key": "k", "fencing_token": float64(1),
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleLockStatus_Success(t *testing.T) {
	handler := handleLockStatus(newCovMockLockSvc())
	req := buildCallToolRequest(t, "qrdb_lock_status", map[string]any{"key": "res/a"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}

func TestCovHandleLockStatus_MissingKey(t *testing.T) {
	handler := handleLockStatus(newCovMockLockSvc())
	req := buildCallToolRequest(t, "qrdb_lock_status", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleLockStatus_ServiceError(t *testing.T) {
	svc := newCovMockLockSvc()
	svc.statusFn = func(_ context.Context, _ string) (*lock.Lock, error) {
		return nil, errors.New("err")
	}
	handler := handleLockStatus(svc)
	req := buildCallToolRequest(t, "qrdb_lock_status", map[string]any{"key": "k"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleLockList_Success(t *testing.T) {
	handler := handleLockList(newCovMockLockSvc())
	req := buildCallToolRequest(t, "qrdb_lock_list", map[string]any{"prefix": "res/"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}

func TestCovHandleLockList_ServiceError(t *testing.T) {
	svc := newCovMockLockSvc()
	svc.listFn = func(_ context.Context, _ string) ([]*lock.Lock, error) {
		return nil, errors.New("err")
	}
	handler := handleLockList(svc)
	req := buildCallToolRequest(t, "qrdb_lock_list", map[string]any{})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// tools_barrier_pin.go
// ===========================================================================

// TestCovApplyBarrierInitializePINs_NilBackend proves PINs supplied with no
// backend are refused, never silently dropped.
func TestCovApplyBarrierInitializePINs_NilBackend(t *testing.T) {
	if err := pin.Provision(context.Background(), nil, "so", "user"); !errors.Is(err, pin.ErrBackendNotConfigured) {
		t.Fatalf("Provision with no backend = %v, want ErrBackendNotConfigured", err)
	}
}

func TestCovApplyBarrierInitializePINs_SetBoth(t *testing.T) {
	backend := &covMockPINBackend{}
	if err := pin.Provision(context.Background(), backend, "sopin123", "userpin456"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if backend.soPIN != "sopin123" {
		t.Errorf("expected soPIN 'sopin123', got %q", backend.soPIN)
	}
	if backend.userPIN != "userpin456" {
		t.Errorf("expected userPIN 'userpin456', got %q", backend.userPIN)
	}
}

func TestCovApplyBarrierInitializePINs_SetSOPINError(t *testing.T) {
	backend := &covMockPINBackend{
		setSOPINFn: func(_, _ string) error { return errors.New("set failed") },
	}
	if err := pin.Provision(context.Background(), backend, "sopin", ""); err == nil {
		t.Fatal("expected error from SetSOPIN")
	}
}

func TestCovApplyBarrierInitializePINs_SetUserPINError(t *testing.T) {
	backend := &covMockPINBackend{
		setUserFn: func(_, _ string) error { return errors.New("set user failed") },
	}
	if err := pin.Provision(context.Background(), backend, "sopin", "userpin"); err == nil {
		t.Fatal("expected error from SetUserPIN")
	}
}

// TestCovVerifyBarrierSOPIN_NilBackend proves the SO PIN gate fails closed.
func TestCovVerifyBarrierSOPIN_NilBackend(t *testing.T) {
	if err := pin.RequireSOPIN(context.Background(), nil, "anypin"); !errors.Is(err, pin.ErrBackendNotConfigured) {
		t.Fatalf("RequireSOPIN with no backend = %v, want ErrBackendNotConfigured", err)
	}
}

func TestCovVerifyBarrierSOPIN_NotSet(t *testing.T) {
	backend := &covMockPINBackend{soPINSet: false}
	if err := pin.RequireSOPIN(context.Background(), backend, "anypin"); err != nil {
		t.Fatalf("expected nil error when SO PIN not set, got %v", err)
	}
}

func TestCovVerifyBarrierSOPIN_Correct(t *testing.T) {
	backend := &covMockPINBackend{soPINSet: true, soPIN: "correct"}
	if err := pin.RequireSOPIN(context.Background(), backend, "correct"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCovVerifyBarrierSOPIN_Wrong(t *testing.T) {
	backend := &covMockPINBackend{soPINSet: true, soPIN: "correct"}
	if err := pin.RequireSOPIN(context.Background(), backend, "wrong"); err == nil {
		t.Fatal("expected error for wrong PIN")
	}
}

// ===========================================================================
// admin shard handlers
// ===========================================================================

func TestCovHandleStartShard_Success(t *testing.T) {
	handler := handleStartShard(newMockAdminService())
	req := buildCallToolRequest(t, "qrdb_start_shard", map[string]any{
		"shard_id": float64(1), "replica_id": float64(1),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}

func TestCovHandleStartShard_WithMembersJSON(t *testing.T) {
	handler := handleStartShard(newMockAdminService())
	req := buildCallToolRequest(t, "qrdb_start_shard", map[string]any{
		"shard_id":     float64(1),
		"replica_id":   float64(1),
		"members_json": `{"1":"127.0.0.1:63001","2":"127.0.0.1:63002"}`,
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}

func TestCovHandleStartShard_MissingShardID(t *testing.T) {
	handler := handleStartShard(newMockAdminService())
	req := buildCallToolRequest(t, "qrdb_start_shard", map[string]any{"replica_id": float64(1)})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result for missing shard_id")
	}
}

func TestCovHandleStartShard_MissingReplicaID(t *testing.T) {
	handler := handleStartShard(newMockAdminService())
	req := buildCallToolRequest(t, "qrdb_start_shard", map[string]any{"shard_id": float64(1)})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result for missing replica_id")
	}
}

func TestCovHandleStartShard_BadMembersJSON(t *testing.T) {
	handler := handleStartShard(newMockAdminService())
	req := buildCallToolRequest(t, "qrdb_start_shard", map[string]any{
		"shard_id": float64(1), "replica_id": float64(1), "members_json": "{bad",
	})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result for bad members_json")
	}
}

func TestCovHandleStartShard_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.startShardFn = func(_ context.Context, _ *service.StartShardRequest) error {
		return errors.New("start failed")
	}
	handler := handleStartShard(adminSvc)
	req := buildCallToolRequest(t, "qrdb_start_shard", map[string]any{
		"shard_id": float64(1), "replica_id": float64(1),
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleStopShard_Success(t *testing.T) {
	handler := handleStopShard(newMockAdminService())
	req := buildCallToolRequest(t, "qrdb_stop_shard", map[string]any{"shard_id": float64(1)})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}

func TestCovHandleStopShard_MissingShardID(t *testing.T) {
	handler := handleStopShard(newMockAdminService())
	req := buildCallToolRequest(t, "qrdb_stop_shard", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleStopShard_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.stopShardFn = func(_ context.Context, _ uint64) error {
		return errors.New("stop failed")
	}
	handler := handleStopShard(adminSvc)
	req := buildCallToolRequest(t, "qrdb_stop_shard", map[string]any{"shard_id": float64(1)})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleGetShardState_Success(t *testing.T) {
	handler := handleGetShardState(newMockAdminService())
	req := buildCallToolRequest(t, "qrdb_shard_state", map[string]any{"shard_id": float64(1)})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}

func TestCovHandleGetShardState_MissingShardID(t *testing.T) {
	handler := handleGetShardState(newMockAdminService())
	req := buildCallToolRequest(t, "qrdb_shard_state", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleGetShardState_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.getShardStateFn = func(_ context.Context, _ uint64) (*service.ShardStateResponse, error) {
		return nil, errors.New("err")
	}
	handler := handleGetShardState(adminSvc)
	req := buildCallToolRequest(t, "qrdb_shard_state", map[string]any{"shard_id": float64(1)})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleGetTopology_Success(t *testing.T) {
	handler := handleGetTopology(newMockAdminService())
	req := buildCallToolRequest(t, "qrdb_topology", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}

func TestCovHandleGetTopology_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.getTopologyFn = func(_ context.Context) (*service.TopologyResponse, error) {
		return nil, errors.New("err")
	}
	handler := handleGetTopology(adminSvc)
	req := buildCallToolRequest(t, "qrdb_topology", map[string]any{})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// credential submit + strategy (0% / low coverage)
// ===========================================================================

func TestCovHandleCredentialSubmit_Success(t *testing.T) {
	handler := handleCredentialSubmit(newCovMockCredentialSvc())
	req := buildCallToolRequest(t, "qrdb_credential_submit", map[string]any{
		"name": "my-cred", "value": base64.StdEncoding.EncodeToString([]byte("secret")),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
	if resultText(t, result) != "credential submitted" {
		t.Errorf("unexpected: %s", resultText(t, result))
	}
}

func TestCovHandleCredentialSubmit_MissingName(t *testing.T) {
	handler := handleCredentialSubmit(newCovMockCredentialSvc())
	req := buildCallToolRequest(t, "qrdb_credential_submit", map[string]any{"value": base64.StdEncoding.EncodeToString([]byte("v"))})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleCredentialSubmit_MissingValue(t *testing.T) {
	handler := handleCredentialSubmit(newCovMockCredentialSvc())
	req := buildCallToolRequest(t, "qrdb_credential_submit", map[string]any{"name": "n"})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleCredentialSubmit_ServiceError(t *testing.T) {
	svc := newCovMockCredentialSvc()
	svc.submitFn = func(_ context.Context, _ string, _ []byte) error { return errors.New("err") }
	handler := handleCredentialSubmit(svc)
	req := buildCallToolRequest(t, "qrdb_credential_submit", map[string]any{"name": "n", "value": base64.StdEncoding.EncodeToString([]byte("v"))})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleCredentialStrategy_Success(t *testing.T) {
	handler := handleCredentialStrategy(newCovMockCredentialSvc())
	req := buildCallToolRequest(t, "qrdb_credential_strategy", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
	var resp struct {
		Strategy string `json:"strategy"`
	}
	if jerr := json.Unmarshal([]byte(resultText(t, result)), &resp); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if resp.Strategy != "barrier" {
		t.Errorf("expected strategy 'barrier', got %q", resp.Strategy)
	}
}

// ===========================================================================
// handleLockTryLock
// ===========================================================================

func TestCovHandleLockTryLock_Success(t *testing.T) {
	handler := handleLockTryLock(newCovMockLockSvc())
	req := buildCallToolRequest(t, "qrdb_lock_trylock", map[string]any{
		"key": "r", "holder_id": "p", "ttl": float64(10),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}

func TestCovHandleLockTryLock_MissingKey(t *testing.T) {
	handler := handleLockTryLock(newCovMockLockSvc())
	req := buildCallToolRequest(t, "qrdb_lock_trylock", map[string]any{"holder_id": "p", "ttl": float64(10)})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleLockTryLock_MissingHolder(t *testing.T) {
	handler := handleLockTryLock(newCovMockLockSvc())
	req := buildCallToolRequest(t, "qrdb_lock_trylock", map[string]any{"key": "k", "ttl": float64(10)})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleLockTryLock_ZeroTTL(t *testing.T) {
	handler := handleLockTryLock(newCovMockLockSvc())
	req := buildCallToolRequest(t, "qrdb_lock_trylock", map[string]any{"key": "k", "holder_id": "p", "ttl": float64(0)})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result for zero ttl")
	}
}

func TestCovHandleLockTryLock_ServiceError(t *testing.T) {
	svc := newCovMockLockSvc()
	svc.tryLockFn = func(_ context.Context, _, _ string, _ time.Duration, _ bool) (*lock.Lock, error) {
		return nil, errors.New("held")
	}
	handler := handleLockTryLock(svc)
	req := buildCallToolRequest(t, "qrdb_lock_trylock", map[string]any{
		"key": "k", "holder_id": "p", "ttl": float64(10),
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// RAG tool schema registration (covers 0% ragTools, toolRAGIngest, etc.)
// ===========================================================================

func TestCovRAGTools_Registration(t *testing.T) {
	svc := &mockRAGSvc{}
	tools := ragTools(svc)
	if len(tools) != 4 {
		t.Fatalf("expected 4 tools, got %d", len(tools))
	}
	names := []string{
		"qrdb_rag_ingest",
		"qrdb_rag_query",
		"qrdb_rag_delete_document",
		"qrdb_rag_list_documents",
	}
	toolMap := make(map[string]bool, len(tools))
	for _, tool := range tools {
		toolMap[tool.Tool.Name] = true
	}
	for _, name := range names {
		if !toolMap[name] {
			t.Errorf("expected tool %s to be registered", name)
		}
	}
}
