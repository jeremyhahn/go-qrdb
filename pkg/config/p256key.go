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

package config

import (
	"crypto/ecdh"
	"encoding/hex"
)

// validateP256PublicKeyHex checks that value, when set, is the hex encoding of a
// valid uncompressed P-256 point -- the form a first node publishes its
// FIPS-approved bootstrap responder key in. An empty value is valid: the key is
// optional and its presence is what selects the suite. The check is on the
// curve, not just the length, so a mistyped key is refused at config load
// instead of surfacing as a handshake failure against a live peer.
func validateP256PublicKeyHex(field, value string) error {
	if value == "" {
		return nil
	}
	raw, err := hex.DecodeString(value)
	if err != nil {
		return &ValidationError{Field: field, Reason: "must be hex-encoded"}
	}
	if _, err := ecdh.P256().NewPublicKey(raw); err != nil {
		return &ValidationError{Field: field, Reason: "must be an uncompressed P-256 public key (65 bytes, 130 hex characters) that lies on the curve"}
	}
	return nil
}
