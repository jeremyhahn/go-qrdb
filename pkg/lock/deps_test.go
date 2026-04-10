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

package lock

import (
	"testing"
)

// ---------------------------------------------------------------------------
// RangeOption helpers
// ---------------------------------------------------------------------------

func TestWithLimit_SetsLimit(t *testing.T) {
	var ro RangeOptions
	WithLimit(50)(&ro)
	if ro.Limit != 50 {
		t.Errorf("Limit = %d, want 50", ro.Limit)
	}
}

func TestWithRevision_SetsRevision(t *testing.T) {
	var ro RangeOptions
	WithRevision(123)(&ro)
	if ro.Revision != 123 {
		t.Errorf("Revision = %d, want 123", ro.Revision)
	}
}

func TestWithCountOnly_SetsCountOnly(t *testing.T) {
	var ro RangeOptions
	WithCountOnly()(&ro)
	if !ro.CountOnly {
		t.Error("CountOnly should be true")
	}
}

func TestWithKeysOnly_SetsKeysOnly(t *testing.T) {
	var ro RangeOptions
	WithKeysOnly()(&ro)
	if !ro.KeysOnly {
		t.Error("KeysOnly should be true")
	}
}

func TestWithSort_SetsSortOrderAndTarget(t *testing.T) {
	var ro RangeOptions
	WithSort(SortDescend, SortByModRevision)(&ro)
	if ro.SortOrder != SortDescend {
		t.Errorf("SortOrder = %v, want SortDescend", ro.SortOrder)
	}
	if ro.SortTarget != SortByModRevision {
		t.Errorf("SortTarget = %v, want SortByModRevision", ro.SortTarget)
	}
}

func TestApplyRangeOptions_AppliesAllOptions(t *testing.T) {
	ro := ApplyRangeOptions([]RangeOption{
		WithLimit(10),
		WithRevision(7),
		WithCountOnly(),
		WithKeysOnly(),
		WithSort(SortAscend, SortByKey),
	})
	if ro.Limit != 10 {
		t.Errorf("Limit = %d, want 10", ro.Limit)
	}
	if ro.Revision != 7 {
		t.Errorf("Revision = %d, want 7", ro.Revision)
	}
	if !ro.CountOnly {
		t.Error("CountOnly should be true")
	}
	if !ro.KeysOnly {
		t.Error("KeysOnly should be true")
	}
	if ro.SortOrder != SortAscend {
		t.Errorf("SortOrder = %v, want SortAscend", ro.SortOrder)
	}
	if ro.SortTarget != SortByKey {
		t.Errorf("SortTarget = %v, want SortByKey", ro.SortTarget)
	}
}

func TestApplyRangeOptions_EmptySlice(t *testing.T) {
	ro := ApplyRangeOptions(nil)
	if ro.Limit != 0 || ro.Revision != 0 || ro.CountOnly || ro.KeysOnly {
		t.Errorf("expected zero RangeOptions for nil opts, got %+v", ro)
	}
}

// ---------------------------------------------------------------------------
// prefixEnd
// ---------------------------------------------------------------------------

func TestPrefixEnd_AllFF_ReturnsNil(t *testing.T) {
	result := prefixEnd([]byte{0xFF, 0xFF, 0xFF})
	if result != nil {
		t.Errorf("expected nil for all-0xFF prefix, got %v", result)
	}
}

func TestPrefixEnd_Normal_IncrementsLastByte(t *testing.T) {
	result := prefixEnd([]byte{'a', 'b', 'c'})
	if string(result) != "abd" {
		t.Errorf("prefixEnd(%q) = %q, want %q", "abc", result, "abd")
	}
}
