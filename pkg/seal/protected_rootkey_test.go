// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// protectedBytes copies a protected root key out for comparison and destroys
// the buffer. Test-only: production code never takes the key out of the
// buffer, which is the property these tests exist to hold.
func protectedBytes(t *testing.T, buf *secure.Buffer) []byte {
	t.Helper()
	defer buf.Destroy()
	out, err := secure.CopyOut(buf)
	if err != nil {
		t.Fatalf("CopyOut: %v", err)
	}
	return out
}

func newTestGCM(t *testing.T, randomNonce bool) cipher.AEAD {
	t.Helper()
	if !randomNonce {
		// cipher.NewGCM (the caller-nonce construction) is refused under
		// GODEBUG=fips140=only, and no approved way exists to build one
		// here instead -- see skipIfFIPS in unsealkey_test.go, which this
		// mirrors, and docs/seal/fips140-mode.md "Skipped tests under
		// fips140=only".
		skipIfFIPS(t)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	var aead cipher.AEAD
	if randomNonce {
		aead, err = cipher.NewGCMWithRandomNonce(block)
	} else {
		aead, err = cipher.NewGCM(block)
	}
	if err != nil {
		t.Fatal(err)
	}
	return aead
}

// sealForTest produces the (nonce, ciphertext) pair openWrapped consumes, for
// either nonce convention.
func sealForTest(t *testing.T, aead cipher.AEAD, plaintext, aad []byte) (nonce, ciphertext []byte) {
	t.Helper()
	if aead.NonceSize() > 0 {
		nonce = make([]byte, aead.NonceSize())
		if _, err := rand.Read(nonce); err != nil {
			t.Fatal(err)
		}
		return nonce, aead.Seal(nil, nonce, plaintext, aad)
	}
	// #nosec G407 -- NonceSize() is 0 only for cipher.NewGCMWithRandomNonce, which samples a fresh 96-bit nonce inside the FIPS module on every Seal and prepends it to the frame. gosec reads the nil as a hardcoded IV; passing anything else makes Seal panic.
	frame := aead.Seal(nil, nil, plaintext, aad)
	const wireNonce = 12
	return frame[:wireNonce], frame[wireNonce:]
}

func TestOpenWrapped_DecryptsIntoProtectedMemory(t *testing.T) {
	eachNonceConvention(t, func(t *testing.T, aead cipher.AEAD) {
		want := bytes.Repeat([]byte{0x5a}, 32)
		aad := []byte("root-key-path")
		nonce, ct := sealForTest(t, aead, want, aad)

		buf, err := openWrapped(aead, nonce, ct, aad)
		if err != nil {
			t.Fatalf("openWrapped: %v", err)
		}
		if got := protectedBytes(t, buf); !bytes.Equal(got, want) {
			t.Fatalf("root key = %x, want %x", got, want)
		}
	})
}

// eachNonceConvention runs test once per AEAD nonce convention openWrapped
// accepts. The caller-nonce subtest skips under fips140=only (newTestGCM), so
// the module-nonce subtest is the one that runs in a FIPS deployment.
func eachNonceConvention(t *testing.T, test func(t *testing.T, aead cipher.AEAD)) {
	t.Helper()
	for _, tc := range []struct {
		name        string
		randomNonce bool
	}{
		{name: "caller nonce", randomNonce: false},
		{name: "module nonce", randomNonce: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			test(t, newTestGCM(t, tc.randomNonce))
		})
	}
}

func TestOpenWrapped_AuthenticationFailureIsKeyMismatch(t *testing.T) {
	eachNonceConvention(t, func(t *testing.T, aead cipher.AEAD) {
		aad := []byte("root-key-path")
		nonce, ct := sealForTest(t, aead, bytes.Repeat([]byte{1}, 32), aad)
		ct[0] ^= 0xff

		if _, err := openWrapped(aead, nonce, ct, aad); !errors.Is(err, ErrUnsealKeyMismatch) {
			t.Fatalf("err = %v, want ErrUnsealKeyMismatch", err)
		}
	})
}

func TestOpenWrapped_CiphertextShorterThanOverheadIsKeyMismatch(t *testing.T) {
	eachNonceConvention(t, func(t *testing.T, aead cipher.AEAD) {
		if _, err := openWrapped(aead, make([]byte, aead.NonceSize()), []byte{1, 2, 3}, nil); !errors.Is(err, ErrUnsealKeyMismatch) {
			t.Fatalf("err = %v, want ErrUnsealKeyMismatch", err)
		}
	})
}

// allocatingAEAD opens correctly but ignores the destination it is handed and
// returns plaintext in a slice of its own -- ordinary heap memory.
type allocatingAEAD struct {
	cipher.AEAD
	returned []byte
}

func (a *allocatingAEAD) Open(_, nonce, ciphertext, aad []byte) ([]byte, error) {
	out, err := a.AEAD.Open(nil, nonce, ciphertext, aad)
	a.returned = out
	return out, err
}

// TestOpenWrapped_RefusesPlaintextOutsideProtectedMemory pins the in-place
// assertion: an AEAD that decrypts into memory it allocated would put the root
// key on the heap, so the result is wiped and refused rather than accepted.
func TestOpenWrapped_RefusesPlaintextOutsideProtectedMemory(t *testing.T) {
	eachNonceConvention(t, func(t *testing.T, inner cipher.AEAD) {
		aad := []byte("root-key-path")
		nonce, ct := sealForTest(t, inner, bytes.Repeat([]byte{0x7e}, 32), aad)

		aead := &allocatingAEAD{AEAD: inner}
		_, err := openWrapped(aead, nonce, ct, aad)
		var unprotected *UnprotectedDecryptError
		if !errors.As(err, &unprotected) {
			t.Fatalf("err = %v (%T), want *UnprotectedDecryptError", err, err)
		}
		if unprotected.Error() == "" {
			t.Error("empty error string")
		}
		if !bytes.Equal(aead.returned, make([]byte, len(aead.returned))) {
			t.Fatal("the heap plaintext the AEAD returned was not wiped")
		}
	})
}
