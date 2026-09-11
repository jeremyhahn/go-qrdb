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
			svc, kv := newTestUserAuthFactorService(t, nil)
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
	svc, _ := newTestUserAuthFactorService(t, nil)
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
	svc, _ := newTestUserAuthFactorService(t, nil)
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

// ---------------------------------------------------------------------------
// go-qrdb#342: the server stamps what was proved; the trusted path records it.
// ---------------------------------------------------------------------------

// attestedFactor reads a factor back through every read path and returns its
// metadata, so a test asserts on what is PERSISTED rather than on the value
// the service left in the caller's struct.
func persistedMetadata(t *testing.T, svc UserAuthFactorServicer, factorID string) map[string]string {
	t.Helper()
	got, err := svc.Get(context.Background(), factorID)
	if err != nil {
		t.Fatalf("Get(%s): %v", factorID, err)
	}
	return got.Metadata
}

// TestAdd_StampsUnattested: an ordinary write proved nothing, and the server
// says so rather than leaving the field absent -- absent is what a record
// written before the field existed looks like.
func TestAdd_StampsUnattested(t *testing.T) {
	t.Run("nil metadata", func(t *testing.T) {
		svc, _ := newTestUserAuthFactorService(t, nil)
		if err := svc.Add(context.Background(), 7, mtlsFactor("f1", 7, []byte("cred"))); err != nil {
			t.Fatalf("Add: %v", err)
		}
		md := persistedMetadata(t, svc, "f1")
		if md[auth.MetadataAttestLevel] != string(auth.AttestationLevelNone) {
			t.Errorf("attest_level = %q, want none: %v", md[auth.MetadataAttestLevel], md)
		}
		if _, ok := md[auth.MetadataAttestAAGUID]; ok {
			t.Errorf("attest_aaguid present on an unattested factor: %v", md)
		}
	})

	t.Run("ordinary metadata is kept beside the stamp", func(t *testing.T) {
		svc, _ := newTestUserAuthFactorService(t, nil)
		f := reservedMetadataFactor("f1", 7, []byte("cred"), map[string]string{"vendor": "yubico"})
		if err := svc.Add(context.Background(), 7, f); err != nil {
			t.Fatalf("Add: %v", err)
		}
		md := persistedMetadata(t, svc, "f1")
		if md["vendor"] != "yubico" || md[auth.MetadataAttestLevel] != "none" {
			t.Errorf("metadata = %v, want vendor kept and attest_level=none", md)
		}
	})
}

// TestRotate_StampsUnattested: rotation is a new factor the server never
// verified, so it is stamped none too.
func TestRotate_StampsUnattested(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, nil)
	ctx := context.Background()
	if err := svc.Add(ctx, 7, mtlsFactor("old", 7, []byte("cred-old"))); err != nil {
		t.Fatalf("seed Add: %v", err)
	}
	if err := svc.Rotate(ctx, "old", mtlsFactor("new", 7, []byte("cred-new"))); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if md := persistedMetadata(t, svc, "new"); md[auth.MetadataAttestLevel] != "none" {
		t.Errorf("rotated factor attest_level = %q, want none", md[auth.MetadataAttestLevel])
	}
}

// TestRotate_DoesNotInheritAttestation is the downgrade check: a factor the
// ceremony proved hardware, rotated through the ordinary path, comes out
// none. The old record's proof is about the OLD key.
func TestRotate_DoesNotInheritAttestation(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, nil)
	rec := svc.(AttestationRecorder)
	ctx := context.Background()

	hardware := auth.VerifiedAttestation{Level: auth.AttestationLevelHardware, AAGUID: "ee882879"}
	if err := rec.AddAttested(ctx, 7, fido2Factor("old", 7, []byte("cred-old")), hardware); err != nil {
		t.Fatalf("AddAttested: %v", err)
	}
	if err := svc.Rotate(ctx, "old", fido2Factor("new", 7, []byte("cred-new"))); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	md := persistedMetadata(t, svc, "new")
	if md[auth.MetadataAttestLevel] != "none" {
		t.Errorf("rotated factor inherited attest_level=%q", md[auth.MetadataAttestLevel])
	}
	if _, ok := md[auth.MetadataAttestAAGUID]; ok {
		t.Errorf("rotated factor inherited attest_aaguid: %v", md)
	}
}

// TestAttestationRecorder_ReachableFromTheServicer: the trusted path is
// reached by type assertion on the servicer a host already holds, the same
// way ActiveFactorChecker is.
func TestAttestationRecorder_ReachableFromTheServicer(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, nil)
	if _, ok := svc.(AttestationRecorder); !ok {
		t.Fatalf("%T does not implement AttestationRecorder", svc)
	}
}

// TestAddAttested_RecordsWhatTheCeremonyProved is the positive half of #342:
// the host that verified the attestation can record it, and every read path
// -- Get, List, and the login resolver's LookupByCredentialKey -- sees it.
func TestAddAttested_RecordsWhatTheCeremonyProved(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, nil)
	rec := svc.(AttestationRecorder)
	ctx := context.Background()

	f := reservedMetadataFactor("f1", 7, []byte("cred"), map[string]string{"transports": "usb"})
	att := auth.VerifiedAttestation{Level: auth.AttestationLevelHardware, AAGUID: "ee882879721c491397753dfcce97072a"}
	if err := rec.AddAttested(ctx, 7, f, att); err != nil {
		t.Fatalf("AddAttested: %v", err)
	}

	check := func(name string, md map[string]string) {
		t.Helper()
		if md[auth.MetadataAttestLevel] != "hardware" {
			t.Errorf("%s: attest_level = %q, want hardware", name, md[auth.MetadataAttestLevel])
		}
		if md[auth.MetadataAttestAAGUID] != att.AAGUID {
			t.Errorf("%s: attest_aaguid = %q, want %q", name, md[auth.MetadataAttestAAGUID], att.AAGUID)
		}
		if md["transports"] != "usb" {
			t.Errorf("%s: ordinary metadata lost: %v", name, md)
		}
	}
	check("Get", persistedMetadata(t, svc, "f1"))

	listed, err := svc.List(ctx, 7)
	if err != nil || len(listed) != 1 {
		t.Fatalf("List = %d, %v", len(listed), err)
	}
	check("List", listed[0].Metadata)

	byKey, err := svc.LookupByCredentialKey(ctx, auth.AuthFactorMTLS, []byte("cred"))
	if err != nil {
		t.Fatalf("LookupByCredentialKey: %v", err)
	}
	check("LookupByCredentialKey", byKey.Metadata)
}

// TestAddAttested_SoftwareWithoutModel: a level with no attested model records
// the level and no aaguid key at all.
func TestAddAttested_SoftwareWithoutModel(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, nil)
	rec := svc.(AttestationRecorder)
	err := rec.AddAttested(context.Background(), 7, mtlsFactor("f1", 7, []byte("cred")),
		auth.VerifiedAttestation{Level: auth.AttestationLevelSoftware})
	if err != nil {
		t.Fatalf("AddAttested: %v", err)
	}
	md := persistedMetadata(t, svc, "f1")
	if md[auth.MetadataAttestLevel] != "software" {
		t.Errorf("attest_level = %q", md[auth.MetadataAttestLevel])
	}
	if _, ok := md[auth.MetadataAttestAAGUID]; ok {
		t.Errorf("attest_aaguid present with no model: %v", md)
	}
}

// TestAddAttested_RefusesCallerSuppliedReservedKeys: the trusted path is not
// a second way to hand the server a map with attest_* in it. The typed
// attestation is the ONLY input, and #234's refusal holds on this path too.
func TestAddAttested_RefusesCallerSuppliedReservedKeys(t *testing.T) {
	svc, kv := newTestUserAuthFactorService(t, nil)
	rec := svc.(AttestationRecorder)

	f := reservedMetadataFactor("f1", 7, []byte("cred"), map[string]string{"attest_level": "hardware"})
	err := rec.AddAttested(context.Background(), 7, f, auth.VerifiedAttestation{Level: auth.AttestationLevelHardware})
	assertReservedRejected(t, err, "attest_level")
	if n := len(kv.store); n != 0 {
		t.Fatalf("%d KV entries after a refused AddAttested, want 0", n)
	}
}

// TestAddAttested_RefusesUndefinedLevel: an unset or misspelled level is
// refused before anything is written. Stored, it would fail login policy
// silently.
func TestAddAttested_RefusesUndefinedLevel(t *testing.T) {
	for _, level := range []auth.AttestationLevel{"", "hardwre", "HARDWARE"} {
		t.Run(string(level), func(t *testing.T) {
			svc, kv := newTestUserAuthFactorService(t, nil)
			rec := svc.(AttestationRecorder)

			err := rec.AddAttested(context.Background(), 7, mtlsFactor("f1", 7, []byte("cred")),
				auth.VerifiedAttestation{Level: level})
			var invalid *auth.InvalidAttestationError
			if !errors.As(err, &invalid) {
				t.Fatalf("error = %v (%T), want *auth.InvalidAttestationError", err, err)
			}
			if !errors.Is(err, auth.ErrInvalidAttestation) {
				t.Error("errors.Is(err, auth.ErrInvalidAttestation) is false")
			}
			if n := len(kv.store); n != 0 {
				t.Fatalf("%d KV entries after a refused AddAttested, want 0", n)
			}
		})
	}
}

// TestAddAttested_SharesAddValidation: the trusted path is Add with a
// different stamp, not a looser Add. The checks that precede the stamp are
// the same ones.
func TestAddAttested_SharesAddValidation(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, nil)
	rec := svc.(AttestationRecorder)
	ctx := context.Background()
	hardware := auth.VerifiedAttestation{Level: auth.AttestationLevelHardware}

	if err := rec.AddAttested(ctx, 7, nil, hardware); !errors.Is(err, auth.ErrInvalidFactor) {
		t.Errorf("nil factor: %v, want ErrInvalidFactor", err)
	}
	if err := rec.AddAttested(ctx, 0, mtlsFactor("f1", 0, []byte("cred")), hardware); !errors.Is(err, auth.ErrInvalidFactor) {
		t.Errorf("zero userID: %v, want ErrInvalidFactor", err)
	}
	if err := rec.AddAttested(ctx, 7, mtlsFactor("f1", 7, []byte("cred")), hardware); err != nil {
		t.Fatalf("first AddAttested: %v", err)
	}
	if err := rec.AddAttested(ctx, 7, mtlsFactor("f2", 7, []byte("cred")), hardware); !errors.Is(err, auth.ErrFactorAlreadyExists) {
		t.Errorf("duplicate credential: %v, want ErrFactorAlreadyExists", err)
	}
}

// TestRotateAttested_RecordsWhatTheCeremonyProved: the rotation twin.
func TestRotateAttested_RecordsWhatTheCeremonyProved(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, nil)
	rec := svc.(AttestationRecorder)
	ctx := context.Background()

	if err := svc.Add(ctx, 7, fido2Factor("old", 7, []byte("cred-old"))); err != nil {
		t.Fatalf("seed Add: %v", err)
	}
	att := auth.VerifiedAttestation{Level: auth.AttestationLevelHardware, AAGUID: "ee882879"}
	if err := rec.RotateAttested(ctx, "old", fido2Factor("new", 7, []byte("cred-new")), att); err != nil {
		t.Fatalf("RotateAttested: %v", err)
	}

	md := persistedMetadata(t, svc, "new")
	if md[auth.MetadataAttestLevel] != "hardware" || md[auth.MetadataAttestAAGUID] != "ee882879" {
		t.Errorf("rotated factor metadata = %v", md)
	}
	old, err := svc.Get(ctx, "old")
	if err != nil {
		t.Fatalf("Get old: %v", err)
	}
	if old.RevokedAt == nil {
		t.Error("RotateAttested did not revoke the old factor")
	}
}

// TestRotateAttested_RefusesUndefinedLevel: refused before the old factor is
// touched -- a refused rotation that revoked the old factor would be a denial
// of service on top of the bad input.
func TestRotateAttested_RefusesUndefinedLevel(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, nil)
	rec := svc.(AttestationRecorder)
	ctx := context.Background()

	if err := svc.Add(ctx, 7, mtlsFactor("old", 7, []byte("cred-old"))); err != nil {
		t.Fatalf("seed Add: %v", err)
	}
	err := rec.RotateAttested(ctx, "old", mtlsFactor("new", 7, []byte("cred-new")),
		auth.VerifiedAttestation{Level: "hardwre"})
	if !errors.Is(err, auth.ErrInvalidAttestation) {
		t.Fatalf("error = %v, want ErrInvalidAttestation", err)
	}
	got, err := svc.List(ctx, 7)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != "old" || got[0].RevokedAt != nil {
		t.Fatalf("a refused RotateAttested changed the factor set: %+v", got)
	}
}

// TestRotateAttested_RefusesCallerSuppliedReservedKeys mirrors the Add case.
func TestRotateAttested_RefusesCallerSuppliedReservedKeys(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, nil)
	rec := svc.(AttestationRecorder)
	ctx := context.Background()

	if err := svc.Add(ctx, 7, mtlsFactor("old", 7, []byte("cred-old"))); err != nil {
		t.Fatalf("seed Add: %v", err)
	}
	newFactor := reservedMetadataFactor("new", 7, []byte("cred-new"), map[string]string{"attest_aaguid": "x"})
	err := rec.RotateAttested(ctx, "old", newFactor, auth.VerifiedAttestation{Level: auth.AttestationLevelHardware})
	assertReservedRejected(t, err, "attest_aaguid")
	if old, err := svc.Get(ctx, "old"); err != nil || old.RevokedAt != nil {
		t.Fatalf("old factor after refused RotateAttested: %+v, %v", old, err)
	}
}
