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

//go:build integration

package integration_test

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// pendingSweepYAML runs the leader's pending-user reaper often enough for a
// one-second TTL to lapse and be swept within the test's deadline.
const pendingSweepYAML = "auth:\n  pending_user_sweep_interval: 200ms\n"

// pendingReapDeadline bounds how long a lapsed pending user may survive: the
// TTL plus a generous number of sweep intervals.
const pendingReapDeadline = 30 * time.Second

type pendingUserCLIJSON struct {
	ID               uint64 `json:"id,string"`
	Lifecycle        string `json:"lifecycle"`
	PendingExpiresAt string `json:"pending_expires_at"`
}

// TestUsers_CLI_PendingTTL_PerProtocol covers the pending lifecycle with a
// TTL on every transport: a pending account is refused, activating it clears
// its expiry and lets it in, and a pending account left past its TTL is
// deleted by the reaper.
func TestUsers_CLI_PendingTTL_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNodeWithConfig(t, "mvcc", pendingSweepYAML)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testUsersPendingTTLOverProtocol(t, node, proto)
		})
	}
}

func testUsersPendingTTLOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	base := fmt.Sprintf("upend-%s-%d", proto.name, time.Now().UnixNano())

	out, err := runCLIProto(t, node, proto, "--output", "json", "roles", "create",
		"--name", base+"-role",
		"--permissions", "users:read",
	)
	if err != nil {
		t.Fatalf("[%s] roles create (fixture): %v\noutput: %s", proto.name, err, out)
	}
	var role roleCLIJSON
	if jerr := json.Unmarshal([]byte(out), &role); jerr != nil {
		t.Fatalf("[%s] decode roles create JSON (fixture): %v\noutput: %s", proto.name, jerr, out)
	}

	createPending := func(username, ttl string) pendingUserCLIJSON {
		t.Helper()
		out, err := runCLIProto(t, node, proto, "--output", "json", "users", "create",
			"--username", username,
			"--email", username+"@example.com",
			"--roles", role.ID,
			"--pending",
			"--pending-ttl", ttl,
		)
		if err != nil {
			t.Fatalf("[%s] users create --pending --pending-ttl %s: %v\noutput: %s", proto.name, ttl, err, out)
		}
		var u pendingUserCLIJSON
		if jerr := json.Unmarshal([]byte(out), &u); jerr != nil || u.ID == 0 {
			t.Fatalf("[%s] decode users create JSON: %v\noutput: %s", proto.name, jerr, out)
		}
		if u.Lifecycle != "pending" {
			t.Fatalf("[%s] created lifecycle = %q, want pending", proto.name, u.Lifecycle)
		}
		if _, perr := time.Parse(time.RFC3339, u.PendingExpiresAt); perr != nil {
			t.Fatalf("[%s] pending_expires_at %q is not RFC 3339: %v", proto.name, u.PendingExpiresAt, perr)
		}
		return u
	}
	get := func(step, userID string) pendingUserCLIJSON {
		t.Helper()
		out, err := runCLIProto(t, node, proto, "--output", "json", "users", "get", userID)
		if err != nil {
			t.Fatalf("[%s] %s: users get: %v\noutput: %s", proto.name, step, err, out)
		}
		var u pendingUserCLIJSON
		if jerr := json.Unmarshal([]byte(out), &u); jerr != nil {
			t.Fatalf("[%s] %s: decode users get JSON: %v\noutput: %s", proto.name, step, jerr, out)
		}
		return u
	}

	// --- pending with a TTL: refused until activated ------------------------
	held := createPending(base, "1h")
	heldID := strconv.FormatUint(held.ID, 10)
	if got := get("read pending", heldID); got.PendingExpiresAt != held.PendingExpiresAt {
		t.Fatalf("[%s] read pending_expires_at = %q, want %q", proto.name, got.PendingExpiresAt, held.PendingExpiresAt)
	}

	admin := mtlsHTTPClient(t, node.ca, 15*time.Second)
	kp := node.ca.issue(t, t.TempDir(), qrdbdharness.WithSubject(base, tenantid.System))
	bindUserCert(t, admin, node.restPort, held.ID, kp.CertPath)
	userConn := tenantCLIArgsForProto(node, proto, kp.CertPath, kp.KeyPath)
	asUser := func() (string, error) {
		return runQRDB(t, node.cli, userConn, "--output", "json", "users", "get", heldID)
	}
	requireRefusedPromptly(t, proto, "request as the pending account", asUser)

	// --- activate: lifecycle active, expiry cleared, requests succeed -------
	if out, err := runCLIProto(t, node, proto, "users", "enable", heldID); err != nil || !strings.Contains(out, "OK") {
		t.Fatalf("[%s] users enable: %v\noutput: %s", proto.name, err, out)
	}
	if got := get("activate", heldID); got.Lifecycle != "active" || got.PendingExpiresAt != "" {
		t.Fatalf("[%s] after enable: lifecycle %q, pending_expires_at %q; want active and none", proto.name, got.Lifecycle, got.PendingExpiresAt)
	}
	if out, err := asUser(); err != nil {
		t.Fatalf("[%s] request as the activated account: %v\noutput: %s", proto.name, err, out)
	}

	// --- a short TTL left to lapse: the reaper deletes the account ----------
	lapsed := createPending(base+"-lapse", "1s")
	lapsedID := strconv.FormatUint(lapsed.ID, 10)
	deadline := time.Now().Add(pendingReapDeadline)
	for {
		out, err := runCLIProto(t, node, proto, "--output", "json", "users", "get", lapsedID)
		if err != nil {
			if !strings.Contains(strings.ToLower(out), "not found") {
				t.Fatalf("[%s] users get of the lapsed account: want not found, got: %v\noutput: %s", proto.name, err, out)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("[%s] pending account past its TTL still present after %v: %s", proto.name, pendingReapDeadline, out)
		}
	}

	// The activated account is never reaped.
	if got := get("after sweep", heldID); got.Lifecycle != "active" {
		t.Fatalf("[%s] activated account lifecycle = %q after sweeps, want active", proto.name, got.Lifecycle)
	}

	if out, err := runCLIProto(t, node, proto, "users", "delete", heldID); err != nil {
		t.Fatalf("[%s] users delete (fixture cleanup): %v\noutput: %s", proto.name, err, out)
	}
	if out, err := runCLIProto(t, node, proto, "roles", "delete", role.ID); err != nil {
		t.Fatalf("[%s] roles delete (fixture cleanup): %v\noutput: %s", proto.name, err, out)
	}
}
