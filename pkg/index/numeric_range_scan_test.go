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

package index

import (
	"errors"
	"fmt"
	"sort"
	"testing"
)

// ---------------------------------------------------------------------------
// Regression coverage for the numeric secondary-index exclusive-range bug.
//
// pkg/sql/provider/range_translator.go expresses an exclusive Scan bound by
// appending a trailing 0x00 byte to the bound's decimal text (see
// appendExclusiveSuffix there). For string-typed indexes this is correct:
// the raw bytes are compared lexicographically. For numeric-typed indexes,
// Scan must parse the bound text as a float before it can be compared, and
// "5\x00" is not valid float syntax — so the exclusivity marker has to be
// resolved AFTER encoding, not before. These tests populate a real
// PebbleIndex/StoragePebbleIndex the same way scanRange in range_translator.go
// does (see appendExclusiveSuffix's doc) and assert the exact row set
// returned, so a regression collapses back to an empty (or wrongly-shaped)
// result rather than a passing-by-coincidence assertion.
// ---------------------------------------------------------------------------

// populateNumericFixture inserts a fixed set of numeric values — integers,
// negatives (exercising the sign-flip encoding path), floats, and values
// straddling zero — keyed by their own decimal text so test assertions can
// name expected members directly.
func populateNumericFixture(t *testing.T, idx Index) {
	t.Helper()
	for _, v := range []string{"-10", "-3", "-0.5", "0", "0.5", "1", "3.5", "5", "10", "15", "100"} {
		if err := idx.Put([]byte("key:"+v), []byte(v)); err != nil {
			t.Fatalf("Put(%s): %v", v, err)
		}
	}
}

// scanValues flattens a Scan result's map keys (the decoded decimal field
// values) into a sorted slice for easy comparison.
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

// numericIndexUnderTest is satisfied by both PebbleIndex and
// StoragePebbleIndex; the fixture/assertions below are shared across both
// concrete numeric-capable index implementations.
type numericIndexUnderTest struct {
	name string
	idx  Index
}

func numericIndexImpls(t *testing.T) []numericIndexUnderTest {
	t.Helper()
	pebbleIdx, _ := NewPebbleIndex(openPebble(t), "n", false, IndexValueTypeNumeric)
	storageIdx := newTestStoragePebbleIndex(t, IndexConfig{
		Name:      "n",
		Type:      IndexTypeNormal,
		ValueType: IndexValueTypeNumeric,
	})
	return []numericIndexUnderTest{
		{name: "PebbleIndex", idx: pebbleIdx},
		{name: "StoragePebbleIndex", idx: storageIdx},
	}
}

func TestNumericIndex_Scan_GreaterThan_ExcludesBoundary(t *testing.T) {
	for _, tc := range numericIndexImpls(t) {
		t.Run(tc.name, func(t *testing.T) {
			populateNumericFixture(t, tc.idx)

			// "n > 5": exclusive lower bound, unbounded upper — exactly what
			// pkg/sql/provider/range_translator.go's RangeType_GreaterThan
			// branch produces (extractLowerBytes + Scan(start, nil)).
			start := appendExclusiveSuffix([]byte("5"))
			result, err := tc.idx.Scan(start, nil)
			if err != nil {
				t.Fatalf("Scan(n > 5): unexpected error: %v", err)
			}
			assertValues(t, scanValues(t, result), []string{"10", "15", "100"})
		})
	}
}

func TestNumericIndex_Scan_GreaterOrEqual_IncludesBoundary(t *testing.T) {
	for _, tc := range numericIndexImpls(t) {
		t.Run(tc.name, func(t *testing.T) {
			populateNumericFixture(t, tc.idx)

			// "n >= 5": inclusive lower bound (no suffix), unbounded upper.
			result, err := tc.idx.Scan([]byte("5"), nil)
			if err != nil {
				t.Fatalf("Scan(n >= 5): unexpected error: %v", err)
			}
			assertValues(t, scanValues(t, result), []string{"5", "10", "15", "100"})
		})
	}
}

func TestNumericIndex_Scan_LessThan_ExcludesBoundary(t *testing.T) {
	for _, tc := range numericIndexImpls(t) {
		t.Run(tc.name, func(t *testing.T) {
			populateNumericFixture(t, tc.idx)

			// "n < 5": unbounded lower, exclusive upper (raw text, no suffix
			// — matches RangeType_LessThanOrNull's extractUpperBytes).
			result, err := tc.idx.Scan(nil, []byte("5"))
			if err != nil {
				t.Fatalf("Scan(n < 5): unexpected error: %v", err)
			}
			assertValues(t, scanValues(t, result), []string{"-10", "-3", "-0.5", "0", "0.5", "1", "3.5"})
		})
	}
}

func TestNumericIndex_Scan_LessOrEqual_IncludesBoundary(t *testing.T) {
	for _, tc := range numericIndexImpls(t) {
		t.Run(tc.name, func(t *testing.T) {
			populateNumericFixture(t, tc.idx)

			// "n <= 5": unbounded lower, inclusive upper (suffixed — matches
			// RangeType_LessOrEqualOrNull's extractUpperBytesInclusive).
			end := appendExclusiveSuffix([]byte("5"))
			result, err := tc.idx.Scan(nil, end)
			if err != nil {
				t.Fatalf("Scan(n <= 5): unexpected error: %v", err)
			}
			assertValues(t, scanValues(t, result), []string{"-10", "-3", "-0.5", "0", "0.5", "1", "3.5", "5"})
		})
	}
}

func TestNumericIndex_Scan_Between_InclusiveBothEnds(t *testing.T) {
	for _, tc := range numericIndexImpls(t) {
		t.Run(tc.name, func(t *testing.T) {
			populateNumericFixture(t, tc.idx)

			// BETWEEN 1 AND 10 -> ClosedClosed, unequal bounds -> scanRange
			// with lowerInclusive=true, upperInclusive=true. Lower is raw
			// text; upper gets the inclusive-suffix treatment.
			start := []byte("1")
			end := appendExclusiveSuffix([]byte("10"))
			result, err := tc.idx.Scan(start, end)
			if err != nil {
				t.Fatalf("Scan(BETWEEN 1 AND 10): unexpected error: %v", err)
			}
			assertValues(t, scanValues(t, result), []string{"1", "3.5", "5", "10"})
		})
	}
}

func TestNumericIndex_Scan_OpenOpen_ExcludesBothEndpoints(t *testing.T) {
	for _, tc := range numericIndexImpls(t) {
		t.Run(tc.name, func(t *testing.T) {
			populateNumericFixture(t, tc.idx)

			// 1 < n < 10 -> OpenOpen -> scanRange(false, false): both ends
			// get the exclusive-suffix treatment.
			start := appendExclusiveSuffix([]byte("1"))
			end := []byte("10")
			result, err := tc.idx.Scan(start, end)
			if err != nil {
				t.Fatalf("Scan(1 < n < 10): unexpected error: %v", err)
			}
			// Differs from the inclusive BETWEEN case above by exactly the
			// two endpoints (1 and 10 are excluded here).
			assertValues(t, scanValues(t, result), []string{"3.5", "5"})
		})
	}
}

func TestNumericIndex_Scan_NegativeBoundary_SignFlip(t *testing.T) {
	for _, tc := range numericIndexImpls(t) {
		t.Run(tc.name, func(t *testing.T) {
			populateNumericFixture(t, tc.idx)

			// "n > -3": exclusive lower bound crossing the sign-flip
			// boundary used to encode negative numbers.
			start := appendExclusiveSuffix([]byte("-3"))
			result, err := tc.idx.Scan(start, nil)
			if err != nil {
				t.Fatalf("Scan(n > -3): unexpected error: %v", err)
			}
			assertValues(t, scanValues(t, result), []string{"-0.5", "0", "0.5", "1", "3.5", "5", "10", "15", "100"})
		})
	}
}

func TestNumericIndex_Scan_StraddlingZero(t *testing.T) {
	for _, tc := range numericIndexImpls(t) {
		t.Run(tc.name, func(t *testing.T) {
			populateNumericFixture(t, tc.idx)

			// -0.5 < n < 0.5 -> excludes both fractional endpoints but keeps
			// the exact integer 0 in between.
			start := appendExclusiveSuffix([]byte("-0.5"))
			end := []byte("0.5")
			result, err := tc.idx.Scan(start, end)
			if err != nil {
				t.Fatalf("Scan(-0.5 < n < 0.5): unexpected error: %v", err)
			}
			assertValues(t, scanValues(t, result), []string{"0"})
		})
	}
}

func TestNumericIndex_Scan_GenuinelyEmptyResult(t *testing.T) {
	for _, tc := range numericIndexImpls(t) {
		t.Run(tc.name, func(t *testing.T) {
			populateNumericFixture(t, tc.idx)

			// "n > 1000": a legitimately empty result — no matching rows —
			// must come back as an empty map with a nil error, exactly the
			// same shape as the bug's silent-failure case, so this test
			// pins down that a real empty result is not itself broken.
			start := appendExclusiveSuffix([]byte("1000"))
			result, err := tc.idx.Scan(start, nil)
			if err != nil {
				t.Fatalf("Scan(n > 1000): unexpected error: %v", err)
			}
			if len(result) != 0 {
				t.Fatalf("Scan(n > 1000): got %d entries, want 0 (genuinely empty)", len(result))
			}
		})
	}
}

func TestNumericIndex_Scan_BoundaryExclusiveVsInclusive_DifferByExactlyEndpoint(t *testing.T) {
	for _, tc := range numericIndexImpls(t) {
		t.Run(tc.name, func(t *testing.T) {
			populateNumericFixture(t, tc.idx)

			gt, err := tc.idx.Scan(appendExclusiveSuffix([]byte("5")), nil)
			if err != nil {
				t.Fatalf("Scan(n > 5): unexpected error: %v", err)
			}
			gte, err := tc.idx.Scan([]byte("5"), nil)
			if err != nil {
				t.Fatalf("Scan(n >= 5): unexpected error: %v", err)
			}
			if len(gte) != len(gt)+1 {
				t.Fatalf("n >= 5 (%d rows) should have exactly one more row than n > 5 (%d rows)", len(gte), len(gt))
			}
			if _, ok := gte["5"]; !ok {
				t.Fatal("n >= 5 must include the boundary value 5")
			}
			if _, ok := gt["5"]; ok {
				t.Fatal("n > 5 must exclude the boundary value 5")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// String and case-insensitive string indexes must be unaffected: the
// exclusivity marker is (and remains) a pure byte-comparable trick for
// those value types.
// ---------------------------------------------------------------------------

// NOTE: these tests intentionally stick to scan shapes that were already
// correct before this fix (natural half-open exclusive upper bound with no
// marker, and the [nil, ...) / [..., nil) unbounded-side handling this fix
// adds uniformly for every value type). They do NOT exercise
// appendExclusiveSuffix as a LOWER bound, or as an "inclusive upper" bound,
// on a string index: testing during this fix surfaced that those two shapes
// are already broken independently of numeric encoding — buildScanBound
// deliberately omits the ':' separator (0x3A) so that value-prefix scans
// compare correctly, but that same omission means a trailing 0x00 marker
// sorts *before* the real separator byte of any composite key sharing that
// value, which inverts the intended inclusion/exclusion on that side. This
// is a distinct, pre-existing defect (not a numeric ParseFloat failure) that
// affects every value type equally and needs its own fix/decision; it is
// out of scope here and is called out separately.
func TestStringIndex_Scan_UnboundedUpper_Unaffected(t *testing.T) {
	idx, _ := newPebbleIdx(t, "s", false)
	for _, v := range []string{"apple", "banana", "cherry", "date"} {
		if err := idx.Put([]byte("key:"+v), []byte(v)); err != nil {
			t.Fatalf("Put(%s): %v", v, err)
		}
	}

	// [cherry, +inf): natural half-open lower bound, no marker involved,
	// plus the nil-upper-means-unbounded handling this fix adds.
	result, err := idx.Scan([]byte("cherry"), nil)
	if err != nil {
		t.Fatalf("Scan(s >= cherry): unexpected error: %v", err)
	}
	assertValues(t, scanValues(t, result), []string{"cherry", "date"})
}

func TestStringIndex_Scan_UnboundedLower_ExclusiveUpper_Unaffected(t *testing.T) {
	idx, _ := newPebbleIdx(t, "s", false)
	for _, v := range []string{"apple", "banana", "cherry", "date"} {
		if err := idx.Put([]byte("key:"+v), []byte(v)); err != nil {
			t.Fatalf("Put(%s): %v", v, err)
		}
	}

	// (-inf, cherry): nil lower means unbounded; unmarked upper bound is
	// naturally exclusive because a stored composite key extends the value
	// as a strict prefix, which already sorted correctly before this fix.
	result, err := idx.Scan(nil, []byte("cherry"))
	if err != nil {
		t.Fatalf("Scan(s < cherry): unexpected error: %v", err)
	}
	assertValues(t, scanValues(t, result), []string{"apple", "banana"})
}

func TestCaseInsensitiveStringIndex_Scan_UnboundedUpper_Unaffected(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, IndexConfig{
		Name:          "ci",
		Type:          IndexTypeNormal,
		ValueType:     IndexValueTypeString,
		CaseSensitive: false,
	})
	for _, v := range []string{"Apple", "Banana", "Cherry", "Date"} {
		if err := idx.Put([]byte("entity:"+v), []byte(v)); err != nil {
			t.Fatalf("Put(%s): %v", v, err)
		}
	}

	// Case-insensitive index normalizes to lowercase on Put; scan bounds are
	// normalized the same way. Mixed-case input plus a nil upper bound
	// (unbounded) must still resolve correctly.
	result, err := idx.Scan([]byte("Cherry"), nil)
	if err != nil {
		t.Fatalf("Scan(ci >= Cherry): unexpected error: %v", err)
	}
	got := make([]string, 0, len(result))
	for k := range result {
		got = append(got, k)
	}
	sort.Strings(got)
	assertValues(t, got, []string{"cherry", "date"})
	if keys, ok := result["cherry"]; !ok || len(keys) != 1 || string(keys[0]) != "entity:Cherry" {
		t.Errorf("Scan(ci >= Cherry): result[\"cherry\"] = %v, want [entity:Cherry]", result["cherry"])
	}
}

// ---------------------------------------------------------------------------
// Unparseable bound: must surface a typed error, never an empty result.
// ---------------------------------------------------------------------------

func TestNumericIndex_Scan_UnparseableBound_ReturnsTypedError(t *testing.T) {
	for _, tc := range numericIndexImpls(t) {
		t.Run(tc.name, func(t *testing.T) {
			populateNumericFixture(t, tc.idx)

			result, err := tc.idx.Scan([]byte("not-a-number"), nil)
			if err == nil {
				t.Fatalf("Scan(unparseable bound): expected error, got result %v", result)
			}
			var inb *InvalidNumericBoundError
			if !errors.As(err, &inb) {
				t.Fatalf("Scan(unparseable bound): expected *InvalidNumericBoundError, got %T: %v", err, err)
			}
			if result != nil {
				t.Fatalf("Scan(unparseable bound): expected nil result alongside the error, got %v", result)
			}
		})
	}
}

func TestNumericIndex_Scan_UnparseableExclusiveBound_ReturnsTypedError(t *testing.T) {
	for _, tc := range numericIndexImpls(t) {
		t.Run(tc.name, func(t *testing.T) {
			populateNumericFixture(t, tc.idx)

			// Even with the trailing exclusivity marker stripped, the base
			// text still must not parse as a number.
			_, err := tc.idx.Scan(appendExclusiveSuffix([]byte("not-a-number")), nil)
			var inb *InvalidNumericBoundError
			if !errors.As(err, &inb) {
				t.Fatalf("Scan(unparseable exclusive bound): expected *InvalidNumericBoundError, got %T: %v", err, err)
			}
		})
	}
}

// appendExclusiveSuffix mirrors pkg/sql/provider/range_translator.go's
// helper of the same name (unexported there; duplicated here so this file
// has no import-cycle-inducing dependency on pkg/sql/provider).
func appendExclusiveSuffix(b []byte) []byte {
	result := make([]byte, len(b)+1)
	copy(result, b)
	result[len(b)] = 0x00
	return result
}

// TestPebbleIndex_ResolveScanBound_NonNumericPassesThrough documents that
// resolveScanBound is a no-op for string-valued indexes.
func TestPebbleIndex_ResolveScanBound_NonNumericPassesThrough(t *testing.T) {
	idx, _ := newPebbleIdx(t, "s", false)
	got, err := idx.resolveScanBound([]byte("hello\x00"), "scan start")
	if err != nil {
		t.Fatalf("resolveScanBound: unexpected error: %v", err)
	}
	if string(got) != "hello\x00" {
		t.Fatalf("resolveScanBound: got %q, want %q", got, "hello\x00")
	}
}

func TestPebbleIndex_ResolveScanBound_NumericSuccessor(t *testing.T) {
	idx, _ := NewPebbleIndex(openPebble(t), "n", false, IndexValueTypeNumeric)

	base, err := EncodeNumericValue("5")
	if err != nil {
		t.Fatalf("EncodeNumericValue: %v", err)
	}
	want := incrementBytes(base)

	got, err := idx.resolveScanBound(appendExclusiveSuffix([]byte("5")), "scan start")
	if err != nil {
		t.Fatalf("resolveScanBound: unexpected error: %v", err)
	}
	if fmt.Sprintf("%x", got) != fmt.Sprintf("%x", want) {
		t.Fatalf("resolveScanBound: got % x, want % x (successor of encoded 5)", got, want)
	}
}
