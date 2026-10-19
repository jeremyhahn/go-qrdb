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

// join_request_cli_test.go drives `qrdb admin join-request list/approve/
// reject` through the real CLI binary against every transport, proving the
// tokenless member-side CSR approval queue end to end: a joiner that
// presents NO token is queued rather than refused (pkg/clusterenroll's
// pending_join.go), an operator reviews and decides through the CLI on
// whichever transport they are connected with, and the joiner's own retry
// (modeled here the same way join_token_cli_test.go's redemption is: a
// direct clusterenroll.JoinCSRHTTP call, the exact client-side function a
// real joining node's --bootstrap-join-url configuration invokes) picks up
// the decision on its next poll.
package integration_test

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-truststrap/pkg/spkipin"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterenroll"
)

// pendingJoinRequestCLIResponse mirrors transport.PendingJoinRequest's wire
// JSON shape, decoded from `admin join-request list --output json`.
type pendingJoinRequestCLIResponse struct {
	ID          string    `json:"id"`
	NodeURI     string    `json:"node_uri,omitempty"`
	ReplicaID   uint64    `json:"replica_id,omitempty"`
	SubmittedAt time.Time `json:"submitted_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// submitTokenlessCSR submits a fresh, tokenless CSR to node's SPKI/Direct
// join route via clusterenroll.JoinCSRHTTP, mirroring join_token_cli_test.
// go's redeemJoinToken -- the same client-side function a real joining
// node's --bootstrap-join-url configuration (with no
// --bootstrap-join-token-file/-env) invokes. A tokenless submission is
// accepted into the approval queue rather than refused, so this always
// returns *clusterenroll.JoinPendingError on the first call; its RequestID
// names the queue entry an operator reviews.
func submitTokenlessCSR(t *testing.T, node *selfIssuedCANode, nodeURI string) (csrDER []byte, requestID string) {
	t.Helper()
	leafPEM, err := os.ReadFile(node.leafCertPath)
	if err != nil {
		t.Fatalf("submitTokenlessCSR: read leaf cert: %v", err)
	}
	block, _ := pem.Decode(leafPEM)
	if block == nil {
		t.Fatalf("submitTokenlessCSR: no PEM block in %s", node.leafCertPath)
	}
	leafCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("submitTokenlessCSR: parse leaf cert: %v", err)
	}
	pin := spkipin.ComputeSPKIPin(leafCert)

	csrDER = generateJoinCSR(t, nodeURI)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err = clusterenroll.JoinCSRHTTP(ctx, &clusterenroll.JoinHTTPClientOptions{
		ServerURL: node.adminURL,
		SPKIPin:   pin,
	}, csrDER, nodeURI)
	var pending *clusterenroll.JoinPendingError
	if !errors.As(err, &pending) {
		t.Fatalf("submitTokenlessCSR: first submission = %v, want *clusterenroll.JoinPendingError", err)
	}
	return csrDER, pending.RequestID
}

// pollTokenlessCSR re-presents the identical CSR submitTokenlessCSR already
// queued, modeling the retry Join's own poll loop performs, and returns
// whatever clusterenroll.JoinCSRHTTP reports: a signed leaf PEM once
// approved, or the typed error (still pending, or a terminal
// *clusterenroll.JoinRefusedError once rejected).
func pollTokenlessCSR(t *testing.T, node *selfIssuedCANode, csrDER []byte, nodeURI string) ([]byte, error) {
	t.Helper()
	leafPEM, err := os.ReadFile(node.leafCertPath)
	if err != nil {
		t.Fatalf("pollTokenlessCSR: read leaf cert: %v", err)
	}
	block, _ := pem.Decode(leafPEM)
	if block == nil {
		t.Fatalf("pollTokenlessCSR: no PEM block in %s", node.leafCertPath)
	}
	leafCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("pollTokenlessCSR: parse leaf cert: %v", err)
	}
	pin := spkipin.ComputeSPKIPin(leafCert)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return clusterenroll.JoinCSRHTTP(ctx, &clusterenroll.JoinHTTPClientOptions{
		ServerURL: node.adminURL,
		SPKIPin:   pin,
	}, csrDER, nodeURI)
}

// TestAdminJoinRequest_CLI_PerProtocol drives the tokenless approval queue
// against a self-issued-CA node, sharing one node and one queued request
// across all 5 protocol subtests for `list` (a read-only, non-consuming
// call -- unlike the join listener itself, `admin join-request list` is an
// ordinary admin verb and is not behind the per-source join-attempt rate
// limiter, so running it 5 times costs nothing). The approve and reject
// decisions, and the CSR submissions/redemptions they gate, each run once
// -- see join_token_cli_test.go's TestAdminJoinTokenMint_CLI_PerProtocol
// for why redemption traffic against the join listener is proven once,
// globally, rather than once per transport.
func TestAdminJoinRequest_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startSelfIssuedCAMultiProtocolNode(t)

	selfReportedURIA := spiffe.NodeURI(clusterTestTrustDomain, "self-reported-a")
	csrA, requestID := submitTokenlessCSR(t, node, selfReportedURIA)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			out, err := runCLIProto(t, node.multiProtoNode, proto, "--output", "json", "admin", "join-request", "list")
			if err != nil {
				t.Fatalf("[%s] admin join-request list: %v\noutput: %s", proto.name, err, out)
			}
			var pending []pendingJoinRequestCLIResponse
			if jerr := json.Unmarshal([]byte(out), &pending); jerr != nil {
				t.Fatalf("[%s] decode `admin join-request list` JSON: %v\noutput: %s", proto.name, jerr, out)
			}
			found := false
			for _, p := range pending {
				if p.ID == requestID {
					found = true
					if !p.ExpiresAt.After(time.Now()) {
						t.Fatalf("[%s] admin join-request list: ExpiresAt %s is not in the future", proto.name, p.ExpiresAt)
					}
				}
			}
			if !found {
				t.Fatalf("[%s] admin join-request list: request %s not present in %+v", proto.name, requestID, pending)
			}

			// Table-format sanity.
			tableOut, err := runCLIProto(t, node.multiProtoNode, proto, "admin", "join-request", "list")
			if err != nil {
				t.Fatalf("[%s] admin join-request list (table): %v\noutput: %s", proto.name, err, tableOut)
			}
			if !strings.Contains(tableOut, requestID) {
				t.Fatalf("[%s] admin join-request list (table): missing ID %s in output: %s", proto.name, requestID, tableOut)
			}
		})
	}

	// Approve, over REST: binds the queued CSR to an operator-chosen
	// identity, never the joiner's self-reported one (JoinCSRHTTP above
	// passed nodeURI="").
	const approvedNodeID = "join-request-approved-node"
	approveOut, err := runCLIProto(t, node.multiProtoNode, multiProtocols[0], "admin", "join-request", "approve",
		requestID, "--node-id", approvedNodeID)
	if err != nil {
		t.Fatalf("admin join-request approve: %v\noutput: %s", err, approveOut)
	}
	if !strings.Contains(approveOut, "OK") {
		t.Fatalf("admin join-request approve: expected OK, got: %s", approveOut)
	}

	certPEM, err := pollTokenlessCSR(t, node, csrA, selfReportedURIA)
	if err != nil {
		t.Fatalf("poll after approval: %v", err)
	}
	wantURI := spiffe.NodeURI(clusterTestTrustDomain, approvedNodeID)
	uris := leafURISANs(t, certPEM)
	if len(uris) != 1 || uris[0] != wantURI {
		t.Fatalf("signed leaf URI SANs = %v, want exactly [%s] (the operator-bound identity, never the joiner's self-reported one)",
			uris, wantURI)
	}

	// Approved and delivered: no longer awaiting a decision.
	listOut, err := runCLIProto(t, node.multiProtoNode, multiProtocols[0], "--output", "json", "admin", "join-request", "list")
	if err != nil {
		t.Fatalf("admin join-request list (post-approval): %v\noutput: %s", err, listOut)
	}
	var postApproval []pendingJoinRequestCLIResponse
	if jerr := json.Unmarshal([]byte(listOut), &postApproval); jerr != nil {
		t.Fatalf("decode `admin join-request list` JSON (post-approval): %v\noutput: %s", jerr, listOut)
	}
	for _, p := range postApproval {
		if p.ID == requestID {
			t.Fatalf("admin join-request list (post-approval): approved request %s still listed as pending", requestID)
		}
	}

	// Reject, over a DIFFERENT transport than approve used.
	selfReportedURIB := spiffe.NodeURI(clusterTestTrustDomain, "self-reported-b")
	csrB, requestID2 := submitTokenlessCSR(t, node, selfReportedURIB)
	rejectProto := multiProtocols[1]
	rejectOut, err := runCLIProto(t, node.multiProtoNode, rejectProto, "admin", "join-request", "reject",
		requestID2, "--reason", "unrecognized source")
	if err != nil {
		t.Fatalf("[%s] admin join-request reject: %v\noutput: %s", rejectProto.name, err, rejectOut)
	}
	if !strings.Contains(rejectOut, "OK") {
		t.Fatalf("[%s] admin join-request reject: expected OK, got: %s", rejectProto.name, rejectOut)
	}

	_, err = pollTokenlessCSR(t, node, csrB, selfReportedURIB)
	if err == nil {
		t.Fatal("poll after rejection unexpectedly succeeded")
	}
	var refused *clusterenroll.JoinRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("poll after rejection: got %v (%T), want *clusterenroll.JoinRefusedError", err, err)
	}
	if refused.Reason != "join request rejected by operator" {
		t.Fatalf("poll after rejection: refusal reason = %q, want %q", refused.Reason, "join request rejected by operator")
	}
}
