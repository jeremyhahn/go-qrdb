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

package config

import (
	"errors"
	"testing"

	"gopkg.in/yaml.v3"
)

func peerCredU32(v uint32) *uint32 { return &v }

func TestUnixPeerCredentials_YAMLRoundTrip(t *testing.T) {
	const doc = `
socket_path: /run/qrdb/qrdb.sock
peer_credentials:
  - uid: 1000
    tenant: acme
    user: alice
  - gid: 0
    user: root-ops
`
	var u UnixConfig
	if err := yaml.Unmarshal([]byte(doc), &u); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(u.PeerCredentials) != 2 {
		t.Fatalf("parsed %d peer credentials, want 2", len(u.PeerCredentials))
	}
	a, b := u.PeerCredentials[0], u.PeerCredentials[1]
	if a.UID == nil || *a.UID != 1000 || a.GID != nil || a.Tenant != "acme" || a.User != "alice" {
		t.Fatalf("first mapping = %+v, want uid 1000 -> acme/alice", a)
	}
	if b.GID == nil || *b.GID != 0 || b.UID != nil || b.Tenant != "" || b.User != "root-ops" {
		t.Fatalf("second mapping = %+v, want gid 0 (set, not absent) -> root-ops", b)
	}

	out, err := yaml.Marshal(u)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var again UnixConfig
	if err := yaml.Unmarshal(out, &again); err != nil {
		t.Fatalf("Unmarshal of marshalled form: %v", err)
	}
	if len(again.PeerCredentials) != 2 || again.PeerCredentials[1].GID == nil || *again.PeerCredentials[1].GID != 0 || again.PeerCredentials[1].UID != nil {
		t.Fatalf("round trip lost a mapping: %s", out)
	}

	empty, err := yaml.Marshal(UnixConfig{SocketPath: "/run/qrdb/qrdb.sock"})
	if err != nil {
		t.Fatalf("Marshal empty: %v", err)
	}
	var back map[string]any
	if err := yaml.Unmarshal(empty, &back); err != nil {
		t.Fatalf("Unmarshal empty: %v", err)
	}
	if _, ok := back["peer_credentials"]; ok {
		t.Fatalf("an unset peer_credentials list is marshalled: %s", empty)
	}
}

func TestUnixPeerCredentials_ValidateRejects(t *testing.T) {
	cases := []struct {
		name  string
		creds []UnixPeerCredential
		field string
	}{
		{"uid and gid", []UnixPeerCredential{{UID: peerCredU32(1), GID: peerCredU32(1), User: "a"}}, "unix.peer_credentials[0].uid"},
		{"neither uid nor gid", []UnixPeerCredential{{User: "a"}}, "unix.peer_credentials[0].uid"},
		{"empty user", []UnixPeerCredential{{UID: peerCredU32(1), User: "  "}}, "unix.peer_credentials[0].user"},
		{"duplicate uid", []UnixPeerCredential{{UID: peerCredU32(1), User: "a"}, {UID: peerCredU32(1), User: "b"}}, "unix.peer_credentials[1].uid"},
		{"duplicate gid", []UnixPeerCredential{{GID: peerCredU32(2), User: "a"}, {GID: peerCredU32(2), User: "b"}}, "unix.peer_credentials[1].gid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Unix.PeerCredentials = tc.creds
			var ve *ValidationError
			if err := cfg.Validate(); !errors.As(err, &ve) || ve.Field != tc.field {
				t.Fatalf("Validate() = %v, want a ValidationError on %s", err, tc.field)
			}
		})
	}
}

func TestUnixPeerCredentials_ValidateAccepts(t *testing.T) {
	cfg := DefaultConfig()
	// A uid and a gid of the same number are distinct keys.
	cfg.Unix.PeerCredentials = []UnixPeerCredential{
		{UID: peerCredU32(1000), Tenant: "acme", User: "alice"},
		{GID: peerCredU32(1000), User: "ops"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestUnixPeerCredentials_Mappings(t *testing.T) {
	if m := (UnixConfig{}).PeerCredentialMappings(); m != nil {
		t.Fatalf("no peer credentials gave mappings %v, want nil", m)
	}
	u := UnixConfig{PeerCredentials: []UnixPeerCredential{
		{UID: peerCredU32(1000), Tenant: "acme", User: "alice"},
		{GID: peerCredU32(0), User: "ops"},
	}}
	m := u.PeerCredentialMappings()
	if len(m) != 2 {
		t.Fatalf("got %d mappings, want 2", len(m))
	}
	if m[0].UID == nil || *m[0].UID != 1000 || m[0].GID != nil || m[0].TenantID != "acme" || m[0].Username != "alice" {
		t.Fatalf("mapping 0 = %+v", m[0])
	}
	if m[1].GID == nil || *m[1].GID != 0 || m[1].UID != nil || m[1].TenantID != "" || m[1].Username != "ops" {
		t.Fatalf("mapping 1 = %+v", m[1])
	}
	// The mappings own their ids: changing the config does not change them.
	*u.PeerCredentials[0].UID = 7
	if *m[0].UID != 1000 {
		t.Fatalf("mapping aliases the config's uid: now %d", *m[0].UID)
	}
}
