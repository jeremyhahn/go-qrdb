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

//go:build !integration

package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// WatchBinding constructor tests
// ---------------------------------------------------------------------------

func TestNewWatchBinding_Constructor(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	b := NewWatchBinding(log, conn)
	if b == nil {
		t.Fatal("expected non-nil WatchBinding")
	}
}

func TestNewWatchBindingWithEmitter_Constructor(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	emit := func(_ context.Context, _ string, _ ...interface{}) {}
	b := NewWatchBindingWithEmitter(log, conn, emit)
	if b == nil {
		t.Fatal("expected non-nil WatchBinding from WithEmitter constructor")
	}
}

// ---------------------------------------------------------------------------
// CancelWatch tests
// ---------------------------------------------------------------------------

func TestCancelWatch_UnknownID_NoOp(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	b := NewWatchBindingWithEmitter(log, conn, func(_ context.Context, _ string, _ ...interface{}) {})

	// CancelWatch on a non-existent ID should be a no-op without panic.
	b.CancelWatch(9999)
}

func TestCancelWatch_CancelsActiveWatch(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)

	var mu sync.Mutex
	var emitted []string

	emit := func(_ context.Context, name string, _ ...interface{}) {
		mu.Lock()
		emitted = append(emitted, name)
		mu.Unlock()
	}

	b := NewWatchBindingWithEmitter(log, conn, emit)

	// Manually insert an active watch entry.
	watchID := int64(42)
	canceled := make(chan struct{})
	cancelFn := func() { close(canceled) }

	b.mu.Lock()
	b.watches[watchID] = &activeWatch{cancel: cancelFn}
	b.mu.Unlock()

	b.CancelWatch(watchID)

	// Cancel function should have been called.
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("expected cancel function to be called within 1s")
	}

	// Watch should be removed from the map.
	b.mu.Lock()
	_, exists := b.watches[watchID]
	b.mu.Unlock()
	if exists {
		t.Error("expected watch to be removed from map after CancelWatch")
	}
}

func TestCancelWatch_DoesNotCancelAlreadyRemoved(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	b := NewWatchBindingWithEmitter(log, conn, func(_ context.Context, _ string, _ ...interface{}) {})

	// Call CancelWatch twice on same ID — second call should be a no-op.
	watchID := int64(7)
	var callCount atomic.Int32
	cancelFn := func() { callCount.Add(1) }

	b.mu.Lock()
	b.watches[watchID] = &activeWatch{cancel: cancelFn}
	b.mu.Unlock()

	b.CancelWatch(watchID)
	b.CancelWatch(watchID) // second call: ID no longer in map

	if callCount.Load() != 1 {
		t.Errorf("expected cancel to be called once, got %d", callCount.Load())
	}
}

// ---------------------------------------------------------------------------
// streamEvents tests
// ---------------------------------------------------------------------------

func TestStreamEvents_ClosedChannel_EmitsCancelEvent(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)

	var mu sync.Mutex
	var events []interface{}

	emit := func(_ context.Context, _ string, data ...interface{}) {
		mu.Lock()
		events = append(events, data...)
		mu.Unlock()
	}

	b := NewWatchBindingWithEmitter(log, conn, emit)
	b.conn.ctx = context.Background()

	watchID := int64(1)

	// Create a channel and close it immediately to simulate stream end.
	ch := make(chan transport.WatchResponse)
	close(ch)

	// Insert a watch entry so cleanup can find it.
	cancelCalled := make(chan struct{})
	b.mu.Lock()
	b.watches[watchID] = &activeWatch{cancel: func() { close(cancelCalled) }}
	b.mu.Unlock()

	// Run streamEvents synchronously in a goroutine and wait.
	done := make(chan struct{})
	go func() {
		b.streamEvents(watchID, ch)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("streamEvents did not complete within 5s")
	}

	// Should have emitted a final cancel event.
	mu.Lock()
	count := len(events)
	mu.Unlock()

	if count == 0 {
		t.Error("expected at least one event to be emitted (cancellation event)")
	}

	// Verify the last event is a cancellation response.
	mu.Lock()
	last := events[count-1]
	mu.Unlock()

	resp, ok := last.(wailsWatchResponse)
	if !ok {
		t.Fatalf("expected wailsWatchResponse, got %T", last)
	}
	if !resp.Canceled {
		t.Error("expected Canceled=true in final event")
	}
	if resp.WatchID != watchID {
		t.Errorf("expected WatchID=%d, got %d", watchID, resp.WatchID)
	}
}

func TestStreamEvents_ForwardsEvents(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)

	var mu sync.Mutex
	var events []wailsWatchResponse

	emit := func(_ context.Context, _ string, data ...interface{}) {
		mu.Lock()
		for _, d := range data {
			if wr, ok := d.(wailsWatchResponse); ok {
				events = append(events, wr)
			}
		}
		mu.Unlock()
	}

	b := NewWatchBindingWithEmitter(log, conn, emit)
	b.conn.ctx = context.Background()

	watchID := int64(5)
	ch := make(chan transport.WatchResponse, 2)

	// Send one event then close.
	ch <- transport.WatchResponse{
		WatchID: 0, // overridden by streamEvents
		Events: []*transport.WatchEvent{
			{
				Type: transport.EventPut,
				Kv: &transport.MVCCKeyValue{
					Key:   []byte("k"),
					Value: []byte("v"),
				},
			},
		},
	}
	close(ch)

	done := make(chan struct{})
	go func() {
		b.streamEvents(watchID, ch)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("streamEvents did not complete within 5s")
	}

	mu.Lock()
	count := len(events)
	mu.Unlock()

	// Should have received the forwarded event plus the cancellation event.
	if count < 2 {
		t.Fatalf("expected at least 2 emitted events, got %d", count)
	}

	// First event should carry the watch ID.
	mu.Lock()
	first := events[0]
	mu.Unlock()
	if first.WatchID != watchID {
		t.Errorf("expected WatchID=%d, got %d", watchID, first.WatchID)
	}
}

// ---------------------------------------------------------------------------
// convertKeyValue and convertWatchResponse tests
// ---------------------------------------------------------------------------

func TestConvertKeyValue_Nil(t *testing.T) {
	result := convertKeyValue(nil)
	if result != nil {
		t.Fatalf("expected nil for nil input, got %v", result)
	}
}

func TestConvertKeyValue_NonNil(t *testing.T) {
	kv := &transport.MVCCKeyValue{
		Key:            []byte("hello"),
		Value:          []byte("world"),
		CreateRevision: 1,
		ModRevision:    2,
		Version:        3,
		LeaseID:        4,
	}
	result := convertKeyValue(kv)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Key != "hello" {
		t.Errorf("Key = %q, want %q", result.Key, "hello")
	}
	if result.Value != "world" {
		t.Errorf("Value = %q, want %q", result.Value, "world")
	}
	if result.CreateRevision != 1 {
		t.Errorf("CreateRevision = %d, want 1", result.CreateRevision)
	}
	if result.ModRevision != 2 {
		t.Errorf("ModRevision = %d, want 2", result.ModRevision)
	}
	if result.Version != 3 {
		t.Errorf("Version = %d, want 3", result.Version)
	}
	if result.LeaseID != 4 {
		t.Errorf("LeaseID = %d, want 4", result.LeaseID)
	}
}

func TestConvertWatchResponse_Empty(t *testing.T) {
	resp := transport.WatchResponse{
		WatchID:  42,
		Revision: 7,
	}
	result := convertWatchResponse(resp)
	if result.WatchID != 42 {
		t.Errorf("WatchID = %d, want 42", result.WatchID)
	}
	if result.Revision != 7 {
		t.Errorf("Revision = %d, want 7", result.Revision)
	}
	if len(result.Events) != 0 {
		t.Errorf("expected 0 events, got %d", len(result.Events))
	}
}

func TestConvertWatchResponse_WithEvents(t *testing.T) {
	resp := transport.WatchResponse{
		WatchID: 1,
		Events: []*transport.WatchEvent{
			{
				Type: transport.EventPut,
				Kv: &transport.MVCCKeyValue{
					Key:   []byte("k1"),
					Value: []byte("v1"),
				},
				PrevKv: &transport.MVCCKeyValue{
					Key:   []byte("k1"),
					Value: []byte("old"),
				},
			},
			{
				Type: transport.EventDelete,
				Kv: &transport.MVCCKeyValue{
					Key: []byte("k2"),
				},
			},
		},
		Canceled:     true,
		CancelReason: "timeout",
	}
	result := convertWatchResponse(resp)
	if len(result.Events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(result.Events))
	}
	if result.Events[0].Kv.Key != "k1" {
		t.Errorf("Events[0].Kv.Key = %q, want %q", result.Events[0].Kv.Key, "k1")
	}
	if result.Events[0].Kv.Value != "v1" {
		t.Errorf("Events[0].Kv.Value = %q, want %q", result.Events[0].Kv.Value, "v1")
	}
	if result.Events[0].PrevKv == nil {
		t.Fatal("expected non-nil PrevKv for first event")
	}
	if result.Events[0].PrevKv.Value != "old" {
		t.Errorf("Events[0].PrevKv.Value = %q, want %q", result.Events[0].PrevKv.Value, "old")
	}
	if result.Events[1].PrevKv != nil {
		t.Error("expected nil PrevKv for second event")
	}
	if !result.Canceled {
		t.Error("expected Canceled=true")
	}
	if result.CancelReason != "timeout" {
		t.Errorf("CancelReason = %q, want %q", result.CancelReason, "timeout")
	}
}

func TestConvertWatchResponse_NilKvInEvent(t *testing.T) {
	resp := transport.WatchResponse{
		WatchID: 2,
		Events: []*transport.WatchEvent{
			{
				Type:   transport.EventPut,
				Kv:     nil,
				PrevKv: nil,
			},
		},
	}
	result := convertWatchResponse(resp)
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}
	if result.Events[0].Kv != nil {
		t.Error("expected nil Kv after conversion of nil input")
	}
	if result.Events[0].PrevKv != nil {
		t.Error("expected nil PrevKv after conversion of nil input")
	}
}
