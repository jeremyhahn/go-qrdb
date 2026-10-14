//go:build integration

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

package integration_test

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	resttransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/rest"
)

// TestTenantBarrierInitProof_CLI_PerProtocol proves on every network
// transport that a tenant barrier is initialized only by a caller that
// proves possession of the first admin's key over the connection it
// initializes on (docs/multi-tenancy, init/begin + init/complete):
//
//  1. no signer: the node asks a proof, and the SDK refuses to complete
//     without one;
//  2. the wrong key: the first admin's certificate on the connection, but a
//     signature by another key, is refused and initializes nothing;
//  3. the real CLI, `qrdb tenant barrier-init` with the first admin's own
//     --cert-file/--key-file, proves possession and initializes the barrier.
func TestTenantBarrierInitProof_CLI_PerProtocol(t *testing.T) {
	node := startTenantEntitlementNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testTenantBarrierInitProofOverProtocol(t, node, proto)
		})
	}
}

func testTenantBarrierInitProofOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	tenantID := fmt.Sprintf("mt06-%s-%d", proto.name, time.Now().UnixNano())
	adminCert, adminKey := createTenantWithAdminCert(t, node, proto, tenantID, "Init Proof Tenant", "raft", false, tenantID+"-admin")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := sdkClientForProtocolAs(t, node, proto, adminCert, adminKey)
	defer client.Close()
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("[%s] connect as the first admin: %v", proto.name, err)
	}

	// 1. No signer.
	_, err := client.Tenants().TenantBarrierInit(ctx, &transport.TenantBarrierInitRequest{TenantID: tenantID, Strategy: "none"})
	var need *transport.InitSignerRequiredError
	if !errors.As(err, &need) {
		t.Fatalf("[%s] barrier init without a signer: err = %T (%v); want *transport.InitSignerRequiredError", proto.name, err, err)
	}

	// 2. The wrong key.
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Tenants().TenantBarrierInit(ctx, &transport.TenantBarrierInitRequest{TenantID: tenantID, Strategy: "none", Signer: other})
	if err == nil || !strings.Contains(err.Error(), "barrier init proof") {
		t.Fatalf("[%s] barrier init signed by another key: err = %v; want the proof refused", proto.name, err)
	}

	adminCLI := func(args ...string) (string, error) {
		return runCLI(t, node.cli, append(tenantCLIArgsForProto(node, proto, adminCert, adminKey), args...)...)
	}
	out, err := adminCLI("tenant", "barrier-status", "--id", tenantID)
	if err != nil {
		t.Fatalf("[%s] barrier-status after the refused attempts: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "Initialized: false") {
		t.Fatalf("[%s] a refused proof initialized the barrier:\n%s", proto.name, out)
	}

	// 3. The real CLI with the first admin's own key.
	if out, err = adminCLI("tenant", "barrier-init", "--id", tenantID, "--strategy", "none"); err != nil {
		t.Fatalf("[%s] tenant barrier-init with the first admin's key: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] tenant barrier-init output = %q; want OK", proto.name, out)
	}

	// The window has closed: the same identity, with no role yet, is refused
	// a second initialization, which is only true of an initialized barrier.
	if out, err = adminCLI("tenant", "barrier-init", "--id", tenantID, "--strategy", "none"); err == nil {
		t.Fatalf("[%s] a second barrier-init succeeded: %s", proto.name, out)
	}
}

// initTenantBarrierAsFirstAdmin initializes tenantID's barrier with the none
// strategy over the REST listener at baseURL, as the first admin whose
// onboarded certificate and key are certPEM/keyPEM. It goes through the SDK,
// which runs init/begin and init/complete on one connection and proves
// possession of the key.
func initTenantBarrierAsFirstAdmin(t *testing.T, baseURL, caPath string, certPEM, keyPEM []byte, tenantID string) {
	t.Helper()
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair for the first admin: %v", err)
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "admin-cert.pem"), filepath.Join(dir, "admin-key.pem")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	tr, err := resttransport.New(
		transport.WithAddress(baseURL),
		transport.WithMTLS(certPath, keyPath, caPath),
		transport.WithTimeout(15*time.Second),
	)
	if err != nil {
		t.Fatalf("REST transport: %v", err)
	}
	client, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("SDK client: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("connect as the first admin: %v", err)
	}
	if _, err := client.Tenants().TenantBarrierInit(ctx, &transport.TenantBarrierInitRequest{
		TenantID: tenantID,
		Strategy: "none",
		Signer:   pair.PrivateKey.(crypto.Signer),
	}); err != nil {
		t.Fatalf("tenant barrier init as the first admin: %v", err)
	}
}
