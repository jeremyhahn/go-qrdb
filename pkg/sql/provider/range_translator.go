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
	"fmt"

	sqle "github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
)

// indexQueryer is the minimal interface required for index-accelerated
// SQL query execution. It is a subset of index.IndexServicer restricted
// to the read-only operations used during query planning and execution.
type indexQueryer interface {
	// Query returns entity keys matching an exact index value.
	Query(ctx context.Context, indexName string, value []byte) ([][]byte, error)
	// Scan returns entity keys whose index values fall in [start, end).
	Scan(ctx context.Context, indexName string, start, end []byte) (map[string][][]byte, error)
}

// Compile-time assertion that index.IndexServicer satisfies indexQueryer.
var _ indexQueryer = (index.IndexServicer)(nil)

// translateRanges converts a slice of sql.Range values produced by the
// go-mysql-server query planner into QRDB entity keys via index lookups.
// Duplicate keys are deduplicated; the relative order of results is
// undefined.
func translateRanges(ctx context.Context, svc indexQueryer, indexName string, ranges []sqle.Range) ([][]byte, error) {
	seen := make(map[string]struct{})
	var allKeys [][]byte

	for _, r := range ranges {
		mysqlRange, ok := r.(sqle.MySQLRange)
		if !ok {
			return nil, &RangeTranslationError{
				Reason: fmt.Sprintf("unsupported range type %T", r),
			}
		}
		keys, err := translateMySQLRange(ctx, svc, indexName, mysqlRange)
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			ks := string(k)
			if _, exists := seen[ks]; !exists {
				seen[ks] = struct{}{}
				allKeys = append(allKeys, k)
			}
		}
	}
	return allKeys, nil
}

// translateMySQLRange translates a single MySQLRange to entity keys.
// For single-column indexes (the only kind QRDB exposes) only the first
// MySQLRangeColumnExpr is considered.
func translateMySQLRange(ctx context.Context, svc indexQueryer, indexName string, r sqle.MySQLRange) ([][]byte, error) {
	if len(r) == 0 {
		return nil, nil
	}

	colExpr := r[0]

	// Determine the range type to choose between point lookup and scan.
	rangeType := colExpr.Type()
	switch rangeType {
	case sqle.RangeType_Empty:
		return nil, nil

	case sqle.RangeType_ClosedClosed:
		// Check for point lookup: lower and upper keys are the same value.
		lowerKey := sqle.GetMySQLRangeCutKey(colExpr.LowerBound)
		upperKey := sqle.GetMySQLRangeCutKey(colExpr.UpperBound)
		if fmt.Sprint(lowerKey) == fmt.Sprint(upperKey) {
			keys, err := svc.Query(ctx, indexName, []byte(fmt.Sprint(lowerKey)))
			if err != nil {
				return nil, &IndexLookupError{IndexName: indexName, Err: err}
			}
			return keys, nil
		}
		// General closed-closed range scan [lower, upper].
		return scanRange(ctx, svc, indexName, colExpr, true, true)

	case sqle.RangeType_OpenOpen:
		return scanRange(ctx, svc, indexName, colExpr, false, false)

	case sqle.RangeType_OpenClosed:
		return scanRange(ctx, svc, indexName, colExpr, false, true)

	case sqle.RangeType_ClosedOpen:
		return scanRange(ctx, svc, indexName, colExpr, true, false)

	case sqle.RangeType_GreaterThan:
		// (lower, +inf)
		start := extractLowerBytes(colExpr)
		result, err := svc.Scan(ctx, indexName, start, nil)
		if err != nil {
			return nil, &IndexLookupError{IndexName: indexName, Err: err}
		}
		return flattenScanResult(result), nil

	case sqle.RangeType_GreaterOrEqual:
		// [lower, +inf)
		start := extractLowerBytesInclusive(colExpr)
		result, err := svc.Scan(ctx, indexName, start, nil)
		if err != nil {
			return nil, &IndexLookupError{IndexName: indexName, Err: err}
		}
		return flattenScanResult(result), nil

	case sqle.RangeType_LessThanOrNull:
		// (-inf, upper)
		end := extractUpperBytes(colExpr)
		result, err := svc.Scan(ctx, indexName, nil, end)
		if err != nil {
			return nil, &IndexLookupError{IndexName: indexName, Err: err}
		}
		return flattenScanResult(result), nil

	case sqle.RangeType_LessOrEqualOrNull:
		// (-inf, upper]
		end := extractUpperBytesInclusive(colExpr)
		result, err := svc.Scan(ctx, indexName, nil, end)
		if err != nil {
			return nil, &IndexLookupError{IndexName: indexName, Err: err}
		}
		return flattenScanResult(result), nil

	case sqle.RangeType_All:
		// Full scan — return nil to signal no index acceleration.
		return nil, nil

	case sqle.RangeType_EqualNull:
		// NULL matching is not supported by QRDB indexes.
		return nil, nil

	default:
		return nil, &UnsupportedRangeTypeError{
			RangeType: fmt.Sprintf("%v", rangeType),
		}
	}
}

// scanRange executes a bounded scan using the column expression's lower and
// upper bounds. The lowerInclusive and upperInclusive flags control whether
// the bounds are included in or excluded from the result set.
func scanRange(
	ctx context.Context,
	svc indexQueryer,
	indexName string,
	colExpr sqle.MySQLRangeColumnExpr,
	lowerInclusive, upperInclusive bool,
) ([][]byte, error) {
	var start, end []byte

	if sqle.MySQLRangeCutIsBinding(colExpr.LowerBound) {
		raw := sqle.GetMySQLRangeCutKey(colExpr.LowerBound)
		s := []byte(fmt.Sprint(raw))
		if lowerInclusive {
			start = s
		} else {
			// Exclusive lower bound: append a suffix byte to advance past
			// the exact lower value while preserving lexicographic order for
			// byte-string keys. The QRDB scan uses half-open [start, end).
			start = appendExclusiveSuffix(s)
		}
	}

	if sqle.MySQLRangeCutIsBinding(colExpr.UpperBound) {
		raw := sqle.GetMySQLRangeCutKey(colExpr.UpperBound)
		e := []byte(fmt.Sprint(raw))
		if upperInclusive {
			// Inclusive upper bound: append a suffix byte so the scan
			// half-open interval [start, end) includes the upper value.
			end = appendExclusiveSuffix(e)
		} else {
			end = e
		}
	}

	result, err := svc.Scan(ctx, indexName, start, end)
	if err != nil {
		return nil, &IndexLookupError{IndexName: indexName, Err: err}
	}
	return flattenScanResult(result), nil
}

// extractLowerBytes returns the byte representation of the exclusive lower
// bound (Above{Key}) for a GreaterThan range.
func extractLowerBytes(colExpr sqle.MySQLRangeColumnExpr) []byte {
	if !sqle.MySQLRangeCutIsBinding(colExpr.LowerBound) {
		return nil
	}
	raw := sqle.GetMySQLRangeCutKey(colExpr.LowerBound)
	return appendExclusiveSuffix([]byte(fmt.Sprint(raw)))
}

// extractLowerBytesInclusive returns the byte representation of the inclusive
// lower bound (Below{Key}) for a GreaterOrEqual range.
func extractLowerBytesInclusive(colExpr sqle.MySQLRangeColumnExpr) []byte {
	if !sqle.MySQLRangeCutIsBinding(colExpr.LowerBound) {
		return nil
	}
	raw := sqle.GetMySQLRangeCutKey(colExpr.LowerBound)
	return []byte(fmt.Sprint(raw))
}

// extractUpperBytes returns the byte representation of the exclusive upper
// bound (Below{Key}) for a LessThan range.
func extractUpperBytes(colExpr sqle.MySQLRangeColumnExpr) []byte {
	if !sqle.MySQLRangeCutIsBinding(colExpr.UpperBound) {
		return nil
	}
	raw := sqle.GetMySQLRangeCutKey(colExpr.UpperBound)
	return []byte(fmt.Sprint(raw))
}

// extractUpperBytesInclusive returns the byte representation of the inclusive
// upper bound (Above{Key}) for a LessOrEqual range.
func extractUpperBytesInclusive(colExpr sqle.MySQLRangeColumnExpr) []byte {
	if !sqle.MySQLRangeCutIsBinding(colExpr.UpperBound) {
		return nil
	}
	raw := sqle.GetMySQLRangeCutKey(colExpr.UpperBound)
	return appendExclusiveSuffix([]byte(fmt.Sprint(raw)))
}

// appendExclusiveSuffix appends a zero byte to b, producing a key that
// sorts immediately after b in lexicographic order. This is used to
// convert inclusive bounds into exclusive ones for QRDB's half-open
// [start, end) scan semantics.
func appendExclusiveSuffix(b []byte) []byte {
	result := make([]byte, len(b)+1)
	copy(result, b)
	result[len(b)] = 0x00
	return result
}

// flattenScanResult collapses the map[string][][]byte returned by
// index.IndexServicer.Scan into a flat slice of entity keys.
func flattenScanResult(result map[string][][]byte) [][]byte {
	if len(result) == 0 {
		return nil
	}
	var keys [][]byte
	for _, entityKeys := range result {
		keys = append(keys, entityKeys...)
	}
	return keys
}
