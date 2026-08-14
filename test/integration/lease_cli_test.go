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

// lease_cli_test.go drives the entire `qrdb lease` command tree (grant,
// list, ttl, keep-alive, revoke) through the real client CLI binary
// (cmd/qrdb/lease.go), against every transport (REST, gRPC, QUIC, MCP,
// Unix) using the shared startMultiProtocolNode harness
// (multi_protocol_helpers_test.go). The whole tree was previously
// exercised only via the raw REST helpers (httpLeaseGrant et al.), never
// through the CLI binary, and never multi-protocol.
package integration_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// leaseInfoDisplay mirrors cmd/qrdb/lease.go's printLeaseInfo JSON shape
// (transport.LeaseInfo), decoded from `lease grant/keep-alive/ttl --output
// json`.
type leaseInfoDisplay struct {
	ID           int64    `json:"id"`
	TTL          int64    `json:"ttl"`
	GrantedTTL   int64    `json:"granted_ttl"`
	RemainingTTL int64    `json:"remaining_ttl"`
	Keys         [][]byte `json:"keys"`
}

// TestLease_CLI_PerProtocol drives the full `qrdb lease` lifecycle -- grant,
// list, ttl, keep-alive, attached-key deletion on revoke, and CLI-side
// validation errors -- through the CLI binary against every transport
// sharing one multiProtoNode.
func TestLease_CLI_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testLeaseLifecycleOverProtocol(t, node, proto)
		})
	}
}

func testLeaseLifecycleOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	prefix := "leasecli/" + proto.name + "/"
	key := prefix + "attached"

	// --- grant -----------------------------------------------------------
	out, err := runCLIProto(t, node, proto, "--output", "json", "lease", "grant", "--ttl", "60")
	if err != nil {
		t.Fatalf("[%s] lease grant --ttl 60: %v\noutput: %s", proto.name, err, out)
	}
	var granted leaseInfoDisplay
	if jerr := json.Unmarshal([]byte(out), &granted); jerr != nil {
		t.Fatalf("[%s] decode `lease grant` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if granted.ID == 0 {
		t.Fatalf("[%s] lease grant --ttl 60: ID = 0, want nonzero (output: %s)", proto.name, out)
	}
	if granted.TTL != 60 || granted.GrantedTTL != 60 {
		t.Fatalf("[%s] lease grant --ttl 60: TTL=%d GrantedTTL=%d, want both 60", proto.name, granted.TTL, granted.GrantedTTL)
	}
	// granted.RemainingTTL must report the real, live remaining TTL for
	// the freshly granted lease, the same as `lease ttl` does below.
	// pkg/service/lease_service.go's LeaseService.Grant reads the lease
	// back through TimeToLive (the same pattern KeepAlive already used)
	// instead of leaving RemainingTTL at its zero value.
	if granted.RemainingTTL <= 0 || granted.RemainingTTL > granted.GrantedTTL {
		t.Fatalf("[%s] lease grant --ttl 60: RemainingTTL=%d GrantedTTL=%d, want RemainingTTL in (0, GrantedTTL]",
			proto.name, granted.RemainingTTL, granted.GrantedTTL)
	}
	leaseID := strconv.FormatInt(granted.ID, 10)

	// grant with no --ttl: cobra must reject the required flag before it
	// ever reaches the server.
	out, err = runCLIProto(t, node, proto, "lease", "grant")
	if err == nil {
		t.Fatalf("[%s] lease grant with no --ttl succeeded, want error (output: %s)", proto.name, out)
	}

	// --- attach a key to the lease via kv put --lease-id ----------------
	out, err = runCLIProto(t, node, proto, "kv", "put", key, "leased-value", "--lease-id", leaseID)
	if err != nil {
		t.Fatalf("[%s] kv put %s --lease-id %s: %v\noutput: %s", proto.name, key, leaseID, err, out)
	}

	// --- ttl ---------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "lease", "ttl", leaseID)
	if err != nil {
		t.Fatalf("[%s] lease ttl %s: %v\noutput: %s", proto.name, leaseID, err, out)
	}
	var ttlInfo leaseInfoDisplay
	if jerr := json.Unmarshal([]byte(out), &ttlInfo); jerr != nil {
		t.Fatalf("[%s] decode `lease ttl` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if ttlInfo.ID != granted.ID {
		t.Fatalf("[%s] lease ttl %s: ID = %d, want %d", proto.name, leaseID, ttlInfo.ID, granted.ID)
	}
	// Unlike `lease grant` (see the KNOWN PRODUCT BUG note above),
	// `lease ttl` goes through the correct TimeToLive/SyncRead path and
	// must report the real, nonzero remaining TTL.
	if ttlInfo.RemainingTTL <= 0 || ttlInfo.RemainingTTL > ttlInfo.GrantedTTL {
		t.Fatalf("[%s] lease ttl %s: RemainingTTL=%d GrantedTTL=%d, want RemainingTTL in (0, GrantedTTL]",
			proto.name, leaseID, ttlInfo.RemainingTTL, ttlInfo.GrantedTTL)
	}
	found := false
	for _, k := range ttlInfo.Keys {
		if string(k) == key {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("[%s] lease ttl %s: Keys = %v, want it to contain %s", proto.name, leaseID, ttlInfo.Keys, key)
	}

	// ttl with a non-numeric lease ID must fail closed.
	out, err = runCLIProto(t, node, proto, "lease", "ttl", "not-a-number")
	if err == nil {
		t.Fatalf("[%s] lease ttl not-a-number succeeded, want error (output: %s)", proto.name, out)
	}

	// ttl on a never-granted lease ID must fail closed.
	out, err = runCLIProto(t, node, proto, "lease", "ttl", "999999999")
	if err == nil {
		t.Fatalf("[%s] lease ttl 999999999 (never granted) succeeded, want error (output: %s)", proto.name, out)
	}

	// --- list ------------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "lease", "list")
	if err != nil {
		t.Fatalf("[%s] lease list: %v\noutput: %s", proto.name, err, out)
	}
	var listed []leaseInfoDisplay
	if jerr := json.Unmarshal([]byte(out), &listed); jerr != nil {
		t.Fatalf("[%s] decode `lease list` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	listHas := false
	for _, l := range listed {
		if l.ID == granted.ID {
			listHas = true
			break
		}
	}
	if !listHas {
		t.Fatalf("[%s] lease list = %+v, want it to contain lease %d", proto.name, listed, granted.ID)
	}

	out, err = runCLIProto(t, node, proto, "lease", "list")
	if err != nil {
		t.Fatalf("[%s] lease list (table): %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, leaseID) {
		t.Fatalf("[%s] lease list (table) = %q, want it to contain lease ID %s", proto.name, out, leaseID)
	}

	// --- keep-alive: extends the TTL back to (approximately) the full
	// granted TTL. -------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "lease", "keep-alive", leaseID)
	if err != nil {
		t.Fatalf("[%s] lease keep-alive %s: %v\noutput: %s", proto.name, leaseID, err, out)
	}
	var kaInfo leaseInfoDisplay
	if jerr := json.Unmarshal([]byte(out), &kaInfo); jerr != nil {
		t.Fatalf("[%s] decode `lease keep-alive` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if kaInfo.RemainingTTL <= 0 || kaInfo.RemainingTTL > kaInfo.GrantedTTL {
		t.Fatalf("[%s] lease keep-alive %s: RemainingTTL=%d GrantedTTL=%d, want RemainingTTL in (0, GrantedTTL]",
			proto.name, leaseID, kaInfo.RemainingTTL, kaInfo.GrantedTTL)
	}

	// keep-alive with a non-numeric lease ID must fail closed.
	out, err = runCLIProto(t, node, proto, "lease", "keep-alive", "not-a-number")
	if err == nil {
		t.Fatalf("[%s] lease keep-alive not-a-number succeeded, want error (output: %s)", proto.name, out)
	}

	// --- revoke: removes the lease and deletes its attached key. -------
	out, err = runCLIProto(t, node, proto, "lease", "revoke", leaseID)
	if err != nil {
		t.Fatalf("[%s] lease revoke %s: %v\noutput: %s", proto.name, leaseID, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] lease revoke %s: expected OK, got: %s", proto.name, leaseID, out)
	}

	out, err = runCLIProto(t, node, proto, "kv", "exists", key)
	if err != nil || strings.TrimSpace(out) != "false" {
		t.Fatalf("[%s] kv exists %s after lease revoke = %q, err=%v, want %q (key attached to revoked lease must be deleted)",
			proto.name, key, out, err, "false")
	}

	// ttl on a revoked lease must fail closed.
	out, err = runCLIProto(t, node, proto, "lease", "ttl", leaseID)
	if err == nil {
		t.Fatalf("[%s] lease ttl %s (revoked) succeeded, want error (output: %s)", proto.name, leaseID, out)
	}

	// revoke with a non-numeric lease ID must fail closed.
	out, err = runCLIProto(t, node, proto, "lease", "revoke", "not-a-number")
	if err == nil {
		t.Fatalf("[%s] lease revoke not-a-number succeeded, want error (output: %s)", proto.name, out)
	}

	// Revoking an already-revoked/nonexistent lease is a BY-DESIGN
	// idempotent no-op success, not an error: pkg/engine/mvcc_engine.go's
	// handleLeaseRevoke explicitly documents (and implements) that Raft
	// state machine handlers must be idempotent so a duplicate/cleanup
	// revoke proposal does not produce a fatal Apply error that would
	// stall consensus -- it catches lease.NotFoundError from the
	// underlying Lessor and returns success instead. Confirm that
	// documented contract explicitly rather than asserting an error.
	out, err = runCLIProto(t, node, proto, "lease", "revoke", leaseID)
	if err != nil {
		t.Fatalf("[%s] lease revoke %s (already revoked) failed, want idempotent OK per pkg/engine/mvcc_engine.go's "+
			"handleLeaseRevoke contract: %v\noutput: %s", proto.name, leaseID, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] lease revoke %s (already revoked): expected idempotent OK, got: %s", proto.name, leaseID, out)
	}
}
