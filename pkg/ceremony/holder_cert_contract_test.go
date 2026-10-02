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

package ceremony

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/holderprofile"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestHolderCert_TenantOUStaysFirst: the tenant is the FIRST OU whatever the
// relative lengths of tenant and profile. Encoded as one multi-valued RDN, a
// DER SET sorts the shorter "custodian" ahead of a longer tenant id, and the
// certificate would authenticate in a tenant named "custodian".
func TestHolderCert_TenantOUStaysFirst(t *testing.T) {
	for _, tenantID := range []string{"a", "acme", "a-tenant-id-longer-than-the-profile"} {
		t.Run(tenantID, func(t *testing.T) {
			b := newHolderCATestbed(t)
			res, err := newTenantHolders(t, tenantID, b.ca).RegisterOfficer(context.Background(), hostileRequest(t, hostileCSRs[0], "officer", seal.RoleCustodian))
			require.NoError(t, err)
			b.assertAuthenticatesAs(t, res.CertPEM, "officer", tenantID)
		})
	}
}

// copyingCA is a CeremonyCA that ignores SignCSROptions' subject and signs
// what the CSR asks for -- the behaviour this package used to have. The
// ceremony must refuse what it returns rather than trust every CA to obey the
// contract.
type copyingCA struct{ *LocalCA }

func (c copyingCA) SignCSR(_ context.Context, csrPEM []byte, opts *SignCSROptions) (*x509.Certificate, error) {
	blk, _ := pem.Decode(csrPEM)
	csr, err := x509.ParseCertificateRequest(blk.Bytes)
	if err != nil {
		return nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial, Subject: csr.Subject, URIs: csr.URIs, DNSNames: csr.DNSNames,
		NotBefore: c.caCert.NotBefore, NotAfter: c.caCert.NotAfter,
		KeyUsage: opts.KeyUsage, ExtKeyUsage: opts.ExtKeyUsage,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.caCert, csr.PublicKey, c.caKey)
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(der)
}

func TestHolderCert_NonConformingCAIsRefused(t *testing.T) {
	for _, c := range hostileCSRs {
		t.Run(c.name, func(t *testing.T) {
			svc := newTenantHolders(t, "acme", copyingCA{newTestLocalCA(t)})
			res, err := svc.RegisterOfficer(context.Background(), hostileRequest(t, c, "acme-officer", seal.RoleSecurityOfficer))
			var subjErr *CertSubjectError
			require.True(t, errors.As(err, &subjErr), "RegisterOfficer = (%v, %v); want *CertSubjectError", res, err)
			officers, err := svc.ListOfficers(context.Background())
			require.NoError(t, err)
			require.Empty(t, officers, "a refused registration was recorded")
		})
	}
}

// keySwappingCA signs the requested subject, but over a key of its own, so
// the certificate is not the CSR holder's.
type keySwappingCA struct{ *LocalCA }

func (c keySwappingCA) SignCSR(ctx context.Context, _ []byte, opts *SignCSROptions) (*x509.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		return nil, err
	}
	return c.LocalCA.SignCSR(ctx, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), opts)
}

func TestHolderCert_CertificateForAnotherKeyIsRefused(t *testing.T) {
	svc := newTenantHolders(t, "acme", keySwappingCA{newTestLocalCA(t)})
	_, err := svc.RegisterOfficer(context.Background(), hostileRequest(t, hostileCSRs[3], "acme-officer", seal.RoleSecurityOfficer))
	var subjErr *CertSubjectError
	require.True(t, errors.As(err, &subjErr), "err = %v; want *CertSubjectError", err)
	require.Equal(t, "PublicKey", subjErr.Field)
}

// TestHolderCert_IsMarkedAsHolder: an officer certificate is recognised by
// the authenticator as a holder certificate, which the authentication
// middleware never resolves to a user account -- even one registered under a
// user's name.
func TestHolderCert_IsMarkedAsHolder(t *testing.T) {
	b := newHolderCATestbed(t)
	res, err := newTenantHolders(t, "acme", b.ca).RegisterOfficer(context.Background(), hostileRequest(t, hostileCSRs[0], "admin", seal.RoleCustodian))
	require.NoError(t, err)
	blk, _ := pem.Decode(res.CertPEM)
	leaf, err := x509.ParseCertificate(blk.Bytes)
	require.NoError(t, err)
	profile, ok := auth.HolderProfile(leaf)
	require.True(t, ok)
	require.Equal(t, holderprofile.Custodian, profile)

	chains, err := leaf.Verify(x509.VerifyOptions{Roots: b.roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	require.NoError(t, err)
	id, err := auth.NewMTLSAuthenticator().Authenticate(auth.WithTLSState(context.Background(),
		&tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: chains}))
	require.NoError(t, err)
	require.True(t, auth.IsHolderIdentity(id), "an officer certificate authenticated as an ordinary user identity")
}
