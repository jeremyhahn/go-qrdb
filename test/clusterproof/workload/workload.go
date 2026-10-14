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

// Package workload drives concurrent key-value clients against a cluster and
// records every operation into a history.History.
//
// Each client has an id, its own connection and its own seeded random stream.
// It picks puts, gets, deletes and compare-and-swaps over a small shared key
// space by the configured Mix, and writes values unique across the run
// ("c<client>-<seq>"), so a value read names exactly one write. Every call and
// return is stamped from the recorder's one clock.
//
// The workload survives node loss. An operation that times out or loses its
// connection is recorded and the client carries on: a write whose answer
// never came is indeterminate, never ok and never fail (see Classifier). A
// client whose connection drops dials again, moving to the next endpoint when
// its own will not answer, so a dead node slows the run without stopping it.
//
// After the load phase, FinalRead reads every key once more, so the ledger can
// check each key's acknowledged writes against what the cluster now holds.
package workload

import (
	"context"
	"math/rand/v2"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/test/clusterproof/history"
)

// Workload is one configured run. Run it once, then call FinalRead, then take
// the history from the recorder.
type Workload struct {
	cfg      Config
	dial     Dialer
	rec      *history.Recorder
	classify Classifier
	keys     []string
	kinds    []weightedKind

	outcomes     map[history.Outcome]*atomic.Uint64
	dialFailures atomic.Uint64
}

// Option adjusts a Workload.
type Option func(*Workload)

// WithClassifier replaces DefaultClassifier.
func WithClassifier(c Classifier) Option {
	return func(w *Workload) { w.classify = c }
}

// Stats counts what a run recorded.
type Stats struct {
	OK            uint64
	Fail          uint64
	Indeterminate uint64
	DialFailures  uint64
}

// Ops is the number of operations recorded.
func (s Stats) Ops() uint64 { return s.OK + s.Fail + s.Indeterminate }

// weightedKind is one operation kind and its cumulative weight.
type weightedKind struct {
	kind history.Kind
	cum  int
}

// New returns a Workload over cfg, which must already be normalized.
func New(cfg Config, dial Dialer, rec *history.Recorder, opts ...Option) (*Workload, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if dial == nil {
		return nil, &ConfigError{Field: "dialer", Reason: "must not be nil"}
	}
	if rec == nil {
		return nil, &ConfigError{Field: "recorder", Reason: "must not be nil"}
	}
	w := &Workload{
		cfg:      cfg,
		dial:     dial,
		rec:      rec,
		classify: DefaultClassifier,
		keys:     make([]string, cfg.Keys),
		outcomes: map[history.Outcome]*atomic.Uint64{
			history.OutcomeOK:            new(atomic.Uint64),
			history.OutcomeFail:          new(atomic.Uint64),
			history.OutcomeIndeterminate: new(atomic.Uint64),
		},
	}
	for _, o := range opts {
		o(w)
	}
	if w.classify == nil {
		return nil, &ConfigError{Field: "classifier", Reason: "must not be nil"}
	}
	for i := range w.keys {
		w.keys[i] = cfg.KeyPrefix + "/k" + strconv.Itoa(i)
	}
	cum := 0
	for _, kw := range []struct {
		kind   history.Kind
		weight int
	}{
		{history.KindPut, cfg.Mix.Put},
		{history.KindGet, cfg.Mix.Get},
		{history.KindDelete, cfg.Mix.Delete},
		{history.KindCAS, cfg.Mix.CAS},
	} {
		if kw.weight > 0 {
			cum += kw.weight
			w.kinds = append(w.kinds, weightedKind{kind: kw.kind, cum: cum})
		}
	}
	return w, nil
}

// Keys returns the keys the run contends on.
func (w *Workload) Keys() []string { return append([]string(nil), w.keys...) }

// Stats returns the counts recorded so far.
func (w *Workload) Stats() Stats {
	return Stats{
		OK:            w.outcomes[history.OutcomeOK].Load(),
		Fail:          w.outcomes[history.OutcomeFail].Load(),
		Indeterminate: w.outcomes[history.OutcomeIndeterminate].Load(),
		DialFailures:  w.dialFailures.Load(),
	}
}

// Run drives cfg.Clients clients until ctx ends, cfg.Duration passes, or each
// has issued cfg.OpsPerClient operations, and returns once every client has
// stopped. Ending ctx stops clients from starting operations; one already in
// flight runs to its own timeout, so its record says what really happened.
func (w *Workload) Run(ctx context.Context) Stats {
	if d := time.Duration(w.cfg.Duration); d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	var wg sync.WaitGroup
	for id := range w.cfg.Clients {
		c := w.newClient(id)
		wg.Go(func() { c.run(ctx) })
	}
	wg.Wait()
	return w.Stats()
}

// FinalRead reads every key through a verifier client whose id follows the
// load clients'. Each key is retried round-robin over the endpoints until one
// answers or ctx ends; every attempt is recorded. Call it after Run returns,
// so each read is called after every load operation returned. A key no
// endpoint answered is reported in a FinalReadError.
func (w *Workload) FinalRead(ctx context.Context) error {
	c := w.newClient(w.cfg.Clients)
	defer c.drop()
	var unread []string
	for _, key := range w.keys {
		if !c.readUntilAnswered(ctx, key) {
			unread = append(unread, key)
		}
	}
	if len(unread) > 0 {
		return &FinalReadError{Keys: unread}
	}
	return nil
}

// observed is the last state of a key a client saw.
type observed struct {
	value   string
	present bool
}

// client is one goroutine's view of the run.
type client struct {
	w        *Workload
	id       int
	log      *history.Log
	rng      *rand.Rand
	endpoint int
	conn     Conn
	seq      uint64
	known    map[string]observed
}

func (w *Workload) newClient(id int) *client {
	return &client{
		w:        w,
		id:       id,
		log:      w.rec.Log(id),
		rng:      rand.New(rand.NewPCG(w.cfg.Seed, uint64(id))),
		endpoint: id % len(w.cfg.Endpoints),
		known:    make(map[string]observed),
	}
}

// ops maps each kind to the client method that performs it.
var ops = map[history.Kind]func(*client, context.Context, string){
	history.KindPut:    (*client).put,
	history.KindGet:    (*client).get,
	history.KindDelete: (*client).del,
	history.KindCAS:    (*client).cas,
}

func (c *client) run(ctx context.Context) {
	defer c.drop()
	limit := c.w.cfg.OpsPerClient
	for n := 0; limit == 0 || n < limit; {
		if ctx.Err() != nil {
			return
		}
		if !c.connect(ctx) {
			continue
		}
		key := c.w.keys[c.rng.IntN(len(c.w.keys))]
		ops[c.pick()](c, ctx, key)
		n++
	}
}

// pick draws an operation kind by the configured weights.
func (c *client) pick() history.Kind {
	kinds := c.w.kinds
	r := c.rng.IntN(kinds[len(kinds)-1].cum)
	for _, k := range kinds {
		if r < k.cum {
			return k.kind
		}
	}
	return kinds[len(kinds)-1].kind
}

// connect ensures the client holds a connection. On a failed dial it moves
// to the next endpoint and backs off, and reports false.
func (c *client) connect(ctx context.Context) bool {
	if c.conn != nil {
		return true
	}
	dctx, cancel := context.WithTimeout(ctx, time.Duration(c.w.cfg.OpTimeout))
	conn, err := c.w.dial(dctx, c.w.cfg.Endpoints[c.endpoint])
	cancel()
	if err == nil {
		c.conn = conn
		return true
	}
	c.w.dialFailures.Add(1)
	c.endpoint = (c.endpoint + 1) % len(c.w.cfg.Endpoints)
	c.backoff(ctx)
	return false
}

// drop closes the client's connection, if any.
func (c *client) drop() {
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}

// opContext bounds one operation by OpTimeout but not by ctx's cancellation:
// stopping the run must not turn an operation in flight into a timeout.
func (c *client) opContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), time.Duration(c.w.cfg.OpTimeout))
}

// nextValue returns a value no other operation in the run writes.
func (c *client) nextValue() string {
	c.seq++
	return "c" + strconv.Itoa(c.id) + "-" + strconv.FormatUint(c.seq, 10)
}

// finish records p's return and counts it, dropping the connection if err
// says it is gone. An error the classifier calls ok -- KeyNotFound on a read
// -- is an answer, not a failure, and is not kept as error text.
func (c *client) finish(p history.Pending, kind history.Kind, out *history.Output, err error) history.Outcome {
	outcome := c.w.classify(kind, err)
	recorded := err
	if outcome == history.OutcomeOK {
		recorded = nil
	}
	c.log.Return(p, outcome, out, recorded)
	c.w.outcomes[outcome].Add(1)
	if connectionLost(err) {
		c.drop()
	}
	return outcome
}

func (c *client) put(ctx context.Context, key string) {
	v := c.nextValue()
	octx, cancel := c.opContext(ctx)
	defer cancel()
	p := c.log.Call(history.KindPut, key, &history.Input{Value: v})
	err := c.conn.Put(octx, key, []byte(v))
	if c.finish(p, history.KindPut, nil, err) == history.OutcomeOK {
		c.known[key] = observed{value: v, present: true}
		return
	}
	delete(c.known, key)
}

func (c *client) get(ctx context.Context, key string) {
	octx, cancel := c.opContext(ctx)
	defer cancel()
	c.read(octx, key)
}

// read performs one recorded get and reports whether it was answered.
func (c *client) read(ctx context.Context, key string) bool {
	p := c.log.Call(history.KindGet, key, nil)
	b, err := c.conn.Get(ctx, key)
	out := &history.Output{Found: err == nil, Value: string(b)}
	if c.finish(p, history.KindGet, out, err) != history.OutcomeOK {
		delete(c.known, key)
		return false
	}
	c.known[key] = observed{value: out.Value, present: out.Found}
	return true
}

func (c *client) del(ctx context.Context, key string) {
	octx, cancel := c.opContext(ctx)
	defer cancel()
	p := c.log.Call(history.KindDelete, key, nil)
	err := c.conn.Delete(octx, key)
	if c.finish(p, history.KindDelete, nil, err) == history.OutcomeOK {
		c.known[key] = observed{}
		return
	}
	delete(c.known, key)
}

// cas swaps the key from the value this client last saw to a fresh one. An
// absent key stays absent under compare-and-swap, so with no present value to
// expect the client reads the key instead, and learns one.
func (c *client) cas(ctx context.Context, key string) {
	exp, ok := c.known[key]
	if !ok || !exp.present {
		c.get(ctx, key)
		return
	}
	v := c.nextValue()
	octx, cancel := c.opContext(ctx)
	defer cancel()
	p := c.log.Call(history.KindCAS, key, &history.Input{Value: v, Expected: exp.value})
	swapped, err := c.conn.CompareAndSwap(octx, key, []byte(exp.value), []byte(v))
	if c.finish(p, history.KindCAS, &history.Output{Swapped: swapped}, err) == history.OutcomeOK && swapped {
		c.known[key] = observed{value: v, present: true}
		return
	}
	delete(c.known, key)
}

// readUntilAnswered reads key, trying each endpoint in turn, until a read is
// answered or ctx ends.
func (c *client) readUntilAnswered(ctx context.Context, key string) bool {
	for ctx.Err() == nil {
		if !c.connect(ctx) {
			continue
		}
		octx, cancel := c.opContext(ctx)
		answered := c.read(octx, key)
		cancel()
		if answered {
			return true
		}
		c.drop()
		c.endpoint = (c.endpoint + 1) % len(c.w.cfg.Endpoints)
		c.backoff(ctx)
	}
	return false
}

// backoff waits RedialBackoff or until ctx ends.
func (c *client) backoff(ctx context.Context) {
	t := time.NewTimer(time.Duration(c.w.cfg.RedialBackoff))
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
