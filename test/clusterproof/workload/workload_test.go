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

//go:build clusterproof

package workload

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/history"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/ledger"
)

// nodeDownError is the cause a fake node gives for refusing a connection.
type nodeDownError struct{}

func (nodeDownError) Error() string { return "fake node is down" }

// fault runs after an operation has taken effect on the fake cluster (or, for
// a get, after it has read) and may replace its answer: a reply lost after
// the write landed, a reply that never comes, a read that errs.
type fault func(ctx context.Context, endpoint string, kind history.Kind) error

// fakeCluster is one linearizable register map served by several endpoints,
// any of which can be taken down.
type fakeCluster struct {
	mu   sync.Mutex
	data map[string]string

	down  map[string]*atomic.Bool // fixed at construction
	fault atomic.Pointer[fault]
	dials atomic.Int64
}

func newFakeCluster(endpoints ...string) *fakeCluster {
	f := &fakeCluster{data: make(map[string]string), down: make(map[string]*atomic.Bool)}
	for _, e := range endpoints {
		f.down[e] = new(atomic.Bool)
	}
	return f
}

func (f *fakeCluster) setFault(fl fault) { f.fault.Store(&fl) }

func (f *fakeCluster) dial(_ context.Context, endpoint string) (Conn, error) {
	f.dials.Add(1)
	if f.down[endpoint].Load() {
		return nil, &transport.ConnectionError{Address: endpoint, Err: nodeDownError{}}
	}
	return &fakeConn{f: f, endpoint: endpoint}, nil
}

// fakeConn is a connection to one endpoint of a fakeCluster.
type fakeConn struct {
	f        *fakeCluster
	endpoint string
}

func (c *fakeConn) reachable() error {
	if c.f.down[c.endpoint].Load() {
		return &transport.ConnectionError{Address: c.endpoint, Err: nodeDownError{}}
	}
	return nil
}

func (c *fakeConn) answer(ctx context.Context, kind history.Kind) error {
	if p := c.f.fault.Load(); p != nil {
		return (*p)(ctx, c.endpoint, kind)
	}
	return nil
}

func (c *fakeConn) Put(ctx context.Context, key string, value []byte, _ ...transport.WriteOption) error {
	if err := c.reachable(); err != nil {
		return err
	}
	c.f.mu.Lock()
	c.f.data[key] = string(value)
	c.f.mu.Unlock()
	return c.answer(ctx, history.KindPut)
}

func (c *fakeConn) Get(ctx context.Context, key string, _ ...transport.ReadOption) ([]byte, error) {
	if err := c.reachable(); err != nil {
		return nil, err
	}
	c.f.mu.Lock()
	v, ok := c.f.data[key]
	c.f.mu.Unlock()
	if err := c.answer(ctx, history.KindGet); err != nil {
		return nil, err
	}
	if !ok {
		return nil, &transport.KeyNotFoundError{Key: key}
	}
	return []byte(v), nil
}

func (c *fakeConn) Delete(ctx context.Context, key string, _ ...transport.WriteOption) error {
	if err := c.reachable(); err != nil {
		return err
	}
	c.f.mu.Lock()
	delete(c.f.data, key)
	c.f.mu.Unlock()
	return c.answer(ctx, history.KindDelete)
}

func (c *fakeConn) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if err := c.reachable(); err != nil {
		return false, err
	}
	c.f.mu.Lock()
	cur, ok := c.f.data[key]
	swapped := ok && cur == string(expected)
	if swapped {
		c.f.data[key] = string(next)
	}
	c.f.mu.Unlock()
	if err := c.answer(ctx, history.KindCAS); err != nil {
		return false, err
	}
	return swapped, nil
}

func (c *fakeConn) Close() error { return nil }

var _ Conn = (*fakeConn)(nil)

func testConfig(endpoints ...string) Config {
	c := Config{
		Endpoints:     endpoints,
		Clients:       4,
		Keys:          4,
		KeyPrefix:     "test",
		Seed:          7,
		OpsPerClient:  200,
		OpTimeout:     Duration(2 * time.Second),
		RedialBackoff: Duration(time.Millisecond),
	}
	if err := c.Normalize(); err != nil {
		panic(err)
	}
	return c
}

// run builds and runs a workload, takes its final read, and returns the
// history and stats.
func run(t *testing.T, cfg Config, dial Dialer, opts ...Option) (history.History, Stats, error) {
	t.Helper()
	rec := history.NewRecorder(history.NewMonotonicClock())
	w, err := New(cfg, dial, rec, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	stats := w.Run(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	finalErr := w.FinalRead(ctx)
	h := rec.History()
	if err := h.Validate(); err != nil {
		t.Fatalf("recorded history is invalid: %v", err)
	}
	return h, stats, finalErr
}

// requireLedgerValid fails the test when the ledger finds an anomaly.
func requireLedgerValid(t *testing.T, h history.History) history.Result {
	t.Helper()
	res, err := ledger.Checker{}.Check(h)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if !res.Valid {
		t.Fatalf("ledger found anomalies: %+v", res.Anomalies)
	}
	return res
}

// countBy tallies a history's records.
func countBy[K comparable](h history.History, key func(history.Op) K) map[K]int {
	m := make(map[K]int)
	for _, op := range h {
		m[key(op)]++
	}
	return m
}

func TestRunHealthyClusterPassesLedger(t *testing.T) {
	cfg := testConfig("n1", "n2", "n3")
	f := newFakeCluster(cfg.Endpoints...)
	h, stats, err := run(t, cfg, f.dial)
	if err != nil {
		t.Fatalf("FinalRead: %v", err)
	}
	res := requireLedgerValid(t, h)
	if res.Checked == 0 {
		t.Fatal("ledger checked no writes")
	}

	perClient := countBy(h, func(op history.Op) int { return op.Client })
	for id := range cfg.Clients {
		if perClient[id] != cfg.OpsPerClient {
			t.Fatalf("client %d recorded %d ops, want %d", id, perClient[id], cfg.OpsPerClient)
		}
	}
	if perClient[cfg.Clients] != cfg.Keys {
		t.Fatalf("verifier recorded %d reads, want one per key (%d)", perClient[cfg.Clients], cfg.Keys)
	}
	if stats.Ops() != uint64(cfg.Clients*cfg.OpsPerClient) || stats.Fail != 0 || stats.Indeterminate != 0 {
		t.Fatalf("stats = %+v", stats)
	}
	kinds := countBy(h, func(op history.Op) history.Kind { return op.Kind })
	for _, k := range []history.Kind{history.KindPut, history.KindGet, history.KindDelete, history.KindCAS} {
		if kinds[k] == 0 {
			t.Fatalf("mix never issued a %s: %v", k, kinds)
		}
	}
	swapped := 0
	for _, op := range h {
		if op.Kind == history.KindCAS && op.Output != nil && op.Output.Swapped {
			swapped++
		}
		if op.CallNS > op.ReturnNS {
			t.Fatalf("op %d returned before it was called", op.ID)
		}
	}
	if swapped == 0 {
		t.Fatal("no compare-and-swap ever swapped: the client is not expecting values it saw")
	}
}

func TestRunSurvivesDeadNode(t *testing.T) {
	cfg := testConfig("n1", "n2", "n3")
	cfg.Clients = 3
	cfg.OpsPerClient = 50
	f := newFakeCluster(cfg.Endpoints...)
	f.down["n2"].Store(true)

	h, stats, err := run(t, cfg, f.dial)
	if err != nil {
		t.Fatalf("FinalRead: %v", err)
	}
	requireLedgerValid(t, h)
	if stats.DialFailures == 0 {
		t.Fatal("no dial failure counted against the dead node")
	}
	perClient := countBy(h, func(op history.Op) int { return op.Client })
	if perClient[1] != cfg.OpsPerClient {
		t.Fatalf("client pinned to the dead node recorded %d ops, want %d: it did not move on", perClient[1], cfg.OpsPerClient)
	}
}

func TestRunRecordsNodeLossAsIndeterminate(t *testing.T) {
	cfg := testConfig("n1", "n2", "n3")
	f := newFakeCluster(cfg.Endpoints...)
	// n1 dies after it has answered 100 ops itself, not after the cluster has.
	// Clients 0 and 3 start on n1 and dial it lazily, so counting every node's
	// answers let n2 and n3 reach 100 before either had connected: both dials
	// then failed, the clients moved on, and n1's loss surfaced as nothing but
	// dial failures (run 55885). Counting n1's own answers means the client
	// that drew the 100th answer is connected to n1, has ops left, and sends
	// its next one into the dead node.
	var answered atomic.Int64
	f.setFault(func(_ context.Context, endpoint string, _ history.Kind) error {
		if endpoint == "n1" && answered.Add(1) == 100 {
			f.down["n1"].Store(true) // n1 dies mid-run
		}
		return nil
	})

	h, stats, err := run(t, cfg, f.dial)
	if err != nil {
		t.Fatalf("FinalRead: %v", err)
	}
	requireLedgerValid(t, h)

	lost := 0
	for _, op := range h {
		if op.Error == "" {
			continue
		}
		lost++
		want := history.OutcomeIndeterminate
		if op.Kind == history.KindGet {
			want = history.OutcomeFail
		}
		if op.Outcome != want {
			t.Fatalf("op %d (%s) failed with %q and was recorded %s, want %s", op.ID, op.Kind, op.Error, op.Outcome, want)
		}
	}
	if lost == 0 || stats.Indeterminate+stats.Fail == 0 {
		t.Fatalf("node loss recorded nothing: lost=%d stats=%+v", lost, stats)
	}
	if stats.Ops() != uint64(cfg.Clients*cfg.OpsPerClient) {
		t.Fatalf("clients stopped early after node loss: stats=%+v", stats)
	}
}

func TestRunIndeterminateWritesThatApplied(t *testing.T) {
	cfg := testConfig("n1", "n2")
	f := newFakeCluster(cfg.Endpoints...)
	var writes atomic.Int64
	f.setFault(func(_ context.Context, _ string, kind history.Kind) error {
		if kind != history.KindGet && writes.Add(1)%3 == 0 {
			return context.DeadlineExceeded // applied, reply lost
		}
		return nil
	})

	h, stats, err := run(t, cfg, f.dial)
	if err != nil {
		t.Fatalf("FinalRead: %v", err)
	}
	requireLedgerValid(t, h)
	if stats.Indeterminate == 0 {
		t.Fatalf("no write recorded indeterminate: %+v", stats)
	}
	for _, op := range h {
		if op.Kind != history.KindGet && op.Outcome == history.OutcomeFail {
			t.Fatalf("write %d recorded fail although it may have applied", op.ID)
		}
	}
}

func TestRunTimesOutHungWrite(t *testing.T) {
	cfg := testConfig("n1")
	cfg.Clients, cfg.OpsPerClient = 1, 3
	cfg.Mix = Mix{Put: 1}
	cfg.OpTimeout = Duration(50 * time.Millisecond)
	f := newFakeCluster(cfg.Endpoints...)
	var once atomic.Bool
	f.setFault(func(ctx context.Context, _ string, _ history.Kind) error {
		if once.CompareAndSwap(false, true) {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	})

	h, _, err := run(t, cfg, f.dial)
	if err != nil {
		t.Fatalf("FinalRead: %v", err)
	}
	requireLedgerValid(t, h)
	first := h[0]
	if first.Outcome != history.OutcomeIndeterminate {
		t.Fatalf("hung write recorded %s, want indeterminate", first.Outcome)
	}
	if d := time.Duration(first.ReturnNS - first.CallNS); d < 50*time.Millisecond {
		t.Fatalf("hung write returned after %v, before its timeout", d)
	}
}

func TestRunStopDoesNotCutInFlightOps(t *testing.T) {
	cfg := testConfig("n1")
	cfg.Clients, cfg.OpsPerClient = 1, 0
	cfg.Mix = Mix{Put: 1}
	cfg.OpTimeout = Duration(150 * time.Millisecond)
	f := newFakeCluster(cfg.Endpoints...)
	started := make(chan struct{})
	var once atomic.Bool
	f.setFault(func(ctx context.Context, _ string, _ history.Kind) error {
		if once.CompareAndSwap(false, true) {
			close(started)
		}
		<-ctx.Done()
		return ctx.Err()
	})

	rec := history.NewRecorder(history.NewMonotonicClock())
	w, err := New(cfg, f.dial, rec)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Stats)
	go func() { done <- w.Run(ctx) }()
	<-started
	cancel()
	stats := <-done

	h := rec.History()
	if len(h) != 1 || stats.Indeterminate != 1 {
		t.Fatalf("history = %+v, stats = %+v; want the one in-flight write", h, stats)
	}
	if d := time.Duration(h[0].ReturnNS - h[0].CallNS); d < 150*time.Millisecond {
		t.Fatalf("in-flight write cut short after %v by stopping the run", d)
	}
}

func TestRunStopsAtDuration(t *testing.T) {
	cfg := testConfig("n1")
	cfg.OpsPerClient = 0
	cfg.Duration = Duration(50 * time.Millisecond)
	f := newFakeCluster(cfg.Endpoints...)
	start := time.Now()
	h, stats, err := run(t, cfg, f.dial)
	if err != nil {
		t.Fatalf("FinalRead: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("run ignored its duration: %v", elapsed)
	}
	if stats.Ops() == 0 {
		t.Fatal("run issued nothing")
	}
	requireLedgerValid(t, h)
}

func TestRunStopsOnContext(t *testing.T) {
	cfg := testConfig("n1")
	cfg.OpsPerClient = 0
	f := newFakeCluster(cfg.Endpoints...)
	rec := history.NewRecorder(history.NewMonotonicClock())
	w, err := New(cfg, f.dial, rec)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if stats := w.Run(ctx); stats.Ops() == 0 {
		t.Fatal("run issued nothing before its context ended")
	}
}

func TestRunWithEveryNodeDownStopsOnContext(t *testing.T) {
	cfg := testConfig("n1", "n2")
	f := newFakeCluster(cfg.Endpoints...)
	f.down["n1"].Store(true)
	f.down["n2"].Store(true)
	rec := history.NewRecorder(history.NewMonotonicClock())
	w, err := New(cfg, f.dial, rec)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	stats := w.Run(ctx)
	if stats.Ops() != 0 || stats.DialFailures == 0 {
		t.Fatalf("stats = %+v, want dial failures and no ops", stats)
	}
	if len(rec.History()) != 0 {
		t.Fatal("an operation was recorded with no node to send it to")
	}
}

func TestFinalReadMovesPastDeadNode(t *testing.T) {
	cfg := testConfig("n1", "n2")
	cfg.Clients = 2 // the verifier, client 2, starts on n1
	cfg.OpsPerClient = 20
	f := newFakeCluster(cfg.Endpoints...)
	rec := history.NewRecorder(history.NewMonotonicClock())
	w, err := New(cfg, f.dial, rec)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w.Run(context.Background())
	f.down["n1"].Store(true)
	if err := w.FinalRead(context.Background()); err != nil {
		t.Fatalf("FinalRead: %v", err)
	}
	requireLedgerValid(t, rec.History())
}

func TestFinalReadRetriesFailedRead(t *testing.T) {
	cfg := testConfig("n1", "n2")
	cfg.OpsPerClient = 20
	f := newFakeCluster(cfg.Endpoints...)
	rec := history.NewRecorder(history.NewMonotonicClock())
	w, err := New(cfg, f.dial, rec)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w.Run(context.Background())
	f.setFault(func(_ context.Context, endpoint string, kind history.Kind) error {
		if kind == history.KindGet && endpoint == "n1" {
			return &transport.RequestError{Method: "get", StatusCode: 503}
		}
		return nil
	})
	if err := w.FinalRead(context.Background()); err != nil {
		t.Fatalf("FinalRead: %v", err)
	}
	h := rec.History()
	requireLedgerValid(t, h)
	failed := 0
	for _, op := range h {
		if op.Client == cfg.Clients && op.Outcome == history.OutcomeFail {
			failed++
		}
	}
	if failed == 0 {
		t.Fatal("the verifier's failed reads were not recorded")
	}
}

func TestFinalReadReportsUnreadKeys(t *testing.T) {
	cfg := testConfig("n1", "n2")
	cfg.OpsPerClient = 20
	cfg.Mix = Mix{Put: 1} // no load-phase read can stand in for the final one
	f := newFakeCluster(cfg.Endpoints...)
	rec := history.NewRecorder(history.NewMonotonicClock())
	w, err := New(cfg, f.dial, rec)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w.Run(context.Background())
	f.down["n1"].Store(true)
	f.down["n2"].Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = w.FinalRead(ctx)
	var fre *FinalReadError
	if !errors.As(err, &fre) || len(fre.Keys) != cfg.Keys {
		t.Fatalf("err = %v, want FinalReadError naming all %d keys", err, cfg.Keys)
	}
	res, err := ledger.Checker{}.Check(rec.History())
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if res.Valid {
		t.Fatal("ledger accepted a history whose keys were never read back")
	}
}

func TestNewRejects(t *testing.T) {
	rec := history.NewRecorder(history.NewMonotonicClock())
	f := newFakeCluster("n1")
	cases := map[string]struct {
		cfg   Config
		dial  Dialer
		rec   *history.Recorder
		opts  []Option
		field string
	}{
		"invalid config": {Config{}, f.dial, rec, nil, "endpoints"},
		"nil dialer":     {testConfig("n1"), nil, rec, nil, "dialer"},
		"nil recorder":   {testConfig("n1"), f.dial, nil, nil, "recorder"},
		"nil classifier": {testConfig("n1"), f.dial, rec, []Option{WithClassifier(nil)}, "classifier"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := New(tc.cfg, tc.dial, tc.rec, tc.opts...)
			var ce *ConfigError
			if !errors.As(err, &ce) || ce.Field != tc.field {
				t.Fatalf("err = %v, want ConfigError on %q", err, tc.field)
			}
		})
	}
}

func TestWithClassifier(t *testing.T) {
	cfg := testConfig("n1")
	cfg.OpsPerClient = 10
	f := newFakeCluster(cfg.Endpoints...)
	pessimist := func(history.Kind, error) history.Outcome { return history.OutcomeIndeterminate }
	rec := history.NewRecorder(history.NewMonotonicClock())
	w, err := New(cfg, f.dial, rec, WithClassifier(pessimist))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if stats := w.Run(context.Background()); stats.Indeterminate != stats.Ops() || stats.Ops() == 0 {
		t.Fatalf("classifier not used: %+v", stats)
	}
}

func TestMixSelectsOperations(t *testing.T) {
	cases := map[string]struct {
		mix  Mix
		want map[history.Kind]bool
	}{
		"puts only":    {Mix{Put: 1}, map[history.Kind]bool{history.KindPut: true}},
		"gets only":    {Mix{Get: 1}, map[history.Kind]bool{history.KindGet: true}},
		"deletes only": {Mix{Delete: 1}, map[history.Kind]bool{history.KindDelete: true}},
		// With nothing written, a swap has no value to expect and reads instead.
		"cas only, nothing written": {Mix{CAS: 1}, map[history.Kind]bool{history.KindGet: true}},
		"put and cas":               {Mix{Put: 1, CAS: 1}, map[history.Kind]bool{history.KindPut: true, history.KindCAS: true, history.KindGet: true}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig("n1")
			cfg.Mix = tc.mix
			cfg.OpsPerClient = 50
			f := newFakeCluster(cfg.Endpoints...)
			rec := history.NewRecorder(history.NewMonotonicClock())
			w, err := New(cfg, f.dial, rec)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			w.Run(context.Background())
			for kind := range countBy(rec.History(), func(op history.Op) history.Kind { return op.Kind }) {
				if !tc.want[kind] {
					t.Fatalf("mix %+v issued a %s", tc.mix, kind)
				}
			}
		})
	}
}

func TestRunIsReproducibleBySeed(t *testing.T) {
	sequence := func() []string {
		cfg := testConfig("n1")
		cfg.Clients, cfg.OpsPerClient = 1, 100
		f := newFakeCluster(cfg.Endpoints...)
		rec := history.NewRecorder(history.NewMonotonicClock())
		w, err := New(cfg, f.dial, rec)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		w.Run(context.Background())
		var s []string
		for _, op := range rec.History() {
			s = append(s, string(op.Kind)+" "+op.Key)
		}
		return s
	}
	a, b := sequence(), sequence()
	if len(a) != len(b) {
		t.Fatalf("lengths differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("op %d differs between runs with one seed: %q vs %q", i, a[i], b[i])
		}
	}
}

func TestKeysAndValuesAreUnique(t *testing.T) {
	cfg := testConfig("n1")
	f := newFakeCluster(cfg.Endpoints...)
	rec := history.NewRecorder(history.NewMonotonicClock())
	w, err := New(cfg, f.dial, rec)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	keys := w.Keys()
	if len(keys) != cfg.Keys || keys[0] != "test/k0" {
		t.Fatalf("Keys() = %v", keys)
	}
	keys[0] = "mutated"
	if w.Keys()[0] != "test/k0" {
		t.Fatal("Keys() exposes the workload's own slice")
	}
	w.Run(context.Background())
	seen := make(map[string]uint64)
	for _, op := range rec.History() {
		if op.Input == nil {
			continue
		}
		if prev, dup := seen[op.Input.Value]; dup {
			t.Fatalf("value %q written by ops %d and %d", op.Input.Value, prev, op.ID)
		}
		seen[op.Input.Value] = op.ID
	}
}
