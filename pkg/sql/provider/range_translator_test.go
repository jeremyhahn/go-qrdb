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

package provider

import (
	"context"
	"errors"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// mockIndexQueryer
// ---------------------------------------------------------------------------

// mockIndexQueryer is a test double for indexQueryer that captures calls
// and returns configurable results.
type mockIndexQueryer struct {
	queryFn func(ctx context.Context, indexName string, value []byte) ([][]byte, error)
	scanFn  func(ctx context.Context, indexName string, start, end []byte) (map[string][][]byte, error)
}

func (m *mockIndexQueryer) Query(ctx context.Context, indexName string, value []byte) ([][]byte, error) {
	if m.queryFn != nil {
		return m.queryFn(ctx, indexName, value)
	}
	return nil, nil
}

func (m *mockIndexQueryer) Scan(ctx context.Context, indexName string, start, end []byte) (map[string][][]byte, error) {
	if m.scanFn != nil {
		return m.scanFn(ctx, indexName, start, end)
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// Empty ranges
// ---------------------------------------------------------------------------

func TestTranslateRanges_Empty(t *testing.T) {
	svc := &mockIndexQueryer{}
	keys, err := translateRanges(context.Background(), svc, "idx", nil)
	require.NoError(t, err)
	assert.Nil(t, keys)
}

func TestTranslateRanges_EmptyMySQLRange(t *testing.T) {
	svc := &mockIndexQueryer{}
	emptyRange := sqle.MySQLRange{
		sqle.EmptyRangeColumnExpr(types.LongText),
	}
	keys, err := translateRanges(context.Background(), svc, "idx", []sqle.Range{emptyRange})
	require.NoError(t, err)
	assert.Nil(t, keys)
}

// ---------------------------------------------------------------------------
// Point lookup (ClosedClosed with equal bounds)
// ---------------------------------------------------------------------------

func TestTranslateRanges_PointLookup(t *testing.T) {
	called := false
	var capturedValue []byte
	svc := &mockIndexQueryer{
		queryFn: func(_ context.Context, _ string, value []byte) ([][]byte, error) {
			called = true
			capturedValue = value
			return [][]byte{[]byte("entity-1")}, nil
		},
	}

	// ClosedClosed with equal lower == upper is a point lookup.
	r := sqle.MySQLRange{
		sqle.ClosedRangeColumnExpr("alice@example.com", "alice@example.com", types.LongText),
	}
	keys, err := translateRanges(context.Background(), svc, "Email", []sqle.Range{r})
	require.NoError(t, err)
	assert.True(t, called, "Query should be called for equal-bounds ClosedClosed range")
	assert.Equal(t, []byte("alice@example.com"), capturedValue)
	require.Len(t, keys, 1)
	assert.Equal(t, []byte("entity-1"), keys[0])
}

// ---------------------------------------------------------------------------
// Range scan (ClosedClosed with different bounds)
// ---------------------------------------------------------------------------

func TestTranslateRanges_ClosedClosedRange(t *testing.T) {
	var capturedStart, capturedEnd []byte
	svc := &mockIndexQueryer{
		scanFn: func(_ context.Context, _ string, start, end []byte) (map[string][][]byte, error) {
			capturedStart = start
			capturedEnd = end
			return map[string][][]byte{
				"a": {[]byte("key-a")},
				"b": {[]byte("key-b")},
			}, nil
		},
	}

	r := sqle.MySQLRange{
		sqle.ClosedRangeColumnExpr("aaa", "zzz", types.LongText),
	}
	keys, err := translateRanges(context.Background(), svc, "Name", []sqle.Range{r})
	require.NoError(t, err)
	assert.NotNil(t, capturedStart)
	assert.NotNil(t, capturedEnd)
	assert.Len(t, keys, 2)
}

// ---------------------------------------------------------------------------
// GreaterThan range
// ---------------------------------------------------------------------------

func TestTranslateRanges_GreaterThan(t *testing.T) {
	var capturedStart []byte
	svc := &mockIndexQueryer{
		scanFn: func(_ context.Context, _ string, start, end []byte) (map[string][][]byte, error) {
			capturedStart = start
			assert.Nil(t, end, "GreaterThan should have nil upper bound")
			return map[string][][]byte{"c": {[]byte("key-c")}}, nil
		},
	}

	r := sqle.MySQLRange{
		sqle.GreaterThanRangeColumnExpr("m", types.LongText),
	}
	keys, err := translateRanges(context.Background(), svc, "Letter", []sqle.Range{r})
	require.NoError(t, err)
	assert.NotNil(t, capturedStart)
	require.Len(t, keys, 1)
	assert.Equal(t, []byte("key-c"), keys[0])
}

// ---------------------------------------------------------------------------
// GreaterOrEqual range
// ---------------------------------------------------------------------------

func TestTranslateRanges_GreaterOrEqual(t *testing.T) {
	var capturedStart []byte
	svc := &mockIndexQueryer{
		scanFn: func(_ context.Context, _ string, start, end []byte) (map[string][][]byte, error) {
			capturedStart = start
			return map[string][][]byte{"d": {[]byte("key-d")}}, nil
		},
	}

	r := sqle.MySQLRange{
		sqle.GreaterOrEqualRangeColumnExpr("m", types.LongText),
	}
	keys, err := translateRanges(context.Background(), svc, "Letter", []sqle.Range{r})
	require.NoError(t, err)
	// GreaterOrEqual uses the inclusive start value directly (no suffix).
	assert.Equal(t, []byte("m"), capturedStart)
	require.Len(t, keys, 1)
}

// ---------------------------------------------------------------------------
// LessThan (LessThanOrNull) range
// ---------------------------------------------------------------------------

func TestTranslateRanges_LessThan(t *testing.T) {
	var capturedEnd []byte
	svc := &mockIndexQueryer{
		scanFn: func(_ context.Context, _ string, start, end []byte) (map[string][][]byte, error) {
			assert.Nil(t, start, "LessThan should have nil lower bound")
			capturedEnd = end
			return map[string][][]byte{"e": {[]byte("key-e")}}, nil
		},
	}

	r := sqle.MySQLRange{
		sqle.LessThanRangeColumnExpr("m", types.LongText),
	}
	keys, err := translateRanges(context.Background(), svc, "Letter", []sqle.Range{r})
	require.NoError(t, err)
	assert.NotNil(t, capturedEnd)
	require.Len(t, keys, 1)
}

// ---------------------------------------------------------------------------
// LessOrEqual (LessOrEqualOrNull) range
// ---------------------------------------------------------------------------

func TestTranslateRanges_LessOrEqual(t *testing.T) {
	var capturedEnd []byte
	svc := &mockIndexQueryer{
		scanFn: func(_ context.Context, _ string, start, end []byte) (map[string][][]byte, error) {
			capturedEnd = end
			return map[string][][]byte{"f": {[]byte("key-f")}}, nil
		},
	}

	r := sqle.MySQLRange{
		sqle.LessOrEqualRangeColumnExpr("m", types.LongText),
	}
	keys, err := translateRanges(context.Background(), svc, "Letter", []sqle.Range{r})
	require.NoError(t, err)
	// LessOrEqual appends suffix to make upper bound inclusive.
	assert.NotNil(t, capturedEnd)
	require.Len(t, keys, 1)
}

// ---------------------------------------------------------------------------
// AllRange
// ---------------------------------------------------------------------------

func TestTranslateRanges_AllRange_ReturnsNil(t *testing.T) {
	svc := &mockIndexQueryer{}
	r := sqle.MySQLRange{
		sqle.AllRangeColumnExpr(types.LongText),
	}
	keys, err := translateRanges(context.Background(), svc, "Letter", []sqle.Range{r})
	require.NoError(t, err)
	// All-range returns nil to signal no index acceleration.
	assert.Nil(t, keys)
}

// ---------------------------------------------------------------------------
// NullRange
// ---------------------------------------------------------------------------

func TestTranslateRanges_NullRange_ReturnsNil(t *testing.T) {
	svc := &mockIndexQueryer{}
	r := sqle.MySQLRange{
		sqle.NullRangeColumnExpr(types.LongText),
	}
	keys, err := translateRanges(context.Background(), svc, "Letter", []sqle.Range{r})
	require.NoError(t, err)
	assert.Nil(t, keys)
}

// ---------------------------------------------------------------------------
// Deduplication across multiple ranges
// ---------------------------------------------------------------------------

func TestTranslateRanges_Deduplication(t *testing.T) {
	callCount := 0
	svc := &mockIndexQueryer{
		queryFn: func(_ context.Context, _ string, _ []byte) ([][]byte, error) {
			callCount++
			// Both ranges return the same key; deduplication should keep only one.
			return [][]byte{[]byte("shared-key")}, nil
		},
	}

	r1 := sqle.MySQLRange{sqle.ClosedRangeColumnExpr("alice@example.com", "alice@example.com", types.LongText)}
	r2 := sqle.MySQLRange{sqle.ClosedRangeColumnExpr("alice@example.com", "alice@example.com", types.LongText)}
	keys, err := translateRanges(context.Background(), svc, "Email", []sqle.Range{r1, r2})
	require.NoError(t, err)
	assert.Equal(t, 2, callCount)
	// Deduplicated: only one key returned.
	require.Len(t, keys, 1)
	assert.Equal(t, []byte("shared-key"), keys[0])
}

// ---------------------------------------------------------------------------
// Error propagation
// ---------------------------------------------------------------------------

func TestTranslateRanges_QueryError(t *testing.T) {
	svc := &mockIndexQueryer{
		queryFn: func(_ context.Context, _ string, _ []byte) ([][]byte, error) {
			return nil, errors.New("index unavailable")
		},
	}

	r := sqle.MySQLRange{sqle.ClosedRangeColumnExpr("x", "x", types.LongText)}
	_, err := translateRanges(context.Background(), svc, "Field", []sqle.Range{r})
	require.Error(t, err)
	var ie *IndexLookupError
	assert.True(t, errors.As(err, &ie))
}

func TestTranslateRanges_ScanError(t *testing.T) {
	svc := &mockIndexQueryer{
		scanFn: func(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
			return nil, errors.New("scan failed")
		},
	}

	r := sqle.MySQLRange{sqle.GreaterThanRangeColumnExpr("a", types.LongText)}
	_, err := translateRanges(context.Background(), svc, "Field", []sqle.Range{r})
	require.Error(t, err)
	var ie *IndexLookupError
	assert.True(t, errors.As(err, &ie))
}

// ---------------------------------------------------------------------------
// Non-MySQLRange type
// ---------------------------------------------------------------------------

func TestTranslateRanges_NonMySQLRange_Error(t *testing.T) {
	svc := &mockIndexQueryer{}
	_, err := translateRanges(context.Background(), svc, "Field", []sqle.Range{&badRange{}})
	require.Error(t, err)
	var rte *RangeTranslationError
	assert.True(t, errors.As(err, &rte))
}

// badRange is a test-only sqle.Range that is not a MySQLRange.
type badRange struct{}

func (b *badRange) String() string                     { return "bad" }
func (b *badRange) DebugString(_ *sqle.Context) string { return "bad" }
func (b *badRange) Equals(_ context.Context, other sqle.Range) (bool, error) {
	return false, nil
}

// ---------------------------------------------------------------------------
// LessThanOrNull range (BelowNull lower, Below upper)
// ---------------------------------------------------------------------------

func TestTranslateRanges_LessThanOrNull(t *testing.T) {
	var capturedEnd []byte
	svc := &mockIndexQueryer{
		scanFn: func(_ context.Context, _ string, start, end []byte) (map[string][][]byte, error) {
			assert.Nil(t, start)
			capturedEnd = end
			return map[string][][]byte{"x": {[]byte("key-x")}}, nil
		},
	}

	// Manually construct a LessThanOrNull range: {BelowNull, Below{Key:"m"}}
	r := sqle.MySQLRange{
		sqle.MySQLRangeColumnExpr{
			LowerBound: sqle.BelowNull{},
			UpperBound: sqle.Below{Key: "m"},
			Typ:        types.LongText,
		},
	}
	keys, err := translateRanges(context.Background(), svc, "Letter", []sqle.Range{r})
	require.NoError(t, err)
	assert.Equal(t, []byte("m"), capturedEnd)
	require.Len(t, keys, 1)
}

// ---------------------------------------------------------------------------
// LessOrEqualOrNull range (BelowNull lower, Above upper)
// ---------------------------------------------------------------------------

func TestTranslateRanges_LessOrEqualOrNull(t *testing.T) {
	var capturedEnd []byte
	svc := &mockIndexQueryer{
		scanFn: func(_ context.Context, _ string, start, end []byte) (map[string][][]byte, error) {
			assert.Nil(t, start)
			capturedEnd = end
			return map[string][][]byte{"y": {[]byte("key-y")}}, nil
		},
	}

	// Manually construct a LessOrEqualOrNull range: {BelowNull, Above{Key:"m"}}
	r := sqle.MySQLRange{
		sqle.MySQLRangeColumnExpr{
			LowerBound: sqle.BelowNull{},
			UpperBound: sqle.Above{Key: "m"},
			Typ:        types.LongText,
		},
	}
	keys, err := translateRanges(context.Background(), svc, "Letter", []sqle.Range{r})
	require.NoError(t, err)
	// LessOrEqualOrNull appends inclusive suffix.
	assert.Equal(t, []byte("m\x00"), capturedEnd)
	require.Len(t, keys, 1)
}

// ---------------------------------------------------------------------------
// OpenOpen range
// ---------------------------------------------------------------------------

func TestTranslateRanges_OpenOpen(t *testing.T) {
	svc := &mockIndexQueryer{
		scanFn: func(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
			return map[string][][]byte{"z": {[]byte("key-z")}}, nil
		},
	}

	r := sqle.MySQLRange{
		sqle.OpenRangeColumnExpr("a", "z", types.LongText),
	}
	keys, err := translateRanges(context.Background(), svc, "Letter", []sqle.Range{r})
	require.NoError(t, err)
	require.Len(t, keys, 1)
}

// ---------------------------------------------------------------------------
// OpenClosed range
// ---------------------------------------------------------------------------

func TestTranslateRanges_OpenClosed(t *testing.T) {
	svc := &mockIndexQueryer{
		scanFn: func(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
			return map[string][][]byte{"w": {[]byte("key-w")}}, nil
		},
	}

	// OpenClosed: {Above{lower}, Above{upper}} = (lower, upper]
	r := sqle.MySQLRange{
		sqle.MySQLRangeColumnExpr{
			LowerBound: sqle.Above{Key: "a"},
			UpperBound: sqle.Above{Key: "z"},
			Typ:        types.LongText,
		},
	}
	keys, err := translateRanges(context.Background(), svc, "Letter", []sqle.Range{r})
	require.NoError(t, err)
	require.Len(t, keys, 1)
}

// ---------------------------------------------------------------------------
// ClosedOpen range
// ---------------------------------------------------------------------------

func TestTranslateRanges_ClosedOpen(t *testing.T) {
	svc := &mockIndexQueryer{
		scanFn: func(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
			return map[string][][]byte{"v": {[]byte("key-v")}}, nil
		},
	}

	// ClosedOpen: {Below{lower}, Below{upper}} = [lower, upper)
	r := sqle.MySQLRange{
		sqle.MySQLRangeColumnExpr{
			LowerBound: sqle.Below{Key: "a"},
			UpperBound: sqle.Below{Key: "z"},
			Typ:        types.LongText,
		},
	}
	keys, err := translateRanges(context.Background(), svc, "Letter", []sqle.Range{r})
	require.NoError(t, err)
	require.Len(t, keys, 1)
}

// ---------------------------------------------------------------------------
// appendExclusiveSuffix
// ---------------------------------------------------------------------------

func TestAppendExclusiveSuffix_Basic(t *testing.T) {
	result := appendExclusiveSuffix([]byte("hello"))
	assert.Equal(t, []byte("hello\x00"), result)
}

func TestAppendExclusiveSuffix_Empty(t *testing.T) {
	result := appendExclusiveSuffix([]byte(""))
	assert.Equal(t, []byte{0x00}, result)
}

// ---------------------------------------------------------------------------
// flattenScanResult
// ---------------------------------------------------------------------------

func TestFlattenScanResult_Empty(t *testing.T) {
	result := flattenScanResult(nil)
	assert.Nil(t, result)
}

func TestFlattenScanResult_NonEmpty(t *testing.T) {
	input := map[string][][]byte{
		"a": {[]byte("k1"), []byte("k2")},
		"b": {[]byte("k3")},
	}
	keys := flattenScanResult(input)
	assert.Len(t, keys, 3)
}
