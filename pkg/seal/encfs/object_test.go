// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package encfs

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

const testObjName = "tenant/db1/000001.sst"

func objectPlaintext(t *testing.T, n int) []byte {
	t.Helper()
	p := make([]byte, n)
	if _, err := rand.Read(p); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return p
}

// objectHeaderSize returns the header size recorded in sealed.
func objectHeaderSize(sealed []byte) int {
	return int(uint16(sealed[offHeaderSize]) | uint16(sealed[offHeaderSize+1])<<8)
}

func TestObjectRoundTrip(t *testing.T) {
	b := newFakeBarrier(t)
	for _, n := range []int{0, 1, BlockSize - 1, BlockSize, BlockSize + 1, 3*BlockSize + 5} {
		plain := objectPlaintext(t, n)
		sealed, err := SealObject(b, testObjName, plain)
		if err != nil {
			t.Fatalf("size %d: SealObject: %v", n, err)
		}
		got, err := OpenObject(b, testObjName, sealed)
		if err != nil {
			t.Fatalf("size %d: OpenObject: %v", n, err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("size %d: round trip mismatch", n)
		}
		size, err := ObjectLogicalSize(sealed[:fixedHeaderSize], int64(len(sealed)))
		if err != nil {
			t.Fatalf("size %d: ObjectLogicalSize: %v", n, err)
		}
		if size != int64(n) {
			t.Fatalf("size %d: ObjectLogicalSize = %d", n, size)
		}
	}
}

func TestObjectEmptyIsOneFinalBlock(t *testing.T) {
	b := newFakeBarrier(t)
	sealed, err := SealObject(b, testObjName, nil)
	if err != nil {
		t.Fatalf("SealObject: %v", err)
	}
	if payload := len(sealed) - objectHeaderSize(sealed); payload != BlockOverhead {
		t.Fatalf("empty object payload = %d, want %d", payload, BlockOverhead)
	}
	if _, err := OpenObject(b, testObjName, sealed[:objectHeaderSize(sealed)]); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("zero blocks: err = %v, want ErrAuthFailed", err)
	}
	if _, err := ObjectLogicalSize(sealed[:fixedHeaderSize], int64(objectHeaderSize(sealed))); err == nil {
		t.Fatal("ObjectLogicalSize accepted zero blocks")
	}
}

func TestObjectBlockTamperFails(t *testing.T) {
	b := newFakeBarrier(t)
	sealed, err := SealObject(b, testObjName, objectPlaintext(t, 3*BlockSize+5))
	if err != nil {
		t.Fatalf("SealObject: %v", err)
	}
	hs := objectHeaderSize(sealed)
	for i := 0; i < 4; i++ {
		bad := bytes.Clone(sealed)
		bad[hs+i*CiphertextBlockSize+inlineNonceSize+3] ^= 0x01
		if _, err := OpenObject(b, testObjName, bad); !errors.Is(err, ErrAuthFailed) {
			t.Fatalf("block %d flipped: err = %v, want ErrAuthFailed", i, err)
		}
	}
}

func TestObjectTruncateAtBlockBoundaryFails(t *testing.T) {
	b := newFakeBarrier(t)
	sealed, err := SealObject(b, testObjName, objectPlaintext(t, BlockSize+1))
	if err != nil {
		t.Fatalf("SealObject: %v", err)
	}
	cut := sealed[:objectHeaderSize(sealed)+CiphertextBlockSize]
	if _, err := OpenObject(b, testObjName, cut); err == nil {
		t.Fatal("OpenObject accepted an object without its final block")
	}
}

func TestObjectAppendedBlockFails(t *testing.T) {
	b := newFakeBarrier(t)
	sealed, err := SealObject(b, testObjName, objectPlaintext(t, BlockSize))
	if err != nil {
		t.Fatalf("SealObject: %v", err)
	}
	hs := objectHeaderSize(sealed)
	extra := append(bytes.Clone(sealed), sealed[hs:]...)
	if _, err := OpenObject(b, testObjName, extra); err == nil {
		t.Fatal("OpenObject accepted data after the final block")
	}
}

func TestObjectBlockSwapFails(t *testing.T) {
	b := newFakeBarrier(t)
	sealed, err := SealObject(b, testObjName, objectPlaintext(t, 3*BlockSize+5))
	if err != nil {
		t.Fatalf("SealObject: %v", err)
	}
	hs := objectHeaderSize(sealed)
	bad := bytes.Clone(sealed)
	first := bad[hs : hs+CiphertextBlockSize]
	second := bad[hs+CiphertextBlockSize : hs+2*CiphertextBlockSize]
	tmp := bytes.Clone(first)
	copy(first, second)
	copy(second, tmp)
	if _, err := OpenObject(b, testObjName, bad); err == nil {
		t.Fatal("OpenObject accepted swapped blocks")
	}
}

func TestObjectOtherNameFails(t *testing.T) {
	b := newFakeBarrier(t)
	sealed, err := SealObject(b, testObjName, objectPlaintext(t, 10))
	if err != nil {
		t.Fatalf("SealObject: %v", err)
	}
	if _, err := OpenObject(b, "tenant/db1/000002.sst", sealed); err == nil {
		t.Fatal("OpenObject accepted the object under another name")
	}
}

func TestObjectOtherBarrierFails(t *testing.T) {
	sealed, err := SealObject(newFakeBarrier(t), testObjName, objectPlaintext(t, 10))
	if err != nil {
		t.Fatalf("SealObject: %v", err)
	}
	if _, err := OpenObject(newFakeBarrier(t), testObjName, sealed); err == nil {
		t.Fatal("OpenObject accepted the object under another KEK")
	}
}

func TestObjectNotQENCRefused(t *testing.T) {
	b := newFakeBarrier(t)
	raw := bytes.Repeat([]byte("plain sst bytes "), 8)
	if _, err := OpenObject(b, testObjName, raw); !errors.Is(err, ErrInvalidHeader) {
		t.Fatalf("OpenObject: err = %v, want ErrInvalidHeader", err)
	}
	if _, err := ObjectLogicalSize(raw[:fixedHeaderSize], int64(len(raw))); !errors.Is(err, ErrInvalidHeader) {
		t.Fatalf("ObjectLogicalSize: err = %v, want ErrInvalidHeader", err)
	}
	if _, err := OpenObject(b, testObjName, []byte("QE")); !errors.Is(err, ErrTruncatedHeader) {
		t.Fatalf("short: err = %v, want ErrTruncatedHeader", err)
	}
	if _, err := ObjectLogicalSize([]byte("QENC"), 4); !errors.Is(err, ErrTruncatedHeader) {
		t.Fatalf("short ObjectLogicalSize: err = %v, want ErrTruncatedHeader", err)
	}
}

func TestObjectNilBarrier(t *testing.T) {
	if _, err := SealObject(nil, testObjName, []byte("x")); !errors.Is(err, ErrNilBarrier) {
		t.Fatalf("SealObject: err = %v, want ErrNilBarrier", err)
	}
	if _, err := OpenObject(nil, testObjName, []byte("x")); !errors.Is(err, ErrNilBarrier) {
		t.Fatalf("OpenObject: err = %v, want ErrNilBarrier", err)
	}
}

func TestObjectSealedBarrierErrorsUnchanged(t *testing.T) {
	b := newFakeBarrier(t)
	sealed, err := SealObject(b, testObjName, []byte("x"))
	if err != nil {
		t.Fatalf("SealObject: %v", err)
	}
	b.seal()
	if _, err := SealObject(b, testObjName, []byte("x")); !errors.Is(err, seal.ErrSealed) {
		t.Fatalf("SealObject: err = %v, want seal.ErrSealed", err)
	}
	if _, err := OpenObject(b, testObjName, sealed); !errors.Is(err, seal.ErrSealed) {
		t.Fatalf("OpenObject: err = %v, want seal.ErrSealed", err)
	}
}

func TestObjectCiphertextHidesPlaintext(t *testing.T) {
	b := newFakeBarrier(t)
	plain := bytes.Repeat([]byte("secret-value-"), 700)
	sealed, err := SealObject(b, testObjName, plain)
	if err != nil {
		t.Fatalf("SealObject: %v", err)
	}
	if bytes.Contains(sealed, []byte("secret-value-")) {
		t.Fatal("sealed object contains the plaintext")
	}
}

func TestObjectSealsFreshNonces(t *testing.T) {
	b := newFakeBarrier(t)
	plain := objectPlaintext(t, 3*BlockSize)
	seen := make(map[string]bool)
	for k := range 2 {
		sealed, err := SealObject(b, testObjName, plain)
		if err != nil {
			t.Fatalf("seal %d: SealObject: %v", k, err)
		}
		payload := sealed[objectHeaderSize(sealed):]
		if len(payload) != 3*CiphertextBlockSize {
			t.Fatalf("seal %d: payload is %d bytes, want %d", k, len(payload), 3*CiphertextBlockSize)
		}
		for i := range 3 {
			nonce := string(payload[i*CiphertextBlockSize : i*CiphertextBlockSize+inlineNonceSize])
			if seen[nonce] {
				t.Fatalf("seal %d block %d: nonce repeated", k, i)
			}
			seen[nonce] = true
		}
		got, err := OpenObject(b, testObjName, sealed)
		if err != nil {
			t.Fatalf("seal %d: OpenObject: %v", k, err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("seal %d: round trip mismatch", k)
		}
	}
}

func TestObjectLayoutUnchanged(t *testing.T) {
	b := newFakeBarrier(t)
	plain := objectPlaintext(t, BlockSize+7)
	sealed, err := SealObject(b, testObjName, plain)
	if err != nil {
		t.Fatalf("SealObject: %v", err)
	}
	payload := sealed[objectHeaderSize(sealed):]
	if want := 2*BlockOverhead + len(plain); len(payload) != want {
		t.Fatalf("payload is %d bytes, want %d", len(payload), want)
	}
}
