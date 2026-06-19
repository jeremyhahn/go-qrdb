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
	"testing"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return u
}

func TestParseID_RoundTrip(t *testing.T) {
	id, ok, err := ParseID(mustURL(t, "spiffe://example.com/tenant/acme/user/alice"))
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if id.TrustDomain != "example.com" || id.TenantID != "acme" || id.Kind != KindUser || id.Name != "alice" {
		t.Fatalf("parsed = %+v", id)
	}
}

func TestParseID_Skips(t *testing.T) {
	for _, u := range []string{"https://example.com/x", "spiffe://example.com/ns/default/sa/web"} {
		_, ok, err := ParseID(mustURL(t, u))
		if ok || err != nil {
			t.Fatalf("%q should be skipped: ok=%v err=%v", u, ok, err)
		}
	}
}

func TestTenantFromCert_PinAndFailClosed(t *testing.T) {
	good := &x509.Certificate{URIs: []*url.URL{mustURL(t, "spiffe://example.com/tenant/acme/user/alice")}}

	id, err := TenantFromCert(good, "example.com")
	if err != nil || id.TenantID != "acme" {
		t.Fatalf("expected acme, got %+v err=%v", id, err)
	}

	if _, err := TenantFromCert(good, "evil.example"); !errors.Is(err, ErrTrustDomainMismatch) {
		t.Fatalf("expected mismatch, got %v", err)
	}

	none := &x509.Certificate{}
	if _, err := TenantFromCert(none, "example.com"); !errors.Is(err, ErrNoTenantSPIFFEID) {
		t.Fatalf("expected no-id, got %v", err)
	}

	if _, err := TenantFromCert(nil, "example.com"); !errors.Is(err, ErrNoTenantSPIFFEID) {
		t.Fatalf("nil cert: expected no-id, got %v", err)
	}
}

func TestID_StringAndValidate(t *testing.T) {
	id := ID{TrustDomain: "td", TenantID: "t", Kind: KindService, Name: "svc1"}
	if got := id.String(); got != "spiffe://td/tenant/t/svc/svc1" {
		t.Fatalf("String = %q", got)
	}
	if (ID{}).String() != "" {
		t.Fatal("invalid ID should stringify empty")
	}
	if err := (ID{Kind: KindUser, TenantID: "t", Name: "n"}).Validate(); !errors.Is(err, ErrTrustDomainEmpty) {
		t.Fatalf("expected trust-domain-empty, got %v", err)
	}
}

func TestID_Validate_AllBranches(t *testing.T) {
	tests := []struct {
		name string
		id   ID
		want error // nil = valid
	}{
		{"valid-user", ID{TrustDomain: "td", TenantID: "t", Kind: KindUser, Name: "n"}, nil},
		{"valid-svc", ID{TrustDomain: "td", TenantID: "t", Kind: KindService, Name: "n"}, nil},
		{"blank-trust-domain", ID{TrustDomain: "  ", TenantID: "t", Kind: KindUser, Name: "n"}, ErrTrustDomainEmpty},
		{"empty-tenant", ID{TrustDomain: "td", TenantID: "", Kind: KindUser, Name: "n"}, ErrTenantEmpty},
		{"blank-tenant", ID{TrustDomain: "td", TenantID: "\t", Kind: KindUser, Name: "n"}, ErrTenantEmpty},
		{"invalid-kind", ID{TrustDomain: "td", TenantID: "t", Kind: "robot", Name: "n"}, ErrKindInvalid},
		{"empty-kind", ID{TrustDomain: "td", TenantID: "t", Kind: "", Name: "n"}, ErrKindInvalid},
		{"empty-name", ID{TrustDomain: "td", TenantID: "t", Kind: KindUser, Name: ""}, ErrNameEmpty},
		{"blank-name", ID{TrustDomain: "td", TenantID: "t", Kind: KindUser, Name: " "}, ErrNameEmpty},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.id.Validate()
			if tc.want == nil {
				if err != nil {
					t.Fatalf("expected valid, got %v", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, err)
			}
		})
	}
}

func TestParseID_ErrorBranches(t *testing.T) {
	// Nil URL is skipped, not an error.
	if _, ok, err := ParseID(nil); ok || err != nil {
		t.Fatalf("nil url: ok=%v err=%v", ok, err)
	}

	// SPIFFE scheme with empty host fails closed on the trust domain.
	if _, ok, err := ParseID(mustURL(t, "spiffe:///tenant/acme/user/alice")); ok || !errors.Is(err, ErrTrustDomainEmpty) {
		t.Fatalf("empty host: ok=%v err=%v", ok, err)
	}

	// Well-formed tenant path but an invalid kind surfaces the Validate error.
	if _, ok, err := ParseID(mustURL(t, "spiffe://example.com/tenant/acme/robot/alice")); ok || !errors.Is(err, ErrKindInvalid) {
		t.Fatalf("invalid kind: ok=%v err=%v", ok, err)
	}

	// Wrong segment count / non-tenant prefix is skipped (not an error).
	for _, u := range []string{
		"spiffe://example.com/tenant/acme/user",      // too few segments
		"spiffe://example.com/tenant/acme/user/a/b",  // too many segments
		"spiffe://example.com/other/acme/user/alice", // not a tenant path
	} {
		if _, ok, err := ParseID(mustURL(t, u)); ok || err != nil {
			t.Fatalf("%q should be skipped: ok=%v err=%v", u, ok, err)
		}
	}
}

func TestTenantFromCert_MalformedAndSkip(t *testing.T) {
	// A malformed tenant SPIFFE SAN fails closed with the parse error.
	bad := &x509.Certificate{URIs: []*url.URL{mustURL(t, "spiffe://example.com/tenant/acme/robot/alice")}}
	if _, err := TenantFromCert(bad, "example.com"); !errors.Is(err, ErrKindInvalid) {
		t.Fatalf("malformed SAN: expected kind-invalid, got %v", err)
	}

	// A non-tenant SAN is skipped before the valid tenant SAN is matched.
	mixed := &x509.Certificate{URIs: []*url.URL{
		mustURL(t, "https://example.com/healthz"),
		mustURL(t, "spiffe://example.com/tenant/acme/svc/api"),
	}}
	id, err := TenantFromCert(mixed, "")
	if err != nil || id.TenantID != "acme" || id.Kind != KindService {
		t.Fatalf("mixed SANs: id=%+v err=%v", id, err)
	}
}
