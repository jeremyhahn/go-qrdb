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

package sdk

import (
	"bytes"
	"context"
	"fmt"
	"sync"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// errPlaneReadOp is what both fakes return for a read operation in a
// transaction branch.
//
// The real service refuses those: pkg/service's opTypeMap carries OpPut and
// OpDelete only, and encodeServiceOp fails closed on anything else before the
// command reaches Raft. An earlier revision of GuardedSwap read through the
// transaction plane and passed every test here, because these fakes had
// helpfully IMPLEMENTED the reads production rejects — a fake more permissive
// than the thing it stands in for tests a store that does not exist. They now
// refuse the same operations for the same reason, and
// guardedswap_service_test.go pins the behaviour against the real service.
var errPlaneReadOp = fmt.Errorf("fake plane: transaction branches carry writes only")

// fakeTxnPlane is an in-memory transaction plane that evaluates compares the
// way the real state machine does: atomically, against the record as it stands
// at the moment the transaction is applied.
//
// It exists because the property under test is a RACE, and a mock that returns
// canned responses cannot exhibit one. Every attempt here takes the same lock
// the record is written under, so N concurrent GuardedSwap calls interleave
// exactly as they would against a replicated store — which is what makes
// "exactly one winner" a real assertion rather than a restatement of the mock's
// script.
type fakeTxnPlane struct {
	mu       sync.Mutex
	records  map[string]*fakeRecord
	revision int64

	// calls counts ExecuteTransaction invocations and reads counts
	// GetRevision invocations, so a test can assert how many round trips of
	// each kind a swap actually cost.
	calls int
	reads int

	// The read gate makes the race DETERMINISTIC. Without it these tests are
	// vacuous: an in-memory plane is fast enough that the first writer usually
	// finishes its whole read-decide-write cycle before the next one starts, so
	// no two writers are ever in flight together and "exactly one winner" holds
	// whether or not a guard is present. Measured — with the gate removed,
	// deleting the guard entirely still passed.
	//
	// The gate holds the first gateN reads, so every writer has seen the record
	// in its pre-write state before any of them may write. From there only the
	// guard can decide who wins.
	gateMu   sync.Mutex
	gateN    int
	gateSeen int
	gateCh   chan struct{}
}

// gateReads arms the read gate for n concurrent callers.
func (p *fakeTxnPlane) gateReads(n int) {
	p.gateMu.Lock()
	defer p.gateMu.Unlock()
	p.gateN = n
	p.gateCh = make(chan struct{})
}

// gate blocks until every gated reader has arrived. It must be called with no
// plane lock held, or the readers deadlock against each other.
func (p *fakeTxnPlane) gate() {
	p.gateMu.Lock()
	if p.gateCh == nil {
		p.gateMu.Unlock()
		return
	}
	ch := p.gateCh
	p.gateSeen++
	if p.gateSeen == p.gateN {
		// Everyone has read; release them together and disarm, so later
		// attempts in a retry loop are not gated too.
		p.gateCh = nil
		p.gateMu.Unlock()
		close(ch)
		return
	}
	p.gateMu.Unlock()
	<-ch
}

type fakeRecord struct {
	value          []byte
	createRevision int64
	modRevision    int64
}

func newFakeTxnPlane() *fakeTxnPlane {
	return &fakeTxnPlane{records: make(map[string]*fakeRecord)}
}

// put seeds a record without going through a transaction.
func (p *fakeTxnPlane) put(key string, value []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.revision++
	rec, ok := p.records[key]
	if !ok {
		p.records[key] = &fakeRecord{
			value:          append([]byte(nil), value...),
			createRevision: p.revision,
			modRevision:    p.revision,
		}
		return
	}
	rec.value = append([]byte(nil), value...)
	rec.modRevision = p.revision
}

// get returns a copy of a record's value.
func (p *fakeTxnPlane) get(key string) ([]byte, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	rec, ok := p.records[key]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), rec.value...), true
}

func (p *fakeTxnPlane) ExecuteTransaction(
	_ context.Context,
	req *transport.TransactionRequest,
	opts ...transport.WriteOption,
) (*transport.TransactionResponse, error) {
	return p.execLocked(req, opts...)
}

// GetRevision is the read half of SwapPlane, reporting a missing key the way
// the transports do — with *transport.KeyNotFoundError.
func (p *fakeTxnPlane) GetRevision(
	_ context.Context,
	key string,
	_ ...transport.ReadOption,
) (*transport.MVCCKeyValue, error) {
	p.mu.Lock()
	p.reads++
	rec, ok := p.records[key]
	var kv *transport.MVCCKeyValue
	if ok {
		kv = &transport.MVCCKeyValue{
			Key:            []byte(key),
			Value:          append([]byte(nil), rec.value...),
			CreateRevision: rec.createRevision,
			ModRevision:    rec.modRevision,
		}
	}
	p.mu.Unlock()

	// Gate AFTER the record has been read and the plane lock released, so
	// every gated caller holds the same pre-write view and none of them blocks
	// the others' reads.
	p.gate()

	if kv == nil {
		return nil, &transport.KeyNotFoundError{Key: key}
	}
	return kv, nil
}

func (p *fakeTxnPlane) execLocked(
	req *transport.TransactionRequest,
	_ ...transport.WriteOption,
) (*transport.TransactionResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++

	// Fail closed on branch reads BEFORE evaluating anything, exactly as the
	// real encoder does — it rejects the command without reaching Raft, so no
	// compare is evaluated and nothing is applied.
	for _, op := range append(append([]transport.Op(nil), req.SuccessOps...), req.FailureOps...) {
		if op.Type != transport.OpPut && op.Type != transport.OpDelete {
			return nil, errPlaneReadOp
		}
	}

	succeeded := true
	for _, cmp := range req.Compares {
		if !p.evaluate(cmp) {
			succeeded = false
			break
		}
	}

	ops := req.SuccessOps
	if !succeeded {
		ops = req.FailureOps
	}

	resp := &transport.TransactionResponse{Succeeded: succeeded}
	for _, op := range ops {
		switch op.Type {
		case transport.OpPut:
			p.revision++
			key := string(op.Key)
			rec, ok := p.records[key]
			if !ok {
				p.records[key] = &fakeRecord{
					value:          append([]byte(nil), op.Value...),
					createRevision: p.revision,
					modRevision:    p.revision,
				}
			} else {
				rec.value = append([]byte(nil), op.Value...)
				rec.modRevision = p.revision
			}
			resp.Revision = p.revision
			resp.Responses = append(resp.Responses, transport.OpResponse{Succeeded: true})
		default:
			resp.Responses = append(resp.Responses, transport.OpResponse{})
		}
	}
	if resp.Revision == 0 {
		resp.Revision = p.revision
	}
	return resp, nil
}

// evaluate applies one compare against the record as it stands. Caller holds
// p.mu.
func (p *fakeTxnPlane) evaluate(cmp transport.Compare) bool {
	rec, exists := p.records[string(cmp.Key)]

	var actual int64
	switch cmp.Target {
	case transport.CompareCreateRevision:
		if exists {
			actual = rec.createRevision
		}
		return compareInt64(actual, cmp.CreateRevision, cmp.Result)
	case transport.CompareModRevision:
		if exists {
			actual = rec.modRevision
		}
		return compareInt64(actual, cmp.ModRevision, cmp.Result)
	case transport.CompareValue:
		var have []byte
		if exists {
			have = rec.value
		}
		equal := bytes.Equal(have, cmp.Value)
		if cmp.Result == transport.CompareNotEqual {
			return !equal
		}
		return equal
	default:
		return false
	}
}

func compareInt64(actual, want int64, result transport.CompareResult) bool {
	switch result {
	case transport.CompareEqual:
		return actual == want
	case transport.CompareNotEqual:
		return actual != want
	case transport.CompareGreater:
		return actual > want
	case transport.CompareLess:
		return actual < want
	default:
		return false
	}
}

// refusingPlane always refuses the guard and serves a value the test chooses,
// so the stall and contention paths can be driven deterministically rather
// than provoked by timing.
type refusingPlane struct {
	mu    sync.Mutex
	calls int
	reads int
	// serve returns the value the read reports for read n (1-based).
	// Returning ok=false reports the key as absent.
	serve func(read int) (value []byte, modRevision int64, ok bool)
}

func (p *refusingPlane) ExecuteTransaction(
	_ context.Context,
	req *transport.TransactionRequest,
	_ ...transport.WriteOption,
) (*transport.TransactionResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++

	for _, op := range append(append([]transport.Op(nil), req.SuccessOps...), req.FailureOps...) {
		if op.Type != transport.OpPut && op.Type != transport.OpDelete {
			return nil, errPlaneReadOp
		}
	}

	// Every guard is refused; that is the whole point of this plane.
	return &transport.TransactionResponse{Succeeded: false, Revision: int64(p.calls)}, nil
}

func (p *refusingPlane) GetRevision(
	_ context.Context,
	key string,
	_ ...transport.ReadOption,
) (*transport.MVCCKeyValue, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reads++

	value, modRev, ok := p.serve(p.reads)
	if !ok {
		return nil, &transport.KeyNotFoundError{Key: key}
	}
	return &transport.MVCCKeyValue{
		Key:         []byte(key),
		Value:       value,
		ModRevision: modRev,
	}, nil
}
