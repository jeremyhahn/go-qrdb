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

package privilege_test

import (
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
)

// BenchmarkSelectUnrestricted measures a simple point SELECT on a database
// with NO privilege catalog -- the default posture, and the one every
// existing deployment is in. The session is the system tenant, which is
// exempt from privilege checks permanently, so the enforcement path costs one
// interface assertion per query and issues no reads at all.
//
// This is the number to compare against the pre-privilege engine.
func BenchmarkSelectUnrestricted(b *testing.B) {
	h := newBenchHarness(b, benchSystemIdentity)
	benchmarkPointSelect(b, h)
}

// BenchmarkSelectLatchedGranted measures the same SELECT on a LATCHED
// database, run by a non-system identity that holds SELECT on the table. This
// is the steady state of an armed deployment: the query resolves the
// grantee's privilege set once, the catalog generation is unchanged from the
// cached snapshot, and the check itself is a mask AND.
func BenchmarkSelectLatchedGranted(b *testing.B) {
	h := newBenchHarness(b, benchTenantIdentity)
	h.grantSelect(b)
	benchmarkPointSelect(b, h)
}

// benchmarkPointSelect runs the point SELECT the baseline was measured with,
// draining the iterator so the row is actually produced.
func benchmarkPointSelect(b *testing.B, h *benchHarness) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx := h.context()
		_, iter, _, err := h.engine.Query(ctx, benchQuery)
		if err != nil {
			b.Fatalf("query: %v", err)
		}
		rows, err := sqle.RowIterToRows(ctx, iter)
		if err != nil {
			b.Fatalf("drain: %v", err)
		}
		if len(rows) != 1 {
			b.Fatalf("rows = %d, want 1", len(rows))
		}
	}
}
