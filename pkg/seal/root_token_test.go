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
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
)

func TestRootTokenGenerateDirectMode(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		b, _, dist := newRecoveryBarrierWithShamir(t)
		ctx := context.Background()

		if _, err := b.InitializeShamir(ctx, Credentials{}); err != nil {
			t.Fatal(err)
		}
		shares := dist.Shares()

		token, err := b.GenerateRootToken(ctx, shares[:3])
		if err != nil {
			t.Fatalf("GenerateRootToken: unexpected error: %v", err)
		}
		if token == nil {
			t.Fatal("GenerateRootToken: token is nil")
		}
		if token.Token == "" {
			t.Error("token string is empty")
		}
		if token.CreatedAt.IsZero() {
			t.Error("CreatedAt is zero")
		}

		// Verify token is valid hex encoding of 32 bytes (SHA-256 output).
		decoded, err := hex.DecodeString(token.Token)
		if err != nil {
			t.Fatalf("token is not valid hex: %v", err)
		}
		if len(decoded) != 32 {
			t.Errorf("decoded token length: got %d, want 32", len(decoded))
		}
	})

	t.Run("Success_AllShares", func(t *testing.T) {
		b, _, dist := newRecoveryBarrierWithShamir(t)
		ctx := context.Background()

		if _, err := b.InitializeShamir(ctx, Credentials{}); err != nil {
			t.Fatal(err)
		}
		shares := dist.Shares()

		// Providing all 5 shares should also work.
		token, err := b.GenerateRootToken(ctx, shares)
		if err != nil {
			t.Fatalf("GenerateRootToken with all shares: %v", err)
		}
		if token.Token == "" {
			t.Error("token string is empty")
		}
	})

	t.Run("DifferentTokensEachCall", func(t *testing.T) {
		b, _, dist := newRecoveryBarrierWithShamir(t)
		ctx := context.Background()

		if _, err := b.InitializeShamir(ctx, Credentials{}); err != nil {
			t.Fatal(err)
		}
		shares := dist.Shares()

		token1, err := b.GenerateRootToken(ctx, shares[:3])
		if err != nil {
			t.Fatal(err)
		}

		token2, err := b.GenerateRootToken(ctx, shares[:3])
		if err != nil {
			t.Fatal(err)
		}

		// Different calls generate different nonces, so tokens should differ.
		if token1.Token == token2.Token {
			t.Error("two calls produced identical tokens; expected different nonces")
		}
	})
}

func TestRootTokenGenerateCredentialMode(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		// Create a barrier with ShamirConfig but without a ShamirStrategy registered.
		// This exercises the credential mode path.
		backend := NewMemoryBackend()
		b, err := NewBarrier(slog.Default(), backend, BarrierConfig{
			RootKeyPath: "test/root-key",
			Shamir:      &ShamirConfig{Threshold: 2, TotalShares: 3},
		}, testSoftwareStrategy(t))
		if err != nil {
			t.Fatal(err)
		}
		dist := newCaptureDistributor()
		b.SetCeremonyDistributor(dist)

		ctx := context.Background()
		password := "credential-mode-password"

		// Initialize using Shamir credential mode.
		result, err := b.InitializeShamir(ctx, Credentials{Secret: []byte(password)})
		if err != nil {
			t.Fatal(err)
		}
		if result.ShareCount != 3 {
			t.Fatalf("ShareCount: got %d, want 3", result.ShareCount)
		}
		shares := dist.Shares()
		if len(shares) != 3 {
			t.Fatalf("captured shares: got %d, want 3", len(shares))
		}

		token, err := b.GenerateRootToken(ctx, shares[:2])
		if err != nil {
			t.Fatalf("GenerateRootToken (credential mode): %v", err)
		}
		if token == nil {
			t.Fatal("token is nil")
		}
		if token.Token == "" {
			t.Error("token string is empty")
		}
	})
}

func TestRootTokenErrors(t *testing.T) {
	t.Run("ErrShamirNotConfigured", func(t *testing.T) {
		// Barrier with no Shamir config.
		b, _, _ := newRecoveryBarrier(t)
		initializeBarrier(t, b)
		ctx := context.Background()

		_, err := b.GenerateRootToken(ctx, []string{"share1", "share2"})
		if !errors.Is(err, ErrShamirNotConfigured) {
			t.Errorf("got %v, want ErrShamirNotConfigured", err)
		}
	})

	t.Run("ErrShamirQuorumIncomplete", func(t *testing.T) {
		b, _, dist := newRecoveryBarrierWithShamir(t)
		ctx := context.Background()

		if _, err := b.InitializeShamir(ctx, Credentials{}); err != nil {
			t.Fatal(err)
		}
		shares := dist.Shares()

		// Provide only 2 shares when 3 are needed.
		_, err := b.GenerateRootToken(ctx, shares[:2])
		if !errors.Is(err, ErrShamirQuorumIncomplete) {
			t.Errorf("got %v, want ErrShamirQuorumIncomplete", err)
		}
	})

	t.Run("ErrShamirQuorumIncomplete_EmptyShares", func(t *testing.T) {
		b, _, _ := newRecoveryBarrierWithShamir(t)
		ctx := context.Background()

		if _, err := b.InitializeShamir(ctx, Credentials{}); err != nil {
			t.Fatal(err)
		}

		_, err := b.GenerateRootToken(ctx, []string{})
		if !errors.Is(err, ErrShamirQuorumIncomplete) {
			t.Errorf("got %v, want ErrShamirQuorumIncomplete", err)
		}
	})

	t.Run("ErrShamirCombineFailed_InvalidShares", func(t *testing.T) {
		b, _, _ := newRecoveryBarrierWithShamir(t)
		ctx := context.Background()

		if _, err := b.InitializeShamir(ctx, Credentials{}); err != nil {
			t.Fatal(err)
		}

		// Provide garbage share values.
		_, err := b.GenerateRootToken(ctx, []string{"bad1", "bad2", "bad3"})
		if !errors.Is(err, ErrShamirCombineFailed) {
			t.Errorf("got %v, want ErrShamirCombineFailed", err)
		}
	})

	t.Run("ErrRootTokenVerificationFailed_CorruptBlob", func(t *testing.T) {
		// Initialize a Shamir barrier, then corrupt the root key blob.
		// GenerateRootToken will reconstruct a valid secret from shares,
		// but verifyDirectMode will fail when unmarshaling the blob.
		b, backend, dist := newRecoveryBarrierWithShamir(t)
		ctx := context.Background()

		if _, err := b.InitializeShamir(ctx, Credentials{}); err != nil {
			t.Fatal(err)
		}
		shares := dist.Shares()

		// Corrupt the root key blob.
		if err := backend.Put(ctx, "test/root-key", []byte("corrupt-json")); err != nil {
			t.Fatal(err)
		}

		_, err := b.GenerateRootToken(ctx, shares[:3])
		if !errors.Is(err, ErrRootTokenVerificationFailed) {
			t.Errorf("got %v, want ErrRootTokenVerificationFailed", err)
		}
	})
}

func TestRootTokenVerifyDirectMode(t *testing.T) {
	t.Run("ErrRootTokenVerificationFailed_CorruptBlob", func(t *testing.T) {
		// Initialize a Shamir barrier, then corrupt the root key blob
		// so that JSON unmarshal fails inside verifyDirectMode.
		b, backend, _ := newRecoveryBarrierWithShamir(t)
		ctx := context.Background()

		if _, err := b.InitializeShamir(ctx, Credentials{}); err != nil {
			t.Fatal(err)
		}

		// Corrupt the root key blob with invalid JSON.
		if err := backend.Put(ctx, "test/root-key", []byte("not-valid-json")); err != nil {
			t.Fatal(err)
		}

		// Any key bytes will create a valid encryptor, but blob unmarshal fails.
		rootKey := make([]byte, 32)
		_, err := b.verifyDirectMode(ctx, rootKey)
		if !errors.Is(err, ErrRootTokenVerificationFailed) {
			t.Errorf("got %v, want ErrRootTokenVerificationFailed", err)
		}
	})

	t.Run("Success_ValidUnsealKey", func(t *testing.T) {
		b, _, dist := newRecoveryBarrierWithShamir(t)
		ctx := context.Background()

		if _, err := b.InitializeShamir(ctx, Credentials{}); err != nil {
			t.Fatal(err)
		}

		// Reconstruct the unseal key from the distributed shares; that is
		// the secret verifyDirectMode accepts in unseal-key mode.
		unsealKey := combineShareValues(t, dist.Shares()[:3], 3, 5)

		rootKey, err := b.verifyDirectMode(ctx, unsealKey)
		if err != nil {
			t.Fatalf("verifyDirectMode with correct unseal key: %v", err)
		}
		if len(rootKey) != 32 {
			t.Errorf("root key length: got %d, want 32", len(rootKey))
		}

		// The unwrap must yield the REAL root key.
		masterKey, err := b.GetMasterKey()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(rootKey, masterKey) {
			t.Error("verifyDirectMode did not return the barrier root key")
		}
	})

	t.Run("ErrRootTokenVerificationFailed_RootKeyIsNotUnsealKey", func(t *testing.T) {
		// With unseal-key indirection, the root key itself must NOT verify:
		// only the unseal key decrypts the wrapped blob.
		b, _, _ := newRecoveryBarrierWithShamir(t)
		ctx := context.Background()

		if _, err := b.InitializeShamir(ctx, Credentials{}); err != nil {
			t.Fatal(err)
		}

		masterKey, err := b.GetMasterKey()
		if err != nil {
			t.Fatal(err)
		}

		if _, err := b.verifyDirectMode(ctx, masterKey); !errors.Is(err, ErrRootTokenVerificationFailed) {
			t.Errorf("got %v, want ErrRootTokenVerificationFailed", err)
		}
	})
}

func TestRootTokenVerifyCredentialMode(t *testing.T) {
	t.Run("ErrRootTokenVerificationFailed_NoRootKeyBlob", func(t *testing.T) {
		// Create a barrier without ShamirStrategy to exercise credential mode.
		backend := NewMemoryBackend()
		b, err := NewBarrier(slog.Default(), backend, BarrierConfig{
			RootKeyPath: "test/root-key",
		}, testSoftwareStrategy(t))
		if err != nil {
			t.Fatal(err)
		}

		ctx := context.Background()
		// No root key blob exists yet.
		_, err = b.verifyCredentialMode(ctx, []byte("wrong"))
		if !errors.Is(err, ErrRootTokenVerificationFailed) {
			t.Errorf("got %v, want ErrRootTokenVerificationFailed", err)
		}
	})

	t.Run("ErrRootTokenVerificationFailed_CorruptBlob", func(t *testing.T) {
		backend := NewMemoryBackend()
		b, err := NewBarrier(slog.Default(), backend, BarrierConfig{
			RootKeyPath: "test/root-key",
		}, testSoftwareStrategy(t))
		if err != nil {
			t.Fatal(err)
		}

		ctx := context.Background()
		// Store corrupt blob.
		if err := backend.Put(ctx, "test/root-key", []byte("not-json")); err != nil {
			t.Fatal(err)
		}

		_, err = b.verifyCredentialMode(ctx, []byte("anything"))
		if !errors.Is(err, ErrRootTokenVerificationFailed) {
			t.Errorf("got %v, want ErrRootTokenVerificationFailed", err)
		}
	})

	t.Run("ErrRootTokenVerificationFailed_StrategyNotRegistered", func(t *testing.T) {
		backend := NewMemoryBackend()
		b, err := NewBarrier(slog.Default(), backend, BarrierConfig{
			RootKeyPath: "test/root-key",
		}, testSoftwareStrategy(t))
		if err != nil {
			t.Fatal(err)
		}

		ctx := context.Background()
		// Store a blob claiming to be sealed by a non-existent strategy.
		sealed := SealedRootKey{
			Version:  1,
			Strategy: StrategyID("nonexistent"),
		}
		blob, _ := json.Marshal(&sealed)
		if err := backend.Put(ctx, "test/root-key", blob); err != nil {
			t.Fatal(err)
		}

		_, err = b.verifyCredentialMode(ctx, []byte("anything"))
		if !errors.Is(err, ErrRootTokenVerificationFailed) {
			t.Errorf("got %v, want ErrRootTokenVerificationFailed", err)
		}
	})

	t.Run("ErrRootTokenVerificationFailed_WrongPassword", func(t *testing.T) {
		// The SoftwareStrategy uses its construction-time passphrase, not the
		// Credentials argument. To test credential mismatch in verifyCredentialMode,
		// we initialize with one passphrase, then swap the registered strategy to
		// one with a different passphrase so UnsealRootKey fails.
		backend := NewMemoryBackend()
		correctStrat, err := NewSoftwareStrategy([]byte("correct-password"))
		if err != nil {
			t.Fatal(err)
		}
		b, err := NewBarrier(slog.Default(), backend, BarrierConfig{
			RootKeyPath: "test/root-key",
		}, correctStrat)
		if err != nil {
			t.Fatal(err)
		}

		ctx := context.Background()
		if err := b.Initialize(ctx, Credentials{}); err != nil {
			t.Fatal(err)
		}

		// Replace the registered strategy with one that has a wrong passphrase.
		wrongStrat, err := NewSoftwareStrategy([]byte("wrong-password"))
		if err != nil {
			t.Fatal(err)
		}
		b.strategies[StrategyPassphrase] = wrongStrat

		// verifyCredentialMode loads the sealed blob, finds the software strategy,
		// and calls UnsealRootKey which will fail with the wrong passphrase.
		_, err = b.verifyCredentialMode(ctx, []byte("anything"))
		if !errors.Is(err, ErrRootTokenVerificationFailed) {
			t.Errorf("got %v, want ErrRootTokenVerificationFailed", err)
		}
	})

	t.Run("Success_CorrectPassword", func(t *testing.T) {
		backend := NewMemoryBackend()
		b, err := NewBarrier(slog.Default(), backend, BarrierConfig{
			RootKeyPath: "test/root-key",
		}, testSoftwareStrategy(t))
		if err != nil {
			t.Fatal(err)
		}

		ctx := context.Background()
		password := "my-secret-pass"
		if err := b.Initialize(ctx, Credentials{Secret: []byte(password)}); err != nil {
			t.Fatal(err)
		}

		dek, err := b.verifyCredentialMode(ctx, []byte(password))
		if err != nil {
			t.Fatalf("verifyCredentialMode with correct password: %v", err)
		}
		if len(dek) != 32 {
			t.Errorf("DEK length: got %d, want 32", len(dek))
		}
	})
}

func TestRootTokenVerifyDirectModeStrategyCheck(t *testing.T) {
	t.Run("ErrRootTokenVerificationFailed_NonShamirStrategy", func(t *testing.T) {
		// Create a barrier with Shamir strategy, but store a root key blob
		// that claims to use the software strategy instead.
		backend := NewMemoryBackend()
		shamirStrat, err := NewShamirStrategy(2, 3)
		if err != nil {
			t.Fatal(err)
		}
		b, err := NewBarrier(slog.Default(), backend, BarrierConfig{
			RootKeyPath:     "test/root-key",
			Shamir:          &ShamirConfig{Threshold: 2, TotalShares: 3},
			PreferenceOrder: []StrategyID{StrategyShamir},
		}, testSoftwareStrategy(t), shamirStrat)
		if err != nil {
			t.Fatal(err)
		}
		dist := newCaptureDistributor()
		b.SetCeremonyDistributor(dist)

		ctx := context.Background()
		if _, err := b.InitializeShamir(ctx, Credentials{}); err != nil {
			t.Fatal(err)
		}

		// Overwrite the root key blob to claim it was sealed with software strategy.
		blob, err := backend.Get(ctx, "test/root-key")
		if err != nil {
			t.Fatal(err)
		}
		var sealed SealedRootKey
		if err := json.Unmarshal(blob, &sealed); err != nil {
			t.Fatal(err)
		}
		sealed.Strategy = StrategyPassphrase
		modified, _ := json.Marshal(&sealed)
		if err := backend.Put(ctx, "test/root-key", modified); err != nil {
			t.Fatal(err)
		}

		// verifyDirectMode should reject because sealed.Strategy != StrategyShamir.
		rootKey := make([]byte, 32) // any 32 bytes, will pass encryptor creation
		_, err = b.verifyDirectMode(ctx, rootKey)
		if !errors.Is(err, ErrRootTokenVerificationFailed) {
			t.Errorf("got %v, want ErrRootTokenVerificationFailed", err)
		}
	})
}

// --------------------------------------------------------------------------
// root_token.go: verifySecretAndGetRootKey — 75%
// (verifyCredentialMode path: no ShamirStrategy registered)
// --------------------------------------------------------------------------

func TestVerifySecretAndGetRootKey_CredentialMode(t *testing.T) {
	// Build a software-strategy barrier (NO ShamirStrategy registered) with a
	// Shamir config so GenerateRootToken is reachable. In this mode,
	// verifySecretAndGetRootKey uses verifyCredentialMode (ShamirStrategy() == nil).
	//
	// We use InitializeShamir in credential mode (no ShamirStrategy) which
	// first calls Initialize internally, then splits the credential secret.
	backend := NewMemoryBackend()
	sw := testSoftwareStrategy(t)
	b, err := NewBarrier(testLogger(), backend, BarrierConfig{
		RootKeyPath:     "test/root-key",
		Shamir:          &ShamirConfig{Threshold: 2, TotalShares: 3},
		PreferenceOrder: []StrategyID{StrategyPassphrase},
	}, sw)
	if err != nil {
		t.Fatal(err)
	}
	dist := newCaptureDistributor()
	b.SetCeremonyDistributor(dist)
	ctx := context.Background()

	// InitializeShamir in credential mode: no ShamirStrategy is registered,
	// so the credential secret ("the-password") is split. The barrier is
	// also initialized inside InitializeShamir.
	if _, err := b.InitializeShamir(ctx, Credentials{Secret: []byte("the-password")}); err != nil {
		t.Fatalf("InitializeShamir (credential mode): %v", err)
	}
	shares := dist.Shares()

	// GenerateRootToken reconstructs the secret from shares and calls
	// verifySecretAndGetRootKey → verifyCredentialMode (no ShamirStrategy).
	token, err := b.GenerateRootToken(ctx, shares[:2])
	if err != nil {
		t.Fatalf("GenerateRootToken (credential mode): %v", err)
	}
	if token == nil || token.Token == "" {
		t.Error("expected non-empty root token")
	}
}
