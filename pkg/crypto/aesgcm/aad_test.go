// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package aesgcm

import (
	"bytes"
	"errors"
	"testing"
)

func TestEncryptWithAADRoundTrip(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	aad := []byte("tenant=acme|path=db/password|v=3")
	ct, err := EncryptWithAAD(key, []byte("s3cret"), aad)
	if err != nil {
		t.Fatalf("EncryptWithAAD: %v", err)
	}
	pt, err := DecryptWithAAD(key, ct, aad)
	if err != nil {
		t.Fatalf("DecryptWithAAD: %v", err)
	}
	if !bytes.Equal(pt, []byte("s3cret")) {
		t.Fatalf("plaintext = %q", pt)
	}
}

func TestDecryptWithAADRejectsADifferentAAD(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ct, err := EncryptWithAAD(key, []byte("s3cret"), []byte("path=a"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = DecryptWithAAD(key, ct, []byte("path=b"))
	var de *DecryptionError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v, want *DecryptionError", err)
	}
	// Dropping the AAD altogether must fail the same way.
	if _, err := Decrypt(key, ct); !errors.As(err, &de) {
		t.Fatalf("Decrypt without AAD: err = %v, want *DecryptionError", err)
	}
}

// TestEncryptIsEncryptWithNilAAD pins that the AAD-less helpers are the AAD
// helpers with no associated data, so ciphertext written by either reads with
// the other.
func TestEncryptIsEncryptWithNilAAD(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ct, err := Encrypt(key, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	pt, err := DecryptWithAAD(key, ct, nil)
	if err != nil || !bytes.Equal(pt, []byte("x")) {
		t.Fatalf("DecryptWithAAD(nil) = %q, %v", pt, err)
	}
}

func TestAADHelpersRejectABadKey(t *testing.T) {
	var kl *InvalidKeyLengthError
	if _, err := EncryptWithAAD(make([]byte, 16), []byte("x"), nil); !errors.As(err, &kl) {
		t.Fatalf("EncryptWithAAD: err = %v, want *InvalidKeyLengthError", err)
	}
	if _, err := DecryptWithAAD(make([]byte, 16), make([]byte, 40), nil); !errors.As(err, &kl) {
		t.Fatalf("DecryptWithAAD: err = %v, want *InvalidKeyLengthError", err)
	}
}
