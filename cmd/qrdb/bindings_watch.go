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
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// EventEmitter is a function that emits an event to the frontend.
// In production this is wailsruntime.EventsEmit; in tests a no-op.
type EventEmitter func(ctx context.Context, eventName string, data ...interface{})

// wailsKeyValue mirrors transport.MVCCKeyValue with string fields so that
// Wails IPC serializes keys and values as plain text instead of base64.
type wailsKeyValue struct {
	Key            string `json:"key"`
	Value          string `json:"value"`
	CreateRevision int64  `json:"create_revision"`
	ModRevision    int64  `json:"mod_revision"`
	Version        int64  `json:"version"`
	LeaseID        int64  `json:"lease_id"`
}

// wailsWatchEvent mirrors transport.WatchEvent with wailsKeyValue fields.
type wailsWatchEvent struct {
	Type   transport.EventType `json:"type"`
	Kv     *wailsKeyValue      `json:"kv"`
	PrevKv *wailsKeyValue      `json:"prev_kv,omitempty"`
}

// wailsWatchResponse mirrors transport.WatchResponse with wailsWatchEvent fields.
type wailsWatchResponse struct {
	WatchID      int64              `json:"watch_id"`
	Events       []*wailsWatchEvent `json:"events"`
	Canceled     bool               `json:"canceled,omitempty"`
	CancelReason string             `json:"cancel_reason,omitempty"`
	Revision     int64              `json:"revision"`
}

// convertKeyValue converts a transport.MVCCKeyValue to a wailsKeyValue,
// converting []byte fields to strings for plain-text Wails IPC serialization.
func convertKeyValue(kv *transport.MVCCKeyValue) *wailsKeyValue {
	if kv == nil {
		return nil
	}
	return &wailsKeyValue{
		Key:            string(kv.Key),
		Value:          string(kv.Value),
		CreateRevision: kv.CreateRevision,
		ModRevision:    kv.ModRevision,
		Version:        kv.Version,
		LeaseID:        kv.LeaseID,
	}
}

// convertWatchResponse converts a transport.WatchResponse to a wailsWatchResponse,
// ensuring all []byte fields are serialized as plain strings over Wails IPC.
func convertWatchResponse(resp transport.WatchResponse) wailsWatchResponse {
	events := make([]*wailsWatchEvent, len(resp.Events))
	for i, evt := range resp.Events {
		events[i] = &wailsWatchEvent{
			Type:   evt.Type,
			Kv:     convertKeyValue(evt.Kv),
			PrevKv: convertKeyValue(evt.PrevKv),
		}
	}
	return wailsWatchResponse{
		WatchID:      resp.WatchID,
		Events:       events,
		Canceled:     resp.Canceled,
		CancelReason: resp.CancelReason,
		Revision:     resp.Revision,
	}
}

// activeWatch tracks a running watch goroutine and its cancellation.
type activeWatch struct {
	cancel context.CancelFunc
}

// WatchBinding exposes key change event streaming to the Wails frontend.
// Watches run in background goroutines and emit events to the frontend
// via the Wails runtime event bus (event name: "qrdb:watch:{id}").
type WatchBinding struct {
	logger  *logger.Logger
	conn    *ConnectionBinding
	emitFn  EventEmitter
	nextID  atomic.Int64
	mu      sync.Mutex
	watches map[int64]*activeWatch
}

// NewWatchBinding creates a new WatchBinding backed by the given connection.
// Event emission uses wailsruntime.EventsEmit by default.
func NewWatchBinding(log *logger.Logger, conn *ConnectionBinding) *WatchBinding {
	return &WatchBinding{
		logger:  log,
		conn:    conn,
		emitFn:  wailsruntime.EventsEmit,
		watches: make(map[int64]*activeWatch),
	}
}

// NewWatchBindingWithEmitter creates a WatchBinding with a custom event
// emitter. Use this in tests to avoid depending on the Wails runtime.
func NewWatchBindingWithEmitter(log *logger.Logger, conn *ConnectionBinding, emit EventEmitter) *WatchBinding {
	return &WatchBinding{
		logger:  log,
		conn:    conn,
		emitFn:  emit,
		watches: make(map[int64]*activeWatch),
	}
}

// Watch creates a watcher for the specified key and returns a watch ID.
// Events are streamed to the frontend via the Wails event bus.
func (b *WatchBinding) Watch(key string, opts transport.WatchOptions) (int64, error) {
	b.logger.Debug("Watch called", "key", key)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Watch failed", "error", err)
		return 0, err
	}

	watchID := b.nextID.Add(1)
	ctx, cancel := context.WithCancel(context.Background())

	ch, err := cl.Watch().Watch(ctx, key, opts)
	if err != nil {
		cancel()
		b.logger.Error("Watch failed", "key", key, "error", err)
		return 0, err
	}

	b.mu.Lock()
	b.watches[watchID] = &activeWatch{cancel: cancel}
	b.mu.Unlock()

	go b.streamEvents(watchID, ch)

	b.logger.Debug("Watch created", "key", key, "watchID", watchID)
	return watchID, nil
}

// WatchPrefix creates a watcher for all keys matching the given prefix
// and returns a watch ID.
func (b *WatchBinding) WatchPrefix(prefix string, opts transport.WatchOptions) (int64, error) {
	b.logger.Debug("WatchPrefix called", "prefix", prefix)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("WatchPrefix failed", "error", err)
		return 0, err
	}

	watchID := b.nextID.Add(1)
	ctx, cancel := context.WithCancel(context.Background())

	ch, err := cl.Watch().WatchPrefix(ctx, prefix, opts)
	if err != nil {
		cancel()
		b.logger.Error("WatchPrefix failed", "prefix", prefix, "error", err)
		return 0, err
	}

	b.mu.Lock()
	b.watches[watchID] = &activeWatch{cancel: cancel}
	b.mu.Unlock()

	go b.streamEvents(watchID, ch)

	b.logger.Debug("WatchPrefix created", "prefix", prefix, "watchID", watchID)
	return watchID, nil
}

// CancelWatch stops an active watch by its ID.
func (b *WatchBinding) CancelWatch(watchID int64) {
	b.logger.Debug("CancelWatch called", "watchID", watchID)
	b.mu.Lock()
	w, ok := b.watches[watchID]
	if ok {
		delete(b.watches, watchID)
	}
	b.mu.Unlock()

	if ok {
		w.cancel()
		b.logger.Debug("CancelWatch completed", "watchID", watchID)
	}
}

// streamEvents reads from the watch channel and emits each response
// to the Wails frontend event bus. It runs until the channel closes
// or the watch is canceled.
func (b *WatchBinding) streamEvents(watchID int64, ch <-chan transport.WatchResponse) {
	eventName := fmt.Sprintf("qrdb:watch:%d", watchID)

	for resp := range ch {
		resp.WatchID = watchID
		b.emitFn(b.conn.ctx, eventName, convertWatchResponse(resp))
	}

	// Channel closed — clean up and notify frontend of cancellation.
	b.mu.Lock()
	w, ok := b.watches[watchID]
	if ok {
		delete(b.watches, watchID)
	}
	b.mu.Unlock()

	if ok {
		w.cancel()
	}

	b.emitFn(b.conn.ctx, eventName, wailsWatchResponse{
		WatchID:      watchID,
		Canceled:     true,
		CancelReason: "watch ended",
	})

	b.logger.Debug("Watch stream ended", "watchID", watchID)
}
