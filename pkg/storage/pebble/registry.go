// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pebble

import (
	"context"
	"log/slog"
	"sync"
)

// PerTenantRegistry tracks open Pebble instances keyed by tenant id.
//
// Each barrier-mode tenant gets its own *PebbleStorage backed by an
// independent on-disk path assigned via the volume pool. The registry
// owns the lifecycle: Open on tenant create, Close on tenant delete,
// CloseAll on daemon shutdown.
//
// Implementations must be safe for concurrent use.
type PerTenantRegistry interface {
	// Open opens or returns an existing Pebble at path. Idempotent for
	// a given (tenantID, path) pair: a second call with the same path
	// returns the cached handle. A second call with a different path
	// returns TenantPathConflictError — tenant migration is out of
	// scope and must go through explicit Close+Open.
	//
	// Returns RegistryClosedError if CloseAll has already been called.
	Open(ctx context.Context, tenantID string, path string) (*PebbleStorage, error)

	// Get returns the Pebble for tenantID. Returns NotRegisteredError
	// when no entry exists for the supplied tenant.
	Get(tenantID string) (*PebbleStorage, error)

	// Close closes the Pebble for tenantID and removes it from the
	// registry. It is idempotent: closing an unknown or already-closed
	// tenant is a no-op and returns nil.
	Close(ctx context.Context, tenantID string) error

	// CloseAll closes every registered Pebble. Used on daemon shutdown.
	// Best-effort: per-tenant Close errors are logged but never abort
	// iteration. Subsequent Open calls return RegistryClosedError.
	// CloseAll is idempotent — calling it twice is a no-op.
	CloseAll(ctx context.Context) error

	// List returns a snapshot of registered tenant ids. The returned
	// slice is independent of the registry's internal state.
	List() []string
}

// RegistryOption configures a PerTenantRegistry.
type RegistryOption func(*perTenantRegistry)

// WithRegistryLogger sets the structured logger used for lifecycle
// events. Nil loggers are coerced to slog.Default.
func WithRegistryLogger(logger *slog.Logger) RegistryOption {
	return func(r *perTenantRegistry) {
		if logger != nil {
			r.logger = logger
		}
	}
}

// PerTenantOpenFunc opens a *PebbleStorage at the supplied path. It is
// injected so tests can substitute a deterministic stub without
// requiring real Pebble files on disk. The default opener is
// New(&Config{DataDir: path}).
type PerTenantOpenFunc func(path string) (*PebbleStorage, error)

// WithRegistryOpener overrides the default open function. Callers
// supply this to inject a stub or to apply per-tenant Pebble tuning.
func WithRegistryOpener(open PerTenantOpenFunc) RegistryOption {
	return func(r *perTenantRegistry) {
		if open != nil {
			r.open = open
		}
	}
}

// NewPerTenantRegistry constructs an in-memory PerTenantRegistry. The
// returned registry holds no Pebble files until Open is invoked.
func NewPerTenantRegistry(opts ...RegistryOption) PerTenantRegistry {
	r := &perTenantRegistry{
		logger:  slog.Default(),
		open:    defaultOpen,
		entries: make(map[string]*registryEntry),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// defaultOpen is the production opener used when no override is
// supplied via WithRegistryOpener.
func defaultOpen(path string) (*PebbleStorage, error) {
	return New(&Config{DataDir: path})
}

// registryEntry pairs an open Pebble with the path it was opened at.
// The path is recorded so subsequent Open calls can verify they target
// the same on-disk location.
type registryEntry struct {
	path    string
	storage *PebbleStorage
}

// perTenantRegistry is the production map-backed implementation.
type perTenantRegistry struct {
	logger *slog.Logger
	open   PerTenantOpenFunc

	mu      sync.RWMutex
	entries map[string]*registryEntry
	closed  bool
}

// Open implements PerTenantRegistry.
func (r *perTenantRegistry) Open(ctx context.Context, tenantID string, path string) (*PebbleStorage, error) {
	if tenantID == "" {
		return nil, &EmptyTenantIDError{}
	}
	if path == "" {
		return nil, &EmptyPathError{}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return nil, &RegistryClosedError{}
	}

	if existing, ok := r.entries[tenantID]; ok {
		if existing.path != path {
			return nil, &TenantPathConflictError{
				TenantID:      tenantID,
				ExistingPath:  existing.path,
				RequestedPath: path,
			}
		}
		return existing.storage, nil
	}

	storage, err := r.open(path)
	if err != nil {
		return nil, &OpenError{TenantID: tenantID, Path: path, Err: err}
	}

	r.entries[tenantID] = &registryEntry{path: path, storage: storage}
	r.logger.Info("per-tenant pebble opened",
		slog.String("tenant", tenantID),
		slog.String("path", path))
	return storage, nil
}

// Get implements PerTenantRegistry.
func (r *perTenantRegistry) Get(tenantID string) (*PebbleStorage, error) {
	if tenantID == "" {
		return nil, &EmptyTenantIDError{}
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, ok := r.entries[tenantID]
	if !ok {
		return nil, &NotRegisteredError{TenantID: tenantID}
	}
	return entry.storage, nil
}

// Close implements PerTenantRegistry. Idempotent.
func (r *perTenantRegistry) Close(ctx context.Context, tenantID string) error {
	if tenantID == "" {
		return &EmptyTenantIDError{}
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	r.mu.Lock()
	entry, ok := r.entries[tenantID]
	if !ok {
		r.mu.Unlock()
		return nil
	}
	delete(r.entries, tenantID)
	r.mu.Unlock()

	if err := entry.storage.Close(); err != nil {
		r.logger.Error("per-tenant pebble close failed",
			slog.String("tenant", tenantID),
			slog.String("path", entry.path),
			slog.Any("error", err))
		return &CloseError{TenantID: tenantID, Path: entry.path, Err: err}
	}
	r.logger.Info("per-tenant pebble closed",
		slog.String("tenant", tenantID),
		slog.String("path", entry.path))
	return nil
}

// CloseAll implements PerTenantRegistry.
func (r *perTenantRegistry) CloseAll(ctx context.Context) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	entries := r.entries
	r.entries = make(map[string]*registryEntry)
	r.mu.Unlock()

	var firstErr error
	for tenantID, entry := range entries {
		if err := entry.storage.Close(); err != nil {
			r.logger.Error("per-tenant pebble close failed during shutdown",
				slog.String("tenant", tenantID),
				slog.String("path", entry.path),
				slog.Any("error", err))
			if firstErr == nil {
				firstErr = &CloseError{TenantID: tenantID, Path: entry.path, Err: err}
			}
			continue
		}
		r.logger.Info("per-tenant pebble closed on shutdown",
			slog.String("tenant", tenantID),
			slog.String("path", entry.path))
	}
	return firstErr
}

// List implements PerTenantRegistry.
func (r *perTenantRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]string, 0, len(r.entries))
	for id := range r.entries {
		out = append(out, id)
	}
	return out
}
