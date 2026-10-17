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
	"io"
	"log/slog"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// schedStore is a store's raw shard service as the scheduler drives it, kept
// faithful to the real one where the scheduler's correctness depends on it:
//
//   - its compaction table applies records with the very compaction.State
//     methods the state machine applies them with (Bind, Record);
//   - a compaction refuses a revision above the current one and one at or
//     below the scope's floor, as the service does;
//   - a proposal carrying a drop-fence guard is refused unless the guard
//     names the database's live incarnation, as the state machine refuses it.
type schedStore struct {
	mu        sync.Mutex
	current   int64
	table     map[string]*compaction.State
	floors    map[string]int64
	live      map[string]string // "tenant/db" -> live incarnation
	compacted []string          // "scope@revision"
	leads     bool
}

var (
	_ compactionStore     = (*schedStore)(nil)
	_ service.ShardLeader = (*schedStore)(nil)
)

func newSchedStore(current int64) *schedStore {
	return &schedStore{
		current: current, leads: true,
		table: map[string]*compaction.State{}, floors: map[string]int64{}, live: map[string]string{},
	}
}

func (s *schedStore) LeadsShard() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leads
}

func (s *schedStore) setCurrent(rev int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = rev
}

// admit evaluates ctx's guard the way the state machine does.
func (s *schedStore) admit(ctx context.Context, st *compaction.State) error {
	guard := service.WriteGuardFrom(ctx)
	if guard.IsZero() {
		return nil
	}
	want, err := service.DropFenceGuard(st.Tenant, st.Database, s.live[st.Tenant+"/"+st.Database])
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(guard, want) {
		return &service.WriteGuardRefusedError{Key: service.DropFenceKey(st.Tenant, st.Database)}
	}
	return nil
}

func (s *schedStore) SetCompactionPolicy(_ context.Context, scope string, b compaction.Binding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.table[scope]
	if st == nil {
		st = &compaction.State{}
	}
	st.Bind(b)
	s.table[scope] = st
	return nil
}

func (s *schedStore) ClearCompactionState(_ context.Context, scope string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.table, scope)
	return nil
}

func (s *schedStore) RecordCompactionSample(ctx context.Context, scope string, at time.Time, revision int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.table[scope]
	if st == nil {
		return nil
	}
	if err := s.admit(ctx, st); err != nil {
		return err
	}
	st.Record(at.UnixNano(), revision)
	return nil
}

func (s *schedStore) CompactionState(_ context.Context, scope string) (*compaction.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return copyState(s.table[scope]), nil
}

func (s *schedStore) CompactionStates(context.Context) (map[string]*compaction.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]*compaction.State, len(s.table))
	for scope, st := range s.table {
		out[scope] = copyState(st)
	}
	return out, nil
}

func (s *schedStore) CurrentRevision(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current, nil
}

func (s *schedStore) CompactPrefix(ctx context.Context, prefix string, revision int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.table[prefix]; st != nil {
		if err := s.admit(ctx, st); err != nil {
			return err
		}
	}
	if revision > s.current {
		return &service.CompactRevisionFutureError{Requested: revision, Current: s.current}
	}
	if revision <= s.floors[prefix] {
		return &service.RevisionCompactedError{Requested: revision, Compacted: s.floors[prefix]}
	}
	s.floors[prefix] = revision
	s.compacted = append(s.compacted, prefix+"@"+strconv.FormatInt(revision, 10))
	return nil
}

func (s *schedStore) CompactNodeKeyspace(ctx context.Context, revision int64) error {
	return s.CompactPrefix(ctx, compaction.NodeScope, revision)
}

func (s *schedStore) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.compacted...)
}

func copyState(st *compaction.State) *compaction.State {
	if st == nil {
		return nil
	}
	cp := *st
	cp.Samples = append([]compaction.Sample(nil), st.Samples...)
	return &cp
}

// newTestScheduler runs over stores with a controllable clock.
func newTestScheduler(now func() time.Time, stores ...scheduledStore) *compactionScheduler {
	return &compactionScheduler{
		stores:   func(context.Context) []scheduledStore { return stores },
		interval: time.Hour,
		now:      now,
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// bind sets scope's policy for (tenant, db, incarnation) and marks that
// incarnation live.
func (s *schedStore) bind(scope, tenantID, db, incarnation string, p *compaction.Policy) {
	_ = s.SetCompactionPolicy(context.Background(), scope, compaction.Binding{Policy: p, Tenant: tenantID, Database: db, Incarnation: incarnation})
	s.mu.Lock()
	s.live[tenantID+"/"+db] = incarnation
	s.mu.Unlock()
}

// No record, an off record: nothing is compacted implicitly.
func TestCompactionScheduler_NoPolicyCompactsNothing(t *testing.T) {
	store := newSchedStore(100)
	s := newTestScheduler(time.Now, scheduledStore{name: "s", store: store, leader: store})
	if n := s.passOnce(context.Background()); n != 0 || len(store.calls()) != 0 {
		t.Fatalf("a store with no record compacted %d: %v", n, store.calls())
	}
}

// A revision policy compacts to current - N, once per new target; a repeat
// of the same target is the already-compacted refusal, not a compaction.
func TestCompactionScheduler_RevisionPolicyKeepsTheLastN(t *testing.T) {
	store := newSchedStore(100)
	store.bind("acme/app/", "acme", "app", "i1", &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 10})
	s := newTestScheduler(time.Now, scheduledStore{name: "s", store: store, leader: store})

	if n := s.passOnce(context.Background()); n != 1 {
		t.Fatalf("pass compacted %d, want 1", n)
	}
	if n := s.passOnce(context.Background()); n != 0 {
		t.Fatalf("a second pass at the same revision compacted %d", n)
	}
	store.setCurrent(150)
	s.passOnce(context.Background())
	if got := store.calls(); !reflect.DeepEqual(got, []string{"acme/app/@90", "acme/app/@140"}) {
		t.Fatalf("compactions = %v", got)
	}
}

// A period policy samples into the replicated record under the cap and
// compacts to the newest sample a period old.
func TestCompactionScheduler_PeriodPolicySamplesIntoTheRecord(t *testing.T) {
	store := newSchedStore(10)
	store.bind("acme/app/", "acme", "app", "i1", &compaction.Policy{Mode: compaction.Period, RetainPeriod: "1h"})
	now := time.Unix(1_700_000_000, 0)
	s := newTestScheduler(func() time.Time { return now }, scheduledStore{name: "s", store: store, leader: store})

	advance := func(d time.Duration, rev int64) int {
		now = now.Add(d)
		store.setCurrent(rev)
		return s.passOnce(context.Background())
	}
	if n := advance(0, 10); n != 0 {
		t.Fatal("compacted on the first observation")
	}
	if n := advance(30*time.Minute, 20); n != 0 {
		t.Fatal("compacted before a full period was observed")
	}
	if n := advance(31*time.Minute, 30); n != 1 {
		t.Fatalf("a period on, compacted %d, want 1", n)
	}
	if got := store.calls(); !reflect.DeepEqual(got, []string{"acme/app/@10"}) {
		t.Fatalf("compactions = %v, want the revision observed a period ago", got)
	}
	st, _ := store.CompactionState(context.Background(), "acme/app/")
	if len(st.Samples) == 0 {
		t.Fatal("the samples are not in the replicated record")
	}
}

// TestCompactionScheduler_OnlyTheLeaderRuns: a store this node does not lead
// is not run -- not sampled, not compacted -- so exactly one node runs each
// policy; when leadership moves here, the record's samples are continued.
func TestCompactionScheduler_OnlyTheLeaderRuns(t *testing.T) {
	store := newSchedStore(10)
	store.bind("acme/app/", "acme", "app", "i1", &compaction.Policy{Mode: compaction.Period, RetainPeriod: "1h"})
	// Another node led the group an hour ago and sampled revision 10.
	now := time.Unix(1_700_000_000, 0)
	_ = store.RecordCompactionSample(context.Background(), "acme/app/", now, 10)
	now = now.Add(61 * time.Minute)
	store.setCurrent(40)

	store.leads = false
	s := newTestScheduler(func() time.Time { return now }, scheduledStore{name: "s", store: store, leader: store})
	if n := s.passOnce(context.Background()); n != 0 || len(store.calls()) != 0 {
		t.Fatalf("a follower ran the policy: %d, %v", n, store.calls())
	}
	st, _ := store.CompactionState(context.Background(), "acme/app/")
	if len(st.Samples) != 1 {
		t.Fatalf("a follower sampled: %+v", st.Samples)
	}

	store.leads = true // leadership moved here
	if n := s.passOnce(context.Background()); n != 1 {
		t.Fatalf("the new leader compacted %d, want 1", n)
	}
	if got := store.calls(); !reflect.DeepEqual(got, []string{"acme/app/@10"}) {
		t.Fatalf("the new leader compacted %v, want revision 10 -- the former leader's sample", got)
	}
}

// TestCompactionScheduler_ADroppedOrRecreatedDatabaseIsNotCompacted is BL2:
// a record that outlived its database -- dropped, or dropped and created
// again under the name -- compacts nothing, because every proposal carries
// the record's incarnation's drop fence and the state machine refuses it.
// The re-review's probe compacted a re-created database to a revision written
// a second earlier, under a one-hour policy.
func TestCompactionScheduler_ADroppedOrRecreatedDatabaseIsNotCompacted(t *testing.T) {
	store := newSchedStore(100)
	store.bind("acme/app/", "acme", "app", "old", &compaction.Policy{Mode: compaction.Period, RetainPeriod: "1h"})
	now := time.Unix(1_700_000_000, 0)
	_ = store.RecordCompactionSample(context.Background(), "acme/app/", now.Add(-2*time.Hour), 90)
	// The database is dropped and created again: a new incarnation is live,
	// the old record is still there.
	store.mu.Lock()
	store.live["acme/app"] = "new"
	store.mu.Unlock()

	s := newTestScheduler(func() time.Time { return now }, scheduledStore{name: "s", store: store, leader: store})
	if n := s.passOnce(context.Background()); n != 0 || len(store.calls()) != 0 {
		t.Fatalf("the old incarnation's record compacted the new database: %d, %v", n, store.calls())
	}
}

// Samples a store's revisions cannot have produced -- above the current
// revision after the revisions started over -- are never a target.
func TestCompactionScheduler_RevisionsThatStartedOverAreNoTarget(t *testing.T) {
	store := newSchedStore(5) // a restored or re-created store
	store.bind("", "acme", "app", "", &compaction.Policy{Mode: compaction.Period, RetainPeriod: "1h"})
	now := time.Unix(1_700_000_000, 0)
	store.mu.Lock()
	store.table[""].Samples = []compaction.Sample{{At: now.Add(-2 * time.Hour).UnixNano(), Revision: 900}}
	store.mu.Unlock()

	s := newTestScheduler(func() time.Time { return now }, scheduledStore{name: "s", store: store, leader: store})
	if n := s.passOnce(context.Background()); n != 0 || len(store.calls()) != 0 {
		t.Fatalf("a sample above the current revision became a target: %v", store.calls())
	}
	st, _ := store.CompactionState(context.Background(), "")
	if len(st.Samples) != 1 || st.Samples[0].Revision != 5 {
		t.Fatalf("samples after the pass = %+v, want the series started over at 5", st.Samples)
	}
}

// The node keyspace's record is run through the node-keyspace compaction,
// with no drop fence: no database owns it.
func TestCompactionScheduler_NodeScopeUsesTheNodeKeyspaceCompaction(t *testing.T) {
	store := newSchedStore(100)
	store.bind(compaction.NodeScope, "", "", "", &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 10})
	s := newTestScheduler(time.Now, scheduledStore{name: "s", store: store, leader: store})
	if n := s.passOnce(context.Background()); n != 1 {
		t.Fatalf("pass compacted %d", n)
	}
	if got := store.calls(); !reflect.DeepEqual(got, []string{compaction.NodeScope + "@90"}) {
		t.Fatalf("compactions = %v", got)
	}
}

// A store that is the node's alone (leader nil) is run without asking.
func TestCompactionScheduler_NodeLocalStoreIsUngated(t *testing.T) {
	store := newSchedStore(100)
	store.leads = false
	store.bind("acme/app/", "acme", "app", "i1", &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 10})
	s := newTestScheduler(time.Now, scheduledStore{name: "s", store: store})
	if n := s.passOnce(context.Background()); n != 1 {
		t.Fatalf("a node-local store compacted %d, want 1", n)
	}
}

// run drives a pass per tick and returns when its context ends; a pass never
// overlaps another.
func TestCompactionScheduler_RunPassesPerTick(t *testing.T) {
	store := newSchedStore(100)
	store.bind("acme/app/", "acme", "app", "i1", &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 10})
	ticks := make(chan time.Time)
	s := newTestScheduler(time.Now, scheduledStore{name: "s", store: store, leader: store})
	s.ticks = ticks
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.run(ctx); close(done) }()
	ticks <- time.Now()
	ticks <- time.Now()
	cancel()
	<-done
	if len(store.calls()) != 1 {
		t.Fatalf("compactions = %v", store.calls())
	}

	s.running.Store(true)
	if n := s.passOnce(context.Background()); n != 0 {
		t.Fatal("a pass ran while another was running")
	}
}

func TestWithCompactionInterval(t *testing.T) {
	var o serverOptions
	WithCompactionInterval(0)(&o)
	if o.compactionInterval != 0 {
		t.Fatal("a non-positive interval was kept")
	}
	WithCompactionInterval(time.Second)(&o)
	if o.compactionInterval != time.Second {
		t.Fatal("interval not set")
	}
}

func TestWithCompactionClock(t *testing.T) {
	var o serverOptions
	WithCompactionClock(nil)(&o)
	if o.compactionClock != nil {
		t.Fatal("a nil clock was kept")
	}
	fake := clocktest.NewAt(time.Unix(1_700_000_000, 0))
	WithCompactionClock(fake)(&o)
	if o.compactionClock != fake {
		t.Fatal("clock not set")
	}
}

// compactionStores runs the shared store gated by leadership, and leaves out
// a store that keeps no MVCC history.
func TestServer_CompactionStores(t *testing.T) {
	shared := newSchedStore(1)
	reg := NewDatabaseServiceRegistry("default")
	reg.Register("default", &DatabaseServices{Name: "default", MVCC: &schedMVCCView{schedStore: shared}})
	srv := New(testConfig(t, false, false), newMockKVService(), &mockAdminService{}, WithDatabaseServiceRegistry(reg))
	stores := srv.compactionStores(context.Background())
	if len(stores) != 1 || stores[0].leader == nil {
		t.Fatalf("stores = %+v, want the shared store, gated", stores)
	}

	noHistory := NewDatabaseServiceRegistry("default")
	noHistory.Register("default", &DatabaseServices{Name: "default", MVCC: &schedMVCCView{schedStore: shared, noHistory: true}})
	srv = New(testConfig(t, false, false), newMockKVService(), &mockAdminService{}, WithDatabaseServiceRegistry(noHistory))
	if stores := srv.compactionStores(context.Background()); len(stores) != 0 {
		t.Fatalf("a store without history was scheduled: %+v", stores)
	}
}

// schedMVCCView is a raw MVCC service over a schedStore, as a registry
// bundle holds it.
type schedMVCCView struct {
	service.MVCCKVServicer
	*schedStore
	noHistory bool
}

func (v *schedMVCCView) KeepsRevisionHistory() bool { return !v.noHistory }

func (v *schedMVCCView) CurrentRevision(ctx context.Context) (int64, error) {
	return v.schedStore.CurrentRevision(ctx)
}
