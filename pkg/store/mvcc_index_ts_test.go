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

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// ---------------------------------------------------------------------------
// Index test helpers
// ---------------------------------------------------------------------------

// indexCreateEntry builds an sm.Entry with an Index Create command.
func indexCreateEntry(index uint64, name, fieldName string, unique, sparse bool) sm.Entry {
	return sm.Entry{
		Index: index,
		Cmd:   protocol.EncodeIndexCreate(name, fieldName, unique, sparse, false),
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
	dir := t.TempDir()
	s := NewMVCCStateMachine(nil)
	s.enableTimeSeries = true
	ctx := context.Background()
	_, err := s.Open(ctx, dir, make(chan struct{}))
	if err != nil {
		t.Fatalf("open mvcc sm with ts: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(ctx) })
	return s
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
	if results[0].Value != 1 {
		t.Fatalf("first create: expected result 1, got %d", results[0].Value)
	}

	// Creating the same index again is a no-op success (idempotent). A
	// non-nil error from a Raft Update() handler permanently stalls the
	// cluster, so duplicate-create must succeed without error.
	entries2 := []sm.Entry{indexCreateEntry(2, "dup_idx", "field1", false, false)}
	results2 := make([]sm.Result, len(entries2))
	if err := s.Update(ctx, entries2, results2); err != nil {
		t.Fatalf("duplicate create: expected no-op success, got error: %v", err)
	}
	if results2[0].Value != 1 {
		t.Fatalf("duplicate create: expected result 1, got %d", results2[0].Value)
	}

	// The original index must still be present and intact.
	listResult, err := s.Lookup(ctx, service.IndexListRequest{})
	if err != nil {
		t.Fatalf("Lookup IndexListRequest: unexpected error: %v", err)
	}
	names := listResult.([]string)
	if len(names) != 1 {
		t.Fatalf("expected exactly 1 index after duplicate create, got %d", len(names))
	}
	if names[0] != "dup_idx" {
		t.Fatalf("expected index name dup_idx, got %s", names[0])
	}
}

func TestMVCCStateMachine_IndexCreate_DuplicateErrorMessage(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	// Create the first index.
	entries := []sm.Entry{indexCreateEntry(1, "dup_idx2", "field1", false, false)}
	results := make([]sm.Result, len(entries))
	if err := s.Update(ctx, entries, results); err != nil {
		t.Fatalf("Update: unexpected error: %v", err)
	}

	// Duplicate create is a no-op success — the idempotent contract locks in
	// that no error is returned and the result value is 1.
	entries2 := []sm.Entry{indexCreateEntry(2, "dup_idx2", "field1", false, false)}
	results2 := make([]sm.Result, len(entries2))
	if err := s.Update(ctx, entries2, results2); err != nil {
		t.Fatalf("duplicate create: expected no-op success, got error: %v", err)
	}
	if results2[0].Value != 1 {
		t.Fatalf("duplicate create: expected result 1, got %d", results2[0].Value)
	}

	// Only one index should exist (the duplicate was silently ignored).
	listResult, err := s.Lookup(ctx, service.IndexListRequest{})
	if err != nil {
		t.Fatalf("Lookup IndexListRequest: unexpected error: %v", err)
	}
	names := listResult.([]string)
	if len(names) != 1 {
		t.Fatalf("expected 1 index after duplicate create, got %d", len(names))
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

	// Dropping a non-existent index is a no-op success (idempotent). A
	// non-nil error from a Raft Update() handler permanently stalls the
	// cluster, so drop-not-found must succeed without error.
	entries := []sm.Entry{indexDropEntry(1, "nonexistent_idx")}
	results := make([]sm.Result, len(entries))
	if err := s.Update(ctx, entries, results); err != nil {
		t.Fatalf("drop nonexistent: expected no-op success, got error: %v", err)
	}
	if results[0].Value != 1 {
		t.Fatalf("drop nonexistent: expected result 1, got %d", results[0].Value)
	}
}

func TestMVCCStateMachine_IndexDrop_NotFound_ErrorMessage(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	// Drop of a non-existent index is a no-op success. This test previously
	// asserted an error message; it is repurposed to lock in the idempotent
	// contract: no error, result value 1, and subsequent index list is empty.
	entries := []sm.Entry{indexDropEntry(1, "ghost_idx")}
	results := make([]sm.Result, len(entries))
	if err := s.Update(ctx, entries, results); err != nil {
		t.Fatalf("drop ghost_idx: expected no-op success, got error: %v", err)
	}
	if results[0].Value != 1 {
		t.Fatalf("drop ghost_idx: expected result 1, got %d", results[0].Value)
	}

	// Index list must remain empty — the drop of a nonexistent index must not
	// corrupt the index registry.
	listResult, err := s.Lookup(ctx, service.IndexListRequest{})
	if err != nil {
		t.Fatalf("Lookup IndexListRequest: unexpected error: %v", err)
	}
	names := listResult.([]string)
	if len(names) != 0 {
		t.Fatalf("expected empty index list after no-op drop, got %d names", len(names))
	}
}

// ---------------------------------------------------------------------------
// Index handler tests: Query
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_IndexQuery(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	// Create an index on "field1".
	entries := []sm.Entry{indexCreateEntry(1, "query_idx", "field1", false, false)}
	results := make([]sm.Result, len(entries))
	if err := s.Update(ctx, entries, results); err != nil {
		t.Fatalf("Update create: unexpected error: %v", err)
	}

	// Insert two documents with the same indexed field value via MVCC put.
	putE1 := mvccPutEntry(2, []byte("doc:1"), []byte(`{"field1":"alice"}`), 0)
	putE2 := mvccPutEntry(3, []byte("doc:2"), []byte(`{"field1":"alice"}`), 0)
	applyMVCCEntries(t, s, []sm.Entry{putE1})
	applyMVCCEntries(t, s, []sm.Entry{putE2})

	// Query via Lookup.
	result, err := s.Lookup(ctx, service.IndexQueryRequest{
		IndexName: "query_idx",
		Value:     []byte("alice"),
	})
	if err != nil {
		t.Fatalf("Lookup IndexQueryRequest: unexpected error: %v", err)
	}
	keys, ok := result.([][]byte)
	if !ok {
		t.Fatalf("expected [][]byte, got %T", result)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 entity keys, got %d", len(keys))
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

	var iqe *engine.InvalidQueryError
	if !errors.As(err, &iqe) {
		t.Fatalf("expected *engine.InvalidQueryError, got %T: %v", err, err)
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

	// Insert documents with indexed field values via MVCC put.
	putE1 := mvccPutEntry(2, []byte("fruit:1"), []byte(`{"field1":"apple"}`), 0)
	putE2 := mvccPutEntry(3, []byte("fruit:2"), []byte(`{"field1":"banana"}`), 0)
	putE3 := mvccPutEntry(4, []byte("fruit:3"), []byte(`{"field1":"cherry"}`), 0)
	applyMVCCEntries(t, s, []sm.Entry{putE1})
	applyMVCCEntries(t, s, []sm.Entry{putE2})
	applyMVCCEntries(t, s, []sm.Entry{putE3})

	// Scan via Lookup with range [apple, cherry).
	result, err := s.Lookup(ctx, service.IndexScanRequest{
		IndexName: "scan_idx",
		Start:     []byte("apple"),
		End:       []byte("cherry"),
	})
	if err != nil {
		t.Fatalf("Lookup IndexScanRequest: unexpected error: %v", err)
	}
	entries2, ok := result.(map[string][][]byte)
	if !ok {
		t.Fatalf("expected map[string][][]byte, got %T", result)
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

	var iqe *engine.InvalidQueryError
	if !errors.As(err, &iqe) {
		t.Fatalf("expected *engine.InvalidQueryError, got %T: %v", err, err)
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
	if err := s.Update(ctx, entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error writing TS without storage: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload writing TS without storage")
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

	var iqe *engine.InvalidQueryError
	if !errors.As(err, &iqe) {
		t.Fatalf("expected *engine.InvalidQueryError, got %T: %v", err, err)
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
	if err := s.Update(ctx, entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error writing TS batch without storage: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload writing TS batch without storage")
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

	var iqe *engine.InvalidQueryError
	if !errors.As(err, &iqe) {
		t.Fatalf("expected *engine.InvalidQueryError, got %T: %v", err, err)
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

	var iqe *engine.InvalidQueryError
	if !errors.As(err, &iqe) {
		t.Fatalf("expected *engine.InvalidQueryError, got %T: %v", err, err)
	}
	if iqe.Reason != "time-series storage not configured" {
		t.Fatalf("expected 'time-series storage not configured', got %q", iqe.Reason)
	}
}

// ---------------------------------------------------------------------------
// SetTimeSeries / TimeSeries getter tests
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_SetTimeSeries(t *testing.T) {
	// SetTimeSeries must be called before Open. The engine creates
	// time-series storage during Open when enableTimeSeries is set.
	s := NewMVCCStateMachine(nil)
	s.enableTimeSeries = true

	dir := t.TempDir()
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	got := s.TimeSeries()
	if got == nil {
		t.Fatal("expected TimeSeries() to return non-nil storage when enableTimeSeries is set")
	}
}

func TestMVCCStateMachine_SetTimeSeries_Overwrite(t *testing.T) {
	// SetTimeSeries(ts) sets enableTimeSeries = (ts != nil). Calling
	// with a non-nil then nil should disable time-series.
	s := NewMVCCStateMachine(nil)

	// Enable via a non-nil argument.
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

	// Disable by passing nil.
	s.SetTimeSeries(nil)

	if s.enableTimeSeries {
		t.Fatal("expected enableTimeSeries to be false after SetTimeSeries(nil)")
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

// ---------------------------------------------------------------------------
// TimeSeries handler test helpers: WritePoint, DeleteSeries, SetRetention
// ---------------------------------------------------------------------------

// tsWritePointEntry builds an sm.Entry with a TS WritePoint command.
func tsWritePointEntry(index uint64, metric string, ts int64, value float64, tags map[string]string) sm.Entry {
	return sm.Entry{
		Index: index,
		Cmd:   protocol.EncodeTSWritePoint(metric, ts, value, tags),
	}
}

// tsDeleteSeriesEntry builds an sm.Entry with a TS DeleteSeries command.
func tsDeleteSeriesEntry(index uint64, metric string, startNs, endNs int64, tags map[string]string) sm.Entry {
	return sm.Entry{
		Index: index,
		Cmd:   protocol.EncodeTSDeleteSeries(metric, startNs, endNs, tags),
	}
}

// tsSetRetentionEntry builds an sm.Entry with a TS SetRetention command.
func tsSetRetentionEntry(index uint64, metric string, durationNs, resolutionNs int64, aggregation byte) sm.Entry {
	return sm.Entry{
		Index: index,
		Cmd:   protocol.EncodeTSSetRetention(metric, durationNs, resolutionNs, aggregation),
	}
}

// ---------------------------------------------------------------------------
// TimeSeries handler tests: WritePoint
// ---------------------------------------------------------------------------

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

func TestMVCCStateMachine_TSWritePoint_NoStorage(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	now := time.Now().UTC()
	entries := []sm.Entry{tsWritePointEntry(1, "net.rx", now.UnixNano(), 99.9, nil)}
	results := make([]sm.Result, len(entries))
	if err := s.Update(ctx, entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error writing TSWritePoint without storage: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload writing TSWritePoint without storage")
	}
}

func TestMVCCStateMachine_TSWritePoint_InvalidPayload(t *testing.T) {
	s := openTestMVCCWithTS(t)
	ctx := context.Background()

	// Build an entry with the correct opcode but garbage payload.
	garbage := []byte{protocol.OpTSWritePoint, 0xFF}
	entries := []sm.Entry{{Index: 1, Cmd: garbage}}
	results := make([]sm.Result, len(entries))
	err := s.Update(ctx, entries, results)
	if err == nil {
		t.Fatal("expected error from invalid TSWritePoint payload, got nil")
	}

	var ice *protocol.InvalidCommandError
	if !errors.As(err, &ice) {
		t.Fatalf("expected *protocol.InvalidCommandError, got %T: %v", err, err)
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

// ---------------------------------------------------------------------------
// TimeSeries handler tests: DeleteSeries
// ---------------------------------------------------------------------------

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

func TestMVCCStateMachine_TSDeleteSeries_NoStorage(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	entries := []sm.Entry{tsDeleteSeriesEntry(1, "some.metric", 0, 0, nil)}
	results := make([]sm.Result, len(entries))
	if err := s.Update(ctx, entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error deleting TS series without storage: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload deleting TS series without storage")
	}
}

func TestMVCCStateMachine_TSDeleteSeries_InvalidPayload(t *testing.T) {
	s := openTestMVCCWithTS(t)
	ctx := context.Background()

	// Build an entry with the correct opcode but garbage payload.
	garbage := []byte{protocol.OpTSDeleteSeries, 0xFF}
	entries := []sm.Entry{{Index: 1, Cmd: garbage}}
	results := make([]sm.Result, len(entries))
	err := s.Update(ctx, entries, results)
	if err == nil {
		t.Fatal("expected error from invalid TSDeleteSeries payload, got nil")
	}

	var ice *protocol.InvalidCommandError
	if !errors.As(err, &ice) {
		t.Fatalf("expected *protocol.InvalidCommandError, got %T: %v", err, err)
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

// ---------------------------------------------------------------------------
// TimeSeries handler tests: SetRetention
// ---------------------------------------------------------------------------

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

func TestMVCCStateMachine_TSSetRetention_NoStorage(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	entries := []sm.Entry{tsSetRetentionEntry(1, "any.metric",
		int64(time.Hour), int64(time.Second), byte(timeseries.AggAvg))}
	results := make([]sm.Result, len(entries))
	if err := s.Update(ctx, entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error setting retention without storage: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload setting retention without storage")
	}
}

func TestMVCCStateMachine_TSSetRetention_InvalidPayload(t *testing.T) {
	s := openTestMVCCWithTS(t)
	ctx := context.Background()

	// Build an entry with the correct opcode but garbage payload.
	garbage := []byte{protocol.OpTSSetRetention, 0xFF}
	entries := []sm.Entry{{Index: 1, Cmd: garbage}}
	results := make([]sm.Result, len(entries))
	err := s.Update(ctx, entries, results)
	if err == nil {
		t.Fatal("expected error from invalid TSSetRetention payload, got nil")
	}

	var ice *protocol.InvalidCommandError
	if !errors.As(err, &ice) {
		t.Fatalf("expected *protocol.InvalidCommandError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Invalid payload tests for index and original TS handlers (decode error paths)
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_IndexCreate_InvalidPayload(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	garbage := []byte{protocol.OpIndexCreate, 0xFF}
	entries := []sm.Entry{{Index: 1, Cmd: garbage}}
	results := make([]sm.Result, len(entries))
	err := s.Update(ctx, entries, results)
	if err == nil {
		t.Fatal("expected error from invalid IndexCreate payload, got nil")
	}
}

func TestMVCCStateMachine_IndexDrop_InvalidPayload(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	garbage := []byte{protocol.OpIndexDrop, 0xFF}
	entries := []sm.Entry{{Index: 1, Cmd: garbage}}
	results := make([]sm.Result, len(entries))
	err := s.Update(ctx, entries, results)
	if err == nil {
		t.Fatal("expected error from invalid IndexDrop payload, got nil")
	}
}

func TestMVCCStateMachine_TSWrite_InvalidPayload(t *testing.T) {
	s := openTestMVCCWithTS(t)
	ctx := context.Background()

	garbage := []byte{protocol.OpTSWrite, 0xFF}
	entries := []sm.Entry{{Index: 1, Cmd: garbage}}
	results := make([]sm.Result, len(entries))
	err := s.Update(ctx, entries, results)
	if err == nil {
		t.Fatal("expected error from invalid TSWrite payload, got nil")
	}
}

func TestMVCCStateMachine_TSWriteBatch_InvalidPayload(t *testing.T) {
	s := openTestMVCCWithTS(t)
	ctx := context.Background()

	garbage := []byte{protocol.OpTSWriteBatch, 0xFF}
	entries := []sm.Entry{{Index: 1, Cmd: garbage}}
	results := make([]sm.Result, len(entries))
	err := s.Update(ctx, entries, results)
	if err == nil {
		t.Fatal("expected error from invalid TSWriteBatch payload, got nil")
	}
}
