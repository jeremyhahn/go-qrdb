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

package service

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// hardConfinedKV wraps a KVServicer, refusing every method that takes a
// caller-supplied key or prefix and reaches into the hard-confined
// namespace (see hard_confined_keys.go) with *HardConfinedKeyError before
// ever delegating to the wrapped servicer.
//
// It exists for sdk/go/transport/embedded's un-namespaced services, NOT
// for pkg/server's registry: a namespaced KVServicer composite-prefixes
// every key before it reaches the shared store, which already makes a
// literal "__sys:cluster_ca/..." request physically harmless there (see
// hardConfinedPrefix's own doc comment). The embedded transport's
// services carry no such prefix.
//
// Point-key methods (Get, Put, PutIfAbsent, CompareAndSwap, CompareAndDelete,
// Delete, GetStale, PutWithMode, DeleteWithMode, GetWithConsistency,
// ExistsWithConsistency) are refused outright: there is nothing to filter
// out of a single-key answer. Enumeration methods (List, ListAll, Scan,
// ListStale, ScanStale, ListWithConsistency, ScanWithConsistency) filter
// the hard-confined key out of the result instead of refusing the whole
// call, so a caller's otherwise-legitimate "list everything" is not
// broken by the mere presence of a record it must not see.
//
// Entity methods (PutEntity, GetEntity, DeleteEntity, ListEntities) compose
// their storage key from EntityType and ID exactly the way Put/Get/Delete
// compose theirs from a caller-supplied string -- entityKey(entityType, id)
// is "{entityType}/{id}", so EntityType: "__sys:cluster_ca", ID: "material"
// addresses the real record precisely. They are NOT promoted through
// embedding: this package's own GetEntity/PutEntity/DeleteEntity are
// concrete methods that write and read through the backend directly, not
// through this interface's Get/Put/Delete, so embedding would bypass every
// refusal and filter above. PutEntity/GetEntity/DeleteEntity refuse
// outright, matching their point-key counterparts; ListEntities filters,
// matching List.
type hardConfinedKV struct {
	KVServicer
}

// NewHardConfinedKV wraps svc, or returns nil unchanged so a resolver that
// found no service configured for a scope keeps reporting that rather than
// a wrapped nil satisfying the interface.
func NewHardConfinedKV(svc KVServicer) KVServicer {
	if svc == nil {
		return nil
	}
	return &hardConfinedKV{KVServicer: svc}
}

func (h *hardConfinedKV) Put(ctx context.Context, key string, value []byte) error {
	if isHardConfinedKey(key) {
		return &HardConfinedKeyError{Key: key}
	}
	return h.KVServicer.Put(ctx, key, value)
}

func (h *hardConfinedKV) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if isHardConfinedKey(key) {
		return false, &HardConfinedKeyError{Key: key}
	}
	return h.KVServicer.PutIfAbsent(ctx, key, value)
}

func (h *hardConfinedKV) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if isHardConfinedKey(key) {
		return false, &HardConfinedKeyError{Key: key}
	}
	return h.KVServicer.CompareAndDelete(ctx, key, expected)
}

func (h *hardConfinedKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if isHardConfinedKey(key) {
		return false, &HardConfinedKeyError{Key: key}
	}
	return h.KVServicer.CompareAndSwap(ctx, key, expected, next)
}

func (h *hardConfinedKV) Get(ctx context.Context, key string) ([]byte, error) {
	if isHardConfinedKey(key) {
		return nil, &HardConfinedKeyError{Key: key}
	}
	return h.KVServicer.Get(ctx, key)
}

func (h *hardConfinedKV) Delete(ctx context.Context, key string) error {
	if isHardConfinedKey(key) {
		return &HardConfinedKeyError{Key: key}
	}
	return h.KVServicer.Delete(ctx, key)
}

func (h *hardConfinedKV) List(ctx context.Context, prefix string) ([]string, error) {
	if isHardConfinedKey(prefix) {
		return nil, &HardConfinedKeyError{Key: prefix}
	}
	keys, err := h.KVServicer.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	return filterHardConfinedKeys(keys), nil
}

func (h *hardConfinedKV) ListAll(ctx context.Context, prefix string) ([]string, error) {
	keys, err := h.KVServicer.ListAll(ctx, prefix)
	if err != nil {
		return nil, err
	}
	return filterHardConfinedKeys(keys), nil
}

func (h *hardConfinedKV) Scan(ctx context.Context, prefix string) (map[string][]byte, error) {
	if isHardConfinedKey(prefix) {
		return nil, &HardConfinedKeyError{Key: prefix}
	}
	data, err := h.KVServicer.Scan(ctx, prefix)
	if err != nil {
		return nil, err
	}
	return filterHardConfinedMap(data), nil
}

func (h *hardConfinedKV) GetStale(ctx context.Context, key string) ([]byte, error) {
	if isHardConfinedKey(key) {
		return nil, &HardConfinedKeyError{Key: key}
	}
	return h.KVServicer.GetStale(ctx, key)
}

func (h *hardConfinedKV) ListStale(ctx context.Context, prefix string) ([]string, error) {
	if isHardConfinedKey(prefix) {
		return nil, &HardConfinedKeyError{Key: prefix}
	}
	keys, err := h.KVServicer.ListStale(ctx, prefix)
	if err != nil {
		return nil, err
	}
	return filterHardConfinedKeys(keys), nil
}

func (h *hardConfinedKV) ScanStale(ctx context.Context, prefix string) (map[string][]byte, error) {
	if isHardConfinedKey(prefix) {
		return nil, &HardConfinedKeyError{Key: prefix}
	}
	data, err := h.KVServicer.ScanStale(ctx, prefix)
	if err != nil {
		return nil, err
	}
	return filterHardConfinedMap(data), nil
}

func (h *hardConfinedKV) PutWithMode(ctx context.Context, key string, value []byte, mode writemode.Mode) error {
	if isHardConfinedKey(key) {
		return &HardConfinedKeyError{Key: key}
	}
	return h.KVServicer.PutWithMode(ctx, key, value, mode)
}

func (h *hardConfinedKV) DeleteWithMode(ctx context.Context, key string, mode writemode.Mode) error {
	if isHardConfinedKey(key) {
		return &HardConfinedKeyError{Key: key}
	}
	return h.KVServicer.DeleteWithMode(ctx, key, mode)
}

func (h *hardConfinedKV) GetWithConsistency(ctx context.Context, key string, opts consistency.ReadOptions) ([]byte, error) {
	if isHardConfinedKey(key) {
		return nil, &HardConfinedKeyError{Key: key}
	}
	return h.KVServicer.GetWithConsistency(ctx, key, opts)
}

func (h *hardConfinedKV) ListWithConsistency(ctx context.Context, prefix string, opts consistency.ReadOptions) ([]string, error) {
	if isHardConfinedKey(prefix) {
		return nil, &HardConfinedKeyError{Key: prefix}
	}
	keys, err := h.KVServicer.ListWithConsistency(ctx, prefix, opts)
	if err != nil {
		return nil, err
	}
	return filterHardConfinedKeys(keys), nil
}

func (h *hardConfinedKV) ScanWithConsistency(ctx context.Context, prefix string, opts consistency.ReadOptions) (map[string][]byte, error) {
	if isHardConfinedKey(prefix) {
		return nil, &HardConfinedKeyError{Key: prefix}
	}
	data, err := h.KVServicer.ScanWithConsistency(ctx, prefix, opts)
	if err != nil {
		return nil, err
	}
	return filterHardConfinedMap(data), nil
}

func (h *hardConfinedKV) ExistsWithConsistency(ctx context.Context, key string, opts consistency.ReadOptions) (bool, error) {
	if isHardConfinedKey(key) {
		return false, &HardConfinedKeyError{Key: key}
	}
	return h.KVServicer.ExistsWithConsistency(ctx, key, opts)
}

func (h *hardConfinedKV) PutEntity(ctx context.Context, req *PutEntityRequest) (*PutEntityResponse, error) {
	if req != nil {
		if key := entityKey(req.EntityType, req.ID); isHardConfinedKey(key) {
			return nil, &HardConfinedKeyError{Key: key}
		}
	}
	return h.KVServicer.PutEntity(ctx, req)
}

func (h *hardConfinedKV) GetEntity(ctx context.Context, req *GetEntityRequest) (*GetEntityResponse, error) {
	if req != nil {
		if key := entityKey(req.EntityType, req.ID); isHardConfinedKey(key) {
			return nil, &HardConfinedKeyError{Key: key}
		}
	}
	return h.KVServicer.GetEntity(ctx, req)
}

func (h *hardConfinedKV) DeleteEntity(ctx context.Context, req *DeleteEntityRequest) (*DeleteEntityResponse, error) {
	if req != nil {
		if key := entityKey(req.EntityType, req.ID); isHardConfinedKey(key) {
			return nil, &HardConfinedKeyError{Key: key}
		}
	}
	return h.KVServicer.DeleteEntity(ctx, req)
}

// ListEntities refuses outright when req.EntityType (plus any req.Prefix
// sub-filter) is itself entirely inside the confined namespace -- every
// result would be confined, matching List's own exact-prefix refusal --
// and otherwise filters confined entries out of the page, mirroring List.
// TotalCount is adjusted by the number removed from THIS page only: like
// List/Scan's callers, a caller needing an exact cross-page total re-derives
// it from filtered results, so no subtraction beyond what was actually
// filtered here is owed or attempted.
func (h *hardConfinedKV) ListEntities(ctx context.Context, req *ListEntitiesRequest) (*ListEntitiesResponse, error) {
	if req != nil {
		if composed := entityPrefix(req.EntityType) + req.Prefix; isHardConfinedKey(composed) {
			return nil, &HardConfinedKeyError{Key: composed}
		}
	}
	resp, err := h.KVServicer.ListEntities(ctx, req)
	if err != nil || resp == nil || req == nil {
		return resp, err
	}
	typePrefix := entityPrefix(req.EntityType)
	filtered := make([]string, 0, len(resp.Keys))
	for _, id := range resp.Keys {
		if isHardConfinedKey(typePrefix + id) {
			continue
		}
		filtered = append(filtered, id)
	}
	resp.TotalCount -= int64(len(resp.Keys) - len(filtered))
	resp.Keys = filtered
	return resp, nil
}
