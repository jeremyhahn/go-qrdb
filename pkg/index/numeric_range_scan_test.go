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

package index_test

import (
	"sort"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/index/indextest"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// newStoragePebbleIndex builds a StoragePebbleIndex over a fresh pebble
// storage backend through the exported constructors only, as an
// indextest.Factory.
func newStoragePebbleIndex(t *testing.T, cfg index.IndexConfig) index.Index {
	t.Helper()
	s, err := pebbleStorage.New(pebbleStorage.TestConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("pebbleStorage.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	log, err := logger.New(logger.LogConfig{Level: "error", Format: "json", Output: "stdout"})
	if err != nil {
		t.Fatalf("logger.New: %v", err)
	}
	idx, err := index.NewStoragePebbleIndex(s, cfg, log)
	if err != nil {
		t.Fatalf("NewStoragePebbleIndex: %v", err)
	}
	return idx
}

// TestStoragePebbleIndex_NumericScan runs the numeric exclusive-range
// contract against StoragePebbleIndex.
func TestStoragePebbleIndex_NumericScan(t *testing.T) {
	indextest.NumericScan(t, newStoragePebbleIndex)
}

// TestCaseInsensitiveStringIndex_Scan_UnboundedUpper_Unaffected: a
// case-insensitive index normalizes to lowercase on Put; scan bounds are
// normalized the same way. Mixed-case input plus a nil upper bound
// (unbounded) must still resolve correctly.
func TestCaseInsensitiveStringIndex_Scan_UnboundedUpper_Unaffected(t *testing.T) {
	idx := newStoragePebbleIndex(t, index.IndexConfig{
		Name:          "ci",
		Type:          index.IndexTypeNormal,
		ValueType:     index.IndexValueTypeString,
		CaseSensitive: false,
	})
	for _, v := range []string{"Apple", "Banana", "Cherry", "Date"} {
		if err := idx.Put([]byte("entity:"+v), []byte(v)); err != nil {
			t.Fatalf("Put(%s): %v", v, err)
		}
	}

	result, err := idx.Scan([]byte("Cherry"), nil)
	if err != nil {
		t.Fatalf("Scan(ci >= Cherry): unexpected error: %v", err)
	}
	got := make([]string, 0, len(result))
	for k := range result {
		got = append(got, k)
	}
	sort.Strings(got)
	if len(got) != 2 || got[0] != "cherry" || got[1] != "date" {
		t.Fatalf("Scan(ci >= Cherry) = %v; want [cherry date]", got)
	}
	if keys, ok := result["cherry"]; !ok || len(keys) != 1 || string(keys[0]) != "entity:Cherry" {
		t.Errorf("Scan(ci >= Cherry): result[\"cherry\"] = %v, want [entity:Cherry]", result["cherry"])
	}
}
