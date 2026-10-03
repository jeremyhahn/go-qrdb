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
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"log/slog"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// adminUsername is the bootstrap admin's fixed username. It is duplicated
// from user.BootstrapAdminUsername (rather than imported) because pkg/user
// imports pkg/tenant, which imports pkg/ceremony for CeremonyCA -- importing
// pkg/user here would cycle. pkg/bootstrap's own admin-username default
// carries the identical duplication for the identical reason.
const adminUsername = "admin"

// adminCertProfile is the certificate profile the bootstrap admin's login
// certificate carries, on every one of the three credential paths. It is the
// same shape sign-csr used to issue under the (now removed) "admin" role
// entry in roleProfileMap, kept here as the single remaining place that
// mints a system-tenant login certificate for "admin" (go-qrdb#548).
var adminCertProfile = &roleProfile{
	profile:      "administrator",
	validityDays: 365,
	keyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	extKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
}

// issueAdminCredential resolves opts' admin-credential inputs into exactly
// one of the three supported paths and produces the bootstrap admin's
// certificate synchronously, during Initialize, where the CA is available.
// Binding the certificate to the admin's user record happens later, at first
// boot, once the admin account exists; see docs/firstboot/architecture.md.
//
//   - Both AdminCSRPEM and AdminCertPEM set: refused,
//     *ConflictingAdminCredentialInputsError.
//   - AdminCertPEM set (BYOC): validated against AdminCertPoolPEM, or against
//     caBundlePEM when that is empty, and adopted unchanged. Nothing is
//     signed; keyPEM is always nil; no CeremonyCA is needed at all -- the
//     ONLY admin-credential path available when ca is nil (a BYO-TLS node).
//   - AdminCSRPEM set (CSR-sign): the ceremony CA signs it as
//     CN=admin, OU=[system, administrator], using only the CSR's key.
//     keyPEM is always nil -- the admin's key never leaves their machine.
//     Refused with *AdminCredentialRequiresCAError when ca is nil.
//   - Neither set (generate, the default): a key pair and CSR are generated
//     in-process and signed the identical way. keyPEM carries the generated
//     key, returned to the caller exactly once. NOT suitable for production;
//     see docs/firstboot/user.md. Refused with *AdminCredentialRequiresCAError
//     when ca is nil, for the same reason as CSR-sign.
func issueAdminCredential(ctx context.Context, ca CeremonyCA, opts *InitializeOptions, caBundlePEM []byte) (certPEM, keyPEM []byte, source AdminCredentialSource, err error) {
	var csrPEM, certInPEM, poolPEM []byte
	if opts != nil {
		csrPEM = opts.AdminCSRPEM
		certInPEM = opts.AdminCertPEM
		poolPEM = opts.AdminCertPoolPEM
	}

	switch {
	case len(csrPEM) > 0 && len(certInPEM) > 0:
		return nil, nil, AdminCredentialNone, &ConflictingAdminCredentialInputsError{}

	case len(certInPEM) > 0:
		if len(poolPEM) == 0 {
			poolPEM = caBundlePEM
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(poolPEM) {
			return nil, nil, AdminCredentialNone, &AdminCredentialRejectedError{
				Cause: errors.New("trust pool contains no usable certificates"),
			}
		}
		adopted, aerr := adoptAdminCert(certInPEM, pool)
		if aerr != nil {
			return nil, nil, AdminCredentialNone, aerr
		}
		return adopted, nil, AdminCredentialBYOC, nil

	case len(csrPEM) > 0:
		if ca == nil {
			return nil, nil, AdminCredentialNone, &AdminCredentialRequiresCAError{Path: "csr-sign"}
		}
		signed, serr := signAdminCSR(ctx, ca, csrPEM)
		if serr != nil {
			return nil, nil, AdminCredentialNone, serr
		}
		return signed, nil, AdminCredentialCSR, nil

	default:
		if ca == nil {
			return nil, nil, AdminCredentialNone, &AdminCredentialRequiresCAError{Path: "generate"}
		}
		genCSR, genKey, gerr := generateAdminCSR()
		if gerr != nil {
			return nil, nil, AdminCredentialNone, gerr
		}
		signed, serr := signAdminCSR(ctx, ca, genCSR)
		if serr != nil {
			secure.Wipe(genKey)
			return nil, nil, AdminCredentialNone, serr
		}
		return signed, genKey, AdminCredentialGenerated, nil
	}
}

// signAdminCSR signs csrPEM as the bootstrap admin's certificate, through the
// same signHolderCSR/checkIssuedSubject rule sign-csr applies to every holder
// certificate: the Subject comes from opts, never the CSR, and the CSR
// contributes only its key.
func signAdminCSR(ctx context.Context, ca CeremonyCA, csrPEM []byte) ([]byte, error) {
	cert, _, err := signHolderCSR(ctx, ca, csrPEM, &SignCSROptions{
		TenantID:     tenantid.System,
		Username:     adminUsername,
		Profile:      adminCertProfile.profile,
		ValidityDays: adminCertProfile.validityDays,
		KeyUsage:     adminCertProfile.keyUsage,
		ExtKeyUsage:  adminCertProfile.extKeyUsage,
	})
	if err != nil {
		return nil, &AdminCredentialIssuanceError{Cause: err}
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), nil
}

// generateAdminCSR generates an ECDSA P-256 key pair and a self-signed CSR
// for it, CN=admin, so the generate path (Path A) can be signed through the
// exact same signAdminCSR call as the CSR-sign path (Path B). The Subject
// here is discarded by signHolderCSR/checkIssuedSubject regardless -- only
// the key and the proof of possession it supplies matter.
func generateAdminCSR() (csrPEM, keyPEM []byte, err error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, &AdminCredentialIssuanceError{Cause: err}
	}
	template := &x509.CertificateRequest{
		Subject:            pkix.Name{CommonName: adminUsername},
		SignatureAlgorithm: x509.ECDSAWithSHA256,
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, template, priv)
	if err != nil {
		return nil, nil, &AdminCredentialIssuanceError{Cause: err}
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, nil, &AdminCredentialIssuanceError{Cause: err}
	}
	csrPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return csrPEM, keyPEM, nil
}

// adoptAdminCert validates a certificate the admin already holds (Path C,
// BYOC) and returns it unchanged for binding: it must parse, fall inside its
// validity window, and chain to pool. Nothing is signed and no key is
// generated -- the private key never exists outside the operator's own
// token.
//
// This duplicates pkg/firstboot's adoptPresentedAdminCert in spirit (both
// apply the same three checks) rather than calling it: pkg/firstboot cannot
// be imported here without a cycle, because pkg/tenant already imports
// pkg/ceremony for CeremonyCA and pkg/firstboot imports pkg/tenant.
func adoptAdminCert(certPEM []byte, pool *x509.CertPool) ([]byte, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, &AdminCredentialRejectedError{Cause: errors.New("no CERTIFICATE block in PEM input")}
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, &AdminCredentialRejectedError{Cause: err}
	}

	now := time.Now()
	if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
		return nil, &AdminCredentialRejectedError{Cause: errors.New("certificate is outside its validity window")}
	}

	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		return nil, &AdminCredentialRejectedError{Cause: err}
	}

	adopted := make([]byte, len(certPEM))
	copy(adopted, certPEM)
	return adopted, nil
}

// LastIssuedAdminCertPEM returns the bootstrap admin certificate the most
// recent Initialize or ReissueAdminCredential call issued or adopted, held
// ONLY in this service's memory (go-qrdb#548).
//
// It is NOT read from any store: the seal/credential store is per-node by
// design (every cluster node holds its own system barrier and master key,
// never shared across nodes -- docs/seal/per-node-barrier-architecture.md),
// so it cannot carry a record the Raft LEADER's first boot needs, because
// Initialize may run against any node in the cluster. The composer
// (cmd/qrdbd, sdk/go/node) reads this immediately after a successful
// Initialize/ReissueAdminCredential call -- while still on the SAME node,
// across the sealed-window-to-full-boot transition, which reuses this exact
// *CeremonyService instance -- and writes it into the REPLICATED system
// store as soon as that store exists. Empty when nothing has been issued yet.
func (s *CeremonyService) LastIssuedAdminCertPEM() ([]byte, AdminCredentialSource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastAdminCertPEM, s.lastAdminSource
}

// ReissueAdminCredential re-runs the admin-credential step (go-qrdb#548) on
// an ALREADY-initialized ceremony: it issues a fresh bootstrap admin
// certificate through the same CSR-sign / BYOC / generate resolution
// Initialize applies, and updates LastIssuedAdminCertPEM. It does not touch
// the barrier, the SO/User PIN, or the M-of-N/single-admin state.
//
// This is the recovery path for the gap between Initialize returning and its
// result reaching the replicated store: if a crash (or any other failure)
// loses that in-memory value before it is durably written, re-running the
// admin-credential step produces a new one. The caller (cmd/qrdbd, via the
// service layer) is responsible for refusing this call once the bootstrap
// admin already holds a live certificate binding -- ceremony has no user
// store to check that itself -- and for writing the result to the replicated
// store exactly as Initialize's result is written.
//
// Returns ErrNotInitializedOrEnrolling when the ceremony has not been
// initialized yet (run Initialize / "qrdb init" first).
func (s *CeremonyService) ReissueAdminCredential(ctx context.Context, opts *InitializeOptions) (*InitResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state == StateAwaitingInit || s.deps.CA == nil {
		return nil, ErrNotInitializedOrEnrolling
	}

	caBundle, err := s.deps.CA.CABundle()
	if err != nil {
		return nil, &AdminCredentialRejectedError{Cause: err}
	}

	certPEM, keyPEM, source, aerr := issueAdminCredential(ctx, s.deps.CA, opts, caBundle)
	if aerr != nil {
		s.logger.Error("bootstrap admin credential reissuance failed", slog.String("error", aerr.Error()))
		return nil, aerr
	}
	s.lastAdminCertPEM = certPEM
	s.lastAdminSource = source
	s.logger.Info("ceremony: bootstrap admin credential reissued",
		slog.String("source", string(source)))

	return &InitResult{
		CACertPEM:             caBundle,
		State:                 s.state,
		AdminCertPEM:          certPEM,
		AdminKeyPEM:           keyPEM,
		AdminCredentialSource: source,
	}, nil
}

// CACertPool returns a pool containing this ceremony's own CA certificate,
// the default trust anchor firstboot validates the bootstrap admin's
// certificate against when binding it (go-qrdb#548). Returns ErrCANotInitialized
// when Initialize has not run.
func (s *CeremonyService) CACertPool() (*x509.CertPool, error) {
	s.mu.Lock()
	ca := s.deps.CA
	s.mu.Unlock()
	if ca == nil {
		return nil, ErrCANotInitialized
	}
	bundle, err := ca.CABundle()
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(bundle) {
		return nil, &AdminCredentialRejectedError{Cause: errors.New("CA bundle contains no usable certificates")}
	}
	return pool, nil
}

// WipeAdminKeyPEM zeroes r's generated admin key material in place and clears
// the field. Transports call this immediately after writing AdminKeyPEM into
// their response: by then the wire encoding already holds its own copy (a
// base64 string in JSON, a proto field, ...), so this reduces how long the
// plaintext key stays resident in the server's heap without affecting what
// was already sent. A nil receiver or an empty key is a no-op.
func (r *InitResult) WipeAdminKeyPEM() {
	if r == nil {
		return
	}
	secure.Wipe(r.AdminKeyPEM)
	r.AdminKeyPEM = nil
}
