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

package engine

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// This file is the go-qrdb audit-replicated review's addendum: a transaction
// compare or an OpGuarded compare against a raw-keyspace key must be refused
// BEFORE it is ever evaluated, on every engine family, not only on the
// write (op-key) side ReservedKeyRefusal already covered. Left unchecked,
// CompareResultLess/Greater -- or simply whether the compare holds at all --
// makes an ordinary transaction or guarded write an extraction oracle over
// state no caller may read directly (a binary search over the stored bytes
// via repeated proposals), and a version compare alone leaks whether a
// reserved key exists.

// reservedTestKeys names one key from each raw-keyspace table this
// repository defines today, so this test covers each without the per-table
// list the shared predicate (statemachine.IsReservedKey) exists to avoid at
// the call sites.
var reservedTestKeys = map[string][]byte{
	"lease table": []byte(lease.TablePrefix + "l/x"),
	"audit table": []byte(vlog.TablePrefix + "h/acme"),
	"named log":   []byte(vlog.LogTablePrefix + "acme/app/ledger/h"),
	"log key":     []byte(vlog.LogTablePrefix + "acme/app/ledger/k/\x00\x00\x00\x01"),
	"node-local":  []byte("\x00__quicraft_last_applied__"),
}

// encodeCompareEqualVersionZero builds a compare asserting key is absent
// (version 0), the simplest compare shape every engine evaluates the same
// way.
func encodeCompareEqualVersionZero(t *testing.T, key []byte) []byte {
	t.Helper()
	c, err := protocol.EncodeCompare(key, protocol.CompareTargetVersion, protocol.CompareResultEqual, nil, 0, 0, 0)
	if err != nil {
		t.Fatalf("EncodeCompare: %v", err)
	}
	return c
}

// TestMVCCEngine_Transaction_RefusesReservedCompareKey covers the
// transaction-compare half on the MVCC engine family.
func TestMVCCEngine_Transaction_RefusesReservedCompareKey(t *testing.T) {
	for name, key := range reservedTestKeys {
		t.Run(name, func(t *testing.T) {
			eng, stg := newMVCCEngine(t)
			defer func() { _ = stg.Close() }()
			defer func() { _ = eng.Close(context.Background()) }()

			compare := encodeCompareEqualVersionZero(t, key)
			successOp := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("harmless"), []byte("v"), nil, 0))
			cmd := mustEncode(protocol.EncodeTransaction([][]byte{compare}, [][]byte{successOp}, nil))

			results := make([]sm.Result, 1)
			if err := eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, results); err != nil {
				t.Fatalf("Update returned a Go error (would halt apply): %v", err)
			}
			msg, ok := protocol.DecodeAppError(results[0].Data)
			if !ok {
				t.Fatalf("expected an encoded refusal, got result=%+v", results[0])
			}
			if _, lookupErr := eng.Store().Get([]byte("harmless")); lookupErr == nil {
				t.Fatal("the transaction's success branch ran despite the reserved compare key")
			}
			t.Logf("refusal message: %s", msg)
		})
	}
}

// TestMVCCEngine_Guarded_RefusesReservedCompareKey covers the
// OpGuarded-compare half on the MVCC engine family.
func TestMVCCEngine_Guarded_RefusesReservedCompareKey(t *testing.T) {
	for name, key := range reservedTestKeys {
		t.Run(name, func(t *testing.T) {
			eng, stg := newMVCCEngine(t)
			defer func() { _ = stg.Close() }()
			defer func() { _ = eng.Close(context.Background()) }()

			compare := encodeCompareEqualVersionZero(t, key)
			inner := mustEncode(protocol.EncodeMVCCPut([]byte("harmless"), []byte("v"), 0))
			cmd := mustEncode(protocol.EncodeGuarded([][]byte{compare}, inner))

			results := make([]sm.Result, 1)
			if err := eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, results); err != nil {
				t.Fatalf("Update returned a Go error (would halt apply): %v", err)
			}
			if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
				t.Fatalf("expected an encoded refusal, got result=%+v", results[0])
			}
			if _, lookupErr := eng.Store().Get([]byte("harmless")); lookupErr == nil {
				t.Fatal("the guarded write's inner command ran despite the reserved compare key")
			}
		})
	}
}

// TestKVEngine_Transaction_RefusesReservedCompareKey is the same property on
// the KV engine family (the file/objstore backend).
func TestKVEngine_Transaction_RefusesReservedCompareKey(t *testing.T) {
	for name, key := range reservedTestKeys {
		t.Run(name, func(t *testing.T) {
			stg := memory.NewStorage()
			defer func() { _ = stg.Close() }()
			eng := NewKVEngine(stg)

			compare := encodeCompareEqualVersionZero(t, key)
			successOp := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("harmless"), []byte("v"), nil, 0))
			cmd := mustEncode(protocol.EncodeTransaction([][]byte{compare}, [][]byte{successOp}, nil))

			results := make([]sm.Result, 1)
			if err := eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, results); err != nil {
				t.Fatalf("Update returned a Go error (would halt apply): %v", err)
			}
			if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
				t.Fatalf("expected an encoded refusal, got result=%+v", results[0])
			}
			if val, _ := stg.Get([]byte("harmless")); val != nil {
				t.Fatal("the transaction's success branch ran despite the reserved compare key")
			}
		})
	}
}

// TestKVEngine_Guarded_RefusesReservedCompareKey is the OpGuarded half on
// the KV engine family.
func TestKVEngine_Guarded_RefusesReservedCompareKey(t *testing.T) {
	for name, key := range reservedTestKeys {
		t.Run(name, func(t *testing.T) {
			stg := memory.NewStorage()
			defer func() { _ = stg.Close() }()
			eng := NewKVEngine(stg)

			compare := encodeCompareEqualVersionZero(t, key)
			inner := mustEncode(protocol.EncodeMVCCPut([]byte("harmless"), []byte("v"), 0))
			cmd := mustEncode(protocol.EncodeGuarded([][]byte{compare}, inner))

			results := make([]sm.Result, 1)
			if err := eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, results); err != nil {
				t.Fatalf("Update returned a Go error (would halt apply): %v", err)
			}
			if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
				t.Fatalf("expected an encoded refusal, got result=%+v", results[0])
			}
			if val, _ := stg.Get([]byte("harmless")); val != nil {
				t.Fatal("the guarded write's inner command ran despite the reserved compare key")
			}
		})
	}
}
