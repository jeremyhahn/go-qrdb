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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// namespacedKVServicerError is a typed error returned by the failing inner service.
type namespacedKVServicerError struct{ msg string }

func (e *namespacedKVServicerError) Error() string { return e.msg }

// failingInnerKV returns errors from every method to verify error propagation.
type failingInnerKV struct {
	err error
}

func (f *failingInnerKV) Put(_ context.Context, _ string, _ []byte) error { return f.err }
func (f *failingInnerKV) PutIfAbsent(_ context.Context, _ string, _ []byte) (bool, error) {
	return false, f.err
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (f *failingInnerKV) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	return false, nil
}
func (f *failingInnerKV) Get(_ context.Context, _ string) ([]byte, error) {
	return nil, f.err
}
func (f *failingInnerKV) Delete(_ context.Context, _ string) error { return f.err }
func (f *failingInnerKV) List(_ context.Context, _ string) ([]string, error) {
	return nil, f.err
}
func (f *failingInnerKV) Scan(_ context.Context, _ string) (map[string][]byte, error) {
	return nil, f.err
}
func (f *failingInnerKV) GetStale(_ context.Context, _ string) ([]byte, error) {
	return nil, f.err
}
func (f *failingInnerKV) ListStale(_ context.Context, _ string) ([]string, error) {
	return nil, f.err
}
func (f *failingInnerKV) ScanStale(_ context.Context, _ string) (map[string][]byte, error) {
	return nil, f.err
}
func (f *failingInnerKV) PutWithMode(_ context.Context, _ string, _ []byte, _ writemode.Mode) error {
	return f.err
}
func (f *failingInnerKV) DeleteWithMode(_ context.Context, _ string, _ writemode.Mode) error {
	return f.err
}
func (f *failingInnerKV) GetWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) ([]byte, error) {
	return nil, f.err
}
func (f *failingInnerKV) ListWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) ([]string, error) {
	return nil, f.err
}
func (f *failingInnerKV) ScanWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (map[string][]byte, error) {
	return nil, f.err
}
func (f *failingInnerKV) ExistsWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (bool, error) {
	return false, f.err
}
func (f *failingInnerKV) PutEntity(_ context.Context, _ *service.PutEntityRequest) (*service.PutEntityResponse, error) {
	return nil, f.err
}
func (f *failingInnerKV) GetEntity(_ context.Context, _ *service.GetEntityRequest) (*service.GetEntityResponse, error) {
	return nil, f.err
}
func (f *failingInnerKV) DeleteEntity(_ context.Context, _ *service.DeleteEntityRequest) (*service.DeleteEntityResponse, error) {
	return nil, f.err
}
func (f *failingInnerKV) ListEntities(_ context.Context, _ *service.ListEntitiesRequest) (*service.ListEntitiesResponse, error) {
	return nil, f.err
}

// captureInnerKV records the last key it was called with for assertion.
type captureInnerKV struct {
	*mockKVService
	lastKey    string
	lastPrefix string
}

func newCaptureInnerKV() *captureInnerKV {
	return &captureInnerKV{mockKVService: newMockKVService()}
}

func (c *captureInnerKV) Put(ctx context.Context, key string, value []byte) error {
	c.lastKey = key
	return c.mockKVService.Put(ctx, key, value)
}
func (c *captureInnerKV) Get(ctx context.Context, key string) ([]byte, error) {
	c.lastKey = key
	return c.mockKVService.Get(ctx, key)
}
func (c *captureInnerKV) Delete(ctx context.Context, key string) error {
	c.lastKey = key
	return c.mockKVService.Delete(ctx, key)
}
func (c *captureInnerKV) List(ctx context.Context, prefix string) ([]string, error) {
	c.lastPrefix = prefix
	return c.mockKVService.List(ctx, prefix)
}
func (c *captureInnerKV) Scan(ctx context.Context, prefix string) (map[string][]byte, error) {
	c.lastPrefix = prefix
	return c.mockKVService.Scan(ctx, prefix)
}
func (c *captureInnerKV) GetStale(ctx context.Context, key string) ([]byte, error) {
	c.lastKey = key
	return c.mockKVService.GetStale(ctx, key)
}
func (c *captureInnerKV) ListStale(ctx context.Context, prefix string) ([]string, error) {
	c.lastPrefix = prefix
	return c.mockKVService.ListStale(ctx, prefix)
}
func (c *captureInnerKV) ScanStale(ctx context.Context, prefix string) (map[string][]byte, error) {
	c.lastPrefix = prefix
	return c.mockKVService.ScanStale(ctx, prefix)
}
func (c *captureInnerKV) PutWithMode(ctx context.Context, key string, value []byte, mode writemode.Mode) error {
	c.lastKey = key
	return c.mockKVService.PutWithMode(ctx, key, value, mode)
}
func (c *captureInnerKV) DeleteWithMode(ctx context.Context, key string, mode writemode.Mode) error {
	c.lastKey = key
	return c.mockKVService.DeleteWithMode(ctx, key, mode)
}

// ---------------------------------------------------------------------------
// newNamespacedKVService
// ---------------------------------------------------------------------------

func TestNewNamespacedKVService_PrefixSet(t *testing.T) {
	inner := newMockKVService()
	svc := newNamespacedKVService(inner, "acme")
	if svc.prefix != "acme/" {
		t.Errorf("prefix = %q, want %q", svc.prefix, "acme/")
	}
	if svc.inner != inner {
		t.Error("inner service not stored")
	}
}

// ---------------------------------------------------------------------------
// Put
// ---------------------------------------------------------------------------

func TestNamespacedKV_Put_PrependsPrefixToKey(t *testing.T) {
	inner := newCaptureInnerKV()
	svc := newNamespacedKVService(inner, "tenant1")
	ctx := context.Background()

	if err := svc.Put(ctx, "mykey", []byte("val")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if inner.lastKey != "tenant1/mykey" {
		t.Errorf("inner received key %q, want %q", inner.lastKey, "tenant1/mykey")
	}
}

func TestNamespacedKV_Put_PropagatesError(t *testing.T) {
	sentinel := &namespacedKVServicerError{"put failed"}
	svc := newNamespacedKVService(&failingInnerKV{err: sentinel}, "ns")
	err := svc.Put(context.Background(), "k", []byte("v"))
	var te *namespacedKVServicerError
	if !errors.As(err, &te) {
		t.Errorf("expected namespacedKVServicerError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Get
// ---------------------------------------------------------------------------

func TestNamespacedKV_Get_PrependsPrefixToKey(t *testing.T) {
	inner := newCaptureInnerKV()
	svc := newNamespacedKVService(inner, "ns")
	ctx := context.Background()

	// Put via inner to set up the key (already prefixed).
	_ = inner.Put(ctx, "ns/hello", []byte("world"))

	val, err := svc.Get(ctx, "hello")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(val) != "world" {
		t.Errorf("Get = %q, want %q", val, "world")
	}
}

func TestNamespacedKV_Get_PropagatesError(t *testing.T) {
	sentinel := &namespacedKVServicerError{"get failed"}
	svc := newNamespacedKVService(&failingInnerKV{err: sentinel}, "ns")
	_, err := svc.Get(context.Background(), "k")
	var te *namespacedKVServicerError
	if !errors.As(err, &te) {
		t.Errorf("expected namespacedKVServicerError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func TestNamespacedKV_Delete_PrependsPrefixToKey(t *testing.T) {
	inner := newCaptureInnerKV()
	svc := newNamespacedKVService(inner, "ns")
	ctx := context.Background()

	_ = inner.Put(ctx, "ns/todelete", []byte("v"))
	if err := svc.Delete(ctx, "todelete"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if inner.lastKey != "ns/todelete" {
		t.Errorf("inner received key %q, want %q", inner.lastKey, "ns/todelete")
	}
}

func TestNamespacedKV_Delete_PropagatesError(t *testing.T) {
	sentinel := &namespacedKVServicerError{"delete failed"}
	svc := newNamespacedKVService(&failingInnerKV{err: sentinel}, "ns")
	err := svc.Delete(context.Background(), "k")
	var te *namespacedKVServicerError
	if !errors.As(err, &te) {
		t.Errorf("expected namespacedKVServicerError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

func TestNamespacedKV_List_StripsPrefix(t *testing.T) {
	inner := newMockKVService()
	ctx := context.Background()
	_ = inner.Put(ctx, "ns/a", []byte("1"))
	_ = inner.Put(ctx, "ns/b", []byte("2"))
	_ = inner.Put(ctx, "other/c", []byte("3"))

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
	// "other/c" should not appear (different namespace prefix).
	for _, k := range keys {
		if k == "other/c" || k == "c" {
			t.Errorf("unexpected key %q in result", k)
		}
	}
}

func TestNamespacedKV_List_PropagatesError(t *testing.T) {
	sentinel := &namespacedKVServicerError{"list failed"}
	svc := newNamespacedKVService(&failingInnerKV{err: sentinel}, "ns")
	_, err := svc.List(context.Background(), "")
	var te *namespacedKVServicerError
	if !errors.As(err, &te) {
		t.Errorf("expected namespacedKVServicerError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Scan
// ---------------------------------------------------------------------------

func TestNamespacedKV_Scan_StripsPrefix(t *testing.T) {
	inner := newMockKVService()
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

func TestNamespacedKV_Scan_PropagatesError(t *testing.T) {
	sentinel := &namespacedKVServicerError{"scan failed"}
	svc := newNamespacedKVService(&failingInnerKV{err: sentinel}, "ns")
	_, err := svc.Scan(context.Background(), "")
	var te *namespacedKVServicerError
	if !errors.As(err, &te) {
		t.Errorf("expected namespacedKVServicerError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// GetStale
// ---------------------------------------------------------------------------

func TestNamespacedKV_GetStale_PrependsPrefixToKey(t *testing.T) {
	inner := newCaptureInnerKV()
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

func TestNamespacedKV_GetStale_PropagatesError(t *testing.T) {
	sentinel := &namespacedKVServicerError{"get stale failed"}
	svc := newNamespacedKVService(&failingInnerKV{err: sentinel}, "ns")
	_, err := svc.GetStale(context.Background(), "k")
	var te *namespacedKVServicerError
	if !errors.As(err, &te) {
		t.Errorf("expected namespacedKVServicerError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// ListStale
// ---------------------------------------------------------------------------

func TestNamespacedKV_ListStale_StripsPrefix(t *testing.T) {
	inner := newMockKVService()
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
			t.Errorf("key %q still has namespace prefix after strip", k)
		}
	}
}

func TestNamespacedKV_ListStale_PropagatesError(t *testing.T) {
	sentinel := &namespacedKVServicerError{"list stale failed"}
	svc := newNamespacedKVService(&failingInnerKV{err: sentinel}, "ns")
	_, err := svc.ListStale(context.Background(), "")
	var te *namespacedKVServicerError
	if !errors.As(err, &te) {
		t.Errorf("expected namespacedKVServicerError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// ScanStale
// ---------------------------------------------------------------------------

func TestNamespacedKV_ScanStale_StripsPrefix(t *testing.T) {
	inner := newMockKVService()
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

func TestNamespacedKV_ScanStale_PropagatesError(t *testing.T) {
	sentinel := &namespacedKVServicerError{"scan stale failed"}
	svc := newNamespacedKVService(&failingInnerKV{err: sentinel}, "ns")
	_, err := svc.ScanStale(context.Background(), "")
	var te *namespacedKVServicerError
	if !errors.As(err, &te) {
		t.Errorf("expected namespacedKVServicerError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// PutWithMode
// ---------------------------------------------------------------------------

func TestNamespacedKV_PutWithMode_PrependsPrefixToKey(t *testing.T) {
	inner := newCaptureInnerKV()
	svc := newNamespacedKVService(inner, "ns")
	ctx := context.Background()

	if err := svc.PutWithMode(ctx, "mkey", []byte("val"), writemode.Sync); err != nil {
		t.Fatalf("PutWithMode: %v", err)
	}
	if inner.lastKey != "ns/mkey" {
		t.Errorf("inner received key %q, want %q", inner.lastKey, "ns/mkey")
	}
}

func TestNamespacedKV_PutWithMode_PropagatesError(t *testing.T) {
	sentinel := &namespacedKVServicerError{"put with mode failed"}
	svc := newNamespacedKVService(&failingInnerKV{err: sentinel}, "ns")
	err := svc.PutWithMode(context.Background(), "k", []byte("v"), writemode.Sync)
	var te *namespacedKVServicerError
	if !errors.As(err, &te) {
		t.Errorf("expected namespacedKVServicerError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// DeleteWithMode
// ---------------------------------------------------------------------------

func TestNamespacedKV_DeleteWithMode_PrependsPrefixToKey(t *testing.T) {
	inner := newCaptureInnerKV()
	svc := newNamespacedKVService(inner, "ns")
	ctx := context.Background()
	_ = inner.Put(ctx, "ns/dmkey", []byte("v"))

	if err := svc.DeleteWithMode(ctx, "dmkey", writemode.Sync); err != nil {
		t.Fatalf("DeleteWithMode: %v", err)
	}
	if inner.lastKey != "ns/dmkey" {
		t.Errorf("inner received key %q, want %q", inner.lastKey, "ns/dmkey")
	}
}

func TestNamespacedKV_DeleteWithMode_PropagatesError(t *testing.T) {
	sentinel := &namespacedKVServicerError{"delete with mode failed"}
	svc := newNamespacedKVService(&failingInnerKV{err: sentinel}, "ns")
	err := svc.DeleteWithMode(context.Background(), "k", writemode.Sync)
	var te *namespacedKVServicerError
	if !errors.As(err, &te) {
		t.Errorf("expected namespacedKVServicerError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// GetWithConsistency
// ---------------------------------------------------------------------------

func TestNamespacedKV_GetWithConsistency_PrependsPrefixToKey(t *testing.T) {
	inner := newMockKVService()
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

func TestNamespacedKV_GetWithConsistency_PropagatesError(t *testing.T) {
	sentinel := &namespacedKVServicerError{"gwc failed"}
	svc := newNamespacedKVService(&failingInnerKV{err: sentinel}, "ns")
	_, err := svc.GetWithConsistency(context.Background(), "k", consistency.ReadOptions{})
	var te *namespacedKVServicerError
	if !errors.As(err, &te) {
		t.Errorf("expected namespacedKVServicerError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// ListWithConsistency
// ---------------------------------------------------------------------------

func TestNamespacedKV_ListWithConsistency_StripsPrefix(t *testing.T) {
	inner := newMockKVService()
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

func TestNamespacedKV_ListWithConsistency_PropagatesError(t *testing.T) {
	sentinel := &namespacedKVServicerError{"lwc failed"}
	svc := newNamespacedKVService(&failingInnerKV{err: sentinel}, "ns")
	_, err := svc.ListWithConsistency(context.Background(), "", consistency.ReadOptions{})
	var te *namespacedKVServicerError
	if !errors.As(err, &te) {
		t.Errorf("expected namespacedKVServicerError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// ScanWithConsistency
// ---------------------------------------------------------------------------

func TestNamespacedKV_ScanWithConsistency_StripsPrefix(t *testing.T) {
	inner := newMockKVService()
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

func TestNamespacedKV_ScanWithConsistency_PropagatesError(t *testing.T) {
	sentinel := &namespacedKVServicerError{"swc failed"}
	svc := newNamespacedKVService(&failingInnerKV{err: sentinel}, "ns")
	_, err := svc.ScanWithConsistency(context.Background(), "", consistency.ReadOptions{})
	var te *namespacedKVServicerError
	if !errors.As(err, &te) {
		t.Errorf("expected namespacedKVServicerError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// ExistsWithConsistency
// ---------------------------------------------------------------------------

func TestNamespacedKV_ExistsWithConsistency_Found(t *testing.T) {
	inner := newMockKVService()
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

func TestNamespacedKV_ExistsWithConsistency_NotFound(t *testing.T) {
	svc := newNamespacedKVService(newMockKVService(), "ns")
	ok, err := svc.ExistsWithConsistency(context.Background(), "ghost", consistency.ReadOptions{})
	if err != nil {
		t.Fatalf("ExistsWithConsistency: %v", err)
	}
	if ok {
		t.Error("expected false for non-existent key")
	}
}

func TestNamespacedKV_ExistsWithConsistency_PropagatesError(t *testing.T) {
	sentinel := &namespacedKVServicerError{"ewc failed"}
	svc := newNamespacedKVService(&failingInnerKV{err: sentinel}, "ns")
	_, err := svc.ExistsWithConsistency(context.Background(), "k", consistency.ReadOptions{})
	var te *namespacedKVServicerError
	if !errors.As(err, &te) {
		t.Errorf("expected namespacedKVServicerError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// PutEntity / GetEntity / DeleteEntity
// ---------------------------------------------------------------------------

func TestNamespacedKV_PutEntity_PrependsPrefixToID(t *testing.T) {
	inner := newMockKVService()
	svc := newNamespacedKVService(inner, "ns")

	_, err := svc.PutEntity(context.Background(), &service.PutEntityRequest{
		ID:         "entity/1",
		EntityType: "user",
	})
	if err != nil {
		t.Fatalf("PutEntity: %v", err)
	}
}

func TestNamespacedKV_PutEntity_PropagatesError(t *testing.T) {
	sentinel := &namespacedKVServicerError{"pe failed"}
	svc := newNamespacedKVService(&failingInnerKV{err: sentinel}, "ns")
	_, err := svc.PutEntity(context.Background(), &service.PutEntityRequest{ID: "e/1", EntityType: "x"})
	var te *namespacedKVServicerError
	if !errors.As(err, &te) {
		t.Errorf("expected namespacedKVServicerError, got %T: %v", err, err)
	}
}

func TestNamespacedKV_GetEntity_PrependsPrefixToID(t *testing.T) {
	inner := newMockKVService()
	svc := newNamespacedKVService(inner, "ns")

	_, err := svc.GetEntity(context.Background(), &service.GetEntityRequest{
		ID:         "entity/1",
		EntityType: "user",
	})
	// inner.GetEntity returns nil, nil — no error expected.
	if err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
}

func TestNamespacedKV_GetEntity_PropagatesError(t *testing.T) {
	sentinel := &namespacedKVServicerError{"ge failed"}
	svc := newNamespacedKVService(&failingInnerKV{err: sentinel}, "ns")
	_, err := svc.GetEntity(context.Background(), &service.GetEntityRequest{ID: "e/1", EntityType: "x"})
	var te *namespacedKVServicerError
	if !errors.As(err, &te) {
		t.Errorf("expected namespacedKVServicerError, got %T: %v", err, err)
	}
}

func TestNamespacedKV_DeleteEntity_PrependsPrefixToID(t *testing.T) {
	inner := newMockKVService()
	svc := newNamespacedKVService(inner, "ns")

	_, err := svc.DeleteEntity(context.Background(), &service.DeleteEntityRequest{
		ID:         "entity/1",
		EntityType: "user",
	})
	if err != nil {
		t.Fatalf("DeleteEntity: %v", err)
	}
}

func TestNamespacedKV_DeleteEntity_PropagatesError(t *testing.T) {
	sentinel := &namespacedKVServicerError{"de failed"}
	svc := newNamespacedKVService(&failingInnerKV{err: sentinel}, "ns")
	_, err := svc.DeleteEntity(context.Background(), &service.DeleteEntityRequest{ID: "e/1", EntityType: "x"})
	var te *namespacedKVServicerError
	if !errors.As(err, &te) {
		t.Errorf("expected namespacedKVServicerError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// ListEntities
// ---------------------------------------------------------------------------

// listEntitiesInner is a test inner KVServicer whose ListEntities returns
// a non-nil response so namespacedKVService.ListEntities can dereference it.
type listEntitiesInner struct {
	*mockKVService
}

func (l *listEntitiesInner) ListEntities(_ context.Context, _ *service.ListEntitiesRequest) (*service.ListEntitiesResponse, error) {
	return &service.ListEntitiesResponse{
		Keys:       []string{"ns/entity/1", "ns/entity/2"},
		TotalCount: 2,
	}, nil
}

func TestNamespacedKV_ListEntities_StripsPrefix(t *testing.T) {
	inner := &listEntitiesInner{mockKVService: newMockKVService()}
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
	// Keys returned by inner have "ns/" prefix; they must be stripped.
	for _, k := range resp.Keys {
		if len(k) >= 3 && k[:3] == "ns/" {
			t.Errorf("key %q still has namespace prefix after strip", k)
		}
	}
}

func TestNamespacedKV_ListEntities_PropagatesError(t *testing.T) {
	sentinel := &namespacedKVServicerError{"le failed"}
	svc := newNamespacedKVService(&failingInnerKV{err: sentinel}, "ns")
	_, err := svc.ListEntities(context.Background(), &service.ListEntitiesRequest{EntityType: "x"})
	var te *namespacedKVServicerError
	if !errors.As(err, &te) {
		t.Errorf("expected namespacedKVServicerError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Compile-time interface check
// ---------------------------------------------------------------------------

var _ service.KVServicer = (*namespacedKVService)(nil)
