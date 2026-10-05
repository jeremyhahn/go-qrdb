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

	"github.com/jeremyhahn/go-qrdb/pkg/holderprofile"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
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
//   - More than one of AdminCSRPEM, AdminCertPEM, AdminGenerate set: refused,
//     *ConflictingAdminCredentialInputsError.
//   - None set: refused, *NoAdminCredentialSelectedError (go-qrdb#548
//     review) -- there is no credential-less default; generate must be
//     selected explicitly, the same way CSR-sign and BYOC must be.
//   - AdminCertPEM set (BYOC): validated against AdminCertPoolPEM, or against
//     caBundlePEM when that is empty, and adopted unchanged. Nothing is
//     signed; keyPEM is always nil; no CeremonyCA is needed at all -- the
//     ONLY admin-credential path available when ca is nil (a BYO-TLS node).
//   - AdminCSRPEM set (CSR-sign): the ceremony CA signs it as
//     CN=admin, OU=[system, administrator], using only the CSR's key.
//     keyPEM is always nil -- the admin's key never leaves their machine.
//     Refused with *AdminCredentialRequiresCAError when ca is nil.
//   - AdminGenerate set: a key pair and CSR are generated in-process and
//     signed the identical way. keyPEM carries the generated key, returned
//     to the caller exactly once. NOT suitable for production; see
//     docs/firstboot/user.md. Refused with *AdminCredentialRequiresCAError
//     when ca is nil, for the same reason as CSR-sign.
func issueAdminCredential(ctx context.Context, ca CeremonyCA, opts *InitializeOptions, caBundlePEM []byte) (certPEM, keyPEM []byte, source AdminCredentialSource, err error) {
	csrPEM, certInPEM, poolPEM, _, selected := adminCredentialSelection(opts)

	switch {
	case selected > 1:
		return nil, nil, AdminCredentialNone, &ConflictingAdminCredentialInputsError{}

	case selected == 0:
		return nil, nil, AdminCredentialNone, &NoAdminCredentialSelectedError{}

	case len(certInPEM) > 0:
		adopted, aerr := adoptPresentedAdminCert(certInPEM, poolPEM, caBundlePEM)
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

	default: // generate -- the only remaining case once selected == 1
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

// adminCredentialSelection extracts opts' three admin-credential inputs and
// counts how many are set, the one computation issueAdminCredential and
// validateAdminCredentialInputs must agree on exactly -- a drift between
// two independent counts would let one accept a shape the other refuses.
func adminCredentialSelection(opts *InitializeOptions) (csrPEM, certInPEM, poolPEM []byte, generate bool, selected int) {
	if opts != nil {
		csrPEM = opts.AdminCSRPEM
		certInPEM = opts.AdminCertPEM
		poolPEM = opts.AdminCertPoolPEM
		generate = opts.AdminGenerate
	}
	if len(csrPEM) > 0 {
		selected++
	}
	if len(certInPEM) > 0 {
		selected++
	}
	if generate {
		selected++
	}
	return csrPEM, certInPEM, poolPEM, generate, selected
}

// adoptPresentedAdminCert resolves the BYOC trust pool (poolPEM, or
// caBundlePEM as its fallback) and adopts certInPEM through it. Factored out
// of issueAdminCredential's BYOC case so validateAdminCredentialInputs can
// run the IDENTICAL check: adoptAdminCert is pure (parsing and chain
// verification only, no CA interaction), so calling it ahead of time to
// validate costs nothing and cannot drift from the value issueAdminCredential
// goes on to use.
func adoptPresentedAdminCert(certInPEM, poolPEM, caBundlePEM []byte) ([]byte, error) {
	if len(poolPEM) == 0 {
		poolPEM = caBundlePEM
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(poolPEM) {
		return nil, &AdminCredentialRejectedError{
			Cause: errors.New("trust pool contains no usable certificates"),
		}
	}
	return adoptAdminCert(certInPEM, pool)
}

// validateAdminCredentialInputs performs every admin-credential input check
// that does not require actually signing or generating anything: the
// selection-conflict/none-selected count (adminCredentialSelection, shared
// with issueAdminCredential), the BYOC chain validation
// (adoptPresentedAdminCert, pure), and the requires-a-CA refusal for
// CSR-sign/generate. Initialize calls it BEFORE provisioning the PINs,
// bringing the system barrier up, or issuing the TLS certificate (go-qrdb#548
// review: B3, "init is atomic" -- a malformed admin-credential request must
// be refused before any of those side effects, not after them). A caller
// that passes this check is guaranteed issueAdminCredential, called later,
// will not fail on input shape -- only on a signing/generation failure
// specific to the CSR-sign or generate path, which has no cheaper way to
// detect ahead of time.
func validateAdminCredentialInputs(ca CeremonyCA, opts *InitializeOptions, caBundlePEM []byte) error {
	csrPEM, certInPEM, poolPEM, generate, selected := adminCredentialSelection(opts)

	switch {
	case selected > 1:
		return &ConflictingAdminCredentialInputsError{}

	case selected == 0:
		return &NoAdminCredentialSelectedError{}

	case len(certInPEM) > 0:
		if _, err := adoptPresentedAdminCert(certInPEM, poolPEM, caBundlePEM); err != nil {
			return err
		}
		return nil

	case len(csrPEM) > 0:
		if ca == nil {
			return &AdminCredentialRequiresCAError{Path: "csr-sign"}
		}
		if _, err := parseAndVerifyCSR(csrPEM); err != nil {
			return err
		}
		return nil

	default: // generate -- the only remaining case once selected == 1
		if !generate {
			// Unreachable: selected == 1 and neither csrPEM nor certInPEM is
			// set, so generate must be true. Guard kept explicit rather than
			// silently falling through, matching issueAdminCredential's own
			// "default: // generate" comment.
			return &NoAdminCredentialSelectedError{}
		}
		if ca == nil {
			return &AdminCredentialRequiresCAError{Path: "generate"}
		}
		return nil
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
	// keyDER is the raw, unencoded private key: pem.EncodeToMemory above
	// already copied it into keyPEM's own PEM block, so this copy has no
	// further use and must not be left unwiped (go-qrdb#560 review item
	// 7) alongside the one keyPEM callers actually need and are
	// responsible for wiping once they are done with it.
	secure.Wipe(keyDER)
	return csrPEM, keyPEM, nil
}

// adoptAdminCert validates a certificate the admin already holds (Path C,
// BYOC) and returns it unchanged for binding: it must parse, fall inside its
// validity window, chain to pool, and -- go-qrdb#548 review, item 8's BYOC
// checklist -- carry exactly the profile the bootstrap admin's own
// certificate always carries: CN=admin, system tenant as the first Subject
// OU, NO holder-profile OU (a Security Officer or custodian share-holder
// certificate can never stand for the admin), and a KeyUsage/ExtKeyUsage
// shape that actually supports client TLS authentication. Unlike an
// enrolled officer's BYOC certificate (pkg/firstboot's own
// adoptPresentedAdminCert, which defers EKU to login-time authorization),
// this one check IS the authorization: adopting it grants system-tenant
// Admin outright, with no later gate, so the checklist is enforced here,
// before binding, not deferred. Nothing is signed and no key is generated
// -- the private key never exists outside the operator's own token.
//
// This duplicates pkg/firstboot's adoptPresentedAdminCert's parse/validity/
// chain checks in spirit rather than calling it: pkg/firstboot cannot be
// imported here without a cycle, because pkg/tenant already imports
// pkg/ceremony for CeremonyCA and pkg/firstboot imports pkg/tenant. The
// admin-profile checklist below has no counterpart there at all -- it is
// specific to this path.
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

	if err := validateAdminCertProfile(cert); err != nil {
		return nil, err
	}

	adopted := make([]byte, len(certPEM))
	copy(adopted, certPEM)
	return adopted, nil
}

// validateAdminCertProfile enforces the BYOC admin checklist (go-qrdb#548
// review, item 8): CN=admin, system tenant as the first Subject OU, no
// holder-profile OU anywhere in the Subject, and a KeyUsage/ExtKeyUsage
// shape that supports client TLS authentication (digital signature, and
// either ExtKeyUsageClientAuth or ExtKeyUsageAny). A certificate failing any
// of these is refused with *AdminCredentialRejectedError before it is ever
// adopted.
func validateAdminCertProfile(cert *x509.Certificate) error {
	if cert.Subject.CommonName != adminUsername {
		return &AdminCredentialRejectedError{
			Cause: errors.New("certificate CommonName must be " + adminUsername + ", got " + cert.Subject.CommonName),
		}
	}
	if len(cert.Subject.OrganizationalUnit) == 0 || cert.Subject.OrganizationalUnit[0] != tenantid.System {
		return &AdminCredentialRejectedError{
			Cause: errors.New("certificate must carry the system tenant as its first Subject Organizational Unit"),
		}
	}
	for _, ou := range cert.Subject.OrganizationalUnit {
		if holderprofile.Is(ou) {
			return &AdminCredentialRejectedError{
				Cause: errors.New("certificate carries holder-profile OU " + ou + "; a holder certificate cannot stand for the bootstrap admin"),
			}
		}
	}
	if cert.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		return &AdminCredentialRejectedError{
			Cause: errors.New("certificate KeyUsage does not include digital signature"),
		}
	}
	clientAuth := false
	for _, eku := range cert.ExtKeyUsage {
		if eku == x509.ExtKeyUsageClientAuth || eku == x509.ExtKeyUsageAny {
			clientAuth = true
			break
		}
	}
	if !clientAuth {
		return &AdminCredentialRejectedError{
			Cause: errors.New("certificate ExtKeyUsage does not include client authentication"),
		}
	}
	return nil
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

// VerifyReissueSOPIN verifies soPIN against the SO PIN this ceremony was
// provisioned with -- the SAME gate ReissueAdminCredential itself applies
// before any side effect (pin.RequireProvisionedSOPIN).
//
// Exported so a composer's wrapper (service.CeremonyServiceWithAdminRebind)
// can check it FIRST, before any other read of ceremony or admin state --
// an admin-binding lookup, in particular -- that would otherwise tell an
// UNAUTHENTICATED caller something (whether the admin is already bound, or
// that a lookup is failing) before that caller has ever presented a valid
// SO PIN (go-qrdb#560 review B1, re-review: "check the SO PIN before
// returning AdminAlreadyBound or a lookup error, so unauthenticated callers
// learn nothing").
func (s *CeremonyService) VerifyReissueSOPIN(ctx context.Context, soPIN string) error {
	s.mu.Lock()
	pins := s.pins
	s.mu.Unlock()
	return pin.RequireProvisionedSOPIN(ctx, pins, soPIN)
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
// Returns ErrNotInitialized when the ceremony has not been initialized yet
// (run Initialize / "qrdb init" first).
func (s *CeremonyService) ReissueAdminCredential(ctx context.Context, opts *InitializeOptions) (*InitResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state == StateRestoreFailed {
		return nil, ErrRestoreFailed
	}
	if s.state == StateAwaitingInit {
		return nil, ErrNotInitialized
	}

	// Reissuing the bootstrap admin's certificate is as powerful as minting
	// it the first time (go-qrdb#560 review B1): it hands the caller a fresh
	// credential this process will bind to the admin account. Gated behind
	// the SO PIN this ceremony was provisioned with, verified before any
	// side effect -- the CA bundle read, the credential issuance, all of
	// it -- so a caller with no SO PIN cannot reach any of them.
	//
	// pin.RequireProvisionedSOPIN, not RequireSOPIN (go-qrdb#560 re-review):
	// an already-initialized ceremony always has a provisioned SO PIN --
	// validateInitOptions refuses an empty one, and Initialize provisions it
	// unconditionally before this state is ever reachable -- so admitting an
	// UNPROVISIONED backend here, as RequireSOPIN's own documented "nothing
	// to check yet" convenience does, could never be the correct answer for
	// THIS call; it would only ever mean the invariant above was violated,
	// and this must refuse rather than silently agree with a caller who
	// presented no PIN at all.
	var soPIN string
	if opts != nil {
		soPIN = opts.SOPin
	}
	if err := pin.RequireProvisionedSOPIN(ctx, s.pins, soPIN); err != nil {
		return nil, err
	}

	// resolveCABundlePEM, exactly as Initialize uses it: a configured
	// CeremonyCA's own bundle, or this node's configured client-CA trust
	// anchor when deps.CA is nil (BYO-TLS, go-qrdb#560 re-review item 3,
	// which this call wrongly refused outright before resolving anything,
	// and would otherwise nil-pointer on deps.CA.CABundle() below -- BYOC
	// reissue needs no CeremonyCA at all).
	caBundle, err := s.resolveCABundlePEM(s.deps.CA)
	if err != nil {
		return nil, err
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
