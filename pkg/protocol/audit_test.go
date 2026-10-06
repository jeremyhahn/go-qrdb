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

package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
)

func fixedAuditEvent(tenantID string) audit.AuditEvent {
	return audit.AuditEvent{
		TenantID:  tenantID,
		Actor:     "operator",
		Resource:  "barrier",
		Action:    "unseal",
		Outcome:   audit.OutcomeSuccess,
		Timestamp: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
	}
}

func TestEncodeAuditAppend_RoundTrips(t *testing.T) {
	ev := fixedAuditEvent("acme")
	cmd, err := EncodeAuditAppend("acme", ev)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if cmd[0] != OpAuditAppend {
		t.Fatalf("op = %d, want OpAuditAppend (%d)", cmd[0], OpAuditAppend)
	}
	subtype, tenantID, body, err := DecodeAuditCommand(cmd[1:])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if subtype != AuditSubtypeAppend {
		t.Fatalf("subtype = %d, want AuditSubtypeAppend", subtype)
	}
	if tenantID != "acme" {
		t.Fatalf("tenantID = %q, want %q", tenantID, "acme")
	}
	var decoded audit.AuditEvent
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if decoded.Action != ev.Action || decoded.TenantID != ev.TenantID {
		t.Fatalf("decoded event = %+v, want %+v", decoded, ev)
	}
}

func TestEncodeAuditPrune_RoundTrips(t *testing.T) {
	cmd, err := EncodeAuditPrune("acme", 42)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if cmd[0] != OpAuditAppend {
		t.Fatalf("op = %d, want OpAuditAppend (%d)", cmd[0], OpAuditAppend)
	}
	subtype, tenantID, body, err := DecodeAuditCommand(cmd[1:])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if subtype != AuditSubtypePrune {
		t.Fatalf("subtype = %d, want AuditSubtypePrune", subtype)
	}
	if tenantID != "acme" {
		t.Fatalf("tenantID = %q, want %q", tenantID, "acme")
	}
	if len(body) != 8 {
		t.Fatalf("body length = %d, want 8", len(body))
	}
	if got := binary.BigEndian.Uint64(body); got != 42 {
		t.Fatalf("cutoff = %d, want 42", got)
	}
}

func TestDecodeAuditCommand_RejectsShortPayloads(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
	}{
		{"empty", nil},
		{"shorter than the fixed header", []byte{AuditSubtypeAppend, 0}},
		{"tenant id truncated", []byte{AuditSubtypeAppend, 0, 5, 'a', 'c'}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, err := DecodeAuditCommand(tt.payload)
			var invalid *InvalidCommandError
			if !errors.As(err, &invalid) {
				t.Fatalf("err = %v, want *InvalidCommandError", err)
			}
		})
	}
}

func TestEncodeAuditAppend_RejectsOversizedTenantID(t *testing.T) {
	huge := strings.Repeat("a", 0x10000)
	if _, err := EncodeAuditAppend(huge, fixedAuditEvent(huge)); err == nil {
		t.Fatal("expected an error for an oversized tenant id")
	}
	if _, err := EncodeAuditPrune(huge, 0); err == nil {
		t.Fatal("expected an error for an oversized tenant id")
	}
}

// TestEncodeAuditAppend_RejectsZeroTimestamp is the encode-time half of N3:
// catching this before a command is ever proposed, rather than only at
// apply time (pkg/audit.Chain.Append's identical backstop check).
func TestEncodeAuditAppend_RejectsZeroTimestamp(t *testing.T) {
	ev := fixedAuditEvent("acme")
	ev.Timestamp = time.Time{}
	_, err := EncodeAuditAppend("acme", ev)
	var missing *audit.MissingTimestampError
	if !errors.As(err, &missing) {
		t.Fatalf("err = %v, want *audit.MissingTimestampError", err)
	}
}

// TestEncodeAuditAppend_RejectsTenantMismatch is the encode-time half of
// N3's tenant-mismatch check.
func TestEncodeAuditAppend_RejectsTenantMismatch(t *testing.T) {
	ev := fixedAuditEvent("widgets")
	_, err := EncodeAuditAppend("acme", ev)
	var mismatch *audit.TenantMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("err = %v, want *audit.TenantMismatchError", err)
	}
}

// TestEncodeAuditAppend_EmptyTenantIDIsNotAMismatch asserts the
// platform-event case still encodes: an event with no tenant of its own is
// not a conflict with the routing tenantID.
func TestEncodeAuditAppend_EmptyTenantIDIsNotAMismatch(t *testing.T) {
	ev := fixedAuditEvent("")
	if _, err := EncodeAuditAppend("system", ev); err != nil {
		t.Fatalf("encode: %v", err)
	}
}
