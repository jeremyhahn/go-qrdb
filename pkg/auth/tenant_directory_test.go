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

package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"testing"
)

// fakeTenantDirectory is a TenantDirectory over a fixed set of tenants. It
// records every tenant it is asked about, so a test can tell a lookup that
// was skipped from one that answered yes.
type fakeTenantDirectory struct {
	tenants map[string]bool
	err     error
	asked   []string
}

var _ TenantDirectory = (*fakeTenantDirectory)(nil)

func (d *fakeTenantDirectory) TenantExists(_ context.Context, tenantID string) (bool, error) {
	d.asked = append(d.asked, tenantID)
	if d.err != nil {
		return false, d.err
	}
	return d.tenants[tenantID], nil
}

// authenticateSPIFFE runs Authenticate over a verified chain holding one leaf
// naming tenant in both its OU and its SPIFFE trust domain.
func authenticateSPIFFE(t *testing.T, a *MTLSAuthenticator, tenant string) (*Identity, error) {
	t.Helper()
	cert := spiffeCert(t, "alice", []string{tenant}, "spiffe://"+tenant+"/user/alice")
	ctx := WithTLSState(t.Context(), &tls.ConnectionState{
		VerifiedChains: [][]*x509.Certificate{{cert}},
	})
	return a.Authenticate(ctx)
}

func TestAuthenticate_KnownTenantAdmitted(t *testing.T) {
	dir := &fakeTenantDirectory{tenants: map[string]bool{"tenant-acme": true}}
	a := NewMTLSAuthenticatorWithTrustDomain("qrdb.cluster", WithTenantDirectory(dir))

	id, err := authenticateSPIFFE(t, a, "tenant-acme")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id.TenantID != "tenant-acme" {
		t.Errorf("TenantID = %q, want tenant-acme", id.TenantID)
	}
	if len(dir.asked) != 1 || dir.asked[0] != "tenant-acme" {
		t.Errorf("directory asked %v, want [tenant-acme]", dir.asked)
	}
}

func TestAuthenticate_UnknownTenantRefused(t *testing.T) {
	dir := &fakeTenantDirectory{tenants: map[string]bool{"tenant-acme": true}}
	a := NewMTLSAuthenticatorWithTrustDomain("qrdb.cluster", WithTenantDirectory(dir))

	id, err := authenticateSPIFFE(t, a, "tenant-ghost")
	if err == nil {
		t.Fatalf("Authenticate admitted %q in a tenant that does not exist", id.TenantID)
	}
	if !errors.Is(err, ErrInvalidCertificate) {
		t.Errorf("err = %v, want ErrInvalidCertificate", err)
	}
	var cte *CertificateTenantError
	if !errors.As(err, &cte) {
		t.Errorf("err = %T, want *CertificateTenantError", err)
	}
	var ute *UnknownTenantError
	if !errors.As(err, &ute) || ute.TenantID != "tenant-ghost" {
		t.Errorf("err = %v, want *UnknownTenantError naming tenant-ghost", err)
	}
}

func TestAuthenticate_TenantLookupFailureIsTyped(t *testing.T) {
	cause := errors.New("tenant store unavailable")
	dir := &fakeTenantDirectory{err: cause}
	a := NewMTLSAuthenticatorWithTrustDomain("qrdb.cluster", WithTenantDirectory(dir))

	_, err := authenticateSPIFFE(t, a, "tenant-acme")
	var tle *TenantLookupError
	if !errors.As(err, &tle) {
		t.Fatalf("err = %v, want *TenantLookupError", err)
	}
	if tle.TenantID != "tenant-acme" || !errors.Is(err, cause) {
		t.Errorf("TenantLookupError = %+v, want tenant-acme wrapping the cause", tle)
	}
	if errors.Is(err, ErrInvalidCertificate) {
		t.Error("a lookup failure must not read as an invalid certificate")
	}
}

func TestAuthenticate_SystemTenantSkipsDirectory(t *testing.T) {
	dir := &fakeTenantDirectory{}
	a := NewMTLSAuthenticatorWithTrustDomain("qrdb.cluster", WithTenantDirectory(dir))

	id, err := authenticateSPIFFE(t, a, "system")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id.TenantID != "system" {
		t.Errorf("TenantID = %q, want system", id.TenantID)
	}
	if len(dir.asked) != 0 {
		t.Errorf("directory asked %v about the system tenant", dir.asked)
	}
}

func TestAuthenticate_NoDirectoryAdmitsWellFormedTenant(t *testing.T) {
	a := NewMTLSAuthenticatorWithTrustDomain("qrdb.cluster")

	id, err := authenticateSPIFFE(t, a, "tenant-acme")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id.TenantID != "tenant-acme" {
		t.Errorf("TenantID = %q, want tenant-acme", id.TenantID)
	}
}

func TestCheckTenantExists(t *testing.T) {
	cause := errors.New("down")
	cases := map[string]struct {
		dir     *fakeTenantDirectory
		tenant  string
		wantErr func(error) bool
	}{
		"known":        {&fakeTenantDirectory{tenants: map[string]bool{"a": true}}, "a", func(err error) bool { return err == nil }},
		"unknown":      {&fakeTenantDirectory{}, "a", func(err error) bool { var e *UnknownTenantError; return errors.As(err, &e) }},
		"lookup fails": {&fakeTenantDirectory{err: cause}, "a", func(err error) bool { return errors.Is(err, cause) }},
		"system":       {&fakeTenantDirectory{err: cause}, "system", func(err error) bool { return err == nil }},
		"empty":        {&fakeTenantDirectory{err: cause}, "", func(err error) bool { return err == nil }},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			a := NewMTLSAuthenticator(WithTenantDirectory(tc.dir))
			if err := a.checkTenantExists(t.Context(), tc.tenant); !tc.wantErr(err) {
				t.Errorf("checkTenantExists(%q) = %v", tc.tenant, err)
			}
		})
	}
}

func TestVerifyDelegation_UnknownTenantRefused(t *testing.T) {
	ca, caKey := testCA(t, "acme-login-ca")
	leaf := delegLeaf(t, ca, caKey, delegLeafSpec{})
	dir := &fakeTenantDirectory{}

	_, err := delegAuthenticator(ca, WithTenantDirectory(dir)).VerifyDelegation(context.Background(), leaf.Raw)
	var ute *UnknownTenantError
	if !errors.As(err, &ute) || ute.TenantID != delegTenant {
		t.Fatalf("err = %v, want *UnknownTenantError naming %q", err, delegTenant)
	}
}

func TestVerifyDelegation_KnownTenantAdmitted(t *testing.T) {
	ca, caKey := testCA(t, "acme-login-ca")
	leaf := delegLeaf(t, ca, caKey, delegLeafSpec{})
	dir := &fakeTenantDirectory{tenants: map[string]bool{delegTenant: true}}

	id, err := delegAuthenticator(ca, WithTenantDirectory(dir)).VerifyDelegation(context.Background(), leaf.Raw)
	if err != nil {
		t.Fatalf("VerifyDelegation: %v", err)
	}
	if id.TenantID != delegTenant {
		t.Errorf("TenantID = %q, want %q", id.TenantID, delegTenant)
	}
}

func TestUnknownTenantError_Error(t *testing.T) {
	if got, want := (&UnknownTenantError{TenantID: "t1"}).Error(), `tenant "t1" does not exist`; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if got := (&UnknownTenantError{}).Error(); got != `tenant "" does not exist` {
		t.Errorf("Error() = %q", got)
	}
}

func TestTenantLookupError_ErrorAndUnwrap(t *testing.T) {
	cause := errors.New("boom")
	e := &TenantLookupError{TenantID: "t1", Err: cause}
	if !errors.Is(e, cause) {
		t.Error("TenantLookupError does not unwrap to its cause")
	}
	if got := e.Error(); got != `qrdb/auth: tenant "t1" lookup failed: boom` {
		t.Errorf("Error() = %q", got)
	}
	if got := (&TenantLookupError{TenantID: "t1"}).Error(); got != `qrdb/auth: tenant "t1" lookup failed` {
		t.Errorf("Error() without a cause = %q", got)
	}
}
