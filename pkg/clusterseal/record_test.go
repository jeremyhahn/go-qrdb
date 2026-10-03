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

package clusterseal

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestBarrierConstructorsAndParse(t *testing.T) {
	tests := []struct {
		name     string
		barrier  Barrier
		want     string
		kind     Kind
		tenant   string
		database string
	}{
		{"node", NodeBarrier(), "node", KindNode, "", ""},
		{"tenant", TenantBarrier("acme"), "tenant/acme", KindTenant, "acme", ""},
		{"database", DatabaseBarrier("acme", "orders"), "database/acme/orders", KindDatabase, "acme", "orders"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if string(tc.barrier) != tc.want {
				t.Fatalf("barrier = %q, want %q", tc.barrier, tc.want)
			}
			parsed, err := ParseBarrier(tc.want)
			if err != nil {
				t.Fatalf("ParseBarrier(%q): %v", tc.want, err)
			}
			if parsed != tc.barrier {
				t.Fatalf("ParseBarrier = %q, want %q", parsed, tc.barrier)
			}
			if parsed.Kind() != tc.kind || parsed.Tenant() != tc.tenant || parsed.Database() != tc.database {
				t.Fatalf("parts = (%v, %q, %q), want (%v, %q, %q)",
					parsed.Kind(), parsed.Tenant(), parsed.Database(), tc.kind, tc.tenant, tc.database)
			}
		})
	}
}

func TestParseBarrierRefusesMalformed(t *testing.T) {
	for _, in := range []string{
		"", "nodes", "tenant/", "tenant", "tenant/a/b", "database/acme", "database//x",
		"database/acme/", "database/acme/a/b", "system", "node/x",
	} {
		t.Run(in, func(t *testing.T) {
			_, err := ParseBarrier(in)
			var bad *InvalidBarrierError
			if !errors.As(err, &bad) {
				t.Fatalf("ParseBarrier(%q) err = %v, want *InvalidBarrierError", in, err)
			}
		})
	}
}

func TestRecordKeys(t *testing.T) {
	if got := recordKey(TenantBarrier("acme")); got != "__sys:cluster/seal/records/tenant/acme" {
		t.Fatalf("recordKey = %q", got)
	}
	if got := recordKey(NodeBarrier()); got != "__sys:cluster/seal/records/node" {
		t.Fatalf("recordKey = %q", got)
	}
	if got := ackKey("192.0.2.1:5000"); got != "__sys:cluster/seal/acks/192.0.2.1:5000" {
		t.Fatalf("ackKey = %q", got)
	}
}

// TestRecordCarriesNoKeyMaterial pins the encoded field set of a record. A
// record is replicated through the Raft log to every node, so it must never be
// able to carry a key, a share, a credential or an envelope. A new field fails
// this test and has to be argued for here.
func TestRecordCarriesNoKeyMaterial(t *testing.T) {
	rec := Record{
		Barrier: TenantBarrier("acme"), Epoch: 3, Sealed: true,
		Principal: "alice", PrincipalTenant: "acme", Node: "192.0.2.1:5000",
		At: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
	}
	raw, err := encodeRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(fields))
	for k := range fields {
		got = append(got, k)
	}
	slices.Sort(got)
	want := []string{"at", "barrier", "epoch", "node", "principal", "principal_tenant", "sealed"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("record fields = %v, want exactly %v", got, want)
	}
	rt := reflect.TypeOf(Record{})
	if rt.NumField() != len(want) {
		t.Fatalf("Record has %d fields, want %d", rt.NumField(), len(want))
	}
	for i := range rt.NumField() {
		if k := rt.Field(i).Type.Kind(); k == reflect.Slice || k == reflect.Map || k == reflect.Interface {
			t.Fatalf("Record field %s is a %v: a record must not be able to carry opaque bytes", rt.Field(i).Name, k)
		}
	}
	back, err := decodeRecord(raw)
	if err != nil {
		t.Fatal(err)
	}
	if back != rec {
		t.Fatalf("round trip = %+v, want %+v", back, rec)
	}
}

func TestDecodeRecordRefusesCorrupt(t *testing.T) {
	for name, raw := range map[string]string{
		"not json":     "{",
		"bad barrier":  `{"barrier":"tenant/","epoch":1,"sealed":true}`,
		"zero epoch":   `{"barrier":"node","epoch":0,"sealed":true}`,
		"unknown kind": `{"barrier":"cluster","epoch":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeRecord([]byte(raw)); err == nil {
				t.Fatalf("decodeRecord(%s) succeeded", raw)
			}
		})
	}
}
