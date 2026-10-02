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

package forward

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestVerify_HolderProfileRoundTrips: the profile is signed with the rest of
// the assertion and comes back from Verify.
func TestVerify_HolderProfileRoundTrips(t *testing.T) {
	cert, key := nodeCert(t, tdom, nodeID)
	now := time.Now()
	body := []byte(`{"v":1}`)
	a := NewCallerAssertion("admin", "system", method, path, BodyDigest(body), nodeID, now, DefaultTTL)
	a.HolderProfile = "custodian"
	ab, sb, err := Sign(a, key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	got, err := Verify(ab, sb, cert, tdom, now, method, path, BodyDigest(body))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.HolderProfile != "custodian" {
		t.Fatalf("HolderProfile = %q, want custodian", got.HolderProfile)
	}
}

// TestVerify_HolderProfileIsSigned: the profile is covered by the signature.
// Stripping it -- which would turn a holder into the account of the same name
// on the leader -- or changing it fails verification.
func TestVerify_HolderProfileIsSigned(t *testing.T) {
	cert, key := nodeCert(t, tdom, nodeID)
	now := time.Now()
	body := []byte(`{"v":1}`)
	a := NewCallerAssertion("admin", "system", method, path, BodyDigest(body), nodeID, now, DefaultTTL)
	a.HolderProfile = "custodian"
	ab, sb, err := Sign(a, key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(ab)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	for name, tampered := range map[string]string{
		"stripped": strings.Replace(string(raw), `,"holder_profile":"custodian"`, "", 1),
		"changed":  strings.Replace(string(raw), `"holder_profile":"custodian"`, `"holder_profile":"security-officer"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if tampered == string(raw) {
				t.Fatalf("fixture: the assertion %s was not tampered with", raw)
			}
			_, err := Verify(base64.StdEncoding.EncodeToString([]byte(tampered)), sb, cert, tdom, now, method, path, BodyDigest(body))
			var sigErr *ForwardAssertionSignatureError
			if !errors.As(err, &sigErr) {
				t.Fatalf("Verify of a %s holder profile: err = %v, want *ForwardAssertionSignatureError", name, err)
			}
		})
	}
}

// TestSign_UserAssertionCarriesNoHolderField: a user's assertion is
// byte-identical to the form before the field existed, so nodes on either
// side of the change agree on it.
func TestSign_UserAssertionCarriesNoHolderField(t *testing.T) {
	_, key := nodeCert(t, tdom, nodeID)
	a := NewCallerAssertion("alice", "acme", method, path, BodyDigest(nil), nodeID, time.Now(), DefaultTTL)
	ab, _, err := Sign(a, key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(ab)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if strings.Contains(string(raw), "holder_profile") {
		t.Fatalf("user assertion %s carries a holder field", raw)
	}
}
