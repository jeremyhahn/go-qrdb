// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package storage

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
)

func TestMergeSpans(t *testing.T) {
	span := func(start, end string) tombstoneSpan { return tombstoneSpan{start: []byte(start), end: []byte(end)} }
	cases := map[string]struct {
		in, want []tombstoneSpan
	}{
		"none":                    {nil, nil},
		"one":                     {[]tombstoneSpan{span("b", "d")}, []tombstoneSpan{span("b", "d")}},
		"disjoint are sorted":     {[]tombstoneSpan{span("m", "p"), span("b", "d")}, []tombstoneSpan{span("b", "d"), span("m", "p")}},
		"overlapping coalesce":    {[]tombstoneSpan{span("b", "f"), span("d", "h")}, []tombstoneSpan{span("b", "h")}},
		"touching coalesce":       {[]tombstoneSpan{span("d", "h"), span("b", "d")}, []tombstoneSpan{span("b", "h")}},
		"contained is absorbed":   {[]tombstoneSpan{span("b", "p"), span("d", "f")}, []tombstoneSpan{span("b", "p")}},
		"unbounded absorbs later": {[]tombstoneSpan{span("x", "z"), span("c", ""), span("a", "b")}, []tombstoneSpan{span("a", "b"), span("c", "")}},
		"unbounded extends":       {[]tombstoneSpan{span("b", "d"), span("c", "")}, []tombstoneSpan{span("b", "")}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := mergeSpans(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("mergeSpans = %q, want %q", got, tc.want)
			}
			for i := range got {
				if string(got[i].start) != string(tc.want[i].start) || string(got[i].end) != string(tc.want[i].end) {
					t.Fatalf("mergeSpans = %q, want %q", got, tc.want)
				}
			}
		})
	}
}

func TestCoveringSpan(t *testing.T) {
	spans := mergeSpans([]tombstoneSpan{
		{start: []byte("b"), end: []byte("d")},
		{start: []byte("m"), end: nil},
	})
	cases := map[string]bool{"a": false, "b": true, "c": true, "d": false, "l": false, "m": true, "zzz": true}
	for key, want := range cases {
		if _, got := coveringSpan(spans, []byte(key)); got != want {
			t.Errorf("coveringSpan(%q) = %v, want %v", key, got, want)
		}
	}
}

// TestBufferedBatch_ReverseIteratorMatchesModel holds the Reverse iterator to
// the order it has always had -- SeekToFirst on the largest key, Next toward
// smaller ones, Prev back up, Seek on the largest key at or below its target --
// over random interleavings of puts, deletes and range deletes, by comparing it
// with the sorted visible keys of the same ops applied to a copy of the base.
func TestBufferedBatch_ReverseIteratorMatchesModel(t *testing.T) {
	keys := make([]string, 24)
	for i := range keys {
		keys[i] = fmt.Sprintf("k%02d", i)
	}
	for seed := int64(1); seed <= 20; seed++ {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed)) // #nosec G404 -- fixed-seed property-test fixture, not a cryptographic use
			pick := func() []byte { return []byte(keys[rng.Intn(len(keys))]) }
			initial := make(map[string]string)
			for _, k := range keys {
				if rng.Intn(2) == 0 {
					initial[k] = "base-" + k
				}
			}
			base, model := newMapReader(initial), newMapReader(initial)
			b := NewBufferedBatch(base, base.apply)
			var ops []BufferedOp
			for i := 0; i < 30; i++ {
				var op BufferedOp
				switch rng.Intn(4) {
				case 0, 1:
					op = BufferedOp{Key: pick(), Value: []byte(fmt.Sprintf("op%d", i))}
					mustNil(t, b.Set(op.Key, op.Value))
				case 2:
					op = BufferedOp{Key: pick(), Deleted: true}
					mustNil(t, b.Delete(op.Key))
				default:
					start, end := pick(), pick()
					if string(end) <= string(start) {
						end = nil
					}
					op = BufferedOp{Key: start, RangeEnd: end, Range: true, Deleted: true}
					mustNil(t, b.DeleteRange(start, end))
				}
				ops = append(ops, op)
			}
			mustNil(t, model.apply(ops))

			var asc []string
			for k := range model.data {
				asc = append(asc, k)
			}
			sort.Strings(asc)
			desc := make([]string, len(asc))
			for i, k := range asc {
				desc[len(asc)-1-i] = k
			}

			it := b.NewIterator(&IteratorOptions{Reverse: true})
			defer func() { _ = it.Close() }()
			var got []string
			for ok := it.SeekToFirst(); ok && it.Valid(); ok = it.Next() {
				if v := string(it.Value()); v != model.data[string(it.Key())] {
					t.Fatalf("value of %s = %q, want %q", it.Key(), v, model.data[string(it.Key())])
				}
				got = append(got, string(it.Key()))
			}
			if fmt.Sprint(got) != fmt.Sprint(desc) {
				t.Fatalf("Reverse SeekToFirst+Next = %v, want %v (ops %+v)", got, desc, ops)
			}
			got = nil
			for ok := it.SeekToLast(); ok && it.Valid(); ok = it.Prev() {
				got = append(got, string(it.Key()))
			}
			if fmt.Sprint(got) != fmt.Sprint(asc) {
				t.Fatalf("Reverse SeekToLast+Prev = %v, want %v", got, asc)
			}

			for _, target := range append(keys, "a", "k05x", "z") {
				// The largest visible key <= target, then one Next (smaller)
				// and two Prev (back up past it).
				i := sort.SearchStrings(asc, target)
				if i == len(asc) || asc[i] != target {
					i--
				}
				var want []string
				for _, j := range []int{i, i - 1, i} {
					if j < 0 {
						break
					}
					want = append(want, asc[j])
				}
				if i+1 < len(asc) && i >= 1 {
					want = append(want, asc[i+1])
				}
				got = nil
				if it.Seek([]byte(target)) && it.Valid() {
					got = append(got, string(it.Key()))
					for _, move := range []func() bool{it.Next, it.Prev, it.Prev} {
						if !move() || !it.Valid() {
							break
						}
						got = append(got, string(it.Key()))
					}
				}
				if fmt.Sprint(got) != fmt.Sprint(want) {
					t.Fatalf("Reverse Seek(%s) walk = %v, want %v over %v", target, got, want, asc)
				}
			}
		})
	}
}

// countingReader wraps a Reader and counts the single-step moves made on the
// iterators it opens.
type countingReader struct {
	Reader
	steps int
}

func (r *countingReader) NewIterator(opts *IteratorOptions) Iterator {
	return &countingIterator{Iterator: r.Reader.NewIterator(opts), steps: &r.steps}
}

type countingIterator struct {
	Iterator
	steps *int
}

func (it *countingIterator) Next() bool { *it.steps++; return it.Iterator.Next() }
func (it *countingIterator) Prev() bool { *it.steps++; return it.Iterator.Prev() }

// TestBufferedBatch_IteratorSeeksPastRangeDelete pins the cost the merge exists
// for: a scan across a pending range delete jumps the base past the range with
// one seek instead of stepping through every committed key it covers, in both
// directions.
func TestBufferedBatch_IteratorSeeksPastRangeDelete(t *testing.T) {
	const n = 10_000
	pairs := make(map[string]string, n)
	for i := 0; i < n; i++ {
		pairs[fmt.Sprintf("k%05d", i)] = "v"
	}
	base := &countingReader{Reader: newMapReader(pairs)}
	b := NewBufferedBatch(base, func([]BufferedOp) error { return nil })
	mustNil(t, b.DeleteRange([]byte("k00002"), []byte("k09998")))
	mustNil(t, b.Set([]byte("k05000"), []byte("kept")))

	want := []string{"k00000", "k00001", "k05000", "k09998", "k09999"}
	for _, backward := range []bool{false, true} {
		base.steps = 0
		it := b.NewIterator(nil)
		var got []string
		ok, move := it.SeekToFirst(), it.Next
		if backward {
			ok, move = it.SeekToLast(), it.Prev
		}
		for ; ok && it.Valid(); ok = move() {
			got = append(got, string(it.Key()))
		}
		_ = it.Close()
		if backward {
			sort.Strings(got)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("backward=%v: keys = %v, want %v", backward, got, want)
		}
		if base.steps > 10 {
			t.Fatalf("backward=%v: the scan stepped the base %d times across a range of %d deleted keys", backward, base.steps, n-4)
		}
	}
}

// TestBufferedBatch_IteratorExhaustedAndClosed pins the overlay iterator's
// edges: an exhausted iterator yields no key or value and will not step, and a
// closed one closes once and refuses every reposition.
func TestBufferedBatch_IteratorExhaustedAndClosed(t *testing.T) {
	b := NewBufferedBatch(newMapReader(map[string]string{"a": "1", "b": "2", "c": "3"}),
		func([]BufferedOp) error { return nil })
	mustNil(t, b.DeleteRange([]byte("b"), []byte("c")))

	for _, reverse := range []bool{false, true} {
		it := b.NewIterator(&IteratorOptions{Reverse: reverse})
		for ok := it.SeekToFirst(); ok; ok = it.Next() {
		}
		if it.Valid() || it.Key() != nil || it.Value() != nil {
			t.Fatalf("reverse=%v: exhausted iterator: valid=%v key=%q value=%q, want invalid and nil", reverse, it.Valid(), it.Key(), it.Value())
		}
		if it.Next() || it.Prev() {
			t.Fatalf("reverse=%v: an exhausted iterator stepped", reverse)
		}

		mustNil(t, it.Close())
		mustNil(t, it.Close())
		if it.SeekToFirst() || it.SeekToLast() || it.Seek([]byte("a")) || it.Valid() {
			t.Fatalf("reverse=%v: a closed iterator repositioned", reverse)
		}
	}
}
