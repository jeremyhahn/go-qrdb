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
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// TestMVCCConcurrency_StalledAuditInstanceAppend_VerifySeesConsistentChain:
// an OpVerifiableLog AuditAppend advances the tenant chain's in-memory head
// before its batch publishes, as OpAuditAppend does, so it takes the
// exclusive apply path: a verify beside its stalled commit sees the chain as
// it was before the append, never the new head over the old snapshot.
func TestMVCCConcurrency_StalledAuditInstanceAppend_VerifySeesConsistentChain(t *testing.T) {
	fs := newWALStallFS()
	s := NewMVCCStateMachine(&pebble.Options{FS: fs})
	s.SetLogs(vlog.NewChain())
	if _, err := s.Open(context.Background(), t.TempDir(), make(chan struct{})); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	appendEntry := func(idx uint64, action string) sm.Entry {
		ev := audit.AuditEvent{
			TenantID:  "acme",
			Resource:  "barrier",
			Action:    action,
			Actor:     "operator",
			Timestamp: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
		}
		body, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		return sm.Entry{Index: idx, Cmd: mustEncode(protocol.EncodeVerifiableLogAuditAppend(vlog.AuditLogID("acme"), [][]byte{body}))}
	}
	applyMVCCEntries(t, s, []sm.Entry{appendEntry(1, "unseal")})

	read := func() string {
		res, err := s.Lookup(context.Background(), audit.VerifyRequest{TenantID: "acme"})
		if err != nil {
			return "verify: " + err.Error()
		}
		report, ok := res.(*audit.ChainContinuityReport)
		if !ok {
			return fmt.Sprintf("verify result %T", res)
		}
		return fmt.Sprintf("verified %t, records %d, head %d, reason %q", report.Verified, report.Records, report.Head, report.Reason)
	}

	got := readBesideExclusiveBatch(t, s, fs, []sm.Entry{appendEntry(2, "seal")}, read)
	want := read()
	if got != want {
		t.Errorf("verify beside the audit-instance append = %s, want %s", got, want)
	}
	if want != `verified true, records 2, head 2, reason ""` {
		t.Errorf("verify after the audit-instance append = %s, want a verified two-record chain", want)
	}
}
