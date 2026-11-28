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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// appliedRetention records one ApplyRetention call a fakeTSStore received.
type appliedRetention struct {
	Metric   string
	CutoffNs int64
}

// fakeTSStore is a minimal service.TimeSeriesServicer the scheduler drives
// through ListMetrics, GetRetentionPolicy and ApplyRetention -- the only
// three methods the scheduler calls. Every other method is unused by it
// and stubbed out.
type fakeTSStore struct {
	mu       sync.Mutex
	metrics  []string
	policies map[string]*timeseries.RetentionPolicy
	applied  []appliedRetention
	applyErr map[string]error // per-metric error ApplyRetention returns
}

var _ service.TimeSeriesServicer = (*fakeTSStore)(nil)

func newFakeTSStore(metrics ...string) *fakeTSStore {
	return &fakeTSStore{
		metrics:  metrics,
		policies: map[string]*timeseries.RetentionPolicy{},
		applyErr: map[string]error{},
	}
}

func (f *fakeTSStore) setPolicy(metric string, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.policies[metric] = &timeseries.RetentionPolicy{Duration: d}
}

func (f *fakeTSStore) failApply(metric string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applyErr[metric] = err
}

func (f *fakeTSStore) appliedCalls() []appliedRetention {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]appliedRetention(nil), f.applied...)
}

func (f *fakeTSStore) ListMetrics(string, int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.metrics...), nil
}

func (f *fakeTSStore) GetRetentionPolicy(metric string) (*timeseries.RetentionPolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.policies[metric], nil
}

func (f *fakeTSStore) ApplyRetention(_ context.Context, metric string, cutoff time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.applyErr[metric]; err != nil {
		return err
	}
	f.applied = append(f.applied, appliedRetention{Metric: metric, CutoffNs: cutoff.UnixNano()})
	return nil
}

func (f *fakeTSStore) Write(context.Context, string, float64, time.Time, map[string]string) error {
	return nil
}
func (f *fakeTSStore) WriteBatch(context.Context, []timeseries.DataPoint) error { return nil }
func (f *fakeTSStore) Query(context.Context, string, time.Time, time.Time, ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	return nil, nil
}
func (f *fakeTSStore) QueryWithConsistency(context.Context, string, time.Time, time.Time, consistency.ReadOptions, ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	return nil, nil
}
func (f *fakeTSStore) Aggregate(context.Context, string, time.Time, time.Time, timeseries.AggFunc, time.Duration) ([]timeseries.AggResult, error) {
	return nil, nil
}
func (f *fakeTSStore) AggregateWithConsistency(context.Context, string, time.Time, time.Time, timeseries.AggFunc, time.Duration, consistency.ReadOptions) ([]timeseries.AggResult, error) {
	return nil, nil
}
func (f *fakeTSStore) WritePoint(context.Context, *timeseries.DataPoint) error { return nil }
func (f *fakeTSStore) QueryRich(context.Context, *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
	return nil, nil
}
func (f *fakeTSStore) QueryRichWithConsistency(context.Context, *timeseries.QueryRequest, consistency.ReadOptions) (*timeseries.QueryResult, error) {
	return nil, nil
}
func (f *fakeTSStore) DeleteSeries(context.Context, *timeseries.DeleteRequest) error { return nil }
func (f *fakeTSStore) SetRetentionPolicy(context.Context, string, *timeseries.RetentionPolicy) error {
	return nil
}
func (f *fakeTSStore) ListMetricsWithConsistency(context.Context, string, int, consistency.ReadOptions) ([]string, error) {
	return nil, nil
}

// fakeShardLeader is a standalone service.ShardLeader for tests that need
// to gate a store independently of schedStore.
type fakeShardLeader struct{ leads bool }

func (f *fakeShardLeader) LeadsShard() bool { return f.leads }

// newTestRetentionScheduler runs over stores with a controllable clock.
func newTestRetentionScheduler(now func() time.Time, stores ...scheduledRetentionStore) *retentionScheduler {
	return &retentionScheduler{
		stores:   func(context.Context) []scheduledRetentionStore { return stores },
		interval: time.Hour,
		now:      now,
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestRetentionScheduler_NoPolicySweepsNothing(t *testing.T) {
	store := newFakeTSStore("cpu.usage")
	s := newTestRetentionScheduler(time.Now, scheduledRetentionStore{name: "s", ts: store, leader: &fakeShardLeader{leads: true}})

	if n := s.passOnce(context.Background()); n != 0 || len(store.appliedCalls()) != 0 {
		t.Fatalf("a metric with no policy was swept: %d, %v", n, store.appliedCalls())
	}
}

func TestRetentionScheduler_SweepsMetricWithPolicy(t *testing.T) {
	store := newFakeTSStore("cpu.usage")
	store.setPolicy("cpu.usage", 24*time.Hour)
	now := time.Unix(1_700_000_000, 0)
	s := newTestRetentionScheduler(func() time.Time { return now }, scheduledRetentionStore{name: "s", ts: store, leader: &fakeShardLeader{leads: true}})

	if n := s.passOnce(context.Background()); n != 1 {
		t.Fatalf("pass swept %d, want 1", n)
	}
	calls := store.appliedCalls()
	if len(calls) != 1 {
		t.Fatalf("applied calls = %v, want 1", calls)
	}
	wantCutoff := now.Add(-24 * time.Hour).UnixNano()
	if calls[0].Metric != "cpu.usage" || calls[0].CutoffNs != wantCutoff {
		t.Fatalf("applied = %+v, want metric cpu.usage cutoff %d", calls[0], wantCutoff)
	}
}

func TestRetentionScheduler_SkipsNonPositiveDuration(t *testing.T) {
	store := newFakeTSStore("cpu.usage")
	store.setPolicy("cpu.usage", 0)
	s := newTestRetentionScheduler(time.Now, scheduledRetentionStore{name: "s", ts: store, leader: &fakeShardLeader{leads: true}})

	if n := s.passOnce(context.Background()); n != 0 || len(store.appliedCalls()) != 0 {
		t.Fatalf("a zero-duration policy was swept: %d, %v", n, store.appliedCalls())
	}
}

// TestRetentionScheduler_DefaultRetentionAppliesWithoutAPolicy pins
// timeseries.default_retention: a metric with no policy, and one whose
// policy has a duration of zero, are swept at the default, while a metric
// with its own non-zero duration keeps it.
func TestRetentionScheduler_DefaultRetentionAppliesWithoutAPolicy(t *testing.T) {
	store := newFakeTSStore("cpu.usage", "disk.free", "mem.used")
	store.setPolicy("disk.free", 0)
	store.setPolicy("mem.used", time.Hour)
	now := time.Unix(1_700_000_000, 0)
	s := newTestRetentionScheduler(func() time.Time { return now }, scheduledRetentionStore{name: "s", ts: store, leader: &fakeShardLeader{leads: true}})
	s.defaultRetention = 24 * time.Hour

	if n := s.passOnce(context.Background()); n != 3 {
		t.Fatalf("pass swept %d, want 3", n)
	}
	want := map[string]int64{
		"cpu.usage": now.Add(-24 * time.Hour).UnixNano(),
		"disk.free": now.Add(-24 * time.Hour).UnixNano(),
		"mem.used":  now.Add(-time.Hour).UnixNano(),
	}
	calls := store.appliedCalls()
	if len(calls) != len(want) {
		t.Fatalf("applied calls = %v, want %d", calls, len(want))
	}
	for _, c := range calls {
		if c.CutoffNs != want[c.Metric] {
			t.Errorf("%s swept at cutoff %d, want %d", c.Metric, c.CutoffNs, want[c.Metric])
		}
	}
}

// TestRetentionScheduler_ZeroDefaultKeepsUnpolicedMetrics pins the zero
// default: a metric with no policy keeps every point.
func TestRetentionScheduler_ZeroDefaultKeepsUnpolicedMetrics(t *testing.T) {
	store := newFakeTSStore("cpu.usage", "disk.free")
	store.setPolicy("disk.free", 0)
	s := newTestRetentionScheduler(time.Now, scheduledRetentionStore{name: "s", ts: store, leader: &fakeShardLeader{leads: true}})

	if n := s.passOnce(context.Background()); n != 0 || len(store.appliedCalls()) != 0 {
		t.Fatalf("a zero default swept a metric with no policy: %d, %v", n, store.appliedCalls())
	}
}

// TestRetentionScheduler_OnlyTheLeaderRuns: a store this node does not lead
// is not swept, so exactly one node sweeps each store.
func TestRetentionScheduler_OnlyTheLeaderRuns(t *testing.T) {
	store := newFakeTSStore("cpu.usage")
	store.setPolicy("cpu.usage", time.Hour)
	leader := &fakeShardLeader{leads: false}
	s := newTestRetentionScheduler(time.Now, scheduledRetentionStore{name: "s", ts: store, leader: leader})

	if n := s.passOnce(context.Background()); n != 0 || len(store.appliedCalls()) != 0 {
		t.Fatalf("a follower swept: %d, %v", n, store.appliedCalls())
	}

	leader.leads = true
	if n := s.passOnce(context.Background()); n != 1 {
		t.Fatalf("the new leader swept %d, want 1", n)
	}
}

// A store that is the node's alone (leader nil) is swept without asking.
func TestRetentionScheduler_NodeLocalStoreIsUngated(t *testing.T) {
	store := newFakeTSStore("cpu.usage")
	store.setPolicy("cpu.usage", time.Hour)
	s := newTestRetentionScheduler(time.Now, scheduledRetentionStore{name: "s", ts: store})

	if n := s.passOnce(context.Background()); n != 1 {
		t.Fatalf("a node-local store swept %d, want 1", n)
	}
}

// A refusal on one metric (e.g. the database's drop fence) does not stop
// the pass from sweeping the rest.
func TestRetentionScheduler_ARefusalOnOneMetricDoesNotStopOthers(t *testing.T) {
	store := newFakeTSStore("cpu.usage", "mem.usage")
	store.setPolicy("cpu.usage", time.Hour)
	store.setPolicy("mem.usage", time.Hour)
	store.failApply("cpu.usage", &database.DroppingError{})

	s := newTestRetentionScheduler(time.Now, scheduledRetentionStore{name: "s", ts: store, leader: &fakeShardLeader{leads: true}})
	if n := s.passOnce(context.Background()); n != 1 {
		t.Fatalf("pass swept %d, want 1 (mem.usage only)", n)
	}
	calls := store.appliedCalls()
	if len(calls) != 1 || calls[0].Metric != "mem.usage" {
		t.Fatalf("applied = %v, want only mem.usage", calls)
	}
}

func TestRetentionScheduler_GenericApplyErrorDoesNotStopOthers(t *testing.T) {
	store := newFakeTSStore("cpu.usage", "mem.usage")
	store.setPolicy("cpu.usage", time.Hour)
	store.setPolicy("mem.usage", time.Hour)
	store.failApply("cpu.usage", &service.WriteGuardRefusedError{Key: "k"})

	s := newTestRetentionScheduler(time.Now, scheduledRetentionStore{name: "s", ts: store, leader: &fakeShardLeader{leads: true}})
	if n := s.passOnce(context.Background()); n != 1 {
		t.Fatalf("pass swept %d, want 1 (mem.usage only)", n)
	}
}

// run drives a pass per tick and returns when its context ends; a pass
// never overlaps another.
func TestRetentionScheduler_RunPassesPerTick(t *testing.T) {
	store := newFakeTSStore("cpu.usage")
	store.setPolicy("cpu.usage", time.Hour)
	ticks := make(chan time.Time)
	s := newTestRetentionScheduler(time.Now, scheduledRetentionStore{name: "s", ts: store, leader: &fakeShardLeader{leads: true}})
	s.ticks = ticks
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.run(ctx); close(done) }()
	ticks <- time.Now()
	ticks <- time.Now()
	cancel()
	<-done
	if len(store.appliedCalls()) != 2 {
		t.Fatalf("applied calls = %v, want 2 (one per tick)", store.appliedCalls())
	}

	s.running.Store(true)
	if n := s.passOnce(context.Background()); n != 0 {
		t.Fatal("a pass ran while another was running")
	}
}

func TestWithRetentionInterval(t *testing.T) {
	var o serverOptions
	WithRetentionInterval(0)(&o)
	if o.retentionInterval != 0 {
		t.Fatal("a non-positive interval was kept")
	}
	WithRetentionInterval(time.Second)(&o)
	if o.retentionInterval != time.Second {
		t.Fatal("interval not set")
	}
}

func TestWithDefaultRetention(t *testing.T) {
	var o serverOptions
	WithDefaultRetention(-time.Second)(&o)
	if o.defaultRetention != 0 {
		t.Fatal("a non-positive default retention was kept")
	}
	WithDefaultRetention(time.Hour)(&o)
	if o.defaultRetention != time.Hour {
		t.Fatal("default retention not set")
	}
}

// retentionStores runs the shared store gated by leadership, and leaves out
// a bundle with no time-series service.
func TestServer_RetentionStores(t *testing.T) {
	shared := newSchedStore(1)
	tsStore := newFakeTSStore("cpu.usage")
	reg := NewDatabaseServiceRegistry("default")
	reg.Register("default", &DatabaseServices{Name: "default", MVCC: &schedMVCCView{schedStore: shared}, TimeSeries: tsStore})
	srv := New(testConfig(t, false, false), newMockKVService(), &mockAdminService{}, WithDatabaseServiceRegistry(reg))
	stores := srv.retentionStores(context.Background())
	if len(stores) != 1 || stores[0].leader == nil {
		t.Fatalf("stores = %+v, want the shared store, gated", stores)
	}

	noTS := NewDatabaseServiceRegistry("default")
	noTS.Register("default", &DatabaseServices{Name: "default", MVCC: &schedMVCCView{schedStore: shared}})
	srv = New(testConfig(t, false, false), newMockKVService(), &mockAdminService{}, WithDatabaseServiceRegistry(noTS))
	if stores := srv.retentionStores(context.Background()); len(stores) != 0 {
		t.Fatalf("a bundle without a time-series service was scheduled: %+v", stores)
	}
}

// TestRetentionScheduler_SweepsMaterializedDatabase proves one retention
// pass reaches a database the Manager materialized in its own store: the
// expired point goes and the live one stays.
func TestRetentionScheduler_SweepsMaterializedDatabase(t *testing.T) {
	const tenantID, name, metric = "acme", "metrics", "cpu.usage"
	ctx := context.Background()
	factory := &fakeMaterializingFactory{}
	dbMgr := databasetest.NewManager(nil, t.TempDir(), database.WithBackendFactory(factory))
	if _, err := dbMgr.Create(database.DatabaseConfig{TenantID: tenantID, Name: name, Backend: database.BackendMemory}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	fake := factory.last(t)
	ts := service.NewTimeSeriesService(fake.Host(), fake.ShardID())

	now := time.Now().UTC().Truncate(time.Second)
	expired := &service.DataPoint{Metric: metric, Value: 1, Timestamp: now.Add(-2 * time.Hour)}
	live := &service.DataPoint{Metric: metric, Value: 2, Timestamp: now.Add(-time.Minute)}
	for _, p := range []*service.DataPoint{expired, live} {
		if err := ts.WritePoint(ctx, p); err != nil {
			var notCompiled *timeseries.NotCompiledError
			if errors.As(err, &notCompiled) || strings.Contains(err.Error(), (&timeseries.NotCompiledError{}).Error()) {
				t.Skipf("time series not built: %v", err)
			}
			t.Fatalf("WritePoint: %v", err)
		}
	}
	if err := ts.SetRetentionPolicy(ctx, metric, &service.RetentionPolicy{Duration: time.Hour}); err != nil {
		t.Fatalf("SetRetentionPolicy: %v", err)
	}

	srv := New(testConfig(t, false, false), newMockKVService(), &mockAdminService{}, WithDatabaseManager(dbMgr))
	s := &retentionScheduler{
		stores:   srv.retentionStores,
		interval: time.Hour,
		now:      func() time.Time { return now },
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if n := s.passOnce(ctx); n != 1 {
		t.Fatalf("retention pass swept %d metrics, want 1", n)
	}

	res, err := ts.QueryRich(ctx, &service.QueryRequest{Metric: metric, StartTime: now.Add(-24 * time.Hour), EndTime: now.Add(time.Hour)})
	if err != nil {
		t.Fatalf("QueryRich: %v", err)
	}
	if len(res.Points) != 1 || res.Points[0].Value != live.Value {
		t.Fatalf("points after retention = %+v, want only the live point", res.Points)
	}
}

// reopeningFactory hands a database the backend it was first created
// with when a later Manager opens it again, standing in for a store
// whose files outlive a restart.
type reopeningFactory struct {
	inner  *fakeMaterializingFactory
	mu     sync.Mutex
	byName map[string]database.Backend
	opens  int
}

func (f *reopeningFactory) Create(cfg database.DatabaseConfig, dataDir string) (database.Backend, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens++
	key := cfg.TenantID + "/" + cfg.Name
	if b, ok := f.byName[key]; ok {
		return b, nil
	}
	b, err := f.inner.Create(cfg, dataDir)
	if err != nil {
		return nil, err
	}
	f.byName[key] = b
	return b, nil
}

func (f *reopeningFactory) Close() error { return nil }

// After a restart nothing has opened an isolated-tenant database: the
// retention pass reaches it from the catalog, opens it, and applies its
// policy without any foreground request.
func TestRetentionScheduler_SweepsUnopenedDatabaseAfterRestart(t *testing.T) {
	const tenantID, name, metric = "acme", "metrics", "cpu.usage"
	ctx := context.Background()
	dir := t.TempDir()
	inner := &fakeMaterializingFactory{}
	factory := &reopeningFactory{inner: inner, byName: map[string]database.Backend{}}
	before := databasetest.NewManager(nil, dir, database.WithBackendFactory(factory))
	if _, err := before.Create(database.DatabaseConfig{TenantID: tenantID, Name: name, Backend: database.BackendMemory}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	fake := inner.last(t)
	ts := service.NewTimeSeriesService(fake.Host(), fake.ShardID())

	now := time.Now().UTC().Truncate(time.Second)
	expired := &service.DataPoint{Metric: metric, Value: 1, Timestamp: now.Add(-2 * time.Hour)}
	live := &service.DataPoint{Metric: metric, Value: 2, Timestamp: now.Add(-time.Minute)}
	for _, p := range []*service.DataPoint{expired, live} {
		if err := ts.WritePoint(ctx, p); err != nil {
			var notCompiled *timeseries.NotCompiledError
			if errors.As(err, &notCompiled) || strings.Contains(err.Error(), (&timeseries.NotCompiledError{}).Error()) {
				t.Skipf("time series not built: %v", err)
			}
			t.Fatalf("WritePoint: %v", err)
		}
	}
	if err := ts.SetRetentionPolicy(ctx, metric, &service.RetentionPolicy{Duration: time.Hour}); err != nil {
		t.Fatalf("SetRetentionPolicy: %v", err)
	}

	// The restarted node's Manager shares the catalog and has opened
	// nothing.
	after := databasetest.NewManager(nil, dir, database.WithBackendFactory(factory))
	if open := after.List(); len(open) != 0 {
		t.Fatalf("restarted Manager has %d open databases, want 0", len(open))
	}
	opensBefore := factory.opens

	srv := New(testConfig(t, false, false), newMockKVService(), &mockAdminService{}, WithDatabaseManager(after))
	s := &retentionScheduler{
		stores:   srv.retentionStores,
		interval: time.Hour,
		now:      func() time.Time { return now },
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if n := s.passOnce(ctx); n != 1 {
		t.Fatalf("retention pass swept %d metrics, want 1", n)
	}
	if factory.opens == opensBefore {
		t.Fatal("the retention pass never opened the unopened database")
	}

	res, err := ts.QueryRich(ctx, &service.QueryRequest{Metric: metric, StartTime: now.Add(-24 * time.Hour), EndTime: now.Add(time.Hour)})
	if err != nil {
		t.Fatalf("QueryRich: %v", err)
	}
	if len(res.Points) != 1 || res.Points[0].Value != live.Value {
		t.Fatalf("points after retention = %+v, want only the live point", res.Points)
	}
}
