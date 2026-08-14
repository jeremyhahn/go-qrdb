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

package forward

// ForwardAssertionEncodingError indicates the on-behalf assertion or signature
// header could not be base64-decoded, or the assertion body could not be
// (de)serialized. It is a structural (malformed-input) failure, distinct from a
// cryptographic or authorization rejection.
type ForwardAssertionEncodingError struct {
	Field string
	Err   error
}

func (e *ForwardAssertionEncodingError) Error() string {
	return "auth/forward: malformed on-behalf assertion " + e.Field + ": " + e.Err.Error()
}

func (e *ForwardAssertionEncodingError) Unwrap() error { return e.Err }

// ForwardAssertionNotClusterPeerError indicates the mTLS peer that presented an
// on-behalf assertion is not a trusted cluster node: its certificate carries no
// valid node-scoped SPIFFE SAN (spiffe://<trust-domain>/node/<nodeID>), or the
// SAN's trust domain does not match the pinned cluster trust domain, or the
// asserted forwarder node id does not match the peer certificate. Only a
// cluster peer may speak on behalf of another caller; a user/service client
// presenting an assertion is rejected fail-closed.
type ForwardAssertionNotClusterPeerError struct {
	Err error
}

func (e *ForwardAssertionNotClusterPeerError) Error() string {
	if e.Err != nil {
		return "auth/forward: on-behalf assertion peer is not a cluster node: " + e.Err.Error()
	}
	return "auth/forward: on-behalf assertion peer is not a cluster node"
}

func (e *ForwardAssertionNotClusterPeerError) Unwrap() error { return e.Err }

// ForwardAssertionSignatureError indicates the assertion signature did not
// verify against the mTLS peer certificate's public key (wrong signer, tampered
// assertion, or an unsupported key type). The assertion must be rejected.
type ForwardAssertionSignatureError struct {
	Reason string
}

func (e *ForwardAssertionSignatureError) Error() string {
	if e.Reason != "" {
		return "auth/forward: on-behalf assertion signature invalid: " + e.Reason
	}
	return "auth/forward: on-behalf assertion signature invalid"
}

// ForwardAssertionExpiredError indicates the assertion is outside its validity
// window: either already expired or issued in the future beyond the allowed
// clock-skew tolerance. Bounds the replay window to the assertion TTL.
type ForwardAssertionExpiredError struct {
	Reason string
}

func (e *ForwardAssertionExpiredError) Error() string {
	return "auth/forward: on-behalf assertion outside validity window: " + e.Reason
}

// ForwardAssertionBindingMismatchError indicates the assertion is not bound to
// the actual request: the HTTP method, request path, or request-body SHA-256
// digest in the assertion does not match the request being authorized. This
// prevents replaying a captured assertion against a different operation or body.
type ForwardAssertionBindingMismatchError struct {
	Field string
}

func (e *ForwardAssertionBindingMismatchError) Error() string {
	return "auth/forward: on-behalf assertion not bound to request (" + e.Field + " mismatch)"
}

// ForwardAssertionFieldError indicates a required assertion field (Subject,
// issued/expiry timestamps) is missing or malformed after decoding.
type ForwardAssertionFieldError struct {
	Field string
}

func (e *ForwardAssertionFieldError) Error() string {
	return "auth/forward: on-behalf assertion field invalid: " + e.Field
}

// ForwardSigningError indicates the assertion could not be signed on the
// forwarding node (serialization failure or signer error).
type ForwardSigningError struct {
	Err error
}

func (e *ForwardSigningError) Error() string {
	return "auth/forward: failed to sign on-behalf assertion: " + e.Err.Error()
}

func (e *ForwardSigningError) Unwrap() error { return e.Err }
