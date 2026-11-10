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

package timeseries

import (
	"fmt"
	"strconv"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// NotCompiledError reports that time-series support was not built into this
// binary. It is the refusal every operation on the stub Storage returns.
//
// It is a TYPE, not a bare errors.New sentinel, because the Apply classifier
// reads the protocol.CapabilityError declaration below to tell a refusal from
// a fault. As an untyped sentinel this error took the fatal branch: one
// refused time-series write halted the shard's state machine, and every later
// linearizable read on it — including plain KV reads of keys written and read
// successfully microseconds earlier — burned the caller's deadline and
// reported a consensus timeout (go-qrdb#520). The sentence "time-series
// support is not compiled in" was always an absent capability; it just had no
// way to say so.
type NotCompiledError struct{}

// Error returns the refusal, naming the build tag that enables the subsystem.
func (e *NotCompiledError) Error() string {
	return "timeseries support not compiled: build with -tags qrdb_ts"
}

// Capability names the absent subsystem, declaring this an application-level
// refusal rather than a fault. See protocol.CapabilityError.
func (e *NotCompiledError) Capability() string {
	return "timeseries"
}

// NotCompiledError is an absent capability, not a fault.
var _ protocol.CapabilityError = (*NotCompiledError)(nil)

// ClosedError is returned when an operation is attempted on closed storage.
type ClosedError struct{}

// Error returns a human-readable description indicating the storage is closed.
func (e *ClosedError) Error() string {
	return "timeseries: storage is closed"
}

// NilDBError is returned when a nil storage.Storage is passed to New.
type NilDBError struct{}

// Error returns a human-readable description indicating a nil storage.
func (e *NilDBError) Error() string {
	return "timeseries: storage is nil"
}

// EmptyDirError is returned when an empty data directory is provided to
// NewWithDir.
type EmptyDirError struct{}

// Error returns a human-readable description indicating the directory is empty.
func (e *EmptyDirError) Error() string {
	return "timeseries: data directory is empty"
}

// OpenDirError is returned when PebbleDB fails to open the specified directory.
type OpenDirError struct {
	Dir string
	Err error
}

// Error returns a human-readable description of the directory open failure.
func (e *OpenDirError) Error() string {
	return fmt.Sprintf("timeseries: failed to open directory %q: %v", e.Dir, e.Err)
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *OpenDirError) Unwrap() error {
	return e.Err
}

// InvalidMetricError is returned when a metric name is empty.
type InvalidMetricError struct{}

// Error returns a human-readable description indicating an empty metric name.
func (e *InvalidMetricError) Error() string {
	return "timeseries: metric name must not be empty"
}

// NilRetentionPolicyError is returned when a retention policy to store is nil.
type NilRetentionPolicyError struct{}

// Error returns a human-readable description indicating a nil retention policy.
func (e *NilRetentionPolicyError) Error() string {
	return "timeseries: retention policy must not be nil"
}

// InvalidTimestampError is returned when a timestamp is zero.
type InvalidTimestampError struct{}

// Error returns a human-readable description indicating a zero timestamp.
func (e *InvalidTimestampError) Error() string {
	return "timeseries: timestamp must not be zero"
}

// InvalidTimeRangeError is returned when end time is before start time.
type InvalidTimeRangeError struct {
	Start time.Time
	End   time.Time
}

// Error returns a human-readable description of the invalid time range.
func (e *InvalidTimeRangeError) Error() string {
	return "timeseries: end time " + e.End.String() +
		" is before start time " + e.Start.String()
}

// DecodeError is returned when data point decoding fails.
type DecodeError struct {
	Reason string
}

// Error returns a human-readable description of the decoding failure.
func (e *DecodeError) Error() string {
	return "timeseries: decode failed: " + e.Reason
}

// WriteError is returned when a PebbleDB write operation fails.
type WriteError struct {
	Err error
}

// Error returns a human-readable description of the write failure.
func (e *WriteError) Error() string {
	return "timeseries: write failed: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *WriteError) Unwrap() error {
	return e.Err
}

// ReadError is returned when a PebbleDB read operation fails.
type ReadError struct {
	Err error
}

// Error returns a human-readable description of the read failure.
func (e *ReadError) Error() string {
	return "timeseries: read failed: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *ReadError) Unwrap() error {
	return e.Err
}

// MetricLimitError is returned by AdmitMetrics when a write would take the
// shard past its metric cap.
type MetricLimitError struct {
	Max uint32
}

// Error implements the error interface.
func (e *MetricLimitError) Error() string {
	return "timeseries: shard holds its limit of " + strconv.FormatUint(uint64(e.Max), 10) + " metric names"
}

// ValidationError is returned when a request fails general validation.
type ValidationError struct {
	Op      string
	Message string
}

// Error returns a human-readable description of the validation failure.
func (e *ValidationError) Error() string {
	return "timeseries: " + e.Op + ": " + e.Message
}

// InvalidAggFuncError is returned when an unrecognized aggregation function
// is specified.
type InvalidAggFuncError struct {
	Func AggFunc
}

// Error returns a human-readable description of the invalid aggregation function.
func (e *InvalidAggFuncError) Error() string {
	return "timeseries: invalid aggregation function: " + strconv.Itoa(int(e.Func))
}

// InvalidBatchPointError is returned when a data point within a batch
// fails validation.
type InvalidBatchPointError struct {
	Index int
	Err   error
}

// Error returns a human-readable description identifying the failing point.
func (e *InvalidBatchPointError) Error() string {
	return "timeseries: invalid data point at index " + strconv.Itoa(e.Index) + ": " + e.Err.Error()
}

// Unwrap returns the underlying validation error.
func (e *InvalidBatchPointError) Unwrap() error {
	return e.Err
}
