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

package auth

import (
	"context"
	"crypto/tls"
	"net/http/httptest"
	"testing"
)

// TestDPoPRequest_RoundTrip verifies the carrier returns what was stored.
func TestDPoPRequest_RoundTrip(t *testing.T) {
	want := DPoPRequest{Proof: "a.b.c", Method: "POST", URI: "https://db.example.com/v1/kv"}
	got, ok := DPoPRequestFromContext(WithDPoPRequest(context.Background(), want))
	if !ok || got != want {
		t.Fatalf("DPoPRequestFromContext = %+v, %v; want %+v, true", got, ok, want)
	}
}

// TestDPoPRequest_EmptyProofNotStored verifies a request without a proof
// leaves the context unchanged and reads back absent.
func TestDPoPRequest_EmptyProofNotStored(t *testing.T) {
	base := context.Background()
	ctx := WithDPoPRequest(base, DPoPRequest{Method: "GET", URI: "http://x/"})
	if ctx != base {
		t.Fatal("a request with no proof must not allocate a new context")
	}
	if _, ok := DPoPRequestFromContext(ctx); ok {
		t.Fatal("DPoPRequestFromContext reported a request on an empty context")
	}
}

// TestWithDPoPFromHTTP_PlainRequest verifies method, scheme, host and path are
// carried and the query is dropped (RFC 9449 section 4.2).
func TestWithDPoPFromHTTP_PlainRequest(t *testing.T) {
	r := httptest.NewRequest("PUT", "http://db.example.com/v1/kv/a%20b?x=1#frag", nil)
	r.Header.Set("DPoP", "proof")
	got, ok := DPoPRequestFromContext(WithDPoPFromHTTP(context.Background(), r))
	want := DPoPRequest{Proof: "proof", Method: "PUT", URI: "http://db.example.com/v1/kv/a%20b"}
	if !ok || got != want {
		t.Fatalf("got %+v, %v; want %+v, true", got, ok, want)
	}
}

// TestWithDPoPFromHTTP_TLSIsHTTPS verifies a TLS connection yields https.
func TestWithDPoPFromHTTP_TLSIsHTTPS(t *testing.T) {
	r := httptest.NewRequest("GET", "https://db.example.com/v1/status", nil)
	r.TLS = &tls.ConnectionState{}
	r.Header.Set("DPoP", "proof")
	got, _ := DPoPRequestFromContext(WithDPoPFromHTTP(context.Background(), r))
	if got.URI != "https://db.example.com/v1/status" {
		t.Fatalf("URI = %q", got.URI)
	}
}

// TestWithDPoPFromHTTP_NoHeader verifies no header stores nothing.
func TestWithDPoPFromHTTP_NoHeader(t *testing.T) {
	r := httptest.NewRequest("GET", "http://db.example.com/", nil)
	if _, ok := DPoPRequestFromContext(WithDPoPFromHTTP(context.Background(), r)); ok {
		t.Fatal("a request without a DPoP header must carry none")
	}
}
