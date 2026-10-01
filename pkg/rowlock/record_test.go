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

func TestWaitsFor_AndOnCycle(t *testing.T) {
	recs := []record{
		{key: "x", owner: ownerA, mode: ModeExclusive, createRev: 1},
		{key: "y", owner: ownerB, mode: ModeExclusive, createRev: 2},
		{key: "y", owner: ownerA, mode: ModeExclusive, createRev: 3},
		{key: "x", owner: ownerB, mode: ModeExclusive, createRev: 4},
		{key: "z", owner: ownerC, mode: ModeShared, createRev: 5},
		{key: "z", owner: ownerA, mode: ModeShared, createRev: 6},
	}
	edges := waitsFor(recs)
	assert.Contains(t, edges[ownerA], ownerB)
	assert.Contains(t, edges[ownerB], ownerA)
	assert.NotContains(t, edges[ownerA], ownerC, "shared behind shared is no edge")
	assert.True(t, onCycle(edges, ownerA))
	assert.True(t, onCycle(edges, ownerB))
	assert.False(t, onCycle(edges, ownerC))

	chain := waitsFor([]record{
		{key: "x", owner: ownerA, mode: ModeExclusive, createRev: 1},
		{key: "x", owner: ownerB, mode: ModeExclusive, createRev: 2},
		{key: "x", owner: ownerC, mode: ModeExclusive, createRev: 3},
	})
	for _, o := range []string{ownerA, ownerB, ownerC} {
		assert.False(t, onCycle(chain, o), "a chain is not a cycle")
	}
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
