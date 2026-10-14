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

package linearizability

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/anishathalye/porcupine"

	"github.com/jeremyhahn/go-qrdb/test/clusterproof/history"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/ledger"
)

// The builders below hand-construct histories on key "k". Times are
// [call, return] on the harness clock.

func put(id uint64, v string, call, ret int64, out history.Outcome) history.Op {
	return history.Op{ID: id, Kind: history.KindPut, Key: "k", Input: &history.Input{Value: v}, CallNS: call, ReturnNS: ret, Outcome: out}
}

func del(id uint64, call, ret int64, out history.Outcome) history.Op {
	return history.Op{ID: id, Kind: history.KindDelete, Key: "k", CallNS: call, ReturnNS: ret, Outcome: out}
}

func cas(id uint64, expected, next string, swapped bool, call, ret int64, out history.Outcome) history.Op {
	op := history.Op{ID: id, Kind: history.KindCAS, Key: "k", Input: &history.Input{Value: next, Expected: expected}, CallNS: call, ReturnNS: ret, Outcome: out}
	if out == history.OutcomeOK {
		op.Output = &history.Output{Swapped: swapped}
	}
	return op
}

func get(id uint64, v string, call, ret int64) history.Op {
	return history.Op{ID: id, Kind: history.KindGet, Key: "k", Output: &history.Output{Value: v, Found: true}, CallNS: call, ReturnNS: ret, Outcome: history.OutcomeOK}
}

func getAbsent(id uint64, call, ret int64) history.Op {
	return history.Op{ID: id, Kind: history.KindGet, Key: "k", Output: &history.Output{}, CallNS: call, ReturnNS: ret, Outcome: history.OutcomeOK}
}

func on(key string, op history.Op) history.Op {
	op.Key = key
	return op
}

const (
	ok    = history.OutcomeOK
	fail  = history.OutcomeFail
	indet = history.OutcomeIndeterminate
)

func check(t *testing.T, h history.History) history.Result {
	t.Helper()
	res, err := Checker{}.Check(h)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if res.Checker != Name {
		t.Fatalf("Checker = %q, want %q", res.Checker, Name)
	}
	return res
}

func wantValid(t *testing.T, h history.History) history.Result {
	t.Helper()
	res := check(t, h)
	if !res.Valid || len(res.Anomalies) != 0 {
		t.Fatalf("want valid, got %+v", res)
	}
	return res
}

// wantRejected asserts the history has exactly one non-linearizable key, k,
// left at value with the named operations as the ones that could not follow.
func wantRejected(t *testing.T, h history.History, value string, ids ...uint64) {
	t.Helper()
	res := check(t, h)
	if res.Valid {
		t.Fatalf("want rejected, got valid result %+v", res)
	}
	want := []history.Anomaly{{Kind: KindNonLinearizable, Key: "k", Value: value, OpIDs: ids}}
	got := slices.Clone(res.Anomalies)
	for i := range got {
		got[i].Detail = ""
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("anomalies = %+v, want %+v", res.Anomalies, want)
	}
	if res.Anomalies[0].Detail == "" {
		t.Fatal("anomaly carries no detail")
	}
}

// ledgerValid asserts the acknowledged-write ledger passes h, so a rejection
// here is something only the linearizability check catches.
func ledgerValid(t *testing.T, h history.History) {
	t.Helper()
	res, err := ledger.Checker{}.Check(h)
	if err != nil || !res.Valid {
		t.Fatalf("ledger: %+v, %v; the history must pass the ledger", res, err)
	}
}

func TestCheck_SequentialHistoryIsLinearizable(t *testing.T) {
	res := wantValid(t, history.History{
		put(1, "a", 0, 10, ok),
		get(2, "a", 20, 30),
		cas(3, "a", "b", true, 40, 50, ok),
		get(4, "b", 60, 70),
		del(5, 80, 90, ok),
		getAbsent(6, 100, 110),
	})
	if res.Checked != 6 {
		t.Fatalf("Checked = %d, want 6", res.Checked)
	}
}

func TestCheck_AcceptsLinearizableHistories(t *testing.T) {
	cases := map[string]struct {
		h       history.History
		checked int
	}{
		"sequential put, read, delete, read": {history.History{
			put(1, "a", 0, 10, ok),
			get(2, "a", 20, 30),
			del(3, 40, 50, ok),
			getAbsent(4, 60, 70),
		}, 4},
		"a read concurrent with an overwrite sees the old value": {history.History{
			put(1, "a", 0, 10, ok),
			put(2, "b", 20, 40, ok),
			get(3, "a", 25, 35),
			get(4, "b", 50, 60),
		}, 4},
		"a read concurrent with an overwrite sees the new value": {history.History{
			put(1, "a", 0, 10, ok),
			put(2, "b", 20, 40, ok),
			get(3, "b", 25, 35),
			get(4, "b", 50, 60),
		}, 4},
		"an indeterminate write that applied long after it was abandoned": {history.History{
			put(1, "a", 0, 10, ok),
			put(2, "b", 20, 30, indet),
			get(3, "a", 40, 50),
			get(4, "b", 60, 70),
		}, 3},
		"an indeterminate write that never applied": {history.History{
			put(1, "a", 0, 10, ok),
			put(2, "b", 20, 30, indet),
			get(3, "a", 40, 50),
			get(4, "a", 60, 70),
		}, 3},
		"an indeterminate swap that applied": {history.History{
			put(1, "a", 0, 10, ok),
			cas(2, "a", "b", false, 20, 30, indet),
			get(3, "b", 40, 50),
		}, 2},
		"an indeterminate swap whose comparand had changed": {history.History{
			put(1, "a", 0, 10, ok),
			put(2, "b", 20, 30, ok),
			cas(3, "a", "c", false, 40, 50, indet),
			get(4, "b", 60, 70),
		}, 3},
		"swapped and unswapped compare-and-swap": {history.History{
			put(1, "a", 0, 10, ok),
			cas(2, "a", "b", true, 20, 30, ok),
			cas(3, "a", "c", false, 40, 50, ok),
			get(4, "b", 60, 70),
		}, 4},
		"a swap on an absent key does not swap": {history.History{
			cas(1, "a", "b", false, 0, 10, ok),
			getAbsent(2, 20, 30),
		}, 2},
		"failed operations and unanswered reads are dropped": {history.History{
			put(1, "a", 0, 10, ok),
			put(2, "b", 20, 30, fail),
			{ID: 3, Kind: history.KindGet, Key: "k", CallNS: 40, ReturnNS: 50, Outcome: indet},
			{ID: 4, Kind: history.KindGet, Key: "k", CallNS: 40, ReturnNS: 50, Outcome: fail},
			get(5, "a", 60, 70),
		}, 2},
		"keys are independent registers": {history.History{
			on("x", put(1, "a", 0, 10, ok)),
			on("y", put(2, "b", 20, 30, ok)),
			on("x", get(3, "a", 40, 50)),
			on("y", get(4, "b", 40, 50)),
		}, 4},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			res := wantValid(t, tc.h)
			if res.Checked != tc.checked {
				t.Fatalf("Checked = %d, want %d", res.Checked, tc.checked)
			}
		})
	}
}

func TestCheck_RejectsNonLinearizableHistories(t *testing.T) {
	cases := map[string]struct {
		h     history.History
		value string
		next  uint64
	}{
		// The read a lease that outlived a leader change would serve: called
		// after the overwrite was acknowledged, it returns the old value.
		"a stale read after an acknowledged overwrite": {history.History{
			put(1, "a", 0, 10, ok),
			put(2, "b", 20, 30, ok),
			get(3, "a", 40, 50),
		}, "b", 3},
		"an acknowledged write lost": {history.History{
			put(1, "a", 0, 10, ok),
			getAbsent(2, 20, 30),
		}, "a", 2},
		"a value no operation wrote": {history.History{
			put(1, "a", 0, 10, ok),
			get(2, "z", 20, 30),
		}, "a", 2},
		"the value of a failed write": {history.History{
			put(1, "a", 0, 10, ok),
			put(2, "b", 20, 30, fail),
			get(3, "b", 40, 50),
		}, "a", 3},
		"a value read before its write was called": {history.History{
			get(1, "a", 0, 10),
			put(2, "a", 20, 30, ok),
		}, absent, 1},
		"a deleted value read again": {history.History{
			put(1, "a", 0, 10, ok),
			del(2, 20, 30, ok),
			get(3, "a", 40, 50),
		}, absent, 3},
		"a swap that succeeded against the wrong value": {history.History{
			put(1, "a", 0, 10, ok),
			put(2, "b", 20, 30, ok),
			cas(3, "a", "c", true, 40, 50, ok),
		}, "b", 3},
		"a swap that failed against the right value": {history.History{
			put(1, "a", 0, 10, ok),
			cas(2, "a", "b", false, 20, 30, ok),
		}, "a", 2},
		// The flip-flop the ledger calls duplicated: two reads that each
		// fit, and a third that brings back the first value.
		"a value read, superseded and read again": {history.History{
			put(1, "a", 0, 10, ok),
			put(2, "b", 20, 100, ok),
			get(3, "a", 30, 40),
			get(4, "b", 50, 60),
			get(5, "a", 70, 80),
		}, "b", 5},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			wantRejected(t, tc.h, tc.value, tc.next)
		})
	}
}

func TestCheck_StaleReadTheLedgerMisses(t *testing.T) {
	// a is read after b was acknowledged, then c is written and read. Data
	// survived, so the ledger passes, but that read of a was stale.
	h := history.History{
		put(1, "a", 0, 10, ok),
		put(2, "b", 20, 30, ok),
		get(3, "a", 40, 50),
		put(4, "c", 60, 70, ok),
		get(5, "c", 80, 90),
	}
	ledgerValid(t, h)
	wantRejected(t, h, "b", 3)
}

func TestCheck_StaleReadBeforeTheFinalReadTheLedgerMisses(t *testing.T) {
	// The final read sees b, so the ledger finds b survived; the read of a
	// after b was acknowledged is the stale read a lease that outlived a
	// leader change would serve.
	h := history.History{
		put(1, "a", 0, 10, ok),
		put(2, "b", 20, 30, ok),
		get(3, "a", 40, 50),
		get(4, "b", 60, 70),
	}
	ledgerValid(t, h)
	wantRejected(t, h, "b", 3)
}

func TestCheck_ReadCannotGoBackwards(t *testing.T) {
	// b's write is still in flight. Once a read has seen b, a later read
	// cannot see a again.
	wantRejected(t, history.History{
		put(1, "a", 0, 10, ok),
		put(2, "b", 20, 100, ok),
		get(3, "b", 30, 40),
		get(4, "a", 50, 60),
	}, "b", 4)
}

func TestCheck_LostWrite(t *testing.T) {
	wantRejected(t, history.History{
		put(1, "a", 0, 10, ok),
		getAbsent(2, 20, 30),
	}, "a", 2)
}

func TestCheck_FailedWriteMustNotShow(t *testing.T) {
	wantRejected(t, history.History{
		put(1, "a", 0, 10, ok),
		put(2, "b", 20, 30, fail),
		get(3, "b", 40, 50),
	}, "a", 3)
}

func TestCheck_DeleteMustTakeEffect(t *testing.T) {
	wantRejected(t, history.History{
		put(1, "a", 0, 10, ok),
		del(2, 20, 30, ok),
		get(3, "a", 40, 50),
	}, absent, 3)
}

func TestCheck_CAS(t *testing.T) {
	t.Run("swap reported on a mismatched value", func(t *testing.T) {
		wantRejected(t, history.History{
			put(1, "a", 0, 10, ok),
			cas(2, "x", "b", true, 20, 30, ok),
		}, "a", 2)
	})
	t.Run("no swap reported on a matching value", func(t *testing.T) {
		wantRejected(t, history.History{
			put(1, "a", 0, 10, ok),
			cas(2, "a", "b", false, 20, 30, ok),
		}, "a", 2)
	})
	t.Run("swap reported on an absent key", func(t *testing.T) {
		wantRejected(t, history.History{
			cas(1, "x", "b", true, 0, 10, ok),
		}, absent, 1)
	})
	t.Run("unswapped value must not show", func(t *testing.T) {
		wantRejected(t, history.History{
			put(1, "a", 0, 10, ok),
			cas(2, "x", "b", false, 20, 30, ok),
			get(3, "b", 40, 50),
		}, "a", 3)
	})
	t.Run("unswapped CAS on a mismatched value", func(t *testing.T) {
		wantValid(t, history.History{
			put(1, "a", 0, 10, ok),
			cas(2, "x", "b", false, 20, 30, ok),
			get(3, "a", 40, 50),
		})
	})
	t.Run("acknowledged CAS without an output", func(t *testing.T) {
		unknown := cas(2, "a", "b", false, 20, 30, ok)
		unknown.Output = nil
		wantValid(t, history.History{put(1, "a", 0, 10, ok), unknown, get(3, "b", 40, 50)})
		wantRejected(t, history.History{put(1, "a", 0, 10, ok), unknown, get(3, "a", 40, 50)}, "b", 3)
	})
	t.Run("indeterminate swap applied", func(t *testing.T) {
		wantValid(t, history.History{
			put(1, "a", 0, 10, ok),
			cas(2, "a", "b", false, 20, 30, indet),
			get(3, "b", 40, 50),
		})
	})
	t.Run("indeterminate swap that cannot apply", func(t *testing.T) {
		// The CAS steps as a no-op, so only the read is left unexplained.
		wantRejected(t, history.History{
			put(1, "a", 0, 10, ok),
			cas(2, "x", "b", false, 20, 30, indet),
			get(3, "b", 40, 50),
		}, "a", 3)
	})
	t.Run("failed swap without a comparand is ignored", func(t *testing.T) {
		wantValid(t, history.History{
			put(1, "a", 0, 10, ok),
			cas(2, "", "b", false, 20, 30, fail),
			get(3, "a", 40, 50),
		})
	})
}

func TestCheck_IndeterminateWrites(t *testing.T) {
	t.Run("applied", func(t *testing.T) {
		wantValid(t, history.History{
			put(1, "a", 0, 10, ok),
			put(2, "b", 20, 30, indet),
			get(3, "b", 40, 50),
		})
	})
	t.Run("never applied", func(t *testing.T) {
		wantValid(t, history.History{
			put(1, "a", 0, 10, ok),
			put(2, "b", 20, 30, indet),
			get(3, "a", 40, 50),
		})
	})
	t.Run("lands after a later acknowledged write", func(t *testing.T) {
		wantValid(t, history.History{
			put(1, "a", 0, 10, indet),
			put(2, "b", 20, 30, ok),
			get(3, "b", 40, 50),
			get(4, "a", 60, 70),
		})
	})
	t.Run("cannot apply before its call", func(t *testing.T) {
		wantRejected(t, history.History{
			put(1, "a", 0, 10, ok),
			get(2, "b", 20, 30),
			put(3, "b", 40, 50, indet),
		}, "a", 2)
	})
	t.Run("delete", func(t *testing.T) {
		wantValid(t, history.History{
			put(1, "a", 0, 10, ok),
			del(2, 20, 30, indet),
			get(3, "a", 40, 50),
			getAbsent(4, 60, 70),
		})
	})
}

func TestCheck_ConcurrentWritesEitherOrder(t *testing.T) {
	for _, final := range []string{"a", "b"} {
		t.Run(final, func(t *testing.T) {
			wantValid(t, history.History{
				put(1, "a", 0, 20, ok),
				put(2, "b", 5, 25, ok),
				get(3, final, 30, 40),
			})
		})
	}
}

func TestCheck_IgnoresWhatObservedNothing(t *testing.T) {
	// A failed delete did not apply, and an indeterminate read saw nothing.
	res := wantValid(t, history.History{
		put(1, "a", 0, 10, ok),
		del(2, 20, 30, fail),
		{ID: 3, Kind: history.KindGet, Key: "k", CallNS: 40, ReturnNS: 50, Outcome: indet},
		get(4, "a", 60, 70),
	})
	if res.Checked != 2 {
		t.Fatalf("Checked = %d, want 2", res.Checked)
	}
}

func TestCheck_FrontierNamesEveryCandidate(t *testing.T) {
	// Neither concurrent read can follow the write, and each could have
	// come next, so both are named.
	wantRejected(t, history.History{
		put(1, "a", 0, 10, ok),
		get(2, "b", 20, 30),
		getAbsent(3, 25, 35),
		get(4, "a", 40, 50),
	}, "a", 2, 3)
}

func TestCheck_KeysAreIndependent(t *testing.T) {
	res := check(t, history.History{
		on("z", put(1, "a", 0, 10, ok)),
		on("z", getAbsent(2, 20, 30)),
		put(3, "a", 0, 10, ok),
		on("m", put(4, "a", 0, 10, ok)),
		on("m", get(5, "b", 20, 30)),
		get(6, "a", 20, 30),
	})
	var keys []string
	for _, a := range res.Anomalies {
		keys = append(keys, a.Key)
	}
	if res.Valid || !slices.Equal(keys, []string{"m", "z"}) {
		t.Fatalf("want anomalies on m then z only, got %+v", res.Anomalies)
	}
	if res.Checked != 6 {
		t.Fatalf("Checked = %d, want 6", res.Checked)
	}
}

func TestCheck_ReportsKeysInOrder(t *testing.T) {
	res := check(t, history.History{
		on("y", put(1, "a", 0, 10, ok)),
		on("y", getAbsent(2, 20, 30)),
		on("x", put(3, "b", 0, 10, ok)),
		on("x", getAbsent(4, 20, 30)),
		on("z", put(5, "c", 0, 10, ok)),
		on("z", get(6, "c", 20, 30)),
	})
	if res.Valid || len(res.Anomalies) != 2 || res.Anomalies[0].Key != "x" || res.Anomalies[1].Key != "y" {
		t.Fatalf("want x then y, got %+v", res.Anomalies)
	}
	if res.Checked != 6 {
		t.Fatalf("Checked = %d, want 6", res.Checked)
	}
}

func TestCheck_Deterministic(t *testing.T) {
	// Many writes overlap, so the search reaches several partial
	// linearizations of the same length; the evidence must not depend on
	// which one porcupine reports first.
	h := history.History{
		put(1, "a", 0, 100, ok),
		put(2, "b", 0, 100, ok),
		put(3, "c", 0, 100, ok),
		put(4, "d", 0, 100, ok),
		get(5, "x", 110, 120),
	}
	first := check(t, h)
	for range 20 {
		if again := check(t, h); !reflect.DeepEqual(again, first) {
			t.Fatalf("verdict changed between runs:\n%+v\n%+v", first, again)
		}
	}
	if first.Valid || !slices.Equal(first.Anomalies[0].OpIDs, []uint64{5}) {
		t.Fatalf("want op 5 named, got %+v", first.Anomalies)
	}
}

func TestCheck_Vacuous(t *testing.T) {
	for name, h := range map[string]history.History{
		"empty": nil,
		"nothing acknowledged": {
			put(1, "a", 0, 10, indet),
			put(2, "b", 20, 30, fail),
		},
		"nothing with a known effect": {
			put(1, "a", 0, 10, fail),
			{ID: 2, Kind: history.KindGet, Key: "k", CallNS: 20, ReturnNS: 30, Outcome: indet},
		},
	} {
		t.Run(name, func(t *testing.T) {
			res := check(t, h)
			if res.Valid || res.Checked != 0 || len(res.Anomalies) != 1 || res.Anomalies[0].Kind != KindVacuous {
				t.Fatalf("want a vacuous failure, got %+v", res)
			}
		})
	}
}

func TestCheck_InvalidHistory(t *testing.T) {
	for name, h := range map[string]history.History{
		"return before call": {put(1, "a", 10, 0, ok)},
		"duplicate id":       {put(1, "a", 0, 10, ok), put(1, "b", 20, 30, ok)},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Checker{}.Check(h)
			var ie *history.InvalidOpError
			if !errors.As(err, &ie) {
				t.Fatalf("err = %v, want *history.InvalidOpError", err)
			}
		})
	}
}

func TestCheck_RefusesASwapWithoutAComparand(t *testing.T) {
	for name, h := range map[string]history.History{
		"acknowledged":  {cas(7, "", "b", true, 0, 10, ok)},
		"indeterminate": {put(1, "a", 0, 10, ok), cas(7, "", "b", false, 20, 30, indet)},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Checker{}.Check(h)
			var ae *AmbiguousCASError
			if !errors.As(err, &ae) {
				t.Fatalf("err = %v, want *AmbiguousCASError", err)
			}
			if ae.ID != 7 || ae.Error() == "" {
				t.Fatalf("AmbiguousCASError = %+v, want ID 7 and a message", ae)
			}
		})
	}
}

func TestCheck_Timeout(t *testing.T) {
	var got []time.Duration
	search = func(_ porcupine.Model, _ []porcupine.Operation, timeout time.Duration) (porcupine.CheckResult, porcupine.LinearizationInfo) {
		got = append(got, timeout)
		return porcupine.Unknown, porcupine.LinearizationInfo{}
	}
	t.Cleanup(func() { search = porcupine.CheckOperationsVerbose })
	h := history.History{put(1, "a", 0, 10, ok), get(2, "a", 20, 30)}

	for _, tc := range []struct {
		c    Checker
		want time.Duration
	}{
		{Checker{}, DefaultTimeout},
		{Checker{Timeout: time.Second}, time.Second},
	} {
		got = nil
		_, err := tc.c.Check(h)
		var te *TimeoutError
		if !errors.As(err, &te) {
			t.Fatalf("Check error = %v, want *TimeoutError", err)
		}
		if te.Key != "k" || te.Ops != 2 || te.Timeout != tc.want || te.Error() == "" {
			t.Fatalf("TimeoutError = %+v, want key k, 2 ops, %v", te, tc.want)
		}
		if !slices.Equal(got, []time.Duration{tc.want}) {
			t.Fatalf("search timeouts = %v, want [%v]", got, tc.want)
		}
	}
}

func TestStep(t *testing.T) {
	a := register{value: "a", present: true}
	tests := []struct {
		name  string
		r     register
		in    input
		out   *history.Output
		legal bool
		next  register
	}{
		{"put", register{}, input{kind: history.KindPut, value: "a"}, nil, true, a},
		{"delete", a, input{kind: history.KindDelete}, nil, true, register{}},
		{"cas swaps", a, input{kind: history.KindCAS, value: "b", expected: "a"}, &history.Output{Swapped: true}, true, register{value: "b", present: true}},
		{"cas unknown swaps", a, input{kind: history.KindCAS, value: "b", expected: "a"}, nil, true, register{value: "b", present: true}},
		{"cas mismatch holds", a, input{kind: history.KindCAS, value: "b", expected: "x"}, nil, true, a},
		{"cas absent holds", register{}, input{kind: history.KindCAS, value: "b"}, &history.Output{}, true, register{}},
		{"cas absent cannot swap", register{}, input{kind: history.KindCAS, value: "b"}, &history.Output{Swapped: true}, false, register{}},
		{"get value", a, input{kind: history.KindGet}, &history.Output{Value: "a", Found: true}, true, a},
		{"get absent", register{}, input{kind: history.KindGet}, &history.Output{}, true, register{}},
		{"get wrong value", a, input{kind: history.KindGet}, &history.Output{Value: "b", Found: true}, false, a},
		{"get absent on present", a, input{kind: history.KindGet}, &history.Output{}, false, a},
		{"get value on absent", register{}, input{kind: history.KindGet}, &history.Output{Found: true}, false, register{}},
		{"get without an output", a, input{kind: history.KindGet}, nil, false, a},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			legal, next := step(tc.r, tc.in, tc.out)
			if legal != tc.legal || next != tc.next {
				t.Fatalf("step = %v, %+v; want %v, %+v", legal, next, tc.legal, tc.next)
			}
		})
	}
}

func TestModelStep(t *testing.T) {
	a := register{value: "a", present: true}
	put := input{kind: history.KindPut, value: "a"}
	tests := []struct {
		name           string
		state, in, out interface{}
		legal          bool
		next           interface{}
	}{
		{"steps the register", register{}, put, (*history.Output)(nil), true, a},
		{"foreign state", "a", put, (*history.Output)(nil), false, "a"},
		{"foreign input", register{}, "put", (*history.Output)(nil), false, register{}},
		{"foreign output", register{}, put, history.Output{}, false, register{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			legal, next := model.Step(tc.state, tc.in, tc.out)
			if legal != tc.legal || next != tc.next {
				t.Fatalf("Step = %v, %+v; want %v, %+v", legal, next, tc.legal, tc.next)
			}
		})
	}
}

func TestLongestBreaksTiesLexically(t *testing.T) {
	got := longest([][][]int{{{2, 0}, {0, 1, 3}, {0, 2}, {0, 1, 2}}})
	if !slices.Equal(got, []int{0, 1, 2}) {
		t.Fatalf("longest = %v, want [0 1 2]", got)
	}
	if got := longest(nil); got != nil {
		t.Fatalf("longest(nil) = %v, want nil", got)
	}
}
