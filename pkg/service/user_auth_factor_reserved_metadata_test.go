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

// Tests for the server-owned factor-metadata namespace (go-qrdb#234).
//
// The property under test is not "a key is rejected" but "a caller cannot
// supply the input that an authentication policy trusts". So the assertions
// are: the call fails, the error names the key and the prefix, and -- the one
// that matters most -- NOTHING WAS PERSISTED. A rejection that still wrote the
// record would defeat the whole control while returning an error.
package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

func reservedMetadataFactor(id string, userID uint64, key []byte, md map[string]string) *auth.AuthFactor {
	f := mtlsFactor(id, userID, key)
	f.Metadata = md
	return f
}

// assertReservedRejected checks the error identifies the offending key and the
// reserved prefix, both of which an operator needs to tell a forged claim from
// a naming collision.
func assertReservedRejected(t *testing.T, err error, wantKey string) {
	t.Helper()
	if err == nil {
		t.Fatal("call succeeded with a server-owned metadata key present")
	}
	var reserved *auth.ReservedMetadataKeyError
	if !errors.As(err, &reserved) {
		t.Fatalf("error = %v (%T), want *auth.ReservedMetadataKeyError", err, err)
	}
	if !errors.Is(err, auth.ErrReservedMetadata) {
		t.Error("error does not satisfy errors.Is(err, auth.ErrReservedMetadata); transports match on the sentinel")
	}
	if reserved.Key != wantKey {
		t.Errorf("error names key %q, want %q", reserved.Key, wantKey)
	}
	if reserved.Prefix != auth.ReservedMetadataPrefix {
		t.Errorf("error names prefix %q, want %q", reserved.Prefix, auth.ReservedMetadataPrefix)
	}
}

// TestAdd_RefusesReservedMetadata covers the keys named in the issue plus the
// case the prefix exists for: a field that does not exist yet.
func TestAdd_RefusesReservedMetadata(t *testing.T) {
	for _, key := range []string{
		"attest_level",
		"attest_aaguid",
		"attest_something_invented_next_quarter",
		"Attest_Level", // case must not be a way around it
		"ATTEST_LEVEL",
	} {
		t.Run(key, func(t *testing.T) {
			svc, kv := newUserAuthFactorService(t, nil)
			f := reservedMetadataFactor("f1", 7, []byte("cred-"+key), map[string]string{key: "hardware"})

			err := svc.Add(context.Background(), 7, f)
			assertReservedRejected(t, err, key)

			// The refusal must leave no record. A stored factor carrying the
			// forged claim would defeat the control even though the call
			// returned an error.
			if got, _ := svc.List(context.Background(), 7); len(got) != 0 {
				t.Fatalf("List returned %d factors after a refused Add; nothing may be persisted", len(got))
			}
			if n := len(kv.store); n != 0 {
				t.Fatalf("%d KV entries after a refused Add, want 0", n)
			}
		})
	}
}

// TestRotate_RefusesReservedMetadata: rotation is the other way a caller
// supplies metadata for a factor the server never verified. The pre-existing
// factor must also survive -- a refused rotation that revoked the old factor
// would be a denial of service on top of the forged claim.
func TestRotate_RefusesReservedMetadata(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, nil)
	ctx := context.Background()

	old := mtlsFactor("old", 7, []byte("cred-old"))
	if err := svc.Add(ctx, 7, old); err != nil {
		t.Fatalf("seed Add: %v", err)
	}

	newFactor := reservedMetadataFactor("new", 7, []byte("cred-new"),
		map[string]string{"attest_level": "hardware"})
	assertReservedRejected(t, svc.Rotate(ctx, "old", newFactor), "attest_level")

	got, err := svc.List(ctx, 7)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != "old" {
		t.Fatalf("factors after refused Rotate = %d, want the single original", len(got))
	}
	if got[0].RevokedAt != nil {
		t.Fatal("a refused Rotate revoked the existing factor")
	}
}

// TestAdd_AllowsOrdinaryMetadata is the control. The rule must be a namespace,
// not a ban on metadata: the domain type documents vendor / AAGUID / cert
// serial / slot info as legitimate, and a key that merely CONTAINS the prefix
// away from the start is not in the namespace.
func TestAdd_AllowsOrdinaryMetadata(t *testing.T) {
	svc, _ := newUserAuthFactorService(t, nil)
	f := reservedMetadataFactor("f1", 7, []byte("cred"), map[string]string{
		"vendor":         "yubico",
		"slot":           "9a",
		"public_key_pem": "-----BEGIN PUBLIC KEY-----",
		"self_attest_ok": "no", // contains "attest_", not as a prefix
	})

	if err := svc.Add(context.Background(), 7, f); err != nil {
		t.Fatalf("Add with ordinary metadata: %v", err)
	}
	got, err := svc.List(context.Background(), 7)
	if err != nil || len(got) != 1 {
		t.Fatalf("List = %d factors, %v; want 1, nil", len(got), err)
	}
	if got[0].Metadata["vendor"] != "yubico" {
		t.Errorf("ordinary metadata not persisted: %v", got[0].Metadata)
	}
}

// TestReservedPrefix_Boundary pins exactly where the namespace starts and
// stops, in both directions, because a later refactor can widen or narrow it
// silently.
//
// Note "attestation_root": it is NOT reserved, because "attestation_"[:7] is
// "attesta", not "attest_". The namespace is the literal prefix "attest_" and
// nothing else. That is a real limit and it is stated here rather than left to
// be discovered: a future field named attestation_* would need either the
// "attest_" spelling or a second reserved prefix.
func TestReservedPrefix_Boundary(t *testing.T) {
	reserved := []string{"attest_", "attest_level", "attest_x", "ATTEST_LEVEL", "Attest_Level"}
	allowed := []string{"attest", "attesta", "attestation_root", "self_attest_ok", "vendor", ""}

	for _, k := range reserved {
		if err := auth.ValidateMetadata(map[string]string{k: "x"}); err == nil {
			t.Errorf("key %q is inside the reserved namespace and must be refused", k)
		}
	}
	for _, k := range allowed {
		if err := auth.ValidateMetadata(map[string]string{k: "x"}); err != nil {
			t.Errorf("key %q is outside the reserved namespace and must be allowed; got %v", k, err)
		}
	}
	if err := auth.ValidateMetadata(nil); err != nil {
		t.Errorf("nil metadata must be allowed; got %v", err)
	}
}
