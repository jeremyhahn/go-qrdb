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

package importstage

import (
	"strings"
	"testing"
)

func TestMetaKey(t *testing.T) {
	k := MetaKey("sess1")
	if !strings.HasPrefix(string(k), TablePrefix) {
		t.Errorf("MetaKey(%q) = %q, want it to start with TablePrefix", "sess1", k)
	}
	if !strings.HasSuffix(string(k), "/meta") {
		t.Errorf("MetaKey(%q) = %q, want it to end with /meta", "sess1", k)
	}
	if !strings.Contains(string(k), "sess1") {
		t.Errorf("MetaKey(%q) = %q, want it to contain the session id", "sess1", k)
	}
}

func TestMetaKey_DifferentSessionsDoNotCollide(t *testing.T) {
	if string(MetaKey("a")) == string(MetaKey("b")) {
		t.Error("MetaKey(\"a\") == MetaKey(\"b\"), want distinct keys for distinct sessions")
	}
}

func TestChunkKey_OrdersBySequence(t *testing.T) {
	c0 := ChunkKey("sess1", 0)
	c1 := ChunkKey("sess1", 1)
	c255 := ChunkKey("sess1", 255)
	c256 := ChunkKey("sess1", 256)

	if string(c0) >= string(c1) {
		t.Errorf("ChunkKey(seq=0) %q >= ChunkKey(seq=1) %q, want strictly increasing order", c0, c1)
	}
	if string(c255) >= string(c256) {
		t.Errorf("ChunkKey(seq=255) %q >= ChunkKey(seq=256) %q, want strictly increasing order across a byte boundary", c255, c256)
	}
	if !strings.HasPrefix(string(c0), TablePrefix) {
		t.Errorf("ChunkKey(%q, 0) = %q, want it to start with TablePrefix", "sess1", c0)
	}
}

func TestChunkKey_DifferentSessionsDoNotCollide(t *testing.T) {
	if string(ChunkKey("a", 0)) == string(ChunkKey("b", 0)) {
		t.Error("ChunkKey(\"a\", 0) == ChunkKey(\"b\", 0), want distinct keys for distinct sessions")
	}
}

func TestMetaKeyAndChunkKey_NeverCollide(t *testing.T) {
	sessionID := "sess1"
	meta := MetaKey(sessionID)
	for _, seq := range []uint64{0, 1, 255, 256, 1 << 32} {
		if string(meta) == string(ChunkKey(sessionID, seq)) {
			t.Fatalf("MetaKey(%q) collided with ChunkKey(%q, %d)", sessionID, sessionID, seq)
		}
	}
}

func TestIsTableKey(t *testing.T) {
	cases := []struct {
		name string
		key  []byte
		want bool
	}{
		{"meta key", MetaKey("sess1"), true},
		{"chunk key", ChunkKey("sess1", 0), true},
		{"tenant index key", TenantIndexKey("acme", "sess1"), true},
		{"table prefix itself", []byte(TablePrefix), true},
		{"ordinary user key", []byte("sess1"), false},
		{"empty key", []byte(""), false},
		{"a different reserved table", []byte("\x00qrdb.lease/seq"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTableKey(tc.key); got != tc.want {
				t.Errorf("IsTableKey(%q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

func TestTenantIndexKey(t *testing.T) {
	k := TenantIndexKey("acme", "sess1")
	if !strings.HasPrefix(string(k), TablePrefix) {
		t.Errorf("TenantIndexKey(%q, %q) = %q, want it to start with TablePrefix", "acme", "sess1", k)
	}
	if !strings.Contains(string(k), "acme") || !strings.Contains(string(k), "sess1") {
		t.Errorf("TenantIndexKey(%q, %q) = %q, want it to contain both the tenant and the session id", "acme", "sess1", k)
	}
}

func TestTenantIndexScanPrefix_MatchesExactlyItsOwnTenant(t *testing.T) {
	acmePrefix := string(TenantIndexScanPrefix("acme"))
	acmeSession := string(TenantIndexKey("acme", "sess1"))
	globexSession := string(TenantIndexKey("globex", "sess1"))
	// A tenant ID prefixing another's ("acme-eu" starting with "acme") must
	// not be matched by "acme"'s scan prefix -- the trailing "/" in both
	// TenantIndexKey and TenantIndexScanPrefix is what prevents that.
	acmeEuSession := string(TenantIndexKey("acme-eu", "sess1"))

	if !strings.HasPrefix(acmeSession, acmePrefix) {
		t.Errorf("acme's own session key %q does not start with its scan prefix %q", acmeSession, acmePrefix)
	}
	if strings.HasPrefix(globexSession, acmePrefix) {
		t.Errorf("globex's session key %q unexpectedly starts with acme's scan prefix %q", globexSession, acmePrefix)
	}
	if strings.HasPrefix(acmeEuSession, acmePrefix) {
		t.Errorf("acme-eu's session key %q unexpectedly starts with acme's scan prefix %q (tenant-prefix collision)", acmeEuSession, acmePrefix)
	}
}
