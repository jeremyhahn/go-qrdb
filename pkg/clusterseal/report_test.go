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
	"testing"
	"time"
)

func TestReportOfInactiveAndNilIsEmpty(t *testing.T) {
	if got := ReportOf(nil, NodeBarrier()); *got != (Report{}) {
		t.Fatalf("nil sealer report = %+v", *got)
	}
	if ActiveIn(nil) {
		t.Fatal("a nil sealer is active")
	}
	h := newHarness(t)
	h.gate.set(false)
	if got := ReportOf(h.m, NodeBarrier()); *got != (Report{}) {
		t.Fatalf("inactive report = %+v", *got)
	}
	if ActiveIn(h.m) {
		t.Fatal("inactive monitor reported active")
	}
}

func TestReportCarriesRecordAndLocal(t *testing.T) {
	h := newHarness(t)
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	h.putRecord(t, Record{Barrier: NodeBarrier(), Epoch: 7, Sealed: true, Principal: "ops", PrincipalTenant: "system", Node: peerB, At: at})
	h.poll(t)
	got := ReportOf(h.m, NodeBarrier())
	want := Report{Active: true, Sealed: true, Epoch: 7, Principal: "ops", PrincipalTenant: "system", Node: peerB, At: at, Local: LocalHeld}
	if *got != want {
		t.Fatalf("report = %+v, want %+v", *got, want)
	}
	if !ActiveIn(h.m) {
		t.Fatal("ActiveIn false for an active monitor")
	}
}

// TestReportJSONShape pins the documented `cluster_seal` field names, and that
// an inactive report omits everything but active, sealed and epoch.
func TestReportJSONShape(t *testing.T) {
	raw, err := json.Marshal(Report{})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"active":false,"sealed":false,"epoch":0}` {
		t.Fatalf("inactive = %s", raw)
	}
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	raw, err = json.Marshal(Report{Active: true, Sealed: true, Epoch: 7, Principal: "alice",
		PrincipalTenant: "acme", Node: "192.0.2.11:5000", At: at, Local: LocalSealed})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"active":true,"sealed":true,"epoch":7,"principal":"alice","principal_tenant":"acme","node":"192.0.2.11:5000","at":"2026-10-01T12:00:00Z","local":"sealed"}`
	if string(raw) != want {
		t.Fatalf("json = %s\nwant   %s", raw, want)
	}
}
