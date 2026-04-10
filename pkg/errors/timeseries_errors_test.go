//go:build timeseries

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-dragondb.
//
// go-dragondb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package errors

import (
	"errors"
	"strings"
	"testing"
)

func TestTimeSeriesNotFoundError_Error(t *testing.T) {
	tests := []struct {
		name     string
		metric   string
		contains string
	}{
		{
			name:     "simple metric name",
			metric:   "cpu.usage",
			contains: "cpu.usage",
		},
		{
			name:     "metric with tags",
			metric:   "memory.used{host=server1}",
			contains: "memory.used{host=server1}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &TimeSeriesNotFoundError{Metric: tt.metric}
			errStr := err.Error()
			if !strings.Contains(errStr, tt.contains) {
				t.Errorf("Error() = %q, want to contain %q", errStr, tt.contains)
			}
			if !strings.Contains(errStr, "not found") {
				t.Errorf("Error() = %q, want to contain 'not found'", errStr)
			}
		})
	}
}

func TestTimeSeriesInvalidTimestampError_Error(t *testing.T) {
	tests := []struct {
		name      string
		timestamp int64
		reason    string
	}{
		{
			name:      "negative timestamp",
			timestamp: -1,
			reason:    "timestamp cannot be negative",
		},
		{
			name:      "future timestamp",
			timestamp: 9999999999999,
			reason:    "timestamp too far in the future",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &TimeSeriesInvalidTimestampError{Timestamp: tt.timestamp, Reason: tt.reason}
			errStr := err.Error()
			if !strings.Contains(errStr, tt.reason) {
				t.Errorf("Error() = %q, want to contain %q", errStr, tt.reason)
			}
		})
	}
}

func TestTimeSeriesRetentionViolationError_Error(t *testing.T) {
	tests := []struct {
		name          string
		metric        string
		retentionDays int
	}{
		{
			name:          "30 day retention",
			metric:        "disk.io",
			retentionDays: 30,
		},
		{
			name:          "90 day retention",
			metric:        "network.bytes",
			retentionDays: 90,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &TimeSeriesRetentionViolationError{Metric: tt.metric, RetentionDays: tt.retentionDays}
			errStr := err.Error()
			if !strings.Contains(errStr, tt.metric) {
				t.Errorf("Error() = %q, want to contain %q", errStr, tt.metric)
			}
			if !strings.Contains(errStr, "retention") {
				t.Errorf("Error() = %q, want to contain 'retention'", errStr)
			}
		})
	}
}

func TestTimeSeriesQueryTimeoutError_Error(t *testing.T) {
	err := &TimeSeriesQueryTimeoutError{Query: "SELECT * FROM metrics", Timeout: "30s"}
	errStr := err.Error()
	if !strings.Contains(errStr, "30s") {
		t.Errorf("Error() = %q, want to contain '30s'", errStr)
	}
	if !strings.Contains(errStr, "SELECT * FROM metrics") {
		t.Errorf("Error() = %q, want to contain query", errStr)
	}
}

func TestTimeSeriesBatchTooLargeError_Error(t *testing.T) {
	tests := []struct {
		name      string
		batchSize int
		maxSize   int
	}{
		{
			name:      "batch exceeds limit",
			batchSize: 10000,
			maxSize:   5000,
		},
		{
			name:      "large batch",
			batchSize: 1000000,
			maxSize:   100000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &TimeSeriesBatchTooLargeError{BatchSize: tt.batchSize, MaxSize: tt.maxSize}
			errStr := err.Error()
			if !strings.Contains(errStr, "exceeds maximum") {
				t.Errorf("Error() = %q, want to contain 'exceeds maximum'", errStr)
			}
		})
	}
}

func TestTimeSeriesInvalidMetricError_Error(t *testing.T) {
	tests := []struct {
		name   string
		metric string
		reason string
	}{
		{
			name:   "empty metric",
			metric: "",
			reason: "metric name cannot be empty",
		},
		{
			name:   "invalid characters",
			metric: "cpu@usage!",
			reason: "metric contains invalid characters",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &TimeSeriesInvalidMetricError{Metric: tt.metric, Reason: tt.reason}
			errStr := err.Error()
			if !strings.Contains(errStr, tt.reason) {
				t.Errorf("Error() = %q, want to contain %q", errStr, tt.reason)
			}
		})
	}
}

func TestTimeSeriesInvalidTagsError_Error(t *testing.T) {
	tags := map[string]string{"host": "server1", "region": ""}
	err := &TimeSeriesInvalidTagsError{Tags: tags, Reason: "tag value cannot be empty"}
	errStr := err.Error()
	if !strings.Contains(errStr, "tag value cannot be empty") {
		t.Errorf("Error() = %q, want to contain reason", errStr)
	}
}

func TestTimeSeriesShardUnavailableError_Error(t *testing.T) {
	tests := []struct {
		name    string
		shardID uint64
		reason  string
	}{
		{
			name:    "shard not found",
			shardID: 1,
			reason:  "shard not found",
		},
		{
			name:    "shard offline",
			shardID: 42,
			reason:  "shard is offline",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &TimeSeriesShardUnavailableError{ShardID: tt.shardID, Reason: tt.reason}
			errStr := err.Error()
			if !strings.Contains(errStr, "unavailable") {
				t.Errorf("Error() = %q, want to contain 'unavailable'", errStr)
			}
			if !strings.Contains(errStr, tt.reason) {
				t.Errorf("Error() = %q, want to contain %q", errStr, tt.reason)
			}
		})
	}
}

func TestNewTimeSeriesNotFoundError(t *testing.T) {
	metric := "cpu.usage"
	err := NewTimeSeriesNotFoundError(metric)

	if err.Code != ErrTimeSeriesNotFound {
		t.Errorf("Code = %v, want %v", err.Code, ErrTimeSeriesNotFound)
	}
	if err.Op != "timeseries" {
		t.Errorf("Op = %q, want %q", err.Op, "timeseries")
	}

	var tsNotFound *TimeSeriesNotFoundError
	if !errors.As(err.Err, &tsNotFound) {
		t.Error("Err should be *TimeSeriesNotFoundError")
	}
	if tsNotFound.Metric != metric {
		t.Errorf("Metric = %q, want %q", tsNotFound.Metric, metric)
	}

	if err.Context["metric"] != metric {
		t.Errorf("Context[metric] = %v, want %q", err.Context["metric"], metric)
	}
}

func TestNewTimeSeriesNotFoundError_InvalidInput(t *testing.T) {
	err := NewTimeSeriesNotFoundError("")
	if err == nil {
		t.Error("NewTimeSeriesNotFoundError with empty string should still return error")
	}

	var tsNotFound *TimeSeriesNotFoundError
	if !errors.As(err.Err, &tsNotFound) {
		t.Error("Err should be *TimeSeriesNotFoundError even with empty metric")
	}
}

func TestNewTimeSeriesRetentionViolationError(t *testing.T) {
	metric := "disk.io"
	retentionDays := 30
	err := NewTimeSeriesRetentionViolationError(metric, retentionDays)

	if err.Code != ErrTimeSeriesRetentionViolation {
		t.Errorf("Code = %v, want %v", err.Code, ErrTimeSeriesRetentionViolation)
	}
	if err.Op != "timeseries" {
		t.Errorf("Op = %q, want %q", err.Op, "timeseries")
	}

	var tsRetention *TimeSeriesRetentionViolationError
	if !errors.As(err.Err, &tsRetention) {
		t.Error("Err should be *TimeSeriesRetentionViolationError")
	}
	if tsRetention.Metric != metric {
		t.Errorf("Metric = %q, want %q", tsRetention.Metric, metric)
	}
	if tsRetention.RetentionDays != retentionDays {
		t.Errorf("RetentionDays = %d, want %d", tsRetention.RetentionDays, retentionDays)
	}

	if err.Context["metric"] != metric {
		t.Errorf("Context[metric] = %v, want %q", err.Context["metric"], metric)
	}
	if err.Context["retention_days"] != retentionDays {
		t.Errorf("Context[retention_days] = %v, want %d", err.Context["retention_days"], retentionDays)
	}
}

func TestNewTimeSeriesRetentionViolationError_EdgeCases(t *testing.T) {
	tests := []struct {
		name          string
		metric        string
		retentionDays int
	}{
		{
			name:          "zero retention days",
			metric:        "test.metric",
			retentionDays: 0,
		},
		{
			name:          "negative retention days",
			metric:        "test.metric",
			retentionDays: -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewTimeSeriesRetentionViolationError(tt.metric, tt.retentionDays)
			if err == nil {
				t.Error("Expected error to be created")
			}
			if err.Code != ErrTimeSeriesRetentionViolation {
				t.Errorf("Code = %v, want %v", err.Code, ErrTimeSeriesRetentionViolation)
			}
		})
	}
}

func TestNewTimeSeriesBatchTooLargeError(t *testing.T) {
	batchSize := 10000
	maxSize := 5000
	err := NewTimeSeriesBatchTooLargeError(batchSize, maxSize)

	if err.Code != ErrTimeSeriesBatchTooLarge {
		t.Errorf("Code = %v, want %v", err.Code, ErrTimeSeriesBatchTooLarge)
	}
	if err.Op != "timeseries.batch" {
		t.Errorf("Op = %q, want %q", err.Op, "timeseries.batch")
	}

	var tsBatch *TimeSeriesBatchTooLargeError
	if !errors.As(err.Err, &tsBatch) {
		t.Error("Err should be *TimeSeriesBatchTooLargeError")
	}
	if tsBatch.BatchSize != batchSize {
		t.Errorf("BatchSize = %d, want %d", tsBatch.BatchSize, batchSize)
	}
	if tsBatch.MaxSize != maxSize {
		t.Errorf("MaxSize = %d, want %d", tsBatch.MaxSize, maxSize)
	}

	if err.Context["batch_size"] != batchSize {
		t.Errorf("Context[batch_size] = %v, want %d", err.Context["batch_size"], batchSize)
	}
	if err.Context["max_size"] != maxSize {
		t.Errorf("Context[max_size] = %v, want %d", err.Context["max_size"], maxSize)
	}
}

func TestNewTimeSeriesBatchTooLargeError_EdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		batchSize int
		maxSize   int
	}{
		{
			name:      "zero batch size",
			batchSize: 0,
			maxSize:   100,
		},
		{
			name:      "negative batch size",
			batchSize: -1,
			maxSize:   100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewTimeSeriesBatchTooLargeError(tt.batchSize, tt.maxSize)
			if err == nil {
				t.Error("Expected error to be created")
			}
			if err.Code != ErrTimeSeriesBatchTooLarge {
				t.Errorf("Code = %v, want %v", err.Code, ErrTimeSeriesBatchTooLarge)
			}
		})
	}
}

func TestNewTimeSeriesInvalidTimestampError(t *testing.T) {
	timestamp := int64(-1)
	reason := "timestamp cannot be negative"
	err := NewTimeSeriesInvalidTimestampError(timestamp, reason)

	if err.Code != ErrTimeSeriesInvalidTimestamp {
		t.Errorf("Code = %v, want %v", err.Code, ErrTimeSeriesInvalidTimestamp)
	}

	var tsInvalidTimestamp *TimeSeriesInvalidTimestampError
	if !errors.As(err.Err, &tsInvalidTimestamp) {
		t.Error("Err should be *TimeSeriesInvalidTimestampError")
	}
	if tsInvalidTimestamp.Timestamp != timestamp {
		t.Errorf("Timestamp = %d, want %d", tsInvalidTimestamp.Timestamp, timestamp)
	}
}

func TestNewTimeSeriesQueryTimeoutError(t *testing.T) {
	query := "SELECT * FROM metrics WHERE time > now() - 1h"
	timeout := "30s"
	err := NewTimeSeriesQueryTimeoutError(query, timeout)

	if err.Code != ErrTimeSeriesQueryTimeout {
		t.Errorf("Code = %v, want %v", err.Code, ErrTimeSeriesQueryTimeout)
	}
	if err.Op != "timeseries.query" {
		t.Errorf("Op = %q, want %q", err.Op, "timeseries.query")
	}
	if !err.Retry {
		t.Error("QueryTimeout error should be retryable")
	}

	var tsQueryTimeout *TimeSeriesQueryTimeoutError
	if !errors.As(err.Err, &tsQueryTimeout) {
		t.Error("Err should be *TimeSeriesQueryTimeoutError")
	}
}

func TestNewTimeSeriesInvalidMetricError(t *testing.T) {
	metric := "cpu@usage"
	reason := "invalid character '@'"
	err := NewTimeSeriesInvalidMetricError(metric, reason)

	if err.Code != ErrTimeSeriesInvalidMetric {
		t.Errorf("Code = %v, want %v", err.Code, ErrTimeSeriesInvalidMetric)
	}

	var tsInvalidMetric *TimeSeriesInvalidMetricError
	if !errors.As(err.Err, &tsInvalidMetric) {
		t.Error("Err should be *TimeSeriesInvalidMetricError")
	}
	if tsInvalidMetric.Metric != metric {
		t.Errorf("Metric = %q, want %q", tsInvalidMetric.Metric, metric)
	}
}

func TestNewTimeSeriesInvalidTagsError(t *testing.T) {
	tags := map[string]string{"host": "server1", "region": ""}
	reason := "tag value cannot be empty"
	err := NewTimeSeriesInvalidTagsError(tags, reason)

	if err.Code != ErrTimeSeriesInvalidTags {
		t.Errorf("Code = %v, want %v", err.Code, ErrTimeSeriesInvalidTags)
	}

	var tsInvalidTags *TimeSeriesInvalidTagsError
	if !errors.As(err.Err, &tsInvalidTags) {
		t.Error("Err should be *TimeSeriesInvalidTagsError")
	}
	if tsInvalidTags.Reason != reason {
		t.Errorf("Reason = %q, want %q", tsInvalidTags.Reason, reason)
	}
}

func TestNewTimeSeriesShardUnavailableError(t *testing.T) {
	shardID := uint64(42)
	reason := "shard is offline"
	err := NewTimeSeriesShardUnavailableError(shardID, reason)

	if err.Code != ErrTimeSeriesShardUnavailable {
		t.Errorf("Code = %v, want %v", err.Code, ErrTimeSeriesShardUnavailable)
	}
	if err.Op != "timeseries.shard" {
		t.Errorf("Op = %q, want %q", err.Op, "timeseries.shard")
	}
	if !err.Retry {
		t.Error("ShardUnavailable error should be retryable")
	}

	var tsShardUnavailable *TimeSeriesShardUnavailableError
	if !errors.As(err.Err, &tsShardUnavailable) {
		t.Error("Err should be *TimeSeriesShardUnavailableError")
	}
	if tsShardUnavailable.ShardID != shardID {
		t.Errorf("ShardID = %d, want %d", tsShardUnavailable.ShardID, shardID)
	}
}

func TestIsTimeSeriesError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "time series not found error",
			err:  NewTimeSeriesNotFoundError("cpu.usage"),
			want: true,
		},
		{
			name: "time series invalid timestamp error",
			err:  NewTimeSeriesInvalidTimestampError(-1, "negative"),
			want: true,
		},
		{
			name: "time series retention violation error",
			err:  NewTimeSeriesRetentionViolationError("disk.io", 30),
			want: true,
		},
		{
			name: "time series query timeout error",
			err:  NewTimeSeriesQueryTimeoutError("SELECT *", "30s"),
			want: true,
		},
		{
			name: "time series batch too large error",
			err:  NewTimeSeriesBatchTooLargeError(10000, 5000),
			want: true,
		},
		{
			name: "time series invalid metric error",
			err:  NewTimeSeriesInvalidMetricError("cpu@usage", "invalid"),
			want: true,
		},
		{
			name: "time series invalid tags error",
			err:  NewTimeSeriesInvalidTagsError(map[string]string{"a": ""}, "empty"),
			want: true,
		},
		{
			name: "time series shard unavailable error",
			err:  NewTimeSeriesShardUnavailableError(1, "offline"),
			want: true,
		},
		{
			name: "raw time series not found error",
			err:  &TimeSeriesNotFoundError{Metric: "test"},
			want: true,
		},
		{
			name: "raw time series invalid timestamp error",
			err:  &TimeSeriesInvalidTimestampError{Timestamp: -1, Reason: "negative"},
			want: true,
		},
		{
			name: "raw time series retention violation error",
			err:  &TimeSeriesRetentionViolationError{Metric: "test", RetentionDays: 30},
			want: true,
		},
		{
			name: "raw time series query timeout error",
			err:  &TimeSeriesQueryTimeoutError{Query: "test", Timeout: "30s"},
			want: true,
		},
		{
			name: "raw time series batch too large error",
			err:  &TimeSeriesBatchTooLargeError{BatchSize: 100, MaxSize: 50},
			want: true,
		},
		{
			name: "raw time series invalid metric error",
			err:  &TimeSeriesInvalidMetricError{Metric: "test", Reason: "invalid"},
			want: true,
		},
		{
			name: "raw time series invalid tags error",
			err:  &TimeSeriesInvalidTagsError{Tags: nil, Reason: "nil"},
			want: true,
		},
		{
			name: "raw time series shard unavailable error",
			err:  &TimeSeriesShardUnavailableError{ShardID: 1, Reason: "offline"},
			want: true,
		},
		{
			name: "non-time series dragon error",
			err:  &DragonError{Code: ErrNotFound, Op: "test"},
			want: false,
		},
		{
			name: "plain error",
			err:  errors.New("some error"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsTimeSeriesError(tt.err); got != tt.want {
				t.Errorf("IsTimeSeriesError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsTimeSeriesNotFound(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "time series not found error",
			err:  NewTimeSeriesNotFoundError("cpu.usage"),
			want: true,
		},
		{
			name: "raw time series not found error",
			err:  &TimeSeriesNotFoundError{Metric: "test"},
			want: true,
		},
		{
			name: "different dragon error",
			err:  &DragonError{Code: ErrNotFound, Op: "test"},
			want: false,
		},
		{
			name: "different time series error",
			err:  NewTimeSeriesRetentionViolationError("test", 30),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsTimeSeriesNotFound(tt.err); got != tt.want {
				t.Errorf("IsTimeSeriesNotFound() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsTimeSeriesInvalidTimestamp(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "time series invalid timestamp error",
			err:  NewTimeSeriesInvalidTimestampError(-1, "negative"),
			want: true,
		},
		{
			name: "raw time series invalid timestamp error",
			err:  &TimeSeriesInvalidTimestampError{Timestamp: -1, Reason: "negative"},
			want: true,
		},
		{
			name: "different error",
			err:  NewTimeSeriesNotFoundError("test"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsTimeSeriesInvalidTimestamp(tt.err); got != tt.want {
				t.Errorf("IsTimeSeriesInvalidTimestamp() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsTimeSeriesRetentionViolation(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "time series retention violation error",
			err:  NewTimeSeriesRetentionViolationError("disk.io", 30),
			want: true,
		},
		{
			name: "raw time series retention violation error",
			err:  &TimeSeriesRetentionViolationError{Metric: "test", RetentionDays: 30},
			want: true,
		},
		{
			name: "different error",
			err:  NewTimeSeriesNotFoundError("test"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsTimeSeriesRetentionViolation(tt.err); got != tt.want {
				t.Errorf("IsTimeSeriesRetentionViolation() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsTimeSeriesQueryTimeout(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "time series query timeout error",
			err:  NewTimeSeriesQueryTimeoutError("SELECT *", "30s"),
			want: true,
		},
		{
			name: "raw time series query timeout error",
			err:  &TimeSeriesQueryTimeoutError{Query: "test", Timeout: "30s"},
			want: true,
		},
		{
			name: "different error",
			err:  NewTimeSeriesNotFoundError("test"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsTimeSeriesQueryTimeout(tt.err); got != tt.want {
				t.Errorf("IsTimeSeriesQueryTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsTimeSeriesBatchTooLarge(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "time series batch too large error",
			err:  NewTimeSeriesBatchTooLargeError(10000, 5000),
			want: true,
		},
		{
			name: "raw time series batch too large error",
			err:  &TimeSeriesBatchTooLargeError{BatchSize: 100, MaxSize: 50},
			want: true,
		},
		{
			name: "different error",
			err:  NewTimeSeriesNotFoundError("test"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsTimeSeriesBatchTooLarge(tt.err); got != tt.want {
				t.Errorf("IsTimeSeriesBatchTooLarge() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsTimeSeriesInvalidMetric(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "time series invalid metric error",
			err:  NewTimeSeriesInvalidMetricError("cpu@usage", "invalid"),
			want: true,
		},
		{
			name: "raw time series invalid metric error",
			err:  &TimeSeriesInvalidMetricError{Metric: "test", Reason: "invalid"},
			want: true,
		},
		{
			name: "different error",
			err:  NewTimeSeriesNotFoundError("test"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsTimeSeriesInvalidMetric(tt.err); got != tt.want {
				t.Errorf("IsTimeSeriesInvalidMetric() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsTimeSeriesInvalidTags(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "time series invalid tags error",
			err:  NewTimeSeriesInvalidTagsError(map[string]string{"a": ""}, "empty"),
			want: true,
		},
		{
			name: "raw time series invalid tags error",
			err:  &TimeSeriesInvalidTagsError{Tags: nil, Reason: "nil"},
			want: true,
		},
		{
			name: "different error",
			err:  NewTimeSeriesNotFoundError("test"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsTimeSeriesInvalidTags(tt.err); got != tt.want {
				t.Errorf("IsTimeSeriesInvalidTags() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsTimeSeriesShardUnavailable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "time series shard unavailable error",
			err:  NewTimeSeriesShardUnavailableError(1, "offline"),
			want: true,
		},
		{
			name: "raw time series shard unavailable error",
			err:  &TimeSeriesShardUnavailableError{ShardID: 1, Reason: "offline"},
			want: true,
		},
		{
			name: "different error",
			err:  NewTimeSeriesNotFoundError("test"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsTimeSeriesShardUnavailable(tt.err); got != tt.want {
				t.Errorf("IsTimeSeriesShardUnavailable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTimeSeriesErrorUnwrap(t *testing.T) {
	notFoundErr := NewTimeSeriesNotFoundError("cpu.usage")

	var tsNotFound *TimeSeriesNotFoundError
	if !errors.As(notFoundErr, &tsNotFound) {
		t.Error("errors.As should find TimeSeriesNotFoundError")
	}

	var de *DragonError
	if !errors.As(notFoundErr, &de) {
		t.Error("errors.As should find DragonError")
	}
}

func TestTimeSeriesErrorCodes(t *testing.T) {
	codes := []struct {
		code     ErrorCode
		expected ErrorCode
	}{
		{ErrTimeSeriesNotFound, ErrorCode(1000)},
		{ErrTimeSeriesInvalidTimestamp, ErrorCode(1001)},
		{ErrTimeSeriesRetentionViolation, ErrorCode(1002)},
		{ErrTimeSeriesQueryTimeout, ErrorCode(1003)},
		{ErrTimeSeriesBatchTooLarge, ErrorCode(1004)},
		{ErrTimeSeriesInvalidMetric, ErrorCode(1005)},
		{ErrTimeSeriesInvalidTags, ErrorCode(1006)},
		{ErrTimeSeriesShardUnavailable, ErrorCode(1007)},
	}

	for _, tc := range codes {
		if tc.code != tc.expected {
			t.Errorf("ErrorCode %v does not match expected %v", tc.code, tc.expected)
		}
	}
}

func TestTimeSeriesErrorRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  *DragonError
		want bool
	}{
		{
			name: "query timeout is retryable",
			err:  NewTimeSeriesQueryTimeoutError("SELECT *", "30s"),
			want: true,
		},
		{
			name: "shard unavailable is retryable",
			err:  NewTimeSeriesShardUnavailableError(1, "offline"),
			want: true,
		},
		{
			name: "not found is not retryable",
			err:  NewTimeSeriesNotFoundError("cpu.usage"),
			want: false,
		},
		{
			name: "invalid metric is not retryable",
			err:  NewTimeSeriesInvalidMetricError("cpu@usage", "invalid"),
			want: false,
		},
		{
			name: "batch too large is not retryable",
			err:  NewTimeSeriesBatchTooLargeError(10000, 5000),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.IsRetryable(); got != tt.want {
				t.Errorf("IsRetryable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTimeSeriesErrorWithContext(t *testing.T) {
	err := NewTimeSeriesNotFoundError("cpu.usage")
	err = err.WithContext("namespace", "production")

	if err.Context["namespace"] != "production" {
		t.Errorf("Context[namespace] = %v, want %q", err.Context["namespace"], "production")
	}
	if err.Context["metric"] != "cpu.usage" {
		t.Errorf("Context[metric] = %v, want %q", err.Context["metric"], "cpu.usage")
	}
}

func TestTimeSeriesErrorMessage(t *testing.T) {
	tests := []struct {
		name     string
		err      *DragonError
		contains []string
	}{
		{
			name: "not found error message",
			err:  NewTimeSeriesNotFoundError("cpu.usage"),
			contains: []string{
				"timeseries",
				"not found",
				"cpu.usage",
			},
		},
		{
			name: "retention violation error message",
			err:  NewTimeSeriesRetentionViolationError("disk.io", 30),
			contains: []string{
				"timeseries",
				"retention",
				"disk.io",
				"30",
			},
		},
		{
			name: "batch too large error message",
			err:  NewTimeSeriesBatchTooLargeError(10000, 5000),
			contains: []string{
				"timeseries.batch",
				"10000",
				"5000",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errStr := tt.err.Error()
			for _, s := range tt.contains {
				if !strings.Contains(errStr, s) {
					t.Errorf("Error() = %q, want to contain %q", errStr, s)
				}
			}
		})
	}
}
