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

package batch

import (
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// ClosedError
// ---------------------------------------------------------------------------

func Test_ClosedError_errorMessage(t *testing.T) {
	t.Parallel()
	err := &ClosedError{}
	want := "quicraft/batch: aggregator closed"
	if got := err.Error(); got != want {
		t.Errorf("ClosedError.Error() = %q, want %q", got, want)
	}
}

func Test_ClosedError_matchesSentinel(t *testing.T) {
	t.Parallel()
	err := &ClosedError{}

	if !errors.Is(err, ErrClosed) {
		t.Error("ClosedError should match ErrClosed sentinel via errors.Is")
	}
	if errors.Is(err, ErrQueueFull) {
		t.Error("ClosedError should not match ErrQueueFull")
	}
	if errors.Is(err, ErrNilProposer) {
		t.Error("ClosedError should not match ErrNilProposer")
	}
}

func Test_ClosedError_errorsAs(t *testing.T) {
	t.Parallel()
	var target *ClosedError
	err := &ClosedError{}

	if !errors.As(err, &target) {
		t.Error("errors.As should match *ClosedError")
	}
}

// ---------------------------------------------------------------------------
// QueueFullError
// ---------------------------------------------------------------------------

func Test_QueueFullError_errorMessage(t *testing.T) {
	t.Parallel()
	err := &QueueFullError{QueueSize: 5000}
	want := "quicraft/batch: queue full (capacity 5000)"
	if got := err.Error(); got != want {
		t.Errorf("QueueFullError.Error() = %q, want %q", got, want)
	}
}

func Test_QueueFullError_matchesSentinel(t *testing.T) {
	t.Parallel()
	err := &QueueFullError{QueueSize: 100}

	if !errors.Is(err, ErrQueueFull) {
		t.Error("QueueFullError should match ErrQueueFull sentinel via errors.Is")
	}
	if errors.Is(err, ErrClosed) {
		t.Error("QueueFullError should not match ErrClosed")
	}
	if errors.Is(err, ErrFlush) {
		t.Error("QueueFullError should not match ErrFlush")
	}
}

func Test_QueueFullError_errorsAs(t *testing.T) {
	t.Parallel()
	var target *QueueFullError
	err := &QueueFullError{QueueSize: 256}

	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *QueueFullError")
	}
	if target.QueueSize != 256 {
		t.Errorf("QueueSize = %d, want 256", target.QueueSize)
	}
}

func Test_QueueFullError_doesNotMatchClosedError(t *testing.T) {
	t.Parallel()
	var target *ClosedError
	err := &QueueFullError{QueueSize: 10}

	if errors.As(err, &target) {
		t.Error("errors.As should not match *ClosedError for a QueueFullError")
	}
}

// ---------------------------------------------------------------------------
// NilProposerError
// ---------------------------------------------------------------------------

func Test_NilProposerError_errorMessage(t *testing.T) {
	t.Parallel()
	err := &NilProposerError{}
	want := "quicraft/batch: proposer is nil"
	if got := err.Error(); got != want {
		t.Errorf("NilProposerError.Error() = %q, want %q", got, want)
	}
}

func Test_NilProposerError_matchesSentinel(t *testing.T) {
	t.Parallel()
	err := &NilProposerError{}

	if !errors.Is(err, ErrNilProposer) {
		t.Error("NilProposerError should match ErrNilProposer sentinel via errors.Is")
	}
	if errors.Is(err, ErrFlush) {
		t.Error("NilProposerError should not match ErrFlush")
	}
	if errors.Is(err, ErrDecode) {
		t.Error("NilProposerError should not match ErrDecode")
	}
}

func Test_NilProposerError_errorsAs(t *testing.T) {
	t.Parallel()
	var target *NilProposerError
	err := &NilProposerError{}

	if !errors.As(err, &target) {
		t.Error("errors.As should match *NilProposerError")
	}
}

// ---------------------------------------------------------------------------
// FlushError
// ---------------------------------------------------------------------------

func Test_FlushError_errorMessage(t *testing.T) {
	t.Parallel()
	inner := errors.New("proposer unavailable")
	err := &FlushError{ShardID: 42, Err: inner}
	want := "quicraft/batch: flush failed for shard 42: proposer unavailable"
	if got := err.Error(); got != want {
		t.Errorf("FlushError.Error() = %q, want %q", got, want)
	}
}

func Test_FlushError_matchesSentinel(t *testing.T) {
	t.Parallel()
	inner := errors.New("some error")
	err := &FlushError{ShardID: 1, Err: inner}

	if !errors.Is(err, ErrFlush) {
		t.Error("FlushError should match ErrFlush sentinel via errors.Is")
	}
	if errors.Is(err, ErrDecode) {
		t.Error("FlushError should not match ErrDecode")
	}
	if errors.Is(err, ErrClosed) {
		t.Error("FlushError should not match ErrClosed")
	}
}

func Test_FlushError_unwrap(t *testing.T) {
	t.Parallel()
	inner := errors.New("wrapped cause")
	err := &FlushError{ShardID: 7, Err: inner}

	if unwrapped := err.Unwrap(); unwrapped != inner {
		t.Errorf("Unwrap() = %v, want %v", unwrapped, inner)
	}
}

func Test_FlushError_unwrapNilErr(t *testing.T) {
	t.Parallel()
	err := &FlushError{ShardID: 1, Err: nil}

	if unwrapped := err.Unwrap(); unwrapped != nil {
		t.Errorf("Unwrap() = %v, want nil", unwrapped)
	}
}

func Test_FlushError_errorsAs(t *testing.T) {
	t.Parallel()
	var target *FlushError
	inner := errors.New("test")
	err := &FlushError{ShardID: 99, Err: inner}

	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *FlushError")
	}
	if target.ShardID != 99 {
		t.Errorf("ShardID = %d, want 99", target.ShardID)
	}
}

// ---------------------------------------------------------------------------
// DecodeError
// ---------------------------------------------------------------------------

func Test_DecodeError_errorMessage(t *testing.T) {
	t.Parallel()
	err := &DecodeError{Reason: "truncated length prefix"}
	want := "quicraft/batch: decode failed: truncated length prefix"
	if got := err.Error(); got != want {
		t.Errorf("DecodeError.Error() = %q, want %q", got, want)
	}
}

func Test_DecodeError_matchesSentinel(t *testing.T) {
	t.Parallel()
	err := &DecodeError{Reason: "bad data"}

	if !errors.Is(err, ErrDecode) {
		t.Error("DecodeError should match ErrDecode sentinel via errors.Is")
	}
	if errors.Is(err, ErrFlush) {
		t.Error("DecodeError should not match ErrFlush")
	}
	if errors.Is(err, ErrClosed) {
		t.Error("DecodeError should not match ErrClosed")
	}
}

func Test_DecodeError_errorsAs(t *testing.T) {
	t.Parallel()
	var target *DecodeError
	err := &DecodeError{Reason: "command data exceeds payload boundary"}

	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *DecodeError")
	}
	if target.Reason != "command data exceeds payload boundary" {
		t.Errorf("Reason = %q, want %q",
			target.Reason, "command data exceeds payload boundary")
	}
}

// ---------------------------------------------------------------------------
// Error types are distinct from each other
// ---------------------------------------------------------------------------

func Test_ErrorTypes_sentinelErrorMessages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		err     error
		contain string
	}{
		{"ErrClosed", ErrClosed, "aggregator closed"},
		{"ErrQueueFull", ErrQueueFull, "queue full"},
		{"ErrNilProposer", ErrNilProposer, "proposer is nil"},
		{"ErrFlush", ErrFlush, "flush failed"},
		{"ErrDecode", ErrDecode, "decode failed"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if !strings.Contains(tc.err.Error(), tc.contain) {
				t.Errorf("%s.Error() = %q, should contain %q",
					tc.name, tc.err.Error(), tc.contain)
			}
		})
	}
}

func Test_ErrorTypes_sentinelPrefixes(t *testing.T) {
	t.Parallel()

	sentinels := []error{ErrClosed, ErrQueueFull, ErrNilProposer, ErrFlush, ErrDecode}
	for _, s := range sentinels {
		if !strings.HasPrefix(s.Error(), "quicraft/batch:") {
			t.Errorf("sentinel %q should have prefix %q", s.Error(), "quicraft/batch:")
		}
	}
}
