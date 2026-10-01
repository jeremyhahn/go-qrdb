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
	"encoding/binary"
	"sort"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
)

// Mode is a lock mode.
type Mode uint8

const (
	// ModeShared is a shared (S) lock: compatible with other shared locks.
	ModeShared Mode = 1

	// ModeExclusive is an exclusive (X) lock: compatible with nothing.
	ModeExclusive Mode = 2
)

// modeChars is each mode's one-letter spelling in a record key; modeNames
// its spelling in metrics and messages. Maps, not switches: a mode added
// without an entry is a missing key the tests catch.
var (
	modeChars = map[Mode]byte{ModeShared: 's', ModeExclusive: 'x'}
	modeNames = map[Mode]string{ModeShared: "shared", ModeExclusive: "exclusive"}
)

// modesByChar inverts modeChars for decoding.
var modesByChar = func() map[byte]Mode {
	out := make(map[byte]Mode, len(modeChars))
	for m, c := range modeChars {
		out[c] = m
	}
	return out
}()

// String returns the mode's name.
func (m Mode) String() string {
	if name, ok := modeNames[m]; ok {
		return name
	}
	return "Mode(" + strconv.Itoa(int(m)) + ")"
}

// valid reports whether m is one of the package's modes.
func (m Mode) valid() bool {
	_, ok := modeChars[m]
	return ok
}

// compatible reports whether a lock of mode held by one owner can coexist
// with a lock of mode want held by another: shared with shared, and nothing
// else.
func compatible(held, want Mode) bool {
	return held == ModeShared && want == ModeShared
}

// Prefix is the internal-key prefix every lock record lives under. It is
// under "__sys:", which the internal-key guard keeps out of user key
// enumeration, MVCC restore and the SQL kv table.
const Prefix = "__sys:rowlock/"

const (
	// recordVersion is the first byte of every record value this version
	// writes.
	recordVersion = 0x01

	// recordValueLen is the length of a record value: version, mode, and an
	// 8-byte enqueue time.
	recordValueLen = 10

	// ownerIDLen is the length of an owner identity: 16 random bytes in
	// lowercase hex.
	ownerIDLen = 32

	// keyLenDigits is the width of the hex length prefix on the locked key.
	keyLenDigits = 8
)

// queuePrefix is the prefix every record on key shares: its queue.
func queuePrefix(key string) string {
	return Prefix + hexLen(len(key)) + key + "/"
}

// recordKey is the record of owner's request for mode on key.
func recordKey(key, owner string, mode Mode) string {
	return queuePrefix(key) + owner + "/" + string(modeChars[mode])
}

// hexLen renders n as keyLenDigits lowercase hex digits.
func hexLen(n int) string {
	s := strconv.FormatUint(uint64(n), 16)
	for len(s) < keyLenDigits {
		s = "0" + s
	}
	return s
}

// encodeValue renders a record value.
func encodeValue(mode Mode, enqueuedUnixNano int64) []byte {
	out := make([]byte, recordValueLen)
	out[0] = recordVersion
	out[1] = byte(mode)
	binary.BigEndian.PutUint64(out[2:], uint64(enqueuedUnixNano))
	return out
}

// record is one parsed lock record.
type record struct {
	key       string // the locked key
	owner     string
	mode      Mode
	createRev int64
	rawKey    string // the record's own key, the FIFO tiebreak
}

// parseRecord parses one stored lock record.
func parseRecord(kvp *kv.KeyValue) (record, error) {
	raw := string(kvp.Key)
	bad := &CorruptRecordError{Key: raw}
	if len(raw) < len(Prefix)+keyLenDigits || raw[:len(Prefix)] != Prefix {
		return record{}, bad
	}
	rest := raw[len(Prefix):]
	n, err := strconv.ParseUint(rest[:keyLenDigits], 16, 32)
	if err != nil {
		return record{}, bad
	}
	rest = rest[keyLenDigits:]
	// key + "/" + owner + "/" + modeChar
	if uint64(len(rest)) != n+1+ownerIDLen+2 {
		return record{}, bad
	}
	key := rest[:n]
	tail := rest[n:]
	if tail[0] != '/' || tail[1+ownerIDLen] != '/' {
		return record{}, bad
	}
	mode, ok := modesByChar[tail[len(tail)-1]]
	if !ok {
		return record{}, bad
	}
	if len(kvp.Value) != recordValueLen || kvp.Value[0] != recordVersion || Mode(kvp.Value[1]) != mode {
		return record{}, bad
	}
	return record{
		key:       key,
		owner:     tail[1 : 1+ownerIDLen],
		mode:      mode,
		createRev: kvp.CreateRevision,
		rawKey:    raw,
	}, nil
}

// sortQueue orders one key's records into FIFO order: create revision, then
// record key for records one transaction wrote together.
func sortQueue(q []record) {
	sort.Slice(q, func(i, j int) bool {
		if q[i].createRev != q[j].createRev {
			return q[i].createRev < q[j].createRev
		}
		return q[i].rawKey < q[j].rawKey
	})
}

// grantState is what evaluating one request against its key's queue found.
type grantState uint8

const (
	// stateMissing: the request's own record is not in the queue.
	stateMissing grantState = iota
	// stateWaiting: the record is queued behind an incompatible one.
	stateWaiting
	// stateGranted: the request is granted.
	stateGranted
)

// evaluate applies the grant rule to owner's request for mode in q, which
// must be one key's records in FIFO order: the request is granted iff every
// earlier record of a different owner is compatible with it.
func evaluate(q []record, owner string, mode Mode) grantState {
	for _, r := range q {
		if r.owner == owner && r.mode == mode {
			return stateGranted
		}
		if r.owner != owner && !compatible(r.mode, mode) {
			// An incompatible record of another owner sorts ahead of ours.
			// Ours is either further back (waiting) or absent; scan on to
			// tell which.
			for _, rest := range q {
				if rest.owner == owner && rest.mode == mode {
					return stateWaiting
				}
			}
			return stateMissing
		}
	}
	return stateMissing
}

// waitsFor builds the wait-for graph of a set of records: an edge from
// owner A to owner B for every record of A queued behind an incompatible
// record of B on the same key.
func waitsFor(records []record) map[string]map[string]struct{} {
	queues := make(map[string][]record)
	for _, r := range records {
		queues[r.key] = append(queues[r.key], r)
	}
	edges := make(map[string]map[string]struct{})
	for _, q := range queues {
		sortQueue(q)
		for i, r := range q {
			for _, earlier := range q[:i] {
				if earlier.owner == r.owner || compatible(earlier.mode, r.mode) {
					continue
				}
				if edges[r.owner] == nil {
					edges[r.owner] = make(map[string]struct{})
				}
				edges[r.owner][earlier.owner] = struct{}{}
			}
		}
	}
	return edges
}

// onCycle reports whether owner is on a cycle of edges.
func onCycle(edges map[string]map[string]struct{}, owner string) bool {
	seen := make(map[string]bool)
	stack := make([]string, 0, len(edges[owner]))
	for next := range edges[owner] {
		stack = append(stack, next)
	}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cur == owner {
			return true
		}
		if seen[cur] {
			continue
		}
		seen[cur] = true
		for next := range edges[cur] {
			stack = append(stack, next)
		}
	}
	return false
}

// prefixEnd is the exclusive upper bound of every key with prefix.
func prefixEnd(prefix string) []byte {
	end := []byte(prefix)
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] < 0xff {
			end[i]++
			return end[:i+1]
		}
	}
	return nil
}
