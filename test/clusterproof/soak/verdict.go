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
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/jeremyhahn/go-qrdb/test/clusterproof/history"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/ledger"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/nemesis"
)

// Exit codes of cluster-proof and cluster-proof-check.
const (
	// ExitProven: the run completed, faults were injected and healed, and
	// E1 to E4 each passed on evidence.
	ExitProven = 0
	// ExitViolated: a check found a violation of E1 to E4.
	ExitViolated = 1
	// ExitInvalid: the run or the bundle cannot support a verdict -- bad
	// configuration, a fault that did not inject or heal, an interrupted
	// run, a criterion with no evidence, or a bundle that fails
	// verification.
	ExitInvalid = 2
)

// Status is one criterion's outcome.
type Status string

const (
	StatusPass        Status = "pass"
	StatusFail        Status = "fail"
	StatusUnavailable Status = "unavailable"
	// StatusMeasured is the status of E5 to E7 until the owner ratifies a
	// bound: the figures are reported and nothing is claimed.
	StatusMeasured Status = "measured"
)

// Outcome summarizes a verdict in one word.
type Outcome string

const (
	OutcomeProven   Outcome = "proven"
	OutcomeViolated Outcome = "violated"
	OutcomeInvalid  Outcome = "invalid"
)

// outcomes maps an outcome to its exit code.
var outcomes = map[Outcome]int{
	OutcomeProven:   ExitProven,
	OutcomeViolated: ExitViolated,
	OutcomeInvalid:  ExitInvalid,
}

// Criterion is one of E1 to E7 as a run judged it.
type Criterion struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Observation is a criterion's evidence that the history cannot carry -- the
// cluster's own answer after the run -- recorded once and judged again by the
// check.
type Observation struct {
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
	// ElapsedNS is how long the observation took to hold, where that is
	// itself a measurement (E4's recovery after the last heal).
	ElapsedNS int64 `json:"elapsed_ns,omitempty"`
}

// RunStatus records what went wrong with a run itself, as opposed to the
// cluster: each entry makes the run invalid.
type RunStatus struct {
	Interrupted bool     `json:"interrupted,omitempty"`
	Errors      []string `json:"errors,omitempty"`
	// Unhealed names every fault whose final heal failed, with its error.
	Unhealed []string `json:"unhealed,omitempty"`
}

// Verdict is the judgment of one run, written to verdict.json.
type Verdict struct {
	Format int `json:"format"`
	// Seal is the node seal type the verdict holds for: a run proves the
	// cluster under that seal type and no other.
	Seal    string  `json:"seal"`
	Outcome Outcome `json:"outcome"`
	Exit    int     `json:"exit"`
	// Reasons say why a run is not proven; empty on a proven run.
	Reasons  []string         `json:"reasons,omitempty"`
	Criteria []Criterion      `json:"criteria"`
	Results  []history.Result `json:"results"`
}

// evidence is everything a verdict is derived from. The run assembles it
// live; the check reassembles it from the bundle and must reach the same
// verdict.
type evidence struct {
	history  history.History
	events   []EventRecord
	checkers []history.Checker
	run      RunRecord
	timings  Timings
}

// ledgerViolations are the ledger anomalies that are a defect in the cluster;
// every other ledger anomaly means the ledger could not decide.
var ledgerViolations = map[string]bool{
	ledger.KindLost:       true,
	ledger.KindPhantom:    true,
	ledger.KindDuplicated: true,
}

// judge derives the verdict. It is deterministic, so the check can rerun it
// and compare bytes.
func judge(ev evidence) Verdict {
	v := Verdict{Format: bundleFormat, Seal: ev.run.Seal}
	var reasons []string

	results, e1, e2, why := judgeHistory(ev.history, ev.checkers)
	v.Results = results
	reasons = append(reasons, why...)

	e3 := observed("E3", "Zero replica divergence", ev.run.Replicas)
	e4 := observed("E4", "Zero permanently halted shards", ev.run.Liveness)
	v.Criteria = append(v.Criteria, e1, e2, e3, e4)
	v.Criteria = append(v.Criteria, measured(ev)...)

	reasons = append(reasons, runReasons(ev)...)

	violated := false
	for _, c := range v.Criteria[:4] {
		switch c.Status {
		case StatusFail:
			violated = true
		case StatusUnavailable:
			reasons = append(reasons, c.ID+" unavailable: "+c.Detail)
		}
	}
	switch {
	case violated:
		v.Outcome = OutcomeViolated
	case len(reasons) > 0:
		v.Outcome = OutcomeInvalid
	default:
		v.Outcome = OutcomeProven
	}
	v.Exit = outcomes[v.Outcome]
	v.Reasons = reasons
	return v
}

// judgeHistory runs the ledger (E1) and the configured checkers (E2).
func judgeHistory(h history.History, checkers []history.Checker) ([]history.Result, Criterion, Criterion, []string) {
	e1 := Criterion{ID: "E1", Name: "Zero acknowledged-write loss"}
	e2 := Criterion{ID: "E2", Name: "Zero linearizability violations"}
	var results []history.Result
	var reasons []string

	res, err := ledger.Checker{}.Check(h)
	if err != nil {
		e1.Status, e1.Detail = StatusUnavailable, "the ledger could not interpret the history: "+err.Error()
	} else {
		results = append(results, res)
		e1.Status, e1.Detail = ledgerStatus(res)
	}

	if len(checkers) == 0 {
		e2.Status, e2.Detail = StatusUnavailable, "no linearizability checker configured"
		return results, e1, e2, reasons
	}
	e2.Status = StatusPass
	var failed []string
	for _, c := range checkers {
		res, err := c.Check(h)
		if err != nil {
			e2.Status = StatusUnavailable
			reasons = append(reasons, "a checker could not interpret the history: "+err.Error())
			continue
		}
		results = append(results, res)
		switch {
		case res.Valid:
		case res.Checked == 0:
			// A checker that examined nothing found nothing either way: it
			// could not decide, which is never a violation.
			e2.Status = StatusUnavailable
		default:
			failed = append(failed, res.Checker+" ("+strconv.Itoa(len(res.Anomalies))+" anomalies)")
		}
	}
	switch {
	case len(failed) > 0:
		e2.Status, e2.Detail = StatusFail, "invalid: "+strings.Join(failed, ", ")
	case e2.Status == StatusUnavailable:
		e2.Detail = "a configured checker could not judge the history"
	}
	return results, e1, e2, reasons
}

// ledgerStatus reads E1 off the ledger's result.
func ledgerStatus(res history.Result) (Status, string) {
	if res.Valid {
		return StatusPass, strconv.Itoa(res.Checked) + " acknowledged writes checked"
	}
	counts := make(map[string]int)
	for _, a := range res.Anomalies {
		counts[a.Kind]++
	}
	kinds := slices.Sorted(maps.Keys(counts))
	var parts []string
	violated := false
	for _, k := range kinds {
		parts = append(parts, strconv.Itoa(counts[k])+" "+k)
		violated = violated || ledgerViolations[k]
	}
	if violated {
		return StatusFail, strings.Join(parts, ", ")
	}
	return StatusUnavailable, "the ledger could not decide: " + strings.Join(parts, ", ")
}

// observed turns a recorded observation into its criterion.
func observed(id, name string, o Observation) Criterion {
	st := o.Status
	if st != StatusPass && st != StatusFail {
		st = StatusUnavailable
	}
	return Criterion{ID: id, Name: name, Status: st, Detail: o.Detail}
}

// runReasons lists what makes the run itself invalid, whatever the cluster
// did.
func runReasons(ev evidence) []string {
	var reasons []string
	if !knownSeal(ev.run.Seal) {
		reasons = append(reasons, "the run names no seal type the release ships: "+strconv.Quote(ev.run.Seal))
	}
	if ev.run.Status.Interrupted {
		reasons = append(reasons, "the run was interrupted")
	}
	reasons = append(reasons, ev.run.Status.Errors...)
	for _, n := range ev.run.Status.Unhealed {
		reasons = append(reasons, "a fault did not heal: "+n)
	}
	injected := 0
	for _, e := range ev.events {
		if e.Phase == nemesis.PhaseFail {
			reasons = append(reasons, "nemesis "+string(e.Op)+" of "+e.Nemesis+" failed: "+e.Error)
		}
		if e.Op == nemesis.OpInject && e.Phase == nemesis.PhaseOK {
			injected++
		}
	}
	if injected == 0 {
		if len(ev.run.Nemesis) == 0 {
			reasons = append(reasons, "no nemesis configured: the run injected no fault and proves nothing about one")
		} else {
			reasons = append(reasons, "the nemesis never injected a fault")
		}
	}
	return reasons
}

// measured reports E5 to E7 from the timings. Their bounds are not ratified,
// so each is measured and never pass or fail.
func measured(ev evidence) []Criterion {
	t := ev.timings
	var failover []string
	for _, w := range t.Windows {
		if w.FirstAckAfterInjectNS >= 0 {
			failover = append(failover, w.Nemesis+" "+nsString(w.FirstAckAfterInjectNS))
		}
	}
	e5 := "first acknowledged write after each inject: " + orNone(failover)
	e6 := "every shard led and every node writable " + nsString(ev.run.Liveness.ElapsedNS) + " after the last heal"
	if ev.run.Liveness.Status != StatusPass {
		e6 = "not reached: E4 did not pass"
	}
	e7 := strconv.FormatUint(t.Overall.Ops, 10) + " ops, " + strconv.FormatFloat(t.Overall.OpsPerSecond, 'f', 1, 64) +
		" ops/s; ok latency p50 " + nsString(t.Overall.P50NS) + ", p99 " + nsString(t.Overall.P99NS) +
		", p99.9 " + nsString(t.Overall.P999NS) + "; see timings.json"
	return []Criterion{
		{ID: "E5", Name: "Failover time", Status: StatusMeasured, Detail: e5},
		{ID: "E6", Name: "Recovery time", Status: StatusMeasured, Detail: e6},
		{ID: "E7", Name: "Soak throughput and latency", Status: StatusMeasured, Detail: e7},
	}
}

func orNone(parts []string) string {
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}
