// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package dek

import (
	"bytes"
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/keywrap"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// addrReader is an entropy source that records where it was asked to write.
type addrReader struct{ at *byte }

func (r *addrReader) Read(p []byte) (int, error) {
	if len(p) > 0 {
		r.at = &p[0]
	}
	return rand.Read(p)
}

// addrProvider is a key-wrap provider that records where the key it was
// handed lives, and the bytes of it.
type addrProvider struct {
	at  *byte
	key []byte
}

func (p *addrProvider) ProviderID() string { return keywrap.SoftwareProviderID }

func (p *addrProvider) Wrap(_ context.Context, _ string, dek []byte) ([]byte, error) {
	if len(dek) > 0 {
		p.at = &dek[0]
	}
	p.key = append([]byte(nil), dek...)
	return append([]byte(nil), dek...), nil
}

func (p *addrProvider) Unwrap(_ context.Context, _ string, wrapped []byte) ([]byte, error) {
	return append([]byte(nil), wrapped...), nil
}

func registryOf(t *testing.T, p keywrap.Provider) keywrap.Registry {
	t.Helper()
	r := keywrap.NewRegistry()
	if err := r.Register(keywrap.SoftwareProviderID, func() (keywrap.Provider, error) { return p, nil }); err != nil {
		t.Fatal(err)
	}
	return r
}

// TestWrapKeyGeneratesAndWrapsInTheSameProtectedBuffer proves the new key is
// generated into one buffer and handed to the provider from that same buffer:
// no heap copy of the key exists between generation and wrapping.
func TestWrapKeyGeneratesAndWrapsInTheSameProtectedBuffer(t *testing.T) {
	k, _ := newTestKeyring(t)
	reader := &addrReader{}
	provider := &addrProvider{}
	k.random = reader
	k.keyWrap = registryOf(t, provider)

	wrapped, providerID, keyRef, err := k.wrapKey(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	if reader.at == nil || reader.at != provider.at {
		t.Fatalf("key generated at %p but wrapped from %p: it was copied between the two", reader.at, provider.at)
	}
	if len(provider.key) != KeySize || bytes.Equal(provider.key, make([]byte, KeySize)) {
		t.Fatalf("the provider was handed %d bytes of %v, want a %d-byte non-zero key", len(provider.key), provider.key, KeySize)
	}
	if !bytes.Equal(wrapped, provider.key) || providerID != keywrap.SoftwareProviderID || keyRef != "acme" {
		t.Fatalf("wrapKey = (%x, %q, %q), want the provider's output under software/acme", wrapped, providerID, keyRef)
	}
}

// wrapToleranceBound is how long a wrap may take before the tolerance test
// calls it wedged. A wrap takes microseconds; a deadlocked one never returns.
const wrapToleranceBound = 30 * time.Second

// protectedMemoryProvider is a key-wrap provider that allocates, opens and
// destroys protected memory of its own inside Wrap, as a hardware-backed or
// KMS provider may, before wrapping like addrProvider.
type protectedMemoryProvider struct {
	addrProvider
	inner *secure.Secret
}

func (p *protectedMemoryProvider) Wrap(ctx context.Context, keyRef string, dek []byte) ([]byte, error) {
	scratch, err := secure.NewBuffer(len(dek))
	if err != nil {
		return nil, err
	}
	defer scratch.Destroy()
	opened, err := p.inner.Open(ctx)
	if err != nil {
		return nil, err
	}
	opened.Destroy()
	return p.addrProvider.Wrap(ctx, keyRef, dek)
}

// TestWrapKeyToleratesAProviderThatAllocatesProtectedMemory: Wrap runs inside
// the new key's borrow, and a provider that uses protected memory of its own
// there must complete, with the armed guard silent, because none of it
// reenters the key's buffer (go-qrdb#145). The wrap runs under a bounded wait
// so a deadlock fails the test instead of hanging the package.
func TestWrapKeyToleratesAProviderThatAllocatesProtectedMemory(t *testing.T) {
	inner, err := secure.NewSecret(context.Background(), []byte("the provider's own secret"))
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	defer inner.Destroy()

	k, _ := newTestKeyring(t)
	reader := &addrReader{}
	provider := &protectedMemoryProvider{inner: inner}
	k.random = reader
	k.keyWrap = registryOf(t, provider)

	stop := secure.GuardBorrows(t.Errorf)
	done := make(chan error, 1)
	go func() {
		// A guard violation Goexits this goroutine; the deferred close still
		// unblocks the wait, and stop reports it.
		defer close(done)
		_, _, _, err := k.wrapKey(context.Background(), "acme")
		done <- err
	}()
	select {
	case err := <-done:
		stop()
		if err != nil {
			t.Fatalf("wrapKey: %v", err)
		}
	case <-time.After(wrapToleranceBound):
		stop()
		t.Fatalf("wrapKey did not return within %v: a provider using protected memory "+
			"inside the key's borrow wedged it", wrapToleranceBound)
	}
	if reader.at == nil || reader.at != provider.at {
		t.Fatalf("key generated at %p but wrapped from %p: it was copied between the two", reader.at, provider.at)
	}
}
