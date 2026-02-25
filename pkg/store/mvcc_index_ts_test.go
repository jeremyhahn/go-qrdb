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

package store

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
)

// ---------------------------------------------------------------------------
// Index test helpers
// ---------------------------------------------------------------------------

// indexCreateEntry builds an sm.Entry with an Index Create command.
func indexCreateEntry(index uint64, name, fieldName string, unique, sparse bool) sm.Entry {
	return sm.Entry{
		Index: index,
		Cmd:   protocol.EncodeIndexCreate(name, fieldName, unique, sparse),
	}
}

// indexDropEntry builds an sm.Entry with an Index Drop command.
func indexDropEntry(index uint64, name string) sm.Entry {
	return sm.Entry{
		Index: index,
		Cmd:   protocol.EncodeIndexDrop(name),
	}
}

// tsWriteEntry builds an sm.Entry with a TS Write command.
func tsWriteEntry(index uint64, metric string, ts int64, value float64, tags map[string]string) sm.Entry {
	return sm.Entry{
		Index: index,
		Cmd:   protocol.EncodeTSWrite(metric, ts, value, tags),
	}
}

// tsWriteBatchEntry builds an sm.Entry with a TS WriteBatch command.
func tsWriteBatchEntry(index uint64, params []protocol.TSWriteParams) sm.Entry {
	return sm.Entry{
		Index: index,
		Cmd:   protocol.EncodeTSWriteBatch(params),
	}
}

// openTestMVCCWithTS creates an MVCCStateMachine with a time-series
// Storage attached, each backed by its own PebbleDB.
func openTestMVCCWithTS(t *testing.T) *MVCCStateMachine {
	t.Helper()
	smach := newTestMVCCStateMachine(t)

	tsDir := t.TempDir()
	tsDB, err := pebble.Open(tsDir, nil)
	if err != nil {
		t.Fatalf("open ts pebble: %v", err)
	}
	t.Cleanup(func() { _ = tsDB.Close() })

	ts, err := timeseries.New(tsDB)
	if err != nil {
		t.Fatalf("create ts storage: %v", err)
	}
	t.Cleanup(func() { _ = ts.Close() })

	smach.SetTimeSeries(ts)
	return smach
}

// ---------------------------------------------------------------------------
// Index handler tests: Create
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_IndexCreate(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	entries := []sm.Entry{indexCreateEntry(1, "test_idx", "field1", false, false)}
	results := make([]sm.Result, len(entries))
	if err := s.Update(ctx, entries, results); err != nil {
		t.Fatalf("Update: unexpected error: %v", err)
	}
	if results[0].Value != 1 {
		t.Fatalf("expected result value 1, got %d", results[0].Value)
	}

	// Verify with IndexListRequest.
	listResult, err := s.Lookup(ctx, service.IndexListRequest{})
	if err != nil {
		t.Fatalf("Lookup IndexListRequest: unexpected error: %v", err)
	}
	names, ok := listResult.([]string)
	if !ok {
		t.Fatalf("expected []string, got %T", listResult)
	}
	if len(names) != 1 {
		t.Fatalf("expected 1 index name, got %d", len(names))
	}
	if names[0] != "test_idx" {
		t.Fatalf("expected index name test_idx, got %s", names[0])
	}
}

func TestMVCCStateMachine_IndexCreate_MultipleIndexes(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	entries := []sm.Entry{
		indexCreateEntry(1, "idx_a", "field_a", false, false),
		indexCreateEntry(2, "idx_b", "field_b", true, false),
	}
	results := make([]sm.Result, len(entries))
	if err := s.Update(ctx, entries, results); err != nil {
		t.Fatalf("Update: unexpected error: %v", err)
	}
	if results[0].Value != 1 {
		t.Fatalf("expected result value 1 for first index, got %d", results[0].Value)
	}
	if results[1].Value != 1 {
		t.Fatalf("expected result value 1 for second index, got %d", results[1].Value)
	}

	listResult, err := s.Lookup(ctx, service.IndexListRequest{})
	if err != nil {
		t.Fatalf("Lookup IndexListRequest: unexpected error: %v", err)
	}
	names, ok := listResult.([]string)
	if !ok {
		t.Fatalf("expected []string, got %T", listResult)
	}
	if len(names) != 2 {
		t.Fatalf("expected 2 index names, got %d", len(names))
	}
}

// ---------------------------------------------------------------------------
// Index handler tests: Create Duplicate
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_IndexCreate_Duplicate(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	// Create the first index.
	entries := []sm.Entry{indexCreateEntry(1, "dup_idx", "field1", false, false)}
	results := make([]sm.Result, len(entries))
	if err := s.Update(ctx, entries, results); err != nil {
		t.Fatalf("Update: unexpected error: %v", err)
	}

	// Attempt to create the same index again.
	entries2 := []sm.Entry{indexCreateEntry(2, "dup_idx", "field1", false, false)}
	results2 := make([]sm.Result, len(entries2))
	err := s.Update(ctx, entries2, results2)
	if err == nil {
		t.Fatal("expected error creating duplicate index, got nil")
	}

	var iqe *InvalidQueryError
	if !errors.As(err, &iqe) {
		t.Fatalf("expected *InvalidQueryError, got %T: %v", err, err)
	}
}

func TestMVCCStateMachine_IndexCreate_DuplicateErrorMessage(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	entries := []sm.Entry{indexCreateEntry(1, "dup_idx2", "field1", false, false)}
	results := make([]sm.Result, len(entries))
	if err := s.Update(ctx, entries, results); err != nil {
		t.Fatalf("Update: unexpected error: %v", err)
	}

	entries2 := []sm.Entry{indexCreateEntry(2, "dup_idx2", "field1", false, false)}
	results2 := make([]sm.Result, len(entries2))
	err := s.Update(ctx, entries2, results2)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var iqe *InvalidQueryError
	if !errors.As(err, &iqe) {
		t.Fatalf("expected *InvalidQueryError, got %T: %v", err, err)
	}
	if iqe.Reason != "index already exists: dup_idx2" {
		t.Fatalf("expected reason containing index name, got %q", iqe.Reason)
	}
}

// ---------------------------------------------------------------------------
// Index handler tests: Drop
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_IndexDrop(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	// Create the index.
	entries := []sm.Entry{indexCreateEntry(1, "drop_idx", "field1", false, false)}
	results := make([]sm.Result, len(entries))
	if err := s.Update(ctx, entries, results); err != nil {
		t.Fatalf("Update create: unexpected error: %v", err)
	}

	// Drop the index.
	dropEntries := []sm.Entry{indexDropEntry(2, "drop_idx")}
	dropResults := make([]sm.Result, len(dropEntries))
	if err := s.Update(ctx, dropEntries, dropResults); err != nil {
		t.Fatalf("Update drop: unexpected error: %v", err)
	}
	if dropResults[0].Value != 1 {
		t.Fatalf("expected drop result value 1, got %d", dropResults[0].Value)
	}

	// Verify the index is removed.
	listResult, err := s.Lookup(ctx, service.IndexListRequest{})
	if err != nil {
		t.Fatalf("Lookup IndexListRequest: unexpected error: %v", err)
	}
	names, ok := listResult.([]string)
	if !ok {
		t.Fatalf("expected []string, got %T", listResult)
	}
	if len(names) != 0 {
		t.Fatalf("expected 0 index names after drop, got %d", len(names))
	}
}

func TestMVCCStateMachine_IndexDrop_VerifiesRemoval(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	// Create two indexes.
	entries := []sm.Entry{
		indexCreateEntry(1, "idx_keep", "field1", false, false),
		indexCreateEntry(2, "idx_drop", "field2", false, false),
	}
	results := make([]sm.Result, len(entries))
	if err := s.Update(ctx, entries, results); err != nil {
		t.Fatalf("Update create: unexpected error: %v", err)
	}

	// Drop only one.
	dropEntries := []sm.Entry{indexDropEntry(3, "idx_drop")}
	dropResults := make([]sm.Result, len(dropEntries))
	if err := s.Update(ctx, dropEntries, dropResults); err != nil {
		t.Fatalf("Update drop: unexpected error: %v", err)
	}

	// Verify only idx_keep remains.
	listResult, err := s.Lookup(ctx, service.IndexListRequest{})
	if err != nil {
		t.Fatalf("Lookup: unexpected error: %v", err)
	}
	names := listResult.([]string)
	if len(names) != 1 {
		t.Fatalf("expected 1 index name, got %d", len(names))
	}
	if names[0] != "idx_keep" {
		t.Fatalf("expected idx_keep, got %s", names[0])
	}
}

// ---------------------------------------------------------------------------
// Index handler tests: Drop NotFound
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_IndexDrop_NotFound(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	entries := []sm.Entry{indexDropEntry(1, "nonexistent_idx")}
	results := make([]sm.Result, len(entries))
	err := s.Update(ctx, entries, results)
	if err == nil {
		t.Fatal("expected error dropping nonexistent index, got nil")
	}

	var iqe *InvalidQueryError
	if !errors.As(err, &iqe) {
		t.Fatalf("expected *InvalidQueryError, got %T: %v", err, err)
	}
}

func TestMVCCStateMachine_IndexDrop_NotFound_ErrorMessage(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	entries := []sm.Entry{indexDropEntry(1, "ghost_idx")}
	results := make([]sm.Result, len(entries))
	err := s.Update(ctx, entries, results)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var iqe *InvalidQueryError
	if !errors.As(err, &iqe) {
		t.Fatalf("expected *InvalidQueryError, got %T: %v", err, err)
	}
	if iqe.Reason != "index not found: ghost_idx" {
		t.Fatalf("expected reason with index name, got %q", iqe.Reason)
	}
}

// ---------------------------------------------------------------------------
// Index handler tests: Query
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_IndexQuery(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	// Create an index.
	entries := []sm.Entry{indexCreateEntry(1, "query_idx", "field1", false, false)}
	results := make([]sm.Result, len(entries))
	if err := s.Update(ctx, entries, results); err != nil {
		t.Fatalf("Update create: unexpected error: %v", err)
	}

	// Access the index directly to put entries.
	s.mu.RLock()
	idx := s.indexes["query_idx"]
	s.mu.RUnlock()
	if idx == nil {
		t.Fatal("expected index query_idx to exist")
	}

	if err := idx.Put(100, []byte("alice")); err != nil {
		t.Fatalf("index.Put: unexpected error: %v", err)
	}
	if err := idx.Put(200, []byte("alice")); err != nil {
		t.Fatalf("index.Put: unexpected error: %v", err)
	}

	// Query via Lookup.
	result, err := s.Lookup(ctx, service.IndexQueryRequest{
		IndexName: "query_idx",
		Value:     []byte("alice"),
	})
	if err != nil {
		t.Fatalf("Lookup IndexQueryRequest: unexpected error: %v", err)
	}
	ids, ok := result.([]uint64)
	if !ok {
		t.Fatalf("expected []uint64, got %T", result)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 entity IDs, got %d", len(ids))
	}
}

func TestMVCCStateMachine_IndexQuery_NotFound(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	// Query a nonexistent index.
	_, err := s.Lookup(ctx, service.IndexQueryRequest{
		IndexName: "nonexistent",
		Value:     []byte("val"),
	})
	if err == nil {
		t.Fatal("expected error querying nonexistent index, got nil")
	}

	var iqe *InvalidQueryError
	if !errors.As(err, &iqe) {
		t.Fatalf("expected *InvalidQueryError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Index handler tests: Scan
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_IndexScan(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	// Create the index.
	entries := []sm.Entry{indexCreateEntry(1, "scan_idx", "field1", false, false)}
	results := make([]sm.Result, len(entries))
	if err := s.Update(ctx, entries, results); err != nil {
		t.Fatalf("Update create: unexpected error: %v", err)
	}

	// Put entries directly in the index.
	s.mu.RLock()
	idx := s.indexes["scan_idx"]
	s.mu.RUnlock()

	if err := idx.Put(1, []byte("apple")); err != nil {
		t.Fatalf("index.Put: %v", err)
	}
	if err := idx.Put(2, []byte("banana")); err != nil {
		t.Fatalf("index.Put: %v", err)
	}
	if err := idx.Put(3, []byte("cherry")); err != nil {
		t.Fatalf("index.Put: %v", err)
	}

	// Scan via Lookup with range [apple, cherry).
	result, err := s.Lookup(ctx, service.IndexScanRequest{
		IndexName: "scan_idx",
		Start:     []byte("apple"),
		End:       []byte("cherry"),
	})
	if err != nil {
		t.Fatalf("Lookup IndexScanRequest: unexpected error: %v", err)
	}
	entries2, ok := result.(map[string][]uint64)
	if !ok {
		t.Fatalf("expected map[string][]uint64, got %T", result)
	}
	// Should contain apple and banana, but not cherry (half-open).
	if len(entries2) < 1 {
		t.Fatalf("expected at least 1 entry in scan result, got %d", len(entries2))
	}
	if _, ok := entries2["apple"]; !ok {
		t.Fatal("expected 'apple' in scan results")
	}
}

func TestMVCCStateMachine_IndexScan_NotFoundIndex(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	_, err := s.Lookup(ctx, service.IndexScanRequest{
		IndexName: "missing_idx",
		Start:     []byte("a"),
		End:       []byte("z"),
	})
	if err == nil {
		t.Fatal("expected error scanning nonexistent index, got nil")
	}

	var iqe *InvalidQueryError
	if !errors.As(err, &iqe) {
		t.Fatalf("expected *InvalidQueryError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// TimeSeries handler tests: Write
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// TimeSeries handler tests: Write without TS storage
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_TSWrite_NoStorage(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	now := time.Now().UTC()
	entries := []sm.Entry{tsWriteEntry(1, "cpu.usage", now.UnixNano(), 42.5, nil)}
	results := make([]sm.Result, len(entries))
	err := s.Update(ctx, entries, results)
	if err == nil {
		t.Fatal("expected error writing TS without storage, got nil")
	}

	var noe *NotOpenError
	if !errors.As(err, &noe) {
		t.Fatalf("expected *NotOpenError, got %T: %v", err, err)
	}
}

func TestMVCCStateMachine_TSWrite_NoStorage_QueryFails(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	now := time.Now().UTC()
	_, err := s.Lookup(ctx, service.TSQueryRequest{
		Metric: "cpu.usage",
		Start:  now.Add(-time.Second),
		End:    now.Add(time.Second),
	})
	if err == nil {
		t.Fatal("expected error querying TS without storage, got nil")
	}

	var iqe *InvalidQueryError
	if !errors.As(err, &iqe) {
		t.Fatalf("expected *InvalidQueryError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// TimeSeries handler tests: WriteBatch
// ---------------------------------------------------------------------------

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

func TestMVCCStateMachine_TSWriteBatch_NoStorage(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	now := time.Now().UTC()
	params := []protocol.TSWriteParams{
		{Metric: "disk.io", Timestamp: now.UnixNano(), Value: 100.0, Tags: nil},
	}
	entries := []sm.Entry{tsWriteBatchEntry(1, params)}
	results := make([]sm.Result, len(entries))
	err := s.Update(ctx, entries, results)
	if err == nil {
		t.Fatal("expected error writing TS batch without storage, got nil")
	}

	var noe *NotOpenError
	if !errors.As(err, &noe) {
		t.Fatalf("expected *NotOpenError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// TimeSeries handler tests: Aggregate
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// TimeSeries handler tests: Aggregate without TS storage
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_TSAggregate_NoStorage(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	now := time.Now().UTC()
	_, err := s.Lookup(ctx, service.TSAggregateRequest{
		Metric:   "latency",
		Start:    now.Add(-time.Minute),
		End:      now,
		Func:     service.AggAvg,
		Interval: time.Minute,
	})
	if err == nil {
		t.Fatal("expected error for TSAggregate without storage, got nil")
	}

	var iqe *InvalidQueryError
	if !errors.As(err, &iqe) {
		t.Fatalf("expected *InvalidQueryError, got %T: %v", err, err)
	}
}

func TestMVCCStateMachine_TSAggregate_NoStorage_ReasonMessage(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	now := time.Now().UTC()
	_, err := s.Lookup(ctx, service.TSAggregateRequest{
		Metric:   "latency",
		Start:    now.Add(-time.Minute),
		End:      now,
		Func:     service.AggSum,
		Interval: time.Minute,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var iqe *InvalidQueryError
	if !errors.As(err, &iqe) {
		t.Fatalf("expected *InvalidQueryError, got %T: %v", err, err)
	}
	if iqe.Reason != "time-series storage not configured" {
		t.Fatalf("expected 'time-series storage not configured', got %q", iqe.Reason)
	}
}

// ---------------------------------------------------------------------------
// SetTimeSeries / TimeSeries getter tests
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_SetTimeSeries(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	dir := t.TempDir()
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	tsDB, err := pebble.Open(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open ts pebble: %v", err)
	}
	t.Cleanup(func() { _ = tsDB.Close() })

	ts, err := timeseries.New(tsDB)
	if err != nil {
		t.Fatalf("create ts storage: %v", err)
	}

	s.SetTimeSeries(ts)

	got := s.TimeSeries()
	if got != ts {
		t.Fatal("expected TimeSeries() to return the same storage that was set")
	}
}

func TestMVCCStateMachine_SetTimeSeries_Overwrite(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	dir := t.TempDir()
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	tsDB1, err := pebble.Open(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open ts pebble 1: %v", err)
	}
	t.Cleanup(func() { _ = tsDB1.Close() })

	ts1, err := timeseries.New(tsDB1)
	if err != nil {
		t.Fatalf("create ts storage 1: %v", err)
	}

	tsDB2, err := pebble.Open(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open ts pebble 2: %v", err)
	}
	t.Cleanup(func() { _ = tsDB2.Close() })

	ts2, err := timeseries.New(tsDB2)
	if err != nil {
		t.Fatalf("create ts storage 2: %v", err)
	}

	s.SetTimeSeries(ts1)
	s.SetTimeSeries(ts2)

	got := s.TimeSeries()
	if got != ts2 {
		t.Fatal("expected TimeSeries() to return the second storage after overwrite")
	}
}

// ---------------------------------------------------------------------------
// TimeSeries nil tests
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_TimeSeries_Nil(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	dir := t.TempDir()
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	got := s.TimeSeries()
	if got != nil {
		t.Fatal("expected nil TimeSeries when not set")
	}
}

func TestMVCCStateMachine_TimeSeries_Nil_BeforeOpen(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	got := s.TimeSeries()
	if got != nil {
		t.Fatal("expected nil TimeSeries on fresh state machine before Open")
	}
}

// ---------------------------------------------------------------------------
// Index list when empty
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_IndexList_Empty(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	result, err := s.Lookup(ctx, service.IndexListRequest{})
	if err != nil {
		t.Fatalf("Lookup IndexListRequest: unexpected error: %v", err)
	}
	names, ok := result.([]string)
	if !ok {
		t.Fatalf("expected []string, got %T", result)
	}
	if len(names) != 0 {
		t.Fatalf("expected 0 index names, got %d", len(names))
	}
}

func TestMVCCStateMachine_IndexList_EmptyAfterCleanup(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	// Create and then drop an index.
	entries := []sm.Entry{indexCreateEntry(1, "temp_idx", "field1", false, false)}
	results := make([]sm.Result, len(entries))
	if err := s.Update(ctx, entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	dropEntries := []sm.Entry{indexDropEntry(2, "temp_idx")}
	dropResults := make([]sm.Result, len(dropEntries))
	if err := s.Update(ctx, dropEntries, dropResults); err != nil {
		t.Fatalf("Update drop: %v", err)
	}

	result, err := s.Lookup(ctx, service.IndexListRequest{})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	names := result.([]string)
	if len(names) != 0 {
		t.Fatalf("expected 0 names after create-drop, got %d", len(names))
	}
}
