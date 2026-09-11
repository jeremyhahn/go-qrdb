// Copyright 2026 Jeremy Hahn
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

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// spkiMtlsFactor builds an mtls factor carrying both the legacy
// SHA-256(cert) credential and the renewal-stable SPKI credential.
func spkiMtlsFactor(id string, userID uint64, cred, spki []byte) *auth.AuthFactor {
	f := mtlsFactor(id, userID, cred)
	f.SPKICredential = spki
	return f
}

// ---------------------------------------------------------------------------
// Add — SPKI lookup index
// ---------------------------------------------------------------------------

func TestAdd_writesSPKILookupIndex(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, nil)
	ctx := context.Background()

	spki := []byte("spki-digest-1")
	if err := svc.Add(ctx, 1, spkiMtlsFactor("f1", 1, []byte("cert-digest-1"), spki)); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Resolves through the SPKI key...
	f, err := svc.LookupByCredentialKey(ctx, auth.AuthFactorMTLS, spki)
	if err != nil {
		t.Fatalf("LookupByCredentialKey(spki): %v", err)
	}
	if f.ID != "f1" {
		t.Fatalf("SPKI lookup resolved %q, want f1", f.ID)
	}

	// ...and still through the legacy credential key.
	f, err = svc.LookupByCredentialKey(ctx, auth.AuthFactorMTLS, []byte("cert-digest-1"))
	if err != nil {
		t.Fatalf("LookupByCredentialKey(credential): %v", err)
	}
	if f.ID != "f1" {
		t.Fatalf("credential lookup resolved %q, want f1", f.ID)
	}
}

func TestAdd_withoutSPKICredential_noSPKIIndex(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, nil)
	ctx := context.Background()

	if err := svc.Add(ctx, 1, mtlsFactor("f1", 1, []byte("cert-digest-1"))); err != nil {
		t.Fatalf("Add: %v", err)
	}

	_, err := svc.LookupByCredentialKey(ctx, auth.AuthFactorMTLS, []byte("spki-digest-1"))
	var notFound *auth.FactorNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected FactorNotFoundError for absent SPKI index, got %v", err)
	}
}

func TestAdd_spkiCollisionRejected(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, nil)
	ctx := context.Background()

	spki := []byte("spki-digest-shared")
	if err := svc.Add(ctx, 1, spkiMtlsFactor("f1", 1, []byte("cert-digest-1"), spki)); err != nil {
		t.Fatalf("Add f1: %v", err)
	}

	err := svc.Add(ctx, 2, spkiMtlsFactor("f2", 2, []byte("cert-digest-2"), spki))
	var exists *auth.FactorAlreadyExistsError
	if !errors.As(err, &exists) {
		t.Fatalf("expected FactorAlreadyExistsError, got %v", err)
	}
	if exists.FactorID != "f1" {
		t.Fatalf("collision reported factor %q, want f1", exists.FactorID)
	}
}

func TestAdd_spkiCollisionRejectedEvenWhenRevoked(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, nil)
	ctx := context.Background()

	spki := []byte("spki-digest-shared")
	if err := svc.Add(ctx, 1, spkiMtlsFactor("f1", 1, []byte("cert-digest-1"), spki)); err != nil {
		t.Fatalf("Add f1: %v", err)
	}
	// Add a second active factor so Remove does not trip the last-factor
	// guard, then soft-delete f1.
	if err := svc.Add(ctx, 1, fido2Factor("f-keep", 1, []byte("fido2-cred"))); err != nil {
		t.Fatalf("Add f-keep: %v", err)
	}
	if err := svc.Remove(ctx, "f1", false); err != nil {
		t.Fatalf("Remove f1: %v", err)
	}

	// Index entries are not cleaned on soft-delete; the collision check
	// mirrors the pre-existing Credential behaviour.
	err := svc.Add(ctx, 2, spkiMtlsFactor("f2", 2, []byte("cert-digest-2"), spki))
	var exists *auth.FactorAlreadyExistsError
	if !errors.As(err, &exists) {
		t.Fatalf("expected FactorAlreadyExistsError against revoked factor, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Rotate — renewal with the same keypair
// ---------------------------------------------------------------------------

func TestRotate_sameSPKIRenewalRepointsIndex(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, nil)
	ctx := context.Background()

	spki := []byte("spki-digest-stable")
	if err := svc.Add(ctx, 1, spkiMtlsFactor("f1", 1, []byte("cert-digest-old"), spki)); err != nil {
		t.Fatalf("Add f1: %v", err)
	}

	// Renewal: new certificate (new Credential), same keypair (same SPKI).
	renewed := spkiMtlsFactor("f2", 1, []byte("cert-digest-new"), spki)
	if err := svc.Rotate(ctx, "f1", renewed); err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	// The SPKI index now points at the new factor — login with the renewed
	// certificate keeps working through the renewal-stable key.
	f, err := svc.LookupByCredentialKey(ctx, auth.AuthFactorMTLS, spki)
	if err != nil {
		t.Fatalf("LookupByCredentialKey(spki) after rotate: %v", err)
	}
	if f.ID != "f2" {
		t.Fatalf("SPKI lookup resolved %q after rotate, want f2", f.ID)
	}

	// The new credential resolves too.
	f, err = svc.LookupByCredentialKey(ctx, auth.AuthFactorMTLS, []byte("cert-digest-new"))
	if err != nil {
		t.Fatalf("LookupByCredentialKey(new credential): %v", err)
	}
	if f.ID != "f2" {
		t.Fatalf("new credential resolved %q, want f2", f.ID)
	}

	// The old credential's index still points at the revoked old factor.
	_, err = svc.LookupByCredentialKey(ctx, auth.AuthFactorMTLS, []byte("cert-digest-old"))
	var revoked *auth.FactorRevokedError
	if !errors.As(err, &revoked) {
		t.Fatalf("expected FactorRevokedError via old credential, got %v", err)
	}
}

func TestRotate_spkiCollisionWithOtherFactorRejected(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, nil)
	ctx := context.Background()

	if err := svc.Add(ctx, 1, spkiMtlsFactor("f1", 1, []byte("cert-digest-1"), []byte("spki-a"))); err != nil {
		t.Fatalf("Add f1: %v", err)
	}
	if err := svc.Add(ctx, 1, spkiMtlsFactor("f2", 1, []byte("cert-digest-2"), []byte("spki-b"))); err != nil {
		t.Fatalf("Add f2: %v", err)
	}

	// Rotating f1 onto f2's SPKI must be rejected.
	err := svc.Rotate(ctx, "f1", spkiMtlsFactor("f3", 1, []byte("cert-digest-3"), []byte("spki-b")))
	var exists *auth.FactorAlreadyExistsError
	if !errors.As(err, &exists) {
		t.Fatalf("expected FactorAlreadyExistsError, got %v", err)
	}
	if exists.FactorID != "f2" {
		t.Fatalf("collision reported factor %q, want f2", exists.FactorID)
	}
}

// ---------------------------------------------------------------------------
// Persistence round trip
// ---------------------------------------------------------------------------

func TestGet_roundTripsSPKICredential(t *testing.T) {
	svc, _ := newTestUserAuthFactorService(t, nil)
	ctx := context.Background()

	spki := []byte("spki-digest-1")
	if err := svc.Add(ctx, 1, spkiMtlsFactor("f1", 1, []byte("cert-digest-1"), spki)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	f, err := svc.Get(ctx, "f1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(f.SPKICredential) != string(spki) {
		t.Fatalf("SPKICredential round trip mismatch: got %q", f.SPKICredential)
	}
}
