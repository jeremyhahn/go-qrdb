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

package service

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

func perEngineStores(t *testing.T) map[string]storage.Storage {
	t.Helper()
	pdb, err := pebble.New(&pebble.Config{DataDir: t.TempDir()})
	require.NoError(t, err)
	fdb, err := file.New(t.TempDir())
	require.NoError(t, err)
	odb, err := objstore.New("local", map[string]string{"path": t.TempDir()})
	require.NoError(t, err)
	stores := map[string]storage.Storage{
		"memory":   memory.NewStorage(),
		"pebble":   pdb,
		"file":     fdb,
		"objstore": odb,
	}
	t.Cleanup(func() {
		for _, s := range stores {
			_ = s.Close()
		}
	})
	return stores
}

func TestPerDatabaseKV_EveryEngine_CompareAndSwapOneWinner(t *testing.T) {
	ctx := context.Background()
	for name, store := range perEngineStores(t) {
		t.Run(name, func(t *testing.T) {
			kv := NewNamedBackendKVService("mydb", database.NewStandaloneBackend(store))
			require.NoError(t, kv.Put(ctx, "k", []byte("v0")))

			var swapped atomic.Int64
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := 0; i < 32; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					ok, err := kv.CompareAndSwap(ctx, "k", []byte("v0"), []byte("next-"+strconv.Itoa(i)))
					if err != nil {
						t.Errorf("CompareAndSwap: %v", err)
						return
					}
					if ok {
						swapped.Add(1)
					}
				}(i)
			}
			close(start)
			wg.Wait()
			require.Equal(t, int64(1), swapped.Load())
		})
	}
}

func TestPerDatabaseKV_EveryEngine_LosingSwapIsFalseNotError(t *testing.T) {
	ctx := context.Background()
	for name, store := range perEngineStores(t) {
		t.Run(name, func(t *testing.T) {
			kv := NewNamedBackendKVService("mydb", database.NewStandaloneBackend(store))
			ok, err := kv.CompareAndSwap(ctx, "absent", []byte("a"), []byte("b"))
			require.NoError(t, err)
			require.False(t, ok)
			_, err = kv.Get(ctx, "absent")
			var notFound *KeyNotFoundError
			require.ErrorAs(t, err, &notFound, "a swap must not create an absent key")

			require.NoError(t, kv.Put(ctx, "k", []byte("a")))
			ok, err = kv.CompareAndSwap(ctx, "k", []byte("x"), []byte("b"))
			require.NoError(t, err)
			require.False(t, ok)
			got, err := kv.Get(ctx, "k")
			require.NoError(t, err)
			require.Equal(t, "a", string(got))
		})
	}
}
