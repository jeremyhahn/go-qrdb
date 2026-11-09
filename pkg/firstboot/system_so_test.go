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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony/ceremonytest"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// soUsers is a user store that keeps what it creates: a username is unique,
// a lookup returns the record with its bindings, so a re-run of Initialize
// over it meets the accounts and bindings an earlier run left.
type soUsers struct {
	byName map[string]*user.User
	nextID uint64
	// creates counts CreateUser calls per username.
	creates map[string]int
}

func newSOUsers() *soUsers {
	return &soUsers{byName: map[string]*user.User{}, creates: map[string]int{}}
}

func (s *soUsers) CreateUser(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
	s.creates[req.Username]++
	if _, ok := s.byName[req.Username]; ok {
		return nil, &user.UsernameExistsError{Username: req.Username, TenantID: req.TenantID}
	}
	s.nextID++
	u := &user.User{ID: s.nextID, Username: req.Username, TenantID: req.TenantID, RoleIDs: req.RoleIDs, Lifecycle: user.LifecycleActive}
	s.byName[req.Username] = u
	c := *u
	return &c, nil
}

func (s *soUsers) GetUserByUsername(_ context.Context, tenantID, username string) (*user.User, error) {
	u, ok := s.byName[username]
	if !ok || u.TenantID != tenantID {
		return nil, &user.UserNotFoundError{}
	}
	c := *u
	c.CertBindings = slices.Clone(u.CertBindings)
	return &c, nil
}

func (s *soUsers) byID(id uint64) *user.User {
	for _, u := range s.byName {
		if u.ID == id {
			return u
		}
	}
	return nil
}

// soBinder binds into soUsers, so a bound account reads back bound.
type soBinder struct {
	users *soUsers
	calls map[uint64]int
}

func (b *soBinder) BindAdminCert(_ context.Context, _ string, userID uint64, thumbprint string) error {
	b.calls[userID]++
	u := b.users.byID(userID)
	if u == nil {
		return errors.New("no such user")
	}
	u.CertBindings = append(u.CertBindings, user.CertBinding{Thumbprint: thumbprint, Kind: user.CertBindingSPKIThumbprint})
	return nil
}

// soSigner signs a CSR under a throwaway CA, over the CSR's own key.
type soSigner struct {
	t     *testing.T
	key   *ecdsa.PrivateKey
	ca    *x509.Certificate
	calls []string
}

func newSOSigner(t *testing.T) *soSigner {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "SO test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &soSigner{t: t, key: key, ca: ca}
}

func (s *soSigner) SignAdminCSR(_ context.Context, _, subject string, csrPEM []byte) ([]byte, error) {
	s.calls = append(s.calls, subject)
	block, _ := pem.Decode(csrPEM)
	if block == nil {
		return nil, errors.New("no CSR")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(int64(len(s.calls) + 1)), Subject: pkix.Name{CommonName: subject},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, s.ca, csr.PublicKey, s.key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// soPINs records committed PINs per tenant and refuses a second commit as
// the SO authenticator does.
type soPINs struct {
	pins map[string]string
}

func (p *soPINs) SetSOPIN(_ context.Context, tenantID string, pin []byte) error {
	if _, ok := p.pins[tenantID]; ok {
		return auth.ErrSOPINAlreadySet
	}
	p.pins[tenantID] = string(pin)
	return nil
}

// VerifySOPIN answers as auth.SOAuthenticator does: nil only for the
// committed PIN.
func (p *soPINs) VerifySOPIN(_ context.Context, tenantID string, pin []byte) error {
	committed, ok := p.pins[tenantID]
	switch {
	case !ok:
		return auth.ErrSOPINNotSet
	case committed != string(pin):
		return auth.ErrSOPINInvalid
	}
	return nil
}

func testCSR(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "so"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

func testPIN(t *testing.T) *secure.Secret {
	t.Helper()
	pin, err := secure.NewSecret(context.Background(), []byte("so-pin-246810"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pin.Destroy)
	return pin
}

// soHarness is a strict first boot over stores that persist across runs.
type soHarness struct {
	users  *soUsers
	binder *soBinder
	signer *soSigner
	pins   *soPINs
	deps   Dependencies
}

func newSOHarness(t *testing.T, strict bool) *soHarness {
	t.Helper()
	users := newSOUsers()
	h := &soHarness{
		users:  users,
		binder: &soBinder{users: users, calls: map[uint64]int{}},
		signer: newSOSigner(t),
		pins:   &soPINs{pins: map[string]string{}},
	}
	h.deps = Dependencies{
		MTLSBindingPolicy:  auth.MTLSBindingEnrolled,
		Tenants:            &fakeTenants{},
		Users:              users,
		Roles:              &fakeRoles{},
		Binder:             h.binder,
		Signer:             h.signer,
		SOPINs:             h.pins,
		Logger:             discardLogger(),
		SeparationOfDuties: strict,
		SO:                 &SOConfig{Username: "officer", CSRPEM: testCSR(t), PIN: testPIN(t)},
	}
	return h
}

func TestInitialize_StrictWithoutSORefusesBeforeAnyWrite(t *testing.T) {
	h := newSOHarness(t, true)
	h.deps.SO = nil
	_, err := Initialize(context.Background(), h.deps)
	var required *SystemSORequiredError
	if !errors.As(err, &required) {
		t.Fatalf("Initialize(strict, no SO) = %v, want *SystemSORequiredError", err)
	}
	if !slices.Equal(required.Missing, systemSOInputs) || !strings.Contains(err.Error(), "so_pin") {
		t.Fatalf("Missing = %v (%q), want every SO input named", required.Missing, err)
	}
	if len(h.users.creates) != 0 {
		t.Fatalf("users created before the refusal: %v", h.users.creates)
	}
}

// A strict re-run after an earlier first boot enrolled the SO (its pending
// record consumed) needs no SO again and enrols none.
func TestInitialize_StrictReRunAfterSOEnrolled(t *testing.T) {
	h := newSOHarness(t, true)
	h.deps.SO = nil
	h.deps.SOEnrolled = true
	if _, err := Initialize(context.Background(), h.deps); err != nil {
		t.Fatalf("Initialize(strict, SO enrolled earlier) = %v", err)
	}
	if len(h.users.creates) != 1 {
		t.Fatalf("creates = %v, want the admin alone", h.users.creates)
	}
}

func TestInitialize_StrictEnrolsSystemSO(t *testing.T) {
	h := newSOHarness(t, true)
	result, err := Initialize(context.Background(), h.deps)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	so := h.users.byName["officer"]
	if so == nil || !result.SOCreated || result.SOUserID != so.ID {
		t.Fatalf("SO not created: result=%+v so=%+v", result, so)
	}
	wantRoles := []string{role.SystemRoleID(tenantid.System, role.RoleNameSO)}
	if so.TenantID != tenantid.System || !slices.Equal(so.RoleIDs, wantRoles) {
		t.Fatalf("SO = tenant %q roles %v, want %q %v", so.TenantID, so.RoleIDs, tenantid.System, wantRoles)
	}
	if admin := h.users.byName[adminUsername]; admin == nil || slices.Contains(admin.RoleIDs, wantRoles[0]) {
		t.Fatalf("admin = %+v, want an admin without the so role", admin)
	}
	if len(so.CertBindings) != 1 || so.CertBindings[0].Thumbprint != result.SOCertThumbprint || result.SOCertThumbprint == "" {
		t.Fatalf("SO bindings = %+v, want the signed certificate %q", so.CertBindings, result.SOCertThumbprint)
	}
	csrKey, err := spkiThumbprintOf(h.deps.SO.CSRPEM, nil)
	if err != nil || csrKey != result.SOCertThumbprint {
		t.Fatalf("bound %q, want the CSR's key %q (%v)", result.SOCertThumbprint, csrKey, err)
	}
	if !slices.Equal(h.signer.calls, []string{"officer"}) {
		t.Fatalf("signer calls = %v, want the SO's CSR alone", h.signer.calls)
	}
	if got := h.pins.pins[tenantid.System]; got != "so-pin-246810" {
		t.Fatalf("committed PIN = %q", got)
	}
	if !h.deps.SO.PIN.IsAlive() {
		t.Fatal("Initialize destroyed the caller's PIN; a retrying caller needs it")
	}
}

func TestInitialize_SystemSOReRunIsIdempotent(t *testing.T) {
	h := newSOHarness(t, true)
	if _, err := Initialize(context.Background(), h.deps); err != nil {
		t.Fatalf("first Initialize: %v", err)
	}
	result, err := Initialize(context.Background(), h.deps)
	if err != nil {
		t.Fatalf("re-run Initialize: %v", err)
	}
	so := h.users.byName["officer"]
	if result.SOCreated || result.SOUserID != so.ID || result.SOCertPEM != nil {
		t.Fatalf("re-run result = %+v, want the existing, bound SO", result)
	}
	if h.binder.calls[so.ID] != 1 || len(so.CertBindings) != 1 {
		t.Fatalf("SO bound %d times, %d bindings; a re-run must not rebind a bound SO", h.binder.calls[so.ID], len(so.CertBindings))
	}
}

func TestInitialize_SystemSOReRunBindsAnUnboundSO(t *testing.T) {
	h := newSOHarness(t, true)
	if _, err := Initialize(context.Background(), h.deps); err != nil {
		t.Fatalf("first Initialize: %v", err)
	}
	so := h.users.byName["officer"]
	so.CertBindings = nil // a crash between create and bind
	result, err := Initialize(context.Background(), h.deps)
	if err != nil {
		t.Fatalf("re-run Initialize: %v", err)
	}
	if result.SOCreated || len(so.CertBindings) != 1 || result.SOCertThumbprint == "" {
		t.Fatalf("re-run did not repair the unbound SO: result=%+v bindings=%+v", result, so.CertBindings)
	}
}

func TestInitialize_SystemSORefusals(t *testing.T) {
	tests := []struct {
		name  string
		edit  func(t *testing.T, h *soHarness)
		check func(error) bool
	}{
		{"admin's username", func(_ *testing.T, h *soHarness) { h.deps.SO.Username = "Admin" },
			func(err error) bool { var e *SOMatchesAdminError; return errors.As(err, &e) && e.Field == "username" }},
		{"admin's key", func(_ *testing.T, h *soHarness) { h.deps.AdminCSRPEM = h.deps.SO.CSRPEM },
			func(err error) bool { var e *SOMatchesAdminError; return errors.As(err, &e) && e.Field == "key" }},
		{"no PIN", func(_ *testing.T, h *soHarness) { h.deps.SO.PIN = nil },
			func(err error) bool { var e *IncompleteSOError; return errors.As(err, &e) && e.Field == "so_pin" }},
		{"both credentials", func(_ *testing.T, h *soHarness) { h.deps.SO.CertPEM = []byte("x") },
			func(err error) bool { var e *ConflictingSOCredentialError; return errors.As(err, &e) }},
		{"no PIN seam", func(_ *testing.T, h *soHarness) { h.deps.SOPINs = nil },
			func(err error) bool { var e *MissingSOSeamError; return errors.As(err, &e) }},
		{"no binder", func(_ *testing.T, h *soHarness) { h.deps.Binder = nil },
			func(err error) bool { var e *MissingSOSeamError; return errors.As(err, &e) }},
		{"no signer", func(_ *testing.T, h *soHarness) { h.deps.Signer = nil },
			func(err error) bool { var e *MissingSOSeamError; return errors.As(err, &e) }},
		{"cert without pool", func(t *testing.T, h *soHarness) {
			h.deps.SO.CSRPEM = nil
			h.deps.SO.CertPEM, _ = issueCertFromCA(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
		}, func(err error) bool { var e *MissingSOSeamError; return errors.As(err, &e) }},
		{"unparsable CSR", func(_ *testing.T, h *soHarness) { h.deps.SO.CSRPEM = []byte("junk") },
			func(err error) bool { var e *SystemSOEnrollmentError; return errors.As(err, &e) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newSOHarness(t, true)
			tc.edit(t, h)
			_, err := Initialize(context.Background(), h.deps)
			if !tc.check(err) {
				t.Fatalf("Initialize = %v", err)
			}
			if len(h.users.creates) != 0 {
				t.Fatalf("users created before the refusal: %v", h.users.creates)
			}
		})
	}
}

func TestInitialize_SystemSOUsernameSquatterRefused(t *testing.T) {
	h := newSOHarness(t, true)
	h.users.byName["officer"] = &user.User{ID: 99, Username: "officer", TenantID: tenantid.System, Lifecycle: user.LifecycleActive}
	h.users.nextID = 100
	_, err := Initialize(context.Background(), h.deps)
	var conflict *SystemSOConflictError
	if !errors.As(err, &conflict) || conflict.Username != "officer" {
		t.Fatalf("Initialize = %v, want *SystemSOConflictError", err)
	}
}

func TestInitialize_SystemSOAdoptsPresentedCert(t *testing.T) {
	h := newSOHarness(t, true)
	certPEM, pool := issueNamedCertFromCA(t, h.deps.SO.Username, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	h.deps.SO.CSRPEM = nil
	h.deps.SO.CertPEM = certPEM
	h.deps.AdminCertPool = pool
	result, err := Initialize(context.Background(), h.deps)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	want, _ := spkiThumbprintFromCertPEM(certPEM)
	if result.SOCertThumbprint != want || len(h.signer.calls) != 0 {
		t.Fatalf("bound %q (signer %v), want the presented certificate %q", result.SOCertThumbprint, h.signer.calls, want)
	}
}

// A presented certificate authenticates as its subject CN, so one naming
// anyone but the SO username is refused before any account exists.
func TestInitialize_SystemSOPresentedCertMustNameTheSO(t *testing.T) {
	h := newSOHarness(t, true)
	certPEM, pool := issueNamedCertFromCA(t, "someone-else", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	h.deps.SO.CSRPEM = nil
	h.deps.SO.CertPEM = certPEM
	h.deps.AdminCertPool = pool
	_, err := Initialize(context.Background(), h.deps)
	var enrolErr *SystemSOEnrollmentError
	if !errors.As(err, &enrolErr) || !strings.Contains(err.Error(), `subject CN "someone-else" is not the SO username "officer"`) {
		t.Fatalf("Initialize: %v, want the CN refused", err)
	}
	if len(h.users.byName) != 0 {
		t.Fatalf("an account was created: %v", h.users.byName)
	}
}

func TestInitialize_SystemSOPINCommitFailureOnFreshSO(t *testing.T) {
	h := newSOHarness(t, true)
	h.pins.pins[tenantid.System] = "someone else's"
	_, err := Initialize(context.Background(), h.deps)
	var e *SystemSOEnrollmentError
	if !errors.As(err, &e) || e.Op != "PIN commit" || !errors.Is(err, auth.ErrSOPINAlreadySet) {
		t.Fatalf("Initialize = %v, want a PIN commit failure", err)
	}
	// The retry finds the SO the first attempt created: the PIN set on
	// it is still someone else's, and the enrolment still fails, so the
	// caller never consumes the pending record.
	_, err = Initialize(context.Background(), h.deps)
	if !errors.As(err, &e) || e.Op != "PIN commit" || !errors.Is(err, auth.ErrSOPINAlreadySet) {
		t.Fatalf("retried Initialize = %v, want the PIN commit failure again", err)
	}
	if got := h.pins.pins[tenantid.System]; got != "someone else's" {
		t.Fatalf("committed PIN = %q, want the conflicting PIN untouched", got)
	}
}

func TestInitialize_SystemSOPINRetryWithItsOwnPINSucceeds(t *testing.T) {
	h := newSOHarness(t, true)
	if _, err := Initialize(context.Background(), h.deps); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	// A retry whose PIN is already committed, by the earlier attempt,
	// proves the PIN is its own and succeeds.
	if _, err := Initialize(context.Background(), h.deps); err != nil {
		t.Fatalf("retried Initialize = %v, want nil", err)
	}
}

func TestEnrollSystemSO_PINAlreadySetNeedsAVerifier(t *testing.T) {
	h := newSOHarness(t, true)
	if _, err := Initialize(context.Background(), h.deps); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	// An SOPINs that cannot verify the committed PIN proves nothing.
	h.deps.SOPINs = setOnlyPINs{h.pins}
	_, err := Initialize(context.Background(), h.deps)
	var missing *MissingSOSeamError
	if !errors.As(err, &missing) {
		t.Fatalf("retried Initialize = %v, want MissingSOSeamError", err)
	}
}

// setOnlyPINs commits a PIN and cannot verify one.
type setOnlyPINs struct{ p *soPINs }

func (s setOnlyPINs) SetSOPIN(ctx context.Context, tenantID string, pin []byte) error {
	return s.p.SetSOPIN(ctx, tenantID, pin)
}

// recordPINs commits and compares hashed PIN records.
type recordPINs struct{ records map[string]string }

func (r *recordPINs) SetSOPIN(context.Context, string, []byte) error {
	return errors.New("recordPINs commits records only")
}

func (r *recordPINs) CommitSOPINRecord(_ context.Context, tenantID, record string) error {
	if _, ok := r.records[tenantID]; ok {
		return auth.ErrSOPINAlreadySet
	}
	r.records[tenantID] = record
	return nil
}

func (r *recordPINs) SOPINRecordMatches(_ context.Context, tenantID, record string) (bool, error) {
	committed, ok := r.records[tenantID]
	return ok && committed == record, nil
}

func TestInitialize_SystemSOPINRecordRetry(t *testing.T) {
	for _, tc := range []struct {
		name      string
		committed string
		wantErr   bool
	}{
		{name: "conflicting record", committed: "someone else's", wantErr: true},
		{name: "own record", committed: "", wantErr: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newSOHarness(t, true)
			pins := &recordPINs{records: map[string]string{}}
			h.deps.SOPINs = pins
			h.deps.SO = &SOConfig{Username: "officer", CSRPEM: testCSR(t), PINRecord: "officer's record"}
			if tc.committed != "" {
				pins.records[tenantid.System] = tc.committed
				if _, err := Initialize(context.Background(), h.deps); err == nil {
					t.Fatal("Initialize = nil, want a PIN commit failure")
				}
			} else if _, err := Initialize(context.Background(), h.deps); err != nil {
				t.Fatalf("Initialize: %v", err)
			}
			_, err := Initialize(context.Background(), h.deps)
			var e *SystemSOEnrollmentError
			switch {
			case tc.wantErr && (!errors.As(err, &e) || e.Op != "PIN commit"):
				t.Fatalf("retried Initialize = %v, want a PIN commit failure", err)
			case !tc.wantErr && err != nil:
				t.Fatalf("retried Initialize = %v, want nil", err)
			}
		})
	}
}

// externalSOCert issues name a system-tenant login certificate from a new
// root of its own, returning the leaf and the root, both PEM.
func externalSOCert(t *testing.T, name, rootCN string) (certPEM, rootPEM []byte) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(200),
		Subject:               pkix.Name{CommonName: rootCN},
		NotBefore:             time.Now().Add(-2 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	subject, uris := presentedIdentityOf(t, name, tenantid.System, tenantid.System, presentedTrustDomain)
	leafDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(201),
		Subject:      subject,
		URIs:         uris,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
}

// An SO certificate the ceremony adopted against AdminCertPoolPEM, from an
// issuer other than the ceremony CA, is bound by first boot on the
// handed-off record: the record carries the pool that validated it. A
// certificate from an issuer in neither pool is still refused.
func TestInitialize_AdoptedSystemSOKeepsItsTrustPoolAcrossTheHandoff(t *testing.T) {
	ctx := context.Background()
	soCert, soRoot := externalSOCert(t, "secofficer", "SO Org Root")
	core := testCeremonyCore(t)
	ca, caBundle := ceremonytest.NewCA(t, "test-ca")
	core.SetPINBackend(pintest.NewBackend(t))
	_, err := core.Initialize(ctx, ca, &ceremony.InitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin",
		AdminCSRPEM: testCSR(t), AdminCertPoolPEM: soRoot,
		SOUsername: "secofficer", SOCertPEM: soCert,
	})
	if err != nil {
		t.Fatalf("ceremony Initialize: %v", err)
	}
	store := newMemPendingAdminCertStore()
	if err := WriteIssuedSystemSOIfAny(ctx, core, store, nil); err != nil {
		t.Fatalf("WriteIssuedSystemSOIfAny: %v", err)
	}
	so, err := PendingSystemSO(ctx, store)
	if err != nil || so == nil {
		t.Fatalf("PendingSystemSO = %v, %v", so, err)
	}

	enrol := func(so *SOConfig) (*Result, error) {
		h := newSOHarness(t, true)
		h.deps.SO = so
		h.deps.SOPINs = &recordPINs{records: map[string]string{}}
		// First boot's own pool is the ceremony CA's alone.
		h.deps.AdminCertPool = AdminCertPoolFromCABundle(caBundle)
		h.deps.TrustDomain = presentedTrustDomain
		return Initialize(ctx, h.deps)
	}
	result, err := enrol(so)
	if err != nil {
		t.Fatalf("first boot: %v", err)
	}
	if want, _ := spkiThumbprintFromCertPEM(soCert); result.SOCertThumbprint != want {
		t.Fatalf("bound %q, want the adopted certificate %q", result.SOCertThumbprint, want)
	}

	stranger, _ := externalSOCert(t, "secofficer", "Stranger Root")
	forged := *so
	forged.CertPEM = stranger
	forged.ChainPEM = nil
	var e *SystemSOEnrollmentError
	if _, err := enrol(&forged); !errors.As(err, &e) || e.Op != "certificate" {
		t.Fatalf("first boot with a stranger's certificate = %v, want a certificate failure", err)
	}
}

func TestInitialize_StandardWithoutSOBootsAsBefore(t *testing.T) {
	h := newSOHarness(t, false)
	h.deps.SO = nil
	result, err := Initialize(context.Background(), h.deps)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if result.SOCreated || result.SOUserID != 0 || len(h.users.byName) != 1 || len(h.pins.pins) != 0 {
		t.Fatalf("standard boot without an SO enrolled one: result=%+v users=%d", result, len(h.users.byName))
	}
}

func TestInitialize_StandardEnrolsAnOptionalSO(t *testing.T) {
	h := newSOHarness(t, false)
	result, err := Initialize(context.Background(), h.deps)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if !result.SOCreated || h.users.byName["officer"] == nil {
		t.Fatalf("standard boot with an SO did not enrol it: %+v", result)
	}
}

// The ceremony hand-off carries the PIN hashed; first boot commits that
// record, and the SO's PIN then verifies at the node's SO authenticator.
func TestInitialize_SystemSOFromAHashedPINRecord(t *testing.T) {
	ctx := context.Background()
	h := newSOHarness(t, true)
	record, err := auth.HashSOPIN([]byte("so-pin-8642"))
	if err != nil {
		t.Fatal(err)
	}
	so, err := auth.NewSOAuthenticator(kvstore.NewMemoryKVStore(), audit.NewRingBuffer(8), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	h.deps.SO.PIN = nil
	h.deps.SO.PINRecord = record
	h.deps.SOPINs = so
	if _, err := Initialize(ctx, h.deps); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := so.VerifySOPIN(ctx, tenantid.System, []byte("so-pin-8642")); err != nil {
		t.Fatalf("VerifySOPIN: %v", err)
	}
	if _, err := Initialize(ctx, h.deps); err != nil {
		t.Fatalf("re-run Initialize: %v", err)
	}
}

func TestInitialize_SystemSOPINRecordNeedsARecordSeam(t *testing.T) {
	h := newSOHarness(t, true)
	h.deps.SO.PIN = nil
	h.deps.SO.PINRecord = "A:00"
	_, err := Initialize(context.Background(), h.deps)
	var e *MissingSOSeamError
	if !errors.As(err, &e) || len(h.users.creates) != 0 {
		t.Fatalf("Initialize = %v (creates %v), want *MissingSOSeamError before any write", err, h.users.creates)
	}
}

func TestSystemSOErrorMessages(t *testing.T) {
	cause := errors.New("boom")
	for _, err := range []error{
		&SystemSORequiredError{Missing: systemSOInputs},
		&IncompleteSOError{Field: "so_pin"},
		&ConflictingSOCredentialError{},
		&SOMatchesAdminError{Field: "key"},
		&MissingSOSeamError{Seam: "x"},
		&SystemSOConflictError{Username: "officer"},
		&SystemSOEnrollmentError{Op: "binding", Cause: cause},
	} {
		if !strings.HasPrefix(err.Error(), "firstboot: ") {
			t.Errorf("%T message %q lacks the package prefix", err, err.Error())
		}
	}
	if !errors.Is(&SystemSOEnrollmentError{Op: "binding", Cause: cause}, cause) {
		t.Error("SystemSOEnrollmentError does not unwrap to its cause")
	}
}
