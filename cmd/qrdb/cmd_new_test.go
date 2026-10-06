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

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// Extended test helper
// ---------------------------------------------------------------------------

// newTestRootCmdWithOpts creates a root command wired to embedded mock
// services with additional embedded.Option configuration for optional
// service injection (MVCC, transaction, batch, watch, lease, TS).
func newTestRootCmdWithOpts(kvSvc service.KVServicer, adminSvc service.AdminServicer, opts ...embedded.Option) *cobra.Command {
	rootCmd := newRootCmd()

	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if cmd.Name() == "version" || cmd.Name() == "help" {
			return nil
		}

		format, _ := cmd.Root().PersistentFlags().GetString("output")
		if err := validateOutputFormat(format); err != nil {
			return err
		}
		outputFormat = format

		t, err := embedded.New(kvSvc, adminSvc, opts...)
		if err != nil {
			return err
		}

		c, err := sdk.NewClient(t)
		if err != nil {
			return err
		}

		if err := c.Connect(cmd.Context()); err != nil {
			return err
		}

		client = c
		return nil
	}

	return rootCmd
}

// ---------------------------------------------------------------------------
// Mock MVCC service
// ---------------------------------------------------------------------------

// mockMVCCService implements service.MVCCKVServicer for testing.
type mockMVCCService struct {
	mockKVService
	revision int64

	// restoreFunc, when set, backs RestoreToRevision. Tests that exercise
	// "kv restore" / MVCCBinding.RestoreToRevision configure it directly;
	// callers that never invoke restore can leave it nil.
	restoreFunc func(context.Context, *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error)
}

func newMockMVCCService() *mockMVCCService {
	return &mockMVCCService{
		mockKVService: mockKVService{store: make(map[string][]byte)},
		revision:      42,
	}
}

func (m *mockMVCCService) PutWithLease(_ context.Context, key string, value []byte, leaseID int64) (*service.KeyValue, error) {
	m.store[key] = value
	return &kv.KeyValue{
		Key:            []byte(key),
		Value:          value,
		CreateRevision: 1,
		ModRevision:    1,
		Version:        1,
		LeaseID:        leaseID,
	}, nil
}

func (m *mockMVCCService) GetRevision(_ context.Context, key string) (*service.KeyValue, error) {
	v, ok := m.store[key]
	if !ok {
		return nil, &service.KeyNotFoundError{Key: key}
	}
	return &kv.KeyValue{
		Key:            []byte(key),
		Value:          v,
		CreateRevision: 1,
		ModRevision:    m.revision,
		Version:        1,
		LeaseID:        0,
	}, nil
}

func (m *mockMVCCService) GetAtRevision(_ context.Context, key string, revision int64) (*service.KeyValue, error) {
	v, ok := m.store[key]
	if !ok {
		return nil, &service.KeyNotFoundError{Key: key}
	}
	return &kv.KeyValue{
		Key:            []byte(key),
		Value:          v,
		CreateRevision: 1,
		ModRevision:    revision,
		Version:        1,
		LeaseID:        0,
	}, nil
}

func (m *mockMVCCService) Range(_ context.Context, start, end []byte, _ ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	var kvs []*service.KeyValue
	for k, v := range m.store {
		kb := []byte(k)
		if bytes.Compare(kb, start) >= 0 && (end == nil || bytes.Compare(kb, end) < 0) {
			kvs = append(kvs, &kv.KeyValue{
				Key:            kb,
				Value:          v,
				CreateRevision: 1,
				ModRevision:    m.revision,
				Version:        1,
			})
		}
	}
	return kvs, int64(len(kvs)), m.revision, nil
}

func (m *mockMVCCService) Compact(_ context.Context, _ int64) error {
	return nil
}

func (m *mockMVCCService) CurrentRevision(_ context.Context) (int64, error) {
	return m.revision, nil
}

func (m *mockMVCCService) PutWithLeaseAndMode(_ context.Context, key string, value []byte, leaseID int64, _ writemode.Mode) (*service.KeyValue, error) {
	m.store[key] = value
	return &kv.KeyValue{
		Key:            []byte(key),
		Value:          value,
		CreateRevision: 1,
		ModRevision:    1,
		Version:        1,
		LeaseID:        leaseID,
	}, nil
}

func (m *mockMVCCService) GetRevisionWithConsistency(_ context.Context, key string, _ consistency.ReadOptions) (*service.KeyValue, error) {
	v, ok := m.store[key]
	if !ok {
		return nil, &service.KeyNotFoundError{Key: key}
	}
	return &kv.KeyValue{
		Key:            []byte(key),
		Value:          v,
		CreateRevision: 1,
		ModRevision:    m.revision,
		Version:        1,
	}, nil
}

func (m *mockMVCCService) GetAtRevisionWithConsistency(_ context.Context, key string, revision int64, _ consistency.ReadOptions) (*service.KeyValue, error) {
	v, ok := m.store[key]
	if !ok {
		return nil, &service.KeyNotFoundError{Key: key}
	}
	return &kv.KeyValue{
		Key:            []byte(key),
		Value:          v,
		CreateRevision: 1,
		ModRevision:    revision,
		Version:        1,
	}, nil
}

func (m *mockMVCCService) RangeWithConsistency(ctx context.Context, start, end []byte, _ consistency.ReadOptions, opts ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	return m.Range(ctx, start, end, opts...)
}

// ---------------------------------------------------------------------------
// Mock transaction service
// ---------------------------------------------------------------------------

// mockTransactionService implements service.TransactionServicer for testing.
type mockTransactionService struct{}

func (m *mockTransactionService) Execute(_ context.Context, _ []service.Compare, successOps, _ []service.Op) (*service.TransactionResponse, error) {
	responses := make([]service.OpResponse, len(successOps))
	for i := range successOps {
		responses[i] = service.OpResponse{Succeeded: true}
	}
	return &service.TransactionResponse{
		Succeeded: true,
		Revision:  1,
		Responses: responses,
	}, nil
}

func (m *mockTransactionService) ExecuteWithMode(_ context.Context, _ []service.Compare, successOps, _ []service.Op, _ writemode.Mode) (*service.TransactionResponse, error) {
	responses := make([]service.OpResponse, len(successOps))
	for i := range successOps {
		responses[i] = service.OpResponse{Succeeded: true}
	}
	return &service.TransactionResponse{
		Succeeded: true,
		Revision:  1,
		Responses: responses,
	}, nil
}

func (m *mockTransactionService) ExecuteEntity(_ context.Context, _ *service.TransactionEntityRequest) (*service.TransactionEntityResponse, error) {
	return &service.TransactionEntityResponse{Succeeded: true}, nil
}

// ---------------------------------------------------------------------------
// Mock batch service
// ---------------------------------------------------------------------------

// mockBatchService implements service.BatchServicer for testing.
type mockBatchService struct{}

func (m *mockBatchService) Execute(_ context.Context, ops []service.Op) ([]service.OpResponse, error) {
	responses := make([]service.OpResponse, len(ops))
	for i := range ops {
		responses[i] = service.OpResponse{Succeeded: true}
	}
	return responses, nil
}

func (m *mockBatchService) ExecuteWithMode(_ context.Context, ops []service.Op, _ writemode.Mode) ([]service.OpResponse, error) {
	responses := make([]service.OpResponse, len(ops))
	for i := range ops {
		responses[i] = service.OpResponse{Succeeded: true}
	}
	return responses, nil
}

func (m *mockBatchService) ExecuteEntity(_ context.Context, _ *service.BatchEntityRequest) (*service.BatchEntityResponse, error) {
	return &service.BatchEntityResponse{}, nil
}

func (m *mockBatchService) Import(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
	return nil, nil
}

func (m *mockBatchService) ImportBegin(_ context.Context, _ service.ImportOptions) (string, error) {
	return "", nil
}

func (m *mockBatchService) ImportAppend(_ context.Context, _ string, _ uint64, _ []byte) error {
	return nil
}

func (m *mockBatchService) ImportCommit(_ context.Context, _ string) (*service.ImportResult, error) {
	return nil, nil
}

func (m *mockBatchService) ImportAbort(_ context.Context, _ string) error {
	return nil
}

// SetImportKeyGuard satisfies service.ImportKeyGuardSetter as a no-op:
// this double is shared across CLI/binding plumbing tests, never the
// hard-confinement feature, and no fixture using it names the confined
// key -- a deliberate, visible opt-out for this one concern, not an
// accidental one.
func (m *mockBatchService) SetImportKeyGuard(func(string) error) {}

// ---------------------------------------------------------------------------
// Mock watch service
// ---------------------------------------------------------------------------

// mockWatcher implements watch.Watcher for testing.
type mockWatcher struct {
	events chan watch.WatchResponse
	id     int64
}

func (m *mockWatcher) Events() <-chan watch.WatchResponse { return m.events }
func (m *mockWatcher) WatchID() int64                     { return m.id }
func (m *mockWatcher) Close() error                       { return nil }
func (m *mockWatcher) Err() error                         { return nil }

// newMockWatcher creates a watcher that delivers the given events and
// then closes the channel, causing the watch loop to exit cleanly.
func newMockWatcher(events ...watch.WatchResponse) *mockWatcher {
	ch := make(chan watch.WatchResponse, len(events))
	for _, e := range events {
		ch <- e
	}
	close(ch)
	return &mockWatcher{events: ch, id: 1}
}

// mockWatchService implements service.WatchServicer for testing.
type mockWatchService struct {
	watcher *mockWatcher
}

func newMockWatchService() *mockWatchService {
	return &mockWatchService{
		watcher: newMockWatcher(watch.WatchResponse{
			WatchID: 1,
			Events: []*watch.Event{
				{
					Type: watch.EventPut,
					Kv: &kv.KeyValue{
						Key:   []byte("watched-key"),
						Value: []byte("watched-value"),
					},
				},
			},
			Revision: 5,
		}),
	}
}

func (m *mockWatchService) Watch(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
	return m.watcher, nil
}

func (m *mockWatchService) WatchPrefix(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
	return m.watcher, nil
}

func (m *mockWatchService) WatchEntity(_ context.Context, _ *service.WatchEntityRequest) (<-chan service.WatchEntityEvent, error) {
	ch := make(chan service.WatchEntityEvent, 1)
	ch <- service.WatchEntityEvent{
		EntityType: "test",
		ID:         "1",
		Data:       []byte("entity-data"),
		Revision:   1,
	}
	close(ch)
	return ch, nil
}

func (m *mockWatchService) WatchEntityType(_ context.Context, _ *service.WatchEntityTypeRequest) (<-chan service.WatchEntityEvent, error) {
	ch := make(chan service.WatchEntityEvent, 1)
	ch <- service.WatchEntityEvent{
		EntityType: "test",
		ID:         "1",
		Data:       []byte("entity-data"),
		Revision:   1,
	}
	close(ch)
	return ch, nil
}

// ---------------------------------------------------------------------------
// Mock lease service
// ---------------------------------------------------------------------------

// mockLeaseService implements service.LeaseServicer (alias for lease.LeaseServicer).
type mockLeaseService struct{}

func (m *mockLeaseService) Grant(_ context.Context, ttl int64) (*lease.Lease, error) {
	return &lease.Lease{
		ID:           1,
		TTL:          ttl,
		GrantedTTL:   ttl,
		RemainingTTL: ttl,
	}, nil
}

func (m *mockLeaseService) Revoke(_ context.Context, _ int64) error {
	return nil
}

func (m *mockLeaseService) KeepAlive(_ context.Context, leaseID int64) (*lease.Lease, error) {
	return &lease.Lease{
		ID:           leaseID,
		TTL:          60,
		GrantedTTL:   60,
		RemainingTTL: 60,
	}, nil
}

func (m *mockLeaseService) TimeToLive(_ context.Context, leaseID int64) (*lease.Lease, error) {
	return &lease.Lease{
		ID:           leaseID,
		TTL:          60,
		GrantedTTL:   60,
		RemainingTTL: 30,
		Keys:         [][]byte{[]byte("key1")},
	}, nil
}

func (m *mockLeaseService) Leases(_ context.Context) ([]*lease.Lease, error) {
	return []*lease.Lease{
		{ID: 1, TTL: 60, GrantedTTL: 60, RemainingTTL: 55},
		{ID: 2, TTL: 120, GrantedTTL: 120, RemainingTTL: 100},
	}, nil
}

// ---------------------------------------------------------------------------
// Mock time-series service
// ---------------------------------------------------------------------------

// mockTimeSeriesService implements service.TimeSeriesServicer (alias for
// timeseries.TimeSeriesServicer).
type mockTimeSeriesService struct{}

func (m *mockTimeSeriesService) Write(_ context.Context, _ string, _ float64, _ time.Time, _ map[string]string) error {
	return nil
}

func (m *mockTimeSeriesService) WriteBatch(_ context.Context, _ []timeseries.DataPoint) error {
	return nil
}

func (m *mockTimeSeriesService) Query(_ context.Context, _ string, _, _ time.Time, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	return []timeseries.DataPoint{
		{
			Metric:    "cpu.usage",
			Value:     75.5,
			Timestamp: time.Unix(0, 1000000000),
			Tags:      map[string]string{"host": "server1"},
		},
		{
			Metric:    "cpu.usage",
			Value:     82.3,
			Timestamp: time.Unix(0, 2000000000),
			Tags:      map[string]string{"host": "server1"},
		},
	}, nil
}

func (m *mockTimeSeriesService) QueryWithConsistency(_ context.Context, metric string, start, end time.Time, _ consistency.ReadOptions, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	return m.Query(nil, metric, start, end, opts...)
}

func (m *mockTimeSeriesService) Aggregate(_ context.Context, _ string, _, _ time.Time, _ timeseries.AggFunc, _ time.Duration) ([]timeseries.AggResult, error) {
	return []timeseries.AggResult{
		{
			Timestamp: time.Unix(0, 1000000000),
			Value:     75.5,
			Count:     10,
		},
		{
			Timestamp: time.Unix(0, 2000000000),
			Value:     82.3,
			Count:     15,
		},
	}, nil
}

func (m *mockTimeSeriesService) AggregateWithConsistency(_ context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration, _ consistency.ReadOptions) ([]timeseries.AggResult, error) {
	return m.Aggregate(nil, metric, start, end, fn, interval)
}

func (m *mockTimeSeriesService) WritePoint(_ context.Context, _ *timeseries.DataPoint) error {
	return nil
}

func (m *mockTimeSeriesService) QueryRich(_ context.Context, _ *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
	return &timeseries.QueryResult{}, nil
}

func (m *mockTimeSeriesService) QueryRichWithConsistency(_ context.Context, req *timeseries.QueryRequest, _ consistency.ReadOptions) (*timeseries.QueryResult, error) {
	return m.QueryRich(nil, req)
}

func (m *mockTimeSeriesService) DeleteSeries(_ context.Context, _ *timeseries.DeleteRequest) error {
	return nil
}

func (m *mockTimeSeriesService) GetRetentionPolicy(_ string) (*timeseries.RetentionPolicy, error) {
	return &timeseries.RetentionPolicy{}, nil
}

func (m *mockTimeSeriesService) SetRetentionPolicy(_ context.Context, _ string, _ *timeseries.RetentionPolicy) error {
	return nil
}

func (m *mockTimeSeriesService) ListMetrics(_ string, _ int) ([]string, error) {
	return nil, nil
}

func (m *mockTimeSeriesService) ListMetricsWithConsistency(_ context.Context, prefix string, limit int, _ consistency.ReadOptions) ([]string, error) {
	return m.ListMetrics(prefix, limit)
}

// ---------------------------------------------------------------------------
// Helper: build standard opts for all optional services
// ---------------------------------------------------------------------------

func newAllServiceOpts(
	mvccSvc service.MVCCKVServicer,
	txSvc service.TransactionServicer,
	batchSvc service.BatchServicer,
	watchSvc service.WatchServicer,
	leaseSvc service.LeaseServicer,
	tsSvc service.TimeSeriesServicer,
) []embedded.Option {
	return []embedded.Option{
		embedded.WithMVCCService(mvccSvc),
		embedded.WithTransactionService(txSvc),
		embedded.WithBatchService(batchSvc),
		embedded.WithWatchService(watchSvc),
		embedded.WithLeaseService(leaseSvc),
		embedded.WithTimeSeriesService(tsSvc),
	}
}

// ---------------------------------------------------------------------------
// MVCC / KV enhanced tests
// ---------------------------------------------------------------------------

func TestKVPutWithLeaseID(t *testing.T) {
	mvccSvc := newMockMVCCService()
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "kv", "put", "mykey", "myval", "--lease-id", "42")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("expected OK output, got: %q", out)
	}
	if string(mvccSvc.store["mykey"]) != "myval" {
		t.Fatalf("expected stored value 'myval', got: %q", mvccSvc.store["mykey"])
	}
}

func TestKVPutWithLeaseIDJSON(t *testing.T) {
	mvccSvc := newMockMVCCService()
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "--output", "json", "kv", "put", "mykey", "myval", "--lease-id", "42")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "lease_id") {
		t.Fatalf("expected JSON with lease_id field, got: %q", out)
	}
}

func TestKVGetRevision(t *testing.T) {
	mvccSvc := newMockMVCCService()
	mvccSvc.store["rev-key"] = []byte("rev-value")
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "kv", "get", "rev-key", "--revision")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "rev-value") {
		t.Fatalf("expected value in output, got: %q", out)
	}
	if !strings.Contains(out, "CreateRevision") {
		t.Fatalf("expected CreateRevision metadata, got: %q", out)
	}
}

func TestKVGetRevisionJSON(t *testing.T) {
	mvccSvc := newMockMVCCService()
	mvccSvc.store["rev-key"] = []byte("rev-value")
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "--output", "json", "kv", "get", "rev-key", "--revision")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "create_revision") {
		t.Fatalf("expected JSON create_revision field, got: %q", out)
	}
}

func TestKVGetAtRevision(t *testing.T) {
	mvccSvc := newMockMVCCService()
	mvccSvc.store["hist-key"] = []byte("hist-value")
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "kv", "get", "hist-key", "--at-revision", "10")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "hist-value") {
		t.Fatalf("expected value in output, got: %q", out)
	}
	if !strings.Contains(out, "ModRevision") {
		t.Fatalf("expected ModRevision metadata, got: %q", out)
	}
}

func TestKVRange(t *testing.T) {
	mvccSvc := newMockMVCCService()
	mvccSvc.store["a"] = []byte("1")
	mvccSvc.store["b"] = []byte("2")
	mvccSvc.store["c"] = []byte("3")
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "kv", "range", "a", "d")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "KEY") {
		t.Fatalf("expected table header KEY, got: %q", out)
	}
}

func TestKVRangeJSON(t *testing.T) {
	mvccSvc := newMockMVCCService()
	mvccSvc.store["x"] = []byte("val-x")
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "--output", "json", "kv", "range", "a", "z")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "kvs") {
		t.Fatalf("expected JSON kvs field, got: %q", out)
	}
}

func TestKVRangeCountOnly(t *testing.T) {
	mvccSvc := newMockMVCCService()
	mvccSvc.store["k1"] = []byte("v1")
	mvccSvc.store["k2"] = []byte("v2")
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "kv", "range", "k", "l", "--count-only")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "Count:") {
		t.Fatalf("expected Count: in output, got: %q", out)
	}
}

func TestKVCompact(t *testing.T) {
	mvccSvc := newMockMVCCService()
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "kv", "compact", "--revision", "10")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("expected OK output, got: %q", out)
	}
}

func TestKVCompactMissingRevision(t *testing.T) {
	mvccSvc := newMockMVCCService()
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	_, err := executeCommand(root, "kv", "compact")
	if err == nil {
		t.Fatal("expected error for missing --revision flag")
	}
}

func TestKVRevision(t *testing.T) {
	mvccSvc := newMockMVCCService()
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "kv", "revision")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "42") {
		t.Fatalf("expected revision 42 in output, got: %q", out)
	}
}

func TestKVRevisionJSON(t *testing.T) {
	mvccSvc := newMockMVCCService()
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "--output", "json", "kv", "revision")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, `"revision"`) {
		t.Fatalf("expected JSON revision field, got: %q", out)
	}
}

func TestKVRangeSortOrderInvalid(t *testing.T) {
	mvccSvc := newMockMVCCService()
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	_, err := executeCommand(root, "kv", "range", "a", "z", "--sort-order", "invalid")
	if err == nil {
		t.Fatal("expected error for invalid --sort-order")
	}
	var iae *InvalidArgError
	if !errors.As(err, &iae) {
		t.Fatalf("expected InvalidArgError, got: %T: %v", err, err)
	}
}

func TestKVRangeSortByInvalid(t *testing.T) {
	mvccSvc := newMockMVCCService()
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	_, err := executeCommand(root, "kv", "range", "a", "z", "--sort-by", "invalid")
	if err == nil {
		t.Fatal("expected error for invalid --sort-by")
	}
	var iae *InvalidArgError
	if !errors.As(err, &iae) {
		t.Fatalf("expected InvalidArgError, got: %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Transaction tests
// ---------------------------------------------------------------------------

func TestTxExecuteFromFile(t *testing.T) {
	txReq := transport.TransactionRequest{
		Compares:   []transport.Compare{},
		SuccessOps: []transport.Op{{Type: transport.OpPut, Key: []byte("tx-key"), Value: []byte("tx-val")}},
		FailureOps: []transport.Op{},
	}
	data, err := json.Marshal(txReq)
	if err != nil {
		t.Fatalf("failed to marshal tx request: %v", err)
	}

	tmpFile, err := os.CreateTemp("", "tx-test-*.json")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()
	if _, err := tmpFile.Write(data); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	_ = tmpFile.Close()

	mvccSvc := newMockMVCCService()
	txSvc := &mockTransactionService{}
	opts := newAllServiceOpts(mvccSvc, txSvc, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "tx", "execute", "--file", tmpFile.Name())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "Succeeded") || !strings.Contains(out, "true") {
		t.Fatalf("expected Succeeded: true in output, got: %q", out)
	}
}

func TestTxExecuteFromFileJSON(t *testing.T) {
	txReq := transport.TransactionRequest{
		Compares:   []transport.Compare{},
		SuccessOps: []transport.Op{{Type: transport.OpPut, Key: []byte("tx-key"), Value: []byte("tx-val")}},
		FailureOps: []transport.Op{},
	}
	data, err := json.Marshal(txReq)
	if err != nil {
		t.Fatalf("failed to marshal tx request: %v", err)
	}

	tmpFile, err := os.CreateTemp("", "tx-test-json-*.json")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()
	if _, err := tmpFile.Write(data); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	_ = tmpFile.Close()

	mvccSvc := newMockMVCCService()
	txSvc := &mockTransactionService{}
	opts := newAllServiceOpts(mvccSvc, txSvc, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "--output", "json", "tx", "execute", "--file", tmpFile.Name())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, `"succeeded"`) {
		t.Fatalf("expected JSON succeeded field, got: %q", out)
	}
}

func TestTxExecuteMissingFile(t *testing.T) {
	mvccSvc := newMockMVCCService()
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	_, err := executeCommand(root, "tx", "execute")
	if err == nil {
		t.Fatal("expected error for missing --file flag")
	}
}

func TestTxExecuteInvalidJSON(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "tx-bad-*.json")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()
	if _, err := tmpFile.WriteString("{invalid json}"); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	_ = tmpFile.Close()

	mvccSvc := newMockMVCCService()
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	_, err = executeCommand(root, "tx", "execute", "--file", tmpFile.Name())
	if err == nil {
		t.Fatal("expected error for invalid JSON file")
	}
}

func TestTxExecuteFileNotFound(t *testing.T) {
	mvccSvc := newMockMVCCService()
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	_, err := executeCommand(root, "tx", "execute", "--file", "/nonexistent/path/tx.json")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
	var fre *FileReadError
	if !errors.As(err, &fre) {
		t.Fatalf("expected FileReadError, got: %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Batch tests
// ---------------------------------------------------------------------------

func TestBatchExecuteFromFile(t *testing.T) {
	batchReq := transport.BatchRequest{
		Ops: []transport.Op{{Type: transport.OpPut, Key: []byte("batch-key"), Value: []byte("batch-val")}},
	}
	data, err := json.Marshal(batchReq)
	if err != nil {
		t.Fatalf("failed to marshal batch request: %v", err)
	}

	tmpFile, err := os.CreateTemp("", "batch-test-*.json")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()
	if _, err := tmpFile.Write(data); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	_ = tmpFile.Close()

	mvccSvc := newMockMVCCService()
	batchSvc := &mockBatchService{}
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, batchSvc, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "batch", "execute", "--file", tmpFile.Name())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "succeeded=true") {
		t.Fatalf("expected succeeded=true in output, got: %q", out)
	}
}

func TestBatchExecuteFromFileJSON(t *testing.T) {
	batchReq := transport.BatchRequest{
		Ops: []transport.Op{{Type: transport.OpPut, Key: []byte("batch-key"), Value: []byte("batch-val")}},
	}
	data, err := json.Marshal(batchReq)
	if err != nil {
		t.Fatalf("failed to marshal batch request: %v", err)
	}

	tmpFile, err := os.CreateTemp("", "batch-json-*.json")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()
	if _, err := tmpFile.Write(data); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	_ = tmpFile.Close()

	mvccSvc := newMockMVCCService()
	batchSvc := &mockBatchService{}
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, batchSvc, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "--output", "json", "batch", "execute", "--file", tmpFile.Name())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, `"succeeded"`) {
		t.Fatalf("expected JSON succeeded field, got: %q", out)
	}
}

func TestBatchExecuteMissingFile(t *testing.T) {
	mvccSvc := newMockMVCCService()
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	_, err := executeCommand(root, "batch", "execute")
	if err == nil {
		t.Fatal("expected error for missing --file flag")
	}
}

func TestBatchExecuteFileNotFound(t *testing.T) {
	mvccSvc := newMockMVCCService()
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	_, err := executeCommand(root, "batch", "execute", "--file", "/nonexistent/batch.json")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
	var fre *FileReadError
	if !errors.As(err, &fre) {
		t.Fatalf("expected FileReadError, got: %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Watch tests
// ---------------------------------------------------------------------------

func TestWatchKey(t *testing.T) {
	mvccSvc := newMockMVCCService()
	watchSvc := newMockWatchService()
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, watchSvc, &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	// The mock watcher sends one event then closes the channel,
	// causing the watch loop to exit cleanly when the channel is closed.
	var buf bytes.Buffer
	outputWriter = &buf
	defer func() { outputWriter = &buf }()

	root.SetArgs([]string{"watch", "watched-key"})
	root.SetOut(&buf)
	root.SetErr(&buf)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := root.ExecuteContext(ctx)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "PUT") {
		t.Fatalf("expected PUT event in output, got: %q", out)
	}
	if !strings.Contains(out, "watched-key") {
		t.Fatalf("expected key in output, got: %q", out)
	}
}

func TestWatchWithPrefix(t *testing.T) {
	mvccSvc := newMockMVCCService()
	watchSvc := newMockWatchService()
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, watchSvc, &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	var buf bytes.Buffer
	outputWriter = &buf
	defer func() { outputWriter = &buf }()

	root.SetArgs([]string{"watch", "watched", "--prefix"})
	root.SetOut(&buf)
	root.SetErr(&buf)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := root.ExecuteContext(ctx)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "PUT") {
		t.Fatalf("expected PUT event in output, got: %q", out)
	}
}

func TestWatchKeyJSON(t *testing.T) {
	mvccSvc := newMockMVCCService()
	// Create a dedicated watch service for this test to avoid channel reuse.
	watchSvc := &mockWatchService{
		watcher: newMockWatcher(watch.WatchResponse{
			WatchID: 1,
			Events: []*watch.Event{
				{
					Type: watch.EventPut,
					Kv: &kv.KeyValue{
						Key:   []byte("json-key"),
						Value: []byte("json-value"),
					},
				},
			},
			Revision: 10,
		}),
	}
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, watchSvc, &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	var buf bytes.Buffer
	outputWriter = &buf
	defer func() { outputWriter = &buf }()

	root.SetArgs([]string{"--output", "json", "watch", "json-key"})
	root.SetOut(&buf)
	root.SetErr(&buf)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := root.ExecuteContext(ctx)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "watch_id") {
		t.Fatalf("expected JSON watch_id field, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// Lease tests
// ---------------------------------------------------------------------------

func TestLeaseGrant(t *testing.T) {
	mvccSvc := newMockMVCCService()
	leaseSvc := &mockLeaseService{}
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), leaseSvc, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "lease", "grant", "--ttl", "60")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "ID:") {
		t.Fatalf("expected ID: in output, got: %q", out)
	}
	if !strings.Contains(out, "TTL:") {
		t.Fatalf("expected TTL: in output, got: %q", out)
	}
}

func TestLeaseGrantJSON(t *testing.T) {
	mvccSvc := newMockMVCCService()
	leaseSvc := &mockLeaseService{}
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), leaseSvc, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "--output", "json", "lease", "grant", "--ttl", "60")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, `"id"`) {
		t.Fatalf("expected JSON id field, got: %q", out)
	}
	if !strings.Contains(out, `"ttl"`) {
		t.Fatalf("expected JSON ttl field, got: %q", out)
	}
}

func TestLeaseGrantMissingTTL(t *testing.T) {
	mvccSvc := newMockMVCCService()
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	_, err := executeCommand(root, "lease", "grant")
	if err == nil {
		t.Fatal("expected error for missing --ttl flag")
	}
}

func TestLeaseRevoke(t *testing.T) {
	mvccSvc := newMockMVCCService()
	leaseSvc := &mockLeaseService{}
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), leaseSvc, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "lease", "revoke", "1")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("expected OK output, got: %q", out)
	}
}

func TestLeaseRevokeInvalidID(t *testing.T) {
	mvccSvc := newMockMVCCService()
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	_, err := executeCommand(root, "lease", "revoke", "not-a-number")
	if err == nil {
		t.Fatal("expected error for invalid lease ID")
	}
	var ile *InvalidLeaseIDError
	if !errors.As(err, &ile) {
		t.Fatalf("expected InvalidLeaseIDError, got: %T: %v", err, err)
	}
}

func TestLeaseKeepAlive(t *testing.T) {
	mvccSvc := newMockMVCCService()
	leaseSvc := &mockLeaseService{}
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), leaseSvc, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "lease", "keep-alive", "1")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "ID:") {
		t.Fatalf("expected ID: in output, got: %q", out)
	}
}

func TestLeaseKeepAliveInvalidID(t *testing.T) {
	mvccSvc := newMockMVCCService()
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	_, err := executeCommand(root, "lease", "keep-alive", "abc")
	if err == nil {
		t.Fatal("expected error for invalid lease ID")
	}
	var ile *InvalidLeaseIDError
	if !errors.As(err, &ile) {
		t.Fatalf("expected InvalidLeaseIDError, got: %T: %v", err, err)
	}
}

func TestLeaseTTL(t *testing.T) {
	mvccSvc := newMockMVCCService()
	leaseSvc := &mockLeaseService{}
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), leaseSvc, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "lease", "ttl", "1")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "RemainingTTL:") {
		t.Fatalf("expected RemainingTTL: in output, got: %q", out)
	}
	if !strings.Contains(out, "key1") {
		t.Fatalf("expected key1 in Keys output, got: %q", out)
	}
}

func TestLeaseTTLJSON(t *testing.T) {
	mvccSvc := newMockMVCCService()
	leaseSvc := &mockLeaseService{}
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), leaseSvc, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "--output", "json", "lease", "ttl", "1")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, `"remaining_ttl"`) {
		t.Fatalf("expected JSON remaining_ttl field, got: %q", out)
	}
}

func TestLeaseTTLInvalidID(t *testing.T) {
	mvccSvc := newMockMVCCService()
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	_, err := executeCommand(root, "lease", "ttl", "xyz")
	if err == nil {
		t.Fatal("expected error for invalid lease ID")
	}
	var ile *InvalidLeaseIDError
	if !errors.As(err, &ile) {
		t.Fatalf("expected InvalidLeaseIDError, got: %T: %v", err, err)
	}
}

func TestLeaseList(t *testing.T) {
	mvccSvc := newMockMVCCService()
	leaseSvc := &mockLeaseService{}
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), leaseSvc, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "lease", "list")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "ID") {
		t.Fatalf("expected ID header in table, got: %q", out)
	}
}

func TestLeaseListJSON(t *testing.T) {
	mvccSvc := newMockMVCCService()
	leaseSvc := &mockLeaseService{}
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), leaseSvc, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "--output", "json", "lease", "list")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "[") {
		t.Fatalf("expected JSON array, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// Error type tests
// ---------------------------------------------------------------------------

func TestInvalidLeaseIDErrorFormatting(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"abc", "qrdb: invalid lease ID: abc: must be a positive integer"},
		{"", "qrdb: invalid lease ID: : must be a positive integer"},
		{"-1", "qrdb: invalid lease ID: -1: must be a positive integer"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			err := &InvalidLeaseIDError{Input: tt.input}
			if err.Error() != tt.want {
				t.Errorf("got %q, want %q", err.Error(), tt.want)
			}
		})
	}
}

func TestInvalidAggFuncErrorFormatting(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"bogus", "qrdb: invalid aggregation function: bogus: must be one of avg, sum, min, max, count, percentile"},
		{"AVG", "qrdb: invalid aggregation function: AVG: must be one of avg, sum, min, max, count, percentile"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			err := &InvalidAggFuncError{Input: tt.input}
			if err.Error() != tt.want {
				t.Errorf("got %q, want %q", err.Error(), tt.want)
			}
		})
	}
}

func TestFileReadErrorFormatting(t *testing.T) {
	underlying := errors.New("permission denied")
	err := &FileReadError{Path: "/tmp/test.json", Err: underlying}

	want := "qrdb: failed to read file /tmp/test.json: permission denied"
	if err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}

	if err.Unwrap() != underlying {
		t.Error("Unwrap did not return the underlying error")
	}
}

func TestFileReadErrorUnwrapChain(t *testing.T) {
	inner := errors.New("disk full")
	err := &FileReadError{Path: "/data/file.dat", Err: inner}

	if !errors.Is(err, inner) {
		t.Error("errors.Is should match the inner error")
	}
}

// ---------------------------------------------------------------------------
// Parse helper tests (table-driven)
// ---------------------------------------------------------------------------

func TestParseSortOrder(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    transport.SortOrder
		wantErr bool
	}{
		{"empty defaults to none", "", transport.SortNone, false},
		{"asc", "asc", transport.SortAscend, false},
		{"desc", "desc", transport.SortDescend, false},
		{"invalid", "random", 0, true},
		{"ASC uppercase", "ASC", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSortOrder(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseSortOrder(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("parseSortOrder(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseSortTarget(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    transport.SortTarget
		wantErr bool
	}{
		{"empty defaults to key", "", transport.SortByKey, false},
		{"key", "key", transport.SortByKey, false},
		{"value", "value", transport.SortByValue, false},
		{"version", "version", transport.SortByVersion, false},
		{"create", "create", transport.SortByCreateRevision, false},
		{"mod", "mod", transport.SortByModRevision, false},
		{"invalid", "timestamp", 0, true},
		{"KEY uppercase", "KEY", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSortTarget(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseSortTarget(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("parseSortTarget(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// eventTypeString tests
// ---------------------------------------------------------------------------

func TestEventTypeString(t *testing.T) {
	tests := []struct {
		name  string
		input transport.EventType
		want  string
	}{
		{"put event", transport.EventPut, "PUT"},
		{"delete event", transport.EventDelete, "DELETE"},
		{"unknown event", transport.EventType(99), "UNKNOWN(99)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := eventTypeString(tt.input)
			if got != tt.want {
				t.Errorf("eventTypeString(%d) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// formatLeaseKeys tests
// ---------------------------------------------------------------------------

func TestFormatLeaseKeys(t *testing.T) {
	tests := []struct {
		name string
		keys [][]byte
		want string
	}{
		{"nil keys", nil, ""},
		{"empty keys", [][]byte{}, ""},
		{"single key", [][]byte{[]byte("key1")}, "key1"},
		{"multiple keys", [][]byte{[]byte("key1"), []byte("key2"), []byte("key3")}, "key1, key2, key3"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatLeaseKeys(tt.keys)
			if got != tt.want {
				t.Errorf("formatLeaseKeys() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// leaseInfoRow tests
// ---------------------------------------------------------------------------

func TestLeaseInfoRow(t *testing.T) {
	info := &transport.LeaseInfo{
		ID:           42,
		TTL:          60,
		GrantedTTL:   60,
		RemainingTTL: 30,
		Keys:         [][]byte{[]byte("k1"), []byte("k2")},
	}

	row := leaseInfoRow(info)
	if len(row) != 5 {
		t.Fatalf("expected 5 columns, got %d", len(row))
	}
	if row[0] != "42" {
		t.Errorf("ID column: got %q, want %q", row[0], "42")
	}
	if row[4] != "k1, k2" {
		t.Errorf("Keys column: got %q, want %q", row[4], "k1, k2")
	}
}

func TestLeaseInfoRowEmptyKeys(t *testing.T) {
	info := &transport.LeaseInfo{
		ID:           1,
		TTL:          10,
		GrantedTTL:   10,
		RemainingTTL: 5,
	}

	row := leaseInfoRow(info)
	if row[4] != "" {
		t.Errorf("Keys column: got %q, want empty string", row[4])
	}
}

// ---------------------------------------------------------------------------
// printLeaseInfo output tests
// ---------------------------------------------------------------------------

func TestPrintLeaseInfoTable(t *testing.T) {
	var buf bytes.Buffer
	oldWriter := outputWriter
	outputWriter = &buf
	oldFormat := outputFormat
	outputFormat = "table"
	t.Cleanup(func() {
		outputWriter = oldWriter
		outputFormat = oldFormat
	})

	info := &transport.LeaseInfo{
		ID:           7,
		TTL:          120,
		GrantedTTL:   120,
		RemainingTTL: 90,
		Keys:         [][]byte{[]byte("lease-key")},
	}

	if err := printLeaseInfo(info); err != nil {
		t.Fatalf("printLeaseInfo error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "ID:") {
		t.Fatalf("expected ID: in output, got: %q", out)
	}
	if !strings.Contains(out, "lease-key") {
		t.Fatalf("expected lease-key in Keys output, got: %q", out)
	}
}

func TestPrintLeaseInfoJSON(t *testing.T) {
	var buf bytes.Buffer
	oldWriter := outputWriter
	outputWriter = &buf
	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() {
		outputWriter = oldWriter
		outputFormat = oldFormat
	})

	info := &transport.LeaseInfo{
		ID:           7,
		TTL:          120,
		GrantedTTL:   120,
		RemainingTTL: 90,
	}

	if err := printLeaseInfo(info); err != nil {
		t.Fatalf("printLeaseInfo error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, `"id"`) {
		t.Fatalf("expected JSON id field, got: %q", out)
	}
}

func TestPrintLeaseInfoYAML(t *testing.T) {
	var buf bytes.Buffer
	oldWriter := outputWriter
	outputWriter = &buf
	oldFormat := outputFormat
	outputFormat = "yaml"
	t.Cleanup(func() {
		outputWriter = oldWriter
		outputFormat = oldFormat
	})

	info := &transport.LeaseInfo{
		ID:  7,
		TTL: 120,
	}

	if err := printLeaseInfo(info); err != nil {
		t.Fatalf("printLeaseInfo error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "id:") {
		t.Fatalf("expected YAML id field, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// printMVCCKeyValue output tests
// ---------------------------------------------------------------------------

func TestPrintMVCCKeyValueTable(t *testing.T) {
	var buf bytes.Buffer
	oldWriter := outputWriter
	outputWriter = &buf
	oldFormat := outputFormat
	outputFormat = "table"
	t.Cleanup(func() {
		outputWriter = oldWriter
		outputFormat = oldFormat
	})

	mkv := &transport.MVCCKeyValue{
		Key:            []byte("test-key"),
		Value:          []byte("test-value"),
		CreateRevision: 1,
		ModRevision:    5,
		Version:        3,
		LeaseID:        42,
	}

	if err := printMVCCKeyValue(mkv); err != nil {
		t.Fatalf("printMVCCKeyValue error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "Key:") {
		t.Fatalf("expected Key: in output, got: %q", out)
	}
	if !strings.Contains(out, "test-value") {
		t.Fatalf("expected test-value in output, got: %q", out)
	}
	if !strings.Contains(out, "LeaseID:") {
		t.Fatalf("expected LeaseID: in output, got: %q", out)
	}
}

func TestPrintMVCCKeyValueJSON(t *testing.T) {
	var buf bytes.Buffer
	oldWriter := outputWriter
	outputWriter = &buf
	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() {
		outputWriter = oldWriter
		outputFormat = oldFormat
	})

	mkv := &transport.MVCCKeyValue{
		Key:            []byte("test-key"),
		Value:          []byte("test-value"),
		CreateRevision: 1,
		ModRevision:    5,
		Version:        3,
	}

	if err := printMVCCKeyValue(mkv); err != nil {
		t.Fatalf("printMVCCKeyValue error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, `"create_revision"`) {
		t.Fatalf("expected JSON create_revision field, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// sortOrderMap and sortTargetMap direct tests
// ---------------------------------------------------------------------------

func TestSortOrderMapEntries(t *testing.T) {
	expected := map[string]transport.SortOrder{
		"":     transport.SortNone,
		"asc":  transport.SortAscend,
		"desc": transport.SortDescend,
	}

	for k, want := range expected {
		got, ok := sortOrderMap[k]
		if !ok {
			t.Errorf("sortOrderMap[%q] not found", k)
			continue
		}
		if got != want {
			t.Errorf("sortOrderMap[%q] = %v, want %v", k, got, want)
		}
	}
}

func TestSortTargetMapEntries(t *testing.T) {
	expected := map[string]transport.SortTarget{
		"":        transport.SortByKey,
		"key":     transport.SortByKey,
		"value":   transport.SortByValue,
		"version": transport.SortByVersion,
		"create":  transport.SortByCreateRevision,
		"mod":     transport.SortByModRevision,
	}

	for k, want := range expected {
		got, ok := sortTargetMap[k]
		if !ok {
			t.Errorf("sortTargetMap[%q] not found", k)
			continue
		}
		if got != want {
			t.Errorf("sortTargetMap[%q] = %v, want %v", k, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Lease YAML output test
// ---------------------------------------------------------------------------

func TestLeaseListYAML(t *testing.T) {
	mvccSvc := newMockMVCCService()
	leaseSvc := &mockLeaseService{}
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), leaseSvc, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "--output", "yaml", "lease", "list")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "id:") {
		t.Fatalf("expected YAML id field, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// KV revision YAML output test
// ---------------------------------------------------------------------------

func TestKVRevisionYAML(t *testing.T) {
	mvccSvc := newMockMVCCService()
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "--output", "yaml", "kv", "revision")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "revision:") {
		t.Fatalf("expected YAML revision field, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// KV range YAML output test
// ---------------------------------------------------------------------------

func TestKVRangeYAML(t *testing.T) {
	mvccSvc := newMockMVCCService()
	mvccSvc.store["y"] = []byte("val-y")
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "--output", "yaml", "kv", "range", "a", "z")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "kvs:") {
		t.Fatalf("expected YAML kvs field, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// Range with single key (no end) test
// ---------------------------------------------------------------------------

func TestKVRangeSingleKey(t *testing.T) {
	mvccSvc := newMockMVCCService()
	mvccSvc.store["single"] = []byte("val")
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "kv", "range", "single")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "KEY") {
		t.Fatalf("expected KEY table header, got: %q", out)
	}
}

// RestoreToRevision satisfies service.MVCCKVServicer, delegating to
// restoreFunc when the test has configured one.
func (m *mockMVCCService) RestoreToRevision(ctx context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
	if m.restoreFunc != nil {
		return m.restoreFunc(ctx, req)
	}
	return nil, nil
}
