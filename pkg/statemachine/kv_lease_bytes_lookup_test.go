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

package statemachine

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	memstore "github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// leaseTableRecords returns the keys of the three lease-table record kinds a
// grant of lease id followed by a put of key under it writes: the sequence,
// the lease record, and the key's binding. They are spelled out here, from
// the layout pkg/lease documents, so the test does not depend on that
// package's unexported key builders.
func leaseTableRecords(id int64, key string) map[string][]byte {
	leaseKey := binary.BigEndian.AppendUint64([]byte(lease.TablePrefix+"l/"), uint64(id))
	return map[string][]byte{
		"seq":     []byte(lease.TablePrefix + "seq"),
		"l/<id>":  leaseKey,
		"b/<key>": []byte(lease.TablePrefix + "b/" + key),
	}
}

// bytesLookupAdapter is a state machine adapter that answers go-quicraft's
// non-allocating point reads. go-quicraft's rsm routes a []byte query to
// BytesLookup, not to Lookup, so this path must hide the state machine's own
// records exactly as Lookup does.
type bytesLookupAdapter interface {
	BytesLookup(key []byte) ([]byte, error)
}

// leaseLookupCase builds one adapter, applies a grant and a leased put of
// "user", and returns the adapter and a raw backend read that proves the
// records the test hides really are in the keyspace.
type leaseLookupCase struct {
	name  string
	build func(t *testing.T) (bytesLookupAdapter, Backend)
}

func leaseLookupCases() []leaseLookupCase {
	cmds := [][]byte{
		mustEncode(protocol.EncodeLeaseGrant(30)),
		mustEncode(protocol.EncodeMVCCPut([]byte("user"), []byte("v"), 1)),
	}
	return []leaseLookupCase{
		{"disk", func(t *testing.T) (bytesLookupAdapter, Backend) {
			t.Helper()
			ctx := context.Background()
			a := newDiskAdapter(memstore.NewStorage())
			if _, err := a.Open(ctx, "", nil); err != nil {
				t.Fatalf("Open: %v", err)
			}
			entries := []sm.Entry{{Index: 1, Cmd: cmds[0]}, {Index: 2, Cmd: cmds[1]}}
			if err := a.Update(ctx, entries, make([]sm.Result, len(entries))); err != nil {
				t.Fatalf("Update: %v", err)
			}
			return a, a.backend
		}},
		{"highperf", func(t *testing.T) (bytesLookupAdapter, Backend) {
			t.Helper()
			a, ok := NewKVHighPerfCreateFunc(nil)(1, 1).(*HighPerfAdapter)
			if !ok {
				t.Fatal("highperf create function returned no *HighPerfAdapter")
			}
			entries := []sm.Entry{{Index: 1, Cmd: cmds[0]}, {Index: 2, Cmd: cmds[1]}}
			if err := a.Update(context.Background(), entries, make([]sm.Result, len(entries))); err != nil {
				t.Fatalf("Update: %v", err)
			}
			return a, a.backend
		}},
	}
}

// TestBytesLookup_HidesTheLeaseTable: the non-allocating point read returns
// no lease-table record -- the sequence, a lease record, or a binding -- on
// either adapter that implements it, while a client key still reads.
func TestBytesLookup_HidesTheLeaseTable(t *testing.T) {
	for _, tc := range leaseLookupCases() {
		t.Run(tc.name, func(t *testing.T) {
			a, backend := tc.build(t)
			for kind, key := range leaseTableRecords(1, "user") {
				raw, err := backend.Get(key)
				if err != nil || raw == nil {
					t.Fatalf("%s: the record is not in the keyspace (%v, %v); the test proves nothing", kind, raw, err)
				}
				got, err := a.BytesLookup(key)
				if err != nil || got != nil {
					t.Errorf("BytesLookup(%s) = %q, %v; want nothing -- the lease table is the state machine's", kind, got, err)
				}
			}
			if got, err := a.BytesLookup([]byte("user")); err != nil || string(got) != "v" {
				t.Fatalf("BytesLookup(user) = %q, %v; want the client value", got, err)
			}
		})
	}
}

// TestBytesLookupInto_HidesTheLeaseTable: the highperf adapter's
// caller-buffer read hides the same records.
func TestBytesLookupInto_HidesTheLeaseTable(t *testing.T) {
	for _, tc := range leaseLookupCases() {
		if tc.name != "highperf" {
			continue
		}
		a, _ := tc.build(t)
		hp := a.(*HighPerfAdapter)
		for kind, key := range leaseTableRecords(1, "user") {
			got, err := hp.BytesLookupInto(key, make([]byte, 0, 64))
			if err != nil || got != nil {
				t.Errorf("BytesLookupInto(%s) = %q, %v; want nothing", kind, got, err)
			}
		}
		if got, err := hp.BytesLookupInto([]byte("user"), make([]byte, 0, 8)); err != nil || string(got) != "v" {
			t.Fatalf("BytesLookupInto(user) = %q, %v; want the client value", got, err)
		}
	}
}
