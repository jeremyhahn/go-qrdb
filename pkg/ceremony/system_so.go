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
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
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
	Username string `json:"username"`
	CertPEM  []byte `json:"cert_pem"`
	// ChainPEM is SOChainPEM for an adopted certificate, which first boot
	// verifies again; empty for a signed one.
	ChainPEM []byte `json:"chain_pem,omitempty"`
	// PoolPEM is the trust anchors the ceremony verified an adopted
	// certificate against, which first boot verifies it against again;
	// empty for a signed one.
	PoolPEM   []byte `json:"pool_pem,omitempty"`
	PINRecord string `json:"pin_record"`
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
		PoolPEM:   bytes.Clone(s.PoolPEM),
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
// CSR or a presented certificate that verifies, and a PIN to hash. Under
// strict separation (the node's posture, not the caller's) options that name
// no system SO are refused too.
func validateSystemSOInputs(ca CeremonyCA, trustDomain string, strict bool, opts *InitializeOptions, caBundlePEM []byte) error {
	if !systemSORequested(opts) {
		// Under strict separation the ceremony refuses to run without a
		// system SO, and an initialized node cannot gain one: refuse here,
		// before anything is committed, so the operator can retry. First
		// boot does not reach this refusal when the boot carries neither a
		// system SO nor an admin credential: it defers the system SO, and
		// this ceremony is the first step that still refuses without one.
		if strict {
			return &SystemSORejectedError{Cause: errors.New("strict separation of duties requires a system SO: SOUsername with SOCSRPEM or SOCertPEM")}
		}
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
	// The SO's SPIFFE ID SAN is built from SOUsername: a name no SAN can be
	// written for must be refused here, on both the CSR and the supplied
	// certificate path, not in issueSystemSO after the PINs, the barrier
	// and the admin credential are committed.
	if _, err := adminLoginSignCSROptions(tenantid.System, opts.SOUsername, trustDomain).URIs(); err != nil {
		return &SystemSORejectedError{Cause: err}
	}
	var soKey []byte
	if len(opts.SOCertPEM) > 0 {
		cert, err := adoptSystemSOCert(opts, caBundlePEM, trustDomain)
		if err != nil {
			return err
		}
		soKey = cert.RawSubjectPublicKeyInfo
	} else {
		if ca == nil {
			return &AdminCredentialRequiresCAError{Path: "so-csr-sign"}
		}
		csr, err := parseAndVerifyCSR(opts.SOCSRPEM)
		if err != nil {
			return &SystemSORejectedError{Cause: err}
		}
		if err := requireAdminSPIFFEIdentity(trustDomain); err != nil {
			return err
		}
		soKey = csr.RawSubjectPublicKeyInfo
	}
	// A generated admin key is fresh and cannot be the SO's; only a key the
	// operator supplies for the admin can collide. Refused here, before
	// the PINs, the barrier or any certificate is committed.
	if adminKey := suppliedAdminKey(opts); adminKey != nil && bytes.Equal(adminKey, soKey) {
		return &SystemSORejectedError{Cause: errors.New("the system SO's key is the bootstrap admin's")}
	}
	return nil
}

// suppliedAdminKey returns the SubjectPublicKeyInfo of the admin's
// AdminCertPEM or AdminCSRPEM, or nil when the admin supplies neither (or
// one that does not parse, which validateAdminCredentialInputs refuses).
func suppliedAdminKey(opts *InitializeOptions) []byte {
	if len(opts.AdminCertPEM) > 0 {
		if cert := parsePEMCert(opts.AdminCertPEM); cert != nil {
			return cert.RawSubjectPublicKeyInfo
		}
		return nil
	}
	if len(opts.AdminCSRPEM) > 0 {
		if csr, err := parseAndVerifyCSR(opts.AdminCSRPEM); err == nil {
			return csr.RawSubjectPublicKeyInfo
		}
	}
	return nil
}

// adoptSystemSOCert verifies a presented system SO certificate against
// AdminCertPoolPEM (or the ceremony CA bundle) through SOChainPEM, under
// the system tenant.
func adoptSystemSOCert(opts *InitializeOptions, caBundlePEM []byte, trustDomain string) (*x509.Certificate, error) {
	poolPEM := systemSOPoolPEM(opts, caBundlePEM)
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
	// auth.HolderProfile classifies a certificate as a custody identity
	// that RBAC refuses every permission: an SO bound to one could
	// perform none of the operations reserved for it.
	if profile, ok := auth.HolderProfile(cert); ok {
		return nil, &SystemSORejectedError{Cause: &SystemSOHolderCertError{Profile: profile}}
	}
	// The SO logs in by this certificate exactly as the admin does by
	// its own, so it is held to the admin's client-auth profile.
	if err := CheckClientAuthProfile(cert); err != nil {
		return nil, &SystemSORejectedError{Cause: err}
	}
	tenant, err := auth.TenantFromClientCert(cert, trustDomain)
	if err != nil {
		return nil, &SystemSORejectedError{Cause: err}
	}
	if tenant != tenantid.System {
		return nil, &SystemSORejectedError{Cause: errors.New("the certificate names tenant " + tenant + ", not the system tenant")}
	}
	// MTLSAuthenticator logs a client in by its subject CN: an SO whose
	// certificate names anyone else could never authenticate as the SO.
	if cert.Subject.CommonName != opts.SOUsername {
		return nil, &SystemSORejectedError{Cause: fmt.Errorf("the certificate's subject CN %q is not SOUsername %q", cert.Subject.CommonName, opts.SOUsername)}
	}
	return cert, nil
}

// issueSystemSO signs or adopts the system SO's certificate and hashes the
// SO PIN. It returns nil, nil when opts name no system SO.
// validateSystemSOInputs has already refused an SO key that is the admin's.
func issueSystemSO(ctx context.Context, ca CeremonyCA, trustDomain string, opts *InitializeOptions, caBundlePEM []byte) (*IssuedSystemSO, error) {
	if !systemSORequested(opts) {
		return nil, nil
	}
	var certPEM, chainPEM, poolPEM []byte
	if len(opts.SOCertPEM) > 0 {
		if _, err := adoptSystemSOCert(opts, caBundlePEM, trustDomain); err != nil {
			return nil, err
		}
		certPEM = bytes.Clone(opts.SOCertPEM)
		chainPEM = bytes.Clone(opts.SOChainPEM)
		poolPEM = bytes.Clone(systemSOPoolPEM(opts, caBundlePEM))
	} else {
		signed, err := signAdminLoginCSR(ctx, ca, adminLoginSignCSROptions(tenantid.System, opts.SOUsername, trustDomain), opts.SOCSRPEM)
		if err != nil {
			return nil, err
		}
		certPEM = signed
	}
	pin := []byte(opts.SOPin)
	defer secure.Wipe(pin)
	record, err := auth.HashSOPIN(pin)
	if err != nil {
		return nil, &SystemSORejectedError{Cause: err}
	}
	return &IssuedSystemSO{Username: opts.SOUsername, CertPEM: certPEM, ChainPEM: chainPEM, PoolPEM: poolPEM, PINRecord: record}, nil
}

// systemSOPoolPEM returns the trust anchors an adopted SO certificate is
// verified against: AdminCertPoolPEM, or the ceremony CA's bundle when it
// is empty.
func systemSOPoolPEM(opts *InitializeOptions, caBundlePEM []byte) []byte {
	if len(opts.AdminCertPoolPEM) > 0 {
		return opts.AdminCertPoolPEM
	}
	return caBundlePEM
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

// pendingSystemSOKey is the system barrier key under which Initialize
// records the system SO it issued, before it reports success.
const pendingSystemSOKey = "ceremony/pending-system-so"

// persistIssuedSystemSOLocked records so in the system barrier, which
// Initialize has unsealed by now. The composer copies the SO into the
// replicated store only once that store exists; a node that crashes before
// then reads it back here on restart (PendingSystemSO), so strict first boot
// is never stranded without its SO. The record carries the SO PIN only as
// its hash, and the barrier encrypts it. Callers hold s.mu.
func (s *CeremonyService) persistIssuedSystemSOLocked(ctx context.Context, so *IssuedSystemSO) error {
	if so == nil {
		return nil
	}
	blob, err := json.Marshal(so)
	if err != nil {
		return &SystemSOPersistError{Op: "encode", Cause: err}
	}
	if err := s.barrier.Put(ctx, pendingSystemSOKey, blob); err != nil {
		return &SystemSOPersistError{Op: "write", Cause: err}
	}
	return nil
}

// PendingSystemSO returns the system SO this node's Initialize issued: the
// one in memory, or, after a restart, the record Initialize left in the
// system barrier. It returns nil, nil when there is none; any other read
// failure is returned, never read as absence.
func (s *CeremonyService) PendingSystemSO(ctx context.Context) (*IssuedSystemSO, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastSystemSO != nil {
		return s.lastSystemSO.clone(), nil
	}
	blob, err := s.barrier.Get(ctx, pendingSystemSOKey)
	if errors.Is(err, seal.ErrSecretNotFound) || (err == nil && len(blob) == 0) {
		return nil, nil
	}
	if err != nil {
		return nil, &SystemSOPersistError{Op: "read", Cause: err}
	}
	var so IssuedSystemSO
	if err := json.Unmarshal(blob, &so); err != nil {
		return nil, &SystemSOPersistError{Op: "decode", Cause: err}
	}
	return &so, nil
}

// ForgetPendingSystemSO removes the system barrier's record of the issued
// system SO once first boot has consumed it, so its PIN hash outlives the
// enrolment nowhere but the PIN store.
func (s *CeremonyService) ForgetPendingSystemSO(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.barrier.Delete(ctx, pendingSystemSOKey); err != nil && !errors.Is(err, seal.ErrSecretNotFound) {
		return &SystemSOPersistError{Op: "remove", Cause: err}
	}
	return nil
}
