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

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// usageCA signs exactly the subject SignCSROptions asks for, over the CSR's
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
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial, RawSubject: raw,
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
			svc, err := NewTenantCeremonyService("acme", seal.NewMemoryBackend(), ca, testLogger())
			require.NoError(t, err)
			res, err := svc.RegisterOfficer(context.Background(), RegisterOfficerRequest{
				Username: "acme-officer", CSRPEM: newHostileCSRPEM(t, hostileCSRs[3]), AuthenticatorType: AuthenticatorSoftware,
			})
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
	svc, err := NewTenantCeremonyService("acme", seal.NewMemoryBackend(), usageCA{LocalCA: newTestLocalCA(t)}, testLogger())
	require.NoError(t, err)
	_, err = svc.RegisterOfficer(context.Background(), RegisterOfficerRequest{
		Username: "acme-officer", CSRPEM: newHostileCSRPEM(t, hostileCSRs[3]), AuthenticatorType: AuthenticatorSoftware,
	})
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
		TenantID: "acme", Username: "officer", ValidityDays: 1,
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}
	cert, _, err := signHolderCSR(context.Background(), reorderingCA{newTestLocalCA(t)}, csrPEM, opts)
	require.NoError(t, err)
	require.Equal(t, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, cert.ExtKeyUsage,
		"fixture: the certificate must carry the usages in the other order")
}

// TestHolderCert_EveryEntryPointRefusesASubCA runs the CA-certificate double
// through every path that signs a holder certificate. Each must refuse it and
// keep nothing. SignCSRInit reports every signing failure as the
// ErrCSRSigningFailed sentinel; the others keep the typed cause.
func TestHolderCert_EveryEntryPointRefusesASubCA(t *testing.T) {
	ctx := context.Background()
	register := RegisterOfficerRequest{Username: "officer", AuthenticatorType: AuthenticatorSoftware}
	entryPoints := map[string]func(t *testing.T, ca CeremonyCA) error{
		"system officer registration": func(t *testing.T, ca CeremonyCA) error {
			svc := newSystemCeremonyWithCA(t, ca)
			req := register
			req.CSRPEM = newHostileCSRPEM(t, hostileCSRs[3])
			_, err := svc.RegisterOfficer(ctx, req)
			require.Empty(t, svc.pendingCerts, "a refused registration left a pending certificate")
			return err
		},
		"tenant officer registration": func(t *testing.T, ca CeremonyCA) error {
			svc, err := NewTenantCeremonyService("acme", seal.NewMemoryBackend(), ca, testLogger())
			require.NoError(t, err)
			req := register
			req.CSRPEM = newHostileCSRPEM(t, hostileCSRs[3])
			_, err = svc.RegisterOfficer(ctx, req)
			officers, lerr := svc.ListOfficers(ctx)
			require.NoError(t, lerr)
			require.Empty(t, officers, "a refused registration was recorded")
			return err
		},
		"recovery officer registration": func(t *testing.T, ca CeremonyCA) error {
			svc := newSystemCeremonyWithCA(t, ca)
			req := register
			req.CSRPEM = newHostileCSRPEM(t, hostileCSRs[3])
			_, err := svc.RegisterRecoveryOfficer(ctx, req)
			return err
		},
		"sign-csr (SignCSRInit)": func(t *testing.T, ca CeremonyCA) error {
			cfg := defaultConfig()
			svc, err := NewCeremonyService(cfg, unsealedTestBarrier(t), newTestCredentialService(), testLogger())
			require.NoError(t, err)
			_, err = svc.Initialize(ctx, ca, nil)
			require.NoError(t, err)
			res, err := svc.SignCSRInit(ctx, "operator-1", cfg.SOPin, newHostileCSRPEM(t, hostileCSRs[3]), "operator")
			require.Nil(t, res)
			require.ErrorIs(t, err, ErrCSRSigningFailed)
			return nil
		},
		"initial M-of-N ceremony": func(t *testing.T, ca CeremonyCA) error {
			cfg, _ := mofnConfig(t, 2, 2)
			svc, err := NewCeremonyService(cfg, unsealedTestBarrier(t), newTestCredentialService(), testLogger())
			require.NoError(t, err)
			_, err = svc.Initialize(ctx, ca, nil)
			require.Empty(t, svc.pendingCerts, "a refused ceremony left a pending certificate")
			return err
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
