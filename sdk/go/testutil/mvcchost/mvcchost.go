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

// Package mvcchost is a service.Host backed by a real MVCC state machine, so
// a test can drive the repository's own services, and every transport over
// them, without a Raft cluster.
//
// A fake cannot refuse what it was never taught to refuse, nor agree with the
// engine on a number it never computed: a test that asserts a property of the
// real plane (a batch's commit revision is the ModRevision a later Get sees,
// a transaction refuses a read in a branch) needs the plane itself.
package mvcchost

import (
	"context"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/store"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ShardID is the shard every service built over a Host addresses.
const ShardID = 1

// Host is a service.Host backed by a real MVCC state machine.
//
// service.Host is embedded rather than implemented in full so that any method
// a test does not exercise panics with a nil-pointer dereference instead of
// quietly returning a plausible zero value. SyncPropose and SyncRead are the
// methods the KV, MVCC, transaction and batch services call.
type Host struct {
	service.Host

	machine *store.MVCCStateMachine

	mu    sync.Mutex
	index uint64
}

// dir is the directory the machine opens on its in-memory filesystem. Each
// Host has a filesystem of its own, so every Host can use the same name.
const dir = "/mvcchost"

// New opens a real MVCC state machine on an in-memory Pebble filesystem of
// its own and returns a Host over it: nothing is written to the host's disk.
// The machine is closed when the test ends.
func New(t testing.TB) *Host {
	t.Helper()
	return open(t, vfs.NewMem())
}

// open opens the machine on fs, the seam a test uses to make the open fail.
func open(t testing.TB, fs vfs.FS) *Host {
	t.Helper()
	machine := store.NewMVCCStateMachine(&pebble.Options{FS: fs})
	ctx := context.Background()
	if _, err := machine.Open(ctx, dir, make(chan struct{})); err != nil {
		t.Fatalf("mvcchost: open MVCC state machine: %v", err)
	}
	t.Cleanup(func() { _ = machine.Close(ctx) })
	return &Host{machine: machine}
}

// SyncPropose applies the command through the state machine, standing in for
// the Raft round trip. Serialised, because a replicated log is: this is what
// makes concurrent writes resolve against a single ordered history rather
// than racing inside the state machine.
func (h *Host) SyncPropose(ctx context.Context, _ uint64, cmd []byte) (sm.Result, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.index++
	entries := []sm.Entry{{Index: h.index, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := h.machine.Update(ctx, entries, results); err != nil {
		return sm.Result{}, err
	}
	return results[0], nil
}

// SyncRead answers the query from the state machine.
func (h *Host) SyncRead(ctx context.Context, _ uint64, query interface{}) (interface{}, error) {
	return h.machine.Lookup(ctx, query)
}

// KV returns the real KV service over h.
func (h *Host) KV() service.KVServicer { return service.NewKVService(h, ShardID) }

// Admin returns the real admin service over h.
func (h *Host) Admin() service.AdminServicer { return service.NewAdminService(h) }

// MVCC returns the real MVCC service over h.
func (h *Host) MVCC() service.MVCCKVServicer { return service.NewMVCCKVService(h, ShardID) }

// Transaction returns the real transaction service over h.
func (h *Host) Transaction() service.TransactionServicer {
	return service.NewTransactionService(h, ShardID)
}

// Batch returns the real batch service over h.
func (h *Host) Batch() service.BatchServicer { return service.NewBatchService(h, ShardID) }

// BatchRevisionClient is the client surface CheckBatchRevision drives; every
// transport's client satisfies it.
type BatchRevisionClient interface {
	ExecuteBatch(ctx context.Context, ops []transport.Op, opts ...transport.WriteOption) (*transport.BatchResponse, error)
	GetRevision(ctx context.Context, key string, opts ...transport.ReadOption) (*transport.MVCCKeyValue, error)
}

// CheckBatchRevision commits a batch of one put and one delete through c and
// asserts the response carries the batch's commit revision: positive, and the
// ModRevision a later Get of the put key reports. Keys are taken under prefix
// so a test can call it more than once against one server.
func CheckBatchRevision(t testing.TB, c BatchRevisionClient, prefix string) int64 {
	t.Helper()
	ctx := context.Background()
	putKey := prefix + "put"

	resp, err := c.ExecuteBatch(ctx, []transport.Op{
		{Type: transport.OpPut, Key: []byte(putKey), Value: []byte("v")},
		{Type: transport.OpDelete, Key: []byte(prefix + "del")},
	})
	if err != nil {
		t.Fatalf("ExecuteBatch: %v", err)
	}
	if resp.Revision <= 0 {
		t.Fatalf("BatchResponse.Revision = %d, want > 0 for a batch with a put", resp.Revision)
	}

	kv, err := c.GetRevision(ctx, putKey)
	if err != nil {
		t.Fatalf("GetRevision(%q): %v", putKey, err)
	}
	if kv.ModRevision != resp.Revision {
		t.Fatalf("ModRevision of %q = %d, want the batch revision %d", putKey, kv.ModRevision, resp.Revision)
	}
	return resp.Revision
}
