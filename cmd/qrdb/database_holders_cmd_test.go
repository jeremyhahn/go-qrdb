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

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// TestDatabaseHolderCLI_Lifecycle drives the database share-holder verbs
// against a real database service through the embedded transport: enrolment,
// listing, custody initialization at the created M-of-N, each holder's claim,
// a quorum seal and unseal, a rekey round, and a revocation the remaining
// holders cannot satisfy.
func TestDatabaseHolderCLI_Lifecycle(t *testing.T) {
	fx := servicetest.NewShamirDatabase(t, "vault", 2, 2)
	root := func() *cobra.Command {
		return newTestRootCmdWithOpts(newMockKVService(), newMockAdminService(), embedded.WithDatabaseService(fx.Service))
	}
	ctx := context.Background()
	alice := newCLIHolder(t, "alice", transport.RoleSecurityOfficer, false, false)
	bob := newCLIHolder(t, "bob", transport.RoleCustodian, false, false)
	holders := []*cliHolder{alice, bob}

	for _, h := range holders {
		out := mustCLI(t, ctx, root, append([]string{"--output", "json", "database", "barrier-officer", "register", "--database", "vault"}, h.registerArgs()...)...)
		var resp transport.OfficerRegisterResponse
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			t.Fatalf("register %s: %q: %v", h.name, out, err)
		}
		cert, err := mtls.ParseCertificatePEM([]byte(resp.CertPEM))
		if err != nil {
			t.Fatal(err)
		}
		h.cert = cert
	}
	if out := mustCLI(t, ctx, root, "database", "barrier-officer", "list", "--database", "vault"); !strings.Contains(out, "alice") || !strings.Contains(out, "bob") {
		t.Fatalf("list = %q", out)
	}
	if out := mustCLI(t, ctx, root, "database", "barrier-init-shamir", "--database", "vault"); !strings.Contains(out, "2-of-2 (2 envelopes to claim)") {
		t.Fatalf("init = %q", out)
	}
	for _, h := range holders {
		mustCLI(t, h.ctx(), root, "database", "barrier-officer", "claim", "--database", "vault", "--username", h.name, "--key", h.keyFile, "--out", h.envFile)
	}

	for _, op := range []string{transport.OpSeal, transport.OpUnseal} {
		mustCLI(t, ctx, root, append([]string{"database", "barrier-quorum", "submit", "--database", "vault"}, alice.submitArgs(op)...)...)
		if out := mustCLI(t, ctx, root, append([]string{"database", "barrier-quorum", "submit", "--database", "vault"}, bob.submitArgs(op)...)...); !strings.Contains(out, "Complete:   true") {
			t.Fatalf("%s completion = %q", op, out)
		}
	}

	if out := mustCLI(t, ctx, root, "database", "barrier-rekey", "--database", "vault", "--threshold", "2"); !strings.Contains(out, "Round:") {
		t.Fatalf("rekey = %q", out)
	}
	if _, err := runCLI(root(), ctx, "database", "barrier-officer", "revoke", "--database", "vault", "--username", "bob", "--threshold", "2"); err == nil {
		t.Fatal("revoking a holder below the threshold succeeded")
	}
}

// TestDatabaseHolderCLI_Refusals covers the database verbs' own refusals:
// --database is required, the rekey and revocation verbs take no SO PIN, and
// a database without a Shamir barrier is refused.
func TestDatabaseHolderCLI_Refusals(t *testing.T) {
	fx := servicetest.NewShamirDatabase(t, "vault", 2, 2)
	root := func() *cobra.Command {
		return newTestRootCmdWithOpts(newMockKVService(), newMockAdminService(), embedded.WithDatabaseService(fx.Service))
	}
	ctx := context.Background()
	for _, args := range [][]string{
		{"database", "barrier-officer", "list"},
		{"database", "barrier-init-shamir"},
		{"database", "barrier-rekey", "--threshold", "2"},
		{"database", "barrier-rekey", "--database", "vault", "--threshold", "2", "--so-pin", "1234"},
		{"database", "barrier-officer", "revoke", "--database", "vault", "--username", "bob", "--so-pin", "1234"},
		{"database", "barrier-init-shamir", "--database", "absent"},
		{"database", "barrier-officer", "list", "--database", "absent"},
	} {
		if _, err := runCLI(root(), ctx, args...); err == nil {
			t.Errorf("%v succeeded", args)
		}
	}
	if out := mustCLI(t, ctx, root, "--output", "json", "database", "barrier-officer", "list", "--database", "vault"); strings.TrimSpace(out) != "[]" {
		t.Fatalf("an empty holder list as JSON = %q", out)
	}
}

// TestHolderKind_SOPinGated pins which barriers take the SO PIN.
func TestHolderKind_SOPinGated(t *testing.T) {
	for kind, want := range map[holderKind]bool{holderSystem: true, holderTenant: false, holderDatabase: false} {
		if got := kind.soPinGated(); got != want {
			t.Errorf("kind %d soPinGated = %v, want %v", kind, got, want)
		}
	}
}

// TestOfficerBinding_DatabaseScope drives a Shamir database's custody
// through the desktop binding over a real database service: enrolment,
// listing, initialization, in-process claims bound by proof of possession,
// a quorum seal and unseal, and a rekey round.
func TestOfficerBinding_DatabaseScope(t *testing.T) {
	fx := servicetest.NewShamirDatabase(t, "vault", 2, 2)
	b := NewOfficerBinding(testLogger(t), newTestConnectionBinding(t, newMockKVService(), newMockAdminService(),
		embedded.WithDatabaseService(fx.Service)))
	scope := HolderScope{Database: "vault"}
	holders := []*cliHolder{
		newCLIHolder(t, "alice", transport.RoleSecurityOfficer, false, false),
		newCLIHolder(t, "bob", transport.RoleCustodian, false, false),
	}
	keys := map[*cliHolder]string{}
	for _, h := range holders {
		csr, err := os.ReadFile(h.csrFile)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := b.RegisterHolder(scope, &transport.OfficerRegisterRequest{Username: h.name, Role: h.role, CSRPEM: string(csr)}); err != nil {
			t.Fatalf("RegisterHolder(%s): %v", h.name, err)
		}
		key, err := os.ReadFile(h.keyFile)
		if err != nil {
			t.Fatal(err)
		}
		keys[h] = string(key)
	}
	if list, err := b.ListHolders(scope); err != nil || len(list) != 2 {
		t.Fatalf("ListHolders = %v, %v", list, err)
	}
	if init, err := b.InitializeDatabaseShamir("vault", ""); err != nil || init.ShareCount != 2 {
		t.Fatalf("InitializeDatabaseShamir = %+v, %v", init, err)
	}
	envelopes := map[*cliHolder]string{}
	for _, h := range holders {
		claimed, err := b.ClaimEnvelope(&HolderClaimParams{HolderScope: scope, Username: h.name, KeyPEM: keys[h]})
		if err != nil {
			t.Fatalf("ClaimEnvelope(%s): %v", h.name, err)
		}
		envelopes[h] = string(claimed.Envelope)
	}
	for _, op := range []string{transport.OpSeal, transport.OpUnseal} {
		var out *transport.QuorumOutcome
		for _, h := range holders {
			var err error
			out, err = b.SubmitShare(&HolderSubmitParams{HolderScope: scope, Op: op, Username: h.name, Envelope: envelopes[h], KeyPEM: keys[h]})
			if err != nil {
				t.Fatalf("SubmitShare(%s, %s): %v", op, h.name, err)
			}
		}
		if !out.Complete {
			t.Fatalf("%s quorum = %+v", op, out)
		}
	}
	if round, err := b.StartRekey(&HolderRekeyParams{HolderScope: scope, Threshold: 2}); err != nil || round.Op != transport.OpRekey {
		t.Fatalf("StartRekey = %+v, %v", round, err)
	}
	if _, err := b.RevokeHolder(&HolderRekeyParams{HolderScope: scope, Username: "bob", Threshold: 2}); err == nil {
		t.Fatal("revoking a holder below the threshold succeeded")
	}
}

// TestOfficerBinding_DatabaseScopeRefusals covers a scope naming both a
// tenant and a database, and an initialization without a connection.
func TestOfficerBinding_DatabaseScopeRefusals(t *testing.T) {
	fx := servicetest.NewShamirDatabase(t, "vault", 2, 2)
	b := NewOfficerBinding(testLogger(t), newTestConnectionBinding(t, newMockKVService(), newMockAdminService(),
		embedded.WithDatabaseService(fx.Service)))
	var invalid *InvalidArgError
	if _, err := b.ListHolders(HolderScope{TenantID: "acme", Database: "vault"}); !errors.As(err, &invalid) {
		t.Fatalf("a scope naming a tenant and a database: %v", err)
	}
	unconnected := NewOfficerBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	if _, err := unconnected.InitializeDatabaseShamir("vault", ""); err == nil {
		t.Fatal("InitializeDatabaseShamir answered without a connection")
	}
	if _, err := b.InitializeDatabaseShamir("absent", ""); err == nil {
		t.Fatal("initializing a database without a Shamir barrier succeeded")
	}
}
