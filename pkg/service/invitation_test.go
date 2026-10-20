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

package service

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/invite"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// invitationResolver gives every tenant its own in-memory store, as the
// production resolver gives every tenant its own keyspace.
type invitationResolver struct {
	mu     sync.Mutex
	stores map[string]kvstore.KVStore
}

func (r *invitationResolver) Resolve(_ context.Context, tenantID string) (kvstore.KVStore, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.stores[tenantID]
	if !ok {
		s = kvstore.NewMemoryKVStore()
		r.stores[tenantID] = s
	}
	return s, nil
}

var _ tenant.StoreResolver = (*invitationResolver)(nil)

// fixtureTenantGetter answers GetTenant for the fixture's own tenants; any
// other id is unknown, as a real TenantServicer answers for one that was
// never created.
type fixtureTenantGetter struct{}

func (fixtureTenantGetter) GetTenant(_ context.Context, tenantID string) (*tenant.TenantInfo, error) {
	switch tenantID {
	case "acme", "beta":
		return &tenant.TenantInfo{ID: tenantID}, nil
	default:
		return nil, tenant.ErrTenantNotFound
	}
}

var fixtureTenants = fixtureTenantGetter{}

// invitationFixture is a real tenant-routed user and role service, a real
// invitation store, a real Key B provider, and a real anchor registry, for
// tenants acme and beta.
type invitationFixture struct {
	users   InvitationUsers
	roles   role.RoleServicer
	invites *invite.TenantStore
	certs   cert.CertProvider
	anchors *trustanchor.KVRegistry
	audit   *audit.RingBuffer
	svc     *InvitationService
	ids     map[string]map[string]string // tenant -> role name -> id
}

func newInvitationFixture(t *testing.T) *invitationFixture {
	t.Helper()
	resolver := &invitationResolver{stores: map[string]kvstore.KVStore{}}
	u, err := user.NewRoutingService(resolver, dao.NewAtomicIncrementGenerator(1))
	if err != nil {
		t.Fatalf("user routing: %v", err)
	}
	users, ok := u.(InvitationUsers)
	if !ok {
		t.Fatal("the routing user service is not an InvitationUsers")
	}
	roles, err := role.NewRoutingService(resolver)
	if err != nil {
		t.Fatalf("role routing: %v", err)
	}
	f := &invitationFixture{users: users, roles: roles, ids: map[string]map[string]string{}}
	for _, tid := range []string{"acme", "beta"} {
		ctx := tenant.WithTenantID(context.Background(), tid)
		f.ids[tid] = map[string]string{}
		for _, r := range append(role.BuiltInRoles(tid), role.OwnerRole(tid)) {
			created, err := roles.CreateRole(ctx, &role.CreateRoleRequest{ID: r.ID, Name: r.Name, TenantID: r.TenantID, Permissions: r.Permissions})
			if err != nil {
				t.Fatalf("seed role %s/%s: %v", tid, r.Name, err)
			}
			f.ids[tid][created.Name] = created.ID
		}
	}
	f.invites, err = invite.NewTenantStore(resolver)
	if err != nil {
		t.Fatalf("invite store: %v", err)
	}
	f.certs = newCertEnrollmentProvider(t)
	f.anchors = trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())
	f.audit = audit.NewRingBuffer(64)
	f.svc = f.service(time.Hour)
	return f
}

func (f *invitationFixture) service(ttl time.Duration) *InvitationService {
	return NewInvitationService(InvitationDeps{
		Invitations: f.invites,
		Users:       f.users,
		Certs:       f.certs,
		Anchors:     f.anchors,
		TTL:         ttl,
		MaxCertTTL:  24 * time.Hour,
		AuditLogger: f.audit,
		Tenants:     fixtureTenants,
	})
}

// findAuditEvent returns the fixture's last recorded event with the given
// action, or nil.
func (f *invitationFixture) findAuditEvent(action string) *audit.AuditEvent {
	return findEvent(f.audit.Snapshot(), action)
}

// admin is an authenticated tenant Admin of tenantID.
func (f *invitationFixture) admin(tenantID string) context.Context {
	ctx := tenant.WithTenantID(context.Background(), tenantID)
	return auth.WithIdentity(ctx, &auth.Identity{Subject: "admin", TenantID: tenantID, Roles: []string{f.ids[tenantID][role.RoleNameAdmin]}})
}

// invite invites username into acme with the User role.
func (f *invitationFixture) invite(t *testing.T, username string) *Invitation {
	t.Helper()
	inv, err := f.svc.InviteUser(f.admin("acme"), &InviteUserRequest{TenantID: "acme", Username: username, RoleIDs: []string{f.ids["acme"][role.RoleNameUser]}})
	if err != nil {
		t.Fatalf("InviteUser(%s): %v", username, err)
	}
	return inv
}

// issueDirectly creates an invitation for an already-existing account,
// bypassing InviteUser -- the shape a lower-level caller (not exercised by
// any transport today) would use.
func (f *invitationFixture) issueDirectly(t *testing.T, tenantID string, userID uint64) string {
	t.Helper()
	token, _, err := f.invites.Create(context.Background(), strconv.FormatUint(userID, 10), tenantID, time.Hour)
	if err != nil {
		t.Fatalf("issueDirectly: %v", err)
	}
	return token
}

// newOwnCSR is a CSR for a fresh key of the redeemer's own.
func newOwnCSR(t *testing.T) []byte {
	t.Helper()
	_, csrPEM, err := certtest.NewCSR("the subject is ignored")
	if err != nil {
		t.Fatal(err)
	}
	return csrPEM
}

// csrFor is a CSR over key.
func csrFor(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "byo"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

// leafFor is a client certificate the CA signs over key, naming cn in ou.
func (ca *testCA) leafFor(t *testing.T, key *ecdsa.PrivateKey, cn, ou string) *x509.Certificate {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: nextCertEnrollmentSerial(),
		Subject:      pkix.Name{CommonName: cn, OrganizationalUnit: []string{ou}},
		URIs:         enrollmentLeafURIs(t, cn, ou),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(12 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func assertInvalidInvitation(t *testing.T, err error, why string) {
	t.Helper()
	var invalid *InvitationInvalidError
	if !errors.As(err, &invalid) {
		t.Fatalf("%s: err = %v (%T), want *InvitationInvalidError", why, err, err)
	}
}

// assertTargetRefused asserts err is the certTargetGuard refusal
// (*CertTargetRefusedError) certs:issue itself gets for the same target.
func assertTargetRefused(t *testing.T, err error, why string) {
	t.Helper()
	var refused *CertTargetRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("%s: err = %v (%T), want *CertTargetRefusedError", why, err, err)
	}
}

func (f *invitationFixture) account(t *testing.T, tenantID string, id uint64) *user.User {
	t.Helper()
	u, err := f.users.GetUser(tenant.WithTenantID(context.Background(), tenantID), id)
	if err != nil {
		t.Fatalf("GetUser(%d): %v", id, err)
	}
	return u
}

// ---------------------------------------------------------------------------
// InviteUser
// ---------------------------------------------------------------------------

func TestInviteUser_CreatesAPendingAccountAndReturnsTheTokenOnce(t *testing.T) {
	f := newInvitationFixture(t)
	inv := f.invite(t, "alice")

	if inv.Token == "" || !inv.Created || inv.Username != "alice" || inv.TenantID != "acme" {
		t.Fatalf("invitation = %+v", inv)
	}
	if d := time.Until(inv.ExpiresAt); d <= 0 || d > time.Hour {
		t.Fatalf("ExpiresAt is %v from now, want within the 1h TTL", d)
	}
	acct := f.account(t, "acme", inv.UserID)
	if acct.Lifecycle != user.LifecyclePending || len(acct.CertBindings) != 0 {
		t.Fatalf("account = %s with %d bindings, want pending with none", acct.Lifecycle, len(acct.CertBindings))
	}
	// Only the digest is stored: the record carries no plaintext.
	entity, err := f.invites.Lookup(context.Background(), "acme", inv.Token)
	if err != nil || entity.TokenHash != invite.TokenDigest(inv.Token) || strings.Contains(entity.TokenHash, inv.Token) {
		t.Fatalf("stored invitation = %+v, %v", entity, err)
	}
	ev := f.findAuditEvent("user.invite")
	if ev == nil || ev.Actor != "admin" || ev.Resource != "user:"+strconv.FormatUint(inv.UserID, 10) || ev.Attributes["account_created"] != true {
		t.Fatalf("user.invite event = %+v", ev)
	}
	for k, v := range ev.Attributes {
		if s, isString := v.(string); isString && strings.Contains(s, inv.Token) {
			t.Fatalf("user.invite attribute %s carries the token", k)
		}
	}
}

func TestInviteUser_ReinvitesAPendingAccount(t *testing.T) {
	f := newInvitationFixture(t)
	first := f.invite(t, "alice")
	again, err := f.svc.InviteUser(f.admin("acme"), &InviteUserRequest{TenantID: "acme", Username: "alice"})
	if err != nil {
		t.Fatalf("re-invite: %v", err)
	}
	if again.Created || again.UserID != first.UserID || again.Token == first.Token {
		t.Fatalf("re-invite = %+v, want a new token for the same account", again)
	}
	// Re-inviting with account attributes is refused rather than ignored.
	var field *InvitationFieldError
	if _, err := f.svc.InviteUser(f.admin("acme"), &InviteUserRequest{TenantID: "acme", Username: "alice", RoleIDs: []string{f.ids["acme"][role.RoleNameAdmin]}}); !errors.As(err, &field) {
		t.Fatalf("re-invite with roles: err = %v, want *InvitationFieldError", err)
	}
}

func TestInviteUser_RefusesAnAccountThatAlreadyHasACredential(t *testing.T) {
	f := newInvitationFixture(t)
	createTestAccount(t, f.users, "acme", "bob")
	var active *InvitationAccountActiveError
	if _, err := f.svc.InviteUser(f.admin("acme"), &InviteUserRequest{TenantID: "acme", Username: "bob"}); !errors.As(err, &active) {
		t.Fatalf("err = %v, want *InvitationAccountActiveError", err)
	}
}

// An invitation is for ORDINARY accounts, not for the Security Officer or
// the Owner (unless the inviter is one): inviting one would let the inviter
// redeem the token and hold it -- the same rule certs:issue applies via
// certTargetGuard.
func TestInviteUser_RefusesSOAndAnOwnerFromANonOwner(t *testing.T) {
	f := newInvitationFixture(t)
	for _, name := range []string{role.RoleNameSO, role.RoleNameOwner} {
		_, err := f.svc.InviteUser(f.admin("acme"), &InviteUserRequest{TenantID: "acme", Username: "x-" + name, RoleIDs: []string{f.ids["acme"][name]}})
		assertTargetRefused(t, err, name)
		if _, err := f.users.GetUserByUsername(f.admin("acme"), "acme", "x-"+name); err == nil {
			t.Fatalf("%s: a refused invitation created the account", name)
		}
	}
	owner := auth.WithIdentity(tenant.WithTenantID(context.Background(), "acme"), &auth.Identity{Subject: "owner", TenantID: "acme", Roles: []string{f.ids["acme"][role.RoleNameOwner]}})
	if _, err := f.svc.InviteUser(owner, &InviteUserRequest{TenantID: "acme", Username: "co-owner", RoleIDs: []string{f.ids["acme"][role.RoleNameOwner]}}); err != nil {
		t.Fatalf("an Owner inviting an Owner: %v", err)
	}
}

func TestInviteUser_ValidatesTheRequestAndConfiguration(t *testing.T) {
	f := newInvitationFixture(t)
	var field *InvitationFieldError
	for _, req := range []*InviteUserRequest{nil, {TenantID: "acme"}, {Username: "alice"}} {
		if _, err := f.svc.InviteUser(f.admin("acme"), req); !errors.As(err, &field) {
			t.Fatalf("%+v: err = %v, want *InvitationFieldError", req, err)
		}
	}
	var nc *CertEnrollmentNotConfiguredError
	for _, svc := range []*InvitationService{
		NewInvitationService(InvitationDeps{Users: f.users, TTL: time.Hour}),
		NewInvitationService(InvitationDeps{Invitations: f.invites, TTL: time.Hour}),
		f.service(0),
	} {
		if _, err := svc.InviteUser(f.admin("acme"), &InviteUserRequest{TenantID: "acme", Username: "alice"}); !errors.As(err, &nc) {
			t.Fatalf("unconfigured: err = %v, want *CertEnrollmentNotConfiguredError", err)
		}
	}
}

// failingCreateInvitations fails Create, delegating everything else.
type failingCreateInvitations struct {
	InvitationStore
	err error
}

func (f *failingCreateInvitations) Create(context.Context, string, string, time.Duration, ...invite.CreateOption) (string, *invite.Entity, error) {
	return "", nil, f.err
}

// failingDeleteUsers fails DeleteUser, delegating everything else.
type failingDeleteUsers struct {
	InvitationUsers
	err error
}

func (f *failingDeleteUsers) DeleteUser(context.Context, uint64) error { return f.err }

func TestInviteUser_DeletesTheAccountWhenTheInvitationCannotBeIssued(t *testing.T) {
	f := newInvitationFixture(t)
	boom := errors.New("invitation store down")
	svc := NewInvitationService(InvitationDeps{Invitations: &failingCreateInvitations{InvitationStore: f.invites, err: boom}, Users: f.users, TTL: time.Hour})
	if _, err := svc.InviteUser(f.admin("acme"), &InviteUserRequest{TenantID: "acme", Username: "alice"}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the store failure", err)
	}
	var nf *user.UserNotFoundError
	if _, err := f.users.GetUserByUsername(f.admin("acme"), "acme", "alice"); !errors.As(err, &nf) {
		t.Fatalf("the account outlived its failed invitation: %v", err)
	}

	undo := errors.New("delete failed")
	svc = NewInvitationService(InvitationDeps{Invitations: &failingCreateInvitations{InvitationStore: f.invites, err: boom}, Users: &failingDeleteUsers{InvitationUsers: f.users, err: undo}, TTL: time.Hour})
	_, err := svc.InviteUser(f.admin("acme"), &InviteUserRequest{TenantID: "acme", Username: "carol"})
	var stranded *InvitationUndoFailedError
	if !errors.As(err, &stranded) || !errors.Is(err, boom) || stranded.UserID == 0 || !strings.Contains(err.Error(), "delete it by hand") {
		t.Fatalf("err = %v, want *InvitationUndoFailedError naming the account", err)
	}
}

// ---------------------------------------------------------------------------
// RedeemInvitation
// ---------------------------------------------------------------------------

func TestRedeemInvitation_SignsTheRedeemersOwnKeyAndActivatesTheAccount(t *testing.T) {
	f := newInvitationFixture(t)
	inv := f.invite(t, "alice")
	csrPEM := newOwnCSR(t)

	// Unauthenticated: no identity, no tenant on the context.
	got, err := f.svc.RedeemInvitation(context.Background(), &RedeemInvitationRequest{TenantID: "acme", Token: inv.Token, CSRPEM: csrPEM})
	if err != nil {
		t.Fatalf("RedeemInvitation: %v", err)
	}
	if got.UserID != inv.UserID || got.Username != "alice" || got.Serial == "" || len(got.CertPEM) == 0 {
		t.Fatalf("enrolled = %+v", got)
	}
	pub, _ := cert.ParseCSR(csrPEM)
	want, _ := user.PublicKeySPKIThumbprint(pub)
	if got.Thumbprint != want {
		t.Fatal("the certificate is not over the redeemer's own key")
	}
	acct := f.account(t, "acme", inv.UserID)
	if acct.Lifecycle != user.LifecycleActive || !acct.HasCertBinding(want) {
		t.Fatalf("account = %s, bindings %v; want active and bound", acct.Lifecycle, acct.CertBindings)
	}
	issued, err := trustanchor.ParseCertificates(got.CertPEM)
	if err != nil || issued[0].Subject.CommonName != "alice" {
		t.Fatalf("issued certificate = %v, %v; want it to name alice", issued, err)
	}
	if issued[0].NotAfter.After(time.Now().Add(24*time.Hour + time.Minute)) {
		t.Fatalf("NotAfter %v exceeds the 24h maximum", issued[0].NotAfter)
	}
	ev := f.findAuditEvent("cert.enroll")
	if ev == nil || ev.Attributes["via"] != "invitation" || ev.Attributes["invitation_id"] != inv.ID {
		t.Fatalf("cert.enroll = %+v", ev)
	}
}

func TestRedeemInvitation_RefusesAReplay(t *testing.T) {
	f := newInvitationFixture(t)
	inv := f.invite(t, "alice")
	req := &RedeemInvitationRequest{TenantID: "acme", Token: inv.Token, CSRPEM: newOwnCSR(t)}
	if _, err := f.svc.RedeemInvitation(context.Background(), req); err != nil {
		t.Fatalf("first redemption: %v", err)
	}
	req.CSRPEM = newOwnCSR(t)
	_, err := f.svc.RedeemInvitation(context.Background(), req)
	assertInvalidInvitation(t, err, "replay")
	if acct := f.account(t, "acme", inv.UserID); len(acct.CertBindings) != 1 {
		t.Fatalf("the replay bound a second key: %v", acct.CertBindings)
	}
}

func TestRedeemInvitation_ATokenForOneTenantCannotEnrolIntoAnother(t *testing.T) {
	f := newInvitationFixture(t)
	inv := f.invite(t, "alice")
	_, err := f.svc.RedeemInvitation(context.Background(), &RedeemInvitationRequest{TenantID: "beta", Token: inv.Token, CSRPEM: newOwnCSR(t)})
	assertInvalidInvitation(t, err, "tenant A's token redeemed in tenant B")
	// Nothing was spent: it still redeems where it was issued.
	if _, err := f.svc.RedeemInvitation(context.Background(), &RedeemInvitationRequest{TenantID: "acme", Token: inv.Token, CSRPEM: newOwnCSR(t)}); err != nil {
		t.Fatalf("redeeming in its own tenant afterwards: %v", err)
	}
}

func TestRedeemInvitation_RefusesAnExpiredToken(t *testing.T) {
	f := newInvitationFixture(t)
	svc := f.service(time.Nanosecond)
	inv, err := svc.InviteUser(f.admin("acme"), &InviteUserRequest{TenantID: "acme", Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	_, err = svc.RedeemInvitation(context.Background(), &RedeemInvitationRequest{TenantID: "acme", Token: inv.Token, CSRPEM: newOwnCSR(t)})
	assertInvalidInvitation(t, err, "expired")
	if acct := f.account(t, "acme", inv.UserID); acct.Lifecycle != user.LifecyclePending {
		t.Fatalf("an expired token activated the account: %s", acct.Lifecycle)
	}
}

// Two invitations for the same account are two tokens for ONE activation:
// the second, however valid, finds no pending account to give a key to.
func TestRedeemInvitation_RefusesAnotherTokenForAnAccountAlreadyActivated(t *testing.T) {
	f := newInvitationFixture(t)
	first := f.invite(t, "alice")
	second, err := f.svc.InviteUser(f.admin("acme"), &InviteUserRequest{TenantID: "acme", Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RedeemInvitation(context.Background(), &RedeemInvitationRequest{TenantID: "acme", Token: first.Token, CSRPEM: newOwnCSR(t)}); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.RedeemInvitation(context.Background(), &RedeemInvitationRequest{TenantID: "acme", Token: second.Token, CSRPEM: newOwnCSR(t)})
	assertInvalidInvitation(t, err, "second token for an activated account")
}

func TestRedeemInvitation_RefusesAnUnknownToken(t *testing.T) {
	f := newInvitationFixture(t)
	f.invite(t, "alice")
	token, _ := invite.GenerateToken()
	_, err := f.svc.RedeemInvitation(context.Background(), &RedeemInvitationRequest{TenantID: "acme", Token: token, CSRPEM: newOwnCSR(t)})
	assertInvalidInvitation(t, err, "unknown token")
}

// failingLookupInvitations fails Lookup, delegating everything else.
type failingLookupInvitations struct {
	InvitationStore
	err error
}

func (f *failingLookupInvitations) Lookup(context.Context, string, string) (*invite.Entity, error) {
	return nil, f.err
}

// failingRedeemInvitations fails Redeem, delegating everything else.
type failingRedeemInvitations struct {
	InvitationStore
	err error
}

func (f *failingRedeemInvitations) Redeem(context.Context, string, string) (*invite.Entity, error) {
	return nil, f.err
}

// withInvitations builds an InvitationService identical to f.service() except
// for the InvitationStore, so a test can swap in one that fails exactly the
// call under test while everything else (users, certs, anchors, audit,
// tenants) stays real.
func (f *invitationFixture) withInvitations(invites InvitationStore) *InvitationService {
	return NewInvitationService(InvitationDeps{
		Invitations: invites,
		Users:       f.users,
		Certs:       f.certs,
		Anchors:     f.anchors,
		TTL:         time.Hour,
		MaxCertTTL:  24 * time.Hour,
		AuditLogger: f.audit,
		Tenants:     fixtureTenants,
	})
}

// TestRedeemInvitation_AStorageFaultOnLookupIsNotReportedAsInvalid asserts the
// Option-2 ruling (docs/identity-rbac/engineering.md's invitation section,
// formerly docs/invite-notfound-ambiguity.md): a genuine storage or transport
// fault behind Lookup must reach the caller as itself, not folded into the
// opaque *InvitationInvalidError a bad, expired or used token gets. Collapsing
// it would silently read a real outage as "every presented token today is
// forged" for as long as the outage lasts.
func TestRedeemInvitation_AStorageFaultOnLookupIsNotReportedAsInvalid(t *testing.T) {
	f := newInvitationFixture(t)
	inv := f.invite(t, "alice")

	boom := &invite.StoreError{Op: "lookup invite", Cause: errors.New("storage unavailable")}
	svc := f.withInvitations(&failingLookupInvitations{InvitationStore: f.invites, err: boom})

	_, err := svc.RedeemInvitation(context.Background(), &RedeemInvitationRequest{TenantID: "acme", Token: inv.Token, CSRPEM: newOwnCSR(t)})
	var invalid *InvitationInvalidError
	if errors.As(err, &invalid) {
		t.Fatalf("a storage fault must not be reported as *InvitationInvalidError: %v", err)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the storage fault itself", err)
	}
}

// TestRedeemInvitation_AStorageFaultOnRedeemIsNotReportedAsInvalid covers the
// consuming write's half of the same ruling.
func TestRedeemInvitation_AStorageFaultOnRedeemIsNotReportedAsInvalid(t *testing.T) {
	f := newInvitationFixture(t)
	inv := f.invite(t, "alice")

	boom := &invite.StoreError{Op: "claim invite", Cause: errors.New("storage unavailable")}
	svc := f.withInvitations(&failingRedeemInvitations{InvitationStore: f.invites, err: boom})

	_, err := svc.RedeemInvitation(context.Background(), &RedeemInvitationRequest{TenantID: "acme", Token: inv.Token, CSRPEM: newOwnCSR(t)})
	var invalid *InvitationInvalidError
	if errors.As(err, &invalid) {
		t.Fatalf("a storage fault must not be reported as *InvitationInvalidError: %v", err)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the storage fault itself", err)
	}
}

// The inviter holds the token. Redeeming it with the inviter's OWN key -- the
// one their certificate is bound to -- is refused before the token is spent,
// so the inviter cannot make their credential the invited user's and the
// invited user can still redeem.
func TestRedeemInvitation_TheInviterCannotRedeemWithTheirOwnKey(t *testing.T) {
	f := newInvitationFixture(t)
	adminKey, adminKeyPEM, err := certtest.NewKey()
	if err != nil || adminKeyPEM == nil {
		t.Fatal(err)
	}
	adminAcct := createTestAccount(t, f.users, "acme", "admin")
	adminThumb, _ := user.PublicKeySPKIThumbprint(&adminKey.PublicKey)
	if _, err := f.users.AddCertBinding(f.admin("acme"), adminAcct.ID, adminThumb, 0); err != nil {
		t.Fatal(err)
	}
	inv := f.invite(t, "alice")

	_, err = f.svc.RedeemInvitation(context.Background(), &RedeemInvitationRequest{TenantID: "acme", Token: inv.Token, CSRPEM: csrFor(t, adminKey)})
	var inUse *InvitationKeyInUseError
	if !errors.As(err, &inUse) {
		t.Fatalf("err = %v, want *InvitationKeyInUseError", err)
	}
	if acct := f.account(t, "acme", inv.UserID); acct.Lifecycle != user.LifecyclePending {
		t.Fatalf("the inviter's key activated the account: %s", acct.Lifecycle)
	}
	// The admin's key still authenticates as the admin and nobody else.
	if who, err := f.users.UserByCertThumbprint(f.admin("acme"), adminThumb); err != nil || who.ID != adminAcct.ID {
		t.Fatalf("the admin's key resolves to %v, %v", who, err)
	}
	if _, err := f.svc.RedeemInvitation(context.Background(), &RedeemInvitationRequest{TenantID: "acme", Token: inv.Token, CSRPEM: newOwnCSR(t)}); err != nil {
		t.Fatalf("the invited user's own redemption afterwards: %v", err)
	}
}

func TestRedeemInvitation_BindsABringYourOwnCertificateOverTheRedeemersKey(t *testing.T) {
	f := newInvitationFixture(t)
	root := newTestRoot(t, "acme corporate CA")
	if _, err := f.anchors.Add(context.Background(), "acme", pemOf(root.cert)); err != nil {
		t.Fatal(err)
	}
	inv := f.invite(t, "alice")
	key := newTestCAKey(t)
	leaf := root.leafFor(t, key, "alice", "acme")

	// A certificate without proof of its key is refused.
	_, err := f.svc.RedeemInvitation(context.Background(), &RedeemInvitationRequest{TenantID: "acme", Token: inv.Token, CSRPEM: newOwnCSR(t), CertPEM: pemOf(leaf)})
	var field *InvitationFieldError
	if !errors.As(err, &field) || field.Field != "csr_pem" {
		t.Fatalf("CSR over another key: err = %v, want *InvitationFieldError on csr_pem", err)
	}

	got, err := f.svc.RedeemInvitation(context.Background(), &RedeemInvitationRequest{TenantID: "acme", Token: inv.Token, CSRPEM: csrFor(t, key), CertPEM: pemOf(leaf)})
	if err != nil {
		t.Fatalf("RedeemInvitation(BYO): %v", err)
	}
	if got.Serial != "" || got.Thumbprint != user.ComputeSPKIThumbprint(leaf) {
		t.Fatalf("enrolled = %+v", got)
	}
	acct := f.account(t, "acme", inv.UserID)
	if acct.Lifecycle != user.LifecycleActive || !acct.MatchesCert(leaf) {
		t.Fatalf("account = %s; want active and matching the certificate", acct.Lifecycle)
	}
	ev := f.findAuditEvent("cert.enroll")
	if ev == nil || ev.Attributes["via"] != "invitation" {
		t.Fatalf("cert.enroll = %+v", ev)
	}
}

func TestRedeemInvitation_RefusesABringYourOwnCertificateThatNamesSomeoneElse(t *testing.T) {
	f := newInvitationFixture(t)
	root := newTestRoot(t, "acme corporate CA")
	if _, err := f.anchors.Add(context.Background(), "acme", pemOf(root.cert)); err != nil {
		t.Fatal(err)
	}
	inv := f.invite(t, "alice")
	key := newTestCAKey(t)
	leaf := root.leafFor(t, key, "mallory", "acme")
	if _, err := f.svc.RedeemInvitation(context.Background(), &RedeemInvitationRequest{TenantID: "acme", Token: inv.Token, CSRPEM: csrFor(t, key), CertPEM: pemOf(leaf)}); err == nil {
		t.Fatal("a certificate naming another account was bound")
	}
	if acct := f.account(t, "acme", inv.UserID); acct.Lifecycle != user.LifecyclePending {
		t.Fatalf("lifecycle = %s, want pending", acct.Lifecycle)
	}
}

func TestRedeemInvitation_ValidatesTheRequest(t *testing.T) {
	f := newInvitationFixture(t)
	inv := f.invite(t, "alice")
	var field *InvitationFieldError
	for _, req := range []*RedeemInvitationRequest{
		nil,
		{Token: inv.Token, CSRPEM: newOwnCSR(t)},
		{TenantID: "acme", CSRPEM: newOwnCSR(t)},
		{TenantID: "acme", Token: inv.Token},
	} {
		if _, err := f.svc.RedeemInvitation(context.Background(), req); !errors.As(err, &field) {
			t.Fatalf("%+v: err = %v, want *InvitationFieldError", req, err)
		}
	}
	var badCSR *cert.InvalidCSRError
	if _, err := f.svc.RedeemInvitation(context.Background(), &RedeemInvitationRequest{TenantID: "acme", Token: inv.Token, CSRPEM: []byte("junk")}); !errors.As(err, &badCSR) {
		t.Fatalf("junk CSR: err = %v, want *cert.InvalidCSRError", err)
	}
	var nc *CertEnrollmentNotConfiguredError
	noCerts := NewInvitationService(InvitationDeps{Invitations: f.invites, Users: f.users, TTL: time.Hour, Tenants: fixtureTenants})
	if _, err := noCerts.RedeemInvitation(context.Background(), &RedeemInvitationRequest{TenantID: "acme", Token: inv.Token, CSRPEM: newOwnCSR(t)}); !errors.As(err, &nc) {
		t.Fatalf("no issuer: err = %v", err)
	}
	if _, err := noCerts.RedeemInvitation(context.Background(), &RedeemInvitationRequest{TenantID: "acme", Token: inv.Token, CSRPEM: newOwnCSR(t), CertPEM: []byte("x")}); !errors.As(err, &nc) {
		t.Fatalf("no anchors: err = %v", err)
	}
	if _, err := NewInvitationService(InvitationDeps{}).RedeemInvitation(context.Background(), nil); !errors.As(err, &nc) {
		t.Fatalf("unconfigured: err = %v", err)
	}
	// None of that spent the token.
	if _, err := f.svc.RedeemInvitation(context.Background(), &RedeemInvitationRequest{TenantID: "acme", Token: inv.Token, CSRPEM: newOwnCSR(t)}); err != nil {
		t.Fatalf("redeeming after refused requests: %v", err)
	}
}

// failingActivateUsers fails ActivateWithCertBinding, delegating the rest.
type failingActivateUsers struct {
	InvitationUsers
	err error
}

func (f *failingActivateUsers) ActivateWithCertBinding(context.Context, uint64, string, int64) (*user.User, error) {
	return nil, f.err
}

func TestRedeemInvitation_RevokesTheCertificateWhenTheAccountCannotBeActivated(t *testing.T) {
	f := newInvitationFixture(t)
	inv := f.invite(t, "alice")
	boom := errors.New("activation lost")
	svc := NewInvitationService(InvitationDeps{Invitations: f.invites, Users: &failingActivateUsers{InvitationUsers: f.users, err: boom}, Certs: f.certs, TTL: time.Hour, Tenants: fixtureTenants})
	_, err := svc.RedeemInvitation(context.Background(), &RedeemInvitationRequest{TenantID: "acme", Token: inv.Token, CSRPEM: newOwnCSR(t)})
	var failed *CertBindingFailedError
	if !errors.As(err, &failed) || failed.Serial == "" || failed.RevokeErr != nil || !errors.Is(err, boom) {
		t.Fatalf("err = %v, want *CertBindingFailedError with a revoked serial", err)
	}
	revoked, rerr := f.certs.IsRevoked(context.Background(), failed.Serial)
	if rerr != nil || !revoked {
		t.Fatalf("serial %s revoked = %v, %v; want revoked", failed.Serial, revoked, rerr)
	}
	// The token was spent: a redemption that issued a certificate cannot be
	// repeated, even though the certificate is gone.
	_, err = f.svc.RedeemInvitation(context.Background(), &RedeemInvitationRequest{TenantID: "acme", Token: inv.Token, CSRPEM: newOwnCSR(t)})
	assertInvalidInvitation(t, err, "after a failed activation")

	// When the revocation fails too, the error says so.
	inv2 := f.invite(t, "bob")
	svc = NewInvitationService(InvitationDeps{Invitations: f.invites, Users: &failingActivateUsers{InvitationUsers: f.users, err: boom}, Certs: &failingRevokeCertProvider{CertProvider: f.certs, err: errors.New("revoke down")}, TTL: time.Hour, Tenants: fixtureTenants})
	_, err = svc.RedeemInvitation(context.Background(), &RedeemInvitationRequest{TenantID: "acme", Token: inv2.Token, CSRPEM: newOwnCSR(t)})
	if !errors.As(err, &failed) || failed.RevokeErr == nil {
		t.Fatalf("err = %v, want the revocation failure reported", err)
	}
}

func TestRedeemInvitation_RefusesAnInvitationForACeremonyRole(t *testing.T) {
	f := newInvitationFixture(t)
	// An SO account made pending by other means, with an invitation issued
	// for it directly: the redemption still refuses it.
	so, err := f.users.CreateUser(f.admin("acme"), &user.CreateUserRequest{Username: "so", TenantID: "acme", WithoutEmailLoginKey: true, Pending: true, RoleIDs: []string{f.ids["acme"][role.RoleNameSO]}})
	if err != nil {
		t.Fatal(err)
	}
	token := f.issueDirectly(t, "acme", so.ID)
	// The refusal is the opaque one: the redeemer is unauthenticated, and a
	// specific answer would name the account and its role.
	_, err = f.svc.RedeemInvitation(context.Background(), &RedeemInvitationRequest{TenantID: "acme", Token: token, CSRPEM: newOwnCSR(t)})
	assertInvalidInvitation(t, err, "SO redemption")
	if strings.Contains(err.Error(), "so") && strings.Contains(err.Error(), "role") {
		t.Fatalf("the refusal names the account or its role: %v", err)
	}
	// And the account stays pending: nothing was bound.
	got, gerr := f.users.GetUser(f.admin("acme"), so.ID)
	if gerr != nil || got.Lifecycle != user.LifecyclePending || len(got.CertBindings) != 0 {
		t.Fatalf("SO account after the refusal = %+v, %v", got, gerr)
	}
}

func TestInvitationErrors_Messages(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&InvitationFieldError{Field: "token", Reason: "is required"}, "field token is required"},
		{&InvitationInvalidError{}, "invalid, expired or already used"},
		{&InvitationKeyInUseError{}, "already bound"},
		{&InvitationAccountActiveError{Username: "bob"}, "account bob is not awaiting"},
	} {
		if !strings.Contains(tc.err.Error(), tc.want) {
			t.Errorf("%T.Error() = %q, want it to contain %q", tc.err, tc.err.Error(), tc.want)
		}
	}
}

func TestRedeemSourceKey(t *testing.T) {
	for host, want := range map[string]string{
		"192.0.2.7":                "192.0.2.7",
		"2001:db8::1":              "2001:db8::/64",
		"2001:db8::ffff:ffff:ffff": "2001:db8::/64",
		"::ffff:192.0.2.7":         "192.0.2.7",
		"not-an-ip":                "local",
		"":                         "local",
	} {
		if got := redeemSourceKey(host); got != want {
			t.Errorf("redeemSourceKey(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestInvitationService_RedeemThrottlesPerSourceAndPerTenant(t *testing.T) {
	f := newInvitationFixture(t)
	limiter := ratelimit.NewTokenBucketLimiter()
	defer limiter.Close()
	svc := NewInvitationService(InvitationDeps{
		Invitations:     f.invites,
		Users:           f.users,
		Certs:           f.certs,
		TTL:             time.Hour,
		Tenants:         fixtureTenants,
		RedeemLimiter:   limiter,
		RedeemPerSource: ratelimit.Policy{RatePerSecond: 0.001, Burst: 1},
		RedeemPerTenant: ratelimit.Policy{RatePerSecond: 1000, Burst: 1000},
	})
	inv := f.invite(t, "alice")
	ctx := auth.WithRemoteAddr(context.Background(), "203.0.113.5:4242")

	if _, err := svc.RedeemInvitation(ctx, &RedeemInvitationRequest{TenantID: "acme", Token: inv.Token, CSRPEM: newOwnCSR(t)}); err != nil {
		t.Fatalf("first redemption: %v", err)
	}
	// The source's burst of 1 is spent; a second redemption from the same
	// source is throttled before the (already-redeemed) token is even
	// looked up, and gets the same opaque answer as a bad token.
	again := f.invite(t, "bob")
	_, err := svc.RedeemInvitation(ctx, &RedeemInvitationRequest{TenantID: "acme", Token: again.Token, CSRPEM: newOwnCSR(t)})
	assertInvalidInvitation(t, err, "throttled source")

	// A per-tenant budget of zero throttles every source immediately.
	tenantThrottled := NewInvitationService(InvitationDeps{
		Invitations:     f.invites,
		Users:           f.users,
		Certs:           f.certs,
		TTL:             time.Hour,
		Tenants:         fixtureTenants,
		RedeemLimiter:   limiter,
		RedeemPerSource: ratelimit.Policy{RatePerSecond: 1000, Burst: 1000},
		RedeemPerTenant: ratelimit.Policy{RatePerSecond: 0.001, Burst: 1},
	})
	fromAnotherSource := auth.WithRemoteAddr(context.Background(), "198.51.100.9:1")
	if _, err := tenantThrottled.RedeemInvitation(fromAnotherSource, &RedeemInvitationRequest{TenantID: "acme", Token: again.Token, CSRPEM: newOwnCSR(t)}); err != nil {
		t.Fatalf("first tenant-bucket redemption: %v", err)
	}
	third := f.invite(t, "carol")
	_, err = tenantThrottled.RedeemInvitation(fromAnotherSource, &RedeemInvitationRequest{TenantID: "acme", Token: third.Token, CSRPEM: newOwnCSR(t)})
	assertInvalidInvitation(t, err, "throttled tenant")
}

// pinOf is the SPKI thumbprint of a CSR's key: what a user sends their
// inviter to pin the invitation.
func pinOf(t *testing.T, csrPEM []byte) string {
	t.Helper()
	pub, err := cert.ParseCSR(csrPEM)
	if err != nil {
		t.Fatal(err)
	}
	tp, err := user.PublicKeySPKIThumbprint(pub)
	if err != nil {
		t.Fatal(err)
	}
	return tp
}

func TestRedeemInvitation_APinnedInvitationEnrolsOnlyThePinnedKey(t *testing.T) {
	f := newInvitationFixture(t)
	bobCSR := newOwnCSR(t)
	inv, err := f.svc.InviteUser(f.admin("acme"), &InviteUserRequest{TenantID: "acme", Username: "bob", KeyPinFingerprint: pinOf(t, bobCSR)})
	if err != nil {
		t.Fatalf("InviteUser: %v", err)
	}
	if inv.KeyPinFingerprint != pinOf(t, bobCSR) {
		t.Fatalf("invitation pin = %q", inv.KeyPinFingerprint)
	}
	if ev := f.findAuditEvent("user.invite"); ev == nil || ev.Attributes["key_pinned"] != true {
		t.Fatalf("user.invite key_pinned event = %+v", ev)
	}

	// A thief holding the token but not bob's key is refused opaquely ...
	_, err = f.svc.RedeemInvitation(context.Background(), &RedeemInvitationRequest{TenantID: "acme", Token: inv.Token, CSRPEM: newOwnCSR(t)})
	assertInvalidInvitation(t, err, "a different key against a pinned invitation")
	// ... and the token is NOT spent.
	if _, err := f.invites.Lookup(context.Background(), "acme", inv.Token); err != nil {
		t.Fatalf("the refused redemption spent the token: %v", err)
	}
	if acct := f.account(t, "acme", inv.UserID); acct.Lifecycle != user.LifecyclePending {
		t.Fatalf("lifecycle = %s, want pending", acct.Lifecycle)
	}

	got, err := f.svc.RedeemInvitation(context.Background(), &RedeemInvitationRequest{TenantID: "acme", Token: inv.Token, CSRPEM: bobCSR})
	if err != nil {
		t.Fatalf("redeeming with the pinned key: %v", err)
	}
	if got.Thumbprint != pinOf(t, bobCSR) {
		t.Fatalf("bound %s, want the pinned key", got.Thumbprint)
	}
}

func TestInviteUser_KeyPinValidationAndTheRequirePinFlag(t *testing.T) {
	f := newInvitationFixture(t)
	var field *InvitationFieldError
	if _, err := f.svc.InviteUser(f.admin("acme"), &InviteUserRequest{TenantID: "acme", Username: "bob", KeyPinFingerprint: "not-a-thumbprint"}); !errors.As(err, &field) || field.Field != "key_pin_fingerprint" {
		t.Fatalf("malformed pin: err = %v", err)
	}

	strict := NewInvitationService(InvitationDeps{Invitations: f.invites, Users: f.users, Certs: f.certs, TTL: time.Hour, RequireKeyPin: true})
	if _, err := strict.InviteUser(f.admin("acme"), &InviteUserRequest{TenantID: "acme", Username: "carol"}); !errors.As(err, &field) || field.Field != "key_pin_fingerprint" {
		t.Fatalf("unpinned invite under require-pin: err = %v", err)
	}
	if _, err := f.users.GetUserByUsername(f.admin("acme"), "acme", "carol"); err == nil {
		t.Fatal("a refused unpinned invite created the account")
	}
	pinned, err := strict.InviteUser(f.admin("acme"), &InviteUserRequest{TenantID: "acme", Username: "carol", KeyPinFingerprint: pinOf(t, newOwnCSR(t))})
	if err != nil || pinned.KeyPinFingerprint == "" {
		t.Fatalf("pinned invite under require-pin = %+v, %v", pinned, err)
	}
}
