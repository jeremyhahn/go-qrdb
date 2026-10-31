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

// cert_tenant_agreement_cli_test.go proves, through the real client CLI on
// every transport, the rule a client certificate names its tenant by
// (docs/identity-rbac/architecture.md): a tenant is a SPIFFE trust domain, so
// its Subject OU and the trust domain of its SPIFFE ID SAN,
// spiffe://<tenant>/<kind>/<name>, must both be present and name the same
// tenant. A certificate naming no tenant is under the cluster trust domain,
// and no tenant may be the cluster trust domain. Every certificate below is
// signed by the node's trusted CA and bound to the same live account, so the
// only thing that differs between the admitted one and the refused ones is
// whether the two fields agree.
package integration_test

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

func TestCertTenantAgreement_CLI_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)
	admin := mtlsHTTPClient(t, node.ca, 15*time.Second)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testCertTenantAgreementOverProtocol(t, node, admin, proto)
		})
	}
}

func testCertTenantAgreementOverProtocol(t *testing.T, node *multiProtoNode, admin *http.Client, proto multiProtocol) {
	t.Helper()

	base := fmt.Sprintf("agree-%s-%d", proto.name, time.Now().UnixNano())
	const (
		otherTenant = "acme"
		foreignTD   = "other.example.org"
	)
	td := qrdbconfig.DefaultClusterTrustDomain

	// The account holds users:read, so `users get` on itself is a request it
	// is authorised to make whenever its certificate names its tenant.
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
	out, err = runCLIProto(t, node, proto, "--output", "json", "users", "create",
		"--username", base,
		"--email", base+"@example.com",
		"--roles", role.ID,
	)
	if err != nil {
		t.Fatalf("[%s] users create: %v\noutput: %s", proto.name, err, out)
	}
	var created userCLIJSON
	if jerr := json.Unmarshal([]byte(out), &created); jerr != nil || created.ID == 0 {
		t.Fatalf("[%s] decode users create JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	userID := strconv.FormatUint(created.ID, 10)

	// bound issues a certificate for the account, enrols it on the account as
	// the default auth.mtls_binding_policy ("required") demands, and returns
	// the CLI flags that present it.
	bound := func(opts ...qrdbdharness.CertOption) []string {
		kp := node.ca.issue(t, t.TempDir(), opts...)
		bindUserCert(t, admin, node.restPort, created.ID, kp.CertPath)
		return tenantCLIArgsForProto(node, proto, kp.CertPath, kp.KeyPath)
	}
	asUser := func(conn []string) (string, error) {
		return runQRDB(t, node.cli, conn, "--output", "json", "users", "get", userID)
	}

	// The harness derives the SAN from the OU, the way every production
	// issuer does: the tenant is the trust domain, spiffe://system/user/<base>.
	agreeing := bound(qrdbdharness.WithSubject(base, tenantid.System))
	if out, err := asUser(agreeing); err != nil {
		t.Fatalf("[%s] request with an agreeing OU and SPIFFE ID SAN: %v\noutput: %s", proto.name, err, out)
	}

	// WithSPIFFEID replaces the derived SAN, so each certificate below
	// carries exactly the fields its case names.
	sanUnder := func(trustDomain string) qrdbdharness.CertOption {
		return qrdbdharness.WithSPIFFEID(trustDomain, "user/"+base)
	}
	mismatch := func(ou, spiffeTenant string) string {
		return fmt.Sprintf("subject OU %q and SPIFFE ID tenant %q disagree", ou, spiffeTenant)
	}
	for _, c := range []struct {
		name string
		opts []qrdbdharness.CertOption
		want string // the typed cause every transport carries
	}{
		{"trust domain names another tenant than the OU", []qrdbdharness.CertOption{
			qrdbdharness.WithSubject(base, tenantid.System), sanUnder(otherTenant)},
			mismatch(tenantid.System, otherTenant)},
		{"OU names another tenant than the trust domain", []qrdbdharness.CertOption{
			qrdbdharness.WithSubject(base, otherTenant), sanUnder(tenantid.System)},
			mismatch(otherTenant, tenantid.System)},
		{"foreign trust domain under a tenant OU", []qrdbdharness.CertOption{
			qrdbdharness.WithSubject(base, tenantid.System), sanUnder(foreignTD)},
			mismatch(tenantid.System, foreignTD)},
		{"foreign trust domain without an OU", []qrdbdharness.CertOption{
			qrdbdharness.WithSubject(base, ""), sanUnder(foreignTD)},
			"spiffe: trust domain does not match the expected trust domain"},
		{"cluster trust domain without an OU", []qrdbdharness.CertOption{
			qrdbdharness.WithSubject(base, ""), sanUnder(td)},
			"client certificate has no subject OU naming its tenant"},
		{"tenant equal to the cluster trust domain", []qrdbdharness.CertOption{
			qrdbdharness.WithSubject(base, td), sanUnder(td)},
			"tenant names the cluster trust domain"},
		// WithURISAN replaces the SAN with a URI that is not a SPIFFE ID: a
		// tenant OU does not stand in for the SAN that must agree with it.
		{"URI SAN of another scheme under a tenant OU", []qrdbdharness.CertOption{
			qrdbdharness.WithSubject(base, tenantid.System), qrdbdharness.WithURISAN("https://db.example.com/user/" + base)},
			"spiffe: no workload spiffe id present"},
		{"spiffe URI SAN that is not a SPIFFE ID", []qrdbdharness.CertOption{
			qrdbdharness.WithSubject(base, tenantid.System), qrdbdharness.WithURISAN("spiffe://" + tenantid.System + "/user/" + base + "?q=1")},
			"spiffe: uri is not a valid spiffe id"},
	} {
		out, err := asUser(bound(c.opts...))
		if err == nil {
			t.Fatalf("[%s] %s: request admitted, want it refused: %s", proto.name, c.name, out)
		}
		if !strings.Contains(out, "client certificate is invalid or unverified") || !strings.Contains(out, c.want) {
			t.Fatalf("[%s] %s: want the invalid-certificate refusal naming %q, got: %s", proto.name, c.name, c.want, out)
		}
	}

	// The refusals were of the certificates, not of the account.
	if out, err := asUser(agreeing); err != nil {
		t.Fatalf("[%s] request with the agreeing certificate after the refusals: %v\noutput: %s", proto.name, err, out)
	}

	run := func(args ...string) (string, error) { return runCLIProto(t, node, proto, args...) }
	if out, err := deleteFixtureCLI(run, time.Now().Add(fixtureDeleteRetryWindow), "users", "delete", userID); err != nil {
		t.Fatalf("[%s] users delete (fixture cleanup): %v\noutput: %s", proto.name, err, out)
	}
	if out, err := deleteFixtureCLI(run, time.Now().Add(fixtureDeleteRetryWindow), "roles", "delete", role.ID); err != nil {
		t.Fatalf("[%s] roles delete (fixture cleanup): %v\noutput: %s", proto.name, err, out)
	}
}

// fixtureDeleteRetryWindow bounds how long deleteFixtureCLI keeps re-issuing
// a delete whose propose timed out: volumePoolCreateRetryWindow's bound, for
// the same cause, a loaded runner's slow raft WAL fsync.
const fixtureDeleteRetryWindow = volumePoolCreateRetryWindow

// deleteFixtureCLI deletes a test fixture by running args through run, the
// CLI on one protocol. A propose that times out is answered 503 and may still
// commit (pkg/api/errclass/table.go: quicraft.ErrTimeout is Unavailable, and a
// client retries only a write it has made idempotent), so the delete is
// re-issued until deadline, and once a timeout has been met a "not found"
// answer is that earlier delete having committed (main run 73290). Any other
// failure, a "not found" no timeout preceded, and a timeout past deadline are
// returned as the failures they are.
func deleteFixtureCLI(run func(args ...string) (string, error), deadline time.Time, args ...string) (string, error) {
	timedOut := false
	for {
		out, err := run(args...)
		switch {
		case err == nil:
			return out, nil
		case timedOut && strings.Contains(strings.ToLower(out), "not found"):
			return out, nil
		case strings.Contains(out, "quicraft: timeout") && time.Now().Before(deadline):
			timedOut = true
			continue
		}
		return out, err
	}
}

// TestDeleteFixtureCLI_ResolvesAnAmbiguousTimeout pins deleteFixtureCLI's
// outcomes against a double CLI that answers a scripted sequence, the first
// answer being main run 73290's proposal timeout where the case scripts one.
func TestDeleteFixtureCLI_ResolvesAnAmbiguousTimeout(t *testing.T) {
	const timeoutOut = "transport: request DeleteUser failed (status 503): user: store delete failed: cert index: " +
		"service: propose failed: quicraft: timeout (already submitted to raft; it may still commit)"
	errExit := errors.New("exit status 1")
	type answer struct {
		out string
		err error
	}
	future := time.Now().Add(time.Hour)
	for _, tc := range []struct {
		name      string
		answers   []answer
		deadline  time.Time
		wantErr   bool
		wantCalls int
	}{
		{"a delete that succeeds at once", []answer{{"OK", nil}}, future, false, 1},
		{"a timeout, then the retry succeeds", []answer{{timeoutOut, errExit}, {"OK", nil}}, future, false, 2},
		{"a timeout that committed: the retry finds it gone", []answer{{timeoutOut, errExit}, {"Error: user Not Found", errExit}}, future, false, 2},
		{"not found with no timeout before it is a failure", []answer{{"Error: user not found", errExit}}, future, true, 1},
		{"any other failure is not retried", []answer{{"permission denied", errExit}}, future, true, 1},
		{"a timeout past the deadline is a failure", []answer{{timeoutOut, errExit}}, time.Now().Add(-time.Second), true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			run := func(args ...string) (string, error) {
				if !slices.Equal(args, []string{"users", "delete", "u1"}) {
					t.Fatalf("args = %q, want the delete re-issued unchanged", args)
				}
				if calls >= len(tc.answers) {
					t.Fatalf("call %d: the double has no answer scripted", calls+1)
				}
				a := tc.answers[calls]
				calls++
				return a.out, a.err
			}
			_, err := deleteFixtureCLI(run, tc.deadline, "users", "delete", "u1")
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if calls != tc.wantCalls {
				t.Fatalf("calls = %d, want %d", calls, tc.wantCalls)
			}
		})
	}
}

// TestCertTenantAgreement_CLI_TenantCertNamesItsTenant_PerProtocol proves the
// rule for a tenant other than system: a certificate whose OU and SPIFFE ID
// SAN, spiffe://<tenant>/user/<name>, both name tenant A acts in tenant A on
// every transport and in no other tenant; and a certificate whose two fields
// agree on a tenant that was never created names no one.
func TestCertTenantAgreement_CLI_TenantCertNamesItsTenant_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testTenantCertOverProtocol(t, node, proto)
		})
	}
}

func testTenantCertOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	suffix := fmt.Sprintf("%s-%d", proto.name, time.Now().UnixNano())
	tenantA := "spa-" + suffix
	tenantB := "spb-" + suffix
	dbA := "spdb-a-" + suffix
	dbB := "spdb-b-" + suffix
	const key = "greeting"

	createTenant(t, node, proto, tenantA, "SPIFFE Tenant A", "prefix", false)
	createTenant(t, node, proto, tenantB, "SPIFFE Tenant B", "prefix", false)
	certA, keyA := provisionTenantIdentity(t, node, tenantA, "db-admin", dataPlanePermissions)
	certB, keyB := provisionTenantIdentity(t, node, tenantB, "db-admin", dataPlanePermissions)
	requireSPIFFEID(t, certA, "spiffe://"+tenantA+"/user/"+adminUser)
	connA := tenantCLIArgsForProto(node, proto, certA, keyA)
	connB := tenantCLIArgsForProto(node, proto, certB, keyB)

	valueA := "tenant-a-" + suffix
	valueB := "tenant-b-" + suffix
	for _, step := range []struct {
		conn []string
		args []string
	}{
		{connA, []string{"database", "create", dbA, "--backend", "pebble"}},
		{connB, []string{"database", "create", dbB, "--backend", "pebble"}},
		{connA, []string{"--database", dbA, "kv", "put", key, valueA}},
		{connB, []string{"--database", dbB, "kv", "put", key, valueB}},
	} {
		if out, err := runQRDB(t, node.cli, step.conn, step.args...); err != nil {
			t.Fatalf("[%s] %v: %v\noutput: %s", proto.name, step.args, err, out)
		}
	}

	// Tenant A's certificate acts in tenant A.
	if out, err := runQRDB(t, node.cli, connA, "--database", dbA, "kv", "get", key); err != nil || strings.TrimSpace(out) != valueA {
		t.Fatalf("[%s] tenant A reading its own database = %q (err=%v), want %q", proto.name, out, err, valueA)
	}

	// The same certificate is refused tenant B's data, read and write.
	if out, err := runQRDB(t, node.cli, connA, "--database", dbB, "kv", "get", key); err == nil || strings.Contains(out, valueB) {
		t.Fatalf("[%s] tenant A's certificate read tenant B's database: %s (err=%v)", proto.name, out, err)
	}
	if out, err := runQRDB(t, node.cli, connA, "--database", dbB, "kv", "put", key, "hostile"); err == nil {
		t.Fatalf("[%s] tenant A's certificate wrote tenant B's database: %s", proto.name, out)
	}
	if out, err := runQRDB(t, node.cli, connB, "--database", dbB, "kv", "get", key); err != nil || strings.TrimSpace(out) != valueB {
		t.Fatalf("[%s] tenant B's value after the refused write = %q (err=%v), want %q", proto.name, out, err, valueB)
	}

	// A certificate from the same CA whose OU and SPIFFE ID agree on a
	// tenant that was never created is refused as a certificate, before any
	// account or permission is consulted.
	ghost := "spg-" + suffix
	kp := node.ca.issue(t, t.TempDir(), qrdbdharness.WithSubject(adminUser, ghost))
	requireSPIFFEID(t, kp.CertPath, "spiffe://"+ghost+"/user/"+adminUser)
	out, err := runQRDB(t, node.cli, tenantCLIArgsForProto(node, proto, kp.CertPath, kp.KeyPath), "--database", dbA, "kv", "get", key)
	if err == nil {
		t.Fatalf("[%s] a certificate for a tenant that does not exist was admitted: %s", proto.name, out)
	}
	if want := fmt.Sprintf("tenant %q does not exist", ghost); !strings.Contains(out, "client certificate is invalid or unverified") || !strings.Contains(out, want) {
		t.Fatalf("[%s] want the invalid-certificate refusal naming %q, got: %s", proto.name, want, out)
	}
}

// requireSPIFFEID fails t unless the PEM certificate at certPath carries
// exactly one URI SAN, want.
func requireSPIFFEID(t *testing.T, certPath, want string) {
	t.Helper()
	raw, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read %s: %v", certPath, err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatalf("%s holds no PEM block", certPath)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse %s: %v", certPath, err)
	}
	if len(cert.URIs) != 1 || cert.URIs[0].String() != want {
		t.Fatalf("%s URI SANs = %v, want [%s]", certPath, cert.URIs, want)
	}
}
