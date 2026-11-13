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

package rest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestUnauthorizedNamesUnauthenticated: a 401 (here the server's refusal of a
// caller with no client certificate) reaches the caller reading
// "unauthenticated", the wording the gRPC, QUIC, MCP and Unix clients give
// the same refusal, inside a RequestError that keeps the status and the
// server's reason code.
func TestUnauthorizedNamesUnauthenticated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/health" {
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(transport.ErrorResponse{Error: "client certificate required", Code: "no_peer_cert"})
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.Status(context.Background())
	if err == nil {
		t.Fatal("a 401 reached the caller as success")
	}
	if !strings.Contains(err.Error(), "unauthenticated: client certificate required") {
		t.Errorf("error = %q, want it to name the refusal unauthenticated with the server's message", err)
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) || reqErr.StatusCode != http.StatusUnauthorized || reqErr.Reason != "no_peer_cert" {
		t.Errorf("the RequestError around it must keep the status and reason; got %v", err)
	}
}

// TestOnlyUnauthorizedIsPrefixed: the wording is keyed on 401 alone; a 403
// is a refused permission, not a missing credential.
func TestOnlyUnauthorizedIsPrefixed(t *testing.T) {
	if msg := serverErrorFor(http.StatusForbidden, "boom").Error(); msg != "boom" {
		t.Errorf("a 403 reads %q, want the server's message unprefixed", msg)
	}
}
