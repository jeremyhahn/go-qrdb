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
	"cmp"
	"math"
	"slices"
	"time"

	"github.com/jeremyhahn/go-qrdb/test/clusterproof/history"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/nemesis"
)

// Timings are the E5 to E7 measurements, written to timings.json. Every time
// is nanoseconds on the run's clock; -1 means the event never happened.
type Timings struct {
	Format int `json:"format"`
	// Overall covers every load operation, the final read excluded; Quiet
	// those called outside every fault window, Faulted those called inside
	// one.
	Overall Throughput     `json:"overall"`
	Quiet   Throughput     `json:"quiet"`
	Faulted Throughput     `json:"faulted"`
	Windows []WindowTiming `json:"windows"`
}

// Throughput is the rate and latency of a set of operations. Latencies are
// over acknowledged operations only: a failed or indeterminate one says how
// long the client waited, not how long the cluster took.
type Throughput struct {
	Ops          uint64  `json:"ops"`
	OK           uint64  `json:"ok"`
	SpanNS       int64   `json:"span_ns"`
	OpsPerSecond float64 `json:"ops_per_second"`
	P50NS        int64   `json:"p50_ns"`
	P99NS        int64   `json:"p99_ns"`
	P999NS       int64   `json:"p999_ns"`
}

// WindowTiming is one fault window as it was played.
type WindowTiming struct {
	Window  int      `json:"window"`
	Nemesis string   `json:"nemesis"`
	Targets []string `json:"targets"`
	// InjectNS and HealNS are when the inject and the heal completed;
	// StartNS is when the inject was called.
	StartNS  int64 `json:"start_ns"`
	InjectNS int64 `json:"inject_ns"`
	HealNS   int64 `json:"heal_ns"`
	// FirstAckAfterInjectNS is the time from the inject completing to the
	// first acknowledged write called after it: the failover measurement.
	FirstAckAfterInjectNS int64 `json:"first_ack_after_inject_ns"`
	// FirstAckAfterHealNS is the same after the heal.
	FirstAckAfterHealNS int64 `json:"first_ack_after_heal_ns"`
}

// computeTimings measures the load phase of the history -- the operations of
// the first clients load clients -- against the fault windows the events
// record. The final read runs as one more client after the recovery wait, and
// counting it would stretch the span by that wait.
func computeTimings(all history.History, events []EventRecord, clients int) Timings {
	t := Timings{Format: bundleFormat, Windows: windowTimings(events)}
	var h history.History
	for _, op := range all {
		if op.Client < clients {
			h = append(h, op)
		}
	}
	var acks []history.Op
	for _, op := range h {
		if op.Outcome == history.OutcomeOK && op.Writes() {
			acks = append(acks, op)
		}
	}
	slices.SortFunc(acks, func(a, b history.Op) int { return cmp.Compare(a.CallNS, b.CallNS) })
	for i := range t.Windows {
		w := &t.Windows[i]
		w.FirstAckAfterInjectNS = firstAckAfter(acks, w.InjectNS)
		w.FirstAckAfterHealNS = firstAckAfter(acks, w.HealNS)
	}

	var quiet, faulted history.History
	var faultedSpan int64
	for _, w := range t.Windows {
		if end := windowEnd(w); end > w.StartNS {
			faultedSpan += end - w.StartNS
		}
	}
	for _, op := range h {
		if inWindow(t.Windows, op.CallNS) {
			faulted = append(faulted, op)
		} else {
			quiet = append(quiet, op)
		}
	}
	t.Overall = throughput(h, span(h))
	t.Faulted = throughput(faulted, faultedSpan)
	t.Quiet = throughput(quiet, max(0, span(h)-faultedSpan))
	return t
}

// windowTimings reads each window's inject and heal off the events.
func windowTimings(events []EventRecord) []WindowTiming {
	byWindow := make(map[int]*WindowTiming)
	var out []*WindowTiming
	for _, e := range events {
		if e.Op != nemesis.OpInject && e.Op != nemesis.OpHeal {
			continue
		}
		w := byWindow[e.Window]
		if w == nil {
			w = &WindowTiming{Window: e.Window, Nemesis: e.Nemesis, Targets: e.Targets, StartNS: -1, InjectNS: -1, HealNS: -1}
			byWindow[e.Window] = w
			out = append(out, w)
		}
		switch {
		case e.Op == nemesis.OpInject && e.Phase == nemesis.PhaseInvoke:
			w.StartNS = e.TimeNS
		case e.Op == nemesis.OpInject && e.Phase == nemesis.PhaseOK:
			w.InjectNS = e.TimeNS
		case e.Op == nemesis.OpHeal && e.Phase == nemesis.PhaseOK:
			w.HealNS = e.TimeNS
		}
	}
	res := make([]WindowTiming, len(out))
	for i, w := range out {
		res[i] = *w
	}
	return res
}

// windowEnd is when a window's fault was certainly gone: its heal, or the
// end of time if it never healed.
func windowEnd(w WindowTiming) int64 {
	if w.HealNS < 0 {
		return math.MaxInt64
	}
	return w.HealNS
}

func inWindow(windows []WindowTiming, at int64) bool {
	for _, w := range windows {
		if w.StartNS >= 0 && at >= w.StartNS && at <= windowEnd(w) {
			return true
		}
	}
	return false
}

// firstAckAfter is the time from at to the return of the first acknowledged
// write called at or after it, or -1.
func firstAckAfter(acks []history.Op, at int64) int64 {
	if at < 0 {
		return -1
	}
	i, _ := slices.BinarySearchFunc(acks, at, func(op history.Op, t int64) int { return cmp.Compare(op.CallNS, t) })
	best := int64(-1)
	for _, op := range acks[i:] {
		if best < 0 || op.ReturnNS < best {
			best = op.ReturnNS
		}
	}
	if best < 0 {
		return -1
	}
	return best - at
}

// span is the time from the first call to the last return.
func span(h history.History) int64 {
	if len(h) == 0 {
		return 0
	}
	first, last := h[0].CallNS, h[0].ReturnNS
	for _, op := range h {
		first = min(first, op.CallNS)
		last = max(last, op.ReturnNS)
	}
	return last - first
}

func throughput(h history.History, spanNS int64) Throughput {
	t := Throughput{Ops: uint64(len(h)), SpanNS: spanNS}
	var lat []int64
	for _, op := range h {
		if op.Outcome == history.OutcomeOK {
			lat = append(lat, op.ReturnNS-op.CallNS)
		}
	}
	t.OK = uint64(len(lat))
	if spanNS > 0 {
		t.OpsPerSecond = float64(t.Ops) / time.Duration(spanNS).Seconds()
	}
	slices.Sort(lat)
	t.P50NS = percentile(lat, 500)
	t.P99NS = percentile(lat, 990)
	t.P999NS = percentile(lat, 999)
	return t
}

// percentile returns the nearest-rank permille of sorted, or 0 when it is
// empty.
func percentile(sorted []int64, permille int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := (len(sorted)*permille + 999) / 1000
	return sorted[max(rank, 1)-1]
}

// nsString spells a nanosecond count as a Go duration.
func nsString(ns int64) string { return time.Duration(ns).String() }
