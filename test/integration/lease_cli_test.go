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
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
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
	t.Parallel()
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

	// ttl on a never-granted lease ID must fail closed with the specific
	// not-found classification, not merely "some error" and not merely
	// text containing the words "lease not found" -- the pre-fix generic
	// engine error ("engine: invalid query: lease not found") contains
	// that same substring, so a Contains check here would pass on both the
	// correct classification and the go-qrdb#387 regression it exists to
	// catch. Every transport's client reconstructs *transport.LeaseNotFoundError
	// from its own not-found status/code (see sdk/go/transport/*), and
	// ONLY that reconstruction; any other status renders the raw wire
	// error instead, whose text differs from this exactly. cmd/qrdb prints
	// err.Error() verbatim to stderr (SilenceErrors is set; see
	// cmd/qrdb/main.go), so asserting an exact match on
	// transport.LeaseNotFoundError.Error() pins the classification itself,
	// not just a word that happens to appear in it.
	const neverGrantedID = 999999999
	out, err = runCLIProto(t, node, proto, "lease", "ttl", strconv.FormatInt(neverGrantedID, 10))
	if err == nil {
		t.Fatalf("[%s] lease ttl 999999999 (never granted) succeeded, want error (output: %s)", proto.name, out)
	}
	wantNotFound := (&transport.LeaseNotFoundError{LeaseID: neverGrantedID}).Error()
	if got := strings.TrimSpace(out); got != wantNotFound {
		t.Fatalf("[%s] lease ttl 999999999 (never granted): output = %q, want exactly %q (the typed not-found classification, not just matching text)",
			proto.name, got, wantNotFound)
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

	// ttl on a revoked lease must fail closed with the same not-found
	// classification a never-granted ID gets -- a revoked lease and one
	// that never existed are deliberately indistinguishable from the read
	// path up (see docs/leases/engineering.md), so this assertion is the
	// one place that equivalence is actually exercised end to end. As
	// above, this is an exact match on the typed classification, not a
	// substring the pre-fix generic error would also satisfy.
	out, err = runCLIProto(t, node, proto, "lease", "ttl", leaseID)
	if err == nil {
		t.Fatalf("[%s] lease ttl %s (revoked) succeeded, want error (output: %s)", proto.name, leaseID, out)
	}
	wantRevokedNotFound := (&transport.LeaseNotFoundError{LeaseID: granted.ID}).Error()
	if got := strings.TrimSpace(out); got != wantRevokedNotFound {
		t.Fatalf("[%s] lease ttl %s (revoked): output = %q, want exactly %q (the typed not-found classification, not just matching text)",
			proto.name, leaseID, got, wantRevokedNotFound)
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

// leaseRenewTTL is the short TTL the keep-alive and expiry test grants: long
// enough for a CLI round trip on every transport, short enough that renewing
// past it and then waiting for the sweeper stays within seconds.
const leaseRenewTTL = 3

// renewalWindow is what a lease's renewals guarantee a test that must not
// depend on its host's pace. A grant or renewal sent at s that succeeds sets
// the server's deadline no earlier than s plus the TTL, so an answer that
// arrives before then must find the lease and its keys. An answer arriving
// later reflects the host's pace, not the contract, and is not judged.
type renewalWindow struct {
	ttl   time.Duration
	until time.Time
}

func newRenewalWindow(ttlSeconds int64) *renewalWindow {
	return &renewalWindow{ttl: time.Duration(ttlSeconds) * time.Second}
}

// renewed records a grant or renewal, sent at sent, that succeeded.
func (w *renewalWindow) renewed(sent time.Time) { w.until = sent.Add(w.ttl) }

// guaranteed reports whether an answer that has just arrived falls inside
// the window the last successful renewal opened.
func (w *renewalWindow) guaranteed() bool { return time.Now().Before(w.until) }

// TestLease_CLI_KeepAliveAndExpiry_PerProtocol drives renewal and expiry
// through the CLI binary on every transport: `lease keep-alive` keeps a short
// lease -- and the key bound to it -- alive well past the TTL it was granted
// with, and once keep-alives stop the leader's sweeper revokes the lease and
// deletes the key, after which `lease ttl` reports the lease not found and
// `lease keep-alive` cannot revive it.
func TestLease_CLI_KeepAliveAndExpiry_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testLeaseKeepAliveAndExpiryOverProtocol(t, node, proto)
		})
	}
}

func testLeaseKeepAliveAndExpiryOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	key := "leasecli/" + proto.name + "/renewed"
	window := newRenewalWindow(leaseRenewTTL)
	sent := time.Now()
	out, err := runCLIProto(t, node, proto, "--output", "json", "lease", "grant", "--ttl", strconv.Itoa(leaseRenewTTL))
	if err != nil {
		t.Fatalf("[%s] lease grant --ttl %d: %v\noutput: %s", proto.name, leaseRenewTTL, err, out)
	}
	window.renewed(sent)
	var granted leaseInfoDisplay
	if jerr := json.Unmarshal([]byte(out), &granted); jerr != nil {
		t.Fatalf("[%s] decode `lease grant` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	leaseID := strconv.FormatInt(granted.ID, 10)
	if out, err = runCLIProto(t, node, proto, "kv", "put", key, "renewed-value", "--lease-id", leaseID); err != nil {
		t.Fatalf("[%s] kv put %s --lease-id %s: %v\noutput: %s", proto.name, key, leaseID, err, out)
	}

	// Renew every second for twice the granted TTL. Each keep-alive either
	// renews the lease back to its full TTL, and its key is there, or answers
	// lease-not-found, and its key is gone; never a renewal followed by a
	// missing key. Only the window a renewal guarantees is judged, so a host
	// too loaded to renew within the TTL lets the lease expire rather than
	// failing the test.
	renewEvery := time.NewTicker(time.Second)
	defer renewEvery.Stop()
	renewUntil := time.After(2 * leaseRenewTTL * time.Second)
renewing:
	for {
		sent = time.Now()
		out, err = runCLIProto(t, node, proto, "--output", "json", "lease", "keep-alive", leaseID)
		if err != nil && strings.Contains(out, "not found") {
			if window.guaranteed() {
				t.Fatalf("[%s] lease keep-alive %s answered lease-not-found before %v, the deadline the last renewal guaranteed\noutput: %s",
					proto.name, leaseID, window.until, out)
			}
			got, existsErr := runCLIProto(t, node, proto, "kv", "exists", key)
			if existsErr != nil || strings.TrimSpace(got) != "false" {
				t.Fatalf("[%s] kv exists %s after lease-not-found = %q, err=%v, want %q", proto.name, key, got, existsErr, "false")
			}
			t.Logf("[%s] the host renewed too slowly to keep the %ds lease: it expired and took its key", proto.name, leaseRenewTTL)
			break renewing
		}
		if err != nil {
			t.Fatalf("[%s] lease keep-alive %s: %v\noutput: %s", proto.name, leaseID, err, out)
		}
		var renewed leaseInfoDisplay
		if jerr := json.Unmarshal([]byte(out), &renewed); jerr != nil {
			t.Fatalf("[%s] decode `lease keep-alive` JSON: %v\noutput: %s", proto.name, jerr, out)
		}
		if renewed.ID != granted.ID || renewed.RemainingTTL < leaseRenewTTL-1 {
			t.Fatalf("[%s] lease keep-alive %s = %+v, want lease %d back at its full %ds",
				proto.name, leaseID, renewed, granted.ID, leaseRenewTTL)
		}
		window.renewed(sent)
		out, err = runCLIProto(t, node, proto, "kv", "exists", key)
		if err != nil {
			t.Fatalf("[%s] kv exists %s after a renewal: %v\noutput: %s", proto.name, key, err, out)
		}
		if strings.TrimSpace(out) != "true" {
			if window.guaranteed() {
				t.Fatalf("[%s] kv exists %s before %v, the deadline a successful renewal guaranteed, = %q, want %q",
					proto.name, key, window.until, out, "true")
			}
			t.Logf("[%s] kv exists after a renewal answered past its guaranteed deadline without the key: not judged", proto.name)
		}
		select {
		case <-renewEvery.C:
		case <-renewUntil:
			break renewing
		}
	}

	// Stop renewing: the sweeper revokes the lease and deletes its key.
	awaitClusterCondition(t, 4*leaseRenewTTL*time.Second, proto.name+" expiry of the unrenewed lease's key", func() error {
		got, existsErr := runCLIProto(t, node, proto, "kv", "exists", key)
		if existsErr != nil {
			return existsErr
		}
		if strings.TrimSpace(got) != "false" {
			return &leaseKeyPresentError{Key: key}
		}
		return nil
	})

	out, err = runCLIProto(t, node, proto, "lease", "ttl", leaseID)
	wantNotFound := (&transport.LeaseNotFoundError{LeaseID: granted.ID}).Error()
	if err == nil || strings.TrimSpace(out) != wantNotFound {
		t.Fatalf("[%s] lease ttl %s (expired) = %q, err=%v, want exactly %q", proto.name, leaseID, out, err, wantNotFound)
	}
	out, err = runCLIProto(t, node, proto, "lease", "keep-alive", leaseID)
	if err == nil || !strings.Contains(out, "not found") {
		t.Fatalf("[%s] lease keep-alive %s (expired) = %q, err=%v, want a lease-not-found failure", proto.name, leaseID, out, err)
	}
}
