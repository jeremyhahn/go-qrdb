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

package storage_test

import (
	"bytes"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// TestBatchRangeDeleteReadParity extends the read-your-own-writes contract in
// batch_parity_test.go to range deletes, as a differential oracle. Each seed
// commits a random base to every Batch implementation, then buffers the same
// random interleaving of puts, deletes and range deletes in one batch, and
// requires each batch to read exactly what a model of the same ops applied in
// order reads: point gets, scans in both directions, bounded scans, keys-only
// scans and seeks followed by moves that change direction. The same
// transcript is required again of the storage after the batch commits.
//
// The partition manager's merge iterator does not move backward (Prev reports
// no entry: each partition holds a hash-scattered subset of the keys), so it is
// held to the forward half of the transcript.
func TestBatchRangeDeleteReadParity(t *testing.T) {
	forwardOnly := map[string]bool{"partition": true}
	for seed := int64(1); seed <= 12; seed++ {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed)) // #nosec G404 -- fixed-seed property-test fixture, not a cryptographic use
			base := randomBase(rng)
			ops := randomBatchOps(rng, seed)

			model := newBatchModel(base)
			for _, op := range ops {
				model.apply(op)
			}

			for _, b := range newBatchParityBackends(t) {
				bidirectional := !forwardOnly[b.name]
				want := readTranscript(model, bidirectional)
				for k, v := range base {
					if err := b.s.Set([]byte(k), []byte(v)); err != nil {
						t.Fatalf("%s: seed base: %v", b.name, err)
					}
				}
				batch := b.s.Batch()
				for _, op := range ops {
					if err := op.stage(batch); err != nil {
						t.Fatalf("%s: stage %s: %v", b.name, op, err)
					}
				}
				diffTranscripts(t, b.name+" batch", readTranscript(batch, bidirectional), want, ops)
				if err := batch.Commit(); err != nil {
					t.Fatalf("%s: commit: %v", b.name, err)
				}
				_ = batch.Close()
				diffTranscripts(t, b.name+" committed", readTranscript(b.s, bidirectional), want, ops)
			}
		})
	}
}

// parityUniverse is every key a seed can write; reads also probe keys
// between and around them.
func parityUniverse() []string {
	keys := make([]string, 30)
	for i := range keys {
		keys[i] = fmt.Sprintf("k%02d", i)
	}
	return keys
}

func randomBase(rng *rand.Rand) map[string]string {
	base := make(map[string]string)
	for _, k := range parityUniverse() {
		if rng.Intn(10) < 6 {
			base[k] = "base-" + k
		}
	}
	return base
}

// batchOp is one buffered write: a put, a delete, or a range delete of
// [key, end) where an empty end is unbounded.
type batchOp struct {
	kind  string
	key   string
	end   string
	value string
}

func (op batchOp) String() string {
	switch op.kind {
	case "put":
		return "put " + op.key + "=" + op.value
	case "delete":
		return "delete " + op.key
	default:
		return "range [" + op.key + ", " + op.end + ")"
	}
}

func (op batchOp) stage(b storage.Batch) error {
	switch op.kind {
	case "put":
		return b.Set([]byte(op.key), []byte(op.value))
	case "delete":
		return b.Delete([]byte(op.key))
	default:
		var end []byte
		if op.end != "" {
			end = []byte(op.end)
		}
		return b.DeleteRange([]byte(op.key), end)
	}
}

// randomBatchOps draws 40 ops. Range bounds fall on and between universe keys
// ("k07x" sorts between k07 and k08), a tenth of them unbounded above.
func randomBatchOps(rng *rand.Rand, seed int64) []batchOp {
	universe := parityUniverse()
	pick := func() string { return universe[rng.Intn(len(universe))] }
	bound := func() string {
		if rng.Intn(3) == 0 {
			return pick() + "x"
		}
		return pick()
	}
	ops := make([]batchOp, 40)
	for i := range ops {
		switch r := rng.Intn(20); {
		case r < 9:
			ops[i] = batchOp{kind: "put", key: pick(), value: fmt.Sprintf("s%d-op%d", seed, i)}
		case r < 13:
			ops[i] = batchOp{kind: "delete", key: pick()}
		default:
			start, end := bound(), bound()
			for end <= start {
				start, end = bound(), bound()
			}
			if rng.Intn(10) == 0 {
				end = ""
			}
			ops[i] = batchOp{kind: "range", key: start, end: end}
		}
	}
	return ops
}

// readTranscript records everything a reader shows: a get of every universe
// key and of keys between them, scans forward and back over the whole
// keyspace and over [k07, k23), a keys-only scan (keys alone: OnlyKeys is a
// hint pebble does not take), and from each seek target a walk that changes
// direction twice. A walk stops at the first move that leaves the range, since
// backends differ on what a move from there does. A reader that is not
// bidirectional is asked only for gets, forward scans and forward walks.
func readTranscript(r storage.Reader, bidirectional bool) []string {
	var tr []string
	for _, k := range parityUniverse() {
		for _, probe := range []string{k, k + "x"} {
			v, err := r.Get([]byte(probe))
			switch {
			case err != nil:
				tr = append(tr, "get "+probe+" ERR "+err.Error())
			case v == nil:
				tr = append(tr, "get "+probe+" <nil>")
			default:
				tr = append(tr, "get "+probe+"="+string(v))
			}
		}
	}

	bounded := &storage.IteratorOptions{LowerBound: []byte("k07"), UpperBound: []byte("k23")}
	for _, scan := range []struct {
		name     string
		opts     *storage.IteratorOptions
		backward bool
	}{
		{"forward", &storage.IteratorOptions{}, false},
		{"backward", &storage.IteratorOptions{}, true},
		{"bounded forward", bounded, false},
		{"bounded backward", bounded, true},
		{"keys only", &storage.IteratorOptions{OnlyKeys: true}, false},
	} {
		if scan.backward && !bidirectional {
			continue
		}
		it := r.NewIterator(scan.opts)
		var pairs []string
		ok := it.SeekToFirst()
		if scan.backward {
			ok = it.SeekToLast()
		}
		for ; ok && it.Valid(); ok = step(it, scan.backward) {
			if scan.opts.OnlyKeys {
				pairs = append(pairs, string(it.Key()))
				continue
			}
			pairs = append(pairs, string(it.Key())+"="+string(it.Value()))
		}
		if err := it.Error(); err != nil {
			pairs = append(pairs, "ERR "+err.Error())
		}
		_ = it.Close()
		tr = append(tr, scan.name+" "+strings.Join(pairs, " "))
	}

	moves := []string{"next", "next", "prev", "prev", "prev", "next"}
	if !bidirectional {
		moves = []string{"next", "next", "next"}
	}
	for _, target := range []string{"a", "k", "k00", "k10", "k10x", "k19", "k29", "k29x", "z"} {
		for _, opts := range []*storage.IteratorOptions{{}, bounded} {
			it := r.NewIterator(opts)
			walk := []string{fmt.Sprintf("seek %s [%s,%s)", target, opts.LowerBound, opts.UpperBound)}
			ok := it.Seek([]byte(target))
			for _, move := range moves {
				if !ok || !it.Valid() {
					walk = append(walk, "<end>")
					break
				}
				walk = append(walk, string(it.Key()))
				if move == "next" {
					ok = it.Next()
				} else {
					ok = it.Prev()
				}
			}
			if ok && it.Valid() {
				walk = append(walk, string(it.Key()))
			}
			_ = it.Close()
			tr = append(tr, strings.Join(walk, " "))
		}
	}
	return tr
}

func diffTranscripts(t *testing.T, name string, got, want []string, ops []batchOp) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d transcript lines, model has %d", name, len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s diverges from the model:\n  got:  %s\n  want: %s\nops: %v", name, got[i], want[i], ops)
		}
	}
}

// batchModel is the specification: a map that applies ops in order, read
// through an iterator over its sorted keys.
type batchModel struct {
	data map[string]string
}

func newBatchModel(base map[string]string) *batchModel {
	m := &batchModel{data: make(map[string]string, len(base))}
	for k, v := range base {
		m.data[k] = v
	}
	return m
}

func (m *batchModel) apply(op batchOp) {
	switch op.kind {
	case "put":
		m.data[op.key] = op.value
	case "delete":
		delete(m.data, op.key)
	default:
		for k := range m.data {
			if k >= op.key && (op.end == "" || k < op.end) {
				delete(m.data, k)
			}
		}
	}
}

func (m *batchModel) Get(key []byte) ([]byte, error) {
	if v, ok := m.data[string(key)]; ok {
		return []byte(v), nil
	}
	return nil, nil
}

func (m *batchModel) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	var keys []string
	for k := range m.data {
		if len(opts.LowerBound) > 0 && k < string(opts.LowerBound) {
			continue
		}
		if len(opts.UpperBound) > 0 && k >= string(opts.UpperBound) {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return &modelIterator{m: m, keys: keys, onlyKeys: opts.OnlyKeys, pos: -1}
}

// modelIterator walks the model's sorted keys. A move from outside the range
// stays outside it; readTranscript never makes one.
type modelIterator struct {
	m        *batchModel
	keys     []string
	onlyKeys bool
	pos      int
}

func (it *modelIterator) at(pos int) bool {
	it.pos = pos
	return it.Valid()
}

func (it *modelIterator) SeekToFirst() bool { return it.at(0) }
func (it *modelIterator) SeekToLast() bool  { return it.at(len(it.keys) - 1) }
func (it *modelIterator) Seek(target []byte) bool {
	return it.at(sort.SearchStrings(it.keys, string(target)))
}
func (it *modelIterator) Next() bool   { return it.Valid() && it.at(it.pos+1) }
func (it *modelIterator) Prev() bool   { return it.Valid() && it.at(it.pos-1) }
func (it *modelIterator) Valid() bool  { return it.pos >= 0 && it.pos < len(it.keys) }
func (it *modelIterator) Error() error { return nil }
func (it *modelIterator) Close() error { return nil }

func (it *modelIterator) Key() []byte {
	if !it.Valid() {
		return nil
	}
	return []byte(it.keys[it.pos])
}

func (it *modelIterator) Value() []byte {
	if !it.Valid() || it.onlyKeys {
		return nil
	}
	return bytes.Clone([]byte(it.m.data[it.keys[it.pos]]))
}
