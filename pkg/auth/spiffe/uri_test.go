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

package spiffe

import (
	"crypto/x509"
	"errors"
	"net/url"
	"strings"
	"testing"
)

// TestURIRoundTripsThroughTenantFromCert is the builder's contract: a SAN it
// builds, placed on a certificate, is read back by the parser as exactly the
// identity it was built from, under the pinned trust domain.
func TestURIRoundTripsThroughTenantFromCert(t *testing.T) {
	for _, id := range []ID{
		{TrustDomain: "db.example.com", TenantID: "acme", Kind: KindUser, Name: "alice"},
		{TrustDomain: "db.example.com", TenantID: "acme", Kind: KindService, Name: "billing"},
	} {
		u, err := id.URI()
		if err != nil {
			t.Fatalf("URI(%+v): %v", id, err)
		}
		if u.String() != id.String() {
			t.Fatalf("URI = %q, want the canonical form %q", u, id.String())
		}
		got, err := TenantFromCert(&x509.Certificate{URIs: []*url.URL{u}}, id.TrustDomain)
		if err != nil || got != id {
			t.Fatalf("TenantFromCert = %+v, %v; want %+v", got, err, id)
		}
	}
}

func TestURIRefusesAnInvalidField(t *testing.T) {
	valid := ID{TrustDomain: "db.example.com", TenantID: "acme", Kind: KindUser, Name: "alice"}
	tests := []struct {
		name   string
		mutate func(*ID)
		want   error
	}{
		{"empty trust domain", func(id *ID) { id.TrustDomain = "" }, ErrTrustDomainEmpty},
		{"empty tenant", func(id *ID) { id.TenantID = " " }, ErrTenantEmpty},
		{"unknown kind", func(id *ID) { id.Kind = "robot" }, ErrKindInvalid},
		{"empty name", func(id *ID) { id.Name = "" }, ErrNameEmpty},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := valid
			tc.mutate(&id)
			u, err := id.URI()
			var refused *IDError
			if !errors.As(err, &refused) || !errors.Is(err, tc.want) || u != nil {
				t.Fatalf("URI = %v, %v; want an *IDError wrapping %v", u, err, tc.want)
			}
			if refused.ID != id {
				t.Fatalf("IDError.ID = %+v, want %+v", refused.ID, id)
			}
		})
	}
}

// TestURIRefusesAnIdentityThatDoesNotRoundTrip covers the case Validate does
// not: every field present, but the URI would name a different identity or
// none, so a certificate carrying it would authenticate as someone else.
func TestURIRefusesAnIdentityThatDoesNotRoundTrip(t *testing.T) {
	for _, id := range []ID{
		{TrustDomain: "db.example.com", TenantID: "acme/evil", Kind: KindUser, Name: "alice"},
		{TrustDomain: "db.example.com", TenantID: "acme", Kind: KindUser, Name: "alice/extra"},
		{TrustDomain: "db.example.com/path", TenantID: "acme", Kind: KindUser, Name: "alice"},
	} {
		u, err := id.URI()
		if !errors.Is(err, ErrIDNotRoundTrip) || u != nil {
			t.Fatalf("URI(%+v) = %v, %v; want ErrIDNotRoundTrip", id, u, err)
		}
	}
}

func TestIDErrorNamesTheIdentity(t *testing.T) {
	err := &IDError{ID: ID{TrustDomain: "db.example.com", TenantID: "acme", Kind: "robot", Name: "r2"}, Err: ErrKindInvalid}
	for _, want := range []string{`"db.example.com"`, `"acme"`, `"robot"`, `"r2"`, ErrKindInvalid.Error()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not mention %s", err.Error(), want)
		}
	}
	if !errors.Is(err, ErrKindInvalid) {
		t.Fatal("IDError does not unwrap to its cause")
	}
}
