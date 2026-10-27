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
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// This file is the Pebble half of the go-qrdb audit-replicated review's
// addendum: see pkg/engine/reserved_key_compare_test.go's header comment
// for the full rationale (a transaction or OpGuarded compare against a
// raw-keyspace key must be refused BEFORE it is ever evaluated, on every
// engine family, Pebble included).

var pebbleReservedTestKeys = map[string][]byte{
	"lease table": []byte(lease.TablePrefix + "l/x"),
	"audit table": []byte(vlog.TablePrefix + "h/acme"),
	"named log":   []byte(vlog.LogTablePrefix + "acme/app/ledger/h"),
	"log key":     []byte(vlog.LogTablePrefix + "acme/app/ledger/k/\x00\x00\x00\x01"),
	"node-local":  []byte(metaKeyLastApplied),
}

func pebbleEncodeCompareEqualVersionZero(t *testing.T, key []byte) []byte {
	t.Helper()
	c, err := protocol.EncodeCompare(key, protocol.CompareTargetVersion, protocol.CompareResultEqual, nil, 0, 0, 0)
	if err != nil {
		t.Fatalf("EncodeCompare: %v", err)
	}
	return c
}

func TestPebbleStateMachine_Transaction_RefusesReservedCompareKey(t *testing.T) {
	for name, key := range pebbleReservedTestKeys {
		t.Run(name, func(t *testing.T) {
			s := openExtendedStore(t)

			compare := pebbleEncodeCompareEqualVersionZero(t, key)
			successOp := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("harmless"), []byte("v"), nil, 0))
			cmd := mustEncode(protocol.EncodeTransaction([][]byte{compare}, [][]byte{successOp}, nil))

			results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})
			if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
				t.Fatalf("expected an encoded refusal, got result=%+v", results[0])
			}
			if _, err := s.Lookup(context.Background(), []byte("harmless")); err == nil {
				t.Fatal("the transaction's success branch ran despite the reserved compare key")
			}
		})
	}
}

func TestPebbleStateMachine_Guarded_RefusesReservedCompareKey(t *testing.T) {
	for name, key := range pebbleReservedTestKeys {
		t.Run(name, func(t *testing.T) {
			s := openExtendedStore(t)

			compare := pebbleEncodeCompareEqualVersionZero(t, key)
			// OpGuarded's inner is a full standalone command (dispatched by
			// opcode), unlike a transaction's ops, which are the lighter
			// op-frame encoding (EncodeOp).
			innerCmd := mustEncode(protocol.EncodeMVCCPut([]byte("harmless"), []byte("v"), 0))
			cmd := mustEncode(protocol.EncodeGuarded([][]byte{compare}, innerCmd))

			results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})
			if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
				t.Fatalf("expected an encoded refusal, got result=%+v", results[0])
			}
			if _, err := s.Lookup(context.Background(), []byte("harmless")); err == nil {
				t.Fatal("the guarded write's inner command ran despite the reserved compare key")
			}
		})
	}
}

// TestPebbleStateMachine_Lookup_RefusesReservedKey is B2's direct
// regression test: Lookup([]byte(key)) (and therefore BytesLookup, which
// calls the same getKey) must answer not-found for a raw-keyspace key
// rather than the record -- exactly as if the key did not exist -- even
// when a real Chain/Lessor is attached and holds a live record under it.
func TestPebbleStateMachine_Lookup_RefusesReservedKey(t *testing.T) {
	s, chain := openStoreWithAuditChain(t)
	// A live record really is written under the audit table (asserted via
	// Query, the sanctioned read path), so the refusal below is about the
	// RESERVED PREFIX itself, not an accident of an empty table.
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: auditAppendCmd(t, "acme", auditEvent("acme"))}})
	page, err := chain.Query(pebbleStorage.NewFromDB(s.db, true), "acme", audit.Filter{Limit: 100})
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("setup: expected one event via Query, got page=%+v err=%v", page, err)
	}

	for name, key := range pebbleReservedTestKeys {
		t.Run(name, func(t *testing.T) {
			val, err := s.Lookup(context.Background(), key)
			if err == nil {
				t.Fatalf("Lookup(%q) returned no error; want a not-found answer, got value %v", key, val)
			}
			var notFound *kv.KeyNotFoundError
			if !errors.As(err, &notFound) {
				t.Fatalf("Lookup(%q) err = %v, want *kv.KeyNotFoundError", key, err)
			}
		})
	}
}

// TestPebbleStateMachine_BytesLookup_RefusesReservedKey covers the second
// exported entry point getKey serves.
func TestPebbleStateMachine_BytesLookup_RefusesReservedKey(t *testing.T) {
	s := openExtendedStore(t)
	for name, key := range pebbleReservedTestKeys {
		t.Run(name, func(t *testing.T) {
			if val, err := s.BytesLookup(key); val != nil || err == nil {
				t.Fatalf("BytesLookup(%q) = (%v, %v), want (nil, an error)", key, val, err)
			}
		})
	}
}
