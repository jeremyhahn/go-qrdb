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

package clusterseal

import (
	"bytes"
	"context"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
)

// memKV is a faithful in-memory KV: PutIfAbsent refuses a present key,
// CompareAndSwap compares exact bytes and never creates, and Scan returns a
// copy. Failures and races are injected through hooks.
type memKV struct {
	mu   sync.Mutex
	data map[string][]byte

	readErr  error
	writeErr error
	// beforeWrite runs before a guarded write takes effect, outside the lock,
	// so a test can land a competing write in the window a real cluster has.
	beforeWrite func()
	// onScan runs at the start of every Scan, outside the lock.
	onScan func()
	scans  int
	// blockPut makes Put wait for its context to end, as a write does on a
	// node that has lost its quorum.
	blockPut bool
	// staleErr fails ScanStale only: the local copy stays readable when the
	// quorum reads (readErr) fail, as it does on a real node.
	staleErr   error
	staleScans int

	// getFaults fail the next Gets, one error each in order, and later Gets
	// are served: a shard electing answers a read or two with a timeout and
	// then serves again. gets counts every Get.
	getFaults []error
	gets      int
	// writeFaults fail the next guarded writes the same way. With
	// faultCommits the write takes effect before its fault is returned, as a
	// proposal that commits after its caller timed out does. writes counts
	// every guarded write.
	writeFaults  []error
	faultCommits bool
	writes       int
	// putFaults fail the next Puts without applying them. puts counts every
	// Put.
	putFaults []error
	puts      int
}

// takeFault pops the first of *faults, or returns nil when none is queued.
// The caller holds m.mu.
func takeFault(faults *[]error) error {
	if len(*faults) == 0 {
		return nil
	}
	err := (*faults)[0]
	*faults = (*faults)[1:]
	return err
}

var _ KV = (*memKV)(nil)

func newMemKV() *memKV { return &memKV{data: map[string][]byte{}} }

func (m *memKV) Get(_ context.Context, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gets++
	if m.readErr != nil {
		return nil, false, m.readErr
	}
	if err := takeFault(&m.getFaults); err != nil {
		return nil, false, err
	}
	v, ok := m.data[key]
	return bytes.Clone(v), ok, nil
}

func (m *memKV) hook() {
	m.mu.Lock()
	f := m.beforeWrite
	m.beforeWrite = nil
	m.mu.Unlock()
	if f != nil {
		f()
	}
}

func (m *memKV) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	m.hook()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes++
	if m.writeErr != nil {
		return false, m.writeErr
	}
	fault := takeFault(&m.writeFaults)
	if fault != nil && !m.faultCommits {
		return false, fault
	}
	if _, ok := m.data[key]; ok {
		return false, fault
	}
	m.data[key] = bytes.Clone(value)
	return fault == nil, fault
}

func (m *memKV) CompareAndSwap(_ context.Context, key string, expected, next []byte) (bool, error) {
	m.hook()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes++
	if m.writeErr != nil {
		return false, m.writeErr
	}
	fault := takeFault(&m.writeFaults)
	if fault != nil && !m.faultCommits {
		return false, fault
	}
	cur, ok := m.data[key]
	if !ok || !bytes.Equal(cur, expected) {
		return false, fault
	}
	m.data[key] = bytes.Clone(next)
	return fault == nil, fault
}

func (m *memKV) Put(ctx context.Context, key string, value []byte) error {
	m.mu.Lock()
	block := m.blockPut
	m.mu.Unlock()
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.puts++
	if m.writeErr != nil {
		return m.writeErr
	}
	if err := takeFault(&m.putFaults); err != nil {
		return err
	}
	m.data[key] = bytes.Clone(value)
	return nil
}

func (m *memKV) Scan(_ context.Context, prefix string) (map[string][]byte, error) {
	m.mu.Lock()
	f := m.onScan
	m.mu.Unlock()
	if f != nil {
		f()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scans++
	if m.readErr != nil {
		return nil, m.readErr
	}
	return m.matchLocked(prefix), nil
}

func (m *memKV) ScanStale(_ context.Context, prefix string) (map[string][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.staleScans++
	if m.staleErr != nil {
		return nil, m.staleErr
	}
	return m.matchLocked(prefix), nil
}

func (m *memKV) matchLocked(prefix string) map[string][]byte {
	out := map[string][]byte{}
	for k, v := range m.data {
		if strings.HasPrefix(k, prefix) {
			out[k] = bytes.Clone(v)
		}
	}
	return out
}

func (m *memKV) set(key string, value []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = bytes.Clone(value)
}

func (m *memKV) get(key string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	return v, ok
}

func (m *memKV) snapshot() map[string][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return maps.Clone(m.data)
}

// fakeEnforcer records every enforcement and holds the per-barrier unsealed
// state a real composition reads from its barriers.
type fakeEnforcer struct {
	mu        sync.Mutex
	node      bool // unsealed
	tenants   map[string]bool
	databases map[string]bool
	calls     []string
	err       error
	// onSealNode runs inside SealNode, so a test can observe what was true at
	// the moment the key was wiped.
	onSealNode func()
	// onSealTenant runs inside SealTenant, before the key is wiped.
	onSealTenant func()
}

var _ Enforcer = (*fakeEnforcer)(nil)

func newFakeEnforcer() *fakeEnforcer {
	return &fakeEnforcer{node: true, tenants: map[string]bool{}, databases: map[string]bool{}}
}

func (f *fakeEnforcer) SealNode(context.Context) error {
	f.mu.Lock()
	hook := f.onSealNode
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "node")
	if f.err != nil {
		return f.err
	}
	f.node = false
	for k := range f.tenants {
		f.tenants[k] = false
	}
	for k := range f.databases {
		f.databases[k] = false
	}
	return nil
}

func (f *fakeEnforcer) SealTenant(_ context.Context, tenantID string) error {
	f.mu.Lock()
	hook := f.onSealTenant
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "tenant/"+tenantID)
	if f.err != nil {
		return f.err
	}
	f.tenants[tenantID] = false
	return nil
}

func (f *fakeEnforcer) SealDatabase(_ context.Context, tenantID, database string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "database/"+tenantID+"/"+database)
	if f.err != nil {
		return f.err
	}
	f.databases[tenantID+"/"+database] = false
	return nil
}

func (f *fakeEnforcer) NodeUnsealed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.node
}

func (f *fakeEnforcer) TenantUnsealed(tenantID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tenants[tenantID]
}

func (f *fakeEnforcer) DatabaseUnsealed(tenantID, database string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.databases[tenantID+"/"+database]
}

func (f *fakeEnforcer) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeEnforcer) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// fakeGate reports a fixed activation for the cluster-seal feature.
type fakeGate struct {
	mu        sync.Mutex
	activated bool
	// unloaded reports that the gate has not read the activations yet.
	unloaded bool
}

func (g *fakeGate) Activated(f feature.Feature) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return !g.unloaded && g.activated && f == featuregate.ClusterSeal
}

func (g *fakeGate) ActivationsLoaded() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return !g.unloaded
}

func (g *fakeGate) setLoaded(v bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.unloaded = !v
}

func (g *fakeGate) set(v bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.activated = v
}

// fakeClock is a manual clock whose After advances time by the requested
// duration and fires at once, so a wait loop runs to its deadline instantly
// and deterministically.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.Advance(d)
	ch := make(chan time.Time, 1)
	ch <- c.Now()
	return ch
}
