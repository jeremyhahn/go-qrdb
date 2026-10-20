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

func TestParseNodeID_RoundTrip(t *testing.T) {
	id, ok, err := ParseNodeID(mustURL(t, "spiffe://qrdb.cluster/node/abcdef0123456789"))
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if id.TrustDomain != "qrdb.cluster" || id.NodeID != "abcdef0123456789" {
		t.Fatalf("parsed = %+v", id)
	}
	if got := id.String(); got != "spiffe://qrdb.cluster/node/abcdef0123456789" {
		t.Fatalf("String = %q", got)
	}
}

func TestParseNodeID_Skips(t *testing.T) {
	// Non-spiffe, tenant-form, and wrong-segment URIs are skipped, not errors.
	for _, u := range []string{
		"https://qrdb.cluster/node/x",
		"spiffe://acme/user/alice",
		"spiffe://qrdb.cluster/node/a/b",
		"spiffe://qrdb.cluster/other/a",
	} {
		if _, ok, err := ParseNodeID(mustURL(t, u)); ok || err != nil {
			t.Fatalf("%q should be skipped: ok=%v err=%v", u, ok, err)
		}
	}
	if _, ok, err := ParseNodeID(nil); ok || err != nil {
		t.Fatalf("nil url: ok=%v err=%v", ok, err)
	}
}

func TestParseNodeID_ErrorBranches(t *testing.T) {
	if _, ok, err := ParseNodeID(mustURL(t, "spiffe:///node/n1")); ok || !errors.Is(err, ErrTrustDomainEmpty) {
		t.Fatalf("empty host: ok=%v err=%v", ok, err)
	}
	// A trailing slash collapses to a 1-segment path → skipped (consistent with
	// the tenant form), not a malformed error.
	if _, ok, err := ParseNodeID(mustURL(t, "spiffe://qrdb.cluster/node/")); ok || err != nil {
		t.Fatalf("trailing slash should skip: ok=%v err=%v", ok, err)
	}
	// A whitespace-only node id is a well-formed 2-segment path that fails
	// validation.
	if _, ok, err := ParseNodeID(mustURL(t, "spiffe://qrdb.cluster/node/%20")); ok || !errors.Is(err, ErrNodeIDEmpty) {
		t.Fatalf("whitespace node id: ok=%v err=%v", ok, err)
	}
}

func TestNodeIdentity_Validate(t *testing.T) {
	cases := []struct {
		name string
		id   NodeIdentity
		want error
	}{
		{"ok", NodeIdentity{"qrdb.cluster", "n1"}, nil},
		{"no-td", NodeIdentity{"  ", "n1"}, ErrTrustDomainEmpty},
		{"no-id", NodeIdentity{"qrdb.cluster", " "}, ErrNodeIDEmpty},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.id.Validate()
			if tc.want == nil && err != nil {
				t.Fatalf("expected valid, got %v", err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, err)
			}
		})
	}
	if (NodeIdentity{}).String() != "" {
		t.Fatal("invalid identity must stringify empty")
	}
}

func TestNodeURI(t *testing.T) {
	if got := NodeURI("qrdb.cluster", "host-7"); got != "spiffe://qrdb.cluster/node/host-7" {
		t.Fatalf("NodeURI = %q", got)
	}
	if NodeURI("", "host-7") != "" {
		t.Fatal("empty trust domain must yield empty URI")
	}
	if NodeURI("qrdb.cluster", "") != "" {
		t.Fatal("empty node id must yield empty URI")
	}
}

func TestNodeFromCert_PinAndFailClosed(t *testing.T) {
	good := &x509.Certificate{URIs: []*url.URL{mustURL(t, "spiffe://qrdb.cluster/node/host-a")}}

	id, err := NodeFromCert(good, "qrdb.cluster")
	if err != nil || id.NodeID != "host-a" {
		t.Fatalf("expected host-a, got %+v err=%v", id, err)
	}

	if _, err := NodeFromCert(good, "evil.cluster"); !errors.Is(err, ErrTrustDomainMismatch) {
		t.Fatalf("expected mismatch, got %v", err)
	}

	if _, err := NodeFromCert(&x509.Certificate{}, "qrdb.cluster"); !errors.Is(err, ErrNoNodeSPIFFEID) {
		t.Fatalf("no SAN: expected no-id, got %v", err)
	}
	if _, err := NodeFromCert(nil, "qrdb.cluster"); !errors.Is(err, ErrNoNodeSPIFFEID) {
		t.Fatalf("nil cert: expected no-id, got %v", err)
	}

	// Malformed node SAN fails closed; a skipped SAN before a valid one matches.
	bad := &x509.Certificate{URIs: []*url.URL{mustURL(t, "spiffe:///node/x")}}
	if _, err := NodeFromCert(bad, ""); !errors.Is(err, ErrTrustDomainEmpty) {
		t.Fatalf("malformed SAN: expected trust-domain-empty, got %v", err)
	}
	mixed := &x509.Certificate{URIs: []*url.URL{
		mustURL(t, "https://qrdb.cluster/healthz"),
		mustURL(t, "spiffe://qrdb.cluster/node/host-b"),
	}}
	if id, err := NodeFromCert(mixed, "qrdb.cluster"); err != nil || id.NodeID != "host-b" {
		t.Fatalf("mixed SANs: id=%+v err=%v", id, err)
	}
}
