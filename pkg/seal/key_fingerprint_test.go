// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package seal

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// keyFingerprint returns b's key fingerprint, failing the test on an error.
func keyFingerprint(t *testing.T, b *StorageBarrier) string {
	t.Helper()
	fp, err := b.KeyFingerprint(context.Background())
	if err != nil {
		t.Fatalf("KeyFingerprint: %v", err)
	}
	return fp
}

// One root key has one fingerprint, kept across a seal and unseal; another
// root key has another; and a fingerprint is 16 hex bytes.
func TestKeyFingerprint_NamesTheRootKey(t *testing.T) {
	ctx := context.Background()
	b := newUnsealedStorageBarrier(t)
	before := keyFingerprint(t, b)
	if raw, err := hex.DecodeString(before); err != nil || len(raw) != keyFingerprintLen {
		t.Fatalf("fingerprint %q is not %d hex bytes: %v", before, keyFingerprintLen, err)
	}
	if other := keyFingerprint(t, newUnsealedStorageBarrier(t)); other == before {
		t.Fatal("two root keys share a fingerprint")
	}
	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := b.Unseal(ctx, MustCredentials(ctx, []byte("pw"))); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	if after := keyFingerprint(t, b); after != before {
		t.Fatalf("an unseal changed the fingerprint: %q then %q", before, after)
	}
}

// A sealed barrier cannot be fingerprinted; a no-encryption barrier holds no
// lasting key and has an empty fingerprint.
func TestKeyFingerprint_SealedAndNone(t *testing.T) {
	sealed := newUnsealedStorageBarrier(t)
	if err := sealed.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	fp, err := sealed.KeyFingerprint(context.Background())
	var fpErr *KeyFingerprintError
	if !errors.As(err, &fpErr) || !errors.Is(err, ErrSealed) || fp != "" {
		t.Fatalf("sealed: KeyFingerprint = %q, %v; want *KeyFingerprintError wrapping ErrSealed", fp, err)
	}
	if !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("error %q names no operation", err)
	}

	none, _ := newNoneBarrier(t)
	if fp := keyFingerprint(t, none); fp != "" {
		t.Fatalf("none: KeyFingerprint = %q, want empty", fp)
	}
}
