// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package uuidv8 derives name-based UUIDs from SHA-256 in the RFC 9562
// version 8 (custom) layout.
//
// It replaces uuid.NewSHA1 (RFC 9562 version 5), which is defined to hash with
// SHA-1 and therefore panics under GODEBUG=fips140=only. SHA-1 is not an
// approved algorithm, and the FIPS gate rejects it wherever it appears — a
// name-based identifier is no exception, so the derivation moves to SHA-256
// rather than being exempted.
//
// The derivation is stable: the same namespace and name always produce the
// same UUID, which is what callers rely on for cluster-wide identifiers that
// no node has to coordinate. It is NOT compatible with the version 5 values
// the same inputs produced before — see the package's callers for the
// identifiers this affects.
package uuidv8

import (
	"crypto/sha256"

	"github.com/google/uuid"
)

// FromName returns the deterministic version 8 UUID for name within namespace.
//
// The digest is SHA-256(namespace || name); its first 16 bytes become the
// UUID, with the version and variant fields overwritten as RFC 9562 §5.8
// requires. Every other bit is digest output.
func FromName(namespace uuid.UUID, name string) uuid.UUID {
	h := sha256.New()
	h.Write(namespace[:])
	h.Write([]byte(name))
	sum := h.Sum(nil)

	var out uuid.UUID
	copy(out[:], sum[:len(out)])
	out[6] = (out[6] & 0x0f) | 0x80 // version 8
	out[8] = (out[8] & 0x3f) | 0x80 // RFC 9562 variant
	return out
}
