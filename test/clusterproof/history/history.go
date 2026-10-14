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

// Package history is the cluster proof's operation-history format: one record
// per client operation, holding what the client asked for, what it got back,
// when it called and returned on the harness's single monotonic clock, and
// whether the outcome is known.
//
// The serialised form is JSON lines, one Op per line, so a history from a
// multi-day soak can be streamed, appended to and read back without holding a
// document open. Every checker -- the acknowledged-write ledger and the
// linearizability checker -- consumes a History through the
// Checker interface and nothing else, so the format is the contract between the
// workload that records and the checkers that judge.
package history

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"sort"
)

// Kind names a client operation.
type Kind string

// The operations the workload issues. A Put, a Delete and a swapped CAS write
// the key; a Get and an unswapped CAS only observe it.
const (
	KindPut    Kind = "put"
	KindGet    Kind = "get"
	KindDelete Kind = "delete"
	KindCAS    Kind = "cas"
)

// Outcome says what the client knows about an operation's effect.
type Outcome string

// The three outcomes. Indeterminate is not a softer failure: a timed-out or
// cut-off write may have applied, and every checker must allow that it did and
// that it did not.
const (
	// OutcomeOK: the cluster answered and the answer is in Output.
	OutcomeOK Outcome = "ok"
	// OutcomeFail: the operation certainly had no effect.
	OutcomeFail Outcome = "fail"
	// OutcomeIndeterminate: the effect is unknown -- a timeout, an
	// unreachable node, a connection lost after the request left.
	OutcomeIndeterminate Outcome = "indeterminate"
)

// validKinds and validOutcomes are the closed vocabularies Validate enforces.
var (
	validKinds = map[Kind]struct{}{
		KindPut: {}, KindGet: {}, KindDelete: {}, KindCAS: {},
	}
	validOutcomes = map[Outcome]struct{}{
		OutcomeOK: {}, OutcomeFail: {}, OutcomeIndeterminate: {},
	}
)

// Input is what the client sent. Value is the value a Put writes or the new
// value a CAS installs; Expected is the value a CAS compares against.
type Input struct {
	Value    string `json:"value,omitempty"`
	Expected string `json:"expected,omitempty"`
}

// Output is what the cluster answered, present only on an OutcomeOK record.
// Found and Value are a Get's result; Swapped is whether a CAS replaced the
// value. A false field is omitted, which is unambiguous because Output only
// exists when the answer is known.
type Output struct {
	Value   string `json:"value,omitempty"`
	Found   bool   `json:"found,omitempty"`
	Swapped bool   `json:"swapped,omitempty"`
}

// Op is one client operation: one line of the serialised history.
//
// CallNS and ReturnNS are nanoseconds on the recording harness's monotonic
// clock, never wall time, so two records from one run are comparable and two
// runs are not. For an indeterminate op ReturnNS is when the client stopped
// waiting, not when the effect happened: the effect, if any, may land later.
type Op struct {
	ID       uint64  `json:"id"`
	Client   int     `json:"client"`
	Kind     Kind    `json:"op"`
	Key      string  `json:"key"`
	Input    *Input  `json:"input,omitempty"`
	Output   *Output `json:"output,omitempty"`
	CallNS   int64   `json:"call_ns"`
	ReturnNS int64   `json:"return_ns"`
	Outcome  Outcome `json:"outcome"`
	Error    string  `json:"error,omitempty"`
}

// Writes reports whether op, had it applied, changed the key: a Put, a Delete,
// or a CAS that swapped -- or a CAS whose answer is unknown and so may have.
func (op Op) Writes() bool {
	switch op.Kind {
	case KindPut, KindDelete:
		return op.Outcome != OutcomeFail
	case KindCAS:
		if op.Outcome == OutcomeIndeterminate {
			return true
		}
		return op.Outcome == OutcomeOK && op.Output != nil && op.Output.Swapped
	}
	return false
}

// Written returns the value op leaves under its key if it applied, and false
// when that is "absent" (a Delete).
func (op Op) Written() (string, bool) {
	if op.Kind == KindDelete || op.Input == nil {
		return "", false
	}
	return op.Input.Value, true
}

// History is an ordered set of operations, sorted by CallNS then ID.
type History []Op

// Sort orders h by call time, breaking ties by ID, so a history assembled
// from per-client logs reads in the order the calls were made.
func (h History) Sort() {
	sort.Slice(h, func(i, j int) bool {
		if h[i].CallNS != h[j].CallNS {
			return h[i].CallNS < h[j].CallNS
		}
		return h[i].ID < h[j].ID
	})
}

// Validate checks every record against the format: a known kind and outcome,
// a key, a return no earlier than the call, an Output only on an ok record,
// an Input on every write, and IDs unique across the history.
func (h History) Validate() error {
	seen := make(map[uint64]struct{}, len(h))
	for _, op := range h {
		if _, dup := seen[op.ID]; dup {
			return &InvalidOpError{ID: op.ID, Reason: "duplicate operation id"}
		}
		seen[op.ID] = struct{}{}
		if err := op.validate(); err != nil {
			return err
		}
	}
	return nil
}

// validate checks one record; see History.Validate.
func (op Op) validate() error {
	invalid := func(reason string) error { return &InvalidOpError{ID: op.ID, Reason: reason} }
	if _, ok := validKinds[op.Kind]; !ok {
		return invalid("unknown op " + string(op.Kind))
	}
	if _, ok := validOutcomes[op.Outcome]; !ok {
		return invalid("unknown outcome " + string(op.Outcome))
	}
	if op.Key == "" {
		return invalid("empty key")
	}
	if op.ReturnNS < op.CallNS {
		return invalid("return_ns precedes call_ns")
	}
	if op.Output != nil && op.Outcome != OutcomeOK {
		return invalid("output on a record whose outcome is not ok")
	}
	if op.Kind == KindGet && op.Outcome == OutcomeOK && op.Output == nil {
		return invalid("ok read without output")
	}
	if (op.Kind == KindPut || op.Kind == KindCAS) && op.Input == nil {
		return invalid("write without input")
	}
	return nil
}

// Encode writes h to w as JSON lines, one Op per line.
func Encode(w io.Writer, h History) error {
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	for i := range h {
		if err := enc.Encode(&h[i]); err != nil {
			return &EncodeError{ID: h[i].ID, Err: err}
		}
	}
	if err := bw.Flush(); err != nil {
		return &EncodeError{Err: err}
	}
	return nil
}

// maxLineBytes bounds one serialised Op. Values are generated by the workload
// and short; a line past this is a corrupt file, not a record.
const maxLineBytes = 1 << 20

// Decode reads a JSON-lines history from r and validates it. Blank lines are
// skipped; an unknown field is an error, so a record from a newer format is
// refused rather than half-read.
func Decode(r io.Reader) (History, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	var h History
	for line := 1; sc.Scan(); line++ {
		raw := sc.Bytes()
		if len(raw) == 0 {
			continue
		}
		var op Op
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&op); err != nil {
			return nil, &DecodeError{Line: line, Err: err}
		}
		h = append(h, op)
	}
	if err := sc.Err(); err != nil {
		return nil, &DecodeError{Err: err}
	}
	if err := h.Validate(); err != nil {
		return nil, err
	}
	return h, nil
}
