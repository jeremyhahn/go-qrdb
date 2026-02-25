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

import "time"

// ClosedError is returned when an operation is attempted on closed storage.
type ClosedError struct{}

// Error returns a human-readable description indicating the storage is closed.
func (e *ClosedError) Error() string {
	return "timeseries: storage is closed"
}

// NilDBError is returned when a nil PebbleDB is passed.
type NilDBError struct{}

// Error returns a human-readable description indicating a nil database.
func (e *NilDBError) Error() string {
	return "timeseries: pebble db is nil"
}

// InvalidMetricError is returned when a metric name is empty.
type InvalidMetricError struct{}

// Error returns a human-readable description indicating an empty metric name.
func (e *InvalidMetricError) Error() string {
	return "timeseries: metric name must not be empty"
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
