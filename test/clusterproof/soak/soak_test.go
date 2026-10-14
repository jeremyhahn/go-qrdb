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

package soak

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/history"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/nemesis"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/workload"
)

// opDelay paces the fake store so a run's history stays small.
const opDelay = 200 * time.Microsecond

// fakeKV is one linearizable register map every node serves. While lossy it
// acknowledges puts and deletes without applying them: a cluster that loses
// acknowledged writes.
type fakeKV struct {
	mu    sync.Mutex
	data  map[string]string
	lossy atomic.Bool
}

func newFakeKV() *fakeKV { return &fakeKV{data: make(map[string]string)} }

func (f *fakeKV) dial(context.Context, string) (workload.Conn, error) { return fakeConn{f}, nil }

type fakeConn struct{ f *fakeKV }

var _ workload.Conn = fakeConn{}

func (c fakeConn) Put(_ context.Context, key string, value []byte, _ ...transport.WriteOption) error {
	time.Sleep(opDelay)
	if c.f.lossy.Load() {
		return nil
	}
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	c.f.data[key] = string(value)
	return nil
}

func (c fakeConn) Get(_ context.Context, key string, _ ...transport.ReadOption) ([]byte, error) {
	time.Sleep(opDelay)
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	v, ok := c.f.data[key]
	if !ok {
		return nil, &transport.KeyNotFoundError{Key: key}
	}
	return []byte(v), nil
}

func (c fakeConn) Delete(_ context.Context, key string, _ ...transport.WriteOption) error {
	time.Sleep(opDelay)
	if c.f.lossy.Load() {
		return nil
	}
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	delete(c.f.data, key)
	return nil
}

func (c fakeConn) CompareAndSwap(_ context.Context, key string, expected, next []byte) (bool, error) {
	time.Sleep(opDelay)
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	cur, ok := c.f.data[key]
	if !ok || cur != string(expected) {
		return false, nil
	}
	c.f.data[key] = string(next)
	return true, nil
}

func (fakeConn) Close() error { return nil }

// fakeCluster has one led shard on every node.
type fakeCluster struct{ leader string }

func (c fakeCluster) Leader(context.Context) (string, error) { return c.leader, nil }

func (fakeCluster) Shards(context.Context, string) (map[uint64]uint64, error) {
	return map[uint64]uint64{1: 1}, nil
}

// fakeTarget is a node no command reaches.
type fakeTarget struct{ name string }

func (t fakeTarget) Name() string                                 { return t.name }
func (fakeTarget) Exec(context.Context, []string) ([]byte, error) { return nil, nemesis.ErrUnsupported }
func (fakeTarget) Signal(context.Context, syscall.Signal) error   { return nemesis.ErrUnsupported }

// fakeNemesis runs its hooks and counts its calls.
type fakeNemesis struct {
	inject func() error
	heal   func() error

	injects, heals atomic.Int32
}

var _ nemesis.Nemesis = (*fakeNemesis)(nil)

func (*fakeNemesis) Name() string { return "fake" }

func (n *fakeNemesis) Inject(context.Context, []nemesis.Target) error {
	n.injects.Add(1)
	if n.inject != nil {
		return n.inject()
	}
	return nil
}

func (n *fakeNemesis) Heal(context.Context) error {
	n.heals.Add(1)
	if n.heal != nil {
		return n.heal()
	}
	return nil
}

// fakeReplicas reports fixed replica states.
type fakeReplicas []ReplicaState

func (r fakeReplicas) ReplicaStates(context.Context) ([]ReplicaState, error) { return r, nil }

// agreeing is every replica of one shard at one state.
var agreeing = fakeReplicas{
	{Node: "n1", ShardID: 1, ReplicaID: 1, AppliedIndex: 9, Hash: 7},
	{Node: "n2", ShardID: 1, ReplicaID: 2, AppliedIndex: 9, Hash: 7},
	{Node: "n3", ShardID: 1, ReplicaID: 3, AppliedIndex: 9, Hash: 7},
}

// passChecker stands in for the linearizability checker and accepts every
// history.
type passChecker struct{}

func (passChecker) Check(h history.History) (history.Result, error) {
	return history.Result{Checker: "pass-checker", Valid: true, Checked: len(h)}, nil
}

// testOptions is a run over three fake nodes with every criterion
// observable. A fault, when given, is injected on n1 50ms into the run for
// 100ms.
func testOptions(t *testing.T, kv *fakeKV, fault *fakeNemesis) Options {
	t.Helper()
	nodes := []Node{{Name: "n1", Address: "a1"}, {Name: "n2", Address: "a2"}, {Name: "n3", Address: "a3"}}
	cfg := workload.Config{
		Endpoints:     []string{"a1", "a2", "a3"},
		Clients:       2,
		Keys:          4,
		KeyPrefix:     "test",
		Seed:          7,
		OpTimeout:     workload.Duration(time.Second),
		RedialBackoff: workload.Duration(time.Millisecond),
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	o := Options{
		Nodes:           nodes,
		Workload:        cfg,
		Seal:            "passphrase",
		Duration:        300 * time.Millisecond,
		Dial:            kv.dial,
		Cluster:         fakeCluster{leader: "n1"},
		Replicas:        agreeing,
		Checkers:        []history.Checker{passChecker{}},
		Targets:         map[string]nemesis.Target{},
		Schedule:        nemesis.Schedule{Seed: 42},
		HealTimeout:     time.Second,
		RecoveryTimeout: 2 * time.Second,
		Harness:         Harness{Version: "test", Commit: "test", Go: "test"},
		Bundle:          filepath.Join(t.TempDir(), "bundle"),
	}
	for _, n := range nodes {
		o.Targets[n.Name] = fakeTarget{n.Name}
	}
	if fault != nil {
		o.Nemeses = map[string]nemesis.Nemesis{fault.Name(): fault}
		s, err := nemesis.FixedSchedule(42, nemesis.Window{Nemesis: fault.Name(), Targets: []string{"n1"}, Start: 50 * time.Millisecond, Length: 100 * time.Millisecond})
		if err != nil {
			t.Fatalf("FixedSchedule: %v", err)
		}
		o.Schedule = s
	}
	return o
}

// run runs o and checks that its bundle verifies and reproduces the verdict.
func run(t *testing.T, o Options) Verdict {
	t.Helper()
	res, err := Run(context.Background(), o)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Bundle != o.Bundle {
		t.Fatalf("bundle = %q, want %q", res.Bundle, o.Bundle)
	}
	chk, err := Check(o.Bundle, CheckOptions{Checkers: o.Checkers})
	if err != nil {
		t.Fatalf("Check of the run's own bundle: %v", err)
	}
	if chk.ManifestSHA256 != res.ManifestSHA256 {
		t.Fatalf("check manifest digest %s, run printed %s", chk.ManifestSHA256, res.ManifestSHA256)
	}
	if chk.Verdict.Exit != res.Verdict.Exit || chk.Verdict.Outcome != res.Verdict.Outcome {
		t.Fatalf("check verdict %s/%d, run verdict %s/%d", chk.Verdict.Outcome, chk.Verdict.Exit, res.Verdict.Outcome, res.Verdict.Exit)
	}
	return res.Verdict
}

func criterion(t *testing.T, v Verdict, id string) Criterion {
	t.Helper()
	for _, c := range v.Criteria {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("verdict has no %s: %+v", id, v.Criteria)
	return Criterion{}
}

func hasReason(v Verdict, substr string) bool {
	return slices.ContainsFunc(v.Reasons, func(r string) bool { return strings.Contains(r, substr) })
}

func TestRunCleanIsProven(t *testing.T) {
	fault := &fakeNemesis{}
	v := run(t, testOptions(t, newFakeKV(), fault))
	if v.Exit != ExitProven || v.Outcome != OutcomeProven || len(v.Reasons) != 0 {
		t.Fatalf("clean run: %s exit %d, reasons %q", v.Outcome, v.Exit, v.Reasons)
	}
	if v.Seal != "passphrase" {
		t.Fatalf("verdict seal %q, want the run's passphrase", v.Seal)
	}
	for _, id := range []string{"E1", "E2", "E3", "E4"} {
		if c := criterion(t, v, id); c.Status != StatusPass {
			t.Errorf("%s = %s (%s), want pass", id, c.Status, c.Detail)
		}
	}
	for _, id := range []string{"E5", "E6", "E7"} {
		if c := criterion(t, v, id); c.Status != StatusMeasured {
			t.Errorf("%s = %s, want measured", id, c.Status)
		}
	}
	if fault.injects.Load() != 1 {
		t.Fatalf("injected %d times, want 1", fault.injects.Load())
	}
}

func TestRunLostWriteIsViolated(t *testing.T) {
	kv := newFakeKV()
	// From the inject on, the cluster acknowledges writes it never applies,
	// and the heal does not bring them back.
	fault := &fakeNemesis{inject: func() error { kv.lossy.Store(true); return nil }}
	v := run(t, testOptions(t, kv, fault))
	if v.Exit != ExitViolated || v.Outcome != OutcomeViolated {
		t.Fatalf("lossy run: %s exit %d, reasons %q", v.Outcome, v.Exit, v.Reasons)
	}
	if c := criterion(t, v, "E1"); c.Status != StatusFail || !strings.Contains(c.Detail, "lost") {
		t.Fatalf("E1 = %s (%s), want a lost-write failure", c.Status, c.Detail)
	}
}

func TestRunUnhealedFaultIsInvalid(t *testing.T) {
	fault := &fakeNemesis{heal: func() error { return errors.New("iptables: permission denied") }}
	v := run(t, testOptions(t, newFakeKV(), fault))
	if v.Exit != ExitInvalid || v.Outcome != OutcomeInvalid {
		t.Fatalf("unhealed run: %s exit %d, reasons %q", v.Outcome, v.Exit, v.Reasons)
	}
	if !hasReason(v, "a fault did not heal: fake: iptables: permission denied") {
		t.Fatalf("reasons %q do not name the unhealed fault", v.Reasons)
	}
	if c := criterion(t, v, "E4"); c.Status != StatusUnavailable {
		t.Fatalf("E4 = %s, want unavailable while a fault is still in place", c.Status)
	}
	// The window's heal and the final heal both ran.
	if fault.heals.Load() < 2 {
		t.Fatalf("healed %d times, want the window's heal and the final one", fault.heals.Load())
	}
}

func TestRunFailedInjectIsInvalid(t *testing.T) {
	fault := &fakeNemesis{inject: func() error { return errors.New("no such container") }}
	v := run(t, testOptions(t, newFakeKV(), fault))
	if v.Exit != ExitInvalid {
		t.Fatalf("failed inject: %s exit %d", v.Outcome, v.Exit)
	}
	for _, want := range []string{"nemesis inject of fake failed: no such container", "the nemesis never injected a fault"} {
		if !hasReason(v, want) {
			t.Errorf("reasons %q lack %q", v.Reasons, want)
		}
	}
}

func TestRunWithoutNemesisIsInvalid(t *testing.T) {
	v := run(t, testOptions(t, newFakeKV(), nil))
	if v.Exit != ExitInvalid || !hasReason(v, "no nemesis configured") {
		t.Fatalf("run without nemesis: %s exit %d, reasons %q", v.Outcome, v.Exit, v.Reasons)
	}
	// The cluster itself was fine: nothing was violated.
	if c := criterion(t, v, "E1"); c.Status != StatusPass {
		t.Fatalf("E1 = %s (%s)", c.Status, c.Detail)
	}
}

// TestUnknownSealIsInvalid covers the reason cluster-proof-check re-derives
// from a run.json whose seal type the release does not ship.
func TestUnknownSealIsInvalid(t *testing.T) {
	for seal, want := range map[string]bool{"none": false, "passphrase": false, "shamir": false, "tpm2": true, "": true} {
		reasons := runReasons(evidence{run: RunRecord{Seal: seal}})
		got := slices.ContainsFunc(reasons, func(r string) bool { return strings.Contains(r, "names no seal type the release ships") })
		if got != want {
			t.Errorf("runReasons under seal %q = %q, want the unknown-seal reason %v", seal, reasons, want)
		}
	}
}

func TestRunWithoutSeamsReportsUnavailable(t *testing.T) {
	o := testOptions(t, newFakeKV(), &fakeNemesis{})
	o.Checkers, o.Replicas = nil, nil
	v := run(t, o)
	if v.Exit != ExitInvalid {
		t.Fatalf("run without a checker or replica states: %s exit %d", v.Outcome, v.Exit)
	}
	if c := criterion(t, v, "E2"); c.Status != StatusUnavailable || c.Detail != "no linearizability checker configured" {
		t.Errorf("E2 = %s (%s)", c.Status, c.Detail)
	}
	if c := criterion(t, v, "E3"); c.Status != StatusUnavailable || c.Detail != replicaStatesUnavailable {
		t.Errorf("E3 = %s (%s)", c.Status, c.Detail)
	}
}

// resultChecker returns a fixed result for every history.
type resultChecker history.Result

func (c resultChecker) Check(history.History) (history.Result, error) {
	return history.Result(c), nil
}

func TestJudgeHistoryE2(t *testing.T) {
	h := history.History{{
		ID: 1, Kind: history.KindPut, Key: "k", Input: &history.Input{Value: "v"},
		CallNS: 1, ReturnNS: 2, Outcome: history.OutcomeOK,
	}}
	anomaly := []history.Anomaly{{Kind: "some-kind", Detail: "d"}}
	tests := []struct {
		name     string
		checkers []history.Checker
		want     Status
	}{
		{"valid", []history.Checker{resultChecker{Checker: "c", Valid: true, Checked: 1}}, StatusPass},
		{"anomaly over checked operations", []history.Checker{resultChecker{Checker: "c", Checked: 1, Anomalies: anomaly}}, StatusFail},
		// A checker that examined nothing -- a vacuous history -- could not
		// decide; it must never read as a violation.
		{"nothing examined", []history.Checker{resultChecker{Checker: "c", Anomalies: anomaly}}, StatusUnavailable},
		{"a violation outranks an undecided checker", []history.Checker{
			resultChecker{Checker: "a", Anomalies: anomaly},
			resultChecker{Checker: "b", Checked: 1, Anomalies: anomaly},
		}, StatusFail},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, e2, _ := judgeHistory(h, tt.checkers)
			if e2.Status != tt.want {
				t.Errorf("E2 = %s (%s), want %s", e2.Status, e2.Detail, tt.want)
			}
		})
	}
}

func TestRunInterruptedIsInvalid(t *testing.T) {
	o := testOptions(t, newFakeKV(), &fakeNemesis{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := Run(ctx, o)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Verdict.Exit != ExitInvalid || !hasReason(res.Verdict, "the run was interrupted") {
		t.Fatalf("interrupted run: %s exit %d, reasons %q", res.Verdict.Outcome, res.Verdict.Exit, res.Verdict.Reasons)
	}
}

func TestRunRefusesInvalidOptions(t *testing.T) {
	cases := map[string]func(*Options){
		"no nodes":           func(o *Options) { o.Nodes = nil },
		"bad node name":      func(o *Options) { o.Nodes[0].Name = "../x" },
		"duplicate node":     func(o *Options) { o.Nodes[1].Name = "n1" },
		"endpoints mismatch": func(o *Options) { o.Workload.Endpoints = []string{"a3", "a2", "a1"} },
		"no dial":            func(o *Options) { o.Dial = nil },
		"no cluster":         func(o *Options) { o.Cluster = nil },
		"no bundle":          func(o *Options) { o.Bundle = "" },
		"no seal":            func(o *Options) { o.Seal = "" },
		"unknown seal":       func(o *Options) { o.Seal = "tpm2" },
		"fault without window": func(o *Options) {
			o.Schedule = nemesis.Schedule{Seed: 1}
		},
		"window without fault": func(o *Options) { o.Nemeses = nil },
		"window past the run":  func(o *Options) { o.Duration = 120 * time.Millisecond },
		"unknown target":       func(o *Options) { delete(o.Targets, "n1") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			o := testOptions(t, newFakeKV(), &fakeNemesis{})
			mutate(&o)
			if _, err := Run(context.Background(), o); err == nil {
				t.Fatal("Run accepted invalid options")
			}
		})
	}
}

func TestCompareReplicas(t *testing.T) {
	cases := []struct {
		name   string
		states []ReplicaState
		want   Status
	}{
		{"none", nil, StatusUnavailable},
		{"agree", agreeing, StatusPass},
		{"diverge", []ReplicaState{{Node: "n1", ShardID: 1, AppliedIndex: 9, Hash: 7}, {Node: "n2", ShardID: 1, AppliedIndex: 9, Hash: 8}}, StatusFail},
		{"not quiesced", []ReplicaState{{Node: "n1", ShardID: 1, AppliedIndex: 9, Hash: 7}, {Node: "n2", ShardID: 1, AppliedIndex: 8, Hash: 6}}, StatusUnavailable},
		{"divergence wins over lag", []ReplicaState{
			{Node: "n1", ShardID: 1, AppliedIndex: 9, Hash: 7}, {Node: "n2", ShardID: 1, AppliedIndex: 8, Hash: 6},
			{Node: "n1", ShardID: 2, AppliedIndex: 4, Hash: 1}, {Node: "n2", ShardID: 2, AppliedIndex: 4, Hash: 2},
		}, StatusFail},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := compareReplicas(c.states); got.Status != c.want {
				t.Fatalf("compareReplicas = %s (%s), want %s", got.Status, got.Detail, c.want)
			}
		})
	}
}

func TestBuildTimelineOrdersOneClock(t *testing.T) {
	h := history.History{
		{ID: 2, Client: 1, Kind: history.KindGet, Key: "k", CallNS: 10, ReturnNS: 40, Outcome: history.OutcomeOK},
		{ID: 1, Client: 0, Kind: history.KindPut, Key: "k", CallNS: 10, ReturnNS: 20, Outcome: history.OutcomeOK},
	}
	events := []EventRecord{{TimeNS: 10, Event: nemesis.Event{Op: nemesis.OpInject, Phase: nemesis.PhaseInvoke, Nemesis: "kill"}}}
	tl := buildTimeline(h, events)
	var got []string
	for _, e := range tl {
		s := e.Source + "/" + e.Type
		if e.Op != nil {
			s += "/" + strings.Repeat("#", int(e.Op.ID))
		}
		got = append(got, s)
	}
	want := []string{"nemesis/inject", "client/invoke/#", "client/invoke/##", "client/complete/#", "client/complete/##"}
	if !slices.Equal(got, want) {
		t.Fatalf("timeline %q, want %q", got, want)
	}
}
