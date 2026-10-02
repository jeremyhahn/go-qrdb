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
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestMVCCEngine_CompactionKeepsTheValueLiveAtTheFloor: through the apply
// path -- the writes and the compaction in ONE apply batch, so the scan reads
// through the batch -- a compaction keeps each key's entry live at the floor.
//
//	rev 1 k=v1   rev 2 k=v2   rev 3 gone=g3   rev 4 gone deleted
//	rev 5 x=x5   rev 6 gone=g6   rev 7 k=v7
//
// Compacted to 5, a read at 6 -- above the floor, below k's newest write --
// answers k=v2, gone=g6, x=x5, exactly as before. gone's tombstone, live at 5
// with g6 newer, goes with g3. Both compaction opcodes.
func TestMVCCEngine_CompactionKeepsTheValueLiveAtTheFloor(t *testing.T) {
	for name, tc := range map[string]struct {
		prefix  string
		compact []byte
	}{
		"prefix": {"acme/app/", mustEncode(protocol.EncodeMVCCCompactPrefix([]byte("acme/app/"), 5))},
		"node":   {"node-", mustEncode(protocol.EncodeMVCCCompactNode(5))},
	} {
		t.Run(name, func(t *testing.T) {
			eng, stg := newMVCCEngine(t)
			defer func() { _ = stg.Close() }()
			defer func() { _ = eng.Close(context.Background()) }()

			k, gone, x := []byte(tc.prefix+"k"), []byte(tc.prefix+"gone"), []byte(tc.prefix+"x")
			entries := []sm.Entry{
				mvccPutEntry(1, k, []byte("v1"), 0),
				mvccPutEntry(2, k, []byte("v2"), 0),
				mvccPutEntry(3, gone, []byte("g3"), 0),
				mvccDeleteEntry(4, gone),
				mvccPutEntry(5, x, []byte("x5"), 0),
				mvccPutEntry(6, gone, []byte("g6"), 0),
				mvccPutEntry(7, k, []byte("v7"), 0),
				{Index: 8, Cmd: tc.compact},
			}
			results := make([]sm.Result, len(entries))
			if err := eng.Update(context.Background(), entries, results); err != nil {
				t.Fatalf("Update: %v", err)
			}
			if r := results[7]; r.Value != 1 || len(r.Data) != 0 {
				t.Fatalf("compaction result = %+v", r)
			}

			want := map[string]string{string(k): "v2", string(gone): "g6", string(x): "x5"}
			for key, v := range want {
				kv, err := eng.Store().GetAtRevision([]byte(key), 6)
				if err != nil || string(kv.Value) != v {
					t.Fatalf("GetAtRevision(%s, 6) after compacting to 5 = %v, %v; want %s", key, kv, err, v)
				}
			}
			kvs, _, _, err := eng.Store().Range([]byte(tc.prefix), []byte(tc.prefix+"\xff"), service.WithRevision(6))
			if err != nil {
				t.Fatalf("Range at 6: %v", err)
			}
			got := map[string]string{}
			for _, kv := range kvs {
				got[string(kv.Key)] = string(kv.Value)
			}
			if len(got) != len(want) {
				t.Fatalf("Range at 6 after compacting to 5 = %v, want %v", got, want)
			}
			for key, v := range want {
				if got[key] != v {
					t.Fatalf("Range at 6 after compacting to 5 = %v, want %v", got, want)
				}
			}
			if kv, err := eng.Store().GetAtRevision(k, 7); err != nil || string(kv.Value) != "v7" {
				t.Fatalf("k's current value = %v, %v", kv, err)
			}
		})
	}
}
