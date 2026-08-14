//go:build !integration

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

// tenant_officer_cmd_test.go verifies the tenant-scoped Security Officer
// CLI surface (`tenant barrier-officer ...`, `tenant barrier-shamir-seal-vote`)
// defined in barrier.go and grafted onto the tenant command tree from
// root.go, plus the claim-complete share-envelope round-trip UX fix
// (resolveShareInput / renderShareClaimComplete) shared by the system,
// recovery, and tenant officer surfaces.
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// newTenantOfficerTestRoot builds a root command with an injected tenant
// service, mirroring newBarrierTestRoot (barrier_cmd_test.go).
func newTenantOfficerTestRoot(t *testing.T, svc tenant.TenantServicer) *cobra.Command {
	t.Helper()
	return newTestRootCmdWithOpts(newMockKVService(), newMockAdminService(),
		embedded.WithTenantService(svc))
}

// writeTenantOfficerTempFile writes content to a file in a per-test temp
// dir and returns its path.
func writeTenantOfficerTempFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}
	return path
}

// ---------------------------------------------------------------------------
// tenant barrier-officer register
// ---------------------------------------------------------------------------

func TestTenantBarrierOfficerRegisterCmd_TextOutput(t *testing.T) {
	csrPath := writeTenantOfficerTempFile(t, "officer.csr", []byte("CSR-PEM-DATA"))

	var gotTenantID string
	var gotReq *ceremony.RegisterOfficerRequest
	svc := &mockTenantService{
		officerRegisterFn: func(_ context.Context, tenantID string, req *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
			gotTenantID = tenantID
			gotReq = req
			return &ceremony.RegisterOfficerResult{CertPEM: []byte("CERT-PEM"), ShareIndex: 2}, nil
		},
	}
	root := newTenantOfficerTestRoot(t, svc)
	out, err := executeCommand(root, "tenant", "barrier-officer", "register",
		"--tenant-id", "acme", "--username", "alice", "--csr-file", csrPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTenantID != "acme" {
		t.Errorf("expected tenant ID acme forwarded, got %q", gotTenantID)
	}
	if gotReq == nil || gotReq.Username != "alice" || string(gotReq.CSRPEM) != "CSR-PEM-DATA" {
		t.Errorf("register request not forwarded correctly: %+v", gotReq)
	}
	if !strings.Contains(out, "ShareIndex:  2") || !strings.Contains(out, "CERT-PEM") {
		t.Errorf("unexpected output: %q", out)
	}
}

func TestTenantBarrierOfficerRegisterCmd_MissingCSRFile(t *testing.T) {
	root := newTenantOfficerTestRoot(t, &mockTenantService{})
	_, err := executeCommand(root, "tenant", "barrier-officer", "register",
		"--tenant-id", "acme", "--username", "alice",
		"--csr-file", filepath.Join(t.TempDir(), "missing.csr"))
	if err == nil {
		t.Fatal("expected error for missing CSR file")
	}
	if !strings.Contains(err.Error(), "reading CSR file") {
		t.Errorf("expected CSR read error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// tenant barrier-officer list / claim-status
// ---------------------------------------------------------------------------

func tenantOfficerTestStatuses() []*ceremony.OfficerStatus {
	return []*ceremony.OfficerStatus{
		{Username: "alice", ShareIndex: 1, AuthFactor: "software", SharePending: true},
		{Username: "bob", ShareIndex: 2, AuthFactor: "fido2", SharePending: false},
	}
}

func TestTenantBarrierOfficerListCmd_TextOutput(t *testing.T) {
	var gotTenantID string
	svc := &mockTenantService{
		officerListFn: func(_ context.Context, tenantID string) ([]*ceremony.OfficerStatus, error) {
			gotTenantID = tenantID
			return tenantOfficerTestStatuses(), nil
		},
	}
	root := newTenantOfficerTestRoot(t, svc)
	out, err := executeCommand(root, "tenant", "barrier-officer", "list", "--tenant-id", "acme")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTenantID != "acme" {
		t.Errorf("expected tenant ID forwarded, got %q", gotTenantID)
	}
	if !strings.Contains(out, "alice") || !strings.Contains(out, "bob") {
		t.Errorf("unexpected output: %q", out)
	}
}

func TestTenantBarrierOfficerListCmd_JSONOutput(t *testing.T) {
	svc := &mockTenantService{
		officerListFn: func(_ context.Context, _ string) ([]*ceremony.OfficerStatus, error) {
			return tenantOfficerTestStatuses(), nil
		},
	}
	root := newTenantOfficerTestRoot(t, svc)
	out, err := executeCommand(root, "--output", "json", "tenant", "barrier-officer", "list", "--tenant-id", "acme")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "alice") {
		t.Errorf("unexpected JSON output: %q", out)
	}
}

func TestTenantBarrierOfficerClaimStatusCmd_TextOutput(t *testing.T) {
	svc := &mockTenantService{
		officerClaimStatusFn: func(_ context.Context, _ string) ([]*ceremony.OfficerStatus, error) {
			return tenantOfficerTestStatuses(), nil
		},
	}
	root := newTenantOfficerTestRoot(t, svc)
	out, err := executeCommand(root, "tenant", "barrier-officer", "claim-status", "--tenant-id", "acme")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "claimed=no") {
		t.Errorf("expected unclaimed officer marked claimed=no, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// tenant barrier-officer claim-begin / claim-complete
// ---------------------------------------------------------------------------

func TestTenantBarrierOfficerClaimBeginCmd_TextOutput(t *testing.T) {
	var gotTenantID, gotUsername string
	svc := &mockTenantService{
		shareClaimBeginFn: func(_ context.Context, tenantID, username, _ string) (*ceremony.ChallengeResponse, error) {
			gotTenantID, gotUsername = tenantID, username
			return &ceremony.ChallengeResponse{Nonce: []byte{0xAB, 0xCD}}, nil
		},
	}
	root := newTenantOfficerTestRoot(t, svc)
	out, err := executeCommand(root, "tenant", "barrier-officer", "claim-begin",
		"--tenant-id", "acme", "--username", "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTenantID != "acme" || gotUsername != "alice" {
		t.Errorf("parameters not forwarded: tenant=%q username=%q", gotTenantID, gotUsername)
	}
	if !strings.Contains(out, "abcd") {
		t.Errorf("expected nonce hex in output, got: %q", out)
	}
}

func TestTenantBarrierOfficerClaimCompleteCmd_TextOutput(t *testing.T) {
	sigPath := writeTenantOfficerTempFile(t, "claim.sig", []byte{0x01, 0x02})

	envelope := `{"index":1,"threshold":2,"total":3,"value":"YmFzZTY0dmFsdWU="}`
	var gotTenantID, gotUsername, gotNonce string
	var gotSig []byte
	svc := &mockTenantService{
		shareClaimCompleteFn: func(_ context.Context, tenantID, username, nonceHex string, signature []byte, _ string) (*ceremony.ShareClaimResult, error) {
			gotTenantID, gotUsername, gotNonce, gotSig = tenantID, username, nonceHex, signature
			return &ceremony.ShareClaimResult{ShareIndex: 1, ShareBytes: []byte(envelope)}, nil
		},
	}
	root := newTenantOfficerTestRoot(t, svc)
	out, err := executeCommand(root, "tenant", "barrier-officer", "claim-complete",
		"--tenant-id", "acme", "--username", "alice", "--nonce-hex", "abcd", "--sig-file", sigPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTenantID != "acme" || gotUsername != "alice" || gotNonce != "abcd" {
		t.Errorf("parameters not forwarded: tenant=%q username=%q nonce=%q", gotTenantID, gotUsername, gotNonce)
	}
	if string(gotSig) != string([]byte{0x01, 0x02}) {
		t.Errorf("signature bytes not forwarded: %x", gotSig)
	}
	if !strings.Contains(out, "ShareIndex:  1") {
		t.Errorf("expected ShareIndex line, got: %q", out)
	}
	if !strings.Contains(out, envelope) {
		t.Errorf("expected the raw JSON envelope on a 'Share:' line, got: %q", out)
	}
}

func TestTenantBarrierOfficerClaimCompleteCmd_MissingSigFile(t *testing.T) {
	root := newTenantOfficerTestRoot(t, &mockTenantService{})
	_, err := executeCommand(root, "tenant", "barrier-officer", "claim-complete",
		"--tenant-id", "acme", "--username", "alice", "--nonce-hex", "abcd",
		"--sig-file", filepath.Join(t.TempDir(), "missing.sig"))
	if err == nil {
		t.Fatal("expected error for missing signature file")
	}
	if !strings.Contains(err.Error(), "reading signature file") {
		t.Errorf("expected signature read error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// tenant barrier-shamir-seal-vote
// ---------------------------------------------------------------------------

func TestTenantBarrierShamirSealVoteCmd_TextOutput(t *testing.T) {
	var gotTenantID, gotShare string
	svc := &mockTenantService{
		shamirSealVoteFn: func(_ context.Context, tenantID, share string) (*tenant.TenantBarrierUnsealProgress, error) {
			gotTenantID, gotShare = tenantID, share
			return &tenant.TenantBarrierUnsealProgress{Required: 3, Submitted: 1, Complete: false}, nil
		},
	}
	root := newTenantOfficerTestRoot(t, svc)
	out, err := executeCommand(root, "tenant", "barrier-shamir-seal-vote",
		"--tenant-id", "acme", "--share", "share-abc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTenantID != "acme" || gotShare != "share-abc" {
		t.Errorf("parameters not forwarded: tenant=%q share=%q", gotTenantID, gotShare)
	}
	if !strings.Contains(out, "Required:   3") || !strings.Contains(out, "Submitted:  1") {
		t.Errorf("unexpected output: %q", out)
	}
}

func TestTenantBarrierShamirSealVoteCmd_EnvelopeInput(t *testing.T) {
	envelope := `{"index":1,"threshold":2,"total":3,"value":"dGVuYW50LXNoYXJlLXZhbHVl"}`
	var gotShare string
	svc := &mockTenantService{
		shamirSealVoteFn: func(_ context.Context, _, share string) (*tenant.TenantBarrierUnsealProgress, error) {
			gotShare = share
			return &tenant.TenantBarrierUnsealProgress{}, nil
		},
	}
	root := newTenantOfficerTestRoot(t, svc)
	_, err := executeCommand(root, "tenant", "barrier-shamir-seal-vote",
		"--tenant-id", "acme", "--share", envelope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotShare != "dGVuYW50LXNoYXJlLXZhbHVl" {
		t.Errorf("expected the unwrapped envelope value to be forwarded, got %q", gotShare)
	}
}

// ---------------------------------------------------------------------------
// Verb-tree wiring
// ---------------------------------------------------------------------------

func TestTenantBarrierOfficerVerbTree(t *testing.T) {
	root := newRootCmd()
	cases := []struct {
		path  []string
		flags []string
	}{
		{
			path:  []string{"tenant", "barrier-officer", "register"},
			flags: []string{"tenant-id", "username", "csr-file", "authenticator-type", "authenticator-ref"},
		},
		{path: []string{"tenant", "barrier-officer", "list"}, flags: []string{"tenant-id"}},
		{path: []string{"tenant", "barrier-officer", "claim-status"}, flags: []string{"tenant-id"}},
		{
			path:  []string{"tenant", "barrier-officer", "claim-begin"},
			flags: []string{"tenant-id", "username"},
		},
		{
			path:  []string{"tenant", "barrier-officer", "claim-complete"},
			flags: []string{"tenant-id", "username", "nonce-hex", "sig-file"},
		},
		{
			path:  []string{"tenant", "barrier-shamir-seal-vote"},
			flags: []string{"tenant-id", "share"},
		},
	}
	for _, tc := range cases {
		name := strings.Join(tc.path, "/")
		t.Run(name, func(t *testing.T) {
			cmd, _, err := root.Find(tc.path)
			if err != nil {
				t.Fatalf("verb %s not registered: %v", name, err)
			}
			for _, flagName := range tc.flags {
				if cmd.Flags().Lookup(flagName) == nil {
					t.Errorf("verb %s missing flag --%s", name, flagName)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Share-envelope UX fix regression: claim-complete's rendered share
// round-trips into a share-consuming command with NO manual unwrap step
// (see barrier.go resolveShareInput / renderShareClaimComplete).
// ---------------------------------------------------------------------------

// extractShareLine pulls the bare value out of a rendered "Share:" line
// (not "ShareIndex:"/"ShareBytes:", which share that prefix) from
// claim-complete's table output.
func extractShareLine(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "Share:") &&
			!strings.HasPrefix(line, "ShareIndex:") &&
			!strings.HasPrefix(line, "ShareBytes:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Share:"))
		}
	}
	t.Fatalf("no 'Share:' line found in output: %q", out)
	return ""
}

// TestShareEnvelopeRoundTrip_SystemOfficer proves that the JSON envelope
// 'barrier officer claim-complete' prints on its "Share:" line can be fed
// straight into 'barrier unseal-shares --share' with no manual editing:
// the consumer command auto-unwraps the envelope to the bare "value"
// field via resolveShareInput.
func TestShareEnvelopeRoundTrip_SystemOfficer(t *testing.T) {
	sigPath := writeTenantOfficerTempFile(t, "claim.sig", []byte{0xAA})
	env := shareEnvelope{Index: 1, Threshold: 2, Total: 3, Value: "cm9vdC1rZXktc2hhcmU="}
	envBytes, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	var gotShares []string
	svc := &mockBarrierService{
		shareClaimCompleteFn: func(_ context.Context, _, _ string, _ []byte, _ string) (*service.BarrierShareClaimResult, error) {
			return &service.BarrierShareClaimResult{ShareIndex: 1, ShareBytes: envBytes}, nil
		},
		unsealSharesFn: func(_ context.Context, shares []string) error {
			gotShares = shares
			return nil
		},
	}
	root := newBarrierTestRoot(t, svc)

	claimOut, err := executeCommand(root, "barrier", "officer", "claim-complete",
		"--username", "alice", "--nonce-hex", "abcd", "--sig-file", sigPath)
	if err != nil {
		t.Fatalf("claim-complete: %v", err)
	}

	share := extractShareLine(t, claimOut)
	if share != string(envBytes) {
		t.Fatalf("extracted share line %q, want the raw envelope %q", share, envBytes)
	}

	if _, err := executeCommand(root, "barrier", "unseal-shares", "--share", share); err != nil {
		t.Fatalf("unseal-shares with the claimed envelope: %v", err)
	}
	if len(gotShares) != 1 || gotShares[0] != env.Value {
		t.Fatalf("unseal-shares received %v, want the unwrapped value [%q]", gotShares, env.Value)
	}
}

// TestShareEnvelopeRoundTrip_TenantOfficer is the tenant-scoped analogue:
// 'tenant barrier-officer claim-complete's envelope round-trips into
// 'tenant barrier-shamir-seal-vote --share' with no manual unwrap step.
func TestShareEnvelopeRoundTrip_TenantOfficer(t *testing.T) {
	sigPath := writeTenantOfficerTempFile(t, "tenant-claim.sig", []byte{0xBB})
	env := shareEnvelope{Index: 2, Threshold: 2, Total: 3, Value: "dGVuYW50LXJvb3Qta2V5"}
	envBytes, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	var gotShare string
	svc := &mockTenantService{
		shareClaimCompleteFn: func(_ context.Context, _, _, _ string, _ []byte, _ string) (*ceremony.ShareClaimResult, error) {
			return &ceremony.ShareClaimResult{ShareIndex: 2, ShareBytes: envBytes}, nil
		},
		shamirSealVoteFn: func(_ context.Context, _, share string) (*tenant.TenantBarrierUnsealProgress, error) {
			gotShare = share
			return &tenant.TenantBarrierUnsealProgress{Complete: true}, nil
		},
	}
	root := newTenantOfficerTestRoot(t, svc)

	claimOut, err := executeCommand(root, "tenant", "barrier-officer", "claim-complete",
		"--tenant-id", "acme", "--username", "alice", "--nonce-hex", "abcd", "--sig-file", sigPath)
	if err != nil {
		t.Fatalf("claim-complete: %v", err)
	}

	share := extractShareLine(t, claimOut)

	if _, err := executeCommand(root, "tenant", "barrier-shamir-seal-vote",
		"--tenant-id", "acme", "--share", share); err != nil {
		t.Fatalf("barrier-shamir-seal-vote with the claimed envelope: %v", err)
	}
	if gotShare != env.Value {
		t.Fatalf("barrier-shamir-seal-vote received %q, want the unwrapped value %q", gotShare, env.Value)
	}
}

// TestResolveShareInput_BareValuePassesThrough verifies a plain base64
// value (no envelope) is returned unchanged.
func TestResolveShareInput_BareValuePassesThrough(t *testing.T) {
	got, err := resolveShareInput("cGxhaW4tc2hhcmU=")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "cGxhaW4tc2hhcmU=" {
		t.Errorf("got %q, want unchanged input", got)
	}
}

// TestResolveShareInput_MalformedEnvelope verifies a value that looks
// like JSON but is malformed fails closed with a typed error instead of
// silently passing through.
func TestResolveShareInput_MalformedEnvelope(t *testing.T) {
	_, err := resolveShareInput(`{"index":1,`)
	if err == nil {
		t.Fatal("expected an error for a malformed JSON envelope")
	}
}

// TestResolveShareInput_EmptyValueField verifies a well-formed envelope
// with an empty "value" field fails closed.
func TestResolveShareInput_EmptyValueField(t *testing.T) {
	_, err := resolveShareInput(`{"index":1,"threshold":2,"total":3,"value":""}`)
	if err == nil {
		t.Fatal("expected an error for an empty value field")
	}
}

// TestResolveShareInputs_MixedValues verifies resolveShareInputs unwraps
// each element independently and preserves order.
func TestResolveShareInputs_MixedValues(t *testing.T) {
	envelope := `{"index":1,"threshold":2,"total":2,"value":"dW53cmFwcGVk"}`
	got, err := resolveShareInputs([]string{"bare-value", envelope})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0] != "bare-value" || got[1] != "dW53cmFwcGVk" {
		t.Fatalf("got %v, want [bare-value dW53cmFwcGVk]", got)
	}
}
