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

package file

import (
	"strconv"
	"sync/atomic"
	"testing"
)

// benchCommitKeys hands out distinct keys so every commit writes new records
// rather than overwriting one.
var benchCommitKeys atomic.Uint64

// BenchmarkBatchCommit measures one batch commit of n distinct keys on a REAL
// disk: the fsyncs a commit issues are the cost being measured, and tmpfs
// makes every one of them free. One op is one commit.
func BenchmarkBatchCommit(b *testing.B) {
	value := make([]byte, 128)
	for _, n := range []int{1, 8, 32} {
		b.Run("keys="+strconv.Itoa(n), func(b *testing.B) {
			e, err := New(b.TempDir())
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = e.Close() })
			b.ResetTimer()
			for b.Loop() {
				batch := e.Batch()
				for range n {
					key := []byte("bench/" + strconv.FormatUint(benchCommitKeys.Add(1), 10))
					if err := batch.Set(key, value); err != nil {
						b.Fatal(err)
					}
				}
				if err := batch.Commit(); err != nil {
					b.Fatal(err)
				}
				_ = batch.Close()
			}
		})
	}
}
