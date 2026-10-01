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

package ceremony

import "context"

// inProcessClaimKey marks a context whose share claim arrives in process.
type inProcessClaimKey struct{}

// WithInProcessClaim marks ctx as carrying a share claim made in process --
// by the embedded transport -- where no TLS connection exists to present the
// holder's certificate. Such a claim is bound to the holder's enrolled
// certificate and released only for a signature over the fresh claim nonce
// made with that certificate's key: the proof of possession the mtls quorum
// factor verifies. Only the embedded transport sets it; a network transport
// never does, and keeps requiring the certificate on the connection.
func WithInProcessClaim(ctx context.Context) context.Context {
	return context.WithValue(ctx, inProcessClaimKey{}, true)
}

// claimCertificate is the certificate thumbprint a claim is bound to: the one
// presented on the connection, or -- for an in-process claim presenting none
// -- the holder's enrolled certificate. A presented certificate is always
// checked, in process or not; the binding never replaces it. completeClaim
// then requires the nonce signature by that certificate's key either way.
func claimCertificate(ctx context.Context, presented, enrolled string) string {
	if presented == "" && InProcessClaim(ctx) {
		return enrolled
	}
	return presented
}

// InProcessClaim reports whether ctx was marked by WithInProcessClaim.
func InProcessClaim(ctx context.Context) bool {
	v, _ := ctx.Value(inProcessClaimKey{}).(bool)
	return v
}
