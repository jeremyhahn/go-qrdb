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

import (
	"crypto/x509"
	"time"
)

// Metadata keys carrying the on-behalf assertion and its signature on the
// non-HTTP transports: gRPC metadata (and the Unix socket, which serves the
// gRPC wire) and QUIC frame metadata. They are the HTTP headers' names in the
// lower case gRPC requires.
const (
	MetadataAssertion = "x-qrdb-on-behalf-assertion"
	MetadataSignature = "x-qrdb-on-behalf-sig"
)

// Binding is the request an assertion is bound to, on any transport. An HTTP
// request binds its method, its request URI and its body; a non-HTTP request
// binds "<protocol>:<method>" with no path and its payload. The assertion's
// field names and serialization are unchanged, so an HTTP assertion is
// byte-identical to one made before bindings were generalized.
type Binding struct {
	Method     string
	Path       string
	BodySHA256 string
}

// HTTPBinding binds an HTTP request: its method, its request URI (path and
// query) and its body.
func HTTPBinding(method, requestURI string, body []byte) Binding {
	return Binding{Method: method, Path: requestURI, BodySHA256: BodyDigest(body)}
}

// RPCBinding binds a non-HTTP request: the protocol ("grpc", "quic", ...), the
// method it names, and the payload as sent.
func RPCBinding(protocol, method string, payload []byte) Binding {
	return Binding{Method: protocol + ":" + method, BodySHA256: BodyDigest(payload)}
}

// Assertion returns the assertion that the caller (subject in tenantID) made
// the bound request, signed for by the node forwarderNodeID, valid from now
// for DefaultTTL.
func (b Binding) Assertion(subject, tenantID, forwarderNodeID string, now time.Time) CallerAssertion {
	return NewCallerAssertion(subject, tenantID, b.Method, b.Path, b.BodySHA256, forwarderNodeID, now, DefaultTTL)
}

// VerifyBinding is Verify against b.
func VerifyBinding(assertionB64, sigB64 string, peerCert *x509.Certificate, pinnedTrustDomain string, now time.Time, b Binding) (*CallerAssertion, error) {
	return Verify(assertionB64, sigB64, peerCert, pinnedTrustDomain, now, b.Method, b.Path, b.BodySHA256)
}
