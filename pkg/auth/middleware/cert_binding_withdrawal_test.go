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

// cert_binding_withdrawal_test.go asserts what WITHDRAWING a binding does to
// authentication. It runs the REAL auth.MTLSAuthenticator over the REAL
// kvstore-backed user store for the reason stated in cert_binding_test.go: the
// admission predicate lives on user.User, so a user-store double asked "is
// this key still bound" would be answering about itself.

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// withdraw removes ck's binding through the canonical withdrawal verb — the
// same call the transports expose.
func withdraw(t *testing.T, svc user.UserServicer, userID uint64, ck certKey) {
	t.Helper()
	if _, err := svc.RemoveCertBinding(context.Background(), userID,
		user.ComputeSPKIThumbprint(ck.cert)); err != nil {
		t.Fatalf("RemoveCertBinding: %v", err)
	}
}

// ---------------------------------------------------------------------------
// The operation the issue exists for
// ---------------------------------------------------------------------------

// TestAuthenticate_AWithdrawnBindingNoLongerAuthenticates is the whole point
// of the verb: an operator who believes a key is compromised withdraws its
// binding, and the certificate issued for that key stops being this account's
// credential.
//
// Revocation cannot do this. Revocation refuses a CERTIFICATE, while the
// binding is over the SubjectPublicKeyInfo — so a re-issue for the same key
// authenticates again, and a renewal for the same key is a supported,
// documented operation. Withdrawal is the only thing that removes the key.
//
// The account keeps a SECOND enrolled key throughout, so the refusal is the
// withdrawal and not the account falling back to the empty-set admission.
func TestAuthenticate_AWithdrawnBindingNoLongerAuthenticates(t *testing.T) {
	svc := newRealUserStore(t)
	alice := createUser(t, svc, "acme", "alice", []string{"admin"})

	compromised := mintCert(t, "alice", "acme", nil, "")
	retained := mintCert(t, "alice", "acme", nil, "")
	enroll(t, svc, alice.ID, compromised)
	enroll(t, svc, alice.ID, retained)

	m := mtlsMiddleware(svc)

	// Control: before the withdrawal the compromised key authenticates.
	if _, _, err := m.authenticate(mtlsContext(compromised)); err != nil {
		t.Fatalf("the enrolled certificate was refused before the withdrawal: %v", err)
	}

	withdraw(t, svc, alice.ID, compromised)

	_, identity, err := m.authenticate(mtlsContext(compromised))
	if err == nil {
		t.Fatalf("the withdrawn key still authenticated as %+v", identity)
	}
	var refused *CertBindingNotPermittedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %T(%v), want *CertBindingNotPermittedError", err, err)
	}
	if refused.Username != "alice" || refused.TenantID != "acme" {
		t.Fatalf("error names %s/%s, want acme/alice", refused.TenantID, refused.Username)
	}

	// The retained key is untouched — a withdrawal is not a lockout.
	if _, _, err := m.authenticate(mtlsContext(retained)); err != nil {
		t.Fatalf("the retained binding stopped authenticating: %v", err)
	}
}

// TestAuthenticate_AReissueForTheWithdrawnKeyIsAlsoRefused is the property
// that makes withdrawal the right remediation rather than revocation. A fresh
// certificate minted for the SAME key — the operation a renewal performs, and
// the one an attacker holding the key would ask the CA for — carries the same
// SPKI thumbprint. Once the binding is withdrawn, that no longer buys
// anything.
func TestAuthenticate_AReissueForTheWithdrawnKeyIsAlsoRefused(t *testing.T) {
	svc := newRealUserStore(t)
	bob := createUser(t, svc, "acme", "bob", nil)

	compromised := mintCert(t, "bob", "acme", nil, "")
	retained := mintCert(t, "bob", "acme", nil, "")
	enroll(t, svc, bob.ID, compromised)
	enroll(t, svc, bob.ID, retained)
	withdraw(t, svc, bob.ID, compromised)

	// A NEW certificate for the withdrawn key: same SPKI, different serial.
	reissued := mintCert(t, "bob", "acme", compromised.key, "")
	if reissued.cert.Equal(compromised.cert) {
		t.Fatal("fixture did not mint a distinct certificate")
	}

	m := mtlsMiddleware(svc)
	if _, identity, err := m.authenticate(mtlsContext(reissued)); err == nil {
		t.Fatalf("a re-issue for the withdrawn key authenticated as %+v", identity)
	}
}

// ---------------------------------------------------------------------------
// The last binding — a deliberate assertion, not a discovery
// ---------------------------------------------------------------------------

// TestAuthenticate_WithdrawingTheLastBindingRestoresTheEmptySetAdmission pins
// the consequence an operator must be told about before they run the verb.
//
// An account with NO bindings states no restriction: it authenticates on the
// certificate's CN and OU alone, exactly as every account did before enrolment
// existed. So withdrawing an account's LAST binding does not lock it down — it
// WIDENS it, back to admitting any certificate from the trusted CA that names
// it. That is the correct behaviour (the alternative locks out every account
// nobody has enrolled) and it is a real posture change, so it is asserted here
// rather than left to be found in production.
//
// The remediation for a compromised key on a single-binding account is
// therefore: enrol the replacement FIRST, then withdraw the compromised one.
func TestAuthenticate_WithdrawingTheLastBindingRestoresTheEmptySetAdmission(t *testing.T) {
	svc := newRealUserStore(t)
	carol := createUser(t, svc, "acme", "carol", []string{"reader"})

	enrolled := mintCert(t, "carol", "acme", nil, "")
	enroll(t, svc, carol.ID, enrolled)

	// A certificate naming carol that she never enrolled.
	unbound := mintCert(t, "carol", "acme", nil, "")

	// This test specifically covers the "enrolled" opt-out (go-qrdb#548): the
	// empty-set-admits widening it asserts does not occur under "required".
	m := mtlsMiddleware(svc, WithMTLSBindingPolicy(auth.MTLSBindingEnrolled))

	// While she holds a binding, the unbound certificate is refused.
	if _, _, err := m.authenticate(mtlsContext(unbound)); err == nil {
		t.Fatal("an enrolled account admitted an unbound certificate")
	}

	withdraw(t, svc, carol.ID, enrolled)

	// With the last binding gone the account is back to CN/OU admission.
	_, identity, err := m.authenticate(mtlsContext(unbound))
	if err != nil {
		t.Fatalf("after the last withdrawal the account did not return to CN/OU admission: %v", err)
	}
	if identity.UserID == 0 || identity.Username != "carol" || identity.TenantID != "acme" {
		t.Fatalf("identity = %s/%s, want acme/carol", identity.TenantID, identity.Subject)
	}

	// And the record really does carry no bindings — the widening is the empty
	// set, not a binding the predicate happened to match.
	reread, err := svc.GetUser(context.Background(), carol.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if len(reread.CertBindings) != 0 {
		t.Fatalf("bindings = %+v, want none", reread.CertBindings)
	}
}

// TestAuthenticate_WithdrawalIsNotRevocationOfTheOtherDirection is the
// converse control: an EXPIRED binding leaves the account in the enforcing
// posture (the set is non-empty), while a WITHDRAWN one removes it from the
// set entirely. The two are different remedies and the difference is
// observable, so it is asserted rather than assumed.
func TestAuthenticate_WithdrawalIsNotRevocationOfTheOtherDirection(t *testing.T) {
	svc := newRealUserStore(t)
	dave := createUser(t, svc, "acme", "dave", nil)

	expired := mintCert(t, "dave", "acme", nil, "")
	holdExpiredBinding(t, svc, dave.ID, expired)
	unbound := mintCert(t, "dave", "acme", nil, "")

	// This test specifically covers the "enrolled" opt-out (go-qrdb#548): the
	// reopen-on-empty-set it asserts does not occur under "required".
	m := mtlsMiddleware(svc, WithMTLSBindingPolicy(auth.MTLSBindingEnrolled))

	// An expired binding is still a binding SET: the account stays closed.
	if _, _, err := m.authenticate(mtlsContext(unbound)); err == nil {
		t.Fatal("an account holding only an expired binding admitted an unbound certificate")
	}

	// Withdrawing it empties the set, which is what reopens the account.
	withdraw(t, svc, dave.ID, expired)
	if _, _, err := m.authenticate(mtlsContext(unbound)); err != nil {
		t.Fatalf("after withdrawing the expired binding the account did not reopen: %v", err)
	}
}
