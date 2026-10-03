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

package middleware

import (
	"context"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// First boot: the two postures a bootstrapped admin can be left in
//
// These run the REAL firstboot.Initialize over the REAL tenant, role and user
// services on a production in-memory KVStore, and then authenticate the admin
// it produced through the REAL mTLS authenticator. The certificate-binding
// gate is a login gate, so "does first boot still work" can only be answered
// by logging in as what first boot created.
// ---------------------------------------------------------------------------

// firstBootServices builds the real tenant/role/user services first boot runs
// against and returns them plus the Dependencies common to both postures.
func firstBootServices(t *testing.T) (user.UserServicer, firstboot.Dependencies) {
	t.Helper()
	kv := kvstore.NewMemoryKVStore()
	tenants, err := tenant.NewKVTenantService(kv, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	roles, err := role.NewStore(kv)
	if err != nil {
		t.Fatalf("role.NewStore: %v", err)
	}
	users := newRealUserStore(t)
	return users, firstboot.Dependencies{
		Tenants:      tenants,
		Users:        users,
		Roles:        roles,
		RoleResolver: roles,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// pemCert wraps a minted DER certificate as the PEM bundle a CertProvider
// returns.
func pemCert(ck certKey) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ck.cert.Raw})
}

// firstBootCA hands first boot an already-minted admin certificate, standing
// in for the CA a composing binary wires (qrdb's own pkg/cert provider, or
// go-xkms's CA adapter).
type firstBootCA struct{ certPEM []byte }

func (c *firstBootCA) GenerateAdminCert(_ context.Context, _, _ string) ([]byte, []byte, error) {
	return c.certPEM, []byte("KEY"), nil
}

// firstBootBinder is the CertBinder seam wired to the REAL user store, which
// is what a composing binary's adapter does: it calls the canonical
// AddCertBinding enrolment verb.
type firstBootBinder struct{ users user.UserServicer }

func (b *firstBootBinder) BindAdminCert(ctx context.Context, _ string, userID uint64, thumbprint string) error {
	_, err := b.users.AddCertBinding(ctx, userID, thumbprint, 0)
	return err
}

// TestFirstBoot_UnboundAdminStillAuthenticates_UnderEnrolled is the
// no-migration assertion for a composer that has explicitly opted OUT to
// "enrolled" (go-qrdb#548): it is the measurement the empty-set admission
// rests on, for a composer that cannot yet wire a CertBinder.
//
// Under the DEFAULT "required" posture this exact configuration -- no CA, no
// CertBinder wired -- is refused at firstboot.Initialize itself
// (*firstboot.AdminCertBindingRequiredError, AdminBindingNoBinder), before an
// unbound admin is ever created; that is the fix go-qrdb#548 makes. This test
// covers the remaining, explicit "enrolled" escape hatch for a composer that
// has not finished wiring a CertBinder yet, not the default behavior.
func TestFirstBoot_UnboundAdminStillAuthenticates_UnderEnrolled(t *testing.T) {
	users, deps := firstBootServices(t)
	deps.MTLSBindingPolicy = auth.MTLSBindingEnrolled
	if _, err := firstboot.Initialize(context.Background(), deps); err != nil {
		t.Fatalf("firstboot.Initialize: %v", err)
	}

	admin, err := users.GetUserByUsername(context.Background(), tenantid.System, "admin")
	if err != nil {
		t.Fatalf("GetUserByUsername(admin): %v", err)
	}
	if len(admin.CertBindings) != 0 {
		t.Fatalf("bootstrap admin has %d bindings; the premise of the empty-set admission is that it has none",
			len(admin.CertBindings))
	}

	m := mtlsMiddleware(users, WithMTLSBindingPolicy(auth.MTLSBindingEnrolled))
	_, identity, err := m.authenticate(mtlsContext(mintCert(t, "admin", tenantid.System, nil, "")))
	if err != nil {
		t.Fatalf("bootstrapped admin refused: %v", err)
	}
	if identity.Subject != "admin" || len(identity.Roles) == 0 {
		t.Fatalf("identity = %+v, want the admin with its bootstrapped roles", identity)
	}
}

// TestFirstBoot_NoBinderRefusedUnderRequired is the go-qrdb#548 fix itself:
// the DEFAULT posture refuses to create an admin it cannot bind, rather than
// creating one with an empty binding set and admitting any certificate that
// names it.
func TestFirstBoot_NoBinderRefusedUnderRequired(t *testing.T) {
	_, deps := firstBootServices(t)
	_, err := firstboot.Initialize(context.Background(), deps)
	var required *firstboot.AdminCertBindingRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("firstboot.Initialize with no CertBinder under the default policy = %v, want *firstboot.AdminCertBindingRequiredError", err)
	}
	if required.Reason != firstboot.AdminBindingNoBinder {
		t.Fatalf("Reason = %q, want %q", required.Reason, firstboot.AdminBindingNoBinder)
	}
}

// TestFirstBoot_BoundAdminAdmitsItsCertAndRefusesAnother is the other posture:
// a composing binary that wires a CA and a CertBinder gets an admin whose
// certificate is enrolled at first boot, and that admin is closed from the
// first request. No enrolment step, no window.
func TestFirstBoot_BoundAdminAdmitsItsCertAndRefusesAnother(t *testing.T) {
	users, deps := firstBootServices(t)
	adminCert := mintCert(t, "admin", tenantid.System, nil, "")
	deps.CA = &firstBootCA{certPEM: pemCert(adminCert)}
	deps.Binder = &firstBootBinder{users: users}

	result, err := firstboot.Initialize(context.Background(), deps)
	if err != nil {
		t.Fatalf("firstboot.Initialize: %v", err)
	}
	if result.AdminCertThumbprint != user.ComputeSPKIThumbprint(adminCert.cert) {
		t.Fatalf("first boot bound %q, want %q",
			result.AdminCertThumbprint, user.ComputeSPKIThumbprint(adminCert.cert))
	}

	m := mtlsMiddleware(users)
	if _, _, err := m.authenticate(mtlsContext(adminCert)); err != nil {
		t.Fatalf("the certificate first boot enrolled was refused: %v", err)
	}

	// Same CN, same OU, a different key: the cross-tenant minting path,
	// contained at the admin account itself.
	forged := mintCert(t, "admin", tenantid.System, nil, "")
	if _, _, err := m.authenticate(mtlsContext(forged)); err == nil {
		t.Fatal("a certificate naming the enrolled admin authenticated as the admin")
	}
}

// TestFirstBoot_RecoveryIsEnrollingTheNewKey is the stated recovery path for
// the one lockout this gate can cause: an enrolled operator whose key changes.
//
// Renewal keeps the key and so keeps the thumbprint, so only an actual key
// change can lock anybody out — and the recovery is additive. AddCertBinding
// appends, it does not replace, so the new key is enrolled while the old
// binding is still the one authenticating, and both then work. An operator who
// enrols before rotating is never locked out; an operator who has already lost
// the only enrolled key recovers by restarting the node with authentication
// disabled, which is the standing break-glass for total credential loss and is
// unchanged by this gate.
func TestFirstBoot_RecoveryIsEnrollingTheNewKey(t *testing.T) {
	users, deps := firstBootServices(t)
	oldCert := mintCert(t, "admin", tenantid.System, nil, "")
	deps.CA = &firstBootCA{certPEM: pemCert(oldCert)}
	deps.Binder = &firstBootBinder{users: users}
	if _, err := firstboot.Initialize(context.Background(), deps); err != nil {
		t.Fatalf("firstboot.Initialize: %v", err)
	}

	admin, err := users.GetUserByUsername(context.Background(), tenantid.System, "admin")
	if err != nil {
		t.Fatalf("GetUserByUsername(admin): %v", err)
	}

	newKeyCert := mintCert(t, "admin", tenantid.System, nil, "")
	m := mtlsMiddleware(users)
	if _, _, err := m.authenticate(mtlsContext(newKeyCert)); err == nil {
		t.Fatal("the new key authenticated before it was enrolled")
	}

	// The recovery: enrol the new key while still holding the old one.
	enroll(t, users, admin.ID, newKeyCert)

	if _, _, err := m.authenticate(mtlsContext(newKeyCert)); err != nil {
		t.Fatalf("the newly enrolled key was refused: %v", err)
	}
	if _, _, err := m.authenticate(mtlsContext(oldCert)); err != nil {
		t.Fatalf("enrolment is additive, so the old key must still work: %v", err)
	}
}
