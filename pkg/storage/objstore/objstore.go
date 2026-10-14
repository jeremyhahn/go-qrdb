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

// Package objstore provides a key-value storage engine backed by go-objstore,
// so any go-objstore provider (local, memory, s3, gcs, azure, ...) can
// serve as a flat KV backend. It mirrors the file backend's contract — a simple
// KV engine with no MVCC, and a commit journal giving Batch the same
// all-or-nothing-after-recovery guarantee (see journal.go) — and is intended
// for blob/backup-oriented workloads rather than hot consensus paths. Keys
// are arbitrary bytes: each is stored under an object name derived from it
// (see objectName), never verbatim, so any key round-trips on every provider
// whatever that provider's own object name rules.
package objstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	objfactory "github.com/jeremyhahn/go-objstore/pkg/factory"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// Compile-time interface check.
var _ storage.Storage = (*Engine)(nil)

// Engine is a key-value storage engine backed by a go-objstore Storage.
type Engine struct {
	store objcommon.Storage

	// journal holds the commit journal. It is store itself unless the
	// provider lists by walking a directory tree, in which case it is a
	// separate store the walk never visits (see openStores).
	journal  objcommon.Storage
	provider string
	closed   atomic.Bool

	// journalMu serializes batch commits: the commit journal is a single
	// object per engine, so two concurrent commits must not interleave
	// publish/apply/clear.
	journalMu sync.Mutex

	// journalPending is set when a batch commit fails after it may have
	// published its journal, so the commit is neither complete nor known to
	// be absent. Every later write resolves it first (resolvePendingJournal)
	// rather than deciding against, or writing under, a half-applied batch.
	journalPending atomic.Bool

	// writeMu makes CompareAndSwap and a batch commit atomic against every
	// other writer in this process: Set and Delete hold it shared from their
	// check for a pending commit through their write (enterWrite);
	// CompareAndSwap, a batch commit and the resolution of a pending commit
	// hold it exclusive, so journalPending changes only under it.
	// Lock order is journalMu, then writeMu. The provider offers no
	// conditional put through this interface, so atomicity does not extend
	// to a second process writing the same bucket.
	writeMu sync.RWMutex
}

// New constructs an objstore-backed engine for the given go-objstore provider.
// Providers are resolved through go-objstore's factory: "local" and "memory" are
// always available, while cloud providers ("s3", "gcs", "azure", "glacier",
// "azurearchive") link their SDKs and are compiled in only when go-objstore's
// matching build tag is set (awss3, gcpstorage, azureblob, glacier,
// azurearchive). Settings are passed verbatim to the provider.
//
// The store must be dedicated to this engine. An empty store is marked with
// this build's object layout; a non-empty one is opened only if it carries
// that mark, so objects written by any other layout are refused, never
// silently hidden. An interrupted batch commit from a previous process is
// completed here (see recoverJournal); a corrupt journal fails the open
// rather than silently serving a half-applied batch.
func New(provider string, settings map[string]string) (*Engine, error) {
	if provider == "" {
		return nil, storage.ErrInvalidPath
	}
	store, err := objfactory.NewStorage(provider, settings)
	if err != nil {
		if errors.Is(err, objfactory.ErrUnknownBackend) {
			return nil, &UnsupportedProviderError{Provider: provider}
		}
		return nil, err
	}
	store, journal, err := openStores(provider, settings, store)
	if err != nil {
		return nil, err
	}
	e := &Engine{store: store, journal: journal, provider: provider}
	if err := e.ensureFormatMarker(); err != nil {
		return nil, err
	}
	// An interrupted batch commit from a previous process is completed here;
	// a corrupt journal fails the open rather than silently serving a
	// half-applied batch.
	if err := e.recoverJournal(); err != nil {
		return nil, err
	}
	return e, nil
}

// Provider returns the go-objstore provider backing this engine.
func (e *Engine) Provider() string { return e.provider }

// notFound reports whether err signals a missing object.
func notFound(err error) bool {
	return errors.Is(err, objcommon.ErrKeyNotFound)
}

// ensureFormatMarker refuses to open a non-empty store that was not written
// by this exact object layout (see FormatMarkerMissingError and
// FormatVersionMismatchError), and marks an empty one as this layout.
func (e *Engine) ensureFormatMarker() error {
	rc, err := e.store.Get(formatMarkerName)
	if err == nil {
		defer func() { _ = rc.Close() }()
		found, readErr := io.ReadAll(rc)
		if readErr != nil {
			return readErr
		}
		if string(found) != formatVersion {
			return &FormatVersionMismatchError{Found: string(found), Want: formatVersion}
		}
		return nil
	}
	if !notFound(err) {
		return err
	}
	names, err := e.store.List("")
	if err != nil {
		return err
	}
	if len(names) > 0 {
		return &FormatMarkerMissingError{Provider: e.provider}
	}
	return e.store.Put(formatMarkerName, strings.NewReader(formatVersion))
}

// Get retrieves the value for key. Returns nil, nil when the key does not exist,
// per the storage.Storage contract.
func (e *Engine) Get(key []byte) ([]byte, error) {
	if e.closed.Load() {
		return nil, &storage.ClosedError{}
	}
	if len(key) == 0 {
		return nil, storage.ErrEmptyKey
	}
	name, hashed := objectName(key)
	if hashed {
		stored, value, err := e.readHashed(name, true)
		if err != nil {
			if notFound(err) {
				return nil, nil
			}
			return nil, err
		}
		if !bytes.Equal(stored, key) {
			return nil, &KeyMismatchError{Name: name}
		}
		return value, nil
	}
	rc, err := e.store.Get(name)
	if err != nil {
		if notFound(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	return io.ReadAll(rc)
}

// Set stores a key-value pair as an object.
func (e *Engine) Set(key, value []byte) error {
	if e.closed.Load() {
		return &storage.ClosedError{}
	}
	if len(key) == 0 {
		return storage.ErrEmptyKey
	}
	if err := e.enterWrite(); err != nil {
		return err
	}
	defer e.writeMu.RUnlock()
	return e.put(key, value)
}

// put writes the object for key. The caller holds writeMu.
func (e *Engine) put(key, value []byte) error {
	name, hashed := objectName(key)
	if hashed {
		return e.store.Put(name, hashedRecord(key, value))
	}
	return e.store.Put(name, bytes.NewReader(value))
}

// CompareAndSwap implements storage.GuardedStorage. The read, compare and
// write run under the exclusive in-process lock, so concurrent swaps on a key
// serialise. It does not guard against a second process writing the bucket.
func (e *Engine) CompareAndSwap(ctx context.Context, key, expected, next []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if e.closed.Load() {
		return false, &storage.ClosedError{}
	}
	if len(key) == 0 {
		return false, storage.ErrEmptyKey
	}
	e.journalMu.Lock()
	defer e.journalMu.Unlock()
	if err := e.resolvePendingJournal(); err != nil {
		return false, err
	}
	e.writeMu.Lock()
	defer e.writeMu.Unlock()

	current, err := e.Get(key)
	if err != nil {
		return false, err
	}
	if current == nil || !bytes.Equal(current, expected) {
		return false, nil
	}
	if err := e.put(key, next); err != nil {
		return false, err
	}
	return true, nil
}

// Delete removes a key. A missing key is not an error (idempotent).
func (e *Engine) Delete(key []byte) error {
	if e.closed.Load() {
		return &storage.ClosedError{}
	}
	if len(key) == 0 {
		return storage.ErrEmptyKey
	}
	if err := e.enterWrite(); err != nil {
		return err
	}
	defer e.writeMu.RUnlock()
	return e.remove(key)
}

// remove deletes the object for key; a missing object is not an error. The
// caller holds writeMu.
func (e *Engine) remove(key []byte) error {
	name, _ := objectName(key)
	if err := e.store.Delete(name); err != nil && !notFound(err) {
		return err
	}
	return nil
}

// List returns all keys matching prefix. An empty prefix returns all keys.
//
// The provider is asked to narrow by the encoded prefix, which is a string
// prefix of every matching key's direct name; the byte-prefix contract of
// storage.Storage is still enforced on the decoded keys (see scan).
func (e *Engine) List(prefix []byte) ([][]byte, error) {
	if e.closed.Load() {
		return nil, &storage.ClosedError{}
	}
	entries, err := e.scan(prefix, false)
	if err != nil {
		return nil, err
	}
	keys := make([][]byte, len(entries))
	for i := range entries {
		keys[i] = entries[i].key
	}
	return keys, nil
}

// scan returns every stored entry whose key begins with prefix, in no
// particular order, carrying values when withValues is set. Direct names are
// narrowed on the provider by the prefix's own encoding; hashed names cannot
// be, so every hashed record is read for the key it carries. An object
// deleted between the listing and its read is skipped.
func (e *Engine) scan(prefix []byte, withValues bool) ([]kvEntry, error) {
	var entries []kvEntry
	if providerPrefix, ok := directListPrefix(prefix); ok {
		names, err := e.store.List(providerPrefix)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			key, ok := decodeDirectName(name)
			// Defend against providers that do not filter server-side.
			if !ok || !bytes.HasPrefix(key, prefix) {
				continue
			}
			entry := kvEntry{key: key}
			if withValues {
				value, err := e.Get(key)
				if err != nil {
					return nil, err
				}
				if value == nil {
					continue
				}
				entry.value = value
			}
			entries = append(entries, entry)
		}
	}
	names, err := e.store.List(hashedPrefix)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		if !isHashedName(name) {
			continue
		}
		key, value, err := e.readHashed(name, withValues)
		if err != nil {
			if notFound(err) {
				continue
			}
			return nil, err
		}
		if bytes.HasPrefix(key, prefix) {
			entries = append(entries, kvEntry{key: key, value: value})
		}
	}
	return entries, nil
}

// readHashed reads the record stored under the hashed name.
func (e *Engine) readHashed(name string, withValue bool) ([]byte, []byte, error) {
	rc, err := e.store.Get(name)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rc.Close() }()
	return readHashedRecord(name, rc, withValue)
}

// Exists reports whether key exists.
func (e *Engine) Exists(key []byte) (bool, error) {
	if e.closed.Load() {
		return false, &storage.ClosedError{}
	}
	if len(key) == 0 {
		return false, storage.ErrEmptyKey
	}
	name, _ := objectName(key)
	ok, err := e.store.Exists(context.Background(), name)
	if err != nil {
		if notFound(err) {
			return false, nil
		}
		return false, err
	}
	return ok, nil
}

// Batch creates a new write batch for atomic multi-key operations. A closed
// engine returns a batch that reports the closure from every operation.
func (e *Engine) Batch() storage.Batch {
	if e.closed.Load() {
		return storage.NewClosedBatch()
	}
	return newBatch(e)
}

// NewIterator creates an iterator for scanning key ranges. A closed engine
// returns an iterator that reports the closure through Error.
func (e *Engine) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	if e.closed.Load() {
		return storage.NewClosedIterator()
	}
	return newIterator(e, opts)
}

// Snapshot creates a point-in-time snapshot for consistent reads.
func (e *Engine) Snapshot() (storage.Snapshot, error) {
	if e.closed.Load() {
		return nil, &storage.ClosedError{}
	}
	return newSnapshot(e)
}

// Close marks the engine as closed. The underlying go-objstore Storage exposes
// no Close, so there is nothing further to release.
func (e *Engine) Close() error {
	e.closed.Store(true)
	return nil
}
