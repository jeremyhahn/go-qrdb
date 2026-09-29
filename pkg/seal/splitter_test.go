// Copyright (c) 2026 Jeremy Hahn
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

package seal

import (
	"bytes"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir"
)

// ---------- ShareSplitter (shamirShareSplitter) tests ----------

func TestShareSplitter_Split_Success(t *testing.T) {
	splitter := NewShamirShareSplitter()
	secret := []byte("split-me")

	shares, err := splitter.SplitProtected(secret, 2, 3)
	if err != nil {
		t.Fatalf("SplitProtected: %v", err)
	}
	defer destroyShares(shares)

	if len(shares) != 3 {
		t.Fatalf("expected 3 shares, got %d", len(shares))
	}

	// Verify each share carries a non-empty protected value.
	for i, share := range shares {
		if err := share.Validate(); err != nil {
			t.Errorf("share %d is invalid: %v", i, err)
		}
	}
}

func TestShareSplitter_Split_InvalidThreshold(t *testing.T) {
	splitter := NewShamirShareSplitter()

	// threshold < 2 is invalid for Shamir.
	shares, err := splitter.SplitProtected([]byte("secret"), 1, 3)
	defer destroyShares(shares)
	if err == nil {
		t.Fatal("expected error for threshold < 2")
	}
}

func TestShareSplitter_Split_ThresholdExceedsTotal(t *testing.T) {
	splitter := NewShamirShareSplitter()

	shares, err := splitter.SplitProtected([]byte("secret"), 5, 3)
	defer destroyShares(shares)
	if err == nil {
		t.Fatal("expected error for threshold > total")
	}
}

func TestShareSplitter_Split_EmptySecret(t *testing.T) {
	splitter := NewShamirShareSplitter()

	shares, err := splitter.SplitProtected([]byte{}, 2, 3)
	defer destroyShares(shares)
	if err == nil {
		t.Fatal("expected error for empty secret")
	}
}

func TestShareSplitter_RoundTrip(t *testing.T) {
	// Split a secret, accumulate threshold shares, and reconstruct.
	secret := []byte("round-trip-test-secret")
	splitter := NewShamirShareSplitter()

	shares, err := splitter.SplitProtected(secret, 3, 5)
	if err != nil {
		t.Fatalf("SplitProtected: %v", err)
	}
	defer destroyShares(shares[3:])

	defer destroyShares(shares[:3])
	secretEnclave, err := shamir.CombineProtected(shares[:3])
	if err != nil {
		t.Fatalf("CombineProtected: %v", err)
	}
	defer secretEnclave.Destroy()

	assertEnclaveEquals(t, secretEnclave, secret)
}

func TestShareSplitter_RoundTrip_DifferentShareSubset(t *testing.T) {
	// Verify that any subset of threshold shares can reconstruct.
	secret := []byte("any-subset-works")
	splitter := NewShamirShareSplitter()

	shares, err := splitter.SplitProtected(secret, 2, 5)
	if err != nil {
		t.Fatalf("SplitProtected: %v", err)
	}
	// Shares 2 and 4 go to the accumulator; the rest must be destroyed here.
	defer destroyShares([]*shamir.ProtectedShare{shares[0], shares[1], shares[3]})

	defer destroyShares([]*shamir.ProtectedShare{shares[2], shares[4]})
	secretEnclave, err := shamir.CombineProtected([]*shamir.ProtectedShare{shares[2], shares[4]})
	if err != nil {
		t.Fatalf("CombineProtected: %v", err)
	}
	defer secretEnclave.Destroy()

	assertEnclaveEquals(t, secretEnclave, secret)
}

func TestShareSplitter_RoundTrip_BinarySecret(t *testing.T) {
	// Regression guard for the protected reconstruction path
	// (shamir.CombineProtected): a binary secret containing embedded NUL
	// bytes and every byte value must survive SplitProtected ->
	// AddProtectedShare -> ReconstructProtected unchanged. A string-based
	// path that truncates on NUL or mangles non-UTF-8 bytes would corrupt it.
	secret := make([]byte, 256)
	for i := range secret {
		secret[i] = byte(i)
	}

	splitter := NewShamirShareSplitter()
	shares, err := splitter.SplitProtected(secret, 3, 5)
	if err != nil {
		t.Fatalf("SplitProtected: %v", err)
	}
	defer destroyShares(shares[3:])

	defer destroyShares(shares[:3])
	secretEnclave, err := shamir.CombineProtected(shares[:3])
	if err != nil {
		t.Fatalf("CombineProtected: %v", err)
	}
	defer secretEnclave.Destroy()

	assertEnclaveEquals(t, secretEnclave, secret)
}

// ---------- Compile-time interface checks ----------

func TestShareSplitter_ImplementsInterface(t *testing.T) {
	var splitter interface{} = NewShamirShareSplitter()
	if _, ok := splitter.(ShareSplitter); !ok {
		t.Fatal("ShamirShareSplitter does not implement ShareSplitter")
	}
}

// destroyShares releases every protected share in the slice. Safe on nil
// entries and a nil slice.
func destroyShares(shares []*shamir.ProtectedShare) {
	for _, s := range shares {
		if s != nil {
			s.Destroy()
		}
	}
}

// assertEnclaveEquals asserts the reconstructed secret held in the enclave
// equals want, reading it only inside the protected borrow.
func assertEnclaveEquals(t *testing.T, enc interface {
	WithBytes(func([]byte) error) error
}, want []byte) {
	t.Helper()
	if err := enc.WithBytes(func(got []byte) error {
		if !bytes.Equal(got, want) {
			t.Errorf("reconstructed secret mismatch: got %d bytes, want %d bytes", len(got), len(want))
		}
		return nil
	}); err != nil {
		t.Fatalf("WithBytes: %v", err)
	}
}
