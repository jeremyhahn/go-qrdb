//go:build qrdb_sql

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
	"sort"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
)

// ---------------------------------------------------------------------------
// End-to-end regression coverage: SQL range predicate -> translateMySQLRange
// -> a real numeric-typed PebbleIndex. This is the full path a query like
//
//	SELECT * FROM t WHERE n > 5
//
// takes through the index-acceleration layer, and is the deliverable
// regression test for the exclusive-numeric-range bug: it fails before the
// fix (empty result, nil error — a plausible-looking but wrong answer) and
// passes after.
// ---------------------------------------------------------------------------

// indexBackedQueryer adapts a single-field index.Index to the indexQueryer
// interface used by translateMySQLRange/scanRange, so these tests exercise
// the real Scan/Get implementation instead of a mock.
type indexBackedQueryer struct {
	idx index.Index
}

func (q *indexBackedQueryer) Query(_ context.Context, _ string, value []byte) ([][]byte, error) {
	return q.idx.Get(value)
}

func (q *indexBackedQueryer) Scan(_ context.Context, _ string, start, end []byte) (map[string][][]byte, error) {
	return q.idx.Scan(start, end)
}

// newNumericIndexQueryer builds a real PebbleIndex over an in-memory Pebble
// instance, populated with a fixed numeric fixture, wrapped as an
// indexQueryer.
func newNumericIndexQueryer(t *testing.T) *indexBackedQueryer {
	t.Helper()
	db, err := pebble.Open(t.TempDir(), &pebble.Options{})
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	idx, err := index.NewPebbleIndex(db, "n", false, index.IndexValueTypeNumeric)
	if err != nil {
		t.Fatalf("NewPebbleIndex: %v", err)
	}

	for _, v := range []string{"-10", "-3", "0", "1", "3.5", "5", "10", "15", "100"} {
		if err := idx.Put([]byte("row:"+v), []byte(v)); err != nil {
			t.Fatalf("Put(%s): %v", v, err)
		}
	}

	return &indexBackedQueryer{idx: idx}
}

// entityRows converts entity keys like "row:5" back to the bare value for
// assertion purposes, and sorts them for deterministic comparison.
func entityRows(keys [][]byte) []string {
	rows := make([]string, len(keys))
	for i, k := range keys {
		s := string(k)
		rows[i] = s[len("row:"):]
	}
	sort.Strings(rows)
	return rows
}

func TestTranslateMySQLRange_Numeric_GreaterThan_EndToEnd(t *testing.T) {
	svc := newNumericIndexQueryer(t)

	r := sqle.MySQLRange{sqle.GreaterThanRangeColumnExpr("5", types.Int64)}
	keys, err := translateRanges(context.Background(), svc, "n", []sqle.Range{r})
	require.NoError(t, err)
	require.Equal(t, []string{"10", "100", "15"}, entityRows(keys))
}

func TestTranslateMySQLRange_Numeric_GreaterOrEqual_EndToEnd(t *testing.T) {
	svc := newNumericIndexQueryer(t)

	r := sqle.MySQLRange{sqle.GreaterOrEqualRangeColumnExpr("5", types.Int64)}
	keys, err := translateRanges(context.Background(), svc, "n", []sqle.Range{r})
	require.NoError(t, err)
	require.Equal(t, []string{"10", "100", "15", "5"}, entityRows(keys))
}

func TestTranslateMySQLRange_Numeric_LessThan_EndToEnd(t *testing.T) {
	svc := newNumericIndexQueryer(t)

	r := sqle.MySQLRange{sqle.LessThanRangeColumnExpr("5", types.Int64)}
	keys, err := translateRanges(context.Background(), svc, "n", []sqle.Range{r})
	require.NoError(t, err)
	require.Equal(t, []string{"-10", "-3", "0", "1", "3.5"}, entityRows(keys))
}

func TestTranslateMySQLRange_Numeric_LessOrEqual_EndToEnd(t *testing.T) {
	svc := newNumericIndexQueryer(t)

	r := sqle.MySQLRange{sqle.LessOrEqualRangeColumnExpr("5", types.Int64)}
	keys, err := translateRanges(context.Background(), svc, "n", []sqle.Range{r})
	require.NoError(t, err)
	require.Equal(t, []string{"-10", "-3", "0", "1", "3.5", "5"}, entityRows(keys))
}

func TestTranslateMySQLRange_Numeric_Between_EndToEnd(t *testing.T) {
	svc := newNumericIndexQueryer(t)

	// BETWEEN 1 AND 10 -> ClosedClosed, unequal bounds -> general range scan.
	r := sqle.MySQLRange{sqle.ClosedRangeColumnExpr("1", "10", types.Int64)}
	keys, err := translateRanges(context.Background(), svc, "n", []sqle.Range{r})
	require.NoError(t, err)
	require.Equal(t, []string{"1", "10", "3.5", "5"}, entityRows(keys))
}

func TestTranslateMySQLRange_Numeric_OpenOpen_EndToEnd(t *testing.T) {
	svc := newNumericIndexQueryer(t)

	// 1 < n < 10 -> excludes both endpoints, unlike the inclusive BETWEEN
	// case above (differs by exactly the two boundary rows).
	r := sqle.MySQLRange{sqle.OpenRangeColumnExpr("1", "10", types.Int64)}
	keys, err := translateRanges(context.Background(), svc, "n", []sqle.Range{r})
	require.NoError(t, err)
	require.Equal(t, []string{"3.5", "5"}, entityRows(keys))
}

func TestTranslateMySQLRange_Numeric_GenuinelyEmpty_EndToEnd(t *testing.T) {
	svc := newNumericIndexQueryer(t)

	r := sqle.MySQLRange{sqle.GreaterThanRangeColumnExpr("1000", types.Int64)}
	keys, err := translateRanges(context.Background(), svc, "n", []sqle.Range{r})
	require.NoError(t, err)
	require.Empty(t, keys)
}

func TestTranslateMySQLRange_Numeric_PointEquality_EndToEnd(t *testing.T) {
	svc := newNumericIndexQueryer(t)

	// n = 5 (ClosedClosed with equal bounds) uses Query/Get, not Scan, and
	// is unaffected by this bug — included here to round out coverage of
	// the full set of range shapes on a numeric index.
	r := sqle.MySQLRange{sqle.ClosedRangeColumnExpr("5", "5", types.Int64)}
	keys, err := translateRanges(context.Background(), svc, "n", []sqle.Range{r})
	require.NoError(t, err)
	require.Equal(t, []string{"5"}, entityRows(keys))
}
