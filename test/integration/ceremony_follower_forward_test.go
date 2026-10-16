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

//go:build integration

package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// httpCeremonyInitializeOnce POSTs /v1/ceremony/initialize to port exactly
// once (no retry loop) using the harness's mTLS client, so the caller can
// tell a forwarded answer from the leader apart from a slow-settling retry.
func httpCeremonyInitializeOnce(t *testing.T, ca *testCA, port int, adminCertPEM []byte) (int, string) {
	t.Helper()
	client := mtlsHTTPClient(t, ca, 15*time.Second)
	body, err := json.Marshal(map[string]any{
		"so_pin":         harnessSOPIN,
		"user_pin":       harnessUserPIN,
		"admin_cert_pem": string(adminCertPEM),
	})
	if err != nil {
		t.Fatalf("httpCeremonyInitializeOnce: marshal: %v", err)
	}
	url := fmt.Sprintf("https://127.0.0.1:%d/v1/ceremony/initialize", port)
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("httpCeremonyInitializeOnce: post to port %d: %v", port, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("httpCeremonyInitializeOnce: read body: %v", err)
	}
	return resp.StatusCode, string(respBody)
}

// TestCeremonyInitialize_RetryAgainstFollowerForwardsToLeader is the
// regression test for the fix: a retry of `qrdb init` -- here, the raw POST
// /v1/ceremony/initialize it drives -- issued against a FOLLOWER of an
// already-operational 3-node raft cluster must be answered by the LEADER's
// admin-rebind logic in one hop, not refused locally.
//
// Before this fix, pkg/service.CeremonyServiceWithLeaderGate refused on a
// follower with *service.AdminCredentialNotLeaderError (naming the leader's
// address, "must run on the Raft leader") and nothing retried the request
// anywhere; the REST handler rendered that refusal as a 503 naming the
// leader and telling the operator to re-run `qrdb init` there by hand --
// docs/firstboot/architecture.md recorded exactly that as the "Known
// limitation". This test issues the request to the follower exactly once
// -- no retry loop -- and asserts the follower's answer is the LEADER's
// own *service.AdminAlreadyBoundError (the admin this cluster already
// bound at start-up still holds a certificate), never the follower's own
// local refusal.
func TestCeremonyInitialize_RetryAgainstFollowerForwardsToLeader(t *testing.T) {
	// SPIFFE node certificates are required for on-behalf leader-forwarding
	// to re-authorize the forwarded request as the ORIGINAL caller (admin)
	// rather than falling back to the forwarding node's own unprivileged
	// identity; see startThreeNodeClusterSPIFFE's doc comment and
	// clusterTestTrustDomain.
	adminPorts, _, ca := startThreeNodeClusterSPIFFE(t, "pebble")
	leaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 30*time.Second)

	var followerPort int
	for _, p := range adminPorts {
		if p != leaderPort {
			followerPort = p
			break
		}
	}
	if followerPort == 0 {
		t.Fatal("no follower admin port found in a 3-node cluster")
	}
	waitForSettledFollowerView(t, ca, followerPort, 30*time.Second)

	// The SAME admin certificate startThreeNodeClusterSPIFFE's own ceremony
	// already bound: AdminAlreadyBoundError triggers on the admin already
	// holding ANY live binding, regardless of what certificate a retry
	// presents, so this test exercises the retry-refusal path, not a
	// successful (re)bind.
	_, certPath, _ := ca.adminIdentity(t, t.TempDir())
	freshCertPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read admin cert: %v", err)
	}

	code, body := httpCeremonyInitializeOnce(t, ca, followerPort, freshCertPEM)

	// The OLD, unfixed behavior: a 503 naming the leader and telling the
	// caller to retry there by hand.
	if strings.Contains(body, "must run on the Raft leader") {
		t.Fatalf("POST /v1/ceremony/initialize to follower (port %d) was refused locally instead of forwarded: status %d, body %s",
			followerPort, code, body)
	}

	// The NEW, fixed behavior: the follower forwarded the whole request to
	// the leader in one hop, and the leader's admin-rebind logic answered.
	if !strings.Contains(body, "already holds a certificate binding") {
		t.Fatalf("POST /v1/ceremony/initialize to follower (port %d): status %d, body %s -- want the leader's AdminAlreadyBoundError, proving the request was forwarded",
			followerPort, code, body)
	}

	// Confirm the leader answers the IDENTICAL request with the SAME
	// refusal directly, so the follower's answer above is proven to be a
	// relay of the leader's own, not some other coincidental 4xx/5xx text.
	leaderCode, leaderBody := httpCeremonyInitializeOnce(t, ca, leaderPort, freshCertPEM)
	if leaderCode != code {
		t.Fatalf("leader status %d != follower (forwarded) status %d", leaderCode, code)
	}
	if !strings.Contains(leaderBody, "already holds a certificate binding") {
		t.Fatalf("leader direct answer %q does not carry AdminAlreadyBoundError; fixture assumption wrong", leaderBody)
	}
}

// TestCeremonyInitialize_FollowerForwardBoundedAcrossLeaderDeath kills the
// leader a follower forwards POST /v1/ceremony/initialize to. A submission
// through a survivor must come back within the caller's deadline whatever the
// election is doing -- httpCeremonyInitializeOnce's client timeout is that
// deadline, and a forward stuck on the dead leader fails the test there --
// and, once a new leader is elected, a submission through its follower must
// be forwarded to it and answered by its admin-rebind logic.
func TestCeremonyInitialize_FollowerForwardBoundedAcrossLeaderDeath(t *testing.T) {
	adminPorts, _, ca, cmds := startThreeNodeClusterSPIFFENodes(t, "pebble")
	leader := slices.Index(adminPorts, waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second))

	_, certPath, _ := ca.adminIdentity(t, t.TempDir())
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read admin cert: %v", err)
	}

	if err := spawnedDaemon(t, cmds[leader]).Kill(); err != nil {
		t.Fatalf("kill leader node %d: %v", leader+1, err)
	}
	survivors := slices.Delete(slices.Clone(adminPorts), leader, leader+1)

	start := time.Now()
	code, body := httpCeremonyInitializeOnce(t, ca, survivors[0], certPEM)
	t.Logf("port %d answered %d after %v with the leader just killed: %s", survivors[0], code, time.Since(start), body)

	newLeader := waitForLeaderViaAdmin(t, ca, survivors, 60*time.Second)
	follower := survivors[0]
	if follower == newLeader {
		follower = survivors[1]
	}
	// The follower may still name the dead leader for a moment; that answer
	// is bounded too, and the next submission reaches the new one.
	deadline := time.Now().Add(60 * time.Second)
	code, body = httpCeremonyInitializeOnce(t, ca, follower, certPEM)
	for !strings.Contains(body, "already holds a certificate binding") {
		if time.Now().After(deadline) {
			t.Fatalf("POST /v1/ceremony/initialize to follower (port %d) after the new leader (port %d) was elected: status %d, body %s -- want the new leader's AdminAlreadyBoundError",
				follower, newLeader, code, body)
		}
		time.Sleep(500 * time.Millisecond)
		code, body = httpCeremonyInitializeOnce(t, ca, follower, certPEM)
	}

	if leaderCode, leaderBody := httpCeremonyInitializeOnce(t, ca, newLeader, certPEM); leaderCode != code {
		t.Fatalf("new leader status %d (%s) != follower (forwarded) status %d", leaderCode, leaderBody, code)
	}
}
