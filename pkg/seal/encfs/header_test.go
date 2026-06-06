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
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

func TestHeader_RoundTrip(t *testing.T) {
	b := newFakeBarrier(t)
	h, err := newHeader(b)
	if err != nil {
		t.Fatalf("newHeader: %v", err)
	}
	if h.Version != headerVersion {
		t.Errorf("version = %d, want %d", h.Version, headerVersion)
	}
	if h.BlockSize != BlockSize {
		t.Errorf("block size = %d, want %d", h.BlockSize, BlockSize)
	}
	if len(h.WrappedDEK) == 0 {
		t.Error("wrapped DEK is empty")
	}
	raw := h.marshal()
	if bytes.Contains(raw, h.DEK[:]) {
		t.Error("plaintext DEK leaked into marshaled header")
	}
	parsed, err := readHeader(bytes.NewReader(raw), b)
	if err != nil {
		t.Fatalf("readHeader: %v", err)
	}
	if parsed.DEK != h.DEK {
		t.Error("DEK mismatch after unwrap")
	}
}

func TestHeader_NewHeader_NilBarrier(t *testing.T) {
	if _, err := newHeader(nil); !errors.Is(err, ErrNilBarrier) {
		t.Errorf("newHeader(nil) error = %v, want ErrNilBarrier", err)
	}
}

func TestHeader_ReadHeader_NilBarrier(t *testing.T) {
	if _, err := readHeader(bytes.NewReader(nil), nil); !errors.Is(err, ErrNilBarrier) {
		t.Errorf("readHeader nil = %v, want ErrNilBarrier", err)
	}
}

func TestHeader_RejectsWrongMagic(t *testing.T) {
	b := newFakeBarrier(t)
	h, _ := newHeader(b)
	raw := h.marshal()
	raw[0] = 'X'
	if _, err := readHeader(bytes.NewReader(raw), b); !errors.Is(err, ErrInvalidHeader) {
		t.Errorf("error = %v, want ErrInvalidHeader", err)
	}
}

func TestHeader_RejectsUnsupportedVersion(t *testing.T) {
	b := newFakeBarrier(t)
	h, _ := newHeader(b)
	raw := h.marshal()
	raw[offVersion] = 0xFF
	if _, err := readHeader(bytes.NewReader(raw), b); !errors.Is(err, ErrUnsupportedVersion) {
		t.Errorf("error = %v, want ErrUnsupportedVersion", err)
	}
}

func TestHeader_RejectsTruncatedFixed(t *testing.T) {
	b := newFakeBarrier(t)
	h, _ := newHeader(b)
	raw := h.marshal()
	if _, err := readHeader(bytes.NewReader(raw[:fixedHeaderSize-5]), b); !errors.Is(err, ErrTruncatedHeader) {
		t.Errorf("error = %v, want ErrTruncatedHeader", err)
	}
}

func TestHeader_RejectsTruncatedWrappedDEK(t *testing.T) {
	b := newFakeBarrier(t)
	h, _ := newHeader(b)
	raw := h.marshal()
	if _, err := readHeader(bytes.NewReader(raw[:fixedHeaderSize+1]), b); !errors.Is(err, ErrTruncatedHeader) {
		t.Errorf("error = %v, want ErrTruncatedHeader", err)
	}
}

func TestHeader_RejectsHeaderSizeTooSmall(t *testing.T) {
	b := newFakeBarrier(t)
	h, _ := newHeader(b)
	raw := h.marshal()
	binary.LittleEndian.PutUint16(raw[offHeaderSize:], 1)
	if _, err := readHeader(bytes.NewReader(raw), b); !errors.Is(err, ErrInvalidHeader) {
		t.Errorf("error = %v, want ErrInvalidHeader", err)
	}
}

func TestHeader_RejectsHeaderSizeTooLarge(t *testing.T) {
	b := newFakeBarrier(t)
	h, _ := newHeader(b)
	raw := h.marshal()
	binary.LittleEndian.PutUint16(raw[offHeaderSize:], 0xFFFF)
	if _, err := readHeader(bytes.NewReader(raw), b); !errors.Is(err, ErrInvalidHeader) {
		t.Errorf("error = %v, want ErrInvalidHeader", err)
	}
}

func TestHeader_RejectsZeroWrappedDEK(t *testing.T) {
	b := newFakeBarrier(t)
	h, _ := newHeader(b)
	raw := h.marshal()
	binary.LittleEndian.PutUint32(raw[offWrappedDEKLen:], 0)
	binary.LittleEndian.PutUint16(raw[offHeaderSize:], uint16(fixedHeaderSize))
	if _, err := readHeader(bytes.NewReader(raw[:fixedHeaderSize]), b); !errors.Is(err, ErrInvalidHeader) {
		t.Errorf("error = %v, want ErrInvalidHeader", err)
	}
}

func TestHeader_RejectsInconsistentSizes(t *testing.T) {
	b := newFakeBarrier(t)
	h, _ := newHeader(b)
	raw := h.marshal()
	binary.LittleEndian.PutUint16(raw[offHeaderSize:], uint16(fixedHeaderSize+9999))
	if _, err := readHeader(bytes.NewReader(raw), b); !errors.Is(err, ErrInvalidHeader) {
		t.Errorf("error = %v, want ErrInvalidHeader", err)
	}
}

func TestHeader_RejectsForeignBlockSize(t *testing.T) {
	b := newFakeBarrier(t)
	h, _ := newHeader(b)
	raw := h.marshal()
	binary.LittleEndian.PutUint32(raw[offBlockSize:], 8192)
	if _, err := readHeader(bytes.NewReader(raw), b); !errors.Is(err, ErrInvalidHeader) {
		t.Errorf("error = %v, want ErrInvalidHeader", err)
	}
}

func TestHeader_BarrierSealedReturnsErrSealed(t *testing.T) {
	b := newFakeBarrier(t)
	h, _ := newHeader(b)
	raw := h.marshal()
	b.seal()
	if _, err := readHeader(bytes.NewReader(raw), b); !errors.Is(err, seal.ErrSealed) {
		t.Errorf("error = %v, want seal.ErrSealed", err)
	}
}

func TestHeader_NewHeaderBarrierSealedReturnsErrSealed(t *testing.T) {
	b := newFakeBarrier(t)
	b.seal()
	if _, err := newHeader(b); !errors.Is(err, seal.ErrSealed) {
		t.Errorf("error = %v, want seal.ErrSealed", err)
	}
}

func TestHeader_WrongBarrierFailsToUnwrap(t *testing.T) {
	a := newFakeBarrier(t)
	c := newFakeBarrier(t)
	h, _ := newHeader(a)
	if _, err := readHeader(bytes.NewReader(h.marshal()), c); err == nil {
		t.Error("expected unwrap failure when reading with different barrier")
	}
}

func TestHeader_ReadHeader_PartialRead(t *testing.T) {
	b := newFakeBarrier(t)
	h, _ := newHeader(b)
	raw := h.marshal()
	r := io.MultiReader(bytes.NewReader(raw[:fixedHeaderSize]), bytes.NewReader(raw[fixedHeaderSize:]))
	parsed, err := readHeader(r, b)
	if err != nil {
		t.Fatalf("readHeader split: %v", err)
	}
	if parsed.DEK != h.DEK {
		t.Error("DEK mismatch after split read")
	}
}

func TestHeader_TotalSize_MatchesMarshalLen(t *testing.T) {
	b := newFakeBarrier(t)
	h, _ := newHeader(b)
	if got := len(h.marshal()); got != h.totalSize() {
		t.Errorf("marshal length = %d, totalSize = %d", got, h.totalSize())
	}
}

func TestHeader_ReservedZero(t *testing.T) {
	b := newFakeBarrier(t)
	h, _ := newHeader(b)
	raw := h.marshal()
	for i := offReserved; i < offReserved+lenReserved; i++ {
		if raw[i] != 0 {
			t.Errorf("reserved byte %d = 0x%x, want 0", i, raw[i])
		}
	}
}

type oversizedBarrier struct{}

func (oversizedBarrier) Encrypt(plaintext []byte) ([]byte, error) {
	return make([]byte, maxWrappedDEKSize+1), nil
}

func (oversizedBarrier) Decrypt(ciphertext []byte) ([]byte, error) {
	return nil, errors.New("oversizedBarrier: not used")
}

func TestHeader_RejectsOversizedWrappedDEK(t *testing.T) {
	if _, err := newHeader(oversizedBarrier{}); !errors.Is(err, ErrInvalidHeader) {
		t.Errorf("error = %v, want ErrInvalidHeader", err)
	}
}

type emptyWrapBarrier struct{}

func (emptyWrapBarrier) Encrypt(plaintext []byte) ([]byte, error) { return []byte{}, nil }
func (emptyWrapBarrier) Decrypt(ciphertext []byte) ([]byte, error) {
	return nil, errors.New("emptyWrapBarrier: not used")
}

func TestHeader_RejectsEmptyWrappedDEK(t *testing.T) {
	if _, err := newHeader(emptyWrapBarrier{}); !errors.Is(err, ErrInvalidHeader) {
		t.Errorf("error = %v, want ErrInvalidHeader", err)
	}
}

type shortDecryptBarrier struct{ inner *fakeBarrier }

func (b shortDecryptBarrier) Encrypt(plaintext []byte) ([]byte, error) {
	return b.inner.Encrypt(plaintext)
}

func (b shortDecryptBarrier) Decrypt(ciphertext []byte) ([]byte, error) {
	pt, err := b.inner.Decrypt(ciphertext)
	if err != nil {
		return nil, err
	}
	return pt[:len(pt)-1], nil
}

func TestHeader_RejectsCorruptDEKLength(t *testing.T) {
	inner := newFakeBarrier(t)
	wrapped := shortDecryptBarrier{inner: inner}
	h, err := newHeader(wrapped)
	if err != nil {
		t.Fatalf("newHeader: %v", err)
	}
	raw := h.marshal()
	if _, err := readHeader(bytes.NewReader(raw), wrapped); !errors.Is(err, ErrCorruptDEK) {
		t.Errorf("error = %v, want ErrCorruptDEK", err)
	}
}
