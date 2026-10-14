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

package ledger

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/test/clusterproof/history"
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

// kinds returns each anomaly kind with the op IDs it names.
func kinds(res history.Result) map[string][]uint64 {
	out := make(map[string][]uint64)
	for _, a := range res.Anomalies {
		out[a.Kind] = append(out[a.Kind], a.OpIDs...)
	}
	return out
}

func wantValid(t *testing.T, h history.History) {
	t.Helper()
	res := check(t, h)
	if !res.Valid || len(res.Anomalies) != 0 {
		t.Fatalf("want valid, got %+v", res)
	}
}

func wantAnomaly(t *testing.T, h history.History, kind string, ids ...uint64) history.Result {
	t.Helper()
	res := check(t, h)
	if res.Valid {
		t.Fatalf("want %s anomaly, got valid result %+v", kind, res)
	}
	got, found := kinds(res)[kind]
	if !found {
		t.Fatalf("want %s anomaly, got %+v", kind, res.Anomalies)
	}
	if !slices.Equal(got, ids) {
		t.Fatalf("%s names ops %v, want %v (all: %+v)", kind, got, ids, res.Anomalies)
	}
	return res
}

func TestCheck_SequentialWritesSurvive(t *testing.T) {
	res := check(t, history.History{
		put(1, "a", 0, 10, ok),
		put(2, "b", 20, 30, ok),
		get(3, "b", 40, 50),
	})
	if !res.Valid || res.Checked != 2 {
		t.Fatalf("want valid with 2 acknowledged writes, got %+v", res)
	}
}

func TestCheck_LostWrite(t *testing.T) {
	// b was acknowledged after a returned, so b is the cluster's last word;
	// reading a afterwards means b is gone.
	res := wantAnomaly(t, history.History{
		put(1, "a", 0, 10, ok),
		put(2, "b", 20, 30, ok),
		get(3, "a", 40, 50),
	}, KindLost, 2, 3)
	if res.Anomalies[0].Value != "a" || res.Anomalies[0].Key != "k" {
		t.Fatalf("lost anomaly does not name what was read: %+v", res.Anomalies[0])
	}
}

func TestCheck_LostWriteToAbsent(t *testing.T) {
	res := wantAnomaly(t, history.History{
		put(1, "a", 0, 10, ok),
		getAbsent(2, 20, 30),
	}, KindLost, 1, 2)
	if res.Anomalies[0].Value != "absent" {
		t.Fatalf("value = %q, want absent", res.Anomalies[0].Value)
	}
}

func TestCheck_PhantomNeverWritten(t *testing.T) {
	res := check(t, history.History{
		put(1, "a", 0, 10, ok),
		get(2, "zzz", 20, 30),
	})
	got := kinds(res)
	if !slices.Equal(got[KindPhantom], []uint64{2}) || !slices.Equal(got[KindLost], []uint64{1, 2}) {
		t.Fatalf("want phantom [2] and lost [1 2], got %+v", res.Anomalies)
	}
}

func TestCheck_PhantomFromFailedWrite(t *testing.T) {
	// A write the client was told certainly failed must not show up.
	wantAnomaly(t, history.History{
		put(1, "a", 0, 10, ok),
		put(2, "b", 20, 30, fail),
		get(3, "a", 40, 50),
		get(4, "b", 60, 70),
	}, KindPhantom, 2, 4)
}

func TestCheck_PhantomFromUnswappedCAS(t *testing.T) {
	wantAnomaly(t, history.History{
		put(1, "a", 0, 10, ok),
		cas(2, "x", "b", false, 20, 30, ok),
		get(3, "b", 40, 50),
	}, KindPhantom, 2, 3)
}

func TestCheck_PhantomReadBeforeWriteCalled(t *testing.T) {
	wantAnomaly(t, history.History{
		get(1, "a", 0, 5),
		put(2, "a", 10, 20, ok),
		get(3, "a", 30, 40),
	}, KindPhantom, 2, 1)
}

func TestCheck_IndeterminateWriteThatApplied(t *testing.T) {
	// The client gave up on b, but the cluster applied it: no loss, no phantom.
	wantValid(t, history.History{
		put(1, "a", 0, 10, ok),
		put(2, "b", 20, 30, indet),
		get(3, "b", 40, 50),
	})
}

func TestCheck_IndeterminateWriteThatDidNotApply(t *testing.T) {
	wantValid(t, history.History{
		put(1, "a", 0, 10, ok),
		put(2, "b", 20, 30, indet),
		get(3, "a", 40, 50),
	})
}

func TestCheck_IndeterminateWriteLandsLate(t *testing.T) {
	// An indeterminate write may apply long after the client stopped waiting,
	// even after a later acknowledged write.
	wantValid(t, history.History{
		put(1, "a", 0, 10, indet),
		put(2, "b", 20, 30, ok),
		get(3, "a", 40, 50),
	})
}

func TestCheck_ConcurrentAcknowledgedWrites(t *testing.T) {
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

func TestCheck_Deletes(t *testing.T) {
	t.Run("acknowledged delete reads absent", func(t *testing.T) {
		wantValid(t, history.History{
			put(1, "a", 0, 10, ok),
			del(2, 20, 30, ok),
			getAbsent(3, 40, 50),
		})
	})
	t.Run("delete then put reads absent", func(t *testing.T) {
		wantAnomaly(t, history.History{
			del(1, 0, 10, ok),
			put(2, "a", 20, 30, ok),
			getAbsent(3, 40, 50),
		}, KindLost, 2, 3)
	})
	t.Run("acknowledged delete reads old value", func(t *testing.T) {
		wantAnomaly(t, history.History{
			put(1, "a", 0, 10, ok),
			del(2, 20, 30, ok),
			get(3, "a", 40, 50),
		}, KindLost, 2, 3)
	})
	t.Run("failed delete reads old value", func(t *testing.T) {
		wantValid(t, history.History{
			put(1, "a", 0, 10, ok),
			del(2, 20, 30, fail),
			get(3, "a", 40, 50),
		})
	})
}

func TestCheck_CAS(t *testing.T) {
	t.Run("swapped value survives", func(t *testing.T) {
		wantValid(t, history.History{
			put(1, "a", 0, 10, ok),
			cas(2, "a", "b", true, 20, 30, ok),
			get(3, "b", 40, 50),
		})
	})
	t.Run("swapped value lost", func(t *testing.T) {
		wantAnomaly(t, history.History{
			put(1, "a", 0, 10, ok),
			cas(2, "a", "b", true, 20, 30, ok),
			get(3, "a", 40, 50),
		}, KindLost, 2, 3)
	})
	t.Run("indeterminate swap applied", func(t *testing.T) {
		wantValid(t, history.History{
			put(1, "a", 0, 10, ok),
			cas(2, "a", "b", false, 20, 30, indet),
			get(3, "b", 40, 50),
		})
	})
}

func TestCheck_DuplicatedWrite(t *testing.T) {
	// a was read, b replaced it and was read, then a came back: a's write
	// landed twice (or a read was stale). The final read also loses b.
	res := check(t, history.History{
		put(1, "a", 0, 10, ok),
		get(2, "a", 20, 30),
		put(3, "b", 40, 50, ok),
		get(4, "b", 60, 70),
		get(5, "a", 80, 90),
	})
	got := kinds(res)
	if !slices.Equal(got[KindDuplicated], []uint64{1, 2, 4, 5}) {
		t.Fatalf("duplicated names %v, want [1 2 4 5] (all: %+v)", got[KindDuplicated], res.Anomalies)
	}
	if !slices.Equal(got[KindLost], []uint64{3, 5}) {
		t.Fatalf("lost names %v, want [3 5]", got[KindLost])
	}
}

func TestCheck_DuplicatedThroughAbsence(t *testing.T) {
	wantAnomaly(t, history.History{
		put(1, "a", 0, 10, indet),
		get(2, "a", 20, 30),
		getAbsent(3, 40, 50),
		get(4, "a", 60, 70),
	}, KindDuplicated, 1, 2, 3, 4)
}

func TestCheck_OverlappingReadsAreNotDuplicates(t *testing.T) {
	// b is written while the reads run. The read of b overlaps the second
	// read of a, so a linearization puts that read of a first -- a, a, b, b
	// -- and nothing was read twice around a different state.
	wantValid(t, history.History{
		put(1, "a", 0, 10, ok),
		put(2, "b", 15, 50, ok),
		get(3, "a", 20, 30),
		get(4, "b", 25, 45),
		get(5, "a", 40, 55),
		get(6, "b", 60, 70),
	})
}

func TestCheck_DuplicateReportedOncePerValue(t *testing.T) {
	res := check(t, history.History{
		put(1, "a", 0, 10, ok),
		put(2, "b", 0, 10, ok),
		get(3, "a", 20, 30),
		get(4, "b", 40, 50),
		get(5, "a", 60, 70),
		get(6, "a", 80, 90),
	})
	if n := len(kinds(res)[KindDuplicated]); n != 4 {
		t.Fatalf("duplicated reported %d op ids, want one finding of 4: %+v", n, res.Anomalies)
	}
}

func TestCheck_UnreadKey(t *testing.T) {
	// The only read started before the write returned, so it cannot be the
	// final read: the key is unchecked, and that is not a pass.
	wantAnomaly(t, history.History{
		put(1, "a", 0, 10, ok),
		get(2, "a", 5, 15),
	}, KindUnread, 1)
}

func TestCheck_Vacuous(t *testing.T) {
	res := wantAnomaly(t, history.History{
		put(1, "a", 0, 10, indet),
		get(2, "a", 20, 30),
	}, KindVacuous)
	if res.Checked != 0 {
		t.Fatalf("Checked = %d, want 0", res.Checked)
	}
	if r := check(t, nil); r.Valid {
		t.Fatal("an empty history must not pass")
	}
}

func TestCheck_ReadOnlyKeyNeedsNoFinalRead(t *testing.T) {
	h := history.History{
		put(1, "a", 0, 10, ok),
		get(2, "a", 20, 30),
		{ID: 3, Kind: history.KindGet, Key: "other", Output: &history.Output{}, CallNS: 0, ReturnNS: 1, Outcome: ok},
	}
	wantValid(t, h)
}

func TestCheck_KeysAreIndependent(t *testing.T) {
	// A value written to one key and read on another is a phantom there.
	h := history.History{
		put(1, "a", 0, 10, ok),
		get(2, "a", 20, 30),
		{ID: 3, Kind: history.KindGet, Key: "other", Output: &history.Output{Value: "a", Found: true}, CallNS: 20, ReturnNS: 30, Outcome: ok},
	}
	res := check(t, h)
	if len(res.Anomalies) != 1 || res.Anomalies[0].Kind != KindPhantom || res.Anomalies[0].Key != "other" {
		t.Fatalf("want one phantom on key other, got %+v", res.Anomalies)
	}
}

func TestCheck_AmbiguousValue(t *testing.T) {
	_, err := Checker{}.Check(history.History{
		put(1, "a", 0, 10, ok),
		put(2, "a", 20, 30, ok),
	})
	var ae *AmbiguousValueError
	if !errors.As(err, &ae) || !slices.Equal(ae.OpIDs, []uint64{1, 2}) {
		t.Fatalf("err = %v, want AmbiguousValueError naming ops 1 and 2", err)
	}
	if !strings.Contains(ae.Error(), `"a"`) || !strings.Contains(ae.Error(), "1 2") {
		t.Errorf("error %q does not name the value and ops", ae.Error())
	}
}

func TestCheck_InvalidHistory(t *testing.T) {
	_, err := Checker{}.Check(history.History{put(1, "a", 10, 0, ok)})
	var ie *history.InvalidOpError
	if !errors.As(err, &ie) {
		t.Fatalf("err = %v, want *history.InvalidOpError", err)
	}
}
