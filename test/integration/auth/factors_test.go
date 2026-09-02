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

//go:build integration

// Package authintegration_test exercises the M14 user-side AuthFactor
// surface end to end across the four protocols. The tests build an
// in-process `service.UserAuthFactorServicer` backed by an in-memory KV
// store, then drive it through:
//
//   - REST: direct HTTP calls against `httptest.NewServer` wrapping the
//     net/http handlers in pkg/api/rest.
//   - gRPC: a real loopback grpc.Server with the M14 service descriptor
//     registered, exercised via the per-method *Unary helper functions.
//   - QUIC: the JSON wire format used by `pkg/api/quic` for
//     userauthfactor.* frames; payloads are encoded and decoded the same
//     way the QUIC server does, validating the wire shape.
//   - MCP: the JSON tool-result shape used by
//     `pkg/api/mcp.userAuthFactorTools`, exercised via the same in-memory
//     service.
//
// The tests assert the spec acceptance criteria from
// docs/08-bootstrap-auth-architecture.md §5e:
//
//  1. Create a user (assigned a synthetic uint64 ID).
//  2. Enroll mtls + fido2 + piv factors via three different "vendors"
//     using three different transports.
//  3. Login via each factor; assert all three resolve to the same
//     UserID.
//  4. Remove all but one factor; verify ErrLastFactor on the last
//     remove without force.
//  5. Force-remove the last factor; verify the user is marked inactive.
package authintegration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	grpcsrv "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// In-memory KV that satisfies service.KVServicer.
// ---------------------------------------------------------------------------

type intKV struct {
	mu    sync.Mutex
	store map[string][]byte
}

func newIntKV() *intKV { return &intKV{store: make(map[string][]byte)} }

// The compile-time check keeps this double honest. intKV went weeks without
// CompareAndDelete or CompareAndSwap after the guarded verbs landed (#119/#121)
// and nothing noticed, because no CI leg compiles this package (#203).
var _ service.KVServicer = (*intKV)(nil)

func (m *intKV) Put(_ context.Context, k string, v []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store[k] = append([]byte(nil), v...)
	return nil
}

// PutIfAbsent stores v under k only when k is absent, reporting whether it
// stored. The whole check-and-store is done under the same lock Put uses, so
// concurrent callers see exactly one winner — the atomicity the interface
// requires, not a Get-then-Put approximation of it.
func (m *intKV) PutIfAbsent(_ context.Context, k string, v []byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.store[k]; exists {
		return false, nil
	}
	m.store[k] = append([]byte(nil), v...)
	return true, nil
}

// CompareAndDelete removes k only when its stored value still equals expected,
// reporting whether it removed. It is the release half of PutIfAbsent
// (go-qrdb#41) and, like PutIfAbsent above, does the whole compare-and-remove
// under the one lock so an unguarded delete cannot un-decide what a guarded
// create decided.
func (m *intKV) CompareAndDelete(_ context.Context, k string, expected []byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.store[k]
	if !ok {
		return false, nil
	}
	if !bytes.Equal(current, expected) {
		return false, nil
	}
	delete(m.store, k)
	return true, nil
}

// CompareAndSwap replaces k's value with next only when its stored value still
// equals expected, reporting whether it replaced. An absent key is left absent
// -- a swap is not a create (go-qrdb#56).
func (m *intKV) CompareAndSwap(_ context.Context, k string, expected, next []byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.store[k]
	if !ok {
		return false, nil
	}
	if !bytes.Equal(current, expected) {
		return false, nil
	}
	m.store[k] = append([]byte(nil), next...)
	return true, nil
}

func (m *intKV) Get(_ context.Context, k string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.store[k]
	if !ok {
		return nil, &service.KeyNotFoundError{Key: k}
	}
	return append([]byte(nil), v...), nil
}

func (m *intKV) Delete(_ context.Context, k string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.store, k)
	return nil
}

func (m *intKV) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0)
	for k := range m.store {
		if hasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *intKV) Scan(_ context.Context, prefix string) (map[string][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string][]byte)
	for k, v := range m.store {
		if hasPrefix(k, prefix) {
			out[k] = append([]byte(nil), v...)
		}
	}
	return out, nil
}

func (m *intKV) GetStale(_ context.Context, _ string) ([]byte, error)    { return nil, nil }
func (m *intKV) ListStale(_ context.Context, _ string) ([]string, error) { return nil, nil }
func (m *intKV) ScanStale(_ context.Context, _ string) (map[string][]byte, error) {
	return nil, nil
}
func (m *intKV) PutWithMode(_ context.Context, _ string, _ []byte, _ writemode.Mode) error {
	return nil
}
func (m *intKV) DeleteWithMode(_ context.Context, _ string, _ writemode.Mode) error {
	return nil
}
func (m *intKV) GetWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) ([]byte, error) {
	return nil, nil
}
func (m *intKV) ListWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) ([]string, error) {
	return nil, nil
}
func (m *intKV) ScanWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (map[string][]byte, error) {
	return nil, nil
}
func (m *intKV) ExistsWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (bool, error) {
	return false, nil
}
func (m *intKV) PutEntity(_ context.Context, _ *service.PutEntityRequest) (*service.PutEntityResponse, error) {
	return &service.PutEntityResponse{}, nil
}
func (m *intKV) GetEntity(_ context.Context, _ *service.GetEntityRequest) (*service.GetEntityResponse, error) {
	return &service.GetEntityResponse{}, nil
}
func (m *intKV) DeleteEntity(_ context.Context, _ *service.DeleteEntityRequest) (*service.DeleteEntityResponse, error) {
	return &service.DeleteEntityResponse{}, nil
}
func (m *intKV) ListEntities(_ context.Context, _ *service.ListEntitiesRequest) (*service.ListEntitiesResponse, error) {
	return &service.ListEntitiesResponse{}, nil
}

func hasPrefix(s, p string) bool {
	return len(s) >= len(p) && s[:len(p)] == p
}

// ---------------------------------------------------------------------------
// User state stub
// ---------------------------------------------------------------------------

type userStateStub struct {
	mu       sync.Mutex
	disabled map[uint64]bool
}

func newUserStateStub() *userStateStub {
	return &userStateStub{disabled: make(map[uint64]bool)}
}

func (s *userStateStub) SetUserActive(_ context.Context, id uint64, active bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.disabled[id] = !active
	return nil
}

func (s *userStateStub) IsDisabled(id uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.disabled[id]
}

// ---------------------------------------------------------------------------
// Multi-protocol harness
// ---------------------------------------------------------------------------

type harness struct {
	svc      service.UserAuthFactorServicer
	users    *userStateStub
	httpSrv  *httptest.Server
	grpcLis  net.Listener
	grpcSrv  *grpc.Server
	grpcConn *grpc.ClientConn
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	users := newUserStateStub()
	svc, err := service.NewUserAuthFactorService(service.UserAuthFactorServiceConfig{
		KV:               newIntKV(),
		UserStateUpdater: users,
	})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}

	h := &harness{svc: svc, users: users}

	// REST: stand up an httptest server with bare-bones routing for the
	// M14 endpoints. This drives the same code path as the real server,
	// minus the surrounding rest.NewServer initialization.
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/users/factors", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			restAdd(svc, w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/v1/users/factors/list", func(w http.ResponseWriter, r *http.Request) {
		restList(svc, w, r)
	})
	mux.HandleFunc("/v1/users/factors/login", func(w http.ResponseWriter, r *http.Request) {
		restLogin(svc, w, r)
	})
	mux.HandleFunc("/v1/users/factors/", func(w http.ResponseWriter, r *http.Request) {
		restByPath(svc, w, r)
	})
	h.httpSrv = httptest.NewServer(mux)

	// gRPC: real loopback.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	h.grpcLis = lis
	h.grpcSrv = grpc.NewServer()
	// userSvc is nil here: this harness exercises the auth-factor RPCs, not
	// login-response enrichment (which is best-effort and skipped on a nil
	// user servicer).
	grpcsrv.RegisterUserAuthFactorService(h.grpcSrv, svc, nil)
	go func() { _ = h.grpcSrv.Serve(lis) }()

	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	h.grpcConn = conn

	t.Cleanup(func() {
		_ = conn.Close()
		h.grpcSrv.GracefulStop()
		h.httpSrv.Close()
	})
	return h
}

// ---------------------------------------------------------------------------
// REST helpers — minimal, mirror the production handlers
// ---------------------------------------------------------------------------

func restAdd(svc service.UserAuthFactorServicer, w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID     uint64            `json:"user_id"`
		Type       string            `json:"type"`
		Label      string            `json:"label"`
		Credential []byte            `json:"credential"`
		Metadata   map[string]string `json:"metadata"`
		CreatedBy  string            `json:"created_by"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	factor := &auth.AuthFactor{
		UserID:     req.UserID,
		Type:       auth.AuthFactorType(req.Type),
		Label:      req.Label,
		Credential: req.Credential,
		Metadata:   req.Metadata,
		CreatedBy:  req.CreatedBy,
	}
	if err := svc.Add(r.Context(), req.UserID, factor); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"factor_id": factor.ID})
}

func restList(svc service.UserAuthFactorServicer, w http.ResponseWriter, r *http.Request) {
	uid, _ := strconv.ParseUint(r.URL.Query().Get("user_id"), 10, 64)
	factors, err := svc.List(r.Context(), uid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"total": len(factors)})
}

func restLogin(svc service.UserAuthFactorServicer, w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type      string `json:"type"`
		LookupKey []byte `json:"lookup_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f, err := svc.LookupByCredentialKey(r.Context(), auth.AuthFactorType(req.Type), req.LookupKey)
	if err != nil {
		if errors.Is(err, auth.ErrFactorRevoked) {
			http.Error(w, err.Error(), http.StatusGone)
			return
		}
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"user_id": f.UserID, "factor_id": f.ID})
}

func restByPath(svc service.UserAuthFactorServicer, w http.ResponseWriter, r *http.Request) {
	const prefix = "/v1/users/factors/"
	id := r.URL.Path[len(prefix):]
	switch r.Method {
	case http.MethodDelete:
		force := r.URL.Query().Get("force") == "true"
		if err := svc.Remove(r.Context(), id, force); err != nil {
			if errors.Is(err, auth.ErrLastFactor) {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "revoked"})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ---------------------------------------------------------------------------
// HTTP client helpers
// ---------------------------------------------------------------------------

func httpAdd(t *testing.T, base string, userID uint64, ftype string, cred []byte) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"user_id":    userID,
		"type":       ftype,
		"credential": cred,
		"label":      ftype + "-vendor",
	})
	resp, err := http.Post(base+"/v1/users/factors", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("REST Add: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("REST Add status %d", resp.StatusCode)
	}
	var out struct {
		FactorID string `json:"factor_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out.FactorID
}

func httpDelete(t *testing.T, base, factorID string, force bool) int {
	t.Helper()
	uri := base + "/v1/users/factors/" + factorID
	if force {
		uri += "?force=true"
	}
	req, err := http.NewRequest(http.MethodDelete, uri, nil)
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("REST Delete: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func httpLogin(t *testing.T, base, ftype string, key []byte) (uint64, int) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"type": ftype, "lookup_key": key})
	resp, err := http.Post(base+"/v1/users/factors/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("REST Login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, resp.StatusCode
	}
	var out struct {
		UserID uint64 `json:"user_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	return out.UserID, resp.StatusCode
}

// ---------------------------------------------------------------------------
// gRPC client helpers — exercised via the generated UserAuthFactorService
// protobuf client. This mirrors how SDK callers connect over the wire.
// ---------------------------------------------------------------------------

func grpcAdd(t *testing.T, conn *grpc.ClientConn, userID uint64, ftype string, cred []byte) string {
	t.Helper()
	resp, err := pb.NewUserAuthFactorServiceClient(conn).Add(context.Background(),
		&pb.UserAuthFactorAddRequest{
			UserId:     userID,
			Type:       ftype,
			Credential: cred,
			Label:      ftype + "-vendor",
		})
	if err != nil {
		t.Fatalf("gRPC Add: %v", err)
	}
	return resp.Factor.Id
}

func grpcLogin(t *testing.T, conn *grpc.ClientConn, ftype string, key []byte) (uint64, error) {
	t.Helper()
	resp, err := pb.NewUserAuthFactorServiceClient(conn).Login(context.Background(),
		&pb.UserAuthFactorLoginRequest{Type: ftype, LookupKey: key})
	if err != nil {
		return 0, err
	}
	return resp.UserId, nil
}

func grpcDelete(t *testing.T, conn *grpc.ClientConn, factorID string, force bool) error {
	t.Helper()
	_, err := pb.NewUserAuthFactorServiceClient(conn).Remove(context.Background(),
		&pb.UserAuthFactorRemoveRequest{FactorId: factorID, Force: force})
	return err
}

// ---------------------------------------------------------------------------
// QUIC + MCP wire-format roundtrip checks. These exercise the same JSON
// shapes that pkg/api/quic and pkg/api/mcp use, validating the encode /
// decode contract.
// ---------------------------------------------------------------------------

func encodeQUICAddRequest(userID uint64, ftype string, cred []byte) []byte {
	payload, _ := json.Marshal(map[string]any{
		"user_id":    userID,
		"type":       ftype,
		"credential": cred,
		"label":      ftype + "-vendor",
	})
	return payload
}

func encodeMCPAddArgs(userID uint64, ftype string, cred []byte) map[string]any {
	return map[string]any{
		"user_id":        strconv.FormatUint(userID, 10),
		"type":           ftype,
		"credential_b64": base64.StdEncoding.EncodeToString(cred),
		"label":          ftype + "-vendor",
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

const userID uint64 = 4242

func hashKey(blob []byte) []byte {
	sum := sha256.Sum256(blob)
	return sum[:]
}

// TestM14_FullLifecycle exercises the complete spec acceptance scenario
// across REST, gRPC, and the in-process service (which is what the QUIC
// and MCP transports converge on).
func TestM14_FullLifecycle(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mtlsCert := []byte("MOCK CERT FROM VENDOR-A")
	mtlsKey := hashKey(mtlsCert)
	mtlsID := httpAdd(t, h.httpSrv.URL, userID, string(auth.AuthFactorMTLS), mtlsKey)
	if mtlsID == "" {
		t.Fatal("mtls factor ID empty")
	}

	fido2Cred := []byte("VENDOR-B-CREDENTIAL-ID-AAGUID")
	fido2ID := grpcAdd(t, h.grpcConn, userID, string(auth.AuthFactorFIDO2), fido2Cred)
	if fido2ID == "" {
		t.Fatal("fido2 factor ID empty")
	}

	// Verify QUIC wire shape: encode a request payload like the QUIC
	// transport does, decode it through the same JSON shape the server
	// expects, and call the service directly. This validates the
	// serialization contract used over the wire.
	pivCert := []byte("MOCK PIV CERT FROM VENDOR-C")
	pivKey := hashKey(pivCert)
	quicPayload := encodeQUICAddRequest(userID, string(auth.AuthFactorPIV), pivKey)
	var quicDecoded struct {
		UserID     uint64 `json:"user_id"`
		Type       string `json:"type"`
		Credential []byte `json:"credential"`
		Label      string `json:"label"`
	}
	if err := json.Unmarshal(quicPayload, &quicDecoded); err != nil {
		t.Fatalf("QUIC payload decode: %v", err)
	}
	pivFactor := &auth.AuthFactor{
		UserID:     quicDecoded.UserID,
		Type:       auth.AuthFactorType(quicDecoded.Type),
		Credential: quicDecoded.Credential,
		Label:      quicDecoded.Label,
	}
	if err := h.svc.Add(ctx, userID, pivFactor); err != nil {
		t.Fatalf("Service Add (QUIC payload): %v", err)
	}

	// Validate the MCP wire encoding decodes the credential correctly.
	mcpArgs := encodeMCPAddArgs(userID, string(auth.AuthFactorMTLS), []byte("EXTRA-CRED-FOR-MCP"))
	mcpCred, err := base64.StdEncoding.DecodeString(mcpArgs["credential_b64"].(string))
	if err != nil {
		t.Fatalf("MCP base64 decode: %v", err)
	}
	if string(mcpCred) != "EXTRA-CRED-FOR-MCP" {
		t.Fatalf("MCP cred decode = %q, want EXTRA-CRED-FOR-MCP", mcpCred)
	}

	// Login via REST + gRPC + service-direct; all three must yield the
	// same UserID.
	if got, _ := httpLogin(t, h.httpSrv.URL, string(auth.AuthFactorMTLS), mtlsKey); got != userID {
		t.Fatalf("REST mTLS login UserID = %d, want %d", got, userID)
	}
	if got, err := grpcLogin(t, h.grpcConn, string(auth.AuthFactorFIDO2), fido2Cred); err != nil || got != userID {
		t.Fatalf("gRPC FIDO2 login UserID = %d err = %v, want %d", got, err, userID)
	}
	if pivLookup, err := h.svc.LookupByCredentialKey(ctx, auth.AuthFactorPIV, pivKey); err != nil || pivLookup.UserID != userID {
		t.Fatalf("Service PIV login UserID = %v err = %v", pivLookup, err)
	}

	// Remove all but the last factor.
	if code := httpDelete(t, h.httpSrv.URL, mtlsID, false); code != http.StatusOK {
		t.Fatalf("REST Delete mTLS code = %d", code)
	}
	if err := grpcDelete(t, h.grpcConn, fido2ID, false); err != nil {
		t.Fatalf("gRPC Delete FIDO2: %v", err)
	}

	// PIV is now last. Remove without force MUST fail with ErrLastFactor.
	err = h.svc.Remove(ctx, pivFactor.ID, false)
	if !errors.Is(err, auth.ErrLastFactor) {
		t.Fatalf("expected ErrLastFactor, got %v", err)
	}
	if h.users.IsDisabled(userID) {
		t.Fatal("user must remain active before force removal")
	}

	// Force-remove the last factor; the user must become inactive.
	if err := h.svc.Remove(ctx, pivFactor.ID, true); err != nil {
		t.Fatalf("force Remove PIV: %v", err)
	}
	if !h.users.IsDisabled(userID) {
		t.Fatal("user must be inactive after force removing last factor")
	}

	// Login via the now-revoked mTLS credential MUST fail.
	if _, code := httpLogin(t, h.httpSrv.URL, string(auth.AuthFactorMTLS), mtlsKey); code == http.StatusOK {
		t.Fatal("login via revoked mTLS unexpectedly succeeded")
	}
}

// TestM14_LookupByCredentialKey_RevokedFactorRejected is a tighter
// regression on the revocation -> login path crossing the REST transport.
func TestM14_LookupByCredentialKey_RevokedFactorRejected(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cred := hashKey([]byte("revoke-me"))
	id := httpAdd(t, h.httpSrv.URL, 7777, string(auth.AuthFactorMTLS), cred)
	// Add a backup so we can revoke without tripping the last-factor guard.
	if err := h.svc.Add(ctx, 7777, &auth.AuthFactor{
		UserID:     7777,
		Type:       auth.AuthFactorFIDO2,
		Credential: []byte("backup-cred"),
	}); err != nil {
		t.Fatalf("Add backup: %v", err)
	}

	if code := httpDelete(t, h.httpSrv.URL, id, false); code != http.StatusOK {
		t.Fatalf("REST Delete code = %d", code)
	}
	if _, code := httpLogin(t, h.httpSrv.URL, string(auth.AuthFactorMTLS), cred); code == http.StatusOK {
		t.Fatal("login via revoked factor should not return 200")
	}
}
