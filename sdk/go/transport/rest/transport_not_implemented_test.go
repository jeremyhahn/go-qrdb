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
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestEveryFiveOhOneIsTheTypedCapabilityRefusal: a 501 from ANY REST method —
// not only the guarded verbs — reaches the caller as
// transport.CapabilityUnsupportedError, inside a RequestError that keeps the
// method and status (go-qrdb#557 retired the go-qrdb#126 exclusion).
func TestEveryFiveOhOneIsTheTypedCapabilityRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotImplemented)
		_ = json.NewEncoder(w).Encode(transport.ErrorResponse{Error: "service: statemachine: mvcc not configured"})
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.Status(context.Background())
	var unsupported *transport.CapabilityUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("a 501 reached the caller as %T (%v), want CapabilityUnsupportedError", err, err)
	}
	if unsupported.Message != "service: statemachine: mvcc not configured" {
		t.Errorf("message = %q, want the server's account of what is missing", unsupported.Message)
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) || reqErr.StatusCode != http.StatusNotImplemented {
		t.Errorf("the RequestError around it must keep the status; got %v", err)
	}
}

// TestOtherStatusesStayUntyped: the typing is keyed on 501 alone. A 500 is a
// fault, not a refusal, and must not be promoted.
func TestOtherStatusesStayUntyped(t *testing.T) {
	var unsupported *transport.CapabilityUnsupportedError
	if errors.As(serverErrorFor(http.StatusInternalServerError, "boom"), &unsupported) {
		t.Fatal("a 500 was typed as a capability refusal")
	}
}
