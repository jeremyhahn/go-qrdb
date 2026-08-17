//go:build qrdb_ts

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

package errors

import (
	"errors"
	"fmt"
)

// Time series specific error codes.
const (
	// ErrTimeSeriesNotFound indicates the series/metric was not found.
	ErrTimeSeriesNotFound ErrorCode = iota + 1000

	// ErrTimeSeriesInvalidTimestamp indicates an invalid timestamp.
	ErrTimeSeriesInvalidTimestamp

	// ErrTimeSeriesRetentionViolation indicates a retention policy violation.
	ErrTimeSeriesRetentionViolation

	// ErrTimeSeriesQueryTimeout indicates a query timeout.
	ErrTimeSeriesQueryTimeout

	// ErrTimeSeriesBatchTooLarge indicates the batch exceeds size limits.
	ErrTimeSeriesBatchTooLarge

	// ErrTimeSeriesInvalidMetric indicates an invalid metric name.
	ErrTimeSeriesInvalidMetric

	// ErrTimeSeriesInvalidTags indicates invalid tags.
	ErrTimeSeriesInvalidTags

	// ErrTimeSeriesShardUnavailable indicates the shard is not available.
	ErrTimeSeriesShardUnavailable
)

// TimeSeriesNotFoundError indicates a time series metric was not found.
type TimeSeriesNotFoundError struct {
	Metric string
}

// Error implements error interface.
func (e *TimeSeriesNotFoundError) Error() string {
	return fmt.Sprintf("time series metric not found: %s", e.Metric)
}

// TimeSeriesInvalidTimestampError indicates an invalid timestamp.
type TimeSeriesInvalidTimestampError struct {
	Timestamp int64
	Reason    string
}

// Error implements error interface.
func (e *TimeSeriesInvalidTimestampError) Error() string {
	return fmt.Sprintf("invalid timestamp %d: %s", e.Timestamp, e.Reason)
}

// TimeSeriesRetentionViolationError indicates a retention policy violation.
type TimeSeriesRetentionViolationError struct {
	Metric        string
	RetentionDays int
}

// Error implements error interface.
func (e *TimeSeriesRetentionViolationError) Error() string {
	return fmt.Sprintf("retention policy violation for metric '%s': data exceeds %d day retention", e.Metric, e.RetentionDays)
}

// TimeSeriesQueryTimeoutError indicates a query timeout.
type TimeSeriesQueryTimeoutError struct {
	Query   string
	Timeout string
}

// Error implements error interface.
func (e *TimeSeriesQueryTimeoutError) Error() string {
	return fmt.Sprintf("query timeout after %s: %s", e.Timeout, e.Query)
}

// TimeSeriesBatchTooLargeError indicates the batch exceeds size limits.
type TimeSeriesBatchTooLargeError struct {
	BatchSize int
	MaxSize   int
}

// Error implements error interface.
func (e *TimeSeriesBatchTooLargeError) Error() string {
	return fmt.Sprintf("batch size %d exceeds maximum %d", e.BatchSize, e.MaxSize)
}

// TimeSeriesInvalidMetricError indicates an invalid metric name.
type TimeSeriesInvalidMetricError struct {
	Metric string
	Reason string
}

// Error implements error interface.
func (e *TimeSeriesInvalidMetricError) Error() string {
	return fmt.Sprintf("invalid metric name '%s': %s", e.Metric, e.Reason)
}

// TimeSeriesInvalidTagsError indicates invalid tags.
type TimeSeriesInvalidTagsError struct {
	Tags   map[string]string
	Reason string
}

// Error implements error interface.
func (e *TimeSeriesInvalidTagsError) Error() string {
	return fmt.Sprintf("invalid tags %v: %s", e.Tags, e.Reason)
}

// TimeSeriesShardUnavailableError indicates the shard is not available.
type TimeSeriesShardUnavailableError struct {
	ShardID uint64
	Reason  string
}

// Error implements error interface.
func (e *TimeSeriesShardUnavailableError) Error() string {
	return fmt.Sprintf("shard %d unavailable: %s", e.ShardID, e.Reason)
}

// NewTimeSeriesNotFoundError creates a new TimeSeriesNotFoundError wrapped in QRDBError.
func NewTimeSeriesNotFoundError(metric string) *QRDBError {
	return &QRDBError{
		Code: ErrTimeSeriesNotFound,
		Op:   "timeseries",
		Err:  &TimeSeriesNotFoundError{Metric: metric},
		Context: map[string]interface{}{
			"metric": metric,
		},
	}
}

// NewTimeSeriesInvalidTimestampError creates a new TimeSeriesInvalidTimestampError wrapped in QRDBError.
func NewTimeSeriesInvalidTimestampError(timestamp int64, reason string) *QRDBError {
	return &QRDBError{
		Code: ErrTimeSeriesInvalidTimestamp,
		Op:   "timeseries",
		Err:  &TimeSeriesInvalidTimestampError{Timestamp: timestamp, Reason: reason},
		Context: map[string]interface{}{
			"timestamp": timestamp,
			"reason":    reason,
		},
	}
}

// NewTimeSeriesRetentionViolationError creates a new TimeSeriesRetentionViolationError wrapped in QRDBError.
func NewTimeSeriesRetentionViolationError(metric string, retentionDays int) *QRDBError {
	return &QRDBError{
		Code: ErrTimeSeriesRetentionViolation,
		Op:   "timeseries",
		Err:  &TimeSeriesRetentionViolationError{Metric: metric, RetentionDays: retentionDays},
		Context: map[string]interface{}{
			"metric":         metric,
			"retention_days": retentionDays,
		},
	}
}

// NewTimeSeriesQueryTimeoutError creates a new TimeSeriesQueryTimeoutError wrapped in QRDBError.
func NewTimeSeriesQueryTimeoutError(query, timeout string) *QRDBError {
	return &QRDBError{
		Code:  ErrTimeSeriesQueryTimeout,
		Op:    "timeseries.query",
		Err:   &TimeSeriesQueryTimeoutError{Query: query, Timeout: timeout},
		Retry: true,
		Context: map[string]interface{}{
			"query":   query,
			"timeout": timeout,
		},
	}
}

// NewTimeSeriesBatchTooLargeError creates a new TimeSeriesBatchTooLargeError wrapped in QRDBError.
func NewTimeSeriesBatchTooLargeError(batchSize, maxSize int) *QRDBError {
	return &QRDBError{
		Code: ErrTimeSeriesBatchTooLarge,
		Op:   "timeseries.batch",
		Err:  &TimeSeriesBatchTooLargeError{BatchSize: batchSize, MaxSize: maxSize},
		Context: map[string]interface{}{
			"batch_size": batchSize,
			"max_size":   maxSize,
		},
	}
}

// NewTimeSeriesInvalidMetricError creates a new TimeSeriesInvalidMetricError wrapped in QRDBError.
func NewTimeSeriesInvalidMetricError(metric, reason string) *QRDBError {
	return &QRDBError{
		Code: ErrTimeSeriesInvalidMetric,
		Op:   "timeseries",
		Err:  &TimeSeriesInvalidMetricError{Metric: metric, Reason: reason},
		Context: map[string]interface{}{
			"metric": metric,
			"reason": reason,
		},
	}
}

// NewTimeSeriesInvalidTagsError creates a new TimeSeriesInvalidTagsError wrapped in QRDBError.
func NewTimeSeriesInvalidTagsError(tags map[string]string, reason string) *QRDBError {
	return &QRDBError{
		Code: ErrTimeSeriesInvalidTags,
		Op:   "timeseries",
		Err:  &TimeSeriesInvalidTagsError{Tags: tags, Reason: reason},
		Context: map[string]interface{}{
			"tags":   tags,
			"reason": reason,
		},
	}
}

// NewTimeSeriesShardUnavailableError creates a new TimeSeriesShardUnavailableError wrapped in QRDBError.
func NewTimeSeriesShardUnavailableError(shardID uint64, reason string) *QRDBError {
	return &QRDBError{
		Code:  ErrTimeSeriesShardUnavailable,
		Op:    "timeseries.shard",
		Err:   &TimeSeriesShardUnavailableError{ShardID: shardID, Reason: reason},
		Retry: true,
		Context: map[string]interface{}{
			"shard_id": shardID,
			"reason":   reason,
		},
	}
}

// IsTimeSeriesError returns true if the error is a time series related error.
func IsTimeSeriesError(err error) bool {
	if err == nil {
		return false
	}

	var de *QRDBError
	if errors.As(err, &de) {
		return de.Code >= ErrTimeSeriesNotFound && de.Code <= ErrTimeSeriesShardUnavailable
	}

	// Check for specific time series error types
	var tsNotFound *TimeSeriesNotFoundError
	if errors.As(err, &tsNotFound) {
		return true
	}

	var tsInvalidTimestamp *TimeSeriesInvalidTimestampError
	if errors.As(err, &tsInvalidTimestamp) {
		return true
	}

	var tsRetention *TimeSeriesRetentionViolationError
	if errors.As(err, &tsRetention) {
		return true
	}

	var tsQueryTimeout *TimeSeriesQueryTimeoutError
	if errors.As(err, &tsQueryTimeout) {
		return true
	}

	var tsBatchTooLarge *TimeSeriesBatchTooLargeError
	if errors.As(err, &tsBatchTooLarge) {
		return true
	}

	var tsInvalidMetric *TimeSeriesInvalidMetricError
	if errors.As(err, &tsInvalidMetric) {
		return true
	}

	var tsInvalidTags *TimeSeriesInvalidTagsError
	if errors.As(err, &tsInvalidTags) {
		return true
	}

	var tsShardUnavailable *TimeSeriesShardUnavailableError
	return errors.As(err, &tsShardUnavailable)
}

// IsTimeSeriesNotFound returns true if the error is a TimeSeriesNotFoundError.
func IsTimeSeriesNotFound(err error) bool {
	if err == nil {
		return false
	}

	var de *QRDBError
	if errors.As(err, &de) {
		if de.Code == ErrTimeSeriesNotFound {
			return true
		}
	}

	var tsNotFound *TimeSeriesNotFoundError
	return errors.As(err, &tsNotFound)
}

// IsTimeSeriesInvalidTimestamp returns true if the error is a TimeSeriesInvalidTimestampError.
func IsTimeSeriesInvalidTimestamp(err error) bool {
	if err == nil {
		return false
	}

	var de *QRDBError
	if errors.As(err, &de) {
		if de.Code == ErrTimeSeriesInvalidTimestamp {
			return true
		}
	}

	var tsInvalidTimestamp *TimeSeriesInvalidTimestampError
	return errors.As(err, &tsInvalidTimestamp)
}

// IsTimeSeriesRetentionViolation returns true if the error is a TimeSeriesRetentionViolationError.
func IsTimeSeriesRetentionViolation(err error) bool {
	if err == nil {
		return false
	}

	var de *QRDBError
	if errors.As(err, &de) {
		if de.Code == ErrTimeSeriesRetentionViolation {
			return true
		}
	}

	var tsRetention *TimeSeriesRetentionViolationError
	return errors.As(err, &tsRetention)
}

// IsTimeSeriesQueryTimeout returns true if the error is a TimeSeriesQueryTimeoutError.
func IsTimeSeriesQueryTimeout(err error) bool {
	if err == nil {
		return false
	}

	var de *QRDBError
	if errors.As(err, &de) {
		if de.Code == ErrTimeSeriesQueryTimeout {
			return true
		}
	}

	var tsQueryTimeout *TimeSeriesQueryTimeoutError
	return errors.As(err, &tsQueryTimeout)
}

// IsTimeSeriesBatchTooLarge returns true if the error is a TimeSeriesBatchTooLargeError.
func IsTimeSeriesBatchTooLarge(err error) bool {
	if err == nil {
		return false
	}

	var de *QRDBError
	if errors.As(err, &de) {
		if de.Code == ErrTimeSeriesBatchTooLarge {
			return true
		}
	}

	var tsBatchTooLarge *TimeSeriesBatchTooLargeError
	return errors.As(err, &tsBatchTooLarge)
}

// IsTimeSeriesInvalidMetric returns true if the error is a TimeSeriesInvalidMetricError.
func IsTimeSeriesInvalidMetric(err error) bool {
	if err == nil {
		return false
	}

	var de *QRDBError
	if errors.As(err, &de) {
		if de.Code == ErrTimeSeriesInvalidMetric {
			return true
		}
	}

	var tsInvalidMetric *TimeSeriesInvalidMetricError
	return errors.As(err, &tsInvalidMetric)
}

// IsTimeSeriesInvalidTags returns true if the error is a TimeSeriesInvalidTagsError.
func IsTimeSeriesInvalidTags(err error) bool {
	if err == nil {
		return false
	}

	var de *QRDBError
	if errors.As(err, &de) {
		if de.Code == ErrTimeSeriesInvalidTags {
			return true
		}
	}

	var tsInvalidTags *TimeSeriesInvalidTagsError
	return errors.As(err, &tsInvalidTags)
}

// IsTimeSeriesShardUnavailable returns true if the error is a TimeSeriesShardUnavailableError.
func IsTimeSeriesShardUnavailable(err error) bool {
	if err == nil {
		return false
	}

	var de *QRDBError
	if errors.As(err, &de) {
		if de.Code == ErrTimeSeriesShardUnavailable {
			return true
		}
	}

	var tsShardUnavailable *TimeSeriesShardUnavailableError
	return errors.As(err, &tsShardUnavailable)
}
