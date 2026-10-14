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

// Package linearizability is the cluster proof's E2 check: the whole recorded
// history must be linearizable, every key and every operation, as decided by
// porcupine (github.com/anishathalye/porcupine).
//
// Linearizability is local -- a history is linearizable when every key's
// sub-history is -- so each key is an independent register, searched on its
// own and in parallel, and reported in key order. The register starts absent,
// and the operations step it as the workload's client issues them:
//
//   - Put sets the value; Delete makes the key absent.
//   - CAS swaps to its value exactly when the key is present and holds the
//     expected value; an absent key never swaps.
//   - An ok Get must observe the register: present with that value, or absent.
//
// A failed operation did not apply and is left out, as is an indeterminate
// Get, which observed nothing. An indeterminate write may or may not have
// applied, at any time after its call, so it is given no return: porcupine may
// linearize it anywhere after its call, the end of the history included,
// which is the same as never applying it. An ok CAS without an output applied
// with an unknown result and steps the register as the model says. A CAS with
// no expected value is refused: the format cannot tell an empty comparand from
// an absent one, so the model could only guess what the store did.
//
// The search is exponential in the worst case, so each key is bounded by
// Checker.Timeout. A key the search cannot decide in time is an error, which
// makes E2 unavailable and never passed. Within the bound porcupine decides
// every key as Ok or Illegal, and the evidence is derived only from the
// history, so `cluster-proof-check` re-derives the verdict exactly.
package linearizability

import (
	"cmp"
	"math"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anishathalye/porcupine"

	"github.com/jeremyhahn/go-qrdb/test/clusterproof/history"
)

// Name is the Checker name the linearizability check reports in a Result.
const Name = "linearizability-porcupine"

// The anomaly kinds the check reports.
const (
	// KindNonLinearizable: the key's operations have no linearization. The
	// anomaly names the operations none of which could come next.
	KindNonLinearizable = "non-linearizable"
	// KindVacuous: the history holds no acknowledged operation, so there was
	// nothing to prove and the verdict cannot be a pass.
	KindVacuous = "vacuous"
)

// DefaultTimeout bounds the search on one key when Checker.Timeout is zero.
const DefaultTimeout = 10 * time.Minute

// absent is how an anomaly's Value shows a register that holds no value.
const absent = "absent"

// Checker is the porcupine linearizability check as a history.Checker.
type Checker struct {
	// Timeout bounds the search on each key; zero is DefaultTimeout.
	Timeout time.Duration
}

var _ history.Checker = Checker{}

// register is the model state of one key.
type register struct {
	value   string
	present bool
}

// input is what one operation asked the register to do.
type input struct {
	kind     history.Kind
	value    string
	expected string
}

// steps is the sequential specification of one key, by operation kind. Each
// step reports whether the operation's recorded output is consistent with the
// register, and the register after it. A nil output is a result the client
// never learned.
var steps = map[history.Kind]func(r register, in input, out *history.Output) (bool, register){
	history.KindPut: func(_ register, in input, _ *history.Output) (bool, register) {
		return true, register{value: in.value, present: true}
	},
	history.KindDelete: func(register, input, *history.Output) (bool, register) {
		return true, register{}
	},
	history.KindCAS: func(r register, in input, out *history.Output) (bool, register) {
		match := r.present && r.value == in.expected
		next := r
		if match {
			next = register{value: in.value, present: true}
		}
		return out == nil || out.Swapped == match, next
	},
	history.KindGet: func(r register, _ input, out *history.Output) (bool, register) {
		return out != nil && out.Found == r.present && (!out.Found || out.Value == r.value), r
	},
}

// step applies one operation to the register. History.Validate admits no kind
// steps lacks.
func step(r register, in input, out *history.Output) (bool, register) {
	return steps[in.kind](r, in, out)
}

// model is the sequential specification of one key. Output is a
// *history.Output, nil when the result is unknown.
var model = porcupine.Model{
	Init: func() interface{} { return register{} },
	Step: func(state, in, out interface{}) (bool, interface{}) {
		r, rok := state.(register)
		i, iok := in.(input)
		o, ook := out.(*history.Output)
		if !rok || !iok || !ook {
			// Init and operation build every value the model is given, so
			// this is unreachable; a value of another type fits no register.
			return false, state
		}
		return step(r, i, o)
	},
}

// search is porcupine's bounded search, a variable so a test can stand in for
// a search that runs out of time.
var search = porcupine.CheckOperationsVerbose

// keyOps is one key's share of the history, in the form porcupine checks.
type keyOps struct {
	key   string
	ops   []history.Op
	acked int
}

// Check judges h. It returns an error for a history that fails
// history.Validate, for a CAS without an expected value (*AmbiguousCASError)
// and for a key the search cannot decide within the timeout (*TimeoutError).
func (c Checker) Check(h history.History) (history.Result, error) {
	if err := h.Validate(); err != nil {
		return history.Result{}, err
	}
	keys, err := partition(h)
	if err != nil {
		return history.Result{}, err
	}

	timeout := cmp.Or(c.Timeout, DefaultTimeout)
	anomalies := make([]*history.Anomaly, len(keys))
	errs := make([]error, len(keys))
	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	var wg sync.WaitGroup
	for i, k := range keys {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			anomalies[i], errs[i] = k.check(timeout)
		})
	}
	wg.Wait()

	res := history.Result{Checker: Name}
	for i, k := range keys {
		if errs[i] != nil {
			return history.Result{}, errs[i]
		}
		res.Checked += k.acked
		if anomalies[i] != nil {
			res.Anomalies = append(res.Anomalies, *anomalies[i])
		}
	}
	if res.Checked == 0 {
		res.Anomalies = append(res.Anomalies, history.Anomaly{
			Kind:   KindVacuous,
			Detail: "the history holds no acknowledged operation",
		})
	}
	res.Valid = len(res.Anomalies) == 0
	return res, nil
}

// partition splits h by key, in key order, keeping the operations the model
// steps in call order. It refuses a CAS without an expected value.
func partition(h history.History) ([]*keyOps, error) {
	byKey := make(map[string]*keyOps)
	for _, op := range h {
		if op.Outcome == history.OutcomeFail ||
			(op.Kind == history.KindGet && op.Outcome == history.OutcomeIndeterminate) {
			continue
		}
		if op.Kind == history.KindCAS && op.Input.Expected == "" {
			return nil, &AmbiguousCASError{ID: op.ID}
		}
		k := byKey[op.Key]
		if k == nil {
			k = &keyOps{key: op.Key}
			byKey[op.Key] = k
		}
		k.ops = append(k.ops, op)
		if op.Outcome == history.OutcomeOK {
			k.acked++
		}
	}
	out := make([]*keyOps, 0, len(byKey))
	for _, k := range byKey {
		slices.SortFunc(k.ops, func(a, b history.Op) int {
			return cmp.Or(cmp.Compare(a.CallNS, b.CallNS), cmp.Compare(a.ID, b.ID))
		})
		out = append(out, k)
	}
	slices.SortFunc(out, func(a, b *keyOps) int { return strings.Compare(a.key, b.key) })
	return out, nil
}

// check runs porcupine over the key. When the key has no linearization it
// returns the anomaly that says where the search stopped.
func (k *keyOps) check(timeout time.Duration) (*history.Anomaly, error) {
	ops := make([]porcupine.Operation, len(k.ops))
	for i, op := range k.ops {
		ops[i] = operation(op)
	}
	verdict, info := search(model, ops, timeout)
	if verdict == porcupine.Unknown {
		return nil, &TimeoutError{Key: k.key, Ops: len(ops), Timeout: timeout}
	}
	if verdict == porcupine.Ok {
		return nil, nil
	}
	a := k.anomaly(ops, longest(info.PartialLinearizations()))
	return &a, nil
}

// operation is op as porcupine sees it.
func operation(op history.Op) porcupine.Operation {
	ret := op.ReturnNS
	if op.Outcome == history.OutcomeIndeterminate {
		ret = math.MaxInt64
	}
	return porcupine.Operation{ClientId: op.Client, Input: inputOf(op), Call: op.CallNS, Output: op.Output, Return: ret}
}

// inputOf is what op asked the register to do.
func inputOf(op history.Op) input {
	in := input{kind: op.Kind}
	if op.Input != nil {
		in.value, in.expected = op.Input.Value, op.Input.Expected
	}
	return in
}

// longest picks one longest partial linearization of a single-partition
// check. Porcupine returns them in no fixed order, so ties go to the
// lexicographically least, keeping the evidence the same on every run.
func longest(partitions [][][]int) []int {
	var best []int
	for _, partials := range partitions {
		for _, p := range partials {
			if len(p) > len(best) || (len(p) == len(best) && slices.Compare(p, best) < 0) {
				best = p
			}
		}
	}
	return best
}

// anomaly reports a key with no linearization. Replaying prefix gives the
// register the search reached; the operations named are those that could
// have come next -- none returned before another remaining one was called --
// and none of which is consistent with that register.
func (k *keyOps) anomaly(ops []porcupine.Operation, prefix []int) history.Anomaly {
	r := register{}
	done := make([]bool, len(ops))
	for _, i := range prefix {
		_, r = step(r, inputOf(k.ops[i]), k.ops[i].Output)
		done[i] = true
	}
	frontier := int64(math.MaxInt64)
	for i, op := range ops {
		if !done[i] {
			frontier = min(frontier, op.Return)
		}
	}
	var ids []uint64
	for i, op := range ops {
		if !done[i] && op.Call <= frontier {
			ids = append(ids, k.ops[i].ID)
		}
	}
	slices.Sort(ids)
	value := absent
	if r.present {
		value = r.value
	}
	return history.Anomaly{
		Kind:  KindNonLinearizable,
		Key:   k.key,
		Value: value,
		OpIDs: ids,
		Detail: "no linearization: " + strconv.Itoa(len(prefix)) + " of " + strconv.Itoa(len(ops)) +
			" operations linearize, leaving the register at the value shown, and no operation that could come next is consistent with it",
	}
}
