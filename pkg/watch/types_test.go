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

package watch

import "testing"

func TestApplyWatchOptionsDefaults(t *testing.T) {
	opts := ApplyWatchOptions(nil)
	if opts.StartRevision != 0 {
		t.Errorf("default StartRevision = %d, want 0", opts.StartRevision)
	}
	if opts.PrevKV {
		t.Error("default PrevKV = true, want false")
	}
	if opts.ProgressNotify {
		t.Error("default ProgressNotify = true, want false")
	}
	if len(opts.Filters) != 0 {
		t.Errorf("default Filters = %v, want empty", opts.Filters)
	}
}

func TestApplyWatchOptionsWithStartRevision(t *testing.T) {
	opts := ApplyWatchOptions([]WatchOption{WithStartRevision(99)})
	if opts.StartRevision != 99 {
		t.Errorf("StartRevision = %d, want 99", opts.StartRevision)
	}
}

func TestApplyWatchOptionsWithPrevKV(t *testing.T) {
	opts := ApplyWatchOptions([]WatchOption{WithPrevKV()})
	if !opts.PrevKV {
		t.Error("PrevKV = false, want true")
	}
}

func TestApplyWatchOptionsWithProgressNotify(t *testing.T) {
	opts := ApplyWatchOptions([]WatchOption{WithProgressNotify()})
	if !opts.ProgressNotify {
		t.Error("ProgressNotify = false, want true")
	}
}

func TestApplyWatchOptionsWithFilters(t *testing.T) {
	opts := ApplyWatchOptions([]WatchOption{WithFilters(WatchFilterNoPut, WatchFilterNoDelete)})
	if len(opts.Filters) != 2 {
		t.Fatalf("Filters length = %d, want 2", len(opts.Filters))
	}
	if opts.Filters[0] != WatchFilterNoPut {
		t.Errorf("Filters[0] = %d, want WatchFilterNoPut", opts.Filters[0])
	}
	if opts.Filters[1] != WatchFilterNoDelete {
		t.Errorf("Filters[1] = %d, want WatchFilterNoDelete", opts.Filters[1])
	}
}

func TestApplyWatchOptionsWithMultipleOptions(t *testing.T) {
	opts := ApplyWatchOptions([]WatchOption{
		WithStartRevision(42),
		WithPrevKV(),
		WithProgressNotify(),
		WithFilters(WatchFilterNoPut),
	})
	if opts.StartRevision != 42 {
		t.Errorf("StartRevision = %d, want 42", opts.StartRevision)
	}
	if !opts.PrevKV {
		t.Error("PrevKV = false, want true")
	}
	if !opts.ProgressNotify {
		t.Error("ProgressNotify = false, want true")
	}
	if len(opts.Filters) != 1 || opts.Filters[0] != WatchFilterNoPut {
		t.Errorf("Filters = %v, want [WatchFilterNoPut]", opts.Filters)
	}
}

func TestWatchFilterConstants(t *testing.T) {
	// Verify constants are distinct and non-zero.
	if WatchFilterNoPut == 0 {
		t.Error("WatchFilterNoPut should not be zero")
	}
	if WatchFilterNoDelete == 0 {
		t.Error("WatchFilterNoDelete should not be zero")
	}
	if WatchFilterNoPut == WatchFilterNoDelete {
		t.Error("WatchFilterNoPut and WatchFilterNoDelete should be different")
	}
}

func TestEventTypeConstants(t *testing.T) {
	// Verify EventPut and EventDelete are distinct.
	if EventPut == EventDelete {
		t.Error("EventPut and EventDelete should be different")
	}
}
