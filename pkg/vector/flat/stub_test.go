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

//go:build !qrdb_vector

package flat

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/vector/hnsw"
)

// TestStubNewIndex verifies the stub constructor returns a usable handle
// whose operations report that vector support is not compiled in.
func TestStubNewIndex(t *testing.T) {
	idx := NewIndex(hnsw.L2Distance)
	if idx == nil {
		t.Fatal("NewIndex returned nil")
	}

	if err := idx.Insert(1, []float32{1, 2, 3}); !errors.Is(err, ErrNotCompiled) {
		t.Errorf("Insert error = %v, want ErrNotCompiled", err)
	}
	if err := idx.Delete(1); !errors.Is(err, ErrNotCompiled) {
		t.Errorf("Delete error = %v, want ErrNotCompiled", err)
	}
	if results, err := idx.Search([]float32{1, 2, 3}, 5); !errors.Is(err, ErrNotCompiled) || results != nil {
		t.Errorf("Search = (%v, %v), want (nil, ErrNotCompiled)", results, err)
	}
}

// TestStubIndexZeroState verifies the stub index reports empty state and
// closes cleanly.
func TestStubIndexZeroState(t *testing.T) {
	idx := NewIndex(hnsw.CosineDistance)
	if count := idx.Count(); count != 0 {
		t.Errorf("Count() = %d, want 0", count)
	}
	if dims := idx.Dimensions(); dims != 0 {
		t.Errorf("Dimensions() = %d, want 0", dims)
	}
	if err := idx.Close(); err != nil {
		t.Errorf("Close() error = %v, want nil", err)
	}
}

// TestStubDimensionMismatchError verifies the stub error type formats its
// message and unwraps to the ErrInvalidVector sentinel.
func TestStubDimensionMismatchError(t *testing.T) {
	err := &DimensionMismatchError{Got: 3, Expected: 8}
	want := "dimension mismatch: got 3, expected 8"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, ErrInvalidVector) {
		t.Error("DimensionMismatchError does not unwrap to ErrInvalidVector")
	}
}
