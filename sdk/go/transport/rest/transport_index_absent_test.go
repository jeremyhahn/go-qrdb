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
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestIndexQueryScan_404IsIndexNotFound pins that the server's 404 on an
// index query or scan reaches the caller as the typed IndexNotFoundError,
// naming the index the caller asked for.
func TestIndexQueryScan_404IsIndexNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"service: index not found: by_email"}`))
	}))
	defer srv.Close()
	tr := newWiredTransport(srv)

	calls := map[string]func() error{
		"query": func() error {
			_, err := tr.IndexQuery(t.Context(), "by_email", "x")
			return err
		},
		"scan": func() error {
			_, err := tr.IndexScan(t.Context(), "by_email", nil, nil)
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			var inf *transport.IndexNotFoundError
			if !errors.As(err, &inf) {
				t.Fatalf("expected *transport.IndexNotFoundError, got %T: %v", err, err)
			}
			if inf.Name != "by_email" {
				t.Fatalf("Name = %q, want %q", inf.Name, "by_email")
			}
		})
	}
}

// TestIndexQuery_500IsNotIndexNotFound pins that a server fault is not
// reported as a missing index.
func TestIndexQuery_500IsNotIndexNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	_, err := newWiredTransport(srv).IndexQuery(t.Context(), "by_email", "x")
	var inf *transport.IndexNotFoundError
	if err == nil || errors.As(err, &inf) {
		t.Fatalf("expected a non-IndexNotFoundError failure, got %T: %v", err, err)
	}
}
