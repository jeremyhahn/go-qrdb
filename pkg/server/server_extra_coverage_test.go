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
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"log/slog"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// Minimal stubs for types only used under codec_json && raft
// (without the full qrdb_ts/qrdb_vector/... build tags that gate
// server_options_test.go and namespaced_kv_test.go).
// ---------------------------------------------------------------------------

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

func (a *extraAuditLogger) Emit(_ context.Context, _ audit.AuditEvent) {}
func (a *extraAuditLogger) Query(_ context.Context, _ audit.Filter) (*audit.Page, error) {
	return nil, nil
}
func (a *extraAuditLogger) Close() error { return nil }

var _ audit.AuditLogger = (*extraAuditLogger)(nil)

// extraAuthFactorService satisfies service.AuthFactorServicer via embedding.
type extraAuthFactorService struct{ service.AuthFactorServicer }

// extraCustodianService is a minimal custodian.Servicer.
type extraCustodianService struct{}

func (s *extraCustodianService) RegisterCustodian(_ context.Context, _ custodian.RegisterRequest) (*custodian.RegisterResponse, error) {
	return nil, nil
}
func (s *extraCustodianService) GetCustodian(_ context.Context, _ string) (*custodian.Custodian, error) {
	return nil, nil
}
func (s *extraCustodianService) ListCustodians(_ context.Context, _ string) ([]*custodian.Custodian, error) {
	return nil, nil
}
func (s *extraCustodianService) RevokeCustodian(_ context.Context, _ string) error { return nil }

var _ custodian.Servicer = (*extraCustodianService)(nil)

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

func TestExtraNamespacedKV_ConstructorAndPrefix(t *testing.T) {
	inner := newExtraKVPassthrough()
	svc := newNamespacedKVService(inner, "tenant1")
	if svc.prefix != "tenant1/" {
		t.Errorf("prefix = %q, want %q", svc.prefix, "tenant1/")
	}
	if svc.inner != inner {
		t.Error("inner not stored")
	}
}

// Put

func TestExtraNamespacedKV_Put_PrependsPrefixToKey(t *testing.T) {
	inner := newExtraKVPassthrough()
	svc := newNamespacedKVService(inner, "ns")
	if err := svc.Put(context.Background(), "mykey", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if inner.lastKey != "ns/mykey" {
		t.Errorf("inner key = %q, want %q", inner.lastKey, "ns/mykey")
	}
}

func TestExtraNamespacedKV_Put_PropagatesError(t *testing.T) {
	sentinel := &extraKVError{"put fail"}
	svc := newNamespacedKVService(&extraFailingKV{err: sentinel}, "ns")
	err := svc.Put(context.Background(), "k", []byte("v"))
	var te *extraKVError
	if !errors.As(err, &te) {
		t.Errorf("expected extraKVError, got %T: %v", err, err)
	}
}

// Get

func TestExtraNamespacedKV_Get_PrependsPrefixToKey(t *testing.T) {
	inner := newExtraKVPassthrough()
	_ = inner.Put(context.Background(), "ns/hello", []byte("world"))
	svc := newNamespacedKVService(inner, "ns")
	val, err := svc.Get(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(val) != "world" {
		t.Errorf("Get = %q, want %q", val, "world")
	}
}

func TestExtraNamespacedKV_Get_PropagatesError(t *testing.T) {
	sentinel := &extraKVError{"get fail"}
	svc := newNamespacedKVService(&extraFailingKV{err: sentinel}, "ns")
	_, err := svc.Get(context.Background(), "k")
	var te *extraKVError
	if !errors.As(err, &te) {
		t.Errorf("expected extraKVError, got %T: %v", err, err)
	}
}

// Delete

func TestExtraNamespacedKV_Delete_PrependsPrefixToKey(t *testing.T) {
	inner := newExtraKVPassthrough()
	_ = inner.Put(context.Background(), "ns/todelete", []byte("v"))
	svc := newNamespacedKVService(inner, "ns")
	if err := svc.Delete(context.Background(), "todelete"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if inner.lastKey != "ns/todelete" {
		t.Errorf("inner key = %q, want %q", inner.lastKey, "ns/todelete")
	}
}

func TestExtraNamespacedKV_Delete_PropagatesError(t *testing.T) {
	sentinel := &extraKVError{"delete fail"}
	svc := newNamespacedKVService(&extraFailingKV{err: sentinel}, "ns")
	err := svc.Delete(context.Background(), "k")
	var te *extraKVError
	if !errors.As(err, &te) {
		t.Errorf("expected extraKVError, got %T: %v", err, err)
	}
}

// List

func TestExtraNamespacedKV_List_StripsPrefix(t *testing.T) {
	inner := newExtraKVPassthrough()
	ctx := context.Background()
	_ = inner.Put(ctx, "ns/a", []byte("1"))
	_ = inner.Put(ctx, "ns/b", []byte("2"))

	svc := newNamespacedKVService(inner, "ns")
	keys, err := svc.List(ctx, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, k := range keys {
		if k == "ns/a" || k == "ns/b" {
			t.Errorf("key %q still has namespace prefix after strip", k)
		}
	}
}

func TestExtraNamespacedKV_List_PropagatesError(t *testing.T) {
	sentinel := &extraKVError{"list fail"}
	svc := newNamespacedKVService(&extraFailingKV{err: sentinel}, "ns")
	_, err := svc.List(context.Background(), "")
	var te *extraKVError
	if !errors.As(err, &te) {
		t.Errorf("expected extraKVError, got %T: %v", err, err)
	}
}

// Scan

func TestExtraNamespacedKV_Scan_StripsPrefix(t *testing.T) {
	inner := newExtraKVPassthrough()
	ctx := context.Background()
	_ = inner.Put(ctx, "ns/x", []byte("vx"))
	_ = inner.Put(ctx, "ns/y", []byte("vy"))

	svc := newNamespacedKVService(inner, "ns")
	result, err := svc.Scan(ctx, "")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for k := range result {
		if k == "ns/x" || k == "ns/y" {
			t.Errorf("key %q still has namespace prefix", k)
		}
	}
	if string(result["x"]) != "vx" {
		t.Errorf("result[x] = %q, want %q", result["x"], "vx")
	}
	if string(result["y"]) != "vy" {
		t.Errorf("result[y] = %q, want %q", result["y"], "vy")
	}
}

func TestExtraNamespacedKV_Scan_PropagatesError(t *testing.T) {
	sentinel := &extraKVError{"scan fail"}
	svc := newNamespacedKVService(&extraFailingKV{err: sentinel}, "ns")
	_, err := svc.Scan(context.Background(), "")
	var te *extraKVError
	if !errors.As(err, &te) {
		t.Errorf("expected extraKVError, got %T: %v", err, err)
	}
}

// GetStale

func TestExtraNamespacedKV_GetStale_PrependsPrefixToKey(t *testing.T) {
	inner := newExtraKVPassthrough()
	ctx := context.Background()
	_ = inner.Put(ctx, "ns/stalekey", []byte("stale"))
	svc := newNamespacedKVService(inner, "ns")
	val, err := svc.GetStale(ctx, "stalekey")
	if err != nil {
		t.Fatalf("GetStale: %v", err)
	}
	if string(val) != "stale" {
		t.Errorf("GetStale = %q, want %q", val, "stale")
	}
}

func TestExtraNamespacedKV_GetStale_PropagatesError(t *testing.T) {
	sentinel := &extraKVError{"stale get fail"}
	svc := newNamespacedKVService(&extraFailingKV{err: sentinel}, "ns")
	_, err := svc.GetStale(context.Background(), "k")
	var te *extraKVError
	if !errors.As(err, &te) {
		t.Errorf("expected extraKVError, got %T: %v", err, err)
	}
}

// ListStale

func TestExtraNamespacedKV_ListStale_StripsPrefix(t *testing.T) {
	inner := newExtraKVPassthrough()
	ctx := context.Background()
	_ = inner.Put(ctx, "ns/s1", []byte("1"))
	_ = inner.Put(ctx, "ns/s2", []byte("2"))

	svc := newNamespacedKVService(inner, "ns")
	keys, err := svc.ListStale(ctx, "")
	if err != nil {
		t.Fatalf("ListStale: %v", err)
	}
	for _, k := range keys {
		if k == "ns/s1" || k == "ns/s2" {
			t.Errorf("key %q still has namespace prefix", k)
		}
	}
}

func TestExtraNamespacedKV_ListStale_PropagatesError(t *testing.T) {
	sentinel := &extraKVError{"stale list fail"}
	svc := newNamespacedKVService(&extraFailingKV{err: sentinel}, "ns")
	_, err := svc.ListStale(context.Background(), "")
	var te *extraKVError
	if !errors.As(err, &te) {
		t.Errorf("expected extraKVError, got %T: %v", err, err)
	}
}

// ScanStale

func TestExtraNamespacedKV_ScanStale_StripsPrefix(t *testing.T) {
	inner := newExtraKVPassthrough()
	ctx := context.Background()
	_ = inner.Put(ctx, "ns/q1", []byte("q1val"))

	svc := newNamespacedKVService(inner, "ns")
	result, err := svc.ScanStale(ctx, "")
	if err != nil {
		t.Fatalf("ScanStale: %v", err)
	}
	for k := range result {
		if k == "ns/q1" {
			t.Errorf("key %q still has namespace prefix", k)
		}
	}
	if string(result["q1"]) != "q1val" {
		t.Errorf("result[q1] = %q, want %q", result["q1"], "q1val")
	}
}

func TestExtraNamespacedKV_ScanStale_PropagatesError(t *testing.T) {
	sentinel := &extraKVError{"stale scan fail"}
	svc := newNamespacedKVService(&extraFailingKV{err: sentinel}, "ns")
	_, err := svc.ScanStale(context.Background(), "")
	var te *extraKVError
	if !errors.As(err, &te) {
		t.Errorf("expected extraKVError, got %T: %v", err, err)
	}
}

// PutWithMode

func TestExtraNamespacedKV_PutWithMode_PrependsPrefixToKey(t *testing.T) {
	inner := newExtraKVPassthrough()
	svc := newNamespacedKVService(inner, "ns")
	if err := svc.PutWithMode(context.Background(), "mkey", []byte("v"), writemode.Sync); err != nil {
		t.Fatalf("PutWithMode: %v", err)
	}
	if inner.lastKey != "ns/mkey" {
		t.Errorf("inner key = %q, want %q", inner.lastKey, "ns/mkey")
	}
}

func TestExtraNamespacedKV_PutWithMode_PropagatesError(t *testing.T) {
	sentinel := &extraKVError{"pwm fail"}
	svc := newNamespacedKVService(&extraFailingKV{err: sentinel}, "ns")
	err := svc.PutWithMode(context.Background(), "k", []byte("v"), writemode.Sync)
	var te *extraKVError
	if !errors.As(err, &te) {
		t.Errorf("expected extraKVError, got %T: %v", err, err)
	}
}

// DeleteWithMode

func TestExtraNamespacedKV_DeleteWithMode_PrependsPrefixToKey(t *testing.T) {
	inner := newExtraKVPassthrough()
	ctx := context.Background()
	_ = inner.Put(ctx, "ns/dmkey", []byte("v"))
	svc := newNamespacedKVService(inner, "ns")
	if err := svc.DeleteWithMode(ctx, "dmkey", writemode.Sync); err != nil {
		t.Fatalf("DeleteWithMode: %v", err)
	}
	if inner.lastKey != "ns/dmkey" {
		t.Errorf("inner key = %q, want %q", inner.lastKey, "ns/dmkey")
	}
}

func TestExtraNamespacedKV_DeleteWithMode_PropagatesError(t *testing.T) {
	sentinel := &extraKVError{"dwm fail"}
	svc := newNamespacedKVService(&extraFailingKV{err: sentinel}, "ns")
	err := svc.DeleteWithMode(context.Background(), "k", writemode.Sync)
	var te *extraKVError
	if !errors.As(err, &te) {
		t.Errorf("expected extraKVError, got %T: %v", err, err)
	}
}

// GetWithConsistency

func TestExtraNamespacedKV_GetWithConsistency_PrependsPrefixToKey(t *testing.T) {
	inner := newExtraKVPassthrough()
	ctx := context.Background()
	_ = inner.Put(ctx, "ns/ckey", []byte("cval"))

	svc := newNamespacedKVService(inner, "ns")
	val, err := svc.GetWithConsistency(ctx, "ckey", consistency.ReadOptions{})
	if err != nil {
		t.Fatalf("GetWithConsistency: %v", err)
	}
	if string(val) != "cval" {
		t.Errorf("GetWithConsistency = %q, want %q", val, "cval")
	}
}

func TestExtraNamespacedKV_GetWithConsistency_PropagatesError(t *testing.T) {
	sentinel := &extraKVError{"gwc fail"}
	svc := newNamespacedKVService(&extraFailingKV{err: sentinel}, "ns")
	_, err := svc.GetWithConsistency(context.Background(), "k", consistency.ReadOptions{})
	var te *extraKVError
	if !errors.As(err, &te) {
		t.Errorf("expected extraKVError, got %T: %v", err, err)
	}
}

// ListWithConsistency

func TestExtraNamespacedKV_ListWithConsistency_StripsPrefix(t *testing.T) {
	inner := newExtraKVPassthrough()
	ctx := context.Background()
	_ = inner.Put(ctx, "ns/lc1", []byte("1"))

	svc := newNamespacedKVService(inner, "ns")
	keys, err := svc.ListWithConsistency(ctx, "", consistency.ReadOptions{})
	if err != nil {
		t.Fatalf("ListWithConsistency: %v", err)
	}
	for _, k := range keys {
		if k == "ns/lc1" {
			t.Errorf("key %q still has namespace prefix", k)
		}
	}
}

func TestExtraNamespacedKV_ListWithConsistency_PropagatesError(t *testing.T) {
	sentinel := &extraKVError{"lwc fail"}
	svc := newNamespacedKVService(&extraFailingKV{err: sentinel}, "ns")
	_, err := svc.ListWithConsistency(context.Background(), "", consistency.ReadOptions{})
	var te *extraKVError
	if !errors.As(err, &te) {
		t.Errorf("expected extraKVError, got %T: %v", err, err)
	}
}

// ScanWithConsistency

func TestExtraNamespacedKV_ScanWithConsistency_StripsPrefix(t *testing.T) {
	inner := newExtraKVPassthrough()
	ctx := context.Background()
	_ = inner.Put(ctx, "ns/sc1", []byte("sc1val"))

	svc := newNamespacedKVService(inner, "ns")
	result, err := svc.ScanWithConsistency(ctx, "", consistency.ReadOptions{})
	if err != nil {
		t.Fatalf("ScanWithConsistency: %v", err)
	}
	for k := range result {
		if k == "ns/sc1" {
			t.Errorf("key %q still has namespace prefix", k)
		}
	}
	if string(result["sc1"]) != "sc1val" {
		t.Errorf("result[sc1] = %q, want %q", result["sc1"], "sc1val")
	}
}

func TestExtraNamespacedKV_ScanWithConsistency_PropagatesError(t *testing.T) {
	sentinel := &extraKVError{"swc fail"}
	svc := newNamespacedKVService(&extraFailingKV{err: sentinel}, "ns")
	_, err := svc.ScanWithConsistency(context.Background(), "", consistency.ReadOptions{})
	var te *extraKVError
	if !errors.As(err, &te) {
		t.Errorf("expected extraKVError, got %T: %v", err, err)
	}
}

// ExistsWithConsistency

func TestExtraNamespacedKV_ExistsWithConsistency_Found(t *testing.T) {
	inner := newExtraKVPassthrough()
	ctx := context.Background()
	_ = inner.Put(ctx, "ns/existkey", []byte("v"))

	svc := newNamespacedKVService(inner, "ns")
	ok, err := svc.ExistsWithConsistency(ctx, "existkey", consistency.ReadOptions{})
	if err != nil {
		t.Fatalf("ExistsWithConsistency: %v", err)
	}
	if !ok {
		t.Error("expected true for existing key")
	}
}

func TestExtraNamespacedKV_ExistsWithConsistency_NotFound(t *testing.T) {
	svc := newNamespacedKVService(newExtraKVPassthrough(), "ns")
	ok, err := svc.ExistsWithConsistency(context.Background(), "ghost", consistency.ReadOptions{})
	if err != nil {
		t.Fatalf("ExistsWithConsistency: %v", err)
	}
	if ok {
		t.Error("expected false for non-existent key")
	}
}

func TestExtraNamespacedKV_ExistsWithConsistency_PropagatesError(t *testing.T) {
	sentinel := &extraKVError{"ewc fail"}
	svc := newNamespacedKVService(&extraFailingKV{err: sentinel}, "ns")
	_, err := svc.ExistsWithConsistency(context.Background(), "k", consistency.ReadOptions{})
	var te *extraKVError
	if !errors.As(err, &te) {
		t.Errorf("expected extraKVError, got %T: %v", err, err)
	}
}

// PutEntity

func TestExtraNamespacedKV_PutEntity_PrependsPrefixToID(t *testing.T) {
	inner := newExtraKVPassthrough()
	svc := newNamespacedKVService(inner, "ns")
	resp, err := svc.PutEntity(context.Background(), &service.PutEntityRequest{
		ID:         "entity/1",
		EntityType: "user",
	})
	if err != nil {
		t.Fatalf("PutEntity: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil PutEntityResponse")
	}
	// The inner saw the prefixed ID.
	if inner.lastKey != "ns/entity/1" {
		t.Errorf("inner lastKey = %q, want %q", inner.lastKey, "ns/entity/1")
	}
}

func TestExtraNamespacedKV_PutEntity_PropagatesError(t *testing.T) {
	sentinel := &extraKVError{"pe fail"}
	svc := newNamespacedKVService(&extraFailingKV{err: sentinel}, "ns")
	_, err := svc.PutEntity(context.Background(), &service.PutEntityRequest{ID: "e/1", EntityType: "x"})
	var te *extraKVError
	if !errors.As(err, &te) {
		t.Errorf("expected extraKVError, got %T: %v", err, err)
	}
}

// GetEntity

func TestExtraNamespacedKV_GetEntity_PrependsPrefixToID(t *testing.T) {
	inner := newExtraKVPassthrough()
	svc := newNamespacedKVService(inner, "ns")
	resp, err := svc.GetEntity(context.Background(), &service.GetEntityRequest{
		ID:         "entity/1",
		EntityType: "user",
	})
	if err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil GetEntityResponse")
	}
	if inner.lastKey != "ns/entity/1" {
		t.Errorf("inner lastKey = %q, want %q", inner.lastKey, "ns/entity/1")
	}
}

func TestExtraNamespacedKV_GetEntity_PropagatesError(t *testing.T) {
	sentinel := &extraKVError{"ge fail"}
	svc := newNamespacedKVService(&extraFailingKV{err: sentinel}, "ns")
	_, err := svc.GetEntity(context.Background(), &service.GetEntityRequest{ID: "e/1", EntityType: "x"})
	var te *extraKVError
	if !errors.As(err, &te) {
		t.Errorf("expected extraKVError, got %T: %v", err, err)
	}
}

// DeleteEntity

func TestExtraNamespacedKV_DeleteEntity_PrependsPrefixToID(t *testing.T) {
	inner := newExtraKVPassthrough()
	svc := newNamespacedKVService(inner, "ns")
	resp, err := svc.DeleteEntity(context.Background(), &service.DeleteEntityRequest{
		ID:         "entity/1",
		EntityType: "user",
	})
	if err != nil {
		t.Fatalf("DeleteEntity: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil DeleteEntityResponse")
	}
	if inner.lastKey != "ns/entity/1" {
		t.Errorf("inner lastKey = %q, want %q", inner.lastKey, "ns/entity/1")
	}
}

func TestExtraNamespacedKV_DeleteEntity_PropagatesError(t *testing.T) {
	sentinel := &extraKVError{"de fail"}
	svc := newNamespacedKVService(&extraFailingKV{err: sentinel}, "ns")
	_, err := svc.DeleteEntity(context.Background(), &service.DeleteEntityRequest{ID: "e/1", EntityType: "x"})
	var te *extraKVError
	if !errors.As(err, &te) {
		t.Errorf("expected extraKVError, got %T: %v", err, err)
	}
}

// ListEntities

func TestExtraNamespacedKV_ListEntities_StripsPrefix(t *testing.T) {
	inner := &extraListEntitiesKV{extraKVPassthrough: newExtraKVPassthrough()}
	svc := newNamespacedKVService(inner, "ns")

	resp, err := svc.ListEntities(context.Background(), &service.ListEntitiesRequest{
		EntityType: "user",
	})
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	for _, k := range resp.Keys {
		if len(k) >= 3 && k[:3] == "ns/" {
			t.Errorf("key %q still has namespace prefix after strip", k)
		}
	}
}

func TestExtraNamespacedKV_ListEntities_PropagatesError(t *testing.T) {
	sentinel := &extraKVError{"le fail"}
	svc := newNamespacedKVService(&extraFailingKV{err: sentinel}, "ns")
	_, err := svc.ListEntities(context.Background(), &service.ListEntitiesRequest{EntityType: "x"})
	var te *extraKVError
	if !errors.As(err, &te) {
		t.Errorf("expected extraKVError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// server.go — WithSealedWindowProbe
// ---------------------------------------------------------------------------

func TestWithSealedWindowProbe_Applied(t *testing.T) {
	t.Parallel()
	probe := &extraSealedWindowProbe{sealed: true}
	var opts serverOptions
	WithSealedWindowProbe(probe)(&opts)
	if opts.sealedWindowProbe != probe {
		t.Error("WithSealedWindowProbe did not store the provided probe")
	}
}

func TestWithSealedWindowProbe_UnsealeValue(t *testing.T) {
	t.Parallel()
	probe := &extraSealedWindowProbe{sealed: false}
	var opts serverOptions
	WithSealedWindowProbe(probe)(&opts)
	if opts.sealedWindowProbe == nil {
		t.Fatal("expected sealedWindowProbe to be set")
	}
	if opts.sealedWindowProbe.IsSealed() {
		t.Error("expected IsSealed() = false")
	}
}

func TestWithSealedWindowProbe_ViaNewServer(t *testing.T) {
	probe := &extraSealedWindowProbe{sealed: false}
	cfg := testConfig(false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithSealedWindowProbe(probe))
	if srv.opts.sealedWindowProbe != probe {
		t.Error("WithSealedWindowProbe not propagated via New()")
	}
}

// ---------------------------------------------------------------------------
// server.go — WithLockService
// ---------------------------------------------------------------------------

func TestExtraWithLockService_Applied(t *testing.T) {
	t.Parallel()
	svc := &extraLockImpl{}
	var opts serverOptions
	WithLockService(svc)(&opts)
	if opts.lockSvc != svc {
		t.Error("WithLockService did not store the provided LockServicer")
	}
}

func TestExtraWithLockService_ViaNewServer(t *testing.T) {
	svc := &extraLockImpl{}
	cfg := testConfig(false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithLockService(svc))
	if srv.opts.lockSvc != svc {
		t.Error("WithLockService not propagated via New()")
	}
}

// ---------------------------------------------------------------------------
// server.go — WithKVService (option-based override)
// ---------------------------------------------------------------------------

func TestExtraWithKVService_StoresOnOpts(t *testing.T) {
	t.Parallel()
	svc := newMockKVService()
	var opts serverOptions
	WithKVService(svc)(&opts)
	if opts.kvSvc != svc {
		t.Error("WithKVService did not store the provided KVServicer on opts")
	}
}

func TestExtraWithKVService_OverridesNilPositionalParam(t *testing.T) {
	t.Parallel()
	kvSvc := newMockKVService()
	cfg := testConfig(false, false)
	srv := New(cfg, nil, &mockAdminService{}, WithKVService(kvSvc))
	if srv.kvSvc != kvSvc {
		t.Error("WithKVService: kvSvc should be the option value, not nil")
	}
}

// ---------------------------------------------------------------------------
// server.go — WithAdminService (option-based override)
// ---------------------------------------------------------------------------

func TestExtraWithAdminService_StoresOnOpts(t *testing.T) {
	t.Parallel()
	svc := &mockAdminService{}
	var opts serverOptions
	WithAdminService(svc)(&opts)
	if opts.adminSvc != svc {
		t.Error("WithAdminService did not store the provided AdminServicer on opts")
	}
}

func TestExtraWithAdminService_OverridesNilPositionalParam(t *testing.T) {
	t.Parallel()
	adminSvc := &mockAdminService{}
	cfg := testConfig(false, false)
	srv := New(cfg, newMockKVService(), nil, WithAdminService(adminSvc))
	if srv.adminSvc != adminSvc {
		t.Error("WithAdminService: adminSvc should be the option value, not nil")
	}
}

// ---------------------------------------------------------------------------
// server.go — WithWebUI
// ---------------------------------------------------------------------------

func TestExtraWithWebUI_StoresOnOpts(t *testing.T) {
	t.Parallel()
	ui := &extraWebUI{}
	var opts serverOptions
	WithWebUI(ui)(&opts)
	if opts.webUI != ui {
		t.Error("WithWebUI did not store the provided http.Handler")
	}
}

func TestExtraWithWebUI_NilIsAccepted(t *testing.T) {
	t.Parallel()
	var opts serverOptions
	WithWebUI(nil)(&opts)
	if opts.webUI != nil {
		t.Errorf("expected nil webUI, got %v", opts.webUI)
	}
}

func TestExtraWithWebUI_ViaNewServer(t *testing.T) {
	ui := &extraWebUI{}
	cfg := testConfig(false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithWebUI(ui))
	if srv.opts.webUI != ui {
		t.Error("WithWebUI not propagated via New()")
	}
}

// ---------------------------------------------------------------------------
// server.go — WithAuditLogger
// ---------------------------------------------------------------------------

func TestExtraWithAuditLogger_StoresOnOpts(t *testing.T) {
	t.Parallel()
	logger := &extraAuditLogger{}
	var opts serverOptions
	WithAuditLogger(logger)(&opts)
	if opts.auditLogger != logger {
		t.Error("WithAuditLogger did not store the provided AuditLogger")
	}
}

func TestExtraWithAuditLogger_ViaNewServer(t *testing.T) {
	logger := &extraAuditLogger{}
	cfg := testConfig(false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithAuditLogger(logger))
	if srv.opts.auditLogger != logger {
		t.Error("WithAuditLogger not propagated via New()")
	}
}

// ---------------------------------------------------------------------------
// server.go — WithAuthFactorService
// ---------------------------------------------------------------------------

func TestExtraWithAuthFactorService_StoresOnOpts(t *testing.T) {
	t.Parallel()
	svc := &extraAuthFactorService{}
	var opts serverOptions
	WithAuthFactorService(svc)(&opts)
	if opts.authFactorSvc != svc {
		t.Error("WithAuthFactorService did not store the provided service")
	}
}

func TestExtraWithAuthFactorService_ViaNewServer(t *testing.T) {
	svc := &extraAuthFactorService{}
	cfg := testConfig(false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithAuthFactorService(svc))
	if srv.opts.authFactorSvc != svc {
		t.Error("WithAuthFactorService not propagated via New()")
	}
}

// ---------------------------------------------------------------------------
// server.go — WithCustodianService
// ---------------------------------------------------------------------------

func TestExtraWithCustodianService_StoresOnOpts(t *testing.T) {
	t.Parallel()
	svc := &extraCustodianService{}
	var opts serverOptions
	WithCustodianService(svc)(&opts)
	if opts.custodianSvc != svc {
		t.Error("WithCustodianService did not store the provided service")
	}
}

func TestExtraWithCustodianService_ViaNewServer(t *testing.T) {
	svc := &extraCustodianService{}
	cfg := testConfig(false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCustodianService(svc))
	if srv.opts.custodianSvc != svc {
		t.Error("WithCustodianService not propagated via New()")
	}
}

// ---------------------------------------------------------------------------
// server.go — WithSOAuthenticator (nil accepted; concrete type not accessible
// without a full auth subsystem, so nil-path is the practical test)
// ---------------------------------------------------------------------------

func TestExtraWithSOAuthenticator_NilAccepted(t *testing.T) {
	t.Parallel()
	var opts serverOptions
	WithSOAuthenticator(nil)(&opts)
	if opts.soAuthenticator != nil {
		t.Errorf("expected nil soAuthenticator, got %v", opts.soAuthenticator)
	}
}

func TestExtraWithSOAuthenticator_ViaNewServer(t *testing.T) {
	cfg := testConfig(false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithSOAuthenticator(nil))
	if srv.opts.soAuthenticator != nil {
		t.Error("expected nil soAuthenticator after nil WithSOAuthenticator")
	}
}

// ---------------------------------------------------------------------------
// rest_registry.go — checkTenantBarrier branches
// (barrierReg != nil, non-system tenant, GetTenant error paths)
// ---------------------------------------------------------------------------

// extraTenantNotFoundServicer embeds mockTenantServicer and overrides
// GetTenant to return ErrTenantNotFound for non-system tenants.
type extraTenantNotFoundServicer struct{ mockTenantServicer }

func (e *extraTenantNotFoundServicer) GetTenant(_ context.Context, id string) (*tenant.TenantInfo, error) {
	if id == "system" {
		return &tenant.TenantInfo{ID: id, Name: id, IsolationType: tenant.IsolationBarrier}, nil
	}
	return nil, tenant.ErrTenantNotFound
}

var _ tenant.TenantServicer = (*extraTenantNotFoundServicer)(nil)

// extraGenericErrTenantServicer embeds mockTenantServicer and overrides
// GetTenant to return a generic error (not ErrTenantNotFound) for all tenants.
type extraGenericErrTenantServicer struct{ mockTenantServicer }

func (e *extraGenericErrTenantServicer) GetTenant(_ context.Context, _ string) (*tenant.TenantInfo, error) {
	return nil, errors.New("generic tenant error")
}

var _ tenant.TenantServicer = (*extraGenericErrTenantServicer)(nil)

// TestRESTServiceRegistry_UnknownTenant_ReturnsTenantNotFoundError verifies
// that when tenantSvc is configured and GetTenant returns ErrTenantNotFound,
// the registry returns a *rest.TenantNotFoundError.
func TestRESTServiceRegistry_UnknownTenant_ReturnsTenantNotFoundError(t *testing.T) {
	r := NewRESTServiceRegistry(
		newExtraFilledRegistry(),
		nil,
		"system",
		WithTenantServicer(&extraTenantNotFoundServicer{}),
	)
	_, err := r.KVService("unknown-tenant", "default")
	if err == nil {
		t.Fatal("expected TenantNotFoundError, got nil")
	}
	var tnf *rest.TenantNotFoundError
	if !errors.As(err, &tnf) {
		t.Errorf("expected *rest.TenantNotFoundError, got %T: %v", err, err)
	}
}

// TestRESTServiceRegistry_GenericTenantError_NonSystemTenant verifies that a
// non-ErrTenantNotFound error from GetTenant still returns a TenantNotFoundError
// (the registry normalizes all GetTenant errors to 404 for non-system tenants).
func TestRESTServiceRegistry_GenericTenantError_NonSystemTenant(t *testing.T) {
	r := NewRESTServiceRegistry(
		newExtraFilledRegistry(),
		nil,
		"system",
		WithTenantServicer(&extraGenericErrTenantServicer{}),
	)
	_, err := r.KVService("sometenant", "default")
	if err == nil {
		t.Fatal("expected TenantNotFoundError for generic GetTenant error")
	}
	var tnf *rest.TenantNotFoundError
	if !errors.As(err, &tnf) {
		t.Errorf("expected *rest.TenantNotFoundError, got %T: %v", err, err)
	}
}

// TestRESTServiceRegistry_SystemTenantGenericError_Fallback verifies that
// when tenantSvc returns a generic error for the system tenant, the registry
// falls back to the barrier check (which succeeds with nil barrierReg).
func TestRESTServiceRegistry_SystemTenantGenericError_Fallback(t *testing.T) {
	r := NewRESTServiceRegistry(
		newExtraFilledRegistry(),
		nil,
		"system",
		WithTenantServicer(&extraGenericErrTenantServicer{}),
	)
	svc, err := r.KVService("system", "default")
	if err != nil {
		t.Fatalf("system tenant with generic error should fall back: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil KVServicer")
	}
}

// TestRESTServiceRegistry_NilServices_AllServiceMethods verifies that each
// named service method returns ServiceNotImplementedError when the resolved
// DatabaseServices has nil service fields.
func TestRESTServiceRegistry_NilServices_AllServiceMethods(t *testing.T) {
	reg := NewDatabaseServiceRegistry("default")
	reg.Register("default", &DatabaseServices{Name: "default", KV: newMockKVService()})
	r := NewRESTServiceRegistry(reg, nil, "system")

	tests := []struct {
		name    string
		svcName string
		fn      func() error
	}{
		{"mvcc", "mvcc", func() error { _, err := r.MVCCService("system", "default"); return err }},
		{"watch", "watch", func() error { _, err := r.WatchService("system", "default"); return err }},
		{"lease", "lease", func() error { _, err := r.LeaseService("system", "default"); return err }},
		{"index", "index", func() error { _, err := r.IndexService("system", "default"); return err }},
		{"timeseries", "timeseries", func() error { _, err := r.TimeSeriesService("system", "default"); return err }},
		{"vector", "vector", func() error { _, err := r.VectorService("system", "default"); return err }},
		{"transaction", "transaction", func() error { _, err := r.TransactionService("system", "default"); return err }},
		{"batch", "batch", func() error { _, err := r.BatchService("system", "default"); return err }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.fn()
			if err == nil {
				t.Fatalf("expected ServiceNotImplementedError for nil %s service", tc.name)
			}
			var nie *rest.ServiceNotImplementedError
			if !errors.As(err, &nie) {
				t.Errorf("expected *rest.ServiceNotImplementedError, got %T: %v", err, err)
			}
			if nie.Service != tc.svcName {
				t.Errorf("Service field = %q, want %q", nie.Service, tc.svcName)
			}
		})
	}
}

// TestRESTServiceRegistry_CheckTenantBarrier_SystemTenantBypass verifies that
// checkTenantBarrier returns nil for the system tenant even when barrierReg
// is nil (the nil-barrierReg early-return path).
func TestRESTServiceRegistry_CheckTenantBarrier_SystemTenantBypass(t *testing.T) {
	r := &restServiceRegistry{
		dbReg:        newExtraFilledRegistry(),
		barrierReg:   nil,
		systemTenant: "system",
	}
	if err := r.checkTenantBarrier("system"); err != nil {
		t.Errorf("system tenant should bypass barrier check, got: %v", err)
	}
}

// TestRESTServiceRegistry_CheckTenantBarrier_NilRegistry verifies that
// checkTenantBarrier returns nil when barrierReg is nil for any tenant.
func TestRESTServiceRegistry_CheckTenantBarrier_NilRegistry(t *testing.T) {
	r := &restServiceRegistry{
		dbReg:        newExtraFilledRegistry(),
		barrierReg:   nil,
		systemTenant: "system",
	}
	if err := r.checkTenantBarrier("any-tenant"); err != nil {
		t.Errorf("nil barrierReg should skip barrier check, got: %v", err)
	}
}

// newExtraFilledRegistry returns a DatabaseServiceRegistry with a "default"
// database that has a non-nil KV service so service-presence checks pass.
// It is intentionally separate from newFilledRegistry (which requires the
// full build-tag set) to avoid duplicate declarations.
func newExtraFilledRegistry() *DatabaseServiceRegistry {
	reg := NewDatabaseServiceRegistry("default")
	reg.Register("default", &DatabaseServices{Name: "default", KV: newMockKVService()})
	reg.Register("mydb", &DatabaseServices{Name: "mydb", KV: newMockKVService()})
	return reg
}

// ---------------------------------------------------------------------------
// server.go — New() with both positional params nil and option-based override
// ---------------------------------------------------------------------------

func TestExtraNew_BothPositionalNilWithOptions(t *testing.T) {
	t.Parallel()
	kvSvc := newMockKVService()
	adminSvc := &mockAdminService{}
	cfg := testConfig(false, false)

	srv := New(cfg, nil, nil, WithKVService(kvSvc), WithAdminService(adminSvc))
	if srv == nil {
		t.Fatal("New returned nil")
	}
	if srv.kvSvc != kvSvc {
		t.Error("kvSvc should be the WithKVService value")
	}
	if srv.adminSvc != adminSvc {
		t.Error("adminSvc should be the WithAdminService value")
	}
}

// ---------------------------------------------------------------------------
// server.go startREST/startGRPC/startQUIC/startUnix/startMCP —
// exercise the remaining if-branches for sealedWindowProbe, lockSvc,
// embeddingSvc, ragSvc, pinBackend, tenantSvc, authFactorSvc, webUI,
// auditLogger, custodianSvc, dbRegistry by starting all five protocols
// with those options set.
// ---------------------------------------------------------------------------

func TestStart_AllFiveProtocols_WithRemainingBranchOptions(t *testing.T) {
	socketPath := t.TempDir() + "/extra.sock"
	tlsCfg := selfSignedTLSConfig(t)
	cfg := testConfigFull(protocolFlags{
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
	custodianSvc := &extraCustodianService{}
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
		WithCustodianService(custodianSvc),
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
	cfg := testConfigFull(protocolFlags{
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

	cfg := testConfig(true, false) // REST only
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

// Compile-time check: SealedWindowProbe is satisfied by the extra stub.
var _ SealedWindowProbe = (*extraSealedWindowProbe)(nil)

// Suppress unused import for authfactors (referenced via AuthFactorServicer).
var _ = authfactors.NewFactorRegistry
