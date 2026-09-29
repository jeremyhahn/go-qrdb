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

package middleware

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/holderprofile"
)

// mintHolderCert issues a self-signed leaf CN=cn with the given OUs, each in
// its own RDN so their order survives encoding.
func mintHolderCert(t *testing.T, cn string, ous ...string) certKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	name := pkix.Name{CommonName: cn}
	for _, ou := range ous {
		name.ExtraNames = append(name.ExtraNames, pkix.AttributeTypeAndValue{Type: []int{2, 5, 4, 11}, Value: ou})
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      name,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return certKey{cert: cert, key: key}
}

// TestAuthenticate_HolderCertificateIsNeverAUserLogin is the defence in depth
// behind the ceremony CA: the system tenant's "admin" exists, holds roles and
// has no enrolled certificate binding -- the default posture admits any
// certificate naming it. An officer certificate the ceremony CA issued under
// that name (CN=admin, OU=[system, custodian]) must still not become that
// user: it authenticates with no roles, and so reaches only the
// identity-gated ceremony surface.
func TestAuthenticate_HolderCertificateIsNeverAUserLogin(t *testing.T) {
	svc := newRealUserStore(t)
	createUser(t, svc, tenantid.System, "admin", []string{"admin-role"})
	m := mtlsMiddleware(svc)

	for _, profile := range []string{holderprofile.Custodian, holderprofile.SecurityOfficer} {
		holder := mintHolderCert(t, "admin", tenantid.System, profile)
		_, identity, err := m.authenticate(mtlsContext(holder))
		if err != nil {
			t.Fatalf("%s: holder certificate refused outright: %v", profile, err)
		}
		if len(identity.Roles) != 0 {
			t.Fatalf("%s: a holder certificate named admin authenticated with admin's roles %v", profile, identity.Roles)
		}
		if !auth.IsHolderIdentity(identity) {
			t.Fatalf("%s: identity not marked as a holder: %+v", profile, identity.Metadata)
		}
	}

	// The control: the same name without a holder profile is the user.
	_, identity, err := m.authenticate(mtlsContext(mintHolderCert(t, "admin", tenantid.System)))
	if err != nil {
		t.Fatalf("user certificate refused: %v", err)
	}
	if len(identity.Roles) != 1 || auth.IsHolderIdentity(identity) {
		t.Fatalf("user certificate identity = %+v, want admin's roles and no holder mark", identity)
	}
}

// TestAuthenticate_LegacyHolderLayoutIsAHolderToo: a certificate issued before
// the ceremony CA set the tenant carries the profile as its FIRST OU. It is a
// holder certificate in any position.
func TestAuthenticate_LegacyHolderLayoutIsAHolderToo(t *testing.T) {
	svc := newRealUserStore(t)
	createUser(t, svc, holderprofile.Custodian, "admin", []string{"admin-role"})
	m := mtlsMiddleware(svc)

	_, identity, err := m.authenticate(mtlsContext(mintHolderCert(t, "admin", holderprofile.Custodian)))
	if err != nil {
		t.Fatalf("legacy holder certificate refused outright: %v", err)
	}
	if len(identity.Roles) != 0 || !auth.IsHolderIdentity(identity) {
		t.Fatalf("legacy holder identity = %+v; want no roles and the holder mark", identity)
	}
}
