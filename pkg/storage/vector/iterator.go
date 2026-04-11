// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package vector

import (
	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector/schema"
)

// vectorIterator implements the Iterator interface for vectors.
type vectorIterator struct {
	iter      *pebble.Iterator
	err       error
	current   *schema.VectorValue
	namespace string
	id        uint64
	version   uint64
}

// newVectorIterator creates a new vector iterator.
func newVectorIterator(db *pebble.DB, prefix []byte) *vectorIterator {
	opts := &pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: schema.PrefixSuccessor(prefix),
	}

	iter, err := db.NewIter(opts)
	if err != nil {
		return &vectorIterator{
			iter: nil,
			err:  err,
		}
	}

	return &vectorIterator{
		iter: iter,
	}
}

// Next advances to the next vector.
func (it *vectorIterator) Next() bool {
	if it.err != nil || it.iter == nil {
		return false
	}

	if !it.iter.Valid() {
		if !it.iter.First() {
			return false
		}
	} else {
		if !it.iter.Next() {
			return false
		}
	}

	if !it.iter.Valid() {
		return false
	}

	// Decode key
	key := it.iter.Key()
	namespace, id, version, err := schema.DecodeVectorKey(key)
	if err != nil {
		it.err = err
		return false
	}

	it.namespace = namespace
	it.id = id
	it.version = version

	// Decode value
	value := it.iter.Value()
	vec, err := schema.DecodeVectorValue(value)
	if err != nil {
		it.err = err
		return false
	}

	it.current = vec

	return true
}

// Value returns the current vector.
func (it *vectorIterator) Value() (*schema.VectorValue, error) {
	if it.err != nil {
		return nil, it.err
	}
	if it.current == nil {
		return nil, ErrVectorNotFound
	}
	return it.current, nil
}

// Key returns the current key (namespace, id, version).
func (it *vectorIterator) Key() (namespace string, id uint64, version uint64) {
	return it.namespace, it.id, it.version
}

// Err returns any iteration error.
func (it *vectorIterator) Err() error {
	if it.err != nil {
		return it.err
	}
	if it.iter != nil {
		return it.iter.Error()
	}
	return nil
}

// Close releases iterator resources.
func (it *vectorIterator) Close() error {
	if it.iter != nil {
		return it.iter.Close()
	}
	return nil
}

// metadataIterator implements iteration over metadata indexes.
type metadataIterator struct {
	iter      *pebble.Iterator
	db        *pebble.DB
	err       error
	current   *schema.VectorValue
	namespace string
	field     string
	value     string
	id        uint64
	version   uint64
}

// newMetadataIterator creates a new metadata iterator.
func newMetadataIterator(db *pebble.DB, prefix []byte) *metadataIterator {
	opts := &pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: schema.PrefixSuccessor(prefix),
	}

	iter, err := db.NewIter(opts)
	if err != nil {
		return &metadataIterator{
			iter: nil,
			db:   db,
			err:  err,
		}
	}

	return &metadataIterator{
		iter: iter,
		db:   db,
	}
}

// Next advances to the next metadata entry and fetches the corresponding vector.
func (it *metadataIterator) Next() bool {
	if it.err != nil || it.iter == nil {
		return false
	}

	if !it.iter.Valid() {
		if !it.iter.First() {
			return false
		}
	} else {
		if !it.iter.Next() {
			return false
		}
	}

	if !it.iter.Valid() {
		return false
	}

	// Decode metadata key
	key := it.iter.Key()
	namespace, field, value, id, version, err := schema.DecodeMetadataKey(key)
	if err != nil {
		it.err = err
		return false
	}

	it.namespace = namespace
	it.field = field
	it.value = value
	it.id = id
	it.version = version

	// Fetch the actual vector using the ID and version
	vectorKey := schema.EncodeVectorKey(namespace, id, version)
	vectorValue, closer, err := it.db.Get(vectorKey)
	if err != nil {
		it.err = err
		return false
	}
	defer func() { _ = closer.Close() }()

	// Decode vector
	vec, err := schema.DecodeVectorValue(vectorValue)
	if err != nil {
		it.err = err
		return false
	}

	it.current = vec

	return true
}

// Value returns the current vector.
func (it *metadataIterator) Value() (*schema.VectorValue, error) {
	if it.err != nil {
		return nil, it.err
	}
	if it.current == nil {
		return nil, ErrVectorNotFound
	}
	return it.current, nil
}

// Key returns the current key (namespace, id, version).
func (it *metadataIterator) Key() (namespace string, id uint64, version uint64) {
	return it.namespace, it.id, it.version
}

// Err returns any iteration error.
func (it *metadataIterator) Err() error {
	if it.err != nil {
		return it.err
	}
	if it.iter != nil {
		return it.iter.Error()
	}
	return nil
}

// Close releases iterator resources.
func (it *metadataIterator) Close() error {
	if it.iter != nil {
		return it.iter.Close()
	}
	return nil
}
