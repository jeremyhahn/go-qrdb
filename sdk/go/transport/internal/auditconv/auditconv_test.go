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

package auditconv

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func sampleEvent() transport.AuditEvent {
	return transport.AuditEvent{
		Timestamp:  time.Unix(1_700_000_000, 0).UTC(),
		Actor:      "alice",
		TenantID:   "acme",
		Role:       "admin",
		Resource:   "user:bob",
		Action:     "create",
		Outcome:    audit.OutcomeSuccess,
		Attributes: map[string]any{"k": "v"},
		ClientIP:   "192.0.2.1",
		RequestID:  "req-1",
		PrevHash:   "sha256:00",
	}
}

func TestEvent_RoundTrip(t *testing.T) {
	in := sampleEvent()
	if got := EventToTransport(EventToDomain(in)); !reflect.DeepEqual(got, in) {
		t.Errorf("round trip = %+v, want %+v", got, in)
	}
}

func TestEvent_AttributesAreCopied(t *testing.T) {
	in := sampleEvent()
	dom := EventToDomain(in)
	dom.Attributes["k"] = "changed"
	if in.Attributes["k"] != "v" {
		t.Error("EventToDomain shares its Attributes map with the input")
	}
	back := EventToTransport(dom)
	back.Attributes["k"] = "again"
	if dom.Attributes["k"] != "changed" {
		t.Error("EventToTransport shares its Attributes map with the input")
	}
	if EventToDomain(transport.AuditEvent{}).Attributes != nil {
		t.Error("an event with no attributes gained a map")
	}
}

func TestFilterToDomain(t *testing.T) {
	f := transport.AuditFilter{
		From: time.Unix(1, 0).UTC(), To: time.Unix(2, 0).UTC(),
		Actor: "a", Resource: "r", Action: "x", Outcome: "success",
		TenantID: "acme", Limit: 7, Cursor: "c",
	}
	got := FilterToDomain(f)
	want := audit.Filter{
		From: f.From, To: f.To, Actor: "a", Resource: "r", Action: "x",
		Outcome: "success", TenantID: "acme", Limit: 7, Cursor: "c",
	}
	if got != want {
		t.Errorf("FilterToDomain = %+v, want %+v", got, want)
	}
	if got.Untenanted {
		t.Error("a client filter set Untenanted")
	}
}

// TestFilterToDomain_KeepsTheTenantOnTheWire is the defect this package
// exists for: the QUIC frame's payload is an audit.Filter, and encoding the
// SDK's filter directly named the tenant "tenantID", which the server never
// read.
func TestFilterToDomain_KeepsTheTenantOnTheWire(t *testing.T) {
	b, err := json.Marshal(FilterToDomain(transport.AuditFilter{TenantID: "acme"}))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded audit.Filter
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.TenantID != "acme" {
		t.Errorf("server decoded tenant %q from %s; want acme", decoded.TenantID, b)
	}
	if !strings.Contains(string(b), `"tenant_id":"acme"`) {
		t.Errorf("payload %s does not carry tenant_id", b)
	}
}

func TestPageToTransport(t *testing.T) {
	if PageToTransport(nil) != nil {
		t.Error("a nil page converted to non-nil")
	}
	ev := EventToDomain(sampleEvent())
	got := PageToTransport(&audit.Page{Events: []audit.AuditEvent{ev}, Next: "n", Total: 3})
	if got.Next != "n" || got.Total != 3 || len(got.Events) != 1 {
		t.Fatalf("page = %+v", got)
	}
	if !reflect.DeepEqual(got.Events[0], sampleEvent()) {
		t.Errorf("event = %+v, want %+v", got.Events[0], sampleEvent())
	}
	if empty := PageToTransport(&audit.Page{}); empty.Events == nil {
		t.Error("an empty page converted to a nil Events slice")
	}
}

// TestPage_DecodesTheServersWireShape: a page decoded from the server's JSON
// keeps every event field, which decoding straight into transport.AuditPage
// did not.
func TestPage_DecodesTheServersWireShape(t *testing.T) {
	wire, err := json.Marshal(audit.Page{Events: []audit.AuditEvent{EventToDomain(sampleEvent())}, Total: 1})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var page audit.Page
	if err := json.Unmarshal(wire, &page); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	got := PageToTransport(&page).Events[0]
	if got.TenantID != "acme" || got.ClientIP != "192.0.2.1" || got.RequestID != "req-1" || got.PrevHash != "sha256:00" {
		t.Errorf("decoded event = %+v; want tenant, client IP, request id and chain hash kept", got)
	}
}
