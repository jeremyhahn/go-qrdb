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

package sdk

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"net/url"
	"testing"
)

func TestTenantFromCert_MatchesTenantFromClientCert(t *testing.T) {
	const trustDomain = "cluster.qrdb.test"
	san, err := url.Parse("spiffe://acme/user/alice")
	if err != nil {
		t.Fatal(err)
	}

	valid := &x509.Certificate{
		Subject: pkix.Name{OrganizationalUnit: []string{"acme"}},
		URIs:    []*url.URL{san},
	}
	tenant, err := TenantFromCert(valid, trustDomain)
	if err != nil || tenant != "acme" {
		t.Fatalf("TenantFromCert(valid) = %q, %v; want \"acme\", nil", tenant, err)
	}
	wantTenant, wantErr := TenantFromClientCert(valid, trustDomain)
	if tenant != wantTenant || wantErr != nil {
		t.Fatalf("TenantFromClientCert(valid) = %q, %v; TenantFromCert = %q", wantTenant, wantErr, tenant)
	}

	mismatched := &x509.Certificate{
		Subject: pkix.Name{OrganizationalUnit: []string{"globex"}},
		URIs:    []*url.URL{san},
	}
	tenant, err = TenantFromCert(mismatched, trustDomain)
	wantTenant, wantErr = TenantFromClientCert(mismatched, trustDomain)
	if tenant != "" || tenant != wantTenant {
		t.Fatalf("TenantFromCert(mismatched) tenant = %q, TenantFromClientCert = %q; want both empty", tenant, wantTenant)
	}
	var mm *ClientCertificateTenantNameMismatchError
	if !errors.As(err, &mm) || mm.OU != "globex" || mm.SPIFFETenant != "acme" {
		t.Fatalf("TenantFromCert(mismatched) err = %v, want a name-mismatch refusal", err)
	}
	if !errors.Is(err, ErrInvalidClientCertificate) {
		t.Fatalf("TenantFromCert(mismatched) err = %v, want ErrInvalidClientCertificate", err)
	}
	if wantErr == nil || err.Error() != wantErr.Error() {
		t.Fatalf("TenantFromCert err %v differs from TenantFromClientCert err %v", err, wantErr)
	}
}
