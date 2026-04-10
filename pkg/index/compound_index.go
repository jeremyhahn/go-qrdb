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

package index

import (
	"bytes"
	"context"
	"encoding/binary"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// CompoundIndex implements a multi-field index backed by storage.Storage.
// It extends the single-field StoragePebbleIndex pattern to support
// order-preserving compound keys for efficient prefix and range scans.
//
// Key format:
//
//	idx:{indexName}:{encoded_val1}{encoded_val2}...:{entityKey}{uint32BE(len(entityKey))}
//
// The compound value portion uses order-preserving encoding (see encoding.go)
// so that lexicographic byte comparison matches logical field ordering.
type CompoundIndex struct {
	storage storage.Storage
	config  IndexConfig
	logger  *logger.Logger
	metrics *storageIndexMetrics
}

// NewCompoundIndex creates a new compound index backed by the given storage.
func NewCompoundIndex(store storage.Storage, config IndexConfig, log *logger.Logger) (*CompoundIndex, error) {
	if store == nil {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.NewCompoundIndex",
			"storage is required",
		)
	}
	if log == nil {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.NewCompoundIndex",
			"logger is required",
		)
	}
	if config.Name == "" {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.NewCompoundIndex",
			"index name is required",
		)
	}
	if len(config.FieldNames) < 2 {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.NewCompoundIndex",
			"compound index requires at least 2 fields, got %d",
			len(config.FieldNames),
		)
	}

	idx := &CompoundIndex{
		storage: store,
		config:  config,
		logger:  log.WithField("compound_index", config.Name),
		metrics: newStorageIndexMetrics(),
	}

	idx.logger.Infof("Created compound index: %s (fields: %v)", config.Name, config.FieldNames)

	return idx, nil
}

// PutCompound adds or updates a compound index entry.
func (idx *CompoundIndex) PutCompound(entityKey []byte, fieldValues [][]byte) error {
	start := time.Now()
	defer func() {
		idx.metrics.putLatency.Observe(time.Since(start).Seconds())
	}()
	idx.metrics.putOps.Inc()

	normalizedValues := idx.normalizeFieldValues(fieldValues)

	compoundValue := EncodeCompoundValue(normalizedValues, idx.valueTypes())

	if idx.config.Unique {
		if err := idx.checkUniqueConstraint(entityKey, compoundValue); err != nil {
			idx.metrics.uniqueViolations.Inc()
			idx.metrics.errors.Inc()
			return err
		}
	}

	key := idx.buildKey(compoundValue, entityKey)

	if err := idx.storage.Set(key, []byte{}); err != nil {
		idx.metrics.errors.Inc()
		return errors.Wrapf(
			err,
			errors.ErrStorage,
			"index.CompoundIndex.PutCompound",
			"failed to store compound index entry",
		)
	}

	return nil
}

// PutCompoundBatch queues a compound index entry into the provided batch.
func (idx *CompoundIndex) PutCompoundBatch(batch storage.Batch, entityKey []byte, fieldValues [][]byte) error {
	start := time.Now()
	defer func() {
		idx.metrics.putLatency.Observe(time.Since(start).Seconds())
	}()
	idx.metrics.putOps.Inc()

	normalizedValues := idx.normalizeFieldValues(fieldValues)

	compoundValue := EncodeCompoundValue(normalizedValues, idx.valueTypes())

	if idx.config.Unique {
		if err := idx.checkUniqueConstraint(entityKey, compoundValue); err != nil {
			idx.metrics.uniqueViolations.Inc()
			idx.metrics.errors.Inc()
			return err
		}
	}

	key := idx.buildKey(compoundValue, entityKey)

	if err := batch.Set(key, []byte{}); err != nil {
		idx.metrics.errors.Inc()
		return errors.Wrapf(
			err,
			errors.ErrStorage,
			"index.CompoundIndex.PutCompoundBatch",
			"failed to queue compound index entry in batch",
		)
	}

	return nil
}

// GetCompound retrieves all entity keys matching an exact compound value.
func (idx *CompoundIndex) GetCompound(fieldValues [][]byte) ([][]byte, error) {
	start := time.Now()
	defer func() {
		idx.metrics.getLatency.Observe(time.Since(start).Seconds())
	}()
	idx.metrics.getOps.Inc()

	normalizedValues := idx.normalizeFieldValues(fieldValues)

	compoundValue := EncodeCompoundValue(normalizedValues, idx.valueTypes())

	prefix := idx.buildValuePrefix(compoundValue)

	iter := idx.storage.NewIterator(&storage.IteratorOptions{
		LowerBound: prefix,
		UpperBound: storageIncrementBytes(prefix),
		OnlyKeys:   true,
	})
	defer func() { _ = iter.Close() }()

	var entityKeys [][]byte
	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		entityKey, err := idx.extractEntityKey(iter.Key())
		if err != nil {
			idx.logger.Warnf("Failed to extract entity key: %v", err)
			continue
		}
		entityKeys = append(entityKeys, entityKey)
	}

	if err := iter.Error(); err != nil {
		idx.metrics.errors.Inc()
		return nil, errors.Wrapf(
			err,
			errors.ErrStorage,
			"index.CompoundIndex.GetCompound",
			"iterator error",
		)
	}

	return entityKeys, nil
}

// GetPrefix retrieves all entity keys matching a prefix of compound
// field values. For example, with a (ConversationID, Timestamp) index,
// passing only [conversationID] returns all messages in that conversation.
func (idx *CompoundIndex) GetPrefix(leadingValues [][]byte) ([][]byte, error) {
	start := time.Now()
	defer func() {
		idx.metrics.getLatency.Observe(time.Since(start).Seconds())
	}()
	idx.metrics.getOps.Inc()

	normalizedValues := idx.normalizeFieldValues(leadingValues)

	// Build the prefix types for only the leading fields.
	allTypes := idx.valueTypes()
	prefixTypes := allTypes
	if len(prefixTypes) > len(normalizedValues) {
		prefixTypes = allTypes[:len(normalizedValues)]
	}

	compoundPrefix := EncodeCompoundValue(normalizedValues, prefixTypes)

	// Use buildScanBound (no trailing ':') so the byte range covers all
	// compound values that start with the encoded leading fields.
	// buildValuePrefix would add a ':' separator which only matches keys
	// where the compound value ends exactly at this point, not partial prefixes.
	lowerBound := idx.buildScanBound(compoundPrefix)

	// Use the prefix as a scan bound — the compound encoding is
	// order-preserving, so all entries with these leading values will
	// sort contiguously.
	iter := idx.storage.NewIterator(&storage.IteratorOptions{
		LowerBound: lowerBound,
		UpperBound: storageIncrementBytes(lowerBound),
		OnlyKeys:   true,
	})
	defer func() { _ = iter.Close() }()

	var entityKeys [][]byte
	seen := make(map[string]struct{})

	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		entityKey, err := idx.extractEntityKey(iter.Key())
		if err != nil {
			idx.logger.Warnf("Failed to extract entity key: %v", err)
			continue
		}
		keyStr := string(entityKey)
		if _, exists := seen[keyStr]; !exists {
			entityKeys = append(entityKeys, entityKey)
			seen[keyStr] = struct{}{}
		}
	}

	if err := iter.Error(); err != nil {
		idx.metrics.errors.Inc()
		return nil, errors.Wrapf(
			err,
			errors.ErrStorage,
			"index.CompoundIndex.GetPrefix",
			"iterator error",
		)
	}

	return entityKeys, nil
}

// ScanCompound performs a prefix match on leading fields with a range
// scan on the next field. prefixValues are exact-match values for the
// leading N fields. rangeStart and rangeEnd define a half-open range
// [start, end) on field N+1.
func (idx *CompoundIndex) ScanCompound(prefixValues [][]byte, rangeStart, rangeEnd []byte) (map[string][][]byte, error) {
	startTime := time.Now()
	defer func() {
		idx.metrics.scanLatency.Observe(time.Since(startTime).Seconds())
	}()
	idx.metrics.scanOps.Inc()

	normalizedPrefix := idx.normalizeFieldValues(prefixValues)

	allTypes := idx.valueTypes()
	prefixTypes := allTypes
	if len(prefixTypes) > len(normalizedPrefix) {
		prefixTypes = allTypes[:len(normalizedPrefix)]
	}

	encodedPrefix := EncodeCompoundValue(normalizedPrefix, prefixTypes)

	// Determine the range field type.
	rangeFieldIdx := len(normalizedPrefix)
	rangeType := IndexValueTypeString
	if rangeFieldIdx < len(allTypes) {
		rangeType = allTypes[rangeFieldIdx]
	}

	// Build lower and upper bounds by appending the range values to
	// the encoded prefix.
	var lowerBound, upperBound []byte

	if rangeStart != nil {
		normalizedStart, _ := NormalizeFieldValue(rangeStart, IndexConfig{ValueType: rangeType})
		encodedStart := EncodeCompoundValue([][]byte{normalizedStart}, []IndexValueType{rangeType})
		combined := make([]byte, 0, len(encodedPrefix)+len(encodedStart))
		combined = append(combined, encodedPrefix...)
		combined = append(combined, encodedStart...)
		lowerBound = idx.buildScanBound(combined)
	} else {
		lowerBound = idx.buildScanBound(encodedPrefix)
	}

	if rangeEnd != nil {
		normalizedEnd, _ := NormalizeFieldValue(rangeEnd, IndexConfig{ValueType: rangeType})
		encodedEnd := EncodeCompoundValue([][]byte{normalizedEnd}, []IndexValueType{rangeType})
		combined := make([]byte, 0, len(encodedPrefix)+len(encodedEnd))
		combined = append(combined, encodedPrefix...)
		combined = append(combined, encodedEnd...)
		upperBound = idx.buildScanBound(combined)
	} else {
		upperBound = storageIncrementBytes(idx.buildScanBound(encodedPrefix))
	}

	iter := idx.storage.NewIterator(&storage.IteratorOptions{
		LowerBound: lowerBound,
		UpperBound: upperBound,
		OnlyKeys:   true,
	})
	defer func() { _ = iter.Close() }()

	result := make(map[string][][]byte)

	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		key := iter.Key()

		compoundValue, entityKey, err := idx.parseKey(key)
		if err != nil {
			idx.logger.Warnf("Failed to parse compound key: %v", err)
			continue
		}

		valueStr := string(compoundValue)
		result[valueStr] = append(result[valueStr], entityKey)
	}

	if err := iter.Error(); err != nil {
		idx.metrics.errors.Inc()
		return nil, errors.Wrapf(
			err,
			errors.ErrStorage,
			"index.CompoundIndex.ScanCompound",
			"iterator error",
		)
	}

	return result, nil
}

// DeleteCompound removes a compound index entry.
func (idx *CompoundIndex) DeleteCompound(entityKey []byte, fieldValues [][]byte) error {
	idx.metrics.deleteOps.Inc()

	normalizedValues := idx.normalizeFieldValues(fieldValues)

	compoundValue := EncodeCompoundValue(normalizedValues, idx.valueTypes())
	key := idx.buildKey(compoundValue, entityKey)

	if err := idx.storage.Delete(key); err != nil {
		idx.metrics.errors.Inc()
		return errors.Wrapf(
			err,
			errors.ErrStorage,
			"index.CompoundIndex.DeleteCompound",
			"failed to delete compound index entry",
		)
	}

	return nil
}

// DeleteCompoundBatch queues a compound index entry removal into the batch.
func (idx *CompoundIndex) DeleteCompoundBatch(batch storage.Batch, entityKey []byte, fieldValues [][]byte) error {
	idx.metrics.deleteOps.Inc()

	normalizedValues := idx.normalizeFieldValues(fieldValues)

	compoundValue := EncodeCompoundValue(normalizedValues, idx.valueTypes())
	key := idx.buildKey(compoundValue, entityKey)

	if err := batch.Delete(key); err != nil {
		idx.metrics.errors.Inc()
		return errors.Wrapf(
			err,
			errors.ErrStorage,
			"index.CompoundIndex.DeleteCompoundBatch",
			"failed to queue compound index deletion",
		)
	}

	return nil
}

// PutCompoundWithContext adds a compound index entry with context support.
func (idx *CompoundIndex) PutCompoundWithContext(ctx context.Context, entityKey []byte, fieldValues [][]byte) error {
	if err := ctx.Err(); err != nil {
		return errors.Wrap(err, errors.ErrCanceled, "index.CompoundIndex.PutCompoundWithContext")
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- idx.PutCompound(entityKey, fieldValues)
	}()

	select {
	case <-ctx.Done():
		return errors.Wrap(ctx.Err(), errors.ErrCanceled, "index.CompoundIndex.PutCompoundWithContext")
	case err := <-errCh:
		return err
	}
}

// GetCompoundWithContext retrieves entity keys with context support.
func (idx *CompoundIndex) GetCompoundWithContext(ctx context.Context, fieldValues [][]byte) ([][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, errors.Wrap(err, errors.ErrCanceled, "index.CompoundIndex.GetCompoundWithContext")
	}

	type result struct {
		keys [][]byte
		err  error
	}

	resultCh := make(chan result, 1)
	go func() {
		keys, err := idx.GetCompound(fieldValues)
		resultCh <- result{keys: keys, err: err}
	}()

	select {
	case <-ctx.Done():
		return nil, errors.Wrap(ctx.Err(), errors.ErrCanceled, "index.CompoundIndex.GetCompoundWithContext")
	case res := <-resultCh:
		return res.keys, res.err
	}
}

// Clear removes all entries from the compound index.
func (idx *CompoundIndex) Clear() error {
	prefix := idx.buildIndexPrefix()

	batch := idx.storage.Batch()
	defer func() { _ = batch.Close() }()

	iter := idx.storage.NewIterator(&storage.IteratorOptions{
		LowerBound: prefix,
		UpperBound: storageIncrementBytes(prefix),
		OnlyKeys:   true,
	})
	defer func() { _ = iter.Close() }()

	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		if err := batch.Delete(iter.Key()); err != nil {
			return errors.Wrapf(
				err,
				errors.ErrStorage,
				"index.CompoundIndex.Clear",
				"failed to delete entry",
			)
		}
	}

	if err := iter.Error(); err != nil {
		return errors.Wrapf(
			err,
			errors.ErrStorage,
			"index.CompoundIndex.Clear",
			"iterator error",
		)
	}

	return batch.Commit()
}

// Close releases resources held by the index.
func (idx *CompoundIndex) Close() error {
	return nil
}

// Config returns the index configuration.
func (idx *CompoundIndex) Config() IndexConfig {
	return idx.config
}

// valueTypes returns the per-field value types, falling back to the
// index-level ValueType for any fields without explicit types.
func (idx *CompoundIndex) valueTypes() []IndexValueType {
	if len(idx.config.ValueTypes) == len(idx.config.FieldNames) {
		return idx.config.ValueTypes
	}
	types := make([]IndexValueType, len(idx.config.FieldNames))
	for i := range types {
		if i < len(idx.config.ValueTypes) {
			types[i] = idx.config.ValueTypes[i]
		} else {
			types[i] = idx.config.ValueType
		}
	}
	return types
}

// normalizeFieldValues applies per-field normalization (case folding,
// numeric encoding) to the raw field values.
func (idx *CompoundIndex) normalizeFieldValues(values [][]byte) [][]byte {
	types := idx.valueTypes()
	normalized := make([][]byte, len(values))
	for i, v := range values {
		cfg := IndexConfig{
			ValueType:     IndexValueTypeString,
			CaseSensitive: idx.config.CaseSensitive,
		}
		if i < len(types) {
			cfg.ValueType = types[i]
		}
		nv, ok := NormalizeFieldValue(v, cfg)
		if !ok {
			normalized[i] = v
		} else {
			normalized[i] = nv
		}
	}
	return normalized
}

// checkUniqueConstraint verifies that the compound value is unique.
func (idx *CompoundIndex) checkUniqueConstraint(entityKey, compoundValue []byte) error {
	prefix := idx.buildValuePrefix(compoundValue)

	iter := idx.storage.NewIterator(&storage.IteratorOptions{
		LowerBound: prefix,
		UpperBound: storageIncrementBytes(prefix),
		OnlyKeys:   true,
	})
	defer func() { _ = iter.Close() }()

	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		existingKey, err := idx.extractEntityKey(iter.Key())
		if err != nil {
			continue
		}
		if !bytes.Equal(existingKey, entityKey) {
			return errors.Newf(
				errors.ErrConflict,
				"index.CompoundIndex.checkUniqueConstraint",
				"unique constraint violation on compound index %s",
				idx.config.Name,
			)
		}
	}

	return iter.Error()
}

// buildKey constructs the full compound index key.
// Format: "idx:{indexName}:{compoundValue}:{entityKey}{uint32BE(len(entityKey))}"
func (idx *CompoundIndex) buildKey(compoundValue []byte, entityKey []byte) []byte {
	prefixLen := len(keyPrefix) + len(idx.config.Name) + 1
	totalLen := prefixLen + len(compoundValue) + 1 + len(entityKey) + 4

	buf := make([]byte, 0, totalLen)
	buf = append(buf, keyPrefix...)
	buf = append(buf, idx.config.Name...)
	buf = append(buf, ':')
	buf = append(buf, compoundValue...)
	buf = append(buf, ':')
	buf = append(buf, entityKey...)

	keyLenBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(keyLenBytes, uint32(len(entityKey)))
	buf = append(buf, keyLenBytes...)

	return buf
}

// buildValuePrefix constructs the prefix for a specific compound value.
// Format: "idx:{indexName}:{compoundValue}:"
func (idx *CompoundIndex) buildValuePrefix(compoundValue []byte) []byte {
	buf := make([]byte, 0, len(keyPrefix)+len(idx.config.Name)+len(compoundValue)+2)
	buf = append(buf, keyPrefix...)
	buf = append(buf, idx.config.Name...)
	buf = append(buf, ':')
	buf = append(buf, compoundValue...)
	buf = append(buf, ':')
	return buf
}

// buildScanBound constructs a scan bound without trailing separator.
// Format: "idx:{indexName}:{compoundValue}"
func (idx *CompoundIndex) buildScanBound(compoundValue []byte) []byte {
	buf := make([]byte, 0, len(keyPrefix)+len(idx.config.Name)+len(compoundValue)+1)
	buf = append(buf, keyPrefix...)
	buf = append(buf, idx.config.Name...)
	buf = append(buf, ':')
	buf = append(buf, compoundValue...)
	return buf
}

// buildIndexPrefix constructs the prefix for the entire index.
// Format: "idx:{indexName}:"
func (idx *CompoundIndex) buildIndexPrefix() []byte {
	buf := make([]byte, 0, len(keyPrefix)+len(idx.config.Name)+1)
	buf = append(buf, keyPrefix...)
	buf = append(buf, idx.config.Name...)
	buf = append(buf, ':')
	return buf
}

// extractEntityKey extracts the entity key from a compound index key.
func (idx *CompoundIndex) extractEntityKey(key []byte) ([]byte, error) {
	if len(key) < 4 {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.CompoundIndex.extractEntityKey",
			"key too short",
		)
	}

	keyLen := binary.BigEndian.Uint32(key[len(key)-4:])

	if uint32(len(key)) < keyLen+4 {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.CompoundIndex.extractEntityKey",
			"key too short for encoded entity key length",
		)
	}

	entityKey := make([]byte, keyLen)
	copy(entityKey, key[len(key)-4-int(keyLen):len(key)-4])

	return entityKey, nil
}

// parseKey parses a compound index key into compound value and entity key.
func (idx *CompoundIndex) parseKey(key []byte) (compoundValue []byte, entityKey []byte, err error) {
	prefix := idx.buildIndexPrefix()
	if !bytes.HasPrefix(key, prefix) {
		return nil, nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.CompoundIndex.parseKey",
			"key does not match index prefix",
		)
	}

	remaining := key[len(prefix):]

	if len(remaining) < 5 {
		return nil, nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.CompoundIndex.parseKey",
			"key too short",
		)
	}

	keyLen := int(binary.BigEndian.Uint32(remaining[len(remaining)-4:]))

	if len(remaining) < keyLen+4+1 {
		return nil, nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.CompoundIndex.parseKey",
			"key too short for encoded entity key length",
		)
	}

	entityKey = make([]byte, keyLen)
	copy(entityKey, remaining[len(remaining)-4-keyLen:len(remaining)-4])

	sepIdx := len(remaining) - 4 - keyLen - 1
	if remaining[sepIdx] != ':' {
		return nil, nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.CompoundIndex.parseKey",
			"missing separator in compound key",
		)
	}

	compoundValue = remaining[:sepIdx]

	return compoundValue, entityKey, nil
}
