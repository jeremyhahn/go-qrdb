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

import "testing"

// TestStorageBarrier_AuditRecordsComponentEvents: a component built over the
// barrier records its security events on the barrier's audit log, against the
// barrier's root key path.
func TestStorageBarrier_AuditRecordsComponentEvents(t *testing.T) {
	logger := &mockAuditLogger{}
	b := &StorageBarrier{config: BarrierConfig{RootKeyPath: "tenants/acme/seal/root-key", AuditLogger: logger}}
	b.Audit("holder-record-conflict", "deny", map[string]string{"username": "alice"})
	events := logger.Events()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	e := events[0]
	if e.Action != "holder-record-conflict" || e.Outcome != "deny" || e.Resource != "barrier" ||
		e.ResourceID != "tenants/acme/seal/root-key" || e.Details["username"] != "alice" {
		t.Fatalf("event = %+v", e)
	}
}

// TestStorageBarrier_AuditWithoutLoggerIsANoOp: a barrier with no audit
// logger drops the event without failing.
func TestStorageBarrier_AuditWithoutLoggerIsANoOp(t *testing.T) {
	b := &StorageBarrier{config: BarrierConfig{RootKeyPath: "root-key"}}
	b.Audit("holder-record-conflict", "deny", nil)
}
