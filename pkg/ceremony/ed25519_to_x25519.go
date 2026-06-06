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

package ceremony

import (
	"crypto/ed25519"

	"filippo.io/edwards25519"
)

// convertEd25519ToX25519 converts an Ed25519 public key to its
// corresponding X25519 public key using the canonical birational map
// u = (1 + y) / (1 - y) on the underlying twisted Edwards / Montgomery
// curve pair (Curve25519). The conversion is exact and deterministic.
//
// Returns ErrInvalidPubKeyPEM if the input does not decode as a valid
// Edwards25519 point.
func convertEd25519ToX25519(pub ed25519.PublicKey) ([]byte, error) {
	if len(pub) != ed25519.PublicKeySize {
		return nil, ErrUnsupportedKeyAlgorithm
	}
	pt, err := new(edwards25519.Point).SetBytes(pub)
	if err != nil {
		return nil, ErrInvalidPubKeyPEM
	}
	// Point.BytesMontgomery returns the X25519 (Montgomery) public key.
	return pt.BytesMontgomery(), nil
}
