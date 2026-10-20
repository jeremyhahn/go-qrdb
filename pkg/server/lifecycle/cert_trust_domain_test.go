// Copyright (c) 2026 Jeremy Hahn
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

package lifecycle_test

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// issueThroughBuiltProvider builds the service graph with the given trust
// domain and mints a client certificate through the provider BuildServices
// wired. It proves the infra field reaches the provider the node actually
// serves certificates from, not a provider built beside it.
func issueThroughBuiltProvider(t *testing.T, trustDomain string) (*certtest.Bundle, error) {
	t.Helper()
	host := newLocalHost()
	t.Cleanup(func() { _ = host.Close() })
	hub := watch.NewHub()
	t.Cleanup(hub.Close)

	barrier, registry, credSvc := newTestBarrier(t)
	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:              host,
		WatchHub:          hub,
		DataDir:           t.TempDir(),
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
		SPIFFETrustDomain: trustDomain,
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	t.Cleanup(func() { _ = node.Stop(context.Background()) })

	return certtest.Generate(context.Background(), node.CertProv, &certtest.Request{Username: "alice", TenantID: "acme"})
}

// issuedCertURIs mints a certificate through issueThroughBuiltProvider and
// returns its URI SANs.
func issuedCertURIs(t *testing.T, trustDomain string) []string {
	t.Helper()
	bundle, err := issueThroughBuiltProvider(t, trustDomain)
	if err != nil {
		t.Fatalf("certtest.Generate: %v", err)
	}
	block, _ := pem.Decode(bundle.CertPEM)
	if block == nil {
		t.Fatal("certificate PEM did not decode")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	out := make([]string, 0, len(c.URIs))
	for _, u := range c.URIs {
		out = append(out, u.String())
	}
	return out
}

func TestBuildServices_SPIFFETrustDomain_ReachesCertProvider(t *testing.T) {
	got := issuedCertURIs(t, "db.example.com")
	want := "spiffe://acme/user/alice"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("issued certificate URIs = %v, want [%s]", got, want)
	}
}

func TestBuildServices_SPIFFETrustDomain_IsNoTenant(t *testing.T) {
	// The tenant's SAN is under the tenant's own trust domain, so what shows
	// the configured one reached the provider is that it names no tenant.
	bundle, err := issueThroughBuiltProvider(t, "acme")
	if !errors.Is(err, auth.ErrTenantIsClusterTrustDomain) {
		t.Fatalf("certtest.Generate = %v, %v; want it to wrap auth.ErrTenantIsClusterTrustDomain", bundle, err)
	}
}

func TestBuildServices_NoSPIFFETrustDomain_CertProviderRefusesToIssue(t *testing.T) {
	bundle, err := issueThroughBuiltProvider(t, "")
	var se *cert.SPIFFEIdentityError
	if !errors.As(err, &se) {
		t.Fatalf("certtest.Generate = %v, %v; want a *cert.SPIFFEIdentityError", bundle, err)
	}
	if !errors.Is(err, spiffe.ErrTrustDomainEmpty) {
		t.Fatalf("certtest.Generate error = %v, want it to wrap spiffe.ErrTrustDomainEmpty", err)
	}
}
