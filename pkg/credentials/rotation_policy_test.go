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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// rotationEpoch is the injected clock every rotation-policy test reads.
var rotationEpoch = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// newRotationService returns a barrier-strategy service over an unsealed
// in-memory barrier whose clock reads rotationEpoch.
func newRotationService(t *testing.T, opts ...Option) *CredentialService {
	t.Helper()
	ctx := t.Context()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	strategy, err := seal.NewSoftwareStrategy(ctx, []byte("rotation-pass"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	barrier, err := seal.NewStorageBarrier(logger, seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "rotation-root-key", DeploymentID: 1}, strategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	creds := seal.MustCredentials(ctx, []byte("rotation-pass"))
	if err := barrier.Initialize(ctx, creds); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if barrier.IsSealed() {
		if err := barrier.Unseal(ctx, creds); err != nil {
			t.Fatalf("Unseal: %v", err)
		}
	}
	t.Cleanup(func() { _ = barrier.Close() })
	svc, err := NewCredentialService(&Config{Strategy: StrategyBarrier}, barrier, logger, opts...)
	if err != nil {
		t.Fatalf("NewCredentialService: %v", err)
	}
	svc.now = func() time.Time { return rotationEpoch }
	return svc
}

// countingSource is a RotationValueSource that hands out a fresh value per
// call and counts the calls.
func countingSource(calls *atomic.Int32) RotationValueSource {
	return func(_ context.Context, name string) (*secure.Buffer, error) {
		n := calls.Add(1)
		return secure.NewBufferFromBytes([]byte(name + "-rotated-" + string(rune('0'+n))))
	}
}

func storeRotationCredential(t *testing.T, svc *CredentialService, name string) {
	t.Helper()
	if err := svc.StoreCredential(t.Context(), name, []byte(name+"-initial")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
}

func versionCount(t *testing.T, svc *CredentialService, name string) int {
	t.Helper()
	versions, err := svc.ListCredentialVersions(t.Context(), name)
	if err != nil {
		t.Fatalf("ListCredentialVersions: %v", err)
	}
	return len(versions)
}

func TestRotateDue_ElapsedIntervalRotatesOnceAndTrims(t *testing.T) {
	var calls atomic.Int32
	svc := newRotationService(t, WithRotationValueSource(countingSource(&calls)))
	ctx := t.Context()
	storeRotationCredential(t, svc, "db-password")
	policy := RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 2}
	if err := svc.SetRotationPolicy(ctx, "db-password", policy); err != nil {
		t.Fatalf("SetRotationPolicy: %v", err)
	}

	// Three intervals in a row: each rotates once, and the history is
	// trimmed so the versions left never exceed RetainVersions.
	at := rotationEpoch
	for i := 1; i <= 3; i++ {
		at = at.Add(time.Hour)
		n, err := svc.RotateDue(ctx, at)
		if err != nil {
			t.Fatalf("RotateDue #%d: %v", i, err)
		}
		if n != 1 {
			t.Fatalf("RotateDue #%d rotated %d, want 1", i, n)
		}
		// A second call at the same instant finds nothing due.
		if n, err := svc.RotateDue(ctx, at); err != nil || n != 0 {
			t.Fatalf("RotateDue repeat #%d = %d, %v; want 0, nil", i, n, err)
		}
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("value source called %d times, want 3", got)
	}
	if got := versionCount(t, svc, "db-password"); got != policy.RetainVersions {
		t.Fatalf("versions left = %d, want %d", got, policy.RetainVersions)
	}
	got, err := svc.RotationPolicy(ctx, "db-password")
	if err != nil {
		t.Fatalf("RotationPolicy: %v", err)
	}
	if got != policy {
		t.Fatalf("policy = %+v, want %+v", got, policy)
	}
	next, err := svc.NextRotation(ctx, "db-password")
	if err != nil {
		t.Fatalf("NextRotation: %v", err)
	}
	if want := at.Add(time.Hour); !next.Equal(want) {
		t.Fatalf("next rotation = %v, want %v", next, want)
	}
}

func TestRotateDue_RetainOneKeepsOnlyCurrent(t *testing.T) {
	var calls atomic.Int32
	svc := newRotationService(t, WithRotationValueSource(countingSource(&calls)))
	ctx := t.Context()
	storeRotationCredential(t, svc, "api-key")
	if err := svc.SetRotationPolicy(ctx, "api-key", RotationPolicy{Enabled: true, Interval: time.Minute, RetainVersions: 1}); err != nil {
		t.Fatalf("SetRotationPolicy: %v", err)
	}
	if n, err := svc.RotateDue(ctx, rotationEpoch.Add(time.Minute)); err != nil || n != 1 {
		t.Fatalf("RotateDue = %d, %v; want 1, nil", n, err)
	}
	if got := versionCount(t, svc, "api-key"); got != 1 {
		t.Fatalf("versions left = %d, want 1", got)
	}
}

func TestRotateDue_BeforeIntervalNothingRotates(t *testing.T) {
	var calls atomic.Int32
	svc := newRotationService(t, WithRotationValueSource(countingSource(&calls)))
	ctx := t.Context()
	storeRotationCredential(t, svc, "db-password")
	if err := svc.SetRotationPolicy(ctx, "db-password", RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 3}); err != nil {
		t.Fatalf("SetRotationPolicy: %v", err)
	}
	n, err := svc.RotateDue(ctx, rotationEpoch.Add(time.Hour-time.Nanosecond))
	if err != nil || n != 0 {
		t.Fatalf("RotateDue = %d, %v; want 0, nil", n, err)
	}
	if calls.Load() != 0 {
		t.Fatal("value source called before the interval elapsed")
	}
}

func TestRotateDue_DisabledPolicyNeverRotates(t *testing.T) {
	var calls atomic.Int32
	svc := newRotationService(t, WithRotationValueSource(countingSource(&calls)))
	ctx := t.Context()
	storeRotationCredential(t, svc, "db-password")
	// A disabled policy needs no valid interval.
	if err := svc.SetRotationPolicy(ctx, "db-password", RotationPolicy{Enabled: false}); err != nil {
		t.Fatalf("SetRotationPolicy: %v", err)
	}
	n, err := svc.RotateDue(ctx, rotationEpoch.Add(100*365*24*time.Hour))
	if err != nil || n != 0 {
		t.Fatalf("RotateDue = %d, %v; want 0, nil", n, err)
	}
	if calls.Load() != 0 {
		t.Fatal("value source called for a disabled policy")
	}
	next, err := svc.NextRotation(ctx, "db-password")
	if err != nil || !next.IsZero() {
		t.Fatalf("NextRotation = %v, %v; want zero, nil", next, err)
	}
}

func TestRotateDue_NoPolicyNothingRotates(t *testing.T) {
	var calls atomic.Int32
	svc := newRotationService(t, WithRotationValueSource(countingSource(&calls)))
	storeRotationCredential(t, svc, "plain")
	n, err := svc.RotateDue(t.Context(), rotationEpoch.Add(time.Hour))
	if err != nil || n != 0 {
		t.Fatalf("RotateDue = %d, %v; want 0, nil", n, err)
	}
	got, err := svc.RotationPolicy(t.Context(), "plain")
	if err != nil || got != (RotationPolicy{}) {
		t.Fatalf("RotationPolicy = %+v, %v; want zero, nil", got, err)
	}
}

func TestSetRotationPolicy_InvalidRefused(t *testing.T) {
	svc := newRotationService(t)
	storeRotationCredential(t, svc, "db-password")
	cases := map[string]RotationPolicy{
		"zero interval":     {Enabled: true, Interval: 0, RetainVersions: 1},
		"negative interval": {Enabled: true, Interval: -time.Second, RetainVersions: 1},
		"zero retain":       {Enabled: true, Interval: time.Hour, RetainVersions: 0},
		"negative retain":   {Enabled: true, Interval: time.Hour, RetainVersions: -1},
	}
	for name, policy := range cases {
		t.Run(name, func(t *testing.T) {
			err := svc.SetRotationPolicy(t.Context(), "db-password", policy)
			var invalid *InvalidRotationPolicyError
			if !errors.As(err, &invalid) {
				t.Fatalf("err = %v, want *InvalidRotationPolicyError", err)
			}
			if invalid.Error() == "" {
				t.Fatal("empty error message")
			}
		})
	}
}

func TestRotationPolicy_UnknownNameNotFound(t *testing.T) {
	svc := newRotationService(t)
	err := svc.SetRotationPolicy(t.Context(), "missing", RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 1})
	if !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("SetRotationPolicy err = %v, want ErrCredentialNotFound", err)
	}
	if _, err := svc.RotationPolicy(t.Context(), "missing"); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("RotationPolicy err = %v, want ErrCredentialNotFound", err)
	}
	if _, err := svc.NextRotation(t.Context(), "missing"); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("NextRotation err = %v, want ErrCredentialNotFound", err)
	}
}

func TestRotationPolicy_EmptyNameRefused(t *testing.T) {
	svc := newRotationService(t)
	if err := svc.SetRotationPolicy(t.Context(), "", RotationPolicy{}); !errors.Is(err, ErrEmptyCredentialName) {
		t.Fatalf("SetRotationPolicy err = %v, want ErrEmptyCredentialName", err)
	}
	if _, err := svc.RotationPolicy(t.Context(), ""); !errors.Is(err, ErrEmptyCredentialName) {
		t.Fatalf("RotationPolicy err = %v, want ErrEmptyCredentialName", err)
	}
}

func TestSetRotationPolicy_ManualServiceRefused(t *testing.T) {
	svc, err := NewCredentialService(&Config{Strategy: StrategyManual}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewCredentialService: %v", err)
	}
	if err := svc.StoreCredential(t.Context(), "pin", []byte("1234")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	err = svc.SetRotationPolicy(t.Context(), "pin", RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 1})
	var invalid *InvalidRotationPolicyError
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %v, want *InvalidRotationPolicyError", err)
	}
	got, err := svc.RotationPolicy(t.Context(), "pin")
	if err != nil || got != (RotationPolicy{}) {
		t.Fatalf("RotationPolicy = %+v, %v; want zero, nil", got, err)
	}
	if _, err := svc.RotationPolicy(t.Context(), "absent"); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("RotationPolicy absent err = %v, want ErrCredentialNotFound", err)
	}
	if n, err := svc.RotateDue(t.Context(), rotationEpoch); err != nil || n != 0 {
		t.Fatalf("RotateDue = %d, %v; want 0, nil", n, err)
	}
}

func TestRotateDue_NoValueSourceTypedErrorPerCredential(t *testing.T) {
	svc := newRotationService(t)
	ctx := t.Context()
	for _, name := range []string{"a", "b"} {
		storeRotationCredential(t, svc, name)
		if err := svc.SetRotationPolicy(ctx, name, RotationPolicy{Enabled: true, Interval: time.Minute, RetainVersions: 1}); err != nil {
			t.Fatalf("SetRotationPolicy: %v", err)
		}
	}
	at := rotationEpoch.Add(time.Minute)
	n, err := svc.RotateDue(ctx, at)
	if n != 0 {
		t.Fatalf("rotated %d, want 0", n)
	}
	if !errors.Is(err, ErrNoRotationValueSource) {
		t.Fatalf("err = %v, want ErrNoRotationValueSource", err)
	}
	failed := map[string]bool{}
	for _, e := range err.(interface{ Unwrap() []error }).Unwrap() {
		var rerr *RotationError
		if !errors.As(e, &rerr) {
			t.Fatalf("joined error %v is not *RotationError", e)
		}
		failed[rerr.Name] = true
	}
	if !failed["a"] || !failed["b"] || len(failed) != 2 {
		t.Fatalf("failed names = %v, want a and b", failed)
	}
	// Nothing was claimed: the credentials stay due.
	next, err := svc.NextRotation(ctx, "a")
	if err != nil || next.After(at) {
		t.Fatalf("NextRotation = %v, %v; want still due", next, err)
	}
}

func TestRotateDue_ValueSourceErrorReported(t *testing.T) {
	boom := errors.New("source down")
	svc := newRotationService(t, WithRotationValueSource(func(context.Context, string) (*secure.Buffer, error) {
		return nil, boom
	}))
	ctx := t.Context()
	storeRotationCredential(t, svc, "a")
	if err := svc.SetRotationPolicy(ctx, "a", RotationPolicy{Enabled: true, Interval: time.Minute, RetainVersions: 1}); err != nil {
		t.Fatalf("SetRotationPolicy: %v", err)
	}
	n, err := svc.RotateDue(ctx, rotationEpoch.Add(time.Minute))
	if n != 0 || !errors.Is(err, boom) {
		t.Fatalf("RotateDue = %d, %v; want 0, source error", n, err)
	}
	var rerr *RotationError
	if !errors.As(err, &rerr) || rerr.Name != "a" {
		t.Fatalf("err = %v, want *RotationError for a", err)
	}
}

// TestRotateDue_RacingCallsRotateOnce: a second caller that finds the due
// credential while the first is inside the rotation source must not
// invoke the source itself, since a source may change the value outside
// the store.
func TestRotateDue_RacingCallsRotateOnce(t *testing.T) {
	var calls atomic.Int32
	inSource := make(chan struct{})
	release := make(chan struct{})
	svc := newRotationService(t, WithRotationValueSource(func(_ context.Context, name string) (*secure.Buffer, error) {
		if calls.Add(1) == 1 {
			close(inSource)
			<-release
		}
		return secure.NewBufferFromBytes([]byte(name + "-raced"))
	}))
	ctx := t.Context()
	storeRotationCredential(t, svc, "db-password")
	if err := svc.SetRotationPolicy(ctx, "db-password", RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 5}); err != nil {
		t.Fatalf("SetRotationPolicy: %v", err)
	}
	at := rotationEpoch.Add(time.Hour)

	type result struct {
		n   int
		err error
	}
	first := make(chan result, 1)
	go func() {
		n, err := svc.RotateDue(ctx, at)
		first <- result{n, err}
	}()
	<-inSource
	n, err := svc.RotateDue(ctx, at)
	if err != nil || n != 0 {
		t.Fatalf("second RotateDue = %d, %v; want 0, nil while the claim is held", n, err)
	}
	close(release)
	if r := <-first; r.err != nil || r.n != 1 {
		t.Fatalf("first RotateDue = %d, %v; want 1, nil", r.n, r.err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("rotation source ran %d times, want 1", got)
	}
	if got := versionCount(t, svc, "db-password"); got != 2 {
		t.Fatalf("versions = %d, want 2", got)
	}
}

// TestRotateDue_ExpiredClaimIsRetaken: a claim left by a holder that
// crashed excludes every caller until it lapses, and then the credential
// rotates once and the claim is gone.
func TestRotateDue_ExpiredClaimIsRetaken(t *testing.T) {
	var calls atomic.Int32
	svc := newRotationService(t, WithRotationValueSource(countingSource(&calls)))
	ctx := t.Context()
	storeRotationCredential(t, svc, "k")
	if err := svc.SetRotationPolicy(ctx, "k", RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 5}); err != nil {
		t.Fatalf("SetRotationPolicy: %v", err)
	}
	at := rotationEpoch.Add(time.Hour)
	crashed := &rotationClaim{ID: "crashed", Due: at, Until: at.Add(rotationClaimLease)}
	if err := svc.updateBarrierMeta(ctx, "k", "write meta", func(meta *credentialMeta) bool {
		meta.Claim = crashed
		return true
	}); err != nil {
		t.Fatalf("updateBarrierMeta: %v", err)
	}

	// A claim lapses on the service clock, at acquisition.
	svc.now = func() time.Time { return crashed.Until.Add(-time.Nanosecond) }
	if n, err := svc.RotateDue(ctx, crashed.Until.Add(-time.Nanosecond)); err != nil || n != 0 || calls.Load() != 0 {
		t.Fatalf("RotateDue under the claim = %d, %v with %d source calls; want 0, nil, 0", n, err, calls.Load())
	}
	lapsed := crashed.Until
	svc.now = func() time.Time { return lapsed }
	if n, err := svc.RotateDue(ctx, lapsed); err != nil || n != 1 || calls.Load() != 1 {
		t.Fatalf("RotateDue past the claim = %d, %v with %d source calls; want 1, nil, 1", n, err, calls.Load())
	}
	meta, err := svc.loadBarrierMeta(ctx, "k")
	if err != nil {
		t.Fatalf("loadBarrierMeta: %v", err)
	}
	if meta.Claim != nil || !meta.NextRotation.Equal(lapsed.Add(time.Hour)) {
		t.Fatalf("claim = %+v, next = %v; want no claim and %v", meta.Claim, meta.NextRotation, lapsed.Add(time.Hour))
	}
}

// TestRotateDue_PolicyChangedDuringRotationKeepsItsSchedule: an operator
// who changes the policy while a rotation holds the claim keeps the
// schedule they set; the completing rotation does not move it.
func TestRotateDue_PolicyChangedDuringRotationKeepsItsSchedule(t *testing.T) {
	var svc *CredentialService
	changed := RotationPolicy{Enabled: true, Interval: 3 * time.Hour, RetainVersions: 5}
	svc = newRotationService(t, WithRotationValueSource(func(ctx context.Context, name string) (*secure.Buffer, error) {
		if err := svc.SetRotationPolicy(ctx, name, changed); err != nil {
			return nil, err
		}
		return secure.NewBufferFromBytes([]byte(name + "-rotated"))
	}))
	ctx := t.Context()
	storeRotationCredential(t, svc, "k")
	if err := svc.SetRotationPolicy(ctx, "k", RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 5}); err != nil {
		t.Fatalf("SetRotationPolicy: %v", err)
	}

	if n, err := svc.RotateDue(ctx, rotationEpoch.Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("RotateDue = %d, %v; want 1, nil", n, err)
	}
	next, err := svc.NextRotation(ctx, "k")
	if err != nil {
		t.Fatalf("NextRotation: %v", err)
	}
	if want := rotationEpoch.Add(changed.Interval); !next.Equal(want) {
		t.Fatalf("next = %v, want the operator's %v", next, want)
	}
}

// The retention cleanup after a scheduled rotation applies the policy as it
// stands at the cleanup, not the one read when the rotation was claimed: an
// operator who raised RetainVersions, or disabled the policy, while the value
// source ran keeps the predecessors the new policy keeps.
func TestRotateDue_CleanupAppliesThePolicyChangedDuringRotation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changed RotationPolicy
	}{
		{"retention raised", RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 5}},
		{"policy disabled", RotationPolicy{Enabled: false, Interval: time.Hour, RetainVersions: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var svc *CredentialService
			svc = newRotationService(t, WithRotationValueSource(func(ctx context.Context, name string) (*secure.Buffer, error) {
				if err := svc.SetRotationPolicy(ctx, name, tc.changed); err != nil {
					return nil, err
				}
				return secure.NewBufferFromBytes([]byte(name + "-rotated"))
			}))
			ctx := t.Context()
			storeRotationCredential(t, svc, "k")
			if err := svc.SetRotationPolicy(ctx, "k", RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 1}); err != nil {
				t.Fatalf("SetRotationPolicy: %v", err)
			}

			if n, err := svc.RotateDue(ctx, rotationEpoch.Add(time.Hour)); err != nil || n != 1 {
				t.Fatalf("RotateDue = %d, %v; want 1, nil", n, err)
			}
			if got := versionCount(t, svc, "k"); got != 2 {
				t.Fatalf("versions = %d, want 2: the changed policy keeps the predecessor", got)
			}
			prev, err := svc.store.Get(ctx, versionKey("k", 1))
			if err != nil {
				t.Fatalf("the predecessor's value: %v", err)
			}
			if string(prev) != "k-initial" {
				t.Fatalf("the predecessor's value = %q, want %q", prev, "k-initial")
			}
		})
	}
}

// TestRotateDue_DeleteDuringSourceStaysDeleted: the credential is deleted
// while the value source runs. The scheduled commit is fenced on its
// claim, so it recreates neither the value nor the record, leaves no
// staged or retained key behind, and reports ErrRotationClaimLost.
func TestRotateDue_DeleteDuringSourceStaysDeleted(t *testing.T) {
	var svc *CredentialService
	svc = newRotationService(t, WithRotationValueSource(func(ctx context.Context, name string) (*secure.Buffer, error) {
		if err := svc.DeleteCredential(ctx, name); err != nil {
			return nil, err
		}
		return secure.NewBufferFromBytes([]byte(name + "-rotated"))
	}))
	ctx := t.Context()
	storeRotationCredential(t, svc, "k")
	if err := svc.SetRotationPolicy(ctx, "k", RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 5}); err != nil {
		t.Fatalf("SetRotationPolicy: %v", err)
	}

	n, err := svc.RotateDue(ctx, rotationEpoch.Add(time.Hour))
	if n != 0 || !errors.Is(err, ErrRotationClaimLost) {
		t.Fatalf("RotateDue = %d, %v; want 0 and ErrRotationClaimLost", n, err)
	}
	if _, err := svc.RetrieveCredentialBuffer(ctx, "k"); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("RetrieveCredentialBuffer error = %v, want ErrCredentialNotFound", err)
	}
	for _, key := range []string{metaKey("k"), credentialPrefix + "k", stagedKey("k", 2), versionKey("k", 1)} {
		if _, err := svc.store.Get(ctx, key); !errors.Is(err, seal.ErrSecretNotFound) {
			t.Errorf("key %q read error = %v, want it absent", key, err)
		}
	}
}

// TestRotateDue_ManualRotationBeforeCommitKept: a manual rotation lands
// between the scheduled rotation's read of the current value and its
// write. That write is conditional on the value read, so the manual value
// stays current and the scheduled rotation reports ErrRotationClaimLost.
func TestRotateDue_ManualRotationBeforeCommitKept(t *testing.T) {
	svc, backend := newFaultRotationService(t)
	ctx := t.Context()
	setFaultPolicy(t, svc, "k", 5)
	var calls atomic.Int32
	countSource(svc, &calls)
	var fired atomic.Bool
	backend.onCAS = func(ctx context.Context, key string) {
		if key != credentialPrefix+"k" || !fired.CompareAndSwap(false, true) {
			return
		}
		if _, err := svc.RotateCredential(ctx, "k", RotateOptions{NewValue: []byte("manual")}); err != nil {
			t.Errorf("RotateCredential: %v", err)
		}
	}

	if _, err := svc.RotateDue(ctx, rotationEpoch.Add(time.Hour)); !errors.Is(err, ErrRotationClaimLost) {
		t.Fatalf("RotateDue error = %v, want ErrRotationClaimLost", err)
	}
	current, _, err := svc.loadBarrierCurrent(ctx, "k")
	if err != nil {
		t.Fatalf("loadBarrierCurrent: %v", err)
	}
	if string(current) != "manual" {
		t.Fatalf("current = %q, want the manual rotation's value", current)
	}
	requireStillDue(t, svc, "k", rotationEpoch.Add(time.Hour))
}

// TestRotateBarrier_ClaimReplacedTakesValueBack: the claim is replaced
// between the scheduled rotation's current-value write and its record
// write. The record is not written, the previous value is restored, and
// ErrRotationClaimLost is returned.
func TestRotateBarrier_ClaimReplacedTakesValueBack(t *testing.T) {
	svc, backend := newFaultRotationService(t)
	ctx := t.Context()
	setFaultPolicy(t, svc, "k", 3)
	claim, _, err := svc.claimRotation(ctx, "k", rotationEpoch.Add(time.Hour))
	if err != nil || claim == nil {
		t.Fatalf("claimRotation = %v, %v; want a claim", claim, err)
	}
	var fired atomic.Bool
	backend.onCAS = func(ctx context.Context, key string) {
		if !strings.HasSuffix(key, metaKey("k")) || !fired.CompareAndSwap(false, true) {
			return
		}
		if err := svc.updateBarrierMeta(ctx, "k", "replace claim", func(m *credentialMeta) bool {
			m.Claim = &rotationClaim{ID: "other", Until: m.Claim.Until}
			return true
		}); err != nil {
			t.Errorf("replace claim: %v", err)
		}
	}
	before, _, err := svc.loadBarrierCurrent(ctx, "k")
	if err != nil {
		t.Fatalf("loadBarrierCurrent: %v", err)
	}

	_, err = svc.RotateCredential(ctx, "k", RotateOptions{
		NewValue:       []byte("scheduled"),
		retainPrevious: true,
		schedule:       &scheduledRotation{claim: claim, now: rotationEpoch},
	})
	if !errors.Is(err, ErrRotationClaimLost) {
		t.Fatalf("RotateCredential error = %v, want ErrRotationClaimLost", err)
	}
	after, _, err := svc.loadBarrierCurrent(ctx, "k")
	if err != nil {
		t.Fatalf("loadBarrierCurrent: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("current = %q, want the previous %q restored", after, before)
	}
	meta, err := svc.loadBarrierMeta(ctx, "k")
	if err != nil {
		t.Fatalf("loadBarrierMeta: %v", err)
	}
	if meta.CurrentVersion == 2 || meta.Claim == nil || meta.Claim.ID != "other" {
		t.Fatalf("meta = %+v, want the replacing claim kept and no version advance", meta)
	}
}

// requireStillDue asserts name holds no claim and is still scheduled at due.
func requireStillDue(t *testing.T, svc *CredentialService, name string, due time.Time) {
	t.Helper()
	meta, err := svc.loadBarrierMeta(t.Context(), name)
	if err != nil {
		t.Fatalf("loadBarrierMeta: %v", err)
	}
	if meta.Claim != nil || !meta.NextRotation.Equal(due) {
		t.Fatalf("claim = %+v, next = %v; want no claim and still due at %v", meta.Claim, meta.NextRotation, due)
	}
}

// TestRotateDue_FailedRotationRetriedAtOnce: a claimed rotation whose
// current-value write fails leaves the credential due, so the next check,
// and a service restarted over the same storage, retries it rather than
// waiting an Interval.
func TestRotateDue_FailedRotationRetriedAtOnce(t *testing.T) {
	svc, backend := newFaultRotationService(t)
	setFaultPolicy(t, svc, "k", 5)
	due := rotationEpoch.Add(time.Hour)
	arm(&backend.failCAS, credentialPrefix+"k")

	n, err := svc.RotateDue(t.Context(), due)
	requireRotationOpError(t, err, "k", "write current")
	if n != 0 {
		t.Fatalf("rotated %d, want 0", n)
	}
	requireStillDue(t, svc, "k", due)

	backend.failCAS.Store(nil)
	var calls atomic.Int32
	restarted, err := NewCredentialService(&Config{Strategy: StrategyBarrier}, svc.store, testLogger(),
		WithRotationValueSource(countingSource(&calls)))
	if err != nil {
		t.Fatalf("NewCredentialService: %v", err)
	}
	// The failed attempt staged the value its source returned, so the
	// restarted service lands that value without drawing another.
	if n, err := restarted.RotateDue(t.Context(), due); err != nil || n != 1 || calls.Load() != 0 {
		t.Fatalf("restarted RotateDue = %d, %v with %d source calls; want 1, nil, 0", n, err, calls.Load())
	}
	buf, err := restarted.RetrieveCredentialBuffer(t.Context(), "k")
	if err != nil {
		t.Fatalf("RetrieveCredentialBuffer: %v", err)
	}
	defer buf.Destroy()
	if err := buf.WithBytes(func(b []byte) error {
		if string(b) != "k-rotated-1" {
			t.Errorf("current = %q, want the staged k-rotated-1", b)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithBytes: %v", err)
	}
}

// TestRotateDue_ExpiredSchedulerContextReleasesClaim: the scheduler's
// context ends inside the rotation; the claim is still released, so the
// next check retries.
func TestRotateDue_ExpiredSchedulerContextReleasesClaim(t *testing.T) {
	cctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var calls atomic.Int32
	svc := newRotationService(t, WithRotationValueSource(func(ctx context.Context, name string) (*secure.Buffer, error) {
		if calls.Add(1) == 1 {
			cancel()
			return nil, ctx.Err()
		}
		return secure.NewBufferFromBytes([]byte(name + "-rotated"))
	}))
	storeRotationCredential(t, svc, "k")
	if err := svc.SetRotationPolicy(t.Context(), "k", RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 5}); err != nil {
		t.Fatalf("SetRotationPolicy: %v", err)
	}
	due := rotationEpoch.Add(time.Hour)

	if n, err := svc.RotateDue(cctx, due); !errors.Is(err, context.Canceled) || n != 0 {
		t.Fatalf("RotateDue = %d, %v; want 0 and context.Canceled", n, err)
	}
	requireStillDue(t, svc, "k", due)
	if n, err := svc.RotateDue(t.Context(), due); err != nil || n != 1 || calls.Load() != 2 {
		t.Fatalf("retry RotateDue = %d, %v with %d source calls; want 1, nil, 2", n, err, calls.Load())
	}
}

// TestRotateDue_UnreleasedClaimLapses: a failed rotation whose release
// fails too leaves its claim, which excludes callers until it lapses; the
// credential then rotates without waiting an Interval.
func TestRotateDue_UnreleasedClaimLapses(t *testing.T) {
	svc, backend := newFaultRotationService(t)
	setFaultPolicy(t, svc, "k", 5)
	due := rotationEpoch.Add(time.Hour)
	source := svc.rotationSource
	svc.rotationSource = func(ctx context.Context, name string) (*secure.Buffer, error) {
		arm(&backend.failCAS, metaSuffix)
		return source(ctx, name)
	}

	_, err := svc.RotateDue(t.Context(), due)
	requireRotationOpError(t, err, "k", "write meta")
	backend.failCAS.Store(nil)
	svc.rotationSource = source

	if n, err := svc.RotateDue(t.Context(), due); err != nil || n != 0 {
		t.Fatalf("RotateDue under the unreleased claim = %d, %v; want 0, nil", n, err)
	}
	// The claim was acquired at rotationEpoch and lapses one lease later
	// on the service clock.
	svc.now = func() time.Time { return rotationEpoch.Add(rotationClaimLease) }
	if n, err := svc.RotateDue(t.Context(), due.Add(rotationClaimLease)); err != nil || n != 1 {
		t.Fatalf("RotateDue past the lapsed claim = %d, %v; want 1, nil", n, err)
	}
}

func TestWithRotationValueSource_Sets(t *testing.T) {
	var calls atomic.Int32
	svc := newRotationService(t, WithRotationValueSource(countingSource(&calls)))
	if svc.rotationSource == nil {
		t.Fatal("WithRotationValueSource did not set the source")
	}
	if newRotationService(t).rotationSource != nil {
		t.Fatal("a service built without the option has a source")
	}
}

// newFaultRotationService is newFaultBarrierService with a counting value
// source and the rotation clock pinned to rotationEpoch.
func newFaultRotationService(t *testing.T) (*CredentialService, *faultBackend) {
	t.Helper()
	svc, backend := newFaultBarrierService(t)
	var calls atomic.Int32
	svc.rotationSource = countingSource(&calls)
	svc.now = func() time.Time { return rotationEpoch }
	return svc, backend
}

// requireRotationOpError asserts err carries a *RotationError for name whose
// cause is an *OperationError for op, and returns that OperationError.
func requireRotationOpError(t *testing.T, err error, name, op string) *OperationError {
	t.Helper()
	var rotErr *RotationError
	if !errors.As(err, &rotErr) {
		t.Fatalf("error %v is not a *RotationError", err)
	}
	if rotErr.Name != name {
		t.Fatalf("RotationError.Name = %q, want %q", rotErr.Name, name)
	}
	var opErr *OperationError
	if !errors.As(err, &opErr) {
		t.Fatalf("error %v carries no *OperationError", err)
	}
	if opErr.Op != op {
		t.Fatalf("OperationError.Op = %q, want %q", opErr.Op, op)
	}
	return opErr
}

func TestRotationError_ErrorAndUnwrap(t *testing.T) {
	cause := ErrEmptyCredentialValue
	err := &RotationError{Name: "db-password", Cause: cause}
	if want := "credentials: scheduled rotation of db-password: " + cause.Error(); err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
	if err.Unwrap() != cause || !errors.Is(err, cause) {
		t.Fatalf("Unwrap() = %v, want %v", err.Unwrap(), cause)
	}
}

func TestRotateDue_ListFailureReported(t *testing.T) {
	svc, backend := newFaultRotationService(t)
	storeRotationCredential(t, svc, "k")
	arm(&backend.failList, credentialPrefix)

	n, err := svc.RotateDue(t.Context(), rotationEpoch.Add(time.Hour))
	var opErr *OperationError
	if !errors.As(err, &opErr) || opErr.Op != "list barrier" || !errors.Is(err, errInjectedFault) {
		t.Fatalf("RotateDue error = %v, want list barrier OperationError", err)
	}
	if n != 0 {
		t.Fatalf("rotated %d, want 0", n)
	}
}

func TestRotateDue_CancelledContextStops(t *testing.T) {
	var calls atomic.Int32
	svc := newRotationService(t, WithRotationValueSource(countingSource(&calls)))
	storeRotationCredential(t, svc, "k")
	if err := svc.SetRotationPolicy(t.Context(), "k", RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 2}); err != nil {
		t.Fatalf("SetRotationPolicy: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	n, err := svc.RotateDue(ctx, rotationEpoch.Add(time.Hour))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RotateDue error = %v, want context.Canceled", err)
	}
	if n != 0 || calls.Load() != 0 {
		t.Fatalf("rotated %d with %d source calls, want none", n, calls.Load())
	}
}

// TestRotation_CorruptMetaReported: a metadata record that does not decode
// fails both the scheduled rotation and a policy update, typed.
func TestRotation_CorruptMetaReported(t *testing.T) {
	svc, _ := newFaultRotationService(t)
	ctx := t.Context()
	storeRotationCredential(t, svc, "k")
	if err := svc.store.Put(ctx, metaKey("k"), []byte("{")); err != nil {
		t.Fatalf("Put corrupt meta: %v", err)
	}

	_, err := svc.RotateDue(ctx, rotationEpoch.Add(time.Hour))
	requireRotationOpError(t, err, "k", "decode meta")

	err = svc.SetRotationPolicy(ctx, "k", RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 2})
	var opErr *OperationError
	if !errors.As(err, &opErr) || opErr.Op != "decode meta" {
		t.Fatalf("SetRotationPolicy error = %v, want decode meta OperationError", err)
	}
}

func TestRotateDue_NilValueReported(t *testing.T) {
	nilSource := func(context.Context, string) (*secure.Buffer, error) { return nil, nil }
	svc := newRotationService(t, WithRotationValueSource(nilSource))
	ctx := t.Context()
	storeRotationCredential(t, svc, "k")
	if err := svc.SetRotationPolicy(ctx, "k", RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 2}); err != nil {
		t.Fatalf("SetRotationPolicy: %v", err)
	}

	n, err := svc.RotateDue(ctx, rotationEpoch.Add(time.Hour))
	var rotErr *RotationError
	if !errors.As(err, &rotErr) || !errors.Is(err, ErrEmptyCredentialValue) {
		t.Fatalf("RotateDue error = %v, want RotationError wrapping ErrEmptyCredentialValue", err)
	}
	if n != 0 {
		t.Fatalf("rotated %d, want 0", n)
	}
	// The credential stays due: no interval was claimed.
	next, err := svc.NextRotation(ctx, "k")
	if err != nil {
		t.Fatalf("NextRotation: %v", err)
	}
	if want := rotationEpoch.Add(time.Hour); !next.Equal(want) {
		t.Fatalf("next rotation = %v, want %v", next, want)
	}
}

// setFaultPolicy stores name and gives it an enabled one-hour policy.
func setFaultPolicy(t *testing.T, svc *CredentialService, name string, retain int) {
	t.Helper()
	storeRotationCredential(t, svc, name)
	if err := svc.SetRotationPolicy(t.Context(), name, RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: retain}); err != nil {
		t.Fatalf("SetRotationPolicy: %v", err)
	}
}

// countSource wraps svc's rotation source so calls counts its invocations.
func countSource(svc *CredentialService, calls *atomic.Int32) {
	source := svc.rotationSource
	svc.rotationSource = func(ctx context.Context, name string) (*secure.Buffer, error) {
		calls.Add(1)
		return source(ctx, name)
	}
}

func TestRotateDue_SettlementTimedFromRotationEnd(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fail   bool
		wantN  int
		settle string
	}{
		{name: "complete", wantN: 1, settle: "write meta"},
		{name: "release", fail: true, settle: "release rotation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, backend := newFaultRotationService(t)
			setFaultPolicy(t, svc, "k", 5)
			source := svc.rotationSource
			var returned time.Time
			svc.rotationSource = func(ctx context.Context, name string) (*secure.Buffer, error) {
				defer func() { returned = time.Now() }()
				if tc.fail {
					return nil, errInjectedFault
				}
				return source(ctx, name)
			}
			var deadlines []time.Time
			backend.onCAS = func(ctx context.Context, _ string) {
				if returned.IsZero() {
					return
				}
				// The retention cleanup runs under the caller's context,
				// which here has no deadline.
				if d, ok := ctx.Deadline(); ok {
					deadlines = append(deadlines, d)
				}
			}

			n, err := svc.RotateDue(t.Context(), rotationEpoch.Add(time.Hour))
			if n != tc.wantN || (err != nil) != tc.fail {
				t.Fatalf("RotateDue = %d, %v; want %d, failed %v", n, err, tc.wantN, tc.fail)
			}
			if len(deadlines) == 0 {
				t.Fatalf("no %s CompareAndSwap after the rotation returned", tc.settle)
			}
			// Every write after the rotation, the settlement among them,
			// has at least the full settle timeout from the rotation's end.
			for _, d := range deadlines {
				if d.Before(returned.Add(rotationSettleTimeout)) {
					t.Fatalf("%s deadline %v is under %v past the rotation's end %v", tc.settle, d, rotationSettleTimeout, returned)
				}
			}
			meta, err := svc.loadBarrierMeta(t.Context(), "k")
			if err != nil {
				t.Fatalf("loadBarrierMeta: %v", err)
			}
			if meta.Claim != nil {
				t.Fatalf("claim = %+v after %s; want none", meta.Claim, tc.settle)
			}
		})
	}
}

func TestRotateDue_LeaseTimedFromAcquisition(t *testing.T) {
	svc, _ := newFaultRotationService(t)
	setFaultPolicy(t, svc, "k", 5)
	due := rotationEpoch.Add(time.Hour)
	// The pass began at due and reaches k ten minutes later.
	acquired := due.Add(10 * time.Minute)
	svc.now = func() time.Time { return acquired }
	source := svc.rotationSource
	var claim *rotationClaim
	svc.rotationSource = func(ctx context.Context, name string) (*secure.Buffer, error) {
		meta, err := svc.loadBarrierMeta(ctx, name)
		if err != nil {
			return nil, err
		}
		claim = meta.Claim
		return source(ctx, name)
	}

	if n, err := svc.RotateDue(t.Context(), due); err != nil || n != 1 {
		t.Fatalf("RotateDue = %d, %v; want 1, nil", n, err)
	}
	if claim == nil || !claim.Until.Equal(acquired.Add(rotationClaimLease)) {
		t.Fatalf("claim during the rotation = %+v; want one until %v", claim, acquired.Add(rotationClaimLease))
	}
	next, err := svc.NextRotation(t.Context(), "k")
	if err != nil || !next.Equal(due.Add(time.Hour)) {
		t.Fatalf("NextRotation = %v, %v; want %v, nil", next, err, due.Add(time.Hour))
	}
}

func TestRotateDue_SourceBoundByLeaseExpiry(t *testing.T) {
	svc, _ := newFaultRotationService(t)
	setFaultPolicy(t, svc, "k", 5)
	acquired := rotationEpoch.Add(time.Hour)
	// The claim is taken at acquired; by the time the source starts the
	// clock reads the claim's expiry, so the source has no lease left.
	var reads atomic.Int32
	svc.now = func() time.Time {
		if reads.Add(1) == 1 {
			return acquired
		}
		return acquired.Add(rotationClaimLease)
	}
	var sourceErr error
	svc.rotationSource = func(ctx context.Context, _ string) (*secure.Buffer, error) {
		sourceErr = ctx.Err()
		return nil, sourceErr
	}

	n, err := svc.RotateDue(t.Context(), acquired)
	if n != 0 || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RotateDue = %d, %v; want 0 and a deadline error", n, err)
	}
	if !errors.Is(sourceErr, context.DeadlineExceeded) {
		t.Fatalf("source context error = %v; want its deadline at the lease's expiry", sourceErr)
	}
	meta, err := svc.loadBarrierMeta(t.Context(), "k")
	if err != nil {
		t.Fatalf("loadBarrierMeta: %v", err)
	}
	if meta.Claim != nil || !meta.NextRotation.Equal(acquired) {
		t.Fatalf("claim = %+v, next = %v; want the claim released and %v", meta.Claim, meta.NextRotation, acquired)
	}
}

func TestRotateDue_FailedCleanupRetriedWithoutSource(t *testing.T) {
	svc, backend := newFaultRotationService(t)
	setFaultPolicy(t, svc, "k", 1)
	var calls atomic.Int32
	countSource(svc, &calls)
	due := rotationEpoch.Add(time.Hour)
	arm(&backend.failDelete, versionKey("k", 1))

	n, err := svc.RotateDue(t.Context(), due)
	var rotErr *RotationError
	// A rotation whose cleanup failed is reported, and not counted.
	if n != 0 || !errors.As(err, &rotErr) || calls.Load() != 1 {
		t.Fatalf("RotateDue = %d, %v with %d source calls; want 0, a *RotationError, 1", n, err, calls.Load())
	}
	meta, err := svc.loadBarrierMeta(t.Context(), "k")
	if err != nil {
		t.Fatalf("loadBarrierMeta: %v", err)
	}
	if meta.PendingCleanup == "" || len(meta.History) != 1 {
		t.Fatalf("pending cleanup = %q, history = %d; want a marker and v1 kept", meta.PendingCleanup, len(meta.History))
	}
	// Still failing: the next check retries the cleanup alone.
	if n, err := svc.RotateDue(t.Context(), due); n != 0 || err == nil || calls.Load() != 1 {
		t.Fatalf("RotateDue under the fault = %d, %v with %d source calls; want 0, an error, 1", n, err, calls.Load())
	}

	backend.failDelete.Store(nil)
	if n, err := svc.RotateDue(t.Context(), due); n != 0 || err != nil || calls.Load() != 1 {
		t.Fatalf("RotateDue after recovery = %d, %v with %d source calls; want 0, nil, 1", n, err, calls.Load())
	}
	meta, err = svc.loadBarrierMeta(t.Context(), "k")
	if err != nil {
		t.Fatalf("loadBarrierMeta: %v", err)
	}
	if meta.PendingCleanup != "" || len(meta.History) != 0 || !meta.NextRotation.Equal(due.Add(time.Hour)) {
		t.Fatalf("pending cleanup = %q, history = %d, next = %v; want none, 0, %v",
			meta.PendingCleanup, len(meta.History), meta.NextRotation, due.Add(time.Hour))
	}
}

func TestRotateDue_PendingCleanupUnderDisabledPolicy(t *testing.T) {
	svc, backend := newFaultRotationService(t)
	setFaultPolicy(t, svc, "k", 1)
	var calls atomic.Int32
	countSource(svc, &calls)
	due := rotationEpoch.Add(time.Hour)
	arm(&backend.failDelete, versionKey("k", 1))
	if n, err := svc.RotateDue(t.Context(), due); n != 0 || err == nil {
		t.Fatalf("RotateDue = %d, %v; want 0 and an error", n, err)
	}
	backend.failDelete.Store(nil)
	if err := svc.SetRotationPolicy(t.Context(), "k", RotationPolicy{}); err != nil {
		t.Fatalf("SetRotationPolicy: %v", err)
	}

	// A disabled policy trims nothing, but the pending GC still runs and
	// the marker clears.
	if n, err := svc.RotateDue(t.Context(), due.Add(time.Hour)); n != 0 || err != nil || calls.Load() != 1 {
		t.Fatalf("RotateDue = %d, %v with %d source calls; want 0, nil, 1", n, err, calls.Load())
	}
	meta, err := svc.loadBarrierMeta(t.Context(), "k")
	if err != nil {
		t.Fatalf("loadBarrierMeta: %v", err)
	}
	if meta.PendingCleanup != "" || len(meta.History) != 1 {
		t.Fatalf("pending cleanup = %q, history = %d; want none and v1 kept", meta.PendingCleanup, len(meta.History))
	}
}

func TestRotateDue_HeldClaimDefersPendingCleanup(t *testing.T) {
	svc, backend := newFaultRotationService(t)
	setFaultPolicy(t, svc, "k", 1)
	due := rotationEpoch.Add(time.Hour)
	arm(&backend.failDelete, versionKey("k", 1))
	if n, err := svc.RotateDue(t.Context(), due); n != 0 || err == nil {
		t.Fatalf("RotateDue = %d, %v; want 0 and an error", n, err)
	}
	next := due.Add(time.Hour)
	held := &rotationClaim{ID: "other", Due: next, Until: rotationEpoch.Add(rotationClaimLease)}
	if err := svc.updateBarrierMeta(t.Context(), "k", "write meta", func(meta *credentialMeta) bool {
		meta.Claim = held
		return true
	}); err != nil {
		t.Fatalf("updateBarrierMeta: %v", err)
	}

	// The holder's rotation cleans up after itself; this caller, still
	// failing deletes, does not attempt it.
	if n, err := svc.RotateDue(t.Context(), next); n != 0 || err != nil {
		t.Fatalf("RotateDue under the held claim = %d, %v; want 0, nil", n, err)
	}
	meta, err := svc.loadBarrierMeta(t.Context(), "k")
	if err != nil {
		t.Fatalf("loadBarrierMeta: %v", err)
	}
	if meta.PendingCleanup == "" || meta.Claim == nil || meta.Claim.ID != "other" {
		t.Fatalf("pending cleanup = %q, claim = %+v; want both left", meta.PendingCleanup, meta.Claim)
	}
}

func TestRotateDue_ClaimFailureReported(t *testing.T) {
	svc, backend := newFaultRotationService(t)
	setFaultPolicy(t, svc, "k", 2)
	arm(&backend.failCAS, metaSuffix)

	n, err := svc.RotateDue(t.Context(), rotationEpoch.Add(time.Hour))
	requireRotationOpError(t, err, "k", "claim rotation")
	if !errors.Is(err, errInjectedFault) || n != 0 {
		t.Fatalf("RotateDue = %d, %v; want 0 and the injected fault", n, err)
	}
	if got := versionCount(t, svc, "k"); got != 1 {
		t.Fatalf("versions = %d, want 1: nothing may rotate without a claim", got)
	}
}

// A metadata record that cannot be read is not an absent one: the pass
// reports the due credential instead of taking it for one with no policy.
func TestRotateDue_MetaReadFailureReported(t *testing.T) {
	svc, backend := newFaultRotationService(t)
	setFaultPolicy(t, svc, "k", 2)
	due := rotationEpoch.Add(time.Hour)
	arm(&backend.failGet, metaKey("k"))

	n, err := svc.RotateDue(t.Context(), due)
	requireRotationOpError(t, err, "k", "read meta")
	if !errors.Is(err, errInjectedFault) || n != 0 {
		t.Fatalf("RotateDue = %d, %v; want 0 and the injected fault", n, err)
	}
	backend.failGet.Store(nil)
	if got := versionCount(t, svc, "k"); got != 1 {
		t.Fatalf("versions = %d, want 1: nothing may rotate on unread metadata", got)
	}

	if n, err := svc.RotateDue(t.Context(), due); n != 1 || err != nil {
		t.Fatalf("RotateDue after the fault = %d, %v; want 1, nil", n, err)
	}
}

func TestRotateDue_RotateFailureReported(t *testing.T) {
	svc, backend := newFaultRotationService(t)
	setFaultPolicy(t, svc, "k", 2)
	arm(&backend.failCAS, credentialPrefix+"k")

	n, err := svc.RotateDue(t.Context(), rotationEpoch.Add(time.Hour))
	requireRotationOpError(t, err, "k", "write current")
	if !errors.Is(err, errInjectedFault) || n != 0 {
		t.Fatalf("RotateDue = %d, %v; want 0 and the injected fault", n, err)
	}
}

// TestRotateDue_GCFailureReported: the rotation lands, but reaping an
// expired grace version fails; the failure is reported for the credential.
func TestRotateDue_GCFailureReported(t *testing.T) {
	svc, backend := newFaultBarrierService(t)
	var calls atomic.Int32
	svc.rotationSource = countingSource(&calls)
	ctx := t.Context()
	seedGraceVersion(t, svc, "k")
	expireBarrierGrace(t, svc, "k")
	if err := svc.SetRotationPolicy(ctx, "k", RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 3}); err != nil {
		t.Fatalf("SetRotationPolicy: %v", err)
	}
	arm(&backend.failDelete, versionKey("k", 1))

	n, err := svc.RotateDue(ctx, svc.now().Add(2*time.Hour))
	var rotErr *RotationError
	if !errors.As(err, &rotErr) || rotErr.Name != "k" || !errors.Is(err, errInjectedFault) {
		t.Fatalf("RotateDue error = %v, want RotationError for k wrapping the injected fault", err)
	}
	if n != 0 || calls.Load() != 1 {
		t.Fatalf("RotateDue = %d with %d source calls, want 0 and 1", n, calls.Load())
	}
}

// TestRotateDue_PruneFailureReported: with one version retained, the
// previous version is pruned after the rotation; a failed delete is
// reported and the version stays.
func TestRotateDue_PruneFailureReported(t *testing.T) {
	svc, backend := newFaultRotationService(t)
	setFaultPolicy(t, svc, "k", 1)
	arm(&backend.failDelete, versionKey("k", 1))

	n, err := svc.RotateDue(t.Context(), rotationEpoch.Add(time.Hour))
	var rotErr *RotationError
	if !errors.As(err, &rotErr) || rotErr.Name != "k" || !errors.Is(err, errInjectedFault) {
		t.Fatalf("RotateDue error = %v, want RotationError for k wrapping the injected fault", err)
	}
	if n != 0 {
		t.Fatalf("rotated %d, want 0", n)
	}
}

// TestRotateDue_PruneMetaWriteFailureReported: the prune deletes the excess
// version and then fails to write the trimmed index.
func TestRotateDue_PruneMetaWriteFailureReported(t *testing.T) {
	svc, backend := newFaultRotationService(t)
	setFaultPolicy(t, svc, "k", 1)
	// Fail the meta write only once the excess version key is gone, which
	// is after the rotation's own meta write.
	backend.onDelete = func(key string) {
		if strings.HasSuffix(key, versionKey("k", 1)) {
			arm(&backend.failCAS, metaSuffix)
		}
	}

	_, err := svc.RotateDue(t.Context(), rotationEpoch.Add(time.Hour))
	requireRotationOpError(t, err, "k", "write meta")
}

// TestSetRotationPolicy_MetaUpdate covers the compare-and-swap update of an
// existing metadata record: a lost race is retried, a record that keeps
// moving fails typed after rotationMetaRetries, and a write error surfaces.
func TestSetRotationPolicy_MetaUpdate(t *testing.T) {
	updated := RotationPolicy{Enabled: true, Interval: 2 * time.Hour, RetainVersions: 3}

	t.Run("lost race retried", func(t *testing.T) {
		svc, backend := newFaultRotationService(t)
		setFaultPolicy(t, svc, "k", 2)
		arm(&backend.loseCAS, metaSuffix)
		backend.casLosses.Store(1)

		if err := svc.SetRotationPolicy(t.Context(), "k", updated); err != nil {
			t.Fatalf("SetRotationPolicy: %v", err)
		}
		got, err := svc.RotationPolicy(t.Context(), "k")
		if err != nil || got != updated {
			t.Fatalf("RotationPolicy = %+v, %v; want %+v", got, err, updated)
		}
	})

	t.Run("retries exhausted", func(t *testing.T) {
		svc, backend := newFaultRotationService(t)
		setFaultPolicy(t, svc, "k", 2)
		arm(&backend.loseCAS, metaSuffix)
		backend.casLosses.Store(rotationMetaRetries)

		err := svc.SetRotationPolicy(t.Context(), "k", updated)
		var opErr *OperationError
		if !errors.As(err, &opErr) || opErr.Op != "write meta" || !errors.Is(err, ErrRotationMetaContended) {
			t.Fatalf("SetRotationPolicy error = %v, want write meta OperationError wrapping ErrRotationMetaContended", err)
		}
	})

	t.Run("write error", func(t *testing.T) {
		svc, backend := newFaultRotationService(t)
		setFaultPolicy(t, svc, "k", 2)
		arm(&backend.failCAS, metaSuffix)

		err := svc.SetRotationPolicy(t.Context(), "k", updated)
		var opErr *OperationError
		if !errors.As(err, &opErr) || opErr.Op != "write meta" || !errors.Is(err, errInjectedFault) {
			t.Fatalf("SetRotationPolicy error = %v, want write meta OperationError wrapping the injected fault", err)
		}
	})
}

// disableOnce returns a hook that disables name's policy the first time
// it sees a key ending in suffix, standing in for an operator's change
// landing between a metadata writer's read and its write.
func disableOnce(t *testing.T, svc *CredentialService, name, suffix string) func(string) {
	t.Helper()
	var fired atomic.Bool
	return func(key string) {
		if !strings.HasSuffix(key, suffix) || !fired.CompareAndSwap(false, true) {
			return
		}
		if err := svc.SetRotationPolicy(context.Background(), name, RotationPolicy{}); err != nil {
			t.Errorf("SetRotationPolicy: %v", err)
		}
	}
}

// requirePolicyDisabled asserts name's policy is disabled with no rotation
// scheduled.
func requirePolicyDisabled(t *testing.T, svc *CredentialService, name string) {
	t.Helper()
	policy, err := svc.RotationPolicy(t.Context(), name)
	if err != nil {
		t.Fatalf("RotationPolicy: %v", err)
	}
	next, err := svc.NextRotation(t.Context(), name)
	if err != nil {
		t.Fatalf("NextRotation: %v", err)
	}
	if policy.Enabled || !next.IsZero() {
		t.Fatalf("policy = %+v, next = %v; want the concurrent disable kept", policy, next)
	}
}

// TestRotateDue_PruneKeepsConcurrentPolicyChange: an operator disables the
// policy after the prune read the record and before it wrote the trimmed
// history; the prune's write must not re-enable it.
func TestRotateDue_PruneKeepsConcurrentPolicyChange(t *testing.T) {
	svc, backend := newFaultRotationService(t)
	setFaultPolicy(t, svc, "k", 1)
	backend.onDelete = disableOnce(t, svc, "k", versionKey("k", 1))

	n, err := svc.RotateDue(t.Context(), rotationEpoch.Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("RotateDue = %d, %v; want 1, nil", n, err)
	}
	requirePolicyDisabled(t, svc, "k")
	if got := versionCount(t, svc, "k"); got != 1 {
		t.Fatalf("versions = %d, want 1: the excess version is still unindexed", got)
	}
}

// TestGCExpiredVersions_KeepsConcurrentPolicyChange: the same race against
// the grace-period reaper.
func TestGCExpiredVersions_KeepsConcurrentPolicyChange(t *testing.T) {
	svc, backend := newFaultRotationService(t)
	ctx := t.Context()
	seedGraceVersion(t, svc, "k")
	expireBarrierGrace(t, svc, "k")
	if err := svc.SetRotationPolicy(ctx, "k", RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 3}); err != nil {
		t.Fatalf("SetRotationPolicy: %v", err)
	}
	backend.onDelete = disableOnce(t, svc, "k", versionKey("k", 1))

	reaped, err := svc.GCExpiredVersions(ctx, "k")
	if err != nil || reaped != 1 {
		t.Fatalf("GCExpiredVersions = %d, %v; want 1, nil", reaped, err)
	}
	requirePolicyDisabled(t, svc, "k")
	if got := versionCount(t, svc, "k"); got != 1 {
		t.Fatalf("versions = %d, want 1: the reaped version is still indexed", got)
	}
}

// TestRotateCredential_KeepsConcurrentPolicyChange: the same race against
// a rotation, between its read of the record and its version write.
func TestRotateCredential_KeepsConcurrentPolicyChange(t *testing.T) {
	svc, backend := newFaultRotationService(t)
	setFaultPolicy(t, svc, "k", 3)
	backend.onPut = disableOnce(t, svc, "k", credentialPrefix+"k")

	res, err := svc.RotateCredential(t.Context(), "k", RotateOptions{NewValue: []byte("k-next"), retainPrevious: true})
	if err != nil {
		t.Fatalf("RotateCredential: %v", err)
	}
	requirePolicyDisabled(t, svc, "k")
	if res.NewVersion != 2 || versionCount(t, svc, "k") != 2 {
		t.Fatalf("rotation = %+v with %d versions, want v2 of 2", res, versionCount(t, svc, "k"))
	}
}
