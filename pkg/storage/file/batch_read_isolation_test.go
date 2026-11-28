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

package file

import (
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

// TestGet_NeverObservesAHalfAppliedBatch is the MVCC read shape: each batch
// writes a data record and the revision pointer naming it, and a reader that
// sees the pointer must find the record (go-qrdb#1765: "mvcc: revision N not
// found" under concurrent user writes on the file backend).
func TestGet_NeverObservesAHalfAppliedBatch(t *testing.T) {
	e := newCASEngine(t)
	rev := []byte("rev")
	const commits = 200
	const readers = 4

	var stop atomic.Bool
	var torn atomic.Int64
	var readErr atomic.Value
	var wg sync.WaitGroup
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				ptr, err := e.Get(rev)
				if err != nil {
					readErr.Store(err)
					return
				}
				if ptr == nil {
					continue
				}
				data, err := e.Get([]byte("data/" + string(ptr)))
				if err != nil {
					readErr.Store(err)
					return
				}
				if data == nil {
					torn.Add(1)
				}
			}
		}()
	}

	for n := 1; n <= commits; n++ {
		s := strconv.Itoa(n)
		b := e.Batch()
		if err := b.Set([]byte("data/"+s), []byte("v"+s)); err != nil {
			t.Fatal(err)
		}
		if err := b.Set(rev, []byte(s)); err != nil {
			t.Fatal(err)
		}
		if err := b.Commit(); err != nil {
			t.Fatalf("commit %d: %v", n, err)
		}
	}
	stop.Store(true)
	wg.Wait()

	if err, _ := readErr.Load().(error); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if n := torn.Load(); n > 0 {
		t.Fatalf("%d reads saw a revision pointer whose data record was not yet written", n)
	}
}
