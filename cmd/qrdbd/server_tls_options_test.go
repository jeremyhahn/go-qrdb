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

package main

import "testing"

// TestServerTLSOptions_WiredPoolAddsTheVerifier: a bundle whose client trust
// pool is wired hands every server it serves the pool as its peer-chain
// verifier, beside the TLS configuration and the CA pool.
func TestServerTLSOptions_WiredPoolAddsTheVerifier(t *testing.T) {
	bundle := testBundle(t)
	if bundle.trustPool == nil {
		t.Fatal("testBundle did not wire the client trust pool")
	}
	if got := len(bundle.serverTLSOptions()); got != 3 {
		t.Fatalf("%d server options, want TLS, CA pool and the peer-chain verifier", got)
	}
}

// TestServerTLSOptions_UnwiredPoolAddsNoVerifier: a bundle with no client
// trust pool hands no peer-chain verifier, rather than a nil pool wrapped as
// a non-nil verifier that every transport would then call.
func TestServerTLSOptions_UnwiredPoolAddsNoVerifier(t *testing.T) {
	bundle, err := generateCATLS()
	if err != nil {
		t.Fatalf("generateCATLS: %v", err)
	}
	if bundle.trustPool != nil {
		t.Fatal("generateCATLS wired a client trust pool")
	}
	if got := len(bundle.serverTLSOptions()); got != 2 {
		t.Fatalf("%d server options, want TLS and CA pool alone", got)
	}
}
