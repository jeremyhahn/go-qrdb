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

package database

import (
	"errors"
	"testing"
)

// A kept secret stays bound to the destination it authenticates. An update
// that points a discovery block somewhere else -- a new token endpoint, a new
// static peer set, a new multicast group -- without sending the secret would
// hand the stored secret, which the caller never knew, to the new destination.
// It is refused; sending the secret with the new destination is allowed.
func TestUpdateRaft_ARedirectNeedsTheSecret(t *testing.T) {
	redirects := []struct {
		name   string
		block  string
		mutate func(*DatabaseRaftConfig)
		resend func(*DatabaseRaftConfig)
		stored func(*DatabaseRaftConfig) string
	}{
		{
			name:   "token endpoint",
			block:  "token",
			mutate: func(r *DatabaseRaftConfig) { r.Token.Endpoint = "https://attacker.example.com/join" },
			resend: func(r *DatabaseRaftConfig) { r.Token.Token = "new-token" },
			stored: func(r *DatabaseRaftConfig) string { return r.Token.Token },
		},
		{
			name:   "static peers",
			block:  "static",
			mutate: func(r *DatabaseRaftConfig) { r.Static.Peers = []string{"attacker.example.com:63001"} },
			resend: func(r *DatabaseRaftConfig) { r.Static.SharedSecret = "new-static-secret" },
			stored: func(r *DatabaseRaftConfig) string { return r.Static.SharedSecret },
		},
		{
			name:   "multicast group",
			block:  "multicast",
			mutate: func(r *DatabaseRaftConfig) { r.Multicast.MulticastGroup = "239.255.9.9:63100" },
			resend: func(r *DatabaseRaftConfig) { r.Multicast.SharedSecret = "new-multicast-secret" },
			stored: func(r *DatabaseRaftConfig) string { return r.Multicast.SharedSecret },
		},
	}
	for _, rd := range redirects {
		t.Run(rd.name, func(t *testing.T) {
			m := newTestManager(t)
			t.Cleanup(func() { _ = m.Close() })
			if _, err := m.Create(DatabaseConfig{Name: "redirected", Engine: EngineKV, Backend: BackendMemory}); err != nil {
				t.Fatal(err)
			}
			if err := m.UpdateRaft("redirected", secretsRaft()); err != nil {
				t.Fatal(err)
			}
			before := rd.stored(storedRaft(t, m, "redirected"))

			update := withoutSecrets(secretsRaft())
			rd.mutate(update)
			err := m.UpdateRaft("redirected", update)
			var rebind *DiscoverySecretRebindError
			if !errors.As(err, &rebind) || rebind.Method != rd.block {
				t.Fatalf("redirect without the secret = %v, want *DiscoverySecretRebindError{%s}", err, rd.block)
			}
			if rebind.Error() == "" {
				t.Fatal("empty error text")
			}
			if got := rd.stored(storedRaft(t, m, "redirected")); got != before {
				t.Fatalf("a refused redirect changed the stored secret to %q", got)
			}

			rd.resend(update)
			if err := m.UpdateRaft("redirected", update); err != nil {
				t.Fatalf("redirect with a new secret: %v", err)
			}
			if got := rd.stored(storedRaft(t, m, "redirected")); got == before || got == "" {
				t.Fatalf("stored secret after a redirect with a new one = %q, want the new one", got)
			}
		})
	}
}

// The same destination written in another order is not a redirect: a static
// peer set is a set.
func TestUpdateRaft_ReorderedPeersKeepTheSecret(t *testing.T) {
	m := newTestManager(t)
	t.Cleanup(func() { _ = m.Close() })
	if _, err := m.Create(DatabaseConfig{Name: "reordered", Engine: EngineKV, Backend: BackendMemory}); err != nil {
		t.Fatal(err)
	}
	cfg := secretsRaft()
	cfg.Static.Peers = []string{"a.example.com:63001", "b.example.com:63001"}
	if err := m.UpdateRaft("reordered", cfg); err != nil {
		t.Fatal(err)
	}
	update := withoutSecrets(cfg)
	update.Static.Peers = []string{"b.example.com:63001", "a.example.com:63001"}
	if err := m.UpdateRaft("reordered", update); err != nil {
		t.Fatalf("reordered peers without the secret: %v", err)
	}
	if got := storedRaft(t, m, "reordered").Static.SharedSecret; got != "static-secret" {
		t.Fatalf("static secret = %q, want it kept", got)
	}
}
