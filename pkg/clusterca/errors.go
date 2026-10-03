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

package clusterca

// NilProviderError is returned when a nil cakey.Provider is passed to an
// issuance function. A nil seam is refused rather than panicking or
// silently falling back to a built-in key.
type NilProviderError struct{}

// Error returns a human-readable description.
func (e *NilProviderError) Error() string {
	return "clusterca: nil cakey.Provider"
}

// ProviderError wraps a failure resolving material (the signer or the
// certificate) from the configured cakey.Provider.
type ProviderError struct {
	// Op names which call failed: "signer" or "certificate".
	Op  string
	Err error
}

// Error returns a human-readable description.
func (e *ProviderError) Error() string {
	return "clusterca: resolving the " + e.Op + " failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ProviderError) Unwrap() error {
	return e.Err
}

// WeakKeyError is returned by IssueLeafForKey when the caller-supplied
// public key (from a joiner's CSR, which this package never generates
// itself) is not strong enough for a cluster-member leaf: ECDSA weaker
// than P-256, an RSA key under 2048 bits, or any other key type Go's
// crypto/x509 does not itself classify as one of ECDSA, Ed25519 or RSA.
type WeakKeyError struct {
	// KeyType names the key's Go type, for a caller that wants to log it.
	KeyType string
}

// Error returns a human-readable description.
func (e *WeakKeyError) Error() string {
	return "clusterca: CSR public key is not strong enough for a cluster-member leaf (" + e.KeyType + ")"
}
