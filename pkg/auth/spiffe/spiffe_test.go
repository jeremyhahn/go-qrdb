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

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return u
}

func TestParseID_Skips(t *testing.T) {
	for _, u := range []string{
		"https://acme/user/alice",       // not a SPIFFE ID
		"spiffe://qrdb.cluster/node/n1", // a node identity, ParseNodeID's
		"spiffe://qrdb.cluster/node/127.0.0.1:7000",
	} {
		if _, ok, err := ParseID(mustURL(t, u)); ok || err != nil {
			t.Fatalf("%q should be skipped: ok=%v err=%v", u, ok, err)
		}
	}
	if _, ok, err := ParseID(nil); ok || err != nil {
		t.Fatalf("nil url: ok=%v err=%v", ok, err)
	}
}

// TestParseID_RefusesNonStandard pins every SPIFFE ID rule (docs/reference/
// spiffe-id.md) ParseID enforces: a spiffe URI that is not a SPIFFE ID, or not
// of the workload form, fails closed rather than being skipped.
func TestParseID_RefusesNonStandard(t *testing.T) {
	long := strings.Repeat("a", maxIDLen)
	tests := []struct {
		name string
		uri  string
		want error
	}{
		{"empty-trust-domain", "spiffe:///user/alice", ErrTrustDomainEmpty},
		{"uppercase-trust-domain", "spiffe://Acme/user/alice", ErrTrustDomainInvalid},
		{"port", "spiffe://acme:8443/user/alice", ErrTrustDomainInvalid},
		{"userinfo", "spiffe://bob@acme/user/alice", ErrURIInvalid},
		{"query", "spiffe://acme/user/alice?x=1", ErrURIInvalid},
		{"empty-query", "spiffe://acme/user/alice?", ErrURIInvalid},
		{"fragment", "spiffe://acme/user/alice#f", ErrURIInvalid},
		{"too-long", "spiffe://acme/user/" + long, ErrURIInvalid},
		{"percent-encoded-path", "spiffe://acme/user/al%69ce", ErrPathInvalid},
		{"percent-encoded-trust-domain", "spiffe://ac%C3%A9/user/alice", ErrTrustDomainInvalid},
		{"trailing-slash", "spiffe://acme/user/alice/", ErrPathInvalid},
		{"empty-segment", "spiffe://acme/user//alice", ErrPathInvalid},
		{"dot-segment", "spiffe://acme/user/.", ErrPathInvalid},
		{"dotdot-segment", "spiffe://acme/user/..", ErrPathInvalid},
		{"bad-char", "spiffe://acme/user/al@ce", ErrPathInvalid},
		{"no-path", "spiffe://acme", ErrPathInvalid},
		{"one-segment", "spiffe://acme/user", ErrPathInvalid},
		{"three-segments", "spiffe://acme/user/a/b", ErrPathInvalid},
		{"old-tenant-grammar", "spiffe://qrdb.cluster/tenant/acme/user/alice", ErrPathInvalid},
		{"foreign-form", "spiffe://acme/ns/default", ErrKindInvalid},
		{"unknown-kind", "spiffe://acme/robot/alice", ErrKindInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.uri)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.uri, err)
			}
			if _, ok, err := ParseID(u); ok || !errors.Is(err, tc.want) {
				t.Fatalf("ParseID(%q) ok=%v err=%v, want %v", tc.uri, ok, err, tc.want)
			}
		})
	}
	if _, ok, err := ParseID(&url.URL{Scheme: scheme, Opaque: "acme/user/alice"}); ok || !errors.Is(err, ErrURIInvalid) {
		t.Fatalf("opaque: ok=%v err=%v", ok, err)
	}
}

func TestWorkloadFromCert_Reads(t *testing.T) {
	good := &x509.Certificate{URIs: []*url.URL{mustURL(t, "spiffe://acme/user/alice")}}
	id, err := WorkloadFromCert(good)
	if err != nil || id != (ID{TrustDomain: "acme", Kind: KindUser, Name: "alice"}) {
		t.Fatalf("expected acme/user/alice, got %+v err=%v", id, err)
	}

	// Non-workload SANs (other schemes, node identities) are skipped.
	mixed := &x509.Certificate{URIs: []*url.URL{
		mustURL(t, "https://acme/healthz"),
		mustURL(t, "spiffe://qrdb.cluster/node/n1"),
		mustURL(t, "spiffe://acme/svc/api"),
	}}
	id, err = WorkloadFromCert(mixed)
	if err != nil || id.TrustDomain != "acme" || id.Kind != KindService {
		t.Fatalf("mixed SANs: id=%+v err=%v", id, err)
	}
}

func TestWorkloadFromCert_FailClosed(t *testing.T) {
	if _, err := WorkloadFromCert(nil); !errors.Is(err, ErrNoTenantSPIFFEID) {
		t.Fatalf("nil cert: expected no-id, got %v", err)
	}
	if _, err := WorkloadFromCert(&x509.Certificate{}); !errors.Is(err, ErrNoTenantSPIFFEID) {
		t.Fatalf("no SANs: expected no-id, got %v", err)
	}
	node := &x509.Certificate{URIs: []*url.URL{mustURL(t, "spiffe://qrdb.cluster/node/n1")}}
	if _, err := WorkloadFromCert(node); !errors.Is(err, ErrNoTenantSPIFFEID) {
		t.Fatalf("node cert: expected no-id, got %v", err)
	}
	// A malformed SPIFFE SAN fails closed even beside a valid one.
	bad := &x509.Certificate{URIs: []*url.URL{
		mustURL(t, "spiffe://acme/user/alice"),
		mustURL(t, "spiffe://acme/robot/alice"),
	}}
	if _, err := WorkloadFromCert(bad); !errors.Is(err, ErrKindInvalid) {
		t.Fatalf("malformed SAN: expected kind-invalid, got %v", err)
	}
}

func TestID_String(t *testing.T) {
	id := ID{TrustDomain: "acme", Kind: KindService, Name: "svc1"}
	if got := id.String(); got != "spiffe://acme/svc/svc1" {
		t.Fatalf("String = %q", got)
	}
	if (ID{}).String() != "" {
		t.Fatal("invalid ID should stringify empty")
	}
}

func TestID_Validate_AllBranches(t *testing.T) {
	tests := []struct {
		name string
		id   ID
		want error // nil = valid
	}{
		{"valid-user", ID{TrustDomain: "acme", Kind: KindUser, Name: "n"}, nil},
		{"valid-svc", ID{TrustDomain: "acme.example", Kind: KindService, Name: "Web_1.v-2"}, nil},
		{"empty-trust-domain", ID{TrustDomain: "", Kind: KindUser, Name: "n"}, ErrTrustDomainEmpty},
		{"blank-trust-domain", ID{TrustDomain: "  ", Kind: KindUser, Name: "n"}, ErrTrustDomainInvalid},
		{"uppercase-trust-domain", ID{TrustDomain: "Acme", Kind: KindUser, Name: "n"}, ErrTrustDomainInvalid},
		{"long-trust-domain", ID{TrustDomain: strings.Repeat("a", maxTrustDomainLen+1), Kind: KindUser, Name: "n"}, ErrTrustDomainInvalid},
		{"invalid-kind", ID{TrustDomain: "acme", Kind: "robot", Name: "n"}, ErrKindInvalid},
		{"empty-kind", ID{TrustDomain: "acme", Kind: "", Name: "n"}, ErrKindInvalid},
		{"empty-name", ID{TrustDomain: "acme", Kind: KindUser, Name: ""}, ErrNameEmpty},
		{"blank-name", ID{TrustDomain: "acme", Kind: KindUser, Name: " "}, ErrPathInvalid},
		{"slash-name", ID{TrustDomain: "acme", Kind: KindUser, Name: "a/b"}, ErrPathInvalid},
		{"email-name", ID{TrustDomain: "acme", Kind: KindUser, Name: "a@b.example"}, ErrPathInvalid},
		{"dot-name", ID{TrustDomain: "acme", Kind: KindUser, Name: ".."}, ErrPathInvalid},
		{"too-long", ID{TrustDomain: "acme", Kind: KindUser, Name: strings.Repeat("n", maxIDLen)}, ErrURIInvalid},
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
