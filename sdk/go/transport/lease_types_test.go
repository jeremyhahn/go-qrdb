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

package transport

import "testing"

// TestFirstLeaseID_FindsTheFirstNonZero proves FirstLeaseID scans in order
// and reports the first op naming a lease, skipping ones that do not --
// the shared helper every remote transport's own mapOpsNotFoundError now
// calls instead of duplicating this loop (go-qrdb#591's review,
// non-blocking).
func TestFirstLeaseID_FindsTheFirstNonZero(t *testing.T) {
	ops := []Op{
		{Type: OpPut, Key: []byte("a")},
		{Type: OpPut, Key: []byte("b"), LeaseID: 42},
		{Type: OpPut, Key: []byte("c"), LeaseID: 7},
	}
	id, found := FirstLeaseID(ops)
	if !found {
		t.Fatal("expected a lease ID to be found")
	}
	if id != 42 {
		t.Errorf("id = %d, want 42 (the first op naming one)", id)
	}
}

// TestFirstLeaseID_NoneFound proves the floor: no op names a lease.
func TestFirstLeaseID_NoneFound(t *testing.T) {
	ops := []Op{
		{Type: OpPut, Key: []byte("a")},
		{Type: OpGet, Key: []byte("b")},
	}
	if _, found := FirstLeaseID(ops); found {
		t.Fatal("expected no lease ID to be found")
	}
}

// TestFirstLeaseID_EmptyOps proves an empty or nil set is the same as none
// found, never a panic.
func TestFirstLeaseID_EmptyOps(t *testing.T) {
	if _, found := FirstLeaseID(nil); found {
		t.Fatal("expected no lease ID for a nil ops slice")
	}
	if _, found := FirstLeaseID([]Op{}); found {
		t.Fatal("expected no lease ID for an empty ops slice")
	}
}
