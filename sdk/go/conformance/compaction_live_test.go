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

package conformance

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestCompaction_KeepsTheValueLiveAtTheFloorOnEveryTransport: etcd's
// compaction contract, which a client listing at a revision above the
// compaction point relies on -- every read above the floor answers as it did
// before the compaction. A key written before the floor and again after it
// still reads its pre-floor value at a revision between the two; a key
// deleted at or below the floor and written again reads its new value.
func TestCompaction_KeepsTheValueLiveAtTheFloorOnEveryTransport(t *testing.T) {
	for _, tk := range transportKinds {
		t.Run(string(tk), func(t *testing.T) {
			c := bootCompactionCell(t, tk)
			h := c.as(t, "tenant-a")

			put := func(key, value string) int64 {
				t.Helper()
				if err := h.kv.Put(h.ctx, key, []byte(value)); err != nil {
					t.Fatalf("%s: Put %s: %v", tk, key, err)
				}
				kv, err := h.mvcc.GetRevision(h.ctx, key)
				if err != nil {
					t.Fatalf("%s: GetRevision %s: %v", tk, key, err)
				}
				return kv.ModRevision
			}

			put("r/k", "v1")
			put("r/k", "v2")
			put("r/gone", "g1")
			if err := h.kv.Delete(h.ctx, "r/gone"); err != nil {
				t.Fatalf("%s: Delete gone: %v", tk, err)
			}
			floor := put("r/x", "x1")
			between := put("r/y", "y1") // above the floor, below k's and gone's next writes
			put("r/gone", "g2")
			put("r/k", "v3")

			if err := h.mvcc.Compact(h.ctx, floor); err != nil {
				t.Fatalf("%s: Compact(%d): %v", tk, floor, err)
			}

			want := map[string]string{"r/k": "v2", "r/x": "x1", "r/y": "y1"}
			for key, v := range want {
				kv, err := h.mvcc.GetAtRevision(h.ctx, key, between)
				if err != nil || string(kv.Value) != v {
					t.Fatalf("%s: GetAtRevision(%s, %d) after compacting to %d = %v, %v; want %s",
						tk, key, between, floor, kv, err, v)
				}
			}
			if kv, err := h.mvcc.GetAtRevision(h.ctx, "r/gone", between); err == nil {
				t.Fatalf("%s: gone at %d = %v; it was deleted before the floor", tk, between, kv)
			}

			resp, err := h.mvcc.Range(h.ctx, []byte("r/"), []byte("r0"), transport.RangeOptions{Revision: between})
			if err != nil {
				t.Fatalf("%s: Range at %d: %v", tk, between, err)
			}
			got := map[string]string{}
			for _, kv := range resp.Kvs {
				got[string(kv.Key)] = string(kv.Value)
			}
			if len(got) != len(want) || got["r/k"] != "v2" || got["r/x"] != "x1" || got["r/y"] != "y1" {
				t.Fatalf("%s: Range at %d after compacting to %d = %v, want %v", tk, between, floor, got, want)
			}
		})
	}
}
