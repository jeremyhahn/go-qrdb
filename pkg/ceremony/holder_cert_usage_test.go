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
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// usageCA signs exactly the subject and SAN SignCSROptions asks for, over the CSR's
// own key, but lets a test change what the certificate may DO: be a CA, sign
// certificates, or carry different extended key usages. A holder certificate
// that can sign certificates is a sub-CA under the cluster CA -- it could
// mint any login -- so the ceremony must refuse it even when the subject is
// right.
type usageCA struct {
	*LocalCA
	isCA        bool
	keyUsage    x509.KeyUsage // added to the requested key usage
	setKeyUsage x509.KeyUsage // replaces it when non-zero
	extUsage    []x509.ExtKeyUsage
}

func (c usageCA) SignCSR(_ context.Context, csrPEM []byte, opts *SignCSROptions) (*x509.Certificate, error) {
	blk, _ := pem.Decode(csrPEM)
	csr, err := x509.ParseCertificateRequest(blk.Bytes)
	if err != nil {
		return nil, err
	}
	raw, err := opts.RawSubject()
	if err != nil {
		return nil, err
	}
	uris, err := opts.URIs()
	if err != nil {
		return nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial, RawSubject: raw, URIs: uris,
		NotBefore: c.caCert.NotBefore, NotAfter: c.caCert.NotAfter,
		KeyUsage: opts.KeyUsage | c.keyUsage, ExtKeyUsage: opts.ExtKeyUsage,
		BasicConstraintsValid: true, IsCA: c.isCA,
	}
	if c.setKeyUsage != 0 {
		tmpl.KeyUsage = c.setKeyUsage
	}
	if c.extUsage != nil {
		tmpl.ExtKeyUsage = c.extUsage
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.caCert, csr.PublicKey, c.caKey)
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(der)
}

func TestHolderCert_CapabilityRefusals(t *testing.T) {
	cases := map[string]struct {
		ca    usageCA
		field string
	}{
		"a CA certificate":                {usageCA{isCA: true}, "IsCA"},
		"certificate signing key usage":   {usageCA{keyUsage: x509.KeyUsageCertSign}, "KeyUsage"},
		"an extra extended key usage":     {usageCA{extUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}}, "ExtKeyUsage"},
		"a different extended key usage":  {usageCA{extUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}, "ExtKeyUsage"},
		"no extended key usage":           {usageCA{extUsage: []x509.ExtKeyUsage{}}, "ExtKeyUsage"},
		"any extended key usage":          {usageCA{extUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}, "ExtKeyUsage"},
		"a duplicated extended key usage": {usageCA{extUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageClientAuth}}, "ExtKeyUsage"},
		"CRL signing key usage":           {usageCA{keyUsage: x509.KeyUsageCRLSign}, "KeyUsage"},
		"a narrower key usage":            {usageCA{setKeyUsage: x509.KeyUsageDigitalSignature}, "KeyUsage"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ca := tc.ca
			ca.LocalCA = newTestLocalCA(t)
			svc := newTenantHolders(t, "acme", ca)
			res, err := svc.RegisterOfficer(context.Background(), hostileRequest(t, hostileCSRs[3], "acme-officer", seal.RoleSecurityOfficer))
			var capErr *IssuedCertCapabilityError
			require.True(t, errors.As(err, &capErr), "RegisterOfficer = (%v, %v); want *IssuedCertCapabilityError", res, err)
			require.Equal(t, tc.field, capErr.Field)
			officers, err := svc.ListOfficers(context.Background())
			require.NoError(t, err)
			require.Empty(t, officers, "a refused registration was recorded")
		})
	}
}

// TestHolderCert_ConformingCAIsAccepted is the control: the same double with
// nothing changed issues the certificate.
func TestHolderCert_ConformingCAIsAccepted(t *testing.T) {
	svc := newTenantHolders(t, "acme", usageCA{LocalCA: newTestLocalCA(t)})
	_, err := svc.RegisterOfficer(context.Background(), hostileRequest(t, hostileCSRs[3], "acme-officer", seal.RoleSecurityOfficer))
	require.NoError(t, err)
}

func TestSameExtKeyUsages(t *testing.T) {
	ca, sa, cs := x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageCodeSigning
	tests := []struct {
		name      string
		got, want []x509.ExtKeyUsage
		same      bool
	}{
		{"equal", []x509.ExtKeyUsage{ca}, []x509.ExtKeyUsage{ca}, true},
		{"reordered", []x509.ExtKeyUsage{sa, cs, ca}, []x509.ExtKeyUsage{ca, sa, cs}, true},
		{"both empty", nil, nil, true},
		{"extra", []x509.ExtKeyUsage{ca, sa}, []x509.ExtKeyUsage{ca}, false},
		{"missing", []x509.ExtKeyUsage{ca}, []x509.ExtKeyUsage{ca, sa}, false},
		{"duplicate in place of another", []x509.ExtKeyUsage{ca, ca}, []x509.ExtKeyUsage{ca, sa}, false},
		{"different", []x509.ExtKeyUsage{sa}, []x509.ExtKeyUsage{ca}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameExtKeyUsages(tc.got, tc.want); got != tc.same {
				t.Errorf("sameExtKeyUsages(%v, %v) = %v, want %v", tc.got, tc.want, got, tc.same)
			}
		})
	}
}

// reorderingCA issues a conforming certificate whose extended key usages are
// the requested set in reverse order. Order carries no meaning, so it is
// accepted.
type reorderingCA struct{ *LocalCA }

func (c reorderingCA) SignCSR(ctx context.Context, csrPEM []byte, opts *SignCSROptions) (*x509.Certificate, error) {
	reversed := slices.Clone(opts.ExtKeyUsage)
	slices.Reverse(reversed)
	return usageCA{LocalCA: c.LocalCA, extUsage: reversed}.SignCSR(ctx, csrPEM, opts)
}

func TestCheckIssuedSubject_ReorderedExtKeyUsagesAreAccepted(t *testing.T) {
	csrPEM := newHostileCSRPEM(t, hostileCSRs[3])
	opts := &SignCSROptions{
		TenantID: "acme", Username: "officer", ValidityDays: 1, TrustDomain: testTrustDomain,
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}
	cert, _, err := signHolderCSR(context.Background(), reorderingCA{newTestLocalCA(t)}, csrPEM, opts)
	require.NoError(t, err)
	require.Equal(t, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, cert.ExtKeyUsage,
		"fixture: the certificate must carry the usages in the other order")
}

// systemHoldersWith is the system ceremony service over a Shamir barrier,
// signing with ca, wired as a composer wires it.
func systemHoldersWith(t *testing.T, ca CeremonyCA) *CeremonyService {
	t.Helper()
	svc, err := NewCeremonyService(&CeremonyConfig{DataDir: t.TempDir()}, shamirBarrier(t, seal.NewMemoryBackend(), 2, 2), testLogger())
	require.NoError(t, err)
	svc.SetCA(ca)
	svc.SetTrustDomain(testTrustDomain)
	svc.SetFactorRegistry(testFactors(t))
	svc.SetPINBackend(pintest.NewBackend(t))
	return svc
}

// TestHolderCert_EveryEntryPointRefusesASubCA runs the CA-certificate double
// through every path that signs a holder certificate -- the unified holder
// engine in every scope, the initial ceremony and sign-csr. Each must refuse
// it and keep nothing. SignCSRInit reports every signing failure as the
// ErrCSRSigningFailed sentinel; the others keep the typed cause.
func TestHolderCert_EveryEntryPointRefusesASubCA(t *testing.T) {
	ctx := context.Background()
	req := func(t *testing.T) RegisterOfficerRequest {
		return hostileRequest(t, hostileCSRs[3], "officer", seal.RoleSecurityOfficer)
	}
	systemScope := func(domain seal.QuorumDomain) func(t *testing.T, ca CeremonyCA) error {
		return func(t *testing.T, ca CeremonyCA) error {
			svc := systemHoldersWith(t, ca)
			_, err := svc.RegisterOfficer(ctx, domain, req(t))
			officers, lerr := svc.ListOfficers(ctx, domain)
			require.NoError(t, lerr)
			require.Empty(t, officers, "a refused registration was recorded")
			return err
		}
	}
	entryPoints := map[string]func(t *testing.T, ca CeremonyCA) error{
		"system holder registration":   systemScope(seal.DomainUnseal),
		"recovery holder registration": systemScope(seal.DomainRecovery),
		"tenant holder registration": func(t *testing.T, ca CeremonyCA) error {
			svc := newTenantHolders(t, "acme", ca)
			_, err := svc.RegisterOfficer(ctx, req(t))
			officers, lerr := svc.ListOfficers(ctx)
			require.NoError(t, lerr)
			require.Empty(t, officers, "a refused registration was recorded")
			return err
		},
		"database holder registration": func(t *testing.T, ca CeremonyCA) error {
			svc, err := NewDatabaseCeremonyService("acme", "vault", seal.NewMemoryBackend(), HolderDeps{TrustDomain: testTrustDomain, CA: ca, Factors: testFactors(t)}, testLogger())
			require.NoError(t, err)
			_, err = svc.RegisterOfficer(ctx, req(t))
			officers, lerr := svc.ListOfficers(ctx)
			require.NoError(t, lerr)
			require.Empty(t, officers, "a refused registration was recorded")
			return err
		},
		"initial ceremony": func(t *testing.T, ca CeremonyCA) error {
			svc := systemHoldersWith(t, ca)
			_, err := svc.Initialize(ctx, ca, initOpts(2, newHolder(t, "alice", ""), newHolder(t, "bob", "")))
			officers, lerr := svc.ListOfficers(ctx, seal.DomainUnseal)
			require.NoError(t, lerr)
			require.Empty(t, officers, "a refused ceremony recorded a holder")
			return err
		},
		"sign-csr (SignCSRInit)": func(t *testing.T, ca CeremonyCA) error {
			// SignCSRInit is PreOperationalOnly (go-qrdb#548): it only
			// matters while StateEnrolling, so this entry point sets that
			// state directly rather than through Initialize. Initialize
			// itself, under M-of-N, would use ca (this subtest's saboteur,
			// whose SignCSR always returns a sub-CA) to sign every officer's
			// CSR synchronously, failing the ceremony before SignCSRInit's
			// own refusal is ever reached -- a problem orthogonal to what
			// this entry point tests. Role is "so": sign-csr issues HOLDER
			// certificates only (go-qrdb#548), "operator" was removed from
			// roleProfileMap.
			opts := enrollingInitOpts(t)
			svc := newTestCeremonyService(t, defaultConfig(), unsealedTestBarrier(t))
			svc.mu.Lock()
			svc.deps.CA = ca
			svc.state = StateEnrolling
			svc.mu.Unlock()
			res, err := svc.SignCSRInit(ctx, "operator-1", opts.SOPin, newHostileCSRPEM(t, hostileCSRs[3]), "so")
			require.Nil(t, res)
			require.ErrorIs(t, err, ErrCSRSigningFailed)
			return nil
		},
	}
	for name, run := range entryPoints {
		t.Run(name, func(t *testing.T) {
			err := run(t, usageCA{LocalCA: newTestLocalCA(t), isCA: true})
			if name == "sign-csr (SignCSRInit)" {
				return
			}
			var capErr *IssuedCertCapabilityError
			require.True(t, errors.As(err, &capErr), "err = %v; want *IssuedCertCapabilityError", err)
			require.Equal(t, "IsCA", capErr.Field)
		})
	}
}
