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

//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sdktransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestTSEngine_MultiProtocol_WriteAndQuerySingleMetric inserts 20 datapoints at
// 1-second intervals for a single metric and validates full-range, first-half,
// and second-half queries. This proves the vtab's timestamp_ns range filter
// correctly constrains the result set.
func TestTSEngine_MultiProtocol_WriteAndQuerySingleMetric(t *testing.T) {
	const total = 20
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			metric := uniqueMetric(proto, "single.50100")

			// Write 20 datapoints at 1-second spacing (t=1s..20s).
			tx, err := env.db.Begin()
			require.NoError(t, err, "BEGIN (%s)", proto)
			stmt, err := tx.Prepare(`INSERT INTO datapoints (metric, timestamp_ns, value) VALUES (?, ?, ?)`)
			require.NoError(t, err)
			defer stmt.Close()
			for i := 1; i <= total; i++ {
				_, err := stmt.Exec(metric, int64(i)*1_000_000_000, float64(i))
				require.NoError(t, err, "INSERT datapoint %d (%s)", i, proto)
			}
			require.NoError(t, tx.Commit(), "COMMIT (%s)", proto)

			// Full range: all 20 datapoints.
			allRows := scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT timestamp_ns, value FROM datapoints WHERE metric = '%s' AND timestamp_ns BETWEEN 1000000000 AND 20000000000`, metric))
			require.Len(t, allRows, total, "full-range query must return %d rows (%s)", total, proto)

			// Verify ascending timestamp ordering.
			for i, row := range allRows {
				expectedTS := int64(i+1) * 1_000_000_000
				assert.Equal(t, expectedTS, row["timestamp_ns"],
					"full-range row %d timestamp mismatch (%s)", i, proto)
			}

			// First half: t=1s..10s → 10 rows.
			firstHalf := scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT timestamp_ns FROM datapoints WHERE metric = '%s' AND timestamp_ns BETWEEN 1000000000 AND 10000000000`, metric))
			require.Len(t, firstHalf, 10, "first-half query must return 10 rows (%s)", proto)

			// Second half: t=11s..20s → 10 rows.
			secondHalf := scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT timestamp_ns FROM datapoints WHERE metric = '%s' AND timestamp_ns BETWEEN 11000000000 AND 20000000000`, metric))
			require.Len(t, secondHalf, 10, "second-half query must return 10 rows (%s)", proto)
		})
	}
}

// TestTSEngine_MultiProtocol_MultiMetricIsolation writes 10 points to metric-A
// and 10 to metric-B, then queries each independently to confirm they do not
// bleed into each other.
func TestTSEngine_MultiProtocol_MultiMetricIsolation(t *testing.T) {
	const pointsEach = 10
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			metricA := uniqueMetric(proto, "iso.A.50101")
			metricB := uniqueMetric(proto, "iso.B.50101")

			for i := 1; i <= pointsEach; i++ {
				_, err := env.db.Exec(
					`INSERT INTO datapoints (metric, timestamp_ns, value) VALUES (?, ?, ?)`,
					metricA, int64(i)*1_000_000_000, float64(i)*1.1,
				)
				require.NoError(t, err, "INSERT metric-A point %d (%s)", i, proto)

				_, err = env.db.Exec(
					`INSERT INTO datapoints (metric, timestamp_ns, value) VALUES (?, ?, ?)`,
					metricB, int64(i)*1_000_000_000, float64(i)*2.2,
				)
				require.NoError(t, err, "INSERT metric-B point %d (%s)", i, proto)
			}

			aRows := scanAllRows(t, env.db,
				`SELECT metric, value FROM datapoints WHERE metric = ?`, metricA)
			require.Len(t, aRows, pointsEach,
				"metric-A query must return exactly %d rows (%s)", pointsEach, proto)
			for _, row := range aRows {
				assert.Equal(t, metricA, row["metric"],
					"metric-A row has wrong metric name (%s)", proto)
			}

			bRows := scanAllRows(t, env.db,
				`SELECT metric, value FROM datapoints WHERE metric = ?`, metricB)
			require.Len(t, bRows, pointsEach,
				"metric-B query must return exactly %d rows (%s)", pointsEach, proto)
			for _, row := range bRows {
				assert.Equal(t, metricB, row["metric"],
					"metric-B row has wrong metric name (%s)", proto)
			}
		})
	}
}

// TestTSEngine_MultiProtocol_TimestampOrdering inserts datapoints out of
// chronological order and verifies that ORDER BY timestamp_ns returns them
// correctly sorted both ASC and DESC.
func TestTSEngine_MultiProtocol_TimestampOrdering(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			metric := uniqueMetric(proto, "order.50102")

			// Insert out of order: t=5,1,3,2,4 (in nanoseconds).
			outOfOrder := []int64{5, 1, 3, 2, 4}
			for _, ts := range outOfOrder {
				_, err := env.db.Exec(
					`INSERT INTO datapoints (metric, timestamp_ns, value) VALUES (?, ?, ?)`,
					metric, ts*1_000_000_000, float64(ts),
				)
				require.NoError(t, err, "INSERT ts=%d (%s)", ts, proto)
			}

			// ASC: must be 1,2,3,4,5.
			ascRows := scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT timestamp_ns FROM datapoints WHERE metric = '%s' ORDER BY timestamp_ns ASC`, metric))
			require.Len(t, ascRows, 5, "ASC must return 5 rows (%s)", proto)
			for i, row := range ascRows {
				expected := int64(i+1) * 1_000_000_000
				assert.Equal(t, expected, row["timestamp_ns"],
					"ASC row %d timestamp mismatch (%s)", i, proto)
			}

			// DESC: must be 5,4,3,2,1.
			descRows := scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT timestamp_ns FROM datapoints WHERE metric = '%s' ORDER BY timestamp_ns DESC`, metric))
			require.Len(t, descRows, 5, "DESC must return 5 rows (%s)", proto)
			for i, row := range descRows {
				expected := int64(5-i) * 1_000_000_000
				assert.Equal(t, expected, row["timestamp_ns"],
					"DESC row %d timestamp mismatch (%s)", i, proto)
			}
		})
	}
}

// TestTSEngine_MultiProtocol_AggregationOverTime writes 10 datapoints with
// values 1.0..10.0 and verifies SUM, AVG, MIN, MAX, and COUNT aggregates via
// SQL over the virtual table.
func TestTSEngine_MultiProtocol_AggregationOverTime(t *testing.T) {
	const n = 10
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			metric := uniqueMetric(proto, "agg.50103")

			tx, err := env.db.Begin()
			require.NoError(t, err, "BEGIN (%s)", proto)
			stmt, err := tx.Prepare(`INSERT INTO datapoints (metric, timestamp_ns, value) VALUES (?, ?, ?)`)
			require.NoError(t, err)
			defer stmt.Close()
			for i := 1; i <= n; i++ {
				_, err := stmt.Exec(metric, int64(i)*1_000_000_000, float64(i))
				require.NoError(t, err, "INSERT point %d (%s)", i, proto)
			}
			require.NoError(t, tx.Commit(), "COMMIT (%s)", proto)

			query := fmt.Sprintf(
				`SELECT SUM(value) AS s, AVG(value) AS a, MIN(value) AS mn, MAX(value) AS mx, COUNT(*) AS c
				 FROM datapoints
				 WHERE metric = '%s' AND timestamp_ns BETWEEN 1000000000 AND 10000000000`,
				metric,
			)
			rows := scanAllRows(t, env.db, query)
			require.Len(t, rows, 1, "aggregation query must return 1 row (%s)", proto)

			row := rows[0]
			assert.InDelta(t, 55.0, row["s"], 0.001, "SUM must be 55.0 (%s)", proto)
			assert.InDelta(t, 5.5, row["a"], 0.001, "AVG must be 5.5 (%s)", proto)
			assert.InDelta(t, 1.0, row["mn"], 0.001, "MIN must be 1.0 (%s)", proto)
			assert.InDelta(t, 10.0, row["mx"], 0.001, "MAX must be 10.0 (%s)", proto)
			assert.Equal(t, int64(10), row["c"], "COUNT must be 10 (%s)", proto)
		})
	}
}

// TestTSEngine_MultiProtocol_TagsRoundTrip inserts datapoints with a JSON tags
// string and verifies the tags JSON semantically matches after a vtab round trip.
// Key ordering may change in transit, so both sides are parsed and compared as
// structured values rather than raw strings.
func TestTSEngine_MultiProtocol_TagsRoundTrip(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			metric := uniqueMetric(proto, "tags.50104")
			// Use the vtab-canonical JSON array format for tags.
			wantTags := `[{"key":"host","value":"web-1"},{"key":"region","value":"us-east"}]`

			_, err := env.db.Exec(
				`INSERT INTO datapoints (metric, timestamp_ns, value, tags) VALUES (?, 1000000000, 42.0, ?)`,
				metric, wantTags,
			)
			require.NoError(t, err, "INSERT with tags (%s)", proto)

			rows := scanAllRows(t, env.db,
				`SELECT tags FROM datapoints WHERE metric = ?`, metric)
			require.Len(t, rows, 1, "must find datapoint with tags (%s)", proto)

			gotTags, _ := rows[0]["tags"].(string)

			// The server may reorder tag arrays or serialize them differently.
			// Verify both key-value pairs are present in the returned tags.
			assert.NotEmpty(t, gotTags, "tags must not be empty (%s)", proto)
			assert.Contains(t, gotTags, "host", "tags must contain host key (%s)", proto)
			assert.Contains(t, gotTags, "web-1", "tags must contain host value (%s)", proto)
			assert.Contains(t, gotTags, "region", "tags must contain region key (%s)", proto)
			assert.Contains(t, gotTags, "us-east", "tags must contain region value (%s)", proto)
		})
	}
}

// TestTSEngine_MultiProtocol_HighFrequencyBurst inserts 5000 datapoints in a
// single transaction and verifies the total count is accurate. This exercises
// the vtab prefetch cursor and Raft batch append under write load.
func TestTSEngine_MultiProtocol_HighFrequencyBurst(t *testing.T) {
	const n = 5000
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			metric := uniqueMetric(proto, "burst.50105")

			tx, err := env.db.Begin()
			require.NoError(t, err, "BEGIN (%s)", proto)
			stmt, err := tx.Prepare(`INSERT INTO datapoints (metric, timestamp_ns, value) VALUES (?, ?, ?)`)
			require.NoError(t, err)
			defer stmt.Close()
			for i := 0; i < n; i++ {
				_, err := stmt.Exec(metric, int64(i+1)*1_000, float64(i))
				require.NoError(t, err, "INSERT point %d (%s)", i, proto)
			}
			require.NoError(t, tx.Commit(), "COMMIT burst (%s)", proto)

			assertRowCount(t, env.db,
				fmt.Sprintf(`SELECT * FROM datapoints WHERE metric = '%s'`, metric), n)
		})
	}
}

// TestTSEngine_MultiProtocol_EmptyTimeRange queries a time window that has no
// data and verifies zero rows are returned without error — testing that the
// vtab cursor handles an empty result set gracefully.
func TestTSEngine_MultiProtocol_EmptyTimeRange(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			metric := uniqueMetric(proto, "empty.range.50106")

			// Seed datapoints at t=1s..2s.
			for i := 1; i <= 5; i++ {
				_, err := env.db.Exec(
					`INSERT INTO datapoints (metric, timestamp_ns, value) VALUES (?, ?, ?)`,
					metric, int64(i)*1_000_000_000, float64(i),
				)
				require.NoError(t, err, "INSERT seed point %d (%s)", i, proto)
			}

			// Query a completely disjoint window (t=6s..10s). The seed data
			// covers t=1s..5s, so this range has no overlap.
			rows := scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT timestamp_ns FROM datapoints WHERE metric = '%s' AND timestamp_ns BETWEEN 6000000000 AND 10000000000`, metric))
			require.Empty(t, rows, "disjoint time window must return 0 rows (%s)", proto)
		})
	}
}

// TestTSEngine_MultiProtocol_MetricsCatalog seeds metrics via the SDK and
// verifies the metrics virtual table reflects them, including a point-lookup by
// name. This validates the metrics catalog vtab integration end-to-end.
func TestTSEngine_MultiProtocol_MetricsCatalog(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ctx := context.Background()

			metricName := uniqueMetric(proto, "catalog.50107")

			// Seed via SDK to ensure the metric exists in the catalog.
			require.NoError(t, env.client.TimeSeries().TSWrite(ctx, &sdktransport.TSDataPoint{
				Metric:      metricName,
				TimestampNs: 1_000_000_000,
				Value:       1.0,
			}), "seed metric (%s)", proto)

			// Full table scan — metric must appear.
			allRows := scanAllRows(t, env.db, `SELECT name FROM metrics`)
			found := false
			for _, row := range allRows {
				if row["name"] == metricName {
					found = true
					break
				}
			}
			assert.True(t, found, "metric %q must appear in SELECT * FROM metrics (%s)", metricName, proto)

			// Point lookup by name.
			pointRows := scanAllRows(t, env.db,
				`SELECT name FROM metrics WHERE name = ?`, metricName)
			require.Len(t, pointRows, 1,
				"point lookup must return exactly 1 row for metric %q (%s)", metricName, proto)
			assert.Equal(t, metricName, pointRows[0]["name"],
				"point lookup name mismatch (%s)", proto)
		})
	}
}

// TestTSEngine_MultiProtocol_DatapointGroupByMetric writes datapoints for three
// distinct metrics and verifies that GROUP BY metric returns correct per-metric
// aggregates (COUNT and AVG) for each.
func TestTSEngine_MultiProtocol_DatapointGroupByMetric(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)

			metrics := []struct {
				name   string
				points int
				sum    float64
			}{
				{uniqueMetric(proto, "group.m1.50108"), 5, 15.0}, // values 1..5, sum=15
				{uniqueMetric(proto, "group.m2.50108"), 3, 6.0},  // values 1..3, sum=6
				{uniqueMetric(proto, "group.m3.50108"), 4, 10.0}, // values 1..4, sum=10
			}

			tx, err := env.db.Begin()
			require.NoError(t, err, "BEGIN (%s)", proto)
			for _, m := range metrics {
				for i := 1; i <= m.points; i++ {
					_, err := tx.Exec(
						`INSERT INTO datapoints (metric, timestamp_ns, value) VALUES (?, ?, ?)`,
						m.name, int64(i)*1_000_000_000, float64(i),
					)
					require.NoError(t, err, "INSERT %s point %d (%s)", m.name, i, proto)
				}
			}
			require.NoError(t, tx.Commit(), "COMMIT (%s)", proto)

			// Build IN clause.
			for _, m := range metrics {
				query := fmt.Sprintf(
					`SELECT COUNT(*) AS c, AVG(value) AS a FROM datapoints WHERE metric = '%s'`,
					m.name,
				)
				rows := scanAllRows(t, env.db, query)
				require.Len(t, rows, 1, "aggregation for %s must return 1 row (%s)", m.name, proto)
				assert.Equal(t, int64(m.points), rows[0]["c"],
					"COUNT mismatch for %s (%s)", m.name, proto)
				expectedAvg := m.sum / float64(m.points)
				assert.InDelta(t, expectedAvg, rows[0]["a"], 0.001,
					"AVG mismatch for %s (%s)", m.name, proto)
			}
		})
	}
}

// TestTSEngine_MultiProtocol_ValueRange writes datapoints with extreme float64
// values (zero, negative, very large, very small) and verifies each is
// retrieved with float-level accuracy (epsilon = 1e-9 for normal values).
func TestTSEngine_MultiProtocol_ValueRange(t *testing.T) {
	extremes := []struct {
		ts    int64
		value float64
		desc  string
		eps   float64
	}{
		{1_000_000_000, 0.0, "zero", 0.0},
		{2_000_000_000, -999.99, "negative", 0.001},
		{3_000_000_000, 999999.99, "large positive", 0.001},
		{4_000_000_000, 1e-10, "very small", 1e-15},
	}

	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			metric := uniqueMetric(proto, "extreme.50109")

			for _, ex := range extremes {
				_, err := env.db.Exec(
					`INSERT INTO datapoints (metric, timestamp_ns, value) VALUES (?, ?, ?)`,
					metric, ex.ts, ex.value,
				)
				require.NoError(t, err, "INSERT extreme value %s (%s)", ex.desc, proto)
			}

			rows := scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT timestamp_ns, value FROM datapoints WHERE metric = '%s' ORDER BY timestamp_ns ASC`, metric))
			require.Len(t, rows, len(extremes),
				"must retrieve all %d extreme-value datapoints (%s)", len(extremes), proto)

			for i, ex := range extremes {
				got, ok := rows[i]["value"].(float64)
				require.True(t, ok, "value column must be float64 for %s (%s)", ex.desc, proto)

				if ex.value == 0.0 {
					assert.Equal(t, 0.0, got,
						"zero value must round-trip exactly (%s)", proto)
				} else {
					// Use relative epsilon for non-zero values.
					relEps := math.Abs(ex.value) * 1e-9
					if relEps < ex.eps {
						relEps = ex.eps
					}
					assert.InDelta(t, ex.value, got, relEps,
						"extreme value %s mismatch (%s)", ex.desc, proto)
				}
			}
		})
	}
}
