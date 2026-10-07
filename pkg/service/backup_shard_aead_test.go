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

package service

import (
	"bytes"
	"testing"
)

// TestEncryptShard_FreshNonceEachSeal pins the property that makes the
// nil-nonce gcm.Seal in encryptShard safe: the AEAD owns nonce generation, so
// two Seals of the same plaintext under the same key and AAD differ, and both
// open to the original plaintext.
func TestEncryptShard_FreshNonceEachSeal(t *testing.T) {
	dek := bytes.Repeat([]byte{0x5a}, 32)
	gcm, err := newShardGCM(dek)
	if err != nil {
		t.Fatalf("newShardGCM: %v", err)
	}
	aad := buildShardAAD("backup-1", "dest", 7, 1)
	plaintext := []byte("shard plaintext")

	a, err := encryptShard(gcm, aad, plaintext)
	if err != nil {
		t.Fatalf("encryptShard (1): %v", err)
	}
	b, err := encryptShard(gcm, aad, plaintext)
	if err != nil {
		t.Fatalf("encryptShard (2): %v", err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("two Seals of the same plaintext produced identical ciphertext -- nonce reuse")
	}
	for i, sealed := range [][]byte{a, b} {
		got, err := decryptShard(gcm, aad, sealed)
		if err != nil {
			t.Fatalf("decryptShard (%d): %v", i+1, err)
		}
		if !bytes.Equal(got, plaintext) {
			t.Fatalf("decryptShard (%d) returned %q, want %q", i+1, got, plaintext)
		}
	}
}

// TestEncryptShard_WrongAADFailsOpen is the companion: the same sealed bytes
// must not open under a different shard's AAD.
func TestEncryptShard_WrongAADFailsOpen(t *testing.T) {
	gcm, err := newShardGCM(bytes.Repeat([]byte{0x5a}, 32))
	if err != nil {
		t.Fatalf("newShardGCM: %v", err)
	}
	sealed, err := encryptShard(gcm, buildShardAAD("backup-1", "dest", 7, 1), []byte("x"))
	if err != nil {
		t.Fatalf("encryptShard: %v", err)
	}
	if _, err := decryptShard(gcm, buildShardAAD("backup-1", "dest", 8, 1), sealed); err == nil {
		t.Fatal("decryptShard opened a shard sealed for a different shard ID")
	}
}
