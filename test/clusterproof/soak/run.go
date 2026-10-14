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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/test/clusterproof/history"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/nemesis"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/workload"
)

// Defaults of a run.
const (
	DefaultHealTimeout      = time.Minute
	DefaultRecoveryTimeout  = 2 * time.Minute
	DefaultProgressInterval = 10 * time.Second
	// livenessPoll is the pause between two E4 rounds.
	livenessPoll = 500 * time.Millisecond
	// replicaPoll is the pause between two E3 reads while replicas catch up.
	replicaPoll = 200 * time.Millisecond
)

// Options is one soak run.
type Options struct {
	// Nodes are the nodes under test.
	Nodes []Node
	// Workload configures the load. Its endpoints are the nodes' addresses
	// in order; its duration is replaced by Duration.
	Workload workload.Config
	// Duration is how long the workload and the nemesis run.
	Duration time.Duration
	// Dial reaches a node for the workload and for E4's probe writes.
	Dial workload.Dialer
	// Cluster answers who leads and which shards are led.
	Cluster Cluster
	// Inspector reads each node's version and configuration digest; nil
	// records both as not available.
	Inspector Inspector
	// Replicas is the E3 seam; nil reports E3 unavailable.
	Replicas ReplicaStates
	// Checkers run beside the ledger. None reports E2 unavailable: the
	// linearizability checker is pending an owner decision.
	Checkers []history.Checker

	// Nemeses are the configured faults by name; empty is a run with the
	// nemesis off, which proves nothing about a fault.
	Nemeses map[string]nemesis.Nemesis
	// Targets are the faults' targets by node name.
	Targets map[string]nemesis.Target
	// Schedule is the schedule to play. It must have a window when a fault
	// is configured, and none when none is.
	Schedule nemesis.Schedule
	// HealTimeout bounds each heal; RecoveryTimeout bounds E4's wait for
	// the cluster after the last heal, and the final read.
	HealTimeout     time.Duration
	RecoveryTimeout time.Duration

	// Seal is the node seal type the cluster runs under, one of sealTypes.
	Seal string
	// Declared is what the operator states about the cluster.
	Declared Declared
	// Harness identifies this build in the manifest.
	Harness Harness
	// Bundle is the evidence directory; it must not exist yet.
	Bundle string
	// Progress receives one line per ProgressInterval; nil is silent.
	Progress         io.Writer
	ProgressInterval time.Duration
}

// Result is a completed run.
type Result struct {
	Verdict        Verdict
	Bundle         string
	ManifestSHA256 string
}

// Run plays the soak: the workload for Duration while the nemesis plays the
// schedule, then every fault healed, the cluster's recovery (E4), the final
// read, the replica comparison (E3), and the evidence bundle. The verdict is
// derived from the bundle as written, so a check of the bundle reaches the
// same one. An error means no bundle supports a verdict.
func Run(ctx context.Context, o Options) (Result, error) {
	o.defaults()
	if err := o.validate(); err != nil {
		return Result{}, err
	}
	origin := time.Now()
	rec := history.NewRecorder(runClock{origin: origin})
	wcfg := o.Workload
	wcfg.Duration = workload.Duration(o.Duration)
	w, err := workload.New(wcfg, o.Dial, rec)
	if err != nil {
		return Result{}, err
	}
	bw, err := createBundle(o.Bundle)
	if err != nil {
		return Result{}, err
	}

	recorded := wcfg
	recorded.Credentials = workload.Credentials{}
	run := RunRecord{
		Format:   bundleFormat,
		Started:  origin.UTC().Format(time.RFC3339Nano),
		Nodes:    o.Nodes,
		Duration: o.Duration.String(),
		Nemesis:  slices.Sorted(maps.Keys(o.Nemeses)),
		Seed:     o.Schedule.Seed,
		Schedule: o.Schedule,
		Workload: recorded,
		Seal:     o.Seal,
		Declared: o.Declared,
	}
	infos := o.inspect(ctx)

	events := &eventLog{origin: origin}
	player := &nemesis.Player{Nemeses: o.Nemeses, Targets: o.Targets, Recorder: events, HealTimeout: o.HealTimeout}
	loadCtx, stopLoad := context.WithCancel(ctx)
	defer stopLoad()
	played := make(chan error, 1)
	go func() {
		err := player.Play(ctx, o.Schedule)
		if err != nil {
			// A failed fault invalidates the run; the load has nothing left
			// to prove.
			stopLoad()
		}
		played <- err
	}()
	stopProgress := o.startProgress(w, events, origin)
	stats := w.Run(loadCtx)
	playErr := <-played
	stopProgress()

	var status RunStatus
	if playErr != nil && ctx.Err() == nil && events.failures() == 0 {
		status.Errors = append(status.Errors, "nemesis: "+playErr.Error())
	}
	status.Unhealed = o.healAll(ctx)
	healedAt := time.Now()

	switch {
	case ctx.Err() != nil:
		run.Liveness = Observation{Status: StatusUnavailable, Detail: "not checked: the run was interrupted"}
	case len(status.Unhealed) > 0:
		run.Liveness = Observation{Status: StatusUnavailable, Detail: "not checked: a fault did not heal"}
	default:
		run.Liveness = o.liveness(ctx, wcfg.KeyPrefix, healedAt)
	}
	if ctx.Err() == nil {
		fctx, cancel := context.WithTimeout(ctx, o.RecoveryTimeout)
		if err := w.FinalRead(fctx); err != nil && ctx.Err() == nil {
			status.Errors = append(status.Errors, "final read: "+err.Error())
		}
		cancel()
	}
	run.Replicas = o.replicas(ctx)
	status.Interrupted = ctx.Err() != nil
	run.Status = status
	run.Stats = WorkloadStats{OK: stats.OK, Fail: stats.Fail, Indeterminate: stats.Indeterminate, DialFailures: stats.DialFailures}
	run.Ended = time.Now().UTC().Format(time.RFC3339Nano)

	return o.writeBundle(bw, rec.History(), events.snapshot(), run, infos)
}

// writeBundle writes the evidence, decodes it again, derives the verdict from
// what was decoded and writes the derived files and the manifest. Judging the
// written bytes rather than the run's memory is what lets a check of the
// bundle reproduce the verdict exactly.
func (o *Options) writeBundle(bw *bundleWriter, h history.History, events []EventRecord, run RunRecord, infos []NodeInfo) (Result, error) {
	res := Result{Bundle: o.Bundle}
	historyData, err := encodeHistory(h)
	if err != nil {
		return res, &BundleError{Path: o.Bundle, Reason: "cannot encode " + fileHistory, Err: err}
	}
	eventData, err := encodeLines(events)
	if err != nil {
		return res, &BundleError{Path: o.Bundle, Reason: "cannot encode " + fileNemesis, Err: err}
	}
	runData, err := encodeJSON(run)
	if err != nil {
		return res, &BundleError{Path: o.Bundle, Reason: "cannot encode " + fileRun, Err: err}
	}
	files := []struct {
		rel  string
		data []byte
	}{{fileHistory, historyData}, {fileNemesis, eventData}, {fileRun, runData}}
	for _, info := range infos {
		data, err := encodeJSON(info)
		if err != nil {
			return res, &BundleError{Path: o.Bundle, Reason: "cannot encode " + nodeFile(info.Name), Err: err}
		}
		files = append(files, struct {
			rel  string
			data []byte
		}{nodeFile(info.Name), data})
	}
	for _, f := range files {
		if err := bw.write(f.rel, f.data); err != nil {
			return res, err
		}
	}

	ev, err := decodeEvidence(o.Bundle, historyData, eventData, runData)
	if err != nil {
		return res, err
	}
	ev.checkers = o.Checkers
	v, d, err := derive(ev)
	if err != nil {
		return res, &BundleError{Path: o.Bundle, Reason: "cannot derive the verdict", Err: err}
	}
	for _, f := range []struct {
		rel  string
		data []byte
	}{{fileTimeline, d.timeline}, {fileTimings, d.timings}, {fileVerdict, d.verdict}} {
		if err := bw.write(f.rel, f.data); err != nil {
			return res, err
		}
	}
	sha, err := bw.finish(Manifest{Format: bundleFormat, Created: run.Ended, Harness: o.Harness})
	if err != nil {
		return res, err
	}
	res.Verdict, res.ManifestSHA256 = v, sha
	return res, nil
}

// decodeEvidence reads the recorded evidence back from its bytes, as a check
// does.
func decodeEvidence(dir string, historyData, eventData, runData []byte) (evidence, error) {
	var ev evidence
	h, err := history.Decode(bytes.NewReader(historyData))
	if err != nil {
		return ev, &BundleError{Path: dir, Reason: "malformed " + fileHistory, Err: err}
	}
	events, err := decodeEvents(dir, eventData)
	if err != nil {
		return ev, err
	}
	var run RunRecord
	if err := decodeStrict(runData, &run); err != nil {
		return ev, &BundleError{Path: dir, Reason: "malformed " + fileRun, Err: err}
	}
	if run.Format != bundleFormat {
		return ev, &BundleError{Path: dir, Reason: fileRun + " has unsupported format " + strconv.Itoa(run.Format)}
	}
	return evidence{history: h, events: events, run: run, timings: computeTimings(h, events, run.Workload.Clients)}, nil
}

// defaults fills the zero fields that have a default.
func (o *Options) defaults() {
	if o.HealTimeout <= 0 {
		o.HealTimeout = DefaultHealTimeout
	}
	if o.RecoveryTimeout <= 0 {
		o.RecoveryTimeout = DefaultRecoveryTimeout
	}
	if o.ProgressInterval <= 0 {
		o.ProgressInterval = DefaultProgressInterval
	}
	if o.Declared.ReadMode == "" {
		o.Declared.ReadMode = undeclared
	}
}

// validate refuses a run that could not support a verdict, before it touches
// the cluster.
func (o *Options) validate() error {
	switch {
	case len(o.Nodes) == 0:
		return &ConfigError{Field: "nodes", Reason: "at least one node is required"}
	case o.Duration <= 0:
		return &ConfigError{Field: "duration", Reason: "must be positive"}
	case o.Dial == nil:
		return &ConfigError{Field: "dial", Reason: "is required"}
	case o.Cluster == nil:
		return &ConfigError{Field: "cluster", Reason: "is required"}
	case o.Bundle == "":
		return &ConfigError{Field: "bundle", Reason: "is required"}
	case !knownSeal(o.Seal):
		return &ConfigError{Field: "seal", Reason: "must be a seal type the release ships, not " + strconv.Quote(o.Seal)}
	}
	seen := make(map[string]bool, len(o.Nodes))
	addresses := make([]string, 0, len(o.Nodes))
	for _, n := range o.Nodes {
		switch {
		case !validNodeName(n.Name):
			return &ConfigError{Field: "nodes", Reason: "invalid node name " + strconv.Quote(n.Name) + ": letters, digits, '.', '_' and '-', starting with a letter or digit"}
		case seen[n.Name]:
			return &ConfigError{Field: "nodes", Reason: "node " + n.Name + " named twice"}
		case n.Address == "":
			return &ConfigError{Field: "nodes", Reason: "node " + n.Name + " has no address"}
		}
		seen[n.Name] = true
		addresses = append(addresses, n.Address)
	}
	if !slices.Equal(o.Workload.Endpoints, addresses) {
		return &ConfigError{Field: "workload endpoints", Reason: "must be the node addresses, in node order"}
	}
	if err := o.Workload.Validate(); err != nil {
		return err
	}
	return o.validateSchedule(seen)
}

// validateSchedule checks the schedule against the configured faults, the
// targets and the run's length.
func (o *Options) validateSchedule(nodes map[string]bool) error {
	s := o.Schedule
	switch {
	case len(o.Nemeses) == 0 && len(s.Windows) > 0:
		return &ConfigError{Field: "schedule", Reason: "has fault windows but no fault is configured"}
	case len(o.Nemeses) > 0 && len(s.Windows) == 0:
		return &ConfigError{Field: "schedule", Reason: "has no fault window: a run with faults configured and none injected proves nothing; lengthen the duration or shorten the schedule's gaps and lengths"}
	}
	if err := s.Validate(); err != nil {
		return err
	}
	for i, w := range s.Windows {
		field := "schedule window " + strconv.Itoa(i)
		if _, ok := o.Nemeses[w.Nemesis]; !ok {
			return &ConfigError{Field: field, Reason: "names fault " + strconv.Quote(w.Nemesis) + ", which is not configured"}
		}
		for _, t := range w.Targets {
			if _, ok := o.Targets[t]; !ok || !nodes[t] {
				return &ConfigError{Field: field, Reason: "targets " + strconv.Quote(t) + ", which is not a node with a target"}
			}
		}
		if w.Start+w.Length > o.Duration {
			return &ConfigError{Field: field, Reason: "ends after the run"}
		}
	}
	return nil
}

// inspect reads every node's identity for the bundle. A node that cannot be
// read is recorded as such; it does not stop the run.
func (o *Options) inspect(ctx context.Context) []NodeInfo {
	infos := make([]NodeInfo, 0, len(o.Nodes))
	for _, n := range o.Nodes {
		info := NodeInfo{Version: notAvailable, ConfigSHA256: notAvailable}
		if o.Inspector != nil {
			got, err := o.Inspector.Inspect(ctx, n.Name)
			info = got
			if err != nil {
				info.Errors = append(info.Errors, err.Error())
			}
		}
		info.Name, info.Address = n.Name, n.Address
		infos = append(infos, info)
	}
	return infos
}

// healAll heals every configured fault once more after the schedule, on a
// context the run's cancellation does not reach. Each heal is idempotent, so
// after a clean schedule this does nothing; after a failed or interrupted one
// it is the last chance to leave the cluster as it was found.
func (o *Options) healAll(ctx context.Context) []string {
	var unhealed []string
	for _, name := range slices.Sorted(maps.Keys(o.Nemeses)) {
		hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), o.HealTimeout)
		err := o.Nemeses[name].Heal(hctx)
		cancel()
		if err != nil {
			unhealed = append(unhealed, name+": "+err.Error())
		}
	}
	return unhealed
}

// liveness is E4: every node reports every shard it hosts led, and takes a
// write, within RecoveryTimeout of the last heal. A shard still unled then is
// a permanently halted shard.
func (o *Options) liveness(ctx context.Context, prefix string, healedAt time.Time) Observation {
	lctx, cancel := context.WithTimeout(ctx, o.RecoveryTimeout)
	defer cancel()
	for {
		why, sharded, err := o.livenessRound(lctx, prefix)
		if err != nil {
			return Observation{Status: StatusUnavailable, Detail: "cannot read shard leadership: " + err.Error()}
		}
		if why == "" && !sharded {
			// Every node took a write, but a cluster that reports no shard
			// says nothing about halted shards.
			return Observation{Status: StatusUnavailable, Detail: "no node reports a raft shard, so shard leadership cannot be judged; every node took a write"}
		}
		if why == "" {
			return Observation{Status: StatusPass, Detail: "every shard led and every node took a write", ElapsedNS: int64(time.Since(healedAt))}
		}
		select {
		case <-lctx.Done():
			if ctx.Err() != nil {
				return Observation{Status: StatusUnavailable, Detail: "not checked: the run was interrupted"}
			}
			return Observation{Status: StatusFail, Detail: "not recovered " + o.RecoveryTimeout.String() + " after the last heal: " + why}
		case <-time.After(livenessPoll):
		}
	}
}

// livenessRound checks every node once. It returns why the cluster has not
// recovered, or "" when it has, and whether any node hosts a shard; an error
// means the cluster cannot say. A node hosting no shard has none halted: its
// write is what shows it serving.
func (o *Options) livenessRound(ctx context.Context, prefix string) (why string, sharded bool, err error) {
	for _, n := range o.Nodes {
		shards, err := o.Cluster.Shards(ctx, n.Name)
		if errors.Is(err, ErrUnavailable) {
			return "", false, err
		}
		if err != nil {
			return err.Error(), sharded, nil
		}
		sharded = sharded || len(shards) > 0
		for _, id := range slices.Sorted(maps.Keys(shards)) {
			if shards[id] == 0 {
				return "shard " + strconv.FormatUint(id, 10) + " on node " + n.Name + " has no leader", sharded, nil
			}
		}
		if why := o.probe(ctx, n, prefix); why != "" {
			return why, sharded, nil
		}
	}
	return "", sharded, nil
}

// probe writes one key through n, outside the workload's key space so the
// ledger never sees it.
func (o *Options) probe(ctx context.Context, n Node, prefix string) string {
	pctx, cancel := context.WithTimeout(ctx, time.Duration(o.Workload.OpTimeout))
	defer cancel()
	conn, err := o.Dial(pctx, n.Address)
	if err != nil {
		return "node " + n.Name + " cannot be dialed: " + err.Error()
	}
	defer conn.Close()
	if err := conn.Put(pctx, prefix+"/probe/"+n.Name, []byte(n.Name)); err != nil {
		return "node " + n.Name + " refused a write: " + err.Error()
	}
	return ""
}

// replicas is E3. It reads until every shard's replicas meet at one applied
// index, or RecoveryTimeout passes and the last read is judged as it stands.
func (o *Options) replicas(ctx context.Context) Observation {
	if o.Replicas == nil {
		return Observation{Status: StatusUnavailable, Detail: replicaStatesUnavailable}
	}
	rctx, cancel := context.WithTimeout(ctx, o.RecoveryTimeout)
	defer cancel()
	for {
		states, err := o.Replicas.ReplicaStates(rctx)
		if err != nil {
			return Observation{Status: StatusUnavailable, Detail: "cannot read replica states: " + err.Error()}
		}
		if settled(states) {
			return compareReplicas(states)
		}
		select {
		case <-rctx.Done():
			return compareReplicas(states)
		case <-time.After(replicaPoll):
		}
	}
}

// settled reports whether every shard's replicas stand at one applied index.
// A follower still applying the last entries trails the leader, and replicas
// at different indexes prove nothing, so E3 reads again until they meet.
func settled(states []ReplicaState) bool {
	index := make(map[uint64]uint64, len(states))
	for _, s := range states {
		if at, ok := index[s.ShardID]; ok && at != s.AppliedIndex {
			return false
		}
		index[s.ShardID] = s.AppliedIndex
	}
	return true
}

// compareReplicas judges E3: replicas of a shard at the same applied index
// must hold the same state. Replicas at different indexes prove nothing
// either way: the cluster had not quiesced.
func compareReplicas(states []ReplicaState) Observation {
	if len(states) == 0 {
		return Observation{Status: StatusUnavailable, Detail: "no replica reported its state"}
	}
	byShard := make(map[uint64][]ReplicaState)
	for _, s := range states {
		byShard[s.ShardID] = append(byShard[s.ShardID], s)
	}
	var unsettled string
	for _, id := range slices.Sorted(maps.Keys(byShard)) {
		rs := byShard[id]
		first := rs[0]
		for _, r := range rs[1:] {
			shard := "shard " + strconv.FormatUint(id, 10)
			switch {
			case r.AppliedIndex != first.AppliedIndex:
				if unsettled == "" {
					unsettled = shard + ": replicas at applied index " + strconv.FormatUint(first.AppliedIndex, 10) +
						" and " + strconv.FormatUint(r.AppliedIndex, 10) + "; the cluster did not quiesce"
				}
			case r.Hash != first.Hash:
				return Observation{Status: StatusFail, Detail: shard + " diverges at applied index " +
					strconv.FormatUint(first.AppliedIndex, 10) + ": node " + first.Node + " and node " + r.Node + " hold different state"}
			}
		}
	}
	if unsettled != "" {
		return Observation{Status: StatusUnavailable, Detail: unsettled}
	}
	return Observation{Status: StatusPass, Detail: strconv.Itoa(len(byShard)) + " shards, " + strconv.Itoa(len(states)) + " replicas, identical state"}
}

// startProgress prints one line per interval until stopped.
func (o *Options) startProgress(w *workload.Workload, events *eventLog, origin time.Time) (stop func()) {
	if o.Progress == nil {
		return func() {}
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		t := time.NewTicker(o.ProgressInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				s := w.Stats()
				fmt.Fprintf(o.Progress, "clusterproof: %s of %s: %d ops (%d ok, %d failed, %d indeterminate), %d of %d faults injected\n",
					time.Since(origin).Round(time.Second), o.Duration, s.Ops(), s.OK, s.Fail, s.Indeterminate,
					events.injected(), len(o.Schedule.Windows))
			}
		}
	})
	return func() {
		close(done)
		wg.Wait()
	}
}

// runClock is the run's one clock: nanoseconds since the run began, on the
// monotonic clock. The history reads it directly; nemesis events are placed
// on it as they are recorded.
type runClock struct{ origin time.Time }

func (c runClock) Now() int64 { return int64(time.Since(c.origin)) }

// eventLog records nemesis events on the run's clock.
type eventLog struct {
	origin time.Time

	mu     sync.Mutex
	events []EventRecord
}

var _ nemesis.Recorder = (*eventLog)(nil)

// Record places e on the run's clock. The offset is taken while e.Time still
// carries its monotonic reading; the wall time is then kept in UTC.
func (l *eventLog) Record(e nemesis.Event) {
	r := EventRecord{TimeNS: int64(e.Time.Sub(l.origin)), Event: e}
	r.Time = e.Time.UTC()
	l.mu.Lock()
	l.events = append(l.events, r)
	l.mu.Unlock()
}

func (l *eventLog) snapshot() []EventRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.events)
}

func (l *eventLog) count(match func(EventRecord) bool) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, e := range l.events {
		if match(e) {
			n++
		}
	}
	return n
}

func (l *eventLog) injected() int {
	return l.count(func(e EventRecord) bool { return e.Op == nemesis.OpInject && e.Phase == nemesis.PhaseOK })
}

func (l *eventLog) failures() int {
	return l.count(func(e EventRecord) bool { return e.Phase == nemesis.PhaseFail })
}
