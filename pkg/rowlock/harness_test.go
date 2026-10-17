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

package rowlock

import (
	"context"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/clock"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// stack is a real, in-memory store: the MVCC engine with its lessor and
// watch hub, behind the same local backend a standalone node uses, and the
// service-layer servicers over it. Nothing in it restates the grant rule or
// lease cascade; the tests exercise the code paths production runs.
type stack struct {
	lessor *lease.Lessor
	deps   Deps
}

func newStack(t *testing.T, opts ...lease.Option) *stack {
	t.Helper()
	hub := watch.NewHub()
	lessor := lease.NewLessor(opts...)
	eng, err := engine.NewMVCCEngine(memory.NewStorage(),
		engine.WithMVCCLessor(lessor), engine.WithMVCCWatchHub(hub))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	host := backend.NewLocalBackend(eng)
	t.Cleanup(func() { _ = host.Close() })
	return &stack{
		lessor: lessor,
		deps: Deps{
			Txn:   service.NewTransactionService(host, 1),
			MVCC:  service.NewMVCCKVService(host, 1),
			Lease: service.NewLeaseService(host, 1),
			Watch: service.NewWatchService(hub),
		},
	}
}

// table returns a lock table over the stack.
func (s *stack) table(t *testing.T, opts ...Option) *Table {
	t.Helper()
	tbl, err := New(s.deps, "acme/shop", opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tbl
}

// runExpiry starts the leader's lease-expiry sweep, as every clustered
// composition does, until the test ends.
func (s *stack) runExpiry(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.lessor.RunExpiry(ctx, 20*time.Millisecond, func() bool { return true },
			s.deps.Lease.Revoke, nil)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

// silentLease is a lease servicer whose KeepAlive is never delivered: the
// lease of an owner whose process has died.
type silentLease struct {
	service.LeaseServicer
}

func (silentLease) KeepAlive(ctx context.Context, _ int64) (*lease.Lease, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// renewalLease is a lease servicer that reports each KeepAlive the lessor
// granted by the lease-clock time the renewal began, so the renewal set the
// lease's deadline to at least that time plus its TTL. A report is dropped
// while an earlier one is unread.
type renewalLease struct {
	service.LeaseServicer
	clk     clock.Clock
	renewed chan time.Time
}

func (r renewalLease) KeepAlive(ctx context.Context, id int64) (*lease.Lease, error) {
	began := r.clk.Now()
	l, err := r.LeaseServicer.KeepAlive(ctx, id)
	if err == nil {
		select {
		case r.renewed <- began:
		default:
		}
	}
	return l, err
}

// queueOwners returns the owners queued on key, in FIFO order.
func queueOwners(t *testing.T, tbl *Table, key string) []string {
	t.Helper()
	q, err := tbl.queue(context.Background(), key)
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	out := make([]string, 0, len(q))
	for _, r := range q {
		out = append(out, r.owner+"/"+string(modeChars[r.mode]))
	}
	return out
}

// acquireAsync runs Acquire on its own goroutine and reports its result.
func acquireAsync(o *Owner, tbl *Table, keys []string, mode Mode, wait time.Duration) <-chan error {
	out := make(chan error, 1)
	go func() { out <- o.Acquire(context.Background(), tbl, keys, mode, wait) }()
	return out
}

// waitQueued blocks until key's queue holds n records, so a test knows an
// asynchronous request has enqueued before it acts.
func waitQueued(t *testing.T, tbl *Table, key string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(queueOwners(t, tbl, key)) == n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("queue on %q never reached %d records: %v", key, n, queueOwners(t, tbl, key))
}

// mustNotReturn fails if ch delivers within d.
func mustNotReturn(t *testing.T, ch <-chan error, d time.Duration, what string) {
	t.Helper()
	select {
	case err := <-ch:
		t.Fatalf("%s returned early: %v", what, err)
	case <-time.After(d):
	}
}

// mustReturn waits up to 5s for ch and returns what it delivered.
func mustReturn(t *testing.T, ch <-chan error, what string) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("%s never returned", what)
		return nil
	}
}

// waitWaiting blocks until want requests are blocked inside Owner.wait.
//
