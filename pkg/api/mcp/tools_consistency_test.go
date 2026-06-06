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

package mcp

import (
	"context"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// captureKVService records which service variant was invoked.
// ---------------------------------------------------------------------------

type captureKVService struct {
	mu sync.Mutex

	// called tracks the method name last invoked.
	called string

	// capturedMode is set when PutWithMode or DeleteWithMode is called.
	capturedMode writemode.Mode

	// capturedConsistency is set when a WithConsistency variant is called.
	capturedConsistency consistency.ReadOptions

	// store backs the plain Get/List/Scan for happy-path tests.
	store map[string][]byte
}

func newCaptureKVService() *captureKVService {
	return &captureKVService{store: make(map[string][]byte)}
}

func (m *captureKVService) record(method string) {
	m.mu.Lock()
	m.called = method
	m.mu.Unlock()
}

func (m *captureKVService) Put(_ context.Context, key string, value []byte) error {
	m.record("Put")
	m.mu.Lock()
	m.store[key] = value
	m.mu.Unlock()
	return nil
}

func (m *captureKVService) PutWithMode(_ context.Context, key string, value []byte, mode writemode.Mode) error {
	m.mu.Lock()
	m.called = "PutWithMode"
	m.capturedMode = mode
	m.store[key] = value
	m.mu.Unlock()
	return nil
}

func (m *captureKVService) Get(_ context.Context, key string) ([]byte, error) {
	m.record("Get")
	m.mu.Lock()
	v := m.store[key]
	m.mu.Unlock()
	return v, nil
}

func (m *captureKVService) GetWithConsistency(_ context.Context, key string, opts consistency.ReadOptions) ([]byte, error) {
	m.mu.Lock()
	m.called = "GetWithConsistency"
	m.capturedConsistency = opts
	v := m.store[key]
	m.mu.Unlock()
	return v, nil
}

func (m *captureKVService) Delete(_ context.Context, _ string) error {
	m.record("Delete")
	return nil
}

func (m *captureKVService) DeleteWithMode(_ context.Context, _ string, mode writemode.Mode) error {
	m.mu.Lock()
	m.called = "DeleteWithMode"
	m.capturedMode = mode
	m.mu.Unlock()
	return nil
}

func (m *captureKVService) List(_ context.Context, _ string) ([]string, error) {
	m.record("List")
	return nil, nil
}

func (m *captureKVService) ListWithConsistency(_ context.Context, _ string, opts consistency.ReadOptions) ([]string, error) {
	m.mu.Lock()
	m.called = "ListWithConsistency"
	m.capturedConsistency = opts
	m.mu.Unlock()
	return nil, nil
}

func (m *captureKVService) Scan(_ context.Context, _ string) (map[string][]byte, error) {
	m.record("Scan")
	return map[string][]byte{}, nil
}

func (m *captureKVService) ScanWithConsistency(_ context.Context, _ string, opts consistency.ReadOptions) (map[string][]byte, error) {
	m.mu.Lock()
	m.called = "ScanWithConsistency"
	m.capturedConsistency = opts
	m.mu.Unlock()
	return map[string][]byte{}, nil
}

// Stub methods to satisfy service.KVServicer.

func (m *captureKVService) PutEntity(_ context.Context, _ *service.PutEntityRequest) (*service.PutEntityResponse, error) {
	return nil, nil
}
func (m *captureKVService) GetEntity(_ context.Context, _ *service.GetEntityRequest) (*service.GetEntityResponse, error) {
	return nil, nil
}
func (m *captureKVService) DeleteEntity(_ context.Context, _ *service.DeleteEntityRequest) (*service.DeleteEntityResponse, error) {
	return nil, nil
}
func (m *captureKVService) ListEntities(_ context.Context, _ *service.ListEntitiesRequest) (*service.ListEntitiesResponse, error) {
	return nil, nil
}
func (m *captureKVService) GetStale(ctx context.Context, key string) ([]byte, error) {
	return m.Get(ctx, key)
}
func (m *captureKVService) ListStale(ctx context.Context, prefix string) ([]string, error) {
	return m.List(ctx, prefix)
}
func (m *captureKVService) ScanStale(ctx context.Context, prefix string) (map[string][]byte, error) {
	return m.Scan(ctx, prefix)
}
func (m *captureKVService) ExistsWithConsistency(ctx context.Context, key string, _ consistency.ReadOptions) (bool, error) {
	v, err := m.Get(ctx, key)
	if err != nil {
		return false, err
	}
	return v != nil, nil
}

var _ service.KVServicer = (*captureKVService)(nil)

// ---------------------------------------------------------------------------
// captureMVCCSvc records which MVCC service variant was invoked.
// ---------------------------------------------------------------------------

type captureMVCCSvc struct {
	captureKVService
	mu2 sync.Mutex

	calledMVCC        string
	capturedMVCCMode  writemode.Mode
	capturedMVCCROpts consistency.ReadOptions
}

func newCaptureMVCCSvc() *captureMVCCSvc {
	return &captureMVCCSvc{
		captureKVService: captureKVService{store: make(map[string][]byte)},
	}
}

func (m *captureMVCCSvc) PutWithLease(_ context.Context, key string, value []byte, leaseID int64) (*kv.KeyValue, error) {
	m.mu2.Lock()
	m.calledMVCC = "PutWithLease"
	m.mu2.Unlock()
	return &kv.KeyValue{Key: []byte(key), Value: value, LeaseID: leaseID, ModRevision: 1, Version: 1}, nil
}

func (m *captureMVCCSvc) PutWithLeaseAndMode(_ context.Context, key string, value []byte, leaseID int64, mode writemode.Mode) (*kv.KeyValue, error) {
	m.mu2.Lock()
	m.calledMVCC = "PutWithLeaseAndMode"
	m.capturedMVCCMode = mode
	m.mu2.Unlock()
	return &kv.KeyValue{Key: []byte(key), Value: value, LeaseID: leaseID, ModRevision: 1, Version: 1}, nil
}

func (m *captureMVCCSvc) GetRevision(_ context.Context, key string) (*kv.KeyValue, error) {
	m.mu2.Lock()
	m.calledMVCC = "GetRevision"
	m.mu2.Unlock()
	return &kv.KeyValue{Key: []byte(key), Value: []byte("v"), ModRevision: 5, Version: 1}, nil
}

func (m *captureMVCCSvc) GetRevisionWithConsistency(_ context.Context, key string, opts consistency.ReadOptions) (*kv.KeyValue, error) {
	m.mu2.Lock()
	m.calledMVCC = "GetRevisionWithConsistency"
	m.capturedMVCCROpts = opts
	m.mu2.Unlock()
	return &kv.KeyValue{Key: []byte(key), Value: []byte("v"), ModRevision: 5, Version: 1}, nil
}

func (m *captureMVCCSvc) GetAtRevision(_ context.Context, key string, revision int64) (*kv.KeyValue, error) {
	m.mu2.Lock()
	m.calledMVCC = "GetAtRevision"
	m.mu2.Unlock()
	return &kv.KeyValue{Key: []byte(key), Value: []byte("v"), ModRevision: revision, Version: 1}, nil
}

func (m *captureMVCCSvc) GetAtRevisionWithConsistency(_ context.Context, key string, revision int64, opts consistency.ReadOptions) (*kv.KeyValue, error) {
	m.mu2.Lock()
	m.calledMVCC = "GetAtRevisionWithConsistency"
	m.capturedMVCCROpts = opts
	m.mu2.Unlock()
	return &kv.KeyValue{Key: []byte(key), Value: []byte("v"), ModRevision: revision, Version: 1}, nil
}

func (m *captureMVCCSvc) Range(_ context.Context, start, _ []byte, _ ...service.RangeOption) ([]*kv.KeyValue, int64, error) {
	m.mu2.Lock()
	m.calledMVCC = "Range"
	m.mu2.Unlock()
	return []*kv.KeyValue{{Key: start, Value: []byte("v"), ModRevision: 1}}, 1, nil
}

func (m *captureMVCCSvc) RangeWithConsistency(_ context.Context, start, _ []byte, opts consistency.ReadOptions, _ ...service.RangeOption) ([]*kv.KeyValue, int64, error) {
	m.mu2.Lock()
	m.calledMVCC = "RangeWithConsistency"
	m.capturedMVCCROpts = opts
	m.mu2.Unlock()
	return []*kv.KeyValue{{Key: start, Value: []byte("v"), ModRevision: 1}}, 1, nil
}

func (m *captureMVCCSvc) Compact(_ context.Context, _ int64) error { return nil }

func (m *captureMVCCSvc) CurrentRevision(_ context.Context) (int64, error) { return 1, nil }

var _ service.MVCCKVServicer = (*captureMVCCSvc)(nil)

// ---------------------------------------------------------------------------
// readOptsFromArg
// ---------------------------------------------------------------------------

func TestReadOptsFromArg_Empty(t *testing.T) {
	r := makeReq(map[string]any{})
	opts, set, err := readOptsFromArg(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if set {
		t.Error("expected set=false for empty arg")
	}
	_ = opts
}

func TestReadOptsFromArg_Linearizable(t *testing.T) {
	r := makeReq(map[string]any{"consistency": "linearizable"})
	opts, set, err := readOptsFromArg(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !set {
		t.Fatal("expected set=true")
	}
	if opts.Consistency != consistency.Linearizable {
		t.Errorf("Consistency = %v, want Linearizable", opts.Consistency)
	}
	if opts.AllowStale {
		t.Error("AllowStale should be false for linearizable")
	}
}

func TestReadOptsFromArg_Eventual(t *testing.T) {
	r := makeReq(map[string]any{"consistency": "eventual"})
	opts, set, err := readOptsFromArg(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !set {
		t.Fatal("expected set=true")
	}
	if opts.Consistency != consistency.Eventual {
		t.Errorf("Consistency = %v, want Eventual", opts.Consistency)
	}
	if !opts.AllowStale {
		t.Error("AllowStale should be true for eventual")
	}
}

func TestReadOptsFromArg_Strong(t *testing.T) {
	r := makeReq(map[string]any{"consistency": "strong"})
	opts, set, err := readOptsFromArg(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !set {
		t.Fatal("expected set=true")
	}
	if opts.Consistency != consistency.Strong {
		t.Errorf("Consistency = %v, want Strong", opts.Consistency)
	}
}

func TestReadOptsFromArg_Invalid(t *testing.T) {
	r := makeReq(map[string]any{"consistency": "garbage"})
	_, set, err := readOptsFromArg(r)
	if err == nil {
		t.Fatal("expected error for invalid consistency")
	}
	if set {
		t.Error("expected set=false on error")
	}
}

// ---------------------------------------------------------------------------
// writeModeFromArg
// ---------------------------------------------------------------------------

func TestWriteModeFromArg_Empty(t *testing.T) {
	r := makeReq(map[string]any{})
	_, set, err := writeModeFromArg(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if set {
		t.Error("expected set=false for empty arg")
	}
}

func TestWriteModeFromArg_Sync(t *testing.T) {
	r := makeReq(map[string]any{"write_mode": "sync"})
	mode, set, err := writeModeFromArg(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !set {
		t.Fatal("expected set=true")
	}
	if mode != writemode.Sync {
		t.Errorf("mode = %v, want Sync", mode)
	}
}

func TestWriteModeFromArg_WALDurable(t *testing.T) {
	r := makeReq(map[string]any{"write_mode": "wal_durable"})
	mode, set, err := writeModeFromArg(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !set {
		t.Fatal("expected set=true")
	}
	if mode != writemode.WALDurable {
		t.Errorf("mode = %v, want WALDurable", mode)
	}
}

func TestWriteModeFromArg_Batched(t *testing.T) {
	r := makeReq(map[string]any{"write_mode": "batched"})
	mode, set, err := writeModeFromArg(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !set {
		t.Fatal("expected set=true")
	}
	if mode != writemode.Batched {
		t.Errorf("mode = %v, want Batched", mode)
	}
}

func TestWriteModeFromArg_Invalid(t *testing.T) {
	r := makeReq(map[string]any{"write_mode": "garbage"})
	_, set, err := writeModeFromArg(r)
	if err == nil {
		t.Fatal("expected error for invalid write_mode")
	}
	if set {
		t.Error("expected set=false on error")
	}
}

// ---------------------------------------------------------------------------
// handleGet: routes to plain vs. WithConsistency
// ---------------------------------------------------------------------------

func TestHandleGet_PlainWhenNoConsistency(t *testing.T) {
	svc := newCaptureKVService()
	_ = svc.Put(context.Background(), "k", []byte("v"))

	handler := handleGet(svc)
	_, err := handler(context.Background(), makeReq(map[string]any{"key": "k"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	svc.mu.Lock()
	called := svc.called
	svc.mu.Unlock()
	if called != "Get" {
		t.Errorf("called = %q, want Get", called)
	}
}

func TestHandleGet_WithConsistencyRoutes(t *testing.T) {
	svc := newCaptureKVService()
	handler := handleGet(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"key":         "k",
		"consistency": "strong",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %v", resultText(t, result))
	}
	svc.mu.Lock()
	called := svc.called
	ropts := svc.capturedConsistency
	svc.mu.Unlock()
	if called != "GetWithConsistency" {
		t.Errorf("called = %q, want GetWithConsistency", called)
	}
	if ropts.Consistency != consistency.Strong {
		t.Errorf("consistency = %v, want Strong", ropts.Consistency)
	}
}

func TestHandleGet_InvalidConsistencyReturnsError(t *testing.T) {
	svc := newCaptureKVService()
	handler := handleGet(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"key":         "k",
		"consistency": "nonsense",
	}))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true for invalid consistency")
	}
}

// ---------------------------------------------------------------------------
// handleList: routes to plain vs. WithConsistency
// ---------------------------------------------------------------------------

func TestHandleList_PlainWhenNoConsistency(t *testing.T) {
	svc := newCaptureKVService()
	handler := handleList(svc)

	if _, err := handler(context.Background(), makeReq(map[string]any{})); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	svc.mu.Lock()
	called := svc.called
	svc.mu.Unlock()
	if called != "List" {
		t.Errorf("called = %q, want List", called)
	}
}

func TestHandleList_WithConsistencyRoutes(t *testing.T) {
	svc := newCaptureKVService()
	handler := handleList(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"consistency": "eventual",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %v", resultText(t, result))
	}
	svc.mu.Lock()
	called := svc.called
	ropts := svc.capturedConsistency
	svc.mu.Unlock()
	if called != "ListWithConsistency" {
		t.Errorf("called = %q, want ListWithConsistency", called)
	}
	if ropts.Consistency != consistency.Eventual {
		t.Errorf("consistency = %v, want Eventual", ropts.Consistency)
	}
	if !ropts.AllowStale {
		t.Error("AllowStale should be true for eventual")
	}
}

func TestHandleList_InvalidConsistencyReturnsError(t *testing.T) {
	svc := newCaptureKVService()
	handler := handleList(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"consistency": "bad",
	}))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true for invalid consistency")
	}
}

// ---------------------------------------------------------------------------
// handleScan: routes to plain vs. WithConsistency
// ---------------------------------------------------------------------------

func TestHandleScan_PlainWhenNoConsistency(t *testing.T) {
	svc := newCaptureKVService()
	handler := handleScan(svc)

	if _, err := handler(context.Background(), makeReq(map[string]any{})); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	svc.mu.Lock()
	called := svc.called
	svc.mu.Unlock()
	if called != "Scan" {
		t.Errorf("called = %q, want Scan", called)
	}
}

func TestHandleScan_WithConsistencyRoutes(t *testing.T) {
	svc := newCaptureKVService()
	handler := handleScan(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"consistency": "linearizable",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %v", resultText(t, result))
	}
	svc.mu.Lock()
	called := svc.called
	svc.mu.Unlock()
	if called != "ScanWithConsistency" {
		t.Errorf("called = %q, want ScanWithConsistency", called)
	}
}

func TestHandleScan_InvalidConsistencyReturnsError(t *testing.T) {
	svc := newCaptureKVService()
	handler := handleScan(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"consistency": "bad_level",
	}))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true for invalid consistency")
	}
}

// ---------------------------------------------------------------------------
// handlePut: routes to plain vs. PutWithMode
// ---------------------------------------------------------------------------

func TestHandlePut_PlainWhenNoMode(t *testing.T) {
	svc := newCaptureKVService()
	handler := handlePut(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"key":   "k",
		"value": "v",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %v", resultText(t, result))
	}
	svc.mu.Lock()
	called := svc.called
	svc.mu.Unlock()
	if called != "Put" {
		t.Errorf("called = %q, want Put", called)
	}
}

func TestHandlePut_WithWriteModeRoutes(t *testing.T) {
	svc := newCaptureKVService()
	handler := handlePut(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"key":        "k",
		"value":      "v",
		"write_mode": "wal_durable",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %v", resultText(t, result))
	}
	svc.mu.Lock()
	called := svc.called
	mode := svc.capturedMode
	svc.mu.Unlock()
	if called != "PutWithMode" {
		t.Errorf("called = %q, want PutWithMode", called)
	}
	if mode != writemode.WALDurable {
		t.Errorf("mode = %v, want WALDurable", mode)
	}
}

func TestHandlePut_InvalidWriteModeReturnsError(t *testing.T) {
	svc := newCaptureKVService()
	handler := handlePut(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"key":        "k",
		"value":      "v",
		"write_mode": "invalid",
	}))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true for invalid write_mode")
	}
}

// ---------------------------------------------------------------------------
// handleDelete: routes to plain vs. DeleteWithMode
// ---------------------------------------------------------------------------

func TestHandleDelete_PlainWhenNoMode(t *testing.T) {
	svc := newCaptureKVService()
	handler := handleDelete(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{"key": "k"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %v", resultText(t, result))
	}
	svc.mu.Lock()
	called := svc.called
	svc.mu.Unlock()
	if called != "Delete" {
		t.Errorf("called = %q, want Delete", called)
	}
}

func TestHandleDelete_WithWriteModeRoutes(t *testing.T) {
	svc := newCaptureKVService()
	handler := handleDelete(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"key":        "k",
		"write_mode": "batched",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %v", resultText(t, result))
	}
	svc.mu.Lock()
	called := svc.called
	mode := svc.capturedMode
	svc.mu.Unlock()
	if called != "DeleteWithMode" {
		t.Errorf("called = %q, want DeleteWithMode", called)
	}
	if mode != writemode.Batched {
		t.Errorf("mode = %v, want Batched", mode)
	}
}

func TestHandleDelete_InvalidWriteModeReturnsError(t *testing.T) {
	svc := newCaptureKVService()
	handler := handleDelete(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"key":        "k",
		"write_mode": "unknown_mode",
	}))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true for invalid write_mode")
	}
}

// ---------------------------------------------------------------------------
// handleMVCCPutWithLease: routes to plain vs. PutWithLeaseAndMode
// ---------------------------------------------------------------------------

func TestHandleMVCCPutWithLease_PlainWhenNoMode(t *testing.T) {
	svc := newCaptureMVCCSvc()
	handler := handleMVCCPutWithLease(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"key":      "k",
		"value":    "v",
		"lease_id": float64(42),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %v", resultText(t, result))
	}
	svc.mu2.Lock()
	called := svc.calledMVCC
	svc.mu2.Unlock()
	if called != "PutWithLease" {
		t.Errorf("called = %q, want PutWithLease", called)
	}
}

func TestHandleMVCCPutWithLease_WithWriteModeRoutes(t *testing.T) {
	svc := newCaptureMVCCSvc()
	handler := handleMVCCPutWithLease(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"key":        "k",
		"value":      "v",
		"lease_id":   float64(42),
		"write_mode": "sync",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %v", resultText(t, result))
	}
	svc.mu2.Lock()
	called := svc.calledMVCC
	mode := svc.capturedMVCCMode
	svc.mu2.Unlock()
	if called != "PutWithLeaseAndMode" {
		t.Errorf("called = %q, want PutWithLeaseAndMode", called)
	}
	if mode != writemode.Sync {
		t.Errorf("mode = %v, want Sync", mode)
	}
}

func TestHandleMVCCPutWithLease_InvalidWriteModeReturnsError(t *testing.T) {
	svc := newCaptureMVCCSvc()
	handler := handleMVCCPutWithLease(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"key":        "k",
		"value":      "v",
		"lease_id":   float64(1),
		"write_mode": "bad",
	}))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true for invalid write_mode")
	}
}

// ---------------------------------------------------------------------------
// handleMVCCGetRevision: routes to plain vs. GetRevisionWithConsistency
// ---------------------------------------------------------------------------

func TestHandleMVCCGetRevision_PlainWhenNoConsistency(t *testing.T) {
	svc := newCaptureMVCCSvc()
	handler := handleMVCCGetRevision(svc)

	if _, err := handler(context.Background(), makeReq(map[string]any{"key": "k"})); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	svc.mu2.Lock()
	called := svc.calledMVCC
	svc.mu2.Unlock()
	if called != "GetRevision" {
		t.Errorf("called = %q, want GetRevision", called)
	}
}

func TestHandleMVCCGetRevision_WithConsistencyRoutes(t *testing.T) {
	svc := newCaptureMVCCSvc()
	handler := handleMVCCGetRevision(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"key":         "k",
		"consistency": "strong",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %v", resultText(t, result))
	}
	svc.mu2.Lock()
	called := svc.calledMVCC
	ropts := svc.capturedMVCCROpts
	svc.mu2.Unlock()
	if called != "GetRevisionWithConsistency" {
		t.Errorf("called = %q, want GetRevisionWithConsistency", called)
	}
	if ropts.Consistency != consistency.Strong {
		t.Errorf("consistency = %v, want Strong", ropts.Consistency)
	}
}

func TestHandleMVCCGetRevision_InvalidConsistencyReturnsError(t *testing.T) {
	svc := newCaptureMVCCSvc()
	handler := handleMVCCGetRevision(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"key":         "k",
		"consistency": "bad",
	}))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true for invalid consistency")
	}
}

// ---------------------------------------------------------------------------
// handleMVCCGetAtRevision: routes to plain vs. GetAtRevisionWithConsistency
// ---------------------------------------------------------------------------

func TestHandleMVCCGetAtRevision_PlainWhenNoConsistency(t *testing.T) {
	svc := newCaptureMVCCSvc()
	handler := handleMVCCGetAtRevision(svc)

	if _, err := handler(context.Background(), makeReq(map[string]any{
		"key":      "k",
		"revision": float64(3),
	})); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	svc.mu2.Lock()
	called := svc.calledMVCC
	svc.mu2.Unlock()
	if called != "GetAtRevision" {
		t.Errorf("called = %q, want GetAtRevision", called)
	}
}

func TestHandleMVCCGetAtRevision_WithConsistencyRoutes(t *testing.T) {
	svc := newCaptureMVCCSvc()
	handler := handleMVCCGetAtRevision(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"key":         "k",
		"revision":    float64(3),
		"consistency": "linearizable",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %v", resultText(t, result))
	}
	svc.mu2.Lock()
	called := svc.calledMVCC
	ropts := svc.capturedMVCCROpts
	svc.mu2.Unlock()
	if called != "GetAtRevisionWithConsistency" {
		t.Errorf("called = %q, want GetAtRevisionWithConsistency", called)
	}
	if ropts.Consistency != consistency.Linearizable {
		t.Errorf("consistency = %v, want Linearizable", ropts.Consistency)
	}
}

func TestHandleMVCCGetAtRevision_InvalidConsistencyReturnsError(t *testing.T) {
	svc := newCaptureMVCCSvc()
	handler := handleMVCCGetAtRevision(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"key":         "k",
		"revision":    float64(1),
		"consistency": "bad_one",
	}))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true for invalid consistency")
	}
}

// ---------------------------------------------------------------------------
// handleMVCCRange: routes to plain vs. RangeWithConsistency
// ---------------------------------------------------------------------------

func TestHandleMVCCRange_PlainWhenNoConsistency(t *testing.T) {
	svc := newCaptureMVCCSvc()
	handler := handleMVCCRange(svc)

	if _, err := handler(context.Background(), makeReq(map[string]any{
		"start": "a",
		"end":   "z",
	})); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	svc.mu2.Lock()
	called := svc.calledMVCC
	svc.mu2.Unlock()
	if called != "Range" {
		t.Errorf("called = %q, want Range", called)
	}
}

func TestHandleMVCCRange_WithConsistencyRoutes(t *testing.T) {
	svc := newCaptureMVCCSvc()
	handler := handleMVCCRange(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"start":       "a",
		"end":         "z",
		"consistency": "eventual",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %v", resultText(t, result))
	}
	svc.mu2.Lock()
	called := svc.calledMVCC
	ropts := svc.capturedMVCCROpts
	svc.mu2.Unlock()
	if called != "RangeWithConsistency" {
		t.Errorf("called = %q, want RangeWithConsistency", called)
	}
	if ropts.Consistency != consistency.Eventual {
		t.Errorf("consistency = %v, want Eventual", ropts.Consistency)
	}
	if !ropts.AllowStale {
		t.Error("AllowStale should be true for eventual")
	}
}

func TestHandleMVCCRange_InvalidConsistencyReturnsError(t *testing.T) {
	svc := newCaptureMVCCSvc()
	handler := handleMVCCRange(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"start":       "a",
		"end":         "z",
		"consistency": "invalid_level",
	}))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true for invalid consistency")
	}
}
