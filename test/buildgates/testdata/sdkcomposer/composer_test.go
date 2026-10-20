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

package composer

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net/url"
	"testing"
	"time"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

func selfSignedCA(t *testing.T) (*ecdsa.PrivateKey, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "composer CA"}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return key, cert
}

func TestNodeOptionsComposeEveryKeySeam(t *testing.T) {
	key, cert := selfSignedCA(t)
	clusterCA := ClusterCAKey(key, cert)
	if got, err := clusterCA.Certificate(context.Background()); err != nil || got != cert {
		t.Fatalf("cluster CA certificate = %v, %v", got, err)
	}
	keyB := TenantKeys{"acme": clusterCA}
	if _, err := keyB.ProviderForTenant("other"); !errors.As(err, new(*sdk.CAKeyNotInitializedError)) {
		t.Fatalf("an unbacked tenant's key = %v, want CAKeyNotInitializedError", err)
	}
	volumes := VolumeKeys{Key: sdk.VolumeKey{1}}

	if n := len(TenantKeyOptions(clusterCA, keyB, volumes)); n != 3 {
		t.Fatalf("TenantKeyOptions built %d options, want 3", n)
	}
	if n := len(OwnCAOptions(clusterCA, OwnCA{Subject: "CN=composer"}, volumes)); n != 3 {
		t.Fatalf("OwnCAOptions built %d options, want 3", n)
	}
}

func TestLedgerDAOsLandInTheNamedDatabase(t *testing.T) {
	ctx := context.Background()
	orders := sdk.NewMemoryKVStore()
	f := sdk.NewDAOFactoryWithConfig(&sdk.DAOFactoryConfig{
		PartitionedProvider: func(name string, _ uint64) sdk.KVStore {
			if name == "orders" {
				return orders
			}
			return nil
		},
	})
	plain, tx, err := LedgerDAOs(f, "orders")
	if err != nil {
		t.Fatalf("LedgerDAOs: %v", err)
	}
	if err := tx.Save(ctx, &Ledger{ID: 1, Memo: "opening"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got, err := plain.Get(ctx, 1); err != nil || got.Memo != "opening" {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if _, _, err := LedgerDAOs(f, "billing"); !errors.As(err, new(sdk.DAODatabaseStoreUnresolvedError)) {
		t.Fatalf("an unknown database = %v, want DAODatabaseStoreUnresolvedError", err)
	}
}

func TestTenantUserSANRoundTrips(t *testing.T) {
	san, err := TenantUserSAN("acme", "alice")
	if err != nil {
		t.Fatalf("TenantUserSAN: %v", err)
	}
	id, err := sdk.WorkloadFromCert(&x509.Certificate{URIs: []*url.URL{san}})
	if err != nil || id.TrustDomain != "acme" || id.Kind != sdk.SpiffeKindUser || id.Name != "alice" {
		t.Fatalf("WorkloadFromCert = %+v, %v", id, err)
	}
}

func TestDelegatedScopeReadsOnlyItsOwnPrincipal(t *testing.T) {
	ext, err := sdk.DelegationGrant{Tenant: "acme", Principal: "xkms", Scope: []string{"kv:get"}}.Extension()
	if err != nil {
		t.Fatalf("Extension: %v", err)
	}
	now := time.Now()
	c := &x509.Certificate{Extensions: []pkix.Extension{ext}, NotBefore: now, NotAfter: now.Add(sdk.DelegationDefaultLifetime)}
	if scope, err := DelegatedScope(c, "xkms"); err != nil || len(scope) != 1 || scope[0] != "kv:get" {
		t.Fatalf("DelegatedScope = %q, %v; want [kv:get]", scope, err)
	}
	if _, err := DelegatedScope(c, "billing"); !errors.As(err, new(*sdk.DelegationGrantError)) {
		t.Fatalf("another principal = %v, want *DelegationGrantError", err)
	}
	c.NotAfter = now.Add(sdk.DelegationMaxLifetime + time.Minute)
	if _, err := DelegatedScope(c, "xkms"); !errors.As(err, new(*sdk.DelegationLifetimeError)) {
		t.Fatalf("an over-long delegation = %v, want *DelegationLifetimeError", err)
	}
	if n := len(DelegationOptions("xkms")); n != 1 {
		t.Fatalf("DelegationOptions built %d options, want 1", n)
	}
}

func TestInvoicePermissionIsReadBack(t *testing.T) {
	got, err := InvoicePermission("billing:issue")
	if err != nil || got != "billing:issue" {
		t.Fatalf("InvoicePermission = %q, %v; want billing:issue", got, err)
	}
}
