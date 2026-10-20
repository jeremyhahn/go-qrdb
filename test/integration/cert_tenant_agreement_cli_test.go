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
	"encoding/json"
	"fmt"
	"net/http"
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

	if out, err := runCLIProto(t, node, proto, "users", "delete", userID); err != nil {
		t.Fatalf("[%s] users delete (fixture cleanup): %v\noutput: %s", proto.name, err, out)
	}
	if out, err := runCLIProto(t, node, proto, "roles", "delete", role.ID); err != nil {
		t.Fatalf("[%s] roles delete (fixture cleanup): %v\noutput: %s", proto.name, err, out)
	}
}
