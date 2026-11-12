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

package placement

import (
	"context"
	"errors"
	"testing"
)

// errKV is a KV whose operations all fail with errBackend, exercising the
// store's error-wrapping paths. It optionally satisfies Watcher with a failing
// Watch when watch is true.
type errKV struct {
	watch bool
}

var errBackend = errors.New("backend boom")

func (e *errKV) Put(context.Context, string, []byte) error   { return errBackend }
func (e *errKV) Get(context.Context, string) ([]byte, error) { return nil, errBackend }
func (e *errKV) Delete(context.Context, string) error        { return errBackend }
func (e *errKV) Scan(context.Context, string, func(string, []byte) error) error {
	return errBackend
}

func (e *errKV) Watch(context.Context, string) (<-chan WatchEvent, error) {
	return nil, errBackend
}

// TestWithEpsilon verifies a positive epsilon is applied while a non-positive
// value leaves the default in place.
func TestWithEpsilon(t *testing.T) {
	topo := &mockTopology{}
	s, err := NewScheduler(topo, WithEpsilon(0.5))
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	if s.epsilon != 0.5 {
		t.Fatalf("epsilon = %v, want 0.5", s.epsilon)
	}

	s2, err := NewScheduler(topo, WithEpsilon(0))
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	if s2.epsilon != DefaultEpsilon {
		t.Fatalf("epsilon = %v, want default %v", s2.epsilon, DefaultEpsilon)
	}

	s3, _ := NewScheduler(topo, WithEpsilon(-1))
	if s3.epsilon != DefaultEpsilon {
		t.Fatalf("negative epsilon applied = %v, want default", s3.epsilon)
	}
}

// TestInsufficientNodesError_Error pins the human-readable message.
func TestInsufficientNodesError_Error(t *testing.T) {
	e := &InsufficientNodesError{Have: 1, Need: 3}
	want := "placement: insufficient healthy nodes: have 1, need 3"
	if e.Error() != want {
		t.Fatalf("Error() = %q, want %q", e.Error(), want)
	}
}

// TestStoreError_Error covers both the tenant/database and tenant-only renders.
func TestStoreError_Error(t *testing.T) {
	withDB := &StoreError{TenantID: "acme", DatabaseID: "orders", Op: "put", Err: errBackend}
	if got, want := withDB.Error(), "placement: put acme/orders: backend boom"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	tenantOnly := &StoreError{TenantID: "acme", Op: "list", Err: errBackend}
	if got, want := tenantOnly.Error(), "placement: list acme: backend boom"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(withDB, errBackend) {
		t.Fatalf("Unwrap did not expose underlying error")
	}
}

// TestSplitPlacementKey covers the key parser's accept and reject paths.
func TestSplitPlacementKey(t *testing.T) {
	cases := []struct {
		name          string
		key           string
		wantT, wantDB string
		wantOK        bool
	}{
		{"valid", PlacementPrefix + "acme/orders", "acme", "orders", true},
		{"wrong prefix", "other/acme/orders", "", "", false},
		{"too short", "x", "", "", false},
		{"no slash", PlacementPrefix + "acmeorders", "", "", false},
		{"empty tenant", PlacementPrefix + "/orders", "", "orders", false},
		{"empty database", PlacementPrefix + "acme/", "acme", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotT, gotDB, ok := splitPlacementKey(tc.key)
			if ok != tc.wantOK || gotT != tc.wantT || gotDB != tc.wantDB {
				t.Fatalf("splitPlacementKey(%q) = (%q,%q,%v), want (%q,%q,%v)",
					tc.key, gotT, gotDB, ok, tc.wantT, tc.wantDB, tc.wantOK)
			}
		})
	}
}

// TestStore_PutNil rejects a nil placement.
func TestStore_PutNil(t *testing.T) {
	s, _ := NewStore(newFakeKV())
	if err := s.Put(context.Background(), nil); !errors.Is(err, ErrPlacementNotFound) {
		t.Fatalf("Put(nil) error = %v, want ErrPlacementNotFound", err)
	}
}

// TestStore_BackendErrors verifies each operation wraps the backend failure in a
// StoreError that still matches the underlying cause.
func TestStore_BackendErrors(t *testing.T) {
	s, _ := NewStore(&errKV{})
	ctx := context.Background()
	p := samplePlacement("acme", "orders", 1)

	if err := s.Put(ctx, p); !errors.Is(err, errBackend) {
		t.Fatalf("Put error = %v, want errBackend", err)
	}
	if _, err := s.Get(ctx, "acme", "orders"); !errors.Is(err, errBackend) {
		t.Fatalf("Get error = %v, want errBackend", err)
	}
	if err := s.Delete(ctx, "acme", "orders"); !errors.Is(err, errBackend) {
		t.Fatalf("Delete error = %v, want errBackend", err)
	}
	if _, err := s.List(ctx); !errors.Is(err, errBackend) {
		t.Fatalf("List error = %v, want errBackend", err)
	}
	if _, err := s.ListForTenant(ctx, "acme"); !errors.Is(err, errBackend) {
		t.Fatalf("ListForTenant error = %v, want errBackend", err)
	}
}

// TestStore_GetDeleteValidation covers the empty-identity guards on Get/Delete.
func TestStore_GetDeleteValidation(t *testing.T) {
	s, _ := NewStore(newFakeKV())
	ctx := context.Background()
	if _, err := s.Get(ctx, "", "db"); !errors.Is(err, ErrEmptyTenant) {
		t.Errorf("Get empty tenant = %v, want ErrEmptyTenant", err)
	}
	if _, err := s.Get(ctx, "t", ""); !errors.Is(err, ErrEmptyDatabaseID) {
		t.Errorf("Get empty database = %v, want ErrEmptyDatabaseID", err)
	}
	if err := s.Delete(ctx, "", "db"); !errors.Is(err, ErrEmptyTenant) {
		t.Errorf("Delete empty tenant = %v, want ErrEmptyTenant", err)
	}
	if err := s.Delete(ctx, "t", ""); !errors.Is(err, ErrEmptyDatabaseID) {
		t.Errorf("Delete empty database = %v, want ErrEmptyDatabaseID", err)
	}
	if _, err := s.ListForTenant(ctx, ""); !errors.Is(err, ErrEmptyTenant) {
		t.Errorf("ListForTenant empty tenant = %v, want ErrEmptyTenant", err)
	}
}

// TestStore_DecodeErrors verifies an undecodable stored value fails Get/List
// rather than yielding a partial or corrupt view.
func TestStore_DecodeErrors(t *testing.T) {
	kv := newFakeKV()
	s, _ := NewStore(kv)
	ctx := context.Background()
	_ = kv.Put(ctx, placementKey("acme", "orders"), []byte("{not json"))

	if _, err := s.Get(ctx, "acme", "orders"); err == nil {
		t.Fatalf("Get of corrupt record returned nil error")
	}
	if _, err := s.List(ctx); err == nil {
		t.Fatalf("List over corrupt record returned nil error")
	}
	if _, err := s.ListForTenant(ctx, "acme"); err == nil {
		t.Fatalf("ListForTenant over corrupt record returned nil error")
	}
}

// TestStore_WatchBackendError surfaces a backend Watch failure as a StoreError.
func TestStore_WatchBackendError(t *testing.T) {
	s, _ := NewStore(&errKV{watch: true})
	_, err := s.Watch(context.Background())
	if !errors.Is(err, errBackend) {
		t.Fatalf("Watch error = %v, want errBackend", err)
	}
	var se *StoreError
	if !errors.As(err, &se) || se.Op != "watch" {
		t.Fatalf("Watch error = %v, want StoreError{Op: watch}", err)
	}
}

// TestStore_WatchContextCancel drains a closed feed: cancelling ctx closes the
// translated event channel.
func TestStore_WatchContextCancel(t *testing.T) {
	kv := newWatchKV()
	s, _ := NewStore(kv)
	ctx, cancel := context.WithCancel(context.Background())

	events, err := s.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	cancel()
	// Either ctx.Done fires or the source closes; both close the out channel.
	for range events {
	}
}

// TestStore_WatchClosedFeed closes the underlying feed and confirms the
// translated stream closes too.
func TestStore_WatchClosedFeed(t *testing.T) {
	kv := newWatchKV()
	s, _ := NewStore(kv)
	events, err := s.Watch(context.Background())
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	close(kv.ch)
	for range events {
	}
}

// TestTranslateEvent_UndecodableEvents covers the false-return paths.
func TestTranslateEvent_UndecodableEvents(t *testing.T) {
	cases := []struct {
		name string
		ev   WatchEvent
	}{
		{"put empty value", WatchEvent{Key: placementKey("a", "b")}},
		{"put bad value", WatchEvent{Key: placementKey("a", "b"), Value: []byte("{bad")}},
		{"delete unparseable key", WatchEvent{Deleted: true, Key: "not-a-placement-key"}},
		{"delete bad prev value", WatchEvent{Deleted: true, Key: "bad", PrevValue: []byte("{bad")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := translateEvent(tc.ev); ok {
				t.Fatalf("translateEvent(%+v) ok = true, want false", tc.ev)
			}
		})
	}
}

// TestTranslateEvent_DeleteFromKey recovers identity from the key when no prior
// value is supplied.
func TestTranslateEvent_DeleteFromKey(t *testing.T) {
	const id = "9f1c2e7a4b5d6c8e0a1b2c3d4e5f6a7b"
	ev, ok := translateEvent(WatchEvent{Deleted: true, Key: placementKey("acme", id)})
	if !ok || ev.Type != EventDelete || ev.TenantID != "acme" || ev.DatabaseID != id || ev.Database != "" {
		t.Fatalf("translateEvent = (%+v,%v), want EventDelete acme/%s with no name", ev, ok, id)
	}
}

// TestLessLoaded_TieBreaks verifies the ShardCount and NodeID tie-break tiers.
func TestLessLoaded_TieBreaks(t *testing.T) {
	// Equal bytes, fewer shards wins.
	a := NodeLoad{NodeID: "z", Bytes: 5, ShardCount: 1}
	b := NodeLoad{NodeID: "a", Bytes: 5, ShardCount: 9}
	if !lessLoaded(a, b) {
		t.Fatalf("lessLoaded should prefer fewer shards")
	}
	// Equal bytes and shards, lower NodeID wins.
	c := NodeLoad{NodeID: "a", Bytes: 5, ShardCount: 3}
	d := NodeLoad{NodeID: "b", Bytes: 5, ShardCount: 3}
	if !lessLoaded(c, d) {
		t.Fatalf("lessLoaded should break ties on NodeID")
	}
	if lessLoaded(d, c) {
		t.Fatalf("lessLoaded not antisymmetric on NodeID tie")
	}
}
