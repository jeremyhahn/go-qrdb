// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package credentials

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// requireBufferEquals fails the test unless buf holds exactly want. The
// comparison runs inside the buffer's own borrow, so the plaintext is never
// copied out of protected memory to be asserted on.
func requireBufferEquals(t *testing.T, buf *secure.Buffer, want string) {
	t.Helper()
	if buf == nil {
		t.Fatalf("buffer is nil, want %q", want)
	}
	if err := buf.WithBytes(func(b []byte) error {
		if !bytes.Equal(b, []byte(want)) {
			t.Errorf("buffer holds a different value than %q", want)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithBytes: %v", err)
	}
}

func TestRetrieveCredentialBuffer_Manual_ReturnsProtectedValue(t *testing.T) {
	svc := newManualService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("s3cret")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	buf, err := svc.RetrieveCredentialBuffer(ctx, "k")
	if err != nil {
		t.Fatalf("RetrieveCredentialBuffer: %v", err)
	}
	defer buf.Destroy()
	requireBufferEquals(t, buf, "s3cret")
}

func TestRetrieveCredentialBuffer_Manual_BuffersAreIndependent(t *testing.T) {
	svc := newManualService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("shared")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	first, err := svc.RetrieveCredentialBuffer(ctx, "k")
	if err != nil {
		t.Fatalf("first retrieve: %v", err)
	}
	second, err := svc.RetrieveCredentialBuffer(ctx, "k")
	if err != nil {
		t.Fatalf("second retrieve: %v", err)
	}
	defer second.Destroy()

	// Destroying one caller's buffer must not disturb another's, nor the
	// stored credential.
	first.Destroy()
	requireBufferEquals(t, second, "shared")
	third, err := svc.RetrieveCredentialBuffer(ctx, "k")
	if err != nil {
		t.Fatalf("retrieve after destroy: %v", err)
	}
	defer third.Destroy()
	requireBufferEquals(t, third, "shared")
}

func TestRetrieveCredentialBuffer_Manual_BlocksUntilSubmitted(t *testing.T) {
	svc := newManualService(t)
	ctx := context.Background()

	const waiters = 3
	bufs := make([]*secure.Buffer, waiters)
	errs := make([]error, waiters)
	var wg sync.WaitGroup
	for i := range waiters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bufs[i], errs[i] = svc.RetrieveCredentialBuffer(ctx, "late")
		}()
	}

	// Wait until every retriever has registered, then submit.
	deadline := time.Now().Add(5 * time.Second)
	for {
		svc.waitersMu.Lock()
		n := len(svc.waiters["late"])
		svc.waitersMu.Unlock()
		if n == waiters {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d waiters registered", n, waiters)
		}
		time.Sleep(time.Millisecond)
	}
	if err := svc.SubmitCredential(ctx, "late", []byte("delivered")); err != nil {
		t.Fatalf("SubmitCredential: %v", err)
	}
	wg.Wait()

	for i := range waiters {
		if errs[i] != nil {
			t.Fatalf("waiter %d: %v", i, errs[i])
		}
		requireBufferEquals(t, bufs[i], "delivered")
	}
	// Each waiter owns its own copy: destroying one leaves the rest readable.
	bufs[0].Destroy()
	requireBufferEquals(t, bufs[1], "delivered")
	bufs[1].Destroy()
	bufs[2].Destroy()
}

func TestRetrieveCredentialBuffer_Manual_CancelReturnsContextError(t *testing.T) {
	svc := newManualService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	buf, err := svc.RetrieveCredentialBuffer(ctx, "never")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if buf != nil {
		t.Error("a cancelled retrieve returned a buffer")
	}
}

func TestRetrieveCredentialBuffer_Barrier_ReturnsProtectedValue(t *testing.T) {
	svc := newBarrierService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("barrier-value")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	buf, err := svc.RetrieveCredentialBuffer(ctx, "k")
	if err != nil {
		t.Fatalf("RetrieveCredentialBuffer: %v", err)
	}
	defer buf.Destroy()
	requireBufferEquals(t, buf, "barrier-value")
}

func TestRetrieveCredentialBuffer_Barrier_NotFound(t *testing.T) {
	svc := newBarrierService(t)
	buf, err := svc.RetrieveCredentialBuffer(context.Background(), "absent")
	if !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("err = %v, want ErrCredentialNotFound", err)
	}
	if buf != nil {
		t.Error("a missing credential returned a buffer")
	}
}

func TestRetrieveCredentialBuffer_EmptyNameAndInvalidStrategy(t *testing.T) {
	svc := newManualService(t)
	if _, err := svc.RetrieveCredentialBuffer(context.Background(), " "); !errors.Is(err, ErrEmptyCredentialName) {
		t.Fatalf("empty name: err = %v, want ErrEmptyCredentialName", err)
	}
	svc.strategy = "bogus"
	if _, err := svc.RetrieveCredentialBuffer(context.Background(), "k"); !errors.Is(err, ErrInvalidStrategy) {
		t.Fatalf("bogus strategy: err = %v, want ErrInvalidStrategy", err)
	}
}

func TestRetrieveCredentialVersionBuffer_Manual_CurrentAndGraced(t *testing.T) {
	svc := newManualService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("old")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	if _, err := svc.RotateCredential(ctx, "k", RotateOptions{NewValue: []byte("new"), GracePeriod: time.Minute}); err != nil {
		t.Fatalf("RotateCredential: %v", err)
	}

	for _, tc := range []struct {
		version uint64
		want    string
	}{{0, "new"}, {2, "new"}, {1, "old"}} {
		buf, err := svc.RetrieveCredentialVersionBuffer(ctx, "k", tc.version)
		if err != nil {
			t.Fatalf("version %d: %v", tc.version, err)
		}
		requireBufferEquals(t, buf, tc.want)
		buf.Destroy()
	}
}

func TestRetrieveCredentialVersionBuffer_Manual_Refusals(t *testing.T) {
	svc := newManualService(t)
	ctx := context.Background()
	if _, err := svc.RetrieveCredentialVersionBuffer(ctx, " ", 1); !errors.Is(err, ErrEmptyCredentialName) {
		t.Fatalf("empty name: err = %v, want ErrEmptyCredentialName", err)
	}
	if err := svc.StoreCredential(ctx, "k", []byte("old")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	if _, err := svc.RotateCredential(ctx, "k", RotateOptions{NewValue: []byte("new"), GracePeriod: time.Minute}); err != nil {
		t.Fatalf("RotateCredential: %v", err)
	}
	if _, err := svc.RetrieveCredentialVersionBuffer(ctx, "k", 42); !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("unknown version: err = %v, want ErrVersionNotFound", err)
	}
	expireGrace(svc, "k")
	if _, err := svc.RetrieveCredentialVersionBuffer(ctx, "k", 1); !errors.Is(err, ErrVersionExpired) {
		t.Fatalf("expired version: err = %v, want ErrVersionExpired", err)
	}
}

func TestRetrieveCredentialVersionBuffer_Barrier_CurrentAndGraced(t *testing.T) {
	svc := newBarrierService(t)
	ctx := context.Background()
	if err := svc.StoreCredential(ctx, "k", []byte("old")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	if _, err := svc.RotateCredential(ctx, "k", RotateOptions{NewValue: []byte("new"), GracePeriod: time.Minute}); err != nil {
		t.Fatalf("RotateCredential: %v", err)
	}

	for _, tc := range []struct {
		version uint64
		want    string
	}{{0, "new"}, {2, "new"}, {1, "old"}} {
		buf, err := svc.RetrieveCredentialVersionBuffer(ctx, "k", tc.version)
		if err != nil {
			t.Fatalf("version %d: %v", tc.version, err)
		}
		requireBufferEquals(t, buf, tc.want)
		buf.Destroy()
	}
	if _, err := svc.RetrieveCredentialVersionBuffer(ctx, "k", 99); !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("unknown version: err = %v, want ErrVersionNotFound", err)
	}
	expireBarrierGrace(t, svc, "k")
	if _, err := svc.RetrieveCredentialVersionBuffer(ctx, "k", 1); !errors.Is(err, ErrVersionExpired) {
		t.Fatalf("expired version: err = %v, want ErrVersionExpired", err)
	}
}

func TestRetrieveCredentialVersionBuffer_InvalidStrategy(t *testing.T) {
	svc := newManualService(t)
	svc.strategy = "bogus"
	if _, err := svc.RetrieveCredentialVersionBuffer(context.Background(), "k", 1); !errors.Is(err, ErrInvalidStrategy) {
		t.Fatalf("err = %v, want ErrInvalidStrategy", err)
	}
}
