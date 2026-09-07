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

// Package transport provides client-side batch coalescing for the QRDB SDK.
// CoalescingTransport wraps any Transport and transparently coalesces
// concurrent Put, Get, and Delete calls into single ExecuteBatch RPCs,
// reducing round-trips under concurrent load while preserving per-call
// semantics for the caller.
package transport

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// Internal types
// ---------------------------------------------------------------------------

// batchItem is a single queued operation waiting for its batch to flush.
type batchItem struct {
	op       Op
	ctx      context.Context
	resultCh chan batchResult
}

// batchResult carries the per-operation response from a flushed batch.
type batchResult struct {
	resp OpResponse
	err  error
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// coalescingConfig holds tunable parameters for the coalescing transport.
type coalescingConfig struct {
	maxBatch int
	window   time.Duration
}

// CoalescingOption configures the CoalescingTransport.
type CoalescingOption func(*coalescingConfig)

// WithMaxBatchSize sets the maximum number of operations per batch.
// When the queue reaches this size the batch is flushed immediately,
// without waiting for the window to expire. Default: 256.
func WithMaxBatchSize(n int) CoalescingOption {
	return func(c *coalescingConfig) { c.maxBatch = n }
}

// WithBatchWindow sets the maximum time to wait before flushing a
// partial batch. Shorter windows reduce latency at the expense of
// larger batches. Default: 1ms.
func WithBatchWindow(d time.Duration) CoalescingOption {
	return func(c *coalescingConfig) { c.window = d }
}

// ---------------------------------------------------------------------------
// CoalescingTransport
// ---------------------------------------------------------------------------

// CoalescingTransport wraps a Transport and automatically coalesces
// concurrent Put, Get, and Delete calls into single ExecuteBatch RPCs.
// Callers see the standard KVClient interface; batching is transparent.
//
// Operations that cannot be expressed as batch Ops (List, Scan, Exists)
// pass through to the underlying transport without modification.
//
// The inner transport must implement both KVClient and BatchClient.
// Use WithCoalescing to construct an instance.
type CoalescingTransport struct {
	inner    Transport
	batchCh  chan batchItem
	maxBatch int
	window   time.Duration
	closed   atomic.Bool
	done     chan struct{}
	wg       sync.WaitGroup
}

// WithCoalescing wraps a Transport with automatic batch coalescing.
// The returned transport starts a single background goroutine that
// accumulates Put/Get/Delete calls and flushes them as Batch RPCs.
// Call Close to drain pending operations and shut down the background
// goroutine.
func WithCoalescing(inner Transport, opts ...CoalescingOption) *CoalescingTransport {
	cfg := coalescingConfig{
		maxBatch: 256,
		window:   time.Millisecond,
	}
	for _, o := range opts {
		o(&cfg)
	}
	c := &CoalescingTransport{
		inner:    inner,
		batchCh:  make(chan batchItem, cfg.maxBatch*2),
		maxBatch: cfg.maxBatch,
		window:   cfg.window,
		done:     make(chan struct{}),
	}
	c.wg.Add(1)
	go c.drainLoop()
	return c
}

// ---------------------------------------------------------------------------
// Background drain loop
// ---------------------------------------------------------------------------

// drainLoop accumulates batchItems from the channel and flushes them
// when either the window elapses or the pending slice reaches maxBatch.
// It exits cleanly when the done channel is closed, draining any
// remaining items first.
func (c *CoalescingTransport) drainLoop() {
	defer c.wg.Done()
	ticker := time.NewTicker(c.window)
	defer ticker.Stop()
	pending := make([]batchItem, 0, c.maxBatch)
	for {
		select {
		case item, ok := <-c.batchCh:
			if !ok {
				// Channel was closed externally; flush remaining work.
				if len(pending) > 0 {
					c.flush(pending)
				}
				return
			}
			pending = append(pending, item)
			if len(pending) >= c.maxBatch {
				c.flush(pending)
				pending = pending[:0]
			}
		case <-ticker.C:
			if len(pending) > 0 {
				c.flush(pending)
				pending = pending[:0]
			}
		case <-c.done:
			// Drain any items already queued before the shutdown signal.
			for {
				select {
				case item := <-c.batchCh:
					pending = append(pending, item)
				default:
					if len(pending) > 0 {
						c.flush(pending)
					}
					return
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Flush
// ---------------------------------------------------------------------------

// flush calls ExecuteBatch for the given items and delivers each
// per-item result to the caller's result channel. A single error from
// ExecuteBatch is fanned out to all waiting callers.
func (c *CoalescingTransport) flush(items []batchItem) {
	ops := make([]Op, len(items))
	for i := range items {
		ops[i] = items[i].op
	}
	ctx, cancel := mergeDeadlines(items)
	defer cancel()
	var (
		resp *BatchResponse
		err  error
	)
	// A bare assertion here panics in a background goroutine -- flush runs on
	// drainLoop, not on the caller -- if an embedder wraps a transport that
	// does not implement BatchClient. Reported as the capability error the SDK
	// already defines, and fanned out to every waiting caller below.
	if batchClient, ok := c.inner.(BatchClient); ok {
		resp, err = batchClient.ExecuteBatch(ctx, ops)
	} else {
		err = &CapabilityUnsupportedError{Message: "coalescing: inner transport is not a BatchClient"}
	}
	for i := range items {
		if err != nil {
			items[i].resultCh <- batchResult{err: err}
			continue
		}
		if i < len(resp.Responses) {
			items[i].resultCh <- batchResult{resp: resp.Responses[i]}
		} else {
			items[i].resultCh <- batchResult{err: &BatchIndexError{Index: i}}
		}
	}
}

// mergeDeadlines returns a context with the earliest deadline from all
// items. If no item carries a deadline the returned context is a fresh
// Background context. The caller must invoke the returned cancel function
// to release resources.
func mergeDeadlines(items []batchItem) (context.Context, context.CancelFunc) {
	var earliest time.Time
	found := false
	for _, item := range items {
		if dl, ok := item.ctx.Deadline(); ok {
			if !found || dl.Before(earliest) {
				earliest = dl
				found = true
			}
		}
	}
	if !found {
		return context.WithCancel(context.Background())
	}
	return context.WithDeadline(context.Background(), earliest)
}

// ---------------------------------------------------------------------------
// submit
// ---------------------------------------------------------------------------

// submit queues an operation and blocks until the batch containing it
// is flushed and the result is available, or until the context is
// canceled.
func (c *CoalescingTransport) submit(ctx context.Context, op Op) (batchResult, error) {
	if c.closed.Load() {
		return batchResult{}, &ClosedError{}
	}
	resultCh := make(chan batchResult, 1)
	item := batchItem{op: op, ctx: ctx, resultCh: resultCh}
	select {
	case c.batchCh <- item:
	case <-ctx.Done():
		return batchResult{}, ctx.Err()
	}
	select {
	case r := <-resultCh:
		return r, nil
	case <-ctx.Done():
		return batchResult{}, ctx.Err()
	}
}

// ---------------------------------------------------------------------------
// KVClient methods
// ---------------------------------------------------------------------------

// Put stores a key-value pair by coalescing the operation into the next
// outbound batch.
func (c *CoalescingTransport) Put(ctx context.Context, key string, value []byte, opts ...WriteOption) error {
	r, err := c.submit(ctx, Op{Type: OpPut, Key: []byte(key), Value: value})
	if err != nil {
		return err
	}
	return r.err
}

// Get retrieves a value by key by coalescing the operation into the next
// outbound batch. Returns KeyNotFoundError when the key is absent.
func (c *CoalescingTransport) Get(ctx context.Context, key string, opts ...ReadOption) ([]byte, error) {
	r, err := c.submit(ctx, Op{Type: OpGet, Key: []byte(key)})
	if err != nil {
		return nil, err
	}
	if r.err != nil {
		return nil, r.err
	}
	if len(r.resp.Kvs) == 0 {
		return nil, &KeyNotFoundError{Key: key}
	}
	return r.resp.Kvs[0].Value, nil
}

// Delete removes a key by coalescing the operation into the next
// outbound batch.
func (c *CoalescingTransport) Delete(ctx context.Context, key string, opts ...WriteOption) error {
	r, err := c.submit(ctx, Op{Type: OpDelete, Key: []byte(key)})
	if err != nil {
		return err
	}
	return r.err
}

// ---------------------------------------------------------------------------
// Pass-through KVClient methods
// ---------------------------------------------------------------------------

// List passes through to the inner transport because range scans cannot
// be expressed as a simple batch Op.
func (c *CoalescingTransport) List(ctx context.Context, prefix string, opts ...ReadOption) ([]string, error) {
	kv, ok := c.inner.(KVClient)
	if !ok {
		return nil, &CapabilityUnsupportedError{Message: "coalescing: inner transport is not a KVClient"}
	}
	return kv.List(ctx, prefix, opts...)
}

// Scan passes through to the inner transport because prefix scans cannot
// be expressed as a simple batch Op.
func (c *CoalescingTransport) Scan(ctx context.Context, prefix string, opts ...ReadOption) (map[string][]byte, error) {
	kv, ok := c.inner.(KVClient)
	if !ok {
		return nil, &CapabilityUnsupportedError{Message: "coalescing: inner transport is not a KVClient"}
	}
	return kv.Scan(ctx, prefix, opts...)
}

// Exists passes through to the inner transport.
func (c *CoalescingTransport) Exists(ctx context.Context, key string, opts ...ReadOption) (bool, error) {
	kv, ok := c.inner.(KVClient)
	if !ok {
		return false, &CapabilityUnsupportedError{Message: "coalescing: inner transport is not a KVClient"}
	}
	return kv.Exists(ctx, key, opts...)
}

// ---------------------------------------------------------------------------
// Transport lifecycle methods
// ---------------------------------------------------------------------------

// Connect delegates to the inner transport.
func (c *CoalescingTransport) Connect(ctx context.Context) error {
	return c.inner.Connect(ctx)
}

// Healthy delegates to the inner transport.
func (c *CoalescingTransport) Healthy(ctx context.Context) bool {
	return c.inner.Healthy(ctx)
}

// Request delegates to the inner transport.
func (c *CoalescingTransport) Request(ctx context.Context, method string, req, resp any) error {
	return c.inner.Request(ctx, method, req, resp)
}

// Close signals the background drain loop to stop, waits for all
// pending operations to flush, then closes the inner transport.
// Subsequent calls are no-ops.
func (c *CoalescingTransport) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(c.done)
	c.wg.Wait()
	return c.inner.Close()
}

// ---------------------------------------------------------------------------
// Compile-time interface assertion
// ---------------------------------------------------------------------------

var _ KVClient = (*CoalescingTransport)(nil)
