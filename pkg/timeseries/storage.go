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

// Package timeseries provides time-series data storage and aggregation built
// on PebbleDB. Data points are stored using composite keys that enable
// efficient time-range queries.
package timeseries

import (
	"context"
	"encoding/binary"
	"hash/fnv"
	"math"
	"sort"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

const (
	// keyPrefix is the namespace prefix for all time-series data point keys.
	keyPrefix = "ts:"

	// prefixMetricIdx is the namespace prefix for the metric name index.
	prefixMetricIdx = "idx:metric:"

	// prefixTagIdx is the namespace prefix for tag key:value indexes.
	prefixTagIdx = "idx:tag:"

	// prefixRetention is the namespace prefix for retention policy storage.
	prefixRetention = "retention:"
)

// Storage provides time-series data storage and aggregation over PebbleDB.
// All methods are safe for concurrent use.
type Storage struct {
	db     *pebble.DB
	ownsDB bool
	closed atomic.Bool
}

// New creates a new time-series Storage backed by the given PebbleDB instance.
// Returns a NilDBError if db is nil.
func New(db *pebble.DB) (*Storage, error) {
	if db == nil {
		return nil, &NilDBError{}
	}
	return &Storage{db: db}, nil
}

// StorageOption configures a new Storage instance created by NewWithDir or
// NewWithBarrier. Options are applied to the pebble.Options before the
// database is opened.
type StorageOption func(*pebble.Options)

// WithBarrier wraps the Pebble vfs.FS with seal/encfs so that every file
// the time-series store writes is transparently encrypted under the supplied
// StorageBarrier. The barrier must already be unsealed when the storage is
// opened. A nil barrier is a no-op — the store opens in plaintext mode,
// which is the default and is appropriate for tests and non-encrypted
// deployments.
//
// Deprecated: prefer WithEncryptor(atrest.New(barrier)) which routes
// through the single atrest.Encryptor authority. WithBarrier is kept for
// backward compatibility.
func WithBarrier(barrier *seal.StorageBarrier) StorageOption {
	return WithEncryptor(atrest.New(barrier))
}

// WithEncryptor applies enc to the Pebble options, wrapping the vfs.FS
// with barrier-driven AES-256-GCM encryption. A nil enc or an enc whose
// barrier is nil is a no-op, so plaintext deployments and tests are
// unaffected.
func WithEncryptor(enc *atrest.Encryptor) StorageOption {
	return func(o *pebble.Options) {
		enc.Apply(o)
	}
}

// NewWithDir creates a new time-series Storage that owns its own PebbleDB
// instance rooted at dataDir. Optional StorageOption values configure the
// underlying PebbleDB instance before it is opened. The caller must call
// Close to release all resources including the underlying PebbleDB.
func NewWithDir(dataDir string, opts ...StorageOption) (*Storage, error) {
	if dataDir == "" {
		return nil, &EmptyDirError{}
	}
	pOpts := &pebble.Options{}
	for _, opt := range opts {
		opt(pOpts)
	}
	db, err := pebble.Open(dataDir, pOpts)
	if err != nil {
		return nil, &OpenDirError{Dir: dataDir, Err: err}
	}
	return &Storage{db: db, ownsDB: true}, nil
}

// DB returns the underlying PebbleDB instance.
func (s *Storage) DB() *pebble.DB {
	return s.db
}

// Write stores a single data point in PebbleDB.
func (s *Storage) Write(metric string, value float64, timestamp time.Time, tags map[string]string) error {
	if s.closed.Load() {
		return &ClosedError{}
	}
	if metric == "" {
		return &InvalidMetricError{}
	}
	if timestamp.IsZero() {
		return &InvalidTimestampError{}
	}

	dp := &DataPoint{
		Metric:    metric,
		Value:     value,
		Timestamp: timestamp,
		Tags:      tags,
	}

	key := buildDataPointKey(metric, timestamp, tags)
	encoded, err := encodeDataPoint(dp)
	if err != nil {
		return err
	}

	if err := s.db.Set(key, encoded, pebble.Sync); err != nil {
		return &WriteError{Err: err}
	}
	if err := s.updateMetricIndex(metric); err != nil {
		return err
	}
	if err := s.updateTagIndexes(metric, tags); err != nil {
		return err
	}
	return nil
}

// WritePoint writes a single DataPoint to storage after validation.
func (s *Storage) WritePoint(_ context.Context, point *DataPoint) error {
	if s.closed.Load() {
		return &ClosedError{}
	}
	if err := point.Validate(); err != nil {
		return err
	}
	key := buildDataPointKey(point.Metric, point.Timestamp, point.Tags)
	encoded, err := encodeDataPoint(point)
	if err != nil {
		return err
	}
	if err := s.db.Set(key, encoded, pebble.Sync); err != nil {
		return &WriteError{Err: err}
	}
	if err := s.updateMetricIndex(point.Metric); err != nil {
		return err
	}
	return s.updateTagIndexes(point.Metric, point.Tags)
}

// WriteBatch atomically stores multiple data points using a PebbleDB batch.
func (s *Storage) WriteBatch(points []DataPoint) error {
	if s.closed.Load() {
		return &ClosedError{}
	}

	for i := range points {
		if points[i].Metric == "" {
			return &InvalidMetricError{}
		}
		if points[i].Timestamp.IsZero() {
			return &InvalidTimestampError{}
		}
	}

	batch := s.db.NewBatch()
	defer func() { _ = batch.Close() }()

	uniqueMetrics := make(map[string]bool)
	uniqueTags := make(map[string]bool)

	for i := range points {
		key := buildDataPointKey(points[i].Metric, points[i].Timestamp, points[i].Tags)
		encoded, err := encodeDataPoint(&points[i])
		if err != nil {
			return err
		}
		if err := batch.Set(key, encoded, pebble.Sync); err != nil {
			return &WriteError{Err: err}
		}

		uniqueMetrics[points[i].Metric] = true
		for k, v := range points[i].Tags {
			uniqueTags[k+":"+v] = true
		}
	}

	// Add metric index entries to the batch.
	for metric := range uniqueMetrics {
		idxKey := []byte(prefixMetricIdx + metric)
		if err := batch.Set(idxKey, []byte{1}, nil); err != nil {
			return &WriteError{Err: err}
		}
	}

	// Add tag index entries to the batch.
	for tagKV := range uniqueTags {
		idxKey := []byte(prefixTagIdx + tagKV)
		if err := batch.Set(idxKey, []byte{1}, nil); err != nil {
			return &WriteError{Err: err}
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return &WriteError{Err: err}
	}
	return nil
}

// Query retrieves data points for a metric within the given time range.
// Optional TSQueryOption values configure tag filtering, limits, and ordering.
func (s *Storage) Query(metric string, start, end time.Time, opts ...TSQueryOption) ([]DataPoint, error) {
	if s.closed.Load() {
		return nil, &ClosedError{}
	}
	if metric == "" {
		return nil, &InvalidMetricError{}
	}
	if start.IsZero() || end.IsZero() {
		return nil, &InvalidTimestampError{}
	}
	if end.Before(start) {
		return nil, &InvalidTimeRangeError{Start: start, End: end}
	}

	qo := ApplyTSQueryOptions(opts)

	lowerBound := buildRangeStartKey(metric, start)
	// Upper bound is exclusive; use end+1ns to include data points at exactly end.
	upperBound := buildRangeStartKey(metric, end.Add(time.Nanosecond))

	iterOpts := &pebble.IterOptions{
		LowerBound: lowerBound,
		UpperBound: upperBound,
	}

	iter, err := s.db.NewIter(iterOpts)
	if err != nil {
		return nil, &ReadError{Err: err}
	}
	defer func() { _ = iter.Close() }()

	var results []DataPoint

	for iter.First(); iter.Valid(); iter.Next() {
		val, err := iter.ValueAndErr()
		if err != nil {
			return nil, &ReadError{Err: err}
		}

		dp, err := decodeDataPoint(val)
		if err != nil {
			return nil, err
		}

		if qo.Tags != nil && !matchesTags(dp.Tags, qo.Tags) {
			continue
		}

		results = append(results, *dp)

		if qo.Limit > 0 && len(results) >= qo.Limit && !qo.Descending {
			break
		}
	}

	if err := iter.Error(); err != nil {
		return nil, &ReadError{Err: err}
	}

	if qo.Descending {
		reverseDataPoints(results)
		if qo.Limit > 0 && len(results) > qo.Limit {
			results = results[:qo.Limit]
		}
	}

	if results == nil {
		results = []DataPoint{}
	}
	return results, nil
}

// QueryRich performs a rich query with aggregation, downsampling, and groupBy
// support. It returns a QueryResult containing raw or aggregated data.
func (s *Storage) QueryRich(_ context.Context, req *QueryRequest) (*QueryResult, error) {
	if s.closed.Load() {
		return nil, &ClosedError{}
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}

	lowerBound := buildRangeStartKey(req.Metric, req.StartTime)
	upperBound := buildRangeStartKey(req.Metric, req.EndTime.Add(time.Nanosecond))

	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: lowerBound,
		UpperBound: upperBound,
	})
	if err != nil {
		return nil, &ReadError{Err: err}
	}
	defer func() { _ = iter.Close() }()

	points := make([]*DataPoint, 0)
	for iter.First(); iter.Valid(); iter.Next() {
		val, err := iter.ValueAndErr()
		if err != nil {
			return nil, &ReadError{Err: err}
		}
		dp, err := decodeDataPoint(val)
		if err != nil {
			return nil, err
		}
		if req.Tags != nil && !matchesTags(dp.Tags, req.Tags) {
			continue
		}
		points = append(points, dp)
		if req.Limit > 0 && len(points) >= req.Limit && req.Aggregation == AggNone {
			break
		}
	}
	if err := iter.Error(); err != nil {
		return nil, &ReadError{Err: err}
	}

	result := &QueryResult{
		Metric: req.Metric,
		Tags:   req.Tags,
		Points: points,
	}

	if req.Aggregation != AggNone {
		if len(req.GroupBy) > 0 {
			result.GroupedResults = groupAndAggregate(points, req.GroupBy, req.Aggregation, req.Interval, req.Percentile)
		} else if req.Interval > 0 {
			result.Points = downsample(points, req.Interval, req.Aggregation, req.Percentile)
		} else {
			value := aggregatePoints(points, req.Aggregation, req.Percentile)
			result.AggregatedValue = &value
		}
	}

	return result, nil
}

// DeleteSeries deletes data points matching the given DeleteRequest criteria.
func (s *Storage) DeleteSeries(_ context.Context, req *DeleteRequest) error {
	if s.closed.Load() {
		return &ClosedError{}
	}
	if err := req.Validate(); err != nil {
		return err
	}

	startTime := req.StartTime
	if startTime.IsZero() {
		startTime = time.Unix(0, 0)
	}
	endTime := req.EndTime
	if endTime.IsZero() {
		endTime = time.Now().Add(100 * 365 * 24 * time.Hour)
	}

	lowerBound := buildRangeStartKey(req.Metric, startTime)
	upperBound := buildRangeStartKey(req.Metric, endTime.Add(time.Nanosecond))

	batch := s.db.NewBatch()
	defer func() { _ = batch.Close() }()

	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: lowerBound,
		UpperBound: upperBound,
	})
	if err != nil {
		return &ReadError{Err: err}
	}
	defer func() { _ = iter.Close() }()

	for iter.First(); iter.Valid(); iter.Next() {
		if len(req.Tags) > 0 {
			val, err := iter.ValueAndErr()
			if err != nil {
				return &ReadError{Err: err}
			}
			dp, err := decodeDataPoint(val)
			if err != nil {
				return err
			}
			if !matchesTags(dp.Tags, req.Tags) {
				continue
			}
		}
		if err := batch.Delete(iter.Key(), nil); err != nil {
			return &WriteError{Err: err}
		}
	}
	if err := iter.Error(); err != nil {
		return &ReadError{Err: err}
	}

	// If deleting all points for metric (no tag filter, full time range),
	// remove metric index too.
	if len(req.Tags) == 0 && req.StartTime.IsZero() && req.EndTime.IsZero() {
		metricKey := []byte(prefixMetricIdx + req.Metric)
		if err := batch.Delete(metricKey, nil); err != nil {
			return &WriteError{Err: err}
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return &WriteError{Err: err}
	}
	return nil
}

// GetRetentionPolicy retrieves the retention policy for the given metric.
// Returns a default (zero-value) policy if none has been set.
func (s *Storage) GetRetentionPolicy(metric string) (*RetentionPolicy, error) {
	if s.closed.Load() {
		return nil, &ClosedError{}
	}

	key := []byte(prefixRetention + metric)
	data, closer, err := s.db.Get(key)
	if err != nil {
		// Return default policy if not found.
		return &RetentionPolicy{}, nil
	}
	defer func() { _ = closer.Close() }()

	dp, err := decodeRetentionPolicy(data)
	if err != nil {
		return nil, &DecodeError{Reason: "retention policy: " + err.Error()}
	}
	return dp, nil
}

// SetRetentionPolicy stores the retention policy for the given metric.
func (s *Storage) SetRetentionPolicy(metric string, policy *RetentionPolicy) error {
	if s.closed.Load() {
		return &ClosedError{}
	}
	key := []byte(prefixRetention + metric)
	data, err := encodeRetentionPolicy(policy)
	if err != nil {
		return &WriteError{Err: err}
	}
	if err := s.db.Set(key, data, pebble.Sync); err != nil {
		return &WriteError{Err: err}
	}
	return nil
}

// ListMetrics returns the names of all indexed metrics whose names start with
// the given prefix. An empty prefix matches all metrics. Use limit to cap the
// number of returned names (0 means unlimited).
func (s *Storage) ListMetrics(prefix string, limit int) ([]string, error) {
	if s.closed.Load() {
		return nil, &ClosedError{}
	}

	lowerBound := []byte(prefixMetricIdx)
	upperBound := []byte(prefixMetricIdx + "\xff")
	if prefix != "" {
		lowerBound = []byte(prefixMetricIdx + prefix)
		upperBound = []byte(prefixMetricIdx + prefix + "\xff")
	}

	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: lowerBound,
		UpperBound: upperBound,
	})
	if err != nil {
		return nil, &ReadError{Err: err}
	}
	defer func() { _ = iter.Close() }()

	metrics := make([]string, 0)
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if len(key) > len(prefixMetricIdx) {
			metric := key[len(prefixMetricIdx):]
			metrics = append(metrics, metric)
		}
		if limit > 0 && len(metrics) >= limit {
			break
		}
	}
	if err := iter.Error(); err != nil {
		return nil, &ReadError{Err: err}
	}
	return metrics, nil
}

// Aggregate computes an aggregation function over data points for a metric
// within the given time range, bucketed by the specified interval.
func (s *Storage) Aggregate(metric string, start, end time.Time, fn AggFunc, interval time.Duration) ([]AggResult, error) {
	if s.closed.Load() {
		return nil, &ClosedError{}
	}
	if metric == "" {
		return nil, &InvalidMetricError{}
	}
	if start.IsZero() || end.IsZero() {
		return nil, &InvalidTimestampError{}
	}
	if end.Before(start) {
		return nil, &InvalidTimeRangeError{Start: start, End: end}
	}

	lowerBound := buildRangeStartKey(metric, start)
	upperBound := buildRangeStartKey(metric, end.Add(time.Nanosecond))

	iterOpts := &pebble.IterOptions{
		LowerBound: lowerBound,
		UpperBound: upperBound,
	}

	iter, err := s.db.NewIter(iterOpts)
	if err != nil {
		return nil, &ReadError{Err: err}
	}
	defer func() { _ = iter.Close() }()

	// Collect points into time buckets keyed by bucket index.
	type bucket struct {
		timestamp time.Time
		values    []float64
	}
	buckets := make(map[int64]*bucket)
	var bucketKeys []int64

	intervalNanos := int64(interval)

	for iter.First(); iter.Valid(); iter.Next() {
		val, err := iter.ValueAndErr()
		if err != nil {
			return nil, &ReadError{Err: err}
		}

		dp, err := decodeDataPoint(val)
		if err != nil {
			return nil, err
		}

		var bucketIdx int64
		if intervalNanos > 0 {
			bucketIdx = dp.Timestamp.UnixNano() / intervalNanos
		}
		b, exists := buckets[bucketIdx]
		if !exists {
			b = &bucket{
				timestamp: time.Unix(0, bucketIdx*intervalNanos).UTC(),
			}
			buckets[bucketIdx] = b
			bucketKeys = append(bucketKeys, bucketIdx)
		}
		b.values = append(b.values, dp.Value)
	}

	if err := iter.Error(); err != nil {
		return nil, &ReadError{Err: err}
	}

	// Sort bucket keys for deterministic chronological output.
	sort.Slice(bucketKeys, func(i, j int) bool {
		return bucketKeys[i] < bucketKeys[j]
	})

	results := make([]AggResult, 0, len(bucketKeys))
	for _, key := range bucketKeys {
		b := buckets[key]
		aggValue := computeAggregation(fn, b.values)
		results = append(results, AggResult{
			Timestamp: b.timestamp,
			Value:     aggValue,
			Count:     int64(len(b.values)),
		})
	}

	return results, nil
}

// Close marks the storage as closed. Subsequent operations will return
// a ClosedError. If the Storage owns its PebbleDB (created via NewWithDir),
// the underlying database is closed as well.
func (s *Storage) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return &ClosedError{}
	}
	if s.ownsDB {
		return s.db.Close()
	}
	return nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// updateMetricIndex writes a metric name into the metric index.
func (s *Storage) updateMetricIndex(metric string) error {
	key := []byte(prefixMetricIdx + metric)
	if err := s.db.Set(key, []byte{1}, pebble.Sync); err != nil {
		return &WriteError{Err: err}
	}
	return nil
}

// updateTagIndexes writes each tag key:value pair into the tag index.
func (s *Storage) updateTagIndexes(metric string, tags map[string]string) error {
	if len(tags) == 0 {
		return nil
	}
	tagKeys := make([]string, 0, len(tags))
	for k := range tags {
		tagKeys = append(tagKeys, k)
	}
	sort.Strings(tagKeys)

	for _, k := range tagKeys {
		key := []byte(prefixTagIdx + k + ":" + tags[k])
		if err := s.db.Set(key, []byte{1}, pebble.Sync); err != nil {
			return &WriteError{Err: err}
		}
	}
	return nil
}

// buildDataPointKey constructs a composite key for a data point.
// Format: "ts:" + metric + ":" + timestamp_ns(8 bytes BE) + ":" + tag_hash(16 hex chars)
func buildDataPointKey(metric string, timestamp time.Time, tags map[string]string) []byte {
	tagHash := hashTags(tags)

	// "ts:" + metric + ":" + 8 bytes timestamp + ":" + 16 hex chars
	keyLen := len(keyPrefix) + len(metric) + 1 + 8 + 1 + 16
	key := make([]byte, 0, keyLen)

	key = append(key, keyPrefix...)
	key = append(key, metric...)
	key = append(key, ':')

	var tsBuf [8]byte
	binary.BigEndian.PutUint64(tsBuf[:], uint64(timestamp.UnixNano()))
	key = append(key, tsBuf[:]...)

	key = append(key, ':')
	key = appendHex(key, tagHash)

	return key
}

// buildRangeStartKey constructs a key prefix for range scans starting at the
// given metric and timestamp.
// Format: "ts:" + metric + ":" + timestamp_ns(8 bytes BE)
func buildRangeStartKey(metric string, t time.Time) []byte {
	keyLen := len(keyPrefix) + len(metric) + 1 + 8
	key := make([]byte, 0, keyLen)

	key = append(key, keyPrefix...)
	key = append(key, metric...)
	key = append(key, ':')

	var tsBuf [8]byte
	binary.BigEndian.PutUint64(tsBuf[:], uint64(t.UnixNano()))
	key = append(key, tsBuf[:]...)

	return key
}

// hashTags computes a deterministic FNV-1a 64-bit hash of the tag map.
// Keys are sorted for determinism. Each tag is written as "key:value;".
func hashTags(tags map[string]string) uint64 {
	if len(tags) == 0 {
		return 0
	}

	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	h := fnv.New64a()
	for _, k := range keys {
		_, _ = h.Write([]byte(k))
		_, _ = h.Write([]byte(":"))
		_, _ = h.Write([]byte(tags[k]))
		_, _ = h.Write([]byte(";"))
	}
	return h.Sum64()
}

// appendHex appends a uint64 as 16 lowercase hexadecimal characters to dst.
func appendHex(dst []byte, v uint64) []byte {
	const hexDigits = "0123456789abcdef"
	var buf [16]byte
	for i := 15; i >= 0; i-- {
		buf[i] = hexDigits[v&0xf]
		v >>= 4
	}
	return append(dst, buf[:]...)
}

// encodeDataPoint serializes a DataPoint to binary format:
// [metricLen:2 LE][metric][value:8 LE float64][timestampNano:8 LE][tagCount:2 LE][tags...]
// Each tag: [keyLen:2 LE][key][valLen:2 LE][val]
func encodeDataPoint(dp *DataPoint) ([]byte, error) {
	metricBytes := []byte(dp.Metric)
	metricLen := len(metricBytes)

	// Pre-compute total size for a single allocation.
	// 2 (metricLen) + metric + 8 (value) + 8 (timestamp) + 2 (tagCount)
	size := 2 + metricLen + 8 + 8 + 2
	for k, v := range dp.Tags {
		// 2 (keyLen) + key + 2 (valLen) + val
		size += 2 + len(k) + 2 + len(v)
	}

	buf := make([]byte, 0, size)

	// Metric length and metric.
	var tmp [8]byte
	binary.LittleEndian.PutUint16(tmp[:2], uint16(metricLen))
	buf = append(buf, tmp[:2]...)
	buf = append(buf, metricBytes...)

	// Value as float64 bits.
	binary.LittleEndian.PutUint64(tmp[:], math.Float64bits(dp.Value))
	buf = append(buf, tmp[:]...)

	// Timestamp as nanoseconds.
	binary.LittleEndian.PutUint64(tmp[:], uint64(dp.Timestamp.UnixNano()))
	buf = append(buf, tmp[:]...)

	// Tag count.
	binary.LittleEndian.PutUint16(tmp[:2], uint16(len(dp.Tags)))
	buf = append(buf, tmp[:2]...)

	// Tags in sorted order for deterministic encoding.
	if len(dp.Tags) > 0 {
		tagKeys := make([]string, 0, len(dp.Tags))
		for k := range dp.Tags {
			tagKeys = append(tagKeys, k)
		}
		sort.Strings(tagKeys)

		for _, k := range tagKeys {
			v := dp.Tags[k]
			binary.LittleEndian.PutUint16(tmp[:2], uint16(len(k)))
			buf = append(buf, tmp[:2]...)
			buf = append(buf, k...)
			binary.LittleEndian.PutUint16(tmp[:2], uint16(len(v)))
			buf = append(buf, tmp[:2]...)
			buf = append(buf, v...)
		}
	}

	return buf, nil
}

// decodeDataPoint deserializes binary data back into a DataPoint.
func decodeDataPoint(data []byte) (*DataPoint, error) {
	// Minimum size: 2 (metricLen) + 0 (metric) + 8 (value) + 8 (timestamp) + 2 (tagCount) = 20
	const minSize = 20
	if len(data) < minSize {
		return nil, &DecodeError{Reason: "data too short"}
	}

	offset := 0

	// Metric length and metric.
	metricLen := int(binary.LittleEndian.Uint16(data[offset:]))
	offset += 2

	if offset+metricLen > len(data) {
		return nil, &DecodeError{Reason: "metric length exceeds data"}
	}
	metric := string(data[offset : offset+metricLen])
	offset += metricLen

	// Value.
	if offset+8 > len(data) {
		return nil, &DecodeError{Reason: "insufficient data for value"}
	}
	valueBits := binary.LittleEndian.Uint64(data[offset:])
	value := math.Float64frombits(valueBits)
	offset += 8

	// Timestamp.
	if offset+8 > len(data) {
		return nil, &DecodeError{Reason: "insufficient data for timestamp"}
	}
	tsNano := binary.LittleEndian.Uint64(data[offset:])
	timestamp := time.Unix(0, int64(tsNano)).UTC()
	offset += 8

	// Tag count.
	if offset+2 > len(data) {
		return nil, &DecodeError{Reason: "insufficient data for tag count"}
	}
	tagCount := int(binary.LittleEndian.Uint16(data[offset:]))
	offset += 2

	// Tags.
	var tags map[string]string
	if tagCount > 0 {
		tags = make(map[string]string, tagCount)
		for i := 0; i < tagCount; i++ {
			if offset+2 > len(data) {
				return nil, &DecodeError{Reason: "insufficient data for tag key length"}
			}
			keyLen := int(binary.LittleEndian.Uint16(data[offset:]))
			offset += 2

			if offset+keyLen > len(data) {
				return nil, &DecodeError{Reason: "tag key length exceeds data"}
			}
			tagKey := string(data[offset : offset+keyLen])
			offset += keyLen

			if offset+2 > len(data) {
				return nil, &DecodeError{Reason: "insufficient data for tag value length"}
			}
			valLen := int(binary.LittleEndian.Uint16(data[offset:]))
			offset += 2

			if offset+valLen > len(data) {
				return nil, &DecodeError{Reason: "tag value length exceeds data"}
			}
			tagVal := string(data[offset : offset+valLen])
			offset += valLen

			tags[tagKey] = tagVal
		}
	}

	return &DataPoint{
		Metric:    metric,
		Value:     value,
		Timestamp: timestamp,
		Tags:      tags,
	}, nil
}

// matchesTags returns true if the data point tags contain all the filter tags.
func matchesTags(dpTags, filterTags map[string]string) bool {
	for k, v := range filterTags {
		if dpTags == nil {
			return false
		}
		if dpVal, ok := dpTags[k]; !ok || dpVal != v {
			return false
		}
	}
	return true
}

// reverseDataPoints reverses a slice of DataPoint in place.
func reverseDataPoints(pts []DataPoint) {
	for i, j := 0, len(pts)-1; i < j; i, j = i+1, j-1 {
		pts[i], pts[j] = pts[j], pts[i]
	}
}

// computeAggregation applies the specified aggregation function to a set of values.
func computeAggregation(fn AggFunc, values []float64) float64 {
	if len(values) == 0 {
		return 0
	}

	// Map-based dispatch over switch statements per project conventions.
	type aggFn func([]float64) float64
	dispatch := map[AggFunc]aggFn{
		AggAvg:        aggAvg,
		AggSum:        aggSum,
		AggMin:        aggMin,
		AggMax:        aggMax,
		AggCount:      aggCount,
		AggPercentile: aggPercentile99,
		AggFirst:      aggFirst,
		AggLast:       aggLast,
	}

	if f, ok := dispatch[fn]; ok {
		return f(values)
	}
	return 0
}

// computeAggregationWithPercentile extends computeAggregation to support a
// custom percentile value when the aggregation function is AggPercentile.
func computeAggregationWithPercentile(fn AggFunc, values []float64, percentile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	if fn == AggPercentile {
		sorted := make([]float64, len(values))
		copy(sorted, values)
		sort.Float64s(sorted)
		idx := int(float64(len(sorted)-1) * percentile / 100.0)
		return sorted[idx]
	}
	return computeAggregation(fn, values)
}

// aggregatePoints computes an aggregation across a slice of DataPoint pointers.
func aggregatePoints(points []*DataPoint, fn AggFunc, percentile float64) float64 {
	if len(points) == 0 {
		return 0
	}
	values := make([]float64, len(points))
	for i, p := range points {
		values[i] = p.Value
	}
	return computeAggregationWithPercentile(fn, values, percentile)
}

// downsample buckets data points by the given interval and applies the
// aggregation function to each bucket, returning one aggregated DataPoint
// per bucket.
func downsample(points []*DataPoint, interval time.Duration, fn AggFunc, percentile float64) []*DataPoint {
	if len(points) == 0 || interval == 0 {
		return points
	}

	type bucket struct {
		points []*DataPoint
	}
	buckets := make(map[int64]*bucket)
	var bucketKeys []int64

	intervalNanos := int64(interval)
	for _, p := range points {
		idx := p.Timestamp.UnixNano() / intervalNanos
		b, exists := buckets[idx]
		if !exists {
			b = &bucket{}
			buckets[idx] = b
			bucketKeys = append(bucketKeys, idx)
		}
		b.points = append(b.points, p)
	}

	sort.Slice(bucketKeys, func(i, j int) bool {
		return bucketKeys[i] < bucketKeys[j]
	})

	result := make([]*DataPoint, 0, len(bucketKeys))
	for _, key := range bucketKeys {
		b := buckets[key]
		value := aggregatePoints(b.points, fn, percentile)
		result = append(result, &DataPoint{
			Metric:    b.points[0].Metric,
			Tags:      b.points[0].Tags,
			Value:     value,
			Timestamp: time.Unix(0, key*intervalNanos).UTC(),
		})
	}
	return result
}

// groupAndAggregate partitions data points by the groupBy tag keys, then
// applies the aggregation function (and optional downsampling) within each
// group.
func groupAndAggregate(points []*DataPoint, groupBy []string, fn AggFunc, interval time.Duration, percentile float64) []*GroupedResult {
	if len(points) == 0 {
		return nil
	}

	groups := make(map[string]*GroupedResult)
	for _, p := range points {
		groupKey := buildGroupKey(p.Tags, groupBy)
		if groups[groupKey] == nil {
			groupTags := make(map[string]string)
			for _, k := range groupBy {
				groupTags[k] = p.Tags[k]
			}
			groups[groupKey] = &GroupedResult{
				Tags:   groupTags,
				Points: make([]*DataPoint, 0),
			}
		}
		groups[groupKey].Points = append(groups[groupKey].Points, p)
	}

	results := make([]*GroupedResult, 0, len(groups))
	for _, group := range groups {
		if interval > 0 {
			group.Points = downsample(group.Points, interval, fn, percentile)
		} else {
			value := aggregatePoints(group.Points, fn, percentile)
			group.AggregatedValue = &value
		}
		results = append(results, group)
	}
	return results
}

// buildGroupKey constructs a deterministic string key from the tag values
// corresponding to the groupBy keys.
func buildGroupKey(tags map[string]string, groupBy []string) string {
	var b []byte
	for i, k := range groupBy {
		if i > 0 {
			b = append(b, '|')
		}
		b = append(b, tags[k]...)
	}
	return string(b)
}

// encodeRetentionPolicy serializes a RetentionPolicy to a 17-byte binary
// format: [duration:8 LE][resolution:8 LE][aggregation:1].
func encodeRetentionPolicy(p *RetentionPolicy) ([]byte, error) {
	buf := make([]byte, 17)
	binary.LittleEndian.PutUint64(buf[0:], uint64(p.Duration))
	binary.LittleEndian.PutUint64(buf[8:], uint64(p.Resolution))
	buf[16] = byte(p.Aggregation)
	return buf, nil
}

// decodeRetentionPolicy deserializes a RetentionPolicy from its binary format.
func decodeRetentionPolicy(data []byte) (*RetentionPolicy, error) {
	if len(data) < 17 {
		return nil, &DecodeError{Reason: "retention policy data too short"}
	}
	return &RetentionPolicy{
		Duration:    time.Duration(binary.LittleEndian.Uint64(data[0:])),
		Resolution:  time.Duration(binary.LittleEndian.Uint64(data[8:])),
		Aggregation: AggFunc(data[16]),
	}, nil
}

// aggAvg computes the arithmetic mean.
func aggAvg(values []float64) float64 {
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

// aggSum computes the sum.
func aggSum(values []float64) float64 {
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum
}

// aggMin finds the minimum value.
func aggMin(values []float64) float64 {
	min := values[0]
	for _, v := range values[1:] {
		if v < min {
			min = v
		}
	}
	return min
}

// aggMax finds the maximum value.
func aggMax(values []float64) float64 {
	max := values[0]
	for _, v := range values[1:] {
		if v > max {
			max = v
		}
	}
	return max
}

// aggCount returns the count of values.
func aggCount(values []float64) float64 {
	return float64(len(values))
}

// aggPercentile99 computes the 99th percentile using nearest-rank method.
func aggPercentile99(values []float64) float64 {
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)
	idx := int(float64(len(sorted)-1) * 0.99)
	return sorted[idx]
}

// aggFirst returns the first value in the slice (earliest in time order).
func aggFirst(values []float64) float64 {
	return values[0]
}

// aggLast returns the last value in the slice (latest in time order).
func aggLast(values []float64) float64 {
	return values[len(values)-1]
}
