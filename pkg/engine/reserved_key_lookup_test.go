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

package engine

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// This file is the READ half of the go-qrdb audit-replicated review's B2
// fix: a direct Lookup for a raw-keyspace key must answer not-found rather
// than the record, on both engine families, exactly as the WRITE side
// (kv_reserved_key_write_test.go) and the COMPARE side
// (reserved_key_compare_test.go) are refused. A record really is written
// under each reserved key first (bypassing the engine entirely, straight
// into storage), so the assertion is about the RESERVED PREFIX itself, not
// an accident of an empty table.

func TestMVCCEngine_Lookup_RefusesReservedKey(t *testing.T) {
	for name, key := range reservedTestKeys {
		t.Run(name, func(t *testing.T) {
			stg := memory.NewStorage()
			defer func() { _ = stg.Close() }()
			if err := stg.Set(key, []byte("already there")); err != nil {
				t.Fatalf("Set: %v", err)
			}
			eng, err := NewMVCCEngine(stg)
			if err != nil {
				t.Fatalf("NewMVCCEngine: %v", err)
			}

			if got, err := eng.Lookup(context.Background(), key); err != nil || got != nil {
				t.Fatalf("Lookup([]byte) = (%v, %v), want (nil, nil)", got, err)
			}
			if got, err := eng.Lookup(context.Background(), string(key)); err != nil || got != nil {
				t.Fatalf("Lookup(string) = (%v, %v), want (nil, nil)", got, err)
			}
		})
	}
}

func TestKVEngine_Lookup_RefusesReservedKey(t *testing.T) {
	for name, key := range reservedTestKeys {
		t.Run(name, func(t *testing.T) {
			stg := memory.NewStorage()
			defer func() { _ = stg.Close() }()
			if err := stg.Set(key, []byte("already there")); err != nil {
				t.Fatalf("Set: %v", err)
			}
			eng := NewKVEngine(stg)

			if got, err := eng.Lookup(context.Background(), key); err != nil || got != nil {
				t.Fatalf("Lookup([]byte) = (%v, %v), want (nil, nil)", got, err)
			}
		})
	}
}

func TestKVEngine_ListPrefix_ExcludesReservedKeys(t *testing.T) {
	for name, key := range reservedTestKeys {
		t.Run(name, func(t *testing.T) {
			stg := memory.NewStorage()
			defer func() { _ = stg.Close() }()
			if err := stg.Set(key, []byte("already there")); err != nil {
				t.Fatalf("Set: %v", err)
			}
			if err := stg.Set([]byte("ordinary"), []byte("v")); err != nil {
				t.Fatalf("Set: %v", err)
			}
			eng := NewKVEngine(stg)

			got, err := eng.Lookup(context.Background(), &statemachine.PrefixQuery{Prefix: []byte{}})
			if err != nil {
				t.Fatalf("Lookup(PrefixQuery): %v", err)
			}
			m, ok := got.(map[string][]byte)
			if !ok {
				t.Fatalf("Lookup(PrefixQuery) returned %T, want map[string][]byte", got)
			}
			if _, present := m[string(key)]; present {
				t.Fatalf("scanPrefix returned the reserved key %q", key)
			}
			if _, present := m["ordinary"]; !present {
				t.Fatal("scanPrefix dropped an ordinary key alongside the reserved one")
			}
		})
	}
}
