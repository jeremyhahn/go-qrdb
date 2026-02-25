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
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// ClosedError
// ---------------------------------------------------------------------------

func TestClosedError_Error_ContainsMessage(t *testing.T) {
	e := &ClosedError{}
	got := e.Error()
	if !strings.Contains(got, "storage is closed") {
		t.Errorf("Error: expected 'storage is closed', got %q", got)
	}
}

func TestClosedError_Error_HasPrefix(t *testing.T) {
	e := &ClosedError{}
	got := e.Error()
	if !strings.HasPrefix(got, "timeseries:") {
		t.Errorf("Error: expected 'timeseries:' prefix, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// NilDBError
// ---------------------------------------------------------------------------

func TestNilDBError_Error_ContainsMessage(t *testing.T) {
	e := &NilDBError{}
	got := e.Error()
	if !strings.Contains(got, "pebble db is nil") {
		t.Errorf("Error: expected nil db message, got %q", got)
	}
}

func TestNilDBError_Error_HasPrefix(t *testing.T) {
	e := &NilDBError{}
	got := e.Error()
	if !strings.HasPrefix(got, "timeseries:") {
		t.Errorf("Error: expected 'timeseries:' prefix, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// InvalidMetricError
// ---------------------------------------------------------------------------

func TestInvalidMetricError_Error_ContainsMessage(t *testing.T) {
	e := &InvalidMetricError{}
	got := e.Error()
	if !strings.Contains(got, "metric name must not be empty") {
		t.Errorf("Error: expected metric message, got %q", got)
	}
}

func TestInvalidMetricError_Error_HasPrefix(t *testing.T) {
	e := &InvalidMetricError{}
	got := e.Error()
	if !strings.HasPrefix(got, "timeseries:") {
		t.Errorf("Error: expected 'timeseries:' prefix, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// InvalidTimestampError
// ---------------------------------------------------------------------------

func TestInvalidTimestampError_Error_ContainsMessage(t *testing.T) {
	e := &InvalidTimestampError{}
	got := e.Error()
	if !strings.Contains(got, "timestamp must not be zero") {
		t.Errorf("Error: expected timestamp message, got %q", got)
	}
}

func TestInvalidTimestampError_Error_HasPrefix(t *testing.T) {
	e := &InvalidTimestampError{}
	got := e.Error()
	if !strings.HasPrefix(got, "timeseries:") {
		t.Errorf("Error: expected 'timeseries:' prefix, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// InvalidTimeRangeError
// ---------------------------------------------------------------------------

func TestInvalidTimeRangeError_Error_ContainsTimes(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	e := &InvalidTimeRangeError{Start: start, End: end}
	got := e.Error()
	if !strings.Contains(got, "2026") {
		t.Errorf("Error: expected start year in message, got %q", got)
	}
	if !strings.Contains(got, "2025") {
		t.Errorf("Error: expected end year in message, got %q", got)
	}
	if !strings.Contains(got, "before") {
		t.Errorf("Error: expected 'before' in message, got %q", got)
	}
}

func TestInvalidTimeRangeError_Error_HasPrefix(t *testing.T) {
	e := &InvalidTimeRangeError{
		Start: time.Now(),
		End:   time.Now().Add(-time.Hour),
	}
	got := e.Error()
	if !strings.HasPrefix(got, "timeseries:") {
		t.Errorf("Error: expected 'timeseries:' prefix, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// DecodeError
// ---------------------------------------------------------------------------

func TestDecodeError_Error_ContainsReason(t *testing.T) {
	e := &DecodeError{Reason: "data too short"}
	got := e.Error()
	if !strings.Contains(got, "data too short") {
		t.Errorf("Error: expected reason, got %q", got)
	}
	if !strings.Contains(got, "decode failed") {
		t.Errorf("Error: expected 'decode failed', got %q", got)
	}
}

func TestDecodeError_Error_EmptyReason(t *testing.T) {
	e := &DecodeError{Reason: ""}
	got := e.Error()
	if !strings.Contains(got, "timeseries: decode failed:") {
		t.Errorf("Error: expected prefix for empty reason, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// WriteError
// ---------------------------------------------------------------------------

func TestWriteError_Error_ContainsInnerErr(t *testing.T) {
	inner := errors.New("disk full")
	e := &WriteError{Err: inner}
	got := e.Error()
	if !strings.Contains(got, "disk full") {
		t.Errorf("Error: expected inner error, got %q", got)
	}
	if !strings.Contains(got, "write failed") {
		t.Errorf("Error: expected 'write failed', got %q", got)
	}
}

func TestWriteError_Unwrap_ReturnsInnerError(t *testing.T) {
	inner := io.ErrUnexpectedEOF
	e := &WriteError{Err: inner}
	if !errors.Is(e, io.ErrUnexpectedEOF) {
		t.Error("Unwrap: errors.Is failed for inner error")
	}
}

func TestWriteError_Unwrap_MatchesAs(t *testing.T) {
	inner := errors.New("batch commit")
	e := &WriteError{Err: inner}
	var target *WriteError
	if !errors.As(e, &target) {
		t.Error("Unwrap: errors.As failed for WriteError")
	}
}

// ---------------------------------------------------------------------------
// ReadError
// ---------------------------------------------------------------------------

func TestReadError_Error_ContainsInnerErr(t *testing.T) {
	inner := errors.New("iterator failed")
	e := &ReadError{Err: inner}
	got := e.Error()
	if !strings.Contains(got, "iterator failed") {
		t.Errorf("Error: expected inner error, got %q", got)
	}
	if !strings.Contains(got, "read failed") {
		t.Errorf("Error: expected 'read failed', got %q", got)
	}
}

func TestReadError_Unwrap_ReturnsInnerError(t *testing.T) {
	inner := io.EOF
	e := &ReadError{Err: inner}
	if !errors.Is(e, io.EOF) {
		t.Error("Unwrap: errors.Is failed for inner error")
	}
}

func TestReadError_Unwrap_MatchesAs(t *testing.T) {
	inner := errors.New("seek failed")
	e := &ReadError{Err: inner}
	var target *ReadError
	if !errors.As(e, &target) {
		t.Error("Unwrap: errors.As failed for ReadError")
	}
}
