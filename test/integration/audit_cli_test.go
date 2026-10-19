// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

//go:build integration

// audit_cli_test.go drives the `qrdb audit query` command through the real
// client CLI binary (cmd/qrdb), against every transport (REST, gRPC, QUIC,
// MCP, Unix) sharing the multi_protocol_helpers_test.go harness.
//
// PARITY BUG FOUND AND FIXED (see sdk/go/transport/{rest,grpc,unix,quic,mcp}
// /transport_audit.go): before this file was written, `qrdb audit query`
// worked over NO transport at all. sdk/go.Client.Audit() type-asserts the
// active transport against transport.AuditProvider (an `AuditClient()
// AuditClient` accessor); only the embedded transport implemented that
// accessor, so Audit() returned nil on every network transport --
// including REST, whose *Transport already had a complete, working
// QueryAudit implementation that simply had no way to be reached from the
// SDK. Separately, qrdb.v1.AuditService (a gRPC service distinct from
// qrdb.v1.QRDBService) was registered server-side for gRPC and Unix
// (pkg/api/grpc/audit_server.go, pkg/api/unix/server.go) and QUIC/MCP had
// working server-side "audit.Query" / "qrdb_audit_query" handlers
// (pkg/api/quic/audit_frame.go, pkg/api/mcp/audit_tool.go), but none of the
// five SDK transports ever called them. All five transports now implement
// transport.AuditProvider and route QueryAudit to their respective
// server-side implementation.
//
// AUDIT-EMISSION (fixed): IAM mutations now emit compliance AuditEvents.
// pkg/role, pkg/user, and pkg/credentials service methods emit on every
// committed mutation (role create/update/delete, user create/update/delete/
// enable/disable, credential store/submit/rotate/delete/gc), capturing the
// acting identity (Subject + TenantID) from context. The daemon threads the
// compliance AuditLogger into the served IAM services (cmd/qrdbd/start.go).
// TestAudit_CLI_RealFlow_PerProtocol below performs the "do an auditable op,
// then query for it" positive assertion this file previously could not: it
// creates a role over each transport and asserts the resulting create event
// appears via `audit query --resource <...>` / `--action create`.
package integration_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// auditEventCLIJSON mirrors transport.AuditEvent.
type auditEventCLIJSON struct {
	Timestamp  time.Time      `json:"timestamp"`
	Actor      string         `json:"actor"`
	TenantID   string         `json:"tenantID,omitempty"`
	Role       string         `json:"role,omitempty"`
	Resource   string         `json:"resource"`
	Action     string         `json:"action"`
	Outcome    string         `json:"outcome"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// auditPageCLIJSON mirrors transport.AuditPage.
type auditPageCLIJSON struct {
	Events []auditEventCLIJSON `json:"events"`
	Next   string              `json:"next,omitempty"`
	Total  int                 `json:"total"`
}

// TestAudit_CLI_Query_PerProtocol drives `qrdb audit query` -- with no
// filter, with a filter that cannot possibly match anything, with a
// time-range filter, and with an invalid --from value -- through the CLI
// binary against every transport sharing one multiProtoNode. It proves the
// audit-query wiring is symmetric across every transport (the parity bug
// this file's header describes and the sdk/go/transport/*/transport_audit.go
// files fix).
func TestAudit_CLI_Query_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testAuditQueryOverProtocol(t, node, proto)
		})
	}
}

func testAuditQueryOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	// --- query with no filter: must succeed with a well-formed page,
	// regardless of whether any events happen to be present. This is the
	// core parity assertion -- before the fix described in this file's
	// header, this call failed on every transport with
	// AuditClientUnavailableError ("no audit client available").
	out, err := runCLIProto(t, node, proto, "--output", "json", "audit", "query")
	if err != nil {
		t.Fatalf("[%s] audit query (no filter): %v\noutput: %s", proto.name, err, out)
	}
	var page auditPageCLIJSON
	if jerr := json.Unmarshal([]byte(out), &page); jerr != nil {
		t.Fatalf("[%s] decode audit query JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if !auditTotalValid(page.Total, len(page.Events)) {
		t.Fatalf("[%s] audit query (no filter) page = %+v, want total == len(events) (or -1 == \"unknown\" when events is empty -- see pkg/audit's aggregateLogger.Query doc comment: it returns the LAST non-nil child page when every child's page is empty, and the slog-only sink child always reports Total: -1)", proto.name, page)
	}

	// --- query with an actor filter that cannot match anything: proves the
	// filter round-trips to the server and is actually applied, not just
	// silently ignored.
	out, err = runCLIProto(t, node, proto, "--output", "json", "audit", "query",
		"--actor", "definitely-nonexistent-actor-xyz",
	)
	if err != nil {
		t.Fatalf("[%s] audit query (actor filter): %v\noutput: %s", proto.name, err, out)
	}
	var filtered auditPageCLIJSON
	if jerr := json.Unmarshal([]byte(out), &filtered); jerr != nil {
		t.Fatalf("[%s] decode audit query JSON (actor filter): %v\noutput: %s", proto.name, jerr, out)
	}
	if len(filtered.Events) != 0 || !auditTotalValid(filtered.Total, 0) {
		t.Fatalf("[%s] audit query (actor filter matching nothing) = %+v, want 0 events", proto.name, filtered)
	}

	// --- query with a narrow future time-range filter: proves --from/--to
	// parse and round-trip. A window entirely in the future can never
	// contain any event recorded so far.
	future := time.Now().Add(365 * 24 * time.Hour)
	out, err = runCLIProto(t, node, proto, "--output", "json", "audit", "query",
		"--from", future.UTC().Format(time.RFC3339),
		"--to", future.Add(time.Hour).UTC().Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("[%s] audit query (future time range): %v\noutput: %s", proto.name, err, out)
	}
	var futurePage auditPageCLIJSON
	if jerr := json.Unmarshal([]byte(out), &futurePage); jerr != nil {
		t.Fatalf("[%s] decode audit query JSON (future time range): %v\noutput: %s", proto.name, jerr, out)
	}
	if len(futurePage.Events) != 0 {
		t.Fatalf("[%s] audit query (future time range) = %+v, want 0 events", proto.name, futurePage)
	}

	// --- query with --limit: accepted and does not error. -----------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "audit", "query", "--limit", "5")
	if err != nil {
		t.Fatalf("[%s] audit query (--limit 5): %v\noutput: %s", proto.name, err, out)
	}
	var limited auditPageCLIJSON
	if jerr := json.Unmarshal([]byte(out), &limited); jerr != nil {
		t.Fatalf("[%s] decode audit query JSON (--limit 5): %v\noutput: %s", proto.name, jerr, out)
	}
	if len(limited.Events) > 5 {
		t.Fatalf("[%s] audit query (--limit 5) returned %d events, want <= 5", proto.name, len(limited.Events))
	}

	// --- query with an invalid --from value: rejected client-side before
	// ever reaching the server (cmd/qrdb/audit.go's parseAuditTime), so this
	// is transport-independent by construction but confirms every protocol
	// binds to the same CLI validation path.
	out, err = runCLIProto(t, node, proto, "audit", "query", "--from", "not-a-timestamp")
	if err == nil {
		t.Fatalf("[%s] audit query (--from not-a-timestamp) should fail client-side, got: %s", proto.name, out)
	}
	if !strings.Contains(strings.ToLower(out), "invalid --from") {
		t.Fatalf("[%s] audit query (--from not-a-timestamp) error should mention 'invalid --from', got: %s", proto.name, out)
	}
}

// TestAudit_CLI_RealFlow_PerProtocol performs a real auditable IAM operation
// (roles create) over each transport, then queries the audit trail for the
// resulting event. This is the positive "do an op, then find it" assertion
// that proves IAM mutations now emit compliance AuditEvents end-to-end
// (pkg/role/pkg/user/pkg/credentials Emit + daemon wiring in start.go).
func TestAudit_CLI_RealFlow_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testAuditRealFlowOverProtocol(t, node, proto)
		})
	}
}

func testAuditRealFlowOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	// --- perform an auditable IAM mutation: create a role. -----------------
	roleName := fmt.Sprintf("audit-flow-%s-%d", proto.name, time.Now().UnixNano())
	out, err := runCLIProto(t, node, proto, "--output", "json", "roles", "create",
		"--name", roleName,
		"--permissions", "users:read,users:list",
	)
	if err != nil {
		t.Fatalf("[%s] roles create: %v\noutput: %s", proto.name, err, out)
	}
	var created roleCLIJSON
	if jerr := json.Unmarshal([]byte(out), &created); jerr != nil {
		t.Fatalf("[%s] decode roles create JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if created.ID == "" {
		t.Fatalf("[%s] roles create returned empty ID: %s", proto.name, out)
	}
	resource := "role:" + created.ID

	// --- query the audit trail for exactly that create event. The resource
	// filter is an exact match, so this returns only the event emitted by the
	// role create above (not the many other create events on the node).
	ev := findAuditEvent(t, node, proto, "--resource", resource)
	if ev.Action != "create" {
		t.Fatalf("[%s] audit event action = %q, want %q", proto.name, ev.Action, "create")
	}
	if ev.Resource != resource {
		t.Fatalf("[%s] audit event resource = %q, want %q", proto.name, ev.Resource, resource)
	}
	if ev.Actor == "" {
		t.Fatalf("[%s] audit event actor is empty, want the acting identity", proto.name)
	}
	if !strings.Contains(ev.Outcome, "success") {
		t.Fatalf("[%s] audit event outcome = %q, want success", proto.name, ev.Outcome)
	}

	// --- prove the --actor filter also round-trips: querying by the recorded
	// actor plus resource returns the same event.
	byActor := findAuditEvent(t, node, proto, "--actor", ev.Actor, "--resource", resource)
	if byActor.Resource != resource {
		t.Fatalf("[%s] audit query by actor %q returned resource %q, want %q",
			proto.name, ev.Actor, byActor.Resource, resource)
	}
}

// findAuditEvent runs `audit query` with the given filter args and returns the
// single matching event, retrying briefly to absorb any replication/flush lag
// between the mutation commit and the audit write becoming queryable.
func findAuditEvent(t *testing.T, node *multiProtoNode, proto multiProtocol, filterArgs ...string) auditEventCLIJSON {
	t.Helper()
	args := append([]string{"--output", "json", "audit", "query"}, filterArgs...)
	var lastOut string
	var found auditEventCLIJSON
	if !pollUntil(30*time.Second, 200*time.Millisecond, func() bool {
		out, err := runCLIProto(t, node, proto, args...)
		lastOut = out
		if err != nil {
			return false
		}
		var page auditPageCLIJSON
		if jerr := json.Unmarshal([]byte(out), &page); jerr != nil {
			t.Fatalf("[%s] decode audit query JSON: %v\noutput: %s", proto.name, jerr, out)
		}
		if len(page.Events) == 0 {
			return false
		}
		found = page.Events[0]
		return true
	}) {
		t.Fatalf("[%s] audit query %v found no events within timeout\nlast output: %s", proto.name, filterArgs, lastOut)
	}
	return found
}

// auditTotalValid reports whether an AuditPage's Total is consistent with
// numEvents. pkg/audit.Page.Total is documented as "-1 if unknown": when
// AuditFilter matches zero events, the daemon's aggregateLogger (fed by
// pkg/audit.NewAggregateLogger's file/ring-buffer/slog children --
// cmd/qrdbd/start.go) returns whichever child page it consulted last if
// every child's page was empty, and the slog-only sink child always reports
// Total: -1 (it stores nothing to count). So -1 is an equally valid "zero
// matches" result alongside a literal 0.
func auditTotalValid(total, numEvents int) bool {
	if total == numEvents {
		return true
	}
	return numEvents == 0 && total == -1
}

// TestAudit_CLI_Verify_PerProtocol drives `qrdb audit verify` through the CLI
// binary against every transport sharing one multiProtoNode. The node's
// audit trail is freshly created and untampered, so Verified must be true on
// every transport, with Files/Records/Genesis populated -- proving the
// chain-verification call (go-qrdb#453) is wired symmetrically across REST,
// gRPC, QUIC, MCP and Unix, not merely reachable on one of them.
func TestAudit_CLI_Verify_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testAuditVerifyOverProtocol(t, node, proto)
		})
	}
}

// auditVerifyCLIJSON mirrors transport.AuditVerifyResult's wire JSON shape.
type auditVerifyCLIJSON struct {
	Verified bool     `json:"verified"`
	Reason   string   `json:"reason,omitempty"`
	Files    int      `json:"files"`
	Records  int64    `json:"records"`
	Order    []string `json:"order,omitempty"`
	Genesis  bool     `json:"genesis"`
}

func testAuditVerifyOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	// Generate at least one auditable event so Records > 0 on every
	// subtest, regardless of ordering across the shared node.
	roleName := fmt.Sprintf("audit-verify-%s-%d", proto.name, time.Now().UnixNano())
	out, err := runCLIProto(t, node, proto, "--output", "json", "roles", "create",
		"--name", roleName, "--permissions", "users:read")
	if err != nil {
		t.Fatalf("[%s] roles create (to seed the audit trail): %v\noutput: %s", proto.name, err, out)
	}

	out, err = runCLIProto(t, node, proto, "--output", "json", "audit", "verify")
	if err != nil {
		t.Fatalf("[%s] audit verify: %v\noutput: %s", proto.name, err, out)
	}
	var result auditVerifyCLIJSON
	if jerr := json.Unmarshal([]byte(out), &result); jerr != nil {
		t.Fatalf("[%s] decode `audit verify` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if !result.Verified {
		t.Fatalf("[%s] audit verify: Verified=false on a fresh untampered trail, reason=%q", proto.name, result.Reason)
	}
	if result.Files < 1 {
		t.Fatalf("[%s] audit verify: Files=%d, want >= 1", proto.name, result.Files)
	}
	if result.Records < 1 {
		t.Fatalf("[%s] audit verify: Records=%d, want >= 1 (the role create above)", proto.name, result.Records)
	}

	// Table-format sanity: the human-readable rendering must carry the same
	// verdict.
	tableOut, err := runCLIProto(t, node, proto, "audit", "verify")
	if err != nil {
		t.Fatalf("[%s] audit verify (table): %v\noutput: %s", proto.name, err, tableOut)
	}
	if !strings.Contains(tableOut, "Verified: true") {
		t.Fatalf("[%s] audit verify (table): expected 'Verified: true', got: %s", proto.name, tableOut)
	}
}
