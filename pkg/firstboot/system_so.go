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

package firstboot

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// SOConfig is a Security Officer to enrol: tenant create's optional tenant
// SO (bootstrap.TenantProvisionConfig.SO) and first boot's system SO
// (Dependencies.SO) are the same inputs, judged by the same Validate.
type SOConfig struct {
	// Username is the SO's login name. It must differ from the admin's.
	Username string

	// CSRPEM is the SO's certificate signing request, signed through the
	// same CertSigner seam as the admin's. The SO keeps its key.
	CSRPEM []byte

	// CertPEM is the SO's own (bring-your-own) certificate, leaf first,
	// verified against the same trust anchors as the admin's.
	CertPEM []byte

	// ChainPEM holds intermediates for CertPEM's chain. Optional.
	ChainPEM []byte

	// PoolPEM is the trust anchors the ceremony verified CertPEM against,
	// carried by its hand-off. When set, CertPEM is verified against these
	// alone, in place of Dependencies.AdminCertPool: the SO's trust is
	// exactly what the ceremony validated.
	PoolPEM []byte

	// PIN is the SO's second factor, sealed by the caller with
	// secure.NewSecret and committed through an SOPINSetter.
	PIN *secure.Secret

	// PINRecord is the SO's PIN already hashed by auth.HashSOPIN, in place
	// of PIN: the ceremony's hand-off to first boot carries this record,
	// replicated to whichever node runs first boot, and never the PIN.
	// Committed through an SOPINRecordSetter.
	PINRecord string
}

// Validate reports whether c is a whole SO: a username, exactly one of a
// CSR or a certificate, and a live PIN or a PIN record. It returns
// *IncompleteSOError naming the first part missing, or
// *ConflictingSOCredentialError for both a CSR and a certificate.
func (c *SOConfig) Validate() error {
	switch {
	case c.Username == "":
		return &IncompleteSOError{Field: "so_username"}
	case len(c.CSRPEM) == 0 && len(c.CertPEM) == 0:
		return &IncompleteSOError{Field: "so_csr_pem or so_cert_pem"}
	case (c.PIN == nil || !c.PIN.IsAlive()) && c.PINRecord == "":
		return &IncompleteSOError{Field: "so_pin"}
	case len(c.CSRPEM) > 0 && len(c.CertPEM) > 0:
		return &ConflictingSOCredentialError{}
	}
	return nil
}

// SOPINSetter commits a tenant's SO PIN. The node's SO authenticator
// (auth.SOAuthenticator) implements it.
type SOPINSetter interface {
	SetSOPIN(ctx context.Context, tenantID string, pin []byte) error
}

// SOPINRecordSetter commits a tenant's SO PIN already hashed by
// auth.HashSOPIN. An SOPINSetter that also implements it accepts an
// SOConfig.PINRecord; auth.SOAuthenticator does.
type SOPINRecordSetter interface {
	CommitSOPINRecord(ctx context.Context, tenantID, record string) error
}

// commitSystemSOPIN commits so's PIN, or its PIN record, through pins.
func commitSystemSOPIN(ctx context.Context, pins SOPINSetter, tenantID string, so *SOConfig) error {
	if so.PIN != nil && so.PIN.IsAlive() {
		return CommitSOPIN(ctx, pins, tenantID, so.PIN)
	}
	records, ok := pins.(SOPINRecordSetter)
	if !ok {
		return &MissingSOSeamError{Seam: "an SOPINs that commits a hashed PIN record (SOPINRecordSetter)"}
	}
	return records.CommitSOPINRecord(ctx, tenantID, so.PINRecord)
}

// CommitSOPIN commits pin as tenantID's SO PIN through pins. The PIN is
// opened for exactly this call; the opened buffer is destroyed when it
// returns, and pin itself stays the caller's to destroy. pins' error is
// returned unchanged (auth.ErrSOPINAlreadySet when a PIN is already set).
func CommitSOPIN(ctx context.Context, pins SOPINSetter, tenantID string, pin *secure.Secret) error {
	return pin.WithBytes(ctx, func(b []byte) error {
		return pins.SetSOPIN(ctx, tenantID, b)
	})
}

// SOPINVerifier verifies a presented PIN against a tenant's committed SO
// PIN. auth.SOAuthenticator implements it.
type SOPINVerifier interface {
	VerifySOPIN(ctx context.Context, tenantID string, pin []byte) error
}

// SOPINRecordMatcher reports whether a tenant's committed SO PIN record is
// record. auth.SOAuthenticator implements it.
type SOPINRecordMatcher interface {
	SOPINRecordMatches(ctx context.Context, tenantID, record string) (bool, error)
}

// TrustAnchorAdder persists a tenant's client-trust anchors.
// trustanchor.Registry satisfies it; adding an anchor already held is a
// no-op, so a retried first boot adds the SO's pool again safely.
type TrustAnchorAdder interface {
	Add(ctx context.Context, tenantID string, bundlePEM []byte) ([]trustanchor.Anchor, error)
}

// ClientTrustRefresher rebuilds the node's runtime client trust from the
// stored anchors and returns once the rebuild is published.
// *clienttrust.Pool satisfies it.
type ClientTrustRefresher interface {
	Refresh(ctx context.Context) error
}

// ClientTrustRefreshFunc adapts a function, such as a lifecycle node's
// RefreshClientTrust, to ClientTrustRefresher.
type ClientTrustRefreshFunc func(ctx context.Context) error

// Refresh calls f.
func (f ClientTrustRefreshFunc) Refresh(ctx context.Context) error { return f(ctx) }

// committedSOPINIsOurs returns nil only when tenantID's committed SO PIN is
// so's: its live PIN verifies, or its PIN record is the one committed. An
// SOPINs that can prove neither refuses with MissingSOSeamError.
func committedSOPINIsOurs(ctx context.Context, pins SOPINSetter, tenantID string, so *SOConfig) error {
	if so.PIN != nil && so.PIN.IsAlive() {
		verifier, ok := pins.(SOPINVerifier)
		if !ok {
			return &MissingSOSeamError{Seam: "an SOPINs that verifies a committed PIN (SOPINVerifier)"}
		}
		return so.PIN.WithBytes(ctx, func(b []byte) error {
			return verifier.VerifySOPIN(ctx, tenantID, b)
		})
	}
	matcher, ok := pins.(SOPINRecordMatcher)
	if !ok {
		return &MissingSOSeamError{Seam: "an SOPINs that compares a committed PIN record (SOPINRecordMatcher)"}
	}
	matches, err := matcher.SOPINRecordMatches(ctx, tenantID, so.PINRecord)
	if err != nil {
		return err
	}
	if !matches {
		return auth.ErrSOPINInvalid
	}
	return nil
}

// checkSystemSO is Initialize's pre-flight for the system SO. It runs BEFORE
// any write: a strict deployment without an SO, an SO given in part, an SO
// that is the admin by username or key, and an SO whose enrolment seams are
// not wired are each refused here rather than half-way through first boot.
func checkSystemSO(ctx context.Context, deps Dependencies) error {
	so := deps.SO
	if so == nil {
		// The system SO is optional in both postures (SPEC row 124): a
		// node booted without one refuses only the SO-exclusive
		// operations, which carry their own gates.
		return nil
	}
	if err := so.Validate(); err != nil {
		return err
	}
	// The SO must be a separate identity only under strict separation
	// (SPEC row 124); in the standard posture the bootstrap admin may hold
	// SO, which enrollment grants it as a role on the admin's own account.
	// The username index is case-sensitive, so only the exact bootstrap
	// name is the admin's own account; a case variant such as "Admin" is a
	// separate account and gets no shared-key exemption. Strict separation
	// still refuses any case variant of the admin's name.
	soIsAdmin := so.Username == adminUsername
	if deps.SeparationOfDuties && strings.EqualFold(so.Username, adminUsername) {
		return &SOMatchesAdminError{Field: "username"}
	}
	switch {
	case !implementsUserUpdater(deps.Users):
		// Every SO needs UpdateUser to record its bound key; the admin
		// enrolled as SO also needs it for the SO role grant.
		return &MissingSOSeamError{Seam: "Dependencies.Users implementing UpdateUser to record the SO's bound key and to grant the bootstrap admin the SO role"}
	case deps.SOPINs == nil:
		return &MissingSOSeamError{Seam: "Dependencies.SOPINs to commit its PIN"}
	case (so.PIN == nil || !so.PIN.IsAlive()) && !implementsPINRecordSetter(deps.SOPINs):
		return &MissingSOSeamError{Seam: "Dependencies.SOPINs implementing SOPINRecordSetter to commit its PIN record"}
	case (so.PIN == nil || !so.PIN.IsAlive()) && !implementsPINRecordMatcher(deps.SOPINs):
		return &MissingSOSeamError{Seam: "Dependencies.SOPINs implementing SOPINRecordMatcher to prove its PIN record"}
	case deps.Binder == nil:
		return &MissingSOSeamError{Seam: "Dependencies.Binder to bind its certificate"}
	case len(so.CSRPEM) > 0 && deps.Signer == nil:
		return &MissingSOSeamError{Seam: "Dependencies.Signer to sign its CSR"}
	case len(so.CertPEM) > 0 && deps.AdminCertPool == nil:
		return &MissingSOSeamError{Seam: "Dependencies.AdminCertPool to verify its certificate"}
	case len(so.CertPEM) > 0 && len(so.PoolPEM) > 0 && deps.TrustAnchors == nil:
		return &MissingSOSeamError{Seam: "Dependencies.TrustAnchors to persist its trust pool"}
	case len(so.CertPEM) > 0 && len(so.PoolPEM) > 0 && deps.ClientTrust == nil:
		return &MissingSOSeamError{Seam: "Dependencies.ClientTrust to refresh client trust"}
	}
	soKey, err := spkiThumbprintOf(so.CSRPEM, so.CertPEM)
	if err != nil {
		return &SystemSOEnrollmentError{Op: "certificate", Cause: err}
	}
	if err := checkSOCertLogsInAsUsername(so); err != nil {
		return &SystemSOEnrollmentError{Op: "certificate", Cause: err}
	}
	// A generated admin key (Path A) is fresh and cannot be the SO's; only a
	// key the operator supplies for the admin can collide.
	if len(deps.AdminCSRPEM) == 0 && len(deps.AdminCertPEM) == 0 {
		return nil
	}
	// Strict separation needs the SO's key distinct from the admin's. In the
	// standard posture only the admin enrolled as SO may share it: one key
	// binds to one account, so two accounts cannot hold it.
	adminKey, err := spkiThumbprintOf(deps.AdminCSRPEM, deps.AdminCertPEM)
	if err == nil && adminKey == soKey && (deps.SeparationOfDuties || !soIsAdmin) {
		return &SOMatchesAdminError{Field: "key"}
	}
	return nil
}

// userUpdater is the user surface's update verb, through which a
// standard-posture first boot grants the bootstrap admin the SO role.
type userUpdater interface {
	UpdateUser(ctx context.Context, req *user.UpdateUserRequest) (*user.User, error)
}

// implementsUserUpdater reports whether users can grant a role to an
// existing account.
func implementsUserUpdater(users UserCreator) bool {
	_, ok := users.(userUpdater)
	return ok
}

// grantAdminSORole adds soRoleID to the bootstrap admin's account, the
// standard-posture enrollment of the admin as the system SO.
func grantAdminSORole(ctx context.Context, deps Dependencies, admin *user.User, soRoleID string) (*user.User, error) {
	updater, ok := deps.Users.(userUpdater)
	if !ok {
		return nil, &MissingSOSeamError{Seam: "Dependencies.Users implementing UpdateUser to grant the bootstrap admin the SO role"}
	}
	roleIDs := append(slices.Clone(admin.RoleIDs), soRoleID)
	updated, err := updater.UpdateUser(ctx, &user.UpdateUserRequest{ID: admin.ID, RoleIDs: roleIDs})
	if err != nil {
		return nil, &SystemSOEnrollmentError{Op: "account", Cause: err}
	}
	if updated == nil {
		return nil, &SystemSOEnrollmentError{Op: "account", Cause: errors.New("the user surface granted the SO role without returning the account")}
	}
	deps.Logger.Info("first-boot: bootstrap admin enrolled as the system SO",
		slog.String("tenant", admin.TenantID),
		slog.Uint64("user_id", admin.ID),
	)
	return updated, nil
}

func implementsPINRecordSetter(pins SOPINSetter) bool {
	_, ok := pins.(SOPINRecordSetter)
	return ok
}

func implementsPINRecordMatcher(pins SOPINSetter) bool {
	_, ok := pins.(SOPINRecordMatcher)
	return ok
}

// spkiThumbprintOf is the SPKI thumbprint of the key a CSR or a
// certificate (whichever is given) names: the identifier a binding holds.
func spkiThumbprintOf(csrPEM, certPEM []byte) (string, error) {
	if len(certPEM) > 0 {
		return spkiThumbprintFromCertPEM(certPEM)
	}
	block, _ := pem.Decode(csrPEM)
	if block == nil {
		return "", errors.New("no CERTIFICATE REQUEST block in PEM input")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return "", err
	}
	return user.ComputeSPKIThumbprint(&x509.Certificate{RawSubjectPublicKeyInfo: csr.RawSubjectPublicKeyInfo}), nil
}

// checkSOCertLogsInAsUsername refuses an SO certificate the SO could never
// authenticate with: one whose subject CN is not the SO's username
// (MTLSAuthenticator logs a client in by its CN), or one outside the
// client-auth profile the ceremony holds --so-cert to. A signed CSR is issued
// for the username, with that profile.
func checkSOCertLogsInAsUsername(so *SOConfig) error {
	if len(so.CertPEM) == 0 {
		return nil
	}
	block, _ := pem.Decode(so.CertPEM)
	if block == nil {
		return errors.New("no CERTIFICATE block in PEM input")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	// auth.HolderProfile classifies a certificate as a custody identity
	// that RBAC refuses every permission: an SO bound to one could perform
	// none of the operations reserved for it. The ceremony refuses it on
	// its adoption path; a certificate supplied directly to first boot is
	// refused here, before any enrollment state is written.
	if profile, ok := auth.HolderProfile(cert); ok {
		return &ceremony.SystemSOHolderCertError{Profile: profile}
	}
	if cert.Subject.CommonName != so.Username {
		return fmt.Errorf("the certificate's subject CN %q is not the SO username %q", cert.Subject.CommonName, so.Username)
	}
	return ceremony.CheckClientAuthProfile(cert)
}

// enrollSystemSO enrols Dependencies.SO in tenantID, after the admin: the
// account with the so role alone, its certificate bound, its PIN
// committed. Idempotent: a re-run finds this SO, binds it only when it
// holds no live binding, accepts a PIN an earlier run already set, and
// returns a CSR-signed SO's certificate again, reissued for its bound key.
// An SO from a retained record whose PIN is committed but whose account is
// gone was enrolled and deleted since, and is not re-created
// (pendingSOAccountDeleted).
func enrollSystemSO(ctx context.Context, deps Dependencies, tenantID string, result *Result) error {
	so := deps.SO
	if so == nil {
		return nil
	}
	deleted, err := pendingSOAccountDeleted(ctx, deps, tenantID)
	if err != nil {
		return err
	}
	if deleted {
		result.SORetired = true
		deps.Logger.Warn("first-boot: the pending system SO was enrolled and deleted since; not re-creating it",
			slog.String("username", so.Username), slog.String("tenant", tenantID))
		return nil
	}
	soUser, created, err := createSystemSO(ctx, deps, tenantID)
	if err != nil {
		return err
	}
	result.SOCreated = created
	result.SOUserID = soUser.ID

	// The SO's key must be bound to its account: an account that already
	// holds another live binding (the bootstrap admin enrolled as SO under a
	// key of its own) still needs the SO's.
	soKey, err := spkiThumbprintOf(so.CSRPEM, so.CertPEM)
	if err != nil {
		return &SystemSOEnrollmentError{Op: "certificate", Cause: err}
	}
	// A key this enrolment bound once and that has no binding since was
	// withdrawn by an operator: a retained pending record must not bring it
	// back. An enrolment that stopped before its binding left no record of
	// one, and is completed below.
	if !created && soKeyWithdrawn(soUser, soKey) {
		deps.Logger.Warn("first-boot: the pending system SO's key was bound and withdrawn since; not binding it again",
			slog.String("username", so.Username), slog.String("tenant", tenantID))
		return nil
	}
	// The PIN is committed, or proven ours, before any trust anchor or
	// binding: a run with someone else's PIN must leave no SO credential.
	if err := commitSystemSOPIN(ctx, deps.SOPINs, tenantID, so); err != nil {
		// A PIN already set is ours only once it proves to be: an earlier
		// run of this node may have committed it before it stopped, and in
		// a cluster a peer booting the same SO config commits the same PIN
		// as soon as this node's SO account replicates to it, so even the
		// node that created the SO can find its own PIN already set. A PIN
		// that does not verify against ours is someone else's and refused.
		if !errors.Is(err, auth.ErrSOPINAlreadySet) {
			return &SystemSOEnrollmentError{Op: "PIN commit", Cause: err}
		}
		if ownErr := committedSOPINIsOurs(ctx, deps.SOPINs, tenantID, so); ownErr != nil {
			return &SystemSOEnrollmentError{Op: "PIN commit", Cause: errors.Join(err, ownErr)}
		}
	}
	// A live binding for the SO's own key is required: another key's live
	// binding does not enrol it, and an expired one for its key would leave
	// its certificate failing User.MatchesCert. Rebinding renews it.
	if created || verifyAdminBindings(soUser, time.Now().UTC()) != nil || !soUser.HasCertBinding(soKey) {
		certPEM, err := issueSOCert(ctx, deps, tenantID)
		if err != nil {
			return &SystemSOEnrollmentError{Op: "certificate", Cause: err}
		}
		thumbprint, err := spkiThumbprintFromCertPEM(certPEM)
		if err != nil {
			return &SystemSOEnrollmentError{Op: "certificate", Cause: err}
		}
		// Before the binding: a retry repeats this branch until the SO is
		// bound, so the anchors are installed whenever a binding is.
		if err := installSOTrustAnchors(ctx, deps, tenantID); err != nil {
			return err
		}
		if err := deps.Binder.BindAdminCert(ctx, tenantID, soUser.ID, thumbprint); err != nil {
			return &SystemSOEnrollmentError{Op: "binding", Cause: err}
		}
		if err := recordSOBoundKey(ctx, deps, soUser, thumbprint); err != nil {
			return err
		}
		result.SOCertPEM = certPEM
		result.SOCertThumbprint = thumbprint
		deps.Logger.Info("first-boot: system SO cert bound",
			slog.String("tenant", tenantID),
			slog.Uint64("user_id", soUser.ID),
		)
		return nil
	}
	// The SO is already bound to its key. Its certificate was returned
	// only by the run that bound it, and that run may have failed after
	// the binding (the admin binding is the last write), so the embedder
	// may hold the SO's key with no login certificate. A CSR is signed
	// again for the same key, which the standing binding already covers,
	// and returned; a presented certificate is the embedder's own. A run
	// that stopped between the binding and its record records it now.
	if err := recordSOBoundKey(ctx, deps, soUser, soKey); err != nil {
		return err
	}
	if len(so.CertPEM) > 0 {
		return nil
	}
	certPEM, err := issueSOCert(ctx, deps, tenantID)
	if err != nil {
		return &SystemSOEnrollmentError{Op: "certificate", Cause: err}
	}
	thumbprint, err := spkiThumbprintFromCertPEM(certPEM)
	if err != nil {
		return &SystemSOEnrollmentError{Op: "certificate", Cause: err}
	}
	if thumbprint != soKey {
		return &SystemSOEnrollmentError{Op: "certificate", Cause: errors.New("the reissued certificate does not carry the SO's bound key")}
	}
	result.SOCertPEM = certPEM
	result.SOCertThumbprint = thumbprint
	return nil
}

// soBoundKeyAttribute names the SO account attribute recording the SPKI
// thumbprint first boot bound to it. It is written after the binding, so an
// account carrying it with no binding for that key had the key withdrawn,
// while one without it never finished its enrolment.
const soBoundKeyAttribute = "qrdb.firstboot.so_bound_key"

// soKeyWithdrawn reports whether soKey was bound to the SO by an earlier
// first boot and holds no binding now, expired or live: an expired binding
// is renewed, a removed one stays removed.
func soKeyWithdrawn(soUser *user.User, soKey string) bool {
	if soUser == nil || soUser.Attributes[soBoundKeyAttribute] != soKey {
		return false
	}
	for i := range soUser.CertBindings {
		if soUser.CertBindings[i].Thumbprint == soKey {
			return false
		}
	}
	return true
}

// recordSOBoundKey records on the SO account that thumbprint is bound to it,
// once. It runs after BindAdminCert and before the run can succeed, so a
// pending record is consumed only once its binding is recorded.
func recordSOBoundKey(ctx context.Context, deps Dependencies, soUser *user.User, thumbprint string) error {
	if soUser.Attributes[soBoundKeyAttribute] == thumbprint {
		return nil
	}
	updater, ok := deps.Users.(userUpdater)
	if !ok {
		return &MissingSOSeamError{Seam: "Dependencies.Users implementing UpdateUser to record the SO's bound key"}
	}
	attrs := maps.Clone(soUser.Attributes)
	if attrs == nil {
		attrs = map[string]string{}
	}
	attrs[soBoundKeyAttribute] = thumbprint
	if _, err := updater.UpdateUser(ctx, &user.UpdateUserRequest{ID: soUser.ID, Attributes: attrs}); err != nil {
		return &SystemSOEnrollmentError{Op: "binding", Cause: err}
	}
	return nil
}

// issueSOCert signs the SO's CSR through Signer or adopts its certificate
// against AdminCertPool, exactly as the admin's Path B and Path C do.
func issueSOCert(ctx context.Context, deps Dependencies, tenantID string) ([]byte, error) {
	so := deps.SO
	if len(so.CertPEM) > 0 {
		pool := deps.AdminCertPool
		if len(so.PoolPEM) > 0 {
			pool = x509.NewCertPool()
			if !pool.AppendCertsFromPEM(so.PoolPEM) {
				return nil, &AdminCertRejectedError{Cause: errors.New("the SO's trust pool contains no usable certificates")}
			}
		}
		return adoptPresentedCert(so.CertPEM, so.ChainPEM, pool, deps.TrustDomain, tenantID)
	}
	return deps.Signer.SignAdminCSR(ctx, tenantID, so.Username, so.CSRPEM)
}

// installSOTrustAnchors persists the trust pool a presented SO certificate
// was verified against (SO.PoolPEM) as the tenant's client-trust anchors
// and refreshes runtime client trust from them, so the SO's login verifies
// at the node's TLS listener. An SO with no pool of its own was verified
// against the pool the node already trusts, and installs nothing.
func installSOTrustAnchors(ctx context.Context, deps Dependencies, tenantID string) error {
	so := deps.SO
	if len(so.CertPEM) == 0 || len(so.PoolPEM) == 0 {
		return nil
	}
	if _, err := deps.TrustAnchors.Add(ctx, tenantID, so.PoolPEM); err != nil {
		return &SystemSOEnrollmentError{Op: "trust anchors", Cause: err}
	}
	if err := deps.ClientTrust.Refresh(ctx); err != nil {
		return &SystemSOEnrollmentError{Op: "trust refresh", Cause: err}
	}
	deps.Logger.Info("first-boot: system SO trust anchors installed", slog.String("tenant", tenantID))
	return nil
}

// pendingSOAccountDeleted reports whether deps.SO, an SO handed over as a
// PIN record (a retained pending record, never a live PIN), was enrolled
// by an earlier run and deleted since: tenantID's committed SO PIN is its
// record, which enrollSystemSO commits only after creating the account,
// and no account holds its username once the lookup has had
// adminConvergeWindow to see a replicated create. Such an SO is never
// re-created: withdrawn access must not return on restart.
func pendingSOAccountDeleted(ctx context.Context, deps Dependencies, tenantID string) (bool, error) {
	so := deps.SO
	if so == nil || so.PINRecord == "" || (so.PIN != nil && so.PIN.IsAlive()) {
		return false, nil
	}
	matcher, ok := deps.SOPINs.(SOPINRecordMatcher)
	if !ok {
		return false, &MissingSOSeamError{Seam: "an SOPINs that compares a committed PIN record (SOPINRecordMatcher)"}
	}
	matches, err := matcher.SOPINRecordMatches(ctx, tenantID, so.PINRecord)
	if err != nil {
		return false, &SystemSOEnrollmentError{Op: "PIN check", Cause: err}
	}
	if !matches {
		return false, nil
	}
	users := userResolver(deps)
	if users == nil {
		return false, &MissingSOSeamError{Seam: "a UserResolver to look up the SO"}
	}
	deadline := time.Now().Add(adminConvergeWindow)
	for {
		existing, lookupErr := users.GetUserByUsername(ctx, tenantID, so.Username)
		if lookupErr == nil && existing != nil {
			return false, nil
		}
		var notFound *user.UserNotFoundError
		if lookupErr != nil && !errors.As(lookupErr, &notFound) {
			return false, &SystemSOEnrollmentError{Op: "account", Cause: lookupErr}
		}
		if !time.Now().Before(deadline) {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, &SystemSOEnrollmentError{Op: "account", Cause: ctx.Err()}
		case <-time.After(adminConvergeInterval):
		}
	}
}

// createSystemSO creates the system SO's account with the so role alone. A
// collision on its username or generated id is first checked against the
// account holding the username: this SO is the idempotent re-run, an
// unresolved holder is a racing create still settling (retried within
// adminConvergeWindow), and anyone else is *SystemSOConflictError.
func createSystemSO(ctx context.Context, deps Dependencies, tenantID string) (*user.User, bool, error) {
	so := deps.SO
	soRoleID := role.SystemRoleID(tenantID, role.RoleNameSO)
	adminRoleID := role.SystemRoleID(tenantID, role.RoleNameAdmin)
	users := userResolver(deps)
	var convergeDeadline time.Time
	idCollisions := 0
	for {
		soUser, createErr := deps.Users.CreateUser(ctx, &user.CreateUserRequest{
			// The SO authenticates by certificate and PIN, not by an email
			// login key.
			WithoutEmailLoginKey: true,
			Username:             so.Username,
			TenantID:             tenantID,
			RoleIDs:              []string{soRoleID},
		})
		if createErr == nil {
			if soUser == nil {
				return nil, false, &SystemSOEnrollmentError{Op: "account", Cause: errors.New("the user surface created the SO without returning it")}
			}
			deps.Logger.Info("first-boot: system SO created",
				slog.String("username", so.Username),
				slog.String("tenant", tenantID),
			)
			return soUser, true, nil
		}
		var usernameExists *user.UsernameExistsError
		var idExists *user.UserIDExistsError
		usernameTaken := errors.As(createErr, &usernameExists)
		if !usernameTaken && !errors.As(createErr, &idExists) {
			return nil, false, &SystemSOEnrollmentError{Op: "account", Cause: createErr}
		}
		if users == nil {
			return nil, false, &SystemSOEnrollmentError{Op: "account", Cause: createErr}
		}
		existing, lookupErr := users.GetUserByUsername(ctx, tenantID, so.Username)
		if lookupErr == nil && existing != nil {
			if !deps.SeparationOfDuties && so.Username == adminUsername &&
				existing.TenantID == tenantID && slices.Contains(existing.RoleIDs, adminRoleID) &&
				!slices.Contains(existing.RoleIDs, soRoleID) &&
				existing.Lifecycle.Valid() && !existing.Lifecycle.Terminal() {
				updated, err := grantAdminSORole(ctx, deps, existing, soRoleID)
				if err != nil {
					return nil, false, err
				}
				return updated, false, nil
			}
			if existing.TenantID != tenantID ||
				!slices.Contains(existing.RoleIDs, soRoleID) ||
				(deps.SeparationOfDuties && slices.Contains(existing.RoleIDs, adminRoleID)) ||
				!existing.Lifecycle.Valid() || existing.Lifecycle.Terminal() {
				return nil, false, &SystemSOConflictError{Username: so.Username}
			}
			return existing, false, nil
		}
		if usernameTaken {
			if convergeDeadline.IsZero() {
				convergeDeadline = time.Now().Add(adminConvergeWindow)
			}
			if !time.Now().Before(convergeDeadline) {
				return nil, false, &SystemSOEnrollmentError{Op: "account", Cause: createErr}
			}
			select {
			case <-ctx.Done():
				return nil, false, &SystemSOEnrollmentError{Op: "account", Cause: ctx.Err()}
			case <-time.After(adminConvergeInterval):
			}
			continue
		}
		idCollisions++
		if idCollisions >= adminCreateAttempts {
			return nil, false, &SystemSOEnrollmentError{Op: "account", Cause: createErr}
		}
	}
}
