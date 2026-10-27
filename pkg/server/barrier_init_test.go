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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/barrierinit"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// barrierInitGatedHost is a host whose groups can hold other binaries: it
// answers FeatureEnabled as the raft adapter does from its feature gate.
type barrierInitGatedHost struct {
	service.Host
	guarded bool
}

var _ service.Host = (*barrierInitGatedHost)(nil)

func (h barrierInitGatedHost) FeatureEnabled(_ uint64, f featuregate.Feature) bool {
	return f == featuregate.GuardedWrites && h.guarded
}

var _ service.FeatureGatedHost = barrierInitGatedHost{}

// putFailingKV is a store whose writes fail with err.
type putFailingKV struct {
	service.KVServicer
	err error
}

func (p putFailingKV) Put(context.Context, string, []byte) error { return p.err }

// newBarrierInitStore returns a real standalone system store: a KVService over
// a LocalBackend over a KVEngine, wrapped by wrap when it is not nil.
func newBarrierInitStore(t *testing.T, wrap func(service.Host) service.Host) service.KVServicer {
	t.Helper()
	local := backend.NewLocalBackend(engine.NewKVEngine(memory.NewStorage()))
	t.Cleanup(func() { _ = local.Close() })
	var host service.Host = local
	if wrap != nil {
		host = wrap(host)
	}
	return service.NewKVService(host, shard.ShardKV)
}

// TestBarrierInitKV_PutIfIsEvaluatedByTheStateMachine: the claim is a
// create-if-absent and the advance a compare-and-set, both decided where the
// write is applied, so a second claim and a stale advance store nothing.
func TestBarrierInitKV_PutIfIsEvaluatedByTheStateMachine(t *testing.T) {
	ctx := context.Background()
	kv := NewBarrierInitKV(newBarrierInitStore(t, nil))
	key := barrierinit.Prefix + "tenant/acme"

	stored, err := kv.PutIf(ctx, key, nil, []byte("v1"))
	if err != nil || !stored {
		t.Fatalf("claim of an absent key: stored=%v err=%v, want stored", stored, err)
	}
	if stored, err = kv.PutIf(ctx, key, nil, []byte("rival")); err != nil || stored {
		t.Fatalf("second claim: stored=%v err=%v, want refused without error", stored, err)
	}
	if stored, err = kv.PutIf(ctx, key, []byte("v1"), []byte("v2")); err != nil || !stored {
		t.Fatalf("advance over the read value: stored=%v err=%v, want stored", stored, err)
	}
	if stored, err = kv.PutIf(ctx, key, []byte("v1"), []byte("v3")); err != nil || stored {
		t.Fatalf("stale advance: stored=%v err=%v, want refused without error", stored, err)
	}

	got, found, err := kv.Get(ctx, key)
	if err != nil || !found || string(got) != "v2" {
		t.Fatalf("Get = %q, %v, %v; want v2", got, found, err)
	}
	all, err := kv.Scan(ctx, barrierinit.Prefix)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(all) != 1 || string(all[key]) != "v2" {
		t.Fatalf("Scan = %q, want only %s = v2", all, key)
	}
}

// TestBarrierInitKV_RefusesAnUnguardedWrite: a cluster that has not enabled
// guarded writes would apply the write unconditionally, so a claim could
// overwrite a rival's; it is refused and nothing is stored.
func TestBarrierInitKV_RefusesAnUnguardedWrite(t *testing.T) {
	ctx := context.Background()
	key := barrierinit.Prefix + "tenant/acme"
	kv := NewBarrierInitKV(newBarrierInitStore(t, func(h service.Host) service.Host {
		return barrierInitGatedHost{Host: h}
	}))

	stored, err := kv.PutIf(ctx, key, nil, []byte("v1"))
	var unavailable *barrierinit.GuardedWritesUnavailableError
	if stored || !errors.As(err, &unavailable) {
		t.Fatalf("PutIf = %v, %v; want *GuardedWritesUnavailableError", stored, err)
	}
	if _, found, _ := kv.Get(ctx, key); found {
		t.Fatal("a refused claim stored its record")
	}
}

// TestBarrierInitKV_GuardedHostAllowsTheClaim: once guarded writes are
// enabled the same host claims and refuses a rival.
func TestBarrierInitKV_GuardedHostAllowsTheClaim(t *testing.T) {
	ctx := context.Background()
	key := barrierinit.Prefix + "tenant/acme"
	kv := NewBarrierInitKV(newBarrierInitStore(t, func(h service.Host) service.Host {
		return barrierInitGatedHost{Host: h, guarded: true}
	}))

	if stored, err := kv.PutIf(ctx, key, nil, []byte("v1")); err != nil || !stored {
		t.Fatalf("claim: stored=%v err=%v, want stored", stored, err)
	}
	if stored, err := kv.PutIf(ctx, key, nil, []byte("rival")); err != nil || stored {
		t.Fatalf("rival claim: stored=%v err=%v, want refused", stored, err)
	}
}

// TestBarrierInitKV_StoreFailureIsReturned: a write that fails for any reason
// other than its guard is an error, never a refusal a Book would read as a
// rival's win.
func TestBarrierInitKV_StoreFailureIsReturned(t *testing.T) {
	cause := errors.New("no quorum")
	kv := NewBarrierInitKV(putFailingKV{KVServicer: newBarrierInitStore(t, nil), err: cause})

	stored, err := kv.PutIf(context.Background(), barrierinit.Prefix+"tenant/acme", nil, []byte("v1"))
	if stored || !errors.Is(err, cause) {
		t.Fatalf("PutIf = %v, %v; want %v", stored, err, cause)
	}
}
