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

//go:build codec_json && raft

package server

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"log/slog"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// The tests in this file are the only pkg/server tests that reach the
// option-driven branches of Start on every protocol and the stores of
// WithClusterKeyReceiver, WithBackupService, WithRebalanceService and
// WithEmbedding. The doubles below exist for them.

// extraSealedWindowProbe implements SealedWindowProbe with a configurable
// sealed state.
type extraSealedWindowProbe struct{ sealed bool }

func (p *extraSealedWindowProbe) IsSealed() bool { return p.sealed }

var _ SealedWindowProbe = (*extraSealedWindowProbe)(nil)

// extraLockService is a minimal service.LockServicer for option tests.
type extraLockService struct{ service.LockServicer }

// extraLockImpl provides concrete method bodies so LockServicer is satisfied
// without panicking on nil-pointer dereferences from the embedded nil.
type extraLockImpl struct{}

func (e *extraLockImpl) Lock(_ context.Context, _, _ string, _ time.Duration, _ bool) (*lock.Lock, error) {
	return nil, nil
}
func (e *extraLockImpl) TryLock(_ context.Context, _, _ string, _ time.Duration, _ bool) (*lock.Lock, error) {
	return nil, nil
}
func (e *extraLockImpl) Unlock(_ context.Context, _ string, _ int64) error { return nil }
func (e *extraLockImpl) Extend(_ context.Context, _ string, _ int64) (*lock.Lock, error) {
	return nil, nil
}
func (e *extraLockImpl) Status(_ context.Context, _ string) (*lock.Lock, error) { return nil, nil }
func (e *extraLockImpl) List(_ context.Context, _ string) ([]*lock.Lock, error) { return nil, nil }

var _ service.LockServicer = (*extraLockImpl)(nil)

// extraWebUI satisfies http.Handler for WebUI option tests.
type extraWebUI struct{}

func (w *extraWebUI) ServeHTTP(_ http.ResponseWriter, _ *http.Request) {}

var _ http.Handler = (*extraWebUI)(nil)

// extraAuditLogger satisfies audit.AuditLogger for option tests.
type extraAuditLogger struct{}

func (a *extraAuditLogger) Emit(_ context.Context, _ audit.AuditEvent) error { return nil }
func (a *extraAuditLogger) Query(_ context.Context, _ audit.Filter) (*audit.Page, error) {
	return nil, nil
}
func (a *extraAuditLogger) Close() error { return nil }

var _ audit.AuditLogger = (*extraAuditLogger)(nil)

// extraAuthFactorService satisfies service.AuthFactorServicer via embedding.
type extraAuthFactorService struct{ service.AuthFactorServicer }

// extraKVPassthrough is a KVServicer that records the last key for assertions.
type extraKVPassthrough struct {
	store   map[string][]byte
	lastKey string
}

func newExtraKVPassthrough() *extraKVPassthrough {
	return &extraKVPassthrough{store: make(map[string][]byte)}
}

func (k *extraKVPassthrough) Put(_ context.Context, key string, value []byte) error {
	k.lastKey = key
	k.store[key] = value
	return nil
}
func (k *extraKVPassthrough) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	k.lastKey = key
	if _, exists := k.store[key]; exists {
		return false, nil
	}
	k.store[key] = value
	return true, nil
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (k *extraKVPassthrough) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	current, ok := k.store[key]
	if !ok {
		return false, nil
	}
	if !bytes.Equal(current, expected) {
		return false, nil
	}
	delete(k.store, key)
	return true, nil
}
func (k *extraKVPassthrough) Get(_ context.Context, key string) ([]byte, error) {
	k.lastKey = key
	v, ok := k.store[key]
	if !ok {
		return nil, &service.KeyNotFoundError{Key: key}
	}
	return v, nil
}
func (k *extraKVPassthrough) Delete(_ context.Context, key string) error {
	k.lastKey = key
	delete(k.store, key)
	return nil
}
func (k *extraKVPassthrough) List(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	for key := range k.store {
		if prefix == "" || len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			keys = append(keys, key)
		}
	}
	return keys, nil
}
func (k *extraKVPassthrough) Scan(_ context.Context, prefix string) (map[string][]byte, error) {
	result := make(map[string][]byte)
	for key, val := range k.store {
		if prefix == "" || len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			result[key] = val
		}
	}
	return result, nil
}
func (k *extraKVPassthrough) GetStale(ctx context.Context, key string) ([]byte, error) {
	return k.Get(ctx, key)
}
func (k *extraKVPassthrough) ListStale(ctx context.Context, prefix string) ([]string, error) {
	return k.List(ctx, prefix)
}
func (k *extraKVPassthrough) ScanStale(ctx context.Context, prefix string) (map[string][]byte, error) {
	return k.Scan(ctx, prefix)
}
func (k *extraKVPassthrough) PutWithMode(_ context.Context, key string, value []byte, _ writemode.Mode) error {
	k.lastKey = key
	k.store[key] = value
	return nil
}
func (k *extraKVPassthrough) DeleteWithMode(_ context.Context, key string, _ writemode.Mode) error {
	k.lastKey = key
	delete(k.store, key)
	return nil
}
func (k *extraKVPassthrough) GetWithConsistency(_ context.Context, key string, _ consistency.ReadOptions) ([]byte, error) {
	v, ok := k.store[key]
	if !ok {
		return nil, &service.KeyNotFoundError{Key: key}
	}
	return v, nil
}
func (k *extraKVPassthrough) ListWithConsistency(_ context.Context, prefix string, _ consistency.ReadOptions) ([]string, error) {
	return k.List(context.Background(), prefix)
}
func (k *extraKVPassthrough) ScanWithConsistency(_ context.Context, prefix string, _ consistency.ReadOptions) (map[string][]byte, error) {
	return k.Scan(context.Background(), prefix)
}
func (k *extraKVPassthrough) ExistsWithConsistency(_ context.Context, key string, _ consistency.ReadOptions) (bool, error) {
	_, ok := k.store[key]
	return ok, nil
}
func (k *extraKVPassthrough) PutEntity(_ context.Context, req *service.PutEntityRequest) (*service.PutEntityResponse, error) {
	k.lastKey = req.ID
	return &service.PutEntityResponse{}, nil
}
func (k *extraKVPassthrough) GetEntity(_ context.Context, req *service.GetEntityRequest) (*service.GetEntityResponse, error) {
	k.lastKey = req.ID
	return &service.GetEntityResponse{}, nil
}
func (k *extraKVPassthrough) DeleteEntity(_ context.Context, req *service.DeleteEntityRequest) (*service.DeleteEntityResponse, error) {
	k.lastKey = req.ID
	return &service.DeleteEntityResponse{}, nil
}
func (k *extraKVPassthrough) ListEntities(_ context.Context, req *service.ListEntitiesRequest) (*service.ListEntitiesResponse, error) {
	// Return keys that match the prefix, already stored.
	var keys []string
	for key := range k.store {
		if req.Prefix == "" || len(key) >= len(req.Prefix) && key[:len(req.Prefix)] == req.Prefix {
			keys = append(keys, key)
		}
	}
	return &service.ListEntitiesResponse{Keys: keys, TotalCount: int64(len(keys))}, nil
}

var _ service.KVServicer = (*extraKVPassthrough)(nil)

// extraFailingKV returns a sentinel error from every method.
type extraFailingKV struct {
	err error
}

func (f *extraFailingKV) Put(_ context.Context, _ string, _ []byte) error { return f.err }
func (f *extraFailingKV) PutIfAbsent(_ context.Context, _ string, _ []byte) (bool, error) {
	return false, f.err
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (f *extraFailingKV) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	return false, nil
}
func (f *extraFailingKV) Get(_ context.Context, _ string) ([]byte, error)    { return nil, f.err }
func (f *extraFailingKV) Delete(_ context.Context, _ string) error           { return f.err }
func (f *extraFailingKV) List(_ context.Context, _ string) ([]string, error) { return nil, f.err }
func (f *extraFailingKV) Scan(_ context.Context, _ string) (map[string][]byte, error) {
	return nil, f.err
}
func (f *extraFailingKV) GetStale(_ context.Context, _ string) ([]byte, error)    { return nil, f.err }
func (f *extraFailingKV) ListStale(_ context.Context, _ string) ([]string, error) { return nil, f.err }
func (f *extraFailingKV) ScanStale(_ context.Context, _ string) (map[string][]byte, error) {
	return nil, f.err
}
func (f *extraFailingKV) PutWithMode(_ context.Context, _ string, _ []byte, _ writemode.Mode) error {
	return f.err
}
func (f *extraFailingKV) DeleteWithMode(_ context.Context, _ string, _ writemode.Mode) error {
	return f.err
}
func (f *extraFailingKV) GetWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) ([]byte, error) {
	return nil, f.err
}
func (f *extraFailingKV) ListWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) ([]string, error) {
	return nil, f.err
}
func (f *extraFailingKV) ScanWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (map[string][]byte, error) {
	return nil, f.err
}
func (f *extraFailingKV) ExistsWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (bool, error) {
	return false, f.err
}
func (f *extraFailingKV) PutEntity(_ context.Context, _ *service.PutEntityRequest) (*service.PutEntityResponse, error) {
	return nil, f.err
}
func (f *extraFailingKV) GetEntity(_ context.Context, _ *service.GetEntityRequest) (*service.GetEntityResponse, error) {
	return nil, f.err
}
func (f *extraFailingKV) DeleteEntity(_ context.Context, _ *service.DeleteEntityRequest) (*service.DeleteEntityResponse, error) {
	return nil, f.err
}
func (f *extraFailingKV) ListEntities(_ context.Context, _ *service.ListEntitiesRequest) (*service.ListEntitiesResponse, error) {
	return nil, f.err
}

var _ service.KVServicer = (*extraFailingKV)(nil)

// extraKVError is a typed error for error propagation assertions.
type extraKVError struct{ msg string }

func (e *extraKVError) Error() string { return e.msg }

// extraListEntitiesKV returns a fixed ListEntities response for prefix-strip tests.
type extraListEntitiesKV struct {
	*extraKVPassthrough
}

func (l *extraListEntitiesKV) ListEntities(_ context.Context, _ *service.ListEntitiesRequest) (*service.ListEntitiesResponse, error) {
	return &service.ListEntitiesResponse{
		Keys:       []string{"ns/entity/1", "ns/entity/2"},
		TotalCount: 2,
	}, nil
}

// ---------------------------------------------------------------------------
// namespacedKVService — complete method coverage
// (mirrors namespaced_kv_test.go but compiles under codec_json && raft only)
// ---------------------------------------------------------------------------

func newExtraFilledRegistry() *DatabaseServiceRegistry {
	reg := NewDatabaseServiceRegistry("default")
	reg.Register("default", &DatabaseServices{Name: "default", KV: newMockKVService()})
	reg.Register("mydb", &DatabaseServices{Name: "mydb", KV: newMockKVService()})
	return reg
}

// ---------------------------------------------------------------------------
// server.go — New() with both positional params nil and option-based override
// ---------------------------------------------------------------------------

func TestStart_AllFiveProtocols_WithRemainingBranchOptions(t *testing.T) {
	socketPath := t.TempDir() + "/extra.sock"
	tlsCfg := selfSignedTLSConfig(t)
	cfg := testConfigFull(t, protocolFlags{
		rest: true,
		grpc: true,
		quic: true,
		unix: true,
		mcp:  true,
	}, socketPath)

	probe := &extraSealedWindowProbe{sealed: false}
	lockSvc := &extraLockImpl{}
	embSvc := &mockEmbeddingService{}
	ragSvc := &mockRAGService{}
	auditLog := &extraAuditLogger{}
	webUI := &extraWebUI{}
	authFactorSvc := &extraAuthFactorService{}
	tenantSvc := &mockTenantServicer{}
	dbReg := newExtraFilledRegistry()
	barrierReg := &fakeBarrierRegistry{}

	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithTLS(tlsCfg),
		WithSealedWindowProbe(probe),
		WithLockService(lockSvc),
		WithEmbedding(embSvc),
		WithRAG(ragSvc),
		WithAuditLogger(auditLog),
		WithWebUI(webUI),
		WithAuthFactorService(authFactorSvc),
		WithTenantService(tenantSvc),
		WithDatabaseServiceRegistry(dbReg),
		WithBarrierRegistry(barrierReg),
	)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start with remaining branch options failed: %v", err)
	}
	defer srv.Stop()

	if srv.RESTAddr() == "" {
		t.Fatal("expected non-empty RESTAddr")
	}
	if srv.GRPCAddr() == "" {
		t.Fatal("expected non-empty GRPCAddr")
	}
	if srv.QUICAddr() == "" {
		t.Fatal("expected non-empty QUICAddr")
	}
	if srv.UnixAddr() == "" {
		t.Fatal("expected non-empty UnixAddr")
	}
	if srv.MCPAddr() == "" {
		t.Fatal("expected non-empty MCPAddr")
	}
}

// fakeBarrierRegistry is a fake *seal.BarrierRegistry-shaped value.
// Because *seal.BarrierRegistry is a concrete struct (not an interface),
// and constructing a real one requires storage infra, we pass a nil
// *seal.BarrierRegistry via the option instead so the option wiring is
// exercised without needing a real registry. The nil value is safe because
// the server only passes it to NewRESTServiceRegistry, which guards on nil.
//
// This helper is kept so the option assignment path is covered by the
// WithBarrierRegistry call in New().
type fakeBarrierRegistry = seal.BarrierRegistry

// TestStart_AllFiveProtocols_WithPINBackend exercises the pinBackend
// if-branches in startREST, startGRPC, startQUIC, startUnix, and startMCP.
func TestStart_AllFiveProtocols_WithPINBackend(t *testing.T) {
	socketPath := t.TempDir() + "/pin.sock"
	tlsCfg := selfSignedTLSConfig(t)
	cfg := testConfigFull(t, protocolFlags{
		rest: true,
		grpc: true,
		quic: true,
		unix: true,
		mcp:  true,
	}, socketPath)

	backend, err := pin.NewSoftwareBackend(nil, pin.DefaultHashConfig())
	if err != nil {
		t.Fatalf("NewSoftwareBackend: %v", err)
	}

	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithTLS(tlsCfg),
		WithPINBackend(backend),
	)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start with PINBackend failed: %v", err)
	}
	defer srv.Stop()

	if srv.RESTAddr() == "" {
		t.Fatal("expected non-empty RESTAddr")
	}
	if srv.GRPCAddr() == "" {
		t.Fatal("expected non-empty GRPCAddr")
	}
	if srv.QUICAddr() == "" {
		t.Fatal("expected non-empty QUICAddr")
	}
	if srv.UnixAddr() == "" {
		t.Fatal("expected non-empty UnixAddr")
	}
	if srv.MCPAddr() == "" {
		t.Fatal("expected non-empty MCPAddr")
	}
}

// ---------------------------------------------------------------------------
// SOAuthenticator — cover the WithSOAuthenticator branch in startREST/startGRPC
// by constructing a concrete *auth.SOAuthenticator and starting the REST server.
// ---------------------------------------------------------------------------

// stubKVStore implements kvstore.KVStore for constructing auth.SOAuthenticator
// in tests without a real storage backend.
type stubKVStore struct{}

func (s *stubKVStore) Put(_ context.Context, _ string, _ []byte) error { return nil }
func (s *stubKVStore) Get(_ context.Context, _ string) ([]byte, error) { return nil, nil }
func (s *stubKVStore) Delete(_ context.Context, _ string) error        { return nil }
func (s *stubKVStore) Scan(_ context.Context, _ string, _ func(string, []byte) error) error {
	return nil
}
func (s *stubKVStore) List(_ context.Context, _ string) ([]string, error)            { return nil, nil }
func (s *stubKVStore) Exists(_ context.Context, _ string) (bool, error)              { return false, nil }
func (s *stubKVStore) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error { return nil }
func (s *stubKVStore) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, nil
}
func (s *stubKVStore) ScanIndex(_ context.Context, _ string, _ []byte, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}
func (s *stubKVStore) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}
func (s *stubKVStore) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _ []byte, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

var _ kvstore.KVStore = (*stubKVStore)(nil)

// TestStart_REST_WithSOAuthenticator exercises the WithSOAuthenticator
// if-branch inside startREST.
func TestStart_REST_WithSOAuthenticator(t *testing.T) {
	store := &stubKVStore{}
	auditLog := &extraAuditLogger{}
	soAuth, err := auth.NewSOAuthenticator(store, auditLog, slog.Default())
	if err != nil {
		t.Fatalf("NewSOAuthenticator: %v", err)
	}

	cfg := testConfig(t, true, false) // REST only
	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithSOAuthenticator(soAuth),
		WithCeremonyCA(testServerCA),
	)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start with SOAuthenticator failed: %v", err)
	}
	defer srv.Stop()

	if srv.RESTAddr() == "" {
		t.Fatal("expected non-empty RESTAddr")
	}
	if srv.opts.soAuthenticator != soAuth {
		t.Error("soAuthenticator not stored in opts")
	}
}

func TestWithClusterKeyReceiver_NonNil(t *testing.T) {
	t.Parallel()
	rcv := &stubClusterKeyReceiver{}
	var opts serverOptions
	WithClusterKeyReceiver(rcv)(&opts)
	if opts.clusterKeyReceiver != rcv {
		t.Error("WithClusterKeyReceiver did not store provided receiver")
	}
}

func TestWithBackupService_NonNil(t *testing.T) {
	t.Parallel()
	svc := &stubBackupService{}
	var opts serverOptions
	WithBackupService(svc)(&opts)
	if opts.backupSvc != svc {
		t.Error("WithBackupService did not store provided service")
	}
}

func TestWithRebalanceService_NonNil(t *testing.T) {
	t.Parallel()
	svc := &stubRebalanceService{}
	var opts serverOptions
	WithRebalanceService(svc)(&opts)
	if opts.rebalanceSvc != svc {
		t.Error("WithRebalanceService did not store provided service")
	}
}

func TestWithEmbedding(t *testing.T) {
	embSvc := &mockEmbeddingService{}
	cfg := testConfig(t, false, false)

	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithEmbedding(embSvc))

	if srv.opts.embeddingSvc != embSvc {
		t.Error("embeddingSvc should be the same instance passed to WithEmbedding")
	}
}

type mockEmbeddingService struct{}

func (m *mockEmbeddingService) Embed(_ context.Context, _ *service.EmbedRequest) (*service.EmbedResponse, error) {
	return nil, nil
}
func (m *mockEmbeddingService) BatchEmbed(_ context.Context, _ *service.BatchEmbedRequest) (*service.BatchEmbedResponse, error) {
	return nil, nil
}
func (m *mockEmbeddingService) ListModels(_ context.Context, _ *service.ListEmbeddingModelsRequest) (*service.ListEmbeddingModelsResponse, error) {
	return nil, nil
}
func (m *mockEmbeddingService) RegisterProvider(_ string, _ service.EmbeddingProvider) error {
	return nil
}
func (m *mockEmbeddingService) DeregisterProvider(_ string) error { return nil }
func (m *mockEmbeddingService) Close() error                      { return nil }

type mockRAGService struct{}

func (m *mockRAGService) IngestDocument(_ context.Context, _ *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error) {
	return nil, nil
}
func (m *mockRAGService) Query(_ context.Context, _ *service.RAGQueryRequest) (*service.RAGQueryResponse, error) {
	return nil, nil
}
func (m *mockRAGService) DeleteDocument(_ context.Context, _ *service.DeleteRAGDocumentRequest) (*service.DeleteRAGDocumentResponse, error) {
	return nil, nil
}
func (m *mockRAGService) ListDocuments(_ context.Context, _ *service.ListRAGDocumentsRequest) (*service.ListRAGDocumentsResponse, error) {
	return nil, nil
}
func (m *mockRAGService) Close() error { return nil }

type stubClusterKeyReceiver struct{}

func (s *stubClusterKeyReceiver) Handler() http.HandlerFunc {
	return func(http.ResponseWriter, *http.Request) {}
}
func (s *stubClusterKeyReceiver) NodeKeyHandler() http.HandlerFunc {
	return func(http.ResponseWriter, *http.Request) {}
}

// stubBackupService satisfies service.BackupServicer.
type stubBackupService struct{ service.BackupServicer }

// stubRebalanceService satisfies service.RebalanceServicer.
type stubRebalanceService struct{ service.RebalanceServicer }

// Compile-time check: SealedWindowProbe is satisfied by the extra stub.
var _ SealedWindowProbe = (*extraSealedWindowProbe)(nil)

// Suppress unused import for authfactors (referenced via AuthFactorServicer).
var _ = authfactors.NewFactorRegistry

// CompareAndSwap satisfies the guarded-swap half of the interface (go-qrdb#56).
func (*extraKVPassthrough) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return false, nil
}

// CompareAndSwap satisfies the guarded-swap half of the interface (go-qrdb#56).
func (*extraFailingKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return false, nil
}

// ListAll mirrors List on this double: it applies no internal-key guard,
// so the filtered and unfiltered enumerations are the same call.
func (k *extraKVPassthrough) ListAll(a0 context.Context, prefix string) ([]string, error) {
	return k.List(a0, prefix)
}

// ListAll mirrors List on this double: it applies no internal-key guard,
// so the filtered and unfiltered enumerations are the same call.
func (f *extraFailingKV) ListAll(a0 context.Context, a1 string) ([]string, error) {
	return f.List(a0, a1)
}
