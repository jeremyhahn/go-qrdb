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

// Package ledger is the cluster proof's acknowledged-write check: every write
// the cluster acknowledged must still be readable afterwards, given the writes
// that came after it.
//
// It reads a history and nothing else, needs no third-party checker, and is
// deliberately weaker than linearizability -- it asks whether data survived,
// not whether every read was fresh -- so it runs on every history beside the
// linearizability check, including one too long for that check to decide, and
// names a lost write in terms the stricter check's evidence does not.
//
// The rules, per key, with "real-time before" meaning one op returned before
// the other was called on the harness clock:
//
//   - A write is a Put, a Delete, or a CAS that swapped. An indeterminate
//     write may or may not have applied, at any time after its call.
//   - The final read is the last ok Get called after every write to the key
//     returned. A key that was written and never finally read is UNREAD: the
//     ledger could not check it, and says so rather than passing it.
//   - A write is a candidate for the final value unless an acknowledged write
//     was called after it returned. An indeterminate write is always a
//     candidate. The initial state, absent, is a candidate only when no write
//     was acknowledged.
//   - LOST: the final read matches no candidate. The acknowledged candidates
//     are reported, because one of them was the cluster's last word and it
//     is gone.
//   - PHANTOM: any ok read observed a value no op wrote to that key, a value
//     whose only writer certainly did not apply (a failed op, an unswapped
//     CAS), or a value whose writer had not been called when the read
//     returned.
//   - DUPLICATED: a value was read, then a different state was read, then the
//     first value was read again, each read after the previous returned. With
//     every written value unique that needs the value's write to land twice
//     -- a replayed proposal -- or a read that returned stale state; the
//     report names the write and all three reads.
//
// The workload writes every value once, so a value identifies its write; a
// history that reuses a value on one key cannot be attributed and Check
// refuses it with an AmbiguousValueError rather than guessing.
package ledger

import (
	"sort"

	"github.com/jeremyhahn/go-qrdb/test/clusterproof/history"
)

// Name is the Checker name the ledger reports in a Result.
const Name = "acknowledged-write-ledger"

// The anomaly kinds the ledger reports.
const (
	KindLost       = "lost"
	KindPhantom    = "phantom"
	KindDuplicated = "duplicated"
	KindUnread     = "unread"
	// KindVacuous: the history holds no acknowledged write, so there was
	// nothing to prove and the verdict cannot be a pass.
	KindVacuous = "vacuous"
)

// Checker is the acknowledged-write ledger as a history.Checker.
type Checker struct{}

var _ history.Checker = Checker{}

// keyOps is one key's share of the history.
type keyOps struct {
	key    string
	writes []history.Op          // ops that applied or may have (Op.Writes)
	gets   []history.Op          // ok Gets
	byVal  map[string]history.Op // every Put/CAS by the value it would write
	lastRe int64                 // latest return of any Put, Delete or CAS
}

// Check judges h. It returns an error only for a history it cannot interpret:
// one that fails history.Validate, or that writes one value twice on a key.
func (Checker) Check(h history.History) (history.Result, error) {
	if err := h.Validate(); err != nil {
		return history.Result{}, err
	}
	keys, err := partition(h)
	if err != nil {
		return history.Result{}, err
	}

	res := history.Result{Checker: Name}
	for _, k := range keys {
		for _, w := range k.writes {
			if w.Outcome == history.OutcomeOK {
				res.Checked++
			}
		}
		res.Anomalies = append(res.Anomalies, k.phantoms()...)
		res.Anomalies = append(res.Anomalies, k.duplicates()...)
		res.Anomalies = append(res.Anomalies, k.final()...)
	}
	if res.Checked == 0 {
		res.Anomalies = append(res.Anomalies, history.Anomaly{
			Kind:   KindVacuous,
			Detail: "the history holds no acknowledged write",
		})
	}
	res.Valid = len(res.Anomalies) == 0
	return res, nil
}

// partition splits h by key, in key order, indexing every written value.
func partition(h history.History) ([]*keyOps, error) {
	byKey := make(map[string]*keyOps)
	for _, op := range h {
		k := byKey[op.Key]
		if k == nil {
			k = &keyOps{key: op.Key, byVal: make(map[string]history.Op)}
			byKey[op.Key] = k
		}
		if op.Kind == history.KindGet {
			if op.Outcome == history.OutcomeOK {
				k.gets = append(k.gets, op)
			}
			continue
		}
		k.lastRe = max(k.lastRe, op.ReturnNS)
		if op.Writes() {
			k.writes = append(k.writes, op)
		}
		if v, ok := op.Written(); ok {
			if prev, dup := k.byVal[v]; dup {
				return nil, &AmbiguousValueError{Key: op.Key, Value: v, OpIDs: []uint64{prev.ID, op.ID}}
			}
			k.byVal[v] = op
		}
	}
	out := make([]*keyOps, 0, len(byKey))
	for _, k := range byKey {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out, nil
}

// phantoms checks every ok read against the writes that could explain it.
func (k *keyOps) phantoms() []history.Anomaly {
	var out []history.Anomaly
	for _, r := range k.gets {
		if !r.Output.Found {
			continue
		}
		v := r.Output.Value
		w, ok := k.byVal[v]
		switch {
		case !ok:
			out = append(out, k.anomaly(KindPhantom, v, "read observed a value no op wrote to this key", r.ID))
		case !w.Writes():
			out = append(out, k.anomaly(KindPhantom, v, "read observed a value whose only writer did not apply", w.ID, r.ID))
		case w.CallNS > r.ReturnNS:
			out = append(out, k.anomaly(KindPhantom, v, "read returned a value before its write was called", w.ID, r.ID))
		}
	}
	return out
}

// observation is what one read saw: a value, or absence.
type observation struct {
	found bool
	value string
}

// seen is the latest-called completed read with a given observation.
type seen struct {
	obs  observation
	call int64
	id   uint64
}

// duplicates finds a value read, then a different state read, then the value
// read again, each read after the previous returned.
//
// It sweeps reads in call order as the third read. Reads that returned before
// the current one was called are folded in, in return order, keeping the two
// latest-called with distinct observations: the latest-called completed read
// whose observation differs from v is then one of those two, and it lies
// between an earlier read of v and this one exactly when it was called after
// the earliest read of v returned.
func (k *keyOps) duplicates() []history.Anomaly {
	byCall := append([]history.Op(nil), k.gets...)
	sort.Slice(byCall, func(i, j int) bool { return byCall[i].CallNS < byCall[j].CallNS })
	byRet := append([]history.Op(nil), k.gets...)
	sort.Slice(byRet, func(i, j int) bool { return byRet[i].ReturnNS < byRet[j].ReturnNS })

	// firstRead is, per value, the read of it that returned earliest.
	firstRead := make(map[string]history.Op)
	for _, r := range byRet {
		if r.Output.Found {
			if _, ok := firstRead[r.Output.Value]; !ok {
				firstRead[r.Output.Value] = r
			}
		}
	}

	var best, second *seen
	reported := make(map[string]struct{})
	var out []history.Anomaly
	next := 0
	for _, r3 := range byCall {
		for ; next < len(byRet) && byRet[next].ReturnNS < r3.CallNS; next++ {
			best, second = fold(best, second, byRet[next])
		}
		if !r3.Output.Found {
			continue
		}
		v := r3.Output.Value
		if _, done := reported[v]; done {
			continue
		}
		mid := best
		if mid != nil && mid.obs == (observation{found: true, value: v}) {
			mid = second
		}
		r1 := firstRead[v]
		if mid == nil || mid.call <= r1.ReturnNS {
			continue
		}
		reported[v] = struct{}{}
		ids := []uint64{r1.ID, mid.id, r3.ID}
		if w, ok := k.byVal[v]; ok {
			ids = append([]uint64{w.ID}, ids...)
		}
		out = append(out, k.anomaly(KindDuplicated, v,
			"value read, overwritten, then read again: its write applied twice or a read was stale", ids...))
	}
	return out
}

// fold adds a completed read to the two latest-called distinct observations.
func fold(best, second *seen, r history.Op) (*seen, *seen) {
	s := &seen{obs: observation{found: r.Output.Found, value: r.Output.Value}, call: r.CallNS, id: r.ID}
	switch {
	case best == nil || s.call > best.call:
		if best != nil && best.obs != s.obs {
			second = best
		}
		best = s
	case s.obs == best.obs:
	case second == nil || s.call > second.call:
		second = s
	}
	return best, second
}

// final checks the final read against the candidate writes.
func (k *keyOps) final() []history.Anomaly {
	if len(k.writes) == 0 {
		return nil
	}
	var acked, candidates []history.Op
	for _, w := range k.writes {
		if w.Outcome == history.OutcomeOK {
			acked = append(acked, w)
		}
	}
	for _, w := range k.writes {
		if w.Outcome == history.OutcomeIndeterminate || !supersededBy(w, acked) {
			candidates = append(candidates, w)
		}
	}

	var fin *history.Op
	for i := range k.gets {
		r := &k.gets[i]
		if r.CallNS > k.lastRe && (fin == nil || r.CallNS > fin.CallNS || (r.CallNS == fin.CallNS && r.ID > fin.ID)) {
			fin = r
		}
	}
	if fin == nil {
		return []history.Anomaly{k.anomaly(KindUnread, "",
			"key was written and never read after its last write returned; its candidate last writes are unchecked",
			ids(candidates)...)}
	}
	if explains(candidates, len(acked) == 0, *fin) {
		return nil
	}

	var lost []history.Op
	for _, w := range candidates {
		if w.Outcome == history.OutcomeOK {
			lost = append(lost, w)
		}
	}
	got := "absent"
	if fin.Output.Found {
		got = fin.Output.Value
	}
	return []history.Anomaly{k.anomaly(KindLost, got,
		"final read matches no acknowledged-or-possible last write; the acknowledged ones are lost",
		append(ids(lost), fin.ID)...)}
}

// supersededBy reports whether an acknowledged write was called after w
// returned, which orders it after w and rules w out as the final value.
func supersededBy(w history.Op, acked []history.Op) bool {
	for _, a := range acked {
		if a.CallNS > w.ReturnNS {
			return true
		}
	}
	return false
}

// explains reports whether the final read is the effect of one candidate, or
// of the initial absent state when that is still possible.
func explains(candidates []history.Op, initialPossible bool, fin history.Op) bool {
	if !fin.Output.Found {
		if initialPossible {
			return true
		}
		for _, w := range candidates {
			if w.Kind == history.KindDelete {
				return true
			}
		}
		return false
	}
	for _, w := range candidates {
		if v, ok := w.Written(); ok && v == fin.Output.Value {
			return true
		}
	}
	return false
}

func (k *keyOps) anomaly(kind, value, detail string, opIDs ...uint64) history.Anomaly {
	return history.Anomaly{Kind: kind, Key: k.key, Value: value, OpIDs: opIDs, Detail: detail}
}

func ids(ops []history.Op) []uint64 {
	out := make([]uint64, len(ops))
	for i, op := range ops {
		out[i] = op.ID
	}
	return out
}
