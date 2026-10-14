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

package backend_test

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
)

// benchPutKeys hands out distinct keys so the benchmark measures the commit
// path rather than one key's history.
var benchPutKeys atomic.Uint64

// BenchmarkLocalBackend_ParallelPut is the standalone write path end to end --
// LocalBackend over the MVCC engine over the file engine, on a REAL disk --
// with many concurrent proposers, the shape the lease sweep, keepalives and
// client writes have on a busy node. One op is one acknowledged Put.
func BenchmarkLocalBackend_ParallelPut(b *testing.B) {
	stg, err := file.New(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = stg.Close() })
	eng, err := engine.NewMVCCEngine(stg)
	if err != nil {
		b.Fatal(err)
	}
	host := backend.NewLocalBackend(eng)
	ctx := context.Background()
	value := make([]byte, 128)

	b.SetParallelism(4)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			key := []byte("bench/" + strconv.FormatUint(benchPutKeys.Add(1), 10))
			cmd, err := protocol.EncodeMVCCPut(key, value, 0)
			if err != nil {
				b.Error(err)
				return
			}
			if _, err := host.SyncPropose(ctx, 0, cmd); err != nil {
				b.Error(err)
				return
			}
		}
	})
}
