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

package sdk_test

import (
	"context"
	"strconv"
	"sync"
	"testing"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/mvcchost"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// These tests drive GuardedSwap through the REAL stack — the embedded
// transport over the repository's own TransactionService and MVCCKVService,
// applied by a real MVCC state machine — rather than the in-package fake
// plane.
//
// They exist because a fake cannot refuse what it was never taught to refuse.
// An earlier revision of GuardedSwap read through the transaction plane, by
// putting an OpGet in a transaction's success and failure branches, and every
// fake-plane test passed: the fake implemented those reads. The real service
// does not and never did — pkg/service's opTypeMap carries OpPut and OpDelete
// only, and encodeServiceOp fails closed on anything else with
// UnsupportedTransactionOpError before the command reaches Raft. GuardedSwap
// would therefore have failed on its very first read against every real
// transport while passing its whole unit suite (go-qrdb#44).
//
// The lesson generalises past this bug: a helper built on a plane it only ever
// meets through a double needs at least one test against the plane itself.

// newRealPlane wires the embedded transport over the real services.
func newRealPlane(t *testing.T) *embedded.Transport {
	t.Helper()

	host := mvcchost.New(t)
	tr, err := embedded.New(
		host.KV(),
		host.Admin(),
		embedded.WithMVCCService(host.MVCC()),
		embedded.WithTransactionService(host.Transaction()),
		embedded.WithBatchService(host.Batch()),
	)
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	return tr
}

// seed writes a record through the real MVCC service.
func seed(t *testing.T, tr *embedded.Transport, key string, value []byte) {
	t.Helper()
	if err := tr.Put(context.Background(), key, value); err != nil {
		t.Fatalf("seed %q: %v", key, err)
	}
}

// A guarded swap of an EXISTING record lands against the real services.
//
// This is the test the transaction-plane read would have failed outright: the
// initial read alone returned UnsupportedTransactionOpError, before any
// compare was evaluated.
func TestGuardedSwap_AgainstRealServices_SwapsAnExistingRecord(t *testing.T) {
	tr := newRealPlane(t)
	seed(t, tr, "counter", []byte("1"))

	res, err := sdk.GuardedSwap(context.Background(), tr, sdk.GuardedSwapRequest{
		Key: []byte("counter"),
		Decide: func(current []byte, exists bool) ([]byte, sdk.SwapDecision, error) {
			if !exists {
				t.Errorf("Decide saw the seeded record as absent")
			}
			n, cerr := strconv.Atoi(string(current))
			if cerr != nil {
				return nil, sdk.SwapWrite, cerr
			}
			return []byte(strconv.Itoa(n + 1)), sdk.SwapWrite, nil
		},
	})
	if err != nil {
		t.Fatalf("GuardedSwap: %v", err)
	}
	if !res.Written {
		t.Fatal("GuardedSwap reported no write")
	}

	got, gerr := tr.Get(context.Background(), "counter")
	if gerr != nil {
		t.Fatalf("Get: %v", gerr)
	}
	if string(got) != "2" {
		t.Errorf("record = %q, want %q", got, "2")
	}
}

// A guarded swap of an ABSENT record creates it, and the create guard is
// evaluated by the real state machine rather than by a fake's arithmetic.
func TestGuardedSwap_AgainstRealServices_CreatesAnAbsentRecord(t *testing.T) {
	tr := newRealPlane(t)

	res, err := sdk.GuardedSwap(context.Background(), tr, sdk.GuardedSwapRequest{
		Key: []byte("fresh"),
		Decide: func(current []byte, exists bool) ([]byte, sdk.SwapDecision, error) {
			if exists {
				t.Errorf("Decide saw an absent record as present, holding %q", current)
			}
			return []byte("first"), sdk.SwapWrite, nil
		},
	})
	if err != nil {
		t.Fatalf("GuardedSwap: %v", err)
	}
	if !res.Written {
		t.Fatal("GuardedSwap reported no write")
	}

	got, gerr := tr.Get(context.Background(), "fresh")
	if gerr != nil {
		t.Fatalf("Get: %v", gerr)
	}
	if string(got) != "first" {
		t.Errorf("record = %q, want %q", got, "first")
	}
}

// Concurrent guarded increments against the real services lose nothing.
//
// Every increment must survive: with N writers each adding one, a lost update
// shows up as a final value below N. This is the property the whole helper
// exists to provide, asserted here against the state machine that actually
// evaluates the compares.
func TestGuardedSwap_AgainstRealServices_ConcurrentIncrementsLoseNothing(t *testing.T) {
	tr := newRealPlane(t)
	seed(t, tr, "tally", []byte("0"))

	const writers = 8
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := sdk.GuardedSwap(context.Background(), tr, sdk.GuardedSwapRequest{
				Key:         []byte("tally"),
				MaxAttempts: writers * 4,
				Decide: func(current []byte, _ bool) ([]byte, sdk.SwapDecision, error) {
					n, cerr := strconv.Atoi(string(current))
					if cerr != nil {
						return nil, sdk.SwapWrite, cerr
					}
					return []byte(strconv.Itoa(n + 1)), sdk.SwapWrite, nil
				},
			})
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("GuardedSwap: %v", err)
	}

	got, gerr := tr.Get(context.Background(), "tally")
	if gerr != nil {
		t.Fatalf("Get: %v", gerr)
	}
	if string(got) != strconv.Itoa(writers) {
		t.Errorf("tally = %q, want %q — a lost update means the guard did not hold",
			got, strconv.Itoa(writers))
	}
}

// The real transaction service refuses a read in a transaction branch.
//
// This is the constraint that shapes GuardedSwap, pinned directly so that a
// future change putting reads back into a branch fails HERE, naming the
// reason, rather than in a distant integration test that merely stops working.
func TestRealTransactionService_RefusesAReadInABranch(t *testing.T) {
	tr := newRealPlane(t)
	seed(t, tr, "k", []byte("v"))

	for _, branch := range []string{"success", "failure"} {
		t.Run(branch, func(t *testing.T) {
			req := &transport.TransactionRequest{
				Compares: []transport.Compare{{
					Key:    []byte("k"),
					Target: transport.CompareValue,
					Result: transport.CompareEqual,
					Value:  []byte("v"),
				}},
			}
			op := transport.Op{Type: transport.OpGet, Key: []byte("k")}
			if branch == "success" {
				req.SuccessOps = []transport.Op{op}
			} else {
				req.FailureOps = []transport.Op{op}
			}

			if _, err := tr.ExecuteTransaction(context.Background(), req); err == nil {
				t.Fatalf("a transaction read in the %s branch was ACCEPTED; "+
					"GuardedSwap's design depends on it being refused", branch)
			}
		})
	}
}
