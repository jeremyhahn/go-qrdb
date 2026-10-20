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

// tenant_barrier_shamir_cluster_cli_test.go drives a Shamir tenant barrier
// init on a three-node cluster through the real CLI binary over every
// transport, end to end: the init on one node returns the holder
// certificates (with admin_pending when that node cannot complete the
// tenant's first admin yet), the holders claim their shares and
// quorum-unseal the other two nodes, and the first admin then completes on
// its own -- which the test observes, on every node, as that admin's
// certificate being accepted (docs/shamir-custody/api.md, "admin_pending").
package integration_test

import (
	"crypto/ecdsa"
	"fmt"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestTenantBarrierInitShamirRoster_CLI_ThreeNode_PerProtocol runs a Shamir
// tenant init with an inline roster on a three-node cluster over every
// transport, unseals the remaining nodes with the holders' quorum, and
// requires the tenant's first admin usable on every node.
func TestTenantBarrierInitShamirRoster_CLI_ThreeNode_PerProtocol(t *testing.T) {
	nodes := startSelfIssuedCAMultiProtocolCluster(t)

	for _, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			testTenantBarrierInitShamirRosterThreeNodeOverProtocol(t, nodes, proto)
		})
	}
}

func testTenantBarrierInitShamirRosterThreeNodeOverProtocol(t *testing.T, nodes [3]*multiProtoNode, proto multiProtocol) {
	t.Helper()
	workDir := t.TempDir()
	tenantID := fmt.Sprintf("tbs3-%s-%d", proto.name, time.Now().UnixNano())
	initNode := nodes[0]

	// The tenant's bootstrap identity is the certificate its first admin is
	// onboarded with; it drives barrier-init on the trust-on-first-init path.
	bootCertPath, bootKeyPath := createTenantWithAdminCert(t, initNode, proto, tenantID,
		"Tenant Shamir Three Node", "raft", false, tenantID+"-admin")

	csrAlice, keyAlice := generateOfficerCSR(t, workDir, "alice")
	csrBob, keyBob := generateOfficerCSR(t, workDir, "bob")
	officerKeys := map[string]*ecdsa.PrivateKey{"alice": keyAlice, "bob": keyBob}

	initArgs := append(tenantCLIArgsForProto(initNode, proto, bootCertPath, bootKeyPath),
		"--output", "json", "tenant", "barrier-init",
		"--id", tenantID, "--strategy", "shamir", "--threshold", "2", "--shares", "2",
		"--officers", "alice="+csrAlice, "--officers", "bob="+csrBob)
	out, err := runCLI(t, initNode.cli, initArgs...)
	if err != nil {
		t.Fatalf("[%s] tenant barrier-init with inline roster: %v\noutput: %s", proto.name, err, out)
	}
	var initResp transport.TenantBarrierInitResponse
	decodeJSON(t, out, &initResp)
	if initResp.Strategy != "shamir" || len(initResp.Holders) != 2 {
		t.Fatalf("[%s] expected a shamir init with 2 holder certs, got %+v", proto.name, initResp)
	}
	t.Logf("[%s] barrier-init admin_pending=%v", proto.name, initResp.AdminPending)

	officers := make(map[string]*officer, len(initResp.Holders))
	for _, h := range initResp.Holders {
		key, ok := officerKeys[h.Username]
		if !ok {
			t.Fatalf("[%s] barrier-init returned an unexpected holder %q", proto.name, h.Username)
		}
		certPath, keyPath := writeOfficerKeyPair(t, workDir, h.Username, key, []byte(h.CertPEM))
		officers[h.Username] = &officer{username: h.Username, key: key, certPath: certPath, keyPath: keyPath}
	}
	alice, bob := officers["alice"], officers["bob"]
	if alice == nil || bob == nil {
		t.Fatalf("[%s] expected both alice and bob back from barrier-init, got %+v", proto.name, initResp.Holders)
	}

	claimTenantEnvelope(t, initNode, proto, workDir, tenantID, alice)
	claimTenantEnvelope(t, initNode, proto, workDir, tenantID, bob)

	// Every other node's barrier is still sealed: only these holders can
	// unseal it, and each quorum that completes may complete the admin.
	for i, n := range nodes[1:] {
		tenantQuorumSubmit(t, n, proto, tenantID, "unseal", alice)
		outcome := tenantQuorumSubmit(t, n, proto, tenantID, "unseal", bob)
		if !outcome.Complete {
			t.Fatalf("[%s] node %d: expected the unseal quorum to complete once both holders submitted, got %+v", proto.name, i+2, outcome)
		}
	}

	// Until the first admin is complete its certificate names no account, so
	// the boot certificate's barrier-status succeeding on a node is the
	// admin being usable there. A status read is itself one of the paths
	// that completes it, within the admin's 2-minute completion budget.
	for i, n := range nodes {
		statusArgs := append(tenantCLIArgsForProto(n, proto, bootCertPath, bootKeyPath),
			"--output", "json", "tenant", "barrier-status", "--id", tenantID)
		var status transport.TenantBarrierStatusResponse
		var lastOut string
		if !pollUntil(3*time.Minute, 500*time.Millisecond, func() bool {
			out, err := runCLI(t, n.cli, statusArgs...)
			lastOut = out
			if err != nil {
				return false
			}
			decodeJSON(t, out, &status)
			return status.Initialized && !status.Sealed && status.Strategy == "shamir"
		}) {
			t.Fatalf("[%s] node %d: the tenant's first admin never became usable on an unsealed shamir barrier; last barrier-status output: %s",
				proto.name, i+1, lastOut)
		}
	}
}
