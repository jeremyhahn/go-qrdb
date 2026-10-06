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

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// This file covers the WRITE half of the go-qrdb audit-replicated review's
// B2 fix on KVEngine directly -- kvHandlePut, kvHandlePutIfAbsent,
// kvHandleDelete and handleBatch each refuse a reserved-keyspace key before
// staging anything -- complementing reserved_key_compare_test.go, which
// covers only the COMPARE side (Transaction/Guarded). KVEngine had no
// reserved-key protection anywhere before that review.

func TestKVEngine_Put_RefusesReservedKey(t *testing.T) {
	for name, key := range reservedTestKeys {
		t.Run(name, func(t *testing.T) {
			stg := memory.NewStorage()
			defer func() { _ = stg.Close() }()
			eng := NewKVEngine(stg)

			results := applyKVEntries(t, eng, []sm.Entry{kvPutEntry(1, string(key), "v")})
			if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
				t.Fatalf("expected an encoded refusal, got result=%+v", results[0])
			}
			if val, _ := stg.Get(key); val != nil {
				t.Fatalf("the reserved key %q was written", key)
			}
		})
	}
}

func TestKVEngine_PutIfAbsent_RefusesReservedKey(t *testing.T) {
	for name, key := range reservedTestKeys {
		t.Run(name, func(t *testing.T) {
			stg := memory.NewStorage()
			defer func() { _ = stg.Close() }()
			eng := NewKVEngine(stg)

			results := applyKVEntries(t, eng, []sm.Entry{kvPutIfAbsentEntry(1, string(key), "v")})
			if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
				t.Fatalf("expected an encoded refusal, got result=%+v", results[0])
			}
			if val, _ := stg.Get(key); val != nil {
				t.Fatalf("the reserved key %q was written", key)
			}
		})
	}
}

func TestKVEngine_Delete_RefusesReservedKey(t *testing.T) {
	for name, key := range reservedTestKeys {
		t.Run(name, func(t *testing.T) {
			stg := memory.NewStorage()
			defer func() { _ = stg.Close() }()
			eng := NewKVEngine(stg)
			if err := stg.Set(key, []byte("already there")); err != nil {
				t.Fatalf("Set: %v", err)
			}

			results := applyKVEntries(t, eng, []sm.Entry{kvDeleteEntry(1, string(key))})
			if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
				t.Fatalf("expected an encoded refusal, got result=%+v", results[0])
			}
			if val, _ := stg.Get(key); val == nil {
				t.Fatalf("the reserved key %q was deleted despite the refusal", key)
			}
		})
	}
}

func TestKVEngine_Batch_RefusesReservedKey(t *testing.T) {
	for name, key := range reservedTestKeys {
		t.Run(name, func(t *testing.T) {
			stg := memory.NewStorage()
			defer func() { _ = stg.Close() }()
			eng := NewKVEngine(stg)

			reservedPut := mustEncode(protocol.EncodeOp(protocol.OpFSMPut, key, []byte("v"), nil, 0))
			harmlessPut := mustEncode(protocol.EncodeOp(protocol.OpFSMPut, []byte("harmless"), []byte("v"), nil, 0))
			cmd := mustEncode(protocol.EncodeBatch([][]byte{harmlessPut, reservedPut}))

			results := make([]sm.Result, 1)
			if err := eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, results); err != nil {
				t.Fatalf("Update returned a Go error (would halt apply): %v", err)
			}
			if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
				t.Fatalf("expected an encoded refusal, got result=%+v", results[0])
			}
			// The WHOLE batch is refused before anything stages, so even the
			// harmless op -- which comes first in the batch -- must not land.
			if val, _ := stg.Get([]byte("harmless")); val != nil {
				t.Fatal("a harmless op in the same batch as a reserved key was applied")
			}
		})
	}
}
