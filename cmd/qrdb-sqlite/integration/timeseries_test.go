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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sdktransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestTimeSeries_MultiProtocol_WriteDatapoints verifies that a single
// datapoint INSERT through the virtual table arrives in the real qrdbd
// time-series store and can be read back for every transport protocol.
func TestTimeSeries_MultiProtocol_WriteDatapoints(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ctx := context.Background()
			metric := uniqueMetric(proto, "cpu.usage")

			_, err := env.db.Exec(
				`INSERT INTO datapoints (metric, timestamp_ns, value, tags) VALUES (?, 1000000000, 55.5, '[]')`,
				metric,
			)
			require.NoError(t, err, "INSERT datapoint via vtab (%s)", proto)

			// Cross-validate via SDK.
			resp, err := env.client.TimeSeries().TSQuery(ctx, &sdktransport.TSQueryRequest{
				Metric:  metric,
				StartNs: 0,
				EndNs:   2_000_000_000,
			})
			require.NoError(t, err, "SDK TSQuery (%s)", proto)
			require.NotEmpty(t, resp.Points, "expected at least one datapoint in qrdbd (%s)", proto)

			// Verify via SQL virtual table.
			rows := scanAllRows(t, env.db,
				`SELECT metric, timestamp_ns, value FROM datapoints WHERE metric = ?`, metric)
			require.Len(t, rows, 1, "expected one row in vtab SELECT (%s)", proto)
			assert.Equal(t, metric, rows[0]["metric"])
			assert.Equal(t, int64(1_000_000_000), rows[0]["timestamp_ns"])
			assert.InDelta(t, 55.5, rows[0]["value"], 0.001)
		})
	}
}

// TestTimeSeries_MultiProtocol_CrossProtocol_WriteSQL_ReadSDK writes a
// datapoint through the SQLite virtual table and reads it back through the raw
// SDK client for each protocol.
func TestTimeSeries_MultiProtocol_CrossProtocol_WriteSQL_ReadSDK(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ctx := context.Background()
			metric := uniqueMetric(proto, "write.sql")

			_, err := env.db.Exec(
				`INSERT INTO datapoints (metric, timestamp_ns, value) VALUES (?, 5000000000, 42.0)`,
				metric,
			)
			require.NoError(t, err, "vtab INSERT (%s)", proto)

			resp, err := env.client.TimeSeries().TSQuery(ctx, &sdktransport.TSQueryRequest{
				Metric:  metric,
				StartNs: 0,
				EndNs:   10_000_000_000,
			})
			require.NoError(t, err, "SDK TSQuery (%s)", proto)
			require.NotEmpty(t, resp.Points, "vtab-written datapoint must be visible via SDK (%s)", proto)

			found := false
			for _, p := range resp.Points {
				if p.Metric == metric && p.TimestampNs == 5_000_000_000 {
					assert.InDelta(t, 42.0, p.Value, 0.001)
					found = true
					break
				}
			}
			assert.True(t, found, "specific datapoint not found in SDK response (%s)", proto)
		})
	}
}

// TestTimeSeries_MultiProtocol_CrossProtocol_WriteSDK_ReadSQL writes a
// datapoint through the raw SDK client and reads it back through the SQLite
// virtual table for each protocol.
func TestTimeSeries_MultiProtocol_CrossProtocol_WriteSDK_ReadSQL(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ctx := context.Background()
			metric := uniqueMetric(proto, "write.sdk")

			require.NoError(t, env.client.TimeSeries().TSWrite(ctx, &sdktransport.TSDataPoint{
				Metric:      metric,
				TimestampNs: 3_000_000_000,
				Value:       99.9,
			}), "SDK TSWrite (%s)", proto)

			rows := scanAllRows(t, env.db,
				`SELECT value FROM datapoints WHERE metric = ? AND timestamp_ns = 3000000000`,
				metric,
			)
			require.Len(t, rows, 1, "SDK-written datapoint must be visible through vtab (%s)", proto)
			assert.InDelta(t, 99.9, rows[0]["value"], 0.001)
		})
	}
}

// TestTimeSeries_MultiProtocol_QueryTimeRange inserts datapoints across a
// time window and verifies that a timestamp_ns BETWEEN query returns only the
// expected subset for each protocol.
func TestTimeSeries_MultiProtocol_QueryTimeRange(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			metric := uniqueMetric(proto, "range.test")

			points := []struct {
				ts    int64
				value float64
			}{
				{1_000_000_000, 10.0},
				{2_000_000_000, 20.0},
				{3_000_000_000, 30.0},
				{4_000_000_000, 40.0},
				{5_000_000_000, 50.0},
			}

			tx, err := env.db.Begin()
			require.NoError(t, err)
			for _, p := range points {
				_, err = tx.Exec(
					`INSERT INTO datapoints (metric, timestamp_ns, value) VALUES (?, ?, ?)`,
					metric, p.ts, p.value,
				)
				require.NoError(t, err)
			}
			require.NoError(t, tx.Commit())

			rows := scanAllRows(t, env.db,
				`SELECT timestamp_ns, value FROM datapoints WHERE metric = ? AND timestamp_ns BETWEEN 2000000000 AND 4000000000`,
				metric,
			)
			require.Len(t, rows, 3, "expected 3 rows in time range (%s)", proto)
			for _, row := range rows {
				ts := row["timestamp_ns"].(int64)
				assert.GreaterOrEqual(t, ts, int64(2_000_000_000))
				assert.LessOrEqual(t, ts, int64(4_000_000_000))
			}
		})
	}
}

// TestTimeSeries_MultiProtocol_MultipleMetrics writes data for two distinct
// metrics and confirms each can be queried independently for each protocol.
func TestTimeSeries_MultiProtocol_MultipleMetrics(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			cpuMetric := uniqueMetric(proto, "cpu.multi")
			memMetric := uniqueMetric(proto, "mem.multi")

			for i := 0; i < 5; i++ {
				_, err := env.db.Exec(
					`INSERT INTO datapoints (metric, timestamp_ns, value) VALUES (?, ?, ?)`,
					cpuMetric, int64(i+1)*1_000_000_000, float64(i)*10.0,
				)
				require.NoError(t, err)
			}
			for i := 0; i < 3; i++ {
				_, err := env.db.Exec(
					`INSERT INTO datapoints (metric, timestamp_ns, value) VALUES (?, ?, ?)`,
					memMetric, int64(i+1)*1_000_000_000, float64(i)*100.0,
				)
				require.NoError(t, err)
			}

			cpuRows := scanAllRows(t, env.db, `SELECT * FROM datapoints WHERE metric = ?`, cpuMetric)
			assert.Len(t, cpuRows, 5, "expected 5 cpu datapoints (%s)", proto)

			memRows := scanAllRows(t, env.db, `SELECT * FROM datapoints WHERE metric = ?`, memMetric)
			assert.Len(t, memRows, 3, "expected 3 mem datapoints (%s)", proto)

			for _, row := range cpuRows {
				assert.Equal(t, cpuMetric, row["metric"])
			}
			for _, row := range memRows {
				assert.Equal(t, memMetric, row["metric"])
			}
		})
	}
}

// TestTimeSeries_MultiProtocol_HighFrequencyWrite inserts 1000 datapoints in a
// single transaction and verifies the total count for each protocol.
func TestTimeSeries_MultiProtocol_HighFrequencyWrite(t *testing.T) {
	const n = 1000
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			metric := uniqueMetric(proto, "high.freq")

			tx, err := env.db.Begin()
			require.NoError(t, err)
			stmt, err := tx.Prepare(`INSERT INTO datapoints (metric, timestamp_ns, value) VALUES (?, ?, ?)`)
			require.NoError(t, err)
			defer stmt.Close()

			for i := 0; i < n; i++ {
				_, err := stmt.Exec(metric, int64(i+1)*1_000_000, float64(i))
				require.NoError(t, err, "INSERT failed at row %d (%s)", i, proto)
			}
			require.NoError(t, tx.Commit())

			assertRowCount(t, env.db,
				fmt.Sprintf(`SELECT * FROM datapoints WHERE metric = '%s'`, metric), n)
		})
	}
}

// TestTimeSeries_MultiProtocol_MetricsList seeds the time-series store via the
// SDK and verifies that SELECT * FROM metrics returns the expected names for
// each protocol.
func TestTimeSeries_MultiProtocol_MetricsList(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ctx := context.Background()

			seedMetrics := []string{
				uniqueMetric(proto, "http.requests"),
				uniqueMetric(proto, "db.queries"),
				uniqueMetric(proto, "cache.hits"),
			}

			for _, name := range seedMetrics {
				require.NoError(t, env.client.TimeSeries().TSWrite(ctx, &sdktransport.TSDataPoint{
					Metric:      name,
					TimestampNs: 1_000_000_000,
					Value:       1.0,
				}), "seed metric %s (%s)", name, proto)
			}

			rows := scanAllRows(t, env.db, `SELECT name FROM metrics`)
			names := make([]string, 0, len(rows))
			for _, row := range rows {
				names = append(names, row["name"].(string))
			}

			for _, expected := range seedMetrics {
				assert.Contains(t, names, expected, "metric %s not found (%s)", expected, proto)
			}
		})
	}
}
