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
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// onboardCmds returns an unmarked tenant.onboard command and its marked
// retry, as OpAuditAppend (instance false) or the verifiable-log
// AuditAppend sub-type (instance true).
func onboardCmds(t *testing.T, instance bool) (unmarked, marked []byte) {
	t.Helper()
	ev := auditEvent("acme")
	ev.Action = audit.ActionTenantOnboard
	ev.RequestID = "req-1"
	retry := ev
	retry.AppendOnce = true
	encode := func(ev audit.AuditEvent) []byte {
		if !instance {
			return auditAppendCmd(t, "acme", ev)
		}
		raw, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		return mustEncode(protocol.EncodeVerifiableLogAuditAppend(vlog.AuditLogID("acme"), [][]byte{raw}))
	}
	return encode(ev), encode(retry)
}

// TestPebbleStateMachine_MarkedOnboardRetryAfterUnmarkedAppendsOnce applies
// an unmarked onboarding event and then its marked retry through both audit
// opcodes: in separate Update calls, in one Update call (the retry finds the
// unmarked event staged in the same pebble batch) and across a reopen of the
// state machine. Each leaves one event: the retry answers the head sequence
// 1, and the next ordinary event takes sequence 2.
func TestPebbleStateMachine_MarkedOnboardRetryAfterUnmarkedAppendsOnce(t *testing.T) {
	for _, instance := range []bool{false, true} {
		for _, mode := range []string{"separate applies", "one batch", "reopen between"} {
			name := map[bool]string{false: "audit append", true: "instance"}[instance] + ", " + mode
			t.Run(name, func(t *testing.T) {
				unmarked, marked := onboardCmds(t, instance)
				dir := filepath.Join(t.TempDir(), "db")
				open := func() *PebbleStateMachine {
					s := NewPebbleStateMachine(nil)
					s.SetLogs(vlog.NewChain())
					if _, err := s.Open(context.Background(), dir, nil); err != nil {
						t.Fatalf("Open: %v", err)
					}
					return s
				}
				s := open()
				var results []sm.Result
				switch mode {
				case "one batch":
					results = applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: unmarked}, {Index: 2, Cmd: marked}})
				case "separate applies":
					results = append(applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: unmarked}}),
						applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: marked}})...)
				default:
					results = applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: unmarked}})
					if err := s.Close(context.Background()); err != nil {
						t.Fatalf("Close: %v", err)
					}
					s = open()
					results = append(results, applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: marked}})...)
				}
				t.Cleanup(func() { _ = s.Close(context.Background()) })
				if results[0].Value != 1 || results[1].Value != 1 {
					t.Fatalf("results = %d, %d; want 1, 1 (the retry appends nothing)", results[0].Value, results[1].Value)
				}
				next := applyExtEntries(t, s, []sm.Entry{{Index: 3, Cmd: auditAppendCmd(t, "acme", auditEvent("acme"))}})
				if next[0].Value != 2 {
					t.Fatalf("next event's sequence = %d, want 2 (one onboarding event)", next[0].Value)
				}
			})
		}
	}
}
