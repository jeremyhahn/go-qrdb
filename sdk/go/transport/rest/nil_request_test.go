// Copyright 2026 Jeremy Hahn
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

package rest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestNilRequest_RefusedBeforeTheWire pins that every call taking a request
// pointer it dereferences for its path refuses a nil one with a RequestError
// wrapping NilRequestError, and sends nothing to the server.
func TestNilRequest_RefusedBeforeTheWire(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/health" {
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		calls.Add(1)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	ctx := context.Background()
	cases := map[string]func() error{
		"UpdatePool": func() error { _, err := rt.UpdatePool(ctx, nil); return err },
		"ShareClaimBegin": func() error {
			_, err := rt.ShareClaimBegin(ctx, nil)
			return err
		},
		"ShareClaimComplete": func() error {
			_, err := rt.ShareClaimComplete(ctx, nil)
			return err
		},
		"RecoveryShareClaimBegin": func() error {
			_, err := rt.RecoveryShareClaimBegin(ctx, nil)
			return err
		},
		"RecoveryShareClaimComplete": func() error {
			_, err := rt.RecoveryShareClaimComplete(ctx, nil)
			return err
		},
	}
	for method, call := range cases {
		t.Run(method, func(t *testing.T) {
			err := call()
			var nre *transport.NilRequestError
			var re *transport.RequestError
			if !errors.As(err, &re) || re.Method != method || !errors.As(err, &nre) {
				t.Fatalf("%s(nil) = %T %v, want RequestError wrapping NilRequestError", method, err, err)
			}
		})
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("a nil request reached the server %d time(s)", n)
	}
}

// TestNilRequest_NonNilStillReachesTheServer pins that the guard refuses only
// nil: a populated request is sent.
func TestNilRequest_NonNilStillReachesTheServer(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/health" {
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		calls.Add(1)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	if _, err := rt.UpdatePool(context.Background(), &transport.UpdateVolumePoolRequest{Name: "p"}); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	if _, err := rt.ShareClaimBegin(context.Background(), &transport.ShareClaimBeginRequest{Username: "alice"}); err != nil {
		t.Fatalf("ShareClaimBegin: %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("server saw %d calls, want 2", n)
	}
}
