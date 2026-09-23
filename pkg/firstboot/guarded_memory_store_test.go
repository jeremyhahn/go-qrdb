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

package firstboot

import (
	"bytes"
	"context"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// guardedMemoryKVStore adds kvstore.MultiGuardedBatchKVStore to the real
// in-memory store, so the System promotion -- which writes the sentinel and
// the record in one guarded batch and refuses a store that cannot -- runs
// against the real user store in these tests. Batches serialise on batchMu
// against each other; the tests using it are single-goroutine, so no plain
// write can interleave with a batch.
type guardedMemoryKVStore struct {
	*kvstore.MemoryKVStore
	batchMu sync.Mutex
}

var _ kvstore.MultiGuardedBatchKVStore = (*guardedMemoryKVStore)(nil)

func newGuardedMemoryKVStore() *guardedMemoryKVStore {
	return &guardedMemoryKVStore{MemoryKVStore: kvstore.NewMemoryKVStore()}
}

// PutAllIfUnchanged implements kvstore.GuardedBatchKVStore through
// PutAllIfAllUnchanged, as the production adapters do.
func (s *guardedMemoryKVStore) PutAllIfUnchanged(ctx context.Context, guardKey string, guardValue []byte, writes []kvstore.KV) (bool, error) {
	return s.PutAllIfAllUnchanged(ctx, []kvstore.Guard{{Key: guardKey, Value: guardValue}}, writes)
}

// PutAllIfAllUnchanged applies writes only while every guard holds: a nil
// Guard.Value requires the key to be absent, any other value requires it to
// be present and equal.
func (s *guardedMemoryKVStore) PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
	s.batchMu.Lock()
	defer s.batchMu.Unlock()
	for _, g := range guards {
		exists, err := s.Exists(ctx, g.Key)
		if err != nil {
			return false, err
		}
		if g.Value == nil {
			if exists {
				return false, nil
			}
			continue
		}
		v, err := s.Get(ctx, g.Key)
		if err != nil {
			return false, err
		}
		if !exists || !bytes.Equal(v, g.Value) {
			return false, nil
		}
	}
	for _, w := range writes {
		if err := s.Put(ctx, w.Key, w.Value); err != nil {
			return false, err
		}
	}
	return true, nil
}
