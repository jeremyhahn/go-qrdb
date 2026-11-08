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

package ceremony

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// IssuedSystemSO is the system tenant's Security Officer as the ceremony
// issued it: the username, the login certificate (signed from SOCSRPEM or
// adopted from SOCertPEM) and the SO PIN as the auth.HashSOPIN record.
// The PIN itself never leaves Initialize; first boot, which may run on
// another node, commits the record through
// auth.SOAuthenticator.CommitSOPINRecord.
type IssuedSystemSO struct {
	Username string
	CertPEM  []byte
	// ChainPEM is SOChainPEM for an adopted certificate, which first boot
	// verifies again; empty for a signed one.
	ChainPEM  []byte
	PINRecord string
}

// clone returns a deep copy so callers never share the ceremony's buffers.
func (s *IssuedSystemSO) clone() *IssuedSystemSO {
	if s == nil {
		return nil
	}
	return &IssuedSystemSO{
		Username:  s.Username,
		CertPEM:   bytes.Clone(s.CertPEM),
		ChainPEM:  bytes.Clone(s.ChainPEM),
		PINRecord: s.PINRecord,
	}
}

// systemSORequested reports whether opts name a system SO at all.
func systemSORequested(opts *InitializeOptions) bool {
	return opts.SOUsername != "" || len(opts.SOCSRPEM) > 0 || len(opts.SOCertPEM) > 0 || len(opts.SOChainPEM) > 0
}

// validateSystemSOInputs checks the system SO inputs before the ceremony
// writes anything: a username with exactly one of SOCSRPEM and SOCertPEM,
// a username that is not the admin's, a CA for the CSR path, a parseable
// CSR or a presented certificate that verifies, and a PIN to hash.
func validateSystemSOInputs(ca CeremonyCA, trustDomain string, opts *InitializeOptions, caBundlePEM []byte) error {
	if !systemSORequested(opts) {
		return nil
	}
	switch {
	case strings.TrimSpace(opts.SOUsername) == "":
		return &SystemSORejectedError{Cause: errors.New("a system SO certificate input needs SOUsername")}
	case strings.EqualFold(opts.SOUsername, adminUsername):
		return &SystemSORejectedError{Cause: errors.New("the system SO cannot be the bootstrap admin")}
	case len(opts.SOCSRPEM) > 0 && len(opts.SOCertPEM) > 0:
		return &SystemSORejectedError{Cause: errors.New("SOCSRPEM and SOCertPEM are mutually exclusive")}
	case len(opts.SOCSRPEM) == 0 && len(opts.SOCertPEM) == 0:
		return &SystemSORejectedError{Cause: errors.New("SOUsername needs SOCSRPEM or SOCertPEM")}
	case len(opts.SOChainPEM) > 0 && len(opts.SOCertPEM) == 0:
		return &SystemSORejectedError{Cause: errors.New("SOChainPEM applies only to SOCertPEM")}
	case opts.SOPin == "":
		return &SystemSORejectedError{Cause: errors.New("the system SO needs SOPin")}
	}
	if len(opts.SOCertPEM) > 0 {
		_, err := adoptSystemSOCert(opts, caBundlePEM, trustDomain)
		return err
	}
	if ca == nil {
		return &AdminCredentialRequiresCAError{Path: "so-csr-sign"}
	}
	if _, err := parseAndVerifyCSR(opts.SOCSRPEM); err != nil {
		return &SystemSORejectedError{Cause: err}
	}
	return requireAdminSPIFFEIdentity(trustDomain)
}

// adoptSystemSOCert verifies a presented system SO certificate against
// AdminCertPoolPEM (or the ceremony CA bundle) through SOChainPEM, under
// the system tenant.
func adoptSystemSOCert(opts *InitializeOptions, caBundlePEM []byte, trustDomain string) (*x509.Certificate, error) {
	poolPEM := opts.AdminCertPoolPEM
	if len(poolPEM) == 0 {
		poolPEM = caBundlePEM
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(poolPEM) {
		return nil, &SystemSORejectedError{Cause: errors.New("trust pool contains no usable certificates")}
	}
	cert, err := verifyPresentedLoginCert(opts.SOCertPEM, opts.SOChainPEM, pool, trustDomain)
	if err != nil {
		// verifyPresentedLoginCert speaks of the admin; report its cause
		// as the SO's.
		var rejected *AdminCredentialRejectedError
		if errors.As(err, &rejected) {
			err = rejected.Cause
		}
		return nil, &SystemSORejectedError{Cause: err}
	}
	tenant, err := auth.TenantFromClientCert(cert, trustDomain)
	if err != nil {
		return nil, &SystemSORejectedError{Cause: err}
	}
	if tenant != tenantid.System {
		return nil, &SystemSORejectedError{Cause: errors.New("the certificate names tenant " + tenant + ", not the system tenant")}
	}
	return cert, nil
}

// issueSystemSO signs or adopts the system SO's certificate and hashes the
// SO PIN. It returns nil, nil when opts name no system SO. The SO's key
// must differ from the admin's: a shared key would let the admin's
// certificate stand in for the SO's.
func issueSystemSO(ctx context.Context, ca CeremonyCA, trustDomain string, opts *InitializeOptions, caBundlePEM, adminCertPEM []byte) (*IssuedSystemSO, error) {
	if !systemSORequested(opts) {
		return nil, nil
	}
	var certPEM, chainPEM []byte
	if len(opts.SOCertPEM) > 0 {
		if _, err := adoptSystemSOCert(opts, caBundlePEM, trustDomain); err != nil {
			return nil, err
		}
		certPEM = bytes.Clone(opts.SOCertPEM)
		chainPEM = bytes.Clone(opts.SOChainPEM)
	} else {
		signed, err := signAdminLoginCSR(ctx, ca, adminLoginSignCSROptions(tenantid.System, opts.SOUsername, trustDomain), opts.SOCSRPEM)
		if err != nil {
			return nil, err
		}
		certPEM = signed
	}
	if sameCertKey(certPEM, adminCertPEM) {
		return nil, &SystemSORejectedError{Cause: errors.New("the system SO's key is the bootstrap admin's")}
	}

	pin := []byte(opts.SOPin)
	defer secure.Wipe(pin)
	record, err := auth.HashSOPIN(pin)
	if err != nil {
		return nil, &SystemSORejectedError{Cause: err}
	}
	return &IssuedSystemSO{Username: opts.SOUsername, CertPEM: certPEM, ChainPEM: chainPEM, PINRecord: record}, nil
}

// sameCertKey reports whether two PEM certificates carry the same public
// key. An unparseable side never matches.
func sameCertKey(aPEM, bPEM []byte) bool {
	a, b := parsePEMCert(aPEM), parsePEMCert(bPEM)
	return a != nil && b != nil && bytes.Equal(a.RawSubjectPublicKeyInfo, b.RawSubjectPublicKeyInfo)
}

func parsePEMCert(certPEM []byte) *x509.Certificate {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil
	}
	return cert
}

// LastIssuedSystemSO returns a copy of the system SO the last successful
// Initialize issued, or nil when it named none. The composer writes it to
// the replicated pending store so first boot on any node can enrol the SO.
func (s *CeremonyService) LastIssuedSystemSO() *IssuedSystemSO {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSystemSO.clone()
}
