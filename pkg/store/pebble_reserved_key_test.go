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

package store

import (
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// pebbleKind is the Pebble state machine among leaseSMKinds: it stores user
// keys raw beside its own 0x00 records, so it is the one a user key could
// reach them on.
func pebbleKind(t *testing.T) leaseSMKind {
	t.Helper()
	for _, k := range leaseSMKinds {
		if k.name == "pebble" {
			return k
		}
	}
	t.Fatal("no pebble kind")
	return leaseSMKind{}
}

// A user write naming a key in the reserved 0x00 namespace is refused, on
// every write path, before it stages anything. Otherwise it could overwrite a
// lease-table record -- here the ID sequence, which then fails the next open
// as corrupt -- or the last-applied index.
func TestPebbleSM_RefusesUserKeysInTheReservedNamespace(t *testing.T) {
	seqKey := []byte(lease.TablePrefix + "seq")
	lastApplied := []byte(metaKeyLastApplied)
	op := func(typ byte, key []byte) []byte {
		b, err := protocol.EncodeOp(typ, key, []byte("x"), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	cases := []struct {
		name string
		cmd  func() ([]byte, error)
	}{
		{"mvcc put", func() ([]byte, error) { return protocol.EncodeMVCCPut(seqKey, []byte("x"), 0) }},
		{"mvcc put-if-absent", func() ([]byte, error) { return protocol.EncodeMVCCPutIfAbsent(seqKey, []byte("x"), 0) }},
		{"mvcc delete", func() ([]byte, error) { return protocol.EncodeMVCCDelete(lastApplied) }},
		{"base put", func() ([]byte, error) { return kv.EncodePut(seqKey, []byte("x")) }},
		{"base delete", func() ([]byte, error) { return kv.EncodeDelete(lastApplied) }},
		{"compare-and-swap", func() ([]byte, error) { return protocol.EncodeCompareAndSwap(seqKey, nil, []byte("x")) }},
		{"compare-and-delete", func() ([]byte, error) { return protocol.EncodeCompareAndDelete(lastApplied, nil) }},
		// Every reserved record sorts in [0x00, 0x01), so this range names
		// them all: the last-applied index and the whole lease table.
		{"range delete", func() ([]byte, error) { return protocol.EncodeDeleteRange([]byte{0x00}, nil) }},
		{"batch", func() ([]byte, error) { return protocol.EncodeBatch([][]byte{op(protocol.OpMVCCPut, seqKey)}) }},
		{"transaction", func() ([]byte, error) {
			return protocol.EncodeTransaction(nil, [][]byte{op(protocol.OpMVCCPut, seqKey)}, nil)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newLeaseHarness(t, pebbleKind(t))
			h.grant(60)
			res := h.apply(tc.cmd())
			if _, _, ok := protocol.DecodeAppErrorKind(res.Data); !ok {
				t.Fatalf("result %q: the write into the reserved namespace was not refused", res.Data)
			}
			// The lease table is intact: the state machine reopens.
			h.restart()
		})
	}
}
