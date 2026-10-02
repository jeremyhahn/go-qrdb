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
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// schedMVCC is a database's MVCC view as the scheduler sees it: a current
// revision, and a Compact that records what it was asked for and the tenant
// the request carried.
type schedMVCC struct {
	service.MVCCKVServicer
	mu         sync.Mutex
	current    int64
	compacted  []int64
	tenants    []string
	compactErr error
}

func (m *schedMVCC) CurrentRevision(context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current, nil
}

func (m *schedMVCC) Compact(ctx context.Context, revision int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.compacted = append(m.compacted, revision)
	m.tenants = append(m.tenants, tenant.FromContext(ctx))
	return m.compactErr
}

func (m *schedMVCC) calls() []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]int64(nil), m.compacted...)
}

// schedRegistry resolves (tenant, database) to its MVCC view, and refuses
// every pair it does not hold -- as the real registry refuses another
// tenant's database.
type schedRegistry struct {
	service.Registry
	views map[string]*schedMVCC
}

func (r *schedRegistry) MVCCService(tenantID, db string) (service.MVCCKVServicer, error) {
	if v, ok := r.views[tenantID+"/"+db]; ok {
		return v, nil
	}
	return nil, &database.NotFoundError{Name: db}
}

func newSched(reg service.Registry, targets []compactionTarget, now func() time.Time) *compactionScheduler {
	return &compactionScheduler{
		registry: reg,
		targets:  func() []compactionTarget { return targets },
		interval: time.Hour,
		now:      now,
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		samples:  make(map[string][]revisionSample),
	}
}

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// No policy, an off policy, or an invalid one compacts nothing: nothing is
// compacted implicitly.
func TestCompactionScheduler_NoPolicyCompactsNothing(t *testing.T) {
	view := &schedMVCC{current: 100}
	reg := &schedRegistry{views: map[string]*schedMVCC{"acme/app": view}}
	s := newSched(reg, []compactionTarget{
		{Tenant: "acme", Database: "app", Policy: nil},
		{Tenant: "acme", Database: "app", Policy: &compaction.Policy{Mode: compaction.Off}},
		{Tenant: "acme", Database: "app", Policy: &compaction.Policy{Mode: compaction.Revision}},
	}, fixedClock(time.Now()))

	for i := 0; i < 3; i++ {
		if n := s.passOnce(context.Background()); n != 0 {
			t.Fatalf("pass %d compacted %d database(s) with no policy", i, n)
		}
	}
	if calls := view.calls(); len(calls) != 0 {
		t.Fatalf("Compact was called %v without a configured policy", calls)
	}
}

// A revision policy keeps the last N revisions, as the owning tenant, and
// waits while there are fewer.
func TestCompactionScheduler_RevisionPolicyKeepsTheLastN(t *testing.T) {
	view := &schedMVCC{current: 5}
	reg := &schedRegistry{views: map[string]*schedMVCC{"acme/app": view}}
	s := newSched(reg, []compactionTarget{{Tenant: "acme", Database: "app",
		Policy: &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 10}}}, fixedClock(time.Now()))

	if n := s.passOnce(context.Background()); n != 0 {
		t.Fatalf("compacted with fewer revisions than the policy keeps")
	}
	view.mu.Lock()
	view.current = 25
	view.mu.Unlock()
	if n := s.passOnce(context.Background()); n != 1 {
		t.Fatalf("pass compacted %d, want 1", n)
	}
	if calls := view.calls(); len(calls) != 1 || calls[0] != 15 {
		t.Fatalf("Compact calls = %v, want [15]", calls)
	}
	if view.tenants[0] != "acme" {
		t.Fatalf("compaction ran as tenant %q, want the owning tenant", view.tenants[0])
	}
}

// A period policy compacts to the newest revision observed at least the
// period ago, and nothing until a full period has been observed.
func TestCompactionScheduler_PeriodPolicyKeepsTheLastPeriod(t *testing.T) {
	view := &schedMVCC{current: 10}
	reg := &schedRegistry{views: map[string]*schedMVCC{"acme/app": view}}
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	now := start
	s := newSched(reg, []compactionTarget{{Tenant: "acme", Database: "app",
		Policy: &compaction.Policy{Mode: compaction.Period, RetainPeriod: "1h"}}}, func() time.Time { return now })

	advance := func(d time.Duration, rev int64) int {
		now = now.Add(d)
		view.mu.Lock()
		view.current = rev
		view.mu.Unlock()
		return s.passOnce(context.Background())
	}

	if n := advance(0, 10); n != 0 {
		t.Fatal("compacted on the first observation")
	}
	if n := advance(30*time.Minute, 20); n != 0 {
		t.Fatal("compacted before a full period was observed")
	}
	if n := advance(30*time.Minute, 30); n != 1 {
		t.Fatalf("an hour on, compacted %d, want 1", n)
	}
	if calls := view.calls(); len(calls) != 1 || calls[0] != 10 {
		t.Fatalf("Compact calls = %v, want [10] (the revision observed an hour ago)", calls)
	}
	if n := advance(40*time.Minute, 40); n != 1 {
		t.Fatalf("compacted %d, want 1", n)
	}
	if calls := view.calls(); calls[len(calls)-1] != 20 {
		t.Fatalf("Compact calls = %v, want the last at 20", calls)
	}
}

// An already-compacted refusal is not a compaction, and any other failure
// or an unresolvable database is skipped without stopping the pass.
func TestCompactionScheduler_RefusalsAndUnresolvableDatabases(t *testing.T) {
	already := &schedMVCC{current: 50, compactErr: &service.RevisionCompactedError{Requested: 40, Compacted: 45}}
	broken := &schedMVCC{current: 50, compactErr: errors.New("boom")}
	fine := &schedMVCC{current: 50}
	reg := &schedRegistry{views: map[string]*schedMVCC{"a/one": already, "a/two": broken, "a/three": fine}}
	policy := &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 10}
	s := newSched(reg, []compactionTarget{
		{Tenant: "a", Database: "one", Policy: policy},
		{Tenant: "a", Database: "two", Policy: policy},
		{Tenant: "a", Database: "gone", Policy: policy},
		{Tenant: "a", Database: "three", Policy: policy},
	}, fixedClock(time.Now()))

	if n := s.passOnce(context.Background()); n != 1 {
		t.Fatalf("pass compacted %d, want 1 (only the database that succeeded)", n)
	}
	if len(fine.calls()) != 1 {
		t.Fatal("a failing database stopped the pass")
	}
}

// Samples of a database whose period policy is removed are forgotten.
func TestCompactionScheduler_ForgetsSamplesOfRemovedPolicies(t *testing.T) {
	view := &schedMVCC{current: 10}
	reg := &schedRegistry{views: map[string]*schedMVCC{"acme/app": view}}
	targets := []compactionTarget{{Tenant: "acme", Database: "app",
		Policy: &compaction.Policy{Mode: compaction.Period, RetainPeriod: "1h"}}}
	s := newSched(reg, nil, fixedClock(time.Now()))
	s.targets = func() []compactionTarget { return targets }
	s.passOnce(context.Background())
	if len(s.samples) != 1 {
		t.Fatalf("samples = %v", s.samples)
	}
	targets = nil
	s.passOnce(context.Background())
	if len(s.samples) != 0 {
		t.Fatalf("samples of a removed policy were kept: %v", s.samples)
	}
}

// run drives a pass per tick and returns when its context ends.
func TestCompactionScheduler_RunPassesPerTick(t *testing.T) {
	view := &schedMVCC{current: 20}
	reg := &schedRegistry{views: map[string]*schedMVCC{"acme/app": view}}
	s := newSched(reg, []compactionTarget{{Tenant: "acme", Database: "app",
		Policy: &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 5}}}, fixedClock(time.Now()))
	ticks := make(chan time.Time)
	s.ticks = ticks
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.run(ctx)
		close(done)
	}()
	ticks <- time.Now()
	ticks <- time.Now()
	cancel()
	<-done
	if calls := view.calls(); len(calls) < 1 {
		t.Fatalf("no pass ran on a tick: %v", calls)
	}
}

// compactionTargets reads policies from the node's catalog, attributing an
// unowned database to the system tenant, and lists only databases with an
// enabled policy.
func TestServer_CompactionTargetsReadTheCatalog(t *testing.T) {
	mgr := database.NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)), t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	for _, cfg := range []database.DatabaseConfig{
		{Name: "acme-db", TenantID: "acme", Engine: database.EngineKV, Backend: database.BackendMemory},
		{Name: "sys-db", Engine: database.EngineKV, Backend: database.BackendMemory},
		{Name: "plain", TenantID: "acme", Engine: database.EngineKV, Backend: database.BackendMemory},
	} {
		if _, err := mgr.Create(cfg); err != nil {
			t.Fatalf("Create(%s): %v", cfg.Name, err)
		}
	}
	revision := &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 3}
	if err := mgr.UpdateCompaction("acme-db", revision); err != nil {
		t.Fatal(err)
	}
	if err := mgr.UpdateCompaction("sys-db", revision); err != nil {
		t.Fatal(err)
	}

	s := &Server{opts: serverOptions{dbManager: mgr}}
	got := map[string]string{}
	for _, tgt := range s.compactionTargets() {
		got[tgt.Database] = tgt.Tenant
	}
	if len(got) != 2 || got["acme-db"] != "acme" || got["sys-db"] != tenant.SystemTenantID {
		t.Fatalf("targets = %v, want acme-db as acme and sys-db as the system tenant only", got)
	}
	if (&Server{}).compactionTargets() != nil {
		t.Fatal("a server with no catalog has targets")
	}
}

func TestWithCompactionInterval(t *testing.T) {
	var o serverOptions
	WithCompactionInterval(0)(&o)
	if o.compactionInterval != 0 {
		t.Fatal("a non-positive interval replaced the default")
	}
	WithCompactionInterval(time.Second)(&o)
	if o.compactionInterval != time.Second {
		t.Fatalf("interval = %v", o.compactionInterval)
	}
}
