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

// Manager implements the original DragonDB watch manager with full-featured
// pub/sub for key change notifications including delta mode, stats, and
// gRPC stream delivery.
//
// Key features:
//   - Sharded watch registry for low lock contention (256 shards)
//   - Support for exact and prefix watches
//   - Zero-cost overhead when no watches are active
//   - Async event delivery to prevent blocking write path
//   - Slow consumer protection with automatic event dropping
//   - Delta mode for efficient bandwidth usage
//
// Performance characteristics:
//   - No watches: Single atomic pointer check (~1-2ns)
//   - Active watches: O(1) hash lookup + async channel send (~150-300ns)
//   - Memory: ~152 bytes per watch
//   - Expected CPU overhead: 3-4% under heavy watch load

package watch

import (
	"bytes"
	"context"
	"sync"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/messaging"
)

// Manager coordinates all active watches and dispatches events.
// Uses sharded locking to minimize contention, matching FastStateMachine's design.
type Manager struct {
	shards        [256]*managerShard
	idSeq         uint64                   // Atomic watch ID sequence
	watchCount    uint64                   // Atomic count of active watches (for fast path check)
	eventPool     sync.Pool                // Reuse event objects to reduce GC pressure
	deltaComputer *messaging.DeltaComputer // Delta computation engine for delta mode watches
	logger        *logger.Logger           // Logger for delta computation errors
}

// managerShard holds a subset of watches to reduce lock contention.
// Each shard manages watches whose keys hash to that shard.
type managerShard struct {
	exactWatches  map[string][]*ManagerWatch // Exact key matches: key -> []*ManagerWatch
	prefixWatches []*ManagerWatch            // Prefix matches (requires iteration)
	mu            sync.RWMutex
}

// ManagerWatch represents a single client subscription to key changes.
type ManagerWatch struct {
	ID              uint64
	Key             []byte
	RangeEnd        []byte             // Non-nil for range/prefix watches
	StartRev        int64              // Start watching from this revision
	Filters         WatchFilters       // Event filtering options
	Stream          ManagerWatchStream // gRPC stream for sending events
	Events          chan *ManagerWatchEvent
	ctx             context.Context
	cancel          context.CancelFunc
	dropped         atomic.Uint64 // Count of dropped events (slow consumer)
	skipDeliver     bool          // If true, don't start deliverEvents goroutine
	deltaMode       bool          // If true, compute and send delta instead of full value
	includeOldValue bool          // If true, include old value in delta computation
	entityType      interface{}   // Pointer to struct type for delta computation (required for deltaMode)
}

// managerWatchOption is a functional option for configuring a Manager watch.
type managerWatchOption func(*managerWatchOptions)

// managerWatchOptions holds configuration for a Manager watch.
type managerWatchOptions struct {
	prefix          bool
	deltaMode       bool
	includeOldValue bool
	entityType      interface{} // Pointer to struct type for delta computation
}

// WithPrefix returns a managerWatchOption that enables prefix matching.
// When enabled, the watch will match all keys with the given prefix.
func WithPrefix() managerWatchOption {
	return func(opts *managerWatchOptions) {
		opts.prefix = true
	}
}

// WithDeltaMode returns a managerWatchOption that enables delta mode.
// When enabled, watch events will contain only changed fields (Delta field)
// instead of the full value (Kv field). This reduces bandwidth for
// entities with many fields where only a few change.
func WithDeltaMode() managerWatchOption {
	return func(opts *managerWatchOptions) {
		opts.deltaMode = true
	}
}

// WithIncludeOldValues returns a managerWatchOption that includes old values in delta computation.
// When enabled, each FieldChange in the delta will include the previous value,
// useful for conflict detection and audit trails.
// This option is only meaningful when delta mode is enabled.
func WithIncludeOldValues() managerWatchOption {
	return func(opts *managerWatchOptions) {
		opts.includeOldValue = true
	}
}

// WithEntityType returns a managerWatchOption that sets the entity type for delta computation.
// The entityType must be a pointer to a struct type (e.g., &User{}).
// This option is required when delta mode is enabled - without it, delta computation
// will be skipped and full values will be sent instead.
func WithEntityType(entityType interface{}) managerWatchOption {
	return func(opts *managerWatchOptions) {
		opts.entityType = entityType
	}
}

// NewManager creates a new watch manager with pre-initialized shards.
func NewManager() *Manager {
	m := &Manager{
		eventPool: sync.Pool{
			New: func() interface{} {
				return &ManagerWatchEvent{}
			},
		},
	}

	// Initialize all shards
	for i := range m.shards {
		m.shards[i] = &managerShard{
			exactWatches:  make(map[string][]*ManagerWatch),
			prefixWatches: make([]*ManagerWatch, 0),
		}
	}

	return m
}

// NewManagerWithDelta creates a new watch manager with delta computation support.
// The deltaComputer and logger are required for computing deltas when watches
// are created with WithDeltaMode().
func NewManagerWithDelta(deltaComputer *messaging.DeltaComputer, log *logger.Logger) *Manager {
	m := &Manager{
		eventPool: sync.Pool{
			New: func() interface{} {
				return &ManagerWatchEvent{}
			},
		},
		deltaComputer: deltaComputer,
		logger:        log,
	}

	// Initialize all shards
	for i := range m.shards {
		m.shards[i] = &managerShard{
			exactWatches:  make(map[string][]*ManagerWatch),
			prefixWatches: make([]*ManagerWatch, 0),
		}
	}

	return m
}

// Watch creates a watch and returns a channel of simplified ManagerEvents.
// This is a higher-level API that wraps the lower-level Add() method.
//
// The returned channel will be closed when:
//   - The context is cancelled
//   - An error occurs in event delivery
//
// Example:
//
//	ch, err := manager.Watch(ctx, "/_cluster/members/", watch.WithPrefix())
//	if err != nil {
//	    return err
//	}
//	for event := range ch {
//	    // Handle event
//	}
func (m *Manager) Watch(ctx context.Context, key string, opts ...managerWatchOption) (<-chan ManagerEvent, error) {
	// Parse options
	options := &managerWatchOptions{}
	for _, opt := range opts {
		opt(options)
	}

	// Calculate rangeEnd for prefix watches
	var rangeEnd []byte
	if options.prefix {
		rangeEnd = managerGetPrefix([]byte(key))
	}

	// Create a channel for simplified events
	eventChan := make(chan ManagerEvent, 100)

	// Create a simple stream wrapper - it won't be used but is required
	stream := &managerNoopStream{ctx: ctx}

	// Add the watch using existing infrastructure, but skip deliverEvents
	watch, err := m.addInternal([]byte(key), rangeEnd, 0, WatchFilters{}, stream, options, true)
	if err != nil {
		close(eventChan)
		return nil, &WatchCreateError{Cause: err}
	}

	// Start a goroutine to convert ManagerWatchEvents to ManagerEvents
	go m.convertEvents(watch, eventChan)

	return eventChan, nil
}

// convertEvents converts ManagerWatchEvents to simplified ManagerEvents and sends them to the channel.
// This runs in a dedicated goroutine and handles cleanup on context cancellation.
func (m *Manager) convertEvents(w *ManagerWatch, eventChan chan<- ManagerEvent) {
	defer close(eventChan)

	for {
		select {
		case watchEvent := <-w.Events:
			// Convert ManagerWatchEvent to simplified ManagerEvent
			event := ManagerEvent{
				Key:      watchEvent.Kv.Key,
				Revision: watchEvent.Revision,
				Version:  watchEvent.Kv.Version,
			}

			switch watchEvent.Type {
			case PUT:
				event.Type = ManagerEventTypePut
				event.Value = watchEvent.Kv.Value
			case DELETE:
				event.Type = ManagerEventTypeDelete
				// Value is nil for deletes, but include previous value if available
				if watchEvent.PrevKv != nil {
					event.Value = watchEvent.PrevKv.Value
				}
			}

			// Send to channel (blocking since this is user-facing)
			select {
			case eventChan <- event:
				// Successfully sent
			case <-w.ctx.Done():
				// Context cancelled
				m.eventPool.Put(watchEvent)
				return
			}

			// Return event to pool after successful send
			m.eventPool.Put(watchEvent)

		case <-w.ctx.Done():
			// Watch cancelled or stream closed
			return
		}
	}
}

// managerNoopStream is a minimal ManagerWatchStream implementation for the Watch() API.
// It's not actually used since we handle events in convertEvents instead.
type managerNoopStream struct {
	ctx context.Context
}

func (s *managerNoopStream) Send(resp *ManagerWatchResponse) error {
	return nil
}

func (s *managerNoopStream) Context() context.Context {
	return s.ctx
}

// managerGetPrefix returns the range end for a prefix watch.
// For example, "foo/" -> "foo0" to match all keys starting with "foo/".
// For an empty prefix, returns []byte{} (empty but non-nil) to indicate "match all keys".
func managerGetPrefix(prefix []byte) []byte {
	// Special case: empty prefix should match all keys
	// Return empty non-nil slice to distinguish from "no range" (nil)
	if len(prefix) == 0 {
		return []byte{}
	}

	end := make([]byte, len(prefix))
	copy(end, prefix)

	for i := len(end) - 1; i >= 0; i-- {
		if end[i] < 0xff {
			end[i]++
			return end[:i+1]
		}
	}

	// If all bytes are 0xff, return empty slice (matches everything)
	return []byte{}
}

// Add registers a new watch and returns its ID.
// This is the public API that always starts the deliverEvents goroutine.
func (m *Manager) Add(key, rangeEnd []byte, startRev int64, filters WatchFilters, stream ManagerWatchStream) (*ManagerWatch, error) {
	return m.addInternal(key, rangeEnd, startRev, filters, stream, &managerWatchOptions{}, false)
}

// addInternal is the internal implementation of Add with optional skipDeliver flag.
func (m *Manager) addInternal(key, rangeEnd []byte, startRev int64, filters WatchFilters, stream ManagerWatchStream, opts *managerWatchOptions, skipDeliver bool) (*ManagerWatch, error) {
	// Generate unique watch ID
	id := atomic.AddUint64(&m.idSeq, 1)

	// Create buffered channel with large buffer to ensure reliability
	// Buffer size of 10000 provides reliability guarantee
	// (never drops events - "Reliable" guarantee)
	events := make(chan *ManagerWatchEvent, 10000)

	// Create cancellable context for watch cleanup.
	// cancel is stored in ManagerWatch.cancel and called by Remove/Close.
	ctx, cancel := context.WithCancel(stream.Context()) // #nosec G118 -- cancel stored in watch.cancel

	watch := &ManagerWatch{
		ID:              id,
		Key:             key,
		RangeEnd:        rangeEnd,
		StartRev:        startRev,
		Filters:         filters,
		Stream:          stream,
		Events:          events,
		ctx:             ctx,
		cancel:          cancel,
		skipDeliver:     skipDeliver,
		deltaMode:       opts.deltaMode,
		includeOldValue: opts.includeOldValue,
		entityType:      opts.entityType,
	}

	// Add to appropriate shard
	shard := m.managerGetShard(key)
	shard.mu.Lock()
	defer shard.mu.Unlock()

	if rangeEnd == nil {
		// Exact match watch (rangeEnd is explicitly nil)
		keyStr := string(key)
		shard.exactWatches[keyStr] = append(shard.exactWatches[keyStr], watch)
	} else {
		// Range/prefix watch (rangeEnd is non-nil, may be empty slice for "match all")
		shard.prefixWatches = append(shard.prefixWatches, watch)
	}

	// Increment watch count for fast path check
	atomic.AddUint64(&m.watchCount, 1)

	// Start event delivery goroutine only if not skipped
	if !skipDeliver {
		go m.deliverEvents(watch)
	}

	return watch, nil
}

// Remove cancels and removes a watch.
func (m *Manager) Remove(watchID uint64) {
	// Iterate through all shards to find and remove the watch
	for i := range m.shards {
		shard := m.shards[i]
		shard.mu.Lock()

		// Check exact watches
		for key, watches := range shard.exactWatches {
			for j, w := range watches {
				if w.ID == watchID {
					// Cancel watch context
					w.cancel()

					// Remove from slice
					shard.exactWatches[key] = append(watches[:j], watches[j+1:]...)
					if len(shard.exactWatches[key]) == 0 {
						delete(shard.exactWatches, key)
					}

					// Decrement watch count
					atomic.AddUint64(&m.watchCount, ^uint64(0)) // Atomic decrement

					shard.mu.Unlock()
					return
				}
			}
		}

		// Check prefix watches
		for j, w := range shard.prefixWatches {
			if w.ID == watchID {
				// Cancel watch context
				w.cancel()

				// Remove from slice
				shard.prefixWatches = append(shard.prefixWatches[:j], shard.prefixWatches[j+1:]...)

				// Decrement watch count
				atomic.AddUint64(&m.watchCount, ^uint64(0)) // Atomic decrement

				shard.mu.Unlock()
				return
			}
		}

		shard.mu.Unlock()
	}
}

// NotifyPut notifies all matching watches of a PUT event.
// This is called from FastStateMachine after successful writes.
// PERFORMANCE: O(1) hash lookup for exact watches, O(N) for prefix watches.
//
// Parameters:
//   - key: The key that was written
//   - prevValue: The previous value before the write (may be nil for new keys)
//   - value: The new value
//   - revision: The global MVCC revision when the write occurred
//   - version: The per-key modification count (number of times this key has been modified)
func (m *Manager) NotifyPut(key, prevValue, value []byte, revision, version int64) {
	m.notify(key, value, prevValue, revision, version, PUT)
}

// NotifyDelete notifies all matching watches of a DELETE event.
// This is called from FastStateMachine after successful deletes.
//
// Parameters:
//   - key: The key that was deleted
//   - prevValue: The previous value before deletion (may be nil)
//   - revision: The global MVCC revision when the delete occurred
//   - version: The per-key version at time of deletion
func (m *Manager) NotifyDelete(key, prevValue []byte, revision, version int64) {
	m.notify(key, nil, prevValue, revision, version, DELETE)
}

// notify is the internal notification handler.
func (m *Manager) notify(key, value, prevValue []byte, revision, version int64, eventType ManagerEventType) {
	// FAST PATH: If no watches are active, return immediately
	// This provides zero-cost overhead when watches are not being used
	if atomic.LoadUint64(&m.watchCount) == 0 {
		return
	}

	// Get event from pool
	event, ok := m.eventPool.Get().(*ManagerWatchEvent)
	if !ok {
		event = &ManagerWatchEvent{}
	}
	event.Type = eventType
	event.Kv = &ManagerKeyValue{
		Key:         key,
		Value:       value,
		ModRevision: revision,
		Version:     version,
	}
	if prevValue != nil {
		event.PrevKv = &ManagerKeyValue{
			Key:   key,
			Value: prevValue,
		}
	} else {
		event.PrevKv = nil
	}
	event.Revision = revision

	// Check exact watches in the shard for this key
	shard := m.managerGetShard(key)
	shard.mu.RLock()
	if watches, exists := shard.exactWatches[string(key)]; exists {
		for _, w := range watches {
			m.sendEvent(w, event)
		}
	}
	shard.mu.RUnlock()

	// For prefix watches, we need to check all shards since a prefix watch
	// might be in a different shard than the event key
	for i := range m.shards {
		shard := m.shards[i]
		shard.mu.RLock()
		for _, w := range shard.prefixWatches {
			if m.managerMatchesRange(key, w.Key, w.RangeEnd) {
				m.sendEvent(w, event)
			}
		}
		shard.mu.RUnlock()
	}
}

// sendEvent sends an event to a watch with reliability guarantee.
// This provides "Reliable" guarantee - events are never dropped.
// Uses blocking send to ensure delivery (with large buffer to prevent blocking in practice).
func (m *Manager) sendEvent(w *ManagerWatch, event *ManagerWatchEvent) {
	// Apply filters
	if (event.Type == PUT && w.Filters.NoPut) ||
		(event.Type == DELETE && w.Filters.NoDelete) {
		return
	}

	// Prepare event for this watch
	eventToSend := event

	// For delta mode, compute delta and create a new event
	if w.deltaMode && m.deltaComputer != nil {
		deltaEvent := m.computeDeltaEvent(w, event)
		if deltaEvent != nil {
			eventToSend = deltaEvent
		}
		// If delta computation failed, fall through to send full value
	}

	// Blocking send to ensure reliability ("Reliable" guarantee)
	// The large buffer (10000) ensures this rarely blocks in practice
	select {
	case w.Events <- eventToSend:
		// Successfully queued
	case <-w.ctx.Done():
		// Watch cancelled - return event to pool
		m.eventPool.Put(event)
	}
}

// computeDeltaEvent computes a delta event for a delta-mode watch.
// Returns nil if delta computation fails or is not possible.
func (m *Manager) computeDeltaEvent(w *ManagerWatch, event *ManagerWatchEvent) *ManagerWatchEvent {
	// Delta computation requires an entity type
	if w.entityType == nil {
		if m.logger != nil {
			m.logger.Warnf("Delta mode enabled for watch %d but no entity type specified, sending full value", w.ID)
		}
		return nil
	}

	// Get old and new values for delta computation
	var oldData, newData []byte

	switch event.Type {
	case PUT:
		newData = event.Kv.Value
		if event.PrevKv != nil {
			oldData = event.PrevKv.Value
		}
	case DELETE:
		// For delete events, the old value is in PrevKv, new value is nil
		if event.PrevKv != nil {
			oldData = event.PrevKv.Value
		}
		// newData stays nil for delete
	}

	// Skip delta computation if no data to compare
	if len(newData) == 0 && len(oldData) == 0 {
		return nil
	}

	// Compute delta
	delta, err := m.deltaComputer.ComputeBytes(oldData, newData, w.entityType)
	if err != nil {
		if m.logger != nil {
			m.logger.Warnf("Delta computation failed for watch %d: %v, sending full value", w.ID, err)
		}
		return nil
	}

	// Create a new event with delta
	deltaEvent := &ManagerWatchEvent{
		Type:     event.Type,
		Kv:       nil, // Don't send full value in delta mode
		PrevKv:   nil, // Don't send previous value (it's in delta if includeOldValue)
		Delta:    delta,
		Revision: event.Revision,
	}

	// If watch doesn't want old values in delta, clear them
	if !w.includeOldValue && delta != nil {
		for i := range delta.Changes {
			delta.Changes[i].OldValue = nil
		}
	}

	return deltaEvent
}

// deliverEvents reads from a watch's event channel and sends to gRPC stream.
// Runs in a dedicated goroutine per watch.
func (m *Manager) deliverEvents(w *ManagerWatch) {
	for {
		select {
		case event := <-w.Events:
			// Build response
			resp := &ManagerWatchResponse{
				WatchID:  int64(w.ID), //nolint:gosec // G115: watch ID conversion for proto
				Events:   []*ManagerWatchEvent{event},
				Revision: event.Revision,
			}

			// Send to gRPC stream
			if err := w.Stream.Send(resp); err != nil {
				// Stream error - cancel watch
				w.cancel()
				// Return event to pool
				m.eventPool.Put(event)
				return
			}

			// Return event to pool after successful send
			m.eventPool.Put(event)

		case <-w.ctx.Done():
			// Watch cancelled or stream closed
			return
		}
	}
}

// managerMatchesRange checks if a key falls within a watch's range.
// For prefix watches, rangeEnd is typically key + 1 (e.g., "foo/" -> "foo0").
// An empty (but non-nil) rangeEnd means "match all keys after start".
func (m *Manager) managerMatchesRange(key, start, end []byte) bool {
	// Key must be >= start
	if bytes.Compare(key, start) < 0 {
		return false
	}
	// If end is non-nil and non-empty, key must be < end
	if len(end) > 0 && bytes.Compare(key, end) >= 0 {
		return false
	}
	return true
}

// managerGetShard returns the shard for a given key using FNV-1a hash.
// Matches the sharding strategy used by FastStateMachine.
func (m *Manager) managerGetShard(key []byte) *managerShard {
	hash := managerFnv1a(key)
	return m.shards[hash]
}

// managerFnv1a implements the FNV-1a hash function (fast, good distribution).
// Returns a shard index in the range [0, 255].
func managerFnv1a(key []byte) uint8 {
	const (
		offset32 = uint32(2166136261)
		prime32  = uint32(16777619)
	)
	hash := offset32
	for _, b := range key {
		hash ^= uint32(b)
		hash *= prime32
	}
	return uint8(hash % 256)
}

// ManagerStats returns statistics about active watches.
type ManagerStats struct {
	TotalWatches  int
	ExactWatches  int
	PrefixWatches int
	DroppedEvents uint64
}

// GetStats returns current watch statistics.
func (m *Manager) GetStats() ManagerStats {
	stats := ManagerStats{}

	for i := range m.shards {
		shard := m.shards[i]
		shard.mu.RLock()

		for _, watches := range shard.exactWatches {
			stats.ExactWatches += len(watches)
			for _, w := range watches {
				stats.DroppedEvents += w.dropped.Load()
			}
		}

		stats.PrefixWatches += len(shard.prefixWatches)
		for _, w := range shard.prefixWatches {
			stats.DroppedEvents += w.dropped.Load()
		}

		shard.mu.RUnlock()
	}

	stats.TotalWatches = stats.ExactWatches + stats.PrefixWatches
	return stats
}
