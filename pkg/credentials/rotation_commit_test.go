package credentials

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// A manual rotation that lands between a scheduled rotation's read and its
// commit must not lose the version it retained: the scheduled rotation
// commits nothing, and the next pass rotates on from the manual version.
func TestRotateDue_ManualRotationDuringTheScheduledCommitKeepsTheRetainedVersion(t *testing.T) {
	svc, backend := newFaultRotationService(t)
	setFaultPolicy(t, svc, "k", 5)
	var calls atomic.Int32
	countSource(svc, &calls)
	before, err := svc.loadBarrierMeta(t.Context(), "k")
	if err != nil {
		t.Fatalf("loadBarrierMeta: %v", err)
	}
	prev, next := rotationVersions(before.CurrentVersion, true)
	due := rotationEpoch.Add(time.Hour)

	// The scheduled rotation has read the record once it starts writing
	// the current value: the manual rotation runs before its metadata is
	// written.
	var (
		sawCurrent, fired bool
		manual            *RotateResult
		manualErr         error
	)
	backend.onCAS = func(ctx context.Context, key string) {
		if fired {
			return
		}
		switch key {
		case credentialPrefix + "k":
			sawCurrent = true
		case metaKey("k"):
			if !sawCurrent {
				return
			}
			fired = true
			manual, manualErr = svc.RotateCredential(ctx, "k", RotateOptions{NewValue: []byte("k-manual"), GracePeriod: time.Hour})
		}
	}
	n, err := svc.RotateDue(t.Context(), due)
	backend.onCAS = nil
	if !fired {
		t.Fatal("the manual rotation never ran inside the scheduled commit")
	}
	if manualErr != nil {
		t.Fatalf("manual RotateCredential: %v", manualErr)
	}
	if manual.NewVersion != next {
		t.Fatalf("manual NewVersion = %d, want %d", manual.NewVersion, next)
	}
	if n != 0 || !errors.Is(err, ErrRotationClaimLost) {
		t.Fatalf("RotateDue = %d, %v; want 0, ErrRotationClaimLost: the manual rotation took version %d", n, err, next)
	}
	requireVersionValue(t, svc, prev, "k-initial")
	requireCurrentValue(t, svc, "k-manual")
	if got, err := svc.loadBarrierMeta(t.Context(), "k"); err != nil || got.CurrentVersion != next {
		t.Fatalf("meta after the lost rotation = %+v, %v; want version %d", got, err, next)
	}

	if n, err := svc.RotateDue(t.Context(), due); n != 1 || err != nil {
		t.Fatalf("next RotateDue = %d, %v; want 1, nil", n, err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("value source called %d times, want 2: the lost rotation's staged value is not replayed", got)
	}
	requireVersionValue(t, svc, prev, "k-initial")
	requireVersionValue(t, svc, next, "k-manual")
	requireVersionValue(t, svc, next+1, "k-rotated-2")
	requireCurrentValue(t, svc, "k-rotated-2")
	if _, err := svc.store.Get(t.Context(), stagedKey("k", next)); !errors.Is(err, seal.ErrSecretNotFound) {
		t.Fatalf("staged value of the lost rotation: %v, want not found", err)
	}
}

// A value stored after a scheduled rotation failed to commit is the
// credential's value: the next pass must not replace it with the value the
// failed rotation staged, and retains it as the previous version.
func TestRotateDue_StoreAfterAFailedScheduledWriteIsNotOverwrittenByTheStagedValue(t *testing.T) {
	cases := []struct {
		name  string
		store func(svc *CredentialService, ctx context.Context, value []byte) error
	}{
		{"StoreCredential", func(svc *CredentialService, ctx context.Context, value []byte) error {
			return svc.StoreCredential(ctx, "k", value)
		}},
		{"SubmitCredential", func(svc *CredentialService, ctx context.Context, value []byte) error {
			return svc.SubmitCredential(ctx, "k", value)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, backend := newFaultRotationService(t)
			setFaultPolicy(t, svc, "k", 5)
			var calls atomic.Int32
			countSource(svc, &calls)
			before, err := svc.loadBarrierMeta(t.Context(), "k")
			if err != nil {
				t.Fatalf("loadBarrierMeta: %v", err)
			}
			prev, next := rotationVersions(before.CurrentVersion, true)
			due := rotationEpoch.Add(time.Hour)

			arm(&backend.failCAS, credentialPrefix+"k")
			if n, err := svc.RotateDue(t.Context(), due); n != 0 || !errors.Is(err, errInjectedFault) {
				t.Fatalf("RotateDue under the fault = %d, %v; want 0, the injected fault", n, err)
			}
			backend.failCAS.Store(nil)
			requireCurrentValue(t, svc, "k-initial")

			if err := tc.store(svc, t.Context(), []byte("k-replacement")); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			requireCurrentValue(t, svc, "k-replacement")

			if n, err := svc.RotateDue(t.Context(), due); n != 1 || err != nil {
				t.Fatalf("RotateDue after the store = %d, %v; want 1, nil", n, err)
			}
			if got := calls.Load(); got != 2 {
				t.Fatalf("value source called %d times, want 2: the staged value is not replayed over a stored one", got)
			}
			requireCurrentValue(t, svc, "k-rotated-2")
			requireVersionValue(t, svc, prev, "k-replacement")
			requireVersionValue(t, svc, next, "k-rotated-2")
			if got, err := svc.loadBarrierMeta(t.Context(), "k"); err != nil || got.CurrentVersion != next {
				t.Fatalf("meta after the rotation = %+v, %v; want version %d", got, err, next)
			}
		})
	}
}

// idempotentSource makes the service's value source honour the idempotency
// contract: a retry of an operation gets the value the operation already
// issued, and the underlying source runs once per operation. It records
// every operation it is asked for.
func idempotentSource(svc *CredentialService, calls *atomic.Int32, ops *[]string) {
	source := svc.rotationSource
	var mu sync.Mutex
	issued := map[string][]byte{}
	svc.rotationSource = func(ctx context.Context, name, op string) (*secure.Buffer, error) {
		mu.Lock()
		defer mu.Unlock()
		*ops = append(*ops, op)
		if value, ok := issued[op]; ok {
			return secure.NewBufferFromBytes(bytes.Clone(value))
		}
		calls.Add(1)
		buf, err := source(ctx, name, op)
		if err != nil {
			return nil, err
		}
		if err := buf.WithBytes(func(p []byte) error {
			issued[op] = bytes.Clone(p)
			return nil
		}); err != nil {
			buf.Destroy()
			return nil, err
		}
		return buf, nil
	}
}

func TestRotateDue_StoreEndsTheSupersededRotationOperation(t *testing.T) {
	cases := []struct {
		name  string
		fault func(b *faultBackend, next uint64)
	}{
		{"commit fails after staging", func(b *faultBackend, _ uint64) { arm(&b.failCAS, credentialPrefix+"k") }},
		{"staging fails", func(b *faultBackend, next uint64) { arm(&b.failCAS, stagedKey("k", next)) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, backend := newFaultRotationService(t)
			setFaultPolicy(t, svc, "k", 5)
			var calls atomic.Int32
			var ops []string
			idempotentSource(svc, &calls, &ops)
			before, err := svc.loadBarrierMeta(t.Context(), "k")
			if err != nil {
				t.Fatalf("loadBarrierMeta: %v", err)
			}
			prev, next := rotationVersions(before.CurrentVersion, true)
			due := rotationEpoch.Add(time.Hour)

			tc.fault(backend, next)
			if n, err := svc.RotateDue(t.Context(), due); n != 0 || !errors.Is(err, errInjectedFault) {
				t.Fatalf("RotateDue under the fault = %d, %v; want 0, the injected fault", n, err)
			}
			backend.failCAS.Store(nil)
			requireCurrentValue(t, svc, "k-initial")

			if err := svc.StoreCredential(t.Context(), "k", []byte("k-replacement")); err != nil {
				t.Fatalf("StoreCredential: %v", err)
			}
			requireCurrentValue(t, svc, "k-replacement")
			if got, err := svc.loadBarrierMeta(t.Context(), "k"); err != nil || got.RotationOp != "" || got.Staged != 0 {
				t.Fatalf("meta after the store = %+v, %v; want no pending rotation operation or staged value", got, err)
			}

			if n, err := svc.RotateDue(t.Context(), due); n != 1 || err != nil {
				t.Fatalf("RotateDue after the store = %d, %v; want 1, nil", n, err)
			}
			if len(ops) != 2 || ops[0] == ops[1] {
				t.Fatalf("value source operations = %q; want two distinct operations", ops)
			}
			if got := calls.Load(); got != 2 {
				t.Fatalf("value source issued %d values, want 2: the superseded operation's value is not replayed", got)
			}
			requireCurrentValue(t, svc, "k-rotated-2")
			requireVersionValue(t, svc, prev, "k-replacement")
			requireVersionValue(t, svc, next, "k-rotated-2")
		})
	}
}

// A replacement that lands while the scheduled rotation is inside its
// value source ends that rotation's operation: the value the source then
// returns is never staged or committed over the replacement.
func TestRotateDue_ReplacementDuringTheValueSourceFencesTheClaim(t *testing.T) {
	cases := []struct {
		name    string
		replace func(ctx context.Context, svc *CredentialService) error
	}{
		{"StoreCredential", func(ctx context.Context, svc *CredentialService) error {
			return svc.StoreCredential(ctx, "k", []byte("k-replacement"))
		}},
		{"SubmitCredential", func(ctx context.Context, svc *CredentialService) error {
			return svc.SubmitCredential(ctx, "k", []byte("k-replacement"))
		}},
		{"RotateCredential", func(ctx context.Context, svc *CredentialService) error {
			_, err := svc.RotateCredential(ctx, "k", RotateOptions{NewValue: []byte("k-replacement"), GracePeriod: time.Hour})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newFaultRotationService(t)
			setFaultPolicy(t, svc, "k", 5)
			entered := make(chan struct{})
			release := make(chan struct{})
			source := svc.rotationSource
			var once sync.Once
			svc.rotationSource = func(ctx context.Context, name, op string) (*secure.Buffer, error) {
				blocked := false
				once.Do(func() { blocked = true })
				if blocked {
					close(entered)
					<-release
				}
				return source(ctx, name, op)
			}
			type result struct {
				n   int
				err error
			}
			done := make(chan result, 1)
			go func() {
				n, err := svc.RotateDue(t.Context(), rotationEpoch.Add(time.Hour))
				done <- result{n, err}
			}()
			<-entered
			if err := tc.replace(t.Context(), svc); err != nil {
				close(release)
				<-done
				t.Fatalf("replacement: %v", err)
			}
			replaced, err := svc.loadBarrierMeta(t.Context(), "k")
			if err != nil {
				close(release)
				<-done
				t.Fatalf("loadBarrierMeta: %v", err)
			}
			close(release)
			got := <-done
			if got.n != 0 || !errors.Is(got.err, ErrRotationClaimLost) {
				t.Fatalf("RotateDue across the replacement = %d, %v; want 0, ErrRotationClaimLost", got.n, got.err)
			}
			requireCurrentValue(t, svc, "k-replacement")
			after, err := svc.loadBarrierMeta(t.Context(), "k")
			if err != nil {
				t.Fatalf("loadBarrierMeta: %v", err)
			}
			if after.CurrentVersion != replaced.CurrentVersion || after.Staged != 0 {
				t.Fatalf("meta after the fenced rotation = version %d, staged %d; want version %d, nothing staged",
					after.CurrentVersion, after.Staged, replaced.CurrentVersion)
			}
		})
	}
}
