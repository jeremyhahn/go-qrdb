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

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// hashOf returns h's replica hash.
func hashOf(t *testing.T, h *leaseHarness) uint64 {
	t.Helper()
	v, err := h.sm.GetHash()
	if err != nil {
		t.Fatalf("GetHash: %v", err)
	}
	return v
}

// The replica hash is what detects a state machine that is not a deterministic
// function of the log. The lease table is replicated state -- a replica that
// lost or never wrote it holds different leases -- so the hash must cover it;
// the last-applied index is local to each node, so the hash must not.
func TestLeaseHash_CoversTheLeaseTableAndNotLastApplied(t *testing.T) {
	for _, kind := range leaseSMKinds {
		t.Run(kind.name, func(t *testing.T) {
			a := newLeaseHarness(t, kind)
			b := newLeaseHarness(t, kind)
			// b applies the same commands at different log positions, so its
			// last-applied index differs from a's throughout.
			b.index = 1000

			a.put("k", "v", 0)
			b.put("k", "v", 0)
			if hashOf(t, a) != hashOf(t, b) {
				t.Fatal("identical state at different log positions hashes differently: the hash covers last-applied")
			}

			// A grant writes nothing but the lease table.
			a.grant(60)
			if hashOf(t, a) == hashOf(t, b) {
				t.Fatal("a replica holding a lease the other lacks hashes the same: the hash does not cover the lease table")
			}

			b.grant(60)
			if hashOf(t, a) != hashOf(t, b) {
				t.Fatal("replicas that applied the same grants hash differently")
			}

			// Binding a key changes the table too.
			id := a.grant(30)
			b.grant(30)
			a.applyOK(protocol.EncodeMVCCPut([]byte("bound"), []byte("x"), id))
			b.applyOK(protocol.EncodeMVCCPut([]byte("bound"), []byte("x"), 0))
			if hashOf(t, a) == hashOf(t, b) {
				t.Fatal("a key bound to a lease on one replica and to none on the other hashes the same")
			}
		})
	}
}
