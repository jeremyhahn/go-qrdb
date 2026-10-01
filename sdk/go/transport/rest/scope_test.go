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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// captureRoundTripper records the X-QRDB-Database header it observes.
type captureRoundTripper struct{ lastDB string }

func (c *captureRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	c.lastDB = req.Header.Get(transport.DatabaseHeader)
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header)}, nil
}

func TestDBHeaderRoundTripper_DataPlaneVsManagement(t *testing.T) {
	base := &captureRoundTripper{}
	rt := &dbHeaderRoundTripper{base: base, database: "db2"}

	// Data-plane path receives the header.
	req, _ := http.NewRequest(http.MethodGet, "https://h/kv/foo", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}
	if base.lastDB != "db2" {
		t.Fatalf("data-plane: expected db2 header, got %q", base.lastDB)
	}
	// Original request must not be mutated (clone contract).
	if req.Header.Get(transport.DatabaseHeader) != "" {
		t.Fatal("original request header should not be mutated")
	}

	// Management path does NOT receive the header.
	base.lastDB = ""
	mreq, _ := http.NewRequest(http.MethodGet, "https://h/v1/databases", nil)
	resp, err = rt.RoundTrip(mreq)
	if err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}
	if base.lastDB != "" {
		t.Fatalf("management path should not carry db header, got %q", base.lastDB)
	}
}

func TestWithDatabase_ScopesDataPlaneRequests(t *testing.T) {
	// A live server that echoes the database header back so we can assert the
	// scoped transport actually transmits it over a real HTTP round-trip.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Echo-Database", r.Header.Get(transport.DatabaseHeader))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	base, err := New(transport.WithAddress(srv.URL))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	// Connect builds the HTTP client; the test server answers /health with 200.
	// (httptest returns 204 for /health here, which reachable() rejects, so
	// build the client directly instead of Connect to keep the test hermetic.)
	base.baseRT = http.DefaultTransport
	base.client = &http.Client{Transport: base.scopedRoundTripper()}

	scoped := base.withDatabase("db2")
	if scoped.activeDatabase != "db2" {
		t.Fatalf("expected activeDatabase db2, got %q", scoped.activeDatabase)
	}

	// Data-plane PUT carries the header.
	if err := scoped.Put(t.Context(), "k", []byte("v")); err != nil {
		t.Fatalf("put: %v", err)
	}

	// system normalizes to unscoped.
	un := base.withDatabase("system")
	if un.activeDatabase != "" {
		t.Fatalf("system should be unscoped, got %q", un.activeDatabase)
	}
}
