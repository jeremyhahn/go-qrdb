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

package mcp

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// TestWithHTTPRequestCredentials_CarriesDPoP verifies the MCP HTTP endpoint
// hands the DPoP proof, method and target URI to authenticators.
func TestWithHTTPRequestCredentials_CarriesDPoP(t *testing.T) {
	r := httptest.NewRequest("POST", "http://db.example.com/mcp", nil)
	r.Header.Set("DPoP", "proof-m")
	got, ok := auth.DPoPRequestFromContext(withHTTPRequestCredentials(context.Background(), r))
	want := auth.DPoPRequest{Proof: "proof-m", Method: "POST", URI: "http://db.example.com/mcp"}
	if !ok || got != want {
		t.Fatalf("got %+v, %v; want %+v", got, ok, want)
	}
}

// TestWithHTTPRequestCredentials_NoDPoP verifies absence and a nil request.
func TestWithHTTPRequestCredentials_NoDPoP(t *testing.T) {
	r := httptest.NewRequest("POST", "http://db.example.com/mcp", nil)
	if _, ok := auth.DPoPRequestFromContext(withHTTPRequestCredentials(context.Background(), r)); ok {
		t.Fatal("DPoP carried without a header")
	}
	if _, ok := auth.DPoPRequestFromContext(withHTTPRequestCredentials(context.Background(), nil)); ok {
		t.Fatal("DPoP carried for a nil request")
	}
}
