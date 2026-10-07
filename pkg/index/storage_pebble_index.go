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
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/util/metrics"
)

// StoragePebbleIndex implements the Index and IndexWithContext interfaces
// using the storage.Storage abstraction. It supports case-insensitive
// indexing, unique constraints, and Prometheus metrics.
//
// Key format: "idx:{indexName}:{fieldValue}:{entityKey}{uint32BE(len(entityKey))}" -> []byte{}
// The last 4 bytes encode the entity key length, enabling variable-length keys.
// This format allows efficient range scans by field value.
type StoragePebbleIndex struct {
	storage storage.Storage
	config  IndexConfig
	logger  *logger.Logger
	metrics *storageIndexMetrics
}

// storageIndexMetrics holds Prometheus metrics for index operations.
type storageIndexMetrics struct {
	putOps           *metrics.Counter
	getOps           *metrics.Counter
	deleteOps        *metrics.Counter
	scanOps          *metrics.Counter
	putLatency       *metrics.Histogram
	getLatency       *metrics.Histogram
	scanLatency      *metrics.Histogram
	errors           *metrics.Counter
	uniqueViolations *metrics.Counter
}

var (
	globalStorageIndexMetrics *storageIndexMetrics
	storageIndexMetricsOnce   sync.Once
)

// newStorageIndexMetrics creates and registers index metrics.
func newStorageIndexMetrics() *storageIndexMetrics {
	storageIndexMetricsOnce.Do(func() {
		globalStorageIndexMetrics = createStorageIndexMetrics()
	})
	return globalStorageIndexMetrics
}

// createStorageIndexMetrics creates the actual metrics instances.
func createStorageIndexMetrics() *storageIndexMetrics {
	return &storageIndexMetrics{
		putOps: metrics.NewCounter(metrics.CounterOpts{
			Namespace: "qrdb",
			Subsystem: "index",
			Name:      "put_operations_total",
			Help:      "Total number of index Put operations",
		}),
		getOps: metrics.NewCounter(metrics.CounterOpts{
			Namespace: "qrdb",
			Subsystem: "index",
			Name:      "get_operations_total",
			Help:      "Total number of index Get operations",
		}),
		deleteOps: metrics.NewCounter(metrics.CounterOpts{
			Namespace: "qrdb",
			Subsystem: "index",
			Name:      "delete_operations_total",
			Help:      "Total number of index Delete operations",
		}),
		scanOps: metrics.NewCounter(metrics.CounterOpts{
			Namespace: "qrdb",
			Subsystem: "index",
			Name:      "scan_operations_total",
			Help:      "Total number of index Scan operations",
		}),
		putLatency: metrics.NewHistogram(metrics.HistogramOpts{
			Namespace: "qrdb",
			Subsystem: "index",
			Name:      "put_duration_seconds",
			Help:      "Histogram of index Put operation latencies",
			Buckets:   []float64{.0001, .0005, .001, .005, .01, .05, .1, .5, 1},
		}),
		getLatency: metrics.NewHistogram(metrics.HistogramOpts{
			Namespace: "qrdb",
			Subsystem: "index",
			Name:      "get_duration_seconds",
			Help:      "Histogram of index Get operation latencies",
			Buckets:   []float64{.0001, .0005, .001, .005, .01, .05, .1, .5, 1},
		}),
		scanLatency: metrics.NewHistogram(metrics.HistogramOpts{
			Namespace: "qrdb",
			Subsystem: "index",
			Name:      "scan_duration_seconds",
			Help:      "Histogram of index Scan operation latencies",
			Buckets:   []float64{.001, .005, .01, .05, .1, .5, 1, 5, 10},
		}),
		errors: metrics.NewCounter(metrics.CounterOpts{
			Namespace: "qrdb",
			Subsystem: "index",
			Name:      "errors_total",
			Help:      "Total number of index errors",
		}),
		uniqueViolations: metrics.NewCounter(metrics.CounterOpts{
			Namespace: "qrdb",
			Subsystem: "index",
			Name:      "unique_violations_total",
			Help:      "Total number of unique constraint violations",
		}),
	}
}

// NewStoragePebbleIndex creates a new storage-backed index.
func NewStoragePebbleIndex(storage storage.Storage, config IndexConfig, log *logger.Logger) (*StoragePebbleIndex, error) {
	if storage == nil {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.NewStoragePebbleIndex",
			"storage is required",
		)
	}

	if log == nil {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.NewStoragePebbleIndex",
			"logger is required",
		)
	}

	if config.Name == "" {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.NewStoragePebbleIndex",
			"index name is required",
		)
	}

	idx := &StoragePebbleIndex{
		storage: storage,
		config:  config,
		logger:  log.WithField("index", config.Name),
		metrics: newStorageIndexMetrics(),
	}

	idx.logger.Infof("Created %s index: %s", config.Type, config.Name)

	return idx, nil
}

// Put adds or updates an index entry. For numeric indexes, fieldValue is
// encoded as a fixed-width 8-byte big-endian integer. Non-numeric values
// are silently skipped.
func (idx *StoragePebbleIndex) Put(entityKey []byte, fieldValue []byte) error {
	start := time.Now()
	defer func() {
		idx.metrics.putLatency.Observe(time.Since(start).Seconds())
	}()

	idx.metrics.putOps.Inc()

	// Normalize field value if needed
	normalizedValue, ok := NormalizeFieldValue(fieldValue, idx.config)
	if !ok {
		return nil // non-numeric value; skip silently
	}

	// Check unique constraint
	if idx.config.Unique {
		if err := idx.checkUniqueConstraint(entityKey, normalizedValue); err != nil {
			idx.metrics.uniqueViolations.Inc()
			idx.metrics.errors.Inc()
			return err
		}
	}

	// Build index key
	key := idx.buildKey(normalizedValue, entityKey)

	// Store in PebbleDB (empty value, key contains all info)
	if err := idx.storage.Set(key, []byte{}); err != nil {
		idx.metrics.errors.Inc()
		return errors.Wrapf(
			err,
			errors.ErrStorage,
			"index.StoragePebbleIndex.Put",
			"failed to store index entry",
		)
	}

	return nil
}

// Get retrieves all entity keys for the given field value. For numeric
// indexes, fieldValue is encoded before the lookup. The read is against
// committed storage; use GetInBatch to also see an in-flight batch's own
// pending writes.
func (idx *StoragePebbleIndex) Get(fieldValue []byte) ([][]byte, error) {
	return idx.getFrom(idx.storage, fieldValue, "index.StoragePebbleIndex.Get")
}

// GetInBatch retrieves all entity keys for the given field value, reading
// through batch rather than through committed storage. An apply loop packs a
// variable number of committed entries into one storage batch; a uniqueness
// check for entry N+1 that read committed storage would not see entry N's
// index write when the two land in the same batch (storage.Reader).
func (idx *StoragePebbleIndex) GetInBatch(batch storage.Batch, fieldValue []byte) ([][]byte, error) {
	return idx.getFrom(batch, fieldValue, "index.StoragePebbleIndex.GetInBatch")
}

// getFrom is Get and GetInBatch's shared body, reading through whichever
// storage.Reader the caller supplies -- committed storage or a live batch.
func (idx *StoragePebbleIndex) getFrom(reader storage.Reader, fieldValue []byte, op string) ([][]byte, error) {
	start := time.Now()
	defer func() {
		idx.metrics.getLatency.Observe(time.Since(start).Seconds())
	}()

	idx.metrics.getOps.Inc()

	// Normalize field value if needed
	normalizedValue, ok := NormalizeFieldValue(fieldValue, idx.config)
	if !ok {
		return nil, nil // non-numeric value; nothing to find
	}

	// Build prefix for this field value
	prefix := idx.buildValuePrefix(normalizedValue)

	// Scan all keys with this prefix
	iter := reader.NewIterator(&storage.IteratorOptions{
		LowerBound: prefix,
		UpperBound: storageIncrementBytes(prefix),
		OnlyKeys:   true, // We only need keys, not values
	})
	defer func() { _ = iter.Close() }()

	var entityKeys [][]byte

	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		key := iter.Key()

		// Extract entity key from key
		entityKey, err := idx.extractEntityKey(key)
		if err != nil {
			idx.logger.Warnf("Failed to extract entity key from key: %v", err)
			continue
		}

		entityKeys = append(entityKeys, entityKey)
	}

	if err := iter.Error(); err != nil {
		idx.metrics.errors.Inc()
		return nil, errors.Wrapf(
			err,
			errors.ErrStorage,
			op,
			"iterator error",
		)
	}

	return entityKeys, nil
}

// Delete removes an index entry. For numeric indexes, fieldValue is encoded
// before building the delete key.
func (idx *StoragePebbleIndex) Delete(entityKey []byte, fieldValue []byte) error {
	idx.metrics.deleteOps.Inc()

	// Normalize field value if needed
	normalizedValue, ok := NormalizeFieldValue(fieldValue, idx.config)
	if !ok {
		return nil // non-numeric value; nothing to delete
	}

	// Build index key
	key := idx.buildKey(normalizedValue, entityKey)

	// Delete from PebbleDB
	if err := idx.storage.Delete(key); err != nil {
		idx.metrics.errors.Inc()
		return errors.Wrapf(
			err,
			errors.ErrStorage,
			"index.StoragePebbleIndex.Delete",
			"failed to delete index entry",
		)
	}

	return nil
}

// Scan retrieves all index entries within the given range. For numeric
// indexes, start and end are encoded before use and result map keys are
// decoded back to decimal strings. A nil start or end means unbounded on
// that side. Returns an InvalidNumericBoundError if a non-nil bound cannot
// be parsed as a number on a numeric index; an unparseable bound is a
// caller error and must never be reported as an empty result.
func (idx *StoragePebbleIndex) Scan(start, end []byte) (map[string][][]byte, error) {
	startTime := time.Now()
	defer func() {
		idx.metrics.scanLatency.Observe(time.Since(startTime).Seconds())
	}()

	idx.metrics.scanOps.Inc()

	// Resolve field values if needed
	var normalizedStart, normalizedEnd []byte
	if start != nil {
		ns, err := idx.resolveScanBound(start, "scan start")
		if err != nil {
			idx.metrics.errors.Inc()
			return nil, err
		}
		normalizedStart = ns
	}
	if end != nil {
		ne, err := idx.resolveScanBound(end, "scan end")
		if err != nil {
			idx.metrics.errors.Inc()
			return nil, err
		}
		normalizedEnd = ne
	}

	// Build range bounds. For correct lexicographic ordering the lower bound
	// must NOT include a trailing ':' separator, because ':' (0x3A) sorts
	// after digits (0x30-0x39). Using the value without a trailing separator
	// ensures that "idx:<name>:<start>" sorts before any key whose field
	// value extends beyond <start> (e.g. "idx:<name>:<start>23:<entity>").
	// The upper bound uses the same approach so that the exclusive end
	// correctly excludes values >= end.
	var lowerBound, upperBound []byte

	if normalizedStart != nil {
		lowerBound = idx.buildScanBound(normalizedStart)
	} else {
		lowerBound = idx.buildIndexPrefix()
	}

	if normalizedEnd != nil {
		upperBound = idx.buildScanBound(normalizedEnd)
	} else {
		upperBound = storageIncrementBytes(idx.buildIndexPrefix())
	}

	// Create iterator
	iter := idx.storage.NewIterator(&storage.IteratorOptions{
		LowerBound: lowerBound,
		UpperBound: upperBound,
		OnlyKeys:   true,
	})
	defer func() { _ = iter.Close() }()

	result := make(map[string][][]byte)

	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		key := iter.Key()

		// Extract field value and entity key
		fieldValue, entityKey, err := idx.parseKey(key)
		if err != nil {
			idx.logger.Warnf("Failed to parse key: %v", err)
			continue
		}

		// Decode the stored field value back to a human-readable string.
		fieldValueStr := DecodeFieldValue(fieldValue, idx.config)
		result[fieldValueStr] = append(result[fieldValueStr], entityKey)
	}

	if err := iter.Error(); err != nil {
		idx.metrics.errors.Inc()
		return nil, errors.Wrapf(
			err,
			errors.ErrStorage,
			"index.StoragePebbleIndex.Scan",
			"iterator error",
		)
	}

	return result, nil
}

// GetAll retrieves all entity keys in the index.
func (idx *StoragePebbleIndex) GetAll() ([][]byte, error) {
	prefix := idx.buildIndexPrefix()

	iter := idx.storage.NewIterator(&storage.IteratorOptions{
		LowerBound: prefix,
		UpperBound: storageIncrementBytes(prefix),
		OnlyKeys:   true,
	})
	defer func() { _ = iter.Close() }()

	var entityKeys [][]byte
	seen := make(map[string]struct{})

	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		key := iter.Key()

		entityKey, err := idx.extractEntityKey(key)
		if err != nil {
			continue
		}

		// Avoid duplicates
		keyStr := string(entityKey)
		if _, exists := seen[keyStr]; !exists {
			entityKeys = append(entityKeys, entityKey)
			seen[keyStr] = struct{}{}
		}
	}

	if err := iter.Error(); err != nil {
		return nil, errors.Wrapf(
			err,
			errors.ErrStorage,
			"index.StoragePebbleIndex.GetAll",
			"iterator error",
		)
	}

	return entityKeys, nil
}

// Clear removes all entries from the index.
func (idx *StoragePebbleIndex) Clear() error {
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
				"index.StoragePebbleIndex.Clear",
				"failed to delete entry",
			)
		}
	}

	if err := iter.Error(); err != nil {
		return errors.Wrapf(
			err,
			errors.ErrStorage,
			"index.StoragePebbleIndex.Clear",
			"iterator error",
		)
	}

	if err := batch.Commit(); err != nil {
		return errors.Wrapf(
			err,
			errors.ErrStorage,
			"index.StoragePebbleIndex.Clear",
			"failed to commit batch",
		)
	}

	return nil
}

// Close releases resources held by the index.
func (idx *StoragePebbleIndex) Close() error {
	// StoragePebbleIndex doesn't own the storage, so nothing to close
	return nil
}

// PutWithContext adds or updates an index entry with context support.
func (idx *StoragePebbleIndex) PutWithContext(ctx context.Context, entityKey []byte, fieldValue []byte) error {
	if err := ctx.Err(); err != nil {
		return errors.Wrap(err, errors.ErrCanceled, "index.StoragePebbleIndex.PutWithContext")
	}

	// Use goroutine for cancellation support
	errCh := make(chan error, 1)
	go func() {
		errCh <- idx.Put(entityKey, fieldValue)
	}()

	select {
	case <-ctx.Done():
		return errors.Wrap(ctx.Err(), errors.ErrCanceled, "index.StoragePebbleIndex.PutWithContext")
	case err := <-errCh:
		return err
	}
}

// GetWithContext retrieves entity keys with context support.
func (idx *StoragePebbleIndex) GetWithContext(ctx context.Context, fieldValue []byte) ([][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, errors.Wrap(err, errors.ErrCanceled, "index.StoragePebbleIndex.GetWithContext")
	}

	type result struct {
		ids [][]byte
		err error
	}

	resultCh := make(chan result, 1)
	go func() {
		ids, err := idx.Get(fieldValue)
		resultCh <- result{ids: ids, err: err}
	}()

	select {
	case <-ctx.Done():
		return nil, errors.Wrap(ctx.Err(), errors.ErrCanceled, "index.StoragePebbleIndex.GetWithContext")
	case res := <-resultCh:
		return res.ids, res.err
	}
}

// DeleteWithContext removes an index entry with context support.
func (idx *StoragePebbleIndex) DeleteWithContext(ctx context.Context, entityKey []byte, fieldValue []byte) error {
	if err := ctx.Err(); err != nil {
		return errors.Wrap(err, errors.ErrCanceled, "index.StoragePebbleIndex.DeleteWithContext")
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- idx.Delete(entityKey, fieldValue)
	}()

	select {
	case <-ctx.Done():
		return errors.Wrap(ctx.Err(), errors.ErrCanceled, "index.StoragePebbleIndex.DeleteWithContext")
	case err := <-errCh:
		return err
	}
}

// ScanWithContext performs a range scan with context support.
func (idx *StoragePebbleIndex) ScanWithContext(ctx context.Context, start, end []byte) (map[string][][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, errors.Wrap(err, errors.ErrCanceled, "index.StoragePebbleIndex.ScanWithContext")
	}

	type result struct {
		data map[string][][]byte
		err  error
	}

	resultCh := make(chan result, 1)
	go func() {
		data, err := idx.Scan(start, end)
		resultCh <- result{data: data, err: err}
	}()

	select {
	case <-ctx.Done():
		return nil, errors.Wrap(ctx.Err(), errors.ErrCanceled, "index.StoragePebbleIndex.ScanWithContext")
	case res := <-resultCh:
		return res.data, res.err
	}
}

// resolveScanBound resolves a Scan boundary value for this index's
// configuration. For non-numeric indexes this defers to
// NormalizeFieldValue, which passes string values through unchanged (or
// lowercases them for case-insensitive indexes) — this also correctly
// preserves the trailing-0x00 exclusivity marker that
// pkg/sql/provider/range_translator.go appends to byte-comparable string
// bounds. For numeric indexes the marker must instead be resolved after
// encoding; see resolveNumericScanBound.
func (idx *StoragePebbleIndex) resolveScanBound(raw []byte, op string) ([]byte, error) {
	if idx.config.ValueType != IndexValueTypeNumeric {
		normalized, _ := NormalizeFieldValue(raw, idx.config)
		return normalized, nil
	}
	return resolveNumericScanBound(raw, idx.config.Name, op)
}

// checkUniqueConstraint verifies that the field value is unique, against
// committed storage.
func (idx *StoragePebbleIndex) checkUniqueConstraint(entityKey []byte, fieldValue []byte) error {
	return idx.checkUniqueConstraintFrom(idx.storage, entityKey, fieldValue)
}

// checkUniqueConstraintInBatch is checkUniqueConstraint read through batch,
// so it also sees an earlier entry of the same apply batch's pending write.
func (idx *StoragePebbleIndex) checkUniqueConstraintInBatch(batch storage.Batch, entityKey []byte, fieldValue []byte) error {
	return idx.checkUniqueConstraintFrom(batch, entityKey, fieldValue)
}

// checkUniqueConstraintFrom is the shared body of checkUniqueConstraint and
// checkUniqueConstraintInBatch.
func (idx *StoragePebbleIndex) checkUniqueConstraintFrom(reader storage.Reader, entityKey []byte, fieldValue []byte) error {
	existingKeys, err := idx.getFrom(reader, fieldValue, "index.StoragePebbleIndex.checkUniqueConstraint")
	if err != nil {
		return err
	}

	// Allow update of same entity
	for _, id := range existingKeys {
		if !bytes.Equal(id, entityKey) {
			return errors.Newf(
				errors.ErrConflict,
				"index.StoragePebbleIndex.checkUniqueConstraint",
				"unique constraint violation: field value already exists",
			)
		}
	}

	return nil
}

// buildKey constructs the full index key.
// Format: "idx:{indexName}:{fieldValue}:{entityKey}{uint32BE(len(entityKey))}".
func (idx *StoragePebbleIndex) buildKey(fieldValue []byte, entityKey []byte) []byte {
	// Pre-allocate buffer to avoid multiple allocations
	prefixLen := len(keyPrefix) + len(idx.config.Name) + 1
	valueLen := len(fieldValue)
	totalLen := prefixLen + valueLen + 1 + len(entityKey) + 4 // +1 for separator, +4 for key length suffix

	buf := make([]byte, 0, totalLen)
	buf = append(buf, keyPrefix...)
	buf = append(buf, idx.config.Name...)
	buf = append(buf, ':')
	buf = append(buf, fieldValue...)
	buf = append(buf, ':')

	// Append entity key followed by its length as uint32 big-endian
	buf = append(buf, entityKey...)
	keyLenBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(keyLenBytes, uint32(len(entityKey)))
	buf = append(buf, keyLenBytes...)

	return buf
}

// buildValuePrefix constructs the prefix for a specific field value.
// Format: "idx:{indexName}:{fieldValue}:".
func (idx *StoragePebbleIndex) buildValuePrefix(fieldValue []byte) []byte {
	buf := make([]byte, 0, len(keyPrefix)+len(idx.config.Name)+len(fieldValue)+2)
	buf = append(buf, keyPrefix...)
	buf = append(buf, idx.config.Name...)
	buf = append(buf, ':')
	buf = append(buf, fieldValue...)
	buf = append(buf, ':')
	return buf
}

// buildScanBound constructs a range bound for scan operations without
// a trailing ':' separator. This ensures correct lexicographic ordering
// where the bound sorts before any key that extends the value prefix.
// Format: "idx:{indexName}:{fieldValue}".
func (idx *StoragePebbleIndex) buildScanBound(fieldValue []byte) []byte {
	buf := make([]byte, 0, len(keyPrefix)+len(idx.config.Name)+len(fieldValue)+1)
	buf = append(buf, keyPrefix...)
	buf = append(buf, idx.config.Name...)
	buf = append(buf, ':')
	buf = append(buf, fieldValue...)
	return buf
}

// buildIndexPrefix constructs the prefix for the entire index.
// Format: "idx:{indexName}:".
func (idx *StoragePebbleIndex) buildIndexPrefix() []byte {
	buf := make([]byte, 0, len(keyPrefix)+len(idx.config.Name)+1)
	buf = append(buf, keyPrefix...)
	buf = append(buf, idx.config.Name...)
	buf = append(buf, ':')
	return buf
}

// extractEntityKey extracts the entity key from an index key.
// The last 4 bytes encode the entity key length as uint32 big-endian.
func (idx *StoragePebbleIndex) extractEntityKey(key []byte) ([]byte, error) {
	// Need at least 4 bytes for the length suffix
	if len(key) < 4 {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.StoragePebbleIndex.extractEntityKey",
			"key too short",
		)
	}

	// Read entity key length from last 4 bytes
	keyLen := binary.BigEndian.Uint32(key[len(key)-4:])

	// Validate key has enough bytes: keyLen + 4 (length suffix)
	if uint32(len(key)) < keyLen+4 {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.StoragePebbleIndex.extractEntityKey",
			"key too short for encoded entity key length",
		)
	}

	// Extract entity key and return a copy
	entityKey := make([]byte, keyLen)
	copy(entityKey, key[len(key)-4-int(keyLen):len(key)-4])

	return entityKey, nil
}

// parseKey parses an index key into field value and entity key.
func (idx *StoragePebbleIndex) parseKey(key []byte) (fieldValue []byte, entityKey []byte, err error) {
	// Key format: "idx:{indexName}:{fieldValue}:{entityKey}{uint32BE(len(entityKey))}"

	// Find the index name prefix
	prefix := idx.buildIndexPrefix()
	if !bytes.HasPrefix(key, prefix) {
		return nil, nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.StoragePebbleIndex.parseKey",
			"key does not match index prefix",
		)
	}

	// Remove prefix
	remaining := key[len(prefix):]

	// Need at least 4 bytes for the key length suffix + 1 byte for separator
	if len(remaining) < 5 {
		return nil, nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.StoragePebbleIndex.parseKey",
			"key too short",
		)
	}

	// Read entity key length from last 4 bytes
	keyLen := int(binary.BigEndian.Uint32(remaining[len(remaining)-4:]))

	// Validate: remaining must have at least keyLen + 4 (length suffix) + 1 (separator)
	if len(remaining) < keyLen+4+1 {
		return nil, nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.StoragePebbleIndex.parseKey",
			"key too short for encoded entity key length",
		)
	}

	// Extract entity key
	entityKey = make([]byte, keyLen)
	copy(entityKey, remaining[len(remaining)-4-keyLen:len(remaining)-4])

	// Separator ':' is at position len(remaining)-4-keyLen-1
	sepIdx := len(remaining) - 4 - keyLen - 1
	if remaining[sepIdx] != ':' {
		return nil, nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.StoragePebbleIndex.parseKey",
			"missing separator in key",
		)
	}

	// Field value is everything before the separator
	fieldValue = remaining[:sepIdx]

	return fieldValue, entityKey, nil
}

// storageIncrementBytes increments a byte slice to create an exclusive upper bound.
func storageIncrementBytes(b []byte) []byte {
	if b == nil {
		return nil
	}

	// Create a copy
	result := make([]byte, len(b))
	copy(result, b)

	// Increment from the end
	for i := len(result) - 1; i >= 0; i-- {
		if result[i] < 0xff {
			result[i]++
			return result
		}
		result[i] = 0
	}

	// If we get here, all bytes were 0xff, so append a byte
	return append(result, 0)
}

// Config returns the index configuration.
func (idx *StoragePebbleIndex) Config() IndexConfig {
	return idx.config
}

// IsUnique reports whether this index enforces a unique constraint. It lets
// callers (CheckUniqueViolation, CheckUniqueViolationsBatch) pre-validate a
// write against this index via the uniqueIndexChecker capability, without
// depending on the concrete Index implementation.
func (idx *StoragePebbleIndex) IsUnique() bool {
	return idx.config.Unique
}

// StoragePebbleIndexName returns the index name.
func (idx *StoragePebbleIndex) StoragePebbleIndexName() string {
	return idx.config.Name
}

// StoragePebbleIndexType returns the index type.
func (idx *StoragePebbleIndex) StoragePebbleIndexType() IndexType {
	return idx.config.Type
}

// PutBatch queues an index entry addition into the provided batch.
// The caller is responsible for committing the batch. For numeric indexes,
// fieldValue is encoded before queueing. Non-numeric values are silently
// skipped.
func (idx *StoragePebbleIndex) PutBatch(batch storage.Batch, entityKey []byte, fieldValue []byte) error {
	start := time.Now()
	defer func() {
		idx.metrics.putLatency.Observe(time.Since(start).Seconds())
	}()

	idx.metrics.putOps.Inc()

	// Normalize field value if needed
	normalizedValue, ok := NormalizeFieldValue(fieldValue, idx.config)
	if !ok {
		return nil // non-numeric value; skip silently
	}

	// Check unique constraint, reading through batch so an earlier entry of
	// the same apply batch's pending write is seen too.
	if idx.config.Unique {
		if err := idx.checkUniqueConstraintInBatch(batch, entityKey, normalizedValue); err != nil {
			idx.metrics.uniqueViolations.Inc()
			idx.metrics.errors.Inc()
			return err
		}
	}

	// Build index key
	key := idx.buildKey(normalizedValue, entityKey)

	// Queue in batch (empty value, key contains all info)
	if err := batch.Set(key, []byte{}); err != nil {
		idx.metrics.errors.Inc()
		return errors.Wrapf(
			err,
			errors.ErrStorage,
			"index.StoragePebbleIndex.PutBatch",
			"failed to queue index entry in batch",
		)
	}

	return nil
}

// DeleteBatch queues an index entry removal into the provided batch.
// The caller is responsible for committing the batch. For numeric indexes,
// fieldValue is encoded before building the delete key.
func (idx *StoragePebbleIndex) DeleteBatch(batch storage.Batch, entityKey []byte, fieldValue []byte) error {
	idx.metrics.deleteOps.Inc()

	// Normalize field value if needed
	normalizedValue, ok := NormalizeFieldValue(fieldValue, idx.config)
	if !ok {
		return nil // non-numeric value; nothing to delete
	}

	// Build index key
	key := idx.buildKey(normalizedValue, entityKey)

	// Queue in batch
	if err := batch.Delete(key); err != nil {
		idx.metrics.errors.Inc()
		return errors.Wrapf(
			err,
			errors.ErrStorage,
			"index.StoragePebbleIndex.DeleteBatch",
			"failed to queue index deletion in batch",
		)
	}

	return nil
}

// Compile-time assertions.
var (
	_ Index            = (*StoragePebbleIndex)(nil)
	_ IndexWithContext = (*StoragePebbleIndex)(nil)
	_ BatchableIndex   = (*StoragePebbleIndex)(nil)
)
