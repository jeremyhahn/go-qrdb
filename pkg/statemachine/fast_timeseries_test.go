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

package statemachine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
)

// ---------------------------------------------------------------------------
// Call tracking structs
// ---------------------------------------------------------------------------

type writePointCall struct {
	Metric    string
	Timestamp int64
	Value     float64
	Tags      map[string]string
}

type deleteSeriesCall struct {
	Metric  string
	StartNs int64
	EndNs   int64
	Tags    map[string]string
}

type setRetentionCall struct {
	Metric       string
	DurationNs   int64
	ResolutionNs int64
	Aggregation  byte
}

// ---------------------------------------------------------------------------
// mockTimeSeriesStore: implements TimeSeriesStore for testing
// ---------------------------------------------------------------------------

type mockTimeSeriesStore struct {
	mu sync.Mutex

	// Call tracking
	writePointCalls   []writePointCall
	writeBatchCalls   [][]TSDataPoint
	deleteSeriesCalls []deleteSeriesCall
	setRetentionCalls []setRetentionCall

	// Error injection
	writePointErr   error
	writeBatchErr   error
	deleteSeriesErr error
	setRetentionErr error
	queryRangeErr   error
	queryAggErr     error
	queryRichErr    error
	getRetentionErr error
	listMetricsErr  error

	// Return values
	queryRangeResult   interface{}
	queryAggResult     interface{}
	queryRichResult    interface{}
	getRetentionResult interface{}
	listMetricsResult  []string
}

func (m *mockTimeSeriesStore) WriteDataPoint(metric string, timestamp int64, value float64, tags map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writePointCalls = append(m.writePointCalls, writePointCall{
		Metric:    metric,
		Timestamp: timestamp,
		Value:     value,
		Tags:      tags,
	})
	return m.writePointErr
}

func (m *mockTimeSeriesStore) WriteDataPoints(points []TSDataPoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]TSDataPoint, len(points))
	copy(cp, points)
	m.writeBatchCalls = append(m.writeBatchCalls, cp)
	return m.writeBatchErr
}

func (m *mockTimeSeriesStore) DeleteSeries(metric string, startNs, endNs int64, tags map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleteSeriesCalls = append(m.deleteSeriesCalls, deleteSeriesCall{
		Metric:  metric,
		StartNs: startNs,
		EndNs:   endNs,
		Tags:    tags,
	})
	return m.deleteSeriesErr
}

func (m *mockTimeSeriesStore) SetRetentionPolicy(metric string, durationNs, resolutionNs int64, aggregation byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setRetentionCalls = append(m.setRetentionCalls, setRetentionCall{
		Metric:       metric,
		DurationNs:   durationNs,
		ResolutionNs: resolutionNs,
		Aggregation:  aggregation,
	})
	return m.setRetentionErr
}

func (m *mockTimeSeriesStore) QueryRange(metric string, startNs, endNs int64) (interface{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.queryRangeResult, m.queryRangeErr
}

func (m *mockTimeSeriesStore) QueryAggregate(metric string, startNs, endNs int64, fn byte, intervalNs int64) (interface{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.queryAggResult, m.queryAggErr
}

func (m *mockTimeSeriesStore) QueryRich(request interface{}) (interface{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.queryRichResult, m.queryRichErr
}

func (m *mockTimeSeriesStore) GetRetentionPolicy(metric string) (interface{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.getRetentionResult, m.getRetentionErr
}

func (m *mockTimeSeriesStore) ListMetrics(prefix string, limit int) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listMetricsResult, m.listMetricsErr
}

func (m *mockTimeSeriesStore) getWritePointCalls() []writePointCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]writePointCall, len(m.writePointCalls))
	copy(cp, m.writePointCalls)
	return cp
}

func (m *mockTimeSeriesStore) getWriteBatchCalls() [][]TSDataPoint {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([][]TSDataPoint, len(m.writeBatchCalls))
	copy(cp, m.writeBatchCalls)
	return cp
}

func (m *mockTimeSeriesStore) getDeleteSeriesCalls() []deleteSeriesCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]deleteSeriesCall, len(m.deleteSeriesCalls))
	copy(cp, m.deleteSeriesCalls)
	return cp
}

func (m *mockTimeSeriesStore) getSetRetentionCalls() []setRetentionCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]setRetentionCall, len(m.setRetentionCalls))
	copy(cp, m.setRetentionCalls)
	return cp
}

// ---------------------------------------------------------------------------
// Test helper
// ---------------------------------------------------------------------------

func newTestFSMWithTS(ts TimeSeriesStore) (*FastStateMachine, *memStorage) {
	store := newMemStorage()
	fsm := NewFastStateMachine(1, 1, store, WithTimeSeriesStore(ts))
	return fsm, store
}

// ---------------------------------------------------------------------------
// Write handler tests: TSWrite
// ---------------------------------------------------------------------------

func TestFSM_TSWrite_Success(t *testing.T) {
	ts := &mockTimeSeriesStore{}
	fsm, _ := newTestFSMWithTS(ts)

	tags := map[string]string{"host": "web1"}
	cmd := protocol.EncodeTSWrite("cpu.usage", 1000000, 42.5, tags)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("Update(ts write) failed: %v", err)
	}
	if results[0].Value == 0 {
		t.Fatal("expected non-zero revision from ts write")
	}

	calls := ts.getWritePointCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 WriteDataPoint call, got %d", len(calls))
	}
	c := calls[0]
	if c.Metric != "cpu.usage" {
		t.Fatalf("expected metric 'cpu.usage', got %q", c.Metric)
	}
	if c.Timestamp != 1000000 {
		t.Fatalf("expected timestamp 1000000, got %d", c.Timestamp)
	}
	if c.Value != 42.5 {
		t.Fatalf("expected value 42.5, got %f", c.Value)
	}
	if c.Tags["host"] != "web1" {
		t.Fatalf("expected tag host=web1, got %q", c.Tags["host"])
	}
}

func TestFSM_TSWrite_NotConfigured(t *testing.T) {
	fsm, _ := newTestFSM() // no TimeSeriesStore

	cmd := protocol.EncodeTSWrite("cpu.usage", 1000000, 42.5, nil)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError, got nil")
	}

	var svcErr *ServiceNotConfiguredError
	if !errors.As(err, &svcErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
	if svcErr.Service != "timeseries" {
		t.Fatalf("expected service 'timeseries', got %q", svcErr.Service)
	}
}

func TestFSM_TSWrite_StoreError(t *testing.T) {
	storeErr := errors.New("disk write failure")
	ts := &mockTimeSeriesStore{writePointErr: storeErr}
	fsm, _ := newTestFSMWithTS(ts)

	cmd := protocol.EncodeTSWrite("cpu.usage", 1000000, 42.5, nil)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected StorageError, got nil")
	}

	var storageErr *StorageError
	if !errors.As(err, &storageErr) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
	if storageErr.Op != "ts-write" {
		t.Fatalf("expected op 'ts-write', got %q", storageErr.Op)
	}
	if !errors.Is(err, storeErr) {
		t.Fatalf("expected underlying error to be disk write failure, got %v", storageErr.Inner)
	}
}

func TestFSM_TSWrite_NoTags(t *testing.T) {
	ts := &mockTimeSeriesStore{}
	fsm, _ := newTestFSMWithTS(ts)

	cmd := protocol.EncodeTSWrite("mem.free", 2000000, 1024.0, nil)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("Update(ts write no tags) failed: %v", err)
	}

	calls := ts.getWritePointCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Metric != "mem.free" {
		t.Fatalf("expected metric 'mem.free', got %q", calls[0].Metric)
	}
	if len(calls[0].Tags) != 0 {
		t.Fatalf("expected empty tags, got %v", calls[0].Tags)
	}
}

// ---------------------------------------------------------------------------
// Write handler tests: TSWriteBatch
// ---------------------------------------------------------------------------

func TestFSM_TSWriteBatch_Success(t *testing.T) {
	ts := &mockTimeSeriesStore{}
	fsm, _ := newTestFSMWithTS(ts)

	points := []protocol.TSWriteParams{
		{Metric: "cpu.usage", Timestamp: 100, Value: 10.0, Tags: map[string]string{"host": "a"}},
		{Metric: "cpu.usage", Timestamp: 200, Value: 20.0, Tags: map[string]string{"host": "b"}},
		{Metric: "mem.free", Timestamp: 300, Value: 30.0, Tags: nil},
	}
	cmd := protocol.EncodeTSWriteBatch(points)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("Update(ts write batch) failed: %v", err)
	}
	if results[0].Value == 0 {
		t.Fatal("expected non-zero revision from batch write")
	}

	batchCalls := ts.getWriteBatchCalls()
	if len(batchCalls) != 1 {
		t.Fatalf("expected 1 WriteDataPoints call, got %d", len(batchCalls))
	}
	received := batchCalls[0]
	if len(received) != 3 {
		t.Fatalf("expected 3 points in batch, got %d", len(received))
	}

	if received[0].Metric != "cpu.usage" || received[0].Timestamp != 100 || received[0].Value != 10.0 {
		t.Fatalf("point 0 mismatch: %+v", received[0])
	}
	if received[0].Tags["host"] != "a" {
		t.Fatalf("point 0 tags mismatch: %v", received[0].Tags)
	}
	if received[1].Metric != "cpu.usage" || received[1].Timestamp != 200 || received[1].Value != 20.0 {
		t.Fatalf("point 1 mismatch: %+v", received[1])
	}
	if received[2].Metric != "mem.free" || received[2].Timestamp != 300 || received[2].Value != 30.0 {
		t.Fatalf("point 2 mismatch: %+v", received[2])
	}
}

func TestFSM_TSWriteBatch_NotConfigured(t *testing.T) {
	fsm, _ := newTestFSM() // no TimeSeriesStore

	points := []protocol.TSWriteParams{
		{Metric: "cpu.usage", Timestamp: 100, Value: 10.0, Tags: nil},
	}
	cmd := protocol.EncodeTSWriteBatch(points)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError, got nil")
	}

	var svcErr *ServiceNotConfiguredError
	if !errors.As(err, &svcErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
	if svcErr.Service != "timeseries" {
		t.Fatalf("expected service 'timeseries', got %q", svcErr.Service)
	}
}

func TestFSM_TSWriteBatch_StoreError(t *testing.T) {
	batchErr := errors.New("batch insert failure")
	ts := &mockTimeSeriesStore{writeBatchErr: batchErr}
	fsm, _ := newTestFSMWithTS(ts)

	points := []protocol.TSWriteParams{
		{Metric: "cpu.usage", Timestamp: 100, Value: 10.0, Tags: nil},
	}
	cmd := protocol.EncodeTSWriteBatch(points)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected StorageError, got nil")
	}

	var storageErr *StorageError
	if !errors.As(err, &storageErr) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
	if storageErr.Op != "ts-write-batch" {
		t.Fatalf("expected op 'ts-write-batch', got %q", storageErr.Op)
	}
	if !errors.Is(err, batchErr) {
		t.Fatalf("expected underlying error, got %v", storageErr.Inner)
	}
}

// ---------------------------------------------------------------------------
// Write handler tests: TSWritePoint
// ---------------------------------------------------------------------------

func TestFSM_TSWritePoint_Success(t *testing.T) {
	ts := &mockTimeSeriesStore{}
	fsm, _ := newTestFSMWithTS(ts)

	tags := map[string]string{"region": "us-east-1"}
	cmd := protocol.EncodeTSWritePoint("disk.iops", 5000000, 99.9, tags)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("Update(ts write point) failed: %v", err)
	}
	if results[0].Value == 0 {
		t.Fatal("expected non-zero revision from ts write point")
	}

	calls := ts.getWritePointCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 WriteDataPoint call, got %d", len(calls))
	}
	c := calls[0]
	if c.Metric != "disk.iops" {
		t.Fatalf("expected metric 'disk.iops', got %q", c.Metric)
	}
	if c.Timestamp != 5000000 {
		t.Fatalf("expected timestamp 5000000, got %d", c.Timestamp)
	}
	if c.Value != 99.9 {
		t.Fatalf("expected value 99.9, got %f", c.Value)
	}
	if c.Tags["region"] != "us-east-1" {
		t.Fatalf("expected tag region=us-east-1, got %q", c.Tags["region"])
	}
}

func TestFSM_TSWritePoint_NotConfigured(t *testing.T) {
	fsm, _ := newTestFSM() // no TimeSeriesStore

	cmd := protocol.EncodeTSWritePoint("disk.iops", 5000000, 99.9, nil)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError, got nil")
	}

	var svcErr *ServiceNotConfiguredError
	if !errors.As(err, &svcErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
	if svcErr.Service != "timeseries" {
		t.Fatalf("expected service 'timeseries', got %q", svcErr.Service)
	}
}

func TestFSM_TSWritePoint_StoreError(t *testing.T) {
	wpErr := errors.New("write point failure")
	ts := &mockTimeSeriesStore{writePointErr: wpErr}
	fsm, _ := newTestFSMWithTS(ts)

	cmd := protocol.EncodeTSWritePoint("disk.iops", 5000000, 99.9, nil)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected StorageError, got nil")
	}

	var storageErr *StorageError
	if !errors.As(err, &storageErr) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
	if storageErr.Op != "ts-write-point" {
		t.Fatalf("expected op 'ts-write-point', got %q", storageErr.Op)
	}
}

// ---------------------------------------------------------------------------
// Write handler tests: TSDeleteSeries
// ---------------------------------------------------------------------------

func TestFSM_TSDeleteSeries_Success(t *testing.T) {
	ts := &mockTimeSeriesStore{}
	fsm, _ := newTestFSMWithTS(ts)

	tags := map[string]string{"env": "prod"}
	cmd := protocol.EncodeTSDeleteSeries("cpu.usage", 1000, 9000, tags)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("Update(ts delete series) failed: %v", err)
	}
	if results[0].Value == 0 {
		t.Fatal("expected non-zero revision from ts delete series")
	}

	calls := ts.getDeleteSeriesCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 DeleteSeries call, got %d", len(calls))
	}
	c := calls[0]
	if c.Metric != "cpu.usage" {
		t.Fatalf("expected metric 'cpu.usage', got %q", c.Metric)
	}
	if c.StartNs != 1000 {
		t.Fatalf("expected startNs 1000, got %d", c.StartNs)
	}
	if c.EndNs != 9000 {
		t.Fatalf("expected endNs 9000, got %d", c.EndNs)
	}
	if c.Tags["env"] != "prod" {
		t.Fatalf("expected tag env=prod, got %q", c.Tags["env"])
	}
}

func TestFSM_TSDeleteSeries_NotConfigured(t *testing.T) {
	fsm, _ := newTestFSM() // no TimeSeriesStore

	cmd := protocol.EncodeTSDeleteSeries("cpu.usage", 1000, 9000, nil)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError, got nil")
	}

	var svcErr *ServiceNotConfiguredError
	if !errors.As(err, &svcErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
	if svcErr.Service != "timeseries" {
		t.Fatalf("expected service 'timeseries', got %q", svcErr.Service)
	}
}

func TestFSM_TSDeleteSeries_StoreError(t *testing.T) {
	delErr := errors.New("delete series failure")
	ts := &mockTimeSeriesStore{deleteSeriesErr: delErr}
	fsm, _ := newTestFSMWithTS(ts)

	cmd := protocol.EncodeTSDeleteSeries("cpu.usage", 1000, 9000, nil)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected StorageError, got nil")
	}

	var storageErr *StorageError
	if !errors.As(err, &storageErr) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
	if storageErr.Op != "ts-delete-series" {
		t.Fatalf("expected op 'ts-delete-series', got %q", storageErr.Op)
	}
	if !errors.Is(err, delErr) {
		t.Fatalf("expected underlying error, got %v", storageErr.Inner)
	}
}

// ---------------------------------------------------------------------------
// Write handler tests: TSSetRetention
// ---------------------------------------------------------------------------

func TestFSM_TSSetRetention_Success(t *testing.T) {
	ts := &mockTimeSeriesStore{}
	fsm, _ := newTestFSMWithTS(ts)

	cmd := protocol.EncodeTSSetRetention("cpu.usage", 86400000000000, 60000000000, byte(timeseries.AggAvg))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("Update(ts set retention) failed: %v", err)
	}
	if results[0].Value == 0 {
		t.Fatal("expected non-zero revision from ts set retention")
	}

	calls := ts.getSetRetentionCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 SetRetentionPolicy call, got %d", len(calls))
	}
	c := calls[0]
	if c.Metric != "cpu.usage" {
		t.Fatalf("expected metric 'cpu.usage', got %q", c.Metric)
	}
	if c.DurationNs != 86400000000000 {
		t.Fatalf("expected durationNs 86400000000000, got %d", c.DurationNs)
	}
	if c.ResolutionNs != 60000000000 {
		t.Fatalf("expected resolutionNs 60000000000, got %d", c.ResolutionNs)
	}
	if c.Aggregation != byte(timeseries.AggAvg) {
		t.Fatalf("expected aggregation %d, got %d", timeseries.AggAvg, c.Aggregation)
	}
}

func TestFSM_TSSetRetention_NotConfigured(t *testing.T) {
	fsm, _ := newTestFSM() // no TimeSeriesStore

	cmd := protocol.EncodeTSSetRetention("cpu.usage", 86400000000000, 60000000000, byte(timeseries.AggAvg))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError, got nil")
	}

	var svcErr *ServiceNotConfiguredError
	if !errors.As(err, &svcErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
	if svcErr.Service != "timeseries" {
		t.Fatalf("expected service 'timeseries', got %q", svcErr.Service)
	}
}

func TestFSM_TSSetRetention_StoreError(t *testing.T) {
	retErr := errors.New("retention policy failure")
	ts := &mockTimeSeriesStore{setRetentionErr: retErr}
	fsm, _ := newTestFSMWithTS(ts)

	cmd := protocol.EncodeTSSetRetention("cpu.usage", 86400000000000, 60000000000, byte(timeseries.AggAvg))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected StorageError, got nil")
	}

	var storageErr *StorageError
	if !errors.As(err, &storageErr) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
	if storageErr.Op != "ts-set-retention" {
		t.Fatalf("expected op 'ts-set-retention', got %q", storageErr.Op)
	}
	if !errors.Is(err, retErr) {
		t.Fatalf("expected underlying error, got %v", storageErr.Inner)
	}
}

// ---------------------------------------------------------------------------
// Lookup handler tests: TSQueryRequest (struct)
// ---------------------------------------------------------------------------

func TestFSM_TSLookup_QueryRange(t *testing.T) {
	expectedResult := []timeseries.DataPoint{
		{Metric: "cpu", Value: 50.0},
	}
	ts := &mockTimeSeriesStore{queryRangeResult: expectedResult}
	fsm, _ := newTestFSMWithTS(ts)

	start := time.Unix(0, 100)
	end := time.Unix(0, 200)
	query := timeseries.TSQueryRequest{
		Metric: "cpu",
		Start:  start,
		End:    end,
	}

	result, err := fsm.Lookup(context.Background(), query)
	if err != nil {
		t.Fatalf("Lookup(TSQueryRequest) failed: %v", err)
	}

	points, ok := result.([]timeseries.DataPoint)
	if !ok {
		t.Fatalf("expected []timeseries.DataPoint, got %T", result)
	}
	if len(points) != 1 {
		t.Fatalf("expected 1 point, got %d", len(points))
	}
	if points[0].Metric != "cpu" {
		t.Fatalf("expected metric 'cpu', got %q", points[0].Metric)
	}
}

func TestFSM_TSLookup_QueryRange_Pointer(t *testing.T) {
	expectedResult := []timeseries.DataPoint{
		{Metric: "cpu", Value: 75.0},
	}
	ts := &mockTimeSeriesStore{queryRangeResult: expectedResult}
	fsm, _ := newTestFSMWithTS(ts)

	start := time.Unix(0, 100)
	end := time.Unix(0, 200)
	query := &timeseries.TSQueryRequest{
		Metric: "cpu",
		Start:  start,
		End:    end,
	}

	result, err := fsm.Lookup(context.Background(), query)
	if err != nil {
		t.Fatalf("Lookup(*TSQueryRequest) failed: %v", err)
	}

	points, ok := result.([]timeseries.DataPoint)
	if !ok {
		t.Fatalf("expected []timeseries.DataPoint, got %T", result)
	}
	if len(points) != 1 {
		t.Fatalf("expected 1 point, got %d", len(points))
	}
	if points[0].Value != 75.0 {
		t.Fatalf("expected value 75.0, got %f", points[0].Value)
	}
}

func TestFSM_TSLookup_QueryRange_Error(t *testing.T) {
	qErr := errors.New("query range failure")
	ts := &mockTimeSeriesStore{queryRangeErr: qErr}
	fsm, _ := newTestFSMWithTS(ts)

	query := timeseries.TSQueryRequest{
		Metric: "cpu",
		Start:  time.Unix(0, 100),
		End:    time.Unix(0, 200),
	}

	_, err := fsm.Lookup(context.Background(), query)
	if err == nil {
		t.Fatal("expected error from QueryRange, got nil")
	}
	if !errors.Is(err, qErr) {
		t.Fatalf("expected query range failure error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Lookup handler tests: TSAggregateRequest
// ---------------------------------------------------------------------------

func TestFSM_TSLookup_Aggregate(t *testing.T) {
	expectedResult := []timeseries.AggResult{
		{Timestamp: time.Unix(0, 100), Value: 42.0, Count: 5},
	}
	ts := &mockTimeSeriesStore{queryAggResult: expectedResult}
	fsm, _ := newTestFSMWithTS(ts)

	query := timeseries.TSAggregateRequest{
		Metric:   "cpu",
		Start:    time.Unix(0, 100),
		End:      time.Unix(0, 200),
		Func:     timeseries.AggAvg,
		Interval: 10 * time.Second,
	}

	result, err := fsm.Lookup(context.Background(), query)
	if err != nil {
		t.Fatalf("Lookup(TSAggregateRequest) failed: %v", err)
	}

	aggResults, ok := result.([]timeseries.AggResult)
	if !ok {
		t.Fatalf("expected []timeseries.AggResult, got %T", result)
	}
	if len(aggResults) != 1 {
		t.Fatalf("expected 1 agg result, got %d", len(aggResults))
	}
	if aggResults[0].Value != 42.0 {
		t.Fatalf("expected aggregated value 42.0, got %f", aggResults[0].Value)
	}
}

func TestFSM_TSLookup_Aggregate_Pointer(t *testing.T) {
	expectedResult := []timeseries.AggResult{
		{Timestamp: time.Unix(0, 100), Value: 55.0, Count: 10},
	}
	ts := &mockTimeSeriesStore{queryAggResult: expectedResult}
	fsm, _ := newTestFSMWithTS(ts)

	query := &timeseries.TSAggregateRequest{
		Metric:   "cpu",
		Start:    time.Unix(0, 100),
		End:      time.Unix(0, 200),
		Func:     timeseries.AggSum,
		Interval: 5 * time.Second,
	}

	result, err := fsm.Lookup(context.Background(), query)
	if err != nil {
		t.Fatalf("Lookup(*TSAggregateRequest) failed: %v", err)
	}

	aggResults, ok := result.([]timeseries.AggResult)
	if !ok {
		t.Fatalf("expected []timeseries.AggResult, got %T", result)
	}
	if aggResults[0].Value != 55.0 {
		t.Fatalf("expected aggregated value 55.0, got %f", aggResults[0].Value)
	}
}

func TestFSM_TSLookup_Aggregate_Error(t *testing.T) {
	aggErr := errors.New("aggregate failure")
	ts := &mockTimeSeriesStore{queryAggErr: aggErr}
	fsm, _ := newTestFSMWithTS(ts)

	query := timeseries.TSAggregateRequest{
		Metric:   "cpu",
		Start:    time.Unix(0, 100),
		End:      time.Unix(0, 200),
		Func:     timeseries.AggAvg,
		Interval: 10 * time.Second,
	}

	_, err := fsm.Lookup(context.Background(), query)
	if err == nil {
		t.Fatal("expected error from QueryAggregate, got nil")
	}
	if !errors.Is(err, aggErr) {
		t.Fatalf("expected aggregate failure error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Lookup handler tests: TSRichQueryRequest
// ---------------------------------------------------------------------------

func TestFSM_TSLookup_RichQuery(t *testing.T) {
	expectedResult := &timeseries.QueryResult{
		Metric: "cpu.usage",
		Points: []*timeseries.DataPoint{
			{Metric: "cpu.usage", Value: 45.0},
		},
	}
	ts := &mockTimeSeriesStore{queryRichResult: expectedResult}
	fsm, _ := newTestFSMWithTS(ts)

	query := timeseries.TSRichQueryRequest{
		Request: &timeseries.QueryRequest{
			Metric:    "cpu.usage",
			StartTime: time.Unix(0, 100),
			EndTime:   time.Unix(0, 200),
		},
	}

	result, err := fsm.Lookup(context.Background(), query)
	if err != nil {
		t.Fatalf("Lookup(TSRichQueryRequest) failed: %v", err)
	}

	qr, ok := result.(*timeseries.QueryResult)
	if !ok {
		t.Fatalf("expected *timeseries.QueryResult, got %T", result)
	}
	if qr.Metric != "cpu.usage" {
		t.Fatalf("expected metric 'cpu.usage', got %q", qr.Metric)
	}
}

func TestFSM_TSLookup_RichQuery_Pointer(t *testing.T) {
	expectedResult := &timeseries.QueryResult{
		Metric: "mem.usage",
	}
	ts := &mockTimeSeriesStore{queryRichResult: expectedResult}
	fsm, _ := newTestFSMWithTS(ts)

	query := &timeseries.TSRichQueryRequest{
		Request: &timeseries.QueryRequest{
			Metric:    "mem.usage",
			StartTime: time.Unix(0, 100),
			EndTime:   time.Unix(0, 200),
		},
	}

	result, err := fsm.Lookup(context.Background(), query)
	if err != nil {
		t.Fatalf("Lookup(*TSRichQueryRequest) failed: %v", err)
	}

	qr, ok := result.(*timeseries.QueryResult)
	if !ok {
		t.Fatalf("expected *timeseries.QueryResult, got %T", result)
	}
	if qr.Metric != "mem.usage" {
		t.Fatalf("expected metric 'mem.usage', got %q", qr.Metric)
	}
}

func TestFSM_TSLookup_RichQuery_Error(t *testing.T) {
	richErr := errors.New("rich query failure")
	ts := &mockTimeSeriesStore{queryRichErr: richErr}
	fsm, _ := newTestFSMWithTS(ts)

	query := timeseries.TSRichQueryRequest{
		Request: &timeseries.QueryRequest{
			Metric:    "cpu.usage",
			StartTime: time.Unix(0, 100),
			EndTime:   time.Unix(0, 200),
		},
	}

	_, err := fsm.Lookup(context.Background(), query)
	if err == nil {
		t.Fatal("expected error from QueryRich, got nil")
	}
	if !errors.Is(err, richErr) {
		t.Fatalf("expected rich query failure error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Lookup handler tests: TSGetRetentionRequest
// ---------------------------------------------------------------------------

func TestFSM_TSLookup_GetRetention(t *testing.T) {
	expectedPolicy := &timeseries.RetentionPolicy{
		Duration:    24 * time.Hour,
		Resolution:  time.Minute,
		Aggregation: timeseries.AggAvg,
	}
	ts := &mockTimeSeriesStore{getRetentionResult: expectedPolicy}
	fsm, _ := newTestFSMWithTS(ts)

	query := timeseries.TSGetRetentionRequest{Metric: "cpu"}

	result, err := fsm.Lookup(context.Background(), query)
	if err != nil {
		t.Fatalf("Lookup(TSGetRetentionRequest) failed: %v", err)
	}

	policy, ok := result.(*timeseries.RetentionPolicy)
	if !ok {
		t.Fatalf("expected *timeseries.RetentionPolicy, got %T", result)
	}
	if policy.Duration != 24*time.Hour {
		t.Fatalf("expected duration 24h, got %v", policy.Duration)
	}
	if policy.Resolution != time.Minute {
		t.Fatalf("expected resolution 1m, got %v", policy.Resolution)
	}
}

func TestFSM_TSLookup_GetRetention_Pointer(t *testing.T) {
	expectedPolicy := &timeseries.RetentionPolicy{
		Duration: 7 * 24 * time.Hour,
	}
	ts := &mockTimeSeriesStore{getRetentionResult: expectedPolicy}
	fsm, _ := newTestFSMWithTS(ts)

	query := &timeseries.TSGetRetentionRequest{Metric: "mem"}

	result, err := fsm.Lookup(context.Background(), query)
	if err != nil {
		t.Fatalf("Lookup(*TSGetRetentionRequest) failed: %v", err)
	}

	policy, ok := result.(*timeseries.RetentionPolicy)
	if !ok {
		t.Fatalf("expected *timeseries.RetentionPolicy, got %T", result)
	}
	if policy.Duration != 7*24*time.Hour {
		t.Fatalf("expected duration 168h, got %v", policy.Duration)
	}
}

func TestFSM_TSLookup_GetRetention_Error(t *testing.T) {
	retErr := errors.New("get retention failure")
	ts := &mockTimeSeriesStore{getRetentionErr: retErr}
	fsm, _ := newTestFSMWithTS(ts)

	query := timeseries.TSGetRetentionRequest{Metric: "cpu"}

	_, err := fsm.Lookup(context.Background(), query)
	if err == nil {
		t.Fatal("expected error from GetRetentionPolicy, got nil")
	}
	if !errors.Is(err, retErr) {
		t.Fatalf("expected get retention failure error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Lookup handler tests: TSListMetricsRequest
// ---------------------------------------------------------------------------

func TestFSM_TSLookup_ListMetrics(t *testing.T) {
	ts := &mockTimeSeriesStore{
		listMetricsResult: []string{"cpu.usage", "cpu.idle", "cpu.system"},
	}
	fsm, _ := newTestFSMWithTS(ts)

	query := timeseries.TSListMetricsRequest{Prefix: "cpu", Limit: 10}

	result, err := fsm.Lookup(context.Background(), query)
	if err != nil {
		t.Fatalf("Lookup(TSListMetricsRequest) failed: %v", err)
	}

	metrics, ok := result.([]string)
	if !ok {
		t.Fatalf("expected []string, got %T", result)
	}
	if len(metrics) != 3 {
		t.Fatalf("expected 3 metrics, got %d", len(metrics))
	}
	if metrics[0] != "cpu.usage" {
		t.Fatalf("expected first metric 'cpu.usage', got %q", metrics[0])
	}
}

func TestFSM_TSLookup_ListMetrics_Pointer(t *testing.T) {
	ts := &mockTimeSeriesStore{
		listMetricsResult: []string{"mem.free", "mem.used"},
	}
	fsm, _ := newTestFSMWithTS(ts)

	query := &timeseries.TSListMetricsRequest{Prefix: "mem", Limit: 5}

	result, err := fsm.Lookup(context.Background(), query)
	if err != nil {
		t.Fatalf("Lookup(*TSListMetricsRequest) failed: %v", err)
	}

	metrics, ok := result.([]string)
	if !ok {
		t.Fatalf("expected []string, got %T", result)
	}
	if len(metrics) != 2 {
		t.Fatalf("expected 2 metrics, got %d", len(metrics))
	}
}

func TestFSM_TSLookup_ListMetrics_Error(t *testing.T) {
	listErr := errors.New("list metrics failure")
	ts := &mockTimeSeriesStore{listMetricsErr: listErr}
	fsm, _ := newTestFSMWithTS(ts)

	query := timeseries.TSListMetricsRequest{Prefix: "cpu", Limit: 10}

	_, err := fsm.Lookup(context.Background(), query)
	if err == nil {
		t.Fatal("expected error from ListMetrics, got nil")
	}
	if !errors.Is(err, listErr) {
		t.Fatalf("expected list metrics failure error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Lookup handler tests: Not configured
// ---------------------------------------------------------------------------

func TestFSM_TSLookup_NotConfigured(t *testing.T) {
	fsm, _ := newTestFSM() // no TimeSeriesStore

	queries := []struct {
		name  string
		query interface{}
	}{
		{"TSQueryRequest", timeseries.TSQueryRequest{Metric: "cpu", Start: time.Unix(0, 100), End: time.Unix(0, 200)}},
		{"*TSQueryRequest", &timeseries.TSQueryRequest{Metric: "cpu", Start: time.Unix(0, 100), End: time.Unix(0, 200)}},
		{"TSAggregateRequest", timeseries.TSAggregateRequest{Metric: "cpu", Start: time.Unix(0, 100), End: time.Unix(0, 200), Func: timeseries.AggAvg, Interval: time.Second}},
		{"*TSAggregateRequest", &timeseries.TSAggregateRequest{Metric: "cpu", Start: time.Unix(0, 100), End: time.Unix(0, 200), Func: timeseries.AggAvg, Interval: time.Second}},
		{"TSRichQueryRequest", timeseries.TSRichQueryRequest{Request: &timeseries.QueryRequest{Metric: "cpu"}}},
		{"*TSRichQueryRequest", &timeseries.TSRichQueryRequest{Request: &timeseries.QueryRequest{Metric: "cpu"}}},
		{"TSGetRetentionRequest", timeseries.TSGetRetentionRequest{Metric: "cpu"}},
		{"*TSGetRetentionRequest", &timeseries.TSGetRetentionRequest{Metric: "cpu"}},
		{"TSListMetricsRequest", timeseries.TSListMetricsRequest{Prefix: "cpu", Limit: 10}},
		{"*TSListMetricsRequest", &timeseries.TSListMetricsRequest{Prefix: "cpu", Limit: 10}},
	}

	for _, tc := range queries {
		t.Run(tc.name, func(t *testing.T) {
			_, err := fsm.Lookup(context.Background(), tc.query)
			if err == nil {
				t.Fatal("expected ServiceNotConfiguredError, got nil")
			}

			var svcErr *ServiceNotConfiguredError
			if !errors.As(err, &svcErr) {
				t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
			}
			if svcErr.Service != "timeseries" {
				t.Fatalf("expected service 'timeseries', got %q", svcErr.Service)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Write handler tests: Invalid payload (decode errors)
// ---------------------------------------------------------------------------

func TestFSM_TSWrite_InvalidPayload(t *testing.T) {
	ts := &mockTimeSeriesStore{}
	fsm, _ := newTestFSMWithTS(ts)

	// OpTSWrite with truncated payload (just the opcode, no metric data).
	cmd := []byte{protocol.OpTSWrite, 0x01}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected IncompleteDataError for truncated ts write, got nil")
	}

	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

func TestFSM_TSWriteBatch_InvalidPayload(t *testing.T) {
	ts := &mockTimeSeriesStore{}
	fsm, _ := newTestFSMWithTS(ts)

	// OpTSWriteBatch with truncated payload (just 1 byte, need at least 2 for count).
	cmd := []byte{protocol.OpTSWriteBatch, 0x01}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected IncompleteDataError for truncated ts write batch, got nil")
	}

	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

func TestFSM_TSDeleteSeries_InvalidPayload(t *testing.T) {
	ts := &mockTimeSeriesStore{}
	fsm, _ := newTestFSMWithTS(ts)

	// OpTSDeleteSeries with truncated payload.
	cmd := []byte{protocol.OpTSDeleteSeries, 0x01}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected IncompleteDataError for truncated ts delete series, got nil")
	}

	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

func TestFSM_TSSetRetention_InvalidPayload(t *testing.T) {
	ts := &mockTimeSeriesStore{}
	fsm, _ := newTestFSMWithTS(ts)

	// OpTSSetRetention with truncated payload.
	cmd := []byte{protocol.OpTSSetRetention, 0x01}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected IncompleteDataError for truncated ts set retention, got nil")
	}

	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Write handler tests: Multiple tags
// ---------------------------------------------------------------------------

func TestFSM_TSWrite_MultipleTags(t *testing.T) {
	ts := &mockTimeSeriesStore{}
	fsm, _ := newTestFSMWithTS(ts)

	tags := map[string]string{
		"host":   "web1",
		"region": "us-east-1",
		"env":    "production",
	}
	cmd := protocol.EncodeTSWrite("cpu.usage", 1000000, 42.5, tags)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("Update(ts write multi tags) failed: %v", err)
	}

	calls := ts.getWritePointCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if len(calls[0].Tags) != 3 {
		t.Fatalf("expected 3 tags, got %d", len(calls[0].Tags))
	}
	if calls[0].Tags["host"] != "web1" {
		t.Fatalf("expected tag host=web1, got %q", calls[0].Tags["host"])
	}
	if calls[0].Tags["region"] != "us-east-1" {
		t.Fatalf("expected tag region=us-east-1, got %q", calls[0].Tags["region"])
	}
	if calls[0].Tags["env"] != "production" {
		t.Fatalf("expected tag env=production, got %q", calls[0].Tags["env"])
	}
}

// ---------------------------------------------------------------------------
// Write handler tests: Multiple entries in single Update
// ---------------------------------------------------------------------------

func TestFSM_TSWrite_MultipleEntries(t *testing.T) {
	ts := &mockTimeSeriesStore{}
	fsm, _ := newTestFSMWithTS(ts)

	cmd1 := protocol.EncodeTSWrite("cpu.usage", 100, 10.0, nil)
	cmd2 := protocol.EncodeTSWrite("mem.free", 200, 20.0, nil)
	entries := []sm.Entry{
		{Index: 1, Cmd: cmd1},
		{Index: 2, Cmd: cmd2},
	}
	results := make([]sm.Result, 2)

	err := fsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("Update(ts write multi entries) failed: %v", err)
	}

	if results[0].Value == 0 {
		t.Fatal("expected non-zero revision for first entry")
	}
	if results[1].Value == 0 {
		t.Fatal("expected non-zero revision for second entry")
	}
	if results[0].Value >= results[1].Value {
		t.Fatalf("expected increasing revisions: %d >= %d", results[0].Value, results[1].Value)
	}

	calls := ts.getWritePointCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 WriteDataPoint calls, got %d", len(calls))
	}
	if calls[0].Metric != "cpu.usage" {
		t.Fatalf("expected first metric 'cpu.usage', got %q", calls[0].Metric)
	}
	if calls[1].Metric != "mem.free" {
		t.Fatalf("expected second metric 'mem.free', got %q", calls[1].Metric)
	}
}

// ---------------------------------------------------------------------------
// Lookup handler tests: Nil result from store
// ---------------------------------------------------------------------------

func TestFSM_TSLookup_QueryRange_NilResult(t *testing.T) {
	ts := &mockTimeSeriesStore{queryRangeResult: nil}
	fsm, _ := newTestFSMWithTS(ts)

	query := timeseries.TSQueryRequest{
		Metric: "nonexistent",
		Start:  time.Unix(0, 100),
		End:    time.Unix(0, 200),
	}

	result, err := fsm.Lookup(context.Background(), query)
	if err != nil {
		t.Fatalf("Lookup(TSQueryRequest nil result) failed: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil result, got %v", result)
	}
}

func TestFSM_TSLookup_ListMetrics_EmptyResult(t *testing.T) {
	ts := &mockTimeSeriesStore{listMetricsResult: []string{}}
	fsm, _ := newTestFSMWithTS(ts)

	query := timeseries.TSListMetricsRequest{Prefix: "nonexistent", Limit: 10}

	result, err := fsm.Lookup(context.Background(), query)
	if err != nil {
		t.Fatalf("Lookup(TSListMetricsRequest empty) failed: %v", err)
	}

	metrics, ok := result.([]string)
	if !ok {
		t.Fatalf("expected []string, got %T", result)
	}
	if len(metrics) != 0 {
		t.Fatalf("expected 0 metrics, got %d", len(metrics))
	}
}
