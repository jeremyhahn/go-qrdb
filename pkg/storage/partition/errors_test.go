// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package partition

import (
	"errors"
	"testing"
)

func TestPartitionNotFoundError(t *testing.T) {
	err := &PartitionNotFoundError{PartitionID: 42}
	expected := "partition not found: 42"
	if err.Error() != expected {
		t.Errorf("got %q, want %q", err.Error(), expected)
	}
}

func TestInvalidPartitionCountError(t *testing.T) {
	err := &InvalidPartitionCountError{Count: 0}
	expected := "invalid partition count: 0"
	if err.Error() != expected {
		t.Errorf("got %q, want %q", err.Error(), expected)
	}
}

func TestPartitionCreateError(t *testing.T) {
	inner := errors.New("disk full")
	err := &PartitionCreateError{PartitionID: 3, Err: inner}
	expected := "failed to create partition 3: disk full"
	if err.Error() != expected {
		t.Errorf("got %q, want %q", err.Error(), expected)
	}
	if !errors.Is(err, inner) {
		t.Error("Unwrap should return inner error")
	}
}

func TestPartitionCloseError(t *testing.T) {
	inner := errors.New("close failed")
	err := &PartitionCloseError{PartitionID: 7, Err: inner}
	expected := "failed to close partition 7: close failed"
	if err.Error() != expected {
		t.Errorf("got %q, want %q", err.Error(), expected)
	}
	if !errors.Is(err, inner) {
		t.Error("Unwrap should return inner error")
	}
}

func TestMergeIteratorError(t *testing.T) {
	t.Run("with underlying error", func(t *testing.T) {
		inner := errors.New("bad read")
		err := &MergeIteratorError{Message: "partition 0", Err: inner}
		expected := "merge iterator: partition 0: bad read"
		if err.Error() != expected {
			t.Errorf("got %q, want %q", err.Error(), expected)
		}
		if !errors.Is(err, inner) {
			t.Error("Unwrap should return inner error")
		}
	})

	t.Run("without underlying error", func(t *testing.T) {
		err := &MergeIteratorError{Message: "no iterators"}
		expected := "merge iterator: no iterators"
		if err.Error() != expected {
			t.Errorf("got %q, want %q", err.Error(), expected)
		}
		if err.Unwrap() != nil {
			t.Error("Unwrap should return nil when no inner error")
		}
	})
}

func TestConfigError(t *testing.T) {
	err := &ConfigError{Field: "PartitionCount", Message: "must be greater than zero"}
	expected := "partition config: PartitionCount: must be greater than zero"
	if err.Error() != expected {
		t.Errorf("got %q, want %q", err.Error(), expected)
	}
}

func TestBatchCommitError(t *testing.T) {
	errs := []error{errors.New("a"), errors.New("b")}
	err := &BatchCommitError{Errors: errs}
	expected := "partition batch: 2 partition(s) failed to commit"
	if err.Error() != expected {
		t.Errorf("got %q, want %q", err.Error(), expected)
	}
}
