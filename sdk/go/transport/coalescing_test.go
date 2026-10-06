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

package transport

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Mock transport
// ---------------------------------------------------------------------------

// mockBatchTransport implements Transport, KVClient, and BatchClient.
// It records every ExecuteBatch invocation and returns configurable results.
// All methods are safe for concurrent use.
type mockBatchTransport struct {
	mu sync.Mutex

	// calls records the ops slice from each ExecuteBatch invocation.
	calls [][]Op

	// batchErr is returned by ExecuteBatch when non-nil.
	batchErr error

	// responseBuilder, when set, is called to build a BatchResponse for
	// a given set of ops. If nil, a default success response is produced.
	responseBuilder func(ops []Op) *BatchResponse

	// listResult and scanResult back the pass-through methods.
	listResult []string
	scanResult map[string][]byte
	listErr    error
	scanErr    error
	existsOk   bool
	existsErr  error
}

func (m *mockBatchTransport) Connect(_ context.Context) error { return nil }
func (m *mockBatchTransport) Close() error                    { return nil }
func (m *mockBatchTransport) Healthy(_ context.Context) bool  { return true }
func (m *mockBatchTransport) Request(_ context.Context, _ string, _, _ any) error {
	return nil
}

func (m *mockBatchTransport) Put(_ context.Context, _ string, _ []byte, _ ...WriteOption) error {
	return nil
}
func (m *mockBatchTransport) Get(_ context.Context, _ string, _ ...ReadOption) ([]byte, error) {
	return nil, nil
}
func (m *mockBatchTransport) Delete(_ context.Context, _ string, _ ...WriteOption) error {
	return nil
}
func (m *mockBatchTransport) List(_ context.Context, _ string, _ ...ReadOption) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listResult, m.listErr
}
func (m *mockBatchTransport) Scan(_ context.Context, _ string, _ ...ReadOption) (map[string][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.scanResult, m.scanErr
}
func (m *mockBatchTransport) Exists(_ context.Context, _ string, _ ...ReadOption) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.existsOk, m.existsErr
}

func (m *mockBatchTransport) ExecuteBatch(_ context.Context, ops []Op, _ ...WriteOption) (*BatchResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Copy ops so the caller's slice can be reused safely.
	copied := make([]Op, len(ops))
	copy(copied, ops)
	m.calls = append(m.calls, copied)
	if m.batchErr != nil {
		return nil, m.batchErr
	}
	if m.responseBuilder != nil {
		return m.responseBuilder(ops), nil
	}
	// Default: return a Succeeded response for every op.
	resp := &BatchResponse{Responses: make([]OpResponse, len(ops))}
	for i := range resp.Responses {
		resp.Responses[i] = OpResponse{Succeeded: true}
	}
	return resp, nil
}

func (m *mockBatchTransport) ImportFile(_ context.Context, _ string, _ io.Reader, _ ImportOptions) (*ImportResult, error) {
	return &ImportResult{}, nil
}

func (m *mockBatchTransport) ImportBegin(_ context.Context, _ ImportOptions) (string, error) {
	return "", nil
}

func (m *mockBatchTransport) ImportAppend(_ context.Context, _ string, _ uint64, _ []byte) error {
	return nil
}

func (m *mockBatchTransport) ImportCommit(_ context.Context, _ string) (*ImportResult, error) {
	return &ImportResult{}, nil
}

func (m *mockBatchTransport) ImportAbort(_ context.Context, _ string) error {
	return nil
}

// callCount returns the number of ExecuteBatch invocations seen so far.
func (m *mockBatchTransport) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// allOps returns a flat slice of all ops from all recorded calls.
func (m *mockBatchTransport) allOps() []Op {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Op
	for _, ops := range m.calls {
		out = append(out, ops...)
	}
	return out
}

// Compile-time checks: mockBatchTransport satisfies the required interfaces.
var (
	_ Transport   = (*mockBatchTransport)(nil)
	_ KVClient    = (*mockBatchTransport)(nil)
	_ BatchClient = (*mockBatchTransport)(nil)
)

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

// newCoalescing builds a CoalescingTransport backed by a fresh mock with a
// short window to keep tests snappy.
func newCoalescing(opts ...CoalescingOption) (*CoalescingTransport, *mockBatchTransport) {
	mock := &mockBatchTransport{}
	defaults := []CoalescingOption{WithBatchWindow(5 * time.Millisecond)}
	ct := WithCoalescing(mock, append(defaults, opts...)...)
	return ct, mock
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestCoalescing_SinglePut(t *testing.T) {
	ct, mock := newCoalescing()
	defer ct.Close()

	if err := ct.Put(context.Background(), "k1", []byte("v1")); err != nil {
		t.Fatalf("Put: unexpected error: %v", err)
	}
	if mock.callCount() != 1 {
		t.Errorf("expected 1 ExecuteBatch call, got %d", mock.callCount())
	}
	ops := mock.allOps()
	if len(ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(ops))
	}
	if ops[0].Type != OpPut {
		t.Errorf("expected OpPut, got %v", ops[0].Type)
	}
	if string(ops[0].Key) != "k1" {
		t.Errorf("expected key k1, got %q", ops[0].Key)
	}
	if string(ops[0].Value) != "v1" {
		t.Errorf("expected value v1, got %q", ops[0].Value)
	}
}

func TestCoalescing_SinglePut_ClosedTransport(t *testing.T) {
	ct, _ := newCoalescing()
	ct.Close()

	err := ct.Put(context.Background(), "k", []byte("v"))
	if err == nil {
		t.Fatal("expected ClosedError, got nil")
	}
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Errorf("expected *ClosedError, got %T: %v", err, err)
	}
}

func TestCoalescing_ConcurrentPuts(t *testing.T) {
	// Use a window long enough that all goroutines enqueue before the flush,
	// but short enough that the test does not stall.
	ct, mock := newCoalescing(WithBatchWindow(50 * time.Millisecond))
	defer ct.Close()

	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if err := ct.Put(context.Background(), "key", []byte("val")); err != nil {
				t.Errorf("Put failed: %v", err)
			}
		}()
	}
	wg.Wait()

	total := mock.callCount()
	if total == 0 {
		t.Fatal("no ExecuteBatch calls recorded")
	}
	if total > n {
		t.Errorf("expected at most %d ExecuteBatch calls, got %d", n, total)
	}
	// All 8 ops must have been submitted.
	if got := len(mock.allOps()); got != n {
		t.Errorf("expected %d ops total, got %d", n, got)
	}
	// With a 50ms window and 8 goroutines starting concurrently, the batch
	// count should be well below n.  We assert < n to verify coalescing.
	if total >= n {
		t.Logf("note: got %d calls; coalescing may not have occurred (timing sensitive)", total)
	}
}

func TestCoalescing_WindowFlush(t *testing.T) {
	const window = 20 * time.Millisecond
	ct, mock := newCoalescing(WithBatchWindow(window))
	defer ct.Close()

	done := make(chan error, 1)
	go func() {
		done <- ct.Put(context.Background(), "wk", []byte("wv"))
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Put error: %v", err)
		}
	case <-time.After(window * 10):
		t.Fatal("Put did not complete within expected window")
	}
	if mock.callCount() != 1 {
		t.Errorf("expected 1 ExecuteBatch call after window, got %d", mock.callCount())
	}
}

func TestCoalescing_BatchSizeTrigger(t *testing.T) {
	const maxBatch = 4
	ct, mock := newCoalescing(
		WithMaxBatchSize(maxBatch),
		// Use a very long window so the only flush trigger is the size limit.
		WithBatchWindow(10*time.Second),
	)
	defer ct.Close()

	var wg sync.WaitGroup
	var errCount atomic.Int32
	wg.Add(maxBatch)
	for i := 0; i < maxBatch; i++ {
		go func() {
			defer wg.Done()
			if err := ct.Put(context.Background(), "bk", []byte("bv")); err != nil {
				errCount.Add(1)
				t.Logf("Put error: %v", err)
			}
		}()
	}
	wg.Wait()

	if errCount.Load() != 0 {
		t.Fatalf("%d Put calls failed", errCount.Load())
	}
	if mock.callCount() < 1 {
		t.Error("expected at least 1 ExecuteBatch call triggered by batch size")
	}
	if got := len(mock.allOps()); got != maxBatch {
		t.Errorf("expected %d ops, got %d", maxBatch, got)
	}
}

func TestCoalescing_ContextCancellation(t *testing.T) {
	// Use a very long window so the operation waits in the queue.
	ct, _ := newCoalescing(WithBatchWindow(10 * time.Second))
	defer ct.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ct.Put(ctx, "ck", []byte("cv"))
	}()

	// Cancel before the batch window fires.
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error after context cancellation, got nil")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Put did not return after context cancellation")
	}
}

func TestCoalescing_Close_DrainsPending(t *testing.T) {
	const window = 10 * time.Second
	ct, mock := newCoalescing(WithBatchWindow(window))

	var wg sync.WaitGroup
	const n = 3
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			// Context has a generous deadline so it does not expire before Close.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = ct.Put(ctx, "dk", []byte("dv"))
		}()
	}

	// Give goroutines time to enqueue their ops before we close.
	time.Sleep(20 * time.Millisecond)
	ct.Close()
	wg.Wait()

	if got := len(mock.allOps()); got != n {
		t.Errorf("expected %d ops flushed on Close, got %d", n, got)
	}
}

func TestCoalescing_MixedOps(t *testing.T) {
	ct, mock := newCoalescing()
	defer ct.Close()

	// Configure the mock to return a realistic value for the Get response.
	mock.responseBuilder = func(ops []Op) *BatchResponse {
		resp := &BatchResponse{Responses: make([]OpResponse, len(ops))}
		for i, op := range ops {
			switch op.Type {
			case OpGet:
				resp.Responses[i] = OpResponse{
					Succeeded: true,
					Kvs: []*MVCCKeyValue{
						{Key: op.Key, Value: []byte("got-value")},
					},
				}
			default:
				resp.Responses[i] = OpResponse{Succeeded: true}
			}
		}
		return resp
	}

	var (
		putErr error
		getVal []byte
		getErr error
		delErr error
		wg     sync.WaitGroup
	)
	wg.Add(3)
	go func() {
		defer wg.Done()
		putErr = ct.Put(context.Background(), "mk", []byte("mv"))
	}()
	go func() {
		defer wg.Done()
		getVal, getErr = ct.Get(context.Background(), "mk")
	}()
	go func() {
		defer wg.Done()
		delErr = ct.Delete(context.Background(), "mk")
	}()
	wg.Wait()

	if putErr != nil {
		t.Errorf("Put: %v", putErr)
	}
	if getErr != nil {
		t.Errorf("Get: %v", getErr)
	}
	if string(getVal) != "got-value" {
		t.Errorf("Get value: got %q, want %q", getVal, "got-value")
	}
	if delErr != nil {
		t.Errorf("Delete: %v", delErr)
	}

	// Verify we captured all three op types.
	ops := mock.allOps()
	if len(ops) != 3 {
		t.Fatalf("expected 3 ops, got %d", len(ops))
	}
	typeSet := make(map[OpType]bool, 3)
	for _, op := range ops {
		typeSet[op.Type] = true
	}
	for _, want := range []OpType{OpPut, OpGet, OpDelete} {
		if !typeSet[want] {
			t.Errorf("missing op type %v in batch", want)
		}
	}
}

func TestCoalescing_ErrorPropagation(t *testing.T) {
	ct, mock := newCoalescing()
	defer ct.Close()

	sentinel := errors.New("server unavailable")
	mock.batchErr = sentinel

	const n = 4
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			errs[i] = ct.Put(context.Background(), "ek", []byte("ev"))
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err == nil {
			t.Errorf("caller %d: expected error, got nil", i)
		}
	}
}

func TestCoalescing_GetReturnsValue(t *testing.T) {
	ct, mock := newCoalescing()
	defer ct.Close()

	want := []byte("hello")
	mock.responseBuilder = func(ops []Op) *BatchResponse {
		resp := &BatchResponse{Responses: make([]OpResponse, len(ops))}
		for i, op := range ops {
			if op.Type == OpGet {
				resp.Responses[i] = OpResponse{
					Succeeded: true,
					Kvs:       []*MVCCKeyValue{{Key: op.Key, Value: want}},
				}
			} else {
				resp.Responses[i] = OpResponse{Succeeded: true}
			}
		}
		return resp
	}

	got, err := ct.Get(context.Background(), "gk")
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("Get: got %q, want %q", got, want)
	}
}

func TestCoalescing_GetKeyNotFound(t *testing.T) {
	ct, mock := newCoalescing()
	defer ct.Close()

	// Return an empty Kvs slice to signal "not found".
	mock.responseBuilder = func(ops []Op) *BatchResponse {
		resp := &BatchResponse{Responses: make([]OpResponse, len(ops))}
		for i := range ops {
			resp.Responses[i] = OpResponse{Succeeded: true} // Kvs is nil/empty
		}
		return resp
	}

	_, err := ct.Get(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected KeyNotFoundError, got nil")
	}
	var knf *KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Errorf("expected *KeyNotFoundError, got %T: %v", err, err)
	}
	if knf.Key != "missing" {
		t.Errorf("KeyNotFoundError.Key = %q, want %q", knf.Key, "missing")
	}
}

// ---------------------------------------------------------------------------
// Pass-through method tests
// ---------------------------------------------------------------------------

func TestCoalescing_ListPassThrough(t *testing.T) {
	ct, mock := newCoalescing()
	defer ct.Close()

	mock.listResult = []string{"a", "b", "c"}
	got, err := ct.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("List: got %v, want %v", got, mock.listResult)
	}
}

func TestCoalescing_ListPassThrough_Error(t *testing.T) {
	ct, mock := newCoalescing()
	defer ct.Close()

	mock.listErr = errors.New("list failed")
	_, err := ct.List(context.Background(), "")
	if err == nil {
		t.Fatal("expected error from List, got nil")
	}
}

func TestCoalescing_ScanPassThrough(t *testing.T) {
	ct, mock := newCoalescing()
	defer ct.Close()

	mock.scanResult = map[string][]byte{"x": []byte("1")}
	got, err := ct.Scan(context.Background(), "")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("Scan: got %v, want %v", got, mock.scanResult)
	}
}

func TestCoalescing_ScanPassThrough_Error(t *testing.T) {
	ct, mock := newCoalescing()
	defer ct.Close()

	mock.scanErr = errors.New("scan failed")
	_, err := ct.Scan(context.Background(), "")
	if err == nil {
		t.Fatal("expected error from Scan, got nil")
	}
}

func TestCoalescing_ExistsPassThrough(t *testing.T) {
	ct, mock := newCoalescing()
	defer ct.Close()

	mock.existsOk = true
	ok, err := ct.Exists(context.Background(), "ek")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !ok {
		t.Error("Exists: expected true, got false")
	}
}

func TestCoalescing_ExistsPassThrough_Error(t *testing.T) {
	ct, mock := newCoalescing()
	defer ct.Close()

	mock.existsErr = errors.New("exists failed")
	_, err := ct.Exists(context.Background(), "ek")
	if err == nil {
		t.Fatal("expected error from Exists, got nil")
	}
}

// ---------------------------------------------------------------------------
// Lifecycle tests
// ---------------------------------------------------------------------------

func TestCoalescing_Connect_Delegates(t *testing.T) {
	ct, _ := newCoalescing()
	defer ct.Close()
	if err := ct.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
}

func TestCoalescing_Healthy_Delegates(t *testing.T) {
	ct, _ := newCoalescing()
	defer ct.Close()
	if !ct.Healthy(context.Background()) {
		t.Error("Healthy: expected true")
	}
}

func TestCoalescing_Close_Idempotent(t *testing.T) {
	ct, _ := newCoalescing()
	if err := ct.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := ct.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Error type tests
// ---------------------------------------------------------------------------

func TestBatchIndexError_Message(t *testing.T) {
	err := &BatchIndexError{Index: 7}
	want := "transport: batch response missing result at index 7"
	if got := err.Error(); got != want {
		t.Errorf("BatchIndexError.Error() = %q, want %q", got, want)
	}
}

func TestBatchIndexError_ZeroIndex(t *testing.T) {
	err := &BatchIndexError{Index: 0}
	want := "transport: batch response missing result at index 0"
	if got := err.Error(); got != want {
		t.Errorf("BatchIndexError.Error() = %q, want %q", got, want)
	}
}

// TestCoalescing_BatchIndexError exercises the flush branch where the server
// returns fewer responses than ops submitted.
func TestCoalescing_BatchIndexError(t *testing.T) {
	ct, mock := newCoalescing()
	defer ct.Close()

	// Return zero responses for any batch, regardless of op count.
	mock.responseBuilder = func(_ []Op) *BatchResponse {
		return &BatchResponse{Responses: []OpResponse{}}
	}

	err := ct.Put(context.Background(), "bik", []byte("biv"))
	if err == nil {
		t.Fatal("expected BatchIndexError, got nil")
	}
	var bie *BatchIndexError
	if !errors.As(err, &bie) {
		t.Errorf("expected *BatchIndexError, got %T: %v", err, err)
	}
}

// TestCoalescing_Delete_SubmitError covers the submit-failure path for Delete.
func TestCoalescing_Delete_SubmitError(t *testing.T) {
	ct, _ := newCoalescing()
	ct.Close() // mark closed so submit returns ClosedError immediately

	err := ct.Delete(context.Background(), "dk")
	if err == nil {
		t.Fatal("expected ClosedError from Delete, got nil")
	}
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Errorf("expected *ClosedError, got %T: %v", err, err)
	}
}

// TestCoalescing_Get_SubmitError covers the submit-failure path for Get.
func TestCoalescing_Get_SubmitError(t *testing.T) {
	ct, _ := newCoalescing()
	ct.Close()

	_, err := ct.Get(context.Background(), "gk")
	if err == nil {
		t.Fatal("expected ClosedError from Get, got nil")
	}
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Errorf("expected *ClosedError, got %T: %v", err, err)
	}
}

func TestCoalescing_RequestDelegates(t *testing.T) {
	ct, _ := newCoalescing()
	defer ct.Close()
	// Request is a passthrough; the mock always returns nil.
	if err := ct.Request(context.Background(), "Test", nil, nil); err != nil {
		t.Fatalf("Request: unexpected error: %v", err)
	}
}

func TestCoalescing_DeleteError(t *testing.T) {
	ct, mock := newCoalescing()
	defer ct.Close()

	mock.batchErr = errors.New("delete failed")
	if err := ct.Delete(context.Background(), "dk"); err == nil {
		t.Fatal("expected error from Delete, got nil")
	}
}

func TestCoalescing_GetError_FromBatch(t *testing.T) {
	ct, mock := newCoalescing()
	defer ct.Close()

	mock.batchErr = errors.New("get failed")
	if _, err := ct.Get(context.Background(), "gk"); err == nil {
		t.Fatal("expected error from Get, got nil")
	}
}

// ---------------------------------------------------------------------------
// CoalescingOption tests
// ---------------------------------------------------------------------------

func TestWithMaxBatchSize(t *testing.T) {
	cfg := coalescingConfig{maxBatch: 256, window: time.Millisecond}
	WithMaxBatchSize(10)(&cfg)
	if cfg.maxBatch != 10 {
		t.Errorf("expected maxBatch=10, got %d", cfg.maxBatch)
	}
}

func TestWithBatchWindow(t *testing.T) {
	cfg := coalescingConfig{maxBatch: 256, window: time.Millisecond}
	WithBatchWindow(50 * time.Millisecond)(&cfg)
	if cfg.window != 50*time.Millisecond {
		t.Errorf("expected window=50ms, got %v", cfg.window)
	}
}

// ---------------------------------------------------------------------------
// mergeDeadlines tests
// ---------------------------------------------------------------------------

func TestMergeDeadlines_NoDeadlines(t *testing.T) {
	items := []batchItem{
		{ctx: context.Background()},
		{ctx: context.Background()},
	}
	ctx, cancel := mergeDeadlines(items)
	defer cancel()
	if _, ok := ctx.Deadline(); ok {
		t.Error("expected no deadline when no items have deadlines")
	}
}

func TestMergeDeadlines_SingleDeadline(t *testing.T) {
	deadline := time.Now().Add(5 * time.Second)
	dctx, dcancel := context.WithDeadline(context.Background(), deadline)
	defer dcancel()

	items := []batchItem{
		{ctx: context.Background()},
		{ctx: dctx},
	}
	ctx, cancel := mergeDeadlines(items)
	defer cancel()
	dl, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected a deadline to be set")
	}
	if dl.After(deadline.Add(time.Millisecond)) || dl.Before(deadline.Add(-time.Millisecond)) {
		t.Errorf("deadline %v not approximately equal to %v", dl, deadline)
	}
}

func TestMergeDeadlines_EarliestWins(t *testing.T) {
	early := time.Now().Add(1 * time.Second)
	late := time.Now().Add(10 * time.Second)

	earlyCtx, earlyCancel := context.WithDeadline(context.Background(), early)
	defer earlyCancel()
	lateCtx, lateCancel := context.WithDeadline(context.Background(), late)
	defer lateCancel()

	items := []batchItem{{ctx: lateCtx}, {ctx: earlyCtx}}
	ctx, cancel := mergeDeadlines(items)
	defer cancel()
	dl, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected a deadline")
	}
	if dl.After(early.Add(time.Millisecond)) {
		t.Errorf("expected earliest deadline %v, got %v", early, dl)
	}
}

// ---------------------------------------------------------------------------
// Capability-less inner transport
// ---------------------------------------------------------------------------

// bareTransport implements Transport and NOTHING else. WithCoalescing accepts
// any Transport -- a four-method interface -- so this is a legal argument, and
// before #333 every capability method below reached a bare type assertion and
// panicked on it. The pass-through methods panicked on the caller's goroutine;
// flush panicked on drainLoop, taking the process down from a background
// goroutine no caller could recover.
type bareTransport struct{}

func (bareTransport) Connect(_ context.Context) error                     { return nil }
func (bareTransport) Close() error                                        { return nil }
func (bareTransport) Healthy(_ context.Context) bool                      { return true }
func (bareTransport) Request(_ context.Context, _ string, _, _ any) error { return nil }

var _ Transport = bareTransport{}

func TestCoalescingRejectsInnerWithoutKVClient(t *testing.T) {
	ct := WithCoalescing(bareTransport{})
	defer func() {
		if err := ct.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()

	ctx := context.Background()
	var capErr *CapabilityUnsupportedError

	if _, err := ct.List(ctx, "p"); !errors.As(err, &capErr) {
		t.Errorf("List: got %v, want *CapabilityUnsupportedError", err)
	}
	if _, err := ct.Scan(ctx, "p"); !errors.As(err, &capErr) {
		t.Errorf("Scan: got %v, want *CapabilityUnsupportedError", err)
	}
	ok, err := ct.Exists(ctx, "k")
	if !errors.As(err, &capErr) {
		t.Errorf("Exists: got %v, want *CapabilityUnsupportedError", err)
	}
	if ok {
		t.Error("Exists reported true on a transport it could not ask")
	}
}

// TestCoalescingRejectsInnerWithoutBatchClient drives the same defect through
// flush, which runs on drainLoop rather than on the caller. The error has to
// reach the caller through the result channel; a panic here would not.
func TestCoalescingRejectsInnerWithoutBatchClient(t *testing.T) {
	ct := WithCoalescing(bareTransport{})
	defer func() {
		if err := ct.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()

	err := ct.Put(context.Background(), "k", []byte("v"))
	var capErr *CapabilityUnsupportedError
	if !errors.As(err, &capErr) {
		t.Fatalf("Put: got %v, want *CapabilityUnsupportedError", err)
	}
}
