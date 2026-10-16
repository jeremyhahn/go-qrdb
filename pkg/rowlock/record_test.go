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

package rowlock

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
)

const (
	ownerA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	ownerB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	ownerC = "cccccccccccccccccccccccccccccccc"
)

func rec(owner string, mode Mode, rev int64) record {
	return record{key: "k", owner: owner, mode: mode, createRev: rev, rawKey: recordKey("k", owner, mode)}
}

func TestEvaluate_GrantRule(t *testing.T) {
	cases := []struct {
		name  string
		queue []record
		owner string
		mode  Mode
		want  grantState
	}{
		{"alone", []record{rec(ownerA, ModeExclusive, 1)}, ownerA, ModeExclusive, stateGranted},
		{"behind exclusive", []record{rec(ownerA, ModeExclusive, 1), rec(ownerB, ModeExclusive, 2)}, ownerB, ModeExclusive, stateWaiting},
		{"shared behind shared", []record{rec(ownerA, ModeShared, 1), rec(ownerB, ModeShared, 2)}, ownerB, ModeShared, stateGranted},
		{"exclusive behind shared", []record{rec(ownerA, ModeShared, 1), rec(ownerB, ModeExclusive, 2)}, ownerB, ModeExclusive, stateWaiting},
		{"shared behind waiting exclusive", []record{rec(ownerA, ModeShared, 1), rec(ownerB, ModeExclusive, 2), rec(ownerC, ModeShared, 3)}, ownerC, ModeShared, stateWaiting},
		{"own earlier record ignored", []record{rec(ownerA, ModeShared, 1), rec(ownerA, ModeExclusive, 2)}, ownerA, ModeExclusive, stateGranted},
		{"ahead of later records", []record{rec(ownerA, ModeExclusive, 1), rec(ownerB, ModeExclusive, 2)}, ownerA, ModeExclusive, stateGranted},
		{"missing", []record{rec(ownerA, ModeExclusive, 1)}, ownerB, ModeExclusive, stateMissing},
		{"missing behind incompatible", []record{rec(ownerA, ModeExclusive, 1)}, ownerB, ModeShared, stateMissing},
		{"empty", nil, ownerA, ModeShared, stateMissing},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, evaluate(c.queue, c.owner, c.mode))
		})
	}
}

func TestSortQueue_OrdersByCreateRevisionThenKey(t *testing.T) {
	q := []record{rec(ownerB, ModeShared, 5), rec(ownerA, ModeShared, 5), rec(ownerC, ModeExclusive, 2)}
	sortQueue(q)
	assert.Equal(t, []string{ownerC, ownerA, ownerB}, []string{q[0].owner, q[1].owner, q[2].owner})
}

func TestWaitsFor_AndClosesCycle(t *testing.T) {
	recs := []record{
		{key: "x", owner: ownerA, mode: ModeExclusive, createRev: 1},
		{key: "y", owner: ownerB, mode: ModeExclusive, createRev: 2},
		{key: "y", owner: ownerA, mode: ModeExclusive, createRev: 3},
		{key: "x", owner: ownerB, mode: ModeExclusive, createRev: 4},
		{key: "z", owner: ownerC, mode: ModeShared, createRev: 5},
		{key: "z", owner: ownerA, mode: ModeShared, createRev: 6},
	}
	edges := waitsFor(recs)
	assert.Equal(t, int64(3), edges[ownerA][ownerB], "an edge appears with its waiter's record")
	assert.Equal(t, int64(4), edges[ownerB][ownerA])
	assert.NotContains(t, edges[ownerA], ownerC, "shared behind shared is no edge")
	assert.False(t, closesCycle(edges, ownerA), "A is on the cycle but did not close it")
	assert.True(t, closesCycle(edges, ownerB), "B's edge is the cycle's newest")
	assert.False(t, closesCycle(edges, ownerC))
}

func TestWaitsFor_LabelsEachEdgeWithTheRevisionThatCreatedIt(t *testing.T) {
	edges := waitsFor([]record{
		{key: "x", owner: ownerA, mode: ModeExclusive, createRev: 1},
		{key: "y", owner: ownerB, mode: ModeExclusive, createRev: 2},
		{key: "y", owner: ownerA, mode: ModeExclusive, createRev: 3},
		{key: "x", owner: ownerB, mode: ModeExclusive, createRev: 4},
		{key: "z", owner: ownerC, mode: ModeShared, createRev: 5},
		{key: "z", owner: ownerA, mode: ModeShared, createRev: 6},
		{key: "w", owner: ownerC, mode: ModeExclusive, createRev: 7},
		{key: "v", owner: ownerC, mode: ModeExclusive, createRev: 8},
		{key: "v", owner: ownerB, mode: ModeShared, createRev: 9},
		{key: "w", owner: ownerB, mode: ModeShared, createRev: 10},
	})
	assert.Equal(t, int64(3), edges[ownerA][ownerB])
	assert.Equal(t, int64(4), edges[ownerB][ownerA])
	assert.NotContains(t, edges[ownerA], ownerC, "shared behind shared is no edge")
	assert.Equal(t, int64(9), edges[ownerB][ownerC], "an edge given twice exists from the older record")
}

func TestClosesCycle_OnlyTheOwnerOfTheNewestEdgeClosesIt(t *testing.T) {
	two := waitsFor([]record{
		{key: "x", owner: ownerA, mode: ModeExclusive, createRev: 1},
		{key: "y", owner: ownerB, mode: ModeExclusive, createRev: 2},
		{key: "y", owner: ownerA, mode: ModeExclusive, createRev: 3},
		{key: "x", owner: ownerB, mode: ModeExclusive, createRev: 4},
	})
	assert.True(t, closesCycle(two, ownerB), "B's request closed the cycle")
	assert.False(t, closesCycle(two, ownerA), "A is on the cycle but did not close it")

	three := waitsFor([]record{
		{key: "x", owner: ownerA, mode: ModeExclusive, createRev: 1},
		{key: "y", owner: ownerB, mode: ModeExclusive, createRev: 2},
		{key: "z", owner: ownerC, mode: ModeExclusive, createRev: 3},
		{key: "y", owner: ownerA, mode: ModeExclusive, createRev: 4},
		{key: "x", owner: ownerC, mode: ModeExclusive, createRev: 5},
		{key: "z", owner: ownerB, mode: ModeExclusive, createRev: 6},
	})
	assert.True(t, closesCycle(three, ownerB))
	assert.False(t, closesCycle(three, ownerA))
	assert.False(t, closesCycle(three, ownerC))
}

func TestClosesCycle_JudgesEachOfTheOwnersBatchesByItsOwnRevision(t *testing.T) {
	// A's request was written in two batches, at 3 and 6. B's request at 5
	// closed a cycle with A's first batch, so B is the victim even though
	// A's second batch is newer.
	edges := waitsFor([]record{
		{key: "x", owner: ownerA, mode: ModeExclusive, createRev: 1},
		{key: "y", owner: ownerB, mode: ModeExclusive, createRev: 2},
		{key: "y", owner: ownerA, mode: ModeExclusive, createRev: 3},
		{key: "w", owner: ownerC, mode: ModeExclusive, createRev: 4},
		{key: "x", owner: ownerB, mode: ModeExclusive, createRev: 5},
		{key: "w", owner: ownerA, mode: ModeExclusive, createRev: 6},
	})
	assert.True(t, closesCycle(edges, ownerB))
	assert.False(t, closesCycle(edges, ownerA))
	assert.False(t, closesCycle(edges, ownerC))
}

func TestClosesCycle_AChainIsNoCycle(t *testing.T) {
	chain := waitsFor([]record{
		{key: "x", owner: ownerA, mode: ModeExclusive, createRev: 1},
		{key: "x", owner: ownerB, mode: ModeExclusive, createRev: 2},
		{key: "x", owner: ownerC, mode: ModeExclusive, createRev: 3},
	})
	for _, o := range []string{ownerA, ownerB, ownerC} {
		assert.False(t, closesCycle(chain, o), "a chain is not a cycle")
	}
	assert.False(t, closesCycle(chain, "nobody"), "an owner with no edges closes nothing")
}

func TestWaitsFor_EdgeIsDatedByTheWaitersOldestRecord(t *testing.T) {
	edges := waitsFor([]record{
		{key: "x", owner: ownerB, mode: ModeExclusive, createRev: 1},
		{key: "y", owner: ownerB, mode: ModeExclusive, createRev: 2},
		{key: "y", owner: ownerA, mode: ModeExclusive, createRev: 3},
		{key: "x", owner: ownerA, mode: ModeExclusive, createRev: 5},
	})
	assert.Equal(t, int64(3), edges[ownerA][ownerB])
}

// Exactly one owner closes a cycle, wherever the newest edge sits in it, and
// an owner that closed one cycle is its victim even if it lies on an older
// cycle as well.
func TestClosesCycle_OneVictimPerCycle(t *testing.T) {
	ring := map[string]map[string]int64{
		ownerA: {ownerB: 1},
		ownerB: {ownerC: 7},
		ownerC: {ownerA: 4},
	}
	var victims []string
	for _, o := range []string{ownerA, ownerB, ownerC} {
		if closesCycle(ring, o) {
			victims = append(victims, o)
		}
	}
	assert.Equal(t, []string{ownerB}, victims)

	twoCycles := map[string]map[string]int64{
		ownerA: {ownerB: 2, ownerC: 9},
		ownerB: {ownerA: 5},
		ownerC: {ownerA: 3},
	}
	assert.True(t, closesCycle(twoCycles, ownerA), "A closed A-C, though B closed A-B")
	assert.True(t, closesCycle(twoCycles, ownerB))
	assert.False(t, closesCycle(twoCycles, ownerC))
}

func TestRecord_RoundTrip(t *testing.T) {
	key := "__tbl:shop/1/0/\x00binary/with/slashes"
	raw := recordKey(key, ownerA, ModeExclusive)
	assert.True(t, strings.HasPrefix(raw, queuePrefix(key)))
	assert.True(t, strings.HasPrefix(queuePrefix(key), Prefix))

	r, err := parseRecord(&kv.KeyValue{Key: []byte(raw), Value: encodeValue(ModeExclusive, 42), CreateRevision: 7})
	require.NoError(t, err)
	assert.Equal(t, key, r.key)
	assert.Equal(t, ownerA, r.owner)
	assert.Equal(t, ModeExclusive, r.mode)
	assert.Equal(t, int64(7), r.createRev)
}

func TestRecord_QueuePrefixIsUnambiguous(t *testing.T) {
	// "a" and "a/b" must never share a queue: the length prefix keeps one
	// key's queue from being a prefix of another's.
	assert.False(t, strings.HasPrefix(queuePrefix("a/b"), queuePrefix("a")))
}

func TestParseRecord_RejectsCorruptRecords(t *testing.T) {
	good := recordKey("k", ownerA, ModeShared)
	val := encodeValue(ModeShared, 1)
	cases := map[string]*kv.KeyValue{
		"foreign prefix":  {Key: []byte("__sys:other/" + good), Value: val},
		"short":           {Key: []byte(Prefix + "00"), Value: val},
		"bad length":      {Key: []byte(Prefix + "zzzzzzzz" + good[len(Prefix)+keyLenDigits:]), Value: val},
		"length mismatch": {Key: []byte(strings.Replace(good, hexLen(1), hexLen(2), 1)), Value: val},
		"bad separator":   {Key: []byte(good[:len(good)-2] + "_s"), Value: val},
		"bad mode char":   {Key: []byte(good[:len(good)-1] + "q"), Value: val},
		"short value":     {Key: []byte(good), Value: val[:3]},
		"bad version":     {Key: []byte(good), Value: append([]byte{9}, val[1:]...)},
		"mode mismatch":   {Key: []byte(good), Value: encodeValue(ModeExclusive, 1)},
	}
	for name, kvp := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parseRecord(kvp)
			var corrupt *CorruptRecordError
			require.ErrorAs(t, err, &corrupt)
		})
	}
}

func TestMode_String(t *testing.T) {
	assert.Equal(t, "shared", ModeShared.String())
	assert.Equal(t, "exclusive", ModeExclusive.String())
	assert.Equal(t, "Mode(9)", Mode(9).String())
}

func TestPrefixEnd(t *testing.T) {
	assert.Equal(t, []byte("ac"), prefixEnd("ab"))
	assert.Equal(t, []byte("b"), prefixEnd("a\xff"))
	assert.Nil(t, prefixEnd("\xff\xff"))
}
