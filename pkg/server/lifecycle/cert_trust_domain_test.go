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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// issuedCertURIs builds the service graph with the given trust domain, mints a
// client certificate through the provider BuildServices wired, and returns the
// certificate's URI SANs. It proves the infra field reaches the provider the
// node actually serves certificates from, not a provider built beside it.
func issuedCertURIs(t *testing.T, trustDomain string) []string {
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

	bundle, err := node.CertProv.GenerateClientCert(context.Background(), &cert.GenerateRequest{Username: "alice", TenantID: "acme"})
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
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
	want := "spiffe://db.example.com/tenant/acme/user/alice"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("issued certificate URIs = %v, want [%s]", got, want)
	}
}

func TestBuildServices_NoSPIFFETrustDomain_CertProviderEmitsNoSAN(t *testing.T) {
	if got := issuedCertURIs(t, ""); len(got) != 0 {
		t.Fatalf("issued certificate URIs = %v, want none", got)
	}
}
