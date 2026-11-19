// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package credentials

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

// =============================================================================
// Helpers
// =============================================================================

func newManualService(t *testing.T) *CredentialService {
	t.Helper()
	svc, err := NewCredentialService(&Config{Strategy: StrategyManual}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewCredentialService: %v", err)
	}
	return svc
}

func newBarrierService(t *testing.T) *CredentialService {
	t.Helper()
	barrier := newTestBarrier(t)
	t.Cleanup(func() {
		_ = barrier.Close()
	})
	svc, err := NewCredentialService(&Config{Strategy: StrategyBarrier}, barrier, testLogger())
	if err != nil {
		t.Fatalf("NewCredentialService: %v", err)
	}
	return svc
}

// expireGrace forces every history entry in the manual meta store to have an
// already-elapsed grace deadline. Use this in place of time.Sleep so tests
// stay synchronous and fast.
func expireGrace(svc *CredentialService, name string) {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	meta := svc.manualMeta[name]
	if meta == nil {
		return
	}
	past := time.Now().Add(-time.Hour)
	for i := range meta.History {
		meta.History[i].GraceUntil = past
	}
}

// expireBarrierGrace rewrites the on-disk meta for name so every grace entry
// is already in the past. Avoids time.Sleep in barrier tests.
func expireBarrierGrace(t *testing.T, svc *CredentialService, name string) {
	t.Helper()
	ctx := context.Background()
	meta, err := svc.loadBarrierMeta(ctx, name)
	if err != nil {
		t.Fatalf("loadBarrierMeta: %v", err)
	}
	past := time.Now().Add(-time.Hour)
	for i := range meta.History {
		meta.History[i].GraceUntil = past
	}
	if err := svc.writeBarrierMeta(ctx, name, meta); err != nil {
		t.Fatalf("writeBarrierMeta: %v", err)
	}
}

// =============================================================================
// ListCredentials
// =============================================================================

func TestListCredentials_ManualEmpty(t *testing.T) {
	svc := newManualService(t)
	names, err := svc.ListCredentials(context.Background())
	if err != nil {
		t.Fatalf("ListCredentials: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("expected empty list; got %v", names)
	}
}

func TestListCredentials_ManualSorted(t *testing.T) {
	svc := newManualService(t)
	ctx := context.Background()
	for _, name := range []string{"zeta", "alpha", "mu"} {
		if err := svc.StoreCredential(ctx, name, []byte("v")); err != nil {
			t.Fatalf("StoreCredential(%q): %v", name, err)
		}
	}
	names, err := svc.ListCredentials(ctx)
	if err != nil {
		t.Fatalf("ListCredentials: %v", err)
	}
	want := []string{"alpha", "mu", "zeta"}
	if len(names) != len(want) {
		t.Fatalf("len = %d, want %d", len(names), len(want))
	}
	for i, n := range want {
		if names[i] != n {
			t.Fatalf("names[%d] = %q, want %q", i, names[i], n)
		}
	}
}

func TestListCredentials_InvalidStrategy(t *testing.T) {
	svc := newManualService(t)
	svc.strategy = "bogus"
	_, err := svc.ListCredentials(context.Background())
	if !errors.Is(err, ErrInvalidStrategy) {
		t.Fatalf("err = %v, want ErrInvalidStrategy", err)
	}
}

// =============================================================================
// RotateCredential
// =============================================================================

func TestRotateCredential_FirstWriteIsVersion1(t *testing.T) {
	svc := newManualService(t)
	res, err := svc.RotateCredential(context.Background(), "k", RotateOptions{NewValue: []byte("v1")})
	if err != nil {
		t.Fatalf("RotateCredential: %v", err)
	}
	if res.PreviousVersion != 0 {
		t.Fatalf("PreviousVersion = %d, want 0", res.PreviousVersion)
	}
	if res.NewVersion != 1 {
		t.Fatalf("NewVersion = %d, want 1", res.NewVersion)
	}
	if !res.GraceUntil.IsZero() {
		t.Fatalf("GraceUntil should be zero when no previous version existed")
	}
}

func TestRotateCredential_SubsequentRotationIncrementsVersion(t *testing.T) {
	svc := newManualService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("old")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	res, err := svc.RotateCredential(ctx, "k", RotateOptions{
		NewValue:    []byte("new"),
		GracePeriod: time.Minute,
	})
	if err != nil {
		t.Fatalf("RotateCredential: %v", err)
	}
	if res.PreviousVersion != 1 {
		t.Fatalf("PreviousVersion = %d, want 1", res.PreviousVersion)
	}
	if res.NewVersion != 2 {
		t.Fatalf("NewVersion = %d, want 2", res.NewVersion)
	}
	if res.GraceUntil.IsZero() {
		t.Fatalf("GraceUntil must be set when GracePeriod > 0")
	}
	current, err := svc.retrieveBytes(ctx, "k")
	if err != nil {
		t.Fatalf("RetrieveCredential: %v", err)
	}
	if string(current) != "new" {
		t.Fatalf("current = %q, want %q", current, "new")
	}
}

func TestRotateCredential_ZeroGraceWipesPrevious(t *testing.T) {
	svc := newManualService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("old")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	res, err := svc.RotateCredential(ctx, "k", RotateOptions{NewValue: []byte("new")})
	if err != nil {
		t.Fatalf("RotateCredential: %v", err)
	}
	if res.NewVersion != 2 {
		t.Fatalf("NewVersion = %d, want 2", res.NewVersion)
	}
	if _, err := svc.retrieveVersionBytes(ctx, "k", 1); !errors.Is(err, ErrVersionNotFound) && !errors.Is(err, ErrVersionExpired) {
		t.Fatalf("RetrieveCredentialVersion(v1) after zero-grace rotation must refuse; got %v", err)
	}
}

func TestRotateCredential_NegativeGracePeriodRejected(t *testing.T) {
	svc := newManualService(t)
	_, err := svc.RotateCredential(context.Background(), "k", RotateOptions{
		NewValue:    []byte("v"),
		GracePeriod: -time.Second,
	})
	if !errors.Is(err, ErrInvalidGracePeriod) {
		t.Fatalf("err = %v, want ErrInvalidGracePeriod", err)
	}
}

func TestRotateCredential_EmptyValueRejected(t *testing.T) {
	svc := newManualService(t)
	_, err := svc.RotateCredential(context.Background(), "k", RotateOptions{NewValue: nil})
	if !errors.Is(err, ErrEmptyCredentialValue) {
		t.Fatalf("err = %v, want ErrEmptyCredentialValue", err)
	}
}

func TestRotateCredential_EmptyNameRejected(t *testing.T) {
	svc := newManualService(t)
	_, err := svc.RotateCredential(context.Background(), " ", RotateOptions{NewValue: []byte("v")})
	if !errors.Is(err, ErrEmptyCredentialName) {
		t.Fatalf("err = %v, want ErrEmptyCredentialName", err)
	}
}

func TestRotateCredential_InvalidStrategy(t *testing.T) {
	svc := newManualService(t)
	svc.strategy = "bogus"
	_, err := svc.RotateCredential(context.Background(), "k", RotateOptions{NewValue: []byte("v")})
	if !errors.Is(err, ErrInvalidStrategy) {
		t.Fatalf("err = %v, want ErrInvalidStrategy", err)
	}
}

// =============================================================================
// RetrieveCredentialVersion
// =============================================================================

func TestRetrieveCredentialVersion_Zero_AliasesCurrent(t *testing.T) {
	svc := newManualService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	got, err := svc.retrieveVersionBytes(ctx, "k", 0)
	if err != nil {
		t.Fatalf("RetrieveCredentialVersion(0): %v", err)
	}
	if string(got) != "v" {
		t.Fatalf("got %q, want %q", got, "v")
	}
}

func TestRetrieveCredentialVersion_GracedVersionRetrievable(t *testing.T) {
	svc := newManualService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("old")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	if _, err := svc.RotateCredential(ctx, "k", RotateOptions{
		NewValue:    []byte("new"),
		GracePeriod: time.Hour,
	}); err != nil {
		t.Fatalf("RotateCredential: %v", err)
	}
	got, err := svc.retrieveVersionBytes(ctx, "k", 1)
	if err != nil {
		t.Fatalf("RetrieveCredentialVersion(v1) during grace: %v", err)
	}
	if string(got) != "old" {
		t.Fatalf("graced v1 = %q, want %q", got, "old")
	}
}

func TestRetrieveCredentialVersion_ExpiredGraceRejected(t *testing.T) {
	svc := newManualService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("old")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	if _, err := svc.RotateCredential(ctx, "k", RotateOptions{
		NewValue:    []byte("new"),
		GracePeriod: time.Hour,
	}); err != nil {
		t.Fatalf("RotateCredential: %v", err)
	}
	expireGrace(svc, "k")
	_, err := svc.retrieveVersionBytes(ctx, "k", 1)
	if !errors.Is(err, ErrVersionExpired) {
		t.Fatalf("err = %v, want ErrVersionExpired", err)
	}
}

func TestRetrieveCredentialVersion_NonExistentVersion(t *testing.T) {
	svc := newManualService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	_, err := svc.retrieveVersionBytes(ctx, "k", 42)
	if !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("err = %v, want ErrVersionNotFound", err)
	}
}

func TestRetrieveCredentialVersion_LegacyNoMetaV1Returns(t *testing.T) {
	svc := newManualService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("legacy")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	got, err := svc.retrieveVersionBytes(ctx, "k", 1)
	if err != nil {
		t.Fatalf("RetrieveCredentialVersion(legacy v1): %v", err)
	}
	if string(got) != "legacy" {
		t.Fatalf("legacy v1 = %q, want %q", got, "legacy")
	}
}

func TestRetrieveCredentialVersion_LegacyMissingNameReturnsNotFound(t *testing.T) {
	svc := newManualService(t)
	_, err := svc.retrieveVersionBytes(context.Background(), "absent", 1)
	if !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("err = %v, want ErrCredentialNotFound", err)
	}
}

func TestRetrieveCredentialVersion_LegacyNoMetaNonV1Rejected(t *testing.T) {
	svc := newManualService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("legacy")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	_, err := svc.retrieveVersionBytes(ctx, "k", 7)
	if !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("err = %v, want ErrVersionNotFound", err)
	}
}

func TestRetrieveCredentialVersion_CurrentMissingValue(t *testing.T) {
	// Edge case: meta says CurrentVersion = X but manualCreds entry is gone
	// (cannot happen under normal API use but the code guards for it).
	svc := newManualService(t)
	svc.ensureManualVersionStore()
	svc.manualMeta["k"] = &credentialMeta{CurrentVersion: 1}
	_, err := svc.retrieveVersionBytes(context.Background(), "k", 1)
	if !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("err = %v, want ErrCredentialNotFound", err)
	}
}

func TestRetrieveCredentialVersion_HistoryWithoutStoredBytes(t *testing.T) {
	// Force a state where meta.History references a version but the value
	// store has dropped it (e.g. partial GC). Verifier must return
	// ErrVersionExpired.
	svc := newManualService(t)
	svc.ensureManualVersionStore()
	curSec, err := newSecretCopy(context.Background(), []byte("current"))
	if err != nil {
		t.Fatalf("newSecretCopy: %v", err)
	}
	svc.manualCreds["k"] = curSec
	svc.manualMeta["k"] = &credentialMeta{
		CurrentVersion: 2,
		History: []credentialMetaEntry{{
			Version:    1,
			GraceUntil: time.Now().Add(time.Hour),
		}},
	}
	_, err = svc.retrieveVersionBytes(context.Background(), "k", 1)
	if !errors.Is(err, ErrVersionExpired) {
		t.Fatalf("err = %v, want ErrVersionExpired", err)
	}
}

func TestRetrieveCredentialVersion_EmptyName(t *testing.T) {
	svc := newManualService(t)
	_, err := svc.retrieveVersionBytes(context.Background(), "", 1)
	if !errors.Is(err, ErrEmptyCredentialName) {
		t.Fatalf("err = %v, want ErrEmptyCredentialName", err)
	}
}

func TestRetrieveCredentialVersion_InvalidStrategy(t *testing.T) {
	svc := newManualService(t)
	svc.strategy = "bogus"
	_, err := svc.retrieveVersionBytes(context.Background(), "k", 1)
	if !errors.Is(err, ErrInvalidStrategy) {
		t.Fatalf("err = %v, want ErrInvalidStrategy", err)
	}
}

// =============================================================================
// ListCredentialVersions
// =============================================================================

func TestListCredentialVersions_LegacyUntrackedReturnsV1Only(t *testing.T) {
	svc := newManualService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	vs, err := svc.ListCredentialVersions(ctx, "k")
	if err != nil {
		t.Fatalf("ListCredentialVersions: %v", err)
	}
	if len(vs) != 1 || vs[0].Version != 1 || !vs[0].IsCurrent {
		t.Fatalf("vs = %+v, want one v1 marked current", vs)
	}
}

func TestListCredentialVersions_RotationsAddHistory(t *testing.T) {
	svc := newManualService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("v1")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	if _, err := svc.RotateCredential(ctx, "k", RotateOptions{NewValue: []byte("v2"), GracePeriod: time.Hour}); err != nil {
		t.Fatalf("Rotate to v2: %v", err)
	}
	if _, err := svc.RotateCredential(ctx, "k", RotateOptions{NewValue: []byte("v3"), GracePeriod: time.Hour}); err != nil {
		t.Fatalf("Rotate to v3: %v", err)
	}
	vs, err := svc.ListCredentialVersions(ctx, "k")
	if err != nil {
		t.Fatalf("ListCredentialVersions: %v", err)
	}
	if len(vs) != 3 {
		t.Fatalf("len(vs) = %d, want 3 (v1+v2 grace, v3 current)", len(vs))
	}
	currents := 0
	for _, v := range vs {
		if v.IsCurrent {
			currents++
			if v.Version != 3 {
				t.Fatalf("current version = %d, want 3", v.Version)
			}
		}
		if v.Value != nil {
			t.Fatalf("ListCredentialVersions must NOT return values; got %v", v.Value)
		}
	}
	if currents != 1 {
		t.Fatalf("currents = %d, want exactly 1", currents)
	}
}

func TestListCredentialVersions_MissingNameReturnsNotFound(t *testing.T) {
	svc := newManualService(t)
	_, err := svc.ListCredentialVersions(context.Background(), "absent")
	if !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("err = %v, want ErrCredentialNotFound", err)
	}
}

func TestListCredentialVersions_EmptyName(t *testing.T) {
	svc := newManualService(t)
	_, err := svc.ListCredentialVersions(context.Background(), " ")
	if !errors.Is(err, ErrEmptyCredentialName) {
		t.Fatalf("err = %v, want ErrEmptyCredentialName", err)
	}
}

func TestListCredentialVersions_ExpiredGraceFiltered(t *testing.T) {
	svc := newManualService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("v1")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	if _, err := svc.RotateCredential(ctx, "k", RotateOptions{NewValue: []byte("v2"), GracePeriod: time.Hour}); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	expireGrace(svc, "k")
	vs, err := svc.ListCredentialVersions(ctx, "k")
	if err != nil {
		t.Fatalf("ListCredentialVersions: %v", err)
	}
	if len(vs) != 1 {
		t.Fatalf("len(vs) = %d, want 1 (only current after expired grace filtered)", len(vs))
	}
}

func TestListCredentialVersions_InvalidStrategy(t *testing.T) {
	svc := newManualService(t)
	svc.strategy = "bogus"
	_, err := svc.ListCredentialVersions(context.Background(), "k")
	if !errors.Is(err, ErrInvalidStrategy) {
		t.Fatalf("err = %v, want ErrInvalidStrategy", err)
	}
}

// =============================================================================
// GCExpiredVersions
// =============================================================================

func TestGCExpiredVersions_ReapsExpiredOnly(t *testing.T) {
	svc := newManualService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("v1")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	// First rotation: short grace that we expire synchronously.
	if _, err := svc.RotateCredential(ctx, "k", RotateOptions{NewValue: []byte("v2"), GracePeriod: time.Hour}); err != nil {
		t.Fatalf("Rotate to v2: %v", err)
	}
	expireGrace(svc, "k")
	// Second rotation: long grace that should NOT be GC'd.
	if _, err := svc.RotateCredential(ctx, "k", RotateOptions{NewValue: []byte("v3"), GracePeriod: time.Hour}); err != nil {
		t.Fatalf("Rotate to v3: %v", err)
	}

	reaped, err := svc.GCExpiredVersions(ctx, "k")
	if err != nil {
		t.Fatalf("GCExpiredVersions: %v", err)
	}
	if reaped != 1 {
		t.Fatalf("reaped = %d, want 1", reaped)
	}

	vs, err := svc.ListCredentialVersions(ctx, "k")
	if err != nil {
		t.Fatalf("ListCredentialVersions: %v", err)
	}
	if len(vs) != 2 {
		t.Fatalf("len(vs) = %d after GC, want 2 (v3 current + v2 still grace)", len(vs))
	}
}

func TestGCExpiredVersions_NoopWhenNothingExpired(t *testing.T) {
	svc := newManualService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	reaped, err := svc.GCExpiredVersions(ctx, "k")
	if err != nil {
		t.Fatalf("GCExpiredVersions: %v", err)
	}
	if reaped != 0 {
		t.Fatalf("reaped = %d, want 0", reaped)
	}
}

func TestGCExpiredVersions_LegacyNoMetaNoop(t *testing.T) {
	svc := newManualService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("legacy")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	reaped, err := svc.GCExpiredVersions(ctx, "k")
	if err != nil {
		t.Fatalf("GCExpiredVersions: %v", err)
	}
	if reaped != 0 {
		t.Fatalf("reaped = %d, want 0", reaped)
	}
}

func TestGCExpiredVersions_EmptyName(t *testing.T) {
	svc := newManualService(t)
	_, err := svc.GCExpiredVersions(context.Background(), "")
	if !errors.Is(err, ErrEmptyCredentialName) {
		t.Fatalf("err = %v, want ErrEmptyCredentialName", err)
	}
}

func TestGCExpiredVersions_InvalidStrategy(t *testing.T) {
	svc := newManualService(t)
	svc.strategy = "bogus"
	_, err := svc.GCExpiredVersions(context.Background(), "k")
	if !errors.Is(err, ErrInvalidStrategy) {
		t.Fatalf("err = %v, want ErrInvalidStrategy", err)
	}
}

// =============================================================================
// Cross-version isolation
// =============================================================================

func TestRotateThenRetrieveRoundtrip_PreservesValues(t *testing.T) {
	svc := newManualService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("v1")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	for i := 2; i <= 5; i++ {
		val := []byte{byte('v'), byte('0' + i)}
		if _, err := svc.RotateCredential(ctx, "k", RotateOptions{
			NewValue:    val,
			GracePeriod: time.Hour,
		}); err != nil {
			t.Fatalf("Rotate to v%d: %v", i, err)
		}
	}
	for i := 1; i <= 5; i++ {
		want := []byte{byte('v'), byte('0' + i)}
		// v1 was stored, not rotated-with-grace. It's the original
		// untracked value at credentials/k; the first rotation
		// promoted it to v1 with a grace window. So it's retrievable.
		got, err := svc.retrieveVersionBytes(ctx, "k", uint64(i))
		if err != nil {
			t.Fatalf("RetrieveCredentialVersion(v%d): %v", i, err)
		}
		if string(got) != string(want) {
			t.Fatalf("v%d = %q, want %q", i, got, want)
		}
	}
}

// =============================================================================
// Barrier strategy
// =============================================================================

func TestListCredentials_BarrierEmpty(t *testing.T) {
	svc := newBarrierService(t)
	names, err := svc.ListCredentials(context.Background())
	if err != nil {
		t.Fatalf("ListCredentials: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("expected empty list; got %v", names)
	}
}

func TestListCredentials_BarrierFiltersMetadataSidecars(t *testing.T) {
	svc := newBarrierService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "alpha", []byte("a")); err != nil {
		t.Fatalf("StoreCredential alpha: %v", err)
	}
	if _, err := svc.RotateCredential(ctx, "alpha", RotateOptions{
		NewValue:    []byte("a2"),
		GracePeriod: time.Hour,
	}); err != nil {
		t.Fatalf("Rotate alpha: %v", err)
	}
	if err := svc.StoreCredential(ctx, "beta", []byte("b")); err != nil {
		t.Fatalf("StoreCredential beta: %v", err)
	}

	names, err := svc.ListCredentials(ctx)
	if err != nil {
		t.Fatalf("ListCredentials: %v", err)
	}
	want := []string{"alpha", "beta"}
	if len(names) != len(want) {
		t.Fatalf("names = %v, want %v (sidecars must be hidden)", names, want)
	}
	for i, n := range want {
		if names[i] != n {
			t.Fatalf("names[%d] = %q, want %q", i, names[i], n)
		}
	}
}

func TestRotateCredential_Barrier_FirstWriteIsVersion1(t *testing.T) {
	svc := newBarrierService(t)
	res, err := svc.RotateCredential(context.Background(), "k", RotateOptions{NewValue: []byte("v1")})
	if err != nil {
		t.Fatalf("RotateCredential: %v", err)
	}
	if res.PreviousVersion != 0 || res.NewVersion != 1 {
		t.Fatalf("res = %+v, want PreviousVersion=0 NewVersion=1", res)
	}
}

func TestRotateCredential_Barrier_LegacyPromotion(t *testing.T) {
	// Pre-populate the barrier with an untracked credential (no meta).
	svc := newBarrierService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("legacy")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	res, err := svc.RotateCredential(ctx, "k", RotateOptions{
		NewValue:    []byte("rotated"),
		GracePeriod: time.Hour,
	})
	if err != nil {
		t.Fatalf("RotateCredential: %v", err)
	}
	if res.PreviousVersion != 1 || res.NewVersion != 2 {
		t.Fatalf("res = %+v, want PreviousVersion=1 NewVersion=2", res)
	}
	// Old value retrievable via v1 during grace.
	got, err := svc.retrieveVersionBytes(ctx, "k", 1)
	if err != nil {
		t.Fatalf("RetrieveCredentialVersion(v1): %v", err)
	}
	if string(got) != "legacy" {
		t.Fatalf("v1 = %q, want %q", got, "legacy")
	}
}

func TestRotateCredential_Barrier_ZeroGraceWipesPrevious(t *testing.T) {
	svc := newBarrierService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("old")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	if _, err := svc.RotateCredential(ctx, "k", RotateOptions{NewValue: []byte("new")}); err != nil {
		t.Fatalf("RotateCredential: %v", err)
	}
	_, err := svc.retrieveVersionBytes(ctx, "k", 1)
	if !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("err = %v, want ErrVersionNotFound (no history was kept)", err)
	}
}

func TestRetrieveCredentialVersion_Barrier_Zero_AliasesCurrent(t *testing.T) {
	svc := newBarrierService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	got, err := svc.retrieveVersionBytes(ctx, "k", 0)
	if err != nil {
		t.Fatalf("RetrieveCredentialVersion(0): %v", err)
	}
	if string(got) != "v" {
		t.Fatalf("got %q, want %q", got, "v")
	}
}

func TestRetrieveCredentialVersion_Barrier_CurrentVersionMatch(t *testing.T) {
	svc := newBarrierService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("v1")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	if _, err := svc.RotateCredential(ctx, "k", RotateOptions{
		NewValue:    []byte("v2"),
		GracePeriod: time.Hour,
	}); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	got, err := svc.retrieveVersionBytes(ctx, "k", 2)
	if err != nil {
		t.Fatalf("RetrieveCredentialVersion(v2): %v", err)
	}
	if string(got) != "v2" {
		t.Fatalf("v2 = %q, want %q", got, "v2")
	}
}

func TestRetrieveCredentialVersion_Barrier_LegacyV1(t *testing.T) {
	svc := newBarrierService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("legacy")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	got, err := svc.retrieveVersionBytes(ctx, "k", 1)
	if err != nil {
		t.Fatalf("RetrieveCredentialVersion(legacy v1): %v", err)
	}
	if string(got) != "legacy" {
		t.Fatalf("legacy v1 = %q, want %q", got, "legacy")
	}
}

func TestRetrieveCredentialVersion_Barrier_LegacyMissing(t *testing.T) {
	svc := newBarrierService(t)
	_, err := svc.retrieveVersionBytes(context.Background(), "absent", 1)
	if !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("err = %v, want ErrCredentialNotFound", err)
	}
}

func TestRetrieveCredentialVersion_Barrier_LegacyNonV1Rejected(t *testing.T) {
	svc := newBarrierService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("legacy")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	_, err := svc.retrieveVersionBytes(ctx, "k", 7)
	if !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("err = %v, want ErrVersionNotFound", err)
	}
}

func TestRetrieveCredentialVersion_Barrier_ExpiredGrace(t *testing.T) {
	svc := newBarrierService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("old")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	if _, err := svc.RotateCredential(ctx, "k", RotateOptions{
		NewValue:    []byte("new"),
		GracePeriod: time.Hour,
	}); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	expireBarrierGrace(t, svc, "k")
	_, err := svc.retrieveVersionBytes(ctx, "k", 1)
	if !errors.Is(err, ErrVersionExpired) {
		t.Fatalf("err = %v, want ErrVersionExpired", err)
	}
}

func TestRetrieveCredentialVersion_Barrier_GraceVersionNotFoundInStore(t *testing.T) {
	svc := newBarrierService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("old")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	if _, err := svc.RotateCredential(ctx, "k", RotateOptions{
		NewValue:    []byte("new"),
		GracePeriod: time.Hour,
	}); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	// Manually delete the version key while leaving meta intact.
	if err := svc.barrier.Delete(ctx, versionKey("k", 1)); err != nil {
		t.Fatalf("barrier.Delete: %v", err)
	}
	_, err := svc.retrieveVersionBytes(ctx, "k", 1)
	if !errors.Is(err, ErrVersionExpired) {
		t.Fatalf("err = %v, want ErrVersionExpired", err)
	}
}

func TestRetrieveCredentialVersion_Barrier_VersionNotInHistory(t *testing.T) {
	svc := newBarrierService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("v1")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	if _, err := svc.RotateCredential(ctx, "k", RotateOptions{
		NewValue:    []byte("v2"),
		GracePeriod: time.Hour,
	}); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	_, err := svc.retrieveVersionBytes(ctx, "k", 99)
	if !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("err = %v, want ErrVersionNotFound", err)
	}
}

func TestListCredentialVersions_Barrier_NotFound(t *testing.T) {
	svc := newBarrierService(t)
	_, err := svc.ListCredentialVersions(context.Background(), "absent")
	if !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("err = %v, want ErrCredentialNotFound", err)
	}
}

func TestListCredentialVersions_Barrier_Legacy(t *testing.T) {
	svc := newBarrierService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("legacy")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	vs, err := svc.ListCredentialVersions(ctx, "k")
	if err != nil {
		t.Fatalf("ListCredentialVersions: %v", err)
	}
	if len(vs) != 1 || vs[0].Version != 1 || !vs[0].IsCurrent {
		t.Fatalf("vs = %+v, want one v1 marked current", vs)
	}
}

func TestListCredentialVersions_Barrier_WithHistory(t *testing.T) {
	svc := newBarrierService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("v1")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	if _, err := svc.RotateCredential(ctx, "k", RotateOptions{
		NewValue:    []byte("v2"),
		GracePeriod: time.Hour,
	}); err != nil {
		t.Fatalf("Rotate v2: %v", err)
	}
	if _, err := svc.RotateCredential(ctx, "k", RotateOptions{
		NewValue:    []byte("v3"),
		GracePeriod: time.Hour,
	}); err != nil {
		t.Fatalf("Rotate v3: %v", err)
	}
	vs, err := svc.ListCredentialVersions(ctx, "k")
	if err != nil {
		t.Fatalf("ListCredentialVersions: %v", err)
	}
	if len(vs) != 3 {
		t.Fatalf("len(vs) = %d, want 3 (v1+v2 grace, v3 current)", len(vs))
	}
}

func TestListCredentialVersions_Barrier_ExpiredFiltered(t *testing.T) {
	svc := newBarrierService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("v1")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	if _, err := svc.RotateCredential(ctx, "k", RotateOptions{
		NewValue:    []byte("v2"),
		GracePeriod: time.Hour,
	}); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	expireBarrierGrace(t, svc, "k")
	vs, err := svc.ListCredentialVersions(ctx, "k")
	if err != nil {
		t.Fatalf("ListCredentialVersions: %v", err)
	}
	if len(vs) != 1 {
		t.Fatalf("len(vs) = %d, want 1 (only current after expired grace filtered)", len(vs))
	}
}

func TestGCExpiredVersions_Barrier_ReapsExpiredOnly(t *testing.T) {
	svc := newBarrierService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("v1")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	if _, err := svc.RotateCredential(ctx, "k", RotateOptions{
		NewValue:    []byte("v2"),
		GracePeriod: time.Hour,
	}); err != nil {
		t.Fatalf("Rotate v2: %v", err)
	}
	expireBarrierGrace(t, svc, "k")
	if _, err := svc.RotateCredential(ctx, "k", RotateOptions{
		NewValue:    []byte("v3"),
		GracePeriod: time.Hour,
	}); err != nil {
		t.Fatalf("Rotate v3: %v", err)
	}

	reaped, err := svc.GCExpiredVersions(ctx, "k")
	if err != nil {
		t.Fatalf("GCExpiredVersions: %v", err)
	}
	if reaped != 1 {
		t.Fatalf("reaped = %d, want 1", reaped)
	}

	vs, err := svc.ListCredentialVersions(ctx, "k")
	if err != nil {
		t.Fatalf("ListCredentialVersions: %v", err)
	}
	if len(vs) != 2 {
		t.Fatalf("len(vs) = %d after GC, want 2 (v3 current + v2 still grace)", len(vs))
	}
}

func TestGCExpiredVersions_Barrier_NoopFresh(t *testing.T) {
	svc := newBarrierService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	reaped, err := svc.GCExpiredVersions(ctx, "k")
	if err != nil {
		t.Fatalf("GCExpiredVersions: %v", err)
	}
	if reaped != 0 {
		t.Fatalf("reaped = %d, want 0", reaped)
	}
}

// =============================================================================
// Storage layout helpers
// =============================================================================

func TestVersionKey_Format(t *testing.T) {
	got := versionKey("my-cred", 42)
	want := credentialPrefix + "my-cred#v42"
	if got != want {
		t.Fatalf("versionKey = %q, want %q", got, want)
	}
}

func TestMetaKey_Format(t *testing.T) {
	got := metaKey("my-cred")
	want := credentialPrefix + "my-cred" + metaSuffix
	if got != want {
		t.Fatalf("metaKey = %q, want %q", got, want)
	}
}

func TestLoadBarrierMeta_MissingReturnsZero(t *testing.T) {
	svc := newBarrierService(t)
	meta, err := svc.loadBarrierMeta(context.Background(), "missing")
	if err != nil {
		t.Fatalf("loadBarrierMeta: %v", err)
	}
	if meta.CurrentVersion != 0 || len(meta.History) != 0 {
		t.Fatalf("meta = %+v, want zero-valued", meta)
	}
}

func TestLoadBarrierMeta_CorruptedJSONRejected(t *testing.T) {
	svc := newBarrierService(t)
	ctx := context.Background()
	if err := svc.barrier.Put(ctx, metaKey("k"), []byte("not-json")); err != nil {
		t.Fatalf("barrier.Put: %v", err)
	}
	_, err := svc.loadBarrierMeta(ctx, "k")
	if err == nil {
		t.Fatalf("loadBarrierMeta: expected decode error, got nil")
	}
}

func TestLoadBarrierCurrent_Missing(t *testing.T) {
	svc := newBarrierService(t)
	value, exists, err := svc.loadBarrierCurrent(context.Background(), "absent")
	if err != nil {
		t.Fatalf("loadBarrierCurrent: %v", err)
	}
	if exists {
		t.Fatal("loadBarrierCurrent reported missing key as existing")
	}
	if value != nil {
		t.Fatalf("value = %v, want nil", value)
	}
}
