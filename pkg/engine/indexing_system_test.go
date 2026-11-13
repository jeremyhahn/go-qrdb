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

package engine_test

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// recordingIndex is an index.Index that records every Put, so a test can see
// exactly which records a backfill indexed.
type recordingIndex struct {
	entries map[string][][]byte
}

func newRecordingIndex() *recordingIndex {
	return &recordingIndex{entries: map[string][][]byte{}}
}

func (r *recordingIndex) Put(key, value []byte) error {
	r.entries[string(value)] = append(r.entries[string(value)], append([]byte(nil), key...))
	return nil
}
func (r *recordingIndex) Get(value []byte) ([][]byte, error) { return r.entries[string(value)], nil }
func (r *recordingIndex) Delete(_, _ []byte) error           { return nil }
func (r *recordingIndex) Scan(_, _ []byte) (map[string][][]byte, error) {
	return r.entries, nil
}
func (r *recordingIndex) GetAll() ([][]byte, error) { return nil, nil }
func (r *recordingIndex) Clear() error              { return nil }
func (r *recordingIndex) Close() error              { return nil }

// TestSystemKeyPrefixIsTheReservedPrefix pins engine.IsSystemKey to the
// reserved namespace the credential store writes under.
func TestSystemKeyPrefixIsTheReservedPrefix(t *testing.T) {
	if !engine.IsSystemKey([]byte(kvstore.ReservedPrefixSystem + "credentials/x")) {
		t.Fatal("a key under kvstore.ReservedPrefixSystem is not a system key")
	}
	if engine.IsSystemKey([]byte("credentials/x")) {
		t.Fatal("an ordinary key is a system key")
	}
}

// TestBackfillIndex_BareIndexNeverIndexesACredentialRecord is the P0
// regression: a bare (unscoped) index on a credential field must not index
// the credential store's records, so no index scan can return the secret.
func TestBackfillIndex_BareIndexNeverIndexesACredentialRecord(t *testing.T) {
	kvs := []*service.KeyValue{
		{Key: []byte("__sys:credentials/x"), Value: []byte(`{"password":"secret"}`)},
		{Key: []byte("__sys:credential-meta/x"), Value: []byte(`{"password":"meta"}`)},
		{Key: []byte("__sys:credential-versions/x#v1"), Value: []byte(`{"password":"old"}`)},
		{Key: []byte("__sys:credential-staged/x"), Value: []byte(`{"password":"staged"}`)},
		{Key: []byte("users/alice"), Value: []byte(`{"password":"public"}`)},
	}
	idx := newRecordingIndex()
	engine.BackfillIndex(func() ([]*service.KeyValue, error) { return kvs, nil }, idx, "password", nil)

	if len(idx.entries) != 1 {
		t.Fatalf("indexed values = %v, want only the ordinary record", idx.entries)
	}
	keys := idx.entries["public"]
	if len(keys) != 1 || string(keys[0]) != "users/alice" {
		t.Fatalf("indexed keys = %q, want [users/alice]", keys)
	}
}

// TestKeyInScope_SystemKeyIsInNoScope proves the per-write maintenance path
// (which asks KeyInScope) never indexes a system key, bare index included.
func TestKeyInScope_SystemKeyIsInNoScope(t *testing.T) {
	key := []byte("__sys:credentials/x")
	for _, scope := range [][]byte{nil, []byte("__sys:"), []byte("__sys:credentials/")} {
		if engine.KeyInScope(scope, key) {
			t.Errorf("KeyInScope(%q, %q) = true, want false", scope, key)
		}
	}
	if !engine.KeyInScope(nil, []byte("users/alice")) {
		t.Error("an ordinary key is out of a bare index's scope")
	}
}
