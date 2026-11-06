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

//go:build qrdb_ts

// This file holds the state machines' tests that need the time-series
// service: without qrdb_ts, pkg/timeseries compiles only its stub, which
// refuses every operation. The tests that the stub serves (a missing store,
// an undecodable payload) and the helpers both sets share stay in
// unconstrained files.

package store

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

func TestMVCCStateMachine_TSWrite(t *testing.T) {
	s := openTestMVCCWithTS(t)
	ctx := context.Background()

	now := time.Now().UTC()
	entries := []sm.Entry{tsWriteEntry(1, "cpu.usage", now.UnixNano(), 42.5, map[string]string{"host": "node1"})}
	results := make([]sm.Result, len(entries))
	if err := s.Update(ctx, entries, results); err != nil {
		t.Fatalf("Update TSWrite: unexpected error: %v", err)
	}
	if results[0].Value != 1 {
		t.Fatalf("expected result value 1, got %d", results[0].Value)
	}

	// Query back through Lookup.
	queryResult, err := s.Lookup(ctx, service.TSQueryRequest{
		Metric: "cpu.usage",
		Start:  now.Add(-time.Second),
		End:    now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("Lookup TSQueryRequest: unexpected error: %v", err)
	}
	points, ok := queryResult.([]service.DataPoint)
	if !ok {
		t.Fatalf("expected []service.DataPoint, got %T", queryResult)
	}
	if len(points) != 1 {
		t.Fatalf("expected 1 data point, got %d", len(points))
	}
	if math.Abs(points[0].Value-42.5) > 0.001 {
		t.Fatalf("expected value 42.5, got %f", points[0].Value)
	}
}

func TestMVCCStateMachine_TSWrite_VerifiesTags(t *testing.T) {
	s := openTestMVCCWithTS(t)
	ctx := context.Background()

	now := time.Now().UTC()
	tags := map[string]string{"host": "node2", "region": "us-east"}
	entries := []sm.Entry{tsWriteEntry(1, "mem.usage", now.UnixNano(), 75.0, tags)}
	results := make([]sm.Result, len(entries))
	if err := s.Update(ctx, entries, results); err != nil {
		t.Fatalf("Update TSWrite: unexpected error: %v", err)
	}

	queryResult, err := s.Lookup(ctx, service.TSQueryRequest{
		Metric: "mem.usage",
		Start:  now.Add(-time.Second),
		End:    now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("Lookup TSQueryRequest: unexpected error: %v", err)
	}
	points := queryResult.([]service.DataPoint)
	if len(points) != 1 {
		t.Fatalf("expected 1 data point, got %d", len(points))
	}
	if points[0].Tags["host"] != "node2" {
		t.Fatalf("expected tag host=node2, got %s", points[0].Tags["host"])
	}
	if points[0].Tags["region"] != "us-east" {
		t.Fatalf("expected tag region=us-east, got %s", points[0].Tags["region"])
	}
}

func TestMVCCStateMachine_TSWriteBatch(t *testing.T) {
	s := openTestMVCCWithTS(t)
	ctx := context.Background()

	now := time.Now().UTC()
	params := []protocol.TSWriteParams{
		{Metric: "disk.io", Timestamp: now.UnixNano(), Value: 100.0, Tags: map[string]string{"host": "node1"}},
		{Metric: "disk.io", Timestamp: now.Add(time.Second).UnixNano(), Value: 200.0, Tags: map[string]string{"host": "node1"}},
		{Metric: "disk.io", Timestamp: now.Add(2 * time.Second).UnixNano(), Value: 300.0, Tags: map[string]string{"host": "node1"}},
	}
	entries := []sm.Entry{tsWriteBatchEntry(1, params)}
	results := make([]sm.Result, len(entries))
	if err := s.Update(ctx, entries, results); err != nil {
		t.Fatalf("Update TSWriteBatch: unexpected error: %v", err)
	}
	if results[0].Value != 3 {
		t.Fatalf("expected result value 3 (point count), got %d", results[0].Value)
	}

	// Query back all points.
	queryResult, err := s.Lookup(ctx, service.TSQueryRequest{
		Metric: "disk.io",
		Start:  now.Add(-time.Second),
		End:    now.Add(3 * time.Second),
	})
	if err != nil {
		t.Fatalf("Lookup TSQueryRequest: unexpected error: %v", err)
	}
	points := queryResult.([]service.DataPoint)
	if len(points) != 3 {
		t.Fatalf("expected 3 data points, got %d", len(points))
	}
}

func TestMVCCStateMachine_TSAggregate(t *testing.T) {
	s := openTestMVCCWithTS(t)
	ctx := context.Background()

	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Write 4 data points, 2 per minute.
	params := []protocol.TSWriteParams{
		{Metric: "latency", Timestamp: baseTime.UnixNano(), Value: 10.0, Tags: nil},
		{Metric: "latency", Timestamp: baseTime.Add(30 * time.Second).UnixNano(), Value: 20.0, Tags: nil},
		{Metric: "latency", Timestamp: baseTime.Add(time.Minute).UnixNano(), Value: 30.0, Tags: nil},
		{Metric: "latency", Timestamp: baseTime.Add(90 * time.Second).UnixNano(), Value: 40.0, Tags: nil},
	}
	entries := []sm.Entry{tsWriteBatchEntry(1, params)}
	results := make([]sm.Result, len(entries))
	if err := s.Update(ctx, entries, results); err != nil {
		t.Fatalf("Update: unexpected error: %v", err)
	}

	// Aggregate with AggAvg at 1 minute intervals.
	aggResult, err := s.Lookup(ctx, service.TSAggregateRequest{
		Metric:   "latency",
		Start:    baseTime.Add(-time.Second),
		End:      baseTime.Add(2 * time.Minute),
		Func:     service.AggAvg,
		Interval: time.Minute,
	})
	if err != nil {
		t.Fatalf("Lookup TSAggregateRequest: unexpected error: %v", err)
	}
	aggResults, ok := aggResult.([]service.AggResult)
	if !ok {
		t.Fatalf("expected []service.AggResult, got %T", aggResult)
	}
	if len(aggResults) != 2 {
		t.Fatalf("expected 2 aggregation buckets, got %d", len(aggResults))
	}

	// First bucket: avg of 10 and 20 = 15.
	if math.Abs(aggResults[0].Value-15.0) > 0.001 {
		t.Fatalf("expected first bucket avg 15.0, got %f", aggResults[0].Value)
	}
	if aggResults[0].Count != 2 {
		t.Fatalf("expected first bucket count 2, got %d", aggResults[0].Count)
	}

	// Second bucket: avg of 30 and 40 = 35.
	if math.Abs(aggResults[1].Value-35.0) > 0.001 {
		t.Fatalf("expected second bucket avg 35.0, got %f", aggResults[1].Value)
	}
	if aggResults[1].Count != 2 {
		t.Fatalf("expected second bucket count 2, got %d", aggResults[1].Count)
	}
}

func TestMVCCStateMachine_TSAggregate_Sum(t *testing.T) {
	s := openTestMVCCWithTS(t)
	ctx := context.Background()

	baseTime := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	params := []protocol.TSWriteParams{
		{Metric: "requests", Timestamp: baseTime.UnixNano(), Value: 5.0, Tags: nil},
		{Metric: "requests", Timestamp: baseTime.Add(10 * time.Second).UnixNano(), Value: 15.0, Tags: nil},
		{Metric: "requests", Timestamp: baseTime.Add(20 * time.Second).UnixNano(), Value: 30.0, Tags: nil},
	}
	entries := []sm.Entry{tsWriteBatchEntry(1, params)}
	results := make([]sm.Result, len(entries))
	if err := s.Update(ctx, entries, results); err != nil {
		t.Fatalf("Update: unexpected error: %v", err)
	}

	aggResult, err := s.Lookup(ctx, service.TSAggregateRequest{
		Metric:   "requests",
		Start:    baseTime.Add(-time.Second),
		End:      baseTime.Add(time.Minute),
		Func:     service.AggSum,
		Interval: time.Minute,
	})
	if err != nil {
		t.Fatalf("Lookup TSAggregateRequest: unexpected error: %v", err)
	}
	aggResults := aggResult.([]service.AggResult)
	if len(aggResults) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(aggResults))
	}
	if math.Abs(aggResults[0].Value-50.0) > 0.001 {
		t.Fatalf("expected sum 50.0, got %f", aggResults[0].Value)
	}
	if aggResults[0].Count != 3 {
		t.Fatalf("expected count 3, got %d", aggResults[0].Count)
	}
}

func TestMVCCStateMachine_TSWritePoint(t *testing.T) {
	s := openTestMVCCWithTS(t)
	ctx := context.Background()

	now := time.Now().UTC()
	entries := []sm.Entry{tsWritePointEntry(1, "net.rx", now.UnixNano(), 99.9, map[string]string{"iface": "eth0"})}
	results := make([]sm.Result, len(entries))
	if err := s.Update(ctx, entries, results); err != nil {
		t.Fatalf("Update TSWritePoint: unexpected error: %v", err)
	}
	if results[0].Value != 1 {
		t.Fatalf("expected result value 1, got %d", results[0].Value)
	}

	// Query back through Lookup to confirm persistence.
	queryResult, err := s.Lookup(ctx, service.TSQueryRequest{
		Metric: "net.rx",
		Start:  now.Add(-time.Second),
		End:    now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("Lookup TSQueryRequest: unexpected error: %v", err)
	}
	points, ok := queryResult.([]service.DataPoint)
	if !ok {
		t.Fatalf("expected []service.DataPoint, got %T", queryResult)
	}
	if len(points) != 1 {
		t.Fatalf("expected 1 data point, got %d", len(points))
	}
	if math.Abs(points[0].Value-99.9) > 0.001 {
		t.Fatalf("expected value 99.9, got %f", points[0].Value)
	}
	if points[0].Tags["iface"] != "eth0" {
		t.Fatalf("expected tag iface=eth0, got %s", points[0].Tags["iface"])
	}
}

func TestMVCCStateMachine_TSWritePoint_StoreError(t *testing.T) {
	s := openTestMVCCWithTS(t)
	ctx := context.Background()

	// WritePoint validates via DataPoint.Validate(). An empty metric triggers
	// an InvalidMetricError from the underlying timeseries storage.
	entries := []sm.Entry{tsWritePointEntry(1, "", time.Now().UTC().UnixNano(), 1.0, nil)}
	results := make([]sm.Result, len(entries))
	err := s.Update(ctx, entries, results)
	if err == nil {
		t.Fatal("expected error from empty metric, got nil")
	}

	var ime *timeseries.InvalidMetricError
	if !errors.As(err, &ime) {
		t.Fatalf("expected *timeseries.InvalidMetricError, got %T: %v", err, err)
	}
}

func TestMVCCStateMachine_TSDeleteSeries(t *testing.T) {
	s := openTestMVCCWithTS(t)
	ctx := context.Background()

	now := time.Now().UTC()

	// Write two points for the same metric.
	entries := []sm.Entry{
		tsWriteEntry(1, "del.metric", now.UnixNano(), 10.0, map[string]string{"host": "a"}),
		tsWriteEntry(2, "del.metric", now.Add(time.Second).UnixNano(), 20.0, map[string]string{"host": "a"}),
	}
	results := make([]sm.Result, len(entries))
	if err := s.Update(ctx, entries, results); err != nil {
		t.Fatalf("Update writes: unexpected error: %v", err)
	}

	// Verify both points exist.
	queryResult, err := s.Lookup(ctx, service.TSQueryRequest{
		Metric: "del.metric",
		Start:  now.Add(-time.Second),
		End:    now.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("Lookup before delete: unexpected error: %v", err)
	}
	pointsBefore := queryResult.([]service.DataPoint)
	if len(pointsBefore) != 2 {
		t.Fatalf("expected 2 points before delete, got %d", len(pointsBefore))
	}

	// Delete the entire series (no time bounds, no tag filter).
	delEntries := []sm.Entry{tsDeleteSeriesEntry(3, "del.metric", 0, 0, nil)}
	delResults := make([]sm.Result, len(delEntries))
	if err := s.Update(ctx, delEntries, delResults); err != nil {
		t.Fatalf("Update TSDeleteSeries: unexpected error: %v", err)
	}
	if delResults[0].Value != 1 {
		t.Fatalf("expected delete result value 1, got %d", delResults[0].Value)
	}

	// Verify data is gone.
	queryResult2, err := s.Lookup(ctx, service.TSQueryRequest{
		Metric: "del.metric",
		Start:  now.Add(-time.Second),
		End:    now.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("Lookup after delete: unexpected error: %v", err)
	}
	pointsAfter := queryResult2.([]service.DataPoint)
	if len(pointsAfter) != 0 {
		t.Fatalf("expected 0 points after delete, got %d", len(pointsAfter))
	}
}

func TestMVCCStateMachine_TSDeleteSeries_WithTimeRange(t *testing.T) {
	s := openTestMVCCWithTS(t)
	ctx := context.Background()

	baseTime := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	// Write three points at 0s, 1s, 2s.
	entries := []sm.Entry{
		tsWriteEntry(1, "partial.del", baseTime.UnixNano(), 1.0, nil),
		tsWriteEntry(2, "partial.del", baseTime.Add(time.Second).UnixNano(), 2.0, nil),
		tsWriteEntry(3, "partial.del", baseTime.Add(2*time.Second).UnixNano(), 3.0, nil),
	}
	results := make([]sm.Result, len(entries))
	if err := s.Update(ctx, entries, results); err != nil {
		t.Fatalf("Update writes: unexpected error: %v", err)
	}

	// Delete only points in [0s, 1s] range (startNs through endNs).
	delEntries := []sm.Entry{tsDeleteSeriesEntry(4, "partial.del",
		baseTime.UnixNano(),
		baseTime.Add(time.Second).UnixNano(),
		nil)}
	delResults := make([]sm.Result, len(delEntries))
	if err := s.Update(ctx, delEntries, delResults); err != nil {
		t.Fatalf("Update TSDeleteSeries: unexpected error: %v", err)
	}

	// Only the point at 2s should remain.
	queryResult, err := s.Lookup(ctx, service.TSQueryRequest{
		Metric: "partial.del",
		Start:  baseTime.Add(-time.Second),
		End:    baseTime.Add(3 * time.Second),
	})
	if err != nil {
		t.Fatalf("Lookup after partial delete: unexpected error: %v", err)
	}
	points := queryResult.([]service.DataPoint)
	if len(points) != 1 {
		t.Fatalf("expected 1 remaining point, got %d", len(points))
	}
	if math.Abs(points[0].Value-3.0) > 0.001 {
		t.Fatalf("expected remaining value 3.0, got %f", points[0].Value)
	}
}

func TestMVCCStateMachine_TSDeleteSeries_StoreError(t *testing.T) {
	s := openTestMVCCWithTS(t)
	ctx := context.Background()

	// DeleteRequest.Validate() rejects an empty metric with InvalidMetricError.
	entries := []sm.Entry{tsDeleteSeriesEntry(1, "", 0, 0, nil)}
	results := make([]sm.Result, len(entries))
	err := s.Update(ctx, entries, results)
	if err == nil {
		t.Fatal("expected error from empty metric delete, got nil")
	}

	var ime *timeseries.InvalidMetricError
	if !errors.As(err, &ime) {
		t.Fatalf("expected *timeseries.InvalidMetricError, got %T: %v", err, err)
	}
}

func TestMVCCStateMachine_TSSetRetention(t *testing.T) {
	s := openTestMVCCWithTS(t)
	ctx := context.Background()

	durationNs := int64(24 * time.Hour)
	resolutionNs := int64(time.Minute)
	aggregation := byte(timeseries.AggAvg)

	entries := []sm.Entry{tsSetRetentionEntry(1, "ret.metric", durationNs, resolutionNs, aggregation)}
	results := make([]sm.Result, len(entries))
	if err := s.Update(ctx, entries, results); err != nil {
		t.Fatalf("Update TSSetRetention: unexpected error: %v", err)
	}
	if results[0].Value != 1 {
		t.Fatalf("expected result value 1, got %d", results[0].Value)
	}

	// Read back the retention policy directly from the TS storage.
	policy, err := s.TimeSeries().GetRetentionPolicy("ret.metric")
	if err != nil {
		t.Fatalf("GetRetentionPolicy: unexpected error: %v", err)
	}
	if policy.Duration != time.Duration(durationNs) {
		t.Fatalf("expected duration %v, got %v", time.Duration(durationNs), policy.Duration)
	}
	if policy.Resolution != time.Duration(resolutionNs) {
		t.Fatalf("expected resolution %v, got %v", time.Duration(resolutionNs), policy.Resolution)
	}
	if policy.Aggregation != timeseries.AggAvg {
		t.Fatalf("expected aggregation AggAvg (%d), got %d", timeseries.AggAvg, policy.Aggregation)
	}
}

func TestMVCCStateMachine_TSSetRetention_OverwritesPolicy(t *testing.T) {
	s := openTestMVCCWithTS(t)
	ctx := context.Background()

	// Set initial policy.
	entries := []sm.Entry{tsSetRetentionEntry(1, "overwrite.metric",
		int64(time.Hour), int64(time.Second), byte(timeseries.AggSum))}
	results := make([]sm.Result, len(entries))
	if err := s.Update(ctx, entries, results); err != nil {
		t.Fatalf("Update first retention: unexpected error: %v", err)
	}

	// Overwrite with a different policy.
	entries2 := []sm.Entry{tsSetRetentionEntry(2, "overwrite.metric",
		int64(7*24*time.Hour), int64(5*time.Minute), byte(timeseries.AggCount))}
	results2 := make([]sm.Result, len(entries2))
	if err := s.Update(ctx, entries2, results2); err != nil {
		t.Fatalf("Update second retention: unexpected error: %v", err)
	}

	policy, err := s.TimeSeries().GetRetentionPolicy("overwrite.metric")
	if err != nil {
		t.Fatalf("GetRetentionPolicy: unexpected error: %v", err)
	}
	if policy.Duration != 7*24*time.Hour {
		t.Fatalf("expected duration 7d, got %v", policy.Duration)
	}
	if policy.Resolution != 5*time.Minute {
		t.Fatalf("expected resolution 5m, got %v", policy.Resolution)
	}
	if policy.Aggregation != timeseries.AggCount {
		t.Fatalf("expected aggregation AggCount (%d), got %d", timeseries.AggCount, policy.Aggregation)
	}
}

// TestMVCCConcurrency_UnsyncedTSPointInvisible is the time-series case of
// TestMVCCConcurrency_UnsyncedIndexEntryInvisible: a data point in a batch
// held between storage visibility and publication must not appear in a
// time-series query, and must once the batch is published.
func TestMVCCConcurrency_UnsyncedTSPointInvisible(t *testing.T) {
	fs := newWALStallFS()
	s := NewMVCCStateMachine(&pebble.Options{FS: fs})
	s.enableTimeSeries = true
	if _, err := s.Open(context.Background(), t.TempDir(), make(chan struct{})); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	at := time.Unix(1_700_000_000, 0).UTC()
	applyMVCCEntries(t, s, []sm.Entry{tsWriteEntry(1, "cpu.usage", at.UnixNano(), 1, nil)})
	points := func() int {
		t.Helper()
		got, err := s.Lookup(context.Background(), service.TSQueryRequest{
			Metric: "cpu.usage",
			Start:  at.Add(-time.Minute),
			End:    at.Add(time.Minute),
		})
		if err != nil {
			t.Fatalf("TSQueryRequest: %v", err)
		}
		return len(got.([]service.DataPoint))
	}

	done := startWALStalledUpdate(t, s, fs, []sm.Entry{tsWriteEntry(2, "cpu.usage", at.Add(time.Second).UnixNano(), 2, nil)})
	within(t, "time-series Lookup", func() {
		if got := points(); got != 1 {
			t.Errorf("time-series points during the held sync = %d, want 1", got)
		}
	})

	fs.unstall()
	if err := <-done; err != nil {
		t.Fatalf("stalled Update: %v", err)
	}
	if got := points(); got != 2 {
		t.Fatalf("time-series points after the commit = %d, want 2", got)
	}
}

func TestPebbleStateMachine_TSWrite(t *testing.T) {
	s := openStoreWithTS(t)

	ts := time.Now().UnixNano()
	cmd := mustEncode(protocol.EncodeTSWrite("cpu.usage", ts, 42.5, map[string]string{"host": "node1"}))
	results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})

	if results[0].Value != 1 {
		t.Fatalf("expected 1, got %d", results[0].Value)
	}
}

func TestPebbleStateMachine_TSWriteBatch(t *testing.T) {
	s := openStoreWithTS(t)

	now := time.Now().UnixNano()
	params := []protocol.TSWriteParams{
		{Metric: "cpu.usage", Timestamp: now, Value: 42.5, Tags: map[string]string{"host": "node1"}},
		{Metric: "mem.usage", Timestamp: now, Value: 85.0, Tags: map[string]string{"host": "node1"}},
	}
	cmd := mustEncode(protocol.EncodeTSWriteBatch(params))
	results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})

	if results[0].Value != 2 {
		t.Fatalf("expected 2, got %d", results[0].Value)
	}
}

func TestPebbleStateMachine_TSWritePoint(t *testing.T) {
	s := openStoreWithTS(t)

	ts := time.Now().UnixNano()
	cmd := mustEncode(protocol.EncodeTSWritePoint("cpu.usage", ts, 42.5, map[string]string{"host": "node1"}))
	results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})

	if results[0].Value != 1 {
		t.Fatalf("expected 1, got %d", results[0].Value)
	}
}

func TestPebbleStateMachine_TSDeleteSeries(t *testing.T) {
	now := time.Now().UnixNano()
	for _, tc := range []struct {
		name       string
		start, end int64
	}{
		{"bounded", now - 1000, now + 1000},
		{"no time bounds", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openStoreWithTS(t)
			writeCmd := mustEncode(protocol.EncodeTSWrite("del.metric", now, 1.0, nil))
			applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: writeCmd}})

			delCmd := mustEncode(protocol.EncodeTSDeleteSeries("del.metric", tc.start, tc.end, nil))
			results := applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: delCmd}})
			if results[0].Value != 1 {
				t.Fatalf("expected 1, got %d", results[0].Value)
			}
		})
	}
}

func TestPebbleStateMachine_TSSetRetention(t *testing.T) {
	s := openStoreWithTS(t)

	cmd := mustEncode(protocol.EncodeTSSetRetention("cpu.usage", int64(24*time.Hour), int64(time.Minute), 1))
	results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})
	if results[0].Value != 1 {
		t.Fatalf("expected 1, got %d", results[0].Value)
	}
}

func TestPebbleStateMachine_TSWrite_MetricCapAdmitsTheLastSlotOnce(t *testing.T) {
	s := openStoreWithTS(t)
	now := time.Now().UnixNano()
	capped := func(cmd []byte) []byte { return protocol.AppendTSMetricCap(cmd, 1) }
	results := applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: capped(mustEncode(protocol.EncodeTSWrite("cpu", now, 1, nil)))},
		{Index: 2, Cmd: capped(mustEncode(protocol.EncodeTSWrite("mem", now, 1, nil)))},
		{Index: 3, Cmd: capped(mustEncode(protocol.EncodeTSWritePoint("disk", now, 1, nil)))},
		{Index: 4, Cmd: capped(mustEncode(protocol.EncodeTSWriteBatch([]protocol.TSWriteParams{
			{Metric: "cpu", Timestamp: now + 1, Value: 2},
			{Metric: "net", Timestamp: now + 1, Value: 2},
		})))},
		{Index: 5, Cmd: capped(mustEncode(protocol.EncodeTSWrite("cpu", now+2, 3, nil)))},
		{Index: 6, Cmd: mustEncode(protocol.EncodeTSWrite("mem", now, 1, nil))},
	})
	assertPebbleAdmitted(t, results[0], "the first new metric")
	assertPebbleLimitRefusal(t, results[1], protocol.LimitMetrics, 1)
	assertPebbleLimitRefusal(t, results[2], protocol.LimitMetrics, 1)
	assertPebbleLimitRefusal(t, results[3], protocol.LimitMetrics, 1)
	assertPebbleAdmitted(t, results[4], "a write to the held metric")
	assertPebbleAdmitted(t, results[5], "an uncapped write")
}

func TestPebbleStateMachine_Lookup_TSQuery(t *testing.T) {
	s := openStoreWithTS(t)

	// Write a data point.
	now := time.Now()
	writeCmd := mustEncode(protocol.EncodeTSWrite("qry.metric", now.UnixNano(), 42.5, nil))
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: writeCmd}})

	// Query the data.
	result, err := s.Lookup(context.Background(), timeseries.TSQueryRequest{
		Metric: "qry.metric",
		Start:  now.Add(-time.Second),
		End:    now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("Lookup TSQueryRequest: %v", err)
	}
	points := result.([]timeseries.DataPoint)
	if len(points) != 1 {
		t.Fatalf("expected 1 point, got %d", len(points))
	}
	if math.Abs(points[0].Value-42.5) > 0.001 {
		t.Fatalf("expected value 42.5, got %f", points[0].Value)
	}
}

func TestPebbleStateMachine_Lookup_TSListMetrics(t *testing.T) {
	s := openStoreWithTS(t)

	// Write data to create metric entries.
	now := time.Now().UnixNano()
	applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: mustEncode(protocol.EncodeTSWrite("cpu.usage", now, 1.0, nil))},
		{Index: 2, Cmd: mustEncode(protocol.EncodeTSWrite("mem.usage", now, 2.0, nil))},
	})

	result, err := s.Lookup(context.Background(), timeseries.TSListMetricsRequest{Prefix: "", Limit: 100})
	if err != nil {
		t.Fatalf("Lookup TSListMetricsRequest: %v", err)
	}
	metrics := result.([]string)
	if len(metrics) < 2 {
		t.Fatalf("expected at least 2 metrics, got %d: %v", len(metrics), metrics)
	}
}

func TestPebbleStateMachine_Lookup_TSGetRetention(t *testing.T) {
	s := openStoreWithTS(t)
	setCmd := mustEncode(protocol.EncodeTSSetRetention("ret-metric", int64(24*time.Hour), int64(time.Minute), 1))
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: setCmd}})

	result, err := s.Lookup(context.Background(), timeseries.TSGetRetentionRequest{Metric: "ret-metric"})
	if err != nil {
		t.Fatalf("Lookup TSGetRetentionRequest: %v", err)
	}
	if policy := result.(*timeseries.RetentionPolicy); policy.Duration != 24*time.Hour {
		t.Fatalf("expected 24h duration, got %v", policy.Duration)
	}
}

func TestPebbleStateMachine_Lookup_TSAggregate(t *testing.T) {
	s := openStoreWithTS(t)
	now := time.Now()
	writeCmd := mustEncode(protocol.EncodeTSWrite("agg-metric", now.UnixNano(), 42.0, nil))
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: writeCmd}})

	result, err := s.Lookup(context.Background(), timeseries.TSAggregateRequest{
		Metric:   "agg-metric",
		Start:    now.Add(-time.Minute),
		End:      now.Add(time.Minute),
		Func:     timeseries.AggAvg,
		Interval: time.Minute,
	})
	if err != nil {
		t.Fatalf("Lookup TSAggregateRequest: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil aggregate result")
	}
}

func TestPebbleStateMachine_Lookup_TSRichQuery(t *testing.T) {
	s := openStoreWithTS(t)

	result, err := s.Lookup(context.Background(), timeseries.TSRichQueryRequest{
		Request: &timeseries.QueryRequest{
			Metric:    "cpu",
			StartTime: time.Unix(0, 0),
			EndTime:   time.Unix(0, 0).Add(time.Hour),
		},
	})
	if err != nil {
		t.Fatalf("Lookup TSRichQueryRequest: %v", err)
	}
	if _, ok := result.(*timeseries.QueryResult); !ok {
		t.Fatalf("Lookup TSRichQueryRequest returned %T, want *timeseries.QueryResult", result)
	}
}
