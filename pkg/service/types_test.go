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

package service

import "testing"

func TestApplyRangeOptionsDefaults(t *testing.T) {
	opts := ApplyRangeOptions(nil)
	if opts.Limit != 0 {
		t.Errorf("default Limit = %d, want 0", opts.Limit)
	}
	if opts.Revision != 0 {
		t.Errorf("default Revision = %d, want 0", opts.Revision)
	}
	if opts.CountOnly {
		t.Error("default CountOnly = true, want false")
	}
	if opts.KeysOnly {
		t.Error("default KeysOnly = true, want false")
	}
}

func TestApplyRangeOptionsWithLimit(t *testing.T) {
	opts := ApplyRangeOptions([]RangeOption{WithLimit(100)})
	if opts.Limit != 100 {
		t.Errorf("Limit = %d, want 100", opts.Limit)
	}
}

func TestApplyRangeOptionsWithRevision(t *testing.T) {
	opts := ApplyRangeOptions([]RangeOption{WithRevision(42)})
	if opts.Revision != 42 {
		t.Errorf("Revision = %d, want 42", opts.Revision)
	}
}

func TestApplyRangeOptionsWithCountOnly(t *testing.T) {
	opts := ApplyRangeOptions([]RangeOption{WithCountOnly()})
	if !opts.CountOnly {
		t.Error("CountOnly = false, want true")
	}
}

func TestApplyRangeOptionsWithKeysOnly(t *testing.T) {
	opts := ApplyRangeOptions([]RangeOption{WithKeysOnly()})
	if !opts.KeysOnly {
		t.Error("KeysOnly = false, want true")
	}
}

func TestApplyRangeOptionsWithSort(t *testing.T) {
	opts := ApplyRangeOptions([]RangeOption{WithSort(SortDescend, SortByModRevision)})
	if opts.SortOrder != SortDescend {
		t.Errorf("SortOrder = %d, want SortDescend", opts.SortOrder)
	}
	if opts.SortTarget != SortByModRevision {
		t.Errorf("SortTarget = %d, want SortByModRevision", opts.SortTarget)
	}
}

