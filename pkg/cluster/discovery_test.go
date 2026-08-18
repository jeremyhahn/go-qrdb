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

package cluster

import (
	"errors"
	"testing"
)

func TestParsePeers(t *testing.T) {
	members, err := ParsePeers([]string{"1=127.0.0.1:7001", "2=127.0.0.1:7002"})
	if err != nil {
		t.Fatalf("ParsePeers() error = %v, want nil", err)
	}
	if len(members) != 2 {
		t.Fatalf("len(members) = %d, want 2", len(members))
	}
	if members[1] != "127.0.0.1:7001" || members[2] != "127.0.0.1:7002" {
		t.Fatalf("members = %v, want {1:127.0.0.1:7001, 2:127.0.0.1:7002}", members)
	}
}

func TestParsePeers_Empty(t *testing.T) {
	members, err := ParsePeers(nil)
	if err != nil {
		t.Fatalf("ParsePeers(nil) error = %v, want nil", err)
	}
	if len(members) != 0 {
		t.Fatalf("len(members) = %d, want 0", len(members))
	}
}

func TestParsePeers_Malformed(t *testing.T) {
	cases := []string{"badformat", "=127.0.0.1:7001", "1=", "x=127.0.0.1:7001"}
	for _, spec := range cases {
		_, err := ParsePeers([]string{spec})
		var ppe *PeerParseError
		if !errors.As(err, &ppe) {
			t.Fatalf("ParsePeers(%q) error = %T (%v), want *PeerParseError", spec, err, err)
		}
		if ppe.Input != spec {
			t.Fatalf("PeerParseError.Input = %q, want %q", ppe.Input, spec)
		}
	}
}

func TestBuildDiscovery_Unknown(t *testing.T) {
	_, err := BuildDiscovery(Options{Method: "consul"})
	var ude *UnknownDiscoveryError
	if !errors.As(err, &ude) {
		t.Fatalf("BuildDiscovery() error = %T (%v), want *UnknownDiscoveryError", err, err)
	}
	if ude.Name != "consul" {
		t.Fatalf("UnknownDiscoveryError.Name = %q, want %q", ude.Name, "consul")
	}
}

func TestBuildDiscovery_Static(t *testing.T) {
	m, err := BuildDiscovery(Options{
		Method: "static",
		Peers:  []string{"1=127.0.0.1:7001", "2=127.0.0.1:7002"},
	})
	if err != nil {
		t.Fatalf("BuildDiscovery(static) error = %v, want nil", err)
	}
	if m.Name() != "static" {
		t.Fatalf("Name() = %q, want %q", m.Name(), "static")
	}
	defer func() { _ = m.Stop() }()
}

func TestBuildDiscovery_StaticBadPeers(t *testing.T) {
	_, err := BuildDiscovery(Options{Method: "static", Peers: []string{"badformat"}})
	var ppe *PeerParseError
	if !errors.As(err, &ppe) {
		t.Fatalf("BuildDiscovery(static) error = %T (%v), want *PeerParseError", err, err)
	}
}

func TestBuildDiscovery_MulticastMissingSecret(t *testing.T) {
	_, err := BuildDiscovery(Options{Method: "multicast"})
	var mfe *MissingFlagError
	if !errors.As(err, &mfe) {
		t.Fatalf("BuildDiscovery(multicast) error = %T (%v), want *MissingFlagError", err, err)
	}
	if mfe.Flag != "multicast-secret" {
		t.Fatalf("MissingFlagError.Flag = %q, want %q", mfe.Flag, "multicast-secret")
	}
}

func TestBuildDiscovery_DNSMissingDomain(t *testing.T) {
	_, err := BuildDiscovery(Options{Method: "dns"})
	var mfe *MissingFlagError
	if !errors.As(err, &mfe) {
		t.Fatalf("BuildDiscovery(dns) error = %T (%v), want *MissingFlagError", err, err)
	}
	if mfe.Flag != "dns-domain" {
		t.Fatalf("MissingFlagError.Flag = %q, want %q", mfe.Flag, "dns-domain")
	}
}

func TestBuildDiscovery_DNS(t *testing.T) {
	m, err := BuildDiscovery(Options{Method: "dns", DNSDomain: "cluster.local"})
	if err != nil {
		t.Fatalf("BuildDiscovery(dns) error = %v, want nil", err)
	}
	if m.Name() != "dns" {
		t.Fatalf("Name() = %q, want %q", m.Name(), "dns")
	}
	defer func() { _ = m.Stop() }()
}

func TestBuildDiscovery_TokenMissingEndpoint(t *testing.T) {
	_, err := BuildDiscovery(Options{Method: "token"})
	var mfe *MissingFlagError
	if !errors.As(err, &mfe) {
		t.Fatalf("BuildDiscovery(token) error = %T (%v), want *MissingFlagError", err, err)
	}
	if mfe.Flag != "discovery-endpoint" {
		t.Fatalf("MissingFlagError.Flag = %q, want %q", mfe.Flag, "discovery-endpoint")
	}
}

func TestBuildDiscovery_TokenMissingToken(t *testing.T) {
	_, err := BuildDiscovery(Options{Method: "token", TokenEndpoint: "https://disco.example.com"})
	var mfe *MissingFlagError
	if !errors.As(err, &mfe) {
		t.Fatalf("BuildDiscovery(token) error = %T (%v), want *MissingFlagError", err, err)
	}
	if mfe.Flag != "discovery-token" {
		t.Fatalf("MissingFlagError.Flag = %q, want %q", mfe.Flag, "discovery-token")
	}
}

func TestBuildDiscovery_Token(t *testing.T) {
	m, err := BuildDiscovery(Options{
		Method:           "token",
		TokenEndpoint:    "https://disco.example.com",
		Token:            "s3cr3t",
		NodeID:           1,
		RaftAddress:      "127.0.0.1:7001",
		TokenClusterSize: 3,
	})
	if err != nil {
		t.Fatalf("BuildDiscovery(token) error = %v, want nil", err)
	}
	if m.Name() != "token" {
		t.Fatalf("Name() = %q, want %q", m.Name(), "token")
	}
	defer func() { _ = m.Stop() }()
}

func TestBuildDiscovery_StaticWithSecret(t *testing.T) {
	m, err := BuildDiscovery(Options{
		Method:          "static",
		Peers:           []string{"1=127.0.0.1:7001"},
		MulticastSecret: "shared-secret-16by",
	})
	if err != nil {
		t.Fatalf("BuildDiscovery(static) error = %v, want nil", err)
	}
	defer func() { _ = m.Stop() }()
	if m.Name() != "static" {
		t.Fatalf("Name() = %q, want %q", m.Name(), "static")
	}
}

func TestBuildDiscovery_MulticastSuccess(t *testing.T) {
	m, err := BuildDiscovery(Options{
		Method:          "multicast",
		NodeID:          1,
		RaftAddress:     "127.0.0.1:7001",
		MulticastSecret: "shared-secret-16by",
		DeploymentID:    42,
	})
	if err != nil {
		t.Fatalf("BuildDiscovery(multicast) error = %v, want nil", err)
	}
	defer func() { _ = m.Stop() }()
	if m.Name() != "multicast" {
		t.Fatalf("Name() = %q, want %q", m.Name(), "multicast")
	}
}

func TestIsSingleNode(t *testing.T) {
	cases := []struct {
		name string
		opts Options
		want bool
	}{
		{"static no peers", Options{Method: "static"}, true},
		{"static one peer", Options{Method: "static", Peers: []string{"1=127.0.0.1:7001"}}, true},
		{"static two peers", Options{Method: "static", Peers: []string{"1=a:1", "2=b:2"}}, false},
		{"static bad peer", Options{Method: "static", Peers: []string{"bad"}}, false},
		{"multicast", Options{Method: "multicast"}, false},
		{"dns", Options{Method: "dns"}, false},
		{"token", Options{Method: "token"}, false},
	}
	for _, tc := range cases {
		if got := IsSingleNode(tc.opts); got != tc.want {
			t.Fatalf("IsSingleNode(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}
