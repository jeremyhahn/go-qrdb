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
	"strings"
	"testing"
)

// TestEffectiveCapabilities_UnencryptedAtRest pins the SEC-4/SEC-5 report:
// with seal enabled, a standalone node whose local backend keeps data on disk
// outside the barrier's encryption must say so in its capability profile,
// and a node that encrypts must not.
func TestEffectiveCapabilities_UnencryptedAtRest(t *testing.T) {
	cases := []struct {
		name    string
		mode    string
		seal    bool
		smType  string
		backend string
		want    string // substring of the single reported gap; "" = none
	}{
		{"raft mvcc", "raft", true, "mvcc", "", ""},
		{"raft pebble", "raft", true, "pebble", "", ""},
		{"raft default sm", "raft", true, "", "", ""},
		// A raft node starts no state machine that persists plaintext under
		// the barrier (disk/fast over pebble are encrypted, over file refused
		// at start; the rest persist nothing), so it reports no gap.
		{"raft disk", "raft", true, "disk", "", ""},
		{"raft fast", "raft", true, "fast", "", ""},
		{"raft memory", "raft", true, "memory", "", ""},
		{"raft disk, seal off", "raft", false, "disk", "", ""},
		{"standalone pebble", "standalone", true, "", "pebble", ""},
		{"standalone default", "standalone", true, "", "", ""},
		{"standalone memory", "standalone", true, "", "memory", ""},
		{"standalone file", "standalone", true, "", "file", ""},
		{"standalone objstore", "standalone", true, "", "objstore", `"objstore"`},
		{"standalone file, seal off", "standalone", false, "", "file", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &Config{Mode: c.mode, Seal: SealConfig{Enabled: c.seal}, Raft: RaftConfig{SMType: c.smType}}
			if c.backend != "" {
				cfg.Databases = []DatabaseConfig{{Name: "system", Backend: c.backend}}
			}
			gaps := cfg.EffectiveCapabilities().UnencryptedAtRest
			if c.want == "" {
				if len(gaps) != 0 {
					t.Fatalf("UnencryptedAtRest = %q, want none", gaps)
				}
				return
			}
			if len(gaps) != 1 || !strings.Contains(gaps[0], c.want) {
				t.Fatalf("UnencryptedAtRest = %q, want one gap naming %s", gaps, c.want)
			}
		})
	}
}
