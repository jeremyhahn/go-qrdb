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

package server

import (
	"context"
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/barrierinit"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// barrierInitKV adapts the system store's KVServicer -- the replicated system
// shard, or a standalone node's own store -- to barrierinit.KV. Reads are the
// cluster seal adapter's; the conditional write is the existing guarded write
// (protocol.OpGuarded), so the init record adds no Raft command.
type barrierInitKV struct {
	clusterSealKV
}

var _ barrierinit.KV = barrierInitKV{}

// NewBarrierInitKV adapts kv, the system store, for a barrierinit.Book and
// barrierinit.Watcher.
func NewBarrierInitKV(kv service.KVServicer) barrierinit.KV {
	return barrierInitKV{clusterSealKV{kv: kv}}
}

// guardEnforcer is a store that reports whether the writes it proposes carry
// their context's WriteGuard (service.KVService.GuardsEnforced).
type guardEnforcer interface {
	GuardsEnforced() bool
}

// PutIf implements barrierinit.KV: value is stored under a guard that key's
// value equal expected when the state machine applies it, an absent key
// comparing equal to the empty value. A host that would propose the write
// unguarded -- a cluster that has not enabled guarded writes -- is refused
// with *barrierinit.GuardedWritesUnavailableError rather than let a claim
// overwrite a rival's.
func (a barrierInitKV) PutIf(ctx context.Context, key string, expected, value []byte) (bool, error) {
	if g, ok := a.kv.(guardEnforcer); ok && !g.GuardsEnforced() {
		return false, &barrierinit.GuardedWritesUnavailableError{}
	}
	guard, err := service.NewWriteGuard(service.Compare{
		Key:    []byte(key),
		Target: service.CompareValue,
		Result: service.CompareEqual,
		Value:  expected,
	})
	if err != nil {
		return false, err
	}
	err = a.kv.Put(service.WithWriteGuard(ctx, guard), key, value)
	var refused *service.WriteGuardRefusedError
	switch {
	case errors.As(err, &refused):
		return false, nil
	case err != nil:
		return false, err
	}
	return true, nil
}
