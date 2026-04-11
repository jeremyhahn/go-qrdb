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

// vectorBatch implements the Batch interface for vector storage.
type vectorBatch struct {
	batch   *pebble.Batch
	db      *pebble.DB
	count   int
	metrics *vectorMetrics
}

// newVectorBatch creates a new vector batch.
func newVectorBatch(db *pebble.DB, metrics *vectorMetrics) *vectorBatch {
	return &vectorBatch{
		batch:   db.NewBatch(),
		db:      db,
		metrics: metrics,
	}
}

// UpsertVector adds a vector upsert to the batch.
func (b *vectorBatch) UpsertVector(namespace string, vec *VectorValue) error {
	if vec == nil {
		return ErrInvalidVector
	}

	// Encode vector value
	value, err := schema.EncodeVectorValue(toSchemaVectorValue(vec))
	if err != nil {
		return err
	}

	// Write primary vector key
	vectorKey := schema.EncodeVectorKey(namespace, vec.ID, vec.Version)
	if err := b.batch.Set(vectorKey, value, pebble.NoSync); err != nil {
		return err
	}

	// Create metadata indexes
	for field, fieldValue := range vec.Metadata {
		metaKey := schema.EncodeMetadataKey(namespace, field, fieldValue, vec.ID, vec.Version)
		if err := b.batch.Set(metaKey, []byte{}, pebble.NoSync); err != nil {
			return err
		}

		// Create reverse index
		reverseKey := schema.EncodeReverseKey(namespace, vec.ID, field, vec.Version)
		reverseValue := []byte(fieldValue)
		if err := b.batch.Set(reverseKey, reverseValue, pebble.NoSync); err != nil {
			return err
		}
	}

	b.count++
	return nil
}

// DeleteVector adds a vector deletion to the batch.
func (b *vectorBatch) DeleteVector(namespace string, id uint64) error {
	if id == 0 {
		return ErrInvalidID
	}

	// Get the current version to delete all metadata indexes
	vectorKeyPrefix := schema.EncodeVectorKeyPrefix(namespace, id)
	iter, err := b.db.NewIter(&pebble.IterOptions{
		LowerBound: vectorKeyPrefix,
		UpperBound: schema.PrefixSuccessor(vectorKeyPrefix),
	})
	if err != nil {
		return err
	}
	defer func() { _ = iter.Close() }()

	// Find all versions
	for iter.First(); iter.Valid(); iter.Next() {
		key := iter.Key()
		_, _, version, err := schema.DecodeVectorKey(key)
		if err != nil {
			continue
		}

		// Delete vector key
		vectorKey := schema.EncodeVectorKey(namespace, id, version)
		if err := b.batch.Delete(vectorKey, pebble.NoSync); err != nil {
			return err
		}

		// Delete metadata indexes using reverse index
		reversePrefix := schema.EncodeReverseKeyPrefix(namespace, id)
		reverseIter, reverseErr := b.db.NewIter(&pebble.IterOptions{
			LowerBound: reversePrefix,
			UpperBound: schema.PrefixSuccessor(reversePrefix),
		})
		if reverseErr != nil {
			continue
		}

		for reverseIter.First(); reverseIter.Valid(); reverseIter.Next() {
			_, _, field, ver, err := schema.DecodeReverseKey(reverseIter.Key())
			if err != nil {
				continue
			}

			if ver != version {
				continue
			}

			// Get the field value
			fieldValue := string(reverseIter.Value())

			// Delete metadata index
			metaKey := schema.EncodeMetadataKey(namespace, field, fieldValue, id, version)
			if err := b.batch.Delete(metaKey, pebble.NoSync); err != nil {
				_ = reverseIter.Close() //nolint:gosec // G104: Error deliberately ignored - iterator cleanup
				return err
			}

			// Delete reverse index
			reverseKey := schema.EncodeReverseKey(namespace, id, field, version)
			if err := b.batch.Delete(reverseKey, pebble.NoSync); err != nil {
				_ = reverseIter.Close() //nolint:gosec // G104: Error deliberately ignored - iterator cleanup
				return err
			}
		}
		_ = reverseIter.Close() //nolint:gosec // G104: Error deliberately ignored - iterator cleanup
	}

	b.count++
	return nil
}

// Commit atomically commits all operations.
func (b *vectorBatch) Commit() error {
	if b.count == 0 {
		return ErrEmptyBatch
	}

	err := b.batch.Commit(pebble.NoSync)
	if err == nil && b.metrics != nil {
		b.metrics.batchCommits.Inc()
	}
	return err
}

// Reset clears all pending operations.
func (b *vectorBatch) Reset() {
	b.batch.Reset()
	b.count = 0
}

// Close releases batch resources.
func (b *vectorBatch) Close() error {
	return b.batch.Close()
}

// Count returns the number of pending operations.
func (b *vectorBatch) Count() int {
	return b.count
}
