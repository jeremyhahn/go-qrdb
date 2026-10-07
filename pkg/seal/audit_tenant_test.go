// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package seal

import (
	"encoding/json"
	"testing"
)

// TestAuditEvent_TenantIDRoundTrips asserts a tenant-scoped event's
// TenantID survives a JSON round trip, which is how this event travels from
// a per-tenant barrier component to an adapter on the tenant-queryable
// audit plane.
func TestAuditEvent_TenantIDRoundTrips(t *testing.T) {
	ev := AuditEvent{Subject: "alice", TenantID: "acme", Action: "holder-claim", Resource: "barrier", ResourceID: "root-key", Outcome: "success"}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out AuditEvent
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.TenantID != "acme" {
		t.Fatalf("TenantID = %q, want acme", out.TenantID)
	}
}

// TestAuditEvent_EmptyTenantIDOmittedFromJSON asserts a system-barrier
// event (no tenant) does not carry an explicit empty tenant_id field,
// matching every other omitempty field on this type.
func TestAuditEvent_EmptyTenantIDOmittedFromJSON(t *testing.T) {
	ev := AuditEvent{Subject: "system", Action: "unseal", Resource: "barrier", Outcome: "success"}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	if _, present := fields["tenant_id"]; present {
		t.Fatalf("tenant_id should be omitted when empty, got %v", fields)
	}
}
