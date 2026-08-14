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

// Package namespace provides tenant isolation by prefixing all keys
// with a namespace separator.
package namespace

import (
	"bytes"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// Compile-time interface check.
var _ storage.Storage = (*Engine)(nil)

const separator = byte('/')

// Engine wraps another Storage and prefixes all keys with a namespace.
type Engine struct {
	inner     storage.Storage
	namespace []byte
	prefix    []byte // namespace + separator, cached
}

// New creates a namespaced storage that prefixes all keys with namespace + "/".
func New(inner storage.Storage, namespace []byte) *Engine {
	prefix := make([]byte, len(namespace)+1)
	copy(prefix, namespace)
	prefix[len(namespace)] = separator
	return &Engine{
		inner:     inner,
		namespace: namespace,
		prefix:    prefix,
	}
}

// Namespace returns the namespace.
func (e *Engine) Namespace() []byte {
	return e.namespace
}

// Get retrieves the value for the namespaced key.
func (e *Engine) Get(key []byte) ([]byte, error) {
	if len(key) == 0 {
		return nil, storage.ErrEmptyKey
	}
	return e.inner.Get(append(e.prefix, key...))
}

// Set stores a key-value pair under the namespace prefix.
func (e *Engine) Set(key, value []byte) error {
	if len(key) == 0 {
		return storage.ErrEmptyKey
	}
	return e.inner.Set(append(e.prefix, key...), value)
}

// Delete removes a namespaced key.
func (e *Engine) Delete(key []byte) error {
	if len(key) == 0 {
		return storage.ErrEmptyKey
	}
	return e.inner.Delete(append(e.prefix, key...))
}

// List returns all keys matching the given prefix within the namespace.
// The namespace prefix is stripped from the returned keys.
func (e *Engine) List(prefix []byte) ([][]byte, error) {
	keys, err := e.inner.List(append(e.prefix, prefix...))
	if err != nil {
		return nil, err
	}
	result := make([][]byte, 0, len(keys))
	for _, k := range keys {
		result = append(result, bytes.TrimPrefix(k, e.prefix))
	}
	return result, nil
}

// Exists reports whether the namespaced key exists.
func (e *Engine) Exists(key []byte) (bool, error) {
	if len(key) == 0 {
		return false, storage.ErrEmptyKey
	}
	return e.inner.Exists(append(e.prefix, key...))
}

// Batch creates a new write batch that applies namespace prefixes to all keys.
func (e *Engine) Batch() storage.Batch {
	return &namespacedBatch{inner: e.inner.Batch(), prefix: e.prefix}
}

// NewIterator creates an iterator scoped to the namespace. Iterator bounds
// are adjusted to include the namespace prefix, and returned keys have the
// prefix stripped.
func (e *Engine) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	return &namespacedIterator{
		inner:  e.inner.NewIterator(scopeIterOptions(e.prefix, opts)),
		prefix: e.prefix,
	}
}

// scopeIterOptions rewrites iterator options into the namespace's key range,
// so an unbounded request scans only this namespace and a bounded one is
// translated into prefixed bounds.
func scopeIterOptions(prefix []byte, opts *storage.IteratorOptions) *storage.IteratorOptions {
	upper := make([]byte, len(prefix))
	copy(upper, prefix)
	upper[len(upper)-1]++

	nsOpts := &storage.IteratorOptions{
		LowerBound: prefix,
		UpperBound: upper,
	}
	if opts == nil {
		return nsOpts
	}

	nsOpts.Reverse = opts.Reverse
	nsOpts.OnlyKeys = opts.OnlyKeys
	if len(opts.LowerBound) > 0 {
		nsOpts.LowerBound = append(prefix, opts.LowerBound...)
	}
	if len(opts.UpperBound) > 0 {
		nsOpts.UpperBound = append(prefix, opts.UpperBound...)
	}
	return nsOpts
}

// Snapshot creates a point-in-time snapshot scoped to the namespace.
func (e *Engine) Snapshot() (storage.Snapshot, error) {
	snap, err := e.inner.Snapshot()
	if err != nil {
		return nil, err
	}
	return &namespacedSnapshot{inner: snap, prefix: e.prefix}, nil
}

// Close releases resources held by the underlying storage.
func (e *Engine) Close() error {
	return e.inner.Close()
}

// Compile-time interface assertion.
var _ storage.Batch = (*namespacedBatch)(nil)

// namespacedBatch wraps a batch with namespace prefix.
type namespacedBatch struct {
	inner  storage.Batch
	prefix []byte
}

// Set stores a key-value pair in the batch with the namespace prefix.
func (b *namespacedBatch) Set(key, value []byte) error {
	return b.inner.Set(append(b.prefix, key...), value)
}

// Delete removes a key in the batch with the namespace prefix.
func (b *namespacedBatch) Delete(key []byte) error {
	return b.inner.Delete(append(b.prefix, key...))
}

// Get reads through the batch's own pending writes, then the namespaced
// committed state.
func (b *namespacedBatch) Get(key []byte) ([]byte, error) {
	if len(key) == 0 {
		return nil, storage.ErrEmptyKey
	}
	return b.inner.Get(append(b.prefix, key...))
}

// NewIterator scans the namespace through the batch's pending writes, with the
// namespace prefix stripped from returned keys.
func (b *namespacedBatch) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	return &namespacedIterator{
		inner:  b.inner.NewIterator(scopeIterOptions(b.prefix, opts)),
		prefix: b.prefix,
	}
}

// Commit applies all batched operations atomically.
func (b *namespacedBatch) Commit() error { return b.inner.Commit() }

// Rollback discards all batched operations without applying them.
func (b *namespacedBatch) Rollback() error { return b.inner.Rollback() }

// Close releases resources held by the batch.
func (b *namespacedBatch) Close() error { return b.inner.Close() }

// namespacedIterator strips the namespace prefix from keys.
type namespacedIterator struct {
	inner  storage.Iterator
	prefix []byte
}

// SeekToFirst positions the iterator at the first key.
func (it *namespacedIterator) SeekToFirst() bool { return it.inner.SeekToFirst() }

// SeekToLast positions the iterator at the last key.
func (it *namespacedIterator) SeekToLast() bool { return it.inner.SeekToLast() }

// Seek positions the iterator at the first key >= target within the namespace.
func (it *namespacedIterator) Seek(target []byte) bool {
	return it.inner.Seek(append(it.prefix, target...))
}

// Next advances the iterator to the next key.
func (it *namespacedIterator) Next() bool { return it.inner.Next() }

// Prev moves the iterator to the previous key.
func (it *namespacedIterator) Prev() bool { return it.inner.Prev() }

// Valid reports whether the iterator is positioned at a valid entry.
func (it *namespacedIterator) Valid() bool { return it.inner.Valid() }

// Key returns the current key with the namespace prefix stripped.
func (it *namespacedIterator) Key() []byte { return bytes.TrimPrefix(it.inner.Key(), it.prefix) }

// Value returns the current value.
func (it *namespacedIterator) Value() []byte { return it.inner.Value() }

// Error returns any accumulated error encountered during iteration.
func (it *namespacedIterator) Error() error { return it.inner.Error() }

// Close releases resources held by the iterator.
func (it *namespacedIterator) Close() error { return it.inner.Close() }

// namespacedSnapshot wraps a snapshot with namespace prefix.
type namespacedSnapshot struct {
	inner  storage.Snapshot
	prefix []byte
}

// Get retrieves a value by key from the snapshot with the namespace prefix.
func (s *namespacedSnapshot) Get(key []byte) ([]byte, error) {
	return s.inner.Get(append(s.prefix, key...))
}

// NewIterator creates an iterator over the snapshot scoped to the namespace.
func (s *namespacedSnapshot) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	nsOpts := &storage.IteratorOptions{}
	if opts != nil {
		nsOpts.Reverse = opts.Reverse
		nsOpts.OnlyKeys = opts.OnlyKeys
		if len(opts.LowerBound) > 0 {
			nsOpts.LowerBound = append(s.prefix, opts.LowerBound...)
		}
		if len(opts.UpperBound) > 0 {
			nsOpts.UpperBound = append(s.prefix, opts.UpperBound...)
		}
	}
	return &namespacedIterator{inner: s.inner.NewIterator(nsOpts), prefix: s.prefix}
}

// Close releases resources held by the snapshot.
func (s *namespacedSnapshot) Close() error { return s.inner.Close() }
