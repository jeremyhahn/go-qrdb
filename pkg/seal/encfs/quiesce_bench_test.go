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

package encfs_test

import (
	"strconv"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/filesys"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/encfs"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/quiesce"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// gatedKEK is a sealable KEK whose barrier has a quiesce gate, as a
// StorageBarrier does, so every admitted write takes and releases a lease.
type gatedKEK struct {
	sealableKEK
	gate quiesce.Gate
}

func (k *gatedKEK) Quiesce() *quiesce.Gate { return &k.gate }

// benchFS builds the encfs file system each benchmark case writes through.
var benchFS = map[string]func() vfs.FS{
	"lease": func() vfs.FS {
		return pebblestore.WrapVFS(vfs.NewMem(), func(base filesys.FS) filesys.FS { return encfs.New(base, &gatedKEK{}) })
	},
	"noLease": func() vfs.FS {
		return pebblestore.WrapVFS(vfs.NewMem(), func(base filesys.FS) filesys.FS { return encfs.New(base, &sealableKEK{}) })
	},
}

// BenchmarkPebbleStorage_SetUnderEncfs measures the durable write path of a
// PebbleStorage over an encfs file system, the path the seal watch admits,
// with and without a quiesce lease on it.
func BenchmarkPebbleStorage_SetUnderEncfs(b *testing.B) {
	for _, name := range []string{"lease", "noLease"} {
		b.Run(name, func(b *testing.B) { benchSetUnderEncfs(b, benchFS[name]()) })
	}
}

func benchSetUnderEncfs(b *testing.B, fs vfs.FS) {
	cfg := pebblestore.TestConfig("db")
	cfg.FS = fs
	store, err := pebblestore.New(cfg)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	defer func() { _ = store.Close() }()
	keys := make([][]byte, 1024)
	for i := range keys {
		keys[i] = []byte("k" + strconv.Itoa(i))
	}
	value := []byte("value")
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if err := store.Set(keys[i%len(keys)], value); err != nil {
				b.Errorf("Set: %v", err)
				return
			}
			i++
		}
	})
}
