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

package forward_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
)

var carryGrant = delegation.Grant{Tenant: "acme", Principal: "carry-test", Scope: []string{"kv:get"}}

// TestCarry_CopiesWhatTheLeaderMustReverify: a holder carries its profile, a
// delegated caller its delegation certificate, an ordinary caller nothing.
func TestCarry_CopiesWhatTheLeaderMustReverify(t *testing.T) {
	d := authtest.NewDelegation(t, carryGrant)
	holder := &auth.Identity{Subject: "alice", Metadata: map[string]string{auth.HolderProfileMetadataKey: "custodian"}}
	for name, tc := range map[string]struct {
		caller                 *auth.Identity
		wantProfile, wantDeleg string
	}{
		"nil":       {},
		"ordinary":  {caller: &auth.Identity{Subject: "alice", TenantID: "acme"}},
		"holder":    {caller: holder, wantProfile: "custodian"},
		"delegated": {caller: d.Identity(t), wantDeleg: base64.StdEncoding.EncodeToString(d.Certificate.Raw)},
	} {
		t.Run(name, func(t *testing.T) {
			var a forward.CallerAssertion
			a.Carry(tc.caller)
			if a.HolderProfile != tc.wantProfile || a.Delegation != tc.wantDeleg {
				t.Fatalf("carried profile %q, delegation %q; want %q, %q", a.HolderProfile, a.Delegation, tc.wantProfile, tc.wantDeleg)
			}
		})
	}
}

// TestSignCaller_SignsTheDelegation: the signed assertion carries the
// delegation certificate byte for byte, and an ordinary caller's carries no
// delegation field at all.
func TestSignCaller_SignsTheDelegation(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	d := authtest.NewDelegation(t, carryGrant)
	b := forward.RPCBinding("quic", "kv.Get", []byte("payload"))

	ab, _, err := forward.SignCaller(d.Identity(t), b, key, "node-1", time.Now())
	if err != nil {
		t.Fatalf("SignCaller: %v", err)
	}
	raw := decodeAssertion(t, ab)
	var got forward.CallerAssertion
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	der, err := base64.StdEncoding.DecodeString(got.Delegation)
	if err != nil || !bytes.Equal(der, d.Certificate.Raw) {
		t.Fatalf("signed delegation does not decode to the certificate (err %v)", err)
	}

	ab, _, err = forward.SignCaller(&auth.Identity{Subject: "alice", TenantID: "acme"}, b, key, "node-1", time.Now())
	if err != nil {
		t.Fatalf("SignCaller: %v", err)
	}
	if raw := decodeAssertion(t, ab); strings.Contains(string(raw), `"delegation"`) {
		t.Fatalf("ordinary assertion %s carries a delegation field", raw)
	}
}

func decodeAssertion(t *testing.T, ab string) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(ab)
	if err != nil {
		t.Fatalf("decode assertion: %v", err)
	}
	return raw
}
