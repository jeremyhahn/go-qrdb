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

package indextest

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
)

// NumericScan is regression coverage for the numeric secondary-index
// exclusive-range contract.
//
// pkg/sql/provider/range_translator.go expresses an exclusive Scan bound by
// appending a trailing 0x00 byte to the bound's decimal text (see
// AppendExclusiveSuffix). For string-typed indexes this is correct: the raw
// bytes are compared lexicographically. For numeric-typed indexes, Scan must
// parse the bound text as a float before it can be compared, and "5\x00" is
// not valid float syntax — so the exclusivity marker has to be resolved AFTER
// encoding, not before. Each case populates a numeric index built by newIndex
// the same way scanRange in range_translator.go does and asserts the exact row
// set returned, so a regression collapses back to an empty (or wrongly-shaped)
// result rather than a passing-by-coincidence assertion.
func NumericScan(t *testing.T, newIndex Factory) {
	t.Helper()
	cfg := index.IndexConfig{
		Name:            "n",
		Type:            index.IndexTypeNormal,
		ValueType:       index.IndexValueTypeNumeric,
		EncodingVersion: index.NumericEncodingVersion,
	}
	for _, tc := range []struct {
		name string
		run  func(t *testing.T, idx index.Index)
	}{
		{"GreaterThan_ExcludesBoundary", func(t *testing.T, idx index.Index) {
			// "n > 5": exclusive lower bound, unbounded upper — exactly what
			// pkg/sql/provider/range_translator.go's RangeType_GreaterThan
			// branch produces (extractLowerBytes + Scan(start, nil)).
			result, err := idx.Scan(AppendExclusiveSuffix([]byte("5")), nil)
			if err != nil {
				t.Fatalf("Scan(n > 5): unexpected error: %v", err)
			}
			assertValues(t, scanValues(t, result), []string{"10", "15", "100"})
		}},
		{"GreaterOrEqual_IncludesBoundary", func(t *testing.T, idx index.Index) {
			// "n >= 5": inclusive lower bound (no suffix), unbounded upper.
			result, err := idx.Scan([]byte("5"), nil)
			if err != nil {
				t.Fatalf("Scan(n >= 5): unexpected error: %v", err)
			}
			assertValues(t, scanValues(t, result), []string{"5", "10", "15", "100"})
		}},
		{"LessThan_ExcludesBoundary", func(t *testing.T, idx index.Index) {
			// "n < 5": unbounded lower, exclusive upper (raw text, no suffix
			// — matches RangeType_LessThanOrNull's extractUpperBytes).
			result, err := idx.Scan(nil, []byte("5"))
			if err != nil {
				t.Fatalf("Scan(n < 5): unexpected error: %v", err)
			}
			assertValues(t, scanValues(t, result), []string{"-10", "-3", "-0.5", "0", "0.5", "1", "3.5"})
		}},
		{"LessOrEqual_IncludesBoundary", func(t *testing.T, idx index.Index) {
			// "n <= 5": unbounded lower, inclusive upper (suffixed — matches
			// RangeType_LessOrEqualOrNull's extractUpperBytesInclusive).
			result, err := idx.Scan(nil, AppendExclusiveSuffix([]byte("5")))
			if err != nil {
				t.Fatalf("Scan(n <= 5): unexpected error: %v", err)
			}
			assertValues(t, scanValues(t, result), []string{"-10", "-3", "-0.5", "0", "0.5", "1", "3.5", "5"})
		}},
		{"Between_InclusiveBothEnds", func(t *testing.T, idx index.Index) {
			// BETWEEN 1 AND 10 -> ClosedClosed, unequal bounds -> scanRange
			// with lowerInclusive=true, upperInclusive=true. Lower is raw
			// text; upper gets the inclusive-suffix treatment.
			result, err := idx.Scan([]byte("1"), AppendExclusiveSuffix([]byte("10")))
			if err != nil {
				t.Fatalf("Scan(BETWEEN 1 AND 10): unexpected error: %v", err)
			}
			assertValues(t, scanValues(t, result), []string{"1", "3.5", "5", "10"})
		}},
		{"OpenOpen_ExcludesBothEndpoints", func(t *testing.T, idx index.Index) {
			// 1 < n < 10 -> OpenOpen -> scanRange(false, false): both ends
			// get the exclusive-suffix treatment.
			result, err := idx.Scan(AppendExclusiveSuffix([]byte("1")), []byte("10"))
			if err != nil {
				t.Fatalf("Scan(1 < n < 10): unexpected error: %v", err)
			}
			// Differs from the inclusive BETWEEN case above by exactly the
			// two endpoints (1 and 10 are excluded here).
			assertValues(t, scanValues(t, result), []string{"3.5", "5"})
		}},
		{"NegativeBoundary_SignFlip", func(t *testing.T, idx index.Index) {
			// "n > -3": exclusive lower bound crossing the sign-flip
			// boundary used to encode negative numbers.
			result, err := idx.Scan(AppendExclusiveSuffix([]byte("-3")), nil)
			if err != nil {
				t.Fatalf("Scan(n > -3): unexpected error: %v", err)
			}
			assertValues(t, scanValues(t, result), []string{"-0.5", "0", "0.5", "1", "3.5", "5", "10", "15", "100"})
		}},
		{"StraddlingZero", func(t *testing.T, idx index.Index) {
			// -0.5 < n < 0.5 -> excludes both fractional endpoints but keeps
			// the exact integer 0 in between.
			result, err := idx.Scan(AppendExclusiveSuffix([]byte("-0.5")), []byte("0.5"))
			if err != nil {
				t.Fatalf("Scan(-0.5 < n < 0.5): unexpected error: %v", err)
			}
			assertValues(t, scanValues(t, result), []string{"0"})
		}},
		{"GenuinelyEmptyResult", func(t *testing.T, idx index.Index) {
			// "n > 1000": a legitimately empty result — no matching rows —
			// must come back as an empty map with a nil error, exactly the
			// same shape as the bug's silent-failure case, so this case
			// pins down that a real empty result is not itself broken.
			result, err := idx.Scan(AppendExclusiveSuffix([]byte("1000")), nil)
			if err != nil {
				t.Fatalf("Scan(n > 1000): unexpected error: %v", err)
			}
			if len(result) != 0 {
				t.Fatalf("Scan(n > 1000): got %d entries, want 0 (genuinely empty)", len(result))
			}
		}},
		{"BoundaryExclusiveVsInclusive_DifferByExactlyEndpoint", func(t *testing.T, idx index.Index) {
			gt, err := idx.Scan(AppendExclusiveSuffix([]byte("5")), nil)
			if err != nil {
				t.Fatalf("Scan(n > 5): unexpected error: %v", err)
			}
			gte, err := idx.Scan([]byte("5"), nil)
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
		}},
		// An unparseable bound must surface a typed error, never an empty
		// result.
		{"UnparseableBound_ReturnsTypedError", func(t *testing.T, idx index.Index) {
			result, err := idx.Scan([]byte("not-a-number"), nil)
			if err == nil {
				t.Fatalf("Scan(unparseable bound): expected error, got result %v", result)
			}
			var inb *index.InvalidNumericBoundError
			if !errors.As(err, &inb) {
				t.Fatalf("Scan(unparseable bound): expected *index.InvalidNumericBoundError, got %T: %v", err, err)
			}
			if result != nil {
				t.Fatalf("Scan(unparseable bound): expected nil result alongside the error, got %v", result)
			}
		}},
		{"UnparseableExclusiveBound_ReturnsTypedError", func(t *testing.T, idx index.Index) {
			// Even with the trailing exclusivity marker stripped, the base
			// text still must not parse as a number.
			_, err := idx.Scan(AppendExclusiveSuffix([]byte("not-a-number")), nil)
			var inb *index.InvalidNumericBoundError
			if !errors.As(err, &inb) {
				t.Fatalf("Scan(unparseable exclusive bound): expected *index.InvalidNumericBoundError, got %T: %v", err, err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			idx := newIndex(t, cfg)
			populateNumericFixture(t, idx)
			tc.run(t, idx)
		})
	}
}

// NumericExactness runs the exactness contract of numeric index terms against
// an empty numeric index built by newIndex: values a float64 term ordered or
// collapsed wrongly come back exactly.
func NumericExactness(t *testing.T, newIndex Factory) {
	t.Helper()
	cfg := index.IndexConfig{
		Name:            "n",
		Type:            index.IndexTypeNormal,
		ValueType:       index.IndexValueTypeNumeric,
		EncodingVersion: index.NumericEncodingVersion,
	}
	for _, tc := range []struct {
		name       string
		values     []string
		start, end []byte
		want       []string
	}{
		// The reported failure: an indexed field holding 2 and 10, scanned
		// >= 2, returns both records.
		{"TwoAndTen", []string{"2", "10"}, []byte("2"), nil, []string{"2", "10"}},
		// Integers past 2^53 that a float64 term rounds together.
		{
			"BeyondFloatPrecision",
			[]string{"1700000000000000001", "1700000000000000002", "1700000000000000003"},
			AppendExclusiveSuffix([]byte("1700000000000000001")),
			[]byte("1700000000000000003"),
			[]string{"1700000000000000002"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			idx := newIndex(t, cfg)
			for _, v := range tc.values {
				if err := idx.Put([]byte("key:"+v), []byte(v)); err != nil {
					t.Fatalf("Put(%s): %v", v, err)
				}
			}
			result, err := idx.Scan(tc.start, tc.end)
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			assertValues(t, scanValues(t, result), tc.want)
		})
	}
}

// StringScan pins the scan shapes of a string index that the numeric
// exclusivity handling must leave unaffected.
//
// It intentionally sticks to shapes that are correct independently of the
// value type: a natural half-open exclusive upper bound with no marker, and
// the [nil, ...) / [..., nil) unbounded-side handling. It does NOT exercise
// AppendExclusiveSuffix as a LOWER bound, or as an "inclusive upper" bound, on
// a string index: the scan bound deliberately omits the ':' separator (0x3A)
// so that value-prefix scans compare correctly, but that same omission means a
// trailing 0x00 marker sorts *before* the real separator byte of any composite
// key sharing that value, which inverts the intended inclusion/exclusion on
// that side. That is a distinct defect affecting every value type equally.
func StringScan(t *testing.T, newIndex Factory) {
	t.Helper()
	cfg := index.IndexConfig{Name: "s", Type: index.IndexTypeNormal, ValueType: index.IndexValueTypeString, CaseSensitive: true}
	for _, tc := range []struct {
		name       string
		start, end []byte
		want       []string
	}{
		// [cherry, +inf): natural half-open lower bound, no marker involved,
		// plus the nil-upper-means-unbounded handling.
		{"UnboundedUpper_Unaffected", []byte("cherry"), nil, []string{"cherry", "date"}},
		// (-inf, cherry): nil lower means unbounded; an unmarked upper bound
		// is naturally exclusive because a stored composite key extends the
		// value as a strict prefix.
		{"UnboundedLower_ExclusiveUpper_Unaffected", nil, []byte("cherry"), []string{"apple", "banana"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			idx := newIndex(t, cfg)
			for _, v := range []string{"apple", "banana", "cherry", "date"} {
				if err := idx.Put([]byte("key:"+v), []byte(v)); err != nil {
					t.Fatalf("Put(%s): %v", v, err)
				}
			}
			result, err := idx.Scan(tc.start, tc.end)
			if err != nil {
				t.Fatalf("Scan(%q, %q): unexpected error: %v", tc.start, tc.end, err)
			}
			assertValues(t, scanValues(t, result), tc.want)
		})
	}
}
