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

// Package indextest holds the behaviour every index.Index implementation must
// share, as test suites each implementation runs against itself. It lets the
// implementations in pkg/index and in its backend subpackages be held to one
// contract without pkg/index importing those backends.
package indextest

import (
	"sort"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
)

// Factory builds a fresh, empty index from cfg for one test. It registers
// any cleanup the index needs with t.
type Factory func(t *testing.T, cfg index.IndexConfig) index.Index

// populateNumericFixture inserts a fixed set of numeric values — integers,
// negatives (exercising the sign-flip encoding path), floats, and values
// straddling zero — keyed by their own decimal text so test assertions can
// name expected members directly.
func populateNumericFixture(t *testing.T, idx index.Index) {
	t.Helper()
	for _, v := range []string{"-10", "-3", "-0.5", "0", "0.5", "1", "3.5", "5", "10", "15", "100"} {
		if err := idx.Put([]byte("key:"+v), []byte(v)); err != nil {
			t.Fatalf("Put(%s): %v", v, err)
		}
	}
}

// scanValues returns the sorted field values of a Scan result, asserting
// that each maps to exactly the entity key the fixtures store it under.
func scanValues(t *testing.T, result map[string][][]byte) []string {
	t.Helper()
	got := make([]string, 0, len(result))
	for k, keys := range result {
		if len(keys) != 1 {
			t.Fatalf("scanValues: expected exactly 1 entity key for value %q, got %d", k, len(keys))
		}
		want := "key:" + k
		if string(keys[0]) != want {
			t.Fatalf("scanValues: value %q mapped to entity key %q, want %q", k, keys[0], want)
		}
		got = append(got, k)
	}
	sort.Strings(got)
	return got
}

func assertValues(t *testing.T, got []string, want []string) {
	t.Helper()
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		// #nosec G602 -- the lengths are compared equal above, and i ranges over got.
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// AppendExclusiveSuffix marks b as an exclusive Scan bound the way
// pkg/sql/provider/range_translator.go does: a trailing 0x00 byte after the
// bound's text.
func AppendExclusiveSuffix(b []byte) []byte {
	result := make([]byte, len(b)+1)
	copy(result, b)
	result[len(b)] = 0x00
	return result
}
