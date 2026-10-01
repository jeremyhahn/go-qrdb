// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"context"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// Unseal-key indirection (Vault-style).
//
// In Shamir direct mode the secret split to the Security Officers is NOT the
// barrier root key itself. Instead a random 256-bit unseal key (UK) is
// generated, the root key is AEAD-wrapped under the UK, and the wrapped blob
// is persisted as a sibling of the sealed root key envelope. The UK — not the
// root key — is what gets split into M-of-N shares and sealed to the
// officers' ceremony enclaves.
//
// This indirection exists so that Rekey can cryptographically revoke every
// previously distributed share, including shares already claimed by
// officers: rekeying generates a fresh UK and re-wraps the root key under
// it, after which shares from any prior round reconstruct a UK that no
// longer decrypts the wrapped blob. The root key itself never rotates (the
// EpochBarrier derives per-epoch DEKs from it via a KDF, so rotating it
// would orphan every epoch), and no stored data is re-encrypted.
//
// The authenticated decrypt of the wrapped blob also provides the
// out-of-band secret validation that shamir.CombineUnverified requires
// when shares carry no integrity digest: garbage or stale shares produce a UK whose
// AEAD open fails, surfacing ErrUnsealKeyMismatch instead of silently
// unsealing with a wrong key.
//
// # Holder roster binding
//
// The blob also carries the holder roster (see holders.go): the public list of
// who holds which share and which factors each may authenticate with. The
// roster's SHA-256 digest is part of the AEAD associated data, so the roster is
// readable while sealed and any edit to it fails the unwrap. There is no
// blob-less "legacy direct mode" any more: a Shamir barrier's key is always
// held under unseal-key indirection, and a barrier whose blob predates the
// roster answers ErrHolderRosterMissing.
//
// # Domains
//
// Every barrier has an unseal-key domain (<RootKeyPath>.wrapped) and, once
// recovery keys are generated, an independent recovery-key domain
// (<RootKeyPath>.recovery.wrapped). Each wraps the same root key under its
// own key and carries its own roster; the domain is bound into the AAD, so a
// blob cannot be moved from one to the other.
//
// # Nonce framing
//
// The envelope carries the AEAD nonce in its own field, beside the
// ciphertext. That predates go-quicraft v0.7.0-alpha, which moved pkg/seal to
// cipher.NewGCMWithRandomNonce -- the only GCM construction approved under
// GODEBUG=fips140=only, since SP 800-38D requires the module itself to own
// nonce generation. The AEAD that package now hands back samples its own
// nonce, reports NonceSize() == 0, and returns nonce ‖ ciphertext ‖ tag.
//
// This code keeps writing the two fields, splitting the module-generated
// nonce off the front of the sealed output and re-joining it before Open, so
// the persisted bytes are unchanged in both directions. Nothing about the
// envelope needed to change and nothing about it did: a wrapped root key is
// written once at initialization and read on every unseal for the life of the
// barrier, and it is the one blob whose loss costs the store, so a format
// change here would need a migration run while the barrier is still sealed --
// that is, while the key material needed to authenticate the migration is by
// definition unavailable. pkg/ceremony's sealEnvelope splits the same prefix
// for the same reason.
//
// The nonce field is validated against the WIRE width the envelope's own
// algorithm declares (SymmetricAlgorithm.NonceSize, which stayed 12), never
// against cipher.AEAD.NonceSize, which is now zero and would accept a nonce
// field of any length at all.

const (
	// wrappedRootKeyPathSuffix is appended to the configured RootKeyPath to
	// form the storage key for the unseal-key domain's wrapped blob. Like
	// metadataPathSuffix it must be a SIBLING of the root key (a "." suffix,
	// never a "/" child) so filesystem-backed KV stores can represent it.
	wrappedRootKeyPathSuffix = ".wrapped"

	// recoveryWrappedPathSuffix is the recovery-key domain's sibling suffix.
	recoveryWrappedPathSuffix = ".recovery.wrapped"

	// wrappedRootKeyVersion is the current wrapped-blob format version. 2
	// added the holder roster and its digest in the associated data.
	wrappedRootKeyVersion = 2

	// wrappedRootKeyAADPrefix is the associated-data domain separator for the
	// root-key wrap. RootKeyPath, the domain and the roster digest follow, so
	// a wrapped blob cannot be transplanted between barriers, between domains,
	// or onto a different roster.
	wrappedRootKeyAADPrefix = "go-qrdb/seal/wrapped-root-key/v2|"

	// unsealKeySize is the size in bytes of a generated unseal or recovery
	// key (AES-256-GCM key size).
	unsealKeySize = 32
)

// wrappedRootKey is the persisted JSON envelope holding the barrier root key
// AEAD-encrypted under a domain key, plus that domain's holder roster. It
// contains no plaintext key material and no secret.
type wrappedRootKey struct {
	Version    int             `json:"version"`
	Algorithm  byte            `json:"algorithm"`
	Nonce      []byte          `json:"nonce"`
	Ciphertext []byte          `json:"ciphertext"`
	Roster     json.RawMessage `json:"roster"`
}

// wrappedKeyPath returns the storage key for domain's wrapped blob.
func (b *StorageBarrier) wrappedKeyPath(domain QuorumDomain) string {
	if domain == DomainRecovery {
		return b.config.RootKeyPath + recoveryWrappedPathSuffix
	}
	return b.config.RootKeyPath + wrappedRootKeyPathSuffix
}

// wrappedKeyAAD returns the associated data binding a wrapped blob to this
// barrier's root key path, its domain and its roster digest.
func (b *StorageBarrier) wrappedKeyAAD(domain QuorumDomain, rosterDigest []byte) []byte {
	return []byte(wrappedRootKeyAADPrefix + b.config.RootKeyPath + "|" + string(domain) + "|" + hex.EncodeToString(rosterDigest))
}

// unsealKeyAEAD constructs the AEAD used to wrap and unwrap the root key
// under a domain key, honoring an injected AEADProvider when configured.
func (b *StorageBarrier) unsealKeyAEAD(unsealKey []byte) (cipher.AEAD, error) {
	provider := b.config.AEADProvider
	if provider == nil {
		provider = NewSoftwareAEADProvider()
	}
	return provider.NewAEAD(AlgAES256GCM, unsealKey)
}

// newUnsealKey returns a fresh random 256-bit domain key, generated straight
// into protected memory. The caller owns the buffer and must destroy it.
func newUnsealKey() (*secure.Buffer, error) {
	key, err := secure.NewBuffer(unsealKeySize)
	if err != nil {
		return nil, err
	}
	if err := key.WithBytes(func(k []byte) error {
		_, rerr := io.ReadFull(rand.Reader, k)
		return rerr
	}); err != nil {
		key.Destroy()
		return nil, err
	}
	return key, nil
}

// wrapRootKey AEAD-encrypts rootKey under key for domain, binding the given
// roster, and returns the serialized blob ready for persistence together with
// the roster digest. Neither key slice is wiped; the caller owns both.
//
// The AEAD samples its own nonce and prepends it to the sealed output (see
// the Nonce framing note above); the prefix is split back off so the envelope
// keeps carrying it as its own field.
func (b *StorageBarrier) wrapRootKey(domain QuorumDomain, key, rootKey []byte, roster *HolderRoster) ([]byte, []byte, error) {
	if roster == nil || roster.Domain != domain {
		return nil, nil, &InvalidRosterError{Reason: "roster belongs to another domain"}
	}
	aead, err := b.unsealKeyAEAD(key)
	if err != nil {
		return nil, nil, err
	}
	return b.wrapRootKeyWith(domain, aead, rootKey, roster)
}

// wrapRootKeyWith is wrapRootKey under an AEAD already built from the domain
// key, so the key can stay in protected memory while the root key is
// borrowed.
func (b *StorageBarrier) wrapRootKeyWith(domain QuorumDomain, aead cipher.AEAD, rootKey []byte, roster *HolderRoster) ([]byte, []byte, error) {
	if roster == nil || roster.Domain != domain {
		return nil, nil, &InvalidRosterError{Reason: "roster belongs to another domain"}
	}
	rosterRaw, digest, err := encodeRoster(roster)
	if err != nil {
		return nil, nil, err
	}

	wireLen := AlgAES256GCM.NonceSize()
	nonce, ciphertext, err := sealWrapped(aead, wireLen, rootKey, b.wrappedKeyAAD(domain, digest))
	if err != nil {
		return nil, nil, err
	}

	wrapped := wrappedRootKey{
		Version:    wrappedRootKeyVersion,
		Algorithm:  byte(AlgAES256GCM),
		Nonce:      nonce,
		Ciphertext: ciphertext,
		Roster:     rosterRaw,
	}
	blob, err := json.Marshal(&wrapped)
	if err != nil {
		return nil, nil, err
	}
	return blob, digest, nil
}

// sealWrapped seals plaintext and returns the envelope's two fields, honouring
// EITHER of the nonce conventions an AEADProvider is allowed to follow.
//
// BarrierConfig.AEADProvider is an injectable extension point — go-qrdb
// re-exports it and go-xkms re-exports it again for hardware-backed providers —
// and go-quicraft's AEADProvider contract admits two kinds of AEAD, which
// declare themselves through NonceSize():
//
//   - NonceSize() > 0: a caller-nonce AEAD. The caller draws the nonce and
//     passes it to Seal; the sealed output carries no nonce prefix.
//   - NonceSize() == 0: a module-nonce AEAD (cipher.NewGCMWithRandomNonce). It
//     samples its own nonce, prepends it to the Seal output, and PANICS if
//     handed a nonce at all.
//
// Both produce the same envelope, because the module-nonce prefix is exactly
// the bytes the caller-nonce convention stores in the Nonce field. Branching
// here rather than assuming one convention is what keeps an injected
// caller-nonce provider working: passing it a nil nonce would panic, and
// hard-coding a drawn nonce would panic on the module-nonce AEAD that
// go-quicraft v0.7.0-alpha made the default.
func sealWrapped(aead cipher.AEAD, wireLen int, plaintext, aad []byte) (nonce, ciphertext []byte, err error) {
	if n := aead.NonceSize(); n > 0 {
		// Caller-nonce provider. Its nonce width must match the width the
		// envelope records for its algorithm, or the blob it produces could not
		// be read back; fail loudly rather than persisting an unreadable one.
		if n != wireLen {
			return nil, nil, ErrWrappedRootKeyCorrupt
		}
		nonce = make([]byte, n)
		if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
			return nil, nil, err
		}
		return nonce, aead.Seal(nil, nonce, plaintext, aad), nil
	}

	// Module-nonce provider: it samples the nonce and prepends it, so the
	// prefix is split back off into the envelope's own field.
	// #nosec G407 -- the nonce argument MUST be nil here: this AEAD is cipher.NewGCMWithRandomNonce, which generates a fresh 96-bit nonce inside the FIPS module on every Seal and prepends it to the ciphertext. gosec reads the nil as a hardcoded IV; passing anything else makes Seal panic.
	sealed := aead.Seal(nil, nil, plaintext, aad)
	if len(sealed) < wireLen {
		return nil, nil, ErrWrappedRootKeyCorrupt
	}
	return sealed[:wireLen:wireLen], sealed[wireLen:], nil
}

// unwrapRootKey validates a decoded wrapped-root-key envelope and recovers the
// root key from it under unsealKey, authenticating the roster digest and
// domain through the associated data. The returned buffer is protected memory
// the caller must destroy.
//
// A malformed envelope is ErrWrappedRootKeyCorrupt; an envelope that is
// well-formed but does not open under unsealKey is ErrUnsealKeyMismatch, which
// is the signal resolveDomainKey turns into "these shares are wrong or were
// revoked by a prior rekey".
func (b *StorageBarrier) unwrapRootKey(domain QuorumDomain, wrapped *wrappedRootKey, rosterDigest, unsealKey []byte) (*secure.Buffer, error) {
	if wrapped.Version != wrappedRootKeyVersion ||
		SymmetricAlgorithm(wrapped.Algorithm) != AlgAES256GCM {
		return nil, ErrWrappedRootKeyCorrupt
	}

	// The AEAD is constructed BEFORE the nonce field is validated, which is
	// the order resolveDirectSecret used before this code was extracted. It
	// looks redundant -- the nonce check needs nothing from the AEAD -- but it
	// fixes which error an input that fails BOTH checks reports. A wrong-length
	// secret against a blob whose nonce field is also malformed answered
	// ErrUnsealKeyMismatch before and must keep doing so: the two errors mean
	// different things to a recovery operator ("your shares are wrong" versus
	// "the stored blob is damaged"), and silently swapping them during a
	// refactor would send one down the wrong path at the worst moment.
	aead, err := b.unsealKeyAEAD(unsealKey)
	if err != nil {
		// Wrong-length secret (e.g. a legacy root-key share set submitted
		// against an upgraded barrier) cannot be a valid unseal key.
		return nil, ErrUnsealKeyMismatch
	}

	// The WIRE nonce width the envelope's algorithm declares -- deliberately
	// not aead.NonceSize(), which is zero for this construction and would
	// accept a nonce field of any length.
	if len(wrapped.Nonce) != SymmetricAlgorithm(wrapped.Algorithm).NonceSize() {
		return nil, ErrWrappedRootKeyCorrupt
	}

	rootKey, err := openWrapped(aead, wrapped.Nonce, wrapped.Ciphertext, b.wrappedKeyAAD(domain, rosterDigest))
	if err != nil {
		return nil, err
	}
	return rootKey, nil
}

// openWrapped is sealWrapped's inverse and honours the same two conventions
// (see sealWrapped for why both must be supported). The stored envelope is
// identical either way, so which branch runs depends only on the AEAD in hand,
// never on how the blob was written.
//
// The plaintext is the barrier root key, so it is decrypted straight into
// protected memory: the destination handed to Open is the buffer's own pages,
// sized exactly, so Open appends in place and no ordinary heap allocation ever
// holds the key. That is asserted rather than assumed -- an AEAD that returned
// a slice it allocated itself would put the key on the heap, and that is
// refused and wiped rather than accepted. The caller owns the buffer and must
// destroy it.
//
// Every authentication failure is ErrUnsealKeyMismatch, as it always was; a
// protected-memory failure is reported as itself.
func openWrapped(aead cipher.AEAD, nonce, ciphertext, aad []byte) (*secure.Buffer, error) {
	input, nonceArg := ciphertext, nonce
	if aead.NonceSize() == 0 {
		// Module-nonce provider: rejoin the fields into the
		// nonce ‖ ciphertext ‖ tag frame it consumes. A fresh buffer, because
		// appending onto the stored nonce would write past its length into
		// whatever backs it. The frame is ciphertext, not key material.
		frame := make([]byte, 0, len(nonce)+len(ciphertext))
		frame = append(frame, nonce...)
		frame = append(frame, ciphertext...)
		input, nonceArg = frame, nil
	}

	size := len(input) - aead.Overhead()
	if size <= 0 {
		return nil, ErrUnsealKeyMismatch
	}
	buf, err := secure.NewBuffer(size)
	if err != nil {
		return nil, err
	}
	openErr := buf.WithBytes(func(dst []byte) error {
		// #nosec G407 -- nonceArg is nil exactly when this AEAD is cipher.NewGCMWithRandomNonce, which reads the nonce from the front of the sealed frame; gosec reads the nil as a hardcoded IV, and passing anything else makes Open panic.
		out, err := aead.Open(dst[:0], nonceArg, input, aad)
		if err != nil {
			return ErrUnsealKeyMismatch
		}
		if len(out) != len(dst) || &out[0] != &dst[0] {
			secure.Wipe(out)
			return &UnprotectedDecryptError{}
		}
		return nil
	})
	if openErr != nil {
		buf.Destroy()
		return nil, openErr
	}
	return buf, nil
}

// loadWrapped reads domain's wrapped blob and its roster. It needs no key
// and works while the barrier is sealed. A missing blob, or one written before
// holder rosters existed, is ErrHolderRosterMissing.
func (b *StorageBarrier) loadWrapped(ctx context.Context, domain QuorumDomain) (*wrappedRootKey, *HolderRoster, []byte, error) {
	exists, err := b.base.Exists(ctx, b.wrappedKeyPath(domain))
	if err != nil {
		return nil, nil, nil, err
	}
	if !exists {
		return nil, nil, nil, ErrHolderRosterMissing
	}
	blob, err := b.base.Get(ctx, b.wrappedKeyPath(domain))
	if err != nil {
		return nil, nil, nil, err
	}
	var wrapped wrappedRootKey
	if err := json.Unmarshal(blob, &wrapped); err != nil {
		return nil, nil, nil, ErrWrappedRootKeyCorrupt
	}
	if wrapped.Version != wrappedRootKeyVersion || len(wrapped.Roster) == 0 {
		return nil, nil, nil, ErrHolderRosterMissing
	}
	roster, digest, err := decodeRoster(wrapped.Roster)
	if err != nil {
		return nil, nil, nil, err
	}
	if roster.Domain != domain {
		return nil, nil, nil, &InvalidRosterError{Reason: "stored roster belongs to another domain"}
	}
	return &wrapped, roster, digest, nil
}

// HolderRoster returns domain's holder roster and its digest. It reads only
// public data and works while the barrier is sealed. The roster is
// authenticated only when a quorum's key opens the blob it is bound to, so a
// caller that acts on it earlier (issuing a challenge, say) is acting on a
// claim; share submission therefore pins the digest per round and the
// completing quorum authenticates exactly that roster.
func (b *StorageBarrier) HolderRoster(ctx context.Context, domain QuorumDomain) (*HolderRoster, []byte, error) {
	if !domain.Valid() {
		return nil, nil, &QuorumOpInvalidError{Domain: domain}
	}
	_, roster, digest, err := b.loadWrapped(ctx, domain)
	return roster, digest, err
}

// resolveDomainKey converts a key reconstructed from domain's share quorum
// into the barrier root key by authenticated decrypt of domain's wrapped
// blob. A failed decrypt means the shares are wrong, were revoked by a prior
// rekey, or the roster was tampered with (ErrUnsealKeyMismatch).
//
// The root key is returned in protected memory, never an ordinary heap slice:
// the caller owns the buffer and must destroy it.
func (b *StorageBarrier) resolveDomainKey(ctx context.Context, domain QuorumDomain, key []byte) (*secure.Buffer, error) {
	wrapped, _, digest, err := b.loadWrapped(ctx, domain)
	if err != nil {
		return nil, err
	}
	return b.unwrapRootKey(domain, wrapped, digest, key)
}
