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

// operator_tls_holder_cli_test.go drives the holder surface of a node started
// with operator-supplied TLS (--tls-ca-file/--tls-cert-file/--tls-key-file)
// through the real CLI binary over every transport. Such a node has no Key A,
// so it has no cluster CA: every holder operation that only verifies is
// served, and only the two that sign -- holder enrolment and sign-csr --
// refuse (docs/certificate-authority/architecture.md).
package integration_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// caNotInitialized is ceremony.ErrCANotInitialized's text, which every
// transport carries through to the CLI.
const caNotInitialized = "CA not initialized"

// failedPreconditionByProto is how each transport's CLI error renders the
// FailedPrecondition class ErrCANotInitialized maps to (pkg/api/errclass).
var failedPreconditionByProto = map[string]string{
	"rest": "status 409",
	"grpc": "code = FailedPrecondition",
	"quic": "failed precondition",
	"mcp":  "failed precondition",
	"unix": "code = FailedPrecondition",
}

// TestOperatorTLSHolder_CLI_PerProtocol proves, on one operator-TLS node and
// over every transport, that the verify-only holder reads succeed, that a
// Shamir tenant barrier-init (which enrols, so signs) refuses with
// ErrCANotInitialized and leaves the barrier uninitialized, and that
// sign-csr refuses.
func TestOperatorTLSHolder_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startTenantEntitlementNode(t)

	for _, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			testOperatorTLSHolderOverProtocol(t, node, proto)
		})
	}
}

func testOperatorTLSHolderOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()
	workDir := t.TempDir()

	// The system holder reads verify and never sign.
	if out, err := runCLIProto(t, node, proto, "--output", "json", "barrier", "officer", "list"); err != nil {
		t.Fatalf("[%s] barrier officer list: %v\noutput: %s", proto.name, err, out)
	}
	out, err := runCLIProto(t, node, proto, "--output", "json", "ceremony", "status")
	if err != nil {
		t.Fatalf("[%s] ceremony status: %v\noutput: %s", proto.name, err, out)
	}
	var status ceremonyStatusJSON
	decodeJSON(t, out, &status)
	if status.State != "operational" {
		t.Fatalf("[%s] ceremony status: state = %q, want operational", proto.name, status.State)
	}

	// A self-managed tenant brings its own admin certificate: there is no
	// cluster CA here to sign a CSR with.
	tenantID := fmt.Sprintf("otls-%s-%d", proto.name, time.Now().UnixNano())
	bootCertPath, bootKeyPath := createTenantWithAdminCert(t, node, proto, tenantID,
		"Operator TLS Holders", "raft", false, tenantID+"-admin")
	bootConn := tenantCLIArgsForProto(node, proto, bootCertPath, bootKeyPath)

	tenantStatus := func() transport.TenantBarrierStatusResponse {
		t.Helper()
		out, err := runCLI(t, node.cli, append(append([]string{}, bootConn...),
			"--output", "json", "tenant", "barrier-status", "--id", tenantID)...)
		if err != nil {
			t.Fatalf("[%s] tenant barrier-status: %v\noutput: %s", proto.name, err, out)
		}
		var st transport.TenantBarrierStatusResponse
		decodeJSON(t, out, &st)
		return st
	}
	if st := tenantStatus(); st.Initialized {
		t.Fatalf("[%s] tenant barrier-status before init: %+v, want uninitialized", proto.name, st)
	}

	// Enrolment signs each officer's certificate, so it is the one tenant
	// barrier-init path that needs the CA.
	csrAlice, _ := generateOfficerCSR(t, workDir, "alice")
	csrBob, _ := generateOfficerCSR(t, workDir, "bob")
	out, err = runCLI(t, node.cli, append(append([]string{}, bootConn...), "tenant", "barrier-init",
		"--id", tenantID, "--strategy", "shamir", "--threshold", "2", "--shares", "2",
		"--officers", "alice="+csrAlice, "--officers", "bob="+csrBob)...)
	if err == nil || !strings.Contains(out, caNotInitialized) {
		t.Fatalf("[%s] shamir tenant barrier-init: err = %v, want %q\noutput: %s", proto.name, err, caNotInitialized, out)
	}
	wantClass, ok := failedPreconditionByProto[proto.name]
	if !ok {
		t.Fatalf("[%s] no FailedPrecondition rendering recorded for this transport", proto.name)
	}
	if !strings.Contains(out, wantClass) {
		t.Fatalf("[%s] shamir tenant barrier-init: want FailedPrecondition (%q)\noutput: %s", proto.name, wantClass, out)
	}
	if st := tenantStatus(); st.Initialized {
		t.Fatalf("[%s] a refused enrolment initialized the tenant barrier: %+v", proto.name, st)
	}

	// sign-csr checks the ceremony state before the CA, and it serves only
	// while the ceremony is enrolling. An operator-TLS node never enrols:
	// the system ceremony reaches enrolling only through an M-of-N init,
	// whose officer enrolment signs and so refuses here. The state check
	// therefore answers, ahead of ErrCANotInitialized, on every transport.
	csrSvc, _ := generateOfficerCSR(t, workDir, "svc-"+proto.name)
	out, err = runCLIProto(t, node, proto, "ceremony", "sign-csr", "--username", "svc-"+proto.name,
		"--so-pin", harnessSOPIN, "--csr-file", csrSvc, "--role", "custodian")
	if err == nil || !strings.Contains(out, "sign-csr requires the enrolling state") {
		t.Fatalf("[%s] ceremony sign-csr: err = %v, want the enrolling-state refusal\noutput: %s", proto.name, err, out)
	}
	if strings.Contains(out, "BEGIN CERTIFICATE") {
		t.Fatalf("[%s] ceremony sign-csr returned a certificate on a node with no CA: %s", proto.name, out)
	}
}
