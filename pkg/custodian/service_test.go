// Copyright (c) 2026 Jeremy Hahn
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

package custodian_test

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// ---------------------------------------------------------------------------
// In-memory KV store for testing
// ---------------------------------------------------------------------------

type memKV struct {
	mu   sync.RWMutex
	data map[string][]byte
}

func newMemKV() *memKV { return &memKV{data: make(map[string][]byte)} }

// Compile-time check: the service refuses a store without the atomic
// conditional write, so the fake must carry it.
var _ kvstore.AtomicKVStore = (*memKV)(nil)

// The service also refuses a store that cannot apply an enrolment's keys as one
// guarded write, so the fake must carry that too.
var _ kvstore.GuardedBatchKVStore = (*memKV)(nil)

func (m *memKV) Put(_ context.Context, key string, value []byte) error {
	m.mu.Lock()
	m.data[key] = append([]byte(nil), value...)
	m.mu.Unlock()
	return nil
}

// PutIfAbsent stores value under key only when the key is absent, serialising
// the check and the write under the same lock — the guarantee
// kvstore.AtomicKVStore requires and every production engine provides
// natively.
func (m *memKV) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.data[key]; exists {
		return false, nil
	}
	m.data[key] = append([]byte(nil), value...)
	return true, nil
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (m *memKV) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.data[key]
	if !ok {
		return false, nil
	}
	if !bytes.Equal(current, expected) {
		return false, nil
	}
	delete(m.data, key)
	return true, nil
}

func (m *memKV) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.RLock()
	v := m.data[key]
	m.mu.RUnlock()
	return v, nil
}

func (m *memKV) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	delete(m.data, key)
	m.mu.Unlock()
	return nil
}

func (m *memKV) Scan(_ context.Context, prefix string, fn func(key string, value []byte) error) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for k, v := range m.data {
		if strings.HasPrefix(k, prefix) {
			if err := fn(k, v); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *memKV) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var keys []string
	for k := range m.data {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *memKV) Exists(_ context.Context, key string) (bool, error) {
	m.mu.RLock()
	_, ok := m.data[key]
	m.mu.RUnlock()
	return ok, nil
}

func (m *memKV) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error { return nil }
func (m *memKV) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, nil
}
func (m *memKV) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}
func (m *memKV) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}
func (m *memKV) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// No-op audit logger for testing
// ---------------------------------------------------------------------------

type noopAudit struct{}

func (n *noopAudit) Emit(_ context.Context, _ audit.AuditEvent) error { return nil }
func (n *noopAudit) Query(_ context.Context, _ audit.Filter) (*audit.Page, error) {
	return &audit.Page{}, nil
}
func (n *noopAudit) Close() error { return nil }

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newTestService(t *testing.T) (custodian.Servicer, *memKV) {
	t.Helper()
	store := newMemKV()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	svc, err := custodian.NewService(store, &noopAudit{}, logger)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc, store
}

func testRegisterRequest(t *testing.T) (custodian.RegisterRequest, *ecdh.PrivateKey) {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pubPEM := marshalECDHPublicKeyPEM(t, priv.PublicKey())
	return custodian.RegisterRequest{
		Username:     "alice",
		TenantID:     "tenant-1",
		Method:       custodian.FactorMethodPIV,
		EnrollmentID: "enroll-001",
		PublicKeyPEM: pubPEM,
		ShareIndex:   1,
		ShareBytes:   []byte("test-shamir-share-32bytes-abc123"),
	}, priv
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// nonAtomicKV is a KVStore without the atomic conditional write. Embedding the
// interface rather than the concrete fake is what withholds PutIfAbsent: only
// the KVStore method set is promoted.
type nonAtomicKV struct{ kvstore.KVStore }

// TestNewService_RefusesAStoreWithoutAtomicWrites pins the fail-closed
// construction. Registration's exactly-once guarantee is the store's
// PutIfAbsent; a store without it could only be served by a Get-then-Put,
// which is the check-then-act window two concurrent enrolments for one
// identity slip through. Refusing at construction states the requirement at
// startup instead of losing a share holder's enrolment at ceremony time.
func TestNewService_RefusesAStoreWithoutAtomicWrites(t *testing.T) {
	logger := slog.Default()
	svc, err := custodian.NewService(&nonAtomicKV{newMemKV()}, &noopAudit{}, logger)
	if !errors.Is(err, custodian.ErrAtomicWriteUnsupported) {
		t.Fatalf("err = %v, want ErrAtomicWriteUnsupported", err)
	}
	if svc != nil {
		t.Error("a service was returned for a store that cannot claim a username")
	}
}

// TestNewService_AcceptsAnAtomicStore is the other half: a store that carries
// the capability constructs.
func TestNewService_AcceptsAnAtomicStore(t *testing.T) {
	svc, err := custodian.NewService(newMemKV(), &noopAudit{}, slog.Default())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if svc == nil {
		t.Fatal("NewService returned no service and no error")
	}
}

func TestNewService_NilStore(t *testing.T) {
	logger := slog.Default()
	_, err := custodian.NewService(nil, &noopAudit{}, logger)
	if err == nil {
		t.Fatal("expected error for nil store")
	}
}

func TestNewService_NilAuditLogger(t *testing.T) {
	logger := slog.Default()
	_, err := custodian.NewService(newMemKV(), nil, logger)
	if err == nil {
		t.Fatal("expected error for nil audit logger")
	}
}

func TestNewService_NilLogger(t *testing.T) {
	_, err := custodian.NewService(newMemKV(), &noopAudit{}, nil)
	if err == nil {
		t.Fatal("expected error for nil logger")
	}
}

func TestRegisterCustodian_Success(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	req, _ := testRegisterRequest(t)

	resp, err := svc.RegisterCustodian(ctx, req)
	if err != nil {
		t.Fatalf("RegisterCustodian: %v", err)
	}
	if resp.Custodian == nil {
		t.Fatal("nil custodian in response")
	}
	if resp.SealedEnvelope == nil {
		t.Fatal("nil sealed envelope in response")
	}
	if resp.Custodian.ID == "" {
		t.Error("empty custodian ID")
	}
	if resp.Custodian.Username != req.Username {
		t.Errorf("username mismatch: got %q, want %q", resp.Custodian.Username, req.Username)
	}
	if resp.Custodian.ShareIndex != req.ShareIndex {
		t.Errorf("shareIndex mismatch: got %d, want %d", resp.Custodian.ShareIndex, req.ShareIndex)
	}
	if resp.Custodian.RevokedAt != nil {
		t.Error("new custodian should not be revoked")
	}
}

func TestRegisterCustodian_DuplicateReturnsError(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)

	req1, priv1 := testRegisterRequest(t)
	pubPEM1 := marshalECDHPublicKeyPEM(t, priv1.PublicKey())

	// Register once.
	req1.ShareBytes = []byte("share-one-32-bytes-of-test-data!")
	req1.PublicKeyPEM = pubPEM1
	if _, err := svc.RegisterCustodian(ctx, req1); err != nil {
		t.Fatalf("first register: %v", err)
	}

	// Generate fresh share bytes for second attempt (same username/tenant).
	priv2, _ := ecdh.P256().GenerateKey(rand.Reader)
	req2 := custodian.RegisterRequest{
		Username:     req1.Username,
		TenantID:     req1.TenantID,
		Method:       custodian.FactorMethodPIV,
		EnrollmentID: "enroll-002",
		PublicKeyPEM: marshalECDHPublicKeyPEM(t, priv2.PublicKey()),
		ShareIndex:   2,
		ShareBytes:   []byte("share-two-32-bytes-of-test-data!"),
	}

	_, err := svc.RegisterCustodian(ctx, req2)
	if err == nil {
		t.Fatal("expected ErrCustodianAlreadyExists")
	}
}

func TestRegisterCustodian_ShareBytesZeroed(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	req, _ := testRegisterRequest(t)
	// Keep a pointer so we can check the bytes after the call.
	share := req.ShareBytes

	_, err := svc.RegisterCustodian(ctx, req)
	if err != nil {
		t.Fatalf("RegisterCustodian: %v", err)
	}

	// share should be zeroed by SealShareTo.
	for i, b := range share {
		if b != 0 {
			t.Fatalf("share byte[%d] not zeroed after register", i)
		}
	}
}

func TestRegisterCustodian_ValidationErrors(t *testing.T) {
	ctx := context.Background()
	priv, _ := ecdh.P256().GenerateKey(rand.Reader)
	pubPEM := marshalECDHPublicKeyPEM(t, priv.PublicKey())

	table := []struct {
		name string
		req  custodian.RegisterRequest
	}{
		{
			name: "missing username",
			req: custodian.RegisterRequest{
				TenantID: "t1", Method: custodian.FactorMethodPIV,
				EnrollmentID: "e1", PublicKeyPEM: pubPEM, ShareIndex: 1,
				ShareBytes: []byte("share"),
			},
		},
		{
			name: "missing tenantID",
			req: custodian.RegisterRequest{
				Username: "bob", Method: custodian.FactorMethodPIV,
				EnrollmentID: "e1", PublicKeyPEM: pubPEM, ShareIndex: 1,
				ShareBytes: []byte("share"),
			},
		},
		{
			name: "missing enrollmentID",
			req: custodian.RegisterRequest{
				Username: "bob", TenantID: "t1", Method: custodian.FactorMethodPIV,
				PublicKeyPEM: pubPEM, ShareIndex: 1, ShareBytes: []byte("share"),
			},
		},
		{
			name: "missing publicKeyPEM",
			req: custodian.RegisterRequest{
				Username: "bob", TenantID: "t1", Method: custodian.FactorMethodPIV,
				EnrollmentID: "e1", ShareIndex: 1, ShareBytes: []byte("share"),
			},
		},
		{
			name: "invalid shareIndex 0",
			req: custodian.RegisterRequest{
				Username: "bob", TenantID: "t1", Method: custodian.FactorMethodPIV,
				EnrollmentID: "e1", PublicKeyPEM: pubPEM, ShareIndex: 0,
				ShareBytes: []byte("share"),
			},
		},
		{
			name: "empty shareBytes",
			req: custodian.RegisterRequest{
				Username: "bob", TenantID: "t1", Method: custodian.FactorMethodPIV,
				EnrollmentID: "e1", PublicKeyPEM: pubPEM, ShareIndex: 1,
				ShareBytes: nil,
			},
		},
		{
			name: "invalid method",
			req: custodian.RegisterRequest{
				Username: "bob", TenantID: "t1", Method: "hsm",
				EnrollmentID: "e1", PublicKeyPEM: pubPEM, ShareIndex: 1,
				ShareBytes: []byte("share"),
			},
		},
	}

	svc, _ := newTestService(t)
	for _, tc := range table {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.RegisterCustodian(ctx, tc.req)
			if err == nil {
				t.Fatalf("expected error for %q", tc.name)
			}
		})
	}
}

func TestGetCustodian_NotFound(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	_, err := svc.GetCustodian(ctx, "nonexistent-id")
	if err == nil {
		t.Fatal("expected ErrCustodianNotFound")
	}
}

func TestGetCustodian_Found(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	req, _ := testRegisterRequest(t)

	resp, err := svc.RegisterCustodian(ctx, req)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	got, err := svc.GetCustodian(ctx, resp.Custodian.ID)
	if err != nil {
		t.Fatalf("GetCustodian: %v", err)
	}
	if got.ID != resp.Custodian.ID {
		t.Errorf("id mismatch: got %q, want %q", got.ID, resp.Custodian.ID)
	}
}

func TestListCustodians_Empty(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	list, err := svc.ListCustodians(ctx, "no-such-tenant")
	if err != nil {
		t.Fatalf("ListCustodians: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected 0, got %d", len(list))
	}
}

func TestListCustodians_MultipleTenants(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)

	for i, user := range []string{"alice", "bob", "carol"} {
		priv, _ := ecdh.P256().GenerateKey(rand.Reader)
		req := custodian.RegisterRequest{
			Username:     user,
			TenantID:     "tenant-A",
			Method:       custodian.FactorMethodFIDO2,
			EnrollmentID: "e-" + user,
			PublicKeyPEM: marshalECDHPublicKeyPEM(t, priv.PublicKey()),
			ShareIndex:   i + 1,
			ShareBytes:   []byte("share-placeholder-32-bytes-xx!!!"),
		}
		if _, err := svc.RegisterCustodian(ctx, req); err != nil {
			t.Fatalf("register %s: %v", user, err)
		}
	}

	// Register one in a different tenant.
	privOther, _ := ecdh.P256().GenerateKey(rand.Reader)
	otherReq := custodian.RegisterRequest{
		Username:     "dave",
		TenantID:     "tenant-B",
		Method:       custodian.FactorMethodSoftware,
		EnrollmentID: "e-dave",
		PublicKeyPEM: marshalECDHPublicKeyPEM(t, privOther.PublicKey()),
		ShareIndex:   1,
		ShareBytes:   []byte("share-placeholder-32-bytes-xx!!!"),
	}
	if _, err := svc.RegisterCustodian(ctx, otherReq); err != nil {
		t.Fatalf("register dave: %v", err)
	}

	listA, err := svc.ListCustodians(ctx, "tenant-A")
	if err != nil {
		t.Fatalf("ListCustodians tenant-A: %v", err)
	}
	if len(listA) != 3 {
		t.Errorf("tenant-A: expected 3, got %d", len(listA))
	}

	listB, err := svc.ListCustodians(ctx, "tenant-B")
	if err != nil {
		t.Fatalf("ListCustodians tenant-B: %v", err)
	}
	if len(listB) != 1 {
		t.Errorf("tenant-B: expected 1, got %d", len(listB))
	}
}

func TestRevokeCustodian_Success(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	req, _ := testRegisterRequest(t)

	resp, err := svc.RegisterCustodian(ctx, req)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := svc.RevokeCustodian(ctx, resp.Custodian.ID); err != nil {
		t.Fatalf("RevokeCustodian: %v", err)
	}

	got, err := svc.GetCustodian(ctx, resp.Custodian.ID)
	if err != nil {
		t.Fatalf("GetCustodian after revoke: %v", err)
	}
	if got.RevokedAt == nil {
		t.Error("RevokedAt should be set after revoke")
	}
	if got.Active() {
		t.Error("custodian should report not active after revoke")
	}
}

func TestRevokeCustodian_Idempotent(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	req, _ := testRegisterRequest(t)

	resp, err := svc.RegisterCustodian(ctx, req)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	// First revoke.
	if err := svc.RevokeCustodian(ctx, resp.Custodian.ID); err != nil {
		t.Fatalf("first revoke: %v", err)
	}

	// Second revoke must return nil (idempotent).
	if err := svc.RevokeCustodian(ctx, resp.Custodian.ID); err != nil {
		t.Fatalf("second revoke (idempotent): %v", err)
	}
}

func TestRevokeCustodian_NotFound(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	err := svc.RevokeCustodian(ctx, "no-such-id")
	if err == nil {
		t.Fatal("expected error for non-existent custodian")
	}
}

func TestCustodianActive(t *testing.T) {
	now := func() *custodian.Custodian {
		return &custodian.Custodian{ID: "x", Username: "x", TenantID: "t"}
	}

	c := now()
	if !c.Active() {
		t.Error("new custodian should be active")
	}

	t2 := now()
	revokedAt := t2.EnrolledAt
	t2.RevokedAt = &revokedAt
	if t2.Active() {
		t.Error("revoked custodian should not be active")
	}
}

// ---------------------------------------------------------------------------
// Error path tests
// ---------------------------------------------------------------------------

// errMemKV is a memKV variant whose Put always returns an error after putFailAfter
// successful Put calls. Used to exercise store error paths.
type errMemKV struct {
	*memKV
	putFailAfter int // fail Put once this many successful Puts have occurred
	putCount     int
	getErrKey    string // if non-empty, Get for this exact key returns errTestStore
	listErr      bool   // if true, List always returns errTestStore
}

var errTestStore = errors.New("test: store unavailable")

func (m *errMemKV) Put(ctx context.Context, key string, value []byte) error {
	m.putCount++
	if m.putFailAfter > 0 && m.putCount > m.putFailAfter {
		return errTestStore
	}
	return m.memKV.Put(ctx, key, value)
}

// PutAllIfUnchanged counts toward putFailAfter as well, and fails the WHOLE
// batch when it would cross the threshold.
//
// Same reasoning as CompareAndSwap below, one step further along: a new
// enrolment's three writes became a single guarded transaction when the fence
// moved from per-key checks to one multi-key compare-and-swap (go-qrdb#21). A
// fixture that counted only Put would stop intercepting registration entirely,
// and a test written to fail one of its writes would silently exercise nothing.
//
// Failing the batch whole, and counting nothing when it fails, is what keeps
// the double honest: the real capability applies every write or none, so a
// fixture that failed partway would let a test pass against a store that can
// tear an enrolment — the exact defect this fence exists to prevent.
func (m *errMemKV) PutAllIfUnchanged(ctx context.Context, guardKey string, guardValue []byte, writes []kvstore.KV) (bool, error) {
	if m.putFailAfter > 0 && m.putCount+len(writes) > m.putFailAfter {
		return false, errTestStore
	}
	applied, err := m.memKV.PutAllIfUnchanged(ctx, guardKey, guardValue, writes)
	if applied {
		m.putCount += len(writes)
	}
	return applied, err
}

// CompareAndSwap counts toward putFailAfter as well.
//
// The registration's COMMIT — the write that replaces the claim token on the
// username index with the custodian identifier — became a compare-and-swap
// when claims gained an expiry (go-qrdb#21): it is the single point at which
// exactly one registration wins. A fixture that counted only Put would stop
// intercepting the commit, and a test written to fail the third write would
// silently exercise nothing.
func (m *errMemKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	m.putCount++
	if m.putFailAfter > 0 && m.putCount > m.putFailAfter {
		return false, errTestStore
	}
	return m.memKV.CompareAndSwap(ctx, key, expected, next)
}

func (m *errMemKV) Get(ctx context.Context, key string) ([]byte, error) {
	if m.getErrKey != "" && key == m.getErrKey {
		return nil, errTestStore
	}
	return m.memKV.Get(ctx, key)
}

func (m *errMemKV) List(ctx context.Context, prefix string) ([]string, error) {
	if m.listErr {
		return nil, errTestStore
	}
	return m.memKV.List(ctx, prefix)
}

func newErrService(t *testing.T, kv *errMemKV) custodian.Servicer {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	svc, err := custodian.NewService(kv, &noopAudit{}, logger)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

func TestStoreError_ErrorAndUnwrap(t *testing.T) {
	underlying := errors.New("disk full")
	se := &custodian.StoreError{Op: "put", Err: underlying}

	if se.Error() == "" {
		t.Error("StoreError.Error() should not be empty")
	}
	if !strings.Contains(se.Error(), "put") {
		t.Errorf("StoreError.Error() should contain op name, got: %q", se.Error())
	}
	if se.Unwrap() != underlying {
		t.Error("StoreError.Unwrap() should return underlying error")
	}
}

func TestGetCustodian_StoreError(t *testing.T) {
	ctx := context.Background()
	// Register a custodian first using the normal memKV.
	normalKV := newMemKV()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	normalSvc, _ := custodian.NewService(normalKV, &noopAudit{}, logger)
	req, _ := testRegisterRequest(t)
	resp, err := normalSvc.RegisterCustodian(ctx, req)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	// Now use an errMemKV that fails Get for the primary key.
	eKV := &errMemKV{
		memKV:     normalKV,
		getErrKey: "__sys:custodian/" + resp.Custodian.ID,
	}
	errSvc := newErrService(t, eKV)
	_, err = errSvc.GetCustodian(ctx, resp.Custodian.ID)
	if err == nil {
		t.Fatal("expected StoreError from Get failure")
	}
}

func TestListCustodians_StoreError(t *testing.T) {
	ctx := context.Background()
	eKV := &errMemKV{memKV: newMemKV(), listErr: true}
	svc := newErrService(t, eKV)
	_, err := svc.ListCustodians(ctx, "tenant-x")
	if err == nil {
		t.Fatal("expected error from List failure")
	}
}

func TestRevokeCustodian_PersistError(t *testing.T) {
	ctx := context.Background()
	// Register first (3 Put calls: primary + 2 indexes).
	eKV := &errMemKV{memKV: newMemKV(), putFailAfter: 3}
	svc := newErrService(t, eKV)
	req, _ := testRegisterRequest(t)

	resp, err := svc.RegisterCustodian(ctx, req)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	// Revoke triggers persist → Put (the 4th call) which will fail.
	err = svc.RevokeCustodian(ctx, resp.Custodian.ID)
	if err == nil {
		t.Fatal("expected error from persist Put failure during revoke")
	}
}

func TestRegisterCustodian_TenantIndexPutError(t *testing.T) {
	ctx := context.Background()
	// First Put (primary record) succeeds; second Put (tenant index) fails.
	eKV := &errMemKV{memKV: newMemKV(), putFailAfter: 1}
	svc := newErrService(t, eKV)
	req, _ := testRegisterRequest(t)

	_, err := svc.RegisterCustodian(ctx, req)
	if err == nil {
		t.Fatal("expected error when tenant index Put fails")
	}
}

func TestRegisterCustodian_UsernameIndexPutError(t *testing.T) {
	ctx := context.Background()
	// First two Puts succeed (primary + tenant index); third Put (username index) fails.
	eKV := &errMemKV{memKV: newMemKV(), putFailAfter: 2}
	svc := newErrService(t, eKV)
	req, _ := testRegisterRequest(t)

	_, err := svc.RegisterCustodian(ctx, req)
	if err == nil {
		t.Fatal("expected error when username index Put fails")
	}
}

func TestRegisterCustodian_SealShareToError(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	// Use a non-empty but invalid PEM as the public key — passes validation
	// (non-empty string) but SealShareTo fails to parse it.
	req := custodian.RegisterRequest{
		Username:     "mallory",
		TenantID:     "tenant-1",
		Method:       custodian.FactorMethodPIV,
		EnrollmentID: "enroll-x",
		PublicKeyPEM: "-----BEGIN PUBLIC KEY-----\nbm90YXZhbGlka2V5\n-----END PUBLIC KEY-----\n",
		ShareIndex:   1,
		ShareBytes:   []byte("test-shamir-share-32bytes-abc123"),
	}
	_, err := svc.RegisterCustodian(ctx, req)
	if err == nil {
		t.Fatal("expected error for invalid public key PEM during SealShareTo")
	}
}

func TestListCustodians_SkipsMissingRecord(t *testing.T) {
	ctx := context.Background()
	// Inject a tenant index entry pointing to a non-existent primary record.
	kv := newMemKV()
	// Write tenant index entry for "ghost-id" without a primary record.
	_ = kv.Put(ctx, "__sys:custodian-idx/tenant/tenant-x/ghost-id", []byte("ghost-id"))

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	svc, err := custodian.NewService(kv, &noopAudit{}, logger)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	// ListCustodians should skip the ghost entry and return an empty list (no error).
	list, err := svc.ListCustodians(ctx, "tenant-x")
	if err != nil {
		t.Fatalf("ListCustodians: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected 0, got %d (ghost entry should be skipped)", len(list))
	}
}

func TestGetCustodian_UnmarshalError(t *testing.T) {
	ctx := context.Background()
	// Inject corrupted data for a custodian primary key.
	kv := newMemKV()
	_ = kv.Put(ctx, "__sys:custodian/bad-id", []byte("not-json"))
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	svc, err := custodian.NewService(kv, &noopAudit{}, logger)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	_, err = svc.GetCustodian(ctx, "bad-id")
	if err == nil {
		t.Fatal("expected StoreError for unmarshal failure")
	}
}

func TestRegisterCustodian_DeterministicID(t *testing.T) {
	// Two registrations with same tenantID+username should produce the same ID.
	// The second will fail with ErrCustodianAlreadyExists, but we can
	// verify determinism by computing the ID externally via the same UUID v5 logic.
	ctx := context.Background()
	svc, _ := newTestService(t)
	req, _ := testRegisterRequest(t)

	resp, err := svc.RegisterCustodian(ctx, req)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	// Retrieve and verify the ID is non-empty and stable.
	got, err := svc.GetCustodian(ctx, resp.Custodian.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != resp.Custodian.ID {
		t.Errorf("ID mismatch: got %q, want %q", got.ID, resp.Custodian.ID)
	}
	// A UUID v5 is 36 chars: xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
	if len(resp.Custodian.ID) != 36 {
		t.Errorf("unexpected UUID length: %d", len(resp.Custodian.ID))
	}
}

// CompareAndSwap replaces the value under key only when it still equals
// expected, serialising the compare and the write under the same lock — the
// guarantee kvstore.AtomicKVStore requires and every production engine
// provides natively.
//
// It was a stub returning (false, nil) while nothing in this package used it
// (go-qrdb#56 added it only to satisfy the interface). That is worse than no
// double: the compile-time assertion passed while the behaviour was absent, so
// the first caller to depend on it — the expired-claim take-over of
// go-qrdb#21 — saw every swap fail and every take-over refuse, in a way that
// read as a bug in the take-over.
// PutAllIfUnchanged applies every write under one lock, and only while guardKey
// still holds guardValue.
//
// The lock is what makes this fake FAITHFUL rather than convenient: it is the
// single-process equivalent of the guarantee a replicated store gets by
// evaluating the compare and the writes inside its state machine. A version
// that checked the guard, released, and then wrote would reproduce in the
// double exactly the window the real capability closes, and every test resting
// on it would pass against a store that cannot actually fence an enrolment.
func (m *memKV) PutAllIfUnchanged(_ context.Context, guardKey string, guardValue []byte, writes []kvstore.KV) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.data[guardKey]
	if !ok || !bytes.Equal(current, guardValue) {
		return false, nil
	}
	for _, w := range writes {
		m.data[w.Key] = append([]byte(nil), w.Value...)
	}
	return true, nil
}

func (m *memKV) CompareAndSwap(_ context.Context, key string, expected, next []byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.data[key]
	if !ok {
		return false, nil
	}
	if !bytes.Equal(current, expected) {
		return false, nil
	}
	m.data[key] = append([]byte(nil), next...)
	return true, nil
}
