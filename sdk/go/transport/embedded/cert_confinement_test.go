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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The embedded half of the certificate confinement the network transports
// enforce through certs:* being ScopeSelfStrict: a certificate authenticates
// as the tenant its Subject names, so every caller -- the system tenant
// included -- generates, lists and revokes only its own tenant's.

// callerCtx is a context whose caller is an identity in tenantID, the way an
// embedded caller presents itself.
func callerCtx(tenantID string) context.Context {
	return auth.WithIdentity(context.Background(), &auth.Identity{Subject: "admin", TenantID: tenantID})
}

// confinementProvider is a certificate provider holding one issued record per
// entry of owners (serial -> tenant). It records which calls reached it, so a
// refusal is proven to have happened BEFORE the provider acted.
func confinementProvider(owners map[string]string, reached *[]string) *mockCertProvider {
	return &mockCertProvider{
		generateClientCertFn: func(_ context.Context, req *cert.GenerateRequest) (*cert.CertBundle, error) {
			*reached = append(*reached, "generate:"+req.TenantID)
			return &cert.CertBundle{CertPEM: []byte("c"), KeyPEM: []byte("k"), Serial: "new"}, nil
		},
		listCertsFn: func(_ context.Context, tenantID string) ([]*cert.CertRecord, error) {
			*reached = append(*reached, "list:"+tenantID)
			return []*cert.CertRecord{}, nil
		},
		revokeCertFn: func(_ context.Context, serial string) (*cert.RevokeResult, error) {
			*reached = append(*reached, "revoke:"+serial)
			outcome := cert.RevokeOutcomeDenyBySerial
			if _, ok := owners[serial]; ok {
				outcome = cert.RevokeOutcomeRecordUpdated
			}
			return &cert.RevokeResult{Serial: serial, Outcome: outcome}, nil
		},
		tenantForSerialFn: func(_ context.Context, serial string) (string, bool, error) {
			owner, ok := owners[serial]
			return owner, ok, nil
		},
	}
}

func assertCrossTenantDenied(t *testing.T, verb string, err error) {
	t.Helper()
	var denied *tenant.CrossTenantDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("%s: err = %v (%T); want *tenant.CrossTenantDeniedError", verb, err, err)
	}
}

func TestCertConfinement_SystemCallerCannotGenerateForAnotherTenant(t *testing.T) {
	var reached []string
	tr := newAuthTransport(t, WithCertProvider(confinementProvider(nil, &reached)))

	_, err := tr.GenerateCert(callerCtx(tenant.SystemTenantID), &transport.GenerateCertRequest{Username: "alice", TenantID: "acme"})
	assertCrossTenantDenied(t, "system caller generating for acme", err)
	if len(reached) != 0 {
		t.Fatalf("the provider was reached (%v); a refused mint must sign nothing", reached)
	}

	if _, err := tr.GenerateCert(callerCtx(tenant.SystemTenantID), &transport.GenerateCertRequest{Username: "op", TenantID: tenant.SystemTenantID}); err != nil {
		t.Fatalf("system caller generating for its own tenant: %v", err)
	}
}

func TestCertConfinement_TenantCallerGeneratesOnlyForItself(t *testing.T) {
	var reached []string
	tr := newAuthTransport(t, WithCertProvider(confinementProvider(nil, &reached)))

	if _, err := tr.GenerateCert(callerCtx("acme"), &transport.GenerateCertRequest{Username: "alice", TenantID: "acme"}); err != nil {
		t.Fatalf("acme generating for acme: %v", err)
	}
	_, err := tr.GenerateCert(callerCtx("acme"), &transport.GenerateCertRequest{Username: "carol", TenantID: "globex"})
	assertCrossTenantDenied(t, "acme generating for globex", err)
	if len(reached) != 1 || reached[0] != "generate:acme" {
		t.Fatalf("provider calls = %v; want only the own-tenant mint", reached)
	}
}

func TestCertConfinement_ListIsConfinedToTheCallersTenant(t *testing.T) {
	var reached []string
	tr := newAuthTransport(t, WithCertProvider(confinementProvider(nil, &reached)))

	_, err := tr.ListCerts(callerCtx(tenant.SystemTenantID), "acme")
	assertCrossTenantDenied(t, "system caller listing acme", err)
	if _, err := tr.ListCerts(callerCtx("acme"), "acme"); err != nil {
		t.Fatalf("acme listing acme: %v", err)
	}
	if len(reached) != 1 || reached[0] != "list:acme" {
		t.Fatalf("provider calls = %v; want only acme's own list", reached)
	}
}

func TestCertConfinement_RevokeIsConfinedToTheSerialsOwner(t *testing.T) {
	var reached []string
	owners := map[string]string{"acme-serial": "acme"}
	tr := newAuthTransport(t, WithCertProvider(confinementProvider(owners, &reached)))

	_, err := tr.RevokeCert(callerCtx(tenant.SystemTenantID), "acme-serial")
	assertCrossTenantDenied(t, "system caller revoking acme's serial", err)
	_, err = tr.RevokeCert(callerCtx("globex"), "acme-serial")
	assertCrossTenantDenied(t, "globex revoking acme's serial", err)

	got, err := tr.RevokeCert(callerCtx("acme"), "acme-serial")
	if err != nil {
		t.Fatalf("acme revoking its own serial: %v", err)
	}
	if !got.RecordFound {
		t.Error("RecordFound = false for a serial with an issued record")
	}
	if len(reached) != 1 || reached[0] != "revoke:acme-serial" {
		t.Fatalf("provider calls = %v; want only the owner's revocation", reached)
	}
}

func TestCertConfinement_DenyBySerialIsSystemOnly(t *testing.T) {
	var reached []string
	tr := newAuthTransport(t, WithCertProvider(confinementProvider(nil, &reached)))

	_, err := tr.RevokeCert(callerCtx("acme"), "peer-serial")
	assertCrossTenantDenied(t, "acme writing a deny-by-serial marker", err)

	got, err := tr.RevokeCert(callerCtx(tenant.SystemTenantID), "peer-serial")
	if err != nil {
		t.Fatalf("system caller writing a deny-by-serial marker: %v", err)
	}
	if got.RecordFound {
		t.Error("RecordFound = true for a serial with no issued record")
	}
	if len(reached) != 1 || reached[0] != "revoke:peer-serial" {
		t.Fatalf("provider calls = %v; want only the system caller's revocation", reached)
	}
}

func TestCertConfinement_UnresolvableSerialIsRefused(t *testing.T) {
	resolveErr := &cert.StoreError{Op: "get", Detail: "unreachable"}
	reached := false
	tr := newAuthTransport(t, WithCertProvider(&mockCertProvider{
		tenantForSerialFn: func(context.Context, string) (string, bool, error) { return "", false, resolveErr },
		revokeCertFn: func(_ context.Context, serial string) (*cert.RevokeResult, error) {
			reached = true
			return &cert.RevokeResult{Serial: serial, Outcome: cert.RevokeOutcomeDenyBySerial}, nil
		},
	}))

	_, err := tr.RevokeCert(callerCtx(tenant.SystemTenantID), "any")
	if !errors.Is(err, resolveErr) {
		t.Fatalf("err = %v; want the resolver's error, since the revocation's tenant is unknown", err)
	}
	if reached {
		t.Fatal("a revocation whose owner could not be resolved reached the provider")
	}
}
